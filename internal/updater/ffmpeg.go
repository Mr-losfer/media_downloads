// Package updater 的 ffmpeg 组件自动补齐。
//
// 背景：fpk 里塞一个 168MB 的静态 ffmpeg 会让安装包从 3MB 涨到 72MB，
// 旧版 fnOS 手动安装上传超时被截断后，排在 app.tgz 后面的 cmd/main 没落盘，
// 于是报「缺少生命周期脚本，安装包可能不完整」。因此 fpk 只放主程序，
// ffmpeg 作为独立压缩包随 Release 发布，首次启动在后台自动下载到 <data>/bin。
//
// 设计要点：
//  1. 只尝试一次，失败即标记状态，绝不重复下载（用户曾抱怨「重新下载三次」）。
//  2. gh-proxy 优先、GitHub 直连兜底，下载后必须校验 SHA256。
//  3. gzip 流式解压、临时文件 + 原子 rename，避免半截文件被当成 ffmpeg。
//  4. 全程不阻塞启动；状态通过 /api/system/info 与 SSE 暴露给前端。
package updater

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mediasuper/internal/version"
)

// ffmpegAssetName 返回当前平台需要的组件文件名；空串表示没有预编译组件。
// fpk 只出 x86(amd64) 与 arm(arm64) 两档，其他架构要么 Docker 镜像自带，
// 要么由用户在设置里指定系统 ffmpeg。
func ffmpegAssetName() string {
	switch PlatformKey() {
	case "linux-amd64":
		return "ffmpeg-linux-amd64.gz"
	case "linux-arm64":
		return "ffmpeg-linux-arm64.gz"
	default:
		return ""
	}
}

// FFmpegAssetAvailable 报告当前平台是否有可自动安装的预编译组件。
func FFmpegAssetAvailable() bool {
	return ffmpegAssetName() != ""
}

// FFmpegState 返回组件安装状态（供 /api/system/info）。
func (u *Manager) FFmpegState() FFmpegState {
	return u.ffmpegState()
}

// ffmpegTarget 是组件最终落盘位置。
func (u *Manager) ffmpegTarget() string {
	return filepath.Join(u.dataDir, "bin", "ffmpeg")
}

