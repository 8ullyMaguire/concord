 `stats`, `stats/summary`, `search` done 2026-11-13 (`4f9a707`, `23c4910`); parity 0 disagreed of 14 checked, 2 declared; leaderboards, feeds, binaries and gates 4/5/47 open |# Plan — port Concord onto the estate profile

Implements `docs/specs/port-to-profile-spec.md`; the decisions are
`~/code/estate/docs/adr/0011-concord-joins-the-profile.md`. Read those two first.

**This file is the programme, not the whole plan.** M0 and S1 are specified to the line, so the
port can start today. S2–S5 are specified as gates and row lists, because each slice's file-level
plan is written when the slice starts — a four-hundred-step plan written in one sitting is wrong
in a hundred places by the time anyone reaches step fifty, and this repo has already paid that
bill twice (`docs/plans/milestone-consensus.md` records three invented symbols in one draft).

**Per-slice plan template** — every slice plan is these seven sections and nothing else:
1. tables and their PG types; 2. store functions with signatures; 3. routes and their handlers;
4. the corpus subset with its `expected_change` entries; 5. the Playwright suites that gate it;
6. the mutation arms (`cargo-mutants` scope); 7. the cutover step and its rollback.

Rules that will bite, from this repo's own record:

- `make verify` and `make gates` run **serially**, never in one shell (`gates` rewrites
  `internal/httpapi/finder.go` in place).
- Go and Rust live in one tree during the port. `go build ./...` and `cargo build` must both pass
  on every commit on `port-rust`; the Go tree is what serves traffic, the Rust tree is what is
  being proved.
- Adding tests to the **Go** tree moves the counts `scripts/check_doc_numbers.py` gates. Adding
  Rust tests does not (the script reads Go). Do not add prose test counts for Rust.
- The parity corpus is the only definition of "the same".

---

## Ledger — the only state list

Read this table FIRST every turn, before any code. A row is done when its gate ran and its output is
quoted below it, never because it merged. The count in this header is the number the turn report
quotes; `grep -cE '^\| [0-9]+ \|' docs/plans/port-to-profile.md` recounts it.

### Open *(3 — row 11 in progress; 12 and 13 not started)*

Rows 7, 8, 9, 9b and 10 are Done and stay in this table with their commits and gates, because a row is done when its
gate ran and its output is quoted — not when it was removed from a list. The header count says 3: 11, 12, 13.


| # | step | gate — the command that fails if the step is absent |
|---|---|---|
| 8 | S2 ideas, complaints, ranking | **Done 2026-11-13** (`0a32d49`) — corpus subset, `concord-ranking` suite and store tests green; every killable ranking mutation arm killed, the rest proved unobservable (see the dated entry below) |
| 9 | S3 consensus, governance, ledger | **Done 2026-11-13** (`a2ace5d`) — corpus subset green, `probe_s3_api.py` 94/94, estate gate 49 green; `consensus_e2e`/`audit_e2e` moved to row 12 by the dated correction |
| 9b | S3 arena and solutions | **Done 2026-11-13** (`345b132`) — `probe_s3b_arena.py` 20/20 (was 5/20), the negative control at 19/20, corpus still 2 disagreed of 14, ranking oracle green with 10 arena rows |
| 10 | S4 moderation, comments, inbox | **Done 2026-11-13** (`b969b26`): four probes 26/26, 27/27, 40/40, 39/39; `e2e.sh` 29 passed; gates 22 and 45 green (`CHECK inbox` exit 0, `CHECK authz` 10/10); `polaris-comments` and `polaris-authz` adopted |
| 11 | S5 stats, feeds, admin binaries | corpus + `stats_e2e` and feed parse; estate gates 4, 5, 47 green; timers migrated to `polaris-loops` |
| 12 | P6 the frontend on SvelteKit + `@polaris/ui` | every page its slice owns renders from the API with the server-rendered head, in a browser; **inherits `feature_e2e`, `feature_ui_mutants`, `milestones_e2e`, `rank_links_e2e`, and from row 9 `consensus_e2e` + `audit_e2e` — the last two FIRST need their fixtures moved from `sqlite3.connect` to `harness.connect`** |
| 13 | P7 cutover | `concord.polarisocial.xyz/api/v1/version` served by the Rust binary, and estate gate 17 green for `concord` with NO `reason` |

**Row 8 (S2) — the failing probe, pasted before any code** (2026-11-13):

```
$ python3 scripts/parity/record.py --seed scripts/parity/seed.txt --out crates/parity/corpus.json
17 shapes -> crates/parity/corpus.json
$ CANDIDATE=http://127.0.0.1:8107 concord-parity --corpus crates/parity/corpus.json
/api/v1/projects/concord/audit: status 404 != 200
/api/v1/projects/concord/complaints: status 404 != 200
/api/v1/projects/concord/consensus: status 404 != 200
/api/v1/projects/concord/features: status 404 != 200
/api/v1/projects/concord/features/:id: status 404 != 200
/api/v1/projects/concord/milestones: status 404 != 200
/api/v1/projects/concord/roadmap: status 404 != 200
/api/v1/search?q=arena: status 404 != 200
/api/v1/stats: status 404 != 200
/api/v1/stats/summary: status 404 != 200
10 shape(s) disagreed of 16 checked, 0 skipped
```

Ten routes, not one: S2 owns features, complaints and ranking; `audit`/`consensus` are S3, `stats` is
S5, `search` is S5's index. The corpus is now the whole recorded surface rather than S1's five shapes,
so each slice's gate is "its rows of this list go green" and the remaining rows are visible while it
works.

**The recording also exposed a disagreement that was the corpus's own fault.** `/api/v1/projects`
differed at `[43].health_score: 0.5081277429935345 vs 0.5134987204761251` — 1.05%, just outside the
tolerant tier. `project_metrics.health_score` is written by the Go service's background sweep, not
computed on read, so the migrated value and the live one diverge between the move and the replay. It
moved to the volatile tier (compared by type, so a string where a number belongs still fails) with the
reason in `crates/parity/src/main.rs`: the port reads the column and has no formula here to get wrong,
so a number comparison measures the sweep's clock, not the port.

**Row 8 (S2) — progress** (2026-11-13): four of its five owned shapes now match, and the remaining
five disagreements are all other slices'.

```
$ CANDIDATE=http://127.0.0.1:8107 concord-parity --corpus crates/parity/corpus.json
/api/v1/projects/concord/audit: status 404 != 200        # S3
/api/v1/projects/concord/consensus: status 404 != 200    # S3
/api/v1/search?q=arena: status 404 != 200                # S5
/api/v1/stats: status 404 != 200                         # S5
/api/v1/stats/summary: status 404 != 200                 # S5
5 shape(s) disagreed of 16 checked, 0 skipped            (from 10)
```

`/features`, `/features/:id`, `/complaints`, `/milestones` and `/roadmap` are byte-identical to the
recorded Go bodies. Four findings came out of it, three of them schema or ordering, one a serializer:

1. **`features.impact_ratio` was missing from the baseline.** It is a `GENERATED ALWAYS AS (…) VIRTUAL`
   column in SQLite, and `pragma table_info` does not list generated columns at all — only
   `table_xinfo` does, and without the expression. The port's first `/features` answered
   `column "impact_ratio" does not exist`. New generator rule **R12**: generated columns are read out
   of the CREATE TABLE text and carried across as `STORED` (PostgreSQL accepts the CASE arithmetic as
   written). One table has one (`features`); 2,766 rows carry a value.
2. **`order by created_at desc` is not enough to reproduce the list.** 16 pairs of features in the
   live data share a `created_at` to the second, and the recorded bodies show the smaller id first in
   **16 of 16** ties — SQLite returns rowid order inside a tie, PostgreSQL has no such order. The
   query now says `order by created_at desc, id asc`. Complaints have no tie in the current data, so
   the same clause there is recorded as an inference, not a measurement.
3. **Go prints a whole `float64` without a decimal point.** `elo_r: 350` came back as `350.0` from
   serde_json, and `elo_rd`, `elo_vol`, `strategic_weight`, `impact_ratio`, `frequency`,
   `strategic_multiplier` all carry the same rule. They use the same serializer the timestamps do.
4. **`impact`/`effort_score` are `bigint` in the baseline**, because R1 keeps SQLite `INTEGER`
   non-key columns wide; decoding them as `i32` fails at runtime with "mismatched types", not at
   build time. They are `i64` now, with the reason at the field.

Still open in S2: the ranking (`priorities`, arenas, Glicko2), `CreateFeature`/`UpdateFeatureStatus`
with the evidence gate and dependency refusal (`docs/REMAINING.md` §44/§46), `crates/ranking`, and the
`rank_*`/`milestones_e2e`/`feature_*` suites.

**Row 8 (S2) — the two rules, probed before they were ported** (2026-11-13). §44/§46 are the reason
this slice exists; before the change the candidate answered:

```
$ CANDIDATE=http://127.0.0.1:8107 scripts/parity/probe_s2_status.py
BAD file without a token: 405 (want 401) -> null
BAD file a feature: 405 (want 201) -> null
BAD ready without evidence: 404 (want 400) -> "no such route"
BAD the refusal names the evidence: 0 (want 1) -> "no such route"
BAD link a validated complaint: 404 (want 200) -> {"error": {"code": "not_found", ...}}
BAD ready with evidence: 404 (want 200) -> ...
BAD blocked feature reaches ready: 404 (want 200) -> ...
BAD in_progress with an open blocker: 404 (want 400) -> "no such route"
BAD the refusal names the blocker: 0 (want 1) -> "no such route"
BAD unlink the evidence after ready: 404 (want 200) -> ...
BAD in_progress after the evidence was removed: 404 (want 400) -> "no such route"
11 of 11 failed
probe exit=1
```

after:

```
ok  file without a token: 401 (want 401)
ok  file a feature: 201 (want 201)
ok  ready without evidence: 400 (want 400)
ok  the refusal names the evidence: 1 (want 1)
ok  link a validated complaint: 200 (want 200)
ok  ready with evidence: 200 (want 200)
ok  blocked feature reaches ready: 200 (want 200)
ok  in_progress with an open blocker: 400 (want 400)
ok  the refusal names the blocker: 1 (want 1)
ok  unlink the evidence after ready: 200 (want 200)
ok  in_progress after the evidence was removed: 400 (want 400)
0 of 11 failed
probe exit=0
```

`crates/db/src/feature_writes.rs` and four tests in `crates/db/tests/store.rs`; `scripts/parity/e2e.sh`
-> **26 passed, 0 failed**.

Decisions taken here, written down because the Go store leaves them implicit:

1. **Evidence is re-checked at `in_progress`, not only at `ready`.** `UnlinkComplaint` and
   `RemoveFeatureEvidence` delete rows and neither un-readies anything — that is deliberate, an edit
   must not silently undo a decision. The consequence is that the gate on `ready` alone has a hole:
   ready → unlink → start work. Both gates now read the same evidence count.
2. **"Open" means `status <> 'shipped'`.** A `review`ed or `rejected` blocker still blocks, because the
   Go `OpenBlockers` query says `f.status <> 'shipped'` and widening it here would let a rejected
   dependency silently unblock work.
3. **The intake-mode rule reads the project, not the instance.** A `complaint_first` project refuses a
   feature with no validated complaint; `idea_first`/`both` do not. Applying the rule globally would
   tell readers in `idea_first` projects to file complaints the project never asked for.
4. **Linking and filing accept only `validated` complaints.** A draft is a report nobody confirmed;
   counting it as evidence would make the gate measure effort, not agreement.
5. **`feature_ids` ordering is not invented**: `feature_complaints` is read `order by complaint_id`,
   because the table has no position column and a join's order is not a contract.

Also fixed in the harness by these tests: `PgSeed` appended `RETURNING id` to every INSERT, which fails
on a join table (`feature_deps` is keyed by its pair), and its VALUES span was matched as a suffix, so
an INSERT that already ended in `RETURNING id` got a placeholder and no argument.

**Correction, dated 2026-11-13 — row 8's gate named the wrong surface.** The gate said
"`rank_*`, `milestones_e2e`, `feature_*` green", and those three suites are browser suites: their
first assertion navigates to `/projects/{slug}` or `/projects/{slug}/roadmap` and waits for a CSS
class in the Go-rendered HTML. Measured before changing anything:

```
$ bash /tmp/run_e2e_suite.sh milestones_e2e
>       expect(column).to_be_visible(timeout=15000)
E       AssertionError: Locator expected to be visible
E         - Expect ".roadmap-column" first
E       Aria snapshot:
E         - text: "{\"error\":{\"code\":\"not_found\",\"message\":\"no such route\"}}"
```

That 404 is `/projects/concord/roadmap` — the HTML page, which no slice before row 12 serves. The
suites cannot pass against an API candidate, so listing them in row 8's gate made row 8 uncloseable
until the frontend existed, while telling nobody why. They move to row 12's gate, which is the row
that can make them pass; row 8's gate now names the API-level evidence it can actually produce.
`feature_e2e`: 0 API calls and page navigations to `/projects/...`; `rank_links_e2e`: registers one
account over HTTP and then asserts on `.rank-card` links; `milestones_e2e`: one API call, six page
navigations.

Also found by running them, and recorded because it is a port-wide risk rather than a suite's:

```
E  seed statement failed: insert or update on table "arenas" violates foreign key constraint
E  "fk_arenas_feature_id"
E  DETAIL:  Key (feature_id)=(0) is not present in table "features".
```

SQLite opened these databases with `PRAGMA foreign_keys=OFF` (the default), so a fixture could write a
sentinel `0` into `arenas.feature_id` and nothing complained; PostgreSQL enforces the constraint the
Go schema always declared. The **application** is unaffected — `internal/store/arenas.go` writes
`NULLIF(?, 0)`, and the moved data has 73 arenas with `feature_id IS NULL`, none with 0 — but every
fixture that writes a sentinel must write `NULL`, and any Rust route that creates an arena owes the
same `NULLIF`. This is the one place where the port is *stricter* than the service it replaces, which
is the right direction, and it only bites writes.

**Process note, dated 2026-11-13 — `cargo mutants --in-place` mutates the work tree you are
committing from.** The gate has to run `--in-place` (a `polaris-core` path dependency escapes
cargo-mutants' copied tree and the baseline fails in `/tmp`), and that means the tree carries a
mutated source file for the whole run. Commit `32e8d6d` was staged while a run was live and captured
`crates/ranking/src/lib.rs` with one flipped comparison in it:

```
-        while f(big_a - k * tau) < 0.0 {
+        while f(big_a - k * tau) > /* ~ changed by cargo-mutants ~ */ 0.0 {
```

Fixed in `b3e826b`, and the gap that let it live is closed by a test rather than by care: the bracket
search only runs on one branch, and every assertion around it was either the published example
rounded to two decimals or a monotonicity check. `the_full_precision_reference_values` asserts four
`rate()` calls to 1e-9 against an independent Python transliteration, which this mutation cannot
survive. Rules from here: the tree is clean before a run starts, nothing is edited while it runs,
`git status` is checked after, and a killed run is followed by `git checkout -- ` on every mutated
path — `git checkout` restores from HEAD, so it cannot fix a mutation that a commit already captured.

**Row 9 (S3) — the read side landed, the write side has not** (2026-11-13). Corpus, before and after:

```
before: 4 shape(s) disagreed of 14 checked, 3 skipped   (audit 404, consensus 404, search, stats/summary)
after:  2 shape(s) disagreed of 14 checked, 3 skipped   (search, stats/summary — both S5)
scripts/parity/e2e.sh -> 29 passed, 0 failed
```

`/api/v1/projects/concord/audit` now matches the recorded body row for row — 50 entries, `total` 9073,
`undone`, `can_undo`, `actor_display_name` — and `/consensus` matches its `[]`. Landed in `f49d333`:
`crates/db/src/audit.rs`, `crates/db/src/consensus.rs`, `governance_model`, and
`crates/governance` (the decision rules themselves, from the previous commit).

Five rules carried across rather than re-derived, each because the obvious version produced a plausible
page: the audit page's **two** clamps (an absent limit is 50, a limit over the ceiling is the ceiling —
folding them silently answers 50 to a caller who asked for 200); no `LIKE` at all for an empty search;
wildcards escaped backslash-first with `ESCAPE`; the total counted before the rows are read; and §6.6's
**hidden tally enforced on the server** — an open call returns counts and ratios as `null` while
`participants`, `eligible` and `quorum_required` stay visible, and positions are reduced to the caller's
own, because a client-side rule is bypassed by devtools.

`eligible` is computed from §8.2 (role **and** a qualifying contribution inside 365 days) and not
assumed: `quorum_threshold(0)` is 0 and the quorum check is `participants < threshold`, so an unset
eligible makes every call quorate by definition — one member consenting to their own call was enough to
accept it.

Two findings, both from writing the fixtures:

1. **A process decision's `feature_id` must be `NULL`, not `0`.** PostgreSQL enforces
   `fk_consensus_calls_feature_id`; SQLite never enforced anything (`PRAGMA foreign_keys=OFF`). The
   application already writes `NULLIF(?, 0)`, so production is unaffected — but every fixture that
   writes a sentinel must write `NULL`, and every Rust route that creates one owes the same `NULLIF`.
2. **An audit total is a count over the whole log, not over the page**, and the project's own
   `create_project`/`create_feature` rows live in it — a fixture must read the delta.

