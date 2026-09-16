// Package store is Concord's SQL data layer.
// This file adds the pairwise voting domain (PLAN M3).
package store

import (
	"context"
	"fmt"
	"time"
)

// PairwiseVote records a pairwise comparison between two features.
type PairwiseVote struct {
	ID        int64   `json:"id"`
	ProjectID int64   `json:"project_id"`
	VoterID   int64   `json:"voter_id"`
	FeatureA  int64   `json:"feature_a"`
	FeatureB  int64   `json:"feature_b"`
	Outcome   string  `json:"outcome"` // a, b, both, neither, skip
	Weight    float64 `json:"weight"`
	CreatedAt float64 `json:"created_at"`
}

// GetNextPair selects an active-learning pair for voting (M3).
// Picks close ratings + high RD; excludes pairs the voter already voted on;
// falls back to the closest pair.
func (d *DB) GetNextPair(ctx context.Context, projectID, voterID int64) (Feature, Feature, error) {
	// Get all features for the project
	rows, err := d.QueryContext(ctx, `SELECT id, elo_r, elo_rd FROM features WHERE project_id=? AND status IN ('draft','discussion','consensus','ready','in_progress') ORDER BY elo_r`, projectID)
	if err != nil {
		return Feature{}, Feature{}, err
	}
	defer rows.Close()
	var features []struct {
		ID  int64
		ER  float64
		ERD float64
	}
	for rows.Next() {
		var f struct {
			ID      int64
			ER, ERD float64
		}
		if err := rows.Scan(&f.ID, &f.ER, &f.ERD); err == nil {
			features = append(features, f)
		}
	}
	_ = rows.Err()
	if len(features) < 2 {
		return Feature{}, Feature{}, fmt.Errorf("need at least 2 features to vote")
	}

	// Get pairs this voter already voted on
	votedRows, err := d.QueryContext(ctx, `SELECT feature_a, feature_b FROM pairwise_votes WHERE project_id=? AND voter_id=?`, projectID, voterID)
	if err != nil {
		return Feature{}, Feature{}, err
	}
	votedPairs := make(map[string]bool)
	for votedRows.Next() {
		var a, b int64
		if err := votedRows.Scan(&a, &b); err == nil {
			votedPairs[fmt.Sprintf("%d-%d", a, b)] = true
			votedPairs[fmt.Sprintf("%d-%d", b, a)] = true
		}
	}
	_ = votedRows.Close()
	_ = votedRows.Err()

	// Find the closest pair (smallest |rA - rB|) that hasn't been voted on
	var bestA, bestB int64
	bestDiff := -1.0
	for i := 0; i < len(features); i++ {
		for j := i + 1; j < len(features); j++ {
			key := fmt.Sprintf("%d-%d", features[i].ID, features[j].ID)
			if votedPairs[key] {
				continue
			}
			diff := features[i].ER - features[j].ER
			if diff < 0 {
				diff = -diff
			}
			if bestDiff < 0 || diff < bestDiff {
				bestDiff = diff
				bestA = features[i].ID
				bestB = features[j].ID
			}
		}
	}

	if bestA == 0 || bestB == 0 {
		// Fallback: closest pair overall
		bestDiff = -1.0
		for i := 0; i < len(features); i++ {
			for j := i + 1; j < len(features); j++ {
				diff := features[i].ER - features[j].ER
				if diff < 0 {
					diff = -diff
				}
				if bestDiff < 0 || diff < bestDiff {
					bestDiff = diff
					bestA = features[i].ID
					bestB = features[j].ID
				}
			}
		}
	}

	a, err := d.GetFeature(ctx, bestA)
	if err != nil {
		return Feature{}, Feature{}, err
	}
	b, err := d.GetFeature(ctx, bestB)
	if err != nil {
		return Feature{}, Feature{}, err
	}
	return a, b, nil
}

// RecordVote records a pairwise vote outcome (M3).
func (d *DB) RecordVote(ctx context.Context, projectID, voterID, featureA, featureB int64, outcome string, weight float64) (PairwiseVote, error) {
	if outcome == "" {
		return PairwiseVote{}, fmt.Errorf("%w: outcome is required", ErrInvalid)
	}
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT OR REPLACE INTO pairwise_votes (project_id, voter_id, feature_a, feature_b, outcome, weight, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, projectID, voterID, featureA, featureB, outcome, weight, now)
	if err != nil {
		return PairwiseVote{}, err
	}
	id, _ := res.LastInsertId()
	return PairwiseVote{
		ID: id, ProjectID: projectID, VoterID: voterID,
		FeatureA: featureA, FeatureB: featureB,
		Outcome: outcome, Weight: weight, CreatedAt: now,
	}, nil
}

// GetFeatureVotes returns all votes for a feature.
func (d *DB) GetFeatureVotes(ctx context.Context, featureID int64) ([]PairwiseVote, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, project_id, voter_id, feature_a, feature_b, outcome, weight, created_at
		FROM pairwise_votes WHERE feature_a=? OR feature_b=? ORDER BY created_at DESC`, featureID, featureID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var votes []PairwiseVote
	for rows.Next() {
		var v PairwiseVote
		if err := rows.Scan(&v.ID, &v.ProjectID, &v.VoterID, &v.FeatureA, &v.FeatureB,
			&v.Outcome, &v.Weight, &v.CreatedAt); err != nil {
			return nil, err
		}
		votes = append(votes, v)
	}
	return votes, rows.Err()
}
