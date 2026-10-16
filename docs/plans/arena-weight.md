# Plan — §30: the influence of a vote, in one place

**Status:** executed 2026-10-16. All eight steps landed. Two things went differently and are recorded in
`docs/REMAINING.md` §30: step 1's rule had to damp RD and volatility as well as the rating (a 3e-06
weight left RD free to fall, and priority is scored on `r - 2*RD`, so the gate was half-inert in the
metric that ranks), and step 5's alternatives arm produced a survivor -- the vote handler's own
copy of the "not against itself" rule had no test. Spec: `docs/specs/arena-weight-spec.md`.

Every step names its file, the shape of the change, and the command that proves it. Steps 1–2 are the
engine, 3 is the route, 4–6 are the gates, 7 is the documentation, 8 is the release. Steps 1, 2 and 3
each have a RED proof: revert that change alone and name the test that goes red.

## Step 1 — the engine honours the weight (`internal/ranking/arena.go`)

`UpdateArenaRating(r, rd, sigma, games, weight)` becomes:

```go
func UpdateArenaRating(r, rd, sigma float64, games []Game, weight float64) ArenaRating {
	if weight <= 0 {
		// §12.3's gate refusing a throwaway account's influence, and §4's rule for a
		// zero-weight vote: a complete no-op, RD and volatility included. Leaving the
		// standard update would still let a mass of new accounts shrink the
		// uncertainty, and priority is scored on r - 2*RD, so collapsing RD is itself
		// a way to move a competitor up the board. ApplyPairwiseVote says this in the
		// same words; this function used to say `weight = 1`.
		return ArenaRating{R: r, RD: rd, Sigma: sigma}
	}
	nr, nrd, ns := Rate(r, rd, sigma, games, DefaultTau)
	if weight < 1 {
		// §5.1's "weight scales the update's influence", continuous below 1: the
		// comparison is one game and the weight scales how far the entry moves --
		// rating, RD and volatility alike. §4's "the RD/volatility update is always
		// standard" is superseded here: at weight 3e-06 a vote moved the rating by
		// 5e-04 and still collapsed RD 350 -> 290, and priority is scored on r - 2*RD.
		nr = r + weight*(nr-r)
		nrd = rd + weight*(nrd-rd)
		ns = sigma + weight*(ns-sigma)
	}
	return ArenaRating{R: nr, RD: nrd, Sigma: ns}
}
```

Doc comment: replace the paragraph claiming "weight […] is NOT applied to the score" with the table
from the spec, so the rule is stated where it is implemented.

Verify: `go test ./internal/ranking/ -run TestWeight -count=1` → `ok`.

## Step 2 — a no-op vote is not a game (`internal/store/arenas.go`)

```go
func gameCount(weight float64) int {
	if weight <= 0 {
		return 0 // an influence-free vote is not a comparison the entry played
	}
	n := int(weight)
	if n < 1 {
		return 1 // one comparison was made, even at 0.4
	}
	return n
}
```

`repeatedGame` is unchanged (it builds `int(weight)` games, floored at 1, and step 1 scales the delta
for the sub-1 case; for `weight <= 0` the game list is never applied).

Verify: `go test ./internal/store/ -run 'TestAZeroWeightVote|TestAFractionalWeight' -count=1` → `ok`
(new tests, step 4).

## Step 3 — the gate, applied last, on every vote route

One helper in `internal/httpapi/voteweight.go`:

```go
// applyAgeGate multiplies §12.3's account-age ramp into a vote weight, LAST.
//
// Last is load-bearing and is why this is a helper rather than a line: the ramp is
// Sybil defence (§12.3), and a defence that any multiplier preceding it can cancel is
// not a defence. Four routes applied it four ways and a fifth did not apply it at all
// -- alternatives.go cast with the ungated weight, so a brand-new account's comparison
// counted in full on the one arena kind the panel can judge.
func (s *Server) applyAgeGate(ctx context.Context, actorID int64, weight *float64) {
	createdAt, err := s.Store.GetUserCreatedAt(ctx, actorID)
	if err != nil {
		return
	}
	*weight *= ranking.AgeGateMultiplier(ranking.AccountAgeDays(createdAt, float64(time.Now().Unix())))
}
```

