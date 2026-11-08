package main

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

func nowMs() int64 { return time.Now().UnixMilli() }

func joinPath(dir, name string) string { return filepath.Join(dir, name) }

// newID 生成 16 位十六进制短 ID，碰撞概率足够低且对日志友好。
func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func humanBytes(n int64) string {
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

// safeName 清洗用户/远端给出的文件名：去掉路径分隔符、控制字符和 Windows 保留字符。
func safeName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', 0:
			return '_'
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.Trim(s, " .")
	// 防止纯 ".." 之类的相对路径
	s = strings.ReplaceAll(s, "..", "_")
	if utf8.RuneCountInString(s) > 120 {
		r := []rune(s)
		s = string(r[:120])
	}
	return s
}

// extFromCT 根据 Content-Type 推断容器后缀。
func extFromCT(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	switch ct {
	case "video/mp4", "video/x-m4v", "application/mp4":
		return "mp4"
	case "video/webm":
		return "webm"
	case "video/x-flv", "video/flv":
		return "flv"
	case "video/mp2t", "video/mpegts", "video/mpeg":
		return "ts"
	case "video/quicktime":
		return "mov"
	case "video/x-matroska":
		return "mkv"
	case "audio/mpeg":
		return "mp3"
	case "audio/mp4", "audio/x-m4a":
		return "m4a"
	case "application/vnd.apple.mpegurl", "application/x-mpegurl":
		return "m3u8"
	}
	return ""
}

// extFromURL 从 URL 路径里取后缀，取不到就返回空。
func extFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(u.Path), "."))
	switch ext {
	case "mp4", "m4v", "webm", "flv", "ts", "mov", "mkv", "mp3", "m4a", "avi", "wmv", "mpg", "mpeg":
		return ext
	}
	return ""
}

func isHLSBody(b []byte) bool {
	return strings.Contains(string(b[:min(len(b), 512)]), "#EXTM3U")
}

// uniquePath 同名文件自动加 -1 / -2 后缀，绝不覆盖已有文件。
func uniquePath(dir, base, ext string) string {
	base = safeName(base)
	if base == "" {
		base = "video"
	}
	name := base
	if ext != "" {
		name += "." + ext
	}
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return p
	}
	for i := 1; i < 10000; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
		if ext != "" {
			name += "." + ext
		}
		p = filepath.Join(dir, name)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d", base, time.Now().UnixNano()))
}

// newHTTPClient 构建低配友好的 HTTP 客户端：连接池按并发数收紧，避免句柄爆炸。
func newHTTPClient(st Settings, conns int) *http.Client {
	perHost := conns * 2
	if perHost < 8 {
		perHost = 8
	}
	if perHost > 64 {
		perHost = 64
	}
	tr := &http.Transport{
		Proxy:                 nil,
		MaxIdleConns:          perHost * 2,
		MaxIdleConnsPerHost:   perHost,
		MaxConnsPerHost:       0,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
		ForceAttemptHTTP2:     true,
		DisableCompression:    true,
		WriteBufferSize:       32 << 10,
		ReadBufferSize:        32 << 10,
	}
	if st.Proxy != "" {
		if pu, err := url.Parse(st.Proxy); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	if st.InsecureTLS {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &http.Client{
		Transport: tr,
		Timeout:   0, // 大文件不能设整体超时，靠 ctx 与 ResponseHeaderTimeout 控制
	}
}

// applyHeaders 写入 UA 与任务自带的 Referer/Cookie/Origin 等防盗链头。
func applyHeaders(req *http.Request, st Settings, hdrs map[string]string) {
	ua := st.UserAgent
	if hdrs != nil {
		for k, v := range hdrs {
			switch strings.ToLower(k) {
			case "user-agent":
				ua = v
			case "host", "content-length", "connection":
				// 不允许覆盖
			default:
				req.Header.Set(k, v)
			}
		}
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")
}

// limiter 是一个全局令牌桶限速器，rate 单位 B/s，0 表示不限速。
type limiter struct {
	mu     sync.Mutex
	rate   int64
	tokens float64
	last   time.Time
}

func newLimiter(rate int64) *limiter {
	return &limiter{rate: rate, tokens: float64(rate), last: time.Now()}
}

// wait 在需要时阻塞，保证长时段平均速率不超过 rate。
func (l *limiter) wait(n int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	rate := l.rate
	if rate <= 0 {
		l.mu.Unlock()
		return
	}
	now := time.Now()
	elapsed := now.Sub(l.last).Seconds()
	l.last = now
	l.tokens += elapsed * float64(rate)
	burst := float64(rate)
	if l.tokens > burst {
		l.tokens = burst
	}
	l.tokens -= float64(n)
	need := -l.tokens
	l.mu.Unlock()
	if need > 0 {
		time.Sleep(time.Duration(need / float64(rate) * float64(time.Second)))
	}
}

func (l *limiter) setRate(rate int64) {
	l.mu.Lock()
	l.rate = rate
	l.tokens = float64(rate)
	l.last = time.Now()
	l.mu.Unlock()
}

// copyWithProgress 边拷边回调，返回本次拷贝字节数。
func copyWithProgress(dst io.Writer, src io.Reader, buf []byte, onWrite func(int)) (int64, error) {
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			off := 0
			for off < n {
				w, werr := dst.Write(buf[off:n])
				if werr != nil {
					return total, werr
				}
				off += w
			}
			total += int64(n)
			if onWrite != nil {
				onWrite(n)
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
