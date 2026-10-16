package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// §6.2's third steering input reads a feature's tags; §4.3 says a tag that does not exist is
// an error naming the proposal route rather than a side effect.
//
// These three functions are the missing writer for `feature_tags`. The table has existed
// since 0001 and every INSERT in the tree was in a test -- the live instance holds 537
// features and 0 rows. So "the live theme weights a feature's tags match" (ThemeWeightTotal)
// had an input nothing could fill, which is the defect one layer under the one this pass
// started from: a mechanism can be inert because its wire is missing, or because its DATA
// is. Both were true here.

// GetFeatureTags returns a feature's tags, alphabetically. The request-tags pair is the
// precedent for the shape: an explicit read, so a client can confirm what it stored.
func (d *DB) GetFeatureTags(ctx context.Context, featureID int64) ([]string, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT t.name FROM feature_tags ft
		JOIN tags t ON t.id = ft.tag_id
		WHERE ft.feature_id = ? ORDER BY t.name`, featureID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// AddFeatureTag attaches an EXISTING tag to a feature.
//
// Additive rather than replacing, unlike SetRequestTags, and the difference is the reason
// this is not a copy of that function: a request's tags ARE its constraints, so removing a
// deal-breaker has to remove it, while a feature's tags are metadata several contributors
// may add to -- the same reasoning (and the same "does not create tags" rule) as
// ApplyExistingProjectTag. Removal is a separate call so the door is not one-way.
func (d *DB) AddFeatureTag(ctx context.Context, projectID, featureID, actorID int64, tag string) error {
	// Trimmed here, LOWERED by globalTagID below: a second ToLower in this function would
	// be a second copy of the normalisation rule, and the mutation run showed it was doing
	// no work -- the lookup normalises, so the mutant that deleted it survived. Spelling a
	// tag once is what stops the two spellings from disagreeing.
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return fmt.Errorf("%w: a tag is required", ErrInvalid)
	}
	f, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return err
	}
	if f.ProjectID != projectID {
		// The same rule as callerComplaint on the HTTP side, enforced at the store too: a
		// global feature id must not be reachable through an unrelated project.
		return fmt.Errorf("%w: feature %d is not in project %d", ErrNotFound, featureID, projectID)
	}
	// The tag's ID comes from globalTagID, which normalises the spelling and resolves
	// aliases, and the row is inserted by ID.
	//
	// It used to `INSERT ... SELECT id FROM tags WHERE name = ?`, re-selecting by the exact
	// name after a ToLower of my own -- and that is the shape of bug a mutation run found:
	// with the ToLower gone, "Security-Hardening" matched no tag row, the SELECT inserted
	// ZERO rows, and the function returned nil. A write that reports success and changes
	// nothing is worse than one that fails, and the fix is to stop spelling the tag twice:
	// one lookup, one normalisation, an ID.
	//
	// Bad REQUEST rather than 404 when the tag does not exist: the feature the URL addresses
	// is right there, and what is wrong is the name in the body.
	tagID, err := d.globalTagID(ctx, tag)
	if err != nil {
		return fmt.Errorf("%w: no such tag -- propose it with POST /api/v1/taxonomy/proposals (action=create)",
			ErrInvalid)
	}
	var who any
	if actorID > 0 {
		who = actorID
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO feature_tags (feature_id, tag_id, applied_by, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(feature_id, tag_id) DO NOTHING`,
		featureID, tagID, who, float64(time.Now().Unix())); err != nil {
		return err
	}
	// The tag was applied, so the project's search index has one more term to carry. The
	// same reindex ApplyExistingProjectTag performs for the same reason.
	if err := d.ReindexProject(ctx, projectID); err != nil {
		return err
	}
	return d.AddAudit(ctx, projectID, actorID, "add_feature_tag", "feature", featureID,
		fmt.Sprintf("tag %q applied", tag))
}

// RemoveFeatureTag detaches a tag from a feature.
//
// Removal is not housekeeping here: a feature's tags decide which ratified themes steer it,
// so a wrong tag silently changes that feature's priority. A mistake that cannot be undone
// would be a steering input nobody can correct.
func (d *DB) RemoveFeatureTag(ctx context.Context, projectID, featureID, actorID int64, tag string) error {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return fmt.Errorf("%w: a tag is required", ErrInvalid)
	}
	f, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return err
	}
	if f.ProjectID != projectID {
		return fmt.Errorf("%w: feature %d is not in project %d", ErrNotFound, featureID, projectID)
	}
	// The same one-lookup rule as AddFeatureTag: resolve the tag through globalTagID (which
	// normalises and follows aliases) instead of re-spelling it in a case-sensitive subquery.
	tagID, tagErr := d.globalTagID(ctx, tag)
	if tagErr != nil {
		// The tag does not exist, so the feature cannot be carrying it: a 404 naming the
		// tag, not a silent success.
		return fmt.Errorf("%w: feature %d carries no tag %q", ErrNotFound, featureID, tag)
	}
	res, err := d.ExecContext(ctx, `
		DELETE FROM feature_tags WHERE feature_id = ? AND tag_id = ?`, featureID, tagID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: feature %d does not carry the tag %q", ErrNotFound, featureID, tag)
	}
	if err := d.ReindexProject(ctx, projectID); err != nil {
		return err
	}
	return d.AddAudit(ctx, projectID, actorID, "remove_feature_tag", "feature", featureID,
		fmt.Sprintf("tag %q removed", tag))
}
