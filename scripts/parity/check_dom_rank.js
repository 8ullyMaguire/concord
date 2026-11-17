#!/usr/bin/env node
// Step 2b: executes the shipped rank page with a hostile pair and every refusal branch.
// Every field must be a text node or a setAttribute value; no HTML string sink may be used.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const domPath = process.argv[2] || 'crates/web/assets/js/dom.js';
const pagePath = process.argv[3] || 'crates/web/assets/js/rank.js';
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
    hidden: false, style: {}, value: '',
    setAttribute(k, v) { this.attrs[k] = String(v); if (k === 'id') this.id = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null; },
    removeAttribute(k) { delete this.attrs[k]; },
    appendChild(c) { this.children.push(c); return c; },
    replaceChildren() {
      this.children = Array.prototype.slice.call(arguments).map(function (c) {
        return (c == null || typeof c === 'object') ? c : textNode(c);
      });
    },
    addEventListener(ev, fn) { this.listeners[ev] = fn; },
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
  };
  Object.defineProperty(n, 'textContent', {
    get() { return n.text; },
    set(v) { n.children = [textNode(v)]; },
  });
  return n;
}

const HOSTILE_TITLE = '<img src=x onerror=alert(1)>';
const HOSTILE_BODY = '<script>alert(2)</script>';
const HOSTILE_ERROR = '<b>refused</b>';
const PAIR = {
  feature_a: { id: 11, title: HOSTILE_TITLE, body: HOSTILE_BODY, elo_r: 1500.4, elo_rd: 350.2 },
  feature_b: { id: 22, title: 'Ordinary feature', body: 'plain', elo_r: 1400, elo_rd: 200 },
};

function err(status, message) { const e = new Error(message); e.status = status; return e; }

function makeSandbox(opts) {
  const root = element('div');
  root.id = 'rank-root';
  root.setAttribute('id', 'rank-root');
  const progress = element('div');
  progress.id = 'rank-progress';
  progress.setAttribute('id', 'rank-progress');
  const cast = element('span');
  cast.setAttribute('data-cast', '');
  const docListeners = {};
  const sandbox = { console, Date, Math, Promise, Object, Array, String, Number, encodeURIComponent, decodeURIComponent };
  sandbox.document = {
    createElement: element,
    createTextNode: textNode,
    getElementById(id) {
      if (id === 'rank-root') return root;
      if (id === 'rank-progress') return progress;
      return find(root, '#' + id)[0] || null;
    },
    querySelector(sel) { return sel === '[data-cast]' ? cast : find(root, sel)[0] || null; },
    addEventListener(ev, fn) { docListeners[ev] = fn; },
  };
  sandbox.window = sandbox;
  sandbox.location = { pathname: '/projects/lorebook/rank' };
  let posts = 0;
  sandbox.ConcordApi = {
    get() {
      if (opts.getError) return Promise.reject(opts.getError);
      if (opts.secondGetNull && posts) return Promise.reject(err(409, JSON.stringify({ error: 'every pair voted' })));
      return Promise.resolve(PAIR);
    },
    post() {
      posts += 1;
      return opts.postError ? Promise.reject(opts.postError) : Promise.resolve({});
    },
  };
  vm.createContext(sandbox);
  return { sandbox: sandbox, root: root, docListeners: docListeners, cast: cast };
}

