package store

import (
	"context"
	"fmt"
	"sort"
)

// RisingMinDays is the shortest series that can carry a slope (spec §4.5.5.7).
//
// Fourteen days is the spec's number, not a tuned one, and it exists to stop a
// two-snapshot accident reading as a trend: with a daily timer, two runs one day apart
// turn any single shipped feature into "seven per week". Below the floor there is NO
// SLOPE, and the project is absent from the ranking -- the same "absent, never last"
// shape as the responsiveness leaderboard, for the same reason.
const RisingMinDays = 14

// RisingRow is one project's change between its first and last recorded snapshot.
//
// The DELTAS ARE SIGNED AND NOT CLAMPED. A project can genuinely go backwards (a feature
// reopened, a complaint deleted), and a ranking that silently reported 0 for that would
// be reporting a measurement it did not take. The ranking sorts descending, so a falling
// project lands at the bottom where a reader can see it.
type RisingRow struct {
	ProjectID int64  `json:"project_id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	// Days is the span between the two snapshots the slope was taken over -- its own
	// number, because a slope over 14 days and one over 90 are different claims.
	Days              int     `json:"days"`
	FeaturesShipped   int     `json:"features_shipped"`
	ComplaintsFiled   int     `json:"complaints_filed"`
	FeaturesPerWeek   float64 `json:"features_per_week"`
	ComplaintsPerWeek float64 `json:"complaints_per_week"`
	// HealthScore is a LEVEL shown beside the slope, never a substitute for it
	// (spec §4.5.5.7: "rising is a change, health is a level"). It is nil when nothing
	// was measured, which the page renders as a dash -- `project_metrics.health_score`
	// is 0 for every project on the live instance, so reading that column would publish
	// a confident "0" for the whole catalog.
	HealthScore *float64 `json:"health_score"`
}

// RisingProjects ranks projects by their organic work rate between two recorded
// snapshots (STAT-3.4, spec §4.5.5.7).
//
// FOUR RULES, EACH OF WHICH IS A DEFECT IF DROPPED:
//
//  1. THE SPAN IS THE PROJECT'S OWN. Its first and last rows, not the instance's: a
//     project created a week ago has a week of series, and measuring it against the
//     instance's oldest day would invent a flat history it never had.
//  2. A SPAN UNDER RisingMinDays HAS NO SLOPE and the project is absent.
//  3. THE RATE IS PER WEEK AND THE SPAN RIDES ALONG. The division is by the days
//     actually between the rows, so a missed snapshot lengthens the divisor instead of
//     inflating the rate -- which is what makes a stored delta unnecessary.
//  4. PUBLIC ONLY. Like the responsiveness leaderboard and unlike the census, this
//     names projects.
//
// Sorted by features per week, then complaints per week, then slug. Without the slug
// tiebreak two projects at the same rate swap places between requests.
func (d *DB) RisingProjects(ctx context.Context) ([]RisingRow, error) {
	rows, err := d.QueryContext(ctx, `
		WITH span AS (
		    SELECT project_id, MIN(day) AS first_day, MAX(day) AS last_day
		      FROM project_stats GROUP BY project_id
		)
		SELECT p.id, p.slug, p.name,
		       CAST(julianday(s.last_day) - julianday(s.first_day) AS INTEGER) AS days,
		       l.organic_features_shipped - f.organic_features_shipped,
		       l.organic_complaints_filed - f.organic_complaints_filed
		  FROM span s
		  JOIN project_stats f ON f.project_id = s.project_id AND f.day = s.first_day
		  JOIN project_stats l ON l.project_id = s.project_id AND l.day = s.last_day
		  JOIN projects p ON p.id = s.project_id
		 WHERE p.visibility = 'public'
		   AND julianday(s.last_day) - julianday(s.first_day) >= ?`,
		RisingMinDays)
	if err != nil {
		return nil, fmt.Errorf("rising projects: %w", err)
	}
	defer rows.Close()

	out := []RisingRow{}
	for rows.Next() {
		var r RisingRow
		if err := rows.Scan(&r.ProjectID, &r.Slug, &r.Name, &r.Days,
			&r.FeaturesShipped, &r.ComplaintsFiled); err != nil {
			return nil, fmt.Errorf("rising projects: scan: %w", err)
		}
		weeks := float64(r.Days) / 7
		r.FeaturesPerWeek = float64(r.FeaturesShipped) / weeks
		r.ComplaintsPerWeek = float64(r.ComplaintsFiled) / weeks
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rising projects: %w", err)
	}

	// The health column is deliberately NOT in the query above: `project_metrics`
	// carries a placeholder row for every project with health_score = 0, so a JOIN
	// would publish a confident zero for the whole catalog. ProjectHealth is the one
	// honest source (it distinguishes "measured" from "no row yet"), and it is a
	// per-project read -- acceptable here because the ranking is a short list.
	for i := range out {
		h, known, err := d.ProjectHealth(ctx, out[i].ProjectID)
		if err != nil {
			return nil, fmt.Errorf("rising projects: health of %s: %w", out[i].Slug, err)
		}
		if known {
			v := h
			out[i].HealthScore = &v
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].FeaturesPerWeek != out[j].FeaturesPerWeek {
			return out[i].FeaturesPerWeek > out[j].FeaturesPerWeek
		}
		if out[i].ComplaintsPerWeek != out[j].ComplaintsPerWeek {
			return out[i].ComplaintsPerWeek > out[j].ComplaintsPerWeek
		}
		return out[i].Slug < out[j].Slug
	})
	return out, nil
}
