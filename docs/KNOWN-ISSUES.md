# Known issues

Defects confirmed by reproduction, with what was ruled out. Each entry states how
it was verified, because every one of these looked like something else first.

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

## `0008_project_delete_cascade.sql` breaks if applied after a later `ADD COLUMN`

`INSERT INTO charters_new SELECT * FROM charters` copies positionally. Once
`0011` has added `support_ratio_min`, the source has 19 columns and the target
18, and the migration fails:

```
table charters_new has 18 columns but 19 values were supplied
```

The runner applies migrations in version order, so this only bites when a
database is caught between the two — a partially-migrated backup, or a manual
replay. Naming the columns instead of `SELECT *` removes the coupling. Recorded
rather than fixed: the deployed database is past both, and rewriting a
historical migration changes what a fresh install does.

## An unbuilt embedding index is indistinguishable from working duplicate detection

Not a bug in the code — every semantic feature was implemented and correct. The
index had simply never been built: `embedbackfill -status` reported 0.0% on all
four kinds, 0 vectors for 959 entities.

The failure mode is quiet. Filing returned 201 for a verbatim copy of a
complaint that existed in the same project, with a well-formed
`{"similar": []}` body and no error. Nothing logs, nothing warns, and
`/api/v1/similar` answers 200 with an empty result — which is the correct answer
to "what matches this?" when you believe the index is populated.

Diagnosing it means asking a question the response shape cannot answer: `select
count(*) from embeddings`. There are now 1918 rows for 959 entities (two fields
per entity: title and full text), one `model_id`, 768 dims throughout.

Writes are unaffected — a row created after the embedder is configured is
embedded on write. Only pre-existing rows need the backfill, which is why this
survived so long in a repo that was being actively developed: new work always
looked fine.

## `docs/concord-spec.md` is two revisions stale

It is a verbatim copy of the master, and revision 4 replaced the master. Nothing
enforces that the copy tracks it, so the file that most readers open first is
the one that is most wrong. `docs/concord-spec-r4.md` is current.

## `docs/HANDOFF.md` and `docs/PLAN-r4.md` counted work instead of measuring it

`HANDOFF.md` claimed 62 tests, 12 store files, and listed arenas and solutions as
unbuilt — 15 milestones after it was written. `PLAN-r4.md` marked R1–R4 "pending"
with the state table still saying arenas were "missing, the spec's central
abstraction". Both were updated 2026-10-02 against the running instance.

The failure is structural: a status table written once and never reconciled goes
stale silently, and nothing in `make verify` reads docs. Milestone rows now name
the migration that proves them.

## Arena `is_baseline` has no column-level guarantee

The "do nothing" baseline is identified by `arena_entries.is_baseline`, enforced
only in Go (`RemoveArenaEntry` refuses to clear it). A direct SQL `UPDATE` can
clear it, and there is no `CHECK` or trigger behind it. The
`arenas.baseline_entry_id` column exists for the same purpose and is populated
by nothing yet; it was the reason the `neither` outcome silently scored as a
draw before 2026-10-02. One representation should be authoritative and
constrained, not two with one unwritten.

## The finder suite has a class of `wait_for_timeout` races, not one flaky test

Recorded after the third instance appeared on 2026-10-03, and this entry
supersedes the narrower one below — same root cause, wider blast radius.

| Test | Symptom | Frequency seen |
|---|---|---|
| `test_escape_goes_back` | **FIXED** — now waits on the summary text, not a sleep; 2/2 mutants killed | was ~1 run in 3, did not reproduce in 9 runs |
| `test_results_show_the_answers_that_produced_them` | `to_have_count(1)` on `#finder-results-answers li` fails | ~1 run in 5 |

The cause is the same in both: a fixed `page.wait_for_timeout(800)` standing in
for a wait on the thing the test actually asserts. The finder is a mount point
that paints instantly and fills in after fetches, so 800ms is a guess about
network latency, and every such guess is a flake waiting for a slow run.

The fix is a wait on the element or its content, not a longer sleep. Two
concretely:

```python
# before
page.wait_for_timeout(800)
before = page.locator("#finder-shortlist-summary").inner_text()

# after
expect(page.locator("#finder-shortlist-summary")).to_contain_text("matches")
before = page.locator("#finder-shortlist-summary").inner_text()
```

and for the answers list, wait for the first `li` to exist rather than for a
count to settle:

```python
expect(page.locator("#finder-results-answers li").first).to_be_visible(timeout=10000)
```

Deliberately not applied in the commits that tripped over these. Both are the
finder's own tests, both were green on the runs that followed, and a suite that
gets its flakes patched by whoever happened to run them last stops being the
finder's problem and starts being everyone's. It belongs with the finder work.

### A note on running `make verify` and `make gates` together

`make gates` mutates `internal/httpapi/finder.go` in place while it runs. Running
it in the same shell as `make verify` produces a `TestWhyThisQuestionDescribes-
TheSplitNotOneOption` failure that does not reproduce when either runs alone:
the test reads source that is mid-mutation. It cost a diagnostic cycle on
2026-10-03 and is the reason the two targets should be run serially, or from
different working trees.

## `test_escape_goes_back` was racy — fixed 2026-10-05 (was "roughly 1 run in 3")

**Was:** the test read `#finder-shortlist-summary` immediately after a fixed
`page.wait_for_timeout(800)`. A sleep is not a synchronisation: it passes when the
machine is fast and fails when it is busy, which is why this presented as an
intermittent failure and not as a bug.

**Measured before changing anything, because the file claimed ~1 run in 3 and I
had no reason to believe it:** 6/6 passing in isolation, then 3/3 full-suite runs
of all 38 finder tests green. Nine consecutive clean runs. The claim did not
reproduce — which is what a load-dependent race looks like on an idle machine, so
it is not evidence the race was never there.

**Now:** it waits on the state, not the clock.
`expect(summary).not_to_have_text(...)` after the `1` press, then
`expect(summary).to_have_text(...)` after Escape, both anchored on the exact count
prefix. The second assertion is what stops the test passing vacuously: it waits
for the ORIGINAL count to come back, so a summary that never changed cannot
satisfy it.

**And it can fail.** Both ways of breaking Escape were applied to `finder.js` and
both were caught, which is the only thing that distinguishes this from the sleep
it replaced:

| mutant | verdict |
|---|---|
| Escape does nothing (`goBack` removed from the handler) | KILLED |
| Escape advances instead of going back | KILLED |

`finder.js` was restored byte-identical and rebuilt afterwards — a mutation run
that leaves the binary stale makes every later browser test a false PASS.

**Still open, and not fixed here:** 22 other `wait_for_timeout` calls remain in
the e2e suites. This one was fixed because it was the recorded flake and had a
cheap, provable state to wait on, not because the pattern is now gone.