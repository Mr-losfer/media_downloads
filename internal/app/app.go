// Package app 负责把存储、下载引擎、HTTP 服务和更新器装配起来。
//
// cmd/mediasuper 只做参数解析，真正的启动流程都在这里，
// 这样以后要加 fpk / systemd / 容器入口只改一处。
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mediasuper/internal/engine"
	"mediasuper/internal/server"
	"mediasuper/internal/updater"
	"mediasuper/internal/version"
	"mediasuper/web"
)

// Options 是启动参数，来自命令行或环境变量。
type Options struct {
	Addr    string
	DataDir string
	// DisableUpdateCheck 关闭启动时的更新探测（离线环境用）
	DisableUpdateCheck bool
}

// 默认管理员密码。首次启动写入数据库，之后请到「设置 → 管理员密码」修改。
const defaultAdminPassword = "losfer"

// Run 启动整个服务，阻塞直到收到退出信号。
func Run(opt Options) error {
	if err := os.MkdirAll(opt.DataDir, 0o755); err != nil {
		return fmt.Errorf("无法创建数据目录 %s: %w", opt.DataDir, err)
	}

	// 数据目录里可能有上次「一键更新」下载的新版本，有新就切过去
	updater.Handoff(opt.DataDir)

	// 上次异常退出可能留下半截分片，启动时清空（此时还没有任务在跑）
	if err := os.RemoveAll(filepath.Join(opt.DataDir, "tmp")); err != nil {
		log.Printf("清理临时目录失败: %v", err)
	}

	db, err := openDB(opt.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()

	firstBoot := db.MetaGet("settings") == ""
	st, err := db.LoadSettings()
	if err != nil {
		log.Printf("配置损坏，使用默认值: %v", err)
	}
	if firstBoot {
		applyEnvSettings(&st)
	}
	st.Sanitize()

	apiKey := db.MetaGet("api_key")
	if apiKey == "" {
		apiKey = "ms_" + randomKey(20)
		_ = db.MetaPut("api_key", apiKey)
	}
	st.APIKey = apiKey

	passHash := db.MetaGet("pass_hash")
	passSource := "环境变量 MS_ADMIN_PASS"
	if passHash == "" {
		pw := envAny("MS_ADMIN_PASS", "VF_ADMIN_PASS")
		if pw == "" {
			pw = defaultAdminPassword
			passSource = "默认密码 losfer（请到设置页修改）"
		}
		h, err := server.HashPassword(pw)
		if err != nil {
			return fmt.Errorf("初始化密码失败: %w", err)
		}
		passHash = h
		if err := db.MetaPut("pass_hash", passHash); err != nil {
			return err
		}
	}

	secretHex := db.MetaGet("secret")
	if secretHex == "" {
		secretHex = randomKey(32)
		_ = db.MetaPut("secret", secretHex)
	}
	secret := hexToBytes(secretHex)
	if len(secret) == 0 {
		secret = []byte("mediasuper-fallback-secret")
	}

	for _, d := range st.SaveDirs {
		if err := os.MkdirAll(d.Path, 0o755); err != nil {
			log.Printf("⚠ 保存目录不可写 %s: %v", d.Path, err)
		}
	}
	if err := db.SaveSettings(st); err != nil {
		return err
	}

	m := engine.NewManager(db, st, opt.DataDir)
	m.Start()
	defer m.Broadcast("shutdown", map[string]any{"ok": true})

	up := updater.New(opt.DataDir, version.Repo)
	srv := server.NewServer(m, server.NewAuth(apiKey, passHash, secret), server.LoadWebAssets(web.FS), up)
	if !opt.DisableUpdateCheck {
		srv.StartAutoUpdate()
	}

	httpSrv := &http.Server{
		Addr:              opt.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	banner(opt.Addr, opt.DataDir, st, apiKey, passSource)

	if open, ffVer := m.FFmpeg(); open {
		log.Printf("  ffmpeg  %s", ffVer)
	} else {
		log.Printf("  ⚠ ffmpeg 未找到：m3u8 分片仍可下载，但无法转成 mp4")
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// 服务起来了，说明当前二进制能跑，清掉切换失败的计数
	updater.MarkHealthy(opt.DataDir)

	if !opt.DisableUpdateCheck {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if s := up.Check(ctx, true); s.HasUpdate {
				log.Printf("发现新版本 %s（当前 %s），可在 设置 → 系统更新 中一键升级", s.Latest, s.Current)
			}
		}()
	}

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

// openDB 打开数据库，并兼容旧版本的 vidfetch.db。
func openDB(dataDir string) (*engine.Store, error) {
	newPath := filepath.Join(dataDir, "mediasuper.db")
	oldPath := filepath.Join(dataDir, "vidfetch.db")
	if _, err := os.Stat(newPath); err != nil {
		if _, err2 := os.Stat(oldPath); err2 == nil {
			if err3 := os.Rename(oldPath, newPath); err3 != nil {
				log.Printf("迁移旧数据库失败（继续使用新库）: %v", err3)
			} else {
				log.Printf("已把旧数据库 vidfetch.db 迁移为 mediasuper.db")
			}
		}
	}
	db, err := engine.OpenStore(newPath)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	return db, nil
}

// applyEnvSettings 只在首次启动（数据库里还没有 settings）时生效。
// 兼容旧的 VF_ 前缀，新变量统一 MS_ 前缀。
func applyEnvSettings(st *engine.Settings) {
	if v := envAny("MS_SAVE_DIRS", "VF_SAVE_DIRS"); v != "" {
		var dirs []engine.SaveDir
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
			dirs = append(dirs, engine.SaveDir{Path: path, Label: label})
		}
		if len(dirs) > 0 {
			st.SaveDirs = dirs
			if envAny("MS_DEFAULT_DIR", "VF_DEFAULT_DIR") == "" {
				st.DefaultDir = dirs[0].Path
			}
		}
	}
	if v := envAny("MS_DEFAULT_DIR", "VF_DEFAULT_DIR"); v != "" {
		st.DefaultDir = v
	}
	if v := envAny("MS_MAX_TASKS", "VF_MAX_TASKS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			st.MaxTasks = n
		}
	}
	if v := envAny("MS_SEG_CONNS", "VF_SEG_CONNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			st.SegConns = n
		}
	}
	if v := envAny("MS_SPEED_LIMIT_KB", "VF_SPEED_LIMIT_KB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			st.SpeedLimitKB = n
		}
	}
	if v := envAny("MS_FFMPEG", "VF_FFMPEG"); v != "" {
		st.FFmpegPath = v
	}
	if v := envAny("MS_WEBHOOK_URL", "VF_WEBHOOK_URL"); v != "" {
		st.WebhookURL = v
	}
}

