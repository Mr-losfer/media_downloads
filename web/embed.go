// Package web 把前端静态资源编进二进制，做到「一个文件就是一套服务」。
package web

import "embed"

// FS 是 web/ 目录下的全部前端文件。
//
//go:embed *.html *.js *.css *.webmanifest *.svg *.png
var FS embed.FS
