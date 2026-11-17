#!/usr/bin/env node
// Step 2b: executes the shipped taxonomy page with hostile tag/proposal fields and a hostile consent refusal.
// Every field must be a text node or a setAttribute value; no HTML string sink may be used.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/taxonomy.js';
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
  return walk(root).filter(function (n) {
    if (n.nodeType !== 1) return false;
    if (eq) return n.attrs[eq[1]] === eq[2];
    if (attr) return Object.prototype.hasOwnProperty.call(n.attrs, attr[1]);
    if (id) return n.attrs.id === id[1];
    return n.tagName === sel;
  });
}
function element(tag) {
  return {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {}, value: '', disabled: false,
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
const HOSTILE_TAG = B('tag name');
const HOSTILE_NS = B('namespace');
const HOSTILE_REL = '<script>alert(1)</script>';
const HOSTILE_TARGET = B('target');
const HOSTILE_VALUE = '<img src=x onerror=alert(2)>';
const HOSTILE_RATIONALE = B('rationale');
const HOSTILE_ERROR = '<script>alert(3)</script>';

function makeSandbox(root, live, failing) {
  const sandbox = {
    console, Date, Math, Promise, Object, Array, String, Number, setTimeout,
    token() { return 'tok'; },
  };
  sandbox.document = {
    createElement: element,
    createTextNode: textNode,
    getElementById(id) {
      if (id === 'taxonomy-root') return root;
      if (id === 'taxonomy-live') return live;
      return find(root, '#' + id)[0] || null;
    },
    addEventListener() {},
  };
  sandbox.window = sandbox;
  sandbox.ConcordSession = { instance() { return { user: { id: 4 } }; } };
  sandbox.ConcordApi = {
    get(url) {
      if (/taxonomy\/tags/.test(url)) {
        return Promise.resolve([{
          id: 1, name: HOSTILE_TAG, namespace: HOSTILE_NS, relation: HOSTILE_REL,
          suggested: true, project_id: 0,
        }]);
      }
      return Promise.resolve([{
        id: 9, target_tag: HOSTILE_TARGET, value: HOSTILE_VALUE, rationale: HOSTILE_RATIONALE,
        action: 'rename', status: 'pending', proposed_by: 7, project_id: 0,
        consent_count: 1, eligible_count: 3,
      }]);
    },
    post() {
      const err = new Error(HOSTILE_ERROR);
      err.status = 403;
      return failing ? Promise.reject(err) : Promise.resolve({ ratified: false });
    },
  };
  vm.createContext(sandbox);
  return sandbox;
}

(async function () {
  const root = element('div');
  root.setAttribute('id', 'taxonomy-root');
  root.setAttribute('aria-busy', 'true');
  const live = element('div');
  live.setAttribute('id', 'taxonomy-live');
  const sandbox = makeSandbox(root, live, false);
  vm.runInContext(domSource, sandbox, { filename: domPath });
  vm.runInContext(pageSource, sandbox, { filename: pagePath });
  for (let i = 0; i < 6; i++) await new Promise(function (resolve) { setImmediate(resolve); });

  assert.ok(!(root._htmlWrites || []).length && !(live._htmlWrites || []).length,
    'taxonomy.js wrote to an HTML string sink: ' + JSON.stringify(root._htmlWrites || live._htmlWrites));
  const nodes = walk(root);
  assert.ok(!nodes.some(function (n) {
    return n.nodeType === 1 && ['img', 'script', 'b'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
  const text = root.textContent;
  [HOSTILE_TAG, HOSTILE_NS, HOSTILE_REL, HOSTILE_TARGET, HOSTILE_VALUE, HOSTILE_RATIONALE].forEach(function (needle) {
    assert.ok(text.indexOf(needle.replace(/<[^>]+>/g, '')) !== -1 || text.indexOf(needle) !== -1,
      'hostile field was not literal text: ' + needle);
  });
  const chip = nodes.find(function (n) { return n.nodeType === 1 && n.attrs.class === 'chip'; });
  assert.ok(chip, 'the tag chip disappeared');
  assert.strictEqual(chip.attrs['data-tag'], HOSTILE_TAG, 'the hostile tag attribute was mangled');
  assert.ok(chip.attrs.title.indexOf(HOSTILE_NS) !== -1, 'the hostile namespace title was mangled');
  const proposal = find(root, '[data-proposal]')[0];
  assert.ok(proposal, 'the proposal card disappeared');
  assert.strictEqual(proposal.attrs['data-proposal'], '9', 'the proposal id was mangled');
  assert.ok(find(root, '#taxonomy-propose').length === 1, 'the propose form disappeared');
  assert.ok(find(root, '[data-consent]').length === 1, 'the consent control disappeared');
  assert.ok(!root.attrs['aria-busy'], 'the page left aria-busy set');

  // Exercise the live-region refusal path with a hostile server message.
  const root2 = element('div');
  root2.setAttribute('id', 'taxonomy-root');
  const live2 = element('div');
  live2.setAttribute('id', 'taxonomy-live');
  const sandbox2 = makeSandbox(root2, live2, true);
  vm.runInContext(domSource, sandbox2, { filename: domPath });
  vm.runInContext(pageSource, sandbox2, { filename: pagePath });
  for (let i = 0; i < 6; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  const consent = find(root2, '[data-consent]')[0];
  assert.ok(consent, 'the second-run consent control disappeared');
  consent.listeners.click();
  for (let i = 0; i < 4; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  assert.ok(!(live2._htmlWrites || []).length, 'the refusal path used a string sink');
  assert.ok(live2.textContent.indexOf(HOSTILE_ERROR) !== -1, 'the hostile refusal was not literal text');
  assert.ok(!walk(live2).some(function (n) { return n.nodeType === 1 && n.tagName === 'script'; }),
    'the hostile refusal was parsed into an element');

  const sink = /(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/;
  assert.ok(!sink.test(pageSource), 'taxonomy.js regained an HTML string sink');
  console.log('dom taxonomy page: PASS — hostile tags and refusal are text, attrs verbatim, no string sink');
})().catch(function (err) {
  console.error('dom taxonomy page: FAIL — ' + err.message);
  process.exitCode = 1;
});
