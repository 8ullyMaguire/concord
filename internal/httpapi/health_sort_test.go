package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// R9 / spec §4.5.7.1's two broken surfaces, pinned.
//
// `sort=health` and `min_health` were reported as broken on the live instance: the filter
// returned nothing for every value > 0 and the sort produced the same order as `sort=updated`.
// Both were measured, and both were TRUE -- but the cause was not the SQL. It was that
// `project_metrics` held 71 rows and a health score on none of them, so every project was
// unmeasured and any ordering of "unmeasured" is `sort=updated`.
//
// That distinction is recorded here rather than in a comment on a rewritten query, because the
// plan prescribed a predicate fix (`COALESCE(m.health_score, 0) >= ?` -> `m.health_score >= ?`)
// and the two predicates are EQUIVALENT for every reachable input: the filter only runs when
// `min_health > 0`, and for x NULL both forms are false while for any real x they agree.
// Changing the SQL would have been churn that no test could see, and this test proves the
// equivalence instead of asserting a fix that is not one.
//
// The repair is therefore DATA (cmd/healthsync populating scores), and these assertions are
// what fails if a future change makes the surfaces lie again: an unmeasured project must never
// appear in a minimum-health result, and must never outrank a measured one.

func seedHealthFixtures(t *testing.T) (*httptest.Server, map[string]int64) {
	t.Helper()
	ts, st := newTestServerWithStore(t)
	ctx := context.Background()
	owner, err := st.GetUser(ctx, "testuser")
	if err != nil {
		t.Fatalf("read the test user: %v", err)
	}
	ids := map[string]int64{}
	for _, slug := range []string{"healthy", "sick", "unmeasured"} {
		p, err := st.CreateProject(ctx, owner.ID, slug, slug, "", "collective", "")
		if err != nil {
			t.Fatalf("CreateProject(%s): %v", slug, err)
		}
		ids[slug] = p.ID
	}
	// Written directly rather than through UpdateProjectMetrics: this test is about what the
	// SEARCH does with a stored score, and routing the fixture through the scorer would make a
	// failure here ambiguous between the two. The row already exists (CreateProject inserts one
	// with a NULL score), so this is an UPDATE -- an INSERT here collides on project_id, which
	// is how this fixture failed first.
	for slug, health := range map[string]float64{"healthy": 0.9, "sick": 0.2} {
		res, err := st.ExecContext(ctx,
			`UPDATE project_metrics SET health_score=?, computed_at=0 WHERE project_id=?`,
			health, ids[slug])
		if err != nil {
			t.Fatalf("set metrics for %s: %v", slug, err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			t.Fatalf("setting metrics for %s affected %d rows; the fixture is not measuring "+
				"what it thinks it is", slug, n)
		}
	}
	// Distinct updated_at values, newest = the SICK project, so that `sort=updated` and
	// `sort=health` MUST disagree. Without this the two orders can coincide by accident on
	// equal timestamps and the assertion below would pass without the health sort reading
	// anything at all.
	for slug, age := range map[string]float64{"sick": 0, "unmeasured": 3600, "healthy": 7200} {
		if _, err := st.ExecContext(ctx,
			`UPDATE projects SET updated_at=? WHERE id=?`, 1_700_000_000-age, ids[slug]); err != nil {
			t.Fatalf("set updated_at for %s: %v", slug, err)
		}
	}
	return ts, ids
}

func searchSlugs(t *testing.T, ts *httptest.Server, query string) []string {
	t.Helper()
	code, body := authJSON(t, ts, http.MethodGet, "/api/v1/search?"+query, anonymousMarker, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /search?%s = %d: %s", query, code, body)
	}
	var res struct {
		Results []struct {
			Slug        string   `json:"slug"`
			HealthScore *float64 `json:"health_score"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("search response is not JSON: %s", body)
	}
	out := make([]string, 0, len(res.Results))
	for _, r := range res.Results {
		out = append(out, r.Slug)
	}
	return out
}

func TestMinHealthMatchesMeasuredProjectsOnly(t *testing.T) {
	ts, _ := seedHealthFixtures(t)

	got := searchSlugs(t, ts, "q=&min_health=0.5")
	if len(got) != 1 || got[0] != "healthy" {
		t.Fatalf("min_health=0.5 returned %v, want exactly [healthy]: an unmeasured project "+
			"cannot satisfy a minimum, and a sick one is below it", got)
	}
	// The control: the same filter at a value BELOW the sick project must return both
	// measured projects and still exclude the unmeasured one. Without this, the assertion
	// above passes for a filter that simply returns the first row.
	got = searchSlugs(t, ts, "q=&min_health=0.1")
	if len(got) != 2 {
		t.Fatalf("min_health=0.1 returned %v, want the two measured projects", got)
	}
	for _, slug := range got {
		if slug == "unmeasured" {
			t.Fatalf("an unmeasured project satisfied min_health=0.1: %v", got)
		}
	}
}

func TestSortByHealthPutsUnmeasuredLast(t *testing.T) {
	ts, _ := seedHealthFixtures(t)

	got := searchSlugs(t, ts, "q=&sort=health")
	if len(got) != 3 {
		t.Fatalf("sort=health returned %v, want all three projects", got)
	}
	if got[0] != "healthy" || got[1] != "sick" || got[2] != "unmeasured" {
		t.Fatalf("sort=health = %v, want [healthy sick unmeasured]: a measured 0.2 outranks an "+
			"unknown score, because unknown is not a low score", got)
	}

	// And the two sorts must be able to disagree, which is the property the live instance
	// lacked: with a measured project the orders differ, because `updated` knows nothing
	// about health. If they ever become identical again the sort has stopped working.
	byUpdated := searchSlugs(t, ts, "q=&sort=updated")
	if len(byUpdated) == len(got) && byUpdated[0] == got[0] && byUpdated[1] == got[1] &&
		byUpdated[2] == got[2] {
		t.Fatalf("sort=health and sort=updated returned the same order (%v); the health sort "+
			"is not reading the score", got)
	}
}
