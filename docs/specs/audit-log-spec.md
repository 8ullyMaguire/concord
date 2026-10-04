# Audit log viewer — idea #25 from the ranked 100

**Status:** specified 2026-10-05. Nothing implemented.
**Origin:** the 100-ideas list, #25 "Searchable audit-log viewer" (impact 6, effort 2).
**Audit, 2026-10-05:** the *store half exists and is unreachable*. This is the fifth
instance of the "implemented and completely unreachable" class this repo documents
(`docs/HANDOFF.md` §"Things that will bite you" #8; the read-time filter; the author
join; the dropped-DTO-field series).

```
$ grep -rn "GetAuditLog" . --include='*.go' --include='*.py' --include='*.js' --include='*.html'
./internal/store/complaints.go:250:// GetAuditLog returns audit entries for a project.
./internal/store/complaints.go:251:func (d *DB) GetAuditLog(ctx context.Context, projectID int64) ([]AuditLogEntry, error) {
```

One definition. **Zero callers** — no handler, no route, no test, no page. Nothing can
fail because nothing uses it.

Meanwhile `audit_log` has **20+ distinct writers** across the codebase
(`AddAudit` call sites in `complaints.go`, `strategy.go`, `holds.go`, `merge.go`,
`features.go`, `taxonomy.go`, `projects.go`), and the instance's own §9.3 rule says a
privileged action that nobody can read is not auditable. This is the highest
impact-per-hour item in the list: the data, the schema, the index and the store method
are all already there and correct. What is missing is a reader.

## 1. What this is

A searchable, filterable audit-log viewer: one API surface, one page.

| # | Route | Scope |
|---|---|---|
| A1 | `GET /api/v1/projects/{project_id}/audit` | one project's audit log, newest first |
| A2 | `GET /audit` | the page, scoped to a project |
| A3 | `GET /api/v1/audit` | instance-wide, for a moderator |

`GetAuditLog` is project-scoped, so A1 and A2 are the direct read. A3 is new work and
is listed in §6 as out of scope for the first cut, because it needs an
instance-wide store method and the privacy question (a project-private action on a
public instance) is not settled by guessing. **Do not build A3 in this milestone.**

## 2. The two rules that decide the design

**R1 — `GetAuditLog` is replaced, not wrapped.** It has `LIMIT 100` hard-coded, no
filters, no paging, and no total. Its signature cannot express "give me the 5 newest
`strategic_weight_consent` rows". The new method is a new signature and the old one is
**deleted**, because leaving it would be the second representation of one fact — the
duplication `arenas.baseline_entry_id` already is, and that
`docs/KNOWN-ISSUES.md` records as an unresolved trap.

**R2 — an audit row is public, and `detail` is not automatically safe.**
Two separate questions, and conflating them is how a viewer leaks:

