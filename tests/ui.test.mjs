// Node tests for the pure parts of ui.html. The script is pulled out of the
// page and evaluated here without a DOM (tests/load-ui.mjs; the page only
// starts itself when its markup is present), so only the functions that build
// requests, parse URLs and import files are exercised.
// Run: node --test "tests/**/*.test.mjs"
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { sr, loadUI, fakeDom, fakeTimers } from './load-ui.mjs';

const request = (over = {}) => Object.assign(sr.newRequest(), over);
const header = (h, name) => h.filter(x => x.name.toLowerCase() === name.toLowerCase()).map(x => x.value);

test('the page exposes its pure functions', () => {
  for (const f of ['splitUrl', 'parseQuery', 'encName', 'encValue', 'subst', 'buildEffective', 'highlightJSON', 'normalizeRequest', 'importPostmanCollection', 'toCurl']) {
    assert.equal(typeof sr[f], 'function', f);
  }
});

test('splitUrl and parseQuery', () => {
  assert.deepEqual(sr.splitUrl('http://x/a?b=1&c'), ['http://x/a', 'b=1&c']);
  assert.deepEqual(sr.splitUrl('http://x/a'), ['http://x/a', '']);
  assert.deepEqual(sr.parseQuery('a=1&b=two%20words&flag&&c=x=y&d=%E2%9C%93'), [
    { enabled: true, name: 'a', value: '1' },
    { enabled: true, name: 'b', value: 'two words' },
    { enabled: true, name: 'flag', value: '' },
    { enabled: true, name: 'c', value: 'x=y' },
    { enabled: true, name: 'd', value: '✓' },
  ]);
  assert.deepEqual(sr.parseQuery('bad=%E0%A4%A'), [{ enabled: true, name: 'bad', value: '%E0%A4%A' }], 'undecodable stays raw');
});

test('encoding keeps variables and plus signs, escapes separators', () => {
  assert.equal(sr.encValue('two words & more #1 {{var}} 1+1'), 'two%20words%20%26%20more%20%231%20{{var}}%201+1');
  assert.equal(sr.encName('a=b&c'), 'a%3Db%26c');
  const value = 'q=a b&c';
  assert.deepEqual(sr.parseQuery('k=' + sr.encValue(value))[0].value, value, 'round trip');
});

test('subst replaces known variables and leaves the rest', () => {
  const vars = { base: 'http://h', 'api.key': 'k1' };
  assert.equal(sr.subst('{{base}}/x?k={{ api.key }}&m={{missing}}', vars), 'http://h/x?k=k1&m={{missing}}');
  assert.equal(sr.subst(null, vars), '');
});

test('buildEffective: url from URL field, headers, default content type for JSON', () => {
  const r = request({ method: 'POST', url: 'http://h/api?x=1', headers: [{ enabled: true, name: 'X-A', value: '1' }, { enabled: false, name: 'X-Off', value: '1' }, { enabled: true, name: '  ', value: 'blank' }], body: { mode: 'raw', raw: '{"a":1}', rawType: 'json', form: [] } });
  const e = sr.buildEffective(r);
  assert.equal(e.method, 'POST');
  assert.equal(e.url, 'http://h/api?x=1');
  assert.equal(e.body, '{"a":1}');
  assert.deepEqual(header(e.headers, 'X-A'), ['1']);
  assert.deepEqual(header(e.headers, 'X-Off'), []);
  assert.deepEqual(header(e.headers, 'Content-Type'), ['application/json']);
  assert.equal(e.headers.some(h => h.name.trim() === ''), false);
  assert.deepEqual([e.timeoutMs, e.followRedirects, e.verifyTls, e.cookies], [30000, true, true, true]);
});

test('buildEffective: user content type wins, form body is urlencoded', () => {
  const raw = request({ body: { mode: 'raw', raw: '<a/>', rawType: 'json', form: [] }, headers: [{ enabled: true, name: 'content-type', value: 'text/xml' }] });
  assert.deepEqual(header(sr.buildEffective(raw).headers, 'content-type'), ['text/xml']);
  const form = request({ method: 'POST', body: { mode: 'form', raw: '', rawType: 'json', form: [{ enabled: true, name: 'a b', value: 'x&y' }, { enabled: false, name: 'off', value: '1' }, { enabled: true, name: '', value: 'noname' }] } });
  const e = sr.buildEffective(form);
  assert.equal(e.body, 'a%20b=x%26y');
  assert.deepEqual(header(e.headers, 'Content-Type'), ['application/x-www-form-urlencoded']);
  assert.equal(sr.buildEffective(request({ body: { mode: 'none', raw: 'ignored', rawType: 'json', form: [] } })).body, '');
});

