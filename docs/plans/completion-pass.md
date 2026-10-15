# Plan — completion pass

Implements `docs/specs/completion-pass-spec.md`. Written 2026-10-06, before any code.
Every step names its verification command and the output that means it worked.

Working rules for this plan, because every one of them has already cost this repo a
session:

1. **Run the Go test, never a grepless pipeline.** `node --check x.js && echo OK` prints
   OK for a file that does not parse. Check exit codes as integers.
2. **A gate is not done until it has been seen red.** Each workstream ends with a named
   mutation and the red output it produced.
3. **Rebuild before any browser run.** Assets are `//go:embed`ed; a stale `bin/concord`
   is a false PASS, not a flake.
4. **Order matters in one place only:** WS-E (deploy) is last, because `make deploy`
   refuses a dirty tree.
5. Here `python3` means `/usr/bin/python3` (the one with pytest + playwright). The
   Hermes tool session shadows `python3` with a venv that has neither.

---

## WS-A — make the e2e collection gate measure the directory

### A1. `pytest.ini`

Replace the hand-written `python_files` list with the convention plus the two files that
do not follow it:

```ini
python_files = *_e2e.py e2e_test.py harness_selftest_test.py
```

Keep the comment, and add why the list is gone: the list was the bug, twice.

Verification:

```bash
/usr/bin/python3 -m pytest tests/e2e --collect-only -q | tail -1
```

Expected: `210 tests collected` (not `152`). Before the change: 152, from 8 files.

### A2. The gate, in `tests/e2e/harness_selftest_test.py`

`test_every_suite_is_collected_by_a_directory_run` stops naming suites. It globs
`tests/e2e/*_e2e.py`, requires **each** to appear in the collect output, and keeps a
per-file floor so a suite that collects one trivial test still fails:

```python
suites = sorted(p.name for p in Path(__file__).parent.glob("*_e2e.py"))
assert suites, "no *_e2e.py suites found; this gate would check nothing"
...
out = subprocess.run([sys.executable, "-m", "pytest", "tests/e2e",
                      "--collect-only", "-q"], cwd=REPO, capture_output=True,
                     text=True, timeout=180).stdout
missing = [s for s in suites if s not in out]
assert not missing, ("these suites are not collected by a directory-level run -- they "
                     "match neither pytest's defaults nor pytest.ini's python_files, "
                     "so `pytest tests/e2e` reports a pass while skipping them: "
                     + ", ".join(missing))
counts = {s: int(re.search(rf"\b(\d+) tests? collected", out).group(1)) ... }
```

plus, per file, `counts[s] >= 3`, and a total floor of `10 * len(suites)` computed from
the glob rather than written down.

Also: the doc-number half. The same test asserts the total it measured equals the number
`docs/REMAINING.md` states, so the tracker cannot go stale the way it just did:

```python
m = re.search(r"(\d+) e2e tests? in (\d+) suites?", remaining_md)
assert m, "docs/REMAINING.md no longer states an e2e count for this to check"
assert int(m.group(1)) == total, ...
assert int(m.group(2)) == len(suites), ...
```

Proving the gate red, three ways, each restored:

| mutation | expected |
|---|---|
| `python_files` back to the eight-name list | FAIL, names feature_e2e.py, requests_e2e.py, lists_e2e.py, board_e2e.py, settings_e2e.py |
| add `tests/e2e/zz_probe_e2e.py` with one trivial test | FAIL, `zz_probe_e2e.py` not collected |
| change the count in `docs/REMAINING.md` by one | FAIL, measured vs stated |

### A3. `docs/REMAINING.md`

Set the measured numbers (210 / 13), and record that the previous pair (174 / 10) was
never true of any state this tree was in — it was written when the suites were fewer and
reconciled by nothing.

---

## WS-B — duplicate detection at filing time (§6.1, §6.2)

### B1. `internal/store/embeddings.go`: the candidate query gains a visibility filter

`FindSimilarField` gains an `actorID int64` parameter. The predicate is copied from
`ListProjectsVisibleTo` rather than re-derived, and applied to the embedding's own
project:

