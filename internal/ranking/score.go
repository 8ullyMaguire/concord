package ranking

import "math"

// Feature carries the rating state used by pairwise priority voting.
type Feature struct {
	R, RD, Vol      float64
	StrategicWeight float64
}

// Outcome of one pairwise comparison (spec §5.4).
type Outcome string

const (
	OutcomeA       Outcome = "a"
	OutcomeB       Outcome = "b"
	OutcomeBoth    Outcome = "both" // draw
	OutcomeNeither Outcome = "neither"
	OutcomeSkip    Outcome = "skip"
)

// AppliesRatingChange reports whether the outcome moves ratings.
func (o Outcome) AppliesRatingChange() bool {
	return o == OutcomeA || o == OutcomeB || o == OutcomeBoth
}

// ScorePair returns the standard Glicko-2 scores (sA, sB) for the outcome.
func (o Outcome) ScorePair() (float64, float64) {
	switch o {
	case OutcomeA:
		return 1.0, 0.0
	case OutcomeB:
		return 0.0, 1.0
	case OutcomeBoth:
		return 0.5, 0.5
	}
	return 0, 0
}

// ApplyPairwiseVote updates both features for one pairwise vote.
//
// Concord's voter-weight model (spec §4): the *rating change* is scaled by
// the voter's weight — r” = r + w·(r' − r), which is standard Glicko-2 at
// w = 1. The RD/volatility update is always standard. Outcomes that do not
// apply a rating change ("neither", "skip") leave both features untouched
// but remain recorded for audit.
//
// A zero weight is a complete no-op on the ratings, RD and volatility alike,
// and the vote is still recorded. That case is the account-age gate
// (AgeGateMultiplier) refusing a brand-new account's influence, and it needs
// the whole update skipped rather than just the rating term: leaving RD and
// volatility at their standard update would still let a mass of throwaway
// accounts shrink the uncertainty on chosen features, and priority is scored
// on the lower confidence bound (r − 2·RD), so collapsing RD is itself a way
// to move a feature up the board without ever shifting its rating.
func ApplyPairwiseVote(a, b Feature, out Outcome, weight, tau float64) (Feature, Feature) {
	if !out.AppliesRatingChange() || weight <= 0 {
		return a, b
	}
	sA, sB := out.ScorePair()
	ra, rdA, volA := Rate(a.R, a.RD, a.Vol, []Game{{b.R, b.RD, sA}}, tau)
	rb, rdB, volB := Rate(b.R, b.RD, b.Vol, []Game{{a.R, a.RD, sB}}, tau)

	a2 := a
	a2.R = a.R + weight*(ra-a.R)
	a2.RD = rdA
	a2.Vol = volA

	b2 := b
	b2.R = b.R + weight*(rb-b.R)
	b2.RD = rdB
	b2.Vol = volB

	return a2, b2
}

// RoleMultiplier maps a member role to its influence multiplier (spec §4).
// Guest has no vote. Stake and recency multipliers from the spec are 1.0
// in this phase; the formula shape is kept so they can be plugged in later.
func RoleMultiplier(role string) float64 {
	switch role {
	case "owner":
		return 2.0
	case "maintainer":
		return 1.75
	case "reviewer":
		return 1.5
	case "contributor":
		return 1.25
	default: // user (and guest, which is gated before weight matters)
		return 1.0
	}
}

// VoteWeight = min(cap, (1 + log10(1 + reputation)) * roleMultiplier).
func VoteWeight(reputation, roleMult, cap float64) float64 {
	raw := (1.0 + math.Log10(1.0+reputation)) * roleMult
	return math.Min(cap, raw)
}

// AccountAgeGateDays is the window over which a new account ramps from zero to
// full vote weight (Sybil defence, spec §12.3). Seven days spans a weekend and
// keeps a genuine new contributor from being meaningfully muted.
const AccountAgeGateDays = 7.0

// AgeGateMultiplier ramps a voter's weight from 0 to 1 across their first
// AccountAgeGateDays on the instance.
//
// The shape is a ramp rather than a cliff on purpose. A cliff makes the day the
// gate opens an event worth waiting for, and a two-contributor project whose
// members registered on the same day then has no way to reach quorum at all —
// the gate would deadlock exactly the small projects consensus is supposed to
// serve. A ramp lets a new account take part immediately at reduced weight: it
// cannot buy influence with a throwaway account, and it is not locked out of
// the conversation it came to join.
//
// A negative age (clock skew, or a created_at ahead of now) reads as 0 days
// rather than as an error — a voter must not be penalised because our clock is
// wrong. Accounts older than the window are unaffected.
func AgeGateMultiplier(accountAgeDays float64) float64 {
	if accountAgeDays < 0 {
		accountAgeDays = 0
	}
	return math.Min(1.0, accountAgeDays/AccountAgeGateDays)
}

// AccountAgeDays converts a stored created_at (unix seconds) and the current
// time into an account age in days.
func AccountAgeDays(createdAt, now float64) float64 {
	return (now - createdAt) / 86400.0
}

// Reputation decays reputation events with an exponential half-life:
// Σ points · 0.5^(age/halflife), floored at 0. Points is the raw sum and
// ageDays the event age; callers sum over events.
func DecayPoints(points, ageDays, halflifeDays float64) float64 {
	if halflifeDays <= 0 {
		return points
	}
	return points * math.Pow(0.5, ageDays/halflifeDays)
}

// PainScore implements spec §6.1:
// severity · frequency · ln(1 + affected) · strategicMult, exponentially
// decayed from creation. affected is already reputation-weighted upstream
// (see store: 1 for the reporter + Σ min(2, 1+log10(1+rep)) per impact).
func PainScore(severity int, frequency, strategicMult, affected, ageDays, halflifeDays float64) float64 {
	base := float64(severity) * frequency * math.Log1p(affected) * strategicMult
	return DecayPoints(base, ageDays, halflifeDays)
}

// PriorityScore implements spec §6.2:
// (r − 2·RD) + λ·ln(1 + painSum) + μ·strategicWeight.
// The lower confidence bound keeps new, uncertain features from dominating.
func PriorityScore(r, rd, painSum, strategicWeight, lam, mu float64) float64 {
	return (r - 2.0*rd) + lam*math.Log1p(painSum) + mu*strategicWeight
}
