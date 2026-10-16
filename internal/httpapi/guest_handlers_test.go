package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/db"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// Guest interactions, the cookie notice, and folding.
//
// The claims under test (docs/specs/guest-interactions-spec.md §1): a visitor can
// act without an account until the instance passes its configured MAU limit; what
// they produced becomes their account's when they create one; and interactions are
// refused until the notice is accepted.
//
// These are HTTP-level tests because the decisions being checked are the SERVER's:
// that a write with no consent cookie is refused, that a guest write stops at the
// limit, and that the consent cookie permits without authorizing. A client-side
// guard could satisfy none of them.

// guestServer builds a server whose limit and store the test controls.
//
// It deliberately does NOT reuse newTestServer: that helper bakes in one identity
// and one limit, and the interesting cases here are "limit reached", "limit
// switched off" and "this request is a guest".
func guestServer(t *testing.T, limit int) (*httptest.Server, *store.DB, *Server) {
	t.Helper()
	sqlDB, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Migrate(t.Context(), sqlDB); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(sqlDB)
	srv, err := NewServer(st, "test")
	if err != nil {
		t.Fatal(err)
	}
	srv.GuestMAULimit = limit
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return ts, st, srv
}

// guestClient is a client that carries whatever the test tells it to: a token, a
// consent cookie, or neither.
func guestClient(t *testing.T, ts *httptest.Server, method, path string, body any, token string, consent bool) (*http.Response, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if consent {
		req.Header.Set("Cookie", consentCookieName+"="+defaultConsentVersion)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

// claimGuest mints a guest identity through the real endpoint and returns its token.
func claimGuest(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	resp, body := guestClient(t, ts, http.MethodPost, "/api/v1/guest", nil, "", true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("claim guest: status %d body=%v", resp.StatusCode, body)
	}
	tok, _ := body["token"].(string)
	if tok == "" {
		t.Fatalf("claim guest returned no token: %v", body)
	}
	if guest, _ := body["user"].(map[string]any); guest == nil || guest["is_guest"] != true {
		t.Fatalf("the claimed identity is not marked a guest: %v", body["user"])
	}
	return tok
}

func TestAWriteWithoutConsentIsRefusedAndSaysWhy(t *testing.T) {
	ts, _, _ := guestServer(t, 1000)
	// A registered account, because the refusal must apply to accounts too: the
	// notice is about cookies, not about being a guest.
	tok := registerOn(t, ts, "consenting")

	resp, body := guestClient(t, ts, http.MethodPost, "/api/v1/projects/1/complaints",
		map[string]any{"project_id": 1, "title": "x"}, tok, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("write with no consent: status %d body=%v; want 403", resp.StatusCode, body)
	}
	if got := fmt.Sprint(body["consent"]); got != "required" {
		t.Errorf("refusal says consent=%v; a client cannot act on that", body["consent"])
	}
	if msg := fmt.Sprint(body["error"]); !strings.Contains(msg, "cookies must be accepted") {
		t.Errorf("refusal message %q does not say what to do", msg)
	}
}

func TestConsentIsNotRequiredToRead(t *testing.T) {
	ts, _, _ := guestServer(t, 1000)
	for _, path := range []string{"/api/v1/projects", "/api/v1/guest", "/api/v1/version"} {
		resp, body := guestClient(t, ts, http.MethodGet, path, nil, "", false)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s without consent: status %d body=%v; reading must stay open", path, resp.StatusCode, body)
		}
	}
}

func TestTheExemptRoutesStayExempt(t *testing.T) {
	ts, _, _ := guestServer(t, 1000)

	// Registration: a visitor must be able to create an account (and so, to stop
	// being a guest) without agreeing to anything first.
	resp, body := guestClient(t, ts, http.MethodPost, "/api/v1/auth/register",
		map[string]any{"username": "exempted", "password": "test-password-123", "display_name": "E"}, "", false)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("register without consent: status %d body=%v; want 201", resp.StatusCode, body)
	}
	// Accepting is the gate's own door.
	resp, body = guestClient(t, ts, http.MethodPost, "/api/v1/consent", nil, "", false)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("accept consent: status %d body=%v; want 200", resp.StatusCode, body)
	}
	if len(resp.Cookies()) == 0 {
		t.Fatalf("accepting the notice set no cookie")
	}
	c := resp.Cookies()[0]
	if c.Name != consentCookieName {
		t.Errorf("acceptance set cookie %q; want %q", c.Name, consentCookieName)
	}
	if !c.HttpOnly {
		t.Errorf("the consent cookie is readable by scripts; it is a gate, not a token, and no script needs it")
	}
	// The webhook is server-to-server and has no browser to accept anything.
	resp, _ = guestClient(t, ts, http.MethodPost, "/api/v1/hooks/forgejo", map[string]any{}, "", false)
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("the forgejo hook was refused by the consent gate; a webhook cannot accept a cookie")
	}
}

