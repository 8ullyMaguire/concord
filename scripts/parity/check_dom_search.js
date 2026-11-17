#!/usr/bin/env node
// Step 2b: executes the shipped search page with hostile results and tag facets.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/search.js';
const domSource = fs.readFileSync(domPath, 'utf8');
const pageSource = fs.readFileSync(pagePath, 'utf8');

function textNode(value) {
  const text = String(value);
  return { nodeType: 3, text: text, get textContent() { return text; } };
}
function matches(n, sel) {
  const cls = /^\.(.+)$/.exec(sel);
  const eq = /^\[([^=\]]+)="([^"]+)"\]$/.exec(sel);
  const attr = /^\[(.+)\]$/.exec(sel);
  if (cls) return (n.attrs.class || '').split(' ').indexOf(cls[1]) !== -1;
  if (eq) return n.attrs[eq[1]] === eq[2];
  if (attr) return Object.prototype.hasOwnProperty.call(n.attrs, attr[1]);
  return n.tagName === sel;
}
function walk(node, out) {
  out = out || [];
  (node.children || []).forEach(function (c) { out.push(c); if (c.nodeType === 1) walk(c, out); });
  return out;
}
function find(root, sel) { return walk(root).filter(function (n) { return n.nodeType === 1 && matches(n, sel); }); }
function element(tag) {
  const n = {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {},
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    removeAttribute(k) { delete this.attrs[k]; },
    appendChild(c) { this.children.push(c); return c; },
    replaceChildren() { this.children = Array.prototype.slice.call(arguments).filter(function (c) { return c != null; }); },
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

const HOSTILE_NAME = '<img src=x onerror=alert(1)>';
const HOSTILE_DESC = '<script>alert(2)</script>';
const HOSTILE_SLUG = 'x" onmouseover="alert(3)';
const HOSTILE_TAG = '<b>tag</b>';

function makeSandbox(opts) {
  const results = element('div');
  results.id = 'search-results';
  results.setAttribute('id', 'search-results');
  const chips = element('div');
  chips.id = 'tag-chips';
  chips.setAttribute('id', 'tag-chips');
  const listeners = {};
  const sandbox = { console, Date, Math, Promise, Object, Array, String, Number, URL, URLSearchParams, setTimeout };
  sandbox.document = {
    createElement: element, createTextNode: textNode,
    getElementById(id) { return id === 'search-results' ? results : id === 'tag-chips' ? chips : null; },
    addEventListener(ev, fn) { listeners[ev] = fn; },
  };
  sandbox.window = sandbox;
  sandbox.location = {
    search: opts.noQuery ? '' : '?q=' + encodeURIComponent('hostile') + '&tag=alpha&tag=' + encodeURIComponent(HOSTILE_TAG),
    href: 'https://concord.polarisocial.xyz/search',
  };
  let doneCount = 0;
  sandbox.ConcordSkeleton = { done(el) {
    if (opts.throwOnDone && ++doneCount === 2) throw new Error('boom');
    el.setAttribute('aria-busy', 'false');
  } };
  sandbox.ConcordApi = {
    get() {
      if (opts.throwLater) throw new Error('boom');
      return Promise.resolve({
        total: 4,
        results: [{
          slug: HOSTILE_SLUG, name: HOSTILE_NAME, description: HOSTILE_DESC,
          governance_model: 'council', license: 'MIT', health_score: 0.82,
        }],
        facets: { tags: [
          { value: 'alpha', count: 3 },
          { value: HOSTILE_TAG, count: 2 },
          { value: 'inactive', count: 1 },
        ] },
      });
    },
  };
  vm.createContext(sandbox);
  return { sandbox, results, chips, listeners };
}
async function boot(opts) {
  const s = makeSandbox(opts || {});
  vm.runInContext(domSource, s.sandbox, { filename: domPath });
  vm.runInContext(pageSource, s.sandbox, { filename: pagePath });
  s.listeners.DOMContentLoaded();
  for (let i = 0; i < 8; i++) await new Promise(function (r) { setImmediate(r); });
  return s;
}
function noSinks(s) {
  const all = [s.results, s.chips].concat(walk(s.results), walk(s.chips));
  const writes = all.filter(function (n) { return n._htmlWrites && n._htmlWrites.length; });
  assert.ok(!writes.length, 'search.js wrote to an HTML string sink: ' +
    JSON.stringify(writes.map(function (n) { return n._htmlWrites; })));
  assert.ok(!all.some(function (n) {
    return n.nodeType === 1 && ['img', 'script', 'b'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
}

(async function () {
  const good = await boot({});
  assert.strictEqual(find(good.results, '.card').length, 1, 'the result card disappeared');
  assert.strictEqual(find(good.results, '.card')[0].attrs.href, '/projects/' + HOSTILE_SLUG,
    'the hostile slug was mangled in the card link');
  assert.strictEqual(find(good.results, '.card-title')[0].textContent.indexOf(HOSTILE_NAME) !== -1, true,
    'the hostile name was not literal text');
  assert.strictEqual(find(good.results, '.card-text')[0].textContent, HOSTILE_DESC,
    'the hostile description was not literal text');
  assert.strictEqual(find(good.results, '.health-fill')[0].attrs.style, 'width:82%',
    'the health bar lost its width');
  assert.strictEqual(find(good.results, '.meta-item')[0].textContent, '1 of 4 projects',
    'the result count disappeared');
  assert.strictEqual(good.results.getAttribute('aria-busy'), 'false', 'aria-busy was left on');
  assert.strictEqual(find(good.chips, '.filter-chip').length, 3, 'the tag chips disappeared');
  const hostileChip = find(good.chips, '.filter-chip')[1];
  assert.strictEqual(hostileChip.textContent, HOSTILE_TAG + ' (2)',
    'the hostile tag was not literal text');
  assert.ok(hostileChip.attrs.href.indexOf('tag=' + encodeURIComponent(HOSTILE_TAG)) === -1,
    'an active chip must toggle its own tag off, not keep it');
  const inactiveChip = find(good.chips, '.filter-chip')[2];
  assert.ok(inactiveChip.attrs.href.indexOf('tag=' + encodeURIComponent(HOSTILE_TAG)) !== -1,
    'an inactive chip must carry the other active tag forward');
  assert.ok(find(good.chips, '.filter-chip')[0].attrs.href.indexOf('tag=' + encodeURIComponent(HOSTILE_TAG)) !== -1,
    'the other active tag disappeared from the chip link');
  noSinks(good);

  const empty = await boot({ noQuery: true });
  assert.strictEqual(empty.results.textContent, '', 'the no-query path should leave the server-rendered state');
  noSinks(empty);

  const failed = await boot({ throwOnDone: true });
  assert.ok(failed.results.textContent.indexOf('Search failed') !== -1, 'the failure state disappeared');
  assert.strictEqual(failed.results.getAttribute('aria-busy'), 'false', 'the failure path left aria-busy on');
  noSinks(failed);

  assert.ok(!/(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/.test(pageSource),
    'search.js regained an HTML string sink');
  console.log('dom search page: PASS — hostile results and tags are text, hrefs verbatim, no string sink');
})().catch(function (e) {
  console.error('dom search page: FAIL — ' + e.message);
  process.exitCode = 1;
});
