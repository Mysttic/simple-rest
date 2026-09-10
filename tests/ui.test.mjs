// Node tests for the pure parts of ui.html. The script is pulled out of the
// page and evaluated here without a DOM (the page only starts itself when its
// markup is present), so only the functions that build requests, parse URLs
// and import files are exercised.
// Run: node --test "tests/**/*.test.mjs"
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const html = readFileSync(new URL('../ui.html', import.meta.url), 'utf8');
const src = html.match(/<script>([\s\S]*?)<\/script>/)[1];
new Function(src)();
const sr = globalThis.__sr;

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
