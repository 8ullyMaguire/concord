package store

import (
	"context"
	"testing"
)

// INVARIANT 5 (spec §5): a second run on the same day updates today's row rather
// than inserting a second one.
//
// A duplicate day would put two points at the same x on a chart and the slope
// between them would be infinite -- so this is not a tidiness rule, it is the
// rule that keeps the series a series.
func TestSnapshotSecondRunUpdatesRatherThanInserts(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	p, err := d.CreateProject(ctx, uid, "p", "P", "d", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO features (project_id, author_id, title, body, effort, status, origin, created_at, updated_at)
		 VALUES (?, ?, 'f', '', 'M', 'draft', 'seed', 0, 0)`, p.ID, uid); err != nil {
		t.Fatalf("seed feature: %v", err)
	}

	s, err := d.ComputeSnapshot(ctx, "2026-11-06")
	if err != nil {
		t.Fatalf("ComputeSnapshot: %v", err)
	}
	s.ComputedAt = 1000
	if err := d.WriteSnapshot(ctx, s); err != nil {
		t.Fatalf("WriteSnapshot #1: %v", err)
	}

	// A second pass, later, with nothing changed.
	s2, err := d.ComputeSnapshot(ctx, "2026-11-06")
	if err != nil {
		t.Fatalf("ComputeSnapshot #2: %v", err)
	}
	s2.ComputedAt = 2000
	if err := d.WriteSnapshot(ctx, s2); err != nil {
		t.Fatalf("WriteSnapshot #2: %v", err)
	}

	var count int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM instance_stats WHERE day = '2026-11-06'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("rows for the day = %d, want 1 -- a second run must correct, not append", count)
	}

	// Every column that does not depend on "now" must be identical. computed_at is
	// the one exception: it records when the pass ran, and that is supposed to move.
	var got Snapshot
	if err := d.QueryRowContext(ctx,
		`SELECT day, projects, projects_measured, features_total, features_seed,
		        complaints_total, complaints_seed, triage_median_h, triage_n,
		        delivery_median_h, delivery_n, decision_median_h, decision_n,
		        no_origin, computed_at
		 FROM instance_stats WHERE day = '2026-11-06'`).
		Scan(&got.Day, &got.Projects, &got.ProjectsMeasured, &got.FeaturesTotal,
			&got.FeaturesSeed, &got.ComplaintsTotal, &got.ComplaintsSeed,
			&got.TriageMedianH, &got.TriageN, &got.DeliveryMedianH, &got.DeliveryN,
			&got.DecisionMedianH, &got.DecisionN, &got.NoOrigin, &got.ComputedAt); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.ComputedAt != 2000 {
		t.Errorf("computed_at = %v, want 2000 -- the later pass must win", got.ComputedAt)
	}
	s.ComputedAt = got.ComputedAt
	if got != s {
		t.Errorf("second run changed a column that does not depend on now:\n first  %+v\n second %+v", s, got)
	}
}

// A day with no measured episode records NULL medians, not 0.0. A zero would claim
// the instance measured nothing and found it instantaneous.
func TestSnapshotUnmeasuredMediansAreNull(t *testing.T) {
	d := setup(t)
	ctx := context.Background()

	s, err := d.ComputeSnapshot(ctx, "2026-11-06")
	if err != nil {
		t.Fatalf("ComputeSnapshot: %v", err)
	}
	if s.TriageMedianH != nil || s.DeliveryMedianH != nil || s.DecisionMedianH != nil {
		t.Errorf("an empty instance produced a median: triage=%v delivery=%v decision=%v",
			s.TriageMedianH, s.DeliveryMedianH, s.DecisionMedianH)
	}
	if s.TriageN != 0 || s.DeliveryN != 0 || s.DecisionN != 0 {
		t.Errorf("n must be 0 with no episodes, got %d/%d/%d",
			s.TriageN, s.DeliveryN, s.DecisionN)
	}
	if err := d.WriteSnapshot(ctx, s); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	// The stored row carries the NULL, not a zero.
	var n int
	var triageNull, deliveryNull, decisionNull bool
	if err := d.QueryRowContext(ctx,
		`SELECT triage_n, triage_median_h IS NULL, delivery_median_h IS NULL, decision_median_h IS NULL
		 FROM instance_stats WHERE day = '2026-11-06'`).Scan(&n, &triageNull, &deliveryNull, &decisionNull); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !triageNull || !deliveryNull || !decisionNull {
		t.Error("the stored medians are non-NULL; an unmeasured day must record NULL")
	}
}

// The snapshot's split columns are the same ones STAT-1 publishes, so the recorded
// series and the live page cannot disagree about what "seeded" means.
func TestSnapshotCountsTheSplit(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	p, _ := d.CreateProject(ctx, uid, "p", "P", "d", "collective", "MIT")

	for _, origin := range []string{"api", "seed", "unknown"} {
		if _, err := d.ExecContext(ctx,
			`INSERT INTO features (project_id, author_id, title, body, effort, status, origin, created_at, updated_at)
			 VALUES (?, ?, ?, '', 'M', 'draft', ?, 0, 0)`, p.ID, uid, "f-"+origin, origin); err != nil {
			t.Fatalf("seed feature (%s): %v", origin, err)
		}
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO complaints (project_id, author_id, title, body, status, origin, created_at, updated_at)
		 VALUES (?, ?, 'c', '', 'open', 'unknown', 0, 0)`, p.ID, uid); err != nil {
		t.Fatalf("seed complaint: %v", err)
	}

	s, err := d.ComputeSnapshot(ctx, "2026-11-06")
	if err != nil {
		t.Fatalf("ComputeSnapshot: %v", err)
	}
	if s.FeaturesTotal != 3 {
		t.Errorf("features_total = %d, want 3", s.FeaturesTotal)
	}
	if s.FeaturesSeed != 1 {
		t.Errorf("features_seed = %d, want 1 (origin='seed')", s.FeaturesSeed)
	}
	if s.ComplaintsTotal != 1 || s.ComplaintsSeed != 0 {
		t.Errorf("complaints = %d/%d, want 1 total and 0 seeded", s.ComplaintsTotal, s.ComplaintsSeed)
	}
	// no_origin spans BOTH tables 0030 backfilled: the one 'unknown' feature from the
	// loop above, plus the 'unknown' complaint.
	if s.NoOrigin != 2 {
		t.Errorf("no_origin = %d, want 2 (1 feature + 1 complaint)", s.NoOrigin)
	}
	if s.Projects != 1 || s.ProjectsMeasured != 0 {
		t.Errorf("projects = %d/%d, want 1 and 0 measured", s.Projects, s.ProjectsMeasured)
	}
}

