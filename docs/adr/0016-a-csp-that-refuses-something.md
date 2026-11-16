# ADR 0016 — the browser is told what to refuse: a CSP without `unsafe-inline`, and no page that needs one

**Status:** accepted, 2026-11-16. Step 2 (security baseline) of `docs/plans/refactor-roadmap.md`, first slice.

## Problem

Measured on 2026-11-15, on the deployed instance:

```
curl -sI https://concord.polarisocial.xyz/ | grep -ic 'content-security-policy\|x-frame-options'
0
```

No `Content-Security-Policy`, no `X-Frame-Options`, no `X-Content-Type-Options`, no `Strict-Transport-Security`. The
session token lives in `localStorage` (`crates/web/assets/js/auth.js` says so, and eleven scripts read it), and the page
scripts contain **137 `innerHTML` assignments against 540 manual `esc()` calls** — escaping enforced by review, one
skipped call away from executing. With no CSP, one injected string in one project description is a full account
takeover: the token is readable from the page that executed it.

By 2026-11-16 a CSP existed, and it was the version that refuses nothing:

```
script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'; frame-ancestors 'self'
```

`'unsafe-inline'` in `script-src` permits exactly the inline `<script>` an injection needs, so the header was present and
the protection was absent. That is worse than no header, because it reads as done.

## Decision

**A policy that forbids inline script, and a renderer that no longer emits any.**

1. `security_headers` (a `from_fn` layer on the router, outermost) sets on **every** response — page, API, 404, the
   gate's own refusals:

   ```
   default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline';
   img-src 'self' data: https:; font-src 'self'; connect-src 'self';
   object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'
   X-Frame-Options: DENY   X-Content-Type-Options: nosniff
   Referrer-Policy: strict-origin-when-cross-origin
   Strict-Transport-Security: max-age=31536000; includeSubDomains
   ```

   `style-src` keeps `'unsafe-inline'` because two pages carry a `style=` attribute; `script-src` does not. `img-src`
   allows `https:` because avatars are remote, `connect-src` does not because every `fetch` in the page scripts goes to
   this origin through `api.js`.

2. The inline scripts that made `script-src 'self'` impossible are gone:
   * **seven placeholder passes** (`feature-root`, `complaint-root`, `project-detail`, `rank-root`, `ranking-root`,
     `audit-root`, `kanban-board`) — each wrapped in `if (window.ConcordSkeleton)`, while `skeleton.js` is emitted with
     `defer` and therefore runs *after* every inline block in the document. **They were no-ops from the day they were
     written**; removing them changes nothing a browser ever did, which is the honest reason they were removed rather
     than converted.
   * **the archive control**, which was a multi-line inline block because it is emitted only for a maintainer. It is now
     `crates/web/assets/js/project-archive.js`, and the tag is still conditional — a page does not ship the JavaScript
     for a control it does not render.
3. One inline event handler had to go with them: `onsubmit="return false;"` on the milestone form
   (`milestones.js`). It was load-bearing — the submit listener never called `preventDefault`, so removing the attribute
   alone would have made every milestone creation reload the page. The handler now calls `event.preventDefault()` and the
   attribute is gone.

## The instrument

`scripts/parity/probe_security_headers.py` asks two questions, because a header and a document can disagree:

* **Is the policy on the response?** Five headers, `script-src 'self'` **without** `'unsafe-inline'`, and
  `frame-ancestors 'none'` — checked on a page, an API route, an unknown path and a refusal. A header that appears only
  on 200s is a header missing where it matters.
* **Does any page still need what the policy forbids?** It parses the served HTML for `<script>` tags without `src`
  (skipping non-executable `application/json` data blocks) and names the line. That is the failure the policy would cause
  silently in a browser, caught by reading the bytes the server sent.

Before: `probe_security_headers: FAIL (10 problem(s))` against the live instance — six for `'unsafe-inline'`, four for
`frame-ancestors 'self'`. After: see the commit message for the run.

## Rejected alternatives

* **A nonce** (`script-src 'nonce-…'`). It buys the same protection and costs plumbing through 39 `<script>` tags across
  five renderer files, where a missed one fails silently in a browser and never in a test. Deleting two kinds of inline
  block is smaller, permanent, and does not have to be remembered by the next person who adds a page.
* **`'unsafe-inline'` plus `'strict-dynamic'`.** Only meaningful when a nonce or hash is also present; without one the
  browser falls back to `'unsafe-inline'` and nothing is refused.
* **Content-Security-Policy-Report-Only first.** Right for a policy whose violations are unknown; here the violations
  were counted before the change (7 dead blocks, 1 control, 1 handler), so report-only would have been a delay dressed
  as caution.
* **Trusting Cloudflare to add the headers.** The edge did not add them, and a header that exists only because a proxy
  in front of the origin happens to add it is a header that disappears when the tunnel is not in the path.

## Consequences

* An inline `<script>` added to a page now fails `probe_security_headers.py` in the live layer and, more importantly,
  stops working in a browser. The instrument and the behaviour agree, which is the property that makes the failure
  findable.
* `X-Frame-Options: DENY` and `frame-ancestors 'none'` mean concord cannot be embedded. Nothing deploys it in a frame;
  if something ever does, it will say so.
* Still open in step 2: the session cookie (`HttpOnly; SameSite=Lax`, so an XSS cannot read the token), rate limiting
  through the vendored `polaris_ratelimit` (592 lines, still imported by zero files), and the `el(tag, props, children)`
  renderer that makes `innerHTML` unavailable rather than merely discouraged.

## Addendum, same day: two policies, and why the probe had to say "intersection"

The first live run after deploying found something the candidate could not: the response carried **two**
`Content-Security-Policy` headers — the app's strict one and nginx's estate-wide one from
`/etc/nginx/snippets/estate-security-headers.conf`:

```
script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'; frame-ancestors 'self'
```

Every estate vhost inherits that snippet, and nginx *appends* its `add_header` to whatever the upstream sent rather than
replacing it. So the header this ADR is about was one of two, and the probe — reading one header per name — reported the
wrong one.

What a browser does with two policies is **enforce both**, which means the effective `script-src` is the intersection:
`'self'`, with no `'unsafe-inline'`, because the strict policy does not allow it. The page is protected. But it is
protected *by accident of aggregation*, and that is not a property to leave to chance: delete the app's header and the
remaining policy refuses nothing.

So the probe now:

* keeps every CSP header apart instead of taking one per name,
* computes each directive's **effective** source set as the intersection across policies (a source must be allowed by
  every policy that mentions the directive), and judges that,
* treats `frame-ancestors 'none'` in *any* policy as the strictest outcome, because one policy forbidding every frame is
  already absolute — and an empty intersection means the same thing,
* prints `[N policies; M permissive]` beside each URL, so a second policy cannot become invisible again.

Its failure mode is the one that matters: with the app header gone, the only policy left is the permissive one and every
URL fails with `effective script-src allows 'unsafe-inline'`.

**Not fixed here, deliberately:** the estate snippet itself. Dropping `'unsafe-inline'` from it would need every one of
the sixteen estate sites to be inline-script-free, and this change cannot claim that about any site but concord. The
snippet is the estate's single point of policy and deserves its own audit, one site at a time, with this probe pointed at
each. Recorded in the plan as an estate-level item, not a concord one.
