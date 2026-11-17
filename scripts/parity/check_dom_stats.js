#!/usr/bin/env node
// Step 2b: executes the shipped stats page with hostile facet/project fields and four failing fetches.
// Every field must be a text node or a setAttribute value; no HTML string sink may be used.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/stats.js';
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
  const tag = /^[a-z]+$/.test(sel) ? sel : null;
  return walk(root).filter(function (n) {
    if (n.nodeType !== 1) return false;
    if (attr) return Object.prototype.hasOwnProperty.call(n.attrs, attr[1]);
    return n.tagName === (tag || sel);
  });
}
function element(tag) {
  return {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {},
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

const HOSTILE_FACET = '<b>facet</b>';
const HOSTILE_NAME = '<img src=x onerror=alert(1)>';
const HOSTILE_SLUG = 'x" onmouseover="alert(2)';

function makeSandbox(ids, failing) {
  const nodes = {};
  ids.forEach(function (id) {
    const n = element('div');
    n.setAttribute('id', id);
    nodes[id] = n;
  });
  const sandbox = { console, Date, Math, Promise, Object, Array, String, Number, encodeURIComponent };
  sandbox.document = {
    createElement: element,
    createTextNode: textNode,
    getElementById(id) { return nodes[id] || null; },
    addEventListener(ev, fn) { if (ev === 'DOMContentLoaded') sandbox._ready = fn; },
  };
  sandbox.window = sandbox;
  sandbox.ConcordApi = {
    get(path) {
      if (failing) return Promise.reject(new Error('boom'));
      if (/stats\/summary/.test(path)) {
        return Promise.resolve({
          features_by_status: [{ value: HOSTILE_FACET, total: 3, organic: 2, seeded: 1, unknown: 0 }],
          features_by_effort: [{ value: 'high', total: 3, organic: 2, seeded: 1, unknown: 0 }],
          complaints_by_status: [{ value: 'open', total: 4, organic: 3, seeded: 1, unknown: 0 }],
          projects_by_language: [{ value: HOSTILE_FACET, count: 2 }],
          projects_by_tag: [], projects_by_license: [], projects_by_governance: [],
          projects_by_visibility: [], members_by_role: [], documents_by_kind: [], arenas_by_type: []
        });
      }
      if (/stats\/responsiveness/.test(path)) {
        return Promise.resolve([{ slug: HOSTILE_SLUG, name: HOSTILE_NAME,
          newcomer_response_hours: 12.34, n: 7 }]);
      }
      if (/stats\/rising/.test(path)) {
        return Promise.resolve([{ slug: HOSTILE_SLUG, name: HOSTILE_NAME,
          features_per_week: 1.2, complaints_per_week: 0.3, health_score: null, days: 30 }]);
      }
      return Promise.resolve({ latency: {
        triage: { n: 3, median_hours: 12.34, unfinished: 1 },
        delivery: { n: 0, median_hours: 0, unfinished: 0 },
        decision: { n: 5, median_hours: 2.5, unfinished: 0, unmeasured: 1 }
      } });
    },
  };
  vm.createContext(sandbox);
  return { sandbox: sandbox, nodes: nodes };
}

(async function () {
  const ids = ['stats-summary', 'stats-latency', 'stats-responsiveness', 'stats-rising'];
  const good = makeSandbox(ids, false);
  vm.runInContext(domSource, good.sandbox, { filename: domPath });
  vm.runInContext(pageSource, good.sandbox, { filename: pagePath });
  good.sandbox._ready();
  for (let i = 0; i < 8; i++) await new Promise(function (resolve) { setImmediate(resolve); });

  const all = [].concat.apply([], ids.map(function (id) { return [good.nodes[id]].concat(walk(good.nodes[id])); }));
  const writes = all.filter(function (n) { return n._htmlWrites && n._htmlWrites.length; });
  assert.ok(!writes.length,
    'stats.js wrote to an HTML string sink: ' + JSON.stringify(writes.map(function (n) { return n._htmlWrites; })));
  assert.ok(!all.some(function (n) {
    return n.nodeType === 1 && ['img', 'b', 'script'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
  const summary = good.nodes['stats-summary'].textContent;
  [HOSTILE_FACET].forEach(function (needle) {
    assert.ok(summary.indexOf(needle.replace(/<[^>]+>/g, '')) !== -1,
      'hostile facet was not literal text: ' + needle);
  });
  ['Complaint triage', 'Features by status', 'Features by effort', 'Projects by language',
    'Newcomer responsiveness', 'Rising', 'n=3 · unfinished=1', '—'].forEach(function (needle) {
      const text = ids.map(function (id) { return good.nodes[id].textContent; }).join(' ');
      assert.ok(text.indexOf(needle) !== -1, 'expected text disappeared: ' + needle);
    });
  const projLinks = [].concat.apply([], ids.map(function (id) {
    return find(good.nodes[id], 'a').filter(function (n) { return n.attrs.href; });
  }));
  assert.strictEqual(projLinks.length, 2, 'the two project links disappeared');
  projLinks.forEach(function (a) {
    assert.strictEqual(a.attrs.href, '/projects/' + encodeURIComponent(HOSTILE_SLUG),
      'the hostile slug was mangled in a link');
    assert.strictEqual(a.textContent, HOSTILE_NAME, 'the hostile name was not literal text');
  });

  // All four failure arms must still render their own empty state.
  const bad = makeSandbox(ids, true);
  vm.runInContext(domSource, bad.sandbox, { filename: domPath });
  vm.runInContext(pageSource, bad.sandbox, { filename: pagePath });
  bad.sandbox._ready();
  for (let i = 0; i < 8; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  ids.forEach(function (id) {
    const n = bad.nodes[id];
    assert.ok(n.textContent.indexOf('unavailable') !== -1,
      'the failure state for ' + id + ' disappeared: ' + n.textContent.slice(0, 60));
    assert.ok(!(n._htmlWrites || []).length, 'the failure path used a string sink');
  });

  const sink = /(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/;
  assert.ok(!sink.test(pageSource), 'stats.js regained an HTML string sink');
  console.log('dom stats page: PASS — hostile facets and projects are text, hrefs verbatim, failure states kept');
})().catch(function (err) {
  console.error('dom stats page: FAIL — ' + err.message);
  process.exitCode = 1;
});
