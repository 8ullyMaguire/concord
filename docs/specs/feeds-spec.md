# Feeds — RSS and Atom for every public collection

**Status:** specified 2026-10-04. Nothing implemented.
**Origin:** the 100-ideas list, Tier S item 7 — "RSS/Atom for everything (any query,
board, list, request, consensus call). Trivial, enormous for integrators." Scored 6/1.
**Audit, 2026-10-04:** absent. `grep -rlE 'application/(rss|atom)\+xml'` over
`internal/` and `tests/` returns zero files. Not partially built.

## 1. What this is

Five public collections, each readable as RSS 2.0 or Atom 1.0, served from the SAME
URL as the HTML page with a `format` parameter. No new discovery surface, no
authentication, no new storage.

| # | Route | Collection |
|---|---|---|
| F1 | `/projects` | public projects, newest activity first |
| F2 | `/projects/{slug}` | one project: its open complaints and resolved ones |
| F3 | `/projects/{slug}/board` | board cards, most recently moved first |
| F4 | `/projects/{slug}/consensus` | consensus calls with their state |
| F5 | `/projects/{slug}/documents` | documents, every kind |

`?format=rss` and `?format=atom` on any of them. `/feed.xml` and `/atom.xml` are
aliases, because that is what people type and what `<link rel="alternate">` wants.

## 2. The two rules that decide the design

**R1 — ONE implementation, two serialisations.** A `Feed` struct with a
`Write(w, format string)` method. RSS 2.0 and Atom 1.0 differ in element names and in
two required fields (`atom:link rel="self"`, `<updated>` vs `<pubDate>`), and nothing
else. Two hand-written templates would drift, and the drift would be silent: an Atom
reader ignores what it does not recognise.

**R2 — the feed and the page read the SAME store call, not the same handler.** Corrected
before implementation. The first version of this rule said the feed is "a projection of
the HTML page" and would take the slice the page handler already computes. Measured:
that slice does not exist. `templates/projects.html`, `board.html` and `project.html`
contain **zero** `{{range}}` directives, and `projects.js:39` / `board.js:28,38` fetch
`/api/v1/projects` and `/api/v1/projects/{slug}/board` from the browser. The pages are
client-rendered over the same public API a feed would use.

So the rule survives in a stronger and more useful form: **the feed calls the same store
method the page's API handler calls.** `feedItems` does not query; it is handed what
`handleListProjects` / `handleBoard` already obtained, and the mutation gate for the feed
is the same gate as for the API. Two independent queries of the same store method would
still be free to disagree, so the shared call is the point — not the shared handler.

The consequence for §6: `TestBothFormatsServeTheSameItemsAsThePage` cannot compare against
server-rendered HTML. It compares the feed against the JSON the page itself consumes, and
that is a stronger test than comparing two of this codebase's own templates.

**R3 — private means absent, not empty.** A private project has no feed at all (404
through the same `requireProjectID` as the page). An EMPTY feed for a private project
tells a reader the project exists and is quiet; a 404 tells them nothing. This is the
rule the whole codebase already follows for visibility.

## 3. Base URL: the one new setting, and why it is justified

Every `<link>` in a feed is absolute; relative URLs are not permitted by either format,
and a reader that fetches `projects/foo` relative to its own base resolves nothing.

There is no base-URL setting in the codebase today (`Server` has `Store`, `Version`,
`WebhookSecret`, `pages`, and the finder mutex; `cmd/concord` reads only `CONCORD_DB`,
`CONCORD_LISTEN`, `CONCORD_EMBED_URL`). Deriving the origin from `r.Host` is wrong:
it is attacker-controlled via `Host:` on a directly-reachable instance, so it would let
a request mint a feed whose links point at an attacker's host — cache poisoning of every
subscriber who ever fetched it. So:

```
CONCORD_BASE_URL   e.g. https://concord.example.org
```

- Required when serving any feed. Absent, feeds answer **503 with a plain-text
  explanation**, not 404: the feature is configured off, which is different from the
  resource not existing.
