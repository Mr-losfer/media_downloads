#!/usr/bin/env bash
#
# 全自动生成飞牛 fnOS 的 .fpk 安装包（x86 与 arm 各一个）。
#
#   ./scripts/build-fpk.sh              # 先编译再打包
#   VERSION=2.2.0 ./scripts/build-fpk.sh
#   SKIP_BUILD=1 ./scripts/build-fpk.sh # 复用 dist/ 里已有的二进制
#
# 依赖 fnpack（官方打包工具）。本机没有时会自动从 fnnas 静态站下载到 build/tools/。
# 参考：https://developer.fnnas.com/docs/cli/fnpack/
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

APP="mediasuper"
FNOS_DIR="deploy/fnos"
OUT_DIR="build"
WORK="${OUT_DIR}/fpk"

if [ -z "${VERSION:-}" ]; then
  VERSION="$(sed -n 's/^[[:space:]]*Version[[:space:]]*=[[:space:]]*"\(.*\)"/\1/p' internal/version/version.go | head -1)"
fi
[ -n "$VERSION" ] || { echo "无法确定版本号" >&2; exit 1; }

CHANGELOG="${NOTES:-$(git log -1 --pretty=%s 2>/dev/null || echo "Media Super v${VERSION}")}"

# ---------- 准备 fnpack ----------
FNPAK="${FNPAK:-fnpack}"
if ! command -v "$FNPAK" >/dev/null 2>&1; then
  mkdir -p "${OUT_DIR}/tools"
  case "$(uname -m)" in
    x86_64|amd64)  farch=amd64 ;;
    aarch64|arm64) farch=arm64 ;;
    *) echo "不支持的打包机架构: $(uname -m)" >&2; exit 1 ;;
  esac
  FNPAK="${OUT_DIR}/tools/fnpack"
  if [ ! -x "$FNPAK" ]; then
    echo "==> 下载 fnpack (linux-${farch})"
    curl -fsSL -o "$FNPAK" "https://static2.fnnas.com/fnpack/fnpack-1.2.3-linux-${farch}"
    chmod +x "$FNPAK"
  fi
fi
# 后面会 cd 到打包目录，先把 fnpack 路径转成绝对路径
case "$FNPAK" in
  /*) ;;
  *) FNPAK="$(pwd)/${FNPAK}" ;;
esac
echo "==> fnpack: $("$FNPAK" --help 2>&1 | grep -i '^Version' || echo "$FNPAK")"

# ---------- 编译二进制 ----------
if [ "${SKIP_BUILD:-0}" -eq 0 ]; then
  echo "==> 编译 linux/amd64 与 linux/arm64"
  for plat in amd64 arm64; do
    CGO_ENABLED=0 GOOS=linux GOARCH="$plat" \
      go build -trimpath \
        -ldflags "-s -w \
          -X mediasuper/internal/version.Version=${VERSION} \
          -X mediasuper/internal/version.Commit=$(git rev-parse --short HEAD 2>/dev/null || echo dev) \
          -X mediasuper/internal/version.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        -o "dist/${APP}-linux-${plat}" ./cmd/mediasuper
  done
fi

mkdir -p "$OUT_DIR"
rm -rf "$WORK"
mkdir -p "$WORK"

build_one() {
  local platform="$1"   # x86 | arm
  local goarch="$2"     # amd64 | arm64
  local src="dist/${APP}-linux-${goarch}"
  local dir="${WORK}/${platform}"

  if [ ! -f "$src" ]; then
    echo "!! 缺少 $src，先用 scripts/build-release.sh 或去掉 SKIP_BUILD" >&2
    return 1
  fi

  echo "==> 组装 ${platform} (${goarch})"
  mkdir -p "$dir"
  cp -a "${FNOS_DIR}/." "$dir/"
  rm -f "$dir/manifest.tmpl"
  sed -e "s#@VERSION@#${VERSION}#g" \
      -e "s#@PLATFORM@#${platform}#g" \
      -e "s#@CHANGELOG@#${CHANGELOG}#g" \
      "${FNOS_DIR}/manifest.tmpl" > "$dir/manifest"

  mkdir -p "$dir/app/server"
  install -m 0755 "$src" "$dir/app/server/${APP}"

  # 生命周期脚本必须可执行
  chmod +x "$dir"/cmd/*

  # 去掉可能混进来的 Mac 垃圾文件
  find "$dir" -name '.DS_Store' -delete

  ( cd "$WORK" && "$FNPAK" build --directory "$platform" )
  if [ ! -f "${WORK}/${APP}.fpk" ]; then
    echo "!! fnpack 没有生成 fpk" >&2
    return 1
  fi
  local out="${OUT_DIR}/${APP}_${VERSION}_fnos-${platform}.fpk"
  mv "${WORK}/${APP}.fpk" "$out"
  echo "    $(du -h "$out" | cut -f1)  $out"
}

build_one x86 amd64
build_one arm arm64

echo "==> fpk 打包完成"
ls -lh "${OUT_DIR}"/*.fpk