func TestAGuestMayActWithoutAnAccount(t *testing.T) {
	ts, st, _ := guestServer(t, 1000)
	// A project to act on, created by an account (guests cannot create one).
	ownerTok := registerOn(t, ts, "project-owner")
	resp, proj := guestClient(t, ts, http.MethodPost, "/api/v1/projects",
		map[string]any{"slug": "guest-acts", "name": "Guest Acts", "description": "d", "license": "MIT"},
		ownerTok, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: status %d body=%v", resp.StatusCode, proj)
	}
	projectID := int64(proj["id"].(float64))

	guestTok := claimGuest(t, ts)
	// A complaint is a write an unprivileged actor may make (§5: anyone may file
	// one), which is exactly the surface a guest gets.
	resp, body := guestClient(t, ts, http.MethodPost, fmt.Sprintf("/api/v1/projects/%d/complaints", projectID),
		map[string]any{"project_id": projectID, "title": "the export loses the last row",
			"body": "b", "severity": 3, "frequency": 0.5}, guestTok, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a guest filing a complaint: status %d body=%v; want 201", resp.StatusCode, body)
	}
	// The row exists and belongs to a guest identity, read from the store rather
	// than from the response: the response is the server's claim, the row is the
	// fact.
	uid, err := st.ResolveToken(t.Context(), guestTok)
	if err != nil {
		t.Fatalf("resolve guest token: %v", err)
	}
	var authorID int64
	if err := st.QueryRowContext(t.Context(),
		`SELECT author_id FROM complaints WHERE project_id = ? ORDER BY id DESC LIMIT 1`,
		projectID).Scan(&authorID); err != nil {
		t.Fatalf("read the complaint's author: %v", err)
	}
	if authorID != uid {
		t.Errorf("the complaint's author is %d; the guest who filed it is %d", authorID, uid)
	}
}

func TestAGuestMayNotCreateAProjectOrAnInvite(t *testing.T) {
	ts, _, _ := guestServer(t, 1000)
	ownerTok := registerOn(t, ts, "invite-owner")
	resp, proj := guestClient(t, ts, http.MethodPost, "/api/v1/projects",
		map[string]any{"slug": "invite-host", "name": "Invite Host", "description": "d", "license": "MIT"},
		ownerTok, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: status %d body=%v", resp.StatusCode, proj)
	}
	guestTok := claimGuest(t, ts)

	resp, body := guestClient(t, ts, http.MethodPost, "/api/v1/projects",
		map[string]any{"slug": "squatted", "name": "Squatted", "description": "d", "license": "MIT"},
		guestTok, true)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a guest created a project: status %d body=%v; want 403", resp.StatusCode, body)
	}
	if code := fmt.Sprint(body["code"]); code != "account_required" {
		t.Errorf("refusal code = %q; a client cannot tell why", code)
	}

	resp, body = guestClient(t, ts, http.MethodPost, "/api/v1/projects/invite-host/invites",
		map[string]any{"expires_in_seconds": 3600, "max_uses": 1}, guestTok, true)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a guest created an invite: status %d body=%v; want 403", resp.StatusCode, body)
	}
}

func TestGuestsStopAtTheLimit(t *testing.T) {
	// Limit 1: one account below the line, the second account crosses it. The
	// arithmetic is the gate's, so the test drives it with real rows rather than a
	// stub counter.
	ts, st, srv := guestServer(t, 2)
	ownerTok := registerOn(t, ts, "limit-owner-one")
	registerOn(t, ts, "limit-owner-two")

	// Two accounts have now acted (registration audits do it), so the limit is
	// reached: no new guest identity.
	resp, body := guestClient(t, ts, http.MethodPost, "/api/v1/guest", nil, "", true)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("claiming a guest at the limit: status %d body=%v; want 403", resp.StatusCode, body)
	}
	if code := fmt.Sprint(body["code"]); code != "guest_limit_reached" {
		t.Errorf("refusal code = %q; want guest_limit_reached", code)
	}
	// And the public status says the same thing, so a page can explain it before
	// the visitor tries.
	resp, status := guestClient(t, ts, http.MethodGet, "/api/v1/guest", nil, "", false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("guest status: status %d", resp.StatusCode)
	}
	if allowed, _ := status["allowed"].(bool); allowed {
		t.Errorf("status says guests are open at MAU %v / limit %v", status["mau"], status["limit"])
	}

	// A guest minted BEFORE the limit was reached must also stop: the gate is per
	// request, not only at mint.
	srv.GuestMAULimit = 1000
	guestTok := claimGuest(t, ts)
	srv.GuestMAULimit = 0 // switched off
	resp, proj := guestClient(t, ts, http.MethodPost, "/api/v1/projects",
		map[string]any{"slug": "a", "name": "a", "description": "d", "license": "MIT"}, ownerTok, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: status %d body=%v", resp.StatusCode, proj)
	}
	projectID := int64(proj["id"].(float64))
	resp, body = guestClient(t, ts, http.MethodPost, fmt.Sprintf("/api/v1/projects/%d/complaints", projectID),
		map[string]any{"project_id": projectID, "title": "t", "body": "b", "severity": 3, "frequency": 0.5},
		guestTok, true)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("an existing guest wrote after the limit closed: status %d body=%v; want 403", resp.StatusCode, body)
	}
	// An account is unaffected by the guest limit -- it is a limit on guests, not
	// on the instance.
	resp, body = guestClient(t, ts, http.MethodPost, fmt.Sprintf("/api/v1/projects/%d/complaints", projectID),
		map[string]any{"project_id": projectID, "title": "t2", "body": "b", "severity": 3, "frequency": 0.5},
		ownerTok, true)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("an account was refused by the guest limit: status %d body=%v", resp.StatusCode, body)
	}
	_ = st
}

