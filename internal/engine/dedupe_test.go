package engine

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestDedupeURLKey(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		same bool
	}{
		{"主机名大小写", "https://Example.COM/v/a.mp4", "https://example.com/v/a.mp4", true},
		{"https 默认端口", "https://Example.COM:443/v/a.mp4", "https://example.com/v/a.mp4", true},
		{"http 默认端口", "http://example.com:80/v/a.mp4", "http://example.com/v/a.mp4", true},
		{"片段", "https://example.com/v/a.mp4#part", "https://example.com/v/a.mp4", true},
		{"末尾斜杠", "https://example.com/v/", "https://example.com/v", true},
		{"首尾空白", "  https://example.com/v/a.mp4  ", "https://example.com/v/a.mp4", true},
		{"query 不同不算同一链接", "https://example.com/v/a.mp4?token=1", "https://example.com/v/a.mp4?token=2", false},
		{"协议不同不算同一链接", "http://example.com/v/a.mp4", "https://example.com/v/a.mp4", false},
		{"路径不同", "https://example.com/a/x.mp4", "https://example.com/b/x.mp4", false},
		{"非默认端口保留", "https://example.com:8443/v/a.mp4", "https://example.com/v/a.mp4", false},
		{"畸形链接不 panic", "not-a-url", "NOT-A-URL", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := dedupeURLKey(c.a) == dedupeURLKey(c.b)
			if got != c.same {
				t.Fatalf("dedupeURLKey(%q)=%q, dedupeURLKey(%q)=%q, same=%v, 期望 %v",
					c.a, dedupeURLKey(c.a), c.b, dedupeURLKey(c.b), got, c.same)
			}
		})
	}
}

func TestDedupeTitleKey(t *testing.T) {
	cases := []struct {
		a, b string
		same bool
	}{
		{"  视频  标题  ", "视频 标题", true},
		{"Hello World", "hello   world", true},
		{"", "   ", true},
		{"视频A", "视频B", false},
		{"abc", "abcd", false},
	}
	for _, c := range cases {
		if got := dedupeTitleKey(c.a) == dedupeTitleKey(c.b); got != c.same {
			t.Fatalf("dedupeTitleKey(%q)=%q vs %q=%q, same=%v 期望 %v",
				c.a, dedupeTitleKey(c.a), c.b, dedupeTitleKey(c.b), got, c.same)
		}
	}
	if dedupeTitleKey("  ") != "" {
		t.Fatal("空标题归一化后应为空串（不参与标题防重）")
	}
}

func testManager(tasks ...*Task) *Manager {
	m := &Manager{tasks: map[string]*Task{}, st: Settings{}}
	for _, tk := range tasks {
		m.tasks[tk.ID] = tk
	}
	return m
}

func TestFindDuplicateURL(t *testing.T) {
	m := testManager(&Task{ID: "t1", URL: "https://example.com/v/a.mp4", Title: "旧片", Status: StatusCompleted})
	// 归一化后相同也应拦下
	dup := m.findDuplicate("https://EXAMPLE.com:443/v/a.mp4#frag", "新片")
	if dup == nil || dup.Reason != "url" || dup.TaskID != "t1" {
		t.Fatalf("链接重复应被拦截: %+v", dup)
	}
	if !errors.Is(dup, ErrDuplicate) {
		t.Fatal("DuplicateError 应匹配 ErrDuplicate 哨兵")
	}
	if !strings.Contains(dup.Message, "已完成") || !strings.Contains(dup.Message, "拦截") {
		t.Fatalf("错误提示应说明状态与拦截行为: %q", dup.Message)
	}
	// 不同链接不拦
	if got := m.findDuplicate("https://example.com/v/b.mp4", "新片"); got != nil {
		t.Fatalf("不同链接不应拦截: %+v", got)
	}
}

func TestFindDuplicateStatusScope(t *testing.T) {
	for _, st := range []string{StatusQueued, StatusRunning, StatusMerging, StatusCompleted} {
		m := testManager(&Task{ID: "t1", URL: "https://example.com/v/a.mp4", Status: st})
		if got := m.findDuplicate("https://example.com/v/a.mp4", ""); got == nil {
			t.Fatalf("%s 状态应参与去重", st)
		}
	}
	for _, st := range []string{StatusFailed, StatusCanceled} {
		m := testManager(&Task{ID: "t1", URL: "https://example.com/v/a.mp4", Status: st})
		if got := m.findDuplicate("https://example.com/v/a.mp4", ""); got != nil {
			t.Fatalf("%s 状态不应拦截（允许重新提交）: %+v", st, got)
		}
	}
}

