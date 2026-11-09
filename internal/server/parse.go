package server

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"mediasuper/internal/parser"
)

// reParseURL 用于把底层错误里的一长串 URL 收敛成「解析服务」，
// 与更新检查的处理一致：不把代理/接口域名甩给界面上的用户。
var reParseURL = regexp.MustCompile(`https?://[^\s"'，,；;)]+`)

func cleanParseError(msg string) string {
	s := reParseURL.ReplaceAllString(msg, "解析服务")
	return strings.Join(strings.Fields(s), " ")
}

// GET /api/parsers —— 列出已注册的解析插件，前端/文档页可据此展示支持范围。
func (s *Server) handleListParsers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"plugins": parser.Default().All()})
}

// POST /api/parse —— 统一的插件解析入口。
//
// 未命中插件时返回 matched=false，前端照旧把链接当直链提交；命中时调用插件
// 解析并返回统一 Result，前端展示结果、把勾选的 items 作为普通下载任务提交。
// 新增插件不需要改这里的分支逻辑。
func (s *Server) handleParse(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL string `json:"url"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	raw := strings.TrimSpace(in.URL)
	if raw == "" {
		writeErr(w, 400, "URL 不能为空")
		return
	}

	p := parser.Default().Match(raw)
	if p == nil {
		writeJSON(w, 200, map[string]any{"matched": false, "url": raw})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := p.Parse(ctx, raw)
	if err != nil {
		writeErr(w, 502, cleanParseError(fmt.Sprintf("%s解析失败：%v", p.Name(), err)))
		return
	}
	writeJSON(w, 200, map[string]any{
		"matched":     true,
		"plugin":      p.ID(),
		"plugin_name": p.Name(),
		"result":      res,
	})
}
