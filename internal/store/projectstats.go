package store

import (
	"context"
	"fmt"
	"sort"
)

// ProjectSnapshot is one project's row for one day (0033_project_stats.sql, STAT-3.4).
//
// Both counts are CUMULATIVE and ORGANIC: "how many organic features had shipped by the
// end of this day", not "how many shipped today". A stored delta is wrong the moment a
// run is missed -- the next row would report three days of change as one -- while
// cumulative counts make a gap a gap, because the slope divides by the days actually
// between the two rows.
type ProjectSnapshot struct {
	ProjectID       int64 `json:"project_id"`
	FeaturesShipped int   `json:"organic_features_shipped"`
	ComplaintsFiled int   `json:"organic_complaints_filed"`
}

// ComputeProjectSnapshots takes every project's cumulative organic counts.
//
// NOT visibility-filtered, for the same reason `ListProjectsWithRepo` is not: a private
// project's numbers are a fact about the project, and filtering here would leave it
// permanently unrecorded. Publishing is a separate question, answered by the leaderboard.
//
// The predicate `origin IN ('api','ui')` is copied from `Census` rather than written
// fresh: `origin='unknown'` is the population that PREDATES the 0030 backfill, not
// organic work, and folding it in would turn a chart of a seed script into a "rising"
// project. The two definitions must stay one.
func (d *DB) ComputeProjectSnapshots(ctx context.Context) ([]ProjectSnapshot, error) {
	byProject := map[int64]*ProjectSnapshot{}
	get := func(id int64) *ProjectSnapshot {
		if s, ok := byProject[id]; ok {
			return s
		}
		s := &ProjectSnapshot{ProjectID: id}
		byProject[id] = s
		return s
	}

	shipRows, err := d.QueryContext(ctx, `
		SELECT project_id, COUNT(*) FROM features
		 WHERE status = 'shipped' AND origin IN ('api','ui')
		 GROUP BY project_id`)
	if err != nil {
		return nil, fmt.Errorf("project snapshot: shipped features: %w", err)
	}
	for shipRows.Next() {
		var id int64
		var n int
		if err := shipRows.Scan(&id, &n); err != nil {
			shipRows.Close()
			return nil, fmt.Errorf("project snapshot: scan shipped: %w", err)
		}
		get(id).FeaturesShipped = n
	}
	shipRows.Close()
	if err := shipRows.Err(); err != nil {
		return nil, fmt.Errorf("project snapshot: shipped: %w", err)
	}

	// "Filed" is every organic complaint, whatever its status. A complaint that was
	// later closed was still filed, and filtering by status would make the series fall
	// when a maintainer closed one -- a slope that goes down because the work was done.
	fileRows, err := d.QueryContext(ctx, `
		SELECT project_id, COUNT(*) FROM complaints
		 WHERE origin IN ('api','ui')
		 GROUP BY project_id`)
	if err != nil {
		return nil, fmt.Errorf("project snapshot: filed complaints: %w", err)
	}
	for fileRows.Next() {
		var id int64
		var n int
		if err := fileRows.Scan(&id, &n); err != nil {
			fileRows.Close()
			return nil, fmt.Errorf("project snapshot: scan filed: %w", err)
		}
		get(id).ComplaintsFiled = n
	}
	fileRows.Close()
	if err := fileRows.Err(); err != nil {
		return nil, fmt.Errorf("project snapshot: filed: %w", err)
	}

	// Every project gets a row, including the ones at 0/0. A project absent from the
	// series has no slope, and "absent" would then mean two different things: "no organic
	// work yet" and "this project was created after the first snapshot". Recording the
	// zero makes the second distinguishable by its start day.
	pRows, err := d.QueryContext(ctx, `SELECT id FROM projects ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("project snapshot: projects: %w", err)
	}
	defer pRows.Close()
	for pRows.Next() {
		var id int64
		if err := pRows.Scan(&id); err != nil {
			return nil, fmt.Errorf("project snapshot: scan project: %w", err)
		}
		get(id)
	}
	if err := pRows.Err(); err != nil {
		return nil, fmt.Errorf("project snapshot: projects: %w", err)
	}

	out := make([]ProjectSnapshot, 0, len(byProject))
	for _, s := range byProject {
		out = append(out, *s)
	}
	// Map iteration order is random; a caller that prints or tests the rows must not get
	// a different order per run.
	sort.Slice(out, func(i, j int) bool { return out[i].ProjectID < out[j].ProjectID })
	return out, nil
}

// WriteProjectSnapshots records or corrects one day's per-project rows.
//
// ON CONFLICT(day, project_id) DO UPDATE, not INSERT OR IGNORE: a second run on the same
// day must CORRECT the row. A day's counts can change between two runs (a feature ships
// at 23:50), and a snapshot that kept the earlier value would be a recording of a moment
// that had already passed.
//
// One transaction for the whole day: a half-written day would be a set of projects
// compared across different moments, and the slope between them would measure the
// writer's interruption.
func (d *DB) WriteProjectSnapshots(ctx context.Context, day string, rows []ProjectSnapshot) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO project_stats (day, project_id,
			     organic_features_shipped, organic_complaints_filed)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(day, project_id) DO UPDATE SET
			     organic_features_shipped = excluded.organic_features_shipped,
			     organic_complaints_filed = excluded.organic_complaints_filed`,
			day, r.ProjectID, r.FeaturesShipped, r.ComplaintsFiled); err != nil {
			return fmt.Errorf("write project snapshot %s/%d: %w", day, r.ProjectID, err)
		}
	}
	return tx.Commit()
}
