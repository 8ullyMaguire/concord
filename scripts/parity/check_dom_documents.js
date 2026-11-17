#!/usr/bin/env node
// Step 2b: executes the shipped documents page with hostile document fields, search hits and a large document.
// Every string sink is confined to ConcordMarkdown.renderInto(); documents.js itself has none.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const base = process.argv[2] || 'crates/web/assets/js';
const domSource = fs.readFileSync(base + '/dom.js', 'utf8');
const escapeSource = fs.readFileSync(base + '/escape.js', 'utf8');
const markdownSource = fs.readFileSync(base + '/markdown.js', 'utf8');
const pageSource = fs.readFileSync(base + '/documents.js', 'utf8');

function textNode(value) {
  const text = String(value);
  return { nodeType: 3, text: text, get textContent() { return text; } };
}
function decode(s) {
  return String(s).replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&').replace(/&quot;/g, '"');
}
function parseHTML(markup) {
  const nodes = [];
  const re = /<([a-z][a-z0-9]*)([^>]*)>([\s\S]*?)<\/\1>/gi;
  let m;
  while ((m = re.exec(markup))) {
    const n = element(m[1]);
    const attrs = m[2];
    const id = /\bid="([^"]*)"/.exec(attrs);
    const cls = /\bclass="([^"]*)"/.exec(attrs);
    if (id) n.setAttribute('id', id[1]);
    if (cls) n.setAttribute('class', cls[1]);
    n.children = [textNode(decode(m[3]))];
    nodes.push(n);
  }
  if (!nodes.length && markup) nodes.push(textNode(decode(markup)));
  return nodes;
}
function tokenMatch(n, token) {
  const id = /^#(.+)$/.exec(token);
  const cls = /^\.(.+)$/.exec(token);
  const eq = /^\[([^=\]^]+)="([^"]+)"\]$/.exec(token);
  const pre = /^\[([^=\]^]+)\^="([^"]+)"\]$/.exec(token);
  const attr = /^\[(.+)\]$/.exec(token);
  if (id) return n.attrs.id === id[1];
  if (cls) {
    const names = cls[1].split('.');
    const have = (n.attrs.class || '').split(' ');
    return names.every(function (c) { return have.indexOf(c) !== -1; });
  }
  if (eq) return n.attrs[eq[1]] === eq[2];
  if (pre) return String(n.attrs[pre[1]] || '').indexOf(pre[2]) === 0;
  if (attr) return Object.prototype.hasOwnProperty.call(n.attrs, attr[1]);
  return n.tagName === token;
}
function matches(n, sel) {
  const direct = sel.replace(/\s*>\s*/g, ' ');
  const parts = direct.split(/\s+/).filter(Boolean);
  let cur = n;
  for (let i = parts.length - 1; i >= 0; i--) {
    if (!cur || !tokenMatch(cur, parts[i])) return false;
    cur = cur.parent;
  }
  return true;
}
function walk(node, out) {
  out = out || [];
  (node.children || []).forEach(function (child) {
    out.push(child);
    if (child.nodeType === 1) walk(child, out);
  });
  return out;
}
function find(root, sel) { return walk(root).filter(function (n) { return n.nodeType === 1 && matches(n, sel); }); }
function element(tag) {
  const n = {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], parent: null, listeners: {},
    hidden: false, value: '', style: {},
    setAttribute(k, v) { this.attrs[k] = String(v); if (k === 'id') this.id = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    hasAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k); },
    removeAttribute(k) { delete this.attrs[k]; },
    appendChild(c) { c.parent = this; this.children.push(c); return c; },
    removeChild(c) { this.children = this.children.filter(function (x) { return x !== c; }); c.parent = null; },
    replaceChildren() {
      this.children = [];
      Array.prototype.slice.call(arguments).forEach(function (c) { if (c != null) n.appendChild(c); });
    },
    addEventListener(ev, fn) { this.listeners[ev] = fn; },
    closest(sel) {
      let cur = this;
      while (cur) { if (cur.nodeType === 1 && tokenMatch(cur, sel)) return cur; cur = cur.parent; }
      return null;
    },
    querySelector(sel) { return find(this, sel)[0] || null; },
    querySelectorAll(sel) { return find(this, sel); },
    scrollIntoView() { this._scrolled = true; },
    get text() {
      return this.children.map(function (c) { return c.nodeType === 3 ? c.text : c.textContent; }).join('');
    },
    set innerHTML(v) {
      this._htmlWrites = (this._htmlWrites || []).concat(String(v));
      this.children = [];
      parseHTML(String(v)).forEach(function (c) { n.appendChild(c); });
    },
    insertAdjacentHTML(pos, v) { this._htmlWrites = (this._htmlWrites || []).concat(String(v)); },
  };
  n.classList = {
    add(c) { n.attrs.class = ((n.attrs.class || '') + ' ' + c).trim(); },
    remove(c) { n.attrs.class = (n.attrs.class || '').split(' ').filter(function (x) { return x !== c; }).join(' '); },
    contains(c) { return (n.attrs.class || '').split(' ').indexOf(c) !== -1; },
  };
  Object.defineProperty(n, 'textContent', {
    get() { return n.text; },
    set(v) { n.children = [textNode(v)]; },
  });
  return n;
}

