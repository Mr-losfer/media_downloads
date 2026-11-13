package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"mediasuper/internal/engine"
	"mediasuper/internal/version"
)

const mcpVersion = "2025-06-18"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func p(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}

func mcpTools() []mcpTool {
	return []mcpTool{
		{
			Name:        "submit_download",
			Description: "提交一个视频/音频下载任务。支持 m3u8(HLS)、mp4、flv、ts 等直链；m3u8 会自动多线程拉分片并转封装成 mp4。同一链接已存在任务会直接拦截重复下载（标题防重可在设置里开启），失败/已取消的任务不拦截。返回任务 ID。",
			InputSchema: obj(map[string]any{
				"url":      p("string", "视频直链（http/https，.m3u8 或 .mp4 等）"),
				"title":    p("string", "可选，保存文件名（不含扩展名）"),
				"save_dir": p("string", "可选，保存目录，必须是设置里配置过的目录"),
				"headers":  map[string]any{"type": "object", "description": "可选，防盗链请求头，如 {\"Referer\":\"...\",\"Cookie\":\"...\"}"},
			}, "url"),
		},
		{
			Name:        "list_tasks",
			Description: "列出下载任务，可按状态过滤。状态: queued/running/merging/completed/failed/canceled，或 active 表示进行中。",
			InputSchema: obj(map[string]any{
				"status": p("string", "可选，状态过滤"),
				"query":  p("string", "可选，标题/URL 关键词"),
				"limit":  map[string]any{"type": "integer", "description": "返回条数，默认 50"},
				"offset": map[string]any{"type": "integer", "description": "跳过条数"},
			}),
		},
		{
			Name:        "get_task",
			Description: "查询单个任务的详细状态、进度百分比、速度、文件路径。",
			InputSchema: obj(map[string]any{"task_id": p("string", "任务 ID")}, "task_id"),
		},
		{
			Name:        "cancel_task",
			Description: "取消排队中或正在下载的任务。",
			InputSchema: obj(map[string]any{"task_id": p("string", "任务 ID")}, "task_id"),
		},
		{
			Name:        "retry_task",
			Description: "重新下载失败或已取消的任务（从头开始）。",
			InputSchema: obj(map[string]any{"task_id": p("string", "任务 ID")}, "task_id"),
		},
		{
			Name:        "delete_task",
			Description: "删除任务记录；delete_file=true 时同时删除磁盘上的视频文件。",
			InputSchema: obj(map[string]any{
				"task_id":     p("string", "任务 ID"),
				"delete_file": map[string]any{"type": "boolean", "description": "是否同时删除文件，默认 false"},
			}, "task_id"),
		},
		{
			Name:        "rename_task",
			Description: "重命名任务及其磁盘文件（扩展名自动保留）。",
			InputSchema: obj(map[string]any{
				"task_id":  p("string", "任务 ID"),
				"new_name": p("string", "新名称，不含扩展名"),
			}, "task_id", "new_name"),
		},
		{
			Name:        "get_download_link",
			Description: "获取任务文件的播放/下载链接（带 API Key，可直接给播放器用）。",
			InputSchema: obj(map[string]any{"task_id": p("string", "任务 ID")}, "task_id"),
		},
		{
			Name:        "get_settings",
			Description: "读取当前配置：保存目录、并发数、限速、ffmpeg、webhook 等。",
			InputSchema: obj(map[string]any{}),
		},
		{
			Name:        "update_settings",
			Description: "修改配置（只传要改的字段即可）。可改: max_tasks(1-8 并发任务)、seg_conns(1-32 分片连接)、speed_limit_kb(0=不限)、save_dirs、default_dir、webhook_url、webhook_secret、auto_clean_days、hls_quality(best/worst)、trim_enabled/trim_head/trim_tail(去掉片头片尾分片)、allow_transcode、thumbnails、keep_temp。",
			InputSchema: obj(map[string]any{
				"max_tasks":       map[string]any{"type": "integer", "description": "同时下载任务数 1-8"},
				"seg_conns":       map[string]any{"type": "integer", "description": "单任务并发连接数 1-32"},
				"speed_limit_kb":  map[string]any{"type": "integer", "description": "全局限速 KB/s，0 为不限"},
				"default_dir":     p("string", "默认保存目录"),
				"save_dirs":       map[string]any{"type": "array", "description": "保存目录列表 [{path,label}]", "items": map[string]any{"type": "object"}},
				"webhook_url":     p("string", "事件回调地址"),
				"webhook_secret":  p("string", "回调签名密钥（HMAC-SHA256）"),
				"auto_clean_days": map[string]any{"type": "integer", "description": "完成 N 天后自动删除，0 关闭"},
				"hls_quality":     p("string", "best 或 worst"),
				"trim_enabled":    map[string]any{"type": "boolean", "description": "是否去掉片头 / 片尾分片（只对 HLS 生效）"},
				"trim_head":       map[string]any{"type": "integer", "description": "去掉开头 N 个分片，0-99"},
				"trim_tail":       map[string]any{"type": "integer", "description": "去掉结尾 N 个分片，0-99"},
				"allow_transcode": map[string]any{"type": "boolean", "description": "重封装失败时是否允许转码"},
				"thumbnails":      map[string]any{"type": "boolean", "description": "是否生成视频缩略图"},
				"keep_temp":       map[string]any{"type": "boolean", "description": "是否保留临时分片"},
			}),
		},
		{
			Name:        "get_stats",
			Description: "获取统计信息：各状态任务数、磁盘剩余空间、运行时长。",
			InputSchema: obj(map[string]any{}),
		},
	}
}

