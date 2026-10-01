// Node tests for the schedules in ui.html: how the page reads their
// definitions (as schedule.go does), the overlay of the program over the
// variables, the snapshot the page polls, Send and cURL with ensure, export
// and import, and the actions it allows only with the program of its own
// version. fetch, the DOM, the timers and the storages are fakes.
// Run: node --test "tests/**/*.test.mjs"
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { loadUI, fakeDom, fakeTimers, settle } from './load-ui.mjs';

const copy = o => JSON.parse(JSON.stringify(o));
const ID = /^[A-Za-z0-9_-]{1,64}$/;

const DOC = {
  version: 1, settings: {}, history: [], activeEnv: 'e1',
  collections: [{ id: 'c1', name: 'Auth', requests: [
    { id: 'login', name: 'Login', method: 'POST', url: 'http://h/token', body: { mode: 'form', form: [{ name: 'password', value: '{{password}}' }] } },
    { id: 'me', name: 'Me', method: 'GET', url: 'http://h/me', auth: { type: 'bearer', token: '{{token}}' } },
  ] }],
  envs: [
    { id: 'e1', name: 'local', vars: [{ name: 'password', value: 'pw' }, { name: 'token', value: 'table' }, { enabled: false, name: 'refresh', value: 'off-row' }] },
    { id: 'e2', name: 'prod', vars: [{ name: 'token', value: 'prod-table' }] },
  ],
  schedules: [{
    id: 's1', name: 'Token refresh', requestId: 'login', envId: 'e1', everySec: 240, expiresPath: 'expires_in', leadSec: 60,
    extract: [{ enabled: true, optional: false, var: 'token', from: 'json', path: 'access_token' }],
  }],
};

// job and snapshot are answers of the program as schedule.go gives them.
const job = (over = {}) => ({
  enabled: false, state: 'off', enabledAt: 0, enabledBy: '', enabledLocal: false, nextAt: 0, nextFrom: '', lastRunAt: 0, lastOkAt: 0,
  failures: 0, lastError: '', blocked: '', changedSinceStop: false, problems: [], fields: [], snippet: '', log: [], ...over,
});
const snapshot = (over = {}) => ({ boot: 'b1', gen: 1, now: Date.now(), version: '1.2.0', stateTag: '"t1"', jobs: { s1: job() }, vars: {}, ...over });
const value = (v, over = {}) => ({ value: v, by: 's1', at: Date.now(), expiresAt: 0, ...over });

// res is the part of a fetch Response the page reads.
function res(status, body = '', headers = {}) {
  const get = n => { const k = Object.keys(headers).find(h => h.toLowerCase() === n.toLowerCase()); return k ? headers[k] : null; };
  return { status, ok: status >= 200 && status < 300, headers: { get }, text: async () => body, json: async () => JSON.parse(body) };
}

