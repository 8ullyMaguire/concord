# Plan — the Consensus page

**Written 2026-10-04**, from `docs/specs/consensus-page-spec.md`. Steps are ordered so
each one is verifiable before the next depends on it. Every step carries the exact
command and its expected output.

**Status 2026-10-04: C1-C6 are DONE** (`4ce3856` and this commit). C7 and C8 remain.
Where the plan's own text was wrong about the tree, the step says so -- three times
it named a file or a mutant that does not exist, which is the useful signal.

**Prerequisite already done:** the quorum defect (spec §2) is fixed in `b8d7b87`.

---

## Step C1 — Refactor the tally out of `CloseConsensusCall`  [the enabler]  [DONE]

**TallyConsensus extracted; CloseConsensusCall calls it. Full store suite green.**


**Why first.** The tally exists only inside `CloseConsensusCall`, so it is unreachable
for an **open** call — which is exactly when a page needs to show it. Extracting it is
what makes C2 possible, and it is a pure refactor: no behaviour changes.

**Files:** `internal/store/consensus.go` (edit)

Add:

```go
// TallyConsensus builds a call's counts without closing it.
//
// Split out of CloseConsensusCall because the counts are needed for an OPEN call:
// the Consensus page has to show quorum progress while the call is still running, and
// before this the only way to get them was to close it. CloseConsensusCall now calls
// this, so the numbers a page shows mid-call and the numbers a close evaluates are
// the same computation -- not two copies that can drift.
func (d *DB) TallyConsensus(ctx context.Context, c ConsensusCall) (governance.ConsensusCounts, error) {
	positions, err := d.GetPositions(ctx, c.ID)
	if err != nil {
		return governance.ConsensusCounts{}, err
	}
	var counts governance.ConsensusCounts
	for _, p := range positions {
		counts.Participants++
		switch p.Position {
		case "consent":
			counts.Consent++
		case "stand_aside":
			counts.StandAside++
		case "block":
			counts.Block++
		case "abstain":
			counts.Abstain++
		}
	}
	objections, err := d.GetObjections(ctx, c.ID)
	if err != nil {
		return governance.ConsensusCounts{}, err
	}
	for _, o := range objections {
		if o.Status == "open" {
			counts.OpenObjections++
		}
	}
	eligible, err := d.EligibleCollaboratorCount(ctx, c.ProjectID)
	if err != nil {
		return governance.ConsensusCounts{}, fmt.Errorf("eligible collaborators: %w", err)
	}
	counts.Eligible = eligible
	return counts, nil
}
```

Then replace the body of `CloseConsensusCall` from `positions, err := d.GetPositions`
through `counts.OpenObjections = openObj` plus the eligible block with:

```go
	counts, err := d.TallyConsensus(ctx, c)
	if err != nil {
		return ConsensusSummary{}, err
	}
```

**Verify:**

```bash
gofmt -l internal/store/ && go test ./internal/store/ -run Consensus
# expect: ok  	git.polarisocial.xyz/concord/concord/internal/store
```

A survivor here means the refactor changed behaviour. The quorum regression tests in
`consensus_quorum_test.go` are the ones that catch it.

---

## Step C2 — Put `tally` in the read response  [the gap the page would otherwise fill with JS]  [DONE]

**`tally` in the read response. `handleGetConsensus` was the natural place to find a second bug -- see C3.**


**Why this cannot be skipped.** `store.ConsensusThresholdsSummary` already serialises
the right shape and has **zero callers**. Without C2 the page must compute both ratios
in JavaScript, which spec §3 forbids in as many words.

**Files:** `internal/httpapi/handlers.go` (edit — `handleGetConsensus`, line ~1058)

Change the final `writeJSON` to:

```go
	counts, err := s.Store.TallyConsensus(r.Context(), c)
	if err != nil {
		mapError(w, err)
		return
	}
	charter := governance.DefaultCharter(governance.GovernanceModel(projectModel))
	writeJSON(w, http.StatusOK, map[string]any{
		"call":       c,
		"positions":  positions,
		"objections": objections,
		"tally":      store.ConsensusThresholdsSummary(counts, charter),
	})
```

`projectModel` is the project's `governance_model` column, already read the same way
in `CloseConsensusCall`:

```go
	var modelStr string
	if err := s.Store.QueryRowContext(r.Context(),
		`SELECT governance_model FROM projects WHERE id = ?`, c.ProjectID).Scan(&modelStr); err != nil {
		mapError(w, err)
		return
	}
	gm := governance.GovernanceModel(modelStr)
	if !gm.Valid() {
		gm = governance.Collective
	}
```

If `Server` has no `QueryRowContext`, use `s.Store.QueryRowContext` (the store embeds
`*sql.DB`). Add the `"git.polarisocial.xyz/concord/concord/internal/governance"`
import if it is not already there.

