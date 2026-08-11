# 语义仓库初始化与本地 clone 操作路径（ADR-0002 §4.2 / ADR-0009 §4.8；US-21/24）

作者入口（按服务拆 YAML + 全局指标/概念文件）落**内部 Gitea 独立语义仓库**；
commit/revert/review 即版本机制与变更闸门。本目录 = 仓库初始化与操作路径
的落地：`bootstrap.sh` 打通本地 clone（种子初始化 + pull），管线/采集器
对该 clone 直接读写。

## 一次性初始化

```sh
# 1. 内部 Gitea 上建空仓库（如 team/semantic）
# 2. 本机初始化（空仓库 → 用 samples/semantic 种子提交并推送）
./deploy/semantic-repo/bootstrap.sh git@gitea.internal:team/semantic.git ./semantic-repo
```

之后同一工作目录重跑 bootstrap = `git pull --rebase` 刷新（增量路径）。

## 标准工作流（采集 → review → 合入 → 同步）

```sh
# 采集器：结构知识 → YAML 草稿（写在 clone 的 services/ 下）
dgw-collect scan --repo ~/cloud/neo-cloud \
  --manifest samples/collector/manifest.yaml \
  --out ./semantic-repo/services

# 自查 diff（草稿会覆盖同名服务文件）
git -C ./semantic-repo diff

# 语义内容（描述/概念/指标口径）Agent 起草 + 服务负责人确认后回写（US-15/16）；
# 纯结构变更走批量确认（US-16，PR review 形态）。
git -C ./semantic-repo add . && git -C ./semantic-repo commit -m "collect: <服务> 结构更新"
git -C ./semantic-repo push origin HEAD      # Gitea 上开 PR → 人工 review → merge

# 同步管线：作者入口 → 运行时 SQLite（编译校验 → dry-run diff → 应用）
git -C ./semantic-repo pull --rebase
dgw semantic-sync --dir ./semantic-repo     # 先 --dry-run 看 diff
```

## 回滚（commit 即版本）

```sh
git -C ./semantic-repo revert <commit>
dgw semantic-sync --dir ./semantic-repo     # 墓碑传播删除（ADR-0002）
```

## 运行时备份/恢复（回滚基线 = 旧镜像 tag + SQLite 备份，ADR-0009）

```sh
dgw semantic-backup --out /backup/semantic-$(date +%F).db   # WAL checkpoint + 文件拷贝
# 恢复：停网关 → 拷贝备份回 /data → 起网关（旧镜像 tag 亦可）
```

## 校验（验收标准第 5 条：管线/采集器可对语义仓库操作）

- 采集器 `--out` 指向 clone 的 `services/` → 草稿即仓库变更（git diff 可见）；
- 同步管线 `--dir` 指向 clone → 编译校验原子拒绝 + dry-run diff + 幂等应用；
- 本地无 Gitea 时可先用裸仓库模拟远端验证路径：

  ```sh
  git init --bare /tmp/semantic-remote.git
  ./deploy/semantic-repo/bootstrap.sh /tmp/semantic-remote.git /tmp/semantic-work
  ```
