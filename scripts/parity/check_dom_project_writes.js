#!/usr/bin/env node
// Step 2b: executes the shipped project write helpers with hostile duplicate matches and every refusal path.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/project-writes.js';
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
  const compound = /^([a-z]+)\[([^=\]]+)=["']?([^"'\]]+)["']?\]$/.exec(sel);
  if (compound) return n.tagName === compound[1] && n.attrs[compound[2]] === compound[3];
  if (bare) return n.attrs[bare[1]] === bare[2];
  if (cls) return (n.attrs.class || '').split(' ').indexOf(cls[1]) !== -1;
  if (eq) return n.attrs[eq[1]] === eq[2];
  if (attr) return Object.prototype.hasOwnProperty.call(n.attrs, attr[1]);
  return n.tagName === sel;
}
function append(n, child) {
  if (child == null) return;
  if (Array.isArray(child)) return child.forEach(function (c) { append(n, c); });
  if (child.nodeType) { child.parent = n; n.children.push(child); return; }
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
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {}, value: '', hidden: false,
    get parentNode() { return this.parent; },
    get className() { return this.attrs.class || ''; },
    set className(v) { this.attrs.class = String(v); },
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

const HOSTILE_TITLE = '<img src=x onerror=alert(1)>';
const HOSTILE_URL = 'javascript:alert(2)';
const HOSTILE_NOTE = '<script>alert(3)</script>';

function makeSandbox(opts) {
  const title = element('input'); title.setAttribute('name', 'title'); title.value = opts.query || '';
  const panel = element('div'); panel.setAttribute('data-dup-panel', 'complaint');
  const form = element('form'); form.appendChild(title); form.appendChild(panel);
  const status = element('p'); status.setAttribute('data-status', '');
  form.appendChild(status);
  const card = element('div');
  const btn = element('button'); card.appendChild(btn);
  const fileForm = element('form'); fileForm.setAttribute('id', 'field-report-form');
  const err = element('p'); err.setAttribute('data-fr-error', '');
  const fileBtn = element('button'); fileBtn.setAttribute('data-file-field-report', '');
  fileForm.appendChild(err); fileForm.appendChild(fileBtn);
  const ids = ['fr-outcome', 'fr-migrated-to', 'fr-version', 'fr-environment', 'fr-use-case', 'fr-caveats', 'fr-advice'];
  const fields = {};
  ids.forEach(function (id) {
    const n = element('input'); n.setAttribute('id', id); n.value = id === 'fr-outcome' ? 'worked' : '';
    fields[id] = n;
  });
  const replyForm = element('form'); replyForm.setAttribute('data-respond-report', '55');
  const replyErr = element('p'); replyErr.setAttribute('data-fr-error', '');
  const replyBtn = element('button'); replyBtn.setAttribute('data-submit-response', '');
  const replyField = element('input'); replyField.setAttribute('data-response-body', ''); replyField.value = 'saw it';
  replyForm.appendChild(replyErr); replyForm.appendChild(replyBtn); replyForm.appendChild(replyField);
  const nodes = Object.assign({ 'field-report-form': fileForm }, fields);
  const calls = [];
  const sandbox = {
    console, Date, Math, Promise, Object, Array, String, Number, setTimeout: function (fn) { fn(); return 1; },
    clearTimeout: function () {}, encodeURIComponent, FormData: function () {},
  };
  sandbox.document = {
    createElement: element, createTextNode: textNode,
    getElementById(id) { return nodes[id] || null; },
    querySelectorAll(sel) { return sel === 'form[data-respond-report]' ? [replyForm] : []; },
    addEventListener() {},
  };
  sandbox.window = sandbox;
  sandbox.location = { href: 'https://concord.polarisocial.xyz/projects/lorebook', pathname: '/projects/lorebook' };
  sandbox.ConcordEscape = { esc: function (s) { return String(s); } };
  sandbox.ConcordApi = {
    get() {
      calls.push('GET similar');
      if (opts.reject) return Promise.reject(new Error('offline'));
      return Promise.resolve(opts.answer === undefined ? {
        available: true, has_duplicate: true,
        coverage: { coverage: 0.25 },
        similar: [{ title: HOSTILE_TITLE, url: HOSTILE_URL, score: 0.87 }],
      } : opts.answer);
    },
    post(path, body) { calls.push('POST ' + path + ' ' + JSON.stringify(body)); return opts.postReject
      ? Promise.reject(new Error(HOSTILE_NOTE)) : Promise.resolve({}); },
  };
  vm.createContext(sandbox);
  return { sandbox, form, panel, status, card, btn, fileForm, fileBtn, err, fields, replyForm, replyErr, replyBtn, replyField, calls };
}
async function boot(opts) {
  const s = makeSandbox(opts || {});
  vm.runInContext(domSource, s.sandbox, { filename: domPath });
  vm.runInContext(pageSource, s.sandbox, { filename: pagePath });
  const W = s.sandbox.ConcordWrites;
  W.panel('complaint');
  W.wire(s.form, 'complaint', 42);
  W.wireFieldReports('lorebook');
  s.title = s.form.querySelector('input[name=title]');
  s.title.value = opts && opts.query ? opts.query : 'search returns nothing for an empty query';
  s.title.listeners.input();
  for (let i = 0; i < 8; i++) await new Promise(function (r) { setImmediate(r); });
  return s;
}
function noSinks(s) {
  const roots = [s.panel, s.status, s.card, s.fileForm, s.replyForm];
  const all = roots.reduce(function (a, r) { return a.concat([r], walk(r)); }, []);
  const writes = all.filter(function (n) { return n._htmlWrites && n._htmlWrites.length; });
  assert.ok(!writes.length, 'project-writes.js wrote to an HTML string sink: ' +
    JSON.stringify(writes.map(function (n) { return n._htmlWrites; })));
  assert.ok(!all.some(function (n) {
    return n.nodeType === 1 && ['img', 'script'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
}

(async function () {
  const good = await boot({});
  assert.ok(good.panel.textContent.indexOf('This looks like an existing complaint') !== -1,
    'the duplicate warning disappeared');
  const link = find(good.panel, 'a')[0];
  assert.ok(link, 'the duplicate link disappeared');
  assert.strictEqual(link.attrs.href, HOSTILE_URL, 'the hostile match URL was mangled');
  assert.strictEqual(link.textContent, HOSTILE_TITLE, 'the hostile match title was not literal text');
  assert.ok(good.panel.textContent.indexOf('87%') !== -1, 'the match score disappeared');
  assert.ok(good.panel.textContent.indexOf('Only 25% of complaints are indexed') !== -1,
    'the coverage caveat disappeared');
  noSinks(good);

  const noMatches = await boot({ answer: { available: true, similar: [], coverage: { coverage: 0.5 } } });
  assert.ok(noMatches.panel.textContent.indexOf('No similar complaint found.') !== -1,
    'the honest empty answer disappeared');
  assert.ok(noMatches.panel.textContent.indexOf('Only 50%') !== -1, 'the coverage caveat disappeared');
  noSinks(noMatches);

  const unavailable = await boot({ answer: { available: false, note: HOSTILE_NOTE } });
  assert.strictEqual(find(unavailable.panel, 'p')[0].textContent, HOSTILE_NOTE,
    'the hostile note was not literal text');
  assert.strictEqual(find(unavailable.panel, 'script').length, 0,
    'the hostile note was parsed into an element');
  noSinks(unavailable);

  const failed = await boot({ reject: true });
  assert.ok(failed.panel.textContent.indexOf('could not run') !== -1, 'the failure answer disappeared');
  assert.ok(failed.panel.textContent.indexOf('Filing still works') !== -1,
    'the failure answer did not say filing still works');
  noSinks(failed);

  const short = await boot({ query: 'short' });
  assert.strictEqual(short.panel.textContent, '', 'a short title should clear the panel');
  noSinks(short);

  // The status and card-refusal helpers keep their plain-text contract.
  const W = good.sandbox.ConcordWrites;
  W.setStatus(good.form, HOSTILE_NOTE, true);
  assert.strictEqual(good.status.textContent, HOSTILE_NOTE, 'setStatus lost its text');
  assert.ok(good.status.attrs.class.indexOf('form-status-error') !== -1, 'setStatus lost its error class');
  W.showCardRefusal(good.btn, HOSTILE_NOTE);
  assert.strictEqual(find(good.card, '[data-card-status]')[0].textContent, HOSTILE_NOTE,
    'the card refusal lost its text');
  assert.strictEqual(find(good.card, 'script').length, 0, 'the card refusal was parsed into an element');
  noSinks(good);

  // Field reports: migrated-away without a successor is refused before the POST.
  const field = await boot({});
  field.fields['fr-outcome'].value = 'migrated-away';
  field.fileForm.listeners.submit({ preventDefault() {} });
  assert.ok(field.err.textContent.indexOf('has to name what you moved to') !== -1,
    'the migrated-away refusal disappeared');
  assert.ok(!field.calls.some(function (c) { return /field-reports /.test(c); }),
    'an impossible report should not be posted');
  field.fields['fr-outcome'].value = 'worked';
  field.fileForm.listeners.submit({ preventDefault() {} });
  for (let i = 0; i < 6; i++) await new Promise(function (r) { setImmediate(r); });
  assert.ok(field.calls.some(function (c) { return /POST .*\/field-reports /.test(c); }),
    'the field report was not posted');
  noSinks(field);

  const reply = await boot({});
  reply.replyField.value = '';
  reply.replyForm.listeners.submit({ preventDefault() {} });
  assert.ok(reply.replyErr.textContent.indexOf('needs something in it') !== -1,
    'the empty-reply refusal disappeared');
  reply.replyField.value = 'saw it';
  reply.replyForm.listeners.submit({ preventDefault() {} });
  for (let i = 0; i < 6; i++) await new Promise(function (r) { setImmediate(r); });
  assert.ok(reply.calls.some(function (c) { return /\/field-reports\/55\/responses /.test(c); }),
    'the reply was not posted');
  noSinks(reply);

  assert.ok(!/(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/.test(pageSource),
    'project-writes.js regained an HTML string sink');
  console.log('dom project-writes: PASS — hostile duplicates are text, hrefs verbatim, all refusals kept, no string sink');
})().catch(function (e) {
  console.error('dom project-writes: FAIL — ' + e.message);
  process.exitCode = 1;
});
