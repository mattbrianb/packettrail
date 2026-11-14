'use strict';

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const sleep = ms => new Promise(r => setTimeout(r, ms));
const saved = {
  get(k, d) { try { const v = localStorage.getItem('pt.' + k); return v == null ? d : JSON.parse(v); } catch { return d; } },
  set(k, v) { try { localStorage.setItem('pt.' + k, JSON.stringify(v)); } catch { /* storage unavailable */ } },
};

const state = {
  period: saved.get('period', 'session'),
  bits: saved.get('bits', true),
  theme: saved.get('theme', 'system'),
  // ?tab=system opens a tab directly (links and screenshots); otherwise the last one used
  tab: ['apps', 'ips', 'asns', 'peering', 'monitors', 'tools', 'system'].find(t => t === new URLSearchParams(location.search).get('tab')) || saved.get('tab', 'apps'),
  hidePrivate: saved.get('hidePrivate', false),
  app: null,
  appName: '',
  status: null,
  pendingExpand: null,
};

// ---------------------------------------------------------------- formatting

function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function scaled(n, units, base) {
  let i = 0;
  while (n >= base && i < units.length - 1) { n /= base; i++; }
  const s = i === 0 ? n.toFixed(0) : n < 10 ? n.toFixed(2) : n < 100 ? n.toFixed(1) : n.toFixed(0);
  return s + ' ' + units[i];
}
function fmtBytes(n) { return n ? scaled(n, ['B', 'kB', 'MB', 'GB', 'TB', 'PB'], 1000) : '0 B'; }
const fmtBytesCell = n => n ? fmtBytes(n) : '–';
// Rates arrive in bytes per second.
function fmtRateAlways(bps) {
  bps = bps || 0;
  return state.bits ? scaled(bps * 8, ['bps', 'kbps', 'Mbps', 'Gbps', 'Tbps'], 1000) : scaled(bps, ['B/s', 'kB/s', 'MB/s', 'GB/s'], 1000);
}
function fmtRate(bps) { return bps >= 0.5 ? fmtRateAlways(bps) : ''; }
const pad = n => String(n).padStart(2, '0');
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
function fmtTime(t) {
  if (!t) return '';
  const d = new Date(t * 1000), now = new Date();
  const hms = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  if (d.toDateString() === now.toDateString()) return hms;
  return `${pad(d.getDate())} ${MONTHS[d.getMonth()]} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
function isoTime(t) {
  if (!t) return '';
  const d = new Date(t * 1000);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}
function stamp() {
  const d = new Date();
  return `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`;
}
// Sort key that orders IPv4 numerically and IPv6 after IPv4.
function ipKey(ip) {
  if (ip.includes('.') && !ip.includes(':')) return '4' + ip.split('.').map(o => o.padStart(3, '0')).join('.');
  const parts = ip.split('::');
  const head = parts[0] ? parts[0].split(':') : [];
  const tail = parts.length > 1 && parts[1] ? parts[1].split(':') : [];
  const full = [...head, ...Array(Math.max(0, 8 - head.length - tail.length)).fill('0'), ...tail];
  return '6' + full.map(h => h.padStart(4, '0')).join(':');
}
// Country flag from an ISO code. Rendered with the embedded Twemoji flag
// font, because Windows has no colour flag emoji of its own.
function flag(cc) {
  if (!cc || !/^[A-Za-z]{2}$/.test(cc)) return '';
  const e = String.fromCodePoint(...[...cc.toUpperCase()].map(c => 0x1f1e6 + c.charCodeAt(0) - 65));
  return `<span class="flag" aria-hidden="true">${e}</span>`;
}
function place(r) {
  return [r.city, r.region, r.country].filter((v, i, a) => v && a.indexOf(v) === i).join(', ');
}

// Resolve a colour token (tokens use light-dark(), which only a real
// element can resolve) for the canvas charts.
let colorProbe;
function cssColor(name) {
  if (!colorProbe) {
    colorProbe = document.createElement('i');
    colorProbe.style.display = 'none';
    document.body.appendChild(colorProbe);
  }
  colorProbe.style.color = `var(${name})`;
  return getComputedStyle(colorProbe).color;
}
// Dark mode only: chart lines get a soft halo in their own colour so they read
// at a glance. Light mode stays flat.
function isDark() {
  return state.theme === 'dark' || (state.theme !== 'light' && matchMedia('(prefers-color-scheme: dark)').matches);
}
function strokeGlow(g, col, blur = 7) {
  if (isDark() && state.glow !== false) { g.shadowColor = withAlpha(col, 0.55); g.shadowBlur = blur; }
  g.stroke();
  g.shadowBlur = 0;
  g.shadowColor = 'rgba(0, 0, 0, 0)';
}
function withAlpha(rgb, a) {
  const m = rgb.match(/[\d.]+/g);
  return m ? `rgba(${m[0]}, ${m[1]}, ${m[2]}, ${a})` : rgb;
}
// App identity: the executable's own icon when the OS provides one, a
// lettered tile in a stable colour otherwise.
// Curated, deep tones (blue, cyan, teal, green, olive, amber, red, slate) that
// all keep white letters above 4.5:1 contrast.
const TILE_TONES = [[255, 0.1], [225, 0.09], [195, 0.08], [160, 0.09], [130, 0.08], [70, 0.09], [35, 0.1], [250, 0.02]];
function toneOf(s) {
  let h = 0;
  for (const c of String(s)) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  const [hue, chroma] = TILE_TONES[h % TILE_TONES.length];
  return `--h:${hue};--c:${chroma}`;
}
function initials(name) {
  const m = String(name || '').match(/[A-Za-z0-9]/);
  return m ? m[0].toUpperCase() : '?';
}
function appIcon(key, name, size, id) {
  return `<span class="appicon${size ? ' ' + size : ''}"${id ? ` id="${id}"` : ''}><span class="mono" style="${toneOf(key || name)}">${esc(initials(name))}</span>`
    + `<img src="/api/icon?app=${encodeURIComponent(key)}" alt="" loading="lazy" decoding="async" onload="this.parentNode.classList.add('has-img')" onerror="this.remove()"></span>`;
}
function appCell(key, name) {
  return `<span class="appcell">${appIcon(key, name)}<span class="nm">${esc(name)}</span></span>`;
}

// ---------------------------------------------------------------- API

async function api(path, opts) {
  const r = await fetch(path, opts);
  if (!r.ok) {
    let msg = r.statusText;
    try { msg = (await r.json()).error || msg; } catch { /* not JSON */ }
    throw new Error(msg);
  }
  return r.json();
}
const post = (path, body, type = 'text/plain') => api(path, { method: 'POST', headers: { 'X-PacketTrail': '1', 'Content-Type': type }, body });
function openLink(url) { post('/api/open?url=' + encodeURIComponent(url)).catch(e => toast('Could not open link: ' + e.message)); }

let toastTimer;
function toast(text, actionLabel, action) {
  const el = $('#toast');
  $('#toastText').textContent = text;
  const b = $('#toastAction');
  b.hidden = !actionLabel;
  b.textContent = actionLabel || '';
  b.onclick = () => { action && action(); el.classList.remove('show'); };
  el.classList.toggle('ok', /^(Copied|Saved)/.test(text));
  el.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.remove('show'), actionLabel ? 9000 : 2600);
}

// ---------------------------------------------------------------- clipboard

const ICON_COPY = '<svg viewBox="0 0 16 16" width="13" height="13" aria-hidden="true"><rect x="5.5" y="5.5" width="8" height="8" rx="1.6" fill="none" stroke="currentColor" stroke-width="1.4"/><path d="M10.5 3.2V3a1.5 1.5 0 0 0-1.5-1.5H4A1.5 1.5 0 0 0 2.5 3v5A1.5 1.5 0 0 0 4 9.5h.3" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg>';
const ICON_CHECK = '<svg viewBox="0 0 16 16" width="13" height="13" aria-hidden="true"><path d="M3.5 8.5 6.5 11.5 12.5 4.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>';
const CHEVRON = '<svg class="chev-ic" viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="M6 3.5 10.5 8 6 12.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>';

function copyBtn(value, label) {
  const l = esc(label || 'Copy ' + value);
  return `<button type="button" class="copy" data-copy="${esc(value)}" title="${l}" aria-label="${l}"><span class="ic ic-copy">${ICON_COPY}</span><span class="ic ic-done">${ICON_CHECK}</span></button>`;
}
async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.cssText = 'position:fixed;opacity:0;pointer-events:none';
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try { ok = document.execCommand('copy'); } catch { /* unsupported */ }
    ta.remove();
    return ok;
  }
}
function flashDone(btn) {
  btn.classList.add('done');
  clearTimeout(btn._t);
  btn._t = setTimeout(() => btn.classList.remove('done'), 1400);
}

// ---------------------------------------------------------------- tables

const allTables = [];

class Table {
  constructor(id, cols, opts = {}) {
    Object.assign(this, { id, cols, opts });
    this.el = $('#t-' + id);
    this.wrap = $('#wrap-' + id);
    this.rows = [];
    this.filter = '';
    this.limit = 500;
    this.ents = new Map();
    this.expanded = null;
    this.headSig = '';
    this.pending = false;
    this.holding = false;
    const defSort = opts.sort || { key: (cols.find(c => c.num) || cols[0]).key, dir: -1 };
    this.sort = saved.get('sort.' + id, defSort);
    if (!cols.some(c => c.key === this.sort.key && !c.hidden && !c.nosort)) this.sort = defSort;
    this.el.innerHTML = '<thead></thead><tbody></tbody>';
    this.thead = this.el.tHead;
    this.tbody = this.el.tBodies[0];
    this.el.addEventListener('click', e => this.onClick(e));
    this.el.addEventListener('keydown', e => this.onKey(e));
    this.el.addEventListener('pointerdown', e => { if (e.button === 0 && this.tbody.contains(e.target)) this.holding = true; });
    new ResizeObserver(() => this.wrap.style.setProperty('--vw', this.wrap.clientWidth + 'px')).observe(this.wrap);
    if (this.sheetMode()) this.el.classList.add('sheet-rows');
    allTables.push(this);
  }
  // Details open in the right-hand inspector instead of under the row.
  sheetMode() { return !!(this.opts.expand && this.opts.expand.sheet); }
  visible() { return this.cols.filter(c => !c.hidden); }
  key(r) { return this.opts.rowKey ? this.opts.rowKey(r) : JSON.stringify(r); }
  setRows(rows) { this.rows = rows || []; this.render(); }
  value(c, r) { return c.sortVal ? c.sortVal(r) : r[c.key]; }
  filtered() {
    let rows = this.rows;
    if (this.opts.prefilter) rows = rows.filter(this.opts.prefilter);
    const terms = this.filter.trim().toLowerCase().split(/\s+/).filter(Boolean);
    if (terms.length) {
      rows = rows.filter(r => {
        const hay = this.cols.map(c => (c.text ? c.text(r) : r[c.key]) ?? '').join(' ').toLowerCase();
        return terms.every(t => hay.includes(t));
      });
    }
    const col = this.cols.find(c => c.key === this.sort.key) || this.cols[0];
    const dir = this.sort.dir;
    const empty = v => v === undefined || v === null || v === '';
    return rows.slice().sort((a, b) => {
      const x = this.value(col, a), y = this.value(col, b);
      if (empty(x) || empty(y)) return empty(x) === empty(y) ? 0 : empty(x) ? 1 : -1; // blanks last
      if (typeof x === 'number' && typeof y === 'number') return (x - y) * dir;
      return String(x).localeCompare(String(y), undefined, { numeric: true, sensitivity: 'base' }) * dir;
    });
  }
  // While the user is pressing in the table or has text selected in it, the
  // DOM is left alone so the selection survives the live refresh.
  frozen() {
    if (this.holding) return true;
    const s = window.getSelection();
    if (!s || s.isCollapsed || !s.rangeCount) return false;
    return this.el.contains(s.getRangeAt(0).commonAncestorContainer);
  }
  cellHtml(c, r) { return c.html ? c.html(r) : esc(c.fmt ? c.fmt(r[c.key], r) : r[c.key]); }
  makeEnt(k) {
    const tr = document.createElement('tr');
    tr.dataset.key = k;
    if (this.opts.onSelect || this.opts.expand) {
      tr.className = 'clickable';
      tr.tabIndex = 0;
    }
    if (this.opts.expand) tr.setAttribute('aria-expanded', 'false');
    for (const c of this.visible()) {
      const td = tr.insertCell();
      td.className = ((c.cls || '') + (c.num ? ' num' : '')).trim();
    }
    return { tr, html: [], tips: [], sel: false, xr: null, row: null };
  }
  render(force) {
    const rows = this.filtered();
    this.pending = !force && this.frozen();
    this.updateCount(rows);
    if (this.pending) return;
    const cols = this.visible();
    const arrow = `<span class="arrow">${this.sort.dir > 0 ? '▲' : '▼'}</span>`;
    const head = '<tr>' + cols.map(c => {
      const sorted = this.sort.key === c.key;
      const cls = [c.num ? 'num' : '', sorted ? 'sorted' : '', c.nosort ? 'nosort' : '', c.cls && c.cls.includes('chev') ? 'chev' : ''].join(' ').trim();
      const title = c.nosort ? '' : esc(c.title || 'Sort by ' + c.label);
      return `<th class="${cls}" data-key="${c.key}" title="${title}">${sorted && c.num ? arrow : ''}${esc(c.label)}${sorted && !c.num ? arrow : ''}</th>`;
    }).join('') + '</tr>';
    if (head !== this.headSig) {
      this.thead.innerHTML = head;
      this.headSig = head;
    }
    const seen = new Set();
    let cursor = this.tbody.firstChild;
    const skipClosing = () => { while (cursor && cursor.classList.contains('closing')) cursor = cursor.nextSibling; };
    for (const r of rows.slice(0, this.limit)) {
      const k = this.key(r);
      if (seen.has(k)) continue;
      seen.add(k);
      let ent = this.ents.get(k);
      if (!ent) { ent = this.makeEnt(k); this.ents.set(k, ent); }
      ent.row = r;
      cols.forEach((c, i) => {
        const h = this.cellHtml(c, r);
        if (ent.html[i] !== h) { ent.tr.cells[i].innerHTML = h; ent.html[i] = h; }
        if (c.tip) {
          const t = c.tip(r) || '';
          if (ent.tips[i] !== t) { ent.tr.cells[i].title = t; ent.tips[i] = t; }
        }
      });
      const sel = !!(this.opts.selected && this.opts.selected(r));
      if (sel !== ent.sel) { ent.tr.classList.toggle('sel', sel); ent.sel = sel; }
      skipClosing();
      if (cursor === ent.tr) cursor = cursor.nextSibling;
      else this.tbody.insertBefore(ent.tr, cursor);
      if (ent.xr) {
        skipClosing();
        if (cursor === ent.xr) cursor = cursor.nextSibling;
        else this.tbody.insertBefore(ent.xr, cursor);
      }
    }
    for (const [k, ent] of this.ents) {
      if (seen.has(k)) continue;
      ent.tr.remove();
      if (ent.xr) { stopDiag(ent.xr); ent.xr.remove(); }
      this.ents.delete(k);
      if (this.expanded === k) this.expanded = null;
    }
  }
  updateCount(rows) {
    const c = $('#count-' + this.id);
    if (!c) return;
    let count = `${rows.length.toLocaleString()} ${rows.length === 1 ? 'row' : 'rows'}`;
    if (rows.length !== this.rows.length) count += ` of ${this.rows.length.toLocaleString()}`;
    if (rows.length > this.limit) count += ` · first ${this.limit} shown; export includes all`;
    if (this.pending) count += ' · updates paused while text is selected';
    c.textContent = count;
  }
  onClick(e) {
    const th = e.target.closest('th');
    if (th && this.thead.contains(th)) {
      const col = this.cols.find(c => c.key === th.dataset.key);
      if (!col || col.nosort) return;
      if (this.sort.key === col.key) this.sort.dir *= -1;
      else this.sort = { key: col.key, dir: col.num ? -1 : 1 };
      saved.set('sort.' + this.id, this.sort);
      this.render(true);
      return;
    }
    if (e.target.closest('button, a, input, .xrow')) return;
    const tr = e.target.closest('tbody tr[data-key]');
    if (!tr || e.detail > 1) return; // a double-click is for selecting text
    const s = window.getSelection();
    if (s && !s.isCollapsed && tr.contains(s.anchorNode)) return; // the user was selecting text
    this.activate(tr.dataset.key);
  }
  onKey(e) {
    if ((e.key === 'Enter' || e.key === ' ') && e.target.matches('tbody tr[data-key]')) {
      e.preventDefault();
      this.activate(e.target.dataset.key);
    }
  }
  activate(k) {
    const ent = this.ents.get(k);
    if (!ent) return;
    if (this.opts.onSelect) this.opts.onSelect(ent.row);
    else if (this.opts.expand) this.toggle(k);
  }
  async toggle(k) {
    if (this.sheetMode()) return this.toggleSheet(k);
    if (this.expanded === k) return this.collapse();
    this.collapse();
    const ent = this.ents.get(k);
    if (!ent) return;
    this.expanded = k;
    ent.tr.classList.add('open');
    ent.tr.setAttribute('aria-expanded', 'true');
    const xr = document.createElement('tr');
    xr.className = 'xrow';
    const td = xr.insertCell();
    td.colSpan = this.visible().length;
    td.innerHTML = '<div class="xwrap"><div class="xclip"><div class="xpanel"><div class="xloading">Loading…</div></div></div></div>';
    ent.xr = xr;
    // Give the (local, fast) request a moment so the panel opens at its
    // final height instead of growing after it lands.
    await Promise.race([this.loadPanel(ent.row, xr.querySelector('.xpanel')), sleep(160)]);
    if (this.expanded !== k || ent.xr !== xr) return;
    ent.tr.after(xr);
    xr.getBoundingClientRect(); // commit the collapsed state before opening
    xr.classList.add('open');
    xr.querySelectorAll('.seg').forEach(seg => { placeThumb(seg); setTimeout(() => { placeThumb(seg); seg.classList.add('ready'); }, 60); });
  }
  // Inspector version of toggle: the row is marked open and its details fill
  // the panel on the right. Clicking another row swaps the content in place;
  // clicking the open row again closes the panel.
  async toggleSheet(k) {
    if (this.expanded === k) return this.collapse();
    const ent = this.ents.get(k);
    if (!ent) return;
    this.markOpen(this.expanded, false);
    this.expanded = k;
    this.markOpen(k, true);
    const panel = document.createElement('div');
    panel.className = 'xpanel in-sheet';
    panel.innerHTML = '<div class="xloading">Loading…</div>';
    this.sheetPanel = panel;
    // Give the local request a moment so the panel opens with its content.
    await Promise.race([this.loadPanel(ent.row, panel), sleep(160)]);
    if (this.expanded !== k || this.sheetPanel !== panel) { if (panel._diag) panel._diag.stop(true); return; }
    inspector.show(this, panel);
  }
  markOpen(k, on) {
    const ent = k != null && this.ents.get(k);
    if (!ent) return;
    ent.tr.classList.toggle('open', on);
    ent.tr.setAttribute('aria-expanded', String(on));
  }
  collapse() {
    if (this.sheetMode()) {
      this.markOpen(this.expanded, false);
      this.expanded = null;
      this.sheetPanel = null;
      inspector.close(this);
      return;
    }
    const k = this.expanded;
    if (k == null) return;
    this.expanded = null;
    const ent = this.ents.get(k);
    if (!ent) return;
    ent.tr.classList.remove('open');
    ent.tr.setAttribute('aria-expanded', 'false');
    const xr = ent.xr;
    ent.xr = null;
    if (xr) stopDiag(xr);
    if (!xr || !xr.isConnected) return;
    xr.classList.add('closing');
    xr.classList.remove('open');
    const done = () => xr.remove();
    xr.addEventListener('transitionend', e => { if (e.target.classList.contains('xwrap')) done(); });
    setTimeout(done, 400);
  }
  async loadPanel(row, panel) {
    try {
      const ex = this.opts.expand;
      const d = ex.url ? await api(ex.url(row)) : row;
      this.fillPanel(panel, d, row);
    } catch (e) {
      panel.innerHTML = `<div class="xloading">Could not load details: ${esc(e.message)}</div>`;
      panel._built = false;
    }
  }
  fillPanel(panel, d, row) {
    const ex = this.opts.expand;
    if (!panel._built) {
      panel.innerHTML = ex.skeleton;
      panel._built = true;
      if (ex.init) ex.init(panel, d, row);
    }
    const out = ex.render(d);
    for (const [name, html] of Object.entries(out.sections)) {
      const el = panel.querySelector(`[data-sec="${name}"]`);
      if (el && el._h !== html) { el.innerHTML = html; el._h = html; }
    }
    panel._text = out.text;
  }
  refreshExpanded() {
    if (this.expanded == null || this.frozen()) return Promise.resolve();
    const ent = this.ents.get(this.expanded);
    if (this.sheetMode()) {
      const p = this.sheetPanel;
      if (!ent || !p || !p.isConnected || selectionIn(p)) return Promise.resolve();
      return this.loadPanel(ent.row, p);
    }
    if (!ent || !ent.xr) return Promise.resolve();
    return this.loadPanel(ent.row, ent.xr.querySelector('.xpanel'));
  }
  expandKey(k) {
    if (this.ents.has(k) && this.expanded !== k) {
      this.toggle(k);
      const ent = this.ents.get(k);
      ent.tr.scrollIntoView({ block: 'nearest' });
    }
  }
  csv(sep) {
    const cols = this.cols.filter(c => c.csv !== false);
    const q = v => {
      if (typeof v === 'number') return String(v);
      let s = String(v ?? '');
      if (/^[=+\-@\t\r]/.test(s)) s = "'" + s; // keep spreadsheets from evaluating text as formulas
      return /[",;\r\n]/.test(s) || s.includes(sep) ? '"' + s.replace(/"/g, '""') + '"' : s;
    };
    const lines = [cols.map(c => q(c.csvLabel || c.label)).join(sep)];
    for (const r of this.filtered()) lines.push(cols.map(c => q(c.csv ? c.csv(r) : r[c.key])).join(sep));
    return lines.join('\r\n') + '\r\n';
  }
}

window.addEventListener('pointerup', () => {
  for (const t of allTables) {
    if (!t.holding) continue;
    t.holding = false;
    if (t.pending) setTimeout(() => t.render(), 0);
  }
});
document.addEventListener('selectionchange', () => {
  for (const t of allTables) if (t.pending && !t.frozen()) t.render();
});

// ---------------------------------------------------------------- columns

function spark(values) {
  if (!values || !values.length) return '<svg class="spark" width="64" height="18"></svg>';
  const max = Math.max(...values, 1);
  const w = 64, h = 18, n = values.length;
  const pts = values.map((v, i) => `${(i * (w - 1) / (n - 1)).toFixed(1)},${(h - 1 - (v / max) * (h - 3)).toFixed(1)}`).join(' ');
  return `<svg class="spark" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}"><polyline points="${pts}" fill="none" stroke="var(--accent)" stroke-width="1.3" stroke-linejoin="round"/></svg>`;
}
function countryHtml(r) {
  if (r.scope) return `<span class="tag">${esc(r.scope)}</span>`;
  if (!r.country) return '';
  return `${flag(r.cc)}<span>${esc(r.country)}</span>`;
}
const countryText = r => r.scope ? r.scope : `${r.cc || ''} ${r.country || ''}`;
const countrySort = r => r.scope ? '~' + r.scope : (r.country || '');
const asnText = r => r.asn ? 'AS' + r.asn : '';
function ipCell(r) {
  return `<span class="ipv">${esc(r.ip)}</span>${copyBtn(r.ip, 'Copy IP address')}`;
}
const chevCol = () => ({ key: '_x', label: '', nosort: true, csv: false, cls: 'chev', html: () => CHEVRON });
const rateCols = () => [
  // Download + upload together, first: sort by it to catch whatever is using
  // the most bandwidth right now (the table re-sorts as rates change).
  { key: 'rateSum', label: 'Total rate', num: true, fmt: (v, r) => fmtRate((r.rateIn || 0) + (r.rateOut || 0)), sortVal: r => (r.rateIn || 0) + (r.rateOut || 0), cls: 'rate-sum', title: 'Current download + upload rate combined. Click to sort: whatever is using the most bandwidth right now goes to the top.', csvLabel: 'Total rate (bytes/s)', csv: r => Math.round((r.rateIn || 0) + (r.rateOut || 0)) },
  { key: 'rateIn', label: '↓ Rate', num: true, fmt: fmtRate, cls: 'rate-rx', title: 'Current download rate', csvLabel: 'Down rate (bytes/s)', csv: r => Math.round(r.rateIn) },
  { key: 'rateOut', label: '↑ Rate', num: true, fmt: fmtRate, cls: 'rate-tx', title: 'Current upload rate', csvLabel: 'Up rate (bytes/s)', csv: r => Math.round(r.rateOut) },
];
const byteCols = () => [
  { key: 'in', label: 'Downloaded', num: true, fmt: fmtBytesCell, csvLabel: 'Bytes down' },
  { key: 'out', label: 'Uploaded', num: true, fmt: fmtBytesCell, csvLabel: 'Bytes up' },
  { key: 'total', label: 'Total', num: true, fmt: fmtBytesCell, csvLabel: 'Bytes total' },
];
const timeCols = () => [
  { key: 'first', label: 'First seen', num: true, fmt: fmtTime, tip: r => isoTime(r.first), csv: r => isoTime(r.first) },
  { key: 'last', label: 'Last seen', num: true, fmt: fmtTime, tip: r => isoTime(r.last), csv: r => isoTime(r.last) },
];
// Passive path quality from the OS TCP stack: average smoothed RTT and the
// share of sent bytes TCP had to retransmit (a loss signal).
function fmtRTT(v) {
  if (v === undefined || v === null || v < 0) return '–';
  return v < 1 ? '<1 ms' : v < 100 ? v.toFixed(1) + ' ms' : Math.round(v) + ' ms';
}
function retransHtml(r) {
  if (!r.retrans) return '<span class="dim">–</span>';
  const p = r.retransPct;
  const cls = p >= 5 ? 'bad' : p >= 1 ? 'warn' : '';
  return `<span class="q ${cls}" title="${esc(fmtBytes(r.retrans))} retransmitted">${p < 0.01 ? '<0.01' : p.toFixed(2)}%</span>`;
}
function lossHtml(r) {
  const p = r.lossInPct;
  if (p === undefined || p === null || p < 0) return '<span class="dim">–</span>';
  const cls = p >= 2 ? 'bad' : p >= 0.5 ? 'warn' : '';
  return `<span class="q ${cls}">${p === 0 ? '0' : p < 0.01 ? '<0.01' : p.toFixed(2)}%</span>`;
}
const qualityCols = () => [
  { key: 'rtt', label: 'RTT', num: true, fmt: fmtRTT, sortVal: r => (r.rtt >= 0 ? r.rtt : ''), title: 'Average TCP round-trip time, measured passively by the OS', csvLabel: 'Avg RTT (ms)', csv: r => (r.rtt >= 0 ? r.rtt : '') },
  { key: 'lossInPct', label: 'Loss ↓', num: true, html: lossHtml, sortVal: r => (r.lossInPct >= 0 ? r.lossInPct : ''), title: 'Download side: gaps your PC detected in data it received (loss or reordering on the way to you). Windows, admin mode.', csvLabel: 'Download loss estimate (%)', csv: r => (r.lossInPct >= 0 ? r.lossInPct : '') },
  { key: 'retransPct', label: 'Retrans ↑', num: true, html: retransHtml, sortVal: r => r.retransPct || 0, title: 'Upload side: share of bytes your PC sent that TCP had to retransmit', csvLabel: 'Retransmitted (% of sent)', csv: r => r.retransPct || 0 },
  { key: 'retrans', label: 'Retransmitted bytes', hidden: true, csvLabel: 'Bytes retransmitted' },
];
const geoCols = () => [
  { key: 'country', label: 'Country', cls: 'country', html: countryHtml, text: countryText, sortVal: countrySort, csv: r => r.scope ? '(' + r.scope + ')' : r.country },
  { key: 'cc', label: 'CC', hidden: true },
  { key: 'city', label: 'City', cls: 'trunc', tip: r => r.region ? `${r.city}, ${r.region}` : r.city },
  { key: 'region', label: 'Region', hidden: true },
  { key: 'asn', label: 'ASN', cls: 'mono-sm', fmt: (v, r) => asnText(r), text: asnText, sortVal: r => r.asn || '', csv: r => r.asn || '' },
  { key: 'asOrg', label: 'AS organisation', cls: 'trunc', tip: r => r.asOrg },
  { key: 'network', label: 'Network', hidden: true },
];

