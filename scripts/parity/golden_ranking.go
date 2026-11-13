// The ranking crate's oracle: values printed by the Go implementation that still serves production.
//
// The mutation gate found 99 survivors across `concord-ranking` and `concord-governance`, and almost
// every one is an arithmetic operator inside `rate` / `apply_pairwise_vote` / `vote_weight`. A loose
// assertion (`> 100`) cannot kill `+` -> `-`; only an exact number can, and an exact number invented by
// hand is a number the Rust port could get wrong in the same direction and agree with itself about. So
// the numbers come from here -- the Go functions in `internal/ranking` and `internal/governance` that
// still serve concord.polarisocial.xyz -- printed at full precision and read back by
// `crates/ranking/tests/golden.rs`.
//
// Usage: go run ./scripts/parity/golden_ranking.go > crates/ranking/tests/golden.tsv
package main

import (
	"fmt"

	"git.polarisocial.xyz/concord/concord/internal/governance"
	"git.polarisocial.xyz/concord/concord/internal/ranking"
)

func row(tag string, values ...any) {
	fmt.Print(tag)
	for _, value := range values {
		switch typed := value.(type) {
		case float64:
			fmt.Printf("\t%.17g", typed)
		case float32:
			fmt.Printf("\t%.17g", float64(typed))
		case bool:
			fmt.Printf("\t%t", typed)
		default:
			fmt.Printf("\t%v", typed)
		}
	}
	fmt.Println()
}

