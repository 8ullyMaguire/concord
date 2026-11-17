#!/usr/bin/env node
// Step 2b / D-16: project.js's outer states must be DOM nodes, not parsed HTML.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const skeletonPath = process.argv[3] || 'crates/web/assets/js/skeleton.js';
const rendererPath = process.argv[4] || 'crates/web/assets/js/project-render.js';
const pagePath = process.argv[5] || 'crates/web/assets/js/project.js';
const sources = [domPath, skeletonPath, rendererPath, pagePath].map(function (p) {
  return { path: p, source: fs.readFileSync(p, 'utf8') };
});

function textNode(value) {
  const text = String(value);
  return { nodeType: 3, text: text, get textContent() { return text; } };
}
function matches(n, sel) {
  const cls = /^\.(.+)$/.exec(sel);
  const id = /^#(.+)$/.exec(sel);
  if (id) return n.attrs.id === id[1];
  if (cls) return (n.attrs.class || '').split(' ').indexOf(cls[1]) !== -1;
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
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {}, value: '',
    get className() { return this.attrs.class || ''; },
    set className(v) { this.attrs.class = String(v); },
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; },
    hasAttribute(k) { return k in this.attrs; },
    appendChild(c) { append(this, c); return c; },
    replaceChildren() { this.children = []; Array.prototype.slice.call(arguments).forEach(function (c) { append(n, c); }); },
    addEventListener(ev, fn) { this.listeners[ev] = fn; },
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
const HOSTILE_SLUG = '<img src=x onerror=alert(1)>';
const HOSTILE_TITLE = '<script>alert(1)</script>';
const HOSTILE_BODY = '<img src=x onerror=alert(2)>';
const HOSTILE_LABEL = '<b>hostile capability</b>';