func TestRegisteringWithAGuestTokenKeepsTheGuestsWork(t *testing.T) {
	ts, st, _ := guestServer(t, 1000)
	ownerTok := registerOn(t, ts, "fold-owner")
	resp, proj := guestClient(t, ts, http.MethodPost, "/api/v1/projects",
		map[string]any{"slug": "fold-host", "name": "Fold Host", "description": "d", "license": "MIT"},
		ownerTok, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: status %d body=%v", resp.StatusCode, proj)
	}
	projectID := int64(proj["id"].(float64))

	guestTok := claimGuest(t, ts)
	guestID, err := st.ResolveToken(t.Context(), guestTok)
	if err != nil {
		t.Fatalf("resolve guest token: %v", err)
	}
	for i := 0; i < 2; i++ {
		resp, body := guestClient(t, ts, http.MethodPost, fmt.Sprintf("/api/v1/projects/%d/complaints", projectID),
			map[string]any{"project_id": projectID, "title": fmt.Sprintf("guest %d", i), "body": "b",
				"severity": 3, "frequency": 0.5}, guestTok, true)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("guest complaint %d: status %d body=%v", i, resp.StatusCode, body)
		}
	}
	// Register while still holding the guest token.
	resp, body := guestClient(t, ts, http.MethodPost, "/api/v1/auth/register",
		map[string]any{"username": "was-a-guest", "password": "test-password-123", "display_name": "Was A Guest"},
		guestTok, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register as guest: status %d body=%v", resp.StatusCode, body)
	}
	user, _ := body["user"].(map[string]any)
	if user == nil {
		t.Fatalf("register returned no user: %v", body)
	}
	if got := int64(user["id"].(float64)); got != guestID {
		t.Errorf("registration created a new identity (%d); the guest's %d is the account now", got, guestID)
	}
	if isGuest, _ := user["is_guest"].(bool); isGuest {
		t.Errorf("the promoted identity is still marked a guest")
	}
	// The two complaints are still there, still owned by the same identity, which
	// is now an account.
	var n int
	if err := st.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM complaints WHERE author_id = ?`, guestID).Scan(&n); err != nil {
		t.Fatalf("count the promoted account's complaints: %v", err)
	}
	if n != 2 {
		t.Errorf("the promoted account has %d complaints; want the guest's 2", n)
	}
	// And the account can log in: that is what promotion buys.
	resp, _ = guestClient(t, ts, http.MethodPost, "/api/v1/auth/login",
		map[string]any{"username": "was-a-guest", "password": "test-password-123"}, "", true)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("login as the promoted account: status %d; want 200", resp.StatusCode)
	}
}

func TestLoggingInWithAGuestTokenFoldsTheWorkAndSaysSo(t *testing.T) {
	ts, st, _ := guestServer(t, 1000)
	ownerTok := registerOn(t, ts, "login-fold-owner")
	resp, proj := guestClient(t, ts, http.MethodPost, "/api/v1/projects",
		map[string]any{"slug": "login-fold-host", "name": "Login Fold Host", "description": "d", "license": "MIT"},
		ownerTok, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: status %d body=%v", resp.StatusCode, proj)
	}
	projectID := int64(proj["id"].(float64))

	guestTok := claimGuest(t, ts)
	guestID, err := st.ResolveToken(t.Context(), guestTok)
	if err != nil {
		t.Fatalf("resolve guest token: %v", err)
	}
	resp, body := guestClient(t, ts, http.MethodPost, fmt.Sprintf("/api/v1/projects/%d/complaints", projectID),
		map[string]any{"project_id": projectID, "title": "filed as a guest", "body": "b",
			"severity": 3, "frequency": 0.5}, guestTok, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("guest complaint: status %d body=%v", resp.StatusCode, body)
	}

	// Log in to the existing account while still holding the guest token.
	resp, body = guestClient(t, ts, http.MethodPost, "/api/v1/auth/login",
		map[string]any{"username": "testuser", "password": "test-password-123"}, guestTok, true)
	// This server has no "testuser": it was built by guestServer, so register one
	// instead and log in to it.
	if resp.StatusCode != http.StatusOK {
		registerOn(t, ts, "fold-target")
		resp, body = guestClient(t, ts, http.MethodPost, "/api/v1/auth/login",
			map[string]any{"username": "fold-target", "password": "test-password-123"}, guestTok, true)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login with a guest token: status %d body=%v", resp.StatusCode, body)
	}
	fold, ok := body["fold"].(map[string]any)
	if !ok {
		t.Fatalf("login response does not report the fold: %v", body)
	}
	if moved, _ := fold["moved"].(float64); moved < 1 {
		t.Errorf("fold reports %v moved rows; the guest's complaint had to move", fold["moved"])
	}
	// The guest identity is gone and the account owns the complaint.
	if _, err := st.GetUserByID(t.Context(), guestID); err == nil {
		t.Errorf("the guest identity survived a fold-on-login")
	}
	account, _ := body["user"].(map[string]any)
	accountID := int64(account["id"].(float64))
	var n int
	if err := st.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM complaints WHERE author_id = ?`, accountID).Scan(&n); err != nil {
		t.Fatalf("count the account's complaints: %v", err)
	}
	if n != 1 {
		t.Errorf("the account has %d complaints; want the guest's 1", n)
	}
}

