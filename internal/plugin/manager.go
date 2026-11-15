package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"mediasuper/internal/parser"
)

// Manager 是插件系统的总入口：磁盘目录、状态、注册表、执行引擎、市场。
type Manager struct {
	dataDir    string
	pluginsDir string

	mu      sync.RWMutex
	plugins map[string]*Installed
	state   stateFile
	warns   []string

	marketMu      sync.Mutex
	marketCache   *MarketIndex
	marketFetched time.Time
	marketCfg     MarketConfig
}

// NewManager 创建管理器并扫描数据目录里的插件。
// 调用方需先 RegisterWechatBuiltin()（app.go 里完成）。
func NewManager(dataDir string) (*Manager, error) {
	dir := filepath.Join(dataDir, "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("无法创建插件目录：%w", err)
	}
	m := &Manager{
		dataDir: dataDir, pluginsDir: dir,
		plugins:   map[string]*Installed{},
		marketCfg: DefaultMarketConfig(),
	}
	if err := m.ensureBuiltin(); err != nil {
		return nil, err
	}
	if err := m.loadState(); err != nil {
		return nil, err
	}
	if err := m.Reload(); err != nil {
		return nil, err
	}
	_ = m.loadMarketConfig()
	return m, nil
}

func (m *Manager) statePath() string     { return filepath.Join(m.pluginsDir, "_state.json") }
func (m *Manager) marketCfgPath() string { return filepath.Join(m.pluginsDir, "_market.json") }
func (m *Manager) PluginsDir() string    { return m.pluginsDir }

// ensureBuiltin 首次启动时把内置公众号插件的 plugin.json 落到磁盘，
// 让它和第三方插件一样拥有独立文件夹，可被导出 / 复制 / 查看文件。
func (m *Manager) ensureBuiltin() error {
	dir := filepath.Join(m.pluginsDir, "wechat_mp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p := filepath.Join(dir, "plugin.json")
	if b, err := os.ReadFile(p); err == nil {
		// 已存在则只校验 id/builtin 是否被误改，不覆盖用户的其它编辑
		var cur Manifest
		if json.Unmarshal(b, &cur) == nil && cur.ID == "wechat_mp" && cur.Builtin {
			return nil
		}
	}
	if err := os.WriteFile(p, []byte(wechatManifestJSON), 0o644); err != nil {
		return err
	}
	// 内置插件也提供一份可读的说明
	readme := "# 微信公众号解析（内置插件）\n\n" +
		"实现位于 `internal/parser/wechat_mp.go`，元数据在本目录 `plugin.json`。\n\n" +
		"在插件页可以配置解析接口地址、超时、Token 与「解析后自动下载」。\n" +
		"点「另存为新插件」可复制一份文件夹，再改成自己的 declarative / go 插件。\n"
	return os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme), 0o644)
}

// Reload 重新扫描插件目录，合并状态；解析失败的目录记录警告但不影响其它插件。
func (m *Manager) Reload() error {
	entries, err := os.ReadDir(m.pluginsDir)
	if err != nil {
		return err
	}
	found := map[string]*Installed{}
	warns := []string{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(m.pluginsDir, e.Name())
		b, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
		if err != nil {
			warns = append(warns, fmt.Sprintf("%s：缺少 plugin.json", e.Name()))
			continue
		}
		mf, err := ParseManifest(b)
		if err != nil {
			warns = append(warns, fmt.Sprintf("%s：%v", e.Name(), err))
			continue
		}
		if mf.ID != e.Name() {
			warns = append(warns, fmt.Sprintf("%s：plugin.json 的 id=%q 与目录名不一致，已按目录名忽略", e.Name(), mf.ID))
			continue
		}
		i := &Installed{Manifest: mf, Dir: dir}
		_ = i.RefreshFiles()
		found[mf.ID] = i
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.plugins = found
	m.warns = warns
	for id, i := range found {
		st := m.state.Plugins[id]
		if st == nil {
			origin := OriginLocal
			if i.Manifest.Builtin {
				origin = OriginBuiltin
			}
			st = &PluginState{
				Enabled: true, Origin: origin,
				InstalledAt: time.Now().UTC().Format(time.RFC3339),
			}
			m.state.Plugins[id] = st
		}
		if st.Config == nil {
			st.Config = map[string]any{}
		}
		// 新增的配置项补默认值，删掉的项不强行移除（保留用户历史值）
		for k, v := range i.Manifest.DefaultConfig() {
			if _, ok := st.Config[k]; !ok {
				st.Config[k] = v
			}
		}
		if i.Manifest.Builtin {
			st.Origin = OriginBuiltin
		}
		i.State = st
	}
	return m.saveStateLocked()
}

// Get 返回一份已安装插件（只读使用）。
func (m *Manager) Get(id string) (*Installed, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	i, ok := m.plugins[strings.ToLower(strings.TrimSpace(id))]
	return i, ok
}

// List 返回排序后的插件视图（内置在前，同优先级按 id）。
func (m *Manager) List() []View {
	m.mu.RLock()
	items := make([]*Installed, 0, len(m.plugins))
	for _, i := range m.plugins {
		items = append(items, i)
	}
	m.mu.RUnlock()
	sort.Slice(items, func(a, b int) bool {
		if items[a].Manifest.Builtin != items[b].Manifest.Builtin {
			return items[a].Manifest.Builtin
		}
		if items[a].Manifest.Match.Priority != items[b].Manifest.Match.Priority {
			return items[a].Manifest.Match.Priority > items[b].Manifest.Match.Priority
		}
		return items[a].Manifest.ID < items[b].Manifest.ID
	})
	out := make([]View, 0, len(items))
	for _, i := range items {
		out = append(out, i.View())
	}
	return out
}

// Warnings 返回扫描到的不规范目录。
func (m *Manager) Warnings() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]string{}, m.warns...)
}

