package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Guest interactions, consent, and folding.
//
// See docs/specs/guest-interactions-spec.md. The four facts this file is built on
// are all properties of the existing schema rather than new invention:
//
//   - A guest is a real users row (is_guest = 1, password_hash = ''). Everything a
//     guest does is already a row owned by a users.id, so a guest identity means
//     every existing write path works unchanged and the fold is an UPDATE.
//   - A guest cannot log in: password_hash is empty, which 0003 and Login already
//     treat as "no credential" rather than as a match on "".
//   - MAU comes from audit_log, which every write path already passes through.
//   - Consent is recorded against the identity, because the cookie is only the
//     gate the request path reads.

// mauWindowDays is the "M" in MAU, as a constant rather than a setting: it is the
// definition of the word, not a policy knob. The configurable number is the limit
// (CONCORD_GUEST_MAU_LIMIT, in internal/httpapi).
const mauWindowDays = 30

// GuestStatus is what the notice, the page state and the operator surface read.
type GuestStatus struct {
	// Allowed is false when the limit is reached or guests are switched off.
	Allowed bool `json:"allowed"`
	// MAU is distinct accounts with a recorded action inside the window.
	MAU int `json:"mau"`
	// Limit is the configured threshold; 0 means guest interactions are off.
	Limit int `json:"limit"`
	// WindowDays is the window MAU was counted over, reported so the number on a
	// page can say what it means without a second source for it.
	WindowDays int `json:"window_days"`
	// Guests is how many guest identities exist, and StaleGuests how many were
	// created more than WindowDays ago and never folded. The second number is the
	// operator's input to a retention decision this pass deliberately does not
	// make on their behalf.
	Guests      int `json:"guests"`
	StaleGuests int `json:"stale_guests"`
}

