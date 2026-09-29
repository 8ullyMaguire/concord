package ranking

import (
	"math"
	"testing"
)

// TestRateGlickmanExample checks the published Glicko-2 worked example
// (Glickman, "Example of the Glicko-2 system", March 2022): player r=1500,
// RD=200, σ=0.06, τ=0.5 over one period with opponents 1400 (RD 30, win),
// 1550 (RD 100, loss), 1700 (RD 300, loss) → r' ≈ 1464.06, RD' ≈ 151.52,
// σ' ≈ 0.05999.
func TestRateGlickmanExample(t *testing.T) {
	games := []Game{
		{1400, 30, 1},
		{1550, 100, 0},
		{1700, 300, 0},
	}
	r, rd, sigma := Rate(1500, 200, 0.06, games, 0.5)

	if diff := math.Abs(r - 1464.06); diff > 0.1 {
		t.Fatalf("new rating = %.4f, want ~1464.06 (diff %.4f)", r, diff)
	}
	if diff := math.Abs(rd - 151.52); diff > 0.1 {
		t.Fatalf("new RD = %.4f, want ~151.52 (diff %.4f)", rd, diff)
	}
	if diff := math.Abs(sigma - 0.05999); diff > 0.0001 {
		t.Fatalf("new sigma = %.6f, want ~0.05999", sigma)
	}
}

func TestRateProperties(t *testing.T) {
	t.Run("win raises rating", func(t *testing.T) {
		r, _, _ := Rate(1500, 200, 0.06, []Game{{1500, 200, 1}}, 0.5)
		if r <= 1500 {
			t.Fatalf("r' = %.2f, want > 1500 after a win vs an equal player", r)
		}
	})
	t.Run("loss lowers rating", func(t *testing.T) {
		r, _, _ := Rate(1500, 200, 0.06, []Game{{1500, 200, 0}}, 0.5)
		if r >= 1500 {
			t.Fatalf("r' = %.2f, want < 1500 after a loss", r)
		}
	})
	t.Run("draw between equals stays equal", func(t *testing.T) {
		r, _, _ := Rate(1500, 200, 0.06, []Game{{1500, 200, 0.5}}, 0.5)
		if math.Abs(r-1500) > 1e-6 {
			t.Fatalf("r' = %.4f, want 1500 for a symmetric draw", r)
		}
	})
	t.Run("RD shrinks after playing", func(t *testing.T) {
		_, rd, _ := Rate(1500, 200, 0.06, []Game{{1500, 200, 1}}, 0.5)
		if rd >= 200 {
			t.Fatalf("RD' = %.2f, want < 200 after a rating period", rd)
		}
	})
	t.Run("idle period grows RD toward cap", func(t *testing.T) {
		_, rd, _ := Rate(1500, 350, 0.06, nil, 0.5)
		if rd != 350 {
			t.Fatalf("RD' = %.2f, want capped at 350 for an idle period at the cap", rd)
		}
	})
	t.Run("uncertain opponent damps the rating change", func(t *testing.T) {
		// Glicko-2 dampens updates by g(RD_j): a result against a high-RD
		// opponent carries less information about YOU, so the rating moves
		// less even though your own RD shrinks less predictably.
		certain, _, _ := Rate(1500, 200, 0.06, []Game{{1500, 30, 0}}, 0.5)
		uncertain, _, _ := Rate(1500, 200, 0.06, []Game{{1500, 300, 0}}, 0.5)
		if math.Abs(1500-uncertain) >= math.Abs(1500-certain) {
			t.Fatalf("loss vs uncertain opponent should move rating LESS "+
				"(certain Δ %.2f, uncertain Δ %.2f)", math.Abs(1500-certain), math.Abs(1500-uncertain))
		}
	})
}

