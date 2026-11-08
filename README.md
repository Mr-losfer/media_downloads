# Media Super

> 为低配 NAS 打造的视频下载器：**单 Go 二进制、常驻内存约 15MB、m3u8 多线程拉流 + 无损转封装成 mp4**。
> 自带 Web 管理台、**REST API** 与 **MCP（Model Context Protocol）**，支持 Docker、飞牛 fnOS（fpk）与裸机运行。

```
https://github.com/losfer778/media_downloads
```

当前版本见 [version.json](version.json)，历史构建见 [docs/RELEASES.md](docs/RELEASES.md)。

## 为什么是它

- **轻**：`CGO_ENABLED=0` 静态编译，成品约 7MB；无 Node、无 Python、无数据库服务。
  实测 Celeron J1800（2 核 / 1GB 可用）并发下载时**峰值 RSS 16MB**。
- **快**：m3u8 分片并发拉取（默认 8 连接/任务），直链走 HTTP Range 多段并行 + `WriteAt` 直接落盘。
- **不烧 CPU**：m3u8 → mp4 默认 ffmpeg `-c copy` 重封装（不解码），只有编码不兼容时才降级转码。
- **省磁盘**：分片边拼接边删除，峰值临时占用约等于一份视频体积。
- **实时**：SSE 推送进度，也可配 Webhook 回调事件。
- **可自更新**：设置页内置「检查更新 / 一键更新」，优先走 `gh-proxy.com`，异常自动直连 GitHub。

## 功能

| 能力 | 说明 |
|---|---|
| 协议 | HLS(m3u8) / mp4 / flv / ts / mkv / webm / mov / 音频 |
| HLS | master 多码率自动选最高（可切最低）、`EXT-X-KEY` AES-128 解密、`EXT-X-BYTERANGE`、`EXT-X-MAP`(fMP4 init 段) |
| 下载 | 分片并发池 + 单分片重试；直链 N 段 Range 并行；不支持 Range 自动降级单流 |
| 转封装 | `-c copy` + `+faststart`（moov 前置，浏览器秒开可拖动）；失败可降级转码 |
| 管理 | 预览播放、下载、改名（同步改磁盘文件）、删除（可选删文件）、取消、重试、搜索、分页 |
| 缩略图 | 完成后自动抽帧生成封面 |
| 设置 | 并发任务/分片连接/全局限速/UA/代理/忽略 TLS/ffmpeg 路径/转码开关/保留临时文件/自动清理/多保存目录/Webhook |
| 系统 | 版本信息、更新检测、一键更新、修改管理员密码 |
| 鉴权 | 后台密码（PBKDF2-SHA256 120k 轮）+ 签名 Cookie；API/MCP 用 Bearer API Key |
| 健壮性 | 重启后未完成任务自动重排；启动清理残留分片；磁盘 <1GB 拒收新任务；设置越界自动夹紧 |
| 界面 | 移动优先单页应用：底部导航 + 底部抽屉、44px 触控命中区、SSE 增量刷新、内置 API / MCP 文档页、PWA 可加到主屏 |

## 快速开始

### 方式一：Docker Compose（推荐，直接拉镜像）

```bash
curl -fsSLO https://raw.githubusercontent.com/losfer778/media_downloads/main/docker-compose.yml
mkdir -p data downloads
docker compose up -d
# 打开 http://<NAS-IP>:9099 ，默认密码 losfer
```

想改下载目录：`DOWNLOAD_DIR=/vol1/1000/影视 docker compose up -d`。
完整说明（含单容器 `docker run`）见 [docs/DEPLOY.md](docs/DEPLOY.md)。

### 方式二：单条 docker run

```bash
docker run -d --name mediasuper --restart unless-stopped \
  -p 9099:8080 \
  -e TZ=Asia/Shanghai \
  -e MS_ADMIN_PASS=losfer \
  -e MS_SAVE_DIRS="/downloads:下载" \
  -v $(pwd)/data:/data \
  -v /vol1/1000/影视:/downloads \
  ghcr.io/losfer778/media-super:latest
```

支持 `linux/amd64`、`linux/arm64`、`linux/arm/v7`。

### 方式三：飞牛 fnOS（fpk）

