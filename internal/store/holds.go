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

// The maintainer emergency hold (§6.6).
//
// The previous spec described the veto two ways at once: an override path for
// blocks in §5.5/§6.3, and limited to security and legal in the role
// definition. Read the first way, a maintainer could cancel a community block,
// which contradicts §7 "maintainers hold no steering power" and the admin-light
// principle.
//
// What is implemented here is the narrow reading, because it is the only one
// consistent with the rest of the spec. A hold:
//
//   - suspends a proposal, and does nothing else;
//   - cannot force a result, cannot override a block, cannot close a call;
//   - requires a written reason and a stated ground (security or legal);
//   - expires on its own;
//   - triggers an automatic confirmation vote inside the expiry window.
//
// The strongest thing a maintainer can do is pause something, and the pause
// ends by itself.

// ErrHoldGroundsInvalid is returned for a ground outside security/legal.
var ErrHoldGroundsInvalid = errors.New("emergency hold grounds must be security or legal")

// DefaultHoldDays is the default hold duration when none is requested.
//
// Seven days, matching the default consensus window: long enough to run a
// security response, short enough that a hold cannot quietly become the
// decision.
const DefaultHoldDays = 7

// MaxHoldDays caps a hold. Without a ceiling the emergency power is just an
// indefinite veto with a different name.
const MaxHoldDays = 30

// EmergencyHold is a maintainer's emergency suspension of a proposal.
type EmergencyHold struct {
	ID                 int64   `json:"id"`
	ProjectID          int64   `json:"project_id"`
	CallID             int64   `json:"call_id"`
	HeldBy             int64   `json:"held_by"`
	Reason             string  `json:"reason"`
	Grounds            string  `json:"grounds"`
	CreatedAt          float64 `json:"created_at"`
	ExpiresAt          float64 `json:"expires_at"`
	ConfirmationCallID int64   `json:"confirmation_call_id"`
	ReleasedAt         float64 `json:"released_at"`
	ReleaseReason      string  `json:"release_reason"`

	// Active is true while the hold is in force: not released and not expired.
	// Computed from the timestamps rather than stored, so a hold cannot be left
	// looking active by a row that was never updated.
	Active bool `json:"active"`
}

// setActive recomputes Active from the stored timestamps.
func (h *EmergencyHold) setActive(now float64) {
	h.Active = h.ReleasedAt == 0 && h.ExpiresAt > now
}