- *Who acted* — `actor_id` is already exposed by `admin_ledger`, which is
  deliberately unauthenticated (`server.go:535`: "Instance-wide, unauthenticated on
  purpose: the point is that everyone can watch what a steward did"). Resolving
  `actor_id` to a **display name** is therefore consistent, and a numeric id alone is
  useless to a reader.
- *What the detail says* — `audit_log.detail` is free text written by 20+ call sites
  with no shared contract. Some already embed user input
  (`projects.go` writes the slug; `taxonomy.go` writes a tag name). It is returned
  **as stored**, and §5.1 explains why rendering it must go through the existing
  `textContent` path, not `innerHTML`.

## 3. Why the viewer must be *searchable*, not just a list

The list is the floor. The idea says "searchable", and the reason that word is load-
bearing is measurable: the live instance's `audit_log` is dominated by a handful of
action names repeated thousands of times. A reverse-chronological list of 100 rows is
100 rows of `strategic_weight_consent`. The filter is the feature.

| filter | param | notes |
|---|---|---|
| action | `action=` | exact match on `audit_log.action` |
| actor | `actor_id=` | plus `me=1` for the session user |
| entity | `entity_type=` + `entity_id=` | the two together, because `entity_id` alone is ambiguous across entity kinds |
| text | `q=` | `LIKE '%q%'` over `action`, `detail` |
| since | `since=` | unix seconds |

`q` is a `LIKE`, not FTS5, and this is stated rather than hidden: the corpus is
`detail`, a free-text column written by 20+ sites with no agreed vocabulary, so FTS5's
tokeniser would silently drop most of what a user types. A LIKE that returns
everything matching is honest about what it does. **If `q` is empty, no LIKE is
emitted at all** — an empty `LIKE '%%'` is a full scan that reads as a slow endpoint
rather than an unfiltered one.

## 4. Paging, and the trap in it

`offset`/`limit`, default 50, max 200. `total` in the envelope so the UI can say
"showing 50 of 4,312".

`COUNT(*)` over the filtered set is a second query, and the pool is
`MaxOpenConns(1)` (`docs/PLAN.md` rule 6): collect the rows, **close the cursor**, then
count. Doing it in the other order is a deadlock that reads as a hung request. This is
the codebase's oldest recorded trap and `SearchProjects` already carries the explicit
`rows.Close()` to copy.

## 5. Tests

`internal/store/audit_test.go` and `internal/httpapi/audit_test.go`:

| test | asserts |
|---|---|
| `TestTheAuditLogIsNewestFirstAndBounded` | order, and `limit` respected |
| `TestAFilteredAuditLogExcludesWhatWasFilteredOut` | `action=` returns only that action — the seed writes two distinct actions so a filter returning everything cannot pass |
| `TestAnEmptyQuerySearchesNothingRatherThanEverything` | `q=` empty ⇒ no LIKE; a `LIKE '%%'` mutant is caught by the count staying at the unfiltered total |
| `TestATextSearchMatchesDetailNotJustAction` | a needle present only in `detail` is found |
| `TestTheTotalIsTheFilteredTotalNotTheTableTotal` | this is the test that catches "paged correctly, counted wrong", which is the defect that looks correct on page 1 |
| `TestAPrivateProjectsAuditLogIsNotReadable` | 404, same body as a missing project — the anti-enumeration rule `requireProjectID` already enforces |
| `TestTheAuditLogCarriesADisplayNameNotAnEmail` | a display name is present, an email is absent, everywhere in the body |
| `TestAnUnresolvedActorDoesNotBreakTheList` | `actor_id` NULL → `null`, not a failed scan |

**Mutation-verified.** Two mutants, each killed by a named test:

1. `LIKE '%%'` emitted unconditionally → killed by `...SearchesNothingRatherThanEverything`.
2. `total` counted unfiltered → killed by `...TheFilteredTotalNotTheTableTotal`.

A green run that was never seen red is not evidence.

Playwright, in `tests/e2e/audit_e2e.py`: the page renders, a filter narrows the list,
and a page with **no** matching rows says so distinctly from "no audit activity".

## 6. Out of scope, deliberately

- **A3, the instance-wide endpoint.** Needs a privacy ruling on project-private
  actions, not a store method.
- **FTS5 over `detail`.** §3 says why, and it is revisit-able with data.
- **Export/download.** Idea #21's export is the natural home.
- **Writing.** `audit_log` is append-only by construction here; no endpoint mutates it.

## 7. What the live run must show

```bash
make verify && make gates && make e2e
git add -A && git commit
ssh thinkcentre 'cd /mnt/disk-important/personal/documents/code/projects/concord && make deploy'
ssh thinkcentre 'curl -s "localhost:8006/api/v1/projects/concord/audit?limit=3"'
# expect entries WITH actor_display_name and total -- not {"error":"not found"}
```

The last curl is the check `make deploy`'s healthz cannot make: healthz is one route
and this is another, and a deploy that reports success while the new route 404s has
proved nothing. The same lesson as `phase3.md` S1.1's two curls.