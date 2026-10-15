# Completion pass — the four surfaces that are built and unreachable

**Status:** specified 2026-10-06, before any code. Derived from a measurement, not a tracker.
**Method:** `docs/REMAINING.md` §11.4 claims "no other endpoint has a handler and no
reader". That claim was checked against the tree and the running service. It is false
in four places, and one of the four is a live defect, not a missing page.

## 0. What was measured, and with what

| Measurement | Command | Result |
|---|---|---|
| every registered route | a throwaway `chi.Walk` test over `NewServer(...).Router()` | 173 routes, 94 GET |
| client-side readers | `grep -rl <route> internal/httpapi/assets/js internal/httpapi/templates` | see §1 |
| the e2e suites a directory run collects | `python3 -m pytest tests/e2e --collect-only -q` | **152 of 210 tests, 8 of 13 files** |
| the deploy's build | `curl -s 127.0.0.1:8006/api/v1/version` | `v0.4.0-voting-92-g7114a54` vs HEAD `a45808e` (15 commits) |

Route enumeration by difference is what found §1 and §2. The `--collect-only` count is
what found §3. Both are one command.

## 1. `GET /api/v1/similar/{kind}/` — zero callers, and the URL it returns 404s

`handleFindSimilar` implements §6.1:

> **Duplicate detection at filing time** uses semantic similarity before submit, and
> also searches *other projects* ("this was fixed in X").

and §6.2's prior-art check. The handler is complete, guarded, measured (it reports
embedding coverage and per-model thresholds) and **nothing anywhere calls it**:
`grep -rin similar` over `assets/js`, `templates/`, `tests/` returns the route
registration and nothing else. The two create forms that could use it are both
client-rendered on the project page (`assets/js/project.js`, `#complaint-form` and
`#feature-form`, lines 501-537).

**The live defect.** Every match carries a link, and the link is wrong:

```
$ curl -s '127.0.0.1:8006/api/v1/similar/feature/?text=search+returns+nothing' | head
  "similar": [{"kind":"feature","entity_id":222,"title":"**NO**","url":"/api/v1/features/222"}, ...]
$ curl -s -o /dev/null -w '%{http_code}' 127.0.0.1:8006/api/v1/features/222
404
```

`internal/store/embeddings.go:hydrateSimilarities` builds
`/api/v1/features/%d`, `/api/v1/complaints/%d`, `/api/v1/requests/%d`. No such routes
exist — the feature route is `/api/v1/projects/{project_id}/features/{id}`, and the
page a person can open is `/projects/{slug}/features/{id}`. The store test that covers
this asserts `sims[0].URL != ""`, which passes for a 404. **A non-empty string is not a
link.**

### What to build

1. **The URLs become page routes.** `hydrateSimilarities` joins `projects` for the slug
   and emits `/projects/{slug}/{complaints|features|requests}/{id}`. Needs the project
   slug, so the row query gains a join.
2. **A gate that resolves the link, not one that checks it is non-empty.** For each of
   the three kinds: seed a row, find it through the API, take `similar[0].url`, GET it
   through the same test server, assert **200** and that the body names the entity.
3. **A panel on both create forms** — §6.1 says "while the filer is still writing", so
   it is debounced on `input` of the title field, before submit. It states the three
   states a real answer has: matches found, no matches (with the coverage number that
   makes "no matches" mean something), and duplicate detection unavailable.

### The privacy half of this, which is the reason it is first

`FindSimilarField` selects from `embeddings` filtered by kind, model and an optional
project id — **and never by visibility**. The route is deliberately unauthenticated
(§6.1: "a panel that needs a token is a panel nobody sees"). So an anonymous caller can
submit a phrase and learn whether a matching row exists in **any** project, including
private ones, and get back its title, status and id. That is §3.4's "a private roadmap,
enumerable with a loop" with a similarity probe instead of a `for` loop, and it is the
same endpoint.

Verified before fixing (§2.1): a private project's complaint title comes back to an
anonymous caller today.

**Fix:** the candidate query gains the visibility predicate already used by
`ListProjectsVisibleTo` (`internal/store/store.go`), parameterised by the actor id.
Anonymous → public and unlisted only. Signed in → plus what they are a member of, or
hold a redeemed invite for. `project_id` (scoping to one project) keeps working.

## 2. `GET /api/v1/taxonomy/tags` + `GET|POST /api/v1/taxonomy/proposals` — no page reads any of it

`grep -rl taxonomy internal/httpapi/assets/js internal/httpapi/templates` returns
**nothing**. Four endpoints — list tags, list proposals, propose a change, consent to a
proposal — and §4.3 is explicit that this is a governance surface:

