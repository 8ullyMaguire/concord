// Package store is Concord's SQL data layer.
// This file adds the complaints domain (PLAN M1).
// Follow the project CRUD pattern in store.go.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/ranking"
)

// Complaint represents a structured problem report.
type Complaint struct {
	ID                  int64   `json:"id"`
	ProjectID           int64   `json:"project_id"`
	AuthorID            int64   `json:"author_id"`
	Title               string  `json:"title"`
	Body                string  `json:"body"`
	Severity            int     `json:"severity"`
	Frequency           float64 `json:"frequency"`
	StrategicMultiplier float64 `json:"strategic_multiplier"`
	Status              string  `json:"status"`
	MergedInto          *int64  `json:"merged_into,omitempty"`
	CreatedAt           float64 `json:"created_at"`
	UpdatedAt           float64 `json:"updated_at"`
}

// CreateComplaint inserts a new complaint (PLAN M1).
func (d *DB) CreateComplaint(ctx context.Context, projectID, authorID int64, title, body string, severity int, frequency, strategicMult float64) (Complaint, error) {
	if projectID == 0 || authorID == 0 {
		return Complaint{}, fmt.Errorf("%w: project_id and author_id are required", ErrInvalid)
	}
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO complaints (project_id, author_id, title, body, severity, frequency, strategic_multiplier, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'open', ?, ?)`,
		projectID, authorID, title, body, severity, frequency, strategicMult, now, now)
	if isUniqueViolation(err) {
		return Complaint{}, fmt.Errorf("%w: complaint", ErrDuplicate)
	}
	if err != nil {
		return Complaint{}, err
	}
	id, _ := res.LastInsertId()
	return d.GetComplaint(ctx, id)
}

// GetComplaint retrieves a complaint by ID.
func (d *DB) GetComplaint(ctx context.Context, id int64) (Complaint, error) {
	var c Complaint
	err := d.QueryRowContext(ctx, `
		SELECT id, project_id, author_id, title, body, severity, frequency,
			strategic_multiplier, status, merged_into, created_at, updated_at
		FROM complaints WHERE id=?`, id).Scan(
		&c.ID, &c.ProjectID, &c.AuthorID, &c.Title, &c.Body, &c.Severity,
		&c.Frequency, &c.StrategicMultiplier, &c.Status, &c.MergedInto,
		&c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Complaint{}, fmt.Errorf("%w: complaint %d", ErrNotFound, id)
	}
	return c, err
}

// ListComplaints returns complaints for a project with computed pain.
func (d *DB) ListComplaints(ctx context.Context, projectID int64, status string) ([]Complaint, error) {
	query := `SELECT id, project_id, author_id, title, body, severity, frequency,
		strategic_multiplier, status, merged_into, created_at, updated_at
		FROM complaints WHERE project_id=?`
	var args []any = []any{projectID}
	if status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := d.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var complaints []Complaint
	for rows.Next() {
		var c Complaint
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.AuthorID, &c.Title, &c.Body,
			&c.Severity, &c.Frequency, &c.StrategicMultiplier, &c.Status,
			&c.MergedInto, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		complaints = append(complaints, c)
	}
	return complaints, rows.Err()
}

// AddImpact records a "me too" endorsement on a complaint.
func (d *DB) AddImpact(ctx context.Context, complaintID, userID int64, severity int) error {
	_, err := d.GetComplaint(ctx, complaintID)
	if err != nil {
		return err
	}
	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `
		INSERT OR IGNORE INTO complaint_impacts (complaint_id, user_id, severity, created_at)
		VALUES (?, ?, ?, ?)`, complaintID, userID, severity, now)
	if err != nil {
		return fmt.Errorf("add impact: %w", err)
	}
	_, err = d.ExecContext(ctx, `UPDATE complaints SET updated_at=? WHERE id=?`, now, complaintID)
	return err
}

// ValidateComplaint moves a complaint to validated status (M1).
func (d *DB) ValidateComplaint(ctx context.Context, complaintID int64) error {
	c, err := d.GetComplaint(ctx, complaintID)
	if err != nil {
		return err
	}
	if c.Status != "open" {
		return fmt.Errorf("%w: only open complaints can be validated", ErrInvalid)
	}
	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `UPDATE complaints SET status='validated', updated_at=? WHERE id=?`, now, complaintID)
	if err != nil {
		return err
	}
	now2 := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `INSERT INTO reputation_events (project_id, user_id, kind, points, created_at)
		VALUES (?, ?, 'complaint_validated', 1, ?)`, c.ProjectID, c.AuthorID, now2)
	return err
}

// RejectComplaint closes a complaint with spam penalty (M1).
func (d *DB) RejectComplaint(ctx context.Context, complaintID int64) error {
	c, err := d.GetComplaint(ctx, complaintID)
	if err != nil {
		return err
	}
	if c.Status != "open" {
		return fmt.Errorf("%w: only open complaints can be rejected", ErrInvalid)
	}
	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `UPDATE complaints SET status='rejected', updated_at=? WHERE id=?`, now, complaintID)
	if err != nil {
		return err
	}
	now2 := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `INSERT INTO reputation_events (project_id, user_id, kind, points, created_at)
		VALUES (?, ?, 'complaint_rejected', -1, ?)`, c.ProjectID, c.AuthorID, now2)
	return err
}

// CloseComplaint marks a complaint as closed (M1).
func (d *DB) CloseComplaint(ctx context.Context, complaintID int64) error {
	_, err := d.GetComplaint(ctx, complaintID)
	if err != nil {
		return err
	}
	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `UPDATE complaints SET status='closed', updated_at=? WHERE id=?`, now, complaintID)
	return err
}

// MergeComplaints merges source into target (M1).
func (d *DB) MergeComplaints(ctx context.Context, sourceID, targetID int64) error {
	_, err := d.GetComplaint(ctx, sourceID)
	if err != nil {
		return err
	}
	target, err := d.GetComplaint(ctx, targetID)
	if err != nil {
		return err
	}
	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `UPDATE complaints SET merged_into=?, updated_at=? WHERE id=?`, targetID, now, sourceID)
	if err != nil {
		return err
	}
	_, err = d.ExecContext(ctx, `INSERT OR IGNORE INTO complaint_impacts (complaint_id, user_id, severity, created_at)
		SELECT ?, user_id, severity, created_at FROM complaint_impacts WHERE complaint_id=?`, targetID, sourceID)
	if err != nil {
		return err
	}
	return d.AddAudit(ctx, target.ProjectID, 0, "merge_complaints", "complaint", sourceID, fmt.Sprintf("merged into %d", target.ID))
}

// GetComplaintPain computes the current pain score for a complaint.
func (d *DB) GetComplaintPain(ctx context.Context, complaintID int64) (float64, error) {
	c, err := d.GetComplaint(ctx, complaintID)
	if err != nil {
		return 0, err
	}
	affected := 1.0 // reporter always counts
	rows, err := d.QueryContext(ctx, `SELECT user_id FROM complaint_impacts WHERE complaint_id=?`, complaintID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err == nil {
			affected += 1.0 + math.Log10(2.0) // simplified reputation weighting
		}
	}
	_ = rows.Err()
	return ranking.PainScore(c.Severity, c.Frequency, c.StrategicMultiplier, affected, 0, 90), nil
}

// AuditLogEntry represents an entry in the audit log.
type AuditLogEntry struct {
	ID        int64   `json:"id"`
	ProjectID *int64  `json:"project_id,omitempty"`
	ActorID   *int64  `json:"actor_id,omitempty"`
	Action    string  `json:"action"`
	Entity    string  `json:"entity"`
	EntityID  *int64  `json:"entity_id,omitempty"`
	Detail    string  `json:"detail"`
	CreatedAt float64 `json:"created_at"`
}

// AddAudit inserts an audit log entry.
func (d *DB) AddAudit(ctx context.Context, projectID int64, actorID int64, action, entity string, entityID int64, detail string) error {
	now := float64(time.Now().Unix())
	_, err := d.ExecContext(ctx, `INSERT INTO audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, projectID, actorID, action, entity, entityID, detail, now)
	return err
}

// GetAuditLog returns audit entries for a project.
func (d *DB) GetAuditLog(ctx context.Context, projectID int64) ([]AuditLogEntry, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, project_id, actor_id, action, entity, entity_id, detail, created_at
		FROM audit_log WHERE project_id=? ORDER BY created_at DESC LIMIT 100`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []AuditLogEntry
	for rows.Next() {
		var e AuditLogEntry
		var pid sql.NullInt64
		var aid sql.NullInt64
		var eid sql.NullInt64
		if err := rows.Scan(&e.ID, &pid, &aid, &e.Action, &e.Entity, &eid, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		if pid.Valid {
			e.ProjectID = &pid.Int64
		}
		if aid.Valid {
			e.ActorID = &aid.Int64
		}
		if eid.Valid {
			e.EntityID = &eid.Int64
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
