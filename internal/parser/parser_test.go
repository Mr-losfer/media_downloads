package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWechatMatch(t *testing.T) {
	p := NewWechatMP("")
	cases := []struct {
		url  string
		want bool
	}{
		{"https://mp.weixin.qq.com/s/epF_Tih8U0wNUUazf0TrsA", true},
		{"http://mp.weixin.qq.com/s/abcdef", true},
		{"https://mp.weixin.qq.com/s?__biz=MzI1&mid=1&idx=1&sn=x", true},
		{"  https://mp.weixin.qq.com/s/abc  ", true},
		{"https://MP.WEIXIN.QQ.COM/s/abc", true},
		{"https://mp.weixin.qq.com/mp/readtemplate?t=pages/video_player_tmpl", false},
		{"https://mp.weixin.qq.com/", false},
		{"https://example.com/s/abc", false},
		{"javascript:alert(1)", false},
		{"", false},
		{"not a url", false},
	}
	for _, c := range cases {
		if got := p.Match(c.url); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	p := NewWechatMP("")
	r.Register(p)
	r.Register(p) // 同 id 去重
	if got := len(r.All()); got != 1 {
		t.Fatalf("重复注册未去重, len=%d", got)
	}
	if r.Match("https://mp.weixin.qq.com/s/abc") == nil {
		t.Error("应命中公众号插件")
	}
	if r.Match("https://example.com/a.mp4") != nil {
		t.Error("普通直链不应命中任何插件")
	}
	if r.ByID("wechat_mp") == nil || r.ByID("nope") != nil {
		t.Error("ByID 行为异常")
	}
	info := r.All()[0]
	if info.ID != "wechat_mp" || len(info.Patterns) == 0 {
		t.Errorf("插件自述信息异常: %+v", info)
	}
}

const mockArticle = `{
  "code": 1,
  "msg": "解析成功",
  "time": "1783434015",
  "runtime": 895.64,
  "data": {
    "title": "暑假适合孩子看的英文动画电影",
    "cover": "https://mmbiz.qpic.cn/mmbiz_jpg/cover/0?wx_fmt=jpeg",
    "author": "Aston",
    "description": "这些适合亲子共赏的英文动画电影",
    "publish_time": "1783164513",
    "content": "<p>第一段 <b>加粗</b></p><script>var x=1;</script><p>第二段&amp;结尾</p>",
    "images": [
      "https://mmbiz.qpic.cn/1/640?wx_fmt=jpeg",
      "javascript:alert(1)",
      "https://mmbiz.qpic.cn/3/640?wx_fmt=png"
    ],
    "wxv_fields": ["wxv_111", "wxv_222"],
    "movie_down_url": [
      "http://vid.losfer.cn/v/?wxv=wxv_111",
      "http://vid.losfer.cn/v/?wxv=wxv_222"
    ],
    "wxv_urls": [
      "https://mp.weixin.qq.com/mp/readtemplate?vid=wxv_111",
      "https://mp.weixin.qq.com/mp/readtemplate?vid=wxv_222"
    ],
    "voice_ids": [],
    "audio_down_urls": ["http://vid.losfer.cn/a/?id=1"],
    "source_url": "https://mp.weixin.qq.com/s/epF_Tih8U0wNUUazf0TrsA",
    "api_desc": "解析列表movie_down_url为当前文章只有视频才会有"
  }
}`

func mockServer(t *testing.T, body string, code int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("url"); !strings.Contains(got, "mp.weixin.qq.com/s/") {
			t.Errorf("请求缺少 url 参数: %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}))
}

func TestWechatParseSuccess(t *testing.T) {
	srv := mockServer(t, mockArticle, 200)
	defer srv.Close()
	p := NewWechatMP(srv.URL)
	res, err := p.Parse(context.Background(), "https://mp.weixin.qq.com/s/epF_Tih8U0wNUUazf0TrsA")
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	if res.Plugin != "wechat_mp" || res.Title != "暑假适合孩子看的英文动画电影" || res.Author != "Aston" {
		t.Errorf("文章元信息映射异常: %+v", res)
	}
	if res.PublishTime != "1783164513" {
		t.Errorf("时间戳应保留原值供前端按时区渲染, got %q", res.PublishTime)
	}
	if len(res.Items) != 2 {
		t.Fatalf("应有 2 个视频下载对象, got %d", len(res.Items))
	}
	it := res.Items[0]
	if it.Kind != "video" || it.Format != "mp4" || it.ID != "wxv_111" {
		t.Errorf("Item 字段异常: %+v", it)
	}
	if !strings.HasPrefix(it.URL, "http://vid.losfer.cn/v/") {
		t.Errorf("下载直链异常: %q", it.URL)
	}
	if it.Title != "暑假适合孩子看的英文动画电影 · 01" {
		t.Errorf("多视频应带序号避免重名, got %q", it.Title)
	}
	if it.Headers["Referer"] != "https://mp.weixin.qq.com/" {
		t.Errorf("缺少防盗链建议头: %+v", it.Headers)
	}
	if len(res.Images) != 2 {
		t.Errorf("javascript: 链接应被过滤, got %d", len(res.Images))
	}
	if res.ContentText == "" || strings.Contains(res.ContentText, "var x") || strings.Contains(res.ContentText, "<p>") {
		t.Errorf("正文清洗异常: %q", res.ContentText)
	}
	if !strings.Contains(res.ContentText, "第二段&结尾") {
		t.Errorf("正文应还原 HTML 实体: %q", res.ContentText)
	}
	if res.Extra["video_count"] != 2 || res.Extra["audio_count"] != 1 {
		t.Errorf("Extra 计数异常: %+v", res.Extra)
	}
	// 结果必须能序列化成前端收到的形状
	if _, err := json.Marshal(res); err != nil {
		t.Fatalf("Result 无法序列化: %v", err)
	}
}

func TestWechatParseSingleVideoNoSuffix(t *testing.T) {
	var doc map[string]any
	json.Unmarshal([]byte(mockArticle), &doc)
	d := doc["data"].(map[string]any)
	d["movie_down_url"] = []any{"http://vid.losfer.cn/v/?wxv=wxv_111"}
	body, _ := json.Marshal(doc)
	srv := mockServer(t, string(body), 200)
	defer srv.Close()
	res, err := NewWechatMP(srv.URL).Parse(context.Background(), "https://mp.weixin.qq.com/s/abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].Title != "暑假适合孩子看的英文动画电影" {
		t.Errorf("单个视频不应加序号: %+v", res.Items)
	}
}

func TestWechatParseErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		code int
		want string
	}{
		{"业务失败", `{"code":0,"msg":"链接已失效"}`, 200, "链接已失效"},
		{"非 JSON", `not json`, 200, "不是合法 JSON"},
		{"服务错误", `{}`, 502, "HTTP 502"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := mockServer(t, c.body, c.code)
			defer srv.Close()
			_, err := NewWechatMP(srv.URL).Parse(context.Background(), "https://mp.weixin.qq.com/s/abc")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("期望错误含 %q, got %v", c.want, err)
			}
		})
	}
}

