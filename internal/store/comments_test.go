package store

import (
	"context"
	"testing"
)

func TestCreateAndGetComment(t *testing.T) {
	store, _, pid := setupWithProject(t)
	ctx := context.Background()
	comment, err := store.CreateComment(ctx, pid, 1, "feature", 1, "This needs work", "objection", nil)
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
	store, _, pid := setupWithProject(t)
	ctx := context.Background()
	// Create two comments in the same thread
	_, err := store.CreateComment(ctx, pid, 1, "feature", 1, "First comment", "question", nil)
	if err != nil {
		t.Fatalf("CreateComment 1: %v", err)
	}
	_, err = store.CreateComment(ctx, pid, 1, "feature", 1, "Second comment", "support", nil)
	if err != nil {
		t.Fatalf("CreateComment 2: %v", err)
	}
	// Create comment in different thread
	_, err = store.CreateComment(ctx, pid, 2, "feature", 1, "Different thread", "evidence", nil)
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
	store, _, pid := setupWithProject(t)
	ctx := context.Background()
	comment, err := store.CreateComment(ctx, pid, 1, "feature", 1, "To delete", "offtopic", nil)
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
	store, _, pid := setupWithProject(t)
	ctx := context.Background()
	comment, err := store.CreateComment(ctx, pid, 1, "feature", 1, "Vote me", "question", nil)
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
	store, _, pid := setupWithProject(t)
	ctx := context.Background()
	parent, err := store.CreateComment(ctx, pid, 1, "feature", 1, "Parent", "question", nil)
	if err != nil {
		t.Fatalf("CreateComment parent: %v", err)
	}

	childID := parent.ID
	child, err := store.CreateComment(ctx, pid, 1, "feature", 1, "Child reply", "objection", &childID)
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

	// The ordinary case: no label. This is what every reader posting a comment
	// produces, because Label is omitempty.
	if _, err := store.CreateComment(ctx, pid, 1, "feature", uid, "no label here", "", nil); err != nil {
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

	if _, err := store.CreateComment(ctx, pid, 1, "feature", uid, "needs evidence", "objection", nil); err != nil {
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
