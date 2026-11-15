package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"mediasuper/internal/parser"
)

// Trace 是 plugin_test / plugin_debug 返回的调试信息，帮助开发者定位问题。
type Trace struct {
	Plugin     string   `json:"plugin"`
	Engine     string   `json:"engine"`
	URL        string   `json:"url"`
	DurationMS int64    `json:"duration_ms"`
	Steps      []string `json:"steps"`
	Logs       []string `json:"logs,omitempty"`
	Request    string   `json:"request,omitempty"`
	Response   string   `json:"response,omitempty"`
}

func (t *Trace) step(format string, a ...any) {
	t.Steps = append(t.Steps, fmt.Sprintf(format, a...))
}

// BuiltinFunc 是编译进主程序的插件实现签名。
type BuiltinFunc func(ctx context.Context, cfg map[string]any, rawURL string, tr *Trace) (*parser.Result, error)

var builtinFuncs = map[string]BuiltinFunc{}

// RegisterBuiltin 供内置实现注册（在 manager 初始化时调用）。
func RegisterBuiltin(id string, fn BuiltinFunc) { builtinFuncs[id] = fn }

// Run 执行一个插件，统一产出 parser.Result。
func (m *Manager) Run(ctx context.Context, id, rawURL string, debug bool) (*parser.Result, *Trace, error) {
	i, ok := m.Get(id)
	if !ok {
		return nil, nil, fmt.Errorf("插件 %s 不存在", id)
	}
	if !i.State.Enabled {
		return nil, nil, fmt.Errorf("插件「%s」已禁用，请先启用", i.Manifest.Name)
	}
	start := time.Now()
	tr := &Trace{Plugin: id, Engine: i.Manifest.Engine, URL: rawURL, Steps: []string{}, Logs: []string{}}
	tr.step("命中插件 %s（%s v%s，引擎 %s）", i.Manifest.Name, id, i.Manifest.Version, i.Manifest.Engine)

	var (
		res *parser.Result
		err error
	)
	switch i.Manifest.Engine {
	case EngineBuiltin:
		res, err = m.runBuiltin(ctx, i, rawURL, tr)
	case EngineDeclarative:
		res, err = m.runDeclarative(ctx, i, rawURL, tr, debug)
	case EngineGo:
		res, err = m.runGo(ctx, i, rawURL, tr, debug)
	default:
		err = fmt.Errorf("未知引擎 %s", i.Manifest.Engine)
	}
	tr.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		tr.step("失败：%v", err)
		return nil, tr, err
	}
	if res == nil {
		return nil, tr, fmt.Errorf("插件没有返回结果")
	}
	if res.Plugin == "" {
		res.Plugin = id
	}
	if res.PluginName == "" {
		res.PluginName = i.Manifest.Name
	}
	// 内置引擎没有独立的 HTTP 调用记录，调试模式下补一份可读摘要，
	// 保证三种引擎在调试弹窗里都能看到「请求 / 响应」两个区块。
	if debug && tr.Request == "" {
		tr.Request = fmt.Sprintf("内置引擎 %s：逻辑编译在主程序内，无独立 HTTP 请求记录。\n可在插件配置弹窗里调整接口地址、超时、Token 等参数。", id)
	}
	if debug && tr.Response == "" {
		tr.Response = fmt.Sprintf("标题: %s\n可下载对象: %d\n来源: %s", res.Title, len(res.Items), res.SourceURL)
	}
	tr.step("完成：标题 %q，可下载对象 %d 个，耗时 %d ms", res.Title, len(res.Items), tr.DurationMS)
	return res, tr, nil
}

// ---------- 内置引擎 ----------

func (m *Manager) runBuiltin(ctx context.Context, i *Installed, rawURL string, tr *Trace) (*parser.Result, error) {
	fn := builtinFuncs[i.Manifest.ID]
	if fn == nil {
		return nil, fmt.Errorf("内置实现 %s 未注册（该插件可能被手工改成了 builtin 引擎）", i.Manifest.ID)
	}
	return fn(ctx, i.State.Config, rawURL, tr)
}

// ---------- 声明式引擎 ----------