function boot(pathname, fail, opts) {
  opts = opts || {};
  const box = element('div'); box.setAttribute('id', 'project-detail');
  const calls = [];
  let released;
  const held = new Promise(function (resolve) { released = resolve; });
  const sandbox = {
    console, Date, Math, Promise, Object, Array, String, Number, JSON, isFinite,
    encodeURIComponent, decodeURIComponent, setTimeout: function (fn) { fn(); return 1; },
    clearTimeout: function () {},
    location: { pathname: pathname, href: 'https://concord.polarisocial.xyz' + pathname },
  };
  sandbox.document = {
    createElement: element, createTextNode: textNode, title: '',
    getElementById(id) { return id === 'project-detail' ? box : null; },
    addEventListener(ev, fn) { this[ev] = fn; },
  };
  sandbox.window = sandbox;
  sandbox.ConcordEscape = { esc: function (s) { return String(s); } };
  sandbox.ConcordSession = { instance: function () { return { signedIn: function () { return !!opts.signedIn; } }; } };
  sandbox.ConcordPanels = {
    intakeMode: function () { return 'complaint_first'; },
    intakeNote: function () { return 'Pick a complaint this feature answers.'; },
    wireWorkItems: function () {},
  };
  sandbox.ConcordWrites = {
    setStatus: function () {}, postJSON: function () { return Promise.resolve({}); },
    showCardRefusal: function () {}, wire: function () {}, wireFieldReports: function () {},
  };
  sandbox.ConcordArena = { controls: function () { return ''; }, wire: function () {} };
  sandbox.ConcordApi = {
    get(url) {
      calls.push('GET ' + url);
      if (fail && fail.first) return Promise.reject(fail.first);
      if (fail && fail.rest && calls.length > 1) return Promise.reject(fail.rest);
      if (fail && fail.hold && calls.length > 1) return held;
      if (/\/features$/.test(url)) return Promise.resolve([
        { id: 7, title: HOSTILE_TITLE, body: HOSTILE_BODY, elo_r: 1500, elo_rd: 40, status: 'proposed' },
      ]);
      if (/\/complaints$/.test(url)) return Promise.resolve([
        { id: 11, title: HOSTILE_TITLE, body: HOSTILE_BODY, status: 'open', severity: 4 },
      ]);
      if (/\/documents/.test(url)) return Promise.resolve([]);
      if (/\/capabilities$/.test(url)) return Promise.resolve({
        unknown_count: 1,
        capabilities: [{ key: 'wip', label: HOSTILE_LABEL, category: 'flow', asserted: true,
          value: 'yes', state: 'disputed', disputes: 2, evidence: HOSTILE_BODY }],
      });
      if (/\/field-reports$/.test(url)) return Promise.resolve({
        outcome_rate: 0.8, sample_size: 5, omitted: 1, shown: 1,
        by_environment: { docker: 0.8 },
        reports: [{ id: 21, use_case: HOSTILE_TITLE, outcome: 'worked', version: '1.0',
          caveats: HOSTILE_BODY, advice: HOSTILE_BODY,
          responses: [{ body: HOSTILE_BODY, is_owner: true }] }],
      });
      if (/\/solutions$/.test(url)) return Promise.resolve({
        slug: 'lorebook', features_with_solutions: 1, features_without: 0,
        consensus_calls: [{ id: 9 }],
        features: [{ feature_id: 7, feature_title: HOSTILE_TITLE,
          solutions: [{ solution_id: 31, title: HOSTILE_TITLE, coverage: 0.7, rating: 1500.25 }] }],
      });
      if (/\/alternatives$/.test(url)) return Promise.resolve({
        arenas: [{ arena_id: 4, use_case: HOSTILE_TITLE, entries: [
          { rank: 1, slug: 'other', title: HOSTILE_TITLE, rating: 1500, better_for: HOSTILE_BODY, worse_for: '' },
        ] }],
      });
      if (/\/requests$/.test(url)) return Promise.resolve([
        { id: 51, title: HOSTILE_TITLE, body: HOSTILE_BODY, status: 'open' },
      ]);
      if (/\/lists$/.test(url)) return Promise.resolve([
        { id: 61, name: HOSTILE_TITLE, description: HOSTILE_BODY, status: 'open' },
      ]);
      if (/\/work-items$/.test(url)) return Promise.resolve([
        { kind: 'feature', id: 71, title: HOSTILE_TITLE, status: 'ready', time_budget_minutes: 60,
          difficulty: 'M', needs_mentor: true },
      ]);
      if (/\/roadmap$/.test(url)) return Promise.resolve({ milestones: [
        { id: 81, title: HOSTILE_TITLE, status: 'active', target_date: 1800000000, open: 2, total: 5, at_risk: true },
      ] });
      return Promise.resolve({
        id: 1, slug: 'lorebook', name: 'Lorebook', governance_model: 'governed', license: 'MIT',
        health_score: 0.82, newcomer_response_hours: 3.5, newcomer_sample: 7,
        created_at: 1700000000, updated_at: 1700000100,
      });
    },
  };
  vm.createContext(sandbox);
  sources.forEach(function (f) { vm.runInContext(f.source, sandbox, { filename: f.path }); });
  sandbox.document.DOMContentLoaded();
  return { sandbox, box, calls, release: function () { released([]); } };
}
function settle() {
  return new Promise(function (resolve) {
    setImmediate(function () { setImmediate(function () { setImmediate(resolve); }); });
  });
}
function noSinks(s) {
  const all = [s.box].concat(walk(s.box));
  const writes = all.filter(function (n) { return n._htmlWrites && n._htmlWrites.length; });
  assert.ok(!writes.length, 'project.js outer state wrote to an HTML string sink: ' +
    JSON.stringify(writes.map(function (n) { return n._htmlWrites; })));
  assert.strictEqual(find(s.box, 'img').length, 0, 'a hostile slug was parsed into an element');
}