Still open in row 9: the consensus **writes** (`POST /consensus`, `/{id}/position`, `/{id}/objection`,
`/{id}/close`, `/{id}/override`, `/{id}/outcome`, `/{id}/hold`, `PUT /objections/{id}/resolve`), the
`consensus_*` and `audit_e2e` suites, and estate gate 49.

**Row 9 (S3) — the writes landed; the row is still open on the browser suites** (2026-11-13, `699959f`).
`scripts/parity/probe_s3_api.py`:

```
27/27 ok, exit 0
negative control (tally forced always-visible): 24/27 ok
  FAIL tally_visible is false: got True want False
  FAIL an open call hides its counts / ...and its ratios
```

The control matters more than the green run: without it, "the tally is hidden" is a claim about a
response nobody tried to break.

What landed: `create_call` (phase gate, one-open-call gate, evidence gate), `cast_position`,
`create_objection`, `resolve_objection`, `close_call`, and `crates/db/src/charter.rs` — the verdict now
reads the project's **own** charter rather than the governance model's defaults, which is the difference
between judging a call by the number the page publishes and judging it by the number the model ships.

The rule the deployed instance lost, and the number that found it: **opening a call moves the feature to
`consensus`**. 87 features sit at `status='consensus'` with zero `consensus_calls` rows, while a feature
with a real call sat in `discussion` for the whole decision — the board's consensus column named a phase
nothing was in. `consensus` is in the openable set for the same reason: refusing it made the only route
that starts a call unreachable for every one of those 87.

Four strictness findings, all sites where SQLite enforced nothing and PostgreSQL does:
`members.joined_at` is `NOT NULL` with no default; a project delete must go children-first
(positions/objections/holds/calls/features/complaints/columns/members); the e2e harness's seed-failure
handler raised `UnboundLocalError` from inside the handler and buried the real error; and `pkill -f` on
the candidate's command line matches the shell running it — `pgrep -x concord-cli` costs one line and
saves the session.

Still open in row 9: `POST /{id}/override`, `/{id}/outcome`, `/{id}/hold`, and the `consensus_*` plus
`audit_e2e` browser suites, then estate gate 49.

**Row 9 (S3) — §6.6's override window landed** (2026-11-13, `edd156e`). `scripts/parity/probe_s3_api.py`:

```
46/46 ok, exit 0                       (was 27/27; 19 new assertions)
negative control (open window publishes its tally): 44/46 ok
  FAIL an open window hides its counts: got 0 want None
  FAIL ...and still hides the counts: got 1 want None
```

`crates/db/src/override_window.rs` plus four routes: `GET`/`POST /{call}/override`,
`/override/position`, `/override/close`. Rules carried across: a window opens only on a call that closed
`blocked` (and names the actual status and result when it did not); only while an objection is **open**,
since a withdrawn block was already resolved by §6.6's other branch; **one window ever**, where
`UNIQUE(call_id)` is the refusal and `23505` becomes a 409; below quorum there is **no verdict at all**
(recording "upheld" would claim the room decided when most of it did not vote); an override requires the
objector's remedy **answered in writing**, checked before any write because the verdict is what makes it
required; and a **held call's window cannot resolve it**, or the hold becomes a pause you can vote around.
On `overridden` the call's own `result` becomes `accepted_overridden` in the same transaction — the write
that makes the window a decision rather than a poll.

The hidden tally lives in the **reader** (`fill_tally`), not in each handler, so no route can publish a
live ballot by forgetting to strip it. That is why the negative control had to flip the reader rather
than a handler.

Two fixture findings. A probe delete must go children-first through `override_positions` and
`override_windows` too (`fk_override_windows_call_id`) — invisible on a first run, fatal on the second. And
"a window opens only on a blocked call" was asserted against call 1, which closes `blocked` *because the
probe filed an objection on it*: a 201 there is correct behaviour and the assertion was wrong. It now
names a call that closed `rejected`, and a blocked call whose objection was withdrawn gets its own refusal.

**Row 8 — the mutation gate's survivor count, and one proof that a survivor cannot be killed**
(2026-11-13, `629bdcc` + `0a32d49`). The isolated run went from **99 missed to 27** once the oracle existed,
then four more classes were closed with the case each one was missing. One class refuses to die and the
reason is structural, so it is recorded here rather than chased again: in `rate`'s regula-falsi `else`
branch, `delta^2 <= phi^2 + v` bounds `delta^2` at roughly 8, while `x = ln(sigma^2) - tau <= -0.5` for
any `sigma <= 1` gives `x^2 >= 0.25`; the first probe's polynomial term `x^2 - delta^2 - phi^2 - e^x`
therefore stays positive and the linear term adds `+1/tau`, so **the `while` body is never entered** and
mutants inside it cannot change an answer. Verified by hand: applying `k * tau -> k + tau` and running the
suite leaves it green. Applied to the ledger as an equivalent mutant, like `place_feature`'s duplicate
guard in row 7.

**Row 8 Done — 2026-11-13, `0a32d49`.** The three halves of the gate:

```
CANDIDATE=http://127.0.0.1:8107 concord-parity --corpus crates/parity/corpus.json
  2 shape(s) disagreed of 14 checked, 3 skipped        (search, stats/summary — S5's, not S2's)
scripts/parity/e2e.sh -> test result: ok. 29 passed; 0 failed
cargo test -p concord-ranking -p concord-governance -> 26 passed, 0 failed
cargo mutants -p concord-ranking -p concord-governance (isolated copy, clean target)
  461 mutants tested in 8m: 20 missed, 427 caught, 10 unviable, 4 timeouts
```

From **99 missed to 20**, and the 20 are accounted for rather than hoped away:

- **19 are inside `rate`'s volatility solver** (lines 92–122: the bracket test, the Illinois sign test,
  the convergence tolerance, the stepping accumulator). All of them change the *path* to the root, not the
  root: `rate` returns the fixed point of `f`, and any valid bracket converges there. Demonstrated, not
  asserted — four representative mutations were applied by hand and each left **all 393 oracle values**
  inside the comparison band:

  ```
  bracket: phi*phi -> phi+phi        -> PASSES (mutant survives)
  sign test: f_c*f_b -> f_c/f_b      -> PASSES (mutant survives)
  tolerance: > 1e-6 -> >= 1e-6       -> PASSES (mutant survives)
  accumulator: += -> -=              -> PASSES (mutant survives)
  ```

  The band is 1e-9 relative and cannot honestly be tightened: with exact equality the *unmutated* port
  already disagrees with Go at ~1e-16 (`5.99776102662121155e-2` vs `5.99776102662121086e-2`), because
  `exp`/`ln` are not required to be correctly rounded. A mutant that moves a value by less than the two
  languages' own floating-point disagreement is not observable through this oracle at any precision the
  two implementations can share.
- **1 is `age_gate_multiplier`'s `<` -> `<=`**, equivalent for the same reason the whole function is a
  clamp: at age 0 both branches assign 0, and no other age reaches the branch.
- **4 timeouts are all in `rate`** — mutants that make the solver non-terminating (`-` -> `/` in the
  bracket, `<` -> `>` in the sign test). The suite does detect them; it detects them by hanging, which
  cargo-mutants reports in its own column.
- **10 unviable** are mutants the compiler refuses, which proves nothing either way.

Two tooling traps found on the way, both recorded in `scripts/parity/mut_gate.sh`:

1. **`rsync -a` preserves mtimes and cargo fingerprints on mtimes**, so a target dir shared with an
   earlier copy looked `Fresh` for files that had changed and the gate tested a binary from a tree that no
   longer existed. `a_deadline_freezes_then_expires` "failed" on a source that says `+ 1`. Fixed by
   `rm -rf $CARGO_TARGET_DIR` plus `touch` after the copy — a gate whose baseline has never once been run
   on a clean tree is not a gate.
2. **A finished `--in-place` run leaves its own artifacts behind**, so a manual `cargo test` in the copy
   straight afterwards can execute a mutated binary. The copy's target dir is disposable; treat it as such.

And one process rule the row taught: **a probe or an oracle that has only ever been green is not
evidence.** Every rule in this slice was shipped with a control run that made it fail first — the hidden
tally (44/46 with the reader forced visible), the hold (60/62 with the close not consulting it), and the
one survivor that had to be applied by hand before it could be called equivalent.

**Row 9 (S3) — §6.5's outcomes, the decision record, the charter and the hold** (2026-11-13, `a2ace5d`,
`69c3c68`, `bc90fd8`). The probe is at **94/94**:

```
scripts/parity/probe_s3_api.py -> 94/94 ok, exit 0
negative control (guard restored to `status == "closed"`, as the Go store had it): 88/94
  FAIL record the outcome: got 409 want 200  (call 55 was already decided: )
```

That control is the whole point of the entry: the Go store guarded the decision record on
`status != "open"`, so a call the page had just closed could never record its outcome. Six assertions fail
under the old guard and all 94 pass under the new one, which is the difference between a rule and a comment.

Landed: `POST /{call}/outcome` (five outcomes, each moving both the solution and the feature; "sent-back"
returns the feature to discussion rather than rejecting it), the `adr` decision record carrying positions,
objections with remedies, and the solutions' final states; `GET /projects/{id}/charter`; `GET`/`POST
/{call}/hold` with the narrow reading of §6.6 — suspend, never override, written reason, stated ground,
refused above the ceiling rather than clamped; and `GET`/`POST /{call}/override` with four refusals.

One deliberate gap, recorded rather than papered over: the decision record's table is **not** ordered by
§6.4's score, because `solutions` carries no rating and the score comes from the feature's arena
(`arena_entries`, coverage, kappa, distinct voters). Reproducing that here would be a second implementation
of the arena's arithmetic — the arena slice's job. The record says where the order lives instead of printing
a column that does not exist.

Still open in row 9: `GET /{call}/fallback`, the `consensus_*` and `audit_e2e` browser suites, and estate
gate 49.

The probe's delete order has now been wrong three times in a row, each time on the second run rather than
the first — `override_positions`/`override_windows`, then `admin_ledger`/`emergency_holds`, then
`solutions` (which must go *after* `consensus_calls`, because the call references the solution). A probe
that deletes children-first by hand is a probe that will be wrong again; the fixtures now list every child
table in dependency order in one block.

**Correction, dated 2026-11-13 — row 9's gate named two suites that cannot run against an API candidate.**
Measured before changing anything, with the candidate serving the e2e database:

```
$ bash /tmp/run_e2e_suite.sh consensus_e2e
>       cur.execute(
            "INSERT INTO capabilities (key, label, category, kind, \"values\","
            " created_at) VALUES (?,?,?,'boolean',?,?)", ...)
E       sqlite3.OperationalError: no such table: capabilities
tests/e2e/project_panels_e2e.py:141: OperationalError
ERROR tests/e2e/consensus_e2e.py::test_the_consensus_page_renders_a_call

$ bash /tmp/run_e2e_suite.sh audit_e2e
>       owner = cur.execute(
            "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)", ...)
