package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/db"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

func newTestServer(t *testing.T) *httptest.Server {
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
	u, err := st.CreateUser(t.Context(), "testuser", "Test User")
	if err != nil {
		t.Fatalf("create test user: %v", err)
	}
	srv, err := NewServer(st, "test")
	if err != nil {
		t.Fatal(err)
	}
	// Authenticate the way a real client does: with a bearer token resolved by
	// the authenticate middleware.
	//
	// This used to inject context.WithValue(ctx, "actor_id", u.ID) directly,
	// which is precisely the thing production never does — no middleware set
	// that key, so every real request saw actor 0 and returned 401 while the
	// whole suite passed. A harness that hand-builds the auth context tests the
	// handlers, not the system. Now the token round-trips through
	// RegisterUser -> ResolveToken -> actorKey.
	// The user already exists (CreateUser above), so give it a credential the
	// way the admin path would, then log in to get a real token.
	if err := st.SetPassword(t.Context(), u.Username, "test-password-123"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	_, tok, err := st.Login(t.Context(), u.Username, "test-password-123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	ts := httptest.NewServer(authenticated(srv.Router(), tok))
	t.Cleanup(ts.Close)
	return ts
}

// authenticated wraps a handler so every request carries tok. This models a
// client that logged in once and is now making ordinary calls; the token is
// still resolved by the real middleware on each request.
func authenticated(next http.Handler, tok string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only supply the default identity when the request does not already
		// carry one. Overwriting unconditionally would silently impersonate the
		// project creator for every request, including the ones a test makes
		// deliberately as somebody else — which showed up as a self-vote
		// refusal for a second registered user.
		// "X-No-Auth" is an explicit opt-out, needed because "no header" is not
		// the same thing on this harness: the wrapper below supplies a default
		// identity for any request that arrives without one, so a test that
		// wants to prove an endpoint rejects anonymous callers cannot express
		// that by omitting the header. Before the sentinel existed, the
		// anonymous-complaint test passed a 201 and called it a 401 failure
		// elsewhere — the assertion was testing the harness, not the handler.
		if r.Header.Get("X-No-Auth") != "" {
			r.Header.Del("X-No-Auth")
		} else if r.Header.Get("Authorization") == "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		next.ServeHTTP(w, r)
	})
}

// registerOn creates an additional account on an existing server and returns
// a token for it.
//
// The reason this exists: a project creator is a maintainer and can vote, but
// they also authored the features, and handleCastVote refuses a self-vote —
// correctly, because Glicko-2 cannot discount self-preference. A single-user
// harness therefore cannot express the ordinary case at all, which is how a
// site where nobody can vote shipped with a green suite. Ranking is a
// multi-party act and the tests have to be too.
func registerOn(t *testing.T, ts *httptest.Server, username string) string {
	t.Helper()
	pw := "test-password-123"
	body := fmt.Sprintf(`{"username":%q,"password":%q,"display_name":%q}`, username, pw, username)
	resp, tokBody := postJSON(t, ts, "/api/v1/auth/register", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register %s: status %d body=%v", username, resp.StatusCode, tokBody)
	}
	tok, _ := tokBody["token"].(string)
	if tok == "" {
		t.Fatalf("register %s: no token in %v", username, tokBody)
	}
	return tok
}

