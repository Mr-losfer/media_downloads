#!/usr/bin/env bash
#
# 打多架构发布包，并（可选）发布到 GitHub Releases + 更新仓库内的版本列表。
#
#   ./scripts/build-release.sh                     # 只本地打包
#   ./scripts/build-release.sh --publish           # 打包 + 建 Release + 上传附件
#   ./scripts/build-release.sh --publish --commit   # 再回写 version.json / RELEASES.md 并推送
#
# 产物：
#   dist/mediasuper-linux-<arch>               裸二进制（一键更新直接下载这个）
#   dist/mediasuper_<ver>_linux_<arch>.tar.gz  带 README/LICENSE 的压缩包
#   dist/SHA256SUMS                            校验和
#   version.json                               更新检测的元数据（会被提交回仓库）
#   docs/RELEASES.md                           人类可读的版本列表
#
set -euo pipefail

REPO="losfer778/media_downloads"
IMAGE="ghcr.io/losfer778/media-super"
APP="mediasuper"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PUBLISH=0
COMMIT=0
SKIP_BUILD=0
for arg in "$@"; do
  case "$arg" in
    --publish) PUBLISH=1 ;;
    --commit) COMMIT=1 ;;
    --no-build) SKIP_BUILD=1 ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "未知参数: $arg" >&2; exit 1 ;;
  esac
done

if [ -z "${VERSION:-}" ]; then
  VERSION="$(sed -n 's/^[[:space:]]*Version[[:space:]]*=[[:space:]]*"\(.*\)"/\1/p' internal/version/version.go | head -1)"
fi
[ -n "$VERSION" ] || { echo "无法确定版本号，请用 VERSION=2.1.0 指定" >&2; exit 1; }

COMMIT_SHA="$(git rev-parse --short HEAD 2>/dev/null || echo dev)"
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# 「更新内容」优先读仓库根目录的 .release-notes：它可以是多行、带 feat:/fix:/
# **重点** 标记的文本，会原样进入 version.json 与 Release body，供前端弹窗高亮展示。
# 没有该文件时退回最近一次 commit subject，保持老流程可用。
if [ -z "${NOTES:-}" ]; then
  if [ -f .release-notes ]; then
    NOTES="$(sed -e 's/[[:space:]]*$//' .release-notes)"
  else
    NOTES="$(git log -1 --pretty=%s 2>/dev/null || echo "Media Super v${VERSION}")"
  fi
fi
# 版本列表是 Markdown 表格，说明列只能用一行：取首行并去掉高亮标记与竖线
NOTES_ROW="$(printf '%s' "$NOTES" | head -n 1 | sed -e 's/[`*]//g' -e 's/|/\//g' -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
[ -n "$NOTES_ROW" ] || NOTES_ROW="Media Super v${VERSION}"

TARGETS=(
  "linux/amd64/amd64/"
  "linux/arm64/arm64/"
  "linux/arm/armv7/7"
  "linux/arm/armv6/6"
  "linux/386/386/"
)