func (m *Manager) runDeclarative(ctx context.Context, i *Installed, rawURL string, tr *Trace, debug bool) (*parser.Result, error) {
	d := i.Manifest.Declarative
	if d == nil {
		return nil, fmt.Errorf("缺少 declarative 规则")
	}
	c := templateContext{Config: i.State.Config, SourceURL: rawURL}
	url := renderTpl(d.Request.URL, c)
	if !isHTTP(url) {
		return nil, fmt.Errorf("解析地址不合法：请检查 declarative.request.url 或配置项（当前渲染结果为空或非 http(s)）")
	}

	method := d.Request.Method
	bodyStr := renderTpl(d.Request.Body, c)
	var body io.Reader
	if bodyStr != "" {
		body = strings.NewReader(bodyStr)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败：%w", err)
	}
	q := req.URL.Query()
	for k, v := range d.Request.Query {
		q.Add(k, renderTpl(v, c))
	}
	req.URL.RawQuery = q.Encode()
	for k, v := range d.Request.Headers {
		if hv := renderTpl(v, c); strings.TrimSpace(hv) != "" {
			req.Header.Set(k, hv)
		}
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "MediaSuper-Plugin/1.0")
	}
	if bodyStr != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	tr.step("请求 %s %s", method, url)
	if debug {
		tr.Request = fmt.Sprintf("%s %s\nheaders=%v\nbody=%s", method, req.URL.String(), redactHeaders(req.Header), bodyStr)
	}

	timeout := time.Duration(d.Request.TimeoutSec) * time.Second
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req.WithContext(cctx))
	if err != nil {
		if cctx.Err() == context.DeadlineExceeded || os.IsTimeout(err) {
			return nil, fmt.Errorf("解析服务响应超时（%d 秒），请检查网络或调大 timeout_sec", d.Request.TimeoutSec)
		}
		return nil, fmt.Errorf("请求解析服务失败：%w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("读取解析结果失败：%w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("解析服务返回 HTTP %d", resp.StatusCode)
	}
	tr.step("响应 HTTP %d，%d 字节", resp.StatusCode, len(raw))
	if debug {
		s := string(raw)
		if len(s) > 4000 {
			s = s[:4000] + "…"
		}
		tr.Response = s
	}

	var root any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("解析结果不是合法 JSON：%w", err)
	}
	if expr := strings.TrimSpace(d.Response.SuccessWhen); expr != "" {
		if msg := checkSuccess(root, expr, d.Response.ErrorField); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
	}

	f := d.Response.Fields
	get := func(key, def string) string {
		path := def
		if p, ok := f[key]; ok && strings.TrimSpace(p) != "" {
			path = p
		}
		return strings.TrimSpace(stringify(getPath(root, path)))
	}
	title := get("title", "data.title")
	content := get("content", "data.content")
	res := &parser.Result{
		Plugin:      i.Manifest.ID,
		PluginName:  i.Manifest.Name,
		SourceURL:   firstNonEmpty(get("source_url", "data.source_url"), rawURL),
		Title:       title,
		Cover:       safeHTTP(get("cover", "data.cover")),
		Author:      get("author", "data.author"),
		Description: get("description", "data.description"),
		PublishTime: get("publish_time", "data.publish_time"),
		ContentText: stripHTML(content, 600),
		ContentLen:  len(content),
		Images:      []string{},
		Items:       []parser.Item{},
		Extra:       map[string]any{},
	}
	// 约定配置项：auto_download=true 时，前端解析卡片自动把全部对象加入下载队列
	if b, ok := i.State.Config["auto_download"].(bool); ok && b {
		res.Extra["auto_download"] = true
	}
	if p, ok := f["images"]; ok {
		for _, u := range asStringList(getPath(root, p)) {
			if s := safeHTTP(u); s != "" {
				res.Images = append(res.Images, s)
			}
		}
		if len(res.Images) > 40 {
			res.Images = res.Images[:40]
		}
	}
	if f != nil {
		if p, ok := f["plugin_name"]; ok {
			res.PluginName = firstNonEmpty(stringify(getPath(root, p)), i.Manifest.Name)
		}
	}

	it := d.Response.Items
	if it != nil && strings.TrimSpace(it.From) != "" {
		arr := getPath(root, it.From)
		list, ok := arr.([]any)
		if !ok {
			list = []any{}
			for _, s := range asStringList(arr) {
				list = append(list, s)
			}
		}
		max := it.Max
		if max <= 0 {
			max = 100
		}
		tr.step("字段 %s 取到 %d 条，展开为可下载对象", it.From, len(list))
		for idx, elem := range list {
			if len(res.Items) >= max {
				break
			}
			ic := c
			ic.Value = elem
			ic.Index = idx
			ic.Title = title
			u := strings.TrimSpace(renderTpl(it.URL, ic))
			if u == "" {
				// 元素是对象且没写模板时，尝试常见的 url/link 字段
				u = strings.TrimSpace(stringify(getPath(elem, "url")))
			}
			if !isHTTP(u) {
				continue
			}
			name := strings.TrimSpace(renderTpl(it.Title, ic))
			if name == "" {
				name = fmt.Sprintf("%s · %02d", firstNonEmpty(title, i.Manifest.Name), idx+1)
				if len(list) == 1 {
					name = firstNonEmpty(title, i.Manifest.Name)
				}
			}
			kind := it.Kind
			if kind == "" {
				kind = "video"
			}
			format := it.Format
			if format == "" {
				format = "mp4"
			}
			headers := map[string]string{}
			for k, v := range it.Headers {
				if hv := strings.TrimSpace(renderTpl(v, ic)); hv != "" {
					headers[k] = hv
				}
			}
			item := parser.Item{
				ID:  strings.TrimSpace(renderTpl(it.ID, ic)),
				URL: u, Title: name,
				Cover: safeHTTP(renderTpl(it.Cover, ic)),
				Page:  safeHTTP(renderTpl(it.Page, ic)),
				Kind:  kind, Format: format,
			}
			if len(headers) > 0 {
				item.Headers = headers
			}
			res.Items = append(res.Items, item)
		}
	}
	return res, nil
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func redactHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		lk := strings.ToLower(k)
		if lk == "authorization" || lk == "cookie" || lk == "x-api-key" {
			out[k] = "***"
		} else {
			out[k] = strings.Join(v, ",")
		}
	}
	return out
}

