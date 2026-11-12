package store

// Evidence that is not a complaint (docs/specs/idea-intake-and-milestones-spec.md
// §3.4, R16).
//
// §6.2's traceability rule is about EVIDENCE, not about the complaints table: a
// project documenting itself has a spec revision or a field report rather than
// pain, and telling it to restate its own document as pain is how a rule stops
// being followed and starts being worked around. Complaints keep their own table
// (feature_complaints) so there is exactly one truth per kind; this one holds the
// other two.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The kinds a feature may cite besides a complaint.
const (
	EvidenceDocument    = "document"
	EvidenceFieldReport = "field_report"
)

// FeatureEvidence is one row of alternative evidence.
type FeatureEvidence struct {
	Kind      string  `json:"kind"`
	RefID     int64   `json:"ref_id"`
	Title     string  `json:"title"` // the document title or the report's version
	CreatedAt float64 `json:"created_at"`
}

// AddFeatureEvidence links a document or a field report to a feature as the
// reason it exists.
//
// The reference is validated against its own table and project: an evidence row
// pointing at another project's document would let a maintainer cite evidence
// they cannot read, and the failure would appear as a broken link rather than a
// refusal.
func (d *DB) AddFeatureEvidence(ctx context.Context, projectID, featureID int64, kind string, refID int64) error {
	if kind != EvidenceDocument && kind != EvidenceFieldReport {
		return fmt.Errorf("%w: kind must be %q or %q", ErrInvalid, EvidenceDocument, EvidenceFieldReport)
	}
	f, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return err
	}
	if f.ProjectID != projectID {
		return fmt.Errorf("%w: feature %d", ErrNotFound, featureID)
	}
	var owner int64
	table := "project_documents"
	if kind == EvidenceFieldReport {
		table = "field_reports"
	}
	err = d.QueryRowContext(ctx,
		`SELECT project_id FROM `+table+` WHERE id = ?`, refID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s %d", ErrNotFound, kind, refID)
	}
	if err != nil {
		return err
	}
	if owner != projectID {
		return fmt.Errorf("%w: %s %d is not in this project", ErrNotFound, kind, refID)
	}
	_, err = d.ExecContext(ctx, `
		INSERT INTO feature_evidence (feature_id, kind, ref_id, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(feature_id, kind, ref_id) DO NOTHING`,
		featureID, kind, refID, float64(time.Now().Unix()))
	return err
}

// RemoveFeatureEvidence deletes one row. A feature that then has no evidence at
// all stays where it is -- the rule is enforced on the NEXT transition to ready,
// not retroactively, because silently un-readying work somebody is doing is worse
// than letting the next reader see it.
func (d *DB) RemoveFeatureEvidence(ctx context.Context, projectID, featureID int64, kind string, refID int64) error {
	f, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return err
	}
	if f.ProjectID != projectID {
		return fmt.Errorf("%w: feature %d", ErrNotFound, featureID)
	}
	_, err = d.ExecContext(ctx,
		`DELETE FROM feature_evidence WHERE feature_id = ? AND kind = ? AND ref_id = ?`,
		featureID, kind, refID)
	return err
}

// ListFeatureEvidence returns a feature's alternative evidence, newest first,
// with the label a page can print.
func (d *DB) ListFeatureEvidence(ctx context.Context, featureID int64) ([]FeatureEvidence, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT ev.kind, ev.ref_id, ev.created_at,
		       CASE ev.kind
		         WHEN 'document' THEN COALESCE(d.title, '')
		         ELSE COALESCE(r.version, '')
		       END
		  FROM feature_evidence ev
		  LEFT JOIN project_documents d ON ev.kind = 'document'    AND d.id = ev.ref_id
		  LEFT JOIN field_reports     r ON ev.kind = 'field_report' AND r.id = ev.ref_id
		 WHERE ev.feature_id = ?
		 ORDER BY ev.created_at DESC, ev.ref_id DESC`, featureID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FeatureEvidence{}
	for rows.Next() {
		var e FeatureEvidence
		if err := rows.Scan(&e.Kind, &e.RefID, &e.CreatedAt, &e.Title); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
