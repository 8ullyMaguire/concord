package store

import (
	"context"
	"testing"
)

func seedComplaintAt(t *testing.T, d *DB, pid, uid int64, title string, createdAt float64) int64 {
	t.Helper()
	res, err := d.ExecContext(context.Background(),
		`INSERT INTO complaints (project_id, author_id, title, body, status, origin, created_at, updated_at)
		 VALUES (?, ?, ?, '', 'open', 'api', ?, ?)`, pid, uid, title, createdAt, createdAt)
	if err != nil {
		t.Fatalf("seedComplaintAt(%s): %v", title, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

func seedFeatureAt(t *testing.T, d *DB, pid, uid int64, title, status string, createdAt float64) int64 {
	t.Helper()
	res, err := d.ExecContext(context.Background(),
		`INSERT INTO features (project_id, author_id, title, body, effort, status, origin, created_at, updated_at)
		 VALUES (?, ?, ?, '', 'M', ?, 'api', ?, ?)`, pid, uid, title, status, createdAt, createdAt)
	if err != nil {
		t.Fatalf("seedFeatureAt(%s): %v", title, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

func auditAt(t *testing.T, d *DB, pid, uid int64, action, entity string, entityID int64, detail string, at float64) {
	t.Helper()
	// AddAudit stamps time.Now(), so a test that needs a specific instant writes
	// the row directly -- the same columns, the same shape.
	_, err := d.ExecContext(context.Background(),
		`INSERT INTO audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`, pid, uid, action, entity, entityID, detail, at)
	if err != nil {
		t.Fatalf("auditAt(%s): %v", action, err)
	}
}

// The first validation is the triage. A complaint validated twice must not be
// measured to the second one, or a re-validation would look like slow triage.
func TestTriageLatencyUsesTheFirstValidation(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, uid, "p", "P", "d", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	cid := seedComplaintAt(t, d, p.ID, uid, "c1", 0)
	auditAt(t, d, p.ID, uid, "validate_complaint", "complaint", cid, "", 3600)
	auditAt(t, d, p.ID, uid, "validate_complaint", "complaint", cid, "", 7200)

	lat, err := d.TriageLatency(ctx)
	if err != nil {
		t.Fatalf("TriageLatency: %v", err)
	}
	if lat.N != 1 || lat.MedianHours != 1 {
		t.Fatalf("N=%d MedianHours=%v, want 1 and 1h", lat.N, lat.MedianHours)
	}
	if lat.Unfinished != 0 {
		t.Fatalf("Unfinished = %d, want 0", lat.Unfinished)
	}
}

// A complaint nobody validated is unfinished, not a zero-hour triage.
func TestTriageLatencyUnvalidatedIsUnfinished(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, uid, "p", "P", "d", "collective", "MIT")
	seedComplaintAt(t, d, p.ID, uid, "c1", 0)

	lat, err := d.TriageLatency(ctx)
	if err != nil {
		t.Fatalf("TriageLatency: %v", err)
	}
	if lat.N != 0 || lat.Unfinished != 1 {
		t.Fatalf("N=%d Unfinished=%d, want 0 and 1", lat.N, lat.Unfinished)
	}
}

// The current episode: a feature that shipped, reopened and shipped again is
// measured on the second episode, not the first.
func TestDeliveryLatencyUsesTheCurrentEpisode(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, uid, "p", "P", "d", "collective", "MIT")
	fid := seedFeatureAt(t, d, p.ID, uid, "f1", "shipped", 0)

	auditAt(t, d, p.ID, uid, "set_feature_status", "feature", fid, "draft -> ready: r", 3600)
	auditAt(t, d, p.ID, uid, "set_feature_status", "feature", fid, "ready -> shipped: r", 7200)
	auditAt(t, d, p.ID, uid, "set_feature_status", "feature", fid, "shipped -> ready: reopened", 10800)
	auditAt(t, d, p.ID, uid, "set_feature_status", "feature", fid, "ready -> shipped: r", 14400)

	lat, err := d.DeliveryLatency(ctx)
	if err != nil {
		t.Fatalf("DeliveryLatency: %v", err)
	}
	// Current episode: 10800 -> 14400 = 1h. The first episode was also 1h, so
	// assert on N and the unfinished count as well: the first episode must not be
	// counted as a second observation.
	if lat.N != 1 {
		t.Fatalf("N = %d, want 1 (one current episode per feature)", lat.N)
	}
	if lat.MedianHours != 1 {
		t.Fatalf("MedianHours = %v, want 1h", lat.MedianHours)
	}
	if lat.Unfinished != 0 {
		t.Fatalf("Unfinished = %d, want 0", lat.Unfinished)
	}
}

// A feature reopened and not yet shipped is unfinished, even though it has a
// completed first episode.
func TestDeliveryLatencyReopenedAndInFlightIsUnfinished(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, uid, "p", "P", "d", "collective", "MIT")
	fid := seedFeatureAt(t, d, p.ID, uid, "f1", "ready", 0)

	auditAt(t, d, p.ID, uid, "set_feature_status", "feature", fid, "draft -> ready: r", 3600)
	auditAt(t, d, p.ID, uid, "set_feature_status", "feature", fid, "ready -> shipped: r", 7200)
	auditAt(t, d, p.ID, uid, "set_feature_status", "feature", fid, "shipped -> ready: reopened", 10800)

	lat, err := d.DeliveryLatency(ctx)
	if err != nil {
		t.Fatalf("DeliveryLatency: %v", err)
	}
	if lat.N != 0 || lat.Unfinished != 1 {
		t.Fatalf("N=%d Unfinished=%d, want 0 and 1", lat.N, lat.Unfinished)
	}
}

// A seeded feature inserted directly as `shipped` never passed through `ready`.
// It is finished, so it is NOT unfinished -- reporting it as in progress would be
// false -- and it has no recorded start, so it cannot be in the sample.
func TestDeliveryLatencyNeverReadyIsUnmeasured(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, uid, "p", "P", "d", "collective", "MIT")
	fid := seedFeatureAt(t, d, p.ID, uid, "f1", "shipped", 0)
	auditAt(t, d, p.ID, uid, "set_feature_status", "feature", fid, "draft -> shipped: seeded", 3600)

	lat, err := d.DeliveryLatency(ctx)
	if err != nil {
		t.Fatalf("DeliveryLatency: %v", err)
	}
	if lat.N != 0 {
		t.Fatalf("N = %d, want 0 -- no recorded `ready` transition", lat.N)
	}
	if lat.Unmeasured != 1 {
		t.Fatalf("Unmeasured = %d, want 1", lat.Unmeasured)
	}
	if lat.Unfinished != 0 {
		t.Fatalf("Unfinished = %d, want 0 -- the feature shipped, it is not in progress", lat.Unfinished)
	}
}

// A feature with no status transition is not in the population at all. The
// metric measures the journey from `ready` to `shipped`; a feature that never
// entered `ready` has no journey, and CreateFeature writes no audit row, so it
// has no transition either. Counting it as unfinished would report every newly
// filed feature as a delivery in progress.
func TestDeliveryLatencyFeatureWithNoTransitionsIsNotInThePopulation(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, uid, "p", "P", "d", "collective", "MIT")
	seedFeatureAt(t, d, p.ID, uid, "f1", "draft", 0)

	lat, err := d.DeliveryLatency(ctx)
	if err != nil {
		t.Fatalf("DeliveryLatency: %v", err)
	}
	if lat.N != 0 || lat.Unfinished != 0 || lat.Unmeasured != 0 {
		t.Fatalf("N=%d Unfinished=%d Unmeasured=%d, want all zero", lat.N, lat.Unfinished, lat.Unmeasured)
	}
}

// THE TRAP. An open consensus call is inserted with closes_at = opens_at (the
// column is NOT NULL), so a query that does not filter on status measures every
// open call as 0.0 hours -- reporting an undecided call as an instant decision.
func TestDecisionLatencyIgnoresOpenCalls(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, uid, "p", "P", "d", "collective", "MIT")
	fid := seedFeatureAt(t, d, p.ID, uid, "f1", "ready", 0)

	// open call: closes_at == opens_at, exactly as consensus.go writes it
	_, err := d.ExecContext(ctx,
		`INSERT INTO consensus_calls (project_id, feature_id, opened_by, opens_at, closes_at, status, question, description)
		 VALUES (?, ?, ?, 0, 0, 'open', 'q', 'd')`, p.ID, fid, uid)
	if err != nil {
		t.Fatalf("insert open call: %v", err)
	}
	// closed call: 2h
	_, err = d.ExecContext(ctx,
		`INSERT INTO consensus_calls (project_id, feature_id, opened_by, opens_at, closes_at, status, question, description)
		 VALUES (?, ?, ?, 100, 7300, 'closed', 'q', 'd')`, p.ID, fid, uid)
	if err != nil {
		t.Fatalf("insert closed call: %v", err)
	}

	lat, err := d.DecisionLatency(ctx)
	if err != nil {
		t.Fatalf("DecisionLatency: %v", err)
	}
	if lat.N != 1 {
		t.Fatalf("N = %d, want 1 -- the open call must not be a 0-hour observation", lat.N)
	}
	if lat.MedianHours != 2 {
		t.Fatalf("MedianHours = %v, want 2h", lat.MedianHours)
	}
	if lat.Unfinished != 1 {
		t.Fatalf("Unfinished = %d, want 1", lat.Unfinished)
	}
}

