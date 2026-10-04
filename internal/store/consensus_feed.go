package store

import (
	"context"
	"database/sql"
	"strings"
)

// ListConsensusCallsForFeed returns a project's consensus calls, newest first.
//
// It deliberately does NOT return a tally. §6.6 hides the running counts until the call
// closes, and a feed item is a durable copy: aggregators cache these and resurface them
// years later, so publishing counts here would defeat the rule no matter how carefully
// the page and the API obey it. The item carries `state` instead, which says the call is
// open and nothing about how anyone has voted.
//
// Ordered by opens_at, not by CreatedAt: consensus_calls has no created_at column at all.
// The Go struct has carried a zero-valued CreatedAt since migration 0014 rebuilt the
// table, so ordering by the struct field yields an arbitrary order.
func (d *DB) ListConsensusCallsForFeed(ctx context.Context, projectID int64, limit int) ([]ConsensusCall, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := d.QueryContext(ctx, `
		SELECT id, project_id, feature_id, opened_by, opens_at, closes_at,
		       status, question, description, result, opened_early, closed_at
		FROM consensus_calls
		WHERE project_id = ?
		ORDER BY opens_at DESC, id DESC
		LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ConsensusCall{}
	for rows.Next() {
		var c ConsensusCall
		// Nullable because the schema says so. `result`, `question` and `description`
		// are all NULL for calls opened before those columns existed, and
		// `opened_early` is NULL unless §6.5's waiver was used. Scanning a NULL into
		// a bare string or bool fails the entire read, so one NULL question would
		// take down the whole feed rather than one item.
		// feature_id is NULLABLE -- the schema says so in a comment on the column
		// ("0/NULL for a process decision that is not about one feature"), and every
		// §6.6 process decision stores NULL there. Scanning it into a bare int64 fails
		// the whole read with a 500, which is how this surfaced: a process decision
		// takes a project's whole consensus feed down.
		var featureID sql.NullInt64
		var question, description, result sql.NullString
		var openedEarly sql.NullBool
		var closedAt sql.NullFloat64
		if err := rows.Scan(&c.ID, &c.ProjectID, &featureID, &c.OpenedBy, &c.OpensAt,
			&c.ClosesAt, &c.Status, &question, &description, &result,
			&openedEarly, &closedAt); err != nil {
			return nil, err
		}
		if featureID.Valid {
			c.FeatureID = featureID.Int64
		}
		c.Question = nullStringPtr(question)
		c.Description = nullStringPtr(description)
		c.Result = nullStringPtr(result)
		if openedEarly.Valid {
			v := openedEarly.Bool
			c.OpenedEarly = &v
		}
		if closedAt.Valid {
			v := closedAt.Float64
			c.ClosedAt = &v
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DisplayNamesFor resolves user ids to display names in ONE query.
//
// Batched because a feed holds up to 50 items and a per-item lookup is N+1 queries on a
// page an aggregator may fetch on a timer.
//
// display_name only. Never username, never email: a feed is cached indefinitely by
// whoever subscribes to it, so an address in one is a permanent exposure. A user with no
// display name maps to the empty string rather than to their username -- the caller
// decides what to omit, so that decision is visible in the feed code and not here.
func (d *DB) DisplayNamesFor(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	// Placeholders built from the id count. Bound as parameters, never interpolated:
	// an id is an int64 here, but the same query shape copied with a string column
	// would be an injection, and this function is the one a future caller will copy.
	q := `SELECT id, display_name FROM users WHERE id IN (?` +
		strings.Repeat(",?", len(ids)-1) + `)`
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// nullStringPtr collapses a NULL column to nil so a NULL and an absent value are the same
// thing, rather than a pointer to "".
func nullStringPtr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}
