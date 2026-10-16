package ranking

// §30: the influence of a vote, at the engine.
//
// §5.1 says "weight scales the update's influence" and §12.3 derives the account-age
// gate from that sentence: a new account's weight ramps from zero over its first days
// so "a throwaway account cannot buy influence". Both are only true if a small weight
// produces a small update, and this function used to say `if weight <= 0 { weight = 1 }`
// -- handing the FULLEST possible update to the accounts the gate exists to stop.

import (
	"math"
	"testing"
)

func TestAZeroWeightChangesNothingAtAll(t *testing.T) {
	opponent := []Game{{OpponentR: 1500, OpponentRD: 350, Score: 1.0}}
	got := UpdateArenaRating(1500, 350, 0.06, opponent, 0)
	want := ArenaRating{R: 1500, RD: 350, Sigma: 0.06}
	if got != want {
		t.Errorf("a zero-weight comparison changed the entry: %+v, want %+v without a single "+
			"field moved. RD matters as much as R: §5.1 scores priority on r - 2*RD, so an "+
			"account whose vote moves no rating can still move a competitor up the board by "+
			"collapsing its uncertainty", got, want)
	}
	// And the control: a full-weight comparison DOES move, or the test above proves
	// nothing about the weight.
	if full := UpdateArenaRating(1500, 350, 0.06, opponent, 1.0); full.R == 1500 {
		t.Fatal("a full-weight comparison did not move the rating: the fixture proves nothing")
	}
}

func TestAWeightBelowOneScalesTheWholeUpdate(t *testing.T) {
	opponent := []Game{{OpponentR: 1500, OpponentRD: 350, Score: 1.0}}
	full := UpdateArenaRating(1500, 350, 0.06, opponent, 1.0)
	quarter := UpdateArenaRating(1500, 350, 0.06, opponent, 0.25)

	for _, c := range []struct {
		name      string
		got, want float64
	}{
		{"rating", quarter.R, 1500 + 0.25*(full.R-1500)},
		{"deviation", quarter.RD, 350 + 0.25*(full.RD-350)},
		{"volatility", quarter.Sigma, 0.06 + 0.25*(full.Sigma-0.06)},
	} {
		if math.Abs(c.got-c.want) > 1e-9 {
			t.Errorf("weight 0.25 left %s at %.9f, want %.9f (a quarter of the full update's "+
				"move): the weight scales the update's influence (§5.1), and the deviation is "+
				"part of the update -- priority is scored on r - 2*RD", c.name, c.got, c.want)
		}
	}
}

func TestAWeightOfOneIsStandardGlicko(t *testing.T) {
	games := []Game{{OpponentR: 1400, OpponentRD: 100, Score: 1.0}}
	got := UpdateArenaRating(1500, 200, 0.06, games, 1.0)
	r, rd, vol := Rate(1500, 200, 0.06, games, DefaultTau)
	if got != (ArenaRating{R: r, RD: rd, Sigma: vol}) {
		t.Errorf("weight 1 gave %+v, want Rate's own %v/%v/%v -- the interpolation must be "+
			"exactly standard Glicko-2 at w = 1", got, r, rd, vol)
	}
}
