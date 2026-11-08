package main

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const appVersion = "1.1.0"

//go:embed web
var webFS embed.FS

func main() {
	addr := flag.String("addr", envOr("VF_ADDR", ":8080"), "HTTP 监听地址")
	dataDir := flag.String("data", envOr("VF_DATA_DIR", "/data"), "数据目录（数据库/临时分片）")
	stdio := flag.Bool("stdio", false, "以 MCP stdio 模式运行（把 stdin 的 JSON-RPC 转发到 -remote）")
	remote := flag.String("remote", envOr("VF_REMOTE", "http://127.0.0.1:8080"), "stdio 模式下要连接的服务地址")
	key := flag.String("key", envOr("VF_API_KEY", ""), "API Key（stdio 模式）")
	flag.Parse()
	log.SetFlags(log.LstdFlags)

	if *stdio {
		if err := runStdioProxy(*remote, *key); err != nil {
			log.Fatalf("stdio 模式失败: %v", err)
		}
		return
	}
	if err := runServer(*addr, *dataDir); err != nil {
		log.Fatalf("启动失败: %v", err)
	}
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func loadWeb() map[string]staticFile {
	out := make(map[string]staticFile, 8)
	entries, err := fs.ReadDir(webFS, "web")
	if err != nil {
		log.Printf("读取内嵌前端失败: %v", err)
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := webFS.ReadFile("web/" + e.Name())
		if err != nil {
			continue
		}
		ct := mime.TypeByExtension(filepath.Ext(e.Name()))
		if ct == "" {
			switch filepath.Ext(e.Name()) {
			case ".svg":
				ct = "image/svg+xml"
			case ".webmanifest":
				ct = "application/manifest+json"
			case ".woff2":
				ct = "font/woff2"
			default:
				ct = "application/octet-stream"
			}
		}
		out[e.Name()] = staticFile{data: b, ctype: ct}
	}
	return out
}

func applyEnvSettings(st *Settings) {
	if v := os.Getenv("VF_SAVE_DIRS"); v != "" {
		var dirs []SaveDir
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			path, label := part, part
			if i := strings.LastIndex(part, ":"); i > 0 && !strings.Contains(part[i:], "/") {
				path = strings.TrimSpace(part[:i])
				label = strings.TrimSpace(part[i+1:])
			}
			dirs = append(dirs, SaveDir{Path: path, Label: label})
		}
		if len(dirs) > 0 {
			st.SaveDirs = dirs
			if os.Getenv("VF_DEFAULT_DIR") == "" {
				st.DefaultDir = dirs[0].Path
			}
		}
	}
	if v := os.Getenv("VF_DEFAULT_DIR"); v != "" {
		st.DefaultDir = v
	}
	if v := os.Getenv("VF_MAX_TASKS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			st.MaxTasks = n
		}
	}
	if v := os.Getenv("VF_SEG_CONNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			st.SegConns = n
		}
	}
	if v := os.Getenv("VF_SPEED_LIMIT_KB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			st.SpeedLimitKB = n
		}
	}
	if v := os.Getenv("VF_FFMPEG"); v != "" {
		st.FFmpegPath = v
	}
	if v := os.Getenv("VF_WEBHOOK_URL"); v != "" {
		st.WebhookURL = v
	}
}

