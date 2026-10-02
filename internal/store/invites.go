package store

// Invite lifecycle for 'protected' projects.
//
// Why this exists: 'protected' means "listed nowhere, readable by a signed-in
// user who has been granted access". The grant has to come from somewhere other
// than the request that wants it, so it is an unguessable token that a member
// mints and hands over. The redemption is recorded in invite_redemptions, and
// redeem is idempotent per (invite, user) so a link clicked twice does not
// create two memberships.
//
// The alternative -- a boolean "has_access" column on users -- cannot answer
// "who let this person in, when, and under which link", which is exactly the
// question an audit has to answer.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// Invite is a minted access link for one project.
type Invite struct {
	ID         int64   `json:"id"`
	ProjectID  int64   `json:"project_id"`
	Project    string  `json:"project"` // slug, for display in a UI
	Token      string  `json:"token"`
	CreatedBy  int64   `json:"created_by"`
	CreatedAt  float64 `json:"created_at"`
	ExpiresAt  *float64 `json:"expires_at,omitempty"`
	MaxUses    *int     `json:"max_uses,omitempty"`
	Uses       int      `json:"uses"`
	RevokedAt  *float64 `json:"revoked_at,omitempty"`
}

// TokenLength is the number of random bytes behind an invite token. base64 of 32
// bytes is 43 characters: enough entropy that guessing one is not a plan
// anyone has.
const TokenLength = 32