func TestFindDuplicateTitleSwitch(t *testing.T) {
	base := &Task{ID: "t1", URL: "https://example.com/v/a.mp4", Title: "同一个标题", Status: StatusCompleted}
	url := "https://example.com/v/other.mp4"

	// 默认关闭：同标题不拦
	m := testManager(base)
	if got := m.findDuplicate(url, "同一个标题"); got != nil {
		t.Fatalf("标题防重默认关闭，不应拦截: %+v", got)
	}

	// 开启后：同标题（含大小写/空白差异）拦下
	m.st.DedupeTitle = true
	dup := m.findDuplicate(url, "  同一个  标题  ")
	if dup == nil || dup.Reason != "title" || dup.TaskID != "t1" {
		t.Fatalf("开启标题防重后应拦截: %+v", dup)
	}
	if !strings.Contains(dup.Message, "标题防重已开启") {
		t.Fatalf("提示应说明是标题防重: %q", dup.Message)
	}

	// 空标题不参与
	if got := m.findDuplicate("https://example.com/v/third.mp4", "   "); got != nil {
		t.Fatalf("空标题不应参与标题防重: %+v", got)
	}
	// 不同标题不拦
	if got := m.findDuplicate("https://example.com/v/third.mp4", "另一个标题"); got != nil {
		t.Fatalf("不同标题不应拦截: %+v", got)
	}
}

func testAddManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	db, err := OpenStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := defaultSettings()
	st.DefaultDir = dir
	st.SaveDirs = []SaveDir{{Path: dir, Label: "test"}}
	st.Sanitize()
	return NewManager(db, st, dir)
}

func TestAddRejectsDuplicateLink(t *testing.T) {
	m := testAddManager(t)
	in := TaskInput{URL: "https://cdn.example.com/v/movie.mp4", Title: "电影"}
	first, err := m.Add(in)
	if err != nil {
		t.Fatalf("首次提交应成功: %v", err)
	}
	if first == nil || first.ID == "" {
		t.Fatal("首次提交应返回任务")
	}
	// 同一链接换个大小写/片段再提交 → 直接拦截
	_, err = m.Add(TaskInput{URL: "https://CDN.example.com:443/v/movie.mp4#part", Title: "换个标题也没用"})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("重复链接应被 ErrDuplicate 拦截, got: %v", err)
	}
	var dup *DuplicateError
	if !errors.As(err, &dup) || dup.Reason != "url" || dup.TaskID != first.ID {
		t.Fatalf("应返回 url 规则的 DuplicateError: %+v", err)
	}
	// 链接不同 → 正常创建
	if _, err := m.Add(TaskInput{URL: "https://cdn.example.com/v/movie2.mp4", Title: "电影"}); err != nil {
		t.Fatalf("不同链接应可提交: %v", err)
	}
}

func TestAddTitleSwitchEndToEnd(t *testing.T) {
	m := testAddManager(t)
	if _, err := m.Add(TaskInput{URL: "https://cdn.example.com/v/a.mp4", Title: "同名"}); err != nil {
		t.Fatal(err)
	}
	// 开关关闭：同标题不同链接可以提交
	if _, err := m.Add(TaskInput{URL: "https://cdn.example.com/v/b.mp4", Title: "同名"}); err != nil {
		t.Fatalf("标题防重关闭时不应拦截: %v", err)
	}

	st := m.Settings()
	st.DedupeTitle = true
	if _, err := m.UpdateSettings(st); err != nil {
		t.Fatal(err)
	}
	_, err := m.Add(TaskInput{URL: "https://cdn.example.com/v/c.mp4", Title: "同  名"})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("开启后同标题应拦截: %v", err)
	}
	var dup *DuplicateError
	if !errors.As(err, &dup) || dup.Reason != "title" {
		t.Fatalf("应返回 title 规则: %+v", err)
	}
	// 失败任务不拦
	for _, tk := range m.tasks {
		tk.Status = StatusFailed
	}
	if _, err := m.Add(TaskInput{URL: "https://cdn.example.com/v/d.mp4", Title: "同名"}); err != nil {
		t.Fatalf("失败任务不应参与标题防重: %v", err)
	}
}