> **Global taxonomy changes** (merge, rename, alias, re-parent, delete) are *proposals
> decided by quorum* among eligible taggers... **Maintainers hold no unilateral taxonomy
> power.** All changes are logged and reversible.

So the whole quorum path is reachable only by `curl`. `POST` requires contributor
somewhere for an instance-wide change, or contributor in the project for a scoped one;
consent answers `202` with the current state when below quorum or when the proposer
tries to consent to their own proposal — both ordinary states of a consensus process,
both currently unsayable to a user.

### What to build

One page, `/taxonomy`, reading all four:

- the tag list, grouped by namespace, showing parent and aliases;
- open proposals and decided ones, each with its action, target, value, rationale and
  consent state;
- a propose form (action, target tag, value, rationale, scope);
- a consent control on each open proposal.

The page states the quorum rule where the control is, because "consent" on a proposal
whose ratification needs other people is meaningless without it.

## 3. `GET /projects/{slug}/documents/search` — an FTS5 index with no way to query it

`documents.js` fetches `/documents` and filters **client-side**; `documents.html` has no
search input at all. The server side is a real FTS5 index (`SearchDocuments`, 1,087
documents in the live instance) and the comment above the handler is explicit that an
empty query is a 400 *because* the endpoint is a search box:

> A search endpoint that returns the whole corpus when the box is empty is a way to dump
> a project's private documents onto a page.

### What to build

A search input on the documents page, wired to that endpoint, replacing the client-side
filter for text queries. `?kind=` and `?doc=` deep links keep working.

## 4. `pytest tests/e2e` silently skips 5 of the 13 suites, and the gate cannot see it

Measured:

```
$ python3 -m pytest tests/e2e --collect-only -q | tail -1
152 tests collected
$ python3 -m pytest tests/e2e --collect-only -q | grep -oE 'tests/e2e/[a-z_0-9]+\.py' | sort -u | wc -l
8
```

`pytest.ini`'s `python_files` names eight files by hand. `feature_e2e.py`,
`requests_e2e.py`, `lists_e2e.py`, `board_e2e.py` and `settings_e2e.py` — 58 tests, every
suite written in the last two sessions — match neither pytest's default patterns nor that
list. `make e2e` runs each suite by explicit path, so the Makefile is not affected; a
directory-level run is, and it **reports a pass**.

The gate written for exactly this failure is blind to it:

```python
for suite in ("e2e_test.py", "finder_e2e.py", "project_panels_e2e.py",
                "harness_selftest_test.py"):
    assert suite in out, ...
assert total >= 88, ...
```

Names four files and a floor of 88. Both were true when written and both stayed true
while five suites stopped running. It is the same shape as the gate in the skill's own
catalogue: **a check whose subject list is hand-maintained goes stale the same way the
thing it guards does.**

`pytest.ini`'s own comment already says `EVERY *_e2e.py FILE BELONGS HERE. This list went
stale once already.` It went stale again, on the next five suites, because a comment is
a note and not a control.

### What to build

1. `python_files` matches the **convention** (`*_e2e.py`) plus the two files that do not
   follow it, so a new suite is collected because of its name and not because someone
   remembered a second registration.
2. The gate **globs the directory** and requires every `*_e2e.py` to appear in the
   collect output, with a per-file floor. A new suite that is not collectible fails the
   suite that is about collectability.
3. Both halves proven red: remove a file from `python_files` (restore the current state)
   and add a new `*_e2e.py` that matches nothing.

## 5. Out of scope, stated rather than discovered later

- **`GET /api/v1/admin-ledger`.** Instance-wide, unauthenticated, no reader. Unchanged:
  it is an operator log, and an admin page is a product decision (who is an operator?),
  not a page. Listed here so the next reader knows it was seen.
- **`GET /api/v1/projects/{project_id}/votes/next`, `/tallies`, `/priorities`.** Read by
  `rank.js` and `ranking.js` through a base constant, so the route text does not appear
  literally in the JS. Not an orphan; the probe's false positives are listed in the plan.
- **The public origin running an old build.** `docs/REMAINING.md` §0. Not this repo's to
  fix — but note that the *local* deploy was 15 commits behind HEAD as well, and §0's
  evidence line reads as if it were current. §5 of the plan closes that.

## 6. Definition of done

For each of the four: a test that fails today, the change, the test green, the gate
mutated and seen red, `make verify` and `make e2e` exit 0 with the counts in
`docs/REMAINING.md` updated to the measured ones, both remotes pushed, `make deploy` run
and the live `/api/v1/version` equal to `git describe` at HEAD.
