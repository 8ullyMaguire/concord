# Questions for the spec owner

Decisions that change more than one surface, or that a spec section should own.
`docs/HANDOFF.md` says spec changes are proposed here rather than made in a
page's commit; this is the first entry, and it was a real blocker rather than a
style question.

## Q1. How does a browser session reach the server on a NAVIGATION?

**Status:** ANSWERED and implemented 2026-10-15 — option 1, narrowed to non-API
GETs. `docs/REMAINING.md` §14 has the implementation and the gates. The question
and the rejected options are kept below rather than deleted, because the
rejection reasoning is the part that would otherwise be re-litigated.
**Raised:** 2026-10-15, found while building the settings administration panels
(`docs/REMAINING.md` §13.4).

**The answer taken:** a `concord_nav` cookie, set at login and register, HttpOnly,
SameSite=Lax, session-scoped, honoured by `authenticate` ONLY for a GET whose path
is not under `/api/`, and cleared at logout. That is option 1 with the CSRF
surface removed by construction rather than by a token: every mutating route in
this server is an API route, so the cookie cannot authorize a write, and no API
read sees it either. `nav_session_test.go` asserts both halves, and the negative
half is verified by mutation — removing the method-and-path condition turns it
red. Option 2 (a service worker adding `X-Auth-Token`) would also have worked and
needs no spec text at all, but it costs a service worker, its cache rules and a
registration path to carry one header; this is smaller and testable in Chrome and
in `httptest` alike. Option 3 (a token in the URL) stays rejected: it leaks
through `Referer`, history, logs and screenshots.

### The problem

Every page that is not public is gated by `projectReadable`, which needs the
request to carry the actor. A browser **navigation** cannot carry a token: the
session lives in `localStorage` and is attached by script, and the script has not
run when the page's own HTML is served. So a member who opens a protected or
private project in a browser gets the "There is no project at this address" page.

Measured, not reasoned: with the project's visibility set to `protected` (from
the settings page itself, which is how it was noticed), the settings page's
`#settings-root` never renders and the browser test's wait times out. Every
member-only panel on it — charter, members, criteria profiles, tags,
completeness, invites, languages — was unreachable for the person entitled to see
all of it.

`GET /api/v1/...` reads are unaffected in principle: the page's own script can
send the token, and after this pass it does (the reads were anonymous before,
which is a separate defect fixed in §13.3).

### The options

1. **An HttpOnly session cookie set at login** (and cleared at logout), read by
   `authenticate` as a fallback when no bearer token is present. This is the
   conventional answer. It brings CSRF with it: cookie-authenticated writes need
   a token or `SameSite=Strict` plus an origin check, and the existing
   `Authorization`-header writes are not exposed to CSRF at all. The spec has no
   CSRF section today.
2. **A service worker** that adds `X-Auth-Token` to navigations, which is the
   mechanism `auth.go`'s `bearerToken` comment already anticipates ("for browser
   clients that cannot set headers on a navigation"). No CSRF surface, since the
   header must be added by first-party script. Costs a service worker, its
   caching rules, and a registration path.
3. **`?token=` on page URLs.** Rejected without building it: a token in a URL
   leaks through `Referer`, browser history, server logs and any shared
   screenshot, and this project stores a full audit log of everything else.

### The question

Which of these does the spec want, and does it want the `Authorization`-header
path to remain the only write path (option 2, no CSRF surface) or to be joined by
a cookie (option 1, conventional, needs a CSRF design)?

Recommendation: **option 2** for now, then option 1 when a session that survives
a browser restart becomes a requirement. A cookie introduced without a CSRF
section in the spec would be a security model invented in a page's commit, which
is exactly what this file exists to prevent.

## Q2. Is §6.2's `theme_weight` the themes alone, or the themes plus the feature's own weight?

**Status:** decided for now (the sum), recorded here because §366 names one input and the tree has
two mechanisms reaching for its slot. **Raised:** 2026-10-16, while building §6.2's themes
(`docs/REMAINING.md` §25) — see `docs/specs/strategic-themes-spec.md` D1.

§363's formula has one term: `priority = (r − 2·RD) + λ·log(1 + pain_sum) + μ·theme_weight`. Two
things in this repository want to be that term:

1. **A feature's own ratified weight** (§12.3's weight proposals): `features.strategic_weight`,
   changed only by consensus, with a consent route, store tests and a test asserting that
   ratification moves the score.
2. **Tag-scoped themes** (§366's own words: "tag-based weights such as 'security hardening: +2'"):
   `strategic_themes`, summed over the feature's matching tags by `ThemeWeightTotal`.

The implementation sums them, so neither mechanism is inert. The alternative readings:

- **Themes only** — the closest reading of §366, and it makes the whole per-feature weight mechanism
  inert (`strategic_weight` would stop reaching the ranking, and the tests that pin its effect would
  have to go).
- **Feature weight only** — the status quo before this pass, which left §366 unimplemented and the
  third steering input non-existent.

**What the spec owner has to decide:** whether a feature can be steered BOTH by its own ratified
weight and by tag-scoped themes in one μ term, or whether one of the two is the μ term and the other
belongs elsewhere (a separate exponent, a tie-break, or nowhere). The cost of the current choice is a
feature tagged `security-hardening` scoring on both dials at once, which is defensible and is not
obviously what a reader of §366 would predict.

## Q3. Where does the PAIN side's `theme_multiplier` come from?

**Status:** not built, and cannot be built without this answer. **Raised:** 2026-10-16, same pass.

§348–350 gives the pain formula a `theme_multiplier` "taken from consensus-ratified strategic themes
(§6.2)". The themes themselves are now real (Q2's mechanism), but the question is which themes apply
to a GIVEN COMPLAINT, and the schema has no answer: `complaints` carries project, author, title,
body, severity, frequency, `strategic_multiplier` and status — no tags, and `complaint_tags` (which
exists, with the same merge machinery as `feature_tags`) has no writer.

The three candidate readings, none of which this pass could pick without inventing product behaviour:

1. **Every theme in the project applies to every complaint** — then the pain term becomes a constant
   multiplier per project, which is not a multiplier at all.
2. **The complaint's own tags** (once `complaint_tags` has a writer, a sibling of §25.3's work):
   precise, and it makes filing a complaint a two-part act (text, then tags) that nothing in the spec
   describes.
3. **The tags of the feature the complaint is linked to** — reuses §25.3, but a complaint usually
   EXISTS before it is linked to a feature, so most complaints would carry no multiplier.

**What the spec owner has to decide:** whose tags the pain-side multiplier reads, and whether
complaints get tags at all. Until then the pain side keeps its `strategic_multiplier` column and the
theme multiplier is not applied, which is a visible gap in §348 rather than a silent invention.
