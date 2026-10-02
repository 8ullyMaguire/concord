package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/governance"
)

type ConsensusCall struct {
	ID        int64 `json:"id"`
	ProjectID int64 `json:"project_id"`
	// FeatureID is 0 for a process decision that is not about one feature --
	// an emergency-hold confirmation, a charter amendment. §6.6 calls a
	// Consensus Call "a formal decision on one specific solution", but the
	// process decisions that §6.6 also requires have no solution to attach to,
	// and hanging them off an arbitrary feature would put a false link in the
	// record.
	FeatureID int64    `json:"feature_id"`
	OpenedBy  int64    `json:"opened_by"`
	OpensAt   float64  `json:"opens_at"`
	ClosesAt  float64  `json:"closes_at"`
	Status    string   `json:"status"`
	Result    *string  `json:"result"`
	Summary   *string  `json:"summary"`
	CreatedAt float64  `json:"created_at"`
	ClosedAt  *float64 `json:"closed_at,omitempty"`
	// Question is the decision actually on the table. It used to be passed in
	// and discarded, so every historical call has NULL here; see migration 0014.
	Question    *string `json:"question,omitempty"`
	Description *string `json:"description,omitempty"`
}

type Position struct {
	CallID    int64   `json:"call_id"`
	UserID    int64   `json:"user_id"`
	Position  string  `json:"position"`
	Reason    string  `json:"reason"`
	UpdatedAt float64 `json:"updated_at"`
}

type Objection struct {
	ID        int64   `json:"id"`
	CallID    int64   `json:"call_id"`
	UserID    int64   `json:"user_id"`
	Principle string  `json:"principle"`
	Violation string  `json:"violation"`
	Remedy    string  `json:"remedy"`
	Status    string  `json:"status"`
	CreatedAt float64 `json:"created_at"`
}

type ConsensusSummary struct {
	Call   ConsensusCall
	Counts governance.ConsensusCounts
	Result string
}

// CreateConsensusCall opens a call.
//
// featureID may be 0 for a process decision that is not about one feature (an
// emergency-hold confirmation, a charter amendment). When it is non-zero the
// feature is validated as before: it must exist, be in draft or discussion, and
// have at least one linked complaint.
//
// The previous version hardcoded opened_by to 1 and discarded title and
// description entirely, so every call in the database claimed user 1 opened it
// and none recorded what was being decided.
func (d *DB) CreateConsensusCall(ctx context.Context, projectID, featureID, openedBy int64, title, description string) (ConsensusCall, error) {
	if featureID != 0 {
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
	}
	now := float64(time.Now().Unix())
	// A NULL feature_id, not 0: the column is a foreign key and 0 would be a
	// dangling reference rather than an explicit "no feature".
	var fid any
	if featureID != 0 {
		fid = featureID
	}
	var opener any
	if openedBy > 0 {
		opener = openedBy
	}
	res, err := d.ExecContext(ctx, `
		INSERT INTO consensus_calls
			(project_id, feature_id, opened_by, opens_at, closes_at, status, question, description)
		VALUES (?, ?, ?, ?, ?, 'open', ?, ?)`,
		projectID, fid, opener, now, now, nullIfEmpty(title), nullIfEmpty(description))
	if err != nil {
		return ConsensusCall{}, err
	}
	id, _ := res.LastInsertId()
	return d.GetConsensusCall(ctx, id)
}

