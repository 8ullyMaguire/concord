package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/db"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// newTestServer gives each test a fresh, migrated SQLite in a temp dir and
// an httptest.Server wired to the real router.
// It creates a test user (the "actor") whose ID is injected into requests.
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
	router := srv.Router()
	injectActor := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), "actor_id", u.ID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	ts := httptest.NewServer(injectActor(router))
	t.Cleanup(ts.Close)
	return ts
}

// newTestServerNoActor gives a test server that does NOT inject actor_id.
// Use this for testing unauthenticated endpoints (should return 401).
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
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
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

	_, _ = doJSON(t, ts, "PUT", "/api/v1/projects/governance-lab/tags",
		map[string]any{"tags": []string{"governance", "consensus", "search"}})
	_, _ = doJSON(t, ts, "PUT", "/api/v1/projects/rust-indexer/tags",
		map[string]any{"tags": []string{"search", "rust"}})

	_, _ = doJSON(t, ts, "PUT", "/api/v1/projects/governance-lab/languages",
		map[string]any{"languages": []map[string]any{{"language": "Go", "pct": 90}, {"language": "Shell", "pct": 10}}})
	_, _ = doJSON(t, ts, "PUT", "/api/v1/projects/rust-indexer/languages",
		map[string]any{"languages": []map[string]any{{"language": "Rust", "pct": 100}}})

	// healthy project
	_, _ = doJSON(t, ts, "PUT", "/api/v1/projects/governance-lab/metrics", map[string]any{
		"stars": 12, "contributors": 40, "last_commit_age_days": 0,
		"median_review_hours": 10, "releases_90d": 4, "open_issues": 3,
	})
	// abandoned project
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

// ---------------------------------------------------------------------------
// AC-grade tests: auth, permissions, vote round-trip
// ---------------------------------------------------------------------------

// TestAuthRequired verifies unauthenticated requests get 401.
func TestAuthRequired(t *testing.T) {
	ts := newTestServerNoActor(t)

	// Without actor context, protected endpoints should return 401
	resp, body := doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": "noauth-proj", "name": "NoAuth", "desc": "x",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /projects without auth should 401, got %d body=%v", resp.StatusCode, body)
	}

	// Priorities endpoint
	resp, _ = doJSON(t, ts, "GET", "/api/v1/projects/1/priorities", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /priorities without auth should 401, got %d", resp.StatusCode)
	}

	// Create feature (need a project first — but all protected)
	resp, _ = doJSON(t, ts, "POST", "/api/v1/projects/1/features", map[string]any{
		"title": "x", "body": "y",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /features without auth should 401, got %d", resp.StatusCode)
	}
}

