#!/usr/bin/env node
// Step 2b: executes the shipped comment thread with hostile comment bodies, labels, author ids and a hostile server refusal.
// Every field must be a text node or a setAttribute value; no HTML string sink may be used, including on the reply/fail paths.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/comments.js';
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
function find(root, sel) {
  const eq = /^\[([^=\]]+)="([^"]+)"\]$/.exec(sel);
  const attr = /^\[(.+)\]$/.exec(sel);
  const id = /^#(.+)$/.exec(sel);
  const cls = /^\.(.+)$/.exec(sel);
  return walk(root).filter(function (n) {
    if (n.nodeType !== 1) return false;
    if (eq) return n.attrs[eq[1]] === eq[2];
    if (attr) return Object.prototype.hasOwnProperty.call(n.attrs, attr[1]);
    if (id) return n.attrs.id === id[1];
    if (cls) return (n.attrs.class || '').split(/\s+/).indexOf(cls[1]) !== -1;
    return n.tagName === sel;
  });
}
function element(tag) {
  return {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {}, value: '', disabled: false,
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
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

const HOSTILE_BODY = '<img src=x onerror=alert(1)>';
const HOSTILE_LABEL = '<b>question</b>';
const HOSTILE_AUTHOR = '<script>alert(2)</script>';
const HOSTILE_ID = '7" onmouseover="evil()';
const HOSTILE_ERROR = '<script>alert(3)</script>';

function makeSandbox(root, slugEl, failingPost) {
  const sandbox = {
    console, Date, Math, Promise, Object, Array, String, Number, parseInt, isNaN, setTimeout,
    encodeURIComponent, decodeURIComponent,
  };
  sandbox.document = {
    getElementById(id) { return id === 'comment-thread' ? root : null; },
    querySelector(sel) { return sel === '[data-slug]' ? slugEl : null; },
    createElement: element,
    createTextNode: textNode,
    addEventListener() {},
  };
  sandbox.window = sandbox;
  sandbox.ConcordApi = {
    get() {
      return Promise.resolve([
        {
          id: HOSTILE_ID, author_id: HOSTILE_AUTHOR, label: HOSTILE_LABEL,
          body: HOSTILE_BODY, score: 3, created_at: 1700000000,
        },
        {
          id: 'child', parent_id: HOSTILE_ID, author_id: '9', body: 'a reply',
          label: null, score: 1, created_at: 1700000001,
        },
      ]);
    },
    post() {
      const err = new Error(HOSTILE_ERROR);
      err.status = 422;
      return failingPost ? Promise.reject(err) : Promise.resolve({});
    },
  };
  vm.createContext(sandbox);
  return sandbox;
}

(async function () {
  const root = element('div');
  root.setAttribute('data-thread-kind', 'feature');
  root.setAttribute('data-thread-id', '5');
  const slugEl = element('span');
  slugEl.textContent = 'lorebook';
  const sandbox = makeSandbox(root, slugEl, true);
  vm.runInContext(domSource, sandbox, { filename: domPath });
  vm.runInContext(pageSource, sandbox, { filename: pagePath });
  for (let i = 0; i < 4; i++) await new Promise(function (resolve) { setImmediate(resolve); });

  assert.ok(!root._htmlWrites || root._htmlWrites.length === 0,
    'comments.js wrote to an HTML string sink: ' + JSON.stringify(root._htmlWrites));
  let nodes = walk(root);
  assert.ok(!nodes.some(function (n) {
    return n.nodeType === 1 && ['img', 'script', 'b'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
  [HOSTILE_BODY, HOSTILE_LABEL, HOSTILE_AUTHOR].forEach(function (needle) {
    assert.ok(root.textContent.indexOf(needle) !== -1, 'hostile field was not literal text: ' + needle);
  });
  const li = nodes.find(function (n) { return n.nodeType === 1 && n.attrs['data-comment-id']; });
  assert.ok(li, 'the comment row disappeared');
  assert.strictEqual(li.attrs['data-comment-id'], HOSTILE_ID, 'the hostile comment id attribute was mangled');
  const reply = find(root, '.js-reply')[0];
  assert.ok(reply, 'the reply control disappeared');
  assert.strictEqual(reply.attrs['data-reply-to'], HOSTILE_ID, 'the hostile reply id was mangled');

  // Exercise the nested reply path: clicking Reply re-renders with the hostile id in a note.
  reply.listeners.click();
  assert.ok(find(root, '.js-cancel-reply').length === 1, 'the reply form did not appear');
  assert.ok(root.textContent.indexOf(HOSTILE_ID) !== -1, 'the reply note lost the hostile id');
  assert.ok(!(root._htmlWrites || []).length, 'the reply path used a string sink');

  // Exercise the failure path with a hostile server message.
  const form = find(root, '[data-comment-form]')[0];
  const textarea = find(form, '#cmt-body')[0];
  const select = find(form, '#cmt-label')[0];
  textarea.value = 'a body';
  select.value = '';
  form.listeners.submit({ preventDefault() {}, target: form });
  for (let i = 0; i < 4; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  nodes = walk(root);
  assert.ok(root.textContent.indexOf(HOSTILE_ERROR) !== -1, 'the hostile server refusal was not literal text');
  assert.ok(!nodes.some(function (n) { return n.nodeType === 1 && n.tagName === 'script'; }),
    'the hostile error was parsed into an element');
  assert.ok(!(root._htmlWrites || []).length, 'the failure path used a string sink');

  const sink = /(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/;
  assert.ok(!sink.test(pageSource), 'comments.js regained an HTML string sink');
  console.log('dom comments page: PASS — hostile fields and refusal are text, reply nesting kept, no string sink');
})().catch(function (err) {
  console.error('dom comments page: FAIL — ' + err.message);
  process.exitCode = 1;
});
