# Concord visual directions (2026-11-18)

Prepared for the owner after the whole-site visual audit. Four coherent directions, each implementable without
changing the product's behaviour. The audit's measured defects are listed at the end so each direction can be judged
against the same evidence rather than against adjectives.

All four preserve the current route surface, the server-rendered HTML, the node-only renderer contract (D-11/D-13),
and the accessibility floor already enforced by `probe_security_headers.py`. None introduces a client-side framework.

---

## A. Refined indigo/slate — the current identity, made deliberate

**Thesis.** Keep the palette the site already uses, but stop treating it as a bag of colours: promote every recurring
decision to a token, give each page a clear hierarchy, and make one component vocabulary serve cards, badges, empty
states and forms. This is the lowest-risk direction and the one that preserves the Go site's family resemblance.

**Palette.** `--indigo #5f62ea` stays the action colour (4.75:1 against white); `--indigo-text #4f46e5` stays the
text colour on slate surfaces (5.74:1). Surfaces become a three-step stack: page `#f8fafc`, card `#ffffff`, inset
`#f1f5f9`. Borders `#e2e8f0`; primary ink `#0f172a`; secondary `#475569`; accent wash `#eef2ff`.

**Type.** One scale, defined as tokens: 13/1.4 meta, 15/1.6 body, 20/1.3 section, 32/1.15 page title, 44/1.05
display. `font-weight: 650` for headings; body copy never justified; `text-wrap: balance` on headings.

**Layout.** A 1200px max-width shell with a 24px gutter (16px at phone). Every page gets a page header with eyebrow,
title, one-sentence lede and one primary action. Cards use a 12px radius, 1px border, 16px padding, and a consistent
`meta / title / body / footer` skeleton. Grid gaps are 16/24/32 tokens, never ad-hoc.

**Components.** `.page-head`, `.surface`, `.stat-tile`, `.chip`, `.segmented`, `.empty-state`, `.form-card`,
`.breadcrumb`, `.nav-link[aria-current]`. One focus ring: `2px solid var(--indigo-text)` with 2px offset.

**Fixes it addresses.** All of them: weak hierarchy, default links, inconsistent card schemas, unstyled forms,
dead-end empty states, missing active nav state, ragged grids.

**Cost / risk.** Lowest. Existing contrast measurements stay valid if the named tokens are kept. It may read as
"more of the same" to someone asking for a dramatic change.

**Where it fails.** If the owner wants the site to look unmistakably different from the current build, this is a polish,
not a redesign.

---

## B. Dark-first editorial — a new identity

**Thesis.** Treat Concord as a publication of decisions: near-black canvas, warm paper text, a single saturated accent,
large serif display type, and generous editorial whitespace. Lists become reading surfaces; detail pages become
articles with a sidebar of facts.

**Palette.** Canvas `#0b1020`, raised surface `#141b2e`, paper text `#e8ecf5`, muted `#9aa7bd`, accent
`#a5b4fc` for dark-canvas links and `#7c8cff` for buttons. One warm `#f5c86b` for status/warning. All text must be
re-measured: the current `--indigo-on-dark #a5b4fc` was chosen for the hero only and is not a whole-site token yet.

**Type.** Serif display (system `Georgia`/`Iowan Old Style` stack, no new webfont) at 40–56px; sans body at 16/1.65;
monospace only for IDs and code. Dramatic initial letter or rule under the page eyebrow.

**Layout.** Two-column editorial grid on detail pages: main argument at 2fr, facts/actions sidebar at 1fr. Browse
lists use wide rows with a title-led left column and metadata right, not a card wall.

**Fixes it addresses.** Barebones feeling most directly: the visual change is unmistakable. Also fixes hierarchy and
card consistency by replacing cards with editorial rows.

**Cost / risk.** Highest: every existing contrast measurement is invalidated, both themes need a fresh sweep at 1280
and 390, and the landing page already polished in indigo/slate would need a matching rewrite or would clash.

**Where it fails.** A dark-first reading surface can hurt dense tables, long tag lists and forms — the exact utility
surfaces the owner asked to skip, but which still share the shell.

---

## C. Dense information console — an operator's site

**Thesis.** Optimise for scanning and comparison over warmth. Tight rows, small type, strong tabular alignment, left
rail navigation, and a persistent context bar. Think issue tracker / observability console rather than catalogue.

**Palette.** Near-white `#ffffff` canvas, `#f8fafc` striped rows, `#0f172a` ink, `#334155` secondary, `#2563eb`
links, and semantic status colours green/amber/red with text labels (never colour alone).

**Type.** 13px UI, 12px meta, tabular numerals via `font-variant-numeric: tabular-nums`, uppercase 11px eyebrow with
0.08em tracking. Headings stay small; density carries the hierarchy.

**Layout.** Full-width rows with aligned columns: name 3fr, status 1fr, health 1fr, tags 2fr, updated 1fr. Sticky
table header. Detail pages get a left rail of section links and a right facts panel.

**Fixes it addresses.** Search and project lists become genuinely scannable; health values become bars and badges;
tag lists become a filterable chip matrix. Empty states become compact notices, not page-sized voids.

**Cost / risk.** Medium. It fights the existing landing page's generous, cinematic composition. Tap targets and
contrast need careful work so density does not become inaccessibility.

**Where it fails.** If Concord is meant to feel inviting to contributors rather than operators, this reads cold.

---

## D. Go-site fidelity — recreate the predecessor

**Thesis.** Use the old Go implementation as the visual source of truth: same spacing, same component shapes, same
copy. The earlier frontend pass already restored the landing composition from Go; extend that to every browsable page.

**Palette / type / layout.** Whatever the vendored Go templates and stylesheets specify. This direction is defined by
fidelity, not by a new taste: no token inventions beyond what the Go CSS already contains.

**Fixes it addresses.** Whatever the Go site did well, which is the reason this direction exists. It also removes the
inconsistency between the new landing page and the older pages if the landing page was itself ported from Go.

**Cost / risk.** Medium to high depending on how complete the vendored source is. It preserves old defects along with
old strengths, and the repo's current ADR 0018/D-75 record that the landing composition was ported, not the whole site.

**Where it fails.** If the Go site's own pages were among the "barebones" surfaces, fidelity preserves the complaint.

---

## Evidence the direction must answer

Measured in a real browser at 1280×900 against live `d444f48`:

- `/projects` — 3-column card grid, dense paragraph per card, tiny chip, no consistent card metadata; primary CTA is
  an inline link.
- `/projects/suki` — title and 13-tab strip, then a mostly empty overview; mismatched button affordances.
- `/search` — native `<input>` and default `<button>`, one-line result cards, raw decimals labelled `health 0.26`.
- `/taxonomy` — bare `<ul>` links in a single narrow column; link text leaks internal IDs such as `performance#163685`;
  visible duplicate tags.
- `/stats`, `/curation`, `/notifications`, `/login` — utility pages deliberately out of scope for this pass; they are
  listed only so the chosen shell does not make them worse.
- `/lists` — linked from the home Explore grid and project nav but answers a raw JSON 404. Out of scope by the owner's
  choice; it is recorded here as a known broken destination, not silently ignored.

Cross-cutting: no `aria-current` on the nav, default blue links, page titles barely larger than body, no consistent
page header or footer rhythm at the top of the viewport.

## Decision

The owner scoped this pass to the main browse/detail pages only and asked for the directions to be written down before
choosing one. **Direction A is the working choice** — it keeps the measured contrast passes valid and makes the
landing page and the rest of the site one system. Directions B/C/D remain available as explicit alternatives; choosing
one later invalidates the token-level evidence and requires a fresh whole-site sweep.
