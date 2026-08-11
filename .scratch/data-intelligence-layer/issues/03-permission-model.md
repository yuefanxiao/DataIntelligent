# 03 权限模型：粒度、API key 机制与维护方

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/4
Status: open
Blocked by (open blockers): 1

Part of #1

## Question

权限模型怎么设计？

- 粒度：表级 / 列级 / 行级（行级是否要 RLS）
- 凭据机制：API key 如何生成、分发、轮换；key 绑定什么（用户/角色/范围）
- 谁维护权限：配置文件 + CLI？管理工作台（见地图 Not yet specified）？
- 与语义层的关系：权限挂在底层表还是语义实体/指标上
- 与只读从库账号的分工（PostgreSQL 侧只读账号 + 网关侧细粒度拦截如何分层）