test('buildEffective: auth types', () => {
  const basic = request({ auth: { type: 'basic', username: 'ala', password: 'kot:ź', token: '', keyName: '', keyValue: '', keyIn: 'header' }, headers: [{ enabled: true, name: 'Authorization', value: 'stale' }] });
  assert.deepEqual(header(sr.buildEffective(basic).headers, 'Authorization'), ['Basic ' + Buffer.from('ala:kot:ź', 'utf8').toString('base64')], 'auth tab replaces a typed Authorization header');
  const bearer = request({ auth: { type: 'bearer', username: '', password: '', token: 't0k', keyName: '', keyValue: '', keyIn: 'header' } });
  assert.deepEqual(header(sr.buildEffective(bearer).headers, 'Authorization'), ['Bearer t0k']);
  const keyHeader = request({ auth: { type: 'apikey', username: '', password: '', token: '', keyName: 'X-API-Key', keyValue: 'abc', keyIn: 'header' } });
  assert.deepEqual(header(sr.buildEffective(keyHeader).headers, 'X-API-Key'), ['abc']);
  const keyQuery = request({ url: 'http://h/p?x=1', auth: { type: 'apikey', username: '', password: '', token: '', keyName: 'key', keyValue: 'a b', keyIn: 'query' } });
  assert.equal(sr.buildEffective(keyQuery).url, 'http://h/p?x=1&key=a%20b');
  assert.equal(sr.buildEffective(request({ url: 'http://h/p', auth: keyQuery.auth })).url, 'http://h/p?key=a%20b');
});

test('buildEffective substitutes variables from the active environment everywhere', () => {
  sr.state.envs = [{ id: 'e1', name: 'local', vars: [{ enabled: true, name: 'base', value: 'http://h' }, { enabled: true, name: 'tok', value: 'T' }, { enabled: false, name: 'off', value: 'no' }] }];
  sr.state.activeEnv = 'e1';
  const r = request({ url: '{{base}}/u?o={{off}}', headers: [{ enabled: true, name: 'X-{{tok}}', value: 'v-{{tok}}' }], body: { mode: 'raw', raw: '{"t":"{{tok}}"}', rawType: 'json', form: [] }, auth: { type: 'bearer', username: '', password: '', token: '{{tok}}', keyName: '', keyValue: '', keyIn: 'header' } });
  const e = sr.buildEffective(r);
  assert.equal(e.url, 'http://h/u?o={{off}}');
  assert.deepEqual(header(e.headers, 'X-T'), ['v-T']);
  assert.equal(e.body, '{"t":"T"}');
  assert.deepEqual(header(e.headers, 'Authorization'), ['Bearer T']);
  sr.state.activeEnv = '';
  assert.equal(sr.buildEffective(r).url, '{{base}}/u?o={{off}}', 'no environment, no substitution');
});

