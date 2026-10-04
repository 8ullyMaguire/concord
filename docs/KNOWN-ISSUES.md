# Known issues

Defects confirmed by reproduction, with what was ruled out. Each entry states how
it was verified, because every one of these looked like something else first.

## Every page existed; almost nothing linked to them (fixed 2026-10-05)

**Reported as:** "I can't see the finder or anything else on the site."

**The cause was not a bug. Nothing was broken.** Measured on the live instance
before touching anything:

| Page | Status | Linked from |
|---|---|---|
| `/finder` | 200 | **nothing** |
| `/scout` | 200 | **nothing** |
| `/projects/{slug}/consensus` | 200 | **nothing** |
| `/projects/{slug}/audit` | 200 | only in an undeployed `project.js` |
| `/projects/{slug}/board`, `/documents`, `/rank`, `/ranking` | 200 | the project header |

The site nav held four links: `/`, `/search`, `/projects`, `/login`. Finder and
Scout each linked only to the *other*, and only from inside their own result state,
so neither was reachable by clicking from anywhere.

A page is a route plus a template. Nothing requires anything to LINK to it, so an
unreachable page is a perfectly ordinary state for the code to be in — which is why
the entire suite was green throughout: every Go test, every e2e test, every UI
mutation killed. None of them could see a missing link.

### The first diagnosis was wrong, and the way it was wrong is the useful part

Grepping the templates for `href="/board"` reported board, documents, rank, ranking,
consensus and audit all orphaned. Four of those six were linked the whole time —
in `project.js`, as string concatenations:

    '<a class="btn" href="/projects/' + esc(p.slug) + '/board">Open board</a>'

A static grep cannot see that, so it reported working navigation as missing. Only
the browser showed the truth: `document.querySelectorAll('.detail-tab')` found four,
not zero. **Verify navigation in a rendered DOM, never by scanning source.**

That mistake is recorded here because it points the wrong way twice: it invents
bugs that are not there, and — had the real gaps been dismissed as "grep says it's
fine" — it hides the ones that are.

### The fix, and the first attempt at it that was also wrong

Nav now carries Scout and Finder ahead of the browse links. The project header
became a seven-tab bar: Overview, Consensus, Board, Vote, Ranking, Documents,
Audit.

The tab bar was first built in `project.js`, which is wrong for a reason worth
stating: **every sub-page is a separate template loading its own bundle**, so a tab
bar built there appears on the overview and nowhere else — turning six working
pages into six dead ends. It now lives in `templates/base.html`, driven by the
request path, so it renders on all seven and cannot disagree with the page it is
on.

### Four bugs this introduced, each caught by making the check fail

Every one of these was green until something was made to go red:

1. **A 404 leaked a private project's slug.** `Path` drove the tab bar, and
   `notFoundPage` was switched to `pageFor`, so every 404 under
   `/projects/<slug>` rendered seven links naming a project the caller may not
   read — enough to enumerate the instance. Caught by
   `TestDocumentsPageHidesAPrivateProjectFromAnonymousCallers`, a *documents* test.
   `notFoundPage` uses `s.page()` again, and
   `TestErrorPagesNeverEchoTheSlugFromThePath` now guards it.
2. **The whole document rendered twice**, so every project page carried 14 tabs.
   Caught by nothing: every navigation test asks whether a link EXISTS, not how
   many times. `TestLayoutRendersEachPieceOnce` asserts counts.
3. **`index $parts 1` panicked the template on `/`**, truncating the page after
   `render` had already sent 200 — the exact failure mode `base.html` documents in
   its own comment about a 506-byte page ending mid-head. Replaced the template
   index arithmetic with a `tabFor` helper that returns nil, so the template says
   only `{{with tabFor .Path}}`.
4. **A redundant-looking guard had no test isolating it.** `projectTabs` checks
   both `len(parts) < 2` and `parts[0] != "projects"`; dropping the second survived
   because every case was either shorter than two segments or began with
   `/projects`. Adding `/finder/x` killed it.

