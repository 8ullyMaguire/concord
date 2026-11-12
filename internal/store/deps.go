package store

// Feature dependencies (docs/specs/idea-intake-and-milestones-spec.md §6, R18).
//
// A milestone's contents could be listed but not ordered while nothing recorded
// that one idea must land before another. The refusal lives at the status
// transition (see UpdateFeatureStatus), because a constraint that only appears in
// a report is a constraint nobody obeys.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// FeatureRef is the minimum a dependency edge needs to say: which idea, and where
// to read about it.
type FeatureRef struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// AddDependency records that `blocker` must ship before `blocked` can start.
//
// The cycle walk runs before the insert: a graph that contains a cycle can never
// be started, and the reader who created it needs to be told which path they
// closed rather than "invalid".
func (d *DB) AddDependency(ctx context.Context, projectID, blocker, blocked int64) error {
	if blocker == 0 || blocked == 0 {
		return fmt.Errorf("%w: blocker_id and blocked_id are required", ErrInvalid)
	}
	if blocker == blocked {
		return fmt.Errorf("%w: a feature cannot block itself", ErrInvalid)
	}
	for _, id := range []int64{blocker, blocked} {
		f, err := d.GetFeature(ctx, id)
		if err != nil {
			return err
		}
		// Both ends must belong to the project in the path. Otherwise a maintainer
		// of one project could reach into another's graph, and the error would be
		// 403-shaped for a row that is simply not theirs.
		if f.ProjectID != projectID {
			return fmt.Errorf("%w: feature %d", ErrNotFound, id)
		}
	}
	// Would `blocker` already wait on `blocked`, transitively? Adding the edge would
	// close the cycle.
	path, err := d.pathTo(ctx, blocked, blocker)
	if err != nil {
		return err
	}
	if path != nil {
		return fmt.Errorf("%w: %s", ErrInvalid, describeCycle(blocker, path, blocked))
	}
	_, err = d.ExecContext(ctx, `
		INSERT INTO feature_deps (blocker_id, blocked_id, created_at) VALUES (?, ?, ?)
		ON CONFLICT(blocker_id, blocked_id) DO NOTHING`, blocker, blocked, float64(time.Now().Unix()))
	return err
}

// RemoveDependency deletes an edge. Removing one that does not exist is a no-op
// rather than an error: the caller's intent ("this must not be a dependency") is
// satisfied either way.
func (d *DB) RemoveDependency(ctx context.Context, projectID, blocker, blocked int64) error {
	if _, err := d.GetFeature(ctx, blocked); err != nil {
		return err
	}
	_, err := d.ExecContext(ctx,
		`DELETE FROM feature_deps WHERE blocker_id = ? AND blocked_id = ?`, blocker, blocked)
	return err
}

// OpenBlockers returns the blockers of a feature that have not shipped.
//
// "Shipped" is the only state that clears a dependency. A rejected blocker is
// still a blocker on purpose: the dependency said this idea needs that one, and a
// refusal is a decision somebody has to revisit rather than a licence to proceed.
func (d *DB) OpenBlockers(ctx context.Context, featureID int64) ([]FeatureRef, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT f.id, f.title, f.status
		  FROM feature_deps dep JOIN features f ON f.id = dep.blocker_id
		 WHERE dep.blocked_id = ? AND f.status <> 'shipped'
		 ORDER BY f.id`, featureID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FeatureRef{}
	for rows.Next() {
		var r FeatureRef
		if err := rows.Scan(&r.ID, &r.Title, &r.Status); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// BlockedBy returns every blocker, shipped or not, for the feature page.
func (d *DB) BlockedBy(ctx context.Context, featureID int64) ([]FeatureRef, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT f.id, f.title, f.status
		  FROM feature_deps dep JOIN features f ON f.id = dep.blocker_id
		 WHERE dep.blocked_id = ? ORDER BY f.id`, featureID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FeatureRef{}
	for rows.Next() {
		var r FeatureRef
		if err := rows.Scan(&r.ID, &r.Title, &r.Status); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Blocks returns the features this one is holding up.
func (d *DB) Blocks(ctx context.Context, featureID int64) ([]FeatureRef, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT f.id, f.title, f.status
		  FROM feature_deps dep JOIN features f ON f.id = dep.blocked_id
		 WHERE dep.blocker_id = ? ORDER BY f.id`, featureID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FeatureRef{}
	for rows.Next() {
		var r FeatureRef
		if err := rows.Scan(&r.ID, &r.Title, &r.Status); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// pathTo walks the blocked->blocker edges from `from` looking for `target` and
// returns the path when it finds one.
//
// Iterative with a visited set and a depth cap: a recursive walk on a graph a
// caller controls is a stack overflow waiting for the first long chain, and the
// cap is what turns an adversarially deep graph into a refusal instead of a hang.
func (d *DB) pathTo(ctx context.Context, from, target int64) ([]int64, error) {
	const maxDepth = 64
	type node struct {
		id   int64
		path []int64
	}
	visited := map[int64]bool{from: true}
	queue := []node{{id: from, path: []int64{from}}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.id == target {
			return cur.path, nil
		}
		if len(cur.path) > maxDepth {
			return nil, fmt.Errorf("%w: dependency chain is deeper than %d; refusing to walk it",
				ErrInvalid, maxDepth)
		}
		// The edge points blocker -> blocked, so the walk follows blocked_id given
		// blocker_id. Walking the other way answers a different question ("what is
		// waiting on me?") and finds no cycle, which is how the first version of this
		// check let every cycle through.
		rows, err := d.QueryContext(ctx,
			`SELECT blocked_id FROM feature_deps WHERE blocker_id = ? ORDER BY blocked_id`, cur.id)
		if err != nil {
			return nil, err
		}
		var next []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			next = append(next, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for _, id := range next {
			if visited[id] {
				continue
			}
			visited[id] = true
			p := make([]int64, len(cur.path)+1)
			copy(p, cur.path)
			p[len(cur.path)] = id
			queue = append(queue, node{id: id, path: p})
		}
	}
	return nil, nil
}

// describeCycle renders the loop the refused edge would have closed.
//
// path runs from the blocked feature to the blocker, one "blocks" step per hop, so
// the loop is: path[0] blocks path[1] ... blocks blocker, and the edge being added
// (blocker blocks blocked) closes it back to the start. Listing it in reading order
// is the difference between "that would close a cycle" and a reader being able to
// find the edge they created.
func describeCycle(blocker int64, path []int64, blocked int64) string {
	parts := make([]string, 0, len(path)+1)
	for i := 0; i+1 < len(path); i++ {
		parts = append(parts, fmt.Sprintf("#%d blocks #%d", path[i], path[i+1]))
	}
	parts = append(parts, fmt.Sprintf("#%d blocks #%d", blocker, blocked))
	return "that would close a cycle: " + strings.Join(parts, ", ")
}

// describeBlockers names what is standing in the way, for the status refusal.
func describeBlockers(blockers []FeatureRef) string {
	parts := make([]string, 0, len(blockers))
	for _, b := range blockers {
		parts = append(parts, fmt.Sprintf("#%d %q (%s)", b.ID, b.Title, b.Status))
	}
	return strings.Join(parts, ", ")
}

// countOpenBlockers is the badge's cheap read: one number per feature.
func (d *DB) countOpenBlockers(ctx context.Context, featureID int64) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `
		SELECT count(*) FROM feature_deps dep JOIN features f ON f.id = dep.blocker_id
		 WHERE dep.blocked_id = ? AND f.status <> 'shipped'`, featureID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}
