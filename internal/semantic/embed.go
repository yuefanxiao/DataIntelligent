package semantic

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
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

// SaveEmbeddings 把实体向量写入 dgw_sem_embeddings（幂等 upsert）。
func SaveEmbeddings(ctx context.Context, st DBer, model string, fqns []string, vecs [][]float32) error {
	if len(fqns) != len(vecs) {
		return fmt.Errorf("SaveEmbeddings: %d 个 FQN vs %d 个向量", len(fqns), len(vecs))
	}
	for i, fqn := range fqns {
		buf := encodeFloats(vecs[i])
		if _, err := st.DB().ExecContext(ctx, `
			INSERT INTO dgw_sem_embeddings (entity_fqn, model, vector)
			VALUES (?, ?, ?)
			ON CONFLICT(entity_fqn) DO UPDATE SET
				model = excluded.model, vector = excluded.vector,
				updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')`,
			fqn, model, buf); err != nil {
			return fmt.Errorf("save embedding %s: %w", fqn, err)
		}
	}
	return nil
}

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
// 返回实际写入的实体数。
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
		return 0, nil
	}
	if err := SaveEmbeddings(ctx, st, model, fqns, vecs); err != nil {
		if logf != nil {
			logf("embedding 写入失败（降级：不阻塞同步）: %v", err)
		}
		return 0, nil
	}
	return len(fqns), nil
}

// RemoveEmbeddings 删除实体的向量（墓碑对应面：实体被墓碑化后其向量一并
// 清理，检索不再命中死实体；幂等，可重复调用）。
func RemoveEmbeddings(ctx context.Context, st DBer, fqns []string) error {
	if len(fqns) == 0 {
		return nil
	}
	query := "DELETE FROM dgw_sem_embeddings WHERE entity_fqn IN (?" + strings.Repeat(",?", len(fqns)-1) + ")"
	args := make([]any, len(fqns))
	for i, f := range fqns {
		args[i] = f
	}
	if _, err := st.DB().ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("remove embeddings: %w", err)
	}
	return nil
}
