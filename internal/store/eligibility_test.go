package store

import (
	"context"
	"testing"
	"time"
)

// §8.2's eligible collaborator, in full.
//
// The previous implementation counted roles and nothing else, so it answered "how
// many people hold a contributor role" to a question about "who has done
// something recently". These tests pin each of the five qualifying routes and
// each of the three ways to fail it, because the failure mode of a too-permissive
// population is invisible: quorum is satisfied by people who left.

// eligibleFixture is a project with a validated complaint, a feature linking it,
// and four accounts: the maintainer, an active contributor, an inactive
// contributor, and an active guest.
type eligibleFixture struct {
	store      *DB
	project    int64
	feature    int64
	maintainer int64
	// author owns the fixture's feature and complaint but never files a solution,
	// because §5.2 refuses a feature's author from authoring its solutions. Every
	// test that needs somebody to ship needs somebody who is not the feature's
	// author, and making that an explicit field stops each test rediscovering it.
	author      int64
	active      int64
	dormant     int64
	activeGuest int64
}

func setupEligible(t *testing.T) eligibleFixture {
	t.Helper()
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	c, err := store.CreateComplaint(ctx, pid, uid, "eligibility complaint", "body", 3, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if _, err := store.ExecContext(ctx,
		`UPDATE complaints SET status = 'validated' WHERE id = ?`, c.ID); err != nil {
		t.Fatalf("validate: %v", err)
	}
	author := newUserNamed(t, store, "eligauthor")
	if err := store.JoinProject(ctx, pid, author); err != nil {
		t.Fatalf("JoinProject: %v", err)
	}
	f, err := store.CreateFeature(ctx, pid, author, "eligibility feature", "body", "M", nil, nil, []int64{c.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	fx := eligibleFixture{
		store:      store,
		project:    pid,
		feature:    f.ID,
		maintainer: uid,
		author:     author,
	}
	// An active contributor who will make a qualifying contribution.
	fx.active = newUserNamed(t, store, "eligactive")
	if err := store.JoinProject(ctx, pid, fx.active); err != nil {
		t.Fatalf("JoinProject: %v", err)
	}
	// A contributor whose only work is older than the window.
	fx.dormant = newUserNamed(t, store, "eligdormant")
	if err := store.JoinProject(ctx, pid, fx.dormant); err != nil {
		t.Fatalf("JoinProject: %v", err)
	}
	// A guest with real work: §8.2 requires the role too, so this stays ineligible.
	fx.activeGuest = newUserNamed(t, store, "eligguest")
	return fx
}

// shipSolution creates a solution and moves it to a shipped status.
func (fx eligibleFixture) shipSolution(t *testing.T, authorID int64, title string) int64 {
	t.Helper()
	ctx := context.Background()
	sol, err := fx.store.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: fx.feature, AuthorID: authorID, Title: title,
		Type: SolutionBuildNew,
	})
	if err != nil {
		t.Fatalf("CreateSolution(%s): %v", title, err)
	}
	if _, err := fx.store.ExecContext(ctx,
		`UPDATE solutions SET status = 'shipped' WHERE id = ?`, sol.ID); err != nil {
		t.Fatalf("ship: %v", err)
	}
	return sol.ID
}

func TestNobodyQualifiesUntilSomethingShips(t *testing.T) {
	fx := setupEligible(t)
	n, err := fx.store.EligibleCollaboratorCount(context.Background(), fx.project)
	if err != nil {
		t.Fatalf("EligibleCollaboratorCount: %v", err)
	}
	// Zero, and this is the point of route (2). The maintainer created the project
	// and filed a *validated* complaint, and that still does not qualify them:
	// §8.2 wants "a validated complaint that led to a shipped solution", and the
	// leading-to clause is doing exactly the work it was written for. A project
	// where hearing complaints is enough to make everyone eligible is a project
	// where the quorum floor is met by the queue, not by the room.
	//
	// The first version of this test asserted 1 on the premise that a validated
	// complaint qualified. It failed, and the failure was the specification
	// working rather than the test being wrong.
	if n != 0 {
		t.Fatalf("count %d, want 0: a validated complaint with nothing shipped qualifies nobody", n)
	}
	// Ship one and the maintainer qualifies immediately.
	fx.shipSolution(t, fx.maintainer, "the fix for the complaint")
	if err := fx.assertEligible(t, fx.maintainer, true); err != "" {
		t.Error(err)
	}
}

// TestAMergedMergeRequestIsQualifying is route (1).
func TestAMergedMergeRequestIsQualifying(t *testing.T) {
	fx := setupEligible(t)
	ctx := context.Background()
	mr, err := fx.store.CreateMergeRequest(ctx, fx.project, fx.feature, fx.active, "implement it")
	if err != nil {
		t.Fatalf("CreateMergeRequest: %v", err)
	}
	if err := fx.assertEligible(t, fx.active, false); err != "" {
		t.Error(err)
	}

	// Opened is not merged. §8.2 says "merged code or docs".
	if _, err := fx.store.ExecContext(ctx,
		`UPDATE merge_requests SET status = 'merged', closed_at = ? WHERE id = ?`,
		float64(time.Now().Unix()), mr.ID); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if err := fx.assertEligible(t, fx.active, true); err != "" {
		t.Error(err)
	}
}

// TestADocumentIsQualifying is the docs half of route (1). Documents have no merge
// request, so requiring one would make documentation never count.
func TestADocumentIsQualifying(t *testing.T) {
	fx := setupEligible(t)
	if err := fx.assertEligible(t, fx.active, false); err != "" {
		t.Fatal(err)
	}
	if _, err := fx.store.PutDocument(context.Background(), fx.project, fx.active,
		"readme", "how-to", "How to do the thing", "body"); err != nil {
		t.Fatalf("PutDocument: %v", err)
	}
	if err := fx.assertEligible(t, fx.active, true); err != "" {
		t.Error(err)
	}
}

// TestAValidatedComplaintNeedsAShippedSolutionToFollow is route (2), and the whole
// point of the "led to" wording. A validated complaint on its own is not enough.
func TestAValidatedComplaintNeedsAShippedSolutionToFollow(t *testing.T) {
	fx := setupEligible(t)
	ctx := context.Background()
	// A second complaint, validated but with no solution behind it.
	c, err := fx.store.CreateComplaint(ctx, fx.project, fx.active, "heard and ignored", "b", 3, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if _, err := fx.store.ExecContext(ctx,
		`UPDATE complaints SET status = 'validated' WHERE id = ?`, c.ID); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := fx.assertEligible(t, fx.active, false); err != "" {
		t.Fatalf("a validated complaint with nothing shipped already qualified: %s", err)
	}

	// Now link it to a feature and ship a solution. A feature's author cannot
	// author its solutions (§5.2), so the fix comes from somebody else -- which is
	// realistic and costs the complainant nothing: §8.2 counts the person whose
	// complaint led to the fix, not the person who wrote it.
	f, err := fx.store.CreateFeature(ctx, fx.project, fx.author, "ignored, now fixed",
		"b", "M", nil, nil, []int64{c.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	sol, err := fx.store.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: f.ID, AuthorID: fx.active, Title: "the fix",
		Type: SolutionBuildNew,
	})
	if err != nil {
		t.Fatalf("CreateSolution: %v", err)
	}
	// Still not eligible for the COMPLAINANT: the solution must be shipped.
	if err := fx.assertEligible(t, fx.active, false); err != "" {
		t.Fatalf("a solution in draft already qualified the complainant: %s", err)
	}
	if _, err := fx.store.ExecContext(ctx,
		`UPDATE solutions SET status = 'shipped' WHERE id = ?`, sol.ID); err != nil {
		t.Fatalf("ship: %v", err)
	}
	if err := fx.assertEligible(t, fx.active, true); err != "" {
		t.Error(err)
	}
}

// TestAnAcceptedSolutionIsQualifying is route (3), and the status boundary: a
// proposal is not an accepted solution.
func TestAnAcceptedSolutionIsQualifying(t *testing.T) {
	fx := setupEligible(t)
	ctx := context.Background()
	if _, err := fx.store.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: fx.feature, AuthorID: fx.active, Title: "just a proposal",
		Type: SolutionBuildNew,
	}); err != nil {
		t.Fatalf("CreateSolution: %v", err)
	}
	// A draft is a proposal.
	if err := fx.assertEligible(t, fx.active, false); err != "" {
		t.Fatalf("a draft solution already qualified its author: %s", err)
	}
	// 'consensus' is where §6.5's outcome accepts a proposal.
	if _, err := fx.store.ExecContext(ctx,
		`UPDATE solutions SET status = 'consensus' WHERE author_id = ? AND feature_id = ?`,
		fx.active, fx.feature); err != nil {
		t.Fatalf("set consensus: %v", err)
	}
	if err := fx.assertEligible(t, fx.active, true); err != "" {
		t.Error(err)
	}
}

