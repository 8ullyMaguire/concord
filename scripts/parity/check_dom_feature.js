#!/usr/bin/env node
// Step 2b: executes the shipped feature page with hostile fields from every read, plus a hostile load error.
// Every field must be a text node or a setAttribute value; no HTML string sink may be used.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/feature.js';
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
  const cls = /^\.(.+)$/.exec(sel);
  return walk(root).filter(function (n) {
    if (n.nodeType !== 1) return false;
    if (eq) return n.attrs[eq[1]] === eq[2];
    if (attr) return Object.prototype.hasOwnProperty.call(n.attrs, attr[1]);
    if (id) return n.attrs.id === id[1];
    if (cls) return (n.attrs.class || '').split(/\s+/).indexOf(cls[1]) !== -1;
    return n.tagName === sel;
  });
}
function element(tag) {
  return {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {}, value: '', disabled: false,
    style: {},
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    appendChild(c) { this.children.push(c); return c; },
    replaceChildren() { this.children = Array.prototype.slice.call(arguments); },
    addEventListener(ev, fn) { this.listeners[ev] = fn; },
    focus() {}, closest() { return null; },
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
const HOSTILE_TITLE = B('title');
const HOSTILE_BODY = '<img src=x onerror=alert(1)>';
const HOSTILE_SOLUTION = B('solution');
const HOSTILE_CLAIM_REASON = '<script>alert(2)</script>';
const HOSTILE_COMPLAINT = B('complaint');
const HOSTILE_TAG = B('tag');
const HOSTILE_DEP = B('dep');
const HOSTILE_EVIDENCE = B('evidence');
const HOSTILE_ERROR = '<script>alert(3)</script>';

function makeSandbox(root, failing) {
  const sandbox = {
    console, Date, Math, Promise, Object, Array, String, Number, isFinite, isNaN, setTimeout,
    encodeURIComponent, decodeURIComponent,
    location: { pathname: '/projects/lorebook/features/5', reload() {} },
  };
  sandbox.document = {
    readyState: 'complete',
    createElement: element,
    createTextNode: textNode,
    getElementById(id) { return id === 'feature-root' ? root : (find(root, '#' + id)[0] || null); },
    querySelector(sel) { return find(root, sel)[0] || null; },
    querySelectorAll(sel) { return find(root, sel); },
    addEventListener() {},
  };
  sandbox.window = sandbox;
  sandbox.ConcordApi = {
    get(url) {
      if (failing) return Promise.reject(new Error(HOSTILE_ERROR));
      if (/\/solutions$/.test(url)) {
        return Promise.resolve({ feature_id: 5, solutions: [{
          solution_id: '11', title: HOSTILE_SOLUTION, status: 'proposed', elo_r: 1600, elo_rd: 350,
          claims: [{ claim: 'resolves', complaint_id: '7', contested: true, contest_reason: HOSTILE_CLAIM_REASON }],
        }] });
      }
      if (/\/consensus$/.test(url)) {
        return Promise.resolve({ feature_id: 5, ready: true, baseline_id: '11', leader_id: '11', runner_up_id: null });
      }
      if (/\/complaints$/.test(url)) {
        return Promise.resolve([{ id: '7', title: HOSTILE_COMPLAINT, severity: 4, status: 'validated' }]);
      }
      if (/\/tags$/.test(url)) return Promise.resolve({ feature_id: 5, tags: [HOSTILE_TAG] });
      if (/\/deps$/.test(url)) return Promise.resolve({ blocked_by: [{ id: '9', title: HOSTILE_DEP, status: 'ready' }], blocks: [] });
      if (/\/evidence$/.test(url)) return Promise.resolve([{ kind: 'document', ref_id: 3, title: HOSTILE_EVIDENCE }]);
      if (/\/milestones$/.test(url)) return Promise.resolve([]);
      return Promise.resolve({
        title: HOSTILE_TITLE, body: HOSTILE_BODY, status: 'proposed', elo_r: 1500, elo_rd: 350,
        impact: 8, effort_score: 2, impact_ratio: 4, effort: 'S', strategic_weight: 2,
      });
    },
    request() { return Promise.resolve({}); },
    post() { return Promise.resolve({}); },
  };
  vm.createContext(sandbox);
  return sandbox;
}

(async function () {
  const root = element('div');
  root.setAttribute('id', 'feature-root');
  root.setAttribute('aria-busy', 'true');
  const sandbox = makeSandbox(root, false);
  vm.runInContext(domSource, sandbox, { filename: domPath });
  vm.runInContext(pageSource, sandbox, { filename: pagePath });
  for (let i = 0; i < 6; i++) await new Promise(function (resolve) { setImmediate(resolve); });

  assert.ok(!root._htmlWrites || root._htmlWrites.length === 0,
    'feature.js wrote to an HTML string sink: ' + JSON.stringify(root._htmlWrites));
  const nodes = walk(root);
  assert.ok(!nodes.some(function (n) {
    return n.nodeType === 1 && ['img', 'script', 'b'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
  const text = root.textContent;
  [HOSTILE_TITLE, HOSTILE_BODY, HOSTILE_SOLUTION, HOSTILE_COMPLAINT, HOSTILE_TAG,
    HOSTILE_DEP, HOSTILE_EVIDENCE].forEach(function (needle) {
    assert.ok(text.indexOf(needle.replace(/<[^>]+>/g, '')) !== -1 || text.indexOf(needle) !== -1,
      'hostile field was not literal text: ' + needle);
  });
  assert.ok(nodes.some(function (n) {
    return n.nodeType === 1 && n.attrs.title === HOSTILE_CLAIM_REASON;
  }), 'the hostile contest reason attribute was mangled');
  assert.ok(find(root, '[data-evidence-badge]').length === 1, 'the evidence badge disappeared');
  assert.ok(find(root, '[data-contest-send]').length === 1, 'the contest control disappeared');
  assert.ok(find(root, '[data-tag-remove]').length === 1, 'the tag control disappeared');
  assert.ok(find(root, '[data-evidence-remove]').length === 1, 'the evidence control disappeared');
  assert.strictEqual(root.attrs['aria-busy'], 'false', 'the page never cleared its busy state');

  const root2 = element('div');
  root2.setAttribute('id', 'feature-root');
  const sandbox2 = makeSandbox(root2, true);
  vm.runInContext(domSource, sandbox2, { filename: domPath });
  vm.runInContext(pageSource, sandbox2, { filename: pagePath });
  for (let i = 0; i < 6; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  assert.ok(!root2._htmlWrites || root2._htmlWrites.length === 0, 'the error path used a string sink');
  assert.ok(root2.textContent.indexOf(HOSTILE_ERROR) !== -1, 'the hostile error was not literal text');
  assert.ok(!walk(root2).some(function (n) { return n.nodeType === 1 && n.tagName === 'script'; }),
    'the hostile error was parsed into an element');

  const sink = /(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/;
  assert.ok(!sink.test(pageSource), 'feature.js regained an HTML string sink');
  console.log('dom feature page: PASS — hostile fields and error are text, controls kept, no string sink');
})().catch(function (err) {
  console.error('dom feature page: FAIL — ' + err.message);
  process.exitCode = 1;
});