// History is capped. A request for everything is a scrape, and the cap is the
// difference between a chart and a database dump.
func TestSnapshotHistoryIsCapped(t *testing.T) {
	d := setup(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		s, err := d.ComputeSnapshot(ctx, "2026-11-0"+string(rune('1'+i)))
		if err != nil {
			t.Fatalf("ComputeSnapshot: %v", err)
		}
		if err := d.WriteSnapshot(ctx, s); err != nil {
			t.Fatalf("WriteSnapshot: %v", err)
		}
	}

	// Ask for more days than exist, and more than the cap: the cap wins.
	rows, err := d.SnapshotHistory(ctx, 100000)
	if err != nil {
		t.Fatalf("SnapshotHistory: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("history = %d rows, want 5 (all of them, capped)", len(rows))
	}
	// Newest first, so a chart can plot the array without reversing it.
	if rows[0].Day != "2026-11-05" {
		t.Errorf("first row = %q, want the newest day 2026-11-05", rows[0].Day)
	}

	// A zero or negative count means "the default", not "no rows".
	rows, err = d.SnapshotHistory(ctx, 0)
	if err != nil {
		t.Fatalf("SnapshotHistory(0): %v", err)
	}
	if len(rows) != 5 {
		t.Errorf("SnapshotHistory(0) = %d rows, want the default window", len(rows))
	}
}

// A day that was never recorded is simply absent. The series does not fabricate a
// zero row for a gap -- a missing day is the honest record of a pass that did not run.
func TestSnapshotHistoryHasNoRowForAnUnrecordedDay(t *testing.T) {
	d := setup(t)
	ctx := context.Background()
	rows, err := d.SnapshotHistory(ctx, 30)
	if err != nil {
		t.Fatalf("SnapshotHistory: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("history = %d rows on an empty database, want 0", len(rows))
	}
}