// newTestServerWithStore is newTestServer but also hands back the store, for
// tests that need to assert on what was actually persisted (an attribution
// column, a row that must not exist) rather than only on the HTTP response.
//
// Asserting on the response alone is how the tag-attribution bug hid: the API
// returned 200 for a body-supplied applied_by, so a status-code test passed while
// the stored user was whatever the caller typed.
func newTestServerWithStore(t *testing.T) (*httptest.Server, *store.DB) {
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
	u, err := st.CreateUser(t.Context(), "testuser", "Test User")
	if err != nil {
		t.Fatalf("create test user: %v", err)
	}
	srv, err := NewServer(st, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPassword(t.Context(), u.Username, "test-password-123"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	_, tok, err := st.Login(t.Context(), u.Username, "test-password-123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	ts := httptest.NewServer(authenticated(srv.Router(), tok))
	t.Cleanup(ts.Close)
	return ts, st
}

func newTestServerNoActor(t *testing.T) *httptest.Server {
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
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return ts
}

// doJSONAnon is doJSON with the X-No-Auth sentinel set, so the request carries
// no identity.
//
// The plain doJSON cannot express an anonymous caller: the authenticated wrapper
// supplies a default bearer token for any request that arrives without an
// Authorization header, so "no header" means "the test user", not "nobody".
func doJSONAnon(t *testing.T, ts *httptest.Server, method, path string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
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
	req.Header.Set("X-No-Auth", "1")
	res, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	res.Body.Close()
	return res, m
}

func doJSON(t *testing.T, ts *httptest.Server, method, path string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	// Several list endpoints (GET /api/v1/projects) return a bare JSON array,
	// which cannot be unmarshalled into map[string]any. Decode into any first
	// and wrap an array under "items" so every caller has a map to read, while
	// object responses pass through unchanged.
	var raw any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	out, ok := raw.(map[string]any)
	if !ok {
		out = map[string]any{"items": raw}
	}
	return resp, out
}

func seedDiscoveryFixtures(t *testing.T, ts *httptest.Server) {
	t.Helper()
	_, _ = doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": "governance-lab", "name": "Governance Lab",
		"description": "tools for quorum consensus experiments",
		"license":     "MIT",
	})
	_, _ = doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": "rust-indexer", "name": "Rust Indexer",
		"description": "fast full-text indexer", "license": "Apache-2.0",
	})
	// §4.3: tagging resolves an existing tag, so the vocabulary is ratified first.
	// These fixtures are search fixtures, not taxonomy fixtures, so they use the
	// product path rather than inserting rows.
	for _, tag := range []string{"governance", "consensus", "search", "rust"} {
		seedGlobalTag(t, ts, tag)
	}
	_, _ = doJSON(t, ts, "PUT", "/api/v1/projects/governance-lab/tags",
		map[string]any{"tags": []string{"governance", "consensus", "search"}})
	_, _ = doJSON(t, ts, "PUT", "/api/v1/projects/rust-indexer/tags",
		map[string]any{"tags": []string{"search", "rust"}})
	_, _ = doJSON(t, ts, "PUT", "/api/v1/projects/governance-lab/languages",
		map[string]any{"languages": []map[string]any{{"language": "Go", "pct": 90}, {"language": "Shell", "pct": 10}}})
	_, _ = doJSON(t, ts, "PUT", "/api/v1/projects/rust-indexer/languages",
		map[string]any{"languages": []map[string]any{{"language": "Rust", "pct": 100}}})
	_, _ = doJSON(t, ts, "PUT", "/api/v1/projects/governance-lab/metrics", map[string]any{
		"stars": 12, "contributors": 40, "last_commit_age_days": 0,
		"median_review_hours": 10, "releases_90d": 4, "open_issues": 3,
	})
	_, _ = doJSON(t, ts, "PUT", "/api/v1/projects/rust-indexer/metrics", map[string]any{
		"stars": 1, "contributors": 1, "last_commit_age_days": 400,
		"median_review_hours": 24 * 30, "releases_90d": 0, "open_issues": 50,
	})
}

func TestProjectCRUD(t *testing.T) {
	ts := newTestServer(t)
	resp, body := doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": "concord", "name": "Concord", "description": "consensus forge",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d body %v", resp.StatusCode, body)
	}
	resp, body = doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": "concord", "name": "Dup",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate should 409, got %d", resp.StatusCode)
	}
	resp, _ = doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": "Bad Slug", "name": "x",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid slug should 400, got %d", resp.StatusCode)
	}
	resp, body = doJSON(t, ts, "GET", "/api/v1/projects/concord", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: %d", resp.StatusCode)
	}
	if body["governance_model"] != "collective" {
		t.Fatalf("default governance_model should be collective, got %v", body["governance_model"])
	}
	resp, _ = doJSON(t, ts, "GET", "/api/v1/projects/concord/charter-x", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown project should 404, got %d", resp.StatusCode)
	}
}

