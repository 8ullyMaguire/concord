# The curation queue: one filtered place to review proposals and moderation acts

**Date:** 2026-11-11 · **Repo:** concord · **Status:** plan only, nothing implemented.
**Depends on:** M1 of `docs/plans/moderation.md` (shipped, commit `4c8be43`).
**Supersedes:** nothing. Extends `docs/specs/moderation-spec.md` §7 (the modlog page) with the
queue half of what the owner asked for.

## 1. Goal

One page — `/curation` — that lists every open proposal and every moderation act with the
filters a reviewer needs, and is the place where they vote: consent to a taxonomy or strategic
weight change, endorse or contest a moderation act.

## 2. What the donor actually does (measured in `~/code-local/go/stash-box`, 2026-11-11)

Stash-box separates a **queue** from a **log** over the same records.

| behaviour | where |
|---|---|
| `/edits` opens on `status=pending`, `voted=not_voted`, bots excluded, user-submitted only, **oldest first** ("A queue is worked from the end that has been waiting longest") | `frontend/src/pages/edits/Edits.tsx:10-22` |
| the filter set: `target_type`, `target_id`, `status`, `operation`, `voted`, `user_id`, `is_favorite`, `is_bot`, `include_user_submitted`, `sort`, `direction`, page/per_page | `frontend/src/components/list/EditList.tsx:73-105` |
| a vote applies the edit at a threshold — default **3**, with `voting_period`, `min_destructive_voting_period` and a cron that applies when met | `internal/config/config.go:93`, `internal/service/edit/service.go:1362,1672,1745` |
| destructive operations need a longer voting period than additive ones | `min_destructive_voting_period`, same config block |
| the applied history is a separate page (`/audits`) with its own filters | `frontend/src/pages/audits/Audits.tsx` |

Three properties are worth copying, and one is worth refusing:

- **copy:** the queue defaults to work-to-do (pending, not voted by me), not to everything;
- **copy:** one list, many target types, filtered rather than split into a page per type;
- **copy:** the log and the queue are the *same records* under different filters — not two
  storages;
- **refuse:** a cron that applies a change when a threshold is met. Concord already decides
  proposals synchronously at the moment of the deciding consent
  (`store.RatifyTaxonomyProposal`, `taxonomy.go:271`), and a second, asynchronous applier would
  be a second truth about when a change took effect.

## 3. What concord already has (measured, not remembered)

| thing | where | state |
|---|---|---|
| taxonomy proposals + consents + synchronous ratification at a charter quorum, with self-consent and eligibility refused | `internal/store/taxonomy.go:98,225,271,549`; table `taxonomy_proposals` + `taxonomy_consents` (`migrations/0015_tag_taxonomy_quorum.sql:42,76`) | working, has a page (`/taxonomy`) |
| strategic-weight proposals + consents + ratification | `internal/store/strategy.go:92,148,177,297`; table `strategic_weight_proposals` + `strategic_weight_consents` (`0012:28,62`) | working, **no page of its own** |
| a third proposal kind (strategic themes) | `0012`/`0028`, `strategic_theme_proposals` + consents | working, out of scope here (§8) |
| capability claims with confirm/dispute quorum (`CapQuorum=2`, `CapDisputeQuorum=2`, one row per user, author excluded) | `internal/store/capabilities.go:37-68`, `0021_capabilities.sql:57` | working |
| moderation acts in `audit_log` (`arena.remove`, `arena.restore`), reversible, append-only | `internal/store/moderation.go`, commit `4c8be43` | shipped today, **not reviewable** |
| a filtered reader over the log | `store.ListAudit` (`internal/store/audit.go:91`), `GET /api/v1/projects/{slug}/audit` (`internal/httpapi/audit.go:17`) | action/actor/entity/text/since + **offset** paging |
| the nav and page registry | `internal/httpapi/server.go:242` (`pages`), `templates/base.html:74` | one line each to extend |

**The gap, in one sentence:** the three proposal kinds each have their own store and no shared
list, so nobody can see the instance's review work in one place; two of the three have no page at
all; and a moderation act has no way to be *reviewed* — only undone, which is a write.

## 4. Approach

Add a read model (`store.ListCuration`) that UNIONs the existing proposal tables and the
moderation acts into one row shape, expose it at `GET /api/v1/curation` with the donor's filter
set and a keyset cursor, and give it a page with a **Queue / Log** toggle. Votes dispatch to the
functions that already decide proposals (`RatifyTaxonomyProposal`, `RatifyStrategicWeightProposal`)
instead of re-implementing them; the only new state is `act_reviews`, one row per reviewer per
act, which records endorse/contest and changes nothing automatically.

No new proposal kind, no duplicated vote counting, no second applier.

## 5. Data model — `internal/db/migrations/0035_curation_reviews.sql`

```sql
-- Reviews of a moderation act: endorse or contest.
--
-- M1's acts are unilateral and reversible, and neither of those makes them REVIEWABLE: a
-- reader who believes a removal was wrong can only undo it, which is a write, and there is
-- nowhere to record "I looked at this and disagreed" without doing one. This table is that
-- record.
--
-- It deliberately applies nothing. A contest raises the act in the queue and marks it
-- contested; it does not reverse it, because an automatic reversal at quorum would hand
-- every removal to whoever organises three people first. Reversal stays a deliberate act
-- with a named actor in the log.
CREATE TABLE act_reviews (
    audit_id  INTEGER NOT NULL REFERENCES audit_log(id) ON DELETE CASCADE,
    user_id   INTEGER NOT NULL REFERENCES users(id),
    vote      INTEGER NOT NULL CHECK (vote IN (1,-1)),
    rationale TEXT NOT NULL DEFAULT '',
    at        REAL NOT NULL,
    PRIMARY KEY (audit_id, user_id)
);

CREATE INDEX idx_act_reviews_audit ON act_reviews(audit_id);

-- "Who has already voted on what", over every vote-backed kind in the queue.
--
-- The queue asks two questions of every row -- how many said yes, and has this reader said
-- anything -- and without this view each question is written once per proposal table, with
-- a different id column name each time. A VIEW, not a table: the consent rows are the truth,
-- and a copy here would be a second place to fall out of step with a ratification.
CREATE VIEW curation_votes AS
    SELECT 'taxonomy' AS kind, proposal_id AS ref_id, user_id FROM taxonomy_consents
    UNION ALL
    SELECT 'strategy', proposal_id, user_id FROM strategic_weight_consents
    UNION ALL
    SELECT 'act', audit_id, user_id FROM act_reviews;
```

