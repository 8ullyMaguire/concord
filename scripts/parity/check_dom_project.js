#!/usr/bin/env node
// Step 2b / D-16: project.js's outer states must be DOM nodes, not parsed HTML.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const skeletonPath = process.argv[3] || 'crates/web/assets/js/skeleton.js';
const pagePath = process.argv[4] || 'crates/web/assets/js/project.js';
const sources = [domPath, skeletonPath, pagePath].map(function (p) {
  return { path: p, source: fs.readFileSync(p, 'utf8') };
});

function textNode(value) {
  const text = String(value);
  return { nodeType: 3, text: text, get textContent() { return text; } };
}
function matches(n, sel) {
  const cls = /^\.(.+)$/.exec(sel);
  const id = /^#(.+)$/.exec(sel);
  if (id) return n.attrs.id === id[1];
  if (cls) return (n.attrs.class || '').split(' ').indexOf(cls[1]) !== -1;
  return n.tagName === sel;
}
function append(n, child) {
  if (child == null) return;
  if (Array.isArray(child)) return child.forEach(function (c) { append(n, c); });
  if (child.nodeType) { n.children.push(child); return; }
  n.children.push(textNode(child));
}
function walk(node, out) {
  out = out || [];
  (node.children || []).forEach(function (c) { out.push(c); if (c.nodeType === 1) walk(c, out); });
  return out;
}
function find(root, sel) { return walk(root).filter(function (n) { return n.nodeType === 1 && matches(n, sel); }); }
function element(tag) {
  const n = {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {}, value: '',
    get className() { return this.attrs.class || ''; },
    set className(v) { this.attrs.class = String(v); },
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; },
    hasAttribute(k) { return k in this.attrs; },
    appendChild(c) { append(this, c); return c; },
    replaceChildren() { this.children = []; Array.prototype.slice.call(arguments).forEach(function (c) { append(n, c); }); },
    addEventListener(ev, fn) { this.listeners[ev] = fn; },
    querySelector(sel) { return find(this, sel)[0] || null; },
    querySelectorAll(sel) { return find(this, sel); },
    get text() {
      return this.children.map(function (c) { return c.nodeType === 3 ? c.text : c.textContent; }).join('');
    },
    set innerHTML(v) { this._htmlWrites = (this._htmlWrites || []).concat(String(v)); },
    insertAdjacentHTML(pos, v) { this._htmlWrites = (this._htmlWrites || []).concat(String(v)); },
  };
  Object.defineProperty(n, 'textContent', {
    get() { return n.text; },
    set(v) { n.children = [textNode(v)]; },
  });
  return n;
}
const HOSTILE_SLUG = '<img src=x onerror=alert(1)>';