### The e2e suite caught the regression this fix introduced

Adding two nav links made the header 20px too wide at a 375px viewport:
`test_mobile_no_horizontal_overflow` failed with `horizontal overflow of 20px`.

The nav had **three** links and fit at 375px by luck, with no media query touching
it anywhere in the stylesheet — so the breakpoint that mattered was never written
down, it was just whatever the link count happened to allow. Adding a fifth broke
it.

Fixed by letting the nav wrap below 640px and hiding the brand name below 30rem.
`min-width: 0` on the nav is load-bearing: it is a flex item in a `nowrap`
container, so without it its min-content width refuses to shrink and the row
overflows anyway — the same overflow the rule exists to remove.

The general lesson: **a layout that fits is not a layout that has a breakpoint.**
"Narrow screens work" was never tested; three links happened to fit.

### Now gated

`internal/httpapi/navigation_test.go`, 10 tests: every registered page has an
inbound link, the nav links every top-level page, every sub-page carries the tab
bar with its own tab lit, non-project pages carry none, the layout renders each
piece once, and error pages never echo the slug.

A `{{define "project-tabs"}}` block placed at the top of `base.html` — above
`<!DOCTYPE html>` — is legal and hides the document's shape behind a block of
markup no reader of the file is looking for. It belongs at the end, with a comment
saying why.

## `PRAGMA integrity_check` disagrees with itself across SQLite builds

`ALTER TABLE t ADD COLUMN b REAL NOT NULL DEFAULT 0.5` does not write a value
into rows that already exist. They read back the default — `SELECT sum(b)` is
correct, `WHERE b IS NULL` matches nothing — but two SQLite builds disagree about
whether the database is intact:

| Reader | `PRAGMA integrity_check` |
|---|---|
| `sqlite3` 3.45.1 (CLI, C) | `NULL value in t.b`, once per row |
| `modernc.org/sqlite` v1.59.0 (what Concord uses) | `ok` |

Reproduced in six statements on stock sqlite3, and cross-checked on the same
database file both ways. The production database reported 70 such lines for
`charters.support_ratio_min` (one per charter) against 0 actual NULLs, from
migration `0011_charter_support_ratio.sql` — the only migration using that shape.

`VACUUM` does **not** clear it, on either build.

**Reading integrity_check with a different tool than the one that wrote the
database will produce false alarms.** Check with the driver, or accept the
report as advisory for tables that gained a `NOT NULL` column this way.

### Ruled out on the way

- **Data loss.** `sum(support_ratio_min)` = 35.0 over 70 rows, `typeof` = real,
  `WHERE … IS NULL` = 0 rows. The values are all there.
- **Stale b-tree records / needs a rewrite.** `VACUUM` and
  `PRAGMA wal_checkpoint(TRUNCATE)` both leave the reports in place.
- **A table rebuild (migration 0008) preceding the `ADD COLUMN`.** A synthetic
  rebuild-then-add reproduces the same reports as add alone, so the rebuild is
  not the trigger.
- **Driver age or CGO.** Reproduced on stock C sqlite3, and a 400-row × 21-column
  table survives `VACUUM` cleanly in both.
- **The migration runner's transaction handling.** Applying the same SQL through
  the runner and through the CLI gives the same result per reader.

### The trap that cost the most time

Copying a WAL-mode database with `cp db out.db` and *not* copying `db-wal`
silently discards every change since the last checkpoint. The copy reads as
`ok` and appears to be missing the column entirely — a different, also-correct
observation that looks like the bug being fixed. Two rounds of "fixing" a
non-existent problem came from comparing against such copies. Always copy
`db`, `db-wal` and `db-shm` together, or checkpoint first.

## `pairwise_votes` was append-only only in intent (fixed 2026-10-05, `0024`)

**Was:** the table every priority score on this site is computed from had no
`CHECK`, no trigger, and no application-layer guard against `UPDATE` or `DELETE`.
`admin_ledger` got both triggers in `0013`; the ranking substrate never did.

