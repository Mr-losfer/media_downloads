package plugin

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

func utf8Valid(b []byte) bool { return utf8.Valid(b) }

func encodeBase64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func decodeBase64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// ---------- 模板渲染 ----------
//
// 可用变量：
//   {{config.<key>}}  用户在配置弹窗里保存的值
//   {{source_url}}    本次提交的原始链接
//   {{url}}           同上（简写）
//   {{title}}         已映射出的标题
//   {{value}}         数组元素本身（items 展开时）
//   {{item.<path>}}   数组元素是对象时的字段
//   {{index}}         从 0 开始的下标
//   {{index1}}        从 1 开始的下标
// 变量缺失时替换为空串，不报错（插件作者容易写错，别让整次解析崩掉）。

var reTpl = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.\-]+)\s*\}\}`)

type templateContext struct {
	Config    map[string]any
	SourceURL string
	Title     string
	Value     any
	Index     int
	Extra     map[string]string
}

func renderTpl(s string, c templateContext) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	return reTpl.ReplaceAllStringFunc(s, func(raw string) string {
		key := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "{{"), "}}"))
		if c.Extra != nil {
			if v, ok := c.Extra[key]; ok {
				return v
			}
		}
		switch key {
		case "source_url", "url":
			return c.SourceURL
		case "title":
			return c.Title
		case "value":
			return stringify(c.Value)
		case "index":
			return strconv.Itoa(c.Index)
		case "index1":
			return strconv.Itoa(c.Index + 1)
		}
		if strings.HasPrefix(key, "config.") {
			if v, ok := c.Config[strings.TrimPrefix(key, "config.")]; ok {
				return stringify(v)
			}
			return ""
		}
		if strings.HasPrefix(key, "item.") {
			return stringify(getPath(c.Value, strings.TrimPrefix(key, "item.")))
		}
		return ""
	})
}

func stringify(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case json.Number:
		return string(x)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// ---------- 点路径取值 ----------
//
// 支持 data.title / data.items[0].url / movie_down_url.0 这类写法。
var reIndex = regexp.MustCompile(`^(.+?)\[(\d+)\]$`)

func getPath(root any, path string) any {
	path = strings.TrimSpace(path)
	if path == "" || path == "$" {
		return root
	}
	var cur any = root
	for _, seg := range strings.Split(path, ".") {
		if seg == "" {
			continue
		}
		idx := -1
		if m := reIndex.FindStringSubmatch(seg); m != nil {
			seg = m[1]
			idx, _ = strconv.Atoi(m[2])
		}
		if seg != "" {
			switch m := cur.(type) {
			case map[string]any:
				cur = m[seg]
			default:
				return nil
			}
		}
		if idx >= 0 {
			switch a := cur.(type) {
			case []any:
				if idx < len(a) {
					cur = a[idx]
				} else {
					return nil
				}
			default:
				return nil
			}
		}
	}
	return cur
}

func asStringList(v any) []string {
	out := []string{}
	switch a := v.(type) {
	case []any:
		for _, x := range a {
			s := strings.TrimSpace(stringify(x))
			if s != "" {
				out = append(out, s)
			}
		}
	case string:
		if strings.TrimSpace(a) != "" {
			out = append(out, strings.TrimSpace(a))
		}
	}
	return out
}

func isHTTP(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func safeHTTP(s string) string {
	if isHTTP(s) {
		return strings.TrimSpace(s)
	}
	return ""
}

var (
	reStripTag   = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>|<[^>]*>`)
	reStripSpace = regexp.MustCompile(`\s+`)
)

func stripHTML(s string, limit int) string {
	if s == "" {
		return ""
	}
	s = reStripTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.TrimSpace(reStripSpace.ReplaceAllString(s, " "))
	r := []rune(s)
	if limit > 0 && len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return s
}

// ---------- 差量修改（patch） ----------

// PatchOp 是一次差量修改，供 MCP plugin_patch 与 REST /patch 使用。
// 支持三种模式：
//
//	replace —— 把 find 首次/全部出现替换成 replace（find 为空表示整文件覆盖）
//	insert  —— 在 anchor 之后插入 content（anchor 为空表示追加到末尾）
//	delete  —— 删除首次/全部出现的 find
//
// Mode=all 时替换全部匹配，默认只改第一处。
type PatchOp struct {
	Path    string `json:"path"`
	Action  string `json:"action"` // replace | insert | delete
	Find    string `json:"find,omitempty"`
	Replace string `json:"replace,omitempty"`
	Content string `json:"content,omitempty"`
	Anchor  string `json:"anchor,omitempty"`
	All     bool   `json:"all,omitempty"`
}

type PatchResult struct {
	Path    string `json:"path"`
	OK      bool   `json:"ok"`
	Changed int    `json:"changed"`
	Message string `json:"message,omitempty"`
}

// ApplyPatches 对插件目录里的文件做差量修改，全部成功才一次性落盘。
func ApplyPatches(i *Installed, ops []PatchOp) ([]PatchResult, error) {
	out := make([]PatchResult, 0, len(ops))
	// 先把所有文件读进缓存，避免多个 op 改同一文件时互相覆盖
	cache := map[string]string{}
	for _, op := range ops {
		p := strings.TrimSpace(op.Path)
		if p == "" {
			out = append(out, PatchResult{Path: p, Message: "path 不能为空"})
			continue
		}
		if _, ok := cache[p]; !ok {
			b, err := i.ReadFile(p)
			if err != nil {
				out = append(out, PatchResult{Path: p, Message: err.Error()})
				continue
			}
			cache[p] = string(b)
		}
		s := cache[p]
		act := strings.ToLower(strings.TrimSpace(op.Action))
		if act == "" {
			act = "replace"
		}
		res := PatchResult{Path: p}
		switch act {
		case "replace":
			if op.Find == "" {
				cache[p] = op.Replace
				res.Changed = 1
			} else {
				if !strings.Contains(s, op.Find) {
					res.Message = "未找到要替换的内容"
					break
				}
				if op.All {
					res.Changed = strings.Count(s, op.Find)
					cache[p] = strings.ReplaceAll(s, op.Find, op.Replace)
				} else {
					res.Changed = 1
					cache[p] = strings.Replace(s, op.Find, op.Replace, 1)
				}
			}
		case "insert":
			if op.Anchor == "" {
				cache[p] = s + op.Content
				res.Changed = 1
			} else if !strings.Contains(s, op.Anchor) {
				res.Message = "未找到锚点"
			} else {
				cache[p] = strings.Replace(s, op.Anchor, op.Anchor+op.Content, 1)
				res.Changed = 1
			}
		case "delete":
			if op.Find == "" {
				res.Message = "delete 需要 find"
				break
			}
			if !strings.Contains(s, op.Find) {
				res.Message = "未找到要删除的内容"
				break
			}
			if op.All {
				res.Changed = strings.Count(s, op.Find)
				cache[p] = strings.ReplaceAll(s, op.Find, "")
			} else {
				res.Changed = 1
				cache[p] = strings.Replace(s, op.Find, "", 1)
			}
		default:
			res.Message = fmt.Sprintf("不支持的 action %q", act)
		}
		res.OK = res.Message == ""
		out = append(out, res)
	}
	// 全部成功后一次性落盘，避免半途失败留下半套修改
	for _, r := range out {
		if !r.OK {
			return out, fmt.Errorf("%s: %s", r.Path, r.Message)
		}
	}
	for p, s := range cache {
		if err := i.WriteFile(p, []byte(s)); err != nil {
			return out, err
		}
	}
	return out, nil
}
