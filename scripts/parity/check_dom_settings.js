#!/usr/bin/env node
// Step 2b: executes the shipped settings page with hostile values from all reads and a hostile write refusal.
// Every field must be a text node or a setAttribute value; no HTML string sink may be used.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/settings.js';
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
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {}, value: '', disabled: false,
    style: {},
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    removeAttribute(k) { delete this.attrs[k]; },
    appendChild(c) { this.children.push(c); return c; },
    replaceChildren() { this.children = Array.prototype.slice.call(arguments); },
    addEventListener(ev, fn) { this.listeners[ev] = fn; },
    focus() {},
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
const HOSTILE_CHARTER = B('charter value');
const HOSTILE_MEMBER = '<img src=x onerror=alert(1)>';
const HOSTILE_PROFILE = B('profile');
const HOSTILE_RATIONALE = '<script>alert(2)</script>';
const HOSTILE_TAG = B('theme tag');
const HOSTILE_TOKEN = B('invite token');
const HOSTILE_LANG = B('language');
const HOSTILE_ERROR = '<script>alert(3)</script>';

function get(url) {
  if (/\/charter$/.test(url)) return Promise.resolve({ QuorumRatio: 0.66, QuorumMin: 3, CustomField: HOSTILE_CHARTER });
  if (/\/members$/.test(url)) return Promise.resolve([{ username: HOSTILE_MEMBER, role: 'owner', is_moderator: true }]);
  if (/\/criteria-profiles$/.test(url)) return Promise.resolve([{ name: HOSTILE_PROFILE }]);
  if (/\/tags\/completeness$/.test(url)) return Promise.resolve({ score: 0.6, tags: 2, want_tags: 4, namespaced: 0, want_namespaced: 2, complete: false });
  if (/\/strategy\/weight-proposals$/.test(url)) return Promise.resolve([{ id: 5, feature_id: 9, weight_before: 1, weight_after: 2, rationale: HOSTILE_RATIONALE }]);
  if (/\/strategy\/themes$/.test(url)) return Promise.resolve([]);
  if (/\/strategy\/theme-proposals$/.test(url)) return Promise.resolve([{ id: 8, theme_tag: HOSTILE_TAG, weight_before: null, weight_after: 2, rationale: 'r', status: 'pending' }]);
  if (/\/features$/.test(url)) return Promise.resolve({ features: [{ id: 9, title: 'f', strategic_weight: 1 }] });
  if (/\/languages$/.test(url)) return Promise.resolve({ languages: [{ language: HOSTILE_LANG, pct: 70 }] });
  if (/\/tags$/.test(url)) return Promise.resolve({ tags: ['rust'] });
  if (/\/invites$/.test(url)) return Promise.resolve([{ id: 3, token: HOSTILE_TOKEN, uses: 0, max_uses: 1, expires_at: 0 }]);
  if (/\/api\/v1\/projects\/lorebook$/.test(url)) return Promise.resolve({ intake_mode: 'both', visibility: 'public' });
  return Promise.resolve({});
}

function makeSandbox(root, live, slugEl) {
  const sandbox = {
    console, Date, Math, Promise, Object, Array, String, Number, isFinite, isNaN, setTimeout,
    encodeURIComponent, decodeURIComponent, FormData: function () {},
  };
  sandbox.document = {
    createElement: element,
    createTextNode: textNode,
    getElementById(id) {
      if (id === 'settings-root') return root;
      if (id === 'settings-live') return live;
      return find(root, '#' + id)[0] || null;
    },
    querySelector(sel) { return sel === '[data-slug]' ? slugEl : null; },
    addEventListener() {},
  };
  sandbox.window = sandbox;
  sandbox.ConcordSession = { instance() { return { signedIn() { return true; } }; } };
  sandbox.ConcordApi = {
    get: get,
    request() { const e = new Error(HOSTILE_ERROR); e.status = 403; return Promise.reject(e); },
    post() { const e = new Error(HOSTILE_ERROR); e.status = 403; return Promise.reject(e); },
    put() { return Promise.resolve({}); },
  };
  vm.createContext(sandbox);
  return sandbox;
}

(async function () {
  const root = element('div');
  root.setAttribute('id', 'settings-root');
  root.setAttribute('aria-busy', 'true');
  const live = element('div');
  live.setAttribute('id', 'settings-live');
  const slugEl = element('span');
  slugEl.textContent = 'lorebook';
  const sandbox = makeSandbox(root, live, slugEl);
  vm.runInContext(domSource, sandbox, { filename: domPath });
  vm.runInContext(pageSource, sandbox, { filename: pagePath });
  for (let i = 0; i < 10; i++) await new Promise(function (resolve) { setImmediate(resolve); });

  assert.ok(!(root._htmlWrites || []).length && !(live._htmlWrites || []).length,
    'settings.js wrote to an HTML string sink: ' + JSON.stringify(root._htmlWrites || live._htmlWrites));
  const nodes = walk(root);
  assert.ok(!nodes.some(function (n) {
    return n.nodeType === 1 && ['img', 'script', 'b'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
  const text = root.textContent;
  [HOSTILE_MEMBER, HOSTILE_PROFILE, HOSTILE_RATIONALE, HOSTILE_TAG, HOSTILE_TOKEN, HOSTILE_LANG].forEach(function (needle) {
    assert.ok(text.indexOf(needle.replace(/<[^>]+>/g, '')) !== -1 || text.indexOf(needle) !== -1,
      'hostile field was not literal text: ' + needle);
  });
  assert.ok(nodes.some(function (n) {
    return n.nodeType === 1 && n.attrs.name === 'CustomField' && n.attrs.value === HOSTILE_CHARTER;
  }), 'the hostile charter value was not a verbatim input value');
  ['charter-form', 'propose-weight-form', 'propose-theme-form', 'visibility-form', 'tags-form', 'languages-form', 'invite-form', 'intake-mode']
    .forEach(function (id) {
      assert.ok(find(root, '#' + id).length === 1, 'the wire hook #' + id + ' disappeared');
    });
  assert.ok(!root.attrs['aria-busy'], 'the page left aria-busy set');
  assert.strictEqual(root.attrs['data-loads'], '1', 'the load counter did not advance');

  // Exercise the live-region failure path with a hostile server refusal.
  const consent = find(root, '[data-consent]')[0];
  assert.ok(consent, 'the consent control disappeared');
  consent.listeners.click();
  for (let i = 0; i < 4; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  assert.ok(!(live._htmlWrites || []).length, 'the refusal path used a string sink');
  assert.ok(live.textContent.indexOf(HOSTILE_ERROR) !== -1, 'the hostile refusal was not literal text');
  assert.ok(!walk(live).some(function (n) { return n.nodeType === 1 && n.tagName === 'script'; }),
    'the hostile refusal was parsed into an element');

  const sink = /(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/;
  assert.ok(!sink.test(pageSource), 'settings.js regained an HTML string sink');
  console.log('dom settings page: PASS — hostile fields and refusal are text, all wire hooks kept, no string sink');
})().catch(function (err) {
  console.error('dom settings page: FAIL — ' + err.message);
  process.exitCode = 1;
});
