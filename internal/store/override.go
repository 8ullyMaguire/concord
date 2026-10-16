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

// OverrideWindow is §6.6's second window: "a second window where >= 80% of
// non-abstain participants vote to override, with the objector's remedy answered
// in writing". It is a ballot of its own -- its own voters, its own window, its
// own verdict -- opened on a call that closed blocked.
type OverrideWindow struct {
	ID       int64    `json:"id"`
	CallID   int64    `json:"call_id"`
	OpenedBy int64    `json:"opened_by"`
	OpenedAt float64  `json:"opened_at"`
	ClosesAt float64  `json:"closes_at"`
	Status   string   `json:"status"`
	ClosedAt *float64 `json:"closed_at,omitempty"`
	// Result is "overridden" or "upheld" once closed: whether the block stood.
	// Nullable rather than defaulted, because "" and "upheld" are different facts
	// about a window that has not closed.
	Result *string `json:"result,omitempty"`
	// Note is §6.6's "objector's remedy answered in writing". Required when the
	// window carries, kept as the record of the decision when it does not.
	Note string `json:"note"`

	// The tally. Not columns, and filled by GetOverrideWindow.
	//
	// Participants/quorum are visible while the window is open (§6.6 keeps
	// "participation progress and the quorum bar" visible so people can see a
	// window they need to vote in); the four counts are not, for the reason the
	// call's tally is hidden -- a visible running count is a bandwagon.
	Participants int `json:"participants"`
	Eligible     int `json:"eligible"`
	QuorumNeeded int `json:"quorum_needed"`
	Consent      int `json:"consent"`
	StandAside   int `json:"stand_aside"`
	Block        int `json:"block"`
	Abstain      int `json:"abstain"`

	// MyPosition is the reader's own stance in this window ("" if none, or if the
	// reader is anonymous). Filled by the handler, which is where the actor is
	// known -- the page needs it to show which button is the reader's.
	MyPosition string `json:"my_position"`
}

// OverridePosition is one voter's stance in the second window.
type OverridePosition struct {
	WindowID  int64   `json:"window_id"`
	UserID    int64   `json:"user_id"`
	Position  string  `json:"position"`
	Reason    string  `json:"reason"`
	UpdatedAt float64 `json:"updated_at"`
}

// TallyOverride counts the second window's positions and sets the population the
// quorum rule is measured against.
//
// OpenObjections is deliberately left at zero: a window has no objections of its
// own. The objections exist on the call, and counting them here would make the
// override rule depend on the thing it is deciding about.
func (d *DB) TallyOverride(ctx context.Context, w OverrideWindow) (governance.ConsensusCounts, error) {
	positions, err := d.GetOverridePositions(ctx, w.ID)
	if err != nil {
		return governance.ConsensusCounts{}, err
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
	// Eligible must be set before EvaluateOverride or quorum is vacuous -- the
	// same trap TallyConsensus documents: QuorumThreshold(eligible<=0) is 0, and
	// an unset population makes every tally satisfy quorum by definition.
	projectID, err := d.projectIDOfCall(ctx, w.CallID)
	if err != nil {
		return governance.ConsensusCounts{}, err
	}
	eligible, err := d.EligibleCollaboratorCount(ctx, projectID)
	if err != nil {
		return governance.ConsensusCounts{}, fmt.Errorf("eligible collaborators: %w", err)
	}
	counts.Eligible = eligible
	return counts, nil
}

// projectIDOfCall reads a call's project. The window's own row does not carry it,
// because the call does and a second copy is a second thing to keep true.
//
// It returns the error rather than a zero id on purpose: project 0 would read the
// default charter and count no eligible collaborators, and an eligible count of 0
// makes the quorum threshold 0 -- the vacuous-quorum trap TallyConsensus
// documents. A swallowed error here would be a silent 100%-of-nobody override.
func (d *DB) projectIDOfCall(ctx context.Context, callID int64) (int64, error) {
	var projectID int64
	err := d.QueryRowContext(ctx,
		`SELECT project_id FROM consensus_calls WHERE id = ?`, callID).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: consensus call %d not found", ErrNotFound, callID)
	}
	return projectID, err
}

