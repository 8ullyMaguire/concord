package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ModerationAct is one act before it is a row.
//
// Entity/EntityID address what the act was done to. They are audit_log's own columns, so
// "what happened to this feature" is an index scan the log already supports.
type ModerationAct struct {
	Action   string
	Entity   string
	EntityID int64
	Reason   string
	// Prev is the state the act is about to replace. The restore path reads it back rather
	// than accepting the values from a caller: a caller who can name a rating can invent one.
	Prev map[string]any
	// Undoes is the audit row this act reverses, or 0.
	Undoes int64
}

// ActResult is the appended row.
type ActResult struct {
	ID        int64   `json:"id"`
	ProjectID int64   `json:"project_id"`
	Action    string  `json:"action"`
	Entity    string  `json:"entity"`
	EntityID  int64   `json:"entity_id"`
	At        float64 `json:"at"`
}

// actJSON is audit_log.detail for one moderation act. The shapes are in the spec §4. The
// type exists so a typo in a key is a compile error rather than a null in the page.
type actJSON struct {
	Reason string         `json:"reason"`
	Prev   map[string]any `json:"prev,omitempty"`
	Undoes int64          `json:"undoes,omitempty"`
}

// insertAct appends one act inside an open transaction.
func insertAct(ctx context.Context, tx *sql.Tx, projectID, actorID int64, act ModerationAct) (ActResult, error) {
	reason := strings.TrimSpace(act.Reason)
	if reason == "" {
		return ActResult{}, fmt.Errorf("%w: moderation act %q has no reason", ErrInvalid, act.Action)
	}
	detail, err := json.Marshal(actJSON{Reason: reason, Prev: act.Prev, Undoes: act.Undoes})
	if err != nil {
		return ActResult{}, fmt.Errorf("encode act detail: %w", err)
	}
	at := float64(time.Now().Unix())
	var undoes any
	if act.Undoes != 0 {
		undoes = act.Undoes
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at, undoes)
		VALUES (?,?,?,?,?,?,?,?)`,
		projectID, actorID, act.Action, act.Entity, act.EntityID, string(detail), at, undoes)
	if err != nil {
		return ActResult{}, fmt.Errorf("write act %q: %w", act.Action, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ActResult{}, fmt.Errorf("act id: %w", err)
	}
	return ActResult{ID: id, ProjectID: projectID, Action: act.Action, Entity: act.Entity, EntityID: act.EntityID, At: at}, nil
}

// RemoveFeatureFromArena takes a feature out of one arena: the entry row goes, the feature's
// status becomes rejected, and one act records both prior values.
//
// The feature is NOT deleted. Its complaints, board card and any decision record address it;
// a delete would leave them pointing at nothing and would make the owner's "undo" request
// unimplementable without a tombstone. See docs/specs/moderation-spec.md D1.
func (d *DB) RemoveFeatureFromArena(ctx context.Context, projectID, arenaID, featureID, actorID int64, reason string) (ActResult, error) {
	f, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return ActResult{}, err // already wraps ErrNotFound
	}
	if f.ProjectID != projectID {
		return ActResult{}, fmt.Errorf("%w: feature %d", ErrNotFound, featureID)
	}
	if f.Status == "shipped" {
		// A shipped feature is not competing any more, and "rejecting" it would rewrite the
		// record of something that happened. Refused rather than silently allowed.
		return ActResult{}, fmt.Errorf("%w: a shipped feature cannot be removed from an arena", ErrInvalid)
	}
	entry, err := d.GetArenaEntry(ctx, arenaID, "feature", featureID)
	if err != nil {
		return ActResult{}, err
	}
	if entry.IsBaseline {
		return ActResult{}, fmt.Errorf("%w: the baseline entry of an arena cannot be removed", ErrInvalid)
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return ActResult{}, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	now := float64(time.Now().Unix())
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM arena_entries WHERE arena_id=? AND entity_type='feature' AND entity_id=?`,
		arenaID, featureID); err != nil {
		return ActResult{}, fmt.Errorf("remove arena entry: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE features SET status='rejected', updated_at=? WHERE id=? AND project_id=?`,
		now, featureID, projectID); err != nil {
		return ActResult{}, fmt.Errorf("reject feature: %w", err)
	}

	// The author is told, in the same transaction as the act. Skipped when the moderator acted
	// on their own feature (notifyTx enforces that) -- they performed it and the modlog records it.
	if err := notifyTx(ctx, tx, f.AuthorID, actorID, projectID, featureID,
		NotificationFeatureRemoved, f.Title, strings.TrimSpace(reason)); err != nil {
		return ActResult{}, err
	}

	act, err := insertAct(ctx, tx, projectID, actorID, ModerationAct{
		Action:   "arena.remove",
		Entity:   "feature",
		EntityID: featureID,
		Reason:   reason,
		Prev: map[string]any{
			"status":   f.Status,
			"arena_id": arenaID,
			"elo_r":    entry.R,
			"elo_rd":   entry.RD,
			"sigma":    entry.Sigma,
			"games":    entry.Games,
		},
	})
	if err != nil {
		return ActResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ActResult{}, fmt.Errorf("commit: %w", err)
	}
	return act, nil
}