**Why that one mattered more than `admin_ledger`.** An `admin_ledger` row is a
record of an event — rewriting it erases history. A `pairwise_votes` row is an
**input to a Glicko-2 rating**: `RecordVote` applies the voter's weight, and the
rating is derived from the sum. Editing one row does not just erase the record,
it silently changes every rating derived from it, and Glicko-2 cannot tell a
corrected database from a tampered one — a tampered rating set is still internally
consistent, which is exactly what makes it hard to notice. Editing three rows
would buy any position on the board.

**What the live instance actually holds, so this is not overstated:** 55
`feature-priority` arenas exist and **`pairwise_votes` has 0 rows** — no vote has
ever been cast here. So the table being unprotected was a real hole in the
*schema*, and it is the table every vote will land in, but no rating is currently
being distorted because no rating exists. Worth stating plainly: had I found
"0 votes", I would not have claimed a tampering problem — I am claiming a missing
constraint on the table the ranking depends on, which is true and would matter the
moment voting is used. The trigger is cheap and has no HTTP path to it, so there
was no reason to defer it to first use.

**Now:** `0024_vote_log_append_only.sql` refuses both, in the database, with the
message naming the rule.

**The escape hatch, and why it is explicit rather than absent.**
`seed/dedupe.py --apply` legitimately deletes votes — it repairs duplicate seed
data, and a feature's pain is the sum over its linked complaints, so a complaint
recorded twice doubles the pain of every feature linked to it. A trigger with no
way through would turn a maintenance script into a crash rather than a warning.
So deletes are permitted only while exactly one row in `maintenance_signals` is
enabled, and `dedupe.py` is the only writer that sets it — armed before its first
delete and disarmed after its last, in one transaction, so there is no window
where the hatch is left open. There is no HTTP path to that table.

**Gates that write the bad row.** `internal/db/vote_log_append_only_test.go`
attempts the `UPDATE` and the `DELETE` and requires them to fail *by message* —
"some error" is satisfied by a malformed `UPDATE`, which proves nothing about the
trigger. The hatch is tested in both directions, because a gate that only proves
the happy path is half a gate: it must open while armed, and refuse again once
disarmed. Five mutants of the migration itself, all killed:

| mutant | killed by |
|---|---|
| DELETE trigger dropped | `TestAVoteCannotBeDeleted` |
| `WHEN` clause always fires (hatch never opens) | `TestTheDedupeEscapeHatchWorksOnlyWhileArmed` |
| exactly-one check dropped | `TestTheHatchCannotBeArmedIntoPermittingMoreThanOneRun` |
| UPDATE trigger dropped | `TestAVoteCannotBeEdited` |
| `CHECK` on the signal removed | `TestTheHatchSignalIsCheckedNotAnyValue` |

`tests/dedupe_hatch_check.py` runs the **real** `dedupe.py` — imported, not
reimplemented — against a database holding a real duplicate and real votes.

**Two gates I wrote that were themselves vacuous**, both caught by running them:

- The "table is protected again afterwards" check deleted from a table that
  `--apply` had just emptied. A `BEFORE DELETE` trigger fires **per row**, so
  deleting zero rows fires nothing, succeeds, and the check reported a hole that
  did not exist. It now seeds a vote first. This is the third time this session
  that a fixture which could not distinguish correct code from broken code was
  the actual defect — the same lesson as an inert XSS fixture and as a Go gate
  that only forbids private fields.
- The fixture itself failed four times before it worked, and every failure was
  the fixture, not the trigger: `features.author_id` is `NOT NULL`, `status` is
  `CHECK`ed (`draft`, not `proposed`), `pairwise_votes.arena_id` became
  `NOT NULL` in `0017`, `INSERT OR IGNORE` silently duplicates because
  `(project_id, title)` is not unique, and one `Scan` cannot read two ids from a
  two-row query. Worth recording because the honest reading of four red runs is
  "the thing under test is broken", and here it never was.

