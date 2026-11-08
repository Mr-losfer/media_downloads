# ---------- 构建阶段 ----------
# 多架构构建由 buildx 自动注入 TARGETOS / TARGETARCH / TARGETVARIANT，
# 本地 docker build 时会退回 amd64。
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG TARGETVARIANT=
ARG VERSION=0.0.0
ARG COMMIT=unknown
ARG BUILD_TIME=

ENV CGO_ENABLED=0 \
    GOOS=$TARGETOS \
    GOARCH=$TARGETARCH \
    GOARM=${TARGETVARIANT#v} \
    GOFLAGS=-mod=mod \
    GOPROXY=https://goproxy.cn,direct \
    GOMAXPROCS=2

WORKDIR /src
# 依赖单独成层：改代码不会重新下载模块
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN LDFLAGS="-s -w \
      -X mediasuper/internal/version.Version=${VERSION} \
      -X mediasuper/internal/version.Commit=${COMMIT} \
      -X mediasuper/internal/version.BuildTime=${BUILD_TIME}" \
    && go build -trimpath -p 2 -ldflags "$LDFLAGS" -o /out/mediasuper ./cmd/mediasuper \
    && ls -lh /out/mediasuper

# ---------- 运行阶段 ----------
FROM alpine:3.21

# ffmpeg 用于 m3u8 → mp4 的 -c copy 重封装（含 ffprobe 读时长）
RUN apk add --no-cache ca-certificates tzdata ffmpeg tini \
    && addgroup -g 1000 -S app \
    && adduser -u 1000 -S -G app -h /home/app app \
    && mkdir -p /data /downloads /home/app \
    && chown -R app:app /data /downloads /home/app

ENV TZ=Asia/Shanghai \
    MS_ADDR=:8080 \
    MS_DATA_DIR=/data \
    MS_FFMPEG=/usr/bin/ffmpeg

COPY --from=build /out/mediasuper /usr/local/bin/mediasuper

LABEL org.opencontainers.image.title="Media Super" \
      org.opencontainers.image.description="轻量视频下载管理器（m3u8 / mp4 / flv / ts），带 Web 管理台、REST API 与 MCP" \
      org.opencontainers.image.source="https://github.com/losfer778/media_downloads" \
      org.opencontainers.image.url="https://github.com/losfer778/media_downloads" \
      org.opencontainers.image.licenses="MIT"

USER 1000:1000
WORKDIR /home/app
VOLUME ["/data"]
EXPOSE 8080

HEALTHCHECK --interval=60s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/api/health >/dev/null || exit 1

# tini 负责回收僵尸进程，让一键更新时的 exec 重启更干净
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/mediasuper"]
