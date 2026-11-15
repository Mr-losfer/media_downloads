package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// cfgString 读一个 text / password / url 配置项。
func cfgString(c map[string]any, key, def string) string {
	if v, ok := c[key]; ok && v != nil {
		if s := strings.TrimSpace(fmt.Sprint(v)); s != "" {
			return s
		}
	}
	return def
}

func cfgInt(c map[string]any, key string, def int) int {
	if v, ok := c[key]; ok && v != nil {
		if n, err := strconv.Atoi(fmt.Sprint(v)); err == nil {
			return n
		}
		if f, ok := v.(float64); ok {
			return int(f)
		}
	}
	return def
}

func cfgBool(c map[string]any, key string, def bool) bool {
	if v, ok := c[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

// parse 是插件的业务入口。真实插件把这里的逻辑换成抓页面 / 调接口 / 解析签名都行。
//
// 本示例有两种模式：
//   - 配置了 api_base：GET 外部 JSON 接口，把 data.list 展开成下载对象；
//   - 未配置（默认）：演示模式，直接把提交的链接包装成一条对象，零依赖即可跑通测试。
func parse(req Request) (*Result, []string, error) {
	logs := []string{}
	verbose := cfgBool(req.Config, "verbose", true)
	prefix := cfgString(req.Config, "prefix", "[Go插件]")
	maxItems := cfgInt(req.Config, "max_items", 10)
	if maxItems < 1 || maxItems > 100 {
		maxItems = 10
	}
	if verbose {
		logf("开始解析 url=%s mode=%s prefix=%s max_items=%d", req.URL, req.Mode, prefix, maxItems)
	}

	res := &Result{
		Plugin:     "go_demo",
		PluginName: "Go 插件开发示例",
		SourceURL:  req.URL,
		Images:     []string{},
		Items:      []Item{},
		Extra:      map[string]any{"engine": "go", "mode": req.Mode},
	}

	api := cfgString(req.Config, "api_base", "")
	if api == "" {
		// 演示模式：不访问网络，便于在没有外部服务时验证整个插件链路
		res.Title = prefix + " 演示解析结果"
		res.Description = "未配置 api_base，使用演示模式。可在配置弹窗里填自己的接口地址。"
		res.Items = append(res.Items, Item{
			ID: "demo-1", URL: req.URL, Title: prefix + " 视频 01",
			Kind: "video", Format: "mp4", Page: req.URL,
			Headers: map[string]string{"User-Agent": "MediaSuper-GoPlugin/1.0"},
		})
		logs = append(logs, "演示模式：返回 1 条包装对象")
		return res, logs, nil
	}
	if !strings.HasPrefix(api, "http://") && !strings.HasPrefix(api, "https://") {
		return nil, logs, fmt.Errorf("配置项 api_base 不是合法的 http(s) 地址")
	}

	u, err := url.Parse(api)
	if err != nil {
		return nil, logs, fmt.Errorf("api_base 解析失败：%w", err)
	}
	q := u.Query()
	q.Set("url", req.URL)
	u.RawQuery = q.Encode()
	if verbose {
		logf("GET %s", u.String())
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(u.String())
	if err != nil {
		return nil, logs, fmt.Errorf("请求解析服务失败：%w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, logs, err
	}
	if verbose {
		logf("HTTP %d, %d bytes", resp.StatusCode, len(body))
	}
	logs = append(logs, fmt.Sprintf("HTTP %d, %d bytes", resp.StatusCode, len(body)))

	var data struct {
		Title string `json:"title"`
		List  []struct {
			ID    string `json:"id"`
			URL   string `json:"url"`
			Title string `json:"title"`
			Cover string `json:"cover"`
		} `json:"list"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, logs, fmt.Errorf("解析结果不是合法 JSON：%w", err)
	}
	res.Title = prefix + " " + data.Title
	for i, v := range data.List {
		if len(res.Items) >= maxItems {
			break
		}
		if !strings.HasPrefix(v.URL, "http") {
			continue
		}
		name := v.Title
		if name == "" {
			name = fmt.Sprintf("%s 视频 %02d", data.Title, i+1)
		}
		res.Items = append(res.Items, Item{
			ID: v.ID, URL: v.URL, Title: prefix + " " + name, Cover: v.Cover,
			Kind: "video", Format: "mp4", Page: req.URL,
		})
	}
	logs = append(logs, fmt.Sprintf("展开 %d 条下载对象", len(res.Items)))
	return res, logs, nil
}
