#!/usr/bin/env node
// Step 2b's largest page, tested as behaviour. Executes the shipped dom.js and criteria.js in a fake browser with hostile
// criterion names, feature titles, descriptions, ids and a hostile load error. Every field must land as a text node or a
// setAttribute value; no HTML string sink may be used.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/criteria.js';
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
function textNode(value) {
  const text = String(value);
  return { nodeType: 3, text: text, get textContent() { return text; } };
}
function element(tag) {
  return {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {}, value: '',
    get id() { return this.attrs.id || ''; },
    set id(v) { this.attrs.id = String(v); },
    get checked() { return Object.prototype.hasOwnProperty.call(this.attrs, 'checked'); },
    set checked(v) { if (v) this.attrs.checked = ''; else delete this.attrs.checked; },
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    hasAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k); },
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
    set textContent(v) { this.children = [textNode(v)]; },
    set innerHTML(v) { this._htmlWrites = (this._htmlWrites || []).concat(String(v)); },
    insertAdjacentHTML(pos, v) { this._htmlWrites = (this._htmlWrites || []).concat(String(v)); },
  };
}
function findAll(rootNode, sel) {
  const attrEq = /^\[([^=\]]+)="([^"]+)"\]$/.exec(sel);
  const attr = /^\[(.+)\]$/.exec(sel);
  const id = /^#(.+)$/.exec(sel);
  return walk(rootNode).filter(function (n) {
    return n.nodeType === 1 && (
      (attrEq && n.attrs[attrEq[1]] === attrEq[2]) ||
      (!attrEq && attr && Object.prototype.hasOwnProperty.call(n.attrs, attr[1])) ||
      (id && n.attrs.id === id[1]) ||
      (!attrEq && !attr && !id && n.tagName === sel)
    );
  });
}

const HOSTILE_NAME = '<img src=x onerror=alert(1)>';
const HOSTILE_TITLE = '<script>alert(2)</script>';
const HOSTILE_DESC = '<b>moderator prose</b>';
const HOSTILE_ID = '1" onmouseover="evil()';
const HOSTILE_SLUG = 'x" onfocus="evil()';
const HOSTILE_PROFILE = '<img src=y onerror=alert(3)>';
const HOSTILE_ERROR = '<script>alert(4)</script>';

function makeSandbox(root, notice, failing) {
  const sandbox = {
    console, Date, Math, Promise, Object, Array, String, Number, parseFloat, isNaN, isFinite,
    encodeURIComponent, decodeURIComponent, setTimeout,
    location: { pathname: '/projects/lorebook/criteria' },
  };
  sandbox.document = {
    readyState: 'complete',
    createElement: element,
    createTextNode: textNode,
    getElementById(id) {
      if (id === 'criteria-root') return root;
      if (id === 'criteria-notice') return notice;
      return null;
    },
    querySelector() { return null; },
    addEventListener() {},
  };
  sandbox.window = sandbox;
  sandbox.ConcordApi = {
    // The shipped client unwraps both paged and bare collections and resolves the
    // numeric project id before the rank call; the fake must offer both (D-199).
    list(data) {
      if (Array.isArray(data)) return data;
      if (data && Array.isArray(data.items)) return data.items;
      return [];
    },
    get(url) {
      if (/^\/api\/v1\/projects\/[^/]+$/.test(url)) return Promise.resolve({ id: 11 });
      if (failing && /\/criteria$/.test(url)) return Promise.reject(new Error(HOSTILE_ERROR));
      if (/\/criteria-profiles$/.test(url)) return Promise.resolve([{ slug: HOSTILE_SLUG, name: HOSTILE_PROFILE }]);
      if (/\/priorities$/.test(url)) return Promise.resolve([
        { id: 11, title: HOSTILE_TITLE }, { id: 12, title: 'Second feature' },
      ]);
      return Promise.resolve([{
        id: HOSTILE_ID, slug: HOSTILE_SLUG, name: HOSTILE_NAME, description: HOSTILE_DESC,
        direction: 'lower_is_better', active: false, default_weight: 1.25,
      }]);
    },
    post() {
      return Promise.resolve({ results: [{
        rank: 1, feature_id: 11, score: 0.9, confidence: 12,
        contributions: [{ criterion_id: HOSTILE_ID, normalised: 0.8, games: 5, raw: 1600, rd: 100 }],
      }] });
    },
    request() { return Promise.resolve({}); },
  };
  vm.createContext(sandbox);
  return sandbox;
}

(async function () {
  const root2 = element('div');
  const notice2 = element('p');
  const sandbox2 = makeSandbox(root2, notice2, false);
  vm.runInContext(domSource, sandbox2, { filename: domPath });
  vm.runInContext(pageSource, sandbox2, { filename: pagePath });
  for (let i = 0; i < 3; i++) await new Promise(function (resolve) { setImmediate(resolve); });

  assert.ok(!root2._htmlWrites || root2._htmlWrites.length === 0, 'a string sink was used');
  const nodes = walk(root2);
  assert.ok(!nodes.some(function (n) {
    return n.nodeType === 1 && ['img', 'script', 'b'].indexOf(n.tagName) !== -1;
  }), 'hostile prose was parsed into an element');
  const text = root2.textContent;
  [HOSTILE_NAME, HOSTILE_TITLE, HOSTILE_DESC, HOSTILE_SLUG, HOSTILE_PROFILE].forEach(function (needle) {
    assert.ok(text.indexOf(needle) !== -1, 'hostile field was not literal text: ' + needle);
  });
  const weight = root2.querySelector('[data-weight]');
  assert.ok(weight, 'the weight input disappeared');
  assert.strictEqual(weight.attrs['data-weight'], HOSTILE_ID, 'the hostile criterion id attribute was mangled');
  const voteA = root2.querySelector('[data-vote-a]');
  assert.ok(voteA && voteA.attrs['data-vote-a'] === HOSTILE_ID, 'the vote selector lost its criterion id');
  assert.ok(root2.querySelectorAll('select').length >= 4, 'the controls lost their selects');
  assert.ok(root2.querySelectorAll('option').length >= 8, 'the option lists disappeared');
  assert.ok(root2.querySelector('[data-create]'), 'the create button disappeared');
  assert.ok(root2.querySelector('[data-rank]'), 'the rank button disappeared');
  assert.ok(root2.querySelectorAll('td').length >= 4, 'the comparison table lost its cells');

  // The load-error path: the server's hostile message must be a text node too.
  const root3 = element('div');
  const notice3 = element('p');
  const sandbox3 = makeSandbox(root3, notice3, true);
  vm.runInContext(domSource, sandbox3, { filename: domPath });
  vm.runInContext(pageSource, sandbox3, { filename: pagePath });
  for (let i = 0; i < 4; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  assert.ok(!root3._htmlWrites || root3._htmlWrites.length === 0, 'the error path used a string sink');
  assert.ok(root3.textContent.indexOf(HOSTILE_ERROR) !== -1, 'the hostile error was not literal text');
  assert.ok(!walk(root3).some(function (n) { return n.nodeType === 1 && n.tagName === 'script'; }),
    'the hostile error was parsed into an element');

  const sink = /(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/;
  assert.ok(!sink.test(pageSource), 'criteria.js regained an HTML string sink');
  console.log('dom criteria page: PASS — hostile fields and error are text, attributes verbatim, no string sink');
})().catch(function (err) {
  console.error('dom criteria page: FAIL — ' + err.message);
  process.exitCode = 1;
});
