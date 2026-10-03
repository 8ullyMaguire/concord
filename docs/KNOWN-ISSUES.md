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

## `test_escape_goes_back` is flaky — roughly 1 run in 3

Found 2026-10-03 while verifying the §4.10 project panels. Pre-existing: the file
is untouched since `51f829a` and the failure reproduces with none of the panel
work in the tree.

```bash
for i in 1 2 3; do python3 -m pytest tests/e2e/finder_e2e.py -q; done
# 1 failed, 37 passed / 38 passed / 38 passed
```

The cause is in the test, not the finder. It reads
`#finder-shortlist-summary`'s text immediately after `start(page)`, and `start`
waits only for `#finder-flow` to be visible — the seed view is `show()`n before
the short list is populated, which is the same defect class as the `show()`
without `render()` bug this suite already documents. Pressing `1` then filters,
and the two reads disagree because the first one caught the short list mid-render.

The fix is a wait on the summary being populated before reading it, not a
`sleep`. Not applied here because it is the finder suite's own file and fixing it
belongs with the finder work rather than inside a commit about project panels —
and a test that only sometimes catches a defect should be fixed by the person who
owns the flow, not quietly patched by whoever trips over it.