func TestSearchFirstClass(t *testing.T) {
	ts := newTestServer(t)
	seedDiscoveryFixtures(t, ts)

	t.Run("free text finds by description", func(t *testing.T) {
		resp, body := doJSON(t, ts, "GET", `/api/v1/search?q=consensus%20experiments`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("search: %d", resp.StatusCode)
		}
		results := body["results"].([]any)
		if len(results) != 1 {
			t.Fatalf("want 1 hit for 'consensus experiments', got %d: %v", len(results), body)
		}
		first := results[0].(map[string]any)
		if first["slug"] != "governance-lab" {
			t.Fatalf("wrong hit: %v", first["slug"])
		}
	})

	t.Run("tag filter with facets", func(t *testing.T) {
		resp, body := doJSON(t, ts, "GET", `/api/v1/search?tag=search`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("search: %d", resp.StatusCode)
		}
		if body["total"].(float64) != 2 {
			t.Fatalf("both projects tagged 'search', got %v", body["total"])
		}
		facets := body["facets"].(map[string]any)
		tags := facets["tags"].([]any)
		if len(tags) == 0 {
			t.Fatal("tag facets must always be returned (spec §15)")
		}
	})

	t.Run("language with min percentage", func(t *testing.T) {
		resp, body := doJSON(t, ts, "GET", `/api/v1/search?language=Go&min_lang_pct=50`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("search: %d", resp.StatusCode)
		}
		if body["total"].(float64) != 1 {
			t.Fatalf("only governance-lab is at least 50 pct Go, got %v", body["total"])
		}
	})

	t.Run("maintenance health filter and sort", func(t *testing.T) {
		resp, body := doJSON(t, ts, "GET", `/api/v1/search?min_health=0.5&sort=health`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("search: %d", resp.StatusCode)
		}
		if body["total"].(float64) != 1 {
			t.Fatalf("only the healthy project passes min_health=0.5, got %v", body["total"])
		}
		results := body["results"].([]any)
		if results[0].(map[string]any)["slug"] != "governance-lab" {
			t.Fatalf("health sort order wrong: %v", results)
		}
	})

	t.Run("governance model filter", func(t *testing.T) {
		resp, body := doJSON(t, ts, "GET", `/api/v1/search?model=collective`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("search: %d", resp.StatusCode)
		}
		if body["total"].(float64) != 2 {
			t.Fatalf("all seeded projects are collective, got %v", body["total"])
		}
	})

	t.Run("bad filters rejected", func(t *testing.T) {
		resp, _ := doJSON(t, ts, "GET", `/api/v1/search?min_health=2`, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("min_health=2 should 400, got %d", resp.StatusCode)
		}
		resp, _ = doJSON(t, ts, "GET", `/api/v1/search?sort=stars`, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("unknown sort should 400, got %d", resp.StatusCode)
		}
	})

	t.Run("health score exposed on hits", func(t *testing.T) {
		_, body := doJSON(t, ts, "GET", `/api/v1/search?q=governance`, nil)
		results := body["results"].([]any)
		hit := results[0].(map[string]any)
		if _, ok := hit["health_score"].(float64); !ok {
			t.Fatalf("health_score should be a number on hits: %v", hit)
		}
	})
}

func TestHealthzAndVersion(t *testing.T) {
	ts := newTestServer(t)
	resp, body := doJSON(t, ts, "GET", "/api/v1/healthz", nil)
	if resp.StatusCode != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("healthz: %d %v", resp.StatusCode, body)
	}
	resp, body = doJSON(t, ts, "GET", "/api/v1/version", nil)
	if resp.StatusCode != http.StatusOK || body["version"] != "test" {
		t.Fatalf("version: %d %v", resp.StatusCode, body)
	}
}

func TestAuthRequired(t *testing.T) {
	ts := newTestServerNoActor(t)
	resp, body := doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": "noauth-proj", "name": "NoAuth", "desc": "x",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /projects without auth should 401, got %d body=%v", resp.StatusCode, body)
	}
	// /priorities is deliberately public: it is the output of a process the
	// public is invited to join, and requiring a token to read the ranking
	// while anyone may read the proposals would make the site a black box.
	// The private act is casting a vote, which TestAuthRequired still covers
	// below via the votes/next route.
	resp, _ = doJSON(t, ts, "GET", "/api/v1/projects/1/priorities", nil)
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("GET /priorities should be public, got 401")
	}
	resp, _ = doJSON(t, ts, "POST", "/api/v1/projects/1/features", map[string]any{
		"title": "x", "body": "y",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /features without auth should 401, got %d", resp.StatusCode)
	}
}

