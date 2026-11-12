package store

// Milestones (docs/specs/idea-intake-and-milestones-spec.md §4, R17).
//
// Grouping, which the schema never had: phases live in board_columns and themes
// live in tags, so a release a maintainer is working towards had no
// representation and the plan lived in Markdown.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Milestone statuses. planned/active/done/cancelled, matching the CHECK in
// 0037_milestones.sql.
const (
	MilestonePlanned   = "planned"
	MilestoneActive    = "active"
	MilestoneDone      = "done"
	MilestoneCancelled = "cancelled"
)

var validMilestoneStatuses = map[string]bool{
	MilestonePlanned: true, MilestoneActive: true, MilestoneDone: true, MilestoneCancelled: true,
}

// Milestone is a group of features with an optional date.
type Milestone struct {
	ID         int64    `json:"id"`
	ProjectID  int64    `json:"project_id"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	TargetDate *float64 `json:"target_date"` // null = no date agreed
	Status     string   `json:"status"`
	CreatedAt  float64  `json:"created_at"`

	// Counts are COMPUTED on read, never stored: a stored total drifts the first
	// time a feature leaves the milestone by another path.
	Total     int `json:"total"`
	Open      int `json:"open"`
	Shipped   int `json:"shipped"`
	Seeded    int `json:"seeded"`    // features with no creation audit event
	Unshipped int `json:"unshipped"` // shipped features with no shipment event
	// AtRisk is derived, never stored (spec §4.2 item 8): a target date in the past
	// with open features. Storing it would need a job to keep it true, and a stale
	// "on track" badge is worse than no badge.
	AtRisk bool `json:"at_risk"`
}

// CreateMilestone inserts one and returns it with its (zero) counts.
func (d *DB) CreateMilestone(ctx context.Context, projectID int64, title, body string, target *float64) (Milestone, error) {
	if projectID == 0 || title == "" {
		return Milestone{}, fmt.Errorf("%w: project_id and title are required", ErrInvalid)
	}
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO milestones (project_id, title, body, target_date, status, created_at)
		VALUES (?, ?, ?, ?, 'planned', ?)`, projectID, title, body, target, now)
	if err != nil {
		if isUniqueViolation(err) {
			return Milestone{}, fmt.Errorf("%w: a milestone called %q already exists in this project", ErrDuplicate, title)
		}
		return Milestone{}, err
	}
	id, _ := res.LastInsertId()
	return d.GetMilestone(ctx, projectID, id)
}

// GetMilestone loads one, scoped by project.
//
// The project scope is not decoration: another project's milestone answers
// ErrNotFound rather than 403, because a distinct status for "exists but not
// yours" is an existence oracle (spec §7 invariant 6).
func (d *DB) GetMilestone(ctx context.Context, projectID, id int64) (Milestone, error) {
	row := d.QueryRowContext(ctx, `
		SELECT id, project_id, title, body, target_date, status, created_at
		  FROM milestones WHERE id = ? AND project_id = ?`, id, projectID)
	var m Milestone
	var target sql.NullFloat64
	if err := row.Scan(&m.ID, &m.ProjectID, &m.Title, &m.Body, &target, &m.Status, &m.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Milestone{}, fmt.Errorf("%w: milestone %d", ErrNotFound, id)
		}
		return Milestone{}, err
	}
	if target.Valid {
		t := target.Float64
		m.TargetDate = &t
	}
	if err := d.countMilestoneFeatures(ctx, &m); err != nil {
		return Milestone{}, err
	}
	return m, nil
}

