#!/usr/bin/env node
// Step 2b: executes the shipped ranking page with hostile feature fields and a hostile load error.
// Every field must be a text node or a setAttribute value; no HTML string sink may be used.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/ranking.js';
const domSource = fs.readFileSync(domPath, 'utf8');
const pageSource = fs.readFileSync(pagePath, 'utf8');

function textNode(value) {
  const text = String(value);
  return { nodeType: 3, text: text, get textContent() { return text; } };
}
function walk(node, out) {
  out = out || [];
  (node.children || []).forEach(function (child) {
    out.push(child);
    if (child.nodeType === 1) walk(child, out);
  });
  return out;
}
function element(tag) {
  return {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {},
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    appendChild(c) { this.children.push(c); return c; },
    replaceChildren() { this.children = Array.prototype.slice.call(arguments); },
    addEventListener(ev, fn) { this.listeners[ev] = fn; },
    querySelector() { return null; },
    get textContent() {
      return this.children.map(function (c) { return c.nodeType === 3 ? c.text : c.textContent; }).join('');
    },
    set innerHTML(v) { this._htmlWrites = (this._htmlWrites || []).concat(String(v)); },
    insertAdjacentHTML(pos, v) { this._htmlWrites = (this._htmlWrites || []).concat(String(v)); },
  };
}

const HOSTILE_TITLE = '<img src=x onerror=alert(1)>';
const HOSTILE_BODY = '<script>alert(2)</script>';
const HOSTILE_ERROR = '<script>alert(3)</script>';

function makeSandbox(root, mode) {
  const sandbox = {
    console, Date, Math, Promise, Object, Array, String, Number, isNaN, isFinite, setTimeout,
    encodeURIComponent, decodeURIComponent,
    location: { pathname: '/projects/lorebook/rank' },
  };
  const listeners = Object.create(null);
  sandbox.document = {
    createElement: element,
    createTextNode: textNode,
    getElementById(id) { return id === 'ranking-root' ? root : null; },
    addEventListener(ev, fn) { listeners[ev] = fn; },
  };
  sandbox.fireClick = function (target) { if (listeners.click) listeners.click({ target: target }); };
  sandbox.window = sandbox;
  sandbox.ConcordApi = {
    get(url) {
      if (/priorities$/.test(url)) {
        if (mode === 'error') return Promise.reject(new Error(HOSTILE_ERROR));
        if (mode === 'empty') return Promise.resolve([]);
        return Promise.resolve([{
          id: '9', title: HOSTILE_TITLE, body: HOSTILE_BODY, elo_r: 1500, elo_rd: 350,
          pain_score: 8, strategic_weight: 2, priority_score: 4,
        }]);
      }
      return Promise.resolve([{ feature_id: '9', votes_cast: 12, skips: 3 }]);
    },
  };
  vm.createContext(sandbox);
  return sandbox;
}

(async function () {
  const root = element('div');
  root.setAttribute('id', 'ranking-root');
  root.setAttribute('data-can-moderate', 'true');
  const sandbox = makeSandbox(root, 'list');
  vm.runInContext(domSource, sandbox, { filename: domPath });
  vm.runInContext(pageSource, sandbox, { filename: pagePath });
  for (let i = 0; i < 4; i++) await new Promise(function (resolve) { setImmediate(resolve); });

  assert.ok(!root._htmlWrites || root._htmlWrites.length === 0,
    'ranking.js wrote to an HTML string sink: ' + JSON.stringify(root._htmlWrites));
  const nodes = walk(root);
  assert.ok(!nodes.some(function (n) {
    return n.nodeType === 1 && ['img', 'script'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
  [HOSTILE_TITLE, HOSTILE_BODY].forEach(function (needle) {
    assert.ok(root.textContent.indexOf(needle) !== -1, 'hostile field was not literal text: ' + needle);
  });
  const mod = nodes.find(function (n) { return n.nodeType === 1 && n.attrs.class === 'rank-moderate'; });
  assert.ok(mod, 'the moderation link disappeared');
  assert.strictEqual(mod.attrs.href,
    '/projects/lorebook/features/9#moderation-panel', 'the moderation href was mangled');
  assert.ok(root.textContent.indexOf('12 (3 skipped)') !== -1, 'the participation count disappeared');
  assert.ok(nodes.some(function (n) { return n.nodeType === 1 && n.attrs.class === 'row-untested'; }),
    'the under-tested row marker disappeared');

  const emptyRoot = element('div');
  emptyRoot.setAttribute('id', 'ranking-root');
  const emptySandbox = makeSandbox(emptyRoot, 'empty');
  vm.runInContext(domSource, emptySandbox, { filename: domPath });
  vm.runInContext(pageSource, emptySandbox, { filename: pagePath });
  for (let i = 0; i < 4; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  assert.ok(emptyRoot.textContent.indexOf('Nothing to rank yet') !== -1, 'the empty state disappeared');
  assert.ok(!(emptyRoot._htmlWrites || []).length, 'the empty state used a string sink');

  const errorRoot = element('div');
  errorRoot.setAttribute('id', 'ranking-root');
  const errorSandbox = makeSandbox(errorRoot, 'error');
  vm.runInContext(domSource, errorSandbox, { filename: domPath });
  vm.runInContext(pageSource, errorSandbox, { filename: pagePath });
  for (let i = 0; i < 4; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  assert.ok(!(errorRoot._htmlWrites || []).length, 'the error state used a string sink');
  assert.ok(errorRoot.textContent.indexOf(HOSTILE_ERROR) !== -1, 'the hostile error was not literal text');
  const retry = walk(errorRoot).find(function (n) { return n.nodeType === 1 && n.attrs.id === 'ranking-retry'; });
  assert.ok(retry, 'the retry control disappeared');
  errorSandbox.fireClick(retry);
  for (let i = 0; i < 4; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  assert.ok(!(errorRoot._htmlWrites || []).length, 'the retry path used a string sink');

  const sink = /(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/;
  assert.ok(!sink.test(pageSource), 'ranking.js regained an HTML string sink');
  console.log('dom ranking page: PASS — hostile fields and error are text, mod href verbatim, no string sink');
})().catch(function (err) {
  console.error('dom ranking page: FAIL — ' + err.message);
  process.exitCode = 1;
});
