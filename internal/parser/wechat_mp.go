package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const defaultWechatEndpoint = "https://t.losfer.cn/apis/parse/mp/parseArticle"

// wechatEndpoint 允许用环境变量换成自建/备用解析服务，默认走文档里的公共服务。
func wechatEndpoint() string {
	if v := strings.TrimSpace(os.Getenv("MS_MP_PARSE_API")); v != "" {
		return v
	}
	return defaultWechatEndpoint
}

// WechatMP 是第一个内置插件：微信公众号文章 /s/ 链接。
// 解析服务返回文章元信息与 movie_down_url（通常是可直接下载的 mp4 直链，
// 不需要 ffmpeg 合成），插件把它们映射成统一的 Result/Item。
type WechatMP struct {
	endpoint string
	client   *http.Client
}

func NewWechatMP(endpoint string) *WechatMP {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = defaultWechatEndpoint
	}
	return &WechatMP{
		endpoint: strings.TrimSpace(endpoint),
		client:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (p *WechatMP) ID() string          { return "wechat_mp" }
func (p *WechatMP) Name() string        { return "微信公众号" }
func (p *WechatMP) Description() string { return "解析 mp.weixin.qq.com/s/ 文章，提取文章信息与视频下载直链（mp4）" }
func (p *WechatMP) Patterns() []string {
	return []string{"https://mp.weixin.qq.com/s/*", "https://mp.weixin.qq.com/s?__biz=*"}
}

// Match 只做本地字符串判断：host 必须是 mp.weixin.qq.com，路径是 /s 或 /s/xxx。
func (p *WechatMP) Match(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Hostname() == "" {
		return false
	}
	if !strings.EqualFold(u.Hostname(), "mp.weixin.qq.com") {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	path := u.Path
	return path == "/s" || strings.HasPrefix(path, "/s/")
}

// mpData 与解析接口 data 字段一一对应，字段全部宽松处理，缺字段不影响其它字段。
type mpData struct {
	Title         string       `json:"title"`
	Cover         string       `json:"cover"`
	Author        string       `json:"author"`
	Description   string       `json:"description"`
	PublishTime   flexString   `json:"publish_time"`
	Content       string       `json:"content"`
	Images        []string     `json:"images"`
	WXVFields     []string     `json:"wxv_fields"`
	MovieDownURL  []string     `json:"movie_down_url"`
	WXVURLs       []string     `json:"wxv_urls"`
	VoiceIDs      []string     `json:"voice_ids"`
	AudioDownURLs []string     `json:"audio_down_urls"`
	SourceURL     string       `json:"source_url"`
	APIDesc       string       `json:"api_desc"`
}

type mpEnvelope struct {
	Code    flexInt   `json:"code"`
	Msg     string    `json:"msg"`
	Time    flexString `json:"time"`
	Runtime float64   `json:"runtime"`
	Data    mpData    `json:"data"`
}

// flexString / flexInt 兼容接口偶尔把数字写成字符串（或反过来）的情况。
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		*f = ""
		return nil
	}
	if len(s) >= 2 && s[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*f = flexString(v)
		return nil
	}
	*f = flexString(s)
	return nil
}

type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return nil // 非数字按 0 处理，交给 code 判定
	}
	*f = flexInt(v)
	return nil
}

