# The criteria page — spec §6.4's comparison table (docs/REMAINING.md §15)

Status: implementation spec, written before the code. Companion to
`docs/specs/solutions-panel-spec.md` and `docs/specs/alternatives-panel-spec.md`.

## 1. Why this is assembly, not construction

Everything below the page exists and has for months: `criteria` (0006),
`criterion_ratings`, `criterion_votes`, `criteria_profiles`, five store methods
with AC tests, six routes with guards, and `ranking.Composite` returning the
per-criterion breakdown that produced each order. Measured on the live database:

```
criteria 0 · criterion_ratings 0 · criterion_votes 0 · criteria_profiles 0    features 537
```

Zero criteria have ever been created, because **no page anywhere calls any of
it**. The spec says the rankings are "displayed as a comparison table, so people
can see *why* a solution leads (cheap but partial vs thorough but risky)" — and
there is no table, no control, and no way to fill the pool the table reads.

## 2. The read that makes the table possible

One request produces the whole table:

```
POST /api/v1/projects/{slug}/rank/composite      {"weights": {"<criterion_id>": 0.5}}
  -> {"count": N, "results": [{feature_id, score, rank, confidence,
        contributions: [{criterion_id, slug, name, weight, raw, rd, games,
                         normalised, score}]}]}
```

`contributions` is the per-criterion cell, `rank` is the order, `confidence` is
the weighted mean RD. **The table is not sorted client-side** — the API returns
"the order the forge concluded", and re-sorting by score or by a bare rating
would discard the normalisation and the weights, exactly the mistake
`ranking.js` documents.

Three properties of `Composite` decide what a cell can say, and the first version of
this spec assumed one of them wrong:

1. **A contribution exists only for a `criterion_ratings` ROW.** The function loops
   over rating rows, so a feature nobody has compared under a criterion has no cell
   value at all. Absence and `games == 0` mean the same thing to a reader — there is
   no measurement — and both render `unrated`. Anything else prints `0.50`, the
   normalised midpoint of an empty distribution, as if it were a result.
2. **A criterion with no rows contributes nothing and gets no column.** It is not
   excluded by a flag; it simply has no data to normalise.
3. **Naming ANY criterion restricts the query to the named set.** Unnamed criteria
   are excluded rather than joining at their default weight — the change that made
   "rank by design only" stop blending efficiency in at equal weight. The page
   therefore always sends every criterion's box value, so the vector it shows is the
   vector it ranks with.

A feature that appears in no criterion at all is absent from the results, and the
page says so in the table's footnote rather than leaving a reader to wonder: it has
no place in an order built from comparisons.

## 3. The page, section by section

Route `/projects/{slug}/criteria`, tab "Criteria" after "Ranking". Template
`criteria.html`, script `criteria.js`, both registered in `NewServer`'s `pages`
and in the `project-tabs` block of `base.html`.

**3.1 The comparison table.** Rows: every feature the composite returned, in
`rank` order. Columns: the composite column (rank, score, confidence), then one
per criterion that contributed, in the criteria's own order (`slug`), each
header carrying its weight and its direction (`lower is better` flips the reading
of the numbers, so it is named on the header). Cell: `normalised` to two
decimals plus `games` behind it.

**An unrated cell says `unrated`, never `0.50`.** Two ways to reach that state —
a rating row with `games == 0` (the Glicko starting value) and no row at all — and
one rendering for both, because they mean the same thing: nothing has been compared
here. Rendering `0.50` would present the midpoint of an empty distribution as a
measurement, the same class of lie as printing a bare rating without its deviation.
The title text says which of the two it is. `require_min_rd` (3.2) is what drops
such a criterion from the blend entirely.

**3.2 Weights, and re-ranking.** One number input per criterion (default:
`default_weight`), a "Rank with these weights" button, and two checkboxes:
*include inactive criteria* (`include_inactive`) and *ignore criteria with no
votes* (`require_min_rd`). Both are sent explicitly, so the request states what
the page claims it is showing.

