package store

import (
	"context"
	"database/sql"

	"git.polarisocial.xyz/concord/concord/internal/discovery"
)

// Snapshot is one day's recorded frame of the instance.
//
// THE MEDIANS ARE POINTERS, AND THAT IS THE POINT. A day with no measured episode has
// no median -- not a zero-hour median, which would claim the instance measured nothing
// and found it instantaneous. `*float64` is nil and the JSON carries null.
//
// It is a pointer rather than sql.NullFloat64 for a reason that cost a test to find:
// sql.NullFloat64 has no MarshalJSON, so the API published
// `{"Float64":0,"Valid":false}` -- the Go type leaked into the document and a reader
// comparing against null got an object. The database scan still uses sql.NullFloat64
// internally; the pointer is the JSON boundary.
//
// The `_n` columns are the other half: a median is never published without its sample
// size (spec §4).
type Snapshot struct {
	Day              string   `json:"day"`
	Projects         int      `json:"projects"`
	ProjectsMeasured int      `json:"projects_measured"`
	FeaturesTotal    int      `json:"features_total"`
	FeaturesSeed     int      `json:"features_seed"`
	ComplaintsTotal  int      `json:"complaints_total"`
	ComplaintsSeed   int      `json:"complaints_seed"`
	TriageMedianH    *float64 `json:"triage_median_h"`
	TriageN          int      `json:"triage_n"`
	DeliveryMedianH  *float64 `json:"delivery_median_h"`
	DeliveryN        int      `json:"delivery_n"`
	DecisionMedianH  *float64 `json:"decision_median_h"`
	DecisionN        int      `json:"decision_n"`
	NoOrigin         int      `json:"no_origin"`
	ComputedAt       float64  `json:"computed_at"`
}

// ComputeSnapshot takes one day's frame. `day` is 'YYYY-MM-DD' in UTC and is the caller's
// to choose: the command decides "today", and a test decides what it needs to assert on.
func (d *DB) ComputeSnapshot(ctx context.Context, day string) (Snapshot, error) {
	var s Snapshot
	s.Day = day

	measured, err := d.Measured(ctx)
	if err != nil {
		return s, err
	}
	s.Projects = measured.Projects
	s.ProjectsMeasured = measured.WithHealth

	if s.FeaturesTotal, s.FeaturesSeed, err = d.countAndSeed(ctx, "features"); err != nil {
		return s, err
	}
	if s.ComplaintsTotal, s.ComplaintsSeed, err = d.countAndSeed(ctx, "complaints"); err != nil {
		return s, err
	}

	// The latency builders are the same ones GET /api/v1/stats uses, so the daily row
	// and the live page cannot disagree about what a median is. A snapshot that
	// recomputed the medians its own way would be a second answer to the same question.
	if s.TriageMedianH, s.TriageN, err = d.nullableLatency(ctx, d.TriageLatency); err != nil {
		return s, err
	}
	if s.DeliveryMedianH, s.DeliveryN, err = d.nullableLatency(ctx, d.DeliveryLatency); err != nil {
		return s, err
	}
	if s.DecisionMedianH, s.DecisionN, err = d.nullableLatency(ctx, d.DecisionLatency); err != nil {
		return s, err
	}

	if s.NoOrigin, err = d.countNoOrigin(ctx); err != nil {
		return s, err
	}
	return s, nil
}

func (d *DB) nullableLatency(ctx context.Context, f func(context.Context) (discovery.Latency, error)) (*float64, int, error) {
	lat, err := f(ctx)
	if err != nil {
		return nil, 0, err
	}
	if lat.N == 0 {
		return nil, 0, nil
	}
	v := lat.MedianHours
	return &v, lat.N, nil
}

// ptrToNull is the database-facing half of the pointer boundary: a nil median is a
// NULL column, never a zero.
func ptrToNull(p *float64) sql.NullFloat64 {
	if p == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *p, Valid: true}
}

