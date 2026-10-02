package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/db"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// Acceptance tests for authentication.
//
// These are not CRUD checks. Each one exists because getting it wrong produces
// a site that looks authenticated and is not, or one that leaks a credential.
// The suite that prompted all of this (0001's api_tokens table, read by nobody,
// written by nobody) passed 100% while the deployed service returned 401 for
// every write and for the feature ranking.

// getJSON is getWith for callers that need to read the response body. getWith
// drains and closes it, so decoding from it afterwards is not possible.
func getJSON(t *testing.T, ts *httptest.Server, path string, hdr ...string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	// A bare JSON array cannot be unmarshalled into a map. Decode into any and
	// wrap an array under "items" so callers always have a map to read; object
	// responses pass through unchanged. Several list endpoints return a bare
	// array, including the feature ranking this file asserts on.
	var raw any
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	_ = res.Body.Close()
	m, ok := raw.(map[string]any)
	if !ok {
		m = map[string]any{"items": raw}
	}
	return res, m
}

// testClient bounds every request. Without a timeout a blocked request
// hangs the whole binary until the package timeout, which reports as an
// unrelated failure.
var testClient = &http.Client{Timeout: 10 * time.Second}

// newAuthServer returns a server with a migrated, empty database and no actor
// injection at all, so every request really does travel the production path.
func newAuthServer(t *testing.T) *httptest.Server {
	t.Helper()
	sqlDB, err := db.Open(t.TempDir() + "/auth.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Migrate(t.Context(), sqlDB); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	srv, err := NewServer(store.New(sqlDB), "test")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return ts
}

// putJSON is postJSON with the PUT method. Needed because PUT /projects/{slug}/tags
// is a PUT and postJSON hardcodes POST -- calling it for that route returned 405
// Method Not Allowed, which reads like a missing route rather than a wrong verb.
func putJSON(t *testing.T, ts *httptest.Server, path, body string, hdr ...string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	res.Body.Close()
	return res, m
}

func postJSON(t *testing.T, ts *httptest.Server, path, body string, hdr ...string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	// Drain and close immediately rather than at cleanup.
	//
	// A response body that is closed but never read keeps the connection
	// unusable for reuse, and the httptest server eventually runs out of
	// connections: the next request blocks in Do() and the test binary hangs
	// until the package timeout. Reading the body to EOF is what returns the
	// connection to the pool.
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	_ = res.Body.Close()
	return res, m
}

func getWith(t *testing.T, ts *httptest.Server, path string, hdr ...string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	return res
}

// TestRegisterReturnsUsableToken is the central one: it proves a token is not
// merely written to the database but actually resolved by the middleware and
// accepted by a write endpoint. A store-only test would pass while the site
// stayed read-only, which is the bug this whole change exists to fix.
func TestRegisterReturnsUsableToken(t *testing.T) {
	ts := newAuthServer(t)

	res, body := postJSON(t, ts, "/api/v1/auth/register",
		`{"username":"alice","password":"correct horse battery","display_name":"Alice"}`)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register: status %d, body %v", res.StatusCode, body)
	}
	tok, _ := body["token"].(string)
	if !strings.HasPrefix(tok, "clt_") {
		t.Fatalf("token should be prefixed clt_, got %q", tok)
	}

	// The token must work on a write endpoint.
	res2, body2 := postJSON(t, ts, "/api/v1/projects",
		`{"slug":"first-project","name":"First"}`,
		"Authorization", "Bearer "+tok)
	if res2.StatusCode != http.StatusCreated {
		t.Fatalf("authenticated create project: status %d, body %v", res2.StatusCode, body2)
	}

	// And identify the caller.
	res3 := getWith(t, ts, "/api/v1/auth/me", "Authorization", "Bearer "+tok)
	if res3.StatusCode != http.StatusOK {
		t.Fatalf("me: status %d", res3.StatusCode)
	}
}

