package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mediasuper/internal/engine"
	"mediasuper/internal/updater"
	"mediasuper/internal/version"
)

// Server 承载所有 HTTP 路由。
type Server struct {
	m    *engine.Manager
	auth *Auth
	web  map[string]staticFile
	up   *updater.Manager

	startedAt time.Time

	lgMu  sync.Mutex
	lgMap map[string][]int64
}

func NewServer(m *engine.Manager, auth *Auth, web map[string]staticFile, up *updater.Manager) *Server {
	return &Server{
		m:         m,
		auth:      auth,
		web:       web,
		up:        up,
		startedAt: time.Now(),
		lgMap:     make(map[string][]int64),
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if v != nil {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v)
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	defer io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(v)
}

func (s *Server) needAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.authorized(r) {
			writeErr(w, http.StatusUnauthorized, "未授权：请先登录或提供 API Key")
			return
		}
		next(w, r)
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "version": version.Version, "time": time.Now().UnixMilli()})
	})

	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		s.auth.clearCookie(w)
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/me", s.needAuth(s.handleMe))

	mux.HandleFunc("GET /api/tasks", s.needAuth(s.handleListTasks))
	mux.HandleFunc("POST /api/tasks", s.needAuth(s.handleAddTask))
	mux.HandleFunc("GET /api/tasks/{id}", s.needAuth(s.handleGetTask))
	mux.HandleFunc("PATCH /api/tasks/{id}", s.needAuth(s.handlePatchTask))
	mux.HandleFunc("DELETE /api/tasks/{id}", s.needAuth(s.handleDeleteTask))
	mux.HandleFunc("POST /api/tasks/{id}/cancel", s.needAuth(s.handleCancelTask))
	mux.HandleFunc("POST /api/tasks/{id}/retry", s.needAuth(s.handleRetryTask))
	mux.HandleFunc("GET /api/tasks/{id}/stream", s.needAuth(s.handleStream))
	mux.HandleFunc("GET /api/tasks/{id}/thumb", s.needAuth(s.handleThumb))
	mux.HandleFunc("GET /api/tasks/{id}/download", s.needAuth(s.handleDownload))

	mux.HandleFunc("GET /api/events", s.needAuth(s.handleEvents))
	mux.HandleFunc("GET /api/stats", s.needAuth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.m.Stats())
	}))
	mux.HandleFunc("GET /api/settings", s.needAuth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.m.Settings())
	}))
	mux.HandleFunc("PUT /api/settings", s.needAuth(s.handleUpdateSettings))
	mux.HandleFunc("GET /api/browse", s.needAuth(s.handleBrowse))

	// 系统：版本信息 / 更新检测 / 一键更新 / 改管理员密码
	mux.HandleFunc("GET /api/system/info", s.needAuth(s.handleSystemInfo))
	mux.HandleFunc("GET /api/system/update", s.needAuth(s.handleCheckUpdate))
	mux.HandleFunc("POST /api/system/update", s.needAuth(s.handleApplyUpdate))
	mux.HandleFunc("POST /api/system/password", s.needAuth(s.handleChangePassword))

	mux.HandleFunc("POST /mcp", s.handleMCP)
	mux.HandleFunc("POST /api/mcp", s.handleMCP)
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "POST")
		writeErr(w, http.StatusMethodNotAllowed, "MCP 端点只接受 POST (JSON-RPC 2.0)")
	})

	mux.HandleFunc("/", s.handleStatic)
	return mux
}

// ---------- 登录 ----------

func (s *Server) loginBlocked(ip string) bool {
	now := time.Now().Unix()
	s.lgMu.Lock()
	defer s.lgMu.Unlock()
	arr := s.lgMap[ip]
	keep := arr[:0]
	for _, t := range arr {
		if now-t < 300 {
			keep = append(keep, t)
		}
	}
	s.lgMap[ip] = keep
	return len(keep) >= 10
}

func (s *Server) loginFailed(ip string) {
	s.lgMu.Lock()
	s.lgMap[ip] = append(s.lgMap[ip], time.Now().Unix())
	s.lgMu.Unlock()
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.loginBlocked(ip) {
		writeErr(w, http.StatusTooManyRequests, "尝试次数过多，请 5 分钟后再试")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	if !s.auth.CheckPassword(in.Password) {
		s.loginFailed(ip)
		time.Sleep(400 * time.Millisecond)
		writeErr(w, http.StatusUnauthorized, "密码错误")
		return
	}
	s.auth.setCookie(w, s.auth.issueSession())
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	st := s.m.Settings()
	writeJSON(w, 200, map[string]any{
		"authenticated": true,
		"version":       version.Version,
		"api_key":       st.APIKey,
		"default_dir":   st.DefaultDir,
		"save_dirs":     st.SaveDirs,
		"max_tasks":     st.MaxTasks,
		"seg_conns":     st.SegConns,
	})
}

// ---------- 任务 ----------

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	tasks, total := s.m.List(engine.ListFilter{
		Status: q.Get("status"),
		Query:  q.Get("q"),
		Limit:  limit,
		Offset: offset,
	})
	writeJSON(w, 200, map[string]any{"tasks": tasks, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) handleAddTask(w http.ResponseWriter, r *http.Request) {
	var in engine.TaskInput
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	t, err := s.m.Add(in)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, t)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "任务不存在")
		return
	}
	writeJSON(w, 200, t)
}

