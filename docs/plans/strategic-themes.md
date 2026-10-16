# Plan — §6.2's strategic themes, end to end

Implements `docs/specs/strategic-themes-spec.md`. **Executed 2026-10-16**; the as-built record,
the deviations and what each mutation run proved are in `docs/REMAINING.md` §25. Every step ends
with the command that proves it, and every one of them was run. Baseline before step 1, measured on this tree (`bc930ed`): **1034 Go tests**,
schema 28 (after step 1), 81 tables, **285 e2e tests in 20 suite files**, 27/27 UI mutants,
128 Go mutants.

Order is deliberate: the migration and the charter field first (nothing else can be verified
without a lifetime), then the store, then the ranking wire **before** any route, because the
ranking wire is the thing the pass exists for — a route that creates themes which do not
move a score would be the same defect with more code.

---

## Step 1 — migration `0028_theme_lifetime.sql`

File: `internal/db/migrations/0028_theme_lifetime.sql` (new).

```sql
-- §366's expiry needs a number, and "the next release cycle" is a per-project judgement
-- rather than a constant: 0012 shipped strategic_themes with expires_at NOT NULL and no
-- writer, which is what a requirement looks like when the column lands before the clause
-- that fills it.
--
-- The charter owns it for the reason SolutionStableHours' comment gives: it encodes how
-- large a project is and how fast it moves. 90 days is a quarter, which is the smallest
-- release cadence the phrase "release cycle" plausibly means.
ALTER TABLE charters ADD COLUMN theme_lifetime_days REAL NOT NULL DEFAULT 90;
```

No `strategic_themes` change: 0012 already has `expires_at REAL NOT NULL`.

**Verify:** `make build` → schema 28; `sqlite3 <fresh db> "select theme_lifetime_days from charters limit 1"` after creating a project → `90.0`.

## Step 2 — the charter carries the lifetime

Files: `internal/governance/governance.go`, `internal/store/store.go`.

1. `Charter` gains `ThemeLifetimeDays float64` (next to `PainHalflifeDays`).
2. `DefaultCharter`: `ThemeLifetimeDays: 90`.
3. `GetCharterForProject`: add `theme_lifetime_days` to the SELECT and the `Scan`.
4. `UpdateCharter`: add it to the UPDATE, and refuse a non-positive lifetime **by name**
   (`theme lifetime must be between 1 and 36500 days`), next to the §6.5 bounds the function
   already checks for the same reason (a CHECK would need a table rebuild).

The field flows through `PUT /api/v1/projects/{id}/charter` with no handler change: the
handler decodes the whole `governance.Charter` (PascalCase — it has no json tags).

**Verify:** `go test ./internal/store/ ./internal/governance/ -count=1` → ok.
`sqlite3 <db> "select theme_lifetime_days from charters"` → the stored value after an update.

## Step 3 — `internal/store/themes.go` (new): the lifecycle

`ListStrategicThemes` and `ThemeWeightTotal` **move here from `strategy.go`** and gain the
expiry filter; `strategy.go` keeps the feature-weight proposals. Types:

```go
type StrategicThemeProposal struct {
	ID             int64   `json:"id"`
	ProjectID      int64   `json:"project_id"`
	ThemeTag       string  `json:"theme_tag"`
	ThemeNamespace string  `json:"theme_namespace"`
	WeightAfter    float64 `json:"weight_after"`
	Rationale      string  `json:"rationale"`
	Status         string  `json:"status"`
	ProposedBy     int64   `json:"proposed_by"`
	CreatedAt      float64 `json:"created_at"`
	DecidedAt      float64 `json:"decided_at"`
}
```

Functions, in the order the plan verifies them:

