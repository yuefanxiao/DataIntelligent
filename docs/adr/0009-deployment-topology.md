# 部署拓扑：内部开发机单机 Docker + 双传输（Streamable HTTP 为主）；HA/监控/正式上线回滚后置

v1 网关部署形态 = **内部开发机单机部署**（能连通生产网络、但不是生产集群的机器，生产面零风险），**Docker 单容器**（compose 单服务：镜像版本化 tag、volume 挂三样——SQLite `/data`、执行记录 `/logs`、配置 env `/config` 0600、`restart: unless-stopped`）；**数据库凭证只存在这台机器的 env 文件，开发机/CI 零凭证**（安全分界线 = 这台机器）。MCP 传输 = **双传输实现**：Streamable HTTP 为主（守护进程 + bearer token 认证，go-sdk `RequireBearerToken` + 自实现 `TokenVerifier`），stdio 为调试形态（env 传 key）。从库连接 = **可配置 DSN 口子**（host/port/用户/密码）+ 按 dbname 路由（本体 Database 实体 = PG database）；生产网络通路方案（NodePort vs port-forward 常驻 vs 内网 LB）**生产部署时再定**，v1 不锁。DB 凭证边界 = 网关/采集器只用**专用共享只读角色**（NOSUPERUSER/NOCREATEDB/NOCREATEROLE/NOINHERIT、10 库 GRANT SELECT、角色级 statement_timeout、收 SET ROLE 能力），**永不使用超管凭证**；provisioning 由开发自建（服务器 root/kubectl 取 CNPG postgres 超管建角色，SQL 脚本入库可重放）。执行记录 JSONL = 网关机本地文件（Docker volume，轮转照 ADR-0006）。监控/告警、正式上线/回滚流程 = **v1 不做，后续优化项**（v1 观测面 = 执行记录 + docker restart 兜底；回滚基线 = 旧镜像 tag + SQLite 文件备份恢复，ADR-0005）。来源：票据 11（issue #12），2026-08-11 拍板。

## Considered Options

- **传输：双传输 vs 仅 stdio vs 仅 Streamable HTTP**：stdio = 每会话一个进程，10 的进程级并发闸退化为每进程独立信号量（每 key 跨会话不强制）、grants 热重载无意义、日志分散；Streamable HTTP = 守护进程，并发闸/热重载/日志单点全有效，key 走 Authorization header（01 的 TokenVerifier 直接复用）。官方 go-sdk 双传输免费（01 已荐「HTTP 为主、stdio 为辅」）。→ **双传输**：HTTP 为主（正式形态），stdio 仅调试（本地起步验证期可直连）。并发闸数值归 12 时**按守护进程语义定**。
- **部署位：生产集群内 vs 内部开发机 vs 纯本地进程**：网关进生产集群 = 与业务共享风险面、要动生产 k8s（Deployment/PV/Ingress/Secret）；纯本地进程 = 每开发一台、每台都要 DB 凭证与网络通路，凭证面扩大；内部开发机 = 生产面零风险、凭证集中单点、Docker 零 k8s 依赖，与 ADR-0005「SQLite 单文件单机硬约束」天然一致。→ **内部开发机单机**；多机/HA 需求出现时走 PG 升级路径（ADR-0005），OTel = 该场景的观测形态（ADR-0006 已在案）。
- **从库连接：静态 DSN vs 动态发现**：同一 CNPG 集群一主两从、10 库每服务一库、v1 只接从库——端点数量 = 1，动态发现（k8s API/DNS SRV 随拓扑自愈）的复杂度换不来收益；且开发机**无法解析集群内 DNS**（环境事实），通路方案本身悬置。→ **可配置 DSN 留口子** + dbname 路由；通路方案（NodePort/port-forward/LB）生产部署时定，v1 配置面不锁。
- **DB 凭证：共享只读角色 vs 超管直连**：超管直连 = 03/10 物理边界（低权限角色 + statement_timeout + 禁 SET ROLE）全部失效，任何绕过校验层的查询都能写库。→ 专用只读角色；超管仅一次性 provisioning（建角色/GRANT/角色级 statement_timeout），密码不进任何配置。
- **日志位置：网关机文件 vs 日志管线 vs OTel**：v1 无集群日志管线（内部开发机无 k8s 设施），执行记录（ADR-0006）已在宿主机文件 + 轮转形态定义；OTel = 多机/HA 场景的形态（ADR-0006 归 11 的升级路径）。→ 网关机本地文件 + Docker volume；数值（7/30 天）归 12。
- **采集触发：手动 on-demand vs 自动轮询/定时**：08 已定「增量触发 v1 = 手动 on-demand、Gitea 后置」；本票补明确无自动轮询/定时（消费方是开发人员，盲采/轮询无消费者）；采集器独立 CLI 不进网关镜像（网关镜像最小化）。→ 手动触发，工作流由**采集工作流 Skill（≤1 页，08 已定）**封装，记录为 v1 交付物。
- **数据新鲜度：接受延迟 vs 实时保证**：CNPG 默认异步流复制、从库延迟秒级；v1 消费场景（「昨天/近 N 天」类排障查询）对延迟无感，实时保证需要主库直连/同步复制——超出 v1 边界。→ 接受延迟、不承诺实时；**启动自检**两条硬校验兜物理边界（`pg_is_in_recovery() = true` 防连错主库 + 角色级 `statement_timeout` 生效确认，不过拒启）；延迟监测（pg_stat_replication 周期读数）与监控栈一并后置。
- **监控/告警、正式上线回滚：v1 建 vs 后置**：v1 消费方 = 开发人员，网关不可达的影响 = 「查不了数」，无生产链路依赖；监控设施（metrics/告警通道/OTel）无消费者。→ 后置；v1 兜底 = docker restart + 执行记录；回滚基线 = 旧镜像 + SQLite 备份文件恢复。

## Consequences

- 交付物形态确定：`dgw` 网关镜像 + `dgw-collect` 采集器 CLI + 两份 Skill（02 Agent Skill + 采集工作流 Skill）+ 配置文件（DSN 口子、env 0600）。
- 安全分界线 = 内部开发机：DB 凭证（只读角色）只在该机 env 文件；开发机/CI 零凭证。provisioning（建只读角色）是上线的第一步，由开发持服务器 root/kubectl 自建。
- 并发闸（10）按守护进程语义落地——双传输下正式形态是 HTTP 守护进程，进程级闸有效；stdio 调试形态下闸退化可接受（调试场景）。
- 08 决议原样有效，补充明确「采集触发 = 手动 on-demand、无自动轮询/定时」；ADR-0007 无需修正，本 ADR 记录该澄清。
- 执行记录（04）落点确定：网关机本地 volume；不接日志管线、不接 OTel（升级路径 = 多机/HA 出现时，ADR-0006）。
- 上线动作 = compose 起服务 + 启动自检（两硬校验）通过即用；回滚基线 = 旧镜像 tag + SQLite 文件备份恢复；正式上线/回滚流程与监控/告警排期归 12（阶段切分）。
- 生产网络通路方案（NodePort vs port-forward 常驻 vs 内网 LB）留待生产部署时定，配置面已留 DSN 口子，翻案成本 = 配置不改代码。
