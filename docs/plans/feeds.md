# Plan — Feeds (RSS 2.0 + Atom 1.0)

Implements `docs/specs/feeds-spec.md`. Written 2026-10-04, before any code.
Every step names its verification command and the output that means it worked.

## Step F1 — the two new store methods the spec proved necessary

Measured while writing the spec: there is no list-calls method. `internal/store/consensus.go`
has `CreateConsensusCall`, `GetConsensusCall`, `TallyConsensus`, `CloseConsensusCall` and
nothing that lists. F4 needs one.

Add `internal/store/consensus_feed.go`:

```go
// ListConsensusCallsForFeed returns a project's calls, newest first.
//
// It deliberately does NOT return a tally. §6.6 hides the running counts until the call
// closes, and a feed item is a durable copy held by a third-party aggregator that will
// resurface it years later -- publishing counts there would defeat the rule no matter how
// carefully the page obeys it. state is carried instead, and it is not a number: the
// item says the call is open, not how many have consented.
func (d *DB) ListConsensusCallsForFeed(ctx context.Context, projectID int64, limit int) ([]ConsensusCall, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := d.QueryContext(ctx, `
		SELECT id, project_id, feature_id, opened_by, opens_at, closes_at,
		       status, question, description, result, opened_early, closed_at
		FROM consensus_calls WHERE project_id = ?
		ORDER BY opens_at DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConsensusCall
	for rows.Next() {
		var c ConsensusCall
		// Nullable columns, because the schema says so: `result`, `question` and
		// `description` are all NULL for calls opened before those columns existed,
		// and `opened_early` is NULL unless §6.5's waiver was used. Scanning a NULL
		// into a bare string or bool fails the whole read, so one NULL question would
		// take down the entire feed.
		var question, description, result sql.NullString
		var openedEarly sql.NullBool
		var closedAt sql.NullFloat64
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.FeatureID, &c.OpenedBy, &c.OpensAt,
			&c.ClosesAt, &c.Status, &question, &description, &result,
			&openedEarly, &closedAt); err != nil {
			return nil, err
		}
		c.Question, c.Description, c.Result = nullString(question), nullString(description), nullString(result)
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

// nullString collapses sql.NullString to the *string the struct uses, so a NULL column
// and an absent one are the same thing rather than a pointer to "".
func nullString(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}

