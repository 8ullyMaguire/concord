package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type MergeRequest struct {
	ID          int64    `json:"id"`
	ProjectID   int64    `json:"project_id"`
	FeatureID   int64    `json:"feature_id"`
	AuthorID    int64    `json:"author_id"`
	Title       string   `json:"title"`
	ExternalRef *string   `json:"external_ref,omitempty"`
	Status      string   `json:"status"`
	OpenedAt    float64  `json:"opened_at"`
	ClosedAt    *float64 `json:"closed_at,omitempty"`
}

type MergeApproval struct {
	MRID      int64   `json:"mr_id"`
	UserID    int64   `json:"user_id"`
	Weight    float64 `json:"weight"`
	CreatedAt float64 `json:"created_at"`
}

func (d *DB) CreateMergeRequest(ctx context.Context, projectID, featureID, authorID int64, title string) (MergeRequest, error) {
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO merge_requests (project_id, feature_id, author_id, title, status, opened_at)
		VALUES (?, ?, ?, ?, 'open', ?)`, projectID, featureID, authorID, title, now)
	if err != nil {
		return MergeRequest{}, err
	}
	id, _ := res.LastInsertId()
	return MergeRequest{ID: id, ProjectID: projectID, FeatureID: featureID, AuthorID: authorID, Title: title, Status: "open", OpenedAt: now}, nil
}

func (d *DB) GetMergeRequest(ctx context.Context, id int64) (MergeRequest, error) {
	var mr MergeRequest
	err := d.QueryRowContext(ctx, `SELECT id, project_id, feature_id, author_id, title, external_ref, status, opened_at, closed_at
		FROM merge_requests WHERE id=?`, id).Scan(
		&mr.ID, &mr.ProjectID, &mr.FeatureID, &mr.AuthorID, &mr.Title, &mr.ExternalRef, &mr.Status, &mr.OpenedAt, &mr.ClosedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MergeRequest{}, fmt.Errorf("%w: merge request %d", ErrNotFound, id)
	}
	return mr, err
}

func (d *DB) ApproveMerge(ctx context.Context, mrID, userID int64) error {
	_, err := d.GetMergeRequest(ctx, mrID)
	if err != nil {
		return err
	}
	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `INSERT OR IGNORE INTO merge_approvals (mr_id, user_id, weight, created_at)
		VALUES (?, ?, 1.0, ?)`, mrID, userID, now)
	return err
}

func (d *DB) GetMergeApprovals(ctx context.Context, mrID int64) ([]MergeApproval, error) {
	rows, err := d.QueryContext(ctx, `SELECT mr_id, user_id, weight, created_at FROM merge_approvals WHERE mr_id=?`, mrID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var approvals []MergeApproval
	for rows.Next() {
		var a MergeApproval
		if err := rows.Scan(&a.MRID, &a.UserID, &a.Weight, &a.CreatedAt); err != nil {
			return nil, err
		}
		approvals = append(approvals, a)
	}
	return approvals, rows.Err()
}

func (d *DB) ExecuteMerge(ctx context.Context, mrID int64, projectID int64) error {
	mr, err := d.GetMergeRequest(ctx, mrID)
	if err != nil {
		return err
	}
	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `UPDATE merge_requests SET status='merged', closed_at=? WHERE id=?`, now, mrID)
	if err != nil {
		return err
	}
	if err := d.UpdateFeatureStatus(ctx, mr.FeatureID, "shipped"); err != nil {
		return err
	}
	complaints, err := d.GetFeatureComplaints(ctx, mr.FeatureID)
	if err != nil {
		return err
	}
	for _, c := range complaints {
		_ = d.CloseComplaint(ctx, c.ID)
	}
	return d.AddAudit(ctx, projectID, 0, "merge", "merge_request", mrID, "merged")
}

func (d *DB) RejectMerge(ctx context.Context, mrID, projectID int64) error {
	_, err := d.GetMergeRequest(ctx, mrID)
	if err != nil {
		return err
	}
	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `UPDATE merge_requests SET status='rejected', closed_at=? WHERE id=?`, now, mrID)
	if err != nil {
		return err
	}
	return d.AddAudit(ctx, projectID, 0, "merge_reject", "merge_request", mrID, "rejected")
}