var reSuccessExpr = regexp.MustCompile(`^\s*([A-Za-z0-9_.\[\]]+)\s*(==|!=)\s*(.+?)\s*$`)

// checkSuccess 支持 "code == 1" / "data.ok == true" / "errcode != 0" 这类简单断言。
// 返回空串表示通过，否则返回给用户看的错误说明。
func checkSuccess(root any, expr, errField string) string {
	m := reSuccessExpr.FindStringSubmatch(expr)
	if m == nil {
		return ""
	}
	actual := strings.TrimSpace(stringify(getPath(root, m[1])))
	expect := strings.Trim(strings.TrimSpace(m[3]), `"'`)
	eq := (actual == expect) || (expect == "true" && actual == "1") || (expect == "false" && (actual == "0" || actual == ""))
	if m[2] == "!=" {
		eq = !eq
	}
	if eq {
		return ""
	}
	msg := ""
	if errField != "" {
		msg = strings.TrimSpace(stringify(getPath(root, errField)))
	}
	if msg == "" {
		msg = fmt.Sprintf("接口返回失败（%s = %s，期望 %s %s）", m[1], firstNonEmpty(actual, "空"), m[2], expect)
	}
	return msg
}

// ---------- Go 子进程引擎 ----------

// goRequest 是主程序通过 stdin 传给插件可执行文件的一行 JSON。
type goRequest struct {
	URL    string         `json:"url"`
	Config map[string]any `json:"config"`
	Mode   string         `json:"mode"` // parse | debug
}

type goResponse struct {
	OK     bool           `json:"ok"`
	Result *parser.Result `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"`
	Logs   []string       `json:"logs,omitempty"`
	Extra  map[string]any `json:"extra,omitempty"`
}

func (m *Manager) runGo(ctx context.Context, i *Installed, rawURL string, tr *Trace, debug bool) (*parser.Result, error) {
	bin := i.binaryPath()
	st, err := os.Stat(bin)
	if err != nil || st.IsDir() {
		return nil, fmt.Errorf("插件尚未编译：请点「编译」或用 MCP plugin_build 生成 %s", filepath.Base(bin))
	}
	mode := "parse"
	if debug {
		mode = "debug"
	}
	payload, _ := json.Marshal(goRequest{URL: rawURL, Config: i.State.Config, Mode: mode})
	tr.step("启动子进程 %s，stdin %d 字节", filepath.Base(bin), len(payload))
	if debug {
		tr.Request = string(payload)
	}

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin)
	cmd.Stdin = bytes.NewReader(append(payload, '\n'))
	cmd.Dir = i.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		logs := splitLines(stderr.String())
		tr.Logs = append(tr.Logs, logs...)
		if cctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("插件执行超时（30 秒）")
		}
		return nil, fmt.Errorf("插件进程启动失败：%w（请先编译；日志：%s）", err, firstLine(stderr.String()))
	}
	if debug {
		tr.Logs = append(tr.Logs, splitLines(stderr.String())...)
		s := stdout.String()
		if len(s) > 4000 {
			s = s[:4000] + "…"
		}
		tr.Response = s
	}
	// 取 stdout 最后一行非空内容作为 JSON 响应；前面允许插件打日志
	lines := splitLines(stdout.String())
	var out goResponse
	parsed := false
	for n := len(lines) - 1; n >= 0; n-- {
		l := strings.TrimSpace(lines[n])
		if !strings.HasPrefix(l, "{") {
			continue
		}
		if err := json.Unmarshal([]byte(l), &out); err == nil {
			parsed = true
			break
		}
	}
	if !parsed {
		return nil, fmt.Errorf("插件输出不是合法 JSON（协议要求 stdout 最后一行输出一行 JSON）")
	}
	if len(out.Logs) > 0 {
		tr.Logs = append(tr.Logs, out.Logs...)
	}
	if !out.OK {
		return nil, fmt.Errorf("%s", firstNonEmpty(out.Error, "插件返回失败"))
	}
	if out.Result == nil {
		return nil, fmt.Errorf("插件返回 ok=true 但缺少 result")
	}
	tr.step("子进程返回 %d 个可下载对象", len(out.Result.Items))
	return out.Result, nil
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	parts := strings.Split(strings.TrimSpace(s), "\n")
	out := []string{}
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}