// TestPrioritiesReachableWithToken covers the endpoint the portfolio import
// depends on: it returned 401 for everyone before this change.
//
// The setup walks the real domain chain, because a feature may only be proposed
// against a *validated* complaint. Creating features directly is not merely
// unsupported, it is what the governance model exists to prevent — so the test
// files a complaint, validates it, and only then proposes.
func TestPrioritiesReachableWithToken(t *testing.T) {
	ts := newAuthServer(t)

	_, reg := postJSON(t, ts, "/api/v1/auth/register",
		`{"username":"bob","password":"correct horse battery"}`)
	tok, _ := reg["token"].(string)
	const (
		ah = "Authorization"
		bt = "Bearer "
	)

	res, proj := postJSON(t, ts, "/api/v1/projects",
		`{"slug":"ranked","name":"Ranked"}`, ah, bt+tok)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create project: %d %v", res.StatusCode, proj)
	}

	cres, comp := postJSON(t, ts, "/api/v1/projects/ranked/complaints",
		`{"project_id":1,"title":"Slow load","body":"Pages take too long.","severity":3,"frequency":0.8,"strategic_multiplier":1.0}`,
		ah, bt+tok)
	if cres.StatusCode != http.StatusCreated {
		t.Fatalf("create complaint: %d %v", cres.StatusCode, comp)
	}
	cid := int64(comp["id"].(float64))

	vres, vbody := postJSON(t, ts, "/api/v1/projects/ranked/complaints/"+itoa(cid)+"/validate", `{}`, ah, bt+tok)
	if vres.StatusCode != http.StatusOK {
		t.Fatalf("validate complaint: %d %v", vres.StatusCode, vbody)
	}

	for _, title := range []string{"Feature A", "Feature B"} {
		body := `{"project_id":1,"title":"` + title + `","body":"Body","effort":"M","linked_complaints":[` + itoa(cid) + `]}`
		fr, fb := postJSON(t, ts, "/api/v1/projects/ranked/features", body, ah, bt+tok)
		if fr.StatusCode >= 400 {
			t.Fatalf("create feature %q: %d %v", title, fr.StatusCode, fb)
		}
	}

	pres, pbody := getJSON(t, ts, "/api/v1/projects/ranked/priorities", ah, bt+tok)
	if pres.StatusCode != http.StatusOK {
		t.Fatalf("priorities with token: got %d, want 200 — this endpoint is the whole point", pres.StatusCode)
	}

	// Assert the content, not just the status.
	//
	// The handler resolved the project by slug, then passed a separately parsed
	// integer to the ranking query. With the bug restored the request still
	// returns 200 — with an empty list, because it ranked project 0. A status
	// assertion alone is what let this survive: the previous suite checked only
	// that requests did not error, so a query against the wrong project looked
	// exactly like an empty project.
	// The endpoint returns a bare JSON array, which getJSON wraps under "items".
	items, _ := pbody["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("priorities returned %d features, want 2; an empty list means "+
			"the ranking queried the wrong project (body %+v)", len(items), pbody)
	}
	seen := map[string]bool{}
	for _, raw := range items {
		it, _ := raw.(map[string]any)
		if it == nil {
			continue
		}
		title, _ := it["title"].(string)
		seen[title] = true
		pain, _ := it["pain_score"].(float64)
		if pain <= 0 {
			t.Errorf("feature %q has pain score %v; the linked complaint was not counted", title, pain)
		}
	}
	if !seen["Feature A"] || !seen["Feature B"] {
		t.Errorf("expected both features in the ranking, got %+v", pbody)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	ts := newAuthServer(t)
	postJSON(t, ts, "/api/v1/auth/register", `{"username":"carol","password":"correct horse battery"}`)

	res, _ := postJSON(t, ts, "/api/v1/auth/login", `{"username":"carol","password":"wrong"}`)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: got %d, want 401", res.StatusCode)
	}
}

// TestUnknownUserIsIndistinguishableFromWrongPassword pins the anti-enumeration
// property. If these two ever differ, the API is a list of which usernames
// exist.
func TestUnknownUserIsIndistinguishableFromWrongPassword(t *testing.T) {
	ts := newAuthServer(t)
	postJSON(t, ts, "/api/v1/auth/register", `{"username":"dave","password":"correct horse battery"}`)

	known, _ := postJSON(t, ts, "/api/v1/auth/login", `{"username":"dave","password":"nope"}`)
	unknown, _ := postJSON(t, ts, "/api/v1/auth/login", `{"username":"nobody","password":"nope"}`)
	if known.StatusCode != unknown.StatusCode {
		t.Fatalf("response differs for known (%d) vs unknown (%d) user — enumeration leak",
			known.StatusCode, unknown.StatusCode)
	}
}

// TestAnonymousReadsStillWork guards the property that makes the site public:
// no token must not mean rejected.
func TestAnonymousReadsStillWork(t *testing.T) {
	ts := newAuthServer(t)
	for _, p := range []string{"/api/v1/projects", "/api/v1/search?q=x"} {
		if res := getWith(t, ts, p); res.StatusCode != http.StatusOK {
			t.Errorf("anonymous GET %s: got %d, want 200", p, res.StatusCode)
		}
	}
}

func TestAnonymousWriteIsRejected(t *testing.T) {
	ts := newAuthServer(t)
	res, _ := postJSON(t, ts, "/api/v1/projects", `{"slug":"anon","name":"Anon"}`)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous create: got %d, want 401", res.StatusCode)
	}
}

