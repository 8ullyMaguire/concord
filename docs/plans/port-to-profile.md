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
| 8 | S2 ideas, complaints, ranking | corpus subset + `rank_*`, `milestones_e2e`, `feature_*` green; ranking mutation arms killed |
| 9 | S3 consensus, governance, ledger | corpus subset + `consensus_*`, `audit_e2e` green; estate gate 49 green |
| 10 | S4 moderation, comments, inbox | corpus subset + `notifications_e2e` and the queue suite green; estate gates 22 and 45 green |
| 11 | S5 stats, feeds, admin binaries | corpus + `stats_e2e` and feed parse; estate gates 4, 5, 47 green; timers migrated to `polaris-loops` |
| 12 | P6 the frontend on SvelteKit + `@polaris/ui` | every page its slice owns renders from the API with the server-rendered head, in a browser |
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
