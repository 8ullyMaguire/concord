package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCreateAndGetComment(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	feature := commentFeature(t, store, pid, uid)
	comment, err := store.CreateComment(ctx, pid, feature, "feature", uid, "This needs work", "objection", nil)
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if comment.Body != "This needs work" {
		t.Errorf("expected body 'This needs work', got %s", comment.Body)
	}
	if comment.Label != "objection" {
		t.Errorf("expected label 'objection', got %s", comment.Label)
	}
	if comment.Score != 0 {
		t.Errorf("expected score 0, got %f", comment.Score)
	}

	got, err := store.GetComment(ctx, comment.ID)
	if err != nil {
		t.Fatalf("GetComment: %v", err)
	}
	if got.Body != comment.Body {
		t.Errorf("expected body %s, got %s", comment.Body, got.Body)
	}
}

func TestGetThreadComments(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	feature := commentFeature(t, store, pid, uid)
	// A SECOND real feature. The old fixture passed the literal 2 here, which only
	// worked because nothing verified the thread -- so this test was asserting that a
	// comment on a thread that does not exist stays out of another thread's list. It
	// was still a useful assertion; it just needed a thread that could exist.
	secondFeature := commentFeature(t, store, pid, uid)
	// Create two comments in the same thread
	_, err := store.CreateComment(ctx, pid, feature, "feature", uid, "First comment", "question", nil)
	if err != nil {
		t.Fatalf("CreateComment 1: %v", err)
	}
	_, err = store.CreateComment(ctx, pid, feature, "feature", uid, "Second comment", "support", nil)
	if err != nil {
		t.Fatalf("CreateComment 2: %v", err)
	}
	// Create comment in different thread
	_, err = store.CreateComment(ctx, pid, secondFeature, "feature", uid, "Different thread", "evidence", nil)
	if err != nil {
		t.Fatalf("CreateComment 3: %v", err)
	}

	comments, err := store.GetThreadComments(ctx, pid, 1, "feature")
	if err != nil {
		t.Fatalf("GetThreadComments: %v", err)
	}
	if len(comments) != 2 {
		t.Errorf("expected 2 comments in thread, got %d", len(comments))
	}
}

func TestDeleteComment(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	feature := commentFeature(t, store, pid, uid)
	comment, err := store.CreateComment(ctx, pid, feature, "feature", uid, "To delete", "offtopic", nil)
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	err = store.DeleteComment(ctx, comment.ID)
	if err != nil {
		t.Fatalf("DeleteComment: %v", err)
	}

	_, err = store.GetComment(ctx, comment.ID)
	if err == nil {
		t.Error("expected error for deleted comment, got nil")
	}
}

func TestVoteComment(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	feature := commentFeature(t, store, pid, uid)
	comment, err := store.CreateComment(ctx, pid, feature, "feature", uid, "Vote me", "question", nil)
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	// Upvote
	err = store.VoteComment(ctx, comment.ID, 1, 1)
	if err != nil {
		t.Fatalf("VoteComment upvote: %v", err)
	}
	err = store.UpdateCommentScore(ctx, comment.ID)
	if err != nil {
		t.Fatalf("UpdateCommentScore: %v", err)
	}

	got, _ := store.GetComment(ctx, comment.ID)
	if got.Score != 1 {
		t.Errorf("expected score 1 after upvote, got %f", got.Score)
	}

	// Downvote (overwrite)
	err = store.VoteComment(ctx, comment.ID, 1, -1)
	if err != nil {
		t.Fatalf("VoteComment downvote: %v", err)
	}
	err = store.UpdateCommentScore(ctx, comment.ID)
	if err != nil {
		t.Fatalf("UpdateCommentScore: %v", err)
	}

	got, _ = store.GetComment(ctx, comment.ID)
	if got.Score != -1 {
		t.Errorf("expected score -1 after downvote, got %f", got.Score)
	}
}

func TestNestedComment(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	feature := commentFeature(t, store, pid, uid)
	parent, err := store.CreateComment(ctx, pid, feature, "feature", uid, "Parent", "question", nil)
	if err != nil {
		t.Fatalf("CreateComment parent: %v", err)
	}

	childID := parent.ID
	child, err := store.CreateComment(ctx, pid, feature, "feature", uid, "Child reply", "objection", &childID)
	if err != nil {
		t.Fatalf("CreateComment child: %v", err)
	}
	if child.ParentID == nil {
		t.Error("expected ParentID to be set on child comment")
	} else if *child.ParentID != parent.ID {
		t.Errorf("expected ParentID %d, got %d", parent.ID, *child.ParentID)
	}
}

