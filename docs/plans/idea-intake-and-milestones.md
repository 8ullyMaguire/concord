# Plan — intake modes (M1) and milestones (M2)

**For an implementer with no context.** The spec is
`docs/specs/idea-intake-and-milestones-spec.md`; read §1 (the measurement), §3 (M1) and §4 (M2)
first. This plan is the *how*: exact files, exact code, and for every step the command that proves
it. M3 (roadmap) and M4 (dependencies) are §7 below: route lists and invariants, not full code.

## 0. Ground rules that will cost you a day if skipped

1. **Migrations are append-only.** Add `0036_*.sql` and `0037_*.sql`; never edit `0001_init.sql`.
   The tree's last migration is `0035_curation_reviews.sql`, so `0036` is next.
2. **A new page template must be added to the `pages` list** in `internal/httpapi/server.go:241`, or
   the route renders **500** rather than blank.
3. **Every API route needs a reader row** in `internal/httpapi/reader_coverage_test.go`'s
   `readerInventory` (Match / Reader / Anchor / Routes / Note), in the SAME commit. `Routes` is a
   MEASURED count; adding routes under a covered prefix means bumping it deliberately.
4. **`make verify` then `make gates`, serially, never in one shell.** `gates` rewrites
   `internal/httpapi/finder.go` in place and a concurrent `verify` produces a failure that reproduces
   in neither target alone.
5. **Run `make verify` on the untouched tree first.** Without that baseline a red gate after your
   edit is unattributable.
6. **Adding tests moves the test count** and `docs/HANDOFF.md` states it — grep the docs for the old
   number in the same commit.
7. `{project_id}` in a URL is a **slug**; only JSON bodies carry the numeric id.

## M1 — the intake mode

### S1. Migration `0036_intake_modes.sql`

`internal/db/migrations/0036_intake_modes.sql`:

```sql
-- Intake modes (spec docs/specs/idea-intake-and-milestones-spec.md §3).
--
-- The default is the behaviour that already exists, so this migration changes no
-- project's semantics: every project on the instance is complaint_first, and the
-- 332 features on project `concord` all carry a complaint. The column exists so a
-- project can say otherwise, not so it can drift silently.
ALTER TABLE projects ADD COLUMN intake_mode TEXT NOT NULL DEFAULT 'complaint_first'
    CHECK (intake_mode IN ('complaint_first','idea_first','both'));
```

**Verify:**

```bash
go test ./internal/db/ -run TestMigrations -count=1     # schema applies to :memory:
```

Expected: `ok  git.polarisocial.xyz/concord/concord/internal/db`.
If a `TestSchemaVersion`-style assertion names the count, bump it in the same commit.

### S2. `Project.IntakeMode` — three edits in one field

`internal/store/store.go`:

1. Add to the struct, after `GovernanceModel`:

```go
	// IntakeMode decides whether a feature must trace to a complaint (spec
	// §3.1): complaint_first (the default and the product's premise),
	// idea_first (ideas may be filed before anyone is hurt) or both.
	IntakeMode string `json:"intake_mode"`
```

2. Append `p.intake_mode` to the `projectColumns` constant (find it above `GetProject`, ~line 430).
3. Add `&p.IntakeMode` to `scanProject`, in the SAME position as the column you appended.

**Verify:**

```bash
go test ./internal/store/ -run TestGetProject -count=1
curl -s https://concord.polarisocial.xyz/api/v1/projects/concord | grep -o '"intake_mode":"[a-z_]*"'
```

Expected in five minutes of work: the test passes, and after a deploy the live JSON carries
`"intake_mode":"complaint_first"` for all 73 projects.

### S3. `CreateFeature` reads the mode

`internal/store/features.go:73`. Replace the first guard:

```go
	if projectID == 0 || authorID == 0 {
		return Feature{}, fmt.Errorf("%w: project_id and author_id are required", ErrInvalid)
	}
	// §6.2's traceability rule is per PROJECT, not per instance (spec §3.1). The
	// old message stated the rule as though it were the platform's law, which was
	// true when every project was complaint_first and became wrong the moment a
	// project could say otherwise.
	if len(linkedComplaints) == 0 {
		mode, err := d.projectIntakeMode(ctx, projectID)
		if err != nil {
			return Feature{}, err
		}
		if mode == IntakeComplaintFirst {
			return Feature{}, fmt.Errorf(
				"%w: project %d is complaint_first, so a feature needs at least one validated "+
					"complaint; an owner or maintainer can change the project's intake mode", ErrInvalid, projectID)
		}
	}
```

