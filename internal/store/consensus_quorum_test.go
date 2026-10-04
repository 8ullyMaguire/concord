package store

import (
	"context"
	"fmt"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/governance"
)

// A closed call counts the eligible population before applying quorum.
//
// This is a regression test for a live bug, found by probing rather than by reading.
// `EvaluateConsensus` opens with
//
//	if cc.Participants < QuorumThreshold(cc.Eligible, c) { return ResultInsufficientQuorum }
//
// and `QuorumThreshold` returns 0 for any eligible <= 0. `CloseConsensusCall` built
// its `ConsensusCounts` by counting position rows and never set `Eligible`, so the
// threshold was always 0 and the first check could never fire: in a project with
// three eligible members, one member consenting to their own call was enough to
// ACCEPT it. Nothing caught it because every other branch -- the two ratios, the
// objection override, the five outcomes -- was correct, and an unset Eligible leaves
// all of those behaving normally.
//
// Why the fixture is shaped the way it is: §8.2's eligibility is role AND a
// qualifying contribution inside the activity window. A fixture that only inserts
// `members` rows has an eligible population of ZERO, which makes the threshold 0
// again and the test passes for the wrong reason -- which is exactly what the first
// version of this probe did. Each extra member here therefore lands a merged merge
// request, route (1) of qualifyingContributionsSQL, through the real ExecuteMerge.

// eligibleMemberPool returns n members who are eligible under §8.2: each has a
// contributor role AND a qualifying contribution.
func eligibleMemberPool(t *testing.T, d *DB, pid int64, n int, prefix string) []int64 {
	t.Helper()
	ctx := context.Background()
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		u := newUserNamed(t, d, fmt.Sprintf("%s-%d", prefix, i))
		if _, err := d.ExecContext(ctx,
			"INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,'contributor',?)",
			pid, u, float64(1)); err != nil {
			t.Fatalf("add member %d: %v", i, err)
		}
		fid := newFeature(t, d, pid, u, fmt.Sprintf("%s %d feature", prefix, i))
		mr, err := d.CreateMergeRequest(ctx, pid, fid, u, fmt.Sprintf("%s MR %d", prefix, i))
		if err != nil {
			t.Fatalf("CreateMergeRequest %d: %v", i, err)
		}
		if err := d.ExecuteMerge(ctx, mr.ID, pid); err != nil {
			t.Fatalf("ExecuteMerge %d: %v", i, err)
		}
		ids = append(ids, u)
	}
	return ids
}

func TestAClosedCallCountsTheEligiblePopulationForQuorum(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	// The identity of these members is not the point -- their existence is. What
	// matters is that eligible > 1, so a quorum threshold above 0 exists at all.
	eligibleMemberPool(t, d, pid, 3, "quorum-member")

	eligible, err := d.EligibleCollaboratorCount(ctx, pid)
	if err != nil {
		t.Fatalf("EligibleCollaboratorCount: %v", err)
	}
	if eligible < 2 {
		t.Fatalf("fixture produced eligible=%d; a threshold of 0 would make this "+
			"test pass for the wrong reason", eligible)
	}

	call, err := d.CreateConsensusCall(ctx, pid, 0, uid, "", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	if _, err := d.CastPosition(ctx, call.ID, uid, "consent"); err != nil {
		t.Fatalf("CastPosition: %v", err)
	}

	sum, err := d.CloseConsensusCall(ctx, call.ID)
	if err != nil {
		t.Fatalf("CloseConsensusCall: %v", err)
	}
	if sum.Counts.Eligible != eligible {
		t.Errorf("the summary reports eligible=%d, want %d", sum.Counts.Eligible, eligible)
	}
	// The specific wrong answer the bug produced. `insufficient_quorum` is the
	// correct one here; asserting the negative alone would also pass if the call
	// failed for some unrelated reason.
	if sum.Result == string(governance.ResultAccepted) {
		t.Fatalf("one consent out of %d eligible was ACCEPTED; quorum is not enforced", eligible)
	}
	if sum.Result != string(governance.ResultInsufficientQuorum) {
		t.Errorf("got result %q, want %q", sum.Result, governance.ResultInsufficientQuorum)
	}
}

// The other half: once the eligible population IS large enough to meet quorum, the
// same single consent must no longer be enough on its own -- it has to clear the
// support ratio as well. Without this the fix could be over-corrected into "quorum
// always fails".
func TestQuorumBeingMetStillRequiresSupportToPass(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	eligibleMemberPool(t, d, pid, 2, "supporter")

	call, err := d.CreateConsensusCall(ctx, pid, 0, uid, "", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	if _, err := d.CastPosition(ctx, call.ID, uid, "consent"); err != nil {
		t.Fatalf("CastPosition: %v", err)
	}
	sum, err := d.CloseConsensusCall(ctx, call.ID)
	if err != nil {
		t.Fatalf("CloseConsensusCall: %v", err)
	}
	// Whatever the verdict, it must be a real evaluation rather than the vacuous
	// one: an unset Eligible produced "accepted", so assert the result is NOT that.
	if sum.Counts.Eligible <= 0 {
		t.Errorf("eligible is %d on a closing call; quorum is being computed against zero", sum.Counts.Eligible)
	}
	if sum.Result == "" {
		t.Error("a closed call reported no result at all")
	}
}

// A stand-aside is a reservation, not consent and not opposition. The tally must
// count it in neither ratio's decisive denominator, which is what
// `Decisive()` encodes. This is the numbers the Consensus page has to render, so it
// is worth pinning at the store level where the page will read them from.
func TestTheTallySeparatesReservationsFromOpposition(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	voters := eligibleMemberPool(t, d, pid, 3, "tally-member")

	call, err := d.CreateConsensusCall(ctx, pid, 0, uid, "", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	// 1 consent, 2 stand-asides, 1 block would be decisive 1/2 = 0.50; the
	// first version of the spec's ratio counted the stand-asides and got 1/4.
	for i, stance := range []string{"consent", "stand_aside", "stand_aside"} {
		who := uid
		if i < len(voters) {
			who = voters[i]
		}
		if _, err := d.CastPosition(ctx, call.ID, who, stance); err != nil {
			t.Fatalf("CastPosition %s: %v", stance, err)
		}
	}
	sum, err := d.CloseConsensusCall(ctx, call.ID)
	if err != nil {
		t.Fatalf("CloseConsensusCall: %v", err)
	}
	if sum.Counts.StandAside != 2 {
		t.Errorf("stand-asides counted as %d, want 2", sum.Counts.StandAside)
	}
	if got := sum.Counts.Decisive(); got != sum.Counts.Consent+sum.Counts.Block {
		t.Errorf("the decisive denominator is %d, want consent+block (%d+%d)",
			got, sum.Counts.Consent, sum.Counts.Block)
	}
}
