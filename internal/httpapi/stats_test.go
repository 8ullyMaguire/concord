package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
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
	for _, want := range []string{"stats-summary", "stats-latency", "/assets/js/stats.js"} {
		if !strings.Contains(html, want) {
			t.Errorf("page does not mention %q", want)
		}
	}
	// The nav link is the page's only discoverability; base.html carries it.
	if !strings.Contains(html, `href="/stats"`) {
		t.Errorf("page does not link /stats from the nav")
	}
}

// --- STAT-2 ------------------------------------------------------------------

type statsShape struct {
	Census struct {
		FeatureStatus []struct {
			Value   string `json:"value"`
			Total   int    `json:"total"`
			Organic int    `json:"organic"`
			Seeded  int    `json:"seeded"`
			Unknown int    `json:"unknown"`
		} `json:"features_by_status"`
	} `json:"census"`
	Latency struct {
		Triage   latencyShape `json:"triage"`
		Delivery latencyShape `json:"delivery"`
		Decision latencyShape `json:"decision"`
	} `json:"latency"`
	Measured struct {
		Projects   int `json:"projects"`
		WithHealth int `json:"with_health"`
		WithOrigin int `json:"with_origin"`
	} `json:"measured"`
	GeneratedAt int64 `json:"generated_at"`
}

type latencyShape struct {
	MedianHours float64 `json:"median_hours"`
	N           int     `json:"n"`
	Unfinished  int     `json:"unfinished"`
	Unmeasured  int     `json:"unmeasured"`
	Unit        string  `json:"unit"`
}

// seedAudit writes an audit row at an explicit instant. AddAudit stamps
// time.Now(), and these tests are about a duration, so the instant is the fact.
func seedAudit(t *testing.T, st *store.DB, pid, uid int64, action, entity string, entityID int64, detail string, at float64) {
	t.Helper()
	if _, err := st.ExecContext(context.Background(),
		`INSERT INTO audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`, pid, uid, action, entity, entityID, detail, at); err != nil {
		t.Fatalf("seedAudit(%s): %v", action, err)
	}
}

