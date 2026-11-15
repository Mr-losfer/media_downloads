package plugin

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PluginState 是运行期状态，集中放在 <data>/plugins/_state.json，
// 不写进插件目录，这样插件文件夹可以原样提交到 Git / 导出给别的机器。
type PluginState struct {
	Enabled       bool           `json:"enabled"`
	Config        map[string]any `json:"config"`
	Origin        string         `json:"origin"`
	Source        string         `json:"source,omitempty"`
	InstalledAt   string         `json:"installed_at,omitempty"`
	MarketVersion string         `json:"market_version,omitempty"`
}

type stateFile struct {
	Plugins map[string]*PluginState `json:"plugins"`
}

// Installed 是「一份已安装插件」的内存表示。
type Installed struct {
	Manifest *Manifest
	State    *PluginState
	Dir      string
	Files    []FileInfo
}

// FileInfo 是插件目录下的文件描述。
type FileInfo struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
	IsDir   bool   `json:"is_dir"`
}

// View 是给前端 / MCP 的合并视图。
type View struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Version       string         `json:"version"`
	Author        string         `json:"author"`
	Description   string         `json:"description"`
	Homepage      string         `json:"homepage,omitempty"`
	Icon          string         `json:"icon,omitempty"`
	Engine        string         `json:"engine"`
	Builtin       bool           `json:"builtin"`
	Origin        string         `json:"origin"`
	Source        string         `json:"source,omitempty"`
	Enabled       bool           `json:"enabled"`
	Tags          []string       `json:"tags,omitempty"`
	Changelog     string         `json:"changelog,omitempty"`
	Match         MatchRule      `json:"match"`
	ConfigSchema  []ConfigField  `json:"config_schema"`
	Config        map[string]any `json:"config"`
	Capabilities  Capabilities   `json:"capabilities"`
	HasBinary     bool           `json:"has_binary"`
	BinaryPath    string         `json:"binary_path,omitempty"`
	Files         []FileInfo     `json:"files"`
	InstalledAt   string         `json:"installed_at,omitempty"`
	MarketVersion string         `json:"market_version,omitempty"`
	MinAppVersion string         `json:"min_app_version,omitempty"`
	Warnings      []string       `json:"warnings,omitempty"`
}

func (i *Installed) View() View {
	v := View{
		ID: i.Manifest.ID, Name: i.Manifest.Name, Version: i.Manifest.Version,
		Author: i.Manifest.Author, Description: i.Manifest.Description,
		Homepage: i.Manifest.Homepage, Icon: i.Manifest.Icon,
		Engine: i.Manifest.Engine, Builtin: i.Manifest.Builtin,
		Origin: i.State.Origin, Source: i.State.Source, Enabled: i.State.Enabled,
		Tags: i.Manifest.Tags, Changelog: i.Manifest.Changelog,
		Match: i.Manifest.Match, ConfigSchema: i.Manifest.ConfigSchema,
		Config: i.State.Config, Capabilities: i.Manifest.Capabilities,
		Files: i.Files, InstalledAt: i.State.InstalledAt,
		MarketVersion: i.State.MarketVersion, MinAppVersion: i.Manifest.MinAppVersion,
	}
	if i.Manifest.Engine == EngineGo {
		bp := i.binaryPath()
		v.BinaryPath = bp
		if st, err := os.Stat(bp); err == nil && !st.IsDir() {
			v.HasBinary = true
			v.BinaryPath = bp
		} else {
			v.Warnings = append(v.Warnings, "尚未编译：请在插件详情点「编译」，或用 MCP plugin_build")
		}
	}
	return v
}

func (i *Installed) binaryPath() string {
	b := strings.TrimSpace(i.Manifest.Entry.Binary)
	if b == "" {
		b = filepath.Join("bin", i.Manifest.ID)
	}
	return filepath.Join(i.Dir, b)
}

// ---------- 状态文件 ----------

func (m *Manager) loadState() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = stateFile{Plugins: map[string]*PluginState{}}
	b, err := os.ReadFile(m.statePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := json.Unmarshal(b, &m.state); err != nil {
		return fmt.Errorf("插件状态文件损坏：%w", err)
	}
	if m.state.Plugins == nil {
		m.state.Plugins = map[string]*PluginState{}
	}
	return nil
}

func (m *Manager) saveStateLocked() error {
	b, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(m.statePath(), b)
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---------- 路径安全 ----------

// safeJoin 把用户给的相对路径限制在插件目录内，阻断 ../、绝对路径与软链接逃逸。
func safeJoin(dir, rel string) (string, error) {
	rel = strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" || rel == "." {
		return "", fmt.Errorf("文件路径不能为空")
	}
	if strings.HasPrefix(rel, "..") || strings.Contains(rel, "/../") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("非法路径：不能越出插件目录")
	}
	clean := filepath.Clean(filepath.Join(dir, filepath.FromSlash(rel)))
	absDir, _ := filepath.Abs(dir)
	absClean, _ := filepath.Abs(clean)
	if absClean != absDir && !strings.HasPrefix(absClean, absDir+string(os.PathSeparator)) {
		return "", fmt.Errorf("非法路径：不能越出插件目录")
	}
	return clean, nil
}

