package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"mediasuper/internal/plugin"
)

// pluginMCPTools 定义「开发插件」相关的 MCP 工具。
// 覆盖完整生命周期：创建 → 写代码 → 差量修改 → 保存 → 校验 → 编译 →
// 测试 / 调试 → 配置 → 上传 / 导出 → 上架市场。
func pluginMCPTools() []mcpTool {
	idParam := p("string", "插件 ID（plugin.json 里的 id，也是目录名）")
	return []mcpTool{
		{
			Name:        "plugin_list",
			Description: "列出所有已安装插件：id、名称、版本、引擎、启停状态、来源、配置项、匹配规则、是否有编译产物。开发插件时先用它确认现有插件。",
			InputSchema: obj(map[string]any{}),
		},
		{
			Name:        "plugin_get",
			Description: "读取单个插件的完整信息（含 config_schema、match 规则、文件清单、当前配置值、警告）。",
			InputSchema: obj(map[string]any{"id": idParam}, "id"),
		},
		{
			Name:        "plugin_create",
			Description: "创建一个插件脚手架：自动生成 plugin.json、README.md，engine=go 时还生成 main.go/parse.go/go.mod。返回新插件详情。这是开发插件的第一步。",
			InputSchema: obj(map[string]any{
				"id":            p("string", "插件 ID，2–48 位小写字母开头，只含 a-z/0-9/_-，如 my_site"),
				"name":          p("string", "显示名称"),
				"engine":        p("string", "declarative（默认，零编译推荐）/ go（Go 源码）/ builtin"),
				"domains":       map[string]any{"type": "array", "description": "匹配域名，决定提交哪些链接时运行本插件，如 [\"example.com\"]", "items": map[string]any{"type": "string"}},
				"path_prefix":   p("string", "可选路径前缀，如 /video"),
				"description":   p("string", "插件简介"),
				"version":       p("string", "版本号，默认 1.0.0"),
				"author":        p("string", "作者，默认「本地开发者」"),
				"config_schema": map[string]any{"type": "array", "description": "可选，可视化配置选项数组，每项 {key,label,type,default,required,desc,options,min,max}", "items": map[string]any{"type": "object"}},
			}, "id", "name"),
		},
		{
			Name:        "plugin_update_meta",
			Description: "编辑插件的 plugin.json。传 manifest 可整体替换；或传 set 做浅合并（只改指定字段，如 match.domains、config_schema、declarative.response.fields）。id 与 builtin 字段不允许修改。",
			InputSchema: obj(map[string]any{
				"id":       idParam,
				"manifest": map[string]any{"type": "object", "description": "完整的新 plugin.json 对象（与 set 二选一）"},
				"set":      map[string]any{"type": "object", "description": "要修改的顶层字段字典，如 {\"description\":\"...\",\"match\":{...}}"},
			}, "id"),
		},
		{
			Name:        "plugin_files",
			Description: "列出插件目录下的所有文件（路径、大小、修改时间）。",
			InputSchema: obj(map[string]any{"id": idParam}, "id"),
		},
		{
			Name:        "plugin_read_file",
			Description: "读取插件目录里的某个文件内容（如 plugin.json / main.go / parse.go）。",
			InputSchema: obj(map[string]any{"id": idParam, "path": p("string", "相对路径，如 main.go")}, "id", "path"),
		},
		{
			Name:        "plugin_write_file",
			Description: "新建或整体覆盖插件里的一个代码/配置文件（可含多个主要代码文件）。append=true 时追加到文件末尾。写完建议用 plugin_save 重载。",
			InputSchema: obj(map[string]any{
				"id":      idParam,
				"path":    p("string", "相对路径，如 parse.go"),
				"content": p("string", "文件完整内容"),
				"append":  map[string]any{"type": "boolean", "description": "默认 false=覆盖；true=追加"},
			}, "id", "path", "content"),
		},
		{
			Name:        "plugin_delete_file",
			Description: "删除插件目录里的一个文件（不能删除 plugin.json 本身以外再破坏结构需自行注意）。",
			InputSchema: obj(map[string]any{"id": idParam, "path": p("string", "相对路径")}, "id", "path"),
		},
		{
			Name:        "plugin_patch",
			Description: "差量修改插件代码：对一个或多个文件做 find/replace、锚点插入、删除，不必重传整份文件。全部操作成功后一次性落盘；任一失败则全部不生效。",
			InputSchema: obj(map[string]any{
				"id": idParam,
				"ops": map[string]any{
					"type": "array",
					"description": "操作数组。每项 {path, action: replace|insert|delete, find, replace, content, anchor, all}。" +
						"replace 的 find 为空表示整文件覆盖；insert 的 anchor 为空表示追加到末尾；all=true 替换/删除全部匹配。",
					"items": map[string]any{"type": "object"},
				},
			}, "id", "ops"),
		},
		{
			Name:        "plugin_save",
			Description: "保存并重载插件：重新扫描目录、校验 plugin.json、补齐新增配置项默认值。改完代码后调用它让改动生效。",
			InputSchema: obj(map[string]any{"id": p("string", "可选，指定插件 ID；不传则重载全部")}),
		},
		{
			Name:        "plugin_validate",
			Description: "只校验一份 plugin.json 是否合规，不安装。返回 id/engine/config_schema/match 或具体错误，适合写完清单后自检。",
			InputSchema: obj(map[string]any{
				"manifest": map[string]any{"type": "object", "description": "要校验的 plugin.json 对象"},
			}, "manifest"),
		},
		{
			Name:        "plugin_build",
			Description: "编译 engine=go 的插件（go build 到 entry.binary）。设备没有 Go 工具链时返回降级建议：改用 declarative 引擎，或在开发机编译后只上传 bin/ 产物。",
			InputSchema: obj(map[string]any{"id": idParam}, "id"),
		},
		{
			Name:        "plugin_test",
			Description: "用一条样例链接运行插件，返回统一解析结果与耗时（不写任务、不下载）。开发时验证输出是否符合预期。",
			InputSchema: obj(map[string]any{"id": idParam, "url": p("string", "样例链接")}, "id", "url"),
		},
		{
			Name:        "plugin_debug",
			Description: "调试模式运行插件：除解析结果外，还返回请求/响应片段、stderr 日志、每一步耗时与中间态，用于定位失败原因。",
			InputSchema: obj(map[string]any{"id": idParam, "url": p("string", "样例链接")}, "id", "url"),
		},
		{
			Name:        "parse_url",
			Description: "不指定插件，按域名匹配规则自动挑选插件解析一条链接（与首页提交时的行为一致）。未命中返回 matched=false。",
			InputSchema: obj(map[string]any{"url": p("string", "要解析的链接")}, "url"),
		},
		{
			Name:        "plugin_config",
			Description: "读取插件的可视化配置 schema 与当前保存值。",
			InputSchema: obj(map[string]any{"id": idParam}, "id"),
		},
		{
			Name:        "plugin_set_config",
			Description: "写入插件配置值（只传要改的键即可），会按 config_schema 校验必填、数字范围、select 选项。保存后立即生效。",
			InputSchema: obj(map[string]any{
				"id":     idParam,
				"config": map[string]any{"type": "object", "description": "配置键值，如 {\"api_base\":\"https://...\",\"timeout_sec\":20}"},
			}, "id", "config"),
		},
		{
			Name:        "plugin_toggle",
			Description: "启用或禁用插件。禁用后提交匹配域名的链接不会再运行它。",
			InputSchema: obj(map[string]any{
				"id":      idParam,
				"enabled": map[string]any{"type": "boolean", "description": "true 启用 / false 禁用；不传则取反"},
			}, "id"),
		},
		{
			Name:        "plugin_copy",
			Description: "把已有插件（含内置公众号插件）另存为一个可自由编辑的新插件。内置引擎会自动转成 declarative 模板。",
			InputSchema: obj(map[string]any{
				"id":     idParam,
				"new_id": p("string", "新插件 ID"),
				"name":   p("string", "可选，新显示名称"),
			}, "id", "new_id"),
		},
		{
			Name:        "plugin_delete",
			Description: "删除一个插件（内置插件禁止删除，请先 plugin_copy）。同时删掉目录与配置状态。",
			InputSchema: obj(map[string]any{"id": idParam}, "id"),
		},
		{
			Name:        "plugin_upload",
			Description: "上传/导入插件：传 files（相对路径 -> 文本内容）安装一个文件夹，或传 zip_base64 安装 zip 包。等价于界面上的「本地上传插件」。",
			InputSchema: obj(map[string]any{
				"files":      map[string]any{"type": "object", "description": "文件映射，如 {\"plugin.json\":\"{...}\",\"main.go\":\"package main...\"}"},
				"zip_base64": p("string", "zip 包的 base64（与 files 二选一）"),
				"source":     p("string", "可选来源备注"),
			}),
		},
		{
			Name:        "plugin_export",
			Description: "把插件导出成 zip 的 base64（不含编译产物），便于提交到 Git 或分发给别人。",
			InputSchema: obj(map[string]any{"id": idParam}, "id"),
		},
		{
			Name:        "marketplace_list",
			Description: "读取在线插件市场（对接 Git 仓库的 marketplace.json），列出可安装插件、版本、是否已安装、是否可更新。",
			InputSchema: obj(map[string]any{
				"force": map[string]any{"type": "boolean", "description": "true 跳过缓存强制刷新"},
			}),
		},
		{
			Name:        "marketplace_install",
			Description: "从在线市场安装或更新一个插件（按索引逐文件下载并落地到插件目录）。",
			InputSchema: obj(map[string]any{"id": p("string", "市场里的插件 ID")}, "id"),
		},
		{
			Name:        "plugin_market_config",
			Description: "读取或设置在线市场源。不传参数时只读；传 url 可换成任意 Git 仓库里的 marketplace.json 直链，也可改代理前缀与缓存时长。",
			InputSchema: obj(map[string]any{
				"url":           p("string", "marketplace.json 的 http(s) 直链"),
				"proxy_prefix":  p("string", "代理前缀，如 https://gh-proxy.com/；传空字符串可关闭代理"),
				"cache_minutes": map[string]any{"type": "integer", "description": "缓存分钟数，默认 30"},
				"enabled":       map[string]any{"type": "boolean", "description": "是否启用在线市场"},
			}),
		},
	}
}

