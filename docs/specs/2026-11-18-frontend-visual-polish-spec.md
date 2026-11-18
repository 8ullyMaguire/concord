# Frontend visual polish — emulate the Go implementation (2026-11-18)

## Problem

Two measured defects on the live site, both reproduced with browser tools:

1. `/` renders only a bare `<ul>` of project names. The Go implementation's landing page had a dark hero, six "What Concord does" cards, a live stats strip, four Explore cards and a "flip" band. ADR 0018 decided to keep the catalogue and add the overview above it; the overview was never implemented. The stylesheet and `index.js` for it are already ported verbatim, so the visual regression is structural markup, not missing CSS.
2. `/projects/{slug}` renders "Could not load project" for every project even though all fourteen API fetches return 200. Browser exception capture shows `ReferenceError: mutant is not defined` at `project-render.js:37` in `panelEmpty()`. A mutation-test token was committed to production JS. The stray `"` seen in the screenshot is the same broken tree's text node.

## Goals

- Port the Go landing-page composition into the Rust home handler: hero, six feature cards, stat strip, Explore grid, flip band, catalogue below.
- Load the already-ported `skeleton.js` and `index.js` on `/` so the stat strip fills from the public API.
- Remove the `mutant` token from `panelEmpty()` so project overviews render.
- Prove the result in a real browser at desktop and mobile widths, with screenshots reviewed by vision, plus DOM assertions and the existing gate/probes.

## Non-goals

- No CSS rewrite: `crates/web/assets/css/style.css` already contains every class the Go page used (`.hero`, `.grid-responsive-3`, `.icon-tile`, `.stats-strip`, `.grid-4`, `.explore-title`, `.flip-section`, `.fade-in`). Only add a rule for the catalogue list if it remains unstyled.
- No new framework, Svelte bundle, hydration layer, API shape change, auth change, or step-3 `lib.rs` move.
- No change to the consent notice, session handling, or the 132-page gate contract.
- No claim that the 21 `RECORDED_UNRUN` e2e suites were run — `pytest`/`playwright` are absent.

## Constraints

- Compile and deploy on thinkcentre only, `/personal/documents/code/rust/concord`; commit on `port-rust`.
- Active foreign turn leases: guarded writes only — check HEAD, require clean tree, whitelist staged files at commit.
- Static SVG icons are trusted literals; emit them only through `Html::raw` with a comment saying why they are safe. Every dynamic value continues through `Html::text`/attribute escaping.
- Page scripts use `crate::shell::asset()` so the version query busts caches.
- `concord-rs.service` serves the site; `concord.service` stays disabled.
- No secrets in notes or commits.

## Success criteria (measured, not asserted)

1. `cargo check -p concord-app --locked` and `cargo test -p concord-web` (if tests exist) exit 0 on thinkcentre.
2. Stamped gate on the new code HEAD: `gate: PASS`, `pages: 132/132 ok`, rate probe PASS; `/version` equality true after `make -o release deploy`.
3. Browser at 1280×900 on `/`: hero heading visible, six `.card` feature cards, `#stat-projects`/`#stat-tags`/`#stat-languages` populated with non-placeholder values, `aria-busy="false"` on `#stats-grid`, four Explore cards, flip band, and the catalogue still present below.
4. Browser at 390×844: no horizontal overflow, hero and cards readable, catalogue reachable.
5. Vision review of desktop and mobile screenshots rates the page as a designed landing page, not a bare list.
6. `/projects/suki` renders project content (no "Could not load project"), with zero captured `ReferenceError`s and all fetches 200.
7. Roadmap Decisions section, review log and vault handoff rewritten in place with these measured lines; code, docs and vault each committed with the hash named.
