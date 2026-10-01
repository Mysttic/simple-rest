// Node tests for how ui.html keeps its state: normalizing the data file,
// saving with If-Match, the stale and conflict banners, picking up what other
// tabs saved, and the environment of the tab. fetch, the DOM, the timers and
// the storages are fakes set up for each test.
// Run: node --test "tests/**/*.test.mjs"
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { loadUI, fakeDom, fakeTimers, settle } from './load-ui.mjs';

const copy = o => JSON.parse(JSON.stringify(o));

const DOC = {
  version: 1, future: { keep: true },
  settings: { timeoutMs: 1000, followRedirects: true, verifyTls: true, cookies: true, history: true, theme: 'dark' },
  collections: [{ id: 'c1', name: 'API', open: true, color: 'red', requests: [
    { id: 'r1', name: 'Login', method: 'POST', url: 'http://h/login', note: 'kept' },
  ] }],
  envs: [
    { id: 'e1', name: 'local', owner: 'me', vars: [{ enabled: true, name: 'base', value: 'http://h', secret: false }] },
    { id: 'e2', name: 'prod', vars: [] },
  ],
  activeEnv: 'e1', history: [],
};

// res is the part of a fetch Response the page reads.
function res(status, body = '', headers = {}) {
  const get = n => { const k = Object.keys(headers).find(h => h.toLowerCase() === n.toLowerCase()); return k ? headers[k] : null; };
  return { status, ok: status >= 200 && status < 300, headers: { get }, text: async () => body, json: async () => JSON.parse(body) };
}

// fakeServer answers like the program does: /api/info, and GET and PUT of
// /api/state with ETag, If-None-Match, X-Simple-Rest-Version and If-Match.
// /api/send answers 200 from the URL it was sent to. change() is a save made
// elsewhere; hold(url) keeps answers back (only for url, if given) until
// release().
function fakeServer(doc) {
  const s = { version: '1.2.0', text: JSON.stringify(doc), tag: '"t1"', n: 1, calls: [], down: false, gate: null, gateUrl: null };
  s.change = d => { s.text = JSON.stringify(d); s.tag = `"t${++s.n}"`; };
  s.hold = (url = null) => { let open; s.gate = new Promise(r => { open = r; }); s.gateUrl = url; s.release = () => { s.gate = null; open(); }; };
  s.count = (method, url = '/api/state') => s.calls.filter(c => c.method === method && c.url === url).length;
  s.fetch = async (url, init = {}) => {
    const method = init.method || 'GET', h = init.headers || {};
    s.calls.push({ method, url, headers: h, body: init.body });
    if (s.down) throw new TypeError('Failed to fetch');
    if (s.gate && (!s.gateUrl || s.gateUrl === url)) await s.gate;
    if (url === '/api/info') return res(200, JSON.stringify({ mode: 'server', version: s.version, os: 'linux', arch: 'amd64', dataPath: '/d.json' }));
    if (url === '/api/send') {
      const sent = JSON.parse(init.body).url;
      return res(200, JSON.stringify({ status: 200, statusText: 'OK', headers: [], body: '{}', size: 2, timeMs: 1, finalUrl: sent }));
    }
    if (url !== '/api/state') return res(404, 'not found');
    if (method === 'GET') {
      if (h['If-None-Match'] === s.tag) return res(304, '', { ETag: s.tag });
      return res(200, s.text, { ETag: s.tag });
    }
    if (h['X-Simple-Rest-Version'] !== s.version) return res(409, 'This page comes from another simple-rest version than the running program (' + s.version + '). Reload the page.\n');
    if (!h['If-Match']) return res(428, 'Missing If-Match. Reload the page.\n');
    if (h['If-Match'] !== s.tag) return res(412, '{"error":"changed"}', { ETag: s.tag });
    s.change(JSON.parse(init.body));
    s.text = init.body;
    return res(204, '', { ETag: s.tag });
  };
  return s;
}

const fakeStorage = (init = {}) => {
  const m = new Map(Object.entries(init));
  return { getItem: k => (m.has(k) ? m.get(k) : null), setItem: (k, v) => { m.set(k, String(v)); } };
};