**Still true, unchanged:** `git fsck`/`VACUUM` are advisory on this file, and
the WAL-copy trap below still applies.

## Replaying `0008` replaces a newer table shape with an older one (2026-10-05)

**Symptom.** Replaying 0008 against a live database dropped columns, indexes,
triggers and foreign keys. All four are fixed. One residue remains, and it is a
different mechanism from the other four.

### Fixed, and how each was found

Nothing here was found by reading the code. Each was found by **replaying 0008
against a copy of the live database** and diffing the schema before and after, or
by a test failing for an unrelated-looking reason:

- **Silent column loss.** 0008 rebuilds 23 tables to shapes written into the
  file. Nineteen later `ADD COLUMN`s land on five of them. `SELECT *` made that a
  loud crash; naming the columns made it silent data loss instead. Fixed by
  widening each rebuilt table from the table it replaces, at an explicit
  `-- concord:preserve-columns-split` marker.
- **A later trigger breaking the rebuild.** 0015's `trg_tag_alias_no_shadow` reads
  `tags`, which 0008 drops, and SQLite revalidates a `WHEN` clause on every write,
  so the replay died with `no such table: main.tags`. Fixed by dropping dependent
  triggers first and restoring them after.
- **Triggers silently removed.** `DROP TABLE` takes a table's own triggers with
  it. `trg_pairwise_votes_no_update` is `BEFORE UPDATE ON pairwise_votes` — the
  rebuilt table itself — so the append-only guarantee 0024 added just vanished, with
  no error. Worse than the crash it replaced: the crash blocked a replay nobody
  performs, the missing trigger removed an invariant from a database that was
  running.
- **Indexes silently removed.** Same mechanism, and `UNIQUE` indexes are
  invariants, not performance. Seven indexes from 0014/0017/0020 were dropped by
  `DROP TABLE` without error.
- **Foreign keys silently removed.** A widened column kept its data and lost its
  `REFERENCES` clause — so `pairwise_votes.arena_id` survived as a plain integer and
  `ON DELETE CASCADE` stopped working. SQLite has no per-column FK in
  `PRAGMA table_info`, so the clause is recovered from the DDL in `sqlite_master`.

### Two of my own claims were wrong, and measuring caught both

- **"NOT NULL columns are skipped" was wrong** in a way that *lost data*. The
  original reasoning was "SQLite refuses `ADD COLUMN NOT NULL` on a non-empty
  table", which is only true **without a default**. Skipping NOT NULL therefore
  dropped `charters.support_ratio_min` (0011, `NOT NULL DEFAULT 0.5`) and
  `tags.suggested` (0015, `NOT NULL DEFAULT 0`) in a replay that otherwise
  reported success. A guard that discards a column to avoid an error it would
  never have hit is the guard causing the damage.
- **A fallback that relaxed NOT NULL was dead code.** It existed because I had
  reasoned about the *source* table, which has rows, rather than the *target*,
  which is empty at widening time. SQLite refuses the strict form only on a
  non-empty table, so it always succeeds and NOT NULL is in fact reproduced
  **exactly**. The mutation was what proved it: replacing the fallback with
  `continue` changed nothing observable, because no fixture can make a rebuilt
  table non-empty before the copy.

### The residue: a replay re-applies the OLD shape

`features` is rebuilt by **both 0008 and 0010**. 0010 declares
`body TEXT NOT NULL DEFAULT ''` and adds `effort`, `impact_ratio` and a CHECK; 0008
declares `body TEXT DEFAULT ''` and has none of them. On a fresh install 0010 runs
last and wins. Replay 0008 alone and 0008's older, weaker shape wins instead:

```
features  lost NOT NULL ['body', 'effort']
```

Widening cannot fix this. Adding a column is `ALTER TABLE ADD COLUMN`; **removing a
NOT NULL, or restoring a CHECK constraint, is not expressible** — SQLite has no
`ALTER COLUMN`. So for a column 0008 *already declares*, widening has nothing to
do; the weakened constraint simply survives.

