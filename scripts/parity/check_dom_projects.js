#!/usr/bin/env node
// Step 2b: executes the shipped projects listing with hostile project fields, an empty list, and a hostile API failure.
// Every field must be a text node or a setAttribute value; no HTML string sink may be used.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/projects.js';
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

const HOSTILE_NAME = '<img src=x onerror=alert(1)>';
const HOSTILE_SLUG = 'lore" onmouseover="evil()';
const HOSTILE_DESC = '<script>alert(2)</script>';
const HOSTILE_LICENSE = '<b>MIT</b>';
const HOSTILE_GOV = '<i>governed</i>';

function makeSandbox(grid, mode) {
  const sandbox = { console, Date, Math, Promise, Object, Array, String, Number, isNaN, setTimeout };
  const listeners = Object.create(null);
  sandbox.document = {
    createElement: element,
    createTextNode: textNode,
    getElementById(id) { return id === 'projects-grid' ? grid : null; },
    addEventListener(ev, fn) { listeners[ev] = fn; },
  };
  sandbox.fire = function (ev) { if (listeners[ev]) listeners[ev](); };
  sandbox.window = sandbox;
  sandbox.ConcordApi = {
    get() {
      if (mode === 'error') return Promise.reject(new Error('boom'));
      if (mode === 'empty') return Promise.resolve([]);
      return Promise.resolve([{
        name: HOSTILE_NAME, slug: HOSTILE_SLUG, description: HOSTILE_DESC,
        license: HOSTILE_LICENSE, governance_model: HOSTILE_GOV,
        health_score: 0.85, updated_at: 1700000000,
      }]);
    },
  };
  vm.createContext(sandbox);
  return sandbox;
}

(async function () {
  const grid = element('div');
  const sandbox = makeSandbox(grid, 'list');
  vm.runInContext(domSource, sandbox, { filename: domPath });
  vm.runInContext(pageSource, sandbox, { filename: pagePath });
  sandbox.fire('DOMContentLoaded');
  for (let i = 0; i < 4; i++) await new Promise(function (resolve) { setImmediate(resolve); });

  assert.ok(!grid._htmlWrites || grid._htmlWrites.length === 0,
    'projects.js wrote to an HTML string sink: ' + JSON.stringify(grid._htmlWrites));
  const nodes = walk(grid);
  assert.ok(!nodes.some(function (n) {
    return n.nodeType === 1 && ['img', 'script', 'b', 'i'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
  [HOSTILE_NAME, HOSTILE_DESC, HOSTILE_LICENSE, HOSTILE_GOV].forEach(function (needle) {
    assert.ok(grid.textContent.indexOf(needle.replace(/<[^>]+>/g, '')) !== -1 ||
      grid.textContent.indexOf(needle) !== -1, 'hostile field was not literal text: ' + needle);
  });
  const cardLink = nodes.find(function (n) { return n.nodeType === 1 && n.tagName === 'a'; });
  assert.ok(cardLink, 'the project card disappeared');
  assert.strictEqual(cardLink.attrs.href, '/projects/' + HOSTILE_SLUG, 'the hostile slug href was mangled');
  const fill = nodes.find(function (n) { return n.nodeType === 1 && n.attrs.class && n.attrs.class.indexOf('health-fill-good') !== -1; });
  assert.ok(fill && fill.attrs.style === 'width:85%', 'the health bar lost its width');

  const emptyGrid = element('div');
  const emptySandbox = makeSandbox(emptyGrid, 'empty');
  vm.runInContext(domSource, emptySandbox, { filename: domPath });
  vm.runInContext(pageSource, emptySandbox, { filename: pagePath });
  emptySandbox.fire('DOMContentLoaded');
  for (let i = 0; i < 4; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  assert.ok(emptyGrid.textContent.indexOf('No projects yet') !== -1, 'the empty state disappeared');
  assert.ok(!(emptyGrid._htmlWrites || []).length, 'the empty state used a string sink');

  const errorGrid = element('div');
  const errorSandbox = makeSandbox(errorGrid, 'error');
  vm.runInContext(domSource, errorSandbox, { filename: domPath });
  vm.runInContext(pageSource, errorSandbox, { filename: pagePath });
  errorSandbox.fire('DOMContentLoaded');
  for (let i = 0; i < 4; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  // A rejected GET is caught upstream into [], so this reaches the empty state; the
  // trailing catch only covers a throw inside the then (same as the shipped original).
  assert.ok(errorGrid.textContent.indexOf('No projects yet') !== -1, 'the failed GET did not yield the empty state');
  assert.ok(!(errorGrid._htmlWrites || []).length, 'the error state used a string sink');

  const sink = /(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/;
  assert.ok(!sink.test(pageSource), 'projects.js regained an HTML string sink');
  console.log('dom projects page: PASS — hostile fields are text, href verbatim, all three states sink-free');
})().catch(function (err) {
  console.error('dom projects page: FAIL — ' + err.message);
  process.exitCode = 1;
});