// OpenOverrideWindow opens §6.6's second window on a call that closed blocked.
//
// Three refusals, each naming what is wrong rather than defaulting to an empty
// window:
//
//   - the call has not closed blocked. An override needs a recorded block to
//     override; before the seventh pass a close could leave `result` NULL, and
//     there would have been nothing to open a window on.
//   - no objection is open. The window's whole subject is a standing block; if
//     the objector withdrew or converted in the meantime, §6.6's other branch
//     ("the blocker withdraws or converts") has already resolved it and a vote
//     would decide nothing.
//   - a window already exists. A second window is A window: repeating the ballot
//     until the room yields is not an override, it is attrition.
func (d *DB) OpenOverrideWindow(ctx context.Context, callID, openedBy int64) (OverrideWindow, error) {
	call, err := d.GetConsensusCall(ctx, callID)
	if err != nil {
		return OverrideWindow{}, err
	}
	if call.Status != "closed" || call.Result == nil || *call.Result != string(governance.ResultBlocked) {
		return OverrideWindow{}, fmt.Errorf(
			"%w: §6.6's override window opens on a call that closed blocked; call %d is %s with result %s",
			ErrInvalid, callID, call.Status, resultOrNull(call.Result))
	}
	objections, err := d.GetObjections(ctx, callID)
	if err != nil {
		return OverrideWindow{}, err
	}
	open := 0
	for _, o := range objections {
		if o.Status == "open" {
			open++
		}
	}
	if open == 0 {
		return OverrideWindow{}, fmt.Errorf(
			"%w: call %d closed blocked but no objection is open, so there is no block to override",
			ErrInvalid, callID)
	}

	charter, err := d.GetCharterForProject(ctx, call.ProjectID)
	if err != nil {
		return OverrideWindow{}, err
	}
	now := float64(time.Now().Unix())
	closes := now + charter.VoteWindowDays*24*3600
	res, err := d.ExecContext(ctx, `
		INSERT INTO override_windows (call_id, opened_by, opened_at, closes_at, status, note)
		VALUES (?, ?, ?, ?, 'open', '')`, callID, openedBy, now, closes)
	if err != nil {
		// The UNIQUE(call_id) constraint is the "already has a window" refusal.
		// Distinguishing it from other write errors matters: a 409 that says
		// "already has a window" is actionable, and a generic 500 is not.
		if isUniqueViolation(err) {
			return OverrideWindow{}, fmt.Errorf("%w: call %d already has an override window", ErrConflict, callID)
		}
		return OverrideWindow{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return OverrideWindow{}, err
	}
	return d.GetOverrideWindowByID(ctx, id)
}

// GetOverrideWindow reads the window a call has, if it has one.
func (d *DB) GetOverrideWindow(ctx context.Context, callID int64) (OverrideWindow, error) {
	var w OverrideWindow
	var closedAt sql.NullFloat64
	var result, note sql.NullString
	err := d.QueryRowContext(ctx, `
		SELECT id, call_id, opened_by, opened_at, closes_at, status, closed_at, result, note
		FROM override_windows WHERE call_id = ?`, callID).Scan(
		&w.ID, &w.CallID, &w.OpenedBy, &w.OpenedAt, &w.ClosesAt, &w.Status,
		&closedAt, &result, &note)
	if errors.Is(err, sql.ErrNoRows) {
		return OverrideWindow{}, fmt.Errorf("%w: call %d has no override window", ErrNotFound, callID)
	}
	if err != nil {
		return OverrideWindow{}, err
	}
	if closedAt.Valid {
		w.ClosedAt = &closedAt.Float64
	}
	if result.Valid {
		w.Result = &result.String
	}
	w.Note = note.String
	return d.fillOverrideTally(ctx, w)
}

// GetOverrideWindowByID is GetOverrideWindow for a caller that has the row id.
func (d *DB) GetOverrideWindowByID(ctx context.Context, id int64) (OverrideWindow, error) {
	var callID int64
	if err := d.QueryRowContext(ctx,
		`SELECT call_id FROM override_windows WHERE id = ?`, id).Scan(&callID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return OverrideWindow{}, fmt.Errorf("%w: override window %d not found", ErrNotFound, id)
		}
		return OverrideWindow{}, err
	}
	return d.GetOverrideWindow(ctx, callID)
}

