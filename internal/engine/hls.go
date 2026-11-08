package engine

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type hlsKey struct {
	Method string
	URI    string
	IV     []byte
}

type hlsSeg struct {
	URI      string
	Key      *hlsKey
	Offset   int64
	Length   int64
	HasRange bool
	Duration float64
}

type hlsVariant struct {
	URI       string
	Bandwidth int
	Res       string
}

type hlsPlaylist struct {
	Master   bool
	Variants []hlsVariant
	Segments []*hlsSeg
	InitURI  string
	EndList  bool
	Duration float64
}

// parseAttrs 解析 HLS 属性列表：KEY=VALUE,KEY="VA,LUE"。
// 注意引号内的逗号不能当分隔符，很多站点会踩这个坑。
func parseAttrs(s string) map[string]string {
	out := make(map[string]string, 8)
	var k, v strings.Builder
	var inVal, inQuote, has bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case !inVal && c == '=':
			inVal, has = true, true
		case inVal && c == '"':
			inQuote = !inQuote
		case !inQuote && c == ',':
			if has {
				out[strings.TrimSpace(k.String())] = strings.TrimSpace(v.String())
			}
			k.Reset()
			v.Reset()
			inVal, has = false, false
		default:
			if inVal {
				v.WriteByte(c)
			} else {
				k.WriteByte(c)
			}
		}
	}
	if has {
		out[strings.TrimSpace(k.String())] = strings.TrimSpace(v.String())
	}
	return out
}

func resolveURI(base *url.URL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	if base == nil {
		return r.String()
	}
	return base.ResolveReference(r).String()
}

// parseHLS 解析 master 或 media playlist。支持 EXT-X-KEY / EXT-X-MAP /
// EXT-X-BYTERANGE / EXT-X-MEDIA-SEQUENCE 这些实际会遇到的东西。
func parseHLS(text string, base *url.URL) (*hlsPlaylist, error) {
	if !strings.Contains(text, "#EXTM3U") {
		return nil, errors.New("不是合法的 m3u8 内容")
	}
	pl := &hlsPlaylist{}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	var pendingVariant *hlsVariant
	var pendingDur float64
	var pendingRange bool
	var rangeLen, rangeOff int64
	var curKey *hlsKey
	lastEnd := map[string]int64{}

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-KEY:"))
			method := strings.ToUpper(attrs["METHOD"])
			if method == "" || method == "NONE" {
				curKey = nil
				continue
			}
			k := &hlsKey{Method: method, URI: resolveURI(base, attrs["URI"])}
			if ivs := attrs["IV"]; ivs != "" {
				ivs = strings.TrimPrefix(strings.TrimPrefix(ivs, "0x"), "0X")
				if b, err := hex.DecodeString(ivs); err == nil {
					k.IV = b
				}
			}
			curKey = k

		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-MAP:"))
			pl.InitURI = resolveURI(base, attrs["URI"])

		case strings.HasPrefix(line, "#EXT-X-BYTERANGE:"):
			spec := strings.TrimPrefix(line, "#EXT-X-BYTERANGE:")
			pendingRange = true
			rangeLen, rangeOff = 0, -1
			if i := strings.IndexByte(spec, '@'); i >= 0 {
				rangeLen, _ = strconv.ParseInt(strings.TrimSpace(spec[:i]), 10, 64)
				rangeOff, _ = strconv.ParseInt(strings.TrimSpace(spec[i+1:]), 10, 64)
			} else {
				rangeLen, _ = strconv.ParseInt(strings.TrimSpace(spec), 10, 64)
			}

		case strings.HasPrefix(line, "#EXTINF:"):
			spec := strings.TrimPrefix(line, "#EXTINF:")
			if i := strings.IndexByte(spec, ','); i >= 0 {
				spec = spec[:i]
			}
			if f, err := strconv.ParseFloat(strings.TrimSpace(spec), 64); err == nil {
				pendingDur = f
				pl.Duration += f
			}

		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
			v := &hlsVariant{Res: attrs["RESOLUTION"]}
			if bw, err := strconv.Atoi(attrs["BANDWIDTH"]); err == nil {
				v.Bandwidth = bw
			}
			pendingVariant = v
			pl.Master = true

		case strings.HasPrefix(line, "#EXT-X-ENDLIST"):
			pl.EndList = true

		case strings.HasPrefix(line, "#"):
			// 其它标签忽略

		default:
			abs := resolveURI(base, line)
			if pendingVariant != nil {
				pendingVariant.URI = abs
				pl.Variants = append(pl.Variants, *pendingVariant)
				pendingVariant = nil
				continue
			}
			seg := &hlsSeg{URI: abs, Key: curKey, Duration: pendingDur}
			if pendingRange && rangeLen > 0 {
				seg.HasRange = true
				seg.Length = rangeLen
				if rangeOff >= 0 {
					seg.Offset = rangeOff
				} else {
					seg.Offset = lastEnd[abs]
				}
				lastEnd[abs] = seg.Offset + rangeLen
			}
			pl.Segments = append(pl.Segments, seg)
			pendingDur = 0
			pendingRange = false
		}
	}
	return pl, nil
}

