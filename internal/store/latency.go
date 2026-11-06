package store

import (
	"context"
	"database/sql"
	"strings"

	"git.polarisocial.xyz/concord/concord/internal/discovery"
)

// The three latency measures (spec §3.2). Each returns a Latency computed by
// discovery.Measure over the episodes this file derives from the audit trail.
//
// WHY EPISODES AND NOT A QUERY THAT COMPUTES THE MEDIAN IN SQL: the median needs
// the sorted sample, and the interesting decisions are about which rows are in
// the sample at all -- whether an open consensus call counts, whether a feature
// that never passed through `ready` counts, whether a complaint validated twice
// uses the first or the last validation. Those are Go-level facts about the
// audit trail, and keeping them here leaves discovery.Measure a pure function
// that can be tested without a database.

// TriageLatency measures complaint created_at -> first validate_complaint audit
// row. The audit row is written by the HTTP layer (handlers.go, "validate_complaint"),
// not by the store: ValidateComplaint only moves the status, so a caller that
// looks for the transition in the store will not find it.
func (d *DB) TriageLatency(ctx context.Context) (discovery.Latency, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT c.created_at,
		       (SELECT MIN(a.created_at) FROM audit_log a
		         WHERE a.action = 'validate_complaint'
		           AND a.entity = 'complaint'
		           AND a.entity_id = c.id)
		FROM complaints c`)
	if err != nil {
		return discovery.Latency{}, err
	}
	defer rows.Close()

	var episodes []discovery.Episode
	for rows.Next() {
		var createdAt float64
		var validatedAt sql.NullFloat64
		if err := rows.Scan(&createdAt, &validatedAt); err != nil {
			return discovery.Latency{}, err
		}
		episodes = append(episodes, discovery.Episode{
			StartedAt: createdAt,
			HasStart:  true,
			EndedAt:   validatedAt.Float64,
			HasEnd:    validatedAt.Valid,
		})
	}
	return discovery.Measure(episodes), rows.Err()
}

// DeliveryLatency measures feature entering `ready` -> entering `shipped`, taken
// from set_feature_status audit rows.
//
// THE TRANSITION IS IN THE DETAIL STRING. trust.go writes
// `fmt.Sprintf("%s -> %s: %s", from, to, reason)`, so the target status is not a
// column -- it is text after " -> " and before ":". Parsing it in SQL with a LIKE
// would also match a reason that happens to contain "-> shipped", so it is parsed
// here by parseTransitionDetail.
//
// CURRENT EPISODE, NOT FIRST. Spec §3.2: "Reopened entities are measured on their
// current episode." So the start is the LAST transition into `ready`, and the end
// is the FIRST transition into `shipped` after that. A feature that shipped, was
// reopened and shipped again measures the second episode, not the first.
func (d *DB) DeliveryLatency(ctx context.Context) (discovery.Latency, error) {
	return d.deliveryLatency(ctx, 0)
}

// DeliveryLatencyForProject measures one project's ready -> shipped episodes.
// A badge needs a project's own number, not the instance's: a badge that shows
// the instance median under a project's name is a number about something else.
func (d *DB) DeliveryLatencyForProject(ctx context.Context, projectID int64) (discovery.Latency, error) {
	return d.deliveryLatency(ctx, projectID)
}

func (d *DB) deliveryLatency(ctx context.Context, projectID int64) (discovery.Latency, error) {
	query := `
		SELECT entity_id, detail, created_at
		FROM audit_log
		WHERE action = 'set_feature_status' AND entity = 'feature'`
	var args []any
	if projectID > 0 {
		query += ` AND project_id = ?`
		args = append(args, projectID)
	}
	query += ` ORDER BY entity_id, created_at`
	rows, err := d.QueryContext(ctx, query, args...)
	if err != nil {
		return discovery.Latency{}, err
	}
	defer rows.Close()

	byFeature := map[int64][]transition{}
	for rows.Next() {
		var featureID int64
		var detail sql.NullString
		var at float64
		if err := rows.Scan(&featureID, &detail, &at); err != nil {
			return discovery.Latency{}, err
		}
		_, to, ok := parseTransitionDetail(detail.String)
		if !ok {
			continue
		}
		byFeature[featureID] = append(byFeature[featureID], transition{at: at, to: to})
	}
	if err := rows.Err(); err != nil {
		return discovery.Latency{}, err
	}

	episodes := make([]discovery.Episode, 0, len(byFeature))
	for _, ts := range byFeature {
		episodes = append(episodes, episodeForDelivery(ts))
	}
	return discovery.Measure(episodes), nil
}

// transition is one audited status change on a feature.
type transition struct {
	at float64
	to string
}

// episodeForDelivery applies the current-episode rule to one feature's ordered
// transitions.
func episodeForDelivery(ts []transition) discovery.Episode {
	lastReady := -1
	shippedAtAll := false
	for i, t := range ts {
		if t.to == "ready" {
			lastReady = i
		}
		if t.to == "shipped" {
			shippedAtAll = true
		}
	}
	// Never entered `ready`. If it nevertheless shipped, the row is finished but
	// has no recorded start -- unmeasured, not unfinished, because reporting a
	// shipped feature as still in progress would be false.
	if lastReady < 0 {
		return discovery.Episode{HasStart: false, HasEnd: shippedAtAll}
	}
	for i := lastReady + 1; i < len(ts); i++ {
		if ts[i].to == "shipped" {
			return discovery.Episode{
				StartedAt: ts[lastReady].at, HasStart: true,
				EndedAt: ts[i].at, HasEnd: true,
			}
		}
	}
	return discovery.Episode{StartedAt: ts[lastReady].at, HasStart: true}
}

// parseTransitionDetail reads the "<from> -> <to>: <reason>" form written by the
// set_feature_status audit row. ok is false when the detail does not have that
// shape, which the caller treats as "not a status transition" rather than as an
// error: an audit row whose detail is malformed is skipped, not fatal.
func parseTransitionDetail(detail string) (from, to string, ok bool) {
	parts := strings.SplitN(detail, " -> ", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	to = parts[1]
	if i := strings.IndexByte(to, ':'); i >= 0 {
		to = to[:i]
	}
	return parts[0], strings.TrimSpace(to), true
}

// DecisionLatency measures consensus call opened -> closed.
//
// THE OPEN-CALL TRAP. An open call is INSERTED with `closes_at = now()` -- the
// same instant as opens_at -- because the column is NOT NULL
// (consensus.go:141). So `closes_at - opens_at` over every call yields 0.0 hours
// for each open call, which is the "zero-day" lie the spec's §3.2 rule exists to
// prevent. The filter is `status = 'closed'`; every other status is unfinished.
//
// The schema allows exactly two statuses -- `CHECK (status IN ('open','closed'))`
// (0014_consensus_call_question.sql:57) -- so in practice the filter separates
// 'closed' from 'open'. There is no 'superseded' status, contrary to what a
// reading of the spec's prose might suggest.
func (d *DB) DecisionLatency(ctx context.Context) (discovery.Latency, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT opens_at, closes_at, status FROM consensus_calls`)
	if err != nil {
		return discovery.Latency{}, err
	}
	defer rows.Close()

	var episodes []discovery.Episode
	for rows.Next() {
		var opensAt, closesAt float64
		var status string
		if err := rows.Scan(&opensAt, &closesAt, &status); err != nil {
			return discovery.Latency{}, err
		}
		ep := discovery.Episode{StartedAt: opensAt, HasStart: true}
		if status == "closed" {
			ep.EndedAt = closesAt
			ep.HasEnd = true
		}
		episodes = append(episodes, ep)
	}
	return discovery.Measure(episodes), rows.Err()
}
