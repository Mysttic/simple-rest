// Captures the screenshots used by the project page in site/simple-rest/.
//
// It builds the app, starts two copies with prepared data (one current, one
// reporting 0.9.0 against a fake hub so the update card has something to show),
// drives a headless Edge or Chrome over the DevTools protocol and writes the
// PNGs. The fake hub also hands out a token, which a schedule of the current
// copy fetches, and a small lab device answers the TCP console with HL7 ACKs
// over MLLP. No dependencies: Node's own WebSocket talks to the browser.
//
// Run: node scripts/screenshots.mjs        (needs Go and Edge or Chrome)
import { spawn, spawnSync } from 'node:child_process';
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { existsSync } from 'node:fs';
import { createServer } from 'node:http';
import { createServer as createTCPServer } from 'node:net';
import { setTimeout as sleep } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const work = path.join(root, '.e2e', 'shots');
const outDir = path.join(root, 'site', 'simple-rest', 'img');
const APP_PORT = 8795, OLD_PORT = 8796, HUB_PORT = 8797, LAB_PORT = 8798, CDP_PORT = 9333;
const WIDTH = 1600, HEIGHT = 1000;
const exe = n => (process.platform === 'win32' ? n + '.exe' : n);

const BROWSERS = [
  'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Google/Chrome/Application/chrome.exe',
  'C:/Program Files (x86)/Google/Chrome/Application/chrome.exe',
  '/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/microsoft-edge',
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
];

// ---- the state the screenshots show ------------------------------------------------
const ENV_ID = 'env-github', SCHED_ID = 'sched-token', TCP_ID = 'tcp-lab';
const HL7_ORDER = [
  'MSH|^~\\&|SIMPLE-REST|LAB|ANALYZER|LAB|20261002093000||ORM^O01|MSG00042|P|2.5',
  'PID|1||{{patient_id}}||Kowalska^Anna||19850312|F',
  'ORC|NW|ORD-7781',
  'OBR|1|ORD-7781||CBC^Complete blood count',
].join('\n');
const req = (id, name, method, url, over = {}) => ({ id, name, method, url, ...over });
const demoData = {
  version: 1,
  activeEnv: ENV_ID,
  envs: [
    { id: ENV_ID, name: 'github', vars: [
      { enabled: true, name: 'base', value: 'https://api.github.com' },
      { enabled: true, name: 'owner', value: 'Mysttic' },
      { enabled: true, name: 'repo', value: 'Mysttic' },
      { enabled: true, name: 'token', value: 'ghp_tu_wklej_swoj_token' },
      { enabled: false, name: 'base', value: 'http://localhost:3000' },
    ] },
    { id: 'env-local', name: 'local', vars: [
      { enabled: true, name: 'base', value: `http://127.0.0.1:${HUB_PORT}` },
      { enabled: true, name: 'user', value: 'demo' },
      { enabled: true, name: 'password', value: 'demo-password' },
      { enabled: true, name: 'patient_id', value: '100234' },
    ] },
  ],
  collections: [
    { id: 'c1', name: 'GitHub API', open: true, requests: [
      req('r1', 'Repository', 'GET', '{{base}}/repos/{{owner}}/{{repo}}'),
      req('r2', 'Releases', 'GET', '{{base}}/repos/{{owner}}/{{repo}}/releases?per_page=30'),
      req('r3', 'Create issue', 'POST', '{{base}}/repos/{{owner}}/{{repo}}/issues', {
        body: { mode: 'raw', rawType: 'json', form: [], raw: '{\n  "title": "Nie da sie zapisac kolekcji",\n  "labels": ["bug"]\n}' },
        auth: { type: 'bearer', token: '{{token}}', username: '', password: '', keyName: '', keyValue: '', keyIn: 'header' },
        headers: [{ enabled: true, name: 'Accept', value: 'application/vnd.github+json' }],
      }),
      req('r4', 'Update issue', 'PATCH', '{{base}}/repos/{{owner}}/{{repo}}/issues/7'),
      req('r5', 'Close issue', 'DELETE', '{{base}}/repos/{{owner}}/{{repo}}/issues/7/labels/bug'),
    ] },
    { id: 'c2', name: 'Local API', open: false, requests: [
      req('r6', 'Health', 'GET', '{{base}}/health'),
      req('r7', 'Login', 'POST', '{{base}}/auth/login', {
        body: { mode: 'raw', rawType: 'json', form: [], raw: '{\n  "username": "{{user}}",\n  "password": "{{password}}"\n}' },
      }),
    ] },
  ],
  // The schedule logs in against the fake hub and keeps {{token}} fresh.
  schedules: [{
    id: SCHED_ID, name: 'Token refresh', requestId: 'r7', envId: 'env-local',
    everySec: 600, expiresPath: 'expires_in', leadSec: 60,
    extract: [
      { enabled: true, optional: false, var: 'token', from: 'json', path: 'access_token' },
      { enabled: true, optional: true, var: 'refresh', from: 'json', path: 'refresh_token' },
    ],
  }],
  // A lab analyzer on the TCP console: HL7 over MLLP, one Enter is one CR.
  tcp: [{
    id: TCP_ID, name: 'Lab analyzer', host: '127.0.0.1', port: String(LAB_PORT),
    framing: { preset: 'mllp' }, wrap: true, waitReply: true, replyTimeoutMs: 5000,
    mode: 'text', newline: 'cr', escapes: false,
    messages: [
      { id: 'm-order', name: 'Order CBC', mode: 'text', data: HL7_ORDER },
      { id: 'm-enq', name: 'ENQ', mode: 'hex', data: '05' },
    ],
  }],
  history: [],
  settings: { timeoutMs: 30000, followRedirects: true, verifyTls: true, cookies: true, history: true },
};

