# Questions for the spec owner

Decisions that change more than one surface, or that a spec section should own.
`docs/HANDOFF.md` says spec changes are proposed here rather than made in a
page's commit; this is the first entry, and it is a real blocker rather than a
style question.

## Q1. How does a browser session reach the server on a NAVIGATION?

**Status:** open, blocking a visible feature.
**Raised:** 2026-10-15, found while building the settings administration panels
(`docs/REMAINING.md` §13.4).

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