func (s *Server) handlePatchTask(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title *string `json:"title"`
		URL   *string `json:"url"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	if in.Title == nil {
		writeErr(w, 400, "目前只支持修改 title")
		return
	}
	t, err := s.m.Rename(r.PathValue("id"), *in.Title)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, t)
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	withFile := r.URL.Query().Get("file") == "true" || r.URL.Query().Get("file") == "1"
	if err := s.m.Delete(r.PathValue("id"), withFile); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	if err := s.m.Cancel(r.PathValue("id")); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleRetryTask(w http.ResponseWriter, r *http.Request) {
	t, err := s.m.Retry(r.PathValue("id"))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, t)
}

// ---------- 文件服务（支持 Range，浏览器可拖动进度条）----------

func (s *Server) serveTaskFile(w http.ResponseWriter, r *http.Request, download bool) {
	t, ok := s.m.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "任务不存在")
		return
	}
	if t.Status != engine.StatusCompleted || t.FileName == "" {
		writeErr(w, 409, "文件尚未下载完成")
		return
	}
	p := t.AbsPath()
	f, err := os.Open(p)
	if err != nil {
		writeErr(w, 404, "文件不存在（可能已被移动或删除）")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeErr(w, 500, "读取文件失败")
		return
	}
	if ct := mimeByExt(t.FileName); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	disposition := "inline"
	if download {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename*=UTF-8''%s", disposition, url.PathEscape(t.FileName)))
	http.ServeContent(w, r, t.FileName, fi.ModTime(), f)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request)   { s.serveTaskFile(w, r, false) }
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) { s.serveTaskFile(w, r, true) }

func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "任务不存在")
		return
	}
	p := t.AbsPath() + ".jpg"
	f, err := os.Open(p)
	if err != nil {
		if !t.HasThumb && t.Status == engine.StatusCompleted {
			s.m.EnsureThumb(t.ID)
		}
		writeErr(w, 404, "暂无缩略图")
		return
	}
	defer f.Close()
	fi, _ := f.Stat()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if fi != nil {
		http.ServeContent(w, r, filepath.Base(p), fi.ModTime(), f)
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "当前连接不支持 SSE")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fl.Flush()

	ch, _ := s.m.Subscribe()
	defer s.m.Unsubscribe(ch)

	hello, _ := json.Marshal(map[string]any{"version": version.Version, "time": time.Now().UnixMilli()})
	fmt.Fprintf(w, "event: hello\ndata: %s\n\n", hello)
	tasks, _ := s.m.List(engine.ListFilter{Limit: 300})
	snap, _ := json.Marshal(tasks)
	fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", snap)
	stats, _ := json.Marshal(s.m.Stats())
	fmt.Fprintf(w, "event: stats\ndata: %s\n\n", stats)
	fl.Flush()

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case frame, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			fl.Flush()
		case <-ticker.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// ---------- 设置 / 浏览 ----------

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	cur := s.m.Settings()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, 400, "读取请求失败")
		return
	}
	if err := json.Unmarshal(body, &cur); err != nil {
		writeErr(w, 400, "设置格式错误: "+err.Error())
		return
	}
	st, err := s.m.UpdateSettings(cur)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, st)
}

type browseEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	// 这一步只给管理页选择保存目录用：只列目录，不读取文件内容。
	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = s.m.Settings().DefaultDir
	}
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) {
		writeErr(w, 400, "必须是绝对路径")
		return
	}
	f, err := os.Open(dir)
	if err != nil {
		writeErr(w, 400, "无法打开目录: "+err.Error())
		return
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		writeErr(w, 400, "读取目录失败")
		return
	}
	sort.Strings(names)
	entries := make([]browseEntry, 0, 128)
	for _, n := range names {
		if strings.HasPrefix(n, ".") {
			continue
		}
		p := filepath.Join(dir, n)
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			continue
		}
		entries = append(entries, browseEntry{Name: n, Path: p, Dir: true})
		if len(entries) >= 400 {
			break
		}
	}
	writeJSON(w, 200, map[string]any{
		"path":    dir,
		"parent":  filepath.Dir(dir),
		"entries": entries,
	})
}

// ---------- 静态资源 ----------

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeErr(w, 404, "接口不存在")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	if f, ok := s.web[name]; ok {
		w.Header().Set("Content-Type", f.ctype)
		if name == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else if r.URL.Query().Get("v") != "" {
			// 带 ?v=版本号 的资源每次发版都会换 URL，可以放心长缓存
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		w.WriteHeader(200)
		w.Write(f.data)
		return
	}
	// 前端是单页应用，未知路径一律回落 index.html
	idx, ok := s.web["index.html"]
	if !ok {
		writeErr(w, 500, "前端资源缺失")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	w.Write(idx.data)
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.IndexByte(v, ','); i > 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return host
}

func mimeByExt(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".flv":
		return "video/x-flv"
	case ".ts":
		return "video/mp2t"
	case ".mkv":
		return "video/x-matroska"
	case ".mov":
		return "video/quicktime"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	}
	return ""
}
