package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// §5.7's gate arithmetic, at the store.
//
// The HTTP tests prove the refusals arrive; these prove the numbers behind them,
// because a gate is only as good as the population it measures and the count it
// makes. Two of the three facts here are invisible from the API:
//
//   - an approval row that does not COUNT (the author's own, or a collaborator who
//     has since gone dormant) is still a row, so a page or a report that counted
//     rows would show a satisfied quorum the store refuses to honour;
//   - `needed` is clamped at the eligible population, so a one-person project does
//     not require two confirmations from two people who do not exist.

// mergeGateFixture is a project, a feature, a merge request and a set of
// accounts with the roles and contributions the gate measures.
type mergeGateFixture struct {
	store   *DB
	project int64
	feature int64
	mr      int64
}

// contributor creates an account that is an eligible collaborator under §8.2: a
// member with the contributor role and a qualifying contribution inside the window.
func contributor(t *testing.T, d *DB, pid int64, name string) int64 {
	t.Helper()
	ctx := context.Background()
	u := newUserNamed(t, d, name)
	if err := d.JoinProject(ctx, pid, u); err != nil {
		t.Fatalf("JoinProject(%s): %v", name, err)
	}
	if _, err := d.PutDocument(ctx, pid, u, DocumentKinds[0], name+"-doc", "notes", "body"); err != nil {
		t.Fatalf("PutDocument(%s): %v", name, err)
	}
	return u
}

func setupMergeGate(t *testing.T) mergeGateFixture {
	t.Helper()
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	fid := newFeature(t, d, pid, uid, "a feature that needs landing")
	author := contributor(t, d, pid, "gate-author")
	mr, err := d.CreateMergeRequest(ctx, pid, fid, author, "land it")
	if err != nil {
		t.Fatalf("CreateMergeRequest: %v", err)
	}
	return mergeGateFixture{store: d, project: pid, feature: fid, mr: mr.ID}
}

func (f mergeGateFixture) gate(t *testing.T) MergeGateState {
	t.Helper()
	g, err := f.store.MergeGateFor(context.Background(), f.mr)
	if err != nil {
		t.Fatalf("MergeGateFor: %v", err)
	}
	return g
}

// TestAnAuthorsOwnApprovalIsNotAConfirmation: §6.3 counts confirmations of each
// other's work. The row is inserted directly because ApproveMerge refuses it at the
// door -- but older rows exist, and the count must not include them either.
func TestAnAuthorsOwnApprovalIsNotAConfirmation(t *testing.T) {
	f := setupMergeGate(t)
	ctx := context.Background()
	author := f.authorOf(t)

	if _, err := f.store.ExecContext(ctx,
		`INSERT INTO merge_approvals (mr_id, user_id, weight, created_at) VALUES (?,?,1.0,?)`,
		f.mr, author, nowUnix()); err != nil {
		t.Fatalf("insert self-approval: %v", err)
	}
	if got := f.gate(t).Approvals; got != 0 {
		t.Errorf("approvals = %d with only the author's own approval present; want 0", got)
	}
}

// TestAConfirmationFromADormantCollaboratorStopsCounting: eligibility is role AND
// activity inside the window (§8.2), so a confirmation is not permanent. A quorum
// held open by people who left a year ago is the failure mode eligibility exists to
// stop.
//
// The voter's qualifying contribution here is the approval itself -- route (4) of
// qualifyingContributionsSQL counts "approving somebody else's merge request", which
// is why reviewing is one of the ways to become a collaborator. So the only way to
// age them out is to age the approval: no other row of theirs exists to backdate.
func TestAConfirmationFromADormantCollaboratorStopsCounting(t *testing.T) {
	f := setupMergeGate(t)
	ctx := context.Background()
	voter := member(t, f.store, f.project, "gate-voter")
	if err := f.store.ApproveMerge(ctx, f.mr, voter); err != nil {
		t.Fatalf("ApproveMerge: %v", err)
	}
	if got := f.gate(t).Approvals; got != 1 {
		t.Fatalf("approvals = %d, want 1 before the window lapses", got)
	}

	if _, err := f.store.ExecContext(ctx,
		`UPDATE merge_approvals SET created_at = ? WHERE mr_id = ? AND user_id = ?`,
		nowUnix()-800*24*3600, f.mr, voter); err != nil {
		t.Fatalf("age the approval: %v", err)
	}
	if got := f.gate(t).Approvals; got != 0 {
		t.Errorf("approvals = %d after the voter dropped out of the window; want 0 "+
			"-- a confirmation from somebody who has left must not hold the gate open", got)
	}
}