// ---------------------------------------------------------------- detail panels

const IP_SKELETON = `
  <div class="xgrid">
    <div class="xtop" data-sec="head"></div>
    <div class="xkpis" data-sec="stats"></div>
    <div class="xcol" data-sec="facts"></div>
    <div class="xcol"><div class="xtitle">Applications talking to this IP</div><div data-sec="apps"></div></div>
    <div class="xdiag"></div>
  </div>`;

function kpi(label, value, rate, cls) {
  return `<div class="kpi"><span class="lbl">${label}</span><span class="val">${value}</span>${rate !== undefined ? `<span class="rate ${cls || ''}">${rate || '&nbsp;'}</span>` : ''}</div>`;
}

function renderIP(d) {
  const where = place(d);
  const loc = d.scope
    ? `<span class="tag">${esc(d.scope)}</span><span class="muted">address, not looked up</span>`
    : where ? `${flag(d.cc)}${esc(where)}` : '<span class="muted">Unknown location</span>';
  const org = d.asn ? `<span class="dot">·</span><span>AS${d.asn} ${esc(d.asOrg)}</span>` : '';
  const head = `
    <div class="xid">
      <div class="xip"><span class="mono">${esc(d.ip)}</span>${copyBtn(d.ip, 'Copy IP address')}</div>
      <div class="xsub">${loc}${org}</div>
    </div>
    <button type="button" class="btn sm" data-copy-details>${ICON_COPY}<span>Copy details</span></button>`;
  const stats = kpi('Downloaded', fmtBytes(d.in), fmtRate(d.rateIn), 'rx') + kpi('Uploaded', fmtBytes(d.out), fmtRate(d.rateOut), 'tx')
    + kpi('Avg RTT', fmtRTT(d.rtt)) + kpi('Loss ↓ (est.)', d.lossInPct >= 0 ? `${d.lossInPct.toFixed(2)}%` : '–', d.lossInPct >= 0 ? 'download side' : '', d.lossInPct >= 2 ? 'bad' : d.lossInPct >= 0.5 ? 'warn' : '') + kpi('Retrans ↑', d.retrans ? `${d.retransPct.toFixed(2)}%` : '–', d.retrans ? fmtBytes(d.retrans) : '', d.retransPct >= 5 ? 'bad' : d.retransPct >= 1 ? 'warn' : '')
    + kpi('First seen', fmtTime(d.first)) + kpi('Last seen', fmtTime(d.last));
  const none = t => `<span class="muted">${t}</span>`;
  const hosts = d.hosts && d.hosts.length
    ? `<div class="chips">${d.hosts.map(h => `<span class="chip"><span>${esc(h)}</span>${copyBtn(h)}</span>`).join('')}</div>`
    : none(d.scope ? 'none seen' : 'none seen; the app may use its own DNS (DoH)');
  const facts = [
    ['Hostnames (DNS)', hosts],
    ['Reverse DNS', d.ptr ? `<span class="mono">${esc(d.ptr)}</span>${copyBtn(d.ptr)}` : none(d.scope ? 'not looked up' : 'no PTR record')],
    ['Network', d.network ? `<span class="mono">${esc(d.network)}</span>${copyBtn(d.network)}` : none('–')],
    ['ASN', d.asn ? `<span class="mono">AS${d.asn}</span>${copyBtn('AS' + d.asn)}<span class="muted">${esc(d.asOrg)}</span>` : none('–')],
    ['Location', d.scope ? none('–') : where ? `${flag(d.cc)}${esc(where)}${d.cc ? ` <span class="muted">(${esc(d.cc)})</span>` : ''}` : none('unknown')],
  ];
  const factsHtml = '<dl class="xfacts">' + facts.map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join('') + '</dl>';
  const apps = d.apps.length ? `<table class="mini"><thead><tr><th>Application</th><th class="num">Port</th><th class="num">↓ Rate</th><th class="num">↑ Rate</th><th class="num">Down</th><th class="num">Up</th><th class="num">Last seen</th></tr></thead><tbody>${
    d.apps.map(a => `<tr><td class="trunc" title="${esc(a.name)}">${appCell(a.key, a.name)}</td><td class="num dim">${a.proto} ${a.dir === 'in' ? '←' : '→'} ${a.port}</td><td class="num rate-rx">${fmtRate(a.rateIn)}</td><td class="num rate-tx">${fmtRate(a.rateOut)}</td><td class="num">${fmtBytesCell(a.in)}</td><td class="num">${fmtBytesCell(a.out)}</td><td class="num" title="${isoTime(a.last)}">${fmtTime(a.last)}</td></tr>`).join('')
  }</tbody></table>` : none('No traffic in this period');
  const text = [
    `IP: ${d.ip}`,
    `Hostnames (DNS): ${(d.hosts || []).join(', ') || '-'}`,
    `Reverse DNS: ${d.ptr || '-'}`,
    `Location: ${d.scope ? d.scope + ' address' : (where || '-') + (d.cc ? ` (${d.cc})` : '')}`,
    `ASN: ${d.asn ? `AS${d.asn} ${d.asOrg}` : '-'}`,
    `Network: ${d.network || '-'}`,
    `Path quality: avg RTT ${fmtRTT(d.rtt)}, download loss ${d.lossInPct >= 0 ? d.lossInPct.toFixed(2) + '%' : 'n/a'}, retransmitted ${d.retrans ? d.retransPct.toFixed(2) + '% (' + fmtBytes(d.retrans) + ')' : 'none'}`,
    `Traffic (${periodLabel().toLowerCase()}): down ${fmtBytes(d.in)}, up ${fmtBytes(d.out)}, first seen ${isoTime(d.first)}, last seen ${isoTime(d.last)}`,
    `Applications: ${d.apps.map(a => `${a.name} ${a.proto}/${a.port} (${fmtBytes(a.total)})`).join('; ') || '-'}`,
  ].join('\r\n');
  return { sections: { head, stats, facts: factsHtml, apps }, text };
}

const ASN_SKELETON = `
  <div class="xgrid stack">
    <div class="xtop" data-sec="head"></div>
    <div class="xcol"><div class="xtitle">Prefixes carrying your traffic</div><div data-sec="prefixes"></div></div>
    <div class="xcol"><div class="xtitle">Busiest IPs <span class="muted">· click one to inspect it</span></div><div data-sec="ips"></div></div>
  </div>`;

function renderASN(d) {
  const title = d.asn ? `<span class="mono">AS${d.asn}</span>${copyBtn('AS' + d.asn)}<span class="org">${esc(d.org)}</span>` : `<span class="org">${esc(d.org || d.key)}</span>`;
  const head = `<div class="xid"><div class="xip">${title}</div><div class="xsub">${d.ips.length} IP${d.ips.length === 1 ? '' : 's'} · ${d.prefixes.length} prefix${d.prefixes.length === 1 ? '' : 'es'} in this period</div></div>
    <button type="button" class="btn sm" data-copy-details>${ICON_COPY}<span>Copy details</span></button>`;
  const prefixes = d.prefixes.length ? `<table class="mini"><thead><tr><th>Network</th><th>Location</th><th class="num">IPs</th><th class="num">RTT</th><th class="num">Retrans</th><th class="num">Down</th><th class="num">Up</th></tr></thead><tbody>${
    d.prefixes.slice(0, 40).map(p => `<tr><td class="mono-sm">${esc(p.network)}${p.network.includes('/') ? copyBtn(p.network) : ''}</td><td class="trunc">${flag(p.cc)}${esc(p.city || p.cc || '')}</td><td class="num">${p.ips}</td><td class="num">${fmtRTT(p.rtt)}</td><td class="num">${retransHtml(p)}</td><td class="num">${fmtBytesCell(p.in)}</td><td class="num">${fmtBytesCell(p.out)}</td></tr>`).join('')
  }</tbody></table>` : '<span class="muted">No prefixes</span>';
  const shown = d.ips.slice(0, 14);
  const ips = shown.length ? `<table class="mini"><thead><tr><th>IP</th><th>Hostname</th><th>Location</th><th class="num">Total</th></tr></thead><tbody>${
    shown.map(i => `<tr class="goto" data-goto-ip="${esc(i.ip)}" title="Show ${esc(i.ip)} in Remote IPs"><td class="mono-sm">${esc(i.ip)}</td><td class="trunc">${esc(i.host || i.ptr || '')}</td><td class="trunc">${flag(i.cc)}${esc(i.city || i.country || i.scope || '')}</td><td class="num">${fmtBytesCell(i.total)}</td></tr>`).join('')
  }</tbody></table>${d.ips.length > shown.length ? `<div class="more">+${d.ips.length - shown.length} more in the Remote IPs tab (filter by AS${d.asn})</div>` : ''}` : '<span class="muted">No IPs</span>';
  const text = [
    d.asn ? `AS${d.asn} ${d.org}` : d.org,
    'Prefixes: ' + d.prefixes.map(p => `${p.network} (${p.ips} IPs, ${fmtBytes(p.total)})`).join('; '),
    'IPs: ' + d.ips.map(i => `${i.ip}${i.host ? ' ' + i.host : ''} (${fmtBytes(i.total)})`).join('; '),
  ].join('\r\n');
  return { sections: { head, prefixes, ips }, text };
}

// ---------------------------------------------------------------- path visualisation

const trimTo = (s, n) => (s.length > n ? s.slice(0, Math.max(1, n - 1)) + '…' : s);
// Exchange peering LANs are often not announced, so they have no ASN; the
// reverse DNS usually gives them away.
const isIX = h => /(^|[.-])(ixp?|napafrica|dinx|jinx|cinx|linx|decix|de-cix|ams-ix|equinix-ix)([.-]|$)/i.test(h.host || '') || /exchange|napafrica/i.test(h.asOrg || '');
// A private address is "your LAN" only until the path first leaves it. Past
// that point the server marks the hop zone "provider": a provider's router
// answering from a private address, with inAsn set when the public hops on
// both sides of it are in one network.
const PROVIDER_PRIVATE = 'Routers that answer from private addresses after the path has left your LAN. They are inside a provider\'s network; the addresses don\'t say whose.';
// lastNet is the last public hop before this one on its path (route map only).
function hopKind(h, own, lastNet) {
  if (!h.addr) return 'none';
  if (h.scope) {
    if (h.zone !== 'provider') return 'local';
    const asn = h.inAsn || (lastNet && lastNet.asn);
    return own && own.has(asn) ? 'own' : 'net';
  }
  if (own && own.has(h.asn)) return 'own';
  if (isIX(h)) return 'ix';
  return 'net';
}
function segKey(h) {
  if (!h.addr) return null;
  if (h.inAsn) return 'as:' + h.inAsn;
  if (h.zone === 'provider') return 'provider';
  if (h.scope) return 'scope:' + h.scope;
  if (!h.asn && isIX(h)) return 'ix';
  return h.asn ? 'as:' + h.asn : 'unknown';
}
function segLabel(h) {
  if (h.inAsn) return `AS${h.inAsn} ${h.inOrg || ''}`;
  if (h.zone === 'provider') return 'Private IPs';
  if (h.scope === 'private') return 'Your LAN';
  if (h.scope === 'cgnat') return 'Carrier NAT';
  if (h.scope) return h.scope;
  if (!h.asn && isIX(h)) return 'Internet exchange';
  return h.asn ? `AS${h.asn} ${h.asOrg || ''}` : 'Unknown network';
}
function hopNetwork(h) {
  if (h.asn) return `AS${h.asn} ${h.asOrg}`;
  if (h.inAsn) return `private address inside AS${h.inAsn} ${h.inOrg || ''}`;
  if (h.zone === 'provider') return 'private address inside a provider\'s network';
  if (h.zone === 'lan') return 'private address on your LAN';
  return h.scope;
}
function shortHost(h) {
  if (!h.addr) return 'no reply';
  if (!h.host) return h.addr;
  const p = h.host.split('.');
  return p.length > 3 ? p.slice(0, 2).join('.') : h.host;
}
function hopTitle(h) {
  return [`Hop ${h.ttl}: ${h.host ? h.host + ' ' : ''}${h.addr || 'no reply'}`, hopNetwork(h),
    h.recv ? `avg ${fmtMs(h.avg)} ms (best ${fmtMs(h.best)}, worst ${fmtMs(h.worst)})` : '', h.sent ? `${h.loss.toFixed(1)}% loss` : ''].filter(Boolean).join('\n');
}

// A left-to-right diagram of one traced path, grouped into network bands.
function pathStrip(d) {
  const hops = d.hops || [];
  if (!hops.length) return '';
  const nodeW = 116, padL = 74, H = 128, cy = 66;
  const W = padL + hops.length * nodeW + 16;
  const segs = [];
  let cur = null;
  hops.forEach((h, i) => {
    const k = segKey(h) || (cur ? cur.key : 'unknown');
    if (!cur || cur.key !== k) {
      cur = { key: k, label: h.addr ? segLabel(h) : '', from: i, to: i };
      segs.push(cur);
    } else {
      cur.to = i;
      if (!cur.label && h.addr) cur.label = segLabel(h);
    }
  });
  const X = i => padL + i * nodeW + nodeW / 2;
  let svg = '';
  segs.forEach((s, si) => {
    const x0 = padL + s.from * nodeW + 4, x1 = padL + (s.to + 1) * nodeW - 4;
    const local = s.key.startsWith('scope:') || s.key === 'provider';
    // Keep narrow network bands readable; the complete organisation stays on hover.
    const label = s.key.startsWith('as:') ? `AS${s.key.slice(3)}` : s.label;
    const title = s.key === 'provider' ? PROVIDER_PRIVATE : s.label || '';
    svg += `<g class="path-band"><title>${esc(title)}</title>`
      + `<rect x="${x0}" y="12" width="${x1 - x0}" height="${H - 22}" rx="12" class="band ${local ? 'local' : 'b' + (si % 6)}"/>`
      + `<text x="${x0 + 10}" y="29" class="band-label">${esc(trimTo(label, Math.floor((x1 - x0 - 16) / 6.4)))}</text></g>`;
  });
  svg += `<line x1="52" y1="${cy}" x2="${X(0)}" y2="${cy}" class="edge"/>`;
  for (let i = 1; i < hops.length; i++) {
    const dashed = !hops[i].addr || !hops[i - 1].addr;
    svg += `<line x1="${X(i - 1)}" y1="${cy}" x2="${X(i)}" y2="${cy}" class="edge${dashed ? ' dashed' : ''}"/>`;
  }
  svg += `<g class="pc-node"><rect x="12" y="${cy - 15}" width="40" height="30" rx="8"/><text x="32" y="${cy + 4}">PC</text></g>`;
  hops.forEach((h, i) => {
    const last = i === hops.length - 1 && d.reached;
    const cls = !h.addr ? 'none' : h.loss >= 50 ? 'bad' : h.loss > 0 ? 'warn' : 'ok';
    svg += `<g class="hop-node ${cls}${last ? ' dest' : ''}"><title>${esc(hopTitle(h))}</title>`
      + `<text x="${X(i)}" y="${cy - 16}" class="node-ttl">${h.ttl}</text>`
      + `<circle cx="${X(i)}" cy="${cy}" r="${last ? 9 : 7}"/>`
      + `<text x="${X(i)}" y="${cy + 25}" class="node-label">${esc(trimTo(shortHost(h), 17))}</text>`
      + `<text x="${X(i)}" y="${cy + 39}" class="node-ms">${h.recv ? fmtMs(h.avg) + ' ms' : ''}</text></g>`;
  });
  return `<div class="path-strip"><svg width="${W}" height="${H}" viewBox="0 0 ${W} ${H}" role="img" aria-label="Path to ${esc(d.target)}">${svg}</svg></div>`;
}

// ---- route map: traces to the top networks merged into one tree

const routeState = { es: null, targets: [], routes: [], running: false };

function routeTreeSVG(routes, own) {
  const nodes = new Map();
  const edges = new Map();
  routes.forEach((r, ri) => {
    let prev = 'PC', gap = false, lastNet = null;
    for (const h of r.path.hops) {
      if (!h.addr) { gap = true; continue; }
      let n = nodes.get(h.addr);
      if (!n) { n = { key: h.addr, h, col: h.ttl, rows: [], kind: hopKind(h, own, lastNet) }; nodes.set(h.addr, n); }
      if (!h.scope) lastNet = h;
      n.col = Math.min(n.col, h.ttl);
      if (!n.rows.includes(ri)) n.rows.push(ri);
      const ek = prev + '>' + h.addr;
      if (!edges.has(ek)) edges.set(ek, { a: prev, b: h.addr, dashed: gap, rows: [] });
      edges.get(ek).rows.push(ri);
      prev = h.addr;
      gap = false;
    }
    r.lastKey = prev;
  });
  const rowH = 54, colW = 122, padT = 30, padL = 70;
  let maxCol = 1;
  for (const n of nodes.values()) maxCol = Math.max(maxCol, n.col);
  // y = average row of the routes through the node; then de-overlap per column
  const cols = new Map();
  for (const n of nodes.values()) {
    n.y = padT + (n.rows.reduce((a, b) => a + b, 0) / n.rows.length) * rowH;
    if (!cols.has(n.col)) cols.set(n.col, []);
    cols.get(n.col).push(n);
  }
  for (const list of cols.values()) {
    list.sort((a, b) => a.y - b.y);
    for (let i = 1; i < list.length; i++) if (list[i].y - list[i - 1].y < rowH * 0.9) list[i].y = list[i - 1].y + rowH * 0.9;
  }
  const pcY = padT + ((routes.length - 1) / 2) * rowH;
  const X = c => padL + c * colW;
  const H = Math.max(...[...nodes.values()].map(n => n.y), pcY, padT + (routes.length - 1) * rowH) + 40;
  const labelX = X(maxCol) + 34; // destination names line up in one column
  const W = labelX + 290;
  const pos = k => (k === 'PC' ? { x: 34, y: pcY } : { x: X(nodes.get(k).col), y: nodes.get(k).y });
  let svg = '';
  for (const e of edges.values()) {
    const a = pos(e.a), b = pos(e.b);
    const mx = (a.x + b.x) / 2;
    svg += `<path d="M${a.x},${a.y} C${mx},${a.y} ${mx},${b.y} ${b.x},${b.y}" class="tree-edge${e.dashed ? ' dashed' : ''}" style="stroke-width:${Math.min(4, 1.2 + e.rows.length * 0.35)}"/>`;
  }
  svg += `<g class="pc-node"><rect x="14" y="${pcY - 15}" width="40" height="30" rx="8"/><text x="34" y="${pcY + 4}">PC</text></g>`;
  const exits = new Set(routes.map(r => {
    const first = r.path.hops.find(h => h.addr && !h.scope && !own.has(h.asn));
    return first && first.addr;
  }).filter(Boolean));
  for (const n of nodes.values()) {
    const kind = n.kind;
    const cls = n.h.loss >= 50 ? ' bad' : n.h.loss > 0 ? ' warn' : '';
    svg += `<g class="tree-node ${kind}${cls}${exits.has(n.key) ? ' exit' : ''}"><title>${esc(hopTitle(n.h))}</title>`
      + `<circle cx="${X(n.col)}" cy="${n.y}" r="7"/>`
      + `<text x="${X(n.col)}" y="${n.y - 12}" class="node-label">${esc(trimTo(shortHost(n.h), 18))}</text>`
      + `<text x="${X(n.col)}" y="${n.y + 20}" class="node-ms">${n.h.recv ? fmtMs(n.h.avg) + ' ms' : ''}</text></g>`;
  }
  routes.forEach(r => {
    if (!r.lastKey || r.lastKey === 'PC') return;
    const p = pos(r.lastKey);
    if (labelX - p.x > 30) svg += `<line x1="${p.x + 11}" y1="${p.y}" x2="${labelX - 8}" y2="${p.y}" class="leader"/>`;
    svg += `<text x="${labelX}" y="${p.y + 4}" class="dest-label">${esc(trimTo(r.target.org, 26))} <tspan class="muted-t">AS${r.target.asn} · ${r.target.share.toFixed(1)}%</tspan></text>`;
  });
  return `<svg width="${W}" height="${H}" viewBox="0 0 ${W} ${H}" role="img" aria-label="Route map">${svg}</svg>`;
}

