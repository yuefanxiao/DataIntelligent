# v1 build 09 — 采集器 CLI

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/26
Status: closed（PR #39 合入 main，2026-08-12）

## 来源

docs/spec.md §4.7；ADR-0007；issue #15

## What to build

采集器（独立 Go CLI，与同步管线同仓）：解析服务仓库 migration 文件（golang-migrate v4.19.1、纯 SQL up/down、目录命名统一、可全量重建）→ 结构 YAML 草稿；解析**生产形态**（每服务一库/schema 前缀，非 docker-compose 布局）；GORM 模型交叉验证为第二道闸；calibrate 子命令按需连只读从库做生产校准（共享只读角色，v1 低优先）；确定性可测（neo-cloud 真实迁移语料即 golden test 集）；触发 = 手动 on-demand（无轮询/定时）。产出与 07 编译校验兼容，可进同步管线。

## Acceptance criteria

- [x] 对 neo-cloud 全部持库服务运行采集 → 结构 YAML 草稿（10 服务 / 90 表 / 1238 列 / 283 枚举 / 30 引用，编译兼容通过）
- [x] GORM 交叉验证作为第二道闸（实证捕获 payment_channel DO 块动态 DDL 漂移 → error 门禁）
- [x] calibrate 子命令可连只读从库校准（docker PG 集成验证：漂移注入精确检出 + 清态零发现；会话级只读强制）
- [x] golden test：真实迁移语料即测试集，同输入同输出（TestGoldenDrafts/Determinism/FullCorpusScan）
- [x] 采集产出与 07 编译校验兼容（semantic.Load+Compile 第三道闸，可进同步管线）

## Blocked by

- #24 — 语义层运行时 + 同步管线（已关闭）

## 交接摘要（v1 build 10 输入）

- 交付形态：`dgw-collect` CLI（scan/calibrate）+ `internal/collector` + `samples/collector/manifest.yaml`
- 采集草稿经人工 review 入语义仓库后，由 `dgw semantic-sync` 进运行时（build 10 的语义仓库/部署上下文）
- calibrate 的只读从库凭证位置与采集器部署位置 → build 10 决策（ADR-0007 输出）
- 采集工作流 Skill（≤1 页：跑采集 → 生成 YAML 草稿 → 引导人工 review → 合入后触发同步）→ issue #28（build 11）