func pickVariant(vs []hlsVariant, quality string) *hlsVariant {
	if len(vs) == 0 {
		return nil
	}
	best := &vs[0]
	for i := range vs {
		v := &vs[i]
		if v.URI == "" {
			continue
		}
		if quality == "worst" {
			if best.URI == "" || v.Bandwidth < best.Bandwidth {
				best = v
			}
		} else if v.Bandwidth > best.Bandwidth || best.URI == "" {
			best = v
		}
	}
	if best.URI == "" {
		return nil
	}
	return best
}

// fetchPlaylist 递归解析 master → media，最多 3 层。
func (d *dlCtx) fetchPlaylist(ctx context.Context, rawurl string) (*hlsPlaylist, *url.URL, error) {
	cur := rawurl
	for depth := 0; depth < 3; depth++ {
		body, finalURL, err := d.getBytes(ctx, cur, 8<<20)
		if err != nil {
			return nil, nil, fmt.Errorf("拉取播放列表失败: %w", err)
		}
		base, err := url.Parse(finalURL)
		if err != nil {
			return nil, nil, err
		}
		pl, err := parseHLS(string(body), base)
		if err != nil {
			return nil, nil, err
		}
		if pl.Master && len(pl.Variants) > 0 {
			v := pickVariant(pl.Variants, d.st.HLSQuality)
			if v == nil {
				return nil, nil, errors.New("master playlist 里没有可用的清晰度")
			}
			cur = v.URI
			continue
		}
		if len(pl.Segments) == 0 {
			return nil, nil, errors.New("播放列表里没有分片（可能是加密直播或需要鉴权）")
		}
		return pl, base, nil
	}
	return nil, nil, errors.New("m3u8 嵌套层级过深")
}

// getBytes 带重试的小文件读取（播放列表、密钥）。
func (d *dlCtx) getBytes(ctx context.Context, rawurl string, limit int64) ([]byte, string, error) {
	var body []byte
	var final string
	err := d.withRetry(ctx, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
		if err != nil {
			return err
		}
		applyHeaders(req, d.st, d.task.Headers)
		resp, err := d.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
		if err != nil {
			return err
		}
		body = b
		final = resp.Request.URL.String()
		return nil
	})
	return body, final, err
}

func (d *dlCtx) fetchKey(ctx context.Context, k *hlsKey) ([]byte, error) {
	if k == nil || k.URI == "" {
		return nil, nil
	}
	d.keyMu.Lock()
	cached, ok := d.keys[k.URI]
	d.keyMu.Unlock()
	if ok {
		return cached, nil
	}
	b, _, err := d.getBytes(ctx, k.URI, 1<<10)
	if err != nil {
		return nil, fmt.Errorf("获取解密密钥失败: %w", err)
	}
	if len(b) != 16 {
		return nil, fmt.Errorf("密钥长度异常: %d 字节", len(b))
	}
	d.keyMu.Lock()
	d.keys[k.URI] = b
	d.keyMu.Unlock()
	return b, nil
}

// ivFor 按 HLS 规范：没有显式 IV 时用媒体序号(media sequence)大端 16 字节。
func ivFor(k *hlsKey, seq int) []byte {
	if len(k.IV) == 16 {
		return k.IV
	}
	iv := make([]byte, 16)
	v := uint64(seq)
	for i := 15; i >= 8; i-- {
		iv[i] = byte(v)
		v >>= 8
	}
	return iv
}

