package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// The queue must list what exists, shape the fields the page renders, narrow on filters, and
// refuse a cursor it cannot parse -- while refusing a vote from nobody.
func TestCurationQueueReadsAndVotesAreGated(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	slug, pid := makeProject(t, ts, "curation-project")
	ids := makeRankedPair(t, ts, slug, pid, "An idea", "Another idea")
	ctx := context.Background()

	owner, err := db.GetUser(ctx, "testuser")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if _, err := db.ProposeStrategicWeight(ctx, pid, ids[0], owner.ID, 2.0, "because"); err != nil {
		t.Fatalf("ProposeStrategicWeight: %v", err)
	}

	resp, body := doJSON(t, ts, "GET", "/api/v1/curation", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/curation: %d body=%v", resp.StatusCode, body)
	}
	items, _ := body["items"].([]any)
	if len(items) == 0 {
		t.Fatal("the queue is empty although a proposal exists")
	}
	first, _ := items[0].(map[string]any)
	for _, field := range []string{"kind", "id", "action", "target", "summary", "state", "yes", "no", "my_vote", "opened_at"} {
		if _, ok := first[field]; !ok {
			t.Fatalf("the row has no %q: the page cannot render it", field)
		}
	}

	// A filter narrows without failing, and a filter that matches nothing is an empty list
	// rather than an error.
	resp, body = doJSON(t, ts, "GET", "/api/v1/curation?kind=strategy&state=pending", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("filtered read: %d", resp.StatusCode)
	}
	for _, it := range body["items"].([]any) {
		if got := it.(map[string]any)["kind"]; got != "strategy" {
			t.Fatalf("the kind filter returned %v", got)
		}
	}

	// A malformed cursor is a 400: answering with the first page would turn a paging bug into
	// an infinite scroll.
	resp, _ = doJSON(t, ts, "GET", "/api/v1/curation?cursor=nonsense", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed cursor: %d, want 400", resp.StatusCode)
	}

	// Nobody cannot vote, whatever the kind.
	if code, raw := authJSON(t, ts, "POST", "/api/v1/curation/taxonomy/1/vote", "", map[string]any{"vote": "yes"}); code != http.StatusUnauthorized {
		t.Fatalf("anonymous vote: %d body=%s, want 401", code, raw)
	}
}

// An act review is reviewer work: it writes a judgement about another person's decision, so it
// takes the standing that decision took. This is the security property of the whole route.
func TestCurationVoteOnAnActNeedsStanding(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	slug, pid := makeProject(t, ts, "curation-stand")
	ids := makeRankedPair(t, ts, slug, pid, "Another idea")
	ctx := context.Background()

	owner, err := db.GetUser(ctx, "testuser")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	arena, err := db.EnsureArena(ctx, store.ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	if err := db.UpsertArenaEntry(ctx, arena.ID, "feature", ids[0]); err != nil {
		t.Fatalf("UpsertArenaEntry: %v", err)
	}
	act, err := db.RemoveFeatureFromArena(ctx, pid, arena.ID, ids[0], owner.ID, "spam")
	if err != nil {
		t.Fatalf("RemoveFeatureFromArena: %v", err)
	}
	path := "/api/v1/curation/act/" + strconv.FormatInt(act.ID, 10) + "/vote"

	stranger, err := db.CreateUser(ctx, "curation-stranger", "Stranger")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.SetPassword(ctx, stranger.Username, "stranger-password-123"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	token := loginAs(t, db, stranger.Username, "stranger-password-123")

	if code, raw := authJSON(t, ts, "POST", path, token, map[string]any{"vote": "yes"}); code != http.StatusForbidden {
		t.Fatalf("stranger reviewing an act: %d body=%s, want 403", code, raw)
	}
	if err := db.SetTrustLevel(ctx, stranger.ID, 1, owner.ID, "test"); err != nil {
		t.Fatalf("SetTrustLevel: %v", err)
	}
	if code, raw := authJSON(t, ts, "POST", path, token, map[string]any{"vote": "yes"}); code != http.StatusOK {
		t.Fatalf("promoted reviewer: %d body=%s, want 200", code, raw)
	}

	// The actor cannot review their own act, however trusted. The harness user is the actor.
	ownToken := loginAs(t, db, "testuser", "test-password-123")
	if err := db.SetTrustLevel(ctx, owner.ID, 1, owner.ID, "test"); err != nil {
		t.Fatalf("SetTrustLevel(owner): %v", err)
	}
	if code, raw := authJSON(t, ts, "POST", path, ownToken, map[string]any{"vote": "no"}); code != http.StatusForbidden {
		t.Fatalf("self review: %d body=%s, want 403", code, raw)
	}

	// And the review is visible in the queue as a count, not as a change of state.
	_, body := doJSON(t, ts, "GET", "/api/v1/curation?kind=act", nil)
	var found bool
	for _, it := range body["items"].([]any) {
		row := it.(map[string]any)
		if int64(row["id"].(float64)) != act.ID {
			continue
		}
		found = true
		if row["state"] != "applied" {
			t.Fatalf("a contested act changed state to %v: a review applies nothing", row["state"])
		}
		if row["yes"].(float64) != 1 {
			t.Fatalf("yes is %v, want 1 after an endorsement", row["yes"])
		}
	}
	if !found {
		t.Fatal("the act is missing from the queue")
	}
}
