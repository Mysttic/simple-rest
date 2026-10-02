// Node tests for the TCP console in ui.html: how the editor turns a message
// into the parts the program sends, the bytes the page shows, the twins of
// tcpframe.go and tcp.go on the shared vectors of testdata/tcp-framing.json,
// what the page keeps of the events of the program, the event stream with its
// fallback to polling, and the actions of the console. fetch, EventSource,
// the DOM, the timers and the storages are fakes.
// Run: node --test "tests/**/*.test.mjs"
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { loadUI, fakeDom, fakeTimers, settle } from './load-ui.mjs';

const copy = o => JSON.parse(JSON.stringify(o));
const ID = /^[A-Za-z0-9_-]{1,64}$/;
const vectors = JSON.parse(readFileSync(new URL('../testdata/tcp-framing.json', import.meta.url), 'utf8'));
// latin1 turns a byte string of the vectors into its bytes, and text into
// those of its UTF-8.
const latin1 = s => Uint8Array.from(s, c => c.charCodeAt(0));
const utf8 = s => new TextEncoder().encode(s);
const b64 = s => Buffer.from(s, 'latin1').toString('base64');
const same = (a, b) => assert.deepEqual([...a], [...b]);

// parts shows the parts of tcpEncode as byte lists and {v}.
const parts = (sr, enc) => enc.parts.map(p => (p.b64 !== undefined ? [...sr.b64ToBytes(p.b64)] : { v: p.v }));
const bytesOf = (sr, enc) => enc.parts.flatMap(p => [...sr.b64ToBytes(p.b64)]);

const DOC = {
  version: 1, settings: {}, history: [], activeEnv: 'e1', collections: [],
  envs: [
    { id: 'e1', name: 'local', vars: [{ name: 'lab_host', value: '10.0.0.5' }, { name: 'patient_id', value: '12345' }] },
    { id: 'e2', name: 'prod', vars: [{ name: 'lab_host', value: '10.9.9.9' }, { name: 'patient_id', value: '999' }] },
  ],
  schedules: [{ id: 's2', name: 'Prod token', requestId: 'r', envId: 'e2', extract: [{ var: 'token', path: 'access_token' }] }],
  tcp: [{
    id: 'lab', name: 'Lab analyzer', host: '{{lab_host}}', port: '2575', framing: { preset: 'mllp' }, waitReply: true, newline: 'cr',
    messages: [{ id: 'm1', name: 'ORM^O01', mode: 'text', data: 'MSH|^~\\&|A\nPID|1||{{patient_id}}' }],
  }],
};

// si, list and entry are what the program says of a connection (tcp.go).
const si = (over = {}) => ({
  id: 'lab', rev: 5, conn: 1, state: 'connected', envId: 'e1', envName: 'local', hostTpl: '{{lab_host}}', portTpl: '2575', host: '10.0.0.5', port: 2575,
  local: '10.0.0.2:51234', remote: '10.0.0.5:2575', connectedForMs: 1000, bytesIn: 0, bytesOut: 0, error: '', framing: { preset: 'mllp' }, idleMs: 50,
  pending: 0, waiting: 0, openedBy: '127.0.0.1', ...over,
});
const list = (sessions, over = {}) => ({ boot: 'b1', seq: 10, client: '127.0.0.1', sessions, ...over });
const entry = (seq, over = {}) => ({ seq, conn: 1, at: 1790000000000, dir: 'rx', size: 1, data: b64('x'), ...over });

function res(status, body = '', headers = {}) {
  const get = n => { const k = Object.keys(headers).find(h => h.toLowerCase() === n.toLowerCase()); return k ? headers[k] : null; };
  return { status, ok: status >= 200 && status < 300, headers: { get }, text: async () => body, json: async () => JSON.parse(body) };
}

// fakeProgram answers like the program: /api/info, /api/state with ETag and
// If-Match, /api/schedules with 204, GET /api/tcp from p.tcp and the logs
// from p.logs. A test answers any other route through routes: a function of
// (url, init) per path, which takes a query too.
function fakeProgram(doc) {
  const p = { version: '1.2.0', os: 'linux', text: JSON.stringify(doc), tag: '"t1"', n: 1, calls: [], routes: {}, tcp: list([]), logs: {} };
  p.change = d => { p.text = JSON.stringify(d); p.tag = `"t${++p.n}"`; };
  p.count = (method, url) => p.calls.filter(c => c.method === method && c.url === url).length;
  p.body = url => JSON.parse(p.calls.filter(c => c.url === url).at(-1).body);
  p.urls = () => p.calls.map(c => c.method + ' ' + c.url);
  p.fetch = async (url, init = {}) => {
    const method = init.method || 'GET', h = init.headers || {};
    p.calls.push({ method, url, headers: h, body: init.body });
    const route = Object.keys(p.routes).find(k => url === k || url.startsWith(k + '?'));
    if (route) return p.routes[route](url, init);
    if (url === '/api/info') return res(200, JSON.stringify({ mode: 'server', version: p.version, os: p.os, arch: 'amd64', dataPath: '/d.json', boot: 'b1' }));
    if (url.startsWith('/api/schedules')) return res(204);
    if (url === '/api/tcp') return res(200, JSON.stringify(p.tcp));
    const log = url.match(/^\/api\/tcp\/([^/?]+)\/log\?/);
    if (log && method === 'GET') {
      return p.tcp.sessions.some(s => s.id === log[1])
        ? res(200, JSON.stringify({ boot: p.tcp.boot, seq: p.tcp.seq, dropped: 0, entries: p.logs[log[1]] || [] }))
        : res(404, '{"error":"TCP connection not found"}');
    }
    if (url !== '/api/state') return res(404, '{"error":"not found"}');
    if (method === 'GET') return h['If-None-Match'] === p.tag ? res(304, '', { ETag: p.tag }) : res(200, p.text, { ETag: p.tag });
    if (h['If-Match'] !== p.tag) return res(412, '{"error":"changed"}', { ETag: p.tag });
    p.text = init.body; p.tag = `"t${++p.n}"`;
    return res(204, '', { ETag: p.tag });
  };
  return p;
}

const fakeStorage = (init = {}) => {
  const m = new Map(Object.entries(init));
  return { getItem: k => (m.has(k) ? m.get(k) : null), setItem: (k, v) => { m.set(k, String(v)); }, removeItem: k => { m.delete(k); } };
};

// FakeEventSource records the streams the page opens; emit delivers an
// event as the browser does, fail an error (closed: the program refused).
class FakeEventSource {
  static opened = [];
  constructor(url) { Object.assign(this, { url, readyState: 0, listeners: {}, closed: false }); FakeEventSource.opened.push(this); }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  close() { this.closed = true; this.readyState = 2; }
  emit(type, data, id = '') { for (const fn of this.listeners[type] || []) fn({ type, data: JSON.stringify(data), lastEventId: id }); }
  fail(closed = true) { this.readyState = closed ? 2 : 0; for (const fn of this.listeners.error || []) fn({ type: 'error' }); }
}

