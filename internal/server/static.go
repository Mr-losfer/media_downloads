package server

import (
	"io/fs"
	"log"
	"mime"
	"path/filepath"
)

// staticFile 是一份已读入内存的前端资源。文件都很小（合计 < 300KB），
// 启动时读一次常驻，避免每个请求都走一次 embed FS 查找。
type staticFile struct {
	data  []byte
	ctype string
}

// LoadWebAssets 把内嵌的前端资源装进 map，顺带按扩展名补齐 MIME。
func LoadWebAssets(webFS fs.FS) map[string]staticFile {
	out := make(map[string]staticFile, 8)
	entries, err := fs.ReadDir(webFS, ".")
	if err != nil {
		log.Printf("读取内嵌前端失败: %v", err)
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := fs.ReadFile(webFS, e.Name())
		if err != nil {
			continue
		}
		ext := filepath.Ext(e.Name())
		ct := mime.TypeByExtension(ext)
		if ct == "" {
			switch ext {
			case ".svg":
				ct = "image/svg+xml"
			case ".webmanifest":
				ct = "application/manifest+json"
			case ".woff2":
				ct = "font/woff2"
			default:
				ct = "application/octet-stream"
			}
		}
		out[e.Name()] = staticFile{data: b, ctype: ct}
	}
	return out
}
