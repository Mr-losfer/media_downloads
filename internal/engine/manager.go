package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"mediasuper/internal/version"
)

var (
	errNotFound = errors.New("任务不存在")
	// ErrNotFound 供 server 包判断「任务不存在」
	ErrNotFound = errNotFound
)

const maxTasksStored = 20000

type job struct {
	id         string
	cancel     context.CancelFunc
	downloaded atomic.Int64
	total      atomic.Int64
	segDone    atomic.Int64
	segTotal   atomic.Int64
	lastBytes  int64
	lastAt     int64
	lastProgAt atomic.Int64
}

// dlCtx 是一次下载运行期的全部上下文，只被单个协程使用。
type dlCtx struct {
	m       *Manager
	j       *job
	task    *Task
	st      Settings
	client  *http.Client
	tmpDir  string
	limiter *limiter
	keyMu   sync.Mutex
	keys    map[string][]byte
}

func (d *dlCtx) withRetry(ctx context.Context, fn func() error) error {
	attempts := clampInt(d.st.MaxRetries, 0, 10) + 1
	var lastErr error
	for i := 0; i < attempts; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if i < attempts-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(400*(i+1)) * time.Millisecond):
			}
		}
	}
	return lastErr
}

// checkProgress 节流刷新进度：最多每 250ms 一次，避免锁竞争拖慢下载。
func (d *dlCtx) checkProgress() {
	now := nowMs()
	last := d.j.lastProgAt.Load()
	if now-last < 250 {
		return
	}
	if d.j.lastProgAt.CompareAndSwap(last, now) {
		d.m.progressUpdate(d.j, d.task)
	}
}

type Manager struct {
	mu        sync.Mutex
	tasks     map[string]*Task
	jobs      map[string]*job
	dirty     map[string]bool
	saved     map[string]int64
	db        *Store
	st        Settings
	hub       *Hub
	ff        *FileOps
	client    *http.Client
	lim       *limiter
	dataDir   string
	startedAt time.Time
	lastClean int64
}

func NewManager(db *Store, st Settings, dataDir string) *Manager {
	m := &Manager{
		tasks:     make(map[string]*Task),
		jobs:      make(map[string]*job),
		dirty:     make(map[string]bool),
		saved:     make(map[string]int64),
		db:        db,
		st:        st,
		hub:       newHub(),
		ff:        newFileOps(st.FFmpegPath, dataDir),
		dataDir:   dataDir,
		startedAt: time.Now(),
	}
	m.client = newHTTPClient(st, st.SegConns)
	m.lim = newLimiter(int64(st.SpeedLimitKB) * 1024)

	list, err := db.AllTasks()
	if err != nil {
		log.Printf("读取历史任务失败: %v", err)
	}
	for _, t := range list {
		// 进程重启后，未完成的任务一律回队列重下（分片无法可靠续传）
		if isActiveStatus(t.Status) || t.Status == StatusQueued {
			t.Status = StatusQueued
			t.Downloaded = 0
			t.Total = 0
			t.Progress = 0
			t.SegDone = 0
			t.Speed = 0
		}
		if t.HasThumb {
			if _, err := os.Stat(t.AbsPath() + ".jpg"); err != nil {
				t.HasThumb = false
			}
		}
		m.tasks[t.ID] = t
	}
	if len(list) > 0 {
		log.Printf("已载入 %d 条历史任务", len(list))
	}
	return m
}

func (m *Manager) Start() {
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		statsAt := time.Now()
		for now := range t.C {
			m.tick()
			if now.Sub(statsAt) >= 3*time.Second {
				statsAt = now
				m.hub.Broadcast("stats", m.Stats())
			}
			m.maybeClean(now.UnixMilli())
		}
	}()
}

