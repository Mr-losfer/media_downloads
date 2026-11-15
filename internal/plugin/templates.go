package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"mediasuper/internal/parser"
)

// ---------- 内置插件：公众号解析 ----------
//
// 它现在和第三方插件走完全相同的生命周期：出现在插件列表、可启用/禁用、
// 有配置弹窗、有匹配域名、可被 MCP 测试调试。唯一区别是 builtin=true，
// 禁止删除，逻辑编译在主程序里（parser.WechatMP）。

const wechatManifestJSON = `{
  "id": "wechat_mp",
  "name": "微信公众号解析",
  "version": "1.0.0",
  "author": "Media Super",
  "description": "解析 mp.weixin.qq.com/s/ 文章，提取标题、封面、作者、正文摘要与视频下载直链（一般 mp4，无需合成）",
  "homepage": "https://github.com/Mr-losfer/media_downloads",
  "engine": "builtin",
  "builtin": true,
  "tags": ["公众号", "内置", "视频"],
  "min_app_version": "2.5.9",
  "match": {
    "domains": ["mp.weixin.qq.com"],
    "path_prefixes": ["/s", "/s/"],
    "schemes": ["http", "https"],
    "priority": 10
  },
  "capabilities": { "parse": true, "configurable": true },
  "config_schema": [
    {
      "key": "api_base", "label": "解析接口地址", "type": "url", "required": true,
      "default": "https://t.losfer.cn/apis/parse/mp/parseArticle",
      "placeholder": "https://your-domain/apis/parse/mp/parseArticle",
      "desc": "GET 接口，接受 url 参数，返回 code/data 结构。也可用环境变量 MS_MP_PARSE_API 覆盖。"
    },
    {
      "key": "timeout_sec", "label": "请求超时（秒）", "type": "number",
      "default": 15, "min": 3, "max": 60, "desc": "解析服务偶发超时时可调大"
    },
    {
      "key": "api_token", "label": "接口 Token", "type": "password", "default": "",
      "desc": "可选。非空时以 Authorization: Bearer 发送；界面上以密码框显示且不回显明文。"
    },
    {
      "key": "auto_download", "label": "解析后自动下载全部视频", "type": "switch",
      "default": false, "desc": "开启后解析结果卡片会自动把全部视频加入下载队列"
    }
  ],
  "changelog": "随 Media Super 分发；可在插件页配置接口地址、超时、Token 与自动下载。"
}
`

// RegisterWechatBuiltin 注册内置实现，必须在 NewManager 之前调用。
func RegisterWechatBuiltin() {
	RegisterBuiltin("wechat_mp", func(ctx context.Context, cfg map[string]any, rawURL string, tr *Trace) (*parser.Result, error) {
		endpoint := strings.TrimSpace(fmt.Sprint(cfg["api_base"]))
		if endpoint == "" {
			endpoint = "https://t.losfer.cn/apis/parse/mp/parseArticle"
		}
		sec := 15
		if v, ok := cfg["timeout_sec"]; ok {
			if n, err := strconv.Atoi(fmt.Sprint(v)); err == nil && n >= 3 && n <= 60 {
				sec = n
			}
		}
		token := strings.TrimSpace(fmt.Sprint(cfg["api_token"]))
		p := parser.NewWechatMPEx(endpoint, token, time.Duration(sec)*time.Second)
		u, _ := url.Parse(endpoint)
		tr.step("调用解析接口 %s（超时 %d 秒%s）", u.Host, sec, map[bool]string{true: "，带 Token", false: ""}[token != ""])
		res, err := p.Parse(ctx, rawURL)
		if err != nil {
			return nil, err
		}
		if res.Extra == nil {
			res.Extra = map[string]any{}
		}
		if b, ok := cfg["auto_download"].(bool); ok && b {
			res.Extra["auto_download"] = true
		}
		tr.step("解析出 %d 个视频直链", len(res.Items))
		return res, nil
	})
}

// ---------- 脚手架模板 ----------

// CreateInput 是 plugin_create / POST /api/plugins 的入参。
type CreateInput struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Version     string        `json:"version,omitempty"`
	Author      string        `json:"author,omitempty"`
	Description string        `json:"description,omitempty"`
	Engine      string        `json:"engine,omitempty"` // declarative（默认）/ go / builtin
	Domains     []string      `json:"domains,omitempty"`
	PathPrefix  string        `json:"path_prefix,omitempty"`
	Config      []ConfigField `json:"config_schema,omitempty"`
}

