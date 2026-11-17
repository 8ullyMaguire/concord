#!/usr/bin/env node
// Step 2b: executes the shipped kanban board with hostile columns/cards/milestones and every refusal branch.
// Every field must be a text node or a setAttribute value; no HTML string sink may be used.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/board.js';
const domSource = fs.readFileSync(domPath, 'utf8');
const pageSource = fs.readFileSync(pagePath, 'utf8');

function textNode(value) {
  const text = String(value);
  return { nodeType: 3, text: text, get textContent() { return text; } };
}
function matches(n, sel) {
  const id = /^#(.+)$/.exec(sel);
  const cls = /^\.(.+)$/.exec(sel);
  const eq = /^\[([^=\]]+)="([^"]+)"\]$/.exec(sel);
  const attr = /^\[(.+)\]$/.exec(sel);
  if (id) return n.attrs.id === id[1];
  if (cls) return (n.attrs.class || '').split(' ').indexOf(cls[1]) !== -1;
  if (eq) return n.attrs[eq[1]] === eq[2];
  if (attr) return Object.prototype.hasOwnProperty.call(n.attrs, attr[1]);
  return n.tagName === sel;
}
function walk(node, out) {
  out = out || [];
  (node.children || []).forEach(function (c) { out.push(c); if (c.nodeType === 1) walk(c, out); });
  return out;
}
function find(root, sel) { return walk(root).filter(function (n) { return n.nodeType === 1 && matches(n, sel); }); }
function element(tag) {
  const n = {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], parent: null, listeners: {},
    hidden: false, value: '', style: {},
    firstChild: null,
    get parentNode() { return this.parent; },
    setAttribute(k, v) { this.attrs[k] = String(v); if (k === 'id') this.id = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    hasAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k); },
    removeAttribute(k) { delete this.attrs[k]; },
    appendChild(c) { c.parent = this; this.children.push(c); this.firstChild = this.children[0]; return c; },
    insertBefore(c, before) {
      c.parent = this;
      const i = before ? this.children.indexOf(before) : -1;
      if (i === -1) this.children.push(c); else this.children.splice(i, 0, c);
      this.firstChild = this.children[0];
      return c;
    },
    replaceChildren() {
      this.children = [];
      Array.prototype.slice.call(arguments).forEach(function (c) { if (c != null) n.appendChild(c); });
    },
    addEventListener(ev, fn) { this.listeners[ev] = fn; },
    focus() { this._focused = true; },
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
  n.classList = {
    add(c) { n.attrs.class = ((n.attrs.class || '') + ' ' + c).trim(); },
    remove(c) { n.attrs.class = (n.attrs.class || '').split(' ').filter(function (x) { return x !== c; }).join(' '); },
  };
  Object.defineProperty(n, 'textContent', {
    get() { return n.text; },
    set(v) { n.children = [textNode(v)]; },
  });
  return n;
}

const HOSTILE_COL = '<img src=x onerror=alert(1)>';
const HOSTILE_TITLE = '<script>alert(2)</script>';
const HOSTILE_MS = '<b>milestone</b>';
const HOSTILE_ERROR = '<script>alert(3)</script>';

const COLUMNS = [
  { name: HOSTILE_COL, wip: 1 },
  { name: 'shipped', wip: 0 },
];
const CARDS = [
  { id: 11, feature_id: 101, column: HOSTILE_COL, title: '', derived: true },
  { id: 22, feature_id: 102, column: 'shipped', title: HOSTILE_TITLE, derived: false },
];
const FEATURES = [{ id: 101, title: 'From the feature list' }];
const MILESTONES = [{ id: 7, title: HOSTILE_MS, open: 3 }];