// setup gives a test a fresh page loaded from doc: by the program, or with
// standalone, straight from a file with localStorage.
async function setup(t, doc, { session = {}, standalone = false } = {}) {
  const server = fakeServer(doc);
  const timers = fakeTimers();
  const keys = ['fetch', 'location', 'sessionStorage', 'localStorage', 'document'];
  const saved = keys.map(k => [k, Object.getOwnPropertyDescriptor(globalThis, k)]);
  const local = fakeStorage(standalone ? { 'simple-rest': JSON.stringify(doc) } : {});
  const tabStore = fakeStorage(session);
  for (const [k, v] of Object.entries({
    fetch: server.fetch, sessionStorage: tabStore, localStorage: local,
    location: { protocol: standalone ? 'file:' : 'http:', reloads: 0, reload() { this.reloads++; } },
  })) Object.defineProperty(globalThis, k, { value: v, configurable: true, writable: true });
  const sr = loadUI();
  globalThis.document = fakeDom();
  t.after(() => {
    timers.restore();
    for (const [k, d] of saved) { delete globalThis[k]; if (d) Object.defineProperty(globalThis, k, d); }
  });
  await sr.detectMode();
  await sr.loadState();
  return { sr, server, timers, session: tabStore, local, el: s => globalThis.document.querySelector(s) };
}

test('loading the script starts nothing: no timers, no listeners', () => {
  const timers = fakeTimers();
  try {
    const sr = loadUI();
    assert.equal(typeof sr.persistNow, 'function');
    assert.equal(timers.created, 0);
  } finally { timers.restore(); }
});

test('normalizeDoc keeps what it does not know through serializeState', () => {
  const sr = loadUI();
  sr.applyState(sr.normalizeDoc(copy(DOC)));
  const out = JSON.parse(sr.serializeState());
  assert.deepEqual(out.future, { keep: true }, 'top level');
  assert.equal(out.settings.theme, 'dark', 'settings');
  assert.equal(out.collections[0].color, 'red', 'collection');
  assert.equal(out.collections[0].requests[0].note, 'kept', 'request');
  assert.equal(out.envs[0].owner, 'me', 'environment');
  assert.equal(out.envs[0].vars[0].secret, false, 'row');
  assert.deepEqual(Object.keys(out).sort(), ['activeEnv', 'collections', 'envs', 'future', 'history', 'settings', 'version']);
  assert.equal(sr.state.activeEnv, 'e1');

  sr.applyState(sr.normalizeDoc(null));
  assert.deepEqual(JSON.parse(sr.serializeState()), {
    version: 1, collections: [], history: [], envs: [], activeEnv: '',
    settings: { timeoutMs: 30000, followRedirects: true, verifyTls: true, cookies: true, history: true },
  }, 'no file: defaults, and the keys of the previous file are gone');
});

test('normalizeDoc keeps a key named __proto__ as a plain key', () => {
  const sr = loadUI();
  const text = '{"version":1,"__proto__":{"x":1},"settings":{"__proto__":{"y":2}},'
    + '"collections":[{"id":"c","name":"C","__proto__":{"c":3},"requests":[{"id":"r","__proto__":{"r":4},"options":{"__proto__":{"o":5}}}]}],'
    + '"envs":[{"id":"e","name":"n","__proto__":{"z":6},"vars":[{"name":"v","__proto__":{"w":7}}]}]}';
  const doc = sr.normalizeDoc(JSON.parse(text));
  const req = doc.collections[0].requests[0];
  for (const o of [doc.extra, doc.settings, doc.collections[0], req, req.options, doc.envs[0], doc.envs[0].vars[0]]) {
    assert.equal(Object.getPrototypeOf(o), Object.prototype, 'the prototype stays');
  }
  sr.applyState(doc);
  const out = JSON.parse(sr.serializeState());
  const own = o => Object.getOwnPropertyDescriptor(o, '__proto__')?.value;
  assert.deepEqual(own(out), { x: 1 }, 'top level');
  assert.deepEqual(own(out.settings), { y: 2 }, 'settings');
  assert.deepEqual(own(out.collections[0]), { c: 3 }, 'collection');
  assert.deepEqual(own(out.collections[0].requests[0]), { r: 4 }, 'request');
  assert.deepEqual(own(out.collections[0].requests[0].options), { o: 5 }, 'request options');
  assert.deepEqual(own(out.envs[0]), { z: 6 }, 'environment');
  assert.deepEqual(own(out.envs[0].vars[0]), { w: 7 }, 'row');
});