func TestApplyPairwiseVote(t *testing.T) {
	tau := 0.5
	a := Feature{R: 1500, RD: 350, Vol: 0.06, StrategicWeight: 1}
	b := Feature{R: 1500, RD: 350, Vol: 0.06, StrategicWeight: 1}

	t.Run("weight one equals standard glicko", func(t *testing.T) {
		a2, b2 := ApplyPairwiseVote(a, b, OutcomeA, 1.0, tau)
		if a2.R <= a.R || b2.R >= b.R {
			t.Fatalf("weight-1 win should raise A and lower B: A %.2f→%.2f B %.2f→%.2f",
				a.R, a2.R, b.R, b2.R)
		}
	})
	t.Run("weight scales the delta", func(t *testing.T) {
		full, _ := ApplyPairwiseVote(a, b, OutcomeA, 1.0, tau)
		half, _ := ApplyPairwiseVote(a, b, OutcomeA, 0.5, tau)
		fullDelta := full.R - a.R
		halfDelta := half.R - a.R
		if math.Abs(halfDelta-fullDelta/2) > 1e-9 {
			t.Fatalf("weight 0.5 should halve the delta: full %.4f half %.4f", fullDelta, halfDelta)
		}
	})
	t.Run("both is a draw", func(t *testing.T) {
		a2, b2 := ApplyPairwiseVote(a, b, OutcomeBoth, 1.0, tau)
		if math.Abs(a2.R-1500) > 1e-6 || math.Abs(b2.R-1500) > 1e-6 {
			t.Fatalf("symmetric draw must not move equal ratings: A %.4f B %.4f", a2.R, b2.R)
		}
	})
	t.Run("neither and skip change nothing", func(t *testing.T) {
		for _, out := range []Outcome{OutcomeNeither, OutcomeSkip} {
			a2, b2 := ApplyPairwiseVote(a, b, out, 1.0, tau)
			if a2 != a || b2 != b {
				t.Fatalf("outcome %q must not change ratings", out)
			}
		}
	})
}

func TestVoteWeight(t *testing.T) {
	cases := []struct {
		rep, mult, cap, want float64
	}{
		{0, 1.0, 3.0, 1.0},
		{9, 1.25, 3.0, 2.5},   // contributor: (1+log10(10))*1.25
		{99, 1.0, 3.0, 3.0},   // (1+log10(100)) = 3.0, at the cap
		{9999, 2.0, 3.0, 3.0}, // capped
	}
	for _, c := range cases {
		got := VoteWeight(c.rep, c.mult, c.cap)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("VoteWeight(rep=%v, mult=%v) = %v, want %v", c.rep, c.mult, got, c.want)
		}
	}
}
func TestAgeGateMultiplier(t *testing.T) {
	cases := []struct {
		name string
		age  float64
		want float64
	}{
		{"brand new account has no weight", 0, 0.0},
		{"half the window is half weight", AccountAgeGateDays / 2, 0.5},
		{"at the window is full weight", AccountAgeGateDays, 1.0},
		{"beyond the window stays at full", AccountAgeGateDays * 10, 1.0},
		{"a year-old account is unaffected", 365, 1.0},
		// Clock skew must not penalise the voter: a created_at slightly in the
		// future reads as brand new (weight 0), not as a negative multiplier.
		{"future created_at reads as brand new", -0.5, 0.0},
		{"far-future created_at is still clamped", -30, 0.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AgeGateMultiplier(c.age); math.Abs(got-c.want) > 1e-9 {
				t.Errorf("AgeGateMultiplier(%v) = %v, want %v", c.age, got, c.want)
			}
		})
	}
}

func TestAccountAgeDays(t *testing.T) {
	now := 1_700_000_000.0
	if got, want := AccountAgeDays(now, now), 0.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("AccountAgeDays(now, now) = %v, want 0", got)
	}
	if got, want := AccountAgeDays(now-7*86400, now), 7.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("AccountAgeDays(7d ago) = %v, want 7", got)
	}
}