func (m *Manager) tick() {
	now := nowMs()
	m.mu.Lock()
	running := 0
	var pending []*Task
	for _, t := range m.tasks {
		if isActiveStatus(t.Status) {
			running++
		}
		if t.Status == StatusQueued {
			pending = append(pending, t)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].CreatedAt < pending[j].CreatedAt })

	var launch []*Task
	for _, t := range pending {
		if running >= m.st.MaxTasks {
			break
		}
		t.Status = StatusRunning
		t.Attempts++
		t.UpdatedAt = now
		m.dirty[t.ID] = true
		running++
		launch = append(launch, t.clone())
	}

	for id, j := range m.jobs {
		cur := j.downloaded.Load()
		if j.lastAt == 0 {
			j.lastBytes = cur
			j.lastAt = now
			continue
		}
		dt := float64(now-j.lastAt) / 1000
		if dt < 0.5 {
			continue
		}
		sp := int64(float64(cur-j.lastBytes) / dt)
		if sp < 0 {
			sp = 0
		}
		if t, ok := m.tasks[id]; ok {
			if t.Speed > 0 && sp > 0 {
				sp = (t.Speed + sp*3) / 4 // 指数平滑，避免数字乱跳
			}
			t.Speed = sp
			m.dirty[id] = true
		}
		j.lastBytes = cur
		j.lastAt = now
	}

	var dirtyTasks []*Task
	var toSave []*Task
	for id := range m.dirty {
		t, ok := m.tasks[id]
		if !ok {
			continue
		}
		dirtyTasks = append(dirtyTasks, t.clone())
		if now-m.saved[id] >= 4000 || !isActiveStatus(t.Status) {
			m.saved[id] = now
			toSave = append(toSave, t.clone())
		}
	}
	m.dirty = make(map[string]bool)
	m.mu.Unlock()

	for _, t := range toSave {
		if err := m.db.PutTask(t); err != nil {
			log.Printf("持久化任务失败: %v", err)
		}
	}
	for _, t := range dirtyTasks {
		m.hub.Broadcast("task", t)
	}
	for _, t := range launch {
		log.Printf("[%s] 开始下载 %s", t.ID, t.URL)
		m.fireWebhook("task.started", t)
		go m.run(t)
	}
}

func (m *Manager) run(t *Task) {
	m.mu.Lock()
	st := m.st
	client := m.client
	lim := m.lim
	m.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	j := &job{id: t.ID, cancel: cancel}
	j.total.Store(t.Total)
	m.mu.Lock()
	m.jobs[t.ID] = j
	m.mu.Unlock()

	tmp := filepath.Join(m.dataDir, "tmp", t.ID)
	_ = os.MkdirAll(tmp, 0o755)
	d := &dlCtx{
		m: m, j: j, task: t, st: st,
		client: client, tmpDir: tmp, limiter: lim,
		keys: make(map[string][]byte),
	}

	out, size, err := d.run(ctx)
	cancel()

	m.mu.Lock()
	delete(m.jobs, t.ID)
	real, exists := m.tasks[t.ID]
	var snapshot *Task
	if exists {
		real.Speed = 0
		real.UpdatedAt = nowMs()
		switch {
		case real.Status == StatusCanceled:
			real.Error = "已取消"
		case err == nil:
			real.Status = StatusCompleted
			real.Progress = 100
			real.Size = size
			real.Total = size
			real.Downloaded = size
			real.SegDone = real.SegTotal
			real.FileName = filepath.Base(out)
			real.FinishedAt = nowMs()
			real.Error = ""
		case errors.Is(err, context.Canceled):
			real.Status = StatusCanceled
			real.Error = "已取消"
		default:
			real.Error = err.Error()
			// 永久错误（ffmpeg 缺失、编码不兼容且不允许转码）重试只会把整部片子重下一遍，
			// 直接判定失败，让用户先修环境。
			if st.MaxRetries > 0 && real.Attempts <= st.MaxRetries && !IsPermanent(err) {
				real.Status = StatusQueued
				real.Downloaded = 0
				real.Progress = 0
				real.SegDone = 0
				real.Total = 0
				log.Printf("[%s] 第 %d 次失败，自动重排: %v", t.ID, real.Attempts, err)
			} else {
				real.Status = StatusFailed
				if IsPermanent(err) {
					log.Printf("[%s] 失败（不可重试）: %v", t.ID, err)
				}
			}
		}
		snapshot = real.clone()
	}
	m.mu.Unlock()

	if !st.KeepTemp {
		os.RemoveAll(tmp)
	}
	if snapshot == nil {
		return
	}
	if err := m.db.PutTask(snapshot); err != nil {
		log.Printf("持久化任务失败: %v", err)
	}
	m.hub.Broadcast("task", snapshot)

	switch snapshot.Status {
	case StatusCompleted:
		log.Printf("[%s] 完成: %s (%s)", snapshot.ID, snapshot.FileName, humanBytes(snapshot.Size))
		m.fireWebhook("task.completed", snapshot)
		if st.Thumbnails {
			go m.makeThumb(snapshot.ID)
		}
	case StatusFailed:
		log.Printf("[%s] 失败: %s", snapshot.ID, snapshot.Error)
		m.fireWebhook("task.failed", snapshot)
	case StatusCanceled:
		m.fireWebhook("task.canceled", snapshot)
	}
}

