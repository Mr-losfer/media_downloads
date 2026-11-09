// Package updater 负责「检查更新」和「一键更新」。
//
// 设计要点：
//  1. 元数据优先走 gh-proxy.com 代理，失败自动回落直连 GitHub —— 国内 NAS 场景
//     下 Git 链路经常抖，代理只是加速手段，不能成为单点。
//  2. 更新包只替换数据目录里的副本（<data>/bin/mediasuper），不动镜像里的原文件，
//     这样 Docker 容器里也能真的「一键更新」，重启后由 Handoff 自动接上。
//  3. 下载必须校验 SHA256，失败即放弃，绝不半截替换。
package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"mediasuper/internal/version"
)

// ProxyPrefix 是更新链路优先使用的 GitHub 代理。
const ProxyPrefix = "https://gh-proxy.com/"

// Asset 描述一个可下载的构建产物。
type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest 是仓库根目录 version.json 的结构，由打包脚本生成。
type Manifest struct {
	Version    string           `json:"version"`
	ReleasedAt string           `json:"released_at"`
	Notes      string           `json:"notes"`
	Assets     map[string]Asset `json:"assets"`
	FFmpeg     map[string]Asset `json:"ffmpeg,omitempty"`
}

// Status 是给前端看的检查结果。
type Status struct {
	Current     string `json:"current"`
	Latest      string `json:"latest"`
	HasUpdate   bool   `json:"has_update"`
	Notes       string `json:"notes"`
	ReleasedAt  string `json:"released_at"`
	Platform    string `json:"platform"`
	AssetName   string `json:"asset_name"`
	AssetSize   int64  `json:"asset_size"`
	DownloadURL string `json:"download_url"`
	SHA256      string `json:"sha256"`
	Source      string `json:"source"`
	CheckedAt   int64  `json:"checked_at"`
	CanAuto     bool   `json:"can_auto"`
	Error       string `json:"error,omitempty"`
}

// Manager 持有更新检查的缓存与网络客户端。
type Manager struct {
	dataDir string
	repo    string
	client  *http.Client

	mu      sync.Mutex
	cached  *Status
	checked time.Time

	ffMu      sync.Mutex
	ffState   FFmpegState
	ffRunning bool
}

// FFmpegState 是运行时 ffmpeg 组件的安装状态，供系统信息与 SSE 使用。
type FFmpegState struct {
	Status   string `json:"status"`   // idle | downloading | installing | ready | failed | unsupported
	Progress int    `json:"progress"` // 0-100
	Detail   string `json:"detail"`   // 人话说明或错误原因
	Path     string `json:"path"`     // 安装成功后的路径
	Source   string `json:"source"`   // 使用的下载源
}

func (u *Manager) ffmpegState() FFmpegState {
	u.ffMu.Lock()
	defer u.ffMu.Unlock()
	return u.ffState
}

func (u *Manager) setFFmpegState(st FFmpegState) {
	u.ffMu.Lock()
	u.ffState = st
	u.ffMu.Unlock()
}

// New 创建更新管理器。repo 形如 losfer778/media_downloads。
func New(dataDir, repo string) *Manager {
	if repo == "" {
		repo = version.Repo
	}
	return &Manager{
		dataDir: dataDir,
		repo:    repo,
		client: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				MaxIdleConns:        4,
				IdleConnTimeout:     30 * time.Second,
				TLSHandshakeTimeout: 15 * time.Second,
			},
		},
	}
}

// PlatformKey 返回 version.json 里资产的键名，如 linux-amd64 / linux-armv7。
func PlatformKey() string {
	arch := runtime.GOARCH
	switch arch {
	case "arm":
		arch = "armv" + strings.TrimSpace(os.Getenv("GOARM"))
		if arch == "armv" {
			arch = "armv7"
		}
	case "386":
		arch = "386"
	}
	return runtime.GOOS + "-" + arch
}

// IsNewer 粗略比较语义化版本，a 比 b 新返回 true。
func IsNewer(a, b string) bool {
	pa, pb := parseVer(a), parseVer(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parseVer(v string) [3]int {
	var out [3]int
	v = strings.TrimSpace(strings.TrimPrefix(v, "v"))
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	for i, part := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		n, _ := strconv.Atoi(strings.TrimSpace(part))
		out[i] = n
	}
	return out
}

func (u *Manager) rawURL(path string) string {
	return fmt.Sprintf("https://raw.githubusercontent.com/%s/main/%s", u.repo, path)
}

func (u *Manager) apiURL() string {
	return fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", u.repo)
}

// fetchFirst 依次尝试多个地址（代理在前、直连在后），返回第一个成功的响应。
func (u *Manager) fetchFirst(ctx context.Context, urls []string, limit int64) ([]byte, string, error) {
	var lastErr error
	for _, raw := range urls {
		body, err := u.fetch(ctx, raw, limit)
		if err == nil {
			host := "direct"
			if strings.HasPrefix(raw, ProxyPrefix) {
				host = "gh-proxy"
			}
			return body, host, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的更新源")
	}
	return nil, "", lastErr
}

func (u *Manager) fetch(ctx context.Context, raw string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.BinaryName+"/"+version.Version)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("%s 返回 %d", raw, resp.StatusCode)
	}
	if limit <= 0 {
		limit = 4 << 20
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, err
	}
	return b, nil
}

