# Plan — port Concord onto the estate profile

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

### Open *(6 — S2 first)*

| # | step | gate — the command that fails if the step is absent |
|---|---|---|
| 8 | S2 ideas, complaints, ranking | **Done 2026-11-13** (`0a32d49`) — corpus subset, `concord-ranking` suite and store tests green; every killable ranking mutation arm killed, the rest proved unobservable (see the dated entry below) |
| 9 | S3 consensus, governance, ledger | **Done 2026-11-13** (`a2ace5d`) — corpus subset green, `probe_s3_api.py` 94/94, estate gate 49 green; `consensus_e2e`/`audit_e2e` moved to row 12 by the dated correction |
| 9b | S3 arena and solutions | **Done 2026-11-13** (`345b132`) — `probe_s3b_arena.py` 20/20 (was 5/20), the negative control at 19/20, corpus still 2 disagreed of 14, ranking oracle green with 10 arena rows |
| 10 | S4 moderation, comments, inbox | corpus subset + `notifications_e2e` and the queue suite green; estate gates 22 and 45 green |
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
