package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FileOps 封装 ffmpeg / ffprobe。全部走 -c copy 重封装，解码转码只做兜底，
// 这是低配 NAS 能扛住 m3u8 的唯一正确姿势。
type FileOps struct {
	mu      sync.Mutex
	hint    string // 用户配置里写的路径，找不到时用于提示
	ffmpeg  string // 解析后的绝对路径；空串表示没找到
	ffprobe string
}

// newFileOps 解析 ffmpeg 位置。
//
// fnOS / 容器里 PATH 往往不含 ffmpeg，用户升级前写进数据库的 "ffmpeg"
// 也不能再用了，所以这里按「用户配置 → 应用自带 → PATH → 常见安装位置」
// 的顺序做一次真实探测，而不是把字符串直接交给 exec。
func newFileOps(ffmpegPath string) *FileOps {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	f := &FileOps{hint: ffmpegPath}
	f.resolve()
	return f
}

func (f *FileOps) resolve() {
	ff := resolveBinary(f.hint, "ffmpeg")
	probe := ""
	if ff != "" {
		if p := filepath.Join(filepath.Dir(ff), "ffprobe"); isExecutable(p) {
			probe = p
		}
	}
	if probe == "" {
		probe = resolveBinary("", "ffprobe")
	}
	f.ffmpeg, f.ffprobe = ff, probe
}

// candidates 列出所有可能的可执行文件位置。
func candidates(pref, name string) []string {
	var list []string
	add := func(p string) {
		if p != "" {
			list = append(list, p)
		}
	}

	// 1. 用户显式配置（只有写成绝对路径/带目录才有意义）
	if pref != "" && pref != name && strings.ContainsRune(pref, '/') {
		add(pref)
	}
	// 2. 环境变量：Docker、systemd 里最常用的注入方式
	add(os.Getenv("MS_FFMPEG"))

	// 3. 程序自带（fpk 会把静态 ffmpeg 放在 app/bin，Docker 镜像放在 /app/bin）
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		add(filepath.Join(dir, name))
		add(filepath.Join(dir, "bin", name))
		add(filepath.Join(dir, "..", "bin", name))
	}
	// 4. PATH
	if p, err := exec.LookPath(name); err == nil {
		add(p)
	}
	// 5. 常见安装位置：fnOS、群晖、威联通、Homebrew、Linux 发行版
	add("/usr/local/bin/" + name)
	add("/usr/bin/" + name)
	add("/bin/" + name)
	add("/usr/trim/bin/" + name)
	add("/vol1/@appcenter/ffmpeg/bin/" + name)
	add("/volume1/@appstore/ffmpeg/bin/" + name)
	add("/opt/homebrew/bin/" + name)
	return list
}

func resolveBinary(pref, name string) string {
	for _, p := range candidates(pref, name) {
		if isExecutable(p) {
			if abs, err := filepath.Abs(p); err == nil {
				return abs
			}
			return p
		}
	}
	return ""
}

func isExecutable(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return false
	}
	return st.Mode()&0o111 != 0
}

// Ready 表示 ffmpeg 现在可用。启动时没找到也没关系：每次调用都会重试解析，
// 用户装完 ffmpeg 或改了设置不用重启进程。
func (f *FileOps) Ready() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if isExecutable(f.ffmpeg) {
		return true
	}
	f.resolve()
	return f.ffmpeg != ""
}

// Path 返回当前解析到的 ffmpeg 路径（可能为空）。
func (f *FileOps) Path() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ffmpeg == "" {
		f.resolve()
	}
	return f.ffmpeg
}

// MissingError 是「找不到 ffmpeg」时给用户看的统一文案。
func (f *FileOps) MissingError() error {
	hint := f.hint
	if hint == "" || hint == "ffmpeg" {
		hint = "未配置"
	}
	return permanent(fmt.Errorf(
		"未找到 ffmpeg（当前配置：%s）：m3u8 分片需要 ffmpeg 才能封装成 mp4。"+
			"请重新安装带 ffmpeg 的安装包，或在「设置 → 下载 → ffmpeg 路径」填写可执行文件的绝对路径", hint))
}

