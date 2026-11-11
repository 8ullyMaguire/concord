package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// The refusal is the security property, so it is asserted before the permitted path: a test
// that only proves the happy path passes on a handler with no gate at all.
func TestArenaRemovalNeedsTrustOrRole(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	slug, pid := makeProject(t, ts, "mod-project")
	ids := makeRankedPair(t, ts, slug, pid, "An idea that will be moderated")
	featureID := ids[0]

	ctx := context.Background()
	arena, err := db.EnsureArena(ctx, store.ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	if err := db.UpsertArenaEntry(ctx, arena.ID, "feature", featureID); err != nil {
		t.Fatalf("UpsertArenaEntry: %v", err)
	}

	path := "/api/v1/projects/" + slug + "/moderation/arena/remove"
	body := map[string]any{
		"feature_id": featureID, "arena_id": arena.ID,
		"reason": "spam: repeated link drops", "confirm": true,
	}

	// A caller with no identity at all.
	resp, _ := doJSONAnon(t, ts, "POST", path, body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous removal: %d, want 401", resp.StatusCode)
	}

	// A signed-in stranger: no role in the project and trust 0.
	stranger, err := db.CreateUser(ctx, "stranger-mod", "Stranger")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.SetPassword(ctx, stranger.Username, "stranger-password-123"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	token := loginAs(t, db, stranger.Username, "stranger-password-123")

	code, raw := authJSON(t, ts, "POST", path, token, body)
	if code != http.StatusForbidden {
		t.Fatalf("stranger removal: %d body=%s, want 403", code, raw)
	}
	if !contains(string(raw), "reviewer") {
		t.Fatalf("the refusal must name the role it wanted: %s", raw)
	}

	// The gate is role OR trust, so promotion alone -- with no role in the project at all --
	// must open it. This is the half the owner asked for ("as a high-trust level user").
	actor, err := db.GetUser(ctx, "testuser")
	if err != nil {
		t.Fatalf("GetUser(testuser): %v", err)
	}
	if err := db.SetTrustLevel(ctx, stranger.ID, 1, actor.ID, "test: promoted to moderate"); err != nil {
		t.Fatalf("SetTrustLevel: %v", err)
	}
	code, raw = authJSON(t, ts, "POST", path, token, body)
	if code != http.StatusCreated {
		t.Fatalf("promoted removal: %d body=%s, want 201", code, raw)
	}

	// No confirmation, no act -- for the script as much as the browser.
	unconfirmed := map[string]any{"feature_id": featureID, "arena_id": arena.ID, "reason": "x"}
	if code, _ := authJSON(t, ts, "POST", path, token, unconfirmed); code != http.StatusBadRequest {
		t.Fatalf("unconfirmed removal: %d, want 400", code)
	}
	// A blank reason is refused with the crate's sentence, not a generic one.
	blank := map[string]any{"feature_id": featureID, "arena_id": arena.ID, "reason": "   ", "confirm": true}
	code, raw = authJSON(t, ts, "POST", path, token, blank)
	if code != http.StatusBadRequest || !contains(string(raw), "requires a reason") {
		t.Fatalf("blank reason: %d body=%s, want 400 with the reason sentence", code, raw)
	}

	// And the reversal puts the entry back.
	entries := projectAudit(t, ts, slug)
	var removal int64
	for _, e := range entries {
		if e["action"] == "arena.remove" {
			removal = int64(e["id"].(float64))
		}
	}
	if removal == 0 {
		t.Fatal("the act is not in the log")
	}
	restorePath := "/api/v1/projects/" + slug + "/moderation/arena/restore"
	restore := map[string]any{"undoes": removal, "reason": "the removal was mistaken", "confirm": true}
	code, raw = authJSON(t, ts, "POST", restorePath, token, restore)
	if code != http.StatusOK {
		t.Fatalf("restore: %d body=%s, want 200", code, raw)
	}
	if _, err := db.GetArenaEntry(ctx, arena.ID, "feature", featureID); err != nil {
		t.Fatalf("the entry did not come back: %v", err)
	}
	// Undoing twice is refused: the log is append-only and the second reversal would insert
	// an entry nobody removed.
	if code, _ := authJSON(t, ts, "POST", restorePath, token, restore); code != http.StatusBadRequest {
		t.Fatalf("double restore: %d, want 400", code)
	}
}

// projectAudit reads the project's audit rows through the API, because the page reads them
// that way and the id shape it produces is what the undo control posts back.
func projectAudit(t *testing.T, ts *httptest.Server, slug string) []map[string]any {
	t.Helper()
	resp, body := doJSON(t, ts, "GET", "/api/v1/projects/"+slug+"/audit?action=arena.remove", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit: %d body=%v", resp.StatusCode, body)
	}
	items, _ := body["entries"].([]any)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
