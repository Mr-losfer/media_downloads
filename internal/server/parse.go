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

// parserInfo 是 GET /api/parsers 的统一输出（兼容旧前端与文档）。
type parserInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Patterns    []string `json:"patterns"`
	Engine      string   `json:"engine,omitempty"`
	Enabled     bool     `json:"enabled"`
}

// GET /api/parsers —— 列出所有已安装且启用的解析插件。
// 有插件系统时以插件列表为准；没有（比如单测里手工构造的 Server）回退内置注册表。
func (s *Server) handleListParsers(w http.ResponseWriter, r *http.Request) {
	list := []parserInfo{}
	if s.pm != nil {
		for _, v := range s.pm.List() {
			if !v.Enabled || !v.Capabilities.Parse {
				continue
			}
			patterns := append([]string{}, v.Match.URLPatterns...)
			for _, d := range v.Match.Domains {
				patterns = append(patterns, "https://"+d+"/*")
			}
			list = append(list, parserInfo{
				ID: v.ID, Name: v.Name, Description: v.Description,
				Patterns: patterns, Engine: v.Engine, Enabled: true,
			})
		}
	} else {
		for _, p := range parser.Default().All() {
			list = append(list, parserInfo{
				ID: p.ID, Name: p.Name, Description: p.Description,
				Patterns: p.Patterns, Engine: "builtin", Enabled: true,
			})
		}
	}
	writeJSON(w, 200, map[string]any{"plugins": list})
}

// POST /api/parse —— 统一的插件解析入口。
//
// 首页提交链接时，服务端按「启用 + 域名/路径/正则命中」挑一个插件接管；
// 未命中返回 matched=false，前端照旧把链接当直链提交。命中时返回统一
// Result，前端展示结果、把勾选的 items 作为普通下载任务提交。
// 新增插件不需要改这里的分支逻辑。
func (s *Server) handleParse(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL   string `json:"url"`
		Debug bool   `json:"debug"`
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

	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()

	if s.pm != nil {
		res, tr, id, err := s.pm.ParseURL(ctx, raw, in.Debug)
		if err != nil {
			writeJSON(w, 502, map[string]any{
				"matched": true, "plugin": id,
				"error": cleanParseError(fmt.Sprintf("解析失败：%v", err)),
				"trace": tr,
			})
			return
		}
		if res == nil {
			writeJSON(w, 200, map[string]any{"matched": false, "url": raw})
			return
		}
		out := map[string]any{
			"matched":     true,
			"plugin":      id,
			"plugin_name": res.PluginName,
			"result":      res,
		}
		if in.Debug {
			out["trace"] = tr
		}
		writeJSON(w, 200, out)
		return
	}

	// 兼容兜底：插件系统未初始化时走旧的进程内注册表
	p := parser.Default().Match(raw)
	if p == nil {
		writeJSON(w, 200, map[string]any{"matched": false, "url": raw})
		return
	}
	res, err := p.Parse(ctx, raw)
	if err != nil {
		writeErr(w, 502, cleanParseError(fmt.Sprintf("%s解析失败：%v", p.Name(), err)))
		return
	}
	writeJSON(w, 200, map[string]any{
		"matched": true, "plugin": p.ID(), "plugin_name": p.Name(), "result": res,
	})
}