func (f *FileOps) Available() (string, bool) {
	f.mu.Lock()
	ff := f.ffmpeg
	if ff == "" {
		f.resolve()
		ff = f.ffmpeg
	}
	f.mu.Unlock()
	if ff == "" {
		return "", false
	}
	// 低配机（J1800）冷启动首次执行 ffmpeg 可能超过 8 秒，给足时间避免误报
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ff, "-version").Output()
	if err != nil {
		// 探测超时/失败但可执行文件确实存在时，按可用处理，真正调用时再兜底
		if isExecutable(ff) {
			return "已就位（-version 未及时响应）", true
		}
		return "", false
	}
	line := strings.SplitN(string(out), "\n", 2)[0]
	return strings.TrimSpace(line), true
}

// Convert 先尝试无损重封装，失败再按需转码。返回成品字节数。
func (f *FileOps) Convert(ctx context.Context, in, out string, st Settings) (int64, error) {
	dir := dirOf(out)
	if err := ensureDir(dir); err != nil {
		return 0, err
	}
	if !f.Ready() {
		return 0, f.MissingError()
	}

	// 不直接写最终文件：NAS 上常见「目标文件属主不同 / 被占用」让 ffmpeg
	// open(O_TRUNC) 报 Permission denied。写到同目录临时文件再原子替换，
	// 只需目录可写即可覆盖，也不会留下半截 mp4。
	tf, err := os.CreateTemp(dir, ".ms-convert-*.mp4")
	if err != nil {
		return 0, writeHint(dir, err)
	}
	tmp := tf.Name()
	tf.Close()
	os.Remove(tmp)
	defer os.Remove(tmp)

	cctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	args := []string{
		"-y", "-hide_banner", "-nostdin", "-loglevel", "error",
		"-threads", "1",
		"-i", in,
		"-map", "0:v?", "-map", "0:a?",
		"-c", "copy",
		"-max_muxing_queue_size", "512",
		"-movflags", "+faststart",
		"-f", "mp4",
		tmp,
	}
	remuxErr := f.run(cctx, args)
	if remuxErr == nil {
		if sz := fileSize(tmp); sz > 0 {
			if err := commitOutput(tmp, out); err != nil {
				return 0, err
			}
			return sz, nil
		}
		remuxErr = fmt.Errorf("ffmpeg 没有输出文件")
	}
	os.Remove(tmp)

	// 可执行文件在运行中被删掉、或权限变了：重试同样没用，直接给永久错误
	if errors.Is(remuxErr, exec.ErrNotFound) {
		return 0, f.MissingError()
	}
	// 目录权限 / 只读文件系统 / 磁盘满：再转码、再重下都是白费
	if isFSPermanent(remuxErr) {
		return 0, fsError(remuxErr, out)
	}

	if !st.AllowTranscode {
		return 0, permanentf(remuxErr,
			"无损重封装失败（编码不兼容），且未开启转码兜底：%v", remuxErr)
	}

	preset := st.TranscodePreset
	if !allowedPresets[preset] {
		preset = "veryfast"
	}
	args = []string{
		"-y", "-hide_banner", "-nostdin", "-loglevel", "error",
		"-threads", "2",
		"-i", in,
		"-map", "0:v?", "-map", "0:a?",
		"-c:v", "libx264", "-preset", preset, "-crf", "23", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k",
		"-movflags", "+faststart",
		"-f", "mp4",
		tmp,
	}
	if err := f.run(cctx, args); err != nil {
		os.Remove(tmp)
		if errors.Is(err, exec.ErrNotFound) {
			return 0, f.MissingError()
		}
		if isFSPermanent(err) {
			return 0, fsError(err, out)
		}
		return 0, fmt.Errorf("转码也失败了: %w", err)
	}
	sz := fileSize(tmp)
	if sz == 0 {
		return 0, fmt.Errorf("ffmpeg 输出为空")
	}
	if err := commitOutput(tmp, out); err != nil {
		return 0, err
	}
	return sz, nil
}

// commitOutput 把临时成品原子替换到目标路径。
// 只要目录可写，rename 就能覆盖已存在的只读文件；失败时给可操作的权限提示。
func commitOutput(tmp, out string) error {
	if err := os.Chmod(tmp, 0o644); err != nil {
		return writeHint(dirOf(out), err)
	}
	if err := os.Rename(tmp, out); err == nil {
		return nil
	}
	// 目标已存在且不能被 rename 覆盖时，先删再挪（部分文件系统 / Windows）
	if _, statErr := os.Stat(out); statErr == nil {
		if rmErr := os.Remove(out); rmErr == nil {
			if err := os.Rename(tmp, out); err == nil {
				return nil
			}
		}
	}
	return writeHint(dirOf(out), fmt.Errorf("无法把成品写入 %s", out))
}

