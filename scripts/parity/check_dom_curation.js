#!/usr/bin/env node
// Step 2b: executes the shipped curation queue with hostile prose and all list/refresh paths.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/curation.js';
const domSource = fs.readFileSync(domPath, 'utf8');
const pageSource = fs.readFileSync(pagePath, 'utf8');

function textNode(value) {
  const text = String(value);
  return { nodeType: 3, text: text, get textContent() { return text; } };
}
function matches(n, sel) {
  const cls = /^\.(.+)$/.exec(sel);
  const attr = /^\[([^=\]]+)=([^\]]+)\]$/.exec(sel);
  if (cls) return (n.attrs.class || '').split(' ').indexOf(cls[1]) !== -1;
  if (attr) return n.attrs[attr[1]] === attr[2];
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
    value: '', hidden: false, disabled: false, checked: false, href: '', type: '', placeholder: '',
    get className() { return this.attrs.class || ''; },
    set className(v) { this.attrs.class = String(v); },
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    hasAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k); },
    appendChild(c) { append(this, c); return c; },
    replaceChildren() { this.children = []; Array.prototype.slice.call(arguments).forEach(function (c) { append(n, c); }); },
    addEventListener(ev, fn) { this.listeners[ev] = fn; },
    focus() {},
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

const HOSTILE_SUMMARY = '<script>alert(1)</script>';
const HOSTILE_TARGET = '<img src=x onerror=alert(2)>';