and add, in the same file:

```go
// Intake modes (spec §3.1). Constants rather than literals because the CHECK
// constraint, the handler's message and the tests must agree on the spelling.
const (
	IntakeComplaintFirst = "complaint_first"
	IntakeIdeaFirst      = "idea_first"
	IntakeBoth           = "both"
)

func (d *DB) projectIntakeMode(ctx context.Context, projectID int64) (string, error) {
	var mode string
	err := d.QueryRowContext(ctx,
		`SELECT intake_mode FROM projects WHERE id = ?`, projectID).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: project %d", ErrNotFound, projectID)
	}
	return mode, err
}
```

**Verify** — a store test that fails on the unfixed code:

```go
func TestIntakeModeDecidesWhetherAComplaintIsRequired(t *testing.T) {
	cases := []struct {
		mode      string
		complaint bool
		wantErr   bool
	}{
		{IntakeComplaintFirst, false, true},
		{IntakeComplaintFirst, true, false},
		{IntakeIdeaFirst, false, false},
		{IntakeBoth, false, false},
	}
	// … setup a project, UPDATE projects SET intake_mode=?, call CreateFeature
	// with or without a linked complaint, and assert the error.
}
```

```bash
go test ./internal/store/ -run TestIntakeModeDecides -count=1 -v
```

Expected: PASS. **Then mutate it:** change `mode == IntakeComplaintFirst` to `mode !=
IntakeComplaintFirst` and re-run — the `complaint_first`/`complaint` case must fail. A test that
passes with the branch inverted is not a test.

### S4. The `ready` gate

`internal/httpapi/trust.go`'s `handleSetFeatureStatus` calls `s.Store.UpdateFeatureStatus`. Put the
rule in the store, not the handler, so a second caller cannot bypass it:

```go
// UpdateFeatureStatus applies §6.2's evidence rule where work actually moves:
// an idea may be filed without a complaint (spec §3.1) but it may not be
// COMMITTED to without one unless its project is idea_first.
func (d *DB) UpdateFeatureStatus(ctx context.Context, featureID int64, status string) error {
	if status == "ready" {
		f, err := d.GetFeature(ctx, featureID)
		if err != nil {
			return err
		}
		n, err := d.countLinkedComplaints(ctx, featureID)
		if err != nil {
			return err
		}
		if n == 0 {
			mode, err := d.projectIntakeMode(ctx, f.ProjectID)
			if err != nil {
				return err
			}
			if mode != IntakeIdeaFirst {
				return fmt.Errorf("%w: a feature reaches ready with a validated complaint, "+
					"or in a project whose intake mode is idea_first", ErrInvalid)
			}
		}
	}
	now := float64(time.Now().Unix())
	_, err := d.ExecContext(ctx, `UPDATE features SET status=?, updated_at=? WHERE id=?`, status, now, featureID)
	return err
}
```

**Verify:** `go test ./internal/store/ -run TestReadyRequiresEvidence -count=1` — three cases
(complaint_first without a complaint → error; idea_first without → ok; with → ok).

### S5. The surfaces: form, badge, audit kind

- `internal/httpapi/templates/feature.html` — above the complaint picker, render the mode from the
  page payload; hide the picker when the mode is `idea_first`.
- `internal/httpapi/templates/project.html` — one line beside the governance model.
- The badge: `feature.html` and `assets/js/*` list rows read the linked-complaint count the page
  already loads; **no new fetch**, the same rule the ranking page's `data-can-moderate` follows.
- Mode changes go through the curation queue as a new proposal kind (V6) — **do this last**, and only
  if the curation kinds list already tolerates a fourth (`internal/store/curation.go`'s
  `ModerationActions`-style resolver is the model). If it does not, defer it and record why in
  `docs/REMAINING.md`.

**Verify:** `make verify` green; then a browser check that an `idea_first` project's form has no
complaint picker and a `complaint_first` project's still does.