func firstLine(s string) string {
	l := splitLines(s)
	if len(l) == 0 {
		return "(无日志)"
	}
	return l[0]
}

// BuildResult 是编译结果。
type BuildResult struct {
	OK      bool     `json:"ok"`
	Binary  string   `json:"binary,omitempty"`
	Output  string   `json:"output,omitempty"`
	Logs    []string `json:"logs,omitempty"`
	GoBin   string   `json:"go_bin,omitempty"`
	Message string   `json:"message,omitempty"`
}

// Build 编译 Go 插件。NAS 上通常没有 Go 工具链，此时返回明确的降级提示：
// 可以在开发机编译后只上传 bin/ 产物，或改用 declarative 引擎（零编译）。
func (m *Manager) Build(ctx context.Context, id string) (BuildResult, error) {
	i, ok := m.Get(id)
	if !ok {
		return BuildResult{}, fmt.Errorf("插件 %s 不存在", id)
	}
	if i.Manifest.Engine != EngineGo {
		return BuildResult{OK: false, Message: "只有 engine=go 的插件需要编译"}, nil
	}
	goBin := strings.TrimSpace(os.Getenv("MS_GO_BIN"))
	if goBin == "" {
		var err error
		goBin, err = exec.LookPath("go")
		if err != nil {
			return BuildResult{
				OK: false,
				Message: "当前设备没有 Go 工具链。两种替代方案：① 把 engine 改为 declarative（零编译、上传即用）；" +
					"② 在开发机执行 go build 后把可执行文件放进插件目录的 bin/ 下，再上传。",
			}, nil
		}
	}
	// 没有 go.mod 就补一个最小模块文件，让源码能用标准库编译
	modPath := filepath.Join(i.Dir, "go.mod")
	if _, err := os.Stat(modPath); err != nil {
		mod := fmt.Sprintf("module %s\n\ngo %s\n", i.Manifest.ID, strings.TrimPrefix(runtime.Version(), "go"))
		if err := os.WriteFile(modPath, []byte(mod), 0o644); err != nil {
			return BuildResult{}, err
		}
	}
	bin := i.binaryPath()
	if runtime.GOOS == "windows" && !strings.HasSuffix(bin, ".exe") {
		bin += ".exe"
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return BuildResult{}, err
	}
	args := []string{"build", "-o", bin}
	sources := i.Manifest.Entry.Sources
	if len(sources) == 0 {
		args = append(args, ".")
	} else {
		for _, s := range sources {
			p, err := safeJoin(i.Dir, s)
			if err != nil {
				return BuildResult{}, err
			}
			args = append(args, p)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, goBin, args...)
	cmd.Dir = i.Dir
	cmd.Env = append(os.Environ(),
		"GO111MODULE=on", "CGO_ENABLED=0",
		"GOTOOLCHAIN=local",
		"GOPROXY="+firstNonEmpty(os.Getenv("GOPROXY"), "https://goproxy.cn,direct"))
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	res := BuildResult{GoBin: goBin, Logs: splitLines(out.String()), Output: out.String()}
	if err != nil {
		res.OK = false
		res.Message = fmt.Sprintf("编译失败：%v。常见原因：源码缺少 package main / func main，或 go 版本不兼容。", err)
		return res, nil
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(bin, 0o755)
	}
	res.OK = true
	res.Binary = bin
	res.Message = "编译成功，插件已可运行"
	return res, nil
}