**Verified against the tree:** `strategic_weight_consents(proposal_id, user_id, consented_at)`
(`0012:62`), `taxonomy_consents(proposal_id, user_id, consented_at)` (`0015:76`),
`audit_log(id, project_id, actor_id, action, entity, entity_id, detail, created_at)` (`0001:338`).
The newest migration is `0034`; this one is `0035`.

## 6. Steps

Each step is one commit. Run the verification command from the repo root
(`/personal/documents/code/projects/concord` on thinkcentre); the expected output is quoted
exactly.

### Step 1 — the migration

Create the file in §5 above at `internal/db/migrations/0035_curation_reviews.sql`.

**Verify:**

```bash
go test ./internal/db/ -run TestMigrate -count=1
```

Expected: `ok  	git.polarisocial.xyz/concord/concord/internal/db`.

Then prove the view answers:

```bash
ssh thinkcentre 'D=/tmp/curation-check.db; rm -f $D; cd /personal/documents/code/projects/concord && for f in internal/db/migrations/*.sql; do sqlite3 $D < $f || break; done; sqlite3 $D "select kind, count(*) from curation_votes group by 1;"'
```

Expected: no output (all three kinds empty), and no error. A view that does not compile fails
here with "no such table: taxonomy_consents" and you have mistyped a column.

**Commit:** `db: act reviews, and one view that answers "has this reader voted"`.

### Step 2 — the read model, failing test first

Create `internal/store/curation_test.go`:

```go
package store

import (
	"context"
	"testing"
)

// The queue is a union of three tables that were never designed to be listed together, so the
// first thing to pin is that each kind survives the projection with its own action and target
// intact -- a UNION that silently projected NULLs would still "return three rows".
func TestCurationListsEveryKindNewestFirst(t *testing.T) {
	d, uid, pid, fID := arenaFixture(t)
	ctx := context.Background()

	if _, err := d.ProposeStrategicWeight(ctx, pid, fID, uid, 2.5, "the release theme"); err == nil {
		// Proposing as the owner is allowed; a second proposal for the same feature is not.
		if _, err := d.ProposeStrategicWeight(ctx, pid, fID, uid, 3.0, "again"); err == nil {
			t.Fatal("a second pending proposal for the same feature was accepted")
		}
	}
	if _, err := d.ProposeTaxonomyChange(ctx, 0, uid, "create", "topic:kanban", "", "the board's vocabulary"); err != nil {
		t.Fatalf("ProposeTaxonomyChange: %v", err)
	}
	a, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	if err := d.UpsertArenaEntry(ctx, a.ID, "feature", fID); err != nil {
		t.Fatalf("UpsertArenaEntry: %v", err)
	}
	if _, err := d.RemoveFeatureFromArena(ctx, pid, a.ID, fID, uid, "spam"); err != nil {
		t.Fatalf("RemoveFeatureFromArena: %v", err)
	}

	items, err := d.ListCuration(ctx, CurationFilter{})
	if err != nil {
		t.Fatalf("ListCuration: %v", err)
	}
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.Kind] = true
		if it.Summary == "" {
			t.Errorf("%s/%d has no summary: the union projected a NULL where the row has text", it.Kind, it.ID)
		}
		if it.ID == 0 {
			t.Errorf("%s item has no id", it.Kind)
		}
	}
	for _, kind := range []string{"taxonomy", "strategy", "act"} {
		if !seen[kind] {
			t.Errorf("kind %q is missing from the queue", kind)
		}
	}
	for i := 1; i < len(items); i++ {
		if items[i].OpenedAt > items[i-1].OpenedAt {
			t.Fatalf("not newest first at %d: %v after %v", i, items[i].OpenedAt, items[i-1].OpenedAt)
		}
	}
}

// "Not voted by me" is the queue's default in the donor, and it is the difference between a
// work list and a wall. It must exclude a reader's own proposal too, or the queue never empties.
func TestCurationCanExcludeWhatIHaveAlreadyVotedOn(t *testing.T) {
	d, uid, pid, fID := arenaFixture(t)
	ctx := context.Background()
	p, err := d.ProposeStrategicWeight(ctx, pid, fID, uid, 2.0, "why")
	if err != nil {
		t.Fatalf("ProposeStrategicWeight: %v", err)
	}
	other, err := d.CreateUser(ctx, "reviewer-curation", "Reviewer")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := d.RatifyStrategicWeightProposal(ctx, p.ID, other.ID); err != nil {
		// It may not ratify alone; the consent row is still written, which is what this checks.
		t.Logf("ratify answered %v (expected: not enough consents yet)", err)
	}
	unvoted, err := d.ListCuration(ctx, CurationFilter{UnvotedBy: other.ID})
	if err != nil {
		t.Fatalf("ListCuration(unvoted): %v", err)
	}
	for _, it := range unvoted {
		if it.Kind == "strategy" && it.ID == p.ID {
			t.Fatal("a proposal the reader consented to is still in their work list")
		}
	}
}

// A cursor that is really an offset repeats or skips rows the moment a new act lands. This
// inserts one between two page reads and asserts the union of the pages is exactly the rows
// that existed when paging started.
func TestCurationCursorDoesNotRepeatOrSkip(t *testing.T) {
	d, uid, pid, fID := arenaFixture(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := d.ProposeTaxonomyChange(ctx, 0, uid, "create", "topic:tag"+itoa(i), "", "r"); err != nil {
			t.Fatalf("propose %d: %v", i, err)
		}
	}
	first, err := d.ListCuration(ctx, CurationFilter{Limit: 2})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(first) != 2 || first.NextCursor == "" {
		t.Fatalf("page 1 returned %d items, cursor %q", len(first), first.NextCursor)
	}
	if _, err := d.ProposeTaxonomyChange(ctx, 0, uid, "create", "topic:late", "", "r"); err != nil {
		t.Fatalf("propose late: %v", err)
	}
	seen := map[string]bool{}
	all := append([]CurationItem{}, first.Items...)
	for _, it := range first.Items {
		seen[it.Kind+"/"+itoa(int(it.ID))] = true
	}
	cursor := first.NextCursor
	for page := 0; page < 5 && cursor != ""; page++ {
		p, err := d.ListCuration(ctx, CurationFilter{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", page+2, err)
		}
		for _, it := range p.Items {
			k := it.Kind + "/" + itoa(int(it.ID))
			if seen[k] {
				t.Fatalf("row %s came back twice across pages", k)
			}
			seen[k] = true
		}
		all = append(all, p.Items...)
		cursor = p.NextCursor
	}
	if len(all) != 5 {
		t.Fatalf("paged through %d rows, want the 5 that existed when paging began", len(all))
	}
	_ = pid
	_ = fID
}
```

