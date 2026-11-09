#!/usr/bin/env bash
#
# 全自动生成飞牛 fnOS 的 .fpk 安装包（x86 与 arm 各一个）。
#
#   ./scripts/build-fpk.sh              # 先编译再打包（fpk 保持轻量）
#   VERSION=2.2.0 ./scripts/build-fpk.sh
#   SKIP_BUILD=1 ./scripts/build-fpk.sh # 复用 dist/ 里已有的二进制
#   BUNDLE_FFMPEG=1 ./scripts/build-fpk.sh # 把 ffmpeg 塞进包（不推荐，见下）
#   EMIT_FFMPEG_ASSETS=0 ./scripts/build-fpk.sh # 跳过组件产出（仅本地调试）
#
# 为什么默认不内置 ffmpeg（重要）：
#   ffmpeg 静态二进制约 168MB，塞进 app.tgz 会让 fpk 从 3MB 暴涨到 72MB。
#   fpk 是单个 gzip 流且 app.tgz 排在最前面，旧版 fnOS 手动安装「上传超过一分钟
#   会报错」，Web 上传被截断后排在后面的 cmd/main 等生命周期脚本根本没落盘，
#   于是报「缺少生命周期脚本 /cmd/main，安装包可能不完整」。
#   因此 fpk 只放主程序，ffmpeg 作为独立压缩组件随 Release 发布，应用首次启动
#   在后台自动下载到 <数据目录>/bin/ffmpeg（gh-proxy 优先、SHA256 校验）。
#   新版 fnOS / 纯离线环境可显式 BUNDLE_FFMPEG=1 自行构建内置版。
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

# 轻量版体积上限：正常约 3MB，超过 16MB 说明又被塞了大文件，直接拦下
MAX_LIGHT_FPK_SIZE=$((16 * 1024 * 1024))

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
# fnOS 不保证系统 PATH 里有 ffmpeg。这里下载一份静态构建，产出两份东西：
#   1) BUNDLE_FFMPEG=1 时直接塞进 fpk（默认关闭，见文件头说明）
#   2) 压缩成 ffmpeg-linux-<arch>.gz，作为运行时自动补齐的 Release 附件
FFMPEG_BUNDLE="${BUNDLE_FFMPEG:-0}"
EMIT_FFMPEG_ASSETS="${EMIT_FFMPEG_ASSETS:-1}"
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

# ---------- 打包后自检：杜绝「装不上」这类结构问题 ----------
verify_fpk() {
  local fpk="$1" dir
  dir="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$dir'" RETURN
  local fail=0
  say_fail() { echo "!! fpk 自检失败 [$fpk]: $1" >&2; fail=1; }

  tar -xzf "$fpk" -C "$dir" 2>/dev/null || { echo "!! fpk 无法解包: $fpk" >&2; return 1; }

  # 顶层 7 项齐备
  for p in app.tgz cmd config wizard manifest ICON.PNG ICON_256.PNG; do
    [ -e "$dir/$p" ] || say_fail "缺少顶层条目 $p"
  done
  # fnOS 要求的 9 个生命周期脚本：存在、可执行、语法正确
  for sc in main install_init install_callback config_init config_callback \
            upgrade_init upgrade_callback uninstall_init uninstall_callback; do
    local f="$dir/cmd/$sc"
    [ -f "$f" ] || { say_fail "缺少生命周期脚本 cmd/$sc"; continue; }
    [ -x "$f" ] || say_fail "cmd/$sc 没有执行权限"
    bash -n "$f" 2>/dev/null || say_fail "cmd/$sc 语法错误"
  done
  # checksum 必须等于 app.tgz 的 MD5（fnOS 安装时会校验）
  local want got
  want="$(sed -n 's/^[[:space:]]*checksum[[:space:]]*=[[:space:]]*//p' "$dir/manifest" | tr -d '[:space:]')"
  got="$(md5sum "$dir/app.tgz" 2>/dev/null | cut -d' ' -f1)"
  [ -n "$want" ] && [ "$want" = "$got" ] || say_fail "manifest.checksum 与 app.tgz 不一致 ($want / $got)"
  # wizard / config 必须是合法 JSON
  for j in "$dir"/wizard/* "$dir"/config/*; do
    [ -f "$j" ] || continue
    python3 -c 'import json,sys;json.load(open(sys.argv[1]))' "$j" >/dev/null 2>&1 \
      || say_fail "$(basename "$j") 不是合法 JSON"
  done
  # 轻量版体积守卫
  local sz
  sz="$(stat -c%s "$fpk" 2>/dev/null || stat -f%z "$fpk")"
  if [ "$FFMPEG_BUNDLE" = "0" ] && [ "$sz" -gt "$MAX_LIGHT_FPK_SIZE" ]; then
    say_fail "轻量版体积 ${sz} 超过上限 ${MAX_LIGHT_FPK_SIZE}，请检查是否误内置了大文件"
  fi

  if [ "$fail" -eq 0 ]; then
    echo "    自检通过：9 个生命周期脚本 / checksum / JSON / 体积 ($(du -h "$fpk" | cut -f1))"
  fi
  return "$fail"
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

  # 可选：把 ffmpeg 塞进包。默认关闭——大包在旧版 fnOS 上会装不上（见文件头）。
  if [ "${FFMPEG_BUNDLE}" = "1" ]; then
    local ffbin
    if ! ffbin="$(prepare_ffmpeg "$goarch")"; then
      echo "!! 内置 ffmpeg 下载失败；可设 BUNDLE_FFMPEG=0（推荐）" >&2
      return 1
    fi
    mkdir -p "$dir/app/bin"
    install -m 0755 "$ffbin" "$dir/app/bin/ffmpeg"
    echo "    ⚠ 已内置 ffmpeg：包体会变大，旧版 fnOS 手动安装可能失败"
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
  verify_fpk "$out"
}

# ---------- 产出运行时自动补齐用的 ffmpeg 组件 ----------
# gzip 而非 xz：Go 标准库 compress/gzip 直接解压，零第三方依赖。
emit_ffmpeg_assets() {
  [ "${EMIT_FFMPEG_ASSETS}" = "1" ] || return 0
  echo "==> 产出 ffmpeg 运行时组件"
  for goarch in amd64 arm64; do
    local ffbin out
    if ! ffbin="$(prepare_ffmpeg "$goarch")"; then
      echo "!! ffmpeg 组件产出失败 (${goarch})" >&2
      return 1
    fi
    out="${OUT_DIR}/ffmpeg-linux-${goarch}.gz"
    gzip -9 -c "$ffbin" > "$out"
    echo "    $(du -h "$out" | cut -f1)  $out"
  done
}

build_one x86 amd64
build_one arm arm64
emit_ffmpeg_assets

echo "==> fpk 打包完成"
ls -lh "${OUT_DIR}"/*.fpk "${OUT_DIR}"/ffmpeg-linux-*.gz 2>/dev/null
