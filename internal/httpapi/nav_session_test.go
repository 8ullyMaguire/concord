package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The navigation cookie: what it is for, and the three things it must NOT do.
//
// The gap it closes was measured, not reasoned. The session is a bearer token in
// localStorage attached by script, so a page's own HTML arrives anonymous; every page
// behind projectReadable then 404s for a member of a protected project. The failing
// request is the NAVIGATION, and only a cookie can carry a credential on one.
//
// A fix for that is one careless line away from being a CSRF hole, so the negative half
// of this file matters more than the positive half.

const harnessPassword = "test-password-123"

// navigationRequest sends a request that deliberately carries NO Authorization header:
// the harness wrapper otherwise supplies the default identity, which would make every
// assertion here about the harness instead of about the cookie.
func navigationRequest(t *testing.T, ts *httptest.Server, method, path, cookie, body string) (*http.Response, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, ts.URL+path, rdr)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-No-Auth", "1")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: navCookieName, Value: cookie})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

// navCookieFor logs in over the real API and returns the cookie the browser would hold.
func navCookieFor(t *testing.T, ts *httptest.Server, username string) string {
	t.Helper()
	body := `{"username":"` + username + `","password":"` + harnessPassword + `"}`
	resp, _ := navigationRequest(t, ts, http.MethodPost, "/api/v1/auth/login", "", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login as %s answered %d", username, resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == navCookieName {
			return c.Value
		}
	}
	t.Fatalf("login set no %s cookie: the browser has nothing a navigation can carry, so "+
		"every non-public page stays a 404 to its own members", navCookieName)
	return ""
}

func TestLoginSetsANavigationCookieThatScriptCannotRead(t *testing.T) {
	ts := newTestServer(t)
	if navCookieFor(t, ts, "testuser") == "" {
		t.Fatal("no cookie value")
	}

	body := `{"username":"testuser","password":"` + harnessPassword + `"}`
	resp, _ := navigationRequest(t, ts, http.MethodPost, "/api/v1/auth/login", "", body)
	var c *http.Cookie
	for _, got := range resp.Cookies() {
		if got.Name == navCookieName {
			c = got
		}
	}
	if c == nil {
		t.Fatal("login set no navigation cookie")
	}
	if !c.HttpOnly {
		t.Error("the navigation cookie is readable by script; the header path is the " +
			"script's credential and this one exists only to be sent automatically")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax: it keeps the cookie off cross-site "+
			"subrequests while still being sent on a top-level navigation, which is the "+
			"case it is for", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("Path = %q, want /: a narrower path would silently not be sent on "+
			"project pages", c.Path)
	}
	if c.MaxAge != 0 {
		t.Errorf("MaxAge = %d, want 0 (a session cookie). The localStorage token lives "+
			"until revoked; this convenience should not outlive the browser session", c.MaxAge)
	}
}

// THE PROPERTY: a member with ONLY the cookie can open a protected project's page.
// This is the case that could not be driven in a browser at all before the cookie
// existed (docs/REMAINING.md §13.4).
func TestANavigationCookieOpensAProtectedPageForAMember(t *testing.T) {
	ts := newTestServer(t)
	createTestProject(t, ts, "nav-protected")
	// The harness actor owns the project it just created, so the privileged token can
	// make it protected.
	if resp, body := doJSON(t, ts, http.MethodPut,
		"/api/v1/projects/nav-protected/visibility",
		map[string]any{"visibility": "protected"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("set visibility: %d %v", resp.StatusCode, body)
	}

	// The stranger first: a valid account that is not a member. The cookie must NOT be
	// what opens the page.
	registerOn(t, ts, "navstranger")
	stranger := navCookieFor(t, ts, "navstranger")
	_, strangerBody := navigationRequest(t, ts, http.MethodGet,
		"/projects/nav-protected/settings", stranger, "")
	if strings.Contains(strangerBody, `id="settings-root"`) {
		t.Error("a signed-in NON-member got the protected project's settings page from a " +
			"cookie alone")
	}

	// The owner, with only the cookie.
	owner := navCookieFor(t, ts, "testuser")
	resp, body := navigationRequest(t, ts, http.MethodGet,
		"/projects/nav-protected/settings", owner, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the owner's navigation to a protected project answered %d; the cookie "+
			"is the only credential a navigation can carry (body starts %q)",
			resp.StatusCode, firstLine(body))
	}
	if !strings.Contains(body, `id="settings-root"`) {
		t.Errorf("the owner got a page without the settings panel: body starts %q", firstLine(body))
	}

	// And without it, the same request is back to the not-found page: the cookie is what
	// makes the difference, not something else about the request.
	_, noCookieBody := navigationRequest(t, ts, http.MethodGet,
		"/projects/nav-protected/settings", "", "")
	if strings.Contains(noCookieBody, `id="settings-root"`) {
		t.Error("the protected settings page rendered for a request with NO credential; " +
			"this test would then pass for a reason unrelated to the cookie")
	}
}

