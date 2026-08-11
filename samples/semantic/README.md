# 语义作者入口（neo-cloud 全量，ADR-0002「按服务拆 YAML + 全局指标/概念文件」）
#
# 本目录 = 语义仓库（内部 Gitea）空仓库初始化时的种子内容（bootstrap.sh），
# 也是同步管线（dgw semantic-sync --dir samples/semantic）的直接输入；
# 覆盖 neo-cloud 全部 13 个后端服务、10 个持库（US-15/16 人工确认后回写）。
#
# 目录约定（编译器的输入面）：
#   services/<服务名>.yaml   每服务一个文件（库/表/列/枚举/references +
#                            语义：描述/is_time/枚举 label）
#   metrics.yaml             全局指标（OSI 式口径：expression + aggregation + filter）
#   concepts.yaml            全局业务概念（describes 到表/列/指标）
#
# 内容分工（ADR-0007「结构自动、语义人工」）：
#   - 结构（表/列/类型/枚举值/引用边）由 dgw-collect 采集 neo-cloud 迁移产出；
#   - 语义（服务/库/表/列描述、枚举含义、is_time 时间轴、指标口径、业务概念）
#     由 Coding Agent 起草 + 服务负责人 review 确认后回写（US-15/16）；
#   - 采集重跑不覆盖已确认语义：WriteDraft 按 FQN 合并保留（结构永远以新采集为准）。
#
# 变更流程（US-21/24）：本目录文件走 git review 合入 → 手动触发同步管线 →
# 运行时 SQLite 更新；revert + 重跑管线即回滚（墓碑传播删除）。