// TestInvalidTokenIsRejectedRatherThanDowngraded: a client holding a bad token
// must be told, not silently treated as anonymous. Otherwise an expired
// credential looks like a working session that mysteriously cannot write.
func TestInvalidTokenIsRejectedRatherThanDowngraded(t *testing.T) {
	ts := newAuthServer(t)
	res, _ := postJSON(t, ts, "/api/v1/projects", `{"slug":"x","name":"X"}`,
		"Authorization", "Bearer clt_definitely_not_a_real_token")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("garbage token: got %d, want 401", res.StatusCode)
	}
}

func TestRevokedTokenStopsWorking(t *testing.T) {
	ts := newAuthServer(t)
	_, reg := postJSON(t, ts, "/api/v1/auth/register", `{"username":"erin","password":"correct horse battery"}`)
	tok, _ := reg["token"].(string)

	// Works before logout.
	if res := getWith(t, ts, "/api/v1/auth/me", "Authorization", "Bearer "+tok); res.StatusCode != http.StatusOK {
		t.Fatalf("me before logout: %d", res.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: got %d, want 204", res.StatusCode)
	}

	if res := getWith(t, ts, "/api/v1/auth/me", "Authorization", "Bearer "+tok); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me after logout: got %d, want 401 — token was not revoked", res.StatusCode)
	}
}

// TestTokenIsStoredHashed is a regression guard for a failure that would be
// silent and severe: if the raw token were stored, a database dump would yield
// live credentials. It needs the database handle, so it builds its own server
// rather than going through HTTP.
func TestTokenIsStoredHashed(t *testing.T) {
	sqlDB, err := db.Open(t.TempDir() + "/hash.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Migrate(t.Context(), sqlDB); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(sqlDB)

	_, tok, err := st.RegisterUser(t.Context(), "frank", "Frank", "correct horse battery")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// Every row in api_tokens, concatenated. The plaintext must not appear.
	rows, err := sqlDB.QueryContext(t.Context(), `SELECT token_hash FROM api_tokens`)
	if err != nil {
		t.Fatalf("query tokens: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			t.Fatal(err)
		}
		n++
		if strings.Contains(h, tok) {
			t.Fatalf("raw token %q appears in the stored hash %q", tok, h)
		}
	}
	if n == 0 {
		t.Fatal("no token row was written at all")
	}

	// And the hash must not be the token under another encoding either.
	var stored string
	if err := sqlDB.QueryRowContext(t.Context(),
		`SELECT token_hash FROM api_tokens LIMIT 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == tok {
		t.Fatal("token_hash is the raw token")
	}
	// Resolution still works, so the hash is of the right thing.
	if uid, err := st.ResolveToken(t.Context(), tok); err != nil || uid == 0 {
		t.Fatalf("token should still resolve: uid=%d err=%v", uid, err)
	}
}

// TestPasswordIsNotStoredPlaintext is the same guard for passwords.
func TestPasswordIsNotStoredPlaintext(t *testing.T) {
	sqlDB, err := db.Open(t.TempDir() + "/pw.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Migrate(t.Context(), sqlDB); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(sqlDB)

	const pw = "a very distinctive passphrase 12345"
	if _, _, err := st.RegisterUser(t.Context(), "ida", "Ida", pw); err != nil {
		t.Fatalf("register: %v", err)
	}
	var h string
	if err := sqlDB.QueryRowContext(t.Context(),
		`SELECT password_hash FROM users WHERE username='ida'`).Scan(&h); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h, pw) {
		t.Fatal("password appears verbatim in the stored hash")
	}
	if !strings.HasPrefix(h, "argon2id$") {
		t.Fatalf("expected an argon2id hash, got %.20q", h)
	}
}

// TestPreexistingAccountCannotLogInWithEmptyPassword: migration 0002 gave every
// existing user password_hash = ”. That must mean "cannot log in", not
// "password is the empty string".
func TestPreexistingAccountCannotLogInWithEmptyPassword(t *testing.T) {
	sqlDB, err := db.Open(t.TempDir() + "/legacy.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Migrate(t.Context(), sqlDB); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(sqlDB)

	// CreateUser is the pre-auth path: no credential.
	if _, err := st.CreateUser(t.Context(), "legacy", "Legacy"); err != nil {
		t.Fatalf("create legacy user: %v", err)
	}
	for _, attempt := range []string{"", " ", "legacy"} {
		if _, _, err := st.Login(t.Context(), "legacy", attempt); err == nil {
			t.Fatalf("legacy account logged in with %q", attempt)
		}
	}
	// Setting a password makes it usable.
	if err := st.SetPassword(t.Context(), "legacy", "a real password"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if _, _, err := st.Login(t.Context(), "legacy", "a real password"); err != nil {
		t.Fatalf("legacy account should work after SetPassword: %v", err)
	}
}

// TestRegisterRejectsShortPassword: the minimum exists so a password is not
// trivially guessable, and it must actually be enforced.
func TestRegisterRejectsShortPassword(t *testing.T) {
	ts := newAuthServer(t)
	res, _ := postJSON(t, ts, "/api/v1/auth/register", `{"username":"gina","password":"short"}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("short password: got %d, want 400", res.StatusCode)
	}
}

func TestDuplicateRegisterIsConflict(t *testing.T) {
	ts := newAuthServer(t)
	body := `{"username":"hank","password":"correct horse battery"}`
	postJSON(t, ts, "/api/v1/auth/register", body)
	res, _ := postJSON(t, ts, "/api/v1/auth/register", body)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate register: got %d, want 409", res.StatusCode)
	}
}