Fixing it means deriving each rebuilt shape from the source table rather than
widening a declared one. That rewrites how all 23 tables are built and wants its
own review, so it is recorded here rather than done in a verification pass.

Measured on a replay of the live database (79 tables, 1806 audit rows, 437
features): **everything else is lossless** — rows, columns, NOT NULL, foreign
keys, 55 named indexes, 10 triggers, `integrity_check ok`. Only this one
constraint discrepancy remains.

### The honest measure of the fix

The replay test is a diff of 79 tables before and after — rows, columns, NOT NULL
flags, foreign keys, named indexes, triggers — against a copy of the live database.
It is the only thing here that found these defects, and four of the five were
invisible to the unit tests.

Two gates, both of which were **wrong first**:

- `TestMigration8SurvivesALaterAddColumn` passed with every one of 23 tables
  reverted to `SELECT *`. `migrateFS` skips versions already in
  `schema_migrations`, so the "replay" re-ran nothing. Clearing the version row
  first is both the fix and the honest model of a restored backup. It also widened
  only `charters`, so reverting any of the other 22 was invisible; it now derives
  the table set from the migration's own `CREATE TABLE x_new` lines.
- `TestNoMigrationCopiesRowsWithSelectStar` asserts the class across all 24 files,
  because a test aimed at one file cannot catch the next one written that way.

**Every marker and helper was mutation-checked**, and two were wrong in a way only
the revert showed: removing `-- concord:preserve-columns-split` from 0008 leaves
the runner's other marker in place, so the file looks opted-in while no widening
runs — killed only by reverting the split marker itself. And
`TestTheHatchSignalIsCheckedNotAnyValue`, a test about a vote log, is what caught
the index-restore collision. Nobody would have looked there.

## An unindexed row makes duplicate detection fail silently (recurs; guard added 2026-10-05)

**The failure mode.** `/api/v1/similar` answers 200 with an empty list, filing
returns 201 for a verbatim duplicate, nothing logs, and `healthz` says ok — the
correct answer to "what matches this?" when you believe the index is populated.
The first time, the index had never been built: `embedbackfill -status` reported
0.0% on all four kinds, 0 vectors for 959 entities.

**It recurred, and that is the finding.** With the index built (1,918 rows,
`nomic`, 768 dims), complaint coverage still sat at **95.5%** — 25 complaints with
no vector, unfindable by similarity search, unreported by anything. Root cause:
**nothing in the deploy path or the service ever runs `embedbackfill`**, so the
gap reopens whenever a row is inserted by a path that skips the embedder. Builds
always looked fine, because writes embed on write and only *pre-existing* rows
need a backfill.

**Guard:** `cmd/concord/main.go` now measures coverage at startup and warns with
the counts and the exact command. A warning rather than a fatal error, on
purpose — the index makes duplicate detection work, and refusing to serve because
a cosine search came back empty would trade a real feature for a cosmetic one.
Empty kinds are skipped so a fresh install is not scolded for nothing.

Live instance now reads **100.0%** on all four kinds.

**Reading a coverage number — the trap that cost an hour.** Run
`embedbackfill` under the same `CONCORD_EMBED_*` environment the service uses.
The systemd unit sets `CONCORD_EMBED_URL` to ollama (`nomic`); a bare shell falls
back to `hashed-v1`. Coverage is reported per `model_id`, so the status command
reported **0.0% on a database that was 95.5% full**. The tool was correct and the
invocation was wrong, which is the most expensive kind of wrong because every
reading taken afterwards is also wrong.

**The denominator bug that would have made the guard useless.** Backfill skips a
row whose text is empty, on purpose; coverage counted those rows. Complaint 24 has
an empty title AND body, so coverage read 99.8% forever and the new warning would
have fired on *every deploy* until the operator learned to ignore it. **A warning
that always fires is worse than no warning, because it teaches the reader that
warnings here are noise.** `total` now excludes unembeddable rows using the
writer's own `TrimSpace` predicate, and reports the count as `unembeddable`.
Gated by a test that can be made to fail (dropping the exclusion, or reporting
the count as 0, both killed).

