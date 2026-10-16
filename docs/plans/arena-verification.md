# Plan — §29: one rating engine, a verifier that verifies, and the arena loop

**Status:** executed 2026-10-16. All ten steps landed; the inventory moved 229/22/0 → 233/19/0 and the
two bugs step 8 was written to catch are in `docs/REMAINING.md` §29.5.

Spec: `docs/specs/arena-verification-spec.md`. Written before any code. Every step ends with the exact
command that proves it, and what that command must print.

## Step 0 — the probe is not the test

`internal/store/zz_experiment_test.go` is a throwaway (it printed the drift above). Delete it in step 1
and keep its content as the failing test; a probe left in the tree is a test nobody runs.

    rm internal/store/zz_experiment_test.go
    go test ./internal/store/ -run TestZZExperiment -count=1   # want: "no tests to run"

## Step 1 — the RED test (before any fix)

Add to `internal/store/arenas_test.go`:

```go
// richArenaFixture builds the log the two rating paths have to agree on: three
// competitors, the permanent baseline (§6.3), both non-voting outcomes, a
// fractional weight, and two voters whose votes interleave in time.
func richArenaFixture(t *testing.T) (*DB, int64, []int64) { ... }
```

`TestTheReplayReproducesTheLiveRatingsExactly` compares, per entity, `r`/`rd`/`games` from
`ArenaLeaderboard` (incremental) against `RecomputeArenaRatings` (replay). Compare **exact** values:
both paths run the same pure function on the same numbers, so a tolerance would hide the drift this
test exists to find.

Two more tests, from the spec: `TestTheReplayAppliesTheBaselineRule`,
`TestTheReplayAppliesVotesInLogOrder`.

    go test ./internal/store/ -run 'TestTheReplay' -count=1
    # want: all three FAIL, naming the divergent entities

## Step 2 — one engine (`internal/store/arenas.go`)

```go
// rateArenaPair is the ONE place a comparison changes a rating.
//
// §5.1's update, its fractional-game treatment, and the games each side played
// are computed here so the incremental path (CastArenaVote -> applyGame) and the
// replay (replayArenaRatings) cannot disagree: they disagree by implementing one
// rule twice, which is exactly how the replay lost the baseline rule and froze
// `games` at 1.
func rateArenaPair(a, b ranking.ArenaRating, aScore, weight float64) (
	newA, newB ranking.ArenaRating, gamesA, gamesB int) {
	gamesA, gamesB = gameCount(weight), gameCount(weight)
	newA = ranking.UpdateArenaRating(a.R, a.RD, a.Sigma,
		repeatedGame(b, aScore, weight), weight)
	newB = ranking.UpdateArenaRating(b.R, b.RD, b.Sigma,
		repeatedGame(a, 1-aScore, weight), weight)
	return newA, newB, gamesA, gamesB
}

func gameCount(weight float64) int { n := int(weight); if n < 1 { return 1 }; return n }
```

- `applyGame` keeps its DB work and replaces its arithmetic with `rateArenaPair`, writing
  `games = games + gamesA` / `+ gamesB` (was `int(weight)` — same number, now from the engine).
- `outcomeScore` is unchanged (`a`→1, `b`→0, `both`/`neither`→0.5) and stays the a-vs-b score; the
  baseline rule is applied by the *caller*, in both paths, exactly as `CastArenaVote` does it today.

    gofmt -l internal/store/arenas.go && go build ./... && go test ./internal/store/ -run TestAVoteMovesBothRatingsInOppositeDirections -count=1
    # want: no gofmt output, build ok, ok

## Step 3 — the replay, extracted and shared

```go
// replayArenaRatings rebuilds every rating from the log WITHOUT writing: the
// verifier's read half. Returns ratings and games per entity id.
func (d *DB) replayArenaRatings(ctx context.Context, arenaID int64) (map[int64]ranking.ArenaRating, map[int64]int, error)
```

Rules, all three from the spec:

1. Read votes `WHERE arena_id = ? AND outcome != 'skip' ORDER BY created_at ASC, id ASC` and apply them
   **in that order**. Delete the `(voter, day)` grouping: the live path applies each vote as it arrives,
   so grouping reorders the two voters who voted in the same second apart — divergence 3.
2. Resolve the baseline once (`baselineEntry`). For `outcome == "neither"` with a baseline: one game per
   competitor against the baseline, score 0 (they lost) — and **no** a-vs-b game. This mirrors
   `CastArenaVote`'s branch; the replay must call `rateArenaPair` for each of those games.
3. Accumulate `games` with the same `gameCount` per side, and start from the prior
   (`ranking.DefaultRating`/`DefaultDeviation`/`DefaultVolatility`) with `games = 0`.

`RecomputeArenaRatings` becomes: `replayArenaRatings` → write `r`/`rd`/`sigma`/`games` per entry
(absolute, not `games + 1`) → mirror feature ratings → return `ArenaLeaderboard`.

    go test ./internal/store/ -run 'TestTheReplay' -count=1
    # want: PASS, both paths identical to the cent