// progressUpdate 把下载协程里的计数同步到共享任务对象（只在内存，持久化另算）。
func (m *Manager) progressUpdate(j *job, local *Task) {
	m.mu.Lock()
	if t, ok := m.tasks[local.ID]; ok {
		t.Downloaded = j.downloaded.Load()
		t.Total = j.total.Load()
		if j.segTotal.Load() > 0 {
			t.SegDone = int(j.segDone.Load())
			t.SegTotal = int(j.segTotal.Load())
		}
		if local.FileName != "" {
			t.FileName = local.FileName
		}
		if local.Kind != "" {
			t.Kind = local.Kind
		}
		if local.Format != "" {
			t.Format = local.Format
		}
		if local.Conns > 0 {
			t.Conns = local.Conns
		}
		if local.Duration > 0 {
			t.Duration = local.Duration
		}
		t.Progress = calcProgress(t)
		t.UpdatedAt = nowMs()
		m.dirty[t.ID] = true
	}
	m.mu.Unlock()
}

func (m *Manager) setPhase(j *job, status string) {
	m.mu.Lock()
	if t, ok := m.tasks[j.id]; ok {
		t.Status = status
		t.UpdatedAt = nowMs()
		if t.Progress < 99 {
			t.Progress = 99
		}
		m.dirty[t.ID] = true
	}
	m.mu.Unlock()
}

func calcProgress(t *Task) float64 {
	if t.Status == StatusCompleted {
		return 100
	}
	var p float64
	switch {
	case t.SegTotal > 0:
		p = float64(t.SegDone) / float64(t.SegTotal) * 100
	case t.Total > 0:
		p = float64(t.Downloaded) / float64(t.Total) * 100
	}
	if p < 0 {
		p = 0
	}
	if p > 99.5 {
		p = 99.5
	}
	return p
}

type TaskInput struct {
	URL     string            `json:"url"`
	Title   string            `json:"title"`
	SaveDir string            `json:"save_dir"`
	Headers map[string]string `json:"headers"`
}

func cleanHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		switch strings.ToLower(k) {
		case "host", "content-length", "connection":
			continue
		}
		out[k] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (m *Manager) Add(in TaskInput) (*Task, error) {
	raw := strings.TrimSpace(in.URL)
	if raw == "" {
		return nil, errors.New("URL 不能为空")
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return nil, errors.New("只支持 http/https 链接")
	}
	pu, err := url.Parse(raw)
	if err != nil || pu.Host == "" {
		return nil, errors.New("URL 格式不合法")
	}

	m.mu.Lock()
	if len(m.tasks) >= maxTasksStored {
		m.mu.Unlock()
		return nil, fmt.Errorf("任务数已达上限 %d，请先清理历史", maxTasksStored)
	}
	dir := strings.TrimSpace(in.SaveDir)
	if dir == "" {
		dir = m.st.DefaultDir
	}
	allowed := false
	for _, d := range m.st.SaveDirs {
		if d.Path == dir {
			allowed = true
			break
		}
	}
	if !allowed {
		m.mu.Unlock()
		return nil, fmt.Errorf("保存目录不在允许列表内: %s", dir)
	}
	if _, free := diskFree(dir); free > 0 && free < 1<<30 {
		m.mu.Unlock()
		return nil, fmt.Errorf("保存目录剩余空间不足 1GB（剩余 %s），已拒绝新任务", humanBytes(free))
	}
	t := &Task{
		ID:        newID(),
		URL:       raw,
		Title:     safeName(in.Title),
		SaveDir:   dir,
		Status:    StatusQueued,
		Host:      pu.Hostname(),
		Headers:   cleanHeaders(in.Headers),
		CreatedAt: nowMs(),
		UpdatedAt: nowMs(),
	}
	if t.Title != "" {
		// 文件名留给下载器探测完容器类型后再生成（要补 .mp4 之类的后缀）
		t.FileName = ""
	}
	m.tasks[t.ID] = t
	m.dirty[t.ID] = true
	snap := t.clone()
	m.mu.Unlock()

	if err := m.db.PutTask(snap); err != nil {
		return nil, err
	}
	m.hub.Broadcast("task", snap)
	m.fireWebhook("task.created", snap)
	return snap, nil
}