type toolResult struct {
	Content []map[string]any `json:"content"`
	IsError bool             `json:"isError,omitempty"`
}

func textResult(v any, isErr bool) toolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		b = []byte(fmt.Sprintf("%v", v))
	}
	return toolResult{Content: []map[string]any{{"type": "text", "text": string(b)}}, IsError: isErr}
}

func errResult(format string, a ...any) toolResult {
	return toolResult{Content: []map[string]any{{"type": "text", "text": fmt.Sprintf(format, a...)}}, IsError: true}
}

func taskBrief(t *engine.Task) map[string]any {
	return map[string]any{
		"task_id":    t.ID,
		"title":      t.Title,
		"status":     t.Status,
		"progress":   t.Progress,
		"downloaded": t.Downloaded,
		"total":      t.Total,
		"size":       t.Size,
		"speed":      t.Speed,
		"seg_done":   t.SegDone,
		"seg_total":  t.SegTotal,
		"file_name":  t.FileName,
		"save_dir":   t.SaveDir,
		"error":      t.Error,
		"url":        t.URL,
	}
}

// dispatch 处理一条 JSON-RPC 请求；httpMode=false 时用于 stdio。
func (s *Server) dispatch(req rpcRequest, host string) (*rpcResponse, bool) {
	resp := &rpcResponse{JSONRPC: "2.0", ID: req.ID}
	notification := len(req.ID) == 0 || string(req.ID) == "null"

	switch req.Method {
	case "initialize":
		ver := mcpVersion
		var in struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &in)
		}
		switch in.ProtocolVersion {
		case "2024-11-05", "2025-03-26", "2025-06-18":
			ver = in.ProtocolVersion
		}
		resp.Result = map[string]any{
			"protocolVersion": ver,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": version.AppID, "version": version.Version},
			"instructions":    "视频下载管理服务。用 submit_download 提交 m3u8/mp4 直链，用 get_task / list_tasks 查询进度（progress 为百分比，speed 单位字节/秒），用 get_download_link 获取可播放链接。",
		}

	case "notifications/initialized", "notifications/cancelled", "notifications/roots/list_changed":
		return nil, true

	case "ping":
		resp.Result = map[string]any{}

	case "prompts/list":
		resp.Result = map[string]any{"prompts": []any{}}

	case "resources/list":
		resp.Result = map[string]any{"resources": []any{}}

	case "tools/list":
		resp.Result = map[string]any{"tools": mcpTools()}

	case "tools/call":
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &call); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: "参数解析失败: " + err.Error()}
			return resp, notification
		}
		res, err := s.callTool(call.Name, call.Arguments, host)
		if err != nil {
			resp.Result = errResult("%s", err.Error())
		} else {
			resp.Result = res
		}

	default:
		resp.Error = &rpcError{Code: -32601, Message: "不支持的方法: " + req.Method}
	}
	return resp, notification
}

