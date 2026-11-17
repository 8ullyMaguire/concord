#!/usr/bin/env node
// Step 2b: executes the shipped Scout and Merge pages with hostile fields and all refusal paths.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const scoutPath = process.argv[3] || 'crates/web/assets/js/scout.js';
const mergePath = process.argv[4] || 'crates/web/assets/js/merge.js';
const domSource = fs.readFileSync(domPath, 'utf8');
const scoutSource = fs.readFileSync(scoutPath, 'utf8');
const mergeSource = fs.readFileSync(mergePath, 'utf8');

function textNode(value) {
  const text = String(value);
  return { nodeType: 3, text: text, get textContent() { return text; } };
}
function matches(n, sel) {
  const cls = /^\.(.+)$/.exec(sel);
  const id = /^#(.+)$/.exec(sel);
  const eq = /^\[([^=\]]+)="([^"]+)"\]$/.exec(sel);
  const attr = /^\[([^=\]]+)\]$/.exec(sel);
  const or = sel.split(',').map(function (s) { return s.trim(); });
  if (or.length > 1) return or.some(function (s) { return matches(n, s); });
  if (id) return n.attrs.id === id[1];
  if (cls) return (n.attrs.class || '').split(' ').indexOf(cls[1]) !== -1;
  if (eq) return n.attrs[eq[1]] === eq[2];
  if (attr) return Object.prototype.hasOwnProperty.call(n.attrs, attr[1]);
  return n.tagName === sel;
}
function append(n, child) {
  if (child == null) return;
  if (Array.isArray(child)) return child.forEach(function (c) { append(n, c); });
  if (child.nodeType) { n.children.push(child); return; }
  n.children.push(textNode(child));
}
function walk(node, out) {
  out = out || [];
  (node.children || []).forEach(function (c) { out.push(c); if (c.nodeType === 1) walk(c, out); });
  return out;
}
function find(root, sel) { return walk(root).filter(function (n) { return n.nodeType === 1 && matches(n, sel); }); }
function element(tag) {
  const n = {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {},
    value: '', hidden: false, href: '', title: '',
    get className() { return this.attrs.class || ''; },
    set className(v) { this.attrs.class = String(v); },
    get id() { return this.attrs.id || ''; },
    get parentNode() { return this.parent; },
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    hasAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k); },
    removeAttribute(k) { delete this.attrs[k]; },
    appendChild(c) { append(this, c); return c; },
    replaceChildren() { this.children = []; Array.prototype.slice.call(arguments).forEach(function (c) { append(n, c); }); },
    addEventListener(ev, fn) { this.listeners[ev] = fn; },
    focus() {},
    closest(sel) {
      let cur = this;
      while (cur) { if (cur.nodeType === 1 && matches(cur, sel)) return cur; cur = cur.parent; }
      return null;
    },
    querySelector(sel) { return find(this, sel)[0] || null; },
    querySelectorAll(sel) { return find(this, sel); },
    get text() {
      return this.children.map(function (c) { return c.nodeType === 3 ? c.text : c.textContent; }).join('');
    },
    set innerHTML(v) { this._htmlWrites = (this._htmlWrites || []).concat(String(v)); },
    insertAdjacentHTML(pos, v) { this._htmlWrites = (this._htmlWrites || []).concat(String(v)); },
  };
  Object.defineProperty(n, 'textContent', {
    get() { return n.text; },
    set(v) { n.children = [textNode(v)]; },
  });
  return n;
}
function baseSandbox(extra) {
  const sandbox = {
    console, Date, Math, Promise, Object, Array, String, Number, JSON, isFinite,
    encodeURIComponent, decodeURIComponent, setTimeout, clearTimeout,
  };
  sandbox.document = {
    createElement: element, createTextNode: textNode, title: '',
    getElementById(id) { return this._nodes[id] || null; },
    querySelector(sel) { return null; }, addEventListener() {},
  };
  sandbox.document._nodes = {};
  sandbox.window = sandbox;
  sandbox.ConcordEscape = { esc: function (s) { return String(s); } };
  Object.assign(sandbox, extra || {});
  vm.createContext(sandbox);
  return sandbox;
}
function noSinks(sandbox, roots) {
  const all = roots.reduce(function (a, r) { return a.concat([r], walk(r)); }, []);
  const writes = all.filter(function (n) { return n._htmlWrites && n._htmlWrites.length; });
  assert.ok(!writes.length, 'a page wrote to an HTML string sink: ' +
    JSON.stringify(writes.map(function (n) { return n._htmlWrites; })));
  assert.ok(!all.some(function (n) {
    return n.nodeType === 1 && ['img', 'script', 'b'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
}
function settle() {
  return new Promise(function (resolve) {
    setImmediate(function () { setImmediate(function () { setImmediate(resolve); }); });
  });
}

const HOSTILE = '<img src=x onerror=alert(1)>';

async function scoutCase(opts) {
  const nodes = {};
  ['scout-root', 'scout-seed', 'scout-report', 'scout-error', 'scout-seed-text', 'scout-exclude',
   'scout-save-slug', 'scout-save', 'scout-save-error', 'scout-save-ok', 'scout-capabilities',
   'scout-verdicts', 'scout-decomp-summary', 'scout-stats', 'scout-seed-form', 'scout-again']
    .forEach(function (id) { nodes[id] = element(id === 'scout-seed-form' ? 'form' : 'div'); });
  nodes['scout-seed-text'].value = 'offline idea';
  nodes['scout-exclude'].value = '';
  nodes['scout-save-slug'].value = 'lorebook';
  const calls = [];
  const data = {
    idea: 'offline idea', capabilities: [{ key: 'offline', label: HOSTILE, coverage: 'open', in_catalog: false }],
    candidates_considered: 2, stats: { 'base-on': 1 },
    verdicts: [{ slug: 'other', name: HOSTILE, verdict: 'base-on', reason: HOSTILE,
      total: 2, matched: 1, fit: 0.5, evidence_coverage: 0.5, health_known: false,
      reports: 0, signals: ['signal'], pain: [{ title: HOSTILE }] }],
  };
  const sandbox = baseSandbox({
    location: { pathname: '/scout', href: 'https://concord.polarisocial.xyz/scout' },
    ConcordApi: {
      get(url) {
        calls.push('GET ' + url);
        if (opts.fail) return Promise.reject(new Error(HOSTILE));
        return Promise.resolve({ ok: true, status: 200, text: function () { return Promise.resolve(JSON.stringify(data)); } });
      },
      put(url, body) { calls.push('PUT ' + url + ' ' + JSON.stringify(body)); return Promise.resolve({ kind: 'scout', revision: 2 }); },
    },
  });
  sandbox.document._nodes = nodes;
  vm.runInContext(domSource, sandbox, { filename: domPath });
  vm.runInContext(scoutSource, sandbox, { filename: scoutPath });
  nodes['scout-seed-form'].listeners.submit({ preventDefault() {} });
  await settle();
  return { sandbox, nodes, calls, data };
}

async function mergeCase(opts) {
  const root = element('div'); root.setAttribute('id', 'merge-root');
  const notice = element('p'); notice.setAttribute('id', 'merge-notice');
  const nodes = { 'merge-root': root, 'merge-notice': notice };
  const calls = [];
  const mrs = opts.failMrs ? [] : [{
    id: 12, title: HOSTILE, status: 'open', feature_id: 33, opened_at: 1700000000,
    gate: { requires_quorum: true, quorum_ok: false, approvals: 1, needed: 2,
      reviewer_ok: false, passed: false, eligible: 3 },
  }];
  const features = opts.noFeatures ? [] : [{ id: 33, title: HOSTILE }];
  const sandbox = baseSandbox({
    location: { pathname: '/projects/lorebook/merge', href: 'https://concord.polarisocial.xyz/projects/lorebook/merge' },
    ConcordSession: { instance: function () { return { signedIn: function () { return !opts.signedOut; } }; } },
    ConcordApi: {
      get(url) {
        calls.push('GET ' + url);
        if (opts.fail) return Promise.reject(new Error(HOSTILE));
        if (/\/features$/.test(url)) return Promise.resolve(features);
        return Promise.resolve(mrs);
      },
      request(method, url, body) {
        calls.push(method + ' ' + url + (body ? ' ' + JSON.stringify(body) : ''));
        return Promise.resolve({ status: HOSTILE });
      },
    },
  });
  sandbox.document._nodes = nodes;
  vm.runInContext(domSource, sandbox, { filename: domPath });
  vm.runInContext(mergeSource, sandbox, { filename: mergePath });
  await settle();
  nodes['merge-title'] = find(root, '#merge-title')[0];
  nodes['merge-feature'] = find(root, '#merge-feature')[0];
  return { sandbox, root, notice, calls, nodes };
}

(async function () {
  // Scout: two clears became replaceChildren; every field still lands as text.
  const scout = await scoutCase({});
  assert.strictEqual(find(scout.nodes['scout-capabilities'], '.scout-cap').length, 1,
    'the capability row disappeared');
  assert.strictEqual(find(scout.nodes['scout-capabilities'], '.scout-cap-label')[0].textContent, HOSTILE,
    'the hostile capability label was not literal text');
  assert.strictEqual(find(scout.nodes['scout-verdicts'], '.scout-verdict').length, 1,
    'the verdict row disappeared');
  assert.strictEqual(find(scout.nodes['scout-verdicts'], '.scout-verdict-name')[0].textContent, HOSTILE,
    'the hostile verdict name was not literal text');
  assert.ok(scout.nodes['scout-stats'].textContent.indexOf('2 projects considered — 1 base-on') !== -1,
    'the server-supplied counts disappeared');
  assert.ok(find(scout.nodes['scout-verdicts'], 'a')[0].href === '/projects/other',
    'the verdict link was mangled');
  noSinks(scout.sandbox, [scout.nodes['scout-capabilities'], scout.nodes['scout-verdicts']]);

  // A second answer must clear the first, not append to it.
  scout.nodes['scout-seed-form'].listeners.submit({ preventDefault() {} });
  await settle();
  assert.strictEqual(find(scout.nodes['scout-capabilities'], '.scout-cap').length, 1,
    'the capability panel appended a duplicate on a second answer');
  assert.strictEqual(find(scout.nodes['scout-verdicts'], '.scout-verdict').length, 1,
    'the verdict panel appended a duplicate on a second answer');
  noSinks(scout.sandbox, [scout.nodes['scout-capabilities'], scout.nodes['scout-verdicts']]);

  const scoutFail = await scoutCase({ fail: true });
  assert.ok(scoutFail.nodes['scout-error'].textContent.indexOf(HOSTILE) !== -1,
    'the hostile Scout failure was not announced');
  noSinks(scoutFail.sandbox, [scoutFail.nodes['scout-error']]);

  // Merge: rows, gate cell, create form and failure state are el() trees.
  const merge = await mergeCase({});
  assert.strictEqual(find(merge.root, '.merge-row').length, 1, 'the merge row disappeared');
  assert.strictEqual(find(merge.root, '.merge-title')[0].textContent, HOSTILE,
    'the hostile merge title was not literal text');
  const gate = find(merge.root, '.merge-gate')[0];
  assert.ok(gate.textContent.indexOf('short: 1 of 2 confirmations') !== -1,
    'the server-supplied gate arithmetic disappeared');
  assert.strictEqual(gate.attrs.title, 'measured against 3 eligible collaborators',
    'the eligible-collaborator explanation disappeared');
  assert.strictEqual(find(find(merge.root, '.merge-feature')[0], 'a')[0].attrs.href,
    '/projects/lorebook/features/33', 'the feature link was mangled');
  assert.strictEqual(find(merge.root, '#merge-create-form').length, 1, 'the create form disappeared');
  assert.strictEqual(find(find(merge.root, '#merge-feature')[0], 'option')[0].textContent, HOSTILE,
    'the hostile feature title was not literal text');
  noSinks(merge.sandbox, [merge.root]);

  const originalTitle = find(merge.root, '#merge-title')[0];
  const approve = find(merge.root, '[data-approve]')[0];
  approve.listeners = { click: null };
  // The page delegates clicks on the root, so dispatch through it.
  merge.root.listeners.click({ target: { closest: function () { return approve; } } });
  for (let i = 0; i < 8; i++) await new Promise(function (r) { setImmediate(r); });
  assert.ok(merge.calls.some(function (c) { return /PUT .*\/merge\/12\/approve/.test(c); }),
    'the approve click did not reach its route');
  assert.ok(merge.notice.textContent.indexOf(HOSTILE) !== -1,
    'the server refusal was not reported in the notice');
  noSinks(merge.sandbox, [merge.root]);

  const form = find(merge.root, '#merge-create-form')[0];
  merge.nodes['merge-title'] = find(merge.root, '#merge-title')[0];
  merge.nodes['merge-feature'] = find(merge.root, '#merge-feature')[0];
  originalTitle.value = 'Land it';
  merge.nodes['merge-title'].value = 'Land it';
  merge.root.listeners.submit({ target: form, preventDefault() {} });
  for (let i = 0; i < 8; i++) await new Promise(function (r) { setImmediate(r); });
  assert.ok(merge.calls.some(function (c) { return /POST .*\/merge /.test(c); }),
    'the create form did not post');

  const signedOut = await mergeCase({ signedOut: true });
  assert.ok(signedOut.root.textContent.indexOf('sign in to confirm or land this') !== -1,
    'a signed-out visitor was shown write controls');
  noSinks(signedOut.sandbox, [signedOut.root]);

  const noFeatures = await mergeCase({ noFeatures: true });
  assert.ok(noFeatures.root.textContent.indexOf('no features to land yet') !== -1,
    'the no-features state disappeared');
  noSinks(noFeatures.sandbox, [noFeatures.root]);

  const mergeFail = await mergeCase({ fail: true });
  assert.ok(mergeFail.root.textContent.indexOf('Could not load the merge requests') !== -1,
    'the merge failure state disappeared');
  assert.strictEqual(find(mergeFail.root, 'img').length, 0,
    'the hostile error was parsed into an element');
  noSinks(mergeFail.sandbox, [mergeFail.root]);

  assert.ok(!/(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/.test(scoutSource),
    'scout.js regained an HTML string sink');
  assert.ok(!/(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/.test(mergeSource),
    'merge.js regained an HTML string sink');
  console.log('dom scout+merge pages: PASS — hostile fields are text, gates and counts verbatim, no string sink');
})().catch(function (e) {
  console.error('dom scout+merge pages: FAIL — ' + e.message);
  process.exitCode = 1;
});