// A closed call that really did decide in zero seconds is a legitimate 0-hour
// observation, and it must be counted. This is what separates "0 because it
// happened" from "0 because the call is still open" -- the filter is on status,
// never on the value.
func TestDecisionLatencyClosedCallWithEqualTimestampsIsCounted(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, uid, "p", "P", "d", "collective", "MIT")
	fid := seedFeatureAt(t, d, p.ID, uid, "f1", "ready", 0)
	_, err := d.ExecContext(ctx,
		`INSERT INTO consensus_calls (project_id, feature_id, opened_by, opens_at, closes_at, status, question, description)
		 VALUES (?, ?, ?, 500, 500, 'closed', 'q', 'd')`, p.ID, fid, uid)
	if err != nil {
		t.Fatalf("insert closed call: %v", err)
	}

	lat, err := d.DecisionLatency(ctx)
	if err != nil {
		t.Fatalf("DecisionLatency: %v", err)
	}
	if lat.N != 1 {
		t.Fatalf("N = %d, want 1 -- a closed call is an observation even at 0 hours", lat.N)
	}
	if lat.MedianHours != 0 {
		t.Fatalf("MedianHours = %v, want 0", lat.MedianHours)
	}
	if lat.Unfinished != 0 {
		t.Fatalf("Unfinished = %d, want 0", lat.Unfinished)
	}
}

// parseTransitionDetail reads trust.go's "<from> -> <to>: <reason>" form.
func TestParseTransitionDetail(t *testing.T) {
	cases := []struct {
		in   string
		from string
		to   string
		ok   bool
	}{
		{"draft -> ready: needs a maintainer", "draft", "ready", true},
		{"ready -> shipped: ", "ready", "shipped", true},
		{"draft -> shipped", "draft", "shipped", true},
		{"no arrow here", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		from, to, ok := parseTransitionDetail(c.in)
		if ok != c.ok || from != c.from || to != c.to {
			t.Errorf("parseTransitionDetail(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.in, from, to, ok, c.from, c.to, c.ok)
		}
	}
}