func (s *Server) callTool(name string, args json.RawMessage, host string) (any, error) {
	switch name {
	case "submit_download":
		var in engine.TaskInput
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, fmt.Errorf("参数错误: %w", err)
		}
		t, err := s.m.Add(in)
		if err != nil {
			return nil, err
		}
		return textResult(map[string]any{
			"ok":       true,
			"task_id":  t.ID,
			"status":   t.Status,
			"title":    t.Title,
			"save_dir": t.SaveDir,
		}, false), nil

	case "list_tasks":
		var in struct {
			Status string `json:"status"`
			Query  string `json:"query"`
			Limit  int    `json:"limit"`
			Offset int    `json:"offset"`
		}
		_ = json.Unmarshal(args, &in)
		if in.Limit <= 0 || in.Limit > 200 {
			in.Limit = 50
		}
		tasks, total := s.m.List(engine.ListFilter{Status: in.Status, Query: in.Query, Limit: in.Limit, Offset: in.Offset})
		out := make([]map[string]any, 0, len(tasks))
		for _, t := range tasks {
			out = append(out, taskBrief(t))
		}
		return textResult(map[string]any{"total": total, "count": len(out), "tasks": out}, false), nil

	case "get_task":
		var in struct {
			TaskID string `json:"task_id"`
		}
		_ = json.Unmarshal(args, &in)
		t, ok := s.m.Get(in.TaskID)
		if !ok {
			return nil, engine.ErrNotFound
		}
		b := taskBrief(t)
		b["kind"] = t.Kind
		b["duration"] = t.Duration
		b["attempts"] = t.Attempts
		b["created_at"] = t.CreatedAt
		b["finished_at"] = t.FinishedAt
		return textResult(b, false), nil

	case "cancel_task":
		var in struct {
			TaskID string `json:"task_id"`
		}
		_ = json.Unmarshal(args, &in)
		if err := s.m.Cancel(in.TaskID); err != nil {
			return nil, err
		}
		return textResult(map[string]any{"ok": true, "task_id": in.TaskID, "status": "canceled"}, false), nil

	case "retry_task":
		var in struct {
			TaskID string `json:"task_id"`
		}
		_ = json.Unmarshal(args, &in)
		t, err := s.m.Retry(in.TaskID)
		if err != nil {
			return nil, err
		}
		return textResult(map[string]any{"ok": true, "task_id": t.ID, "status": t.Status}, false), nil

	case "delete_task":
		var in struct {
			TaskID     string `json:"task_id"`
			DeleteFile bool   `json:"delete_file"`
		}
		_ = json.Unmarshal(args, &in)
		if err := s.m.Delete(in.TaskID, in.DeleteFile); err != nil {
			return nil, err
		}
		return textResult(map[string]any{"ok": true, "deleted": in.TaskID, "file_deleted": in.DeleteFile}, false), nil

	case "rename_task":
		var in struct {
			TaskID  string `json:"task_id"`
			NewName string `json:"new_name"`
		}
		_ = json.Unmarshal(args, &in)
		t, err := s.m.Rename(in.TaskID, in.NewName)
		if err != nil {
			return nil, err
		}
		return textResult(map[string]any{"ok": true, "task_id": t.ID, "file_name": t.FileName}, false), nil

	case "get_download_link":
		var in struct {
			TaskID string `json:"task_id"`
		}
		_ = json.Unmarshal(args, &in)
		t, ok := s.m.Get(in.TaskID)
		if !ok {
			return nil, engine.ErrNotFound
		}
		if t.Status != engine.StatusCompleted {
			return nil, fmt.Errorf("文件尚未下载完成（当前状态 %s）", t.Status)
		}
		st := s.m.Settings()
		base := strings.TrimSuffix(st.PublicBase, "/")
		if base == "" {
			base = "http://" + host
		}
		return textResult(map[string]any{
			"task_id":      t.ID,
			"file_name":    t.FileName,
			"size":         t.Size,
			"play_url":     fmt.Sprintf("%s/api/tasks/%s/stream?token=%s", base, t.ID, st.APIKey),
			"download_url": fmt.Sprintf("%s/api/tasks/%s/download?token=%s", base, t.ID, st.APIKey),
		}, false), nil

	case "get_settings":
		st := s.m.Settings()
		st.APIKey = "***" // 不通过 MCP 泄露密钥
		return textResult(st, false), nil

	case "update_settings":
		cur := s.m.Settings()
		body, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &cur); err != nil {
			return nil, fmt.Errorf("参数错误: %w", err)
		}
		st, err := s.m.UpdateSettings(cur)
		if err != nil {
			return nil, err
		}
		st.APIKey = "***"
		return textResult(st, false), nil

	case "get_stats":
		return textResult(s.m.Stats(), false), nil
	}
	return nil, fmt.Errorf("未知工具: %s", name)
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if !s.auth.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="mediasuper"`)
		writeErr(w, http.StatusUnauthorized, "未授权：请在 Authorization: Bearer 提供 API Key")
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "json") {
		writeErr(w, http.StatusUnsupportedMediaType, "Content-Type 必须是 application/json")
		return
	}
	var req rpcRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, 200, &rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &rpcError{Code: -32700, Message: "JSON 解析失败: " + err.Error()}})
		return
	}
	resp, notification := s.dispatch(req, r.Host)
	if notification || resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if req.Method == "initialize" {
		w.Header().Set("Mcp-Session-Id", randomHex(16))
	}
	w.Header().Set("Mcp-Protocol-Version", mcpVersion)
	writeJSON(w, 200, resp)
}