type ListFilter struct {
	Status string
	Query  string
	Limit  int
	Offset int
}

func (m *Manager) List(f ListFilter) ([]*Task, int) {
	m.mu.Lock()
	all := make([]*Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		all = append(all, t.clone())
	}
	m.mu.Unlock()

	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt == all[j].CreatedAt {
			return all[i].ID > all[j].ID
		}
		return all[i].CreatedAt > all[j].CreatedAt
	})

	q := strings.ToLower(strings.TrimSpace(f.Query))
	out := make([]*Task, 0, len(all))
	for _, t := range all {
		if f.Status != "" && f.Status != "all" {
			if f.Status == "active" {
				if !isActiveStatus(t.Status) && t.Status != StatusQueued {
					continue
				}
			} else if t.Status != f.Status {
				continue
			}
		}
		if q != "" && !strings.Contains(strings.ToLower(t.Title+" "+t.URL+" "+t.FileName), q) {
			continue
		}
		out = append(out, t)
	}
	total := len(out)
	if f.Offset > 0 {
		if f.Offset >= len(out) {
			return []*Task{}, total
		}
		out = out[f.Offset:]
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, total
}

func (m *Manager) Get(id string) (*Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return nil, false
	}
	return t.clone(), true
}

func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	t, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return errNotFound
	}
	if j := m.jobs[id]; j != nil {
		t.Status = StatusCanceled
		t.UpdatedAt = nowMs()
		j.cancel()
	} else if t.Status == StatusQueued {
		t.Status = StatusCanceled
		t.Error = "已取消"
		t.UpdatedAt = nowMs()
	} else {
		m.mu.Unlock()
		return fmt.Errorf("任务当前状态是 %s，无法取消", t.Status)
	}
	snap := t.clone()
	m.mu.Unlock()

	_ = m.db.PutTask(snap)
	m.hub.Broadcast("task", snap)
	m.fireWebhook("task.canceled", snap)
	return nil
}

func (m *Manager) Retry(id string) (*Task, error) {
	m.mu.Lock()
	t, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return nil, errNotFound
	}
	if isActiveStatus(t.Status) {
		m.mu.Unlock()
		return nil, errors.New("任务正在运行，请先取消")
	}
	t.Status = StatusQueued
	t.Error = ""
	t.Downloaded = 0
	t.Total = 0
	t.Progress = 0
	t.SegDone = 0
	t.Speed = 0
	t.Attempts = 0
	t.FinishedAt = 0
	t.UpdatedAt = nowMs()
	m.dirty[t.ID] = true
	snap := t.clone()
	m.mu.Unlock()

	_ = m.db.PutTask(snap)
	m.hub.Broadcast("task", snap)
	return snap, nil
}

func (m *Manager) Delete(id string, withFile bool) error {
	m.mu.Lock()
	t, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return errNotFound
	}
	if j := m.jobs[id]; j != nil {
		j.cancel()
	}
	path := t.AbsPath()
	title := t.Title
	isCompleted := t.Status == StatusCompleted
	delete(m.tasks, id)
	delete(m.dirty, id)
	delete(m.saved, id)
	m.mu.Unlock()

	if err := m.db.DelTask(id); err != nil {
		return err
	}
	if withFile && path != "" && isCompleted {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("删除文件失败 %s: %v", path, err)
		} else {
			os.Remove(path + ".jpg")
		}
	}
	log.Printf("[%s] 已删除任务 %s", id, title)
	m.hub.Broadcast("task.deleted", map[string]string{"id": id})
	return nil
}

