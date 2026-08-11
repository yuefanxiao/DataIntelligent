package semantic

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Embedder 是 embedding 生成的接口（ADR-0002：外部 OpenAI text-embedding-3
// 系列，同步期写入向量；失败降级不阻塞同步）。
//
// v1 生产实现 = OpenAIEmbedder（OpenAI API）；测试注入 fake。检索消费
// （sqlite-vec / 暴力余弦）在 08 票，本包只保证「向量已写入」。
type Embedder interface {
	// Embed 返回输入文本的向量（float32 序列，text-embedding-3-small 为
	// 1536 维；large 为 3072 维）。
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// OpenAIEmbedder 调 OpenAI embeddings API 生成向量。
type OpenAIEmbedder struct {
	apiKey string
	model  string
	client *http.Client
	// 每批文本数上限（API 单请求上限；小批量避免超时）。
	batchSize int
}

// DefaultEmbeddingModel 是 v1 默认 embedding 模型：text-embedding-3-small
// （OpenAI 真实模型 id，1536 维；spec §4.9 参数表写的「text-embedding-3」
// 是系列名简写，不是可调用的模型 id）。
const DefaultEmbeddingModel = "text-embedding-3-small"

// DefaultVectorDim 是 DefaultEmbeddingModel 的向量维度（vec0 索引的初始
// 维度，与 store 包 schema 一致；模型切换由 EnsureVecIndex 检测重建）。
const DefaultVectorDim = 1536

// NewOpenAIEmbedder 构造 OpenAI embedding 客户端（env DGW_OPENAI_API_KEY）。
// model 缺省 DefaultEmbeddingModel（spec §4.9 参数表「env 可覆盖」）。
func NewOpenAIEmbedder(apiKey, model string) *OpenAIEmbedder {
	if model == "" {
		model = DefaultEmbeddingModel
	}
	return &OpenAIEmbedder{
		apiKey:    apiKey,
		model:     model,
		client:    &http.Client{Timeout: 30 * time.Second},
		batchSize: 100,
	}
}

const openAIEmbeddingsURL = "https://api.openai.com/v1/embeddings"

// Embed 分批发请求；单个批次失败重试一次（网络抖动自愈），仍失败返回
// 错误——调用方降级不阻塞。
func (o *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += o.batchSize {
		end := min(start+o.batchSize, len(texts))
		vecs, err := o.embedBatch(ctx, texts[start:end])
		if err != nil {
			vecs, err = o.embedBatch(ctx, texts[start:end]) // 重试一次
			if err != nil {
				return nil, err
			}
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (o *OpenAIEmbedder) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{
		"model": o.model,
		"input": texts,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIEmbeddingsURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("OpenAI embeddings HTTP %d: %s", resp.StatusCode, string(b))
	}
	var parsed struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode embeddings response: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings 返回 %d 条，期望 %d", len(parsed.Data), len(texts))
	}
	out := make([][]float32, len(parsed.Data))
	for i, d := range parsed.Data {
		out[i] = d.Embedding
	}
	return out, nil
}

// embedEntity 是同步期对单个实体生成向量的文本输入（检索语义用，08 票消费）。
// 文本 = 类型 + FQN + 描述 + 关键属性：语义匹配的素材面。
func embedEntity(e Entity) string {
	s := string(e.Kind) + " " + e.FQN
	if e.Description != "" {
		s += " " + e.Description
	}
	if e.Name != "" && e.Name != e.FQN {
		s += " " + e.Name
	}
	switch e.Kind {
	case KindMetric:
		if e.Expression != "" {
			s += " " + e.Expression
		}
	case KindColumn:
		if e.DataType != "" {
			s += " type:" + e.DataType
		}
	}
	return s
}

// SaveEmbeddings 把实体向量写入 dgw_sem_embeddings（幂等 upsert）并同步
// 维护 vec0 检索索引（08 票，ADR-0005：双写，vec0 是 KNN 索引面）。vec0
// 不支持 UPSERT/REPLACE（虚拟表限制），更新 = 先删后插。维度与索引不符
// （模型切换）由 EnsureVecIndex 重建。双写在单事务内（review 修复：
// 崩溃不留半态漂移窗口）。失败返回错误——调用方按「降级不阻塞」处理
// （与 embedding 生成失败同一契约）。
func SaveEmbeddings(ctx context.Context, st DBer, model string, fqns []string, vecs [][]float32) error {
	if len(fqns) != len(vecs) {
		return fmt.Errorf("SaveEmbeddings: %d 个 FQN vs %d 个向量", len(fqns), len(vecs))
	}
	if len(vecs) > 0 {
		if err := EnsureVecIndex(ctx, st, len(vecs[0])); err != nil {
			return err
		}
	}
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	for i, fqn := range fqns {
		buf := encodeFloats(vecs[i])
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO dgw_sem_embeddings (entity_fqn, model, vector)
			VALUES (?, ?, ?)
			ON CONFLICT(entity_fqn) DO UPDATE SET
				model = excluded.model, vector = excluded.vector,
				updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')`,
			fqn, model, buf); err != nil {
			return fmt.Errorf("save embedding %s: %w", fqn, err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM dgw_sem_vec WHERE entity_fqn = ?`, fqn); err != nil {
			return fmt.Errorf("delete vec0 row %s: %w", fqn, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO dgw_sem_vec (entity_fqn, vector) VALUES (?, ?)`,
			fqn, buf); err != nil {
			return fmt.Errorf("save vec0 row %s: %w", fqn, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit embeddings: %w", err)
	}
	return nil
}

// EnsureVecIndex 确保 vec0 索引存在且维度与 dim 一致（08 票迁移/维护）：
//   - dim ≤ 0（CLI 迁移路径未指定）：从存量向量推导维度（最长者；
//     无存量取 DefaultVectorDim）——避免「CLI 硬编码默认维度 vs 实际
//     模型维度」在非 1536 维模型（DGW_EMBEDDING_MODEL 切换）下反复
//     重建索引（review 修复）；
//   - 表缺失（历史库升级 / 从未同步过）→ 按 dim 建表 + 从 embeddings
//     回填同维存量行（07 的 BLOB 与 sqlite-vec 字节兼容，无需重嵌入）；
//   - 维度不符（模型切换 → 全量重嵌走 SaveEmbeddings，首条新向量触发
//     重建）→ DROP + 重建 + 回填同维行。
//
// 幂等：维度一致 = 无操作。失败返回错误（调用方降级：检索退化为纯关键词，
// 与「向量是兜底通道」的定位一致）。
func EnsureVecIndex(ctx context.Context, st DBer, dim int) error {
	if dim <= 0 {
		dim = DefaultVectorDim
		if d, err := maxVectorDim(ctx, st.DB()); err != nil {
			return err
		} else if d > 0 {
			dim = d
		}
	}
	cur, err := vecIndexDim(ctx, st.DB())
	if err != nil {
		return err
	}
	if cur == dim {
		return nil
	}
	if _, err := st.DB().ExecContext(ctx, `DROP TABLE IF EXISTS dgw_sem_vec`); err != nil {
		return fmt.Errorf("drop vec0: %w", err)
	}
	if _, err := st.DB().ExecContext(ctx,
		fmt.Sprintf(`CREATE VIRTUAL TABLE dgw_sem_vec USING vec0(entity_fqn TEXT PRIMARY KEY, vector float[%d])`, dim)); err != nil {
		return fmt.Errorf("create vec0(%d): %w", dim, err)
	}
	if _, err := st.DB().ExecContext(ctx, `
		INSERT INTO dgw_sem_vec (entity_fqn, vector)
		SELECT entity_fqn, vector FROM dgw_sem_embeddings
		WHERE length(vector) = ?`, dim*4); err != nil {
		return fmt.Errorf("backfill vec0(%d): %w", dim, err)
	}
	return nil
}

// maxVectorDim 返回存量向量的最大维度（无向量 = 0）。推导依据：模型切换
// 后全量重嵌，新模型维度是当前事实源；混合维度期间取最大维（旧维行会被
// 维度过滤排除在回填外）。
func maxVectorDim(ctx context.Context, db *sql.DB) (int, error) {
	var bytesLen sql.NullInt64
	if err := db.QueryRowContext(ctx,
		`SELECT max(length(vector)) FROM dgw_sem_embeddings`).Scan(&bytesLen); err != nil {
		return 0, fmt.Errorf("max embedding dim: %w", err)
	}
	if !bytesLen.Valid {
		return 0, nil
	}
	return int(bytesLen.Int64) / 4, nil
}

// vecIndexDim 读 vec0 索引的当前维度（从 sqlite_master 的建表语句解析；
// 表缺失 = 0）。vec0 是虚拟表，维度只在 DDL 里声明，无 SQL 元数据面可查。
func vecIndexDim(ctx context.Context, db *sql.DB) (int, error) {
	var sqlText string
	err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'dgw_sem_vec'`).Scan(&sqlText)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read vec0 definition: %w", err)
	}
	m := vecDimRe.FindStringSubmatch(sqlText)
	if m == nil {
		return 0, fmt.Errorf("parse vec0 definition: %q", sqlText)
	}
	dim, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, fmt.Errorf("parse vec0 dim %q: %w", m[1], err)
	}
	return dim, nil
}

// vecDimRe 匹配 vec0 建表语句里的维度声明（float[N]）。
var vecDimRe = regexp.MustCompile(`float\[(\d+)\]`)

// encodeFloats 序列化 float32 切片为小端字节（存 BLOB）。
func encodeFloats(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(f))
	}
	return buf
}

// decodeFloats 反序列化 BLOB 为 float32 切片（检索/测试用）。
func decodeFloats(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("向量 BLOB 长度 %d 不是 4 的倍数", len(b))
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out, nil
}

// EmbedEntityTexts 是同步期生成 embedding 的公共入口：对 target 里（已变更
// 的）实体生成文本 → 调 Embedder 生成向量 → 写入。调用方传「变更实体」
// （diff 的 Added + Updated）即满足 ADR-0002「增量只嵌入变更实体」。失败
// 降级：embedding 出错只记日志不阻断同步（验收「失败降级不阻塞同步」）。
// 返回实际写入的实体数。失败返回错误（调用方记录降级提示，不阻塞同步——
// 「失败降级不阻塞」的契约在调用方侧兑现）。
func EmbedEntityTexts(ctx context.Context, st DBer, target *Target, emb Embedder, model string, logf func(format string, args ...any)) (int, error) {
	if emb == nil {
		return 0, nil // 未配置 embedding（无 API key）= 跳过，不阻塞
	}
	if len(target.Entities) == 0 {
		return 0, nil
	}
	fqns := make([]string, 0, len(target.Entities))
	texts := make([]string, 0, len(target.Entities))
	for _, e := range target.Entities {
		fqns = append(fqns, e.FQN)
		texts = append(texts, embedEntity(e))
	}
	vecs, err := emb.Embed(ctx, texts)
	if err != nil {
		if logf != nil {
			logf("embedding 生成失败（降级：不阻塞同步）: %v", err)
		}
		return 0, fmt.Errorf("embedding 生成失败: %w", err)
	}
	if err := SaveEmbeddings(ctx, st, model, fqns, vecs); err != nil {
		if logf != nil {
			logf("embedding 写入失败（降级：不阻塞同步）: %v", err)
		}
		return 0, fmt.Errorf("embedding 写入失败: %w", err)
	}
	return len(fqns), nil
}

// EmbeddingCoverage 报告向量库覆盖情况（cmdSemanticSync 决定全量回填 vs
// 增量嵌入的依据，对抗评审修复「模型切换 → 混合维度」与「API key 后配 →
// 永久部分覆盖」）：
//   - missing = 无向量的活跃实体数（首启/历史失败留空）；
//   - mismatch = 模型与当前不一致的向量数（DGW_EMBEDDING_MODEL 变更残留）。
func EmbeddingCoverage(ctx context.Context, st DBer, model string) (missing, mismatch int, err error) {
	if err := st.DB().QueryRowContext(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM dgw_sem_entities WHERE tombstone = 0) -
		  (SELECT COUNT(*) FROM dgw_sem_embeddings)`).Scan(&missing); err != nil {
		return 0, 0, fmt.Errorf("count embedding coverage: %w", err)
	}
	if missing < 0 {
		missing = 0 // 残留向量多于实体（清理滞后）：不算缺失
	}
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dgw_sem_embeddings WHERE model != ?`, model).Scan(&mismatch); err != nil {
		return 0, 0, fmt.Errorf("count embedding model mismatch: %w", err)
	}
	return missing, mismatch, nil
}

// RemoveEmbeddings 删除实体的向量（墓碑对应面：实体被墓碑化后其向量一并
// 清理，检索不再命中死实体；幂等，可重复调用）。embeddings 与 vec0 双删
// （08 票双写维护，索引面不留孤儿行），单事务内（review 修复）。
func RemoveEmbeddings(ctx context.Context, st DBer, fqns []string) error {
	if len(fqns) == 0 {
		return nil
	}
	query := "DELETE FROM dgw_sem_embeddings WHERE entity_fqn IN (?" + strings.Repeat(",?", len(fqns)-1) + ")"
	args := make([]any, len(fqns))
	for i, f := range fqns {
		args[i] = f
	}
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("remove embeddings: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM dgw_sem_vec WHERE entity_fqn IN (?"+strings.Repeat(",?", len(fqns)-1)+")", args...); err != nil {
		return fmt.Errorf("remove vec0 rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit remove embeddings: %w", err)
	}
	return nil
}