// The gate is a multiplier on weight, so it must never make a vote *stronger*
// than the unramped weight — only weaker, and never negative.
func TestAgeGateOnlyReducesWeight(t *testing.T) {
	full := VoteWeight(50, 1.0, 3.0)
	for _, age := range []float64{0, 1, 3, 6, 6.9, 7, 30} {
		gated := full * AgeGateMultiplier(age)
		if gated > full+1e-9 {
			t.Errorf("age %v: gated weight %v exceeded ungated %v", age, gated, full)
		}
		if gated < 0 {
			t.Errorf("age %v: gated weight %v is negative", age, gated)
		}
	}
}

// A brand-new account weighs zero. The vote is still recorded, but it must
// leave the ratings completely untouched — including RD and volatility, since
// priority is scored on the lower confidence bound and shrinking RD is itself
// a way to push a feature up the board.
func TestAgeGateZeroWeightIsFullNoOp(t *testing.T) {
	if m := AgeGateMultiplier(0); m != 0 {
		t.Fatalf("expected zero multiplier for a brand-new account, got %v", m)
	}
	a := Feature{R: 1500, RD: 350, Vol: 0.06}
	b := Feature{R: 1500, RD: 400, Vol: 0.07}
	a2, b2 := ApplyPairwiseVote(a, b, OutcomeA, 0, 0.5)
	if a2 != a || b2 != b {
		t.Errorf("zero-weight vote changed state: a %+v -> %+v, b %+v -> %+v", a, a2, b, b2)
	}
}

// A partially-ramped account (day 3 of 7) does move the rating, and moves it
// strictly less than a fully-established voter would.
func TestAgeGateRampScalesButPreservesInfluence(t *testing.T) {
	a := Feature{R: 1500, RD: 350, Vol: 0.06}
	b := Feature{R: 1500, RD: 350, Vol: 0.06}
	ramped, _ := ApplyPairwiseVote(a, b, OutcomeA, 0.5, 0.5)
	full, _ := ApplyPairwiseVote(a, b, OutcomeA, 1.0, 0.5)
	if ramped.R == a.R {
		t.Error("a half-ramped account should still move the rating")
	}
	// "Less influence" means a smaller *move* from the starting rating, not a
	// lower ending rating: a weaker voter's outcome is a shorter step away
	// from the prior. Asserting on the delta catches the real property.
	if rampedDelta, fullDelta := ramped.R-a.R, full.R-a.R; rampedDelta >= fullDelta {
		t.Errorf("half-ramped voter moved rating by %v, full voter by %v: the ramp did not damp the vote",
			rampedDelta, fullDelta)
	}
}

func TestPainAndPriority(t *testing.T) {
	// severity 5, frequency 1, affected 10, no decay: 5·ln(11) ≈ 11.9829
	pain := PainScore(5, 1, 1, 10, 0, 90)
	if math.Abs(pain-5*math.Log(11)) > 1e-9 {
		t.Fatalf("PainScore = %.6f, want %.6f", pain, 5*math.Log(11))
	}
	half := PainScore(5, 1, 1, 10, 90, 90) // one halflife later
	if math.Abs(half-pain/2) > 1e-9 {
		t.Fatalf("pain after one halflife = %.6f, want %.6f", half, pain/2)
	}

	prio := PriorityScore(1500, 100, pain, 1.0, 20, 100)
	want := (1500 - 200) + 20*math.Log1p(pain) + 100
	if math.Abs(prio-want) > 1e-9 {
		t.Fatalf("PriorityScore = %.6f, want %.6f", prio, want)
	}
}

func TestDecayPoints(t *testing.T) {
	if got := DecayPoints(10, 0, 100); got != 10 {
		t.Fatalf("no age: got %v", got)
	}
	if got := DecayPoints(10, 180, 180); math.Abs(got-5) > 1e-9 {
		t.Fatalf("one halflife: got %v, want 5", got)
	}
	if got := DecayPoints(10, 180, 0); got != 10 {
		t.Fatalf("zero halflife disables decay: got %v", got)
	}
}