```sql
      FROM embeddings e
      JOIN projects p ON p.id = e.project_id
      WHERE e.kind = ? AND e.model_id = ?
        AND (p.visibility = 'public'
             OR (p.visibility IN ('unlisted','private') AND EXISTS (
                   SELECT 1 FROM members mm WHERE mm.project_id = p.id AND mm.user_id = ?))
             OR (p.visibility = 'protected' AND (
                   EXISTS (SELECT 1 FROM members mm2 WHERE mm2.project_id = p.id AND mm2.user_id = ?)
                OR EXISTS (SELECT 1 FROM ir JOIN project_invites i ON i.id = ir.invite_id
                           WHERE i.project_id = p.id AND ir.user_id = ?))))
```

For `kind = project`, `e.project_id` may be 0 (the entity IS the project), so the join
target is `COALESCE(NULLIF(e.project_id, 0), e.entity_id)`. Confirm against the write
path before assuming; if the embedder never writes a project-kind row, drop that branch
and say so.

### B2. `hydrateSimilarities`: the URLs become pages

Needs the slug, so each branch joins `projects`. The three replacements:

| kind | today | after |
|---|---|---|
| complaint | `/api/v1/complaints/%d` | `/projects/%s/complaints/%d` |
| feature | `/api/v1/features/%d` | `/projects/%s/features/%d` |
| request | `/api/v1/requests/%d` | `/projects/%s/requests/%d`, or `/requests/%d` when `requests.project_id IS NULL` |

Page routes to confirm against `chi.Walk` output before writing the test:
`/projects/{slug}/complaints/{id}`, `/projects/{slug}/features/{id}`,
`/projects/{slug}/requests/{id}`, `/requests/{id}`.

### B3. `internal/store/embeddings_test.go`: the URL must RESOLVE

The existing assertion is `sims[0].URL != ""`. Replace it with the thing that would have
caught the defect: the URL must match a page route and the page must render.

- store level: the URL is one of the four known shapes (a regex built from the route list,
  not a hand-typed prefix).
- httpapi level (`internal/httpapi/duplicates_test.go`, extend): seed a complaint, find it
  through `/api/v1/similar/complaint/`, GET `similar[0].url` through the same test server,
  assert **200** and that the body contains the title. Same for feature and request.

This is the gate for B2 and it fails today with `404`.

### B4. The privacy test, written first

`internal/httpapi/duplicates_test.go`:

```go
func TestASimilarityProbeDoesNotLeakAPrivateProjectsTitles(t *testing.T) {
    // private project p1, owned by user A, with a distinctive complaint title
    // anonymous GET /api/v1/similar/complaint/?text=<that title>
    // assert the title does not appear in the body, and entity_id is not in the results
    // then as the member: assert it DOES appear (the panel must work for the people
    // who are entitled to the answer; a fix that hides everything is not a fix)
}
```

Expected before B1: **FAIL** — the anonymous call returns the private title. Captured
output goes in the commit message.

### B5. `assets/js/project.js`: the panel

Inside `wire()`, on `#complaint-form` and `#feature-form`: a debounced `input` listener on
the title field, `GET /api/v1/similar/<kind>/?text=…&project_id=<id>`, rendering into a
`<div class="dup-panel" aria-live="polite">` placed directly under the title field. Four
states, because the endpoint has four:

| response | what the panel says |
|---|---|
| `available: false` | "Duplicate detection is not configured on this instance." |
| `has_duplicate: true` | "This looks like an existing X: <link>" — the strong band |
| matches, below strong | "Similar existing X" with titles as links |
| no matches | "No similar complaints found" **plus the coverage line**, because "nothing similar" on a 12%-indexed corpus is a false statement |

Every title goes through `esc()`; every link comes from the API's own `url`, so a wrong
URL is a test failure and not a second place to get it wrong.

Requests to cancel in flight: keep the last query and drop a response whose text is no
longer current, or a slow answer to "search" overwrites the panel for "search returns".

### B6. Gates for B5

- Go source gate: `project.js` calls `/api/v1/similar/` and builds the URL from the
  response's `url` field (assert by index, from the JS source), plus the positive half —
  the string is present AND `esc(` appears in the same function, so the gate cannot be
  satisfied by deleting the panel.
- Playwright (`feature_e2e.py` or a new `duplicates_e2e.py`): type a title that exists and
  assert a link appears whose href opens; then type gibberish and assert the panel says so
  *with* a coverage number. Both need a real embedder, so the suite skips with a printed
  reason when `available: false` — a skipped-with-reason is honest, a pass is not.
- Mutation: point the panel at `/api/v1/similar/complaint/` for both forms → the feature
  case goes red.

---

## WS-C — the taxonomy page (§4.3)

