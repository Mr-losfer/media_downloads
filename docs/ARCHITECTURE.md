# 代码结构

```
cmd/mediasuper/         入口：flag 解析、-version、-stdio
internal/app/           装配层：开库、初始化设置/密码、启动 HTTP、优雅退出、banner
internal/engine/        下载引擎 + 持久化 + 事件广播
internal/parser/        插件化链接解析（Parser 接口 / 注册表 / 公众号插件）
internal/server/        HTTP 层：REST、鉴权、MCP、静态资源、系统接口
internal/updater/       更新检测、下载校验、热切换
internal/version/       版本号常量（构建时注入）
web/                    前端资源 + go:embed
deploy/docker/          镜像与容器编排
deploy/fnos/            fnOS fpk 模板
scripts/                发布脚本
```

依赖方向是单向的：

```
cmd → app → server ─┬→ engine → （store / model 同包）
                    ├→ parser  （独立包，不依赖 engine / server）
                    └→ updater → version
app → web
```

`engine` 不 import `server`，`server` 不碰 bbolt；`parser` 也不依赖 `engine`，
只输出统一的 `Result`（展示信息 + 可下载对象），由 `server` 交给前端。
这样加 CLI、加 MQTT、换 HTTP 框架、加解析站点都不用动下载逻辑。

---

## 一、各包职责

### `internal/engine`

| 文件 | 内容 |
|---|---|
| `model.go` | `Task`、`Settings`、`SaveDir`、状态机常量、默认值、`Sanitize()` |
| `store.go` | bbolt 封装（`tasks` / `meta` 两个 bucket） |
| `manager.go` | 调度循环、并发控制、进度聚合、删除/改名/重试、统计 |
| `dedupe.go` | 防重复下载：链接/标题归一化、`DuplicateError`、重复任务检索 |
| `download.go` | 直链探测、Range 多段并行、单流兜底 |
| `hls.go` | m3u8 解析、主流/子流选择、AES-128、BYTERANGE、分片并发与拼接 |
| `ffmpeg.go` | `-c copy` 重封装、转码兜底、缩略图、时长 |
| `sse.go` | `Hub`：一条事件序列化一次，扇出给所有 SSE 客户端 |
| `webhook.go` | 事件回调（HMAC 签名、异步重试） |
| `util.go` | 文件名清洗、HTTP client 构造、令牌桶限速、原子写文件 |

任务状态机：

```
queued ──► running ──► merging ──► completed
   │           │
   ├──────────►│ canceled
   └──────────►│ failed（可自动重排回 queued）
```

`Manager` 用一个互斥锁保护 `tasks`、`dirty`、`jobs` 三个 map；
下载协程只写自己的 `job`（原子计数器），由 1 秒一次的 `tick()` 汇总成任务进度并广播，
避免高频加锁。

### `internal/server`

| 文件 | 内容 |
|---|---|
| `api.go` | 路由注册、任务增删改查、文件流（Range）、SSE、静态资源 |
| `auth.go` | PBKDF2 密码、HMAC 签名会话 Cookie、API Key 校验 |
| `mcp.go` | JSON-RPC 2.0 / MCP 工具实现（基础 11 个） |
| `mcp_plugins.go` | 插件开发类 MCP 工具（25 个） |
| `system.go` | 版本信息、检查更新、一键更新、改密码、ffmpeg 组件 |
| `parse.go` | `/api/parsers` 插件列表、`/api/parse` 统一解析入口（优先走 `plugin.Manager`） |
| `plugins.go` | 插件管理与市场全套 REST（列表/创建/上传/配置/启停/文件/差量/编译/测试/调试/导出/安装） |
| `static.go` | 把 `web.FS` 读进内存 map（合计 < 300KB） |
| `stdio.go` | 把 stdio 的 JSON-RPC 转发给远端 HTTP（Claude Desktop 兼容） |

