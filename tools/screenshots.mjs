// Render the actual dashboard with synthetic fixtures; no live traffic is read.
// Requires Node.js 22+ and Chrome. Run from the repository: node tools/screenshots.mjs
import http from 'node:http';
import { readFile, writeFile, mkdir, unlink } from 'node:fs/promises';
import { resolve, join, extname, sep } from 'node:path';
import { spawn } from 'node:child_process';
import { once } from 'node:events';

const root = process.cwd();
const now = Math.floor(Date.now() / 1000);
const wave = (i, phase = 0) => 1 + 0.12 * Math.sin(i * 0.23 + phase) + 0.06 * Math.sin(i * 1.13 + phase);
const series = Array.from({ length: 300 }, (_, i) => ({ t: now - 299 + i, in: Math.round(5.55e6 * wave(i)), out: Math.round(462500 * wave(i, 2)) }));
const names = ['chrome.exe', 'steam.exe', 'ms-teams.exe', 'OneDrive.exe', 'Discord.exe', 'pwsh.exe', 'msedgewebview2.exe', 'svchost.exe', 'Spotify.exe', 'explorer.exe', 'SearchHost.exe', 'RuntimeBroker.exe', 'ApplicationFrameHost.exe', 'Widgets.exe'];
const rates = [15.3, 9.92, 5.41, 4.36, 2.74, 2.58, 1.25, 1, 0.519, 0.478, 0.351, 0.229, 0.152, 0.111];
const apps = names.map((name, i) => ({ key: name, name, path: 'C:\\Applications\\' + name, pids: String(24688 + i * 117), rateIn: rates[i] * 1e6 / 8, rateOut: rates[i] * 1e6 / 8 * 0.08, in: Math.round(364e6 / (1 + i * 0.6)), out: Math.round(30.2e6 / (1 + i * 0.6)), total: Math.round(394.2e6 / (1 + i * 0.6)), ips: i === 0 ? 33 : Math.max(1, 14 - i), flows: i === 0 ? 36 : Math.max(1, 18 - i), first: now - 2400, last: now, rtt: i === 0 ? 46.9 : 12 + i * 6.7, retrans: 100, retransPct: 0.15, lossInPct: i === 0 ? 0.25 : i === 4 ? 1.5 : 0.04, spark: Array.from({ length: 60 }, (_, j) => rates[i] * 1e6 / 8 * wave(j, i)) }));
const networks = [
  ['1.1.1.1', 'one.one.one.one', 'CA', 'Canada', 'Toronto', 13335, 'Cloudflare, Inc.'],
  ['8.8.8.8', 'dns.google', 'US', 'United States', 'Mountain View', 15169, 'Google LLC'],
  ['9.9.9.9', 'dns.quad9.net', 'CH', 'Switzerland', 'Zurich', 19281, 'Quad9'],
  ['1.0.0.1', 'one.one.one.one', 'GB', 'United Kingdom', 'London', 13335, 'Cloudflare, Inc.'],
  ['8.8.4.4', 'dns.google', 'DE', 'Germany', 'Frankfurt', 15169, 'Google LLC'],
];
const ips = Array.from({ length: 14 }, (_, i) => { const n = networks[i % networks.length]; return { ip: i < 5 ? n[0] : `203.0.113.${i + 10}`, host: i < 5 ? n[1] : `cdn-${i}.example.com`, ptr: i < 5 ? n[1] : `edge-${i}.example.com`, cc: n[2], country: n[3], city: n[4], region: '', asn: n[5], asOrg: n[6], network: '203.0.113.0/24', scope: '', rateIn: 1.4e6 / (i + 1), rateOut: 90000 / (i + 1), in: 242e6 / (i + 1), out: 18e6 / (i + 1), total: 260e6 / (i + 1), apps: names.slice(0, 2 + i % 3).join(', '), appKeys: names.slice(0, 2), ports: '443, 53', first: now - 2400, last: now, rtt: 11.4 + i * 3.6, retransPct: 0.12, lossInPct: 0, retrans: 0 }; });
const monitors = [['Cloudflare DNS', '1.1.1.1', 11.4], ['Google DNS', '8.8.8.8', 12.0], ['Quad9 DNS', '9.9.9.9', 12.5]].map(([name, target, avg], i) => ({ id: i + 1, name, target, addr: target, method: 'icmp', port: 0, interval: 2, enabled: true, created: now - 600, last: avg, sent: 300, recv: 300, loss: 0, min: avg - 2.1, avg, max: avg + 4.1, jitter: 1.86, health: 'ok', spark: Array.from({ length: 60 }, (_, j) => avg * wave(j, i)) }));
const history = Array.from({ length: 450 }, (_, i) => ({ t: now - 898 + i * 2, cpu: 22 + 7 * Math.sin(i / 14), maxCore: 44 + 15 * Math.sin(i / 10), dpc: 1.2, mem: 92 + i / 150, diskBusy: 15 * wave(i), diskR: 6e6 * wave(i), diskW: 3e6 * wave(i, 1), diskLat: 1.4, netRx: 5.55e6 * wave(i), netTx: 462500 * wave(i, 2), outSegs: 3000, retrans: 2, udpErrs: 0 }));
const system = {
  ready: true, host: 'DEMO-PC', os: 'windows', uptime: 145800, t: now * 1000,
  cpu: { busy: 23, user: 17, system: 6, dpc: 0.8, irq: 0.4, hasDpc: true, hasWait: false, count: 16, cores: Array.from({ length: 16 }, (_, i) => 13 + (i * 17) % 52) },
  mem: { pct: 95, total: 17.1e9, used: 16.245e9, avail: 855e6, commitUsed: 26.1e9, commitLimit: 30e9 },
  diskIO: { has: true, split: true, read: 6e6, write: 3e6, busy: 16, latency: 1.4, queue: 0.2 },
  tcp: { has: true, outSegs: 3000, inSegs: 4300, retrans: 2, retransPct: 0.067, retransPct1: 0.067, resets: 0.2, fails: 0, estab: 86, inErrs: 0, udpErrs: 0, udpIn: 24, udpOut: 19 },
  history, hints: [{ level: 'bad', area: 'mem', title: 'Memory is nearly full', text: 'Physical memory has stayed above 92%. Close an unused application and check whether performance improves.' }], notes: [],
  ifaces: [{ id: 'demo-ethernet', name: 'Ethernet', desc: 'Gigabit Ethernet adapter', kind: 'ethernet', physical: true, up: true, speed: 1e9, speedTx: 1e9, mtu: 1500, mac: '02:00:00:00:00:01', rx: 5.55e6, tx: 462500, rxUtil: 4.44, txUtil: 0.37, rxBytes: 1.09e9, txBytes: 89.7e6, errs: 0, discards: 0, errsRecent: 0, discardsRecent: 0 }],
  disks: [{ mount: 'C:\\', label: 'System', fs: 'NTFS', total: 1e12, free: 640e9, pct: 36 }],
  procs: apps.map((a, i) => ({ pid: Number(a.pids), name: a.name, path: a.path, cpu: 5 / (i + 1), mem: Math.round(1.3e9 / (i + 1)), io: 1e6 / (i + 1), threads: 24 + i, handles: 300 + i * 16, app: a.key, netIn: a.rateIn, netOut: a.rateOut })),
};
const status = { app: 'packettrail', version: '1.0.0', copyright: '© 2026 Matt Burnett', os: 'windows', arch: 'amd64', hostname: 'DEMO-PC', capture: { backend: 'demo', mode: 'full', privileged: false, events: 42000, lost: 0 }, canElevate: false, dataDir: 'PacketTrail\\data', dbPath: 'PacketTrail\\data\\packettrail-demo.db', dbSize: 1.1e6, geo: { city: true, cityDate: '2026-11', asnDate: '2026-11' }, start: now - 2400, totalIn: 1.09e9, totalOut: 89.7e6, rateIn: 5.55e6, rateOut: 462500, listSep: ',', token: 'synthetic-fixture', exportDir: 'PacketTrail\\exports' };