// setup gives a test a fresh page loaded from doc: by the program, or with
// standalone, straight from a file with localStorage. confirm answers yes
// (or confirms.answer) and records its questions; prompt records its
// questions and answers prompts.answer.
async function setup(t, doc = DOC, { standalone = false } = {}) {
  const p = fakeProgram(doc);
  const timers = fakeTimers();
  const keys = ['fetch', 'location', 'sessionStorage', 'localStorage', 'document', 'confirm', 'prompt', 'EventSource'];
  const saved = keys.map(k => [k, Object.getOwnPropertyDescriptor(globalThis, k)]);
  const confirms = Object.assign([], { answer: true }), prompts = Object.assign([], { answer: null });
  FakeEventSource.opened = [];
  for (const [k, v] of Object.entries({
    fetch: p.fetch, sessionStorage: fakeStorage(), localStorage: fakeStorage(standalone ? { 'simple-rest': JSON.stringify(doc) } : {}),
    location: { protocol: standalone ? 'file:' : 'http:', reload() {} },
    confirm: msg => { confirms.push(msg); return confirms.answer; },
    prompt: msg => { prompts.push(msg); return prompts.answer; },
    EventSource: FakeEventSource,
  })) Object.defineProperty(globalThis, k, { value: v, configurable: true, writable: true });
  const sr = loadUI();
  globalThis.document = fakeDom();
  t.after(() => {
    timers.restore();
    for (const [k, d] of saved) { delete globalThis[k]; if (d) Object.defineProperty(globalThis, k, d); }
  });
  await sr.detectMode();
  await sr.loadState();
  return { sr, p, timers, confirms, prompts, el: s => globalThis.document.querySelector(s), def: () => sr.state.tcp[0] };
}

// open makes the event stream live, with the program saying info of
// connection lab (or nothing), and shows lab in the console. While the
// stream is live the page polls nothing, so what a test emits stays.
async function open(sr, p, info = si()) {
  p.tcp = list(info ? [info] : []);
  sr.openEvents();
  const es = FakeEventSource.opened.at(-1);
  es.emit('tcp.snapshot', p.tcp, 'b1.10');
  es.emit('ready', { boot: 'b1' }, 'b1.10');
  sr.setView('tcp', info ? info.id : 'lab');
  await settle();
  return es;
}

// ---- the editor

test('tcpEncode: escapes give their bytes, \\xHH one byte, any other backslash stays', () => {
  const sr = loadUI();
  const text = 'a\\r\\n\\t\\\\\\x0B\\xff\\q\\&\\x4';
  assert.deepEqual(bytesOf(sr, sr.tcpEncode(text, { mode: 'text', escapes: true })),
    [0x61, 0x0D, 0x0A, 0x09, 0x5C, 0x0B, 0xFF, 0x5C, 0x71, 0x5C, 0x26, 0x5C, 0x78, 0x34]);
  assert.deepEqual(bytesOf(sr, sr.tcpEncode(text, { mode: 'text', escapes: false })), [...utf8(text)], 'escapes off: the text as it is');
  assert.deepEqual(bytesOf(sr, sr.tcpEncode('é\\xE9', { escapes: true })), [0xC3, 0xA9, 0xE9], 'é is UTF-8, \\xE9 one byte');
});

test('tcpEncode: Enter gives the bytes of newline, in the text and from the textarea', () => {
  const sr = loadUI();
  for (const [newline, want] of [['lf', [0x0A]], ['cr', [0x0D]], ['crlf', [0x0D, 0x0A]]]) {
    assert.deepEqual(bytesOf(sr, sr.tcpEncode('a\nb\r\nc', { newline })), [0x61, ...want, 0x62, ...want, 0x63], newline);
  }
  assert.deepEqual(bytesOf(sr, sr.tcpEncode('a\\nb', { newline: 'crlf', escapes: true })), [0x61, 0x0A, 0x62], '\\n is LF whatever Enter gives');
});

test('tcpEncode: {{name}} is a variable in Text and in Hex mode', () => {
  const sr = loadUI();
  assert.deepEqual(parts(sr, sr.tcpEncode('PID|{{patient_id}}|{{ x.y-z }}|', { mode: 'text' })),
    [[...utf8('PID|')], { v: 'patient_id' }, [0x7C], { v: 'x.y-z' }, [0x7C]]);
  assert.deepEqual(parts(sr, sr.tcpEncode('0B 4d\n53 {{pid}} 1C0D', { mode: 'hex' })), [[0x0B, 0x4D, 0x53], { v: 'pid' }, [0x1C, 0x0D]]);
  assert.deepEqual(parts(sr, sr.tcpEncode('{{a}}{{b}}', {})), [{ v: 'a' }, { v: 'b' }], 'no empty byte parts');
  const long = 'x'.repeat(129);
  assert.deepEqual(parts(sr, sr.tcpEncode(`{{${long}}}`, {})), [[...utf8(`{{${long}}}`)]], 'a name the program does not take stays text');
});

test('tcpEncode: hex that is not hex says so', () => {
  const sr = loadUI();
  assert.equal(sr.tcpEncode('0B GG', { mode: 'hex' }).error, 'Hex: "G" is not a hex digit.');
  assert.match(sr.tcpEncode('0B 1', { mode: 'hex' }).error, /^Hex: an odd number of digits/);
  assert.match(sr.tcpEncode('0{{v}}B', { mode: 'hex' }).error, /odd/, 'the digits on each side of a variable make whole bytes');
  assert.deepEqual(sr.parseHex(' 0b\t4D\n'), { bytes: Uint8Array.from([0x0B, 0x4D]), error: '' });
  assert.equal(sr.tcpEncode('0B 1C', { mode: 'hex' }).error, '');
});

test('tcpEncode: 1 MB fits the 2 MB body, and src over 64 KB stays out', () => {
  const sr = loadUI();
  const body = enc => JSON.stringify({ parts: enc.parts, src: enc.src, wrap: true, waitReply: true, replyTimeoutMs: 5000 });
  const mb = sr.tcpEncode('\\xFF'.repeat(1 << 20), { escapes: true });
  assert.equal(sr.b64ToBytes(mb.parts[0].b64).length, 1 << 20);
  assert.equal(mb.src, '', 'the 4 MB template is not kept');
  assert.ok(body(mb).length <= 2 << 20, `${body(mb).length} bytes`);
  assert.ok(body(sr.tcpEncode('A'.repeat(1 << 20), {})).length <= 2 << 20);
  assert.equal(sr.tcpEncode('a'.repeat(64 << 10), {}).src.length, 64 << 10, 'up to 64 KB the template goes along');
  assert.equal(sr.tcpEncode('a'.repeat((64 << 10) + 1), {}).src, '');
  assert.equal(sr.tcpEncode('ą'.repeat(32769), {}).src, '', '64 KB counts bytes, not characters');
});

test('escapeBytes writes any bytes as text that tcpEncode turns back into them', () => {
  const sr = loadUI();
  let seed = 7;
  const rnd = () => { seed = (seed * 1103515245 + 12345) % 2147483648; return seed >> 16 & 0xFF; };
  const samples = [
    Uint8Array.from({ length: 256 }, (_, i) => i), Uint8Array.from({ length: 20000 }, rnd),
    utf8('zażółć gęślą jaźń\r\n\t{{x}} {{{y}}} \\x41  ﻿'), Uint8Array.from([0xC3, 0x28, 0xE2, 0x82, 0xF0, 0x9F, 0x98, 0x80, 0xED, 0xA0, 0x80]),
  ];
  for (const b of samples) {
    const text = sr.escapeBytes(b);
    assert.ok(!/[\r\n]/.test(text) && !text.includes('{{'), 'one line, no variable');
    for (const newline of ['lf', 'cr', 'crlf']) same(bytesOf(sr, sr.tcpEncode(text, { mode: 'text', escapes: true, newline })), b);
  }
  assert.equal(sr.escapeBytes(utf8('zażółć\r\n\t\\')), 'zażółć\\r\\n\\t\\\\', 'text stays readable');
  assert.equal(sr.escapeBytes(utf8('{{x}}')), '\\x7B{x}}');
  assert.equal(sr.escapeBytes(Uint8Array.from([0x0B, 0x1C, 0x0D, 0xFF])), '\\x0B\\x1C\\r\\xFF');
});

