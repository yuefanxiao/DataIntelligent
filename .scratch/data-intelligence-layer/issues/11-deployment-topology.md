# 11 部署拓扑：本地起步、接从库、高可用

GitHub: https://github.com/yuefanxiao/DataIntelligent/issues/12
Status: closed (2026-08-11)
Blocked by (open blockers): 0

Part of #1

## Question

部署拓扑：本地起步怎么跑（MCP stdio vs Streamable HTTP）、如何接从库（连接串/发现机制）、网关自身是否需要高可用、上线与回滚流程？

## Resolution

决议：部署拓扑 = **内部开发机单机 Docker + 双传输（Streamable HTTP 为主）**；监控/HA/正式上线回滚后置（2026-08-11 拍板，决议全文 = GitHub issue #12 评论）。

1. **传输形态**：双传输实现（官方 go-sdk 免费支持）——Streamable HTTP 为主（守护进程 + bearer token：RequireBearerToken + TokenVerifier），stdio 为调试形态（env 传 key）；并发闸按守护进程语义定，数值归 12。
2. **部署位**：内部开发机单机 Docker（不进生产集群）——compose 单服务、镜像版本化、volume 挂 SQLite `/data` + 日志 `/logs` + 配置 env `/config` 0600、restart: unless-stopped；**数据库凭证只在该机 env 文件，开发机/CI 零凭证**。
3. **从库连接**：可配置 DSN 口子（host/port/用户/密码）+ dbname 路由（本体 Database = PG database）；生产网络通路方案（NodePort vs port-forward vs 内网 LB，开发机无法解析集群内 DNS）生产部署时再定。
4. **DB 角色边界**：专用共享只读角色（NOSUPERUSER/NOCREATEDB/NOCREATEROLE/NOINHERIT、10 库 GRANT SELECT、角色级 statement_timeout、收 SET ROLE）；**provisioning 开发自建**（服务器 root/kubectl 取 CNPG postgres 超管建角色）；网关永不超管。
5. **日志位置（04 输入）**：执行记录 JSONL = 网关机本地文件（Docker volume）；轮转照 04（原始 ~7 天 + 摘要 ~30 天，数值归 12）；不接日志管线、不接 OTel（升级路径 = 多机/HA，ADR-0006）。
6. **采集器与采集工作流（08 输入）**：dgw-collect 保留，**手动触发、无自动轮询/定时**（clone 语义仓库 → 采集 → review → 合入 → 手动同步到目标机）；校准凭证 = 同一只读角色；采集器独立 CLI 不进网关镜像；**采集工作流 Skill（≤1 页）记录为 v1 交付物**（与 02 Agent Skill 并列）。
7. **数据新鲜度（fog 毕业）**：接受从库延迟（秒级）、不承诺实时；**启动自检** = pg_is_in_recovery()=true + 角色级 statement_timeout 生效，不过拒启；延迟监测后置。
8. **监控/告警、正式上线回滚流程**：v1 不做，后续优化项（v1 观测面 = 执行记录 + docker restart 兜底；回滚基线 = 旧镜像 + SQLite 备份恢复，ADR-0005）。

### 给下游的输入

- **12（阶段切分，issue #13）**：并发闸数值（每 key N / 进程级 M / statement_timeout 默认）按守护进程语义定；保留期数值；v1 交付物清单 = 网关镜像 + 采集器 CLI + 两份 Skill + 配置/DSN 口子；监控/告警与正式上线回滚流程排期。
- **08（ADR-0007）**：决议原样有效，补明确「采集触发 = 手动 on-demand、无自动轮询/定时」。
- **ADR-0009**：部署拓扑决议入档。
