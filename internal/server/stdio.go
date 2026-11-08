package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// StdioProxy 让只支持 stdio 的 MCP 客户端（Claude Desktop 等）也能用：
// 逐行读 JSON-RPC，转发到远端 HTTP 服务再把结果写回 stdout。
func StdioProxy(remote, apiKey string) error {
	remote = strings.TrimSuffix(remote, "/") + "/mcp"
	client := &http.Client{Timeout: 120 * time.Second}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 1<<20), 4<<20)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	for in.Scan() {
		line := bytes.TrimSpace(in.Bytes())
		if len(line) == 0 {
			continue
		}
		req, err := http.NewRequest(http.MethodPost, remote, bytes.NewReader(line))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			body, _ := json.Marshal(&rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
				Error: &rpcError{Code: -32000, Message: "连接远端失败: " + err.Error()}})
			out.Write(body)
			out.WriteString("\n")
			out.Flush()
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if len(bytes.TrimSpace(body)) > 0 {
			out.Write(bytes.TrimSpace(body))
			out.WriteString("\n")
			out.Flush()
		}
	}
	return in.Err()
}