鉴权接受三种凭证：会话 Cookie、`Authorization: Bearer <API Key>`、`?token=<API Key>`。
最后一种给 `<video>` 和 `EventSource` 用，它们没法自定义请求头。

### `internal/plugin`

插件系统的核心，**一个插件 = 一个文件夹 + plugin.json**。

| 文件 | 内容 |
|---|---|
| `spec.go` | `Manifest` 全字段、严格校验（未知字段也拒绝）、`ConfigField` / `MatchRule` / `Declarative` 规范、配置校验 |
| `store.go` | 插件目录、运行期状态 `_state.json`、文件读写、路径安全 `safeJoin`、zip 导入导出 |
| `manager.go` | 注册表、扫描重载、域名匹配与优先级、创建/导入/复制/删除/启停/配置、内置插件落盘 |
| `runtime.go` | 三种执行引擎 + `Trace` 调试追踪 + `go build` 编译 |
| `market.go` | 在线市场：读 `marketplace.json`、代理优先下载、缓存、安装 / 更新 |
| `templates.go` | 内置公众号插件清单、脚手架模板、Go SDK 源码模板 |
| `util.go` | 模板渲染、点路径取值、HTML 清洗、差量修改 `ApplyPatches` |
| `plugin_test.go` | 规范校验、匹配、模板、配置、声明式执行、patch、zip、市场索引一致性 |

三种引擎：

```
builtin      → 注册进 builtinFuncs 的进程内函数（公众号解析）
declarative  → 内置 HTTP 规则解释器：请求模板 → JSON 解析 → 字段映射 → 数组展开
go           → exec 独立可执行文件，stdin 一行 JSON / stdout 最后一行 JSON
```

`plugin.Manager` 在 `app.Run` 里创建，注入 `server.Server`；`/api/parse` 按「启用 + match 命中 +
priority 排序」挑第一个插件执行，`Match` 只做本地字符串/正则判断（零网络开销）。
状态与配置值集中在 `_state.json`，不写进插件目录，插件文件夹可原样提交 Git / 导出。
完整规范见 [PLUGINS.md](PLUGINS.md)。

### `internal/parser`

保留统一的 `Result` / `Item` 数据结构与公众号解析的 Go 实现，供 `builtin` 引擎调用：

| 文件 | 内容 |
|---|---|
| `parser.go` | `Parser` 接口、`Result` / `Item` 统一结构、`Registry`、`Default()`（兼容兜底） |
| `wechat_mp.go` | 公众号文章：调解析服务，把 `movie_down_url` 映射成视频 Item；支持配置超时与 Token |
| `parser_test.go` | httptest mock 解析服务，覆盖匹配、字段映射、失败与空数据 |

首页提交流程：

```
POST /api/parse {url}
  ├─ plugin.Manager.Match 无命中 → {matched:false} → 前端照旧 POST /api/tasks（直链下载）
  └─ 命中插件 → Manager.Run（builtin / declarative / go）
                 → {matched:true, plugin, result, trace?} → 前端展示 result
                 → 勾选 items → 逐个 POST /api/tasks（重复项按 409 统计为「跳过」）
```

`Result` 是前端唯一认识的形状，插件特有字段放 `Extra`；`Extra.auto_download` 可让解析卡片自动全量入队。
新增站点支持不需要改主程序：上传一个文件夹，或往 `marketplace/` 加一个插件并登记索引。
解析出的 `Item.URL` 走现有 `downloadDirect`，一般是 mp4 直链，不经过 m3u8 / ffmpeg。

### `internal/updater`

见 [UPDATE.md](UPDATE.md)。核心是「代理优先 + 直连兜底」的取元数据策略，
以及把新二进制落到数据卷、用 `exec` 热切换，从而在 Docker 里也能一键更新。

### `web/`

原生 JS 单页应用，无构建链、无 CDN。`embed.go` 用 `//go:embed` 把
`*.html *.js *.css *.webmanifest *.svg *.png` 打进二进制，启动时由 `server.LoadWebAssets`
一次性读进内存。

