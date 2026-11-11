// PacketTrail website: the Plain | Engineer switch, the Ghost Fibers hero
// background, and the animated demo window (demo data, labelled as such).
import { ghostFibers } from './ghost-fibers.js';

const root = document.documentElement;
const reduce = matchMedia('(prefers-reduced-motion: reduce)');
const STILL = new URLSearchParams(location.search).has('still');

/* ---------- register switch ---------- */
const switches = document.querySelectorAll('.reg-switch');
function setRegister(reg, remember) {
  root.dataset.reg = reg;
  for (const s of switches) {
    for (const b of s.querySelectorAll('button')) b.setAttribute('aria-checked', String(b.dataset.set === reg));
  }
  if (remember) {
    try { localStorage.setItem('pt.view', reg === 'eng' ? 'engineer' : 'plain'); } catch (e) { /* storage blocked: the choice just isn't remembered */ }
  }
}
for (const s of switches) {
  s.addEventListener('click', e => {
    const b = e.target.closest('button[data-set]');
    if (b) setRegister(b.dataset.set, true);
  });
  s.addEventListener('keydown', e => {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
    e.preventDefault();
    const next = root.dataset.reg === 'eng' ? 'plain' : 'eng';
    setRegister(next, true);
    s.querySelector(`[data-set="${next}"]`).focus();
  });
}
setRegister(root.dataset.reg === 'eng' ? 'eng' : 'plain', false);

/* ---------- copy buttons (the macOS install command) ---------- */
for (const b of document.querySelectorAll('[data-copy]')) {
  const label = b.textContent;
  b.addEventListener('click', async () => {
    const src = document.getElementById(b.dataset.copy);
    let done = 'Copied';
    try {
      await navigator.clipboard.writeText(src.textContent.trim());
    } catch (e) {
      // No clipboard access: select the text, so the usual copy shortcut works.
      const range = document.createRange();
      range.selectNodeContents(src);
      const sel = getSelection();
      sel.removeAllRanges();
      sel.addRange(range);
      done = 'Selected';
    }
    b.textContent = done;
    clearTimeout(b._t);
    b._t = setTimeout(() => { b.textContent = label; }, 1800);
  });
}

/* ---------- Ghost Fibers ---------- */
const host = document.getElementById('fibers');
const dark = matchMedia('(prefers-color-scheme: dark)');
const narrow = matchMedia('(max-width: 720px)');
let fibers = null;
function fiberOptions() {
  const small = narrow.matches;
  const d = dark.matches;
  return {
    glowColor: d ? '#43afff' : '#0a7be8',
    secondaryColor: d ? '#ff9c28' : '#e06a00',
    lineColor: '',
    gradient: 'depth', // with the port's pure per-ribbon tints: two blue ribbons, one orange
    lightMode: !d,
    speed: 0.32,
    rotation: small ? 8 : 14,
    scale: small ? 1 : 1.3,
    threads: small ? 48 : 80,
    density: small ? 0.65 : 1,
    particles: small ? 0.25 : 0.45,
    // Kept below saturation so the additive blend and bloom don't wash the
    // blue and orange out to cream.
    glow: 0.8,
    foldGlow: 0.8,
    sparkle: 0.6,
    depthOfField: 0.7,
    brightness: d ? 2 : 1.1,
    vignette: 0.28,
    grain: 0.04,
    perspective: 0.62,
    hoverEffect: 'focus',
    paused: STILL, // ?still renders one frame, for screenshots
  };
}
function startFibers() {
  if (fibers) fibers.destroy();
  fibers = null;
  try { fibers = ghostFibers(host, fiberOptions()); } catch (e) { fibers = null; }
  host.classList.toggle('static', !fibers);
}
startFibers();
dark.addEventListener('change', () => fibers ? fibers.set(fiberOptions()) : startFibers());
narrow.addEventListener('change', () => fibers && fibers.set(fiberOptions()));