// checkManifest 只取 version.json，代理优先。
func (u *Manager) checkManifest(ctx context.Context) (*Manifest, string, error) {
	raw := u.rawURL("version.json")
	body, src, err := u.fetchFirst(ctx, []string{
		ProxyPrefix + raw,
		raw,
	}, 1<<20)
	if err != nil {
		return nil, "", err
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, src, fmt.Errorf("version.json 解析失败: %w", err)
	}
	if m.Version == "" {
		return nil, src, errors.New("version.json 缺少 version 字段")
	}
	return &m, src, nil
}

// checkReleaseAPI 兜底方案：直接读 GitHub Releases API。
func (u *Manager) checkReleaseAPI(ctx context.Context) (*Manifest, string, error) {
	api := u.apiURL()
	body, src, err := u.fetchFirst(ctx, []string{ProxyPrefix + api, api}, 1<<20)
	if err != nil {
		return nil, "", err
	}
	var rel struct {
		TagName     string `json:"tag_name"`
		PublishedAt string `json:"published_at"`
		Body        string `json:"body"`
		Assets      []struct {
			Name   string `json:"name"`
			Size   int64  `json:"size"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, src, fmt.Errorf("Release API 解析失败: %w", err)
	}
	m := &Manifest{
		Version:    strings.TrimPrefix(rel.TagName, "v"),
		ReleasedAt: rel.PublishedAt,
		Notes:      rel.Body,
		Assets:     map[string]Asset{},
	}
	for _, a := range rel.Assets {
		key := strings.TrimPrefix(a.Name, version.BinaryName+"-")
		if !strings.HasPrefix(a.Name, version.BinaryName+"-") {
			continue // 跳过压缩包 / fpk
		}
		m.Assets[key] = Asset{
			Name:   a.Name,
			URL:    a.URL,
			Size:   a.Size,
			SHA256: strings.TrimPrefix(a.Digest, "sha256:"),
		}
	}
	return m, src, nil
}

// Check 检查更新。force=false 时 5 分钟内复用缓存，避免前端反复点。
func (u *Manager) Check(ctx context.Context, force bool) *Status {
	u.mu.Lock()
	if !force && u.cached != nil && time.Since(u.checked) < 5*time.Minute {
		st := *u.cached
		u.mu.Unlock()
		return &st
	}
	u.mu.Unlock()

	st := &Status{
		Current:   version.Version,
		Platform:  PlatformKey(),
		CheckedAt: time.Now().UnixMilli(),
	}

	m, src, err := u.checkManifest(ctx)
	if err != nil {
		// version.json 拿不到时退回 Releases API（老版本仓库没有 version.json 也能用）
		m, src, err = u.checkReleaseAPI(ctx)
	}
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "404") {
			msg = "更新源暂无版本信息（仓库尚未发布 Release 或 version.json）"
		}
		st.Error = msg
		u.mu.Lock()
		u.cached = st
		u.checked = time.Now()
		u.mu.Unlock()
		return st
	}

	applyManifest(st, m, src)

	u.mu.Lock()
	u.cached = st
	u.checked = time.Now()
	u.mu.Unlock()
	return st
}

// downloadURLs 生成代理优先、直连兜底的下载地址列表。
// 本机/内网地址不应经过公网代理（否则必然 404），直接返回原地址。
func downloadURLs(raw string) []string {
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, ProxyPrefix) {
		return []string{raw}
	}
	if isLocalURL(raw) {
		return []string{raw}
	}
	return []string{ProxyPrefix + raw, raw}
}

func isLocalURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// Stage 下载并校验新版本，落到 <data>/bin 下，返回可执行文件路径。
func (u *Manager) Stage(ctx context.Context, st *Status, onProgress func(done, total int64)) (string, error) {
	if !st.CanAuto || st.DownloadURL == "" {
		return "", fmt.Errorf("当前平台 %s 没有可自动安装的更新包", st.Platform)
	}
	binDir := filepath.Join(u.dataDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}
	tmp := filepath.Join(binDir, version.BinaryName+".new")

	var (
		body []byte
		err  error
	)
	for _, raw := range downloadURLs(st.DownloadURL) {
		body, err = u.download(ctx, raw, st.AssetSize, onProgress)
		if err == nil {
			break
		}
	}
	if err != nil {
		return "", err
	}
	if len(body) == 0 {
		return "", errors.New("下载到的文件为空")
	}
	sum := sha256.Sum256(body)
	got := hex.EncodeToString(sum[:])
	if st.SHA256 != "" && !strings.EqualFold(got, st.SHA256) {
		return "", fmt.Errorf("校验失败：期望 %s，实际 %s", st.SHA256[:12], got[:12])
	}
	if err := os.WriteFile(tmp, body, 0o755); err != nil {
		return "", err
	}

	target := filepath.Join(binDir, version.BinaryName)
	if err := os.Rename(tmp, target); err != nil {
		return "", err
	}
	_ = os.Chmod(target, 0o755)
	if err := os.WriteFile(filepath.Join(binDir, "version"), []byte(st.Latest), 0o644); err != nil {
		return "", err
	}
	_ = os.WriteFile(filepath.Join(binDir, "sha256"), []byte(got), 0o644)
	log.Printf("更新已就绪: %s -> %s (%s)", version.Version, st.Latest, humanSize(int64(len(body))))
	return target, nil
}

func (u *Manager) download(ctx context.Context, raw string, wantSize int64, onProgress func(done, total int64)) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.BinaryName+"/"+version.Version)
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("下载 %s 返回 %d", raw, resp.StatusCode)
	}
	total := resp.ContentLength
	if total <= 0 {
		total = wantSize
	}
	var buf bytes.Buffer
	if total > 0 {
		buf.Grow(int(min64(total, 16<<20)))
	}
	chunk := make([]byte, 128<<10)
	var done int64
	for {
		n, rerr := resp.Body.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			done += int64(n)
			if onProgress != nil {
				onProgress(done, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	return buf.Bytes(), nil
}

// Restart 用新二进制替换当前进程（exec，PID 不变，容器里也安全）。
func Restart(binPath string) error {
	if err := os.Chmod(binPath, 0o755); err != nil {
		return fmt.Errorf("新文件不可执行: %w", err)
	}
	args := append([]string{binPath}, os.Args[1:]...)
	env := append(os.Environ(), "MS_NO_HANDOFF=1")
	return syscall.Exec(binPath, args, env)
}

// Handoff 在进程启动时调用：如果数据目录里有比镜像更新的二进制，直接切过去。
// 这样 Docker 里点过「一键更新」后，容器重启也能继续用新版。
func Handoff(dataDir string) {
	if os.Getenv("MS_NO_HANDOFF") == "1" {
		return
	}
	binDir := filepath.Join(dataDir, "bin")
	target := filepath.Join(binDir, version.BinaryName)
	if _, err := os.Stat(target); err != nil {
		return
	}
	raw, err := os.ReadFile(filepath.Join(binDir, "version"))
	if err != nil {
		return
	}
	stored := strings.TrimSpace(string(raw))
	if !IsNewer(stored, version.Version) {
		return
	}
	if self, err := os.Executable(); err == nil {
		if real, err2 := filepath.EvalSymlinks(self); err2 == nil {
			self = real
		}
		if real, err2 := filepath.EvalSymlinks(target); err2 == nil {
			if self == real {
				return
			}
		}
	}

	failFile := filepath.Join(binDir, "handoff.fail")
	fails := 0
	if b, err := os.ReadFile(failFile); err == nil {
		fails, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	if fails >= 3 {
		log.Printf("⚠ 新版 %s 连续启动失败 %d 次，已跳过自动接管（可在设置页重新升级或删除 %s）", stored, fails, binDir)
		return
	}
	_ = os.WriteFile(failFile, []byte(strconv.Itoa(fails+1)), 0o644)

	log.Printf("检测到数据目录中有更新版本 %s（镜像内置 %s），切换运行…", stored, version.Version)
	args := append([]string{target}, os.Args[1:]...)
	env := append(os.Environ(), "MS_NO_HANDOFF=1")
	if err := syscall.Exec(target, args, env); err != nil {
		log.Printf("切换新版本失败，继续使用内置版本: %v", err)
	}
}

// MarkHealthy 服务成功启动后调用，清掉 Handoff 的失败计数。
func MarkHealthy(dataDir string) {
	_ = os.Remove(filepath.Join(dataDir, "bin", "handoff.fail"))
}

// Staged 返回数据目录里已就绪的更新版本（没有则空）。
func Staged(dataDir string) string {
	b, err := os.ReadFile(filepath.Join(dataDir, "bin", "version"))
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(b))
	if !IsNewer(v, version.Version) {
		return ""
	}
	return v
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// applyManifest 把清单里的版本信息填进 Status。
func applyManifest(st *Status, m *Manifest, src string) {
	st.Latest = m.Version
	st.Notes = m.Notes
	st.ReleasedAt = m.ReleasedAt
	st.Source = src
	st.HasUpdate = IsNewer(m.Version, version.Version)
	if a, ok := m.Assets[st.Platform]; ok {
		st.AssetName = a.Name
		st.DownloadURL = a.URL
		st.SHA256 = a.SHA256
		st.AssetSize = a.Size
		st.CanAuto = true
	}
}

// CheckAPIOnly 跳过 version.json，直接用 Releases API 取元数据。
// 用于 version.json 与实际附件不同步（例如重复发布覆盖了产物）时纠正摘要。
func (u *Manager) CheckAPIOnly(ctx context.Context) *Status {
	st := &Status{
		Current:   version.Version,
		Platform:  PlatformKey(),
		CheckedAt: time.Now().UnixMilli(),
	}
	m, src, err := u.checkReleaseAPI(ctx)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	applyManifest(st, m, src+"-api")
	return st
}
