package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

// 在线插件市场直接对接 Git 仓库：仓库根目录放一份 marketplace.json 索引，
// 每个插件一个文件夹。客户端按索引里的 base_url + path + files 逐文件下载，
// 不需要额外的对象存储或发布流水线，git push 即上架。
const defaultMarketURL = "https://raw.githubusercontent.com/Mr-losfer/media_downloads/main/marketplace.json"
const defaultProxyPrefix = "https://gh-proxy.com/"

// MarketConfig 是市场源配置，保存在 <data>/plugins/_market.json。
type MarketConfig struct {
	URL          string `json:"url"`
	ProxyPrefix  string `json:"proxy_prefix,omitempty"`
	CacheMinutes int    `json:"cache_minutes,omitempty"`
	Enabled      bool   `json:"enabled"`
}

func DefaultMarketConfig() MarketConfig {
	return MarketConfig{URL: defaultMarketURL, ProxyPrefix: defaultProxyPrefix, CacheMinutes: 30, Enabled: true}
}

// MarketItem 是 marketplace.json 里的一条插件记录。
type MarketItem struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Description   string   `json:"description,omitempty"`
	Author        string   `json:"author,omitempty"`
	Homepage      string   `json:"homepage,omitempty"`
	Icon          string   `json:"icon,omitempty"`
	Engine        string   `json:"engine,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	Path          string   `json:"path,omitempty"` // 相对 base_url 的目录
	Files         []string `json:"files"`          // 目录内文件清单
	MinAppVersion string   `json:"min_app_version,omitempty"`
	// 运行期填充
	Installed        bool   `json:"installed"`
	InstalledVersion string `json:"installed_version,omitempty"`
	UpdateAvailable  bool   `json:"update_available"`
}

// MarketIndex 是仓库根目录 marketplace.json 的结构。
type MarketIndex struct {
	SchemaVersion string       `json:"schema_version"`
	Name          string       `json:"name"`
	Owner         string       `json:"owner,omitempty"`
	Repo          string       `json:"repo,omitempty"`
	UpdatedAt     string       `json:"updated_at,omitempty"`
	BaseURL       string       `json:"base_url"`
	Plugins       []MarketItem `json:"plugins"`
	// 运行期字段
	SourceURL string `json:"source_url,omitempty"`
}

func (m *Manager) loadMarketConfig() error {
	b, err := os.ReadFile(m.marketCfgPath())
	if err != nil {
		return nil // 用默认值
	}
	var c MarketConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return err
	}
	if strings.TrimSpace(c.URL) == "" {
		c.URL = defaultMarketURL
	}
	m.marketCfg = c
	return nil
}

func (m *Manager) MarketConfig() MarketConfig {
	m.marketMu.Lock()
	defer m.marketMu.Unlock()
	return m.marketCfg
}

func (m *Manager) SetMarketConfig(c MarketConfig) (MarketConfig, error) {
	c.URL = strings.TrimSpace(c.URL)
	if c.URL == "" {
		return MarketConfig{}, fmt.Errorf("市场地址不能为空")
	}
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return MarketConfig{}, fmt.Errorf("市场地址必须是 http(s) 的 marketplace.json 直链")
	}
	if c.CacheMinutes <= 0 {
		c.CacheMinutes = 30
	}
	m.marketMu.Lock()
	m.marketCfg = c
	m.marketCache = nil
	m.marketFetched = time.Time{}
	m.marketMu.Unlock()
	b, _ := json.MarshalIndent(c, "", "  ")
	if err := atomicWrite(m.marketCfgPath(), b); err != nil {
		return MarketConfig{}, err
	}
	return c, nil
}

// candidates 生成下载地址尝试顺序：代理优先（国内 NAS 直连 raw 常失败），再直连。
func candidates(raw string, proxy string) []string {
	out := []string{}
	p := strings.TrimSpace(proxy)
	if p != "" && !strings.HasPrefix(raw, p) {
		out = append(out, strings.TrimSuffix(p, "/")+"/"+raw)
	}
	out = append(out, raw)
	return out
}

func httpGet(ctx context.Context, raw, proxy string) ([]byte, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	var last error
	for _, u := range candidates(raw, proxy) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			last = err
			continue
		}
		req.Header.Set("User-Agent", "MediaSuper-Market/1.0")
		resp, err := client.Do(req)
		if err != nil {
			last = err
			continue
		}
		if resp.StatusCode != 200 {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
			resp.Body.Close()
			last = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			last = err
			continue
		}
		return b, nil
	}
	return nil, fmt.Errorf("下载失败（已尝试代理与直连）：%v", last)
}

func resolveBase(indexURL, base string) string {
	base = strings.TrimSpace(base)
	if strings.HasPrefix(base, "http://") || strings.HasPrefix(base, "https://") {
		return strings.TrimSuffix(base, "/")
	}
	// 相对路径：相对 marketplace.json 所在目录
	u, err := url.Parse(indexURL)
	if err != nil {
		return strings.TrimSuffix(base, "/")
	}
	u.Path = path.Join(path.Dir(u.Path), base)
	u.RawQuery = ""
	return strings.TrimSuffix(u.String(), "/")
}

func joinFile(base, p, file string) string {
	parts := []string{strings.TrimSuffix(base, "/")}
	if strings.TrimSpace(p) != "" {
		parts = append(parts, strings.Trim(strings.TrimSpace(p), "/"))
	}
	parts = append(parts, strings.TrimPrefix(file, "/"))
	return strings.Join(parts, "/")
}

// MarketList 拉取（或读缓存）市场索引，并标记本地已安装 / 可更新。
func (m *Manager) MarketList(ctx context.Context, force bool) (*MarketIndex, error) {
	cfg := m.MarketConfig()
	if !cfg.Enabled {
		return &MarketIndex{Name: "在线插件市场（已停用）", BaseURL: "", Plugins: []MarketItem{}, SourceURL: cfg.URL}, nil
	}
	m.marketMu.Lock()
	cached, fetched := m.marketCache, m.marketFetched
	m.marketMu.Unlock()
	ttl := time.Duration(cfg.CacheMinutes) * time.Minute
	if !force && cached != nil && time.Since(fetched) < ttl {
		return m.decorateIndex(cached), nil
	}
	b, err := httpGet(ctx, cfg.URL, cfg.ProxyPrefix)
	if err != nil {
		if cached != nil {
			return m.decorateIndex(cached), nil // 离线时降级到缓存
		}
		return nil, fmt.Errorf("无法读取插件市场：%w。可在插件页修改市场地址，或关闭市场只用本地上传。", err)
	}
	var idx MarketIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("marketplace.json 不是合法 JSON：%w", err)
	}
	if len(idx.Plugins) == 0 {
		idx.Plugins = []MarketItem{}
	}
	idx.SourceURL = cfg.URL
	m.marketMu.Lock()
	m.marketCache = &idx
	m.marketFetched = time.Now()
	m.marketMu.Unlock()
	return m.decorateIndex(&idx), nil
}

func (m *Manager) decorateIndex(idx *MarketIndex) *MarketIndex {
	out := *idx
	out.Plugins = append([]MarketItem{}, idx.Plugins...)
	for n := range out.Plugins {
		it := &out.Plugins[n]
		if i, ok := m.Get(it.ID); ok {
			it.Installed = true
			it.InstalledVersion = i.Manifest.Version
			it.UpdateAvailable = compareVersion(it.Version, i.Manifest.Version) > 0
		}
	}
	return &out
}

func compareVersion(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, y := 0, 0
		if i < len(pa) {
			fmt.Sscanf(pa[i], "%d", &x)
		}
		if i < len(pb) {
			fmt.Sscanf(pb[i], "%d", &y)
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

// MarketInstall 从 Git 市场安装（或更新）一个插件。
func (m *Manager) MarketInstall(ctx context.Context, id string) (*Installed, error) {
	cfg := m.MarketConfig()
	idx, err := m.MarketList(ctx, true)
	if err != nil {
		return nil, err
	}
	var target *MarketItem
	for i := range idx.Plugins {
		if idx.Plugins[i].ID == id {
			target = &idx.Plugins[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("市场里没有插件 %s", id)
	}
	if len(target.Files) == 0 {
		return nil, fmt.Errorf("市场条目 %s 没有声明 files", id)
	}
	base := resolveBase(cfg.URL, idx.BaseURL)
	files := map[string]string{}
	bin := map[string]string{}
	for _, f := range target.Files {
		u := joinFile(base, target.Path, f)
		b, err := httpGet(ctx, u, cfg.ProxyPrefix)
		if err != nil {
			return nil, fmt.Errorf("下载 %s 失败：%w", f, err)
		}
		if isTextFile(f) && isUTF8(b) {
			files[f] = string(b)
		} else {
			bin[f] = encodeBase64(b)
		}
	}
	if _, ok := files["plugin.json"]; !ok {
		return nil, fmt.Errorf("市场条目 %s 的 files 里缺少 plugin.json", id)
	}
	_, err = m.ImportBundle(Bundle{
		Files: files, Binary: bin,
		Origin: OriginMarket,
		Source: fmt.Sprintf("%s/%s", strings.TrimSuffix(base, "/"), strings.Trim(target.Path, "/")),
	})
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if st := m.state.Plugins[id]; st != nil {
		st.MarketVersion = target.Version
		st.Source = fmt.Sprintf("%s/%s", strings.TrimSuffix(base, "/"), strings.Trim(target.Path, "/"))
		_ = m.saveStateLocked()
	}
	m.mu.Unlock()
	m.Reload()
	out, _ := m.Get(id)
	return out, nil
}
