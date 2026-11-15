package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	RegisterWechatBuiltin()
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func TestBuiltinInstalledAndConfigurable(t *testing.T) {
	m := newTestManager(t)
	list := m.List()
	if len(list) != 1 {
		t.Fatalf("期望 1 个内置插件，实际 %d", len(list))
	}
	v := list[0]
	if v.ID != "wechat_mp" || !v.Builtin || !v.Enabled || v.Origin != OriginBuiltin {
		t.Fatalf("内置插件信息不符: %+v", v)
	}
	if len(v.ConfigSchema) < 3 {
		t.Fatalf("内置插件应有配置项，实际 %d", len(v.ConfigSchema))
	}
	if v.Config["api_base"] == "" {
		t.Fatal("api_base 应补上默认值")
	}
	// 匹配域名 / 路径
	if !MatchRuleOK(v2Manifest(t, m, "wechat_mp"), "https://mp.weixin.qq.com/s/AbCd") {
		t.Fatal("公众号 /s/ 链接应命中")
	}
	if MatchRuleOK(v2Manifest(t, m, "wechat_mp"), "https://mp.weixin.qq.com/other") {
		t.Fatal("非 /s 路径不应命中")
	}
	if MatchRuleOK(v2Manifest(t, m, "wechat_mp"), "https://example.com/s/AbCd") {
		t.Fatal("其它域名不应命中")
	}
}

func v2Manifest(t *testing.T, m *Manager, id string) *Manifest {
	t.Helper()
	i, ok := m.Get(id)
	if !ok {
		t.Fatalf("插件 %s 不存在", id)
	}
	return i.Manifest
}

func TestManifestValidation(t *testing.T) {
	base := `{"id":"my_site","name":"我的站点","version":"1.0.0","engine":"declarative",` +
		`"match":{"domains":["example.com"]},` +
		`"config_schema":[{"key":"api_base","label":"接口","type":"url","required":true,"default":"https://a.com"}],` +
		`"declarative":{"request":{"url":"{{config.api_base}}"},"response":{"format":"json"}}}`
	cases := []struct {
		name, raw, wantErr string
	}{
		{"合法", base, ""},
		{"未知字段", strings.Replace(base, `"name":"我的站点",`, `"name":"我的站点","bogus":1,`, 1), "未知字段"},
		{"id 数字开头", strings.Replace(base, `"my_site"`, `"1bad"`, 1), "id 不合法"},
		{"id 含非法字符", strings.Replace(base, `"my_site"`, `"a b"`, 1), "id 不合法"},
		{"版本错", strings.Replace(base, `"1.0.0"`, `"one"`, 1), "version"},
		{"引擎未知", strings.Replace(base, `"declarative"`, `"lua"`, 1), "engine 只能"},
		{"声明式缺规则", `{"id":"a_b","name":"A","version":"1.0.0","engine":"declarative"}`, "declarative"},
		{"select 缺选项", strings.Replace(base, `"type":"url"`, `"type":"select"`, 1), "options"},
		{"正则非法", strings.Replace(base, `"domains":["example.com"]`, `"url_patterns":["(["]`, 1), "正则不合法"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(c.raw))
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("期望通过，实际: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("期望错误含 %q，实际: %v", c.wantErr, err)
			}
		})
	}
}

func TestMatchRule(t *testing.T) {
	// ① 域名通配 + 路径前缀 + 协议（无正则）
	mf := &Manifest{Match: MatchRule{
		Domains:      []string{"*.example.com", "plain.cn"},
		PathPrefixes: []string{"/video"},
		Schemes:      []string{"https"},
	}}
	ok := []string{"https://a.example.com/video/123", "https://example.com/video/9", "https://plain.cn/video/1"}
	no := []string{"http://a.example.com/video/1", "https://a.example.com/article/1", "https://other.com/video/1"}
	for _, u := range ok {
		if !MatchRuleOK(mf, u) {
			t.Errorf("应命中: %s", u)
		}
	}
	for _, u := range no {
		if MatchRuleOK(mf, u) {
			t.Errorf("不应命中: %s", u)
		}
	}
	// ② 写了 url_patterns 就必须至少命中一条（与前面字段是「与」关系）
	mf2 := &Manifest{Match: MatchRule{
		Domains:     []string{"*.example.com"},
		URLPatterns: []string{`^https://.*example\.com/video/\d+$`},
	}}
	if !MatchRuleOK(mf2, "https://a.example.com/video/123") {
		t.Error("正则应命中数字路径")
	}
	if MatchRuleOK(mf2, "https://a.example.com/video/abc") {
		t.Error("正则未命中时整条规则应失败")
	}
	if MatchRuleOK(mf2, "https://other.com/video/123") {
		t.Error("域名不匹配时正则也不应放行")
	}
	// ③ 扩展名
	mf3 := &Manifest{Match: MatchRule{Domains: []string{"cdn.com"}, Extensions: []string{".m3u8", ".mp4"}}}
	if !MatchRuleOK(mf3, "https://cdn.com/a.MP4") {
		t.Error("扩展名应大小写不敏感")
	}
	if MatchRuleOK(mf3, "https://cdn.com/a.ts") {
		t.Error("不在扩展名列表里不应命中")
	}
	empty := &Manifest{}
	if MatchRuleOK(empty, "https://x.com") {
		t.Error("没有规则时不应自动接管")
	}
}

