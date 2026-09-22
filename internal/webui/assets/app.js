/* Keypoint board — vanilla JS, no build step.
 *
 * The API is the interface; this file is one consumer of it. Everything it does
 * is something the CLI does too, so if a behaviour is missing here it is still
 * reachable from the terminal.
 */
'use strict';

// ------------------------------------------------------------------ state
const S = {
  me: null,
  tasks: [],
  filter: { q: '', status: '', role: '', assigned: '' },
  route: { name: 'board', code: null },
  task: null,
};

// ------------------------------------------------------------------- api
async function api(method, path, body, raw) {
  const opts = { method, headers: {}, credentials: 'same-origin' };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const resp = await fetch('/api/v1' + path, opts);
  if (resp.status === 401) { showGate(); throw new Error('unauthorized'); }
  const text = await resp.text();
  if (raw) {
    if (!resp.ok) throw new Error(text);
    return text;
  }
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (!resp.ok) {
    const err = new Error((data && data.message) || resp.statusText);
    err.api = data;
    throw err;
  }
  return data;
}

// ----------------------------------------------------------------- toast
let toastTimer = null;
function toast(msg, isErr) {
  const el = document.getElementById('toast');
  el.textContent = msg;
  el.className = 'toast' + (isErr ? ' err' : '');
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; }, isErr ? 5200 : 2400);
}
function fail(err) {
  console.error(err);
  const api = err && err.api;
  if (api) {
    toast(api.error + '：' + api.message + (api.hint ? ' — ' + api.hint : ''), true);
  } else {
    toast(String(err && err.message || err), true);
  }
}