// ---- a hub with one release, for the update card ---------------------------------------
// readVersion reads VERSION.md the way main.go does: the first non-empty line,
// without heading marks or a leading "v". The hub offers that release, the
// one being made.
function readVersion() {
  for (const line of readFileSync(path.join(root, 'VERSION.md'), 'utf8').split('\n')) {
    const v = line.trim().replace(/^#+/, '').trim();
    if (v) return v.replace(/^v/, '');
  }
  throw new Error('VERSION.md holds no version');
}

// The same server is the login of the schedule: POST /auth/login answers with
// a token that looks like a JWT and lasts 5 minutes.
const b64 = o => Buffer.from(JSON.stringify(o)).toString('base64url');
const LOGIN = JSON.stringify({
  access_token: [b64({ alg: 'HS256', typ: 'JWT' }), b64({ sub: 'demo', scope: 'api' }),
    Buffer.from('not a real signature, made for the screenshot').toString('base64url')].join('.'),
  token_type: 'Bearer', expires_in: 300, refresh_token: 'rt_' + Buffer.from('demo refresh token').toString('hex'),
});

function fakeHub() {
  const version = readVersion();
  const asset = `simple-rest-${version}-${process.platform === 'win32' ? 'windows-amd64.exe' : 'linux-amd64'}`;
  const body = JSON.stringify([{
    tag_name: `simple-rest-v${version}`, draft: false, prerelease: false,
    published_at: new Date().toISOString(), html_url: 'https://example.invalid/release',
    body: `simple-rest ${version}\n\nhttps://mysttic.github.io/Mysttic/simple-rest/`,
    assets: [
      { name: asset, size: 7159296, browser_download_url: `http://127.0.0.1:${HUB_PORT}/dl/${asset}` },
      { name: `simple-rest-${version}-checksums.txt`, size: 294, browser_download_url: `http://127.0.0.1:${HUB_PORT}/dl/sums` },
    ],
  }]);
  const srv = createServer((r, w) => {
    r.resume();
    if (r.method === 'POST' && r.url === '/auth/login') {
      w.writeHead(200, { 'Content-Type': 'application/json' });
      w.end(LOGIN);
      return;
    }
    w.writeHead(r.url.startsWith('/releases') ? 200 : 404, { 'Content-Type': 'application/json' });
    w.end(r.url.startsWith('/releases') ? body : '[]');
  });
  return new Promise(res => srv.listen(HUB_PORT, '127.0.0.1', () => res(srv)));
}

// ---- a lab device for the TCP console ---------------------------------------------------
// It speaks MLLP (0x0B, the message, 0x1C 0x0D) and answers every HL7 message
// with an ACK that names the control id of the message, as an analyzer does.
const MLLP_START = Buffer.from([0x0b]), MLLP_END = Buffer.from([0x1c, 0x0d]);
function hl7Ack(msg) {
  const msh = (msg.split('\r').find(s => s.startsWith('MSH|')) || '').split('|');
  const f = i => msh[i] || '';
  const at = new Date().toISOString().replace(/\D/g, '').slice(0, 14);
  return `MSH|^~\\&|${f(4)}|${f(5)}|${f(2)}|${f(3)}|${at}||ACK^${f(8).split('^')[1] || ''}|ACK${f(9)}|P|2.5\rMSA|AA|${f(9)}\r`;
}
function labDevice() {
  const srv = createTCPServer(sock => {
    let buf = Buffer.alloc(0);
    sock.on('error', () => {});
    sock.on('data', d => {
      buf = Buffer.concat([buf, d]);
      for (;;) {
        const start = buf.indexOf(MLLP_START), end = start < 0 ? -1 : buf.indexOf(MLLP_END, start + 1);
        if (end < 0) break;
        const msg = buf.subarray(start + 1, end).toString('utf8');
        buf = buf.subarray(end + MLLP_END.length);
        sock.write(Buffer.concat([MLLP_START, Buffer.from(hl7Ack(msg)), MLLP_END]));
      }
    });
  });
  return new Promise(res => srv.listen(LAB_PORT, '127.0.0.1', () => res(srv)));
}

// ---- a small DevTools protocol client ------------------------------------------------------
class CDP {
  constructor(ws) {
    this.ws = ws; this.next = 1; this.waiting = new Map(); this.listeners = new Set();
    ws.addEventListener('message', e => {
      const msg = JSON.parse(e.data);
      if (msg.id && this.waiting.has(msg.id)) {
        const { resolve, reject } = this.waiting.get(msg.id);
        this.waiting.delete(msg.id);
        msg.error ? reject(new Error(msg.error.message)) : resolve(msg.result);
      } else if (msg.method) {
        for (const fn of this.listeners) fn(msg);
      }
    });
  }
  static async open(url) {
    const ws = new WebSocket(url);
    await new Promise((res, rej) => { ws.addEventListener('open', res); ws.addEventListener('error', () => rej(new Error('cannot reach ' + url))); });
    return new CDP(ws);
  }
  send(method, params = {}, sessionId) {
    const id = this.next++;
    return new Promise((resolve, reject) => {
      this.waiting.set(id, { resolve, reject });
      this.ws.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }));
    });
  }
  once(method) {
    return new Promise(res => {
      const fn = m => { if (m.method === method) { this.listeners.delete(fn); res(m.params); } };
      this.listeners.add(fn);
    });
  }
}