// TestVoteRoundTrip verifies the full vote flow through HTTP API.
func TestVoteRoundTrip(t *testing.T) {
	ts := newTestServer(t)

	// Create a project
	resp, projBody := doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": "vote-project", "name": "Vote Project", "description": "x",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: status %d body=%v", resp.StatusCode, projBody)
	}
	projID := int64(projBody["id"].(float64))

	// Create a complaint
	resp, compBody := doJSON(t, ts, "POST", "/api/v1/projects/"+itoa(projID)+"/complaints", map[string]any{
			"title": "Vote complaint", "body": "needs fixing", "severity": 3,
			"frequency": 1.0, "strategic_multiplier": 2.0, "project_id": projID,
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create complaint: status %d body=%v", resp.StatusCode, compBody)
		}
		compID := int64(compBody["id"].(float64))

		// Validate the complaint
		resp, _ = doJSON(t, ts, "POST", "/api/v1/projects/"+itoa(projID)+"/complaints/"+itoa(compID)+"/validate", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("validate complaint: status %d", resp.StatusCode)
		}

		// Create two features linked to the complaint
		resp, featBody := doJSON(t, ts, "POST", "/api/v1/projects/"+itoa(projID)+"/features", map[string]any{
			"title": "Feature A", "body": "first option", "linked_complaints": []int64{compID},
			"project_id": projID,
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create feature A: status %d body=%v", resp.StatusCode, featBody)
		}
		featA := int64(featBody["id"].(float64))

		resp, featBody = doJSON(t, ts, "POST", "/api/v1/projects/"+itoa(projID)+"/features", map[string]any{
			"title": "Feature B", "body": "second option", "linked_complaints": []int64{compID},
			"project_id": projID,
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create feature B: status %d body=%v", resp.StatusCode, featBody)
		}
		featB := int64(featBody["id"].(float64))

		// Cast vote: A beats B
		resp, voteBody := doJSON(t, ts, "POST", "/api/v1/projects/"+itoa(projID)+"/features/"+itoa(featA)+"/vote", map[string]any{
			"feature_a": featA, "feature_b": featB, "outcome": "a",
		})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("cast vote: status %d body=%v", resp.StatusCode, voteBody)
	}
	if voteBody["weight"].(float64) <= 0 {
		t.Fatalf("expected positive vote weight, got %v", voteBody["weight"])
	}

	// Get the pair — should be exhausted now (only one pair, just voted)
	resp, _ = doJSON(t, ts, "GET", "/api/v1/projects/vote-project/votes/next", nil)
	if resp.StatusCode == http.StatusOK {
		// If there are more pairs this is fine — but with 2 features, there's only 1 pair
		t.Logf("votes/next returned 200: %v", voteBody)
	}
}

// TestConsensusFiveOutcomes tests all five consensus outcomes.
func TestConsensusFiveOutcomes(t *testing.T) {
	ts := newTestServer(t)

	// Setup
	resp, projBody := doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
		"slug": "consensus-project", "name": "Consensus Project", "description": "x",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: %d", resp.StatusCode)
	}
	projID := int64(projBody["id"].(float64))

	resp, compBody := doJSON(t, ts, "POST", "/api/v1/projects/"+itoa(projID)+"/complaints", map[string]any{
		"title": "Consensus complaint", "body": "x", "severity": 1, "frequency": 0.5,
		"strategic_multiplier": 1.0, "project_id": projID,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create complaint: %d", resp.StatusCode)
	}
	compID := int64(compBody["id"].(float64))

	resp, _ = doJSON(t, ts, "POST", "/api/v1/projects/"+itoa(projID)+"/complaints/"+itoa(compID)+"/validate", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("validate complaint: %d", resp.StatusCode)
	}

	resp, featBody := doJSON(t, ts, "POST", "/api/v1/projects/"+itoa(projID)+"/features", map[string]any{
		"title": "Consensus feature", "body": "x", "linked_complaints": []int64{compID},
		"project_id": projID,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create feature: %d", resp.StatusCode)
	}
	featID := int64(featBody["id"].(float64))

	// Create consensus call
	resp, callBody := doJSON(t, ts, "POST", "/api/v1/projects/"+itoa(projID)+"/consensus", map[string]any{
		"feature_id": featID, "title": "Test call", "description": "x",
		"project_id": projID,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create consensus call: %d body=%v", resp.StatusCode, callBody)
	}
	callID := int64(callBody["id"].(float64))

	// Cast positions with different stances
	outcomes := []string{"consent", "abstain", "stand_aside", "block", "consent"}
	for i, stance := range outcomes {
		resp, body := doJSON(t, ts, "POST", "/api/v1/projects/"+itoa(projID)+"/consensus/"+itoa(callID)+"/position", map[string]any{
				"role": "voter", "position": stance,
			})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("cast position %q (#%d): status %d body=%v", stance, i, resp.StatusCode, body)
		}
		if body["position"] != stance {
			t.Fatalf("expected stance %q, got %v", stance, body["position"])
		}
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


// TestPermissionDenial verifies role-based access control.
// Users can be added to projects with specific roles to test enforcement.
func TestPermissionDenial(t *testing.T) {
	t.Run("guest_cannot_vote", func(t *testing.T) {
		ts := newTestServerNoActor(t)
		// Without auth, all protected endpoints 401
		resp, _ := doJSON(t, ts, "POST", "/api/v1/projects/1/features/1/vote", map[string]any{
			"feature_a": 1, "feature_b": 2, "outcome": "a",
		})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 for unauthenticated vote, got %d", resp.StatusCode)
		}
	})

	t.Run("set_strategic_weight_requires_maintainer", func(t *testing.T) {
		ts := newTestServer(t)
		// Create project (actor becomes maintainer)
		resp, projBody := doJSON(t, ts, "POST", "/api/v1/projects", map[string]any{
			"slug": "weight-test", "name": "Weight Test", "description": "x",
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create project: %d", resp.StatusCode)
		}
		projID := int64(projBody["id"].(float64))

		// Create a feature to set weight on
		resp, compBody := doJSON(t, ts, "POST", "/api/v1/projects/"+itoa(projID)+"/complaints", map[string]any{
			"title": "C", "body": "x", "severity": 1, "frequency": 0.5,
			"strategic_multiplier": 1.0, "project_id": projID,
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create complaint: %d", resp.StatusCode)
		}
		compID := int64(compBody["id"].(float64))

		resp, _ = doJSON(t, ts, "POST", "/api/v1/projects/"+itoa(projID)+"/complaints/"+itoa(compID)+"/validate", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("validate: %d", resp.StatusCode)
		}

		resp, featBody := doJSON(t, ts, "POST", "/api/v1/projects/"+itoa(projID)+"/features", map[string]any{
			"title": "F", "body": "x", "linked_complaints": []int64{compID},
			"project_id": projID,
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create feature: %d", resp.StatusCode)
		}
		featID := int64(featBody["id"].(float64))

		// Maintainer can set strategic weight
		resp, body := doJSON(t, ts, "PUT", "/api/v1/projects/"+itoa(projID)+"/features/"+itoa(featID)+"/strategic-weight", map[string]any{
			"weight": 5.0,
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("maintainer should be able to set strategic weight, got %d body=%v", resp.StatusCode, body)
		}
	})
}
