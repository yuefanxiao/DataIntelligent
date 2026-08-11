# v1 build 11 — Agent Skill + 采集工作流 Skill

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/28
Status: closed（PR #42 合入 main，2026-08-12 关闭）

## 交付摘要

1. **Agent Skill**（deploy/skills/agent-skill.md，≤1 页）：六工具清单（含
   execute_sql 语义与限制：默认 500/硬上限 5000 + truncated、多库 dbname 必填、
   并发闸每 key 2/进程 8、statement_timeout 30s、plan_id 透传）、标准工作流
   「发现→解析→执行」、回退路径（无现成指标走表/列原料路径、被拒后按结构化
   错误 kind 调整——permission_denied/rate_limited/invalid_request（details.reason
   区分 syntax_error/timeout）/internal）。
2. **采集工作流 Skill**（deploy/skills/collector-workflow.md，≤1 页）：封装
   09 采集器「跑采集 → 生成 YAML 草稿 → 引导人工 review → 合入 → 手动触发同步」
   全流程 + 可选校准（v1 低优先）+ 回滚（commit 即版本，引用语义仓库 README）。
3. **deploy/skills/README.md**：两件 Skill 的索引与安装说明。
4. **--out 参数修正**（deploy/semantic-repo/README.md + bootstrap.sh）：采集器
   `--out` 指向 clone **根**（草稿落 `<clone>/services/`），原写法
   `<clone>/services` 会产生 `services/services/` 嵌套、同步管线读不到
   （draft.go WriteDraft: filepath.Join(outDir, "services")）。

## 验证

- **Agent Skill 实测**（demo 主从 PG + 网关，Coding Agent 按文档走六工具）：
  search_entities 双入口命中 → get_entity/traverse_relations/list_enum_values →
  get_metric_definition 带 time_range dry-run 展开（不执行）→ execute_sql 真实
  数据（昨日退款率 10%）→ 未授权表 permission_denied / 非 SELECT invalid_request。
- **采集工作流 Skill 实测**（真实 ~/cloud/neo-cloud 全量 10 服务）：采集 1366
  实体/1388 边/284 枚举（exit 2 门禁发现照写）→ review → commit/push（裸仓库
  模拟 Gitea）→ semantic-sync dry-run + 应用；实测触发第三道闸（种子样例
  payment-service 不在清单被清理 → 悬空引用编译拒绝，按设计工作）。
- verify.sh 全链路通过（bootstrap → 采集 → 同步 → revert 回滚）。
- code-review（双轴）：timeout 非独立 kind（实为 invalid_request +
  details.reason=timeout）修正；回滚段去重复。
- code-review-adversarial（准确性审计师，docs 型）：1×P1（bootstrap.sh 残留
  --out 错误）修复，收敛 ready。

## 交接注意

- 语义仓库真实使用时，种子 metrics/concepts（samples/semantic）引用样例服务
  （order/payment-service），与 neo-cloud 采集产出不兼容——需按 US-15/16 由
  服务负责人确认后回写真实语义（本票实测即按此处理，交付物不包含测试用语义）。
- #30（v1 build 13，全服务语义数据）可将本票实测流程作为标准路径参考。
