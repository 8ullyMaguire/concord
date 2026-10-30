package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Work items — spec §4.5.7 and migration 0030.
//
// The unit of contribution is a work item, not a repository: "contribute to X" with no named task
// makes the reader reverse-engineer a backlog they have never seen. Two kinds exist today, because
// those are the two the product can already express:
//
//   - a complaint in `open` — it needs VALIDATING, which is a fifteen-minute job and the only one
//     a newcomer can do without knowing the codebase;
//   - a feature in `ready` or `in_progress` — it needs IMPLEMENTING, which is where a claim matters.
//
// Complaints are deliberately claimable-free: validation is idempotent and two people validating the
// same complaint is not a wasted afternoon, while two people implementing the same feature is. A
// symmetric claim table would make the fifteen-minute path cost a round-trip for no benefit.

// Work kinds, as they appear in a JSON response.
const (
	WorkKindComplaint = "complaint"
	WorkKindFeature   = "feature"
)

// claimableFeatureStatuses is the set a claim is allowed on. A draft is work nobody has agreed to do;
// claiming it would commit a stranger to implementing a proposal that may be rejected tomorrow.
var claimableFeatureStatuses = map[string]bool{"ready": true, "in_progress": true}

// WorkItem is one thing a person can pick up.
type WorkItem struct {
	Kind              string  `json:"kind"`
	ID                int64   `json:"id"`
	ProjectID         int64   `json:"project_id"`
	Title             string  `json:"title"`
	Status            string  `json:"status"`
	Difficulty        string  `json:"difficulty,omitempty"`
	TimeBudgetMinutes *int    `json:"time_budget_minutes,omitempty"`
	NeedsMentor       bool    `json:"needs_mentor"`
	ClaimedBy         *int64  `json:"claimed_by,omitempty"`
	ImpactRatio       float64 `json:"impact_ratio,omitempty"`
}

// WorkItemLabel is what a maintainer sets on a feature to say what it costs a newcomer.
//
// Pointer fields on purpose: "no time budget stated" and "a budget of zero" are different claims,
// and only the first is honest for a task nobody has sized. Zero minutes is refused by the schema.
type WorkItemLabel struct {
	Difficulty        *string `json:"difficulty"`
	TimeBudgetMinutes *int    `json:"time_budget_minutes"`
	NeedsMentor       bool    `json:"needs_mentor"`
}

// SetWorkItemLabel records what a task costs. Maintainer-only at the HTTP layer: labelling something
// "good first" is a promise about review capacity, and only somebody who will review it can make it.
func (d *DB) SetWorkItemLabel(ctx context.Context, projectID, featureID int64, l WorkItemLabel) error {
	if l.Difficulty != nil {
		switch *l.Difficulty {
		case "S", "M", "L":
		case "":
			l.Difficulty = nil
		default:
			return fmt.Errorf("%w: difficulty must be S, M or L", ErrInvalid)
		}
	}
	if l.TimeBudgetMinutes != nil && *l.TimeBudgetMinutes <= 0 {
		return fmt.Errorf("%w: time budget must be a positive number of minutes", ErrInvalid)
	}
	var difficulty any
	if l.Difficulty != nil {
		difficulty = *l.Difficulty
	}
	var budget any
	if l.TimeBudgetMinutes != nil {
		budget = *l.TimeBudgetMinutes
	}
	res, err := d.ExecContext(ctx, `
		UPDATE features SET difficulty=?, time_budget_minutes=?, needs_mentor=?, updated_at=?
		WHERE id=? AND project_id=?`,
		difficulty, budget, boolInt(l.NeedsMentor), float64(time.Now().Unix()), featureID, projectID)
	if err != nil {
		return fmt.Errorf("set work item label: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Zero rows is not "nothing to change": the UPDATE matches on both keys, so it means the
		// feature does not exist in THIS project. Reporting success here would let a maintainer
		// of project A label a feature of project B and be told it worked.
		return fmt.Errorf("%w: feature %d in project %d", ErrNotFound, featureID, projectID)
	}
	return nil
}

// ClaimFeature takes the single active claim on a feature.
//
// ErrDuplicate on a live claim, mapped to 409 by the HTTP layer. The unique partial index is the
// authority, not a preceding SELECT: a check-then-insert loses the race between two claimants, and
// the index cannot.
func (d *DB) ClaimFeature(ctx context.Context, projectID, featureID, userID int64) error {
	var status string
	err := d.QueryRowContext(ctx,
		`SELECT status FROM features WHERE id=? AND project_id=?`, featureID, projectID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: feature %d in project %d", ErrNotFound, featureID, projectID)
	}
	if err != nil {
		return fmt.Errorf("claim: read feature: %w", err)
	}
	if !claimableFeatureStatuses[status] {
		return fmt.Errorf("%w: a %s feature cannot be claimed; only ready or in_progress work can",
			ErrInvalid, status)
	}
	_, err = d.ExecContext(ctx,
		`INSERT INTO work_item_claims (feature_id, user_id, claimed_at) VALUES (?, ?, ?)`,
		featureID, userID, float64(time.Now().Unix()))
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: another contributor holds the claim on feature %d", ErrDuplicate, featureID)
	}
	if err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	// A claim deliberately does NOT change the feature's status. The board is the only mechanism
	// that moves work; two mechanisms that both move it is how a board and a feed start disagreeing.
	return nil
}

// ReleaseClaim withdraws a claim. The row stays with `released_at` set: a withdrawal is a state,
// not a deletion, so "picked up and dropped twice" is answerable rather than invisible.
func (d *DB) ReleaseClaim(ctx context.Context, projectID, featureID, userID int64) error {
	res, err := d.ExecContext(ctx, `
		UPDATE work_item_claims SET released_at=?
		WHERE feature_id=? AND user_id=? AND released_at IS NULL
		  AND feature_id IN (SELECT id FROM features WHERE project_id=?)`,
		float64(time.Now().Unix()), featureID, userID, projectID)
	if err != nil {
		return fmt.Errorf("release claim: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: no claim of yours on feature %d", ErrNotFound, featureID)
	}
	return nil
}

