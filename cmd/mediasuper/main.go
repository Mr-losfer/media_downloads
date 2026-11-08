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
	"path/filepath"
	"strings"

	"mediasuper/internal/app"
	"mediasuper/internal/engine"
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
	resetPW := flag.String("reset-password", "", "重置管理员密码后退出（供 fpk 安装/配置脚本调用）")
	flag.Parse()
	log.SetFlags(log.LstdFlags)

	if *showVer {
		fmt.Printf("%s %s (commit %s, built %s)\n", version.Name, version.Version, version.Commit, version.BuildTime)
		return
	}
	if *resetPW != "" {
		if err := resetPassword(*dataDir, *resetPW); err != nil {
			log.Fatalf("重置密码失败: %v", err)
		}
		fmt.Println("管理员密码已更新")
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

// resetPassword 直接把新密码哈希写进数据库，供 fpk 安装/配置脚本调用。
// bbolt 是独占锁，调用前必须先停掉服务。
func resetPassword(dataDir, pw string) error {
	if len(pw) < 6 {
		return fmt.Errorf("密码至少 6 位")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	dbPath := filepath.Join(dataDir, "mediasuper.db")
	if _, err := os.Stat(dbPath); err != nil {
		oldPath := filepath.Join(dataDir, "vidfetch.db")
		if _, err2 := os.Stat(oldPath); err2 == nil {
			_ = os.Rename(oldPath, dbPath)
		}
	}
	db, err := engine.OpenStore(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	h, err := server.HashPassword(pw)
	if err != nil {
		return err
	}
	return db.MetaPut("pass_hash", h)
}