// countAndSeed returns the total row count and how many of them carry a seed origin.
// SUM returns NULL over zero rows, so it is COALESCEd -- a table with no rows has no
// seeded rows, which is a count of 0 and not an absent value.
func (d *DB) countAndSeed(ctx context.Context, table string) (total, seed int, err error) {
	err = d.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN origin IN ('seed','import') THEN 1 ELSE 0 END), 0)
		 FROM `+table).Scan(&total, &seed)
	return total, seed, err
}

// countNoOrigin counts rows whose provenance nobody can state, across both tables
// migration 0030 backfilled. They are one population.
func (d *DB) countNoOrigin(ctx context.Context) (int, error) {
	var n int
	err := d.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM features WHERE origin = 'unknown')
		      + (SELECT COUNT(*) FROM complaints WHERE origin = 'unknown')`).Scan(&n)
	return n, err
}

// WriteSnapshot records or corrects one day's row.
//
// ON CONFLICT(day) DO UPDATE, not INSERT OR IGNORE and not a check-then-write. A second
// run on the same day must CORRECT the row rather than skip it -- the day's numbers may
// have changed since the first pass, and a snapshot that kept the earlier value would be
// a recording of a moment that had already passed. A check-then-write has a race between
// the check and the write; the constraint is the arbiter.
func (d *DB) WriteSnapshot(ctx context.Context, s Snapshot) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO instance_stats
		    (day, projects, projects_measured, features_total, features_seed,
		     complaints_total, complaints_seed, triage_median_h, triage_n,
		     delivery_median_h, delivery_n, decision_median_h, decision_n,
		     no_origin, computed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(day) DO UPDATE SET
		    projects          = excluded.projects,
		    projects_measured = excluded.projects_measured,
		    features_total    = excluded.features_total,
		    features_seed     = excluded.features_seed,
		    complaints_total  = excluded.complaints_total,
		    complaints_seed   = excluded.complaints_seed,
		    triage_median_h   = excluded.triage_median_h,
		    triage_n          = excluded.triage_n,
		    delivery_median_h = excluded.delivery_median_h,
		    delivery_n        = excluded.delivery_n,
		    decision_median_h = excluded.decision_median_h,
		    decision_n        = excluded.decision_n,
		    no_origin         = excluded.no_origin,
		    computed_at       = excluded.computed_at`,
		s.Day, s.Projects, s.ProjectsMeasured, s.FeaturesTotal, s.FeaturesSeed,
		s.ComplaintsTotal, s.ComplaintsSeed, ptrToNull(s.TriageMedianH), s.TriageN,
		ptrToNull(s.DeliveryMedianH), s.DeliveryN, ptrToNull(s.DecisionMedianH), s.DecisionN,
		s.NoOrigin, s.ComputedAt)
	return err
}

// SnapshotHistory returns recorded days, newest first.
//
// `days` is clamped to [1, 365]: a request for the whole table is a scrape, and a
// series longer than a year is a chart nobody reads. The cap is a constant rather than
// a setting because the alternative -- an unbounded series route -- is how a database
// ends up serialized into somebody's RSS reader.
func (d *DB) SnapshotHistory(ctx context.Context, days int) ([]Snapshot, error) {
	if days <= 0 {
		days = 30
	}
	if days > 365 {
		days = 365
	}
	rows, err := d.QueryContext(ctx,
		`SELECT day, projects, projects_measured, features_total, features_seed,
		        complaints_total, complaints_seed, triage_median_h, triage_n,
		        delivery_median_h, delivery_n, decision_median_h, decision_n,
		        no_origin, computed_at
		 FROM instance_stats ORDER BY day DESC LIMIT ?`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Snapshot{}
	for rows.Next() {
		var s Snapshot
		var triage, delivery, decision sql.NullFloat64
		if err := rows.Scan(&s.Day, &s.Projects, &s.ProjectsMeasured, &s.FeaturesTotal,
			&s.FeaturesSeed, &s.ComplaintsTotal, &s.ComplaintsSeed, &triage,
			&s.TriageN, &delivery, &s.DeliveryN, &decision,
			&s.DecisionN, &s.NoOrigin, &s.ComputedAt); err != nil {
			return nil, err
		}
		// A NULL column becomes a nil pointer, which is what the JSON carries as
		// null. Scanning straight into *float64 would leave a nil that is
		// indistinguishable from a scan failure.
		s.TriageMedianH = nullToPtr(triage)
		s.DeliveryMedianH = nullToPtr(delivery)
		s.DecisionMedianH = nullToPtr(decision)
		out = append(out, s)
	}
	return out, rows.Err()
}

// nullToPtr is the read half of the pointer boundary.
func nullToPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}