test('normalizeDoc applies the type rules', () => {
  const sr = loadUI();
  const doc = sr.normalizeDoc({
    settings: { timeoutMs: '2500.9', followRedirects: 0, verifyTls: 'yes', cookies: null },
    collections: [{ id: 'c', requests: [
      {
        id: 'r', method: null, url: 42,
        params: [{ name: 7, value: null }, { enabled: 0, name: true, value: { a: 1 } }, { enabled: 'x', name: 'n', value: false }],
        headers: [{ name: 'H', value: [1, 2] }],
        body: { mode: 'raw', raw: 12.5, rawType: 'constructor', form: [{ name: 'f' }] },
        auth: { type: 'bearer', token: 123, username: null, keyIn: false },
        options: { timeoutMs: -3, followRedirects: '', verifyTls: 1, cookies: 'no' },
      },
      { id: 'r2', method: 'patch', body: { rawType: 'xml' }, options: { timeoutMs: 1.5 } },
      { id: 'r3', body: { rawType: '__proto__' }, options: { timeoutMs: 'abc' } },
      { id: 'r4' },
    ] }],
    envs: [{ id: 'e', vars: [{ name: 'tok', value: null }, { name: 'n', value: 5 }] }],
  });
  assert.deepEqual(doc.settings, { timeoutMs: 2500, followRedirects: false, verifyTls: true, cookies: false, history: true });
  const [r, r2, r3, r4] = doc.collections[0].requests;
  assert.equal(r.method, 'GET');
  assert.equal(r.url, '42');
  assert.deepEqual(r.params, [
    { enabled: true, name: '7', value: '' },
    { enabled: false, name: 'true', value: '{"a":1}' },
    { enabled: true, name: 'n', value: 'false' },
  ]);
  assert.equal(r.headers[0].value, '[1,2]');
  assert.deepEqual(r.body, { mode: 'raw', raw: '12.5', rawType: 'text', form: [{ enabled: true, name: 'f', value: '' }] });
  assert.deepEqual(r.auth, { type: 'bearer', username: '', password: '', token: '123', keyName: '', keyValue: '', keyIn: 'false' });
  assert.deepEqual(r.options, { timeoutMs: 0, followRedirects: false, verifyTls: true, cookies: true });
  assert.deepEqual([r2.method, r2.body.rawType, r2.options.timeoutMs], ['PATCH', 'xml', 1]);
  assert.deepEqual([r3.body.rawType, r3.options.timeoutMs], ['text', 0]);
  assert.equal(r4.body.rawType, 'json', 'no body: the default type');
  assert.deepEqual(r4.options, { timeoutMs: 2500, followRedirects: false, verifyTls: true, cookies: false }, 'defaults come from the settings of the same file');
  assert.deepEqual(doc.envs[0].vars.map(v => v.value), ['', '5']);
});

test('varsOf, subst and buildEffective with the variables it is given', () => {
  const sr = loadUI();
  const env = { vars: [
    { enabled: true, name: 'a', value: '1' }, { enabled: false, name: 'b', value: '2' }, { enabled: true, name: '', value: 'x' },
    { enabled: true, name: 'a', value: '3' }, { enabled: true, name: ' sp', value: 's' }, { enabled: true, name: '__proto__', value: 'p' },
  ] };
  const v = sr.varsOf(env);
  assert.equal(Object.getPrototypeOf(v), null);
  assert.deepEqual(Object.keys(v), ['a', ' sp', '__proto__'], 'enabled rows with a name, taken as typed');
  assert.equal(v.a, '3', 'the later row wins');
  assert.equal(sr.subst('{{a}} {{b}} {{constructor}} {{toString}} {{__proto__}}', v), '3 {{b}} {{constructor}} {{toString}} p');
  assert.equal(sr.subst('{{constructor}} {{hasOwnProperty}}', {}), '{{constructor}} {{hasOwnProperty}}', 'a plain object from the caller');
  assert.deepEqual(Object.keys(sr.varsOf(undefined)), []);

  sr.state.envs = [{ id: 'e1', name: 'x', vars: [{ enabled: true, name: 'a', value: 'active' }] }];
  sr.state.activeEnv = 'e1';
  assert.equal(sr.findEnv('e1'), sr.state.envs[0]);
  const r = Object.assign(sr.newRequest(), { url: '{{a}}/x' });
  assert.equal(sr.buildEffective(r).url, 'active/x', 'default: the active environment');
  assert.equal(sr.buildEffective(r, v).url, '3/x', 'given variables');
  r.options.timeoutMs = 1500.7;
  assert.equal(sr.buildEffective(r).timeoutMs, 1500);
  r.options.timeoutMs = 0.5;
  assert.equal(sr.buildEffective(r).timeoutMs, 30000, 'below 1 ms: the default');
});