func banner(addr, dataDir string, st engine.Settings, apiKey, passSource string) {
	lines := []string{
		"",
		"  __  __          _ _         ____",
		" |  \\/  | ___  __| (_) __ _  / ___| _   _ _ __   ___ _ __",
		" | |\\/| |/ _ \\/ _` | |/ _` | \\___ \\| | | | '_ \\ / _ \\ '__|",
		" | |  | |  __/ (_| | | (_| |  ___) | |_| | |_) |  __/ |",
		" |_|  |_|\\___|\\__,_|_|\\__,_| |____/ \\__,_| .__/ \\___|_|",
		"",
		fmt.Sprintf("  %s %s · Go 单二进制 · 低配 NAS 优化", version.Name, version.String()),
		fmt.Sprintf("  监听    %s", addr),
		fmt.Sprintf("  数据    %s", dataDir),
		fmt.Sprintf("  目录    %s", joinDirList(st.SaveDirs)),
		fmt.Sprintf("  并发    任务 %d / 单任务分片 %d", st.MaxTasks, st.SegConns),
		fmt.Sprintf("  密码    %s", passSource),
		fmt.Sprintf("  API Key %s", apiKey),
		"",
	}
	for _, l := range lines {
		log.Println(l)
	}
	hint := addr
	if strings.HasPrefix(addr, ":") {
		hint = "<NAS-IP>" + addr
	}
	log.Printf("  打开 http://%s 开始使用", hint)
}

func joinDirList(dirs []engine.SaveDir) string {
	parts := make([]string, 0, len(dirs))
	for _, d := range dirs {
		parts = append(parts, fmt.Sprintf("%s(%s)", d.Path, d.Label))
	}
	return strings.Join(parts, "  ")
}

func envAny(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func randomKey(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func hexToBytes(s string) []byte {
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		v, err := strconv.ParseUint(s[i*2:i*2+2], 16, 8)
		if err != nil {
			return nil
		}
		out[i] = byte(v)
	}
	return out
}