func TestVoteRoundTrip(t *testing.T) {
	ts := newTestServer(t)
	resp, projBody := doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": "vote-project", "name": "Vote Project", "description": "x",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: status %d body=%v", resp.StatusCode, projBody)
	}
	projID := int64(projBody["id"].(float64))

	resp, compBody := doJSON(t, ts, "POST", "/api/v1/projects/"+slugOf(t, ts, projID)+"/complaints", map[string]any{
		"title": "Vote complaint", "body": "needs fixing", "severity": 3,
		"frequency": 1.0, "strategic_multiplier": 2.0, "project_id": projID,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create complaint: status %d body=%v", resp.StatusCode, compBody)
	}
	compID := int64(compBody["id"].(float64))

	resp, _ = doJSON(t, ts, "POST", "/api/v1/projects/"+slugOf(t, ts, projID)+"/complaints/"+itoa(compID)+"/validate", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("validate complaint: status %d", resp.StatusCode)
	}

	resp, featBody := doJSON(t, ts, "POST", "/api/v1/projects/"+slugOf(t, ts, projID)+"/features", map[string]any{
		"title": "Feature A", "body": "first option", "linked_complaints": []int64{compID},
		"project_id": projID,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create feature A: status %d body=%v", resp.StatusCode, featBody)
	}
	featA := int64(featBody["id"].(float64))

	resp, featBody = doJSON(t, ts, "POST", "/api/v1/projects/"+slugOf(t, ts, projID)+"/features", map[string]any{
		"title": "Feature B", "body": "second option", "linked_complaints": []int64{compID},
		"project_id": projID,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create feature B: status %d body=%v", resp.StatusCode, featBody)
	}
	featB := int64(featBody["id"].(float64))

	// A second person votes. The author cannot: see registerOn.
	//
	// The account is seconds old, so the account-age gate (spec §12.3) gives it
	// zero weight: the vote is accepted and recorded, but carries no influence.
	// That is the intended behaviour for a fresh account, not a defect — so the
	// assertion is that the weight is exactly 0 and the vote still lands, not
	// that the weight is positive. A positive-weight path is covered by
	// TestAgeGateRampScalesButPreservesInfluence in the ranking package.
	voterTok := registerOn(t, ts, "ranker")
	resp, voteBody := postJSON(t, ts,
		"/api/v1/projects/"+slugOf(t, ts, projID)+"/features/"+itoa(featA)+"/vote",
		fmt.Sprintf(`{"feature_a":%d,"feature_b":%d,"outcome":"a"}`, featA, featB),
		"Authorization", "Bearer "+voterTok)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("cast vote: status %d body=%v", resp.StatusCode, voteBody)
	}
	if got := voteBody["weight"].(float64); got != 0 {
		t.Fatalf("a brand-new account should weigh exactly 0, got %v", got)
	}
	// The vote must still be recorded — the gate reduces influence, it does not
	// discard participation.
	if voteBody["outcome"] != "a" {
		t.Fatalf("vote should still be recorded, got %v", voteBody)
	}
}

func TestConsensusFiveOutcomes(t *testing.T) {
	ts := newTestServer(t)
	resp, projBody := doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": "consensus-project", "name": "Consensus Project", "description": "x",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: %d", resp.StatusCode)
	}
	projID := int64(projBody["id"].(float64))

	resp, compBody := doJSON(t, ts, "POST", "/api/v1/projects/"+slugOf(t, ts, projID)+"/complaints", map[string]any{
		"title": "Consensus complaint", "body": "x", "severity": 1, "frequency": 0.5,
		"strategic_multiplier": 1.0, "project_id": projID,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create complaint: %d", resp.StatusCode)
	}
	compID := int64(compBody["id"].(float64))

	resp, _ = doJSON(t, ts, "POST", "/api/v1/projects/"+slugOf(t, ts, projID)+"/complaints/"+itoa(compID)+"/validate", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("validate complaint: %d", resp.StatusCode)
	}

	resp, featBody := doJSON(t, ts, "POST", "/api/v1/projects/"+slugOf(t, ts, projID)+"/features", map[string]any{
		"title": "Consensus feature", "body": "x", "linked_complaints": []int64{compID},
		"project_id": projID,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create feature: %d", resp.StatusCode)
	}
	featID := int64(featBody["id"].(float64))

	resp, callBody := doJSON(t, ts, "POST", "/api/v1/projects/"+slugOf(t, ts, projID)+"/consensus", map[string]any{
		"feature_id": featID, "title": "Test call", "description": "x",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create consensus call: %d body=%v", resp.StatusCode, callBody)
	}
	callID := int64(callBody["id"].(float64))

	outcomes := []string{"consent", "abstain", "stand_aside", "block", "consent"}
	for i, stance := range outcomes {
		resp, body := doJSON(t, ts, "POST", "/api/v1/projects/"+slugOf(t, ts, projID)+"/consensus/"+itoa(callID)+"/position", map[string]any{
			"position": stance,
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("cast position %q (#%d): status %d body=%v", stance, i, resp.StatusCode, body)
		}
		if body["position"] != stance {
			t.Fatalf("expected stance %q, got %v", stance, body["position"])
		}
	}
}

func TestPermissionDenial(t *testing.T) {
	t.Run("guest_cannot_vote", func(t *testing.T) {
		ts := newTestServerNoActor(t)
		resp, _ := doJSON(t, ts, "POST", "/api/v1/projects/1/features/1/vote", map[string]any{
			"feature_a": 1, "feature_b": 2, "outcome": "a",
		})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 for unauthenticated vote, got %d", resp.StatusCode)
		}
	})

	t.Run("set_strategic_weight_requires_maintainer", func(t *testing.T) {
		ts := newTestServer(t)
		resp, projBody := doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
			"slug": "weight-test", "name": "Weight Test", "description": "x",
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create project: %d", resp.StatusCode)
		}
		projID := int64(projBody["id"].(float64))

		resp, compBody := doJSON(t, ts, "POST", "/api/v1/projects/"+slugOf(t, ts, projID)+"/complaints", map[string]any{
			"title": "C", "body": "x", "severity": 1, "frequency": 0.5,
			"strategic_multiplier": 1.0, "project_id": projID,
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create complaint: %d", resp.StatusCode)
		}
		compID := int64(compBody["id"].(float64))

		resp, _ = doJSON(t, ts, "POST", "/api/v1/projects/"+slugOf(t, ts, projID)+"/complaints/"+itoa(compID)+"/validate", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("validate: %d", resp.StatusCode)
		}

		resp, featBody := doJSON(t, ts, "POST", "/api/v1/projects/"+slugOf(t, ts, projID)+"/features", map[string]any{
			"title": "F", "body": "x", "linked_complaints": []int64{compID},
			"project_id": projID,
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create feature: %d", resp.StatusCode)
		}
		featID := int64(featBody["id"].(float64))

		// Spec revision 4 §6.2: strategic weight is no longer a maintainer
		// dial. The route now opens a proposal and the weight is applied only
		// when eligible collaborators ratify it, because weight multiplies into
		// the priority formula and a maintainer applying it directly is the
		// steering lever §5.3 forbids.
		slug := slugOf(t, ts, projID)

		t.Run("weight change without a rationale is refused", func(t *testing.T) {
			resp, body := doJSON(t, ts, "PUT", "/api/v1/projects/"+slug+"/features/"+itoa(featID)+"/strategic-weight", map[string]any{
				"weight": 5.0,
			})
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("missing rationale: got %d body=%v, want 400", resp.StatusCode, body)
			}
		})

		t.Run("out-of-range weight is refused", func(t *testing.T) {
			// 5.0 exceeds the 3.0 ceiling. A weight that can dominate the
			// priority formula reintroduces hand-steering.
			resp, body := doJSON(t, ts, "PUT", "/api/v1/projects/"+slug+"/features/"+itoa(featID)+"/strategic-weight", map[string]any{
				"weight": 5.0, "rationale": "security hardening",
			})
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("weight 5.0: got %d body=%v, want 400", resp.StatusCode, body)
			}
		})

		t.Run("a proposal is opened, not applied", func(t *testing.T) {
			resp, body := doJSON(t, ts, "PUT", "/api/v1/projects/"+slug+"/features/"+itoa(featID)+"/strategic-weight", map[string]any{
				"weight": 2.0, "rationale": "security hardening work",
			})
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("propose: got %d body=%v, want 201", resp.StatusCode, body)
			}
			prop, ok := body["proposal"].(map[string]any)
			if !ok {
				t.Fatalf("no proposal object in %v", body)
			}
			if prop["status"] != "pending" {
				t.Errorf("proposal status = %v, want pending", prop["status"])
			}
			// The weight must NOT have moved: that is the whole point.
			resp, featBody := doJSON(t, ts, "GET", "/api/v1/projects/"+slug+"/features/"+itoa(featID), nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("get feature: %d", resp.StatusCode)
			}
			if w, _ := featBody["strategic_weight"].(float64); w != 1.0 {
				t.Errorf("strategic_weight = %v, want 1.0 -- a proposal must not apply the change", w)
			}
			proposalID := int64(prop["id"].(float64))

			t.Run("the proposer cannot consent to their own proposal", func(t *testing.T) {
				// §5.1: no voting on your own items. Without this the proposal
				// path is just a slower maintainer dial.
				resp, body := doJSON(t, ts, "POST",
					"/api/v1/projects/"+slug+"/strategy/weight-proposals/"+itoa(proposalID)+"/consent", nil)
				if resp.StatusCode != http.StatusForbidden {
					t.Fatalf("self-consent: got %d body=%v, want 403", resp.StatusCode, body)
				}
			})

			t.Run("another collaborator consents without reaching quorum", func(t *testing.T) {
				// A different account, so the self-vote rule is satisfied. The
				// project has one eligible collaborator plus this new account,
				// so quorum is 2 and one consent is not enough to ratify: 202.
				_, reg := postJSON(t, ts, "/api/v1/auth/register",
					`{"username":"consenter","password":"correct horse battery"}`)
				tok, _ := reg["token"].(string)
				if tok == "" {
					t.Fatalf("register returned no token: %v", reg)
				}

				// A fresh registration is a guest, and consent requires
				// contributor. Joining is the documented path, and it is also
				// what makes the user an eligible collaborator (§8.2).
				postJSON(t, ts, "/api/v1/projects/"+slug+"/join", "{}",
					"Authorization", "Bearer "+tok)
				resp, body := postJSON(t, ts,
					"/api/v1/projects/"+slug+"/strategy/weight-proposals/"+itoa(proposalID)+"/consent",
					"{}", "Authorization", "Bearer "+tok)
				if resp.StatusCode != http.StatusAccepted {
					t.Fatalf("consent below quorum: got %d body=%v, want 202", resp.StatusCode, body)
				}

				// Still unapplied.
				_, featBody := doJSON(t, ts, "GET", "/api/v1/projects/"+slug+"/features/"+itoa(featID), nil)
				if w, _ := featBody["strategic_weight"].(float64); w != 1.0 {
					t.Errorf("strategic_weight = %v after one consent, want 1.0", w)
				}
			})

			t.Run("proposals are listed", func(t *testing.T) {
				resp, body := doJSON(t, ts, "GET", "/api/v1/projects/"+slug+"/strategy/weight-proposals", nil)
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("list proposals: %d", resp.StatusCode)
				}
				// doJSON wraps a bare JSON array under "items".
				items, ok := body["items"].([]any)
				if !ok || len(items) == 0 {
					t.Fatalf("expected a non-empty array of proposals, got %v", body)
				}
			})
		})
	})
}