// MonthlyActiveUsers counts distinct accounts with a recorded action inside the
// window.
//
// Guests are excluded on purpose: the limit says "this instance is big enough to
// ask people to sign up", and counting the guests themselves would let the gate
// satisfy itself -- guests would be the reason guests are still allowed.
//
// audit_log is the source because every write path in the product already passes
// through it (58 call sites, and AddAudit is the only way anything is recorded).
// Union'ing the ~48 activity columns instead would look more complete and be
// worse: that list grows with every migration, and the first time someone adds a
// write path without updating it, the count is silently low and the door opens
// later than the operator asked for.
func (d *DB) MonthlyActiveUsers(ctx context.Context) (int, error) {
	since := float64(time.Now().AddDate(0, 0, -mauWindowDays).Unix())
	var n int
	err := d.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT a.actor_id)
		FROM audit_log a
		JOIN users u ON u.id = a.actor_id
		WHERE a.actor_id IS NOT NULL AND a.actor_id > 0
		  AND a.created_at >= ? AND u.is_guest = 0`, since).Scan(&n)
	return n, err
}

// GuestStatusFor assembles the status against a configured limit. A limit <= 0
// means guests are switched off, which is the same answer as "the limit is
// reached" and deliberately not a separate state: an operator who sets 0 wants
// the door shut, and a second flag for that is a second thing to get wrong.
func (d *DB) GuestStatusFor(ctx context.Context, limit int) (GuestStatus, error) {
	mau, err := d.MonthlyActiveUsers(ctx)
	if err != nil {
		return GuestStatus{}, err
	}
	st := GuestStatus{
		MAU:        mau,
		Limit:      limit,
		WindowDays: mauWindowDays,
		Allowed:    limit > 0 && mau < limit,
	}
	since := float64(time.Now().AddDate(0, 0, -mauWindowDays).Unix())
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE is_guest = 1`).Scan(&st.Guests); err != nil {
		return GuestStatus{}, err
	}
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE is_guest = 1 AND created_at < ?`, since).Scan(&st.StaleGuests); err != nil {
		return GuestStatus{}, err
	}
	return st, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read entropy: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// CreateGuest makes an identity that can act but cannot log in, and returns it
// with a token exactly like RegisterUser does. The token is the identity's only
// credential and is returned once; the caller must not store it anywhere the
// browser can be tricked into sending it (see the spec's D1: identity is a
// bearer token, never a cookie).
func (d *DB) CreateGuest(ctx context.Context) (User, string, error) {
	for attempt := 0; attempt < 5; attempt++ {
		suffix, err := randomHex(4)
		if err != nil {
			return User{}, "", err
		}
		now := float64(time.Now().Unix())
		res, err := d.ExecContext(ctx,
			`INSERT INTO users (username, display_name, password_hash, created_at, is_guest)
			 VALUES (?, 'guest', '', ?, 1)`, "guest-"+suffix, now)
		if isUniqueViolation(err) {
			// Astronomically unlikely (2^32 names), but "unlikely" is not a reason
			// to return a 500 to a visitor: retry, then give up honestly.
			continue
		}
		if err != nil {
			return User{}, "", err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return User{}, "", err
		}
		tok, err := d.issueToken(ctx, id)
		if err != nil {
			return User{}, "", err
		}
		u, err := d.GetUserByID(ctx, id)
		if err != nil {
			return User{}, "", err
		}
		return u, tok, nil
	}
	return User{}, "", errors.New("could not find a free guest name after 5 attempts")
}

// IsGuest reports whether a user id belongs to a guest identity. Used by the write
// path of every route a guest may reach, so the limit is enforced per request and
// not only when the identity was minted.
func (d *DB) IsGuest(ctx context.Context, userID int64) (bool, error) {
	var g int
	err := d.QueryRowContext(ctx, `SELECT is_guest FROM users WHERE id = ?`, userID).Scan(&g)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	return g == 1, nil
}

// RecordConsent stamps the identity's acceptance of the cookie notice.
//
// The cookie is the gate the request path reads (it must be, since a GET of any
// page has to know whether to render the notice). This is the record that
// survives the browser, so "which identities accepted, and when" is answerable
// from the database rather than from the visitor's laptop.
func (d *DB) RecordConsent(ctx context.Context, userID int64) error {
	if userID == 0 {
		return fmt.Errorf("%w: consent needs an actor to record against", ErrInvalid)
	}
	_, err := d.ExecContext(ctx, `UPDATE users SET consented_at = ? WHERE id = ?`,
		float64(time.Now().Unix()), userID)
	return err
}

// ConsentRecords counts identities that have accepted, for the operator surface.
func (d *DB) ConsentRecords(ctx context.Context) (int, error) {
	var n int
	err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE consented_at IS NOT NULL`).Scan(&n)
	return n, err
}