// ListMilestones returns a project's milestones ordered by date, undated last.
//
// `target_date IS NULL` first in the ORDER BY rather than NULLS LAST, which is not
// available on every SQLite this has to run on. Undated milestones sort last
// because a date is a promise and a milestone without one has made no promise.
func (d *DB) ListMilestones(ctx context.Context, projectID int64) ([]Milestone, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, project_id, title, body, target_date, status, created_at
		  FROM milestones WHERE project_id = ?
		 ORDER BY target_date IS NULL, target_date, id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Milestone{}
	for rows.Next() {
		var m Milestone
		var target sql.NullFloat64
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.Title, &m.Body, &target, &m.Status, &m.CreatedAt); err != nil {
			return nil, err
		}
		if target.Valid {
			t := target.Float64
			m.TargetDate = &t
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := d.countMilestoneFeatures(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// countMilestoneFeatures fills the computed counts on one milestone.
//
// The provenance rule (spec §4.3): a feature counts as SHIPPED only when a
// shipment left an audit event, and Seeded/Unshipped say how much of the group the
// seeding script produced. A progress bar computed from `status` alone measures
// whoever ran the import -- 925 of 1,874 features on this instance have no
// creation event at all, which is the same trap internal/store/stats.go exists to
// publish.
func (d *DB) countMilestoneFeatures(ctx context.Context, m *Milestone) error {
	var total, open, shipped, seeded, unshipped int
	err := d.QueryRowContext(ctx, `
		SELECT count(*),
		       COALESCE(sum(CASE WHEN f.status IN ('shipped','rejected') THEN 0 ELSE 1 END), 0),
		       COALESCE(sum(CASE WHEN f.status = 'shipped' AND EXISTS (
		            SELECT 1 FROM audit_log a WHERE a.entity = 'feature' AND a.entity_id = f.id
		              AND a.action = 'feature.status' AND a.detail LIKE '%-> shipped%'
		            ) THEN 1 ELSE 0 END), 0),
		       COALESCE(sum(CASE WHEN NOT EXISTS (
		            SELECT 1 FROM audit_log a WHERE a.entity = 'feature' AND a.entity_id = f.id
		              AND a.action = 'create_feature'
		            ) THEN 1 ELSE 0 END), 0),
		       COALESCE(sum(CASE WHEN f.status = 'shipped' AND NOT EXISTS (
		            SELECT 1 FROM audit_log a WHERE a.entity = 'feature' AND a.entity_id = f.id
		              AND a.action = 'feature.status' AND a.detail LIKE '%-> shipped%'
		            ) THEN 1 ELSE 0 END), 0)
		  FROM features f WHERE f.milestone_id = ?`, m.ID).
		Scan(&total, &open, &shipped, &seeded, &unshipped)
	if err != nil {
		return err
	}
	m.Total, m.Open, m.Shipped, m.Seeded, m.Unshipped = total, open, shipped, seeded, unshipped
	m.AtRisk = m.TargetDate != nil && *m.TargetDate > 0 && m.Open > 0 &&
		*m.TargetDate < float64(time.Now().Unix()) &&
		(m.Status == MilestonePlanned || m.Status == MilestoneActive)
	return nil
}

// SetFeatureMilestone assigns (or, with a nil milestone, unassigns) one feature.
//
// Unassigning is the same call as assigning on purpose: the backlog is the set no
// milestone claims, so leaving it must be as easy as joining it -- otherwise the
// first wrong click is permanent until somebody edits the database.
//
// `position` orders the feature INSIDE the milestone (§6). Zero means "wherever",
// which is what the overwhelming majority of rows will say.
func (d *DB) SetFeatureMilestone(ctx context.Context, projectID, featureID int64, milestoneID *int64, position int) (int, error) {
	f, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return 0, err
	}
	if f.ProjectID != projectID {
		return 0, fmt.Errorf("%w: feature %d", ErrNotFound, featureID)
	}
	if milestoneID != nil {
		if _, err := d.GetMilestone(ctx, projectID, *milestoneID); err != nil {
			return 0, err
		}
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE features SET milestone_id = ?, milestone_position = ?, updated_at = ? WHERE id = ?`,
		milestoneID, position, float64(time.Now().Unix()), featureID); err != nil {
		return 0, err
	}
	n, err := d.countOpenBlockers(ctx, featureID)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// AssignFeatures assigns a selection of features to a milestone in one call
// (spec §6, "bulk assign a selection"), appending them in the order given.
//
// All or nothing: a batch that half-succeeded would leave the caller without a way
// to say what happened, and the audit row names ONE act, so a partial move would be
// a single act describing several different outcomes.
func (d *DB) AssignFeatures(ctx context.Context, projectID, milestoneID int64, featureIDs []int64) (int, error) {
	if _, err := d.GetMilestone(ctx, projectID, milestoneID); err != nil {
		return 0, err
	}
	if len(featureIDs) == 0 {
		return 0, fmt.Errorf("%w: no features named", ErrInvalid)
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// Positions continue after the current tail, so a batch lands after what is
	// already there rather than interleaving.
	var tail int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(max(milestone_position), 0) FROM features WHERE milestone_id = ?`,
		milestoneID).Scan(&tail); err != nil {
		return 0, err
	}
	now := float64(time.Now().Unix())
	done := 0
	for _, fid := range featureIDs {
		var owner int64
		err := tx.QueryRowContext(ctx, `SELECT project_id FROM features WHERE id = ?`, fid).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != projectID) {
			return 0, fmt.Errorf("%w: feature %d", ErrNotFound, fid)
		}
		if err != nil {
			return 0, err
		}
		tail++
		if _, err := tx.ExecContext(ctx,
			`UPDATE features SET milestone_id = ?, milestone_position = ?, updated_at = ? WHERE id = ?`,
			milestoneID, tail, now, fid); err != nil {
			return 0, err
		}
		done++
	}
	return done, tx.Commit()
}

// ReorderMilestone writes an explicit order for a milestone's features.
//
// The order is the caller's array, one-based; a feature named that is not in the
// milestone is refused rather than silently skipped, because a reorder that dropped
// somebody's work would look like a successful reorder.
func (d *DB) ReorderMilestone(ctx context.Context, projectID, milestoneID int64, featureIDs []int64) error {
	if _, err := d.GetMilestone(ctx, projectID, milestoneID); err != nil {
		return err
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, fid := range featureIDs {
		var ms sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT milestone_id FROM features WHERE id = ? AND project_id = ?`,
			fid, projectID).Scan(&ms); err != nil {
			return fmt.Errorf("%w: feature %d", ErrNotFound, fid)
		}
		if !ms.Valid || ms.Int64 != milestoneID {
			return fmt.Errorf("%w: feature %d is not in this milestone", ErrInvalid, fid)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE features SET milestone_position = ? WHERE id = ?`, i+1, fid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MilestoneFeatures returns a milestone's features ordered for its page: by the
// migration's position first, then by the id that records creation order.
func (d *DB) MilestoneFeatures(ctx context.Context, projectID, milestoneID int64) ([]Feature, error) {
	if _, err := d.GetMilestone(ctx, projectID, milestoneID); err != nil {
		return nil, err
	}
	rows, err := d.QueryContext(ctx, `
		SELECT id, project_id, author_id, title, body, effort, status, elo_r, elo_rd, elo_vol,
		       strategic_weight, impact, effort_score, created_at, updated_at,
		       milestone_id, milestone_position
		  FROM features WHERE milestone_id = ?
		 ORDER BY milestone_position, id`, milestoneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Feature{}
	for rows.Next() {
		var f Feature
		var impact, effortScore sql.NullInt64
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.AuthorID, &f.Title, &f.Body, &f.Effort,
			&f.Status, &f.EloR, &f.EloRD, &f.EloVol, &f.StrategicWeight,
			&impact, &effortScore, &f.CreatedAt, &f.UpdatedAt,
			&f.MilestoneID, &f.MilestonePosition); err != nil {
			return nil, err
		}
		if impact.Valid {
			v := int(impact.Int64)
			f.Impact = &v
		}
		if effortScore.Valid {
			v := int(effortScore.Int64)
			f.EffortScore = &v
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// MilestoneEvent is a milestone with the time it last changed shape: created, or
// moved between statuses. The feed needs that time and `created_at` is not it.
type MilestoneEvent struct {
	MilestoneID int64
	Title       string
	Status      string
	CreatedAt   float64
	// StatusChangedAt is 0 when the milestone has never moved.
	StatusChangedAt float64
}

// MilestoneEvents returns every milestone with its last status change, newest
// signal first, for the milestones feed (§5, "a feed item when a milestone opens or
// closes").
//
// The change time comes from audit_log, not from a column: a milestone has no
// updated_at, and adding one would be a second truth about when it moved whose only
// writer would be the same code that writes the act.
func (d *DB) MilestoneEvents(ctx context.Context, projectID int64) ([]MilestoneEvent, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT m.id, m.title, m.status, m.created_at,
		       COALESCE((SELECT max(a.created_at) FROM audit_log a
		                  WHERE a.entity = 'milestone' AND a.entity_id = m.id
		                    AND a.action = 'milestone.status'), 0)
		  FROM milestones m WHERE m.project_id = ?
		 ORDER BY m.id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MilestoneEvent{}
	for rows.Next() {
		var e MilestoneEvent
		if err := rows.Scan(&e.MilestoneID, &e.Title, &e.Status, &e.CreatedAt, &e.StatusChangedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MilestoneSlippage is one row of the roadmap's hardest number (spec §5).
//
// Both fields are POINTERS and nil means unmeasured, not zero: a slip of 0 days is
// the best possible answer and a zero is the easiest thing in the world to fake by
// accident. This is the same absence rule the instance's responsiveness metric
// already carries (UnknownReviewHours).
type MilestoneSlippage struct {
	MilestoneID int64    `json:"milestone_id"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	TargetDate  *float64 `json:"target_date"`
	// SlipDays is positive when the milestone's last shipment came after its target.
	// Only a closed milestone has one.
	SlipDays *float64 `json:"slip_days"`
	// OldestOpenDays is how long the oldest unfinished feature in an OPEN milestone
	// has been waiting. Only an open milestone has one.
	OldestOpenDays *float64 `json:"oldest_open_days"`
}

// RoadmapSlippage computes the plan-versus-reality figure for every milestone.
//
// The shipment time comes from `audit_log`, not from `features.updated_at`: the
// update timestamp moves when anything about the row changes, so a typo fix would
// look like a shipment, and 925 of 1,874 features on this instance never had a
// creation event at all -- a plan measured from status columns measures the import.
func (d *DB) RoadmapSlippage(ctx context.Context, projectID int64) ([]MilestoneSlippage, error) {
	ms, err := d.ListMilestones(ctx, projectID)
	if err != nil {
		return nil, err
	}
	now := float64(time.Now().Unix())
	out := make([]MilestoneSlippage, 0, len(ms))
	for _, m := range ms {
		row := MilestoneSlippage{
			MilestoneID: m.ID, Title: m.Title, Status: m.Status, TargetDate: m.TargetDate,
		}
		if m.Status == MilestoneDone || m.Status == MilestoneCancelled {
			var lastShip sql.NullFloat64
			if err := d.QueryRowContext(ctx, `
				SELECT max(a.created_at) FROM audit_log a
				  JOIN features f ON f.id = a.entity_id
				 WHERE f.milestone_id = ? AND a.entity = 'feature' AND a.action = 'feature.status'
				   AND a.detail LIKE '%-> shipped%'`, m.ID).Scan(&lastShip); err != nil {
				return nil, err
			}
			if lastShip.Valid && m.TargetDate != nil && *m.TargetDate > 0 {
				slip := (lastShip.Float64 - *m.TargetDate) / 86400.0
				row.SlipDays = &slip
			}
		} else {
			var oldest sql.NullFloat64
			if err := d.QueryRowContext(ctx, `
				SELECT min(f.created_at) FROM features f
				 WHERE f.milestone_id = ? AND f.status NOT IN ('shipped','rejected')`, m.ID).Scan(&oldest); err != nil {
				return nil, err
			}
			if oldest.Valid && oldest.Float64 > 0 {
				age := (now - oldest.Float64) / 86400.0
				row.OldestOpenDays = &age
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// UpdateMilestone edits the editable fields. Status has its own call because it
// changes what the milestone MEANS and writes an audit act.
func (d *DB) UpdateMilestone(ctx context.Context, projectID, id int64, title, body string, target *float64) (Milestone, error) {
	if title == "" {
		return Milestone{}, fmt.Errorf("%w: title is required", ErrInvalid)
	}
	res, err := d.ExecContext(ctx, `
		UPDATE milestones SET title = ?, body = ?, target_date = ?
		 WHERE id = ? AND project_id = ?`, title, body, target, id, projectID)
	if err != nil {
		if isUniqueViolation(err) {
			return Milestone{}, fmt.Errorf("%w: a milestone called %q already exists in this project", ErrDuplicate, title)
		}
		return Milestone{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Milestone{}, fmt.Errorf("%w: milestone %d", ErrNotFound, id)
	}
	return d.GetMilestone(ctx, projectID, id)
}

// SetMilestoneStatus moves a milestone between planned/active and writes the act.
// Closing (done/cancelled) goes through CloseMilestone, which also decides what
// happens to unfinished features.
func (d *DB) SetMilestoneStatus(ctx context.Context, projectID, id, actorID int64, status string) (Milestone, error) {
	if !validMilestoneStatuses[status] {
		return Milestone{}, fmt.Errorf("%w: unknown milestone status %q", ErrInvalid, status)
	}
	m, err := d.GetMilestone(ctx, projectID, id)
	if err != nil {
		return Milestone{}, err
	}
	if m.Status == status {
		return m, nil
	}
	if _, err := d.ExecContext(ctx, `UPDATE milestones SET status = ? WHERE id = ? AND project_id = ?`,
		status, id, projectID); err != nil {
		return Milestone{}, err
	}
	if err := d.AddAudit(ctx, projectID, actorID, "milestone.status", "milestone", id,
		m.Status+" -> "+status); err != nil {
		return Milestone{}, err
	}
	return d.GetMilestone(ctx, projectID, id)
}

// CloseMilestone closes a milestone and decides what happens to its unfinished
// features (spec §4.4).
//
// `done` with open features is a lie and a blocked close is a trap, so the two
// are reconciled explicitly: a successor moves them (one audit act each, so "why
// did this feature move" is answerable from the log), and a cancellation moves
// none -- the features stay where they are, which is what "we are not doing this"
// means for the work already grouped under it.
func (d *DB) CloseMilestone(ctx context.Context, projectID, id, actorID int64, successor *int64, cancel bool) (Milestone, int, error) {
	m, err := d.GetMilestone(ctx, projectID, id)
	if err != nil {
		return Milestone{}, 0, err
	}
	if m.Status == MilestoneDone || m.Status == MilestoneCancelled {
		return Milestone{}, 0, fmt.Errorf("%w: milestone %d is already %s", ErrConflict, id, m.Status)
	}
	if successor != nil {
		if *successor == id {
			return Milestone{}, 0, fmt.Errorf("%w: a milestone cannot succeed itself", ErrInvalid)
		}
		if _, err := d.GetMilestone(ctx, projectID, *successor); err != nil {
			return Milestone{}, 0, err
		}
	} else if !cancel && m.Open > 0 {
		return Milestone{}, 0, fmt.Errorf(
			"%w: %d feature(s) are still open; name a successor milestone or cancel instead",
			ErrInvalid, m.Open)
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Milestone{}, 0, err
	}
	defer tx.Rollback()

	moved := 0
	var movedIDs []int64
	if successor != nil {
		rows, qerr := tx.QueryContext(ctx, `
			SELECT id FROM features
			 WHERE milestone_id = ? AND status NOT IN ('shipped','rejected')`, id)
		if qerr != nil {
			return Milestone{}, 0, qerr
		}
		for rows.Next() {
			var fid int64
			if err := rows.Scan(&fid); err != nil {
				rows.Close()
				return Milestone{}, 0, err
			}
			movedIDs = append(movedIDs, fid)
		}
		rows.Close()
		for _, fid := range movedIDs {
			if _, err := tx.ExecContext(ctx,
				`UPDATE features SET milestone_id = ? WHERE id = ?`, *successor, fid); err != nil {
				return Milestone{}, 0, err
			}
			moved++
		}
	}

	status := MilestoneDone
	if cancel {
		status = MilestoneCancelled
	}
	if _, err := tx.ExecContext(ctx, `UPDATE milestones SET status = ? WHERE id = ?`, status, id); err != nil {
		return Milestone{}, 0, err
	}
	detail := fmt.Sprintf("%s -> %s", m.Status, status)
	if successor != nil {
		detail += fmt.Sprintf("; %d feature(s) moved to milestone %d", moved, *successor)
	}
	now := float64(time.Now().Unix())
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
		VALUES (?, ?, 'milestone.status', 'milestone', ?, ?, ?)`,
		projectID, actorID, id, detail, now); err != nil {
		return Milestone{}, 0, err
	}
	// One act per moved feature, in the same transaction: a feature that moved with
	// no record is the exact gap this rule exists to prevent.
	if successor != nil {
		for _, fid := range movedIDs {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
				VALUES (?, ?, 'feature.milestone', 'feature', ?, ?, ?)`,
				projectID, actorID, fid,
				fmt.Sprintf("milestone %d -> %d (closed)", id, *successor), now); err != nil {
				return Milestone{}, 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Milestone{}, 0, err
	}
	out, err := d.GetMilestone(ctx, projectID, id)
	return out, moved, err
}
