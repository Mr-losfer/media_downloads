package engine

import (
	"strings"
)

// 任务状态机: queued -> running -> merging -> completed
//
//	queued -> canceled / failed
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusMerging   = "merging"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCanceled  = "canceled"
)

// Task 是一条下载任务。字段全部扁平化，方便直接喂给前端 / API / MCP。
type Task struct {
	ID         string            `json:"id"`
	URL        string            `json:"url"`
	Title      string            `json:"title"`
	SaveDir    string            `json:"save_dir"`
	FileName   string            `json:"file_name"`
	Kind       string            `json:"kind"`   // hls | file
	Format     string            `json:"format"` // 探测到的容器: mp4/ts/flv/...
	Status     string            `json:"status"`
	Progress   float64           `json:"progress"` // 0-100
	Downloaded int64             `json:"downloaded"`
	Total      int64             `json:"total"`
	Speed      int64             `json:"speed"` // B/s
	SegDone    int               `json:"seg_done"`
	SegTotal   int               `json:"seg_total"`
	Conns      int               `json:"conns"`
	Size       int64             `json:"size"`
	Duration   float64           `json:"duration"`
	Host       string            `json:"host"`
	Error      string            `json:"error,omitempty"`
	Attempts   int               `json:"attempts"`
	HasThumb   bool              `json:"has_thumb"`
	Headers    map[string]string `json:"headers,omitempty"`
	CreatedAt  int64             `json:"created_at"`
	UpdatedAt  int64             `json:"updated_at"`
	FinishedAt int64             `json:"finished_at,omitempty"`
}

func (t *Task) clone() *Task {
	c := *t
	if t.Headers != nil {
		c.Headers = make(map[string]string, len(t.Headers))
		for k, v := range t.Headers {
			c.Headers[k] = v
		}
	}
	return &c
}

// AbsPath 返回任务产物的绝对路径。
func (t *Task) AbsPath() string {
	if t.SaveDir == "" || t.FileName == "" {
		return ""
	}
	return joinPath(t.SaveDir, t.FileName)
}

func isActiveStatus(s string) bool {
	return s == StatusRunning || s == StatusMerging
}

type SaveDir struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

// Settings 是设置中心的全部可配置项，整体以 JSON 存进 bbolt。
type Settings struct {
	DefaultDir      string    `json:"default_dir"`
	SaveDirs        []SaveDir `json:"save_dirs"`
	MaxTasks        int       `json:"max_tasks"`      // 同时下载任务数
	SegConns        int       `json:"seg_conns"`      // 单任务并发连接数
	SpeedLimitKB    int       `json:"speed_limit_kb"` // 全局限速, 0=不限
	MaxRetries      int       `json:"max_retries"`
	UserAgent       string    `json:"user_agent"`
	Proxy           string    `json:"proxy"`
	InsecureTLS     bool      `json:"insecure_tls"`
	HLSQuality      string    `json:"hls_quality"` // best | worst
	TrimEnabled     bool      `json:"trim_enabled"`
	TrimHead        int       `json:"trim_head"` // 去掉开头 N 个分片
	TrimTail        int       `json:"trim_tail"` // 去掉结尾 N 个分片
	FFmpegPath      string    `json:"ffmpeg_path"`
	AllowTranscode  bool      `json:"allow_transcode"`
	TranscodePreset string    `json:"transcode_preset"`
	KeepTemp        bool      `json:"keep_temp"`
	Thumbnails      bool      `json:"thumbnails"`
	AutoCleanDays   int       `json:"auto_clean_days"`
	AutoUpdate      bool      `json:"auto_update"` // 开启后每 2 小时检查，空闲时自动升级
	WebhookURL      string    `json:"webhook_url"`
	WebhookSecret   string    `json:"webhook_secret"`
	WebhookProgress bool      `json:"webhook_progress"`
	PublicBase      string    `json:"public_base"`
	APIKey          string    `json:"api_key"`
}

const defaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

func defaultSettings() Settings {
	return Settings{
		DefaultDir:      "/downloads/hdd",
		SaveDirs:        []SaveDir{{Path: "/downloads/hdd", Label: "硬盘"}},
		MaxTasks:        2,
		SegConns:        8,
		SpeedLimitKB:    0,
		MaxRetries:      3,
		UserAgent:       defaultUA,
		HLSQuality:      "best",
		FFmpegPath:      "ffmpeg",
		AllowTranscode:  true,
		TranscodePreset: "veryfast",
		Thumbnails:      true,
		AutoCleanDays:   0,
	}
}

var allowedPresets = map[string]bool{
	"ultrafast": true, "superfast": true, "veryfast": true, "faster": true,
	"fast": true, "medium": true, "slow": true, "slower": true, "veryslow": true,
}

// Sanitize 把任何来源（API / MCP / 配置文件）的设置夹到安全范围，
// 防止有人写 SegConns=5000 把 NAS 打爆。
func (s *Settings) Sanitize() {
	s.MaxTasks = clampInt(s.MaxTasks, 1, 8)
	s.SegConns = clampInt(s.SegConns, 1, 32)
	s.SpeedLimitKB = clampInt(s.SpeedLimitKB, 0, 1048576)
	s.MaxRetries = clampInt(s.MaxRetries, 0, 10)
	s.AutoCleanDays = clampInt(s.AutoCleanDays, 0, 3650)
	s.TrimHead = clampInt(s.TrimHead, 0, 99)
	s.TrimTail = clampInt(s.TrimTail, 0, 99)
	if s.UserAgent == "" {
		s.UserAgent = defaultUA
	}
	if s.FFmpegPath == "" {
		s.FFmpegPath = "ffmpeg"
	}
	if s.HLSQuality != "worst" {
		s.HLSQuality = "best"
	}
	if !allowedPresets[s.TranscodePreset] {
		s.TranscodePreset = "veryfast"
	}
	seen := map[string]bool{}
	dirs := make([]SaveDir, 0, len(s.SaveDirs))
	for _, d := range s.SaveDirs {
		d.Path = strings.TrimSpace(d.Path)
		if d.Path == "" || seen[d.Path] {
			continue
		}
		seen[d.Path] = true
		if d.Label == "" {
			d.Label = d.Path
		}
		dirs = append(dirs, d)
	}
	if s.DefaultDir == "" && len(dirs) > 0 {
		s.DefaultDir = dirs[0].Path
	}
	if s.DefaultDir != "" && !seen[s.DefaultDir] {
		dirs = append([]SaveDir{{Path: s.DefaultDir, Label: "默认"}}, dirs...)
	}
	if len(dirs) == 0 {
		dirs = []SaveDir{{Path: "/downloads", Label: "默认"}}
		s.DefaultDir = "/downloads"
	}
	s.SaveDirs = dirs
}