E       sqlite3.OperationalError: no such table: users
tests/e2e/audit_e2e.py:53: OperationalError
ERROR tests/e2e/audit_e2e.py::test_the_audit_page_lists_the_seeded_entries
```

Two obstacles, and the first one is worse than row 8's: these suites never reach a page. They call
`sqlite3.connect` directly instead of `harness.connect`, so against the PostgreSQL e2e database they open an
empty SQLite file and every seed statement fails on the first table. Row 8's suites at least had a
harness-aware fixture and failed at the page. The second obstacle is the same as row 8's: their assertions
navigate to Go-rendered HTML (`/projects/{slug}/consensus`, `/projects/{slug}/audit`), which no slice before
row 12 serves.

So the two suites move to **row 12's gate**, which is the row that can make them pass, and row 12 inherits a
second job it did not have before: `consensus_e2e` and `audit_e2e` must be converted from `sqlite3.connect`
to `harness.connect` before their page assertions mean anything. Row 9's gate now names the API-level
evidence it can actually produce: the corpus subset and `probe_s3_api.py`.

**Row 9 Done — 2026-11-13, `a2ace5d`.** The gate, in the form the dated correction left it:

```
CANDIDATE=http://127.0.0.1:8107 concord-parity --corpus crates/parity/corpus.json
  2 shape(s) disagreed of 14 checked, 3 skipped        (/search, /stats/summary — S5's)
scripts/parity/probe_s3_api.py -> 94/94 ok, exit 0
bash scripts/check-ledger.sh -> CHECK ledger: 8 arm(s) passed, 0 failed   (gate 49, exit 0)
```

What the slice contains: the audit log's reader (two clamps, escaped LIKE, count before rows, NULL detail as
""), the consensus index and detail with §6.6's hidden tally enforced server-side, the five writes that open,
vote, object, close and decide a call, §6.5's decision record as an `adr` project document, §6.6's override
window with its four refusals, and the emergency hold in its narrow reading. Four controls were run against
those rules and each one made the suite fail first; the entries above quote them.

Two things this row leaves open, both named rather than buried:

1. **Three routes that need §6.4's arena.** `GET`/`POST /{feature}/solution-call` (readiness and open) and
   `GET /{call}/fallback` all read the arena: `FindArena`, `arena_entries`, the leaderboard's conservative
   rating, coverage, kappa and distinct voters. Returning a half-filled `SolutionScore` would be a lie in a
   response body, so they are not written — and because row 9's gate never named them, a port could have
   been called done with three 404s where the Go service answers. They get their own row, **9b**, whose gate
   is a probe that fails on all three before the change.
2. **Gate 49 will need editing at cutover.** One of its eight arms reads "concord's trigger refuses the same
   rows the crate refuses (conformance, not adoption — concord is Go)". That sentence stops being true the
   moment row 13 serves the Rust binary, and the gate does not fail when a comment goes stale.

**Row 9b Done — 2026-11-13, `345b132`.** The gate:

```
bash /tmp/run_probe.sh probe_s3b_arena.py   -> 20/20 ok, exit 0            (was 5/20)
negative control, the fallback filter removed -> 19/20 ok
  FAIL the call's own solution is excluded: got 1 want 0  ([29, 30, 31])
bash /tmp/run_probe.sh probe_s3_api.py      -> 94/94 ok, exit 0            (no regression)
CANDIDATE=http://127.0.0.1:8108 concord-parity -> 2 shape(s) disagreed of 14 checked, 3 skipped
cargo test -p concord-ranking               -> golden suite green, 10 new arena rows
```

`crates/db/src/arena.rs` carries every number the three routes need: the leaderboard ordered on `r - 2*RD`
(so an entry with no games cannot outrank a well-attested one on the strength of not having lost yet), the
charter's coverage weight with §6.4's default, distinct voters, stability hours, coverage computed from the
feature's real pain through the project's one definition of it, the claims, the ranking with the baseline
sorted last, and §8.2's eligible collaborators as role AND activity — the conjunction, because counting
roles alone makes an inactive maintainer eligible and counting activity alone makes an active guest eligible.

`crates/ranking` gained `conservative` and `win_probability`, **and the oracle grew with them**: ten arena
rows added to `golden_ranking.go`, regenerated from the Go functions still serving production, replayed at
1e-9. Row 8's lesson, applied without being told: a number this port invented is a number it can be wrong
about in the same direction and agree with itself about. The golden table is now 166 rows.

Four defects that only running found:

1. `arena_entries.is_baseline` and `solution_coverage.contested` are **real booleans** in the generated
   baseline (the generator promotes 0/1 columns whose names say boolean), so decoding them as `i64` was a
   500 on the first readiness read.
2. The eligibility window compared a `timestamptz` to a `double precision` — `operator does not exist:
   timestamp with time zone >= double precision`. The Go store compares numbers because SQLite stores
   numbers; PostgreSQL needs `extract(epoch from ...)`. The same class as R1 and R12, a fourth time.
3. `pairwise_votes.voter_id REFERENCES users`, which SQLite never enforced (`PRAGMA foreign_keys=OFF`), so
   the fixture's synthetic voter ids were refused. The constraint doing its job; the fixture was wrong.
4. The probe's delete list has now been wrong **five times**, every time on the second run and never the
   first, because the first run creates exactly the rows the second run cannot delete. It walks the FK graph
   now, deepest first, with every child table named — `pairwise_votes`, `arena_entries`, `arenas`,
   `positions`, `consensus_calls`, `solution_coverage`, `solutions`, `feature_complaints`, `features`,
   `complaints`, `members`.

One thing the row leaves deliberately undone: the `solutions` list route (`GET /{feature}/solutions`) is
still not exposed even though `list_solutions` now exists, because no ledger row names it and adding product
surface during a port is what the invariants forbid. It is one route over a function that now works, and it
belongs to whichever row first needs the page that reads it.

**Correction, dated 2026-11-13 — the baseline was missing a VIEW, and the probe database could drift.**
Found by row 10, not by reading the migrations:

```
$ curl -s -o /dev/null -w "%{http_code}" https://concord.polarisocial.xyz/api/v1/curation
200
$ sqlite3 ~/.local/share/concord/concord.db \
    "select name from sqlite_master where name like '%curat%'"
(nothing — curation_votes is a VIEW)
# the Rust candidate: relation "curation_votes" does not exist
```

Migration `0035_curation_reviews.sql` creates `curation_votes` as a **VIEW** over `taxonomy_consents`,
`strategic_weight_consents` and `act_reviews` — deliberately, because a table would be a second place to fall
out of step with a ratification. `scripts/parity/ddl.py` read `type='table'` and stopped there, so the view
never reached PostgreSQL and the queue's own query named a relation that did not exist.

**R13: views are part of the schema.** Views are emitted after the tables and before the foreign keys, with
the `CREATE VIEW … AS` prefix anchored rather than the first ` as ` in the text — splitting on that turned
`select 'taxonomy' as kind` into the view's body and produced SQL that parses as far as the keyword and then
means nothing. Bodies that use `IFNULL` or `GROUP_CONCAT` get a note instead of a guess.

Two consequences recorded because they are the same family as R1 and R12 — *the schema is not what the
baseline says*:

1. `run_probe.sh` now **drops and rebuilds** `concord_e2e` from `crates/db/sql/0001_baseline.sql` on every
   run. It used to create the database once, so anything added to the baseline afterwards was silently
   absent from the database the probes tested against. That is exactly how the hour above was spent: the
   file had the view, the database did not, and nothing said so.
2. A probe that writes to a view is a probe that lies about the schema. The curation fixture now writes the
   consent row the view projects.

Baseline regenerated from the live SQLite on thinkcentre: 79 tables + 1 view, and `diff` against the
previous file shows the view and nothing else.

**Correction, dated 2026-11-13 — row 10's gate names two suites that cannot run against an API candidate.**
Measured, not assumed:

```
$ bash /tmp/run_e2e_suite.sh notifications_e2e
E  sqlite3.OperationalError: no such table: projects
tests/e2e/notifications_e2e.py:71: OperationalError
ERROR tests/e2e/notifications_e2e.py::test_the_header_shows_the_unread_count_before_any_script_runs

$ bash /tmp/run_e2e_suite.sh curation_e2e          # "the queue suite"
E  sqlite3.OperationalError: no such table: projects
tests/e2e/curation_e2e.py:80: OperationalError
ERROR tests/e2e/curation_e2e.py::test_the_queue_is_reachable_and_lists_the_act
```

Both seed through `sqlite3.connect`. This is the **sixth and seventh** suite to fail this way (row 8's
`feature_e2e`/`milestones_e2e`/`rank_links_e2e`, row 9's `consensus_e2e`/`audit_e2e`), so the correction is
no longer row-specific and is stated once here: **every browser suite in `tests/e2e/` whose fixture opens
`sqlite3.connect` cannot run against a PostgreSQL candidate, and the port cannot fix that from the API side.**
They move to row 12, which is the row that can convert them; row 10's gate now names the two probes.

**Row 10 status, dated 2026-11-13 — the four surfaces answer, but two of them are mine and should be the
estate's.** This is the ledger correcting itself rather than a row being marked Done, because the plan's own
`Adopt:` line for S4 names three crates and I built two of them from scratch instead of looking first:

```
$ ls -d /personal/documents/code/polaris-* | wc -l          -> 20+
$ ls src/ of polaris-authz                                    -> eval.rs authorize(policy, subject, capability, scope, now)
$ grep -nE "^pub (fn|struct|enum)" polaris-comments/src/lib.rs
   pub fn check_body(policy, body) -> Result<(), Refused>
   pub fn place(policy, parent: Option<Parent>) -> Result<u8, Refused>
   pub fn order(policy, comments: &mut [Comment])
   pub fn after_delete(policy, comment) -> Option<Comment>
   pub fn replies_lost(policy, comments, id) -> usize
```

Both exist, both are pure, both are exactly what I hand-wrote:

* `crates/db/src/moderation.rs` keeps a `ROLES` ladder and `role_at_least` — a permission check beside
  `polaris-authz`, which is what **gate 45** fails a site for. The crate's `authorize()` is deny-overrides,
  expiry-aware, and returns a `Denial` variant per reason; my ladder returns a bool.
* `crates/db/src/comments.rs` keeps `body.trim().is_empty()`, ordering by `created_at`, and a tombstone
  delete — `check_body`, `order`, and `after_delete` in `polaris-comments`. `place` (max depth, overflow
  policy) and `replies_lost` (what a delete costs the tree) are rules I did not implement at all, which is
  the part of this that is a real gap rather than a style preference.

What landed and is proved by probes stands: the inbox answers with the owner check in the WHERE clause
(26/26), the queue pages on a three-part cursor (27/27), threads refuse a cross-project or cross-parent
comment (31/31), and moderation refuses a shipped feature, a baseline entry and a second reversal (39/39),
with the reason now validated before the first write. Those are behaviours the Go service has; adopting the
crates must not lose one of them, and the probes are the evidence that it did not.

**Decision, dated 2026-11-13:** adoption happens by building the crate's `Policy` from concord's governance
model (its `Charter` already carries the quorum and role rules) and deleting the hand-written parts, in this
order — `polaris-comments` first, because it closes a functional gap (`max_depth`/`overflow` are unimplemented
today), then `polaris-authz`, then `polaris-inbox` at cutover, where the gate's old-route counter is what
retires `/api/v1/notifications`. Rejected alternative: keep the hand-written pair and declare `authz = local`
in the manifest, because gate 45 does not care that it works — it fails on the check existing beside the
crate, and a second implementation of a permission ladder is how one surface ends up granting what another
denies.

### Row 10 — Done 2026-11-13

The four surfaces answer, and two of them are the estate's code rather than mine.

```
$ bash /tmp/run_probe.sh probe_s4_inbox.py        -> 26/26 ok
$ bash /tmp/run_probe.sh probe_s4_curation.py     -> 27/27 ok
$ bash /tmp/run_probe.sh probe_s4_comments.py     -> 40/40 ok
$ bash /tmp/run_probe.sh probe_s4_moderation.py   -> 39/39 ok
$ bash scripts/parity/e2e.sh                      -> test result: ok. 29 passed; 0 failed
$ cargo test -p concord-db --lib                  -> test result: ok. 13 passed; 0 failed
$ cargo mutants / probes' negative controls        -> see each part's commit message
```

Each part was proved by a control that reproduced the defect it guards against, not by the green run:

| part | gate | control |
|---|---|---|
| inbox (`e0b31f0`) | 26/26 | owner check dropped → 23/26, "marking somebody else's is a 404: got 200" |
| queue (`be68505`) | 27/27 | two-part cursor → 24/27, the same row 41 times and never terminating |
| threads (`9e8f49c`) | 31/31 | thread-ownership guard removed → 26/31, a comment on project B's feature filed under project A (201) |
| moderation (`130a332`) | 39/39 | reason checked after the write → 30/39, "a refused act left the entry alone: got 0", status 'rejected' |
| adoption (`b969b26`) | 40/40 + 39/39 | the probes are the control: the crate must not lose one of the behaviours the Go service has |

**Adopted, not written.** `polaris-comments` owns `check_body`, `place`, `order`, `after_delete`,
`replies_lost`; `polaris-authz` owns the permission decision (one capability `moderation.act`, grants for
reviewer/maintainer/owner at `Scope::Curated`, and the trust floor as `Requirement::TrustAtLeast` on the
`everyone` role, so two doors are one call). The hand-written ladder and the hand-written body/order/tombstone
rules are deleted, because gate 45 fails a site for keeping its own check beside the crate and gate 19 for the
duplication.

`POLICY` declares unbounded depth with `Overflow::Refuse` (the table has no depth column — suki's shape) and
`body.max = usize::MAX` (the product has never had a ceiling; inventing one is a new product rule, and this row
is a port). The crate decides *whether*; the product keeps saying *what it says today*, so adopting a rule
cannot silently rewrite the text a person reads.

**Decisions taken in this row, dated 2026-11-13:**

1. **`taxonomy` and `strategy` votes are refused, not half-implemented.** Their ratification needs the eligible
   set (`eligibleTaggers`, the collaborators a weight proposal is put to). A consent recorded without it lets
   anyone ratify a proposal that was never put to them — the exact defect the Go store's own comment records
   ("anyone with an account could ratify a tag proposal for a project they had no relationship with"). The
   route answers a 400 that names the missing set. **Gap, tracked on row 11**, which is the slice that can
   port the eligibility queries.
2. **The 401 shape is declared divergent, not fixed.** The deployed Go build answers `{"error": "authentication
   required"}` — a bare string — while its own 404s use `polaris-core`'s envelope. The port has one envelope
   (ADR 0011). `concord-parity` now prints `DECLARED /api/v1/notifications: … (removed by row 13)`, so the
   divergence is named in every run instead of hiding the next real difference in silence.
3. **No thread path in the corpus.** The deployed build is 87 commits behind the tree and answers
   `{"error":{"code":"not_found","message":"No such API route."}}` for
   `/api/v1/projects/49/threads/feature/5755`, so recording it would freeze a deploy artefact. The route's
   shape comes from `internal/httpapi/server.go`; the port's evidence for it is `probe_s4_comments.py`.
4. **Route shapes follow the Go table, not my router.** Threads hang off the numeric `{project_id}` and the
   queue is `/api/v1/curation/` with `POST /{kind}/{id}/vote` below it. `project_by_id` sits beside
   `project_for` rather than one function accepting either, because a route that quietly took both would hide
   a client calling the wrong one.

**Corpus after this row:** 20 shapes recorded; `2 shape(s) disagreed of 14 checked, 5 skipped` — both
survivors are row 11's (`/api/v1/search`, `/api/v1/stats/summary`).

### Row 11 — in progress (2026-11-13)

**Landed:** `/api/v1/stats`, `/api/v1/stats/summary` (`4f9a707`) and `/api/v1/search` (`23c4910`).

```
$ CANDIDATE=…:8108 concord-parity --corpus crates/parity/corpus.json
DECLARED /api/v1/notifications: the Go build's 401 is a bare string; … (removed by row 13)
DECLARED /api/v1/search?q=arena: the Go fts index is missing 2 of 73 projects, so it returns 2 hits where the
         port returns 3 (suki's description contains 'an arena'); the port's index is generated and cannot go
         stale (removed by row 13)
0 shape(s) disagreed of 14 checked, 5 skipped
```

`/api/v1/stats` is declared time-varying, so the tool skips it; compared by hand against the recorded live
body with `generated_at` removed: **195 leaf values, 0 differences**.

**The finding of this row, and the first difference the port is right about.** The live build's search cannot
find two of its own projects:

```
$ sqlite3 ~/.local/share/concord/concord.db \
    "select (select count(*) from projects), (select count(*) from projects_fts),
            (select count(*) from projects p where not exists
               (select 1 from projects_fts f where f.slug = p.slug))"
73|111|2
# missing: ascend, suki
```

111 index rows for 73 projects, and two projects absent entirely — `suki`'s description reads "…a wiki, an
arena…" and the live search for `arena` does not return it. The port's index is a generated `search_tsv`
column, so the database recomputes it from the row and this class of staleness cannot exist. That is DECLARED
in `concord-parity` with the query above beside it, not reproduced: copying a stale index is not parity, it is
a bug with a test.

**Two things that cannot be reproduced and are stated rather than hidden:**

1. **The rank.** fts5 ranks by bm25, PostgreSQL by `ts_rank`. The shape keeps a `score` on every hit and orders
   by it; the values are this database's own. A number forged to look like another database's is worse than an
   honest one from this one.
2. **Coverage, kept deliberately.** `projects_fts` indexed name, description, **tags** and **languages**; the
   generated column covers only the two columns of `projects`, so free text also matches a tag or a language
   through an `EXISTS`. Without it, a search for `rust` would have quietly stopped finding the `rust` projects
   — a regression in the direction of "the search got worse", which is exactly what a port must not ship.

**Still open on this row:** the newcomer-responsiveness leaderboard, the feeds, the three binaries
(`healthsync`, `proposalsweep`, `statsnapshot`) onto `polaris-loops`, `granttrust`/`capableseed`/`forgeseed`/
`embedbackfill` folded into `crates/cli`, `taxonomy`/`strategy` curation votes (whose eligible sets row 10
deferred here), and gates 4, 5 and 47.

### Row 11 — parts 3, 4, 5 (2026-11-13)

**Landed:** `5e6ed67` (stats/history + stats/responsiveness), `2d60543` (thirteen feed documents), `da88036`
(statsnapshot + proposalsweep into `concord-cli`).

```
$ python3 scripts/parity/probe_s5_stats.py    -> 16/16 ok
$ python3 scripts/parity/probe_s5_feeds.py    -> 60/60 ok
$ python3 scripts/parity/probe_s5_timers.py   -> 30/30 ok
$ CANDIDATE=…:8108 concord-parity --corpus crates/parity/corpus.json
  0 shape(s) disagreed of 17 checked, 4 skipped
$ curl /api/v1/stats                          -> 0.058s
```

**The migrated database could not take a write, and nothing but a probe would have found it.**

```
$ python3 scripts/parity/sequences.py
19 of 47 sequences are behind the rows they serve
  arenas: sequence at 0, rows reach 74 (73 rows)
  audit_log: sequence at 0, rows reach 9332 (9326 rows)
  features: sequence at 0, rows reach 5784 (5760 rows)
```

The mover inserts explicit ids, which leaves every sequence at its starting value; the first insert the port
attempted failed with `duplicate key … "projects_pkey" DETAIL: Key (id)=(1) already exists`. Fixed in two
places: `scripts/parity/sequences.py` measures and repairs (`--fix` applied, 0 of 47 behind), and `move.py` now
advances sequences as its last step. The e2e database hid this by seeding fresh.

**A 4.3-second statistics query.** `concord-cli statsnapshot --dry-run` printed
`WARN sqlx::query: slow statement: elapsed=4.291468634s rows_returned=2402` — `triage_latency` ran a correlated
subquery per complaint. Rewritten as one grouped pass with a left join; the route now answers in 0.058s.

**All thirteen feed routes are 503 on the live instance**, because `CONCORD_BASE_URL` is not in the unit's
environment (`systemctl --user show concord -p Environment` lists `CONCORD_LISTEN`, `CONCORD_DB`,
`CONCORD_EMBED_URL`, `CONCORD_EMBED_MODEL`, `CONCORD_EMBED_TIMEOUT_MS` — no base URL). So syndication has never
been on, and "feed parse against live" cannot be the proof: the proof is the candidate with the variable set.
**Row 13 inherits: set `CONCORD_BASE_URL=https://concord.polarisocial.xyz` in the unit and declare the feeds in
`estate.toml`, then gate 47 covers concord too.**

**Estate gates 4, 5 and 47, measured:**

```
gate  4 check-versions      20/22 hostnames report the estate version   media.polarisocial.xyz 502
gate  5 check-capabilities  21/22 hostnames serve what the manifest declares   media 502
gate 47 check-feed-routes   8 arm(s) passed, 0 failed
```

Gates 4 and 5 are red for **`media.polarisocial.xyz`** (502; no unit answers to `media`), which is not concord's
and not this row's. Gate 47 is green and does not yet cover concord, because no manifest entry declares its
feeds — deliberately, since every live feed answers 503 today.

**`polaris-loops` does not own scheduling.** The row's line says "timers migrated to `polaris-loops`"; the crate
is `level_for`/`next_level_at`/`weight_for` — XP arithmetic, no scheduler. The estate's scheduler is systemd and
`scheduled.toml` is its declaration (`scripts/schedule-registry.py` writes it). So the migration is: jobs in
`concord-cli`, units declared in `scheduled.toml`, and **the Go timers keep running until row 13** — moving a
timer onto a service that is not serving yet would stop the live instance's data from being refreshed.

**Still open on this row:** `healthsync` (709 lines, forge HTTP clients + the health scorer — the scorer belongs
beside `crates/ranking`'s math, and the job's three rules are "a failed fetch writes nothing", "an unmeasurable
field skips the project", "one scorer"), the three systemd units in `scheduled.toml`, the `taxonomy`/`strategy`
curation eligible sets row 10 deferred here, and `granttrust`/`capableseed`/`forgeseed`/`embedbackfill` folded
into `crates/cli`.

### Row 11 — part 6: the taxonomy eligible set (2026-11-13, `d5364d6`)

```
gate (before): POST /api/v1/curation/taxonomy/{id}/vote
  400  curation kind "taxonomy" is not ported yet: its ratification needs the eligible set …
gate (after):
  probe_s5_taxonomy.py -> 18/18 ok
  probe_s5_stats.py    -> 16/16 ok      probe_s5_timers.py -> 30/30 ok
  probe_s5_feeds.py    -> 60/60 ok      probe_s4_curation.py -> 27/27 ok
  concord-parity       -> 0 shape(s) disagreed of 17 checked, 4 skipped
```

`crates/db/src/taxonomy.rs` closes the hole row 10 recorded: a consent is decided by the eligible set, and the
set is checked in the store before anything is written. A stranger's 200 is a stranger's vote — the negative
control reproduces it exactly (`FAIL a non-member cannot consent: got 409 want 403`, `FAIL the third consent
ratifies: got 409 want 200`, 11/18).

Rules now enforced, each read from the Go store and recorded here so a later reader does not re-derive them:

* eligible = role `contributor`/`reviewer`/`maintainer`/`owner` in the proposal's project; for an instance-wide
  proposal, in **any** project;
* the denominator is the eligible set, not the participants;
* a consent below the threshold is **recorded** and answered as not-yet (409, naming `insufficient_quorum`);
* ratification **applies** the change (six actions) and writes `ratify_taxonomy_change`;
* a repeated click is one voice (the tally reads the row count).

**`strategy` is still refused**, now with a 400 that names what is missing: its ratification applies a weight
or opens a theme window, over three proposal tables, and its consent rule additionally forbids the proposer
consenting to their own proposal (`themes.go:206`). Recording a consent without that step would count toward a
quorum nobody convened. This is the last functional gap on the row and the next thing to port.

### A probe that dies mid-way poisons the next gate (2026-11-13)

An interrupted run left one project, three members, one tag and one proposal in the database, and
`concord-parity` then reported

```
/api/v1/stats/summary.members_by_role[1].count: 11 vs 8
/api/v1/stats/summary.projects_by_governance[0].count: 63 vs 62
```

which reads exactly like a port defect and is not one. **Probes now clean up at START as well as at the end**,
and the leftovers were removed (73 projects, 0 probe tags, 0 proposals, parity back to 0 disagreed). Any new
probe written for this port must do the same.

### Row 11 — part 7: the strategic weight vote (2026-11-13, `50a7891`)

```
gate (before): POST /api/v1/curation/strategy/{id}/vote -> 400 "not ported yet"
gate (after):
  probe_s5_strategy.py 17/17   probe_s5_taxonomy.py 18/18   probe_s5_stats.py 16/16
  probe_s5_timers.py   30/30   probe_s5_feeds.py    60/60   probe_s4_comments.py 41/41
  concord-parity       0 shape(s) disagreed of 17 checked, 4 skipped
```

`crates/db/src/strategy.rs`: consents come from §8.2's eligible population (role AND a qualifying
contribution, the same set the arena's distinct-voter condition uses), the proposer cannot consent to their own
proposal, and ratification applies the weight **only if** the feature still carries `weight_before` — otherwise
the proposal is marked `stale` and nothing is written over a newer decision.

Negative control (both guards off, 6/17): `FAIL the proposer cannot consent: got 409 want 403` and `FAIL the
proposal is stale: got 'ratified' want 'stale'` — one click from the proposer ratifies and rewrites the weight,
which is the outcome these two guards exist to prevent.

**A test that encoded the old gap had to change, and that is a finding rather than housekeeping.**
`probe_s4_comments` asserted *"an unported kind says so instead of recording a consent … and names the missing
eligible set"*. Taxonomy is now ported, so the route answers 404 "no such proposal" for a missing id. The
assertion now protects what the rule actually says: a missing proposal is a 404 and not a recorded consent, an
unknown kind is a 400 naming it. 41/41.

**Theme proposals are still not ported** (`strategic_theme_proposals`: `applyTheme` upserts `strategic_themes`
with an expiry from `charter.theme_lifetime_days`, and a zero lifetime is refused rather than written born
expired). `kind=strategy` reaches weight proposals only; the theme table has no route. Named here so the next
reader does not mistake the coverage for complete.

### Row 11 — part 8: the health score and the newcomer median (2026-11-13, `e95811c`)

```
gate (before): crates/ranking had no health module; project_metrics.health_score was written only by the Go
               binary and had no port.
gate (after):
  cargo test -p concord-ranking -> 21 + 3 + 2 passed
  health_golden                 -> 8087 values compared over 1344 health rows, 0 mismatched
  probe_s5_healthsync.py        -> 15/15 ok
  concord-parity                -> 0 shape(s) disagreed of 17 checked, 4 skipped
```

`crates/ranking/src/health.rs` (score, components, normalisation, newcomer median) + `crates/db/src/metrics.rs`
(the write) + `concord-cli healthsync --from|--newcomer`. The oracle in `scripts/parity/golden_ranking.go` grew
1,344 health rows, five weight rows and six newcomer fixtures — 8,087 values, all the Go implementation's.

**The oracle caught a real bug on its first run**, and only the oracle could have:
```
MISMATCH health(age=0 review=-1 c=50 r=0).7: rust 5.85627468784419625e-1 vs go 9.94963503179956965e-1
```
`log10(c).min(1.0) / log10(51)` clamps the logarithm instead of the ratio, scoring fifty contributors 0.5856
instead of 0.9949. The code reads perfectly and a hand-written expectation would have been 1.0.

Negative control (unmeasured guard off, 12/15): `FAIL an unmeasured project writes nothing: got
(0.2803443825037019, …) want (0.0, None, None, …)` — a project the forge did not answer for is written over with
the previous score and a fresh `computed_at`.

**Still open on this row: the forge fetch.** No estate crate owns a forge client (`ls ~/code/polaris-*`), so the
fetch half stays with the Go binary until one exists; `--from <dump>` applies what the fetch produced. That is the
same split the Go binary has, and it is the one item that would block a cutover of `healthsync` specifically.

**Two traps recorded for whoever runs this next.**
1. A restored source file kept its old mtime on the network mount, so cargo reused the *mutated* artifact and a
   test failed against code that was already correct. `touch` the file after restoring it.
2. `probe_s5_healthsync` assumed the no-number newcomer state was `(NULL, NULL)`; the schema says
   `newcomer_sample bigint NOT NULL DEFAULT 0`, so it is `(NULL, 0)`. A schema read beats an assumption every time.

### Row 11 — part 9: the forge fetch (2026-11-13, `fcce61c`)

```
gate (before): healthsync --from <dump>  ->  Error: needs --from … and there was no way to produce the dump
gate (after):
  probe_s5_forgefetch.py -> 16/16 ok   (real HTTP against a fake forge)
  probe_s5_healthsync.py 15/15   probe_s5_strategy.py 17/17   probe_s5_taxonomy.py 18/18
  cargo test -p concord-cli -> 5 passed
  concord-parity -> 0 shape(s) disagreed of 17 checked, 4 skipped
```

`crates/cli/src/forge.rs` closes the item part 8 named: repo-URL normalisation (five spellings → one value, an
unknown host refused by name), the GitHub and Forgejo reads, and the four verdicts. `reqwest` with the same
features as the four sibling estate crates, so no second HTTP stack.

The four verdicts are separate because they mean different things to an operator: **synced** (written),
**deferred** (the forge asked for a pause; exit 0, next pass reads it), **failed** (corrupt row, 404, broken
answer; exit 1), **skipped** (nothing measured, nothing written). The Go comment is the reason: *"counting it as
failed made the timer exit 1 every hour on a shared IP, which trains an operator to ignore the unit."*

**The gate found a bug in my own parser:** `part.find("page=")` matches the `page=` inside `per_page=1`, so a
137-commit repository reported 1. The probe asserted 137 from the Link header:
`FAIL commits come from the Link header, not the page: got 1 want 137`. Fixed by requiring the `page=` that is
not preceded by `_`.

Negative control (reviews only, comments ignored): `FAIL a comment counts as a response: got 4.0 want 2.0` —
15/16. The fixture's first pull request is answered *only* by a comment, so dropping comments moves the median
from 2h to 4h, which is the defect that left five of six live repositories "unmeasurable".

**Decision (dated, mine):** the fake forge is reached through `CONCORD_GITHUB_API` / `CONCORD_FORGEJO_API`, not
through a test-only binary flag. The gate is then a real HTTP exchange — the thing that can actually be wrong —
and production leaves both empty.

**Not asserted:** the Forgejo branch has no probe; the fixtures are GitHub-shaped. Same code path, different JSON
keys — the kind of claim that deserves a run, so it is listed as open rather than assumed.

### Row 11 — part 10: the Forgejo branch (2026-11-13, `62f462b`)

Part 9 closed naming this gap in its own commit message; this closes it.

```
gate (before): the Forgejo read had no assertion anywhere — the fixtures were GitHub-shaped.
gate (after):
  probe_s5_forgefetch.py -> 24/24 ok   (was 16/16)
    ok forgejo commits come from X-Total-Count: got 88 want 88
    ok forgejo contributors are distinct logins on one page: got 3 want 3
    ok forgejo review latency: got 3.0 want 3.0
```

Three differences from the GitHub path, each now measured: the commit total is `X-Total-Count` (GitHub uses the
Link header), contributors are distinct `author.login` over one commits page (GitHub has a contributors
endpoint), and reviews arrive under `/api/v1/repos`. The fixture repeats one login four times so the assertion is
the distinct count and not the page length.

### Row 11 — part 11: §6.2's themes at the frontend's own routes (2026-11-13, `331556d`)

```
gate (before): GET/POST /api/v1/projects/{id}/strategy/themes       -> 404 (no route)
               GET      /api/v1/projects/{id}/strategy/theme-proposals  -> 404
               GET/POST /api/v1/projects/{id}/strategy/weight-proposals -> 404
gate (after):
  probe_s5_themes.py 41/41   probe_s5_strategy.py 17/17
  probe_s5_taxonomy.py 18/18  probe_s5_healthsync.py 15/15
  concord-parity 0 shape(s) disagreed of 17 checked, 4 skipped
```

`crates/db/src/themes.rs` plus nine routes in the Go's shape. **The route names were the finding**: settings.js
reads `/api/v1/projects/{id}/strategy/themes`, and until now the port answered only the curation queue — a page
that would have been blank at cutover while every probe passed. The curation queue stays as an extra.

Rules now enforced: proposing applies nothing (asserted by comparing the live list before and after); an unknown
tag is refused naming `/taxonomy/proposals`; weight within `[-2, 3]`; a rationale is required; one pending per
(project, tag, namespace) via `idx_theme_one_pending`; the proposer cannot consent to their own proposal; a
member without a qualifying contribution is not in the eligible population; a consent below threshold is
recorded and answered **202** (200 would say the roadmap moved); a repeated click is one voice; a theme expires at
`charter.theme_lifetime_days`; a renewal UPSERTS the same row; a zero lifetime is refused, not written born
expired.

Negative control (renewal upsert → `do nothing`, 38/41): `FAIL with the new weight: got 1.5 want 2.5` — the
renewal ratifies and changes nothing, so the proposal says the weight moved and the weight did not.

**Two of my assumptions were wrong and the probe said so:**
1. the eligible population is 4, not 3 — the proposer is in it and merely barred from consenting. Excluding them
   would make a three-person project believe it has two and never reach its floor;
2. `UPDATE charters … WHERE project_id` touched zero rows (no charter row existed), so the zero-lifetime case
   silently ran on the 90-day default and asserted 200. It is an upsert now. **A write that matches no row is not
   a write** — the same class of mistake as a green build with an unwired route.

### Row 11 — part 12: the route inventory, and the 139 that remain (2026-11-13, `3365a04`)

```
gate (before):
  scripts/parity/routes.py -> go routes: 216   port routes: 56
                              142 go route(s) the port does not answer
gate (after):
  scripts/parity/routes.py -> go routes: 216   port routes: 79   (139 remain; the script exits 1)
  probe_s5_panels.py       -> 15/15 ok
  concord-parity           -> 0 shape(s) disagreed of 17 checked, 4 skipped
```

**This is the most important measurement of the port so far, and it is not flattering.** The port answered 56 of
the Go's 216 routes. Every probe written before this one asked for the routes its author remembered, so
`/auth/me` — which every page needs before it can render an identity — was a 404 that no probe looked at, while
every gate stayed green.

`scripts/parity/routes.py` is the answer to that class of blindness: it parses the Go's chi registrations
(`r.Get/Post/Put/Delete`, nested `r.Route`/`r.Group` prefixes) against the port's `.route(…)` chains, compares by
SHAPE (`{slug}` and `{project_id}` are the same path), and exits 1 with the missing list. It also prints the
port's extras, because an extra route is a claim the Go tree does not make.

**Its own first two runs were wrong, and both errors are recorded because the tool is now a gate:** it compared
prefixed paths against nested ones (reporting 182 of 236), and its lazy regex stopped at the first nested `)` (so
`/projects/{}/features` looked missing when the port answers it). A route-comparison tool that lies is worse than
none, so it was validated against paths known to be ported before its verdict was believed.

Ported in this part, by what the frontend needs first: `GET /auth/me`, `GET /projects/{slug}/tags`,
`GET /projects/{id}/members`. `readable()` implements the Go's `CanAccessProject` rule for rule, including "an
unknown visibility FAILS CLOSED". Negative control (visibility check removed, 13/15):
`FAIL a private project's tags are 404, not an empty 200: got 200 want 404`.

**The remaining 139, measured and named** (regenerate with `python3 scripts/parity/routes.py`; the script is the
state list, this paragraph is a snapshot): finder 4 · contribute 2 · field reports 2 · lists 5 · invites 1 ·
criteria + criteria-profiles 3 · solutions 6 · requests 5 · arena reads 3 · capabilities 1 · languages 1 ·
documents kinds/search 2 · merge 4 · admin-ledger 1 · guest 1 · badge/login/pages 6 · per-feature
evidence/deps/tags/claim 9 · plus the write routes on board/features/milestones that the port answers under a
different method or path.

**Decision (dated, mine):** the `routes.py` exit code is the row's gate from here. A slice is not finished because
its probe is green; it is finished when the missing count drops by the routes it claims and nothing else moves.
The count is 139 and the ledger will not say otherwise.

### Row 11 — part 13: the project page's panels (2026-11-13, `42bce61`)

```
gate (before): GET /projects/{id}/criteria, /criteria-profiles, /projects/{slug}/languages, /projects/{id}/lists
               -> 404, all four
gate (after):
  probe_s5_criteria.py -> 18/18 ok
  routes.py            -> go 216 / port 83   (135 remain, exit 1)
  cargo test -p concord-db --lib -> 23 passed
  concord-parity       -> 0 shape(s) disagreed of 17 checked, 4 skipped
```

Four routes and four orderings, because the ordering IS the answer here: criteria `active DESC, slug` (a retired
criterion is listed after the live ones, not dropped), profiles by slug, languages `pct DESC, language ASC` (so
equal shares do not swap between requests), lists with the project's own first and `project_id: null` for the
instance-wide ones — a SCOPE (§16), not a zero.

Negative control (criteria by slug alone, 17/18): `FAIL active first, then slug: got ['aaa-retired', 'zzz-live']
want ['zzz-live', 'aaa-retired']`.

`readable_project()` centralises the visibility rule for these four: **404, never 403**, because a 403 tells an
anonymous caller which ids exist.

**Two schema facts the baseline decided, not the code** — both discovered as probe failures rather than by
reading: `criteria.active` is a BIGINT (SQLite kept INTEGER affinity), so the select compares (`active <> 0`)
instead of decoding a bool; `criteria_profiles` has no `weights` column on this schema. Read the columns before
writing the query, or the probe will read them for you.

### Row 11 — part 14: capabilities, and a census that cannot be compared (2026-11-13, `3013034`)

```
gate (before): GET /projects/{id}/capabilities -> 404
gate (after):
  probe_s5_capabilities.py -> 12/12 ok
  routes.py                -> go 216 / port 84   (134 remain)
  cargo test -p concord-db --lib -> 25 passed
  concord-parity           -> 0 shape(s) disagreed of 16 checked, 5 skipped
```

The panel is a join of two decisions. The whole catalogue is rendered (an unasserted capability IS the contribution
queue); `asserted: false` means nobody said anything, never "no", and the counts partition the catalogue; **a
dispute outranks a confirmation at the same quorum** — 2/2 is disputed, because reading it as confirmed lets
agreement that arrived first outrank louder disagreement; a disputed claim keeps its ORIGINAL value, since
substituting "no" rewrites "people disagree" as "this is false"; and the asserter's own confirmation does not
count. Negative control (confirmation checked first, 11/12): `FAIL two confirmations and two disputes read as
disputed: got 'confirmed' want 'disputed'`.

**Three schema facts found by failing rather than by reading:** `capability_confirmations.confirmed` is a BIGINT
(PostgreSQL refuses `AND` on it, and the message names the type, not the column); its timestamp column is `at`;
and `capabilities` is an instance-wide catalogue, so the probe asserts `probe keys ⊆ catalogue` and
`unknown = total - asserted` — a total of 3 would only pass on an empty instance, i.e. on a broken one.

**Decision (dated, mine): `/api/v1/stats/summary` is `unstable` in the corpus, with the reason recorded.**
It is a census over a corpus that is still growing: the live Go counts projects created after the snapshot and the
port's copy cannot, so the COUNTS are not comparable before cutover. The SHAPE is compared, by probe_s5_stats.py.
Declaring it unstable without the reason would be exactly the silence the parity tool exists to prevent.

### Row 11 — part 15: field reports, and the denominator (2026-11-13, `7e5fbc6`)

```
gate (before): GET /projects/{id}/field-reports -> 404
gate (after):
  probe_s5_fieldreports.py -> 14/14 ok
  routes.py                -> go 216 / port 85   (133 remain)
  cargo test -p concord-db --lib -> 28 passed
  concord-parity           -> 0 shape(s) disagreed of 16 checked, 5 skipped
```

§4.7's "worked for 83% of **reporters** on arm64" — the rate without its denominator is the whole bug, so no
reports is an ABSENT `outcome_rate` with `sample_size: 0` (never a zero, which claims a failure nobody observed),
one report carries `sample_size: 1`, the rate is reputation-weighted with the weight CAPPED at 100 (else two
prolific users decide the panel), `worked-with-caveats` counts half (scoring it as either erases the caveat), and
removed reports are excluded from both the list and the rate — serving one would undo the quorum decision that
removed it. Negative control (caveats credited as full success, 12/14): `FAIL arm64 is the spec's 83%: got 1.0
want 0.8333`.

Two schema facts found by failing: `reputation_events` has `kind`, not `reason`; and a bare `0` in a select list is
INT4, so decoding it as `i64` fails with a message naming a **column number** rather than a column — `0::bigint`.

### Row 11 — part 16: document kinds and feature tags (2026-11-13, `171c2b1`)

```
gate (before): GET /projects/{slug}/documents/kinds -> 404
               GET /projects/{slug}/features/{feature}/tags -> 404
gate (after):
  probe_s5_kinds.py -> 7/7 ok
  routes.py         -> go 216 / port 87   (131 remain)
  cargo test -p concord-db --lib -> 29 passed
```

Two one-line lookups, each carrying the rule that makes it honest: `documents/kinds` returns the schema's
vocabulary rather than `select distinct kind` (a distinct query lets one typo invent a kind that the writer then
validates against), and the tags route resolves the feature INSIDE the project first, so another project's feature
id is a 404 rather than a list of somebody else's tags. Both are also refused for a project the caller cannot read.

Three schema facts found by failing: `features` has no `description` column, `features.author_id` is NOT NULL (a
fixture needs a real user, not a literal), and the e2e harness appends `public_id` on its own.

### Row 11 — part 17: the dependency panel (2026-11-13, `6738877`)

```
gate (before): GET /projects/{slug}/features/{feature}/deps -> 404
gate (after):
  probe_s5_deps.py -> 10/10 ok
  routes.py        -> go 216 / port 88   (130 remain)
  cargo test -p concord-db --lib -> 30 passed
  concord-parity   -> 0 shape(s) disagreed of 16 checked, 5 skipped
```

Three lists over one table where the schema makes the confusion easy: `blocked_by` joins
`f.id = dep.blocker_id where dep.blocked_id = $1` and `blocks` is the mirror image. A copy-paste leaving both on
`blocked_id` is plausible on a feature nobody blocks, so the fixture is **asymmetric**: A blocked by B (in
progress) and C (shipped). `open_blockers` is a STATUS PREDICATE (`<> 'shipped'`), not a row property — a client
deriving it ships the status vocabulary and, at the next terminal status, claims work is blocked by finished work.

Negative control (blocks joined on `blocker_id`, 8/10): `FAIL and it blocks exactly the one feature: got [5817]
want [5816]`. Note what it did NOT break — `blocked_by` still read correctly — so a suite that only checked the
blocked feature's panel would have passed.

### Row 11 — part 18: a feature's evidence (2026-11-13, `d9c1ab6`)

```
gate (before): GET /projects/{slug}/features/{feature}/evidence -> 404
gate (after):
  probe_s5_evidence.py -> 7/7 ok
  routes.py            -> go 216 / port 89   (129 remain)
  cargo test -p concord-db --lib -> 30 passed
  concord-parity       -> 0 shape(s) disagreed of 16 checked, 5 skipped
```

Evidence is a historical claim, so a deleted target leaves the LINK with an empty title; an inner join makes the
evidence vanish and the panel reads "nobody linked anything" — a different and wrong claim. `kind` decides which
table names the ref, and the probe's fixture has a document, a report, and a document deleted between the link and
the read. Negative control (inner join, 3/7): `FAIL all three links are listed, including the one whose document is
gone`. The control also caught a probe defect: the first version raised `KeyError` and printed NO result block, so a
broken build looked like a broken probe — rows are now indexed with `.get` so a missing row is a failed ASSERTION.
Three schema facts found by failing: `project_documents.slug` and `.author_id` are NOT NULL, and `field_reports` has
a FK from `projects` (teardown order).

### Row 11 — part 19: one complaint, bound to the project (2026-11-13, `b6eab0c`)

```
gate (before): GET /projects/{project_id}/complaints/{id} -> 404
gate (after):
  probe_s5_complaint.py -> 12/12 ok
  routes.py             -> go 216 / port 90   (128 remain)
  cargo test -p concord-db --lib -> 30 passed
  concord-parity        -> 0 shape(s) disagreed of 16 checked, 5 skipped
```

The URL has two halves and this route ties them: a complaint id is global, so a handler reading only `{id}` makes the
project half decoration and lets any caller reach any complaint through their own project's URL. A mismatch is 404,
never 403. The probe separates four refusals that are four different mistakes: another project's complaint (404), a
non-numeric complaint id (**400** — parsing it as 0 is a 404 for the wrong reason), an unknown or non-numeric project
segment (404, as the Go's `requireProjectID` maps a parse failure to `ErrNotFound`), and a private project without a
token (404). The probe caught the third: `FAIL an unknown project is 404: got 400 want 404`, because the handler used
the slug helper on a numeric segment.

### Row 11 — part 20: the complaint ↔ feature link (2026-11-13, `f0ca066`)

```
gate (before): GET /projects/{id}/features/{feature}/complaints -> 404
               GET /projects/{id}/complaints/{id}/features      -> 404
gate (after):
  probe_s5_links.py -> 13/13 ok
  routes.py         -> go 216 / port 92   (126 remain)
  cargo test -p concord-db --lib -> 30 passed
  concord-parity    -> 0 shape(s) disagreed of 16 checked, 5 skipped
```

Two routes over one link table, joining on **different columns** and sorting by different keys — `fc.feature_id` with
`created_at desc`, `fc.complaint_id` with `elo_r desc, id`. The fixture is asymmetric (feature X shares complaint A
with feature Y) because a swapped join still returns a plausible list.

Negative control (10/13) and the failure worth reading: `FAIL a complaint with no features answers an empty array:
got [{'id': 2423, 'title': 'Project maintainer office hours…'}] want []` — the swapped join returned **another
project's feature** under a complaint with no links at all. That is exactly the class of answer that looks like data.

Both routes answer an **empty array, never null**: `null` breaks a client expecting a list, and "nothing links here"
is the common case. One axum fact learned here: two routes differing only in a **parameter name** are refused at
insertion, so the new route binds `{slug}` like its siblings although the Go names it `{project_id}` — parameter
names are not part of a shape.

### Row 11 — part 21: one milestone with its planned work (2026-11-13, `c4917f3`)

```
gate (before): GET /projects/{project_id}/milestones/{id} -> 404
gate (after):
  probe_s5_milestone.py -> 13/13 ok
  routes.py             -> go 216 / port 93   (125 remain)
  cargo test -p concord-db --lib -> 30 passed
  concord-parity        -> 0 shape(s) disagreed of 16 checked, 5 skipped
```

The project predicate lives in the QUERY, not in a comparison after the read; the feature order is the milestone's
PLAN (`milestone_position, id`) and the fixture inserts features in reverse plan order so ordering by id differs; and
`shipped` is shipped **with a shipment event**, the gap reported as `unshipped` — the probe asserts `(3, 0, 1)` before
the event and `(1, 0)` after inserting one, which pins the rule rather than the arithmetic. Negative control
(predicate removed, 12/13): `FAIL another project's milestone is 404: got 200 want 404`.

Two schema facts found by failing: `milestones.target_date` is a **TIMESTAMPTZ** in this baseline (the Go stored a REAL
unix stamp) and must be converted in SQL, and `at_risk` is derived in two places that must agree — the getter reuses
the list's exact predicate rather than restating it.

### Row 11 — part 22: a list and its ranked entries (2026-11-13, `37fe698`)

```
gate (before): GET /projects/{project_id}/lists/{id}          -> 404
               GET /projects/{project_id}/lists/{id}/entries  -> 404
gate (after):
  probe_s5_lists.py -> 16/16 ok
  routes.py         -> go 216 / port 95   (123 remain)
  cargo test -p concord-db --lib -> 30 passed
  concord-parity    -> 0 shape(s) disagreed of 16 checked, 5 skipped
```

§16's "entries are **ranked**, not just listed": the fixture's ratings are in neither insertion nor alphabetical order
and the removed entry holds the highest rating, so title order, id order and rating-without-the-visibility-filter each
yield a different plausible list. Negative control (ordered by title, 13/16): `FAIL ranked by rating, not by title or
id: got [484, 485, 486] want [485, 486, 484]`.

The visibility decision belongs to the **list's** project, not the one in the URL: a list owned by a private project is
404 through a public project's URL, and an **instance-wide** list (`project_id` NULL) is readable everywhere by §16 —
precisely the case an `int64 project_id` made unreachable, because the scan failed and the detail route blamed the
database. A non-numeric list id is 400 (the caller's mistake); an id nobody created is 404.

### Row 11 — part 23: the missing list is two lists (2026-11-13, `3f073e7`)

```
gate (before): go routes: 216   port routes: 95
               123 go route(s) the port does not answer
gate (after):  go routes: 216   port routes: 95   missing: 123 (113 json, 10 page)
               113 JSON route(s) the port does not answer (the row's work)
               10 PAGE route(s) the Go renders and the frontend serves (row 12)
```

The Go registers **HTML page routes beside its JSON API** (`/projects/{slug}/rank`, `/ranking`, `/settings`,
`/finder`, `/login`, `/register`, `/contribute`, `/taxonomy`, `/`). The estate profile serves those from the frontend,
so porting them as JSON is work created to be deleted — and leaving them in one bucket made the row's own state list
wrong by ten. `routes.py` now maps each registration to its handler and asks whether the handler calls `writeJSON` or
`s.render(`; an unfindable handler counts as **JSON**, because guessing "page" would silently drop real API work.

Two facts the classification forced out: `normalise()` strips `/api/v1`, so an API route and a page route of the SAME
shape collide — registrations are now a LIST per shape and a shape is JSON when any handler answers JSON; and the
handler-name regex first dropped closure registrations, taking the go count 216 → **215**. A count going DOWN is never
an improvement in this tool, so it now falls back to recording the route with an empty handler.

**Decision (dated, mine): the row's gate is the JSON count.** 113 is the number to drive to zero; the 10 page routes
belong to row 12 and are listed separately so neither is hidden behind the other.

### Row 11 — part 24: feature tallies, and four defects reproduced on purpose (2026-11-13, `257e4a8`)

```
gate (before): GET /projects/{project_id}/tallies -> 404
gate (after):
  probe_s5_tallies.py -> 11/11 ok
  routes.py           -> go 216 / port 96   missing: 122 (112 json, 10 page)
  cargo test -p concord-db --lib -> 31 passed
  concord-parity      -> 0 shape(s) disagreed of 16 checked, 5 skipped
```

The Go's tallies carry four behaviours that are not a reasonable reading of the spec, and the port reproduces all four,
because its gate is "the same answers on the same data" — fixing one here would make the two services disagree and hide
real port bugs inside a well-intentioned improvement:

* the wire key is **`Title`**, capitalised — that field has no json tag while every sibling is snake_case;
* **`votes_cast = wins + losses`**, so SKIPS are counted and never cast: eight votes can report `votes_cast: 4`;
* wins/losses **do not partition** the matches: the count reads the pair's `outcome` without asking which SIDE this
  feature was on, so a feature that LOST still gains a `wins` when the pair's outcome was `a`;
* a project with no features answers **`null`**, not `[]` — the Go's nil slice.

**These four are open defects for row 13 (cutover)**, recorded here rather than fixed: each needs one knowing change in
one place, after the port can no longer be verified by equality. Negative control — the "fix" one would be tempted to
make (`votes_cast` including skips) — fails two assertions, 9/11, which is the proof the probe distinguishes the
service's arithmetic from the plausible one.

One schema fact found by failing: `pairwise_votes.arena_id` is NOT NULL, so every vote belongs to an arena and a
fixture needs one even when the read under test never consults it.

### Row 11 — part 25: document search, and a control that did NOT fail (2026-11-13, `8c39449`)

```
gate (before): GET /projects/{slug}/documents/search?q=… -> 404
gate (after):
  probe_s5_search.py -> 10/10 ok
  routes.py          -> go 216 / port 97   missing: 121 (111 json, 10 page)
  cargo test -p concord-db --lib -> 33 passed
```

`q` is REQUIRED (missing → 400) while a present-but-empty `q` is an empty array: "you did not ask" and "you asked for
nothing" are different requests. A bare term is quoted as a PHRASE (`language-neutral` under FTS5 is "language AND NOT
neutral"); multi-word and quoted queries pass through untouched; `limit <= 0 or > 100` becomes 20. The fixture has a
document that says "language" and never "neutral", so the boolean reading returns it and the phrase reading does not —
`language-neutral` returns exactly one of the three documents.

**The negative control did NOT fail, and that is this part's finding.** Forcing every query down the expression path
changed no assertion, because PostgreSQL's `websearch_to_tsquery` already treats a bare hyphen as a phrase separator
where FTS5 treats it as NOT. The quoting rule is REDUNDANT on this engine for the very case that motivated it. It stays
— `phraseto_tsquery` pins the semantics instead of leaving them to be re-derived — and the code now says so. The real
engine risk is elsewhere: **stopwords, stemming and operator syntax differ between FTS5 and tsquery**, and only a
corpus comparison can say whether that matters. That is an **open question for row 13**, not an assumed equivalence.

A control that passes on unfixed code is not evidence — but a control that passes because the premise was wrong is
still information, and recording it is cheaper than rediscovering it at cutover.

### Row 11 — part 26 (scoping, no route landed): `rank/composite` measured end to end (2026-11-13)

This part landed **no route**. It read `rank/composite` end to end because it is the largest single route left, and
stopping to record what it consists of is cheaper than implementing half of it twice.

Measured shape, from `internal/httpapi/criteria_api.go:175`, `internal/store/criteria_store.go:423` and
`internal/ranking/criteria.go:98-360`:

* **POST**, body optional. Empty body = "rank with every criterion's default weight".
* `weights` keys must parse as criterion ids — a non-numeric key is **400** with the key echoed.
* `profile` (when no weights) loads a saved profile via `GetCriteriaProfileWeights`.
* `include_inactive` and `require_min_rd` are passed through. `include_inactive` off means an inactive criterion is
  excluded, because deactivating is how a project retires a dimension. `require_min_rd` keeps a criterion only if at
  least one rating in it has been **played** (`Games > 0` or `RD < InitialRD - 1e-9`) — an all-initial criterion
  contributes an arbitrary 0.5 to every feature and dilutes the result with no vote behind it.
* **Weights are the whole query when any are named**: an unnamed ACTIVE criterion is EXCLUDED, not defaulted. The old
  behaviour (unnamed criteria join at their default) made "rank by design only" blend efficiency in at equal weight.
* `normaliseWeights` drops negative weights, sums, and normalises; when the total is `<= 1e-9` every weight becomes
  0 (so nothing contributes rather than everything blowing up).
* per-criterion ratings are min–max normalised: **one rating → 0.5** (not 1.0 — a single data point is not a top
  score), equal values → 0.5, and `lower_is_better` reverses the scale.
* `Result.Rank` is 1-based after a sort by `Score desc, FeatureID asc` with a `1e-9` tolerance, so a tie is
  deterministic and a "rank 3" claim is reproducible.
* `confidence = TotalConfidence(contributions)`: weight-weighted mean of the contributions' `RD`, falling back to the
  plain mean when the weights sum to zero, and `InitialRD` when there are no contributions at all.
* empty (`results == null`) becomes `[]` before serialising, and `count` is the length after `limit` is applied.

**Not yet read (and not guessed):** `criteria.go:120-143` — the direction branch inside `normalised` and the tail of
`normaliseWeights`. Those two blocks are the only unknown, and the golden test cannot be written without them.

**Next action, exactly:** read those 24 lines; add `crates/ranking/src/composite.rs` with `Composite`,
`normalise_weights`, `normalised`, `total_confidence`; extend `crates/ranking/tests/golden.rs` with a Go-emitted oracle
over a 3-criterion × 5-feature grid crossing `lower_is_better`, a single-rating criterion, and equal ratings; then the
route and a probe whose negative control is "unnamed criteria join at their default" — the behaviour the Go
deliberately removed.

### Row 11 — part 27: composite ranking, finished (2026-11-13, `d514d8f`)

```
gate (before): POST /projects/{id}/rank/composite -> 404
gate (after):
  probe_s5_composite.py -> 19/19 ok
  routes.py             -> go 216 / port 98   missing: 120 (110 json, 10 page)
  cargo test -p concord-db --lib -> 33 passed
  cargo test -p concord-ranking -> 25 + 3 + 2 passed
```

Part 26 scoped this route and named 24 unread lines as its only unknown; they are read and the route is in.
`crates/ranking/src/composite.rs` ports `composite`/`normalised`/`normalise_weights`/`total_confidence`;
`crates/db/src/criteria.rs` gains `criterion_ratings` (one query, not N+1) and `profile_weights` — an absent profile is
**404**, because an empty weight list means "rank with every default", a different query that would answer a typo with
a plausible ranking.

**Correction to part 26's note, and the correction that matters:** part 26 said a non-positive weight total becomes
`0`. It becomes **uniform** (`1/n`), and zero would have reported every feature as scoring nothing. The Go was read
again rather than trusted: `normaliseWeights` divides `1/len(out)`.

**Two of my expectations were wrong and the probe said so** — the useful outcome, not a setback:

* an ACTIVE criterion nobody has played still contributes at its own weight, so the no-body ranking explains the top
  feature with **three** contributions and a confidence of **183.333**, not two and 100.0. That number *is* the
  dilution `require_min_rd` exists to remove, so the probe now asserts both sides: 183.333 with the flag off,
  **100.0** with it on;
* `include_inactive` yields **four** contributions, not three.

**NEGATIVE CONTROL** (the behaviour the Go deliberately removed — unnamed criteria joining at their own default):

```
FAIL naming one weight makes it the whole query: got 3 want 1
FAIL and drops the criterion it did not name: got ['cost', 'speed', 'unplayed'] want ['speed']
17/19 ok
```

**Found by failing:** `Content-Type: application/json` with an *empty* body makes axum's `Json` extractor answer 400,
so a client that always sends the header cannot make the "no body means every default" request at all. The probe sends
the header only when there is a body, and the helper carries the reason.

### Row 11 — part 28: the arena vote log, and the audit view that cannot audit (2026-11-13, `3013a45`)

```
gate (before): GET /projects/{id}/arenas/{arena}/votes -> 404
gate (after):
  probe_s5_arenavotes.py -> 17/17 ok
  routes.py              -> go 216 / port 99   missing: 119 (109 json, 10 page)
  cargo test -p concord-db --lib -> 33 passed
  concord-parity         -> 0 shape(s) disagreed of 16 checked, 5 skipped
```

Rules carried: a **SKIP is not a vote** (it means "I don't understand this", not "neither"); newest first; **never
null**, so a client cannot confuse "no votes" with "null"; **arena ids are global**, so another project's arena answers
exactly what a missing one answers (404) and a non-numeric id is 400; **no authentication at all**, because an audit
view that only insiders can read is not an audit view.

**PINNED DEFECT, carried deliberately.** Each vote's two sides report `r=0, rd=0, games=0, conservative=0`, because
`ListArenaVotes` selects only `v.a`/`v.b` and never joins the entries. §5.2 calls this route "the appeals view", and an
appeals view that cannot show what a vote **changed** is a poor one. The port reproduces it — the probe asserts the
zeros — so that the two services agree while equality is still the test, and it joins the row-13 defect list.

**NEGATIVE CONTROL** (skips included — the plausible misreading):

```
FAIL a skip is not a vote: got 3 want 2
FAIL newest first: got ['tie', 'skip', 'a'] want ['tie', 'a']
FAIL the reason travels with its vote: got 'do not understand' want 'clearer'
14/17 ok
```

**Found by failing:** `arena_entries` has no `project_id` column — it is scoped by `arena_id`, so a fixture cleans up
through a subquery on `arenas`.

Row 13's carry-in now stands at: **109 JSON routes**, 10 page routes, the four tallies defects, the vote-log zeros, and
the FTS5-vs-tsquery equivalence question.

### Row 11 — part 29: next-pair, and the discount that is not a bonus (2026-11-13, `805f69b`)

```
gate (before): GET /projects/{id}/arenas/{arena}/next-pair -> 404
gate (after):
  probe_s5_nextpair.py -> 15/15 ok
  routes.py            -> go 216 / port 100   missing: 118 (108 json, 10 page)
  cargo test -p concord-db --lib -> 36 passed
```

`crates/db/src/arena.rs` gains `pair_selection_cost`, `judged_pairs` and `next_pair`, with three unit tests pinning the
rules that are easy to invert: **uncertainty is a DISCOUNT**, a pair including the **baseline is discounted by 150**
("beats doing nothing" is the question every solution answers first), and a pair's identity **does not depend on which
side it came from**. A **skip is not a judgement** — the pair stays open and is asked again — while a real vote closes
it for that voter and for that voter only.

**What the rule actually says, which my first fixture got wrong:** the discount is `0.5 * mean(RD)`, so high
uncertainty does **not** rescue a wide rating gap. Two untouched entries 20 points apart cost `20-175 = -155`; a fresh
entry 100 points from a measured one costs `100-107.5 = -7.5` and loses to a measured pair at `-20`. The probe asserts
that shape now instead of the wish that "untouched always wins".

**NEGATIVE CONTROL** (uncertainty as a BONUS — the historic bug its own tests caught):

```
FAIL the proposed pair is the two untouched entries: got False want True
FAIL its sides are labelled with their titles: got {'Measured close', 'Measured strong'} want {'Untouched', 'Untouched too'}
13/15 ok
```

**Found by failing:** `arena_entries` has neither `project_id` nor `feature_id` — it is scoped by `arena_id` and
identifies its competitor by `(entity_type, entity_id)`. Also fixed in the probe itself: it captured the first pair
*after* a later call had reassigned the response, so two assertions compared the wrong arena's answer — a probe bug that
would have made a green run meaningless.

### Row 11 — part 30: arena verification, and a control that lied (2026-11-13, `9f3bfd2`)

```
gate (before): GET /projects/{id}/arenas/{arena}/verification -> 404
gate (after):
  probe_s5_verify.py  -> 20/20 ok
  routes.py           -> go 216 / port 101   missing: 117 (107 json, 10 page)
  cargo test -p concord-db --lib -> 36 passed
  cargo test -p concord-ranking  -> 28 + 3 passed
  concord-parity      -> 0 shape(s) disagreed of 16 checked, 5 skipped
```

§5.1's "anyone can verify a ranking" is now a route that **writes nothing**. `crates/ranking` gains
`update_arena_rating` (`weight <= 0` unchanged; `weight < 1` interpolates the whole update, **including rd and sigma**),
`games_for_weight` (a weight below 1 is still **one** game — truncating recorded a 0.4-weight voter as no game at all)
and `repeated_game`. `crates/db/src/arena.rs` gains `replay()` and `verify()`: votes apply in `(created_at, id)` order
because the update depends on the ratings the pair had when the vote was cast; `neither` in an arena with a baseline is
**two losses to the baseline**, written as two numbered steps because when a side IS the baseline the Go rates it as a
loss and then a win; and `agrees` is **exact float equality**, not a tolerance — a tolerance would hide the drift this
route exists to find.

**The strongest assertion:** after a vote the stored ratings disagree; write the *replayed* values back and the check
must agree again. Self-consistency proved rather than asserted.

**NEGATIVE CONTROL** (baseline branch removed — `neither` becomes a plain tie):

```
FAIL the baseline gains TWO games from one neither: got 0 want 2
FAIL and rises above the prior: got False want True
FAIL and both fall: got (False, False) want (True, True)
17/20 ok
```

**And the first attempt at that control reported 20/20, which was a lie.** The mutation did not compile, the build error
went into a stream I did not read, and `scp` deployed the **previous** binary — so the probe ran against correct code and
passed. A control is evidence only once the build is known to have succeeded and the binary is known to have changed; the
rerun checks the build output before deploying. **A green control on unfixed code is the exact failure mode this whole
ledger is built to prevent, and it happened here through an unchecked exit status.**

**Found by failing:** `arena_entries.is_baseline` is a real `BOOLEAN` in PostgreSQL, so `coalesce(is_baseline, 0) <> 0`
fails with "COALESCE types boolean and integer cannot be matched" — the query answers 500 and the route dies.

### Row 11 — part 31: project solution standings (2026-11-13, `33eecff`)

```
gate (before): GET /projects/{id}/solutions -> 404
gate (after):
  probe_s5_standings.py -> 12/12 ok
  routes.py             -> go 216 / port 102   missing: 116 (106 json, 10 page)
  cargo test -p concord-db --lib -> 36 passed
```

Two rules carried from the spec, each a way to be plausible and wrong:

* standings are **grouped per feature and never merged**: a solution's score is computed against the entries competing
  with it, so a project-wide "best solution" ranks numbers that were never comparable — and a merged list would let one
  feature's do-nothing baseline outrank another feature's real proposal;
* a feature with **no** solutions is **included** with an empty array and counted in `features_without`: "one feature has
  proposals and four do not" is the useful sentence, while a panel that omits the four reads as if one were the project.

Plus truncation: five shown per feature, `omitted` from an explicit `COUNT(*)`. The standings walk the arena and skip
entries whose solution row is gone, so "one more row" is not a witness of "one more solution" — the Go's own note, now
the port's.

**NEGATIVE CONTROL** (features with no solutions skipped):

```
FAIL every feature is present, including the one nobody proposed for: got [5941] want [5941, 5942]
FAIL the idle feature answers an empty ARRAY and not null: got None want []
FAIL with nothing omitted: got None want 0
9/12 ok
```

**The probe had to be hardened first**: its indexed lookups raised `KeyError` on the very failure they exist to detect, so
the control produced a traceback instead of assertions. A probe that crashes on the failure it is built to find reports
nothing; every lookup is now `.get(..., {})`.

### Row 11 — part 32: one feature's solution standings, and two families that disagree (2026-11-13, `0b68ffe`)

```
gate (before): GET /projects/{id}/features/{feature}/solutions -> 404
gate (after):
  probe_s5_featuresolutions.py -> 14/14 ok
  routes.py                    -> go 216 / port 103   missing: 115 (105 json, 10 page)
  cargo test -p concord-db --lib -> 36 passed
  concord-parity               -> 0 shape(s) disagreed of 16 checked, 5 skipped
```

Rules carried: a feature nobody has proposed for answers an **empty array**, not 404; `limit` defaults to **100** and
only a positive integer overrides it — junk and `0` fall back rather than erroring; a missing feature is 404 and a
non-numeric id 400.

**The rule worth the ink:** a feature belonging to **another project** answers **403** here, while the complaint family
answered **404** for the same situation (part 18). Two routes, two choices, written on different days. The port
reproduces each family's own behaviour instead of harmonising them behind the service's back — harmonising would make the
port disagree with the live service in one of the two places, and the parity suite would then have to be taught to
tolerate a difference I invented.

**NEGATIVE CONTROL** (cross-project feature made 404, i.e. "harmonised" with the complaint family):

```
FAIL another project's feature is 403 on this family: got 404 want 403
13/14 ok
```

A one-assertion control is weak evidence, and it is the right shape here: fourteen assertions, and this mutation can only
move one of them — a fourteen-way break would have meant the mutation changed something else.

### Row 11 — part 33: one solution, its rating, its claims (2026-11-13, `2cc7fb9`)

```
gate (before): GET /projects/{id}/features/{feature}/solutions/{solution} -> 404
gate (after):
  probe_s5_solution.py -> 19/19 ok
  routes.py            -> go 216 / port 104   missing: 114 (104 json, 10 page)
  cargo test -p concord-db --lib -> 36 passed
```

Rules carried: a solution **not yet in an arena** is still a solution (LEFT JOIN, rating zeros, `arena_id` 0 — an inner
join would 404 a proposal that exists); coverage claims ordered by complaint id, an unchallenged claim's `contested_by` /
`contested_at` / `contest_reason` **null**; `is_baseline` comes from the solution's **TYPE** (`do-nothing`), not from the
arena entry's flag, so the two cannot disagree — deriving it is *why* the current service stopped scoring every
`neither` as a draw; and a solution belonging to **another feature** of the same project is **404** while a feature from
another project is **403**. Two sizes of "not yours" inside one route family, both reproduced.

**NEGATIVE CONTROL** (`is_baseline` taken from the arena entry's flag — the column nothing populated):

```
FAIL a do-nothing proposal is the baseline by its type: got False want True
18/19 ok
```

**Found by failing, twice:** sqlx implements `FromRow` for tuples up to sixteen elements and this projection is
twenty-one, so the tuple form does not compile (rewritten to `Row` accessors); and `solution_coverage.contested` is an
**INTEGER** column here, so `coalesce(contested, false)` dies with "COALESCE types bigint and boolean cannot be matched" —
the same trap as `arena_entries.is_baseline` being a real `BOOLEAN`, in the opposite direction. A schema skimmed from the
Go's scan targets gets both wrong.

## Layout change, 2026-11-13: the Go tree archived, the Rust tree moved

Decision, recorded here because it changes every path in this ledger:

* the **Go implementation** — the reference until cutover — now lives at **`~/code/archive/concord`**
  (`/personal/documents/code/archive/concord` on thinkcentre), frozen at this repository's `port-rust` commit `eb10320`.
  It carries `ARCHIVE.md` with its provenance. It has no `.git`: history for every file stays in this repository, which
  is why the removal below is a commit and not a delete;
* the **Rust implementation** — this checkout — moved from `~/code/projects/concord` to **`~/code/rust/concord`**;
* the paths *inside* this ledger that say `crates/…`, `scripts/parity/…` or `docs/…` are unchanged and relative to the new
  root. `CONCORD_GO_SRC`, if set, points elsewhere for the reference; otherwise `scripts/parity/routes.py` resolves this
  repository first and `~/code/archive/concord` second, and **refuses to report an inventory** when it finds neither —
  an inventory computed against a missing tree would read as a finished port.

What did NOT change, and is the point of keeping the archive: the Go service still serves `concord.polarisocial.xyz`
until the cutover row, its binary is `/home/alvaro/concord-deploy/concord` built from the archived tree, and this port's
gate is still behavioural — the parity corpus replays responses recorded from the LIVE service and compares them with the
Rust candidate. Nothing in the Rust tree compiles against the archive.

Consequence for the remaining rows: every "read the Go first" step now reads `~/code/archive/concord`. The route
inventory gate is unchanged at its last measurement — 216 Go routes, 104 answered, 114 missing (104 JSON, 10 page) —
because the reference is the same code, moved.

### Blocked

**B1. (closed 2026-11-13 — decided without a deploy; see row 6's Done entry.** The corpus
stays a recording of `pre-merge-2026-11-05-63-g313b4ee` and is re-recorded after the cutover
deploy, before row 13 closes.)

### Done *(2 — 11 open)*

**7. S1 projects, documents, board** — done 2026-11-13, commits `07b1663` and this one, pushed to
`forgejo/port-rust` and `github/port-rust`.

```
$ scripts/parity/e2e.sh
test result: ok. 22 passed; 0 failed; 0 ignored
$ cargo mutants -p concord-db --in-place
119 mutants tested: 97 caught, 1 missed, 19 unviable
$ scripts/parity/probe_s1_api.py
ok  register: 201 / place derived card: 200 / second into full wip=1: 409 / no token: 401
probe exit=0
$ CANDIDATE=http://127.0.0.1:8107 concord-parity --corpus s1corpus.json
0 shape(s) disagreed of 5 checked, 0 skipped
```

The one surviving mutant (`place_feature`'s `current != Some(column)` → `true`) is provably
equivalent, not ignored: the branch calls `check_wip`, whose own already-in-this-column guard returns
`Ok`, and two tests pin that pair from both sides.

The gate line this row was written with — `projects_e2e`, `board_e2e`, `pages_e2e` green — cannot be
met before row 12: after its API half passes, `board_e2e` waits on `locator("#kanban-board
.kanban-card")`, the board *page*. Decision (2026-11-13, see "Decisions taken for this row"): a
slice's gate is its corpus subset plus its API behaviour proved by request, and the DOM assertions
move to row 12, whose gate becomes those suites green in full. The API half was proved both ways:
`probe_s1_api.py` against the Rust binary, and `board_e2e`'s seeding path against PostgreSQL.

**Decisions taken for this row, dated 2026-11-13, recorded rather than asked:**

1. **Write routes are proved by probe, not by the corpus.** The corpus replays GETs against the live
   Go service; recording a `POST /api/v1/projects` would create a project on production to learn a
   shape the probe already asserts. The probe asserts status codes and the fields that matter and the
   corpus keeps the read surface.
2. **A slice's gate is its corpus subset plus its API behaviour proved by request;** the DOM
   assertions of the e2e suites belong to row 12, where the frontend lands.
3. **The estate's stack declaration follows the port, not the other way round** — `runtime = "go+rust"`
   until row 13 restores `"rust"` and deletes the reason.
4. **The store's tests run against PostgreSQL, not a mock.** `scripts/parity/e2e.sh` recreates a
   scratch `concord_test` database each run: a store proved against a mock proves the mock.

**1. M0.1 Rust workspace skeleton on `port-rust`** — done 2026-11-13, commit `89057c8`, pushed to
`forgejo/port-rust` and `github/port-rust`.

```
$ cargo metadata --no-deps --format-version 1 >/dev/null && echo OK
OK
```

Five members (`concord-domain`, `concord-db`, `concord-app`, `concord-parity`, `concord-cli`), the
workspace dependencies of the plan, and the six family crates by path. The path depth is counted
from `Cargo.toml`, not copied: the checkout is `<code>/projects/concord` and the family crates are
siblings of `<code>/projects`, so `../../polaris-*`. `go build ./...` passes on the same commit, as
every commit on this branch must while the Go tree serves traffic. The gate before the change was
`error: could not find Cargo.toml in .../projects/concord or any parent directory`.

Two decisions the row left open, taken here: `/target/` added to `.gitignore` (the tree had no
Rust ignore rule), and `Cargo.lock` is committed — this is a binary product, so the lock file is
part of the build, and the branch must be reproducible on the host that deploys it.

**2. M0.2 PostgreSQL 18 on the concord cluster, port 55450** — done 2026-11-13, commit (this one; host
state plus this ledger edit).

```
$ psql "$DATABASE_URL" -tAc "select version()"
PostgreSQL 18.7 (Ubuntu 18.7-1.pgdg24.04+2) on x86_64-pc-linu...

$ grep -c 55450 ~/.config/concord/.env
1
```

`pg_lsclusters` now lists `18 concord 55450 online` beside the four 16 clusters, which did not move;
the live Go service answered `200 0.100743s` on `https://concord.polarisocial.xyz/` with the unit
still `active` after the install, so the invariant held through the row.

Two deviations, and why each was taken rather than worked around:

- **`noble-pgdg`, not `<codename>-pgdg`.** This host is Linux Mint 22.3 "zena"; pgdg publishes no
  `zena` suite, and the first attempt with the detected codename failed exactly as it should —
  `E: The repository 'https://apt.postgresql.org/pub/repos/apt zena-pgdg Release' does not have a
  Release file`. Mint 22.3 is Ubuntu 24.04 underneath, so the suite is `noble-pgdg` and the package
  that landed is `18.7-1.pgdg24.04+2`.
- **The database's timezone is set to UTC on the database itself** (`alter database concord set
  timezone to 'UTC'`), not left at the cluster default. The Go tree's timestamps travel as
  contiguous values, so every reader here compares them without a session zone in play; making UTC
  a property of the database means a psql check cannot be the one thing that reads local time.

The role password is generated on the host (`openssl rand -hex 24`), written only to
`~/.config/concord/.env` (mode 600), never printed and never in git.

**3. M0.3 parity recorder, seed, two stable recordings** — done 2026-11-13, commit with this edit.

```
$ python3 scripts/parity/record.py --seed scripts/parity/seed.txt --out /tmp/r1.json
17 shapes -> /tmp/r1.json
$ python3 scripts/parity/record.py --seed scripts/parity/seed.txt --out /tmp/r2.json --previous /tmp/r1.json
17 shapes -> /tmp/r2.json
$ diff <(stable r1) <(stable r2)
DIFF EMPTY            # 16 shapes, 15 compared byte-for-byte, 1 declared time-varying
```

The seed as written in the plan was **four beliefs and twelve facts**. Probed against the deployed
host before the recorder ran:

| seed path | answer | what the tree says |
| --- | --- | --- |
| `/api/v1/projects/concord/features/5785` | **404** | `features/{id}` exists; 5785 does not. `5755` answers 200 — the id was invented |
| `/api/v1/projects/concord/stats` | **404** | stats is global: `/api/v1/stats` answers 200 with 6,177 bytes |
| `/api/v1/projects/concord/arenas` | **404** | `internal/httpapi/server.go:627` registers `.../projects/{project_id}/arenas` — numeric id, and newer than the deployed build |
| `/api/v1/projects/concord/consensus/feed.xml` | **404** | `feeds_handlers.go:394` registers it; `/projects/concord/feed.xml` also 404, so the deployed build predates the feed routes |

The three that answer 404 on the host but exist in the tree are **commented out of the seed with the
reason**, not recorded: freezing a 404 into the corpus would make the port owe a piece of deploy
drift, and uncommenting them is a one-line change once the host carries the tree being ported.

Two things the plan's code could not do, both found by running it:

- **`unstable` could never fire across runs.** The seed visits each shape once, so the per-run check
  compares a shape against itself; two recordings a minute apart differed at `/api/v1/stats` and
  neither was flagged. Added `--previous` (defaults to `--out` if it exists) so run 2 compares
  against run 1, and the gate is now a real diff.
- **A time-varying shape had no way to say so.** `/api/v1/stats` carries the moment it was computed:
  same length (6,177 bytes), different hash every run. The seed declares it — `# time-varying:
  /api/v1/stats` — and the recorder stamps `time_varying: true`; the gate excludes declared shapes
  and nothing else. A shape that is skipped silently is a shape nobody remembers to port.

The corpus also records which build it describes (`__meta.version` = `pre-merge-2026-11-05-63-g313b4ee`),
because the answer to "is this corpus stale?" was otherwise a judgement call.

**4. M0.4 baseline schema, mapped row by row** — done 2026-11-13, commit with this edit
(`crates/db/sql/0001_baseline.sql` 1,115 lines + `scripts/parity/ddl.py`).

```
$ psql -c '\dt' | grep -c '^ public'                                   79
$ information_schema columns where data_type='real'                    0
$ columns named public_id                                              7
$ columns where data_type='tsvector'                                   2
$ columns where data_type='boolean'                                    7
$ psql -v ON_ERROR_STOP=1 -f crates/db/sql/0001_baseline.sql           79 relations, no error
```

Before the change: `select count(*) from pg_tables where schemaname='public'` -> **0**, and
`\d features` -> `Did not find any relation named "features"`.

The row's verify line said `83` ± eight shadows. Measured on the live database: **91** tables = 11
fts-related + 1 `sqlite_sequence` + **79** real, so 79 is the only count that means anything. That
is now the gate.

Generated rather than typed (39 migrations, 79 tables — a hand translation drifts on the next
migration), with every judgement in `OVERRIDES` or in a rule the code refuses to apply naively.
Five places where the plan's mapping is not safely mechanical, all found by applying it:

- **R2 (`INTEGER` holding 0/1 -> boolean) converts counters.** `project_metrics.stars`,
  `project_metrics.forks`, `users.trust_level`, `trust_grants.level`,
  `charters.merge_requires_quorum`, `features.milestone_position` all hold only 0 and 1 in today's
  data and none is a flag. A boolean now also requires a boolean-shaped name (`is_`, `has_`,
  `needs_`, `requires_`, or a known flag word); **10 columns the data test alone would have
  converted stay `bigint`**. The plan's test ("check the column never holds 2") is necessary and not
  sufficient, which is the same lesson row 3's draw produced.
- **R2 must also refuse foreign keys and id-shaped names.** `capability_assertions.asserted_by`
  holds 0 and 1 because the table has two rows; PostgreSQL refused the FK with
  `Key columns "asserted_by" ... are of incompatible types: boolean and bigint`, which is the rule
  stating itself. Ids ending `_id`, and `by`/`actor`/`owner`/`author`, are excluded outright.
- **FKs cannot be inline.** Alphabetical order puts a child above its parent and PostgreSQL will
  not reference a table that does not exist yet: they are added by `ALTER TABLE` after every table
  exists, which also makes the file's table order irrelevant.
- **Single-column non-integer primary keys must keep the constraint.** `capabilities.key` is
  `TEXT PRIMARY KEY`, and a translation that emits `key text` leaves the FK pointing at a column
  that is no longer unique — `ERROR: there is no unique constraint matching given keys for
  referenced table "capabilities"`.
- **R7 needs a decision on the text-search config.** `to_tsvector('simple', ...)` and not
  `'english'`: fts5's `unicode61` tokeniser does no stemming, so an English config in the move would
  silently change which rows match. One generated column per table (`search_tsv`), not one per
  indexed column, because search queries the union.

Two facts the row surfaced that the slices owe: `projects_fts` indexed **`tags` and `languages`**,
which are not columns of `projects` — the fts row was assembled by the writer, so the generated
`search_tsv` covers `name` and `description` only and the slice that serves search must decide what
replaces the rest. And `api_tokens`, `admin_ledger`, `act_reviews` came through as ordinary tables,
so the port's schema is 79 relations and not the 83 the plan assumed.

**5. M0.5 data mover loads and counts agree** — done 2026-11-13, commit with this edit
(`scripts/parity/move.py`; `crates/db/sql/0001_baseline.sql` regenerated).

```
$ python3 scripts/parity/move.py
42071 rows, 0 tables failed
$ psql -c 'select (select count(*) from projects) p, (select count(*) from features) f,
           (select count(*) from complaints) c, (select count(*) from users) u'
73 5760 2402 88          # the gate's four numbers
```

Before the change the same counts were `0 0 0 0` and the move failed on 31 tables. Re-run for
idempotency: `42071 rows, 0 tables failed` again, same four counts.

Fidelity, spot-checked rather than assumed: `features.id=1` carries
`created_at 2026-09-29 07:56:31+00` and a 26-character `public_id`; the newest feature is
`2026-11-12 19:26:17+00` with a 1,726-byte body; **0 rows** have a timestamp outside
2020..now (which is what a numeric-as-timestamp mistranslation would produce); `members.is_moderator`
counts `t|3, f|78`; `audit_log` moved 9,313 rows with `detail` non-null on all 9,313, matching the
source exactly.

Three things the plan's sketch would have got wrong, all found by running it:

- **`column_default is null` as the column filter drops every timestamp.** `created_at` has
  `DEFAULT now()` in the baseline, so filtering defaulted columns away would carry the rows without
  their dates and the corpus would compare a database that had forgotten when everything happened.
  The filter here is `is_generated = 'NEVER'` intersected with the source's own columns.
- **The table order cannot be typed out.** It is derived from PostgreSQL's own `pg_constraint`
  foreign keys (depth-first), so the first migration that adds a table does not silently produce a
  load that inserts a child before its parent.
- **`public_id` is deterministic, not random.** A ULID-shaped 26-character value derived from
  `sha256(table:id)`: deterministic so a re-run produces the same ids and the corpus stays
  comparable across rebuilds, ULID-shaped so the column matches what the slices will generate.

`psycopg2` 2.9.13 rather than the plan's `psycopg` (v3): v3 is not installed on this host, and the
difference is a driver import, not a design.

**Row 4's artifact changed after row 4 closed, and that is recorded rather than quietly rewritten.**
The move failed with
`duplicate key value violates unique constraint "idx_arena_entries_one_baseline"` because the
generator rebuilt each index from `pragma index_info`, which returns neither the `WHERE` clause of a
partial index nor the columns of an expression index. `WHERE is_baseline = 1` had become a full
unique index, refusing rows the source accepts. `ddl.py` now translates the index DDL from
`sqlite_master` itself — predicate kept, `ifnull` mapped to `coalesce`, and `col = 1` on a boolean
column mapped to the column, because PostgreSQL has no `boolean = integer`.

**6. M0.6 replay tool passes against the CURRENT Go service** — done 2026-11-13, commit with this
edit (`crates/parity/src/main.rs`, `crates/parity/corpus.json`, `scripts/parity/record.py`).

```
$ CANDIDATE=https://concord.polarisocial.xyz cargo run -q -p concord-parity -- \
      --corpus crates/parity/corpus.json
0 shape(s) disagreed of 16 checked, 0 skipped
$ ... --verbose
/api/v1/projects: ok ([73:{created_at:number:int,description:string:Tauri ebook library manager...
/api/v1/projects/concord/board: ok ({cards:[370:{...}],columns:[9:{name:string:inbox,...
```

Negative control, because a gate that cannot fail is not a gate:
`CANDIDATE=https://concord.polarisocial.xyz/wrong` -> `16 shape(s) disagreed of 16 checked`,
exit **1**, against exit **0** for the real candidate.

The plan's sketch compared **only the status code** while its own doc comment promised a structural
diff. A 200 says the route exists and nothing about the payload, and the payload is what S1–S5 are
judged on. The binary now compares bodies by shape: key order normalised (objects are key-sorted),
array length compared with the first element standing for the rest, and volatile leaves — ids,
timestamps, versions, cursors, totals — reduced to their *type* so a timestamp that becomes a string
still fails. Two decisions that follow from it:

- **The recorder stores the body, not a shape it computed itself** (682 KB of JSON for 16 shapes).
  Two implementations of "structurally equal" drift and then agree with each other instead of with
  the service; one implementation, run over both bodies, cannot. If 682 KB ever hurts, the move is
  gzip, not a second shape function.
- **The corpus records the path as asked, not only the shape.** The sketch replays `/:id` as `/0`,
  which asks a candidate whether it has a row zero — a different question, and one that turns every
  id-shaped route into a guaranteed 404 against a service that works.

`reqwest` with `rustls-tls` and a `curl/8` user agent: Cloudflare answers 403 to a bare reqwest UA,
which would have made every replay look like a parity failure.

**B1 is closed by decision, not by a deploy** (2026-11-13): the corpus stays a recording of
`pre-merge-2026-11-05-63-g313b4ee`, and the replay is re-run against the host after the cutover
deploy, before row 13 is called done. Reason: the ADR's invariant is that the Go service keeps
serving, and sending 87 commits of drift to a live service to make a fixture match its source tree
trades a certainty (the port compares against the build that is actually in front of users) for a
risk (a redeploy nobody asked for, on the day the port starts). Where the tree has a route the
deployed build lacks — `arenas`, `consensus/feed.xml`, the project-scoped `stats` — the corpus is
silent rather than 404, so the port implements the tree's route and the cutover replay is where that
divergence gets its first real test.


## M0 — the ground (no product code)

### M0.1 Branch and workspace skeleton (20 min)

```sh
cd ~/code/projects/concord
git switch -c port-rust
```

`Cargo.toml` (workspace only — the Go module stays at the root and is untouched):

```toml
[workspace]
resolver = "2"
members = ["crates/domain", "crates/db", "crates/app", "crates/parity", "crates/cli"]

[workspace.package]
version = "0.0.0"
edition = "2021"
rust-version = "1.82"
license = "AGPL-3.0-or-later"
repository = "https://git.polarisocial.xyz/concord/concord"

[workspace.dependencies]
anyhow = "1"
axum = { version = "0.8", default-features = false, features = ["http1", "json", "query", "tokio", "matched-path"] }
chrono = { version = "0.4", features = ["serde"] }
serde = { version = "1", features = ["derive"] }
serde_json = "1"
sqlx = { version = "0.8", default-features = false, features = ["runtime-tokio", "postgres", "macros", "chrono", "uuid"] }
thiserror = "2"
tokio = { version = "1", features = ["macros", "rt-multi-thread", "signal"] }
tower-http = { version = "0.6", features = ["trace", "compression-gzip"] }
tracing = "0.1"
tracing-subscriber = { version = "0.3", features = ["env-filter", "json"] }
ulid = { version = "1", features = ["serde"] }
uuid = { version = "1", features = ["v4", "serde"] }
# estate crates, by path while the port is a branch; by version at cutover
polaris-core = { path = "../../polaris-core-rust" }
polaris-healthz = { path = "../../polaris-healthz" }
polaris-authz = { path = "../../polaris-authz" }
polaris-ledger = { path = "../../polaris-ledger" }
polaris-ratelimit = { path = "../../polaris-ratelimit" }
polaris-slug = { path = "../../polaris-slug" }
```

Apply the same relative-path convention darkhold uses: the sibling checkouts are at
`~/code-local/polaris-*`, and `~/code-local/rust/darkhold/Cargo.toml` writes them as
`../../../../polaris-core-rust` — count the depth from your crate, do not copy that number.

**Verify:** `cargo metadata --no-deps --format-version 1 >/dev/null && echo OK` → `OK`.
(The members do not exist yet; create each as `cargo new --lib crates/<name>` first, then this
metadata call succeeds.)

### M0.2 The PostgreSQL decision (30 min, decision rule written down)

The profile says PostgreSQL 18 (`STACK.md` §1). This host runs **16.15**: clusters `main` 5432,
`boards` 55442, `suki` 55440, `suki-test` 55441 (`pg_lsclusters`).

1. Try the profile's answer first:

```sh
ssh thinkcentre 'sudo install -d /usr/share/postgresql-common/pgdg && \
  sudo curl -fsSL -o /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc \
    https://www.postgresql.org/media/keys/ACCC4CF8.asc && \
  echo "deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] \
    https://apt.postgresql.org/pub/repos/apt noble-pgdg main" | sudo tee /etc/apt/sources.list.d/pgdg.list && \
  sudo apt-get update && sudo apt-get install -y postgresql-18 && pg_lsclusters'
```

2. If 18 appears, create the cluster and stop:

```sh
ssh thinkcentre 'sudo pg_createcluster 18 concord --port 55450 --start && \
  sudo -u postgres psql -p 55450 -c "create role concord login password '"'"'concord'"'"';" && \
  sudo -u postgres createdb -p 55450 -O concord concord && \
  psql "postgres://concord:concord@127.0.0.1:55450/concord" -c "select version();"'
```

3. If the install fails or fights, run on 16 and **record the deviation in the same commit**:
   `[site.stack] reason = "postgres 16.15, the host's clusters are 16 and the port does not
   depend on 18-only features: tsvector, GIN, generated columns and ULIDs-as-text all exist in
   16"`, and open an estate backlog row for the upgrade. A silent downgrade is the failure mode
   this step exists to prevent.

**Verify:** `psql "$URL" -c 'select version()'` names the server version, and
`grep -c 55450 ~/.config/concord/.env` is `1`.

### M0.3 The parity recorder (60 min)

`scripts/parity/record.py` — records the live API's shapes. It records, it does not write: every
request is a GET or a POST whose path holds no mutation (the recorder refuses `DELETE`, `PUT`,
`POST` outside an allowlist, so pointing it at production cannot change production).

```python
#!/usr/bin/env python3
"""Record the live /api/v1 shapes into a parity corpus (W11 phase 0, applied to Concord).

The corpus is what the port is judged against, so it is recorded from the DEPLOYED host and
deduplicated by (method, path-shape, body-shape). A path segment that is a number is replaced by
:id so 5,760 feature pages collapse into one shape; query parameters are sorted and kept.
"""
import argparse, hashlib, json, re, sys, urllib.request, urllib.error

BASE = "https://concord.polarisocial.xyz"
ID = re.compile(r"/\d+(?=/|$)")

def shape(path: str) -> str:
    return ID.sub("/:id", path)

def fetch(method: str, path: str):
    req = urllib.request.Request(BASE + path, method=method)
    req.add_header("User-Agent", "curl/8")          # Cloudflare 1010s the python UA
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, dict(r.headers), r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), e.read().decode("utf-8", "replace")

def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--seed", required=True, help="file of paths to walk, one per line")
    ap.add_argument("--out", required=True)
    args = ap.parse_args()
    corpus = {}
    for line in open(args.seed):
        path = line.strip()
        if not path or path.startswith("#"):
            continue
        status, headers, body = fetch("GET", path)
        key = shape(path)
        entry = corpus.setdefault(key, {
            "key": key, "status": status,
            "content_type": headers.get("content-type", ""),
            "estate_headers": {k: headers[k] for k in headers
                               if k.lower() in ("x-estate-site", "x-estate-version", "etag",
                                                "cache-control")},
            "body_sha": hashlib.sha256(body.encode()).hexdigest()[:16],
            "body_len": len(body),
        })
        # Two recordings of the same shape must agree, or the corpus is of a moving target.
        if entry["body_sha"] != hashlib.sha256(body.encode()).hexdigest()[:16]:
            entry.setdefault("unstable", 0)
            entry["unstable"] += 1
    json.dump(corpus, open(args.out, "w"), indent=1, sort_keys=True)
    print(f"{len(corpus)} shapes -> {args.out}")
    return 0

if __name__ == "__main__":
    sys.exit(main())
```

Seed file `scripts/parity/seed.txt` — the shapes that matter, written by hand once:

    /api/v1/version
    /api/v1/healthz
    /api/v1/projects
    /api/v1/projects/concord
    /api/v1/projects/concord/features
    /api/v1/projects/concord/features/5785
    /api/v1/projects/concord/complaints
    /api/v1/projects/concord/documents
    /api/v1/projects/concord/board
    /api/v1/projects/concord/milestones
    /api/v1/projects/concord/roadmap
    /api/v1/projects/concord/arenas
    /api/v1/projects/concord/audit
    /api/v1/projects/concord/stats
    /api/v1/projects/concord/consensus
    /api/v1/projects/concord/consensus/feed.xml
    /api/v1/stats/summary
    /api/v1/search?q=arena

**Verify:** `python3 scripts/parity/record.py --seed scripts/parity/seed.txt --out /tmp/c1.json`
prints `N shapes -> /tmp/c1.json`; run it twice and `diff <(jq -S . /tmp/c1.json) <(jq -S . /tmp/c2.json)`
is empty for every shape whose `unstable` field is absent. A shape that reports `unstable` is a
shape the port must not be judged on until it is made stable.

### M0.4 The baseline schema (90 min)

Generate PG DDL from the **live** schema, not from the 39 migrations:

```sh
ssh thinkcentre 'sqlite3 ~/.local/share/concord/concord.db ".schema" > /tmp/concord.sqlite.sql'
```

Then apply this mapping table by hand, table by table — it is a 91-row checklist, and every row
is a decision the generator cannot make for you:

| SQLite | PostgreSQL | why |
|---|---|---|
| `INTEGER PRIMARY KEY` | `bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY` | keep insert order; ULID goes in `public_id` |
| `INTEGER` holding 0/1 | `boolean` | only after checking the column never holds 2; `select distinct` it |
| `REAL` timestamps (unix seconds) | `timestamptz` via `to_timestamp(col)` | never `double precision`; a float timestamp is a bug waiting for a leap second |
| `REAL` scores/ratings | `double precision` | genuinely fractional |
| `TEXT` + `CHECK (x IN (...))` | `TEXT` + `CHECK` + a `CREATE TYPE ... AS ENUM` where the value set is closed | the enum makes gate 19's grep honest |
| `TEXT` JSON blobs | `jsonb` | `audit_log.detail` is read structurally |
| `*_fts` + shadow tables | **omitted**, replaced by `tsvector` + GIN on the source table | FTS5 does not exist in PG; the 8 shadow tables are SQLite internals |
| `feature_complaints`, `arena_entries` composite PKs | same, as `PRIMARY KEY (a, b)` | the ON CONFLICT targets depend on it |
| `NOT NULL DEFAULT (strftime('%s','now'))` | `NOT NULL DEFAULT now()` | |
| every table | `created_at`/`updated_at` stay `timestamptz NOT NULL` | the store scans them everywhere |

Plus, on the API-facing tables (`projects`, `features`, `complaints`, `users`, `documents`,
`milestones`, `consensus_calls`): `public_id text NOT NULL UNIQUE` with a ULID, generated at
insert. Backfill order is by `id` so the corpus's ids stay comparable.

**Verify:** `psql "$URL" -c '\dt' | wc -l` is `83` ± the shadow tables you dropped, counted
against `sqlite3 ... "select count(*) from sqlite_master where type='table'"` minus 8; and
`psql "$URL" -c '\d features'` shows `public_id`, `timestamptz` timestamps and no `REAL`.

### M0.5 The data mover (90 min)

`scripts/parity/move.py` — SQLite to PG, one transaction per table, in foreign-key order:

```python
#!/usr/bin/env python3
"""Move the live SQLite rows into the PostgreSQL baseline, one transaction per table.

Two rules, both learned the hard way in this repo:
  * the column list is read from the DESTINATION (`information_schema.columns`), never from the
    source: a source column that the baseline dropped (every `*_fts*` shadow) must not be
    silently re-inserted, and a destination column the source lacks must get its DEFAULT.
  * a table whose row count disagrees after the move ABORTS the whole run, because a partial
    load that reports success is the failure this script exists to prevent.
"""
import sqlite3, sys, psycopg, os

SQLITE = os.path.expanduser("~/.local/share/concord/concord.db")
PG = os.environ["CONCORD_PG_URL"]
ORDER = ["users", "projects", "members", "complaints", "features", "feature_complaints",
         "arenas", "arena_entries", "board_columns", "board_cards", "audit_log"]
# ... the full order is the baseline's FK graph, walked depth-first; ORDER above is the prefix
# that must be exact, the rest may follow any order that satisfies the graph.

def main() -> int:
    src = sqlite3.connect(f"file:{SQLITE}?mode=ro", uri=True)
    src.row_factory = sqlite3.Row
    dst = psycopg.connect(PG)
    for table in ORDER:
        cols = [r[0] for r in dst.execute(
            "select column_name from information_schema.columns where table_name=%s "
            "and is_generated='NEVER' and column_default is null order by ordinal_position",
            (table,))]
        rows = src.execute(f"select {','.join(cols)} from {table}").fetchall()
        with dst.transaction():
            with dst.cursor() as cur:
                cur.executemany(
                    f"insert into {table} ({','.join(cols)}) values ({','.join(['%s']*len(cols))})",
                    [tuple(r) for r in rows])
        n_src = src.execute(f"select count(*) from {table}").fetchone()[0]
        n_dst = dst.execute(f"select count(*) from {table}").fetchone()[0]
        if n_src != n_dst:
            sys.exit(f"{table}: {n_src} -> {n_dst}, aborting")
        print(f"{table}: {n_dst}")
    return 0

if __name__ == "__main__":
    sys.exit(main())
```

**Verify:** `CONCORD_PG_URL=... python3 scripts/parity/move.py` prints one line per table and
exits 0; then the four counts the port is judged on agree:

```sh
psql "$URL" -c "select (select count(*) from projects) p, (select count(*) from features) f,
  (select count(*) from complaints) c, (select count(*) from users) u;"
```
→ `73 | 5760 | 2402 | 88` (the live numbers on 2026-11-13).

### M0.6 The replay tool (60 min)

`crates/parity` — a binary that reads the corpus and diffs a candidate service against it:

```rust
// crates/parity/src/main.rs
//! Replay the parity corpus against a candidate service and diff structurally.
//!
//! Structural, not textual: key order is normalised, timestamps are compared as shapes
//! (a value of the right type), and ids by shape unless the corpus names them. A textual diff
//! over JSON reports every reordered key as a regression, which is how a parity gate gets
//! ignored.
use std::collections::BTreeMap;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let corpus: BTreeMap<String, serde_json::Value> = serde_json::from_str(
        &tokio::fs::read_to_string("corpus.json").await?)?;
    let base = std::env::var("CANDIDATE").unwrap_or_else(|_| "http://127.0.0.1:8106".into());
    let client = reqwest::Client::builder().user_agent("curl/8").build()?;
    let mut bad = 0usize;
    for (shape, rec) in &corpus {
        if rec.get("unstable").is_some() || rec.get("expected_change").is_some() {
            continue;
        }
        let path = shape.replace("/:id", "/0");
        let resp = client.get(format!("{base}{path}")).send().await?;
        let want = rec["status"].as_u64().unwrap_or(200) as u16;
        if resp.status().as_u16() != want {
            eprintln!("{shape}: status {} != {want}", resp.status().as_u16());
            bad += 1;
        }
    }
    println!("{} shape(s) disagreed", bad);
    std::process::exit(if bad == 0 { 0 } else { 1 });
}
```

**Verify:** `CANDIDATE=https://concord.polarisocial.xyz cargo run -p parity` prints
`0 shape(s) disagreed` against the **current Go service**. A replay tool that fails against the
service it was recorded from is broken, not strict.

### M0.7 Record the plan's own claims (15 min)

Every symbol this plan tells you to write is new, so the claim set to check is the *existing*
one: `grep -n "func (d \*DB) <Name>" internal/store/*.go` for each store function a slice plan
cites, and `cargo metadata` after every Cargo.toml edit. The two draft-corrections in
`docs/plans/milestone-consensus.md` are the local precedent for why.

---

## S1 — projects, documents, board

**Tables (primary):** `projects`, `project_documents`, `project_documents_fts`→`tsvector`,
`project_tags`, `tags`, `tag_aliases`, `project_languages`, `project_metrics`, `project_stats`,
`charters`, `members`, `board_columns`, `board_cards`, `project_invites`, `invite_redemptions`,
`capabilities`, `capability_assertions`, `capability_confirmations`.
**Shared, lands here because S1 needs it:** `users`, `api_tokens`, `audit_log` (write-only until
S3 reads it).

**Store functions** (`crates/db/src/projects.rs`) — the Go signatures are the contract:
`GetProject(ctx, slug)`, `CreateProject(ctx, uid, slug, name, desc, governance, license)`,
`ProjectSlugs(ctx)`, `PutDocument`, `ListDocuments`, `GetDocument`,
`ListBoardColumns`, `GetBoard`/`GetBoardForMilestone`, `placeFeature`, `JoinProject`,
`GetRoleForProject`, `ListMembers`.

**Routes:** everything under `/api/v1/projects` that S1 owns, plus `/api/v1/projects`, `/healthz`,
`/api/v1/capabilities`, `/api/v1/version`.

**The first handler, in full** — this is the shape every later route copies, and it is where the
estate contract is either adopted or lost:

```rust
// crates/app/src/http/mod.rs
use axum::{extract::{Path, State}, http::StatusCode, response::IntoResponse, Json, Router, routing::get};
use polaris_core::{Error, ErrorEnvelope, Page};   // the estate's envelope and cursor

pub fn router(state: AppState) -> Router {
    Router::new()
        .route("/healthz", get(healthz))
        .route("/api/v1/capabilities", get(capabilities))
        .route("/api/v1/version", get(version))
        .route("/api/v1/projects", get(list_projects))
        .route("/api/v1/projects/{slug}", get(get_project))
        .with_state(state)
}

async fn get_project(
    State(state): State<AppState>,
    Path(slug): Path<String>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = state.db.project_by_slug(&slug).await?;   // Ok(None) when absent
    match project {
        Some(p) => Ok(Json(serde_json::to_value(p)?)),
        None => Err(ApiError::NotFound),                    // 404 with the envelope, never the SPA
    }
}

// One error type, one mapping. `IntoResponse` is the only place a status code is chosen,
// so a handler cannot invent `503` for something the contract calls `unavailable`.
pub enum ApiError { NotFound, Unauthenticated, Forbidden, Invalid(String), Conflict(String), RateLimited(u64), Internal(anyhow::Error) }

impl IntoResponse for ApiError {
    fn into_response(self) -> axum::response::Response {
        let (status, code) = match &self {
            ApiError::NotFound => (StatusCode::NOT_FOUND, "not_found"),
            ApiError::Unauthenticated => (StatusCode::UNAUTHORIZED, "unauthenticated"),
            ApiError::Forbidden => (StatusCode::FORBIDDEN, "forbidden"),
            ApiError::Invalid(_) => (StatusCode::BAD_REQUEST, "invalid_request"),
            ApiError::Conflict(_) => (StatusCode::CONFLICT, "conflict"),
            ApiError::RateLimited(_) => (StatusCode::TOO_MANY_REQUESTS, "rate_limited"),
            ApiError::Internal(_) => (StatusCode::INTERNAL_SERVER_ERROR, "internal"),
        };
        let message = match &self {
            ApiError::Invalid(m) | ApiError::Conflict(m) => m.clone(),
            ApiError::Internal(e) => { tracing::error!(error = %e, "internal"); "internal error".into() }
            _ => code.replace('_', " "),
        };
        (status, Json(ErrorEnvelope::new(code, message))).into_response()
    }
}
```

**Corpus subset:** every `projects`, `documents`, `board` shape. **Playwright:** `projects_e2e`,
`board_e2e`, `pages_e2e` run against the candidate port with `BASE_URL` pointed at it (the suites
already take a base URL; check `tests/e2e/harness.py` before assuming, and if they hardcode a
port, that is a one-line change in the harness, not in 28 suites).

**Done when:** corpus subset byte-identical, those three suites green, `cargo mutants -p
concord-db --in-place` reports no survivor on `projects.rs`, and gates 17/19 unchanged (the Go
service still serves traffic; the slice has not cut over yet).

**Cutover:** the slice is cut over only after S5, when the service can answer everything. Until
then each slice is proved on a scratch port — the point of the slices is that a slice can be
declared finished, not that it can be deployed alone.

## S2 — ideas, complaints, ranking

**Tables:** `features`, `complaints`, `complaint_impacts`, `complaint_tags`, `feature_complaints`,
`feature_tags`, `feature_deps`, `feature_evidence`, `milestones` (with `features.milestone_id`),
`field_reports`, `field_report_responses`, `embeddings`, `embedding_backfill`, `arenas`,
`arena_entries`, `pairwise_votes`, `criteria`, `criteria_profiles`, `criteria_profile_weights`,
`criterion_votes`, `criterion_ratings`, `solutions` (arena side), `lists`, `list_entries`,
`list_entry_votes`, `list_tags`, `requests`, `request_answers`, `request_answer_votes`,
`request_tags`, `merge_requests`, `merge_approvals`, `reputation_events`.
**Store functions:** `CreateFeature`, `UpdateFeatureStatus` (the evidence gate + dependency
refusal — two rules this port must not lose: `docs/REMAINING.md` §44/§46), `GetBoard` derivation,
`CastArenaVote`, `FindArena`/`EnsureArena`, the Glicko2 update, `MilestoneFeatures`,
`MilestoneProgress`, `RoadmapSlippage` (nullable, never `0.0`).
**Gates:** the corpus subset; `rank_*`, `milestones_e2e`, `feature_*` suites; mutation arms on
the ranking and the status gate.
**Extraction:** `crates/ranking` is written here and later moved to `polaris-ranking` unchanged.

## S3 — consensus, governance, ledger

**Tables:** `consensus_calls`, `positions`, `objections`, `override_windows`,
`override_positions`, `emergency_holds`, `solutions`, `solution_coverage`, `audit_log`
(read + write), `act_reviews`, `admin_ledger`.
**Adopt:** `polaris-ledger` — and delete the Go restatement's role entirely (estate row 419).
**Rules that must survive:** the hidden tally (§6.6) reaching feeds; the quorum basis; the
`feature.status` ↔ call coupling just repaired in §46 (a call opening moves the feature; an
outcome moves it; sent-back returns it to discussion; one open call per feature is `409`).
**Gates:** `consensus_*`, `audit_e2e` green; gate 49 (`check-ledger`) green for the host.

## S4 — moderation, comments, inbox

**Tables:** `notifications`, `comments`, `comment_votes`, moderation acts inside `audit_log`,
the curation queue's projections (`strategic_*_proposals`, `taxonomy_proposals`, `act_reviews`).
**Adopt:** `polaris-authz` for every permission decision (gate 45 fails on a hand-rolled check
beside the crate), `polaris-comments` for the thread rules, then `polaris-inbox` for
notifications (gate 22: one inbox, one unread number, only its subject can read it).
**Gates:** `notifications_e2e`, the queue suite; gates 22, 45.

## S5 — stats, feeds, admin binaries

**Tables:** `instance_stats`, `forge_events`, `maintenance_signals`, `strategic_themes`,
`strategic_theme_consents`, `strategic_weight_proposals`, `trust_config`, `trust_grants`.
**Retire:** `healthsync`, `proposalsweep`, `statsnapshot` into `polaris-loops` jobs (three
binaries, three timers, one owner); `granttrust`/`capableseed`/`forgeseed`/`embedbackfill` fold
into `crates/cli` as subcommands, so `concord` stays one binary plus `parity`.
**Gates:** `stats_e2e`, feed parse; gates 4, 5, 47; `estate.toml` capability row corrected
(`feeds = true` today is already true of the product, not of the manifest).

## P6 — the frontend

Per ADR 0005 and ADR 0011 §7(Q4): SvelteKit, `@polaris/ui` tokens, and a server-rendered
document — `crates/web` renders `<head>` (title, canonical, `og:image`), the first-paint JSON and
the app mount point; Svelte hydrates. Pages are ported in the order the slices land, and each
page ships with its slice rather than at the end. `docs/REMAINING.md` §46's rule applies to the
frontend too: a status the page shows and the API does not return is a lie.

## P7 — cutover

1. Both services running: Go on 8006 (live), Rust on 8016 (candidate), same PG.
2. Shadow: the recorder replays the full corpus against 8016 nightly for a week; any diff is a
   page, not a note.
3. Flip: `systemctl --user stop concord && systemctl --user start concord-rs`, nginx 8063 →
   8016, `scripts/repoint-tunnel.py` — reversible in one command, per W11 §6.
4. Retire: `estate.toml` `[site.stack]` loses its `reason`; `runtime = "rust"`; capabilities row
   corrected; gate 17 green for a true reason.
5. Archive, never delete: Go binary as `.bak`, `concord.db` to
   `/mnt/disk-important/archive/concord-sqlite-2026.db`, kept a month.