func TestWechatParseEmptyArticle(t *testing.T) {
	// 示例文章当前返回 code=1 但 data 全空，插件应正常返回空列表而不是报错
	srv := mockServer(t, `{"code":1,"msg":"解析成功","data":{"source_url":"https://mp.weixin.qq.com/s/abc"}}`, 200)
	defer srv.Close()
	res, err := NewWechatMP(srv.URL).Parse(context.Background(), "https://mp.weixin.qq.com/s/abc")
	if err != nil {
		t.Fatalf("空文章不应报错: %v", err)
	}
	if len(res.Items) != 0 || res.Title != "" {
		t.Errorf("空文章应得到空结果: %+v", res)
	}
}

func TestWechatParseTimeoutMessage(t *testing.T) {
	// 外部解析服务偶发无响应时，界面上应看到中文提示而不是底层英文报错
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, `{"code":1}`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_, err := NewWechatMP(srv.URL).Parse(ctx, "https://mp.weixin.qq.com/s/abc")
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Errorf("超时应翻译成「解析服务响应超时，请稍后重试」, got %v", err)
	}
}

func TestFlexTypes(t *testing.T) {
	// 接口偶尔把数字写成字符串，兼容层不能让整个解析失败
	srv := mockServer(t, `{"code":"1","msg":"ok","data":{"publish_time":1783164513,"title":"T"}}`, 200)
	defer srv.Close()
	res, err := NewWechatMP(srv.URL).Parse(context.Background(), "https://mp.weixin.qq.com/s/abc")
	if err != nil {
		t.Fatalf("code 为字符串时应兼容: %v", err)
	}
	if res.PublishTime != "1783164513" {
		t.Errorf("数字型时间戳应转成字符串, got %q", res.PublishTime)
	}
}

func TestStripHTML(t *testing.T) {
	got := stripHTML(`<div>你好 <span>世界</span></div><style>.a{color:red}</style><p>&amp;</p>`, 0)
	if got != "你好 世界 &" {
		t.Errorf("清洗结果异常: %q", got)
	}
	if s := stripHTML(strings.Repeat("字", 700), 600); len([]rune(s)) != 601 {
		t.Errorf("截断应按 rune 且带省略号, got %d", len([]rune(s)))
	}
}

func TestDefaultRegistry(t *testing.T) {
	// 默认注册表必须包含公众号插件，且可被环境变量覆盖端点（只验证不 panic）
	d := Default()
	if len(d.All()) == 0 || d.Match("https://mp.weixin.qq.com/s/abc") == nil {
		t.Fatal("默认注册表缺少公众号插件")
	}
	if Default() != d {
		t.Error("Default() 应返回同一实例")
	}
}