// member is a project member with the contributor role and no qualifying
// contribution yet: the state an account is in between joining and doing something.
func member(t *testing.T, d *DB, pid int64, name string) int64 {
	t.Helper()
	u := newUserNamed(t, d, name)
	if err := d.JoinProject(context.Background(), pid, u); err != nil {
		t.Fatalf("JoinProject(%s): %v", name, err)
	}
	return u
}

// TestTheQuorumIsFlooredAtTwoAndClampedAtThePopulation pins §6.3's arithmetic as
// the store reports it: min(eligible, max(2, ceil(0.25*eligible))).
func TestTheQuorumIsFlooredAtTwoAndClampedAtThePopulation(t *testing.T) {
	for _, tc := range []struct {
		extraCollaborators int
		wantNeeded         int
	}{
		// eligible 1: the floor would ask for 2 confirmations from a population of 1,
		// which is unsatisfiable -- hence the clamp.
		{0, 1},
		// eligible 4: max(2, ceil(1)) = 2, so the floor is what decides.
		{3, 2},
		// eligible 8: 25% of 8 is 2, still the floor.
		{7, 2},
		// eligible 12: ceil(3) = 3, so the ratio overtakes the floor.
		{11, 3},
	} {
		t.Run(fmt.Sprintf("eligible=%d", tc.extraCollaborators+1), func(t *testing.T) {
			f := setupMergeGate(t)
			for i := 0; i < tc.extraCollaborators; i++ {
				contributor(t, f.store, f.project, fmt.Sprintf("gate-extra-%d-%d", tc.extraCollaborators, i))
			}
			g := f.gate(t)
			if g.Needed != tc.wantNeeded {
				t.Errorf("needed = %d with eligible = %d, want %d", g.Needed, g.Eligible, tc.wantNeeded)
			}
			if g.Eligible != tc.extraCollaborators+1 {
				t.Errorf("eligible = %d, want %d: the fixture is not measuring what it thinks",
					g.Eligible, tc.extraCollaborators+1)
			}
			if g.Passed {
				t.Errorf("the gate passed with no confirmations at all: %+v", g)
			}
		})
	}
}

// TestDroppingTheQuorumOpensTheGateAndSaysSo: §5.7 lets a charter drop the merge
// quorum, and the state has to keep saying that it was dropped -- a gate that
// reports "passed" without distinguishing "two people confirmed" from "nobody had
// to" is a page that cannot explain what it is showing.
func TestDroppingTheQuorumOpensTheGateAndSaysSo(t *testing.T) {
	f := setupMergeGate(t)
	ctx := context.Background()
	if g := f.gate(t); g.Passed || !g.RequiresQuorum {
		t.Fatalf("a fresh project's gate is not asking for confirmations: %+v", g)
	}

	charter, err := f.store.GetCharterForProject(ctx, f.project)
	if err != nil {
		t.Fatalf("GetCharterForProject: %v", err)
	}
	charter.MergeRequiresQuorum = false
	charter.RequireReviewerApproval = false
	if err := f.store.UpdateCharter(ctx, f.project, charter); err != nil {
		t.Fatalf("UpdateCharter: %v", err)
	}

	g := f.gate(t)
	if !g.Passed {
		t.Fatalf("the charter dropped both requirements and the gate still refuses: %+v", g)
	}
	if g.RequiresQuorum {
		t.Errorf("the state does not say the quorum was dropped: %+v", g)
	}
	if g.Needed != 0 {
		t.Errorf("needed = %d with the quorum dropped, want 0", g.Needed)
	}
}

