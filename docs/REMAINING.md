# Concord — what is left, and what "done" means here

Written 2026-10-05, revised 2026-10-06. Every number below is measured, not remembered.
The doc-number gate (`scripts/check_doc_numbers.py`, in `make verify`) fails when any of
the schema/runtime numbers goes stale, and the e2e numbers are gated by
`tests/e2e/harness_selftest_test.py`, so this file cannot quietly become fiction.

**Measured state:** schema 26 · 26 migrations · 81 live tables · 1004 Go tests across
9 test-bearing packages · 277 e2e tests in 19 suite files · 16/16 UI mutants killed (7 audit.js, 9 consensus.js) ·
`make verify` exit 0, `make e2e` exit 0.

This line previously read "882 Go tests ... 174 e2e tests in 10 suites". The Go half was
gated and correct; the e2e half was reconciled by nothing and was wrong twice over — the
suites had grown to 13 while a directory-level `pytest tests/e2e` collected only 8 of
them, and 58 of the 210 e2e tests then collected were unreachable from it. Both numbers are
now measured by a test. See §12.

**Updated 2026-10-05 (feature detail, then complaint detail).** Two pages built,
and **nine defects** they exposed that no existing test could see — including a
security hole and a write-only database table. See §3.

---

## 0. The one thing that is not ours to do

**`concord.polarisocial.xyz` runs an old build and this repo cannot change that.**

| Where | Version |
|---|---|
| `https://concord.polarisocial.xyz/api/v1/version` | `v0.4.0-voting-56-g3ffae12` |
| `http://127.0.0.1:8006/api/v1/version` (this host) | `v0.4.0-voting-123-gae1e210` (`ae1e210`, the last commit that changed code — doc-only commits after it do not change the binary) |

Both rows were re-measured 2026-10-16: the public host still answers `g3ffae12` (67
commits behind), and this host answers `gae1e210`, deployed by `make deploy` and
verified live (the hold scoping, the release route and the empty-reason refusal were all
exercised against the running binary — `docs/REMAINING.md` §21.7).

The public hostname is served by a **different machine**. Evidence, not inference: this
host has no tunnel, no reverse proxy and nothing on :80/:443; the only Concord binary on
it is `/home/alvaro/concord-deploy/concord`, which `make deploy` refreshes, and which
answers `gae1e210`. Cloudflare reports `cf-cache-status: DYNAMIC`, so it is not serving a
stale cache either — it is proxying to an origin elsewhere. Cloudflare reports
`cf-cache-status: DYNAMIC`, so it is not serving a stale cache either — it is
proxying to an origin elsewhere.

**Action for whoever owns that host:** `git pull && make deploy`. Nothing else is
blocked on it.

---

## 1. Pages the spec requires and nobody has built

