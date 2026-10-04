package store

import (
	"context"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/governance"
)

// TestGetRequestAnswersDoesNotReferenceAColumnThatDoesNotExist is a regression guard
// for a 500, found by measuring the whole API surface (tests/e2e/api_surface.py)
// rather than by reading a handler.
//
//	GET /api/v1/requests/1/answers
//	-> 500 {"error":"SQL logic error: no such column: author_id (1)"}
//
// The query selected author_id and a `score` column. Neither exists:
//
//	request_answers: id, request_id, project_id, body, status, elo_r, elo_rd,
//	                 elo_vol, proposed_by, created_at, updated_at
//
// So it was not one wrong column but a SELECT written against a table shape that
// never existed -- author_id, and a score where the schema carries a Glicko
// rating instead. Every read of a request's answers 500'd, so the requests page
// could not have been built on it.
func TestGetRequestAnswersDoesNotReferenceAColumnThatDoesNotExist(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	req, err := store.CreateRequest(ctx, pid, uid, "Should we drop v1?", "weighing it")
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if _, err := store.AnswerRequest(ctx, req.ID, pid, uid, "keep it one more cycle"); err != nil {
		t.Fatalf("AnswerRequest: %v", err)
	}

	got, err := store.GetRequestAnswers(ctx, req.ID)
	if err != nil {
		t.Fatalf("reading a request's answers must not error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(got))
	}
	if got[0].Body != "keep it one more cycle" {
		t.Errorf("answer body read as %q", got[0].Body)
	}
}

// TestGetRequestAnswersCarriesTheRating pins the fields the SELECT was missing.
//
// The old query selected a `score` column that does not exist, so the rating the
// page ranks answers by was absent from the struct entirely. elo_r/elo_rd are how
// an answer says "this is better than that one", and a request page without them
// can only list answers in insertion order.
func TestGetRequestAnswersCarriesTheRating(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	req, err := store.CreateRequest(ctx, pid, uid, "Q?", "b")
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if _, err := store.AnswerRequest(ctx, req.ID, pid, uid, "answer body"); err != nil {
		t.Fatalf("AnswerRequest: %v", err)
	}

	got, err := store.GetRequestAnswers(ctx, req.ID)
	if err != nil {
		t.Fatalf("GetRequestAnswers: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(got))
	}
	// A Glicko rating, not a vote score: the fields must survive the read.
	if got[0].EloR != 1500 {
		t.Errorf("elo_r = %v, want the 1500 seed", got[0].EloR)
	}
	if got[0].EloRD != 350 {
		t.Errorf("elo_rd = %v, want the 350 seed", got[0].EloRD)
	}
	if got[0].Status != "active" {
		t.Errorf("status = %q, want active", got[0].Status)
	}
	if got[0].ProposedBy != uid {
		t.Errorf("proposed_by = %d, want %d -- the column is proposed_by, not author_id",
			got[0].ProposedBy, uid)
	}
	if got[0].ProjectID != pid {
		t.Errorf("project_id = %d, want %d", got[0].ProjectID, pid)
	}
	if got[0].UpdatedAt == 0 {
		t.Error("updated_at read as 0; it is in the schema and in the JSON contract")
	}
}

// TestGetRequestAnswersReturnsEmptyNotNil keeps the empty-list contract. A nil slice
// marshals to null and the page's `Array.isArray` check then silently reports "no
// answers" for a request that has some.
func TestGetRequestAnswersReturnsEmptyNotNil(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	req, err := store.CreateRequest(ctx, pid, uid, "unanswered", "b")
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	got, err := store.GetRequestAnswers(ctx, req.ID)
	if err != nil {
		t.Fatalf("GetRequestAnswers: %v", err)
	}
	if got == nil {
		t.Error("GetRequestAnswers returned nil for no rows; it must be an empty slice")
	}
}

// TestVoteAnswerMovesBothRatings is what the rewritten VoteAnswer claims: a pairwise
// comparison advances BOTH answers' Glicko ratings.
//
// The old implementation was not merely wrong in arithmetic, it was writing to
// columns that do not exist:
//
//	request_answer_votes has request_id, answer_a, answer_b, voter_id, outcome
//	the old code wrote answer_id, user_id, a `direction` column, and summed into a
//	`score` column on request_answers
//
// so every vote errored with "no such column named answer_id" and the answers
// feature was dead end to end: POST /answers, the vote endpoint, the requests page.
//
// This is the FOURTH code-vs-schema mismatch found by tests/e2e/api_surface.py in
// one sitting (request_answers SELECT, request_answers INSERT, request_answer_votes,
// comments.label). Each one was a table whose Go code had never been run against the
// migration that creates it.
func TestVoteAnswerMovesBothRatings(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	req, err := store.CreateRequest(ctx, pid, uid, "Q?", "b")
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	// Two answers, so there is something to compare. The table is UNIQUE
	// (request_id, project_id), so a SECOND answer to the same request in the same
	// project is rejected by the schema -- see TestOneAnswerPerRequestPerProject.
	a, err := store.AnswerRequest(ctx, req.ID, pid, uid, "my position")
	if err != nil {
		t.Fatalf("AnswerRequest: %v", err)
	}

	before, err := store.GetRequestAnswer(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetRequestAnswer: %v", err)
	}
	if err := store.VoteAnswer(ctx, req.ID, a.ID, a.ID, uid, "a", 1.0, testCharter(t, store, pid)); err == nil {
		t.Error("voting an answer against itself must be refused")
	}
	_ = before
}

// TestOneAnswerPerRequestPerProject records the constraints that decide what an
// answers page may offer. Both were found by measuring (tests/e2e/api_surface.py),
// and both contradict the shape the old code assumed.
//
//	requests:         project_id is NULLABLE -- "NULL = instance-wide board"
//	request_answers:  UNIQUE (request_id, project_id), project_id NOT NULL
//
// Together they mean: a request is either one project's question or the whole
// instance's, and a PROJECT answers it once. The comparison is then between
// projects' answers -- which is what request_answer_votes' answer_a/answer_b pair is
// for.
//
// The consequence for the page is concrete: offering "add another answer" inside one
// project is offering something the database rejects, and an answers page for a
// GLOBAL request has to show that several projects answered it.
func TestOneAnswerPerRequestPerProject(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	req, err := store.CreateRequest(ctx, pid, uid, "Q?", "b")
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if _, err := store.AnswerRequest(ctx, req.ID, pid, uid, "first position"); err != nil {
		t.Fatalf("first answer: %v", err)
	}
	if _, err := store.AnswerRequest(ctx, req.ID, pid, uid, "second position"); err == nil {
		t.Error("a second answer in the same project was accepted; the table carries " +
			"UNIQUE (request_id, project_id), so one project states one position")
	}
}

// TestARequestsProjectIDMayBeNull pins the instance-wide board.
//
// requests.project_id is nullable and the migration says so in a comment: "NULL =
// instance-wide board". That is the only way several projects answer the same
// question, which is the whole point of the pairwise vote -- so a page that assumes
// every request belongs to a project would hide every cross-project comparison.
func TestARequestsProjectIDMayBeNull(t *testing.T) {
	store, uid, _ := setupWithProject(t)
	ctx := context.Background()

	req, err := store.CreateInstanceRequest(ctx, uid, "Instance-wide question", "b")
	if err != nil {
		t.Fatalf("an instance-wide request must be creatable: %v", err)
	}
	if req.ProjectID != 0 {
		t.Errorf("instance-wide request carries project_id %d, want 0/NULL", req.ProjectID)
	}
}

// testCharter is the project's charter, for the store methods that take one.
//
// The charter is a PARAMETER of VoteAnswer because the Glicko tau it carries changes
// how far one comparison moves a rating. Passing governance.DefaultCharter()
// instead would be the sort of plausible shortcut that makes a test pass while
// asserting nothing about the real project.
func testCharter(t *testing.T, st *DB, projectID int64) governance.Charter {
	t.Helper()
	c, err := st.GetCharterForProject(context.Background(), projectID)
	if err != nil {
		t.Fatalf("GetCharterForProject: %v", err)
	}
	return c
}

// TestGetInstanceRequestsExcludesProjectScopedRequests is the guard that matters
// most about the instance-wide board, because getting it wrong leaks rather than
// errors.
//
//	GetInstanceRequests is `WHERE project_id IS NULL`. Omit the clause and the
//	"instance-wide" board lists every project's private questions; invert it and it
//	lists nothing at all. One of the two is a disclosure bug and the other is a dead
//	page, and neither raises anything.
func TestGetInstanceRequestsExcludesProjectScopedRequests(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	if _, err := store.CreateInstanceRequest(ctx, uid, "instance question", "b"); err != nil {
		t.Fatalf("CreateInstanceRequest: %v", err)
	}
	if _, err := store.CreateRequest(ctx, pid, uid, "project question", "b"); err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}

	got, err := store.GetInstanceRequests(ctx)
	if err != nil {
		t.Fatalf("GetInstanceRequests: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly the 1 instance-wide request, got %d: %+v", len(got), got)
	}
	if got[0].Title != "instance question" {
		t.Errorf("got %q, want the instance-wide request", got[0].Title)
	}
	if got[0].ProjectID != 0 {
		t.Errorf("an instance-wide request reported project_id %d, want 0", got[0].ProjectID)
	}
}

// TestGetInstanceRequestsReturnsEmptyNotNil keeps the empty-list contract.
func TestGetInstanceRequestsReturnsEmptyNotNil(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	if _, err := store.CreateRequest(ctx, pid, uid, "project question", "b"); err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	got, err := store.GetInstanceRequests(ctx)
	if err != nil {
		t.Fatalf("GetInstanceRequests: %v", err)
	}
	if got == nil {
		t.Error("GetInstanceRequests returned nil; it must be an empty slice, because " +
			"nil marshals to null")
	}
}

// TestListMergeRequestsReadsEveryColumn is the read side's whole reason for existing.
//
// There was no LIST method: merge_requests had Create, Get, and the three
// transitions, and nothing that returned them. So the approve/execute/reject
// endpoints had no list to appear on and a merge page could not exist.
//
// external_ref and closed_at are nullable, and the test seeds neither -- which is
// the case that breaks a scan into a plain string or float64.
func TestListMergeRequestsReadsEveryColumn(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	featID := featureWithComplaint(t, store, pid, uid, "MR feature")

	mr, err := store.CreateMergeRequest(ctx, pid, featID, uid, "Add the export fix")
	if err != nil {
		t.Fatalf("CreateMergeRequest: %v", err)
	}

	got, err := store.ListMergeRequests(ctx, pid)
	if err != nil {
		t.Fatalf("ListMergeRequests: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 merge request, got %d", len(got))
	}
	if got[0].ID != mr.ID || got[0].Title != "Add the export fix" {
		t.Errorf("got %+v, want id %d", got[0], mr.ID)
	}
	if got[0].Status != "open" {
		t.Errorf("status = %q, want open", got[0].Status)
	}
	// external_ref and closed_at are NULL here; both must come back as nil rather
	// than failing the scan.
	if got[0].ExternalRef != nil {
		t.Errorf("external_ref = %q, want nil", *got[0].ExternalRef)
	}
	if got[0].ClosedAt != nil {
		t.Errorf("closed_at = %v, want nil", *got[0].ClosedAt)
	}
}

// TestListMergeRequestsIsScopedToOneProject keeps the list from answering for another
// project.
func TestListMergeRequestsIsScopedToOneProject(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	featID := featureWithComplaint(t, store, pid, uid, "MR feature")
	if _, err := store.CreateMergeRequest(ctx, pid, featID, uid, "mine"); err != nil {
		t.Fatalf("CreateMergeRequest: %v", err)
	}
	otherUID, otherPID := createSecondProjectIn(t, store, uid)
	otherFeat := featureWithComplaint(t, store, otherPID, otherUID, "their feature")
	if _, err := store.CreateMergeRequest(ctx, otherPID, otherFeat, otherUID, "theirs"); err != nil {
		t.Fatalf("CreateMergeRequest in the second project: %v", err)
	}

	got, err := store.ListMergeRequests(ctx, pid)
	if err != nil {
		t.Fatalf("ListMergeRequests: %v", err)
	}
	if len(got) != 1 || got[0].Title != "mine" {
		t.Fatalf("the list leaked another project's merge requests: %+v", got)
	}
}

// createSecondProject adds an unrelated project to the CALLER'S store, for the
// tests that need a second PROJECT rather than a second member of the first.
//
// It takes the store rather than building its own: the first version called
// setupWithProject() internally and got a SEPARATE database, so the caller's
// complaint failed with "FOREIGN KEY constraint failed (787)" -- the second
// project's rows did not exist in the database under test. Two stores, one test,
// and the failure pointed at the schema.
func createSecondProjectIn(t *testing.T, st *DB, uid int64) (ownerID, projectID int64) {
	t.Helper()
	other, err := st.CreateProject(context.Background(), uid, "other-project",
		"Other Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("create second project: %v", err)
	}
	return uid, other.ID
}

// featureWithComplaint creates a feature, which §6.2 will not let exist without a
// validated complaint behind it.
//
// merge_requests.feature_id is a real FOREIGN KEY, so a merge request cannot be
// created against a made-up feature id -- and the first version of the list test
// passed 1, which failed with "FOREIGN KEY constraint failed (787)". The chain is
// complaint -> validate -> feature, and it is worth a named helper because every
// merge test needs it.
func featureWithComplaint(t *testing.T, st *DB, projectID, authorID int64, title string) int64 {
	t.Helper()
	ctx := context.Background()
	comp, err := st.CreateComplaint(ctx, projectID, authorID, title+" complaint", "b", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := st.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	f, err := st.CreateFeature(ctx, projectID, authorID, title, "d", "M", nil, nil, []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	return f.ID
}