func scaffoldManifest(in CreateInput) (*Manifest, error) {
	engine := strings.ToLower(strings.TrimSpace(in.Engine))
	if engine == "" {
		engine = EngineDeclarative
	}
	mf := &Manifest{
		ID:           strings.ToLower(strings.TrimSpace(in.ID)),
		Name:         strings.TrimSpace(in.Name),
		Version:      firstNonEmpty(strings.TrimSpace(in.Version), "1.0.0"),
		Author:       firstNonEmpty(strings.TrimSpace(in.Author), "本地开发者"),
		Description:  firstNonEmpty(strings.TrimSpace(in.Description), "自定义解析插件"),
		Engine:       engine,
		Capabilities: Capabilities{Parse: true},
		Tags:         []string{"自定义"},
		Match: MatchRule{
			Domains: in.Domains,
			Schemes: []string{"http", "https"},
		},
	}
	if in.PathPrefix != "" {
		mf.Match.PathPrefixes = []string{in.PathPrefix}
	}
	if len(in.Config) > 0 {
		mf.ConfigSchema = in.Config
	} else {
		mf.ConfigSchema = []ConfigField{
			{Key: "api_base", Label: "解析接口地址", Type: TypeURL, Required: true,
				Default: "https://example.com/api/parse", Desc: "GET 接口，接受 url 参数，返回 JSON"},
			{Key: "api_token", Label: "接口 Token", Type: TypePassword, Desc: "可选，写入 Authorization 头"},
		}
	}
	switch engine {
	case EngineDeclarative:
		mf.Declarative = &Declarative{
			Request: DeclarativeRequest{
				Method:     "GET",
				URL:        "{{config.api_base}}",
				Query:      map[string]string{"url": "{{source_url}}"},
				Headers:    map[string]string{"Accept": "application/json", "Authorization": "{{config.api_token}}"},
				TimeoutSec: 15,
			},
			Response: DeclarativeResponse{
				Format: "json", SuccessWhen: "code == 1", ErrorField: "msg",
				Fields: map[string]string{
					"title": "data.title", "cover": "data.cover", "author": "data.author",
					"description": "data.description", "publish_time": "data.publish_time",
					"content": "data.content", "images": "data.images",
				},
				Items: &DeclarativeItems{
					From: "data.list", URL: "{{item.url}}", Title: "{{title}} · {{index1}}",
					Kind: "video", Format: "mp4", Max: 100,
					Headers: map[string]string{"Referer": "{{source_url}}"},
				},
			},
		}
	case EngineGo:
		mf.Entry = Entry{Binary: "bin/" + mf.ID, Sources: []string{"main.go"}}
	}
	if err := mf.Validate(); err != nil {
		return nil, err
	}
	return mf, nil
}

func scaffoldReadme(mf *Manifest) string {
	return fmt.Sprintf(`# %s

%s

- ID：%s
- 版本：%s
- 引擎：%s
- 匹配域名：%v

## 目录结构

    %s/
      plugin.json     插件清单（元数据 / 匹配规则 / 配置项 / 执行规则）
      main.go         Go 源码（仅 engine=go）
      README.md       本文件

## 本地开发

1. 在「插件」页点「新建插件」或从本仓库 marketplace/ 复制一份模板。
2. 修改 plugin.json 的 match.domains，决定提交哪些链接时运行本插件。
3. config_schema 里每加一项，插件列表就会出现对应配置控件，保存后以 {{config.<key>}} 注入。
4. 用「测试」按钮或 MCP plugin_test 输入样例链接，查看解析结果与调试日志。
5. 点「导出」得到 zip，或直接把整个文件夹提交到 Git 后在 marketplace.json 登记，即可上架市场。

详见应用内「插件 → 开发文档」。
`, mf.Name, mf.Description, mf.ID, mf.Version, mf.Engine, mf.Match.Domains, mf.ID)
}