/* ---------- the demo window ---------- */
// Demo data, shaped like what the app shows: a game updater wakes up, climbs
// to the top by Total rate, holds while the callouts explain it, then the
// download finishes and it sinks back (the loop resets with a visible cause).
const APPS = [
  { id: 'chrome', name: 'chrome.exe', base: 14.6, ips: 34, rtt: '46.9 ms', loss: '0.25%', tint: '#5f8fd8' },
  { id: 'teams', name: 'ms-teams.exe', base: 5.28, ips: 2, rtt: '121 ms', loss: '0.06%', tint: '#7b6fd6' },
  { id: 'onedrive', name: 'OneDrive.exe', base: 1.03, ips: 1, rtt: '54.7 ms', loss: '0.05%', tint: '#3d8fd6' },
  { id: 'svchost', name: 'svchost.exe', base: 0.52, ips: 1, rtt: '54.9 ms', loss: '0.02%', tint: '#7d828c' },
  { id: 'spotify', name: 'Spotify.exe', base: 0.32, ips: 3, rtt: '41.2 ms', loss: '0.03%', tint: '#3fae6a' },
  // a game download is almost all download: 48.2 Mbps total = 46.9 down + 1.3 up
  { id: 'steam', name: 'steam.exe', base: 0.18, ips: 6, rtt: '46.0 ms', loss: '0.04%', tint: '#5a6b85', peak: 48.2, up: 0.027, wobble: 0.35 },
];
const UP_SHARE = 0.075;
const upShare = a => a.up ?? UP_SHARE;
const LOOP = 15000;
const RAMP_AT = 1500, PEAK_AT = 4800, FALL_AT = 11200, CALM_AT = 13400;
const ROW_H = 38;

const rowsEl = document.getElementById('rows');
const kDown = document.getElementById('kDown');
const kUp = document.getElementById('kUp');
const chart = document.getElementById('chart');
const stage = document.getElementById('demo');
const c1 = stage.querySelector('.c1');
const c2 = stage.querySelector('.c2');

function fmt(mbps) {
  if (mbps >= 100) return `${mbps.toFixed(0)} Mbps`;
  if (mbps >= 10) return `${mbps.toFixed(1)} Mbps`;
  if (mbps >= 1) return `${mbps.toFixed(2)} Mbps`;
  const k = mbps * 1000;
  return k >= 100 ? `${k.toFixed(0)} kbps` : `${k.toFixed(1)} kbps`;
}
const ease = t => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2);

const rows = new Map();
for (const a of APPS) {
  const el = document.createElement('div');
  el.className = 'w-row';
  el.innerHTML = `<span class="app"><i style="background:${a.tint}"></i>${a.name}</span><span class="num tot"></span><span class="num r"></span><span class="num t"></span><span class="num ips">${a.ips}</span><span class="num ips">${a.rtt}</span><span class="num ips">${a.loss}</span>`;
  rowsEl.appendChild(el);
  rows.set(a.id, { el, tot: el.children[1], r: el.children[2], t: el.children[3] });
}

function rateAt(a, t) {
  const wobble = 1 + (a.wobble ?? 1) * (0.045 * Math.sin(t / 610 + a.base * 3.1) + 0.025 * Math.sin(t / 233 + a.ips));
  if (!a.peak) return a.base * wobble;
  let v = a.base;
  if (t >= RAMP_AT && t < PEAK_AT) v = a.base + (a.peak - a.base) * ease((t - RAMP_AT) / (PEAK_AT - RAMP_AT));
  else if (t >= PEAK_AT && t < FALL_AT) v = a.peak;
  else if (t >= FALL_AT && t < CALM_AT) v = a.peak + (a.base - a.peak) * ease((t - FALL_AT) / (CALM_AT - FALL_AT));
  return v * wobble;
}

let order = APPS.map(a => a.id);
let lastSort = -1;
const history = [];
let lastSample = -1;

function draw(t, now) {
  let down = 0, up = 0;
  const rates = {};
  for (const a of APPS) {
    const v = rateAt(a, t);
    rates[a.id] = v;
    down += v * (1 - upShare(a));
    up += v * upShare(a);
    const r = rows.get(a.id);
    r.tot.textContent = fmt(v);
    r.r.textContent = fmt(v * (1 - upShare(a)));
    r.t.textContent = fmt(v * upShare(a));
  }
  // re-rank a couple of times a second, like the app's live table
  if (now - lastSort > 450 || lastSort < 0) {
    lastSort = now;
    const prev = new Map(order.map((id, i) => [id, i]));
    order = APPS.map(a => a.id).sort((x, y) => rates[y] - rates[x]);
    order.forEach((id, i) => {
      const el = rows.get(id).el;
      el.classList.toggle('rising', i < prev.get(id)); // the row moving up slides over the one it passes
      el.style.transform = `translateY(${i * ROW_H}px)`;
    });
  }
  kDown.textContent = fmt(down);
  kUp.textContent = fmt(up);

  if (now - lastSample > 180 || lastSample < 0) {
    lastSample = now;
    history.push([down, up]);
    if (history.length > 80) history.shift();
    plot();
  }
  const story = t >= PEAK_AT + 400 && t < FALL_AT - 200;
  const who = story && t >= PEAK_AT + 1400;
  const steam = rows.get('steam').el;
  steam.classList.toggle('hot', rates.steam > 30);
  steam.classList.toggle('ipfocus', who); // c2 is about this row's remote IPs
  c1.classList.toggle('show', story);
  c2.classList.toggle('show', who);
}

