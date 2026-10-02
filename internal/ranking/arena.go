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

// UpdateArenaRating runs one period and returns the new triple.
//
// A thin wrapper over Rate so callers name the concept they mean. weight scales
// the influence of the period (§5.1's fractional-game treatment): the caller
// passes the same games repeated, and weight is used for the audit and for
// arena_games accounting. It is NOT applied to the score, because scaling a
// 0-1 game score by a weight and feeding that to Rate would be a different
// (and wrong) model -- the weight is expressed as repetition, not magnitude.
func UpdateArenaRating(r, rd, sigma float64, games []Game, weight float64) ArenaRating {
	if weight <= 0 {
		weight = 1
	}
	nr, nrd, ns := Rate(r, rd, sigma, games, DefaultTau)
	return ArenaRating{R: nr, RD: nrd, Sigma: ns}
}
