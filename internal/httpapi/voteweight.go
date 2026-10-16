package httpapi

import (
	"context"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/ranking"
)

// applyAgeGate multiplies §12.3's account-age ramp into a vote weight, LAST.
//
// Last is load-bearing, and it is why this is a helper rather than a line: the ramp
// is Sybil defence (§12.3), and a defence that a multiplier preceding it can cancel is
// not a defence -- a 2.0x role or expertise multiplier applied afterwards would halve
// the ramp's effect and more.
//
// §30 added this because four routes applied the gate four slightly different ways and
// a fifth did not apply it at all: alternatives.go cast with the ungated weight, on the
// one arena kind the panel can judge. A rule that lives in five copies is a rule that
// one of the copies does not have.
//
// A failed read leaves the weight alone rather than zeroing it: the account exists (the
// caller is authenticated), so the gate is a ramp on a real account, not a refusal.
// The vote routes that care about the zero case get weight 0 from the ramp itself.
func (s *Server) applyAgeGate(ctx context.Context, actorID int64, weight *float64) {
	createdAt, err := s.Store.GetUserCreatedAt(ctx, actorID)
	if err != nil {
		return
	}
	*weight *= ranking.AgeGateMultiplier(
		ranking.AccountAgeDays(createdAt, float64(time.Now().Unix())))
}
