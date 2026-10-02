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
	QuorumRatio float64 // share of eligible collaborators
	QuorumMin   int     // ... but never fewer than this many
	// Two thresholds that answer different questions (spec §6.6).
	// SupportRatioMin is checked against SupportRatio -- consent over all
	// substantive positions, so a room full of reservations can carry a
	// proposal off a handful of supporters.
	// ConsentRatio is the decisive bar -- consent over the positions that
	// took a side, where a stand-aside is neutral rather than opposed.
	SupportRatioMin         float64
	ConsentRatio            float64 // decisive: consent / (consent + block)
	OverrideRatio           float64 // supermajority overriding a block
	VoteWindowDays          float64
	MergeRequiresQuorum     bool
	MergeQuorumMin          int
	MergeQuorumRatio        float64
	RequireReviewerApproval bool
	WIPInProgress           int
	WIPReview               int
	Lam, Mu                 float64 // priority score weights (spec §6.2)
	PainHalflifeDays        float64
	RepHalflifeDays         float64
	GlickoTau               float64
	VoteWeightCap           float64
}

// DefaultCharter returns the spec defaults, adjusted per governance model:
// maintainer-led projects may merge without quorum (they can re-enable it).
func DefaultCharter(m GovernanceModel) Charter {
	c := Charter{
		QuorumRatio: 0.2, QuorumMin: 3,
		// §6.6: support >= 0.50 and decisive >= 0.70. The support floor
		// is what a consent ratio alone could not express -- a call can
		// clear 70% of the decisive positions while being carried by a
		// handful of people against a room full of reservations.
		ConsentRatio: 0.7, SupportRatioMin: 0.5, OverrideRatio: 0.8,
		VoteWindowDays:      7,
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

// Decisive returns the denominator of the decisive ratio: consent + block.
//
// A stand-aside is a reservation, not opposition -- §6.6 defines it as "I have
// concerns, but I will not block". Counting it in the denominator of the
// ratio that decides the outcome made a stand-aside vote *against* the
// proposal, which is the opposite of what the position means, and it was a real
// bug: a call with 4 consent, 3 stand-aside and 0 block scored 4/7 = 0.57 and
// failed a 0.7 threshold, so the people who withheld consent because they had
// reservations were the ones who sank it.
//
// Abstain is already excluded (it is neutral in both measures) and stand-aside
// is now too. NonAbstain is kept because it is still the right denominator for
// participation reporting, which counts reservations as participation.
func (cc ConsensusCounts) Decisive() int {
	return cc.Consent + cc.Block
}

// SupportRatio is consent / non-abstain: the share of substantive positions
// that support the proposal. A stand-aside dilutes this, which is meaningful
// -- it is the "how many people went along with reservations" number.
func (cc ConsensusCounts) SupportRatio() float64 {
	if n := cc.NonAbstain(); n > 0 {
		return float64(cc.Consent) / float64(n)
	}
	return 0
}

// DecisiveRatio is consent / (consent + block): the share of positions that took
// a side against each other. Stand-asides and abstentions are excluded, so a
// proposal passes this on the strength of the people who actually decided it,
// not on the size of the room.
//
// The two ratios differ, and reporting only one of them hides either the
// reservations or the opposition. §6.6 requires both: support >= 0.50 and
// decisive >= 0.70.
func (cc ConsensusCounts) DecisiveRatio() float64 {
	if n := cc.Decisive(); n > 0 {
		return float64(cc.Consent) / float64(n)
	}
	return 0
}

// Reluctant reports whether the call carries more reservations than support.
// Such a decision passes but is flagged for review, because a room that mostly
// stands aside has not actually agreed.
func (cc ConsensusCounts) Reluctant() bool {
	return cc.StandAside > cc.Consent
}

// Result is the outcome of a consensus call.
type Result string

const (
	ResultAccepted           Result = "accepted"
	ResultAcceptedOverridden Result = "accepted_overridden"
	ResultRejected           Result = "rejected"
	ResultBlocked            Result = "blocked"
	ResultInsufficientQuorum Result = "insufficient_quorum"
)

// EvaluateConsensus applies the spec's decision rule WITHOUT side effects.
//
// Two separate thresholds, because they answer different questions (§6.6):
//
//	support  = consent / (consent + stand_aside + block) >= SupportRatioMin
//	decisive = consent / (consent + block)                    >= ConsentRatio
//
// Stand-asides are neutral in the decisive measure. They were not neutral
// before, which meant a stand-aside counted as a vote against the proposal --
// see ConsensusCounts.Decisive.
//
// Any open block forces either an >=override-ratio supermajority
// (accepted_overridden) or a blocked result.
func EvaluateConsensus(cc ConsensusCounts, c Charter) Result {
	if cc.Participants < QuorumThreshold(cc.Eligible, c) {
		return ResultInsufficientQuorum
	}
	support := cc.SupportRatio()
	decisive := cc.DecisiveRatio()

	if cc.OpenObjections > 0 {
		// The override supermajority is measured the same way as a passing
		// call: over the people who took a side. Including stand-asides here
		// would mean a reservation could block an override it never opposed.
		if decisive >= c.OverrideRatio {
			return ResultAcceptedOverridden
		}
		return ResultBlocked
	}

	if support < c.SupportRatioMin {
		return ResultRejected
	}
	if decisive >= c.ConsentRatio {
		return ResultAccepted
	}
	return ResultRejected
}

// MergeGate is the merge-confirmation verdict (spec §5.7).
type MergeGate struct {
	Approvals           int
	QuorumNeeded        int
	QuorumOK            bool
	ReviewerOK          bool
	MergeRequiresQuorum bool
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

// MaxWindowExtensions bounds how far an unmet quorum may push a call's
// deadline (spec §5.5: "silence cannot decide").
//
// Silence not deciding is a rule about *evaluation*, not about eternity. With
// no cap, a call nobody joins stays open forever and accumulates a board of
// zombies that makes the whole process look broken. With a cap, a call that
// still has not reached quorum when the window runs out expires, and expiry is
// deliberately distinct from rejection: nobody voted against it, it simply
// ran out of attention. Reusing "rejected" would put a project's failure to
// find participants in the same bucket as a proposal that was argued down.
const MaxWindowExtensions = 2

// Deadline is a consensus call's open/close bookkeeping.
type Deadline struct {
	OpensAt    float64
	ClosesAt   float64
	Extensions int // window extensions already applied
}

// Expired reports whether the call is past its final deadline and therefore can
// no longer be extended.
//
// A call that is inside its window, or that has already been extended the
// maximum number of times but still has time left, returns false — running out
// of extensions freezes the deadline rather than expiring the call on the spot.
// The two are different: a frozen call can still be closed by an explicit
// decision, whereas an expired one is over.
func (d Deadline) Expired(now float64) bool {
	if now < d.ClosesAt {
		return false
	}
	return d.Extensions >= MaxWindowExtensions
}

// Extendable reports whether an unmet-quorum call may still push its deadline.
func (d Deadline) Extendable() bool { return d.Extensions < MaxWindowExtensions }

// Extend returns a new deadline one window-length later, or the deadline
// unchanged and false if no extension remains. The window length is the
// original opens_at→closes_at span, so extending does not silently shorten the
// window on a call that was created with a custom duration.
func (d Deadline) Extend() (Deadline, bool) {
	if !d.Extendable() {
		return d, false
	}
	span := d.ClosesAt - d.OpensAt
	if span <= 0 {
		span = 24 * 3600 // a degenerate window still gets one day, not zero
	}
	return Deadline{OpensAt: d.OpensAt, ClosesAt: d.ClosesAt + span, Extensions: d.Extensions + 1}, true
}