Five registration points, all of them, because a missing one is silent:

| # | file | change |
|---|---|---|
| 1 | `internal/httpapi/server.go` | `r.Get("/taxonomy", s.handleTaxonomyPage)` |
| 2 | `internal/httpapi/server.go` | `"taxonomy"` in the `pages` slice |
| 3 | `internal/httpapi/templates/taxonomy.html` | new |
| 4 | `internal/httpapi/assets/js/taxonomy.js` | new |
| 5 | `internal/httpapi/templates/base.html` | nav link, or `TestEveryRegisteredPageHasAnInboundLink` fails |

Plus: `navigation_test.go`'s page list gains `/taxonomy`; `a11y_test.go`'s skeleton table
gains `taxonomy: /taxonomy`; `tests/e2e/taxonomy_e2e.py` (collected by name, now);
`Makefile`'s `e2e` target gains the line.

Handler: copy `handleSettingsPage`'s shape (a server-rendered shell with a mount point),
except there is no slug — the page is instance-wide, so no visibility guard applies and
the tag/proposal lists are public reads.

The page:

1. tags grouped by namespace, with parent and aliases;
2. proposals split open/decided, each with action, target, value, rationale, and its
   consent count;
3. a propose form — action, target tag, value, rationale, scope — posting to
   `/api/v1/taxonomy/proposals`;
4. a consent control per open proposal, posting to `/api/v1/taxonomy/proposals/{id}/consent`;
5. the quorum rule stated next to the control, because consent on a proposal that needs
   other people is meaningless without it.

Behaviour the handler dictates and the page must reproduce, not paraphrase:

| server | page |
|---|---|
| `201` + `{proposal, message}` | show the message; the change is not live, it is proposed |
| `202` + `{proposal, message}` | below quorum / self-consent: show the message as information, not as an error |
| `200` + `{proposal, "status":"ratified"}` | the change applied; re-read the tag list |
| anonymous `POST` | 401 with the write gate's message |
| `GET /taxonomy/proposals` on an empty instance | `[]` — assert `Array.isArray`, and assert the empty state renders |
| `GET /taxonomy/tags` on an empty instance | `[]` |

Empty state is the live state: measured today both are empty on this instance, so a page
that only renders with data would look correct to whoever built it and be broken for
everyone else.

Gates: renders; linked (nav); `/taxonomy` in the inbound-link corpus; a Playwright test
that proposes a change and consents, asserting the **202-with-message** path, because
that is the path a user will hit first; a mutation that removes the quorum note from the
page → red.

---

## WS-D — the documents search box

1. `templates/documents.html`: a search input inside the documents mount, with a label.
2. `assets/js/documents.js`: `input` (debounced) → `GET /documents/search?q=`, replacing
   the client-side filtering for text queries. `?kind=` and `?doc=` deep links unchanged.
3. Empty `q` sends nothing — the endpoint answers 400 by design and the box is not an
   error state.
4. A malformed FTS5 expression answers 400 with the store's message; the page shows it
   rather than an empty list, because an empty list is a claim about the corpus.

Gates: a Go test that the JS calls the endpoint and reads `.results`-shaped output
(measure the shape first — `api_surface.py` exists for this); a Playwright test that
searches for a word that exists and asserts the matching document is the only row, then a
word that does not and asserts the page says so.

---

## WS-E — tracker, push, deploy

1. `docs/REMAINING.md`: rewrite §0 (the local deploy was 15 commits behind HEAD, not just
   the public origin), §11.4 (its "nothing else is outstanding" was false in four places,
   with the measurement that shows it), and the header numbers.
2. `python3 scripts/check_doc_numbers.py` → `OK … agree with the tree`.
3. `make verify` → exit 0. `make e2e` → exit 0, with the new suite counted.
4. Commit each workstream separately, with the defect and the evidence in the message.
5. `git push forgejo HEAD:main && git push github HEAD:master` → both remotes at HEAD.
6. `make deploy` → `✓ healthz OK`, then:

```bash
curl -s 127.0.0.1:8006/api/v1/version
git describe --tags --always --dirty
```

Expected: the same commit hash in both.

## Step order, and why

A → B → C → D → E. A first because it protects the verification of everything after it: a
green `make e2e` that skipped five suites is the failure mode this whole plan exists to
avoid, and running it as the acceptance evidence for B, C and D while it is broken would
put three more green-looking results on top of the same hole.