test('tcpVisible names control characters, ends lines after CR and LF, and stays text', () => {
  const sr = loadUI();
  assert.equal(sr.tcpVisible(latin1('\x0BMSH|^~\\&|A\rPID|1\r\x1C\r')), '<VT>MSH|^~\\&|A<CR>\nPID|1<CR><FS><CR>');
  assert.equal(sr.tcpVisible(latin1('A\r\nB\x06')), 'A<CR><LF>\nB<ACK>');
  assert.equal(sr.tcpVisible(latin1('A\rB'), 'utf-8', { breaks: false }), 'A<CR>B');
  assert.equal(sr.tcpVisible(latin1('\x00\x7F\t')), '<NUL><DEL><HT>');
  assert.equal(sr.tcpVisible(Uint8Array.from([0xEF, 0xBB, 0xBF, 0x41])), '<BOM>A', 'a BOM is shown, not dropped');
  assert.equal(sr.tcpVisible(Uint8Array.from([0xB9, 0x9C]), 'windows-1250'), 'ąś');
  assert.equal(sr.tcpVisible(Uint8Array.from([0xB1, 0xB6]), 'iso-8859-2'), 'ąś');
  assert.equal(sr.tcpVisible(Uint8Array.from([0xE9, 0x85]), 'latin1'), 'é<85>');
  assert.equal(sr.tcpVisible(utf8('<script>alert(1)</script>')), '<script>alert(1)</script>', 'text; the log escapes it');
  const html = sr.tcpEntryHTML(entry(3, { data: b64('<script>alert(1)</script>'), by: '<img src=x onerror=alert(1)>', incomplete: '<b>' }), { hex: false, charset: 'utf-8', client: '1.1.1.1' });
  assert.ok(!html.includes('<script>') && !html.includes('<img') && !html.includes('<b>'), html);
  assert.ok(html.includes('&lt;script&gt;alert(1)&lt;/script&gt;'));
});

test('hexDump, hexText and base64 of 1 MB', () => {
  const sr = loadUI();
  assert.equal(sr.hexDump(latin1('\x0BMSH|^~\\&|A\rPID|1\r\x1C\rab')),
    '0000  0B 4D 53 48 7C 5E 7E 5C  26 7C 41 0D 50 49 44 7C  .MSH|^~\\&|A.PID|\n'
    + '0010  ' + '31 0D 1C 0D 61 62'.padEnd(48) + '  1...ab');
  assert.equal(sr.hexText(Uint8Array.from({ length: 17 }, (_, i) => i)), '00 01 02 03 04 05 06 07 08 09 0A 0B 0C 0D 0E 0F\n10');
  const mb = Uint8Array.from({ length: 1 << 20 }, (_, i) => (i * 7) & 0xFF);
  const text = sr.bytesToB64(mb);
  assert.equal(text, Buffer.from(mb).toString('base64'));
  same(sr.b64ToBytes(text), mb);
  same(sr.b64ToBytes('not base64!'), []);
});

// ---- the twins of tcpframe.go and tcp.go

test('presets, tcpWrap and tcpTarget pass the shared vectors', () => {
  const sr = loadUI();
  assert.deepEqual(Object.keys(sr.TCP_PRESETS).sort(), Object.keys(vectors.presets).sort());
  for (const [name, p] of Object.entries(vectors.presets)) {
    assert.deepEqual([sr.TCP_PRESETS[name].start, sr.TCP_PRESETS[name].end], [[...latin1(p.start)], [...latin1(p.end)]], name);
  }
  assert.ok(vectors.wrap.length && vectors.targets.length);
  for (const c of vectors.wrap) {
    const w = sr.tcpWrap(latin1(c.in), c.preset);
    assert.deepEqual([[...w.bytes], w.wrapped], [[...latin1(c.out)], c.wrapped], `${c.preset} ${JSON.stringify(c.in)}`);
    if (c.wrapped) same(sr.tcpUnwrap(w.bytes, c.preset), latin1(c.in));
  }
  for (const c of vectors.targets) {
    const got = sr.tcpTarget(c.host, c.port);
    if (c.error) assert.equal(got.error, c.error, `${JSON.stringify(c.host)} ${JSON.stringify(c.port)}`);
    else assert.equal(got.addr, c.addr, `${JSON.stringify(c.host)} ${JSON.stringify(c.port)}`);
  }
  assert.equal(sr.tcpTarget('10.0.0.5', '0').msg, 'Port "0" is not a number from 1 to 65535.');
  assert.equal(sr.tcpTarget(' ', '1').msg, 'The host is empty.');
  for (const ok of ['::', '1::', '::ffff:1.2.3.4', '1:2:3:4:5:6:1.2.3.4', '1:2:3:4:5:6:7::']) assert.ok(!sr.tcpTarget(ok, '1').error, ok);
  for (const bad of ['1:2:3:4:5:6:7:8::', ':::', '1::2::3', '::1.2.3.04', '12345::', 'fe80::1%a b']) assert.equal(sr.tcpTarget(bad, '1').error, 'host', bad);
});

test('normalizeTcp keeps every preset of the vectors and the ranges of tcp.go', () => {
  const sr = loadUI();
  for (const name of Object.keys(vectors.presets)) assert.equal(sr.normalizeTcp({ framing: { preset: name } }).framing.preset, name);
  const d = sr.normalizeTcp({
    id: 'lab', name: 'Lab', host: '{{h}}', port: 2575, connectTimeoutMs: 99999.7, framing: { preset: 'custom', start: 'x' }, idleMs: -5,
    wrap: false, waitReply: true, replyTimeoutMs: 50, mode: 'binary', newline: 'cr', escapes: false, rxCharset: 'koi8', note: 'kept',
    messages: [{ name: 'M', mode: 'hex', data: '0B', extra: 1 }, null],
  });
  assert.deepEqual([d.id, d.name, d.host, d.port, d.connectTimeoutMs, d.framing, d.idleMs, d.wrap, d.waitReply, d.replyTimeoutMs, d.mode, d.newline, d.escapes, d.rxCharset, d.note],
    ['lab', 'Lab', '{{h}}', '2575', 60000, { preset: 'none', start: 'x' }, 0, false, true, 100, 'text', 'cr', false, 'utf-8', 'kept']);
  assert.deepEqual(d.messages.map(m => [ID.test(m.id), m.name, m.mode, m.data, m.extra]), [[true, 'M', 'hex', '0B', 1], [true, '', 'text', '', undefined]]);
  const e = sr.normalizeTcp({ connectTimeoutMs: '5000', idleMs: null });
  assert.deepEqual([e.name, e.host, e.port, e.connectTimeoutMs, e.framing.preset, e.idleMs, e.wrap, e.waitReply, e.replyTimeoutMs, e.mode, e.newline, e.escapes, e.rxCharset, e.messages],
    ['', '', '', 10000, 'none', 50, true, false, 5000, 'text', 'lf', true, 'utf-8', []], 'defaults');
  const doc = sr.normalizeDoc({ tcp: [{ id: 'a' }, { id: 'a' }, { id: 'bad id' }] });
  assert.equal(doc.tcp[0].id, 'a');
  assert.ok(doc.tcp.slice(1).every(x => ID.test(x.id) && x.id !== 'a'), 'a second id and one the routes do not take get new ones');
});

