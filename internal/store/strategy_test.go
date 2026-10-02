package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/governance"
)

// §5.1: "no voting on your own items". Nothing enforced this — an author could
// vote in their own feature's arena and move its rating directly, and in a
// proposal flow the proposer could consent to their own proposal, which in a
// one-collaborator project (where the quorum floor caps down to the eligible
// count) means quorum is met by the proposer alone.

func voteFixture(t *testing.T) (*DB, int64, int64, int64, int64, int64) {
	t.Helper()
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	comp, err := store.CreateComplaint(ctx, pid, uid, "Self-vote complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	a, err := store.CreateFeature(ctx, pid, uid, "Self-vote A", "body", "", nil, nil, []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature A: %v", err)
	}
	b, err := store.CreateFeature(ctx, pid, uid, "Self-vote B", "body", "", nil, nil, []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature B: %v", err)
	}
	return store, pid, uid, a.ID, b.ID, comp.ID
}

func TestRecordVoteRefusesAuthorOfEitherFeature(t *testing.T) {
	store, pid, uid, aID, bID, _ := voteFixture(t)
	charter := governance.DefaultCharter(governance.Collective)
	ctx := context.Background()

	// Author of both: refused.
	if _, err := store.RecordVote(ctx, pid, uid, aID, bID, "a", 1.0, charter); err == nil {
		t.Error("author voting on their own features: got nil, want rejection")
	}
	// A different user is fine.
	voter := secondVoter(t, store)
	if _, err := store.RecordVote(ctx, pid, voter, aID, bID, "a", 1.0, charter); err != nil {
		t.Errorf("non-author vote refused: %v", err)
	}
}

func TestRecordVoteAuthorOfOnlyOneIsStillRefused(t *testing.T) {
	// Voting for the other person's feature while being the author of one of
	// them is still a partial self-vote: the rating that moves includes the
	// voter's own submission.
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	comp, err := store.CreateComplaint(ctx, pid, uid, "Mixed complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	mine, err := store.CreateFeature(ctx, pid, uid, "Mine", "body", "", nil, nil, []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature mine: %v", err)
	}
	other := secondVoter(t, store)
	theirs, err := store.CreateFeature(ctx, pid, other, "Theirs", "body", "", nil, nil, []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature theirs: %v", err)
	}
	charter := governance.DefaultCharter(governance.Collective)
	if _, err := store.RecordVote(ctx, pid, uid, mine.ID, theirs.ID, "a", 1.0, charter); err == nil {
		t.Error("author of one of the pair: got nil, want rejection")
	}
}

func TestProposeStrategicWeightRejectsOutOfRange(t *testing.T) {
	store, pid, uid, aID, _, _ := voteFixture(t)
	ctx := context.Background()

	// A weight large enough to dominate the priority formula reintroduces the
	// hand-steering the proposal path exists to remove.
	for _, bad := range []float64{5.0, -1.0, 100.0} {
		if _, err := store.ProposeStrategicWeight(ctx, pid, aID, uid, bad, "because"); err == nil {
			t.Errorf("weight %g: got nil, want rejection", bad)
		}
	}
}

func TestProposeStrategicWeightRequiresRationale(t *testing.T) {
	store, pid, uid, aID, _, _ := voteFixture(t)
	ctx := context.Background()
	if _, err := store.ProposeStrategicWeight(ctx, pid, aID, uid, 2.0, "   "); err == nil {
		t.Error("blank rationale: got nil, want rejection")
	}
	p, err := store.ProposeStrategicWeight(ctx, pid, aID, uid, 2.0, "security hardening")
	if err != nil {
		t.Fatalf("ProposeStrategicWeight: %v", err)
	}
	if p.Status != "pending" {
		t.Errorf("status = %q, want pending", p.Status)
	}
	if p.WeightBefore != 1.0 {
		t.Errorf("weight_before = %v, want 1.0", p.WeightBefore)
	}
	// A second pending proposal for the same feature is refused.
	if _, err := store.ProposeStrategicWeight(ctx, pid, aID, uid, 2.5, "competing idea"); err == nil {
		t.Error("competing pending proposal: got nil, want rejection")
	}
}

func TestProposerCannotConsentToOwnProposal(t *testing.T) {
	store, pid, uid, aID, _, _ := voteFixture(t)
	ctx := context.Background()
	p, err := store.ProposeStrategicWeight(ctx, pid, aID, uid, 2.0, "security hardening")
	if err != nil {
		t.Fatalf("ProposeStrategicWeight: %v", err)
	}
	if _, err := store.RatifyStrategicWeightProposal(ctx, p.ID, uid); err == nil {
		t.Error("proposer consenting to own proposal: got nil, want rejection")
	}
	// The weight must be untouched.
	f, err := store.GetFeature(ctx, aID)
	if err != nil {
		t.Fatalf("GetFeature: %v", err)
	}
	if f.StrategicWeight != 1.0 {
		t.Errorf("strategic_weight = %v after self-consent, want 1.0", f.StrategicWeight)
	}
}

