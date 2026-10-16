package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
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

	// SolutionID is §6.5's subject: the solution a call opens on. NULL for a
	// §6.6 process decision (an emergency-hold confirmation, a charter
	// amendment) and NULL for every call opened before solutions existed, which
	// is why it is nullable rather than defaulted.
	SolutionID *int64 `json:"solution_id,omitempty"`

	// OpenedEarly is §6.5's "Early calls are flagged as such". A pointer rather
	// than a bool so a call that predates the column reads as "not flagged"
	// instead of claiming it was considered and found unnecessary.
	OpenedEarly *bool `json:"opened_early,omitempty"`
	// EarlyReason is why the conditions were waived. Set only when OpenedEarly.
	EarlyReason *string `json:"early_reason,omitempty"`

	// Outcome is §6.5's decision about the solution, distinct from Result,
	// which is §6.6's verdict on the call. A call can pass and still fall back
	// to #2, so conflating them would make the fallback chain unrepresentable.
	Outcome *string `json:"outcome,omitempty"`
	// FallbackToSolutionID is the solution that took over on fall-back.
	FallbackToSolutionID *int64 `json:"fallback_to_solution_id,omitempty"`
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
	// Resolution is the reason a maintainer recorded for the outcome, or -- when the
	// call was accepted over the block -- §6.6's "objector's remedy answered in
	// writing". Empty while the objection is open.
	//
	// It has been written since migration 0001 (ResolveObjection requires it, and this
	// pass's close writes it) and was read by NOTHING: GetObjections' SELECT lists
	// eight columns and this was not one of them, so the field did not exist and the
	// answer could not reach a page. A record no reader selects is write-only.
	Resolution string `json:"resolution"`
}