test('splitTarget takes a pasted address apart', () => {
  const sr = loadUI();
  assert.deepEqual(sr.splitTarget('tcp://10.0.0.5:2575'), { host: '10.0.0.5', port: '2575' });
  assert.deepEqual(sr.splitTarget(' lab-pc:2575 '), { host: 'lab-pc', port: '2575' });
  assert.deepEqual(sr.splitTarget('[fe80::1%eth0]:2575'), { host: 'fe80::1%eth0', port: '2575' });
  assert.deepEqual(sr.splitTarget('{{lab_host}}:2575'), { host: '{{lab_host}}', port: '2575' });
  assert.deepEqual(sr.splitTarget('tcp://lab-pc'), { host: 'lab-pc', port: '' });
  for (const keep of ['fe80::1', 'lab-pc', '{{h}}:{{p}}']) assert.equal(sr.splitTarget(keep), null, keep);
});

// ---- what the program says

test('tcpReduce: rev, another boot, entries once, clears and replies', () => {
  const sr = loadUI();
  const rt = sr.newTcpRT();
  assert.equal(sr.tcpReduce(rt, 'tcp.state', si(), 'b1.3'), null, 'nothing before the snapshot of the boot');
  assert.equal(sr.tcpReduce(rt, 'tcp.snapshot', list([si({ rev: 5 })]), 'b1.10'), '*');
  assert.deepEqual([rt.boot, rt.client, rt.sessions.get('lab').state], ['b1', '127.0.0.1', 'connected']);
  assert.equal(sr.tcpReduce(rt, 'tcp.state', si({ rev: 4, state: 'closed' }), 'b1.11'), null, 'an older rev');
  assert.equal(sr.tcpReduce(rt, 'tcp.state', si({ rev: 12, waiting: 11 }), 'b1.12'), 'lab');
  assert.equal(rt.sessions.get('lab').waiting, 11);
  assert.equal(sr.tcpReduce(rt, 'tcp.state', si({ rev: 99, state: 'closed' }), 'b0.13'), null, 'another boot');
  assert.equal(rt.sessions.get('lab').state, 'connected');

  sr.tcpReduce(rt, 'tcp.entry', { id: 'lab', bytesIn: 5, bytesOut: 7, entry: entry(13) }, 'b1.13');
  assert.deepEqual([rt.logs.size, rt.sessions.get('lab').bytesIn, rt.sessions.get('lab').bytesOut], [0, 5, 7], 'no log kept, the counters move');
  sr.tcpReduce(rt, 'tcp.entry', { id: 'lab', bytesIn: 3, bytesOut: 1, entry: entry(14) }, 'b1.14');
  assert.equal(rt.sessions.get('lab').bytesIn, 5, 'counters do not go back');

  assert.equal(sr.tcpMergeLog(rt, 'lab', { boot: 'b1', entries: [entry(13), entry(14)] }), true);
  for (const seq of [15, 16, 15, 14]) sr.tcpReduce(rt, 'tcp.entry', { id: 'lab', entry: entry(seq) }, 'b1.' + seq);
  assert.deepEqual(rt.logs.get('lab').entries.map(e => e.seq), [13, 14, 15, 16], 'each entry once');
  sr.tcpMergeLog(rt, 'lab', { boot: 'b1', entries: [entry(12), entry(16), entry(17)] });
  assert.deepEqual(rt.logs.get('lab').entries.map(e => e.seq), [12, 13, 14, 15, 16, 17], 'the answer and the events, in order');
  assert.equal(sr.tcpMergeLog(rt, 'lab', { boot: 'b0', entries: [entry(1)] }), false, 'a log of another boot');

  sr.tcpReduce(rt, 'tcp.cleared', { id: 'lab', seq: 18 }, 'b1.18');
  assert.deepEqual(rt.logs.get('lab').entries, []);
  sr.tcpReduce(rt, 'tcp.entry', { id: 'lab', entry: entry(17) }, 'b1.17');
  sr.tcpMergeLog(rt, 'lab', { boot: 'b1', entries: [entry(16), entry(17), entry(19)] });
  assert.deepEqual(rt.logs.get('lab').entries.map(e => e.seq), [19], 'nothing from before the clear comes back');

  sr.tcpReduce(rt, 'tcp.entry', { id: 'lab', entry: entry(20, { dir: 'tx' }) }, 'b1.20');
  sr.tcpReduce(rt, 'tcp.entry', { id: 'lab', entry: entry(21, { replyTo: 20, rttMs: 59.4 }) }, 'b1.21');
  assert.deepEqual(rt.replies.get('lab'), { seq: 21, tx: 20, dir: 'rx', rttMs: 59.4, text: '', timedOut: false, closed: false });
  sr.tcpReduce(rt, 'tcp.entry', { id: 'lab', entry: entry(19, { replyTo: 3 }) }, 'b1.19');
  assert.equal(rt.replies.get('lab').tx, 20, 'an older reply does not replace a newer one');

  assert.equal(sr.tcpReduce(rt, 'tcp.removed', { id: 'lab' }, 'b1.22'), 'lab');
  assert.deepEqual([rt.sessions.has('lab'), rt.logs.get('lab').entries], [false, []]);
  sr.tcpReduce(rt, 'tcp.snapshot', list([si({ rev: 30 })]), 'b1.30');
  sr.tcpMergeLog(rt, 'lab', { boot: 'b1', entries: [entry(25)] });
  sr.tcpReduce(rt, 'tcp.snapshot', list([si({ id: 'other', rev: 2 })], { boot: 'b2' }), 'b2.2');
  assert.deepEqual([rt.boot, [...rt.sessions.keys()], rt.logs.get('lab').entries], ['b2', ['other'], []], 'a restart starts over');
});

// ---- the console

test('previewBytes: a value of the overlay shows in Sends:, from the environment of the connection', async t => {
  const { sr, p, el, def } = await setup(t);
  sr.applySnapshot({ boot: 'b1', gen: 1, now: Date.now(), version: '1.2.0', stateTag: '"t1"', jobs: {}, vars: { e1: { patient_id: { value: 'ovl-7', by: 's', at: 1 } } } });
  const tab = sr.previewBytes('PID|{{patient_id}}|{{nope}}', def());
  assert.equal(new TextDecoder().decode(tab.bytes), '\x0BPID|ovl-7|{{nope}}\x1C\r', 'the overlay of the tab environment, and the framing of the definition');
  assert.deepEqual([tab.wrapped, tab.already, tab.envId, tab.missing], [true, false, 'e1', ['nope']]);
  const other = sr.previewBytes('PID|{{patient_id}}', def(), si({ envId: 'e2', framing: { preset: 'stxetx' } }));
  assert.equal(new TextDecoder().decode(other.bytes), '\x02PID|999\x03', 'an open connection: its environment and its framing');
  assert.equal(sr.previewBytes('PID', def(), si({ state: 'closed', envId: 'e2' })).envId, 'e1', 'a closed one: the tab, as Connect will');
  assert.equal(sr.previewBytes('\\x0BA\\x1C\\r', def()).already, true);

  await open(sr, p, null);
  el('#tcpData').value = 'PID|{{patient_id}}';
  sr.renderTcpView();
  assert.equal(el('#tcpSends').textContent, 'Sends: <VT>PID|ovl-7<FS><CR>   (wrapped: MLLP (HL7))');
  assert.equal(el('#tcpBytes').textContent, '12 bytes');
});