function fixture(url) {
  switch (url.pathname) {
    case '/api/status': return status;
    case '/api/live': return { t: now, totalIn: status.totalIn, totalOut: status.totalOut, rateIn: status.rateIn, rateOut: status.rateOut, series };
    case '/api/apps': return apps;
    case '/api/ips': return ips;
    case '/api/asns': return [];
    case '/api/flows': return [];
    case '/api/monitors': return { monitors, suggest: [] };
    case '/api/monitors/series': { const m = monitors[Number(url.searchParams.get('id')) - 1] || monitors[0]; return { step: 2, since: now - 600, now, points: Array.from({ length: 300 }, (_, i) => ({ t: now - 598 + i * 2, sent: 1, recv: 1, min: m.avg * wave(i), avg: m.avg * wave(i), max: m.avg * wave(i), sd: 0 })) }; }
    case '/api/system': return system;
    case '/api/series': return { points: series, step: 1, now, since: now - 300 };
    case '/api/peering': return { own: { asns: [], source: 'manual' }, ownPdb: [], networks: [], local: 0, onNet: 0, offNet: 0 };
    default: return undefined;
  }
}

const mime = { '.html': 'text/html', '.css': 'text/css', '.js': 'text/javascript', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2' };
const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, 'http://127.0.0.1');
  const data = fixture(url);
  if (data !== undefined) { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(data)); return; }
  if (url.pathname.startsWith('/api/')) { res.writeHead(404); res.end(); return; }
  const isSite = url.pathname.startsWith('/site/');
  const base = join(root, isSite ? 'docs' : 'internal/web/static');
  const relative = isSite ? url.pathname.slice(6) : url.pathname.slice(1);
  const path = resolve(base, relative || 'index.html');
  if (!path.startsWith(base + sep)) { res.writeHead(403); res.end(); return; }
  try { const bytes = await readFile(path); res.setHeader('Content-Type', mime[extname(path)] || 'application/octet-stream'); res.end(bytes); }
  catch { res.writeHead(404); res.end(); }
});
server.listen(0, '127.0.0.1');
await once(server, 'listening');
const baseUrl = `http://127.0.0.1:${server.address().port}`;
const profile = join(root, 'dist', 'qa-browser');
await mkdir(profile, { recursive: true });
await unlink(join(profile, 'DevToolsActivePort')).catch(e => { if (e.code !== 'ENOENT') throw e; });
await mkdir(join(root, 'dist', 'qa'), { recursive: true });
const chrome = spawn(process.env.CHROME_PATH || 'C:/Program Files/Google/Chrome/Application/chrome.exe', ['--headless=new', '--no-first-run', '--no-default-browser-check', '--disable-extensions', '--remote-debugging-port=0', '--enable-unsafe-swiftshader', `--user-data-dir=${profile}`, 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'], windowsHide: true });
const pause = ms => new Promise(r => setTimeout(r, ms));
let socket;
const errors = [];
try {
  let port;
  for (let i = 0; i < 100; i++) { try { port = Number((await readFile(join(profile, 'DevToolsActivePort'), 'utf8')).split('\n')[0]); break; } catch { await pause(100); } }
  if (!port) throw new Error('Chrome did not expose a debugging port');
  const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
  const target = targets.find(t => t.type === 'page');
  socket = new WebSocket(target.webSocketDebuggerUrl);
  await once(socket, 'open');
  const pending = new Map(); let nextId = 0;
  socket.addEventListener('message', event => { const m = JSON.parse(event.data); if (m.id) { const p = pending.get(m.id); if (!p) return; pending.delete(m.id); m.error ? p.reject(new Error(JSON.stringify(m.error))) : p.resolve(m.result); } else if (m.method === 'Runtime.exceptionThrown') errors.push(m.params.exceptionDetails.exception?.description || m.params.exceptionDetails.text); });
  const call = (method, params = {}) => new Promise((resolve, reject) => { const id = ++nextId; pending.set(id, { resolve, reject }); socket.send(JSON.stringify({ id, method, params })); });
  const evaluate = async expression => { const r = await call('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error(r.exceptionDetails.text); return r.result.value; };
  await call('Page.enable'); await call('Runtime.enable');
  await call('Page.addScriptToEvaluateOnNewDocument', { source: `localStorage.setItem('pt.theme', JSON.stringify('dark')); localStorage.setItem('pt.tourSeen', 'true'); localStorage.setItem('pt.view', 'plain');` });
  async function viewport(width, height, theme) {
    await call('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile: false });
    await call('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: theme }, { name: 'prefers-reduced-motion', value: 'reduce' }] });
  }
  async function navigate(url) { await call('Page.navigate', { url }); for (let i = 0; i < 80; i++) { await pause(100); if (await evaluate(`document.readyState === 'complete'`)) break; } await pause(1000); }
  async function screenshot(path) { const result = await call('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false }); await writeFile(path, Buffer.from(result.data, 'base64')); }
  await viewport(1600, 900, 'dark');
  for (const tab of ['apps', 'ips', 'monitors', 'system']) {
    await navigate(`${baseUrl}/?notour&tab=${tab}`);
    await evaluate(`applyTheme('dark', false)`);
    await pause(500);
    await screenshot(join(root, 'docs', 'assets', `${tab}-dark.png`));
    if (tab === 'apps') { await screenshot(join(root, 'docs', 'assets', 'overview-dark.png')); await evaluate(`applyTheme('light', false)`); await pause(500); await screenshot(join(root, 'docs', 'assets', 'apps-light.png')); }
    console.log(`Captured ${tab} dashboard with synthetic fixtures`);
  }
  const checks = [];
  for (const [width, height, name] of [[1440, 1000, 'desktop'], [390, 844, 'mobile']]) {
    await viewport(width, height, 'dark');
    await navigate(baseUrl + '/site/');
    await screenshot(join(root, 'dist', 'qa', `website-${name}.png`));
    checks.push(await evaluate(`({width: innerWidth, overflow: document.documentElement.scrollWidth > innerWidth, title: document.title, brokenImages: [...document.images].filter(i => i.complete && i.naturalWidth === 0).map(i => i.src), donation: [...document.links].some(a => a.href.includes('paypal.me/matthewbrianburnett')), branding: document.body.innerText.includes('PacketTrail')})`));
    const toggles = await evaluate(`[...document.querySelectorAll('.seg button')].map(b=>({text:b.textContent.trim(), value:b.dataset.reg}))`);
    console.log(name, JSON.stringify(checks.at(-1)), JSON.stringify(toggles));
    await evaluate(`document.querySelector('.seg button:last-child').click()`);
    const engineer = await evaluate(`document.documentElement.dataset.reg`);
    if (engineer !== 'eng') throw new Error('Engineer view toggle failed');
  }
  await viewport(1440, 1000, 'light');
  await navigate(baseUrl + '/site/');
  await screenshot(join(root, 'dist', 'qa', 'website-light.png'));
  if (checks.some(c => c.overflow || c.brokenImages.length || !c.donation || !c.branding)) throw new Error('Website layout or content check failed: ' + JSON.stringify(checks));
  if (errors.length) throw new Error('Browser errors: ' + errors.join('\n'));
  await writeFile(join(root, 'dist', 'qa', 'checks.json'), JSON.stringify({ checks, errors }, null, 2));
  console.log('Dashboard captures and desktop/mobile website checks passed.');
  await call('Browser.close');
} finally {
  socket?.close();
  chrome.kill();
  server.close();
}