// ConsensusSummary is what a close returns: the call as it now stands, the counts the
// verdict was computed from, and the verdict.
//
// The json tags are load-bearing and were missing. `json.Unmarshal` matches a Go field
// case-insensitively, so every Go test decoding this into a tagged struct was happy
// while the wire carried `{"Call":…,"Counts":…,"Result":…}` and the browser -- which
// looks up `data.result` -- found nothing. The consensus page reported "Recorded:
// closed" instead of the verdict, and the e2e suite is what caught it.
type ConsensusSummary struct {
	Call   ConsensusCall              `json:"call"`
	Counts governance.ConsensusCounts `json:"counts"`
	Result string                     `json:"result"`
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

// nullInt64Ptr turns a nullable id into a *int64, so JSON emits null rather than
// 0 for "no solution" -- 0 is a real id in these tables and would be a false link.
func nullInt64Ptr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// nullBool turns a nullable integer flag into a *bool.
//
// NULL becomes false rather than nil: opened_early is nullable only because
// migration 0019 added it that way to avoid the integrity_check false positive
// the NOT NULL form triggers (see docs/KNOWN-ISSUES.md). A call with NULL there
// is not flagged, and reporting that as absent would make every pre-0019 call
// look like a flag whose reason was lost.
func nullBool(n sql.NullInt64) *bool {
	v := n.Valid && n.Int64 != 0
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
	var question, description, earlyReason, outcome sql.NullString
	var solID, fbTo, early sql.NullInt64
	err := d.QueryRowContext(ctx, `
		SELECT id, project_id, feature_id, opened_by, opens_at, closes_at,
		       status, result, summary, closed_at, question, description,
		       solution_id, opened_early, early_reason, outcome,
		       fallback_to_solution_id
		FROM consensus_calls WHERE id=?`, id).Scan(
		&c.ID, &c.ProjectID, &fid, &opener, &c.OpensAt, &c.ClosesAt, &c.Status,
		&c.Result, &c.Summary, &closedAt, &question, &description,
		&solID, &early, &earlyReason, &outcome, &fbTo)
	c.FeatureID, c.OpenedBy = fid.Int64, opener.Int64
	c.Question, c.Description = nullString(question), nullString(description)
	c.SolutionID = nullInt64Ptr(solID)
	c.FallbackToSolutionID = nullInt64Ptr(fbTo)
	c.OpenedEarly, c.EarlyReason, c.Outcome = nullBool(early), nullString(earlyReason), nullString(outcome)
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
	// COALESCE, not a nullable scan: `resolution` is NULL for every open objection and
	// the page distinguishes "no answer yet" by the field being empty, not by it being
	// absent -- an absent key and an empty one look the same to a client that does not
	// check for null.
	rows, err := d.QueryContext(ctx, `SELECT id, call_id, user_id, principle, violation,
		remedy, status, created_at, COALESCE(resolution, '')
		FROM objections WHERE call_id=? ORDER BY created_at`, callID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var objections []Objection
	for rows.Next() {
		var o Objection
		if err := rows.Scan(&o.ID, &o.CallID, &o.UserID, &o.Principle, &o.Violation,
			&o.Remedy, &o.Status, &o.CreatedAt, &o.Resolution); err != nil {
			return nil, err
		}
		objections = append(objections, o)
	}
	return objections, rows.Err()
}

// objectionResolutionStatuses is the closed set the objections table's CHECK has
// permitted since migration 0001, minus 'open'.
//
// 'open' is in the CHECK because it is the DEFAULT, not because it is an outcome:
// a resolve that could write it would reopen a call's blocker with no record of
// having done so. The rest are the four ways an objection stops standing.
var objectionResolutionStatuses = []string{"withdrawn", "resolved", "overridden", "vetoed"}

// ResolveObjection records how an objection was dealt with.
//
// The status is checked HERE, beside the write, and not only in the handler: the
// endpoint passed whatever the caller sent straight into the UPDATE, so the
// table's CHECK was the thing refusing a junk status -- and a refusal from a
// constraint arrives as a 500 carrying a SQLite message with the schema in it
// ("constraint failed: CHECK constraint failed: status IN (...)").
//
// Two things this closes that the CHECK could not:
//
//   - a 400 that names the permitted set, instead of a 500 that leaks the schema.
//     The client can act on the first and not on the second.
//   - a resolution with no reasoning. §6.6's resolutions are governance records,
//     the way §6.5's outcomes are: the caller has to say what was decided.
//
// The CHECK stays as the backstop it should always have been, and the test for it
// writes the bad row through the store rather than trusting the constraint to be
// reachable.
func (d *DB) ResolveObjection(ctx context.Context, id int64, status, resolution string) error {
	if !slices.Contains(objectionResolutionStatuses, status) {
		return fmt.Errorf("%w: an objection is resolved as one of %s, not %q",
			ErrInvalid, strings.Join(objectionResolutionStatuses, ", "), status)
	}
	if strings.TrimSpace(resolution) == "" {
		return fmt.Errorf("%w: a resolution is recorded with its reason; that is what "+
			"makes it a record rather than a flag", ErrInvalid)
	}
	res, err := d.ExecContext(ctx, `
		UPDATE objections SET status=?, resolution=? WHERE id=?`, status, resolution, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// The handler reads the objection first and 404s, but a caller that does not
		// must not get a 200 for writing nothing.
		return fmt.Errorf("%w: objection %d", ErrNotFound, id)
	}
	return nil
}

// TallyConsensus builds a call's counts without closing it.
//
// Split out of CloseConsensusCall because the counts are needed for an OPEN call:
// the Consensus page has to show quorum progress while the call is still running,
// and before this the only way to get them was to close it. CloseConsensusCall calls
// this too, so the numbers a page shows mid-call and the numbers a close evaluates
// come from one computation rather than two that can drift.
func (d *DB) TallyConsensus(ctx context.Context, c ConsensusCall) (governance.ConsensusCounts, error) {
	positions, err := d.GetPositions(ctx, c.ID)
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
	objections, err := d.GetObjections(ctx, c.ID)
	if err != nil {
		return governance.ConsensusCounts{}, err
	}
	for _, o := range objections {
		if o.Status == "open" {
			counts.OpenObjections++
		}
	}

	// Eligible must be set before EvaluateConsensus, or quorum is vacuous.
	//
	// QuorumThreshold(eligible<=0) returns 0, and EvaluateConsensus's first check is
	// `Participants < QuorumThreshold(...)`, so an unset Eligible makes every call
	// satisfy quorum by definition: one member consenting to their own call was
	// enough to accept it, in a project with three members eligible to vote. Probed
	// with a real fixture (see TestAClosedCallCountsTheEligiblePopulationForQuorum),
	// which is why this was caught: the ratios and every other branch were correct,
	// so no test that asserted on a ratio or a threshold could see it.
	//
	// §8.2's conjunction -- role AND a qualifying contribution inside the activity
	// window -- is what EligibleCollaboratorCount already computes, so the threshold
	// the page shows and the threshold enforced here are the same number.
	eligible, err := d.EligibleCollaboratorCount(ctx, c.ProjectID)
	if err != nil {
		return governance.ConsensusCounts{}, fmt.Errorf("eligible collaborators: %w", err)
	}
	counts.Eligible = eligible
	return counts, nil
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

	counts, err := d.TallyConsensus(ctx, c)
	if err != nil {
		return ConsensusSummary{}, err
	}

	// The project's OWN charter, not DefaultCharter(governance model).
	//
	// The two agree until someone tunes the charter, and the settings page has a
	// charter editor (§6.9). A project that raised its override ratio would have
	// had its calls judged against the default number while the page showed the
	// tuned one -- the rule enforced with a different number than the one published.
	charter, err := d.GetCharterForProject(ctx, c.ProjectID)
	if err != nil {
		return ConsensusSummary{}, err
	}
	// A first-window close never overrides. §6.6's pass conditions end with "No
	// unresolved block", and its override is "a second window" -- so an unresolved
	// block ends this call as blocked, with its objection recorded and standing.
	// CloseOverrideWindow is where a block is answered.
	result := governance.EvaluateConsensus(counts, charter)

	// One transaction, because the verdict and the call's own record are one
	// decision. Half of it committed is the state that made this necessary: a call
	// stored as accepted over a block that is still standing.
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return ConsensusSummary{}, err
	}
	defer func() { _ = tx.Rollback() }()

	now := float64(time.Now().Unix())
	if _, err := tx.ExecContext(ctx,
		`UPDATE consensus_calls SET status='closed', closed_at=?, result=? WHERE id=?`,
		now, string(result), callID); err != nil {
		return ConsensusSummary{}, err
	}
	if err := tx.Commit(); err != nil {
		return ConsensusSummary{}, err
	}

	// Re-read so the summary describes what was written. It used to return the call as
	// it stood BEFORE the close (status "open", result NULL), which is a response that
	// contradicts the write it just performed.
	closed, err := d.GetConsensusCall(ctx, callID)
	if err != nil {
		return ConsensusSummary{}, err
	}
	return ConsensusSummary{Call: closed, Counts: counts, Result: string(result)}, nil
}
