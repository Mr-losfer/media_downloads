'use strict';
/* ============================================================
   Media Super 管理端
   - 原生 JS，零依赖，全部逻辑内联在二进制里
   - 列表用 keyed patch，SSE 推来的进度只改变动节点，不整表重绘
   - 移动端优先：底部导航、底部抽屉、44px 触控目标
   ============================================================ */

/* ---------------- DOM 小工具 ---------------- */
const $ = (s, r) => (r || document).querySelector(s);
const $$ = (s, r) => Array.from((r || document).querySelectorAll(s));
const esc = (s) => String(s == null ? '' : s).replace(/[&<>"']/g, (c) => ({
  '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
}[c]));

/* ---------------- 格式化 ---------------- */
function fmtBytes(n) {
  n = Number(n) || 0;
  if (n < 1024) return n + ' B';
  const u = ['KB', 'MB', 'GB', 'TB', 'PB'];
  let i = -1, v = n;
  do { v /= 1024; i++; } while (v >= 1024 && i < u.length - 1);
  return (v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2)) + ' ' + u[i];
}
function fmtDur(sec) {
  sec = Math.max(0, Math.round(Number(sec) || 0));
  if (sec < 60) return sec + ' 秒';
  const m = Math.floor(sec / 60), s = sec % 60;
  if (m < 60) return m + ' 分 ' + (s < 10 ? '0' + s : s) + ' 秒';
  const h = Math.floor(m / 60);
  return h + ' 小时 ' + (m % 60 < 10 ? '0' + (m % 60) : m % 60) + ' 分';
}
function baseName(fn, title) {
  let s = fn || title || '未命名';
  const i = s.lastIndexOf('.');
  if (i > 0 && s.length - i <= 6) s = s.slice(0, i);
  return s;
}

/* ---------------- 更新内容渲染（重要部分高亮） ----------------
   内容来自 .release-notes（发布脚本写进 version.json / Release body）。约定：
   - 每行一条，行首 feat: / fix: / 新增： 等前缀渲染成分类徽章
   - **重点** 高亮、反引号是行内代码、v2.5.4 这类版本号单独强调
   全部先做 HTML 转义，再在转义后的文本上替换，不会注入标签。          */