test('setView switches the work area and outlives a reload; keyAction keeps Ctrl+S of the console off the HTTP request', async t => {
  const { sr, p, el, prompts } = await setup(t);
  await open(sr, p);
  assert.equal(el('.work').classList.contains('view-tcp'), true);
  assert.deepEqual(JSON.parse(sessionStorage.getItem('simple-rest-view')), { view: 'tcp', tcpId: 'lab' });
  assert.equal(sr.tcpInfo().view, 'tcp');
  const key = (k, v) => sr.keyAction({ key: k, ctrlKey: true, preventDefault() {} }, v);

  el('#tcpData').value = 'ENQ';
  prompts.answer = 'Enquiry';
  assert.equal(key('s', 'tcp'), 'save');
  assert.deepEqual([...prompts], ['Message name'], 'Save as… of the message, never the HTTP save');
  assert.deepEqual(sr.state.tcp[0].messages.map(m => [m.name, m.data, m.mode]), [['ORM^O01', DOC.tcp[0].messages[0].data, 'text'], ['Enquiry', 'ENQ', 'text']]);
  assert.deepEqual(sr.state.collections, []);
  const before = p.calls.length;
  assert.equal(key('Enter', 'tcp'), 'send');
  await settle();
  assert.ok(!p.calls.slice(before).some(c => c.url === '/api/send'), 'Ctrl+Enter sends the message, not the request');

  prompts.length = 0;
  prompts.answer = null;
  assert.equal(key('s', 'http'), 'save');
  assert.deepEqual([...prompts], ['Name of the first collection'], 'in the HTTP view Ctrl+S saves the request');
  sr.openRequest(sr.newRequest(), null);
  assert.equal(el('.work').classList.contains('view-tcp'), false, 'opening a request goes back to HTTP');
  assert.deepEqual(JSON.parse(sessionStorage.getItem('simple-rest-view')), { view: 'http', tcpId: 'lab' });
});

test('the sidebar lists the connections, counts the open ones and keeps one whose definition is gone', async t => {
  const { sr, el } = await setup(t);
  sr.applyTcpEvent('tcp.snapshot', list([si(), si({ id: 'gone', hostTpl: 'old-pc', portTpl: '9100', state: 'connecting' })]), 'b1.10');
  assert.equal(el('#tcpTabCount').textContent, '1');
  const html = el('#tcpList').innerHTML;
  assert.match(html, /data-tcp="lab".*Lab analyzer.*\{\{lab_host\}\}:2575/);
  assert.match(html, /data-tcp="gone".*\(not saved\).*old-pc:9100/);
  el('#sideSearch').value = 'old';
  sr.renderTcpList();
  assert.doesNotMatch(el('#tcpList').innerHTML, /Lab analyzer/);
  el('#sideSearch').value = '';

  sr.setView('tcp', 'gone');
  assert.equal(el('#tcpName').value, '(not saved)');
  assert.match(el('#tcpHints').innerHTML, /not in the data file any more/);
  assert.equal(el('#tcpConnBtn').textContent, 'Cancel');
  assert.equal(el('#tcpConnBtn').disabled, false, 'it can still be closed');
});

test('Connect saves first, refreshes a value a schedule sets, then sends the definition with the environment of the tab', async t => {
  const { sr, p, el, def } = await setup(t, { ...copy(DOC), activeEnv: 'e2', tcp: [{ ...DOC.tcp[0], host: '{{token}}.lan' }] });
  await open(sr, p, null);
  p.routes['/api/vars/ensure'] = () => res(200, JSON.stringify({ boot: 'b1', envId: 'e2', vars: { token: { value: 'lab9', by: 's2', at: 1 } }, notes: [] }));
  p.routes['/api/tcp/lab/connect'] = () => {
    p.tcp = list([si({ rev: 11, state: 'connecting', envId: 'e2', envName: 'prod', hostTpl: '{{token}}.lan', host: 'lab9.lan' })]);
    return res(202, JSON.stringify(p.tcp.sessions[0]));
  };
  assert.equal(el('#tcpConnBtn').disabled, true, '{{token}} has no value in prod yet');
  assert.equal(el('#tcpHints').innerHTML.includes('Variable {{token}} is not set in &quot;prod&quot;.'), true);
  sr.state.envs[0].name = 'Local';
  sr.persist();
  const r = await sr.tcpConnect();
  assert.equal(r.status, 202);
  assert.deepEqual(p.calls.filter(c => c.method !== 'GET').map(c => c.method + ' ' + c.url), ['PUT /api/state', 'POST /api/vars/ensure', 'POST /api/tcp/lab/connect']);
  assert.deepEqual(p.body('/api/vars/ensure'), { envId: 'e2', names: ['token'] });
  assert.deepEqual(p.body('/api/tcp/lab/connect'), { envId: 'e2', host: '{{token}}.lan', port: '2575', timeoutMs: 10000, framing: { preset: 'mllp' }, idleMs: 50 });
  assert.equal(sr.tcpInfo().rt.sessions.get('lab').state, 'connecting');
  assert.equal(el('#tcpConnBtn').textContent, 'Cancel');
  assert.equal(def().host, '{{token}}.lan', 'the template stays');
});

test('Connect stops at a variable without a value or a bad port, and says when another tab connected first', async t => {
  const { sr, p, el, def } = await setup(t, { ...copy(DOC), tcp: [{ ...DOC.tcp[0], host: '{{nope}}' }] });
  await open(sr, p, null);
  assert.equal(await sr.tcpConnect(), null);
  assert.equal(sr.tcpInfo().msg, 'Variable {{nope}} is not set in "local".');
  def().host = '{{lab_host}}'; def().port = '70000';
  assert.equal(await sr.tcpConnect(), null);
  assert.equal(sr.tcpInfo().msg, 'Port "70000" is not a number from 1 to 65535.');
  assert.equal(p.count('POST', '/api/tcp/lab/connect'), 0);
  def().port = '2575';
  p.routes['/api/tcp/lab/connect'] = () => res(409, '{"error":"already connected"}');
  await sr.tcpConnect();
  assert.equal(el('#toast').textContent, 'Already connected (another tab or computer?)');
});