// TestRatifyRefusesAnIneligibleConsenter is the guard that stops a newly-arrived
// account from supplying a deciding consent.
//
// It was added alongside the §8.2 activity window and had no test of its own --
// removing it left the suite green, which is the whole reason this exists. The
// failure it prevents is specific: QuorumThreshold reports 0 when the eligible
// count is 0, so a project whose members have contributed nothing has a quorum
// of zero and any single consent ratifies.
func TestRatifyRefusesAnIneligibleConsenter(t *testing.T) {
	store, pid, uid, aID, _, _ := voteFixture(t)
	ctx := context.Background()
	p, err := store.ProposeStrategicWeight(ctx, pid, aID, uid, 2.0, "security hardening")
	if err != nil {
		t.Fatalf("ProposeStrategicWeight: %v", err)
	}

	// A registered account that joined the project and did nothing else. It holds
	// the contributor role, so the old role-only check passed it.
	arrived := newUserNamed(t, store, "arrivedonly")
	if err := store.JoinProject(ctx, pid, arrived); err != nil {
		t.Fatalf("JoinProject: %v", err)
	}
	role, err := store.GetRoleForProject(ctx, pid, arrived)
	if err != nil {
		t.Fatalf("GetRoleForProject: %v", err)
	}
	if role == "guest" {
		t.Fatalf("the fixture did not grant a contributor role (%q); the test would not "+
			"isolate the activity requirement", role)
	}
	eligible, err := store.EligibleCollaboratorCount(ctx, pid)
	if err != nil {
		t.Fatalf("EligibleCollaboratorCount: %v", err)
	}
	if eligible != 0 {
		t.Skipf("the project already has %d eligible collaborators, so this test cannot "+
			"demonstrate the zero-population case", eligible)
	}

	if _, err := store.RatifyStrategicWeightProposal(ctx, p.ID, arrived); err == nil {
		t.Fatal("an account that only arrived consented to a proposal")
	}

	// And the proposal is untouched: not ratified, and the weight unmoved.
	got, err := store.GetStrategicWeightProposal(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetStrategicWeightProposal: %v", err)
	}
	if got.Status != "pending" {
		t.Errorf("proposal status %q after a refused consent, want pending", got.Status)
	}
	f, err := store.GetFeature(ctx, aID)
	if err != nil {
		t.Fatalf("GetFeature: %v", err)
	}
	if f.StrategicWeight != 1.0 {
		t.Errorf("strategic_weight = %v after a refused consent, want 1.0", f.StrategicWeight)
	}
}

func TestRatifyAppliesWeightAfterQuorum(t *testing.T) {
	store, pid, uid, aID, _, _ := voteFixture(t)
	ctx := context.Background()
	p, err := store.ProposeStrategicWeight(ctx, pid, aID, uid, 2.0, "security hardening")
	if err != nil {
		t.Fatalf("ProposeStrategicWeight: %v", err)
	}

	// Add enough collaborators that quorum is more than one, then consent until
	// it passes. Quorum is min(quorum_min, eligible) so the number needed moves
	// with the population.
	//
	// Each voter has to be an ELIGIBLE collaborator to consent at all (§8.2 plus
	// the guard in RatifyStrategicWeightProposal), and eligibility needs a
	// qualifying contribution in the trailing window -- not merely a role. A
	// merged merge request is the cheapest route: one row, no status ceremony.
	//
	// Before that guard existed, five brand-new accounts could consent and the
	// quorum floor was computed over an eligible count of 0, which QuorumThreshold
	// reports as 0 -- so the very first consent ratified the proposal.
	consents := make([]int64, 0, 6)
	for i := 0; i < 5; i++ {
		voter := newUserNamed(t, store, fmt.Sprintf("voter%d", i))
		if err := store.JoinProject(ctx, pid, voter); err != nil {
			t.Fatalf("JoinProject: %v", err)
		}
		mr, err := store.CreateMergeRequest(ctx, pid, aID, voter, "the work that counts")
		if err != nil {
			t.Fatalf("CreateMergeRequest: %v", err)
		}
		if _, err := store.ExecContext(ctx,
			`UPDATE merge_requests SET status = 'merged', closed_at = ? WHERE id = ?`,
			float64(time.Now().Unix()), mr.ID); err != nil {
			t.Fatalf("merge: %v", err)
		}
		consents = append(consents, voter)
	}

	ratified := false
	for _, voter := range consents {
		got, err := store.RatifyStrategicWeightProposal(ctx, p.ID, voter)
		switch {
		case err == nil:
			ratified = true
			if got.Status != "ratified" {
				t.Errorf("status = %q, want ratified", got.Status)
			}
		case err == ErrProposalStale:
			t.Fatalf("proposal went stale unexpectedly: %v", err)
		}
		if ratified {
			break
		}
	}
	if !ratified {
		t.Fatal("proposal never ratified despite five distinct consents")
	}

	f, err := store.GetFeature(ctx, aID)
	if err != nil {
		t.Fatalf("GetFeature: %v", err)
	}
	if f.StrategicWeight != 2.0 {
		t.Errorf("strategic_weight = %v after ratification, want 2.0", f.StrategicWeight)
	}
}

func TestStandAsideIsNeutralInBothRatios(t *testing.T) {
	c := governance.ConsensusCounts{
		Consent: 6, StandAside: 4, Participants: 10, Eligible: 20,
	}
	if got := c.Decisive(); got != 6 {
		t.Errorf("Decisive() = %d, want 6 (stand-asides are not opposition)", got)
	}
	if r := c.DecisiveRatio(); r != 1.0 {
		t.Errorf("DecisiveRatio() = %v, want 1.0", r)
	}
	if r := c.SupportRatio(); r < 0.59 || r > 0.61 {
		t.Errorf("SupportRatio() = %v, want ~0.6", r)
	}
	// Reluctant is stand-asides OUTNUMBERING consents. 4 < 6 here, so this room
	// is not reluctant.
	if c.Reluctant() {
		t.Error("Reluctant() = true with 4 stand-asides vs 6 consents, want false")
	}
	// Flip the counts and it should report reluctant.
	reluctant := governance.ConsensusCounts{Consent: 3, StandAside: 5, Participants: 8, Eligible: 20}
	if !reluctant.Reluctant() {
		t.Error("Reluctant() = false with 5 stand-asides vs 3 consents, want true")
	}
}