// PlaceEmergencyHold suspends a consensus call.
//
// days <= 0 uses DefaultHoldDays; days above MaxHoldDays is refused rather than
// clamped, because silently shortening a hold the maintainer asked for would be
// as surprising as silently extending one.
func (d *DB) PlaceEmergencyHold(ctx context.Context, projectID, callID, userID int64, grounds, reason string, days int) (EmergencyHold, error) {
	grounds = strings.ToLower(strings.TrimSpace(grounds))
	if grounds != "security" && grounds != "legal" {
		return EmergencyHold{}, ErrHoldGroundsInvalid
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		// A hold with no written justification cannot be reviewed by the
		// community, so the accountability mechanism is the reason field.
		return EmergencyHold{}, fmt.Errorf("%w: an emergency hold requires a written reason", ErrInvalid)
	}
	if days <= 0 {
		days = DefaultHoldDays
	}
	if days > MaxHoldDays {
		return EmergencyHold{}, fmt.Errorf("%w: a hold may not exceed %d days", ErrInvalid, MaxHoldDays)
	}
	if _, err := d.GetConsensusCall(ctx, callID); err != nil {
		return EmergencyHold{}, err
	}

	now := float64(time.Now().Unix())
	expires := now + float64(days*86400)
	res, err := d.ExecContext(ctx, `
		INSERT INTO emergency_holds
			(project_id, call_id, held_by, reason, grounds, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		projectID, callID, userID, reason, grounds, now, expires)
	if err != nil {
		return EmergencyHold{}, err
	}
	id, _ := res.LastInsertId()
	// The hold goes in the admin ledger as well as the audit log: §9.3 makes a
	// steward's action on content publicly visible, and a hold is exactly that.
	_ = d.AddAdminLedger(ctx, userID, "emergency_hold",
		fmt.Sprintf("consensus call %d", callID), reason, projectID)
	_ = d.AddAudit(ctx, projectID, userID, "emergency_hold", "consensus_call", callID,
		fmt.Sprintf("%s hold for %d days: %s", grounds, days, reason))
	return d.GetEmergencyHold(ctx, id)
}

// GetEmergencyHold reads one hold.
func (d *DB) GetEmergencyHold(ctx context.Context, id int64) (EmergencyHold, error) {
	var h EmergencyHold
	var confirmation sql.NullInt64
	var released sql.NullFloat64
	var releaseReason sql.NullString
	err := d.QueryRowContext(ctx, `
		SELECT id, project_id, call_id, held_by, reason, grounds, created_at,
		       expires_at, confirmation_call_id, released_at, release_reason
		FROM emergency_holds WHERE id = ?`, id).Scan(
		&h.ID, &h.ProjectID, &h.CallID, &h.HeldBy, &h.Reason, &h.Grounds,
		&h.CreatedAt, &h.ExpiresAt, &confirmation, &released, &releaseReason)
	if errors.Is(err, sql.ErrNoRows) {
		return EmergencyHold{}, fmt.Errorf("%w: hold %d", ErrNotFound, id)
	}
	if err != nil {
		return EmergencyHold{}, err
	}
	h.ConfirmationCallID = confirmation.Int64
	h.ReleasedAt = released.Float64
	h.ReleaseReason = releaseReason.String
	h.setActive(float64(time.Now().Unix()))
	return h, nil
}

// ActiveHoldForCall returns the in-force hold on a call, if any.
//
// A call is suspended only by a hold that is neither released nor expired, so an
// expired hold needs no cleanup job to stop working: it simply stops matching.
func (d *DB) ActiveHoldForCall(ctx context.Context, callID int64) (*EmergencyHold, error) {
	now := float64(time.Now().Unix())
	rows, err := d.QueryContext(ctx, `
		SELECT id, project_id, call_id, held_by, reason, grounds, created_at,
		       expires_at, confirmation_call_id, released_at, release_reason
		FROM emergency_holds
		WHERE call_id = ? AND released_at IS NULL AND expires_at > ?
		ORDER BY expires_at DESC`, callID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h EmergencyHold
		var confirmation sql.NullInt64
		var released sql.NullFloat64
		var releaseReason sql.NullString
		if err := rows.Scan(&h.ID, &h.ProjectID, &h.CallID, &h.HeldBy, &h.Reason,
			&h.Grounds, &h.CreatedAt, &h.ExpiresAt, &confirmation,
			&released, &releaseReason); err != nil {
			return nil, err
		}
		h.ConfirmationCallID = confirmation.Int64
		h.setActive(now)
		return &h, nil
	}
	return nil, rows.Err()
}

// ReleaseEmergencyHold ends a hold early. The reason is recorded because a hold
// lifted without explanation is indistinguishable from one that was never
// justified.
func (d *DB) ReleaseEmergencyHold(ctx context.Context, holdID, userID int64, reason string) error {
	h, err := d.GetEmergencyHold(ctx, holdID)
	if err != nil {
		return err
	}
	if h.ReleasedAt != 0 {
		return fmt.Errorf("%w: hold %d is already released", ErrInvalid, holdID)
	}
	now := float64(time.Now().Unix())
	if _, err := d.ExecContext(ctx, `
		UPDATE emergency_holds SET released_at = ?, release_reason = ?
		WHERE id = ? AND released_at IS NULL`, now, strings.TrimSpace(reason), holdID); err != nil {
		return err
	}
	_ = d.AddAdminLedger(ctx, userID, "release_emergency_hold",
		fmt.Sprintf("consensus call %d", h.CallID), reason, h.ProjectID)
	_ = d.AddAudit(ctx, h.ProjectID, userID, "release_emergency_hold", "consensus_call", h.CallID, reason)
	return nil
}

// ExpireHolds confirms the confirmation vote for holds that have now expired
// without one having been opened.
//
// §6.6 says a hold "triggers an automatic community confirmation vote within N
// days". Without this, the trigger is only a column: a hold could sit expired
// and the community would never be asked. Called from the consensus close path
// so the confirmation happens as part of normal traffic rather than needing a
// scheduler.
func (d *DB) ExpireHolds(ctx context.Context, projectID int64) ([]EmergencyHold, error) {
	now := float64(time.Now().Unix())
	rows, err := d.QueryContext(ctx, `
		SELECT id, project_id, call_id, held_by, reason, grounds, created_at,
		       expires_at, confirmation_call_id, released_at, release_reason
		FROM emergency_holds
		WHERE project_id = ? AND confirmation_call_id IS NULL AND expires_at <= ?`,
		projectID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var expired []EmergencyHold
	for rows.Next() {
		var h EmergencyHold
		var confirmation sql.NullInt64
		var released sql.NullFloat64
		var releaseReason sql.NullString
		if err := rows.Scan(&h.ID, &h.ProjectID, &h.CallID, &h.HeldBy, &h.Reason,
			&h.Grounds, &h.CreatedAt, &h.ExpiresAt, &confirmation,
			&released, &releaseReason); err != nil {
			return nil, err
		}
		expired = append(expired, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range expired {
		h := &expired[i]
		call, err := d.openHoldConfirmation(ctx, *h)
		if err != nil {
			// A failure to open the confirmation must not abort the rest: the
			// hold stays unconfirmed and will be retried on the next pass.
			continue
		}
		if _, err := d.ExecContext(ctx,
			`UPDATE emergency_holds SET confirmation_call_id = ? WHERE id = ?`,
			call.ID, h.ID); err != nil {
			continue
		}
		h.ConfirmationCallID = call.ID
	}
	return expired, nil
}

// openHoldConfirmation opens the confirmation vote for an expired hold.
func (d *DB) openHoldConfirmation(ctx context.Context, h EmergencyHold) (ConsensusCall, error) {
	title := "Emergency hold expired: continue with this proposal?"
	description := fmt.Sprintf(
		"A maintainer placed an emergency hold on consensus call %d (%s ground), "+
			"stating: %s. The hold has expired without the community being asked, "+
			"so this call exists to put the decision back to the collaborators "+
			"rather than leaving it with whoever placed the hold. §6.6.",
		h.CallID, h.Grounds, h.Reason)
	// featureID 0: a hold applies to a call, not to a feature, and the
	// confirmation call has no feature of its own to attach to. openedBy is the
	// maintainer who placed the hold, so the confirmation's opener names who
	// the community is being asked to review rather than defaulting to user 1.
	return d.CreateConsensusCall(ctx, h.ProjectID, 0, h.HeldBy, title, description)
}

// CallIsHeld reports whether a consensus call is suspended by an emergency hold.
// The close path consults this so a held call cannot be closed: that is the
// whole substance of the power, and everything else about it is bookkeeping.
func (d *DB) CallIsHeld(ctx context.Context, callID int64) (bool, *EmergencyHold, error) {
	h, err := d.ActiveHoldForCall(ctx, callID)
	if err != nil {
		return false, nil, err
	}
	return h != nil, h, nil
}

// AdminLedgerEntry is one publicly visible steward or admin action (§9.3).
type AdminLedgerEntry struct {
	ID            int64   `json:"id"`
	ActorID       int64   `json:"actor_id"`
	Action        string  `json:"action"`
	Subject       string  `json:"subject"`
	Justification string  `json:"justification"`
	ProjectID     int64   `json:"project_id"`
	CreatedAt     float64 `json:"created_at"`
}

// AddAdminLedger appends to the public ledger. It writes to a table whose
// UPDATE and DELETE triggers refuse, so an entry cannot be quietly removed.
func (d *DB) AddAdminLedger(ctx context.Context, actorID int64, action, subject, justification string, projectID int64) error {
	if strings.TrimSpace(action) == "" {
		return fmt.Errorf("%w: an admin ledger entry requires an action", ErrInvalid)
	}
	now := float64(time.Now().Unix())
	var pid any
	if projectID > 0 {
		pid = projectID
	}
	_, err := d.ExecContext(ctx, `
		INSERT INTO admin_ledger
			(actor_id, action, subject, justification, project_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		actorID, action, subject, justification, pid, now)
	return err
}

// ListAdminLedger returns the most recent entries, instance-wide.
//
// No project filter and no permission gate: §9.3 requires this to be public
// precisely because an admin holding no content authority is only a real
// constraint when anyone can watch. That is also why the table refuses UPDATE
// and DELETE.
func (d *DB) ListAdminLedger(ctx context.Context, limit int) ([]AdminLedgerEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.QueryContext(ctx, `
		SELECT id, actor_id, action, subject, justification, project_id, created_at
		FROM admin_ledger ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminLedgerEntry
	for rows.Next() {
		var e AdminLedgerEntry
		var actor sql.NullInt64
		var project sql.NullInt64
		var just sql.NullString
		if err := rows.Scan(&e.ID, &actor, &e.Action, &e.Subject, &just,
			&project, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.ActorID = actor.Int64
		e.ProjectID = project.Int64
		e.Justification = just.String
		out = append(out, e)
	}
	return out, rows.Err()
}

// HoldSuspendsResult reports whether a hold prevents a call from resolving.
//
// Kept as an explicit predicate rather than an inline check so the rule reads in
// one place: a hold suspends, and suspension means the call does not resolve
// while the hold is in force.
func HoldSuspendsResult(h *EmergencyHold) bool {
	return h != nil && h.Active
}

// ConsensusThresholdsSummary reports both ratios for a call, so a client can
// show the two measures §6.6 requires rather than one blended number.
//
// Rendering a single ratio would show the bug this change fixed: a stand-aside
// counted against consent reads as a vote against the proposal.
func ConsensusThresholdsSummary(cc governance.ConsensusCounts, c governance.Charter) map[string]any {
	return map[string]any{
		"consent":           cc.Consent,
		"abstain":           cc.Abstain,
		"stand_aside":       cc.StandAside,
		"block":             cc.Block,
		"participants":      cc.Participants,
		"eligible":          cc.Eligible,
		"quorum_required":   governance.QuorumThreshold(cc.Eligible, c),
		"support_ratio":     cc.SupportRatio(),
		"decisive_ratio":    cc.DecisiveRatio(),
		"support_required":  c.SupportRatioMin,
		"decisive_required": c.ConsentRatio,
		"reluctant":         cc.Reluctant(),
		"open_objections":   cc.OpenObjections,
	}
}
