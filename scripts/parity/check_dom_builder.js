#!/usr/bin/env node
// Step 2b's renderer, tested as behaviour rather than as a grep. This executes the SHIPPED dom.js and notifications.js in
// a fake browser and hands the page a notification whose prose is an XSS payload. The assertion is about the resulting
// DOM tree: hostile text must be a text node, hostile attribute values must be set as attributes, and nothing anywhere may
// write to innerHTML. A future page that reintroduces a string sink reddens this arm even if it renames its helper.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/notifications.js';
const domSource = fs.readFileSync(domPath, 'utf8');
const pageSource = fs.readFileSync(pagePath, 'utf8');

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
    querySelector(sel) {
      const key = sel.slice(1, -1);
      return walk(this).find(function (n) { return n.nodeType === 1 && Object.prototype.hasOwnProperty.call(n.attrs, key); }) || null;
    },
    querySelectorAll(sel) {
      const key = sel.slice(1, -1);
      return walk(this).filter(function (n) { return n.nodeType === 1 && Object.prototype.hasOwnProperty.call(n.attrs, key); });
    },
    get textContent() {
      return this.children.map(function (c) { return c.nodeType === 3 ? c.text : c.textContent; }).join('');
    },
    set innerHTML(v) { this._htmlWrites = (this._htmlWrites || []).concat(String(v)); },
  };
}
function textNode(value) {
  const text = String(value);
  return { nodeType: 3, text: text, get textContent() { return text; } };
}

const HOSTILE = '<img src=x onerror=alert(1)>';
const HOSTILE_ID = 'x" onmouseover="evil()';
const root = element('div');

const sandbox = { console, Date, Promise, encodeURIComponent, URLSearchParams, setTimeout };
sandbox.document = {
  readyState: 'complete',
  createElement: element,
  createTextNode: textNode,
  getElementById(id) { return id === 'notifications-root' ? root : null; },
  addEventListener() {},
};
sandbox.window = sandbox;
sandbox.ConcordApi = {
  get() {
    return Promise.resolve({
      unread: 1,
      notifications: [{
        id: HOSTILE_ID, kind: 'feature_removed', title: HOSTILE,
        reason: '<script>alert(2)</script>', actor: '<b>mod</b>',
        project_slug: 'lorebook', created_at: 1700000000, read: false,
      }],
    });
  },
  post() { return Promise.resolve({}); },
};
vm.createContext(sandbox);
vm.runInContext(domSource, sandbox, { filename: domPath });

// The helper alone: text is a text node, attributes are attributes, no HTML parsing.
assert.strictEqual(typeof sandbox.ConcordDom.el, 'function', 'dom.js did not export ConcordDom.el');
const standalone = sandbox.ConcordDom.el('p', { class: 'hostile', 'data-x': HOSTILE_ID }, HOSTILE);
assert.strictEqual(standalone.attrs.class, 'hostile', 'el() dropped a class attribute');
assert.strictEqual(standalone.attrs['data-x'], HOSTILE_ID, 'el() mangled an attribute value');
assert.strictEqual(standalone.children.length, 1, 'el() did not create exactly one text child');
assert.strictEqual(standalone.children[0].nodeType, 3, 'el() emitted an element instead of a text node');
assert.strictEqual(standalone.textContent, HOSTILE, 'el() changed the hostile text');

vm.runInContext(pageSource, sandbox, { filename: pagePath });

(async function () {
  await new Promise(function (resolve) { setImmediate(resolve); });
  await new Promise(function (resolve) { setImmediate(resolve); });

  assert.ok(!root._htmlWrites || root._htmlWrites.length === 0,
    'notifications.js wrote to innerHTML: ' + JSON.stringify(root._htmlWrites));
  const nodes = walk(root);
  assert.ok(!nodes.some(function (n) { return n.nodeType === 1 && (n.tagName === 'img' || n.tagName === 'script' || n.tagName === 'b'); }),
    'the page parsed hostile prose into an element');
  assert.ok(root.textContent.indexOf(HOSTILE) !== -1, 'hostile title was not rendered as literal text');
  assert.ok(root.textContent.indexOf('<script>alert(2)</script>') !== -1, 'hostile reason was not rendered as literal text');
  assert.ok(root.textContent.indexOf('<b>mod</b>') !== -1, 'hostile actor was not rendered as literal text');
  const card = nodes.find(function (n) { return n.nodeType === 1 && n.tagName === 'li'; });
  assert.ok(card, 'the page rendered no notification card');
  assert.strictEqual(card.attrs['data-id'], HOSTILE_ID, 'the id attribute was not set verbatim');
  assert.ok(nodes.some(function (n) { return n.nodeType === 1 && Object.prototype.hasOwnProperty.call(n.attrs, 'data-read-all'); }),
    'the mark-all control disappeared');
  assert.ok(nodes.some(function (n) { return n.nodeType === 1 && Object.prototype.hasOwnProperty.call(n.attrs, 'data-read'); }),
    'the per-card mark-read control disappeared');
  const sink = /\.innerHTML\s*=/;
  assert.ok(!sink.test(domSource) && !sink.test(pageSource),
    'a string sink came back into dom.js or notifications.js');

  console.log('dom builder: PASS — hostile prose is text, attributes are set, no innerHTML sink');
})().catch(function (err) {
  console.error('dom builder: FAIL — ' + err.message);
  process.exitCode = 1;
});