// goMainTemplate 是一份可直接编译、实现 stdin/stdout JSON 行协议的 SDK 示例。
// 插件作者可以在此基础上拆成多个 .go 文件（都在 package main 下）。
const goMainTemplate = `// %s —— Media Super Go 插件示例（SDK 协议 v1）
//
// 编译：go build -o bin/%s .
// 协议：stdin 读一行 JSON 请求；stdout 最后一行输出一行 JSON 响应；
//       stderr 可以任意打日志（调试模式会回传给界面）。
// 只用标准库，因此可以离线编译，也不需要依赖主程序的版本。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Request struct {
	URL    string         ` + "`json:\"url\"`" + `
	Config map[string]any ` + "`json:\"config\"`" + `
	Mode   string         ` + "`json:\"mode\"`" + `
}

type Item struct {
	ID      string            ` + "`json:\"id\"`" + `
	URL     string            ` + "`json:\"url\"`" + `
	Title   string            ` + "`json:\"title\"`" + `
	Cover   string            ` + "`json:\"cover,omitempty\"`" + `
	Kind    string            ` + "`json:\"kind\"`" + `
	Format  string            ` + "`json:\"format,omitempty\"`" + `
	Page    string            ` + "`json:\"page,omitempty\"`" + `
	Headers map[string]string ` + "`json:\"headers,omitempty\"`" + `
}

type Result struct {
	Plugin      string         ` + "`json:\"plugin\"`" + `
	PluginName  string         ` + "`json:\"plugin_name\"`" + `
	SourceURL   string         ` + "`json:\"source_url\"`" + `
	Title       string         ` + "`json:\"title\"`" + `
	Cover       string         ` + "`json:\"cover\"`" + `
	Author      string         ` + "`json:\"author\"`" + `
	Description string         ` + "`json:\"description\"`" + `
	PublishTime string         ` + "`json:\"publish_time\"`" + `
	ContentText string         ` + "`json:\"content_text\"`" + `
	ContentLen  int            ` + "`json:\"content_len\"`" + `
	Images      []string       ` + "`json:\"images\"`" + `
	Items       []Item         ` + "`json:\"items\"`" + `
	Extra       map[string]any ` + "`json:\"extra,omitempty\"`" + `
}

type Response struct {
	OK     bool     ` + "`json:\"ok\"`" + `
	Result *Result  ` + "`json:\"result,omitempty\"`" + `
	Error  string   ` + "`json:\"error,omitempty\"`" + `
	Logs   []string ` + "`json:\"logs,omitempty\"`" + `
}

func emit(r Response) {
	b, _ := json.Marshal(r)
	fmt.Println(string(b))
}

func logf(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }

func cfgString(c map[string]any, key, def string) string {
	if v, ok := c[key]; ok && v != nil {
		if s := strings.TrimSpace(fmt.Sprint(v)); s != "" {
			return s
		}
	}
	return def
}

// parse 是唯一需要改的地方：把页面/接口解析成 Result。
// 下面是一份「调用外部 JSON 接口」的示例，可替换成任意 Go 逻辑（多个 .go 文件都行）。
func parse(req Request) (*Result, []string, error) {
	logs := []string{}
	api := cfgString(req.Config, "api_base", "https://example.com/api/parse")
	if !strings.HasPrefix(api, "http://") && !strings.HasPrefix(api, "https://") {
		return nil, logs, fmt.Errorf("配置项 api_base 不是合法的 http(s) 地址")
	}
	sep := "?"
	if strings.Contains(api, "?") { sep = "&" }
	full := api + sep + "url=" + urlQueryEscape(req.URL)
	logf("GET %%s", full)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(full)
	if err != nil {
		return nil, logs, fmt.Errorf("请求解析服务失败：%%w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, logs, err
	}
	logf("HTTP %%d, %%d bytes", resp.StatusCode, len(body))

	var data struct {
		Title string ` + "`json:\"title\"`" + `
		List  []struct {
			URL   string ` + "`json:\"url\"`" + `
			Title string ` + "`json:\"title\"`" + `
		} ` + "`json:\"list\"`" + `
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, logs, fmt.Errorf("解析结果不是合法 JSON：%%w", err)
	}

	res := &Result{
		Plugin: "%s", PluginName: "%s", SourceURL: req.URL,
		Title: data.Title, Images: []string{}, Items: []Item{},
		Extra: map[string]any{},
	}
	for i, v := range data.List {
		if !strings.HasPrefix(v.URL, "http") { continue }
		name := v.Title
		if name == "" { name = fmt.Sprintf("%%s · %%02d", data.Title, i+1) }
		res.Items = append(res.Items, Item{
			URL: v.URL, Title: name, Kind: "video", Format: "mp4",
			Headers: map[string]string{"Referer": req.URL},
		})
	}
	return res, logs, nil
}

func urlQueryEscape(s string) string {
	// 用 net/url 更严谨；这里保持单文件模板的零额外 import
	r := strings.NewReplacer("%%", "%%25", "&", "%%26", "?", "%%3F", "#", "%%23", " ", "%%20")
	return r.Replace(s)
}

func main() {
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil { emit(Response{OK: false, Error: err.Error()}); return }
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		emit(Response{OK: false, Error: "请求不是合法 JSON: " + err.Error()}); return
	}
	res, logs, err := parse(req)
	if err != nil { emit(Response{OK: false, Error: err.Error(), Logs: logs}); return }
	emit(Response{OK: true, Result: res, Logs: logs})
}
`

func goMainSource(mf *Manifest) string {
	return fmt.Sprintf(goMainTemplate, mf.Name, mf.ID, mf.ID, mf.Name)
}

func manifestBytes(mf *Manifest) ([]byte, error) {
	b, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
