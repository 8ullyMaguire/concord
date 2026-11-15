# 0012 — The API is a designed surface, and every route ships with its reader

- **Status:** Accepted
- **Date:** 2026-11-15
- **Supersedes:** nothing. Answers item 3 of `docs/reviews/2026-11-14-from-scratch-review.md`.

## Problem

The API grew by port, not by design. The rule for the port was "additive only, byte-identical shapes",
which was correct while a Go service was still the reference and is wrong now: it made every shape a
compatibility surface before anything depended on it, and it left the surface with no rule about what
belongs on it. The review's own words: *"Grep is the whole audit: every route with no reader is a route to
remove."* Nobody had run that grep.

## Measurement

`scripts/parity/route_readers.py` — a repeatable audit, not a one-off. It reads every `.route("…")`
literal out of `crates/app/src/lib.rs` and asks whether any reader names that path:

| reader | why it counts |
| --- | --- |
| `crates/web/assets/js/*.js` | the browser, which is one of the two clients |
| `crates/cli/src/*.rs` | the CLI, the other one |
| `tests/e2e/*.py` | the browser suites, which stand in for a reader that cannot speak for itself |
| `scripts/**/*.py`, `scripts/**/*.sh` | the probes and gate scripts |
| `crates/web/src/*.rs` | server-rendered pages that fetch their own API |

A parameter matches up to eighty characters of anything, so `'/api/v1/projects/' + slug + '/claim'` counts
as a reader of `/api/v1/projects/{slug}/claim`. The first version of the script matched the whole path as
one string and reported twenty routes unread, including `/claim`, which `adopt.js` calls on every press —
a zero grep is not a count.

Result on 2026-11-15, commit `263ef14`:

```
routes registered: 174
routes with a reader: 168
  declared indirect (a verb table, in route_readers.allow): 2
routes with no reader: 6
```

The first run of this script, on `263ef14`, said 165 and 9. The difference is `/archive` (wired into the project page, see
the table) and the merge pair (below). The audit made both obvious moves; that is the whole argument for it existing.

The final accounting, after each of these was opened and read:

```
routes registered: 174
routes with an adjacent reader (proved by the script): 166
routes declared by hand (route_readers.allow): 8
routes with only weak evidence (every segment in one file, no path): 0
routes with no evidence at all: 0
```

The eight the script could not prove, and what each one turned out to be:

| route | what it is |
| --- | --- |
| `/api/v1/projects/{slug}/merge/{merge}/approve` | `merge.js:195` — the path is built as `base + '/merge/' + id + '/' + verb` from an `ACTIONS` table. **Was genuinely unreachable**: the page that loads `merge.js` had no `#merge-root`. Wired. |
| `/api/v1/projects/{slug}/merge/{merge}/reject` | same table, same page. Wired with it. |
| `/api/v1/projects/{slug}/audit/actions` | `audit.js:252` — `fetch(base + '/audit/actions')`, where `base` already holds the project prefix. Always read; the script could not see it. |
| `/api/v1/projects/{slug}/alternatives/{arena}/vote` | `project.js:604`. Always read. |
| `/api/v1/projects/{project_id}/features/{id}/solutions/{solution_id}/coverage/{complaint_id}/contest` | `feature.js:105`, path built from three `data-contest-*` attributes. Always read. |
| `/api/v1/projects/{project_id}/strategy/themes/{id}/withdraw` | `settings.js:928`, `withdrawAll("data-withdraw-theme", "/strategy/themes/", …)`. Always read. |
| `/api/v1/projects/{project_id}/strategy/weight-proposals/{id}/withdraw` | the same wiring, a different path argument. Always read. |
| `/api/v1/projects/{project_id}/criteria-profiles/` | a deliberate alias: `lib.rs:107` registers the trailing-slash form of the handler at line 102, because the reference answered both. |

**Six of the eight were false alarms in the audit, not defects in the API** — and that is the finding worth keeping:
a grep for a path answers "can I find this string", and page scripts assemble paths through variables, so the honest
output is three answers, not two. The script therefore reports *proved*, *declared by hand with a `file:line`*, and
*no evidence* separately, and a zero in the last column is a question rather than a verdict.

`/archive` was the failure this repository produces most often, and it is worth naming: the act was built
last session with a migration, a route, a permission check, two unit tests and a passing gate — **and no
way to reach it from the site**. A capability that exists, is tested, and is unreachable is the same
defect as one that does not exist, except that the green tests argue against fixing it. It is fixed: the control
is rendered only for a reader whose role in `members` is owner or maintainer, the script is emitted only with the
control, and an anonymous reader and a stranger are refused — measured, not assumed.

## Decision

1. **A route ships with its reader.** A change that adds an API route and no caller in the same commit is
   not finished, and the review will say so. The one exception is an explicit alias, named as an alias in
   a comment.
2. **No `/api/v2` yet.** The review's middle path — publish a designed v2 beside v1 — is the right shape
   for the rewrite it describes, but it is a product decision with a client migration behind it, and doing
   it now would put a second surface in front of the same tables while the first still has unreachable
   routes on it. Fix the unreachable ones first; v2 is worth doing when a field name actually costs money.
3. **The audit is a gate, not an anecdote.** `scripts/parity/route_readers.py` is run by hand today and
   belongs in `make gate` beside the probes; its unread list must be a list of decisions, not a list of
   surprises.
4. **A reader this script cannot see is declared, not assumed.** `scripts/parity/route_readers.allow` names the routes whose
   reader assembles the path from a table (`merge.js`'s `ACTIONS`), one per line with the `file:line` that proves it. The
   count prints those separately, because a reader the audit had to be told about is a weaker claim than one it found.
5. **The unread list above is the queue.** `/archive` first (wire the act into the project page), then the
   merge approve/reject pair (either finish the merge UI or delete the routes), then the strategy
   withdraws, then the alternatives vote. Each is a decision — delete or adopt — recorded where the next
   session will read it.

## Rejected alternatives

- **Delete every unread route now.** Tempting and wrong: three of them are the write half of a feature
  whose read half is wired (merge, strategy withdrawal, alternatives voting), and deleting the write would
  make the read a lie. They are decisions, not garbage.
- **Design v1's shapes now.** This is the review's "objects that say whether a field is measured or
  declared". Correct, expensive, and it touches every client and every recorded corpus at once. It is a
  rewrite, not a cleanup, and it should be scheduled as one.
- **Freeze v1 and change nothing.** The state that produced the finding: additive-only forever makes every
  mistake permanent, and the count of routes nobody calls only grows.

## Consequences

- The repository now has a number where it had an opinion: 9 of 174 routes have no reader, 1 of those is a
  deliberate alias, and 8 are decisions that were invisible before this script existed.
- The next person who adds a route has a rule to follow and a script that will disagree with them if they
  do not.
- `/archive` is unfinished in the only sense that matters — a reader cannot use it — and that is recorded
  as the first item of the queue rather than as a completed row.
