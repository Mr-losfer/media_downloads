// Package plugin 实现 Media Super 的「文件夹即插件」体系。
//
// 设计要点：
//   - 一个插件 = <data>/plugins/<id>/ 一个文件夹 + 根目录的 plugin.json；
//     文件夹里可以有多个 Go 源码文件、资源文件、README 等。
//   - plugin.json 是唯一权威元数据：声明匹配哪些域名、暴露哪些可视化配置项、
//     用哪种执行引擎、入口在哪。运行期状态（启用/配置值/来源）单独存在
//     <data>/plugins/_state.json，不污染插件目录，方便导出与提交到 Git。
//   - 三种执行引擎：
//     builtin     —— 编译进主程序的实现（公众号解析），性能最好、零依赖；
//     declarative —— plugin.json 里的 HTTP + 字段映射规则，主程序内置解释执行，
//     零编译、跨架构、上传即可用（推荐给大多数解析站）；
//     go          —— Go 语言源码编译成独立可执行文件，主程序用 stdin/stdout
//     的 JSON 行协议调用（Hashicorp go-plugin 风格），跨版本、
//     跨依赖、可沙箱化，适合复杂逻辑。
//   - 首页提交链接时，管理器按「启用 + 域名/正则命中」挑一个插件接管，
//     统一产出 parser.Result，前端与下载引擎完全不用改。
package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Engine 常量。
const (
	EngineBuiltin     = "builtin"
	EngineDeclarative = "declarative"
	EngineGo          = "go"
)

// ConfigType 常量：决定设置弹窗里渲染成什么控件。
const (
	TypeText     = "text"
	TypePassword = "password"
	TypeNumber   = "number"
	TypeSwitch   = "switch"
	TypeSelect   = "select"
	TypeTextarea = "textarea"
	TypeURL      = "url"
)

// Origin 常量：插件来源，前端用于显示徽标。
const (
	OriginBuiltin = "builtin"
	OriginLocal   = "local"
	OriginMarket  = "market"
)

// SelectOption 是 select 控件的一个选项。
type SelectOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// ConfigField 是「代码中定义的用户可视化配置选项」。
//
// 插件作者只要在 plugin.json 的 config_schema 里写一段，插件列表就会出现
// 「配置」按钮；点击后弹窗按 type 渲染控件，保存的值以 {{config.<key>}} 注入
// 声明式规则 / 请求模板，也会通过 stdin 的 config 对象传给 Go 插件。
type ConfigField struct {
	Key         string         `json:"key"`
	Label       string         `json:"label"`
	Type        string         `json:"type"`
	Desc        string         `json:"desc,omitempty"`
	Default     any            `json:"default,omitempty"`
	Required    bool           `json:"required,omitempty"`
	Placeholder string         `json:"placeholder,omitempty"`
	Options     []SelectOption `json:"options,omitempty"`
	Min         *float64       `json:"min,omitempty"`
	Max         *float64       `json:"max,omitempty"`
	Group       string         `json:"group,omitempty"`
}

// MatchRule 定义「提交哪一种链接时运行这个插件」。
// 多个字段之间是「与」关系；domains 内部是「或」关系；都不填表示不匹配任何链接
// （可由 MCP / 测试接口显式指定 id 调用）。
type MatchRule struct {
	// Domains 精确域名或通配符（*.example.com 同时匹配 example.com 与子域）。
	Domains []string `json:"domains,omitempty"`
	// URLPatterns 是 Go 正则，命中整条 URL 即可（可选，用于更精细的路由）。
	URLPatterns []string `json:"url_patterns,omitempty"`
	// PathPrefixes 路径前缀，如 ["/s", "/video"]。
	PathPrefixes []string `json:"path_prefixes,omitempty"`
	// Schemes 允许的协议，为空表示 http/https 都行。
	Schemes []string `json:"schemes,omitempty"`
	// Extensions 允许的扩展名（小写，带点），如 [".m3u8", ".mp4"]。
	Extensions []string `json:"extensions,omitempty"`
	// Priority 越大越先匹配，默认 0。
	Priority int `json:"priority,omitempty"`
}

// Capabilities 声明插件能力，前端据此决定是否显示解析结果卡片。
type Capabilities struct {
	Parse        bool `json:"parse"`
	Download     bool `json:"download,omitempty"`
	Configurable bool `json:"configurable,omitempty"`
}

// Entry 描述 Go 引擎的源码与产物位置。
type Entry struct {
	// Sources 是参与编译的 Go 文件（相对插件目录），为空时自动扫描 *.go。
	Sources []string `json:"sources,omitempty"`
	// Binary 是编译产物相对路径，默认 bin/<id>；Windows 自动补 .exe。
	Binary string `json:"binary,omitempty"`
	// Package 固定为 main（SDK 约定），保留字段供以后扩展。
	Package string `json:"package,omitempty"`
}