test('a reloaded tab takes its environment from sessionStorage', async t => {
  const { sr } = await setup(t, DOC, { session: { 'simple-rest-env': 'e2' } });
  assert.equal(sr.state.activeEnv, 'e2');
});

test('a tab whose environment is gone takes the one from the file', async t => {
  const { sr } = await setup(t, DOC, { session: { 'simple-rest-env': 'gone' } });
  assert.equal(sr.state.activeEnv, 'e1');
});

test('a tab that picked No environment keeps it over a reload', async t => {
  const { sr } = await setup(t, DOC, { session: { 'simple-rest-env': '' } });
  assert.equal(sr.state.activeEnv, '', 'the file names e1');
});

test('the tab keeps its own environment', async t => {
  const { sr, server, session } = await setup(t, DOC);
  assert.equal(sr.state.activeEnv, 'e1');
  sr.setActiveEnv('e2');
  assert.equal(session.getItem('simple-rest-env'), 'e2');
  assert.equal(await sr.flushPersist(), true);
  assert.equal(JSON.parse(server.text).activeEnv, 'e2', 'the file keeps it as the start value for new tabs');

  // Another computer picks another environment; this tab stays with its own.
  server.change(Object.assign(copy(DOC), { activeEnv: 'e1' }));
  assert.equal(await sr.syncIfBehind(), 'applied');
  assert.equal(sr.state.activeEnv, 'e2');
  // Its environment is deleted elsewhere: no environment.
  server.change(Object.assign(copy(DOC), { envs: [DOC.envs[0]] }));
  assert.equal(await sr.syncIfBehind(), 'applied');
  assert.equal(sr.state.activeEnv, '');
});

test('persistNow sends the page version and If-Match and takes the new ETag', async t => {
  const { sr, server, timers } = await setup(t, DOC);
  assert.equal(sr.saveInfo().lastTag, '"t1"');
  sr.state.collections[0].name = 'Renamed';
  sr.persist();
  assert.equal(server.count('PUT'), 0, 'persist waits for the timer');
  await timers.run(400);
  assert.equal(server.count('PUT'), 1);
  const put = server.calls.find(c => c.method === 'PUT');
  assert.equal(put.headers['X-Simple-Rest-Version'], '1.2.0');
  assert.equal(put.headers['If-Match'], '"t1"');
  assert.equal(sr.saveInfo().lastTag, server.tag);
  const saved = JSON.parse(server.text);
  assert.equal(saved.collections[0].name, 'Renamed');
  assert.deepEqual(saved.future, { keep: true }, 'an unknown key survives a save');

  assert.equal(await sr.persistNow(), true);
  assert.equal(server.count('PUT'), 1, 'nothing changed, nothing sent');
});

test('one PUT in flight; a change meanwhile is saved right after it', async t => {
  const { sr, server } = await setup(t, DOC);
  server.hold();
  sr.state.collections[0].name = 'A';
  const first = sr.persistNow();
  await settle();
  sr.state.collections[0].name = 'B';
  const second = sr.persistNow();
  await settle();
  assert.equal(server.count('PUT'), 1);
  server.release();
  assert.equal(await first, true);
  assert.equal(await second, true);
  const puts = server.calls.filter(c => c.method === 'PUT');
  assert.equal(puts.length, 2);
  assert.equal(puts[1].headers['If-Match'], '"t2"', 'the second PUT carries the ETag of the first');
  assert.equal(JSON.parse(server.text).collections[0].name, 'B');
  assert.equal(sr.saveInfo().saveState, 'ok');
});

test('409: the page is stale, shows the banner and stops saving', async t => {
  const { sr, server, el } = await setup(t, DOC);
  server.version = '1.2.1'; // the program was updated under the page
  sr.state.collections[0].name = 'X';
  assert.equal(await sr.persistNow(), false);
  assert.equal(sr.saveInfo().saveState, 'stale');
  assert.equal(el('#staleBanner').hidden, false);
  assert.equal(el('#conflictBanner').hidden, true);
  assert.deepEqual(JSON.parse(server.text), DOC, 'the file is untouched');
  const calls = server.calls.length;
  sr.persist();
  assert.equal(sr.saveInfo().pending, false, 'autosave is off');
  assert.equal(await sr.persistNow(), false);
  assert.equal(await sr.syncIfBehind(), 'off');
  assert.equal(server.calls.length, calls);
});