// foldColumns is every column in the schema that carries a user id: the 47 that
// have a `REFERENCES users(id)`, plus the one that carries one without a foreign
// key, which is a decision rather than an oversight:
//
//   - audit_log.actor_id has no FK on purpose (the log outlives the row it names),
//     and it is folded with everything else so the account's history reads as one
//     identity's -- including the part that happened before it had a name.
//
// maintenance_signals.set_by is NOT in this list, and the reason is in the schema:
// it is TEXT, an operator's name, not a users.id. The gate below derives that by
// type, which is also where the first version of this list was wrong -- it named
// set_by as a user id and TestEveryColumnThatCarriesAUserIsFolded said so.
//
// THIS LIST IS NOT MAINTAINED BY HAND. TestEveryColumnThatCarriesAUserIsFolded
// derives the real set from the migrated schema (PRAGMA foreign_key_list, plus a
// name-and-type pattern for FK-less columns) and fails BOTH when a column is
// missing from this list and when this list names a column that does not carry a
// user id. A fold that silently misses a column is how "my votes disappeared" gets
// discovered months later, by the one person who cannot debug it.
var foldColumns = []struct{ Table, Column string }{
	{"admin_ledger", "actor_id"},
	{"api_tokens", "user_id"},
	{"audit_log", "actor_id"},
	{"capability_assertions", "asserted_by"},
	{"capability_confirmations", "user_id"},
	{"comment_votes", "user_id"},
	{"comments", "author_id"},
	{"complaint_impacts", "user_id"},
	{"complaints", "author_id"},
	{"consensus_calls", "opened_by"},
	{"criteria", "created_by"},
	{"criteria_profiles", "created_by"},
	{"criterion_votes", "voter_id"},
	{"emergency_holds", "held_by"},
	{"feature_tags", "applied_by"},
	{"features", "author_id"},
	{"field_report_responses", "user_id"},
	{"field_reports", "removed_by"},
	{"field_reports", "user_id"},
	{"invite_redemptions", "user_id"},
	{"list_entries", "proposed_by"},
	{"list_entry_votes", "voter_id"},
	{"lists", "created_by"},
	{"members", "user_id"},
	{"merge_approvals", "user_id"},
	{"merge_requests", "author_id"},
	{"merge_requests", "closed_by"},
	{"objections", "user_id"},
	{"override_positions", "user_id"},
	{"override_windows", "opened_by"},
	{"pairwise_votes", "voter_id"},
	{"positions", "user_id"},
	{"project_documents", "author_id"},
	{"project_invites", "created_by"},
	{"project_tags", "applied_by"},
	{"reputation_events", "user_id"},
	{"request_answer_votes", "voter_id"},
	{"request_answers", "proposed_by"},
	{"requests", "author_id"},
	{"solution_coverage", "contested_by"},
	{"solutions", "author_id"},
	{"strategic_theme_consents", "user_id"},
	{"strategic_theme_proposals", "proposed_by"},
	{"strategic_themes", "set_by"},
	{"strategic_weight_consents", "user_id"},
	{"strategic_weight_proposals", "proposed_by"},
	{"tag_aliases", "created_by"},
	{"taxonomy_consents", "user_id"},
	{"taxonomy_proposals", "proposed_by"},
	{"trust_grants", "granted_by"},
	{"trust_grants", "user_id"},
}

// FoldReport is what a fold did, so it can be told rather than assumed: the rows
// that changed owner, and the rows that could not because the account had already
// done the same thing.
type FoldReport struct {
	// Moved is rows that changed owner.
	Moved int `json:"moved"`
	// Superseded is rows dropped because the account already had a row under the
	// same key (a comment the guest upvoted, and the account had too). The
	// account's row wins: it is the identity that persists, and the guest's action
	// was taken with less standing. It is REPORTED, never silent -- "some of what
	// you did as a guest was already covered by your account" is a sentence a
	// person can read, and a dropped vote nobody mentions is not.
	Superseded int `json:"superseded"`
}

