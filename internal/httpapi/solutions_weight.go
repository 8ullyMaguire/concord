package httpapi

import (
	"context"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/governance"
	"git.polarisocial.xyz/concord/concord/internal/ranking"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// solutionVoteWeight is §8.3's formula, and the reason the solution vote does
// not trust the request body.
//
//	weight = min(3, 1 + log10(1 + reputation))
//	        · role_multiplier · expertise_multiplier · recency_factor
//
// Three decisions are in here, and each of them is a reading of the spec rather
// than the spec's own text:
//
//  1. The cap applies to the REPUTATION TERM, not to the product. §8.3's `min(3,…)`
//     binds the first factor inside its own parentheses, and the factors multiply
//     outside. §6.4's "within the existing weight caps" is the counterweight, so the
//     product is bounded too -- but by the cap scaled by the factors that are
//     legitimately 1.0, which keeps the age gate and role multiplier authoritative
//     while stopping expertise from compounding on top of a maxed-out reputation.
//  2. Expertise uses the STRONGEST tag across both solutions, not the per-solution
//     average. A vote comparing a Rust design against a JS one is a vote about
//     Rust to the extent it is about Rust at all, and averaging the two would let
//     either design's author dilute the other by adding a tag.
//  3. The age gate multiplies last, after expertise. It is Sybil defence (§12.3),
//     and a defence that a 2.0x expertise multiplier can halve is not a defence.
func (s *Server) solutionVoteWeight(ctx context.Context, projectID, actorID int64, charter governance.Charter, a, b store.Solution) (float64, error) {
	// §8.3's reputation term, capped.
	reputation, err := s.Store.GetReputation(ctx, projectID, actorID)
	if err != nil {
		return 0, err
	}
	role, err := s.Store.GetRoleForProject(ctx, projectID, actorID)
	if err != nil {
		return 0, err
	}
	weight := ranking.VoteWeight(reputation, ranking.RoleMultiplier(role), charter.VoteWeightCap)

	// §6.4/§8.3's expertise multiplier, tag-scoped to the solutions being judged.
	perTag, err := s.Store.TagReputation(ctx, projectID, actorID)
	if err != nil {
		return 0, err
	}
	tags := append(ranking.ParseExpertiseTags(a.ExpertiseTags),
		ranking.ParseExpertiseTags(b.ExpertiseTags)...)
	tagRep, _ := ranking.BestTagReputation(perTag, tags)
	expertise := ranking.ExpertiseMultiplier(tagRep, len(tags))
	weight *= expertise

	// The age gate, last, for the reason in the comment above.
	createdAt, err := s.Store.GetUserCreatedAt(ctx, actorID)
	if err == nil {
		weight *= ranking.AgeGateMultiplier(ranking.AccountAgeDays(createdAt, float64(time.Now().Unix())))
	}

	// The product bound. Scaling the cap by the age gate keeps a new account's
	// weight at or below what the gate alone allows; scaling by role and expertise
	// would make this a no-op, which is why it is not done.
	bound := charter.VoteWeightCap
	if weight > bound {
		weight = bound
	}
	if weight <= 0 {
		weight = 0
	}
	return weight, nil
}