// FFmpegInstalled 报告组件是否已经落盘可执行。
func (u *Manager) FFmpegInstalled() bool {
	p := u.ffmpegTarget()
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

// EnsureFFmpeg 在后台补齐 ffmpeg 组件。needInstall 为 false 时直接返回 ready
//（系统里已经有可用的 ffmpeg，不必下载）。
//
// onProgress 用于把状态变化广播给前端；可以为空。
// 返回最终状态，err 非空只在确实需要补齐但失败时出现。
func (u *Manager) EnsureFFmpeg(ctx context.Context, needInstall bool, onProgress func(FFmpegState)) FFmpegState {
	report := func(st FFmpegState) FFmpegState {
		u.setFFmpegState(st)
		if onProgress != nil {
			onProgress(st)
		}
		return st
	}

	if !needInstall {
		return report(FFmpegState{Status: "ready", Progress: 100, Detail: "已检测到可用的 ffmpeg，无需安装组件"})
	}
	if u.FFmpegInstalled() {
		return report(FFmpegState{Status: "ready", Progress: 100, Detail: "组件已就绪", Path: u.ffmpegTarget()})
	}

	name := ffmpegAssetName()
	if name == "" {
		return report(FFmpegState{
			Status: "unsupported",
			Detail: "当前架构暂无预编译组件，请手动安装 ffmpeg 或在「设置 → 下载 → ffmpeg 路径」填写绝对路径",
		})
	}

	// 同一时间只允许一次，避免多次触发并发下载
	u.ffMu.Lock()
	if u.ffRunning {
		u.ffMu.Unlock()
		return u.ffmpegState()
	}
	u.ffRunning = true
	u.ffMu.Unlock()
	defer func() {
		u.ffMu.Lock()
		u.ffRunning = false
		u.ffMu.Unlock()
	}()

	// 上限 15 分钟：国内 gh-proxy 下 60MB 可能较慢，但绝不能无限等
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	asset, src, err := u.ffmpegAsset(ctx, name)
	if err != nil {
		log.Printf("ffmpeg 组件信息获取失败: %v", err)
		return report(FFmpegState{Status: "failed", Detail: "获取组件信息失败：" + err.Error()})
	}

	log.Printf("正在补齐 ffmpeg 组件 %s（%.1f MB，来源 %s）", name, float64(asset.Size)/1048576, src)
	report(FFmpegState{Status: "downloading", Progress: 0, Detail: "正在下载 ffmpeg 组件…", Source: src})

	_, err = u.installFFmpeg(ctx, asset, func(done, total int64) {
		pct := 0
		if total > 0 {
			pct = int(done * 100 / total)
			if pct > 100 {
				pct = 100
			}
		}
		report(FFmpegState{
			Status: "downloading", Progress: pct, Source: src,
			Detail: fmt.Sprintf("正在下载 ffmpeg 组件… %s / %s", humanSize(done), humanSize(total)),
		})
	})
	if err != nil {
		log.Printf("ffmpeg 组件安装失败: %v", err)
		return report(FFmpegState{Status: "failed", Detail: "组件安装失败：" + err.Error(), Source: src})
	}

	target := u.ffmpegTarget()
	log.Printf("ffmpeg 组件已就绪: %s", target)
	return report(FFmpegState{Status: "ready", Progress: 100, Detail: "组件安装完成，m3u8 可正常封装为 mp4", Path: target, Source: src})
}

// ffmpegAsset 优先从 version.json 取带 SHA256 的组件信息；拿不到时按固定
// Release URL 兜底（此时无校验和，installFFmpeg 会改用 ELF 头 + 可执行校验）。
func (u *Manager) ffmpegAsset(ctx context.Context, name string) (Asset, string, error) {
	if m, src, err := u.checkManifest(ctx); err == nil && m.FFmpeg != nil {
		if a, ok := m.FFmpeg[PlatformKey()]; ok && a.URL != "" {
			return a, src, nil
		}
	}
	// 兜底：当前版本 Release 里的同名附件
	raw := fmt.Sprintf("https://github.com/%s/releases/download/v%s/%s",
		version.Repo, version.Version, name)
	return Asset{Name: name, URL: raw}, "release", nil
}

// installFFmpeg 下载 gzip 压缩包 → 校验 → 流式解压 → 原子落盘。
func (u *Manager) installFFmpeg(ctx context.Context, a Asset, progress func(done, total int64)) (string, error) {
	binDir := filepath.Join(u.dataDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}

	var lastErr error
	for _, raw := range downloadURLs(a.URL) {
		lastErr = u.fetchAndUnpack(ctx, raw, a, binDir, progress)
		if lastErr == nil {
			return u.ffmpegTarget(), nil
		}
		log.Printf("ffmpeg 组件下载源 %s 失败: %v", raw, lastErr)
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的下载地址")
	}
	return "", lastErr
}

func (u *Manager) fetchAndUnpack(ctx context.Context, raw string, a Asset, binDir string, progress func(done, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", version.BinaryName+"/"+version.Version)
	// 组件 60MB 左右，更新检查用的 60s 客户端不够，这里单独放宽到 15 分钟
	// （上层 ctx 已有 15 分钟上限，双保险）。
	client := &http.Client{
		Timeout: 15 * time.Minute,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			TLSHandshakeTimeout: 15 * time.Second,
			IdleConnTimeout:     30 * time.Second,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("返回 %d", resp.StatusCode)
	}

	total := resp.ContentLength
	if total <= 0 {
		total = a.Size
	}

	// 先落到 .gz 临时文件，边下边算 SHA256，避免把 60MB 全塞进内存
	gzPath := filepath.Join(binDir, "ffmpeg.download.gz")
	gzFile, err := os.OpenFile(gzPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	hash := sha256.New()
	src := io.TeeReader(resp.Body, hash)
	chunk := make([]byte, 128<<10)
	var done int64
	for {
		n, rerr := src.Read(chunk)
		if n > 0 {
			if _, werr := gzFile.Write(chunk[:n]); werr != nil {
				gzFile.Close()
				os.Remove(gzPath)
				return werr
			}
			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			gzFile.Close()
			os.Remove(gzPath)
			return rerr
		}
	}
	if err := gzFile.Close(); err != nil {
		os.Remove(gzPath)
		return err
	}
	defer os.Remove(gzPath)

	if a.SHA256 != "" {
		got := hex.EncodeToString(hash.Sum(nil))
		if !strings.EqualFold(got, a.SHA256) {
			return fmt.Errorf("SHA256 校验失败：期望 %s，实际 %s", a.SHA256[:12], got[:12])
		}
	}

	// 流式解压到临时文件，检查 ELF 头后才算成功
	f, err := os.Open(gzPath)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("组件不是有效的 gzip 包: %w", err)
	}
	defer zr.Close()

	tmp := filepath.Join(binDir, "ffmpeg.new")
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	buf := make([]byte, 128<<10)
	written := int64(0)
	for {
		n, rerr := zr.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				os.Remove(tmp)
				return werr
			}
			written += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(tmp)
			return fmt.Errorf("解压失败: %w", rerr)
		}
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if written < 1<<20 {
		os.Remove(tmp)
		return errors.New("解压后的文件异常小，可能不是 ffmpeg")
	}

	// 校验 ELF 头，避免把 HTML 错误页之类当成可执行文件装进去
	fh, err := os.Open(tmp)
	if err != nil {
		os.Remove(tmp)
		return err
	}
	head := make([]byte, 4)
	_, _ = io.ReadFull(fh, head)
	fh.Close()
	if string(head) != "\x7fELF" {
		os.Remove(tmp)
		return errors.New("组件内容不是可执行文件")
	}

	target := u.ffmpegTarget()
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}
	_ = os.Chmod(target, 0o755)
	return nil
}