**Writes are unaffected** — a row created after the embedder is configured is
embedded on write. Only pre-existing rows need the backfill.

## Readers were pointed at a superseded spec (fixed 2026-10-05, `scripts/check_spec_drift.py`)

**Was:** `docs/concord-spec.md` is two revisions behind
`docs/concord-spec-r4.md`, and that was never the real problem. The real problem
was that **seven other documents cited it as authority**: `frontend-spec.md`
three times ("`concord-spec.md` §9.4 names ... as the stack"), `PREMISE.md`
("read `concord-spec.md` and join in"), `PLAN.md` twice, `PLAN-r4.md` and
`HANDOFF.md` once each. A reader following any of those implements from a spec two
revisions back and has no way to tell, because the file looks exactly as
authoritative as its replacement.

KNOWN-ISSUES had carried this for revisions. That is the finding worth keeping:
**a doc-rot finding that only lives in a markdown list is a note, not a control**,
because acting on it depends on someone reading that list — which is the same
dependency the original problem had.

**Now:**
- `docs/concord-spec.md` opens with a banner naming the replacement, saying not to
  implement from it, and saying why it is retained rather than deleted (older
  notes cite it by section number, and silently repointing them would make those
  citations wrong in a way nothing could detect).
- All six citations re-pointed at `concord-spec-r4.md`.
- `scripts/check_spec_drift.py`, wired into `make verify`, fails when a superseded
  spec has no banner or when any document cites a superseded spec normatively.

**The gate is not idle by default, and says so.** It reports
`checked 0 normative citation(s)` with an explicit note that if the docs ever stop
naming the old spec at all, the gate has nothing left to guard. A check that
silently passes because its search pattern stopped matching is the failure mode
this repo keeps hitting.

Two mutations of the gate itself were applied and both caught: removing the
banner, and re-introducing a misdirected citation in `PREMISE.md`.

**Deliberately not done:** rewriting the body of `concord-spec.md`. It is 26KB of
superseded prose and the honest options are delete-it or leave-it, and deleting it
would break every historical link. The banner is the reversible choice.

## `docs/HANDOFF.md` and `docs/PLAN-r4.md` counted work instead of measuring it (gated 2026-10-05)

**Was:** `HANDOFF.md` claimed 62 tests, 12 store files, and listed arenas and
solutions as unbuilt — 15 milestones after it was written. `PLAN-r4.md` marked R1–R4
"pending" with the state table still saying arenas were "missing, the spec's central
abstraction". Both were updated 2026-10-02 against the running instance.

**Then it happened again, silently, and that is the finding.** Measured 2026-10-05:
`HANDOFF.md` and `PLAN.md` both still quoted the *then-current* schema, table,
migration and test counts — all four wrong, by six schema versions and several
hundred tests — while the tree had moved on. Nothing failed, because nothing read
the docs, which is the structural half of this entry and the half that was unfixed.

> Written without the literal figures on purpose: this gate fails on a document
> stating a measured count that disagrees with the tree, and a sentence QUOTING a
> stale count trips it. The exemption is a markdown table row and nothing else —
> see `is_historical` — because the alternative (exempting prose that mentions
> "was") silently exempted every line in `docs/`.

