#!/usr/bin/env node
// Step 2b: executes the shipped finder flow through seed -> question -> stop -> results with hostile server fields.
// Every field must be a text node or a setAttribute value; no HTML string sink may be used.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/finder.js';
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
  const attr = /^\[(.+)\]$/.exec(sel);
  const id = /^#(.+)$/.exec(sel);
  const cls = /^\.(.+)$/.exec(sel);
  return walk(root).filter(function (n) {
    if (n.nodeType !== 1) return false;
    if (attr) return Object.prototype.hasOwnProperty.call(n.attrs, attr[1]);
    if (id) return n.attrs.id === id[1];
    if (cls) return (n.attrs.class || '').split(' ').indexOf(cls[1]) !== -1;
    return n.tagName === sel;
  });
}
function element(tag) {
  const n = {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {},
    hidden: false, disabled: false, value: '', style: {}, textContent: '',
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    removeAttribute(k) { delete this.attrs[k]; },
    appendChild(c) { this.children.push(c); return c; },
    replaceChildren() {
      // Real DOM converts strings to text nodes; the fake must too.
      this.children = Array.prototype.slice.call(arguments).map(function (c) {
        return (c == null || typeof c === 'object') ? c : textNode(c);
      });
    },
    addEventListener(ev, fn) { this.listeners[ev] = fn; },
    closest(sel) {
      const a = /^\[(.+)\]$/.exec(sel);
      const i = /^#(.+)$/.exec(sel);
      if (a && Object.prototype.hasOwnProperty.call(this.attrs, a[1])) return this;
      if (i && this.attrs.id === i[1]) return this;
      return null;
    },
    click() { if (this.listeners.click) this.listeners.click({ target: this, preventDefault() {} }); },
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
    toggle(c, on) { if (on) n.classList.add(c); else n.classList.remove(c); },
  };
  Object.defineProperty(n, 'textContent', {
    get() { return n.text; },
    set(v) { n.children = [textNode(v)]; },
  });
  return n;
}

const HOSTILE_NAME = '<img src=x onerror=alert(1)>';
const HOSTILE_SLUG = 'x" onmouseover="alert(2)';
const HOSTILE_UNKNOWN = '<script>alert(3)</script>';
const HOSTILE_OPTION = '<b>option</b>';
const HOSTILE_FILTER = '<i>filter</i>';
const HOSTILE_ERROR = '<script>alert(4)</script>';

const IDS = ['finder-root', 'finder-seed', 'finder-flow', 'finder-results', 'finder-error',
  'finder-categories', 'finder-progress-text', 'finder-progress-bar', 'finder-back',
  'finder-question-text', 'finder-why', 'finder-why-text', 'finder-options',
  'finder-stop-prompt', 'finder-stop-keep', 'finder-stop-reason', 'finder-stop-top',
  'finder-shortlist-summary', 'finder-shortlist-items', 'finder-answers',
  'finder-results-answers', 'finder-ranked-heading', 'finder-ranked', 'finder-unknown',
  'finder-unknown-items', 'finder-filtered-out', 'finder-filtered-items',
  'finder-nothing', 'finder-nothing-detail', 'finder-export-json',
  'finder-seed-form', 'finder-seed-text', 'finder-stop', 'finder-stop-go', 'finder-view-all'];

const SESSION1 = {
  session_id: 'abc123', questions_asked: 2, candidate_count: 3, initial_candidate_count: 12,
  question: { key: 'hosting', text: 'How do you host it?', why_asked: 'changes fit',
    options: [{ id: 'self', label: HOSTILE_OPTION }, { id: 'saas', label: 'SaaS' }] },
  top_candidates: [{ name: HOSTILE_NAME, slug: HOSTILE_SLUG, fit_score: 0.8 }],
  answers: [{ dimension_key: 'hosting', option_id: 'self', mode: 'required' }],
  should_suggest_stop: false,
};
const SESSION2 = Object.assign({}, SESSION1, {
  question: null, stop_reason: 'LOW_GAIN', candidate_count: 2,
  answers: [{ dimension_key: 'hosting', option_id: HOSTILE_FILTER, mode: 'skip' }],
});

function makeSandbox(fail401) {
  const nodes = {};
  IDS.forEach(function (id) { const n = element('div'); n.id = id; n.setAttribute('id', id); nodes[id] = n; });
  const sandbox = {
    console, Date, Math, Promise, Object, Array, String, Number, URLSearchParams,
    setTimeout, encodeURIComponent,
  };
  sandbox.document = {
    createElement: element,
    createTextNode: textNode,
    getElementById(id) { return nodes[id] || null; },
    querySelectorAll(sel) {
      if (sel === '#finder-categories .chip') return find(nodes['finder-categories'], '.chip');
      if (sel === '#finder-options [data-option]') return find(nodes['finder-options'], '[data-option]');
      return [];
    },
    addEventListener() {},
  };
  sandbox.window = sandbox;
  sandbox.location = { search: '' };
  sandbox.ConcordApi = {
    request(method, path) {
      if (fail401) { const e = new Error(HOSTILE_ERROR); e.status = 401; return Promise.reject(e); }
      if (/sessions$/.test(path)) return Promise.resolve(SESSION1);
      if (/\/answers$/.test(path)) return Promise.resolve(SESSION2);
      return Promise.resolve({
        candidates: [{ name: HOSTILE_NAME, slug: HOSTILE_SLUG, fit_score: 0.9, evidence_coverage: 0.5 }],
        unknown_fit: [{ name: HOSTILE_NAME, slug: HOSTILE_SLUG, unknown: [HOSTILE_UNKNOWN] }],
        filtered_out: [{ filters: [HOSTILE_FILTER, 'another'] }],
        weights: {}, gaps: {},
      });
    },
  };
  vm.createContext(sandbox);
  return { sandbox: sandbox, nodes: nodes };
}

