#!/usr/bin/env node
// Step 2b: executes the shipped roadmap and milestone pages with hostile titles/bodies and exercises both write paths.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/milestones.js';
const domSource = fs.readFileSync(domPath, 'utf8');
const pageSource = fs.readFileSync(pagePath, 'utf8');

function textNode(value) {
  const text = String(value);
  return { nodeType: 3, text: text, get textContent() { return text; } };
}
function matches(n, sel) {
  const cls = /^\.(.+)$/.exec(sel);
  const eq = /^\[([^=\]]+)="([^"]+)"\]$/.exec(sel);
  const attr = /^\[([^=\]]+)\]$/.exec(sel);
  const bare = /^\[([^=\]]+)=([^\]]+)\]$/.exec(sel);
  if (bare) return n.attrs[bare[1]] === bare[2];
  if (sel === 'option:checked') return n.tagName === 'option' && !!n.selected;
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
function append(n, child) {
  if (child == null) return;
  if (Array.isArray(child)) return child.forEach(function (c) { append(n, c); });
  if (child.nodeType) { child.parent = n; n.children.push(child); return; }
  n.children.push(textNode(child));
}
function element(tag) {
  const n = {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {}, value: '', style: {},
    get parentNode() { return this.parent; },
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    removeAttribute(k) { delete this.attrs[k]; },
    hasAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k); },
    appendChild(c) { append(this, c); return c; },
    insertBefore(c, before) {
      const old = this.children.indexOf(c);
      if (old !== -1) this.children.splice(old, 1);
      c.parent = this;
      const i = before ? this.children.indexOf(before) : -1;
      if (i === -1) this.children.push(c); else this.children.splice(i, 0, c);
      return c;
    },
    replaceChildren() { this.children = []; Array.prototype.slice.call(arguments).forEach(function (c) { append(n, c); }); },
    addEventListener(ev, fn) { this.listeners[ev] = fn; },
    closest(sel) {
      let cur = this;
      while (cur) { if (cur.nodeType === 1 && matches(cur, sel)) return cur; cur = cur.parent; }
      return null;
    },
    get previousElementSibling() {
      const s = (this.parent && this.parent.children) || [];
      const i = s.indexOf(this);
      for (let j = i - 1; j >= 0; j--) if (s[j].nodeType === 1) return s[j];
      return null;
    },
    get nextElementSibling() {
      const s = (this.parent && this.parent.children) || [];
      const i = s.indexOf(this);
      for (let j = i + 1; j < s.length; j++) if (s[j].nodeType === 1) return s[j];
      return null;
    },
    get selectedOptions() { return this.children.filter(function (c) { return c.selected; }); },
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

const HOSTILE_TITLE = '<img src=x onerror=alert(1)>';
const HOSTILE_BODY = '<script>alert(2)</script>';
const HOSTILE_FEATURE = '<b>feature</b>';
const HOSTILE_ERROR = '<img src=y onerror=alert(3)>';