class Page {
  constructor(cdp, sessionId) { this.cdp = cdp; this.sid = sessionId; }
  cmd(method, params) { return this.cdp.send(method, params, this.sid); }
  async goto(url) {
    const loaded = this.cdp.once('Page.loadEventFired');
    await this.cmd('Page.navigate', { url });
    await loaded;
  }
  async eval(expression) {
    const r = await this.cmd('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || 'evaluate failed');
    return r.result.value;
  }
  async until(expression, what, tries = 100) {
    for (let i = 0; i < tries; i++) {
      if (await this.eval(`!!(${expression})`)) return;
      await sleep(100);
    }
    throw new Error('timed out waiting for ' + what);
  }
  // shot writes a PNG. With `clip` it crops to that element plus `pad`; with
  // `fromTop` it keeps the full width from the top of the window down to the
  // bottom of the element, which is how the toolbar stays in the picture.
  async shot(file, { clip: selector, pad = 24, padTop = null, fromTop = false } = {}) {
    let clip;
    if (selector) {
      const top = padTop ?? pad;
      const b = await this.eval(`(() => { const b = document.querySelector(${JSON.stringify(selector)}).getBoundingClientRect();
        return { x: b.x, y: b.y, width: b.width, height: b.height, bottom: b.bottom }; })()`);
      clip = fromTop
        ? { x: 0, y: 0, width: WIDTH, height: Math.ceil(b.bottom) + 8, scale: 1 }
        : { x: Math.max(0, b.x - pad), y: Math.max(0, b.y - top), width: b.width + pad * 2, height: b.height + top + pad, scale: 1 };
    }
    const { data } = await this.cmd('Page.captureScreenshot', { format: 'png', ...(clip ? { clip } : {}) });
    writeFileSync(file, Buffer.from(data, 'base64'));
    console.log('  ' + path.relative(root, file));
  }
}

// ---- running things ------------------------------------------------------------------------------
const children = [];
function start(cmd, args, name) {
  const p = spawn(cmd, args, { cwd: root, stdio: ['ignore', 'pipe', 'pipe'] });
  p.stderr.on('data', d => process.env.VERBOSE && console.log(`[${name}] ${d}`));
  children.push(p);
  return p;
}
async function waitForHTTP(url, what) {
  for (let i = 0; i < 120; i++) {
    try { const r = await fetch(url); if (r.ok) return await r.json().catch(() => ({})); } catch {}
    await sleep(250);
  }
  throw new Error(what + ' did not come up: ' + url);
}
// enableSchedule turns schedule id on, as the Enabled box in the page does,
// and waits for its first run to set the token.
async function enableSchedule(id) {
  const api = `http://127.0.0.1:${APP_PORT}/api/schedules`;
  const r = await fetch(`${api}/${id}/enable`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
  if (r.status !== 200) throw new Error(`turning the schedule on answered ${r.status}: ${await r.text()}`);
  for (let i = 0; i < 100; i++) {
    const job = (await (await fetch(api)).json()).jobs[id] || {};
    if (job.state === 'ok') return;
    if (job.state === 'stopped' || job.state === 'config') {
      throw new Error('the schedule did not set the token: ' + (job.blocked || job.lastError || (job.problems || []).join('; ')));
    }
    await sleep(100);
  }
  throw new Error('timed out waiting for the schedule to set the token');
}
function cleanup() {
  for (const p of children) { try { p.kill(); } catch {} }
  if (process.platform === 'win32') {
    for (const n of ['simple-rest-shots.exe', 'simple-rest-shots-old.exe']) {
      spawnSync('taskkill', ['/F', '/IM', n], { stdio: 'ignore' });
    }
  }
}

async function main() {
  const browser = BROWSERS.find(existsSync);
  if (!browser) throw new Error('no Edge or Chrome found, add its path to BROWSERS');

  rmSync(work, { recursive: true, force: true });
  mkdirSync(work, { recursive: true });
  mkdirSync(outDir, { recursive: true });

  console.log('building');
  for (const [out, flags] of [[exe('simple-rest-shots'), []], [exe('simple-rest-shots-old'), ['-ldflags', '-X main.versionOverride=0.9.0']]]) {
    const r = spawnSync('go', ['build', ...flags, '-o', path.join(work, out), '.'], { cwd: root, stdio: 'inherit' });
    if (r.status !== 0) throw new Error('go build failed');
  }
  const dataFile = path.join(work, 'app-data.json');
  writeFileSync(dataFile, JSON.stringify(demoData));

  console.log('starting the app, an older copy, a fake hub and a lab device');
  const hub = await fakeHub();
  const lab = await labDevice();
  start(path.join(work, exe('simple-rest-shots')), ['-no-browser', '-listen', `127.0.0.1:${APP_PORT}`, '-data', dataFile], 'app');
  start(path.join(work, exe('simple-rest-shots-old')), ['-no-browser', '-listen', `127.0.0.1:${OLD_PORT}`,
    '-data', path.join(work, 'old-data.json'), '-update-url', `http://127.0.0.1:${HUB_PORT}/releases`], 'old');
  await waitForHTTP(`http://127.0.0.1:${APP_PORT}/api/info`, 'the app');
  await waitForHTTP(`http://127.0.0.1:${OLD_PORT}/api/info`, 'the older copy');
  // Every start turns the schedules off, so the token refresh is turned on
  // here; the top bar in the shots counts it.
  await enableSchedule(SCHED_ID);

  const profile = path.join(work, 'browser-profile');
  start(browser, ['--headless=new', `--remote-debugging-port=${CDP_PORT}`, `--user-data-dir=${profile}`,
    '--no-first-run', '--no-default-browser-check', '--disable-gpu', '--hide-scrollbars',
    `--window-size=${WIDTH},${HEIGHT}`, 'about:blank'], 'browser');
  const version = await waitForHTTP(`http://127.0.0.1:${CDP_PORT}/json/version`, 'the browser');
  const cdp = await CDP.open(version.webSocketDebuggerUrl);
  const { targetId } = await cdp.send('Target.createTarget', { url: 'about:blank' });
  const { sessionId } = await cdp.send('Target.attachToTarget', { targetId, flatten: true });
  const page = new Page(cdp, sessionId);
  await page.cmd('Page.enable');
  await page.cmd('Runtime.enable');
  await page.cmd('Emulation.setDeviceMetricsOverride', { width: WIDTH, height: HEIGHT, deviceScaleFactor: 1, mobile: false });

  console.log('capturing');

  // 1. The main view: collections, an environment, a real answer with coloured JSON.
  await page.goto(`http://127.0.0.1:${APP_PORT}/`);
  await page.until(`document.querySelector('#url')`, 'the UI');
  await page.until(`document.querySelector('#schedCount').textContent === '1'`, 'the schedules button');
  await page.eval(`(() => {
    const items = [...document.querySelectorAll('#collList .item')];
    items.find(i => i.textContent.includes('Repository')).click();
  })()`);
  await page.until(`document.querySelector('#url').value.includes('repos')`, 'the request to load');
  await page.eval(`document.querySelector('#sendBtn').click()`);
  await page.until(`/^\\d/.test(document.querySelector('#respStatus').textContent)`, 'the response', 200);
  const status = await page.eval(`document.querySelector('#respStatus').textContent`);
  if (!status.startsWith('200')) throw new Error('the demo request answered ' + status + ' (network?)');
  // Give the answer most of the window, the way anyone reading a response would.
  await page.eval(`document.documentElement.style.setProperty('--req-h', '250px')`);
  await sleep(300);
  await page.shot(path.join(outDir, 'promo.png'));

  // 2. The request editor: body, variables, auth. Cropped to the editor, so no
  // answer to the previous request hangs underneath it.
  await page.eval(`(() => {
    [...document.querySelectorAll('#collList .item')].find(i => i.textContent.includes('Create issue')).click();
  })()`);
  await page.until(`document.querySelector('#method').value === 'POST'`, 'the POST request');
  await page.eval(`document.querySelector('[data-tab="body"]').click()`);
  await page.eval(`document.documentElement.style.setProperty('--req-h', '430px')`);
  await sleep(250);
  await page.shot(path.join(outDir, 'request.png'), { clip: '.request', fromTop: true });

  // 3. Environments with their variables.
  await page.eval(`document.querySelector('#envBtn').click()`);
  await page.until(`document.querySelector('#envModal.open')`, 'the environments window');
  await sleep(250);
  await page.shot(path.join(outDir, 'environments.png'), { clip: '#envModal .modal' });
  await page.eval(`document.querySelector('#envModal [data-close]').click()`);

  // 4. Schedules: the token refresh turned on above, its last run and the
  // values it set. The tab first picks the environment of the schedule, the
  // way Select it in the window would.
  await page.eval(`(() => {
    const s = document.querySelector('#envSelect');
    s.value = 'env-local';
    s.dispatchEvent(new Event('change'));
  })()`);
  await page.eval(`document.querySelector('#schedBtn').click()`);
  await page.until(`document.querySelector('#schedModal.open')
    && [...document.querySelectorAll('#schedRun .k')].some(k => k.textContent === 'token')`, 'the schedules window');
  await sleep(250);
  await page.shot(path.join(outDir, 'schedules.png'), { clip: '#schedModal .modal' });
  await page.eval(`document.querySelector('#schedModal [data-close]').click()`);

  // 5. The TCP console: connected to the lab device, one order sent with a
  // variable from the environment of the tab, and the ACK it answered with.
  await page.eval(`document.querySelector('[data-side="tcp"]').click()`);
  await page.until(`document.querySelector('#tcpList [data-tcp="${TCP_ID}"]')`, 'the TCP connection in the sidebar');
  await page.eval(`document.querySelector('#tcpList [data-tcp="${TCP_ID}"]').click()`);
  await page.until(`!document.querySelector('#tcpMain').hidden`, 'the TCP view');
  await page.eval(`document.querySelector('#tcpConnBtn').click()`);
  await page.until(`document.querySelector('#tcpStatus').textContent.includes('Connected')`, 'the TCP connection', 100);
  await page.eval(`(() => {
    const t = document.querySelector('#tcpData');
    t.value = ${JSON.stringify(HL7_ORDER)};
    t.dispatchEvent(new Event('input', { bubbles: true }));
  })()`);
  await page.eval(`document.querySelector('#tcpSendBtn').click()`);
  await page.until(`document.querySelector('#tcpReply').textContent.startsWith('Reply in')`, 'the ACK of the lab device', 100);
  await sleep(300);
  await page.shot(path.join(outDir, 'tcp.png'));

  // 6. The update card, on the copy that reports 0.9.0.
  await page.goto(`http://127.0.0.1:${OLD_PORT}/`);
  await page.until(`document.querySelector('#settingsBtn')`, 'the older copy');
  await page.eval(`document.querySelector('#settingsBtn').click()`);
  await page.until(`document.querySelector('#settingsModal.open')`, 'the settings window');
  await page.eval(`document.querySelector('#updCheckBtn').click()`);
  await page.until(`document.querySelector('#updInstallBtn')`, 'the update card');
  await sleep(250);
  // Only the Updates part: the rest of the dialog carries this machine's paths.
  await page.shot(path.join(outDir, 'update.png'), { clip: '#updSection', pad: 20, padTop: 2 });

  hub.close();
  lab.close();
  console.log('done');
}

process.on('exit', cleanup);
process.on('SIGINT', () => { cleanup(); process.exit(130); });
try {
  await main();
  cleanup();
  process.exit(0);
} catch (e) {
  console.error('FAIL: ' + e.message);
  cleanup();
  process.exit(1);
}
