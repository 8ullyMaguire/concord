// Package governance holds Concord's collective-decision rules (spec §4–§6):
// role levels, charter defaults, quorum math, consensus evaluation, and the
// merge confirmation gate. Pure logic only — SQL and HTTP live elsewhere.
package governance

import "math"

type Role string

const (
	Guest       Role = "guest"
	User        Role = "user"
	Contributor Role = "contributor"
	Reviewer    Role = "reviewer"
	Maintainer  Role = "maintainer"
	Owner       Role = "owner"
)

var roleLevel = map[Role]int{
	Guest: 0, User: 1, Contributor: 2, Reviewer: 3, Maintainer: 4, Owner: 5,
}

// Level is the numeric rank of a role; higher outranks lower.
func (r Role) Level() int { return roleLevel[r] }

// AtLeast reports whether r carries at least min's rank.
func (r Role) AtLeast(min Role) bool { return r.Level() >= min.Level() }

// GovernanceModel is the project's decision regime.
type GovernanceModel string

const (
	// Collective (default): collaborators decide direction; no role can
	// steer alone (spec §4 "Governance models").
	Collective GovernanceModel = "collective"
	// MaintainerLed (opt-in): maintainers may accept features and bypass
	// the merge quorum; every bypass is logged.
	MaintainerLed GovernanceModel = "maintainer_led"
)

func (m GovernanceModel) Valid() bool {
	return m == Collective || m == MaintainerLed
}

// Charter holds the project's decision thresholds (spec §5.5, §6.3, §8).
type Charter struct {
	QuorumRatio              float64 // share of eligible collaborators
	QuorumMin                int     // ... but never fewer than this many
	ConsentRatio             float64 // consent / (consent + stand_aside + block)
	OverrideRatio            float64 // supermajority overriding a block
	VoteWindowDays           float64
	MergeRequiresQuorum      bool
	MergeQuorumMin           int
	MergeQuorumRatio         float64
	RequireReviewerApproval  bool
	WIPInProgress            int
	WIPReview                int
	Lam, Mu                  float64 // priority score weights (spec §6.2)
	PainHalflifeDays         float64
	RepHalflifeDays          float64
	GlickoTau                float64
	VoteWeightCap            float64
}

// DefaultCharter returns the spec defaults, adjusted per governance model:
// maintainer-led projects may merge without quorum (they can re-enable it).
func DefaultCharter(m GovernanceModel) Charter {
	c := Charter{
		QuorumRatio: 0.2, QuorumMin: 3,
		ConsentRatio: 0.7, OverrideRatio: 0.8,
		VoteWindowDays: 7,
		MergeRequiresQuorum: true, MergeQuorumMin: 2, MergeQuorumRatio: 0.25,
		RequireReviewerApproval: true,
		WIPInProgress:           3, WIPReview: 4,
		Lam: 20.0, Mu: 100.0,
		PainHalflifeDays: 90, RepHalflifeDays: 180,
		GlickoTau: 0.5, VoteWeightCap: 3.0,
	}
	if m == MaintainerLed {
		c.MergeRequiresQuorum = false
	}
	return c
}

// QuorumThreshold: max(quorum_min, ceil(ratio·eligible)), capped by the
// eligible count; 0 when nobody is eligible (spec §5.5/§6.3).
func QuorumThreshold(eligible int, c Charter) int {
	if eligible <= 0 {
		return 0
	}
	need := max(c.QuorumMin, int(math.Ceil(c.QuorumRatio*float64(eligible))))
	return min(eligible, need)
}

// MergeQuorumThreshold mirrors QuorumThreshold with merge settings.
func MergeQuorumThreshold(eligible int, c Charter) int {
	if eligible <= 0 {
		return 0
	}
	need := max(c.MergeQuorumMin, int(math.Ceil(c.MergeQuorumRatio*float64(eligible))))
	return min(eligible, need)
}

// Position is a consensus-call stance (spec §5.5).
type Position string

const (
	Consent    Position = "consent"
	Abstain    Position = "abstain"
	StandAside Position = "stand_aside"
	Block      Position = "block"
)

// ConsensusCounts summarizes a call's cast positions.
type ConsensusCounts struct {
	Consent, Abstain, StandAside, Block int
	Participants                        int // distinct position rows
	Eligible                            int // contributor+ members
	OpenObjections                      int
}

// NonAbstain returns consent + stand_aside + block.
func (cc ConsensusCounts) NonAbstain() int {
	return cc.Consent + cc.StandAside + cc.Block
}

// Result is the outcome of a consensus call.
type Result string

const (
	ResultAccepted            Result = "accepted"
	ResultAcceptedOverridden  Result = "accepted_overridden"
	ResultRejected            Result = "rejected"
	ResultBlocked             Result = "blocked"
	ResultInsufficientQuorum  Result = "insufficient_quorum"
)

// EvaluateConsensus applies the spec's decision rule WITHOUT side effects:
//
//   - quorum counts every cast position (abstentions show up, silence does
//     not decide — an unmet quorum extends the window);
//   - the consent ratio is computed over non-abstain votes;
//   - any open block forces either an ≥override-ratio supermajority
//     (accepted_overridden) or a blocked result.
func EvaluateConsensus(cc ConsensusCounts, c Charter) Result {
	if cc.Participants < QuorumThreshold(cc.Eligible, c) {
		return ResultInsufficientQuorum
	}
	nonAbstain := cc.NonAbstain()
	ratio := 0.0
	if nonAbstain > 0 {
		ratio = float64(cc.Consent) / float64(nonAbstain)
	}
	if cc.OpenObjections > 0 {
		if ratio >= c.OverrideRatio {
			return ResultAcceptedOverridden
		}
		return ResultBlocked
	}
	if ratio >= c.ConsentRatio {
		return ResultAccepted
	}
	return ResultRejected
}

// MergeGate is the merge-confirmation verdict (spec §5.7).
type MergeGate struct {
	Approvals            int
	QuorumNeeded         int
	QuorumOK             bool
	ReviewerOK           bool
	MergeRequiresQuorum  bool
}

// Passed reports whether the merge may proceed.
func (g MergeGate) Passed() bool { return g.QuorumOK && g.ReviewerOK }

// CheckMergeGate applies the merge confirmation rule: distinct collaborator
// confirmations ≥ MergeQuorumThreshold (unless the charter disabled the
// quorum) plus, when required, at least one reviewer+ technical approval.
func CheckMergeGate(approvals, eligible int, reviewerApproval bool, c Charter) MergeGate {
	needsQuorum := c.MergeRequiresQuorum
	needed := 0
	if needsQuorum {
		needed = MergeQuorumThreshold(eligible, c)
	}
	return MergeGate{
		Approvals:           approvals,
		QuorumNeeded:        needed,
		QuorumOK:            !needsQuorum || approvals >= needed,
		ReviewerOK:          !c.RequireReviewerApproval || reviewerApproval,
		MergeRequiresQuorum: needsQuorum,
	}
}

// BoardPhases are the fixed kanban phases in advance order (spec §5.6).
var BoardPhases = []string{
	"inbox", "triaged", "solution_draft", "consensus", "ready",
	"in_progress", "review", "done", "rejected",
}

// WIPFor returns the default WIP limit for a phase (0 = unlimited).
func (c Charter) WIPFor(phase string) int {
	switch phase {
	case "in_progress":
		return c.WIPInProgress
	case "review":
		return c.WIPReview
	default:
		return 0
	}
}