func (m *Manager) Rename(id, name string) (*Task, error) {
	clean := safeName(strings.TrimSpace(name))
	if clean == "" {
		return nil, errors.New("名称不能为空")
	}
	m.mu.Lock()
	t, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return nil, errNotFound
	}
	if isActiveStatus(t.Status) {
		m.mu.Unlock()
		return nil, errors.New("任务正在运行，请先取消再改名")
	}
	oldPath := t.AbsPath()
	ext := filepath.Ext(t.FileName)
	if ext == "" && t.Format != "" {
		ext = "." + t.Format
	}
	newName := clean + ext
	newPath := filepath.Join(t.SaveDir, newName)
	if s, err := os.Stat(newPath); err == nil && s.Mode().IsRegular() && newPath != oldPath {
		m.mu.Unlock()
		return nil, errors.New("目标文件名已存在")
	}
	t.Title = clean
	t.FileName = newName
	t.UpdatedAt = nowMs()
	m.mu.Unlock()

	if oldPath != "" && oldPath != newPath {
		if _, err := os.Stat(oldPath); err == nil {
			if err := os.Rename(oldPath, newPath); err != nil {
				return nil, fmt.Errorf("重命名文件失败: %w", err)
			}
			os.Rename(oldPath+".jpg", newPath+".jpg")
		}
	}

	m.mu.Lock()
	snap := m.tasks[id].clone()
	m.mu.Unlock()
	_ = m.db.PutTask(snap)
	m.hub.Broadcast("task", snap)
	return snap, nil
}

func (m *Manager) makeThumb(id string) {
	t, ok := m.Get(id)
	if !ok || t.FileName == "" {
		return
	}
	src := t.AbsPath()
	if !strings.HasSuffix(strings.ToLower(src), ".mp4") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := m.ff.Thumb(ctx, src, src+".jpg"); err != nil {
		log.Printf("[%s] 生成缩略图失败: %v", id, err)
		return
	}
	dur := m.ff.Duration(ctx, src)

	m.mu.Lock()
	if real, ok := m.tasks[id]; ok {
		real.HasThumb = true
		if dur > 0 && real.Duration == 0 {
			real.Duration = dur
		}
		real.UpdatedAt = nowMs()
	}
	var snap *Task
	if real, ok := m.tasks[id]; ok {
		snap = real.clone()
	}
	m.mu.Unlock()

	if snap != nil {
		_ = m.db.PutTask(snap)
		m.hub.Broadcast("task", snap)
	}
}

func (m *Manager) maybeClean(now int64) {
	m.mu.Lock()
	days := m.st.AutoCleanDays
	if days <= 0 || now-m.lastClean < 3_600_000 {
		m.mu.Unlock()
		return
	}
	m.lastClean = now
	cutoff := now - int64(days)*86_400_000
	var old []string
	for _, t := range m.tasks {
		if t.Status == StatusCompleted && t.FinishedAt > 0 && t.FinishedAt < cutoff {
			old = append(old, t.ID)
		}
	}
	m.mu.Unlock()

	for _, id := range old {
		if err := m.Delete(id, true); err == nil {
			log.Printf("自动清理过期任务 %s", id)
		}
	}
}

func (m *Manager) Stats() map[string]any {
	m.mu.Lock()
	counts := map[string]int{}
	var totalSize int64
	for _, t := range m.tasks {
		counts[t.Status]++
		if t.Status == StatusCompleted {
			totalSize += t.Size
		}
	}
	dirs := make([]map[string]any, 0, len(m.st.SaveDirs))
	for _, d := range m.st.SaveDirs {
		total, free := diskFree(d.Path)
		dirs = append(dirs, map[string]any{
			"path": d.Path, "label": d.Label, "total": total, "free": free,
			"exists": dirExists(d.Path),
		})
	}
	n := len(m.tasks)
	maxTasks := m.st.MaxTasks
	segConns := m.st.SegConns
	m.mu.Unlock()

	return map[string]any{
		"version":         version.Version,
		"tasks":           n,
		"counts":          counts,
		"completed_bytes": totalSize,
		"dirs":            dirs,
		"max_tasks":       maxTasks,
		"seg_conns":       segConns,
		"uptime":          int64(time.Since(m.startedAt).Seconds()),
		"sse_clients":     m.hub.count(),
	}
}

