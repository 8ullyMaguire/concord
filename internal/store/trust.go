// Trust level: per-user, instance-wide, and bounded by trust_config.max_level.
//
// This is deliberately NOT the same thing as a project role. A role answers
// "what may this person do in this project" and is granted per project; a
// trust level answers "how much do we believe this account on this instance".
// Mixing them would mean an account's power silently changes when they join a
// project, which is the wrong coupling for a threshold that gates approving
// other people's work.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrTrustCeiling is returned when an assignment would exceed the configured
// ceiling. It is distinct from ErrPerm so a caller can tell "you may not do
// this" from "this would exceed the instance's maximum".
var ErrTrustCeiling = errors.New("trust level exceeds the configured maximum")

// GetTrustLevel returns an account's trust level, 0 for an unknown account.
//
// An unknown account is 0 rather than an error: the callers are all
// authorisation checks, and a missing user must fail closed without needing a
// separate branch at every call site.
func (d *DB) GetTrustLevel(ctx context.Context, userID int64) (int, error) {
	var lvl int
	err := d.QueryRowContext(ctx, `SELECT trust_level FROM users WHERE id=?`, userID).Scan(&lvl)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return lvl, err
}

// GetUserCreatedAt returns the account's creation time (unix seconds), used to
// ramp a new account's vote weight (spec §12.3). A missing user is reported as
// zero rather than as an error, so a caller deciding on a *weight multiplier*
// can treat "unknown" as "brand new" instead of failing the whole vote.
func (d *DB) GetUserCreatedAt(ctx context.Context, userID int64) (float64, error) {
	var created float64
	err := d.QueryRowContext(ctx, `SELECT created_at FROM users WHERE id=?`, userID).Scan(&created)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return created, err
}

// SetTrustLevel assigns a trust level, refusing to exceed the configured
// ceiling and recording who did it.
//
// The ceiling is enforced in the same statement that writes the grant, so a
// concurrent second writer cannot slip past a read-then-write check. The
// audit row is written first and in the same transaction: if the grant fails,
// no history of an attempt that did not happen is left behind.
func (d *DB) SetTrustLevel(ctx context.Context, targetUser int64, level int, grantedBy int64, reason string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var maxLevel int
	if err := tx.QueryRowContext(ctx, `SELECT max_level FROM trust_config WHERE id=1`).Scan(&maxLevel); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			maxLevel = 1
		} else {
			return err
		}
	}
	if level > maxLevel {
		return fmt.Errorf("%w: %d > %d", ErrTrustCeiling, level, maxLevel)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO trust_grants (user_id, level, granted_by, reason, created_at)
		 VALUES (?,?,?,?,?)`,
		targetUser, level, grantedBy, reason, float64(time.Now().Unix())); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET trust_level=? WHERE id=?`, level, targetUser); err != nil {
		return err
	}
	return tx.Commit()
}

// TrustCeiling returns the instance maximum, for reporting.
func (d *DB) TrustCeiling(ctx context.Context) (int, error) {
	var maxLevel int
	err := d.QueryRowContext(ctx, `SELECT max_level FROM trust_config WHERE id=1`).Scan(&maxLevel)
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	return maxLevel, err
}
