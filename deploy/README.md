# 部署（ADR-0009：内部开发机单机 Docker）

**形态**：内部开发机单机 Docker（compose 单服务），**不进生产集群**；
三挂载：SQLite `/data`、执行记录 `/logs`、凭证 env `/config` 0600；
`restart: unless-stopped`；数据库凭证**只存该机 env 文件**，开发机/CI
零凭证；启动自检两条硬校验（不过拒启）；回滚基线 = 旧镜像 tag + SQLite
备份文件恢复。

```
┌─ 内部开发机 ────────────────────────────┐
│  docker compose（restart unless-stopped）│
│  ┌─ dgw 容器（非 root）──────────────┐  │
│  │  dgw serve：HTTP :8080 (bearer)   │  │
│  │  ├─ /data   ← volume dgw-data     │  │
│  │  ├─ /logs   ← volume dgw-logs     │  │
│  │  └─ /config ← env 文件只读(0600)  │  │
│  └───────────────────────────────────┘  │
│          │ 启动自检：pg_is_in_recovery  │
│          │         + 角色级 timeout     │
└──────────┼──────────────────────────────┘
           ▼
    业务从库（CNPG replica，共享只读角色 dgw_reader）
```

## 0. 前置

- Docker Engine + Compose（v2+）；开发机到从库的网络通路（NodePort /
  port-forward / 内网 LB，生产部署时定，v1 配置面只留 DSN 口子）。

## 1. 共享只读角色 provisioning（开发自建，超管一次性）

见 [`provisioning/README.md`](provisioning/README.md)：CNPG postgres 超管
执行 `readonly-role.sql`（角色 + 10 库只读 + 角色级 statement_timeout）。

## 2. 语义仓库初始化（Gitea）

见 [`semantic-repo/README.md`](semantic-repo/README.md)：
`bootstrap.sh` 打通本地 clone 操作路径；`semantic-repo/verify.sh`
全链路自动验证（采集 → 合入 → 同步 → revert 回滚）。

## 3. 网关配置（凭证只在这个文件）

```sh
cd deploy
cp config/env.example config/env
chmod 600 config/env          # 凭证文件 0600
vim config/env                # 填 DGW_PG_DATABASES（共享只读角色 DSN）
```

`config/env` 已在 .gitignore，不提交；开发机/CI 零凭证。

## 4. 起服务（启动自检不过 = 拒启）

```sh
docker compose -f deploy/docker-compose.yml up -d --build
docker compose -f deploy/docker-compose.yml logs -f
```

启动日志应含：

```
dgw: 启动自检通过：N 条 dbname 路由全部连到从库（pg_is_in_recovery() = true）+ 角色级 statement_timeout 生效
dgw: MCP Streamable HTTP 监听 :8080（bearer 认证）
```

**失败场景（拒启演示）**——任一条硬校验不过，进程退出非零、不监听：

- DSN 指向主库 → `pg_is_in_recovery() = false——连到的是主库`；
- 角色未配 statement_timeout / 与 env 不一致 → `角色级 statement_timeout 未生效`；
- 从库不可达 → `连接失败`。

单独复现（不起网关）：`docker compose ... exec dgw dgw selfcheck` 或宿主机 `dgw selfcheck`。

## 5. 验收验证（HTTP 形态 MCP 查询）

```sh
# 建凭据 + 授权（宿主机对 /data 的 SQLite 操作，或 compose exec）
docker compose -f deploy/docker-compose.yml exec dgw dgw key-create --user dev-alice
# 创建后明文仅打印一次；随后 dgw grant-add --user dev-alice --table bss-bill.bill.orders

# 真实 MCP 往返（官方 go-sdk；deploy/demo/mcp-ping.go）
cd deploy/demo && go run mcp-ping.go --addr http://127.0.0.1:8080/mcp \
  --key <明文 key> --dbname bill --sql "SELECT count(*) FROM orders"
```

## 6. 运维

| 事项 | 操作 |
|---|---|
| 执行记录 | `/logs`（原始 JSONL 7 天轮转 + 聚合摘要 30 天，env 可配） |
| 语义备份 | `dgw semantic-backup --out <path>`（WAL checkpoint + 文件拷贝） |
| 升级 | 新镜像 tag `DGW_IMAGE_TAG=dgw:v2 ... up -d`（旧 tag 即回滚基线） |
| 回滚 | 停 → 旧镜像 tag + 备份文件恢复 `/data` → 起 |
| 观测面 | 执行记录 + `docker restart` 兜底（监控/告警 = 阶段 2，v1 不做） |

## 本地全量验证（demo 环境，含主从流复制 + 拒启演示）

`deploy/demo/README.md`：一条命令起「主 + 从」PG（流复制）+ 网关 +
真实 MCP 查询 + 两个拒启失败场景 + 语义仓库操作路径验证。

## 交付物清单（ADR-0009 Consequences）

- `Dockerfile` / `docker-compose.yml` / `config/env.example` —— 网关镜像
  与部署形态（三挂载 + 凭证边界 + restart unless-stopped）；
- `provisioning/readonly-role.sql` —— 共享只读角色（可重放 SQL）；
- `semantic-repo/bootstrap.sh` —— 语义仓库本地 clone 操作路径；
- `semantic-repo/verify.sh` —— 操作路径全链路自动化验证；
- 启动自检（`internal/db/selfcheck.go` + `dgw selfcheck` + serve 拒启）。
