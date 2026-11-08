package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 假 ffmpeg：把一段固定内容写进最后一个参数（输出路径）；
// FAKE_FFMPEG_FAIL=1 时模拟 NAS 上常见的 Permission denied。
const fakeFFmpeg = `#!/bin/sh
if [ "${FAKE_FFMPEG_FAIL:-}" != "" ]; then
  echo "Error opening output x.mp4: Permission denied" >&2
  exit 1
fi
for last; do :; done
printf 'MP4DATA' > "$last"
exit 0
`

func newFakeFileOps(t *testing.T) *FileOps {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(p, []byte(fakeFFmpeg), 0o755); err != nil {
		t.Fatal(err)
	}
	return &FileOps{ffmpeg: p, hint: "ffmpeg"}
}

// 回归：目标位置已有只读文件时，旧实现让 ffmpeg 直接 open(out) 会报
// Permission denied；新实现写临时文件再原子替换，只要目录可写就能覆盖。
func TestConvertAtomicRenameOverReadOnlyTarget(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "video.mp4")
	if err := os.WriteFile(out, []byte("OLD"), 0o444); err != nil {
		t.Fatal(err)
	}
	f := newFakeFileOps(t)
	sz, err := f.Convert(context.Background(), "in.bin", out, Settings{})
	if err != nil {
		t.Fatalf("Convert 失败: %v", err)
	}
	if sz != int64(len("MP4DATA")) {
		t.Fatalf("size = %d", sz)
	}
	b, err := os.ReadFile(out)
	if err != nil || string(b) != "MP4DATA" {
		t.Fatalf("成品内容不对: %q %v", b, err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".ms-convert-*"))
	if len(left) != 0 {
		t.Fatalf("残留临时文件: %v", left)
	}
}

// 目录真的不可写时，必须给出可操作提示 + 永久错误，不要走转码兜底、
// 也不要让上层把几百个分片重下三遍。
func TestConvertPermissionIsPermanent(t *testing.T) {
	dir := t.TempDir()
	f := newFakeFileOps(t)
	t.Setenv("FAKE_FFMPEG_FAIL", "1")
	_, err := f.Convert(context.Background(), "in.bin", filepath.Join(dir, "v.mp4"), Settings{AllowTranscode: true})
	if err == nil {
		t.Fatal("应该失败")
	}
	if !IsPermanent(err) {
		t.Fatalf("权限错误应判定为永久错误: %v", err)
	}
	if !strings.Contains(err.Error(), "写入权限") {
		t.Fatalf("错误文案应给出可操作提示: %v", err)
	}
}

func TestIsFSPermanent(t *testing.T) {
	for _, s := range []string{
		"Error opening output x.mp4: Permission denied",
		"Read-only file system",
		"No space left on device",
		"Operation not permitted",
	} {
		if !isFSPermanent(errors.New(s)) {
			t.Fatalf("应判定为文件系统永久错误: %s", s)
		}
	}
	if isFSPermanent(errors.New("Invalid data found when processing input")) {
		t.Fatal("普通解码错误不该被判成文件系统永久错误")
	}
}

func TestEnsureDirAndWriteHint(t *testing.T) {
	if err := ensureDir(t.TempDir()); err != nil {
		t.Fatalf("可写目录不应报错: %v", err)
	}
	err := writeHint("/some/dir", os.ErrPermission)
	if !IsPermanent(err) || !strings.Contains(err.Error(), "写入权限") {
		t.Fatalf("writeHint 应给出永久错误与操作提示: %v", err)
	}
}

func TestSafeNameByteLimit(t *testing.T) {
	s := safeName(strings.Repeat("测试视频", 100))
	if len(s) > 200 {
		t.Fatalf("文件名超过字节上限: %d", len(s))
	}
	if strings.ContainsAny(s, "/\\:*?\"<>|") {
		t.Fatalf("文件名仍有非法字符: %q", s)
	}
}
