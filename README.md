# Media Super

> 为低配 NAS 打造的视频下载器：**单 Go 二进制、常驻内存约 15MB、m3u8 多线程拉流 + 无损转封装成 mp4**。
> 自带 Web 管理台、**REST API** 与 **MCP（Model Context Protocol）**，支持 Docker、飞牛 fnOS（fpk）与裸机运行。

```
https://github.com/Mr-losfer/media_downloads
```

当前版本见 [version.json](version.json)，历史构建见 [docs/RELEASES.md](docs/RELEASES.md)。

版本号规则：`主.次.补丁`，补丁位 0→9 逐次加一（2.5.1、2.5.2 …），到 9 之后进位到次版本（2.6.0），不跳号。

## 为什么是它

- **轻**：`CGO_ENABLED=0` 静态编译，成品约 7MB；无 Node、无 Python、无数据库服务。
  实测 Celeron J1800（2 核 / 1GB 可用）并发下载时**峰值 RSS 16MB**。
- **快**：m3u8 分片并发拉取（默认 8 连接/任务），直链走 HTTP Range 多段并行 + `WriteAt` 直接落盘。
- **不烧 CPU**：m3u8 → mp4 默认 ffmpeg `-c copy` 重封装（不解码），只有编码不兼容时才降级转码。
- **省磁盘**：分片边拼接边删除，峰值临时占用约等于一份视频体积。
- **实时**：SSE 推送进度，也可配 Webhook 回调事件。
- **可自更新**：设置页内置「检查更新 / 一键更新」，优先走 `gh-proxy.com`，异常自动直连 GitHub；
  打开「自动更新」后每 2 小时巡检一次，只在没有下载任务时自动升级。

## 功能

| 能力 | 说明 |
|---|---|
| 协议 | HLS(m3u8) / mp4 / flv / ts / mkv / webm / mov / 音频 |
| 链接解析 | 首页粘贴公众号文章 `mp.weixin.qq.com/s/*` 自动解析标题/封面/作者/简介/图片，把视频直链列成可勾选下载对象（一般 mp4、无需合成）；命中的插件由域名匹配决定 |
| 插件系统 | **文件夹即插件**：`plugin.json` 声明匹配域名、可视化配置项与执行规则，Go 插件可含多个 `.go` 文件；三种引擎 `builtin` / `declarative`（零编译）/ `go`（子进程 JSON 协议）；插件页支持本地上传、在线市场、启停、配置弹窗、文件编辑、差量修改、编译、测试/调试、导出；公众号解析已纳入插件体系；MCP 提供 25 个开发插件工具 |
| HLS | master 多码率自动选最高（可切最低）、`EXT-X-KEY` AES-128 解密、`EXT-X-BYTERANGE`、`EXT-X-MAP`(fMP4 init 段)、可去掉片头/片尾 N 个分片（黑屏或台标） |
| 下载 | 分片并发池 + 单分片重试；直链 N 段 Range 并行；不支持 Range 自动降级单流 |
| 防重复下载 | 提交时**只按链接**判断，同一个链接已有任务（排队 / 下载中 / 已完成）直接拦截，返回 `409` 且不创建第二条；「设置 → 下载」可开启**标题防重**，同标题也拦截。失败 / 已取消的任务不拦截 |
| 转封装 | `-c copy` + `+faststart`（moov 前置，浏览器秒开可拖动）；失败可降级转码 |
| 管理 | 预览播放、下载、改名（同步改磁盘文件）、删除（可选删文件）、取消、重试、搜索、分页 |
| 缩略图 | 完成后自动抽帧生成封面 |
| 设置 | 并发任务/分片连接/全局限速/UA/代理/忽略 TLS/ffmpeg 路径/转码开关/保留临时文件/自动清理/多保存目录/Webhook/标题防重开关 |
| 系统 | 版本信息、更新检测、一键更新、首页新版本弹窗（可跳过当前版本）、自动更新（2 小时巡检 / 空闲升级）、修改管理员密码 |
| 鉴权 | 后台密码（PBKDF2-SHA256 120k 轮）+ 签名 Cookie；API/MCP 用 Bearer API Key |
| 健壮性 | 重启后未完成任务自动重排；启动清理残留分片；磁盘 <1GB 拒收新任务；设置越界自动夹紧 |
| 界面 | 移动优先单页应用：手机端四页底部导航、桌面端（≥1024px）左侧栏导航（首页 = 按时段问候 + 下载卡片 / 任务 / 插件 / 设置）、插件页内置「插件开发文档」入口、文档页含 REST API / 插件开发 / MCP 三个分类、设置内再按更新/下载/存储/通知/安全分类、提交后自动跳转任务页、夜间 / 白天一键切换（圆形水波过渡）、手机端关闭橡皮筋回弹、底部抽屉、SSE 增量刷新、PWA 可加到主屏 |

