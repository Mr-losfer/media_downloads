package server

import (
	"context"
	"log"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"mediasuper/internal/updater"
	"mediasuper/internal/version"
)

// updateMu 保证同一时间只有一个「一键更新」在跑，避免重复下载把 NAS 打满。
var updateMu sync.Mutex

// handleSystemInfo 返回构建信息与运行环境，前端「关于」区块直接用。
func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	st := s.m.Settings()
	writeJSON(w, 200, map[string]any{
		"name":        version.Name,
		"version":     version.Version,
		"version_str": version.String(),
		"commit":      version.Commit,
		"build_time":  version.BuildTime,
		"go_version":  runtime.Version(),
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
		"platform":    updater.PlatformKey(),
		"repo":        version.Repo,
		"data_dir":    s.m.DataDir(),
		"staged":      updater.Staged(s.m.DataDir()),
		"uptime":      int64(time.Since(s.startedAt).Seconds()),
		"started_at":  s.startedAt.UnixMilli(),
		"default_dir": st.DefaultDir,
	})
}

// handleCheckUpdate 检查更新。?force=1 跳过 5 分钟缓存。
func (s *Server) handleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "1" || r.URL.Query().Get("force") == "true"
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	st := s.up.Check(ctx, force)
	writeJSON(w, 200, st)
}

// handleApplyUpdate 一键更新：下载 → 校验 → 落盘 → 重启进程。
//
// 注意：进程会用新二进制 exec 自己，所以响应必须先写回客户端。
func (s *Server) handleApplyUpdate(w http.ResponseWriter, r *http.Request) {
	if !updateMu.TryLock() {
		writeErr(w, http.StatusConflict, "已有一个更新任务在进行中")
		return
	}
	defer updateMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cur := version.Version
	st := s.up.Check(ctx, true)
	if st.Error != "" {
		writeErr(w, http.StatusBadGateway, "检查更新失败："+st.Error)
		return
	}
	if !st.HasUpdate {
		writeJSON(w, 200, map[string]any{
			"ok": true, "updated": false, "version": cur,
			"message": "已经是最新版本 " + cur,
		})
		return
	}
	binPath, err := s.up.Stage(ctx, st, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "下载更新失败："+err.Error())
		return
	}

	log.Printf("更新已下载，准备重启到 %s", st.Latest)
	writeJSON(w, 200, map[string]any{
		"ok": true, "updated": true,
		"from": cur, "to": st.Latest,
		"restarting": true,
		"message":    "更新已下载，服务正在重启，几秒后自动生效",
	})

	go func() {
		// 给 HTTP 响应留出写回时间，再 exec 新进程
		time.Sleep(900 * time.Millisecond)
		if err := updater.Restart(binPath); err != nil {
			log.Printf("重启到新版本失败: %v", err)
		}
	}()
}

// handleChangePassword 修改管理员密码。
//
// 必须校验旧密码，避免 Cookie 被盗后直接改密码锁死后台。
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	if !s.auth.CheckPassword(in.OldPassword) {
		writeErr(w, http.StatusUnauthorized, "当前密码不正确")
		return
	}
	pw := strings.TrimSpace(in.NewPassword)
	if len([]rune(pw)) < 6 {
		writeErr(w, 400, "新密码至少 6 位")
		return
	}
	if len([]rune(pw)) > 128 {
		writeErr(w, 400, "新密码太长（最多 128 位）")
		return
	}
	if pw == in.OldPassword {
		writeErr(w, 400, "新密码不能和当前密码相同")
		return
	}
	h, err := HashPassword(pw)
	if err != nil {
		writeErr(w, 500, "生成密码哈希失败")
		return
	}
	if err := s.m.SetPasswordHash(h); err != nil {
		writeErr(w, 500, "保存新密码失败："+err.Error())
		return
	}
	s.auth.SetPassHash(h)
	log.Printf("管理员密码已更新")
	writeJSON(w, 200, map[string]any{"ok": true, "message": "密码已更新，下次登录请使用新密码"})
}