echo "==> 构建 Media Super v${VERSION} (${COMMIT_SHA})"
mkdir -p dist
if [ "$SKIP_BUILD" -eq 0 ]; then
  rm -f dist/mediasuper-* dist/SHA256SUMS dist/*.tar.gz
  for t in "${TARGETS[@]}"; do
    IFS='/' read -r goos goarch label goarm <<<"$t"
    out="dist/${APP}-${goos}-${label}"
    printf '    %-28s' "$out"
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" GOARM="$goarm" \
      go build -trimpath \
        -ldflags "-s -w \
          -X mediasuper/internal/version.Version=${VERSION} \
          -X mediasuper/internal/version.Commit=${COMMIT_SHA} \
          -X mediasuper/internal/version.BuildTime=${BUILT_AT}" \
        -o "$out" ./cmd/mediasuper
    printf '%s\n' "$(du -h "$out" | cut -f1)"
  done
fi

echo "==> 压缩 + 校验和"
for t in "${TARGETS[@]}"; do
  IFS='/' read -r goos goarch label goarm <<<"$t"
  bin="dist/${APP}-${goos}-${label}"
  [ -f "$bin" ] || continue
  stage="$(mktemp -d)"
  cp "$bin" "$stage/${APP}"
  cp README.md LICENSE "$stage/"
  tar -C "$stage" -czf "dist/${APP}_${VERSION}_${goos}_${label}.tar.gz" "${APP}" README.md LICENSE
  rm -rf "$stage"
done
( cd dist && sha256sum mediasuper-* > SHA256SUMS )

# ---------- 生成 version.json ----------
json_escape() {
  # 转义反斜杠、双引号、控制字符，换行转 \n
  printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' -e ':a' -e 'N' -e '$!ba' -e 's/\n/\\n/g' -e 's/\r//g' -e 's/\t/\\t/g'
}

ASSETS_JSON=""
FIRST=1
for t in "${TARGETS[@]}"; do
  IFS='/' read -r goos goarch label goarm <<<"$t"
  bin="dist/${APP}-${goos}-${label}"
  [ -f "$bin" ] || continue
  size="$(stat -c%s "$bin" 2>/dev/null || stat -f%z "$bin")"
  sha="$(cd dist && sha256sum "${APP}-${goos}-${label}" | cut -d' ' -f1)"
  url="https://github.com/${REPO}/releases/download/v${VERSION}/${APP}-${goos}-${label}"
  [ "$FIRST" -eq 1 ] || ASSETS_JSON+=","$'\n'
  FIRST=0
  ASSETS_JSON+="    \"${goos}-${label}\": { \"name\": \"${APP}-${goos}-${label}\", \"url\": \"${url}\", \"sha256\": \"${sha}\", \"size\": ${size} }"
done

FPK_JSON=""
if ls build/*.fpk >/dev/null 2>&1; then
  FPK_JSON=$'\n  "fpk": {'
  ffirst=1
  for f in build/*.fpk; do
    name="$(basename "$f")"
    sz="$(stat -c%s "$f" 2>/dev/null || stat -f%z "$f")"
    sh="$(sha256sum "$f" | cut -d' ' -f1)"
    [ "$ffirst" -eq 1 ] || FPK_JSON+=","
    ffirst=0
    FPK_JSON+=$'\n'"    \"${name}\": { \"url\": \"https://github.com/${REPO}/releases/download/v${VERSION}/${name}\", \"sha256\": \"${sh}\", \"size\": ${sz} }"
  done
  FPK_JSON+=$'\n  },'
fi

cat > version.json <<EOF
{
  "version": "${VERSION}",
  "released_at": "${BUILT_AT}",
  "notes": "$(json_escape "$NOTES")",
  "repo": "https://github.com/${REPO}",
  "image": "${IMAGE}:${VERSION}",
  "assets": {
${ASSETS_JSON}
  },${FPK_JSON}
  "checksums": "https://github.com/${REPO}/releases/download/v${VERSION}/SHA256SUMS"
}
EOF

# ---------- 更新人类可读的版本列表 ----------
mkdir -p docs
LIST="docs/RELEASES.md"
if [ ! -f "$LIST" ]; then
  cat > "$LIST" <<'EOF'
# 版本列表

每次 `scripts/build-release.sh` 会自动在下面追加一行，构建产物同时上传到 GitHub Releases。

| 版本 | 构建时间 | 架构 | 镜像 | 说明 |
| --- | --- | --- | --- | --- |
EOF
fi
ROW="| [v${VERSION}](https://github.com/${REPO}/releases/tag/v${VERSION}) | ${BUILT_AT} | amd64 / arm64 / armv7 / armv6 / 386 | \`${IMAGE}:${VERSION}\` | ${NOTES_ROW} |"
# 同版本重复构建时先删掉旧行，避免版本列表出现重复
grep -v "releases/tag/v${VERSION})" "$LIST" > "${LIST}.2" && mv "${LIST}.2" "$LIST"
# 插入到表头分隔线之后（保持新版本在最上面）
awk -v row="$ROW" '
  { print }
  /^\| --- / && !done { print row; done=1 }
' "$LIST" > "${LIST}.tmp" && mv "${LIST}.tmp" "$LIST"

echo "==> version.json 已更新"
echo "    version: ${VERSION}"
echo "    文件:    $(ls dist | wc -l) 个"

# ---------- 发布到 GitHub ----------
if [ "$PUBLISH" -eq 1 ]; then
  echo "==> 创建 GitHub Release v${VERSION}"
  GH_BIN="${GH_BIN:-gh}"
  ASSETS=()
  for f in dist/*.tar.gz dist/${APP}-* dist/SHA256SUMS; do
    [ -f "$f" ] || continue
    case "$f" in *SHA256SUMS|*tar.gz|*mediasuper-linux-*) ASSETS+=("$f") ;; esac
  done
  for f in build/*.fpk; do [ -f "$f" ] && ASSETS+=("$f"); done

  if "$GH_BIN" release view "v${VERSION}" --repo "$REPO" >/dev/null 2>&1; then
    echo "    Release 已存在，改为上传/覆盖附件"
    "$GH_BIN" release upload "v${VERSION}" "${ASSETS[@]}" --repo "$REPO" --clobber
  else
    "$GH_BIN" release create "v${VERSION}" "${ASSETS[@]}" \
      --repo "$REPO" \
      --title "Media Super v${VERSION}" \
      --notes "$NOTES" \
      --latest
  fi
  echo "    https://github.com/${REPO}/releases/tag/v${VERSION}"
fi

# ---------- 把版本列表提交回仓库 ----------
if [ "$COMMIT" -eq 1 ]; then
  echo "==> 提交 version.json / docs/RELEASES.md"
  git add version.json docs/RELEASES.md
  if git diff --cached --quiet; then
    echo "    没有变化，跳过提交"
  else
    git commit -q -m "chore(release): v${VERSION} 构建产物与版本列表"
    ./scripts/git-push.sh
  fi
fi

echo "==> 完成"
