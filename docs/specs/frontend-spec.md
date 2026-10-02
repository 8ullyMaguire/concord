# Concord — Frontend Specification

**Status:** specification of record for the web client
**Date:** 2026-10-02, revised 2026-10-02 with the document viewer built
**Covers:** every page under `internal/httpapi/templates/` and every script under
`internal/httpapi/assets/`, and the pages that do not exist yet
**Companion:** `concord-spec.md` (the product spec). This document does not restate
it; where the two disagree about the frontend, this one is correct about what is
built and the other is aspirational.

---

## 0. What changed in this revision

The document viewer (§3.2) is **built**, not proposed. This section says what
was decided and why, so the rest of the document reads as a specification of
record rather than a wish list.

    /projects/{slug}/documents    the viewer: kind rail, list, reader
    assets/js/markdown.js        the renderer; escapes raw HTML, inerts images
    assets/js/documents.js       the page
    assets/js/corpus-coverage.js measures which constructs the subset drops
    seed/concord_register_docs.js registers this repo's docs as project documents

    80 assertions                test-markdown.js, run with node
    8 Go tests                   documents_page_test.go

Twelve of this repository's own documents are registered on the `concord`
project (README, the r4 spec, both plans, the handoff, architecture, premise,
known issues, three specs/ and one plans/ file — 168 KB across 4 kinds), so the
site is the place to read them. `seed/concord_register_docs.js` is idempotent and
re-running it increments each document's revision rather than duplicating.

Three decisions were taken:

**The renderer is hand-written, in ~400 lines, vendored.** A CommonMark parser
is 8,000+ lines. This app has no build step and no `node_modules`, so a library
arrives as an unauditable blob and would be the largest thing in the codebase by
a factor of twenty. The supported subset is named in the file and tested against
this repository's own documents. When a real document needs a construct the
subset lacks, widen it deliberately — that is the trade this choice makes
explicit. `TestMarkdownRendererIsSelfContainedAndOffline` fails if a CDN
reference appears, so the choice cannot erode silently.

**Raw HTML is escaped, images are inert.** This is the security boundary and it
is not negotiable: 1,087 documents arrived from 50 repositories and are writable
by any contributor. `<img>` is rendered as a labelled placeholder that makes no
network request, because a real one turns "who read the spec" into a question
the author can answer. `javascript:`, `data:` and `vbscript:` are refused by an
allowlist of schemes, not a blocklist — a blocklist is defeated by tab, newline,
entity and case tricks that the browser resolves before the URL is parsed.

**Documents over 200 KB render by section.** One `innerHTML` assignment for a
megabyte of markdown locks the tab. The body is split at level-2 headings, each
section reserves height proportional to its source length, and each is rendered
by an `IntersectionObserver` as it approaches the viewport, with a table of
contents.

That last path took three attempts and each failure was silent, which is worth
recording because all three looked like working software:

1. Sections were marked `hidden` until they intersected. A hidden element has
   zero height, so it never intersects, so it stays hidden. A 337 KB document
   rendered 5,028px tall — its first section — and there was no scroll left to
   trigger anything. **Reserve height, do not remove the element.**
2. `renderMarkdownNodes` selected every `[data-markdown]` node, so all 40
   sections rendered eagerly and the observer, the heights and the TOC were
   decorative. It looked correct because the content appeared, while paying the
   exact cost the split exists to avoid on every load.
3. Fixed, it renders 1 section at load and 27 after scrolling to 120 KB, with 13
   ahead still pending — which is the behaviour the split was for.

Measured in a browser against a 337 KB, 40-section document. The Go tests assert
the source-level invariants (the `closest('.doc-section.is-pending')` guard, the
`is-pending` class, the reserved `min-height`) because the behaviour needs a JS
engine and a scrolling viewport.

### 0.1 The corpus is 6 documents, not 1,087

This specification previously asserted that 1,087 documents were stored and
invisible. **They are not.** Measured:

    projects 70   features 772   complaints 117   documents 6   documents bytes 67KB

All six belong to `tessera` and were added on 2026-09-30, when the document API
landed — not by the portfolio import. Commit `da2bb09` is titled "portfolio
import: 50 projects, 1087 documents, 337 features", and the projects and features
counts are real; the document count is not. The importer reads a manifest from
`~/.hermes/profiles/sysadmin/cache/scratch/manifest.json`, and that scratch
directory prunes entries idle for 24 hours, so the manifest no longer exists and
the import cannot be re-run as it stands. The scripts are idempotent and would
work again given a manifest.

