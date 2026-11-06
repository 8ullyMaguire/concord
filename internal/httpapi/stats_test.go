package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type statsSummaryShape struct {
	FeatureStatus []struct {
		Value   string `json:"value"`
		Total   int    `json:"total"`
		Organic int    `json:"organic"`
		Seeded  int    `json:"seeded"`
		Unknown int    `json:"unknown"`
	} `json:"features_by_status"`
	Projects []struct {
		Value string `json:"value"`
		Count int    `json:"count"`
	} `json:"projects_by_language"`
}

// The endpoint publishes the split, not a bare total.
//
// Spec §2: an aggregate over rows that can be created by an import is published
// as two numbers, and one that cannot produce the split is not published at all.
// A response carrying only `total` would pass a reader's eye and fail the spec.
func TestStatsSummaryServesTheProvenanceSplit(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	ctx := context.Background()

	u, err := st.GetUser(ctx, "testuser")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	proj, err := st.CreateProject(ctx, u.ID, "stats-proj", "Stats Project", "d", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Three origins, so the split has all three populations to separate.
	for _, origin := range []string{"api", "seed", "unknown"} {
		if _, err := st.ExecContext(ctx,
			`INSERT INTO features (project_id, author_id, title, body, effort, status, origin, created_at, updated_at)
			 VALUES (?, ?, ?, '', 'M', 'draft', ?, 0, 0)`, proj.ID, u.ID, "f-"+origin, origin); err != nil {
			t.Fatalf("seed feature (%s): %v", origin, err)
		}
	}

	resp, err := http.Get(ts.URL + "/api/v1/stats/summary")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body statsSummaryShape
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.FeatureStatus) != 1 {
		t.Fatalf("features_by_status = %+v, want one row (all fixtures are draft)", body.FeatureStatus)
	}
	got := body.FeatureStatus[0]
	if got.Total != 3 || got.Organic != 1 || got.Seeded != 1 || got.Unknown != 1 {
		t.Errorf("features_by_status[draft] = %+v, want total 3 organic 1 seeded 1 unknown 1", got)
	}
}

// The page is a shell over the API: it must render without the data and carry the
// mount point the reader hydrates. A page that server-rendered the numbers would
// be a second computation, which is what spec §3.4 forbids.
func TestStatsPageIsAShellOverTheAPI(t *testing.T) {
	ts := newTestServerNoActor(t)

	resp, err := http.Get(ts.URL + "/stats")
	if err != nil {
		t.Fatalf("GET /stats: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	buf := new(strings.Builder)
	if _, err := io.Copy(buf, resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	html := buf.String()
	for _, want := range []string{"stats-summary", "/assets/js/stats.js"} {
		if !strings.Contains(html, want) {
			t.Errorf("page does not mention %q", want)
		}
	}
	// The nav link is the page's only discoverability; base.html carries it.
	if !strings.Contains(html, `href="/stats"`) {
		t.Errorf("page does not link /stats from the nav")
	}
}
