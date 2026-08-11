# v1 build 07 — 语义层运行时 + 同步管线

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/24
Status: closed（PR #36 合入 main，squash bc50c61；对抗评审收敛补强 PR #37 合入 main 0767f9a）
Blocked by: 无

## 来源

docs/spec.md §2.1/§4.1/§4.2/§4.4；ADR-0001/0002(修正)/0005；issue #15

## What to build

语义层运行时 + 同步管线：SQLite 语义库（六类实体 service/database/table/column/metric/concept + 四类关系边 connects_to/contains/references/describes 双向可遍历 + 枚举取值挂列 + is_time 标注 + 稳定 FQN）；YAML 作者入口（按服务拆文件 + 全局指标/概念）；自研 Go 同步管线：编译校验（FQN 唯一/引用完整性/指标 SQL 可解析/枚举合法，失败原子拒绝）→ dry-run diff（增删改清单）→ 幂等 upsert + 墓碑软删除应用；运行时只查 SQLite 不查 YAML。embedding 生成（外部 OpenAI text-embedding-3，同步期写入向量，失败降级不阻塞）；指标/概念授权编译期展开为底层表授权写入 02 权限表（杜绝悬空授权）；服务/库级通配 = 语法糖，展开为具体表清单快照（新表默认拒绝 + 管线告警 + 重展开确认，`*` 不开放）。备份 = WAL checkpoint + 文件拷贝（10 部署接入）。

## Acceptance criteria

- [x] 样例语义 YAML（多服务 + 全局）编译 → dry-run diff 正确 → 应用后 SQLite 全量可查（实体/边/枚举/is_time）——samples/semantic/ 交付 + 端到端测试
- [x] 编译校验：FQN 重复 / 引用缺失 / 指标 SQL 不可解析 / 枚举非法 → 原子拒绝
- [x] 幂等：同输入重跑同输出（diff 驱动 apply 零写库、墓碑传播删除）
- [x] 运行时只查 SQLite，不读 YAML
- [x] embedding 生成写入向量（OpenAI text-embedding-3-small；失败降级不阻塞同步）
- [x] 指标/概念授权编译期展开为表授权（写入 02 权限表），无「指标有权底层没权」悬空
- [x] 通配展开为快照：新表默认拒绝 + 管线告警
- [x] 同步管线 dry-run 确定性测试（同输入同输出，§5.3 seam）

## Blocked by

- #T01 — 网关骨架（#18，已关）
- #T02 — 权限面（#19，已关）

## 交付物（PR #36）

- internal/semantic/：model/yaml/compile/probe/diff/query/apply/sync/embed/backup/grants_expander + 测试
- store.go：dgw_sem_entities / dgw_sem_relations / dgw_sem_enum_values / dgw_sem_embeddings / dgw_grant_patterns
- grants：metric:/concept:/service:/database: 授权对象展开（Expander 注入）
- cmd/dgw：semantic-sync / semantic-backup 子命令
- samples/semantic/：样例作者入口（多服务 + 全局指标/概念）

## 评审

- /code-review 两轴（Standards/Spec）通过
- /code-review-adversarial 两轮收敛：5 角色（设计/正确性/安全/魔鬼代言人/可简化性），P1 全修（LIKE 通配泄漏、embedding 模型 id、备份同文件+busy、探针单语句），P2 全修，P3 残余 2 项非阻塞（告警 N+1、无 key 历史向量不清理）

## 交接（给 08 票）

运行时查询面 = internal/semantic/query.go（GetEntity / ListTables / relationTargets / relationSources / MetricTables / ConceptTables / TablesForService|Database）；向量 BLOB float32 小端与 sqlite-vec vec0 字节兼容（升级 modernc≥v1.47 后 INSERT..SELECT 迁移）；语义元数据面认证即读；样例 `dgw semantic-sync --dir samples/semantic` 可体验。