// copyDecrypt 流式 AES-128-CBC 解密：只保留最后一块用于去 PKCS7 填充，
// 内存占用固定 64KB，绝不因为分片大而爆内存。
func copyDecrypt(dst io.Writer, src io.Reader, key, iv []byte) (int64, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return 0, err
	}
	if len(iv) != 16 {
		return 0, errors.New("IV 长度不是 16")
	}
	mode := cipher.NewCBCDecrypter(block, iv)
	const bs = aes.BlockSize
	buf := make([]byte, 64*1024)
	var pending []byte
	var written int64

	for {
		n, rerr := io.ReadFull(src, buf)
		if n > 0 {
			if n%bs != 0 {
				return written, fmt.Errorf("密文长度 %d 不是 %d 的整数倍", n, bs)
			}
			mode.CryptBlocks(buf[:n], buf[:n])
			chunk := buf[:n]
			if len(pending) > 0 {
				if _, err := dst.Write(pending); err != nil {
					return written, err
				}
				written += int64(len(pending))
			}
			if n > bs {
				if _, err := dst.Write(chunk[:n-bs]); err != nil {
					return written, err
				}
				written += int64(n - bs)
			}
			pending = append(pending[:0], chunk[n-bs:]...)
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return written, rerr
		}
	}
	if len(pending) == bs {
		pad := int(pending[bs-1])
		if pad >= 1 && pad <= bs {
			pending = pending[:bs-pad]
		}
		if len(pending) > 0 {
			if _, err := dst.Write(pending); err != nil {
				return written, err
			}
			written += int64(len(pending))
		}
	}
	return written, nil
}

// trimSegments 按设置丢掉片头 / 片尾的分片，返回保留的切片和实际丢掉的片数。
// 至少保留 1 片：N 填得比总片数还大时不会把整个任务变成空文件。
func trimSegments(segs []*hlsSeg, st Settings) ([]*hlsSeg, int, int) {
	if !st.TrimEnabled || len(segs) == 0 {
		return segs, 0, 0
	}
	head, tail := st.TrimHead, st.TrimTail
	if head < 0 {
		head = 0
	}
	if tail < 0 {
		tail = 0
	}
	if head > len(segs)-1 {
		head = len(segs) - 1
	}
	if head+tail > len(segs)-1 {
		tail = len(segs) - head - 1
	}
	if head == 0 && tail == 0 {
		return segs, 0, 0
	}
	return segs[head : len(segs)-tail], head, tail
}

// playlistDuration 优先按保留的分片累加，保证裁剪后展示的时长不是原片时长。
func playlistDuration(segs []*hlsSeg, pl *hlsPlaylist) float64 {
	if len(segs) == len(pl.Segments) {
		return pl.Duration
	}
	var d float64
	for _, s := range segs {
		d += s.Duration
	}
	return d
}

// downloadHLS 下载全部分片 → 顺序拼接 → 交给 ffmpeg 重封装。
func (d *dlCtx) downloadHLS(ctx context.Context, rawurl string) (string, int64, error) {
	pl, base, err := d.fetchPlaylist(ctx, rawurl)
	if err != nil {
		return "", 0, err
	}
	_ = base
	if len(pl.Segments) == 0 {
		return "", 0, errors.New("播放列表为空")
	}

	segs, head, tail := trimSegments(pl.Segments, d.st)
	if head > 0 || tail > 0 {
		log.Printf("[%s] 去掉片头 %d 片、片尾 %d 片：%d → %d 片",
			d.task.ID, head, tail, len(pl.Segments), len(segs))
	}

	d.task.Kind = "hls"
	d.task.Format = "mp4"
	d.task.SegTotal = len(segs)
	d.task.Duration = playlistDuration(segs, pl)
	d.j.segTotal.Store(int64(len(segs)))
	d.setFileName("mp4", "")

	conns := clampInt(d.st.SegConns, 1, 32)
	if len(segs) < conns {
		conns = len(segs)
	}
	d.task.Conns = conns
	d.m.progressUpdate(d.j, d.task)

	segPaths := make([]string, len(segs))
	var mu sync.Mutex
	sem := make(chan struct{}, conns)
	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for i, seg := range segs {
		select {
		case <-ctx.Done():
		case sem <- struct{}{}:
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int, seg *hlsSeg) {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			p := filepath.Join(d.tmpDir, fmt.Sprintf("%06d.seg", i))
			if err := d.fetchSegment(ctx, seg, i, p); err != nil {
				select {
				case errCh <- fmt.Errorf("分片 %d/%d 失败: %w", i+1, len(segs), err):
					cancel()
				default:
				}
				return
			}
			mu.Lock()
			segPaths[i] = p
			mu.Unlock()
			d.j.segDone.Add(1)
			d.checkProgress()
		}(i, seg)
	}
	wg.Wait()

	select {
	case err := <-errCh:
		return "", 0, err
	default:
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	for i, p := range segPaths {
		if p == "" {
			return "", 0, fmt.Errorf("分片 %d 缺失，下载被中断", i+1)
		}
	}

	// 拼接：fMP4 需要 init 段在前
	merged := filepath.Join(d.tmpDir, "merged.bin")
	if err := d.concat(merged, pl, segPaths); err != nil {
		return "", 0, err
	}
	for _, p := range segPaths {
		os.Remove(p)
	}

	out := joinPath(d.task.SaveDir, d.task.FileName)
	if err := ensureDir(d.task.SaveDir); err != nil {
		return "", 0, err
	}
	d.m.setPhase(d.j, StatusMerging)
	size, err := d.m.ff.Convert(ctx, merged, out, d.st)
	if err != nil {
		return "", 0, err
	}
	os.Chmod(out, 0o644)
	if !d.st.KeepTemp {
		os.Remove(merged)
	}
	return out, size, nil
}

func (d *dlCtx) concat(dst string, pl *hlsPlaylist, segs []string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 256<<10)
	defer w.Flush()
	buf := make([]byte, 256<<10)

	if pl.InitURI != "" {
		initPath := filepath.Join(d.tmpDir, "init.seg")
		if err := d.fetchRaw(context.Background(), pl.InitURI, initPath); err != nil {
			return fmt.Errorf("下载 init 段失败: %w", err)
		}
		if err := appendFile(w, initPath, buf); err != nil {
			return err
		}
		os.Remove(initPath)
	}
	for i, p := range segs {
		if err := appendFile(w, p, buf); err != nil {
			return fmt.Errorf("拼接分片 %d 失败: %w", i+1, err)
		}
		// 边拼边删：峰值临时占用只有一份视频，而不是两份
		if !d.st.KeepTemp {
			os.Remove(p)
		}
	}
	return nil
}

