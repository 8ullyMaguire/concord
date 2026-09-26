package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// FeatureTally is one feature's participation record.
//
// It exists because a rating on its own cannot be judged. A feature with no
// votes and a feature with four hundred are both "1500" if the maths is fed
// nothing, and only the second of those means anything.
type FeatureTally struct {
	FeatureID int64 `json:"feature_id"`
	Title     string
	Wins      int `json:"wins"`
	Losses    int `json:"losses"`
	Skips     int `json:"skips"`
	VotesCast int `json:"votes_cast"`
}

// JoinProject enrols a user as a contributor in a project.
//
// It is deliberately not "SetRole". An existing membership is never modified:
// enrolment is a side effect of taking part, so calling it again must be a
// no-op, and it must never demote the maintainer who created the project
// simply because they filed a complaint. INSERT OR IGNORE is load-bearing
// here, not a shortcut.
//
// The alternative — a separate POST /join a user must call before voting —
// buys nothing. It adds a step whose only purpose is to satisfy a role check
// and creates a class of account that is authenticated but has done nothing,
// which is exactly the state the system should not have. Communities do not
// work that way: you do not apply to a forum before posting to it.
func (d *DB) JoinProject(ctx context.Context, projectID, userID int64) error {
	_, err := d.ExecContext(ctx, `
		INSERT OR IGNORE INTO members (project_id, user_id, role, joined_at)
		VALUES (?, ?, 'contributor', ?)`,
		projectID, userID, float64(time.Now().Unix()))
	if err != nil {
		return fmt.Errorf("join project: %w", err)
	}
	return nil
}

// IsFeatureAuthor reports whether userID proposed featureID.
//
// Voting on your own feature is refused because Glicko-2 has no way to
// discount it. A rating inflated by its author's own vote is not a measurement
// of anyone else's preference, and the inflation is unbounded: the author can
// vote for the same feature against every competitor, forever.
//
// Returns ErrNotFound for a feature that does not exist, so the caller can
// distinguish "not yours" from "no such thing" — a self-vote refusal for a
// feature that is not there would be a confusing way to say 404.
func (d *DB) IsFeatureAuthor(ctx context.Context, featureID, userID int64) (bool, error) {
	var authorID int64
	err := d.QueryRowContext(ctx,
		`SELECT author_id FROM features WHERE id = ?`, featureID).Scan(&authorID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("feature %d: %w", featureID, ErrNotFound)
	}
	if err != nil {
		return false, fmt.Errorf("load feature author: %w", err)
	}
	return authorID == userID, nil
}

// ListMembers returns a project's membership, most privileged first.
//
// Ordering is by rank rather than alphabetically because the roster is read to
// answer "who can change the charter", and an answer in role order is the one
// that answers the question.
func (d *DB) ListMembers(ctx context.Context, projectID int64) ([]Member, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT m.project_id, m.user_id, m.role, m.is_moderator, m.joined_at, u.username
		FROM members m
		JOIN users u ON u.id = m.user_id
		WHERE m.project_id = ?
		ORDER BY m.joined_at`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	// Read every row before ranking. The pool allows one connection, so
	// issuing a query while this cursor is open would wait for a connection
	// that cannot be handed out until the cursor closes.
	type row struct {
		m Member
		n string
	}
	var collected []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.m.ProjectID, &r.m.UserID, &r.m.Role,
			&r.m.IsModerator, &r.m.JoinedAt, &r.n); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		collected = append(collected, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate members: %w", err)
	}

	// Most privileged first, then by username so the order is stable and
	// does not depend on join time.
	ranks := map[string]int{
		"guest": 0, "user": 1, "contributor": 2,
		"reviewer": 3, "maintainer": 4, "owner": 5,
	}
	out := make([]Member, 0, len(collected))
	for _, r := range collected {
		r.m.Username = r.n
		out = append(out, r.m)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a, b := out[j-1], out[j]
			if ranks[b.Role] > ranks[a.Role] ||
				(ranks[b.Role] == ranks[a.Role] && b.UserID < a.UserID) {
				out[j-1], out[j] = b, a
				continue
			}
			break
		}
	}
	return out, nil
}

// FeatureTallies returns participation counts per feature for a project.
//
// Skips are counted separately from losses because they mean different things:
// a loss is evidence against a feature, a skip is the absence of evidence. Fold
// them together and the UI cannot tell "nobody wanted it" from "nobody had an
// opinion", which are opposite conclusions.
func (d *DB) FeatureTallies(ctx context.Context, projectID int64) ([]FeatureTally, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT f.id, f.title,
		       COALESCE(SUM(CASE WHEN pv.outcome IN ('a','both') THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN pv.outcome IN ('b','neither') THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN pv.outcome IN ('skip','neither') THEN 1 ELSE 0 END), 0)
		FROM features f
		LEFT JOIN pairwise_votes pv
		       ON pv.feature_a = f.id OR pv.feature_b = f.id
		WHERE f.project_id = ?
		GROUP BY f.id, f.title
		ORDER BY f.id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("feature tallies: %w", err)
	}
	defer rows.Close()

	var out []FeatureTally
	for rows.Next() {
		var t FeatureTally
		if err := rows.Scan(&t.FeatureID, &t.Title, &t.Wins, &t.Losses, &t.Skips); err != nil {
			return nil, fmt.Errorf("scan tally: %w", err)
		}
		t.VotesCast = t.Wins + t.Losses
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tallies: %w", err)
	}
	return out, nil
}
