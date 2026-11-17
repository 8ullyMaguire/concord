#!/usr/bin/env node
// Step 2b: the consensus page's four sinks are three list clears and one decision-record link. This executes the shipped
// file in a fake browser on its index path and fails if any element is written through an HTML string sink.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/consensus.js';
const domSource = fs.readFileSync(domPath, 'utf8');
const pageSource = fs.readFileSync(pagePath, 'utf8');
const writes = [];

function textNode(value) {
  const text = String(value);
  return { nodeType: 3, text: text, get textContent() { return text; } };
}
function element(tag) {
  return {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {}, value: '',
    get id() { return this.attrs.id || ''; },
    set id(v) { this.attrs.id = String(v); },
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    hasAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k); },
    appendChild(c) { this.children.push(c); return c; },
    replaceChildren() { this.children = Array.prototype.slice.call(arguments); },
    addEventListener(ev, fn) { this.listeners[ev] = fn; },
    querySelector() { return null; },
    querySelectorAll() { return []; },
    remove() {},
    get textContent() {
      return this.children.map(function (c) { return c.nodeType === 3 ? c.text : c.textContent; }).join('');
    },
    set textContent(v) { this.children = [textNode(v)]; },
    set innerHTML(v) { writes.push(String(v)); },
    insertAdjacentHTML(pos, v) { writes.push(String(v)); },
  };
}

const byID = Object.create(null);
const sandbox = {
  console, Date, Math, Promise, Object, Array, String, Number, parseFloat, parseInt, isNaN, isFinite,
  encodeURIComponent, decodeURIComponent, setTimeout, clearTimeout,
  location: { pathname: '/projects/lorebook/consensus', search: '', href: 'https://example.test/consensus' },
};
sandbox.document = {
  readyState: 'complete',
  createElement: element,
  createTextNode: textNode,
  getElementById(id) {
    if (!byID[id]) { byID[id] = element('div'); byID[id].attrs.id = id; }
    return byID[id];
  },
  querySelector() { return null; },
  querySelectorAll() { return []; },
  addEventListener() {},
};
sandbox.localStorage = { getItem() { return null; }, setItem() {}, removeItem() {} };
sandbox.window = sandbox;
sandbox.ConcordSession = { instance() { return { token: '' }; } };
sandbox.ConcordEscape = { esc(v) { return String(v == null ? '' : v); } };
sandbox.ConcordApi = {
  get() { return Promise.resolve([]); },
  post() { return Promise.resolve({}); },
  put() { return Promise.resolve({}); },
  request() { return Promise.resolve({}); },
};
vm.createContext(sandbox);
vm.runInContext(domSource, sandbox, { filename: domPath });
vm.runInContext(pageSource, sandbox, { filename: pagePath });

(async function () {
  for (let i = 0; i < 4; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  assert.strictEqual(writes.length, 0, 'consensus.js wrote to an HTML string sink: ' + JSON.stringify(writes));
  assert.strictEqual(byID['consensus-root'].attrs['aria-busy'], 'false', 'the page never cleared its busy state');
  const sink = /(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/;
  assert.ok(!sink.test(pageSource), 'consensus.js regained an HTML string sink');
  assert.ok(pageSource.includes('replaceChildren()'), 'a list clear was not converted to replaceChildren()');
  assert.ok(pageSource.includes("domEl('a'"), 'the decision-record link is not built with el()');
  console.log('dom consensus page: PASS — no string sink, clears and record link use the DOM');
})().catch(function (err) {
  console.error('dom consensus page: FAIL — ' + err.message);
  process.exitCode = 1;
});