function makeSandbox(opts, which) {
  const roadmap = element('div'); roadmap.setAttribute('id', 'roadmap'); roadmap.setAttribute('data-slug', 'lorebook');
  const milestone = element('div'); milestone.setAttribute('id', 'milestone'); milestone.setAttribute('data-slug', 'lorebook'); milestone.setAttribute('data-milestone', '7');
  const msTitle = element('h1'); msTitle.setAttribute('id', 'ms-title');
  const msSub = element('p'); msSub.setAttribute('id', 'ms-subtitle');
  const nodes = which === 'milestone'
    ? { milestone, 'ms-title': msTitle, 'ms-subtitle': msSub }
    : { roadmap, milestone, 'ms-title': msTitle, 'ms-subtitle': msSub };
  const listeners = {};
  const calls = [];
  const sandbox = { console, Date, Math, Promise, Object, Array, String, Number, URL, URLSearchParams, JSON, setTimeout };
  sandbox.document = {
    createElement: element, createTextNode: textNode, title: 'before',
    getElementById(id) { return nodes[id] || null; },
    addEventListener(ev, fn) { listeners[ev] = fn; },
  };
  sandbox.window = sandbox;
  sandbox.location = { pathname: '/projects/lorebook/roadmap', search: '', href: 'https://concord.polarisocial.xyz/projects/lorebook/roadmap' };
  sandbox.alert = function (msg) { calls.push('ALERT:' + msg); };
  sandbox.ConcordApi = {
    request(method, url, body) {
      calls.push(method + ' ' + url + (body ? ' ' + body : ''));
      if (opts.failRead || (method === 'POST' && /\/milestones$/.test(url))) {
        return Promise.reject(new Error(HOSTILE_ERROR));
      }
      if (/\/roadmap$/.test(url)) return Promise.resolve(opts.emptyRoadmap ? {
        totals: { total: 0, open: 0, shipped: 0 }, milestones: [], slippage: [],
      } : {
        totals: { total: 9, open: 4, shipped: 5 },
        milestones: [
          { id: 7, title: HOSTILE_TITLE, status: 'active', at_risk: true, target_date: null, total: 9, shipped: 5, open: 4, seeded: 2 },
          { id: 8, title: 'Done one', status: 'done', target_date: 1700000000, total: 2, shipped: 2, open: 0 },
        ],
        slippage: [{ milestone_id: 7, slip_days: 3 }],
      });
      if (/\/milestones\/7$/.test(url)) return Promise.resolve({
        milestone: { id: 7, title: HOSTILE_TITLE, status: 'active', target_date: 1700000000,
          body: HOSTILE_BODY, total: 3, shipped: 1, open: 2 },
        features: [
          { id: 21, title: HOSTILE_FEATURE, status: 'draft' },
          { id: 22, title: 'Second', status: 'draft' },
          { id: 23, title: 'Old ship', status: 'shipped' },
        ],
      });
      if (/\/features$/.test(url)) return Promise.resolve({ features: [
        { id: 21, title: 'in this milestone', status: 'draft' },
        { id: 30, title: HOSTILE_FEATURE, status: 'draft' },
      ] });
      return Promise.resolve({});
    },
  };
  vm.createContext(sandbox);
  return { sandbox, nodes, listeners, calls, roadmap, milestone };
}
async function boot(which, opts) {
  const s = makeSandbox(opts || {}, which);
  vm.runInContext(domSource, s.sandbox, { filename: domPath });
  vm.runInContext(pageSource, s.sandbox, { filename: pagePath });
  s.listeners.DOMContentLoaded();
  for (let i = 0; i < 10; i++) await new Promise(function (r) { setImmediate(r); });
  return s;
}
function noSinks(s, roots) {
  const all = roots.reduce(function (a, r) { return a.concat([r], walk(r)); }, []);
  const writes = all.filter(function (n) { return n._htmlWrites && n._htmlWrites.length; });
  assert.ok(!writes.length, 'milestones.js wrote to an HTML string sink: ' +
    JSON.stringify(writes.map(function (n) { return n._htmlWrites; })));
  assert.ok(!all.some(function (n) {
    return n.nodeType === 1 && ['img', 'script', 'b'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
}

(async function () {
  const road = await boot('roadmap', {});
  assert.ok(road.roadmap.textContent.indexOf('9 features in milestones') !== -1, 'the totals disappeared');
  assert.strictEqual(find(road.roadmap, '.roadmap-title').length, 2, 'the roadmap columns disappeared');
  assert.strictEqual(find(road.roadmap, '.roadmap-title')[0].textContent, HOSTILE_TITLE,
    'the hostile milestone title was not literal text');
  assert.ok(find(road.roadmap, '.roadmap-date')[0].textContent.indexOf('no date · slipped 3d') !== -1,
    'the no-date or measured-slip rule broke');
  assert.ok(find(road.roadmap, '.ms-counts')[0].textContent.indexOf('5/9 shipped · 4 open · 2 seeded') !== -1,
    'the provenance line disappeared');
  assert.ok(find(road.roadmap, '.ms-risk').length === 1, 'the at-risk badge disappeared');
  assert.ok(find(road.roadmap, '[data-ms-close]')[1].hasAttribute('disabled'),
    'a finished milestone still has an enabled close button');
  assert.strictEqual(find(road.roadmap, '[data-ms-create]').length, 1, 'the create form disappeared');
  assert.strictEqual(road.roadmap.getAttribute('aria-busy'), 'false', 'aria-busy was left on');
  noSinks(road, [road.roadmap]);

  // Create: hostile server sentence goes to the live error region as text.
  const form = find(road.roadmap, '[data-ms-create]')[0];
  form.querySelector('[name=title]').value = HOSTILE_TITLE;
  form.listeners.submit({ preventDefault() {} });
  for (let i = 0; i < 12; i++) await new Promise(function (r) { setImmediate(r); });
  assert.ok(road.calls.some(function (c) { return /^POST .*\/milestones /.test(c); }), 'the create POST was not sent');
  assert.strictEqual(form.querySelector('.ms-error').textContent, HOSTILE_ERROR,
    'the hostile refusal was not announced as text');
  noSinks(road, [road.roadmap]);

  const empty = await boot('roadmap', { emptyRoadmap: true });
  assert.ok(empty.roadmap.textContent.indexOf('No milestones yet') !== -1, 'the empty state disappeared');
  noSinks(empty, [empty.roadmap]);

  const roadFail = await boot('roadmap', { failRead: true });
  assert.ok(roadFail.roadmap.textContent.indexOf('Could not load the roadmap') !== -1,
    'the roadmap failure state disappeared');
  assert.ok(find(roadFail.roadmap, 'img').length === 0, 'the hostile error was parsed into an element');
  noSinks(roadFail, [roadFail.roadmap]);

  const det = await boot('milestone', {});
  assert.strictEqual(det.sandbox.document.title, HOSTILE_TITLE + ' · Concord', 'the document title was not set');
  assert.strictEqual(det.nodes['ms-title'].textContent, HOSTILE_TITLE, 'the milestone heading was not literal text');
  assert.ok(det.nodes['ms-subtitle'].textContent.indexOf(HOSTILE_BODY) !== -1,
    'the hostile body was not literal text in the subtitle');
  assert.strictEqual(find(det.nodes['ms-subtitle'], 'script').length, 0,
    'the hostile body was parsed into an element');
  assert.strictEqual(find(det.milestone, '.ms-group').length, 2, 'the feature groups disappeared');
  assert.strictEqual(find(det.milestone, '[data-ms-feature]').length, 3, 'the feature rows disappeared');
  assert.ok(find(det.milestone, '[data-ms-feature]')[0].textContent.indexOf(HOSTILE_FEATURE) !== -1,
    'the hostile feature title was not literal text');
  const addBox = find(det.milestone, '[data-ms-add]')[0];
  assert.ok(addBox, 'the candidate select disappeared');
  assert.strictEqual(find(addBox, 'option').length, 1, 'the candidate list disappeared');
  assert.ok(find(addBox, 'option')[0].textContent.indexOf(HOSTILE_FEATURE) !== -1,
    'the hostile candidate was not literal text');
  noSinks(det, [det.milestone, det.nodes['ms-subtitle']]);

  // Up swaps the two open rows and PUTs the whole order in DOM order.
  const ups = find(det.milestone, '[data-ms-up]');
  ups[1].listeners.click();
  for (let i = 0; i < 6; i++) await new Promise(function (r) { setImmediate(r); });
  const order = find(det.milestone, '[data-ms-feature]').map(function (n) { return n.getAttribute('data-ms-feature'); });
  assert.deepStrictEqual(order, ['22', '21', '23'], 'the up button did not swap the two rows');
  assert.ok(det.calls.some(function (c) { return /PUT .*\/order \{"feature_ids":\[22,21\]\}/.test(c); }),
    'the reordered list was not PUT');
  noSinks(det, [det.milestone, det.nodes['ms-subtitle']]);

  // Assignment takes the selected candidates and reports a refusal as text.
  const addSelect = find(det.milestone, '[data-ms-add]')[0];
  addSelect.children[0].value = '30';
  addSelect.children[0].selected = true;
  const assign = find(det.milestone, '[data-ms-assign]')[0];
  assign.listeners.click();
  for (let i = 0; i < 6; i++) await new Promise(function (r) { setImmediate(r); });
  assert.ok(det.calls.some(function (c) { return /POST .*\/features \{"feature_ids":\[30\]\}/.test(c); }),
    'the assign POST was not sent');
  noSinks(det, [det.milestone, det.nodes['ms-subtitle']]);

  const detFail = await boot('milestone', { failRead: true });
  assert.ok(detFail.milestone.textContent.indexOf('Could not load this milestone') !== -1,
    'the milestone failure state disappeared');
  assert.strictEqual(find(detFail.milestone, 'img').length, 0, 'the hostile error was parsed into an element');
  noSinks(detFail, [detFail.milestone]);

  assert.ok(!/(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/.test(pageSource),
    'milestones.js regained an HTML string sink');
  console.log('dom milestones pages: PASS — hostile titles/errors are text, dates and provenance kept, no string sink');
})().catch(function (e) {
  console.error('dom milestones pages: FAIL — ' + e.message);
  process.exitCode = 1;
});