// callPluginTool 处理全部插件相关 MCP 工具。
func (s *Server) callPluginTool(name string, args json.RawMessage) (any, error) {
	if s.pm == nil {
		return nil, fmt.Errorf("插件系统未初始化")
	}
	var in map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, fmt.Errorf("参数错误: %w", err)
		}
	}
	if in == nil {
		in = map[string]any{}
	}
	str := func(k string) string {
		v, ok := in[k]
		if !ok || v == nil {
			return "" // 可选参数没传时不能变成 "<nil>"
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}
	id := strings.ToLower(str("id"))

	switch name {
	case "plugin_list":
		return textResult(map[string]any{
			"count": len(s.pm.List()), "plugins": s.pm.List(),
			"warnings": s.pm.Warnings(), "plugins_dir": s.pm.PluginsDir(),
		}, false), nil

	case "plugin_get":
		i, ok := s.pm.Get(id)
		if !ok {
			return nil, fmt.Errorf("插件 %s 不存在，先用 plugin_list 查看", id)
		}
		return textResult(i.View(), false), nil

	case "plugin_create":
		ci := plugin.CreateInput{
			ID: str("id"), Name: str("name"), Engine: str("engine"),
			Version: str("version"), Author: str("author"), Description: str("description"),
			PathPrefix: str("path_prefix"),
		}
		if v, ok := in["domains"].([]any); ok {
			for _, d := range v {
				ci.Domains = append(ci.Domains, fmt.Sprint(d))
			}
		}
		if v, ok := in["config_schema"].([]any); ok {
			b, _ := json.Marshal(v)
			_ = json.Unmarshal(b, &ci.Config)
		}
		i, err := s.pm.Create(ci)
		if err != nil {
			return nil, err
		}
		return textResult(map[string]any{
			"ok": true, "plugin": i.View(),
			"next": "用 plugin_write_file / plugin_patch 写代码，plugin_save 重载，plugin_test 测试，plugin_build 编译（仅 engine=go）",
		}, false), nil

	case "plugin_update_meta":
		i, ok := s.pm.Get(id)
		if !ok {
			return nil, fmt.Errorf("插件 %s 不存在", id)
		}
		var raw []byte
		if mf, ok := in["manifest"]; ok {
			raw, _ = json.Marshal(mf)
		} else if set, ok := in["set"].(map[string]any); ok {
			cur := map[string]any{}
			b, err := i.ReadFile("plugin.json")
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(b, &cur); err != nil {
				return nil, err
			}
			for k, v := range set {
				cur[k] = v
			}
			raw, _ = json.Marshal(cur)
		} else {
			return nil, fmt.Errorf("需要传 manifest 或 set")
		}
		out, err := s.pm.SaveMeta(id, raw)
		if err != nil {
			return nil, err
		}
		return textResult(map[string]any{"ok": true, "plugin": out.View()}, false), nil

	case "plugin_files":
		i, ok := s.pm.Get(id)
		if !ok {
			return nil, fmt.Errorf("插件 %s 不存在", id)
		}
		_ = i.RefreshFiles()
		return textResult(map[string]any{"id": id, "files": i.Files}, false), nil

	case "plugin_read_file":
		i, ok := s.pm.Get(id)
		if !ok {
			return nil, fmt.Errorf("插件 %s 不存在", id)
		}
		b, err := i.ReadFile(str("path"))
		if err != nil {
			return nil, err
		}
		return textResult(map[string]any{"id": id, "path": str("path"), "content": string(b), "size": len(b)}, false), nil

	case "plugin_write_file":
		i, ok := s.pm.Get(id)
		if !ok {
			return nil, fmt.Errorf("插件 %s 不存在", id)
		}
		content := fmt.Sprint(in["content"])
		if b, _ := in["append"].(bool); b {
			if old, err := i.ReadFile(str("path")); err == nil {
				content = string(old) + content
			}
		}
		if err := i.WriteFile(str("path"), []byte(content)); err != nil {
			return nil, err
		}
		if strings.TrimSpace(str("path")) == "plugin.json" {
			_ = s.pm.Reload()
		}
		return textResult(map[string]any{"ok": true, "path": str("path"), "size": len(content), "hint": "改完记得 plugin_save 重载"}, false), nil

	case "plugin_delete_file":
		i, ok := s.pm.Get(id)
		if !ok {
			return nil, fmt.Errorf("插件 %s 不存在", id)
		}
		if err := i.DeleteFile(str("path")); err != nil {
			return nil, err
		}
		return textResult(map[string]any{"ok": true, "deleted": str("path")}, false), nil

	case "plugin_patch":
		i, ok := s.pm.Get(id)
		if !ok {
			return nil, fmt.Errorf("插件 %s 不存在", id)
		}
		var ops []plugin.PatchOp
		if v, ok := in["ops"].([]any); ok {
			b, _ := json.Marshal(v)
			_ = json.Unmarshal(b, &ops)
		}
		if len(ops) == 0 {
			return nil, fmt.Errorf("ops 不能为空")
		}
		results, err := plugin.ApplyPatches(i, ops)
		if err != nil {
			return textResult(map[string]any{"ok": false, "error": err.Error(), "results": results}, true), nil
		}
		_ = s.pm.Reload()
		return textResult(map[string]any{"ok": true, "results": results, "hint": "差量修改已落盘并重载"}, false), nil

	case "plugin_save":
		if err := s.pm.Reload(); err != nil {
			return nil, err
		}
		out := map[string]any{"ok": true, "count": len(s.pm.List()), "warnings": s.pm.Warnings()}
		if id != "" {
			if i, ok := s.pm.Get(id); ok {
				out["plugin"] = i.View()
			}
		}
		return textResult(out, false), nil

	case "plugin_validate":
		mf, ok := in["manifest"]
		if !ok {
			return nil, fmt.Errorf("需要传 manifest")
		}
		raw, _ := json.Marshal(mf)
		parsed, err := plugin.ParseManifest(raw)
		if err != nil {
			return textResult(map[string]any{"ok": false, "error": err.Error()}, true), nil
		}
		return textResult(map[string]any{
			"ok": true, "id": parsed.ID, "engine": parsed.Engine,
			"config_schema": parsed.ConfigSchema, "match": parsed.Match,
		}, false), nil

	case "plugin_build":
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		res, err := s.pm.Build(ctx, id)
		if err != nil {
			return nil, err
		}
		_ = s.pm.Reload()
		return textResult(res, !res.OK), nil

	case "plugin_test", "plugin_debug":
		debug := name == "plugin_debug"
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		res, tr, err := s.pm.Run(ctx, id, str("url"), debug)
		if err != nil {
			return textResult(map[string]any{
				"ok": false, "error": cleanParseError(err.Error()), "trace": tr,
			}, true), nil
		}
		return textResult(map[string]any{
			"ok": true, "items": len(res.Items), "result": res, "trace": tr,
		}, false), nil

	case "parse_url":
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		res, tr, pid, err := s.pm.ParseURL(ctx, str("url"), true)
		if err != nil {
			return textResult(map[string]any{
				"matched": true, "plugin": pid, "ok": false,
				"error": cleanParseError(err.Error()), "trace": tr,
			}, true), nil
		}
		if res == nil {
			return textResult(map[string]any{"matched": false, "hint": "没有插件接管，按普通直链提交即可"}, false), nil
		}
		return textResult(map[string]any{
			"matched": true, "plugin": pid, "ok": true,
			"items": len(res.Items), "result": res, "trace": tr,
		}, false), nil

	case "plugin_config":
		i, ok := s.pm.Get(id)
		if !ok {
			return nil, fmt.Errorf("插件 %s 不存在", id)
		}
		return textResult(map[string]any{
			"id": id, "config": i.State.Config, "config_schema": i.Manifest.ConfigSchema,
		}, false), nil

	case "plugin_set_config":
		cfg := map[string]any{}
		if v, ok := in["config"].(map[string]any); ok {
			cfg = v
		}
		out, err := s.pm.SetConfig(id, cfg)
		if err != nil {
			return nil, err
		}
		return textResult(map[string]any{"ok": true, "id": id, "config": out}, false), nil

	case "plugin_toggle":
		i, ok := s.pm.Get(id)
		if !ok {
			return nil, fmt.Errorf("插件 %s 不存在", id)
		}
		on := !i.State.Enabled
		if v, ok := in["enabled"].(bool); ok {
			on = v
		}
		if err := s.pm.SetEnabled(id, on); err != nil {
			return nil, err
		}
		return textResult(map[string]any{"ok": true, "id": id, "enabled": on}, false), nil

	case "plugin_copy":
		i, err := s.pm.Copy(id, str("new_id"), str("name"))
		if err != nil {
			return nil, err
		}
		return textResult(map[string]any{"ok": true, "plugin": i.View()}, false), nil

	case "plugin_delete":
		if err := s.pm.Delete(id); err != nil {
			return nil, err
		}
		return textResult(map[string]any{"ok": true, "deleted": id}, false), nil

	case "plugin_upload":
		var (
			i   *plugin.Installed
			err error
		)
		if z := str("zip_base64"); z != "" {
			data, e := base64.StdEncoding.DecodeString(z)
			if e != nil {
				return nil, fmt.Errorf("zip_base64 不合法：%w", e)
			}
			i, err = s.pm.ImportZip(bytes.NewReader(data), plugin.OriginLocal, str("source"))
		} else {
			files := map[string]string{}
			if v, ok := in["files"].(map[string]any); ok {
				for k, x := range v {
					files[k] = fmt.Sprint(x)
				}
			}
			i, err = s.pm.ImportBundle(plugin.Bundle{
				Files: files, Origin: plugin.OriginLocal, Source: str("source"),
			})
		}
		if err != nil {
			return nil, err
		}
		return textResult(map[string]any{"ok": true, "plugin": i.View()}, false), nil

	case "plugin_export":
		i, ok := s.pm.Get(id)
		if !ok {
			return nil, fmt.Errorf("插件 %s 不存在", id)
		}
		b, err := i.ExportZip()
		if err != nil {
			return nil, err
		}
		return textResult(map[string]any{
			"ok": true, "id": id, "filename": id + "-" + i.Manifest.Version + ".zip",
			"size": len(b), "zip_base64": base64.StdEncoding.EncodeToString(b),
		}, false), nil

	case "marketplace_list":
		force, _ := in["force"].(bool)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		idx, err := s.pm.MarketList(ctx, force)
		if err != nil {
			return nil, err
		}
		return textResult(map[string]any{
			"ok": true, "name": idx.Name, "repo": idx.Repo,
			"base_url": idx.BaseURL, "updated_at": idx.UpdatedAt,
			"count": len(idx.Plugins), "plugins": idx.Plugins,
		}, false), nil

	case "marketplace_install":
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		i, err := s.pm.MarketInstall(ctx, id)
		if err != nil {
			return nil, err
		}
		return textResult(map[string]any{"ok": true, "plugin": i.View()}, false), nil

	case "plugin_market_config":
		cur := s.pm.MarketConfig()
		_, dirty := in["url"]
		for _, k := range []string{"proxy_prefix", "cache_minutes", "enabled"} {
			if _, ok := in[k]; ok {
				dirty = true
			}
		}
		if !dirty {
			return textResult(map[string]any{"ok": true, "config": cur}, false), nil
		}
		if v, ok := in["url"]; ok && strings.TrimSpace(fmt.Sprint(v)) != "" {
			cur.URL = strings.TrimSpace(fmt.Sprint(v))
		}
		if v, ok := in["proxy_prefix"]; ok {
			cur.ProxyPrefix = strings.TrimSpace(fmt.Sprint(v))
		}
		if v, ok := in["cache_minutes"].(float64); ok {
			cur.CacheMinutes = int(v)
		}
		if v, ok := in["enabled"].(bool); ok {
			cur.Enabled = v
		}
		out, err := s.pm.SetMarketConfig(cur)
		if err != nil {
			return nil, err
		}
		return textResult(map[string]any{"ok": true, "config": out}, false), nil
	}
	return nil, fmt.Errorf("未知插件工具: %s", name)
}
