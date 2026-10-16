package ranking

import "math"

// Arena rating vocabulary (spec revision 4 §5).
//
// Rate() is the Glicko-2 period and was already entity-agnostic -- it takes
// (r, rd, sigma) and games against opponents described the same way. What it
// lacked was the *named* vocabulary the arena tables store, plus the one
// derived figure the spec displays. These are the only additions §5 needs, and
// they are pure functions so the store stays SQL-only.

// Glicko-2 defaults (§5.1: "rating r (default 1500), deviation RD (350), and
// volatility sigma").
const (
	DefaultRating     = 1500.0
	DefaultDeviation  = 350.0
	DefaultVolatility = 0.06
	// DefaultTau is Glickman's system constant, held fixed.
	DefaultTau = 0.5
)

// ArenaRating is the triple every arena entry stores.
type ArenaRating struct {
	R     float64 `json:"r"`
	RD    float64 `json:"rd"`
	Sigma float64 `json:"sigma"`
}

// Conservative is §5.1's displayed score, r - 2*RD.
//
// Not a display detail: it is what the leaderboard orders on, so an entry with
// no games (RD 350) cannot outrank a well-attested one on the strength of not
// having lost yet. Ranking on r alone makes a brand-new entry look like a winner
// until its first loss arrives.
func Conservative(r, rd float64) float64 { return r - 2*rd }

// WinProbability is §5.1's expected score, E(r1, r2, RD1, RD2), shown to users
// as "A is better with 87% confidence".
//
// Returns the probability that the FIRST entry beats the second, so a caller
// comparing an entry to the leader gets its chance of winning directly rather
// than having to know the argument order.
func WinProbability(r1, rd1, r2, rd2 float64) float64 {
	// Glickman eq. 2.1: E uses the *rating* player's own deviation, not the
	// opponent's. rd2 is in the signature because a caller always has it and
	// dropping it would make the arguments asymmetric -- but it does not enter the
	// formula, and pretending otherwise would make a high-RD leader look harder
	// to beat than it is.
	mu1 := (r1 - 1500.0) / scale
	mu2 := (r2 - 1500.0) / scale
	return 1.0 / (1.0 + math.Exp(-g(rd1/scale)*(mu1-mu2)))
}

// BeatsBy is WinProbability expressed as the margin the spec displays.
//
// 0.87 becomes 87. Kept as a percentage rather than a fraction so a UI does not
// have to remember which, and clamped to [0, 100] because a probability that
// formats as 101% is a bug report waiting to happen.
func BeatsBy(r1, rd1, r2, rd2 float64) float64 {
	p := WinProbability(r1, rd1, r2, rd2) * 100
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

// UpdateArenaRating applies one weighted comparison and returns the new triple.
//
// §5.1: "weight scales the update's influence". This function used to say
// `if weight <= 0 { weight = 1 }`, which is that sentence upside down -- zero is
// exactly what AgeGateMultiplier returns for an account minutes old, so §12.3's
// Sybil defence was handing the fullest possible update to the accounts it exists
// to stop.
//
// The rule, and it is the only implementation of it in the arena path:
//
//	w <= 0     nothing changes: rating, RD and volatility alike. A mass of
//	           throwaway accounts that never moves a rating can still collapse the
//	           uncertainty, and priority is scored on r - 2*RD, so collapsing RD is
//	           itself a way to move a competitor up the board. ApplyPairwiseVote
//	           (score.go) says this in the same words; this is the generic-arena
//	           half of one rule.
//	0 < w < 1  one game, and the WHOLE update is scaled by w: rating, RD and
//	           volatility alike. §4's comment on ApplyPairwiseVote says "the
//	           RD/volatility update is always standard"; that sentence is
//	           superseded, and the measurement that superseded it is that a
//	           brand-new account's vote (weight ~3e-6, an invisible rating move of
//	           5e-4) still collapsed RD from 350 to 290 -- and priority is scored on
//	           r - 2*RD, so the conservative score jumped 119 points on a vote that
//	           was supposed to carry none of the account's influence. §12.3's
//	           defence has to hold in the metric that ranks, not only in the
//	           rating. Continuous at w = 1.
//	w >= 1     the caller's repeated games, unchanged: §5.1 says a weight-2 voter
//	           "is recorded as two games".
func UpdateArenaRating(r, rd, sigma float64, games []Game, weight float64) ArenaRating {
	if weight <= 0 {
		return ArenaRating{R: r, RD: rd, Sigma: sigma}
	}
	nr, nrd, ns := Rate(r, rd, sigma, games, DefaultTau)
	if weight < 1 {
		nr = r + weight*(nr-r)
		nrd = rd + weight*(nrd-rd)
		ns = sigma + weight*(ns-sigma)
	}
	return ArenaRating{R: nr, RD: nrd, Sigma: ns}
}