// TestOnlyAReviewerSatisfiesTheReviewerRequirement: two contributor confirmations
// meet the quorum and not the reviewer condition, which is the difference between
// RequireReviewerApproval being read and being decorative.
func TestOnlyAReviewerSatisfiesTheReviewerRequirement(t *testing.T) {
	f := setupMergeGate(t)
	ctx := context.Background()
	one := contributor(t, f.store, f.project, "gate-c1")
	two := contributor(t, f.store, f.project, "gate-c2")
	for _, u := range []int64{one, two} {
		if err := f.store.ApproveMerge(ctx, f.mr, u); err != nil {
			t.Fatalf("ApproveMerge: %v", err)
		}
	}
	g := f.gate(t)
	if !g.QuorumOK {
		t.Fatalf("two confirmations did not meet the quorum: %+v", g)
	}
	if g.ReviewerOK || g.Passed {
		t.Fatalf("two contributors satisfied the reviewer requirement: %+v", g)
	}

	// A reviewer's confirmation is the missing piece.
	reviewer := contributor(t, f.store, f.project, "gate-reviewer")
	if _, err := f.store.ExecContext(ctx,
		`UPDATE members SET role = 'reviewer' WHERE project_id = ? AND user_id = ?`,
		f.project, reviewer); err != nil {
		t.Fatalf("promote to reviewer: %v", err)
	}
	if err := f.store.ApproveMerge(ctx, f.mr, reviewer); err != nil {
		t.Fatalf("reviewer ApproveMerge: %v", err)
	}
	if g := f.gate(t); !g.Passed {
		t.Errorf("a reviewer's confirmation did not open the gate: %+v", g)
	}

	// And a duplicated approval is one confirmation: the schema's primary key makes
	// the row idempotent, and the count must be distinct people.
	if err := f.store.ApproveMerge(ctx, f.mr, one); err != nil {
		t.Fatalf("repeat ApproveMerge: %v", err)
	}
	if got := f.gate(t).Approvals; got != 3 {
		t.Errorf("approvals = %d after a repeated approval by the same person; want 3", got)
	}
}

// TestAMergeRequestLandsOnceAndRefusesToLandAgain: open -> merged, once. A second
// execute re-shipped the feature and re-closed complaints that were already closed,
// and a reject afterwards flipped a landed change to `rejected` -- both of which the
// status guard now refuses. Found by asking what happens twice rather than by reading.
func TestAMergeRequestLandsOnceAndRefusesToLandAgain(t *testing.T) {
	f := setupMergeGate(t)
	ctx := context.Background()
	charter, err := f.store.GetCharterForProject(ctx, f.project)
	if err != nil {
		t.Fatalf("GetCharterForProject: %v", err)
	}
	charter.MergeRequiresQuorum = false
	charter.RequireReviewerApproval = false
	if err := f.store.UpdateCharter(ctx, f.project, charter); err != nil {
		t.Fatalf("UpdateCharter: %v", err)
	}
	if err := f.store.ExecuteMerge(ctx, f.mr, f.project); err != nil {
		t.Fatalf("first merge: %v", err)
	}
	if err := f.store.ExecuteMerge(ctx, f.mr, f.project); !errors.Is(err, ErrConflict) {
		t.Errorf("a second merge returned %v; want a conflict saying it already merged", err)
	}
	if err := f.store.RejectMerge(ctx, f.mr, f.project); !errors.Is(err, ErrConflict) {
		t.Errorf("rejecting a merged request returned %v; want a conflict -- a landed "+
			"change must not be flipped to rejected", err)
	}
	mr, err := f.store.GetMergeRequest(ctx, f.mr)
	if err != nil {
		t.Fatalf("GetMergeRequest: %v", err)
	}
	if mr.Status != "merged" {
		t.Errorf("status = %q after a second execute and a reject; want merged", mr.Status)
	}
}

// authorOf returns the merge request's author, for the tests that need to say
// "this account, specifically".
func (f mergeGateFixture) authorOf(t *testing.T) int64 {
	t.Helper()
	mr, err := f.store.GetMergeRequest(context.Background(), f.mr)
	if err != nil {
		t.Fatalf("GetMergeRequest: %v", err)
	}
	return mr.AuthorID
}