// fillOverrideTally attaches the counts, honouring §6.6's hidden tally: a running
// count is visible only to the window that has closed.
//
// This lives in the reader rather than in the handler so that no caller can
// accidentally publish a live tally by forgetting to strip it -- the mistake
// ConsensusSummary's missing json tags made in the other direction.
func (d *DB) fillOverrideTally(ctx context.Context, w OverrideWindow) (OverrideWindow, error) {
	projectID, err := d.projectIDOfCall(ctx, w.CallID)
	if err != nil {
		return OverrideWindow{}, err
	}
	charter, err := d.GetCharterForProject(ctx, projectID)
	if err != nil {
		return OverrideWindow{}, err
	}
	counts, err := d.TallyOverride(ctx, w)
	if err != nil {
		return OverrideWindow{}, err
	}
	w.Participants = counts.Participants
	w.Eligible = counts.Eligible
	w.QuorumNeeded = governance.QuorumThreshold(counts.Eligible, charter)
	if w.Status == "closed" {
		w.Consent = counts.Consent
		w.StandAside = counts.StandAside
		w.Block = counts.Block
		w.Abstain = counts.Abstain
	}
	return w, nil
}

func (d *DB) GetOverridePositions(ctx context.Context, windowID int64) ([]OverridePosition, error) {
	rows, err := d.QueryContext(ctx, `SELECT window_id, user_id, position, reason, updated_at
		FROM override_positions WHERE window_id=? ORDER BY updated_at ASC`, windowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var positions []OverridePosition
	for rows.Next() {
		var p OverridePosition
		if err := rows.Scan(&p.WindowID, &p.UserID, &p.Position, &p.Reason, &p.UpdatedAt); err != nil {
			return nil, err
		}
		positions = append(positions, p)
	}
	return positions, rows.Err()
}

func (d *DB) GetOverridePosition(ctx context.Context, windowID, userID int64) (OverridePosition, error) {
	var p OverridePosition
	err := d.QueryRowContext(ctx, `SELECT window_id, user_id, position, reason, updated_at
		FROM override_positions WHERE window_id=? AND user_id=?`, windowID, userID).Scan(
		&p.WindowID, &p.UserID, &p.Position, &p.Reason, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return OverridePosition{}, fmt.Errorf("%w: no stance in window %d", ErrNotFound, windowID)
	}
	return p, err
}

// CastOverridePosition records a stance in the second window.
//
// Refused once the window has closed, unlike the first window's positions: a vote
// recorded after the verdict would silently disagree with the verdict it did not
// influence, and the ratio is what decided.
func (d *DB) CastOverridePosition(ctx context.Context, windowID, userID int64, stance, reason string) (OverridePosition, error) {
	var status string
	if err := d.QueryRowContext(ctx,
		`SELECT status FROM override_windows WHERE id = ?`, windowID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return OverridePosition{}, fmt.Errorf("%w: override window %d not found", ErrNotFound, windowID)
		}
		return OverridePosition{}, err
	}
	if status != "open" {
		return OverridePosition{}, fmt.Errorf(
			"%w: §6.6's override window is closed; a stance recorded now would not have counted in the ratio that decided it",
			ErrConflict)
	}
	now := float64(time.Now().Unix())
	_, err := d.ExecContext(ctx, `
		INSERT INTO override_positions (window_id, user_id, position, reason, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(window_id, user_id) DO UPDATE SET
			position=excluded.position, reason=excluded.reason, updated_at=excluded.updated_at`,
		windowID, userID, stance, strings.TrimSpace(reason), now)
	if err != nil {
		return OverridePosition{}, err
	}
	return d.GetOverridePosition(ctx, windowID, userID)
}

