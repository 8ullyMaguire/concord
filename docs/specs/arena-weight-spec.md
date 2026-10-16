# Spec — §30: the influence of a vote, in one place

**Status:** IMPLEMENTED 2026-10-16 in §30 (`ranking.UpdateArenaRating`'s weight rule, `gameCount`'s zero
case, `Server.applyAgeGate` on all five vote routes, the panel's weight column). Written before any
code. The §4 sentence this spec supersedes, the measurement that forced the deviation, the three
fixtures that had been asserting a brand-new account's vote moves something, and what is deliberately
left alone are in `docs/REMAINING.md` §30.

## The claim

Two sentences, both about the same number.

§5.1, on the arena ranking:

> **Vote weights:** each vote has a weight from voter reputation and role. **Weight scales the update's
> influence:** a weight-2 voter is recorded as two games.

§12.3, on the account-age gate:

> **New-account gate:** a vote weight ramps from zero over the account's first days, so **a throwaway
> account cannot buy influence** with a fresh registration.

The second sentence is derived from the first: the gate works by making `weight` small, and it is only
a defence if a small weight produces a small update.

## What is actually true today

**Measured**, on the deployed instance (both instances run it, and this is the whole vote log):

```
sqlite> SELECT COUNT(*) total, SUM(CASE WHEN weight<1 THEN 1 ELSE 0 END) fractional FROM pairwise_votes;
2|2
```

Both votes in production carry `weight` 0.0004 and 0.016 — the ramp's first steps. Three separate
places make those weights inert:

1. **The engine inverts the zero case.** `ranking.UpdateArenaRating` opens with
   `if weight <= 0 { weight = 1 }`: a voter whose gated weight is exactly **0** — which is what
   `AgeGateMultiplier` returns for an account minutes old, and what its own test asserts
   (*"brand new account has no weight" → 0.0*) — is updated as a **full-weight** voter. The gate is not
   merely ignored, it is replaced by its opposite.
2. **A fractional weight is rounded up to a whole game.** `repeatedGame` builds `int(weight)` games
   with a floor of 1, so 0.0004 is **one full game** — 2500× its nominal influence.
3. **One vote route never applies the gate at all.** `alternatives.go` computes
   `ranking.VoteWeight(reputation, RoleMultiplier(role), cap)` and casts with it. The other four sites
   (feature votes in `handlers.go`, entry/answer votes in `lists.go`, solution votes in
   `solutions_weight.go`) each multiply by `ranking.AgeGateMultiplier(...)` last.

Net effect: on every arena ranking — feature-priority, solution and alternatives alike — the
account-age gate does not change the outcome. This is the same class as §29: a rule implemented once in
the reference implementation and again, differently, in the path that production uses.
`ranking.ApplyPairwiseVote` (`score.go`) has the correct model, documented and tested:

> the *rating change* is scaled by the voter's weight — r″ = r + w·(r′ − r), which is standard Glicko-2
> at w = 1. The RD/volatility update is always standard. […] A zero weight is a complete no-op on the
> ratings, RD and volatility alike, and the vote is still recorded. […] collapsing RD is itself a way to
> move a feature up the board without ever shifting its rating.

## The contract

One rule, in one place (`ranking.UpdateArenaRating` is the only implementation of "apply a weighted
comparison"), used by both arena callers:

| weight | rating | RD, volatility | `games` | recorded in the log |
|---|---|---|---|---|
| `w <= 0` | unchanged | unchanged | `+0` | yes |
| `0 < w < 1` | `r + w·(r_game − r)` | `rd + w·(rd_game − rd)`, same for volatility | `+1` | yes |
| `w >= 1` | `int(w)` repeated games (unchanged) | standard update | `+int(w)` | yes |

- **`w <= 0` is a complete no-op, not a full update.** Same rule, same reasoning and the same words as
  `ApplyPairwiseVote`. The vote is still written: §5.2 makes the log append-only, and §12.3's gate
  refuses influence, not participation.
- **`0 < w < 1` scales the whole update** — rating, deviation and volatility alike. It is continuous at
  `w = 1`.

  This is where the spec deviates from §4's comment, and the measurement is why. §4 says "the
  RD/volatility update is always standard", and implementing that sentence literally left the gate
  half-inert in the metric that ranks: a brand-new account's alternatives vote carried weight
  `2.9e-06`, moved the rating by `5e-04` (invisible), and **still took RD from 350 to 290**. §5.1 scores
  priority on the conservative estimate `r − 2·RD`, so that vote moved the number that orders the board
  by **119 points** while looking like it had done nothing — a throwaway account buying influence with
  a registration, which is the one thing §12.3 forbids. Both implementations (`ranking.ApplyPairwiseVote`
  and `ranking.UpdateArenaRating`) now apply the same interpolation, and §4's sentence is marked
  superseded where it sits.
- **`w >= 1` keeps repetition**, because §5.1 says a weight-2 voter *is* two games. Unchanged
  behaviour for every full-weight vote, so no existing leaderboard moves.
- **The age gate multiplies last, on every vote route**, through one helper: §12.3's defence must not be
  cancellable by the multiplier that precedes it, and a gate that five routes apply four different ways
  is a gate that one of them will not apply.

## What this deliberately does not change

- **§5.1's `cap`** (`charter.VoteWeightCap`) stays where it is: it bounds the weight, and the gate is
  applied after it so a new account's weight is at or below what the gate alone allows.
- **A sub-1 weight still counts as one comparison** in `games` (`0.4` is one vote, not zero), which is
  §5.1's counter of how many comparisons an entry has played rather than how much influence they
  carried.
- **Nothing in the storage schema.** The weight of a vote is already stored, and the replay already
  reads it; only its application changes.

## Verification

- `go test ./internal/ranking/ ./internal/store/ ./internal/httpapi/` — the engine's weight rule, the
  no-op case, the half-weight case, and one behavioural check per vote route that a brand-new account's
  vote moves nothing.
- Red proofs, each by reverting the fix alone: `weight <= 0 { weight = 1 }` restored → the no-op test
  fails; the delta scaling removed → the half-weight test fails; the gate line removed from
  `alternatives.go` → the route test fails.
- Mutants: *a zero-weight vote still applies its RD update*, *a fractional weight is rounded up to a
  full game*, *the alternatives route skips the age gate*.
- Live: the two production votes carry weights below 1 and their entities are not arena entries, so no
  stored rating changes — `POST …/recompute` answers `changed: 0` before and after.
