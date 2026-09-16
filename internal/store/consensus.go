package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/governance"
)

type ConsensusCall struct {
	ID        int64   `json:"id"`
	ProjectID int64   `json:"project_id"`
	FeatureID int64   `json:"feature_id"`
	OpenedBy  int64   `json:"opened_by"`
	OpensAt   float64 `json:"opens_at"`
	ClosesAt  float64 `json:"closes_at"`
	Status    string  `json:"status"`
	Result    *string  `json:"result"`
	Summary   *string  `json:"summary"`
	CreatedAt   float64  `json:"created_at"`
	ClosedAt    *float64 `json:"closed_at,omitempty"`
}

type Position struct {
	ID        int64   `json:"id"`
	CallID    int64   `json:"call_id"`
	UserID    int64   `json:"user_id"`
	Role      string  `json:"role"`
	Stance    string  `json:"stance"`
	Principle string  `json:"principle"`
	CreatedAt float64 `json:"created_at"`
}

type Objection struct {
	ID        int64   `json:"id"`
	CallID    int64   `json:"call_id"`
	UserID    int64   `json:"user_id"`
	Principle string  `json:"principle"`
	Violation string  `json:"violation"`
	Remy      string  `json:"remedy"`
	Status    string  `json:"status"`
	CreatedAt float64 `json:"created_at"`
}

type ConsensusSummary struct {
	Call   ConsensusCall
	Counts governance.ConsensusCounts
	Result string
}

func (d *DB) CreateConsensusCall(ctx context.Context, projectID, featureID int64, title, description string) (ConsensusCall, error) {
	f, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return ConsensusCall{}, err
	}
	if f.Status != "draft" && f.Status != "discussion" {
		return ConsensusCall{}, fmt.Errorf("%w: feature must be draft or discussion", ErrInvalid)
	}
	complaints, err := d.GetFeatureComplaints(ctx, featureID)
	if err != nil || len(complaints) == 0 {
		return ConsensusCall{}, fmt.Errorf("%w: feature must have at least one linked complaint", ErrInvalid)
	}
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO consensus_calls (project_id, feature_id, opened_by, opens_at, closes_at, status)
		VALUES (?, ?, 1, ?, ?, 'open')`, projectID, featureID, now, now)
	if err != nil {
		return ConsensusCall{}, err
	}
	id, _ := res.LastInsertId()
	return d.GetConsensusCall(ctx, id)
}

func (d *DB) GetConsensusCall(ctx context.Context, id int64) (ConsensusCall, error) {
	var c ConsensusCall
	err := d.QueryRowContext(ctx, `SELECT id, project_id, feature_id, opened_by, opens_at, closes_at, status, result, summary FROM consensus_calls WHERE id=?`, id).Scan(
		&c.ID, &c.ProjectID, &c.FeatureID, &c.OpenedBy, &c.OpensAt, &c.ClosesAt, &c.Status, &c.Result, &c.Summary)
	if errors.Is(err, sql.ErrNoRows) {
		return ConsensusCall{}, fmt.Errorf("%w: consensus call %d", ErrNotFound, id)
	}
	return c, err
}

func (d *DB) GetPositions(ctx context.Context, callID int64) ([]Position, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, call_id, user_id, role, stance, principle, created_at
		FROM positions WHERE call_id=? ORDER BY created_at ASC`, callID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var positions []Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.ID, &p.CallID, &p.UserID, &p.Role, &p.Stance, &p.Principle, &p.CreatedAt); err != nil {
			return nil, err
		}
		positions = append(positions, p)
	}
	return positions, rows.Err()
}

func (d *DB) CastPosition(ctx context.Context, callID, userID int64, role, stance string) (Position, error) {
	now := float64(time.Now().Unix())
	_, err := d.ExecContext(ctx, `
		INSERT INTO positions (call_id, user_id, role, stance, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(call_id, user_id) DO UPDATE SET stance=excluded.stance, role=excluded.role, created_at=excluded.created_at`,
		callID, userID, role, stance, now)
	if err != nil {
		return Position{}, err
	}
	return d.GetPosition(ctx, callID, userID)
}

func (d *DB) GetPosition(ctx context.Context, callID, userID int64) (Position, error) {
	var p Position
	err := d.QueryRowContext(ctx, `SELECT id, call_id, user_id, role, stance, principle, created_at
		FROM positions WHERE call_id=? AND user_id=?`, callID, userID).Scan(
		&p.ID, &p.CallID, &p.UserID, &p.Role, &p.Stance, &p.Principle, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Position{}, fmt.Errorf("%w: position not found", ErrNotFound)
	}
	return p, err
}

func (d *DB) CreateObjection(ctx context.Context, callID, userID int64, principle, violation, remedy string) (Objection, error) {
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO objections (call_id, user_id, principle, violation, remedy, status, created_at)
		VALUES (?, ?, ?, ?, ?, 'open', ?)`, callID, userID, principle, violation, remedy, now)
	if err != nil {
		return Objection{}, err
	}
	id, _ := res.LastInsertId()
	return Objection{ID: id, CallID: callID, UserID: userID, Principle: principle, Violation: violation, Remy: remedy, Status: "open", CreatedAt: now}, nil
}

func (d *DB) GetObjections(ctx context.Context, callID int64) ([]Objection, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, call_id, user_id, principle, violation, remedy, status, created_at
		FROM objections WHERE call_id=? ORDER BY created_at`, callID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var objections []Objection
	for rows.Next() {
		var o Objection
		if err := rows.Scan(&o.ID, &o.CallID, &o.UserID, &o.Principle, &o.Violation, &o.Remy, &o.Status, &o.CreatedAt); err != nil {
			return nil, err
		}
		objections = append(objections, o)
	}
	return objections, rows.Err()
}

func (d *DB) CloseConsensusCall(ctx context.Context, callID int64) (ConsensusSummary, error) {
	c, err := d.GetConsensusCall(ctx, callID)
	if err != nil {
		return ConsensusSummary{}, err
	}
	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `UPDATE consensus_calls SET status='closed', closed_at=? WHERE id=?`, now, callID)
	if err != nil {
		return ConsensusSummary{}, err
	}
	positions, err := d.GetPositions(ctx, callID)
	if err != nil {
		return ConsensusSummary{}, err
	}
	
	
		var counts governance.ConsensusCounts
	for _, p := range positions {
		counts.Participants++
		switch p.Stance {
		case "consent":
			counts.Consent++
		case "stand_aside":
			counts.StandAside++
		case "block":
			counts.Block++
		case "abstain":
			counts.Abstain++
		}
	}
	objections, err := d.GetObjections(ctx, callID)
	if err != nil {
		return ConsensusSummary{}, err
	}
	openObj := 0
	for _, o := range objections {
		if o.Status == "open" {
			openObj++
		}
	}
	counts.OpenObjections = openObj
	// Get project's governance model to use the correct charter
	var modelStr string
	err = d.QueryRowContext(ctx,
		`SELECT governance_model FROM projects WHERE id = ?`, c.ProjectID).Scan(&modelStr)
	if err != nil {
		return ConsensusSummary{}, fmt.Errorf("get project governance model: %w", err)
	}
	gm := governance.GovernanceModel(modelStr)
	if !gm.Valid() {
		gm = governance.Collective
	}
	result := governance.EvaluateConsensus(counts, governance.DefaultCharter(gm))
	return ConsensusSummary{Call: c, Counts: counts, Result: string(result)}, nil
}
