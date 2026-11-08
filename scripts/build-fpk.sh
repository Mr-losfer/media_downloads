#!/usr/bin/env bash
#
# 全自动生成飞牛 fnOS 的 .fpk 安装包（x86 与 arm 各一个）。
#
#   ./scripts/build-fpk.sh              # 先编译再打包
#   VERSION=2.2.0 ./scripts/build-fpk.sh
#   SKIP_BUILD=1 ./scripts/build-fpk.sh # 复用 dist/ 里已有的二进制
#   BUNDLE_FFMPEG=0 ./scripts/build-fpk.sh # 不内置 ffmpeg（仅调试用）
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

# 与 build-release.sh 共用同一份「更新内容」文件
if [ -z "${NOTES:-}" ] && [ -f .release-notes ]; then
  NOTES="$(sed -e 's/[[:space:]]*$//' .release-notes)"
fi
CHANGELOG="${NOTES:-$(git log -1 --pretty=%s 2>/dev/null || echo "Media Super v${VERSION}")}"
# manifest 是 key=value 文本，fnpack 会把 # 当注释、= 当分隔、换行当多条记录。
# 这里只取更新内容首行做摘要，并去掉 **重点** / 反引号等标记，避免多行被截断成乱码。
CHANGELOG="$(printf '%s' "$CHANGELOG" | head -n 1 | sed -e 's/[`*]//g' -e 's/#/ /g' -e 's/=/-/g' | cut -c1-240)"

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

# ---------- 准备静态 ffmpeg ----------
# fnOS 不保证系统 PATH 里有 ffmpeg，安装包自带一份静态构建，
# 否则 m3u8 下完分片会在封装阶段报「ffmpeg: executable file not found」。
FFMPEG_BUNDLE="${BUNDLE_FFMPEG:-1}"
prepare_ffmpeg() {
  local goarch="$1"
  local dir="${OUT_DIR}/tools/ffmpeg-${goarch}"
  local bin="${dir}/ffmpeg"
  if [ -x "$bin" ]; then
    echo "$bin"
    return 0
  fi
  mkdir -p "$dir" "${OUT_DIR}/tools/cache"

  # 用 BtbN 的 GPL 构建：自带 libx264 / libx265，mpegts 解封装与重封装都验证过。
  # 先走 gh-proxy（国内 NAS / CI 都稳），失败再直连 GitHub。
  local file=""
  case "$goarch" in
    amd64) file="ffmpeg-master-latest-linux64-gpl.tar.xz" ;;
    arm64) file="ffmpeg-master-latest-linuxarm64-gpl.tar.xz" ;;
  esac
  local gh="https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/${file}"
  local urls=("https://gh-proxy.com/${gh}" "${gh}")

  local url tar tmp try ok
  for url in "${urls[@]}"; do
    tar="${OUT_DIR}/tools/cache/ffmpeg-${goarch}-btbn.tar.xz"
    tmp="${OUT_DIR}/tools/cache/unpack-${goarch}"
    echo "==> 下载 ffmpeg (${goarch})：${url}" >&2
    try=0; ok=0
    # 上次已经完整下好的包直接用，避免重复下载
    if [ -f "$tar" ] && tar -tJf "$tar" >/dev/null 2>&1; then
      ok=1
    fi
    while [ "$ok" = 0 ] && [ "$try" -lt 2 ]; do
      # 这个源偶尔很慢，断点续传 + 低于 8KB/s 持续 60 秒就重来，避免 CI 卡死
      if curl -fsSL -C - --connect-timeout 20 --speed-time 60 --speed-limit 8192 \
          -o "$tar" "$url" >&2; then
        ok=1
        break
      fi
      rm -f "$tar"
      try=$((try + 1))
      echo "    下载中断，重试 ${try}/2" >&2
    done
    [ "$ok" = 1 ] || { rm -f "$tar"; continue; }

    rm -rf "$tmp"
    mkdir -p "$tmp"
    if ! tar -xJf "$tar" -C "$tmp" >&2; then
      rm -rf "$tmp"; rm -f "$tar"
      continue
    fi
    # BtbN 的包解压后是 <dir>/bin/ffmpeg，这里用 find 兼容其他可能的布局
    local found
    found="$(find "$tmp" -type f -name ffmpeg -perm -u+x | head -n 1)"
    if [ -n "$found" ]; then
      cp "$found" "$bin"
      chmod 0755 "$bin"
    fi
    rm -rf "$tmp"; rm -f "$tar"
    if [ -x "$bin" ]; then
      echo "$bin"
      return 0
    fi
  done
  echo "!! 所有 ffmpeg 下载源都失败" >&2
  return 1
}

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
  # 更新说明来自 commit，可能含 # & / 等字符，先转义再交给 sed
  local safe_changelog
  safe_changelog="$(printf '%s' "$CHANGELOG" | sed -e 's/[\\&#]/\\&/g')"
  sed -e "s#@VERSION@#${VERSION}#g" \
      -e "s#@PLATFORM@#${platform}#g" \
      -e "s#@CHANGELOG@#${safe_changelog}#g" \
      "${FNOS_DIR}/manifest.tmpl" > "$dir/manifest"

  mkdir -p "$dir/app/server"
  install -m 0755 "$src" "$dir/app/server/${APP}"

  # 内置 ffmpeg：cmd/main 会优先指向它，引擎也会自动发现
  if [ "${FFMPEG_BUNDLE}" = "1" ]; then
    local ffbin
    if ! ffbin="$(prepare_ffmpeg "$goarch")"; then
      echo "!! 内置 ffmpeg 下载失败；可设 BUNDLE_FFMPEG=0 跳过（不推荐，m3u8 将无法转 mp4）" >&2
      return 1
    fi
    mkdir -p "$dir/app/bin"
    install -m 0755 "$ffbin" "$dir/app/bin/ffmpeg"
    echo "    内置 ffmpeg $(du -h "$dir/app/bin/ffmpeg" | cut -f1)"
  fi

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