**3.3 Profiles.** A `<select>` of saved profiles (`GET /criteria-profiles`), a
"Load" action (the next composite POST sends `profile` instead of `weights`), and
a "Save this weighting" form (name + slug + `POST /criteria-profiles` with the
current vector). The page states the server's precedence rule in the copy: an
explicit weight vector beats a named profile, so editing any weight clears the
profile selection rather than sending both and letting the server guess.

**3.4 Managing criteria.** A create form (slug, name, description, direction,
default weight → `POST /criteria`) and, per row, an activate/deactivate control
(`PUT /criteria/{id}/active`) whose copy says what the flag does: the composite
omits an inactive criterion unless the box in 3.2 is ticked, because deactivating
is how a project retires a dimension.

**3.5 Voting, per criterion.** A compact form inside each criterion's row: two
feature selects (from `GET /priorities`, which carries the titles) and an outcome
select with the five values the schema allows — `a`, `b`, `both`, `neither`,
`skip` — plus `POST /criteria/{id}/vote`. The copy names what each does, because
they are not interchangeable: `both`/`neither` move no rating (they are recorded,
audited as neutral), `skip` is recorded and changes nothing on purpose, and a
re-vote on the same pair is refused as a double count. The response's two updated
ratings are rendered, so a voter sees what their comparison did.

## 4. Rules

1. Every fetch carries `apiAuth()` — a protected project's page renders its shell
   and then 404s every panel without it, and `TestEveryAPIFetchPresentsAToken`
   enforces it.
2. No client-side ranking arithmetic. Order, scores, confidence and cells come
   from the server; the page formats and labels them.
3. Every write reports the server's refusal verbatim, and a 401 says what it
   means (sign in) rather than "something went wrong".
4. The page renders for anyone who can read the project; the writes are server-gated,
   which the page states rather than duplicating. **What the server actually requires
   was measured in the tenth pass and this sentence overstated it**: creating and
   retiring a criterion is `contributor`/`maintainer`-gated, but `POST
   /criteria/{id}/vote` requires only an identity (`getActorID != 0`) and has since it
   shipped. It now admits a guest identity too -- a pairwise comparison feeds the
   ranking, not a quorum, and registration was already free, so a nameless voter does
   not make stuffed input cheaper. The test that used to assert "an anonymous visitor
   cannot vote" asserts what the server does now: the visitor votes as a guest and the
   page reports the recorded ratings (docs/REMAINING.md §23.11).
5. An empty project (no criteria) gets the create form and the reason the table is
   empty, not a bare "no data".

## 5. Definition of done

- `make verify` green; `make e2e` green; `TestEveryAPIFetchPresentsAToken` covers
  the new file without an exemption.
- A Go gate asserts the page is registered, the template renders the slug, and
  `criteria.js` contains: the composite POST, all five outcomes, the `unrated`
  branch, the profile save, the active toggle, and a *reason* string naming what
  inactive means.
- A browser suite (`tests/e2e/criteria_e2e.py`, 13 tests) seeds four criteria and
  votes **directly in sqlite** (a fixture written through the endpoints under test
  cannot fail for the reason it exists) and asserts, in the rendered DOM: a column
  per active criterion and a row per feature; `unrated` in both of its forms;
  flipping a weight moves the composite leader (an order a page that ignores the
  request body cannot produce); `include_inactive` brings a retired criterion into
  the columns; `require_min_rd` drops a criterion whose ratings are all at the
  starting state; creating a criterion adds it; retiring one reports what that
  means; a comparison moves the rating and `skip` does not; a duplicate vote is
  refused in the server's own words; a hostile criterion NAME renders as text; an
  anonymous visitor reads the table and cannot vote.
- Each of the two JS gates is killed by a mutation (remove the `unrated` branch;
  remove the five outcomes), verified by running the mutation.

## 6. Known deferrals

- **The comparison pair is chosen by hand.** The rank page's pair-suggestion
  logic (closest-uncertain pair) is not reused; two selects is the honest minimum
  and a suggestion engine is a separate idea with its own tests.
- **No per-criterion standings leaderboard** beside the table. `RankByCriterion`
  is exercised through the composite with a single weight, so a leaderboard would
  be a second rendering of the same numbers.
- **Criteria are not deletable.** The schema has no delete path and retiring is
  the `active` flag; a delete would need a decision about the votes behind it.
