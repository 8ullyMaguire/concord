package store

import (
	"context"
	"testing"
)

// seedFeature inserts a feature with an explicit origin, which is the only way
// to build the three-way split: CreateFeature leaves origin at its column
// default ('api'), so the organic row comes from the store path and the other
// two from SQL.
func seedFeature(t *testing.T, d *DB, pid, uid int64, title, origin string) {
	t.Helper()
	_, err := d.ExecContext(context.Background(),
		`INSERT INTO features (project_id, author_id, title, body, effort, status, origin, created_at, updated_at)
		 VALUES (?, ?, ?, '', 'M', 'draft', ?, 0, 0)`, pid, uid, title, origin)
	if err != nil {
		t.Fatalf("seedFeature(%s, %s): %v", title, origin, err)
	}
}

func seedComplaint(t *testing.T, d *DB, pid, uid int64, title, origin string) {
	t.Helper()
	_, err := d.ExecContext(context.Background(),
		`INSERT INTO complaints (project_id, author_id, title, body, status, origin, created_at, updated_at)
		 VALUES (?, ?, ?, '', 'open', ?, 0, 0)`, pid, uid, title, origin)
	if err != nil {
		t.Fatalf("seedComplaint(%s, %s): %v", title, origin, err)
	}
}

// The arithmetic invariant, over a fixture that holds all three populations.
//
// This is the AC the plan states as "Total = Seed + Other", and it is asserted
// here in its three-way form because the fixture has an 'unknown' row: a
// two-way sum would pass while reporting the backfilled population as organic.
func TestCensusProvenanceRowsSum(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()

	newFeature(t, d, pid, uid, "organic") // origin='api' via the column default
	seedFeature(t, d, pid, uid, "seeded", "seed")
	seedFeature(t, d, pid, uid, "imported", "import")
	seedFeature(t, d, pid, uid, "backfilled", "unknown")
	seedComplaint(t, d, pid, uid, "organic complaint", "ui")
	seedComplaint(t, d, pid, uid, "seeded complaint", "seed")
	seedComplaint(t, d, pid, uid, "backfilled complaint", "unknown")

	c, err := d.Census(ctx)
	if err != nil {
		t.Fatalf("Census: %v", err)
	}

	check := func(name string, rows []ProvenanceCount) {
		t.Helper()
		for _, r := range rows {
			if r.Total != r.Organic+r.Seeded+r.Unknown {
				t.Errorf("%s[%s]: total %d != organic %d + seeded %d + unknown %d",
					name, r.Value, r.Total, r.Organic, r.Seeded, r.Unknown)
			}
		}
	}
	check("features_by_status", c.FeatureStatus)
	check("features_by_effort", c.FeatureEffort)
	check("complaints_by_status", c.Complaints)

	// Every fixture row is 'draft', so the status facet has one row holding all
	// four features -- and it is the row that would hide a split.
	if len(c.FeatureStatus) != 1 {
		t.Fatalf("features_by_status: got %d rows, want 1 (all fixtures are draft): %+v",
			len(c.FeatureStatus), c.FeatureStatus)
	}
	got := c.FeatureStatus[0]
	if got.Total != 4 || got.Organic != 1 || got.Seeded != 2 || got.Unknown != 1 {
		t.Errorf("features_by_status[draft] = %+v, want total 4 organic 1 seeded 2 unknown 1", got)
	}
}

// The third column is the point: 'unknown' must not be reported as organic.
//
// A two-way split that folds 'unknown' into "other" would answer organic=2 for
// a population of one, which is the lie R13 exists to prevent.
func TestCensusUnknownIsNotOrganic(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()

	seedFeature(t, d, pid, uid, "only-backfilled", "unknown")

	c, err := d.Census(ctx)
	if err != nil {
		t.Fatalf("Census: %v", err)
	}
	got := c.FeatureStatus[0]
	if got.Organic != 0 {
		t.Errorf("organic = %d, want 0: a backfilled row is not organic", got.Organic)
	}
	if got.Unknown != 1 {
		t.Errorf("unknown = %d, want 1", got.Unknown)
	}
	if got.Seeded != 0 {
		t.Errorf("seeded = %d, want 0", got.Seeded)
	}
}

// The classic facet defect: a LEFT JOIN that multiplies, so a facet total
// exceeds the table it counts. Asserted against count(*), not against another
// aggregate.
func TestCensusTotalNeverExceedsTableCount(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		newFeature(t, d, pid, uid, "f")
	}

	var tableCount int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM features`).Scan(&tableCount); err != nil {
		t.Fatalf("count features: %v", err)
	}

	c, err := d.Census(ctx)
	if err != nil {
		t.Fatalf("Census: %v", err)
	}
	for _, r := range c.FeatureStatus {
		if r.Total > tableCount {
			t.Errorf("features_by_status[%s]: total %d exceeds table count %d -- facet multiplication",
				r.Value, r.Total, tableCount)
		}
	}
}

// The plain facets have no origin, so they publish a count and must not invent
// a split. This also pins the table name: the plan said "documents", the schema
// says project_documents.
func TestCensusPlainFacets(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()

	// 'reviewer', not 'maintainer': CreateProject makes the creator a
	// maintainer (store.go:411), so a maintainer assertion would be counting the
	// fixture's own creator and would read as an off-by-one in the facet.
	other := newUserNamed(t, d, "censusmember")
	if _, err := d.ExecContext(ctx,
		`INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?, ?, 'reviewer', 0)`,
		pid, other); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO project_documents (project_id, kind, slug, title, author_id, created_at, updated_at)
		 VALUES (?, 'spec', 's', 'S', ?, 0, 0)`, pid, uid); err != nil {
		t.Fatalf("insert document: %v", err)
	}

	c, err := d.Census(ctx)
	if err != nil {
		t.Fatalf("Census: %v", err)
	}

	var reviewerSeen bool
	for _, m := range c.Members {
		if m.Value == "reviewer" {
			reviewerSeen = true
			if m.Count != 1 {
				t.Errorf("members_by_role[reviewer] = %d, want 1", m.Count)
			}
		}
	}
	if !reviewerSeen {
		t.Errorf("members_by_role has no reviewer row: %+v", c.Members)
	}

	var specSeen bool
	for _, doc := range c.Documents {
		if doc.Value == "spec" {
			specSeen = true
			if doc.Count != 1 {
				t.Errorf("documents_by_kind[spec] = %d, want 1", doc.Count)
			}
		}
	}
	if !specSeen {
		t.Errorf("documents_by_kind has no spec row: %+v", c.Documents)
	}
}