(async function () {
  const good = makeSandbox(false);
  vm.runInContext(domSource, good.sandbox, { filename: domPath });
  vm.runInContext(pageSource, good.sandbox, { filename: pagePath });
  const n = good.nodes;

  assert.strictEqual(find(n['finder-categories'], '[data-category]').length, 8,
    'the seed category row lost its buttons');
  n['finder-root'].listeners.submit({ target: n['finder-seed-form'], preventDefault() {} });
  for (let i = 0; i < 6; i++) await new Promise(function (resolve) { setImmediate(resolve); });

  assert.strictEqual(n['finder-question-text'].textContent, 'How do you host it?', 'the question disappeared');
  const opts = find(n['finder-options'], '[data-option]');
  assert.strictEqual(opts.length, 3, 'the two options plus "any" disappeared');
  assert.ok(n['finder-options'].textContent.indexOf(HOSTILE_OPTION) !== -1,
    'the hostile option label was not literal text');
  assert.ok(!find(n['finder-options'], 'b').length, 'the hostile option label was parsed into an element');

  n['finder-root'].listeners.click({ target: opts[0], preventDefault() {} });
  for (let i = 0; i < 6; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  assert.ok(n['finder-stop-reason'].textContent.indexOf('slightly change') !== -1,
    'the stop reason disappeared');
  assert.strictEqual(find(n['finder-stop-top'], 'li').length, 1, 'the top candidate disappeared');

  n['finder-root'].listeners.click({ target: n['finder-stop'], preventDefault() {} });
  for (let i = 0; i < 6; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  assert.strictEqual(find(n['finder-ranked'], 'a').length, 1, 'the ranked project link disappeared');
  assert.strictEqual(n['finder-export-json'].href, '/api/v1/finder/sessions/abc123/results',
    'the export link was mangled');

  const all = [].concat.apply([], IDS.map(function (id) { return [n[id]].concat(walk(n[id])); }));
  const writes = all.filter(function (x) { return x._htmlWrites && x._htmlWrites.length; });
  assert.ok(!writes.length, 'finder.js wrote to an HTML string sink: ' +
    JSON.stringify(writes.map(function (x) { return x._htmlWrites; })));
  assert.ok(!all.some(function (x) {
    return x.nodeType === 1 && ['img', 'script', 'b', 'i'].indexOf(x.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
  const text = IDS.map(function (id) { return n[id].textContent; }).join(' ');
  [HOSTILE_NAME, HOSTILE_UNKNOWN, HOSTILE_FILTER].forEach(function (needle) {
    assert.ok(text.indexOf(needle.replace(/<[^>]+>/g, '')) !== -1 || text.indexOf(needle) !== -1,
      'hostile field was not literal text: ' + needle);
  });
  const links = all.filter(function (x) { return x.nodeType === 1 && x.attrs.href && /^\/projects\//.test(x.attrs.href); });
  assert.ok(links.length >= 2, 'the project links disappeared');
  links.forEach(function (a) {
    assert.strictEqual(a.attrs.href, '/projects/' + HOSTILE_SLUG, 'the hostile slug was mangled in a link');
  });
  const why = all.filter(function (x) { return x.nodeType === 1 && Object.prototype.hasOwnProperty.call(x.attrs, 'data-why'); });
  assert.strictEqual(why[0].attrs['data-why'], HOSTILE_SLUG, 'the hostile why attribute was mangled');
  const lift = all.filter(function (x) { return x.nodeType === 1 && Object.prototype.hasOwnProperty.call(x.attrs, 'data-lift'); });
  assert.strictEqual(lift[0].attrs['data-lift'], HOSTILE_FILTER, 'the hostile lift attribute was mangled');

  // 401 refusal must render a sign-in link as an element, not as HTML text.
  const bad = makeSandbox(true);
  vm.runInContext(domSource, bad.sandbox, { filename: domPath });
  vm.runInContext(pageSource, bad.sandbox, { filename: pagePath });
  bad.nodes['finder-root'].listeners.submit({ target: bad.nodes['finder-seed-form'], preventDefault() {} });
  for (let i = 0; i < 6; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  const err = bad.nodes['finder-error'];
  assert.ok(find(err, 'a').length === 1, 'the sign-in link disappeared from the 401 refusal');
  assert.ok(err.textContent.indexOf(HOSTILE_ERROR) !== -1, 'the hostile error was not literal text');
  assert.ok(!(err._htmlWrites || []).length, 'the failure path used a string sink');

  const sink = /(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/;
  assert.ok(!sink.test(pageSource), 'finder.js regained an HTML string sink');
  console.log('dom finder page: PASS — hostile fields and refusal are text, hrefs and data attrs verbatim, no string sink');
})().catch(function (err) {
  console.error('dom finder page: FAIL — ' + err.message);
  process.exitCode = 1;
});