`itoa` exists in the package's tests (`internal/store/*_test.go` uses it); if the compiler says
otherwise, use `strconv.Itoa` and add the import.

**Run it and watch it fail:**

```bash
go test ./internal/store/ -run TestCuration -count=1
```

Expected: `undefined: CurationFilter`, `undefined: d.ListCuration` — a build failure, which is the
red state for a package-level API.

### Step 3 — implement the read model

Create `internal/store/curation.go`:

```go
package store

import (
	"context"
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
	Kind        string  `json:"kind"`   // taxonomy | strategy | act
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
	MyVote      int     `json:"my_vote"` // 1, -1, or 0
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

// curationQueueSQL projects every kind into one shape. Kept as a constant because the UNION's
// column order IS the scan order below, and the two must be read together to be checked.
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
     WHERE a.action IN ('arena.remove', 'arena.restore', 'feature.reject', 'feature.unreject')
)
SELECT q.kind, q.id, q.project_id, COALESCE(pr.slug, ''), q.actor_id, q.action, q.target,
       q.summary, q.state, q.opened_at, q.decided_at, q.yes_count, q.no_count,
       COALESCE((SELECT v.vote FROM curation_votes x
                    JOIN act_reviews v ON v.audit_id = x.ref_id AND v.user_id = x.user_id
                  WHERE x.kind = q.kind AND x.ref_id = q.id AND x.user_id = ?
                  LIMIT 1), 0)
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
	args := []any{f.ViewerID}
	add := func(clause string, v any) {
		where = append(where, clause)
		args = append(args, v)
	}
	if f.Kind != "" {
		add("q.kind = ?", f.Kind)
	}
	if f.State != "" {
		add("q.state = ?", f.State)
	}
	if f.ProjectID != 0 {
		add("q.project_id = ?", f.ProjectID)
	}
	if f.ActorID != 0 {
		add("q.actor_id = ?", f.ActorID)
	}
	if f.Text != "" {
		like := "%" + f.Text + "%"
		where = append(where, "(q.summary LIKE ? OR q.target LIKE ? OR q.action LIKE ?)")
		args = append(args, like, like, like)
	}
	if f.Since != 0 {
		add("q.opened_at >= ?", f.Since)
	}
	if f.Until != 0 {
		add("q.opened_at <= ?", f.Until)
	}
	if f.UnvotedBy != 0 {
		add("q.actor_id <> ?", f.UnvotedBy)
		where = append(where,
			`NOT EXISTS (SELECT 1 FROM curation_votes v WHERE v.kind = q.kind AND v.ref_id = q.id AND v.user_id = ?)`)
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
// ledger.Cursor is (at, id) and cannot express this order, so the tuple is repeated here
// rather than bent into that one -- two kinds can share an id and a millisecond, and a page
// boundary landing exactly there would drop a row.
type curationCursor struct {
	At   float64
	Kind string
	ID   int64
}

func encodeCurationCursor(it CurationItem) string {
	return strconv.FormatFloat(it.OpenedAt, 'f', 6, 64) + "|" + it.Kind + "|" + strconv.FormatInt(it.ID, 10)
}

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

// ActTarget resolves a moderation act for a permission check: who did it, in which project,
// and whether it is one of the acts the queue lists.
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
	if action != "arena.remove" && action != "arena.restore" &&
		action != "feature.reject" && action != "feature.unreject" {
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
```

Add `"database/sql"` and `"errors"` to the imports (used by `ActTarget`).

Two things in this code are deliberate and must not be "cleaned up":

1. `MyVote` is filled by a correlated subquery over `curation_votes` in the projection, not by a
   second round trip, so a page of 20 rows is one query.
2. `limit+1` is fetched and the extra row discarded: "is there another page" is answered by the
   data rather than by counting.

**Verify:**

```bash
gofmt -l internal/store && go vet ./internal/store/ && go test ./internal/store/ -run TestCuration -count=1
```