// TestComplaintDefaultsStrategicMultiplier pins the fix for a silent scoring
// failure: PainScore multiplies by the strategic multiplier, so a client that
// omitted the field got a pain score of exactly 0. The complaint was stored,
// validated and linked to a feature, and contributed nothing to its priority
// with nothing to indicate why. Omitting a field must mean "neutral", not
// "zero".
func TestComplaintDefaultsStrategicMultiplier(t *testing.T) {
	ts := newAuthServer(t)
	_, reg := postJSON(t, ts, "/api/v1/auth/register",
		`{"username":"ivy","password":"correct horse battery"}`)
	tok, _ := reg["token"].(string)
	const (
		ah = "Authorization"
		bt = "Bearer "
	)

	_, proj := postJSON(t, ts, "/api/v1/projects",
		`{"slug":"defaults","name":"Defaults"}`, ah, bt+tok)
	_ = proj

	// Deliberately omit strategic_multiplier.
	cres, comp := postJSON(t, ts, "/api/v1/projects/defaults/complaints",
		`{"project_id":1,"title":"No multiplier given","body":"b","severity":3,"frequency":0.8}`,
		ah, bt+tok)
	if cres.StatusCode != http.StatusCreated {
		t.Fatalf("create complaint: %d %v", cres.StatusCode, comp)
	}
	if got, _ := comp["strategic_multiplier"].(float64); got != 1.0 {
		t.Errorf("strategic_multiplier = %v, want the 1.0 default; a 0 here "+
			"silently zeroes this complaint's contribution to feature priority", got)
	}
}

// TestAuthPagesRender: the API having a login endpoint is not the same as a
// visitor being able to sign in. These pages are the only route to a token, so
// a template that fails to parse or a route that was never registered leaves
// the site permanently read-only with nothing visibly broken.
func TestAuthPagesRender(t *testing.T) {
	ts := newAuthServer(t)
	for _, p := range []string{"/login", "/register"} {
		res := getWith(t, ts, p)
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s: got %d, want 200", p, res.StatusCode)
		}
	}
}

// TestAuthPagesPostToTheAPI guards the wiring rather than the styling: the form
// must name the endpoints that exist. A typo here produces a 404 that the page
// TestAuthPagesReferenceRealEndpoints guards the wiring rather than the
// styling: the page must reference the script that talks to the API, and the
// endpoints that script calls must exist. A 404 on either leaves a form that
// does nothing on submit.
func TestAuthPagesReferenceRealEndpoints(t *testing.T) {
	ts := newAuthServer(t)

	for _, tc := range []struct{ path, mustContain string }{
		{"/login", `id="login-form"`},
		{"/login", "/assets/js/auth.js"},
		{"/register", `id="register-form"`},
		{"/register", "/assets/js/auth.js"},
		// The password field must be a password field. A typo that leaves
		// type="text" puts a password in plain sight on screen, and no
		// assertion on "does the page load" would notice.
		{"/login", `type="password"`},
	} {
		if body := htmlBody(t, ts, tc.path); !strings.Contains(body, tc.mustContain) {
			t.Errorf("%s does not contain %q", tc.path, tc.mustContain)
		}
	}

	// Every page carries the session script, so the header can reflect who is
	// signed in.
	for _, p := range []string{"/", "/projects", "/login", "/register"} {
		if body := htmlBody(t, ts, p); !strings.Contains(body, "/assets/js/session.js") {
			t.Errorf("%s does not load session.js, so the header cannot show the session", p)
		}
	}

	// And the endpoints the script calls must be routed. 404 means missing.
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/register", "/api/v1/auth/me"} {
		if res := getWith(t, ts, path); res.StatusCode == 404 {
			t.Errorf("%s is not routed; the sign-in form cannot work", path)
		}
	}
}

// htmlBody fetches a page and returns its text. Unlike getJSON it does not try
// to parse the response, because these routes serve HTML.
func htmlBody(t *testing.T, ts *httptest.Server, path string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// visitor, and the form doing nothing on submit.
func TestStaticAssetsAreServed(t *testing.T) {
	ts := newAuthServer(t)
	for _, p := range []string{"/assets/js/session.js", "/assets/js/auth.js", "/assets/js/project.js", "/assets/css/style.css"} {
		res := getWith(t, ts, p)
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s: got %d, want 200", p, res.StatusCode)
		}
	}
}
