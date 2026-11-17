#!/usr/bin/env node
// Step 2b: executes the shipped new-project intent check and create form with hostile hits and all refusal branches.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/new-project.js';
const domSource = fs.readFileSync(domPath, 'utf8');
const pageSource = fs.readFileSync(pagePath, 'utf8');

function textNode(value) {
  const text = String(value);
  return { nodeType: 3, text: text, get textContent() { return text; } };
}
function walk(node, out) {
  out = out || [];
  (node.children || []).forEach(function (c) { out.push(c); if (c.nodeType === 1) walk(c, out); });
  return out;
}
function find(root, tag) { return walk(root).filter(function (n) { return n.nodeType === 1 && n.tagName === tag; }); }
function element(tag) {
  const n = {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {}, value: '',
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    removeAttribute(k) { delete this.attrs[k]; },
    appendChild(c) { this.children.push(c); return c; },
    replaceChildren() { this.children = Array.prototype.slice.call(arguments).filter(function (c) { return c != null; }); },
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

const HOSTILE_TITLE = '<script>alert(1)</script>';
const HOSTILE_URL = 'javascript:alert(2)';
const HOSTILE_INTENT = '<b>idea</b>';

function makeSandbox(opts) {
  const nodes = {
    intent: element('input'),
    'intent-results': element('div'),
    'create-form': element('form'),
    'create-status': element('p'),
    name: element('input'), slug: element('input'), description: element('textarea'),
  };
  nodes.name.value = 'Name'; nodes.slug.value = 'slug'; nodes.description.value = 'Desc';
  const sandbox = { console, Date, Math, Promise, Object, Array, String, Number, setTimeout: function (fn) { fn(); return 1; }, clearTimeout: function () {} };
  sandbox.document = {
    createElement: element, createTextNode: textNode,
    getElementById(id) { return nodes[id] || null; },
    addEventListener() {},
  };
  sandbox.window = sandbox;
  sandbox.location = { href: 'https://concord.polarisocial.xyz/projects/new' };
  sandbox.ConcordApi = {
    get() {
      if (opts.reject) return Promise.reject(new Error('offline'));
      return Promise.resolve(opts.answer === undefined ? {
        available: true, kind: 'projects', has_duplicate: true,
        similar: [
          { entity_id: 10, title: HOSTILE_TITLE, url: HOSTILE_URL, score: 0.9123 },
          { entity_id: 'bad id', title: 'Second project', score: 0.5 },
        ],
      } : opts.answer);
    },
    post() {
      if (opts.postError !== undefined) {
        const e = new Error(opts.postMessage || 'refused'); e.status = opts.postError;
        return Promise.reject(e);
      }
      return Promise.resolve({ slug: 'made-slug' });
    },
  };
  vm.createContext(sandbox);
  return { sandbox, nodes };
}
async function boot(opts) {
  const s = makeSandbox(opts || {});
  vm.runInContext(domSource, s.sandbox, { filename: domPath });
  vm.runInContext(pageSource, s.sandbox, { filename: pagePath });
  s.nodes.intent.value = opts && opts.noQuery ? 'ab' : HOSTILE_INTENT;
  s.nodes.intent.listeners.input();
  for (let i = 0; i < 6; i++) await new Promise(function (r) { setImmediate(r); });
  return s;
}
function noSinks(s) {
  const all = [s.nodes['intent-results'], s.nodes['create-status']]
    .concat(walk(s.nodes['intent-results']), walk(s.nodes['create-status']));
  const writes = all.filter(function (n) { return n._htmlWrites && n._htmlWrites.length; });
  assert.ok(!writes.length, 'new-project.js wrote to an HTML string sink: ' +
    JSON.stringify(writes.map(function (n) { return n._htmlWrites; })));
  assert.ok(!all.some(function (n) {
    return n.nodeType === 1 && ['script', 'b', 'img'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
}

(async function () {
  const good = await boot({});
  const links = find(good.nodes['intent-results'], 'a');
  assert.strictEqual(links.length, 2, 'the intent-check links disappeared');
  assert.strictEqual(links[0].attrs.href, HOSTILE_URL, 'the hostile hit URL was mangled');
  assert.strictEqual(links[0].text, HOSTILE_TITLE, 'the hostile hit title was not literal text');
  assert.strictEqual(links[1].attrs.href, '/projects/bad%20id', 'the fallback URL was mangled');
  assert.ok(good.nodes['intent-results'].text.indexOf('0.91') !== -1, 'the top score disappeared');
  assert.ok(good.nodes['intent-results'].text.indexOf('0.50') !== -1, 'the second score disappeared');
  assert.ok(good.nodes['intent-results'].text.indexOf('may already exist') !== -1,
    'the duplicate note disappeared');
  noSinks(good);

  const noHits = await boot({ answer: { available: true, similar: [] } });
  assert.strictEqual(find(noHits.nodes['intent-results'], 'code')[0].text, HOSTILE_INTENT,
    'the hostile intent was not literal text inside <code>');
  assert.strictEqual(find(noHits.nodes['intent-results'], 'script').length, 0,
    'the hostile intent was parsed into an element');
  noSinks(noHits);

  const unavailable = await boot({ answer: { available: false } });
  assert.ok(unavailable.nodes['intent-results'].text.indexOf('not available') !== -1,
    'the unavailable answer disappeared');
  noSinks(unavailable);

  const rejected = await boot({ reject: true });
  assert.ok(rejected.nodes['intent-results'].text.indexOf('could not be reached') !== -1,
    'the failed request disappeared');
  noSinks(rejected);

  const short = await boot({ noQuery: true });
  assert.strictEqual(short.nodes['intent-results'].text, '', 'a short query should clear results');
  noSinks(short);

  // The create form keeps its four distinct refusal messages and its redirect.
  for (const [status, needle] of [[403, 'Sign in first'], [409, 'That slug is taken'], [422, 'refused'], [0, 'could not be sent']]) {
    const s = makeSandbox({ postError: status || 0, postMessage: status === 0 ? undefined : 'refused' });
    vm.runInContext(domSource, s.sandbox, { filename: domPath });
    vm.runInContext(pageSource, s.sandbox, { filename: pagePath });
    s.nodes['create-form'].listeners.submit({ preventDefault() {} });
    for (let i = 0; i < 6; i++) await new Promise(function (r) { setImmediate(r); });
    assert.ok(s.nodes['create-status'].text.indexOf(needle) !== -1,
      'status ' + status + ' lost its message: ' + s.nodes['create-status'].text);
    noSinks(s);
  }
  const created = makeSandbox({});
  vm.runInContext(domSource, created.sandbox, { filename: domPath });
  vm.runInContext(pageSource, created.sandbox, { filename: pagePath });
  created.nodes['create-form'].listeners.submit({ preventDefault() {} });
  for (let i = 0; i < 6; i++) await new Promise(function (r) { setImmediate(r); });
  assert.strictEqual(created.sandbox.location.href, '/projects/made-slug', 'the create redirect disappeared');

  assert.ok(!/(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/.test(pageSource),
    'new-project.js regained an HTML string sink');
  console.log('dom new-project page: PASS — hostile hits are text, hrefs verbatim, all refusals kept, no string sink');
})().catch(function (e) {
  console.error('dom new-project page: FAIL — ' + e.message);
  process.exitCode = 1;
});
