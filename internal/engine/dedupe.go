package engine

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// ErrDuplicate 是「重复下载被拦截」的哨兵错误，server 层据此返回 409 与结构化字段。
var ErrDuplicate = errors.New("重复下载")

// DuplicateError 说明是哪条规则拦下的、命中了哪条已有任务。
// Reason: "url"（链接防重，始终开启）| "title"（标题防重，由开关控制）。
type DuplicateError struct {
	Reason  string `json:"reason"`
	TaskID  string `json:"task_id"`
	Message string `json:"message"`
}

func (e *DuplicateError) Error() string { return e.Message }

// Is 让 errors.Is(err, engine.ErrDuplicate) 成立，方便调用方统一识别。
func (e *DuplicateError) Is(target error) bool { return target == ErrDuplicate }

// dedupeURLKey 把链接归一化，只用于「是否同一个链接」的比较，不会改写实际下载用的 URL。
// 处理：去掉 # 片段、统一协议与主机名大小写、去掉默认端口、去掉路径末尾斜杠。
// query 原样保留——很多直链的防盗链签名就在 query 里，去掉会把不同链接误判成同一个。
func dedupeURLKey(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return strings.ToLower(s)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	switch {
	case u.Scheme == "http" && strings.HasSuffix(u.Host, ":80"):
		u.Host = strings.TrimSuffix(u.Host, ":80")
	case u.Scheme == "https" && strings.HasSuffix(u.Host, ":443"):
		u.Host = strings.TrimSuffix(u.Host, ":443")
	}
	u.Fragment = ""
	u.RawFragment = ""
	if len(u.Path) > 1 && strings.HasSuffix(u.Path, "/") {
		u.Path = strings.TrimRight(u.Path, "/")
	}
	return u.String()
}

// dedupeTitleKey 归一化标题：大小写不敏感、忽略全部空白字符。
// 中文标题里手动加的空格属于排版差异，不应让同一部片被当成两部；
// 空标题返回空串，不参与标题防重判断。
func dedupeTitleKey(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// dedupeBlocks 判断某状态是否参与去重。
// 失败 / 已取消的任务不拦，用户可以直接重新粘贴链接或用「重试」重下。
func dedupeBlocks(s string) bool {
	switch s {
	case StatusQueued, StatusRunning, StatusMerging, StatusCompleted:
		return true
	default:
		return false
	}
}

func statusText(s string) string {
	switch s {
	case StatusQueued:
		return "排队中"
	case StatusRunning, StatusMerging:
		return "下载中"
	case StatusCompleted:
		return "已完成"
	case StatusFailed:
		return "失败"
	case StatusCanceled:
		return "已取消"
	default:
		return s
	}
}

func shortTitle(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// findDuplicate 在已有任务里找重复项。调用方必须持有 m.mu。
// 链接防重始终生效；标题防重仅在设置开关开启、且标题非空时生效。
func (m *Manager) findDuplicate(rawURL, title string) *DuplicateError {
	key := dedupeURLKey(rawURL)
	for _, t := range m.tasks {
		if !dedupeBlocks(t.Status) {
			continue
		}
		if t.URL == rawURL || dedupeURLKey(t.URL) == key {
			return &DuplicateError{
				Reason: "url",
				TaskID: t.ID,
				Message: fmt.Sprintf(
					"该链接已存在任务（%s），已拦截重复下载；如需重新下载，请先到任务页删除该任务记录",
					statusText(t.Status)),
			}
		}
	}

	if !m.st.DedupeTitle {
		return nil
	}
	tk := dedupeTitleKey(title)
	if tk == "" {
		return nil
	}
	for _, t := range m.tasks {
		if !dedupeBlocks(t.Status) {
			continue
		}
		if dedupeTitleKey(t.Title) == tk {
			return &DuplicateError{
				Reason: "title",
				TaskID: t.ID,
				Message: fmt.Sprintf(
					"标题「%s」已存在任务（%s），已拦截重复下载（标题防重已开启）；如需重新下载，请先删除该任务记录或关闭标题防重",
					shortTitle(t.Title, 30), statusText(t.Status)),
			}
		}
	}
	return nil
}