function plot() {
  const max = 80;
  const n = 80;
  const x = i => (i + (n - history.length)) * (400 / (n - 1));
  const y = v => 88 - Math.min(v, max) / max * 82;
  let rx = '', tx = '';
  history.forEach(([d, u], i) => {
    rx += `${i ? 'L' : 'M'}${x(i).toFixed(1)} ${y(d).toFixed(1)}`;
    tx += `${i ? 'L' : 'M'}${x(i).toFixed(1)} ${y(u).toFixed(1)}`;
  });
  const [area, lineRx, lineTx] = chart.children;
  lineRx.setAttribute('d', rx);
  lineTx.setAttribute('d', tx);
  if (history.length > 1) area.setAttribute('d', `${rx}L${x(history.length - 1).toFixed(1)} 90L${x(0).toFixed(1)} 90Z`);
}

function placeCallouts() {
  // Callouts sit just under the top row; a leader line runs up into the row
  // to the cell they talk about (c1: the app name, c2: its Remote IPs count).
  const rowTop = rowsEl.offsetTop; // .w-rows is positioned, so this is already stage-relative
  const gap = ROW_H + 4; // boxes start on row 3, so no row is half covered
  stage.style.setProperty('--row1', `${rowTop + ROW_H + gap}px`);
  stage.style.setProperty('--lead-h', `${gap + ROW_H / 2}px`);
  // c2 covers whole cells only: from the gap after Total rate to the visible
  // edge. When that is too narrow (smaller screens), it sits under the window.
  const heads = stage.querySelectorAll('.w-head > span');
  const total = heads[1], ips = heads[4];
  const visibleRight = innerWidth - stage.getBoundingClientRect().left - 10;
  const left = total.offsetLeft + total.offsetWidth + 7;
  const width = visibleRight - left;
  const anchorX = ips.offsetLeft + ips.offsetWidth - 26; // just left of the count, not on it
  const fits = innerWidth > 640 && ips.offsetWidth > 0 && width >= 210 && anchorX < visibleRight;
  c2.classList.toggle('anchored', fits);
  if (fits) {
    c2.style.left = `${left}px`;
    c2.style.width = `${width}px`;
    c2.style.setProperty('--lead', `${anchorX - left}px`);
  } else {
    c2.style.left = '';
    c2.style.width = '';
  }
}

// The poster: the payoff frame, with the chart already showing the climb. It is
// what reduced motion shows, and where the loop starts, so the first thing a
// visitor sees is the result rather than a quiet table.
const POSTER_AT = PEAK_AT + 2600;
function poster() {
  history.length = 0;
  for (let i = 0; i < 80; i++) {
    const tt = Math.max(0, POSTER_AT - (79 - i) * 180);
    let d = 0, u = 0;
    for (const a of APPS) { const v = rateAt(a, tt); d += v * (1 - upShare(a)); u += v * upShare(a); }
    history.push([d, u]);
  }
  lastSample = Infinity;
  lastSort = -1;
  draw(POSTER_AT, 0);
  plot();
  lastSample = -1;
}

let running = false, raf = 0, start = 0, visible = false, resumeAt = POSTER_AT;
function frame(now) {
  if (!running) return;
  if (!start) start = now - resumeAt;
  const t = (now - start) % LOOP;
  resumeAt = t;
  draw(t, now);
  raf = requestAnimationFrame(frame);
}
function play() {
  if (running || reduce.matches || !visible || document.hidden) return;
  running = true;
  start = 0;
  lastSort = -1;
  raf = requestAnimationFrame(frame);
}
function stop() { running = false; cancelAnimationFrame(raf); }

placeCallouts();
poster();
addEventListener('resize', placeCallouts);
new IntersectionObserver(entries => {
  visible = entries.some(e => e.isIntersecting);
  visible ? play() : stop();
}, { threshold: 0.15 }).observe(stage);
document.addEventListener('visibilitychange', () => (document.hidden ? stop() : play()));
reduce.addEventListener('change', () => { if (reduce.matches) { stop(); poster(); } else play(); });
