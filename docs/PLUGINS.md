# Media Super 插件系统

> 应用内「插件 → 插件开发文档」有面向开发者的完整图文版；本文是仓库侧的规范与维护说明。

## 一、设计目标

- **文件夹即插件**：`<数据目录>/plugins/<id>/` 一个文件夹 + 根目录 `plugin.json`，可带多个 Go 源码文件。
- **零改动的接入**：首页提交链接时按「启用 + 域名/路径/正则命中」挑一个插件，统一产出 `parser.Result`，前端与下载引擎不用改。
- **可视化配置**：插件在 `config_schema` 里声明选项，插件列表自动出现「配置」按钮，弹窗按类型渲染，值以 `{{config.<key>}}` 注入。
- **三种引擎覆盖不同复杂度**，且都能在 NAS 上落地：

| 引擎 | 写代码 | 编译 | 说明 |
| --- | --- | --- | --- |
| `declarative` | 不写，只写规则 | 零编译 | HTTP 请求 + 点路径字段映射 + 数组展开，上传即用，推荐 |
| `go` | Go（可多文件） | 需编译 | 独立可执行文件 + stdin/stdout JSON 行协议，跨版本跨架构 |
| `builtin` | 编译进主程序 | 随主程序 | 官方内置；当前仅 `wechat_mp`（公众号解析） |

> 为什么不用 `-buildmode=plugin` 的 `.so`：Go 插件包要求主程序与插件的 Go 版本、依赖版本完全一致，
> NAS 还分 x86/arm，用户上传源码无法直接加载。子进程协议没有这些限制，崩溃也不影响主服务。

## 二、目录结构

```
<data>/plugins/
  _state.json              运行期状态（启停 / 配置值 / 来源），自动生成，勿提交
  _market.json             市场源配置，自动生成
  my_site/
    plugin.json           必需，唯一权威元数据
    main.go               Go 引擎：SDK 协议入口
    parse.go              Go 引擎：业务逻辑（可拆任意多个 .go 文件）
    go.mod                Go 引擎模块文件（只用标准库即可）
    README.md
    bin/my_site           编译产物（不导出、不入库）
```

内置公众号插件同样有独立文件夹，`builtin: true`，禁止删除但可禁用、可配置、可「另存为新插件」。

## 三、plugin.json 字段

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `id` | 是 | 2–48 位，小写字母开头，只含 `a-z 0-9 _ -`，**必须与目录同名** |
| `name` / `version` | 是 | 显示名；语义化版本 `1.0.0` |
| `engine` | 是 | `declarative` / `go` / `builtin` |
| `match` | 否 | 决定提交哪种链接时运行，见下 |
| `config_schema` | 否 | 可视化配置选项，非空时出现「配置」按钮 |
| `capabilities` | 否 | `{ "parse": true, "configurable": true }` |
| `declarative` | 引擎为 declarative 时必填 | 请求 + 响应规则 |
| `entry` | 否 | Go 引擎的 `sources` / `binary` |
| `description` / `author` / `homepage` / `icon` / `tags` / `changelog` / `min_app_version` | 否 | 展示元信息 |
| `builtin` | 否 | 内置标记，第三方插件不要写 |

**match 规则**（字段间是「与」，domains 内是「或」）：

- `domains`：精确域名或 `*.example.com` 通配（同时匹配裸域）
- `url_patterns`：Go 正则数组，写了就必须至少命中一条
- `path_prefixes`：路径前缀，如 `["/s", "/video"]`
- `schemes`：允许协议，留空 = http/https
- `extensions`：扩展名（带点、小写）
- `priority`：数值越大越先匹配，默认 0；多个命中时只运行第一个

整条 `match` 为空的插件不会自动接管任何链接，但仍可被 `plugin_test` / `parse_url` 显式调用。

**config_schema 类型**：`text` / `password` / `number` / `switch` / `select` / `textarea` / `url`。
通用属性：`key`（必填唯一）、`label`、`desc`、`default`、`required`、`placeholder`、`group`、`min` / `max`（数字）、`options`（select）。
约定键 `auto_download: true` 会让前端解析卡片自动把全部视频加入下载队列。

## 四、declarative 规则

```json
{
  "request": {
    "method": "GET",
    "url": "{{config.api_base}}",
    "query": { "url": "{{source_url}}" },
    "headers": { "Authorization": "{{config.api_token}}" },
    "timeout_sec": 15
  },
  "response": {
    "format": "json",
    "success_when": "code == 1",
    "error_field": "msg",
    "fields": { "title": "data.title", "cover": "data.cover", "images": "data.images" },
    "items": {
      "from": "data.movie_down_url",
      "url": "{{value}}",
      "title": "{{title}} · {{index1}}",
      "kind": "video", "format": "mp4",
      "headers": { "Referer": "{{source_url}}" },
      "max": 100
    }
  }
}
```

- 模板变量：`{{config.<key>}}`、`{{source_url}}`（或 `{{url}}`）、`{{title}}`、`{{cover}}`、
  `{{value}}`（数组元素是字符串）、`{{item.<path>}}`（元素是对象）、`{{index}}`、`{{index1}}`。
- 点路径支持 `data.list[0].url`。
- `success_when` 支持 `code == 1` / `data.ok == true` / `errcode != 0` 这类简单断言。
- 响应体上限 8 MB；`content` 字段会剥掉 HTML 标签压成 600 字摘要。

完整示例见 [`marketplace/wechat_mp_lite/plugin.json`](../marketplace/wechat_mp_lite/plugin.json)
与 [`marketplace/json_api_parser/plugin.json`](../marketplace/json_api_parser/plugin.json)。

