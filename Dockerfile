# ---------- 构建阶段 ----------
FROM golang:1.24-alpine AS build

ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOFLAGS=-mod=mod \
    GOPROXY=https://goproxy.cn,direct \
    GOMAXPROCS=2

WORKDIR /src
# 依赖单独成层：改代码不会重新下载模块
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -trimpath -p 2 -ldflags "-s -w" -o /out/vidfetch . \
    && ls -lh /out/vidfetch

# ---------- 运行阶段 ----------
FROM alpine:3.21

# ffmpeg 用于 m3u8 → mp4 的 -c copy 重封装（含 ffprobe 读时长）
RUN apk add --no-cache ca-certificates tzdata ffmpeg \
    && addgroup -g 1000 -S app \
    && adduser -u 1000 -S -G app -h /home/app app \
    && mkdir -p /data /downloads /home/app \
    && chown -R app:app /data /downloads /home/app

ENV TZ=Asia/Shanghai \
    VF_ADDR=:8080 \
    VF_DATA_DIR=/data \
    VF_FFMPEG=/usr/bin/ffmpeg

COPY --from=build /out/vidfetch /usr/local/bin/vidfetch

USER 1000:1000
WORKDIR /home/app
VOLUME ["/data"]
EXPOSE 8080

HEALTHCHECK --interval=60s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/api/health >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/vidfetch"]
