# Concord — what is left, and what "done" means here

Written 2026-10-05. Every number below is measured, not remembered. The doc-number
gate (`scripts/check_doc_numbers.py`, in `make verify`) fails when any of them goes
stale, so this file cannot quietly become fiction.

**Measured state:** schema 25 · 25 migrations · 79 live tables · 801 Go tests across
9 test-bearing packages · 162 e2e tests in 9 suites · 7/7 UI mutants killed ·
HEAD `910da5e` on both remotes. `make verify` exit 0, `make e2e` exit 0.

**Updated 2026-10-05 (feature detail, then complaint detail).** Two pages built,
and **nine defects** they exposed that no existing test could see — including a
security hole and a write-only database table. See §3.

---

## 0. The one thing that is not ours to do

**`concord.polarisocial.xyz` runs an old build and this repo cannot change that.**

| Where | Version |
|---|---|
| `https://concord.polarisocial.xyz/api/v1/version` | `v0.4.0-voting-56-g3ffae12` |
| `http://127.0.0.1:8006/api/v1/version` (this host) | `v0.4.0-voting-92-g7114a54` |

The public hostname is served by a **different machine**, 36 commits behind. Evidence,
not inference: this host has no tunnel, no reverse proxy and nothing on :80/:443; the
only Concord binary on it is `/home/alvaro/concord-deploy/concord`, which `make deploy`
refreshed at 22:10 and which now answers `g7114a54`. Cloudflare reports
`cf-cache-status: DYNAMIC`, so it is not serving a stale cache either — it is
proxying to an origin elsewhere.

**Action for whoever owns that host:** `git pull && make deploy`. Nothing else is
blocked on it.

---

## 1. Pages the spec requires and nobody has built

`docs/specs/frontend-spec.md` §3.1 ranks ten pages by what a user cannot do at all.
Two are built. The rest are the real remaining work, and every one of them has a
**complete API already** — these are pages, not features.

| # | Page | Backing API | Template | Tab-linked |
|---|---|---|---|---|
| 1 | Documents | ✅ full read/search | ✅ `documents.html` | ✅ |
| 2 | Consensus | ✅ full | ✅ `consensus.html` | ✅ |
| 3 | ~~Feature detail~~ | ✅ built 2026-10-05; **new** `.../features/{id}/complaints` | ✅ `feature.html` | via feature card |
| 4 | ~~Complaint detail~~ | ✅ built 2026-10-05; **new** `.../complaints/{id}/features` | ✅ `complaint.html` | via complaint card |
| 5 | **Project settings** | ✅ charter, members, invites, tags, languages, visibility | ❌ | — |
| 6 | **Comments** | ✅ create/list/vote/delete | ❌ | — |
| 7 | **Merge requests** | ✅ create/approve/execute/reject | ❌ | — |
| 8 | **Requests + answers** | ✅ create/answer/vote | ❌ | — |
| 9 | **Lists** | ✅ create/entries | ❌ | — |
| 10 | **Criteria profiles** | ✅ list/save | ❌ | — |

Build order is the spec's, not mine: **feature detail (#3) and complaint detail (#4)
first**, because they are the two units everything else hangs off — a feature is what
gets ranked and a complaint is what justifies a feature. #5 next, because project
settings governs whether any of the rest is reachable.

Each page needs, in this order: template → route → `pages` slice entry → JS bundle →
a11y table row → `make e2e` registration. **The `make e2e` registration is three
separate places** (page route, `pages` slice, `pytest.ini`) and a missing one makes the
suite silently skip. That is how a page can be built and untested.

### 1a. Definitions of done for a page

Do not call a page done until all of these are true:

1. `go test ./internal/httpapi/` green, including a test that asserts the page's links.
2. The page is reachable **by clicking** — `TestEveryRegisteredPageHasAnInboundLink`
   enforces this and must be extended with the new page.
3. The page carries the tab bar and its own tab is lit
   (`TestSubPagesCarryTheTabBar`, `TestProjectTabMarksTheCurrentPage`).