Expected: no file listed by `gofmt -l`, then `ok  	git.polarisocial.xyz/concord/concord/internal/store`.
If `TestCurationCursorDoesNotRepeatOrSkip` fails with "paged through 6 rows", the cursor predicate
is `>=` where it should be `>`.

**Commit:** `store: one queue over proposals and acts, with a cursor`.

### Step 4 — the vote dispatcher

Append to `internal/store/curation.go`:

```go
// VoteOnCuration is the write side of the queue: it dispatches to the function that already
// decides each kind.
//
// It does not re-implement ratification. RatifyTaxonomyProposal and
// RatifyStrategicWeightProposal own their eligibility rules, their self-consent refusal and
// their charter thresholds, and a second copy of those rules behind a generic endpoint is how
// a permission ends up enforced on one path and not the other.
func (d *DB) VoteOnCuration(ctx context.Context, kind string, refID, userID int64, vote int, rationale string) (int, int, error) {
	switch kind {
	case "taxonomy":
		if vote != 1 {
			return 0, 0, fmt.Errorf("%w: a taxonomy proposal takes consent, not a no vote", ErrInvalid)
		}
		if _, err := d.RatifyTaxonomyProposal(ctx, refID, userID); err != nil {
			return 0, 0, err
		}
		p, err := d.GetTaxonomyProposal(ctx, refID)
		if err != nil {
			return 0, 0, err
		}
		n, err := d.taxonomyConsents(ctx, refID)
		return n, 0, err
	case "strategy":
		if vote != 1 {
			return 0, 0, fmt.Errorf("%w: a weight proposal takes consent, not a no vote", ErrInvalid)
		}
		if _, err := d.RatifyStrategicWeightProposal(ctx, refID, userID); err != nil {
			return 0, 0, err
		}
		n, err := d.strategyProposalConsents(ctx, refID)
		return n, 0, err
	case "act":
		return d.ReviewAct(ctx, refID, userID, vote, rationale)
	}
	return 0, 0, fmt.Errorf("%w: unknown curation kind %q", ErrInvalid, kind)
}
```

Note: `RatifyTaxonomyProposal` returns `ErrConflict` when the consent is recorded but the quorum
is not met yet, and *that is not an error for the caller* — the vote landed. The handler in Step 5
treats `ErrConflict` as "recorded, not yet decided" and answers 202.

**Verify:**

```bash
go test ./internal/store/ -run 'TestCuration|TestReview' -count=1
```

Expected: `ok`. Add a review test before moving on:

```go
// Two reviews from one reader must collapse to one row, and an actor must not review their own
// act -- the count is the only thing the queue shows, so a double row is a lie about agreement.
func TestReviewingAnActIsOneRowPerReader(t *testing.T) {
	d, uid, pid, fID := arenaFixture(t)
	ctx := context.Background()
	a, _ := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err := d.UpsertArenaEntry(ctx, a.ID, "feature", fID); err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	act, err := d.RemoveFeatureFromArena(ctx, pid, a.ID, fID, uid, "spam")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	other, _ := d.CreateUser(ctx, "reviewer-act", "Reviewer")
	if yes, no, err := d.ReviewAct(ctx, act.ID, other.ID, 1, "agreed"); err != nil || yes != 1 || no != 0 {
		t.Fatalf("first review: yes=%d no=%d err=%v", yes, no, err)
	}
	if yes, no, err := d.ReviewAct(ctx, act.ID, other.ID, -1, "changed my mind"); err != nil || yes != 0 || no != 1 {
		t.Fatalf("after changing their mind: yes=%d no=%d err=%v", yes, no, err)
	}
	if _, _, err := d.ReviewAct(ctx, act.ID, uid, 1, "self"); !errors.Is(err, ErrPerm) {
		t.Fatalf("self review: got %v, want ErrPerm", err)
	}
}
```

**Commit:** `store: votes on the queue dispatch to the deciders that already exist`.

### Step 5 — the API

Create `internal/httpapi/curation.go`:

```go
package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// handleListCuration serves GET /api/v1/curation.
//
// Every query parameter is optional and a malformed one narrows the list rather than failing
// the request, matching handleListProjectAudit: this is a surface a person types into.
func (s *Server) handleListCuration(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.CurationFilter{
		Kind:  strings.TrimSpace(q.Get("kind")),
		State: strings.TrimSpace(q.Get("state")),
		Text:  strings.TrimSpace(q.Get("q")),
	}
	if v := q.Get("project"); v != "" {
		if p, err := s.Store.GetProject(r.Context(), v); err == nil {
			f.ProjectID = p.ID
		}
	}
	if n, err := strconv.ParseInt(q.Get("actor_id"), 10, 64); err == nil {
		f.ActorID = n
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil {
		f.Limit = n
	}
	if n, err := strconv.ParseFloat(q.Get("since"), 64); err == nil {
		f.Since = n
	}
	if n, err := strconv.ParseFloat(q.Get("until"), 64); err == nil {
		f.Until = n
	}
	if q.Get("unvoted") == "1" && getActorID(r) != 0 {
		f.UnvotedBy = getActorID(r)
	}
	f.ViewerID = getActorID(r)
	f.Cursor = q.Get("cursor")

	page, err := s.Store.ListCuration(r.Context(), f)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// handleCurationVote serves POST /api/v1/curation/{kind}/{id}/vote.
//
// Consent to a taxonomy or weight proposal is decided by that proposal's own rules, so a
// recorded vote that has not yet reached quorum answers 202 -- the vote landed, the change has
// not. A contest of a moderation act changes nothing but its count, and answers 200.
func (s *Server) handleCurationVote(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the id must be a number"})
		return
	}
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	var req struct {
		Vote      string `json:"vote"`
		Rationale string `json:"rationale"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var vote int
	switch strings.ToLower(strings.TrimSpace(req.Vote)) {
	case "yes", "consent", "endorse", "1":
		vote = 1
	case "no", "contest", "-1":
		vote = -1
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "vote must be yes or no"})
		return
	}

	if kind == "act" {
		_, projectID, if err := s.Store.ActTarget(r.Context(), id); err != nil {
			mapError(w, err)
			return
		}
		if !s.permitModeration(w, r, projectID, "reviewer") {
			return
		}
	}

	yes, no, err := s.Store.VoteOnCuration(r.Context(), kind, id, actorID, vote, req.Rationale)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeJSON(w, http.StatusAccepted, map[string]any{
				"recorded": true, "decided": false, "yes": yes, "no": no,
			})
			return
		}
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recorded": true, "decided": true, "yes": yes, "no": no})
}
```

**Register the routes** in `internal/httpapi/server.go`, immediately after the `/api/v1/taxonomy`
block (around line 807):

```go
	// The curation queue (§4 of docs/plans/curation-queue.md). One read over proposals and
	// moderation acts, and one vote route that dispatches to the deciders that already exist.
	// Instance-wide like the taxonomy it mostly lists: a project's queue is a filter on it.
	r.Route("/api/v1/curation", func(r chi.Router) {
		r.Get("/", s.handleListCuration)
		r.Post("/{kind}/{id}/vote", s.handleCurationVote)
	})