func main() {
	// 1. rate(): a grid wide enough that a folded loop, a moved anchor or a flipped sign changes a digit.
	//
	// Opponents sit on both sides of our rating and at three RDs, because a sign error is invisible when
	// every opponent is on one side, and an RD error is invisible when every opponent is equally certain.
	type opp struct {
		r, rd, score float64
	}
	opponents := []opp{}
	for _, r := range []float64{1200, 1500, 1800} {
		for _, rd := range []float64{50, 100, 350} {
			for _, score := range []float64{0, 0.5, 1} {
				opponents = append(opponents, opp{r, rd, score})
			}
		}
	}
	for _, start := range [][3]float64{
		{1500, 350, 0.06},
		{1500, 100, 0.06},
		{1500, 50, 0.02},
		{1800, 120, 0.05},
		{1300, 400, 0.09},
	} {
		for _, count := range []int{0, 1, 2, 3, 6, 27} {
			games := make([]ranking.Game, 0, count)
			for i := 0; i < count && i < len(opponents); i++ {
				games = append(games, ranking.Game{
					OpponentR:  opponents[i].r,
					OpponentRD: opponents[i].rd,
					Score:      opponents[i].score,
				})
			}
			r, rd, vol := ranking.Rate(start[0], start[1], start[2], games, 0.5)
			row("rate", start[0], start[1], start[2], count, r, rd, vol)
		}
	}

	// 1b. The regula-falsi `else` branch in `rate`.
	//
	// When delta^2 <= phi^2 + v the volatility iteration starts from `A - k*tau` found by stepping down
	// until f goes negative, instead of from `ln(delta^2 - phi^2 - v)`. Almost every game in the grid
	// above takes the other branch, so the mutants in that step survived: a game whose result MATCHES the
	// expectation (delta ~ 0) against a high-RD opponent is what reaches it, and 0.5 against an equal
	// opponent is the sharpest case.
	for _, start := range [][3]float64{
		{1500, 350, 0.06},
		{1500, 250, 0.06},
		{1500, 120, 0.03},
	} {
		for _, one := range []opp{
			{1500, 350, 0.5}, // exactly the expectation
			{1505, 350, 0.5}, // and a hair off it
			{1490, 300, 0.5},
			{1500, 200, 1.0}, // a win the expectation half-expected
			{1500, 200, 0.0},
		} {
			games := []ranking.Game{{OpponentR: one.r, OpponentRD: one.rd, Score: one.score}}
			r, rd, vol := ranking.Rate(start[0], start[1], start[2], games, 0.5)
			row("rate1", start[0], start[1], start[2], one.r, one.rd, one.score, r, rd, vol)
		}
	}

	// 1c. Small-RD starts, where phi^2 and 2*phi are far apart (phi = 0.29 for rd 50: 0.083 against
	// 0.576). The branch test is `delta^2 > phi^2 + v`, so a mutant that turns `phi*phi` into `phi+phi`
	// is invisible at high RD -- where the two agree to three digits -- and decides the branch here.
	for _, start := range [][3]float64{
		{1500, 50, 0.02},
		{1500, 100, 0.06},
		{1600, 50, 0.02},
		{1400, 80, 0.04},
	} {
		for _, one := range []opp{
			{1500, 50, 1.0}, {1500, 50, 0.0}, {1600, 50, 1.0}, {1400, 50, 0.0},
			{1500, 100, 0.5}, {1800, 60, 1.0}, {1200, 60, 0.0},
		} {
			games := []ranking.Game{{OpponentR: one.r, OpponentRD: one.rd, Score: one.score}}
			r, rd, vol := ranking.Rate(start[0], start[1], start[2], games, 0.5)
			row("rate1", start[0], start[1], start[2], one.r, one.rd, one.score, r, rd, vol)
		}
	}

	// 2. apply_pairwise_vote(): the weight clamp, the zero-weight no-op and the volatility carry, each of
	// which is a branch a survivor can delete.
	a := ranking.Feature{R: 1520, RD: 90, Vol: 0.06, StrategicWeight: 1.5}
	b := ranking.Feature{R: 1470, RD: 110, Vol: 0.061, StrategicWeight: 0.5}
	for _, out := range []ranking.Outcome{
		ranking.OutcomeA, ranking.OutcomeB, ranking.OutcomeBoth,
		ranking.OutcomeNeither, ranking.OutcomeSkip,
	} {
		for _, weight := range []float64{-1, 0, 0.25, 1, 2.5} {
			ra, rb := ranking.ApplyPairwiseVote(a, b, out, weight, 0.5)
			row("pairwise", string(out), weight,
				ra.R, ra.RD, ra.Vol, ra.StrategicWeight,
				rb.R, rb.RD, rb.Vol, rb.StrategicWeight)
		}
	}

	// 3. The small functions, over every branch rather than one sample each.
	for _, role := range []string{"owner", "maintainer", "reviewer", "contributor", "observer", "unknown", ""} {
		row("role", role, ranking.RoleMultiplier(role))
	}
	for _, args := range [][3]float64{
		{0, 1, 2}, {1, 1, 2}, {2.5, 1, 2}, {3, 1, 2}, {1, 0.5, 2}, {4, 3, 1}, {1, 0.5, 0}, {-1, 1, 2},
	} {
		row("vote_weight", args[0], args[1], args[2], ranking.VoteWeight(args[0], args[1], args[2]))
	}
	for _, age := range []float64{-1, 0, 0.5, 1, 3, 7, 14, 30, 365} {
		row("age_gate", age, ranking.AgeGateMultiplier(age))
	}
	// account_age_days had no test at all: a mutant that returns 1.0, or that adds the timestamps
	// instead of subtracting, was free to pass. Values straddling the 7-day gate, and one where now is
	// BEFORE created_at so the sign is live.
	for _, pair := range [][2]float64{
		{1_700_000_000, 1_700_000_000},
		{1_700_000_000, 1_700_000_000 + 3 * 86_400},
		{1_700_000_000, 1_700_000_000 + 7 * 86_400},
		{1_700_000_000, 1_700_000_000 + 400 * 86_400},
		{1_700_000_000, 1_600_000_000},
	} {
		row("account_age", pair[0], pair[1], ranking.AccountAgeDays(pair[0], pair[1]))
	}
	for _, args := range [][3]float64{
		{100, 0, 30}, {100, 15, 30}, {100, 30, 30}, {100, 45, 30}, {0, 500, 30}, {100, 1, 0}, {-5, 10, 30},
	} {
		row("decay", args[0], args[1], args[2], ranking.DecayPoints(args[0], args[1], args[2]))
	}
	for _, args := range [][6]float64{
		{1, 1, 1, 1, 0, 180}, {2, 2, 1, 3, 10, 180}, {3, 1, 1.5, 1, 400, 180},
		{1, 5, 1, 2, 5, 30}, {2, 1, 2, 4, 60, 60}, {0, 0, 1, 0, 0, 180},
	} {
		row("pain", args[0], args[1], args[2], args[3], args[4], args[5],
			ranking.PainScore(int(args[0]), args[1], args[2], args[3], args[4], args[5]))
	}
	for _, args := range [][6]float64{
		{1500, 100, 0, 0, 0.5, 0.1},
		{1500, 100, 10, 1, 0.5, 0.1},
		{1200, 350, 5, 0, 0.7, 0.2},
		{1900, 80, 3.5, 2, 0.3, 0.05},
		{0, 0, 0, 0, 0, 0},
	} {
		row("priority", args[0], args[1], args[2], args[3], args[4], args[5],
			ranking.PriorityScore(args[0], args[1], args[2], args[3], args[4], args[5]))
	}

	// 4. The governance counts, where a comparison operator is the whole rule.
	for _, counts := range []governance.ConsensusCounts{
		{Consent: 0, Abstain: 0, StandAside: 0, Block: 0, Participants: 0, Eligible: 0},
		{Consent: 3, Abstain: 0, StandAside: 0, Block: 0, Participants: 3, Eligible: 3},
		{Consent: 2, Abstain: 1, StandAside: 1, Block: 0, Participants: 4, Eligible: 5},
		{Consent: 2, Abstain: 0, StandAside: 0, Block: 1, Participants: 3, Eligible: 3, OpenObjections: 1},
		{Consent: 5, Abstain: 2, StandAside: 0, Block: 0, Participants: 7, Eligible: 10},
	} {
		row("counts", counts.Consent, counts.Abstain, counts.StandAside, counts.Block,
			counts.Participants, counts.Eligible, counts.OpenObjections,
			counts.Decisive(), counts.SupportRatio(), counts.DecisiveRatio(), counts.Reluctant())
	}

	// 5. The enums, arm by arm: a deleted match arm survives unless every arm is exercised, and an
	// `as_str` replaced by "" survives unless its output is compared to a literal.
	// The governance enums have no Go parser to be an oracle for (`Position` is a bare string type with
	// no `Valid`), so their arms are covered by exhaustive Rust tests against literals instead -- see
	// `the_enums_round_trip_every_arm` in crates/governance.

	for _, outcome := range []ranking.Outcome{
		ranking.OutcomeA, ranking.OutcomeB, ranking.OutcomeBoth, ranking.OutcomeNeither,
		ranking.OutcomeSkip, "nonsense",
	} {
		sA, sB := outcome.ScorePair()
		row("outcome", string(outcome), outcome.AppliesRatingChange(), sA, sB)
	}
}
