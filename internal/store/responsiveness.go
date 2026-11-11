package store

import (
	"context"
	"fmt"

	"git.polarisocial.xyz/concord/concord/internal/discovery"
)

// ResponsivenessRow is one project's newcomer responsiveness: the median hours
// from a newcomer's first pull request to the first answer by somebody else,
// always carried with the sample it was computed over (spec §4.5.7.2).
type ResponsivenessRow struct {
	ProjectID int64   `json:"project_id"`
	Slug      string  `json:"slug"`
	Name      string  `json:"name"`
	Hours     float64 `json:"newcomer_response_hours"`
	N         int     `json:"n"`
}

// ResponsivenessLeaderboard returns the projects that publish a newcomer
// responsiveness median, fastest first (STAT-3.5, spec §4.5.7.2).
//
// THREE RULES, AND THE FIRST IS THE POINT OF THE ROUTE:
//
//  1. A PROJECT UNDER THE FLOOR IS ABSENT, NEVER LAST. The WHERE clause drops
//     it; it is not sorted to the bottom with a null hole. "We have not measured
//     this" and "this is the worst" are different facts, and a leaderboard that
//     renders 0.0 for a project nobody measured ranks it BEST -- the one
//     direction a reader will act on.
//  2. THE SAMPLE IS PART OF THE ROW. Two projects at 2h are not the same claim
//     when one rests on 5 PRs and the other on 500, and the reader cannot tell
//     them apart without `n`.
//  3. PRIVATE PROJECTS ARE NOT RANKED. Every other statistics route publishes an
//     AGGREGATE, which a private project contributes to invisibly; this one
//     publishes a ranking of NAMED projects, so an unlisted project's position
//     would be a disclosure its owner did not make. It is absent for that reason,
//     not for want of samples.
//
// The tiebreak on slug is not cosmetic: without it two projects at the same
// median swap places between two requests, and a ranking that is not stable
// cannot be compared with its own previous rendering.
func (d *DB) ResponsivenessLeaderboard(ctx context.Context) ([]ResponsivenessRow, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT p.id, p.slug, p.name, pm.newcomer_response_hours, pm.newcomer_sample
		  FROM project_metrics pm
		  JOIN projects p ON p.id = pm.project_id
		 WHERE pm.newcomer_response_hours IS NOT NULL
		   AND pm.newcomer_sample >= ?
		   AND p.visibility = 'public'
		 ORDER BY pm.newcomer_response_hours ASC, p.slug ASC`,
		discovery.NewcomerMinSample)
	if err != nil {
		return nil, fmt.Errorf("responsiveness leaderboard: %w", err)
	}
	defer rows.Close()

	// An empty slice, not nil: the JSON contract for a list route is [] and a
	// null body would make every client write its own nil check.
	out := []ResponsivenessRow{}
	for rows.Next() {
		var r ResponsivenessRow
		if err := rows.Scan(&r.ProjectID, &r.Slug, &r.Name, &r.Hours, &r.N); err != nil {
			return nil, fmt.Errorf("responsiveness leaderboard: scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("responsiveness leaderboard: %w", err)
	}
	return out, nil
}
