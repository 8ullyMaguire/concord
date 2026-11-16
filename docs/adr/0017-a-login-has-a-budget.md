# ADR 0017 — a login has a budget, chosen by the route and keyed by a header the client cannot forge

**Status:** accepted, 2026-11-16. Step 2 (security baseline), slice 2b-i.

## Problem

Measured on 2026-11-16:

```
grep -rn "rate\|ratelimit" crates/app/src/lib.rs     -> nothing
cargo tree -i polaris-ratelimit                       -> nothing depends on it
python3 scripts/parity/probe_rate_limit.py --candidate https://concord.polarisocial.xyz --budget 10
    login #1..#13  403
    FAIL no 429 in 13 logins from one client — the route has no budget (statuses: [403])
```

The vendored `polaris_ratelimit` crate has been in `vendor/` since the port: a token bucket with the clock injected,
per-class quotas, `client_key` that trusts `CF-Connecting-IP`/`X-Forwarded-For` only from a loopback peer, an idle sweep
and a bucket cap. 592 lines, tested … and imported by nothing. Meanwhile `POST /api/v1/auth/login` answered every request
a caller made to it, forever, from one address. That is the shape of a credential-stuffing surface: the cost of a guess is
the client's, not the server's.

The session token living in `localStorage` (ADR 0016's problem) makes it worse — a stolen token needs no guessing at all —
but a login with no budget is the half of the attack that needs no theft.

## Decision

**`crates/app/src/rate.rs`, one `from_fn_with_state` layer, `polaris_ratelimit`'s limiter behind `AppState::rate`.**

1. **The class comes from the matched route, not the caller.** `MatchedPath`, so the classification cannot be steered by
   how a path is spelled (`/api/v1/auth/login` and `/API/V1/AUTH/LOGIN` are the same route and the same class):

   | class | matched path | default |
   |---|---|---|
   | `login` | ends `/auth/login`, `/auth/register`, `/guests` | 10/min |
   | `asset` | `/assets/…`, `/favicon.ico`, `/robots.txt` | 3000/min |
   | `api` | `/api/…` | 600/min |
   | `default` | everything else | 300/min |

   `asset` exists because one page load asks for a dozen scripts, and charging those to the same budget as the API would
   refuse a reader for reading. `login` is narrow because it is the only class where a wrong guess costs the *attacker*
   nothing. Every class is overridable (`CONCORD_RATE_LOGIN`, `CONCORD_RATE_API`, `CONCORD_RATE_ASSET`,
   `CONCORD_RATE_DEFAULT`), because a hard-coded budget is a deployment nobody can tune.

2. **The key is `client_key(peer, CF-Connecting-IP, X-Forwarded-For)`**, unchanged from the vendored crate: the headers are
   read only when the peer is loopback, and `CF-Connecting-IP` is the one header Cloudflare overwrites on every request. A
   client cannot mint a new identity for itself without being nginx first. Where the peer is absent (a unit test calling
   the router directly) the peer is loopback, which is what a test is.

3. **A refusal is `429` + `Retry-After` (integer seconds, rounded up) + `X-RateLimit-Limit` + `X-RateLimit-Remaining`, and
   every allowed response carries the limit and what is left.** A limit nobody can observe is a limit nobody can tune, and
   the client's only useful response to a refusal is to wait — which it can only do if the refusal says how long.

4. **The layer sits above the identity gates.** A flood must be refused before it costs a session lookup or a database
   round trip, and the refusal must not depend on who is calling. (It sits below `security_headers`, so a 429 still
   carries the CSP.)

## The instrument

`scripts/parity/probe_rate_limit.py` and two unit tests (`cargo test -p concord-app --lib rate`).

The probe is given the budget it is testing (`--budget`, matching `CONCORD_RATE_LOGIN`) rather than guessing, and asks
four questions a rate limit has to answer at once:

| question | how it is answered | why it matters |
|---|---|---|
| does the budget hold? | the Nth request is allowed, the N+1th is 429, `X-RateLimit-Limit` equals the configured budget | a limit that never trips is decoration |
| can the client act on the refusal? | `Retry-After` is a positive integer, `X-RateLimit-Remaining` is `0` | a 429 nobody can comply with is a wall |
| is it per client? | a second `CF-Connecting-IP` is still answered | one loop must not silence the site |
| is it per class? | an ordinary `GET /api/v1/projects` is unanswered by a login burst | classes that share a bucket refuse readers |

Before: `FAIL (1 problem(s))` against live — no 429 in 13 attempts. After: see the commit message.

Run against a candidate with a lowered budget so the refusal is provable in seconds instead of by flooding a real one:

```
CONCORD_RATE_LOGIN=3 ./concord-cli serve --addr 127.0.0.1:8097
python3 scripts/parity/probe_rate_limit.py --candidate http://127.0.0.1:8097 --budget 3
```

## Rejected alternatives

* **A fixed-window counter on the login handler.** It is three lines in the handler and it is wrong twice: no class table
  (so the next route that needs a budget invents its own), and no per-client key (so the counter is either global or keyed
  on a header the caller chooses).
* **Rate limiting in nginx.** The edge cannot express "five login attempts per client per minute and six hundred API reads",
  and it cannot see the matched route. It is the right place for a connection flood, which is a different instrument.
* **Trusting `X-Forwarded-For` first.** It is appended to by nginx and its first element is whatever the caller sent, so a
  scraper gets a new identity per request. `CF-Connecting-IP` is overwritten by Cloudflare and is the primary key here.
* **A `429` with no body and no headers.** Cheap to write, impossible to obey: a client that cannot tell a rate limit from
  a forbidden resource retries immediately and makes the flood worse.
* **Wiring a distributed limiter (Redis).** One host, one process per product; a Redis bucket can be down while the site is
  up. The vendored crate says the same thing in its own header comment.

## Consequences

* A caller who exceeds a class is refused for the rest of that window; the window refills continuously, so a client that
  slows down is served again without waiting for a boundary.
* The e2e suites run against a fresh process, so each gets a fresh limiter — but a suite that makes more than 600 API calls
  in a minute would now fail. If that happens, the honest fix is `CONCORD_RATE_API` in the harness, not raising the default
  until it stops tripping.
* Still open in step 2 (2b-ii): the session cookie (`HttpOnly; SameSite=Lax`) so an XSS cannot read the token — in flight
  in the other session as `presented_token` — and the `el(tag, props, children)` renderer that makes `innerHTML` unavailable
  rather than merely discouraged.