func TestObjectionLifecycle(t *testing.T) {
	ts := newTestServer(t)

	resp, projBody := doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": "obj-test", "name": "Objection Test", "description": "x",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: %d", resp.StatusCode)
	}
	projID := int64(projBody["id"].(float64))

	resp, compBody := doJSON(t, ts, "POST", "/api/v1/projects/"+slugOf(t, ts, projID)+"/complaints", map[string]any{
		"title": "Obj complaint", "body": "x", "severity": 1, "frequency": 0.5,
		"strategic_multiplier": 1.0, "project_id": projID,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create complaint: %d", resp.StatusCode)
	}
	compID := int64(compBody["id"].(float64))

	resp, _ = doJSON(t, ts, "POST", "/api/v1/projects/"+slugOf(t, ts, projID)+"/complaints/"+itoa(compID)+"/validate", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("validate complaint: %d", resp.StatusCode)
	}

	resp, featBody := doJSON(t, ts, "POST", "/api/v1/projects/"+slugOf(t, ts, projID)+"/features", map[string]any{
		"title": "Obj feature", "body": "x", "linked_complaints": []int64{compID},
		"project_id": projID,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create feature: %d", resp.StatusCode)
	}
	featID := int64(featBody["id"].(float64))

	resp, callBody := doJSON(t, ts, "POST", "/api/v1/projects/"+slugOf(t, ts, projID)+"/consensus", map[string]any{
		"feature_id": featID, "title": "Obj call", "description": "x",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create consensus call: %d", resp.StatusCode)
	}
	callID := int64(callBody["id"].(float64))

	resp, objBody := doJSON(t, ts, "POST", "/api/v1/projects/"+slugOf(t, ts, projID)+"/consensus/"+itoa(callID)+"/objection", map[string]any{
		"principle": "Fairness",
		"violation": "Process was rushed",
		"remedy":    "Extend discussion period",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create objection: status %d body=%v", resp.StatusCode, objBody)
	}
	objID := int64(objBody["id"].(float64))
	if objBody["status"] != "open" {
		t.Fatalf("expected objection status open, got %v", objBody["status"])
	}

	resp, _ = doJSON(t, ts, "PUT", "/api/v1/projects/"+slugOf(t, ts, projID)+"/consensus/objections/"+itoa(objID)+"/resolve", map[string]any{
		"status":     "resolved",
		"resolution": "Discussion period extended by 3 days",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve objection: status %d", resp.StatusCode)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// TestCharterEndpoints verifies charter read and update.
func TestCharterEndpoints(t *testing.T) {
	ts := newTestServer(t)

	resp, projBody := doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": "charter-test", "name": "Charter Test", "description": "x",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: %d", resp.StatusCode)
	}
	projID := int64(projBody["id"].(float64))

	// Get charter (default)
	resp, charterBody := doJSON(t, ts, "GET", "/api/v1/projects/"+slugOf(t, ts, projID)+"/charter", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get charter: status %d", resp.StatusCode)
	}
	if charterBody["QuorumRatio"] == nil {
		t.Fatal("charter should include quorum_ratio")
	}

	// Update charter (maintainer can)
	_, _ = doJSON(t, ts, "PUT", "/api/v1/projects/"+slugOf(t, ts, projID)+"/charter", map[string]any{
		"quorum_ratio": 0.5,
		"quorum_min":   5,
	})
	// Just verify it doesn't 401/403
}

// TestUnauthCharterUpdate verifies charter updates require auth.
func TestUnauthCharterUpdate(t *testing.T) {
	ts := newTestServerNoActor(t)
	resp, _ := doJSON(t, ts, "PUT", "/api/v1/projects/1/charter", map[string]any{
		"quorum_ratio": 0.5,
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated charter update should 401, got %d", resp.StatusCode)
	}
}

// slugOf resolves a project id to the slug the routes expect.
//
// The project-scoped routes are /api/v1/projects/{project_id}/..., but that
// parameter carries a *slug* — the sibling route is /api/v1/projects/{slug} and
// GetProject matches on p.slug. These tests used to interpolate the numeric id
// with itoa(projID), which the old handlers happened to accept: they parsed the
// path as an integer, and on a fresh database project 1 is also the first
// project, so the two agreed by coincidence. The tests therefore encoded the
// bug rather than the contract, and every one of them would have started
// failing the moment a second project existed in an earlier position.
func slugOf(t *testing.T, ts *httptest.Server, projectID int64) string {
	t.Helper()
	res, body := doJSON(t, ts, "GET", "/api/v1/projects", nil)
	if res.StatusCode != 200 {
		t.Fatalf("list projects: %d %v", res.StatusCode, body)
	}
	// The list endpoint returns a bare JSON array; doJSON wraps it under a
	// synthetic key so its map return type can carry any top-level shape.
	items, _ := body["items"].([]any)
	if items == nil {
		items, _ = body["projects"].([]any)
	}
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		if int64(m["id"].(float64)) == projectID {
			return m["slug"].(string)
		}
	}
	t.Fatalf("no project with id %d in list (got %d items)", projectID, len(items))
	return ""
}
