# Spec — browse/detail visual polish (2026-11-18)

## Problem

A whole-site browser/vision audit of live `d444f48` proved the site is not stale: every page requests
`/assets/css/style.css?v=concord-rs 0.0.0+d444f48+vendor`, and the served CSS is byte-identical to the repo
(89,773 bytes, MD5 `db05fb28b3dcd42333d3e107286394f8`). Only the landing page looks designed because only the landing
composition was restyled in `db1a122`, `83799ae` and `e2b29bf`; the browse and detail pages kept the original
server-rendered markup and utility CSS.

The owner scoped this pass to the **main browse/detail pages only** and asked for the visual directions to be written
first: `docs/specs/2026-11-18-visual-directions.md`. Direction A (refined indigo/slate) is the working choice because
it preserves the measured contrast passes and keeps the landing page in the same visual family.

## Goals

1. `/projects`, `/search`, `/taxonomy` and `/projects/{slug}` read as one designed product with the landing page.
2. Each of those pages has a page header (eyebrow, title, lede, primary action where applicable), one card/chip
   vocabulary, and a measurable active state in the top navigation.
3. The existing accessibility floor holds: no horizontal overflow at 1280 or 390, AA contrast for text, visible focus,
   and every link/button keeps its accessible name.

## Non-goals (explicitly not shipping)

- Utility pages `/stats`, `/curation`, `/notifications`, `/login`, `/scout`, `/finder` stay as they are. Shared shells
  may gain an active nav state, but their bodies are not restyled.
- `/lists` remains a broken destination (raw JSON 404). It is recorded in the visual-directions doc as a known defect;
  adding a new page is a public-behaviour change the owner did not request in this pass.
- No new data semantics: duplicate taxonomy names are collapsed **for display** only, and no tag name is rewritten to
  hide its imported numeric suffix.
- No new webfont, CSS framework, client-side framework or theme. Direction B/C/D are documented alternatives, not
  partially adopted here.
- No change to API routes, policy rows or the step-3 module split.

## Constraints

- One Rust binary embeds the CSS and templates; changing CSS requires a release build and `make deploy`.
- Node-only DOM renderer contract (D-11/D-13/D-74): no new sink outside `markdown.js`, no `innerHTML` in page scripts.
- The existing `project-list` class is also used by utility pages. Use additive classes (`project-grid`, `tag-cloud`)
  on the four target pages so the skip list stays untouched.
- Assets are cache-busted with the build stamp from `crate::version()`; verify the stamp, not a guessed hash.

## Requirements and checks

| id | requirement | check |
|----|-------------|-------|
| R1 | Served deploy matches the tree before and after the work | `/api/v1/version` equals local HEAD stamp; served CSS bytes equal repo CSS bytes |
| R2 | Four target pages have `.page-head` with an eyebrow and a title | browser: `document.querySelectorAll('main .page-head').length === 1` on each route |
| R3 | Projects and search use the shared card grid | browser: `.project-grid > li` count > 0; computed `display` is `grid` |
| R4 | Taxonomy renders a chip cloud, not a bare bullet list | browser: `.tag-cloud` exists; its `list-style` is `none`; duplicate visible labels = 0 |
| R5 | Detail page has a restyled header and active tab | browser: `.detail-header .page-eyebrow` exists; `.detail-tab.is-active` count = 1 |
| R6 | Active nav state reflects the route | browser: one `.site-nav a.active` with the expected `href` on `/projects`, `/taxonomy`, `/projects/suki` |
| R7 | No layout regression at desktop and phone | browser: `scrollWidth - clientWidth === 0` at 1280 and 390 on all four routes |
| R8 | Contrast floor holds | browser: computed colours on new text tokens keep the existing AA ratios; no new `#94a3b8`-as-text usage |
| R9 | Unit tests cover the nav-active rule | `cargo test -p concord-web --lib`: 3 baseline tests + 4 new = 7 passed |
| R10 | Gate and live probe prove the landing | `make gate`: `gate: PASS`; `make -o release deploy`; `probe_security_headers`: PASS; `anonymous_walk`: PASS |

## Success criteria

- R1–R10 all print the measured value in the docs proof and vault handoff.
- Screenshots of the four pages at 1280×900 and 390×844 are captured and visually reviewed; the review names any
  remaining defect instead of claiming the site is perfect.
- Commits: one code commit with the measured gate line, one docs commit, one vault commit, pushed with `make push`.

## Decisions already taken

- Direction A; see `docs/specs/2026-11-18-visual-directions.md` for the four options and the rejected alternatives.
- Additive classes rather than editing `.project-list`/`.tags` globally, because utility pages share them.
- Taxonomy dedupe by display name, no rewrite of imported numeric suffixes.
- `/lists` out of scope despite being broken; recorded, not silently dropped.
