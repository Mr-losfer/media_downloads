# Media Super

> 为低配 NAS 打造的视频下载器：**单 Go 二进制、常驻内存约 15MB、m3u8 多线程拉流 + 无损转封装成 mp4**。
> 同时提供 **REST API** 与 **MCP（Model Context Protocol）** 两种接入方式，自带 Web 管理页。

```
https://github.com/losfer778/media_downloads
```

## 为什么是它

- **轻**：`CGO_ENABLED=0` 静态编译，成品 6.9MB；无 Node、无 Python、无数据库服务。
  实测在 Celeron J1800（2 核，可用内存 1GB）上并发下载时 **峰值 RSS 16MB**。
- **快**：m3u8 分片并发拉取（默认 8 连接/任务），直链走 HTTP Range 多段并行 + `WriteAt` 直接落盘，不需要二次合并。
- **不烧 CPU**：m3u8 → mp4 默认用 ffmpeg `-c copy` 重封装（不解码），只在不兼容时才降级转码。
- **省磁盘**：分片边拼接边删除，峰值临时占用只有一份视频体积。
- **实时**：SSE 推送进度（前端进度条 + 速度 + ETA），也可配 Webhook 回调事件。

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
| 鉴权 | 后台密码（PBKDF2-SHA256 120k 轮）+ 签名 Cookie；API/MCP 用 Bearer API Key |
| 健壮性 | 重启后未完成任务自动重排；启动清理残留分片；磁盘 <1GB 拒收新任务；设置越界自动夹紧 |
| 界面 | 移动优先单页应用：底部导航 + 底部抽屉、44px 触控命中区、SSE 增量刷新（列表不重绘）、内置 API / MCP 文档页（示例一键复制）、可加到主屏（PWA 图标与主题色） |

## 管理界面

原生 JS 单页，`embed` 进二进制，无构建链、无 CDN、无字体下载：

- **任务**：粘链接回车即下；进度条 + 速度 + 剩余时间实时走；预览、下载、改名、删除、取消、重试都是图标按钮（带 aria-label）
- **文档**：`#/docs/api` 与 `#/docs/mcp`，Base URL 自动取当前访问地址（设置里填了 `public_base` 则用公网地址），curl 与客户端配置可一键复制
- **设置**：并发/分片/限速/UA/代理/转码/清理/Webhook/保存目录，目录支持递归浏览选择
- **移动端**：≤420px 时任务操作换行成独立按钮行；`hover:none` 设备命中区自动放大到 44px；输入框 16px 防 iOS 聚焦缩放；底部导航带 `safe-area-inset` 适配刘海屏

## 快速开始

```bash
# 1. 拉代码
git clone https://github.com/losfer778/media_downloads.git && cd media_downloads

# 2. 准备目录与密码
mkdir -p data downloads
echo 'VF_ADMIN_PASS=你的后台密码' > .env && chmod 600 .env

# 3. 起服务（首次构建镜像，J1800 上约 2~3 分钟）
docker compose up -d --build

# 4. 打开
#   http://<NAS-IP>:9099
```

**升级**：`git pull && docker compose up -d --build`（`data/` 里的任务记录和 API Key 会保留）。

首次启动日志里会打印 **API Key**，后台「设置 → API 与 MCP」也能看到。

### 不用 Docker

```bash
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o mediasuper .
VF_ADMIN_PASS=xxx VF_SAVE_DIRS="/data/videos:本地" ./mediasuper -addr :8080 -data ./data
```

需要系统里有 `ffmpeg`（`apk add ffmpeg` / `apt install ffmpeg`）。没装也能下 m3u8 分片，但无法合成 mp4。

## 环境变量（仅首次启动生效）

| 变量 | 默认 | 说明 |
|---|---|---|
| `VF_ADDR` | `:8080` | 监听地址 |
| `VF_DATA_DIR` | `/data` | 数据库 + 临时分片目录 |
| `VF_ADMIN_PASS` | `mediasuper123` | 后台密码，首次启动写入数据库 |
| `VF_SAVE_DIRS` | `/downloads/hdd:硬盘` | `路径:标签,路径:标签` |
| `VF_DEFAULT_DIR` | 第一个目录 | 默认保存目录 |
| `VF_MAX_TASKS` | `2` | 同时下载任务数 |
| `VF_SEG_CONNS` | `8` | 单任务并发连接数 |
| `VF_SPEED_LIMIT_KB` | `0` | 全局限速，0 不限 |
| `VF_FFMPEG` | `ffmpeg` | ffmpeg 可执行路径 |
| `VF_WEBHOOK_URL` | 空 | 事件回调地址 |

> 启动后这些值都存进了数据库，改环境变量不再生效——请到「设置」页改，或删掉 `data/` 重来。

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
| POST | `/mcp` | MCP JSON-RPC 2.0 端点 |