4. An `a11y_test.go` row exists for it.
5. A Playwright suite exercises it, registered in all three `make e2e` places.
6. `make verify` and `make e2e` green.
7. **The gate can be made to fail.** Mutate the thing it guards, confirm red, restore.

---

## 2. Open items in `docs/KNOWN-ISSUES.md`

Re-audited 2026-10-05. Nine entries; none is outstanding work.

- **Closed with gates:** doc-number staleness, arena `is_baseline`, spec drift,
  append-only `pairwise_votes`, e2e clock-sync, unlinked pages, `0008` replay loss.
- **Documented, not fixable here:** `PRAGMA integrity_check` differs between SQLite
  builds (data verified intact — CLI and modernc disagree on *reporting*, not content);
  embedding coverage has a startup guard; SQLite has no `ALTER COLUMN`, so replaying
  `0008` alone still leaves `features` with an older, weaker shape.
- **Open by decision, needs a product answer, not engineering:** the §6 out-of-scope
  lists in each spec.

---

## 3. What is deliberately not being done

- **GraphQL.** `PLAN-r4.md` names it a non-goal while the API surface moves. Reopening
  it now means regenerating a schema immediately.
- **New milestones from `PLAN-r4.md`'s table.** Its status table is a historical record;
  the canonical claim list is `KNOWN-ISSUES.md` plus this file.
- **Optimising the 39e1-audit storage.** 1,806 audit rows in a 131 MB database is
  unremarkable at this scale, and the schema is indexed.

---

## 4. How to verify a change, in order

```bash
make verify   # go vet + go test ./... + CGO_ENABLED=0 build + 3 doc gates
make e2e      # 8 Playwright suites + the audit UI mutation gate
```

`make deploy` refuses a dirty tree, so commit first. It copies the binary, restarts
`concord.service`, and polls `healthz` three times before declaring success.

**A passing run proves nothing unless a check can fail.** The defects this project
shipped were green: unreachable pages, a truncated 200, a slug leak on a 404, a
14-times-duplicated document. Each was found by mutating the thing a gate guarded and
confirming it went red.

---

## 3. What building two pages exposed, 2026-10-05

Pages 3 and 4 turned up nine defects that every existing test was blind to. They
are recorded here because the patterns are the point, not the individual bugs.

| # | Defect | Cost if it shipped |
|---|---|---|
| 1 | Six endpoints read any row by numeric id | **a private roadmap, enumerable with a loop** |
| 2 | `feature_complaints` write-only in one direction | feature page's pain evidence rendered nothing |
| 3 | `complaints/{id}/features` had no reader at all | complaint page could not name the work answering it |
| 4 | `GetFeatureComplaints` returned nil for no rows | API served `null` where clients expect `[]` |
| 5 | Feature card title was a plain `<div>` | feature page reachable only by typing a URL |
| 6 | Complaint card title was a plain `<div>` | complaint page reachable only by typing a URL |
| 7 | URL parser read `parts[2]`, not `parts[3]` | all three fetches 400'd; page showed an error box |
| 8 | `/solutions` and `/consensus` are wrapped objects | "0 solutions" on a feature that had some |
| 9 | `slug` not in scope in `render()` | **ReferenceError killed the whole project page** |

Defects 5, 6 and 9 are the same failure as Finder, Scout, the consensus page and
the audit log: working code that nothing points at, or that throws when reached.

### 3.1 The shape that keeps recurring: a field nobody ever reads

`feature_complaints` was **write-only for the life of the schema.** `CreateFeature`
accepted `linked_complaints` and stored the ids; no route read them back. So the
evidence for a feature's rank was in the database and in no response.

It survived because it *looked* finished: `feature.js` had a Complaints section
reading `f.linked_complaints`, a field that is never populated on a read. The
section was implemented, the field was write-only, and the page rendered empty —
so spec §3.4's "why is this ranked here?" had no answer on the page that claims to
answer it.

**The check that catches it: for every field the UI reads, name the route that
writes it.** A field with a writer and no reader is not an unfinished feature, it
is a silent hole.

