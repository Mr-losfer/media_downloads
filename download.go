package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errRangeUnsupported = errors.New("服务器不支持 Range 分片")

type probeInfo struct {
	HLS         bool
	Head        []byte
	Total       int64
	Ranges      bool
	ContentType string
	Disposition string
	FinalURL    string
}

func ensureDir(dir string) error {
	if dir == "" {
		return errors.New("保存目录为空")
	}
	return os.MkdirAll(dir, 0o755)
}

// probe 用一次 2KB 的 Range 请求同时判断：是否 m3u8 / 是否支持分片 / 总大小 / 容器类型。
// 比 HEAD 更可靠（大量站点对 HEAD 直接 403）。
func (d *dlCtx) probe(ctx context.Context) (*probeInfo, error) {
	var pi *probeInfo
	err := d.withRetry(ctx, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.task.URL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Range", "bytes=0-2047")
		applyHeaders(req, d.st, d.task.Headers)
		resp, err := d.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		head := make([]byte, 2048)
		n, _ := io.ReadFull(resp.Body, head)
		head = head[:n]

		p := &probeInfo{
			Head:        head,
			ContentType: resp.Header.Get("Content-Type"),
			Disposition: resp.Header.Get("Content-Disposition"),
			FinalURL:    resp.Request.URL.String(),
		}
		if strings.Contains(string(head), "#EXTM3U") {
			p.HLS = true
			p.Ranges = true
		}
		if cr := resp.Header.Get("Content-Range"); cr != "" {
			if i := strings.LastIndexByte(cr, '/'); i >= 0 {
				if total, err := strconv.ParseInt(strings.TrimSpace(cr[i+1:]), 10, 64); err == nil && total > 0 {
					p.Total = total
					p.Ranges = true
				}
			}
		}
		if p.Total == 0 && resp.ContentLength > 0 {
			p.Total = resp.ContentLength
		}
		if p.Total == 0 && resp.StatusCode == http.StatusOK && len(head) > 0 {
			// 服务器忽略了 Range：把这次读到的当作完整小文件
			p.Total = int64(len(head))
		}
		pi = p
		return nil
	})
	return pi, err
}

// run 是任务执行的唯一入口：探测后分流到 HLS 或普通文件。
func (d *dlCtx) run(ctx context.Context) (string, int64, error) {
	pi, err := d.probe(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("探测资源失败: %w", err)
	}
	if pi.HLS {
		return d.downloadHLS(ctx, d.task.URL)
	}
	return d.downloadDirect(ctx, pi)
}

// setFileName 只计算一次文件名，避免重试时反复生成 -1 -2 副本。
func (d *dlCtx) setFileName(ext, disp string) {
	if d.task.FileName != "" {
		if ext != "" && filepath.Ext(d.task.FileName) == "" {
			d.task.FileName += "." + ext
		}
		return
	}
	base := strings.TrimSpace(d.task.Title)
	if base == "" {
		if n := parseDispositionName(disp); n != "" {
			base = strings.TrimSuffix(n, filepath.Ext(n))
		}
	}
	if base == "" {
		base = titleFromURL(d.task.URL)
	}
	if base == "" {
		base = "video-" + time.Now().Format("20060102-150405")
	}
	d.task.Title = safeName(base)
	d.task.FileName = filepath.Base(uniquePath(d.task.SaveDir, d.task.Title, ext))
}

func (d *dlCtx) downloadDirect(ctx context.Context, pi *probeInfo) (string, int64, error) {
	ext := extFromCT(pi.ContentType)
	if ext == "" {
		ext = extFromURL(pi.FinalURL)
	}
	if ext == "" {
		ext = "mp4"
	}
	d.task.Kind = "file"
	d.task.Format = ext
	d.setFileName(ext, pi.Disposition)
	if err := ensureDir(d.task.SaveDir); err != nil {
		return "", 0, err
	}

	out := joinPath(d.task.SaveDir, d.task.FileName)
	part := out + ".part"
	defer func() {
		if _, err := os.Stat(part); err == nil {
			os.Remove(part)
		}
	}()

	if pi.Total > 0 {
		d.j.total.Store(pi.Total)
		d.task.Total = pi.Total
	}

	conns := clampInt(d.st.SegConns, 1, 32)
	var err error
	switch {
	case len(pi.Head) > 0 && pi.Total > 0 && pi.Total <= int64(len(pi.Head)):
		// 探测响应已经把整个小文件读完了
		err = os.WriteFile(part, pi.Head, 0o644)
		if err == nil {
			d.j.downloaded.Store(int64(len(pi.Head)))
		}
	case pi.Ranges && pi.Total > 4<<20 && conns > 1:
		d.task.Conns = conns
		d.m.progressUpdate(d.j, d.task)
		err = d.chunkedDownload(ctx, part, pi.Total, conns)
		if errors.Is(err, errRangeUnsupported) {
			os.Remove(part)
			d.j.downloaded.Store(0)
			d.task.Conns = 1
			err = d.streamDownload(ctx, part, true)
		}
	default:
		d.task.Conns = 1
		d.m.progressUpdate(d.j, d.task)
		err = d.streamDownload(ctx, part, false)
	}
	if err != nil {
		return "", 0, err
	}
	if err := os.Rename(part, out); err != nil {
		return "", 0, err
	}
	os.Chmod(out, 0o644)
	fi, err := os.Stat(out)
	if err != nil {
		return "", 0, err
	}
	return out, fi.Size(), nil
}