```

**Verify:**

```bash
go build ./... && echo BUILD-OK
go test ./internal/httpapi/ -run TestCuration -count=1
```

Expected: `BUILD-OK`, then `ok` (or "no tests to run", which is fine until Step 7 adds them).

**Commit:** `api: the curation queue, read and vote`.

### Step 6 — the page

Create `internal/httpapi/templates/curation.html`. Copy the head/foot structure of
`templates/taxonomy.html` (35 lines) exactly — it is the closest neighbour: `<div class="page-head">`
with a title and lede, one `<section>`, and a `<script src="{{ asset "/assets/js/curation.js" }}">`.

Body (the part that differs):

```html
<section class="curation-controls">
  <label for="curation-kind">Kind</label>
  <select id="curation-kind">
    <option value="">Everything</option>
    <option value="taxonomy">Taxonomy</option>
    <option value="strategy">Strategic weight</option>
    <option value="act">Moderation act</option>
  </select>

  <label for="curation-state">State</label>
  <select id="curation-state">
    <option value="pending">Waiting for review</option>
    <option value="">Any state</option>
    <option value="ratified">Ratified</option>
    <option value="applied">Applied</option>
    <option value="undone">Undone</option>
  </select>

  <label class="curation-unvoted"><input type="checkbox" id="curation-unvoted" checked>
    Only what I have not voted on</label>

  <label for="curation-q">Text</label>
  <input type="search" id="curation-q" placeholder="tag, feature, reason">

  <button type="button" class="btn btn-secondary" id="curation-more" hidden>Load more</button>
</section>

<p class="note" id="curation-status" hidden></p>
<ul class="curation-list" id="curation-list" aria-live="polite"></ul>
```

Create `internal/httpapi/assets/js/curation.js`:

```js
/*
 * The curation queue (docs/plans/curation-queue.md). One list over taxonomy proposals,
 * strategic-weight proposals and moderation acts, read from GET /api/v1/curation, with the
 * vote posted back to the kind's own route.
 *
 * The queue opens on work-to-do: pending, and nothing this reader has already voted on. That is
 * the donor's default (stash-box's EditsList) and it is the difference between a work list and
 * a wall.
 *
 * Every value from the API is written with textContent or esc(): a rationale is prose typed by
 * a person and must never be parsed as markup.
 */