// RestoreFeatureToArena reverses one arena.remove.
//
// The prior values come from the act being undone, never from the caller, and a second
// reversal of the same act is refused: the log is append-only and "undo twice" would
// re-insert an entry the second act did not remove.
func (d *DB) RestoreFeatureToArena(ctx context.Context, projectID, actorID, undoOf int64, reason string) (ActResult, error) {
	var action, entity, detail string
	var entityID sql.NullInt64
	err := d.QueryRowContext(ctx, `
		SELECT action, entity, entity_id, detail FROM audit_log
		WHERE id=? AND project_id=?`, undoOf, projectID).Scan(&action, &entity, &entityID, &detail)
	if errors.Is(err, sql.ErrNoRows) {
		return ActResult{}, fmt.Errorf("%w: act %d", ErrNotFound, undoOf)
	}
	if err != nil {
		return ActResult{}, fmt.Errorf("read act %d: %w", undoOf, err)
	}
	if action != "arena.remove" || entity != "feature" || !entityID.Valid {
		return ActResult{}, fmt.Errorf("%w: act %d is %q and cannot be restored", ErrInvalid, undoOf, action)
	}
	var already int
	if err := d.QueryRowContext(ctx,
		`SELECT count(*) FROM audit_log WHERE undoes=?`, undoOf).Scan(&already); err != nil {
		return ActResult{}, fmt.Errorf("check reversal: %w", err)
	}
	if already > 0 {
		return ActResult{}, fmt.Errorf("%w: act %d has already been reversed", ErrInvalid, undoOf)
	}

	var wrapper struct {
		Prev json.RawMessage `json:"prev"`
	}
	if err := json.Unmarshal([]byte(detail), &wrapper); err != nil || len(wrapper.Prev) == 0 {
		return ActResult{}, fmt.Errorf("%w: act %d carries no prior state", ErrInvalid, undoOf)
	}
	var prev struct {
		Status string  `json:"status"`
		Arena  int64   `json:"arena_id"`
		EloR   float64 `json:"elo_r"`
		EloRD  float64 `json:"elo_rd"`
		Sigma  float64 `json:"sigma"`
		Games  int     `json:"games"`
	}
	if err := json.Unmarshal(wrapper.Prev, &prev); err != nil {
		return ActResult{}, fmt.Errorf("%w: act %d's prior state is unreadable", ErrInvalid, undoOf)
	}
	if prev.Status == "" || prev.Arena == 0 {
		return ActResult{}, fmt.Errorf("%w: act %d's prior state is incomplete", ErrInvalid, undoOf)
	}
	// The subject must still be this project's: a project's log cannot restore a feature that
	// has since moved, and the check is cheap.
	f, err := d.GetFeature(ctx, entityID.Int64)
	if err != nil {
		return ActResult{}, err
	}
	if f.ProjectID != projectID {
		return ActResult{}, fmt.Errorf("%w: feature %d", ErrNotFound, entityID.Int64)
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return ActResult{}, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	now := float64(time.Now().Unix())
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd, sigma, games, updated_at)
		VALUES (?, 'feature', ?, ?, ?, ?, ?, ?)
		ON CONFLICT(arena_id, entity_type, entity_id) DO UPDATE SET
			r=excluded.r, rd=excluded.rd, sigma=excluded.sigma,
			games=excluded.games, updated_at=excluded.updated_at`,
		prev.Arena, entityID.Int64, prev.EloR, prev.EloRD, prev.Sigma, prev.Games, now); err != nil {
		return ActResult{}, fmt.Errorf("restore arena entry: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE features SET status=?, updated_at=? WHERE id=? AND project_id=?`,
		prev.Status, now, entityID.Int64, projectID); err != nil {
		return ActResult{}, fmt.Errorf("restore status: %w", err)
	}
	act, err := insertAct(ctx, tx, projectID, actorID, ModerationAct{
		Action:   "arena.restore",
		Entity:   "feature",
		EntityID: entityID.Int64,
		Reason:   reason,
		Undoes:   undoOf,
	})
	if err != nil {
		return ActResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ActResult{}, fmt.Errorf("commit: %w", err)
	}
	return act, nil
}

// ArenasForFeature returns the arenas a feature currently competes in.
//
// The feature page does not know which arena an idea is in, and it should not have to: the
// arena is a property of the ranking, not of the idea. The removal route uses this to
// resolve the arena when the caller names only the feature, and refuses to guess when there
// is more than one.
func (d *DB) ArenasForFeature(ctx context.Context, featureID int64) ([]int64, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT arena_id FROM arena_entries
		WHERE entity_type='feature' AND entity_id=?
		ORDER BY arena_id`, featureID)
	if err != nil {
		return nil, fmt.Errorf("arenas for feature %d: %w", featureID, err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan arena id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
