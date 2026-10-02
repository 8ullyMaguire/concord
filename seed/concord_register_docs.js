// Register this repository's documents on the Concord project.
//
// Idempotent: PUT /documents replaces the body at (project, kind, slug) and
// increments revision, so re-running is safe and reports what changed.
//
// Documents are written with an API token from the session scratch directory,
// never one typed here.

const fs = require('fs');
const path = require('path');

const API = process.env.CONCORD_API || 'http://127.0.0.1:8006/api/v1';
const SLUG = process.env.CONCORD_PROJECT || 'concord';
const REPO = process.env.CONCORD_REPO || '/home/alvaro/code/projects/concord';
const TOKEN_FILE = path.join(
  process.env.HOME,
  '.hermes/profiles/sysadmin/cache/scratch/import.token');

// kind is chosen from the document's role, matching DocumentKinds in
// internal/store/documents.go. A file that maps to none of them is not filed
// under a wrong kind to make the count work.
const PLAN = [
  { file: 'README.md',                    kind: 'readme',    slug: 'readme',
    title: 'Concord — README' },
  { file: 'docs/concord-spec-r4.md',      kind: 'spec',      slug: 'spec-r4',
    title: 'Concord specification, revision 4' },
  { file: 'docs/PLAN-r4.md',              kind: 'plan',      slug: 'plan-r4',
    title: 'r4 implementation plan' },
  { file: 'docs/PLAN.md',                 kind: 'plan',      slug: 'plan',
    title: 'Implementation plan (pre-r4 milestones)' },
  { file: 'docs/HANDOFF.md',              kind: 'readme',    slug: 'handoff',
    title: 'Handoff for the implementing agent' },
  { file: 'docs/ARCHITECTURE.md',         kind: 'spec',      slug: 'architecture',
    title: 'Architecture' },
  { file: 'docs/PREMISE.md',              kind: 'readme',    slug: 'premise',
    title: 'Premise' },
  { file: 'docs/KNOWN-ISSUES.md',         kind: 'adr',       slug: 'known-issues',
    title: 'Known issues' },
  { file: 'docs/specs/frontend-spec.md',  kind: 'spec',      slug: 'frontend',
    title: 'Frontend specification' },
  { file: 'docs/specs/membership-and-voting.md',
    kind: 'spec', slug: 'membership-and-voting',
    title: 'Membership and voting specification' },
  { file: 'docs/specs/auth-and-portfolio-import.md',
    kind: 'spec', slug: 'auth-and-portfolio-import',
    title: 'Authentication and portfolio import specification' },
  { file: 'docs/plans/membership-and-voting.md',
    kind: 'plan', slug: 'membership-and-voting-plan',
    title: 'Membership and voting — plan' },
];

const token = fs.readFileSync(TOKEN_FILE, 'utf8').trim();

async function main() {
  let ok = 0, skipped = 0, failed = 0;
  for (const doc of PLAN) {
    const full = path.join(REPO, doc.file);
    if (!fs.existsSync(full)) {
      console.log('  SKIP  ' + doc.file + ' (not on disk)');
      skipped++;
      continue;
    }
    const body = fs.readFileSync(full, 'utf8');
    const res = await fetch(`${API}/projects/${SLUG}/documents`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token },
      body: JSON.stringify({ kind: doc.kind, slug: doc.slug, title: doc.title, body }),
    });
    const text = await res.text();
    if (!res.ok) {
      console.log('  FAIL  ' + doc.file + ' -> ' + res.status + ' ' + text.slice(0, 160));
      failed++;
      continue;
    }
    const parsed = JSON.parse(text);
    console.log('  rev ' + String(parsed.revision).padEnd(3) +
                (doc.kind + '/' + doc.slug).padEnd(34) +
                String(body.length).padStart(7) + ' B  ' + doc.file);
    ok++;
  }
  console.log('\n' + ok + ' registered, ' + skipped + ' skipped, ' + failed + ' failed');

  // Verify by reading back through the same route the viewer uses.
  const list = await (await fetch(`${API}/projects/${SLUG}/documents`)).json();
  const docs = Array.isArray(list) ? list : (list.documents || []);
  console.log('\nread back: ' + docs.length + ' documents on ' + SLUG);
  const byKind = {};
  docs.forEach(d => { byKind[d.kind] = (byKind[d.kind] || 0) + 1; });
  Object.keys(byKind).sort().forEach(k =>
    console.log('  ' + k.padEnd(10) + byKind[k]));
  const total = docs.reduce((n, d) => n + (d.body || '').length, 0);
  console.log('  total body bytes: ' + total);
  if (failed) process.exit(1);
}

main().catch(e => { console.error(e); process.exit(1); });