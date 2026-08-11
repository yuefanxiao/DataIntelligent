#!/bin/sh
# 语义仓库本地 clone 初始化 + 工作流引导（ADR-0002/0009；US-21/24）。
#
# 语义仓库 = 独立 git 仓库（内部 Gitea），commit/revert/review 即版本机制
# 与变更闸门；作者入口（services/ + metrics.yaml + concepts.yaml）只经它
# 进运行时（dgw semantic-sync）。本脚本打通「本地 clone 操作路径」：
#
#   clone → （空仓库时以 samples/semantic 种子初始化提交）→ pull 刷新
#
# 用法：
#   ./deploy/semantic-repo/bootstrap.sh <gitea-仓库-URL> [<工作目录>]
#     URL       Gitea 语义仓库地址（如 git@gitea.internal:team/semantic.git）
#     工作目录  缺省 ./semantic-repo
#
# 初始化后的标准工作流（详见 deploy/semantic-repo/README.md）：
#   1. 跑采集器出草稿：dgw-collect scan --repo ~/cloud/neo-cloud \
#        --manifest samples/collector/manifest.yaml --out <工作目录>/services
#   2. 人工 review（Gitea PR）→ 合入
#   3. 本机 pull → dgw semantic-sync --dir <工作目录>（进运行时）
#   4. 回滚 = git revert + 重跑同步管线（墓碑传播删除）
set -eu

URL="${1:-}"
WORK="${2:-./semantic-repo}"
SEED="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)/samples/semantic"

if [ -z "$URL" ]; then
  echo "用法: $0 <gitea-仓库-URL> [<工作目录>]" >&2
  exit 2
fi

if [ -d "$WORK/.git" ]; then
  echo "==> 工作目录已是 git 仓库：git pull 刷新"
  git -C "$WORK" pull --rebase
else
  echo "==> 克隆语义仓库：$URL → $WORK"
  git clone "$URL" "$WORK"
fi

if [ -z "$(git -C "$WORK" ls-files)" ]; then
  echo "==> 仓库为空：以 samples/semantic 作为初始作者入口并推送"
  mkdir -p "$WORK/services"
  cp "$SEED/metrics.yaml" "$SEED/concepts.yaml" "$SEED/README.md" "$WORK/"
  cp "$SEED"/services/*.yaml "$WORK/services/"
  git -C "$WORK" add .
  git -C "$WORK" commit -m "chore: 语义仓库初始作者入口（samples/semantic 种子）"
  git -C "$WORK" push -u origin HEAD
else
  echo "==> 仓库已有内容，跳过种子初始化"
fi

cat <<'EOF'

============================================================
语义仓库本地操作路径已打通。标准工作流：

  采集    dgw-collect scan --repo ~/cloud/neo-cloud \
            --manifest samples/collector/manifest.yaml \
            --out <工作目录>/services
  review  草稿经 Gitea PR 人工 review（纯结构变更可批量确认，US-16）
  合入    Gitea merge（commit 即版本）
  同步    git pull && dgw semantic-sync --dir <工作目录>
  回滚    git revert <commit> && dgw semantic-sync --dir <工作目录>

注意：采集草稿会覆盖 <工作目录>/services/ 下同名服务文件；review
前先 git diff 自查。语义内容（描述/概念/指标）由服务负责人确认后
回写（US-15/16），结构内容由采集器机械产出（ADR-0007）。
EOF