So the viewer was built against a real API and an almost-empty corpus. That does
not weaken the security work — the XSS boundary does not depend on how many
documents exist — but it does mean §0's subset was chosen from 6 documents and
from reading the spec, not from the 1,087 the design assumed. `corpus-coverage.js`
is the tool that settles it: point it at any database and it reports which
constructs the subset drops. Run against the current corpus it reports 0 dropped
instances, which is 6 documents' worth of evidence and no more.

Two consequences worth stating plainly:

- No stored document exceeds 200 KB, so the sectioned path is not exercised by
  production data. It was verified against a synthetic 337 KB, 40-section
  document with tables and code fences, then deleted; the bugs it found are
  listed in §0. The threshold itself is sized for what the portfolio held (a
  1.1 MB document, a 1.7 MB `CHANGELOG.md`, per the importer's comments), not
  for anything currently stored.
- The subset's coverage is unverified at scale. When the corpus is restored,
  re-run `corpus-coverage.js` before trusting §0's list.

## 0.2 A competing frontend spec was evaluated and rejected in full

A 15-section frontend specification was offered for adoption on 2026-10-02:
SvelteKit + GraphQL + Tailwind, 30 implementation phases, ~140 named
components, and a design system with density toggles and dark themes.

**It was rejected as a whole, and the reasons are recorded here because "we
looked at a big spec and said no" is not a decision anyone can audit later.**

It is a competent document. It is not a description of this system, and the
distances are not subtle:

| It assumes | This repository |
|---|---|
| `/:owner/:project` URLs, `owner { login, avatarUrl }` | `/projects/{slug}`. **There is no owner.** `projects` has `id, slug, name, description, governance_model, license, created_at, updated_at, visibility` and no `owner_id`, `tier` or `archived` column |
| `users.login`, `users.avatarUrl` | `users.username`, `display_name`. No avatar |
| GraphQL for every page | REST. `PLAN-r4.md` lists GraphQL as a **deliberate non-goal**: "the API surface is still moving; a generated schema now would be rewritten" |
| `solution_complaint_claims`, `capabilities`, `capability_confirmations`, `field_reports`, `scout_reports`, `decision_records`, `boards` | none of these tables exist. `boards` is `board_columns` + `board_cards` |
| Service worker, web push, notification table, saved searches | **no notification or subscription table exists at all.** No service worker |
| SvelteKit or Next.js | no `package.json`, no `node_modules`, no build step. 13 JS files, 2,136 lines, embedded in the Go binary |

Three divergences are the kind that matter, because re-adopting them is a
regression rather than an upgrade:

1. **The framework.** §1.2 chose option (b) deliberately, with the reasoning
   written down: no build step, no supply chain, every existing page keeps
   working, and the API is not finished enough for a component framework to be
   written against. That document opens by assuming the opposite.
2. **GraphQL.** Its §13 is six hand-written queries that do not compile against
   any schema we have or intend. Adopting it would mean inventing the schema it
   implies.
3. **Its admin model contradicts a governance decision.** §4.18 says "No content
   controls on admin pages. Content moderation happens through moderation
   juries and quorum." r4 §6.6 and §11 built the opposite on purpose: an
   **emergency hold** with mandatory written justification, automatic community
   confirmation, and expiry, plus a **public admin ledger**. Weakening that to
   match is a decision, not a UI change.

It also names tables we would have to build to render its own screens — R5's
`field_reports`, `capabilities` and R6's `scout_reports` are all absent, and its
Opportunity Radar has no data source in this system at all.

**Kept, because they are real gaps in *this* frontend and were verified as
absent rather than assumed:** §0.3.

## 0.3 What was taken from it

Four items, all small, all additive, none requiring the architecture it came
from. Verified absent in the tree before being written:

- **`:focus-visible` on every interactive element.** Measured: five `:focus`
  rules exist and all five are on inputs. Buttons, links, rail items and kanban
  cards have no focus indicator at all.
- **Skeleton loaders instead of a centred spinner.** Measured: `.loading-spinner`
  is used on every page and is a blocking centred spinner.
- **`prefers-reduced-motion`.** Measured: `@media` is used for layout only;
  there is no `prefers-reduced-motion` block, and `.loading-spinner` animates
  indefinitely regardless.
- **A single documented loading pattern**, so the next page does not invent one.

Deliberately **not** taken, having been considered: dark theme, density toggle,
command palette, notification panel, service worker/PWA, i18n catalogs, GraphQL
contracts. Each is either a large surface with no current demand (dark theme on a
site that is currently light-only), or depends on the architecture just rejected.

Two things are deferred rather than refused, and recorded so they are not lost:

- **Live-region announcements for vote/consensus results** (§5 of our spec, and
  this document agrees): needs the consensus page, which does not exist.
- **A "why this rank?" breakdown popover**: the data exists — `ranking` already
  exposes per-criterion scores — but it wants the arena page.

Two things are deliberately *not* built yet, and both are named in §9.

## 1. The state of the frontend, measured

Measured at commit `d5c9435`, before the document viewer existed. Lines 27-50
are therefore still accurate about what had **no page** then, and §3.2 is the one
row now marked done.

    9 page routes          /  /search  /projects  /projects/{slug}
                           /projects/{slug}/board  /projects/{slug}/rank
                           /projects/{slug}/ranking  /login  /register

    10 templates           313 lines total
    9 scripts              1,139 lines total
    1 stylesheet           922 lines, 148 utility/component classes
    0 build step           no bundler, no framework, no node_modules
    18 top-level /api/v1 mounts, 90 route registrations in total

**The API is roughly ten times larger than the frontend.** Fifteen resource
groups have no page at all:

| resource | endpoints | page |
|---|---|---|
| documents | list, put, kinds, search, get, delete | **built** (was none) |
| consensus | create, get, position, objection, close, resolve | **none** |
| merge requests | create, approve, execute, reject | **none** |
| lists | list, create, get, entries | **none** |
| requests | list, create, get, answers, answer, vote-answer | **none** |
| criteria | list, create, vote, set-active | **none** |
| criteria profiles | list, save | **none** |
| comments | list, create, delete, vote | **none** |
| invites | create, list, revoke | **none** |
| charter | get, update | **none** |
| members | list | **none** |
| tags / languages / metrics | put | **none** |
| visibility | put | **none** |
| priorities / tallies | get | **none** |
| composite rank | post | **none** |

The sharpest instance: **six documents are stored, with a full read/search API
and no viewer** — see §0.1 for why the figure was once thought to be 1,087. A
project's entire specification, plan and ADR history was reachable only through
`curl`.

### 1.1 The architecture that is actually in place

Not React, not SvelteKit. The real architecture is:

1. **A shell per page.** Templates are thin: `project.html` is four lines — a
   mount point, a spinner, and a `<script>` tag. `base.html` supplies chrome,
   nav, flash region and the asset-versioned stylesheet.
2. **The client fetches its own data.** Each script resolves the slug from
   `window.location`, calls `/api/v1/...`, and renders the entire view into the
   mount point with string concatenation.
3. **Auth is a Bearer token in `localStorage`**, sent by `session.js`. There is
   no session cookie anywhere in the codebase — verified by grepping for
   `SetCookie` across `auth*.go`.
4. **Mutations reload the page** rather than splicing in a row. `project.js`
   says why, and the reason is good:

   > A complaint that appeared without a validation round-trip would be shown as
   > filed even when the server rejected it, and the author would believe they
   > had reported something.

Keep that. It is the correct trade for a server whose responses are the only
evidence a write succeeded.

### 1.2 Where this contradicts the product spec

`concord-spec.md` §9.4 names "SvelteKit or React, PWA, SSR" as the stack and
§10 describes a Complaint Page, Feature Page and Voting UI. That is the intended
end state, not the current one. **§9.4 is now wrong about the present tense and
this spec supersedes it on that point.** Three options were available:

- **(a) Adopt a framework now.** Replaces 1,139 working lines, introduces a build
  step and `node_modules` into a Go binary that currently embeds its own assets,
  and risks the working pages while the replacement is written.
- **(b) Keep the current architecture and finish it.** No build step, no supply
  chain, 120 KB of assets, and every existing page keeps working.
- **(c) Both, sequenced.** Keep what works; introduce a framework only for the
  surfaces that genuinely need one, behind the same API.

**(b) is chosen for now, (c) is the destination.** The deciding factor is cost,
not taste: the API the frontend would consume is not finished, so a component
framework would be written against a moving target and rewritten. Revisit when
the API is stable and there is a page whose interactivity no amount of string
concatenation handles — realistically the consensus voting UI and the pairwise
vote queue, both of which need focus management and live state.

---

## 2. Design system

It exists and it is good. Extend it; do not replace it.

**Tokens** live in `:root` in `style.css`: indigo primary (`#6366f1`), a
ten-step slate ramp for text and surfaces, and green/amber/red for state. Use
the variables, never raw hex — a raw hex is how a second grey appears.

| need | use |
|---|---|
| primary action | `.btn.btn-primary` |
| secondary | `.btn.btn-outline`, `.btn.btn-secondary` |
| destructive | `.btn.btn-danger` |
| low-emphasis | `.btn.btn-quiet`, `.btn.btn-ghost` |
| surface | `.card` |
| state pill | `.badge` + `badge-green` / `badge-indigo` / `badge-amber` / `badge-red` / `badge-slate` |
| section header | `.section-head` > `.section-title` + `.section-sub` |
| empty and error | `.empty-state`, `.error-state` |
| spacing | `.mt-*`, `.mb-1` … `.mb-8` — no ad-hoc margins |
| health | `.health-bar` + `.health-fill-good` / `-mid` / `-poor` |

**Tokens this system does not yet have, and needs:**

```css
/* Density: the board and tables are dense; a project page is not. */
--space-page: 2rem;        /* vertical rhythm on a content page */
--radius-card: 0.5rem;     /* every .card uses this */
--radius-pill: 999px;      /* every .badge and .tag-pill */
--shadow-card: 0 1px 2px rgb(15 23 42 / 0.06);
--shadow-raised: 0 4px 12px rgb(15 23 42 / 0.10);
--focus-ring: 0 0 0 2px var(--slate-50), 0 0 0 4px var(--indigo);
```

`--focus-ring` is not a nicety. **Inputs already do this correctly** —
`.input:focus`, `.textarea:focus` and `.auth-input:focus` replace the browser
outline with `box-shadow: 0 0 0 3px`, and the stylesheet carries a comment saying
exactly why: *"A visible focus ring is not optional: the whole form is reached by
keyboard for anyone who does not use a pointer."* Buttons, links and the kanban
cards have **no** equivalent — so a keyboard user tabbing from a form field to a
button loses the indicator entirely. The pattern already exists; it needs
extending to the elements that never had it.

**Dark mode.** Not a `prefers-color-scheme` media query sprinkled on individual
components — the tokens exist precisely so it is one override block:

```css
@media (prefers-color-scheme: dark) {
  :root { --indigo: #818cf8; --slate-100: #0f172a; /* …the ramp, inverted */ }
}
```

Slate is already a 50→600 ramp, so inverting is mechanical. Ship it with the
document viewer, not before: it is the first page anyone will look at at night.

---

## 3. The pages

### 3.1 Priorities, and why

Ordered by what a user cannot do today at all, not by what is easiest to build.

| # | page | why now |
|---|---|---|
| 1 | **Documents** | 1,087 stored, zero views. A dead API is worse than a missing page. |
| 2 | **Consensus** | The core of the product. §5.5 is unbuildable without it. |
| 3 | **Feature detail** | Carries Elo, I/E score, linked complaints. The unit of ranking. |
| 4 | **Complaint detail** | Carries impact meter, linked features, status. §10 asks for it. |
| 5 | **Project settings** | Visibility, tags, languages, charter, invites — all shipped, none reachable. |
| 6 | **Comments** | Threads exist in the API with nowhere to read them. |
| 7 | **Merge requests** | Confirm/approve/execute/reject. The second half of §5.7. |
| 8 | **Requests + answers** | §17. |
| 9 | **Lists** | §16. |
| 10 | **Criteria** | Weight profiles behind ranking. |

### 3.2 Documents — `/{project}/documents` — **BUILT**

The highest-value page and the most mechanical. Built; see §0 for the decisions
and §9 for what is still open.

```
GET /api/v1/projects/{slug}/documents?kind=spec   → browse, grouped by kind
GET /api/v1/projects/{slug}/documents/{doc_id}    → one document
GET /api/v1/projects/{slug}/documents/search?q=   → search within the project
GET /api/v1/projects/{slug}/documents/kinds       → kind list with counts
```

Layout: a left rail of kinds (spec 344 · plan 186 · wiki 372 · readme 163 ·
adr 16 · changelog 6 — real counts), a list of documents, and a reading pane.
Selecting a document fetches it and renders the markdown.

**Markdown rendering needs care and is the one real risk on this page.** The
stored bodies are the repositories' own files: 1.1 MB `BUILD_FERRISFEED.md`, a
1.7 MB `CHANGELOG.md`, nested code fences, tables, and HTML that was never meant
to be re-rendered. Rendering requires a markdown parser, and the parser's config
is the security boundary:

- **HTML in markdown: stripped, not passed through.** These documents are
  written by any contributor with the contributor role and are not trusted
  input. A raw-HTML passthrough is a stored XSS vector against every reader.
  *(Implemented: `escapeHTML` in `markdown.js`; 6 raw-HTML vectors asserted.)*
- **Links get `rel="nofollow noopener noreferrer"`** — `target="_blank"` without
  `noopener` hands the opener to the destination. *(Implemented, asserted.)*
- **External images and iframes: blocked.** Rendering `<img src="https://…">`
  from a document turns every reader's browser into a beacon to whoever wrote
  the file, and leaks their IP to a third party. *(Implemented: images render as
  a labelled placeholder and make no request.)*
- **Render long documents progressively.** 1.7 MB of markdown as one
  `innerHTML` assignment will jank. Chunk by heading.
- **A 1 MB+ document is a scroll event, not a page load.** Virtualise the body,
  or paginate by section.

The existing `esc()` helper in every script is the right instinct and the wrong
tool here — it is for interpolating data into HTML you build, not for rendering
authored content. Do not extend it into a markdown renderer.

**As built.** `markdown.js` implements the subset: ATX headings, fenced code with
an info string, blockquotes, one-level lists with lazy continuation, tables with
alignment, rules, paragraphs, and inline code/bold/italic/strike/links/autolinks.
It is a two-pass renderer — code spans, links and images are lifted into
placeholders before the text is escaped, then restored — because doing it in one
pass mangles `**` inside a code span, which is extremely common in these
documents.

Three implementation details that were bugs first:

- **The table delimiter row is validated per cell, not by one regex.** A
  whole-line regex has to tolerate a leading pipe, a trailing pipe and three
  colon arrangements simultaneously; the first attempt rejected `|:--|:-:|--:|`
  and every aligned table silently rendered as a paragraph. Alignment silently
  vanishing is a worse bug than a broken table, because a table still reads.
- **A pipe inside a code span is not a column boundary.** `` `a|b` `` split one
  cell into two and shifted every column after it.
- **The sentinel that protects stashed fragments is NUL-wrapped.** Anything
  typable by an author could collide with a placeholder and resurrect a fragment.

**Test coverage is the point here.** `test-markdown.js` runs 80 assertions with
no dependencies: 6 raw-HTML vectors, 6 hostile-URL vectors (`javascript:` in
five spellings, `data:`, `vbscript:`), image inertness, code-fence injection,
the structural subset, this repository's own README rendered and asserted to
contain no `<script>`/`<img>`/`onerror`, and 13 termination cases (unclosed
fences, 500 asterisks, 300 backticks, 200 blockquote levels) that must finish
in under 500 ms each. Four mutations of the security boundary were applied and
each was caught: escaping neutered (8 failures), `safeURL` made permissive (4),
images made real (crash), `rel="noopener"` dropped (1).

### 3.3 Consensus — `/{project}/consensus`

```
POST   /consensus                    open a call
GET    /consensus/{id}               read positions, objections, tally
POST   /consensus/{id}/position      consent / stand_aside / block
POST   /consensus/{id}/objection     raise one
POST   /consensus/{id}/close         close
PUT    /objections/{id}/resolve      resolve
```

The tally is the whole point of the page and it is where the spec's own
inconsistency bites: §6.3 computes
`consent / (consent + stand_aside + block)`, which counts a stand-aside *against*
consent even though §3 defines it as "reservations, but I won't block". **Render
the three counts separately and never collapse them into one percentage.** The
spec fix is tracked as a complaint; until it lands, showing a single ratio would
display the bug.

Positions are consent / stand aside / block, plus Both and Neither for pairwise
features. Keyboard-first: `c` / `s` / `b`, arrow keys to change, `Enter` to
submit. Every keyboard shortcut must also be reachable as a button, because
idea #10 in the ranked 100 makes shortcuts the biggest lever on vote volume and
that is only true if the shortcut is an accelerator, never the only route.

### 3.4 Feature detail — `/{project}/features/{id}`

Carries, in order: title and status badge; **I/E score and ratio** (from
migration `0010` — this is the number the whole ranked list is sorted on);
`elo_r` with `elo_rd` shown as an uncertainty bar rather than a bare number
(idea #37: "new and unsure" must not read as "ranked low"); pain term and
strategic term with the sources shown (idea #10, "Why this rank?"); linked
complaints; consensus status; kanban card.

Every field here is already in the API response. The page is assembly.

### 3.5 Project settings — `/{project}/settings`

Four sections, all shipped, all currently unreachable: **visibility** (four
levels, a `<select>` plus a live preview of what an anonymous visitor sees —
the 404 behaviour is confusing enough to need explaining), **tags and
languages**, **charter**, **invites**.

Visibility deserves care: a private project 404s for anonymous users, which is
correct and indistinguishable from a nonexistent slug by design. The settings
page must show the owner's view and say plainly why others see nothing.

### 3.6 Comments — thread view

Lemmy-style (§7): top-level, one level of replies, vote on any comment.
`GET/POST/DELETE /comments`, `POST /comments/vote`. Author can delete their own.
Markdown applies here too — comments are user input from signed-in accounts, so
they are *more* hostile than documents, not less.

---

## 4. The layers that do not change

These hold for every page. Deviating from them is how this frontend becomes
unmaintainable.

**4.1 Escaping.** Every interpolation of server or user data into HTML goes
through `esc()`. There is no framework enforcing it, so it is a convention with
no teeth — and one missed call is a stored XSS. Any script growing past ~200
lines should switch to `textContent` and DOM construction for user data rather
than concatenating strings, because at that size the convention starts failing.

**4.2 Errors are states, not alerts.** `.error-state` with the server's message
and a retry. Never `alert()`. Never a console-only failure: a silent error that
leaves a spinner running is the current worst outcome in the codebase
(`project.js` and `board.js` both render a spinner that nothing removes on
failure).

**4.3 Loading is explicit.** Every async render starts from `.loading-spinner`
and leaves it on every path, including failure. This is the single most
consistent fix available.

**4.4 Auth.** `session.js` is the only module that touches `localStorage`; other
scripts go through `ConcordSession.instance()`. Keep the key names in one file —
`project.js` already comments on the hazard of a second script reaching into
storage directly.

**4.5 Mutations reload.** As §1.1 explains. Keep it.

**4.6 Assets are versioned.** `{{ asset "..." }}` in `base.html`. Any new script
goes through it or it will be cached stale.

---

## 5. Accessibility

Not a compliance section. Two of the requirements are prerequisites for features
already on the roadmap.

- **Every interactive element is reachable and operable by keyboard**, with a
  visible focus indicator. Inputs have one today (`box-shadow`, §2); buttons,
  links and kanban cards do not. The voting queue and the consensus position
  buttons are keyboard-first by design.
- **Every icon-only control has an accessible name.** The spinners in
  `project.html` and `board.html` already carry `role="status"` and
  `aria-label`; that is the pattern.
- **Live regions for async results.** A complaint filed, a card moved, a vote
  cast: announce it. `aria-live="polite"`, not an alert.
- **Semantic tables** for tallies and rankings, with `scope` on headers.
  `.ranking-table` already implies the structure.
- **Colour is never the only signal.** Every state pill carries its status in
  text. Health bars carry a percentage. This matters more as the palette grows.
- **Respect `prefers-reduced-motion`** for the `.fade-in` and `.stagger-*`
  transitions.

---

## 6. Performance

Targets, measured on the page a user actually lands on:

| metric | target |
|---|---|
| largest contentful paint, project page | < 1.5 s |
| interaction to next paint, vote cast | < 200 ms |
| total assets | < 200 KB, no framework |
| requests to first render, project page | ≤ 3 |

The current build is 120 KB with no build step, so these are achievable without
a framework. Two things will threaten them:

- **The 1.7 MB changelog.** Handled by §3.2's progressive rendering.
- **N+1 request patterns.** The project page currently fetches the project, then
  complaints, then features, then the user. Batch where the API allows, and
  cache `auth/me` in `session.js` for the session rather than per page — it is
  already fetched on every page load and it changes rarely.

---

## 7. What is deliberately not in this spec

- **No component framework.** §1.2 explains the reasoning and the condition for
  revisiting it.
- **No PWA or service worker.** Idea #90 in the ranked 100. Not before the
  consensus page exists; a cached shell serving stale governance state is worse
  than no cache.
- **No GraphQL client.** `concord-spec.md` §9.4 proposes GraphQL for the UI. The
  REST API is what exists and is sufficient; adding a client layer now is a
  second thing to keep in sync with no consumer.
- **No offline or draft autosave.** Idea #8 from the earlier stash-box list is
  about a different project.
- **No design overhaul.** The design system is coherent and good. It needs dark
  mode, focus indicators on the elements that lack them, and density tokens —
  not a replacement.

---

## 8. Definition of done, per page

A page is done when all of these are true. This is the checklist; a page that
skips it is not finished.

1. Mount point, spinner, and every async path removes the spinner — **including
   the failure path**.
2. Every server response the page uses is rendered. A shipped endpoint with no
   UI is unfinished work, not a backend detail.
3. Signed out: read-only, with a sign-in prompt in place of every write control.
4. Private projects render for members and 404 for everyone else, with no
   layout flash before the answer arrives.
5. Keyboard-operable throughout, with a visible focus indicator on every
   control — extending the `box-shadow` ring inputs already have (§2).
6. Markdown content rendered through a sanitising parser, never via `innerHTML`.
7. Errors render as `.error-state` with the server's message and a retry.
8. Tests in `internal/httpapi` cover the page's `200`, its `404`, and one
   anonymous case — the suite already has `newTestServerNoActor` for the last.
9. No raw hex, no ad-hoc margins, no new top-level CSS file.
10. Registered in the document viewer if it lists documents.

---

## 9. Open questions

1. **Does the consensus tally change before the UI ships?** §6.3's formula
   penalises stand-asides against consent. The page can render three separate
   counts and be correct either way, which is why §3.3 requires it — but if the
   formula changes, the headline number changes with it.
2. **Framework threshold.** §1.2 sets no date. The condition is "the API is
   stable and one page needs real interactivity". Worth naming which pages those
   are so the trigger is not a matter of taste: consensus voting, and the
   pairwise queue with its active-learning pair selection.
3. **Document search.** `GET /documents/search` exists. Whether it backs a
   project-scoped search box or the global one at `/search` is undecided; the
   global page currently searches projects only.
4. **What "collaborator" means.** Tracked as a complaint from the ranked 100.
   The frontend cannot draw role-gated UI against an undefined role. *Partly
   resolved:* §8.2's eligible-collaborator rule is implemented in
   `internal/store/eligibility.go` — five qualifying routes plus a role floor,
   inside a 365-day window. Two clauses remain unimplementable against the
   current schema and are stated in that file rather than approximated:
   "seated by charter consensus" (no table to count) and "agents are excluded"
   (`users` has no agent flag).
5. **Editing documents in the browser.** The viewer is read-only. `PUT
   /documents` exists and is role-gated, and every other write surface in this
   app has a form. Not built: the markdown source editor needs the same
   preview/render split as the reader, plus a diff against the previous revision,
   and `revision` exists in the schema precisely so that history is recoverable.
   Worth deciding whether editing belongs on this page or on the settings page.
6. **Whether the markdown subset is enough in practice.** The corpus is 1,087
   documents, and the subset was chosen by reading the spec and this repository's
   own docs, not by surveying all 1,087. Setext headings, nested lists,
   reference links and task lists are absent. The right way to find out whether
   that costs anything is to render the whole corpus through the subset and
   count what fails to produce its structure — a measurement, not a guess.