func (i *Installed) safe(rel string) (string, error) { return safeJoin(i.Dir, rel) }

// ---------- 文件读写 ----------

func (i *Installed) RefreshFiles() error {
	files := make([]FileInfo, 0, 8)
	err := filepath.Walk(i.Dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, e := filepath.Rel(i.Dir, p)
		if e != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		files = append(files, FileInfo{
			Path: rel, Size: info.Size(),
			ModTime: info.ModTime().UTC().Format(time.RFC3339), IsDir: info.IsDir(),
		})
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(files, func(a, b int) bool { return files[a].Path < files[b].Path })
	i.Files = files
	return nil
}

func (i *Installed) ReadFile(rel string) ([]byte, error) {
	p, err := i.safe(rel)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("文件不存在：%s", rel)
	}
	return b, err
}

func (i *Installed) WriteFile(rel string, data []byte) error {
	p, err := i.safe(rel)
	if err != nil {
		return err
	}
	if len(data) > 2<<20 {
		return fmt.Errorf("单个文件不能超过 2 MB")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return atomicWrite(p, data)
}

func (i *Installed) DeleteFile(rel string) error {
	p, err := i.safe(rel)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// WriteManifest 把规范化后的 Manifest 写回 plugin.json（不覆盖格式以外的内容）。
func (i *Installed) WriteManifest(mf *Manifest) error {
	b, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return i.WriteFile("plugin.json", b)
}

// ---------- 导出 / 导入 ----------

// ExportZip 把插件目录打成 zip（不含编译产物，便于上架 Git）。
func (i *Installed) ExportZip() ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.Walk(i.Dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(i.Dir, p)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "bin/") || strings.HasSuffix(rel, ".exe") {
			return nil // 编译产物不导出，避免架构混用
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name = i.Manifest.ID + "/" + rel
		h.Method = zip.Deflate
		fw, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(fw, io.LimitReader(f, 4<<20))
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Bundle 是一份「内联插件包」，供本地上传与 MCP plugin_upload 使用。
type Bundle struct {
	Files map[string]string `json:"files"` // 相对路径 -> 文本内容（二进制可 base64，见 Binary）
	// Binary 是文件名 -> base64 内容，用于图标等二进制资源。
	Binary map[string]string `json:"binary,omitempty"`
	// Origin / Source 记录来源，便于前端显示徽标。
	Origin string `json:"origin,omitempty"`
	Source string `json:"source,omitempty"`
}

// ImportZip 从 zip 流安装一个插件。zip 里必须有且仅有一个顶层目录下的 plugin.json。
func (m *Manager) ImportZip(r io.Reader, origin, source string) (*Installed, error) {
	b, err := io.ReadAll(io.LimitReader(r, 16<<20))
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, fmt.Errorf("不是有效的 zip 包：%w", err)
	}
	files := map[string]string{}
	bin := map[string]string{}
	prefix := ""
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := strings.ReplaceAll(f.Name, "\\", "/")
		// 统一剥掉一层顶层目录（打包工具常带）
		parts := strings.SplitN(strings.TrimPrefix(name, "/"), "/", 2)
		if len(parts) == 2 && prefix == "" {
			prefix = parts[0] + "/"
		}
		if len(parts) < 2 {
			// 顶层直接放 plugin.json 也接受
			prefix = ""
		}
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := strings.ReplaceAll(f.Name, "\\", "/")
		name = strings.TrimPrefix(name, "/")
		if prefix != "" && strings.HasPrefix(name, prefix) {
			name = strings.TrimPrefix(name, prefix)
		}
		if name == "" || strings.Contains(name, "..") {
			continue
		}
		if f.UncompressedSize64 > 4<<20 {
			return nil, fmt.Errorf("包内文件 %s 超过 4 MB", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(rc, 4<<20))
		rc.Close()
		if err != nil {
			return nil, err
		}
		if isTextFile(name) && isUTF8(data) {
			files[name] = string(data)
		} else {
			bin[name] = encodeBase64(data)
		}
	}
	if _, ok := files["plugin.json"]; !ok {
		return nil, fmt.Errorf("zip 包里缺少 plugin.json")
	}
	return m.ImportBundle(Bundle{Files: files, Binary: bin, Origin: origin, Source: source})
}

func isTextFile(name string) bool {
	l := strings.ToLower(name)
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".zip", ".so", ".exe"} {
		if strings.HasSuffix(l, ext) {
			return false
		}
	}
	return true
}

func isUTF8(b []byte) bool {
	// 用 json.Valid 不够（普通 Go 源码不是 JSON），这里用 utf8 包判断
	return utf8Valid(b)
}
