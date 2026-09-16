package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/db"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// newTestServer gives each test a fresh, migrated SQLite in a temp dir and
// an httptest.Server wired to the real router.
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
	srv, err := NewServer(store.New(sqlDB), "test")
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