function renderNotes(raw) {
  const lines = String(raw || '').split(/\r?\n/).map((s) => s.trim()).filter(Boolean);
  if (!lines.length) return '<p class="chg-empty">暂无更新内容</p>';
  const TAGS = {
    feat: ['新增', 't-feat'], fix: ['修复', 't-fix'], perf: ['优化', 't-perf'],
    ui: ['界面', 't-perf'], refactor: ['重构', 't-info'], docs: ['文档', 't-info'],
    chore: ['维护', 't-info'],
    '新增': ['新增', 't-feat'], '修复': ['修复', 't-fix'], '优化': ['优化', 't-perf'],
    '改进': ['优化', 't-perf'], '重要': ['重要', 't-fix'], '注意': ['注意', 't-fix'],
    '提醒': ['提醒', 't-info']
  };
  return '<div class="chg-list">' + lines.map((line) => {
    let s = esc(line.replace(/^[-*•]\s+/, ''));
    let tag = '';
    const m = s.match(/^([A-Za-z]+|[\u4e00-\u9fa5]{2,4})\s*[:：]\s*/);
    if (m) {
      const hit = TAGS[m[1].toLowerCase()] || TAGS[m[1]];
      if (hit) {
        tag = '<span class="chg-tag ' + hit[1] + '">' + hit[0] + '</span>';
        s = s.slice(m[0].length);
      }
    }
    s = s.replace(/\*\*([^*]+)\*\*/g, '<mark class="hl">$1</mark>');
    s = s.replace(/`([^`]+)`/g, '<code class="hl-code">$1</code>');
    s = s.replace(/\bv\d+\.\d+\.\d+\b/g, '<b class="hl-ver">$&</b>');
    return '<div class="chg-line">' + tag + '<span class="chg-text">' + s + '</span></div>';
  }).join('') + '</div>';
}

// 检查失败时不要把代理域名 / 一长串 URL 甩给用户
function cleanUpdError(msg) {
  let s = String(msg || '');
  s = s.replace(/https?:\/\/[^\s，,；;)]+/g, '更新源');
  s = s.replace(/\bgh-proxy\.com\b/gi, '代理').replace(/\bGitHub\b/gi, '更新源');
  return s.replace(/\s{2,}/g, ' ').trim();
}

// 语义化版本比较：a 比 b 新才返回 true
function isNewerVer(a, b) {
  const p = (v) => String(v || '').replace(/^v/i, '').split(/[.\-+]/).map((n) => parseInt(n, 10) || 0);
  const pa = p(a), pb = p(b);
  for (let i = 0; i < 3; i++) {
    if ((pa[i] || 0) !== (pb[i] || 0)) return (pa[i] || 0) > (pb[i] || 0);
  }
  return false;
}
function lsGet(k) { try { return localStorage.getItem(k); } catch (e) { return null; } }
function lsSet(k, v) { try { localStorage.setItem(k, v); } catch (e) { /* 隐私模式 */ } }
function ssGet(k) { try { return sessionStorage.getItem(k); } catch (e) { return null; } }
function ssSet(k, v) { try { sessionStorage.setItem(k, v); } catch (e) { /* ignore */ } }

/* ---------------- 图标（1.8 描边，24 网格） ---------------- */
const ICO = {
  play: '<path d="M8.5 5.6l9.6 6.4-9.6 6.4z" fill="currentColor" stroke="none"/>',
  download: '<path d="M12 3.5v11.5"/><path d="M7 10.5l5 5 5-5"/><path d="M4.5 20h15"/>',
  pencil: '<path d="M4 20h4.2L19 9.2a2.3 2.3 0 0 0-3.2-3.2L5 16.8z"/><path d="M13.6 6.6l3.8 3.8"/>',
  trash: '<path d="M4 7h16"/><path d="M9.6 7V5.3A1.3 1.3 0 0 1 10.9 4h2.2a1.3 1.3 0 0 1 1.3 1.3V7"/><path d="M6.6 7l.8 12.1A1.9 1.9 0 0 0 9.3 21h5.4a1.9 1.9 0 0 0 1.9-1.9L17.4 7"/>',
  x: '<path d="M6 6l12 12M18 6L6 18"/>',
  retry: '<path d="M20 12a8 8 0 1 1-2.7-6"/><path d="M20 3.8v4.4h-4.4"/>',
  folder: '<path d="M3 7.5A1.5 1.5 0 0 1 4.5 6h3.9l2 2.4h7.1A1.5 1.5 0 0 1 19 9.9v7.6A1.5 1.5 0 0 1 17.5 19h-13A1.5 1.5 0 0 1 3 17.5z"/>',
  copy: '<rect x="9" y="9" width="11" height="11" rx="2.2"/><path d="M6.2 15H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v1.2"/>',
  check: '<path d="M5 12.6l4.6 4.4L19 6.6"/>',
  film: '<rect x="3" y="4.6" width="18" height="14.8" rx="2.4"/><path d="M8.2 4.6v14.8M15.8 4.6v14.8M3 9.6h5.2M15.8 9.6H21M3 14.4h5.2M15.8 14.4H21"/>',
  link: '<path d="M10.2 13.8a3.4 3.4 0 0 0 4.8 0l2.9-2.9a3.4 3.4 0 0 0-4.8-4.8L11.6 7.6"/><path d="M13.8 10.2a3.4 3.4 0 0 0-4.8 0l-2.9 2.9a3.4 3.4 0 0 0 4.8 4.8l1.5-1.5"/>',
  up: '<path d="M12 19.5V5"/><path d="M6.5 10.5L12 5l5.5 5.5"/>'
};
function ico(name) {
  return '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" ' +
    'stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' + (ICO[name] || '') + '</svg>';
}

/* ---------------- 状态 ---------------- */
const STATUS = {
  queued: { label: '排队', cls: 'queued' },
  running: { label: '下载中', cls: 'running' },
  merging: { label: '处理中', cls: 'merging' },
  completed: { label: '已完成', cls: 'completed' },
  failed: { label: '失败', cls: 'failed' },
  canceled: { label: '已取消', cls: 'canceled' }
};
const state = {
  tasks: new Map(),
  stats: null,
  settings: null,
  filter: 'all',
  q: '',
  view: 'home',
  doc: 'api',
  setTab: 'update',
  es: null
};
let visIds = [];
let renderQueued = false;

/* ---------------- Toast ---------------- */
function toast(msg, kind) {
  const box = $('#toasts');
  const el = document.createElement('div');
  el.className = 'toast' + (kind ? ' ' + kind : '');
  el.setAttribute('role', 'status');
  el.textContent = msg;
  box.appendChild(el);
  while (box.children.length > 3) box.firstElementChild.remove();
  setTimeout(() => {
    el.style.transition = 'opacity .24s ease, transform .24s ease';
    el.style.opacity = '0';
    el.style.transform = 'translateY(6px)';
    setTimeout(() => el.remove(), 260);
  }, 2400);
}

/* ---------------- HTTP ---------------- */
async function api(path, opt) {
  const o = Object.assign({ credentials: 'same-origin', headers: {} }, opt || {});
  if (o.body && typeof o.body !== 'string') {
    o.body = JSON.stringify(o.body);
    o.headers['Content-Type'] = 'application/json';
  }
  const r = await fetch(path, o);
  let data = null;
  const ct = r.headers.get('content-type') || '';
  if (ct.indexOf('json') >= 0) { try { data = await r.json(); } catch (e) { /* ignore */ } }
  if (r.status === 401) { showLogin(); throw new Error('登录已过期，请重新登录'); }
  if (!r.ok) throw new Error((data && (data.error || data.message)) || ('请求失败 HTTP ' + r.status));
  return data;
}

function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) return navigator.clipboard.writeText(text);
  return new Promise((res, rej) => {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.cssText = 'position:fixed;top:-1000px;opacity:0';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy') ? res() : rej(new Error('复制失败')); }
    catch (e) { rej(e); }
    finally { ta.remove(); }
  });
}

/* ---------------- 登录 / 视图切换 ---------------- */
function showLogin() {
  if (state.es) { state.es.close(); state.es = null; }
  $('#appView').classList.add('hidden');
  $('#loginView').classList.remove('hidden');
  setTimeout(() => { const p = $('#loginPass'); if (p) p.focus(); }, 60);
}
function showApp() {
  $('#loginView').classList.add('hidden');
  $('#appView').classList.remove('hidden');
}

$('#loginForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const btn = $('#loginBtn'), err = $('#loginErr');
  err.textContent = '';
  btn.disabled = true; btn.textContent = '登录中…';
  try {
    await api('/api/login', { method: 'POST', body: { password: $('#loginPass').value } });
    $('#loginPass').value = '';
    await enterApp();
  } catch (ex) {
    err.textContent = ex.message || '登录失败';
  } finally {
    btn.disabled = false; btn.textContent = '登录';
  }
});

/* ---------------- 启动 ---------------- */
async function boot() {
  applyBase();
  try {
    await api('/api/me');
    await enterApp();
  } catch (e) {
    showLogin();
  }
}

async function enterApp() {
  const me = await api('/api/me');
  $('#verLabel').textContent = 'v' + (me.version || '');
  showApp();
  route();
  await Promise.all([loadTasks(), loadSettings(), loadStats(), loadSystemInfo()]);
  paintHero();
  paintThemeBtn();
  connectEvents();
  // 进后台先静默查一次更新，命中 5 分钟缓存时几乎零开销
  setTimeout(() => checkUpdate(false), 1200);
  if (state.view === 'home') $('#inUrl').focus({ preventScroll: true });
}

/* ---------------- 任务加载与渲染 ---------------- */
async function loadTasks() {
  const d = await api('/api/tasks?limit=200');
  state.tasks.clear();
  (d.tasks || []).forEach((t) => state.tasks.set(t.id, t));
  renderList(true);
}
async function loadStats() {
  try {
    state.stats = await api('/api/stats');
    renderStats();
  } catch (e) { /* 静默 */ }
}

function rank(t) {
  if (t.status === 'running') return 0;
  if (t.status === 'merging') return 1;
  if (t.status === 'queued') return 2;
  return 3;
}
function computeVisible() {
  const f = state.filter, q = state.q.trim().toLowerCase();
  let arr = Array.from(state.tasks.values());
  if (f === 'active') arr = arr.filter((t) => t.status === 'running' || t.status === 'merging' || t.status === 'queued');
  else if (f === 'completed') arr = arr.filter((t) => t.status === 'completed');
  else if (f === 'failed') arr = arr.filter((t) => t.status === 'failed' || t.status === 'canceled');
  if (q) {
    arr = arr.filter((t) =>
      String(t.title || '').toLowerCase().indexOf(q) >= 0 ||
      String(t.url || '').toLowerCase().indexOf(q) >= 0 ||
      String(t.file_name || '').toLowerCase().indexOf(q) >= 0);
  }
  arr.sort((a, b) => (rank(a) - rank(b)) || ((b.created_at || 0) - (a.created_at || 0)));
  return arr;
}

function subLine(t) {
  const bits = [];
  if (t.host) bits.push(esc(t.host));
  bits.push(t.kind === 'hls' ? 'HLS' : '直链');
  if (t.seg_total > 0) bits.push(t.seg_done + '/' + t.seg_total + ' 分片');
  else if (t.conns > 0) bits.push(t.conns + ' 连接');
  if (t.attempts > 1) bits.push('第 ' + t.attempts + ' 次尝试');
  return bits.join('<span class="dot">·</span>');
}
function numLine(t) {
  if (t.status === 'completed') return esc(fmtBytes(t.size || t.total));
  if (t.status === 'queued') return '等待中';
  if (t.status === 'failed') return '已失败';
  if (t.status === 'canceled') return '已取消';
  const parts = [];
  if (t.total > 0) parts.push(fmtBytes(t.downloaded) + ' / ' + fmtBytes(t.total));
  else parts.push(fmtBytes(t.downloaded));
  if (t.status !== 'merging' && t.speed > 0) {
    parts.push(fmtBytes(t.speed) + '/s');
    if (t.total > t.downloaded) parts.push('剩余 ' + fmtDur((t.total - t.downloaded) / t.speed));
  }
  return esc(parts.join(' · '));
}
function pctLine(t) {
  if (t.status === 'completed') return '100%';
  if (t.status === 'failed' || t.status === 'canceled' || t.status === 'queued') return '—';
  return Math.max(0, Math.min(100, Math.round(t.progress || 0))) + '%';
}
function actionsHTML(t) {
  const b = (act, icon, label, cls) =>
    '<button class="icon-btn' + (cls ? ' ' + cls : '') + '" data-act="' + act + '" aria-label="' + label + '" title="' + label + '">' + ico(icon) + '</button>';
  if (t.status === 'completed') {
    return b('preview', 'play', '预览') + b('download', 'download', '下载文件') +
      b('rename', 'pencil', '重命名') + b('delete', 'trash', '删除', 'danger');
  }
  if (t.status === 'failed' || t.status === 'canceled') {
    return b('retry', 'retry', '重试') + b('rename', 'pencil', '重命名') + b('delete', 'trash', '删除', 'danger');
  }
  return b('cancel', 'x', '取消任务') + b('rename', 'pencil', '重命名') +
    b('delete', 'trash', '删除', 'danger');
}

function rowHTML(t) {
  const st = STATUS[t.status] || { label: t.status, cls: 'queued' };
  const name = esc(t.title || t.file_name || '未命名');
  const thumb = t.has_thumb
    ? '<img class="task-thumb" src="/api/tasks/' + t.id + '/thumb?v=' + (t.updated_at || 0) + '" alt="" loading="lazy" decoding="async">'
    : '<div class="task-thumb ph">' + ico(t.kind === 'hls' ? 'film' : 'link') + '</div>';
  return '<li class="task is-' + t.status + '" id="t-' + t.id + '" data-id="' + t.id + '">' +
    thumb +
    '<div class="task-body">' +
      '<div class="task-head">' +
        '<div class="task-title" data-f="title" title="' + esc(t.url || '') + '">' + name + '</div>' +
        '<span class="pill ' + st.cls + '" data-f="pill">' + st.label + '</span>' +
      '</div>' +
      '<div class="task-sub" data-f="sub">' + subLine(t) + '</div>' +
      '<div class="bar' + (t.status === 'completed' ? ' done' : t.status === 'failed' ? ' err' : '') + '" data-f="bar">' +
        '<i data-f="fill" style="width:' + Math.max(0, Math.min(100, t.status === 'completed' ? 100 : (t.progress || 0))) + '%"></i>' +
      '</div>' +
      '<div class="task-foot"><span data-f="nums">' + numLine(t) + '</span><span class="pct" data-f="pct">' + pctLine(t) + '</span></div>' +
      (t.error ? '<div class="task-err" data-f="err">' + esc(t.error) + '</div>' : '<div class="task-err" data-f="err" hidden></div>') +
    '</div>' +
    '<div class="task-actions">' + actionsHTML(t) + '</div>' +
  '</li>';
}

function emptyHTML() {
  if (state.q.trim()) {
    return '<li class="empty">' + ico('link') + '<h3>没有匹配的任务</h3><p>换个关键词试试。</p></li>';
  }
  return '<li class="empty">' + ico('film') + '<h3>还没有下载任务</h3><p>去「首页」粘贴一个 m3u8 或 mp4 直链，回车即可开始。</p>' +
    '<a class="empty-go" href="#/home">去首页添加</a></li>';
}

function paintList(items) {
  const host = $('#taskList');
  host.innerHTML = items.length ? items.map(rowHTML).join('') : emptyHTML();
}

function patchRow(t) {
  const el = document.getElementById('t-' + t.id);
  if (!el) return;
  const st = STATUS[t.status] || { label: t.status, cls: 'queued' };
  el.className = 'task is-' + t.status;
  const pill = el.querySelector('[data-f="pill"]');
  if (pill) { pill.className = 'pill ' + st.cls; pill.textContent = st.label; }
  const sub = el.querySelector('[data-f="sub"]');
  if (sub) sub.innerHTML = subLine(t);
  const bar = el.querySelector('[data-f="bar"]');
  if (bar) {
    bar.className = 'bar' + (t.status === 'completed' ? ' done' : t.status === 'failed' ? ' err' : '');
    const fill = bar.querySelector('[data-f="fill"]');
    if (fill) fill.style.width = Math.max(0, Math.min(100, t.status === 'completed' ? 100 : (t.progress || 0))) + '%';
  }
  const nums = el.querySelector('[data-f="nums"]');
  if (nums) nums.innerHTML = numLine(t);
  const pct = el.querySelector('[data-f="pct"]');
  if (pct) pct.textContent = pctLine(t);
  const err = el.querySelector('[data-f="err"]');
  if (err) {
    if (t.error) { err.hidden = false; err.textContent = t.error; }
    else { err.hidden = true; err.textContent = ''; }
  }
}

function renderList(force) {
  const items = computeVisible();
  const ids = items.map((t) => t.id);
  const same = !force && ids.length === visIds.length && ids.every((v, i) => v === ids[i]);
  if (same) items.forEach(patchRow);
  else { paintList(items); visIds = ids; }
  renderCounts();
  paintHero();
}

function renderCounts() {
  const c = (state.stats && state.stats.counts) || {};
  const total = state.stats ? state.stats.tasks : state.tasks.size;
  const active = (c.running || 0) + (c.merging || 0);
  const queued = c.queued || 0;
  const done = c.completed || 0;
  const fail = (c.failed || 0) + (c.canceled || 0);
  $('#stActive').textContent = active;
  $('#stQueued').textContent = queued;
  $('#stDone').textContent = done;
  $('#stSize').textContent = fmtBytes((state.stats && state.stats.completed_bytes) || 0);
  $('#tabAll').textContent = total;
  $('#tabActive').textContent = active + queued;
  $('#tabDone').textContent = done;
  $('#tabFail').textContent = fail;
}

function paintHero() {
  const greetEl = $('#heroGreet');
  if (!greetEl) return;
  const now = new Date();
  const h = now.getHours();
  const greet = h < 5 ? '夜深了' : h < 9 ? '早上好' : h < 12 ? '上午好'
    : h < 14 ? '中午好' : h < 18 ? '下午好' : h < 23 ? '晚上好' : '夜深了';
  greetEl.textContent = greet;
  const week = '日一二三四五六'[now.getDay()];
  $('#heroDate').textContent = (now.getMonth() + 1) + ' 月 ' + now.getDate() + ' 日 · 周' + week;
  const c = (state.stats && state.stats.counts) || {};
  const active = (c.running || 0) + (c.merging || 0);
  const queued = c.queued || 0;
  const done = c.completed || 0;
  const fail = (c.failed || 0) + (c.canceled || 0);
  let sub;
  if (active || queued) {
    sub = active ? (active + ' 个任务正在下载') : '队列里有 ' + queued + ' 个任务';
    if (queued) sub += '，另有 ' + queued + ' 个排队中';
  } else if (fail) {
    sub = '有 ' + fail + ' 个任务未完成，点开卡片可以重试或删除';
  } else if (done) {
    sub = '已完成 ' + done + ' 个任务，粘贴新链接继续下载';
  } else {
    sub = '粘贴一个 m3u8 或 mp4 直链，回车即可开始下载';
  }
  $('#heroSub').textContent = sub;
}

function renderStats() {
  renderCounts();
  const s = state.stats;
  if (!s) return;
  const dirs = s.dirs || [];
  const def = (state.settings && state.settings.default_dir) || '';
  let hit = dirs.find((d) => d.path === def) || dirs.find((d) => d.exists) || dirs[0];
  const pill = $('#pillDisk');
  if (hit && hit.free > 0) {
    pill.classList.remove('off');
    const b = pill.querySelector('b');
    if (b) b.textContent = '余 ' + fmtBytes(hit.free);
    pill.title = (hit.label ? hit.label + ' · ' : '') + hit.path + ' 剩余 ' + fmtBytes(hit.free);
  } else {
    pill.classList.add('off');
    const b = pill.querySelector('b');
    if (b) b.textContent = '磁盘未知';
  }
}

/* ---------------- SSE ---------------- */
function scheduleRender() {
  if (renderQueued) return;
  renderQueued = true;
  requestAnimationFrame(() => { renderQueued = false; renderList(false); });
}
function connectEvents() {
  if (state.es) state.es.close();
  const es = new EventSource('/api/events');
  state.es = es;
  es.addEventListener('hello', () => { /* 连接已就绪 */ });
  es.addEventListener('snapshot', (e) => {
    try {
      const arr = JSON.parse(e.data);
      state.tasks.clear();
      arr.forEach((t) => state.tasks.set(t.id, t));
      renderList(true);
    } catch (err) { /* ignore */ }
  });
  es.addEventListener('task', (e) => {
    try {
      const t = JSON.parse(e.data);
      if (!t || !t.id) return;
      state.tasks.set(t.id, t);
      if (state.stats) {
        // 本地粗算，等 stats 事件到达再校正
        const c = state.stats.counts || {};
        c[t.status] = c[t.status] || 0;
      }
      scheduleRender();
    } catch (err) { /* ignore */ }
  });
  es.addEventListener('stats', (e) => {
    try { state.stats = JSON.parse(e.data); renderStats(); } catch (err) { /* ignore */ }
  });
  es.addEventListener('task.deleted', (e) => {
    try {
      const d = JSON.parse(e.data);
      if (d && d.id) state.tasks.delete(d.id);
      renderList(true);
    } catch (err) { /* ignore */ }
  });
  es.addEventListener('update.auto', (e) => {
    try {
      const d = JSON.parse(e.data);
      toast('发现新版本 v' + (d.to || '') + '，正在自动更新…', 'ok');
      waitRestart();
    } catch (err) { /* ignore */ }
  });
  es.addEventListener('ffmpeg.state', (e) => {
    try {
      const st = JSON.parse(e.data);
      state.ffmpegState = st;
      paintFFmpeg(state.sys, st);
      if (st && st.status === 'ready') {
        // 组件刚装好，刷新一次系统信息拿到真实路径
        loadSystemInfo();
        toast('ffmpeg 组件已就绪，m3u8 可以正常封装 mp4 了', 'ok');
      }
    } catch (err) { /* ignore */ }
  });
}

/* ---------------- 提交任务 ---------------- */
$('#submitForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const url = $('#inUrl').value.trim();
  if (!url) { $('#inUrl').focus(); return; }
  const btn = $('#btnSubmit');
  btn.disabled = true; btn.textContent = '提交中…';
  try {
    const body = { url: url };
    const title = $('#inTitle').value.trim();
    if (title) body.title = title;
    const dir = $('#inDir').value;
    if (dir) body.save_dir = dir;
    const ref = $('#inReferer').value.trim();
    const ck = $('#inCookie').value.trim();
    if (ref || ck) {
      body.headers = {};
      if (ref) body.headers.Referer = ref;
      if (ck) body.headers.Cookie = ck;
    }
    const t = await api('/api/tasks', { method: 'POST', body: body });
    state.tasks.set(t.id, t);
    $('#inUrl').value = '';
    $('#inTitle').value = '';
    $('#inReferer').value = '';
    $('#inCookie').value = '';
    renderList(true);
    toast('已加入队列', 'ok');
    jumpToTask(t.id);
  } catch (ex) {
    toast(ex.message, 'err');
  } finally {
    btn.disabled = false; btn.textContent = '下载';
  }
});

// jumpToTask 跳到任务页并滚动到新任务，方便确认是否开始下载
function jumpToTask(id) {
  if (state.view !== 'tasks') location.hash = '#/tasks';
  const focus = () => {
    const li = document.getElementById('t-' + id);
    if (!li) return;
    li.scrollIntoView({ behavior: 'smooth', block: 'center' });
    li.classList.add('flash');
    setTimeout(() => li.classList.remove('flash'), 1800);
  };
  setTimeout(focus, state.view === 'tasks' ? 60 : 240);
}

/* ---------------- 列表操作（事件委托） ---------------- */
$('#taskList').addEventListener('click', async (e) => {
  const btn = e.target.closest('[data-act]');
  if (!btn) return;
  const li = btn.closest('.task');
  if (!li) return;
  const id = li.dataset.id;
  const t = state.tasks.get(id);
  const act = btn.dataset.act;
  if (!t) return;
  try {
    if (act === 'preview') { openPreview(t); return; }
    if (act === 'download') {
      const a = document.createElement('a');
      a.href = '/api/tasks/' + id + '/download';
      if (t.file_name) a.download = t.file_name;
      document.body.appendChild(a); a.click(); a.remove();
      return;
    }
    if (act === 'rename') { openRename(t); return; }
    if (act === 'cancel') {
      btn.disabled = true;
      await api('/api/tasks/' + id + '/cancel', { method: 'POST' });
      toast('已取消', 'ok');
      return;
    }
    if (act === 'retry') {
      btn.disabled = true;
      const nt = await api('/api/tasks/' + id + '/retry', { method: 'POST' });
      if (nt && nt.id) state.tasks.set(nt.id, nt);
      renderList(true);
      toast('已重新排队', 'ok');
      return;
    }
    if (act === 'delete') {
      const pick = await confirmSheet(
        '删除任务',
        '「' + (t.title || t.file_name || '该任务') + '」将从列表中移除。是否同时删除磁盘文件？'
      );
      if (!pick) return;
      await api('/api/tasks/' + id + '?file=' + (pick === 'file' ? 'true' : 'false'), { method: 'DELETE' });
      state.tasks.delete(id);
      renderList(true);
      toast(pick === 'file' ? '已删除任务和文件' : '已移除任务记录', 'ok');
    }
  } catch (ex) {
    toast(ex.message, 'err');
    btn.disabled = false;
  }
});

/* ---------------- 抽屉 / 弹层 ---------------- */
function escClose(e) { if (e.key === 'Escape') closeSheet(); }
function closeSheet() {
  const m = $('.sheet-mask');
  if (m) m.remove();
  document.body.style.overflow = '';
  document.removeEventListener('keydown', escClose);
}
function openSheet(inner) {
  closeSheet();
  const mask = document.createElement('div');
  mask.className = 'sheet-mask';
  mask.innerHTML = '<div class="sheet" role="dialog" aria-modal="true">' +
    '<div class="sheet-grab"></div>' + inner + '</div>';
  document.body.appendChild(mask);
  document.body.style.overflow = 'hidden';
  mask.addEventListener('click', (e) => { if (e.target === mask) closeSheet(); });
  document.addEventListener('keydown', escClose);
  return mask.querySelector('.sheet');
}

function confirmSheet(title, text) {
  return new Promise((resolve) => {
    const sheet = openSheet(
      '<h3>' + esc(title) + '</h3>' +
      '<p class="hint" style="font-size:13px;color:var(--fg-dim);margin:0 0 14px">' + esc(text) + '</p>' +
      '<div class="sheet-actions">' +
        '<button class="danger" data-x="file">删除任务和文件</button>' +
        '<span class="spacer"></span>' +
        '<button class="ghost" data-x="cancel">取消</button>' +
        '<button class="ghost" data-x="record">只删记录</button>' +
      '</div>'
    );
    let done = false;
    sheet.addEventListener('click', (e) => {
      const b = e.target.closest('[data-x]');
      if (!b) return;
      done = true;
      closeSheet();
      const x = b.dataset.x;
      resolve(x === 'cancel' ? null : x);
    });
    const obs = new MutationObserver(() => {
      if (!document.body.contains(sheet) && !done) { done = true; obs.disconnect(); resolve(null); }
    });
    obs.observe(document.body, { childList: true });
  });
}

function openPreview(t) {
  const sheet = openSheet(
    '<h3>' + esc(t.title || t.file_name || '预览') + '</h3>' +
    '<video controls playsinline autoplay preload="metadata" src="/api/tasks/' + t.id + '/stream"></video>' +
    '<div class="sheet-actions">' +
      '<button class="ghost" data-x="close">关闭</button>' +
      '<span class="spacer"></span>' +
      '<button class="primary" data-x="dl">下载文件</button>' +
    '</div>'
  );
  sheet.addEventListener('click', (e) => {
    const b = e.target.closest('[data-x]');
    if (!b) return;
    if (b.dataset.x === 'dl') {
      const a = document.createElement('a');
      a.href = '/api/tasks/' + t.id + '/download';
      if (t.file_name) a.download = t.file_name;
      document.body.appendChild(a); a.click(); a.remove();
      return;
    }
    closeSheet();
  });
}

function openRename(t) {
  const raw = baseName(t.file_name, t.title);
  const sheet = openSheet(
    '<h3>重命名</h3>' +
    '<label class="lbl" for="rnInput">新名称</label>' +
    '<input type="text" id="rnInput" value="' + esc(raw) + '" spellcheck="false">' +
    '<p class="hint" style="margin-top:8px">扩展名会自动保留。</p>' +
    '<div class="sheet-actions">' +
      '<span class="spacer"></span>' +
      '<button class="ghost" data-x="close">取消</button>' +
      '<button class="primary" data-x="ok">保存</button>' +
    '</div>'
  );
  const input = $('#rnInput', sheet);
  setTimeout(() => { input.focus(); input.select(); }, 80);
  const submit = async () => {
    const v = input.value.trim();
    if (!v) { input.focus(); return; }
    try {
      const nt = await api('/api/tasks/' + t.id, { method: 'PATCH', body: { title: v } });
      if (nt && nt.id) state.tasks.set(nt.id, nt);
      closeSheet();
      renderList(true);
      toast('已重命名', 'ok');
    } catch (ex) { toast(ex.message, 'err'); }
  };
  sheet.addEventListener('click', (e) => {
    const b = e.target.closest('[data-x]');
    if (!b) return;
    if (b.dataset.x === 'ok') submit(); else closeSheet();
  });
  input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); submit(); } });
}

/* ---------------- 筛选 / 搜索 ---------------- */
$$('[data-filter]').forEach((b) => {
  b.addEventListener('click', () => {
    state.filter = b.dataset.filter;
    $$('[data-filter]').forEach((x) => x.setAttribute('aria-pressed', String(x === b)));
    renderList(true);
  });
});
let qTimer = 0;
$('#search').addEventListener('input', (e) => {
  clearTimeout(qTimer);
  const v = e.target.value;
  qTimer = setTimeout(() => { state.q = v; renderList(true); }, 180);
});

/* ---------------- 设置中心 ---------------- */
const SET = {
  max_tasks: '#sMaxTasks', seg_conns: '#sSegConns', speed_limit_kb: '#sSpeed',
  max_retries: '#sRetries', hls_quality: '#sQuality', transcode_preset: '#sPreset',
  user_agent: '#sUA', proxy: '#sProxy', ffmpeg_path: '#sFFmpeg',
  webhook_url: '#sWebhook', webhook_secret: '#sHookSecret',
  auto_clean_days: '#sClean', public_base: '#sPublic',
  trim_head: '#sTrimHead', trim_tail: '#sTrimTail'
};
const BOOL = {
  allow_transcode: '#sTranscode', thumbnails: '#sThumbs',
  keep_temp: '#sKeepTemp', insecure_tls: '#sInsecure',
  auto_update: '#sAutoUpdate', trim_enabled: '#sTrim'
};
function numVal(sel) { const v = parseInt($(sel).value, 10); return isNaN(v) ? 0 : v; }

// 关闭「去除片头片尾」时把两个输入框置灰，避免填了数字却没生效
function paintTrim() {
  const sw = $('#sTrim');
  const on = sw ? sw.checked : false;
  ['#sTrimHead', '#sTrimTail'].forEach((s) => {
    const el = $(s);
    if (el) el.disabled = !on;
  });
}
if ($('#sTrim')) $('#sTrim').addEventListener('change', paintTrim);

async function loadSettings() {
  try {
    const st = await api('/api/settings');
    state.settings = st;
    Object.keys(SET).forEach((k) => {
      const el = $(SET[k]);
      if (!el) return;
      el.value = st[k] == null ? '' : st[k];
    });
    Object.keys(BOOL).forEach((k) => {
      const el = $(BOOL[k]);
      if (el) el.checked = !!st[k];
    });
    paintTrim();
    renderDirs(st.save_dirs || []);
    fillDirSelect(st);
    if (st.api_key) $('#apiKeyBox').textContent = st.api_key;
    applyBase();
    renderStats();
  } catch (e) {
    toast('读取设置失败：' + e.message, 'err');
  }
}

function fillDirSelect(st) {
  const sel = $('#inDir');
  const cur = sel.value;
  sel.innerHTML = '';
  (st.save_dirs || []).forEach((d) => {
    const o = document.createElement('option');
    o.value = d.path;
    o.textContent = d.label ? d.label + ' · ' + d.path : d.path;
    sel.appendChild(o);
  });
  const want = cur || st.default_dir || (st.save_dirs && st.save_dirs[0] && st.save_dirs[0].path) || '';
  if (want) sel.value = want;
}

function dirRowHTML(d) {
  return '<button class="icon-btn" data-x="browse" aria-label="浏览目录" title="浏览">' + ico('folder') + '</button>' +
    '<input class="path" value="' + esc(d.path || '') + '" placeholder="/downloads/hdd" aria-label="目录路径" spellcheck="false">' +
    '<input class="label" value="' + esc(d.label || '') + '" placeholder="备注" aria-label="目录备注">' +
    '<button class="icon-btn" data-x="del" aria-label="删除该目录" title="删除">' + ico('x') + '</button>';
}
function renderDirs(dirs) {
  const host = $('#dirRows');
  host.innerHTML = '';
  (dirs.length ? dirs : [{ path: '', label: '' }]).forEach((d) => {
    const row = document.createElement('div');
    row.className = 'dir-row';
    row.innerHTML = dirRowHTML(d);
    host.appendChild(row);
  });
}
function collectDirs() {
  return $$('.dir-row').map((r) => ({
    path: ($('.path', r) || {}).value ? $('.path', r).value.trim() : '',
    label: ($('.label', r) || {}).value ? $('.label', r).value.trim() : ''
  })).filter((d) => d.path);
}

$('#dirRows').addEventListener('click', (e) => {
  const b = e.target.closest('[data-x]');
  if (!b) return;
  const row = b.closest('.dir-row');
  if (b.dataset.x === 'del') {
    if ($$('.dir-row').length <= 1) { toast('至少保留一个目录', 'err'); return; }
    row.remove();
    return;
  }
  if (b.dataset.x === 'browse') {
    const cur = $('.path', row).value.trim() || (state.settings && state.settings.default_dir) || '/';
    openBrowse(cur, (p) => { $('.path', row).value = p; });
  }
});
$('#btnAddDir').addEventListener('click', () => {
  const row = document.createElement('div');
  row.className = 'dir-row';
  row.innerHTML = dirRowHTML({ path: '', label: '' });
  $('#dirRows').appendChild(row);
  const inp = $('.path', row);
  if (inp) inp.focus();
});

function openBrowse(start, onPick) {
  const sheet = openSheet(
    '<h3>选择目录</h3>' +
    '<div class="mono" id="brPath" style="font-size:12px;word-break:break-all;color:var(--fg-dim);margin-bottom:8px"></div>' +
    '<div class="browse-list" id="brList"><div class="browse-item">载入中…</div></div>' +
    '<div class="sheet-actions">' +
      '<button class="ghost" data-x="up">' + ico('up') + '上一级</button>' +
      '<span class="spacer"></span>' +
      '<button class="ghost" data-x="close">取消</button>' +
      '<button class="primary" data-x="pick">选择此目录</button>' +
    '</div>'
  );
  let cur = start;
  const pathEl = $('#brPath', sheet), listEl = $('#brList', sheet);
  const load = async (p) => {
    listEl.innerHTML = '<div class="browse-item">载入中…</div>';
    try {
      const d = await api('/api/browse?path=' + encodeURIComponent(p));
      cur = d.path;
      pathEl.textContent = cur;
      const items = (d.entries || []);
      listEl.innerHTML = items.length
        ? items.map((x) => '<button class="browse-item" data-path="' + esc(x.path) + '">' + ico('folder') + '<span>' + esc(x.name) + '</span></button>').join('')
        : '<div class="browse-item">（没有子目录）</div>';
    } catch (ex) {
      listEl.innerHTML = '<div class="browse-item">无法打开：' + esc(ex.message) + '</div>';
    }
  };
  load(cur);
  sheet.addEventListener('click', (e) => {
    const item = e.target.closest('.browse-item[data-path]');
    if (item) { load(item.dataset.path); return; }
    const b = e.target.closest('[data-x]');
    if (!b) return;
    if (b.dataset.x === 'up') { load(cur.replace(/\/+[^/]*$/, '') || '/'); return; }
    if (b.dataset.x === 'pick') { onPick(cur); closeSheet(); return; }
    closeSheet();
  });
}

$('#btnSaveSettings').addEventListener('click', async () => {
  if (!state.settings) return;
  const btn = $('#btnSaveSettings');
  btn.disabled = true; btn.textContent = '保存中…';
  try {
    const st = Object.assign({}, state.settings);
    Object.keys(SET).forEach((k) => {
      const el = $(SET[k]);
      if (!el) return;
      st[k] = (k === 'hls_quality' || k === 'transcode_preset') ? el.value : (el.type === 'number' ? numVal(SET[k]) : el.value.trim());
    });
    Object.keys(BOOL).forEach((k) => { const el = $(BOOL[k]); if (el) st[k] = el.checked; });
    st.save_dirs = collectDirs();
    if (!st.default_dir || !st.save_dirs.some((d) => d.path === st.default_dir)) {
      st.default_dir = (st.save_dirs[0] || {}).path || '';
    }
    if (!st.save_dirs.length) throw new Error('至少需要一个保存目录');
    const out = await api('/api/settings', { method: 'PUT', body: st });
    state.settings = out;
    renderDirs(out.save_dirs || []);
    fillDirSelect(out);
    applyBase();
    await loadStats();
    await loadSystemInfo();
    toast('设置已保存', 'ok');
  } catch (ex) {
    toast(ex.message, 'err');
  } finally {
    btn.disabled = false; btn.textContent = '保存设置';
  }
});
$('#btnReloadSettings').addEventListener('click', () => loadSettings());
$('#btnCopyKey').addEventListener('click', () => {
  const k = $('#apiKeyBox').textContent.trim();
  if (!k || k === '—') { toast('暂无 API Key', 'err'); return; }
  copyText(k).then(() => toast('API Key 已复制', 'ok')).catch(() => toast('复制失败', 'err'));
});


/* ---------------- 系统更新 ---------------- */
state.sys = null;
state.update = null;

const UPD_SKIP_KEY = 'ms-upd-skip-version';   // 永久跳过某个版本（localStorage）
const UPD_DISMISS_PREFIX = 'ms-upd-dismiss-'; // 本次会话点过「取消更新」
let updPrompted = null;                       // 本次会话已经弹过的版本

async function loadSystemInfo() {
  try {
    const d = await api('/api/system/info');
    state.sys = d;
    if ($('#curVer')) $('#curVer').textContent = 'v' + d.version;
    if ($('#sysPlatform')) $('#sysPlatform').textContent = d.platform || (d.os + '/' + d.arch);
    paintFFmpeg(d);
  } catch (e) { /* 静默：不影响主流程 */ }
}

// 「设置 → 下载」里显示 ffmpeg 是否就绪。
// fpk 默认不内置 ffmpeg（大包会让旧版 fnOS 装不上），首次启动会在后台
// 自动下载组件；这里同时展示安装进度、失败原因与手动重试入口。
function paintFFmpeg(d, live) {
  const box = $('#ffmpegState');
  if (!box) return;
  const ok = !!(d && d.ffmpeg_ok);
  const p = (d && d.ffmpeg_path) || '';
  const st = live || (d && d.ffmpeg_state) || state.ffmpegState || {};
  const status = st.status || '';
  const busy = status === 'downloading' || status === 'installing';
  const failed = status === 'failed';
  const canInstall = !!(d && d.ffmpeg_can_install);

  box.classList.toggle('bad', !ok && !busy);
  box.classList.toggle('busy', busy);

  let text;
  if (ok) {
    text = 'ffmpeg 已就绪';
  } else if (busy) {
    text = esc(st.detail || '正在安装 ffmpeg 组件…') +
      (st.progress ? ' <b>' + st.progress + '%</b>' : '');
  } else if (failed) {
    text = esc(st.detail || '组件安装失败') + '，可手动指定 ffmpeg 路径或点击右侧重试';
  } else if (status === 'unsupported') {
    text = esc(st.detail || '当前架构没有预编译组件，请手动安装 ffmpeg');
  } else {
    text = '未找到 ffmpeg：正在后台自动补齐组件，m3u8 封装会在就绪后恢复';
  }

  let html = '<i></i><span>' + text + (p ? ' <code>' + esc(p) + '</code>' : '') + '</span>';
  if (!ok && !busy && canInstall) {
    html += '<button type="button" class="ff-retry" id="btnFFmpegRetry">' +
      (failed ? '重新安装' : '立即安装') + '</button>';
  }
  box.innerHTML = html;
  const btn = $('#btnFFmpegRetry');
  if (btn) {
    btn.addEventListener('click', async () => {
      btn.disabled = true;
      btn.textContent = '安装中…';
      try {
        const r = await api('/api/system/ffmpeg', { method: 'POST' });
        toast(r && r.message ? r.message : '已开始安装组件', 'ok');
      } catch (err) {
        toast('启动组件安装失败：' + err.message, 'err');
      }
      setTimeout(() => { btn.disabled = false; }, 3000);
    });
  }
}

function paintUpdate(st) {
  state.update = st;
  const badge = $('#updBadge'), pill = $('#pillUpdate'), apply = $('#btnApplyUpdate'), status = $('#updStatus');
  if (!status) return;
  if (st.current && $('#curVer')) $('#curVer').textContent = 'v' + st.current;
  $('#latestVer').textContent = st.latest ? 'v' + st.latest : '—';

  if (st.error) {
    status.textContent = '检查失败：' + cleanUpdError(st.error);
    badge.hidden = true; pill.hidden = true; apply.disabled = true;
  } else if (st.has_update) {
    status.textContent = st.can_auto
      ? '发现新版本 v' + st.latest + '，可以一键更新'
      : '发现新版本 v' + st.latest + '，但当前架构没有可用的自动更新包';
    badge.hidden = false; pill.hidden = false;
    apply.disabled = !st.can_auto;
  } else {
    const auto = !!(state.settings && state.settings.auto_update);
    status.textContent = '已是最新版本 · ' + (st.platform || '') + (auto ? ' · 自动更新已开启' : '');
    badge.hidden = true; pill.hidden = true; apply.disabled = true;
  }

  // 更新内容默认展开，重点部分由 renderNotes 高亮
  const notes = (st.notes || '').trim();
  const wrap = $('#updNotesWrap');
  if (wrap) { wrap.hidden = !notes; if (notes) wrap.open = true; }
  if (notes && $('#updNotes')) $('#updNotes').innerHTML = renderNotes(notes);

  maybePromptUpdate(st);
}

/* ---------------- 首页新版本弹窗 ----------------
   只在首页提醒；「不再更新此版本」按版本号记到 localStorage，
   出现更高的版本时仍然会弹；「取消更新」只在本会话内不再弹。 */
function shouldPromptUpdate(st) {
  if (!st || !st.has_update || st.error || !st.latest) return false;
  if (state.view !== 'home') return false;
  if (updPrompted === st.latest) return false;
  if (ssGet(UPD_DISMISS_PREFIX + st.latest)) return false;
  const skip = lsGet(UPD_SKIP_KEY);
  // 跳过的是这个版本（或更新的版本）就不再提醒；来了更高版本照常弹窗
  if (skip && !isNewerVer(st.latest, skip)) return false;
  return true;
}
function maybePromptUpdate(st) {
  if (shouldPromptUpdate(st)) showUpdateDialog(st);
}

function showUpdateDialog(st) {
  updPrompted = st.latest;
  const from = st.current || (state.sys && state.sys.version) || '';
  const canAuto = !!st.can_auto;
  const date = st.released_at ? String(st.released_at).slice(0, 10) : '';
  const sheet = openSheet(
    '<div class="upd-dialog">' +
      '<div class="upd-head">' +
        '<span class="upd-kicker"><i></i>发现新版本</span>' +
        '<div class="upd-ver">' +
          (from ? '<span class="from">v' + esc(from) + '</span><span class="arrow">→</span>' : '') +
          '<strong>v' + esc(st.latest) + '</strong>' +
        '</div>' +
        (date ? '<p class="upd-date">发布于 ' + esc(date) + '</p>' : '') +
      '</div>' +
      '<div class="upd-sec-title">更新内容</div>' +
      '<div class="chg-box">' + renderNotes(st.notes) + '</div>' +
      (canAuto ? '' : '<p class="upd-warn">当前架构暂无自动更新包，请前往 Releases 手动下载。</p>') +
      '<div class="upd-dialog-actions">' +
        '<button class="ghost upd-skip" data-x="skip">不再更新此版本</button>' +
        '<span class="spacer"></span>' +
        '<button class="ghost" data-x="cancel">取消更新</button>' +
        '<button class="primary" data-x="ok"' + (canAuto ? '' : ' disabled') + '>确认更新</button>' +
      '</div>' +
      '<p class="upd-skip-note">仅跳过 v' + esc(st.latest) + '，下一个新版本仍会提醒你更新。</p>' +
    '</div>'
  );
  sheet.classList.add('upd-sheet');
  sheet.addEventListener('click', (e) => {
    const b = e.target.closest('[data-x]');
    if (!b) return;
    const x = b.dataset.x;
    if (x === 'skip') {
      lsSet(UPD_SKIP_KEY, st.latest);
      closeSheet();
      toast('已跳过 v' + st.latest + '，下个版本会再提醒', 'ok');
    } else if (x === 'cancel') {
      ssSet(UPD_DISMISS_PREFIX + st.latest, '1');
      closeSheet();
    } else if (x === 'ok') {
      closeSheet();
      runUpdate();
    }
  });
}

async function checkUpdate(force) {
  const btn = $('#btnCheckUpdate');
  if (!btn) return;
  const label = btn.textContent;
  btn.disabled = true; btn.textContent = '检查中…';
  try {
    const st = await api('/api/system/update' + (force ? '?force=1' : ''));
    paintUpdate(st);
    if (force) toast(st.error ? ('检查失败：' + cleanUpdError(st.error)) : (st.has_update ? ('发现新版本 v' + st.latest) : '已是最新版本'), st.error ? 'err' : 'ok');
  } catch (e) {
    toast('检查更新失败：' + cleanUpdError(e.message), 'err');
    if ($('#updStatus')) $('#updStatus').textContent = '检查失败：' + cleanUpdError(e.message);
  } finally {
    btn.disabled = false; btn.textContent = label;
  }
}

function askUpdate(st) {
  return new Promise((resolve) => {
    const sheet = openSheet(
      '<h3>更新到 v' + esc(st.latest) + '</h3>' +
      '<p class="hint" style="margin:0 0 12px">' +
        '将下载更新包 <span class="mono">' + esc(st.asset_name || '') + '</span>' +
        (st.asset_size ? '（' + fmtBytes(st.asset_size) + '）' : '') +
        '，完成后自动校验并重启。更新期间下载任务会暂停，重启后自动恢复队列。' +
      '</p>' +
      '<div class="sheet-actions">' +
        '<button class="ghost" data-x="cancel">取消</button>' +
        '<span class="spacer"></span>' +
        '<button class="primary" data-x="ok">立即更新</button>' +
      '</div>'
    );
    let done = false;
    sheet.addEventListener('click', (e) => {
      const b = e.target.closest('[data-x]');
      if (!b) return;
      done = true;
      closeSheet();
      resolve(b.dataset.x === 'ok');
    });
    const obs = new MutationObserver(() => {
      if (!document.body.contains(sheet) && !done) { done = true; obs.disconnect(); resolve(false); }
    });
    obs.observe(document.body, { childList: true });
  });
}

async function applyUpdate() {
  const st = state.update;
  if (!st || !st.has_update) { toast('当前已是最新版本', 'err'); return; }
  if (!(await askUpdate(st))) return;
  return runUpdate();
}

// 真正执行更新。首页弹窗里的「确认更新」直接走这里，不再二次确认。
async function runUpdate() {
  const st = state.update;
  if (!st || !st.has_update) { toast('当前已是最新版本', 'err'); return; }
  const btn = $('#btnApplyUpdate');
  const label = btn.textContent;
  btn.disabled = true; btn.textContent = '下载中…';
  $('#updStatus').textContent = '正在下载并校验更新包，请勿关闭页面…';
  try {
    const r = await api('/api/system/update', { method: 'POST' });
    toast(r.message || '更新完成，正在重启', 'ok');
    $('#updStatus').textContent = r.restarting ? '服务正在重启，稍候…' : (r.message || '');
    if (r.restarting) waitRestart();
    else { btn.disabled = false; btn.textContent = label; }
  } catch (e) {
    toast(e.message, 'err');
    $('#updStatus').textContent = e.message;
    btn.disabled = false; btn.textContent = label;
  }
}

async function waitRestart() {
  const before = (state.sys && state.sys.version) || (state.update && state.update.current) || '';
  for (let i = 0; i < 60; i++) {
    await new Promise((r) => setTimeout(r, 1500));
    try {
      const r = await fetch('/api/health', { cache: 'no-store' });
      if (!r.ok) continue;
      const d = await r.json();
      if (d.version && d.version !== before) { location.reload(); return; }
    } catch (e) { /* 还没起来 */ }
  }
  toast('重启时间较长，请手动刷新页面', 'err');
  const btn = $('#btnApplyUpdate');
  if (btn) { btn.disabled = false; btn.textContent = '一键更新'; }
}

if ($('#btnCheckUpdate')) $('#btnCheckUpdate').addEventListener('click', () => checkUpdate(true));
if ($('#btnApplyUpdate')) $('#btnApplyUpdate').addEventListener('click', () => applyUpdate());
if ($('#pillUpdate')) {
  const go = () => { location.hash = '#/settings'; checkUpdate(true); };
  $('#pillUpdate').addEventListener('click', go);
  $('#pillUpdate').addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); go(); } });
}

/* ---------------- 管理员密码 ---------------- */
if ($('#btnChangePw')) {
  $('#btnChangePw').addEventListener('click', async () => {
    const oldP = $('#pwOld').value;
    const n1 = $('#pwNew').value;
    const n2 = $('#pwNew2').value;
    if (!oldP) return toast('请输入当前密码', 'err');
    if (n1.length < 6) return toast('新密码至少 6 位', 'err');
    if (n1 !== n2) return toast('两次输入的新密码不一致', 'err');
    const btn = $('#btnChangePw');
    const label = btn.textContent;
    btn.disabled = true; btn.textContent = '提交中…';
    try {
      const r = await api('/api/system/password', {
        method: 'POST',
        body: { old_password: oldP, new_password: n1 }
      });
      toast(r.message || '密码已更新', 'ok');
      $('#pwOld').value = ''; $('#pwNew').value = ''; $('#pwNew2').value = '';
    } catch (e) {
      toast(e.message, 'err');
    } finally {
      btn.disabled = false; btn.textContent = label;
    }
  });
}

/* ---------------- 夜间 / 白天 ---------------- */
const THEME_KEY = 'ms-theme';
function currentTheme() {
  return document.documentElement.dataset.theme === 'light' ? 'light' : 'dark';
}
function paintThemeBtn() {
  const b = $('#btnTheme');
  if (!b) return;
  const light = currentTheme() === 'light';
  b.setAttribute('aria-pressed', String(light));
  b.setAttribute('aria-label', light ? '切换到夜间模式' : '切换到白天模式');
  b.title = light ? '切换到夜间模式' : '切换到白天模式';
  document.documentElement.style.colorScheme = light ? 'light' : 'dark';
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.setAttribute('content', light ? '#f6f7f9' : '#0a0b0d');
}
function applyTheme(next) {
  document.documentElement.dataset.theme = next;
  try { localStorage.setItem(THEME_KEY, next); } catch (e) { /* ignore */ }
  paintThemeBtn();
}
function toggleTheme(ev) {
  const next = currentTheme() === 'light' ? 'dark' : 'light';
  const btn = $('#btnTheme');
  const r = btn ? btn.getBoundingClientRect() : { left: 0, top: 0, width: 0, height: 0 };
  const x = ev && ev.clientX ? ev.clientX : r.left + r.width / 2;
  const y = ev && ev.clientY ? ev.clientY : r.top + r.height / 2;
  const radius = Math.hypot(Math.max(x, window.innerWidth - x), Math.max(y, window.innerHeight - y));
  const root = document.documentElement;
  root.style.setProperty('--ripple-x', x + 'px');
  root.style.setProperty('--ripple-y', y + 'px');
  root.style.setProperty('--ripple-r', radius + 'px');

  if (document.startViewTransition) {
    document.startViewTransition(() => applyTheme(next));
    return;
  }
  // 老浏览器：退化为整体颜色渐变
  root.classList.add('theme-fade');
  applyTheme(next);
  setTimeout(() => root.classList.remove('theme-fade'), 420);
}
if ($('#btnTheme')) $('#btnTheme').addEventListener('click', toggleTheme);
paintThemeBtn();

/* ---------------- 文档页 ---------------- */
function setDoc(d) {
  state.doc = d;
  $$('[data-doc]').forEach((b) => b.setAttribute('aria-pressed', String(b.dataset.doc === d)));
  $('#docApi').classList.toggle('hidden', d !== 'api');
  $('#docMcp').classList.toggle('hidden', d !== 'mcp');
}
$$('[data-doc]').forEach((b) => b.addEventListener('click', () => {
  setDoc(b.dataset.doc);
  const h = '#/docs/' + b.dataset.doc;
  if (location.hash !== h) history.replaceState(null, '', h);
}));

/* ---------------- 设置分类 ---------------- */
const SET_TABS = ['update', 'download', 'storage', 'notify', 'security'];
function setSetTab(t) {
  if (SET_TABS.indexOf(t) < 0) t = 'update';
  state.setTab = t;
  $$('[data-set]').forEach((b) => b.setAttribute('aria-pressed', String(b.dataset.set === t)));
  SET_TABS.forEach((k) => {
    const el = $('#set' + k.charAt(0).toUpperCase() + k.slice(1));
    if (el) el.classList.toggle('hidden', k !== t);
  });
}
$$('[data-set]').forEach((b) => b.addEventListener('click', () => {
  setSetTab(b.dataset.set);
  const h = '#/settings/' + b.dataset.set;
  if (location.hash !== h) history.replaceState(null, '', h);
}));

function applyBase() {
  const pb = state.settings && state.settings.public_base ? String(state.settings.public_base) : '';
  const origin = (pb || location.origin).replace(/\/+$/, '');
  $$('[data-base]').forEach((el) => {
    if (!el.dataset.tpl) el.dataset.tpl = el.textContent || '';
    const tpl = el.dataset.tpl;
    el.textContent = tpl.indexOf('…') >= 0 ? origin : origin + tpl;
  });
}

document.addEventListener('click', (e) => {
  const b = e.target.closest('[data-copy]');
  if (!b) return;
  const pre = b.parentElement.querySelector('pre');
  if (!pre) return;
  copyText(pre.innerText).then(() => {
    b.classList.add('done');
    b.innerHTML = ico('check');
    setTimeout(() => { b.classList.remove('done'); b.innerHTML = ico('copy'); }, 1400);
    toast('已复制', 'ok');
  }).catch(() => toast('复制失败', 'err'));
});

/* ---------------- 路由 ---------------- */
const VIEWS = ['home', 'tasks', 'docs', 'settings'];
// 页面区块可能因缓存出现「新 JS + 旧 HTML」的组合，取不到就回退首页，别让整页卡死
function viewEl(v) { return document.getElementById('view' + v.charAt(0).toUpperCase() + v.slice(1)); }
function route() {
  const raw = location.hash.replace(/^#\/?/, '');
  const parts = raw.split('/').filter(Boolean);
  let view = parts[0] || 'home';
  if (VIEWS.indexOf(view) < 0 || !viewEl(view)) view = 'home';
  state.view = view;
  VIEWS.forEach((v) => {
    const el = viewEl(v);
    if (el) el.classList.toggle('hidden', v !== view);
  });
  $$('.tabbar a').forEach((a) => {
    if (a.dataset.view === view) a.setAttribute('aria-current', 'page');
    else a.removeAttribute('aria-current');
  });
  if (view === 'docs') setDoc(parts[1] === 'mcp' ? 'mcp' : 'api');
  if (view === 'settings') setSetTab(parts[1] || state.setTab);
  document.title = view === 'docs' ? 'API / MCP 文档 · Media Super'
    : view === 'settings' ? '设置 · Media Super'
    : view === 'tasks' ? '任务 · Media Super'
    : '首页 · Media Super';
  window.scrollTo(0, 0);

  // 从其它页回到首页时，用已缓存的检查结果补一次弹窗（不额外请求网络）
  if (view === 'home' && state.update) setTimeout(() => maybePromptUpdate(state.update), 80);
}
window.addEventListener('hashchange', route);
// hash 相同不会触发 hashchange（比如上次渲染失败停在首页），点导航时兜底重绘一次
$$('.tabbar a, .brand').forEach((a) => a.addEventListener('click', () => setTimeout(route, 0)));
// 从浏览器往返缓存恢复时也重绘，避免看到过期的页面状态
window.addEventListener('pageshow', (e) => { if (e.persisted) route(); });

/* ---------------- 初始化 ---------------- */
// 手机端关掉橡皮筋回弹：滚到顶 / 底之后再往外拉，不再把整页带动。
// 弹层、代码块这些内部可滚动区域交给它们自己处理。
(function lockOverscroll() {
  let startY = 0;
  document.addEventListener('touchstart', (e) => {
    if (e.touches.length === 1) startY = e.touches[0].clientY;
  }, { passive: true });
  document.addEventListener('touchmove', (e) => {
    if (e.touches.length !== 1) return;
    const t = e.target;
    if (t && t.closest && t.closest('.sheet, .sheet-mask, .code pre, .notes-pre, .chg-box, textarea')) return;
    const el = document.scrollingElement || document.documentElement;
    const dy = e.touches[0].clientY - startY;
    const atTop = el.scrollTop <= 0;
    const atBottom = el.scrollTop + el.clientHeight >= el.scrollHeight - 1;
    if ((atTop && dy > 0) || (atBottom && dy < 0)) e.preventDefault();
  }, { passive: false });
})();

$$('[data-copy]').forEach((b) => { b.innerHTML = ico('copy'); });
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible' && state.es && state.es.readyState === 2) connectEvents();
});
boot();