test('toCurl quotes safely and picks the right flags', () => {
  const r = request({ method: 'POST', url: "http://h/it's", headers: [{ enabled: true, name: 'X-A', value: '1' }], body: { mode: 'raw', raw: '{"a":"b"}', rawType: 'json', form: [] }, options: { timeoutMs: 1, followRedirects: false, verifyTls: false, cookies: true } });
  const c = sr.toCurl(r);
  assert.match(c, /^curl \\\n {2}-X POST \\\n {2}-k \\\n {2}'http:\/\/h\/it'\\''s' \\\n/);
  assert.match(c, /-H 'X-A: 1'/);
  assert.match(c, /-H 'Content-Type: application\/json'/);
  assert.match(c, /--data-raw '\{"a":"b"\}'$/);
  assert.equal(sr.toCurl(request({ method: 'HEAD', url: 'http://h' })), "curl \\\n  -I \\\n  -L \\\n  'http://h'");
});

test('highlightJSON escapes HTML and classifies tokens', () => {
  const out = sr.highlightJSON('{"k": "<b>", "n": -1.5e3, "t": true, "z": null}');
  assert.ok(!out.includes('<b>'), 'raw HTML must be escaped');
  assert.ok(out.includes('<span class="k">&quot;k&quot;:</span>'), out);
  assert.ok(out.includes('<span class="s">&quot;&lt;b&gt;&quot;</span>'), out);
  assert.ok(out.includes('<span class="n">-1.5e3</span>'));
  assert.ok(out.includes('<span class="b">true</span>'));
  assert.ok(out.includes('<span class="x">null</span>'));
});

test('normalizeRequest fills in what old or foreign files lack', () => {
  const r = sr.normalizeRequest({ method: 'delete', url: 'http://h', params: [{ name: 'a' }], options: { timeoutMs: 5 } });
  assert.equal(r.method, 'DELETE');
  assert.deepEqual(r.params, [{ enabled: true, name: 'a', value: '' }]);
  assert.deepEqual(r.body, { mode: 'none', raw: '', rawType: 'json', form: [] });
  assert.equal(r.auth.type, 'none');
  assert.deepEqual(r.options, { timeoutMs: 5, followRedirects: true, verifyTls: true, cookies: true });
  assert.ok(r.id);
});

test('Postman collection import maps folders, urls, headers, bodies and auth', () => {
  const coll = {
    info: { name: 'Shop', schema: 'https://schema.getpostman.com/json/collection/v2.1.0/collection.json' },
    item: [
      { name: 'Orders', item: [
        { name: 'List', request: { method: 'GET', url: { raw: '{{base}}/orders?page=1&debug=1', query: [{ key: 'page', value: '1' }, { key: 'debug', value: '1', disabled: true }] }, header: [{ key: 'Accept', value: 'application/json' }, { key: 'X-Off', value: '0', disabled: true }] } },
        { name: 'Create', request: { method: 'POST', url: '{{base}}/orders', body: { mode: 'raw', raw: '{"sku":"a"}', options: { raw: { language: 'json' } } }, auth: { type: 'bearer', bearer: [{ key: 'token', value: '{{tok}}' }] } } },
      ] },
      { name: 'Login', request: { method: 'POST', url: { raw: 'http://h/login' }, body: { mode: 'urlencoded', urlencoded: [{ key: 'u', value: 'a' }, { key: 'p', value: 'b', disabled: true }] }, auth: { type: 'basic', basic: [{ key: 'username', value: 'ala' }, { key: 'password', value: 'kot' }] } } },
      { name: 'Key', request: { method: 'GET', url: 'http://h/k', auth: { type: 'apikey', apikey: [{ key: 'key', value: 'X-Key' }, { key: 'value', value: '42' }, { key: 'in', value: 'query' }] } } },
      { name: 'Plain', request: 'http://h/plain' },
    ],
  };
  const c = sr.importPostmanCollection(coll);
  assert.equal(c.name, 'Shop');
  assert.deepEqual(c.requests.map(r => r.name), ['Orders / List', 'Orders / Create', 'Login', 'Key', 'Plain']);
  const [list, create, login, key, plain] = c.requests;
  assert.equal(list.url, '{{base}}/orders?page=1&debug=1');
  assert.deepEqual(list.params, [{ enabled: true, name: 'page', value: '1' }, { enabled: true, name: 'debug', value: '1' }, { enabled: false, name: 'debug', value: '1' }]);
  assert.deepEqual(list.headers, [{ enabled: true, name: 'Accept', value: 'application/json' }, { enabled: false, name: 'X-Off', value: '0' }]);
  assert.deepEqual(create.body, { mode: 'raw', raw: '{"sku":"a"}', rawType: 'json', form: [] });
  assert.equal(create.auth.type, 'bearer');
  assert.equal(create.auth.token, '{{tok}}');
  assert.equal(login.body.mode, 'form');
  assert.deepEqual(login.body.form, [{ enabled: true, name: 'u', value: 'a' }, { enabled: false, name: 'p', value: 'b' }]);
  assert.deepEqual([login.auth.type, login.auth.username, login.auth.password], ['basic', 'ala', 'kot']);
  assert.deepEqual([key.auth.type, key.auth.keyName, key.auth.keyValue, key.auth.keyIn], ['apikey', 'X-Key', '42', 'query']);
  assert.deepEqual([plain.method, plain.url], ['GET', 'http://h/plain']);
});

const BEARER = { type: 'bearer', bearer: [{ key: 'token', value: '{{tok}}' }] };
const BASIC = { type: 'basic', basic: [{ key: 'username', value: 'ala' }, { key: 'password', value: 'kot' }] };
const NO_AUTH = { type: 'none', username: '', password: '', token: '', keyName: '', keyValue: '', keyIn: 'header' };
const preRequest = exec => [{ listen: 'prerequest', script: { type: 'text/javascript', exec } }];
const authOf = c => Object.fromEntries(c.requests.map(r => [r.name, r.auth]));

test('Postman import: a request without auth takes it from the nearest folder, then from the collection', () => {
  const c = sr.importPostmanCollection({
    info: { name: 'Shop' }, auth: BEARER,
    item: [
      { name: 'Top', request: { method: 'GET', url: 'http://h/top' } },
      { name: 'Inherit', request: { method: 'GET', url: 'http://h/i', auth: { type: 'inherit' } } },
      { name: 'Plain', request: 'http://h/plain' },
      { name: 'Admin', auth: BASIC, item: [
        { name: 'Users', request: { method: 'GET', url: 'http://h/u' } },
        { name: 'Deep', item: [{ name: 'Leaf', request: { method: 'GET', url: 'http://h/l', auth: { type: 'inherit' } } }] },
        { name: 'Own', request: { method: 'GET', url: 'http://h/o', auth: { type: 'apikey', apikey: [{ key: 'key', value: 'K' }, { key: 'value', value: 'V' }] } } },
      ] },
      { name: 'Passing', auth: { type: 'inherit' }, item: [{ name: 'Through', request: { method: 'GET', url: 'http://h/t', auth: null } }] },
    ],
  });
  const a = authOf(c);
  assert.deepEqual(Object.keys(a), ['Top', 'Inherit', 'Plain', 'Admin / Users', 'Admin / Deep / Leaf', 'Admin / Own', 'Passing / Through']);
  for (const n of ['Top', 'Inherit', 'Plain', 'Passing / Through']) assert.deepEqual([a[n].type, a[n].token], ['bearer', '{{tok}}'], n + ': from the collection');
  for (const n of ['Admin / Users', 'Admin / Deep / Leaf']) assert.deepEqual([a[n].type, a[n].username, a[n].password], ['basic', 'ala', 'kot'], n + ': from the folder');
  assert.deepEqual([a['Admin / Own'].type, a['Admin / Own'].keyName, a['Admin / Own'].keyValue], ['apikey', 'K', 'V'], 'its own auth wins');
  assert.equal(sr.importPostmanCollection({ info: { name: 'Bare' }, item: [{ name: 'X', request: { url: 'http://h' } }] }).requests[0].auth.type, 'none', 'nothing to inherit');
});

test('Postman import: noauth means no auth and stops the inheritance', () => {
  const a = authOf(sr.importPostmanCollection({
    info: { name: 'Open' }, auth: BEARER,
    item: [
      { name: 'Health', request: { url: 'http://h/health', auth: { type: 'noauth' } } },
      { name: 'Public', auth: { type: 'noauth' }, item: [
        { name: 'List', request: { url: 'http://h/p' } },
        { name: 'Inner', item: [{ name: 'Deep', request: { url: 'http://h/d', auth: { type: 'inherit' } } }] },
        { name: 'Mine', request: { url: 'http://h/m', auth: BASIC } },
      ] },
    ],
  }));
  for (const n of ['Health', 'Public / List', 'Public / Inner / Deep']) assert.deepEqual(a[n], NO_AUTH, n);
  assert.equal(a['Public / Mine'].type, 'basic', 'a request below noauth may still set its own');
});

test('Postman import: collection variables become an environment, and an environment file one too', () => {
  const ui = loadUI();
  const got = ui.importData({
    info: { name: 'Shop' }, item: [],
    variable: [{ key: 'base', value: 'http://h' }, { key: 'n', value: 5, type: 'number' }, { key: 'off', value: 'x', disabled: true }],
  });
  assert.deepEqual(got.collections.map(c => c.name), ['Shop']);
  assert.deepEqual(got.envs.map(e => e.name), ['Shop variables']);
  assert.deepEqual(got.envs[0].vars, [
    { enabled: true, name: 'base', value: 'http://h' },
    { enabled: true, name: 'n', value: '5' },
    { enabled: false, name: 'off', value: 'x' },
  ]);
  assert.ok(got.envs[0].id);
  assert.deepEqual(ui.importData({ info: { name: 'Shop' }, item: [], variable: [] }).envs, [], 'no variables, no environment');
  assert.deepEqual(ui.importData({ info: {}, item: [], variable: [{ key: 'a', value: '1' }] }).envs.map(e => e.name), ['Postman import variables']);

  const env = ui.importData({ id: 'x', name: 'Prod', values: [{ key: 'tok', value: 't', enabled: false }, { key: 'base', value: null }], _postman_variable_scope: 'environment' });
  assert.deepEqual(env.collections, []);
  assert.deepEqual(env.envs.map(e => [e.name, e.vars]), [['Prod', [{ enabled: false, name: 'tok', value: 't' }, { enabled: true, name: 'base', value: '' }]]]);
  assert.deepEqual(env.skipped, []);
});

test('Postman import counts the requests with OAuth 2.0 or a pre-request script and imports them without', () => {
  const ui = loadUI();
  const got = ui.importData({
    info: { name: 'S' },
    item: [
      { name: 'OAuth', request: { url: 'http://h/1', auth: { type: 'oauth2', oauth2: [{ key: 'accessToken', value: 'x' }] } } },
      { name: 'Script', event: preRequest(['pm.environment.set("t", 1);']), request: { url: 'http://h/2' } },
      { name: 'Blank script', event: preRequest(['', '  ']), request: { url: 'http://h/3' } },
      { name: 'Off script', event: [{ listen: 'prerequest', disabled: true, script: { exec: ['x()'] } }], request: { url: 'http://h/4' } },
      { name: 'Tests only', event: [{ listen: 'test', script: { exec: ['pm.test("ok")'] } }], request: { url: 'http://h/5' } },
      { name: 'F', event: preRequest('console.log(1)'), item: [
        { name: 'In folder', request: { url: 'http://h/6' } },
        { name: 'Both', event: preRequest(['y()']), request: { url: 'http://h/7', auth: { type: 'oauth2' } } },
      ] },
      { name: 'G', auth: { type: 'oauth2' }, item: [
        { name: 'Inherits OAuth', request: { url: 'http://h/8' } },
        { name: 'Own basic', request: { url: 'http://h/9', auth: BASIC } },
      ] },
    ],
  });
  assert.equal(got.unsupported, 5, 'OAuth, Script, F / In folder, F / Both, G / Inherits OAuth');
  const reqs = got.collections[0].requests;
  assert.equal(reqs.length, 9, 'every request is still imported');
  for (const n of ['OAuth', 'F / Both', 'G / Inherits OAuth']) assert.deepEqual(reqs.find(r => r.name === n).auth, NO_AUTH, n);
  assert.equal(reqs.find(r => r.name === 'G / Own basic').auth.type, 'basic');

  const all = ui.importData({ info: { name: 'All' }, event: preRequest(['setup()']), item: [{ name: 'A', request: { url: 'http://h/a' } }, { name: 'B', item: [{ name: 'C', request: { url: 'http://h/c' } }] }] });
  assert.equal(all.unsupported, 2, 'a script of the collection runs before every request');
  assert.equal(ui.importData({ info: { name: 'None' }, item: [{ name: 'A', request: { url: 'http://h/a', auth: BEARER } }] }).unsupported, 0);
});

test('importData reads our own format, skips what only a newer version knows and leaves the state alone', () => {
  const ui = loadUI();
  ui.state.collections.push({ id: 'mine', name: 'Mine', open: true, requests: [] });
  const before = JSON.stringify(ui.state);
  const got = ui.importData({
    app: 'simple-rest', version: 1, exportedAt: '2026-10-01T00:00:00.000Z',
    collections: [{ id: 'c1', name: 'API', open: false, requests: [{ id: 'r1', name: 'One', method: 'get', url: 'http://h', note: 'kept' }] }, { name: 'Empty' }, null],
    envs: [{ id: 'e1', name: 'local', vars: [{ name: 'base', value: 1 }] }],
    tcp: [{ id: 't1', host: 'h', port: 1 }], future: { x: 1 },
  });
  assert.deepEqual(Object.keys(got).sort(), ['collections', 'envs', 'schedules', 'skipped', 'tcp', 'unsupported']);
  assert.deepEqual(got.skipped, ['tcp', 'future']);
  assert.deepEqual([got.schedules, got.tcp, got.unsupported], [[], [], 0]);
  assert.deepEqual(got.collections.map(c => [c.name, c.open, c.requests.length]), [['API', true, 1], ['Empty', true, 0], ['Imported', true, 0]]);
  const r = got.collections[0].requests[0];
  assert.deepEqual([r.name, r.method, r.url, r.note], ['One', 'GET', 'http://h', 'kept']);
  assert.notEqual(got.collections[0].id, 'c1', 'new ids');
  assert.notEqual(r.id, 'r1');
  assert.notEqual(got.envs[0].id, 'e1');
  assert.deepEqual(got.envs.map(e => [e.name, e.vars]), [['local', [{ enabled: true, name: 'base', value: '1' }]]]);
  assert.equal(JSON.stringify(ui.state), before, 'the state is left alone');

  assert.deepEqual(ui.importData({ app: 'simple-rest', version: 1, tcp: [] }).skipped, ['tcp'], 'an export with nothing this version knows');
  const dataFile = ui.importData({ version: 1, collections: [{ name: 'A', requests: [] }], history: [{ id: 'h' }], envs: [], activeEnv: '', settings: { timeoutMs: 1 } });
  assert.deepEqual([dataFile.collections.length, dataFile.skipped], [1, []], 'the keys of the data file are known');
  // There is no document here: a toast from importData would throw.
  for (const d of [null, 'text', 42, [1, 2], { foo: 1 }, { info: { name: 'x' } }]) {
    const none = ui.importData(d);
    assert.deepEqual([none.collections, none.envs, none.skipped, none.unsupported], [[], [], [], 0], JSON.stringify(d));
  }
});

test('exportData is a copy of the collections and environments that imports back', () => {
  const ui = loadUI();
  ui.state.collections.push({ id: 'c1', name: 'API', open: true, requests: [ui.normalizeRequest({ id: 'r1', name: 'One', url: 'http://h' })] });
  ui.state.envs.push({ id: 'e1', name: 'local', vars: [{ enabled: true, name: 'base', value: 'http://h' }] });
  const out = ui.exportData();
  assert.deepEqual(Object.keys(out), ['app', 'version', 'exportedAt', 'collections', 'envs']);
  assert.deepEqual([out.app, out.version], ['simple-rest', 1]);
  assert.ok(!Number.isNaN(Date.parse(out.exportedAt)));
  assert.deepEqual(out.collections, ui.state.collections);
  assert.deepEqual(out.envs, ui.state.envs);
  out.collections[0].name = 'Changed';
  out.envs[0].vars.length = 0;
  assert.equal(ui.state.collections[0].name, 'API', 'a copy');
  assert.equal(ui.state.envs[0].vars.length, 1);

  const back = ui.importData(JSON.parse(JSON.stringify(ui.exportData())));
  assert.deepEqual(back.skipped, []);
  assert.deepEqual(back.collections.map(c => [c.name, c.requests.map(r => [r.name, r.url])]), [['API', [['One', 'http://h']]]]);
  assert.deepEqual(back.envs.map(e => [e.name, e.vars]), [['local', ui.state.envs[0].vars]]);
});

test('importFiles adds what importData found and sums it up in one toast', async t => {
  const timers = fakeTimers();
  const ui = loadUI();
  globalThis.document = fakeDom();
  t.after(() => { timers.restore(); delete globalThis.document; });
  const file = (name, data) => ({ name, text: async () => (typeof data === 'string' ? data : JSON.stringify(data)) });
  const shown = () => globalThis.document.querySelector('#toast').textContent;

  await ui.importFiles([
    file('newer.json', { app: 'simple-rest', version: 1, collections: [{ name: 'One', requests: [] }], envs: [], tcp: [{}], future: 1 }),
    file('shop.postman_collection.json', { info: { name: 'Shop' }, item: [{ name: 'Token', request: { url: 'http://h', auth: { type: 'oauth2' } } }, { name: 'Plain', request: 'http://h/p' }], variable: [{ key: 'base', value: 'http://h' }] }),
    file('newest.json', { app: 'simple-rest', version: 1, tcp: [] }),
  ]);
  assert.deepEqual(ui.state.collections.map(c => c.name), ['One', 'Shop']);
  assert.deepEqual(ui.state.envs.map(e => e.name), ['Shop variables']);
  assert.equal(shown(), 'Imported 2 collection(s), 1 environment(s). 1 request(s) use OAuth 2.0 or pre-request scripts, which are not imported. Skipped: tcp, future (needs a newer simple-rest)');

  await ui.importFiles([file('newest.json', { app: 'simple-rest', version: 1, tcp: [] })]);
  assert.equal(shown(), 'Skipped: tcp (needs a newer simple-rest)', 'not "Unrecognized format"');
  await ui.importFiles([file('other.json', { foo: 1 })]);
  assert.equal(shown(), 'Unrecognized format: other.json');
  await ui.importFiles([file('broken.json', '{')]);
  assert.equal(shown(), 'Not a JSON file: broken.json');
  assert.deepEqual(ui.state.collections.map(c => c.name), ['One', 'Shop'], 'nothing more came in');
});