func TestTemplateAndPath(t *testing.T) {
	c := templateContext{
		Config:    map[string]any{"token": "abc", "n": float64(3)},
		SourceURL: "https://site.com/p", Title: "标题",
		Value: map[string]any{"url": "https://cdn/a.mp4"}, Index: 0,
	}
	cases := map[string]string{
		"{{config.token}}":                  "abc",
		"{{source_url}}":                    "https://site.com/p",
		"{{title}} · {{index1}}":            "标题 · 1",
		"{{item.url}}":                      "https://cdn/a.mp4",
		"{{config.missing}}x":               "x",
		"Bearer {{config.token}}/{{index}}": "Bearer abc/0",
	}
	for in, want := range cases {
		if got := renderTpl(in, c); got != want {
			t.Errorf("renderTpl(%q) = %q, want %q", in, got, want)
		}
	}
	var root any
	_ = json.Unmarshal([]byte(`{"data":{"list":[{"url":"u1"},{"url":"u2"}],"title":"T"}}`), &root)
	if got := stringify(getPath(root, "data.list[1].url")); got != "u2" {
		t.Errorf("getPath = %q, want u2", got)
	}
	if got := stringify(getPath(root, "data.title")); got != "T" {
		t.Errorf("getPath = %q, want T", got)
	}
}

func TestValidateConfig(t *testing.T) {
	mf := &Manifest{ConfigSchema: []ConfigField{
		{Key: "api_base", Label: "接口", Type: TypeURL, Required: true},
		{Key: "timeout", Label: "超时", Type: TypeNumber, Min: floatPtr(3), Max: floatPtr(60)},
		{Key: "quality", Label: "清晰度", Type: TypeSelect, Options: []SelectOption{{Label: "高", Value: "best"}}},
		{Key: "auto", Label: "自动", Type: TypeSwitch},
	}}
	if _, err := mf.ValidateConfig(map[string]any{}); err == nil {
		t.Fatal("必填项缺失应报错")
	}
	if _, err := mf.ValidateConfig(map[string]any{"api_base": "ftp://x"}); err == nil {
		t.Fatal("url 类型应校验协议")
	}
	if _, err := mf.ValidateConfig(map[string]any{"api_base": "https://x", "timeout": 999}); err == nil {
		t.Fatal("number 超范围应报错")
	}
	if _, err := mf.ValidateConfig(map[string]any{"api_base": "https://x", "quality": "bad"}); err == nil {
		t.Fatal("select 非法值应报错")
	}
	out, err := mf.ValidateConfig(map[string]any{"api_base": "https://x", "timeout": "20", "quality": "best", "auto": "true"})
	if err != nil {
		t.Fatalf("合法配置不应报错: %v", err)
	}
	if out["timeout"] != float64(20) || out["auto"] != true {
		t.Fatalf("类型归一化失败: %+v", out)
	}
}

func floatPtr(v float64) *float64 { return &v }