到 [Releases](https://github.com/losfer778/media_downloads/releases) 下载 `mediasuper_<版本>_fnos-x86.fpk` 或 `_fnos-arm.fpk`，
在应用中心手动安装即可。打包与调试见 [docs/FNOS.md](docs/FNOS.md)。

### 方式四：裸机二进制

```bash
curl -fsSL -o mediasuper \
  https://gh-proxy.com/https://github.com/losfer778/media_downloads/releases/latest/download/mediasuper-linux-amd64
chmod +x mediasuper
MS_ADMIN_PASS=losfer MS_SAVE_DIRS="/data/videos:本地" ./mediasuper -addr :8080 -data ./data
```

需要系统里有 `ffmpeg`（`apk add ffmpeg` / `apt install ffmpeg`）。没装也能下 m3u8 分片，但无法合成 mp4。

## 首次登录

- 地址：`http://<NAS-IP>:9099`
- 默认密码：`losfer`（登录后到「设置 → 管理员密码」修改）
- API Key：启动日志里会打印，后台「设置 → API 与 MCP」也能复制

## 环境变量（仅首次启动生效）

| 变量 | 默认 | 说明 |
|---|---|---|
| `MS_ADDR` | `:8080` | 监听地址 |
| `MS_DATA_DIR` | `/data` | 数据库 + 临时分片 + 待安装更新 |
| `MS_ADMIN_PASS` | `losfer` | 后台密码，首次启动写入数据库 |
| `MS_SAVE_DIRS` | `/downloads:下载` | `路径:标签,路径:标签` |
| `MS_DEFAULT_DIR` | 第一个目录 | 默认保存目录 |
| `MS_MAX_TASKS` | `2` | 同时下载任务数 |
| `MS_SEG_CONNS` | `8` | 单任务并发连接数 |
| `MS_SPEED_LIMIT_KB` | `0` | 全局限速，0 不限 |
| `MS_FFMPEG` | `ffmpeg` | ffmpeg 可执行路径 |
| `MS_WEBHOOK_URL` | 空 | 事件回调地址 |

> 旧的 `VF_*` 变量仍然兼容。启动后这些值都存进了数据库，改环境变量不再生效——请到「设置」页改。

## REST API

所有接口需 `Authorization: Bearer <API Key>`（浏览器后台另可用 Cookie）。
播放类接口额外支持 `?token=<API Key>`，方便 `<video>` / EventSource 使用。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/health` | 健康检查（无需鉴权） |
| POST | `/api/login` | `{"password":"..."}` → 设置会话 Cookie |
| GET | `/api/me` | 当前身份 + API Key |
| GET | `/api/tasks?status=&q=&limit=&offset=` | 任务列表 |
| POST | `/api/tasks` | `{"url":"…","title":"…","save_dir":"…","headers":{"Referer":"…"}}` |
| GET | `/api/tasks/{id}` | 单个任务 |
| PATCH | `/api/tasks/{id}` | `{"title":"新名字"}` 改名 |
| DELETE | `/api/tasks/{id}?file=true` | 删除任务（`file=true` 连文件一起删） |
| POST | `/api/tasks/{id}/cancel` | 取消 |
| POST | `/api/tasks/{id}/retry` | 重试 |
| GET | `/api/tasks/{id}/stream` | 在线播放（支持 Range/206） |
| GET | `/api/tasks/{id}/download` | 下载文件 |
| GET | `/api/tasks/{id}/thumb` | 缩略图 jpg |
| GET | `/api/events` | SSE 实时事件 |
| GET/PUT | `/api/settings` | 读/改设置（PUT 只传要改的字段） |
| GET | `/api/stats` | 统计 + 各磁盘剩余空间 |
| GET | `/api/browse?path=/` | 列目录（选保存路径用） |
| GET | `/api/system/info` | 版本、架构、运行环境 |
| GET | `/api/system/update` | 检查更新（`?force=1` 跳过缓存） |
| POST | `/api/system/update` | 下载并应用更新，随后重启 |
| POST | `/api/system/password` | `{"old_password":"…","new_password":"…"}` |
| POST | `/mcp` | MCP JSON-RPC 2.0 端点 |

```bash
KEY=ms_xxxxxxxx
curl -s -X POST http://192.168.1.123:9099/api/tasks \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"url":"https://example.com/hls/index.m3u8","title":"测试视频"}'

curl -s http://192.168.1.123:9099/api/tasks -H "Authorization: Bearer $KEY"
```

## MCP 接入

端点：`POST http://<NAS-IP>:9099/mcp`，鉴权 `Authorization: Bearer <API Key>`。
协议：JSON-RPC 2.0 / Streamable HTTP，支持 `initialize`、`tools/list`、`tools/call`、`ping`。

工具（11 个）：`submit_download`、`list_tasks`、`get_task`、`cancel_task`、`retry_task`、
`delete_task`、`rename_task`、`get_download_link`、`get_settings`、`update_settings`、`get_stats`

支持 HTTP 直连的客户端（Cherry Studio / Cline / 自研 Agent）：

```json
{
  "mcpServers": {
    "mediasuper": {
      "type": "http",
      "url": "http://192.168.1.123:9099/mcp",
      "headers": { "Authorization": "Bearer ms_xxxxxxxx" }
    }
  }
}
```

只支持 stdio 的客户端（Claude Desktop）：

```json
{
  "mcpServers": {
    "mediasuper": {
      "command": "docker",
      "args": ["exec", "-i", "mediasuper", "/usr/local/bin/mediasuper",
               "-stdio", "-remote", "http://127.0.0.1:8080", "-key", "ms_xxxxxxxx"]
    }
  }
}
```

## 更新

两种方式，任选：

1. **后台一键更新**：设置 → 系统更新 → 检查更新 → 一键更新。下载校验后热切换进程，容器重启也保留。
2. **换镜像**：`docker compose pull && docker compose up -d`。

细节与原理（gh-proxy 优先、SHA256 校验、失败回滚）见 [docs/UPDATE.md](docs/UPDATE.md)。

## 低配 NAS 调优

| 场景 | 建议 |
|---|---|
| 2 核 / 1~2GB 内存 | `max_tasks=2`、`seg_conns=8`（默认就是） |
| 只想晚上下、不影响看片 | `speed_limit_kb` 设成带宽的 60% |
| 机械盘 + 大文件 | 保持 `-c copy`（默认）；`+faststart` 会整盘重写一遍，1GB 视频约多花几十秒 |
| 网络差 | 调大 `max_retries`；单任务连接数别超过 16，容易触发 CDN 限速 |
| 存储紧张 | 设 `auto_clean_days=7` 自动清理；下载中峰值只占 1× 视频体积 |

容器已内置保护：`mem_limit 512m`、`cpus 1.0`、`pids_limit 256`。

## 目录结构

```
cmd/mediasuper/         程序入口（参数解析 / -version / -stdio）
internal/app/           装配层：数据库、设置、HTTP 服务、优雅退出
internal/engine/        下载引擎：调度、m3u8、Range 分段、ffmpeg、SSE、Webhook
internal/server/        HTTP：REST、鉴权、MCP、静态资源、系统接口
internal/updater/       更新检测 / 下载校验 / 热切换
internal/version/       版本号（构建时 -ldflags 注入）
web/                    前端（原生 JS，embed 进二进制）
deploy/docker/          Dockerfile、构建用 compose
deploy/fnos/            fnOS fpk 模板（manifest / cmd / config / ui）
scripts/                build-release.sh、build-fpk.sh、git-push.sh
docs/                   部署、更新、fnOS、架构、版本列表
.github/workflows/      Release 与多架构镜像流水线
```

详细设计见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)。