## 五、Go 插件 SDK 协议

```
stdin  → {"url":"...","config":{...},"mode":"parse|debug"}
stdout → {"ok":true,"result":{ ...parser.Result... },"logs":[]}
       → {"ok":false,"error":"中文错误","logs":[]}
stderr → 任意日志（debug 模式回传界面）
```

- stdout **最后一行**必须是 JSON；前面允许打日志。
- 30 秒超时 / 退出码非 0 判定失败。
- 所有 `.go` 文件放 `package main`，`entry.sources` 列出或留空自动扫描。
- 完整可编译示例见 [`marketplace/go_demo/`](../marketplace/go_demo/)（`main.go` 管协议、`parse.go` 管业务）。

NAS 上没有 Go 工具链时，点「编译」会返回降级建议：改用 declarative，或在开发机
`go build -o bin/<id> .` 后只上传 `bin/` 产物。

## 六、在线插件市场（对接 Git）

仓库根目录 [`marketplace.json`](../marketplace.json) 是索引，`marketplace/<id>/` 是每个插件的文件夹。
**git push 即上架**，不需要额外服务器。

```json
{
  "schema_version": "1",
  "name": "Media Super 官方插件市场",
  "owner": "Mr-losfer",
  "repo": "Mr-losfer/media_downloads",
  "updated_at": "2026-11-15",
  "base_url": "marketplace",
  "plugins": [
    {
      "id": "json_api_parser", "name": "通用 JSON 解析接口",
      "version": "1.0.2", "description": "...", "author": "...",
      "engine": "declarative", "tags": ["通用"],
      "path": "json_api_parser",
      "files": ["plugin.json", "README.md"]
    }
  ]
}
```

- `base_url` 可以是绝对 URL，也可以相对 `marketplace.json` 所在目录。
- `files` 必须列全要下载的文件，其中必须有 `plugin.json`。
- 客户端按 `proxy_prefix`（默认 `https://gh-proxy.com/`）优先 → 直连回退下载；
  市场地址 / 代理 / 缓存分钟数可在插件页「市场源设置」或 `PUT /api/marketplace/config` 修改。
- 客户端按 `version` 判断「可更新」，安装时保留用户已有配置值。

上架步骤：① 把插件文件夹放进 `marketplace/<id>/`；② 在 `marketplace.json` 加一条并列全 `files`；
③ commit + push；④ 客户端「刷新市场」即可看到。Go 插件的 `bin/` 不要提交。

## 七、API 与 MCP

REST 全量接口见应用内「文档 → REST API」或 [`ARCHITECTURE.md`](ARCHITECTURE.md)。核心：

| 目的 | 接口 |
| --- | --- |
| 列表 / 新建 / 上传 | `GET /api/plugins` · `POST /api/plugins` · `POST /api/plugins/upload` |
| 详情 / 清单 / 删除 | `GET|PUT|DELETE /api/plugins/{id}` |
| 配置 / 启停 / 复制 | `POST /api/plugins/{id}/config` · `/toggle` · `/copy` |
| 文件 / 差量修改 | `GET|PUT|DELETE /api/plugins/{id}/file` · `POST /api/plugins/{id}/patch` |
| 校验 / 编译 / 重载 | `POST /api/plugins/validate` · `POST /api/plugins/{id}/build` · `POST /api/plugins/reload` |
| 测试 / 调试 / 导出 | `POST /api/plugins/{id}/test` · `/debug` · `GET /api/plugins/{id}/export` |
| 链接解析 | `GET /api/parsers` · `POST /api/parse` |
| 市场 | `GET /api/marketplace` · `POST /api/marketplace/refresh` · `/install` · `GET|PUT /api/marketplace/config` |

MCP 提供 25 个插件开发工具（共 36 个），覆盖：
创建、代码修改、编辑、上传、差量修改、保存、测试、调试、编译、配置、启停、复制、删除、导出、市场列表/安装/配置。
典型流程：`plugin_create` → `plugin_write_file` → `plugin_patch` → `plugin_save` → `plugin_validate`
→ `plugin_build` → `plugin_debug` → `plugin_test` → `plugin_export`。

## 八、安全与边界

- 文件路径经 `safeJoin` 限制在插件目录内，阻断 `../`、绝对路径与软链接逃逸。
- 单个文件 2 MB、插件包 16 MB、HTTP 响应 8 MB 上限；正则在保存时预编译校验。
- 插件配置里的 `password` 类型只影响界面回显；服务端仍会正常保存，MCP `plugin_get` 会返回当前值，请勿在高权限共享环境滥用。
- Go 插件以独立进程运行，超时 30 秒；它继承主进程的运行环境，请只安装信任来源的插件。
- 内置插件的 `id` / `engine` 不允许修改，禁止删除。

## 九、常见问题

| 现象 | 处理 |
| --- | --- |
| 提交链接没走插件 | 检查 `match.domains`、是否启用、是否被更高 `priority` 抢先命中 |
| 「解析地址不合法」 | `{{config.api_base}}` 未填或非 http(s) |
| 解析成功但 0 个视频 | `items.from` 点路径不对，用「调试」看响应体 |
| 「插件尚未编译」 | engine=go 缺 `bin/` 产物，点编译或改 declarative |
| 「没有 Go 工具链」 | 开发机编译后上传产物，或改用 declarative |
| zip 报缺少 plugin.json | 允许顶层多一层目录，但清单必须在插件文件夹根部 |
| 改了清单不生效 | 点「重载」；`id` 不能改，要换 id 得新建 |