### 公众号文章怎么下载

首页粘贴 `https://mp.weixin.qq.com/s/xxxx` 点「下载」，先展示解析结果（标题、封面、作者、简介、发布时间、
文章图片与正文摘要），并把文章里的视频直链列成可勾选列表（默认全选）；点「下载选中」批量入队并跳到任务页。
视频一般是 **mp4 直链，不走 m3u8 合成、不需要 ffmpeg**。纯图文文章会提示「没有解析到视频」。

解析能力是**插件化**的：`internal/plugin` 负责插件规范、目录、配置、匹配与执行，`internal/parser` 定义统一
`Result` / `Item` 结构。插件页「配置」可改公众号解析的接口地址、超时、Token 与「解析后自动下载」。
新增站点支持不用改主程序：在插件页上传一个文件夹，或在 `marketplace/` 里加一个插件并登记 `marketplace.json`。
完整规范见 **[docs/PLUGINS.md](docs/PLUGINS.md)** 与应用内「插件 → 插件开发文档」。

## 快速开始

### 方式一：Docker Compose（推荐，直接拉镜像）

```bash
curl -fsSLO https://raw.githubusercontent.com/Mr-losfer/media_downloads/main/docker-compose.yml
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
  ghcr.io/mr-losfer/media-super:latest
```

支持 `linux/amd64`、`linux/arm64`、`linux/arm/v7`。

### 方式三：飞牛 fnOS（fpk）

