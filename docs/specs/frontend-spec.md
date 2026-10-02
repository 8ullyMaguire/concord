# Concord — Frontend Specification

**Status:** specification of record for the web client
**Date:** 2026-10-02
**Covers:** every page under `internal/httpapi/templates/` and every script under
`internal/httpapi/assets/`, and the pages that do not exist yet
**Companion:** `concord-spec.md` (the product spec). This document does not restate
it; where the two disagree about the frontend, this one is correct about what is
built and the other is aspirational.

---

## 1. The state of the frontend, measured

Not a proposal. This is what exists at commit `d5c9435`.

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
| documents | list, put, kinds, search, get, delete | **none** |
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

The sharpest instance: **1,087 documents are imported and stored, with a full
read/search API and no viewer.** A project's entire specification, plan and ADR
history is reachable only through `curl`.

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

### 3.2 Documents — `/{project}/documents`

The highest-value page and the most mechanical.

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

- **HTML in markdown: stripped, not passed through.** These documents came from
  50 repositories and are not trusted input. A raw-HTML passthrough is a stored
  XSS vector against every reader.
- **Links get `rel="nofollow noopener noreferrer"`** — `target="_blank"` without
  `noopener` hands the opener to the destination.
- **External images and iframes: blocked.** Rendering `<img src="https://…">`
  from a document turns every reader's browser into a beacon to whoever wrote
  the file, and leaks their IP to a third party.
- **Render long documents progressively.** 1.7 MB of markdown as one
  `innerHTML` assignment will jank. Chunk by heading.
- **A 1 MB+ document is a scroll event, not a page load.** Virtualise the body,
  or paginate by section.

The existing `esc()` helper in every script is the right instinct and the wrong
tool here — it is for interpolating data into HTML you build, not for rendering
authored content. Do not extend it into a markdown renderer.

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
   The frontend cannot draw role-gated UI against an undefined role.