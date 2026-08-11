# 采集工作流 Skill：结构知识 → 语义仓库（采集 → review → 合入 → 同步）

封装 `dgw-collect`（采集器，ADR-0007）：结构知识自动、语义知识人工；触发 = **手动 on-demand**（无轮询/定时）。输出是语义作者入口 YAML 草稿，写入语义仓库 clone 的 `services/`。

## 全流程

**1. 跑采集**（结构 → YAML 草稿，三道闸：迁移解析 → GORM 交叉验证 → 编译兼容检查）：

```sh
dgw-collect scan --repo ~/cloud/neo-cloud \
  --manifest samples/collector/manifest.yaml \
  --out <语义仓库 clone>        # 草稿写到 <clone>/services/（git diff 可见）
# --service NAME 只采清单内一个服务（增量）；--no-gorm 跳过第二道闸
```

退出码：`0` = 门禁全过；`2` = 有 error 级发现（**草稿照写**，交人确认）；`1` = 操作失败（未产出）。

清单里不配 `db` 的服务（纯编排/聚合/事件采集类，如 ops-operation / dashboard-backend / usage-collection）= 无持库服务：只产出服务实体草稿（无表结构），保证语义层覆盖全部后端服务。

**2. 引导人工 review**：

```sh
git -C <clone> diff          # 自查草稿（结构以新采集为准）
```

语义内容（描述/业务概念/指标口径/枚举含义）Agent 起草 + 服务负责人确认后回写（US-15/16）；纯结构变更走批量确认（US-16，PR review 形态）。**重跑采集不覆盖已确认语义**：`services/*.yaml` 的语义字段（description / is_time / 枚举 label）按 FQN 合并保留，diff 里只剩结构变化（ADR-0007「结构自动、语义人工」）。

**3. 合入**（git review 闸门：commit 即版本）：

```sh
git -C <clone> add . && git -C <clone> commit -m "collect: <服务> 结构更新"
git -C <clone> push origin HEAD   # Gitea 上开 PR → 人工 review → merge
```

**4. 手动触发同步**（作者入口 → 运行时 SQLite；编译校验失败原子拒绝）：

```sh
git -C <clone> pull --rebase
dgw semantic-sync --dir <clone> --dry-run   # 先看 diff（增删改清单）
dgw semantic-sync --dir <clone>             # 幂等 upsert + 墓碑软删除
```

## 可选：生产校准（v1 低优先）

```sh
dgw-collect calibrate --repo ~/cloud/neo-cloud \
  --manifest samples/collector/manifest.yaml --service bss-wallet --dsn <从库 URL>
# DSN 也可经 DGW_COLLECT_DSN env 传入，避免 argv 泄露口令
```

连只读从库对照 `information_schema` 出**漂移报告，只报告不改**（exit 2 = 有 error 级漂移）。

## 回滚（commit 即版本）

`git revert <commit>` → 重跑 `semantic-sync`（墓碑传播删除，ADR-0002）；完整操作路径见 [`deploy/semantic-repo/README.md`](../semantic-repo/README.md)。