func TestTheConsentCookieIsNotACredential(t *testing.T) {
	ts, _, _ := guestServer(t, 1000)
	// Consent, no token: a route that needs an actor must still refuse. If this
	// ever passes, the cookie has become an authorization and every request from
	// any site the visitor has open is a request they made.
	resp, body := guestClient(t, ts, http.MethodPost, "/api/v1/projects",
		map[string]any{"slug": "cookie-proj", "name": "Cookie", "description": "d", "license": "MIT"},
		"", true)
	if resp.StatusCode == http.StatusCreated {
		t.Fatalf("a consent cookie alone created a project: %v", body)
	}
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Errorf("anonymous write with consent: status %d; want 401 or 403", resp.StatusCode)
	}
}

func TestConsentIsRecordedAgainstTheAccountThatAcceptedIt(t *testing.T) {
	ts, st, _ := guestServer(t, 1000)
	tok := registerOn(t, ts, "records-consent")
	uid, err := st.ResolveToken(t.Context(), tok)
	if err != nil {
		t.Fatalf("resolve token: %v", err)
	}
	resp, body := guestClient(t, ts, http.MethodPost, "/api/v1/consent", nil, tok, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accept consent as an account: status %d body=%v", resp.StatusCode, body)
	}
	if n, err := st.ConsentRecords(t.Context()); err != nil || n != 1 {
		t.Errorf("consent records = %d, %v; want the one identity that accepted", n, err)
	}
	if _, err := st.GetUserByID(t.Context(), uid); err != nil {
		t.Errorf("the accepting identity disappeared: %v", err)
	}
}

func TestThePageTellsTheVisitorWhetherTheNoticeIsAccepted(t *testing.T) {
	ts, _, _ := guestServer(t, 1000)
	// The page carries the state, so the notice does not depend on a fetch that
	// could fail and leave a visitor with no way to accept.
	resp, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read page: %v", err)
	}
	page := buf.String()
	if !strings.Contains(page, `data-consent="required"`) {
		t.Errorf("the home page does not carry data-consent=\"required\" for a visitor with no notice accepted")
	}
	if !strings.Contains(page, `id="cookie-notice"`) {
		t.Errorf("the notice is not in the page")
	}
	// With the cookie accepted, neither the state nor the notice is there.
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", consentCookieName+"="+defaultConsentVersion)
	resp2, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET / with consent: %v", err)
	}
	defer resp2.Body.Close()
	var buf2 bytes.Buffer
	if _, err := buf2.ReadFrom(resp2.Body); err != nil {
		t.Fatalf("read page: %v", err)
	}
	accepted := buf2.String()
	if !strings.Contains(accepted, `data-consent="accepted"`) {
		t.Errorf("the page does not report an accepted notice")
	}
	if strings.Contains(accepted, `id="cookie-notice"`) {
		t.Errorf("the notice is still rendered after acceptance")
	}
}