| function | invariant |
|---|---|
| `ProposeStrategicTheme(ctx, projectID, userID, tag, namespace, weight, rationale) (StrategicThemeProposal, error)` | refuses weight outside `[ThemeWeightMin, ThemeWeightMax]` and an empty tag; `INSERT` with status `pending`, `proposed_by = userID`; the partial index `idx_theme_one_pending` turns a second pending proposal for the same (tag, namespace) into `ErrDuplicate` (409 at the edge) |
| `GetStrategicThemeProposal(ctx, id)` | by id, `ErrNotFound` when absent |
| `ListStrategicThemeProposals(ctx, projectID)` | newest first |
| `ThemeProposalConsents(ctx, proposalID)` | distinct consents, from a `strategic_theme_consents` table added in this step's migration half (see below) |
| `ConsentStrategicTheme(ctx, proposalID, userID) (StrategicThemeProposal, string, error)` | mirrors `RatifyStrategicWeightProposal` clause for clause: pending check; **proposer cannot consent** (`ErrPerm`); consenter must be an eligible collaborator (`ErrPerm`); quorum from `governance.EvaluateConsensus` over the charter; below quorum → `ErrDuplicate` **carrying the count** (202 at the edge); at quorum → upsert the live row + status `ratified` + audit `ratify_strategic_theme` |
| `WithdrawStrategicThemeProposal(ctx, id, userID, isMaintainer)` | proposer or maintainer, pending only |
| `ListStrategicThemes(ctx, projectID)` | **only `expires_at > now`** |
| `ThemeWeightTotal(ctx, projectID, tags)` | sum over live themes whose `tag` is in `tags`; 0 for no tags; expired rows contribute 0 |
| `applyTheme(ctx, tx, p, userID, lifetimeDays)` | upsert one live row per (project, tag, namespace): `ON CONFLICT(project_id, tag, namespace) DO UPDATE SET weight=…, rationale=…, set_by=…, expires_at=…` — needs a UNIQUE index (below) |

Migration half (same file, second statement):

```sql
-- The live side needs the same one-row-per-tag identity the pending side got from
-- idx_theme_one_pending, or a renewal inserts a second row and ThemeWeightTotal counts the
-- tag twice.
CREATE UNIQUE INDEX idx_theme_identity ON strategic_themes(project_id, tag, namespace);

-- Consents are their own table for the reason strategic_weight_consents gives: counting
-- distinct consents by proposal id out of audit_log's JSON detail is a LIKE with no index.
CREATE TABLE strategic_theme_consents (
    proposal_id INTEGER NOT NULL REFERENCES strategic_theme_proposals(id) ON DELETE CASCADE,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    consented_at REAL NOT NULL,
    PRIMARY KEY (proposal_id, user_id)
);
```

**Verify:** `go test ./internal/store/ -run 'Theme' -count=1 -v` → the new tests pass (step 7
adds them; until then this command proves only that the package still compiles, which is why
step 7 is not optional).

## Step 4 — the ranking wire (the point of the pass)

File: `internal/store/features.go` (`FeaturePriorities`, line ~216).

```go
// §6.2's third steering input: the μ term carries the feature's ratified weight AND the sum
// of the live themes its tags match. Both are consensus-ratified, which is why they share
// the term; the sum is recorded as docs/QUESTIONS.md Q2 because §366 names only themes.
themeWeight, err := d.ThemeWeightTotal(ctx, projectID, f.Tags)
if err != nil { return nil, err }
prio := ranking.PriorityScore(f.EloR, f.EloRD, painSum, f.StrategicWeight+themeWeight, charter.Lam, charter.Mu)
```

`FeaturePriority` gains `ThemeWeight float64 \`json:"theme_weight"\`` — a score a reader
cannot explain is the defect this repository keeps finding; the ranking page shows the
components.

**Verify (the load-bearing one):**

```sh
go test ./internal/store/ -run 'TestARatifiedThemeMovesThePriorityScore' -count=1 -v
```
→ `--- PASS`, and the same test with the `+themeWeight` removed must fail (mutation arm in
step 7).

## Step 5 — routes and handlers

File: `internal/httpapi/themes.go` (new) + `internal/httpapi/server.go`.

| method | path | handler | gate |
|---|---|---|---|
| POST | `/api/v1/projects/{project_id}/strategy/themes` | `handleProposeStrategicTheme` | `requireRole("contributor")`; 201 + a message saying **proposed, not applied** |
| POST | `/api/v1/projects/{project_id}/strategy/themes/{id}/consent` | `handleConsentStrategicTheme` | `requireEligibleCollaborator`; 200 ratified / 202 below quorum with the server's count |
| POST | `/api/v1/projects/{project_id}/strategy/themes/{id}/withdraw` | `handleWithdrawStrategicTheme` | actor must be the proposer or a maintainer |
| GET | `/api/v1/projects/{project_id}/strategy/theme-proposals` | `handleListStrategicThemeProposals` | any reader |
| GET | `/api/v1/projects/{project_id}/strategy/themes` | exists | unchanged |

