#!/usr/bin/env node
// Step 2b: executes the shipped arena panel with hostile pair/log/verdict fields and hostile refusals.
// arenaControls() stays a string because project.js concatenates it; this checks the eight former sink sites.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/project-arena.js';
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
function matches(n, sel) {
  const eq = /^\[([^=\]]+)="([^"]+)"\]$/.exec(sel);
  const attr = /^\[(.+)\]$/.exec(sel);
  const cls = /^\.(.+)$/.exec(sel);
  if (eq) return n.attrs[eq[1]] === eq[2];
  if (attr) return Object.prototype.hasOwnProperty.call(n.attrs, attr[1]);
  if (cls) return (n.attrs.class || '').split(' ').indexOf(cls[1]) !== -1;
  return n.tagName === sel;
}
function find(root, sel) { return walk(root).filter(function (n) { return n.nodeType === 1 && matches(n, sel); }); }
function element(tag) {
  const n = {
    tagName: tag, nodeType: 1, attrs: Object.create(null), children: [], listeners: {},
    disabled: false, value: '',
    setAttribute(k, v) { this.attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    removeAttribute(k) { delete this.attrs[k]; },
    appendChild(c) { this.children.push(c); return c; },
    replaceChildren() {
      this.children = Array.prototype.slice.call(arguments).map(function (c) {
        return (c == null || typeof c === 'object') ? c : textNode(c);
      });
    },
    addEventListener(ev, fn) { this.listeners[ev] = fn; },
    closest(sel) {
      let cur = this;
      while (cur) {
        if (cur.nodeType === 1 && matches(cur, sel)) return cur;
        cur = cur.parent;
      }
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
  Object.defineProperty(n, 'textContent', {
    get() { return n.text; },
    set(v) { n.children = [textNode(v)]; },
  });
  return n;
}

const HOSTILE_LABEL = '<img src=x onerror=alert(1)>';
const HOSTILE_QUESTION = '<b>Which wins?</b>';
const HOSTILE_REASON = '<script>alert(2)</script>';
const HOSTILE_ENTITY = 'x" onmouseover="alert(3)';
const HOSTILE_ERROR = '<script>alert(4)</script>';

const PAIR = {
  question: HOSTILE_QUESTION,
  a: { entity_id: '11', label: HOSTILE_LABEL, r: 1500.4, rd: 350.2, win_probability: 0.62, is_baseline: false },
  b: { entity_id: '22', label: 'Ordinary', r: 1400, rd: 200, win_probability: 0.38, is_baseline: true },
};
const LOG = { votes: [{
  outcome: 'a', a: { label: HOSTILE_LABEL }, b: { label: 'Ordinary' },
  weight: 0.5, reason: HOSTILE_REASON,
}] };
const VERDICT = {
  arena_id: '7', agrees: false, divergent: 1,
  entries: [{ entity_id: HOSTILE_ENTITY, agrees: false, stored_r: 1500.4, replayed_r: 1400.6 }],
};

function makeBox() {
  const box = element('div');
  [['pair', 'data-arena-pair'], ['log', 'data-arena-log'], ['verify', 'data-arena-verify']].forEach(function (x) {
    const b = element('button');
    b.setAttribute(x[1], '7');
    box.appendChild(b);
  });
  [['pair-out', 'data-arena-pair-out'], ['log-out', 'data-arena-log-out'], ['verify-out', 'data-arena-verify-out']]
    .forEach(function (x) {
      const d = element('div');
      d.setAttribute(x[1], '7');
      box.appendChild(d);
    });
  return box;
}

function makeSandbox(opts) {
  const sandbox = {
    console, Date, Math, Promise, Object, Array, String, Number, isFinite, setTimeout,
    encodeURIComponent, decodeURIComponent, FormData: function () {},
  };
  sandbox.document = { createElement: element, createTextNode: textNode, addEventListener() {} };
  sandbox.window = sandbox;
  sandbox.ConcordEscape = { esc(s) { return String(s == null ? '' : s); } };
  sandbox.ConcordApi = {
    get(path) {
      if (opts.failGet) { const e = new Error(HOSTILE_ERROR); e.status = 500; return Promise.reject(e); }
      if (/next-pair/.test(path)) return Promise.resolve(PAIR);
      if (/votes/.test(path)) return Promise.resolve(LOG);
      return Promise.resolve(VERDICT);
    },
  };
  vm.createContext(sandbox);
  return sandbox;
}

async function click(box, sel) {
  const b = find(box, sel)[0];
  assert.ok(b, 'the control ' + sel + ' disappeared');
  b.listeners.click();
  for (let i = 0; i < 6; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  return find(box, sel.replace(/\[data-arena-(\w+)\]/, '[data-arena-$1-out]'))[0];
}

function noSinks(box) {
  const all = [box].concat(walk(box));
  const writes = all.filter(function (n) { return n._htmlWrites && n._htmlWrites.length; });
  assert.ok(!writes.length, 'project-arena.js wrote to an HTML string sink: ' +
    JSON.stringify(writes.map(function (n) { return n._htmlWrites; })));
  assert.ok(!all.some(function (n) {
    return n.nodeType === 1 && ['img', 'script', 'b'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
}

(async function () {
  const sandbox = makeSandbox({});
  vm.runInContext(domSource, sandbox, { filename: domPath });
  vm.runInContext(pageSource, sandbox, { filename: pagePath });
  const box = makeBox();
  const posted = [];
  sandbox.ConcordArena.wire(box, 'lorebook', {
    setStatus() {}, postJSON(url, body) { posted.push([url, body]); return Promise.resolve({ changed: 2 }); },
  });

  const pairOut = await click(box, '[data-arena-pair]');
  const form = find(pairOut, '[data-vote-pair]')[0];
  assert.ok(form, 'the vote form disappeared');
  assert.strictEqual(form.attrs['data-vote-pair'], '7', 'the arena id was mangled');
  assert.strictEqual(form.attrs['data-side-a'], '11', 'side A id was mangled');
  assert.strictEqual(find(form, '[type="radio"]').length, 5, 'the five outcomes disappeared');
  assert.ok(pairOut.textContent.indexOf(HOSTILE_QUESTION.replace(/<[^>]+>/g, '')) !== -1,
    'the hostile question was not literal text');
  assert.strictEqual(find(pairOut, '[data-pair-side="11"]').length, 1, 'side A card disappeared');
  assert.strictEqual(find(pairOut, '.pair-baseline').length, 1, 'the baseline marker disappeared');
  noSinks(box);

  const logOut = await click(box, '[data-arena-log]');
  assert.strictEqual(find(logOut, '.arena-log-row').length, 1, 'the vote log row disappeared');
  assert.ok(logOut.textContent.indexOf('weight 0.5') !== -1, 'the vote weight disappeared');
  assert.ok(logOut.textContent.indexOf(HOSTILE_LABEL) !== -1, 'the hostile label was not literal text');
  assert.ok(logOut.textContent.indexOf(HOSTILE_REASON) !== -1, 'the hostile reason was not literal text');
  noSinks(box);

  const verifyOut = await click(box, '[data-arena-verify]');
  assert.ok(verifyOut.textContent.indexOf('not what the vote log produces') !== -1,
    'the disagreement verdict disappeared');
  assert.strictEqual(find(verifyOut, '[data-arena-recompute]')[0].attrs['data-arena-recompute'], '7',
    'the recompute control lost its arena id');
  assert.ok(verifyOut.textContent.indexOf(HOSTILE_ENTITY.replace(/<[^>]+>/g, '')) !== -1,
    'the hostile entity id was not literal text');
  noSinks(box);

  // Delegated recompute: the button arrives after wiring, so the listener is on the box.
  const recompute = find(verifyOut, '[data-arena-recompute]')[0];
  recompute.parent = verifyOut;
  box.listeners.click({ target: recompute });
  for (let i = 0; i < 6; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  assert.strictEqual(posted.length, 1, 'the recompute did not post');
  assert.ok(/2 entries were/.test(verifyOut.textContent), 'the repair count disappeared');
  noSinks(box);

  // Hostile failure messages from all three loaders.
  const bad = makeSandbox({ failGet: true });
  vm.runInContext(domSource, bad, { filename: domPath });
  vm.runInContext(pageSource, bad, { filename: pagePath });
  const badBox = makeBox();
  bad.ConcordArena.wire(badBox, 'lorebook', { setStatus() {}, postJSON() { return Promise.resolve({}); } });
  const badOut = await click(badBox, '[data-arena-pair]');
  assert.ok(badOut.textContent.indexOf(HOSTILE_ERROR) !== -1, 'the hostile refusal was not literal text');
  assert.strictEqual(find(badOut, 'script').length, 0, 'the hostile refusal was parsed into an element');
  noSinks(badBox);

  const sink = /(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/;
  assert.ok(!sink.test(pageSource), 'project-arena.js regained an HTML string sink');
  console.log('dom arena page: PASS — hostile pair/log/verdict and refusal are text, attrs verbatim, no string sink');
})().catch(function (e) {
  console.error('dom arena page: FAIL — ' + e.message);
  process.exitCode = 1;
});