## M2 — milestones

### S6. Migration `0037_milestones.sql`

```sql
-- Milestones (spec §4.1). Grouping, which the schema has never had: phases live
-- in board_columns and themes in tags, so a release a maintainer is working
-- towards had no representation and the roadmap lived in Markdown.
CREATE TABLE milestones (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    body        TEXT NOT NULL DEFAULT '',
    -- Nullable on purpose: a milestone dated by wish is a deadline nobody agreed.
    target_date REAL,
    status      TEXT NOT NULL DEFAULT 'planned'
                CHECK (status IN ('planned','active','done','cancelled')),
    created_at  REAL NOT NULL,
    UNIQUE (project_id, title)
);
-- Nullable on purpose too: the features no milestone claims ARE the backlog, and
-- a default milestone would make that set unrepresentable.
ALTER TABLE features ADD COLUMN milestone_id INTEGER REFERENCES milestones(id) ON DELETE SET NULL;
CREATE INDEX features_milestone ON features(milestone_id);
```

**Verify:** `go test ./internal/db/ -count=1`; then on a copy of the live DB
(`sqlite3 live.db < 0037_milestones.sql`), `select count(*) from features where milestone_id is
null` must equal the feature count (332 on `concord`) — nothing is claimed by default.

### S7. Store: `internal/store/milestones.go`

```go
type Milestone struct {
	ID         int64    `json:"id"`
	ProjectID  int64    `json:"project_id"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	TargetDate *float64 `json:"target_date"` // null = no date agreed
	Status     string   `json:"status"`
	CreatedAt  float64  `json:"created_at"`

	// Counts are computed, never stored: a stored total drifts the first time a
	// feature leaves the milestone by another path.
	Open     int `json:"open"`
	Shipped  int `json:"shipped"`
	Total    int `json:"total"`
	SeededOpen int `json:"seeded_open"` // open features with no creation event (spec §4.3)
}

func (d *DB) CreateMilestone(ctx context.Context, projectID int64, title, body string, target *float64) (Milestone, error)
func (d *DB) ListMilestones(ctx context.Context, projectID int64) ([]Milestone, error)
func (d *DB) GetMilestone(ctx context.Context, projectID, id int64) (Milestone, error)
func (d *DB) UpdateMilestone(ctx context.Context, projectID, id int64, title, body string, target *float64) error
func (d *DB) SetMilestoneStatus(ctx context.Context, projectID, id int64, status string) error
func (d *DB) SetFeatureMilestone(ctx context.Context, projectID, featureID int64, milestoneID *int64) error
func (d *DB) CloseMilestone(ctx context.Context, projectID, id int64, successor *int64, actor int64) (moved int, err error)
```

Two rules the SQL must carry, both a spec invariant:

- **`GetMilestone` and `SetFeatureMilestone` scope by `project_id`**, so another project's milestone
  answers `ErrNotFound` rather than 403 — an existence oracle is a permission bug (spec invariant 6).
- **`ListMilestones` orders `target_date IS NULL, target_date, id`** — SQLite's `NULLS LAST` is not
  available everywhere, and the `IS NULL` term puts undated milestones last without a dialect
  special case.

`CloseMilestone` runs one `UPDATE features SET milestone_id=?` plus one `AddAudit` per moved feature
inside a transaction; `successor == nil` (a `cancelled` close) moves none.

**Verify:** `go test ./internal/store/ -run TestMilestone -count=1 -v`, with a case per invariant 6–9.

### S8. Handler + routes

`internal/httpapi/milestones.go`, and in `server.go`'s project subtree:

```go
	r.Route("/milestones", func(r chi.Router) {
		r.Get("/", s.handleListMilestones)
		r.Post("/", s.handleCreateMilestone)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", s.handleGetMilestone)
			r.Patch("/", s.handleUpdateMilestone)
			r.Put("/status", s.handleSetMilestoneStatus)
			r.Post("/close", s.handleCloseMilestone)
		})
	})
	// inside the existing features/{id} subtree:
	//   r.Put("/milestone", s.handleSetFeatureMilestone)