## 文档

| 文档 | 内容 |
|---|---|
| [docs/DEPLOY.md](docs/DEPLOY.md) | Docker 单容器 / Compose / 镜像源 / 目录权限 |
| [docs/UPDATE.md](docs/UPDATE.md) | 更新检测、一键更新、代理策略、回滚 |
| [docs/FNOS.md](docs/FNOS.md) | fpk 打包流程、安装、日志排查 |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 代码结构与关键流程 |
| [docs/RELEASES.md](docs/RELEASES.md) | 历史构建列表 |

## 常见问题

**m3u8 下完了但 mp4 打不开？**
`-c copy` 只换容器不解码。若源编码 mp4 不支持（例如某些 H.265/AC3 组合），开启「允许转码」后会自动降级转码（J1800 上很慢，慎用）。

**403 / 401？**
需要防盗链头。提交时填 Referer / Cookie，或用 API 的 `headers` 字段。

**直播流（没有 `#EXT-X-ENDLIST`）？**
会下载当前窗口内的分片并合成，不做持续录制。

**任务重启后从头开始？**
是的，分片不做断点续传（避免错位）。已完成的文件不受影响。

**忘记后台密码？**
删掉数据目录里的 `mediasuper.db` 会重置为 `losfer`，但任务记录与 API Key 也会一起丢失。

## 许可

MIT