test('Send goes with the environment of the connection: ensure there, then the parts, the template and the reply settings', async t => {
  const { sr, p, el } = await setup(t);
  await open(sr, p, si({ envId: 'e2', envName: 'prod' }));
  assert.equal(el('#tcpHints').innerHTML.includes('This tab uses &quot;local&quot;. This connection was opened with &quot;prod&quot; and sends with its values.'), true);
  p.routes['/api/vars/ensure'] = () => res(200, JSON.stringify({ boot: 'b1', envId: 'e2', vars: {}, notes: [{ id: 's2', name: 'Prod token', state: 'off' }] }));
  p.routes['/api/tcp/lab/send'] = () => res(200, JSON.stringify({ seq: 21, size: 30, wrapped: true, waiting: true, missing: [] }));
  el('#tcpData').value = 'MSH|{{token}}\nPID|{{patient_id}}';
  const r = await sr.tcpSend();
  assert.equal(r.status, 200);
  assert.deepEqual(p.body('/api/vars/ensure'), { envId: 'e2', names: ['token', 'patient_id'] });
  const body = p.body('/api/tcp/lab/send');
  assert.deepEqual(body.parts, [{ b64: b64('MSH|') }, { v: 'token' }, { b64: b64('\rPID|') }, { v: 'patient_id' }], 'Enter is CR in this definition');
  assert.deepEqual([body.src, body.wrap, body.waitReply, body.replyTimeoutMs], ['MSH|{{token}}\nPID|{{patient_id}}', true, true, 5000]);
  assert.equal(sr.tcpInfo().note, '{{token}} comes from schedule "Prod token", which is off. Turn it on in Schedules.');

  // The program tells the wait, the message and its reply.
  sr.applyTcpEvent('tcp.state', si({ rev: 20, envId: 'e2', envName: 'prod', waiting: 21 }), 'b1.20');
  sr.applyTcpEvent('tcp.entry', { id: 'lab', bytesOut: 30, entry: entry(21, { dir: 'tx', src: body.src, wrapped: true }) }, 'b1.21');
  assert.equal(el('#tcpSendBtn').textContent, 'Waiting for reply…');
  assert.equal(el('#tcpCancelWait').hidden, false);
  const before = p.calls.length;
  sr.keyAction({ key: 'Enter', ctrlKey: true, preventDefault() {} }, 'tcp');
  await settle();
  assert.equal(p.calls.length, before, 'no second message while one waits');
  assert.equal(sr.tcpInfo().msg, 'Waiting for the reply to #21. Press Cancel to stop waiting.');
  sr.applyTcpEvent('tcp.entry', { id: 'lab', bytesIn: 9, entry: entry(22, { replyTo: 21, rttMs: 59.2 }) }, 'b1.22');
  sr.applyTcpEvent('tcp.state', si({ rev: 22, envId: 'e2', envName: 'prod', waiting: 0, bytesIn: 9, bytesOut: 30 }), 'b1.23');
  assert.equal(el('#tcpReply').textContent, 'Reply in 59 ms');
  assert.equal(el('#tcpSendBtn').textContent, 'Send');
  assert.match(el('#tcpLog').children.at(-1).innerHTML, /reply to #21 in 59 ms/);
  assert.equal(el('#tcpMeta').textContent, '10.0.0.2:51234 -> 10.0.0.5:2575 · env prod · TX 30 B · RX 9 B · opened by you');

  sr.applyTcpEvent('tcp.entry', { id: 'lab', entry: entry(24, { dir: 'tx' }) }, 'b1.24');
  sr.applyTcpEvent('tcp.entry', { id: 'lab', entry: entry(25, { dir: 'info', replyTo: 24, timedOut: true, text: 'no reply within 5 s' }) }, 'b1.25');
  assert.equal(el('#tcpReply').textContent, 'No reply within 5 s');
  sr.applyTcpEvent('tcp.entry', { id: 'lab', entry: entry(26, { dir: 'tx' }) }, 'b1.26');
  sr.applyTcpEvent('tcp.entry', { id: 'lab', entry: entry(27, { dir: 'info', replyTo: 26, closed: true, text: 'connection closed while waiting for the reply to #26' }) }, 'b1.27');
  assert.equal(el('#tcpReply').textContent, 'Connection closed while waiting');

  sr.applyTcpEvent('tcp.state', si({ rev: 30, envId: 'e2', envName: 'prod', waiting: 28 }), 'b1.30');
  await sr.tcpCancelWait();
  assert.equal(p.count('DELETE', '/api/tcp/lab/wait'), 1);
});

test('Send says what keeps a message from going, and shows what the program answered', async t => {
  const { sr, p, el } = await setup(t);
  await open(sr, p, si({ state: 'closed' }));
  el('#tcpData').value = 'A';
  assert.equal(await sr.tcpSend(), null);
  assert.equal(sr.tcpInfo().msg, 'Not connected: press Connect first.');
  sr.applyTcpEvent('tcp.state', si({ rev: 6 }), 'b1.11');
  el('#tcpData').value = '{{nope}}';
  sr.renderTcpView();
  assert.match(el('#tcpNote').innerHTML, /unknown variable: \{\{nope\}\}/, 'allowed, as in HTTP');
  sr.state.tcp[0].mode = 'hex';
  el('#tcpData').value = '0B ZZ';
  assert.equal(await sr.tcpSend(), null);
  assert.equal(sr.tcpInfo().msg, 'Hex: "Z" is not a hex digit.');
  sr.state.tcp[0].mode = 'text';
  p.routes['/api/tcp/lab/send'] = () => res(200, JSON.stringify({ seq: 9, error: 'write timeout: no progress for 10 s' }));
  el('#tcpData').value = 'A';
  await sr.tcpSend();
  assert.equal(sr.tcpInfo().msg, 'Send failed: write timeout: no progress for 10 s');
  p.routes['/api/tcp/lab/send'] = () => res(409, '{"error":"not connected"}');
  await sr.tcpSend();
  assert.equal(sr.tcpInfo().msg, 'not connected');
  el('#tcpData').value = '{"a":"x\\ny"}';
  sr.renderTcpView();
  assert.match(el('#tcpNote').innerHTML, /Escapes are on: a \\ in this JSON starts an escape/);
});

test('hints: Reconnect for changes, the framing, the end of a frame and .local on Linux', async t => {
  const { sr, p, el, def } = await setup(t);
  await open(sr, p);
  assert.equal(el('#tcpHints').innerHTML, '');
  def().framing.preset = 'stxetx';
  sr.renderTcpView();
  assert.match(el('#tcpHints').innerHTML, /Framing changes apply after Reconnect\..*data-act="reconnect"/);
  def().framing.preset = 'mllp';
  sr.state.envs[0].vars[0].value = '10.0.0.6';
  sr.renderTcpView();
  assert.match(el('#tcpHints').innerHTML, /Connected to 10\.0\.0\.5:2575\. Reconnect to apply changes\./, 'the value of the host changed');
  sr.applyTcpEvent('tcp.state', si({ rev: 7, pending: 12 }), 'b1.12');
  assert.deepEqual([el('#tcpPending').hidden, el('#tcpPending').textContent], [false, 'Waiting for the end of a frame: 12 B']);
  sr.applyTcpEvent('tcp.state', si({ rev: 8, state: 'closed', host: 'pi.local', error: 'lookup pi.local: no such host' }), 'b1.13');
  assert.equal(el('#tcpStatus').textContent, '○ Closed: lookup pi.local: no such host');
  assert.match(el('#tcpHints').innerHTML, /This build cannot resolve \.local names \(mDNS\)\. Use the IP address\./);
  p.routes['/api/tcp/lab/disconnect'] = () => res(200, JSON.stringify(si({ rev: 9, state: 'closed' })));
  p.routes['/api/tcp/lab/connect'] = () => res(202, JSON.stringify(si({ rev: 10, state: 'connecting', host: '10.0.0.6' })));
  sr.applyTcpEvent('tcp.state', si({ rev: 8 }), 'b1.14');
  await sr.tcpReconnect();
  assert.deepEqual(p.calls.filter(c => c.method === 'POST').map(c => c.url), ['/api/tcp/lab/disconnect', '/api/tcp/lab/connect']);
});

test('the log: entries come in, labels, To editor and Clear', async t => {
  const { sr, p, el, def } = await setup(t);
  p.logs.lab = [entry(11, { dir: 'info', text: 'connected to 10.0.0.5:2575 from 10.0.0.2:51234' })];
  await open(sr, p);
  assert.equal(el('#tcpCount').textContent, ' 1 entry');
  assert.match(el('#tcpLog').innerHTML, /connected to 10\.0\.0\.5:2575/);
  sr.applyTcpEvent('tcp.entry', { id: 'lab', entry: entry(12, { data: b64('\x06'), outside: true }) }, 'b1.12');
  sr.applyTcpEvent('tcp.entry', { id: 'lab', entry: entry(13, { data: b64('\x0BA'), incomplete: 'peer closed', by: '192.168.1.31' }) }, 'b1.13');
  sr.applyTcpEvent('tcp.entry', { id: 'lab', entry: entry(14, { dir: 'tx', data: b64('\x0BMSH\r\x1C\r'), wrapped: true, missing: ['x'] }) }, 'b1.14');
  assert.equal(el('#tcpCount').textContent, ' 4 entries');
  const html = sr.tcpInfo().rt.logs.get('lab').entries.map(e => sr.tcpEntryHTML(e, { hex: false, charset: 'utf-8', client: '127.0.0.1' }));
  assert.match(html[1], /&lt;ACK&gt;.*outside frame/);
  assert.match(html[2], /incomplete \(peer closed\).*by 192\.168\.1\.31/);
  assert.match(html[3], /&lt;VT&gt;MSH&lt;CR&gt;&lt;FS&gt;&lt;CR&gt;.*sent as typed, not set: \{\{x\}\}/);
  assert.match(sr.tcpEntryHTML(entry(15, { data: b64('AB') }), { hex: true, charset: 'utf-8', client: '' }), /0000  41 42 {45}AB/);

  const log = sr.tcpInfo().rt.logs.get('lab').entries;
  sr.tcpToEditor(log[3]);
  assert.equal(el('#tcpData').value, 'MSH\\r', 'without the framing the send added, as escapes');
  assert.equal(localStorage.getItem('simple-rest-tcp-draft:lab'), 'MSH\\r', 'the draft of this browser');
  sr.tcpToEditor({ ...log[3], src: 'MSH|{{patient_id}}' });
  assert.equal(el('#tcpData').value, 'MSH|{{patient_id}}', 'the template it was sent from');
  def().mode = 'hex';
  sr.tcpToEditor(log[1]);
  assert.equal(el('#tcpData').value, '06');
  sr.tcpToEditor({ ...log[3], src: 'MSH|{{patient_id}}' });
  assert.equal(def().mode, 'text', 'a template typed as text is no hex');

  p.routes['/api/tcp/lab/log'] = () => res(204);
  await sr.tcpClearLog();
  assert.equal(p.count('DELETE', '/api/tcp/lab/log'), 1);
  assert.equal(el('#tcpCount').textContent, ' 0 entries');
  sr.applyTcpEvent('tcp.entry', { id: 'lab', entry: entry(13) }, 'b1.13');
  sr.applyTcpEvent('tcp.cleared', { id: 'lab', seq: 15 }, 'b1.15');
  sr.applyTcpEvent('tcp.entry', { id: 'lab', entry: entry(16) }, 'b1.16');
  assert.deepEqual(sr.tcpInfo().rt.logs.get('lab').entries.map(e => e.seq), [16]);
});

test('Delete removes the connection from the program and the definition from the file', async t => {
  const { sr, p, confirms } = await setup(t);
  await open(sr, p);
  p.routes['/api/tcp/lab'] = () => res(204);
  await sr.tcpDelete();
  assert.deepEqual([...confirms], ['Delete TCP connection "Lab analyzer"? It is open and gets disconnected.']);
  assert.equal(p.count('DELETE', '/api/tcp/lab'), 1);
  assert.deepEqual([sr.state.tcp, sr.tcpInfo().rt.sessions.size, sr.tcpInfo().tcpId], [[], 0, null]);
  assert.equal(await sr.flushPersist(), true);
  assert.deepEqual(JSON.parse(p.text).tcp, []);
});

test('+ adds a connection, Duplicate copies one with new ids, and a schedule cannot come from the console', async t => {
  const { sr, p, el } = await setup(t);
  await open(sr, p, null);
  const c = sr.tcpDuplicate();
  assert.deepEqual([c.name, c.host, c.framing.preset, c.messages.length], ['Lab analyzer copy', '{{lab_host}}', 'mllp', 1]);
  assert.ok(ID.test(c.id) && c.id !== 'lab' && c.messages[0].id !== 'm1');
  assert.equal(sr.tcpInfo().tcpId, c.id);
  const d = sr.addTcp();
  assert.deepEqual([d.name, d.host, d.port, d.framing.preset, sr.tcpInfo().tcpId], ['New TCP connection', '', '', 'none', d.id]);
  assert.match(el('#tcpHints').innerHTML, /The host is empty\./);
  assert.equal(sr.state.tcp.length, 3);
  assert.equal(sr.addSchedule(null), null);
  assert.equal(el('#schedAddHint').textContent, 'Open an HTTP request to schedule it.');
});

test('export carries the connections and their messages; import gives them new ids', async t => {
  const ui = loadUI();
  ui.applyState(ui.normalizeDoc(copy(DOC)));
  const out = ui.exportData();
  assert.deepEqual(out.tcp, ui.state.tcp);
  out.tcp[0].name = 'Changed';
  assert.equal(ui.state.tcp[0].name, 'Lab analyzer', 'a copy');
  const got = ui.importData(JSON.parse(JSON.stringify(ui.exportData())));
  assert.deepEqual(got.skipped, []);
  const [c] = got.tcp;
  assert.ok(ID.test(c.id) && c.id !== 'lab' && c.messages[0].id !== 'm1');
  assert.deepEqual([c.name, c.host, c.port, c.framing, c.waitReply, c.newline, c.messages.map(m => [m.name, m.data])],
    ['Lab analyzer', '{{lab_host}}', '2575', { preset: 'mllp' }, true, 'cr', [['ORM^O01', DOC.tcp[0].messages[0].data]]]);
  assert.equal(ui.importData({ app: 'simple-rest', tcp: [{ framing: { preset: 'x' } }] }).tcp[0].framing.preset, 'none');

  const { sr, el } = await setup(t, { ...copy(DOC), tcp: [] });
  await sr.importFiles([{ name: 'export.json', text: async () => JSON.stringify({ app: 'simple-rest', version: 1, tcp: DOC.tcp }) }]);
  assert.equal(el('#toast').textContent, 'Imported 0 collection(s), 0 environment(s), 1 TCP connection(s)');
  assert.equal(sr.state.tcp[0].name, 'Lab analyzer');
});

test('featureState: without the program the console shows its definitions and does nothing', async t => {
  const { sr, p, el } = await setup(t, DOC, { standalone: true });
  sr.setView('tcp', 'lab');
  await settle();
  assert.equal(sr.featureState(), 'nobin');
  assert.deepEqual([el('#tcpBanner').hidden, el('#tcpBanner').textContent], [false, 'TCP needs the simple-rest program: browsers cannot open raw TCP sockets.']);
  assert.equal(el('#tcpHost').value, '{{lab_host}}', 'the definition is there to read');
  assert.deepEqual([el('#tcpConnBtn').disabled, el('#tcpSendBtn').disabled, el('#tcpHost').disabled], [true, true, true]);
  assert.equal(await sr.tcpConnect(), null);
  assert.equal(await sr.tcpSend('A'), null);
  assert.equal(sr.addTcp(), null);
  await sr.tcpDelete();
  await sr.tick(true);
  assert.equal(sr.openEvents(), false, 'no stream');
  assert.equal(sr.state.tcp.length, 1);
  assert.deepEqual(p.calls, []);
});

test('featureState: from another version Connect, Send and Disconnect stop', async t => {
  const { sr, p, el } = await setup(t);
  await open(sr, p);
  sr.applySnapshot({ boot: 'b1', gen: 1, now: Date.now(), version: '1.2.1', stateTag: '"t1"', jobs: {}, vars: {} });
  assert.equal(sr.featureState(), 'stale');
  assert.equal(el('#tcpBanner').textContent, 'This page comes from another simple-rest version than the running program. Reload the page.');
  await settle();
  const before = p.calls.length;
  assert.equal(await sr.tcpDisconnect(), null);
  assert.equal(await sr.tcpSend('A'), null);
  await sr.tcpCancelWait();
  await sr.tcpClearLog();
  assert.equal(p.calls.length, before);
  assert.equal(el('#tcpConnBtn').disabled, true);
});

test('Install and restart says how many schedules turn off and TCP connections close', async t => {
  const { sr, p, confirms } = await setup(t);
  confirms.answer = false;
  sr.applyTcpEvent('tcp.snapshot', list([si(), si({ id: 'b', state: 'closed' }), si({ id: 'c', state: 'connecting' })]), 'b1.10');
  await sr.installUpdate();
  sr.applySnapshot({ boot: 'b1', gen: 2, now: Date.now(), version: '1.2.0', stateTag: '"t1"', jobs: { s2: { enabled: true, state: 'ok' } }, vars: {} });
  await sr.installUpdate();
  assert.deepEqual([...confirms], [
    'Installing restarts the program: 2 TCP connection(s) close. Continue?',
    'Installing restarts the program: 1 schedule(s) turn off and 2 TCP connection(s) close. Continue?',
  ]);
  assert.equal(p.count('POST', '/api/update/install'), 0);
});

// ---- the event stream and the polls without it

test('the stream: live stops the polls, and its events sync the file, the schedules and the console', async t => {
  const { sr, p, el } = await setup(t);
  sr.setView('tcp', 'lab');
  assert.equal(sr.openEvents(), true);
  const [es] = FakeEventSource.opened;
  assert.equal(es.url, '/api/events');
  es.emit('tcp.snapshot', list([si()]), 'b1.10');
  es.emit('state.changed', { tag: p.tag }, 'b1.10');
  es.emit('sched.changed', { gen: 0 }, 'b1.10');
  es.emit('ready', { boot: 'b1' }, 'b1.10');
  await settle();
  assert.equal(sr.tcpInfo().live, true);
  assert.equal(el('#tcpLive').hidden, false);
  assert.equal(el('#tcpTabCount').textContent, '1');

  p.calls.length = 0;
  await sr.tick();
  assert.deepEqual(p.calls, [], 'live: no poll');
  const theirs = copy(DOC);
  theirs.tcp[0].name = 'Renamed elsewhere';
  p.change(theirs);
  es.emit('state.changed', { tag: p.tag }, 'b1.11');
  await settle();
  assert.equal(sr.state.tcp[0].name, 'Renamed elsewhere');
  assert.equal(el('#tcpName').value, 'Renamed elsewhere', 'the console follows the file');
  es.emit('sched.changed', { gen: 4 }, 'b1.12');
  await settle();
  assert.equal(p.count('GET', '/api/schedules'), 1, 'a new gen: the snapshot of the schedules');
  es.emit('tcp.entry', { id: 'lab', bytesIn: 1, entry: entry(13) }, 'b1.13');
  assert.deepEqual(sr.tcpInfo().rt.logs.get('lab').entries.map(e => e.seq), [13]);
  assert.equal(sr.tcpInfo().lastEventId, 'b1.13');
  assert.ok(!p.calls.some(c => c.url === '/api/tcp'), 'nothing polled');
});

test('the stream: a refused stream is tried again every 2 s for 30 s, then every 30 s; a hidden tab closes it', async t => {
  const { sr, timers } = await setup(t);
  const realNow = Date.now;
  let now = realNow();
  Date.now = () => now;
  t.after(() => { Date.now = realNow; });
  const run = async ms => { now += ms; await timers.run(ms); };
  sr.openEvents();
  let es = FakeEventSource.opened.at(-1);
  es.emit('tcp.snapshot', list([]), 'b1.5');
  es.emit('ready', { boot: 'b1' }, 'b1.5');
  assert.equal(sr.tcpInfo().live, true);
  es.fail(false);
  assert.equal(sr.tcpInfo().live, false, 'the browser tries again by itself');
  assert.equal(FakeEventSource.opened.length, 1);
  es.fail(true);
  assert.equal(es.closed, true);
  await run(1999);
  assert.equal(FakeEventSource.opened.length, 1);
  await run(1);
  es = FakeEventSource.opened.at(-1);
  assert.deepEqual([FakeEventSource.opened.length, es.url], [2, '/api/events?after=b1.5']);
  for (let i = 0; i < 14; i++) { es.fail(true); await run(2000); es = FakeEventSource.opened.at(-1); }
  assert.equal(FakeEventSource.opened.length, 16);
  es.fail(true);
  await run(2000);
  assert.equal(FakeEventSource.opened.length, 16, '30 s down: the next try waits 30 s');
  await run(28000);
  assert.equal(FakeEventSource.opened.length, 17);
  es = FakeEventSource.opened.at(-1);
  es.emit('ready', { boot: 'b1' }, 'b1.5');
  es.fail(true);
  await run(2000);
  assert.equal(FakeEventSource.opened.length, 18, 'up again: back to 2 s');

  es = FakeEventSource.opened.at(-1);
  es.emit('tcp.state', si({ rev: 6, state: 'closed' }), 'b1.6');
  document.visibilityState = 'hidden';
  sr.onVisibility();
  assert.deepEqual([es.closed, sr.tcpInfo().live], [true, false]);
  document.visibilityState = 'visible';
  sr.onVisibility();
  assert.equal(FakeEventSource.opened.at(-1).url, '/api/events?after=b1.6', 'back to the tab: it catches up');
});

test('without the stream the tab polls the connections, and the log of the one shown when it changed', async t => {
  const { sr, p, el } = await setup(t);
  p.tcp = list([si({ bytesIn: 1 })]);
  p.logs.lab = [entry(11)];
  sr.setView('tcp', 'lab');
  await settle();
  const logGets = () => p.calls.filter(c => c.url.startsWith('/api/tcp/lab/log')).length;
  assert.equal(logGets(), 1, 'the log of the connection shown');
  await sr.tick(true);
  assert.equal(p.count('GET', '/api/tcp'), 1);
  assert.equal(el('#tcpTabCount').textContent, '1');
  assert.match(el('#tcpNote').innerHTML, /Live updates are off: values may be stale\./);
  const after = logGets();
  await sr.tick(true);
  assert.equal(logGets(), after, 'nothing changed: no log');
  p.tcp = list([si({ bytesIn: 2 })]);
  p.logs.lab = [entry(11), entry(12)];
  await sr.tick(true);
  assert.equal(logGets(), after + 1);
  assert.deepEqual(sr.tcpInfo().rt.logs.get('lab').entries.map(e => e.seq), [11, 12]);
  p.tcp = list([]);
  await sr.tick(true);
  assert.equal(el('#tcpTabCount').textContent, '');
  assert.deepEqual(sr.tcpInfo().rt.logs.get('lab').entries, [], 'the program forgot the connection');
});
