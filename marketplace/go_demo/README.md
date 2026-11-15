# Go 插件开发示例

演示「一个插件 = 一个文件夹 + plugin.json + 多个 Go 源码文件」的完整形态：

```
go_demo/
  plugin.json   元数据 / 匹配域名 / 配置项 / entry.sources
  main.go       SDK 协议：stdin 读请求、stdout 写响应
  parse.go      业务逻辑：读配置、调接口、产出 Result
  go.mod        标准库即可，无第三方依赖
  README.md
  bin/go_demo   编译产物（不入库，安装后生成）
```

## 本地跑一遍

```bash
cd go_demo
go build -o bin/go_demo .
echo '{"url":"https://example.com/a.mp4","config":{"prefix":"[测试]","verbose":true},"mode":"debug"}' \
  | ./bin/go_demo
```

stdout 最后一行应当是：

```json
{"ok":true,"result":{"plugin":"go_demo","items":[...]},"logs":[]}
```

## 在 Media Super 里用

1. 插件页 → 上传插件（zip）或在线市场安装本示例。
2. 若设备没有 Go 工具链，点「编译」会给出提示：可在开发机编译后只上传 `bin/`，
   或者改用 `declarative` 引擎（零编译、上传即用）。
3. 点「配置」填标题前缀/最多条数，点「测试」输入任意链接看结果，
   点「调试」还能看到 stderr 日志与耗时。

## 协议要点

- stdin：**一行** JSON `{ "url": "...", "config": {...}, "mode": "parse|debug" }`
- stdout：**最后一行**必须是 JSON `{ "ok": true, "result": {...} }` 或 `{ "ok": false, "error": "..." }`
- stderr：任意日志，`debug` 模式会回传到界面
- 退出码非 0 或超时（30 秒）会被判为失败

`Result` 字段名与 `plugin.json` 的字段说明见应用内「插件 → 开发文档」。
