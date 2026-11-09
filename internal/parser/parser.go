// Package parser 提供可插拔的「链接解析」能力。
//
// 首页提交链接时，服务端先让所有已注册插件依次 Match；命中的插件把原始页面
// 解析成统一的 Result（展示信息 + 可下载对象列表），前端展示解析结果，并把用户
// 勾选的 Item 作为普通下载任务提交给现有引擎。
//
// 新增站点支持只需实现 Parser 接口并注册进 Default()，不用改前端、路由和下载引擎。
package parser

import (
	"context"
	"sync"
)

// Item 是一条可下载对象。插件负责把页面里的媒体直链映射成 Item；
// 前端勾选后原样组装成 POST /api/tasks 的请求体。
type Item struct {
	ID      string            `json:"id"`                 // 站点内唯一标识（如 wxv_xxx）
	URL     string            `json:"url"`                // 真实下载直链
	Title   string            `json:"title"`              // 建议文件名/显示名
	Cover   string            `json:"cover,omitempty"`
	Kind    string            `json:"kind"`               // video | audio | image | file
	Format  string            `json:"format,omitempty"`   // mp4 / mp3 ...
	Page    string            `json:"page,omitempty"`     // 原始播放页链接
	Headers map[string]string `json:"headers,omitempty"`  // 建议携带的请求头（防盗链等）
}

// Result 是所有插件共用的解析结果形状，前端只认这一种结构。
// 后续插件可以往 Extra 里塞自定义字段（前端按通用键值渲染），无需改动结构定义。
type Result struct {
	Plugin      string         `json:"plugin"`
	PluginName  string         `json:"plugin_name"`
	SourceURL   string         `json:"source_url"`
	Title       string         `json:"title"`
	Cover       string         `json:"cover"`
	Author      string         `json:"author"`
	Description string         `json:"description"`
	PublishTime string         `json:"publish_time"` // 原始值，前端按浏览器时区分辨时间戳
	ContentText string         `json:"content_text"` // 剥离标签后的正文摘要
	ContentLen  int            `json:"content_len"`  // 原始正文 HTML 长度
	Images      []string       `json:"images"`
	Items       []Item         `json:"items"`
	Extra       map[string]any `json:"extra,omitempty"`
}

// Info 是插件自述信息，供 GET /api/parsers 与文档页展示。
type Info struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Patterns    []string `json:"patterns"`
}

// Parser 是所有解析插件必须实现的接口。
// Match 只做廉价的字符串判断（不发网络请求），Parse 才真正访问外部服务。
type Parser interface {
	ID() string
	Name() string
	Description() string
	Patterns() []string
	Match(rawURL string) bool
	Parse(ctx context.Context, rawURL string) (*Result, error)
}

// Registry 是插件注册表，注册顺序即匹配优先级。
type Registry struct {
	mu    sync.RWMutex
	items []Parser
}

func NewRegistry() *Registry { return &Registry{} }

// Register 注册一个插件，同 id 只保留第一次注册，方便测试里覆盖。
func (r *Registry) Register(p Parser) {
	if p == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.items {
		if x.ID() == p.ID() {
			return
		}
	}
	r.items = append(r.items, p)
}

// Match 返回第一个接管该链接的插件，没有则返回 nil。
func (r *Registry) Match(rawURL string) Parser {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.items {
		if p.Match(rawURL) {
			return p
		}
	}
	return nil
}

func (r *Registry) ByID(id string) Parser {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.items {
		if p.ID() == id {
			return p
		}
	}
	return nil
}

func (r *Registry) All() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Info, 0, len(r.items))
	for _, p := range r.items {
		out = append(out, Info{
			ID:          p.ID(),
			Name:        p.Name(),
			Description: p.Description(),
			Patterns:    p.Patterns(),
		})
	}
	return out
}

var (
	defaultOnce sync.Once
	defaultReg  *Registry
)

// Default 是进程内唯一的内置插件注册表。新增插件在下面 Register 一行即可。
func Default() *Registry {
	defaultOnce.Do(func() {
		defaultReg = NewRegistry()
		defaultReg.Register(NewWechatMP(wechatEndpoint()))
	})
	return defaultReg
}
