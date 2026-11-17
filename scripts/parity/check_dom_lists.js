#!/usr/bin/env node
// Step 2b: executes the shipped project-lists page with hostile list fields and all failure states.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/lists.js';
const domSource = fs.readFileSync(domPath, 'utf8');
const pageSource = fs.readFileSync(pagePath, 'utf8');

function textNode(value) {
  const text = String(value);
  return { nodeType: 3, text: text, get textContent() { return text; } };
}
function matches(n, sel) {
  const cls = /^\.(.+)$/.exec(sel);
  const attr = /^\[([^=\]]+)\]$/.exec(sel);
  const tagCls = /^([a-z]+)\.(.+)$/.exec(sel);
  if (tagCls) return n.tagName === tagCls[1] && (n.attrs.class || '').split(' ').indexOf(tagCls[2]) !== -1;
  if (cls) return (n.attrs.class || '').split(' ').indexOf(cls[1]) !== -1;
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
  if (child.nodeType) { n.children.push(child); return; }
  n.children.push(textNode(child));
}
function element(tag) {
  const n = {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {},
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    removeAttribute(k) { delete this.attrs[k]; },
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

const HOSTILE_NAME = '<img src=x onerror=alert(1)>';
const HOSTILE_DESC = '<script>alert(2)</script>';
const HOSTILE_STATUS = '<b>open</b>';

function boot(opts) {
  const root = element('div'); root.setAttribute('id', 'lists-root'); root.setAttribute('aria-busy', 'true');
  const slug = element('span'); slug.setAttribute('data-slug', ''); slug.textContent = 'lorebook';
  const sandbox = { console, Date, Math, Promise, Object, Array, String, Number, encodeURIComponent };
  sandbox.document = {
    createElement: element, createTextNode: textNode,
    getElementById(id) { return id === 'lists-root' ? root : null; },
    querySelector(sel) { return sel === '[data-slug]' ? slug : null; },
    addEventListener() {},
  };
  sandbox.window = sandbox;
  sandbox.ConcordApi = {
    get(url) {
      if (opts.notFound) { const e = new Error('missing'); e.status = 404; return Promise.reject(e); }
      if (opts.fail) { const e = new Error('offline'); e.status = 500; return Promise.reject(e); }
      return Promise.resolve(opts.answer === undefined ? [
        { id: 7, name: HOSTILE_NAME, description: HOSTILE_DESC, status: HOSTILE_STATUS,
          project_id: 0, updated_at: Date.now() / 1000 - 7200 },
        { id: 8, name: 'Second list', status: 'open', project_id: 1, updated_at: 0 },
      ] : opts.answer);
    },
  };
  vm.createContext(sandbox);
  vm.runInContext(domSource, sandbox, { filename: domPath });
  vm.runInContext(pageSource, sandbox, { filename: pagePath });
  return { sandbox, root };
}
function settle(s) { return new Promise(function (resolve) { setImmediate(function () { setImmediate(resolve); }); }); }
function noSinks(s) {
  const all = [s.root].concat(walk(s.root));
  const writes = all.filter(function (n) { return n._htmlWrites && n._htmlWrites.length; });
  assert.ok(!writes.length, 'lists.js wrote to an HTML string sink: ' +
    JSON.stringify(writes.map(function (n) { return n._htmlWrites; })));
  assert.ok(!all.some(function (n) {
    return n.nodeType === 1 && ['img', 'script', 'b'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
}

(async function () {
  const good = boot({});
  await settle(good);
  assert.strictEqual(find(good.root, 'article.card').length, 2, 'the list cards disappeared');
  const first = find(good.root, 'article.card')[0];
  assert.strictEqual(find(first, 'a')[0].attrs.href, '/projects/lorebook/lists/7',
    'the card link was mangled');
  assert.strictEqual(find(first, 'a')[0].textContent, HOSTILE_NAME,
    'the hostile list name was not literal text');
  assert.strictEqual(find(first, '.card-text')[0].textContent, HOSTILE_DESC,
    'the hostile description was not literal text');
  assert.strictEqual(find(first, '.badge')[0].textContent, 'instance-wide',
    'the instance-wide label disappeared');
  assert.strictEqual(find(first, '.badge')[1].textContent, HOSTILE_STATUS,
    'the hostile status was not literal text');
  assert.ok(find(first, '.card-meta')[0].textContent.indexOf('2h ago') !== -1,
    'the relative update time disappeared');
  assert.strictEqual(good.root.getAttribute('aria-busy'), null, 'aria-busy was left on');
  noSinks(good);

  const empty = boot({ answer: [] });
  await settle(empty);
  assert.ok(empty.root.textContent.indexOf('No lists yet') !== -1, 'the empty state disappeared');
  noSinks(empty);

  const notFound = boot({ notFound: true });
  await settle(notFound);
  assert.ok(notFound.root.textContent.indexOf('no lists at this address') !== -1,
    'the not-found state disappeared');
  noSinks(notFound);

  const failed = boot({ fail: true });
  await settle(failed);
  assert.ok(failed.root.textContent.indexOf('Lists could not be loaded') !== -1,
    'the failure state disappeared');
  noSinks(failed);

  const odd = boot({ answer: { not: 'an array' } });
  await settle(odd);
  assert.ok(odd.root.textContent.indexOf('No lists yet') !== -1,
    'a non-array answer should degrade to the empty state');
  noSinks(odd);

  assert.ok(!/(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/.test(pageSource),
    'lists.js regained an HTML string sink');
  console.log('dom lists page: PASS — hostile fields are text, href verbatim, all failure states kept, no string sink');
})().catch(function (e) {
  console.error('dom lists page: FAIL — ' + e.message);
  process.exitCode = 1;
});