async function run(opts) {
  const s = makeSandbox(opts);
  vm.runInContext(domSource, s.sandbox, { filename: domPath });
  vm.runInContext(pageSource, s.sandbox, { filename: pagePath });
  for (let i = 0; i < 8; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  return s;
}
function noSinks(s) {
  const all = [s.root].concat(walk(s.root));
  const writes = all.filter(function (n) { return n._htmlWrites && n._htmlWrites.length; });
  assert.ok(!writes.length, 'rank.js wrote to an HTML string sink: ' +
    JSON.stringify(writes.map(function (n) { return n._htmlWrites; })));
  assert.ok(!all.some(function (n) {
    return n.nodeType === 1 && ['img', 'script', 'b'].indexOf(n.tagName) !== -1;
  }), 'a hostile field was parsed into an element');
  return all;
}

(async function () {
  // Success path: two cards, five outcomes, own hrefs, hostile fields as text.
  const good = await run({});
  const cards = find(good.root, '.rank-card');
  assert.strictEqual(cards.length, 2, 'the two feature cards disappeared');
  assert.strictEqual(cards[0].attrs['data-feature'], '11', 'card A carries the wrong feature id');
  assert.strictEqual(cards[1].attrs['data-feature'], '22', 'card B carries the wrong feature id');
  const links = find(good.root, '.rank-open');
  assert.deepStrictEqual(links.map(function (a) { return a.attrs.href; }),
    ['/projects/lorebook/features/11', '/projects/lorebook/features/22'], 'each card must link to itself');
  const actionBox = find(good.root, '#rank-actions')[0];
  assert.ok(actionBox, 'the rank actions disappeared');
  const buttons = find(actionBox, 'button');
  assert.deepStrictEqual(buttons.map(function (b) { return b.attrs['data-outcome']; }),
    ['a', 'b', 'both', 'neither', 'skip'], 'the five outcomes disappeared');
  assert.strictEqual(find(find(good.root, '.rank-keys')[0], 'kbd').length, 5, 'the keyboard hint disappeared');
  assert.ok(good.root.textContent.indexOf(HOSTILE_TITLE) !== -1, 'the hostile title was not literal text');
  assert.ok(good.root.textContent.indexOf(HOSTILE_BODY) !== -1, 'the hostile body was not literal text');
  noSinks(good);

  // Keyboard and click both go through submit(); after a success the next pair is loaded.
  const voted = await run({ secondGetNull: true });
  const btnA = find(find(voted.root, '#rank-actions')[0], 'button')[0];
  btnA.listeners.click();
  for (let i = 0; i < 8; i++) await new Promise(function (resolve) { setImmediate(resolve); });
  assert.strictEqual(voted.cast.textContent, '1', 'the cast counter did not advance');
  assert.ok(voted.root.textContent.indexOf('voted on every pair') !== -1,
    'the exhausted-pairs state disappeared');
  noSinks(voted);

  // Every refusal branch.
  const cases = [
    [{ getError: err(401, 'sign in') }, 'Sign in to rank'],
    [{ getError: err(403, HOSTILE_ERROR) }, 'Sign in to rank'],
    [{ getError: err(404, 'missing') }, 'Could not load a comparison'],
    [{ getError: err(409, JSON.stringify({ error: 'at least 2 features required' })) }, 'Nothing to compare yet'],
    [{ getError: err(422, JSON.stringify({ detail: 'every pair voted' })) }, 'voted on every pair'],
    [{ getError: err(500, HOSTILE_ERROR) }, 'Could not load a comparison'],
    [{ postError: err(403, HOSTILE_ERROR) }, HOSTILE_ERROR],
    [{ postError: err(500, HOSTILE_ERROR) }, 'The vote was not recorded (HTTP 500)'],
  ];
  for (const [opts, needle] of cases) {
    const s = await run(opts);
    if (opts.postError) {
      find(find(s.root, '#rank-actions')[0], 'button')[0].listeners.click();
      for (let i = 0; i < 8; i++) await new Promise(function (resolve) { setImmediate(resolve); });
    }
    assert.ok(s.root.textContent.indexOf(needle) !== -1,
      'expected refusal disappeared for ' + JSON.stringify(opts) + ': ' + s.root.textContent.slice(0, 90));
    noSinks(s);
  }

  const sink = /(\.innerHTML\s*=|\.insertAdjacentHTML\s*\()/;
  assert.ok(!sink.test(pageSource), 'rank.js regained an HTML string sink');
  console.log('dom rank page: PASS — hostile cards and all refusal states are text, links verbatim, no string sink');
})().catch(function (e) {
  console.error('dom rank page: FAIL — ' + e.message);
  process.exitCode = 1;
});