示例：

```bash
KEY=vf_xxxxxxxx
curl -s -X POST http://192.168.1.123:9099/api/tasks \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"url":"https://example.com/hls/index.m3u8","title":"测试视频"}'

curl -s http://192.168.1.123:9099/api/tasks -H "Authorization: Bearer $KEY"
```

## MCP 接入

端点：`POST http://<NAS-IP>:9099/mcp`，鉴权 `Authorization: Bearer <API Key>`。
协议：JSON-RPC 2.0 / Streamable HTTP，支持 `initialize`、`tools/list`、`tools/call`、`ping`。

### 工具清单（11 个）

`submit_download`、`list_tasks`、`get_task`、`cancel_task`、`retry_task`、
`delete_task`、`rename_task`、`get_download_link`、`get_settings`、`update_settings`、`get_stats`

### 客户端配置（只支持 stdio 的客户端走代理模式）

Claude Desktop 之类：

```json
{
  "mcpServers": {
    "mediasuper": {
      "command": "docker",
      "args": ["exec", "-i", "mediasuper", "/usr/local/bin/mediasuper",
               "-stdio", "-remote", "http://127.0.0.1:8080", "-key", "vf_xxxxxxxx"]
    }
  }
}
```

或本机有二进制时：

```json
{
  "mcpServers": {
    "mediasuper": {
      "command": "/usr/local/bin/mediasuper",
      "args": ["-stdio", "-remote", "http://192.168.1.123:9099", "-key", "vf_xxxxxxxx"]
    }
  }
}
```

支持 HTTP 直连的客户端（Cherry Studio / Cline / 自研 Agent）直接填 URL + Bearer 即可。

## SSE 与 Webhook

### SSE

```
GET /api/events            （Cookie 或 ?token=API Key）
event: hello    → {"version":"1.1.0"}
event: snapshot → [任务数组]
event: task     → 单个任务（变化即推，服务端 500ms 节流）
event: stats    → 统计信息（每 3 秒）
event: task.deleted → {"id":"…"}
: ping          → 每 20 秒心跳
```

### Webhook

配置后每个事件 POST JSON：

```json
{ "event": "task.completed", "time": 1730000000000, "task": { …完整任务对象… } }
```

请求头：`X-VF-Event`、`X-VF-Signature: sha256=<hmac>`（配置了密钥时）。
验签：`HMAC_SHA256(secret, raw_body)`，用常量时间比较。
事件：`task.created` / `task.started` / `task.completed` / `task.failed` / `task.canceled`。
失败重试 2 次（间隔 2s），超时 8s，异步不阻塞下载。

## 低配 NAS 调优

| 场景 | 建议 |
|---|---|
| 2 核 / 1~2GB 内存 | `max_tasks=2`、`seg_conns=8`（默认就是） |
| 只想晚上下、不影响看片 | `speed_limit_kb` 设成带宽的 60% |
| 机械盘 + 大文件 | 保持 `-c copy`（默认）；`+faststart` 会整盘重写一遍，1GB 视频约多花几十秒 |
| 网络差 | 调大 `max_retries`；单任务连接数别超过 16，容易触发 CDN 限速 |
| 存储紧张 | 设 `auto_clean_days=7` 自动清理；下载中峰值只占 1× 视频体积 |

容器已内置保护：`mem_limit 512m`、`cpus 1.0`、`pids_limit 256`。

## 常见问题

**m3u8 下完了但 mp4 打不开？**
`-c copy` 只换容器不解码。若源编码 mp4 不支持（例如某些 H.265/AC3 组合），开启「允许转码」后会自动降级转码（J1800 上很慢，慎用）。

**403 / 401？**
需要防盗链头。提交时填 Referer / Cookie，或用 API 的 `headers` 字段。

**直播流（没有 `#EXT-X-ENDLIST`）？**
会下载当前窗口内的分片并合成，不做持续录制。

**任务重启后从头开始？**
是的，分片不做断点续传（避免错位）。已完成的文件不受影响。

**浏览器不能播放某种编码？**
服务端只做容器转换，播放能力取决于浏览器。HEVC 建议客户端播放器。

## 目录结构

```
main.go      启动、内嵌前端、stdio 代理
model.go     任务/设置数据结构
store.go     bbolt 持久化
manager.go   调度器、并发、进度、清理
hls.go       m3u8 解析 + 分片并发下载 + AES 解密 + 拼接
download.go  探测 + Range 多段并行 + 单流下载
ffmpeg.go    重封装 / 转码 / 缩略图 / 时长
api.go       REST + SSE + 静态资源
mcp.go       MCP JSON-RPC
auth.go      PBKDF2 / 会话 / API Key
sse.go       事件广播
webhook.go   事件回调
web/         前端（原生 JS，embed 进二进制）
```

## 许可

MIT