(function () {
  'use strict';

  var listEl = document.getElementById('curation-list');
  if (!listEl) return;

  var kindEl = document.getElementById('curation-kind');
  var stateEl = document.getElementById('curation-state');
  var unvotedEl = document.getElementById('curation-unvoted');
  var qEl = document.getElementById('curation-q');
  var moreEl = document.getElementById('curation-more');
  var statusEl = document.getElementById('curation-status');

  var cursor = '';
  var seen = 0;

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function say(msg) {
    if (!statusEl) return;
    statusEl.textContent = msg || '';
    statusEl.hidden = !msg;
  }

  function stateLabel(state) {
    return { pending: 'waiting', ratified: 'ratified', rejected: 'rejected',
             withdrawn: 'withdrawn', applied: 'applied', undone: 'undone' }[state] || state;
  }

  function hrefFor(item) {
    if (!item.project_slug) return '';
    if (item.kind === 'act') {
      return '/projects/' + encodeURIComponent(item.project_slug) + '/audit?action=' +
        encodeURIComponent(item.action);
    }
    if (item.kind === 'taxonomy') return '/taxonomy';
    return '/projects/' + encodeURIComponent(item.project_slug) + '/ranking';
  }

  function card(item) {
    var li = document.createElement('li');
    li.className = 'curation-card';
    li.setAttribute('data-kind', item.kind);
    li.setAttribute('data-id', String(item.id));

    var head = document.createElement('p');
    head.className = 'curation-card-head';
    head.textContent = item.kind + ' \u00b7 ' + item.action + ' \u00b7 ' + stateLabel(item.state);
    li.appendChild(head);

    var target = document.createElement('a');
    target.className = 'curation-target';
    target.href = hrefFor(item) || '#';
    target.textContent = item.target || '(no target)';
    li.appendChild(target);

    var why = document.createElement('p');
    why.className = 'curation-summary';
    // The act's detail is JSON; a page that dumps it is unreadable, so pull the reason out and
    // show the rest only when there is no reason to show.
    var text = item.summary || '';
    try {
      var parsed = JSON.parse(text);
      if (parsed && typeof parsed === 'object') text = parsed.reason || text;
    } catch (e) { /* a proposal's summary is plain prose */ }
    why.textContent = text;
    li.appendChild(why);

    var meta = document.createElement('p');
    meta.className = 'curation-meta';
    meta.textContent = item.yes + ' for \u00b7 ' + item.no + ' against' +
      (item.my_vote ? ' \u00b7 you voted ' + (item.my_vote > 0 ? 'yes' : 'no') : '');
    li.appendChild(meta);

    if (item.state === 'pending' || item.state === 'applied' || item.state === 'undone') {
      li.appendChild(voteRow(item));
    }
    return li;
  }

  function voteRow(item) {
    var wrap = document.createElement('p');
    wrap.className = 'curation-votes';

    var yesLabel = item.kind === 'act' ? 'Endorse' : 'Consent';
    var noLabel = item.kind === 'act' ? 'Contest' : 'Oppose';
    var yes = document.createElement('button');
    yes.type = 'button';
    yes.className = 'btn btn-secondary';
    yes.textContent = yesLabel;
    yes.addEventListener('click', function () { vote(item, 'yes'); });
    wrap.appendChild(yes);

    // Only acts have a no vote: a proposal's ratification is a consent count, and offering an
    // "oppose" that the server refuses is a button that teaches a lie.
    if (item.kind === 'act') {
      var no = document.createElement('button');
      no.type = 'button';
      no.className = 'btn btn-secondary';
      no.textContent = noLabel;
      no.addEventListener('click', function () { vote(item, 'no'); });
      wrap.appendChild(no);
    }
    return wrap;
  }

  function vote(item, choice) {
    say('Recording\u2026');
    fetch('/api/v1/curation/' + encodeURIComponent(item.kind) + '/' + item.id + '/vote', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({ vote: choice, rationale: '' })
    }).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (data) {
        if (res.status !== 200 && res.status !== 202) {
          say((data && data.error) || 'Could not record that.');
          return;
        }
        item.my_vote = choice === 'yes' ? 1 : -1;
        item.yes = data.yes || 0;
        item.no = data.no || 0;
        if (res.status === 202) {
          say('Recorded. The change is not decided yet: ' + item.yes + ' of the quorum so far.');
        } else {
          say('Recorded.');
          item.state = item.kind === 'act' ? item.state : 'ratified';
        }
        var el = listEl.querySelector('[data-kind="' + item.kind + '"][data-id="' + item.id + '"]');
        if (el) el.replaceWith(card(item));
      });
    }).catch(function () { say('Could not reach the server.'); });
  }

  function load(append) {
    var params = new URLSearchParams();
    if (kindEl.value) params.set('kind', kindEl.value);
    if (stateEl.value) params.set('state', stateEl.value);
    if (unvotedEl.checked) params.set('unvoted', '1');
    if (qEl.value.trim()) params.set('q', qEl.value.trim());
    if (append && cursor) params.set('cursor', cursor);
    params.set('limit', '20');

    fetch('/api/v1/curation?' + params.toString(), {
      headers: { Accept: 'application/json' }, credentials: 'same-origin'
    }).then(function (res) {
      if (!res.ok) throw new Error('HTTP ' + res.status);
      return res.json();
    }).then(function (page) {
      if (!append) { listEl.innerHTML = ''; seen = 0; }
      (page.items || []).forEach(function (item) {
        listEl.appendChild(card(item));
        seen++;
      });
      cursor = page.next_cursor || '';
      if (moreEl) moreEl.hidden = !cursor;
      if (!seen) {
        say('Nothing is waiting. That is either good news or the filters are too narrow.');
      } else {
        say(seen + ' shown.');
      }
    }).catch(function (err) { say('Could not load the queue: ' + err.message); });
  }

  [kindEl, stateEl, unvotedEl].forEach(function (el) {
    if (el) el.addEventListener('change', function () { load(false); });
  });
  if (qEl) {
    var t = null;
    qEl.addEventListener('input', function () {
      clearTimeout(t);
      t = setTimeout(function () { load(false); }, 300);
    });
  }
  if (moreEl) moreEl.addEventListener('click', function () { load(true); });

  load(false);
})();
```

**Wire it up**, three one-line edits:

1. `internal/httpapi/server.go:242` — add `"curation"` to the `pages` slice:
   ```go
   		"taxonomy", "curation", "criteria", "merge", "contribute", "stats"}
   ```
2. `internal/httpapi/server.go` — the page handler, next to `handleTaxonomyPage` (~line 417):
   ```go
   // handleCurationPage renders the review queue (§4 of the plan). It reads the instance-wide
   // API, so it needs no slug: a project's queue is a filter on the same list.
   func (s *Server) handleCurationPage(w http.ResponseWriter, r *http.Request) {
   	s.render(w, http.StatusOK, "curation", s.pageFor(r, "Curation"))
   }
   ```
   and the route, next to `r.Get("/taxonomy", s.handleTaxonomyPage)`:
   ```go
   	r.Get("/curation", s.handleCurationPage)
   ```
3. `internal/httpapi/templates/base.html:74` — after the Taxonomy link:
   ```html
         <a href="/curation">Curation</a>
   ```

**Verify:**

```bash
go build ./... && echo BUILD-OK
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8006/curation   # only after a local run
```

Expected: `BUILD-OK`, and `200` from a locally running server. `TestEveryAPIRouteIsDeclaredWithAReaderOrAReason`
will now fail — that is Step 7.

**Commit:** `web: the curation queue, queue and log on one page`.

### Step 7 — the inventory row and the HTTP tests

`internal/httpapi/reader_coverage_test.go`, beside the taxonomy rows:

```go
	// The curation queue. Its own row: the routes exist to be read by one page, and the anchor
	// is the list the page renders, so deleting the page's list stops satisfying this row.
	{Match: "/api/v1/curation", Reader: "curation.js", Anchor: "curation-list",
		Routes: 2, Note: "the queue read, and the vote that acts on it"},
