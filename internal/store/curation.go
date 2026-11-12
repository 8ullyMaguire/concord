package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CurationItem is one row of the review queue: a proposal of any kind, or a moderation act.
//
// The shape is a projection, not a model. Each kind keeps its own table and its own rules; this
// exists so one page can list them and one filter set can narrow them.
type CurationItem struct {
	Kind        string  `json:"kind"` // taxonomy | strategy | act
	ID          int64   `json:"id"`
	ProjectID   int64   `json:"project_id"`
	ProjectSlug string  `json:"project_slug"`
	ActorID     int64   `json:"actor_id"`
	Action      string  `json:"action"`
	Target      string  `json:"target"`
	Summary     string  `json:"summary"`
	State       string  `json:"state"` // pending | ratified | rejected | withdrawn | applied | undone
	OpenedAt    float64 `json:"opened_at"`
	DecidedAt   float64 `json:"decided_at"`
	Yes         int     `json:"yes"`
	No          int     `json:"no"`
	// Contested is derived, never stored: it is the instance's existing answer to "how many
	// people disagree" (CapDisputeQuorum), not a second threshold invented for this page.
	Contested bool `json:"contested"`
	MyVote    int  `json:"my_vote"` // 1, -1, or 0
	// CanVote is filled by the HTTP layer, not here: eligibility depends on the request's
	// actor and on rules (trust level, project role) the store does not own. A page that
	// renders the button regardless offers an action the server will refuse, and the refusal
	// reads as a broken page rather than as a rule.
	CanVote bool `json:"can_vote"`
}