// DisplayNamesFor resolves ids to display names in ONE query.
//
// Batched on purpose: a feed holds up to 50 items, and a per-item lookup is N+1 queries
// on a page an aggregator may fetch on a timer. display_name only, never username or
// email -- see spec §5.1.
func (d *DB) DisplayNamesFor(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	q := `SELECT id, display_name FROM users WHERE id IN (?` + strings.Repeat(",?", len(ids)-1) + `)`
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
```

**The SELECT below was corrected against the real schema before being written, and the
correction changed the scan.** The first draft scanned `result`, `question` and
`description` into plain `string` and `opened_early` into `bool`. `.schema
consensus_calls` says otherwise:

- `result TEXT`, `summary TEXT`, `question TEXT`, `description TEXT` — all NULLABLE, so
  they need `sql.NullString` or the struct's `*string` scanned through it;
- `opened_early INTEGER` — nullable, and the struct models it as `*bool`;
- `closed_at REAL` is nullable and `extensions INTEGER NOT NULL DEFAULT 0` exists;
- **there is no `created_at` column at all.** `ConsensusCall.CreatedAt` has been zero
  since migration 0014 rebuilt the table — the same trap as the Consensus page, and the
  reason §7 says to order by `opens_at` and never by `CreatedAt`.

**Verify**

```bash
go test ./internal/store/ -run 'ListConsensusCallsForFeed|DisplayNamesFor' -v
```
expect: `--- PASS` on both, and `--- PASS: ListConsensusCallsForFeedOrdersNewestFirst`.
That last one matters: a feed in arbitrary order is a feed nobody reads.

## Step F2 — one Feed type, two serialisations (spec R1)

`internal/httpapi/feeds.go`:

```go
// Feed is a syndication document in either of two serialisations. RSS 2.0 and Atom 1.0
// differ in element names and in two required fields; they do not differ in content. Two
// hand-written templates would drift, and the drift would be silent, because a reader
// ignores what it does not recognise.
type Feed struct {
	Title       string
	SelfURL     string   // absolute; atom:link rel="self" and RSS <atom:link>
	AltURL      string   // the HTML page a human should read
	Description string
	Updated     time.Time
	Items       []FeedItem
}

type FeedItem struct {
	ID        string // absolute, stable, never reused
	Title     string
	Link      string // absolute
	Updated   time.Time
	Summary   string
	Author    string // display name only
	Categories []string
}
```

`writeFeed(w, format string, f Feed)` dispatches to `writeRSS` or `writeAtom`.

**The escaping rule, which is the whole security surface of a feed:** summaries and titles
are user-supplied text going into XML. Go's `encoding/xml` escapes on marshal; hand-built
strings will not. Use `xml.EscapeText` or a struct marshal. A summary containing
`<script>` must come out as `&lt;script&gt;` in BOTH formats.

**Verify**

```bash
go test ./internal/httpapi/ -run 'Feed' -v
```
expect: `TestBothFormatsCarryIdenticalItems`, `TestUserTextIsEscapedInBothFormats`,
`TestAtomCarriesSelfLink`, `TestRSSAndAtomDatesUseTheRightFormat` — and a test that
deliberately sets `Host: evil.example` asserting that string appears NOWHERE in the body.

## Step F3 — routes and the base URL (spec §3)

In `server.go`, alongside the existing page routes, register:
`/projects/{slug}/feed.xml`, `/projects/{slug}/atom.xml`, `/feed.xml`, `/atom.xml`.

```go
base := strings.TrimRight(os.Getenv("CONCORD_BASE_URL"), "/")
if base == "" {
	http.Error(w, "Feeds are disabled: set CONCORD_BASE_URL to this site's public origin.",
		http.StatusServiceUnavailable)
	return
}
```

503 and not 404 when unset: the feature is configured off, which is a different fact from
the resource not existing. And `r.Host` is never consulted — it is attacker-controlled via
the `Host:` header, so deriving link origins from it lets a request mint a feed whose links
point at an attacker's host, which cache poisoning turns into a permanent trap for every
subscriber.

Every feed handler begins with `s.requireProjectID(w, r)` so a private project 404s through
the same path as its page (spec R3), and the body never carries the slug.

**Verify**

```bash
go test ./internal/httpapi/ -run 'Feed' -v
```
expect `TestAPrivateProjectHasNoFeedAtAll` (404, not an empty feed),
`TestAnUnsetBaseURLRefusesToServeFeeds` (503 with the variable named),
`TestFeedLinksAreAbsoluteAndUseTheConfiguredBase`.

## Step F4 — §6.6 in the feed, and it is the easiest thing to get wrong

The store method that returns a tally is right there and already used by the page. Writing
this feature without noticing, you would publish the running tally to every aggregator that
ever subscribes.

`TestAnOpenConsensusCallPublishesNoCountsInTheFeed` scans the body for every stance key —
`consent`, `stand_aside`, `block`, `support_ratio`, `decisive_ratio`, `participants`,
`eligible` — and fails on any of them.

**Mutation-verify it.** Replace the F4 item builder with one that includes
`store.ConsensusThresholdsSummary(...)`. The test must fail. A green test that was never
seen red here is not evidence.

## Step F5 — Playwright

`tests/e2e/feeds_e2e.py`: the `<link rel="alternate" type="application/atom+xml">` that F3
adds to a page HEAD resolves to a parseable feed, and an Atom item's `<link>` loads the
HTML page it names. The second assertion is the one that catches a link built from the
wrong base.

## Step F6 — gates

Add a mutation gate for `internal/httpapi/feeds.go`. Two mutants worth arming:

- a feed item that omits `Author` — killed by a test asserting a display name is present
  and an email is not;
- `limit` clamped to the wrong value — killed by a test seeding 60 rows and asserting 50.

## Final verification

```bash
make verify && make gates && make e2e
```