func (d *dlCtx) fetchRaw(ctx context.Context, rawurl, dst string) error {
	return d.withRetry(ctx, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
		if err != nil {
			return err
		}
		applyHeaders(req, d.st, d.task.Headers)
		resp, err := d.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		f, err := os.Create(dst)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(f, resp.Body)
		return err
	})
}

// fetchSegment 下载单个分片（支持 BYTERANGE + AES-128），一次流式落盘。
func (d *dlCtx) fetchSegment(ctx context.Context, seg *hlsSeg, seq int, dst string) error {
	var key, iv []byte
	if seg.Key != nil && strings.EqualFold(seg.Key.Method, "AES-128") {
		k, err := d.fetchKey(ctx, seg.Key)
		if err != nil {
			return err
		}
		key = k
		iv = ivFor(seg.Key, seq)
	}
	return d.withRetry(ctx, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, seg.URI, nil)
		if err != nil {
			return err
		}
		if seg.HasRange {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", seg.Offset, seg.Offset+seg.Length-1))
		}
		applyHeaders(req, d.st, d.task.Headers)
		resp, err := d.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		f, err := os.Create(dst)
		if err != nil {
			return err
		}
		defer f.Close()
		var n int64
		if len(key) == 16 {
			n, err = copyDecrypt(f, resp.Body, key, iv)
		} else {
			n, err = io.Copy(f, resp.Body)
		}
		if err != nil {
			return err
		}
		if seg.HasRange && seg.Length > 0 && len(key) == 0 && n != seg.Length {
			return fmt.Errorf("字节区间长度不符: 期望 %d 实得 %d", seg.Length, n)
		}
		d.j.downloaded.Add(n)
		return nil
	})
}

func appendFile(w io.Writer, path string, buf []byte) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = copyWithProgress(w, f, buf, nil)
	return err
}

// probeHLS 判断一个 URL 到底是 m3u8 还是普通文件。
func (d *dlCtx) probeHLS(ctx context.Context, rawurl string) (bool, []byte, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return false, nil, nil, err
	}
	req.Header.Set("Range", "bytes=0-1023")
	applyHeaders(req, d.st, d.task.Headers)
	resp, err := d.client.Do(req)
	if err != nil {
		return false, nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return false, nil, nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	head := make([]byte, 1024)
	n, _ := io.ReadFull(resp.Body, head)
	head = head[:n]
	u, _ := url.Parse(resp.Request.URL.String())
	if strings.Contains(string(head), "#EXTM3U") {
		return true, head, u, nil
	}
	return false, head, u, nil
}