// DeclarativeRequest 是一次 HTTP 请求的模板。
type DeclarativeRequest struct {
	Method     string            `json:"method,omitempty"` // GET（默认）/ POST
	URL        string            `json:"url"`              // 支持 {{...}} 模板
	Query      map[string]string `json:"query,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body,omitempty"`
	TimeoutSec int               `json:"timeout_sec,omitempty"` // 默认 15，最大 60
}

// DeclarativeItems 把响应里的一个数组展开成可下载对象列表。
type DeclarativeItems struct {
	// From 是数组在响应 JSON 里的点路径，如 data.movie_down_url。
	From string `json:"from"`
	// URL 模板：可用 {{value}}（数组元素本身）、{{item.xxx}}（元素是对象时）、
	// {{title}} / {{index}} / {{index1}} / {{source_url}} / {{config.xxx}}。
	URL string `json:"url"`
	// Title 模板，同上；留空则用「标题 · 序号」。
	Title string `json:"title,omitempty"`
	// Cover / Page / ID 也是模板，可留空。
	Cover  string `json:"cover,omitempty"`
	Page   string `json:"page,omitempty"`
	ID     string `json:"id,omitempty"`
	Kind   string `json:"kind,omitempty"`   // 默认 video
	Format string `json:"format,omitempty"` // 默认 mp4
	// Headers 随 Item 一起提交给下载引擎（防盗链）。值同样支持模板。
	Headers map[string]string `json:"headers,omitempty"`
	// Max 最多取多少条，默认 100。
	Max int `json:"max,omitempty"`
}

// DeclarativeResponse 定义如何把 HTTP 响应映射成统一 Result。
type DeclarativeResponse struct {
	// Format 目前只支持 json。
	Format string `json:"format,omitempty"`
	// SuccessWhen 形如 "code == 1" / "data.ok == true"，留空表示不校验。
	SuccessWhen string `json:"success_when,omitempty"`
	// ErrorField 失败时从哪个点路径取错误信息。
	ErrorField string `json:"error_field,omitempty"`
	// Fields: 结果字段名 -> 响应点路径。
	// 支持 title / cover / author / description / publish_time / content /
	// source_url / images / plugin_name。
	Fields map[string]string `json:"fields,omitempty"`
	Items  *DeclarativeItems `json:"items,omitempty"`
}

// Declarative 是零编译引擎的完整规则。
type Declarative struct {
	Request  DeclarativeRequest  `json:"request"`
	Response DeclarativeResponse `json:"response"`
}

// Manifest 对应插件目录根下的 plugin.json。
type Manifest struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Version       string        `json:"version"`
	Author        string        `json:"author,omitempty"`
	Description   string        `json:"description,omitempty"`
	Homepage      string        `json:"homepage,omitempty"`
	Icon          string        `json:"icon,omitempty"`
	Engine        string        `json:"engine"`
	Match         MatchRule     `json:"match"`
	ConfigSchema  []ConfigField `json:"config_schema"`
	Capabilities  Capabilities  `json:"capabilities,omitempty"`
	Entry         Entry         `json:"entry,omitempty"`
	Declarative   *Declarative  `json:"declarative,omitempty"`
	MinAppVersion string        `json:"min_app_version,omitempty"`
	Tags          []string      `json:"tags,omitempty"`
	Changelog     string        `json:"changelog,omitempty"`
	// Builtin 为 true 表示随主程序分发，禁止删除与改 id（可禁用、可配置、可复制）。
	Builtin bool `json:"builtin,omitempty"`
}

