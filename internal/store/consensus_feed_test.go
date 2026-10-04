package store

import (
	"context"
	"testing"
)

// ListConsensusCallsForFeed and DisplayNamesFor, from docs/plans/feeds.md step F1.
//
// The nullable-column tests exist because the first version of the SELECT scanned
// feature_id into a bare int64, and every §6.6 process decision stores NULL there. The
// read then failed with "converting NULL to int64 is unsupported" -- a 500 that took a
// project's ENTIRE consensus feed down, not just the process decisions. Every nullable
// column gets its own case below.

// seedFeedCalls creates `n` calls for a project and returns their ids, oldest first.
func seedFeedCalls(t *testing.T, db *DB, pid int64, n int) []int64 {
	t.Helper()
	ctx := context.Background()
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		c, err := db.CreateConsensusCall(ctx, pid, 0, 1, "call", "")
		if err != nil {
			t.Fatalf("CreateConsensusCall(%d): %v", i, err)
		}
		ids = append(ids, c.ID)
	}
	return ids
}

func TestListConsensusCallsForFeedReturnsNewestFirst(t *testing.T) {
	db, _, pid := setupWithProject(t)
	seedFeedCalls(t, db, pid, 3)

	got, err := db.ListConsensusCallsForFeed(context.Background(), pid, 50)
	if err != nil {
		t.Fatalf("ListConsensusCallsForFeed: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d calls, want 3", len(got))
	}
	// A feed in arbitrary order is a feed nobody reads, so the order is the claim.
	for i := 1; i < len(got); i++ {
		if got[i-1].OpensAt < got[i].OpensAt {
			t.Errorf("call %d is older than call %d; the feed is not newest-first",
				i-1, i)
		}
	}
}

// A process decision -- an emergency-hold confirmation, a charter amendment -- has no
// feature to attach to, and the column is NULL for exactly that case.
func TestListConsensusCallsForFeedHandlesAPolicyDecisionWithNoFeature(t *testing.T) {
	db, author, pid := setupWithProject(t)
	ctx := context.Background()

	c, err := db.CreateConsensusCall(ctx, pid, 0, author, "Amend the charter?", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	// NULL feature_id, straight SQL: no store method writes one, because the Go API takes
	// an int64 and has no way to express NULL.
	if _, err := db.ExecContext(ctx,
		`UPDATE consensus_calls SET feature_id = NULL WHERE id = ?`, c.ID); err != nil {
		t.Fatalf("clearing feature_id: %v", err)
	}

	got, err := db.ListConsensusCallsForFeed(ctx, pid, 50)
	if err != nil {
		t.Fatalf("a process decision breaks the feed read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d calls, want 1", len(got))
	}
	if got[0].FeatureID != 0 {
		t.Errorf("FeatureID=%d for a NULL feature_id, want 0", got[0].FeatureID)
	}
}

func TestListConsensusCallsForFeedClampsTheLimit(t *testing.T) {
	db, _, pid := setupWithProject(t)
	ctx := context.Background()
	seedFeedCalls(t, db, pid, 3)

	// Zero and negative mean "the default", not "no rows".
	for _, limit := range []int{0, -5} {
		got, err := db.ListConsensusCallsForFeed(ctx, pid, limit)
		if err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if len(got) != 3 {
			t.Errorf("limit %d returned %d calls, want all 3", limit, len(got))
		}
	}
	// A real limit is honoured.
	got, err := db.ListConsensusCallsForFeed(ctx, pid, 2)
	if err != nil {
		t.Fatalf("limit 2: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("limit 2 returned %d calls, want 2", len(got))
	}
}

func TestDisplayNamesForResolvesInOneQueryAndNeverReturnsAnAddress(t *testing.T) {
	db, author, _ := setupWithProject(t)
	ctx := context.Background()

	got, err := db.DisplayNamesFor(ctx, []int64{author, author, 99999})
	if err != nil {
		t.Fatalf("DisplayNamesFor: %v", err)
	}
	// setupWithUser creates the account with username "testuser" and display name
	// "Test User". Asserted on the real value, not a guessed one.
	if got[author] != "Test User" {
		t.Errorf("display_name=%q, want %q", got[author], "Test User")
	}
	// An unknown id is absent rather than an empty string with a username in it: the
	// caller decides what to omit, and a fabricated fallback here would leak.
	if _, present := got[99999]; present {
		t.Error("an unknown id produced a name; that is how a username leaks into a feed")
	}
	// No ids at all is not an error, and must not build an "IN ()" query.
	if got, err := db.DisplayNamesFor(ctx, nil); err != nil || len(got) != 0 {
		t.Errorf("DisplayNamesFor(nil) = %v, %v; want an empty map and no error", got, err)
	}
}