func runServer(addr, dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("无法创建数据目录 %s: %w", dataDir, err)
	}
	// 上次异常退出可能留下半截分片，启动时清空（此时还没有任何任务在跑）
	if err := os.RemoveAll(filepath.Join(dataDir, "tmp")); err != nil {
		log.Printf("清理临时目录失败: %v", err)
	}
	db, err := openStore(filepath.Join(dataDir, "vidfetch.db"))
	if err != nil {
		return fmt.Errorf("打开数据库失败: %w", err)
	}
	defer db.Close()

	firstBoot := db.metaGet("settings") == ""
	st, err := db.loadSettings()
	if err != nil {
		log.Printf("配置损坏，使用默认值: %v", err)
	}
	if firstBoot {
		applyEnvSettings(&st)
	}
	st.sanitize()

	apiKey := db.metaGet("api_key")
	if apiKey == "" {
		apiKey = "vf_" + randomHex(20)
		_ = db.metaPut("api_key", apiKey)
	}
	st.APIKey = apiKey

	passHash := db.metaGet("pass_hash")
	passSource := "环境变量 VF_ADMIN_PASS"
	if passHash == "" {
		pw := os.Getenv("VF_ADMIN_PASS")
		if pw == "" {
			pw = "vidfetch123"
			passSource = "默认密码（请尽快用 VF_ADMIN_PASS 修改）"
		}
		h, err := hashPassword(pw)
		if err != nil {
			return fmt.Errorf("初始化密码失败: %w", err)
		}
		passHash = h
		if err := db.metaPut("pass_hash", passHash); err != nil {
			return err
		}
	}

	secretHex := db.metaGet("secret")
	if secretHex == "" {
		secretHex = randomHex(32)
		_ = db.metaPut("secret", secretHex)
	}
	secret, _ := hexDecode(secretHex)
	if len(secret) == 0 {
		secret = []byte("vidfetch-fallback-secret")
	}

	for _, d := range st.SaveDirs {
		if err := os.MkdirAll(d.Path, 0o755); err != nil {
			log.Printf("⚠ 保存目录不可写 %s: %v", d.Path, err)
		}
	}
	if err := db.saveSettings(st); err != nil {
		return err
	}

	m := NewManager(db, st, dataDir)
	m.Start()
	defer m.hub.Broadcast("shutdown", map[string]any{"ok": true})

	srv := NewServer(m, newAuth(apiKey, passHash, secret), loadWeb())

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	ffVer, ffOK := m.ff.Available()
	banner := []string{
		"",
		"  ██╗   ██╗██╗██████╗ ███████╗███████╗████████╗ ██████╗██╗  ██╗",
		"  ██║   ██║██║██╔══██╗██╔════╝██╔════╝╚══██╔══╝██╔════╝██║  ██║",
		"  ██║   ██║██║██║  ██║█████╗  █████╗     ██║   ██║     ███████║",
		"  ╚██╗ ██╔╝██║██║  ██║██╔══╝  ██╔══╝     ██║   ██║     ██╔══██║",
		"   ╚████╔╝ ██║██████╔╝██║     ███████╗   ██║   ╚██████╗██║  ██║",
		"    ╚═══╝  ╚═╝╚═════╝ ╚═╝     ╚══════╝   ╚═╝    ╚═════╝╚═╝  ╚═╝",
		"",
		fmt.Sprintf("  版本 %s · Go 单二进制 · 低配 NAS 优化", appVersion),
		fmt.Sprintf("  监听    %s", addr),
		fmt.Sprintf("  数据    %s", dataDir),
		fmt.Sprintf("  目录    %s", joinDirList(st.SaveDirs)),
		fmt.Sprintf("  并发    任务 %d / 单任务分片 %d", st.MaxTasks, st.SegConns),
		fmt.Sprintf("  密码    %s", passSource),
		fmt.Sprintf("  API Key %s", apiKey),
		"",
	}
	for _, l := range banner {
		log.Println(l)
	}
	if ffOK {
		log.Printf("  ffmpeg  %s", ffVer)
	} else {
		log.Printf("  ⚠ ffmpeg 未找到（%s）：m3u8 分片仍可下载，但无法转成 mp4", st.FFmpegPath)
	}
	hint := addr
	if strings.HasPrefix(addr, ":") {
		hint = "<NAS-IP>" + addr
	}
	log.Printf("  打开 http://%s 开始使用", hint)

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case s := <-sig:
		log.Printf("收到 %v，正在退出…", s)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(ctx)
}

func joinDirList(dirs []SaveDir) string {
	parts := make([]string, 0, len(dirs))
	for _, d := range dirs {
		parts = append(parts, fmt.Sprintf("%s(%s)", d.Path, d.Label))
	}
	return strings.Join(parts, "  ")
}

func hexDecode(s string) ([]byte, error) {
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		v, err := strconv.ParseUint(s[i*2:i*2+2], 16, 8)
		if err != nil {
			return nil, err
		}
		out[i] = byte(v)
	}
	return out, nil
}

// runStdioProxy 让 Claude Desktop 这类只支持 stdio 的客户端也能用：
// 逐行读 JSON-RPC，转发到远端 HTTP 服务再写回 stdout。
func runStdioProxy(remote, apiKey string) error {
	remote = strings.TrimSuffix(remote, "/") + "/mcp"
	client := &http.Client{Timeout: 120 * time.Second}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 1<<20), 4<<20)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	for in.Scan() {
		line := bytes.TrimSpace(in.Bytes())
		if len(line) == 0 {
			continue
		}
		req, err := http.NewRequest(http.MethodPost, remote, bytes.NewReader(line))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			body, _ := json.Marshal(&rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
				Error: &rpcError{Code: -32000, Message: "连接远端失败: " + err.Error()}})
			out.Write(body)
			out.WriteString("\n")
			out.Flush()
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if len(bytes.TrimSpace(body)) > 0 {
			out.Write(bytes.TrimSpace(body))
			out.WriteString("\n")
			out.Flush()
		}
	}
	return in.Err()
}