function routeSummary(routes, own) {
  const rows = routes.slice().sort((a, b) => b.target.share - a.target.share).map(r => {
    const hops = r.path.hops;
    let exitHop = null, next = null;
    for (const h of hops) {
      if (!h.addr) continue;
      if (h.scope || own.has(h.asn)) { exitHop = h; continue; }
      next = h;
      break;
    }
    const ix = hops.some(h => hopKind(h, own) === 'ix');
    const last = hops[hops.length - 1];
    return `<tr><td class="trunc">${flag(r.target.cc)}<b>${esc(r.target.org)}</b> <span class="muted">AS${r.target.asn}</span></td><td class="mono-sm">${esc(r.target.ip)}</td>`
      + `<td class="trunc">${exitHop ? esc(shortHost(exitHop)) : '–'}</td><td class="trunc">${next ? esc(next.asn ? `AS${next.asn} ${next.asOrg}` : next.addr) : '<span class="muted">–</span>'}</td>`
      + `<td>${ix ? '<span class="pol open">via IX</span>' : '<span class="muted">no</span>'}</td><td class="num">${hops.length}</td>`
      + `<td class="num">${last && last.recv ? fmtMs(last.avg) + ' ms' : '–'}</td><td class="num">${r.path.reached ? '' : '<span class="q warn">not reached</span>'}</td></tr>`;
  }).join('');
  return `<table class="mini"><thead><tr><th>Network</th><th>Traced IP</th><th>Leaves your network at</th><th>Next network</th><th>Exchange</th><th class="num">Hops</th><th class="num">RTT</th><th></th></tr></thead><tbody>${rows}</tbody></table>`;
}

function renderRoutes() {
  const own = new Set(((peerState.data && peerState.data.own.asns) || []));
  const done = routeState.routes.filter(Boolean);
  const body = $('#routeBody');
  if (!done.length) {
    body.innerHTML = `<div class="diag-empty">${routeState.running ? `Tracing ${routeState.targets.length} networks…` : 'Press <b>Map routes</b> to trace the path to the busiest IP of each of your top networks. Probes go only to those IPs.'}</div>`;
    return;
  }
  body.innerHTML = `<div class="route-legend"><span class="lg own"></span>Your network<span class="lg local"></span>LAN / carrier NAT<span class="lg ix"></span>Internet exchange<span class="lg net"></span>Other networks<span class="lg exitk"></span>Where each route leaves your network</div>`
    + `<div class="route-tree">${routeTreeSVG(done, own)}</div><div class="xtitle">Per destination</div>${routeSummary(done, own)}`;
}

function runRoutes() {
  const token = state.status && state.status.token;
  if (!token) return;
  if (routeState.es) routeState.es.close();
  routeState.targets = [];
  routeState.routes = [];
  routeState.running = true;
  $('#routeRun').textContent = 'Tracing…';
  $('#routeRun').disabled = true;
  $('#routeStatus').textContent = '';
  renderRoutes();
  const es = new EventSource(`/api/routes?n=8&period=${state.period}&token=${encodeURIComponent(token)}`);
  routeState.es = es;
  const finish = msg => {
    es.close();
    routeState.es = null;
    routeState.running = false;
    $('#routeRun').textContent = 'Map again';
    $('#routeRun').disabled = false;
    $('#routeStatus').textContent = msg || '';
    renderRoutes();
  };
  es.addEventListener('targets', e => { routeState.targets = JSON.parse(e.data) || []; renderRoutes(); $('#routeStatus').textContent = `0 of ${routeState.targets.length} traced`; });
  es.addEventListener('route', e => {
    const d = JSON.parse(e.data);
    routeState.routes[d.index] = { target: d.target, path: d.path };
    $('#routeStatus').textContent = `${routeState.routes.filter(Boolean).length} of ${routeState.targets.length} traced`;
    renderRoutes();
  });
  es.addEventListener('failure', e => finish(JSON.parse(e.data).error));
  es.addEventListener('done', () => finish(`Traced ${routeState.routes.filter(Boolean).length} networks · ${fmtTime(Math.floor(Date.now() / 1000))}`));
  es.onerror = () => { if (routeState.running) finish('Stopped'); };
}

function openRouteSheet() {
  if (inspector.owner) inspector.owner.collapse();
  const sheet = $('#routeSheet');
  sheet.classList.add('open');
  sheet.setAttribute('aria-hidden', 'false');
  renderRoutes();
  if (!routeState.routes.length && !routeState.running) runRoutes();
}
function closeRouteSheet() {
  const sheet = $('#routeSheet');
  sheet.classList.remove('open');
  sheet.setAttribute('aria-hidden', 'true');
}

// ---------------------------------------------------------------- diagnostics (MTR, traceroute, ping, TCP)

const TOOL_LABEL = { mtr: 'MTR', trace: 'Traceroute', ping: 'Ping', tcp: 'TCP port' };
const fmtMs = v => (v === undefined || v === null || v < 0) ? '–' : v < 10 ? v.toFixed(2) : v < 100 ? v.toFixed(1) : Math.round(v).toString();

function selectionIn(el) {
  const s = window.getSelection();
  return !!(s && !s.isCollapsed && s.rangeCount && el.contains(s.getRangeAt(0).commonAncestorContainer));
}

class Diag {
  constructor(el, ip, port) {
    this.el = el;
    this.ip = ip;
    this.port = port || 443;
    this.kind = saved.get('diagKind', 'mtr');
    this.es = null;
    this.data = null;
    this.running = false;
    this.pending = false;
    el.innerHTML = `
      <div class="diag-head">
        <span class="diag-title">
          <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M1.5 8h3l2-5 3 10 2-5h3" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
          Diagnostics
        </span>
        <div class="seg sm" role="radiogroup" aria-label="Diagnostic tool">
          <span class="seg-thumb" aria-hidden="true"></span>
          ${Object.entries(TOOL_LABEL).map(([k, l]) => `<button type="button" role="radio" data-value="${k}">${l}</button>`).join('')}
        </div>
        <label class="port-field"><span>Port</span><input type="number" min="1" max="65535" value="${this.port}" aria-label="TCP port"></label>
        <button type="button" class="btn primary sm diag-run">Start</button>
        <span class="diag-status"></span>
        <span class="spacer"></span>
        <button type="button" class="btn sm diag-pin" title="Keep pinging this IP in the background (Monitors tab)">
          <svg viewBox="0 0 16 16" width="13" height="13" aria-hidden="true"><path d="M1.5 8h3l2-5 3 10 2-5h3" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg><span>Monitor</span></button>
        <button type="button" class="btn sm diag-copy" hidden>${ICON_COPY}<span>Copy results</span></button>
      </div>
      <div class="diag-body"><div class="diag-empty"></div></div>`;
    this.seg = $('.seg', el);
    this.portField = $('.port-field', el);
    this.portInput = $('.port-field input', el);
    this.runBtn = $('.diag-run', el);
    this.statusEl = $('.diag-status', el);
    this.copyBtn = $('.diag-copy', el);
    this.body = $('.diag-body', el);
    initSeg(this.seg, k => this.setKind(k));
    this.runBtn.addEventListener('click', () => (this.running ? this.stop() : this.start()));
    this.copyBtn.addEventListener('click', async () => {
      if (await copyText(this.text())) { flashDone(this.copyBtn); toast('Copied results'); }
    });
    this.portInput.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); this.start(); } });
    $('.diag-pin', el).addEventListener('click', () => {
      const tcp = this.kind === 'tcp';
      addMonitor({ target: this.ip, name: this.ip, method: tcp ? 'tcp' : 'icmp', port: tcp ? parseInt(this.portInput.value, 10) || 0 : 0, interval: 2 });
    });
    this.setKind(this.kind);
  }
  setKind(k) {
    if (this.running) this.stop();
    this.kind = k;
    saved.set('diagKind', k);
    setSeg(this.seg, k);
    this.portField.hidden = k !== 'tcp';
    this.routeView = 'path';
    this.routeMap = null;
    this.data = null;
    this.copyBtn.hidden = true;
    this.statusEl.textContent = '';
    const hint = {
      mtr: 'Continuous traceroute: probes every hop each second and builds loss and latency statistics per hop.',
      trace: 'Three probes per hop to map the path once.',
      ping: 'ICMP echo every second, with loss, latency and jitter.',
      tcp: 'Times a TCP handshake to the port every second. Useful where ICMP is filtered.',
    }[k];
    this.body.innerHTML = `<div class="diag-empty">${esc(hint)} Results stay on this PC.</div>`;
  }
  start() {
    this.stop(true);
    const token = state.status && state.status.token;
    if (!token) { toast('Not ready yet, try again in a moment'); return; }
    let port = 0;
    if (this.kind === 'tcp') {
      port = parseInt(this.portInput.value, 10);
      if (!(port >= 1 && port <= 65535)) { toast('Enter a port between 1 and 65535'); return; }
    }
    this.data = null;
    this.running = true;
    this.started = Date.now();
    this.runBtn.textContent = 'Stop';
    this.runBtn.classList.remove('primary');
    this.copyBtn.hidden = true;
    this.status('Starting…', 'live');
    this.body.innerHTML = '<div class="diag-empty">Waiting for the first results…</div>';
    const url = `/api/tool?kind=${this.kind}&ip=${encodeURIComponent(this.ip)}&port=${port}&token=${encodeURIComponent(token)}`;
    const es = new EventSource(url);
    this.es = es;
    es.onmessage = e => {
      this.data = JSON.parse(e.data);
      this.render();
    };
    es.addEventListener('done', e => {
      const d = JSON.parse(e.data || '{}');
      this.finish(d.reason || (this.kind === 'trace' ? 'Completed' : 'Stopped'));
    });
    es.addEventListener('failure', e => this.finish(JSON.parse(e.data || '{}').error || 'Failed', true));
    es.onerror = () => {
      if (!this.running) return;
      this.finish(this.data ? 'Connection closed' : 'Could not start. Too many diagnostics may be running.', !this.data);
    };
  }
  stop(silent) {
    if (this.es) { this.es.close(); this.es = null; }
    if (this.running) this.finish(silent ? '' : 'Stopped');
  }
  finish(msg, isErr) {
    if (this.es) { this.es.close(); this.es = null; }
    this.running = false;
    this.runBtn.textContent = this.data ? 'Run again' : 'Start';
    this.runBtn.classList.add('primary');
    if (this.data) { this.data.running = false; this.render(true); }
    this.copyBtn.hidden = !this.data;
    this.status(msg, isErr ? 'bad' : '');
  }
  status(text, cls) {
    this.statusEl.className = 'diag-status' + (cls ? ' ' + cls : '');
    this.statusEl.textContent = text;
  }
  render(force) {
    const d = this.data;
    if (!d) return;
    if (!force && selectionIn(this.body)) { this.pending = true; return; } // don't wipe a selection
    this.pending = false;
    if (d.kind === 'mtr' || d.kind === 'trace') this.renderPath(d);
    else this.renderSeries(d);
    this.copyBtn.hidden = false;
    if (this.running) {
      const what = d.kind === 'mtr' || d.kind === 'trace' ? `round ${d.rounds}` : `${d.sent} sent`;
      this.status(`Running · ${what}`, 'live');
    }
  }
  renderPath(d) {
    const max = Math.max(1, ...d.hops.map(h => h.worst || 0));
    const rows = d.hops.map(h => {
      const lossCls = h.sent === 0 ? '' : h.loss >= 100 ? 'bad' : h.loss > 0 ? 'warn' : 'ok';
      const where = h.scope ? `<span class="tag">${esc(h.scope)}</span>` : (h.cc ? `${flag(h.cc)}${esc(h.city || h.cc)}` : '');
      const host = h.addr
        ? `<span class="hop-host">${esc(h.host || h.addr)}</span>${h.host ? `<span class="hop-ip">${esc(h.addr)}</span>` : ''}${(h.others || []).length ? `<span class="hop-ip" title="Other addresses seen at this hop (ECMP): ${esc(h.others.join(', '))}">+${h.others.length}</span>` : ''}`
        : '<span class="muted">No reply</span>';
      const range = h.recv ? rangeBar(h.best, h.avg, h.worst, max) : '';
      return `<tr><td class="num dim">${h.ttl}</td><td class="hop">${host}</td><td class="trunc">${h.asn ? `<span class="mono-sm">AS${h.asn}</span> <span class="muted">${esc(h.asOrg)}</span>` : ''}</td>`
        + `<td class="trunc">${where}</td><td class="num loss ${lossCls}">${h.sent ? h.loss.toFixed(1) + '%' : '–'}</td><td class="num dim">${h.sent}</td>`
        + `<td class="num">${fmtMs(h.recv ? h.last : -1)}</td><td class="num strong">${fmtMs(h.recv ? h.avg : -1)}</td><td class="num">${fmtMs(h.recv ? h.best : -1)}</td><td class="num">${fmtMs(h.recv ? h.worst : -1)}</td><td class="num dim">${fmtMs(h.recv ? h.stdev : -1)}</td><td class="rangecell">${range}</td></tr>`;
    }).join('');
    // Keep scroll owners alive across updates, including trackpad momentum.
    if (!this.body.querySelector('.diag-scroll')) {
      this.body.innerHTML = '<div class="diag-note" hidden></div><div class="route-views" role="group" aria-label="Route view"><button class="btn sm" data-route-view="path" aria-pressed="true">Path</button><button class="btn sm" data-route-view="map" aria-pressed="false">Map</button></div><div class="path-strip"></div><div class="route-map" hidden></div><div class="diag-scroll"></div>';
      this.routeMap = null;
      this.body.querySelectorAll('[data-route-view]').forEach(button => button.addEventListener('click', () => {
        this.routeView = button.dataset.routeView;
        this.updateRouteView(d);
      }));
    }
    const noteEl = this.body.querySelector('.diag-note');
    noteEl.hidden = !d.note;
    noteEl.textContent = d.note || '';
    const path = this.body.querySelector('.path-strip');
    const nextPath = document.createElement('template');
    nextPath.innerHTML = pathStrip(d);
    path.replaceChildren(...(nextPath.content.firstElementChild?.childNodes || []));
    this.body.querySelector('.diag-scroll').innerHTML = `<table class="mini hops"><thead><tr><th class="num">#</th><th>Host</th><th>Network</th><th>Location</th><th class="num">Loss</th><th class="num">Sent</th><th class="num">Last</th><th class="num">Avg</th><th class="num">Best</th><th class="num">Worst</th><th class="num">StDev</th><th>Latency (ms)</th></tr></thead><tbody>${rows}</tbody></table>`;
    this.updateRouteView(d);
  }
  updateRouteView(fallback) {
    const d = this.data || fallback;
    const map = this.body.querySelector('.route-map');
    const active = this.routeView === 'map';
    map.hidden = !active;
    this.body.querySelector('.path-strip').hidden = active;
    this.body.querySelectorAll('[data-route-view]').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.routeView === (active ? 'map' : 'path'))));
    if (active) {
      if (!this.routeMap) this.routeMap = new RouteMap(map);
      this.routeMap.update(d);
    }
  }
  renderSeries(d) {
    const tiles = [
      ['Sent', d.sent], ['Received', d.recv], ['Loss', d.sent ? d.loss.toFixed(1) + '%' : '–'],
      ['Min', fmtMs(d.recv ? d.min : -1) + ' ms'], ['Avg', fmtMs(d.recv ? d.avg : -1) + ' ms'], ['Max', fmtMs(d.recv ? d.max : -1) + ' ms'], ['Jitter', fmtMs(d.recv > 1 ? d.jitter : -1) + ' ms'],
    ].map(([k, v]) => `<div class="kpi"><span class="lbl">${k}</span><span class="val">${v}</span></div>`).join('');
    const last = d.samples[d.samples.length - 1];
    let lastLine = '';
    if (last) {
      lastLine = last.rtt >= 0
        ? (d.kind === 'tcp' ? `Port ${d.port} open · handshake ${fmtMs(last.rtt)} ms` : `Reply from ${esc(last.from || d.target)} · ${fmtMs(last.rtt)} ms${last.ttl ? ` · TTL ${last.ttl}` : ''}`)
        : (d.kind === 'tcp' ? `Port ${d.port}: ${esc(last.status)}` : `No reply (${esc(last.status)})`);
    }
    this.body.innerHTML = `<div class="xkpis">${tiles}</div>${seriesBars(d.samples)}<div class="diag-last">${lastLine}</div>`;
  }
  text() {
    const d = this.data;
    if (!d) return '';
    const head = `${TOOL_LABEL[d.kind]} to ${d.target}${d.port ? ':' + d.port : ''} · ${isoTime(Math.floor(Date.now() / 1000))}`;
    if (d.kind === 'mtr' || d.kind === 'trace') {
      const lines = [head + ` · ${d.rounds} round${d.rounds === 1 ? '' : 's'}`, ' #  Host                                      Loss%   Snt    Last     Avg    Best    Wrst   StDev  Network'];
      for (const h of d.hops) {
        const host = (h.addr ? (h.host && h.host !== h.addr ? `${h.host} (${h.addr})` : h.addr) : '???').slice(0, 41).padEnd(41);
        const n = v => (h.recv ? fmtMs(v) : '–').padStart(7);
        lines.push(`${String(h.ttl).padStart(2)}. ${host} ${(h.sent ? h.loss.toFixed(1) + '%' : '–').padStart(6)} ${String(h.sent).padStart(5)} ${n(h.last)} ${n(h.avg)} ${n(h.best)} ${n(h.worst)} ${n(h.stdev)}  ${h.asn ? 'AS' + h.asn + ' ' + h.asOrg : (h.scope || '')}`);
      }
      return lines.join('\r\n');
    }
    return [head,
      `${d.sent} sent, ${d.recv} received, ${d.loss.toFixed(1)}% loss`,
      d.recv ? `rtt min/avg/max/stdev = ${fmtMs(d.min)}/${fmtMs(d.avg)}/${fmtMs(d.max)}/${fmtMs(d.stdev)} ms, jitter ${fmtMs(d.jitter)} ms` : '',
      ...d.samples.map(s => `seq ${s.seq}: ${s.rtt >= 0 ? fmtMs(s.rtt) + ' ms' : s.status}${s.ttl ? ' ttl ' + s.ttl : ''}`),
    ].filter(Boolean).join('\r\n');
  }
}

// A best–worst range with the average marked, scaled to the slowest hop.
function rangeBar(best, avg, worst, max) {
  const W = 120, x = v => 2 + (Math.min(v, max) / max) * (W - 4);
  return `<svg class="range" width="${W}" height="12" viewBox="0 0 ${W} 12" aria-hidden="true"><rect x="2" y="5" width="${W - 4}" height="2" rx="1" class="track"/><rect x="${x(best).toFixed(1)}" y="3.5" width="${Math.max(2, x(worst) - x(best)).toFixed(1)}" height="5" rx="2.5" class="span"/><circle cx="${x(avg).toFixed(1)}" cy="6" r="3" class="avg"/></svg>`;
}

// Bars for the last 60 samples; lost probes show as short red ticks.
function seriesBars(samples) {
  const s = samples.slice(-60);
  if (!s.length) return '';
  const max = Math.max(1, ...s.map(x => x.rtt));
  const W = 600, H = 56, bw = W / 60;
  const bars = s.map((x, i) => {
    const xpos = (i + 60 - s.length) * bw + 1;
    if (x.rtt < 0) return `<rect x="${xpos.toFixed(1)}" y="${H - 6}" width="${(bw - 2).toFixed(1)}" height="6" rx="1.5" class="lost"><title>seq ${x.seq}: ${esc(x.status)}</title></rect>`;
    const h = Math.max(2, (x.rtt / max) * (H - 4));
    return `<rect x="${xpos.toFixed(1)}" y="${(H - h).toFixed(1)}" width="${(bw - 2).toFixed(1)}" height="${h.toFixed(1)}" rx="1.5" class="ok"><title>seq ${x.seq}: ${fmtMs(x.rtt)} ms</title></rect>`;
  }).join('');
  return `<svg class="bars" viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" aria-label="Round-trip times">${bars}</svg>`;
}

document.addEventListener('selectionchange', () => {
  for (const p of $$('.xpanel')) if (p._diag && p._diag.pending && !selectionIn(p._diag.body)) p._diag.render();
});

function stopDiag(xr) {
  const p = xr.querySelector('.xpanel');
  if (p && p._diag) p._diag.stop(true);
}

const ipExpand = {
  url: r => `/api/ip?period=${state.period}&ip=${encodeURIComponent(r.ip)}`,
  skeleton: IP_SKELETON,
  render: renderIP,
  init(panel, d, row) {
    const out = (d.apps || []).find(a => a.dir === 'out');
    const port = row && row.dir === 'out' && row.port ? row.port : out ? out.port : 443;
    panel._diag = new Diag(panel.querySelector('.xdiag'), d.ip, port);
  },
};
const asnExpand = {
  url: r => `/api/asn?period=${state.period}&key=${encodeURIComponent(r.key)}`,
  skeleton: ASN_SKELETON,
  render: renderASN,
};

// ---------------------------------------------------------------- tables

