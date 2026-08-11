# v1 build 03 — 校验层：AST 分类 + 表提取 + 授权比对

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/20
Status: assigned（yuefanxiao，2026-08-12 领取；实现 + 单测完成，PR 合入后关闭）
Blocked by (open blockers): 0

## 来源

docs/spec.md §2.2/§4.5/§5；ADR-0008；issue #15

## What to build

校验层四段链的前两段（AST 分类 + 表提取与授权比对），作为独立包交付 + 单测 seam（§5.2）：wasilibs/go-pgquery（libpg_query WASM 移植，cgo-free、PG 17 真实语法）解析 → 非 SELECT 类（DML/DDL/COPY/utility/数据修改 CTE）一律拒绝 → 语法层表引用提取（CTE/子查询/join 全可见）→ 对 02 的表 FQN 白名单逐表比对（未知/未授权表拒绝；EXPLAIN 不作授权依据）。失败 = 结构化错误回传（区分语法错误 vs 无权限），网关不重试、无自愈循环。后两段（PG 物理边界 + 限额包层）在 04 完成接线。

## Acceptance criteria

- [x] 非 SELECT 拒绝集单测全绿：DML / DDL / COPY / utility / 数据修改 CTE
- [x] 表提取覆盖 CTE / 子查询 / join，语法层表引用全可见
- [x] 授权比对：白名单命中通过；未知表 / 未授权表拒绝，错误区分「无权限表」
- [x] 拒绝错误结构化（语法错误 vs 无权限可机器区分）
- [x] EXPLAIN 不作为授权依据
- [x] 单测 seam 覆盖 §5.2 全部四类（分类/提取/比对/拒绝语义）

## 交付（2026-08-12）

- `internal/validate` 独立包（validate.go + walk.go）：Parse / ClassifyStmt / ExtractTables / AuthorizeTables / Check
- 遍历器：CTE 作用域（递归/互引/遮蔽）+ 反射下钻（子查询/join/集合运算全可见，函数实参内隐藏子查询不漏）
- 比对注入式 Resolve/Allow（FQN 解析归 04 接线，白名单归 authz）
- 超出枚举拒绝集的「只读推论」：SELECT INTO / 行锁子句（FOR UPDATE/SHARE/…）→ write_side_effect；EXPLAIN 整体按 utility 拒绝
- 单测 60+ 例：分类 28 拒绝例 + 7 通过例 / 提取 31 例 / 比对 6 例 / 端到端拒绝语义 6 例

## Blocked by

- #19 — 权限面（已关闭 2026-08-12，PR #32 合入）