- Trailing slash trimmed on load. A doubled slash in every one of 20 `<link>` elements
  is a bug that reads as cosmetic.
- Used ONLY for link construction. Never for fetch, never for redirects, never
  compared against the request.

**Decision to notify:** this adds a setting, against the standing "no new flags unless
strictly needed" rule. It is needed: without it the feeds are either unbuildable
(relative links) or forgeable (`r.Host`). Recorded here rather than buried in code.

## 4. Item shape

Common to both formats:

| Field | Source | Note |
|---|---|---|
| `id` | the row's own URL under the base | stable, never reused |
| `title` | project name / complaint title / call question | |
| `updated` | the row's real last-change time | RFC3339 in Atom, RFC822 in RSS |
| `link` | the HTML page for that row | what a reader clicks |
| `summary` | the row's description, plain text | |
| `author` | `users.display_name` via a join | **display name only** — never a username or email, see §5 |

Measured, because the first version of this row asserted the name was already on the
row: it is not. `complaints` carries `author_id INTEGER NOT NULL REFERENCES users(id)`
and no name column, and `ListComplaints` (`internal/store/complaints.go:70`) returns
`[]Complaint` with no author field. `documents` is the same shape. Only
`membership.go:81` joins `users` today. So F1–F5 need one small shared helper —
`DisplayNamesFor(ctx, ids []int64) (map[int64]string, error)` — batched per feed, never
per item, because a per-item lookup is N+1 queries on a feed that may hold 200 rows.

**F4 additionally carries `consensus_state`** as an Atom `category` and an RSS
`<category>`: `open` / `closed`. A subscriber watching a project's calls wants to filter
on state, and putting it in a category makes that possible without parsing prose.

## 5. Privacy: three leaks this design has to refuse

1. **No email addresses, ever.** `users` has an email column. Feeds carry `display_name`
   and nothing else. A feed is fetched by aggregators that cache forever and resurface
   content years later; an email in one is a permanent exposure.
2. **Private and unlisted projects are absent.** See R3.
3. **No unpublished documents.** There is no such state to filter on.
   `project_documents` has no draft/published column — `kind` is one of
   readme|spec|plan|wiki|adr|changelog|scout — so a document is public the moment it
   exists and every document belongs in the feed. This was the spec's first draft and it
   was wrong in a way that returned `ErrInvalid` → 400 to every reader.
4. **No position tallies.** §6.6's hidden tally applies to the feed exactly as it does
   to the API and the page: an open consensus call's feed item carries its state and
   question and NO counts. A subscriber is a third party with a durable copy; publishing
   the running tally there would defeat the rule no matter how well the page obeys it.
   This is the single easiest thing to get wrong in this feature, because the store
   method that returns a tally is right there and already used by the page.

## 6. Tests

Store/HTTP, all in `internal/httpapi/feeds_test.go`:

- `TestBothFormatsServeTheSameItemsAsTheAPIThePageRenders` — the load-bearing one: RSS
  and Atom contain the same N titles, and N and the titles equal what `GET
  /api/v1/projects` returns, which is what the browser fetches to draw the page.
- `TestAPrivateProjectHasNoFeedAtAll` — not an empty feed. 404.
- `TestAnOpenConsensusCallPublishesNoCountsInTheFeed` — §5.4. The feed body is scanned
  for every stance key. Mutation-verified: publishing the tally fails it.
- `TestFeedLinksAreAbsoluteAndUseTheConfiguredBase` — no `href="/`, and no `Host` from
  the request appears anywhere in the output. Set a bogus `Host:` header on the request
  and assert it never appears: that is the poisoning test.
- `TestAnUnsetBaseURLRefusesToServeFeeds` — 503, and the body explains which variable.

Playwright, in `tests/e2e/feeds_e2e.py`: the `<link rel="alternate">` on a page actually
resolves, and an Atom item's link loads the HTML page it names.

## 7. Explicitly out of scope

Search-query feeds (idea 17), saved-search permalinks (17), full-text search over feeds.
Idea 17 subsumes part of this and should be built next; the projection rule (R2) is what
makes it cheap to extend.
