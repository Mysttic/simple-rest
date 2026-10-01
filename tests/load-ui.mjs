// Loads the script of ui.html into Node, without a browser. The page starts
// itself only when its markup is there, so evaluating the script only defines
// its functions and exposes them as globalThis.__sr. This module is not a test
// itself: node --test picks up tests/**/*.test.mjs only.
import { readFileSync } from 'node:fs';

const html = readFileSync(new URL('../ui.html', import.meta.url), 'utf8');
const src = html.match(/<script>([\s\S]*?)<\/script>/)[1];

// loadUI evaluates a fresh copy of the script, with state of its own, and
// returns what it exposes. A fake document set up by a test is hidden
// meanwhile, so the page never starts and creates no timers or listeners.
export function loadUI() {
  const doc = Object.getOwnPropertyDescriptor(globalThis, 'document');
  delete globalThis.document;
  try {
    new Function(src)();
  } finally {
    if (doc) Object.defineProperty(globalThis, 'document', doc);
  }
  return globalThis.__sr;
}

export const sr = loadUI();

// ---- fakes for the tests that go past the pure functions

// FakeElement stands in for any element the page touches: it takes any
// property, keeps its listeners for dispatch(), and what it looks up inside
// itself is new. closest(sel) gives what a test put into parents[sel].
class FakeElement {
  constructor(tag = 'div') {
    Object.assign(this, {
      tagName: tag.toUpperCase(), hidden: false, value: '', textContent: '', innerHTML: '', className: '',
      checked: false, disabled: false, style: {}, dataset: {}, children: [], isConnected: true,
      parents: {}, listeners: {},
    });
    const cls = new Set();
    this.classList = {
      add: (...c) => c.forEach(x => cls.add(x)),
      remove: (...c) => c.forEach(x => cls.delete(x)),
      toggle: (c, on = !cls.has(c)) => { if (on) cls.add(c); else cls.delete(c); return on; },
      contains: c => cls.has(c),
    };
  }
  addEventListener(type, fn, opts) { (this.listeners[type] ||= []).push({ fn, once: !!(opts && opts.once) }); }
  removeEventListener(type, fn) { this.listeners[type] = (this.listeners[type] || []).filter(l => l.fn !== fn); }
  // dispatch calls the listeners of type, and drops those added with once.
  dispatch(type) {
    const ls = this.listeners[type] || [];
    this.listeners[type] = ls.filter(l => !l.once);
    for (const l of ls) l.fn({ type, target: this });
  }
  querySelector() { return new FakeElement(); }
  // A key/value row has three inputs: the checkbox, the name and the value.
  querySelectorAll() { return [new FakeElement('input'), new FakeElement('input'), new FakeElement('input')]; }
  appendChild(c) { this.children.push(c); return c; }
  insertRow() { return new FakeElement('tr'); }
  closest(sel) { return this.parents[sel] || null; }
  remove() {}
  focus() {}
  select() {}
  click() {}
  setAttribute() {}
}

// fakeDom is a document whose every selector names one element that stays
// the same, so a test can read what the page put into it.
export function fakeDom() {
  const els = new Map();
  const el = s => { if (!els.has(s)) els.set(s, new FakeElement()); return els.get(s); };
  return {
    activeElement: null, visibilityState: 'visible', body: new FakeElement('body'),
    querySelector: el, getElementById: id => el('#' + id), querySelectorAll: () => [],
    createElement: tag => new FakeElement(tag), addEventListener() {}, removeEventListener() {},
  };
}

// settle lets every promise chain that waits for nothing but other promises
// run to its end.
export async function settle() {
  for (let i = 0; i < 20; i++) await new Promise(r => setImmediate(r));
}

// fakeTimers replaces setTimeout, clearTimeout, setInterval and clearInterval
// until restore(). run(ms) moves the clock and fires the timeouts that are
// due; intervals are only counted.
export function fakeTimers() {
  const saved = Object.fromEntries(['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'].map(k => [k, globalThis[k]]));
  const due = new Map();
  let now = 0, seq = 0, intervals = 0;
  Object.assign(globalThis, {
    setTimeout: (fn, ms = 0, ...args) => { due.set(++seq, { at: now + ms, fn, args }); return seq; },
    clearTimeout: id => { due.delete(id); },
    setInterval: () => ++intervals,
    clearInterval: () => {},
  });
  return {
    get pending() { return due.size; },
    get created() { return seq + intervals; },
    async run(ms) {
      now += ms;
      for (const [id, t] of [...due].sort((a, b) => a[1].at - b[1].at)) {
        if (t.at > now || !due.has(id)) continue;
        due.delete(id);
        t.fn(...t.args);
      }
      await settle();
    },
    restore() { Object.assign(globalThis, saved); },
  };
}