// FoldGuestInto moves everything guestID produced onto accountID, then removes the
// guest identity, and reports what it did.
//
// One transaction, because a half-folded guest is an identity whose votes are in
// two places: worse than either end state, and the kind of thing nobody notices
// until a quorum count is wrong.
//
// Collisions are real and reachable: several folded tables are keyed by
// (thing, user_id) -- comment_votes, complaint_impacts, positions -- so a guest
// who upvoted a comment their account had also upvoted cannot simply be moved
// there. UPDATE OR IGNORE moves what can move; what it refuses is counted and then
// deleted, because leaving it would leave a row owned by an identity that is about
// to stop existing.
func (d *DB) FoldGuestInto(ctx context.Context, guestID, accountID int64) (FoldReport, error) {
	var rep FoldReport
	if guestID == 0 || accountID == 0 || guestID == accountID {
		return rep, fmt.Errorf("%w: a fold needs two distinct identities", ErrInvalid)
	}
	g, err := d.GetUserByID(ctx, guestID)
	if err != nil {
		return rep, err
	}
	if !g.IsGuest {
		return rep, fmt.Errorf("%w: user %d is not a guest", ErrInvalid, guestID)
	}
	if _, err := d.GetUserByID(ctx, accountID); err != nil {
		return rep, err
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return rep, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, c := range foldColumns {
		var before int
		if err := tx.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s = ?`, c.Table, c.Column),
			guestID).Scan(&before); err != nil {
			return rep, fmt.Errorf("count %s.%s: %w", c.Table, c.Column, err)
		}
		res, err := tx.ExecContext(ctx,
			fmt.Sprintf(`UPDATE OR IGNORE %s SET %s = ? WHERE %s = ?`, c.Table, c.Column, c.Column),
			accountID, guestID)
		if err != nil {
			return rep, fmt.Errorf("fold %s.%s: %w", c.Table, c.Column, err)
		}
		moved, _ := res.RowsAffected()
		rep.Moved += int(moved)
		rep.Superseded += before - int(moved)
		if before > int(moved) {
			// Whatever OR IGNORE refused is still owned by an identity that is
			// about to be deleted, so it goes with it. Counted above.
			if _, err := tx.ExecContext(ctx,
				fmt.Sprintf(`DELETE FROM %s WHERE %s = ?`, c.Table, c.Column), guestID); err != nil {
				return rep, fmt.Errorf("drop superseded %s.%s: %w", c.Table, c.Column, err)
			}
		}
	}
	// The guest's tokens die with the guest. After a login the browser holds the
	// account's token; leaving a live token on a deleted row would be a credential
	// with no owner, and leaving it on the account would be a second way in that
	// the account never asked for.
	if _, err := tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE user_id = ?`, guestID); err != nil {
		return rep, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ? AND is_guest = 1`, guestID); err != nil {
		return rep, err
	}
	if err := tx.Commit(); err != nil {
		return FoldReport{}, err
	}
	// Written after the commit and best-effort: the fold is the fact, and a log
	// that cannot be written must not undo it. The guest id is named in the detail
	// because after this the identity no longer resolves.
	if err := d.AddAudit(ctx, 0, accountID, "guest-followed-into-account", "user", accountID,
		fmt.Sprintf(`{"guest":%d,"moved":%d,"superseded":%d}`, guestID, rep.Moved, rep.Superseded)); err != nil {
		return rep, err
	}
	return rep, nil
}

// PromoteGuest turns a guest identity into an account in place.
//
// Every row the guest produced already points at this id, so registering with a
// guest token copies nothing, orphans nothing, and keeps the history one
// identity's: "this account took these positions while it had no name". That is
// also why the fold-on-login path exists separately -- there a second identity is
// already holding the token, so its rows genuinely have to move.
func (d *DB) PromoteGuest(ctx context.Context, guestID int64, username, displayName, password string) (User, string, error) {
	g, err := d.GetUserByID(ctx, guestID)
	if err != nil {
		return User{}, "", err
	}
	if !g.IsGuest {
		return User{}, "", fmt.Errorf("%w: user %d is not a guest", ErrInvalid, guestID)
	}
	if !validUsername(username) {
		return User{}, "", fmt.Errorf("%w: username must be lowercase alnum with - _ .", ErrInvalid)
	}
	if err := validPassword(password); err != nil {
		return User{}, "", err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return User{}, "", err
	}
	_, err = d.ExecContext(ctx,
		`UPDATE users SET username = ?, display_name = ?, password_hash = ?, is_guest = 0
		 WHERE id = ? AND is_guest = 1`, username, displayName, hash, guestID)
	if isUniqueViolation(err) {
		return User{}, "", fmt.Errorf("%w: user %q", ErrDuplicate, username)
	}
	if err != nil {
		return User{}, "", err
	}
	u, err := d.GetUserByID(ctx, guestID)
	if err != nil {
		return User{}, "", err
	}
	tok, err := d.issueToken(ctx, guestID)
	if err != nil {
		return User{}, "", err
	}
	return u, tok, nil
}