const HOSTILE_TITLE = '<b>hostile title</b>';
const HOSTILE_BODY = '## First section\n\n<img src=x onerror=alert(1)>\n\n## Second section\n\n<b>alert(2)</b>';
const HOSTILE_SEARCH = '<script>alert(3)</script>';

function makeSandbox(opts) {
  const box = element('div');
  box.id = 'project-documents';
  box.setAttribute('id', 'project-documents');
  const searchForm = element('form');
  searchForm.id = 'doc-search';
  searchForm.setAttribute('id', 'doc-search');
  const status = element('span');
  status.setAttribute('data-search-status', '');
  searchForm.appendChild(status);
  const input = element('input');
  input.id = 'doc-search-input';
  input.setAttribute('id', 'doc-search-input');
  const docListeners = {};
  const sandbox = {
    console, Date, Math, Promise, Object, Array, String, Number, setTimeout, clearTimeout,
    encodeURIComponent, decodeURIComponent, URL, URLSearchParams,
    history: { replaceState() {} },
    navigator: {},
    Intl,
  };
  sandbox.document = {
    createElement: element, createTextNode: textNode, body: element('body'),
    getElementById(id) {
      if (id === 'project-documents') return box;
      if (id === 'doc-search') return searchForm;
      if (id === 'doc-search-input') return input;
      return null;
    },
    addEventListener(ev, fn) { docListeners[ev] = fn; },
  };
  sandbox.window = sandbox;
  sandbox.location = opts.location || {
    pathname: '/projects/lorebook/documents', search: '',
    href: 'https://concord.polarisocial.xyz/projects/lorebook/documents',
  };
  sandbox.ConcordSkeleton = { done(el) { el.setAttribute('aria-busy', 'false'); } };
  sandbox.IntersectionObserver = function (fn) { this.observe = function () {}; this.unobserve = function () {}; };
  sandbox.CSS = { escape(s) { return s; } };
  sandbox.ConcordApi = {
    get(path) {
      if (opts.failList) return Promise.reject(new Error('boom'));
      if (/documents\/search/.test(path)) {
        if (opts.failSearch) { const e = new Error('bad query'); e.status = 400; return Promise.reject(e); }
        return Promise.resolve([{ id: 9, kind: 'adr', title: HOSTILE_SEARCH, slug: 'hit', body: '' }]);
      }
      if (/\/documents\/1$/.test(path)) return Promise.resolve({
        id: 1, kind: 'readme', title: HOSTILE_TITLE, slug: 'readme', revision: 2,
        body: opts.big ? '## First section\n' + 'x'.repeat(210000) + '\n## Second section\nbody' : HOSTILE_BODY,
      });
      return Promise.resolve([{ id: 1, kind: 'readme', title: HOSTILE_TITLE, slug: 'readme', body: '', revision: 2 }]);
    },
  };
  vm.createContext(sandbox);
  return { sandbox, box, searchForm, input, status, docListeners };
}