// THE NEGATIVE HALF. Each case is a way the cookie must not behave; each one would be a
// CSRF or an authorization bug if it did.
func TestANavigationCookieCannotAuthorizeAnythingButAPageGet(t *testing.T) {
	ts := newTestServer(t)
	createTestProject(t, ts, "nav-negative")
	cookie := navCookieFor(t, ts, "testuser")

	// (1) No /api/ read. The API's answer to a cookie alone is "anonymous", so an
	// endpoint that refuses anonymous callers still refuses this.
	if resp, _ := navigationRequest(t, ts, http.MethodGet,
		"/api/v1/projects/nav-negative/invites", cookie, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/.../invites with only the cookie answered %d, want 401: the "+
			"cookie must not be an API credential, or a cross-site request to an API read "+
			"would be authenticated by the browser's automatic cookie", resp.StatusCode)
	}

	// (2) No write, on an API route or anywhere else. This is the CSRF assertion.
	for _, path := range []string{
		"/api/v1/projects/nav-negative/visibility",
		"/api/v1/projects/nav-negative/tags",
		"/api/v1/projects/nav-negative/languages",
	} {
		if resp, _ := navigationRequest(t, ts, http.MethodPut, path, cookie,
			`{"visibility":"private"}`); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("PUT %s with only the cookie answered %d, want 401: a mutating route "+
				"reachable by a cookie is a CSRF hole", path, resp.StatusCode)
		}
	}

	// (3) No POST either. The condition is a GET, not merely "not an API path".
	if resp, _ := navigationRequest(t, ts, http.MethodPost,
		"/api/v1/projects/nav-negative/invites", cookie, `{}`); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST with only the cookie answered %d, want 401", resp.StatusCode)
	}

	// (4) And the page GET it IS allowed to serve still refuses a revoked token: the
	// cookie is not a bypass of token resolution.
	if resp, _ := navigationRequest(t, ts, http.MethodPost, "/api/v1/auth/logout", cookie, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout answered %d", resp.StatusCode)
	}
	if _, body := navigationRequest(t, ts, http.MethodGet,
		"/projects/nav-negative/settings", cookie, ""); strings.Contains(body, `id="settings-root"`) {
		t.Error("a revoked token's cookie still rendered the settings page")
	}
}

// A revoked token's cookie must stop working, or logout is a lie the browser keeps
// telling: the header token is gone and the cookie still renders member pages.
func TestLogoutClearsTheNavigationCookie(t *testing.T) {
	ts := newTestServer(t)
	cookie := navCookieFor(t, ts, "testuser")

	resp, _ := navigationRequest(t, ts, http.MethodPost, "/api/v1/auth/logout", cookie, "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout answered %d", resp.StatusCode)
	}
	cleared := false
	for _, c := range resp.Cookies() {
		if c.Name == navCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("logout did not expire %s; a logout that leaves the navigation cookie "+
			"behind keeps rendering member pages for a credential that no longer resolves",
			navCookieName)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if len(s) > 120 {
		return s[:120]
	}
	return s
}
