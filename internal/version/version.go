// Package version 集中存放应用标识与版本号。
//
// Version 只是保底值，正式构建时由 scripts/build-release.sh 通过
// -ldflags "-X mediasuper/internal/version.Version=x.y.z" 注入，
// 这样前端、健康检查、更新检测看到的版本永远一致。
package version

import "strings"

var (
	// Version 语义化版本号，例如 2.1.0
	Version = "2.5.8"
	// Commit 构建时的提交号（短哈希）
	Commit = "dev"
	// BuildTime 构建时间（RFC3339）
	BuildTime = ""
)

const (
	// Name 应用显示名
	Name = "Media Super"
	// AppID fnOS / 容器里使用的应用唯一标识
	AppID = "mediasuper"
	// BinaryName 可执行文件名
	BinaryName = "mediasuper"
	// Tagline 一句话简介
	Tagline = "视频下载管理器"
	// Repo 更新源仓库（owner/name）
	Repo = "Mr-losfer/media_downloads"
)

// String 返回 "v2.1.0" 形式，方便日志与前端直接展示。
func String() string {
	v := strings.TrimSpace(Version)
	if v == "" {
		return "v0.0.0"
	}
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}
