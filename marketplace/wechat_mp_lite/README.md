# 公众号解析（规则版）

内置 `wechat_mp` 插件的 **declarative** 版本。同样的功能，不用写 Go 代码：

- `declarative.request` 描述「怎么请求解析服务」；
- `declarative.response.fields` 描述「响应里的哪个字段映射到标题/封面/作者」；
- `declarative.response.items` 描述「哪个数组展开成可下载视频列表」。

## 怎么改造成自己的插件

1. 复制整个文件夹，改 `id`、`name`、`match.domains`（决定提交哪些链接时运行）。
2. 在 `config_schema` 里加配置项，插件列表会自动出现「配置」按钮；值用 `{{config.<key>}}` 注入。
3. 按你的接口返回结构调整 `fields` 的点路径，如 `data.result.title`。
4. 用插件页「测试」输入一条真实链接，看结果与调试日志。
5. 提交到 Git，并在仓库根 `marketplace.json` 里登记即可上架市场。

> 点路径支持 `data.list[0].url` 这样的数组下标；items 模板可用
> `{{value}}`（元素是字符串）、`{{item.url}}`（元素是对象）、`{{index}}`、
> `{{index1}}`、`{{title}}`、`{{source_url}}`、`{{config.xxx}}`。