(async function () {
  const missing = boot('/projects/', null);
  assert.ok(missing.box.textContent.indexOf('Project not found') !== -1,
    'an empty slug did not render the not-found state');
  noSinks(missing);

  const notFound = boot('/projects/' + encodeURIComponent(HOSTILE_SLUG),
    { first: { status: 404, notFound: true } });
  await settle();
  assert.ok(notFound.box.textContent.indexOf(HOSTILE_SLUG) !== -1,
    'the hostile slug was not rendered as literal text');
  assert.strictEqual(find(notFound.box, 'a')[0].attrs.href, '/projects',
    'the browse-projects link was mangled');
  noSinks(notFound);

  const loading = boot('/projects/lorebook', { hold: true });
  await settle();
  assert.strictEqual(find(loading.box, '.skeleton-title').length, 1,
    'the skeleton title disappeared');
  assert.strictEqual(find(loading.box, '.skeleton-text').length, 3,
    'the three skeleton lines disappeared');
  noSinks(loading);
  loading.release();

  const auth = boot('/projects/lorebook', { rest: { status: 401 } });
  await settle();
  assert.ok(auth.box.textContent.indexOf('Sign in to see features') !== -1,
    'the auth refusal disappeared');
  assert.strictEqual(find(auth.box, 'a')[0].attrs.href, '/login',
    'the sign-in link was mangled');
  noSinks(auth);

  const failure = boot('/projects/lorebook', { first: { status: 503 } });
  await settle();
  assert.ok(failure.box.textContent.indexOf('Could not load project (HTTP 503)') !== -1,
    'the HTTP status was not reported');
  noSinks(failure);

  // The populated page: every panel is a node tree, with hostile values as text.
  const full = boot('/projects/lorebook', null, { signedIn: true });
  await settle();
  const anchors = find(full.box, 'a');
  const featureLink = anchors.filter(function (n) {
    return n.attrs.href === '/projects/lorebook/features/7';
  })[0];
  assert.ok(featureLink && featureLink.textContent === HOSTILE_TITLE,
    'the feature title link was missing or parsed');
  assert.ok(anchors.some(function (n) {
    return n.attrs.href === '/projects/lorebook/milestones/81' && n.textContent.indexOf('Next milestone') !== -1;
  }), 'the next-milestone card link disappeared');
  assert.strictEqual(find(full.box, '.panel-capabilities').length, 1, 'the capability panel disappeared');
  assert.strictEqual(find(full.box, '.panel-field-reports').length, 1, 'the field-report panel disappeared');
  assert.strictEqual(find(full.box, '.panel-solutions').length, 1, 'the solutions panel disappeared');
  assert.strictEqual(find(full.box, '.panel-alternatives').length, 1, 'the alternatives panel disappeared');
  assert.strictEqual(find(full.box, '#complaint-form').length, 1, 'the complaint form disappeared');
  assert.strictEqual(find(full.box, '#feature-form')[0].attrs['data-intake-mode'], 'complaint_first',
    'the intake mode was not carried to the form');
  assert.ok(find(full.box, 'option').some(function (n) { return n.textContent === HOSTILE_TITLE; }),
    'the hostile complaint title was not literal option text');
  const buttons = find(full.box, 'button');
  assert.ok(buttons.some(function (n) { return 'data-arena-pair' in n.attrs; }),
    'the arena controls disappeared');
  assert.ok(buttons.some(function (n) { return 'data-claim' in n.attrs; }),
    'the work-item claim control disappeared');
  assert.ok(anchors.some(function (n) {
    return n.attrs.href === '/projects/lorebook/lists/61' && n.textContent === HOSTILE_TITLE;
  }), 'the list card link disappeared');
  noSinks(full);
  assert.strictEqual(find(full.box, 'script').length, 0, 'a hostile field became a script element');
  assert.strictEqual(find(full.box, 'img').length, 0, 'a hostile field became an img element');

  const pageSource = sources[3].source;
  const rendererSource = sources[2].source;
  assert.ok(!/(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/.test(rendererSource),
    'project-render.js regained an HTML string sink');
  assert.ok(!/if \(!slug\) \{ box\.innerHTML/.test(pageSource), 'empty-slug path regained an HTML string sink');
  assert.ok(!/box\.innerHTML = \(window\.ConcordSkeleton/.test(pageSource), 'loading path regained an HTML string sink');
  assert.ok(!/box\.innerHTML = notFound/.test(pageSource), 'not-found path regained an HTML string sink');
  assert.ok(!/box\.innerHTML = '<div class="empty-state"/.test(pageSource),
    'a failure path regained an HTML string sink');
  console.log('dom project page: PASS — all panels and states are nodes, hostile values are text, no string sink');
})().catch(function (e) {
  console.error('dom project page: FAIL — ' + e.message);
  process.exitCode = 1;
});