// ------------------------------------------------------------------- esc
function esc(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

/* A deliberately small Markdown subset: enough for task prose, small enough to
 * audit. Everything is escaped first, so a segment body can never inject HTML. */
function md(src) {
  if (!src) return '';
  let s = esc(src);
  const blocks = [];
  s = s.replace(/```([\s\S]*?)```/g, (_, code) => {
    blocks.push('<pre><code>' + code.replace(/^\w*\n/, '') + '</code></pre>');
    return '\u0000B' + (blocks.length - 1) + '\u0000';
  });
  s = s.replace(/`([^`\n]+)`/g, '<code>$1</code>');
  s = s.replace(/!\[([^\]]*)\]\(([^)\s]+)\)/g, '<img alt="$1" src="$2" loading="lazy">');
  s = s.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noreferrer">$1</a>');
  s = s.replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>');
  s = s.replace(/(^|[\s(])\*([^*\n]+)\*/g, '$1<em>$2</em>');

  const out = [];
  for (const line of s.split('\n')) {
    const h = line.match(/^(#{1,4})\s+(.*)$/);
    const li = line.match(/^\s*[-*]\s+(.*)$/);
    const bq = line.match(/^&gt;\s?(.*)$/);
    if (h) out.push('<h' + h[1].length + '>' + h[2] + '</h' + h[1].length + '>');
    else if (li) out.push('<li>' + li[1] + '</li>');
    else if (bq) out.push('<blockquote>' + bq[1] + '</blockquote>');
    else if (/^\s*---+\s*$/.test(line)) out.push('<hr>');
    else if (!line.trim()) out.push('');
    else out.push('<p>' + line + '</p>');
  }
  let html = out.join('\n')
    .replace(/(<li>[\s\S]*?<\/li>)\n(?=<li>)/g, '$1')
    .replace(/(?:<li>[\s\S]*?<\/li>\n?)+/g, m => '<ul>' + m.replace(/\n/g, '') + '</ul>');
  return html.replace(/\u0000B(\d+)\u0000/g, (_, i) => blocks[+i]);
}

// ------------------------------------------------------------- clipboard
async function copy(text, label) {
  try {
    await navigator.clipboard.writeText(text);
    toast('已复制' + (label ? '：' + label : ''));
  } catch {
    // Clipboard API needs a secure context; plain-http LAN use falls back.
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    document.execCommand('copy');
    ta.remove();
    toast('已复制' + (label ? '：' + label : ''));
  }
}

// ------------------------------------------------------------ formatting
const TASK_STATUS = {
  inbox: '待整理', ready: '可开工', doing: '进行中',
  blocked: '阻塞', review: '待审查', done: '已完成', archived: '已归档',
};
const SIDE_STATUS = { todo: '未开始', doing: '进行中', blocked: '阻塞', done: '已完成' };
const REPORT_LABEL = {
  progress: '进展', blocker: '阻塞', decision: '决策',
  handoff: '交接', result: '结果', question: '提问',
};
const REPORT_CLASS = {
  progress: '', blocker: 'blocker', decision: 'decision',
  handoff: 'handoff', result: 'result', question: 'question',
};
const COLUMNS = ['inbox', 'ready', 'doing', 'blocked', 'review', 'done'];

function fmtWhen(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  const diff = (Date.now() - d.getTime()) / 1000;
  if (diff < 60) return '刚刚';
  if (diff < 3600) return Math.floor(diff / 60) + ' 分钟前';
  if (diff < 86400) return Math.floor(diff / 3600) + ' 小时前';
  if (diff < 86400 * 7) return Math.floor(diff / 86400) + ' 天前';
  return d.toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' });
}

// ------------------------------------------------------------------ boot
async function boot() {
  wireGate();
  try {
    S.me = await api('GET', '/whoami');
  } catch { return; }
  showApp();
  wireChrome();
  window.addEventListener('popstate', route);
  route();
  pollUnread();
  setInterval(pollUnread, 20000);
}

function showGate() {
  document.getElementById('gate').hidden = false;
  document.getElementById('app').hidden = true;
}

function showApp() {
  document.getElementById('gate').hidden = true;
  document.getElementById('app').hidden = false;
  renderIdentityChip();
}

function wireGate() {
  const form = document.getElementById('gate-form');
  const input = document.getElementById('gate-key');
  const err = document.getElementById('gate-error');
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    err.hidden = true;
    try {
      await api('POST', '/session', { key: input.value.trim() });
      location.reload();
    } catch {
      err.textContent = 'key 无效。确认复制完整；假如刚轮换过，旧 key 已失效。';
      err.hidden = false;
    }
  });
}

function wireChrome() {
  document.addEventListener('click', (e) => {
    const a = e.target.closest('a[data-link]');
    if (a) {
      e.preventDefault();
      const href = a.getAttribute('href');
      if (href !== location.pathname) history.pushState({}, '', href);
      route();
    }
    if (!e.target.closest('#identity-pop') && !e.target.closest('#identity-chip')) {
      document.getElementById('identity-pop').hidden = true;
    }
  });
  const search = document.getElementById('search');
  let t = null;
  search.addEventListener('input', () => {
    clearTimeout(t);
    t = setTimeout(() => {
      S.filter.q = search.value.trim();
      if (S.route.name !== 'board') { history.pushState({}, '', '/'); S.route = { name: 'board' }; }
      renderBoard();
    }, 250);
  });
  document.getElementById('identity-chip').addEventListener('click', (e) => {
    e.stopPropagation();
    toggleIdentityPop();
  });
}

// ------------------------------------------------------------- identity
function renderIdentityChip() {
  if (!S.me) return;
  document.getElementById('identity-chip').innerHTML =
    '<span>' + esc(S.me.identity.name) + '</span>' +
    '<span class="faint">·</span>' +
    '<span style="color:var(--accent)">@' + esc(S.me.role) + '</span>';
}

function toggleIdentityPop() {
  const pop = document.getElementById('identity-pop');
  if (!pop.hidden) { pop.hidden = true; return; }
  const id = S.me.identity;
  const roles = id.roles.map(r =>
    '<option value="' + esc(r) + '"' + (r === S.me.role ? ' selected' : '') + '>' + esc(r) + '</option>'
  ).join('');
  document.getElementById('identity-pop-body').innerHTML =
    '<div style="font-weight:600;margin-bottom:2px">' + esc(id.name) + '</div>' +
    '<div class="muted small" style="margin-bottom:10px">' + esc(id.kind) +
      ' · key ' + esc(id.key_prefix || '') + '</div>' +
    '<label class="small muted">激活角色（决定上报归属）</label>' +
    '<div class="row"><select id="pop-role" style="flex:1;background:var(--bg-sunken);color:var(--fg);border:1px solid var(--line);border-radius:6px;padding:4px 7px">' +
      roles + '</select><button class="btn tiny" id="pop-role-save">切换</button></div>' +
    '<div class="row"><a class="btn tiny" href="/admin" data-link style="text-align:center">管理身份</a>' +
      '<button class="btn tiny danger" id="pop-logout">退出登录</button></div>';

  const rect = document.getElementById('identity-chip').getBoundingClientRect();
  pop.style.top = (rect.bottom + 8) + 'px';
  pop.style.right = '18px';
  pop.hidden = false;

  document.getElementById('pop-role-save').onclick = async () => {
    const role = document.getElementById('pop-role').value;
    try {
      const r = await api('PATCH', '/identities/' + S.me.identity.id, { active_role: role });
      S.me.identity = r.identity;
      S.me.role = r.identity.active_role;
      renderIdentityChip();
      pop.hidden = true;
      toast('已切换为 @' + role);
      route();
    } catch (e) { fail(e); }
  };
  document.getElementById('pop-logout').onclick = async () => {
    await api('DELETE', '/session');
    location.reload();
  };
}

async function pollUnread() {
  try {
    const r = await api('GET', '/inbox?unread=1&limit=1');
    const badge = document.getElementById('nav-unread');
    badge.hidden = !r.unread;
    badge.textContent = r.unread;
  } catch { /* transient */ }
}

// ---------------------------------------------------------------- router
function route() {
  const path = location.pathname;
  document.querySelectorAll('.nav a[data-nav]').forEach(a => {
    const target = a.getAttribute('href');
    a.classList.toggle('active',
      target === '/' ? (path === '/' || path.startsWith('/t/')) : path.startsWith(target));
  });
  if (path.startsWith('/t/')) {
    S.route = { name: 'task', code: decodeURIComponent(path.slice(3)) };
    return renderTask(S.route.code);
  }
  if (path.startsWith('/inbox')) { S.route = { name: 'inbox' }; return renderInbox(); }
  if (path.startsWith('/admin')) { S.route = { name: 'admin' }; return renderAdmin(); }
  S.route = { name: 'board' };
  return renderBoard();
}

function setView(html, narrow) {
  const v = document.getElementById('view');
  v.className = 'view' + (narrow ? ' narrow' : '');
  v.innerHTML = html;
}

function loading() { setView('<div class="spinner">加载中…</div>'); }

// ----------------------------------------------------------------- board
async function renderBoard() {
  const params = new URLSearchParams({ group: 'status', limit: '300' });
  if (S.filter.q) params.set('q', S.filter.q);
  if (S.filter.role) params.set('role', S.filter.role);
  if (S.filter.assigned) params.set('assigned', S.filter.assigned);
  if (S.filter.status) params.set('status', S.filter.status);
  let data;
  try { data = await api('GET', '/tasks?' + params.toString()); } catch (e) { return fail(e); }

  const cols = data.columns || {};
  let html = '<div class="board" id="board">';
  for (const col of COLUMNS) {
    const items = cols[col] || [];
    html += '<div class="column" data-status="' + col + '">' +
      '<div class="column-head"><span>' + TASK_STATUS[col] +
      '</span><span class="count">' + items.length + '</span></div>';
    for (const t of items) html += cardHTML(t);
    html += '</div>';
  }
  html += '</div>';
  if (!data.count) {
    html += '<div class="empty-state"><div class="big">📍</div>' +
      (S.filter.q ? '没有匹配 “' + esc(S.filter.q) + '” 的任务' : '还没有任务') +
      '<div class="small" style="margin-top:8px">用 <code>kp task new</code> 或在 Claude Code 里说“记一个任务：……”</div></div>';
  }
  setView(html);
  wireBoard();
}

function cardHTML(t) {
  const sides = (t.sides || []).map(s =>
    '<span class="side-dot ' + esc(s.status) + '" title="' + esc(s.title || s.key) +
    (s.assignee_role ? ' → @' + esc(s.assignee_role) : '') + '">' +
    esc(s.key) + '</span>').join('');
  return '<div class="card' + (t.unread_count ? ' unread' : '') + '" draggable="true" data-code="' + esc(t.code) + '">' +
    '<div class="card-top">' +
      '<span class="card-code">' + esc(t.code) + '</span>' +
      (t.priority && t.priority !== 'P2' ? '<span class="chip ' + esc(t.priority) + '">' + esc(t.priority) + '</span>' : '') +
      '<span class="chip">' + esc(t.kind) + '</span>' +
    '</div>' +
    '<div class="card-title">' + esc(t.title) + '</div>' +
    '<div class="card-meta">' +
      (t.owner_role ? '<span class="chip role">@' + esc(t.owner_role) + '</span>' : '') +
      (t.report_count ? '<span class="chip">' + t.report_count + ' 上报</span>' : '') +
      '<span class="faint small">' + fmtWhen(t.updated_at) + '</span>' +
    '</div>' +
    (sides ? '<div class="card-sides">' + sides + '</div>' : '') +
  '</div>';
}

function wireBoard() {
  const board = document.getElementById('board');
  if (!board) return;
  board.querySelectorAll('.card').forEach(card => {
    card.addEventListener('click', () => {
      const code = card.dataset.code;
      history.pushState({}, '', '/t/' + encodeURIComponent(code));
      route();
    });
    card.addEventListener('dragstart', (e) => {
      card.classList.add('dragging');
      e.dataTransfer.setData('text/plain', card.dataset.code);
      e.dataTransfer.effectAllowed = 'move';
    });
    card.addEventListener('dragend', () => card.classList.remove('dragging'));
  });
  board.querySelectorAll('.column').forEach(col => {
    col.addEventListener('dragover', (e) => {
      e.preventDefault();
      col.classList.add('drop-target');
    });
    col.addEventListener('dragleave', () => col.classList.remove('drop-target'));
    col.addEventListener('drop', async (e) => {
      e.preventDefault();
      col.classList.remove('drop-target');
      const code = e.dataTransfer.getData('text/plain');
      const status = col.dataset.status;
      if (!code) return;
      try {
        await api('PATCH', '/tasks/' + encodeURIComponent(code), { status });
        toast(code + ' → ' + TASK_STATUS[status]);
        renderBoard();
      } catch (err) { fail(err); }
    });
  });
}

// ------------------------------------------------------------ task detail
async function renderTask(code) {
  loading();
  let t;
  try { t = await api('GET', '/tasks/' + encodeURIComponent(code)); }
  catch (e) { return fail(e); }
  S.task = t;
  let reports = { reports: [] };
  try { reports = await api('GET', '/tasks/' + encodeURIComponent(code) + '/reports?limit=50'); }
  catch { /* timeline is optional */ }

  const sides = (t.sides || []);
  let html = '';
  html += '<div class="task-head">' +
    '<a class="crumbs" href="/" data-link>← 看板</a>' +
    '<h1>' + esc(t.title) + '</h1>' +
    '<div class="task-meta">' +
      '<span class="chip mono">' + esc(t.code) + '</span>' +
      '<span class="chip ' + esc(t.priority) + '">' + esc(t.priority) + '</span>' +
      '<span class="chip">' + esc(t.kind) + '</span>' +
      '<select id="task-status" class="chip" style="background:var(--bg-hover);border:1px solid var(--line);border-radius:20px;padding:3px 8px;font-size:12px">' +
        COLUMNS.concat('archived').map(s =>
          '<option value="' + s + '"' + (s === t.status ? ' selected' : '') + '>' +
          TASK_STATUS[s] + '</option>').join('') +
      '</select>' +
      (t.owner_role ? '<span class="chip role">@' + esc(t.owner_role) + '</span>' : '') +
      (t.owner_identity ? '<span class="chip">' + esc(t.owner_identity) + '</span>' : '') +
      (t.labels || []).map(l => '<span class="chip">' + esc(l) + '</span>').join('') +
      '<span class="faint small" style="margin-left:auto">更新于 ' + fmtWhen(t.updated_at) + '</span>' +
    '</div>' +
    (t.summary ? '<div class="task-summary">' + esc(t.summary) + '</div>' : '') +
    '<div class="task-actions">' +
      '<button class="btn primary" id="copy-pack">复制开工包</button>' +
      '<button class="btn" id="copy-pack-json">复制 JSON</button>' +
      '<button class="btn" id="show-plain">纯文本预览</button>' +
      '<button class="btn danger" id="task-del" style="margin-left:auto">删除任务</button>' +
    '</div>' +
  '</div>';

  // sides
  if (sides.length) {
    html += '<div class="section"><div class="section-head"><span>工作面</span><span class="rule"></span>' +
      '<button class="btn tiny" id="side-add">+ 加工作面</button></div>';
    html += '<table class="sides-table"><thead><tr>' +
      '<th>side</th><th>状态</th><th>负责角色</th><th>负责身份</th><th>依赖</th><th>开工</th><th></th>' +
      '</tr></thead><tbody>';
    for (const s of sides) {
      const deps = (s.deps || []).map(d => {
        const dep = sides.find(x => x.key === d);
        const cls = dep && dep.status !== 'done' ? 'chip warn' : 'chip';
        return '<span class="' + cls + '">' + esc(d) + '</span>';
      }).join(' ') || '<span class="faint">—</span>';
      html += '<tr data-side="' + esc(s.key) + '">' +
        '<td class="k">' + esc(s.key) + '</td>' +
        '<td><select class="side-status">' +
          Object.keys(SIDE_STATUS).map(st =>
            '<option value="' + st + '"' + (st === s.status ? ' selected' : '') + '>' +
            SIDE_STATUS[st] + '</option>').join('') +
        '</select></td>' +
        '<td><input class="side-role" value="' + esc(s.assignee_role || '') + '" placeholder="—" ' +
          'style="width:92px;background:var(--bg-sunken);color:var(--fg);border:1px solid var(--line);border-radius:4px;padding:2px 5px;font-size:12px"></td>' +
        '<td class="faint">' + esc(s.assignee_identity || '—') + '</td>' +
        '<td>' + deps + '</td>' +
        '<td><button class="btn tiny side-pack">复制包</button></td>' +
        '<td><button class="btn tiny danger side-del">删除</button></td>' +
      '</tr>';
    }
    html += '</tbody></table></div>';
  }

  // segments
  html += '<div class="section"><div class="section-head"><span>分段</span><span class="rule"></span>' +
    '<button class="btn tiny" id="seg-add">+ 加分段</button></div>';
  const allSegs = (t.segments || []).slice();
  for (const s of sides) for (const g of (s.segments || [])) allSegs.push(Object.assign({}, g, { side_key: s.key }));
  if (!allSegs.length) {
    html += '<div class="empty-state small">还没有分段</div>';
  }
  for (const g of allSegs) html += segHTML(g);

  // attachments
  const atts = t.attachments || [];
  if (atts.length) {
    html += '<div class="section"><div class="section-head"><span>附件</span><span class="rule"></span></div>' +
      '<div class="report-att">' + atts.map(attHTML).join('') + '</div></div>';
  }

  // timeline
  html += '<div class="section"><div class="section-head"><span>上报时间线</span><span class="rule"></span></div>';
  const list = (reports.reports || []);
  if (!list.length) html += '<div class="empty-state small">还没有上报</div>';
  for (const r of list) html += reportHTML(r);
  html += '</div>';

  // composer
  html += composerHTML(sides);

  setView(html, true);
  wireTask(t, sides, allSegs);
}

function segHTML(g) {
  const empty = !g.body || !g.body.trim();
  return '<div class="seg" data-seg="' + esc(g.key) + '">' +
    '<div class="seg-head">' +
      '<span class="seg-key">' + esc(g.key) + '</span>' +
      '<span class="seg-title">' + esc(g.title || '') + '</span>' +
      (g.side_key ? '<span class="seg-side">side:' + esc(g.side_key) + '</span>' : '') +
      '<span class="spacer"></span>' +
      '<button class="btn tiny seg-copy">复制</button>' +
      '<button class="btn tiny seg-prompt">复制为 prompt</button>' +
      '<button class="btn tiny seg-edit">编辑</button>' +
    '</div>' +
    '<div class="seg-body' + (empty ? ' empty' : '') + '">' +
      (empty ? '（待补）' : '<div class="md">' + md(g.body) + '</div>') +
    '</div>' +
  '</div>';
}

function attHTML(a) {
  return a.is_image
    ? '<img src="' + esc(a.url) + '" alt="' + esc(a.name) + '" loading="lazy" data-zoom="' + esc(a.url) + '">'
    : '<a class="chip" href="' + esc(a.url) + '" target="_blank" rel="noreferrer">📎 ' + esc(a.name) + '</a>';
}

function reportHTML(r) {
  const cls = REPORT_CLASS[r.type] || '';
  const segs = (r.segments || []).map(s =>
    '<div class="report-seg"><span class="k">[' + esc(s.key) + '] ' + esc(s.title || '') + '</span>' +
    '<div class="md">' + md(s.body) + '</div></div>').join('');
  const atts = (r.attachments || []).length
    ? '<div class="report-att">' + r.attachments.map(attHTML).join('') + '</div>' : '';
  return '<div class="report ' + cls + '">' +
    '<div class="report-head">' +
      '<span class="chip ' + (r.type === 'blocker' ? 'danger' : r.type === 'result' ? 'ok' : '') + '">' +
        esc(REPORT_LABEL[r.type] || r.type) + '</span>' +
      '<span class="report-who">' + esc(r.identity_name || '?') + '</span>' +
      (r.role ? '<span class="chip role">@' + esc(r.role) + '</span>' : '') +
      (r.side_key ? '<span class="chip purple">side:' + esc(r.side_key) + '</span>' : '') +
      '<span class="report-time">' + fmtWhen(r.created_at) + '</span>' +
    '</div>' +
    (r.body ? '<div class="report-body md">' + md(r.body) + '</div>' : '') +
    segs + atts +
  '</div>';
}

function composerHTML(sides) {
  const sideOpts = sides.map(s =>
    '<option value="' + esc(s.key) + '">side: ' + esc(s.key) + '</option>').join('');
  return '<div class="section"><div class="section-head"><span>上报</span><span class="rule"></span></div>' +
    '<div class="composer">' +
      '<textarea id="rep-body" placeholder="进展 / 阻塞 / 决策 / 结果…（Cmd+Enter 提交）"></textarea>' +
      '<div class="composer-row">' +
        '<select id="rep-type">' +
          Object.keys(REPORT_LABEL).map(k =>
            '<option value="' + k + '"' + (k === 'progress' ? ' selected' : '') + '>' +
            REPORT_LABEL[k] + '</option>').join('') +
        '</select>' +
        (sideOpts ? '<select id="rep-side"><option value="">不属于任何 side</option>' + sideOpts + '</select>' : '') +
        '<input type="text" id="rep-mention" placeholder="@谁 或角色，逗号分隔">' +
        '<button class="btn primary" id="rep-send">提交上报</button>' +
      '</div>' +
      '<div class="composer-row">' +
        '<label class="btn tiny" style="cursor:pointer">📎 加附件<input type="file" id="rep-file" multiple hidden></label>' +
        '<span class="faint small" id="rep-files"></span>' +
      '</div>' +
    '</div></div>';
}

function wireTask(t, sides, segs) {
  const code = t.code;

  document.getElementById('task-status').onchange = async (e) => {
    try {
      await api('PATCH', '/tasks/' + code, { status: e.target.value });
      toast(code + ' → ' + TASK_STATUS[e.target.value]);
      route();
    } catch (err) { fail(err); }
  };

  document.getElementById('copy-pack').onclick = async () => {
    try {
      const text = await api('GET', '/tasks/' + code + '/pack?format=md', undefined, true);
      await copy(text, '开工包（可直接粘进 Claude Code）');
    } catch (e) { fail(e); }
  };
  document.getElementById('copy-pack-json').onclick = async () => {
    try {
      const text = await api('GET', '/tasks/' + code + '/pack?format=json', undefined, true);
      await copy(text, 'pack JSON');
    } catch (e) { fail(e); }
  };
  document.getElementById('show-plain').onclick = async () => {
    try {
      const text = await api('GET', '/tasks/' + code + '/pack?format=md', undefined, true);
      showPlain(text);
    } catch (e) { fail(e); }
  };
  document.getElementById('task-del').onclick = async () => {
    if (!confirm('删除 ' + code + '？这会连带删掉它所有工作面、分段和上报。')) return;
    try {
      await api('DELETE', '/tasks/' + code);
      toast('已删除 ' + code);
      history.pushState({}, '', '/');
      route();
    } catch (e) { fail(e); }
  };

  document.querySelectorAll('.seg').forEach(el => {
    const key = el.dataset.seg;
    const seg = segs.find(g => g.key === key);
    el.querySelector('.seg-copy').onclick = () => copy(seg.body || '', '[' + key + ']');
    el.querySelector('.seg-prompt').onclick = async () => {
      try {
        const text = await api('GET', '/tasks/' + code + '/segments/' + encodeURIComponent(key) + '?format=prompt', undefined, true);
        await copy(text, '[' + key + '] as prompt');
      } catch (e) { fail(e); }
    };
    el.querySelector('.seg-edit').onclick = () => editSegment(code, seg);
  });

  const addSeg = document.getElementById('seg-add');
  if (addSeg) addSeg.onclick = () => editSegment(code, null);

  document.querySelectorAll('.sides-table tr[data-side]').forEach(tr => {
    const key = tr.dataset.side;
    tr.querySelector('.side-status').onchange = async (e) => {
      try {
        await api('PATCH', '/tasks/' + code + '/sides/' + key, { status: e.target.value });
        toast('side ' + key + ' → ' + SIDE_STATUS[e.target.value]);
        route();
      } catch (err) { fail(err); }
    };
    const roleInput = tr.querySelector('.side-role');
    roleInput.onchange = async () => {
      try {
        await api('PATCH', '/tasks/' + code + '/sides/' + key, { assignee_role: roleInput.value.trim() });
        toast('side ' + key + ' → @' + roleInput.value.trim());
        route();
      } catch (err) { fail(err); roleInput.value = ''; }
    };
    tr.querySelector('.side-pack').onclick = async () => {
      try {
        const text = await api('GET', '/tasks/' + code + '/pack?side=' + encodeURIComponent(key) + '&format=md', undefined, true);
        await copy(text, 'side ' + key + ' 的开工包');
      } catch (e) { fail(e); }
    };
    tr.querySelector('.side-del').onclick = async () => {
      if (!confirm('删除工作面 ' + key + '？它的分段也会一起没。')) return;
      try {
        await api('DELETE', '/tasks/' + code + '/sides/' + key);
        toast('已删除 side ' + key);
        route();
      } catch (e) { fail(e); }
    };
  });

  const sideAdd = document.getElementById('side-add');
  if (sideAdd) sideAdd.onclick = async () => {
    const key = prompt('工作面 key（如 api / ui / review）');
    if (!key) return;
    const role = prompt('指派给哪个角色？（可留空，之后再说）', '') || '';
    try {
      await api('POST', '/tasks/' + code + '/sides', { key, assignee_role: role });
      toast('已加工作面 ' + key);
      route();
    } catch (e) { fail(e); }
  };

  const pendingFiles = [];
  const fileInput = document.getElementById('rep-file');
  fileInput.onchange = async () => {
    if (!fileInput.files.length) return;
    const fd = new FormData();
    for (const f of fileInput.files) fd.append('file', f);
    try {
      const resp = await fetch('/api/v1/files', {
        method: 'POST', body: fd, credentials: 'same-origin',
      });
      const data = await resp.json();
      if (!resp.ok) throw Object.assign(new Error(data.message), { api: data });
      for (const f of data.files) pendingFiles.push(f.id);
      document.getElementById('rep-files').textContent =
        '已上传 ' + pendingFiles.length + ' 个：' + data.files.map(f => f.name).join(', ');
    } catch (e) { fail(e); }
  };

  const send = async () => {
    const body = document.getElementById('rep-body').value.trim();
    const type = document.getElementById('rep-type').value;
    const sideEl = document.getElementById('rep-side');
    const mentions = document.getElementById('rep-mention').value
      .split(',').map(s => s.trim()).filter(Boolean);
    if (!body) { toast('写点什么再提交', true); return; }
    const payload = { type, body, mentions, attachments: pendingFiles };
    if (sideEl && sideEl.value) payload.side_key = sideEl.value;
    try {
      await api('POST', '/tasks/' + code + '/reports', payload);
      toast('已上报');
      route();
    } catch (e) { fail(e); }
  };
  document.getElementById('rep-send').onclick = send;
  document.getElementById('rep-body').addEventListener('keydown', (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') { e.preventDefault(); send(); }
  });

  document.querySelectorAll('[data-zoom]').forEach(img => {
    img.onclick = () => {
      const box = document.createElement('div');
      box.className = 'lightbox';
      box.innerHTML = '<img src="' + esc(img.dataset.zoom) + '">';
      box.onclick = () => box.remove();
      document.body.appendChild(box);
    };
  });
}

function editSegment(code, seg) {
  const key = seg ? seg.key : prompt('分段 key（固定：context/goal/deliverable/constraint/acceptance/interface/files；也可以自起）');
  if (!key) return;
  const title = seg ? (seg.title || key) : (prompt('分段标题', key) || key);
  const body = prompt('正文（Markdown）。留空则清空这一段。', seg ? (seg.body || '') : '');
  if (body === null) return;
  api('POST', '/tasks/' + code + '/segments', { key, title, body, side_key: seg ? (seg.side_key || '') : '' })
    .then(() => { toast('已保存 [' + key + ']'); route(); })
    .catch(fail);
}

function showPlain(text) {
  const w = document.createElement('div');
  w.className = 'lightbox';
  w.style.cursor = 'default';
  w.style.alignItems = 'start';
  w.style.padding = '5vh 4vw';
  const pre = document.createElement('pre');
  pre.style.cssText = 'white-space:pre-wrap;word-break:break-word;background:var(--bg-raised);' +
    'padding:22px;border-radius:12px;border:1px solid var(--line);max-width:900px;width:100%;' +
    'max-height:88vh;overflow:auto;font-size:12.5px;line-height:1.6;text-align:left';
  pre.textContent = text;
  w.appendChild(pre);
  w.onclick = (e) => { if (e.target === w) w.remove(); };
  document.body.appendChild(w);
}

// ----------------------------------------------------------------- inbox
async function renderInbox() {
  loading();
  let data;
  try { data = await api('GET', '/inbox?limit=80'); } catch (e) { return fail(e); }
  const items = data.items || [];
  let html = '<div class="section-head" style="margin-bottom:14px">' +
    '<span>收件箱</span><span class="chip">' + data.unread + ' 未读</span><span class="rule"></span>' +
    (data.unread ? '<button class="btn tiny" id="read-all">全部已读</button>' : '') +
    '</div>';
  if (!items.length) {
    html += '<div class="empty-state"><div class="big">📭</div>没有通知<div class="small" style="margin-top:6px">被 @ 或者你关注的任务有变化时会出现在这里</div></div>';
  } else {
    for (const n of items) {
      html += '<div class="inbox-item ' + (n.read_at ? 'read' : 'unread') + '" data-url="' + esc(n.url || '') + '">' +
        '<span class="inbox-dot"></span>' +
        '<div class="inbox-main">' +
          '<div class="inbox-title">' + esc(n.title) + '</div>' +
          '<div class="inbox-when">' + esc(n.kind) + ' · ' + fmtWhen(n.created_at) + '</div>' +
        '</div></div>';
    }
  }
  setView(html, true);
  document.querySelectorAll('.inbox-item').forEach(el => {
    el.onclick = () => {
      const url = el.dataset.url;
      if (!url) return;
      history.pushState({}, '', url);
      route();
    };
  });
  const ra = document.getElementById('read-all');
  if (ra) ra.onclick = async () => {
    try { await api('POST', '/inbox/read', { ids: [] }); toast('已全部标记为已读'); route(); }
    catch (e) { fail(e); }
  };
}

// ----------------------------------------------------------------- admin
async function renderAdmin() {
  loading();
  let ids, roles, hooks;
  try {
    [ids, roles, hooks] = await Promise.all([
      api('GET', '/identities'),
      api('GET', '/roles?holders=1'),
      api('GET', '/webhooks'),
    ]);
  } catch (e) { return fail(e); }

  let html = '<div class="admin-grid">';

  // identities
  html += '<div class="panel"><h3>身份（API key）</h3>';
  for (const id of ids.identities) {
    html += '<div class="row">' +
      '<div class="grow"><div class="name">' + esc(id.name) +
        (id.disabled ? ' <span class="chip danger">已停用</span>' : '') + '</div>' +
        '<div class="faint small">' + esc(id.kind) + ' · ' + esc(id.key_prefix || '') +
        ' · 角色 ' + esc((id.roles || []).join(', ')) + '</div></div>' +
      '<button class="btn tiny" data-rotate="' + esc(id.id) + '">轮换 key</button>' +
      (id.disabled
        ? '<button class="btn tiny" data-enable="' + esc(id.id) + '">启用</button>'
        : '<button class="btn tiny danger" data-disable="' + esc(id.id) + '">停用</button>') +
    '</div>';
  }
  html += '<div class="row"><button class="btn tiny primary" id="id-new">+ 新建身份</button></div>' +
    '<div id="id-key-slot"></div></div>';

  // roles
  html += '<div class="panel"><h3>角色</h3>';
  for (const r of roles.roles) {
    const holders = (roles.holders && roles.holders[r.key]) || [];
    html += '<div class="row">' +
      '<div class="grow"><div class="name">@' + esc(r.key) + ' <span class="faint">' + esc(r.name) + '</span></div>' +
      '<div class="faint small">' + esc(r.description || '') +
        (holders.length ? ' · 持有：' + esc(holders.join(', ')) : ' · 无人持有') + '</div></div>' +
      (r.builtin ? '<span class="chip">内置</span>'
        : '<button class="btn tiny danger" data-role-del="' + esc(r.key) + '">删除</button>') +
    '</div>';
  }
  html += '<div class="row"><button class="btn tiny" id="role-new">+ 自定义角色</button></div></div>';

  // webhooks
  html += '<div class="panel"><h3>Webhook 出口</h3>';
  if (!hooks.webhooks.length) {
    html += '<div class="faint small" style="padding:8px 0">还没有配置。上报和任务变更可以 POST 到任意地址（n8n / Slack / 自建）。</div>';
  }
  for (const h of hooks.webhooks) {
    html += '<div class="row">' +
      '<div class="grow"><div class="name" style="word-break:break-all">' + esc(h.url) + '</div>' +
      '<div class="faint small">事件 ' + esc((h.events || []).join(', ') || '全部') +
        ' · 上次 ' + (h.last_status ? h.last_status : '—') +
        (h.last_error ? ' · ' + esc(h.last_error.slice(0, 60)) : '') + '</div></div>' +
      '<button class="btn tiny danger" data-hook-del="' + esc(h.id) + '">删除</button>' +
    '</div>';
  }
  html += '<div class="row"><button class="btn tiny" id="hook-new">+ 加 webhook</button></div>' +
    '<div class="faint small" style="margin-top:8px">事件类型：' + esc((hooks.event_types || []).join(', ')) + '</div>' +
    '<div class="faint small">签名：<code>' + esc(hooks.signature || '') + '</code></div>' +
    '</div>';

  // server info
  html += '<div class="panel"><h3>这台服务端</h3>' +
    '<div class="row"><div class="grow"><div class="name">API 说明书</div>' +
    '<div class="faint small">给模型/agent 读的完整接口文档</div></div>' +
    '<a class="btn tiny" href="/api/v1/llms.txt" target="_blank">打开 llms.txt</a></div>' +
    '<div class="row"><div class="grow"><div class="name">机器可读 schema</div>' +
    '<div class="faint small">枚举、错误码、端点表（JSON）</div></div>' +
    '<a class="btn tiny" href="/api/v1/schema" target="_blank">打开 schema</a></div>' +
    '<div class="row"><div class="grow"><div class="name">事件流</div>' +
    '<div class="faint small">SSE：<code>/api/v1/stream</code>，轮询：<code>/api/v1/events?since=N</code></div></div></div>' +
    '</div>';

  html += '</div>';
  setView(html);

  const newKey = (title, key) => {
    document.getElementById('id-key-slot').innerHTML =
      '<div class="key-reveal"><strong>' + esc(title) + '</strong>' +
      '<code>' + esc(key) + '</code>' +
      '<div class="row" style="padding:0"><button class="btn tiny primary" id="copy-new-key">复制</button>' +
      '<span class="faint small">只显示这一次，离开页面就没了</span></div></div>';
    document.getElementById('copy-new-key').onclick = () => copy(key, 'API key');
  };

  document.querySelectorAll('[data-rotate]').forEach(b => b.onclick = async () => {
    if (!confirm('轮换后旧 key 立即失效，正在用它的会话会掉线。继续？')) return;
    try {
      const r = await api('POST', '/identities/' + b.dataset.rotate + '/rotate');
      newKey('新 API key', r.api_key);
    } catch (e) { fail(e); }
  });
  document.querySelectorAll('[data-disable]').forEach(b => b.onclick = async () => {
    try { await api('PATCH', '/identities/' + b.dataset.disable, { disabled: true }); route(); }
    catch (e) { fail(e); }
  });
  document.querySelectorAll('[data-enable]').forEach(b => b.onclick = async () => {
    try { await api('PATCH', '/identities/' + b.dataset.enable, { disabled: false }); route(); }
    catch (e) { fail(e); }
  });
  document.querySelectorAll('[data-role-del]').forEach(b => b.onclick = async () => {
    try { await api('DELETE', '/roles/' + b.dataset.roleDel); route(); }
    catch (e) { fail(e); }
  });
  document.querySelectorAll('[data-hook-del]').forEach(b => b.onclick = async () => {
    try { await api('DELETE', '/webhooks/' + b.dataset.hookDel); route(); }
    catch (e) { fail(e); }
  });

  document.getElementById('id-new').onclick = async () => {
    const name = prompt('新身份名（如 ci-runner、reviewer-bot）');
    if (!name) return;
    const roles = prompt('角色（逗号分隔）', 'member') || 'member';
    const kind = prompt('类型 human/agent', 'agent') || 'agent';
    try {
      const r = await api('POST', '/identities', {
        name, kind, roles: roles.split(',').map(s => s.trim()).filter(Boolean),
      });
      newKey('新身份 ' + name + ' 的 API key', r.api_key);
    } catch (e) { fail(e); }
  };
  document.getElementById('role-new').onclick = async () => {
    const key = prompt('角色 key（英文小写，如 data）');
    if (!key) return;
    const name = prompt('显示名', key) || key;
    const description = prompt('说明（它会接什么活）', '') || '';
    try { await api('POST', '/roles', { key, name, description }); route(); }
    catch (e) { fail(e); }
  };
  document.getElementById('hook-new').onclick = async () => {
    const url = prompt('POST 到哪个 URL？');
    if (!url) return;
    const secret = prompt('签名密钥（可留空）', '') || '';
    const events = prompt('订阅事件（逗号分隔，留空=全部）', '') || '';
    try {
      await api('POST', '/webhooks', {
        url, secret, events: events.split(',').map(s => s.trim()).filter(Boolean),
      });
      toast('已添加 webhook'); route();
    } catch (e) { fail(e); }
  };
}

// -------------------------------------------------------------- start up
boot();