**Verify:**

```bash
go build ./... && go test ./internal/httpapi/ -run Consensus
# expect: ok
```

---

## Step C3 — The API tests, before any UI  [DONE]

**Five tests. They found a live hole: none of the five consensus handlers checked the call belonged to the project in the URL, so a private project's call was readable by anyone who guessed the id, and project A's call was readable through project B's URL. Fixed in `4ce3856` via `requireCallInProject`.**


**Files:** `internal/httpapi/consensus_page_test.go` (new)

Four tests, each failing when its rule breaks:

| test | asserts |
|---|---|
| `TestTheConsensusReadCarriesTheTally` | `tally` present with all 13 keys |
| `TestTheTallyReportsBothRatiosSeparately` | 4 consent / 3 stand-aside / 0 block → `support_ratio` 0.57, `decisive_ratio` 1.0 |
| `TestAConsensusPageForAPrivateProjectIsNotFoundToAStranger` | 404, body is exactly `{"error":"not found"}` and leaks no slug |
| `TestTheConsensusReadWorksSignedOut` | 200 unauthenticated |

The second is the load-bearing one. It is the pair the spec §3 says must never be
collapsed, and it fails if anyone "simplifies" the response to one ratio.

Use the existing helpers in this package: `newTestServer(t)`,
`newTestServerNoActor(t)`, `seedPanels`, `mustProjectIDBySlug`. Cast positions through
the real endpoint (`POST .../position`) rather than inserting rows, so the test also
covers the write path the page uses.

**Verify:**

```bash
go test ./internal/httpapi/ -run Consensus -v 2>&1 | grep -E '^(---|ok|FAIL)'
# expect 4x --- PASS
```

---

## Step C4 — The page  [DONE]

**consensus.html, consensus.js, the handler, the route, the template registration, and CSS appended to style.css. Verified in a browser, which found the bug the unit tests could not: `done()` emptied the root's textContent, destroying the article and every button, so the page rendered nothing at all with no console error to explain it.**


**Files:** `internal/httpapi/templates/consensus.html` (new),
`internal/httpapi/assets/js/consensus.js` (new),
`internal/httpapi/assets/css/style.css` (edit — append only)

Template, following `scout.html`'s shape exactly (it is the most recent page and the
one whose conventions the repo follows):

```html
{{define "consensus.html"}}
<section id="consensus-root" class="consensus">
  <div class="spinner" id="consensus-spinner">Loading…</div>
  <div id="consensus-error" class="error-state" hidden></div>
  <article id="consensus-call" hidden>
    <h1 class="consensus-question" id="consensus-question"></h1>
    <p class="consensus-meta" id="consensus-meta"></p>

    <div class="consensus-tally" id="consensus-tally">
      <div class="tally-measure">
        <span class="tally-label">Support</span>
        <span class="tally-value" id="tally-support"></span>
        <span class="tally-detail" id="tally-support-detail"></span>
      </div>
      <div class="tally-measure">
        <span class="tally-label">Decisive</span>
        <span class="tally-value" id="tally-decisive"></span>
        <span class="tally-detail" id="tally-decisive-detail"></span>
      </div>
    </div>
    <dl class="tally-counts" id="tally-counts"></dl>

    <div class="consensus-positions" id="consensus-positions" hidden>
      <button type="button" data-position="consent"     id="pos-consent">     Consent</button>
      <button type="button" data-position="abstain"      id="pos-abstain">      Abstain</button>
      <button type="button" data-position="stand_aside"  id="pos-stand-aside">  Stand aside</button>
      <button type="button" data-position="block"        id="pos-block">        Block</button>
    </div>
    <p class="consensus-signed-out" id="consensus-signed-out" hidden>
      <a href="/login">Sign in</a> to record a position.
    </p>

    <section class="consensus-objections" id="consensus-objections" hidden>
      <h2>Objections</h2>
      <ul id="objection-list"></ul>
    </section>
  </article>
</section>
{{end}}
```

Route in `server.go`, beside the other page routes:

```go
	r.Get("/projects/{slug}/consensus", s.handleConsensusPage)
```

`handleConsensusPage` renders the template with the project resolved through
`s.requireProjectID(w, r)` — so a private project 404s for a stranger with no layout
flash, satisfying §8 rule 4 by using the same guard every other page uses.

`consensus.js` rules, all of which have bitten this codebase before:

- `textContent` for **every** authored string. There is a test
  (`TestDocumentsPageNeverWritesAuthoredTextIntoInnerHTMLWithoutTheRenderer`)
  because this has been a real bug here.
- Remove the spinner on **every** path, including the failure path (§8 rule 1).
- Fetch `?call=<id>`; render `tally` verbatim. **Never** compute a ratio.
- Signed out (`!token()`): hide `#consensus-positions`, show
  `#consensus-signed-out`.
