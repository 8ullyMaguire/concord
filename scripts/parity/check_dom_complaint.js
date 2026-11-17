#!/usr/bin/env node
// Step 2b: executes the shipped complaint page with hostile complaint and linked-feature fields, and with a hostile load
// error. Every field must be a text node or a verbatim attribute; no HTML string sink may be used.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/complaint.js';
const domSource = fs.readFileSync(domPath, 'utf8');
const pageSource = fs.readFileSync(pagePath, 'utf8');

function textNode(value) {
  const text = String(value);
  return { nodeType: 3, text: text, get textContent() { return text; } };
}
function element(tag) {
  return {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {}, value: '',
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    appendChild(c) { this.children.push(c); return c; },
    replaceChildren() { this.children = Array.prototype.slice.call(arguments); },
    addEventListener(ev, fn) { this.listeners[ev] = fn; },
    querySelector() { return null; },
    querySelectorAll() { return []; },
    get textContent() {
      return this.children.map(function (c) { return c.nodeType === 3 ? c.text : c.textContent; }).join('');
    },
    set innerHTML(v) { this._htmlWrites = (this._htmlWrites || []).concat(String(v)); },
    insertAdjacentHTML(pos, v) { this._htmlWrites = (this._htmlWrites || []).concat(String(v)); },
  };
}
function walk(node, out) {
  out = out || [];
  (node.children || []).forEach(function (child) {
    out.push(child);
    if (child.nodeType === 1) walk(child, out);
  });
  return out;
}

const HOSTILE_TITLE = '<img src=x onerror=alert(1)>';
const HOSTILE_BODY = '<script>alert(2)</script>';
const HOSTILE_FEATURE = '<b>linked feature</b>';
const HOSTILE_ID = '7" onmouseover="evil()';
const HOSTILE_ERROR = '<script>alert(3)</script>';

function makeSandbox(root, failing) {
  const sandbox = {
    console, Date, Math, Promise, Object, Array, String, Number, parseFloat, isNaN, isFinite,
    encodeURIComponent, decodeURIComponent, setTimeout,
    location: { pathname: '/projects/lorebook/complaints/' + HOSTILE_ID },
  };
  sandbox.document = {
    readyState: 'complete',
    createElement: element,
    createTextNode: textNode,
    getElementById(id) { return id === 'complaint-root' ? root : null; },
    querySelector() { return null; },
    addEventListener() {},
  };
  sandbox.window = sandbox;
  sandbox.ConcordApi = {
    get(url) {
      if (failing) return Promise.reject(new Error(HOSTILE_ERROR));
      if (/\/features$/.test(url)) {
        return Promise.resolve([{
          id: '12', title: HOSTILE_FEATURE, project_slug: 'lorebook',
          elo_r: 1600, elo_rd: 350, status: 'open',
        }]);
      }
      return Promise.resolve({
        title: HOSTILE_TITLE, body: HOSTILE_BODY, status: 'validated',
        severity: 4, frequency: 3, strategic_multiplier: 2,
      });
    },
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
    'complaint.js wrote to an HTML string sink: ' + JSON.stringify(root._htmlWrites));
  const nodes = walk(root);
  assert.ok(!nodes.some(function (n) {
    return n.nodeType === 1 && ['img', 'script', 'b'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
  const text = root.textContent;
  [HOSTILE_TITLE, HOSTILE_BODY, HOSTILE_FEATURE].forEach(function (needle) {
    assert.ok(text.indexOf(needle) !== -1, 'hostile field was not literal text: ' + needle);
  });
  const links = nodes.filter(function (n) { return n.nodeType === 1 && n.tagName === 'a'; });
  assert.ok(links.length >= 2, 'the breadcrumb and feature links disappeared');
  assert.ok(links.some(function (a) { return a.attrs.href === '/projects/lorebook/features/12'; }),
    'the feature link href was mangled');
  assert.strictEqual(root.attrs['aria-busy'], 'false', 'the page never cleared its busy state');

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
  assert.ok(!sink.test(pageSource), 'complaint.js regained an HTML string sink');
  console.log('dom complaint page: PASS — hostile fields and error are text, links verbatim, no string sink');
})().catch(function (err) {
  console.error('dom complaint page: FAIL — ' + err.message);
  process.exitCode = 1;
});
