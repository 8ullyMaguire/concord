#!/usr/bin/env node
// Step 2b: executes the shipped audit page with hostile actor names, detail reasons, action/entity values and a hostile
// load error. Every field must be a text node or a setAttribute value; no HTML string sink may be used.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/audit.js';
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
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {}, value: '', disabled: false,
    get id() { return this.attrs.id || ''; },
    set id(v) { this.attrs.id = String(v); },
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    hasAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k); },
    appendChild(c) { this.children.push(c); return c; },
    replaceChildren() { this.children = Array.prototype.slice.call(arguments); },
    addEventListener(ev, fn) { this.listeners[ev] = fn; },
    focus() {},
    querySelector(sel) { return find(this, sel)[0] || null; },
    querySelectorAll(sel) { return find(this, sel); },
    get textContent() {
      return this.children.map(function (c) { return c.nodeType === 3 ? c.text : c.textContent; }).join('');
    },
    set textContent(v) { this.children = [textNode(v)]; },
    set innerHTML(v) { this._htmlWrites = (this._htmlWrites || []).concat(String(v)); },
    insertAdjacentHTML(pos, v) { this._htmlWrites = (this._htmlWrites || []).concat(String(v)); },
  };
}
function find(root, sel) {
  const eq = /^\[([^=\]]+)="([^"]+)"\]$/.exec(sel);
  const attr = /^\[(.+)\]$/.exec(sel);
  const id = /^#(.+)$/.exec(sel);
  return walk(root).filter(function (n) {
    return n.nodeType === 1 && (
      (eq && n.attrs[eq[1]] === eq[2]) ||
      (!eq && attr && Object.prototype.hasOwnProperty.call(n.attrs, attr[1])) ||
      (id && n.attrs.id === id[1]) ||
      (!eq && !attr && !id && n.tagName === sel)
    );
  });
}

const HOSTILE_ACTOR = '<img src=x onerror=alert(1)>';
const HOSTILE_REASON = '<script>alert(2)</script>';
const HOSTILE_ACTION = '<b>moderation</b>';
const HOSTILE_ENTITY = 'project <i>7</i>';
const HOSTILE_ID = '9" onmouseover="evil()';
const HOSTILE_ERROR = '<script>alert(3)</script>';

function makeSandbox(root, failing) {
  const sandbox = {
    console, Date, Math, Promise, Object, Array, String, Number, parseFloat, isNaN, isFinite, JSON,
    encodeURIComponent, decodeURIComponent, setTimeout,
    location: { pathname: '/projects/lorebook/audit' },
  };
  sandbox.document = {
    readyState: 'complete',
    createElement: element,
    createTextNode: textNode,
    getElementById(id) { return id === 'audit-root' ? root : null; },
    querySelector(sel) { return find(root, sel)[0] || null; },
    addEventListener() {},
  };
  sandbox.window = sandbox;
  sandbox.ConcordApi = {
    get(url) {
      if (failing) return Promise.reject(new Error(HOSTILE_ERROR));
      if (/\/audit\/actions$/.test(url)) return Promise.resolve([HOSTILE_ACTION]);
      return Promise.resolve({
        total: 1,
        entries: [{
          id: HOSTILE_ID, created_at: 1700000000, action: HOSTILE_ACTION, entity: HOSTILE_ENTITY,
          actor_display_name: HOSTILE_ACTOR,
          detail: JSON.stringify({ reason: HOSTILE_REASON, prev: { a: 1 } }),
          can_undo: true, undone: true, undoes: '5',
        }],
      });
    },
    post() { return Promise.resolve({}); },
  };
  vm.createContext(sandbox);
  return sandbox;
}

(async function () {
  const root = element('div');
  const sandbox = makeSandbox(root, false);
  vm.runInContext(domSource, sandbox, { filename: domPath });
  vm.runInContext(pageSource, sandbox, { filename: pagePath });
  for (let i = 0; i < 4; i++) await new Promise(function (resolve) { setImmediate(resolve); });

  assert.ok(!root._htmlWrites || root._htmlWrites.length === 0,
    'audit.js wrote to an HTML string sink: ' + JSON.stringify(root._htmlWrites));
  const nodes = walk(root);
  assert.ok(!nodes.some(function (n) {
    return n.nodeType === 1 && ['img', 'script', 'b', 'i'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
  const text = root.textContent;
  [HOSTILE_ACTOR, HOSTILE_REASON, HOSTILE_ACTION, HOSTILE_ENTITY].forEach(function (needle) {
    assert.ok(text.indexOf(needle) !== -1, 'hostile field was not literal text: ' + needle);
  });
  const undo = nodes.find(function (n) { return n.nodeType === 1 && n.attrs['data-undo']; });
  assert.ok(undo, 'the undo control disappeared');
  assert.strictEqual(undo.attrs['data-undo'], HOSTILE_ID, 'the hostile audit id attribute was mangled');
  assert.ok(nodes.some(function (n) {
    return n.nodeType === 1 && Object.prototype.hasOwnProperty.call(n.attrs, 'data-audit-count');
  }),
    'the count line disappeared');
  assert.ok(nodes.some(function (n) { return n.nodeType === 1 && n.tagName === 'strong'; }),
    'the count line lost its emphasis nodes');

  const root2 = element('div');
  const sandbox2 = makeSandbox(root2, true);
  vm.runInContext(domSource, sandbox2, { filename: domPath });
  vm.runInContext(pageSource, sandbox2, { filename: pagePath });
  for (let i = 0; i < 4; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  assert.ok(!root2._htmlWrites || root2._htmlWrites.length === 0, 'the error path used a string sink');
  assert.ok(root2.textContent.indexOf(HOSTILE_ERROR) !== -1, 'the hostile error was not literal text');
  assert.ok(!walk(root2).some(function (n) { return n.nodeType === 1 && n.tagName === 'script'; }),
    'the hostile error was parsed into an element');

  const sink = /(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/;
  assert.ok(!sink.test(pageSource), 'audit.js regained an HTML string sink');
  console.log('dom audit page: PASS — hostile fields and error are text, attributes verbatim, no string sink');
})().catch(function (err) {
  console.error('dom audit page: FAIL — ' + err.message);
  process.exitCode = 1;
});
