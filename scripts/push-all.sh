#!/usr/bin/env bash
#
# push-all.sh —— 把两个仓库推上 GitHub
#
# 用法：
#   GH_TOKEN=github_pat_xxx bash scripts/push-all.sh
#
# 为什么单独写个脚本：token 只在环境变量里过一遍，**不落盘、不进 git config**，
# 避免手滑把凭据写进仓库。推完自己就把 remote 还原成不带 token 的地址。
#
# 前置：两个空仓库要先在 GitHub 上建好（不要勾 README / .gitignore / license，
# 否则远端有初始提交，首次推送会因历史不相关被拒）。

set -euo pipefail

OWNER="${GH_OWNER:-Vincentkeio}"
PROJ_DIR="${PROJ_DIR:-D:/workbuddycache/kokoro探针}"
BENCH_DIR="${BENCH_DIR:-D:/workbuddycache/kokoro-bench}"

[ -n "${GH_TOKEN:-}" ] || { echo "缺少 GH_TOKEN 环境变量" >&2; exit 1; }

push_one() {
  local dir=$1 repo=$2 name=$3
  echo "=== 推送 $name → $OWNER/$repo ==="
  cd "$dir"
  git remote remove origin 2>/dev/null || true
  # token 只在这里出现一次，写在命令行里而不是 remote 配置里
  git push "https://${GH_TOKEN}@github.com/${OWNER}/${repo}.git" HEAD:main --force
  git remote add origin "https://github.com/${OWNER}/${repo}.git"
  echo "  完成: https://github.com/${OWNER}/${repo}"
}

push_one "$BENCH_DIR" "kokoro-bench" "测试脚本"
push_one "$PROJ_DIR"  "kokoro"       "探针主项目"

echo
echo "两个仓库都推好了："
echo "  https://github.com/$OWNER/kokoro"
echo "  https://github.com/$OWNER/kokoro-bench"
