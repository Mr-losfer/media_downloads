package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"mediasuper/internal/plugin"
)

// 本文件是「插件管理」与「在线插件市场」的 REST 层。
// 所有接口都需要登录或 API Key（在 api.go 统一挂 needAuth）。

const pluginAPIVersion = "1"

func (s *Server) pluginsNotReady(w http.ResponseWriter) bool {
	if s.pm == nil {
		writeErr(w, 503, "插件系统未初始化")
		return true
	}
	return false
}

func pluginID(r *http.Request) string {
	return strings.ToLower(strings.TrimSpace(r.PathValue("id")))
}

// readJSONLarge 用于插件包导入（1MB 的默认上限不够放多份源码）。
func readJSONLarge(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 16<<20))
	return dec.Decode(v)
}

// GET /api/plugins —— 插件列表 + 扫描警告
func (s *Server) handlePluginList(w http.ResponseWriter, r *http.Request) {
	if s.pluginsNotReady(w) {
		return
	}
	list := s.pm.List()
	if list == nil {
		list = []plugin.View{}
	}
	writeJSON(w, 200, map[string]any{
		"api_version": pluginAPIVersion,
		"count":       len(list),
		"plugins":     list,
		"warnings":    s.pm.Warnings(),
		"plugins_dir": s.pm.PluginsDir(),
	})
}

// POST /api/plugins —— 新建脚手架；若带 bundle 则视为内联导入
func (s *Server) handlePluginCreate(w http.ResponseWriter, r *http.Request) {
	if s.pluginsNotReady(w) {
		return
	}
	var in struct {
		plugin.CreateInput
		Bundle *plugin.Bundle `json:"bundle"`
	}
	if err := readJSONLarge(r, &in); err != nil {
		writeErr(w, 400, "请求体格式错误："+err.Error())
		return
	}
	var (
		i   *plugin.Installed
		err error
	)
	if in.Bundle != nil {
		i, err = s.pm.ImportBundle(*in.Bundle)
	} else {
		i, err = s.pm.Create(in.CreateInput)
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"ok": true, "plugin": i.View()})
}

// GET /api/plugins/{id}
func (s *Server) handlePluginGet(w http.ResponseWriter, r *http.Request) {
	if s.pluginsNotReady(w) {
		return
	}
	i, ok := s.pm.Get(pluginID(r))
	if !ok {
		writeErr(w, 404, "插件不存在")
		return
	}
	writeJSON(w, 200, i.View())
}

// PUT /api/plugins/{id} —— 覆盖 plugin.json
func (s *Server) handlePluginUpdate(w http.ResponseWriter, r *http.Request) {
	if s.pluginsNotReady(w) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeErr(w, 400, "读取请求体失败")
		return
	}
	// 允许 { "manifest": {...} } 或直接一份 manifest
	var wrap struct {
		Manifest json.RawMessage `json:"manifest"`
	}
	if json.Unmarshal(body, &wrap) == nil && len(wrap.Manifest) > 0 {
		body = wrap.Manifest
	}
	i, err := s.pm.SaveMeta(pluginID(r), body)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "plugin": i.View()})
}

// DELETE /api/plugins/{id}
func (s *Server) handlePluginDelete(w http.ResponseWriter, r *http.Request) {
	if s.pluginsNotReady(w) {
		return
	}
	id := pluginID(r)
	if err := s.pm.Delete(id); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "deleted": id})
}

// GET/POST /api/plugins/{id}/config —— 可视化配置值
func (s *Server) handlePluginConfig(w http.ResponseWriter, r *http.Request) {
	if s.pluginsNotReady(w) {
		return
	}
	id := pluginID(r)
	i, ok := s.pm.Get(id)
	if !ok {
		writeErr(w, 404, "插件不存在")
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]any{
			"id": id, "config": i.State.Config, "config_schema": i.Manifest.ConfigSchema,
		})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, 400, "读取请求体失败")
		return
	}
	var cfgIn map[string]any
	if err := json.Unmarshal(body, &cfgIn); err != nil {
		writeErr(w, 400, "请求体格式错误："+err.Error())
		return
	}
	// 支持两种写法：{"config":{...}} 和直接 {"api_base":"..."}
	if v, ok := cfgIn["config"]; ok {
		if m, ok := v.(map[string]any); ok {
			cfgIn = m
		}
	}
	cfg, err := s.pm.SetConfig(id, cfgIn)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "id": id, "config": cfg})
}

