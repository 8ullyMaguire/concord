// Does the markdown subset actually cover the corpus?
//
// The frontend spec claims the subset is chosen by reading the spec and this
// repo's docs. That is a guess. This measures it: render every document body in
// the live database and report what the subset had to give up.
//
// A construct is "unhandled" when it is present in the source and absent from
// the output. Each check below is deliberately cheap and specific.

const fs = require('fs');
const path = require('path');
const vm = require('vm');

const dbPath = process.env.CONCORD_DB ||
  (process.env.HOME + '/.local/share/concord/concord.db');
const src = fs.readFileSync(path.join(__dirname, 'markdown.js'), 'utf8');
const sandbox = { window: {} };
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
const md = sandbox.window.ConcordMarkdown;

// Pull bodies out with sqlite3 so this script needs no driver.
const { execFileSync } = require('child_process');
const rows = JSON.parse(execFileSync('sqlite3', [
  '-json', dbPath,
  "select kind, title, coalesce(body,'') from project_documents;"
], { maxBuffer: 1024 * 1024 * 256 }).toString() || '[]');

if (!rows.length) {
  console.log('no documents in ' + dbPath);
  process.exit(0);
}

// Each probe: [name, does the source contain it?, does the output show it was handled?]
const PROBES = [
  ['setext heading (text underlined with = or -)',
   /^(?!\s*[-*+\d])[^\n]+\n(=+|-+)\s*$/m, m => /<h[1-6][ >]/.test(m)],
  ['nested list (2+ space indent under an item)',
   /^\s*[-*+]\s+.*\n\s{2,}[-*+]\s+/m, null],
  ['reference link [a][b]',
   /\[[^\]]+\]\[[^\]]*\]/, m => /<a href/.test(m)],
  ['task list item (- [ ] or - [x])',
   /^\s*[-*+]\s+\[[ xX]\]\s/m, m => /<li>/.test(m)],
  ['footnote reference [^1]',
   /\[\^[^\]]+\]/, m => /<sup/.test(m) || /<a href="#fn/.test(m)],
  ['inline HTML tag',
   /<[a-zA-Z\/][^>]*>/, null],
  ['HTML entity in source (&amp; &#65;)',
   /&(?:amp|lt|gt|quot|#\d+);/, null],
  ['image', /!\[[^\]]*\]\([^)]+\)/, m => /<img/.test(m)],
  ['definition-style block (::: or ---frontmatter)',
   /^(:::|---)\s*$/m, null],
  ['tab-indented code block',
   /^\t/m, m => /md-code/.test(m)],
];

const counts = {};
let totalBytes = 0, slowest = 0, slowestTitle = '';
const failures = [];

rows.forEach(function (r) {
  const body = r.body || '';
  totalBytes += body.length;
  const t0 = Date.now();
  let out;
  try {
    out = md.render(body);
  } catch (e) {
    failures.push({ title: r.title, kind: r.kind, why: 'threw: ' + e.message });
    return;
  }
  const ms = Date.now() - t0;
  if (ms > slowest) { slowest = ms; slowestTitle = r.title; }

  PROBES.forEach(function (p) {
    const name = p[0], re = p[1], handled = p[2];
    if (!re.test(body)) return;
    counts[name] = counts[name] || { hit: 0, handled: 0 };
    counts[name].hit++;
    if (handled && handled(out)) counts[name].handled++;
    else failures.push({ title: r.title, kind: r.kind, why: name,
                         sample: (body.match(re) || [''])[0].slice(0, 60) });
  });
});

console.log('Corpus: ' + rows.length + ' documents, ' +
            (totalBytes / 1024 / 1024).toFixed(1) + ' MB, markdown.js ' +
            (src.length / 1024).toFixed(1) + ' KB');
console.log('Slowest render: ' + slowest + 'ms (' + slowestTitle + ')');
console.log('');

console.log('Construct                        docs   handled');
Object.keys(counts).forEach(function (k) {
  const c = counts[k];
  console.log('  ' + k.padEnd(33) + String(c.hit).padStart(4) +
              String(c.handled).padStart(9) +
              (c.handled < c.hit ? '   <-- ' + (c.hit - c.handled) + ' dropped' : ''));
});

console.log('');
console.log('Dropped instances: ' + failures.length);
const byWhy = {};
failures.forEach(function (f) {
  byWhy[f.why] = byWhy[f.why] || [];
  byWhy[f.why].push(f);
});
Object.keys(byWhy).sort(function (a, b) {
  return byWhy[b].length - byWhy[a].length;
}).forEach(function (why) {
  console.log('  ' + why + ': ' + byWhy[why].length);
  byWhy[why].slice(0, 3).forEach(function (f) {
    console.log('      [' + f.kind + '] ' + (f.title || '').slice(0, 50) +
                (f.sample ? '   e.g. ' + JSON.stringify(f.sample) : ''));
  });
});