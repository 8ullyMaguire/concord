package httpapi

// The inbox at the edge (docs/specs/notification-spec.md §5). The store tests prove the rows are
// written for the right person; these prove the ROUTES cannot be turned into a way to read
// somebody else's, and that the header badge is the reader's own count.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// notifRouteFixture: a project with one feature in an arena, its author, and a moderator who
// removes it — so the author has exactly one real notification produced by the real path,
// rather than by an INSERT this test invented.
func notifRouteFixture(t *testing.T, slug string) (ts *httptest.Server, st *store.DB, authorTok, moderatorTok string, authorID int64) {
	t.Helper()
	ts, st = newTestServerWithStore(t)

	author := registerAndLogin(t, ts, slug+"-author")
	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", author,
		map[string]any{"slug": slug, "name": slug, "description": "the inbox"})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create project: %d %s", code, body)
	}
	pid := bodyInt64(t, body, "id")
	cid := fileComplaint(t, ts, author, slug, pid, "the exporter drops the last row")
	if err := st.ValidateComplaint(context.Background(), cid); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	code, body = authJSON(t, ts, http.MethodPost, "/api/v1/projects/"+slug+"/features", author,
		map[string]any{"project_id": pid, "title": "Make the export complete", "body": "rows missing",
			"effort": "M", "linked_complaints": []int64{cid}})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create feature: %d %s", code, body)
	}
	fid := bodyInt64(t, body, "id")

	authorID = bodyInt64(t, mustProfile(t, ts, author), "id")
	// Both are project members; the moderator is a second person so the notification is not a
	// self-decision (notifyTx skips those).
	moderator := registerAndLogin(t, ts, slug+"-mod")
	arena, err := st.EnsureArena(context.Background(), store.ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	if err := st.UpsertArenaEntry(context.Background(), arena.ID, "feature", fid); err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	// The MODERATOR acts, not the author: notifyTx skips a self-decision, so removing your own
	// feature would leave this fixture with nothing to test.
	moderatorID := bodyInt64(t, mustProfile(t, ts, moderator), "id")
	if _, err := st.RemoveFeatureFromArena(context.Background(), pid, arena.ID, fid, moderatorID, "duplicate of #12"); err != nil {
		t.Fatalf("RemoveFeatureFromArena: %v", err)
	}
	return ts, st, author, moderator, authorID
}

// mustProfile reads the signed-in user's own profile, for its id.
func mustProfile(t *testing.T, ts *httptest.Server, token string) []byte {
	t.Helper()
	code, body := authGet(t, ts, "/api/v1/auth/me", token)
	if code != http.StatusOK {
		t.Fatalf("auth/me: %d %s", code, body)
	}
	return body
}

func TestTheInboxIsOnlyEverYourOwn(t *testing.T) {
	ts, _, authorTok, moderatorTok, _ := notifRouteFixture(t, "inbox-own")

	code, body := authGet(t, ts, "/api/v1/notifications", authorTok)
	if code != http.StatusOK {
		t.Fatalf("the author's inbox: %d %s", code, body)
	}
	m := decodedBody(t, body)
	list, _ := m["notifications"].([]any)
	if len(list) != 1 {
		t.Fatalf("the author has %d notifications, want 1: %s", len(list), body)
	}
	id := int64(list[0].(map[string]any)["id"].(float64))

	// The moderator decided it: their own inbox is empty and the badge would be absent.
	code, body = authGet(t, ts, "/api/v1/notifications", moderatorTok)
	if code != http.StatusOK {
		t.Fatalf("the moderator's inbox: %d %s", code, body)
	}
	if theirs, _ := decodedBody(t, body)["notifications"].([]any); len(theirs) != 0 {
		t.Fatalf("the moderator was notified of their own decision: %s", body)
	}

	// And the moderator cannot mark the author's notification read, or learn that it exists.
	code, body = authJSON(t, ts, http.MethodPost,
		"/api/v1/notifications/"+strconv.FormatInt(id, 10)+"/read", moderatorTok, nil)
	if code != http.StatusNotFound {
		t.Fatalf("a stranger marking another reader's notification: %d %s, want 404", code, body)
	}
	// Unchanged, not merely refused.
	_, body = authGet(t, ts, "/api/v1/notifications", authorTok)
	if n := decodedBody(t, body)["unread"]; n != float64(1) {
		t.Fatalf("unread = %v after a stranger's attempt, want 1", n)
	}

	// Anonymous is 401, not an empty inbox: an empty answer would read as "you have none".
	code, _ = authGet(t, ts, "/api/v1/notifications", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous read answered %d, want 401", code)
	}
}

func TestTheHeaderBadgeIsTheReadersOwnCount(t *testing.T) {
	ts, _, authorTok, _, _ := notifRouteFixture(t, "inbox-badge")

	code, body := authGet(t, ts, "/notifications", authorTok)
	if code != http.StatusOK {
		t.Fatalf("page: %d %s", code, body)
	}
	if !strings.Contains(string(body), `data-unread-count="1"`) {
		t.Fatalf("the header does not show one unread notification")
	}

	// Anonymous: the link is there (the page says "sign in") and no badge is.
	code, body = authGet(t, ts, "/notifications", "")
	if code != http.StatusOK {
		t.Fatalf("anonymous page: %d %s", code, body)
	}
	if strings.Contains(string(body), "data-unread-count") {
		t.Fatal("an anonymous reader was shown an unread count")
	}
	if !strings.Contains(string(body), "Sign in to see your notifications") {
		t.Fatal("the signed-out page does not say to sign in")
	}
}

func TestMarkingAllReadClearsTheBadge(t *testing.T) {
	ts, _, authorTok, _, _ := notifRouteFixture(t, "inbox-clear")

	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/notifications/read-all", authorTok, nil)
	if code != http.StatusOK {
		t.Fatalf("read-all: %d %s", code, body)
	}
	if m := decodedBody(t, body); m["marked"] != float64(1) {
		t.Fatalf("marked %v, want 1", m["marked"])
	}

	// The badge is gone from the next paint, which is the only place a reader sees it.
	_, body = authGet(t, ts, "/notifications", authorTok)
	if strings.Contains(string(body), "data-unread-count") {
		t.Fatal("the badge survived mark-all")
	}
	// Marking again is a 200 that clears nothing, not an error.
	code, body = authJSON(t, ts, http.MethodPost, "/api/v1/notifications/read-all", authorTok, nil)
	if code != http.StatusOK {
		t.Fatalf("second read-all: %d %s", code, body)
	}
	if m := decodedBody(t, body); m["marked"] != float64(0) {
		t.Fatalf("second read-all marked %v, want 0", m["marked"])
	}
}
