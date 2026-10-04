# Concord — what is left, and what "done" means here

Written 2026-10-05. Every number below is measured, not remembered. The doc-number
gate (`scripts/check_doc_numbers.py`, in `make verify`) fails when any of them goes
stale, so this file cannot quietly become fiction.

**Measured state:** schema 25 · 25 migrations · 79 live tables · 877 Go tests across
9 test-bearing packages · 174 e2e tests in 10 suites · 7/7 UI mutants killed ·
`make verify` exit 0, `make e2e` exit 0.

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

877 Go tests. `make verify` and `make e2e` green.

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

877 Go tests. `make verify` and `make e2e` green.

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

877 Go tests. `make verify` and `make e2e` green.
