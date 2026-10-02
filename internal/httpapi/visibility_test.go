package httpapi

// Visibility at the HTTP boundary.
//
// The store tests prove the access rule. These prove that the rule is actually
// reached on every route that carries project data, because the failure mode
// that matters is a handler that loads a project by slug and forgets to ask:
// the store's CanAccessProject would be correct and unused.
//
// The status code is part of the contract. A refusal is 404, never 403, so an
// instance cannot be enumerated by comparing responses.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// registerAndLogin returns an Authorization header value for a fresh account.
func registerAndLogin(t *testing.T, ts *httptest.Server, username string) string {
	t.Helper()
	body := mustJSON(t, map[string]any{
		"username": username, "password": "correct-horse-battery", "display_name": username,
	})
	resp, err := ts.Client().Post(ts.URL+"/api/v1/auth/register", "application/json",
		bytes.NewReader(body))
	if err != nil {
		t.Fatalf("register %s: %v", username, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register %s: status %d", username, resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode register: %v", err)
	}
	tok, _ := out["token"].(string)
	if tok == "" {
		t.Fatalf("register %s returned no token: %v", username, out)
	}
	return "Bearer " + tok
}

// anonymousMarker is what "no Authorization header" means on this harness.
// newTestServer wraps the router so any request arriving without an
// Authorization header is given testuser's identity; passing "" is therefore
// the MOST privileged thing a test can do, not the least. The X-No-Auth
// sentinel is the documented opt-out, and using it wrong is how the anonymous
// paths here initially "passed" a stranger's token.
const anonymousMarker = "anonymous"

func isAnon(auth string) bool { return auth == "" || auth == anonymousMarker }