```

Create `internal/httpapi/curation_test.go`:

```go
package httpapi

import (
	"context"
	"net/http"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// The queue must list the three kinds for an anonymous reader (it is a read of public work),
// and the vote must be refused without a session -- the refusal is the security property.
func TestCurationQueueReadsAndVotesAreGated(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	slug, pid := makeProject(t, ts, "curation-project")
	ids := makeRankedPair(t, ts, slug, pid, "An idea")
	ctx := context.Background()

	if _, err := db.ProposeStrategicWeight(ctx, pid, ids[0], 1, 2.0, "because"); err != nil {
		// Proposing as the harness user is fine; a duplicate pending proposal is not.
		t.Logf("ProposeStrategicWeight: %v", err)
	}

	resp, body := doJSON(t, ts, "GET", "/api/v1/curation", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/curation: %d body=%v", resp.StatusCode, body)
	}
	items, _ := body["items"].([]any)
	if len(items) == 0 {
		t.Fatal("the queue is empty although a proposal exists")
	}
	first, _ := items[0].(map[string]any)
	for _, field := range []string{"kind", "id", "state", "yes", "no", "my_vote"} {
		if _, ok := first[field]; !ok {
			t.Fatalf("the row has no %q: the page cannot render it", field)
		}
	}

	// A filtered read narrows without failing.
	resp, body = doJSON(t, ts, "GET", "/api/v1/curation?kind=strategy&state=pending", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("filtered read: %d", resp.StatusCode)
	}
	for _, it := range body["items"].([]any) {
		if it.(map[string]any)["kind"] != "strategy" {
			t.Fatalf("the kind filter returned %v", it.(map[string]any)["kind"])
		}
	}

	// A malformed cursor is a 400, not the first page again.
	resp, _ = doJSON(t, ts, "GET", "/api/v1/curation?cursor=nonsense", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed cursor: %d, want 400", resp.StatusCode)
	}

	// Voting needs a session, whichever kind.
	code, _ := authJSON(t, ts, "POST", "/api/v1/curation/taxonomy/1/vote",
		"Bearer nonsense", map[string]any{"vote": "yes"})
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous vote: %d, want 401", code)
	}
}

