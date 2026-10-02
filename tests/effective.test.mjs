// Node side of the shared vectors in testdata/effective.json. A schedule sends
// a request that effective.go builds in Go; the Send button sends one that
// buildEffective in ui.html builds. effective_test.go runs the same vectors
// against the Go side, so both build the same request from the same data.
// Run: node --test "tests/**/*.test.mjs"
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { sr } from './load-ui.mjs';

const V = JSON.parse(readFileSync(new URL('../testdata/effective.json', import.meta.url), 'utf8'));

// What want of a request vector leaves out.
const BASE = { method: 'GET', url: '', headers: [], body: '', bodyBase64: false, timeoutMs: 30000, followRedirects: true, verifyTls: true, cookies: true };

test('whitespace is \\s and what String.prototype.trim removes on this engine', () => {
  const space = [], trimmed = [];
  for (let c = 0; c <= 0x10ffff; c++) {
    const ch = String.fromCodePoint(c);
    if (/\s/.test(ch)) space.push(c);
    if ((ch + 'x' + ch).trim() === 'x') trimmed.push(c);
  }
  assert.equal(V.whitespace.length, 25);
  assert.deepEqual(space, V.whitespace);
  assert.deepEqual(trimmed, V.whitespace);
});

test('trim', () => {
  for (const { in: s, out } of V.trim) assert.equal(s.trim(), out, JSON.stringify(s));
});

test('encodeURIComponent', () => {
  for (const { in: s, out } of V.encodeURIComponent) assert.equal(encodeURIComponent(s), out, JSON.stringify(s));
});

// Each request vector goes through the page as a data file would: normalizeDoc
// reads the settings, the request and the environment, then buildEffective
// builds the request with the variables of that environment.
for (const v of V.requests) {
  test('request: ' + v.name, () => {
    const doc = sr.normalizeDoc({ settings: v.settings, collections: [{ requests: [v.request] }], envs: [{ vars: v.vars }] });
    const eff = sr.buildEffective(doc.collections[0].requests[0], sr.varsOf(doc.envs[0]));
    assert.deepEqual(eff, { ...BASE, ...v.want });
  });
}

test('the vector names are unique', () => {
  const names = V.requests.map(v => v.name);
  assert.deepEqual(names, [...new Set(names)]);
});