test('412: the conflict banner shows and saving stops until a reload', async t => {
  const { sr, server, el } = await setup(t, DOC);
  server.change(Object.assign(copy(DOC), { collections: [] })); // another computer saved
  sr.state.collections[0].name = 'Mine';
  assert.equal(await sr.persistNow(), false);
  assert.equal(sr.saveInfo().saveState, 'conflict');
  assert.equal(el('#conflictBanner').hidden, false);
  assert.equal(el('#staleBanner').hidden, true);
  assert.deepEqual(JSON.parse(server.text).collections, [], 'the other save stays');
  const calls = server.calls.length;
  sr.persist();
  assert.equal(sr.saveInfo().pending, false, 'autosave is off');
  assert.equal(await sr.persistNow(), false);
  assert.equal(await sr.syncIfBehind(), 'off', 'no sync over the change that was not saved');
  await sr.installUpdate();
  assert.equal(server.count('POST', '/api/update/install'), 0, 'no update over an unsaved change');
  assert.match(el('#toast').textContent, /^Update not installed/);
  assert.equal(server.calls.length, calls);
  assert.equal(sr.state.collections[0].name, 'Mine', 'the tab keeps working');
});

test('a network failure keeps the change, says so once and retries every 5 s', async t => {
  const { sr, server, timers, el } = await setup(t, DOC);
  server.down = true;
  sr.state.collections[0].name = 'Offline';
  assert.equal(await sr.persistNow(), false);
  assert.equal(sr.saveInfo().saveState, 'unsaved');
  assert.match(el('#toast').textContent, /^Save failed/);
  el('#toast').textContent = '';
  await timers.run(5000);
  assert.equal(server.count('PUT'), 2, 'retried');
  assert.equal(el('#toast').textContent, '', 'one toast for the whole series');
  assert.equal(await sr.syncIfBehind(), 'off', 'no sync over the change that was not saved');
  server.down = false;
  await timers.run(5000);
  assert.equal(sr.saveInfo().saveState, 'ok');
  assert.equal(JSON.parse(server.text).collections[0].name, 'Offline');
  assert.equal(timers.pending, 0, 'no retry left');
});

test('flushPersist saves what waits for the timer', async t => {
  const { sr, server } = await setup(t, DOC);
  assert.equal(await sr.flushPersist(), true, 'nothing waits');
  sr.state.collections[0].name = 'F';
  sr.persist();
  assert.equal(sr.saveInfo().pending, true);
  assert.equal(await sr.flushPersist(), true);
  assert.equal(server.count('PUT'), 1);
  assert.equal(sr.saveInfo().pending, false);
});

test('syncIfBehind: 304 changes nothing', async t => {
  const { sr, server } = await setup(t, DOC);
  const before = sr.serializeState();
  assert.equal(await sr.syncIfBehind(), 'same');
  const get = server.calls.at(-1);
  assert.deepEqual([get.method, get.headers['If-None-Match']], ['GET', '"t1"']);
  assert.equal(sr.serializeState(), before);
});

test('syncIfBehind: 200 takes the new document as it is', async t => {
  const { sr, server, timers } = await setup(t, DOC);
  const theirs = copy(DOC);
  theirs.collections[0].name = 'Theirs';
  theirs.future = 'v2';
  delete theirs.envs[0].owner;
  server.change(theirs);
  assert.equal(await sr.syncIfBehind(), 'applied');
  assert.equal(sr.state.collections[0].name, 'Theirs');
  assert.equal(sr.saveInfo().lastTag, server.tag);
  const out = JSON.parse(sr.serializeState());
  assert.equal(out.future, 'v2');
  assert.equal(out.envs[0].owner, undefined);
  await timers.run(1000);
  assert.equal(server.count('PUT'), 0, 'taking a sync does not save');
});

