# 样例语义作者入口（ADR-0002「按服务拆 YAML + 全局指标/概念文件」）
#
# 本目录 = 同步管线（dgw semantic-sync --dir samples/semantic）的输入样例，
# 供验收重放与人工体验；结构镜像 neo-cloud 形态（每服务一库、枚举挂列、
# is_time 标注、表间 references、指标口径、业务概念）。
#
# 目录约定（编译器的输入面）：
#   services/<服务名>.yaml   每服务一个文件（库/表/列/枚举/references）
#   metrics.yaml             全局指标（OSI 式口径：expression + aggregation + filter）
#   concepts.yaml            全局业务概念（describes 到表/列/指标）
#
# 变更流程（US-21/24）：本目录文件走 git review 合入 → 手动触发同步管线 →
# 运行时 SQLite 更新；revert + 重跑管线即回滚（墓碑传播删除）。