// POST /api/plugins/{id}/toggle
func (s *Server) handlePluginToggle(w http.ResponseWriter, r *http.Request) {
	if s.pluginsNotReady(w) {
		return
	}
	id := pluginID(r)
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	_ = readJSON(r, &in)
	i, ok := s.pm.Get(id)
	if !ok {
		writeErr(w, 404, "插件不存在")
		return
	}
	on := !i.State.Enabled
	if in.Enabled != nil {
		on = *in.Enabled
	}
	if err := s.pm.SetEnabled(id, on); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "id": id, "enabled": on})
}

// POST /api/plugins/{id}/copy —— 把内置/已有插件另存为可编辑副本
func (s *Server) handlePluginCopy(w http.ResponseWriter, r *http.Request) {
	if s.pluginsNotReady(w) {
		return
	}
	var in struct {
		NewID string `json:"new_id"`
		Name  string `json:"name"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	i, err := s.pm.Copy(pluginID(r), in.NewID, in.Name)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"ok": true, "plugin": i.View()})
}

// POST /api/plugins/{id}/test | /debug —— 用样例链接跑一次插件
func (s *Server) handlePluginTest(w http.ResponseWriter, r *http.Request, debug bool) {
	if s.pluginsNotReady(w) {
		return
	}
	var in struct {
		URL   string `json:"url"`
		Debug bool   `json:"debug"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	raw := strings.TrimSpace(in.URL)
	if raw == "" {
		writeErr(w, 400, "请提供测试链接 url")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	res, tr, err := s.pm.Run(ctx, pluginID(r), raw, debug || in.Debug)
	if err != nil {
		writeJSON(w, 502, map[string]any{
			"ok": false, "error": cleanParseError(err.Error()), "trace": tr,
		})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "result": res, "trace": tr})
}

// GET /api/plugins/{id}/files
func (s *Server) handlePluginFiles(w http.ResponseWriter, r *http.Request) {
	if s.pluginsNotReady(w) {
		return
	}
	i, ok := s.pm.Get(pluginID(r))
	if !ok {
		writeErr(w, 404, "插件不存在")
		return
	}
	_ = i.RefreshFiles()
	writeJSON(w, 200, map[string]any{"id": i.Manifest.ID, "files": i.Files})
}

// GET/PUT/DELETE /api/plugins/{id}/file
func (s *Server) handlePluginFile(w http.ResponseWriter, r *http.Request) {
	if s.pluginsNotReady(w) {
		return
	}
	i, ok := s.pm.Get(pluginID(r))
	if !ok {
		writeErr(w, 404, "插件不存在")
		return
	}
	rel := r.URL.Query().Get("path")
	switch r.Method {
	case http.MethodGet:
		b, err := i.ReadFile(rel)
		if err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	case http.MethodPut:
		var in struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := readJSONLarge(r, &in); err != nil {
			writeErr(w, 400, "请求体格式错误")
			return
		}
		if in.Path == "" {
			in.Path = rel
		}
		if err := i.WriteFile(in.Path, []byte(in.Content)); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		// 改了 plugin.json 要立刻重新加载元数据
		if strings.TrimSuffix(in.Path, "/") == "plugin.json" {
			if err := s.pm.Reload(); err != nil {
				writeErr(w, 400, "plugin.json 已写入但重载失败："+err.Error())
				return
			}
		} else {
			_ = i.RefreshFiles()
		}
		writeJSON(w, 200, map[string]any{"ok": true, "path": in.Path, "size": len(in.Content)})
	case http.MethodDelete:
		if err := i.DeleteFile(rel); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "deleted": rel})
	}
}

// POST /api/plugins/{id}/patch —— 差量修改（find/replace、insert、delete）
func (s *Server) handlePluginPatch(w http.ResponseWriter, r *http.Request) {
	if s.pluginsNotReady(w) {
		return
	}
	i, ok := s.pm.Get(pluginID(r))
	if !ok {
		writeErr(w, 404, "插件不存在")
		return
	}
	var in struct {
		Ops []plugin.PatchOp `json:"ops"`
	}
	if err := readJSONLarge(r, &in); err != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	if len(in.Ops) == 0 {
		writeErr(w, 400, "ops 不能为空")
		return
	}
	results, err := plugin.ApplyPatches(i, in.Ops)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "results": results})
		return
	}
	_ = s.pm.Reload()
	writeJSON(w, 200, map[string]any{"ok": true, "results": results})
}

// POST /api/plugins/{id}/build —— 编译 Go 插件
func (s *Server) handlePluginBuild(w http.ResponseWriter, r *http.Request) {
	if s.pluginsNotReady(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	res, err := s.pm.Build(ctx, pluginID(r))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	_ = s.pm.Reload()
	writeJSON(w, 200, res)
}

// POST /api/plugins/{id}/reload
func (s *Server) handlePluginReload(w http.ResponseWriter, r *http.Request) {
	if s.pluginsNotReady(w) {
		return
	}
	if err := s.pm.Reload(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "count": len(s.pm.List()), "warnings": s.pm.Warnings()})
}