function makeSandbox(opts) {
  const box = element('div');
  box.id = 'kanban-board';
  box.setAttribute('id', 'kanban-board');
  const page = element('div');
  page.appendChild(box);
  const listeners = {};
  const calls = [];
  const sandbox = {
    console, Date, Math, Promise, Object, Array, String, Number, setTimeout,
    encodeURIComponent, decodeURIComponent, URL, URLSearchParams,
  };
  sandbox.document = {
    createElement: element, createTextNode: textNode,
    getElementById(id) { return id === 'kanban-board' ? box : null; },
    addEventListener(ev, fn) { listeners[ev] = fn; },
  };
  sandbox.window = sandbox;
  sandbox.location = {
    pathname: opts.noSlug ? '/' : '/projects/lorebook/board', search: '',
    href: 'https://concord.polarisocial.xyz/projects/lorebook/board',
  };
  sandbox.ConcordSkeleton = { done(el) { el.setAttribute('aria-busy', 'false'); } };
  sandbox.ConcordApi = {
    get(path) {
      calls.push(path);
      if (opts.failProject) { const e = new Error(HOSTILE_ERROR); e.status = opts.notFound ? 404 : 500; return Promise.reject(e); }
      if (/\/board/.test(path)) return Promise.resolve({ columns: opts.noColumns ? [] : COLUMNS, cards: opts.noCards ? [] : CARDS });
      if (/\/features/.test(path)) return Promise.resolve(FEATURES);
      if (/\/milestones/.test(path)) return Promise.resolve(MILESTONES);
      return Promise.resolve({ id: 1, slug: 'lorebook' });
    },
    put(path) { calls.push(path); return opts.refuseMove ? Promise.reject(new Error(HOSTILE_ERROR)) : Promise.resolve({ status: 'moved' }); },
  };
  vm.createContext(sandbox);
  return { sandbox, box, page, listeners, calls };
}
async function boot(opts) {
  const s = makeSandbox(opts || {});
  vm.runInContext(domSource, s.sandbox, { filename: domPath });
  vm.runInContext(pageSource, s.sandbox, { filename: pagePath });
  s.listeners.DOMContentLoaded();
  for (let i = 0; i < 14; i++) await new Promise(function (r) { setImmediate(r); });
  return s;
}
function noSinks(s) {
  const all = [s.box, s.page].concat(walk(s.page));
  const writes = all.filter(function (n) { return n._htmlWrites && n._htmlWrites.length; });
  assert.ok(!writes.length, 'board.js wrote to an HTML string sink: ' +
    JSON.stringify(writes.map(function (n) { return n._htmlWrites; })));
  assert.ok(!all.some(function (n) {
    return n.nodeType === 1 && ['img', 'script', 'b'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
}

(async function () {
  const good = await boot({});
  assert.strictEqual(find(good.box, '.kanban-column').length, 2, 'the two columns disappeared');
  assert.strictEqual(find(good.box, '.kanban-card').length, 2, 'the two cards disappeared');
  assert.strictEqual(find(good.box, '.kanban-card-move').length, 2, 'the accessible move controls disappeared');
  assert.strictEqual(find(good.box, '.kanban-column-title')[0].textContent, HOSTILE_COL,
    'the hostile column name was not literal text');
  assert.strictEqual(find(good.box, '.kanban-card')[1].textContent, HOSTILE_TITLE,
    'the hostile card title was not literal text');
  assert.strictEqual(find(good.box, '.kanban-card')[0].textContent, 'From the feature list',
    'the server-less card title did not fall back to the feature list');
  assert.strictEqual(find(good.page, '[data-board-filter]').length, 1, 'the milestone filter disappeared');
  const filterSelect = find(good.page, '#board-milestone')[0];
  assert.ok(filterSelect, 'the milestone select disappeared');
  const filterOpts = find(filterSelect, 'option');
  assert.strictEqual(filterOpts[1].textContent, HOSTILE_MS + ' (3 open)',
    'the hostile milestone title was not literal text');
  assert.ok(find(good.box, '.is-at-wip').length === 1, 'the WIP marker disappeared');
  assert.ok(good.box.textContent.indexOf('1 of 2 cards sit here') !== -1, 'the derived-card note disappeared');
  assert.strictEqual(good.box.getAttribute('aria-busy'), 'false', 'aria-busy was left on');
  noSinks(good);

  // The accessible move path posts to the card-id route and re-reads the board.
  const sel = find(good.box, '.kanban-card-move')[1];
  sel.value = 'shipped';
  sel.listeners.change();
  for (let i = 0; i < 14; i++) await new Promise(function (r) { setImmediate(r); });
  assert.ok(good.calls.some(function (p) { return /\/cards\/22\/move\?to=shipped/.test(p); }),
    'the select did not post to the card-id move route');
  assert.ok(good.box.textContent.indexOf('Moved to shipped.') !== -1, 'the confirmation was wiped by the repaint');
  noSinks(good);

  // A refused move announces the server's sentence as text.
  const refused = await boot({ refuseMove: true });
  const sel2 = find(refused.box, '.kanban-card-move')[1];
  sel2.value = 'shipped';
  sel2.listeners.change();
  for (let i = 0; i < 10; i++) await new Promise(function (r) { setImmediate(r); });
  assert.ok(refused.box.textContent.indexOf(HOSTILE_ERROR) !== -1, 'the hostile refusal was not announced');
  assert.strictEqual(find(refused.box, 'script').length, 0, 'the hostile refusal was parsed into an element');
  noSinks(refused);

  const scenarios = [
    [{ noColumns: true }, 'No board yet'],
    [{ noCards: true }, 'Nothing on the board yet'],
    [{ failProject: true, notFound: true }, 'Project not found'],
    [{ failProject: true }, 'Could not load board'],
    [{ noSlug: true }, 'No project specified'],
  ];
  for (const [opts, needle] of scenarios) {
    const s = await boot(opts);
    assert.ok(s.box.textContent.indexOf(needle) !== -1,
      'expected ' + needle + ' but got: ' + s.box.textContent.slice(0, 80));
    noSinks(s);
  }

  assert.ok(!/(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/.test(pageSource),
    'board.js regained an HTML string sink');
  console.log('dom board page: PASS — hostile columns/cards and refusals are text, attrs verbatim, no string sink');
})().catch(function (e) {
  console.error('dom board page: FAIL — ' + e.message);
  process.exitCode = 1;
});