// ---------- 匹配 ----------

func domainMatch(host, pattern string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return false
	}
	if strings.HasPrefix(pattern, "*.") {
		base := strings.TrimPrefix(pattern, "*.")
		return host == base || strings.HasSuffix(host, "."+base)
	}
	return host == pattern
}

// MatchRuleOK 判断一条 URL 是否命中插件的匹配规则。
func MatchRuleOK(mf *Manifest, raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	r := mf.Match
	hasRule := len(r.Domains)+len(r.URLPatterns)+len(r.PathPrefixes)+len(r.Extensions) > 0
	if !hasRule {
		return false // 没有声明域名等规则的插件只可被显式调用（测试 / MCP）
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if len(r.Schemes) > 0 {
		ok := false
		for _, s := range r.Schemes {
			if strings.EqualFold(u.Scheme, s) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(r.Domains) > 0 {
		ok := false
		for _, d := range r.Domains {
			if domainMatch(host, d) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(r.PathPrefixes) > 0 {
		ok := false
		for _, p := range r.PathPrefixes {
			if strings.HasPrefix(u.Path, p) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(r.Extensions) > 0 {
		ext := strings.ToLower(filepath.Ext(u.Path))
		ok := false
		for _, e := range r.Extensions {
			if strings.EqualFold(ext, e) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	for _, p := range r.URLPatterns {
		if re, err := compileCached(p); err == nil && re.MatchString(raw) {
			return true
		}
	}
	// 有 url_patterns 但没命中，则整条规则不通过；否则前面几组条件全通过即可
	return len(r.URLPatterns) == 0
}

var reCache sync.Map

func compileCached(p string) (*regexp.Regexp, error) {
	if v, ok := reCache.Load(p); ok {
		return v.(*regexp.Regexp), nil
	}
	r, err := regexp.Compile(p)
	if err != nil {
		return nil, err
	}
	reCache.Store(p, r)
	return r, nil
}

// Match 返回第一个接管该链接的已启用插件。
func (m *Manager) Match(raw string) (*Installed, bool) {
	m.mu.RLock()
	items := make([]*Installed, 0, len(m.plugins))
	for _, i := range m.plugins {
		items = append(items, i)
	}
	m.mu.RUnlock()
	sort.Slice(items, func(a, b int) bool {
		x, y := items[a], items[b]
		if x.Manifest.Match.Priority != y.Manifest.Match.Priority {
			return x.Manifest.Match.Priority > y.Manifest.Match.Priority
		}
		return x.Manifest.ID < y.Manifest.ID
	})
	for _, i := range items {
		if !i.State.Enabled {
			continue
		}
		if MatchRuleOK(i.Manifest, raw) {
			return i, true
		}
	}
	return nil, false
}

// ParseURL 自动匹配插件并执行；未命中返回 (nil, nil, "", nil)。
func (m *Manager) ParseURL(ctx context.Context, rawURL string, debug bool) (*parser.Result, *Trace, string, error) {
	i, ok := m.Match(rawURL)
	if !ok {
		return nil, nil, "", nil
	}
	res, tr, err := m.Run(ctx, i.Manifest.ID, rawURL, debug)
	return res, tr, i.Manifest.ID, err
}

// ---------- 增删改 ----------

func (m *Manager) Create(in CreateInput) (*Installed, error) {
	mf, err := scaffoldManifest(in)
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	b, _ := manifestBytes(mf)
	files["plugin.json"] = string(b)
	files["README.md"] = scaffoldReadme(mf)
	if mf.Engine == EngineGo {
		files["main.go"] = goMainSource(mf)
		files["go.mod"] = fmt.Sprintf("module %s\n\ngo 1.24\n", mf.ID)
	}
	return m.installFiles(mf.ID, Bundle{Files: files, Origin: OriginLocal}, true)
}

// ImportBundle 从内联文件包安装 / 覆盖插件。
func (m *Manager) ImportBundle(b Bundle) (*Installed, error) {
	raw, ok := b.Files["plugin.json"]
	if !ok {
		// 有些上传会把文件放在一层目录下
		for k, v := range b.Files {
			if strings.HasSuffix(k, "/plugin.json") {
				raw = v
				prefix := strings.TrimSuffix(k, "plugin.json")
				nb := Bundle{Files: map[string]string{}, Binary: map[string]string{}, Origin: b.Origin, Source: b.Source}
				for kk, vv := range b.Files {
					nb.Files[strings.TrimPrefix(kk, prefix)] = vv
				}
				for kk, vv := range b.Binary {
					nb.Binary[strings.TrimPrefix(kk, prefix)] = vv
				}
				b = nb
				ok = true
				break
			}
		}
	}
	if !ok {
		return nil, fmt.Errorf("插件包里缺少 plugin.json")
	}
	mf, err := ParseManifest([]byte(raw))
	if err != nil {
		return nil, err
	}
	return m.installFiles(mf.ID, b, false)
}

func (m *Manager) installFiles(id string, b Bundle, failIfExists bool) (*Installed, error) {
	dir := filepath.Join(m.pluginsDir, id)
	if failIfExists {
		if _, err := os.Stat(filepath.Join(dir, "plugin.json")); err == nil {
			return nil, fmt.Errorf("插件 %s 已存在，请换一个 id 或先删除", id)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	i := &Installed{Dir: dir}
	for rel, content := range b.Files {
		p, err := i.safe(rel)
		if err != nil {
			return nil, fmt.Errorf("文件 %s：%w", rel, err)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := atomicWrite(p, []byte(content)); err != nil {
			return nil, err
		}
	}
	for rel, content := range b.Binary {
		p, err := i.safe(rel)
		if err != nil {
			return nil, fmt.Errorf("文件 %s：%w", rel, err)
		}
		data, err := decodeBase64(content)
		if err != nil {
			return nil, fmt.Errorf("文件 %s 的 base64 不合法：%w", rel, err)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := atomicWrite(p, data); err != nil {
			return nil, err
		}
	}
	// 重新读取并校验，保证落盘的是合法 manifest
	mf, err := ParseManifest(mustRead(filepath.Join(dir, "plugin.json")))
	if err != nil {
		return nil, err
	}
	origin := b.Origin
	if origin == "" {
		origin = OriginLocal
	}
	m.mu.Lock()
	st := m.state.Plugins[id]
	if st == nil {
		st = &PluginState{InstalledAt: time.Now().UTC().Format(time.RFC3339)}
		m.state.Plugins[id] = st
	}
	st.Enabled = true
	st.Origin = origin
	st.Source = b.Source
	// 覆盖安装时保留用户已填配置，只补新增项默认值
	if st.Config == nil {
		st.Config = map[string]any{}
	}
	for k, v := range mf.DefaultConfig() {
		if _, exists := st.Config[k]; !exists {
			st.Config[k] = v
		}
	}
	err = m.saveStateLocked()
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := m.Reload(); err != nil {
		return nil, err
	}
	out, _ := m.Get(id)
	return out, nil
}

func mustRead(p string) []byte {
	b, _ := os.ReadFile(p)
	return b
}

// SaveMeta 用一份新的 plugin.json 覆盖元数据（id / builtin 不允许改）。
func (m *Manager) SaveMeta(id string, raw []byte) (*Installed, error) {
	i, ok := m.Get(id)
	if !ok {
		return nil, fmt.Errorf("插件 %s 不存在", id)
	}
	mf, err := ParseManifest(raw)
	if err != nil {
		return nil, err
	}
	if mf.ID != id {
		return nil, fmt.Errorf("不能把 id 从 %s 改成 %s（id 与目录名绑定）", id, mf.ID)
	}
	if i.Manifest.Builtin {
		mf.Builtin = true
		mf.Engine = i.Manifest.Engine // 内置实现由主程序提供，禁止改成别的引擎
	}
	if err := i.WriteManifest(mf); err != nil {
		return nil, err
	}
	if err := m.Reload(); err != nil {
		return nil, err
	}
	out, _ := m.Get(id)
	return out, nil
}

// Delete 删除插件目录（内置插件禁止删除，请先「另存为新插件」）。
func (m *Manager) Delete(id string) error {
	i, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("插件 %s 不存在", id)
	}
	if i.Manifest.Builtin {
		return fmt.Errorf("内置插件不能删除，可以禁用；如需自定义请点「另存为新插件」")
	}
	m.mu.Lock()
	delete(m.state.Plugins, id)
	err := m.saveStateLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(i.Dir); err != nil {
		return err
	}
	return m.Reload()
}

// SetEnabled 启用 / 禁用插件。
func (m *Manager) SetEnabled(id string, on bool) error {
	i, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("插件 %s 不存在", id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	i.State.Enabled = on
	return m.saveStateLocked()
}

// SetConfig 保存可视化配置值。
func (m *Manager) SetConfig(id string, cfg map[string]any) (map[string]any, error) {
	i, ok := m.Get(id)
	if !ok {
		return nil, fmt.Errorf("插件 %s 不存在", id)
	}
	merged, err := i.Manifest.ValidateConfig(cfg)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	i.State.Config = merged
	err = m.saveStateLocked()
	m.mu.Unlock()
	return merged, err
}

// Copy 把已有插件（含内置）复制成一个可自由编辑的新插件。
func (m *Manager) Copy(id, newID, newName string) (*Installed, error) {
	src, ok := m.Get(id)
	if !ok {
		return nil, fmt.Errorf("插件 %s 不存在", id)
	}
	newID = strings.ToLower(strings.TrimSpace(newID))
	if !rePluginID.MatchString(newID) {
		return nil, fmt.Errorf("新 id 不合法（2–48 位小写字母开头，只含 a-z/0-9/_-）")
	}
	if _, exists := m.Get(newID); exists {
		return nil, fmt.Errorf("插件 %s 已存在", newID)
	}
	files := map[string]string{}
	bin := map[string]string{}
	for _, f := range src.Files {
		if f.IsDir || strings.HasPrefix(f.Path, "bin/") {
			continue
		}
		b, err := src.ReadFile(f.Path)
		if err != nil {
			continue
		}
		if isTextFile(f.Path) && isUTF8(b) {
			files[f.Path] = string(b)
		} else {
			bin[f.Path] = encodeBase64(b)
		}
	}
	mf := *src.Manifest
	mf.ID = newID
	mf.Builtin = false
	mf.Name = firstNonEmpty(strings.TrimSpace(newName), src.Manifest.Name+" 副本")
	if mf.Engine == EngineBuiltin {
		// 内置实现无法复制，转成 declarative 模板并给出可运行的规则骨架
		mf.Engine = EngineDeclarative
		mf.Declarative = scaffoldDeclarativeForWechat()
		mf.Entry = Entry{}
		mf.MinAppVersion = ""
	}
	if mf.Engine == EngineGo {
		mf.Entry.Binary = "bin/" + newID
	}
	b, _ := manifestBytes(&mf)
	files["plugin.json"] = string(b)
	return m.installFiles(newID, Bundle{Files: files, Binary: bin, Origin: OriginLocal}, true)
}

func scaffoldDeclarativeForWechat() *Declarative {
	return &Declarative{
		Request: DeclarativeRequest{
			Method: "GET", URL: "{{config.api_base}}",
			Query:      map[string]string{"url": "{{source_url}}"},
			Headers:    map[string]string{"Accept": "application/json", "Authorization": "{{config.api_token}}"},
			TimeoutSec: 15,
		},
		Response: DeclarativeResponse{
			Format: "json", SuccessWhen: "code == 1", ErrorField: "msg",
			Fields: map[string]string{
				"title": "data.title", "cover": "data.cover", "author": "data.author",
				"description": "data.description", "publish_time": "data.publish_time",
				"content": "data.content", "images": "data.images",
			},
			Items: &DeclarativeItems{
				From: "data.movie_down_url", URL: "{{value}}", Title: "{{title}} · {{index1}}",
				Kind: "video", Format: "mp4", Max: 100,
				Headers: map[string]string{"Referer": "https://mp.weixin.qq.com/"},
			},
		},
	}
}