// POST /api/plugins/reload —— 全局重载
func (s *Server) handlePluginReloadAll(w http.ResponseWriter, r *http.Request) {
	s.handlePluginReload(w, r)
}

// GET /api/plugins/{id}/export —— 导出 zip
func (s *Server) handlePluginExport(w http.ResponseWriter, r *http.Request) {
	if s.pluginsNotReady(w) {
		return
	}
	i, ok := s.pm.Get(pluginID(r))
	if !ok {
		writeErr(w, 404, "插件不存在")
		return
	}
	b, err := i.ExportZip()
	if err != nil {
		writeErr(w, 500, "导出失败："+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.zip"`, i.Manifest.ID, i.Manifest.Version))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// POST /api/plugins/upload —— 上传 zip（multipart file 或 raw body）或 JSON Bundle
func (s *Server) handlePluginUpload(w http.ResponseWriter, r *http.Request) {
	if s.pluginsNotReady(w) {
		return
	}
	ct := r.Header.Get("Content-Type")
	var (
		i   *plugin.Installed
		err error
	)
	switch {
	case strings.Contains(ct, "multipart/form-data"):
		if err := r.ParseMultipartForm(16 << 20); err != nil {
			writeErr(w, 400, "表单解析失败："+err.Error())
			return
		}
		f, _, e := r.FormFile("file")
		if e != nil {
			writeErr(w, 400, "缺少 file 字段")
			return
		}
		defer f.Close()
		i, err = s.pm.ImportZip(io.LimitReader(f, 16<<20), plugin.OriginLocal, "本地上传 zip")
	case strings.Contains(ct, "application/zip") || strings.Contains(ct, "application/octet-stream"):
		i, err = s.pm.ImportZip(io.LimitReader(r.Body, 16<<20), plugin.OriginLocal, "本地上传 zip")
	default:
		var in struct {
			Bundle plugin.Bundle     `json:"bundle"`
			Files  map[string]string `json:"files"`
		}
		if e := readJSONLarge(r, &in); e != nil {
			writeErr(w, 400, "请求体格式错误："+e.Error())
			return
		}
		b := in.Bundle
		if b.Files == nil {
			b.Files = in.Files
		}
		b.Origin = plugin.OriginLocal
		if b.Source == "" {
			b.Source = "本地上传文件夹"
		}
		i, err = s.pm.ImportBundle(b)
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"ok": true, "plugin": i.View()})
}

// POST /api/plugins/validate —— 只校验不安装
func (s *Server) handlePluginValidate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeErr(w, 400, "读取请求体失败")
		return
	}
	var wrap struct {
		Manifest json.RawMessage `json:"manifest"`
	}
	if json.Unmarshal(body, &wrap) == nil && len(wrap.Manifest) > 0 {
		body = wrap.Manifest
	}
	mf, err := plugin.ParseManifest(body)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok": true, "id": mf.ID, "engine": mf.Engine,
		"config_schema": mf.ConfigSchema, "match": mf.Match,
		"message": "plugin.json 校验通过",
	})
}

// ---------- 在线插件市场 ----------

func (s *Server) handleMarketList(w http.ResponseWriter, r *http.Request) {
	if s.pluginsNotReady(w) {
		return
	}
	force := r.URL.Query().Get("force") == "1"
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	idx, err := s.pm.MarketList(ctx, force)
	if err != nil {
		writeJSON(w, 200, map[string]any{
			"ok": false, "error": cleanParseError(err.Error()),
			"config": s.pm.MarketConfig(), "plugins": []any{},
		})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "index": idx, "config": s.pm.MarketConfig()})
}

func (s *Server) handleMarketRefresh(w http.ResponseWriter, r *http.Request) {
	r.URL.RawQuery = "force=1"
	s.handleMarketList(w, r)
}

func (s *Server) handleMarketInstall(w http.ResponseWriter, r *http.Request) {
	if s.pluginsNotReady(w) {
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	i, err := s.pm.MarketInstall(ctx, strings.ToLower(strings.TrimSpace(in.ID)))
	if err != nil {
		writeErr(w, 502, cleanParseError(err.Error()))
		return
	}
	writeJSON(w, 201, map[string]any{"ok": true, "plugin": i.View()})
}

func (s *Server) handleMarketConfig(w http.ResponseWriter, r *http.Request) {
	if s.pluginsNotReady(w) {
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, 200, s.pm.MarketConfig())
		return
	}
	var c plugin.MarketConfig
	if err := readJSON(r, &c); err != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	out, err := s.pm.SetMarketConfig(c)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "config": out})
}
