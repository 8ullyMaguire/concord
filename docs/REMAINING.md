# Concord — what is left, and what "done" means here

Written 2026-10-05. Every number below is measured, not remembered. The doc-number
gate (`scripts/check_doc_numbers.py`, in `make verify`) fails when any of them goes
stale, so this file cannot quietly become fiction.

**Measured state:** schema 25 · 25 migrations · 79 live tables · 776 Go tests across
9 test-bearing packages · 149 e2e tests in 8 suites · 7/7 UI mutants killed ·
HEAD `7114a54` on both remotes.

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
| 3 | **Feature detail** | ✅ `GET .../features/{id}`, votes, complaints, solutions | ❌ | — |
| 4 | **Complaint detail** | ✅ `GET .../complaints/{id}`, impact, validate, merge | ❌ | — |
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