- Keys `c`/`a`/`s`/`b` set the pending position; `Enter` submits; arrows move between
  buttons. Every shortcut also has its button above.
- Errors → `#consensus-error` with the server's message and a retry button.

**Verify:**

```bash
make build
./bin/concord -db /tmp/c4.db -listen 127.0.0.1:8479 &
# open /projects/<seeded-slug>/consensus?call=1
```

Manual gate: both ratios visible and differently valued; four position buttons; the
spinner gone on failure; no console errors.

---

## Step C5 — Playwright  [DONE]

**Seven tests. The headline one is verified to fail: collapsing `tally-decisive` to the support ratio fails it with `decisive is 57%, want 100%`.**


**Files:** `tests/e2e/consensus_e2e.py` (new), `Makefile` (edit — add the suite)

The fixture seeds a project, a call, and the 4/3/0 position split that makes the two
ratios disagree. Then:

- `test_the_page_renders_both_ratios_and_they_differ` — the headline assertion.
- `test_a_reservation_is_not_counted_against_the_decisive_ratio`
- `test_the_tally_shows_quorum_progress_while_the_call_is_open` — C1/C2's whole reason.
- `test_a_private_project_is_not_found_to_a_stranger` — and the body leaks no slug.
- `test_signed_out_reads_but_offers_a_sign_in_prompt_instead_of_the_buttons`
- `test_a_failed_read_removes_the_spinner` — §8 rule 1, the failure path.
- `test_keyboard_c_sets_a_position_and_a_button_shows_the_same_state`

Follow `scout_e2e.py`'s conventions exactly: session-scoped `server` fixture,
`expect(locator(...)).to_be_visible(timeout=...)` instead of sleeps, and a
`page.errors` listener asserted empty on every test.

**Verify:**

```bash
python3 -m pytest -p no:cacheprovider tests/e2e/consensus_e2e.py -q
# expect: 7 passed
```

---

## Step C6 — Mutation gate  [DONE]

**Two gates, 4/4 each: `store/consensus.go` and `governance/governance.go`. Two deviations from the plan text, both because it was wrong about the tree: there is no `internal/store/consensus_test.go` (it is `consensus_solutions_test.go`), and one planned mutant -- `Participants += 2` -- is unkillable, because `positions` is keyed on `(call_id, user_id)` so no test can distinguish it. Replaced with the abstention rule, which is observable and which needed a new test.**


**Files:** `internal/store/consensus_mutants.json` (new), `Makefile` (edit)

```json
[
  { "name": "quorum is computed against zero",
    "old": "counts.Eligible = eligible",
    "new": "counts.Eligible = 0" },
  { "name": "a reservation counts against consent",
    "old": "return cc.Consent + cc.StandAside + cc.Block",
    "new": "return cc.Consent + cc.StandAside + cc.Block - cc.StandAside" },
  { "name": "an abstention counts as a side taken",
    "old": "return cc.Consent + cc.Block",
    "new": "return cc.Consent + cc.Block + cc.Abstain" },
  { "name": "an open objection stops being counted",
    "old": "if o.Status == "open" {",
    "new": "if false {" },
  { "name": "the eligible population ignores activity",
    "old": "counts.Eligible = eligible",
    "new": "counts.Eligible = 1000" }
]
```

Add to `gates:`:

```
	python3 internal/mutate.py internal/store/consensus.go ./internal/store/ internal/store/consensus_test.go,internal/store/consensus_quorum_test.go internal/store/consensus_mutants.json
```

**Verify:**

```bash
make gates
# expect: [consensus.go] ---- 5/5 killed, 0 survived
```

**A survivor means the TEST is wrong, not the code.** Investigate before weakening an
assertion.

---

## Step C7 — Docs and the live check

- `docs/plans/consensus-page.md`: mark each step `[DONE]` as it lands.
- `docs/plans/phase3.md`: add a line under S1 noting the Consensus page shipped.
- `README.md`: the consensus page in the pages list.

```bash
make verify && make e2e && make gates
make deploy
curl -s "localhost:8006/api/v1/projects/<public-slug>/consensus/1" | python3 -m json.tool | head -40
# expect: a JSON object with "tally" containing both ratios
```

Use a **public** slug: a private project answers `not found` to a non-member by
design, which is indistinguishable from a missing route.

---

## Step C8 — Out of scope, recorded so it is not mistaken for an omission

- **Pairwise Both/Neither.** The `positions` CHECK constraint has no such value.
  Genuinely unimplemented, not deferred.
- **A calls index.** No endpoint lists calls, so there is nothing to list. The page is
  reached from a feature or solution.
- **`positions.reason`.** `CastPosition` takes no reason; the column defaults to `''`.
  Render the stance or nothing.