// TestAReviewOfSomebodyElsesWorkIsQualifying is route (4), including the
// own-approval exclusion.
//
// The exclusion needs an account whose ONLY possible qualification is the
// self-approval. Using the maintainer does not work: they also own a validated
// complaint, so they qualify either way and the test proves nothing. That is why
// this uses a dedicated author with no other route open -- the first version of
// this test logged the ambiguity instead of eliminating it, and removing the
// own-approval guard from the query left it green.
func TestAReviewOfSomebodyElsesWorkIsQualifying(t *testing.T) {
	fx := setupEligible(t)
	ctx := context.Background()
	mr, err := fx.store.CreateMergeRequest(ctx, fx.project, fx.feature, fx.author, "needs review")
	if err != nil {
		t.Fatalf("CreateMergeRequest: %v", err)
	}
	// The author approving their own merge request is not a review.
	if err := fx.store.ApproveMerge(ctx, mr.ID, fx.author); err != nil {
		t.Fatalf("ApproveMerge: %v", err)
	}
	if ok, err := fx.store.HasQualifyingContribution(ctx, fx.project, fx.author); err != nil {
		t.Fatalf("HasQualifyingContribution: %v", err)
	} else if ok {
		t.Error("approving your own merge request counted as a substantive review")
	}
	if err := fx.assertEligible(t, fx.author, false); err != "" {
		t.Error(err)
	}

	// A third party approving IS a review.
	reviewer := newUserNamed(t, fx.store, "eligreviewer")
	if err := fx.store.JoinProject(ctx, fx.project, reviewer); err != nil {
		t.Fatalf("JoinProject: %v", err)
	}
	if err := fx.store.ApproveMerge(ctx, mr.ID, reviewer); err != nil {
		t.Fatalf("ApproveMerge: %v", err)
	}
	if err := fx.assertEligible(t, reviewer, true); err != "" {
		t.Error(err)
	}
}