Call sites, each reduced to the same two lines: `alternatives.go` (the fix), `handlers.go`,
`lists.go` (`entryVoteWeight`, `answerVoteWeight`), `solutions_weight.go` (after its expertise step).

Verify: `go test ./internal/httpapi/ -run 'VoteWeight|AgeGate|AlternativesVote' -count=1` → `ok`.

## Step 4 — the tests that must fail without steps 1–3

`internal/store/arenas_test.go`:

- `TestAZeroWeightVoteChangesNothingButIsStillRecorded` — a 0-weight vote leaves `r`, `rd`, `sigma` and
  `games` untouched for both entries and the `pairwise_votes` row exists. RED with `weight = 1`
  restored (`rd` moves, `games` +1).
- `TestAFractionalWeightMovesTheRatingByThatFraction` — the same vote at weight 1.0 and at 0.25 from
  identical starting state: `delta(0.25) == 0.25 * delta(1.0)` within 1e-9, and both count one game.
  RED with scaling removed (the two deltas are equal).
- `TestAWeightTwoVoteIsTwoGames` — unchanged behaviour, pinned because step 2 touched `gameCount`.
- `TestTheReplayAgreesOnAWeightedLog` — the existing agreement test grows a 0.25-weight vote and a
  0-weight vote, so the replay is exercised on the new arithmetic (one engine, two callers).

`internal/httpapi/arenas_test.go` and `internal/httpapi/alternatives_test.go`:

- `TestABrandNewAccountsAlternativesVoteMovesNothing` — POST the alternatives vote from an account
  created now, assert `weight: 0` in the response and identical ratings afterwards. RED with the gate
  line removed from `alternatives.go`.
- `TestABrandNewAccountCastsNoFeatureVote` — the same for the feature-priority route (this one already
  applies the gate; the test pins it against a refactor).

Also extend the panel's log row to name the weight, so §5.2's audit view shows the number the gate acts
on, with a UI mutant (`the vote log hides the weight`).

Verify: `go test ./... -count=1` → all pass; `make verify` → exit 0.

## Step 5 — mutants

`internal/store/arenas_mutants.json` (arm grows by 3):

- `a zero-weight vote still applies its RD update` — `return ArenaRating{R: r, RD: rd, ...}` →
  `nr, nrd, ns := Rate(...)`; `return ArenaRating{R: nr, RD: nrd, Sigma: ns}`.
- `a fractional weight is rounded up to a full game` — `if weight < 1 {` → `if false {`.
- `the alternatives route skips the age gate` — remove the `s.applyAgeGate(...)` call.

Verify: `python3 internal/mutate.py internal/store/arenas.go …` → `13/13 killed` → `16/16 killed`;
`make gates` → exit 0.

## Step 6 — run the full chain

```
make verify   # → VERIFY_EXIT=0
make gates    # → GATES_EXIT=0, every arm 0 survived, 0 not applicable
make e2e      # → E2E_EXIT=0
```

Then update the measured numbers in `docs/REMAINING.md` and the three docs that quote the Go test
count, and re-run `python3 scripts/check_doc_numbers.py` → `OK … agree with the tree`.

## Step 7 — documentation

- `docs/REMAINING.md` §30: the three places, the live measurement, the fix, the residual (RD on a
  sub-1 weight), the new numbers.
- `docs/specs/frontend-spec.md`: the log row names the weight.
- SecondBrain note; the skills' lessons (`a gate that is applied N times is applied N-1 times`;
  `an inverted default is worse than a missing one`).

## Step 8 — release

```
git push forgejo master && git push github master
make deploy && scp the binary to thinkcentre && restart
```

Live verification, on both instances:

- `POST /api/v1/projects/{slug}/arenas/{id}/recompute` as a member → `changed: 0` (the two production
  votes are sub-1 and their entities are not entries, so no stored rating may move).
- The `/votes` response still carries each stored weight (0.0004, 0.016) — the log is unchanged.
- `GET …/verification` → `agrees: true` over every entry, including the sub-1 votes.