const tables = {};
tables.apps = new Table('apps', [
  { key: 'spark', label: 'Last 60 s', csv: false, html: r => spark(r.spark), sortVal: r => r.rateIn + r.rateOut, num: true, title: 'Traffic over the last 60 seconds (sort = current rate)' },
  { key: 'name', label: 'Application', cls: 'trunc strong', html: r => appCell(r.key, r.name), tip: r => r.path || r.name, text: r => `${r.name} ${r.path || ''}` },
  { key: 'pids', label: 'PID', cls: 'dim mono-sm', title: 'Process IDs seen in the last 10 minutes' },
  ...rateCols(), ...byteCols(), ...qualityCols(),
  { key: 'ips', label: 'Remote IPs', num: true },
  { key: 'flows', label: 'Endpoints', num: true, title: 'Distinct protocol / IP / port combinations' },
  ...timeCols(),
  { key: 'path', label: 'Path', hidden: true },
], {
  sort: { key: 'total', dir: -1 },
  rowKey: r => r.key,
  onSelect: r => selectApp(r),
  selected: r => r.key === state.app,
});

tables.flows = new Table('flows', [
  chevCol(),
  { key: 'proto', label: 'Proto', cls: 'dim' },
  { key: 'ip', label: 'Remote IP', cls: 'ipcell', html: ipCell, sortVal: r => ipKey(r.ip) },
  { key: 'port', label: 'Port', num: true, fmt: (v, r) => (r.dir === 'in' ? '← ' : '→ ') + v, title: '→ remote port (we connected out)   ← local port (they connected in)' },
  { key: 'dir', label: 'Direction', hidden: true },
  { key: 'host', label: 'Hostname (DNS)', cls: 'trunc', tip: r => r.host, title: 'The name the app looked up that resolved to this IP' },
  ...rateCols(), ...byteCols(), ...qualityCols(), ...geoCols(),
  { key: 'ptr', label: 'Reverse DNS', cls: 'trunc dim', tip: r => r.ptr },
  ...timeCols(),
], { sort: { key: 'total', dir: -1 }, rowKey: r => `${r.proto}|${r.ip}|${r.port}|${r.dir}`, expand: ipExpand });

tables.ips = new Table('ips', [
  chevCol(),
  { key: 'ip', label: 'Remote IP', cls: 'ipcell', html: ipCell, sortVal: r => ipKey(r.ip) },
  { key: 'host', label: 'Hostname (DNS)', cls: 'trunc', tip: r => r.host },
  ...rateCols(), ...byteCols(), ...qualityCols(), ...geoCols(),
  { key: 'apps', label: 'Applications', cls: 'trunc', tip: r => r.apps },
  { key: 'ports', label: 'Ports', cls: 'trunc dim', tip: r => r.ports },
  { key: 'ptr', label: 'Reverse DNS', cls: 'trunc dim', tip: r => r.ptr },
  ...timeCols(),
], { sort: { key: 'total', dir: -1 }, rowKey: r => r.ip, expand: { ...ipExpand, sheet: true }, prefilter: r => !(state.hidePrivate && r.scope) });

tables.asns = new Table('asns', [
  chevCol(),
  { key: 'asn', label: 'ASN', cls: 'mono-sm', fmt: (v, r) => asnText(r), text: asnText, sortVal: r => r.asn || '', csv: r => r.asn || '' },
  { key: 'org', label: 'Organisation', cls: 'trunc strong', tip: r => r.org },
  ...rateCols(), ...byteCols(), ...qualityCols(),
  { key: 'ips', label: 'IPs', num: true },
  { key: 'countries', label: 'Countries', cls: 'trunc dim', html: r => r.countries ? r.countries.split(', ').map(c => /^[A-Z]{2}$/.test(c) ? `${flag(c)}${c}` : esc(c)).join('<span class="sep"></span>') : '', text: r => r.countries },
  { key: 'apps', label: 'Applications', cls: 'trunc', tip: r => r.apps },
  ...timeCols(),
], { sort: { key: 'total', dir: -1 }, rowKey: r => r.key, expand: { ...asnExpand, sheet: true } });

// ---------------------------------------------------------------- peering view

const peerState = { data: null, editing: false };

function policyHtml(r) {
  const p = r.pdb && r.pdb.found ? r.pdb.policy : '';
  if (!p) return r.pending ? '<span class="dim">…</span>' : '';
  const cls = /open/i.test(p) ? 'open' : /restrict|no/i.test(p) ? 'closed' : 'sel';
  return `<span class="pol ${cls}">${esc(p)}</span>`;
}
// What to say while a PeeringDB record is still missing: the actual problem
// when the lookups are failing, not an endless "Looking up".
function pdbWaiting() {
  const e = peerState.data && peerState.data.pdbError;
  return e ? esc(e) : 'Looking up PeeringDB…';
}
function commonHtml(r) {
  if (!r.pdb) {
    if (!r.pending) return '';
    const e = peerState.data && peerState.data.pdbError;
    return e ? `<span class="dim" title="${esc(e)}">Unavailable</span>` : '<span class="dim">Looking up…</span>';
  }
  if (!r.common.length) return '<span class="dim">–</span>';
  return r.common.map(x => `<span class="ixchip">${esc(x.name.split(':')[0])}</span>`).join('');
}
function candidateHtml(r) {
  if (!r.candidate) return '';
  const good = /common IX/.test(r.candidate);
  return `<span class="cand ${good ? 'good' : ''}">${esc(r.candidate)}</span>`;
}
function shareHtml(r) {
  return `<span class="sharecell"><span class="sharebar"><i style="width:${Math.min(100, r.share).toFixed(1)}%"></i></span>${r.share.toFixed(1)}%</span>`;
}

const PEER_SKELETON = `
  <div class="xgrid">
    <div class="xtop" data-sec="head"></div>
    <div class="xcol" data-sec="facts"></div>
    <div class="xcol"><div class="xtitle">Internet exchanges <span class="muted">· shared with your network first</span></div><div data-sec="ixes"></div></div>
  </div>`;

function renderPeer(r) {
  const p = r.pdb;
  const head = `<div class="xid"><div class="xip"><span class="mono">AS${r.asn}</span>${copyBtn('AS' + r.asn)}<span class="org">${esc(r.org)}</span></div>
    <div class="xsub">${flag(r.cc)}${esc(r.cc || '')}<span class="dot">·</span>${r.share.toFixed(1)}% of your off-net traffic<span class="dot">·</span>${fmtBytes(r.total)}</div></div>`;
  const none = t => `<span class="muted">${t}</span>`;
  const facts = !p ? none(r.pending ? pdbWaiting() : 'Not looked up (only the top 25 networks are)')
    : !p.found ? none('This network has no PeeringDB record.')
      : '<dl class="xfacts">' + [
        ['Name', esc(p.name)], ['Type', esc(p.type || '–')], ['Peering policy', policyHtml(r) || '–'],
        ['Traffic ratio', esc(p.ratio || '–')], ['Scope', esc(p.scope || '–')], ['Traffic level', esc(p.traffic || '–')],
        ['Exchanges', `${p.ixes.length} (${r.common.length} shared with you)`],
        ['Path quality', `RTT ${fmtRTT(r.rtt)} · retransmitted ${r.retrans ? r.retransPct.toFixed(2) + '%' : 'none'}`],
      ].map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join('') + '</dl>';
  let ixes = none('–');
  if (p && p.found && p.ixes.length) {
    const common = new Set(r.common.map(x => x.id));
    const list = p.ixes.slice().sort((a, b) => (common.has(b.id) - common.has(a.id)) || a.name.localeCompare(b.name));
    ixes = `<table class="mini"><thead><tr><th>Exchange</th><th class="num">Port speed</th><th>Route server</th></tr></thead><tbody>${
      list.slice(0, 60).map(x => `<tr class="${common.has(x.id) ? 'shared' : ''}"><td class="trunc">${common.has(x.id) ? '<span class="ixdot"></span>' : ''}${esc(x.name)}</td><td class="num">${x.speed >= 1000 ? (x.speed / 1000) + ' Gbit/s' : x.speed + ' Mbit/s'}</td><td class="dim">${x.rs ? 'yes' : 'no'}</td></tr>`).join('')
    }</tbody></table>${list.length > 60 ? `<div class="more">+${list.length - 60} more</div>` : ''}`;
  }
  const text = [`AS${r.asn} ${r.org}`, `Share of off-net traffic: ${r.share.toFixed(1)}% (${fmtBytes(r.total)})`,
    p && p.found ? `PeeringDB: ${p.type || '-'}, policy ${p.policy || '-'}, ${p.ixes.length} exchanges` : '',
    r.common.length ? `Shared exchanges: ${r.common.map(x => x.name).join('; ')}` : '', r.candidate ? `Hint: ${r.candidate}` : ''].filter(Boolean).join('\r\n');
  return { sections: { head, facts, ixes }, text };
}

tables.peers = new Table('peers', [
  chevCol(),
  { key: 'rank', label: '#', num: true, title: 'Rank by traffic volume' },
  { key: 'asn', label: 'ASN', cls: 'mono-sm', fmt: (v, r) => 'AS' + r.asn, text: r => 'AS' + r.asn },
  { key: 'org', label: 'Network', cls: 'trunc strong', html: r => `${flag(r.cc)}${esc(r.org)}`, tip: r => r.org, text: r => `${r.org} ${r.cc}` },
  { key: 'share', label: 'Share', num: true, html: shareHtml, title: "This network's part of all the traffic this PC exchanged with networks outside your own (off-net) in the selected period. Traffic to your own network (on-net) and LAN addresses isn't counted.", csvLabel: 'Share of off-net (%)' },
  ...rateCols(), ...byteCols(), ...qualityCols(),
  { key: 'ips', label: 'IPs', num: true },
  { key: 'type', label: 'Type', cls: 'dim', fmt: (v, r) => (r.pdb && r.pdb.found ? r.pdb.type : ''), sortVal: r => (r.pdb && r.pdb.type) || '', csv: r => (r.pdb && r.pdb.type) || '' },
  { key: 'policy', label: 'Policy', html: policyHtml, sortVal: r => (r.pdb && r.pdb.policy) || '', text: r => (r.pdb && r.pdb.policy) || '', csv: r => (r.pdb && r.pdb.policy) || '' },
  { key: 'ixCount', label: 'IXes', num: true, fmt: (v, r) => (r.pdb && r.pdb.found ? r.pdb.ixes.length : ''), sortVal: r => (r.pdb && r.pdb.found ? r.pdb.ixes.length : ''), title: 'Internet exchanges where this network peers (PeeringDB)', csv: r => (r.pdb && r.pdb.found ? r.pdb.ixes.length : '') },
  { key: 'common', label: 'Shared IXes', cls: 'trunc', html: commonHtml, sortVal: r => r.common.length, text: r => r.common.map(x => x.name).join(' '), csv: r => r.common.map(x => x.name).join('; '), title: 'Exchanges where both you and this network are present' },
  { key: 'candidate', label: 'Peering hint', html: candidateHtml, text: r => r.candidate },
  { key: 'apps', label: 'Applications', cls: 'trunc dim', tip: r => r.apps },
], { sort: { key: 'total', dir: -1 }, rowKey: r => String(r.asn), expand: { skeleton: PEER_SKELETON, render: renderPeer, sheet: true } });

function renderOwn(d) {
  const own = d.own;
  const recs = d.ownPdb || [];
  const asns = own.asns || [];
  let body;
  if (peerState.editing) {
    body = `<div class="own-edit">
      <input id="ownInput" class="field" value="${esc(asns.join(', '))}" placeholder="e.g. 64500, 64501" aria-label="Your AS numbers">
      <button type="button" class="btn primary sm" data-own="save">Save</button>
      <button type="button" class="btn sm" data-own="auto">Use auto-detection</button>
      <button type="button" class="btn sm" data-own="cancel">Cancel</button>
    </div><div class="own-detail">Enter the AS numbers that count as your own network (on-net). Separate several with commas.</div>`;
  } else if (!asns.length) {
    body = `<div class="own-asn muted">${d.detecting ? 'Detecting your network…' : 'Not detected'}</div>
      <div class="own-detail">${esc(own.detail || '')}</div>
      <div class="own-actions"><button type="button" class="btn sm" data-own="edit">Set manually</button><button type="button" class="btn sm" data-own="detect">Detect again</button></div>`;
  } else {
    const names = asns.map(a => { const r = recs.find(x => x.asn === a); return `<span class="mono">AS${a}</span> ${esc(r && r.found ? r.name : '')}`; }).join('<span class="dot">·</span>');
    const ixes = recs.flatMap(r => r.ixes || []);
    body = `<div class="own-asn">${names}</div>
      <div class="own-detail">${own.source === 'manual' ? 'Set by you' : esc(own.detail || 'Detected')}</div>
      <div class="own-ix">${ixes.length ? `${ixes.length} exchange${ixes.length === 1 ? '' : 's'}: ${ixes.slice(0, 8).map(x => `<span class="ixchip">${esc(x.name.split(':')[0])}</span>`).join('')}${ixes.length > 8 ? ` <span class="muted">+${ixes.length - 8}</span>` : ''}` : (recs.length ? '<span class="muted">No public exchange presence in PeeringDB</span>' : `<span class="muted">${d.pdbError ? esc(d.pdbError) : 'Looking up PeeringDB…'}</span>`)}</div>
      <div class="own-actions"><button type="button" class="btn sm" data-own="edit">Change</button>${own.source === 'detected' ? '<button type="button" class="btn sm" data-own="detect">Detect again</button>' : ''}</div>`;
  }
  return `<div class="card-label">Your network</div>${body}`;
}

function renderMix(d) {
  const total = d.local + d.onNet + d.offNet;
  const pct = v => (total ? (100 * v) / total : 0);
  const top5 = d.networks.slice(0, 5).reduce((a, n) => a + n.share, 0);
  const seg = (cls, v, label) => v ? `<i class="${cls}" style="width:${pct(v).toFixed(2)}%" title="${label} ${pct(v).toFixed(1)}%"></i>` : '';
  const leg = (cls, label, v) => `<div class="mix-leg"><span class="sw ${cls}"></span>${label}<b>${pct(v).toFixed(1)}%</b><span class="muted">${fmtBytes(v)}</span></div>`;
  return `<div class="card-label">Traffic mix · ${esc(periodLabel().toLowerCase())}</div>
    <div class="mixbar">${seg('off', d.offNet, 'Off-net')}${seg('on', d.onNet, 'On-net')}${seg('loc', d.local, 'Local')}</div>
    <div class="mix-legend">${leg('off', 'Off-net', d.offNet)}${leg('on', 'On-net', d.onNet)}${leg('loc', 'Local / private', d.local)}</div>
    <div class="mix-kpis"><div class="kpi"><span class="lbl">Networks reached</span><span class="val">${d.networks.length}</span></div>
    <div class="kpi"><span class="lbl">Top 5 networks</span><span class="val">${top5.toFixed(1)}%</span><span class="rate">of off-net traffic</span></div></div>`;
}

async function loadPeering() {
  const d = await api('/api/peering?period=' + state.period);
  peerState.data = d;
  d.networks.forEach((n, i) => { n.rank = i + 1; });
  tables.peers.setRows(d.networks);
  const own = $('#peerOwn');
  if (!peerState.editing) {
    const h = renderOwn(d);
    if (own._h !== h) { own.innerHTML = h; own._h = h; }
  }
  const mix = $('#peerMix');
  const mh = renderMix(d);
  if (mix._h !== mh) { mix.innerHTML = mh; mix._h = mh; }
  // One line above the table that says what "Share" is a share of.
  const ownNames = (d.own.asns || []).map(a => {
    const r = (d.ownPdb || []).find(x => x.asn === a);
    return `AS${a}${r && r.found && r.name ? ' ' + r.name : ''}`;
  }).join(', ');
  const ex = `<b>Share</b> is each network's part of the <b>${esc(fmtBytes(d.offNet))}</b> this PC exchanged with networks outside ${ownNames ? esc(ownNames) : 'your own network'} in this period (off-net). Traffic to your own network (on-net) and LAN addresses isn't counted.`;
  const exEl = $('#peerExplain');
  if (exEl._h !== ex) { exEl.innerHTML = ex; exEl._h = ex; }
  const pending = d.networks.filter(n => n.pending).length;
  $('#pdbNote').textContent = d.pdbError ? `PeeringDB: ${d.pdbError}` : pending ? `Fetching PeeringDB data for ${pending} network${pending === 1 ? '' : 's'}…` : '';
}

function initPeeringUI() {
  $('#peerOwn').addEventListener('click', async e => {
    const b = e.target.closest('button[data-own]');
    if (!b) return;
    const act = b.dataset.own;
    const own = $('#peerOwn');
    if (act === 'edit') {
      peerState.editing = true;
      own.innerHTML = renderOwn(peerState.data);
      own._h = '';
      const inp = $('#ownInput');
      inp.focus();
      inp.addEventListener('keydown', ev => { if (ev.key === 'Enter') $('[data-own="save"]', own).click(); if (ev.key === 'Escape') $('[data-own="cancel"]', own).click(); });
      return;
    }
    try {
      if (act === 'save') await post('/api/peering/own?asns=' + encodeURIComponent($('#ownInput').value));
      if (act === 'auto') await post('/api/peering/own?asns=');
      if (act === 'detect') { await post('/api/peering/detect'); toast('Detecting your network from the path to the internet…'); }
    } catch (err) { toast(err.message); }
    peerState.editing = false;
    own._h = '';
    invalidate('peering');
  });
}

// ---------------------------------------------------------------- charts

// Monotone cubic interpolation (Fritsch–Carlson): smooth, never overshoots.
function smoothPath(g, xs, ys) {
  const n = xs.length;
  if (n < 3) { g.moveTo(xs[0], ys[0]); for (let i = 1; i < n; i++) g.lineTo(xs[i], ys[i]); return; }
  const dx = [], m = [], t = new Array(n);
  for (let i = 0; i < n - 1; i++) { dx[i] = xs[i + 1] - xs[i]; m[i] = (ys[i + 1] - ys[i]) / dx[i]; }
  t[0] = m[0];
  t[n - 1] = m[n - 2];
  for (let i = 1; i < n - 1; i++) t[i] = m[i - 1] * m[i] <= 0 ? 0 : (m[i - 1] + m[i]) / 2;
  for (let i = 0; i < n - 1; i++) {
    if (m[i] === 0) { t[i] = 0; t[i + 1] = 0; continue; }
    const a = t[i] / m[i], b = t[i + 1] / m[i], h = a * a + b * b;
    if (h > 9) { const k = 3 / Math.sqrt(h); t[i] = k * a * m[i]; t[i + 1] = k * b * m[i]; }
  }
  g.moveTo(xs[0], ys[0]);
  for (let i = 0; i < n - 1; i++) {
    const d = dx[i] / 3;
    g.bezierCurveTo(xs[i] + d, ys[i] + t[i] * d, xs[i + 1] - d, ys[i + 1] - t[i + 1] * d, xs[i + 1], ys[i + 1]);
  }
}