// CurationPage is one page of the queue plus the position after it.
type CurationPage struct {
	Items      []CurationItem `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
	Limit      int            `json:"limit"`
}

// CurationFilter narrows the queue. The zero value matches everything, which is what the log
// view wants; the queue view sets State and UnvotedBy.
type CurationFilter struct {
	Kind      string
	State     string
	ProjectID int64
	ActorID   int64
	Text      string
	Since     float64
	Until     float64
	// UnvotedBy restricts the list to rows this reader has taken no position on. Their own
	// proposals are excluded too: a queue that shows you your own work never empties.
	UnvotedBy int64
	// ViewerID fills MyVote. It does not filter.
	ViewerID int64
	Cursor   string
	Limit    int
}

// CurationMaxLimit is the ceiling ListCuration clamps to, and it reports what it used.
const CurationMaxLimit = 100

// CurationStateOpen is the queue's default filter: proposals still pending, acts still applied.
// It is a filter value, not a value any row holds -- see ListCuration.
const CurationStateOpen = "open"

// curationActions are the audit_log actions the queue lists. A whitelist rather than a LIKE so
// an unrelated future action cannot appear in a review queue by naming itself "arena.something".
const curationActions = `'arena.remove','arena.restore','feature.reject','feature.unreject'`

// curationQueueSQL projects every kind into one shape. Kept as a constant because the UNION's
// column order IS the scan order below, and the two must be read together to be checked.
//
// The first two parameters are the viewer's id, twice: once for the act branch's vote value and
// once for the "has this reader consented" test on a proposal. Both are ? rather than a repeated
// literal so the caller cannot forget one.
const curationQueueSQL = `
WITH queue AS (
    SELECT 'taxonomy' AS kind, p.id AS id, COALESCE(p.project_id, 0) AS project_id,
           p.proposed_by AS actor_id, p.action AS action,
           p.target_tag AS target, COALESCE(p.rationale, '') AS summary,
           p.status AS state, p.created_at AS opened_at, COALESCE(p.decided_at, 0) AS decided_at,
           (SELECT COUNT(*) FROM taxonomy_consents c WHERE c.proposal_id = p.id) AS yes_count,
           0 AS no_count
      FROM taxonomy_proposals p
    UNION ALL
    SELECT 'strategy', s.id, s.project_id, s.proposed_by, 'weight',
           'feature:' || s.feature_id, COALESCE(s.rationale, ''),
           s.status, s.created_at, COALESCE(s.decided_at, 0),
           (SELECT COUNT(*) FROM strategic_weight_consents c WHERE c.proposal_id = s.id),
           0
      FROM strategic_weight_proposals s
    UNION ALL
    SELECT 'act', a.id, COALESCE(a.project_id, 0), COALESCE(a.actor_id, 0),
           a.action,
           COALESCE(a.entity, '') || ':' || COALESCE(a.entity_id, 0),
           COALESCE(a.detail, ''),
           CASE WHEN EXISTS (SELECT 1 FROM audit_log u WHERE u.undoes = a.id) THEN 'undone'
                ELSE 'applied' END,
           a.created_at, 0,
           (SELECT COUNT(*) FROM act_reviews r WHERE r.audit_id = a.id AND r.vote = 1),
           (SELECT COUNT(*) FROM act_reviews r WHERE r.audit_id = a.id AND r.vote = -1)
      FROM audit_log a
     WHERE a.action IN (` + curationActions + `)
)
SELECT q.kind, q.id, q.project_id, COALESCE(pr.slug, ''), q.actor_id, q.action, q.target,
       q.summary, q.state, q.opened_at, q.decided_at, q.yes_count, q.no_count,
       CASE
         WHEN q.kind = 'act' THEN COALESCE(
              (SELECT r.vote FROM act_reviews r WHERE r.audit_id = q.id AND r.user_id = ?), 0)
         WHEN EXISTS (SELECT 1 FROM curation_votes x
                       WHERE x.kind = q.kind AND x.ref_id = q.id AND x.user_id = ?) THEN 1
         ELSE 0
       END AS my_vote
  FROM queue q
  LEFT JOIN projects pr ON pr.id = q.project_id
`

// ListCuration returns one page of the queue.
func (d *DB) ListCuration(ctx context.Context, f CurationFilter) (CurationPage, error) {
	limit := f.Limit
	if limit <= 0 || limit > CurationMaxLimit {
		limit = 20
	}
	where := []string{"1=1"}
	args := []any{f.ViewerID, f.ViewerID}
	if f.Kind != "" {
		where = append(where, "q.kind = ?")
		args = append(args, f.Kind)
	}
	// "open" is the queue's default, and it is not a state any row carries: a proposal is open
	// while it is `pending`, and an act is open while it is `applied` -- an act has no pending
	// phase, it exists only after somebody did it. A page whose default filter was the literal
	// "pending" would show every proposal and no act at all, which is the half of this queue
	// the owner asked for.
	if f.State == CurationStateOpen {
		where = append(where, `((q.kind = 'act' AND q.state = 'applied') OR
			(q.kind <> 'act' AND q.state = 'pending'))`)
	} else if f.State != "" {
		where = append(where, "q.state = ?")
		args = append(args, f.State)
	}
	if f.ProjectID != 0 {
		where = append(where, "q.project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.ActorID != 0 {
		where = append(where, "q.actor_id = ?")
		args = append(args, f.ActorID)
	}
	if f.Text != "" {
		like := "%" + f.Text + "%"
		where = append(where, "(q.summary LIKE ? OR q.target LIKE ? OR q.action LIKE ?)")
		args = append(args, like, like, like)
	}
	if f.Since != 0 {
		where = append(where, "q.opened_at >= ?")
		args = append(args, f.Since)
	}
	if f.Until != 0 {
		where = append(where, "q.opened_at <= ?")
		args = append(args, f.Until)
	}
	if f.UnvotedBy != 0 {
		where = append(where, "q.actor_id <> ?")
		args = append(args, f.UnvotedBy)
		where = append(where, `NOT EXISTS (SELECT 1 FROM curation_votes v WHERE v.kind = q.kind AND v.ref_id = q.id AND v.user_id = ?)`)
		args = append(args, f.UnvotedBy)
	}
	if f.Cursor != "" {
		c, err := decodeCurationCursor(f.Cursor)
		if err != nil {
			return CurationPage{}, err
		}
		// Strictly after the cursor in (opened_at DESC, kind ASC, id ASC) scan order.
		where = append(where, `(q.opened_at < ? OR (q.opened_at = ? AND
			(q.kind > ? OR (q.kind = ? AND q.id > ?))))`)
		args = append(args, c.At, c.At, c.Kind, c.Kind, c.ID)
	}

	q := curationQueueSQL + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY q.opened_at DESC, q.kind ASC, q.id ASC LIMIT ?"
	args = append(args, limit+1)

	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return CurationPage{}, fmt.Errorf("list curation: %w", err)
	}
	defer rows.Close()

	items := []CurationItem{}
	for rows.Next() {
		var it CurationItem
		if err := rows.Scan(&it.Kind, &it.ID, &it.ProjectID, &it.ProjectSlug, &it.ActorID,
			&it.Action, &it.Target, &it.Summary, &it.State, &it.OpenedAt, &it.DecidedAt,
			&it.Yes, &it.No, &it.MyVote); err != nil {
			return CurationPage{}, fmt.Errorf("scan curation row: %w", err)
		}
		it.Contested = it.No >= CapDisputeQuorum
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return CurationPage{}, err
	}

	page := CurationPage{Items: items, Limit: limit}
	if len(items) > limit {
		last := items[limit-1]
		page.Items = items[:limit]
		page.NextCursor = encodeCurationCursor(last)
	}
	return page, nil
}

// curationCursor is (opened_at, kind, id): the sort key of the queue, in full.
//
// ledger.Cursor is (at, id) and cannot express this order, so the tuple is repeated here rather
// than bent into that one -- two kinds can share an id and a millisecond, and a page boundary
// landing exactly there would drop a row.
type curationCursor struct {
	At   float64
	Kind string
	ID   int64
}

func encodeCurationCursor(it CurationItem) string {
	return strconv.FormatFloat(it.OpenedAt, 'f', 6, 64) + "|" + it.Kind + "|" + strconv.FormatInt(it.ID, 10)
}

// ErrMalformedCurationCursor is returned rather than a silent first page, because a silent
// first page turns a paging bug into an infinite scroll.
var ErrMalformedCurationCursor = fmt.Errorf("%w: this cursor is not a position in the queue", ErrInvalid)

func decodeCurationCursor(raw string) (curationCursor, error) {
	parts := strings.Split(raw, "|")
	if len(parts) != 3 {
		return curationCursor{}, ErrMalformedCurationCursor
	}
	at, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return curationCursor{}, ErrMalformedCurationCursor
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || parts[1] == "" {
		return curationCursor{}, ErrMalformedCurationCursor
	}
	return curationCursor{At: at, Kind: parts[1], ID: id}, nil
}

// ActTarget resolves a moderation act for a permission check: which action, in which project,
// by whom. The queue needs the project id to ask whether a reader may review that project.
func (d *DB) ActTarget(ctx context.Context, auditID int64) (action string, projectID, actorID int64, err error) {
	var pid, aid sql.NullInt64
	err = d.QueryRowContext(ctx,
		`SELECT action, project_id, actor_id FROM audit_log WHERE id = ?`, auditID).
		Scan(&action, &pid, &aid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, 0, fmt.Errorf("%w: act %d", ErrNotFound, auditID)
	}
	if err != nil {
		return "", 0, 0, err
	}
	return action, pid.Int64, aid.Int64, nil
}

// ReviewAct records an endorse (1) or a contest (-1) of one moderation act.
//
// Changing your mind replaces your row rather than adding another: the question the queue asks
// is "where does this reader stand now", and two rows for one reader would make the count lie.
func (d *DB) ReviewAct(ctx context.Context, auditID, userID int64, vote int, rationale string) (int, int, error) {
	if vote != 1 && vote != -1 {
		return 0, 0, fmt.Errorf("%w: a review is endorse (1) or contest (-1)", ErrInvalid)
	}
	action, _, actorID, err := d.ActTarget(ctx, auditID)
	if err != nil {
		return 0, 0, err
	}
	if !isCurationAction(action) {
		return 0, 0, fmt.Errorf("%w: %q is not a moderation act", ErrInvalid, action)
	}
	if actorID == userID {
		return 0, 0, fmt.Errorf("%w: an actor cannot review their own act", ErrPerm)
	}
	now := float64(time.Now().Unix())
	if _, err := d.ExecContext(ctx, `
		INSERT INTO act_reviews (audit_id, user_id, vote, rationale, at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(audit_id, user_id) DO UPDATE SET
			vote = excluded.vote, rationale = excluded.rationale, at = excluded.at`,
		auditID, userID, vote, strings.TrimSpace(rationale), now); err != nil {
		return 0, 0, fmt.Errorf("record act review: %w", err)
	}
	var yes, no int
	if err := d.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(vote = 1), 0), COALESCE(SUM(vote = -1), 0)
		  FROM act_reviews WHERE audit_id = ?`, auditID).Scan(&yes, &no); err != nil {
		return 0, 0, fmt.Errorf("count act reviews: %w", err)
	}
	return yes, no, nil
}