test('syncIfBehind waits for a pending save', async t => {
  const { sr, server, timers } = await setup(t, DOC);
  sr.state.collections[0].name = 'Mine';
  sr.persist();
  assert.equal(await sr.syncIfBehind(), 'deferred');
  assert.equal(server.count('GET'), 1, 'only the load');
  await timers.run(400);
  // The save went out and the sync came right after it: nothing new.
  const [put, get] = server.calls.slice(-2);
  assert.equal(put.method, 'PUT');
  assert.deepEqual([get.method, get.headers['If-None-Match']], ['GET', server.tag]);

  // A change made while the answer of a sync is on its way wins over it.
  server.change(Object.assign(copy(DOC), { collections: [] }));
  server.hold();
  const sync = sr.syncIfBehind();
  await settle();
  sr.state.collections[0].name = 'Mine again';
  sr.persist();
  server.release();
  assert.equal(await sync, 'deferred');
  assert.equal(sr.state.collections[0].name, 'Mine again');
  // Its save then meets the other save: a conflict, and nothing overwritten.
  await timers.run(400);
  assert.equal(sr.saveInfo().saveState, 'conflict');
  assert.deepEqual(JSON.parse(server.text).collections, []);
});

test('a typed value in a modal field holds the sync until the field loses focus', async t => {
  const { sr, server } = await setup(t, DOC);
  const theirs = copy(DOC);
  theirs.collections[0].name = 'Theirs';
  server.change(theirs);
  const field = document.createElement('input');
  field.dataset.held = '1';
  document.activeElement = field;
  assert.equal(await sr.syncIfBehind(), 'applied', 'outside an open modal the field holds nothing');

  theirs.collections[0].name = 'Theirs again';
  server.change(theirs);
  field.parents['.modal-bg.open'] = document.createElement('div');
  const gets = server.count('GET');
  assert.equal(await sr.syncIfBehind(), 'deferred');
  assert.equal(await sr.syncIfBehind(), 'deferred');
  assert.equal(server.count('GET'), gets, 'the file is not asked for');
  assert.equal(field.listeners.focusout.length, 1, 'one wait for the field');
  assert.equal(sr.state.collections[0].name, 'Theirs');

  document.activeElement = null;
  field.dispatch('focusout');
  await settle();
  assert.equal(server.count('GET'), gets + 1);
  assert.equal(sr.state.collections[0].name, 'Theirs again');
});

test('a sync puts the cursor back into the table cell the redraw replaced', async t => {
  const { sr, server, el } = await setup(t, DOC);
  el('#envModal').classList.add('open');
  const table = el('#envVarsTable');
  table.id = 'envVarsTable';
  const td = document.createElement('td');
  Object.assign(td, { cellIndex: 2, parentNode: { rowIndex: 1 } });
  const field = document.createElement('input');
  Object.assign(field, { selectionStart: 2, selectionEnd: 4, parents: { td, table } });
  // Redrawing the table (kvTable) takes the old field out of the page.
  Object.defineProperty(table, 'innerHTML', { set() { field.isConnected = false; }, get() { return ''; } });
  const fresh = document.createElement('input');
  fresh.focus = () => { document.activeElement = fresh; };
  fresh.setSelectionRange = (a, b) => { fresh.selection = [a, b]; };
  table.rows = [null, { cells: [null, null, { querySelector: s => (s === 'input' ? fresh : null) }] }];
  document.activeElement = field;

  const theirs = copy(DOC);
  theirs.envs[0].vars[0].value = 'http://other';
  server.change(theirs);
  assert.equal(await sr.syncIfBehind(), 'applied');
  assert.equal(field.isConnected, false, 'the environment table was redrawn');
  assert.equal(document.activeElement, fresh);
  assert.deepEqual(fresh.selection, [2, 4]);
});

test('a sync loads the open request into a clean editor and keeps unsaved edits', async t => {
  const { sr, server } = await setup(t, DOC);
  sr.loadRequest(sr.state.collections[0].requests[0], 'c1');
  const edited = url => { const d = copy(DOC); d.collections[0].requests[0].url = url; return d; };

  server.change(edited('http://h/v2'));
  await sr.syncIfBehind();
  assert.equal(sr.editorInfo().cur.url, 'http://h/v2', 'a clean editor loads the new version');

  sr.editorInfo().cur.url = 'http://h/mine';
  server.change(edited('http://h/v3'));
  await sr.syncIfBehind();
  const ed = sr.editorInfo();
  assert.equal(ed.cur.url, 'http://h/mine', 'unsaved edits stay');
  assert.equal(JSON.parse(ed.saved).url, 'http://h/v3', 'and are compared with the new version');
  assert.equal(ed.collId, 'c1');

  const gone = copy(DOC);
  gone.collections[0].requests = [];
  server.change(gone);
  await sr.syncIfBehind();
  assert.equal(sr.editorInfo().collId, null, 'deleted elsewhere: the editor holds an unsaved request');
  assert.equal(sr.editorInfo().cur.url, 'http://h/mine');
});

