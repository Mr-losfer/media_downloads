package updater

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 造一个「假 ffmpeg」gzip 包：ELF 头 + 填充，验证下载/校验/解压/落盘全链路。
func fakeFFmpegGz(t *testing.T) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	body := append([]byte("\x7fELF"), bytes.Repeat([]byte("X"), 2<<20)...)
	if _, err := zw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

func TestEnsureFFmpegDownloadsAndInstalls(t *testing.T) {
	gz, sum := fakeFFmpegGz(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(gz)
	}))
	defer srv.Close()

	dir := t.TempDir()
	u := New(dir, "test/repo")
	a := Asset{Name: "ffmpeg.gz", URL: srv.URL, SHA256: sum, Size: int64(len(gz))}

	if _, err := u.installFFmpeg(context.Background(), a, nil); err != nil {
		t.Fatalf("安装失败: %v", err)
	}
	target := u.ffmpegTarget()
	st, err := os.Stat(target)
	if err != nil {
		t.Fatalf("组件未落盘: %v", err)
	}
	if st.Mode()&0o111 == 0 {
		t.Fatal("组件没有可执行权限")
	}
	head := make([]byte, 4)
	f, _ := os.Open(target)
	_, _ = f.Read(head)
	f.Close()
	if string(head) != "\x7fELF" {
		t.Fatalf("组件内容不对: %q", head)
	}
	if !u.FFmpegInstalled() {
		t.Fatal("FFmpegInstalled 应为 true")
	}
	// 不应留下临时文件
	if _, err := os.Stat(filepath.Join(dir, "bin", "ffmpeg.download.gz")); !os.IsNotExist(err) {
		t.Fatal("下载临时文件未清理")
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "ffmpeg.new")); !os.IsNotExist(err) {
		t.Fatal("解压临时文件未清理")
	}
}

func TestEnsureFFmpegRejectsBadSHA(t *testing.T) {
	gz, _ := fakeFFmpegGz(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(gz)
	}))
	defer srv.Close()

	u := New(t.TempDir(), "test/repo")
	a := Asset{Name: "ffmpeg.gz", URL: srv.URL, SHA256: strings.Repeat("0", 64), Size: int64(len(gz))}
	if _, err := u.installFFmpeg(context.Background(), a, nil); err == nil {
		t.Fatal("SHA256 不匹配时应失败")
	}
	if u.FFmpegInstalled() {
		t.Fatal("校验失败不应留下可执行文件")
	}
}

func TestEnsureFFmpegRejectsNonELF(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(bytes.Repeat([]byte("<html>error</html>"), 100000))
	_ = zw.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	u := New(t.TempDir(), "test/repo")
	a := Asset{Name: "ffmpeg.gz", URL: srv.URL, Size: int64(buf.Len())}
	if _, err := u.installFFmpeg(context.Background(), a, nil); err == nil {
		t.Fatal("非 ELF 内容应被拒绝")
	}
	if u.FFmpegInstalled() {
		t.Fatal("非法内容不应落盘")
	}
}

func TestEnsureFFmpegSkipsWhenSystemReady(t *testing.T) {
	u := New(t.TempDir(), "test/repo")
	st := u.EnsureFFmpeg(context.Background(), false, nil)
	if st.Status != "ready" || st.Progress != 100 {
		t.Fatalf("系统已有 ffmpeg 时应直接 ready，得到 %+v", st)
	}
	if u.FFmpegState().Status != "ready" {
		t.Fatal("状态未写入")
	}
}

func TestFFmpegAssetNameOnlyForFPKPlatforms(t *testing.T) {
	// 只有 amd64 / arm64 两个 fpk 平台有预编译组件
	if got := ffmpegAssetName(); got != "" {
		key := PlatformKey()
		if key != "linux-amd64" && key != "linux-arm64" && got != "" {
			t.Fatalf("非 fpk 平台不应有组件: %s -> %s", key, got)
		}
	}
}