// ListWorkItems returns a project's open work, ordered the way a first-time contributor should read
// it: unclaimed before claimed, then the cheapest within the offered budget, then by impact.
//
// The order is deterministic (id breaks every tie) so two identical requests return an identical
// list. A feed that reshuffles on refresh cannot be trusted by a reader or asserted on by a test.
func (d *DB) ListWorkItems(ctx context.Context, projectID int64) ([]WorkItem, error) {
	out := []WorkItem{}

	rows, err := d.QueryContext(ctx, `
		SELECT f.id, f.project_id, f.title, f.status,
		       COALESCE(f.difficulty, ''), f.time_budget_minutes, f.needs_mentor,
		       COALESCE(f.impact_ratio, 0),
		       (SELECT wc.user_id FROM work_item_claims wc
		         WHERE wc.feature_id = f.id AND wc.released_at IS NULL)
		FROM features f
		WHERE f.project_id=? AND f.status IN ('ready','in_progress')`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("list work items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var w WorkItem
		var claimed sql.NullInt64
		if err := rows.Scan(&w.ID, &w.ProjectID, &w.Title, &w.Status,
			&w.Difficulty, &w.TimeBudgetMinutes, &w.NeedsMentor, &w.ImpactRatio, &claimed); err != nil {
			return nil, fmt.Errorf("list work items: scan: %w", err)
		}
		w.Kind = WorkKindFeature
		if claimed.Valid {
			id := claimed.Int64
			w.ClaimedBy = &id
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list work items: %w", err)
	}

	// Complaints in `open` need validating, which is the fifteen-minute contribution. They carry no
	// claim, no difficulty and no budget: the job is to read one complaint and judge whether it
	// describes a real problem, and sizing that would be inventing a number.
	crows, err := d.QueryContext(ctx, `
		SELECT id, project_id, title, status FROM complaints
		WHERE project_id=? AND status='open'`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list work items: complaints: %w", err)
	}
	defer crows.Close()
	for crows.Next() {
		var w WorkItem
		if err := crows.Scan(&w.ID, &w.ProjectID, &w.Title, &w.Status); err != nil {
			return nil, fmt.Errorf("list work items: complaints: scan: %w", err)
		}
		w.Kind = WorkKindComplaint
		out = append(out, w)
	}
	if err := crows.Err(); err != nil {
		return nil, fmt.Errorf("list work items: complaints: %w", err)
	}

	sortWorkItems(out)
	return out, nil
}

// GetWorkItem returns one feature's work item, for the response of a write. It reports ErrNotFound
// when the feature is not in that project, and still returns a labeled item for a feature whose
// status is not claimable -- the label panel is what a maintainer edits, and a draft with no label
// is exactly the row they are about to label.
func (d *DB) GetWorkItem(ctx context.Context, projectID, featureID int64) (WorkItem, error) {
	var w WorkItem
	var claimed sql.NullInt64
	err := d.QueryRowContext(ctx, `
		SELECT f.id, f.project_id, f.title, f.status,
		       COALESCE(f.difficulty, ''), f.time_budget_minutes, f.needs_mentor,
		       COALESCE(f.impact_ratio, 0),
		       (SELECT wc.user_id FROM work_item_claims wc
		         WHERE wc.feature_id = f.id AND wc.released_at IS NULL)
		FROM features f WHERE f.id=? AND f.project_id=?`, featureID, projectID).Scan(
		&w.ID, &w.ProjectID, &w.Title, &w.Status, &w.Difficulty,
		&w.TimeBudgetMinutes, &w.NeedsMentor, &w.ImpactRatio, &claimed)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkItem{}, fmt.Errorf("%w: feature %d in project %d", ErrNotFound, featureID, projectID)
	}
	if err != nil {
		return WorkItem{}, fmt.Errorf("get work item: %w", err)
	}
	w.Kind = WorkKindFeature
	if claimed.Valid {
		id := claimed.Int64
		w.ClaimedBy = &id
	}
	return w, nil
}

// sortWorkItems is exported to the file rather than to the package: the ordering is part of the
// contract (the page and the feed must agree), and a caller that re-sorted would be a second
// definition of what "next" means.
func sortWorkItems(items []WorkItem) {
	rank := func(w WorkItem) int {
		// A complaint needing validation is the cheapest real contribution on the board, so it
		// leads regardless of size: it has no budget to compare, and burying it under features
		// would hide the only path a newcomer can walk without the codebase.
		if w.Kind == WorkKindComplaint {
			return -1
		}
		return 0
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		// Unclaimed before claimed: a claimed item is somebody's, and offering it invites duplicate
		// work even though the claim API refuses a second claim.
		if (a.ClaimedBy == nil) != (b.ClaimedBy == nil) {
			return a.ClaimedBy == nil
		}
		// Cheapest first, and an unlabelled item after every labelled one: unknown size is worse
		// information than a stated size, not better.
		switch {
		case a.TimeBudgetMinutes != nil && b.TimeBudgetMinutes != nil:
			if *a.TimeBudgetMinutes != *b.TimeBudgetMinutes {
				return *a.TimeBudgetMinutes < *b.TimeBudgetMinutes
			}
		case a.TimeBudgetMinutes != nil:
			return true
		case b.TimeBudgetMinutes != nil:
			return false
		}
		if a.ImpactRatio != b.ImpactRatio {
			return a.ImpactRatio > b.ImpactRatio
		}
		return a.ID < b.ID
	})
}