test('Send records the request it sent, also when a sync reloads the editor meanwhile', async t => {
  const { sr, server, el } = await setup(t, DOC);
  sr.loadRequest(sr.state.collections[0].requests[0], 'c1');
  server.hold('/api/send');
  const send = sr.sendRequest();
  await settle();
  const theirs = copy(DOC);
  theirs.collections[0].requests[0].url = 'http://h/v2';
  server.change(theirs);
  assert.equal(await sr.syncIfBehind(), 'applied');
  assert.equal(sr.editorInfo().cur.url, 'http://h/v2', 'the clean editor took the new version');
  server.release();
  await send;
  assert.equal(JSON.parse(server.calls.find(c => c.url === '/api/send').body).url, 'http://h/login');
  assert.equal(sr.state.history[0].req.url, 'http://h/login', 'history has what was sent');
  assert.equal(el('#respUrl').textContent, '', 'no redirect line: the answer came from the URL that was sent');
});

test('an import of several files changes the state in one go, so a sync cannot drop a file', async t => {
  const { sr, server, timers, el } = await setup(t, DOC);
  const theirs = copy(DOC);
  theirs.collections.push({ id: 'c2', name: 'Theirs', open: true, requests: [] });
  server.change(theirs);
  // Closing the file chooser focuses the window: a sync starts while the files are read.
  server.hold();
  const sync = sr.syncIfBehind();
  let second;
  const imported = sr.importFiles([
    { name: 'one.json', text: async () => JSON.stringify({ collections: [{ name: 'One', requests: [] }] }) },
    { name: 'two.json', text: () => new Promise(r => { second = () => r(JSON.stringify({ collections: [{ name: 'Two', requests: [] }] })); }) },
  ]);
  await settle();
  server.release();
  assert.equal(await sync, 'applied');
  second();
  await imported;
  await timers.run(400);
  const names = ['API', 'Theirs', 'One', 'Two'];
  assert.deepEqual(sr.state.collections.map(c => c.name), names);
  assert.deepEqual(JSON.parse(server.text).collections.map(c => c.name), names);
  assert.equal(sr.saveInfo().saveState, 'ok');
  assert.equal(el('#toast').textContent, 'Imported 2 collection(s), 0 environment(s)');
});

test('without the program: localStorage, and what another tab saved there', async t => {
  const { sr, server, local } = await setup(t, DOC, { standalone: true });
  assert.equal(sr.state.collections[0].name, 'API');
  sr.state.collections[0].name = 'Local';
  assert.equal(await sr.persistNow(), true);
  assert.equal(JSON.parse(local.getItem('simple-rest')).collections[0].name, 'Local');
  assert.equal(await sr.syncIfBehind(), 'same');
  local.setItem('simple-rest', JSON.stringify(Object.assign(copy(DOC), { collections: [] })));
  assert.equal(await sr.syncIfBehind(), 'applied');
  assert.deepEqual(sr.state.collections, []);
  assert.equal(server.calls.length, 0, 'the program is never asked');
});

test('without the program: a save over what another tab saved is a conflict', async t => {
  const { sr: a, local, timers, el } = await setup(t, DOC, { standalone: true });
  const b = loadUI();
  await b.detectMode();
  await b.loadState();
  b.state.collections[0].name = 'Changed in B';
  assert.equal(await b.persistNow(), true);

  a.state.envs[0].name = 'Changed in A';
  a.persist();
  assert.equal(await a.syncIfBehind(), 'deferred', 'the storage event finds a save pending');
  await timers.run(400);
  assert.equal(a.saveInfo().saveState, 'conflict');
  assert.equal(el('#conflictBanner').hidden, false);
  const saved = JSON.parse(local.getItem('simple-rest'));
  assert.equal(saved.collections[0].name, 'Changed in B', 'the save of the other tab stays');
  assert.equal(saved.envs[0].name, 'local');
  assert.equal(await b.syncIfBehind(), 'same');
  assert.equal(b.state.collections[0].name, 'Changed in B');
});
