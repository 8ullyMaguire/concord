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

### Open *(2 — rows 12 and 13; row 11 closed 2026-11-14)*

Rows 7, 8, 9, 9b and 10 are Done and stay in this table with their commits and gates, because a row is done when its
gate ran and its output is quoted — not when it was removed from a list. The header count says 2: 12 and 13.


| # | step | gate — the command that fails if the step is absent |
|---|---|---|
| 8 | S2 ideas, complaints, ranking | **Done 2026-11-13** (`0a32d49`) — corpus subset, `concord-ranking` suite and store tests green; every killable ranking mutation arm killed, the rest proved unobservable (see the dated entry below) |
| 9 | S3 consensus, governance, ledger | **Done 2026-11-13** (`a2ace5d`) — corpus subset green, `probe_s3_api.py` 94/94, estate gate 49 green; `consensus_e2e`/`audit_e2e` moved to row 12 by the dated correction |
| 9b | S3 arena and solutions | **Done 2026-11-13** (`345b132`) — `probe_s3b_arena.py` 20/20 (was 5/20), the negative control at 19/20, corpus still 2 disagreed of 14, ranking oracle green with 10 arena rows |
| 10 | S4 moderation, comments, inbox | **Done 2026-11-13** (`b969b26`): four probes 26/26, 27/27, 40/40, 39/39; `e2e.sh` 29 passed; gates 22 and 45 green (`CHECK inbox` exit 0, `CHECK authz` 10/10); `polaris-comments` and `polaris-authz` adopted |
| 11 | S5 stats, feeds, admin binaries | **Done 2026-11-14** (`3ac7f5e`) — corpus `0 shape(s) disagreed of 16 checked, 5 skipped`; `probe_s5_stats` 16/16; `probe_admin_binaries` **69/69** with four controls; three `concord-rs-*` timers declared in `scheduled.toml` (`active = false` until cutover); `CHECK feed-routes: 8 arm(s) passed, 0 failed`. **Dated correction 2026-11-14:** "feed parse" moves to row 12 — the feeds are page routes returning XML, not JSON routes, and `probe_s5_feeds` is that row's gate. Gates 4 and 5 are red for `media.polarisocial.xyz` and not for concord (recorded in the row-11 body below). |
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

### Row 11 — part 34: the request board, its answers and its tags (2026-11-13, `4611bde`)

```
gate (before): GET /projects/{id}/requests, GET /requests, GET /requests/{id}, /answers, /tags -> 404 (five routes)
gate (after):
  probe_s5_requests.py -> 23/23 ok
  routes.py            -> go 216 / port 109   missing: 109 (99 json, 10 page)
  cargo test -p concord-db --lib -> 38 passed
  concord-parity       -> 0 shape(s) disagreed of 16 checked, 5 skipped
```

Five read routes in one part because they share one guard: `require_readable_request` reaches the project **through the
request row**, since `/api/v1/requests/{id}` has no project segment — the shape that let this family stay unguarded when
the project-scoped guard was applied everywhere else.

Three rules, each a bug in the service being ported:

* `requests.project_id` is **nullable** and NULL means **instance-wide**. A plain `i64` that scans the column fails on
  exactly the rows that make the feature work ("converting NULL to int64"); it is read as `Option<i64>`, normalised to
  `0`, and the JSON **omits** the key so a client can tell *instance-wide* from *project 0*;
* an instance-wide request is **public by construction** — it belongs to no project, so no project's visibility can hide
  it. The unported helper called `GetProjectByID(0)` unconditionally and answered 404, making every instance-wide request
  unreadable through every route that resolves one;
* an answer's ranking is a **Glicko rating** (`elo_r DESC, id`), not a vote score: the table has `proposed_by` and
  `elo_r/elo_rd/elo_vol`, and neither `author_id` nor `score`.

**NEGATIVE CONTROL** (the instance-wide branch removed — `GetProjectByID(0)` again):

```
FAIL an instance-wide request is readable with NO token: got 404 want 200
FAIL and says what it is: got None want 'Instance-wide request'
FAIL but an instance-wide request stays readable: got 404 want 200
20/23 ok
```

**Found by failing:** `request_answers` carries `UNIQUE (request_id, project_id)` — an answer is **a project's** answer
to a request, one per project, which is precisely what makes the cross-project comparison the rating exists for. The
fixture had to create a second project to hold the second answer. Cleanup had to delete through **both** FK routes (by
request and by project) and the tags before the projects, because a rival project answering another project's request
holds references on both sides.

### Row 11 — part 35: §17's accepted-answer badge (2026-11-13, `238ffe7`)

```
gate (before): PUT /requests/{id}/accept, DELETE /requests/{id}/accept -> 404 (two routes)
gate (after):
  probe_s5_accept.py    -> 15/15 ok
  probe_s5_requests.py  -> 23/23 ok (no regression)
  routes.py             -> go 216 / port 111   missing: 107 (97 json, 10 page)
  concord-parity        -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-db --lib -> 38 passed
```

`accept` and `unaccept` return a `Refusal` enum (`NotAuthor` / `ForeignAnswer` / `NotActive`) rather than an `sqlx::Error`,
because the three refusals are three statuses that mean three different things — 403 *not yours to decide*, 404 *no such
answer on this request*, 400 *that answer cannot be accepted*. Flattening them is how a permission failure ends up
reported as a missing row.

**The order is the rule.** The author check runs **before** the answer is looked up, so a stranger naming an answer id
that does not exist gets **403, not 404**. The other order tells people who may not accept anything which answer ids
exist.

**The badge does not override the ranking.** `accept` writes `accepted_answer_id` and `updated_at` and nothing else, so an
accepted answer that lost its comparisons still sits where its rating puts it — proved by accepting the 1400 answer while
the 1700 answer leads: order and every `elo_r` unchanged. **Unaccepting is not restricted by the answer's status**: an
accepted answer that was later removed still leaves a badge pointing at it, and the author must be able to clear it
(proved with `status='removed'`). No token is **401** (`store.ErrAuth`), not 403 — the missing actor and the wrong actor
are different answers.

**NEGATIVE CONTROL** (the author check moved after the answer lookup):

```
FAIL a stranger naming a nonexistent answer still gets 403, not 404: got 404 want 403
14/15 ok
```

Exactly one assertion moves, which is the point: with a *valid* answer id both orders still end at 403, so the control
proves precisely the leak the order exists to prevent.

**Found by failing:** `request_answers` is `UNIQUE (request_id, project_id)`, so the live answer and the removed answer
cannot share a project's slot — the fixture needs a third project. Second time this constraint shaped a fixture; it is
now recorded on part 34 too.

### Row 11 — part 36: the duplicate-detection embedder (2026-11-13, `230cd06`)

**Why this before more routes.** The request/complaint/feature filing paths go through `checkDuplicates`, and the **live**
service has an embedder configured (`CONCORD_EMBED_URL=http://127.0.0.1:11434`, `nomic-embed-text`), so the duplicate
gate is not inert in production and cannot be declared away. The vector layer comes first, or a filed create answers a
different question than the service it replaces.

New crate **`crates/embed`** = the ported `internal/embed`: signed hashed bag of words (unigrams 1.0, character trigrams
0.6, word bigrams 0.4) over SHA-256 with the sign taken from the top bit, 2048 dimensions, L2 normalisation, cosine in
`f64` over `f32` buckets, little-endian blob encode/decode, and the three calibrated thresholds (strong 0.85, duplicate
0.62, weak 0.40) plus `Rank`'s score-desc / entity-id tiebreak.

```
gate (before): crates/embed did not exist; internal/embed had no port; 107 json routes missing (unchanged by this part)
gate (after):
  cargo test -p concord-embed -> 5 passed   (859 non-zero buckets compared bit-for-bit, 1024 ordered pairs, 32 encode
                                             round trips, 2 threshold bands, 1 ranked list)
  cargo test -p concord-db --lib -> 38 passed
  routes.py -> go 216 / port 111   missing: 107 (97 json, 10 page)
```

The fixture is **generated, not written**: `scripts/parity/golden_embed.go` runs the reference over a 32-text corpus and
prints *every* non-zero bucket (index + shortest round-trip `float32`), every pairwise cosine, the encode/decode round
trip, both threshold decisions and one ranked list. It lived in the archived tree for exactly one command and was deleted
from it; it is kept in `scripts/parity` so the fixture can be regenerated.

Three things only a golden oracle would have caught:

* a short string's trigram is the **whole padded string** (`"##x##"` as one feature), not a three-byte slice of it — I
  wrote the slice first;
* trigrams slice **bytes**, not characters, so non-ASCII text hashes invalid-UTF-8 sequences and must do so identically;
* stop words are **not** dropped by the tokenizer (the Go doc comment claims they are) — they are skipped at the unigram
  and bigram sites and deliberately **kept** for the trigram signal. I had copied `"does"` into the stop list; it is not
  in the reference's list.

And one only the oracle could settle: my hand-written `Rank` expectation said ties come out `[3,5,7]`. The rule says id
order, so the 0.5 tie is 7 and 1 — `[3,5,1]`, cutting 7 entirely. The oracle was right.

**NEGATIVE CONTROL** (the sign dropped, `let sign = 1.0`):

```
FAIL the_embedder_reproduces_every_bucket...: text 1 ("crash") bucket 132: 0.3198011 != -0.3198011 (bits differ)
FAIL every_ordered_pair_keeps_its_cosine
3 passed, 2 failed
```

The two threshold tests still passed, and that is the instructive part: flipping every sign negates every vector, and
cosine is invariant under that, so the band memberships did not move. **Only the bucket-level fixture can see it** —
which is why the fixture compares buckets, not scores.

**Restore note:** writing the file back on the network mount left cargo running the mutated artefact; `touch` is part of
the restore. Third time. It is now in the skill, not just in this ledger.

### Row 11 — part 37: the remote embedder and the per-model bands (2026-11-13, `cf4cbb7`)

```
gate (before): crates/embed had the hashed embedder only; no Ollama client, no per-model thresholds, no Classify
gate (after):
  cargo test -p concord-embed -> 15 passed   (5 golden + 10 ollama)
  cargo test -p concord-db --lib -> 38 passed
  LIVE on thinkcentre, against the real server, at the deployment's own timeout:
    timeout 8000 ms
    id      nomic:nomic-embed-text:latest
    dims    768 (configured width 768)
    provider responded with 768 values, first three [0.01169132, 0.028616866, -0.15483397]
    near    0.9715  the roadmap page shows an empty column for every milestone
    far     0.5165  users cannot reset their password
    bands   near Strong, far Weak
    ok      the model answered and ranks a paraphrase above an unrelated complaint
```

`crates/embed/src/ollama.rs` — `Ollama` + `OllamaConfig` (endpoint, model, timeout, **prefix**, fallback), `POST
{endpoint}/api/embed` with `{model, input: prefix+text}`, plural `embeddings[0]` first and the singular old-server form
accepted, non-200 **and** in-body `error` both degrading to the hashed fallback, and a request mutex held across the round
trip because two concurrent filings racing the server would both fail — and a failure there reads as "no duplicates".

`crates/embed/src/bands.rs` — `thresholds_for()` keyed by model id (nomic 0.92 / 0.60 / 0.35, hashed 0.85 / 0.62 /
0.40), `classify`, `is_strong_duplicate_for`, `is_duplicate_for`. The two scales are **not comparable** — identical titles
score 1.00 under both, a paraphrase 0.37 under one and 0.61 under the other — so a single global constant mis-calibrates
whichever embedder it was not measured on. Proven at 0.90: strong under hashed, advisory under nomic.

The `Embedder` trait is **async and object-safe by hand** (`BoxFuture`) rather than sync or via `async-trait`: the remote
call is on the filing path and the store holds the embedder behind a `dyn`.

**NEGATIVE CONTROL** (`thresholds_for` always returns the hashed bands):

```
FAIL the_bands_are_per_model_and_each_is_a_decision: left (0.85, 0.62, 0.4) right (0.92, 0.6, 0.35)
9 passed, 1 failed
```

**Found by running it live rather than by reading:** the first live run **failed** — 2048-wide vector, i.e. the fallback —
with no diagnostic, because a cold model load exceeded the 10 s default. The fallback did its job (a degraded lexical check
instead of a false "no duplicates") but nothing said so. The client now prints the reason under `CONCORD_EMBED_DEBUG`, and
the example honours `CONCORD_EMBED_TIMEOUT_MS` so the measurement uses the deployment's 8 s rather than a friendlier
default; the second run answered 768-wide. **Cutover note:** the first filing after an idle period may be checked
lexically, and that is the deployment's timeout, not the port's.

### Row 11 — part 38: filing with the duplicate gate, and the `/similar` panel (2026-11-13, `01052ce`)

```
gate (before): POST /projects/{id}/requests, POST /requests, GET /similar/{kind} -> 404 (three routes)
               no embedding store
gate (after):
  probe_s5_filing.py   -> 31/31 ok
  probe_s5_requests.py -> 23/23 ok   (no regression)
  probe_s5_accept.py   -> 15/15 ok   (no regression)
  routes.py            -> go 216 / port 114   missing: 104 (94 json, 10 page)
  concord-parity       -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-db --lib -> 38 passed
```

`crates/db/src/embeddings.rs` — `upsert_field`, `upsert_entity_text`, `delete_for`, `find_similar`, `hydrate`, `coverage`.
Four rules, each one a defect the service already paid for: **two fields per entity and the max over them** (title-to-title
1.00 against 0.58 for the concatenation, which filed a literal duplicate with no warning); **`has_content` and not a
length check** (a zero vector occupies a slot forever, scores 0 against everything, and makes "is it indexed?" checks that
count rows lie); **the visibility clause inside the query**, not at the caller (without it `/similar` is an oracle: submit
a sentence, learn whether a matching row exists in a private project, read back its title); and **an unindexable query
returns nothing**, not everything.

`AppState` grew an embedder (`Arc<dyn Embedder>`) built by `embedder_from_env()` — `CONCORD_EMBED_URL` / `_MODEL` /
`_TIMEOUT_MS` / `_PREFIX`, with an absent or misconfigured remote falling back to the local embedder rather than to "no
duplicate detection".