// chunkedDownload 把文件切成 N 段并行 Range 下载，WriteAt 直接落盘，
// 不需要合并步骤，内存占用 = N × 128KB。
func (d *dlCtx) chunkedDownload(ctx context.Context, part string, total int64, conns int) error {
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(total); err != nil {
		return err
	}

	chunk := total / int64(conns)
	if chunk < 1 {
		return errRangeUnsupported
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for i := 0; i < conns; i++ {
		start := int64(i) * chunk
		end := start + chunk - 1
		if i == conns-1 {
			end = total - 1
		}
		wg.Add(1)
		go func(start, end int64) {
			defer wg.Done()
			if err := d.fetchRange(ctx, f, start, end); err != nil {
				select {
				case errCh <- err:
					cancel()
				default:
				}
			}
		}(start, end)
	}
	wg.Wait()

	select {
	case err := <-errCh:
		return err
	default:
	}
	return ctx.Err()
}

func (d *dlCtx) fetchRange(ctx context.Context, f *os.File, start, end int64) error {
	var pos int64
	attempts := d.st.MaxRetries + 1
	var lastErr error
	buf := make([]byte, 128<<10)

	for attempt := 0; attempt < attempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 700 * time.Millisecond):
			}
		}
		err := func() error {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.task.URL, nil)
			if err != nil {
				return err
			}
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start+pos, end))
			applyHeaders(req, d.st, d.task.Headers)
			resp, err := d.client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
				return nil // 已经下完
			}
			if resp.StatusCode == http.StatusOK && start+pos > 0 {
				return errRangeUnsupported
			}
			if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
				return fmt.Errorf("HTTP %d", resp.StatusCode)
			}
			ow := io.NewOffsetWriter(f, start+pos)
			for {
				n, rerr := resp.Body.Read(buf)
				if n > 0 {
					if _, werr := ow.Write(buf[:n]); werr != nil {
						return werr
					}
					pos += int64(n)
					d.j.downloaded.Add(int64(n))
					d.checkProgress()
					d.limiter.wait(n)
				}
				if rerr == io.EOF {
					return nil
				}
				if rerr != nil {
					return rerr
				}
			}
		}()
		if err != nil {
			if errors.Is(err, errRangeUnsupported) {
				return err
			}
			lastErr = err
			continue
		}
		if start+pos >= end+1 {
			return nil
		}
		lastErr = fmt.Errorf("分片未写完(%d/%d)", pos, end-start+1)
	}
	if lastErr == nil {
		lastErr = errors.New("分片下载失败")
	}
	return lastErr
}

// streamDownload 单连接顺序下载，用于不支持 Range 或体积较小的资源。
func (d *dlCtx) streamDownload(ctx context.Context, part string, ranged bool) error {
	return d.withRetry(ctx, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.task.URL, nil)
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
		if resp.ContentLength > 0 && d.j.total.Load() == 0 {
			d.j.total.Store(resp.ContentLength)
		}
		f, err := os.Create(part)
		if err != nil {
			return err
		}
		defer f.Close()
		buf := make([]byte, 128<<10)
		var n int64
		n, err = copyWithProgress(f, resp.Body, buf, func(k int) {
			d.j.downloaded.Add(int64(k))
			d.checkProgress()
			d.limiter.wait(k)
		})
		if err != nil {
			// 保留已下部分用于下次续传？单连接场景直接重来，避免错位
			return err
		}
		if resp.ContentLength > 0 && n != resp.ContentLength {
			return fmt.Errorf("下载不完整: %d/%d", n, resp.ContentLength)
		}
		return nil
	})
}

func parseDispositionName(disp string) string {
	if disp == "" {
		return ""
	}
	if i := strings.Index(disp, "filename*="); i >= 0 {
		s := disp[i+len("filename*="):]
		if j := strings.IndexByte(s, ';'); j >= 0 {
			s = s[:j]
		}
		s = strings.Trim(strings.TrimSpace(s), "\"'")
		if k := strings.Index(s, "''"); k >= 0 {
			s = s[k+2:]
		}
		if u, err := url.QueryUnescape(s); err == nil {
			return u
		}
		return s
	}
	if i := strings.Index(disp, "filename="); i >= 0 {
		s := disp[i+len("filename="):]
		if j := strings.IndexByte(s, ';'); j >= 0 {
			s = s[:j]
		}
		return strings.Trim(strings.TrimSpace(s), "\"'")
	}
	return ""
}

func titleFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	base := filepath.Base(strings.TrimSuffix(u.Path, "/"))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if base == "" || base == "." || base == "/" {
		return u.Hostname()
	}
	switch strings.ToLower(base) {
	case "index", "playlist", "master", "main", "video", "hls", "prog_index":
		return u.Hostname() + "-" + time.Now().Format("20060102-150405")
	}
	if u, err := url.PathUnescape(base); err == nil {
		base = u
	}
	return base
}
