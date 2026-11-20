# Spec: site-review repairs (2026-11-20)

## Problem
The 34-route visual review found four blockers and several high-value defects on the live Concord site. A raw JSON 404 is served for unknown page URLs, `/lists` is linked from the home page but has no route, `/scout` renders two forms, the project sub-nav runs together and overflows by 241px at 390px, and several JS panels wait forever because they assume an API shape the server no longer sends.

## Goals
1. Page URLs get a branded 404 inside the normal shell; `/api/**` keeps its JSON envelope.
2. `/lists` becomes an honest page (lists live inside projects) and no longer returns raw JSON.
3. Scout has exactly one form.
4. The compact project tab strip wraps at phone width and has visible separation at desktop width.
5. Home project count and consensus calls accept the shared paged/bare list shapes.
6. Feature and milestone detail pages render or show an explicit not-found state; no permanent "Loading…".
7. Curation shows real queue rows or an explicit empty state, with a count that matches.
8. List detail caps its rendered entries with a clear "showing first N" sentence.

## Non-goals
- Cosmetic nits: hero gradient, nav density, author dates, repeated "under test" badges, settings field ordering.
- Signed-in flows: the review covered signed-out shells and gates only.
- Redesign of the visual language.

## Evidence already captured
- Browser/DOM sweep: `/home/alvaro/site-audits/concord-2026-11-20/findings.md`.
- Unknown page URL body: `{"code":"not_found","error":"no such route"}`.
- `/lists` body: same raw JSON, no `<nav>`.
- `/scout`: `document.querySelectorAll('form').length === 2`, `textarea.length === 2`.
- 390px overflow: `/projects/concord/criteria` and `/projects/concord/documents` both `scrollWidth-clientWidth === 241`.
- Consensus: `Could not load this project’s calls. the calls index returned no list`.

## Success criteria
Each numbered defect has a targeted browser/DOM check before and after, one build+deploy, and a live probe on `https://concord.polarisocial.xyz`. Roadmap, review log, handoff and vault note name the measured output. Unknown numbers stay unknown.

## Decisions
Reversible implementation choices are recorded in the roadmap Decisions section in the same commit as the code.