路由用 hash（`#/tasks`、`#/plugins`、`#/docs/api|plugins|mcp`、`#/settings`），未知路径回落 `index.html`。
导航四页为「首页 / 任务 / 插件 / 设置」；文档页不占导航项，由插件页顶部的「插件开发文档」入口进入。
插件页自己的逻辑在 `app.js` 的 `setPlug / loadPlugins / renderPlugins / openPluginConfig / loadMarket` 等函数中，
全部走 `data-plug-act` 事件委托，弹窗复用 `openSheet`。

---

## 二、一次下载的完整链路

```
POST /api/tasks
  └─ Manager.Add            校验 URL → 防重复检查 → 目录白名单 → 磁盘剩余空间
       ├─ 命中重复：返回 DuplicateError，HTTP 409 + {duplicate,reason,task_id}，不建任务
       │            （链接防重始终开启；标题防重由 settings.dedupe_title 控制；
       │              只拦 queued/running/merging/completed，failed/canceled 放行）
       └─ 写入 bbolt + SSE 广播 task.created + Webhook

Manager.tick（每秒）
  └─ 取最早排队的任务 → 标记 running → go Manager.run

Manager.run
  └─ dlCtx.run
       ├─ 探测：HEAD/GET 前 512B，判断 HLS 还是普通文件
       ├─ HLS：解析 m3u8 → 选码率 → 分片并发（默认 8）→ AES 解密 → 顺序拼接 .ts
       │        └─ ffmpeg -c copy → mp4（+faststart）
       └─ 文件：HEAD 拿 Content-Length / Accept-Ranges
                ├─ 支持 Range：切 N 段，各自 WriteAt 到同一文件
                └─ 不支持：单流顺序写
  └─ 汇总：completed / failed（按 max_retries 自动重排）/ canceled
       └─ 持久化 + SSE 广播 + Webhook + 异步生成缩略图
```

磁盘约定：

- 临时分片：`<数据目录>/tmp/<taskID>/`，启动时清空（不跨进程续传，避免错位）；
- 成品：直接写到 `save_dir`，同名自动加 `-1`、`-2`；
- 缩略图：`<成品路径>.jpg`。

---

## 三、数据存储

bbolt 单文件 `<数据目录>/mediasuper.db`：

| bucket | key | value |
|---|---|---|
| `tasks` | 任务 ID（16 位 hex） | 任务 JSON |
| `meta` | `settings` / `api_key` / `pass_hash` / `secret` | 字符串 |

选 bbolt 的理由：单文件、无服务进程、B+Tree 写入 O(log n)，
比「JSON 全量重写」省 IO，比 SQLite 少一层依赖，低配 NAS 上更划算。
`NoFreelistSync` 省一次 fsync，`InitialMmapSize=1MB` 控制内存映射初始大小。

旧版本的 `vidfetch.db` 会在启动时自动改名为 `mediasuper.db`。

---

## 四、前端刷新策略

- 首次进入：`GET /api/tasks?limit=200` 全量拉一次；
- 之后只靠 SSE：`task` 事件按 ID 增量 patch DOM，`stats` 每 3 秒推一次；
- 列表滚动/筛选时合并渲染（`scheduleRender`），避免每个事件都重排；
- `prefers-reduced-motion` 与 `hover:none` 都有专门适配（44px 命中区、iOS 16px 输入）。

---

## 五、构建与版本注入

`internal/version` 里三个变量在构建时用 `-ldflags -X` 覆盖：

```bash
go build -ldflags "-X mediasuper/internal/version.Version=2.2.0 \
                   -X mediasuper/internal/version.Commit=abc1234 \
                   -X mediasuper/internal/version.BuildTime=2026-11-08T14:00:00Z"
```

`Makefile`、`scripts/build-release.sh`、`deploy/docker/Dockerfile` 都按这个约定注入，
所以页面上的版本号、`/api/health`、更新检测里的当前版本永远一致。