到 [Releases](https://github.com/Mr-losfer/media_downloads/releases) 下载 `mediasuper_<版本>_fnos-x86.fpk` 或 `_fnos-arm.fpk`，
在应用中心手动安装即可。**安装向导里可直接设置管理员密码和 Web 端口**（密码留空则为 `losfer`，
端口默认 `9099`），装好后也能在应用中心 →「配置」里随时修改。
安装包保持**轻量（约 3MB）**；首次启动会在后台自动补齐 ffmpeg 组件，补齐后 m3u8 下完即可封装成 mp4，
不依赖 NAS 系统里是否装了 ffmpeg。进度与手动重试见「设置 → 下载」，打包与调试见 [docs/FNOS.md](docs/FNOS.md)。

### 方式四：FnDepot 第三方商店（外部源）

在 [FnDepot](https://github.com/EWEDLCM/FnDepot) 客户端（版本 `> 0.0.7`）添加外部源：

```
https://github.com/Mr-losfer/media_downloads
```

商店里会出现 **Media Super**，按设备架构自动选 x86 / arm 安装包，安装向导同样可自定义密码与端口。
源索引为仓库根目录的 [`fnpack.json`](fnpack.json)（V2 格式），复用已发布的 fpk 附件并带 SHA256 校验，
上架/换图标**不需要发布新版本**。完整字段、校验与发版规则见 [docs/FNDEPOT.md](docs/FNDEPOT.md)。

### 方式五：裸机二进制

```bash
curl -fsSL -o mediasuper \
  https://gh-proxy.com/https://github.com/Mr-losfer/media_downloads/releases/latest/download/mediasuper-linux-amd64
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
| `MS_MP_PARSE_API` | `https://t.losfer.cn/apis/parse/mp/parseArticle` | 公众号解析服务地址，可换自建服务 |

> 旧的 `VF_*` 变量仍然兼容。启动后这些值都存进了数据库，改环境变量不再生效——请到「设置」页改。
> `MS_MP_PARSE_API` 例外：它属于解析插件，每次启动读取、不写入数据库，改完重启即可生效。

## REST API

所有接口需 `Authorization: Bearer <API Key>`（浏览器后台另可用 Cookie）。
播放类接口额外支持 `?token=<API Key>`，方便 `<video>` / EventSource 使用。
**完整版**（每个接口的请求体、响应示例、字段表、错误码、提交示范）见应用内「插件 → 插件开发文档 → REST API」，
插件专项接口另见 [docs/PLUGINS.md](docs/PLUGINS.md)。

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
| GET | `/api/parsers` | 列出已注册的解析插件 |
| POST | `/api/parse` | `{"url":"…"}` → `matched:false` 或 `result`（统一解析结果） |
| GET | `/api/system/info` | 版本、架构、运行环境 |
| GET | `/api/system/update` | 检查更新（`?force=1` 跳过缓存） |
| POST | `/api/system/update` | 下载并应用更新，随后重启 |
| POST | `/api/system/password` | `{"old_password":"…","new_password":"…"}` |
| POST | `/api/system/ffmpeg` | 安装 / 补齐 ffmpeg 组件 |
| GET | `/api/plugins` | 插件列表（含配置 schema、状态、文件清单） |
| POST | `/api/plugins` | 新建脚手架 / 内联导入 |
| POST | `/api/plugins/upload` · `/validate` · `/reload` | 上传 zip / 校验清单 / 重载 |
| GET/PUT/DELETE | `/api/plugins/{id}` | 详情 / 覆盖 plugin.json / 删除 |
| POST | `/api/plugins/{id}/config` · `/toggle` · `/copy` | 配置 / 启停 / 另存 |
| GET/PUT/DELETE | `/api/plugins/{id}/file` · `GET /files` | 在线编辑源码 |
| POST | `/api/plugins/{id}/patch` · `/build` · `/test` · `/debug` · `/reload` | 差量修改 / 编译 / 测试 / 调试 / 重载 |
| GET | `/api/plugins/{id}/export` | 导出 zip（不含编译产物） |
| GET | `/api/marketplace` | 在线插件市场（代理优先 / 直连兜底） |
| POST | `/api/marketplace/refresh` · `/install` | 刷新 / 安装市场插件 |
| GET/PUT | `/api/marketplace/config` | 市场源地址 / 代理 / 缓存 |
| POST | `/mcp` | MCP JSON-RPC 2.0 端点（36 个工具，含 25 个插件开发工具） |

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

工具（36 个）：基础 11 个 —— `submit_download`、`list_tasks`、`get_task`、`cancel_task`、`retry_task`、
`delete_task`、`rename_task`、`get_download_link`、`get_settings`、`update_settings`、`get_stats`；
插件开发 25 个 —— `plugin_create`、`plugin_update_meta`、`plugin_write_file`、`plugin_read_file`、
`plugin_delete_file`、`plugin_patch`、`plugin_save`、`plugin_validate`、`plugin_build`、`plugin_test`、
`plugin_debug`、`plugin_list`、`plugin_get`、`plugin_files`、`plugin_config`、`plugin_set_config`、
`plugin_toggle`、`plugin_copy`、`plugin_delete`、`plugin_upload`、`plugin_export`、`parse_url`、
`marketplace_list`、`marketplace_install`、`plugin_market_config`。
典型流程：创建 → 写代码 → 差量修改 → 保存 → 校验 → 编译 → 调试 → 测试 → 导出上架。

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

进入首页检测到新版本会自动弹窗，展示新版本号与**更新内容**（按新增 / 修复 / 优化分类、重点高亮）。
弹窗右侧是「取消更新 / 确认更新」，左侧「不再更新此版本」只对当前版本生效，出现更新的版本仍会提醒。

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
internal/plugin/        插件系统：plugin.json 规范、目录与状态、配置、匹配、三种执行引擎、市场
internal/parser/        统一 Result 结构 / 内置公众号解析实现（供 builtin 引擎调用）
internal/server/        HTTP：REST、鉴权、MCP（36 工具）、插件 API、静态资源、系统接口
internal/updater/       更新检测 / 下载校验 / 热切换
internal/version/       版本号（构建时 -ldflags 注入）
web/                    前端（原生 JS，embed 进二进制）
fnpack.json             FnDepot 外部源 V2 索引（商店上架）
marketplace.json        在线插件市场索引（对接 Git，git push 即上架）
marketplace/<id>/       市场里的示例插件（规则版公众号 / 通用 JSON 接口 / Go 多文件示例）
assets/mediasuper/      商店图标、预览图、README、更新内容
deploy/docker/          Dockerfile、构建用 compose
deploy/fnos/            fnOS fpk 模板（manifest / cmd / config / ui）
scripts/                build-release.sh、build-fpk.sh、git-push.sh
docs/                   插件规范、部署、更新、fnOS、FnDepot 上架、架构、版本列表
.release-notes          本次「更新内容」（发布脚本读取，支持多行与高亮标记）
.github/workflows/      Release 与多架构镜像流水线
```

详细设计见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)。

## 文档

| 文档 | 内容 |
|---|---|
| [docs/PLUGINS.md](docs/PLUGINS.md) | **插件开发规范**：目录结构、plugin.json 全字段、配置 schema、三种引擎、Go SDK 协议、市场上架、API / MCP 速查 |
| [docs/DEPLOY.md](docs/DEPLOY.md) | Docker 单容器 / Compose / 镜像源 / 目录权限 |
| [docs/UPDATE.md](docs/UPDATE.md) | 更新检测、一键更新、代理策略、回滚 |
| [docs/FNOS.md](docs/FNOS.md) | fpk 打包流程、安装、日志排查 |
| [docs/FNDEPOT.md](docs/FNDEPOT.md) | FnDepot 第三方商店上架、源字段、校验与发版规则 |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 代码结构与关键流程 |
| [docs/RELEASES.md](docs/RELEASES.md) | 历史构建列表 |

## 常见问题

**怎么给一个新网站写解析插件？**
最快的做法：插件页 →「新建插件」选 `declarative`，填匹配域名；在「文件」里编辑 `plugin.json` 的
`declarative.request / response`，用「配置」填接口地址，用「测试 / 调试」验证；不需要写 Go、不需要编译、不需要重启。
需要复杂逻辑时选 `go` 引擎（可含多个 `.go` 文件），或用 MCP 的插件开发工具让 Agent 帮你写、改、测。
完整规范见 [docs/PLUGINS.md](docs/PLUGINS.md) 与应用内「插件开发文档」。
插件市场直接对接本仓库的 `marketplace.json`，把文件夹放进 `marketplace/` 并登记索引，push 即上架。

**Go 插件在 NAS 上提示「没有 Go 工具链」？**
正常。两条路：① 改用 `declarative` 引擎，零编译；② 在开发机 `go build -o bin/<id> .` 后只上传编译产物。
主程序用独立子进程 + stdin/stdout JSON 行协议调用插件，不依赖主程序的 Go 版本。

**同一个链接提交两次会怎样？**
会**直接拦截**，不创建第二条任务，接口返回 `409` 并带 `{"duplicate":true,"reason":"url"}`。
链接比较前会归一化（忽略 `#` 片段、统一协议与主机名大小写、去掉默认端口和路径末尾斜杠），`query` 里的签名参数保持原样所以不会被误判。
失败或已取消的任务不参与判断，可以直接重新提交或用「重试」；想重新下载已完成的同一链接，先到任务页删除那条任务记录。
想让**同名标题**也不重复下载，到「设置 → 下载」打开标题防重（默认关闭）；批量下载时标题相同的多个视频只会保留第一个。

**提示「未找到 ffmpeg」，m3u8 无法封装？**
v2.5.5 起 fpk 不再内置 ffmpeg，首次启动会在**后台自动补齐**组件（从 Release 下载对应架构的压缩包，
校验 SHA256 后解压到数据目录），到「设置 → 下载」能看到下载进度；失败或想重装可点「立即安装 / 重新安装」，
也可以把系统里 ffmpeg 的绝对路径填进「ffmpeg 路径」。补齐前提交的 m3u8 任务会**直接失败、不再自动重下整部片子**。

**m3u8 下完了但 mp4 打不开？**
`-c copy` 只换容器不解码。若源编码 mp4 不支持（例如某些 H.265/AC3 组合），开启「允许转码」后会自动降级转码（J1800 上很慢，慎用）。

**合成 mp4 时报 `Permission denied`？**
保存目录对应用进程没有写入权限（常见于手动填写的共享目录），或同名文件是只读 / 属于其它用户。
v2.5.4 起会在下载前**真实探测目录可写性**并给出明确提示，成品改为「同目录临时文件 + 原子替换」写入，
同名只读文件不再导致失败；这类错误判定为不可重试，不会反复重下整部片子。
解决：在 NAS / fnOS 里把该目录的写入权限授权给应用，或到「设置 → 存储」换一个可写目录。

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

运行时自动补齐的 ffmpeg 取自 [BtbN/FFmpeg-Builds](https://github.com/BtbN/FFmpeg-Builds) 的 linux-gpl 构建（GPLv3，含 libx264/libx265），
作为独立可执行文件在首次启动时下载到数据目录，**不随 fpk 分发**；对应源码与构建脚本见该仓库。