class Chart {
  constructor(canvas, tip) {
    this.c = canvas;
    this.tip = tip;
    this.points = [];
    this.step = 1;
    this.hover = -1;
    this.h = +canvas.getAttribute('height');
    canvas.style.height = this.h + 'px';
    canvas.addEventListener('mousemove', e => {
      const r = canvas.getBoundingClientRect();
      const n = this.points.length;
      if (n < 2) return;
      const i = Math.round((e.clientX - r.left - this.pl) / ((r.width - this.pl - this.pr) / (n - 1)));
      this.hover = Math.max(0, Math.min(n - 1, i));
      this.draw();
    });
    canvas.addEventListener('mouseleave', () => { this.hover = -1; this.draw(); });
    new ResizeObserver(() => this.draw()).observe(canvas);
  }
  set(points, step) { this.points = points || []; this.step = step || 1; this.draw(); }
  draw() {
    const c = this.c, dpr = window.devicePixelRatio || 1;
    const w = c.clientWidth, h = this.h;
    if (!w) return;
    c.width = Math.round(w * dpr);
    c.height = Math.round(h * dpr);
    const g = c.getContext('2d');
    g.setTransform(dpr, 0, 0, dpr, 0, 0);
    g.clearRect(0, 0, w, h);
    const pts = this.points, n = pts.length, step = this.step;
    const pl = this.pl = 62, pr = this.pr = 6, pt = 8, pb = 20;
    const cw = w - pl - pr, ch = h - pt - pb;
    let max = 0;
    for (const p of pts) max = Math.max(max, p.in / step, p.out / step);
    const unit = state.bits ? 8 : 1;
    let top = max * unit;
    const mag = Math.pow(10, Math.floor(Math.log10(top || 1)));
    top = [1, 2, 4, 8, 10].map(m => m * mag).find(v => v >= top) || 10 * mag;
    if (!top || top < 8) top = 8;
    const ymax = top / unit;
    const faint = cssColor('--text-3'), grid = cssColor('--grid');
    g.font = '500 11px ' + getComputedStyle(document.body).fontFamily;
    g.fillStyle = faint;
    g.textAlign = 'right';
    g.textBaseline = 'middle';
    g.lineWidth = 1;
    for (let i = 0; i <= 4; i++) {
      const y = Math.round(pt + ch - (ch * i) / 4) + 0.5;
      g.strokeStyle = grid;
      g.setLineDash(i === 0 ? [] : [2, 4]);
      g.beginPath(); g.moveTo(pl, y); g.lineTo(w - pr, y); g.stroke();
      if (i > 0) g.fillText(fmtRateAlways((ymax * i) / 4), pl - 10, y);
    }
    g.setLineDash([]);
    if (n < 2) {
      g.textAlign = 'center';
      g.fillText('No traffic recorded for this period yet', pl + cw / 2, pt + ch / 2);
      this.showTip(null);
      return;
    }
    const X = i => pl + (cw * i) / (n - 1);
    const Y = v => pt + ch - Math.min(1, v / step / ymax) * ch;
    g.textAlign = 'center';
    g.textBaseline = 'alphabetic';
    const span = pts[n - 1].t - pts[0].t;
    const labels = Math.max(2, Math.floor(cw / 120));
    for (let k = 0; k <= labels; k++) {
      const i = Math.round((k * (n - 1)) / labels);
      const d = new Date(pts[i].t * 1000);
      const txt = span > 2 * 86400 ? `${pad(d.getDate())} ${MONTHS[d.getMonth()]}` + (span < 10 * 86400 ? ` ${pad(d.getHours())}h` : '')
        : span > 3600 ? `${pad(d.getHours())}:${pad(d.getMinutes())}` : `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
      g.fillText(txt, Math.max(pl + 24, Math.min(w - pr - 26, X(i))), h - 5);
    }
    const xs = pts.map((_, i) => X(i));
    for (const [key, token] of [['out', '--tx'], ['in', '--rx']]) {
      const col = cssColor(token);
      const ys = pts.map(p => Y(p[key]));
      const grad = g.createLinearGradient(0, pt, 0, pt + ch);
      grad.addColorStop(0, withAlpha(col, key === 'in' ? 0.26 : 0.2));
      grad.addColorStop(1, withAlpha(col, 0));
      g.beginPath();
      smoothPath(g, xs, ys);
      g.lineTo(xs[n - 1], pt + ch);
      g.lineTo(xs[0], pt + ch);
      g.closePath();
      g.fillStyle = grad;
      g.fill();
      g.beginPath();
      smoothPath(g, xs, ys);
      g.strokeStyle = col;
      g.lineWidth = 1.8;
      g.lineJoin = 'round';
      strokeGlow(g, col);
    }
    const card = cssColor('--card');
    const dot = (x, y, col, r) => {
      g.beginPath(); g.arc(x, y, r + 2.5, 0, Math.PI * 2); g.fillStyle = withAlpha(col, 0.18); g.fill();
      g.beginPath(); g.arc(x, y, r, 0, Math.PI * 2); g.fillStyle = col; g.fill();
      g.lineWidth = 1.5; g.strokeStyle = card; g.stroke();
    };
    const hi = this.hover;
    if (hi >= 0 && hi < n) {
      const x = Math.round(X(hi)) + 0.5;
      g.strokeStyle = cssColor('--hair-strong');
      g.lineWidth = 1;
      g.beginPath(); g.moveTo(x, pt); g.lineTo(x, pt + ch); g.stroke();
      dot(X(hi), Y(pts[hi].out), cssColor('--tx'), 3);
      dot(X(hi), Y(pts[hi].in), cssColor('--rx'), 3);
      this.showTip(pts[hi], X(hi), w);
    } else {
      if (step === 1) dot(X(n - 1), Y(pts[n - 1].in), cssColor('--rx'), 2.5);
      this.showTip(null);
    }
  }
  showTip(p, x, w) {
    if (!this.tip) return;
    if (!p) { this.tip.classList.remove('show'); return; }
    const step = this.step;
    const when = step > 1 ? `${isoTime(p.t)} · avg over ${step >= 3600 ? step / 3600 + ' h' : step / 60 + ' min'}` : isoTime(p.t);
    this.tip.innerHTML = `<div class="t">${esc(when)}</div><div class="r rx"><i></i>${fmtRateAlways(p.in / step)}</div><div class="r tx"><i></i>${fmtRateAlways(p.out / step)}</div>`;
    const tw = this.tip.offsetWidth || 160;
    const left = x + 14 + tw > w ? x - tw - 14 : x + 14;
    this.tip.style.left = Math.max(0, left) + 'px';
    this.tip.classList.add('show');
  }
}

const mainChart = new Chart($('#mainChart'), $('#mainTip'));
const appChart = new Chart($('#appChart'), $('#appTip'));

// ---------------------------------------------------------------- data loading

const isLive = () => state.period === 'session';
const PERIOD_LABEL = { session: 'This session', '1h': 'Last hour', '24h': 'Last 24 hours', '7d': 'Last 7 days', '30d': 'Last 30 days', all: 'All history' };
const periodLabel = () => PERIOD_LABEL[state.period] || 'This session';

async function loadLive() {
  const d = await api('/api/live');
  $('#rateIn').textContent = fmtRateAlways(d.rateIn);
  $('#rateOut').textContent = fmtRateAlways(d.rateOut);
  if (isLive()) {
    $('#totIn').textContent = `${fmtBytes(d.totalIn)} this session`;
    $('#totOut').textContent = `${fmtBytes(d.totalOut)} this session`;
    mainChart.set(d.series, 1);
  }
}
async function loadMainHistory() {
  if (isLive()) return;
  const d = await api('/api/series?period=' + state.period);
  let tin = 0, tout = 0;
  for (const p of d.points) { tin += p.in; tout += p.out; }
  const label = periodLabel().toLowerCase();
  $('#totIn').textContent = `${fmtBytes(tin)} · ${label}`;
  $('#totOut').textContent = `${fmtBytes(tout)} · ${label}`;
  mainChart.set(d.points, d.step);
}
async function loadApps() {
  const rows = await api('/api/apps?period=' + state.period);
  tables.apps.setRows(rows);
  if (state.app) {
    const r = rows.find(x => x.key === state.app);
    if (r) {
      $('#dSub').textContent = [r.path, r.pids && 'PID ' + r.pids].filter(Boolean).join('  ·  ');
      fillAppKpis(r);
    }
  }
}
async function loadFlows() {
  if (!state.app) return;
  const key = state.app;
  const rows = await api(`/api/flows?period=${state.period}&app=${encodeURIComponent(key)}`);
  if (key !== state.app) return;
  tables.flows.setRows(rows);
  $('#wrap-flows').classList.remove('stale');
}
async function loadAppChart() {
  if (!state.app) return;
  const key = state.app;
  const q = `app=${encodeURIComponent(key)}`;
  const d = isLive() ? { points: (await api('/api/live?' + q)).series, step: 1 } : await api(`/api/series?period=${state.period}&${q}`);
  if (key === state.app) appChart.set(d.points, d.step);
}
async function loadIPs() {
  tables.ips.setRows(await api('/api/ips?period=' + state.period));
  if (state.pendingExpand) {
    const ip = state.pendingExpand;
    state.pendingExpand = null;
    tables.ips.expandKey(ip);
  }
}
async function loadASNs() { tables.asns.setRows(await api('/api/asns?period=' + state.period)); }

async function loadStatus() {
  const s = await api('/api/status');
  state.status = s;
  const cap = s.capture || {};
  $('#host').textContent = s.hostname ? `${s.hostname} · ${s.os === 'darwin' ? 'macOS' : s.os}` : '';
  // Linux without root sees less; macOS (nettop) needs no root for bytes per app.
  const partial = cap.backend === 'sockdiag' && cap.mode === 'full' && !cap.privileged;
  let st = 'ok', text = 'Live capture · ' + String(cap.backend || '').toUpperCase();
  if (cap.backend === 'demo') { st = 'demo'; text = 'Demo data'; }
  else if (cap.backend === 'nettop' && cap.error) { st = 'bad'; text = 'Capture problem · hover for details'; }
  else if (partial) { st = 'warn'; text = 'Partial capture · run as root for UDP and DNS'; }
  else if (cap.mode === 'connections') { st = 'warn'; text = 'Connections only · no byte counts'; }
  else if (cap.mode !== 'full') { st = 'bad'; text = 'Capture stopped'; }
  const pill = $('#status');
  pill.dataset.state = st;
  $('#statusText').textContent = text;
  pill.title = [cap.detail, cap.error, ...(cap.notes || [])].filter(Boolean).join('\n');
  // The fix sits inline, next to the status it explains (not in a banner).
  $('#elevateBtn').hidden = !(cap.mode === 'connections' && s.canElevate);
  const geo = s.geo || {};
  $('#footGeo').textContent = geo.downloading ? (geo.message || 'Downloading the GeoIP databases…')
    : geo.city ? `GeoIP ${geo.cityDate || ''} · ASN ${geo.asnDate || ''}`
    : geo.error ? 'GeoIP download failed. Open Settings to retry.' : 'No GeoIP database. Open Settings to download one.';
  $('#footData').textContent = `History ${fmtBytes(s.dbSize)} · ${s.dataDir}`;
  if ($('#settings').open) fillSettings();
  maybeStartTour();
}

function fillSettings() {
  const s = state.status;
  if (!s) return;
  const cap = s.capture || {};
  const geo = s.geo || {};
  const rows = list => list.map(([k, v]) => `<dt>${esc(k)}</dt><dd>${esc(v)}</dd>`).join('');
  $('#statusList').innerHTML = rows([
    ['Source', `${cap.backend} · ${cap.mode} mode${cap.privileged ? ' · elevated' : ''}`],
    ['Details', cap.detail || '–'],
    ['Events', `${(cap.events || 0).toLocaleString()} captured · ${(cap.lost || 0).toLocaleString()} lost`],
    ['DNS answers', (s.dnsEvents || 0).toLocaleString()],
    ['Loopback', s.loopback ? 'Included' : 'Excluded (start with --loopback to include)'],
    ['Warnings', cap.error || 'None'],
  ]);
  $('#storageList').innerHTML = rows([
    ['Data folder', s.dataDir],
    ['History', `${s.dbPath} · ${fmtBytes(s.dbSize)}`],
    ['Running since', isoTime(s.start)],
  ]);
  $('#aboutVersion').textContent = `Version ${s.version} · ${s.os}/${s.arch}`;
  if (s.copyright) $('#aboutCopy').textContent = s.copyright;
  let g = geo.city ? `City ${geo.cityDate} · ASN ${geo.asnDate}` : 'Not installed';
  if (geo.downloading) g = geo.message || 'Downloading…';
  else if (geo.error) g += ` · last update failed: ${geo.error}`;
  else if (geo.message) g += ` · ${geo.message}`;
  $('#geoStatus').textContent = g;
  $('#geoUpdate').disabled = !!geo.downloading;
  const notes = cap.notes || [];
  $('#notesBox').hidden = !notes.length;
  $('#notes').innerHTML = notes.map(n => `<li>${esc(n)}</li>`).join('');
}

// ---------------------------------------------------------------- monitors (pinned latency targets)

const monState = { list: [], cards: new Map(), method: 'icmp', interval: '2' };

function monStats(m, series) {
  // Live: the server's last-5-minutes stats. History: summarise the series.
  if (isLive() || !series) return { last: m.last, avg: m.recv ? m.avg : -1, min: m.recv ? m.min : -1, max: m.recv ? m.max : -1, jitter: m.recv > 1 ? m.jitter : -1, loss: m.sent ? m.loss : -1, sent: m.sent };
  let sent = 0, recv = 0, sum = 0, mn = Infinity, mx = 0;
  for (const p of series.points) {
    sent += p.sent; recv += p.recv; sum += p.avg * p.recv;
    if (p.recv) { mn = Math.min(mn, p.min); mx = Math.max(mx, p.max); }
  }
  return { last: m.last, avg: recv ? sum / recv : -1, min: recv ? mn : -1, max: recv ? mx : -1, jitter: -1, loss: sent ? 100 * (sent - recv) / sent : -1, sent };
}

// The height a chart canvas was given in the markup, in CSS pixels. Drawing
// sets canvas.height to that times devicePixelRatio, which rewrites the height
// attribute, so the original is remembered on first use. Reading the attribute
// back on every draw made charts grow without end on any screen whose
// devicePixelRatio isn't 1 (Retina Macs, Windows display scaling).
function canvasHeight(canvas) {
  if (!canvas.dataset.h) canvas.dataset.h = canvas.getAttribute('height') || 40;
  return +canvas.dataset.h;
}

function drawMonChart(canvas, series, readout) {
  const dpr = window.devicePixelRatio || 1;
  const w = canvas.clientWidth, h = canvasHeight(canvas);
  if (!w) return;
  canvas.style.height = h + 'px';
  canvas.width = Math.round(w * dpr);
  canvas.height = Math.round(h * dpr);
  const g = canvas.getContext('2d');
  g.setTransform(dpr, 0, 0, dpr, 0, 0);
  g.clearRect(0, 0, w, h);
  const pts = (series && series.points) || [];
  const pl = 44, pr = 4, pt = 4, pb = 14, cw = w - pl - pr, ch = h - pt - pb;
  const now = Math.floor(Date.now() / 1000);
  const t1 = series && series.step ? now : (pts.length ? pts[pts.length - 1].t : now);
  const t0 = series && series.step ? series.since : (pts.length ? Math.min(pts[0].t, t1 - 60) : t1 - 600);
  const span = Math.max(60, t1 - t0);
  let max = 0;
  for (const p of pts) if (p.recv) max = Math.max(max, p.max);
  max = max <= 0 ? 10 : [1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000].find(v => v >= max * 1.1) || max * 1.2;
  const X = t => pl + ((t - t0) / span) * cw;
  const Y = v => pt + ch - Math.min(1, v / max) * ch;
  g.font = '500 11px ' + getComputedStyle(document.body).fontFamily;
  g.fillStyle = cssColor('--text-3');
  g.strokeStyle = cssColor('--grid');
  g.textAlign = 'right';
  g.textBaseline = 'middle';
  for (const f of [0, 0.5, 1]) {
    const y = Math.round(Y(max * f)) + 0.5;
    g.setLineDash(f === 0 ? [] : [2, 4]);
    g.beginPath(); g.moveTo(pl, y); g.lineTo(w - pr, y); g.stroke();
    if (f > 0) g.fillText(`${fmtMs(max * f)} ms`, pl - 6, y);
  }
  g.setLineDash([]);
  g.textAlign = 'left';
  g.textBaseline = 'alphabetic';
  g.fillText(isLive() ? 'last ~10 min' : periodLabel().toLowerCase(), pl, h - 2);
  if (!pts.length) {
    g.textAlign = 'center';
    g.fillText('Waiting for the first probes…', pl + cw / 2, pt + ch / 2);
    return;
  }
  const rx = cssColor('--rx'), bad = cssColor('--bad');
  const step = series.step || (pts.length > 1 ? Math.max(1, pts[1].t - pts[0].t) : 2);
  const bw = Math.max(1.5, (step / span) * cw);
  // min–max band per bucket, then the average line, then loss marks
  g.fillStyle = withAlpha(rx, 0.22);
  for (const p of pts) {
    if (!p.recv) continue;
    const x = X(p.t), y1 = Y(p.max), y2 = Y(p.min);
    g.fillRect(x, y1, Math.max(1, bw - 0.5), Math.max(1.5, y2 - y1));
  }
  g.beginPath();
  let pen = false;
  for (const p of pts) {
    if (!p.recv) { pen = false; continue; }
    const x = X(p.t) + bw / 2, y = Y(p.avg);
    pen ? g.lineTo(x, y) : g.moveTo(x, y);
    pen = true;
  }
  g.strokeStyle = rx;
  g.lineWidth = 1.5;
  g.lineJoin = 'round';
  strokeGlow(g, rx, 6);
  g.fillStyle = bad;
  for (const p of pts) {
    if (p.sent && p.recv < p.sent) {
      const f = (p.sent - p.recv) / p.sent;
      const hh = Math.max(3, f * ch * 0.6);
      g.fillRect(X(p.t), pt + ch - hh, Math.max(1.5, bw - 0.5), hh);
    }
  }
  if (readout) {
    canvas.onmousemove = e => {
      const r = canvas.getBoundingClientRect();
      const t = t0 + ((e.clientX - r.left - pl) / cw) * span;
      let best = null;
      for (const p of pts) if (!best || Math.abs(p.t - t) < Math.abs(best.t - t)) best = p;
      if (!best) return;
      const loss = best.sent ? (100 * (best.sent - best.recv)) / best.sent : 0;
      readout.textContent = `${fmtTime(best.t)} · ${best.recv ? fmtMs(best.avg) + ' ms' + (best.max > best.min ? ` (${fmtMs(best.min)}–${fmtMs(best.max)})` : '') : 'no reply'}${loss ? ` · ${loss.toFixed(0)}% loss` : ''}`;
    };
    canvas.onmouseleave = () => { readout.textContent = ''; };
  }
}

function monCard(m) {
  const el = document.createElement('div');
  el.className = 'mon-card';
  el.dataset.id = m.id;
  el.innerHTML = `
    <div class="mon-id">
      <span class="mon-dot"></span>
      <div class="mon-text"><div class="mon-name"></div><div class="mon-target"></div></div>
    </div>
    <div class="mon-stats"></div>
    <div class="mon-chartwrap"><canvas class="mon-chart" height="64"></canvas><div class="mon-readout"></div></div>
    <div class="mon-actions">
      <button type="button" class="icon-btn plain" data-act="test" title="Run MTR to this target (Tools tab)" aria-label="Run MTR to this target">
        <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><circle cx="3" cy="12.5" r="1.6" fill="currentColor"/><circle cx="8" cy="7.5" r="1.6" fill="currentColor"/><circle cx="13" cy="3.5" r="1.6" fill="currentColor"/><path d="M4.2 11.3 6.8 8.7M9.3 6.5l2.4-2" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg>
      </button>
      <button type="button" class="icon-btn plain" data-act="toggle"></button>
      <button type="button" class="icon-btn plain" data-act="remove" title="Remove monitor and its history" aria-label="Remove monitor">
        <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="m4 4 8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>
      </button>
    </div>`;
  el.addEventListener('click', e => monAction(e, m.id));
  new ResizeObserver(() => drawMonChart($('.mon-chart', el), el._series, $('.mon-readout', el))).observe($('.mon-chart', el));
  return el;
}

const PAUSE_IC = '<svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M5.5 3.5v9M10.5 3.5v9" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/></svg>';
const PLAY_IC = '<svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M5 3.5v9l7-4.5Z" fill="currentColor"/></svg>';

function fillMonCard(el, m) {
  const st = monStats(m, el._series);
  const health = m.enabled ? m.health : 'idle';
  $('.mon-dot', el).className = 'mon-dot ' + health;
  $('.mon-name', el).textContent = m.name;
  const method = m.method === 'tcp' ? `TCP ${m.port}` : 'Ping';
  $('.mon-target', el).textContent = `${m.target}${m.addr && m.addr !== m.target ? ' → ' + m.addr : ''} · ${method} every ${m.interval} s${m.enabled ? '' : ' · paused'}${m.error ? ' · ' + m.error : ''}`;
  const lossCls = st.loss >= 5 ? 'bad' : st.loss > 0 ? 'warn' : '';
  const cell = (k, v, cls) => `<div class="ms"><span class="lbl">${k}</span><span class="val ${cls || ''}">${v}</span></div>`;
  const html = cell('Last', m.last >= 0 ? fmtMs(m.last) + ' ms' : m.sent ? '<span class="q bad">lost</span>' : '–')
    + cell('Avg', st.avg >= 0 ? fmtMs(st.avg) + ' ms' : '–')
    + cell('Min / Max', st.min >= 0 ? `${fmtMs(st.min)} / ${fmtMs(st.max)}` : '–')
    + (isLive() ? cell('Jitter', st.jitter >= 0 ? fmtMs(st.jitter) + ' ms' : '–') : '')
    + cell('Loss', st.loss >= 0 ? st.loss.toFixed(1) + '%' : '–', lossCls);
  const stats = $('.mon-stats', el);
  if (stats._h !== html) { stats.innerHTML = html; stats._h = html; }
  const tg = $('[data-act="toggle"]', el);
  tg.innerHTML = m.enabled ? PAUSE_IC : PLAY_IC;
  tg.title = m.enabled ? 'Pause' : 'Resume';
  tg.setAttribute('aria-label', tg.title);
}

async function monAction(e, id) {
  const b = e.target.closest('button[data-act]');
  if (!b) return;
  const m = monState.list.find(x => x.id === id);
  if (!m) return;
  if (b.dataset.act === 'test') {
    if (m.method === 'tcp') testTarget(m.target, 'tcp', m.port);
    else testTarget(m.target, 'mtr');
    return;
  }
  try {
    if (b.dataset.act === 'toggle') {
      await post(`/api/monitors/enable?id=${id}&on=${m.enabled ? 0 : 1}`);
    } else if (b.dataset.act === 'remove') {
      // Two-step confirm: removal deletes the monitor's history.
      if (!b.classList.contains('confirm')) {
        b.classList.add('confirm');
        b.title = 'Click again to remove (deletes its history)';
        toast(`Click ✕ again to remove "${m.name}" and its history`);
        setTimeout(() => { b.classList.remove('confirm'); b.title = 'Remove monitor and its history'; }, 4000);
        return;
      }
      await post(`/api/monitors/delete?id=${id}`);
      toast(`Removed "${m.name}"`);
    }
  } catch (err) { toast(err.message); }
  invalidate('monlist', 'monseries');
}

async function addMonitor(body) {
  try {
    const m = await post('/api/monitors', JSON.stringify(body), 'application/json');
    toast(`Monitoring ${m.name}`, state.tab === 'monitors' ? '' : 'Open', () => setTab('monitors'));
    invalidate('monlist', 'monseries');
    return true;
  } catch (err) {
    toast(err.message);
    return false;
  }
}

async function loadMonitors() {
  const d = await api('/api/monitors');
  monState.list = d.monitors || [];
  renderChips();
  if (state.tab !== 'monitors') return;
  const list = $('#monList');
  const seen = new Set();
  for (const m of monState.list) {
    seen.add(m.id);
    let el = monState.cards.get(m.id);
    if (!el) { el = monCard(m); monState.cards.set(m.id, el); }
    if (el.parentNode !== list) list.appendChild(el);
    fillMonCard(el, m);
  }
  for (const [id, el] of monState.cards) if (!seen.has(id)) { el.remove(); monState.cards.delete(id); }
  $('#monEmpty').hidden = monState.list.length > 0;
  const sug = d.suggest || [];
  const sugHtml = sug.length
    ? `<span class="muted">Suggested:</span>` + sug.map(s => `<button type="button" class="chip-btn" data-target="${esc(s.target)}" data-name="${esc(s.name)}"><span class="plus">+</span>${esc(s.name)}${s.name.includes(s.target) ? '' : ` <span class="mono-sm">${esc(s.target)}</span>`}</button>`).join('')
    : '';
  const box = $('#monSuggest');
  if (box._h !== sugHtml) { box.innerHTML = sugHtml; box._h = sugHtml; }
  renderMonAll(sug.length > 0);
}

// "Pause all" while anything is probing, "Resume all" once everything is paused.
function renderMonAll(hasSuggestions) {
  const total = monState.list.length;
  const running = monState.list.filter(m => m.enabled).length;
  $('#monBar').hidden = !total && !hasSuggestions;
  $('#monCount').textContent = !total ? '' : running === total ? `${total} running` : running ? `${running} of ${total} running` : 'All paused';
  const b = $('#monAll');
  b.hidden = !total;
  if (!total || b.disabled) return;
  const pause = running > 0;
  const html = (pause ? PAUSE_IC : PLAY_IC) + (pause ? 'Pause all' : 'Resume all');
  if (b._h !== html) { b.innerHTML = html; b._h = html; }
  b.dataset.on = pause ? '0' : '1';
  b.title = pause ? 'Stop probing every monitor. History is kept.' : 'Start probing every monitor again';
}

async function setAllMonitors(b) {
  const on = b.dataset.on === '1';
  b.disabled = true;
  try {
    const r = await post(`/api/monitors/enable-all?on=${on ? 1 : 0}`);
    const n = r.changed || 0;
    toast(on ? `Resumed ${n} monitor${n === 1 ? '' : 's'}` : `Paused ${n} monitor${n === 1 ? '' : 's'}. Their history is kept.`);
  } catch (err) { toast(err.message); }
  b.disabled = false;
  invalidate('monlist', 'monseries');
}

async function loadMonSeries() {
  if (state.tab !== 'monitors') return;
  await Promise.all(monState.list.map(async m => {
    const el = monState.cards.get(m.id);
    if (!el) return;
    try {
      el._series = await api(`/api/monitors/series?id=${m.id}&period=${state.period}`);
    } catch { return; }
    fillMonCard(el, m);
    drawMonChart($('.mon-chart', el), el._series, $('.mon-readout', el));
  }));
}

function renderChips() {
  const chips = monState.list.filter(m => m.enabled).slice(0, 4);
  const html = chips.map(m => {
    const v = m.last >= 0 ? `${fmtMs(m.last)} ms` : m.sent ? 'lost' : '…';
    return `<button type="button" class="mon-chip ${m.health}" data-mon="${m.id}" title="${esc(m.target)} · ${m.loss.toFixed(1)}% loss (5 min)"><i></i><span class="n">${esc(m.name)}</span><b>${v}</b></button>`;
  }).join('');
  const box = $('#monChips');
  if (box._h !== html) { box.innerHTML = html; box._h = html; }
}

function initMonitorsUI() {
  initSeg($('#monMethod'), v => {
    monState.method = v;
    setSeg($('#monMethod'), v);
    $('#monPortField').hidden = v !== 'tcp';
  });
  setSeg($('#monMethod'), 'icmp');
  initSeg($('#monInterval'), v => { monState.interval = v; setSeg($('#monInterval'), v); });
  setSeg($('#monInterval'), monState.interval);
  const submit = async () => {
    const body = {
      target: $('#monTarget').value.trim(),
      name: $('#monName').value.trim(),
      method: monState.method,
      port: monState.method === 'tcp' ? parseInt($('#monPort').value, 10) || 0 : 0,
      interval: parseInt(monState.interval, 10) || 2,
    };
    if (!body.target) { toast('Enter an IP address or hostname'); $('#monTarget').focus(); return; }
    if (await addMonitor(body)) { $('#monTarget').value = ''; $('#monName').value = ''; }
  };
  $('#monAdd').addEventListener('click', submit);
  for (const id of ['#monTarget', '#monName', '#monPort']) $(id).addEventListener('keydown', e => { if (e.key === 'Enter') submit(); });
  $('#monSuggest').addEventListener('click', e => {
    const b = e.target.closest('button[data-target]');
    if (b) addMonitor({ target: b.dataset.target, name: b.dataset.name, method: 'icmp', interval: 2 });
  });
  $('#monChips').addEventListener('click', e => { if (e.target.closest('.mon-chip')) setTab('monitors'); });
  $('#monAll').addEventListener('click', e => setAllMonitors(e.currentTarget));
}

// ---------------------------------------------------------------- tools tab
// MTR, traceroute, ping and TCP checks to any address, not only ones in the
// tables. Reuses the IP panel, with the diagnostics card near the top.

const TOOL_SKELETON = `
  <div class="xgrid">
    <div class="xtop" data-sec="head"></div>
    <div class="xkpis" data-sec="stats"></div>
    <div class="xdiag"></div>
    <div class="xcol" data-sec="facts"></div>
    <div class="xcol"><div class="xtitle">Applications talking to this IP</div><div data-sec="apps"></div></div>
  </div>`;
const toolState = { target: null, ip: '', seq: 0 };

function renderToolRecent() {
  const list = saved.get('toolRecent', []);
  const box = $('#toolRecent');
  box.hidden = !list.length;
  const html = list.length
    ? `<div class="mon-suggest"><span class="muted">Recent:</span>${list.map(q => `<button type="button" class="chip-btn mono-chip" data-tool-q="${esc(q)}">${esc(q)}</button>`).join('')}</div><button type="button" class="btn sm" data-tool-clear>Clear</button>`
    : '';
  if (box._h !== html) { box.innerHTML = html; box._h = html; }
}
function rememberTool(q) {
  const list = saved.get('toolRecent', []).filter(x => x !== q);
  list.unshift(q);
  saved.set('toolRecent', list.slice(0, 8));
  renderToolRecent();
}

// Resolve what was typed, open the panel for it and start the last-used test.
async function runTool(q, opts = {}) {
  q = (q || '').trim();
  if (!q) { toast('Enter an IP address or hostname'); $('#toolTarget').focus(); return; }
  const seq = ++toolState.seq;
  const go = $('#toolGo');
  go.disabled = true;
  let t;
  try {
    t = await api('/api/resolve?q=' + encodeURIComponent(q));
  } catch (e) {
    if (seq === toolState.seq) toast(e.message);
    return;
  } finally {
    if (seq === toolState.seq) go.disabled = false;
  }
  if (seq !== toolState.seq) return;
  rememberTool(q);
  toolState.target = t;
  if (opts.kind) saved.set('diagKind', opts.kind);
  await openToolIP(t.addrs[0], opts.port || t.port, true);
}

function renderToolResolved() {
  const t = toolState.target, box = $('#toolResolved');
  if (!t || !t.host) { box.hidden = true; box.innerHTML = ''; return; }
  box.hidden = false;
  box.innerHTML = `<span class="mono">${esc(t.host)}</span><span class="muted">resolves to</span>`
    + t.addrs.map(a => `<button type="button" class="chip-btn${a === toolState.ip ? ' on' : ''}" data-tool-ip="${esc(a)}" aria-pressed="${a === toolState.ip}">${esc(a)}</button>`).join('')
    + (t.addrs.length > 1 ? '<span class="muted">Click another address to test it instead.</span>' : '');
}

async function openToolIP(ip, port, autostart) {
  const panel = $('#toolPanel');
  if (panel._diag) panel._diag.stop(true);
  toolState.ip = ip;
  renderToolResolved();
  let d;
  try {
    d = await api(`/api/ip?period=${state.period}&ip=${encodeURIComponent(ip)}`);
  } catch (e) { toast('Could not load details: ' + e.message); return; }
  if (toolState.ip !== ip) return;
  if (panel._diag) panel._diag.stop(true);
  $('#toolEmpty').hidden = true;
  panel.hidden = false;
  panel.innerHTML = TOOL_SKELETON;
  panel._built = true;
  const out = (d.apps || []).find(a => a.dir === 'out');
  panel._diag = new Diag($('.xdiag', panel), d.ip, port || (out ? out.port : 443));
  fillToolPanel(d);
  panel.querySelectorAll('.seg').forEach(seg => { placeThumb(seg); setTimeout(() => { placeThumb(seg); seg.classList.add('ready'); }, 60); });
  if (autostart) panel._diag.start();
}

function fillToolPanel(d) {
  const panel = $('#toolPanel');
  const out = renderIP(d);
  const seen = d.total > 0 || d.rateIn > 0 || d.rateOut > 0 || (d.apps || []).length > 0;
  if (!seen) {
    out.sections.stats = '';
    out.sections.apps = `<span class="muted">No traffic between this PC and ${esc(d.ip)} in the selected period.</span>`;
  }
  for (const [name, html] of Object.entries(out.sections)) {
    const el = panel.querySelector(`[data-sec="${name}"]`);
    if (el && el._h !== html) { el.innerHTML = html; el._h = html; }
  }
  $('[data-sec="stats"]', panel).hidden = !seen;
  panel._text = out.text;
}

async function refreshTool() {
  const ip = toolState.ip, panel = $('#toolPanel');
  if (!ip || panel.hidden || selectionIn(panel)) return;
  const d = await api(`/api/ip?period=${state.period}&ip=${encodeURIComponent(ip)}`);
  if (toolState.ip === ip) fillToolPanel(d); // reverse DNS arrives a moment after the first look
}

// Open the Tools tab on an address and start a test there.
function testTarget(q, kind, port) {
  setTab('tools');
  $('#toolTarget').value = q;
  requestAnimationFrame(() => runTool(q, { kind, port }));
}

function initToolsUI() {
  renderToolRecent();
  $('#toolForm').addEventListener('submit', e => { e.preventDefault(); runTool($('#toolTarget').value); });
  $('#toolRecent').addEventListener('click', e => {
    const b = e.target.closest('[data-tool-q]');
    if (b) { $('#toolTarget').value = b.dataset.toolQ; runTool(b.dataset.toolQ); return; }
    if (e.target.closest('[data-tool-clear]')) { saved.set('toolRecent', []); renderToolRecent(); }
  });
  $('#toolResolved').addEventListener('click', e => {
    const b = e.target.closest('[data-tool-ip]');
    if (b && b.dataset.toolIp !== toolState.ip) openToolIP(b.dataset.toolIp, toolState.target && toolState.target.port, true);
  });
}

// ---------------------------------------------------------------- traffic colours
// Download / upload colours for Settings → Appearance (download first). Each
// pair is graded like a film look (blockbuster blue/orange, phosphor green,
// neon-noir cyan/magenta, frost…) but named by its colours. Derived in OKLCH.
//   line: [light, dark] for lines, dots and fills: vivid, 3:1 or more on white
//         except gold (2.6:1; a 3:1 yellow turns brown).
//   ink:  light-mode shade for small text, 4.5:1 or more on white (dark mode
//         uses the line colour, which already passes).
// "Custom" takes two exact colours, used as-is everywhere in both modes.
const TRAFFIC_COLOURS = {
  blue: { line: ['#0c7cf4', '#43afff'], ink: '#006cd8' },
  orange: { line: ['#f16a00', '#ff9c28'], ink: '#c35200' },
  phosphor: { line: ['#00a82d', '#58fa6a'], ink: '#097f23' },
  gold: { line: ['#c89a00', '#ffdc20'], ink: '#976c00' },
  emerald: { line: ['#00a263', '#30de97'], ink: '#007c4b' },
  crimson: { line: ['#e20c35', '#ff3b4d'], ink: '#c5002b' },
  signal: { line: ['#2b62ef', '#5492ff'], ink: '#1d52de' },
  cyan: { line: ['#00a0ba', '#00f0ff'], ink: '#007e96' },
  magenta: { line: ['#d900c0', '#ff2ce3'], ink: '#bc00a6' },
  acid: { line: ['#6ba300', '#bafa35'], ink: '#4e7b00' },
  violet: { line: ['#8c34e9', '#b77bff'], ink: '#7b2bcf' },
  deep: { line: ['#1f5ed9', '#4886fe'], ink: '#1251cc' },
  ice: { line: ['#4c9cba', '#c4f0ff'], ink: '#346b84' },
  purple: { line: ['#6a43c4', '#b99cff'] },
  teal: { line: ['#00a598', '#41dfcf'] },
  lavender: { line: ['#9477dd', '#cebfff'] },
};
// A preset is a whole data palette: download and upload (a, b), plus the two
// System-tab colours: s3 for memory and kernel time, s4 for TCP retransmits.
// CPU follows download and disk follows upload.
const preset = (id, name, a, b, s3, s4) => {
  const [x, y] = [TRAFFIC_COLOURS[a], TRAFFIC_COLOURS[b]];
  return { id, name, rx: x.line, tx: y.line, ink: [x.ink, y.ink], s3: TRAFFIC_COLOURS[s3].line, s4: TRAFFIC_COLOURS[s4].line };
};
const PALETTES = [
  preset('classic', 'Blue & orange', 'blue', 'orange', 'purple', 'teal'),
  preset('greenyellow', 'Green & yellow', 'phosphor', 'gold', 'cyan', 'orange'),
  preset('greenred', 'Green & red', 'emerald', 'crimson', 'gold', 'signal'),
  preset('bluered', 'Blue & red', 'signal', 'crimson', 'violet', 'gold'),
  preset('neon', 'Cyan & magenta', 'cyan', 'magenta', 'gold', 'violet'),
  preset('acid', 'Lime & purple', 'acid', 'violet', 'cyan', 'magenta'),
  preset('frost', 'Blue & ice', 'deep', 'ice', 'lavender', 'cyan'),
];
const HEX = /^#?([0-9a-f]{6})$/i;
function customPalette() {
  const ok = (v, d) => (HEX.test(v || '') ? '#' + HEX.exec(v)[1].toLowerCase() : d);
  const rx = ok(saved.get('customRx', ''), '#43afff'), tx = ok(saved.get('customTx', ''), '#ff9c28');
  // The System-tab colours keep the default palette's purple and teal.
  return { id: 'custom', name: 'Custom', rx: [rx, rx], tx: [tx, tx], ink: [rx, tx], s3: TRAFFIC_COLOURS.purple.line, s4: TRAFFIC_COLOURS.teal.line };
}
const allPalettes = () => [...PALETTES, customPalette()];
const pair = c => (c[0] === c[1] ? c[0] : `light-dark(${c[0]}, ${c[1]})`);
function applyPalette(id) {
  const p = allPalettes().find(x => x.id === id) || PALETTES[0];
  state.palette = p.id;
  const st = document.documentElement.style;
  const vars = ['--rx', '--tx', '--rx-ink', '--tx-ink', '--s3', '--s4'];
  if (p.id === 'classic') { vars.forEach(v => st.removeProperty(v)); return; } // the stylesheet's own values
  st.setProperty('--rx', pair(p.rx));
  st.setProperty('--tx', pair(p.tx));
  st.setProperty('--rx-ink', pair([p.ink[0], p.rx[1]]));
  st.setProperty('--tx-ink', pair([p.ink[1], p.tx[1]]));
  st.setProperty('--s3', pair(p.s3));
  st.setProperty('--s4', pair(p.s4));
}
// WCAG contrast of a hex colour against a hex background.
function contrast(a, b) {
  const lum = h => {
    const c = [1, 3, 5].map(i => parseInt(h.slice(i, i + 2), 16) / 255).map(v => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
    return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
  };
  const [x, y] = [lum(a), lum(b)].sort((m, n) => n - m);
  return (x + 0.05) / (y + 0.05);
}
function setGlow(on) {
  state.glow = on;
  document.documentElement.classList.toggle('no-glow', !on);
}
// Canvas charts read the colours when they draw, so redraw them after a change.
function redrawCharts() {
  mainChart.draw();
  appChart.draw();
  sysChart.draw();
  if (sysState.data && sysState.data.ready) renderSysTiles(sysState.data);
  invalidate('monseries');
}
// A mini traffic chart: download area and line behind, upload in front.
const PAL_A = 'M0 20 C 12 20, 16 5, 30 8 S 50 21, 64 12 S 86 3, 100 6';
const PAL_B = 'M0 31 C 16 31, 22 22, 38 25 S 66 31, 78 23 S 92 24, 100 21';
const PAL_PREVIEW = `<svg viewBox="0 0 100 38" preserveAspectRatio="none" aria-hidden="true">
  <path class="fa" d="${PAL_A} L100 38 L0 38 Z"/><path class="a" vector-effect="non-scaling-stroke" d="${PAL_A}"/>
  <path class="fb" d="${PAL_B} L100 38 L0 38 Z"/><path class="b" vector-effect="non-scaling-stroke" d="${PAL_B}"/></svg>`;
function renderPalettes() {
  $('#paletteGrid').innerHTML = allPalettes().map(p => `<button type="button" role="radio" class="pal" data-pal="${p.id}" aria-checked="${p.id === state.palette}" style="--a: ${pair(p.rx)}; --b: ${pair(p.tx)}; --c: ${pair(p.s3)}; --d: ${pair(p.s4)}">
      ${PAL_PREVIEW}<span class="pal-foot"><span class="pal-name">${esc(p.name)}</span><span class="pal-dots" title="Download, upload, memory, TCP"><i></i><i></i><i></i><i></i></span></span></button>`).join('');
  const c = customPalette();
  $('#pcRx').value = $('#pcRxHex').value = c.rx[0];
  $('#pcTx').value = $('#pcTxHex').value = c.tx[0];
  $('#palCustom').hidden = state.palette !== 'custom';
  customNote();
  $('#glowToggle').checked = state.glow !== false;
}
// Inline hint when a custom colour would be hard to read as text.
function customNote() {
  const c = customPalette(), bad = [];
  for (const [label, hex] of [['Download', c.rx[0]], ['Upload', c.tx[0]]]) {
    const light = contrast(hex, '#ffffff') < 4.5, dark = contrast(hex, '#17181c') < 4.5;
    if (light || dark) bad.push(`${label} may be hard to read in ${light && dark ? 'both modes' : light ? 'light mode' : 'dark mode'}`);
  }
  $('#pcNote').textContent = bad.join(' · ');
}
let paletteRedraw = 0;
function choosePalette(id) {
  applyPalette(id);
  saved.set('palette', state.palette);
  $$('#paletteGrid [data-pal]').forEach(x => x.setAttribute('aria-checked', String(x.dataset.pal === state.palette)));
  $('#palCustom').hidden = state.palette !== 'custom';
  cancelAnimationFrame(paletteRedraw); // a colour picker fires many times while dragging
  paletteRedraw = requestAnimationFrame(redrawCharts);
}
$('#paletteGrid').addEventListener('click', e => {
  const b = e.target.closest('[data-pal]');
  if (b) choosePalette(b.dataset.pal);
});
// Custom colours: the swatch opens the system colour picker; the field takes a hex code.
function setCustom(which, value, from) {
  const m = HEX.exec(value.trim());
  if (!m) return false;
  const hex = '#' + m[1].toLowerCase();
  saved.set(which === 'rx' ? 'customRx' : 'customTx', hex);
  const pick = which === 'rx' ? $('#pcRx') : $('#pcTx'), field = which === 'rx' ? $('#pcRxHex') : $('#pcTxHex');
  if (from !== pick) pick.value = hex;
  if (from !== field) field.value = hex;
  const card = $('#paletteGrid [data-pal="custom"]');
  if (card) card.style.setProperty(which === 'rx' ? '--a' : '--b', hex);
  customNote();
  choosePalette('custom');
  return true;
}
for (const which of ['rx', 'tx']) {
  const pick = which === 'rx' ? $('#pcRx') : $('#pcTx'), field = which === 'rx' ? $('#pcRxHex') : $('#pcTxHex');
  pick.addEventListener('input', () => setCustom(which, pick.value, pick));
  field.addEventListener('input', () => field.classList.toggle('bad', !setCustom(which, field.value, field) && field.value.trim().length >= 6));
  field.addEventListener('blur', () => { field.value = customPalette()[which][0]; field.classList.remove('bad'); });
}
$('#glowToggle').addEventListener('change', e => {
  setGlow(e.target.checked);
  saved.set('glow', state.glow);
  redrawCharts();
});

// ---------------------------------------------------------------- inspector
// One right-hand panel shared by Remote IPs, Networks and Peering. A table in
// sheet mode hands it a filled panel; the table stays usable on the left.
const inspector = {
  owner: null,
  clearTimer: 0,
  show(table, panel) {
    if (this.owner && this.owner !== table) { const o = this.owner; this.owner = null; o.collapse(); }
    if (table === tables.peers) closeRouteSheet();
    this.owner = table;
    clearTimeout(this.clearTimer);
    const body = $('#inspBody');
    const old = body.firstElementChild;
    if (old && old !== panel && old._diag) old._diag.stop(true);
    body.replaceChildren(panel);
    body.scrollTop = 0;
    const sheet = $('#inspector');
    placeSheet();
    sheet.classList.add('open');
    sheet.setAttribute('aria-hidden', 'false');
    panel.querySelectorAll('.seg').forEach(seg => { placeThumb(seg); setTimeout(() => { placeThumb(seg); seg.classList.add('ready'); }, 60); });
  },
  close(table) {
    if (table && this.owner !== table) return;
    this.owner = null;
    const body = $('#inspBody');
    const p = body.firstElementChild;
    if (p && p._diag) p._diag.stop(true);
    const sheet = $('#inspector');
    sheet.classList.remove('open');
    sheet.setAttribute('aria-hidden', 'true');
    // empty it once it has slid away, so the content doesn't vanish mid-slide
    clearTimeout(this.clearTimer);
    this.clearTimer = setTimeout(() => { if (!this.owner) body.replaceChildren(); }, 320);
  },
};
$('#inspClose').addEventListener('click', () => (inspector.owner ? inspector.owner.collapse() : inspector.close()));

// Light dismiss: a click anywhere outside an open side panel closes it.
// Exceptions: the table that owns the panel (its rows switch or close it, its
// headers re-sort), the top toolbar (period, units and theme change what the panel
// shows), and anything layered above (dialogs, toasts, the tour).
document.addEventListener('click', e => {
  const t = e.target;
  if (!(t instanceof Element) || !t.isConnected) return;
  const sel = window.getSelection();
  if (sel && !sel.isCollapsed) return; // the click ended a text selection
  if (t.closest('.chrome, dialog, .toast, #tour')) return;
  // rows switch the panel, headers re-sort it: both keep it open
  const ownRow = table => table.el.contains(t);
  if (inspector.owner && !t.closest('#inspector') && !ownRow(inspector.owner)) inspector.owner.collapse();
  if (state.app && $('#detail').classList.contains('open') && !t.closest('#detail') && !ownRow(tables.apps)) closeApp();
  if ($('#routeSheet').classList.contains('open') && !t.closest('#routeSheet, #routeOpen')) closeRouteSheet();
});

// ---------------------------------------------------------------- help

let helpLock = '', helpLockTimer;
function openHelp(topic) {
  const d = $('#help');
  const s = state.status;
  if (s) {
    $('#helpVersion').textContent = `version ${s.version} · ${s.os}/${s.arch}`;
    if (s.copyright) $('#helpCopy').textContent = s.copyright;
  }
  if (!d.open) d.showModal();
  if (topic) showHelpTopic(topic, false);
  else helpSpy();
}
function showHelpTopic(id, smooth = true) {
  const sec = $('#help-' + id);
  if (!sec) return;
  // Keep the clicked topic highlighted until the scroll settles, even when
  // a short last topic can't reach the top of the pane.
  const box = $('#helpContent');
  const unlock = () => { helpLock = ''; clearTimeout(helpLockTimer); };
  helpLock = id;
  clearTimeout(helpLockTimer);
  helpLockTimer = setTimeout(unlock, 1500);
  box.addEventListener('scrollend', unlock, { once: true });
  const top = Math.min(sec.offsetTop - 4, box.scrollHeight - box.clientHeight);
  if (Math.abs(box.scrollTop - top) < 1) unlock();
  else box.scrollTo({ top, behavior: smooth ? 'smooth' : 'instant' });
  markHelpNav(id);
}
function markHelpNav(cur) {
  $$('#helpNav [data-help]').forEach(b => b.setAttribute('aria-current', String(b.dataset.help === cur)));
}
// Highlight the topic being read.
function helpSpy() {
  if (helpLock) return;
  const box = $('#helpContent');
  let cur = 'start';
  for (const s of $$('section', box)) if (s.offsetTop <= box.scrollTop + box.clientHeight / 4) cur = s.id.slice(5);
  if (box.scrollTop > 0 && box.scrollTop + box.clientHeight >= box.scrollHeight - 2) cur = $$('section', box).pop().id.slice(5);
  markHelpNav(cur);
}

// ---------------------------------------------------------------- guided tour
// Shown once on first launch; replay it from Help. Each step dims the page
// around one element and explains it.

const TOUR = [
  { title: 'Welcome to PacketTrail', text: 'A quick look at the main screens. It takes under a minute, and you can replay it from Help at any time.' },
  { el: '#status', title: 'Capture status', text: 'Green means full capture with byte counts. If it says “Connections only”, click Restart as administrator to see how much data each app moves.' },
  { el: '.hero', title: 'Traffic for the whole PC', text: 'Download and upload speed right now, with a live chart: hover it for exact values. Use Live, or 1H to All at the top right, to look back through history. Every table follows that choice.' },
  { tab: 'apps', el: ['#tabSeg', '#tab-apps .panel'], title: 'Applications', text: 'Every app with traffic. Click one to open its details on the right, with every address it talked to. Click a column header to sort.' },
  { tab: 'ips', el: ['#tabSeg', '#tab-ips .panel'], title: 'Remote IPs', text: 'Every address this PC talked to, with location, network and path quality. Click a row to open its details on the right, where you can run MTR, traceroute or ping.' },
  { tab: 'tools', el: ['#tabSeg', '#tab-tools .panel'], title: 'Test any address', text: 'Type any IP or hostname and press Enter to run MTR, traceroute, ping or a TCP check. Click Monitor on a result to keep testing it in the background.' },
  { tab: 'system', el: ['#tabSeg', '#tab-system'], title: 'Rule the PC in or out', text: 'CPU, memory, disks and network adapters, with a list of things worth checking when the network seems slow.' },
  { el: '#helpBtn', title: 'Help is always here', text: 'What every column means, keyboard shortcuts and troubleshooting. Press F1 at any time, and replay this tour from Help.' },
];
const tour = { i: -1, prevTab: null, prevFocus: null };

function startTour() {
  for (const d of $$('dialog[open]')) d.close();
  if (state.app) closeApp();
  tour.prevTab = state.tab;
  tour.prevFocus = document.activeElement;
  tour.i = -1;
  const root = $('#tour');
  root.classList.add('placing');
  root.hidden = false;
  document.addEventListener('keydown', tourKeys, true);
  window.addEventListener('resize', placeTour);
  showTourStep(0);
  root.getBoundingClientRect(); // commit the first position without animating to it
  requestAnimationFrame(() => root.classList.remove('placing'));
}
function endTour() {
  if (tour.i < 0) return;
  tour.i = -1;
  saved.set('tourSeen', true);
  $('#tour').hidden = true;
  document.removeEventListener('keydown', tourKeys, true);
  window.removeEventListener('resize', placeTour);
  if (tour.prevTab && state.tab !== tour.prevTab) setTab(tour.prevTab);
  if (tour.prevFocus && tour.prevFocus.isConnected && tour.prevFocus.focus) tour.prevFocus.focus({ preventScroll: true });
}
function showTourStep(i) {
  if (i < 0) return;
  const st = TOUR[i];
  if (!st) { endTour(); return; }
  const first = tour.i < 0;
  tour.i = i;
  if (st.tab && state.tab !== st.tab) setTab(st.tab);
  const card = $('#tourCard');
  const fill = () => {
    if (tour.i !== i) return;
    $('#tourStep').textContent = `${i + 1} of ${TOUR.length}`;
    $('#tourTitle').textContent = st.title;
    $('#tourText').textContent = st.text;
    $('#tourBack').hidden = i === 0;
    $('#tourSkip').hidden = i === TOUR.length - 1;
    $('#tourNext').textContent = i === 0 ? 'Start tour' : i === TOUR.length - 1 ? 'Done' : 'Next';
    placeTour();
    card.classList.remove('swap');
    $('#tourNext').focus({ preventScroll: true });
  };
  if (first) { fill(); return; }
  card.classList.add('swap'); // fade the old text out, move, fade the new text in
  setTimeout(fill, 110);
}
function placeTour() {
  const st = TOUR[tour.i];
  if (!st) return;
  const spot = $('#tourSpot'), card = $('#tourCard');
  const vw = innerWidth, vh = innerHeight, pad = 6, gap = 14, m = 12;
  const cw = card.offsetWidth, ch = card.offsetHeight;
  // A step can light up several elements (a tab plus its panel): use their union.
  const rects = [].concat(st.el || []).map(s => $(s)).filter(Boolean).map(e => e.getBoundingClientRect()).filter(b => b.width);
  const r = rects.length ? {
    left: Math.min(...rects.map(b => b.left)), top: Math.min(...rects.map(b => b.top)),
    right: Math.max(...rects.map(b => b.right)), bottom: Math.max(...rects.map(b => b.bottom)), width: 1,
  } : null;
  let x, y;
  if (!r || !r.width) {
    spot.classList.add('none');
    spot.style.width = spot.style.height = '0px';
    spot.style.transform = `translate(${Math.round(vw / 2)}px, ${Math.round(vh / 2)}px)`;
    x = (vw - cw) / 2;
    y = (vh - ch) / 2;
  } else {
    spot.classList.remove('none');
    const sx = Math.max(m / 2, r.left - pad), sy = Math.max(m / 2, r.top - pad);
    const sw = Math.min(vw - m / 2, r.right + pad) - sx, sh = Math.min(vh - m / 2, r.bottom + pad) - sy;
    spot.style.transform = `translate(${Math.round(sx)}px, ${Math.round(sy)}px)`;
    spot.style.width = Math.round(sw) + 'px';
    spot.style.height = Math.round(sh) + 'px';
    // Below the target, else above, right, left; big panels get it inside their top corner.
    if (sy + sh + gap + ch <= vh - m) { x = r.left; y = sy + sh + gap; }
    else if (sy - gap - ch >= m) { x = r.left; y = sy - gap - ch; }
    else if (sx + sw + gap + cw <= vw - m) { x = sx + sw + gap; y = r.top; }
    else if (sx - gap - cw >= m) { x = sx - gap - cw; y = r.top; }
    else { x = sx + sw - cw - 20; y = sy + 20; }
  }
  x = Math.min(Math.max(m, x), vw - cw - m);
  y = Math.min(Math.max(m, y), vh - ch - m);
  card.style.transform = `translate(${Math.round(x)}px, ${Math.round(y)}px)`;
}
function tourKeys(e) {
  const k = e.key;
  if (k === 'Escape') endTour();
  else if (k === 'ArrowRight') showTourStep(tour.i + 1);
  else if (k === 'ArrowLeft') showTourStep(tour.i - 1);
  else if (k === 'Tab') {
    // keep focus inside the card
    const btns = $$('#tourCard button').filter(b => !b.hidden);
    const at = btns.indexOf(document.activeElement);
    btns[(at + (e.shiftKey ? -1 : 1) + btns.length) % btns.length].focus();
  } else if (k !== 'Enter' && k !== ' ' && k !== 'F1') return;
  if (k === 'Enter' || k === ' ') { if (!$('#tourCard').contains(document.activeElement)) { e.preventDefault(); $('#tourNext').click(); } e.stopPropagation(); return; }
  e.preventDefault();
  e.stopPropagation();
}

function initHelp() {
  $('#helpBtn').addEventListener('click', () => openHelp());
  $('#helpClose').addEventListener('click', () => $('#help').close());
  // Light dismiss for every dialog (Settings, Help): a click on the dimmed
  // backdrop closes it. The press must start outside the dialog's box too, so
  // dragging a text selection out of it, or using its scrollbar, doesn't.
  for (const d of $$('dialog')) {
    const outside = e => {
      const r = d.getBoundingClientRect();
      return e.target === d && (e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom);
    };
    let pressedOutside = false;
    d.addEventListener('pointerdown', e => { pressedOutside = outside(e); });
    d.addEventListener('click', e => {
      if (pressedOutside && outside(e)) d.close();
      pressedOutside = false;
    });
  }
  $('#helpNav').addEventListener('click', e => { const b = e.target.closest('[data-help]'); if (b) showHelpTopic(b.dataset.help); });
  $('#helpContent').addEventListener('scroll', helpSpy, { passive: true });
  $('#tourStart').addEventListener('click', startTour);
  $('#tourNext').addEventListener('click', () => showTourStep(tour.i + 1));
  $('#tourBack').addEventListener('click', () => showTourStep(tour.i - 1));
  $('#tourSkip').addEventListener('click', endTour);
}
// First launch: start the tour once the dashboard has its first data.
function maybeStartTour() {
  if (state.tourChecked) return;
  state.tourChecked = true;
  // ?notour skips it (used for screenshots)
  if (saved.get('tourSeen', false) || new URLSearchParams(location.search).has('notour')) return;
  setTimeout(() => { if (!$$('dialog[open]').length && tour.i < 0) startTour(); }, 900);
}

// ---------------------------------------------------------------- system monitor

const sysState = { data: null, alert: '' };
const KIND_LABEL = { ethernet: 'Ethernet', wifi: 'Wi-Fi', cellular: 'Cellular', vpn: 'VPN', virtual: 'Virtual', other: 'Other' };
const lvlOf = (v, warn, bad) => (v >= bad ? 'bad' : v >= warn ? 'warn' : '');
function fmtPct(v, d = 0) {
  if (v === undefined || v === null || v < 0) return '–';
  const floor = Math.pow(10, -d);
  return (v > 0 && v < floor ? '<' + floor : v.toFixed(d)) + '%';
}
function fmtPerSec(v) {
  if (!v || v < 0.05) return '0/s';
  return (v >= 100 ? Math.round(v).toLocaleString() : v >= 10 ? v.toFixed(0) : v.toFixed(1)) + '/s';
}
function fmtLink(bps) {
  if (!bps) return '–';
  if (bps >= 1e9) { const g = bps / 1e9; return (Number.isInteger(g) ? g : g.toFixed(1)) + ' Gbit/s'; }
  return Math.round(bps / 1e6) + ' Mbit/s';
}
function fmtUptime(s) {
  if (!s) return '';
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
  return d ? `${d} d ${h} h` : h ? `${h} h ${m} min` : `${m} min`;
}
const fmtDiskRate = v => (v > 0 ? fmtBytes(v) + '/s' : '0 B/s');
function meter(pct, cls) {
  return `<span class="meter ${cls || ''}"><i style="transform:scaleX(${(Math.max(0, Math.min(100, pct)) / 100).toFixed(3)})"></i></span>`;
}
function cntHtml(recent, total, warn, bad) {
  if (!recent && !total) return '<span class="dim">0</span>';
  return `<span class="q ${recent ? lvlOf(recent, warn, bad) || 'warn' : ''}">${recent.toLocaleString()}</span><span class="dim"> / ${total.toLocaleString()}</span>`;
}
function utilHtml(r) {
  if (!r.up || r.rxUtil < 0) return '<span class="dim">–</span>';
  const u = Math.max(r.rxUtil, r.txUtil);
  return `<span class="meter-cell">${meter(u, lvlOf(u, 70, 85))}<span>${fmtPct(u, u < 10 ? 1 : 0)}</span></span>`;
}
function procCell(r) {
  if (r.app) return appCell(r.app, r.name);
  return `<span class="appcell"><span class="appicon"><span class="mono" style="${toneOf(r.name)}">${esc(initials(r.name))}</span></span><span class="nm">${esc(r.name)}</span></span>`;
}

tables.ifaces = new Table('ifaces', [
  { key: 'order', label: 'Order', hidden: true, csv: false },
  { key: 'name', label: 'Adapter', cls: 'trunc strong', html: r => `<span class="ifcell"><i class="ifdot ${r.up ? 'up' : ''}" title="${r.up ? 'Connected' : 'Disconnected'}"></i>${esc(r.name)}</span>`, tip: r => r.desc, text: r => `${r.name} ${r.desc} ${r.mac}` },
  { key: 'kind', label: 'Type', cls: 'dim', fmt: v => KIND_LABEL[v] || v },
  { key: 'desc', label: 'Device', cls: 'trunc dim', tip: r => r.desc },
  { key: 'speed', label: 'Link', num: true, fmt: (v, r) => (r.up ? fmtLink(v) : 'Down'), sortVal: r => (r.up ? r.speed : -1), title: 'Negotiated link speed', csv: r => r.speed },
  { key: 'rx', label: '↓ Rate', num: true, fmt: fmtRate, cls: 'rate-rx', csvLabel: 'Down rate (bytes/s)', csv: r => Math.round(r.rx) },
  { key: 'tx', label: '↑ Rate', num: true, fmt: fmtRate, cls: 'rate-tx', csvLabel: 'Up rate (bytes/s)', csv: r => Math.round(r.tx) },
  { key: 'util', label: 'Utilisation', num: true, html: utilHtml, sortVal: r => Math.max(r.rxUtil, r.txUtil), title: 'The busier direction as a share of the link speed', csv: r => Math.max(r.rxUtil, r.txUtil).toFixed(1) },
  { key: 'errs', label: 'Errors', num: true, html: r => cntHtml(r.errsRecent, r.errs, 1, 50), sortVal: r => r.errsRecent * 1e9 + r.errs, title: 'Packet errors: last minute / since boot', csvLabel: 'Errors since boot' },
  { key: 'discards', label: 'Discards', num: true, html: r => cntHtml(r.discardsRecent, r.discards, 200, 2000), sortVal: r => r.discardsRecent * 1e9 + r.discards, title: 'Packets the adapter or driver dropped: last minute / since boot', csvLabel: 'Discards since boot' },
  { key: 'mtu', label: 'MTU', num: true, fmt: v => v || '–' },
  { key: 'mac', label: 'MAC address', cls: 'mono-sm dim' },
], { sort: { key: 'order', dir: 1 }, rowKey: r => r.id });

tables.disks = new Table('disks', [
  { key: 'mount', label: 'Drive', cls: 'strong' },
  { key: 'label', label: 'Label', cls: 'trunc', tip: r => r.label },
  { key: 'fs', label: 'File system', cls: 'dim' },
  { key: 'pct', label: 'Used', num: true, html: r => `<span class="meter-cell wide">${meter(r.pct, lvlOf(r.pct, 90, 95))}<span>${fmtPct(r.pct)}</span></span>`, csv: r => r.pct.toFixed(1) },
  { key: 'free', label: 'Free', num: true, fmt: fmtBytes, csvLabel: 'Free (bytes)', csv: r => r.free },
  { key: 'total', label: 'Size', num: true, fmt: fmtBytes, csvLabel: 'Size (bytes)', csv: r => r.total },
], { sort: { key: 'mount', dir: 1 }, rowKey: r => r.mount });

tables.procs = new Table('procs', [
  { key: 'name', label: 'Process', cls: 'trunc strong', html: procCell, tip: r => r.path || r.name, text: r => `${r.name} ${r.path || ''} ${r.pid}` },
  { key: 'pid', label: 'PID', num: true, cls: 'dim' },
  { key: 'cpu', label: 'CPU', num: true, html: r => (r.cpu < 0.05 ? '<span class="dim">0</span>' : `<span class="q ${lvlOf(r.cpu, 50, 80)}">${r.cpu.toFixed(1)}%</span>`), title: 'Share of the whole machine (all cores)', csvLabel: 'CPU (%)', csv: r => r.cpu.toFixed(2) },
  { key: 'mem', label: 'Memory', num: true, fmt: fmtBytesCell, title: 'Private working set (Windows) or resident memory', csvLabel: 'Memory (bytes)', csv: r => r.mem },
  { key: 'io', label: 'I/O', num: true, fmt: v => (v < 0 ? '–' : v >= 1 ? fmtBytes(v) + '/s' : ''), sortVal: r => (r.io >= 0 ? r.io : ''), title: 'All reads and writes per second: files, devices and network', csvLabel: 'I/O (bytes/s)', csv: r => (r.io >= 0 ? Math.round(r.io) : '') },
  { key: 'netIn', label: 'Net ↓', num: true, fmt: fmtRate, cls: 'rate-rx', title: 'Download rate the capture attributes to this process', csvLabel: 'Net down (bytes/s)', csv: r => Math.round(r.netIn) },
  { key: 'netOut', label: 'Net ↑', num: true, fmt: fmtRate, cls: 'rate-tx', title: 'Upload rate the capture attributes to this process', csvLabel: 'Net up (bytes/s)', csv: r => Math.round(r.netOut) },
  { key: 'threads', label: 'Threads', num: true, fmt: v => (v >= 0 ? v.toLocaleString() : '–') },
  { key: 'handles', label: 'Handles', num: true, fmt: v => (v >= 0 ? v.toLocaleString() : '–'), title: 'Open handles (files, sockets, registry keys…); a number that only ever grows is a leak' },
  { key: 'path', label: 'Path', hidden: true },
], { sort: { key: 'cpu', dir: -1 }, rowKey: r => String(r.pid) });

// Small area sparkline on a canvas.
function drawSpark(canvas, vals, token, max) {
  const dpr = window.devicePixelRatio || 1, w = canvas.clientWidth, h = canvasHeight(canvas);
  if (!w) return;
  canvas.style.height = h + 'px';
  canvas.width = Math.round(w * dpr);
  canvas.height = Math.round(h * dpr);
  const g = canvas.getContext('2d');
  g.setTransform(dpr, 0, 0, dpr, 0, 0);
  g.clearRect(0, 0, w, h);
  g.strokeStyle = cssColor('--grid');
  g.lineWidth = 1;
  g.beginPath(); g.moveTo(0, h - 0.5); g.lineTo(w, h - 0.5); g.stroke();
  const n = vals.length;
  if (n < 2) return;
  const top = max || Math.max(...vals) * 1.15 || 1;
  const xs = vals.map((_, i) => (w * i) / (n - 1));
  const ys = vals.map(v => h - 1.5 - Math.min(1, Math.max(0, v) / top) * (h - 5));
  const col = cssColor(token);
  const grad = g.createLinearGradient(0, 0, 0, h);
  grad.addColorStop(0, withAlpha(col, 0.24));
  grad.addColorStop(1, withAlpha(col, 0));
  g.beginPath(); smoothPath(g, xs, ys); g.lineTo(w, h); g.lineTo(0, h); g.closePath();
  g.fillStyle = grad; g.fill();
  g.beginPath(); smoothPath(g, xs, ys);
  g.strokeStyle = col; g.lineWidth = 1.6; g.lineJoin = 'round'; strokeGlow(g, col, 5);
}

// Percentage chart (CPU, memory, disk busy) with a hover readout.
const SYS_SERIES = [
  { key: 'cpu', label: 'CPU', token: '--rx' },
  { key: 'mem', label: 'Memory', token: '--s3' },
  { key: 'diskBusy', label: 'Disk busy', token: '--tx' },
];
class PctChart {
  constructor(canvas, tip) {
    Object.assign(this, { c: canvas, tip, points: [], hover: -1, h: +canvas.getAttribute('height') });
    canvas.style.height = this.h + 'px';
    canvas.addEventListener('mousemove', e => {
      const n = this.points.length;
      if (n < 2) return;
      const r = canvas.getBoundingClientRect();
      this.hover = Math.max(0, Math.min(n - 1, Math.round((e.clientX - r.left - this.pl) / ((r.width - this.pl - this.pr) / (n - 1)))));
      this.draw();
    });
    canvas.addEventListener('mouseleave', () => { this.hover = -1; this.draw(); });
    new ResizeObserver(() => this.draw()).observe(canvas);
  }
  set(points) { this.points = points || []; this.draw(); }
  series() { return SYS_SERIES.filter(s => s.key !== 'diskBusy' || this.points.some(p => p.diskBusy >= 0)); }
  draw() {
    const c = this.c, dpr = window.devicePixelRatio || 1, w = c.clientWidth, h = this.h;
    if (!w) return;
    c.width = Math.round(w * dpr);
    c.height = Math.round(h * dpr);
    const g = c.getContext('2d');
    g.setTransform(dpr, 0, 0, dpr, 0, 0);
    g.clearRect(0, 0, w, h);
    const pl = this.pl = 40, pr = this.pr = 6, pt = 8, pb = 20, cw = w - pl - pr, ch = h - pt - pb;
    g.font = '500 11px ' + getComputedStyle(document.body).fontFamily;
    g.fillStyle = cssColor('--text-3');
    g.textAlign = 'right';
    g.textBaseline = 'middle';
    for (let i = 0; i <= 4; i++) {
      const y = Math.round(pt + ch - (ch * i) / 4) + 0.5;
      g.strokeStyle = cssColor('--grid');
      g.setLineDash(i === 0 ? [] : [2, 4]);
      g.beginPath(); g.moveTo(pl, y); g.lineTo(w - pr, y); g.stroke();
      if (i > 0) g.fillText(25 * i + '%', pl - 8, y);
    }
    g.setLineDash([]);
    const pts = this.points, n = pts.length;
    if (n < 2) {
      g.textAlign = 'center';
      g.fillText('Collecting samples…', pl + cw / 2, pt + ch / 2);
      this.showTip(-1);
      return;
    }
    const X = i => pl + (cw * i) / (n - 1);
    const Y = v => pt + ch - (Math.max(0, Math.min(100, v)) / 100) * ch;
    g.textAlign = 'center';
    g.textBaseline = 'alphabetic';
    const secs = pts[n - 1].t - pts[0].t > 600 ? '' : 's';
    const labels = Math.max(2, Math.floor(cw / (secs ? 120 : 100)));
    for (let k = 0; k <= labels; k++) {
      const i = Math.round((k * (n - 1)) / labels);
      const d = new Date(pts[i].t * 1000);
      g.fillText(`${pad(d.getHours())}:${pad(d.getMinutes())}${secs && ':' + pad(d.getSeconds())}`, Math.max(pl + 24, Math.min(w - pr - 24, X(i))), h - 5);
    }
    const xs = pts.map((_, i) => X(i));
    for (const s of this.series().slice().reverse()) {
      const col = cssColor(s.token);
      const ys = pts.map(p => Y(p[s.key]));
      if (s.key === 'cpu') {
        const grad = g.createLinearGradient(0, pt, 0, pt + ch);
        grad.addColorStop(0, withAlpha(col, 0.2));
        grad.addColorStop(1, withAlpha(col, 0));
        g.beginPath(); smoothPath(g, xs, ys); g.lineTo(xs[n - 1], pt + ch); g.lineTo(xs[0], pt + ch); g.closePath();
        g.fillStyle = grad; g.fill();
      }
      g.beginPath(); smoothPath(g, xs, ys);
      g.strokeStyle = col; g.lineWidth = s.key === 'cpu' ? 1.8 : 1.5; g.lineJoin = 'round'; strokeGlow(g, col);
    }
    const hi = this.hover;
    if (hi >= 0 && hi < n) {
      const x = Math.round(X(hi)) + 0.5;
      g.strokeStyle = cssColor('--hair-strong');
      g.lineWidth = 1;
      g.beginPath(); g.moveTo(x, pt); g.lineTo(x, pt + ch); g.stroke();
      for (const s of this.series()) {
        const col = cssColor(s.token);
        g.beginPath(); g.arc(X(hi), Y(pts[hi][s.key]), 3, 0, Math.PI * 2);
        g.fillStyle = col; g.fill();
        g.lineWidth = 1.5; g.strokeStyle = cssColor('--card'); g.stroke();
      }
    }
    this.showTip(hi, X(hi), w);
  }
  showTip(i, x, w) {
    if (i < 0 || !this.points[i]) { this.tip.classList.remove('show'); return; }
    const p = this.points[i];
    this.tip.innerHTML = `<div class="t">${esc(isoTime(p.t))}</div>` + this.series().map(s =>
      `<div class="r"><i style="background:var(${s.token})"></i>${s.label}<b>${fmtPct(p[s.key])}</b></div>`).join('');
    const tw = this.tip.offsetWidth || 150;
    this.tip.style.left = Math.max(0, x + 14 + tw > w ? x - tw - 14 : x + 14) + 'px';
    this.tip.classList.add('show');
  }
}
const sysChart = new PctChart($('#sysChart'), $('#sysTip'));

function setTile(id, value, cls, sub, vals, token, max) {
  const t = $('#' + id);
  const v = $('.st-val', t);
  if (v.textContent !== value) v.textContent = value;
  v.className = 'st-val ' + (cls || '');
  if ($('.st-sub', t).innerHTML !== sub) $('.st-sub', t).innerHTML = sub;
  drawSpark($('canvas', t), vals, token, max);
}

function renderSysTiles(d) {
  const hist = d.history || [];
  const recent = hist.slice(-150); // last 5 minutes
  const c = d.cpu, m = d.mem, io = d.diskIO, t = d.tcp;
  const cpuSub = [`${c.count} logical processor${c.count === 1 ? '' : 's'}`,
    c.hasDpc ? `DPC + IRQ ${fmtPct(c.dpc + c.irq, 1)}` : '',
    c.load && c.load.length ? `load ${c.load[0].toFixed(2)}` : '',
    c.hasWait && c.iowait >= 1 ? `I/O wait ${fmtPct(c.iowait)}` : ''].filter(Boolean).join(' · ');
  setTile('tileCpu', fmtPct(c.busy), lvlOf(c.busy, 75, 90), esc(cpuSub), recent.map(p => p.cpu), '--rx', 100);
  const memSub = `${fmtBytes(m.used)} of ${fmtBytes(m.total)} · ${fmtBytes(m.avail)} free` +
    (m.commitLimit ? ` · commit ${fmtPct(100 * m.commitUsed / m.commitLimit)}` : m.swapTotal ? ` · swap ${fmtBytes(m.swapUsed)}` : '');
  setTile('tileMem', fmtPct(m.pct), lvlOf(m.pct, 85, 92), esc(memSub), recent.map(p => p.mem), '--s3', 100);
  if (io.has) {
    const sub = io.split ? `Read ${fmtDiskRate(io.read)} · write ${fmtDiskRate(io.write)}` : 'Read + write';
    const extra = [io.busy >= 0 ? `busy ${fmtPct(io.busy)}` : '', io.latency >= 0 ? `${io.latency < 10 ? io.latency.toFixed(1) : Math.round(io.latency)} ms` : ''].filter(Boolean).join(' · ');
    setTile('tileDisk', fmtDiskRate(io.read + io.write), io.latency >= 50 ? 'warn' : '', esc(sub + (extra ? ' · ' + extra : '')), recent.map(p => p.diskR + p.diskW), '--tx');
  } else {
    setTile('tileDisk', '–', '', 'Not available on this system', [], '--tx');
  }
  if (t.has) {
    // Over the last minute: single 2-second samples are too noisy to read.
    let out = 0, re = 0;
    for (const p of hist.slice(-30)) { out += p.outSegs; re += p.retrans; }
    const pct1 = out >= 50 ? (100 * re) / out : -1;
    const series = recent.map(p => (p.outSegs > 0 ? (100 * p.retrans) / p.outSegs : 0));
    const sub = `last minute · ${fmtPerSec(t.outSegs)} segments out · ${t.estab >= 0 ? t.estab.toLocaleString() + ' established' : fmtPerSec(t.inSegs) + ' in'}`;
    setTile('tileTcp', fmtPct(pct1, 2), out >= 3000 && re >= 30 ? lvlOf(pct1, 2, 5) : '', esc(sub), series, '--s4', Math.max(2, ...series) * 1.15);
  } else {
    setTile('tileTcp', '–', '', 'Not available on this system', [], '--s4');
  }
}

function renderSysHints(d) {
  const hs = d.hints || [];
  const meta = [d.host, { windows: 'Windows', linux: 'Linux', darwin: 'macOS' }[d.os] || d.os, d.uptime ? 'up ' + fmtUptime(d.uptime) : ''].filter(Boolean).join(' · ');
  const body = hs.length
    ? `<ul class="hint-list">${hs.map(h => `<li class="hint ${h.level}"><i class="hint-dot" aria-hidden="true"></i><div><strong>${esc(h.title)}</strong><p>${esc(h.text)}</p></div></li>`).join('')}</ul>`
    : `<div class="hint-ok"><svg viewBox="0 0 20 20" width="20" height="20" aria-hidden="true"><circle cx="10" cy="10" r="8.2" fill="none" stroke="currentColor" stroke-width="1.5"/><path d="m6.4 10.2 2.5 2.5 4.8-5.2" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/></svg><div><strong>Nothing stands out</strong><span>CPU, memory, disks, network adapters and the TCP stack look healthy right now.</span></div></div>`;
  const notes = (d.notes || []).map(n => `<p class="sys-note">${esc(n)}</p>`).join('');
  const html = `<div class="card-label sys-label"><span>Things to look at${hs.length ? ` <span class="count-pill ${hs[0].level}">${hs.length}</span>` : ''}</span><span class="sys-meta">${esc(meta)}</span></div>${body}${notes}`;
  const el = $('#sysHints');
  if (el._h !== html) { el.innerHTML = html; el._h = html; }
}

function renderSysCpu(d) {
  const c = d.cpu;
  const cores = c.cores || [];
  const grid = cores.length
    ? `<div class="core-grid" style="--cols:${cores.length <= 32 ? cores.length : Math.ceil(cores.length / Math.ceil(cores.length / 32))}">${cores.map((v, i) => `<div class="core ${lvlOf(v, 75, 95)}" title="Processor ${i}: ${v.toFixed(0)}%"><i style="transform:scaleY(${(Math.max(1.5, v) / 100).toFixed(3)})"></i></div>`).join('')}</div>`
    : '<p class="sys-note">Per-core figures are not available on this platform.</p>';
  let parts;
  if (c.hasDpc) parts = [['user', 'User', c.user], ['sys', 'Kernel', Math.max(0, c.system - c.dpc - c.irq)], ['dpc', 'DPC', c.dpc], ['irq', 'Interrupts', c.irq]];
  else if (c.hasWait) parts = [['user', 'User', c.user], ['sys', 'System', c.system], ['irq', 'IRQ', c.irq], ['dpc', 'I/O wait', c.iowait], ['steal', 'Steal', c.steal]].filter(p => p[0] !== 'steal' || p[2] > 0);
  else parts = [['user', 'User', c.user], ['sys', 'System', c.system]];
  const bar = `<div class="cpu-split">${parts.map(([k, , v]) => `<i class="${k}" style="flex-grow:${Math.max(0, v).toFixed(2)}"></i>`).join('')}<i class="idle" style="flex-grow:${Math.max(0, 100 - parts.reduce((a, p) => a + p[2], 0)).toFixed(2)}"></i></div>`;
  const legend = `<div class="mix-legend">${parts.map(([k, l, v]) => `<span class="mix-leg"><span class="sw cpu-${k}"></span>${l} <b>${fmtPct(v, 1)}</b></span>`).join('')}</div>`;
  const html = `<div class="card-label">Processors <span class="dim">· ${cores.length || c.count} logical</span></div>${grid}${bar}${legend}`;
  const el = $('#sysCpu');
  if (el._h !== html) { el.innerHTML = html; el._h = html; }
}

function stat(label, value, sub, cls, title) {
  return `<div class="stat"${title ? ` title="${esc(title)}"` : ''}><span class="lbl">${label}</span><span class="val ${cls || ''}">${value}</span>${sub ? `<span class="sub">${sub}</span>` : ''}</div>`;
}

function renderSysStack(d) {
  const t = d.tcp;
  const el = $('#sysStack');
  let html = '<div class="card-label">TCP / IP stack</div>';
  if (!t.has) html += '<p class="sys-note">Stack counters are not available on this system.</p>';
  else {
    html += '<div class="stat-grid">'
      + stat('Retransmitted', t.retransPct1 >= 0 ? fmtPct(t.retransPct1, 2) : '–', t.retransPct1 >= 0 ? 'of segments, last minute' : 'needs more traffic', (sysState.data.hints || []).some(h => h.area === 'tcp' && h.title.startsWith('TCP')) ? lvlOf(t.retransPct1, 2, 5) : '', 'Share of TCP segments this machine sent that had to be sent again. Above ~1–2% means loss on the path.')
      + stat('Segments out', fmtPerSec(t.outSegs), `${fmtPerSec(t.retrans)} retransmitted`)
      + stat('Segments in', fmtPerSec(t.inSegs), t.inErrs >= 0.05 ? `<span class="q warn">${fmtPerSec(t.inErrs)} in error</span>` : 'no errors')
      + stat('Established', t.estab >= 0 ? t.estab.toLocaleString() : '–', 'TCP connections')
      + stat('Resets sent', fmtPerSec(t.resets), 'refused or aborted', '', 'TCP resets this machine sent: closed ports, aborted connections. A sudden rise can mean a firewall or app problem.')
      + stat('Failed connects', fmtPerSec(t.fails), 'no answer or reset', t.fails >= 1 ? 'warn' : '')
      + stat('UDP received', fmtPerSec(t.udpIn), `${fmtPerSec(t.udpOut)} sent`)
      + stat('UDP drops', fmtPerSec(t.udpErrs), 'receive buffer full', t.udpErrs >= 0.5 ? 'warn' : '', 'Datagrams the OS threw away because an app was not reading them fast enough.')
      + '</div>';
  }
  if (el._h !== html) { el.innerHTML = html; el._h = html; }
}

function setTabAlert(level) {
  if (sysState.alert === level) return;
  sysState.alert = level;
  const b = $('#tabSeg button[data-tab="system"]');
  if (level) b.dataset.alert = level; else delete b.dataset.alert;
  b.title = level ? 'System: something worth checking' : '';
}

async function loadSystem() {
  const d = await api('/api/system');
  sysState.data = d;
  const st = $('#sysStatus');
  if (!d.ready) { st.hidden = false; return; }
  st.hidden = true;
  renderSysTiles(d);
  renderSysHints(d);
  renderSysCpu(d);
  renderSysStack(d);
  sysChart.set(d.history);
  (d.ifaces || []).forEach((f, i) => { f.order = i; });
  tables.ifaces.setRows(d.ifaces || []);
  tables.disks.setRows(d.disks || []);
  tables.procs.setRows(d.procs || []);
  setTabAlert((d.hints || []).some(h => h.level === 'bad') ? 'bad' : (d.hints || []).some(h => h.level === 'warn') ? 'warn' : '');
}

// Away from the tab, only the hints are fetched, for the tab's alert dot.
async function loadSystemHints() {
  const d = await api('/api/system?lite=1');
  if (!d.ready) return;
  setTabAlert((d.hints || []).some(h => h.level === 'bad') ? 'bad' : (d.hints || []).some(h => h.level === 'warn') ? 'warn' : '');
}

// ---------------------------------------------------------------- scheduler

const last = {}, busy = {};
function every(key, ms, fn) {
  const now = Date.now();
  if (busy[key] || now - (last[key] || 0) < ms) return;
  last[key] = now;
  busy[key] = true;
  fn().catch(err => console.warn(key, err)).finally(() => { busy[key] = false; });
}
function invalidate(...keys) { for (const k of keys) last[k] = 0; tick(); }
function tick() {
  if (document.hidden) return;
  const live = isLive();
  every('live', 1000, loadLive);
  every('status', 5000, loadStatus);
  every('monlist', state.tab === 'monitors' ? 2000 : 4000, loadMonitors);
  if (!live) every('mainHist', 30000, loadMainHistory);
  const iv = live ? 2000 : 15000;
  const xiv = live ? 3000 : 15000;
  if (state.tab === 'apps') {
    every('apps', iv, loadApps);
    if (state.app) {
      every('flows', iv, loadFlows);
      every('appChart', live ? 1000 : 30000, loadAppChart);
      every('x-flows', xiv, () => tables.flows.refreshExpanded());
    }
  } else if (state.tab === 'ips') {
    every('ips', live ? 3000 : 15000, loadIPs);
    every('x-ips', xiv, () => tables.ips.refreshExpanded());
  } else if (state.tab === 'asns') {
    every('asns', live ? 3000 : 15000, loadASNs);
    every('x-asns', xiv, () => tables.asns.refreshExpanded());
  } else if (state.tab === 'monitors') {
    every('monseries', live ? 2000 : 20000, loadMonSeries);
  } else if (state.tab === 'peering') {
    every('peering', live ? 5000 : 20000, loadPeering);
    every('x-peers', xiv, () => tables.peers.refreshExpanded());
  } else if (state.tab === 'system') {
    every('system', 2000, loadSystem);
  } else if (state.tab === 'tools') {
    every('tool', live ? 5000 : 20000, refreshTool);
  }
  if (state.tab !== 'system') every('syshints', 10000, loadSystemHints);
}
setInterval(tick, 500);
document.addEventListener('visibilitychange', tick);

// ---------------------------------------------------------------- interactions

// App details open in a panel that slides over from the right; the apps list
// keeps its full height behind it. Clicking the open app again closes it.
function selectApp(r) {
  if (state.app === r.key && $('#detail').classList.contains('open')) { closeApp(); return; }
  const first = !state.app;
  state.app = r.key;
  state.appName = r.name;
  const sheet = $('#detail');
  sheet.classList.add('open');
  sheet.setAttribute('aria-hidden', 'false');
  fillAppKpis(r);
  $('#dName').textContent = r.name;
  $('#dIcon').outerHTML = appIcon(r.key, r.name, 'lg', 'dIcon');
  $('#dSub').textContent = [r.path, r.pids && 'PID ' + r.pids].filter(Boolean).join('  ·  ');
  tables.flows.collapse();
  if (first) { tables.flows.setRows([]); appChart.set([], 1); }
  else $('#wrap-flows').classList.add('stale'); // keep the old rows dimmed until the new ones land
  tables.apps.render(true);
  invalidate('flows', 'appChart');
}

function closeApp() {
  const sheet = $('#detail');
  if (!sheet.classList.contains('open')) return;
  sheet.classList.remove('open');
  sheet.setAttribute('aria-hidden', 'true');
  tables.flows.collapse(); // also stops any running diagnostics
  state.app = null;
  tables.apps.render(true);
}

function fillAppKpis(r) {
  const tiles = kpi('Download', fmtRate(r.rateIn) || '0', fmtBytes(r.in) + ' total', 'rx')
    + kpi('Upload', fmtRate(r.rateOut) || '0', fmtBytes(r.out) + ' total', 'tx')
    + kpi('Remote IPs', String(r.ips), `${r.flows} endpoint${r.flows === 1 ? '' : 's'}`)
    + kpi('Avg RTT', fmtRTT(r.rtt))
    + kpi('Loss ↓ (est.)', r.lossInPct >= 0 ? r.lossInPct.toFixed(2) + '%' : '–', '', r.lossInPct >= 2 ? 'bad' : r.lossInPct >= 0.5 ? 'warn' : '')
    + kpi('Retrans ↑', r.retrans ? r.retransPct.toFixed(2) + '%' : '–', r.retrans ? fmtBytes(r.retrans) : '', r.retransPct >= 5 ? 'bad' : r.retransPct >= 1 ? 'warn' : '')
    + kpi('First seen', fmtTime(r.first)) + kpi('Last seen', fmtTime(r.last));
  const el = $('#appKpis');
  if (el._h !== tiles) { el.innerHTML = tiles; el._h = tiles; }
}

// Keep the slide-over just below the floating toolbar.
function placeSheet() {
  const top = $('.chrome').getBoundingClientRect().bottom + 10;
  document.documentElement.style.setProperty('--sheet-top', top + 'px');
}
// Side panels narrower than 900 px stack their two-column detail grids
// (.sheet-panel.narrow .xgrid). This used to be a CSS container query, but in
// WebKit (the Mac and Linux window) content replaced inside a size query
// container is laid out in two passes, and the scroll box inside the panel
// lost its position on every refresh: scrolling down to a running test kept
// bouncing back to the top.
function watchSheetWidths() {
  const ro = new ResizeObserver(entries => {
    for (const e of entries) e.target.classList.toggle('narrow', e.contentRect.width <= 900);
  });
  for (const el of $$('.sheet-panel')) ro.observe(el);
}

// Segmented controls: a sliding thumb under the checked option.
function placeThumb(seg) {
  const on = seg.querySelector('button[aria-checked="true"], button[aria-selected="true"]');
  const th = seg.querySelector('.seg-thumb');
  if (!on || !th) return;
  th.style.width = on.offsetWidth + 'px';
  th.style.transform = `translateX(${on.offsetLeft}px)`;
}
function setSeg(seg, value, attr = 'aria-checked', key = 'value') {
  for (const b of seg.querySelectorAll('button')) b.setAttribute(attr, String(b.dataset[key] === value));
  placeThumb(seg);
}
function initSeg(seg, onPick, key = 'value') {
  seg.addEventListener('click', e => {
    const b = e.target.closest('button');
    if (b && b.dataset[key]) onPick(b.dataset[key]);
  });
  seg.addEventListener('keydown', e => {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
    const btns = [...seg.querySelectorAll('button')];
    const i = btns.findIndex(b => b.getAttribute('aria-checked') === 'true' || b.getAttribute('aria-selected') === 'true');
    const next = btns[(i + (e.key === 'ArrowRight' ? 1 : btns.length - 1)) % btns.length];
    e.preventDefault();
    next.focus();
    onPick(next.dataset[key]);
  });
}
const allSegs = () => $$('.seg');

function setPeriod(p) {
  state.period = p;
  saved.set('period', p);
  setSeg($('#periodSeg'), p);
  $('#chartTitle').textContent = p === 'session' ? 'Live · last 5 minutes' : `${periodLabel()} · all applications`;
  invalidate('apps', 'flows', 'appChart', 'ips', 'asns', 'mainHist', 'live', 'x-flows', 'x-ips', 'x-asns', 'monseries', 'peering');
}

function setTab(t) {
  if (inspector.owner) inspector.owner.collapse();
  state.tab = t;
  saved.set('tab', t);
  setSeg($('#tabSeg'), t, 'aria-selected', 'tab');
  $$('.tab').forEach(s => { s.hidden = s.id !== 'tab-' + t; });
  if (t === 'monitors') { invalidate('monlist', 'monseries'); requestAnimationFrame(() => { placeThumb($('#monMethod')); placeThumb($('#monInterval')); }); }
  else if (t === 'tools') {
    invalidate('tool');
    requestAnimationFrame(() => {
      $$('#toolPanel .seg').forEach(placeThumb);
      if (!toolState.ip) $('#toolTarget').focus();
    });
  } else invalidate(t === 'apps' ? 'apps' : t);
}

function applyTheme(t, animate) {
  state.theme = t;
  saved.set('theme', t);
  const root = document.documentElement;
  const apply = () => {
    if (t === 'light' || t === 'dark') root.dataset.theme = t;
    else delete root.dataset.theme;
    setSeg($('#themeSeg'), t);
    syncTitleBar();
    mainChart.draw();
    appChart.draw();
    sysChart.draw();
    if (sysState.data && sysState.data.ready) renderSysTiles(sysState.data);
  };
  // Cross-fade a snapshot of the old look into the new one on the GPU; per-
  // element colour transitions repaint thousands of table cells and stutter.
  const calm = matchMedia('(prefers-reduced-motion: reduce)').matches;
  if (animate && !calm && document.startViewTransition) document.startViewTransition(apply);
  else apply();
}
// In the native window, match the Windows title bar to the resolved theme.
function syncTitleBar() {
  const dark = state.theme === 'dark' || (state.theme === 'system' && matchMedia('(prefers-color-scheme: dark)').matches);
  if (typeof window.nmSetDark === 'function') window.nmSetDark(dark).catch(() => {});
}

function gotoIP(ip) {
  const inp = $('input.filter[data-table="ips"]');
  inp.value = ip;
  tables.ips.filter = ip;
  if (state.hidePrivate) {
    state.hidePrivate = false;
    $('#hidePrivate').checked = false;
  }
  state.pendingExpand = ip;
  setTab('ips');
}

initSeg($('#periodSeg'), setPeriod);
initSeg($('#tabSeg'), setTab, 'tab');
initSeg($('#themeSeg'), t => applyTheme(t, true));
initSeg($('#unitSeg'), u => {
  state.bits = u === 'bits';
  saved.set('bits', state.bits);
  setUnits();
  invalidate('live', 'x-flows', 'x-ips', 'x-asns');
});
window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => { syncTitleBar(); mainChart.draw(); appChart.draw(); sysChart.draw(); if (sysState.data && sysState.data.ready) renderSysTiles(sysState.data); });
$$('input.filter').forEach(inp => inp.addEventListener('input', () => {
  const t = tables[inp.dataset.table];
  t.filter = inp.value;
  t.render(true);
}));
$('#hidePrivate').checked = state.hidePrivate;
$('#hidePrivate').addEventListener('change', e => {
  state.hidePrivate = e.target.checked;
  saved.set('hidePrivate', state.hidePrivate);
  tables.ips.render(true);
});
function setUnits() {
  setSeg($('#unitSeg'), state.bits ? 'bits' : 'bytes');
  Object.values(tables).forEach(t => t.render(true));
  mainChart.draw();
  appChart.draw();
}
$('#settingsBtn').addEventListener('click', () => { fillSettings(); renderPalettes(); $('#settings').showModal(); invalidate('status'); });
$('#geoUpdate').addEventListener('click', async () => {
  try {
    await post('/api/geo/update');
    toast('Downloading the latest DB-IP Lite databases…');
  } catch (e) { toast('Update failed: ' + e.message); }
  invalidate('status');
});
$('#elevateBtn').addEventListener('click', async () => {
  try {
    await post('/api/elevate');
    toast('Restarting as administrator…');
  } catch (e) { toast('Not restarted: ' + e.message); }
});
document.addEventListener('click', async e => {
  const cb = e.target.closest('[data-copy]');
  if (cb) {
    e.preventDefault();
    const ok = await copyText(cb.dataset.copy);
    if (ok) flashDone(cb);
    toast(ok ? `Copied ${cb.dataset.copy}` : 'Could not copy to the clipboard');
    return;
  }
  const cd = e.target.closest('[data-copy-details]');
  if (cd) {
    const p = cd.closest('.xpanel');
    if (p && p._text && await copyText(p._text)) { flashDone(cd); toast('Copied details'); }
    return;
  }
  const g = e.target.closest('[data-goto-ip]');
  if (g) { gotoIP(g.dataset.gotoIp); return; }
  const a = e.target.closest('a[data-link]');
  if (a) { e.preventDefault(); openLink(a.dataset.link); }
});
document.addEventListener('keydown', e => {
  if (e.key === '/' && !(e.target instanceof HTMLInputElement)) {
    const sec = $('#tab-' + state.tab);
    const inp = $$('input.filter', sec).filter(i => i.offsetParent).pop() || $('input.filter', sec);
    if (inp) { e.preventDefault(); inp.focus(); }
  }
  if (e.key === 'F1') { e.preventDefault(); openHelp(); return; }
  if (e.key === 'Escape' && !$$('dialog[open]').length) {
    // Close the innermost thing first: an expanded row in the panel, then the panel.
    if (state.tab === 'peering' && $('#routeSheet').classList.contains('open') && tables.peers.expanded == null) closeRouteSheet();
    else if (state.tab === 'apps' && state.app && tables.flows.expanded == null) closeApp();
    else allTables.forEach(t => t.collapse());
  }
});
window.addEventListener('resize', () => { allSegs().forEach(placeThumb); placeSheet(); });
$('#detailClose').addEventListener('click', closeApp);

function slug(s) { return String(s).toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 40); }
async function exportTable(id) {
  const t = tables[id];
  const sep = (state.status && state.status.listSep) || ',';
  const csv = '﻿' + t.csv(sep);
  const n = t.filtered().length;
  const name = `packettrail-${id}${id === 'flows' && state.appName ? '-' + slug(state.appName) : ''}-${state.period}-${stamp()}.csv`;
  try {
    const r = await post('/api/export?name=' + encodeURIComponent(name), csv, 'text/csv');
    toast(`Saved ${n.toLocaleString()} rows to ${r.path}`, 'Show in folder', () => post('/api/reveal?path=' + encodeURIComponent(r.path)).catch(() => {}));
  } catch (e) {
    toast('Export failed: ' + e.message);
  }
}
$$('[data-export]').forEach(b => b.addEventListener('click', () => exportTable(b.dataset.export)));

// ---------------------------------------------------------------- start

initMonitorsUI();
initToolsUI();
initHelp();
initPeeringUI();
$('#routeOpen').addEventListener('click', openRouteSheet);
$('#routeClose').addEventListener('click', closeRouteSheet);
$('#routeRun').addEventListener('click', runRoutes);
placeSheet();
watchSheetWidths();
applyPalette(saved.get('palette', 'classic'));
setGlow(saved.get('glow', true));
applyTheme(state.theme, false);
setUnits();
setTab(state.tab);
setPeriod(state.period);
// Enable thumb motion only after the first placement, and re-place once
// fonts have loaded (button widths change).
if (document.fonts) document.fonts.ready.then(() => allSegs().forEach(placeThumb));
setTimeout(() => allSegs().forEach(seg => { placeThumb(seg); seg.classList.add('ready'); }), 60);