function boot(opts) {
  const nodes = {};
  ['curation-list', 'curation-kind', 'curation-state', 'curation-unvoted', 'curation-q',
   'curation-more', 'curation-status'].forEach(function (id) { nodes[id] = element(id === 'curation-q' ? 'input' : 'div'); });
  const calls = [];
  let first = true;
  const item = {
    id: 12, kind: 'act', action: 'arena.remove', state: 'applied', target: HOSTILE_TARGET,
    summary: JSON.stringify({ reason: HOSTILE_SUMMARY }),
    yes: 2, no: 1, contested: true, my_vote: 0, can_vote: true,
    project_slug: 'lorebook',
  };
  const sandbox = {
    console, Date, Math, Promise, Object, Array, String, Number, JSON, URLSearchParams,
    encodeURIComponent, setTimeout: function (fn) { fn(); return 1; }, clearTimeout: function () {},
  };
  sandbox.document = {
    createElement: element, createTextNode: textNode, readyState: 'complete',
    getElementById(id) { return nodes[id] || null; },
    addEventListener() {},
  };
  sandbox.window = sandbox;
  sandbox.ConcordSession = { instance: function () { return { user: { id: 1 } }; } };
  sandbox.ConcordApi = {
    get(url) {
      calls.push('GET ' + url);
      if (opts.fail) return Promise.reject(new Error(HOSTILE_SUMMARY));
      return Promise.resolve(first ? (first = false, { items: [item], next_cursor: 'cursor-2' })
        : { items: [{
            id: 13, kind: 'taxonomy', action: 'add', state: 'pending', target: 'second target',
            summary: 'plain summary', yes: 0, no: 0, can_vote: false, project_slug: '',
          }], next_cursor: '' });
    },
    post(url, body) {
      calls.push('POST ' + url + ' ' + JSON.stringify(body));
      return opts.postFail ? Promise.reject(new Error(HOSTILE_SUMMARY)) : Promise.resolve({ yes: 3, no: 1, ratified: false, decided: false });
    },
  };
  vm.createContext(sandbox);
  vm.runInContext(domSource, sandbox, { filename: domPath });
  vm.runInContext(pageSource, sandbox, { filename: pagePath });
  return { sandbox, nodes, calls, item };
}
function settle() {
  return new Promise(function (resolve) {
    setImmediate(function () { setImmediate(function () { setImmediate(resolve); }); });
  });
}
function noSinks(s) {
  const all = [s.nodes['curation-list'], s.nodes['curation-status']]
    .concat(walk(s.nodes['curation-list']), walk(s.nodes['curation-status']));
  const writes = all.filter(function (n) { return n._htmlWrites && n._htmlWrites.length; });
  assert.ok(!writes.length, 'curation.js wrote to an HTML string sink: ' +
    JSON.stringify(writes.map(function (n) { return n._htmlWrites; })));
  assert.ok(!all.some(function (n) {
    return n.nodeType === 1 && ['script', 'img'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
}

(async function () {
  const q = boot({});
  await settle();
  assert.strictEqual(find(q.nodes['curation-list'], '.curation-card').length, 1,
    'the first curation card disappeared');
  const first = find(q.nodes['curation-list'], '.curation-card')[0];
  assert.strictEqual(find(first, '.curation-summary')[0].textContent, HOSTILE_SUMMARY,
    'the hostile act reason was not literal text');
  assert.strictEqual(find(first, '.curation-target')[0].textContent, HOSTILE_TARGET,
    'the hostile target was not literal text');
  assert.strictEqual(find(first, '.curation-target')[0].href,
    '/projects/lorebook/audit?action=arena.remove', 'the act href was mangled');
  assert.strictEqual(q.nodes['curation-status'].textContent, '1 shown.', 'the count line disappeared');
  assert.strictEqual(q.nodes['curation-more'].hidden, false, 'the more button should be visible');
  noSinks(q);

  // Append keeps the first card and adds the second one.
  q.nodes['curation-more'].listeners.click();
  await settle();
  assert.strictEqual(find(q.nodes['curation-list'], '.curation-card').length, 2,
    'the append did not add the second card');
  noSinks(q);

  // A filter change clears the list; a failed clear would append a third card.
  q.nodes['curation-kind'].value = 'taxonomy';
  q.nodes['curation-kind'].listeners.change();
  await settle();
  assert.strictEqual(find(q.nodes['curation-list'], '.curation-card').length, 1,
    'a refresh did not replace the list');
  assert.strictEqual(find(q.nodes['curation-list'], '.curation-card')[0].getAttribute('data-id'), '13',
    'the refreshed card was not the new first page');
  noSinks(q);

  // Vote and undo both keep their server-owned refusals as text.
  const voted = boot({});
  await settle();
  find(voted.nodes['curation-list'], 'button')[0].listeners.click();
  for (let i = 0; i < 8; i++) await new Promise(function (r) { setImmediate(r); });
  assert.ok(voted.calls.some(function (c) { return /POST .*\/curation\/act\/12\/vote/.test(c); }),
    'the vote was not posted');
  assert.ok(voted.nodes['curation-status'].textContent.indexOf('Not decided yet') !== -1,
    'the server decision flag was not reported');
  noSinks(voted);

  const undo = boot({});
  await settle();
  const undoButtons = find(undo.nodes['curation-list'], 'button');
  const open = undoButtons.filter(function (b) { return b.textContent === 'Undo this removal'; })[0];
  assert.ok(open, 'the undo control disappeared');
  open.listeners.click();
  const reverse = find(undo.nodes['curation-list'], 'button')
    .filter(function (b) { return b.textContent === 'Reverse it'; })[0];
  reverse.listeners.click();
  assert.ok(undo.nodes['curation-status'].textContent.indexOf('needs a reason') !== -1,
    'the empty-reason refusal disappeared');
  noSinks(undo);

  const failed = boot({ fail: true });
  await settle();
  assert.ok(failed.nodes['curation-status'].textContent.indexOf('Could not load the queue') !== -1,
    'the load failure disappeared');
  noSinks(failed);

  const postFailed = boot({ postFail: true });
  await settle();
  find(postFailed.nodes['curation-list'], 'button')[0].listeners.click();
  for (let i = 0; i < 8; i++) await new Promise(function (r) { setImmediate(r); });
  assert.strictEqual(find(postFailed.nodes['curation-list'], 'script').length, 0,
    'the hostile refusal was parsed into an element');
  noSinks(postFailed);

  assert.ok(!/(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/.test(pageSource),
    'curation.js regained an HTML string sink');
  console.log('dom curation queue: PASS — hostile prose is text, refresh clears, votes and undo kept, no string sink');
})().catch(function (e) {
  console.error('dom curation queue: FAIL — ' + e.message);
  process.exitCode = 1;
});