// fakeProgram answers like the program: /api/info, GET and PUT of
// /api/state with ETag and If-Match, and /api/send. A test answers the
// schedule routes itself through routes: a function of (url, init) per path,
// which takes a query too. A URL of another host is a request the browser
// sends without the program; direct answers it.
function fakeProgram(doc) {
  const p = { version: '1.2.0', os: 'linux', text: JSON.stringify(doc), tag: '"t1"', n: 1, calls: [], routes: {} };
  p.change = d => { p.text = JSON.stringify(d); p.tag = `"t${++p.n}"`; };
  p.count = (method, url) => p.calls.filter(c => c.method === method && c.url === url).length;
  p.body = url => JSON.parse(p.calls.filter(c => c.url === url).at(-1).body);
  p.sendAnswer = e => ({ status: 200, statusText: 'OK', headers: [], body: '{}', size: 2, timeMs: 1, finalUrl: e.url });
  p.direct = url => ({
    status: 200, statusText: 'OK', url, headers: new Map([['content-type', 'application/json']]),
    arrayBuffer: async () => new TextEncoder().encode('{}').buffer,
  });
  p.fetch = async (url, init = {}) => {
    const method = init.method || 'GET', h = init.headers || {};
    p.calls.push({ method, url, headers: h, body: init.body });
    if (/^https?:/.test(url)) return p.direct(url, init);
    const route = Object.keys(p.routes).find(k => url === k || url.startsWith(k + '?'));
    if (route) return p.routes[route](url, init);
    if (url === '/api/info') return res(200, JSON.stringify({ mode: 'server', version: p.version, os: p.os, arch: 'amd64', dataPath: '/d.json', boot: 'b1' }));
    if (url === '/api/send') return res(200, JSON.stringify(p.sendAnswer(JSON.parse(init.body))));
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
  return { getItem: k => (m.has(k) ? m.get(k) : null), setItem: (k, v) => { m.set(k, String(v)); } };
};

// setup gives a test a fresh page loaded from doc: by the program, or with
// standalone, straight from a file with localStorage. confirm answers yes
// (or what the test puts into confirms.answer) and records its questions;
// prompt records what the page offers to copy.
async function setup(t, doc, { standalone = false } = {}) {
  const p = fakeProgram(doc);
  const timers = fakeTimers();
  const keys = ['fetch', 'location', 'sessionStorage', 'localStorage', 'document', 'confirm', 'prompt'];
  const saved = keys.map(k => [k, Object.getOwnPropertyDescriptor(globalThis, k)]);
  const confirms = Object.assign([], { answer: true }), prompts = [];
  for (const [k, v] of Object.entries({
    fetch: p.fetch, sessionStorage: fakeStorage(), localStorage: fakeStorage(standalone ? { 'simple-rest': JSON.stringify(doc) } : {}),
    location: { protocol: standalone ? 'file:' : 'http:', reload() {} },
    confirm: msg => { confirms.push(msg); return confirms.answer; },
    prompt: (msg, text) => { prompts.push(text); return null; },
  })) Object.defineProperty(globalThis, k, { value: v, configurable: true, writable: true });
  const sr = loadUI();
  globalThis.document = fakeDom();
  t.after(() => {
    timers.restore();
    for (const [k, d] of saved) { delete globalThis[k]; if (d) Object.defineProperty(globalThis, k, d); }
  });
  await sr.detectMode();
  await sr.loadState();
  const me = () => sr.state.collections[0].requests[1];
  return { sr, p, timers, confirms, prompts, me, el: s => globalThis.document.querySelector(s) };
}

const methodsAndUrls = p => p.calls.filter(c => c.method !== 'GET').map(c => c.method + ' ' + c.url);
const bearerSent = p => p.body('/api/send').headers.find(h => h.name === 'Authorization').value;

// ---- definitions

test('normalizeSchedule reads the fields of a schedule as the program does', () => {
  const sr = loadUI();
  const s = sr.normalizeSchedule({
    id: 'k3j2a9x1lq', name: 'Token refresh', requestId: 'r', envId: 'e', everySec: 300.9, expiresPath: 'expires_in', leadSec: 7.5,
    enabled: true, note: 'kept',
    extract: [
      { var: 'token', path: 'access_token' },
      { enabled: false, optional: true, var: 'refresh', from: 'header', path: 'X-Refresh', mine: 1 },
      { from: 'xpath' }, { from: 'regex' }, null,
    ],
  });
  assert.deepEqual([s.id, s.name, s.requestId, s.envId, s.everySec, s.expiresPath, s.leadSec], ['k3j2a9x1lq', 'Token refresh', 'r', 'e', 300, 'expires_in', 7]);
  assert.deepEqual(s.extract, [
    { enabled: true, optional: false, var: 'token', from: 'json', path: 'access_token' },
    { enabled: false, optional: true, var: 'refresh', from: 'header', path: 'X-Refresh', mine: 1 },
    { enabled: true, optional: false, var: '', from: 'json', path: '' },
    { enabled: true, optional: false, var: '', from: 'regex', path: '' },
    { enabled: true, optional: false, var: '', from: 'json', path: '' },
  ], 'a rule is on unless it says otherwise, an unknown from is json');
  assert.deepEqual([s.enabled, s.note], [true, 'kept'], 'unknown fields stay; the program never reads enabled');

  const d = sr.normalizeSchedule({});
  assert.match(d.id, ID);
  assert.deepEqual({ ...d, id: '' }, { id: '', name: '', requestId: '', envId: '', everySec: 240, expiresPath: '', leadSec: 60, extract: [] });
  const nulls = sr.normalizeSchedule({ name: null, everySec: null, leadSec: null, expiresPath: null, extract: null });
  assert.deepEqual([nulls.name, nulls.everySec, nulls.leadSec, nulls.expiresPath, nulls.extract], ['', 240, 60, '', []], 'null: the default, or empty text');
});

test('normalizeSchedule keeps the seconds in range and takes another JSON type as missing', () => {
  const sr = loadUI();
  const n = x => sr.normalizeSchedule(x);
  assert.deepEqual([n({ everySec: 5 }).everySec, n({ everySec: 1e9 }).everySec, n({ everySec: -1 }).everySec], [10, 86400, 10]);
  assert.deepEqual([n({ leadSec: -3 }).leadSec, n({ leadSec: 4000 }).leadSec, n({ leadSec: 0.9 }).leadSec], [0, 3600, 0]);
  assert.equal(n(JSON.parse('{"everySec":1e400}')).everySec, 86400, 'a number too big for a double');
  const wrong = n({ name: 5, requestId: {}, envId: [], everySec: '300', leadSec: true, expiresPath: 1, extract: 'x' });
  assert.deepEqual([wrong.name, wrong.requestId, wrong.envId, wrong.everySec, wrong.leadSec, wrong.expiresPath, wrong.extract], ['', '', '', 240, 60, '', []]);
  const rule = n({ extract: [{ enabled: 'yes', optional: 1, var: 7, from: 3, path: false }] }).extract[0];
  assert.deepEqual(rule, { enabled: true, optional: false, var: '', from: 'json', path: '' });
  const many = n({ extract: Array.from({ length: 25 }, (_, i) => ({ var: 'v' + i, path: 'p' })) }).extract;
  assert.deepEqual([many.length, many.at(-1).var], [20, 'v19'], 'only the first 20 rules count');
});

test('normalizeDoc gives new ids to schedules the program would skip, and keeps __proto__ as a key', () => {
  const sr = loadUI();
  const doc = sr.normalizeDoc({ schedules: [{ id: 'bad id' }, { id: 's1' }, { id: 's1' }, { id: 'x'.repeat(65) }, { id: 5 }, { id: 'ok_-9' }] });
  const ids = doc.schedules.map(s => s.id);
  assert.equal(ids[1], 's1');
  assert.equal(ids[5], 'ok_-9');
  for (const i of [0, 2, 3, 4]) assert.ok(!['bad id', 's1', 'x'.repeat(65), '5'].includes(ids[i]), 'new id ' + i);
  assert.equal(new Set(ids).size, ids.length, 'unique');
  for (const id of ids) assert.match(id, ID);

  const p = sr.normalizeDoc(JSON.parse('{"schedules":[{"id":"a","__proto__":{"x":1},"extract":[{"var":"t","__proto__":{"y":2}}]}]}'));
  const [s] = p.schedules;
  assert.equal(Object.getPrototypeOf(s), Object.prototype);
  assert.equal(Object.getPrototypeOf(s.extract[0]), Object.prototype);
  sr.applyState(p);
  const out = JSON.parse(sr.serializeState()).schedules[0];
  assert.deepEqual(Object.getOwnPropertyDescriptor(out, '__proto__').value, { x: 1 });
  assert.deepEqual(Object.getOwnPropertyDescriptor(out.extract[0], '__proto__').value, { y: 2 });
});

test('requestTexts and referencedVars count what buildEffective substitutes, each name once', () => {
  const sr = loadUI();
  const r = sr.normalizeRequest({
    url: '{{base}}/x?k={{ key }}',
    headers: [{ name: 'A', value: '{{h1}}' }, { enabled: false, name: 'B', value: '{{off}}' }, { name: ' ', value: '{{blank}}' }],
    auth: { type: 'bearer', token: '{{token}}', username: '{{user}}', keyValue: '{{kv}}' },
    body: { mode: 'form', raw: '{{raw}}', form: [{ name: 'f', value: '{{f1}}' }, { enabled: false, name: 'g', value: '{{g}}' }, { name: '', value: '{{nameless}}' }] },
  });
  assert.deepEqual(sr.requestTexts(r), ['{{base}}/x?k={{ key }}', 'A', '{{h1}}', '{{token}}', 'f', '{{f1}}']);
  assert.deepEqual(sr.referencedVars(sr.requestTexts(r)), ['base', 'key', 'h1', 'token', 'f1']);

  const basic = sr.normalizeRequest({ auth: { type: 'basic', username: '{{u}}', password: '{{p}}', token: '{{t}}' }, body: { mode: 'raw', raw: '{"a":"{{raw}}"}' } });
  assert.deepEqual(sr.referencedVars(sr.requestTexts(basic)), ['u', 'p', 'raw']);
  const key = sr.normalizeRequest({ auth: { type: 'apikey', keyName: '{{kn}}', keyValue: '{{kv}}' } });
  assert.deepEqual(sr.referencedVars(sr.requestTexts(key)), ['kn', 'kv']);
  const blankKey = sr.normalizeRequest({ auth: { type: 'apikey', keyName: ' ', keyValue: '{{kv}}' } });
  assert.deepEqual(sr.referencedVars(sr.requestTexts(blankKey)), [], 'no key name: buildEffective adds no key');
  assert.deepEqual(sr.referencedVars(['{{a}}{{a}}', '{{ b }}', '{{a}}', '{{c d}}', '{{x.y-z}}', null]), ['a', 'b', 'x.y-z']);
});

test('schedulesSetting finds the schedules of the environment that set one of the names', () => {
  const sr = loadUI();
  sr.state.schedules = [
    sr.normalizeSchedule({ id: 'a', envId: 'e1', extract: [{ var: 'token' }, { enabled: false, var: 'refresh' }] }),
    sr.normalizeSchedule({ id: 'b', envId: 'e2', extract: [{ var: 'token' }] }),
  ];
  const ids = (env, names) => sr.schedulesSetting(env, names).map(s => s.id);
  assert.deepEqual(ids('e1', ['token']), ['a']);
  assert.deepEqual(ids('e2', ['x', 'token']), ['b']);
  assert.deepEqual(ids('e1', ['refresh']), [], 'a rule that is off sets nothing');
  assert.deepEqual(ids('e1', ['other']), []);
  assert.deepEqual(ids('', ['token']), [], 'No environment');
});

test('varConflicts and schedulesUsing', () => {
  const sr = loadUI();
  const [a, b, c, d] = sr.state.schedules = [
    sr.normalizeSchedule({ id: 'a', name: 'A', requestId: 'r1', envId: 'e1', extract: [{ var: 'token' }, { enabled: false, var: 'refresh' }] }),
    sr.normalizeSchedule({ id: 'b', name: 'Login B', requestId: 'r2', envId: 'e1', extract: [{ var: 'token' }] }),
    sr.normalizeSchedule({ id: 'c', name: 'C', requestId: 'r1', envId: 'e2', extract: [{ var: 'token' }] }),
    sr.normalizeSchedule({ id: 'd', name: 'D', requestId: 'r3', envId: 'e1', extract: [{ var: 'refresh' }, { var: 'token', enabled: false }] }),
  ];
  assert.deepEqual(sr.varConflicts(a).map(x => [x.name, x.by.id]), [['token', 'b']], 'another environment and rules that are off do not count');
  assert.deepEqual(sr.varConflicts(b).map(x => [x.name, x.by.id]), [['token', 'a']]);
  assert.deepEqual(sr.varConflicts(c), []);
  assert.deepEqual(sr.varConflicts(d), []);
  const ids = what => sr.schedulesUsing(what).map(s => s.id);
  assert.deepEqual(ids({ requestIds: ['r1'] }), ['a', 'c']);
  assert.deepEqual(ids({ requestIds: ['r2', 'r3'] }), ['b', 'd']);
  assert.deepEqual(ids({ envId: 'e2' }), ['c']);
  assert.deepEqual(ids({}), []);
});

test('maskValue shows a few characters and the length, isExpired compares with the lifetime', () => {
  const sr = loadUI();
  assert.equal(sr.maskValue(''), '(0 chars)');
  assert.equal(sr.maskValue('a'), '(1 char)');
  assert.equal(sr.maskValue('x'.repeat(12)), '(12 chars)', 'up to 12 characters: the length only');
  assert.equal(sr.maskValue('abcdefghijklm'), 'abc... (13 chars)');
  assert.equal(sr.maskValue('eyJhbGciOiJIUzI1NiJ9' + 'x'.repeat(792)), 'eyJhbG... (812 chars)', 'at most 6');
  assert.equal(sr.maskValue('\u{1F600}'.repeat(16)), '\u{1F600}'.repeat(4) + '... (16 chars)', 'characters, not UTF-16 units');

  const v = { value: 't', by: 's1', at: 1000, expiresAt: 5000 };
  assert.equal(sr.isExpired(v, 4999), false);
  assert.equal(sr.isExpired(v, 5000), true);
  const noLife = { value: 't', by: 's1', at: 1000, expiresAt: 0 };
  assert.equal(sr.isExpired(noLife, 1e15), false, 'no lifetime and no Every: never');
  assert.equal(sr.isExpired(noLife, 10999, 10), false);
  assert.equal(sr.isExpired(noLife, 11000, 10), true, 'Every after it was set');
});

// ---- the overlay and the snapshot

test('effectiveVars: the overlay wins in its environment and stays out of the others', async t => {
  const { sr, p, me } = await setup(t, DOC);
  sr.applySnapshot(snapshot({ vars: { e1: { token: value('fresh'), refresh: value('r2'), extra: value('x') } } }));
  const v = sr.effectiveVars('e1');
  assert.deepEqual([v.token, v.refresh, v.extra, v.password], ['fresh', 'r2', 'x', 'pw'], 'over a row, over a disabled row, without a row');
  assert.equal(sr.effectiveVars('e2').token, 'prod-table');
  assert.equal(sr.effectiveVars('').token, undefined);
  assert.equal(sr.buildEffective(me()).headers.find(h => h.name === 'Authorization').value, 'Bearer fresh', 'the active environment, e1');
  sr.setActiveEnv('e2');
  assert.equal(sr.buildEffective(me()).headers.find(h => h.name === 'Authorization').value, 'Bearer prod-table');
  assert.equal(p.count('GET', '/api/state'), 1, 'a snapshot with the tag the tab has asks for nothing');
});

test('applySnapshot: another boot tells of the restart when it undid something', async t => {
  const { sr, el } = await setup(t, DOC);
  const text = 'simple-rest was restarted: all schedules are off. Turn them on in Schedules.';
  sr.applySnapshot(snapshot({ jobs: { s1: job({ enabled: true, state: 'ok' }) } }));
  assert.equal(el('#toast').textContent, '');
  sr.applySnapshot(snapshot({ boot: 'b2' }));
  assert.equal(el('#toast').textContent, text, 'a schedule was on');
  el('#toast').textContent = '';
  sr.applySnapshot(snapshot({ boot: 'b3' }));
  assert.equal(el('#toast').textContent, '', 'nothing was on and the overlay was empty');
  sr.applySnapshot(snapshot({ boot: 'b3', gen: 2, vars: { e1: { token: value('t') } } }));
  sr.applySnapshot(snapshot({ boot: 'b4' }));
  assert.equal(el('#toast').textContent, text, 'the overlay had a value');
  assert.deepEqual(sr.schedInfo().ovl, { boot: 'b4', vars: {} });
});

test('applySnapshot: another version makes the page stale, a new stateTag syncs the tab', async t => {
  const { sr, p, el } = await setup(t, DOC);
  sr.applySnapshot(snapshot({ now: Date.now() + 60000 }));
  assert.ok(Math.abs(sr.schedInfo().skew - 60000) < 1000, 'the clock of the program');
  await settle();
  assert.equal(p.count('GET', '/api/state'), 1, 'the same tag: no request');

  const theirs = copy(DOC);
  theirs.collections[0].name = 'Theirs';
  p.change(theirs);
  sr.applySnapshot(snapshot({ gen: 2, stateTag: p.tag }));
  await settle();
  assert.equal(sr.state.collections[0].name, 'Theirs');
  assert.equal(sr.saveInfo().lastTag, p.tag);
  assert.equal(sr.schedInfo().seenTag, p.tag);

  assert.equal(sr.featureState(), 'ok');
  sr.applySnapshot(snapshot({ gen: 3, stateTag: p.tag, version: '1.2.1' }));
  assert.equal(sr.saveInfo().saveState, 'stale');
  assert.equal(sr.featureState(), 'stale');
  assert.equal(el('#staleBanner').hidden, false);
});

test('tick polls with since, takes 204 as no change and shows ? while the program is away', async t => {
  const { sr, p, el } = await setup(t, DOC);
  let answer = () => res(200, JSON.stringify(snapshot({ gen: 7 })));
  p.routes['/api/schedules'] = () => answer();
  const polls = () => p.calls.filter(c => c.url.startsWith('/api/schedules')).map(c => c.url);
  await sr.tick(true);
  assert.deepEqual(polls(), ['/api/schedules']);
  answer = () => res(204);
  await sr.tick(true);
  assert.equal(polls()[1], '/api/schedules?since=b1.7');
  assert.equal(sr.schedInfo().snap.gen, 7);

  answer = () => { throw new TypeError('Failed to fetch'); };
  await sr.tick(true);
  assert.equal(el('#schedCount').textContent, '?');
  assert.equal(el('#schedBtn').title, 'Schedules: simple-rest does not answer');

  answer = () => res(200, JSON.stringify(snapshot({ gen: 8, jobs: { s1: job({ enabled: true, state: 'ok' }) } })));
  await sr.tick(true);
  assert.equal(el('#schedCount').textContent, '1');
  assert.equal(el('#schedBtn').title, 'Schedules: 1 on');
  assert.equal(el('#schedBtn').classList.contains('err'), false);
  answer = () => res(200, JSON.stringify(snapshot({ gen: 9, jobs: { s1: job({ enabled: true, state: 'stopped', blocked: 'HTTP 404. Stopped after one try.' }) } })));
  await sr.tick(true);
  assert.equal(el('#schedCount').textContent, '', 'a stopped schedule does not count');
  assert.equal(el('#schedBtn').title, 'Schedules: 0 on, 1 stopped');
  assert.equal(el('#schedBtn').classList.contains('err'), true);
});

test('tick waits for its turn: 10 s in a visible tab, never in the background', async t => {
  const { sr, p } = await setup(t, DOC);
  p.routes['/api/schedules'] = () => res(204);
  const polls = () => p.calls.filter(c => c.url.startsWith('/api/schedules')).length;
  await sr.tick(true);
  await sr.tick();
  assert.equal(polls(), 1, 'too early');
  document.visibilityState = 'hidden';
  await sr.tick();
  assert.equal(polls(), 1);
});

// ---- Send and cURL

test('Send saves first, then refreshes the value with ensure, then builds the request with it', async t => {
  const { sr, p, me } = await setup(t, DOC);
  sr.applySnapshot(snapshot());
  sr.loadRequest(me(), 'c1');
  p.routes['/api/vars/ensure'] = () => res(200, JSON.stringify({ boot: 'b1', envId: 'e1', vars: { token: value('fresh') }, notes: [] }));
  sr.state.envs[1].name = 'Production';
  sr.persist(); // a change waits for the timer
  await sr.sendRequest();
  assert.deepEqual(methodsAndUrls(p), ['PUT /api/state', 'POST /api/vars/ensure', 'POST /api/send']);
  const ensure = p.calls.find(c => c.url === '/api/vars/ensure');
  assert.equal(ensure.headers['Content-Type'], 'application/json');
  assert.deepEqual(JSON.parse(ensure.body), { envId: 'e1', names: ['token'] });
  assert.equal(bearerSent(p), 'Bearer fresh');
  assert.equal(sr.state.history[0].req.auth.token, '{{token}}', 'the history keeps the template, not the value');
  assert.equal(sr.schedInfo().ovl.vars.e1.token.value, 'fresh');
});

test('Send shows Refreshing token… while ensure takes over 300 ms', async t => {
  const { sr, p, timers, el, me } = await setup(t, DOC);
  sr.applySnapshot(snapshot());
  sr.loadRequest(me(), 'c1');
  let release;
  p.routes['/api/vars/ensure'] = () => new Promise(r => { release = () => r(res(200, JSON.stringify({ boot: 'b1', envId: 'e1', vars: {}, notes: [] }))); });
  const send = sr.sendRequest();
  await settle();
  assert.equal(el('#respStatus').textContent, 'Sending…');
  await timers.run(300);
  assert.equal(el('#respStatus').textContent, 'Refreshing token…');
  release();
  await send;
  assert.match(el('#respStatus').textContent, /^200/);
  assert.equal(bearerSent(p), 'Bearer table', 'no overlay value: the row');
});

for (const [what, answer] of [
  ['an error', () => res(500, '{"error":"boom"}')],
  ['no answer', () => { throw new TypeError('Failed to fetch'); }],
  ['a timeout', () => { throw new DOMException('The operation was aborted due to timeout', 'TimeoutError'); }],
]) {
  test(`Send goes out with the value the tab has after ${what} of ensure`, async t => {
    const { sr, p, me } = await setup(t, DOC);
    sr.applySnapshot(snapshot({ vars: { e1: { token: value('old') } } }));
    sr.loadRequest(me(), 'c1');
    p.routes['/api/vars/ensure'] = answer;
    await sr.sendRequest();
    assert.equal(p.count('POST', '/api/vars/ensure'), 1);
    assert.equal(bearerSent(p), 'Bearer old');
  });
}

test('Send asks nothing when no schedule of the environment sets a variable of the request', async t => {
  const { sr, p, me } = await setup(t, DOC);
  sr.applySnapshot(snapshot());
  sr.loadRequest(sr.state.collections[0].requests[0], 'c1'); // Login uses {{password}}
  await sr.sendRequest();
  sr.loadRequest(me(), 'c1');
  sr.setActiveEnv('e2'); // s1 works in e1
  await sr.flushPersist();
  await sr.sendRequest();
  assert.equal(p.count('POST', '/api/send'), 2);
  assert.equal(p.count('POST', '/api/vars/ensure'), 0);
  assert.equal(bearerSent(p), 'Bearer prod-table');
});

test('ensure: the answer replaces the values asked for and leaves the others', async t => {
  const { sr, p } = await setup(t, DOC);
  sr.applySnapshot(snapshot({ vars: { e1: { token: value('old'), refresh: value('r') } } }));
  p.routes['/api/vars/ensure'] = () => res(200, JSON.stringify({ boot: 'b1', envId: 'e1', vars: {}, notes: [{ id: 's1', state: 'off' }] }));
  assert.deepEqual(await sr.ensureVars(['token', 'bad name', 'x'.repeat(129)], 'e1'), [{ id: 's1', state: 'off' }]);
  assert.deepEqual(p.body('/api/vars/ensure').names, ['token'], 'only names the program takes');
  assert.deepEqual(Object.keys(sr.schedInfo().ovl.vars.e1), ['refresh'], 'token was cleared in the program');
});

test('Send shows the note that matters most', async t => {
  const stopped = 'Login rejected (HTTP 401). Stopped so the account does not get locked. Fix the request, then press Run now.';
  const next = new Date(2026, 9, 1, 12, 7, 30).getTime();
  const gone = new Date(2026, 9, 1, 12, 9, 30).getTime();
  const cases = [
    [[{ state: 'off' }, { state: 'failed', error: 'HTTP 503' }, { state: 'stopped', error: stopped }], 'Schedule "Token refresh" is stopped: Login rejected (HTTP 401). Sent with the old value.'],
    [[{ state: 'expired' }, { state: 'config', error: 'Variable {{password}} is not set in "prod".' }, { state: 'failed', error: 'HTTP 503' }], 'Schedule "Token refresh" failed: HTTP 503. Sent with the old value.'],
    [[{ state: 'missing', nextAt: next }, { state: 'config', error: 'Variable {{password}} is not set in "prod".' }], 'Schedule "Token refresh" could not run: Variable {{password}} is not set in "prod".'],
    [[{ state: 'running' }, { state: 'missing', nextAt: next }], '{{token}} is not set yet by schedule "Token refresh". Next run at 12:07.'],
    [[{ state: 'off' }, { state: 'expired' }, { state: 'running' }], 'Schedule "Token refresh" is still running. Sent with the old value.'],
    [[{ state: 'off' }, { state: 'expired' }], '{{token}} from schedule "Token refresh" expired at 12:09 and the schedule is off. Turn it on in Schedules.'],
    [[{ state: 'off' }], '{{token}} comes from schedule "Token refresh", which is off. Turn it on in Schedules.'],
  ];
  for (const [notes, want] of cases) {
    await t.test(want, async t => {
      const { sr, p, el, me } = await setup(t, DOC);
      sr.applySnapshot(snapshot());
      sr.loadRequest(me(), 'c1');
      const expired = notes.some(n => n.state === 'expired');
      const vars = expired ? { token: value('old', { at: gone - 300000, expiresAt: gone }) } : {};
      p.routes['/api/vars/ensure'] = () => res(200, JSON.stringify({ boot: 'b1', envId: 'e1', vars, notes: notes.map(n => ({ id: 's1', name: 'Token refresh', ...n })) }));
      await sr.sendRequest();
      assert.equal(el('#toast').textContent, want);
    });
  }
});

test('a failed lookup of a .local name gets the mDNS hint on Linux only', async t => {
  const { sr, p, el } = await setup(t, DOC);
  const hint = '. This build cannot resolve .local names (mDNS). Use the IP address.';
  p.sendAnswer = () => ({ status: 0, statusText: '', proto: '', headers: [], body: '', bodyBase64: false, size: 0, timeMs: 3, finalUrl: '', error: 'dial tcp: lookup pi.local: no such host' });
  sr.loadRequest(sr.normalizeRequest({ url: 'http://pi.local:8080/token' }), null);
  await sr.sendRequest();
  assert.equal(el('#respOut').innerHTML, `<span class="x">dial tcp: lookup pi.local: no such host${hint}</span>`);
  sr.loadRequest(sr.normalizeRequest({ url: 'http://pi.lan/token' }), null);
  await sr.sendRequest();
  assert.doesNotMatch(el('#respOut').innerHTML, /mDNS/);
});

test('cURL copies the request with the refreshed value', async t => {
  const { sr, p, prompts, me } = await setup(t, DOC);
  sr.applySnapshot(snapshot());
  sr.loadRequest(me(), 'c1');
  p.routes['/api/vars/ensure'] = () => res(200, JSON.stringify({ boot: 'b1', envId: 'e1', vars: { token: value('fresh') }, notes: [] }));
  await sr.copyCurl();
  assert.equal(p.count('POST', '/api/vars/ensure'), 1);
  assert.match(prompts[0], /-H 'Authorization: Bearer fresh'/);
});

test('without the program Send goes straight from the browser and asks nothing about schedules', async t => {
  const { sr, p, me } = await setup(t, DOC, { standalone: true });
  assert.equal(sr.featureState(), 'nobin');
  sr.loadRequest(me(), 'c1');
  await sr.sendRequest();
  assert.deepEqual(p.calls.map(c => c.url), ['http://h/me']);
});

// ---- actions

test('the actions ask the program after saving and show what it answers', async t => {
  const { sr, p, el } = await setup(t, DOC);
  sr.applySnapshot(snapshot());
  p.routes['/api/schedules'] = () => res(204);
  sr.openSchedModal('s1');
  assert.equal(el('#schedModal').classList.contains('open'), true);
  assert.equal(el('#schedName').value, 'Token refresh');
  assert.equal(el('#schedBanner').hidden, true, 'no banner with the program of the same version');
  p.calls.length = 0;
  p.routes['/api/schedules/s1/enable'] = () => res(200, JSON.stringify(job({ enabled: true, state: 'waiting', enabledAt: Date.now(), enabledBy: '192.168.1.20' })));
  sr.state.schedules[0].name = 'Renamed';
  sr.persist();
  const r = await sr.schedAction('s1', 'enable');
  assert.equal(r.status, 200);
  assert.deepEqual(methodsAndUrls(p), ['PUT /api/state', 'POST /api/schedules/s1/enable']);
  const call = p.calls.find(c => c.url === '/api/schedules/s1/enable');
  assert.deepEqual([call.headers['Content-Type'], call.body], ['application/json', '{}']);
  assert.equal(sr.schedInfo().snap.jobs.s1.enabled, true);
  assert.equal(el('#schedCount').textContent, '1');
  assert.match(el('#schedSince').textContent, /^since \d\d:\d\d, from 192\.168\.1\.20$/);

  p.routes['/api/schedules/s1/run'] = () => res(422, JSON.stringify({ error: 'This schedule cannot run: No request is picked.', problems: ['No request is picked.'] }));
  assert.equal((await sr.schedAction('s1', 'run')).status, 422);
  assert.match(el('#schedHints').innerHTML, /This schedule cannot run: No request is picked\./);
  p.routes['/api/schedules/s1/run'] = () => res(503, '{"error":"simple-rest is shutting down"}');
  await sr.schedAction('s1', 'run');
  assert.match(el('#schedHints').innerHTML, /simple-rest is shutting down\./);
  p.routes['/api/schedules/s1/run'] = () => res(200, JSON.stringify(job({ state: 'off', lastRunAt: 5, lastOkAt: 5 })));
  await sr.schedAction('s1', 'run');
  assert.match(el('#schedHints').innerHTML, /Value set once\. Tick Enabled to keep it fresh\./, 'Run now on a schedule that is off');
});

// held answers a route only when the test calls release, as a slow program.
function held() {
  const h = { calls: 0 };
  h.route = answer => () => { h.calls++; return new Promise(r => { h.release = () => r(answer()); }); };
  return h;
}
const TWO = { ...copy(DOC), schedules: [...copy(DOC).schedules, { ...copy(DOC).schedules[0], id: 's2', name: 'Other', envId: 'e2' }] };

test('while Run now of one schedule is on its way, another shows its own Enabled box and Run now', async t => {
  const { sr, p, el } = await setup(t, TWO);
  sr.applySnapshot(snapshot({ jobs: { s1: job(), s2: job({ enabled: true, state: 'ok', enabledAt: Date.now(), enabledBy: '10.0.0.9' }) } }));
  p.routes['/api/schedules'] = () => res(204);
  const run = held();
  p.routes['/api/schedules/s1/run'] = run.route(() => res(200, JSON.stringify(job({ state: 'off', lastRunAt: 5, lastOkAt: 5 }))));
  sr.openSchedModal('s1');
  const done = sr.schedAction('s1', 'run');
  await settle();
  assert.equal(run.calls, 1);
  assert.equal(el('#schedRunBtn').disabled, true, 'Run now of s1 is on its way');
  sr.openSchedModal('s2');
  assert.equal(el('#schedEnabled').checked, true);
  assert.match(el('#schedSince').textContent, /^since \d\d:\d\d, from 10\.0\.0\.9$/);
  assert.deepEqual([el('#schedRunBtn').disabled, el('#schedRunBtn').textContent], [false, 'Run now'], 's2 has no run on its way');
  sr.openSchedModal('s1');
  assert.deepEqual([el('#schedEnabled').checked, el('#schedSince').textContent], [false, ''], 's1 shows its own box, not the one of s2');
  assert.equal(el('#schedRunBtn').disabled, true);
  run.release();
  await done;
  assert.deepEqual([el('#schedRunBtn').disabled, el('#schedRunBtn').textContent], [false, 'Run now']);
});

test('a click on Enabled shows until its own answer, whatever answers in between', async t => {
  const { sr, p, el } = await setup(t, TWO);
  sr.applySnapshot(snapshot({ jobs: { s1: job(), s2: job({ enabled: true, state: 'ok', enabledAt: Date.now(), enabledBy: '10.0.0.9' }) } }));
  p.routes['/api/schedules'] = () => res(204);
  const on = held();
  p.routes['/api/schedules/s1/enable'] = on.route(() => res(200, JSON.stringify(job({ enabled: true, state: 'waiting', enabledAt: Date.now(), enabledBy: '127.0.0.1' }))));
  p.routes['/api/schedules/s2/disable'] = () => res(200, JSON.stringify(job()));
  sr.openSchedModal('s1');
  el('#schedEnabled').checked = true; // the click
  const enabling = sr.schedAction('s1', 'enable');
  await settle();
  sr.openSchedModal('s2');
  el('#schedEnabled').checked = false;
  await sr.schedAction('s2', 'disable');
  assert.equal(el('#schedEnabled').checked, false);
  sr.openSchedModal('s1');
  assert.equal(el('#schedEnabled').checked, true, 'the answer for s2 does not undo the click on s1');
  sr.applySnapshot(snapshot({ gen: 5, jobs: { s1: job(), s2: job() } }));
  assert.equal(el('#schedEnabled').checked, true, 'nor does a poll from before it');
  on.release();
  await enabling;
  assert.deepEqual([el('#schedEnabled').checked, el('#schedSince').textContent.endsWith('from 127.0.0.1')], [true, true]);
});

test('Enabled and Run now wait for a reload when the changes of the tab cannot be saved; turning off does not', async t => {
  const { sr, p, el } = await setup(t, DOC);
  sr.applySnapshot(snapshot({ jobs: { s1: job({ enabled: true, state: 'ok' }) } }));
  p.routes['/api/schedules'] = () => res(204);
  for (const a of ['run', 'enable', 'disable']) p.routes['/api/schedules/s1/' + a] = () => res(200, JSON.stringify(job()));
  p.routes['/api/schedules/disable-all'] = () => res(200, '{"off":1}');
  sr.openSchedModal('s1');
  p.change(copy(DOC)); // another tab saved meanwhile: the save of this tab gets 412
  sr.state.schedules[0].requestId = 'me';
  sr.persist();
  assert.equal(await sr.schedAction('s1', 'run'), null);
  assert.equal(sr.saveInfo().saveState, 'conflict');
  assert.equal(await sr.schedAction('s1', 'enable'), null);
  assert.deepEqual([p.count('POST', '/api/schedules/s1/run'), p.count('POST', '/api/schedules/s1/enable')], [0, 0], 'the program would use the old definition');
  assert.match(el('#schedHints').innerHTML, /Your changes are not saved, so simple-rest would use the old definition\. Reload the page\./);
  assert.equal(el('#schedRunBtn').disabled, false);
  assert.equal((await sr.schedAction('s1', 'disable')).status, 200, 'turning off needs no definition');
  await sr.turnAllOff();
  assert.equal(p.count('POST', '/api/schedules/disable-all'), 1);
});

test('a poll that gets no answer gives up within 10 s and shows ?', async t => {
  const { sr, p, el } = await setup(t, DOC);
  const ctrl = new AbortController(), asked = [], timeout = AbortSignal.timeout;
  AbortSignal.timeout = ms => { asked.push(ms); return ctrl.signal; };
  t.after(() => { AbortSignal.timeout = timeout; });
  p.routes['/api/schedules'] = (url, init) => new Promise((_, reject) => {
    if (init.signal) init.signal.addEventListener('abort', () => reject(init.signal.reason));
  });
  sr.tick(true);
  await settle();
  assert.equal(el('#schedCount').textContent, '');
  ctrl.abort(new DOMException('The operation was aborted due to timeout', 'TimeoutError'));
  await settle();
  assert.equal(el('#schedCount').textContent, '?');
  assert.equal(asked.length, 1);
  assert.ok(asked[0] < 10000, 'shorter than the 10 s between polls');
});

test('Response shows an error answer as it came, and of a 2xx answer only the keys', async t => {
  const { sr, el } = await setup(t, DOC);
  const jwt = 'eyJhbGciOi' + 'x'.repeat(802);
  const ok = JSON.stringify({ accessToken: jwt, refresh_token: 'r-123', data: { list: ['secret-1'] }, expires_in: 300 });
  const stopped = (status, snippet) => snapshot({ gen: status, jobs: { s1: job({
    lastRunAt: 5, lastError: 'No value for token at access_token. Stopped.', snippet, log: [{ at: Date.now(), status, timeMs: 5, note: 'x' }],
  }) } });
  sr.openSchedModal('s1');
  sr.applySnapshot(stopped(200, ok));
  let html = el('#schedRun').innerHTML;
  for (const secret of [jwt, 'r-123', 'secret-1']) assert.ok(!html.includes(secret), secret);
  assert.ok(html.includes('{&quot;accessToken&quot;:&quot;eyJhbG... (812 chars)&quot;,&quot;refresh_token&quot;:&quot;(5 chars)&quot;,&quot;data&quot;:{&quot;list&quot;:[&quot;(8 chars)&quot;]},&quot;expires_in&quot;:300}'), html);

  sr.applySnapshot(stopped(201, ok.slice(0, 40) + '...'));
  html = el('#schedRun').innerHTML;
  assert.ok(!html.includes('eyJhbGciOix'), 'cut short inside a value');
  assert.ok(html.includes('{&quot;accessToken&quot;:&quot;eyJhbG... (27 chars)&quot;'), html);

  sr.applySnapshot(stopped(200, 'access_token=abc123def456ghi&expires_in=300'));
  assert.ok(!el('#schedRun').innerHTML.includes('abc123'), 'a 2xx answer that is not JSON is not shown');
  assert.ok(!el('#schedRun').innerHTML.includes('Response'));

  sr.applySnapshot(stopped(401, '{"error":"invalid_grant"}'));
  assert.ok(el('#schedRun').innerHTML.includes('<div class="k">Response</div><div><span class="mono">{&quot;error&quot;:&quot;invalid_grant&quot;}</span></div>'));
});

test('Turn all off asks first when more than one is on; Delete turns the schedule off first', async t => {
  const { sr, p, confirms } = await setup(t, DOC);
  sr.applySnapshot(snapshot({ jobs: { s1: job({ enabled: true, state: 'ok' }), s2: job({ enabled: true, state: 'ok' }) } }));
  p.routes['/api/schedules'] = () => res(204);
  p.routes['/api/schedules/disable-all'] = () => res(200, '{"off":2}');
  confirms.answer = false;
  await sr.turnAllOff();
  assert.deepEqual([...confirms], ['Turn all 2 schedules off?']);
  assert.equal(p.count('POST', '/api/schedules/disable-all'), 0);
  confirms.answer = true;
  await sr.turnAllOff();
  assert.equal(p.count('POST', '/api/schedules/disable-all'), 1);

  p.routes['/api/schedules/s1/disable'] = () => res(200, JSON.stringify(job()));
  await sr.deleteSched(sr.state.schedules[0]);
  assert.equal(confirms.at(-1), 'Delete schedule "Token refresh"?');
  assert.equal(p.count('POST', '/api/schedules/s1/disable'), 1);
  assert.deepEqual(sr.state.schedules, []);
  assert.equal(await sr.flushPersist(), true);
  assert.deepEqual(JSON.parse(p.text).schedules, []);
});

test('Install and restart says how many schedules turn off', async t => {
  const { sr, p, confirms } = await setup(t, DOC);
  sr.applySnapshot(snapshot({ jobs: { s1: job({ enabled: true, state: 'ok' }), s2: job({ enabled: true, state: 'stopped' }), s3: job() } }));
  confirms.answer = false;
  await sr.installUpdate();
  assert.deepEqual([...confirms], ['Installing restarts the program: 2 schedule(s) turn off. Continue?']);
  assert.equal(p.count('POST', '/api/update/install'), 0);
});

test('+ Schedule takes the saved request, off, and makes an environment when there is none', async t => {
  const { sr, el, me } = await setup(t, { ...copy(DOC), envs: [], activeEnv: '', schedules: [] });
  sr.loadRequest(sr.newRequest(), null);
  assert.equal(sr.addSchedule(null), null);
  assert.equal(el('#schedAddHint').textContent, 'Save the request to a collection first');
  sr.loadRequest(me(), 'c1');
  const s = sr.addSchedule(null);
  assert.deepEqual([s.name, s.requestId, s.everySec, s.leadSec, s.expiresPath], ['Me', 'me', 240, 60, 'expires_in']);
  assert.deepEqual(s.extract, [{ enabled: true, optional: false, var: 'token', from: 'json', path: 'access_token' }]);
  assert.deepEqual(sr.state.envs.map(e => e.name), ['Default']);
  assert.equal(s.envId, sr.state.envs[0].id);
  assert.equal(sr.state.activeEnv, s.envId, 'picked in this tab');
  assert.equal(sr.schedInfo().snap, null, 'nothing turned on');
  assert.equal(await sr.flushPersist(), true);
});

test('featureState: without the program the actions do nothing', async t => {
  const { sr, p, el } = await setup(t, DOC, { standalone: true });
  assert.equal(sr.featureState(), 'nobin');
  sr.openSchedModal();
  assert.equal(el('#schedBanner').hidden, false);
  assert.equal(el('#schedBanner').textContent, 'Schedules run inside the simple-rest program. Open this page from the program to use them.');
  assert.equal(el('#schedName').value, 'Token refresh', 'the definition is there to read');
  assert.equal(await sr.schedAction('s1', 'enable'), null);
  await sr.turnAllOff();
  await sr.clearVar('e1', 'token');
  await sr.deleteSched(sr.state.schedules[0]);
  assert.equal(sr.addSchedule(sr.state.collections[0].requests[1]), null);
  await sr.tick(true);
  assert.equal(sr.state.schedules.length, 1, 'the definition stays');
  assert.deepEqual(p.calls, []);
});

test('featureState: from another version the actions and ensure stop, Send still works', async t => {
  const { sr, p, el, me } = await setup(t, DOC);
  sr.openSchedModal();
  sr.applySnapshot(snapshot({ version: '1.2.1', jobs: { s1: job({ enabled: true, state: 'ok' }) }, vars: { e1: { token: value('t') } } }));
  assert.equal(sr.featureState(), 'stale');
  assert.equal(el('#schedBanner').hidden, false);
  assert.equal(el('#schedBanner').textContent, 'This page comes from another simple-rest version than the running program. Reload the page.');
  await settle();
  const before = p.calls.length;
  assert.equal(await sr.schedAction('s1', 'run'), null);
  await sr.turnAllOff();
  await sr.clearVar('e1', 'token');
  await sr.deleteSched(sr.state.schedules[0]);
  assert.equal(sr.state.schedules.length, 1);
  assert.equal(p.calls.length, before);
  sr.loadRequest(me(), 'c1');
  await sr.sendRequest();
  assert.deepEqual(p.calls.slice(before).map(c => c.url), ['/api/send'], 'no ensure');
  assert.equal(bearerSent(p), 'Bearer t');
});

// ---- export and import

test('export carries the schedules; import maps them to the new ids of their request and environment', () => {
  const ui = loadUI();
  ui.applyState(ui.normalizeDoc(copy(DOC)));
  const out = ui.exportData();
  assert.deepEqual(out.schedules, ui.state.schedules);
  out.schedules[0].name = 'Changed';
  assert.equal(ui.state.schedules[0].name, 'Token refresh', 'a copy');

  const got = ui.importData(JSON.parse(JSON.stringify(ui.exportData())));
  assert.deepEqual([got.schedules.length, got.orphans], [1, 0]);
  const [s] = got.schedules;
  const login = got.collections[0].requests.find(r => r.name === 'Login');
  assert.equal(s.requestId, login.id);
  assert.notEqual(s.requestId, 'login');
  assert.equal(s.envId, got.envs.find(e => e.name === 'local').id);
  assert.notEqual(s.envId, 'e1');
  assert.match(s.id, ID);
  assert.notEqual(s.id, 's1');
  assert.deepEqual([s.name, s.everySec, s.expiresPath, s.leadSec, s.extract], ['Token refresh', 240, 'expires_in', 60, DOC.schedules[0].extract]);

  const lost = ui.importData({
    app: 'simple-rest', version: 1, collections: [{ requests: [{ id: 'r1' }] }], envs: [{ id: 'e1' }],
    schedules: [{ id: 'a', requestId: 'r1', envId: 'gone' }, { id: 'b', requestId: 'gone', envId: 'e1' }, { id: 'c', requestId: 'r1', envId: 'e1' }, null],
  });
  assert.deepEqual([lost.schedules.length, lost.orphans], [1, 3], 'without their request or environment');
  const dup = ui.importData({
    app: 'simple-rest', collections: [{ name: 'A', requests: [{ id: 'r', name: 'first' }] }, { name: 'B', requests: [{ id: 'r', name: 'second' }] }],
    envs: [{ id: 'e' }], schedules: [{ requestId: 'r', envId: 'e' }],
  });
  assert.equal(dup.schedules[0].requestId, dup.collections[0].requests[0].id, 'the first request of an id, as the program takes it');
});

test('importFiles adds the schedules and counts the ones left out', async t => {
  const { sr, el } = await setup(t, { ...copy(DOC), schedules: [] });
  const file = (name, data) => ({ name, text: async () => JSON.stringify(data) });
  const exp = { app: 'simple-rest', version: 1, collections: DOC.collections, envs: DOC.envs, schedules: [DOC.schedules[0], { requestId: 'nope', envId: 'e1' }] };
  await sr.importFiles([file('export.json', exp)]);
  assert.equal(sr.state.schedules.length, 1);
  assert.equal(el('#toast').textContent, 'Imported 1 collection(s), 2 environment(s), 1 schedule(s). 1 schedule(s) not imported: their request or environment is not in the file');
  await sr.importFiles([file('only.json', { app: 'simple-rest', version: 1, schedules: [DOC.schedules[0]] })]);
  assert.equal(el('#toast').textContent, '1 schedule(s) not imported: their request or environment is not in the file', 'not "Unrecognized format"');
});
