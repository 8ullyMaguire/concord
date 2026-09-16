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