func authGet(t *testing.T, ts *httptest.Server, path, auth string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if isAnon(auth) {
		req.Header.Set("X-No-Auth", "1")
	} else {
		req.Header.Set("Authorization", auth)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

func authJSON(t *testing.T, ts *httptest.Server, method, path, auth string, payload any) (int, []byte) {
	t.Helper()
	var rdr *bytes.Reader
	if payload != nil {
		rdr = bytes.NewReader(mustJSON(t, payload))
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if isAnon(auth) {
		req.Header.Set("X-No-Auth", "1")
	} else {
		req.Header.Set("Authorization", auth)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

func setVisibility(t *testing.T, ts *httptest.Server, auth, slug, visibility string) {
	t.Helper()
	code, body := authJSON(t, ts, http.MethodPut,
		"/api/v1/projects/"+slug+"/visibility", auth,
		map[string]any{"visibility": visibility})
	if code != http.StatusOK {
		t.Fatalf("set %s=%s: status %d body %s", slug, visibility, code, body)
	}
}

// TestAnonymousListHidesPrivateProjects is the core guarantee: the public
// project list is exactly the public set.
func TestAnonymousListHidesPrivateProjects(t *testing.T) {
	ts := newTestServer(t)
	owner := registerAndLogin(t, ts, "owner")

	for _, slug := range []string{"vis-public", "vis-private", "vis-protected", "vis-unlisted"} {
		createTestProjectWithAuth(t, ts, owner, slug)
	}
	setVisibility(t, ts, owner, "vis-private", "private")
	setVisibility(t, ts, owner, "vis-protected", "protected")
	setVisibility(t, ts, owner, "vis-unlisted", "unlisted")

	code, body := authGet(t, ts, "/api/v1/projects", "")
	if code != http.StatusOK {
		t.Fatalf("list: status %d", code)
	}
	for _, slug := range []string{"vis-private", "vis-protected", "vis-unlisted"} {
		if strings.Contains(string(body), `"`+slug+`"`) {
			t.Errorf("anonymous project list leaked %s", slug)
		}
	}
	if !strings.Contains(string(body), `"vis-public"`) {
		t.Error("anonymous project list is missing the public project")
	}
}

// TestOwnerSeesOwnPrivateProjectsInList: private must not mean invisible to its
// owner, or the setting would make a project unmanageable from the UI.
func TestOwnerSeesOwnPrivateProjectsInList(t *testing.T) {
	ts := newTestServer(t)
	owner := registerAndLogin(t, ts, "owner")
	createTestProjectWithAuth(t, ts, owner, "mine")
	setVisibility(t, ts, owner, "mine", "private")

	code, body := authGet(t, ts, "/api/v1/projects", owner)
	if code != http.StatusOK {
		t.Fatalf("list: status %d", code)
	}
	if !strings.Contains(string(body), `"mine"`) {
		t.Error("the owner cannot see their own private project in the list")
	}
}

// TestPrivateProjectIsInvisibleToNonMembers walks every project-scoped surface
// and requires the same answer: 404. A private project's documents are the
// content most worth protecting, so that route is in the list explicitly.
func TestPrivateProjectIsInvisibleToNonMembers(t *testing.T) {
	ts := newTestServer(t)
	owner := registerAndLogin(t, ts, "owner")
	stranger := registerAndLogin(t, ts, "stranger")
	anon := anonymousMarker

	createTestProjectWithAuth(t, ts, owner, "hidden")
	setVisibility(t, ts, owner, "hidden", "private")
	// A document, so the documents routes have something to refuse.
	authJSON(t, ts, http.MethodPut, "/api/v1/projects/hidden/documents", owner,
		map[string]any{"kind": "spec", "slug": "s", "title": "S", "body": "secret"})

	paths := []string{
		"/api/v1/projects/hidden",
		"/api/v1/projects/hidden/charter",
		"/api/v1/projects/hidden/features",
		"/api/v1/projects/hidden/complaints",
		"/api/v1/projects/hidden/board",
		"/api/v1/projects/hidden/members",
		"/api/v1/projects/hidden/documents",
		"/api/v1/projects/hidden/tags",
		"/projects/hidden",
		"/projects/hidden/board",
		"/projects/hidden/rank",
	}
	for _, who := range []struct {
		name string
		auth string
	}{{"anonymous", anon}, {"stranger", stranger}} {
		for _, p := range paths {
			t.Run(who.name+" "+p, func(t *testing.T) {
				code, body := authGet(t, ts, p, who.auth)
				if code != http.StatusNotFound {
					t.Errorf("GET %s as %s: got %d, want 404 (body %s)",
						p, who.name, code, body)
				}
				if bytes.Contains(body, []byte("secret")) {
					t.Errorf("GET %s as %s leaked document content", p, who.name)
				}
			})
		}
	}

	// The owner sees all of it.
	code, body := authGet(t, ts, "/api/v1/projects/hidden", owner)
	if code != http.StatusOK {
		t.Fatalf("owner GET project: got %d, want 200 (%s)", code, body)
	}
}

// TestUnlistedIsReadableWithoutASession pins the level's actual promise: the URL
// is the capability. Anyone asserting otherwise later is changing the level.
func TestUnlistedIsReadableWithoutASession(t *testing.T) {
	ts := newTestServer(t)
	owner := registerAndLogin(t, ts, "owner")
	createTestProjectWithAuth(t, ts, owner, "quiet")
	setVisibility(t, ts, owner, "quiet", "unlisted")

	code, _ := authGet(t, ts, "/api/v1/projects/quiet", "")
	if code != http.StatusOK {
		t.Errorf("unlisted project as anonymous: got %d, want 200", code)
	}
}

// TestSearchHidesPrivateProjects: search is a second path to the same data, and
// a private project found by name is just as leaked as one found in the list.
func TestSearchHidesPrivateProjects(t *testing.T) {
	ts := newTestServer(t)
	owner := registerAndLogin(t, ts, "owner")
	createTestProjectWithAuth(t, ts, owner, "findme-private")
	createTestProjectWithAuth(t, ts, owner, "findme-public")
	setVisibility(t, ts, owner, "findme-private", "private")

	code, body := authGet(t, ts, "/api/v1/search?q=findme", "")
	if code != http.StatusOK {
		t.Fatalf("search: status %d (%s)", code, body)
	}
	if strings.Contains(string(body), "findme-private") {
		t.Error("anonymous search leaked a private project")
	}
	if !strings.Contains(string(body), "findme-public") {
		t.Error("anonymous search is missing the public project")
	}

	// The owner can find their own.
	code, body = authGet(t, ts, "/api/v1/search?q=findme", owner)
	if code != http.StatusOK {
		t.Fatalf("owner search: status %d", code)
	}
	if !strings.Contains(string(body), "findme-private") {
		t.Error("the owner cannot find their own private project")
	}
}

// TestNonMemberCannotChangeVisibility: the setting must not be settable by
// anyone who merely has an account.
func TestNonMemberCannotChangeVisibility(t *testing.T) {
	ts := newTestServer(t)
	owner := registerAndLogin(t, ts, "owner")
	stranger := registerAndLogin(t, ts, "stranger")
	createTestProjectWithAuth(t, ts, owner, "guarded")
	// Start from private. Asserting that the value is still "public" after
	// asking a stranger to set it to "public" proves nothing -- it would pass
	// whether or not the write landed. The interesting direction is a stranger
	// trying to UNhide a project.
	setVisibility(t, ts, owner, "guarded", "private")

	code, _ := authJSON(t, ts, http.MethodPut, "/api/v1/projects/guarded/visibility",
		stranger, map[string]any{"visibility": "public"})
	if code != http.StatusNotFound {
		t.Errorf("stranger changing visibility: got %d, want 404", code)
	}

	// And it must still be private.
	code, body := authGet(t, ts, "/api/v1/projects/guarded", owner)
	if code != http.StatusOK {
		t.Fatalf("owner read: %d", code)
	}
	if got := projectVisibility(t, body); got != "private" {
		t.Errorf("a non-member changed visibility to %q; body: %s", got, body)
	}

	// Anonymous cannot either.
	code, _ = authJSON(t, ts, http.MethodPut, "/api/v1/projects/guarded/visibility",
		"", map[string]any{"visibility": "public"})
	if code == http.StatusOK {
		t.Error("anonymous caller changed a project's visibility")
	}
}

// TestSetVisibilityRejectsUnknownLevel: a typo must be a 400, not a stored
// value that no read path can classify.
func TestSetVisibilityRejectsUnknownLevel(t *testing.T) {
	ts := newTestServer(t)
	owner := registerAndLogin(t, ts, "owner")
	createTestProjectWithAuth(t, ts, owner, "typo")

	code, _ := authJSON(t, ts, http.MethodPut, "/api/v1/projects/typo/visibility",
		owner, map[string]any{"visibility": "semi-public"})
	if code != http.StatusBadRequest {
		t.Errorf("unknown visibility: got %d, want 400", code)
	}
}

// TestInviteGrantsProtectedAccess walks the whole protected flow over HTTP,
// because the point of the level is that the grant crosses a process boundary.
func TestInviteGrantsProtectedAccess(t *testing.T) {
	ts := newTestServer(t)
	owner := registerAndLogin(t, ts, "owner")
	guest := registerAndLogin(t, ts, "guest")

	createTestProjectWithAuth(t, ts, owner, "gated")
	setVisibility(t, ts, owner, "gated", "protected")

	// Closed before the invite.
	code, _ := authGet(t, ts, "/api/v1/projects/gated", guest)
	if code != http.StatusNotFound {
		t.Fatalf("protected before invite: got %d, want 404", code)
	}

	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/gated/invites",
		owner, map[string]any{"max_uses": 5})
	if code != http.StatusCreated {
		t.Fatalf("create invite: %d (%s)", code, body)
	}
	var inv struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &inv); err != nil {
		t.Fatalf("decode invite: %v", err)
	}
	inviteID := inv.ID
	if len(inv.Token) < 32 {
		t.Fatalf("invite token is only %d chars", len(inv.Token))
	}

	// A stranger cannot mint their own way in.
	code, _ = authJSON(t, ts, http.MethodPost, "/api/v1/projects/gated/invites",
		guest, map[string]any{})
	if code != http.StatusNotFound {
		t.Errorf("stranger minting an invite: got %d, want 404", code)
	}

	code, body = authJSON(t, ts, http.MethodPost, "/api/v1/auth/redeem-invite",
		guest, map[string]any{"token": inv.Token})
	if code != http.StatusOK {
		t.Fatalf("redeem: %d (%s)", code, body)
	}

	code, _ = authGet(t, ts, "/api/v1/projects/gated", guest)
	if code != http.StatusOK {
		t.Errorf("protected after redeem: got %d, want 200", code)
	}

	// Redeeming twice is not an error, and does not exhaust the invite.
	for i := 0; i < 2; i++ {
		code, body = authJSON(t, ts, http.MethodPost, "/api/v1/auth/redeem-invite",
			guest, map[string]any{"token": inv.Token})
		if code != http.StatusOK {
			t.Fatalf("repeat redeem %d: %d (%s)", i+1, code, body)
		}
	}
	code, body = authGet(t, ts, "/api/v1/projects/gated/invites", owner)
	if code != http.StatusOK {
		t.Fatalf("list invites: %d", code)
	}
	// Parse rather than substring-match: the response is indented, so
	// `"uses":1` never appears literally and the check would be meaningless.
	var invites []struct {
		ID   int64 `json:"id"`
		Uses int   `json:"uses"`
	}
	if err := json.Unmarshal(body, &invites); err != nil {
		t.Fatalf("decode invites: %v (%s)", err, body)
	}
	found := false
	for _, inv := range invites {
		if inv.ID == inviteID {
			found = true
			if inv.Uses != 1 {
				t.Errorf("uses = %d after 3 redemptions by one user, want 1", inv.Uses)
			}
		}
	}
	if !found {
		t.Errorf("the minted invite is missing from the list: %s", body)
	}
}

// TestRevokedInviteStopsWorking, and a non-member cannot revoke: revoking is a
// member action, and it must apply to the invite you own and nobody else's.
func TestRevokedInviteStopsWorking(t *testing.T) {
	ts := newTestServer(t)
	owner := registerAndLogin(t, ts, "owner")
	guest := registerAndLogin(t, ts, "guest")
	other := registerAndLogin(t, ts, "other")

	createTestProjectWithAuth(t, ts, owner, "revokable")
	setVisibility(t, ts, owner, "revokable", "protected")
	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/revokable/invites",
		owner, map[string]any{})
	if code != http.StatusCreated {
		t.Fatalf("create invite: %d (%s)", code, body)
	}
	var inv struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	json.Unmarshal(body, &inv)

	// A stranger cannot revoke someone else's project's invite.
	code, _ = authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/revokable/invites/"+itoa(inv.ID)+"/revoke", other, nil)
	if code != http.StatusNotFound {
		t.Errorf("stranger revoking: got %d, want 404", code)
	}

	code, body = authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/revokable/invites/"+itoa(inv.ID)+"/revoke", owner, nil)
	if code != http.StatusOK {
		t.Fatalf("owner revoke: %d (%s)", code, body)
	}

	code, _ = authJSON(t, ts, http.MethodPost, "/api/v1/auth/redeem-invite",
		guest, map[string]any{"token": inv.Token})
	if code != http.StatusNotFound {
		t.Errorf("redeeming a revoked invite: got %d, want 404", code)
	}
}

// TestRedeemRequiresASession: the grant it creates is a membership, so an
// anonymous caller cannot hold one.
func TestRedeemRequiresASession(t *testing.T) {
	ts := newTestServer(t)
	owner := registerAndLogin(t, ts, "owner")
	createTestProjectWithAuth(t, ts, owner, "needs-session")
	setVisibility(t, ts, owner, "needs-session", "protected")

	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/needs-session/invites",
		owner, map[string]any{})
	if code != http.StatusCreated {
		t.Fatalf("create invite: %d (%s)", code, body)
	}
	var inv struct {
		Token string `json:"token"`
	}
	json.Unmarshal(body, &inv)

	code, _ = authJSON(t, ts, http.MethodPost, "/api/v1/auth/redeem-invite", "",
		map[string]any{"token": inv.Token})
	if code != http.StatusUnauthorized {
		t.Errorf("anonymous redeem: got %d, want 401", code)
	}
}

func createTestProjectWithAuth(t *testing.T, ts *httptest.Server, auth, slug string) {
	t.Helper()
	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", auth,
		map[string]any{
			"slug": slug, "name": slug, "description": "visibility test project",
		})
	if code != http.StatusCreated {
		t.Fatalf("create project %s: status %d body %s", slug, code, body)
	}
}

// projectVisibility parses visibility out of a project JSON body. A substring
// check is wrong here: responses are indented, so `"visibility":"private"`
// never matches and the assertion would need to be written against the
// formatting rather than the value.
func projectVisibility(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		Visibility string `json:"visibility"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode project: %v (body %s)", err, body)
	}
	return out.Visibility
}
