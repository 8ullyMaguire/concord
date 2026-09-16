// Package store is Concord's SQL data layer.
// This file adds the features domain (PLAN M2).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Feature represents a proposed solution linked to complaints.
type Feature struct {
	ID              int64   `json:"id"`
	ProjectID       int64   `json:"project_id"`
	AuthorID        int64   `json:"author_id"`
	Title           string  `json:"title"`
	Body            string  `json:"body"`
	Effort          string  `json:"effort"`
	Status          string  `json:"status"`
	EloR            float64 `json:"elo_r"`
	EloRD           float64 `json:"elo_rd"`
	EloVol          float64 `json:"elo_vol"`
	StrategicWeight float64 `json:"strategic_weight"`
	CreatedAt       float64 `json:"created_at"`
	UpdatedAt       float64 `json:"updated_at"`
}

// FeatureComplaintLink links a feature to a complaint.
type FeatureComplaintLink struct {
	FeatureID   int64   `json:"feature_id"`
	ComplaintID int64   `json:"complaint_id"`
	LinkedAt    float64 `json:"linked_at"`
}

// CreateFeature inserts a new feature proposal (M2).
// Must be linked to at least one validated complaint.
func (d *DB) CreateFeature(ctx context.Context, projectID, authorID int64, title, body string, linkedComplaints []int64) (Feature, error) {
	if projectID == 0 || authorID == 0 || len(linkedComplaints) == 0 {
		return Feature{}, fmt.Errorf("%w: project_id, author_id, and at least one validated complaint are required", ErrInvalid)
	}
	now := float64(time.Now().Unix())
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Feature{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO features (project_id, author_id, title, body, effort, status, elo_r, elo_rd, elo_vol, strategic_weight, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'M', 'draft', 1500, 350, 0.06, 1.0, ?, ?)`,
		projectID, authorID, title, body, now, now)
	if err != nil {
		return Feature{}, err
	}
	featureID, _ := res.LastInsertId()
	// Link to validated complaints
	for _, cid := range linkedComplaints {
		// Verify complaint is validated
		c, err := d.GetComplaint(ctx, cid)
		if err != nil || c.Status != "validated" {
			return Feature{}, fmt.Errorf("%w: complaint %d must be validated", ErrInvalid, cid)
		}
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO feature_complaints (feature_id, complaint_id) VALUES (?, ?)`, featureID, cid)
		if err != nil {
			return Feature{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Feature{}, err
	}
	return d.GetFeature(ctx, featureID)
}

// GetFeature retrieves a feature by ID.
func (d *DB) GetFeature(ctx context.Context, id int64) (Feature, error) {
	var f Feature
	err := d.QueryRowContext(ctx, `
		SELECT id, project_id, author_id, title, body, effort, status,
			elo_r, elo_rd, elo_vol, strategic_weight, created_at, updated_at
		FROM features WHERE id=?`, id).Scan(
		&f.ID, &f.ProjectID, &f.AuthorID, &f.Title, &f.Body, &f.Effort,
		&f.Status, &f.EloR, &f.EloRD, &f.EloVol, &f.StrategicWeight,
		&f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Feature{}, fmt.Errorf("%w: feature %d", ErrNotFound, id)
	}
	return f, err
}

// ListFeatures returns features for a project, filtered by status.
func (d *DB) ListFeatures(ctx context.Context, projectID int64, status string) ([]Feature, error) {
	query := `SELECT id, project_id, author_id, title, body, effort, status,
		elo_r, elo_rd, elo_vol, strategic_weight, created_at, updated_at
		FROM features WHERE project_id=?`
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
	var features []Feature
	for rows.Next() {
		var f Feature
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.AuthorID, &f.Title, &f.Body,
			&f.Effort, &f.Status, &f.EloR, &f.EloRD, &f.EloVol, &f.StrategicWeight,
			&f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		features = append(features, f)
	}
	return features, rows.Err()
}

// LinkComplaint links a validated complaint to a feature (M2).
func (d *DB) LinkComplaint(ctx context.Context, featureID, complaintID int64) error {
	_, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return err
	}
	c, err := d.GetComplaint(ctx, complaintID)
	if err != nil {
		return err
	}
	if c.Status != "validated" {
		return fmt.Errorf("%w: complaint must be validated to link", ErrInvalid)
	}
	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `INSERT OR IGNORE INTO feature_complaints (feature_id, complaint_id) VALUES (?, ?)`, featureID, complaintID)
	if err != nil {
		return err
	}
	_, err = d.ExecContext(ctx, `UPDATE features SET updated_at=? WHERE id=?`, now, featureID)
	return err
}

// SetStrategicWeight changes a feature's strategic weight (M2).
// Maintainer+ only. Audited with old→new.
func (d *DB) SetStrategicWeight(ctx context.Context, featureID int64, newWeight float64) error {
	f, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return err
	}
	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `UPDATE features SET strategic_weight=?, updated_at=? WHERE id=?`, newWeight, now, featureID)
	if err != nil {
		return err
	}
	return d.AddAudit(ctx, f.ProjectID, 0, "set_strategic_weight", "feature", featureID, fmt.Sprintf("weight: %f → %f", f.StrategicWeight, newWeight))
}

// UpdateFeatureStatus changes a feature's status (M2).
func (d *DB) UpdateFeatureStatus(ctx context.Context, featureID int64, status string) error {
	now := float64(time.Now().Unix())
	_, err := d.ExecContext(ctx, `UPDATE features SET status=?, updated_at=? WHERE id=?`, status, now, featureID)
	return err
}

// GetFeatureComplaints returns all validated complaints linked to a feature.
func (d *DB) GetFeatureComplaints(ctx context.Context, featureID int64) ([]Complaint, error) {
	rows, err := d.QueryContext(ctx, `SELECT c.id, c.project_id, c.author_id, c.title, c.body, c.severity, c.frequency,
		c.strategic_multiplier, c.status, c.merged_into, c.created_at, c.updated_at
		FROM feature_complaints fc JOIN complaints c ON c.id = fc.complaint_id
		WHERE fc.feature_id=? ORDER BY c.created_at DESC`, featureID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var complaints []Complaint
	for rows.Next() {
		var c Complaint
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.AuthorID, &c.Title, &c.Body, &c.Severity,
			&c.Frequency, &c.StrategicMultiplier, &c.Status, &c.MergedInto,
			&c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		complaints = append(complaints, c)
	}
	return complaints, rows.Err()
}