### 3.2 Bugs the Go tests could not see, caught by the browser suite

The Go tests assert on the shell and on `project.js`. Only a browser has the DOM,
and it immediately found three things:

| Bug | Symptom | Why Go could not see it |
|---|---|---|
| URL parser read `parts[2]` | all three fetches `400` | no Go test executes the JS |
| `/solutions` and `/consensus` are **wrapped** objects | "0 solutions" on a feature with none | the Go tests never read the response |
| the project card's `.card-title` is deliberately not a link | a too-broad reachability assertion reported 1 spurious difference | no Go test counts DOM nodes |

Both wrappers are silent failures: `Array.isArray({solutions:[]})` is false, the
code falls back to `[]`, and the page reports the truth about a feature whose
solutions it simply failed to load.

### 3.3 Two fixtures wrote states the product forbids

Both were written as "the obvious missing case". Neither state exists, so both
tests were asserting on fiction — and the schema and the API caught them, not review.

| Fixture | Why it is impossible | What actually signals the real state |
|---|---|---|
| `elo_r = NULL` | `NOT NULL` in the schema; `EloR` is a non-pointer `float64` in Go | `elo_rd == 350`, the starting deviation |
| a feature with no complaints | §6.2: `400 "at least one validated complaint are required"` | unlink a complaint (store-level only) |

"New and unsure" is `elo_rd == 350`, not a missing rating. That is why the page
draws a **bar** and not a number, and why the e2e fixture seeds a real
never-compared feature (`elo_r` 1500, `elo_rd` 350) instead of a null one.

### 3.4 Six endpoints served any row by numeric id

Found in the same session, before the page work — see `KNOWN-ISSUES.md`. Every one
was anonymous-reachable and read by sequential integer, so a private roadmap was
one `for` loop. The list endpoint beside them was already guarded, so the API
answered `404` for a private project's feature *list* and `200` for feature 1 of
the same project.

**The check that catches it: a guard on a `{slug}` route is not a guard on the
row.** `requireProjectID` passes on the project *named in the path*; without an
ownership check, a public project in the path serves a private row. Both were
droppable, and the ownership check survived the entire suite until a test existed
for it.

### 3.5 A static check on JS proves a string exists, never that the code runs

Defect 9 is the one worth keeping. `complaintCards(complaints)` became
`complaintCards(complaints, p.slug)` because `render()` receives the project as `p`.
`slug` was not in scope, so the line threw a ReferenceError, which aborted the whole
render — the project page showed nothing.

`TestComplaintPageIsLinkedFromTheProject` **passed throughout**, because it checks
that the link string is present in the source. A page can be entirely broken and
that gate green.

So both halves are gated now: `node --check` on each script (a syntax error in CI,
before any browser runs) and the 13 e2e tests for the runtime half. A source gate
alone would have missed the ReferenceError; a browser gate alone would have needed a
browser to find a missing brace. Two mutants confirm each can be made to fail.

---

## 4. Next, in order

Two of the eight are built. Both followed the same contract, so the next six have a
template to copy rather than a shape to discover.

1. **Project settings** — charter, members, invites, tags, languages, visibility.
   The most API surface of any remaining page.
2. **Requests + answers.** `GET /api/v1/requests/{request_id}` has **no project
   segment**: `requireReadableRequest` resolves the project *through the row*, and
   both those endpoints were leaking until that helper existed. A page built on them
   must use the helper, not a path-parameter check that cannot.
3. **Lists**, **merge requests**, **comments**, **criteria profiles** — all
   API-complete, all the same three-part contract.

Before building any of them, run the read-side check from §3.1 against the fields
the page needs: *for every field the UI reads, name the route that writes it.* Two
of the two pages built today were found by exactly that question, and both had a
complete-looking page component that rendered empty.

Every page follows the same three-part contract now, from
`internal/httpapi/feature_page_test.go`: it renders, it is **linked from
somewhere**, and it hides a private project. Plus the browser test that clicks the
link, which is the part the Go layer cannot substitute for.
