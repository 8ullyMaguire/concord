# ADR 0018 — the home page keeps the catalogue and gains a compact overview

**Status:** accepted as a design decision, 2026-11-17. Not yet implemented; the implementation is a separate slice.
Step 2 (security baseline), product/UX decision recorded while finishing step 2b.

## Problem

Two home pages exist in this repository's history:

1. The archived Go template at `1e6d48b^:internal/httpapi/templates/index.html` was three sentences —
   "A complaint-driven, consensus-based forge for building things together" followed by
   *Complain — Build consensus — Ship it*.
2. The premise-2 theme at `1e6d48b:internal/httpapi/templates/index.html` added a hero and a
   "What Concord does" section with one paragraph per surface (complaints, pairwise solutions, consent rounds,
   gated kanban, discovery, lists).

The Rust port did not carry either forward. `crates/web/src/lib.rs:184` now declares
`/// The home page: the catalogue, as the API lists it.` and `home()` renders `h1`, one `DESCRIPTION` paragraph
and the live project list. Measured on the live site 2026-11-17:

```
curl -s https://concord.polarisocial.xyz/ | text
  -> "Concord is a consensus forge: propose, argue, decide, and keep a public record ..."
  -> "Projects" followed by 50+ named repositories (Lorebook, Commons, Whitebois, ...)
  -> no explanation of Scout, Finder, Curation, consensus, merge requests, or documents
```

A first-time visitor lands on a wall of repository names behind a cookie-consent banner. The surfaces are only in
the header nav, with no sentence saying which problem each one solves. The user prefers the earlier overview.

## Question

Is there a reason to keep the catalogue-only page instead of something like the earlier overview?

**No product or technical reason was found.** The catalogue replaced the overview because the Rust port
reimplemented `/` as a server-rendered list (`d15ceba` and later), not because a decision rejected the overview.
The catalogue is useful — it is the live state of the network — but it is a directory, not a landing page.

## Decision

Keep `/` as the catalogue and add a **compact overview above the project list**, rendered by the existing `home()`
function with the existing `Html` builder:

- one headline and one sentence naming what Concord is (reuse `DESCRIPTION`, do not invent a new claim);
- one short paragraph on the project surfaces — complaints, competing solutions, consent rounds, gated kanban,
  merge requests — linked to a real project rather than to a fictional example;
- one row of links to the live global surfaces that already exist in the nav: Scout, Finder, Search, Projects,
  Taxonomy, Statistics, Curation. Notifications is omitted from the overview because it requires identity;
  it stays in the header nav.
- the existing `h2 Projects` and full catalogue remain directly below, unchanged.

No new template parser, JS, asset or CSP change. The overview is static prose plus links to routes that are already
public and already walked by `anonymous_walk.py`.

## Rejected alternatives

- **Restore the full premise-2 hero verbatim.** It is in an archived Go template with a different asset pipeline.
  Seven marketing sections would duplicate the nav and create copy that can drift from the product; the task at hand
  is a short answer to "what is this and where do I go", not a brand site.
- **Replace `/` with the overview and move the catalogue to `/projects`.** `/` is already linked as "Concord home"
  in the shell and is asserted by page/walk gates. A redirect or relocated list adds a break for no explanatory gain,
  and `/projects` already exists with its own page.
- **Keep catalogue-only.** No recorded decision supports it, and the measured first-run experience does not explain
  a single surface. Rejected on the user's explicit preference and on the absence of a counter-argument in the repo.

## Consequences

- `home()` grows by a small static block; the catalogue query and `error_page` behavior are untouched.
- The copy names surfaces that can be renamed. Review it whenever a nav route or a project tab is renamed; keep it
  to one paragraph so that review is cheap.
- `probe_pages`, `anonymous_walk.py` and `probe_security_headers.py` remain the gates — this change introduces no new
  route, no new script and no inline JavaScript.

## Superseded in scope (2026-11-18)

D-75 in `docs/plans/refactor-roadmap.md` supersedes the "compact overview" scope: the owner asked to emulate the Go
frontend as closely as possible. The catalogue still stays below the overview; only the size of the composition changed.
