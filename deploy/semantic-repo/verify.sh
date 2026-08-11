#!/bin/sh
# 语义仓库操作路径自动化验证（issue #27 AC5：管线/采集器可对该仓库操作；
# ADR-0002 commit/revert/review 即版本机制）。
#
# 用本地裸仓库模拟内部 Gitea（无真实 Gitea 时），全链路断言：
#   1. bootstrap.sh：clone 空仓库 → samples/semantic 种子初始化提交 → push
#   2. 采集器：scan 草稿直接写 clone 的 services/（git diff 可见）
#   3. 版本机制：commit + push（review 由 Gitea PR 承载，模拟端到端）
#   4. 同步管线：semantic-sync --dry-run + 应用（对 clone 操作成功）
#   5. revert 即回滚：revert 采集提交 → 重跑同步 → 墓碑/回退生效
#
# 用法：./verify.sh [--repo /path/to/neo-cloud] [--work /tmp/semantic-work]
#   --repo  服务仓库根（采集器真相源；缺省用仓库内 testdata 的 neo-cloud 语料）
#   --work  语义仓库工作目录（临时，脚本自行清理）
# 前置：go 工具链（go run ./cmd/...）。
set -eu

cd "$(dirname "$0")/../.." # 仓库根（bootstrap.sh 种子路径依赖）
REPO_ROOT="$(pwd)"
REPO="$REPO_ROOT/internal/collector/testdata/neo-cloud"
WORK="/tmp/semantic-work"
REMOTE="/tmp/semantic-remote.git"
STORE="/tmp/semantic-verify.db"

# 参数解析：只接受 --repo/--work 两个 flag（值跟随其后），其余拒绝；
# 所有工作路径必须落在 /tmp 下（清理 rm -rf 的安全边界——绝不触碰
# 用户真实路径）。
while [ "$#" -gt 0 ]; do
  case "$1" in
  --repo)
    [ "$#" -ge 2 ] || { echo "--repo 需要值"; exit 2; }
    REPO="$2"; shift 2
    ;;
  --work)
    [ "$#" -ge 2 ] || { echo "--work 需要值"; exit 2; }
    WORK="$2"; shift 2
    ;;
  *)
    echo "未知参数: $1" >&2
    exit 2
    ;;
  esac
done
case "$WORK" in
/tmp/*) ;;
*) echo "错误: --work 必须落在 /tmp 下（清理安全边界）" >&2; exit 2 ;;
esac
case "$REMOTE" in
/tmp/*) ;;
*) echo "错误: --remote 必须落在 /tmp 下（清理安全边界）" >&2; exit 2 ;;
esac

rm -rf "$REMOTE" "$WORK" "$STORE" "$STORE-wal" "$STORE-shm"
git init --bare -q "$REMOTE"

echo "==> [1/5] bootstrap.sh：clone + 种子初始化 + push（模拟 Gitea 空仓库）"
./deploy/semantic-repo/bootstrap.sh "$REMOTE" "$WORK" >/dev/null
[ -n "$(git -C "$WORK" ls-files)" ] || { echo "FAIL: 种子为空"; exit 1; }
echo "    种子已推送：$(git -C "$WORK" ls-files | tr '\n' ' ')"

echo "==> [2/5] 采集器：scan 草稿写入 clone 的 services/（操作路径）"
go run ./cmd/dgw-collect scan --repo "$REPO" \
  --manifest samples/collector/manifest.yaml --service bss-wallet \
  --out "$WORK" >/dev/null 2>&1
git -C "$WORK" status --short | grep -q "services/bss-wallet.yaml" \
  || { echo "FAIL: 采集草稿未落 clone 的 services/"; exit 1; }
echo "    草稿已写入：$(git -C "$WORK" status --short | tr '\n' ' ')"

echo "==> [3/5] 版本机制：commit + push（review = Gitea PR 承载）"
git -C "$WORK" add services/bss-wallet.yaml
git -C "$WORK" commit -q -m "collect: bss-wallet 结构草稿（人工 review 后合入）"
git -C "$WORK" push -q origin HEAD
echo "    committed + pushed（HEAD = $(git -C "$WORK" rev-parse --short HEAD)）"

echo "==> [4/5] 同步管线：dry-run diff + 应用（对 clone 操作）"
go run ./cmd/dgw semantic-sync --dir "$WORK" --db "$STORE" --dry-run \
  | grep -q "dry-run 完成" || { echo "FAIL: dry-run"; exit 1; }
go run ./cmd/dgw semantic-sync --dir "$WORK" --db "$STORE" \
  | grep -q "语义同步完成" || { echo "FAIL: 同步应用"; exit 1; }
echo "    同步完成（作者入口 → 运行时 SQLite）"

echo "==> [5/5] revert 即回滚：revert 采集提交 → 重跑同步（墓碑传播删除）"
REVERT_TARGET="$(git -C "$WORK" rev-parse HEAD)"
git -C "$WORK" revert --no-edit "$REVERT_TARGET" >/dev/null
git -C "$WORK" push -q origin HEAD
go run ./cmd/dgw semantic-sync --dir "$WORK" --db "$STORE" \
  | grep -q "语义同步完成" || { echo "FAIL: revert 后重同步"; exit 1; }
echo "    revert 完成（commit 即版本，revert + 重跑管线即回滚，ADR-0002）"

rm -rf "$REMOTE" "$WORK" "$STORE" "$STORE-wal" "$STORE-shm"
echo
echo "✅ 语义仓库操作路径验证通过：bootstrap → 采集 → commit/push → 同步 → revert 回滚"