func (p *WechatMP) Parse(ctx context.Context, rawURL string) (*Result, error) {
	rawURL = strings.TrimSpace(rawURL)
	if !p.Match(rawURL) {
		return nil, fmt.Errorf("不是微信公众号文章链接")
	}
	u, err := url.Parse(p.endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("解析服务地址配置不合法")
	}
	q := u.Query()
	q.Set("url", rawURL)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "MediaSuper-Parser/1.0")

	resp, err := p.client.Do(req)
	if err != nil {
		// 外部解析服务偶发不可用，别把 "Client.Timeout exceeded while awaiting headers"
		// 这种底层细节甩给界面上的用户
		if ctx.Err() == context.DeadlineExceeded || os.IsTimeout(err) {
			return nil, fmt.Errorf("解析服务响应超时，请稍后重试")
		}
		return nil, fmt.Errorf("请求解析服务失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("解析服务返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("读取解析结果失败：%w", err)
	}
	var env mpEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("解析结果不是合法 JSON：%w", err)
	}
	if env.Code != 1 {
		msg := strings.TrimSpace(env.Msg)
		if msg == "" {
			msg = "未返回有效数据"
		}
		return nil, fmt.Errorf("%s（code=%d）", msg, env.Code)
	}
	return p.toResult(rawURL, &env), nil
}

func (p *WechatMP) toResult(rawURL string, env *mpEnvelope) *Result {
	d := env.Data
	title := strings.TrimSpace(d.Title)
	source := strings.TrimSpace(d.SourceURL)
	if source == "" {
		source = rawURL
	}

	res := &Result{
		Plugin:      p.ID(),
		PluginName:  p.Name(),
		SourceURL:   source,
		Title:       title,
		Cover:       safeHTTP(d.Cover),
		Author:      strings.TrimSpace(d.Author),
		Description: strings.TrimSpace(d.Description),
		PublishTime: string(d.PublishTime),
		ContentText: stripHTML(d.Content, 600),
		ContentLen:  len(d.Content),
		Images:      safeHTTPList(d.Images, 40),
		Items:       make([]Item, 0, len(d.MovieDownURL)),
		Extra: map[string]any{
			"api_desc":        d.APIDesc,
			"wxv_fields":      d.WXVFields,
			"wxv_urls":        d.WXVURLs,
			"voice_ids":       d.VoiceIDs,
			"audio_down_urls": d.AudioDownURLs,
			"video_count":     len(d.MovieDownURL),
			"audio_count":     len(d.AudioDownURLs),
			"image_count":     len(d.Images),
			"runtime_ms":      env.Runtime,
			"server_time":     string(env.Time),
		},
	}

	for i, link := range d.MovieDownURL {
		link = strings.TrimSpace(link)
		if !isHTTP(link) {
			continue
		}
		id, page := "", ""
		if i < len(d.WXVFields) {
			id = strings.TrimSpace(d.WXVFields[i])
		}
		if i < len(d.WXVURLs) {
			page = safeHTTP(d.WXVURLs[i])
		}
		name := fmt.Sprintf("%s · %02d", title, i+1)
		if len(d.MovieDownURL) == 1 {
			name = title
		}
		if strings.TrimSpace(title) == "" {
			name = fmt.Sprintf("微信视频 %02d", i+1)
			if id != "" {
				name = "微信视频 " + id
			}
		}
		res.Items = append(res.Items, Item{
			ID:      id,
			URL:     link,
			Title:   name,
			Cover:   res.Cover,
			Kind:    "video",
			Format:  "mp4",
			Page:    page,
			Headers: map[string]string{"Referer": "https://mp.weixin.qq.com/"},
		})
		if len(res.Items) >= 100 {
			break
		}
	}
	return res
}

func isHTTP(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func safeHTTP(s string) string {
	s = strings.TrimSpace(s)
	if isHTTP(s) {
		return s
	}
	return ""
}

func safeHTTPList(in []string, max int) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if v := safeHTTP(s); v != "" {
			out = append(out, v)
		}
		if len(out) >= max {
			break
		}
	}
	return out
}

var (
	reTag   = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>|<[^>]*>`)
	reSpace = regexp.MustCompile(`\s+`)
)

// stripHTML 把正文 HTML 压成一段纯文本摘要，避免把整篇富文本塞给前端，
// 也避免前端渲染外部 HTML 带来的注入风险。
func stripHTML(s string, limit int) string {
	if s == "" {
		return ""
	}
	s = reTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
	r := []rune(s)
	if limit > 0 && len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return s
}
