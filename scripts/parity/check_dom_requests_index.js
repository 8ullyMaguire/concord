#!/usr/bin/env node
// Step 2b: executes the shipped request index with hostile titles, both sources, a duplicate id, and an empty list.
// Every field must be a text node or a setAttribute value; no HTML string sink may be used.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/requests-index.js';
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
function find(root, cls) {
  return walk(root).filter(function (n) {
    return n.nodeType === 1 && (n.attrs.class || '').split(/\s+/).indexOf(cls) !== -1;
  });
}
function element(tag) {
  return {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {},
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    removeAttribute(k) { delete this.attrs[k]; },
    appendChild(c) { this.children.push(c); return c; },
    replaceChildren() { this.children = Array.prototype.slice.call(arguments); },
    addEventListener() {},
    querySelector() { return null; },
    get textContent() {
      return this.children.map(function (c) { return c.nodeType === 3 ? c.text : c.textContent; }).join('');
    },
    set textContent(v) { this.children = [textNode(v)]; },
    set innerHTML(v) { this._htmlWrites = (this._htmlWrites || []).concat(String(v)); },
    insertAdjacentHTML(pos, v) { this._htmlWrites = (this._htmlWrites || []).concat(String(v)); },
  };
}

const HOSTILE_TITLE = '<img src=x onerror=alert(1)>';
const HOSTILE_SHARED = '<script>alert(2)</script>';

function makeSandbox(root, slugEl, empty) {
  const sandbox = { console, Date, Math, Promise, Object, Array, String, Number, setTimeout, encodeURIComponent, decodeURIComponent };
  sandbox.document = {
    createElement: element,
    createTextNode: textNode,
    getElementById(id) { return id === 'requests-root' ? root : null; },
    querySelector(sel) { return sel === '[data-slug]' ? slugEl : null; },
  };
  sandbox.window = sandbox;
  sandbox.ConcordApi = {
    get(url) {
      if (empty) return Promise.resolve([]);
      if (/\/api\/v1\/requests$/.test(url)) {
        return Promise.resolve([
          // Duplicate of the project-scoped row; must be de-duplicated by id.
          { id: 7, title: HOSTILE_TITLE, status: 'open', project_id: 2, created_at: 1700000003 },
          { id: 9, title: HOSTILE_SHARED, status: 'open', created_at: 1700000001 },
        ]);
      }
      return Promise.resolve([
        { id: 7, title: HOSTILE_TITLE, status: 'open', project_id: 2, created_at: 1700000003 },
        { id: 8, title: 'settled one', status: 'answered', project_id: 2, created_at: 1700000002, accepted_answer_id: 4 },
      ]);
    },
  };
  vm.createContext(sandbox);
  return sandbox;
}

(async function () {
  const root = element('div');
  const slugEl = element('span');
  slugEl.textContent = 'lorebook';
  const sandbox = makeSandbox(root, slugEl, false);
  vm.runInContext(domSource, sandbox, { filename: domPath });
  vm.runInContext(pageSource, sandbox, { filename: pagePath });
  for (let i = 0; i < 5; i++) await new Promise(function (resolve) { setImmediate(resolve); });

  assert.ok(!root._htmlWrites || root._htmlWrites.length === 0,
    'requests-index.js wrote to an HTML string sink: ' + JSON.stringify(root._htmlWrites));
  const nodes = walk(root);
  assert.ok(!nodes.some(function (n) {
    return n.nodeType === 1 && ['img', 'script'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
  [HOSTILE_TITLE, HOSTILE_SHARED].forEach(function (needle) {
    assert.ok(root.textContent.indexOf(needle) !== -1, 'hostile title was not literal text: ' + needle);
  });
  const links = nodes.filter(function (n) { return n.nodeType === 1 && n.tagName === 'a'; });
  assert.strictEqual(links.length, 3, 'the duplicate id was not de-duplicated');
  assert.ok(links.every(function (a) { return (a.attrs.href || '').indexOf('/projects/lorebook/requests/') === 0; }),
    'a request href was mangled');
  const groups = find(root, 'entry-group');
  assert.strictEqual(groups.length, 2, 'the open/settled groups disappeared');
  assert.ok(groups[0].textContent.indexOf('open') !== -1 && groups[1].textContent.indexOf('settled') !== -1,
    'open was not ordered before settled');
  assert.ok(nodes.some(function (n) { return n.nodeType === 1 && n.attrs.class === 'badge badge-instance'; }),
    'the instance-wide badge disappeared');
  assert.ok(nodes.some(function (n) { return n.nodeType === 1 && n.attrs.class === 'badge badge-green'; }),
    'the accepted-answer badge disappeared');

  const emptyRoot = element('div');
  const emptySandbox = makeSandbox(emptyRoot, slugEl, true);
  vm.runInContext(domSource, emptySandbox, { filename: domPath });
  vm.runInContext(pageSource, emptySandbox, { filename: pagePath });
  for (let i = 0; i < 5; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  assert.ok(emptyRoot.textContent.indexOf('No requests here') !== -1, 'the empty state disappeared');
  assert.ok(!(emptyRoot._htmlWrites || []).length, 'the empty state used a string sink');
  assert.ok(!emptyRoot.attrs['aria-busy'], 'the empty state left aria-busy set');

  const sink = /(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/;
  assert.ok(!sink.test(pageSource), 'requests-index.js regained an HTML string sink');
  console.log('dom requests index: PASS — hostile titles are text, dedupe and groups kept, no string sink');
})().catch(function (err) {
  console.error('dom requests index: FAIL — ' + err.message);
  process.exitCode = 1;
});
