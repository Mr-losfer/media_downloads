// Media Super 服务入口。
//
// 只做三件事：解析参数、处理 -stdio / -version、把剩下的交给 internal/app。
// 真正的装配逻辑在 internal/app/app.go，方便容器、fpk、systemd 共用。
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"mediasuper/internal/app"
	"mediasuper/internal/server"
	"mediasuper/internal/version"
)

func main() {
	addr := flag.String("addr", envAny("MS_ADDR", "VF_ADDR", ":8080"), "HTTP 监听地址")
	dataDir := flag.String("data", envAny("MS_DATA_DIR", "VF_DATA_DIR", "/data"), "数据目录（数据库/临时分片/更新包）")
	stdio := flag.Bool("stdio", false, "以 MCP stdio 模式运行（把 stdin 的 JSON-RPC 转发到 -remote）")
	remote := flag.String("remote", envAny("MS_REMOTE", "VF_REMOTE", "http://127.0.0.1:8080"), "stdio 模式下要连接的服务地址")
	key := flag.String("key", envAny("MS_API_KEY", "VF_API_KEY", ""), "API Key（stdio 模式）")
	showVer := flag.Bool("version", false, "打印版本号后退出")
	noUpdate := flag.Bool("no-update-check", false, "启动时不检查更新")
	flag.Parse()
	log.SetFlags(log.LstdFlags)

	if *showVer {
		fmt.Printf("%s %s (commit %s, built %s)\n", version.Name, version.Version, version.Commit, version.BuildTime)
		return
	}
	if *stdio {
		if err := server.StdioProxy(*remote, *key); err != nil {
			log.Fatalf("stdio 模式失败: %v", err)
		}
		return
	}
	if err := app.Run(app.Options{
		Addr:               *addr,
		DataDir:            *dataDir,
		DisableUpdateCheck: *noUpdate,
	}); err != nil {
		log.Fatalf("启动失败: %v", err)
	}
}

func envAny(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}