```

Writes gate on `s.requireRole(r, projectID, "maintainer")`; reads use `requireProjectID` +
`projectReadable` (the page pattern, `projects.go:71`).

**Verify:** `go test ./internal/httpapi/ -run TestMilestoneRoutes -count=1`, plus
`reader_coverage_test.go`'s inventory gets ONE row per new surface with the measured `Routes` count —
the gate prints the number it measured when it disagrees.

### S9. The page

`templates/milestone.html` + a row in `server.go:241`'s `pages` list + `GET
/projects/{slug}/milestones/{id}` via `handleMilestonePage`. Sections: title/status/target, progress
(§S10), and the features grouped by status reusing the grouping the board page already computes.

**Verify:** `curl -s -o /dev/null -w "%{http_code}" .../projects/concord/milestones/1` → `200`, and
the body is longer than the SPA shell (a 500 renders a short error page; a page whose `pages` entry is
missing renders 500, not blank).

### S10. Progress that excludes the import

In `internal/store/milestones.go`'s aggregate, count a feature as shipped only when a
`status -> shipped` transition exists in `audit_log` for it (`detail LIKE '%-> shipped%'` is the
wrong shape — parse `detail` structurally, as `internal/store/stats.go` does), and return
`SeededOpen` beside the counts. The page prints "N of M shipped, K with no creation event" so a
milestone made entirely of imported rows cannot look delivered.

**Verify:** a store test with one seeded `shipped` feature and one audited one; the shipped count must
be 1, and a mutation that drops the audit join must break it.

### S11. Board filter and overview line

- `handleBoardPage`/board read: a `milestone` query parameter, composing with `phase` by AND.
- `templates/project.html`: one line naming the earliest open milestone and its remaining count,
  computed from the `ListMilestones` the page already needs — no second query.

**Verify:** `pytest tests/e2e/board_milestone_e2e.py -q` (new): a board filtered to one milestone
shows only its cards, and the WIP limit still reads from `board_columns`.

### S12. Board cards: nothing to do, and that is the finding

A step was planned here — place a `board_cards` row for every new feature — and **measuring the board
page deleted it.** `GetBoard` returns a derived card for every feature (362 render in `inbox` on the
live project while `board_cards` holds 20 rows, because a row is written only when somebody moves a
card). A row per feature would duplicate the renderer and make "placed" meaningless; the idea is
filed at V3 and rejected in `docs/IDEAS.md`.

What remains for whoever implements M2 is a display question, not a placement one: a derived card and
a placed row look identical to a reader and neither is identified as such in `GET .../board`'s
response. If milestone progress is to distinguish "somebody chose this phase" from "it defaulted",
that distinction has to appear in the response first — and the column that carries it already exists
(`board_cards.entered_at`), so it is a `LEFT JOIN` and a boolean, not a write path.

## M3 — roadmap (§5 of the spec), route list only

`GET /api/v1/projects/{slug}/roadmap` (milestones with counts, ordered by target date),
`/projects/{slug}/roadmap` page (page + `pages` entry + reader row), slippage from `audit_log` and
`target_date`. Invariants: the page and the route read ONE shape; a project with zero milestones shows
an empty state naming what to create; slippage is `null` rather than `0.0` for a milestone with no
shipment (the absence rule `internal/discovery/responsiveness.go`'s `UnknownReviewHours` established).

## M4 — dependencies (§6), route list only

`feature_deps(blocker_id, blocked_id)` with both FKs and `UNIQUE(blocker_id, blocked_id)`;
`PUT/DELETE /api/v1/projects/{slug}/features/{id}/deps`; the `in_progress` refusal in
`UpdateFeatureStatus`; cycle refusal naming the path. Invariants: a cycle is refused with the cycle in
the message; a blocked feature cannot enter `in_progress` and can once its blocker ships.

## The ladder, in this order

```bash
make verify        # go vet, gofmt, go test ./..., build, docs-check, e2e-sync-check
make gates         # mutation arms + every e2e suite; rewrites finder.go in place
make e2e           # only if gates is not being run
```

`make verify` first, alone. `make gates` second, alone. A commit per step where the step is
independently verifiable; a commit per milestone where it is not. Then `git fetch forgejo`, read BOTH
`forgejo/main..HEAD` and `HEAD..forgejo/main`, and push `forgejo HEAD:main` and
`github HEAD:master`.