**SUPERSEDED 2026-10-05 — every item below is now built.** Kept as the record of
what the spec claimed at the start. The live list is §11; §4 ("Next, in order") is
superseded by it. See §11 for what the read-side check found afterwards.


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
| 6 | ~~Comments~~ | ✅ built 2026-10-05 on the feature + complaint detail pages; **new** thread-ownership guard | ✅ shared `comments.js` | on both detail pages |
| 6b | ~~Lists (§16)~~ | ✅ built 2026-10-05; **new** pairwise Glicko-2 vote (§16's central claim, previously unbuilt) | ✅ `lists.js` + `list.js` | index + ranking detail |
| 17 | ~~Requests board~~ | ✅ index added 2026-10-05 (only a DETAIL page existed, so no id was discoverable) | ✅ `requests-index.js` | tab + project overview |
| 7 | **Merge requests** | ✅ create/approve/execute/reject | ❌ | — |
| 8 | ~~Requests + answers~~ | ✅ built 2026-10-05; **new** accept-answer, request tags, instance-wide board | ✅ `requests.html` | via request card |
| 9 | ~~Lists~~ | ✅ create/entries **+ §16 pairwise ranking vote (new)** | ✅ `lists.js` + `list.js` | tab + project overview |
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

**SUPERSEDED 2026-10-05 by §11.** Items 1–3 here are now built and verified;
the remaining work is the nine unread endpoints and the settings page, both in §11.


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

---

## 5. Six code-vs-schema mismatches, and what found them

**2026-10-05, requests page (#8).** The new tool is `tests/e2e/api_surface.py`: it
seeds one project with one of everything and prints the status, shape and first bytes
of every GET the remaining pages need. It exists because each of the two pages built so
far turned up the same class of defect on first run — the JS guessed a response shape,
rendered empty, **and logged no error**. A page reporting "0 answers" for a request
that has some is worse than a crash, because it is believed.

Every one of the six was Go code that had never been run against the migration that
creates its table.

| # | Where | Symptom |
|---|---|---|
| 1 | `GetRequestAnswers` SELECT | `no such column: author_id` — the column is `proposed_by`, and an answer carries a Glicko rating, not a score |
| 2 | `AnswerRequest` INSERT | omitted the NOT NULL `project_id`; and the handler passed `getActorID(r)` where a project id belonged, so an answer would have been filed under whichever project shared the caller's user id — a **cross-project write** |
| 3 | `VoteAnswer` | `no such column: answer_id`. `request_answer_votes` is **pairwise** (`answer_a`, `answer_b`, `outcome`), not per-answer with a `direction`; the old code also summed into a `score` column that does not exist |
| 4 | `comments.label` | `converting NULL to string is unsupported`. Read side 500'd on any unlabelled comment; write side put `""` where the CHECK wants NULL, so an unlabelled comment could not be **created** either — the ordinary case |
| 5 | `requests.project_id` | `converting NULL to int64 is unsupported`. The column is nullable and the migration says *"NULL = instance-wide board"*; `CreateRequest` required a project id and every caller passed one, so that board was **unreachable** |
| 6 | `requireReadableRequest` | called `GetProjectByID(0)` for an instance-wide request. No project has id 0, so the lookup failed and every instance-wide request was **unreadable** — via detail page, answers, tags, accept and vote |

Two read sides simply did not exist: `GET .../merge` was **405**, the only route in
`server.go` with no read side, so the approve/execute/reject endpoints had no list to
appear on.

**The generalisable lesson.** Five of the six are one mistake: a nullable column, or a
column that was renamed, where one caller was updated and the rest were not. Ask of
every helper on a row *what does the other branch of the nullable column need*, and ask
of every column *which route writes it*.

**Now measured once, not rediscovered per page.** `api_surface.py` also records that
`/charter` returns PascalCase keys unlike every other endpoint; that `/tags` wraps as
`{tags:[]}` while `/members`, `/lists`, `/requests`, `/criteria-profiles` are bare
arrays; that comments live at `/threads/{kind}/{id}`, not `/comments`; and that
`request_answers` is `UNIQUE (request_id, project_id)` — so one project states one
answer, and an answers page must not offer "add another answer" within one project.

**And the instance-wide board works end to end.** `requests.project_id IS NULL` plus a
pairwise answer vote is the only way two projects' answers meet on one question, and
`CreateInstanceRequest` + `GET/POST /api/v1/requests` is what makes it reachable.

**§17's accepted-fit badge was storable and unshowable.** `requests.accepted_answer_id`
arrived with migration 0008 and had **no Go code at all**. It is now
`AcceptAnswer`/`UnacceptAnswer` + `PUT/DELETE /api/v1/requests/{id}/accept`, and a test
pins §17's exact clause — the badge is *displayed but does not override the ranking* —
by seeding the accepted answer with the **lowest** rating and asserting it still sorts
last. `request_tags` had no store method either, so §17's "requests are taggable"
constraint had no read side at all.


---

## 6. Comments were a write-only surface, and writing the tests found two more defects

**2026-10-05.** Four working endpoints — create, list, vote, delete — and a grep for
`threads/` across `internal/httpapi/assets/js/` returned **nothing**. Not an unfinished
page this time but a missing one: the API is thread-shaped
(`/api/v1/projects/{id}/threads/{kind}/{thread_id}`) and fully tested at the store
layer, and no user could reach it. The same shape as `feature_complaints`, found by the
same question: *for every field the UI reads, name the route that writes it* — and its
inverse, which is the one that bites: *for every endpoint the API offers, name the page
that calls it.*

One `comments.js`, mounted by both the feature and the complaint detail page with the
kind and id in `data-thread-kind` / `data-thread-id`. Two copies of the same renderer
drift, and then one page keeps a bug the other has fixed.

### Three defects the new tests found, all in the store

1. **A blank comment was accepted.** `comments.body` is `TEXT NOT NULL` and nothing
   else, so SQLite took both `''` and `'   '` and the endpoint returned **201**. A
   thread then carries a row that renders as nothing, and a reader cannot tell an empty
   comment from one that failed to load.

2. **Comments crossed project boundaries.** `comments.thread_id` is a bare `INTEGER`
   with **no foreign key** — the one column in that table that references something and
   does not check it. A comment could name a feature in another project and be filed
   under this one: a cross-project *write*, and a read of another project's discussion
   through the wrong URL. That is the write side of the defect
   `id_enumeration_test.go` fixed on the read side.

3. **`thread_kind='release'` is in the CHECK list and there is no `releases` table.** A
   comment on a release could never resolve a project, so the ownership query would be a
   **500** on a valid-looking kind. And `projects` has no `project_id` column — a project
   is its own owner — so kind `project` is answered by comparing ids, with no query at
   all. Both found by asking what table each `CHECK`ed kind points at.

Plus: a reply's parent is checked on **all three** axes, and each was made
individually load-bearing. `parentThread` alone caught everything at first, so the kind
and project terms were never exercised — and mutation proved it: dropping either one
left the suite green.

**Every one of the seven existing comment tests used the literal `1` as the thread id**,
with no such feature. They worked *only* because the foreign key was missing. Fixing the
fixtures rather than weakening the guard is the point: a fixture that only passes because
a constraint is absent is a fixture asserting fiction.

### The mutation lesson, written into the skill

Two of the survivors were **not missing guards**:

- a `-run Comment` filter that did not match the two tests written to kill them, so the
  gate never ran and every mutant "survived" — indistinguishable from a real gap
- a loop without a `finally` that wrote a mutant over the source and lost 163
  uncommitted lines. The file now has a `/tmp` backup and an unconditional restore.

And one guard was unreachable through the API: `thread_kind` follows from `thread_id`,
so two comments sharing a thread necessarily share a kind. That term is pinned by a
direct SQL INSERT instead — the thing the guard exists to survive.


---

## 7. Lists: §16 said "ranked, not just listed" and nothing ranked anything

**2026-10-05.** §16's second line is the whole point of the feature: *"Entries are
ranked, not just listed. Entries compete in pairwise comparisons (the same Glicko-2
engine as feature priority), so 'best first' reflects community judgment instead of
alphabetical accident."*

`list_entry_votes` was in migration 0001, `list_entries` carried `elo_r`/`elo_rd`/
`elo_vol`, and `GetListEntries` already did `ORDER BY elo_r DESC`. **No Go code wrote
the table.** No route called a vote. So every entry read back at its 1500 seed forever,
every list was in creation order, and the section's central claim was false in exactly
the way it named.

### Four defects, and the instance-wide scope was unreachable four ways

The interesting part of this section was not the missing vote. It was that
**"attachable to a project or instance-wide" (§16, line 437) could not be done at all**:

1. `List.ProjectID` was an `int64`, so `SELECT`ing an instance-wide row failed the
   scan and the detail route answered **500**. The problem looked like a database
   error; it was a type.
2. `GetListsByProject` filtered `project_id=?`, which never matches `NULL`, so no index
   could return one.
3. `handleGetList` required `l.ProjectID == projectID` — 0 against a real id — so the
   only detail route 404'd.
4. **`CreateList` sent `0` to the column**, which has a `FOREIGN KEY` to `projects(id)`
   with no row 0. So even the store refused: *"FOREIGN KEY constraint failed"*, naming
   a constraint when the real fault was 0-versus-NULL.

Each alone would have been a bug. Together: a list created instance-wide was invisible,
unreadable, and **impossible to create over HTTP** — a spec feature reachable only from
SQL. There was no `instance_wide` field, because the path's project was the only source
of scope.

### Two copies of one guard, disagreeing

`handleGetList` and `requireReadableList` both answered "may this actor read this list",
and they **disagreed**: fixing instance-wide in one left the other refusing it. So
`handleGetList` now calls `requireReadableList` instead of repeating it. And the nil
branch inside that function was dead code — only one route used it — which is why
mutating it away left the suite green until the entries route got its own assertion.

### A gate satisfied by my own comment

The link gate read `project.js` as text and required `listSection(lists, p) +`. It
passed **while the call was deleted**, because the comment I had written above the
function quoted the expression back:

```go
// That gate wants the concatenation `listSection(lists, p) +`,
```

**A gate satisfied by a comment is worse than no gate, because it is confident.** The
static half now asserts the call exists by index and the DOM half moved to Playwright,
where a client-rendered anchor can actually be seen.

### A test that was wrong while the code was right

The ranking assertion compared titles in DOM order against the rating order — and
failed on **correct** output. Entries are grouped by category (§16), so document order
is `[runtime group, uncategorised group]`, and the assertion conflated two independent
claims. It now reads the rank off each row's `data-entry-id`.

And the seed that proves §16 at all is built so **alphabetical and rating order
disagree completely** (`aaaa first` is best, `zzzz last` is worst). Without that, an
unranked implementation and a correctly ranked one render identically.

882 Go tests. `make verify` and `make e2e` green. (historical)

---

## 8. The board, the tabs, and two filenames that had to be split

**2026-10-05.** With lists and requests built, the question became *can a reader reach
them*, and the answer was worse than "not easily".

**`/projects/{slug}/requests` had no route at all.** The only request page was the
DETAIL page at `/requests/{id}`. So a reader had no way to discover a request — every id
was known only to whoever created it, and the request board showed nothing. That is the
write-only-API defect one layer up: the feature exists, is tested, and is invisible.

**Neither page had a tab.** Both were reachable only by scrolling the overview far
enough to find a section that mentioned them. The tab bar had seven entries; it now has
nine, and `TestLayoutRendersEachPieceOnce` counts nine — that gate is a *measured number*
gate, so it correctly failed when the number moved rather than silently passing.

### The tab bar's active state is derived, so detail pages depend on it

`Sub` is `parts[2]` of the request path, which is why `/lists/2` still highlights
"Lists". Had it been a full-path match, every detail page would have lit **no** tab —
which presents as a styling bug and is actually a lost position in the section. Asserted
in both suites.

### `requests.html` had to become `requests-detail.html`

The board needed a filename and the detail page had the only sensible one. Both were
`requests.html`, so **whichever was written last won and the other 500'd** with a clear
log and a blank page. The registered-template list is the only thing that distinguishes
them at render time.

### A Go sentinel the JSON does not carry

`store.Request.ProjectID` is `json:"project_id,omitempty"`, so an instance-wide request
has **no key at all** rather than `0`. A page testing `r.project_id === 0` would never
see one and would label a shared question as this project's own — the `lists.title` /
`name` mistake one layer down. The board tests `undefined || null || 0`, and the gate
was verified RED by replacing it with the `=== 0` version alone.

### Three gates, each verified to fail

Deleting the Lists tab, removing the instance-wide fetch, and reducing the sentinel to
`=== 0` each turn the suite red. **A gate that has never been seen red is a claim.**

Two of the e2e assertions I wrote were also wrong while the code was right: a card count
of 1 against a seed of 3, and a fixed-width window for finding `is-active` that failed
for both sections while both tabs were correct. The second is the general lesson —
**searching backwards from a label with a character budget is a guess; find the element
by an attribute and inspect its own tag.**

882 Go tests. `make verify` and `make e2e` green. (historical)

---

## 9. The board could not be moved on — and a token typo in four files

**2026-10-05.** The fourth time *"for every endpoint the API offers, name the page that
calls it"* has paid. `handleMoveCard`, `MoveCard` and `PUT .../board/cards/{id}/move`
existed, were guarded, and were tested at the store layer. `board.js` fetched the board
and rendered it. **There was no way to move a card.** A kanban board nobody can move a
card on is a read-only list wearing a board's layout, and §36's "implementation is a
kanban board with WIP limits, not an infinite backlog" was a list.

Three ways to move, because drag alone is not an interface: drag, a select per card, and
arrow keys on a focused card. All three post the same request. A card is a `<button>`,
because a draggable div is not focusable and announces nothing — the arrow-key handlers
would have had no element to attach to.

### THE BUG THAT MATTERED: `concord_token` vs `concord.token`

The session key is `concord.token` (session.js, auth.js, consensus.js, finder.js, rank.js,
scout_e2e.py). **Four scripts I wrote this session read `concord_token`, with an
underscore.**

The failure mode is the worst kind: `getItem` returns `null` instead of throwing, the
`Authorization` header is simply absent, and every write **silently 401s**. The page
renders, the control is there, and nothing happens. It presents as a permissions bug and
is a typo. Four files, found only because the board's e2e watched the response status —
a move that is refused and a move that is never sent look identical in the DOM.

`TestEveryWriteSendsTheTokenTheServerReads` now greps `assets/js` for the wrong key
(cross-file, because a file cannot see its siblings), and it has a positive half so it
cannot be satisfied by deleting every authenticated write.

### Two ids, and the route wanted the other one

`/cards/{card_id}/move` moves a `board_cards` row by its own id; `feature_id` is a
*query* parameter for placing a feature with no row. The first version put the **feature**
id in the path, so every move 404'd and the board ignored the drag. The URL looks
unambiguous and is not.

### Piping a syntax check hides its exit status

`node --check board.js && echo JS_OK` printed **JS_OK for a file that did not parse** —
one stray `+` from an edit made an unterminated string, and `node --check` exit 1 was
consumed by the `&&`. I read that as a pass twice. The Go test (`node --check` via
`exec.Command`, exit code inspected) caught it immediately and had been catching it the
whole time.

**Never pipe or `&&`-chain a syntax check. Check the exit code.** This is the same
class as the earlier `git commit … | head` and `pytest … | tail` mistakes in this
session: the pipe's status is `tail`'s, and it is always 0.

Also: `expect()` rejects a bare string (`ValueError: Unsupported type: <class 'str'>`) —
it is for auto-retrying locator assertions. And Playwright's `has=` locator resolves
against the whole document, not the candidate's subtree, so
`.kanban-column has=<a card>` matched nothing on a correct board. **Read an element's
position from that element** (`data-column`), never by composing locators.

### What the board now refuses, visibly

A column at its WIP limit is marked `is-at-wip` — the header already rendered
"n / WIP k", but the reader found out by being refused. And a refusal shows **the store's
message**, because "that phase has a gate" and "WIP limit reached" are things a reader
can act on and a generic string discards. The board is **re-read** after a move rather
than reordered locally, since the move response is `{"status":"moved"}` and says nothing
about whether a limit or a gate refused it.

### The e2e needs a signed-in page, and a resettable board

`handleMoveCard` requires an actor, so the suite cannot be anonymous — the first version
moved cards signed-out, got a 401, and failed at the final DOM assertion rather than at
the request. And one session means one database, so `_reset()` puts the board back before
each test: the suite is *about* moving cards, and a fixture that cannot be restored makes
every test after the first order-dependent.

882 Go tests. `make verify` and `make e2e` green. (historical)

---

## 10. WIP limits were displayed and never enforced

**2026-10-05.** The board e2e seeded a column at its WIP limit, dragged a third card
into it, and the move **succeeded**. That was not a bug in the new UI.

`wip_limit` was written by `SetBoardColumn`, read by `GetBoard`, and printed by the board
page as `n / WIP k` — and **nothing consulted it on a write**. `grep -rn wip_limit
internal/ --include=*.go` returned exactly three hits: the insert, the select, and an
unrelated comment in `scout/decompose.go`.

So the column header said a limit was in force while the server accepted any number of
cards into it. **A limit that is only displayed is worse than no limit**: it teaches
readers that the board is policed, and then lets the board grow.

Now enforced in `checkWIPLimit`, with three cases that are the reason it is a function
and not a comparison:

| case | rule | why it is separate |
|---|---|---|
| `wip_limit IS NULL` | unlimited | NULL scanned as 0 would mean "take none" |
| card already in this column | allowed | a re-drop does not change the count, and drag-and-drop does it constantly — without this a successful retry reports the column as full |
| moving in, at or over the limit | refused | with the column name, the current count and the permitted count, because "WIP limit reached" is not something a reader can act on |

`ErrConflict`, so the HTTP layer answers **409** rather than blaming the request with a
400. The state refuses it; the request is well formed.

Added to **both** move paths, and the mutation run is what proved that matters: removing
the check from `moveRow` left the suite **green**, because every test seeded and moved
through `placeFeature`. So `TestTheWipLimitIsEnforcedOnTheCardIdPathToo` moves a card
that already has a row — which is what a drag on a placed card always does. Five mutants,
five killed.

Two of my own fixtures were wrong while the code was right, and both failed *before*
reaching the page: the helper looked up a card id **before creating any card**
("no rows in result set"), and the spare-card test inserted a feature titled `'four'` and
then looked it up by title, on a table where `features.title` is not unique. **Name a row
you just created by the handle the INSERT returned, never by a title.**

882 Go tests. `make verify` and `make e2e` green. (historical)

---

## 11. What is left, 2026-10-05 (measured, not asserted)

**SUPERSEDED 2026-10-15 by §12.** Kept as the record. §11.4's claim that "no other
endpoint has a handler and no reader" was **wrong**, and §12 says how it was wrong and
what found it; §11.1 and §11.2 remain accurate as history.

§1 and §4 above are the state at the start. Everything they listed is built. This section
is the current answer, and every line was checked against the code rather than against a
tracker.

### 11.1 Built this session

| page | endpoints it consumed | note |
|---|---|---|
| feature, complaint | `features/{id}/complaints`, `complaints/{id}/features` | two working endpoints with no reader |
| requests board + detail | `requests`, `requests/{id}`, `/answers`, `/tags` | the index route did not exist at all |
| lists + detail | `lists`, `lists/{id}`, `lists/{id}/entries` | §16 said "ranked"; nothing wrote `list_entry_votes` |
| board | `board`, `cards/{id}/move` | `handleMoveCard` existed; the board could not be moved on |
| **settings** | **`charter`, `members`, `criteria-profiles`** | three endpoints with handlers, guards and store tests, and **no reader in assets/js at all** |

### 11.2 The last page, and the one shape nobody had hit before

The settings page is the first consumer of `GET /charter`, `GET /members` and
`GET /criteria-profiles`. All three had been fully implemented the whole time.

**`governance.Charter` carries no `json` tags**, so `encoding/json` marshals the Go
**field names** and the endpoint answers **PascalCase** — `QuorumRatio`, `ConsentRatio`,
`WIPInProgress` — while every other endpoint in this API is snake_case. A page written
from the house style reads `quorum_ratio`, gets `undefined` for all 22 fields, and renders
22 empty inputs that submit an empty charter. It looks like a project with no charter
rather than like a bug.

So the page is written against a **measured** response, and
`TestTheCharterIsServedInPascalCaseNotSnakeCase` pins both directions: the keys that must
be there, and the snake_case spellings that must **not** appear. The e2e asserts no input
renders empty, which is the assertion that catches the wrong-key page — verified by
mutation (exit 1 on a snake_case page, exit 0 restored).

The form is also built from the response's own key list rather than 22 hand-written
inputs, so a field added to `governance.Charter` appears without touching the JS and one
removed leaves no orphan input. Anything the grouping does not mention lands in an "Other"
group, because **a charter field that is invisible on the settings page is the §3.1 defect
one level up**.

### 11.3 Two endpoints answered `null` where every other one answers `[]`

Measured on an empty project, by asking the running server:

| endpoint | before | after |
|---|---|---|
| `GET /admin-ledger` | `null` | `[]` |
| `GET /taxonomy/proposals` | `null` | `[]` |
| `GET /projects/{p}/complaints` | `null` | **`null`, deliberately** |
| `GET /projects/{p}/features` | `null` | **`null`, deliberately** |

`Array.isArray(null)` is false and `null.length` throws. Nothing was visibly broken
because **every** JS consumer happens to guard with `x || []` or `x && x.length` — so
this survived by accident of the client code, not by the contract. A caller written
without that habit throws on an empty project, and nothing tells them.

`/complaints` and `/features` are left alone on purpose: their pages guard, and changing
a shipped API shape mid-session is riskier than the wart. They are named in the test's
exemption, so fixing them later means updating a list rather than relaxing a check.

#### The gate took four attempts, and the failures are the lesson

1. **Source inspection** — "does the handler guard the slice it writes?" Reported **six
   false positives**: `audit`, `criteria-profiles`, `documents`, `strategic-weights`,
   `tags`, `strategic-themes` all answer `[]`. It cannot tell `handleGetFeature` (an
   object) from `handleListFeatures` (a slice) without resolving types.
2. **A backreference** — `(\w+) == nil \{\s*\1` **does not compile under RE2** and
   panics at init, taking every test in the package with it. Same class as reading
   `concord_token` when the key is `concord.token`: a pattern written from memory about
   what is usually there, not from what the engine supports.
3. **`!= nil` as an acceptable guard** — `if !ok` from `requireProjectID` **matches
   `!= nil` as a substring**, so the allowance exempted exactly the handlers it was meant
   to catch. Removing a real guard from `handleListMembers` left the suite green. A guard
   allowance phrased as a substring test has to be about the **variable**, not about the
   characters in the function.
4. **`[]\w+` for the type** — `[]store.Project{}` and `[]ranking.Criterion{}` are
   dotted, so a bare `\w+` reported **eleven correctly-guarded handlers** as unguarded.

What survives is a **measurement**: walk every registered GET route with `chi.Walk`,
request each against an empty project, and fail on a top-level `null`. The route table is
the authority, so a new endpoint is covered the day it is registered.

**What it cannot catch, stated rather than discovered later:** removing the guard from
`handleListMembers` stays green, because `ListMembers` ends in
`make([]Member, 0, len(collected))` — non-nil by construction. The gate catches a
nil-returning **store**, which is the case that actually ships `null`.

And the suite's own helper could not have caught either: `doJSON` unmarshals into
`map[string]any`, where `null` becomes a nil map and `[]` fails to unmarshal at all. The
distinction requires reading the raw body.

### 11.4 Nothing else is outstanding — THIS CLAIM WAS FALSE

**Corrected 2026-10-15.** Verified against the running server **by hand**, which is why it
was wrong twice over: `GET /tags/completeness` (§4.3's completeness meter) and the whole
`/strategy/*` surface (§6.2) had no reader, and §6.2's **write path had no route at all**
— `store.ProposeStrategicWeight` existed with tests and nothing in `httpapi` called it.
Neither survives a measurement of the route table; both survive a reading of it. See §12.

Verified against the running server: **no other endpoint has a handler and no reader.**
`taxonomy/proposals`, `admin-ledger`, `members` and `criteria-profiles` were the four, and
the last three are now read by the settings page. `admin-ledger` is deliberately still
unread — it is an operator log, not a project page, and the gate that would expose it is
an admin surface nobody has specced.

Three `wait_for_timeout` calls remain in e2e, all marked `BOUNDED (justified)` by
`check_e2e_sync.py`. Known-open by design, not by neglect.

---

## 12. What is left, 2026-10-15 — THIS IS THE LIVE TRACKER

§11 and everything above it are history. §1, §4 and §11 are marked superseded; this
section is the current answer, and like §11 every line was checked against the code.

**Measured state:** schema 26 · 26 migrations · 81 live tables · **1004 Go tests** across
9 test-bearing packages · **277 e2e tests in 19 suite files** · **16/16 UI mutants** killed ·
`make verify` exit 0, `make e2e` exit 0.

### 12.1 How the remaining work was found (it was not in any tracker)

> **SUPERSEDED by §17.1.** The hand-built version below was a heuristic -- search the
> client bundle for each route's contiguous static tail -- and it recorded its own
> false-positive rate. §17.1 replaces it with a gate that walks the router and checks a
> declared inventory in both directions. Kept because it is the measurement that found
> §12.2's and §12.3's work, and because the difference between the two is the point.

§11.4 claimed "no other endpoint has a handler and no reader". It was verified **by hand**
and it was wrong. The replacement is a measurement: walk the registered route table with
`chi.Walk` (174 routes, 95 GET), then look for each route's **contiguous static segments**
in `assets/js`, `assets/css` and `templates/`. Forty routes have no static tail worth
searching (a bare `/{id}`), and thirteen have a tail that appears in no client file:

| route | verdict |
|---|---|
| `GET /projects/{slug}/tags/completeness` | **§4.3's completeness meter. Built (§12.2).** |
| `GET /projects/{id}/strategy/weight-proposals`, `/strategy/themes` | **§6.2. Built (§12.3).** |
| `POST /projects/{id}/strategy/weight-proposals` | **did not exist at all — see §12.3** |
| `/atom.xml`, `/feed.xml`, `/rss.xml` | feeds; linked from `base.html`'s `<link rel="alternate">`, correctly not fetched by JS |
| `/healthz`, `/api/v1/version` | machine/operator surfaces, deliberately unread |
| `/admin-ledger` | operator log, no specced admin UI (§11.4) |
| `/`, `/projects/{slug}/documents/search` | a page (no inbound link needed) and a fetch built from interpolated segments |

**A gate for this class is NOT built, deliberately.** The sweep has a high false-positive
rate in the other direction — `'/api/v1/projects/' + slug + '/documents'` never appears as
a contiguous string, so the heuristic finds readers it should not trust either. A gate with
an allowlist long enough to be quiet is a gate people route around, which is the lesson
§11.4's own section learned. What is in the tree instead is the per-page **read-side gate
by NAME** (`TestEveryEndpointThisPageReadsIsActuallyCalledFromIt` and §12.4's two
additions), which cannot be quiet and cannot be satisfied by its own documentation.

### 12.2 §4.3's completeness meter (built)

`GET /tags/completeness` has answered "how well-tagged is this project" since the taxonomy
work, and the handler's comment **names it as the §4.3 meter** — which is the trap: a
route comment describing the feature it serves reads like the feature being present. The
meter is now on the settings page.

The score is `0.6 * min(tags/want_tags, 1) + 0.4 * min(namespaced/want_namespaced, 1)`,
and the page renders **the components, not just the percentage**, because a bare 56% is a
number a reader cannot act on. The threshold (`want_tags`, `want_namespaced`) is read from
the response rather than restated in JS, and the test asserts `c.wantTags` is absent — a
page that hardcoded 5 and 2 would keep claiming them after the store changed.

Measured against a seeded fixture: 3 tags, 1 namespaced, 1 unapplied-then-applied
suggestion → 56%, "3 of 5 tags, 1 of 2 namespaced", "Add 2 more tags and 1 more namespaced
tag", "1 suggested tag is still waiting to be applied". The score is compared with a
tolerance: the endpoint sends a **float64 sum** (`0.6*3/5 + 0.4*1/2`), so the string in the
attribute is whatever binary arithmetic produced — `0.5600000000000001` is the same claim.
Asserting the literal `"0.56"` was the first version of that test and it was red.

### 12.3 §6.2's write path did not exist (built)

§6.2: *"changed only by consensus (Normal tier) … Nobody can pin an item to the top by
hand. Steering happens through public inputs: pain, pairwise votes, and ratified themes."*

`store.ProposeStrategicWeight` existed, with store tests. The list route, the consent route
and the themes route were registered. **There was no route to open a proposal.** So the
mechanism could list proposals and consent to them and had no way to `propose`, every read
worked, the store correctly refused out-of-range weights, the whole suite was green, and
the feature was inert. This is §3.1's shape at the API level: a *writer* with no route is a
write that cannot happen.

Fixed: `POST /api/v1/projects/{project_id}/strategy/weight-proposals`, contributor-gated
with a required rationale (the store's rule), 201 with a message that says the change is
**proposed rather than applied**, and the `Go` test asserts exactly that — the feature's
`strategic_weight` is still 1.0 after proposing 2.0. A 201 that had already moved the dial
would be the hand-steering the mechanism exists to prevent. Also asserted: anonymous → 401;
self-consent → **403 naming the rule** (not 202 — the first version of this test guessed
202 and was corrected by the measurement); a second pending proposal → **409**, from the
partial unique index; weight 99 → 400 naming the range.

**THE PATH SEGMENT NAMED `{project_id}` CARRIES A SLUG.** `requireProjectID` resolves it
with `GetProject`, which is a slug lookup, so a numeric id answers `404 not found` —
indistinguishable from a missing project, and it cost a debug cycle to find. Every
project-scoped route behaves this way, including the `/charter` the settings page already
read. Written down here because the route pattern reads like an integer.

The settings page now shows the pending proposals with a **Consent** button and the live
themes, and it distinguishes the two outcomes the handshake has: **202** (consent recorded,
quorum not reached — the server's own reason, including the consent count) and **200**
(ratified). Collapsing those would report a weight change that has not happened.

### 12.4 What the gates for §12.2/§12.3 are, and that they can fail

Four mutants, each killing exactly its own gate (run against a rebuilt binary, because the
assets are **embedded in the Go binary** — an un-rebuilt `./bin/concord` serves the previous
`settings.js`, which is how three of these tests first failed for a reason that had nothing
to do with the code under test):

| mutant | red |
|---|---|
| the `POST /weight-proposals` route removed | `TestTheStrategicWeightWritePathIsReachable` |
| `settings.js` stops fetching `/tags/completeness` | the read-side gate + the meter e2e |
| `settings.js` treats 202 as ratified | `test_a_consent_below_quorum_…` |
| the meter hardcodes `want = 5` | `TestTheCompletenessMeterReadsTheKeysTheStoreReturns` |

**One of these tests was decoration when written.** `test_a_consent_below_quorum_…`
asserted that the word "ratified" was absent from the live region — but the below-quorum
message does not contain that word *either way*, so deleting the entire 202 branch left it
green. It was found by mutating the branch, not by reading the test, and it now asserts
what has to be true: the page reports the **server's** reason (the consent count) and does
not claim "quorum is reached".

Two harness lessons, both from this session:

* **A fixture that seeds from the first test makes the suite order-dependent.** The
  below-quorum test relied on the meter test having seeded the proposal, so
  `pytest -k below_quorum` found no Consent button and failed with a 30-second **click
  timeout** — a red that says the code is broken when the fixture is. The seeder is now
  idempotent and each test that needs it calls it.
* **A 403 is not always a 403.** The Go route test asserts the self-consent refusal from a
  *signed-in* actor; the browser test asserts the *anonymous* refusal, which is a 401
  `authentication required`. Both messages name their cause, and both are asserted as
  measured rather than as guessed.

### 12.5 §3's documents page can search its own corpus (built)

`GET /projects/{slug}/documents/search` is a real FTS5 index — bm25-ranked, with the
hyphen fix that stops `language-neutral` searching for `language AND NOT neutral` — and it
had **no caller**: `documents.js` fetched the whole list and filtered it in the browser.
1,087 documents on the live instance were searchable with curl and not from the site.

The wire shape is a **bare JSON array**, and two sources disagreed about it: the
`documents_test.go` helper reads `hits["items"]` (its `doJSON` wraps a bare array under
`"items"`, so the helper was hiding the wire from the tests), and the plan for this page
guessed `{results: […]}`. Neither is what arrives; a test now asserts the raw body.

What only a browser can assert, and does: a search **crosses kinds** (it runs from the
readme kind, a hit in the spec kind appears, and clicking it opens that kind — the one
thing a client-side filter cannot do, and the reason the endpoint is worth having); `?q=`
is a **shareable address** (asserted by reloading, the only way to tell a URL that is
written from a URL that is only displayed); and a **malformed FTS5 expression** (an
unbalanced quote → 400) shows the server's message and leaves the previous results on
screen, because "your query could not be parsed" and "nothing matches" are different
statements and an empty list asserts the other one.

### 12.6 The actual remaining list

1. **`concord.polarisocial.xyz` runs a 36-commits-behind build** and this repo cannot
   change that. Unchanged from §0; it is the one item that is not ours to do.
2. **`admin-ledger` has no UI**, deliberately: an operator log, and the admin surface it
   would need is not specced. §11.4's reasoning stands.
3. **A propose-a-weight **form** is not on any page.** The API path is now reachable
   (§12.3) and the proposals/themes/consent are visible on settings; *opening* a proposal
   is API-only, and the natural home for the control is the feature detail page (a weight
   belongs to a feature). Not built here because the route — the part that made the feature
   inert — was the defect; the form is a control, and §6.2 does not require one.
4. **5 `wait_for_timeout` calls remain in e2e** — measured by `check_e2e_sync.py` as 3
   bounded-and-justified, 2 idle, **0 racy**; the gate passes. (§11's "22" was the count
   at the time and is no longer the number.) Known-open by design, not by neglect.
5. **The §6 out-of-scope items in each revision of the spec.** Unchanged.
6. **No "every route has a reader" gate.** Reasoning in §12.1; the per-page named gates
   are the coverage, and any *new* page has to bring its own.

---

## 13. 2026-10-15: the administration surface, and three defects it exposed

Where §12's list stands after this pass, and what this pass was.

### 13.1 Three mutating routes had no guard, and one of them could never have worked

Found by walking the ROUTE TABLE (`chi.Walk`, the measurement §12.4's class gate
now uses) and reading each mutating handler for a guard call. Reading the
handlers is how these were missed: `PUT /tags` and `PUT /visibility` sit in the
same route group as the two that were unguarded, and a reader skimming a group
stops noticing.

| route | before | now |
|---|---|---|
| `PUT /projects/{slug}/languages` | **no guard at all**: an anonymous PUT answered 200 and `project_languages` gained the row (verified on the running instance) | `requireWriteActor` + role `contributor` + readable |
| `PUT /projects/{slug}/metrics` | **no guard at all**: an anonymous PUT answered 200 and `project_metrics` went to `stars=999999` | role `maintainer` (stars/forks/commits are inbound forge facts, not an opinion) |
| `POST /projects/{p}/complaints/{id}/merge/{target}` | no guard, and the handler read `chi.URLParam(r, "source_id")` while the router registers `{id}`: the source id was ALWAYS 0, so every merge answered 404 for its whole life. Nothing called it, so nothing noticed | `requireWriteActor` + `requireProjectID` + `maintainer`, and the complaint's own project is checked against the path's |

The merge had two further defects, both invisible while it answered 404 for
everyone: the store loaded source and target and compared **nothing**, so a
maintainer could fold one project's complaint into another project's complaint
(and `INSERT OR IGNORE` the source's impacts against a foreign row); and its
audit row recorded actor 0 — nobody — for the one action that most needs
attribution.

Gates: four behavioural tests written failing first, each seen red under a
compile-clean mutation; and `TestEveryMutatingRouteIsGuarded` walks the route
table and allows only nine public routes, each with its reason. Its limits are
stated in the file, including the measured one — deleting `requireWriteActor`
from the merge leaves it GREEN, because `requireRole` returns `ErrAuth` for
actor 0. That mutation removes a redundant line rather than creating a hole, and
it is why a `SURVIVED` line is a statement about the mutant first.

### 13.2 Six endpoints had no UI at all, and the meter told readers to use one

`PUT /visibility`, `GET`+`PUT /tags`, `PUT /languages`, `POST`+`GET /invites`
and `POST /invites/{id}/revoke` had handlers, guards and store tests, and
`docs/specs/frontend-spec.md` listed them as "none". They are also the only way
to change what a project looks like to the rest of the instance, so a project
could not be made private, tagged, or invited to from the site.

The sharpest instance was already in the tree: §4.3's completeness meter printed
**"Add 2 more tags and 1 more namespaced tag"** with no control anywhere on the
site that applied a tag. A meter that instructs and cannot be obeyed is worse
than no meter, and there is now a gate for that pair (`TestTheMeterAndTheTagsControlShipTogether`).

Built on the settings page, four panels: **Visibility** (the four levels the
store accepts, each with what it means), **Tags** (the applied tags, and a form;
the route attaches tags that EXIST rather than minting them, because a new tag is
a proposal with a quorum), **Languages** (§15's collaboratively correctable
percentages, seeded from what is stored), **Invites** (list, mint, revoke — the
token is reported in the live region, which is *outside* the re-rendered panel).

`GET /projects/{slug}/languages` did not exist and was added with the panel: the
read half of a surface whose only reader was the finder, querying the table
directly. Without it a project's own page could not show what a `PUT` stored —
the same write-blind shape `PUT /tags` had before `GET /tags` was added.

### 13.3 Two defects the panels exposed, both in the page itself

1. **Every read on the page was anonymous.** The read helper sent only `Accept`
   — the token was attached to writes and not to reads. Invisible while a project
   is public (all nine panels load), and fatal for the projects whose settings
   most need changing. Fixed; the gate is behavioural and mutation-verified:
   `GET /invites` is member-only, so the invite panel rendering a minted row can
   only happen if the reads carry the token.
2. **A stale read after a write.** `PUT /visibility` answered 200, SQLite held
   `protected`, and the panel still rendered `public`. The cause was the
   *document reload* the page used to do after a write — a reload of a page whose
   project had just stopped being public — and what fixed it was re-reading the
   panels in place (`load()`) instead. This entry's first version blamed the
   browser's HTTP cache; that was a plausible story the experiment did not
   support, and it is corrected here rather than quietly dropped. Both halves of
   the `no-store` change were kept as API hygiene, and labelled hygiene: removing
   them does **not** reproduce the stale read (verified by mutation, both sides).

### 13.4 OPEN: a browser navigation carries no token (needs a spec decision)

`handleSettingsPage` — and every page behind `projectReadable` — needs the
request to carry the actor, and a browser **navigation** cannot: the session
lives in `localStorage` and is attached by script, which has not run yet. A
member opening a protected or private project in a browser gets the 404 page.
Measured, not reasoned: with the project set to `protected`, `#settings-root`
never renders and the wait times out.

`auth.go`'s `bearerToken` already accepts an `X-Auth-Token` header "for browser
clients that cannot set headers on a navigation", so the design has a seat for
the mechanism and nothing fills it. The options (an HttpOnly session cookie set
at login, or a service worker) change the auth model and carry CSRF
consequences, so this is raised as a spec question in **`docs/QUESTIONS.md`
Q1** rather than settled in a page's commit. No test asserts the unreachable
behaviour; the reasoning is recorded where the test would have been.

### 13.5 The remaining list, restated

1. **`concord.polarisocial.xyz` runs a 36-commits-behind build.** Unchanged;
   not ours to do.
2. **Q1: the browser session** (§13.4). The one item that blocks a feature a
   reader can see: a member cannot open their own project's settings page once
   it is not public.
3. **`admin-ledger` has no UI**, deliberately (§11.4).
4. **A propose-a-weight form is not on any page** (§12.6.3). Unchanged.
5. **`metrics` has no reader**, deliberately (§13.2): a forge projection.
6. **`merge requests`, `criteria` and `composite rank` have no reader.** Measured
   by the inventory in `docs/specs/frontend-spec.md`; each needs its own spec
   section before a page, since none of the three is described in detail in r4.
7. **No "every route has a reader" gate** (§12.1). The inventory table is now
   measured rather than remembered, which is the closest thing to it that does
   not require the spec to name a page for every route.
8. **The §6 out-of-scope items in each revision of the spec.** Unchanged.

---

## 14. 2026-10-15 (second pass): the navigation session, and §13.4 closed

§13.4 was the one open item from the previous pass that blocked a feature a reader
can see: a member could not open their own project's settings page once the project
was not public. It is fixed, and the fix is asserted from both sides.

### 14.1 What the gap was, restated as the measurement

`handleSettingsPage` resolves the project and calls `projectReadable`, which for a
protected or private project needs the request to carry the actor. A browser
**navigation** could not: the session is a bearer token in `localStorage`, attached
by script, and the page's own HTML arrives before any script runs. Measured: with
the project `protected`, `#settings-root` never rendered and the browser test's
wait timed out — which is why the previous pass wrote a finding instead of a test.

### 14.2 The fix, and the property that makes it safe

`concord_nav`: set by `POST /api/v1/auth/login` and `/register`, `HttpOnly`,
`SameSite=Lax`, `Path=/`, **session** (no `Max-Age`), `Secure` when the request
arrived over TLS or `X-Forwarded-Proto: https`. `authenticate` reads it only when
`navToken` returns something, and `navToken` returns something only for
`GET` + a path that is **not** under `/api/`. Cleared at logout, beside the token
revocation, so a logout cannot leave pages rendering for a credential that no
longer resolves.

The condition is the security property, which is why it is one expression in one
place. **Every mutating route in this server is an API route** — checked, not
assumed; a `r.Post(` outside `/api/` is the thing that would break the argument —
so the cookie cannot authorize a write, and no API read sees it either. The
bearer header stays the only API credential, and `X-Auth-Token` (the seat
`auth.go` had reserved for this since it was written) stays empty.

### 14.3 The gates, and which half is mutation-verified

`nav_session_test.go`, four tests, both directions:

| assertion | mutation that turns it red |
|---|---|
| the cookie's attributes (HttpOnly, Lax, `/`, session) | login stops setting it |
| **a member opens a protected project's page with the cookie alone** | `authenticate` stops consulting the cookie |
| **the same cookie authorizes no API read and no write** | the method-and-path condition is removed — i.e. the CSRF hole is injected and the test catches it |
| a revoked token's cookie stops working, logout expires it | login stops setting it |

Plus two browser tests, one of which was impossible to write before this pass:
`test_a_member_can_open_a_protected_projects_settings_page` (all nine panels load
for the member through a real navigation) and
`test_a_stranger_gets_no_settings_page_for_a_protected_project` (the shell still
does not render for a stranger — the existence of the project does not leak).

### 14.5 The pages' own reads went out anonymous (26 call sites) — CLOSED

The navigation cookie makes the page's **HTML** arrive with an actor. The page's
**data** did not: most page scripts fetched `/api/v1/...` with no `Authorization`
header, invisible on a public project and fatal on a protected one — the member got
the shell (cookie) and then a page whose panels 404ed (no token in the read).
`settings.js` was the only file sending the token on reads; `project.js` sent it on
writes and not on its ten reads. So §14.2's fix was half a fix until this landed.

Measured with this rule — a `fetch(` whose own argument list names neither
`Authorization` nor `headers` — because the count is the thing to re-check, not the
prose:

**Result: 10 files, 26 call sites** — `project.js` 10, `audit.js` 3, `board.js` 3,
`documents.js`, `index.js`, `ranking.js`, `requests-index.js` 2 each, `lists.js`,
`projects.js`, `search.js` 1 each. Two further sites were found by the stronger
rule the test below uses: `documents.js`'s document search and `scout.js`'s scout
request passed `{ headers: { Accept: ... } }` — a `headers` object with no token,
which the first rule counts as covered. So the measured floor was 26 and the real
number 28.

The fix: `apiAuth(extra)` in each file, two lines over `session.js`'s existing
`ConcordSession.instance().authHeaders()`, and it takes the extras so the five
hand-built copies of *"var headers = {Content-Type}; if (token)
headers.Authorization = ..."* could go. Those five are why this was worth fixing as
a class rather than 26 times: five copies of a rule is five chances to leave the
token out, and two had (`documents.js`, `scout.js`). For a signed-out visitor
`apiAuth()` returns `{}` and the request is byte-for-byte what it was.

**The gate is the interesting part**: `TestEveryAPIFetchPresentsAToken` walks
`assets/js/*.js`, extracts every `fetch(` call site by matching parentheses, and
requires each one whose URL names an `/api/v1` path to present a token carrier *in
its own arguments*. Three things it does that a grep cannot:

  - it asserts it examined a non-zero number of call sites, so a broken scanner
    fails instead of passing while guarding nothing;
  - the one legitimate anonymous site (auth.js's login/register submit, which is
    the request that mints the token) is **enumerated by file and count**, and a
    second anonymous call site in that file fails the test. That assertion is
    mutation-verified: inserting a second one turns it red;
  - it forced five *correct but unverifiable* call sites to be rewritten. A hoisted
    `var headers = apiAuth(...)` followed by `headers: headers` sends the token and
    the test cannot see it, so every such site was inlined. The rule reads source,
    so the helper has to be at the call site.

Restoring one read to a bare `fetch(url)` turns it red (verified). A fetch whose
URL arrives in a variable is not examined at all: this is a floor, not a census.

**The browser gate is the one that would have caught the whole class**:
`test_the_owner_sees_a_private_projects_panels` — the owner of the `panels-private`
fixture loads the page in Chromium and the private claim must be **in the DOM**.
Two independent credentials are needed and the test needs both: the `concord_nav`
cookie, because a navigation cannot carry a token, and the token in `localStorage`,
because the page's fetches are separate requests. Reverting any read to a bare fetch
turns it red, and so does making `authenticate` ignore the cookie (both verified).

Two facts about the server that cost a debugging round each and are now written
down where they are used: `api_tokens.token_hash` is **base64 RawStd** of the
sha256 digest, not hex (and `0001_init.sql`'s comment saying "sha256 hex" is
wrong), and `ResolveToken` rejects any token that does not start with `clt_`
before it looks it up. Both mistakes produce the same 401 on a page that should
have rendered, which is a shapes-not-symptoms wait.

### 14.6 What is left, restated once more

~~The browser session~~ (§14.2) and ~~the pages' anonymous reads~~ (§14.5) are both
closed. The list is now:

1. **`concord.polarisocial.xyz` runs a 36-commits-behind build.** Unchanged; not
   ours.
2. **`merge requests`, `criteria` and `composite rank` have no reader.** The
   largest remaining body of work, and the next item: three resource groups with
   handlers, guards and store tests and no page anywhere. `criteria` first — it is
   the input to the consensus pillar (§6.4's comparison table, which is the reason
   people can trust a ranking) and its `vote`/`set-active` verbs are the ones a page
   would have to *explain*, not just call.
3. **A propose-a-weight form is not on any page** (§12.6.3). The route exists and
   is guarded; the control is missing.
4. **`admin-ledger` has no UI**, deliberately (§11.4).
5. **`metrics` has no reader**, deliberately (§13.2).
6. **No "every route has a reader" gate** (§12.1). `TestEveryAPIFetchPresentsAToken`
   (§14.5) is the same shape pointed at the read path, and the measured inventory in
   `docs/specs/frontend-spec.md` is still the closest thing for routes.
7. **`check_doc_numbers.py` cannot see a QUALIFIED test count.** Its pattern is
   `(\d{3,4})\s+tests\b`, so a bare count is gated and the same number followed by
   a qualifier — `902 Go tests` — is invisible
   — which is how this file's own headline line sat at **902** for several passes
   while the gate printed OK, and why the row above it had to be found by hand.
   Widening the pattern is the obvious fix and is not the whole fix: this file also
   *records* older counts in prose ("882 Go tests" in §11, §12 and §13), and the
   only exemption the gate has is a markdown table row, so a wider pattern would
   flag the history it is meant to preserve. Doing it properly needs a
   section-level exemption for superseded records — measured before it is written,
   or it will exempt the wrong paragraphs (the same failure the "any line
   containing *was*" exemption already produced once).
7. **The §6 out-of-scope items in each revision of the spec.** Unchanged.

---

## 15. 2026-10-15 (third pass): the criteria page, and §6.4's comparison table

§14.6's first item. `criteria` is the largest of the three API surfaces that had
handlers, guards and store tests and **no reader anywhere**; it is now the one with
a page. Measured on the live database before this pass:

```
criteria 0 · criterion_ratings 0 · criterion_votes 0 · criteria_profiles 0   features 537
```

Zero criteria had ever been created, so §6.4's "criteria rankings are displayed as
a comparison table" had never been displayed. Spec: `docs/specs/criteria-panel-spec.md`,
written before the code.

### 15.1 What the page is

`/projects/{slug}/criteria`, tab "Criteria" (after Ranking). One POST to
`/rank/composite` produces the whole table: its `results` carry `rank`, `score`,
`confidence` and the `contributions` that produced them, so the page renders the
forgery's own arithmetic instead of re-deriving it. Around it: per-criterion weight
boxes, the two flags (`include_inactive`, `require_min_rd`), saved weightings
(load + save), the criteria list with create and retire, and a per-criterion
comparison form with the five outcomes the schema allows.

### 15.2 Three properties of `Composite` the page had to learn, and one bug

The first version of the page assumed a contribution exists for every (feature,
criterion) pair. It does not — `Composite` loops over `criterion_ratings` ROWS, so:

1. a feature nobody has compared under a criterion has **no cell value at all**;
2. a criterion with no rows contributes nothing and gets **no column** (that is why
   an empty pool is invisible rather than present and empty);
3. naming **any** criterion restricts the query to the named set, so the page must
   send every box or it would silently rank a subset.

The first two are why an unrated cell must read `unrated` in both of its forms: a
rating row with `games == 0` sits at the Glicko starting value, and its normalised
value is 0.5 **by construction** — the literal midpoint of an empty distribution.
Rendering that number would present an artifact as a measurement. The fixture seeds
a criterion in exactly that state so the browser suite can prove the page does not.

The bug the browser suite found, which no source check could: **the rank's own
re-render wiped the message the write had just set.** "Added UX" was shown and
cleared inside one tick, and the two updated ratings from a vote were written into a
row that the follow-up rank destroyed. Messages now ride through the rank
(`rank(msg)`), and the vote's outcome goes to the notice, which is outside the
re-rendered subtree. A write that reports nothing is indistinguishable from a write
that failed.

### 15.3 Gates, and what each mutation proved

| gate | mutation that turns it red |
|---|---|
| `TestTheCriteriaPageIsRegisteredAndMounted` | unregistering the template name |
| `TestCriteriaPageHidesAPrivateProjectFromAnonymousCallers` | dropping the `projectReadable` check |
| `TestTheCriteriaScriptKeepsItsRules` (16 rules) | removing `skip` from the outcome list; rendering `0.50` instead of `unrated` |
| `criteria_e2e.py` — cells | rendering `0.50` for an unrated cell |
| `criteria_e2e.py` — the leader moves with the weights | never sending the weight vector |
| `criteria_e2e.py` — the hostile name is text | dropping `esc()` from the criterion name |
| `criteria_e2e.py` — retired criteria join on request | never sending `include_inactive` |

All verified by running the mutation, not by reading the source.

### 15.4 What is left, restated

1. **`merge requests` and `composite rank` still have no reader** — the two
   remaining surfaces of the three. `composite rank` is now *nearly* reachable: the
   criteria page posts to it, but nothing renders a saved weighting as its own view
   and `GET /rank/composite` does not exist (it is a POST because a weight vector
   belongs in a body).
2. **A propose-a-weight form is still on no page** (§12.6.3): the route exists and
   is guarded; the control is missing.
3. **`admin-ledger` has no UI**, deliberately (§11.4).
4. **`metrics` has no reader**, deliberately (§13.2).
5. **No "every route has a reader" gate** (§12.1).
6. **`check_doc_numbers.py` still cannot see a QUALIFIED test count** (§14.6 item 7).
7. **The criteria page defers three things on purpose** (spec §6): pair suggestion,
   a per-criterion standings leaderboard, and deleting a criterion — retiring is the
   flag, and a delete needs a decision about the votes behind it.

---

## 16. 2026-10-15 (fourth pass): §5.7's merge gate, which nothing consulted

The largest defect found in this project so far, and it was not a missing feature.
It was a written, unit-tested rule that no production code path called, guarding the
one write that ships work.

### 16.1 What was there, and what it did

`governance.CheckMergeGate(approvals, eligible, reviewerApproval, charter)` implements
§5.7 exactly — `MergeQuorumThreshold` clamps `max(min_confirmations, ceil(ratio ×
eligible))` at the population, `QuorumOK` honours a charter that dropped the quorum,
`ReviewerOK` honours the flag. Four branches, all pinned by `governance_test.go`.

Its only caller in the whole tree was that test:

```
$ grep -rn 'CheckMergeGate' --include=*.go .
./internal/governance/governance.go:264:func CheckMergeGate(...)
./internal/governance/governance_test.go:140:	g := CheckMergeGate(1, 10, true, c)
```

`store.ExecuteMerge` required a non-zero actor id and nothing else. It set
`status='merged'`, moved the feature to `shipped`, **closed every complaint linked to
that feature**, and wrote an audit row. `ApproveMerge` accepted anyone. Neither read
the charter, whose four merge columns have existed since migration 0001 with defaults
`(requires_quorum 1, min 2, ratio 0.25, require_reviewer_approval 1)`.

So the default state of every project was: one person who can read it presses a
button and the change lands, in a charter that says two collaborators must confirm it
first.

### 16.2 Reproduced before it was fixed

`internal/httpapi/merge_gate_test.go` was written first. Six of its eight tests failed
against the pre-fix tree, and the failures ARE the defects:

```
--- FAIL: TestAMergeWithNoApprovalsIsRefused
    a merge with zero confirmations was executed: 200 map[status:merged]
--- FAIL: TestAMergeIsRefusedToSomebodyOutsideTheProject
    an account with no membership in the project executed a merge: map[status:merged]
--- FAIL: TestAnAuthorCannotConfirmTheirOwnMergeRequest
    the author confirmed their own merge request: map[status:approved]
--- FAIL: TestAMergeRequestCannotBeExecutedThroughAnotherProject
    a merge request of project merge-project was executed through project
    some-other-project: map[status:merged]
--- FAIL: TestTheMergeListReportsTheGateAndItsShortfall
    the list item carries no gate: map[author_id:2 feature_id:1 id:1 …]
--- FAIL: TestAReviewerApprovalIsNotJustAnyApproval
    two contributor confirmations and no reviewer passed a charter that requires
    reviewer approval (reviewer): map[status=merged]
```

The two that passed are the ones that must keep passing: the charter's dropped
quorum, and the happy path. A rule that refuses everything is as wrong as one that
refuses nothing, and only a test can tell them apart.

### 16.3 The gate, as it now runs

Counted confirmation = a `merge_approvals` row whose user is not the author and is an
eligible collaborator **now** (a lapsed collaborator's stops counting: eligibility is
role AND activity, and a quorum held open by people who left is what §8.2 exists to
prevent). Reviewer approval = a counted confirmation from someone holding ≥ reviewer.

- **approve**: member with role ≥ contributor, and not the author. NOT "an eligible
  collaborator" — that was this pass's own bug, caught by the existing route-(4) test:
  approving somebody else's request *is* a qualifying contribution, so requiring
  eligibility first would make that route unreachable.
- **execute**: role ≥ contributor, the request must belong to the project in the URL,
  and the gate must pass. The quorum lives in the store next to the state change, the
  way `CloseConsensusCall` owns the consensus quorum.
- **reject**: same standing; no quorum (refusing is the safe direction).
- The list carries each row's `gate` (`approvals`, `needed`, `eligible`, `quorum_ok`,
  `reviewer_ok`, `requires_quorum`, `passed`) so no client re-derives §5.7.
- A quorumless merge is legal and the audit detail says
  `merged without a quorum (the charter dropped it)` — §5.7 allows the exception only
  because every such merge is logged.

Second real bug found and fixed in the same path: `ExecuteMerge` took the project id
from the URL and used it **only** as the audit row's project, so a member of any
readable project could execute any merge request by id and have the record filed
under their own project. Now a mismatch is a 404, indistinguishable from a request
that does not exist.

A third, found by asking what happens TWICE rather than by reading: nothing enforced
`open -> merged|rejected`. A second execute re-ran the whole landing — re-shipping
the feature and re-closing complaints already closed — and a reject afterwards
flipped a landed change to `rejected`. Both are now conflicts, and
`TestAMergeRequestLandsOnceAndRefusesToLandAgain` fails with
`a second merge returned <nil>` when the guard is removed.

That question — "what happens if this runs twice?" — is what the whole pass was. The
gate itself was a rule nobody ran. This is the same question one step further in, and
the answer was on no list before it was asked.

### 16.4 The page

`/projects/{slug}/merge`, tab "Merge" (twelfth). The four endpoints have had a reader
missing for their whole life; the page renders each request's gate in the server's
own numbers, offers Confirm / Merge / Reject, reports refusals in the server's
sentence, and opens new requests. It does not compute the quorum — the source gate
forbids the string `0.25` in it, because a second implementation of §6.3 is the one
that gets it wrong.

### 16.5 Gates, and the mutation that turns each red

Every one verified by running the mutation; two of the ten produced a **compile**
failure on the first attempt, which proves the compiler works and nothing about the
gate, so both were rewritten to compile and assert.

| mutation | gate that fires |
|---|---|
| `ExecuteMerge` does not consult the gate | `TestAMergeWithNoApprovalsIsRefused` |
| the URL's project is not checked | `TestAMergeRequestCannotBeExecutedThroughAnotherProject` |
| an author may confirm their own request | `TestAnAuthorCannotConfirmTheirOwnMergeRequest` |
| approving needs no standing | `TestSomebodyOutsideTheProjectCannotConfirm` |
| every approval row counts | `TestAnAuthorsOwnApprovalIsNotAConfirmation` |
| any approval satisfies the reviewer requirement | `TestOnlyAReviewerSatisfiesTheReviewerRequirement` |
| executing needs no membership | `TestAMergeIsRefusedToSomebodyOutsideTheProject` |
| the list reports no gate | `TestTheMergeListReportsTheGateAndItsShortfall` |
| a quorumless merge is logged like any other | `TestTheCharterCanDropTheMergeQuorum` |
| the reported threshold is not the charter's | `TestTheQuorumIsFlooredAtTwoAndClampedAtThePopulation` |

Plus `tests/e2e/merge_e2e.py` (7 browser tests, fixtures seeded in sqlite): a row per
request with the gate it must meet; Merge on a short request refused with the
server's sentence; two accounts filling a real quorum through the UI (the count on
the row goes 0 of 2 → 1 of 2 → met, with a reviewer's sign-off as the last piece) and
the change then landing; a signed-out visitor offered no controls; a title that is
markup rendering as text.

### 16.6 What is left

1. **A standalone composite-rank view** — the last API surface with no reader
   (§15.4 item 1 is now closed: merge requests have the page).
2. **A propose-a-weight form** (§12.6.3).
3. **No "every route has a reader" gate** (§12.1) — this pass is the second time the
   measurement was made by hand and found something.
4. **`check_doc_numbers.py` cannot see a qualified test count** (§14.6 item 7).
5. Deliberate, unchanged: `admin-ledger` and `metrics` have no UI; the criteria page
   defers pair suggestion, a per-criterion leaderboard and deleting a criterion.

---

## 17. 2026-10-16 (fifth pass): the measurement, instead of another hand count

"Which pages read this endpoint?" had been answered by hand three times, and each
hand count found something the previous one had missed. §12.1 did it with a
heuristic — search the client bundle for each route's contiguous static tail — and
recorded its own false-positive rate: 40 routes with no tail worth searching, 13 with
a tail appearing in no client file, several of those wrongly. A measurement that
cannot tell a miss from a false alarm is a measurement nobody reruns.

This pass replaces it with a gate, then closes what the gate found.

### 17.1 The gate

`internal/httpapi/reader_coverage_test.go` walks the router the binary actually
serves — `chi.Walk` over `srv.Router()`, so the list cannot go stale beside the thing
it describes — and checks a declared inventory (`readerInventory`) in both
directions:

| assertion | what it prevents |
|---|---|
| every API route is matched by a row | an endpoint with no reader — the class this pass is the fifth instance of |
| every row matches a registered route | a row left behind by a deleted route, asserting a reader for something nobody serves |
| each row's `Routes` count **equals** the routes it matches | a route added UNDER an existing fragment, which would otherwise be covered silently |
| a row's `Reader` exists and contains its `Anchor` | a claim of a reader that does not reference the endpoint |
| a row with no `Reader` carries a `Note` | an exemption nobody stated |

The `Routes` count is the load-bearing part: a coarse row matching
`…/features` and everything under it would be a gate that can never fire for the next
feature. The count going stale is the signal, and the failure prints the corrected row
plus the routes it now matches.

`Reader` is a file under `assets/js` and `Anchor` is the endpoint's own path. The pair
proves a reference, not a behaviour, and the spec says so
(`docs/specs/reader-coverage-spec.md`): it cannot see a comment, and it cannot prove
every VERB is reachable.

**The count is deliberate, not clever.** Adding a route anywhere fails the build until
its author reads a row, decides what should read the new endpoint, and bumps a number.
That is the whole mechanism.

### 17.2 What it found

The gate's first run named three surfaces with no reader at all, and a fourth that had
one on paper:

| endpoint | what was missing |
|---|---|
| `POST /projects/{id}/join` | no way to join a project from the browser. "Participate" was a route with a handler, an audit row and a 200 for any signed-in account, and no door. |
| `PUT /consensus/objections/{id}/resolve` | §6.6's resolution: nothing in the browser could record one. |
| `POST /strategy/weight-proposals` | §12.6.3's propose form. The panel could list proposals and consent to them for its whole life and could not open one. |
| `POST /solutions/{id}/coverage/{cid}/contest` | §6.4's challenge — "the claim must stand to count" — enforced against a table no read exposed. A client had no way to name the claim it would contest. |

All four are built. The fourth needed a read that did not exist: the claims now ride
with the solutions payload (`SolutionScore.Claims`), because a contest names a claim
and there was no way to list them.

### 17.3 Three bugs in the same pass, all one shape

The pass's shape is "a rule that exists somewhere and is enforced nowhere", and
building the readers walked straight into three more:

1. **A junk resolution status was a 500 carrying the schema.**
   `UPDATE objections SET status=?` passed the caller's string through, so the table's
   `CHECK` was what refused `"banana"` — and a constraint failure arrives as
   `500 constraint failed: CHECK constraint failed: status IN ('open','withdrawn',
   'resolved','overridden','vetoed')`. A client cannot act on that. The rule now lives
   in the store beside the write (the `CloseConsensusCall` precedent) and answers **400
   naming the four statuses**, with the `CHECK` kept as the backstop it should have
   been.
2. **The response reported a hardcoded `"resolved"`.** Whatever was written —
   `vetoed`, `overridden` — the 200 said `{"status":"resolved"}`, and the page renders
   what the response says; a resolution nobody made is what it would have shown.
   `TestTheResponseEchoesTheStatusThatWasWritten` fails on the old text.
3. **`handleProposeStrategicWeight` had no contributor gate**, while its own doc
   comment said it "follows the taxonomy proposal's shape deliberately: contributor
   gate". Reproduced before it was fixed: a **registered account with no membership in
   the project** opened a weight proposal and got `201` with the proposal body. §6.2's
   "nobody can pin an item to the top by hand" was, at that door, exactly that. Now
   `requireRole(projectID, "contributor", r)`.

Also closed on the way: `ResolveObjection` returned nothing for an id that does not
exist (a 200 for writing nothing) — it is now `ErrNotFound`.

### 17.4 Verification

Eight mutations, each verified to turn a named test red, each applied to the tree,
checked to COMPILE, and restored per-mutation:

| mutation | gate that fires |
|---|---|
| the objections row names a file that does not reference the endpoint | `TestEveryAPIRouteIsDeclaredWithAReaderOrAReason` |
| a row's measured route count is wrong by one | `TestEveryAPIRouteIsDeclaredWithAReaderOrAReason` |
| a new API route with no reader at all | `TestEveryAPIRouteIsDeclaredWithAReaderOrAReason` |
| the resolution status whitelist removed | `TestAResolutionStatusOutsideTheSchemaVocabularyIsRefused` |
| the required reason removed | `TestAResolutionNeedsAResolution` |
| the response reports a hardcoded `resolved` | `TestTheResponseEchoesTheStatusThatWasWritten` |
| the solutions payload does not carry the claims | `TestTheSolutionsReadCarriesTheCoverageClaims` |
| the contributor gate on a weight proposal removed | `TestOnlyAContributorCanOpenAWeightProposal` |

Two of the eight were rewritten before they were accepted as evidence. The first
version of the resolution mutations removed the guard along with the `slices` and
`strings` imports, so they failed to COMPILE — which proves the compiler works and
nothing about the guard. The `Claims` mutation removed the field assignment and left
the variable unused, the same mistake. A red for the wrong reason is not a red (the
§16 lesson, in a new place).

The sweep itself had a bug worth recording because it produced five confident lies in
a row: it restored every mutated file only in its **outer** `finally`, so the first
mutation that failed to compile stayed in the tree and every later mutation was
measured against an already-broken package. Five `COMPILE` verdicts, all naming the
same two files, and the two files were the tell. It now restores per mutation inside
the loop, and verifies the restore byte-for-byte against content read before the sweep
started.

`tests/e2e/readers_e2e.py` (8 browser tests, sqlite-seeded fixtures) proves each of
the four controls appears where a reader looks, does the thing, and reports the
server's refusal to a reader who may not do it:

- a signed-in non-member presses Join and the membership row exists, with one audit
  entry; a signed-out visitor is offered no button;
- a maintainer resolves an objection from the consensus page and the reason is stored
  with it; a contributor's attempt shows the server's refusal and writes nothing;
- a contributor opens a weight proposal from the settings page and the proposal
  appears while the feature's weight does not move; a non-member's is refused;
- a member contests a coverage claim and `contested` is 1 with the reason; the
  author's own challenge is refused in the server's words.

One e2e claim had to be corrected rather than forced: the refusal a non-maintainer
sees is the store's generic `permission denied`, not a sentence naming the maintainer
role, so the test asserts that the refusal ARRIVES and that nothing was written. The
alternative — loosening the assertion until it passed — would have hidden the fact
that `requireRole` cannot say which role it wanted.

### 17.5 What is left

1. **`requireRole`'s refusal does not name the role it wanted** (17.4). Every gate in
   the codebase answers `permission denied`, so a page can report that a write was
   refused and not why. Worth a store-side message with the required role in it, and
   it touches every guarded route, which is why it is recorded rather than done here.
   **Closed by §18.2.**
2. **A standalone composite-rank view** (§16.6 item 1) stays open, and this pass's
   measurement says what it really is: `POST /rank/composite` HAS a reader — the
   criteria table is built from one request — so the gap is not "no reader" but "no
   page of its own". That is a feature, not a defect this gate can see.
3. **An objector cannot withdraw their own objection.** The route requires a
   maintainer, so `withdrawn` is recorded by a maintainer on the objector's behalf,
   and §55's "the bar is no unresolved principled objection" can only be cleared by
   someone else. Needs a route and a reader; deferred with the reason stated.
   **Closed by §18.1** — no new route was needed, because the standing was the wrong
   shape rather than the surface.
4. **`check_doc_numbers.py` still cannot see a qualified test count** (§14.6 item 7).
   **Closed by §18.3** — and the number it could not see was itself two passes stale.
5. Deliberate, unchanged: `admin-ledger` and `metrics` have no UI; the criteria page
   defers pair suggestion, a per-criterion leaderboard and deleting a criterion.
---

## 18. 2026-10-16 (sixth pass): the blocker withdraws, and the refusal says what it wanted

Two entries from §17.5, plus the one that kept the tracker's own numbers invisible.

### 18.1 §6.6 says "the blocker withdraws". The blocker could not.

The spec's remedy for a block has two branches and only one of them was reachable:

> **Resolving a block:**
> - The author amends the solution and the blocker withdraws or converts, or
> - **Override:** a second window where ≥ 80% of non-abstain participants vote to
>   override, with the objector's remedy answered in writing.

`PUT /consensus/objections/{id}/resolve` required the maintainer role for all four
statuses, `withdrawn` included. So the first branch of §6.6's remedy needed the
objector to ask a maintainer to drop their block — and a maintainer to agree — or a
second consensus window with an 80% override. The person whose proposal was blocked
was, in practice, the one who had to clear it.

**The rule now depends on the outcome, not only on the route.** `resolved`,
`overridden` and `vetoed` are findings about somebody else's work and still need a
maintainer; `withdrawn` needs only to be the account that raised the objection:

```go
func objectionOutcomeNeedsMaintainer(obj store.Objection, actorID int64, status string) bool {
	if status == "withdrawn" && obj.UserID != 0 && obj.UserID == actorID {
		return false
	}
	return true
}
```

Three things this deliberately does NOT do:

- **It does not let the objector do anything else.** A verdict on their own block is
  still refused (`TestAnObjectorCannotResolveTheirOwnObjection`), and so is a bystander
  withdrawing somebody else's (`TestSomebodyElseCannotWithdrawAnObjection`).
- **It does not require membership.** Creating an objection has never needed a ROLE
  (§19.3 later gave it §8.2 standing, which is a different question: the account must
  be an eligible collaborator of the project, but no role is asked for) —
  any signed-in account may block — so withdrawing one asks for less: being the
  account that raised it. A rule that asked for membership would leave an objection
  standing that only a maintainer could drop, which is the bug being fixed.
- **It does not move the check into the store.** Roles stay in the handler beside
  `requireRole`; the store's rule is the vocabulary and the reason
  (§17.3). The pure function is what the table test pins, including the case where an
  unknown author (`user_id 0`) must not match an unknown actor.

The page follows the server rather than guessing at it: an objection the reader raised
gets a **Withdraw this objection** control (one status, reason required) and the
four-status control is not shown, because the server refuses the objector all four of
those. Everything else gets the four-status control and the server's answer. The one
thing the page decides for itself is whether the objection is *the reader's* — a fact
about its own session, which it can read, and not a claim about standing, which it
cannot.

### 18.2 The refusal said "permission denied" and not which permission

`requireRole` returned a bare `store.ErrPerm` for every gate, so a page could report
that a write was refused and never why. The rest of this codebase had already rejected
that shape once — the self-vote refusal answers *"you proposed one of these features"*
with a `detail` field, and its test says why: *"A bare 'permission denied' for an
action the interface offered is indistinguishable from a bug to whoever hit it."*

`roleRefusal` wraps `store.ErrPerm` (so `mapError` still answers 403 and every
`errors.Is` check keeps working) and names the role:

```
permission denied: this action needs the maintainer role or above
```

`TestTheRefusalNamesTheRoleItWanted` exercises **two routes with different
requirements** — the objection route (maintainer) and the weight-proposal route
(contributor) — because a message naming a constant role passes a test that only ever
hits one. The browser suite asserts the sentence reaches a reader.

### 18.3 The gate could not see the number it was written for

`scripts/check_doc_numbers.py` exists because a status line sat
`schema 19, 73 tables, 443 tests` for weeks while nothing read the docs. Its test pattern was
`(\d{3,4})\s+tests\b`, so `962 Go tests` — the form `REMAINING.md` actually uses —
was invisible. Measured the moment the qualifier was accepted:

```
REMAINING.md:8:   says '943 Go tests' (test count); the tree has 962
REMAINING.md:740: says '943 Go tests' (test count); the tree has 962
```

The tracker's own headline, two passes stale, in the file the gate reads. §17.5 called
this "still cannot see a qualified test count"; it was worse than that — the number it
could not see was wrong.

Widening is not free, and both obvious widenings are wrong:

- **"A dated section is a record"** would have exempted `REMAINING.md` §12, titled
  *"What is left, 2026-10-15 — THIS IS THE LIVE TRACKER"*, i.e. the section holding the
  numbers that must be right. Pinned by
  `test_a_dated_section_is_not_exempt_by_its_date`.
- **"A line containing 'was' is historical"** was tried once and exempted the whole
  file (§ the gate's own docstring).

So the gate has exactly three exemptions, and COUNTS each one in its output:

| exemption | why |
|---|---|
| a markdown table row | `HANDOFF.md`'s was/now table must keep its *Was* column |
| the literal `(historical)` | each pass section ends with the numbers that pass measured; four lines now carry the marker |
| a number inside quotes, or inside a fenced block | the docs *discuss* stale numbers and quote command output. `"902 Go tests"` in a sentence about this gate is a quotation; so is the block below, which is this gate's own output |

`OK 14 measured number(s) across docs/ agree with the tree ; exempted 9 quoted or
fenced, 5 table row or (historical)`. The exemption counts are printed on the failure
path too, because a gate that silently exemptions lines is a gate you cannot trust.

The fence exemption ends with the fence
(`test_a_number_after_a_closed_fence_is_still_compared`), because a marker that
switched the gate off for the rest of a document would be a way to disable it by
opening a code block.

The `Go` qualifier is deliberately the only one accepted. `N e2e tests` is measured by
pytest, and `make verify` must keep running without the browser toolchain;
`tests/e2e/harness_selftest_test.py` already compares that number
(`test_an_e2e_count_is_not_compared_against_the_go_count` pins the separation, which
would otherwise flag a correct "269 e2e tests" against the Go count).

### 18.4 Verification

Seven mutations, each verified to turn a named test red, each applied to the tree,
checked to COMPILE (or build, for the JS), and restored per mutation:

| mutation | gate that fires |
|---|---|
| the objector's own withdrawal needs a maintainer again | `TestAnObjectorMayWithdrawTheirOwnObjection` |
| nobody needs a maintainer for anything | `TestSomebodyElseCannotWithdrawAnObjection` |
| the objector may record any outcome | `TestAnObjectorCannotResolveTheirOwnObjection` |
| the refusal drops the role name | `TestTheRefusalNamesTheRoleItWanted` |
| the page never recognises the reader's own objection | `test_the_objector_withdraws_their_own_objection` (browser) |
| the doc gate stops accepting the `Go` qualifier | `test_catches_a_stale_qualified_go_test_count` |
| the doc gate exempts any dated section | `test_a_dated_section_is_not_exempt_by_its_date` |

`tests/e2e/readers_e2e.py` grew from 8 tests to 10, and its session fixture now writes
**both** `concord.token` and `concord.user` — the consensus page compares the
objection's author against the session's own id, so a token-only session reads as "not
mine" and the withdrawal control never renders. That is the third time this fixture
family has had to learn the same lesson (`taxonomy_e2e.py`'s proposer card, and the
`apiAuth` call sites before it): **a signed-in fixture that does not say WHO is signed
in tests the signed-out path.**

### 18.5 What is left

1. **A standalone composite-rank view** (§16.6 item 1, §17.5 item 2). §17.5
   re-classified it and this pass confirms it: `POST /rank/composite` HAS a reader —
   the criteria table is built from one request — so the gap is a page of its own, not
   an unreachable endpoint. A feature, not a defect the reader gate can see.
2. **A maintainer's own objection cannot be resolved by them** — which is the new rule
   working, and worth stating as a consequence: a maintainer who blocks something and
   then changes their mind withdraws; if the block stands and the author amends, the
   verdict has to come from another maintainer. In a project with exactly one
   maintainer, an objection that is not withdrawn cannot be resolved at all. §6.6's
   override window is the escape hatch, and it needs the whole second window.
3. **An override window has no route.** §6.6's second branch — "a second window where
   ≥ 80% of non-abstain participants vote to override" — has no endpoint, no store
   method and no page. The `overridden` status can be written by a maintainer's
   decision alone. This is the largest remaining gap between §6.6's prose and the
   product, and it is a feature with its own design questions (who opens the window,
   what counts as the objector's remedy being answered), not a defect to patch.
4. Deliberate, unchanged: `admin-ledger` and `metrics` have no UI; the criteria page
   defers pair suggestion, a per-criterion leaderboard and deleting a criterion.

Note: §19 (the seventh pass) closed item 3 for the RECORD — the override's ratio is
evaluated, the verdict is stored, and the objection it overrides is answered — and §20
(the eighth) then moved the override to where §6.6 puts it and built the second window, so
item 3 is closed outright: see §20.2 for the denominator it is counted with, and §20.7 for
what the window still cannot do.

---

## 19. 2026-10-16 (seventh pass): the verdict nobody recorded, and the call nobody could close

Four defects of one shape: a rule that existed and nothing enforced, and a write whose
value no reader could see. §18.5's item 3 — "an override window has no route", called
the largest remaining gap between §6.6's prose and the product — turned out to be half a
feature gap and half a bug: the ratio was counted, the verdict was computed, and the
record of it was thrown away.

### 19.1 The override was computed, returned, and never written

Measured with a throwaway probe against the code as it stood, before anything was
changed (the probe is deleted; its assertions survive as
`TestACallThatPassesByOverrideRecordsTheOverride` and three siblings):

```
PROBE verdict returned to the caller: "accepted_overridden"
PROBE stored result column: <nil>
PROBE objection status after an ACCEPTED-OVER call: "open"
```

`CloseConsensusCall` ran `UPDATE consensus_calls SET status='closed', closed_at=?`.
Not `result`. So:

- **the verdict lived only in the HTTP response of whoever closed the call.** The
  Consensus page renders `call.result`, so a closed call rendered with no outcome at
  all — "closed" and nothing about how it was decided, for every reader afterwards;
- **the objection the call was accepted OVER stayed `status='open'`.** That column is
  what §55's bar ("no unresolved principled objection") and `TallyConsensus` count, so
  the record contradicted its own result: a call stored as accepted still counted a
  standing block;
- §6.6's "with the objector's remedy answered in writing" had nowhere to be written.

The close is now one transaction writing three things: the call's `status`, `closed_at`
and `result`; every open objection → `overridden`, with the answer as its `resolution`;
the audit entry. All three or none, because they are one decision — half of it committed
is exactly the state above.

**The note is required precisely when the verdict makes it required.** The store refuses
an `accepted_overridden` close with no written answer, naming §6.6, and refuses it
BEFORE any write: the refused close leaves the call open, the result NULL and the
objection open, asserted three ways rather than one. The body is optional, because a
call with no open block has no remedy to answer — which is why `readOptionalJSON` exists
beside `readJSON`: `readJSON` turns an empty body into `400 invalid JSON body: EOF`, and
requiring a note everywhere would have broken every unblocked close. A body that is not
JSON is still a 400; "no body" and "a malformed body" are different things and only the
first may be absent.

### 19.2 The close route was gated on a session, and the call had no reader

§6.8's role table gives "call and close consensus" to the maintainer. The route asked
for a session and nothing else, so any account could fix any project's call's result —
the same missing-gate shape as §17.3's weight proposal, found the same way.

- `POST /consensus/{call_id}/close` now requires the maintainer role. The refusal names
  it (the §18.2 work paying for itself a pass later).
- `POST /consensus` — opening a call — had no gate either, and a call is what quorum is
  later measured against. Same gate, same sentence.

**And nothing could close a call from the browser.** The reader inventory covered
`/consensus` with ONE coarse row: `Routes: 9`, anchor `/consensus`, which any page that
merely reads a call satisfies. The close route sat inside that count and no reader ever
called it. The row is split: `/close` now has its own row, its own anchor and its own
note, and the consensus page has a **Close this call** control with the note field, the
server's refusal shown verbatim, and a re-read on success. The control is offered to
anyone signed in and the SERVER decides, because the page cannot know who is a
maintainer — a hidden control would be a claim it has no way to check (§5.2's rule for
the objection controls, applied to the same page).

### 19.3 A stance was open to anyone with an account

§6.6 says "Everyone eligible can record a position". Eligibility is §8.2's conjunction —
a member of the project AND a qualifying contribution inside the activity window — and
`IsEligibleCollaborator` has existed with a doc comment saying it is "the check §6.5's
distinct-voter condition needs". Neither `POST /position` nor `POST /objection` called
it. A position is counted in `Participants`, which is what `QuorumThreshold` is compared
against, so a stance from outside the pool both met quorum and moved both ratios. A block
is worse than a vote: an open objection holds the call below every pass condition, so a
stranger's block was a stranger's veto.

Both routes now require §8.2 standing and refuse with a sentence that names the rule and
both of its halves.

**Eight existing tests broke, and every one of them was voting as an account with no
standing.** Their fixtures registered a fresh account and cast a stance through the real
endpoint; those accounts were not members and had contributed nothing, and nothing had
ever asked. The fix is `voterFor`/`makeEligible` — a members row plus a project document,
which is route (1) of `qualifyingContributionsSQL`, the same trick the merge-gate pass
already had as `collaborator` — and for the tests that vote as the harness actor, one
`makeEligible` for `testuser`. The new refusals are checked in both directions: an
outsider AND a member with no contribution (a membership-only gate would pass a test that
only asks about the outsider), and then an account that has contributed, which must still
be allowed (a gate that refused everybody would pass the other two).

The e2e fixture had the matching belief in a comment: its closer was granted
*contributor* rights, "closing a call needs one". The route's silence is what had taught
it that; it is a maintainer now.

### 19.4 The resolution was write-only

`Objection` had no `Resolution` field and `GetObjections` selected eight columns, so
`resolution` — written by `ResolveObjection` since migration 0001, required non-empty
there, and written again by this pass's close — was read by nothing. Two consequences: a
maintainer's reason for resolving an objection was invisible on the only page that shows
objections (§17.3's "a resolution is recorded with its reason", recorded and unreadable),
and §6.6's answer to the remedy would have gone the same way.

The field exists, the read selects it as `COALESCE(resolution, '')` (an open objection
reads as empty rather than as a missing key), and the page shows it under the objection
it belongs to, labelled. **The assertion that found this was a read-back, not a store
test**: the store had the value the whole time.

### 19.5 The wire names were PascalCase

`ConsensusSummary` and `governance.ConsensusCounts` carried no json tags, so the close
response was `{"Call":…,"Counts":…,"Result":…}`. Every Go test decoding it into a tagged
struct passed — `json.Unmarshal` matches field names case-insensitively — while the
browser, which looks up `data.result`, found nothing: the page reported "Recorded:
closed" instead of the verdict. **The e2e suite caught it**, and the Go test now asserts
the wire names AS names: the key that must be present and the PascalCase spelling that
must be absent. That is the check a struct-decoding test can never make, and the reason
the assertion is written on the raw body.

This is the second instance of the same trap (§15.2: `governance.Charter` has no tags and
the settings page rendered 22 empty inputs). The rule the two share: a response struct a
page reads is a contract, and the contract is the KEY.

### 19.6 The UI mutation gate could not fail, and reported seven kills it had not earned

`make e2e` ends with `tests/e2e/audit_ui_mutants.py`, which mutates `audit.js`, rebuilds
the binary that embeds it and runs the browser suite once per mutant. Run from a shell
whose `python3` cannot import pytest, every mutant "died": the subprocess exited
non-zero, so it was counted as a kill — with an EMPTY tail in parentheses, because
pytest had printed nothing. Seven false kills, printed as a green line.

The fix is two rules in one shared runner (`tests/e2e/ui_mutants.py`):

1. run pytest with the interpreter running the gate (`sys.executable`, or `E2E_PYTHON`
   when the Makefile sets it), not with whatever `python3` resolves to;
2. **require evidence that pytest ran** — a kill is only a kill if the run reported test
   results. Anything else is `NOT RUN` and fails the gate.

"Non-zero exit means the mutant died" is the same defect this whole pass is about, one
level up: a gate that cannot distinguish "the rule was violated" from "the tool was
broken", and reports the second as the first.

### 19.7 Gates, and the mutation that turns each red

Nine Go mutants, each applied to one source file and each run against the test that
guards the rule. Every one was killed, and every file was restored byte-for-byte
(hash-compared; the harness refuses to continue if a restore differs):

| Mutant | Killed by |
|---|---|
| the maintainer gate on `/close` is removed | `TestTheCloseRouteNeedsAMaintainer` |
| the maintainer gate on opening a call is removed | `TestOpeningACallNeedsAMaintainer` |
| the §8.2 gate on a position is removed | `TestAPositionNeedsBothHalvesOfEligibility` |
| the §8.2 gate on a block is removed | `TestABlockNeedsBothHalvesOfEligibility` |
| the close stops writing the verdict | `TestACallThatPassesByOverrideRecordsTheOverride` |
| the close stops answering the objection it overrides | same |
| the override stops needing the remedy answered | `TestAnOverrideWithNoRemedyAnsweredInWritingIsRefused` |
| the read stops selecting the resolution | `TestACallAcceptedOverABlockNeedsTheRemedyAnsweredInWriting` |
| the close response goes back to PascalCase keys | same |

Three UI mutants for `consensus.js` (see `tests/e2e/consensus_ui_mutants.py`): the close
control offered on a closed call; the note never sent; the resolution not rendered. Each
is a client-side rule a Go test cannot observe — the server answers 200 either way.

The `resolution` field's own test is worth naming for what it is: a READ-BACK. The store
had the value from `ResolveObjection`; asking the store again would have agreed with
itself. The assertion that fails when the select is wrong is the one that reads the API.

### 19.8 Verification

Measured 2026-10-16, seventh pass. Nothing below is remembered.

| Check | Result |
|---|---|
| `make verify` | exit 0 — vet, 973 Go tests in 9 test-bearing packages, build, doc-number gate, e2e-sync gate |
| `make e2e` | exit 0 — 274 e2e tests in 19 suite files, then 10/10 UI mutants killed |
| Go mutants | 9/9 killed, files restored byte-for-byte (hash-compared) |
| UI mutants | 7/7 for `audit.js`, 3/3 for `consensus.js`, source restored byte-identical |
| Doc-number gate | OK 14 measured numbers agree; 9 quoted-or-fenced and 5 table-row/(historical) exempted, each counted and printed |
| Schema | 25 · 25 migrations · 79 live tables — **unchanged**: this pass adds no migration, because every column it writes (`consensus_calls.result`, `objections.resolution`) has existed since migration 0001 and was simply never written or never read |

### 19.9 What is left

1. **The override has no WINDOW.** §6.6 describes "a second window where ≥ 80% of
   non-abstain participants vote to override". What exists now is one close that
   evaluates the ratio, requires the remedy answered, records the verdict and answers
   the objection. There is no second ballot and no second window: a call is open, then
   it is closed. This is still the largest gap between §6.6's prose and the product —
   §18.5 item 3, restated rather than closed.
2. **A closed call cannot be reopened**, and `result` is written once. Combined with the
   maintainer gate on the close, a verdict is final within the product (the admin ledger
   records who wrote it).
3. **A single-maintainer project** still cannot resolve a block that stands (§18.5
   item 2) — the escape hatches are the objector's own withdrawal and, now, closing the
   call over the block with the remedy answered.
4. **The e2e suite runs near the rate limiter's ceiling.** 100 requests/minute per client
   address: three new tests in this suite tipped a shared bucket and a PAGE load answered
   `429 rate limit exceeded`, which a reader would report as a broken page. The new tests
   present their own address (`X-Forwarded-For` from loopback, which the limiter already
   honours) as a fixture fix, not by weakening an assertion. The suite-level fix — a
   distinct address per browser context — is worth doing the next time it bites.
5. Deliberate, unchanged (§18.5 item 4): `admin-ledger` and `metrics` have no UI; the
   criteria page defers pair suggestion, a per-criterion leaderboard and deleting a
   criterion; a standalone composite-rank view is a page of its own, not an unreachable
   endpoint.


---

## 20. 2026-10-16 (eighth pass): §6.6's second window, and the denominator it was counted with

§19.9 item 1 called it "the largest gap between §6.6's prose and the product", and §18.5
item 3 had said the same the pass before. It is now built — but building it forced a
question nobody had asked, and the answer changed a shipped rule.

### 20.1 What §6.6 actually says, in two parts

> **Resolving a block:** … or **Override:** a second window where ≥ 80% of non-abstain
> participants vote to override, with the objector's remedy answered in writing.

Two things are load-bearing, and both were wrong or missing.

**"a second window"** is a SEPARATE ballot. §6.6's pass conditions end with "**No
unresolved block**", so a call with a standing block closes `blocked`; the override is a
second ballot's decision. The seventh pass had made a first-window close compute the
override ratio inline, which made the second window redundant and shipped a product that
could not do the thing §6.6 describes.

**"≥ 80% of non-abstain participants"** is a denominator: consent over everyone who took
a substantive position — consent + stand_aside + block. The tree used
`consent / (consent + block)` — the decisive ratio, whose denominator excludes
stand-asides — behind this comment:

> The override supermajority is measured the same way as a passing call: over the people
> who took a side. Including stand-asides here would mean a reservation could block an
> override it never opposed.

Measured before anything was changed, with a throwaway probe against the code as it stood
so the red would be a behaviour and not a compile error:

```
PROBE non-abstain(spec §6.6) = 0.500  decisive(older reading) = 0.800  override ratio = 0.80
PROBE EvaluateConsensus returns "accepted_overridden" (spec's letter says blocked)
```

4 consent, 3 stand-aside, 1 block. The two readings disagree on exactly this fixture, and
nothing in the suite could see it: every other override case had either no stand-asides
(where the two denominators are the same number) or a supermajority that clears both. The
one case that DID discriminate — `6 consent / 2 stand_aside / 1 block` — pinned the
reading the spec does not state.

### 20.2 Why the spec's reading wins (three grounds, not the letter alone)

1. **Otherwise an override is easier than an ordinary pass.** `5 consent / 6 stand_aside
   / 1 block` reads decisive 5/6 = 0.833 ≥ 0.8 and carries under the old reading, while
   its support of 5/12 = 0.42 is below the 0.5 floor an ordinary call must clear to pass
   at all. A room that is more reservations than consents (`Reluctant()`) would not be
   allowed to ACCEPT a proposal but would be allowed to OVERRIDE a principled objection
   to it — which is the precise hazard §6.6's two-threshold design exists to prevent.
2. **It makes the two rules mutually exclusive**, which the design assumes: under the
   non-abstain denominator `stand_aside > consent` implies a ratio below 0.5, so a
   reluctant consensus can never override anything.
3. **The intent behind the old comment survives, weakened and correct.** A stand-aside is
   "reservations, but I won't block": under the spec's arithmetic they raise the bar
   without vetoing — 8 consent against 2 reservations is exactly 0.80 and carries; add one
   block and it is 0.727 and does not.

`EvaluateConsensus` no longer computes an override at all (an unresolved block is
`blocked`, whatever the ratios say, asserted at 100% consent), and
`governance.EvaluateOverride` applies the second window's rule:

```
quorum:  participants >= QuorumThreshold(eligible, charter)   // the same rule as §6.6
carries: consent / (consent + stand_aside + block) >= OverrideRatio
```

`OverrideRatio`'s doc comment now names the denominator, so the next reader cannot repeat
the mistake the old comment taught.

### 20.3 The second window, built

| `override_windows` | |
|---|---|
| `call_id` | `UNIQUE` — a second window is A window, not a series |
| `opened_by`, `opened_at`, `closes_at` | who called it, and the charter's `VoteWindowDays` |
| `status` | `open` \| `closed` |
| `result` | `overridden` \| `upheld` — nullable, because "" and `upheld` are different facts |
| `note` | §6.6's remedy answered in writing |

| `override_positions` | |
|---|---|
| `(window_id, user_id)` | primary key, so a voter updates their stance rather than duplicating it |
| `position` | the same four values and the same CHECK: `consent` \| `abstain` \| `stand_aside` \| `block` |

One vocabulary on purpose: in the second window **consent = override the block** and
**block = uphold it**, so the ratios come from one function. Separate tables rather than a
nullable `window_id` on `positions`: the existing `PRIMARY KEY (call_id, user_id)` would
become one row per (NULL, user), and every reader of `positions` in the tree would need a
filter it can forget — a vote on whether to override a block must not overwrite the vote
on the proposal.

**Routes** (all four with their own reader-inventory row, since the ninth pass's split row
showed what a coarse one hides):

| Route | Role |
|---|---|
| `POST /api/v1/projects/{project_id}/consensus/{call_id}/override` | maintainer: open (§6.8's "call and close consensus") |
| `GET …/override` | public, like the call; carries `my_position` for the reader |
| `POST …/override/position` | §8.2's eligible collaborators, via `requireEligibleCollaborator` |
| `POST …/override/close` | maintainer; the note required when the window carries |

**Refusals, each naming what is wrong rather than returning an empty window:** the call has
not closed blocked (§6.6's override needs a recorded block — which is what §19.2 fixed);
no objection is open (the objector withdrew or converted, so §6.6's other branch resolved
it and a vote would decide nothing); a window already exists (409, from the UNIQUE); below
quorum there is no verdict to record (closing would claim the room decided against an
override while most of the room did not vote); a stance after the close (409 — a vote
recorded after the verdict would disagree with the ratio that decided it).

**The emergency hold applies.** §6.6's hold "suspends a call", and overriding a block
RESOLVES it: without the check the hold would be a pause you can vote around.

**Three things stayed exactly where §19 put them.** The verdict is written in one
transaction with what it changes (the window's row, the call's `result`, the objection's
status and answer); the note is required by the VERDICT and refused before any write; and
the read hides a running tally (`participants`, `eligible`, `quorum_needed` visible; the
four counts only once closed).

### 20.4 What moved, and what that cost

The close lost its note and its override branch: `CloseConsensusCall(ctx, callID)` no
longer takes a body at all, `readOptionalJSON` moved to the window's close (where a body is
still optional and a malformed one is still a 400), and the four store tests + four HTTP
tests + one e2e test that pinned the old behaviour were retargeted rather than deleted. One
of them was deleted as a case and re-added as its opposite: `TestAnOverrideWithNoRemedy
AnsweredInWritingIsRefused` is now `TestAnOverrideWindowNeedsTheRemedyAnsweredInWriting`,
with the same three assertions about nothing being half-written.

**Found on the way, same family, one line each:**

- **The close used `DefaultCharter(governance_model)` rather than the project's own
  charter.** They agree until someone tunes the charter — and the settings page has a
  charter editor (§6.9) — so a project that raised its consent ratio had its calls judged
  against the default number while its own page published the tuned one. The threshold a
  call is judged by is now the threshold its page shows.
- **`projectIDOfCall` returns its error rather than a zero id.** A swallowed error would
  read project 0, take the default charter and count zero eligible collaborators — and
  `QuorumThreshold(0)` is 0, which is the vacuous-quorum trap `TallyConsensus` documents:
  a silent 100%-of-nobody override.
- **A fixture that could not fail for the right reason.** `seedBlockedCall` cast one
  consent, so the call closed `insufficient_quorum` rather than `blocked` and all seven
  window tests failed on their own setup. Quorum comes first: eligible 6 needs 3
  participants before "blocked" is reachable at all.

### 20.5 Verification

Measured 2026-10-16, eighth pass. Nothing below is remembered.

| Check | Result |
|---|---|
| `make verify` | exit 0 — vet, 1004 Go tests in 9 test-bearing packages, build, doc-number gate, e2e-sync gate |
| `make e2e` | exit 0 — 274 e2e tests in 19 suite files, then 12/12 UI mutants killed |
| Go mutants (`make gates`) | **every gate clean, 0 survived, 0 not applicable** — see §20.6 for what it took to make that line mean something |
| UI mutants | 7/7 for `audit.js` and 5/5 for `consensus.js` = 12/12 (two new: the window shown on any call, and the window's state never read), source restored byte-identical |
| Doc-number gate | OK — 16 measured numbers agree; 9 quoted-or-fenced and 11 table-row/(historical) exempted |
| Schema | **26 · 26 migrations · 81 live tables** (was 25 · 25 · 79): this pass adds `override_windows` and `override_positions`, which is the first migration since 0025 |
| Live | deployed, healthz 200, the override section served, `GET …/override` on a call with no window answers 404 |

### 20.6 The mutation gates had been reporting kills they had not earned

This pass changed `mutate.py` for an unrelated reason and discovered that five of the
twelve mutation gates were lying, in the same way and for four passes.

**The change.** A kill was `"FAIL" in output`. But `go test` exits non-zero for a COMPILE
error exactly as it does for a failing test, so a mutation that never built was printed as
`killed` — and the gate's summary line (`---- 25/25 killed, 0 survived`) then claimed every
rule it names is enforced by a test that actually ran. The driver now reports a build
failure as `UNUSABLE`, counts it with NOT APPLIED (which fails the gate), and says why. The
mutation had to be made to fail a TEST, which is the only thing that proves the rule is
enforced.

**What that exposed, measured: 13 mutants across 5 gate files, every one of them reported
as a kill on the previous pass.** Each is one of two shapes:

- **A dead branch left a variable unused** (`if false {` around a body that read `o`, `role`,
  `held`, `second`, `isOwner`, `surviveBest`), so the compiler refused to build the
  mutation. Fixed by keeping the condition in the dead branch: `if o.Status == "open" &&
  false {`, `if held && false {`.
- **A dropped call left an import or an argument unused** (`math.Min` removed → `math` unused;
  a `Sprintf` with fewer verbs and the same five arguments → vet rejects the format; `if n :=
  cc.Decisive(); n > 0` → `if true` deleted the init statement, so `n` was undeclared in the
  body). Fixed by mutating the RULE instead of deleting the code that reads it: an
  effectively-uncapped weight (`math.Min(reputation, MaxReportReputation*1e6)`), a format
  whose two ends are swapped, an empty room returning `1`.

**Two of them, once made to compile, then SURVIVED — which is the finding worth keeping.**
The httpapi finder gate's "why-text reports only the most destructive option" had been
"killed" by a test that only checked that all four strings appear somewhere in the sentence:
swapping the two ends leaves all four strings present. `TestWhyThisQuestionDescribesTheSplit
NotOneOption` now asserts the PAIRING (`"Self-hosted (I run it)" would rule out 142` and
`"Managed / SaaS" only 38`), and the mutant dies. The fourth instance in this project of the
same lesson: **an assertion that cannot distinguish the two outcomes it is comparing is not
covering the rule**, and a gate that counts non-zero exits cannot tell you it happened.

`scripts/run_all_gates.sh` exists because of the other half of this: `make gates` stops at
the first failing gate, so the gates after it never ran on the passes where one failed —
and a gate that has never run is indistinguishable from a gate whose rules are all proven.
The script takes the gate list from the Makefile (two lists would drift, and the drifted one
is the one that runs) and reports every gate's summary line.

The same pass added a second `<N> live tables` pattern to `check_doc_numbers.py`, for the
one sentence in this pass's measured-state lines that the existing `the N tables` pattern
did not reach: the sentence reads better than the gated phrasing, which is exactly how an
ungated number gets introduced. 16 measured numbers now, 2 of them new.

### 20.7 What is left, after this pass

Items 1–4 below are no longer open questions. Each is a decision the code was already taking
without a record of it, and each is now decided in §22 with the evidence that holds it; item 3
is the one that had no test at all and has one now.

1. **A window that does not carry cannot be retried.** `UNIQUE(call_id)` is deliberate —
   repeating the ballot until the room yields is attrition, not an override — but it means
   the sequence is: the author amends (or the objector withdraws), or the call stays
   blocked. §6.6's other branch covers the first; nothing covers "the room was one vote
   short last month".
2. **A closed window cannot be reopened**, like a closed call, and `result` is written
   once.
3. **The objector may take a position in their own window**, and that stance counts like any
   other: they are §8.2-eligible whenever they are a member with a contribution, and §6.6's
   denominator is "non-abstain participants" — everyone who took a position. Nothing in the
   code excluded them; what was missing was a record of the decision, which is now §22.5 and
   `TestTheObjectorsStanceInTheirOwnWindowIsCounted`.
4. **A single-maintainer project** can now escape a standing block through the window
   (which is what §18.5 item 2 asked for), and still cannot resolve the objection
   directly.
5. Deliberate, unchanged: `admin-ledger` and `metrics` have no UI; the criteria page
   defers pair suggestion, a per-criterion leaderboard and deleting a criterion; a
   standalone composite-rank view is a page of its own.


---

## 21. 2026-10-16 (ninth pass): §6.5's decision record, which the close made unwritable

This pass started from the placeholder PLAN-r4 has carried since 2026-10-05: *"R4's
`decision_records` ... the part of the outcome a reader most needs later -- why we did this
instead of the runner-up -- is recorded nowhere."* The record existed by then, as a
`project_documents` row of kind `adr`. What the note described was still true, and the
reason was worse than a missing field.

### 21.1 Two facts the code made mutually exclusive

- §6.8 gives the maintainer "close consensus", and the close writes the verdict. It sets
  `status = 'closed'`.
- §6.5 says *"the call's result, positions, objections, and remedies are auto-written into
  a decision record (ADR) attached to the feature"*. `RecordCallOutcome` writes exactly
  that — and it refused any call whose status was not `open`.

So the maintainer's own close was what made the record unwritable. Measured first, against
the code as it stood:

```
FAIL: RecordCallOutcome after a close: conflicts with current state: call 1 is already closed
FAIL: the decision record does not mention "Document the workaround"   # the runner-up
```

The page offered the close and nothing else: no reader anywhere called `/outcome`, which is
the third instalment of this repository's most expensive shape. The record was reachable
only by **not** closing the call — the opposite of "auto-written".

**Fixed** by making the guard say what it is for. It is not "this call is open", it is "this
call is already decided":

```go
if call.Outcome != nil {   // was: if call.Status != "open"
```

Both orders now work and neither erases the other: the verdict stays on `result`, the
decision on `outcome`. A second outcome is still refused — which was always the guard's
real concern, since a second one would overwrite the first and move the board under it.

### 21.2 The record said what was chosen and never why

§6.5's next sentence is *"the ranking is the fallback chain. If the call stalls or a block
is upheld, the next-ranked solution is the pre-agreed fallback."* The record carried the
outcome, the positions, the objections and the remedies — and not the ranking, so "why this
and not the runner-up" had no answer in it.

The record now carries **the ranking the decision was made against**: rank, title, score,
rating, coverage and distinct voters for each solution, with the one the call was about
marked *adopted*/*not adopted*, the runner-up marked **the pre-agreed fallback**, and the
baseline last, marked *ranked, never adopted*. It is a **snapshot** — the arena keeps
moving, and a chain asked for a year later answers "what would we do now" rather than "why
this, then". `- **Fell back to:**` names the solution (`Document the workaround (#7)`), not
its id.

Two smaller things in the same function:

- **The record claimed the epoch.** `- **Closed at:**` read `call.ClosedAt`, and in the only
  order that worked (record a call that was never closed) that column is still NULL, so
  every such record was dated `1970-01-01T00:00:00Z`. It is now `- **Decided at:**` from
  the timestamp of the write, passed in rather than read off a row that has not been
  updated yet.
- **An objection's `resolution` was never in the record** even though §17.3 requires one to
  be recorded with its reason and the column has been written since 0001. The record shows
  it under the objection now.

### 21.3 Three routes with no reader, and one of them leaked

`/outcome`, `/fallback` and `/hold` all rode the coarse `"/consensus"` reader-inventory
row — the row that is satisfied by *any page that merely reads a call*, which is the row
that already hid `/close` and §6.6's window in earlier passes. Each now has its own row,
with an anchor in `consensus.js` and a counted route total, and the page reads all three:

| Surface | What the page does with it |
|---|---|
| `GET …/fallback` | §6.5's chain, rendered **while the call is open** — "pre-agreed" means knowable before the decision, not after it stalls |
| `POST …/outcome` | **Record the decision**: the five outcomes, the fall-back picker (the chain minus the baseline, because `rejected` is §6.5's "the baseline wins"), the amendment fields, and the name of the record it wrote |
| `GET …/hold` | §6.6's hold, said out loud: a suspended call looks entirely ordinary while every write is refused |

**The hold handler did not check that the call belonged to the project in the URL.** It
resolved `{call_id}` and read the hold, so a hold's *grounds* and expiry were readable
through **any** project's URL by anyone who could read that project. Ids are sequential;
the URL was the only thing naming which call was meant. It now resolves the call through
the same guard every sibling route uses.

### 21.4 Two more routes that answered to a session rather than a role

- **`POST …/outcome` accepted any account.** Recording an outcome *closes the call* and
  moves the board — the adopted solution to `ready`, the displaced one to `rejected`. That
  is §6.8's "close consensus" with a different verb, and it was open to any session while
  the close route beside it required a maintainer. It requires the maintainer now, and a
  test names the role in the refusal.
- **It ignored §6.6's hold.** The hold suspends a call; an outcome resolves one. The close
  and the override window both refuse a held call for exactly this reason, so leaving this
  route open made the hold walk-able by choosing a different menu item. Refused now, with
  the grounds and the expiry in the message.

### 21.5 Verification

| Check | Result |
|---|---|
| `make verify` | exit 0 — vet, the Go suites, build, doc-number gate, e2e-sync gate |
| `make e2e` | exit 0 — 277 e2e tests in 19 suite files, then 16/16 UI mutants killed (9 of them the consensus page's) |
| `make gates` | every gate clean; the new decision-record gate is 8/8 killed |
| Reader inventory | `/outcome`, `/fallback` and `/hold` have their own rows; the coarse row is 4 |
| Schema | 26 · 26 migrations · 81 tables — **unchanged**: this pass needed no migration, because every column it writes has existed since 0001–0013 |

The mutation gate earned its keep on the new tests: mutating the row marker (*"the record
stops saying which alternative is the agreed fallback"*) **survived**, because the
assertion asked whether the words appear anywhere in the document and the paragraph under
the table also contains them. The assertion is now on the runner-up's own row, and the
survivor is killed. Same lesson as §20.6, one layer deeper: *where* a string is matters as
much as whether it is there.

### 21.6 What is left, after this pass

1. Everything in §20.7, unchanged.
2. **A call closed with no outcome recorded writes no decision record.** That is the
   design and not an oversight — §6.5's five outcomes include choices the verdict cannot
   derive ("fall back to #2", "sent back", "accepted with amendments"), so the record is a
   decision a maintainer makes, not a rendering of the tally — but it does mean a project
   that never records outcomes has calls with no ADR. The page now offers the control on
   every solution call that has no outcome, which is the most that can be done without
   inventing the decision.
3. **The outcome panel is offered to anyone signed in**, and refused by the server to
   everyone but a maintainer. That is the house rule (a client-side role check is a claim
   it cannot verify), and it is worth restating here because the refusal is the only thing
   standing between the control and the rule.
4. **`rejected` and "fall back to the baseline" are two spellings kept apart on purpose**:
   the fall-back picker hides the baseline, because §6.5 lists them as different outcomes.

### 21.7 The other two hold routes: one wrote across projects, the other answered 404 for everyone

The hold had a third and fourth defect, found by asking the question §21.1 asks, of the
route §21.3 had just given a reader. Measured first, both against the tree as it stood:

```
FAIL: a maintainer of project B suspended project A's call through B's URL (201 Created):
      {"hold":{"id":1,"project_id":1,"call_id":1,"held_by":1,"grounds":"legal",...}}
FAIL: the project's own maintainer could not lift its hold = 404: {"error": "not found"}
```

**`POST …/consensus/{call_id}/hold` asked for the role against the project in the URL** and
then loaded the call by its id alone, writing the hold to `call.ProjectID`. A maintainer of
any project could therefore suspend **any other project's call** by asking through their own
project's URL — and the write landed on the right row, carrying the right project id, which
is exactly what made it look correct. §8.1 reserves this power to the maintainer as *"the
only content-level power a maintainer keeps"*.

**`POST /api/v1/emergency-holds/{hold_id}/release` could not be used by anyone.** It asked
for a role against `chi.URLParam(r, "project_id")`, and that path has no project in it: the
slug resolved to the empty string, `requireProjectID` answered 404, and every caller got the
same 404 — including the project's own maintainer. It sat behind an inventory row that
explained why no *page* called it ("an operator releases a hold out of band"), which is true
and was also the reason nobody noticed that **no one** could call it. Behind that defect the
second one was waiting: it released the hold **by id alone**, so had the URL carried a
project it would have lifted another project's security or legal embargo on a role from an
unrelated project.

**One guard, not two more patches.** The two halves of a call URL are independent
parameters — `{project_id}` names a project, `{call_id}` names a call, and the router does
not tie them together — so *every* handler that resolves a role against one and a row
against the other is open, and patching the two that had been exploited leaves the next one
to be written. `callerCall` now resolves the call and proves it belongs to the URL's project
in one place, and every route that names a call goes through it: `GET /consensus`,
`GET|POST …/hold`, `POST …/close`, `POST …/position`, `POST …/objection`, `POST …/outcome`,
`GET …/fallback` and the override window's three. The two that answered **403** for a call
from another project answer **404** now — a 403 confirms the id is real, which is the reason
the read routes already said 404.

The release route resolves the hold first and asks for the maintainer role on **the hold's
own project**, which is the only project that can lift it. It also requires a written reason
now: its own doc comment has said "recording why" all along while the ledger recorded an
empty string for anyone who sent one.

**A route nothing calls has no test for whether it can be called at all.** That is the
lesson the release route teaches, and it generalises past pages: the reader inventory's
"no page calls this" rows are honest reasons, but they are not evidence that the route works
— the API test is. There is one for the hold's whole lifecycle now.

---

## 22. 2026-10-16 (the decisions left, taken)

§20.7 and §21.6 listed what remained as questions. Read again, most of them were never work:
the code was already doing something coherent, and what was missing was a record of *which*
behaviour is intended — so that a later pass does not find it, read it as an accident and
"fix" it. Each is decided below with the evidence that holds it. One was a genuine gap: the
objector's own stance had no test, and that is §22.5.

### 22.1 A window that does not carry cannot be retried — retained

`UNIQUE(call_id)` stays. Repeating the ballot until the room yields is attrition, not an
override, and §6.6 gives the blocked call two other exits: the author amends, or the objector
withdraws. Held by `TestOnlyOneOverrideWindowPerCall` and by §6.6's other branch in the close.

The cost is stated rather than hidden: "the room was one vote short last month" has no route.
That is the point — a second window on the same objections would let a determined majority
wait out the objector, which is what §6.2's floor exists to prevent.

### 22.2 Neither a closed window nor a closed call is reopened — retained

`result` is written once, on both windows. Held by `TestAStanceInAClosedWindowIsRefused` — a
stance recorded after the verdict would silently disagree with a ratio it did not influence —
and by the close's own refusal to re-close.

### 22.3 The outcome panel is offered to anyone signed in — retained

Not a client-side role check, deliberately: the page cannot verify a role, so a hidden control
would be a claim rather than a rule, and the refusal it would be hiding is the authoritative
one. Held by the API tests asserting the maintainer requirement.

### 22.4 A call closed with no outcome writes no decision record — retained

§6.5's five outcomes include choices the verdict cannot derive ("fall back to #2", "sent back",
"accepted with amendments"), so the record is a decision a maintainer makes rather than a
rendering of the tally. Held by `TestACallWithADecisionRecordIsNotDecidedTwice` (a second
outcome is refused) and by `TestASolutionCallKeepsItsDecisionRecordReachableAfterTheVerdict`
(recording the outcome after the close works, and neither order erases the other).

### 22.5 The objector may take a position in their own window — retained, and now pinned

This was the one real gap. Nothing in the code excluded the objector, and nothing recorded that
as intended either. It is intended, for the reason §6.6 names its own denominator: the window
carries on non-abstain *participants*, every person who took a position, and the objector's
position is the most informative one in the window — whether they consent to being overridden
or vote to uphold their own block. Excluding them would put the conflict-of-interest guard on
the wrong question: the objector cannot **resolve** their own objection (held by
`TestAnObjectorCannotResolveTheirOwnObjection`), but voting on it is what the second window is
for.

`TestTheObjectorsStanceInTheirOwnWindowIsCounted` has three cases, and the arithmetic in the
first is what makes it load-bearing rather than decorative: 3 consents of 4 non-abstain
participants is 0.75, which does NOT carry, while the same 3 consents with the objector
abstaining is 3/3 = 1.00, which does. The objector's own stance decides the window.

**The mutation gate had a hole exactly here, and it is closed.** The armed test files for
override.go's gate had never cast an `abstain` — measured, not assumed: one occurrence of
`"abstain"` across `override_window_test.go` and `consensus_override_test.go`, and it was this
new test's — so the mutant *"an abstain counts as a non-abstain participant in the override
ratio"* **survived** the gate. Demonstrated rather than asserted: with the mutant applied and
the new test filtered out, the pre-existing armed set reports `ok` (the mutant lives); with the
new test in, it fails on the tally. Two mutants were added to
`internal/store/override_mutants.json` — that one, and "a block in the second window is not a
participant at all" — both killed, with every anchor verified to match once in the source.

### 22.6 A single-maintainer project still cannot resolve the objection directly — retained

The escape is the window (§18.5 item 2, which is what §22.1's refusal leaves standing), and the
refusal is the conflict-of-interest rule: the party an objection is about does not answer it. A
one-person project's other remedy is a second maintainer, which is what §8.2 membership is for.
Held by `TestAnObjectorCannotResolveTheirOwnObjection`.

### 22.7 `rejected` and "fall back to the baseline" stay two spellings — retained

§6.5 lists them as different outcomes, and the fall-back picker hides the baseline for that
reason.

### 22.8 The public instance is current as of this pass

`concord.polarisocial.xyz` was running `v0.4.0-voting-56-g3ffae12` — the 2026-10-14 build, 67
commits behind HEAD — reached through the cloudflared tunnel on thinkcentre to
`127.0.0.1:8006`. Read from the running process rather than from a document: the tunnel's origin
is a systemd *user* unit (`~/.config/systemd/user/concord.service`), and the live version came
from `/api/v1/version` and the page footer, which agreed with each other.

It now runs `v0.4.0-voting-123-gae1e210-3-g6921056` — commit `6921056`, the code commit of
this pass. Both instances do, the public one and the laptop's, so "which build is deployed?"
has one answer. Deployed with both backups taken first: the database online
(`concord.db.bak-predeploy-20261016-090636`, 22 migrations, taken with `sqlite3 .backup` so the
WAL is consistent) and the previous binary (`concord.bak-20261016-090636`). Migrations applied
on start: 22 → 26.

**Reachability was verified rather than assumed.** `make deploy` targets `$HOME/concord-deploy`
on whichever machine runs it, and this repository is a mount of thinkcentre's disk, so the
deploy was run *on* thinkcentre: the built binary was already on that disk
(`/home/alvaro/code/projects/concord/bin/concord`, sha256 compared before install). Running
`make deploy` from the laptop would have stopped and restarted the laptop's **own** `concord` on
`127.0.0.1:8006` — a different instance, answering `healthz` perfectly — and reported success.

**A deploy that copies `bin/concord` without building first installs an unnamed binary.** The
first attempt on the laptop instance installed a binary that reported `version "dev"` — the
e2e/UI-mutant harness rebuilds `bin/concord` with a mutated or partial tree and leaves whatever
it built last, so the release artifact and the build artifact share a path. `make deploy` is
safe for exactly this reason (`deploy: build`); a hand-rolled `install bin/concord` is not.
Both instances were redeployed from a clean `make build` at that commit, and the version string
in the footer is the check that catches it.

### 22.9 The embedding index was incomplete, and is not now

The new build printed two warnings on start that the deployed one could not, because the check
is newer than that binary:

```
WARNING: feature embedding index is incomplete: 872/1156 embedded (284 never indexed).
WARNING: project embedding index is incomplete: 70/71 embedded (1 never indexed).
```

Data conditions rather than defects — 284 features and 1 project could not be found by
similarity search — and the binary names its own remedy (`bin/embedbackfill -kind feature`).
Backfilled on both instances: the public one, feature 284 embedded → 1156/1156 = 100.0% and
project 1 → 71/71 = 100.0%; the laptop's, project 1 → 56/56 = 100.0% (its features and
complaints were already complete: 537/537 and 655/655). Requests have no rows on either.
