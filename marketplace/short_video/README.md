# 短视频解析（多平台）

对接 [LOSFER AI · 万能平台媒体解析](https://t.losfer.cn/)（VIP，ID 15，`GET /apis/parse/qsy`），把短视频分享链接解析成**标题 / 封面 / 作者 / 播放直链**，在首页提交后即可勾选下载。

**零编译**：`engine = declarative`，全部逻辑写在 `plugin.json` 的 HTTP 请求 + 字段映射规则里，装完即用，不需要 Go 工具链，x86 / arm 设备都能跑。

## 支持平台与匹配域名

| 平台 | 示例链接 | 匹配域名 |
| --- | --- | --- |
| 抖音 | `https://v.douyin.com/aLBdA0PJri0/` | `*.douyin.com`、`*.iesdouyin.com` |
| 快手 | `https://v.kuaishou.com/xxxx` | `*.kuaishou.com`、`*.chenzhongtech.com` |
| 小红书 | `https://xhslink.com/a/xxxx` | `*.xiaohongshu.com`、`*.xhslink.com` |
| bilibili | `https://b23.tv/xxxx` | `*.bilibili.com`、`b23.tv` |
| **微信视频号** | `https://weixin.qq.com/sph/A9sf1QxerX` | `weixin.qq.com` |
| 皮皮虾 / 皮皮搞笑 | — | `*.pipix.com`、`*.pipigx.com` |
| 豆包 / 即梦 AI | — | `*.doubao.com`、`*.jianying.com` |

> 视频号链接是 `weixin.qq.com/sph/…`，与公众号文章 `mp.weixin.qq.com/s/…` 属于**不同域名**，本插件单独加入了 `weixin.qq.com`；内置公众号插件只匹配 `mp.weixin.qq.com`，两者互不干扰。

## 安装与配置

1. 打开「插件」页 → 「插件市场」→ 找到 **短视频解析（多平台）** → 点「安装」。
2. 点插件卡片上的「**配置**」，填入 **API Key**（必填，到 LOSFER AI 控制台 → API 列表 → 万能平台媒体解析 获取）。
   - 不填时接口返回 `code=0 / 请传入APIKEY 支持header/get/post方式传入`，解析卡片会原样显示这条中文提示。
   - 接口按次计费（约 ¥0.002/次），请使用自己的 Key，不要共用。
3. 可选：开启「**解析后自动下载**」，解析完自动入队；默认关闭，先看结果再点「下载」。
4. 可选：把「**解析接口地址**」改成自建服务或反向代理（需返回相同 JSON 结构）。

## 使用方式

只把**链接部分**粘贴到首页输入框，例如：

```
https://v.douyin.com/aLBdA0PJri0/
https://weixin.qq.com/sph/A9sf1QxerX
```

> ⚠️ 请去掉「8.88 复制打开抖音，看看…」这类分享文案。当前版本按整条输入做域名匹配，文案混在里面会匹配不到插件（会被当成普通直链直接下载）。

解析成功后，卡片显示标题、封面、作者、画质 / 发布时间；视频列表里是**一条播放直链**（一般是 mp4，无需 ffmpeg 合成），点「下载」即加入队列。

## 接口与字段映射

请求：`GET {{config.api_base}}?url=<urlencoded>&apikey=<key>`，超时 30 秒。

成功响应（`code` 实测为 `1`，接口文档写「200 表示成功」，本插件不依赖 `code` 判断）：

```json
{
  "code": 1, "msg": "解析成功啦", "time": "1783516736",
  "data": {
    "title": "视频标题",
    "cover": "https://.../cover.jpeg",
    "url": "https://.../video.mp4",
    "quality": "720p original 1280x720",
    "video_backup": [ { "quality": "1080p", "url": "https://..." } ],
    "music": { "name": "M3", "author": "Les Apollo" },
    "author": { "name": "作者", "avatar": "https://..." },
    "statistics": { "digg_count": 53, "comment_count": 9 }
  }
}
```

视频号返回结构略有不同：带 `platform=wxchannels`、`url_hd`（高清）、`backup`、`publish_time`、`expire_time`，**没有** `quality` / `video_backup`。

| 结果字段 | 取值路径 | 说明 |
| --- | --- | --- |
| 标题 | `data.title` | |
| 封面 | `data.cover` | |
| 作者 | `data.author.name` | 从 author 对象里取 name，不显示整段 JSON |
| 简介 | `data.quality` | 抖音显示画质；视频号无此字段，留空 |
| 发布时间 | `data.publish_time` | 视频号有 |
| 下载对象 | `data.url` | 单个字符串 → 展开为 1 条 Item，`Referer` 用原始分享链接 |
| 成功判定 | `data.url != ''` | 有播放链接即成功，兼容 `code=1` 与 `code=200` |
| 错误提示 | `msg` | 失败时原样展示（如「请传入APIKEY」） |

## 想用高清 / 多画质

接口在 `data.url` 给的是**默认播放链接**（文档说明：抖音原始画质、视频号默认链接），另外还提供：

- 视频号 `data.url_hd` / `data.backup`：高清链接
- 抖音 `data.video_backup[]`：其他画质数组，元素含 `url` / `quality`

declarative 一次只能展开一个字段，所以本插件只取 `data.url`。想要高清：点插件卡片「**另存**」复制一份，把 `declarative.response.items.from` 改成 `data.url_hd`；想列出全部画质，改成 `data.video_backup`，并把 `items.url` 写成 `{{item.url}}`、`items.title` 写成 `{{item.quality}}`。

## 常见问题

- **提示「请传入APIKEY」**：配置弹窗没填 API Key，或 Key 已失效。
- **提示「解析服务响应超时」**：接口平均约 331ms，个别请求可达 5s 以上；本插件超时设为 30s，仍失败可稍后重试（接口成功率与链接有效性、平台风控有关）。
- **解析成功但列表为空**：接口没返回 `data.url`（链接失效、内容需登录、平台暂不支持）。
- **下载 403 / 防盗链**：Item 已带 `Referer`（原始分享链接）；视频号链接有 `expire_time`，过期后需重新解析一次。
- **费用**：接口按次计费，请留意控制台余额；改 `api_base` 可切到自建服务。

## 开发者说明

- `min_app_version: 2.5.9`（插件系统随 2.5.9 引入）。
- 匹配规则：`match.domains` 内部是「或」，与 `schemes` 之间为「与」；`priority: 5`。
- 规则字段详见应用内「插件开发文档」tab 与仓库 [`docs/PLUGINS.md`](https://github.com/Mr-losfer/media_downloads/blob/main/docs/PLUGINS.md)。
