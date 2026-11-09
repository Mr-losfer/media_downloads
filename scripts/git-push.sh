#!/usr/bin/env bash
#
# 推送脚本：优先走 gh-proxy.com，代理异常（比如它不支持 git push 鉴权）时自动直连。
#
#   ./scripts/git-push.sh            # 推送当前分支到 origin 同名分支
#   ./scripts/git-push.sh main       # 指定分支
#
# Token 来源优先级：GITHUB_TOKEN / GH_TOKEN 环境变量 → gh auth token → 已配置的 credential helper。
# 仓库地址里的 token 不会被写进 .git/config，避免泄露。
#
set -uo pipefail

REPO_PATH="Mr-losfer/media_downloads.git"
PROXY_URL="https://gh-proxy.com/https://github.com/${REPO_PATH}"
DIRECT_URL="https://github.com/${REPO_PATH}"

BRANCH="${1:-$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo main)}"
[ "$BRANCH" = "HEAD" ] && BRANCH=main

TOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
if [ -z "$TOKEN" ] && command -v gh >/dev/null 2>&1; then
  TOKEN="$(gh auth token 2>/dev/null || true)"
fi

echo "==> 尝试通过 gh-proxy 推送 ${BRANCH}"
if timeout 30 git push "$PROXY_URL" "HEAD:${BRANCH}" 2>/dev/null; then
  echo "    已通过 gh-proxy 推送成功"
  exit 0
fi

echo "==> 代理不可用，改用 GitHub 直连"
if [ -n "$TOKEN" ]; then
  if git push "https://${TOKEN}@github.com/${REPO_PATH}" "HEAD:${BRANCH}"; then
    echo "    已通过直连推送成功"
    exit 0
  fi
else
  if git push "$DIRECT_URL" "HEAD:${BRANCH}"; then
    echo "    已通过直连推送成功"
    exit 0
  fi
fi

echo "!! 推送失败：检查网络、Token 权限（需要 repo 写权限）" >&2
exit 1
