# 通用 JSON 解析接口（模板）

一个「装上去再改配置」就能对接自建服务的 declarative 模板。

## 期望的接口返回

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "title": "视频标题",
    "cover": "https://.../cover.jpg",
    "author": "作者",
    "description": "简介",
    "publish_time": "2026-11-15 12:00:00",
    "content": "<p>正文 HTML</p>",
    "images": ["https://.../1.jpg"],
    "list": [
      { "id": "v1", "url": "https://.../a.mp4", "title": "第 1 集", "cover": "https://.../a.jpg" },
      { "id": "v2", "url": "https://.../b.mp4", "title": "第 2 集" }
    ]
  }
}
```

- `success_when: "code == 0"` 校验业务成功码，失败时取 `message` 提示用户。
- `fields` 把点路径映射到统一结果字段。
- `items.from: "data.list"` 展开数组；`{{item.url}}` 取元素对象字段。

## 改成自己的站点

1. 复制文件夹，改 `id` 与 `match.domains`（例如 `["bilibili.com", "*.bilibili.com"]`）。
2. 按需增删 `config_schema`，控件会自动出现在配置弹窗。
3. 调整 `fields` / `items` 的点路径与模板。
4. 在插件页「测试」里输入样例链接验证，然后提交 Git + 登记 `marketplace.json`。