The gate (`check_duplicates`): title first then title+body; **instance-wide** scope (§6.1's *"this was fixed in X"*); a
failure to *check* must not block the filing; the band is per model. The 409 carries `similar[]`, `top_score`,
`threshold`, `resubmit_with` and a note; `confirm_duplicate: true` proceeds and leaves the existing row untouched.
`POST /requests` (instance-wide) has **no gate** — the reference never checked there, and matching it is the job until
cutover. Recorded rather than silently improved.

**NEGATIVE CONTROLS.**

```
visibility clause removed (1 = 1 OR ...):
  FAIL once it is private, the anonymous panel no longer sees it: got True want False
  30/31 ok

gate removed (never refuse):
  FAIL filing the SAME text again is refused with 409: got 201 want 409
  ... and the five field assertions that follow it
  25/31 ok
```

**Two failures found by the probe, not by reading.**

* `operator does not exist: bigint = text` — the visibility predicate reused `$2` for the actor while `$2` held the model
  id. It did **not** surface as a 500 on the filing path, because the gate's own rule is *"a failure to check must not
  block the filing"*: the duplicate check silently proceeded. Only the 409 assertion caught it. **A silent-proceed rule
  needs an assertion on the refusal, or it hides every bug in the checker.**
* the 409 body was built as an `ApiError`, whose envelope has room for a message and nothing else — so `similar[]`,
  `top_score`, `threshold` and `resubmit_with` were dropped and the refusal said only that something was wrong. It is now
  a full response.

**And one of my expectations corrected by the embedder's own documented limit:** a paraphrase is **filed** under the local
hashed embedder (measured before: 0 of 6 paraphrases flagged) and only surfaces as a lead. Asserting a 409 there would
have been asserting that the local embedder is a transformer.

### Row 11 — what remains, and in what order (plan, 2026-11-13)

Generated from `scripts/parity/routes.py --verbose`, not from memory: **94 JSON routes missing, 10 page routes** (the page
routes are row 12's, not row 11's). Grouped into eleven batches so each has one gate a reader can run.

| batch | routes | what it is |
|---|---|---|
| B1 requests | 3 | `PUT /requests/{}/tags`, `POST /requests/{}/answers`, `POST /answers/{}/vote` |
| B2 features | 18 | claim, deps, evidence, tags(+remove), vote, milestone, strategic-weight, work-item, consensus, solutions, solution vote, coverage + contest |
| B3 complaints | 6 | file, impact, merge, reject, validate, `POST /complaints/{}/merge/{}` |
| B4 project admin | 14 | charter, criteria (+vote, +active), criteria-profiles, intake-mode, visibility, metrics, languages, lists (+entries, +vote), tags (+completeness) |
| B5 milestones | 6 | create, patch, close, features, order, status |
| B6 merge + invites | 9 | `projects/{}/merge` read/create, approve, execute, reject; invites read/create/revoke; join |
| B7 documents + threads | 4 | `PUT`/`DELETE /projects/{}/documents`, `GET`/`POST /projects/{}/threads/{}/{}` |
| B8 alternatives | 4 | read, create, add entry, vote |
| B9 finder | 7 | questions, session create/read/results, answer, back, lift |
| B10 autonomy + filed + arena | 9 | votes/next, work-items, field-reports (+responses), taxonomy read/create/consent, `arenas/{}/recompute` |
| B11 auth + global | 14 | login, logout, redeem-invite, guest (read/create), `GET /lists`, `GET /scout`, `GET /contribute/next`, `GET /stats/rising`, `GET /badge/{}`, `GET /admin-ledger`, `POST /consent`, `POST /emergency-holds/{}/release`, `POST /hooks/forgejo` |

Each batch follows the same shape this row has used since part 12: paste the route's 404 from `routes.py` **before**,
implement the store function and the handler, write one probe asserting the *rules* (not the status codes) plus a negative
control that breaks one rule and shows exactly which assertions move, then quote the probe's line and the inventory count
in the commit.

**Order is by risk, not by size.** B1 first because two of its three routes have already been half-designed here (the
answer table's `unique (request_id, project_id)` and the author rule the accept badge already reads); B2 last of the big
ones because its 18 routes share three stores that do not exist yet (claim, evidence, work-item) and one that does
(solutions). B11 sits at the end because two of its members are not port work at all: `POST /hooks/forgejo` needs the
deployment's webhook secret, and `GET /admin-ledger` reads an admin view whose authorization rule lives in the Go template,
not the router.

**Rows 12 and 13, restated so they are not a surprise at the end.**

* Row 12 is the SvelteKit frontend plus **eleven** browser suites: the four it inherited (`feature_e2e`,
  `feature_ui_mutants`, `milestones_e2e`, `rank_links_e2e`), the two moved here from row 9 (`consensus_e2e`, `audit_e2e`)
  and the three from row 10 (`curation_e2e`, `comments_e2e`, `notifications_e2e`), plus the ten page routes above. The
  first obstacle is mechanical and shared by all of them: each seeds through `sqlite3.connect` instead of
  `harness.connect`, so against PostgreSQL they open an empty file and die on the first table.
* Row 13 is the cutover: Rust binary serving `concord.polarisocial.xyz`, `systemd` unit replaced, sequence repair re-run,
  corpus re-recorded after the deploy, estate gate 17 green with no `reason`.

**One correction to this session's own record:** the B1 store code written in the turn before this plan was **lost** — the
tool call that wrote it was interrupted and the tree was left clean at `610ac9f` (verified with `git status --short` and
`grep -c set_tags`). Nothing half-applied, nothing to revert; B1 starts from the query I read out of the reference
(`SetRequestTags` replaces and reuses instance-wide tag rows; `AnswerRequest` writes `(request_id, project_id, body,
'active', proposed_by)`).

### Row 11 — part 39: B1, request tags, answers and the answer vote (2026-11-13, `8024005`)

```
gate (before): PUT /requests/{}/tags, POST /requests/{}/answers, POST /answers/{}/vote -> 404
gate (after):
  probe_s5_requestwrites.py -> 43/43 ok
  probe_s5_filing.py 31/31, probe_s5_requests.py 23/23, probe_s5_accept.py 15/15   (no regression)
  routes.py  -> go 216 / port 117   missing: 101 (91 json, 10 page)
  concord-parity -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-db --lib -> 38 passed
```

`crates/db/src/requests.rs` gained `set_tags`, `answer`, `one_answer`, `vote_answer` (+ `VoteRefusal`); `crates/db/src/voteweight.rs`
is §8.3's formula in one place with the two omissions the reference makes and documents — **no expertise term** (an answer
has no tags; borrowing the solution's would apply a multiplier computed from an unrelated question) and **reputation as a
plain `sum(points)`**, not decayed by `rep_halflife_days` (the charter carries the half-life, `GetReputation` never applies
it).

Rules asserted, each one a defect already paid for: tags are **author-only and not a role check** (the reference's first
version refused the author for having an ordinary role); setting tags **replaces** and drops whitespace-only names; the tag
vocabulary is **instance-wide**; an answer belongs to a **project** while the caller goes in `proposed_by` (the reference
once put the caller's user id in the project column — a cross-project write); **one answer per project per request**;
an **instance-wide** request has no project to inherit; a vote names **two answers and an outcome**; a foreign answer is
**403**, an unknown one 404; **you cannot vote on your own proposal** (§5.1); **skip records and moves nothing**; and
`weight` in the body is **ignored** (§8.3).

**NEGATIVE CONTROLS.**

```
weight taken from the body:
  FAIL the 999 in the body is IGNORED: got False want True
  FAIL the stored weight is the computed one, not 999            41/43
self-vote check removed:
  FAIL you cannot vote on an answer you proposed: got 200 want 403
  FAIL every accepted vote left a row...  got ['a','skip','b']   41/43   (the refused vote also left a row)
author check on tags removed:
  FAIL a stranger may not set them: got 200 want 403             42/43
```

**Two decisions taken by running it, both recorded as deliberate divergences:**

* `unique (voter_id, answer_a, answer_b)` — a voter compares a given **ordered pair** once. The reference lets that
  constraint error out as a **500**, so a second click on the same pair looks like a server fault. Ported as **409**, the
  refusal itself unchanged. Same treatment as a project answering twice (`unique (request_id, project_id)`).
* the **instance-wide answer**: the reference passes the request's project (`0`) straight into a NOT NULL column and answers
  500 for a question the instance board exists for. The port requires the body to name the answering project and refuses
  with **400** otherwise — guessing a project from the caller's memberships would invent an answer's provenance.

Also: an answer's `strategic_weight` is **0** in the Glicko call. §6.2's weight is a feature's maintainer-set importance
and an answer has no such thing; borrowing one would move ratings by an unrelated project decision.

### Row 11 — part 40: B3, the complaint writes (2026-11-13, `ac86d9c`)

```
gate (before): the five write routes under /projects/{}/complaints -> 404
gate (after):
  probe_s5_complaintwrites.py -> 40/40 ok
  probe_s5_requestwrites.py 43/43, filing 31/31, requests 23/23, accept 15/15   (no regression)
  routes.py  -> go 216 / port 122   missing: 96 (86 json, 10 page)
  concord-parity -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-db --lib -> 38 passed
```

`crates/db/src/complaints.rs` gained `create`, `add_impact`, `validate`, `reject`, `merge` (+ `Refused`);
`crates/db/src/membership.rs` holds `join` and `add_reputation` together, because *"taking part makes you a participant"*
is one rule and a write path that remembers one and forgets the other produces a project whose contributors cannot
validate anything.

Nine rules asserted: filing is **authenticated** (it once wrote `author_id 0`); path and body must **agree** and the path
wins; `strategic_multiplier` defaults to **1.0**, not 0; the §6.1 gate is instance-wide and `confirm_duplicate` proceeds;
indexing may fail without losing the complaint (`embed_failed`); filing pays **+5** and grants `contributor` **or-ignore**;
an impact is recorded **once per person** and `severity=0` falls back to 1; validate/reject are **contributor acts on open
complaints**; rejecting **tells the author who decided** in the same transaction and never notifies somebody of their own
action; merge is **maintainer** work, cannot cross projects, cannot target itself, and a source from another project is
**404** through this path (not 403 — a 403 confirms the id is real).

**NEGATIVE CONTROLS.**

```
merge's same-project check removed (if false):
  FAIL a merge across projects is 400: got 200 want 400
  FAIL the audit row names the real actor, once: got [(478,'merged into 2468'), (478,'merged into 2467')]   38/40
a second impact overwrites instead of being ignored:
  FAIL a second impact from the same person does not overwrite the first: got 5 want 3                       39/40
validate without the contributor gate:
  FAIL a non-member may not validate: got 200 want 403
  FAIL a contributor-or-above may validate: got 400 want 200   (the stranger spent the open state)
  FAIL and the answer says so: got None want 'validated'                                                   37/40
```

**Three things found by running it, not by reading:**

* **`complaints.public_id` is NOT NULL with no default** — the insert answered 500 until the writer supplied a ULID. Every
  other API-facing table (`users`, `projects`, `features`, `consensus_calls`, `milestones`, `project_documents`) is the
  same; this is the first insert path into a *new* table since the baseline, so it is the first place the rule bit.
  Recorded here so the next new table costs nobody a 500.
* **`POST /projects/{slug}/complaints` was already registered for the GET** by S2, and adding `.post()` panicked at boot:
  *"Overlapping method route. Handler for `GET /api/v1/projects/{slug}/complaints` already exists"*. A 404 was expected
  and a **boot panic** arrived instead; the probe found it by failing to connect and the candidate's log named it. Route
  registration must be read before a shape is extended, not after it panics.
* **`require_role` must be evaluated after the complaint resolves.** With the gate first, a stranger's 403 is replaced by
  a 404 (the id is not looked up yet) and the ordering the probe asserts — 403 *before* any state change — is lost.

**Deliberate divergences, recorded:** an anonymous impact is **401** here (the reference passes `getActorID` = 0 into a
foreign key and answers 500 — the row cannot exist without an attributable person, so 401 states what the schema already
holds); and a non-numeric id on the reject/validate path is **404**, kept as the reference has it for *this* path even
though its generic complaint path answers 400.

### Row 11 — part 41: B5, the milestone writes (2026-11-13, `8c1f46f`)

```
gate (before): the six milestone write routes -> 404
gate (after):
  probe_s5_milestonewrites.py -> 41/41 ok
  complaintwrites 40/40, requestwrites 43/43, filing 31/31, requests 23/23, accept 15/15   (no regression)
  routes.py -> go 216 / port 128   missing: 90 (80 json, 10 page)
  concord-parity -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-db --lib -> 38 passed
```

`crates/db/src/milestones.rs` gained `create`, `update`, `set_status`, `close`, `assign_features`, `reorder` (+ `Refused`).

**B5 was taken before B4**, which the plan listed next: B5 is self-contained, and its create path is the first insert into a
new table since the complaint writes, where the `public_id` rule had just been learned — cheaper to pay here than in the
middle of fourteen admin routes. A reordering, not a change of plan.

Rules asserted: every write is **maintainer** work; `target_date` is optional and an undated milestone has made no
promise; a title is required and unique **within the project**; `PUT /status` is a **no-op with no act** when nothing
changes; **closing is the only path** into `done`/`cancelled` — `done` with open features and no successor is refused
*with the count*, a successor **moves** them with **one act per feature**, a cancellation **moves none**; a batch assign
**appends after the tail**; a feature from another project is refused rather than pulled in; reordering refuses a feature
that is not in the milestone instead of skipping it, and answers **204 with no body**.

**NEGATIVE CONTROLS.**

```
close without the open-features refusal:
  FAIL closing as done with 3 open features and no successor is 400: got 200 want 400
  ... and the five after it (the milestone is already done, so nothing can succeed it)        35/41
status no-op removed (always update + audit):
  FAIL and writes NO second act: got 2 want 1                                                40/41
assign ignores feature ownership:
  FAIL a feature from ANOTHER project is 404: got 200 want 404
  FAIL positions ... got [1, 2, 3, 3] want [1, 2, 3]   (a duplicate position, from the foreign row)
  ... and five more                                                                          34/41
```

**Two things found by running it, not by reading:**

* the probe's cross-project-title assertion was passing **for the wrong reason** — the maintainer was not a member of the
  second project, so the 403 came from the role gate rather than the uniqueness rule. Fixed by making them a maintainer
  of both, so the assertion tests what its label says. *A green assertion whose reason differs from its label is a lie that
  costs a later reviewer an hour.*
* axum refuses two routes that differ only in a parameter's **name**, so the five writes reuse `{project_id}`/`{milestone}`
  from the read that already owns that path, and `POST /projects/{slug}/milestones` was attached to the existing GET
  instead of registered twice — part 40's boot panic, avoided by reading the registration first.

**Recorded, not silently fixed:** `PUT /status` accepts `done` and `cancelled` as words, so a client can mark a milestone
done with open features under it — the exact state `/close` exists to prevent. The reference does this too and a page may
use the route, so it is carried as-is for the cutover checklist rather than tightened here.

### Row 11 — part 42: B4a, the project settings writes (2026-11-13, `aaa2ec6`)

```
gate (before): PUT /projects/{}/tags, GET /projects/{}/tags/completeness, PUT /projects/{}/languages,
               PUT /projects/{}/intake-mode -> 404
gate (after):
  probe_s5_projsettings.py -> 28/28 ok
  milestonewrites 41/41, complaintwrites 40/40, requestwrites 43/43, filing 31/31, requests 23/23, accept 15/15
  routes.py -> go 216 / port 132   missing: 86 (76 json, 10 page)
  concord-parity -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-db --lib -> 38 passed
```

**B4 is split, not half-done:** B4a is the four settings writes; **B4b** is the remaining ten (charter, criteria ×3,
criteria-profiles, lists ×3, metrics, visibility). The batch count stays honest that way.

`taxonomy.rs`: `apply_project_tag` (+ `Applied`), `completeness` (+ `Completeness`, carrying **both thresholds** so the
page can explain the number). `projects.rs`: `set_languages` (+ `Invalid`), `set_intake_mode`, `INTAKE_MODES`.

Rules asserted: an unknown tag name is **refused with the route that can propose one**; aliases count as existing names;
applying twice leaves one link; tagging and languages are **contributor** work, the intake mode **maintainer**;
percentages are bounded **both per-share and in total** (the discovery score treats them as shares, so a set summing to
140 overstates the project wherever it is ranked); a refused language write leaves the stored set alone; a **no-op intake
save writes no act**; and `mode`/`intake_mode` are both accepted because echoing the response back is the obvious thing.

**NEGATIVE CONTROLS.**

```
unknown tag silently ignored:
  FAIL an unknown tag is 400: got 200 want 400
  FAIL and the refusal names the proposing route: got False want True
  FAIL each accepted call audits apply_tags: got 3 want 2                            25/28
language sum unchecked:
  FAIL a sum above 100% is 400: got 200 want 400                                     26/28
intake no-op removed:
  FAIL and a no-op save writes NO second act: got 2 want 1                           27/28
```

**Found by running it:** a **namespaced tag is stored as the bare name plus a `namespace` column** — `topic:kanban` is
name `kanban` with namespace `topic`, which is what `SplitTag` splits and what the apply looks up. Seeding the whole
`topic:kanban` string as the name made every namespaced apply miss, so the meter read 3 tags / 0 namespaced where the
probe expected 5 / 2. The Go writer does the same (`INSERT INTO tags (name, namespace) VALUES (NULL, bare, ns)`), so the
port was right and the fixture was wrong — written down because the next person to seed taxonomy will hit it. Also
`tags.suggested` is **BOOLEAN** here (an integer flag in SQLite) and a bare `0` is an integer literal, so the fixture
needs `false`.

**No reindex after applying tags**, deliberately: the Go store writes an fts5 table, while here `search_tsv` is a
GENERATED column and the tag half of free-text search is an `EXISTS` over `project_tags` — the index follows the row
without a second write that could fall out of step (same reasoning already recorded for `create_project`).

### Row 11 — part 43: B4b, criterion writes and the charter editor (2026-11-13, `678b77e`)

```
gate (before): POST /projects/{}/criteria, PUT /projects/{}/criteria/{id}/active,
               POST /projects/{}/criteria/{id}/vote, POST /projects/{}/criteria-profiles,
               PUT /projects/{}/charter -> 404
gate (after):
  probe_s5_criteriawrites.py -> 45/45 ok
  projsettings 28/28, milestonewrites 41/41, complaintwrites 40/40, requestwrites 43/43, filing 31/31,
  requests 23/23, accept 15/15
  routes.py -> go 216 / port 137   missing: 81 (71 json, 10 page)
  concord-parity -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-db --lib -> 38 passed
```

`crates/db/src/criteria_writes.rs` (new): `create`, `set_active`, `get`, `rating`, `cast_vote`, `save_profile`
(+ `Refused`, `Weight`). The reads stay in `criteria.rs`; mixing them made that module's doc-comment a list of two
unrelated jobs. `charter.rs`: `set_charter` (+ `Refused`).

Rules asserted: a criterion is scoped to its project **twice** (the row lookup and a vote's ownership re-check, so ids
cannot pool votes across projects); **one vote per (criterion, voter, pair)**; `skip`/`both`/`neither` are recorded,
audited and move nothing; an absent direction is `higher_is_better` and a non-positive weight `1.0` **silently** while an
explicit bad direction is refused; a profile is **replaced wholesale** on re-save and keeps its id; the charter's four
bounds are refusals guarding switches that would otherwise silently disable a mechanism for the whole project.

**NEGATIVE CONTROLS.**

```
cross-project feature check removed:
  FAIL a feature from another project is refused: got 200 want 400
  FAIL and nothing is recorded for it: got 1 want 0
  FAIL and both ratings come back: ... got 2 want 1                            42/45
theme_lifetime_days bound removed:
  FAIL theme lifetime 0 is refused: got 200 want 400
  FAIL theme lifetime past a century is refused: got 200 want 400
  FAIL a refused edit changed nothing: got (0.9, 40000.0) want (0.9, 30)      42/45
profile weights merged instead of replaced:
  FAIL re-saving REPLACES the weights wholesale: got 1 want 0                  44/45
```

**One decision taken here rather than copied — 2026-11-13.** A **partial** charter body is filled from the *current*
charter instead of being zeroed. The reference decodes the body straight into its `Charter`, so an omitted
`solution_call_confidence` arrives as `0`, fails its own bound, and answers "must be between 0 and 1 exclusive" to a
request that never mentioned the field. The settings panel posts the whole form, so no real client can tell; the
alternative was to reproduce a 400 that misdescribes the request. Asserted in the probe (`a PARTIAL body is filled from
the current charter, not zeroed`).

**Found by running it:** the probe's own teardown compared **criterion** ids against `project_id`, so the delete matched
nothing and the projects stayed behind a foreign key — the tear-down failed, not the product. Recorded because a probe
that cannot clean up poisons every later run that shares the database.

### Row 11 — part 44: B4c, list writes, entry votes, metrics, visibility (2026-11-13, `faf01d9`)

```
gate (before): POST /projects/{}/lists, POST /projects/{}/lists/{}/entries,
               POST /projects/{}/lists/{}/entries/vote, PUT /projects/{}/metrics,
               PUT /projects/{}/visibility -> 404
gate (after):
  probe_s5_listwrites.py -> 41/41 ok
  criteriawrites 45/45, projsettings 28/28, milestonewrites 41/41, complaintwrites 40/40,
  requestwrites 43/43, filing 31/31, requests 23/23, accept 15/15
  routes.py -> go 216 / port 142   missing: 76 (66 json, 10 page)
  concord-parity -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-db --lib -> 38 passed
```

**B4 is now whole:** B4a (4 settings writes) + B4b (5 criteria and charter) + B4c (these 5) = the 14 routes the batch
listed. `list_writes.rs` (new): `create`, `add_entry`, `vote_entry` (+ `Refused`). `projects.rs`: `set_visibility`
(+ `VISIBILITIES`).

Rules asserted: **instance-wide must be asked for** (the path's project is also the access check); the audit row for an
instance-wide list is written against the **path's** project because the list has none; an entry is **proposed** with its
proposer recorded; an entry vote uses the same §8.3 weight as every other vote and refuses a removed entry, an entry from
another list, and **a proposer voting on their own entry**; an instance-wide list cannot be voted through a project URL
(the weight needs a project); a missing `median_review_hours` is **NULL, not 0**; a non-member changing visibility gets
**404, not 403**, because visibility is the one setting where the project's existence is the secret.

**NEGATIVE CONTROLS.**

```
self-vote refusal removed:
  FAIL a proposer may not vote on their own entry: got 200 want 403
  FAIL a vote is audited: got 2 want 1                                              39/41
removed-entry refusal removed:
  FAIL a removed entry is not a candidate: got 403 want 400                          40/41
an unmeasured review time written as 0:
  FAIL an unmeasured review time is NULL, not 0: got 0.0 want None                   40/41
```

**Reuse rather than a second formula, twice:** the vote weight is `voteweight::answer_vote_weight` and the health score
is `metrics::sync` — the same functions the answer votes and the forge importer already use. A copy of either here would
be a second answer to "how much does this vote count" and "how healthy is this project", and the search ordering reads the
first one.

**Found by running it:** the probe expected a **maintainer** to be a neutral voter, but entry B was proposed by that
maintainer and the refusal is **per-proposer, not per-role** — so "someone else may" was 403. Fixed by adding a fourth
member who proposed neither entry, which is the only honest way to test a successful comparison. The teardown also needed
both patterns: the list's slug does not prefix-match the project's, so cleaning by project and by slug separately left
list rows behind a foreign key.

### Row 11 — part 45: B2a, feature tags, feature milestone, strategic weight (2026-11-13, `7885345`)

```
gate (before): POST /projects/{}/features/{}/tags, POST .../tags/remove,
               PUT .../features/{}/milestone, PUT .../features/{}/strategic-weight -> 404
gate (after):
  probe_s5_featurewrites.py -> 37/37 ok
  listwrites 41/41, criteriawrites 45/45, projsettings 28/28, milestonewrites 41/41, complaintwrites 40/40,
  requestwrites 43/43, filing 31/31, requests 23/23, accept 15/15
  routes.py -> go 216 / port 146   missing: 72 (62 json, 10 page)
  concord-parity -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-db --lib -> 38 passed
```

`taxonomy.rs`: `add_feature_tag`, `remove_feature_tag` (+ `Applied::UnknownFeature`). `milestones.rs`:
`set_feature_milestone`. `strategy.rs`: `Refused::Invalid`, so a bad weight is a **400** instead of a 409.

`/features/{}/claim`, `/work-item`, `/deps`, `/evidence`, `/solutions*`, `/consensus` and `/vote` are still open — this is
B2's first slice, not B2.

Rules asserted: an unknown tag is refused with the proposing route; the feature id is scope-checked twice; removing an
unattached tag is a **no-op**; a weight change needs a **rationale** and is a **proposal** (a second pending one is 409, a
proposer's own consent is 403); the milestone response counts **open blockers**, read from `feature_deps`' **blocker**
side and only for blockers that are not `shipped`.

**NEGATIVE CONTROLS.**

```
blocker count read from the wrong side:
  FAIL and the response counts OPEN blockers only: got 0 want 1                          36/37
an unknown tag silently skipped:
  FAIL an unknown tag is 400: got 200 want 400                                           36/37
the weight range unchecked:
  FAIL a weight outside the range is 400: got 201 want 400
  ... and seven downstream (the 9.0 proposal is pending, so the real one is a 409)       29/37
```

**Four findings, all in instruments or wiring rather than in a formula:**

* the strategy store was **already wired**, but only at the port's own path (`POST /strategy/weight-proposals` with
  `feature_id` in the body). The reference — and the pages built against it — use `PUT /features/{id}/strategic-weight`.
  Both are now served from one store. *"Wired somewhere" is not "wired where the caller looks"* — the reason the route
  inventory is a gate at all;
* the strategy routes take a **numeric** project id; a slug there is a 400. Recorded rather than changed under the port's
  feet, and the probe says so at the call site;
* a bad weight came back as **409**, because the store folded "your number is wrong" and "the database failed" into one
  variant and the HTTP mapping called it a conflict. `Refused::Invalid` now maps to 400 — a caller who typed `9.0` must
  not be told someone else got there first;
* the parity gate disagreed by **one shape and it was my own fixture litter**: 24 half-torn probe projects from crashed
  runs were still in the database and `/api/v1/curation` counts live rows. `scripts/parity/clear_probe_litter.py` now
  removes them by computing the deletion order from the FK graph (*hops from `projects`, descending*). A hand-written
  order was wrong twice — `milestones` before `features`, then `request_answers`, which reaches `projects` only through
  `requests`. A gate that reads live rows needs a working broom.

### Row 11 — part 46: B2b, work items, claims, dependencies, evidence (2026-11-13, `78706fa`)

```
gate (before): PUT .../features/{}/work-item, POST and DELETE .../claim, POST and DELETE .../deps,
               POST and DELETE .../evidence -> 404
gate (after):
  probe_s5_workitems.py -> 44/44 ok
  featurewrites 37/37, listwrites 41/41, criteriawrites 45/45, projsettings 28/28, milestonewrites 41/41,
  complaintwrites 40/40, requestwrites 43/43, filing 31/31, requests 23/23, accept 15/15
  routes.py -> go 216 / port 153   missing: 65 (55 json, 10 page)
  concord-parity -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-db --lib -> 38 passed
```

`crates/db/src/feature_work.rs` (new): `work_item`, `claim`, `release_claim`, `set_work_item_label`,
`add_dependency`, `remove_dependency`, `open_blockers`, `add_evidence`, `remove_evidence` (+ `Refused`).

Rules asserted: **a claim never moves the feature**; only `ready`/`in_progress` is claimable; **one live claim**, a release
recorded as a state (`released_at`) so "picked up and dropped twice" stays answerable; a cycle is refused **with the path**
that would close it and only `shipped` clears a blocker; evidence is scoped **twice** — the feature and the cited row.

**NEGATIVE CONTROLS.**

```
a claim also moves the feature:
  FAIL the claim does NOT move the feature: got 'in_progress' want 'ready'                  43/44
the cycle walk follows the edge backwards:
  FAIL a cycle is refused: got 200 want 400
  FAIL and the refusal names the path that would close it: got False want True
  FAIL recording the same edge twice is not an error: got 400 want 200                      41/44
evidence skips the cited row's project:
  FAIL a document from another project is 404: got 201 want 404
  FAIL two citations are attached: got 3 want 2
  ... and two more                                                                          40/44
```

**Three things found by running it:**

* the **GET half of `/deps` already existed**, and registering a second GET panicked the process at boot — *"Overlapping
  method route. Handler for GET … already exists"*. The route inventory never listed it as missing, which is the check that
  would have caught me before the panic did. POST and DELETE now hang off the existing GET, whose panel already carries
  `open_blockers`;
* `coalesce(f.difficulty, '')` is reported by PostgreSQL as the column **`coalesce`**, so `row.get("difficulty")` panicked
  a tokio worker with `ColumnNotFound` and the caller saw a dropped connection instead of a status code. Every expression
  in that `SELECT` is now aliased;
* `field_reports` is **not** a document with a title: it is a migration report (`version`, `use_case`, `environment`,
  `scale`, `duration`, `outcome`, `caveats`, `workaround`, `advice`), and only `outcome` has no default. The column list
  is the record of what it actually is.

### Row 11 — part 47: B2c, §6.5's feature-scoped call (2026-11-13, `7a59f5e`)

```
gate (before): GET and POST /projects/{}/features/{}/consensus -> 404
gate (after):
  probe_s5_solutioncall.py -> 24/24 ok
  workitems 44/44, featurewrites 37/37, listwrites 41/41, criteriawrites 45/45, projsettings 28/28,
  milestonewrites 41/41, complaintwrites 40/40, requestwrites 43/43, filing 31/31, requests 23/23, accept 15/15
  routes.py -> go 216 / port 155   missing: 63 (53 json, 10 page)
  concord-parity -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-db --lib -> 38 passed
```

`consensus_writes`: `open_solution_call_on_merit`, `open_solution_call_early`, `unmet_condition`. The reference path now
aliases the handler pair the port already had, and **the readiness gate moved out of the handler into the store, beside the
write** — the handler was re-deriving the conditions the readiness page already computes, which is two answers to "may this
be called?" and the second one goes stale the first time a threshold is tuned.

The body accepts **both** shapes: the reference sends `{early, reason}`, the port has accepted `{solution_id,
early_reason}` since before the alias. `early: true` with a blank reason is a 400, never a silent merit open.

**Decision (dated, my call):** the merit path is **not** role-gated. The reference's `handleOpenSolutionCall` resolves the
actor and lets the conditions decide, and only the *early* path asks whether the actor is a collaborator. A non-member
therefore gets 409 not-callable, not 403 — asserting 403 would have been asserting a rule the reference does not have.

**Two boot panics, both mine.** Registering a second GET on a path that already had one (`Overlapping method route`) — the
same class of mistake as part 46, repeated an hour later — then applying the same scripted insert **twice in one session**
and reproducing the panic identically. The insert is now idempotent and the check is `git diff` before `cargo build`.

**Negative controls.**

```
merit ignores the unmet conditions:
  FAIL so opening on merit is still refused: got 400 want 409                                   23/24
  (400, because the next gate to fire is the linked-complaint rule)
an early call needs no reason:
  FAIL an early call with no reason is 400: got 201 want 400
  FAIL a collaborator may open one early: got 400 want 201  ... and three more                   19/24
```

**Two rules the fixture had to learn:** a call must be *about* something — the store refuses a feature with no linked
complaint — and `complaints.public_id` has no default, so an inserted complaint needs one.

### Row 11 — part 48: B2c, filing a solution (2026-11-13, `2194e16`)

```
gate (before): POST /projects/{}/features/{}/solutions -> 404
gate (after):
  probe_s5_solutionwrites.py -> 24/24 ok
  solutioncall 24/24, workitems 44/44, featurewrites 37/37, listwrites 41/41, criteriawrites 45/45,
  projsettings 28/28, milestonewrites 41/41, complaintwrites 40/40, requestwrites 43/43, filing 31/31,
  requests 23/23, accept 15/15
  routes.py -> go 216 / port 156   missing: 62 (52 json, 10 page)
  concord-parity -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-db --lib -> 38 passed
```

`crates/db/src/solution_writes.rs` (new): `file_solution` + `NewSolution` + `Refused`, with `ensure_solution_arena`,
`inherit_parent_rating`, `set_baseline`.

**Filing one solution is three writes that have to agree** — the solution, the arena that ranks it, and its slot in that
arena — and the difference between all three and some of them is invisible from every read the product has, so most of the
assertions are about the other two writes.

Rules: the feature's author cannot author its solutions (§5.2); `integrate-external` needs a catalog link; a
**complementary** solution is refused (stackable, so the rival arena would ask the engine to compare "do A, then B"
against "C"); the first `do-nothing` becomes the permanent baseline and a second **demotes** the first (its votes are
history) rather than deleting it; a fork inherits the parent's rating with **doubled, unclamped** RD.

**Negative controls.**

```
the feature's author may author its solutions:
  FAIL the feature's author may not author its solutions: got 201 want 403
  FAIL no refusal left an arena behind: got 1 want 0                                     22/24
the previous baseline is not demoted:
  FAIL a second do-nothing DEMOTES the first rather than adding one: got [107] want [None]  23/24
```

**Four things found by running it:**

* this schema's timestamps are **`timestamptz`, not the Go's float epoch** — binding an `f64` gave *"column created_at is
  of type timestamp with time zone but expression is of type double precision"*. The port's convention is `now()` on write
  and `extract(epoch …)` on read;
* **`arena_entries` has a composite primary key and no surrogate `id`**, so `parent_entry_id` has nothing to point at. The
  fork relation lives on `solutions.parent_solution_id`, which is a real foreign key — and the probe was asserting a column
  that cannot be populated until the schema corrected it;
* a fork of a solution outside the feature hit the FK as a **500**; the parent is now checked before the insert, so it is a
  400 for a mistake the caller can fix in one field;
* I removed a placeholder from the inherit `UPDATE` with a scripted patch and **left its `.bind(parent_id)` behind**, so the
  fork updated the *parent's* row and then its own. The ratings looked plausible at a glance; only the RD gave it away.
  Read the bind list after every patched SQL statement.

### Row 11 — part 49: B2c, the solution arena's pairwise vote (2026-11-13, `f66b0ab`)

```
gate (before): POST /projects/{}/features/{}/solutions/vote -> 404
gate (after):
  probe_s5_solutionvote.py -> 21/21 ok
  solutionwrites 24/24, solutioncall 24/24, workitems 44/44, featurewrites 37/37, listwrites 41/41,
  criteriawrites 45/45, projsettings 28/28, milestonewrites 41/41, complaintwrites 40/40, requestwrites 43/43,
  filing 31/31, requests 23/23, accept 15/15
  routes.py -> go 216 / port 157   missing: 61 (51 json, 10 page)
  concord-parity -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-db --lib -> 38 passed; -p concord-ranking -> 28 passed + 3 golden
```

`crates/db/src/arena_writes.rs` (new): `cast_vote`, `entry`, `baseline`, `refuse_own_vote`, `apply_game`.
`voteweight`: `solution_vote_weight` + `tag_reputation` (§8.3's per-tag reputation over four sources — validated complaints,
merged merge requests, solutions that reached consensus, and non-skip votes on tagged solutions).
`ranking`: `expertise_multiplier`, `parse_expertise_tags`, `best_tag_reputation`, `outcome_score`.

Rules: both sides must be solutions **of this feature**, checked before the arena is touched; an author cannot rank their own
entry; a **skip** is recorded and moves nothing; **neither with a baseline is two losses to the baseline**, not a draw; the
weight comes from the charter, never from the body. Both ratings are computed from their **pre-vote** values and written
together — updating A then scoring B against the new A gives the winner a smaller gain than the loser's loss.

**Negative controls.**

```
a skip is scored:                    FAIL and moves nothing at all: got ((1576.68, 260.48, 2), …)   20/21
neither is a draw:                   FAIL both competitors LOSE to the baseline: got (True, False)   18/21
an author may rank their own entry:  FAIL an author cannot rank their own entry: got 200 want 403   18/21
```

**Two things found by running it:**

* the first run measured a weight of **8.79e-07** and *"the winner rose"* **passed** — on a rounding-level change. §12.3's
  age gate ramps over seven days, so a brand-new account's vote weighs almost nothing: correct behaviour, and a measurement
  that could not have failed. The probe now backdates the voter a month and asserts the weight lands in `0.5..4.0` (it is
  1.25) **and** that each rating moves by more than a rating point;
* removing two statements made **rustc 1.99 ICE** on `crates/cli`; the retry with the parameters explicitly consumed built
  cleanly. Recorded because the failed build left the **previous binary** in place, so control C printed control B's
  failures — a stale binary under a failed build is exactly how a control reports somebody else's result.

### Row 11 — part 50: B2c, coverage claims and their contests (2026-11-13, `5040712`)

```
gate (before): POST …/solutions/{}/coverage -> 404 ; POST …/coverage/{}/contest -> 404
gate (after):
  probe_s5_coverage.py -> 22/22 ok
  solutionvote 21/21, solutionwrites 24/24, solutioncall 24/24, workitems 44/44, featurewrites 37/37,
  listwrites 41/41, criteriawrites 45/45, projsettings 28/28, milestonewrites 41/41, complaintwrites 40/40,
  requestwrites 43/43, filing 31/31, requests 23/23, accept 15/15
  routes.py -> go 216 / port 159   missing: 59 (49 json, 10 page)
  concord-parity -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-db --lib -> 38 passed
```

`crates/db/src/coverage_writes.rs` (new): `claim_coverage`, `contest_coverage`, `coverage_score` + `CoverageScore`.

The score is served **with its parts** (`coverage`, `resolved_pain`, `total_pain`, `claimed_pain`, `contested`) because
"resolves 340 of 510 pain" is what a reader can act on. Pain comes from `priorities::complaint_pain` — the project's one
definition — so coverage ranks against the same measure as every other pain-based number.

Rules: the complaint must be linked to the **solution's own feature** (else the solution picks its own denominator); a contest
needs a reason and the claimant cannot be the contestor; a contested claim **stops counting until restated**, and restating
clears the contest.

**Negative controls.**

```
a contested claim keeps counting:   FAIL a contested claim stops counting: got 0.8333… want 0.0     21/22
restating does not clear:           FAIL the claim counts again: got False want True
                                    FAIL and only the other contest remains: got 2 want 1           20/22
```

**Two things found by running it:**

* `solution_coverage.contested` is a **bigint count (0/1), not a boolean** — `contested = false` was a 500 that answered
  nothing. Same class as `parent_entry_id` in part 48: **a column is what the schema says it is**, not what the reference's
  variable name suggests;
* three assertions failed first because **pain decays** against the charter's half-life, so an exact float equality between
  two HTTP calls asserts that no time passed. They compare at 1e-6 relative now — the claim is that the denominator did not
  move by a claim's worth, not that the clock stopped.

### Row 11 — part 51: B2c, the feature vote (2026-11-13, `28a1670`)

```
gate (before): POST /projects/{}/features/{}/vote -> 404
gate (after):
  probe_s5_featurevote.py -> 20/20 ok
  coverage 22/22, solutionvote 21/21, solutionwrites 24/24, solutioncall 24/24, workitems 44/44,
  featurewrites 37/37, listwrites 41/41, criteriawrites 45/45, projsettings 28/28, milestonewrites 41/41,
  complaintwrites 40/40, requestwrites 43/43, filing 31/31, requests 23/23, accept 15/15
  routes.py -> go 216 / port 160   missing: 58 (48 json, 10 page)
  concord-parity -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-db --lib -> 38 passed
```

`crates/db/src/votes_writes.rs` (new): `record_feature_vote`, `ensure_priority_arena`. `voteweight`: `feature_vote_weight`.

Three behaviours that differ from the solution vote **on purpose**: the weight **ignores the member's role**
(`VoteWeight(reputation, 1.0, cap) × age gate`, as the reference computes it); **one live vote per voter per pair** — the
reference's `INSERT OR REPLACE` relied on a uniqueness constraint this schema does not have (primary key only), so without the
replacement a voter could repeat one comparison to walk a feature up the board; and the vote row carries **both shapes**
(legacy `feature_a`/`feature_b`/`project_id` for the priority query, `arena_id`/`entity_type`/`a`/`b` for the arena queries
and the recompute) — a half-populated row is invisible to one reader and the recompute would then rebuild every rating at the
prior.

**Decision (dated, my call):** the join happens **after** the vote succeeds, not before as the reference's code does. Its own
comment says *"enrol only after the vote has been recorded"*; its code joins first, so a vote refused for ranking your own
feature still added you to the project. Following the documented intent over the code, and the weight is unaffected — a
non-member has no reputation there and this arena does not use the role.

**Negative controls.**

```
a repeat vote inserts a second row:  FAIL but there is still ONE row for it: got 2 want 1               18/20
the self-vote check removed:         FAIL an author cannot rank their own feature: got 200 want 403
                                     … and six more (enrolment, the reputation point doubling, two rows,
                                     the last-said outcome, the maintainer weight)                         13/20
```

The second control is worth keeping: **one removed rule moved seven observations**, which is what a probe looks like when it
is measuring a decision rather than a route.

Also: `pairwise_votes.feature_a` and `.project_id` are foreign keys, so the probe's cleanup had to delete votes before
features — it failed at cleanup *after* every assertion passed, which is the good order for a fixture bug to appear in.

### Row 11 — part 52: B6a, invites (2026-11-13, `c84e273`)

```
gate (before): POST and GET /projects/{}/invites, POST /projects/{}/invites/{}/revoke,
               POST /auth/redeem-invite -> all 404
gate (after):
  probe_s5_invites.py -> 32/32 ok
  featurevote 20/20, coverage 22/22, solutionvote 21/21, solutionwrites 24/24, solutioncall 24/24,
  workitems 44/44, featurewrites 37/37, listwrites 41/41, criteriawrites 45/45, projsettings 28/28,
  milestonewrites 41/41, complaintwrites 40/40, requestwrites 43/43, filing 31/31, requests 23/23, accept 15/15
  routes.py -> go 216 / port 164   missing: 54 (44 json, 10 page)
  concord-parity -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-db --lib -> 38 passed
```

`crates/db/src/invite_writes.rs` (new): `create`, `get`, `list`, `revoke`, `redeem`.

Five properties: only a member mints or revokes, and a non-member gets **404, not 403** (a 403 confirms the project exists);
redemption is **idempotent per (invite, user)** and the use counter advances only on a FIRST redemption; every failure is the
**same 404**, or the endpoint is an oracle for testing tokens; revoking sets `revoked_at` and **keeps the row**; revoking twice
is a 400. An invite grants `contributor` and nothing more — a link lets someone in, it does not confer standing to admit others.

**Negative controls.**

```
the use counter advances on every redemption:  FAIL without exhausting it by themselves: got 2 want 1
                                               FAIL so somebody else can still use it: got 404 want 200   30/32
the expiry is not checked:                     FAIL an expired invite is the same 404: got 200 want 404    31/32
```

**Two things found by running it:** my first fixture minted `max_uses=1` and then asserted a *second* person could redeem it —
the 404 was the cap working and the assertion was the bug; and the probe now asserts the token's **shape** (43 characters of
base64url, no `+` or `/`), not just its presence, because a 32-character standard-base64 token works everywhere except the one
place these get pasted.

### Row 11 — part 53: B6b, merge requests (2026-11-14, `6e8f8dd`)

```
gate (before): GET/POST /projects/{}/merge -> 404, PUT /merge/{}/approve|execute|reject -> 404
gate (after):
  probe_s5_merge.py -> 37/37 ok
  invites 32/32, featurevote 20/20, coverage 22/22, solutionvote 21/21, solutionwrites 24/24, solutioncall 24/24,
  workitems 44/44, featurewrites 37/37, listwrites 41/41, criteriawrites 45/45, projsettings 28/28,
  milestonewrites 41/41, complaintwrites 40/40, requestwrites 43/43, filing 31/31, requests 23/23, accept 15/15
  routes.py -> go 216 / port 169   missing: 49 (39 json, 10 page)
  concord-parity -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-db --lib -> 38 passed
  cargo test -p concord-governance -> 9 passed (+ 3 golden)
```

`crates/db/src/merge_writes.rs` (new): `get`, `list`, `create`, `approve`, `gate`, `gate_for`, `execute`, `reject`. The role
ladder got a `Role::parse` in `concord-governance` rather than a second rank table in the app.

`execute` closes the request, marks the feature `shipped` and closes every complaint linked to it — one transaction. Three
guards beside it: the request belongs to the caller's project (404, never 403); it is still `open`; §5.7's gate passes via
`governance::check_merge_gate`. An approval counts only from a non-author who is *still eligible*, decided when counting.

**Three negative controls**: no `open` guard → `got (409, False) want (409, True)` (36/37); gate not consulted → 6 observations
move (31/37); approvals counted as rows → `got (1, 1) want (1, 0)` (36/37).

**Decision, dated.** My first control A passed 37/37 and I nearly recorded it as "the guard is redundant". The cause is that
merging makes the request's author eligible (a merged merge request is itself a qualifying contribution), so the quorum rises
from 1-of-1 to 1-of-2 and a second `execute` is refused by the *gate* — same status, a message that tells the reader to collect
a confirmation that does not exist. **A status-only assertion cannot tell "you cannot" from "you may, later"**, so the probe now
asserts the reason string. The accidental rising quorum stays as it is: it is a defence, not a rule, and the ledger says so.

### Row 11 — part 54: B7, documents and threads (2026-11-14, `30c7fde`)

```
gate (before): PUT /projects/{}/documents -> 405, DELETE /projects/{}/documents/{} -> 405
               routes.py reported GET/POST /projects/{}/threads/{}/{} as MISSING — implemented, never called
gate (after):
  probe_s5_documents.py -> 46/46 ok
  merge 37/37, invites 32/32, featurevote 20/20, coverage 22/22, solutionvote 21/21, solutionwrites 24/24,
  solutioncall 24/24, workitems 44/44, featurewrites 37/37, listwrites 41/41, criteriawrites 45/45,
  projsettings 28/28, milestonewrites 41/41, complaintwrites 40/40, requestwrites 43/43, filing 31/31,
  requests 23/23, accept 15/15
  routes.py -> go 216 / port 173   missing: 45 (35 json, 10 page)
  concord-parity -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-db --lib -> 38 passed
```

`crates/db/src/document_writes.rs` (new): `put` (upsert at `(kind, slug)` bumping `revision`, `author_id` to the last writer,
`created_at` fixed), `delete` (project predicate in the `WHERE`, not a read-then-write). 8 MB body limit on that PUT only.

**The inventory was lying, dated.** `routes.py` matched `.route(\s*"…"` — path string immediately after the paren. The threads
route carries a comment line between the two, so **three routes the port already answered were counted as missing work**, and
that count is what every turn chooses from. Fixed the regex to skip `//` lines: port routes 169 → **171 with no code change**,
missing 47. Recorded here because the count is the ledger's only input.

**Found by running it:** `public_id` is NOT NULL with no default on `project_documents`, so every write was a 500 (supplied on
insert only, never rewritten on update); and GET/POST disagree about `thread_kind = 'release'` — the read answers `[]`, the
write refuses 400. That asymmetry is the reference's own (`GetThreadComments` does not validate either), so the probe asserts it
and says why.

**Two negative controls**: overwrite instead of bump → `FAIL the second write is revision 2: got 1 want 2` (45/46); delete not
scoped → `FAIL another project's document cannot be deleted under this URL: got 200 want 404` (44/46).

### Row 11 — part 55a: the finder engine (2026-11-14, `6932904`)

```
gate (before): crates/ had app, cli, db, domain, embed, governance, parity, ranking — no engine;
               /api/v1/finder/* is 6 of the 45 missing JSON routes, the largest cluster left
gate (after):  cargo test -p concord-finder -> 11 passed; 0 failed
```

`crates/finder` (`concord-finder`): `gain`, `counts_by_value`, `build_dimension`, `classify`, `next_question`, `all_dimensions`,
`should_stop`, `gaps`, `apply`, `score`, `weight_report`, plus the contribution feed's vocabulary (`contribute_questions`,
`minutes_bucket`, `difficulty_word`, `item_key`/`item_id_from_key`, `work_item_candidate`, `order_work_items`).

Eleven tests, one per rule: constant dimension never asked; unknown is not no (kept, not removed); skip never filters; a full
match scores `1 - penalty × coverage` = 0.975 rather than 0.15 (the renormalised denominator); positive `no` = 0.0 and a
different known value = 0.5, with partial ranking above no; a decide-later preference actually reorders (my first version of
that assertion passed against an engine that ignored preferences); the stop rule needs a margin (six-way tie is not a finding);
gaps report unknown data; work items inherit project dimensions and bucket 20 minutes to "60"; an unstated budget leaves the
attribute absent while `needs_mentor=false` is written as a fact; an unmeasured project never outranks a measured one.

**Decision, dated:** the answer type is `AnswerOption`, not `Option` — the Go's name shadows `std::option::Option` in every
signature below it. The JSON (`id`/`label`/`impact`) is unchanged.

**Not done (part 55b):** the 6 routes, the candidate loader, the session store, and the differential probe that drives the same
answers into the live Go service and this port and compares questions and ranking.

### Row 11 — part 55b: the finder's six routes (2026-11-14, `3165a10`)

```
gate (before): GET /api/v1/finder/questions -> 404, POST /finder/sessions -> 404 (6 routes missing)
gate (after):
  probe_s6_finder_diff.py -> 34/34 ok
  documents 46/46, merge 37/37, invites 32/32, featurevote 20/20, coverage 22/22, solutionvote 21/21, solutionwrites 24/24,
  solutioncall 24/24, workitems 44/44, featurewrites 37/37, listwrites 41/41, criteriawrites 45/45, projsettings 28/28,
  milestonewrites 41/41, complaintwrites 40/40, requestwrites 43/43, filing 31/31, requests 23/23, accept 15/15
  routes.py -> go 216 / port 180   missing: 38 (28 json, 10 page)
  concord-parity -> 0 shape(s) disagreed of 16 checked, 5 skipped
  cargo test -p concord-finder -> 12 passed;  cargo test -p concord-db --lib -> 38 passed
```

`crates/db/src/finder_reads.rs`: `attributes`, `report_rates`, `arena_standings`, `candidates` — batched by id, because the set is
73 projects. `crates/app/src/finder.rs`: six routes, in-memory session store, DTOs.

**The gate is differential** — the same seed and the same answers into the Go service (live, serving production) and into the
port, comparing at each step: question key, option order, surviving count, stop verdict, top five, fits to 1e-9, gaps,
unknown-fit, back, lift. 34/34 agree.

**What the probe found before it agreed (dated):**
1. The services do not share data — the Go runs on its own SQLite (`CONCORD_DB=…/concord.db`, 73 public), the port on Postgres.
2. All 18 of the port's extra catalogue rows were **fixtures my own earlier probes had left behind** and were still publicly
   listed. Removed (with their features, arenas, votes, solutions, documents, members, users); the probe now asserts the two
   catalogues hold the same projects, so a future leak fails instead of drifting.
3. The port's search 500'd on the first empty-query search it ever served: `0::double precision` with no alias, read back as
   column `score`. Every earlier caller passed a query.

**Negative controls**: `apply()` dropping the unknown rule → 9 observations move; **the diversity penalty 0.7 → 1.0 passed
34/34**, because the live catalogue's first three questions never repeat a family — so that rule now has a unit test that builds
the tie by hand, and it fails under the mutation (`left: "cap:x"`). A probe that cannot fail is not a gate.

**Decisions, dated:** sessions stay in memory (mutex + map, one-hour age-out), exactly the reference's shape — a table would buy
durability the Go service does not have, and `persisted: false` already tells the client. And `filtered_out` records the filter,
not the slugs it removed: naming near-miss projects would disclose what the catalogue holds.

### Row 11 — part 56 (S2): alternatives arenas (2026-11-14, `ca79518`)

```
gate (before): GET /projects/{id}/alternatives -> 404   (all 4 routes missing)
gate (after):
  probe_alternatives.py -> 62/62 ok
  probe_s6_finder_diff.py -> 34/34 ok
  documents 46/46, merge 37/37, invites 32/32, featurevote 20/20, coverage 22/22, solutionvote 21/21, solutionwrites 24/24,
  solutioncall 24/24, workitems 44/44, featurewrites 37/37, listwrites 41/41, criteriawrites 45/45, projsettings 28/28,
  milestonewrites 41/41, complaintwrites 40/40, requestwrites 43/43, filing 31/31, requests 23/23, accept 15/15
  routes.py -> go 216 / port 184   missing: 34 (24 json, 10 page)
  cargo test -p concord-db --lib -> 38 passed
```

`crates/db/src/alternatives.rs`: `list`, `ensure`, `add_competitor`, `not_for_use_case`. Four routes in `crates/app/src/lib.rs`.

The machinery already existed and had never been exercised — every arena in the database was feature-priority — so the probe
proves the door, not the engine:

* the **key is the use case** and an empty one is refused (400 naming `use_case`): the other arena shapes key on
  `use_case IS NULL`, and an empty string is how a "create" becomes a silent second arena of a different kind;
* create is an **ensure**: the same use case twice, whitespace included, is one arena;
* an arena ranks **other** projects — its own project cannot compete (400) nor be one side of a vote;
* **no baseline**: "do nothing" is a solution's competitor; asserted from the table, 0 baseline rows;
* **visibility for the reader**: unreadable competitor → 404 on add and on vote, and filtered out of the panel if it later became
  private (filtered AFTER the sort, so the ranking stays the arena's real one);
* **the weight is the server's**: reputation × role with the age gate last — a fresh account's vote asserted from
  `pairwise_votes.weight` to be strictly between 0 and 1;
* **an author cannot judge their own competing project** (403) — for a project, authorship is the maintainer/owner row in
  `members`, the only authorship this schema has.

**Negative controls**: self-competition guard removed → 9 observations move ("got 200 want 400", entry count doubles, rank 1
becomes the arena's own project, ranks `[1,2,3]`); empty use case defaulted instead of refused → 4 move ("got 201 want 400",
three arenas).

**The finding that matters**: this probe leaked three public projects per run — it cleaned on the way in only — and the finder's
differential probe, which asserts both catalogues hold the same 73, went red and named all 28 leftovers by slug. `probe_alternatives.py`
now cleans on the way out too, and `.gitignore` covers the `unused` file a probe's DB handle drops in the repo root. A probe that
writes must not leave a trace; one that cleans only on entry leaves one on every run that fails halfway.

### Row 11 — part 57: the identity cluster — guest, consent, login, logout, fold (2026-11-14, `ceaf47f`)

```
gate (before): POST /api/v1/auth/login -> 404, /auth/logout -> 404, GET /guest -> 404, POST /guest -> 404, /consent -> 404
               (5 routes missing, and no gate on the other 184)
gate (after):
  probe_identity.py -> 65/65 ok
  probe_s6_finder_diff.py -> 35/35 ok
  alternatives 62/62, documents 46/46, merge 37/37, invites 32/32, featurevote 20/20, coverage 22/22, solutionvote 21/21,
  solutionwrites 24/24, solutioncall 24/24, workitems 44/44, featurewrites 37/37, listwrites 41/41, criteriawrites 45/45,
  projsettings 28/28, milestonewrites 41/41, complaintwrites 40/40, requestwrites 43/43, filing 31/31, requests 23/23,
  accept 15/15, s3_api 94/94, s4_comments 41/41, s4_moderation 39/39, s3b_arena 20/20, s4_inbox 26/26, s4_curation 27/27,
  s5_* (35 probes) all ok;   probe_s5_feeds RED — it drives /projects/{}/feed.xml, a PAGE route, i.e. row 12
  routes.py -> go 216 / port 189   missing: 29 (19 json, 10 page)
```

`crates/db/src/auth.rs`: `login`, `issue_token`, `revoke_token`, `verify_password`, and two `AuthError` variants
(`Credentials`, `NoCredential`). `crates/db/src/identity.rs` (new): `create_guest`, `promote_guest`, `guest_status`,
`monthly_active_users`, `fold_columns`, `fold_guest_into`, `record_consent`, `is_guest`. `crates/app/src/lib.rs`: five routes, the
consent middleware, the navigation cookie, register-with-promotion.

**The consent gate is the reference's, probed before it was ported**: `POST /api/v1/projects/suki/join` on the live Go answers
`403 {"code":"consent_required"}` without the cookie and `401` with it. The port now does the same, as one layer instead of 189
handlers. Identity-establishing paths stay exempt — hiding registration behind a cookie banner is a dark pattern. Consequence:
every probe carries the cookie (53 of them, through one shim), which is what a browser does.

**Properties established by `probe_identity.py` (65 checks):**
* a guest cannot log in, and "no such user" / "wrong password" / "guest identity" answer the SAME 401, compared byte for byte;
* an unknown username still costs an argon2 hash, so the answer cannot be read off a stopwatch;
* registering WITH a guest token returns the guest's own id — the row becomes the account;
* logging in from a guest folds its work (2 audit rows + 1 reputation event + the guest's token = 4 moved) and deletes the guest;
  the column list is derived from `pg_constraint` at fold time, and `audit_log.actor_id` is added by name-and-type because it has
  no foreign key on purpose;
* the navigation cookie is accepted for logout and NOT for an API call — the header is the API's only credential;
* logout is idempotent (204 twice); a revoked token is dead;
* consent is recorded against the identity, and a failure to record it is logged rather than turned into a failed acceptance.

**Negative controls**: `audit_log.actor_id` dropped from the derived list → `moved: got 2 want 4`, `audit rows name the account:
got 0 want 2`; the gate short-circuited → `got 401 want 403`, no code, no sentence.

**Decisions, dated:**
* **`/auth/me` does not grow an `is_guest` field.** The reference emits `false` for a guest too (its handler reads four columns
  and leaves the flag at its zero value), so guest-ness is visible where it is true: in `GET /guest`, and in the register that
  promotes the row. Copying the reference's lie would have been the wrong kind of fidelity.
* **The fold moves the guest's API tokens too**, so the browser's guest token keeps working after the login, now naming the
  account. That is the reference's behaviour and the right one: the alternative logs the visitor out of the page they just signed
  into.
* **58 `probe_s5_feeds`-style checks are row 12's**, not row 11's: feeds live at `/projects/{}/feed.xml`, outside `/api/v1` and
  outside the JSON surface. Recorded so a red probe is not mistaken for a missing JSON route.

**Found while probing, and fixed**: `probe_s1_api` and `probe_s2_status` could not delete their own fixtures once a project
carried a board (a real foreign key refuses the bare delete), so each failing run left one behind and the next failed on the
duplicate; both now use a stamped slug and delete the board first. `probe_s5_healthsync`'s cleanup was broken by my own scripted
edit and is fixed.

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

### Part 58 — the contribution feed, and the two reads it stands on (2026-11-14)

**Clustered on purpose.** Four routes, one probe, one commit — `/contribute/next`, `/projects/{}/work-items`, `/projects/{}/join`,
`/projects/{}/votes/next`. They are one piece of plumbing: the feed ranks work ITEMS with `concord-finder`'s own engine,
`/votes/next` is the project's closest-rated unjudged pair, and both enrol or refuse a caller through one membership rule. Batching
by shared plumbing rather than by route is the change the last turn recommended; it cost one context window for four routes
instead of four.

**Before** (`probe_contribute.py` against the deployed port):
```
FAIL anyone may read a public project's work items: got 404 want 200
FAIL it lists every item, claimed or not: got 1 want 4
```

**After:**
```
46/46 ok
```

**What the probe establishes, and why each one is not free:**

1. *A claimed item is never offered.* Offering a newcomer work somebody is already doing is the single most discouraging thing a
   feed can do, and it is invisible unless the fixture has one claimed and one unclaimed item. **This is also where the port
   learned `claimed_by` is not a column**: a claim is an unreleased row in `work_item_claims`, which is what makes the history of
   who held a task survive the release. First build returned `column f.claimed_by does not exist`; the probe saw 500.
2. *Health OMITS the field when unmeasured*, like the reference's `omitempty` — absent is not null and never zero. A client reading
   a missing score as 0 % would render every unmeasured project as the unhealthiest in the catalogue. The probe distinguishes all
   three cases (`0.9`, field absent, and the label), which is why the first version of the check — `.get("health")` returning
   `None` for both absent and null — had to be rewritten.
3. *Constraints are refused, not dropped.* `minutes` and `difficulty` are REQUIRED answers; `mentor=no` is a preference
   (`MODE_DECIDE_LATER`), because declining a mentor does not make a mentored task impossible, only less attractive. An unknown
   value is a 400 naming the parameter — a URL that says "filtered" must not return the unfiltered feed.
4. *`/votes/next` enrols the caller before showing a pair.* Asking someone to compare two features and then refusing their vote
   is an invitation followed by a refusal; it is also why `/join` exists as a route of its own, so a page can enrol without
   asking for a pair. `join` is `on conflict do nothing`, not a read-then-write.
5. *`TooFewFeatures` and `AllCompared` are different 409s* — one means "stop asking", the other "wait for more features". Both
   were 500s in the reference's own known-issues ("Could not load a comparison — retry", a button that fails identically forever).
6. *The pair is the closest-rated unjudged one*, `abs(r_a − r_b)`, which is where one vote carries the most information. The probe
   gives every fixture feature an explicit rating so the intended pair is unique; on the first run three features shared the
   default 1500 and the nearest pair was an accident (`got {6806, 6807} want {6808, 6807}`).
7. *The judged pair disappears*, and it is judged **through the API**, not by inserting a row: a vote is three writes in this
   schema (vote, arena, entries) and a hand-written row leaves the arena untouched — the next pair would then be chosen from a
   state the product cannot reach.

**Decision (dated 2026-11-14): the health score is read, not recomputed.** `project_metrics.health_score` is what every other
surface shows; recomputing it in the feed from the same inputs would be a second implementation of `ranking::health` that could
disagree with the stored number at the same instant. An unmeasured project is therefore `None`, not a fresh computation.

**Decision (dated 2026-11-14): mutants as a startup seam.** `CONCORD_MUTANT=offer-claimed|health-ignored`, read once at boot, set
by nothing in the product. Two controls used to cost two 35-second rebuilds each; now they cost a restart. Both were run:

```
control A (offer-claimed):  44/46 — FAIL the claimed item is never offered
control B (health-ignored): 45/46 — FAIL health breaks a tie
```
Each moves exactly the check it names and nothing else, which is the property a seam has to have to be worth having.

**Gate:** `probe_contribute.py` 46/46; `probe_s6_finder_diff.py` 35/35 (no regression from the shared candidate loader);
`concord-db` lib 38 passed; `routes.py` go 216 / port 193 / missing 25 (15 json, 10 page).
**Commit:** `af6e9ae`. **Files:** `crates/db/src/feed_reads.rs` (new), `crates/app/src/lib.rs` (4 routes + 5 handlers),
`crates/app/src/finder.rs` (`seed_projects` reachable), `scripts/parity/probe_contribute.py` (new, 46 checks).

### Part 59 — §4.7's write half: the report and the reply (2026-11-14)

**Before** (`probe_fieldreports_write.py` against the deployed port — the route existed only as a GET, so every write was a 405):
```
FAIL filing anonymously is 401: got 405 want 401
FAIL a report from a non-member is accepted — §4.7's rate is not a poll of people who already joined: got 405 want 201
```

**After:**
```
33/33 ok
```

**The frame that made the checks fall out.** The read half has been routed since the port began, which means the project page's "No
field reports yet" was not a state the instance happened to be in — it was the state the code *guaranteed*. That is the whole
argument for §4.7's write half: a rate nobody can contribute to is not a measurement of zero, it is a measurement of nothing.

**What the probe establishes:**

1. *`outcome` is required, and one of four.* A free-text outcome would BE the rant §4.7 exists to replace — "works fine mostly"
   parses, reads as evidence and cannot be aggregated. A missing outcome names the field; an unknown one lists the four.
2. *`migrated-away` must name `migrated_to`* — §4.7 asks "migrated away (to what?)", and the successor is the most useful thing in
   that report: it is a second candidate for whoever asked the question.
3. *Author and project come from the token and the URL.* `NewReport` has no field to put them in, which is stronger than
   validating them: the probe sends another user's id and another project's id in the body and reads back the real ones.
4. *A non-member may file AND reply.* §4.7's sentence is "worked for 83 % of reporters"; a rate computed from members only would be
   a poll of people who already joined — exactly the people most likely to have made it work.
5. *`is_owner` is read from the membership table at write time, never from the request.* The probe sends `is_owner: true` as a
   bystander and reads back `false`; a maintainer's reply reads back `true`; the audit detail carries `is_owner=false`.
6. *Auth before project.* Anonymous to a real project and anonymous to a nonexistent one both answer 401 — a different status for
   the second would enumerate the instance's slugs.
7. *Report ids are global behind a project-scoped URL*, so the project is in the WHERE clause: the same report id under another
   project's URL is 404.
8. *A removed report takes no new replies and answers 404, not 403* — the report is not there for the caller, and a distinct
   refusal would confirm it exists to somebody who cannot see it. Removal is a flag and never a delete: §4.7's abusive reports are
   removed by quorum *with appeal*, and the appeal is conducted on the record. The probe asserts the row still exists and the
   earlier replies survive.

**Two schema facts the port learned by failing, not by reading:**
- `field_reports.removed` is `bigint`, not boolean — `coalesce(r.removed,false)` fails to type-check, and reading the column as
  `i64` after coercing it to a boolean fails to decode (`COALESCE types bigint and boolean cannot be matched`, then `SQL type
  BOOL is not compatible with i64`). The query coerces, the struct holds a bool.
- `field_report_responses.is_owner` is also `bigint`. Postgres rejected the boolean bind outright, which is why a bystander's reply
  was a 500 for two builds.

**Decision (dated 2026-11-14): the row is read by column NAME, not as a positional tuple.** This table has seventeen columns and
`sqlx`'s tuple impls stop at sixteen; a name is also the thing a reader can check against the schema. `select _ from _` with
`try_get("x")` is the shape for wide rows in this port from now on.

**Controls** (`CONCORD_MUTANT=owner-from-request|replies-after-removal`, same startup seam as part 58, no rebuild):
```
control owner-from-request:    31/33 — FAIL is_owner is FALSE ... got True; FAIL the audit detail says is_owner=true
control replies-after-removal: 31/33 — FAIL a removed report takes no new replies ... got 201; FAIL replies survive, got 3
```
The first attempt at control A was **a no-op** — it forced `is_owner=false`, which is what a bystander already gets. A control has
to move the value in the direction the defect would, or it proves nothing while looking green. Fixed to force `true`.

**Gate:** `probe_fieldreports_write.py` 33/33; read half `probe_s5_fieldreports.py` 14/14 (no regression); `probe_contribute.py`
46/46; `concord-db` lib 38 passed; `routes.py` go 216 / port 195 / missing 23 (13 json, 10 page).
**Commit:** `3241e3c`. **Files:** `crates/db/src/field_report_writes.rs` (new), `crates/app/src/lib.rs` (2 routes + 2 handlers),
`scripts/parity/probe_fieldreports_write.py` (new, 33 checks).

### Part 60 — three operator reads, and two divergences in routes already called done (2026-11-14)

**Before** (`probe_ops_reads.py` against the deployed port):
```
FAIL the ledger is readable with no token at all — §9.3's whole point: got 404 want 200
FAIL rising is public: got 500 want 200
```

**After:**
```
34/34 ok
```

**Routes:** `GET /api/v1/admin-ledger`, `GET /api/v1/stats/rising`, `GET /api/v1/lists`.

**The rule the three share,** which is why they were batched: *a missing measurement is not a zero, and an empty set is not null.*

1. *The ledger is public, unfiltered, ungated.* §9.3 makes admin actions publicly visible because an admin with no content authority
   is only a real constraint when anyone can watch them. A gate on this route would be the constraint watching itself.
2. *`limit` is clamped, not refused* — `<= 0` or `> 500` becomes 100. A viewer typed at by hand must not 400 on a typo, and a
   caller that asks for a million learns the ceiling from the size of the page.
3. *A rising row carries its span.* A rate over 14 days and one over 90 are different claims, so `days` travels with
   `features_per_week`; a series under the floor is **absent**, because `0.0` reads as "flat" rather than "not measurable".
4. *Health is recomputed, never read from the column.* Project creation inserts an empty `project_metrics` row for every project,
   and the live instance's `health_score` is 0 everywhere — reading it would publish a confident "0" for the whole catalogue
   (§6.3's trap, one layer down).
5. *Instance-wide lists are a scope, not a missing project*: `project_id` is `null`, and a project's list set **includes** them.

**Decision (dated 2026-11-14): "measured" means `last_commit_at is not null or median_review_hours is not null`.** The other two
signals, `contributors` and `releases_90d`, are `NOT NULL default 0` in this schema, so a placeholder row carries 0 there and 0 is
indistinguishable from a real measurement of zero. The reference has the same blind spot — which is exactly why its own comment
says the live instance's scores are all 0. Only the two nullable columns can say "never measured".

**Two divergences found in routes already marked ported**, both by probing the reference rather than this repo's probes:
- **`lists` serialised `title`; the reference emits `name`.** Its Go struct maps the `title` *column* onto a field called `Name`.
  `probe_s5_lists` had asserted `title` — the port's own key — which is how a self-consistent suite hides a shape divergence. Fixed
  in `ProjectList` (`#[serde(rename = "name")]`), and the probe corrected.
- **`GET /projects/{}/lists` took a numeric id**, so the slug form that every other project route accepts answered 400. It now
  resolves slug-then-id, like the reference's `requireProjectID`.

**Controls** (startup seam, no rebuild):
```
control rising-short-series: 32/34 — FAIL a series shorter than the floor is ABSENT ... got True; FAIL no other fixture leaked
control health-from-column:  32/34 — FAIL a real score in [0,1] got False; FAIL a placeholder row publishes NO score got 0.0
```
A third control was written (`admin-ledger-null`) and **could not be exercised**: the instance ledger is never empty during a probe,
and deleting rows that are not the probe's to delete is not a control, it is vandalism. Recorded as unrun rather than reported as
passing.

**Gate:** `probe_ops_reads.py` 34/34; `probe_s5_lists.py` 16/16; `probe_s5_stats.py` 16/16; `probe_contribute.py` 46/46;
`concord-db` lib 38 passed; `routes.py` go 216 / port 198 / missing 20 (10 json, 10 page).
**Commit:** `528974d`. **Files:** `crates/db/src/ops_reads.rs` (new), `crates/db/src/criteria.rs` (`name`), `crates/app/src/lib.rs`
(3 routes + 3 handlers, lists takes a slug), `scripts/parity/probe_ops_reads.py` (new, 34 checks).

### Part 61 — lifting a hold, and two inventory bugs that were inflating the count (2026-11-14)

**Before** (`probe_ops_writes.py` against the deployed port):
```
FAIL releasing a hold anonymously is 401: got 404 want 401
FAIL the project's maintainer lifts it: got 404 want 200
```
Note what the 404-for-everyone did to the *other* checks: "a maintainer of another project cannot lift this hold" passed **vacuously**,
because nobody could. That is the same defect the reference's own comment describes — the route asked for a role against a
`project_id` parameter its path does not have, so it answered 404 for every caller and hid the second defect, that a hold was
releasable by its id alone.

**After:**
```
31/31 ok
```

**Landed:** `POST /api/v1/emergency-holds/{hold_id}/release`. The merge route
`POST /api/v1/projects/{}/complaints/{}/merge/{}` turned out to be registered and answering already — the port's *inventory* said it
was missing, which is the worse kind of error: it invites the work to be done twice.

**What the probe establishes:**
1. *The hold is resolved first, because the URL carries no project.* The role check is against the **hold's** project — the one fact
   the path does not have. A maintainer of another project is refused, and so is the other project's maintainer here.
2. *A written reason is required* — §9.3's ledger carries it, and "who did it" with no "why" is not accountability.
3. *Releasing twice is 400, not 200.* The UPDATE carries `released_at is null` in its WHERE, so the first reason cannot be
   overwritten; the probe asserts the stored reason is unchanged after the second attempt.
4. *Two records per release*: §9.3's admin ledger (`release_emergency_hold`, subject `consensus call N`) and the project's audit
   log. One alone answers half the question.
5. *A merge cannot cross projects and cannot be into itself* — both the store's refusals, asserted with their messages, because a
   status-only assertion cannot tell "you cannot" from "you may later".
6. *A complaint reached through another project's URL is 404*, since the path's project and the row's project are two facts.

**Two bugs in `scripts/parity/routes.py`, both of which had been feeding this ledger's own counts:**
- A chi prefix such as `"/api/v1/projects/{project_id}/complaints"` carries braces of its own, and the depth counter counted them —
  so one `r.Route(` line opened **two** blocks, the stack popped the outer prefix early, and every route in a doubly-nested block was
  recorded without it. Braces are now counted outside string literals.
- A route quoted **inside a comment** — `handlers.go` explains a defect by quoting `// r.Post("/merge/{target_id}", …)` — was counted
  as a live registration under the comment's own prefix. That phantom is where "missing `POST /complaints/{}/merge/{}`" came from: an
  endpoint that has never existed on either side. Comment lines are now skipped.

**Go routes are 215, not 216.** One of the 216 was never a route. The port is at 199, missing 18 (8 JSON, 10 page) — so the JSON
remainder is now: badge, scout, four taxonomy routes, the arena recompute and the Forgejo webhook.

**Controls** (startup seam, no rebuild):
```
control hold-any-role:         28/31 — FAIL the hold is no longer active; FAIL the first reason is not overwritten; FAIL ledger; FAIL audit
control hold-releasable-twice: 28/31 — FAIL releasing twice is 400 got 200; FAIL the first reason is not overwritten; FAIL ledger
```
Both move exactly the observations that depend on the rule they remove. A probe bug found while writing this: one check asserted on
a `body` left over from an earlier call because the response was discarded with `_` — the cross-project message check was reading the
release route's body. Fixed by capturing it.

**Gate:** `probe_ops_writes.py` 31/31; `probe_s5_complaintwrites.py` 40/40; `concord-db` lib 38 passed; `routes.py` go 215 / port 199
/ missing 18 (8 json, 10 page).
**Commit:** `d78ab4b`. **Files:** `crates/db/src/holds.rs` (`release`), `crates/app/src/lib.rs` (1 route + 1 handler),
`scripts/parity/routes.py` (2 parse fixes), `scripts/parity/probe_ops_writes.py` (new, 31 checks).

### Part 62 — the global taxonomy: four routes, one rule (2026-11-14)

**Before** (`probe_taxonomy.py` against the deployed port):
```
FAIL the tag list is public: got 404 want 200
FAIL and it is a list, never null: got False want True
```

**After:**
```
48/48 ok
```

**Routes:** `GET /taxonomy/tags`, `GET /taxonomy/proposals`, `POST /taxonomy/proposals`,
`POST /taxonomy/proposals/{id}/consent`. The consent **store** function already existed (from an earlier part); what was missing was
every route around it, so a proposal could be consented to and never proposed.

**The rule the cluster enforces:** *maintainers hold no unilateral taxonomy power.* Tagging used to auto-create a tag on use, which
gave every project editor instance-wide vocabulary control; the fix was proposals decided by the eligible set. Each assertion below
is one way that stops being true.

1. *`include_suggested` is opt-in.* A suggested tag is a hint nobody has ratified; listing it by default is exactly how the
   vocabulary grew by being mentioned.
2. *The action is a closed set*, and the refusal names it. `create`/`delete` take no operand while the other four require one —
   requiring a value from a create would make every create invalid.
3. *A rename whose value equals the target's **bare** form is a no-op even when the spellings differ.* `topic:thing` and `thing` are
   the same tag, so comparing the raw strings lets it through. The control that removes this check moves exactly two observations.
4. *An unknown namespace is refused with the known list in the message*, rather than minted by use.
5. *A rationale is required*, and *a duplicate pending proposal is 409*: a double submit cannot stack two voices for one change.
6. *Instance-wide proposals need `contributor` somewhere* — one query over the highest role, not a loop over projects, which is the
   same query N times — and project proposals need it in that project. Both refusals are 403: authenticated, and the rule says "not
   you".
7. *Below the threshold is 202, carrying the proposal as it now stands.* The first consent is an ordinary state of a consensus
   process; answering it as a failure makes the first voter think they broke something. A repeated consent adds no voice and answers
   the same 202, because the caller's intent is already satisfied.
8. *The eligible set is the denominator*, and a stranger's consent is refused rather than counted — the probe asserts the consent
   row count is 0 after the refusal.
9. *`[]`, never `null`,* on both list routes.

**Decision (dated 2026-11-14): the eligible set stays the project's members with `contributor` or above.** §4.3 says "users with
reputation in the tag's namespace", and no per-namespace reputation table exists. Counting project members is the tractable
approximation, and it is written down in the module rather than pretended — a smaller denominator is the failure mode, because it
makes a change easier to ratify than the section intends.

**Controls** (startup seam, no rebuild):
```
control taxonomy-noop-allowed:     46/48 — FAIL a rename that only drops the namespace is a no-op got 201
control taxonomy-any-namespace:    46/48 — FAIL an unknown namespace is 400 got 201
control taxonomy-double-consent:   43/48 — FAIL the proposal comes back pending got 'ratified'; FAIL answers 202 got 409;
                                             FAIL pending proposls sort first got 'ratified'
```
The third is instructive: counting CLICKS instead of voters made the second consent meet the threshold, so the proposal ratified and
was APPLIED — the whole point of the rule, visible in five observations.

**Probe bug found while writing this:** the harness translates `?` to a driver placeholder, so a literal `'%taxonomy%'` inside the
SQL was read as a format specifier and demanded an argument that was not there. Cleanup queries now list actions explicitly instead
of pattern-matching them. Also cleaned ten leftover `probe-*` tags from runs whose cleanup died on the `tag_aliases` FK — children
first, always.

**Gate:** `probe_taxonomy.py` 48/48; `probe_s5_taxonomy.py` 18/18; `concord-db` lib 38 passed; `routes.py` go 215 / port 203 /
missing 14 (4 json, 10 page). The JSON remainder is now four routes: badge, scout, arena recompute, Forgejo webhook.
**Commit:** `ae70634`. **Files:** `crates/db/src/taxonomy.rs` (`tags`, `list`, `propose`, `ProposeRefused`, `KNOWN_NAMESPACES`),
`crates/app/src/lib.rs` (4 routes + 4 handlers + `contributor_somewhere`), `scripts/parity/probe_taxonomy.py` (new, 48 checks).

### Part 63 — the two writes nobody clicks (2026-11-14)

**Before** (`probe_arena_webhook.py` against the deployed port):
```
FAIL recomputing anonymously is 401: got 404 want 401
FAIL a contributor's recompute is 200: got 404 want 200
```

**After:**
```
30/30 ok
```

**Routes:** `POST /projects/{slug}/arenas/{id}/recompute`, `POST /hooks/forgejo`. `arena::replay` and `arena::verify` already existed
from the rating work; what was missing was the write that reconciles the stored entries with them, and the endpoint that reaches for a
forge.

**Recompute.** Votes are applied incrementally, and an incremental application of a rating system drifts for reasons with no common
shape: a failed request between the game and the entry update, a manual fix, a bug shipped for an afternoon. The replay is the only
answer to "are these numbers right" as opposed to "these are the numbers the code produced".

* *`changed` is measured before the write.* Afterwards the two agree by construction and the count is always zero — a caller could not
  distinguish "nothing had drifted" from "I fixed twelve".
* *It counts disagreements, not rows.* `verify` returns one row per entry with an `agrees` flag; counting rows reports every entry as
  drift and makes the number meaningless. The control `recompute-count-rows` moves exactly the two checks that read it.
* *A feature arena mirrors into `features.elo_r`/`elo_rd`/`elo_vol`.* Features keep their own columns for the priority endpoint, so a
  recompute that only fixed `arena_entries` leaves two numbers disagreeing — and the disagreement is invisible from the arena side.
  Control `recompute-no-mirror` moves the mirror check and nothing else.
* *The arena's membership in the project is checked before the role.* A role check first answers 403 "not your project" for an arena
  that is not in this project either, which confirms an enumerable id exists. Both differences are 404.

**Webhook.** Four arms (push, pull request, review, comment) plus the two Forgejo/GitHub comment shapes, which differ in where the
number lives.

* *No secret configured is 503*, not "accept everything". An unauthenticated write endpoint that is live because nobody finished setting
  it up is worse than one that says so.
* *Constant-time comparison over the exact bytes received*, and every refusal is the same 401 — no header, wrong scheme and wrong digest
  answer identically, because the difference tells an attacker whether their format is right.
* *The timestamp is the payload's, and the unique key includes it*, so a redelivery writes nothing. A receipt time makes every retry a
  new row, and a forge that retried five times would outweigh one that did not — silently, because nothing about the row looks wrong.
  Control `webhook-receipt-time` moves both the timestamp check and the redelivery check.
* *An unreadable timestamp writes no event row at all*, while the audit row is still written. `now()` would invent a time for an event
  that may have happened a week ago, and the newcomer median is exactly the number such a row moves. Control
  `webhook-timestamp-fallback` moves the one check that guards it.
* *A push's number is stored `0`, not NULL.* In a unique index NULL never equals NULL, so a NULL number would let every duplicate push
  through as a new row.
* *An unknown event type is `ignored`, not an error*: a forge that adds an event must not start failing deliveries.

**Decision (dated 2026-11-14): the event timestamp is parsed from the payload, not read from the forge's API.** The webhook is the only
place the original time exists; a backfill that asked the forge afterwards would get the same answer only for events young enough to
still be in its timeline, so the median would drift by age. The column is `timestamptz` and the payload supplies seconds, so the parse
is RFC 3339 in the request path with no date crate.

**Written out rather than pulled in:** HMAC-SHA256 and the civil-day count, both six lines against `sha2` which the crate already has.
Checked against vectors written by other people — RFC 4231 cases 2 and 6, and a leap day — because a hand-written HMAC or a
hand-written `days_from_civil` is a hand-written bug until something external agrees with it. Four tests in `concord-app`.

**Gate:** `probe_arena_webhook.py` 30/30; controls 29/30, 28/30, 28/30, 29/30; regressions `probe_alternatives` 62/62,
`solutionvote` 21/21, `featurevote` 20/20, `probe_taxonomy` 48/48; `concord-app` lib 4 passed. `routes.py` go 215 / port 205 /
missing 12 (2 json, 10 page) — the JSON remainder is `/badge/{slug}.svg` and `/scout`.
**Commit:** `957f348`. **Files:** `crates/db/src/forge_events.rs` (new), `crates/db/src/arena_writes.rs` (`recompute`),
`crates/db/src/arena.rs` (`entries`), `crates/app/src/lib.rs` (2 routes, 2 handlers, HMAC + RFC 3339 + tests),
`scripts/parity/probe_arena_webhook.py` (new, 30 checks).

### Part 64 — the badge: three numbers in someone else's README (2026-11-14)

**Before** (`probe_badge.py` against the deployed port):
```
FAIL a project badge is 200: got 404 want 200
FAIL served as an image, never as html: got '' want 'image/svg+xml'
```

**After:**
```
21/21 ok
```

**Routes:** `GET /badge/{slug}.svg` and `GET /badge/instance.svg`. Mounted **outside `/api/v1`** because it is fetched by an `<img>`
tag, not by `fetch()` — like the feeds. New store reads: `stats::delivery_latency_for_project`, `stats::shipped_since`. The existing
`stats::delivery_latency` was refactored into `latency_for(pool, Option<project_id>)` rather than copied: two copies of the episode
rule would eventually disagree about the same log.

**Rules, each of which is a claim it must not make:**

1. *An unmeasured health or latency renders `—`.* A zero is a claim that the instance measured nothing **and found it instantaneous**,
   and that claim lands in a README where nobody can check it. Same reason `n=0` is not a median of zero.
2. *`instance` is a reserved slug handled before the project lookup.* A project actually named `instance` would shadow its own badge,
   and the instance badge would then show one project's numbers to every reader but that project's.
3. *An unreadable project and a nonexistent one answer the same 404, and the body never echoes the slug.* A badge is public and
   unauthenticated; a 404 naming the slug confirms an unlisted project exists. The probe asserts the two 404 bodies are byte-identical.
4. *The width is the sum of the segments*, not a constant — truncation in an image is invisible to the person reading it.
5. *`Content-Type: image/svg+xml`*, because an SVG served as `text/html` is a script execution vector in someone else's page.

**The finding: widths in the reference are Go's `len()`, which is BYTES.** An em dash is three UTF-8 bytes, so the reference allots it
21px while drawing it in 7 — every badge with an unmeasured value is 14px wider than the text needs. The port reproduced
`chars().count()` first and produced a 273px badge against the reference's 301px; the byte-for-byte check caught it, which is the whole
reason that check exists. Matching the defect is deliberate: this SVG is embedded in other people's pages, and a badge one glyph
narrower than its neighbours is a diff in someone else's README. **Decision (dated 2026-11-14): match the reference's byte-counted
geometry exactly, and say so in the code.**

**A live header disagrees with the reference's source:** `stats.go` sets `max-age=3600`, the live service answers `max-age=14400`. The
difference is Cloudflare's edge (`Cache-Control` is rewritten when a response is edge-cached), not the service. The port sets the
source's 3600; the probe asserts a numeric `max-age` rather than the digit, because the number a client sees will be rewritten by
whatever proxy is in front of the cut-over host too.

**Controls** (startup seam, no rebuild):
```
control badge-zero-for-unmeasured: 19/21 — FAIL unmeasured renders a dash got '0.00'; FAIL byte-identical
control badge-fixed-width:         18/21 — FAIL width is the sum got 270 want 301; FAIL instance geometry; FAIL byte-identical
control badge-chars-not-bytes:     18/21 — FAIL width got 273; FAIL instance geometry; FAIL byte-identical
control badge-slug-in-404:         19/21 — FAIL body does not echo the slug; FAIL the two 404 bodies are byte-identical
```

**Probe bugs found while writing it:** response headers arrive lowercased (`content-type`), so the probe compares case-insensitively;
and the byte-for-byte comparison initially ran against the *fixture* project, which does not exist on the reference — whose answer was a
404 body. It now compares `/badge/suki.svg`, a project both services serve.

**Gate:** `probe_badge.py` 21/21; controls 19/21, 18/21, 18/21, 19/21; regressions `probe_ops_reads` 34/34,
`probe_arena_webhook` 30/30, `probe_taxonomy` 48/48; `concord-app` lib 4 passed. `routes.py` go 215 / port 206 / missing 11
(1 json, 10 page) — `/scout` is the last JSON route in row 11.
**Commit:** `94eff41`. **Files:** `crates/db/src/stats.rs` (`latency_for`, `delivery_latency_for_project`, `shipped_since`),
`crates/app/src/lib.rs` (`badge_svg`, `badge_svg_text`, `escape_xml`, `advance`, `latency_value`, 1 route),
`scripts/parity/probe_badge.py` (new, 21 checks).

### Part 65a — scout's engine, as its own crate (2026-11-14)

`GET /api/v1/scout` is the last JSON route in row 11, and it is the only one whose answer is a *judgement* rather than a row. It is split
in two: the engine here (no pool, no database), the store reads and the route in part 65b.

**Before:** `crates/scout` did not exist. `grep -rn "scout" crates/` found nothing but a comment.

**After:**
```
running 22 tests
test result: ok. 22 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out
```

**What was ported.** `decompose` (verbatim key → label word → hyphenated-token proposal, whole-word matching, cross-phase dedupe seeded
with the verbatim keys, duplicate-label collapse, in-catalogue-first ordering) and `classify` (licence veto → abandonment on KNOWN
metrics → uncontested lead → subset → complete → dead-but-instructive → unclassified).

**Three defects in the reference, found by porting it, recorded rather than copied silently:**

1. **The tie branch is dead in the Go.** `applyVerdict` reads `tiedNames(v.Signals)` *before* `Classify` appends the tie signals, so
   `signals` is empty at that moment and the branch can never fire — a tie falls through to `adopt`/`complete`. Both of the reference's
   own comments say a tie is a fork request rather than a base, and `scout-spec.md §3` says the same. **Decision (dated 2026-11-14):
   intent over code — the tie is passed into `apply_verdict`**, so tied candidates are `extend` and both names appear in the signals. This
   is the second time this port has had to choose the comment over the statement (part 51: "enrol after the vote is recorded"); the rule
   is the same and it is written into the ledger each time it is applied.
2. **`inspire` is nearly unreachable.** Every path out of the scoring has a branch above it, and the only route left is an idea that names
   NO catalogue capability at all — while the branch's comment calls it "still the closest thing in the catalog". Kept, because the verdict
   is part of the API and a client stores it; the test states the contradiction rather than hiding it.
3. **A tie is global, not per capability.** `fit`, `coverage` and `matched` are per project, so "tied on one axis, ahead on another" cannot
   be expressed by this scoring at all. The Go's per-capability loop *looks* like it supports that; it cannot.

**Also kept deliberately:** `fit` and `evidence_coverage` are the same number (`matched / total`). The reference's comment describes
coverage as "the share of those that had any evidence", which is the same figure when every satisfied capability is evidence. Kept
identical rather than corrected: the two are reported side by side, and inventing a second denominator here would make the port disagree
with the service it replaces on a page whose whole purpose is comparison.

**Mutation controls** (five, each killed by the test that guards it):
```
mutant no-known-check              → a_missing_attribute_is_unknown_and_a_no_is_evidence, a_proposal_carries_no_weight (3 failed)
mutant no-licence-veto             → a_licence_constraint_beats_a_healthy_lead, +2 (3 failed)
mutant tie-not-passed              → a_tie_awards_nobody_the_lead, a_missing_attribute… (2 failed)
mutant fit-always-one              → a_missing_attribute…, a_tie_awards_nobody_the_lead (2 failed)
mutant unmeasured-signal-dropped   → SURVIVED the first run (22 passed)
```
The survivor is the finding worth keeping: dropping `health_unmeasured` from the **complete** branch broke nothing, because the only test
for that signal used a *subset* match. The reference's own comment documents that exact bug ("A first version put it on `subset:` alone")
— so the port had reproduced the bug in its test suite rather than in its code. A test was added and the mutant now dies
(`a_complete_match_still_says_its_health_is_unmeasured`).

**Gate:** `cargo test -p concord-scout` → 22 passed; five mutants, five kills after the survivor was repaired.
**Commit:** `a8bc2c9`. **Files:** `crates/scout/Cargo.toml`, `crates/scout/src/lib.rs` (types, `classify`, `apply_verdict`,
`lead_counts`, `score`), `crates/scout/src/decompose.rs`, `crates/scout/tests/rules.rs` (22 tests), workspace `Cargo.toml`.

### Part 65b — the last JSON route, and a database that had been lying about its catalogue (2026-11-14)

**Before:** `GET /api/v1/scout` answered `404`. It was the only JSON route in row 11 still missing.

**After:**
```
$ python3 scripts/parity/probe_scout.py
ok   the idea is echoed trimmed: got 'a self hosted tool for WIP limits with offline use and live-sync' want '…'
ok   an unconfirmed assertion is partial coverage: got 'partial' want 'partial'
ok   the count is the whole catalogue, not the trimmed page: got 73 want 73
ok   and it is not counted either: the stranger's total is the readable catalogue: got 73 want 73
ok   the decomposition is byte-identical to the reference's: got [...] want [...]
ok   every verdict disagreement is a tie the reference could not express: got [] want []
55/55 ok
```

**What the route is.** Four reads behind one answer, each a different question and kept apart in `scout_reads` so a change to one cannot be
mistaken for a change to another: the **catalogue** (the vocabulary the decomposition matches, always whole — a filtered catalogue silently
changes what Scout can see), the **candidates** (readable projects with their asserted `value` attached), the **state counts** (from
`capability_confirmations`, not from the values — reading coverage out of the values makes `covered` unreachable and every capability comes
back `partial`), and **pain** (unresolved complaints, newest first).

**Where the decisions live, and why there rather than in the handler.** Health is read live through `ops_reads::project_health`, never from
`project_metrics.health_score` — that column is 0 for every project on this instance, so a rule keyed on it calls the whole catalogue dead.
`Some` means measured; `None` becomes `Health { known: false }` and the verdict says `health_unmeasured` instead of scoring the absence as
zero. Visibility is filtered **in the WHERE**, not afterwards: applying it afterwards reads the row first, and the next edit that adds a field
to the map would leak it for a project the caller cannot read.

**Zero verdict disagreements.** Across 73 projects and a `max=500` fetch on both sides, the port and the reference classify every project
identically — `got [] want []` for the disagreement list. Part 65a's tie fix was the one expected deviation and it fires only when two
candidates tie, which this idea does not produce.

**The finding that cost the most and had nothing to do with Scout.** The target database carried **120 fixture projects** — 111 `probe-…`
slugs and nine one-offs named `api-probe`, `arena-probe`, `created-probe`, `s2-probe`, `s3-probe` — left by earlier probe runs, against a
catalogue the reference serves as 73. Two consequences: `routes.py` and every count read off that database were describing fixtures, and the
differential probe reported 12 of 25 candidates in common because fixtures were crowding real projects out of the trimmed list. Purging them
took three failed scripts before one worked, and the failures are worth recording because each is a real property of the schema:

* `clean_project.py` has the right algorithm (depth-descending per root, derived from `pg_constraint`) and did not finish over 120 roots:
  thousands of statements on a slow disk, and it was killed three times before this part ended.
* A first rewrite keyed the doomed values by **(table, column)** — the column that names the parent — which cannot walk transitively: the
  ids of an `arenas` row are needed by `pairwise_votes.arena_id`, and no plan entry ever holds them, so the parent was deleted first.
* A second rewrite computed the delete order by leaf-peeling but compared `(parent, "_")` as a tuple, where `"_"` is the literal string: no
  edge ever matched, every table looked like a leaf, and parents were deleted before children again.
* `set session_replication_role = replica` — the standard bulk-delete route, which removes the ordering problem entirely — is **not
  permitted to the application role** (`permission denied to set parameter`). So the ordering cannot be avoided, only respected.

`scripts/parity/purge_fixtures.py` is the version that worked: collect each table's **own ids** (`id`) from the rows already identified and
walk outward level by level, then delete `where id = any(…)` with a savepoint per statement so a delete blocked by a foreign key is retried
on the next round instead of failing the transaction. Result: `projects 193 → 73`, which is exactly the catalogue the
reference serves, and a second run finds nothing (`fixture projects: 0`) — the purge is idempotent. One transaction throughout, so a failure leaves the database untouched.

**Controls** (startup seam, no rebuild):
```
control scout-private-leak:     53/57 — FAIL the PRIVATE fixture project is invisible to a stranger's report: got True want False;
                                         FAIL and it is not counted either: got 75 want 74
control scout-health-assumed:   33/37 — FAIL an unmeasured project is not called abandoned: got 'avoid' want 'adopt';
                                         37 of 73 verdicts flip adopt→avoid in the differential
control scout-ignore-license:   52/55 — FAIL a licence constraint is echoed so a reader can see it was applied: got None
```
The private-leak control is why the assertion is on the **count**, not on the verdict list: a leaked private project only appears in a
trimmed list when it happens to rank in the top 25, so a list-shaped assertion catches the defect on some runs and not others. The count
moves by exactly one, every run.

**Probe bug found while writing it:** the reference sits behind Cloudflare and rate-limits a client that asks several questions inside a
minute — `429` on the fourth call of a control loop. That is not a disagreement, so a persistent `429` is now reported as
`(not judged — reference unavailable)` and the differential checks become notes rather than failures. The alternative — treating it as a
failure — reports two services as differing because one refused to answer, which is the error this probe exists to avoid.

**Gate:** `probe_scout.py` 55/55; controls 53/57, 33/37, 52/55; `routes.py` **go 215 / port 207 / missing 10 — 0 JSON, 10 page**, and
`every go JSON route is answered`. Regressions: `concord-db` lib 38 passed, `concord-scout` 22 passed.
**Commit:** `9274bea`. **Files:** `crates/db/src/scout_reads.rs` (new: `catalog`, `candidates`, `state_counts`, `ids_of`, `pain`, `mutant`),
`crates/app/src/lib.rs` (`scout_report`, `apply_coverage`, 1 route, 2 startup seams), `scripts/parity/probe_scout.py` (new, 55 checks),
`scripts/parity/purge_fixtures.py` (new).

### Part 65c — the four admin binaries, and three timers the registry could not see (2026-11-14)

**Before:** `concord-cli granttrust` → `Error: unknown command granttrust`; `probe_admin_binaries.py` **3/13 ok**, and all three passes were
vacuous — the refusal assertions passed only because every command 404'd the same way.

**After:**
```
ok   a grant at the ceiling exits 0                                  ok   a complete mapping exits 0
ok   and writes the level to the account                             ok   and the repository is stored
ok   the grant carries the level                                     ok   a project that becomes a skip loses its repository
ok   a grant above the ceiling exits non-zero                        ok   a backfill under a model the index does not hold is refused
ok   and the refusal names the ceiling                               ok   and writes both vectors: title and full text
ok   the positional form is accepted                                 69/69 ok
```

**What each one is, and the rule that keeps it honest.**

* **`granttrust`** — the only writer of `users.trust_level`, because `SetTrustLevel` has no HTTP route: without it no account reaches
  the configured minimum and every trust-gated endpoint is unreachable for everyone, including the operator. Both writes (the grant row and
  the account column) are one transaction; the ceiling is read **inside** it, so a concurrent second writer cannot slip past a
  read-then-write check, and the refusal precedes both writes, so a refused assignment leaves no history of an attempt that did not happen.
  The ceiling is a **refusal, not a clamp**: a clamp reports success while storing something else. `granted_by` is the target account —
  reaching the database is the authorisation, and pretending there is an admin role would be a second permission system with one user.
* **`capableseed`** — the matrix is what Finder asks from, and a matrix nobody asserted into carries no information: the question selector
  picks by information gain, so an empty matrix makes the engine correctly decline to ask anything. Three refusals: the key is
  canonicalised **on write** (a second spelling would be a second capability holding half the assertions); re-registering may change the
  label and category but never the kind or the value set (the assertions point at the old vocabulary); a value must be one the capability
  permits. Assertions are written as claims and never as confirmed — a seed file that could manufacture a quorum would let whoever edits it
  promote their own claims.
* **`forgeseed`** — refuses more than it accepts, and the whole file is refused if any problem is present: an unlisted catalogue project, a
  slug that is not in the catalogue, a skip without a reason. Found on the instance this port replaces: a remote started answering 404, the
  mapping was changed to a skip, and the row kept its `repo_url` — so the hourly ingest kept failing against a URL the mapping no longer
  contained. Clearing is therefore a write (`repo_url = null`), not an absence.
* **`embedbackfill`** — deliberate, because a partial index silently answers "no duplicates found" and a filer reads that as "I am the
  first to report this". Re-running re-embeds exactly the rows with no vector from the **current** model and nothing else.

**The finding that changed the command.** Running `embedbackfill` against the live database indexed the whole complaint kind under the
port's default `hashed-v1` embedder — **4,818 vectors** beside the **5,482** `nomic` ones the service actually reads. Inert for readers
that filter by model id, and a trap for any reader that does not: two models' vectors in one table score nonsense against each other. The
Go tree documented this as a comment (`$CONCORD_EMBED_URL must be set identically for both`) and a comment refuses nothing. **Decision
(dated 2026-11-14): `embedbackfill` refuses to write when the table holds vectors from a model other than its own and none of its own,
naming the models present; `--force` overrides for a real model change.** The 4,818 stray rows were deleted with one
statement, `delete from embeddings where model_id = 'hashed-v1'`, verified by re-reading the table: 5,482 rows, all `nomic`.

**The three timers, and why the registry had never seen them.** `~/.config/systemd/user/concord-{healthsync,statsnapshot,proposalsweep}` have
existed for a year, running `/home/alvaro/concord-deploy/{…}` — copies of the Go build, **outside every owned-code prefix**, which is exactly
why `scheduled.toml` contained zero concord rows. Three `concord-rs-*` unit pairs now point at `deploy/concord-cli` inside the repository, so
a target that moves shows up as `target_exists = false` rather than as a timer that has been failing quietly. They are installed and
**disabled**, and the registry says so (`active = false`): they run the Rust binary against Postgres, which is not the live database until
the cutover row, and enabling them now would refresh one database twice and the other not at all.

**Controls** (startup/`CONCORD_MUTANT` seam, no rebuild):
```
control granttrust-clamp:            66/69 — FAIL a grant above the ceiling exits non-zero — code=0; a grant row was written for the attempt
control capableseed-shape:           68/69 — FAIL changing a capability's kind is refused
control forgeseed-skip-keeps-repo:   67/69 — FAIL and loses its repository: got ('https://github.com/example/probe', 'github')
control embedbackfill-title-only:    67/69 — FAIL and writes both vectors: got 1 want 2; FAIL and embeds nothing — embedded 77
```
The `granttrust-clamp` control is the one worth keeping: it moves three observations, because a clamped grant does not just succeed — it
writes a grant row and makes `granted_by` history say something the operator did not ask for.

**Gate:** `probe_admin_binaries.py` **69/69**; controls 66/69, 68/69, 67/69, 67/69; corpus `0 shape(s) disagreed of 16 checked, 5 skipped`;
`probe_s5_stats` 16/16; `concord-db --test store` **29 passed** against a scratch database; `concord-cli` 5 passed; `concord-db` lib 38
passed; `CHECK feed-routes: 8 arm(s) passed, 0 failed`.
**Commit:** `3ac7f5e`. **Files:** `crates/db/src/trust.rs` (new), `crates/db/src/capability_writes.rs` (new), `crates/db/src/forge_repos.rs`
(new), `crates/db/src/embeddings.rs` (`backfill`, `backfill_titles_only`), `crates/cli/src/lib.rs` (four subcommands, positional arguments,
four seams), `crates/cli/src/forge.rs` (`Repo::url`, `Display for RefusedUrl`), `scripts/parity/probe_admin_binaries.py` (new, 69 checks),
`~/code/estate/scheduled.toml` (three `concord-rs-*` jobs).

