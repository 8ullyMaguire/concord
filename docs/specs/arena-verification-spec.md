# Spec — §5.1's verifier, and the arena loop it belongs to

**Status:** IMPLEMENTED 2026-10-16 in §29 (`internal/store/arenas.go`'s shared engine and
`VerifyArenaRatings`, the four routes, the panel's three controls). Written before any code; the
measured defect it was written against, what three surviving mutants taught, and what is deliberately
still unreachable are in `docs/REMAINING.md` §29.

## The problem

`docs/concord-spec-r4.md` §5.1 promises:

> **Batch rating periods:** ratings are recomputed deterministically from the **append-only vote log**.
> Anyone can verify a ranking.

The recomputation exists — `store.RecomputeArenaRatings`, whose own doc comment says it "is the check
that the incremental path has not drifted: running it after votes have been cast must produce the same
order" — and **nothing outside tests calls it** (§27's inventory: *"nothing recomputes arena ratings
outside tests"*). The one test that compares the two paths
(`TestRecomputeFromTheVoteLogReproducesTheOrder`) compares ORDER ONLY, on a **feature** arena with **no
baseline** and no `neither` vote.

Measured, with a throwaway probe (`internal/store/zz_experiment_test.go`, run 2026-10-16): an arena
with three features **and a baseline** (§6.3's permanent do-nothing competitor), six votes — `a>b` ×3,
`b>c` ×3, one `neither`, one `skip`:

```
incremental  entity=0 baseline=true  r=1880.800 rd=242.649 games=2
incremental  entity=3 baseline=false r=1601.024 rd=219.896 games=4
incremental  entity=1 baseline=false r=1584.013 rd=230.510 games=4
incremental  entity=2 baseline=false r=1142.866 rd=208.414 games=6
recomputed   entity=1 baseline=false r=1717.831 rd=219.146 games=1
recomputed   entity=3 baseline=false r=1694.875 rd=216.523 games=1
recomputed   entity=0 baseline=true  r=1500.000 rd=350.000 games=1
recomputed   entity=2 baseline=false r=1142.866 rd=208.414 games=1
order_changed=true    DRIFT entity=1 +133.818   entity=3 +93.851   entity=0 -380.800
```

The replay **reorders two competitors**, **freezes the baseline** at its prior (the live path had it
leading, having won both `neither` votes), and reports **`games = 1` for every entry** no matter how
many games it played.

So §5.1's transparency claim is false in precisely the arena kind that has a baseline — which §6.3 says
is *every solution arena*. Worse, the recompute **writes**: §5.1 says recomputes "run frequently so the
UI feels live", and running this one replaces a correct leaderboard with a wrong one, indistinguishable
from a fresh measurement. **The verifier is a saboteur.**

## The cause

The rating rule is implemented **twice** — `CastArenaVote` → `applyGame` (live) and
`RecomputeArenaRatings` (replay) — and the two disagree in everything that is not "update a rating":

1. **§5.1's baseline rule.** *"In arenas with a baseline, Neither counts as both competitors losing to
   the baseline."* `CastArenaVote` implements it (two baseline-vs-competitor games, and no a-vs-b game);
   the replay scores `neither` as `outcomeScore("neither") = 0.5`, a draw.
2. **`games`.** `applyGame` adds `int(weight)` per side. The replay's final write adds `1` to every
   entry in the arena, and starts from `games = 0`.
3. **Application order.** The replay groups votes by `(voter, day)` and applies each group together; the
   live path applies each vote as it arrives. Two voters who vote in the same second are interleaved in
   the log and **batched apart** by the replay, so — the update being order-dependent — the two paths
   disagree for exactly the case §5.2 says is normal (a crowd voting at once).

The existing test could not see any of this: order is preserved by a uniform shift, and a baseline-less
arena never enters the branch.

## Scope

**In:**

- **One engine, two callers.** The pair update becomes a pure function both paths call, so a rule can no
  longer be implemented twice and drift.
- **The tests that pin it**: the two paths must agree **exactly** — `r`, `rd`, `games`, per entity — on a
  log containing a baseline, `neither`, `skip`, fractional weights, and two interleaved voters.
- **§5.1's "anyone can verify" become reachable**: a **verification** route that replays **without
  writing** and reports per-entry agreement, and a **recompute** route that repairs (and is therefore
  member-only).
- **The arena loop's read half**, so the arena is a surface and not a leaderboard: the **next pair** to
  judge (§5.1 pair selection) and the **vote log** (§5.2 *"a per-user vote audit view feed appeals"*).

**Out, and named in the inventory with reasons:** a generic per-arena **vote** route (the vote stays
where it is — the alternatives route and the feature/solution pages — because each kind's weight and
visibility rules differ and re-deriving them generically is its own pass); `RemoveArenaEntry` (no
removal flow); paging for a long log.

## Rules

1. **One engine.** `rateArenaPair(a, b, aScore, weight)` returns the two new ratings and the games each
   side played; `applyGame` and the replay both call it.
2. The replay **resolves the baseline once** from `arena_entries.is_baseline` — the same source the live
   path uses — and applies §5.1's rule for `neither`.
3. The replay applies votes **in `(created_at, id)` order, one game per side**, which is the order the
   live path applied them in. No `(voter, day)` grouping.
4. **games** ends equal to what the live path accumulated: `int(weight)` per side per game, minimum 1.
5. Verification **never writes**. It returns per entry the stored and replayed `r`/`rd`/`games` and
   whether they agree, plus a verdict and the count of divergent entries — a *check*, not a repair.
6. `skip` stays out of the rating in both paths (recorded, meaningless to the rating).
7. Every route is project-scoped **and** proves the arena belongs to that project, answering the same
   bare 404 as a missing arena: arena ids are global, and a distinct refusal is an existence oracle
   (the rule from §26 and §28).
8. The verification route needs **no role** — §5.1 says *anyone* can verify. The recompute route writes
   ratings, so it needs authentication and the project's contributor role, checked after the project is
   resolved (auth first, per `auth.go`).

## Routes

| route | actor | store call | why |
|---|---|---|---|
| `GET /api/v1/projects/{project_id}/arenas/{arena_id}/next-pair` | anyone who can read the project (`actorID` may be 0) | `NextArenaPair` | §5.1 pair selection: *"active learning (close ratings plus high RD) mixed with random exploration, a cap on repeated exposure, and no voting on your own items"*. A signed-out reader gets the active-learning pair; a voter gets one they have not judged. |
| `GET /api/v1/projects/{project_id}/arenas/{arena_id}/votes` | anyone who can read the project | `ListArenaVotes` | §5.2 *"a per-user vote audit view feed appeals"*. No permission gate, like the store's other log reads. |
| `GET /api/v1/projects/{project_id}/arenas/{arena_id}/verification` | anyone who can read the project | `VerifyArenaRatings` (new) | §5.1 *"Anyone can verify a ranking"* — the replay, compared, without writing. |
| `POST /api/v1/projects/{project_id}/arenas/{arena_id}/recompute` | authenticated + contributor | `RecomputeArenaRatings` | §5.1 *"Recomputes run frequently so the UI feels live"* — the repair, and the only writer. |

Both pair sides are labelled with what a voter is judging: a feature's title, a solution's title, or a
project's title and slug. A pair the voter cannot judge (every pair already judged) answers 404 with the
store's message, which names the reason.

## The panel

The alternatives panel (`project.js` `alternativesPanel`) already renders one block per arena, so it is
where the loop becomes visible: per arena, **Judge the next pair** (the two sides, with rating /
deviation / conservative score and the win probability, then the existing vote control), **Vote log**
(the most recent comparisons with their reasons), and **Verify ranking** (the verdict, and — for a
contributor — **Recompute**). A verification that only ran in tests would be the same unverifiable
promise in a new place.

## Tests

Store (`internal/store/arenas_test.go`):

- `TestTheReplayReproducesTheLiveRatingsExactly` — the rich log above; asserts `r`, `rd`, `games` per
  entity for BOTH paths (this is the test the probe showed failing).
- `TestTheReplayAppliesTheBaselineRule` — an arena with a baseline and a `neither` vote: the baseline
  gains and the two competitors lose, in the replay as in the live path.
- `TestTheReplayAppliesVotesInLogOrder` — two voters alternating: the replay's result must equal the
  live one (this is what the `(voter, day)` grouping got wrong).
- `TestVerificationAgreesAndCanDisagree` — after votes, verification reports agreement; then a stored
  rating is perturbed directly and verification reports the divergence (a check that cannot fail is
  decoration).

HTTP (`internal/httpapi/arenas_test.go`): each route's shape; another project's arena → 404 for all four;
an anonymous recompute → 401; a reader without the contributor role → 403; `next-pair` for a voter who
has judged every pair → 404 with the store's reason; `verification` on a consistent arena → `agrees:
true` and no divergent entries; `recompute` on a consistent arena changes nothing and says so.

Browser (`tests/e2e/arena_e2e.py`): the panel's three controls against a live seeded instance — a
verification verdict rendered, the log rendered with a vote's reason, the next pair rendered with both
sides labelled; mutants on `project.js` for the panel's fetch paths and controls.

Mutants: the replay's baseline branch, its `games` accounting, its ordering, and the verification's
comparison (each must be killed by the tests above).