// ModerationActions is the act vocabulary from docs/specs/moderation-spec.md §5, minus
// `moderation.undo`, which is a dispatch over the act it names rather than an act of its own.
//
// ONE list, in one place: the curation queue's whitelist, the audit viewer's `kind=moderation`
// filter and the undo control all have to agree about what a moderation act is, and a second
// copy drifts the first time an act is added.
var moderationActions = []string{
	"arena.remove", "arena.restore", "feature.reject", "feature.unreject",
}

// ModerationActions returns a copy, so a caller cannot reorder the package's vocabulary by
// sorting the slice it was handed.
func ModerationActions() []string {
	return append([]string(nil), moderationActions...)
}

// IsModerationAction reports whether an audit action names a moderation act.
func IsModerationAction(action string) bool {
	for _, a := range moderationActions {
		if a == action {
			return true
		}
	}
	return false
}

// isCurationAction reports whether an audit action is one the queue lists and reviewers may
// review. One function so the SQL whitelist and the store check cannot disagree.
func isCurationAction(action string) bool {
	return IsModerationAction(action)
}

// VoteOnCuration is the write side of the queue: it dispatches to the function that already
// decides each kind.
//
// It does not re-implement ratification. RatifyTaxonomyProposal and
// RatifyStrategicWeightProposal own their eligibility rules, their self-consent refusal and
// their charter thresholds, and a second copy behind a generic endpoint is how a permission
// ends up enforced on one path and not the other.
//
// A consent that is recorded but has not reached quorum comes back as ErrConflict from those
// functions. That is not a failure for this caller -- the vote landed -- so it is passed
// through for the handler to answer 202 instead of 409.
func (d *DB) VoteOnCuration(ctx context.Context, kind string, refID, userID int64, vote int, rationale string) (int, int, error) {
	switch kind {
	case "taxonomy":
		if vote != 1 {
			return 0, 0, fmt.Errorf("%w: a taxonomy proposal takes consent, not a no vote", ErrInvalid)
		}
		if _, err := d.RatifyTaxonomyProposal(ctx, refID, userID); err != nil && !errors.Is(err, ErrConflict) {
			return 0, 0, err
		}
		n, err := d.taxonomyConsents(ctx, refID)
		return n, 0, err
	case "strategy":
		if vote != 1 {
			return 0, 0, fmt.Errorf("%w: a weight proposal takes consent, not a no vote", ErrInvalid)
		}
		if _, err := d.RatifyStrategicWeightProposal(ctx, refID, userID); err != nil && !errors.Is(err, ErrConflict) {
			return 0, 0, err
		}
		n, err := d.strategyProposalConsents(ctx, refID)
		return n, 0, err
	case "act":
		return d.ReviewAct(ctx, refID, userID, vote, rationale)
	}
	return 0, 0, fmt.Errorf("%w: unknown curation kind %q", ErrInvalid, kind)
}
