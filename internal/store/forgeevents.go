package store

import (
	"context"
	"database/sql"
	"fmt"

	"git.polarisocial.xyz/concord/concord/internal/discovery"
)

// RecordForgeEvent writes one repository event, and is FREE on redelivery.
//
// The UNIQUE constraint (repo_full, kind, number, actor, at) plus ON CONFLICT DO
// NOTHING is what makes a replayed webhook payload a no-op. Without it, Forgejo
// retrying a delivery doubles every sample, and a median computed over doubled
// rows is still a median over the same set -- until one delivery is lost and the
// asymmetry silently weights the survivors. Idempotency is not tidiness here; it
// is what makes the number recomputable.
//
// inserted reports whether the row was new. The webhook does not need it, but the
// test for the redelivery AC does, and a caller that cannot observe the
// difference cannot prove the constraint is doing anything.
//
// The audit row the webhook already writes is NOT replaced. The audit log answers
// "what did the instance do"; this table answers "what happened in the
// repository". Collapsing them loses the ability to recompute after a rule
// change.
func (d *DB) RecordForgeEvent(ctx context.Context, ev discovery.ForgeEvent, receivedAt float64) (bool, error) {
	if ev.RepoFull == "" {
		return false, fmt.Errorf("%w: forge event has no repository", ErrInvalid)
	}
	switch ev.Kind {
	case discovery.ForgeEventPush, discovery.ForgeEventPROpened, discovery.ForgeEventPRClosed,
		discovery.ForgeEventPRReview, discovery.ForgeEventPRComment:
	default:
		// Refused rather than stored: the table's CHECK would reject it with a
		// constraint error naming a column rather than a value, and an unknown
		// kind is a parser bug worth surfacing as one.
		return false, fmt.Errorf("%w: unknown forge event kind %q", ErrInvalid, ev.Kind)
	}
	var number any
	if ev.Number != 0 {
		number = ev.Number
	}
	res, err := d.ExecContext(ctx,
		`INSERT INTO forge_events (repo_full, kind, number, actor, at, received_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (repo_full, kind, number, actor, at) DO NOTHING`,
		ev.RepoFull, ev.Kind, number, ev.Actor, ev.At, receivedAt)
	if err != nil {
		return false, fmt.Errorf("record forge event: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		// The driver cannot say; the write itself succeeded. Reporting an error
		// here would fail a delivery that actually landed.
		return true, nil
	}
	return n > 0, nil
}

// UpdateProjectNewcomer records the newcomer responsiveness measurement on
// project_metrics (0031_repo_identity.sql).
//
// hours is a POINTER for the same reason MedianReviewHours is passed as a
// sentinel to UpdateProjectMetrics and then turned into NULL there: "we could
// not measure it" and "we measured zero hours" are different facts, and zero is
// the BEST possible score. Passing 0 for an unmeasured project would publish a
// perfect responsiveness nobody experienced.
//
// sample is written even when hours is NULL. It is the fact that explains the
// silence -- a project at 3 of the required 5 says why it is absent from the
// leaderboard, while a bare NULL says nothing.
//
// A SEPARATE METHOD FROM UpdateProjectMetrics, deliberately. UpdateProjectMetrics
// is also reached by PATCH /projects/{slug}/metrics, whose request body has no
// newcomer fields; folding these columns into that upsert would let an operator
// repairing a star count blank the responsiveness median, because the route
// would write zeros for a measurement it never takes. Two sources, two writes.
func (d *DB) UpdateProjectNewcomer(ctx context.Context, slug string, hours *float64, sample int) error {
	if sample < 0 {
		return fmt.Errorf("%w: newcomer sample %d", ErrInvalid, sample)
	}
	if hours != nil && sample < discovery.NewcomerMinSample {
		// The threshold is enforced here as well as in NewcomerResponse: a caller
		// that computes its own median cannot publish one over three PRs, and a
		// row where the sample contradicts the published number is unreadable.
		return fmt.Errorf("%w: newcomer median published on a sample of %d (floor %d)",
			ErrInvalid, sample, discovery.NewcomerMinSample)
	}
	id, err := d.projectIDBySlug(ctx, slug)
	if err != nil {
		return err
	}
	var value any
	if hours != nil {
		value = *hours
	}
	// The INSERT branch exists so this cannot silently no-op on a project that has
	// never been health-synced; it writes only the two columns it owns and leaves
	// the forge facts at their defaults for UpdateProjectMetrics to fill.
	_, err = d.ExecContext(ctx, `
		INSERT INTO project_metrics (project_id, newcomer_response_hours, newcomer_sample)
		VALUES (?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			newcomer_response_hours=excluded.newcomer_response_hours,
			newcomer_sample=excluded.newcomer_sample`,
		id, value, sample)
	if err != nil {
		return fmt.Errorf("update project newcomer: %w", err)
	}
	return nil
}

// ListForgeEvents returns every recorded event for one repository, oldest first.
//
// A repository with no events returns an empty slice and no error -- that is the
// normal state of a project whose webhook has never fired, not a failure, and the
// caller distinguishes it by n = 0 rather than by an error.
func (d *DB) ListForgeEvents(ctx context.Context, repoFull string) ([]discovery.ForgeEvent, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT repo_full, kind, COALESCE(number, 0), actor, at
		   FROM forge_events WHERE repo_full = ? ORDER BY at ASC`, repoFull)
	if err != nil {
		return nil, fmt.Errorf("list forge events: %w", err)
	}
	defer rows.Close()

	var out []discovery.ForgeEvent
	for rows.Next() {
		var e discovery.ForgeEvent
		var number sql.NullInt64
		if err := rows.Scan(&e.RepoFull, &e.Kind, &number, &e.Actor, &e.At); err != nil {
			return nil, fmt.Errorf("scan forge event: %w", err)
		}
		e.Number = int(number.Int64)
		out = append(out, e)
	}
	return out, rows.Err()
}