**Now gated:** `scripts/check_doc_numbers.py` fails when a document states a schema
version, migration count, table count or test count that disagrees with the tree.
Wired into `make verify` via `docs-check`, with its own 8 tests in
`scripts/test_check_doc_numbers.py`. It found two more stale lines on its first
honest run (PLAN-r4's status header) beyond the two above.

### The gate was decorative on its first run, twice

Worth recording, because the failure is the same shape as the thing it guards:

- **It exempted every line containing the word "was"**, on the theory that such a
  line was quoting a past state. Every status line in `docs/` contains "was", so it
  examined **0 numbers**, printed `OK 0 measured number(s)` and exited 0. A gate
  that reports OK without examining anything is worse than no gate: it converts a
  known risk into a false assurance.
- **A slice-based edit left a second definition of its check-builder after
  `main`**, and Python binds the last one, so the call site used a different
  contract than the checks were built for. The same run, the same green.

Both are now closed structurally: `is_historical` matches only a markdown table row,
and a run that examines zero numbers returns **1**, not 0.

### A gate that flags correct subset counts is worse than one that flags none

The first honest run reported `KNOWN-ISSUES.md: 0008 rebuilds 23 tables` as stale,
three times. Those are **correct** — 23 is the number of tables migration `0008`
rebuilds, a subset of the 79.

So the table pattern is narrowed to a claim about the whole schema ("the 79 tables").
Narrowing a gate after it cries wolf is normally how you end up with a gate that
catches nothing, so the narrowing is constrained two ways: each subset phrasing in
`docs/` is named in a negative lookbehind, and `test_a_subset_count_is_not_a_finding`
asserts both ("rebuilds 23", "across all 23") stay unflagged. A **new** subset
phrasing therefore arrives as a finding to classify, not as noise to ignore.

## Arena `is_baseline` had no schema guarantee — the invariant lived only in Go (fixed 2026-10-05, `0025`)

**Was:** §6.3's "every solution arena has a permanent baseline" was enforced in
exactly one place — `setBaseline` in Go. No CHECK, no trigger, no index.

**Understated, twice.** The note said "Go-enforced", which reads like a weaker
guarantee rather than a missing one. Measured:

```
INSERT (arena 1, solution 5, is_baseline 1)
INSERT (arena 1, solution 6, is_baseline 1)   -- accepted: TWO baselines
```

**Two baselines is an ambiguity. ZERO is the real hazard,** and it is reachable
*inside the store*: `setBaseline` clears every baseline and then sets one, in two
statements with no transaction between them — and `CreateSolution` runs outside any
transaction at all. The state in that window is an arena with no baseline, where
`neither` has nothing to lose to and reads as a 0.5/0.5 draw instead of a loss to
doing nothing. That is precisely the bug `arenas.baseline_entry_id` caused before
`0018` dropped it, and this column is now the only representation of that fact.

**Fixed** by `0025`: a partial unique index,
`CREATE UNIQUE INDEX ... ON arena_entries(arena_id) WHERE is_baseline = 1`. A CHECK
cannot express "at most one" at all — it cannot see other rows.

### A behavioural test cannot tell a partial index from a catastrophic one

`UNIQUE (arena_id)` refuses a second baseline **exactly as well** — while capping
every arena at ONE COMPETITOR. `TestArenaRefusesASecondBaseline` passes for both.

`sqlite_master` has no `partial` column, so the index's own `sql` TEXT is what
distinguishes them, and `TestArenaBaselineIndexIsPartial` reads it. Mutating the
index to non-partial is killed only by that test; the behavioural one stays green.

### Three wrong versions of the test before one that could fail

All three are recorded because the shape recurs:

1. **Reused the baseline's own `entity_id`**, so the composite PRIMARY KEY
   `(arena_id, entity_type, entity_id)` refused the row and the test passed proving
   nothing about `is_baseline`.
2. **Asked `CreateSolution` for another solution** — which returned the SAME
   `entity_id`, because the arena entry already existed. The PK refused again.
3. **Asserted only `err != nil`**, so any constraint at all satisfied it.

A guard satisfied by an unrelated constraint is decoration: it reports the invariant
holds while leaving it untested. The working version uses an `entity_id` nothing
could have generated, and asserts on the error TEXT — SQLite names the indexed
COLUMN, not the index, so the message is necessary but not sufficient, which is
why the partial-shape test has to exist separately.

Verified on a copy of the live database with a seeded 7-entry arena: a second
baseline is refused, the store's demote-then-set still works, and 6 ordinary
competitors are unaffected.

## The finder suite's wait_for_timeout races — SUPERSEDED, see the section below

**Closed.** This entry recorded the third instance of one class (recorded
2026-10-03) and listed two concrete fixes, with the note that they were
"deliberately not applied" because a suite whose flakes get patched by whoever
happened to run them last stops being one person's problem and becomes everyone's.

That reasoning was right about ownership and wrong about the work: the fixes were
applied as one sweep against the whole class on 2026-10-05, not as two
drive-by patches. **22 sleeps -> 3, all three justified, 0 racy**, plus
`scripts/check_e2e_sync.py` in `make verify` so the class cannot regrow
unnoticed. `finder_e2e.py` also went 43s -> 9s.

Both fixes proposed here were the right ones. Kept because the reasoning about
*why* it was deferred is the part worth not losing: a flake fix belongs with the
work that caused the flake, not with the run that happened to trip over it.


## The e2e suites synchronised on a clock, not on state (fixed 2026-10-05)

**Was:** 22 `page.wait_for_timeout(...)` calls standing in for
synchronisation. Twenty were in `finder_e2e.py`. The pattern is: act, sleep,
read, assert on the read.

**Why that is a bug and not a style preference.** A sleep passes when the machine
is fast and fails when it is busy. That is precisely why it presents as an
intermittent flake nobody can reproduce — `test_escape_goes_back` was recorded
here as "fails ~1 run in 3" and did not reproduce in nine consecutive runs, nor
under 8x artificial CPU load. A race that vanishes when you measure it on an idle
box is still a race; it just needs the suite busy to show itself.

**Now: 3 sleeps remain, all justified, and 0 racy.**

| was | now |
|---|---|
| `answer_first()` slept 900ms after clicking an option | waits for the next question to render — and it is the shared helper most of the suite builds on, so its wait was load-bearing |
| 6 tests each did click-stop-then-sleep | one `show_results(page)` helper waiting for `#finder-results` visible **and** a ranked row present |
| `test_back_restores...` slept then read | waits for the summary to BE the original count, anchored so `12 matches` cannot satisfy a wait for `2 matches` |
| `.first.inner_text()` after a sleep | waits for the element to be visible first, so a too-early read cannot report "no fit percentage" when nothing had rendered |

**Two of the fixes would have passed vacuously, and that is why they are shaped
the way they are.** `test_back_restores...` waits for the *original* count to
return, not merely for a change — a summary that never moved cannot satisfy it.
And `test_no_fit_percentage_before_answers` now asserts `fits` is non-empty: it
reads via `all_inner_texts()`, which has no retry, so on an empty region it
returned `[]` and the loop passed over nothing.

**The three survivors are correct, and saying so matters:**

- `audit_e2e.py` — in a branch where the caller asserts nothing afterwards; every
  path that does assert goes through `wait_for_function`.
- `scout_e2e.py` — asserts the **absence** of a request. There is no state to
  wait for: "no fetch has arrived yet" and "no fetch will arrive" are identical
  until time passes. Removing this sleep would make the test pass more reliably
  while checking less.
- `finder_e2e.py` — the no-JS-errors test, where the same argument applies (an
  exception in a promise chain arrives after the DOM is already correct), plus a
  bounded 300ms.

**Gated, because a fix that lasts until the next person copies the pattern is not
a fix:** `scripts/check_e2e_sync.py`, in `make verify`. It classifies each sleep
as RACY / BOUNDED / IDLE rather than banning the call, so a justified exception
is a visible comment rather than a workaround, and it reports its own idleness.
Two mutations applied and caught: the original racy pattern reintroduced, and a
fresh unjustified sleep-then-read-then-assert.

**Proof the new waits still detect real breakage** — three mutations of
`finder.js`, all caught: answering does not record, the short-list count never
updates, and back does not restore.

**Side effect worth having:** `finder_e2e.py` went from 43s to 9s, because waits
are now on state rather than on a fixed delay. Fixing a race made the suite
nearly 5x faster.