// TestGetThreadCommentsToleratesANullLabel is a regression guard for a 500, found by
// measuring the whole API surface rather than by reading a handler.
//
//	GET /api/v1/projects/probe/threads/feature/1
//	-> 500 {"error":"sql: Scan error on column index 7, name \"label\":
//	         converting NULL to string is unsupported"}
//
// The cause is a type mismatch that reads as correct. comments.label is NULLABLE
// (`label TEXT CHECK (label IN (...))` -- no NOT NULL) and ParentID right above it
// is correctly a *int64 for the same reason. Label was a plain string, so every
// read of a comment posted WITHOUT a label -- which is the default, since the field
// is omitempty -- scanned NULL into a string and failed.
//
// That made the whole comments API unusable for the ordinary case: a thread could be
// created and then never listed. The comment page could not have been built on it.
func TestGetThreadCommentsToleratesANullLabel(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	feature := commentFeature(t, store, pid, uid)

	// The ordinary case: no label. This is what every reader posting a comment
	// produces, because Label is omitempty.
	if _, err := store.CreateComment(ctx, pid, feature, "feature", uid, "no label here", "", nil); err != nil {
		t.Fatalf("CreateComment without a label: %v", err)
	}

	got, err := store.GetThreadComments(ctx, pid, 1, "feature")
	if err != nil {
		t.Fatalf("reading a thread whose comments have no label must not error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(got))
	}
	if got[0].Label != "" {
		t.Errorf("a NULL label read as %q, want the empty string", got[0].Label)
	}
}

// TestGetThreadCommentsStillReadsLabels keeps the labelled case working, so the fix
// cannot be "stop selecting the column".
func TestGetThreadCommentsStillReadsLabels(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	feature := commentFeature(t, store, pid, uid)

	if _, err := store.CreateComment(ctx, pid, feature, "feature", uid, "needs evidence", "objection", nil); err != nil {
		t.Fatalf("CreateComment with a label: %v", err)
	}
	got, err := store.GetThreadComments(ctx, pid, 1, "feature")
	if err != nil {
		t.Fatalf("GetThreadComments: %v", err)
	}
	if len(got) != 1 || got[0].Label != "objection" {
		t.Fatalf("want one comment labelled objection, got %+v", got)
	}
}

// commentFeature and commentThread build a REAL thread for a comment test.
//
// Every test in this file used to pass the literal 1 as the thread id, which worked
// only because comments.thread_id carries no FOREIGN KEY -- the one column in that
// table that references something and does not check it. CreateComment now verifies
// the thread belongs to the project (a cross-project comment was the consequence), so
// the phantom id is refused and the fixtures had to become real.
//
// This is the schema catching a test, not a test breaking: the fixtures asserted on a
// state the database should never have allowed.
func commentThread(t *testing.T, st *DB, projectID, authorID int64) (featureID, complaintID int64) {
	t.Helper()
	ctx := context.Background()

	comp, err := st.CreateComplaint(ctx, projectID, authorID, "complaint for the thread",
		"b", 3, 1.0, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := st.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	f, err := st.CreateFeature(ctx, projectID, authorID, "a feature to discuss", "b",
		"M", nil, nil, []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	return f.ID, comp.ID
}

func commentFeature(t *testing.T, st *DB, projectID, authorID int64) int64 {
	t.Helper()
	feature, _ := commentThread(t, st, projectID, authorID)
	return feature
}

func commentComplaint(t *testing.T, st *DB, projectID, authorID int64) int64 {
	t.Helper()
	_, complaint := commentThread(t, st, projectID, authorID)
	return complaint
}

// TestABlankCommentIsRefused: comments.body is `TEXT NOT NULL` and nothing else, so
// SQLite accepted ” and '   ' and the endpoint returned 201 for an empty comment.
//
// A thread then carries a row that renders as nothing, and a reader cannot tell an
// empty comment from one that failed to load. That is why the store refuses it rather
// than the UI.
func TestABlankCommentIsRefused(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	feature := commentFeature(t, store, pid, uid)

	for _, body := range []string{"", "   ", "\t", "\n", " \t\n "} {
		if _, err := store.CreateComment(ctx, pid, feature, "feature", uid, body, "", nil); err == nil {
			t.Errorf("a comment with body %q was accepted; an empty comment is not a "+
				"comment", body)
		}
	}

	// And a real one still goes through, so the guard is not just refusing everything.
	if _, err := store.CreateComment(ctx, pid, feature, "feature", uid, "actual", "", nil); err != nil {
		t.Fatalf("a real comment was refused: %v", err)
	}
}

// TestACommentOnAnotherProjectsFeatureIsRefused is the WRITE side of the defect
// id_enumeration_test.go fixed on the read side.
//
// comments.thread_id is a bare INTEGER with no FOREIGN KEY -- the one column in that
// table that references something and does not check it -- so a comment could name a
// feature belonging to a DIFFERENT project and be filed under this one. Two
// consequences, and the second is the worse one: a cross-project write, and a read of
// another project's feature discussion through the wrong project's URL.
func TestACommentOnAnotherProjectsFeatureIsRefused(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	mine := commentFeature(t, store, pid, uid)

	// A second PROJECT with its own feature. Same store on purpose: two stores, two
	// databases, and the ownership question would not arise.
	other, err := store.CreateProject(ctx, uid, "other-comment-project", "Other", "d",
		"collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	theirs := commentFeature(t, store, other.ID, uid)

	if _, err := store.CreateComment(ctx, other.ID, mine, "feature", uid,
		"their project, their feature", "", nil); err == nil {
		t.Error("a comment was filed under the second project naming the FIRST " +
			"project's feature")
	}

	// And the mirror image: this project's URL must not accept a comment about the
	// other project's feature.
	if _, err := store.CreateComment(ctx, pid, theirs, "feature", uid,
		"cross-project", "", nil); err == nil {
		t.Error("a comment was filed under this project naming the SECOND project's " +
			"feature")
	}

	// A thread id that exists nowhere is a 404, not the same error as the wrong
	// project: one is "no such feature", the other is "not yours", and the client can
	// only tell them apart if they are different errors.
	_, err = store.CreateComment(ctx, pid, 99999, "feature", uid, "ghost", "", nil)
	if err == nil {
		t.Error("a comment on a thread that does not exist was accepted")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Errorf("a missing thread gave %q; it should be a not-found, so a client can "+
			"tell it from a thread that belongs to somebody else", err)
	}
}

// TestAReplyMustShareItsParentsThread: parent_id is a real FK to comments, and the
// renderer nests a reply under its parent. A parent from a DIFFERENT thread would make
// the rendered nesting a lie -- a reply appearing under an unrelated comment.
func TestAReplyMustShareItsParentsThread(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	featureA := commentFeature(t, store, pid, uid)
	featureB := commentFeature(t, store, pid, uid)

	parent, err := store.CreateComment(ctx, pid, featureA, "feature", uid,
		"on the first feature", "", nil)
	if err != nil {
		t.Fatalf("CreateComment parent: %v", err)
	}

	// A parent from another thread is refused.
	if _, err := store.CreateComment(ctx, pid, featureB, "feature", uid,
		"reply", "", &parent.ID); err == nil {
		t.Error("a reply was accepted under a parent comment from another thread; " +
			"the rendered nesting would claim they are related")
	}

	// The same thread is fine.
	if _, err := store.CreateComment(ctx, pid, featureA, "feature", uid,
		"a real reply", "", &parent.ID); err != nil {
		t.Errorf("a reply within its own thread was refused: %v", err)
	}

	// A parent that does not exist at all.
	missing := parent.ID + 9999
	if _, err := store.CreateComment(ctx, pid, featureA, "feature", uid,
		"orphan", "", &missing); err == nil {
		t.Error("a reply to a comment that does not exist was accepted")
	}
}

// TestTheCommentKindsMapToRealTables walks the thread_kind CHECK list. Each kind names
// a different table, and threadBelongsToProject is a switch over them -- so a kind
// added to the CHECK without a case would be accepted by isValidThreadKind and then
// fail to resolve a table.
func TestTheCommentKindsMapToRealTables(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	// 'release' is EXCLUDED, and that is the assertion: it is in the CHECK list and
	// there is no releases table, so a comment on one could never resolve a project --
	// which would be a 500 on a valid-looking kind. The test below proves the refusal.
	for _, kind := range []string{"complaint", "feature", "merge_request", "project"} {
		var threadID int64
		switch kind {
		case "complaint":
			threadID = commentComplaint(t, store, pid, uid)
		case "feature":
			threadID = commentFeature(t, store, pid, uid)
		case "merge_request":
			mr, err := store.CreateMergeRequest(ctx, pid, commentFeature(t, store, pid, uid),
				uid, "a merge request")
			if err != nil {
				t.Fatalf("CreateMergeRequest: %v", err)
			}
			threadID = mr.ID
		case "project":
			threadID = pid // the project threads on itself
		}
		if _, err := store.CreateComment(ctx, pid, threadID, kind, uid,
			"a comment on a "+kind, "", nil); err != nil {
			t.Errorf("a comment on thread_kind %q was refused: %v", kind, err)
		}
	}

	// And a kind outside the CHECK is refused, so the switch and the CHECK cannot
	// disagree.
	if _, err := store.CreateComment(ctx, pid, 1, "banana", uid, "b", "", nil); err == nil {
		t.Error("an invalid thread_kind was accepted")
	}

	// 'release' passes isValidThreadKind -- it is IN the CHECK list -- but there is no
	// releases table. Without an explicit case it would reach the query and 500.
	if _, err := store.CreateComment(ctx, pid, 1, "release", uid, "b", "", nil); err == nil {
		t.Error("thread_kind 'release' was accepted; there is no releases table, so " +
			"resolving its project could only fail")
	} else if !strings.Contains(err.Error(), "release") {
		t.Errorf("the release refusal does not mention release: %q", err)
	} else if !errors.Is(err, ErrInvalid) {
		// The distinction that makes this a test rather than a smoke alarm. A missing
		// releases table also produces an error, so asserting only "an error" is passed
		// by a 500 -- which is the failure mode this whole file is about. The refusal
		// must be a CLEAN one: ErrInvalid, not a SQL error.
		t.Errorf("release was refused with %q, which is not ErrInvalid; pointing the "+
			"switch at a nonexistent table would also 'fail' and look correct here",
			err)
	}
}

// TestAProjectCannotThreadOnAnotherProject: the self-thread rule for kind 'project'.
//
// A project is its own owner -- `projects` has no project_id column -- so the ownership
// question is answered without a query, by comparing the thread id to the project id.
// That comparison is a guard, and this is the test for it: without it, a comment filed
// under project 1 could name project 2 as its thread and appear on 2's page.
//
// Found by mutation: neutering `threadID != projectID` left the suite green.
func TestAProjectCannotThreadOnAnotherProject(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	// Its own id: fine.
	if _, err := store.CreateComment(ctx, pid, pid, "project", uid,
		"a note on this project", "", nil); err != nil {
		t.Fatalf("a project comment on itself was refused: %v", err)
	}

	// Another project's id under this project: refused.
	other, err := store.CreateProject(ctx, uid, "other-threaded", "Other", "d",
		"collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := store.CreateComment(ctx, pid, other.ID, "project", uid,
		"not mine", "", nil); err == nil {
		t.Error("a project comment named another project as its thread and was " +
			"filed under this one")
	}
}

// TestAReplyParentMustMatchOnEveryAxis walks each condition of the parent check
// SEPARATELY, because the check has three terms and a fixture that varies all three at
// once cannot tell which one is load-bearing.
//
// Found by mutation: weakening the condition to `parentThread != threadID` left the
// suite green. Every earlier fixture varied the THREAD and left kind and project equal,
// so only one term was ever exercised -- and the cross-kind case below failed for the
// wrong reason (a different thread), which is exactly what let the mutant survive.
//
// Each block below changes ONE axis and must be refused:
//
//	thread  same thread, different kind, and same kind/different thread
//	kind    same thread AND same project, but a parent of another thread_kind
//	project same thread, same kind, but a parent belonging to another project
func TestAReplyParentMustMatchOnEveryAxis(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	// A COMPLAINT thread and a FEATURE thread in the same project. Comments are made on
	// both, so a parent of either kind exists to be misused.
	feature := commentFeature(t, store, pid, uid)
	complaint := commentComplaint(t, store, pid, uid)

	featureComment, err := store.CreateComment(ctx, pid, feature, "feature", uid,
		"a feature comment", "", nil)
	if err != nil {
		t.Fatalf("CreateComment on the feature: %v", err)
	}
	complaintComment, err := store.CreateComment(ctx, pid, complaint, "complaint",
		uid, "a complaint comment", "", nil)
	if err != nil {
		t.Fatalf("CreateComment on the complaint: %v", err)
	}

	// AXIS: kind. Same thread, same project, but the parent is a FEATURE comment.
	//
	// The thread id is the FEATURE's id, and thread_kind is "feature" on the reply
	// while the parent is a complaint -- no: read it carefully. To vary ONLY the kind,
	// the reply must be posted to the SAME complaint thread with a parent from the
	// feature thread, which varies the thread too.
	//
	// The obvious case: a feature comment as the parent of a complaint reply.
	if _, err := store.CreateComment(ctx, pid, complaint, "complaint", uid,
		"cross-kind", "", &featureComment.ID); err == nil {
		t.Error("a feature comment was accepted as the parent of a complaint reply")
	}

	// AXIS: thread. A complaint reply under a complaint comment from ANOTHER complaint
	// thread -- kind and project both match.
	otherComplaint := commentComplaint(t, store, pid, uid)
	otherComment, err := store.CreateComment(ctx, pid, otherComplaint, "complaint",
		uid, "a comment elsewhere", "", nil)
	if err != nil {
		t.Fatalf("CreateComment on the second complaint: %v", err)
	}
	if _, err := store.CreateComment(ctx, pid, complaint, "complaint", uid,
		"cross-thread", "", &otherComment.ID); err == nil {
		t.Error("a comment from another complaint thread was accepted as a parent")
	}

	// AXIS: project. The parent belongs to ANOTHER project, and its thread_id is
	// forced to be the SAME number as ours, so only project_id differs.
	//
	// Without the force, the thread term alone refuses it and the project term is never
	// exercised -- which is exactly what mutation showed: dropping `parentProject`
	// left the suite green while dropping `parentThread` went red. A guard that is
	// only ever caught by a neighbouring guard is not a guard.
	other, err := store.CreateProject(ctx, uid, "other-parent", "Other", "d",
		"collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	theirFeature := commentFeature(t, store, other.ID, uid)
	theirComment, err := store.CreateComment(ctx, other.ID, theirFeature, "feature",
		uid, "their comment", "", nil)
	if err != nil {
		t.Fatalf("CreateComment in the other project: %v", err)
	}
	if _, err := store.ExecContext(ctx,
		`UPDATE comments SET thread_id=? WHERE id=?`, feature, theirComment.ID); err != nil {
		t.Fatalf("align the foreign parent's thread_id: %v", err)
	}
	if _, err := store.CreateComment(ctx, pid, feature, "feature", uid,
		"borrowed parent", "", &theirComment.ID); err == nil {
		t.Error("a comment from another project was accepted as a parent, even with " +
			"its thread_id matching; only the project check can refuse it")
	}

	// AXIS: kind alone, and this one needs a row CreateComment could not have made.
	//
	// Every other fixture varies the THREAD, because a comment's thread_kind follows
	// from its thread_id: threadBelongsToProject resolves complaint->complaints,
	// feature->features, so two comments sharing a thread_id necessarily share a kind.
	// The kind term is therefore unreachable through this API -- which is exactly why
	// mutation showed it as "not load-bearing" while `parentThread != threadID` alone
	// also passed.
	//
	// It still has to be checked, because the row can arrive another way: a migration,
	// a direct INSERT, a future writer that forgets the rule. So it is seeded as SQL --
	// the one thing the guard exists to survive -- and asserted.
	mismatch := int64(90001)
	if _, err := store.ExecContext(ctx, `
		INSERT INTO comments (project_id, thread_kind, thread_id, author_id, body,
			score, created_at)
		VALUES (?, 'feature', ?, ?, 'a row whose kind contradicts its thread', 0, ?)`,
		pid, complaint, uid, float64(1)); err != nil {
		t.Fatalf("seed a kind-mismatched comment: %v", err)
	}
	if _, err := store.ExecContext(ctx,
		`UPDATE comments SET id=? WHERE thread_id=? AND thread_kind='feature'`,
		mismatch, complaint); err != nil {
		t.Fatalf("pin the seeded id: %v", err)
	}

	// Its thread_id and project_id both MATCH this reply's. Only the kind differs, so
	// dropping the kind term from the check lets this through.
	if _, err := store.CreateComment(ctx, pid, complaint, "complaint", uid,
		"nesting under a kind-mismatched row", "", &mismatch); err == nil {
		t.Error("a comment whose thread_kind contradicts its thread_id was accepted " +
			"as a parent; the schema permits such a row and only the kind term " +
			"refuses it")
	}

	// And the three matching cases still work, so the check is not simply refusing
	// every reply.
	if _, err := store.CreateComment(ctx, pid, feature, "feature", uid,
		"reply to the feature comment", "", &featureComment.ID); err != nil {
		t.Errorf("a reply within its own feature thread was refused: %v", err)
	}
	if _, err := store.CreateComment(ctx, pid, complaint, "complaint", uid,
		"reply to the complaint comment", "", &complaintComment.ID); err != nil {
		t.Errorf("a reply within its own complaint thread was refused: %v", err)
	}
}