## Step 4 — verification (`VerifyArenaRatings`)

```go
// ArenaVerification is one entry's stored rating beside its replay.
type ArenaVerification struct {
	EntityType string  `json:"entity_type"`
	EntityID   int64   `json:"entity_id"`
	StoredR, ReplayedR         float64 `json:"stored_r"`
	...
	Agrees bool `json:"agrees"`
}
func (d *DB) VerifyArenaRatings(ctx context.Context, arenaID int64) ([]ArenaVerification, error)
```

Replays (no writes) and compares against the stored rows. `TestVerificationAgreesAndCanDisagree`: after
votes it agrees; then `UPDATE arena_entries SET r = r + 1` and it does not — the gate must be able to
fail.

    go test ./internal/store/ -run 'TestVerification' -count=1

## Step 5 — the routes (`internal/httpapi/arenas.go`, new)

Registered in `server.go` next to the alternatives family, as the generic arena surface:

```go
r.Route("/api/v1/projects/{project_id}/arenas", func(r chi.Router) {
	r.Get("/{arena_id}/next-pair", s.handleArenaNextPair)
	r.Get("/{arena_id}/votes", s.handleArenaVotes)
	r.Get("/{arena_id}/verification", s.handleVerifyArena)
	r.Post("/{arena_id}/recompute", s.handleRecomputeArena)
})
```

Shared helper `s.arenaInProject(w, r) (store.Arena, int64, bool)`: parse `{arena_id}` (400 on garbage),
`requireProjectID`, `GetArena`, and a bare 404 when `arena.ProjectID != projectID`.

- `handleArenaNextPair`: `actorID(r)` (0 for anonymous), `NextArenaPair`, then a label per side
  (`GetFeature` title / `GetProjectByID` title+slug / `GetSolution` title, `""` for an unknown type),
  plus the derived `conservative` and the win probability from `ranking.WinProbability`.
- `handleArenaVotes`: `ListArenaVotes(arenaID, 50)`, each row carrying both sides' entity labels.
- `handleVerifyArena`: `VerifyArenaRatings` + `{agrees: bool, divergent: n, entries: [...]}`.
- `handleRecomputeArena`: `requireWriteActor` → `requireProjectID` → `requireRole(projectID,
  "contributor")` → `RecomputeArenaRatings` → the leaderboard plus how many entries changed.

    go test ./internal/httpapi/ -run 'TestArena' -count=1     # handler tests from the spec
    go test ./internal/store/ -run TestStoreExportInventory -count=1
    # want: the inventory demanding the three drop out of TEST-ONLY (they are routed now)

## Step 6 — the inventory, honestly

Drop `NextArenaPair`, `ListArenaVotes`, `RecomputeArenaRatings` from the testOnly map; add
`VerifyArenaRatings` as live (the gate enumerates exported methods, so a new one has to be classified).
`RemoveArenaEntry` stays, with its reason.

    go test ./internal/store/ -run TestStoreExportInventory -count=1 -v
    # want: 25 LIVE / 19 TEST-ONLY / 0 UNCALLED (numbers from the run, not from this plan)

## Step 7 — the panel (`internal/httpapi/assets/js/project.js`)

`alternativesPanel` gains, per arena block: `data-arena-pair="<id>"`, `data-arena-log="<id>"`,
`data-arena-verify="<id>"` controls, and `wireAlternativesPanel` fetches the three routes. The vote
control stays the existing one (the alternatives vote route) — the panel judges alternatives arenas, and
that is what its vote path already knows.

    node --check internal/httpapi/assets/js/project.js

## Step 8 — browser tests and mutants

`tests/e2e/arena_e2e.py`: the three controls, asserting **positive** states (a verdict string, a log row
carrying a vote's reason, a pair with two labelled sides) — never an absence, which a reload satisfies.
Add its port to the suite registry the harness self-test checks, and ≥3 collected tests.

`tests/e2e/project_ui_mutants.py`: 3 new mutants (the verification fetch path, the log's field name, the
pair control's selector).

    /usr/bin/python3 -m pytest -p no:cacheprovider tests/e2e/harness_selftest_test.py -q   # 12 passed
    python3 tests/e2e/ui_mutants.py --only project.js                                      # 11/11 killed

## Step 9 — docs and numbers

`docs/REMAINING.md` §29 (the finding, with the measured drift), the header's measured-state line, the
live tracker's counts, `docs/specs/frontend-spec.md`, and the spec/plan statuses. Then:

    make verify && make gates && make e2e

## Step 10 — publish

    git commit; git push forgejo master; git push github master; make deploy
    ssh thinkcentre 'systemctl --user restart concord; systemctl --user is-active concord'
    # live probes: GET verification on a real arena (agrees:true), GET votes, GET next-pair,
    # POST recompute without a token (401) and anonymous arena ids (404)