// nullIfEmpty maps "" to a SQL NULL, so an empty question reads as "not stated"
// rather than as an empty string that looks answered.
// nullString flattens a scanned nullable string to *string, so JSON emits
// null instead of "" for a value that was never set.
func nullString(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func (d *DB) GetConsensusCall(ctx context.Context, id int64) (ConsensusCall, error) {
	var c ConsensusCall
	// feature_id, opened_by, question and description are all nullable, so each
	// scans through a Null* wrapper: feature_id 0 means "this call is about a
	// decision, not a feature" (§6.6 process decisions), and question NULL means
	// "not stated" rather than an empty string.
	var fid, opener sql.NullInt64
	var closedAt sql.NullFloat64
	var question, description sql.NullString
	err := d.QueryRowContext(ctx, `
		SELECT id, project_id, feature_id, opened_by, opens_at, closes_at,
		       status, result, summary, closed_at, question, description
		FROM consensus_calls WHERE id=?`, id).Scan(
		&c.ID, &c.ProjectID, &fid, &opener, &c.OpensAt, &c.ClosesAt, &c.Status,
		&c.Result, &c.Summary, &closedAt, &question, &description)
	c.FeatureID, c.OpenedBy = fid.Int64, opener.Int64
	c.Question, c.Description = nullString(question), nullString(description)
	if closedAt.Valid {
		t := closedAt.Float64
		c.ClosedAt = &t
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ConsensusCall{}, fmt.Errorf("%w: consensus call %d", ErrNotFound, id)
	}
	return c, err
}

func (d *DB) GetPositions(ctx context.Context, callID int64) ([]Position, error) {
	rows, err := d.QueryContext(ctx, `SELECT call_id, user_id, position, reason, updated_at
		FROM positions WHERE call_id=? ORDER BY updated_at ASC`, callID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var positions []Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.CallID, &p.UserID, &p.Position, &p.Reason, &p.UpdatedAt); err != nil {
			return nil, err
		}
		positions = append(positions, p)
	}
	return positions, rows.Err()
}

func (d *DB) GetPosition(ctx context.Context, callID, userID int64) (Position, error) {
	var p Position
	err := d.QueryRowContext(ctx, `SELECT call_id, user_id, position, reason, updated_at
		FROM positions WHERE call_id=? AND user_id=?`, callID, userID).Scan(
		&p.CallID, &p.UserID, &p.Position, &p.Reason, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Position{}, fmt.Errorf("%w: position not found", ErrNotFound)
	}
	return p, err
}

func (d *DB) CastPosition(ctx context.Context, callID, userID int64, stance string) (Position, error) {
	now := float64(time.Now().Unix())
	_, err := d.ExecContext(ctx, `
		INSERT INTO positions (call_id, user_id, position, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(call_id, user_id) DO UPDATE SET position=excluded.position, updated_at=excluded.updated_at`,
		callID, userID, stance, now)
	if err != nil {
		return Position{}, err
	}
	return d.GetPosition(ctx, callID, userID)
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
	return Objection{ID: id, CallID: callID, UserID: userID, Principle: principle, Violation: violation, Remedy: remedy, Status: "open", CreatedAt: now}, nil
}

func (d *DB) GetObjection(ctx context.Context, id int64) (Objection, error) {
	var o Objection
	err := d.QueryRowContext(ctx, `SELECT id, call_id, user_id, principle, violation, remedy, status, created_at
		FROM objections WHERE id=?`, id).Scan(
		&o.ID, &o.CallID, &o.UserID, &o.Principle, &o.Violation, &o.Remedy, &o.Status, &o.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Objection{}, fmt.Errorf("%w: objection %d", ErrNotFound, id)
	}
	return o, err
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
		if err := rows.Scan(&o.ID, &o.CallID, &o.UserID, &o.Principle, &o.Violation, &o.Remedy, &o.Status, &o.CreatedAt); err != nil {
			return nil, err
		}
		objections = append(objections, o)
	}
	return objections, rows.Err()
}

func (d *DB) ResolveObjection(ctx context.Context, id int64, status, resolution string) error {
	_, err := d.ExecContext(ctx, `
		UPDATE objections SET status=?, resolution=? WHERE id=?`, status, resolution, id)
	return err
}

func (d *DB) CloseConsensusCall(ctx context.Context, callID int64) (ConsensusSummary, error) {
	c, err := d.GetConsensusCall(ctx, callID)
	if err != nil {
		return ConsensusSummary{}, err
	}

	// §6.6: an emergency hold suspends a call, and suspension means the call
	// does not resolve while the hold is in force. This check is the entire
	// substance of the power -- a hold cannot override a block or force a
	// result, it can only pause one, so refusing to close a held call is what
	// stops it from being a veto in disguise.
	held, hold, err := d.CallIsHeld(ctx, callID)
	if err != nil {
		return ConsensusSummary{}, err
	}
	if held {
		return ConsensusSummary{}, fmt.Errorf(
			"%w: consensus call %d is suspended by an emergency hold (%s ground) until %s",
			ErrConflict, callID, hold.Grounds,
			time.Unix(int64(hold.ExpiresAt), 0).UTC().Format(time.RFC3339))
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
		switch p.Position {
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
