package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// FileOps 封装 ffmpeg / ffprobe。全部走 -c copy 重封装，解码转码只做兜底，
// 这是低配 NAS 能扛住 m3u8 的唯一正确姿势。
type FileOps struct {
	ffmpeg  string
	ffprobe string
}

func newFileOps(ffmpegPath string) *FileOps {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	probe := "ffprobe"
	if strings.Contains(ffmpegPath, "/") {
		probe = strings.TrimSuffix(ffmpegPath, "ffmpeg") + "ffprobe"
	}
	return &FileOps{ffmpeg: ffmpegPath, ffprobe: probe}
}

func (f *FileOps) Available() (string, bool) {
	// 低配机（J1800）冷启动首次执行 ffmpeg 可能超过 8 秒，给足时间避免误报
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, f.ffmpeg, "-version").Output()
	if err != nil {
		// 探测超时/失败但可执行文件确实存在时，按可用处理，真正调用时再兜底
		if st, serr := os.Stat(f.ffmpeg); serr == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return "已就位（-version 未及时响应）", true
		}
		return "", false
	}
	line := strings.SplitN(string(out), "\n", 2)[0]
	return strings.TrimSpace(line), true
}

// Convert 先尝试无损重封装，失败再按需转码。返回成品字节数。
func (f *FileOps) Convert(ctx context.Context, in, out string, st Settings) (int64, error) {
	if err := os.MkdirAll(dirOf(out), 0o755); err != nil {
		return 0, err
	}

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
		out,
	}
	if err := f.run(cctx, args); err == nil {
		if sz := fileSize(out); sz > 0 {
			return sz, nil
		}
	} else {
		os.Remove(out)
	}

	if !st.AllowTranscode {
		return 0, fmt.Errorf("无损重封装失败（编码不兼容），且未开启转码兜底")
	}

	os.Remove(out)
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
		out,
	}
	if err := f.run(cctx, args); err != nil {
		os.Remove(out)
		return 0, fmt.Errorf("转码也失败了: %w", err)
	}
	sz := fileSize(out)
	if sz == 0 {
		return 0, fmt.Errorf("ffmpeg 输出为空")
	}
	return sz, nil
}

// Thumb 抽一帧做封面，用完即走，失败不影响主流程。
func (f *FileOps) Thumb(ctx context.Context, in, out string) error {
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

func (f *FileOps) Duration(ctx context.Context, in string) float64 {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, f.ffprobe,
		"-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", in).Output()
	if err != nil {
		return 0
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return d
}

func (f *FileOps) run(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, f.ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, n: 4096}
	cmd.Stdout = nil
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", strings.ReplaceAll(msg, "\n", " | "))
	}
	return nil
}

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
