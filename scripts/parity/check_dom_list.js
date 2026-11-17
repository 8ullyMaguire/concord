#!/usr/bin/env node
// Step 2b's second page, tested as behaviour. This executes the shipped dom.js and list.js in a fake browser with hostile
// API fields and a hostile API error, then inspects the resulting tree. The page has no innerHTML or insertAdjacentHTML
// sink to hide behind: every field must be a text node or an attribute value.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/list.js';
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
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {}, value: '',
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    removeAttribute(k) { delete this.attrs[k]; },
    appendChild(c) { this.children.push(c); return c; },
    replaceChildren() { this.children = Array.prototype.slice.call(arguments); },
    prepend() { this.children = Array.prototype.slice.call(arguments).concat(this.children); },
    addEventListener(ev, fn) { this.listeners[ev] = fn; },
    querySelector(sel) { return findAll(this, sel)[0] || null; },
    querySelectorAll(sel) { return findAll(this, sel); },
    get textContent() {
      return this.children.map(function (c) { return c.nodeType === 3 ? c.text : c.textContent; }).join('');
    },
    set innerHTML(v) { this._htmlWrites = (this._htmlWrites || []).concat(String(v)); },
    insertAdjacentHTML(pos, v) { this._htmlWrites = (this._htmlWrites || []).concat(String(v)); },
  };
}
function findAll(rootNode, sel) {
  const attr = /^\[(.+)\]$/.exec(sel);
  const id = /^#(.+)$/.exec(sel);
  return walk(rootNode).filter(function (n) {
    return n.nodeType === 1 && (
      (attr && Object.prototype.hasOwnProperty.call(n.attrs, attr[1])) ||
      (id && n.attrs.id === id[1]) ||
      (!attr && !id && n.tagName === sel)
    );
  });
}
function textNode(value) {
  const text = String(value);
  return { nodeType: 3, text: text, get textContent() { return text; } };
}

const HOSTILE = '<img src=x onerror=alert(1)>';
const HOSTILE_URL = 'javascript:alert(2)";<b>x</b>';
const HOSTILE_ID = '7" onmouseover="evil()';
const root = element('div');
const slug = element('span');
slug.children.push(textNode('lorebook '));

const sandbox = {
  console, Date, Math, Promise, parseInt, isFinite, encodeURIComponent, setTimeout,
  location: { pathname: '/projects/lorebook/lists/7' },
};
sandbox.document = {
  readyState: 'complete',
  createElement: element,
  createTextNode: textNode,
  getElementById(id) { return id === 'list-root' ? root : null; },
  querySelector(sel) { return sel === '[data-slug]' ? slug : null; },
  addEventListener() {},
};
sandbox.window = sandbox;
sandbox.ConcordApi = {
  get(url) {
    if (/\/entries$/.test(url)) {
      return Promise.resolve([{
        id: HOSTILE_ID, title: HOSTILE, url: HOSTILE_URL, description: '<script>alert(3)</script>',
        category: '<b>cat</b>', status: 'open', elo_r: 1600, elo_rd: 350, created_at: 1700000000,
      }, {
        id: '8', title: 'Second', url: 'https://example.test', description: '', category: 'books',
        status: 'closed', elo_r: 1400, elo_rd: 100, created_at: 1700000001,
      }]);
    }
    return Promise.resolve({
      name: '<b>hostile list</b>', description: '<img src=y onerror=alert(4)>',
      status: 'open', project_id: 0,
    });
  },
  post() { return Promise.reject({ status: 400, message: '<script>alert(5)</script>' }); },
};
vm.createContext(sandbox);
vm.runInContext(domSource, sandbox, { filename: domPath });
vm.runInContext(pageSource, sandbox, { filename: pagePath });

(async function () {
  await new Promise(function (resolve) { setImmediate(resolve); });
  await new Promise(function (resolve) { setImmediate(resolve); });

  assert.ok(!root._htmlWrites || root._htmlWrites.length === 0,
    'list.js wrote to an HTML string sink: ' + JSON.stringify(root._htmlWrites));
  const nodes = walk(root);
  assert.ok(!nodes.some(function (n) {
    return n.nodeType === 1 && ['img', 'script', 'b'].indexOf(n.tagName) !== -1;
  }), 'a hostile API field was parsed into an element');

  const text = root.textContent;
  [HOSTILE, HOSTILE_URL, '<script>alert(3)</script>', '<b>cat</b>',
    '<b>hostile list</b>', '<img src=y onerror=alert(4)>'].forEach(function (needle) {
    assert.ok(text.indexOf(needle) !== -1, 'hostile field was not rendered as literal text: ' + needle);
  });

  const link = nodes.find(function (n) { return n.nodeType === 1 && n.tagName === 'a'; });
  assert.ok(link, 'the entry link disappeared');
  assert.strictEqual(link.attrs.href, HOSTILE_URL, 'the URL attribute was mangled');
  const row = nodes.find(function (n) { return n.nodeType === 1 && n.attrs['data-entry-id']; });
  assert.strictEqual(row.attrs['data-entry-id'], HOSTILE_ID, 'the id attribute was mangled');
  const form = root.querySelector('[data-compare-form]');
  assert.ok(form, 'the comparison form disappeared');
  assert.strictEqual(root.querySelectorAll('select').length, 3, 'the three selects disappeared');
  assert.strictEqual(root.querySelectorAll('option').length, 9, 'the option list changed');

  // Drive the error path with a hostile API message: it must become a text node too.
  form.querySelector('#cmp-a').value = '7';
  form.querySelector('#cmp-b').value = '8';
  form.querySelector('#cmp-outcome').value = 'a';
  form.listeners.submit({ preventDefault() {} });
  await new Promise(function (resolve) { setImmediate(resolve); });
  await new Promise(function (resolve) { setImmediate(resolve); });

  assert.ok(!root._htmlWrites || root._htmlWrites.length === 0,
    'the error path regained an HTML string sink: ' + JSON.stringify(root._htmlWrites));
  assert.ok(!walk(root).some(function (n) { return n.nodeType === 1 && n.tagName === 'script'; }),
    'the hostile error message was parsed into an element');
  assert.ok(root.textContent.indexOf('<script>alert(5)</script>') !== -1,
    'the hostile error message was not rendered as literal text');

  const sink = /(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/;
  assert.ok(!sink.test(pageSource), 'list.js regained an HTML string sink');
  console.log('dom list page: PASS — hostile fields and error are text, attributes verbatim, no string sink');
})().catch(function (err) {
  console.error('dom list page: FAIL — ' + err.message);
  process.exitCode = 1;
});
