package server

import (
	"context"
	"log"
	"net/http"
	"os"
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
	ffPath, ffOK := s.m.FFmpegPath()
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
		"ffmpeg_ok":   ffOK,
		"ffmpeg_path": ffPath,
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
	binPath, err := s.stageLatest(ctx, st)
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

// stageLatest 下载并校验更新包。
// version.json 与 Release 附件偶尔会不同步（重复发布覆盖产物），
// 校验失败时用 Releases API 里的 digest 再试一次。
func (s *Server) stageLatest(ctx context.Context, st *updater.Status) (string, error) {
	binPath, err := s.up.Stage(ctx, st, nil)
	if err != nil && strings.Contains(err.Error(), "校验失败") {
		log.Printf("更新包摘要不一致，改用 Release API 校验重试")
		if alt := s.up.CheckAPIOnly(ctx); alt.SHA256 != "" && alt.SHA256 != st.SHA256 {
			if p2, err2 := s.up.Stage(ctx, alt, nil); err2 == nil {
				return p2, nil
			}
		}
	}
	return binPath, err
}

const (
	// autoUpdateInterval 自动更新开启后的检查周期
	autoUpdateInterval = 2 * time.Hour
	// autoUpdateTick 后台巡检粒度（真正检查仍受 autoUpdateInterval 限制）
	autoUpdateTick = 5 * time.Minute
	// autoUpdateSettle 发现新版本后再等一会儿，确认用户没有新任务
	autoUpdateSettle = 90 * time.Second
)

// autoInterval 返回检查周期，默认 2 小时；MS_AUTO_UPDATE_INTERVAL 仅供调试。
func autoInterval() time.Duration {
	if v := strings.TrimSpace(os.Getenv("MS_AUTO_UPDATE_INTERVAL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 5*time.Second {
			return d
		}
	}
	return autoUpdateInterval
}

// autoSettle 返回确认空闲的等待时间，默认 90 秒；MS_AUTO_UPDATE_SETTLE 仅供调试。
func autoSettle() time.Duration {
	if v := strings.TrimSpace(os.Getenv("MS_AUTO_UPDATE_SETTLE")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return autoUpdateSettle
}

// StartAutoUpdate 启动后台巡检：开启「自动更新」后每 2 小时检查一次，
// 发现新版本且当前没有下载任务时，自动下载、校验并热切换到新版本。
func (s *Server) StartAutoUpdate() {
	go func() {
		iv := autoInterval()
		tick := autoUpdateTick
		if iv < tick {
			tick = iv
		}
		// 启动后先缓一会儿，避开启动时的首次检查与初始化
		delay := 2 * time.Minute
		if tick < delay {
			delay = tick
		}
		var lastCheck time.Time
		var pending *updater.Status
		timer := time.NewTimer(delay)
		defer timer.Stop()
		for {
			<-timer.C
			timer.Reset(tick)

			if !s.m.Settings().AutoUpdate {
				pending = nil
				continue
			}
			if pending == nil {
				if !lastCheck.IsZero() && time.Since(lastCheck) < iv {
					continue
				}
				lastCheck = time.Now()
				pending = s.checkForAutoUpdate()
				if pending == nil {
					continue
				}
			}
			// 已有确认的新版本：等空闲就升级；忙时下个巡检周期再看，不重复请求网络
			if !s.m.Idle() {
				log.Printf("自动更新：发现 %s，但有任务在跑，等空闲后再试", pending.Latest)
				continue
			}
			if settle := autoSettle(); settle > 0 {
				log.Printf("自动更新：发现 %s，等待 %s 确认空闲", pending.Latest, settle)
				time.Sleep(settle)
			}
			if !s.m.Idle() {
				log.Printf("自动更新：检测到新任务，取消本次升级")
				continue
			}
			s.applyAutoUpdate(pending)
			pending = nil
		}
	}()
}

// checkForAutoUpdate 检查新版本；没有可自动升级的目标时返回 nil。
func (s *Server) checkForAutoUpdate() *updater.Status {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	st := s.up.Check(ctx, true)
	if st.Error != "" {
		log.Printf("自动更新：检查失败 %s", st.Error)
		return nil
	}
	if !st.HasUpdate {
		return nil
	}
	if !st.CanAuto {
		log.Printf("自动更新：发现 %s，但当前架构没有可用的自动更新包", st.Latest)
		return nil
	}
	return st
}

// applyAutoUpdate 下载、校验并重启到新版本。
func (s *Server) applyAutoUpdate(st *updater.Status) {
	if !updateMu.TryLock() {
		log.Printf("自动更新：已有更新任务在进行，跳过")
		return
	}
	defer updateMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	binPath, err := s.stageLatest(ctx, st)
	if err != nil {
		log.Printf("自动更新：下载失败 %v", err)
		return
	}

	log.Printf("自动更新：已下载 %s，准备重启", st.Latest)
	s.m.Broadcast("update.auto", map[string]any{
		"from": version.Version,
		"to":   st.Latest,
	})
	// 给前端留出提示时间
	time.Sleep(900 * time.Millisecond)
	if err := updater.Restart(binPath); err != nil {
		log.Printf("自动更新：重启失败 %v", err)
	}
}