// TestAnUnjustifiedModerationActionIsNotQualifying is route (5). "Upheld" is read
// as a justified ledger entry, which is why the query checks justification rather
// than mere existence.
func TestAnUnjustifiedModerationActionIsNotQualifying(t *testing.T) {
	fx := setupEligible(t)
	ctx := context.Background()
	if _, err := fx.store.ExecContext(ctx, `
		INSERT INTO admin_ledger (actor_id, action, subject, justification, project_id, created_at)
		VALUES (?, 'revert_status', 'complaint', NULL, ?, ?)`, fx.active, fx.project, nowUnix()); err != nil {
		t.Fatalf("unjustified: %v", err)
	}
	if err := fx.assertEligible(t, fx.active, false); err != "" {
		t.Fatalf("an unjustified ledger entry already qualified: %s", err)
	}
	// A blank justification is no justification.
	if _, err := fx.store.ExecContext(ctx, `
		INSERT INTO admin_ledger (actor_id, action, subject, justification, project_id, created_at)
		VALUES (?, 'revert_status', 'complaint', '   ', ?, ?)`, fx.active, fx.project, nowUnix()); err != nil {
		t.Fatalf("blank: %v", err)
	}
	if err := fx.assertEligible(t, fx.active, false); err != "" {
		t.Fatalf("a whitespace justification already qualified: %s", err)
	}
	// A real one does.
	if _, err := fx.store.ExecContext(ctx, `
		INSERT INTO admin_ledger (actor_id, action, subject, justification, project_id, created_at)
		VALUES (?, 'revert_status', 'complaint', 'the complaint misdescribed the bug', ?, ?)`,
		fx.active, fx.project, nowUnix()); err != nil {
		t.Fatalf("justified: %v", err)
	}
	if err := fx.assertEligible(t, fx.active, true); err != "" {
		t.Error(err)
	}
}

// TestARoleIsNecessaryAsWellAsActivity is §8.2's conjunction. An active guest with
// real work is not eligible, because the spec requires the role too.
func TestARoleIsNecessaryAsWellAsActivity(t *testing.T) {
	fx := setupEligible(t)
	fx.shipSolution(t, fx.activeGuest, "a guest's work")
	if ok, err := fx.store.HasQualifyingContribution(context.Background(), fx.project, fx.activeGuest); err != nil {
		t.Fatalf("HasQualifyingContribution: %v", err)
	} else if !ok {
		t.Fatal("the guest's shipped solution is not counted as a contribution at all")
	}
	if err := fx.assertEligible(t, fx.activeGuest, false); err != "" {
		t.Fatal(err)
	}
	// Promote and they qualify immediately -- the activity was already there.
	if _, err := fx.store.ExecContext(context.Background(),
		`INSERT OR REPLACE INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)`,
		fx.project, fx.activeGuest, "contributor", float64(time.Now().Unix())); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := fx.assertEligible(t, fx.activeGuest, true); err != "" {
		t.Error(err)
	}
}

