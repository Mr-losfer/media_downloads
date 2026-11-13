package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"mediasuper/internal/engine"
)

func testDedupeServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	db, err := engine.OpenStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := engine.Settings{
		DefaultDir: dir,
		SaveDirs:   []engine.SaveDir{{Path: dir, Label: "test"}},
	}
	st.Sanitize()
	return &Server{m: engine.NewManager(db, st, dir)}
}

func postTask(s *Server, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleAddTask(w, r)
	return w
}

func TestAddTaskDuplicateReturns409(t *testing.T) {
	s := testDedupeServer(t)

	w := postTask(s, `{"url":"https://cdn.example.com/v/a.mp4","title":"A"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("首次提交应返回 201, got %d: %s", w.Code, w.Body.String())
	}

	w = postTask(s, `{"url":"https://cdn.example.com/v/a.mp4","title":"B"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("重复链接应返回 409, got %d: %s", w.Code, w.Body.String())
	}
	var d map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d["duplicate"] != true || d["reason"] != "url" {
		t.Fatalf("响应应带 duplicate/reason 字段: %#v", d)
	}
	if id, _ := d["task_id"].(string); id == "" {
		t.Fatalf("响应应带命中的 task_id: %#v", d)
	}
	if msg, _ := d["error"].(string); !strings.Contains(msg, "拦截") {
		t.Fatalf("应返回中文拦截提示: %#v", d)
	}
}

func TestAddTaskOtherErrorsStill400(t *testing.T) {
	s := testDedupeServer(t)
	w := postTask(s, `{"url":""}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("普通参数错误仍应返回 400, got %d: %s", w.Code, w.Body.String())
	}
	var d map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	if d["duplicate"] != nil {
		t.Fatalf("普通错误不应带 duplicate 标识: %#v", d)
	}
}