func TestCreateApplyPatchesAndDelete(t *testing.T) {
	m := newTestManager(t)
	i, err := m.Create(CreateInput{ID: "my_site", Name: "我的站点", Engine: EngineDeclarative, Domains: []string{"example.com"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if i.Manifest.Engine != EngineDeclarative || i.Manifest.Declarative == nil {
		t.Fatal("脚手架应生成 declarative 规则")
	}
	if _, err := m.Create(CreateInput{ID: "my_site", Name: "重复"}); err == nil {
		t.Fatal("同 id 不应重复创建")
	}
	b, err := i.ReadFile("plugin.json")
	if err != nil || !strings.Contains(string(b), `"id": "my_site"`) {
		t.Fatal("plugin.json 未正确落盘")
	}
	// 差量修改
	res, err := ApplyPatches(i, []PatchOp{
		{Path: "plugin.json", Action: "replace", Find: `"version": "1.0.0"`, Replace: `"version": "1.0.1"`},
		{Path: "README.md", Action: "insert", Anchor: "", Content: "\n## 追加章节\n"},
	})
	if err != nil {
		t.Fatalf("ApplyPatches: %v (%+v)", err, res)
	}
	if !res[0].OK || res[0].Changed != 1 {
		t.Fatalf("patch 结果异常: %+v", res[0])
	}
	if err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := v2Manifest(t, m, "my_site").Version; got != "1.0.1" {
		t.Fatalf("重载后版本应为 1.0.1，实际 %s", got)
	}
	// 差量失败应整体不落盘
	_, err = ApplyPatches(i, []PatchOp{
		{Path: "README.md", Action: "insert", Anchor: "", Content: "OK"},
		{Path: "plugin.json", Action: "replace", Find: "根本不存在", Replace: "x"},
	})
	if err == nil {
		t.Fatal("其中一个 op 失败时应整体失败")
	}
	rb, _ := i.ReadFile("README.md")
	if strings.Contains(string(rb), "OK") {
		t.Fatal("失败时不应部分落盘")
	}
	// 内置不可删，普通可删
	if err := m.Delete("wechat_mp"); err == nil {
		t.Fatal("内置插件不应允许删除")
	}
	if err := m.Delete("my_site"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := m.Get("my_site"); ok {
		t.Fatal("删除后仍能查到")
	}
}

func TestCopyBuiltinBecomesDeclarative(t *testing.T) {
	m := newTestManager(t)
	i, err := m.Copy("wechat_mp", "wx_copy", "公众号副本")
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if i.Manifest.Builtin || i.Manifest.Engine != EngineDeclarative || i.Manifest.Declarative == nil {
		t.Fatalf("内置副本应转 declarative: %+v", i.Manifest)
	}
	if i.State.Origin != OriginLocal {
		t.Fatal("副本来源应为 local")
	}
}

func TestDeclarativeRun(t *testing.T) {
	// 模拟解析接口：返回 1 个标题 + 2 条视频直链
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("url") == "" {
			t.Error("query 里应带 source_url")
		}
		if r.Header.Get("X-Token") != "secret" {
			t.Errorf("配置注入失败: X-Token=%q", r.Header.Get("X-Token"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"code":1,"msg":"ok","data":{"title":"测试文章","cover":"https://cdn/c.jpg",`+
			`"author":"作者","content":"<p>正文</p>","images":["https://cdn/1.jpg"],`+
			`"movie_down_url":["https://cdn/a.mp4","javascript:bad","https://cdn/b.mp4"]}}`)
	}))
	defer srv.Close()

	raw := fmt.Sprintf(`{
	  "id":"decl_site","name":"声明式测试","version":"1.0.0","engine":"declarative",
	  "match":{"domains":["example.com"]},
	  "config_schema":[{"key":"api_base","label":"接口","type":"url","default":"%s","required":true},
	                   {"key":"tok","label":"Token","type":"password","default":"secret"}],
	  "declarative":{
	    "request":{"method":"GET","url":"{{config.api_base}}","query":{"url":"{{source_url}}"},
	               "headers":{"X-Token":"{{config.tok}}"},"timeout_sec":10},
	    "response":{"format":"json","success_when":"code == 1","error_field":"msg",
	      "fields":{"title":"data.title","cover":"data.cover","author":"data.author","content":"data.content","images":"data.images"},
	      "items":{"from":"data.movie_down_url","url":"{{value}}","title":"{{title}} · {{index1}}",
	               "kind":"video","format":"mp4","headers":{"Referer":"{{source_url}}"}}}
	  }
	}`, srv.URL)
	m := newTestManager(t)
	if _, err := m.ImportBundle(Bundle{Files: map[string]string{"plugin.json": raw}}); err != nil {
		t.Fatalf("导入: %v", err)
	}
	res, tr, err := m.Run(context.Background(), "decl_site", "https://example.com/page/1", true)
	if err != nil {
		t.Fatalf("Run: %v (trace=%+v)", err, tr)
	}
	if res.Title != "测试文章" || res.Author != "作者" || len(res.Images) != 1 {
		t.Fatalf("字段映射异常: %+v", res)
	}
	if len(res.Items) != 2 {
		t.Fatalf("应过滤掉非 http 直链，剩 2 条，实际 %d", len(res.Items))
	}
	if res.Items[0].URL != "https://cdn/a.mp4" || res.Items[1].Title != "测试文章 · 3" {
		t.Fatalf("items 映射异常: %+v", res.Items)
	}
	if res.Items[0].Headers["Referer"] != "https://example.com/page/1" {
		t.Fatal("防盗链头模板未渲染")
	}
	if tr.DurationMS < 0 || len(tr.Steps) == 0 || tr.Request == "" || tr.Response == "" {
		t.Fatalf("调试信息不完整: %+v", tr)
	}
}

func TestDeclarativeFailureAndSuccessWhen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"msg":"链接已失效"}`)
	}))
	defer srv.Close()
	raw := fmt.Sprintf(`{"id":"fail_site","name":"失败测试","version":"1.0.0","engine":"declarative",
	  "match":{"domains":["x.com"]},"declarative":{"request":{"url":"%s"},
	  "response":{"success_when":"code == 1","error_field":"msg"}}}`, srv.URL)
	m := newTestManager(t)
	if _, err := m.ImportBundle(Bundle{Files: map[string]string{"plugin.json": raw}}); err != nil {
		t.Fatal(err)
	}
	_, _, err := m.Run(context.Background(), "fail_site", "https://x.com/a", false)
	if err == nil || !strings.Contains(err.Error(), "链接已失效") {
		t.Fatalf("应返回业务错误，实际: %v", err)
	}
}

func TestConfigAndToggleAndMatch(t *testing.T) {
	m := newTestManager(t)
	if err := m.SetEnabled("wechat_mp", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Match("https://mp.weixin.qq.com/s/AbCd"); ok {
		t.Fatal("禁用后不应再命中")
	}
	_ = m.SetEnabled("wechat_mp", true)
	if i, ok := m.Match("https://mp.weixin.qq.com/s/AbCd"); !ok || i.Manifest.ID != "wechat_mp" {
		t.Fatal("启用后应命中内置插件")
	}
	if _, err := m.SetConfig("wechat_mp", map[string]any{"timeout_sec": 999}); err == nil {
		t.Fatal("超出 max 应被拒")
	}
	cfg, err := m.SetConfig("wechat_mp", map[string]any{"timeout_sec": 20, "auto_download": true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg["auto_download"] != true {
		t.Fatal("配置未保存")
	}
	// 状态持久化：重新加载后配置与启停保留
	m2, err := NewManager(m.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	i2, ok := m2.Get("wechat_mp")
	if !ok || i2.State.Config["timeout_sec"] != float64(20) {
		t.Fatalf("状态未持久化: %+v", i2)
	}
}

func TestImportZipAndExport(t *testing.T) {
	m := newTestManager(t)
	i, err := m.Create(CreateInput{ID: "zip_site", Name: "Zip 测试", Domains: []string{"zip.com"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := i.ExportZip()
	if err != nil || len(b) < 100 {
		t.Fatalf("导出失败: %v (%d bytes)", err, len(b))
	}
	if err := m.Delete("zip_site"); err != nil {
		t.Fatal(err)
	}
	i2, err := m.ImportZip(bytes.NewReader(b), OriginLocal, "测试导入")
	if err != nil {
		t.Fatalf("ImportZip: %v", err)
	}
	if i2.Manifest.ID != "zip_site" {
		t.Fatalf("导入后 id 不符: %s", i2.Manifest.ID)
	}
}

func TestMarketplaceIndexInRepo(t *testing.T) {
	// 校验仓库根目录的市场索引与示例插件都能被严格解析
	root := filepath.Join("..", "..")
	idxRaw, err := os.ReadFile(filepath.Join(root, "marketplace.json"))
	if err != nil {
		t.Skipf("未找到 marketplace.json: %v", err)
	}
	var idx MarketIndex
	if err := json.Unmarshal(idxRaw, &idx); err != nil {
		t.Fatalf("marketplace.json 非法: %v", err)
	}
	if len(idx.Plugins) == 0 {
		t.Fatal("市场索引里没有插件")
	}
	for _, it := range idx.Plugins {
		if len(it.Files) == 0 {
			t.Errorf("%s 没有 files", it.ID)
		}
		p := filepath.Join(root, idx.BaseURL, it.Path, "plugin.json")
		b, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("%s 的 plugin.json 不存在: %v", it.ID, err)
			continue
		}
		mf, err := ParseManifest(b)
		if err != nil {
			t.Errorf("%s 的 plugin.json 不合规: %v", it.ID, err)
			continue
		}
		if mf.ID != it.ID {
			t.Errorf("%s 的 id 与目录不一致: %s", it.ID, mf.ID)
		}
		for _, f := range it.Files {
			if _, err := os.Stat(filepath.Join(root, idx.BaseURL, it.Path, f)); err != nil {
				t.Errorf("%s 缺少声明的文件 %s", it.ID, f)
			}
		}
	}
}