// newInviteToken returns a URL-safe random token, or an error. crypto/rand
// rather than math/rand: this value is the only thing standing between a
// stranger and a protected project, and math/rand is seeded from the clock.
func newInviteToken() (string, error) {
	b := make([]byte, TokenLength)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate invite token: %w", err)
	}
	// RawURLEncoding so the token can be pasted into a URL path or a query
	// string without escaping.
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CreateInvite mints an invite for a project.
//
// expiresInSeconds <= 0 means no expiry. maxUses <= 0 means unlimited. The two
// are checked by the migration's CHECK constraints as well, but an invalid pair
// should never reach the table: a caller that passes maxUses=3 with an expiry
// that has already passed has made a mistake, not a policy.
func (d *DB) CreateInvite(ctx context.Context, projectID, createdBy int64, expiresInSeconds, maxUses int) (Invite, error) {
	if _, err := d.GetProjectByID(ctx, projectID); err != nil {
		return Invite{}, err
	}

	token, err := newInviteToken()
	if err != nil {
		return Invite{}, err
	}

	now := time.Now().Unix()
	var expires any
	if expiresInSeconds > 0 {
		expires = float64(now + int64(expiresInSeconds))
	}
	var uses any
	if maxUses > 0 {
		uses = maxUses
	}

	res, err := d.ExecContext(ctx, `
		INSERT INTO project_invites (project_id, token, created_by, created_at, expires_at, max_uses)
		VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, token, createdBy, float64(now), expires, uses)
	if err != nil {
		return Invite{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Invite{}, err
	}
	return d.GetInvite(ctx, id)
}

// GetInvite loads one invite by id.
func (d *DB) GetInvite(ctx context.Context, id int64) (Invite, error) {
	var i Invite
	var expires, maxUses, revoked sql.NullFloat64
	err := d.QueryRowContext(ctx, `
		SELECT i.id, i.project_id, p.slug, i.token, i.created_by, i.created_at,
		       i.expires_at, i.max_uses, i.uses, i.revoked_at
		FROM project_invites i JOIN projects p ON p.id = i.project_id
		WHERE i.id = ?`, id).Scan(
		&i.ID, &i.ProjectID, &i.Project, &i.Token, &i.CreatedBy, &i.CreatedAt,
		&expires, &maxUses, &i.Uses, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return Invite{}, fmt.Errorf("%w: invite %d", ErrNotFound, id)
	}
	if err != nil {
		return Invite{}, err
	}
	if expires.Valid {
		v := expires.Float64
		i.ExpiresAt = &v
	}
	if maxUses.Valid {
		v := int(maxUses.Float64)
		i.MaxUses = &v
	}
	if revoked.Valid {
		v := revoked.Float64
		i.RevokedAt = &v
	}
	return i, nil
}

// ListInvites returns a project's invites, newest first, for the members who
// minted them to see what is outstanding.
func (d *DB) ListInvites(ctx context.Context, projectID int64) ([]Invite, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT i.id FROM project_invites i
		WHERE i.project_id = ?
		ORDER BY i.created_at DESC, i.id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Invite, 0, len(ids))
	for _, id := range ids {
		i, err := d.GetInvite(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, nil
}

// RevokeInvite marks an invite dead without deleting it, so the record of who
// was invited and when survives the link being withdrawn.
func (d *DB) RevokeInvite(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx,
		`UPDATE project_invites SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		float64(time.Now().Unix()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Either it does not exist or it was already revoked. Distinguish so
		// the caller can report honestly.
		var exists int
		if err := d.QueryRowContext(ctx, `SELECT count(*) FROM project_invites WHERE id = ?`, id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("%w: invite %d", ErrNotFound, id)
		}
		return fmt.Errorf("%w: invite %d is already revoked", ErrInvalid, id)
	}
	return nil
}

// RedeemInvite turns a valid token into a membership for userID.
//
// Idempotent: redeeming twice returns the same success and creates no second
// membership, because invite_redemptions has PRIMARY KEY (invite_id, user_id).
// The membership insert is on its own ON CONFLICT DO NOTHING for the same
// reason -- two browsers opening the same link is normal, not an attack.
//
// Every failure mode returns ErrNotFound rather than a distinct error, because
// the caller turns all of them into the same 404 and a distinguishable error
// would let someone probe tokens: "expired" and "revoked" and "wrong project"
// all confirm that the token exists.
func (d *DB) RedeemInvite(ctx context.Context, token string, userID int64) (int64, error) {
	if userID == 0 {
		return 0, ErrAuth
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var (
		inviteID  int64
		projectID int64
		expiresAt sql.NullFloat64
		maxUses   sql.NullFloat64
		uses      int
		revokedAt sql.NullFloat64
	)
	err = tx.QueryRowContext(ctx, `
		SELECT id, project_id, expires_at, max_uses, uses, revoked_at
		FROM project_invites WHERE token = ?`, token).
		Scan(&inviteID, &projectID, &expiresAt, &maxUses, &uses, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: invite", ErrNotFound)
	}
	if err != nil {
		return 0, err
	}

	now := time.Now().Unix()
	if revokedAt.Valid {
		return 0, fmt.Errorf("%w: invite", ErrNotFound)
	}
	if expiresAt.Valid && expiresAt.Float64 < float64(now) {
		return 0, fmt.Errorf("%w: invite", ErrNotFound)
	}

	// Check whether this user has already redeemed BEFORE applying the use cap.
	// A repeat redemption by someone who already holds access must succeed even
	// when the invite is exhausted: they are not consuming a new use, and
	// refusing them would make a max_uses=1 link fail on the second tab or a
	// browser retry -- turning an idempotent action into an error that reads as
	// "this link is dead".
	var already int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM invite_redemptions WHERE invite_id = ? AND user_id = ?`,
		inviteID, userID).Scan(&already); err != nil {
		return 0, err
	}
	alreadyRedeemed := already > 0

	if maxUses.Valid && float64(uses) >= maxUses.Float64 && !alreadyRedeemed {
		return 0, fmt.Errorf("%w: invite", ErrNotFound)
	}

	// The redemption record, then the membership. Both ignore conflicts: a
	// repeated click is a success, not an error.
	//
	// RowsAffected is what distinguishes a first redemption from a repeat: with
	// ON CONFLICT DO NOTHING the statement succeeds either way, so only the
	// count says whether this user is new. The use counter has to advance on the
	// first redemption only, or a person clicking the link twice would exhaust a
	// max_uses=1 invite by themselves.
	if !alreadyRedeemed {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO invite_redemptions (invite_id, user_id, redeemed_at)
			VALUES (?, ?, ?)
			ON CONFLICT (invite_id, user_id) DO NOTHING`,
			inviteID, userID, float64(now)); err != nil {
			return 0, err
		}
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO members (project_id, user_id, role, is_moderator, joined_at)
		VALUES (?, ?, 'contributor', 0, ?)
		ON CONFLICT (project_id, user_id) DO NOTHING`,
		projectID, userID, float64(now)); err != nil {
		return 0, err
	}

	if !alreadyRedeemed {
		if _, err := tx.ExecContext(ctx,
			`UPDATE project_invites SET uses = uses + 1 WHERE id = ?`, inviteID); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return projectID, nil
}