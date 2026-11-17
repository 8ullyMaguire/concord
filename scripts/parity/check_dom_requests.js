#!/usr/bin/env node
// Step 2b: executes the shipped request page with hostile request/answer/tag/project fields and a hostile server refusal.
// Every field must be a text node or a setAttribute value; no HTML string sink may be used.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/requests.js';
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
  const tag = /^[a-z]+$/.test(sel) ? sel : null;
  return walk(root).filter(function (n) {
    if (n.nodeType !== 1) return false;
    if (eq) return n.attrs[eq[1]] === eq[2];
    if (attr) return Object.prototype.hasOwnProperty.call(n.attrs, attr[1]);
    if (id) return n.attrs.id === id[1];
    return n.tagName === (tag || sel);
  });
}
function element(tag) {
  return {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {}, value: '',
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    removeAttribute(k) { delete this.attrs[k]; },
    appendChild(c) { this.children.push(c); return c; },
    replaceChildren() { this.children = Array.prototype.slice.call(arguments); },
    addEventListener(ev, fn) { this.listeners[ev] = fn; },
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

const B = function (s) { return '<b>' + s + '</b>'; };
const HOSTILE_TITLE = B('request title');
const HOSTILE_BODY = '<img src=x onerror=alert(1)>';
const HOSTILE_ANSWER = '<script>alert(2)</script>';
const HOSTILE_TAG = B('tag');
const HOSTILE_PROJECT = B('project name');
const HOSTILE_ERROR = '<script>alert(3)</script>';

function makeSandbox(root, slugEl) {
  const sandbox = {
    console, Date, Math, Promise, Object, Array, String, Number, parseInt, isNaN, isFinite, setTimeout,
    location: { pathname: '/projects/lorebook/requests/7' },
  };
  sandbox.document = {
    createElement: element,
    createTextNode: textNode,
    getElementById(id) { return id === 'request-root' ? root : null; },
    querySelector(sel) { return sel === '[data-slug]' ? slugEl : null; },
    addEventListener() {},
  };
  sandbox.window = sandbox;
  sandbox.ConcordMe = { id: 5, project_ids: [] };
  sandbox.ConcordApi = {
    get(url) {
      if (/\/api\/v1\/projects$/.test(url)) {
        return Promise.resolve([{ id: 3, name: HOSTILE_PROJECT, slug: 'lorebook' }]);
      }
      if (/\/answers$/.test(url)) {
        return Promise.resolve([
          { id: 11, project_id: 3, body: HOSTILE_ANSWER, status: 'active', elo_r: 1600, elo_rd: 350, created_at: 1700000000 },
          { id: 12, project_id: 4, body: 'a second answer', status: 'active', elo_r: 1500, elo_rd: 40, created_at: 1700000001 },
        ]);
      }
      if (/\/tags$/.test(url)) return Promise.resolve({ tags: [HOSTILE_TAG] });
      return Promise.resolve({
        id: 7, title: HOSTILE_TITLE, body: HOSTILE_BODY, status: 'open',
        project_id: 3, accepted_answer_id: 11, created_at: 1700000000,
      });
    },
    post() {
      const err = new Error(HOSTILE_ERROR);
      err.status = 422;
      return Promise.reject(err);
    },
  };
  vm.createContext(sandbox);
  return sandbox;
}

(async function () {
  const root = element('div');
  root.setAttribute('id', 'request-root');
  const slugEl = element('span');
  slugEl.textContent = 'lorebook';
  const sandbox = makeSandbox(root, slugEl);
  vm.runInContext(domSource, sandbox, { filename: domPath });
  vm.runInContext(pageSource, sandbox, { filename: pagePath });
  for (let i = 0; i < 6; i++) await new Promise(function (resolve) { setImmediate(resolve); });

  assert.ok(!root._htmlWrites || root._htmlWrites.length === 0,
    'requests.js wrote to an HTML string sink: ' + JSON.stringify(root._htmlWrites));
  let nodes = walk(root);
  assert.ok(!nodes.some(function (n) {
    return n.nodeType === 1 && ['img', 'script', 'b'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
  [HOSTILE_TITLE, HOSTILE_BODY, HOSTILE_ANSWER, HOSTILE_TAG, HOSTILE_PROJECT].forEach(function (needle) {
    assert.ok(root.textContent.indexOf(needle.replace(/<[^>]+>/g, '')) !== -1 ||
      root.textContent.indexOf(needle) !== -1, 'hostile field was not literal text: ' + needle);
  });
  const projectLink = nodes.find(function (n) { return n.nodeType === 1 && n.tagName === 'a'; });
  assert.ok(projectLink && projectLink.attrs.href === '/projects/lorebook', 'the project answer link was mangled');
  assert.ok(find(root, '[data-answer-id]').length === 2, 'the answer cards disappeared');
  assert.ok(find(root, '[data-compare-form]').length === 1, 'the comparison form disappeared');
  assert.ok(nodes.some(function (n) {
    return n.nodeType === 1 && n.attrs.class === 'badge badge-affiliated' &&
      (n.attrs.title || '').indexOf('weight is unchanged') !== -1;
  }), 'the affiliation label lost its verbatim title');
  assert.ok(nodes.some(function (n) { return n.nodeType === 1 && n.attrs.class === 'badge badge-accepted'; }),
    'the accepted badge disappeared');

  // Exercise the post refusal path with a hostile server message.
  const form = find(root, '[data-answer-form]')[0];
  assert.ok(form, 'the answer form disappeared');
  const textarea = find(form, 'textarea')[0];
  textarea.value = 'a fit rationale';
  form.listeners.submit({ preventDefault() {} });
  for (let i = 0; i < 4; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  nodes = walk(root);
  assert.ok(root.textContent.indexOf(HOSTILE_ERROR) !== -1, 'the hostile refusal was not literal text');
  assert.ok(!nodes.some(function (n) { return n.nodeType === 1 && n.tagName === 'script'; }),
    'the hostile refusal was parsed into an element');
  assert.ok(!(root._htmlWrites || []).length, 'the refusal path used a string sink');

  const sink = /(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/;
  assert.ok(!sink.test(pageSource), 'requests.js regained an HTML string sink');
  console.log('dom requests page: PASS — hostile fields and refusal are text, answer/compare controls kept, no string sink');
})().catch(function (err) {
  console.error('dom requests page: FAIL — ' + err.message);
  process.exitCode = 1;
});
