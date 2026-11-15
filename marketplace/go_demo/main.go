// go_demo —— Media Super Go 插件示例（SDK 协议 v1）
//
// 本文件只负责「协议」，业务逻辑在 parse.go。真实插件可以拆成任意多个
// .go 文件，全部放在 package main 下即可，编译时会自动一起编译。
//
//	编译：go build -o bin/go_demo .
//	协议：stdin 一行 JSON 请求；stdout 最后一行输出一行 JSON 响应；
//	     stderr 可任意打日志（调试模式会回传给界面）。
//
// 只用标准库，因此不需要依赖主程序的 Go 版本，也不需要联网拉包。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Request 是主程序通过 stdin 传进来的入参。
type Request struct {
	URL    string         `json:"url"`
	Config map[string]any `json:"config"`
	Mode   string         `json:"mode"` // parse | debug
}

// Item 是一条可下载对象。
type Item struct {
	ID      string            `json:"id"`
	URL     string            `json:"url"`
	Title   string            `json:"title"`
	Cover   string            `json:"cover,omitempty"`
	Kind    string            `json:"kind"`
	Format  string            `json:"format,omitempty"`
	Page    string            `json:"page,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Result 是所有插件共用的解析结果形状，前端只认这一种结构。
type Result struct {
	Plugin      string         `json:"plugin"`
	PluginName  string         `json:"plugin_name"`
	SourceURL   string         `json:"source_url"`
	Title       string         `json:"title"`
	Cover       string         `json:"cover"`
	Author      string         `json:"author"`
	Description string         `json:"description"`
	PublishTime string         `json:"publish_time"`
	ContentText string         `json:"content_text"`
	ContentLen  int            `json:"content_len"`
	Images      []string       `json:"images"`
	Items       []Item         `json:"items"`
	Extra       map[string]any `json:"extra,omitempty"`
}

// Response 是 stdout 最后一行的输出。
type Response struct {
	OK     bool     `json:"ok"`
	Result *Result  `json:"result,omitempty"`
	Error  string   `json:"error,omitempty"`
	Logs   []string `json:"logs,omitempty"`
}

func emit(r Response) {
	b, _ := json.Marshal(r)
	fmt.Println(string(b))
}

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
}

func main() {
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		emit(Response{OK: false, Error: "读取 stdin 失败: " + err.Error()})
		return
	}
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		emit(Response{OK: false, Error: "请求不是合法 JSON: " + err.Error()})
		return
	}
	res, logs, err := parse(req)
	if err != nil {
		emit(Response{OK: false, Error: err.Error(), Logs: logs})
		return
	}
	emit(Response{OK: true, Result: res, Logs: logs})
}