async function boot(opts) {
  const s = makeSandbox(opts || {});
  [escapeSource, domSource, markdownSource, pageSource].forEach(function (src, i) {
    vm.runInContext(src, s.sandbox, { filename: ['escape.js', 'dom.js', 'markdown.js', 'documents.js'][i] });
  });
  if (opts && opts.breakRender) {
    s.sandbox.ConcordMarkdown.renderInto = function () { throw new Error('boom'); };
  }
  s.docListeners.DOMContentLoaded();
  for (let i = 0; i < 12; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  return s;
}
function assertNoPageSinks(box) {
  const all = [box].concat(walk(box));
  const writes = all.filter(function (n) {
    return n._htmlWrites && n._htmlWrites.length &&
      !n._htmlWrites.every(function (s) { return /^<(h[1-6]|p|ul|ol|pre|blockquote|table|hr)\b/.test(s); });
  });
  assert.ok(!writes.length, 'documents.js wrote to a non-markdown HTML string sink: ' +
    JSON.stringify(writes.map(function (n) { return n._htmlWrites; })));
}
function assertNoInjected(box, tag) {
  assert.strictEqual(find(box, tag).length, 0, 'a hostile document field was parsed into a ' + tag);
}

(async function () {
  const good = await boot({});
  assert.strictEqual(find(good.box, '.doc-kind').length, 6, 'the kind rail lost its entries');
  assert.strictEqual(find(good.box, '.doc-item').length, 1, 'the document list lost its entry');
  assert.strictEqual(find(good.box, '.doc-item-title')[0].textContent, HOSTILE_TITLE,
    'the hostile document title was not literal text');
  assert.strictEqual(find(good.box, '.page-title')[0].textContent, HOSTILE_TITLE,
    'the hostile reader title was not literal text');
  assert.strictEqual(find(good.box, '[data-copy]').length, 1, 'the copy control disappeared');
  assert.strictEqual(good.box.attrs['aria-busy'], 'false', 'aria-busy was left set');
  assertNoInjected(good.box, 'b');
  assertNoInjected(good.box, 'img');
  assertNoPageSinks(good.box);

  // The only HTML write is the sanitizer's, and authored HTML is escaped in it.
  const writes = walk(good.box).filter(function (n) { return n._htmlWrites; });
  assert.strictEqual(writes.length, 1, 'expected exactly one markdown sink write, got ' + writes.length);
  assert.ok(writes[0]._htmlWrites[0].indexOf('&lt;img') !== -1,
    'the sanitizer did not escape the hostile image');
  assert.ok(writes[0]._htmlWrites[0].indexOf('doc-s-0') !== -1,
    'the renderer did not emit heading ids');

  // Search replaces the list column, and its hostile title stays text.
  good.input.value = 'hostile';
  good.searchForm.listeners.submit({ preventDefault() {} });
  for (let i = 0; i < 8; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  assert.strictEqual(find(good.box, '.doc-item-title')[0].textContent, HOSTILE_SEARCH,
    'the hostile search title was not literal text');
  assertNoInjected(good.box, 'script');
  assertNoPageSinks(good.box);
  assert.ok(good.status.textContent.indexOf('result') !== -1, 'the search status disappeared');

  // Failure, no-slug and large-document paths.
  const bad = await boot({ breakRender: true });
  assert.ok(bad.box.textContent.indexOf('Could not load documents') !== -1, 'the failure state disappeared');
  assert.strictEqual(bad.box.attrs['aria-busy'], 'false', 'the failure path left aria-busy set');
  assertNoPageSinks(bad.box);

  const noSlug = await boot({ location: { pathname: '/', search: '', href: 'https://x/' } });
  assert.strictEqual(noSlug.box.textContent, 'No project.', 'the no-slug state disappeared');
  assertNoPageSinks(noSlug.box);

  const big = await boot({ big: true });
  assert.strictEqual(find(big.box, '.doc-section.is-pending').length, 1, 'the lazy placeholder disappeared');
  assert.strictEqual(find(big.box, '[data-section-body]').length, 1, 'the section index disappeared');
  assert.strictEqual(find(big.box, '.doc-toc-link').length, 2, 'the large-document TOC disappeared');
  assertNoPageSinks(big.box);

  assert.ok(!/(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/.test(pageSource),
    'documents.js regained an HTML string sink');
  assert.ok(/node\.innerHTML = render\(source, opts\)/.test(markdownSource),
    'the allowlisted markdown sink moved out of the sanitizer');
  console.log('dom documents page: PASS — hostile fields are text, one allowlisted markdown sink, no page sink');
})().catch(function (e) {
  console.error('dom documents page: FAIL — ' + e.message);
  process.exitCode = 1;
});