// CloseOverrideWindow records the second window's verdict.
//
//	carries = governance.EvaluateOverride(counts, charter) == overridden
//
// When it carries, §6.6 requires the objector's remedy answered in writing, and the
// answer, the window's verdict, the call's new result and the objection's new
// status are ONE transaction: a call stored as accepted over a block that is still
// standing is exactly the inconsistent state the seventh pass removed from the
// first window, and there is no reason to reintroduce it here.
//
// When it does not carry the call is left exactly as it was -- blocked, with its
// objection open -- and the window records `upheld`. A block that stood is not
// quietly cleared by having been voted on.
func (d *DB) CloseOverrideWindow(ctx context.Context, callID int64, note string) (OverrideWindow, error) {
	w, err := d.GetOverrideWindow(ctx, callID)
	if err != nil {
		return OverrideWindow{}, err
	}
	if w.Status != "open" {
		return OverrideWindow{}, fmt.Errorf(
			"%w: the override window on call %d is already %s", ErrConflict, callID, w.Status)
	}
	call, err := d.GetConsensusCall(ctx, callID)
	if err != nil {
		return OverrideWindow{}, err
	}
	// §6.6: an emergency hold "suspends a call", and suspension means the call does
	// not resolve while the hold is in force. Overriding a block resolves the call --
	// so without this check the hold would be bypassable by a route it never heard
	// of, and the power would be a pause you can vote around instead of a pause.
	held, hold, err := d.CallIsHeld(ctx, callID)
	if err != nil {
		return OverrideWindow{}, err
	}
	if held {
		return OverrideWindow{}, fmt.Errorf(
			"%w: consensus call %d is suspended by an emergency hold (%s ground) until %s, so its override window cannot resolve it",
			ErrConflict, callID, hold.Grounds,
			time.Unix(int64(hold.ExpiresAt), 0).UTC().Format(time.RFC3339))
	}
	charter, err := d.GetCharterForProject(ctx, call.ProjectID)
	if err != nil {
		return OverrideWindow{}, err
	}
	counts, err := d.TallyOverride(ctx, w)
	if err != nil {
		return OverrideWindow{}, err
	}
	verdict := governance.EvaluateOverride(counts, charter)
	if verdict == governance.ResultInsufficientQuorum {
		// Below quorum there is no ratio to read, and recording "upheld" would
		// claim the room decided against an override when most of the room did
		// not vote at all. §6.6 lets a window lapse instead.
		return OverrideWindow{}, fmt.Errorf(
			"%w: §6.6's override window needs %d participants and %d have voted, so there is no verdict to record",
			ErrInvalid, governance.QuorumThreshold(counts.Eligible, charter), counts.Participants)
	}
	trimmed := strings.TrimSpace(note)
	if verdict == governance.ResultOverridden && trimmed == "" {
		// Checked here, before any write, because the verdict is what makes the
		// answer required -- the same reason the close's note lived in the store.
		return OverrideWindow{}, fmt.Errorf(
			"%w: §6.6 overrides a block only with \"the objector's remedy answered in writing\"; "+
				"this window carries with %d of %d non-abstain participants consenting, so the override needs that answer",
			ErrInvalid, counts.Consent, counts.Consent+counts.StandAside+counts.Block)
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return OverrideWindow{}, err
	}
	defer func() { _ = tx.Rollback() }()

	now := float64(time.Now().Unix())
	if _, err := tx.ExecContext(ctx,
		`UPDATE override_windows SET status='closed', closed_at=?, result=?, note=? WHERE id=?`,
		now, string(verdict), trimmed, w.ID); err != nil {
		return OverrideWindow{}, err
	}
	if verdict == governance.ResultOverridden {
		if _, err := tx.ExecContext(ctx,
			`UPDATE consensus_calls SET result='accepted_overridden' WHERE id=?`, callID); err != nil {
			return OverrideWindow{}, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE objections SET status='overridden', resolution=? WHERE call_id=? AND status='open'`,
			trimmed, callID); err != nil {
			return OverrideWindow{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return OverrideWindow{}, err
	}
	return d.GetOverrideWindow(ctx, callID)
}

// resultOrNull renders a nullable verdict for a refusal message.
func resultOrNull(r *string) string {
	if r == nil {
		return "no result recorded"
	}
	if *r == "" {
		return "an empty result"
	}
	return fmt.Sprintf("%q", *r)
}