func (m *Manager) Settings() Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.st
	st.SaveDirs = append([]SaveDir(nil), m.st.SaveDirs...)
	return st
}

func (m *Manager) UpdateSettings(in Settings) (Settings, error) {
	in.Sanitize()
	for _, d := range in.SaveDirs {
		if err := os.MkdirAll(d.Path, 0o755); err != nil {
			return m.Settings(), fmt.Errorf("目录不可用 %s: %w", d.Path, err)
		}
	}
	m.mu.Lock()
	in.APIKey = m.st.APIKey // APIKey 不能通过设置接口改
	m.st = in
	m.client = newHTTPClient(in, in.SegConns)
	if m.lim != nil {
		m.lim.setRate(int64(in.SpeedLimitKB) * 1024)
	} else {
		m.lim = newLimiter(int64(in.SpeedLimitKB) * 1024)
	}
	m.ff = newFileOps(in.FFmpegPath, m.dataDir)
	st := in
	m.mu.Unlock()

	if err := m.db.SaveSettings(in); err != nil {
		return st, err
	}
	log.Printf("设置已更新: 并发任务=%d 分片连接=%d 限速=%dKB/s", in.MaxTasks, in.SegConns, in.SpeedLimitKB)
	return st, nil
}

func diskFree(path string) (int64, int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	bs := int64(st.Bsize)
	return int64(st.Blocks) * bs, int64(st.Bavail) * bs
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// Idle 返回当前是否处于空闲状态（没有进行中或排队的任务）。
// 自动更新只在空闲时执行，避免升级重启打断下载。
func (m *Manager) Idle() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tasks {
		if isActiveStatus(t.Status) || t.Status == StatusQueued {
			return false
		}
	}
	return true
}

// FFmpeg 返回 ffmpeg 是否可用及版本信息。
func (m *Manager) FFmpeg() (bool, string) {
	m.mu.Lock()
	ff := m.ff
	m.mu.Unlock()
	ver, ok := ff.Available()
	return ok, ver
}

// FFmpegPath 返回解析后的 ffmpeg 路径（空串表示没找到），不执行进程，适合频繁查询。
func (m *Manager) FFmpegPath() (string, bool) {
	m.mu.Lock()
	ff := m.ff
	m.mu.Unlock()
	p := ff.Path()
	return p, p != ""
}

// ---------- 对外暴露的少量桥接方法（server 包用） ----------

// Subscribe 订阅 SSE 事件流。
func (m *Manager) Subscribe() (chan []byte, int) { return m.hub.subscribe() }

// Unsubscribe 取消订阅。
func (m *Manager) Unsubscribe(ch chan []byte) { m.hub.unsubscribe(ch) }

// Broadcast 手动播一条事件（例如 shutdown）。
func (m *Manager) Broadcast(event string, data any) { m.hub.Broadcast(event, data) }

// EnsureThumb 异步补一张缩略图，已有则跳过。
func (m *Manager) EnsureThumb(id string) {
	if t, ok := m.Get(id); ok && t.HasThumb {
		return
	}
	go m.makeThumb(id)
}

// SetPasswordHash 把新的密码哈希写进数据库。
func (m *Manager) SetPasswordHash(h string) error { return m.db.MetaPut("pass_hash", h) }

// MetaGet 读一条 meta 记录（启动初始化用）。
func (m *Manager) MetaGet(key string) string { return m.db.MetaGet(key) }

// MetaPut 写一条 meta 记录。
func (m *Manager) MetaPut(key, val string) error { return m.db.MetaPut(key, val) }

// DataDir 返回运行数据目录（临时分片、待安装更新都放这里）。
func (m *Manager) DataDir() string { return m.dataDir }