// fsErrKeys 是重试也没用的文件系统错误，统一转成永久错误 + 可操作提示。
var fsErrKeys = []string{
	"Permission denied", "permission denied",
	"Read-only file system", "No space left on device",
	"Disk quota exceeded", "Operation not permitted",
}

func isFSPermanent(err error) bool {
	if err == nil {
		return false
	}
	if os.IsPermission(err) {
		return true
	}
	s := err.Error()
	for _, k := range fsErrKeys {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

func fsError(err error, out string) error {
	s := err.Error()
	if strings.Contains(s, "No space left") || strings.Contains(s, "quota") {
		return permanentf(err, "磁盘空间不足，无法写入 %s：%v", dirOf(out), err)
	}
	return permanentf(err,
		"无法写入成品文件 %s（%v）。请检查保存目录权限：在 NAS / fnOS 里给应用授权该目录的写入权限，"+
			"或到「设置 → 存储」换一个可写目录", out, err)
}

// Thumb 抽一帧做封面，用完即走，失败不影响主流程。
func (f *FileOps) Thumb(ctx context.Context, in, out string) error {
	if !f.Ready() {
		return f.MissingError()
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	args := []string{
		"-y", "-hide_banner", "-nostdin", "-loglevel", "error",
		"-ss", "3", "-i", in,
		"-frames:v", "1", "-vf", "scale=480:-2", "-q:v", "6",
		out,
	}
	if err := f.run(cctx, args); err == nil && fileSize(out) > 0 {
		return nil
	}
	os.Remove(out)
	args[6] = "0"
	if err := f.run(cctx, args); err != nil {
		return err
	}
	return nil
}

// Duration 优先用 ffprobe；安装包里只带了 ffmpeg 时，退回解析 ffmpeg -i 的输出。
func (f *FileOps) Duration(ctx context.Context, in string) float64 {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	f.mu.Lock()
	probe := f.ffprobe
	f.mu.Unlock()
	if probe != "" {
		out, err := exec.CommandContext(cctx, probe,
			"-v", "error", "-show_entries", "format=duration",
			"-of", "default=noprint_wrappers=1:nokey=1", in).Output()
		if err == nil {
			if d, perr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); perr == nil {
				return d
			}
		}
	}
	ff := f.Path()
	if ff == "" {
		return 0
	}
	cmd := exec.CommandContext(cctx, ff, "-hide_banner", "-nostdin", "-i", in)
	var buf bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &buf, n: 8192}
	_ = cmd.Run() // 只给 -i 时 ffmpeg 必然以退出码 1 结束，信息全在 stderr
	return parseFFmpegDuration(buf.String())
}

// parseFFmpegDuration 从 ffmpeg -i 的 stderr 里抠 "Duration: HH:MM:SS.ss"。
func parseFFmpegDuration(s string) float64 {
	const key = "Duration: "
	i := strings.Index(s, key)
	if i < 0 {
		return 0
	}
	rest := s[i+len(key):]
	if len(rest) < 11 {
		return 0
	}
	parts := strings.Split(strings.TrimSpace(rest[:11]), ":")
	if len(parts) != 3 {
		return 0
	}
	h, err1 := strconv.ParseFloat(parts[0], 64)
	m, err2 := strconv.ParseFloat(parts[1], 64)
	sec, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0
	}
	return h*3600 + m*60 + sec
}

func (f *FileOps) run(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, f.Path(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, n: 4096}
	cmd.Stdout = nil
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		// 保留原始错误：调用方要靠 errors.Is(err, exec.ErrNotFound) 区分
		//「ffmpeg 没装」和「ffmpeg 跑失败」。
		return &cmdError{msg: strings.ReplaceAll(msg, "\n", " | "), err: err}
	}
	return nil
}

type cmdError struct {
	msg string
	err error
}

func (e *cmdError) Error() string { return e.msg }
func (e *cmdError) Unwrap() error { return e.err }

type limitedWriter struct {
	w *bytes.Buffer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.w.Len() < l.n {
		room := l.n - l.w.Len()
		if len(p) > room {
			l.w.Write(p[:room])
		} else {
			l.w.Write(p)
		}
	}
	return len(p), nil
}

func fileSize(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func dirOf(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return "."
	}
	if i == 0 {
		return "/"
	}
	return p[:i]
}