function boot(pathname, fail) {
  const box = element('div'); box.setAttribute('id', 'project-detail');
  const calls = [];
  let released;
  const held = new Promise(function (resolve) { released = resolve; });
  const sandbox = {
    console, Date, Math, Promise, Object, Array, String, Number, JSON, isFinite,
    encodeURIComponent, decodeURIComponent, setTimeout: function (fn) { fn(); return 1; },
    clearTimeout: function () {},
    location: { pathname: pathname, href: 'https://concord.polarisocial.xyz' + pathname },
  };
  sandbox.document = {
    createElement: element, createTextNode: textNode, title: '',
    getElementById(id) { return id === 'project-detail' ? box : null; },
    addEventListener(ev, fn) { this[ev] = fn; },
  };
  sandbox.window = sandbox;
  sandbox.ConcordEscape = { esc: function (s) { return String(s); } };
  sandbox.ConcordSession = { instance: function () { return { signedIn: function () { return false; } }; } };
  sandbox.ConcordPanels = {
    intakeMode: function () { return 'complaint_first'; },
    complaintCards: function () { return ''; }, workItemsSection: function () { return ''; },
    requestSection: function () { return ''; }, listSection: function () { return ''; },
  };
  sandbox.ConcordWrites = {
    setStatus: function () {}, postJSON: function () { return Promise.resolve({}); }, showCardRefusal: function () {},
  };
  sandbox.ConcordArena = { controls: function () { return ''; }, wire: function () {} };
  sandbox.ConcordApi = {
    get(url) {
      calls.push('GET ' + url);
      if (fail && fail.first) return Promise.reject(fail.first);
      if (fail && fail.rest && calls.length > 1) return Promise.reject(fail.rest);
      if (fail && fail.hold && calls.length > 1) return held;
      return Promise.resolve({ id: 1, slug: 'lorebook', name: 'Lorebook' });
    },
  };
  vm.createContext(sandbox);
  sources.forEach(function (f) { vm.runInContext(f.source, sandbox, { filename: f.path }); });
  sandbox.document.DOMContentLoaded();
  return { sandbox, box, calls, release: function () { released([]); } };
}
function settle() {
  return new Promise(function (resolve) {
    setImmediate(function () { setImmediate(function () { setImmediate(resolve); }); });
  });
}
function noSinks(s) {
  const all = [s.box].concat(walk(s.box));
  const writes = all.filter(function (n) { return n._htmlWrites && n._htmlWrites.length; });
  assert.ok(!writes.length, 'project.js outer state wrote to an HTML string sink: ' +
    JSON.stringify(writes.map(function (n) { return n._htmlWrites; })));
  assert.strictEqual(find(s.box, 'img').length, 0, 'a hostile slug was parsed into an element');
}

(async function () {
  const missing = boot('/projects/', null);
  assert.ok(missing.box.textContent.indexOf('Project not found') !== -1,
    'an empty slug did not render the not-found state');
  noSinks(missing);

  const notFound = boot('/projects/' + encodeURIComponent(HOSTILE_SLUG),
    { first: { status: 404, notFound: true } });
  await settle();
  assert.ok(notFound.box.textContent.indexOf(HOSTILE_SLUG) !== -1,
    'the hostile slug was not rendered as literal text');
  assert.strictEqual(find(notFound.box, 'a')[0].attrs.href, '/projects',
    'the browse-projects link was mangled');
  noSinks(notFound);

  const loading = boot('/projects/lorebook', { hold: true });
  await settle();
  assert.strictEqual(find(loading.box, '.skeleton-title').length, 1,
    'the skeleton title disappeared');
  assert.strictEqual(find(loading.box, '.skeleton-text').length, 3,
    'the three skeleton lines disappeared');
  noSinks(loading);
  loading.release();

  const auth = boot('/projects/lorebook', { rest: { status: 401 } });
  await settle();
  assert.ok(auth.box.textContent.indexOf('Sign in to see features') !== -1,
    'the auth refusal disappeared');
  assert.strictEqual(find(auth.box, 'a')[0].attrs.href, '/login',
    'the sign-in link was mangled');
  noSinks(auth);

  const failure = boot('/projects/lorebook', { first: { status: 503 } });
  await settle();
  assert.ok(failure.box.textContent.indexOf('Could not load project (HTTP 503)') !== -1,
    'the HTTP status was not reported');
  noSinks(failure);

  const pageSource = sources[2].source;
  assert.ok(!/if \(!slug\) \{ box\.innerHTML/.test(pageSource), 'empty-slug path regained an HTML string sink');
  assert.ok(!/box\.innerHTML = \(window\.ConcordSkeleton/.test(pageSource), 'loading path regained an HTML string sink');
  assert.ok(!/box\.innerHTML = notFound/.test(pageSource), 'not-found path regained an HTML string sink');
  assert.ok(!/box\.innerHTML = '<div class="empty-state"/.test(pageSource),
    'a failure path regained an HTML string sink');
  console.log('dom project outer states: PASS — slug is text, skeleton and refusals are nodes, no string sink');
})().catch(function (e) {
  console.error('dom project outer states: FAIL — ' + e.message);
  process.exitCode = 1;
});