// An act review is reviewer work, so a plain signed-in user must not reach it: this is the one
// route in the queue that writes something about another person's decision.
func TestCurationVoteOnAnActNeedsStanding(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	slug, pid := makeProject(t, ts, "curation-stand")
	ids := makeRankedPair(t, ts, slug, pid, "Another idea")
	ctx := context.Background()
	arena, err := db.EnsureArena(ctx, store.ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	if err := db.UpsertArenaEntry(ctx, arena.ID, "feature", ids[0]); err != nil {
		t.Fatalf("UpsertArenaEntry: %v", err)
	}
	owner, err := db.GetUser(ctx, "testuser")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	act, err := db.RemoveFeatureFromArena(ctx, pid, arena.ID, ids[0], owner.ID, "spam")
	if err != nil {
		t.Fatalf("RemoveFeatureFromArena: %v", err)
	}

	stranger, err := db.CreateUser(ctx, "curation-stranger", "Stranger")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.SetPassword(ctx, stranger.Username, "stranger-password-123"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	token := loginAs(t, db, stranger.Username, "stranger-password-123")

	path := "/api/v1/curation/act/" + strconv.FormatInt(act.ID, 10) + "/vote"
	if code, _ := authJSON(t, ts, "POST", path, token, map[string]any{"vote": "yes"}); code != http.StatusForbidden {
		t.Fatalf("stranger reviewing an act: %d, want 403", code)
	}
	if err := db.SetTrustLevel(ctx, stranger.ID, 1, owner.ID, "test"); err != nil {
		t.Fatalf("SetTrustLevel: %v", err)
	}
	if code, _ := authJSON(t, ts, "POST", path, token, map[string]any{"vote": "yes"}); code != http.StatusOK {
		t.Fatalf("promoted reviewer: %d, want 200", code)
	}
	// The actor cannot review their own act, however trusted. The harness user IS the actor
	// here, so their own token is the one to post with.
	_, ownToken, err := db.Login(ctx, "testuser", "test-password-123")
	if err != nil {
		t.Fatalf("Login(testuser): %v", err)
	}
	if code, raw := authJSON(t, ts, "POST", path, "Bearer "+ownToken,
		map[string]any{"vote": "no"}); code != http.StatusForbidden {
		t.Fatalf("self review: %d body=%s, want 403", code, raw)
	}
}
```

Add `"strconv"` to that file's imports. Every helper it uses already exists in the package:
`newTestServerWithStore`, `makeProject`, `makeRankedPair`, `loginAs`, `authJSON`
(`internal/httpapi/visibility_test.go:84`), and the harness user's password is set by the fixture
(`internal/httpapi/httpapi_test.go:199`).

**Run:**

```bash
go test ./internal/httpapi/ -run 'TestCuration|TestEveryAPIRouteIsDeclaredWithAReaderOrAReason' -count=1
```

Expected: `ok`. Then break it on purpose once: change the `act` branch's `permitModeration` call
to `return true` and confirm `TestCurationVoteOnAnActNeedsStanding` fails with
`stranger reviewing an act: 200, want 403`. Restore. A gate that cannot fail is decoration.

**Commit:** `test: the queue's read shape, and the gate on reviewing an act`.

### Step 8 — docs, numbers, and the gate

1. `docs/plans/moderation.md`: add one line under M2's acceptance list — "the queue half is
   `docs/plans/curation-queue.md`", so the two plans do not contradict each other about what
   `/audit` versus `/curation` is for.
2. The docs gate reads numbers out of the docs and compares them with the tree. It will report
   `schema 34` → `35`, `34 migrations` → `35`, and a new test count. Run it first and fix exactly
   what it names:

```bash
python3 scripts/check_doc_numbers.py
```

Expected on the first run: lines naming `HANDOFF.md`, `PLAN-r4.md`, `PREMISE.md`, `REMAINING.md`
with `says '34 migrations'` and `says '1228 tests'`. Fix those four files to `35` and to the count
the gate reports, then re-run until it prints:
`OK  N measured number(s) across docs/ agree with the tree`.

3. Full gate:

```bash
make verify
```

Expected: ends with `all checks passed` and exit 0. If `internal/httpapi` times out, the budget in
`Makefile:20` is 900s and the package takes ~285s; a timeout there means the machine is loaded,
not that a test failed — re-run and read the last `--- FAIL` line, if any.

**Commit:** `docs: the curation queue, counted`.

## 7. Tests and validation summary

| what | how it is proved |
|---|---|
| the union projects every kind with real values | `TestCurationListsEveryKindNewestFirst` — asserts a non-empty summary and id per kind, because a UNION of mismatched columns still returns rows, full of NULLs |
| the queue empties as you work it | `TestCurationCanExcludeWhatIHaveAlreadyVotedOn` |
| paging does not repeat or skip under insertion | `TestCurationCursorDoesNotRepeatOrSkip` — inserts a row between pages and requires exactly the original 5 |
| changing your mind is one row, and an actor cannot review their own act | `TestReviewingAnActIsOneRowPerReader` |
| the vote route dispatches to the real deciders | `TestCurationQueueReadsAndVotesAreGated` + the existing taxonomy/strategy tests still passing unchanged |
| act reviews are reviewer work | `TestCurationVoteOnAnActNeedsStanding`, mutated to prove it can fail |
| the page is reachable and its routes have a reader | `TestEveryAPIRouteIsDeclaredWithAReaderOrAReason`, plus a live GET of `/curation` |

Manual pass, on the live instance after deploy: open `/curation`, confirm the default view is
"waiting for review" and excludes your own votes, consent to a taxonomy proposal and watch the
row leave the queue, contest an act from the log view and watch its count move.

## 8. Risks, tradeoffs, open questions

**The union is a read model, and read models drift.** It projects `detail` (JSON) into `summary`
for acts and plain prose for proposals, and the page parses the JSON to pull out a reason. The
alternative — a `curation_items` table written by every decider — is faster to query and is a
second truth about every proposal; refused for the same reason the spec refused a second modlog
table. If the union becomes slow, the fix is an index on `audit_log(action, created_at)`, not a
copy.

**"Oppose" does not exist for proposals.** Taxonomies and weights ratify on consent count; there
is no negative vote in the schema or in the charter maths. The page therefore offers only
"Consent" for those kinds. A real reject path is a proposal-table change (a `rejections` table
plus a `governance.EvaluateConsensus` input that counts against) and is deliberately not in this
plan — it changes the ratification maths, which is a governance decision, not a UI one.

**A contest changes nothing.** This is the honest reading of "review the decisions": the act stays
applied, the count is public, and a maintainer who agrees can undo it. Making a contest tripping a
quorum reverse an act automatically would let any three people reverse any decision, which is
worse than the problem it solves. If that changes, it belongs in the moderation spec as a
decision, with its own rejected alternatives, not in a page.

**Eligibility is inherited, not invented.** Consenting to a taxonomy proposal requires an eligible
tagger (`store.isEligibleTagger`) and refuses self-consent; a weight proposal requires the
project's consensus rules. The queue does not re-check any of it — the deciders do. The consequence
is that a vote can come back `ErrConflict` ("recorded, not decided"), which the page shows as a
count rather than an error.

**Open questions for the owner, not blockers:**

1. Should the contest threshold be `CapDisputeQuorum` (2) — reused, so the instance has one answer
   to "how many people disagree" — or a separate number? The plan assumes reuse but does not
   display a "contested" badge yet.
2. Does the queue belong in the nav, or behind `/taxonomy`? The plan puts it in the nav beside
   Taxonomy because a hidden queue is a queue nobody works.
3. Stash-box has `voting_period` and `min_destructive_voting_period`: proposals expire. Concord's
   proposals never do (a `stale` status exists and nothing sets it). Adding expiry to the queue
   would be a governance change and is out of scope here, but the `stale` status already being
   modelled suggests someone intended it.

---

## Appendix — where the implementation left this plan (2026-11-11)

Built as written, with four changes the plan did not foresee and the browser suite forced. They
are listed here rather than edited into the steps above, because the steps are the record of what
was decided before the code existed:

1. **Step 6's page defaulted `state=pending`.** An act has no pending phase, so the default view
   listed every proposal and no act. The server now answers `state=open` (pending proposals +
   applied acts) and the page asks for that.
2. **`apiAuth()`.** Step 6's fetch snippets carried `credentials: 'same-origin'` and no token.
   This app is bearer-token-in-localStorage; `TestEveryAPIFetchPresentsAToken` refuses the shape
   the plan printed.
3. **The first read must wait for `DOMContentLoaded`**, because `base.html` loads `session.js`
   after the page's own script.
4. **`can_vote` per row.** The plan's Step 6 renders the Consent button unconditionally; the
   server refuses it for a reader without standing, so the page now says which of the two reasons
   applies instead of offering a control that fails.

Also: `act_reviews.user_id` had to be added to `foldColumns` in `internal/store/guest.go` --
`TestEveryColumnThatCarriesAUserIsFolded` derives that list from the schema and refuses a new
user-carrying column that a guest fold would leave behind.