var rePluginID = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,47}$`)
var reVersion = regexp.MustCompile(`^\d+\.\d+\.\d+([-+][0-9A-Za-z.-]+)?$`)

// ParseManifest 解析并严格校验一份 plugin.json，返回规范化后的 Manifest。
func ParseManifest(raw []byte) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("plugin.json 不是合法 JSON 或含未知字段：%w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate 是上架 / 上传 / 保存前共用的规范校验。
func (m *Manifest) Validate() error {
	m.ID = strings.ToLower(strings.TrimSpace(m.ID))
	m.Name = strings.TrimSpace(m.Name)
	m.Version = strings.TrimSpace(m.Version)
	m.Engine = strings.ToLower(strings.TrimSpace(m.Engine))

	if !rePluginID.MatchString(m.ID) {
		return fmt.Errorf("id 不合法：必须是 2–48 位小写字母开头、只含 a-z/0-9/_- 的字符串（如 wechat_mp）")
	}
	if m.Name == "" {
		return fmt.Errorf("name 不能为空")
	}
	if !reVersion.MatchString(m.Version) {
		return fmt.Errorf("version 必须形如 1.0.0（语义化版本）")
	}
	switch m.Engine {
	case EngineBuiltin:
		// 内置实现由主程序提供，不需要额外文件
	case EngineDeclarative:
		if m.Declarative == nil {
			return fmt.Errorf("engine=declarative 必须提供 declarative 规则")
		}
		if strings.TrimSpace(m.Declarative.Request.URL) == "" {
			return fmt.Errorf("declarative.request.url 不能为空")
		}
	case EngineGo:
		// 源码文件由目录扫描，这里不强制
	default:
		return fmt.Errorf("engine 只能是 builtin / declarative / go，当前为 %q", m.Engine)
	}

	if m.Capabilities.Parse == false && m.Capabilities.Download == false {
		m.Capabilities.Parse = true // 默认至少能解析
	}
	m.Capabilities.Configurable = len(m.ConfigSchema) > 0

	seen := map[string]bool{}
	for i := range m.ConfigSchema {
		f := &m.ConfigSchema[i]
		f.Key = strings.TrimSpace(f.Key)
		f.Type = strings.ToLower(strings.TrimSpace(f.Type))
		if f.Key == "" {
			return fmt.Errorf("config_schema[%d].key 不能为空", i)
		}
		if seen[f.Key] {
			return fmt.Errorf("config_schema 里 key %q 重复", f.Key)
		}
		seen[f.Key] = true
		if f.Label == "" {
			f.Label = f.Key
		}
		switch f.Type {
		case "":
			f.Type = TypeText
		case TypeText, TypePassword, TypeNumber, TypeSwitch, TypeSelect, TypeTextarea, TypeURL:
		default:
			return fmt.Errorf("config_schema[%d].type %q 不支持（可用 text/password/number/switch/select/textarea/url）", i, f.Type)
		}
		if f.Type == TypeSelect && len(f.Options) == 0 {
			return fmt.Errorf("config_schema[%d] 是 select 但缺少 options", i)
		}
	}
	for i, p := range m.Match.URLPatterns {
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("match.url_patterns[%d] 正则不合法：%w", i, err)
		}
	}
	if m.Declarative != nil {
		d := m.Declarative
		if d.Request.Method == "" {
			d.Request.Method = "GET"
		}
		d.Request.Method = strings.ToUpper(d.Request.Method)
		if d.Request.Method != "GET" && d.Request.Method != "POST" {
			return fmt.Errorf("declarative.request.method 只支持 GET / POST")
		}
		if d.Request.TimeoutSec <= 0 {
			d.Request.TimeoutSec = 15
		}
		if d.Request.TimeoutSec > 60 {
			d.Request.TimeoutSec = 60
		}
		if d.Response.Format == "" {
			d.Response.Format = "json"
		}
		if d.Response.Items != nil && d.Response.Items.Max <= 0 {
			d.Response.Items.Max = 100
		}
	}
	return nil
}

// DefaultConfig 按 config_schema 生成一份默认配置值。
func (m *Manifest) DefaultConfig() map[string]any {
	out := make(map[string]any, len(m.ConfigSchema))
	for _, f := range m.ConfigSchema {
		if f.Default != nil {
			out[f.Key] = f.Default
			continue
		}
		switch f.Type {
		case TypeSwitch:
			out[f.Key] = false
		case TypeNumber:
			out[f.Key] = 0
		default:
			out[f.Key] = ""
		}
	}
	return out
}

// ValidateConfig 校验用户提交的配置值（必填、数字范围、select 选项、类型）。
func (m *Manifest) ValidateConfig(cfg map[string]any) (map[string]any, error) {
	out := m.DefaultConfig()
	for k, v := range cfg {
		out[k] = v
	}
	for _, f := range m.ConfigSchema {
		v, ok := out[f.Key]
		if !ok || v == nil {
			v = ""
		}
		s := fmt.Sprint(v)
		if f.Required && strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("配置项「%s」不能为空", f.Label)
		}
		switch f.Type {
		case TypeNumber:
			var n float64
			switch x := v.(type) {
			case float64:
				n = x
			case int:
				n = float64(x)
			case json.Number:
				n, _ = x.Float64()
			case string:
				if _, err := fmt.Sscanf(x, "%g", &n); err != nil {
					return nil, fmt.Errorf("配置项「%s」必须是数字", f.Label)
				}
			default:
				return nil, fmt.Errorf("配置项「%s」必须是数字", f.Label)
			}
			if f.Min != nil && n < *f.Min {
				return nil, fmt.Errorf("配置项「%s」不能小于 %g", f.Label, *f.Min)
			}
			if f.Max != nil && n > *f.Max {
				return nil, fmt.Errorf("配置项「%s」不能大于 %g", f.Label, *f.Max)
			}
			out[f.Key] = n
		case TypeSwitch:
			b, ok := v.(bool)
			if !ok {
				b = s == "true" || s == "1" || s == "on"
			}
			out[f.Key] = b
		case TypeSelect:
			valid := false
			for _, o := range f.Options {
				if o.Value == s {
					valid = true
					break
				}
			}
			if !valid && s != "" {
				return nil, fmt.Errorf("配置项「%s」的值不在可选项里", f.Label)
			}
		case TypeURL:
			if s != "" && !strings.HasPrefix(strings.ToLower(s), "http://") && !strings.HasPrefix(strings.ToLower(s), "https://") {
				return nil, fmt.Errorf("配置项「%s」必须是 http(s) 地址", f.Label)
			}
		}
	}
	return out, nil
}