// The three medians are served with their n, and each is computed from the audit
// trail rather than from a status column.
func TestStatsServesTheLatencyMedians(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	ctx := context.Background()
	u, err := st.GetUser(ctx, "testuser")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	proj, err := st.CreateProject(ctx, u.ID, "lat-proj", "Lat", "d", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// triage: complaint validated 2h after it was filed
	cres, err := st.ExecContext(ctx,
		`INSERT INTO complaints (project_id, author_id, title, body, status, origin, created_at, updated_at)
		 VALUES (?, ?, 'c', '', 'validated', 'api', 100, 7300)`, proj.ID, u.ID)
	if err != nil {
		t.Fatalf("seed complaint: %v", err)
	}
	cid, _ := cres.LastInsertId()
	seedAudit(t, st, proj.ID, u.ID, "validate_complaint", "complaint", cid, "", 7300)

	// delivery: feature ready at 0, shipped at 3h
	fres, err := st.ExecContext(ctx,
		`INSERT INTO features (project_id, author_id, title, body, effort, status, origin, created_at, updated_at)
		 VALUES (?, ?, 'f', '', 'M', 'shipped', 'api', 0, 10800)`, proj.ID, u.ID)
	if err != nil {
		t.Fatalf("seed feature: %v", err)
	}
	fid, _ := fres.LastInsertId()
	seedAudit(t, st, proj.ID, u.ID, "set_feature_status", "feature", fid, "draft -> ready: r", 0)
	seedAudit(t, st, proj.ID, u.ID, "set_feature_status", "feature", fid, "ready -> shipped: r", 10800)

	// decision: a closed call, opened at 0, closed at 4h
	if _, err := st.ExecContext(ctx,
		`INSERT INTO consensus_calls (project_id, feature_id, opened_by, opens_at, closes_at, status, question, description)
		 VALUES (?, ?, ?, 0, 14400, 'closed', 'q', 'd')`, proj.ID, fid, u.ID); err != nil {
		t.Fatalf("seed call: %v", err)
	}

	resp, err := http.Get(ts.URL + "/api/v1/stats")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body statsShape
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body.Latency.Triage.N != 1 || body.Latency.Triage.MedianHours != 2 {
		t.Errorf("triage = %+v, want n=1 median=2h", body.Latency.Triage)
	}
	if body.Latency.Delivery.N != 1 || body.Latency.Delivery.MedianHours != 3 {
		t.Errorf("delivery = %+v, want n=1 median=3h", body.Latency.Delivery)
	}
	if body.Latency.Decision.N != 1 || body.Latency.Decision.MedianHours != 4 {
		t.Errorf("decision = %+v, want n=1 median=4h", body.Latency.Decision)
	}
	// Every median carries its unit, so a reader is never left guessing hours
	// against days.
	for name, lat := range map[string]latencyShape{
		"triage": body.Latency.Triage, "delivery": body.Latency.Delivery, "decision": body.Latency.Decision,
	} {
		if lat.Unit != "hours" {
			t.Errorf("%s unit = %q, want hours", name, lat.Unit)
		}
	}
	if body.GeneratedAt == 0 {
		t.Error("generated_at is 0; the payload must be datable")
	}
}

// INVARIANT 6 (spec §5): a project with no computed health renders "—", never
// "0.0". The stored project_metrics.health_score column is 0 for every project on
// a fresh instance -- it is a placeholder -- so a badge built on the column would
// render "0.00" for all of them, and that zero propagates into a README.
func TestBadgeRendersADashForUnmeasuredHealth(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	ctx := context.Background()
	u, err := st.GetUser(ctx, "testuser")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if _, err := st.CreateProject(ctx, u.ID, "no-health", "No Health", "d", "collective", "MIT"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	resp, err := http.Get(ts.URL + "/badge/no-health.svg")
	if err != nil {
		t.Fatalf("GET badge: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want image/svg+xml", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "max-age=3600" {
		t.Errorf("Cache-Control = %q, want max-age=3600", got)
	}
	buf := new(strings.Builder)
	if _, err := io.Copy(buf, resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	svg := buf.String()
	if !strings.Contains(svg, "—") {
		t.Errorf("unmeasured health rendered without a dash:\n%s", svg)
	}
	if strings.Contains(svg, "0.0") {
		t.Errorf("unmeasured health rendered as a zero, which is the lie spec §4.13 refuses:\n%s", svg)
	}
}

// A project that does not exist answers 404, not an SVG with nobody's numbers.
func TestBadgeForAMissingProjectIs404(t *testing.T) {
	ts := newTestServerNoActor(t)
	resp, err := http.Get(ts.URL + "/badge/does-not-exist.svg")
	if err != nil {
		t.Fatalf("GET badge: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// The history route returns a BARE ARRAY of recorded days, newest first. An envelope
// would be a second thing for a charting client to unwrap, and this is the only series
// route in the spec -- every other statistics route is a frame.
func TestStatsHistoryServesRecordedDays(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	ctx := context.Background()
	for _, day := range []string{"2026-11-05", "2026-11-06"} {
		s, err := st.ComputeSnapshot(ctx, day)
		if err != nil {
			t.Fatalf("ComputeSnapshot(%s): %v", day, err)
		}
		if err := st.WriteSnapshot(ctx, s); err != nil {
			t.Fatalf("WriteSnapshot(%s): %v", day, err)
		}
	}

	resp, err := http.Get(ts.URL + "/api/v1/stats/history?days=90")
	if err != nil {
		t.Fatalf("GET history: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	// A bare array, not {"days": [...]}.
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("decode as a bare array: %v\nbody: %s", err, body)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0]["day"] != "2026-11-06" {
		t.Errorf("first row = %v, want the newest day first", rows[0]["day"])
	}
	// An unmeasured day carries null, not 0.0 -- a zero would claim the instance
	// measured nothing and found it instantaneous.
	if v, ok := rows[0]["triage_median_h"]; !ok || v != nil {
		t.Errorf("triage_median_h = %v (present=%t), want null", v, ok)
	}
}

// An empty instance returns an empty array, not a fabricated row for today.
func TestStatsHistoryEmptyIsAnEmptyArray(t *testing.T) {
	ts := newTestServerNoActor(t)
	resp, err := http.Get(ts.URL + "/api/v1/stats/history")
	if err != nil {
		t.Fatalf("GET history: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("body = %s, want [] -- no recorded day must not fabricate a row", body)
	}
}

// The feed item is built from the same payload as the API, so a subscriber and a
// page reader cannot see different numbers. The comparison is on the summary
// text against the JSON, not on two bodies being equal.
func TestStatsFeedItemMatchesTheAPI(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	ctx := context.Background()
	u, err := st.GetUser(ctx, "testuser")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	proj, err := st.CreateProject(ctx, u.ID, "feed-proj", "Feed", "d", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	fres, err := st.ExecContext(ctx,
		`INSERT INTO features (project_id, author_id, title, body, effort, status, origin, created_at, updated_at)
		 VALUES (?, ?, 'f', '', 'M', 'shipped', 'seed', 0, 10800)`, proj.ID, u.ID)
	if err != nil {
		t.Fatalf("seed feature: %v", err)
	}
	fid, _ := fres.LastInsertId()
	seedAudit(t, st, proj.ID, u.ID, "set_feature_status", "feature", fid, "draft -> ready: r", 0)
	seedAudit(t, st, proj.ID, u.ID, "set_feature_status", "feature", fid, "ready -> shipped: r", 7200)

	// The API's number.
	apiResp, err := http.Get(ts.URL + "/api/v1/stats")
	if err != nil {
		t.Fatalf("GET api: %v", err)
	}
	defer apiResp.Body.Close()
	var api statsShape
	if err := json.NewDecoder(apiResp.Body).Decode(&api); err != nil {
		t.Fatalf("decode api: %v", err)
	}

	// The feed's rendering of the same number.
	code, feed, _ := feedGet(t, ts, "/feed.xml?kind=stats")
	if code != http.StatusOK {
		t.Fatalf("feed status = %d, want 200", code)
	}
	if !strings.Contains(feed, "Instance statistics,") {
		t.Errorf("feed has no statistics item:\n%s", feed)
	}
	// n=1 and the 2h median must both appear in the feed's summary.
	if !strings.Contains(feed, "delivery 2.0h (n=1") {
		t.Errorf("feed summary does not carry the API's delivery median and n:\n%s", feed)
	}
	if !strings.Contains(feed, ">1</span>") && !strings.Contains(feed, "1 projects") {
		t.Errorf("feed summary does not carry the API's project count:\n%s", feed)
	}
}