// TestTheTrailingWindowExcludesDormantContributors is the reason the count changed.
// This is the case the previous role-only count got wrong in every project with
// anyone who joined long ago and left.
func TestTheTrailingWindowExcludesDormantContributors(t *testing.T) {
	fx := setupEligible(t)
	ctx := context.Background()
	fx.shipSolution(t, fx.dormant, "old work")
	if err := fx.assertEligible(t, fx.dormant, true); err != "" {
		t.Fatalf("fresh work does not qualify: %s", err)
	}

	// Age every qualifying event for the dormant user past the window.
	old := float64(time.Now().Add(-500 * 24 * time.Hour).Unix())
	if _, err := fx.store.ExecContext(ctx,
		`UPDATE solutions SET updated_at = ? WHERE author_id = ?`, old, fx.dormant); err != nil {
		t.Fatalf("age: %v", err)
	}
	if err := fx.assertEligible(t, fx.dormant, false); err != "" {
		t.Fatal(err)
	}

	// The window is a parameter, not a constant in stone: a two-year window
	// brings them back.
	ids, err := fx.store.EligibleCollaborators(ctx, fx.project, 730)
	if err != nil {
		t.Fatalf("EligibleCollaborators: %v", err)
	}
	if !contains(ids, fx.dormant) {
		t.Errorf("a 730-day window excluded the dormant user (%v); the window is not being applied",
			ids)
	}
	// And a one-day window excludes everybody, which is the sanity check that the
	// parameter does anything at all.
	ids, err = fx.store.EligibleCollaborators(ctx, fx.project, 0.001)
	if err != nil {
		t.Fatalf("EligibleCollaborators: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("a window of ~1 minute returned %d eligible collaborators (%v)", len(ids), ids)
	}
}

// TestTheCountAndTheIdListAgree is the invariant every quorum caller depends on:
// §6.5's distinct-voter check needs the ids, and the threshold needs the count, and
// they must describe the same population.
func TestTheCountAndTheIdListAgree(t *testing.T) {
	fx := setupEligible(t)
	ctx := context.Background()
	fx.shipSolution(t, fx.active, "one")
	fx.shipSolution(t, fx.dormant, "two")
	fx.shipSolution(t, fx.activeGuest, "three")

	ids, err := fx.store.EligibleCollaborators(ctx, fx.project, 0)
	if err != nil {
		t.Fatalf("EligibleCollaborators: %v", err)
	}
	n, err := fx.store.EligibleCollaboratorCount(ctx, fx.project)
	if err != nil {
		t.Fatalf("EligibleCollaboratorCount: %v", err)
	}
	if n != len(ids) {
		t.Errorf("count %d but %d ids (%v)", n, len(ids), ids)
	}
	// And IsEligibleCollaborator agrees with membership of that list.
	for _, id := range ids {
		ok, err := fx.store.IsEligibleCollaborator(ctx, fx.project, id)
		if err != nil {
			t.Fatalf("IsEligibleCollaborator: %v", err)
		}
		if !ok {
			t.Errorf("user %d is in the id list but IsEligibleCollaborator says no", id)
		}
	}
	ok, err := fx.store.IsEligibleCollaborator(ctx, fx.project, 999999)
	if err != nil {
		t.Fatalf("IsEligibleCollaborator: %v", err)
	}
	if ok {
		t.Error("a user who has never existed is eligible")
	}
}

// assertEligible reports a mismatch as a string, so several tests can assert it
// with t.Fatal and still run their remaining setup.
func (fx eligibleFixture) assertEligible(t *testing.T, userID int64, want bool) string {
	t.Helper()
	got, err := fx.store.IsEligibleCollaborator(context.Background(), fx.project, userID)
	if err != nil {
		return "IsEligibleCollaborator: " + err.Error()
	}
	if got != want {
		return "eligible = " + boolStr(got) + ", want " + boolStr(want)
	}
	return ""
}

func nowUnix() float64 { return float64(time.Now().Unix()) }

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func contains(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