`reader_coverage_test.go` gains rows: the propose/consent/withdraw routes read by
`settings.js` anchored on the **rendered control**, not the attribute name (the §24 lesson: an
anchor the wiring also contains is decoration).

**Verify:** `go test ./internal/httpapi/ -run 'TestEveryAPIRouteIsDeclaredWithAReaderOrAReason|TestEveryMutatingRouteIsGuarded' -count=1` → ok.

## Step 6 — the settings page

File: `internal/httpapi/assets/js/settings.js` (the strategy panel).

- A propose form (`tag`, `namespace`, `weight`, `rationale`) posting to
  `/strategy/themes`, with the weight range in the hint.
- Each pending proposal: **Consent** and **Withdraw** buttons, rendering the server's own
  message for both outcomes — **202 is "recorded, quorum not reached (n/m)"; 200 is
  "ratified"** (collapsing them reports a change that has not happened; §12.4's mutant).
- The live themes list shows weight, tag, and **when it lapses** — a weight with an invisible
  expiry is the mechanism §366 forbids.

**Verify:** `node --check internal/httpapi/assets/js/settings.js` and the e2e in step 7.

## Step 7 — tests and gates

1. `internal/store/themes_test.go` — the eight invariants in the spec's §5, including
   `TestARatifiedThemeMovesThePriorityScore` (feature with tag `security-hardening`; before →
   after) and `TestAnExpiredThemeContributesNothing` (ratify, then backdate `expires_at`,
   then re-read).
2. `internal/store/themes_mutants.json` + a `make gates` arm — three mutants: the theme sum
   dropped from the μ term, the expiry filter removed from `ThemeWeightTotal`, and the
   proposer-cannot-consent guard removed.
3. `internal/httpapi/themes_test.go` — anonymous 401; non-member 403 naming `contributor`;
   propose 201 **and the projects' live themes unchanged**; self-consent 403; consent 202
   then 200 with a second eligible collaborator; weight 99 → 400 naming the range; a second
   pending proposal → 409; the {project_id}/{id} halves tied together (404, not "any
   project's URL").
4. `tests/e2e/settings_e2e.py` — a browser: propose → the proposal appears as pending → the
   consent below quorum says so → ratify → the theme appears in the live list **with its
   expiry** → the ranking page's feature shows a changed `theme_weight`.
5. `tests/e2e/settings_ui_mutants.py` (new) + a Makefile e2e line — two mutants: the propose
   form never posts; the 202 answer treated as ratified.
6. `docs/REMAINING.md` §25 + the §24.7 correction (the *claim* that `strategic_themes` has no
   `expires_at` was wrong — the column is there and unwritten, which is a different defect);
   `docs/QUESTIONS.md` Q2 (the sum) and Q3 (the pain-side multiplier); the measured counts in
   §25, HANDOFF and the tracker's top line.

**Verify:** `make verify` → 0; `make gates` → 0 survived; `make e2e` → 0, all mutants killed.

## Step 8 — ship

`make deploy` (laptop) and `ssh thinkcentre 'cd ~/code/projects/concord && make deploy'`
(public), then the live probe that can actually distinguish: `GET` on the new POST route →
**405** (registered) against a **404** control, because chi's 404 body for an unregistered
path is identical to the project-resolution one.

Then both remotes pushed and the vault note written.

## Risks, stated before the work

- **The sum (D1) is a spec question.** If the owner means themes-only, step 4 loses
  `f.StrategicWeight` and the weight-proposal mechanism becomes inert — a one-line change,
  which is why it is filed rather than buried.
- **`theme_weight` is a new DTO field**, and this repository has four times shipped a field
  that a DTO dropped. The e2e in step 7.4 asserts the value reaches the DOM.
- **A UNIQUE index on `strategic_themes`** will fail to create if a live instance somehow
  holds duplicate (project, tag, namespace) rows. Measured on the live DB before writing it:
  0 rows, so the index is safe here; on another instance the migration would fail loudly
  rather than silently sum twice.
