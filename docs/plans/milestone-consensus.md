# Plan — milestone-level consensus (V6)

Implements `docs/specs/milestone-consensus-spec.md`. Written for an implementer with no context
for the repo: every step names the file, shows the code, and gives the command that proves it.

**Read first:** `§46` of `docs/REMAINING.md`. This surface exists because a status without a
record is a lie; do not add one.

Conventions that will bite:

- `make verify` (static) and `make gates` (mutation) run **serially**, never in one shell —
  `gates` rewrites `internal/httpapi/finder.go` in place.
- Adding tests moves the test count, and `scripts/check_doc_numbers.py` fails on any prose that
  states it: `python3 scripts/check_doc_numbers.py` names every stale `file:line`.
- Migrations are append-only. The next number is **0040**.
- The `{project_id}` path param is a **slug**; numeric ids live in JSON bodies.

---

## M1 — Migration `0040_milestone_arena.sql` (5 min)

Two CHECK constraints have to widen (`arenas.type`, `arena_entries.entity_type`), and SQLite
cannot `ALTER` a CHECK: both tables are rebuilt in place with the 12-step pattern `0018` already
used in this tree.

`internal/db/migrations/0040_milestone_arena.sql`:

```sql
-- V6: a milestone is a thing a project can DECIDE, not only a label it can apply.
--
-- Two CHECK constraints widen here -- arenas.type and arena_entries.entity_type -- and
-- SQLite cannot alter either, so both tables are rebuilt. The copies are column-for-column
-- and were taken from `.schema` on the deployed instance, not from migration 0017 (which
-- 0018 had already rebuilt once).

PRAGMA foreign_keys = OFF;

CREATE TABLE arenas_new (
    id         INTEGER PRIMARY KEY,
    type       TEXT NOT NULL CHECK (type IN
                ('feature-priority','solution','list','request','alternatives','use-case',
                 'milestone')),
    project_id INTEGER REFERENCES projects(id) ON DELETE CASCADE,
    feature_id INTEGER REFERENCES features(id) ON DELETE CASCADE,
    question   TEXT NOT NULL DEFAULT '',
    use_case   TEXT,
    created_at REAL NOT NULL
);
INSERT INTO arenas_new (id, type, project_id, feature_id, question, use_case, created_at)
    SELECT id, type, project_id, feature_id, question, use_case, created_at FROM arenas;
DROP TABLE arenas;
ALTER TABLE arenas_new RENAME TO arenas;

CREATE UNIQUE INDEX idx_arenas_feature_priority
    ON arenas(project_id) WHERE type = 'feature-priority';
CREATE UNIQUE INDEX idx_arenas_solution
    ON arenas(feature_id) WHERE type = 'solution';
CREATE UNIQUE INDEX idx_arenas_use_case
    ON arenas(project_id, use_case) WHERE type = 'use-case' AND use_case IS NOT NULL;
CREATE UNIQUE INDEX idx_arenas_alternatives
    ON arenas(project_id, use_case) WHERE type = 'alternatives' AND use_case IS NOT NULL;
-- ONE milestone arena per project: milestones compete with each other and with doing
-- nothing, which is the whole reason to rank them (spec 4.1).
CREATE UNIQUE INDEX idx_arenas_milestone
    ON arenas(project_id) WHERE type = 'milestone';

CREATE TABLE arena_entries_new (
    arena_id        INTEGER NOT NULL REFERENCES arenas(id) ON DELETE CASCADE,
    entity_type     TEXT NOT NULL CHECK (entity_type IN
                     ('feature','solution','project','list_entry','milestone')),
    entity_id       INTEGER NOT NULL,
    r               REAL NOT NULL DEFAULT 1500,
    rd              REAL NOT NULL DEFAULT 350,
    sigma           REAL NOT NULL DEFAULT 0.06,
    games           INTEGER NOT NULL DEFAULT 0,
    is_baseline     INTEGER NOT NULL DEFAULT 0,
    parent_entry_id INTEGER,
    updated_at      REAL NOT NULL,
    PRIMARY KEY (arena_id, entity_type, entity_id)
);
INSERT INTO arena_entries_new (arena_id, entity_type, entity_id, r, rd, sigma, games,
                               is_baseline, parent_entry_id, updated_at)
    SELECT arena_id, entity_type, entity_id, r, rd, sigma, games,
           is_baseline, parent_entry_id, updated_at FROM arena_entries;
DROP TABLE arena_entries;
ALTER TABLE arena_entries_new RENAME TO arena_entries;

CREATE INDEX idx_arena_entries_entity ON arena_entries(entity_type, entity_id);
CREATE UNIQUE INDEX idx_arena_entries_one_baseline
    ON arena_entries(arena_id) WHERE is_baseline = 1;

PRAGMA foreign_keys = ON;

-- The call's subject, beside the nullable feature_id. ADD COLUMN is safe with a bare
-- REFERENCES: the default is NULL, which is the "not about a milestone" case.
ALTER TABLE consensus_calls ADD COLUMN milestone_id INTEGER REFERENCES milestones(id);
CREATE INDEX idx_calls_milestone ON consensus_calls(milestone_id);
```

**Verify:**

```sh
cd ~/code/projects/concord && go test ./internal/db/ -count=1
sqlite3 /tmp/m40.db < /dev/null   # (scratch DB is created by the test run)
```

Expected: `ok  git.polarisocial.xyz/concord/concord/internal/db` and, on a migrated DB,
`sqlite3 x.db ".schema arenas" | grep -c milestone` → `2` (the CHECK value and the index line).

---

## M2 — Store constants and the arena (5 min)

`internal/store/arenas.go`, beside the existing constants at lines 45–56:

```go
// ArenaMilestone ranks a project's milestones against each other and against doing
// nothing (§4.3's baseline). One per project, like feature-priority, because a rating
// with no competitor answers no question.
ArenaMilestone = "milestone"

// EntityMilestone is the entry type inside it.
EntityMilestone = "milestone"
```

Then, beside `EnsureArena`:

```go
// EnsureMilestoneArena returns the project's milestone arena, creating it on first use.
//
// The question is fixed rather than passed in: a milestone arena always answers the same
// thing, and a per-call question would let two callers disagree about what the ratings mean.
func (d *DB) EnsureMilestoneArena(ctx context.Context, projectID int64) (Arena, error) {
	return d.EnsureArena(ctx, ArenaMilestone, projectID, 0, "",
		"Which milestone should this project deliver next?")
}

// UpsertMilestoneEntry puts a milestone into the project's arena at the prior if it is not
// already there, and returns its current rating.
func (d *DB) UpsertMilestoneEntry(ctx context.Context, projectID, milestoneID int64) (ArenaEntry, error) {
	arena, err := d.EnsureMilestoneArena(ctx, projectID)
	if err != nil {
		return ArenaEntry{}, err
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO arena_entries (arena_id, entity_type, entity_id, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(arena_id, entity_type, entity_id) DO NOTHING`,
		arena.ID, EntityMilestone, milestoneID, float64(time.Now().Unix())); err != nil {
		return ArenaEntry{}, fmt.Errorf("place milestone in its arena: %w", err)
	}
	return d.GetArenaEntry(ctx, arena.ID, EntityMilestone, milestoneID)
}
```

**Verify:** `go build ./... && go vet ./internal/store/` → no output.

---

## M3 — Readiness with three named conditions (15 min)

`internal/store/milestone_consensus.go` (new file):

```go
package store

import (
	"context"
	"fmt"
	"strings"
)

// MilestoneCallReadiness answers "may this milestone be decided?" and says which condition
// failed (§4.3). Reported condition by condition for the same reason SolutionCallReadiness
// is: "the conditions are not met" is indistinguishable from "this milestone is broken".
type MilestoneCallReadiness struct {
	MilestoneID int64        `json:"milestone_id"`
	Title       string       `json:"title"`
	OpenCallID  int64        `json:"open_call_id,omitempty"`
	Ready       bool         `json:"ready"`
	Conditions  []MilestoneCondition `json:"conditions"`
}

type MilestoneCondition struct {
	Name   string `json:"name"`
	Met    bool   `json:"met"`
	Detail string `json:"detail"`
}

// milestoneDeliverableStatuses are the phases in which a feature is close enough to count
// as something a project can promise. A draft is not: promising work nobody designed is
// how a roadmap becomes fiction.
var milestoneDeliverableStatuses = map[string]bool{
	"ready": true, "in_progress": true, "review": true, "shipped": true,
}

func (d *DB) MilestoneCallReadiness(ctx context.Context, projectID, milestoneID int64) (MilestoneCallReadiness, error) {
	// GetMilestone takes the project as well as the id, and that is the scoping rule this
	// whole tree follows: a milestone of another project answers ErrNotFound, never its row.
	m, err := d.GetMilestone(ctx, projectID, milestoneID)
	if err != nil {
		return MilestoneCallReadiness{}, err
	}
	out := MilestoneCallReadiness{MilestoneID: m.ID, Title: m.Title}

	features, err := d.MilestoneFeatures(ctx, projectID, milestoneID)
	if err != nil {
		return MilestoneCallReadiness{}, err
	}

	// 1. grouped
	grouped := MilestoneCondition{Name: "held a group of features", Met: len(features) > 0}
	if grouped.Met {
		grouped.Detail = fmt.Sprintf("%d feature(s)", len(features))
	} else {
		grouped.Detail = "this milestone holds no features, so there is nothing to promise"
	}

	// 2. deliverable -- names the FIRST offender, because a list of nine drafts does not
	// tell a reader what to fix.
	deliverable := MilestoneCondition{Name: "every feature is deliverable", Met: true,
		Detail: "every feature is ready, in progress, in review or shipped"}
	var undeliverable []string
	for _, f := range features {
		if !milestoneDeliverableStatuses[f.Status] {
			undeliverable = append(undeliverable, fmt.Sprintf("%q (%s)", f.Title, f.Status))
		}
	}
	if len(undeliverable) > 0 {
		deliverable.Met = false
		deliverable.Detail = fmt.Sprintf("%d feature(s) are not deliverable, first: %s",
			len(undeliverable), undeliverable[0])
	}

	// 3. dated
	dated := MilestoneCondition{Name: "has a target date", Met: m.TargetDate != nil}
	if dated.Met {
		dated.Detail = "target date set"
	} else {
		dated.Detail = "no target date: a promise with no date is not a promise"
	}

	out.Conditions = []MilestoneCondition{grouped, deliverable, dated}
	out.Ready = true
	for _, c := range out.Conditions {
		if !c.Met {
			out.Ready = false
		}
	}

	// An already-open call is reported, not counted as a failed condition: the milestone is
	// perfectly decidable, somebody is deciding it.
	var openID int64
	if err := d.QueryRowContext(ctx, `
		SELECT id FROM consensus_calls
		WHERE milestone_id = ? AND status = 'open' ORDER BY id DESC LIMIT 1`,
		milestoneID).Scan(&openID); err == nil {
		out.OpenCallID = openID
	}
	return out, nil
}

// unmetMilestoneCondition renders the first failing condition for an error message.
func unmetMilestoneCondition(r MilestoneCallReadiness) string {
	for _, c := range r.Conditions {
		if !c.Met {
			return fmt.Sprintf("%s (%s)", c.Name, c.Detail)
		}
	}
	return "unknown"
}

var _ = strings.TrimSpace // keep the import honest if the body above changes
```

**The names this file calls, verified against the tree on 2026-11-12** (`GetMilestone(ctx, projectID, id)`,
`MilestoneFeatures(ctx, projectID, milestoneID)`, `GetArenaEntry(ctx, arenaID, entityType, entityID)`,
`MilestonePlanned/Active/Done/Cancelled`, `Milestone.TargetDate *float64`):

```sh
grep -n "func (d \*DB) GetMilestone\b\|func (d \*DB) MilestoneFeatures\|func (d \*DB) GetArenaEntry" internal/store/*.go
```

If any is missing, use the real one and say so in the commit message — a plan is a claim set.

**Tests** (`internal/store/milestone_consensus_test.go`), one per condition, each asserting the
condition's OWN name appears in the refusal: `TestAMilestoneOfDraftsIsNotDeliverable`,
`TestAnUndatedMilestoneIsNotAPromise`, `TestAnEmptyMilestoneIsNotAGroup`.

**Verify:** `go test ./internal/store/ -run 'Milestone' -count=1` → `ok`.

---

## M4 — Opening a call (15 min)

Append to `internal/store/milestone_consensus.go`:

```go
// OpenMilestoneCall opens a consensus call on a milestone (§4.3).
func (d *DB) OpenMilestoneCall(ctx context.Context, projectID, milestoneID, openedBy int64) (ConsensusCall, error) {
	r, err := d.MilestoneCallReadiness(ctx, projectID, milestoneID)
	if err != nil {
		return ConsensusCall{}, err
	}
	if !r.Ready {
		return ConsensusCall{}, fmt.Errorf("%w: %s", ErrNotCallable, unmetMilestoneCondition(r))
	}
	return d.createMilestoneCall(ctx, projectID, milestoneID, openedBy, false, nil)
}

// OpenMilestoneCallEarly is the discretionary path: a stated reason waives the conditions,
// and the call records that it did.
func (d *DB) OpenMilestoneCallEarly(ctx context.Context, projectID, milestoneID, openedBy int64, reason string) (ConsensusCall, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ConsensusCall{}, fmt.Errorf(
			"%w: opening before the conditions hold needs a stated reason", ErrInvalid)
	}
	role, err := d.GetRoleForProject(ctx, projectID, openedBy)
	if err != nil {
		return ConsensusCall{}, err
	}
	if !eligibleCollaboratorRole(role) {
		return ConsensusCall{}, fmt.Errorf("%w: role %q", ErrNotEligibleCollaborator, role)
	}
	return d.createMilestoneCall(ctx, projectID, milestoneID, openedBy, true, &reason)
}

// createMilestoneCall is the shared insert. It refuses a milestone that is already decided
// and one that already has an open call -- the two states in which a new call would mean
// two answers to one question.
func (d *DB) createMilestoneCall(ctx context.Context, projectID, milestoneID, openedBy int64,
	early bool, reason *string) (ConsensusCall, error) {

	m, err := d.GetMilestone(ctx, projectID, milestoneID)
	if err != nil {
		return ConsensusCall{}, err
	}
	switch m.Status {
	case MilestonePlanned, MilestoneActive:
	default:
		return ConsensusCall{}, fmt.Errorf(
			"%w: milestone is %q, so it is not up for a decision", ErrInvalid, m.Status)
	}
	var open int
	if err := d.QueryRowContext(ctx, `
		SELECT count(*) FROM consensus_calls WHERE milestone_id = ? AND status = 'open'`,
		milestoneID).Scan(&open); err != nil {
		return ConsensusCall{}, fmt.Errorf("count open calls: %w", err)
	}
	if open > 0 {
		return ConsensusCall{}, fmt.Errorf(
			"%w: a consensus call is already open on this milestone", ErrConflict)
	}

	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO consensus_calls
			(project_id, feature_id, milestone_id, opened_by, opens_at, closes_at, status,
			 question, description, opened_early, early_reason)
		VALUES (?, NULL, ?, ?, ?, ?, 'open', ?, ?, ?, ?)`,
		m.ProjectID, milestoneID, openedBy, now, now,
		"Deliver "+m.Title+"?", m.Body, boolToInt(early), reason)
	if err != nil {
		return ConsensusCall{}, fmt.Errorf("open milestone call: %w", err)
	}
	id, _ := res.LastInsertId()
	return d.GetConsensusCall(ctx, id)
}
```

`boolToInt` does NOT exist — the tree already has `boolInt(b bool) int` at
`internal/store/store.go:924`. Call that one; do not add a second helper with a different name
for the same conversion (a grep for `func .*bool.*int` finds it).

**Verify:** `go test ./internal/store/ -run 'MilestoneCall' -count=1` → `ok`, with tests
asserting: early-with-no-reason → `ErrInvalid`; early-with-reason → `opened_early == 1`; second
call → `ErrConflict` naming the open call; `done` milestone → `ErrInvalid` naming `"done"`.

---

## M5 — The call's subject: one of two, never both (10 min)

`internal/store/consensus.go`, inside `CreateConsensusCall` where `featureID != 0` is validated,
and again in `createMilestoneCall`: a caller that passes both must be refused, not silently
truncated.

Add to `CreateConsensusCall`'s signature is NOT needed — instead add one guard where the insert
happens, so it covers every path:

```go
// Exactly one subject. A call about a feature AND a milestone is a call about two things,
// and whichever column a reader joined on would be half the answer.
if featureID != 0 && milestoneID != 0 { ... }
```

The simplest implementation that keeps the existing signature: `CreateConsensusCall` keeps its
4-argument form (no milestone), and `createMilestoneCall` is the only writer of
`milestone_id` — so "both set" cannot arise from either. Enforce it where it CAN arise, on the
HTTP body, in M7, and assert it there. Write the assertion now as a store test that calls the
proof: `TestACallCannotBeAboutAFeatureAndAMilestone` drives the HTTP handler (M7) and expects
`400`.

---

## M6 — The outcome moves the milestone, and nothing else (15 min)

`internal/store/milestone_consensus.go`:

```go
// milestonePhaseForOutcome maps a §6.5 outcome onto the milestone's status (§4.4).
//
// sent-back returns ok=false on purpose: the call decided not to decide, the milestone stays
// where it was, and NO audit row is written. The feature ladder has a phase to fall back to
// ('discussion') and milestones do not -- inventing one to have something to write is the
// defect §46 documents.
func milestonePhaseForOutcome(outcome string) (status string, ok bool) {
	switch outcome {
	case OutcomeAccepted, OutcomeAmended, OutcomeFallBack:
		return MilestoneActive, true
	case OutcomeRejected:
		return MilestoneCancelled, true
	}
	return "", false
}

// setMilestonePhase writes the status and the audit row together, in one place.
func (d *DB) setMilestonePhase(ctx context.Context, projectID, milestoneID int64, to, why string) error {
	m, err := d.GetMilestone(ctx, projectID, milestoneID)
	if err != nil {
		return err
	}
	if m.Status == to {
		return nil
	}
	if _, err := d.ExecContext(ctx, `UPDATE milestones SET status = ? WHERE id = ?`, to, milestoneID); err != nil {
		return fmt.Errorf("move milestone %d to %s: %w", milestoneID, to, err)
	}
	return d.AddAudit(ctx, projectID, 0, "milestone.status", "milestone", milestoneID,
		fmt.Sprintf("%s -> %s: %s", m.Status, to, why))
}
```

Then in `RecordCallOutcome` (`internal/store/consensus_solutions.go`), before the
`solution_id == nil` refusal, branch:

```go
	// A milestone call has no solution and no feature: its subject is the milestone, and
	// §4.4's mapping is the whole of what an outcome does.
	if call.MilestoneID != nil {
		if status, ok := milestonePhaseForOutcome(outcome); ok {
			if err := d.setMilestonePhase(ctx, call.ProjectID, *call.MilestoneID,
				status, fmt.Sprintf("%s: call #%d", outcome, call.ID)); err != nil {
				return ConsensusCall{}, err
			}
		}
		return d.closeCallWithOutcome(ctx, callID, outcome, summary, now)
	}
```

`closeCallWithOutcome` is the existing five-line UPDATE extracted into a helper so both branches
share it — extract it from the `UPDATE consensus_calls SET status='closed' ...` block at
`consensus_solutions.go:530`. `ConsensusCall` must gain `MilestoneID *int64`, scanned in
`GetConsensusCall` and listed in its SELECT.

**Verify:**

```sh
go test ./internal/store/ -run 'Milestone' -count=1
```

Expected: `ok`, with these tests green:
`TestAnAcceptedMilestoneCallActivatesIt`, `TestARejectedMilestoneCallCancelsIt`,
`TestASentBackMilestoneCallWritesNothing` (asserts **no** `milestone.status` row),
`TestNoFeatureChangesStatusWhenAMilestoneIsDecided` (reads every feature of the milestone
before and after — non-goal §3, the one a future contributor will try to "improve").

---

## M7 — Routes (15 min)

`internal/httpapi/server.go`, inside the existing `/api/v1/projects/{project_id}` subtree, near
the milestone routes:

```go
	r.Route("/milestones/{id}/consensus", func(r chi.Router) {
		// Readiness is a public read: it is the same "why has no call opened yet" view as
		// the feature-level one, and it leaks nothing a milestone page does not already show.
		r.Get("/", s.handleMilestoneCallReadiness)
		r.Post("/", s.handleOpenMilestoneCall)
	})
```

`internal/httpapi/milestones.go` (append; the handlers in the same file already resolve the
project from the slug):

```go
func (s *Server) handleMilestoneCallReadiness(w http.ResponseWriter, r *http.Request) {
	milestoneID, ok := s.milestoneInProject(w, r)
	if !ok {
		return
	}
	readiness, err := s.Store.MilestoneCallReadiness(r.Context(), milestoneID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, readiness)
}

func (s *Server) handleOpenMilestoneCall(w http.ResponseWriter, r *http.Request) {
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	milestoneID, ok := s.milestoneInProject(w, r)
	if !ok {
		return
	}
	var req struct {
		Early  bool   `json:"early"`
		Reason string `json:"reason"`
	}
	if r.ContentLength > 0 && !readJSON(w, r, &req) {
		return
	}
	var (
		call store.ConsensusCall
		err  error
	)
	if req.Early {
		call, err = s.Store.OpenMilestoneCallEarly(r.Context(), milestoneID, actorID, req.Reason)
	} else {
		call, err = s.Store.OpenMilestoneCall(r.Context(), milestoneID, actorID)
	}
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"call": call})
}
```

`milestoneInProject` = the existing pattern: `requireProjectID` + parse `{id}` + verify the
milestone's `project_id` matches, answering 404 for a milestone of another project —
**never** load a milestone by id alone (the consensus handlers' old leak, spec §11).

Also in `handleCreateConsensus`, refuse a body naming BOTH `feature_id` and `milestone_id` with
`400`, and accept `milestone_id` by delegating to `OpenMilestoneCall`.

**Verify** (against a scratch instance, never the deployed one):

```sh
go test ./internal/httpapi/ -run 'MilestoneConsensus' -count=1
```

Expected tests: `TestOpeningAMilestoneCallAnswers201Then409`, `TestTheReadinessRouteNamesTheFailingCondition`,
`TestAnotherProjectsMilestoneAnswers404`, `TestACallCannotBeAboutAFeatureAndAMilestone`.

---

## M8 — The page says what the record says (20 min)

`internal/httpapi/templates/milestone.html` gains a decision block, rendered from the same
readiness struct the API returns (one computation, two renderings):

```html
<section class="panel" id="milestone-decision" aria-labelledby="milestone-decision-head">
  <header class="section-head"><h2 id="milestone-decision-head">Decision</h2></header>
  {{if .Decision.OpenCallID}}
    <p>Open since the call was opened: <a href="/projects/{{.Slug}}/consensus?call={{.Decision.OpenCallID}}">call {{.Decision.OpenCallID}}</a>.</p>
  {{else}}
    <ul class="conditions">
      {{range .Decision.Conditions}}
        <li class="{{if .Met}}met{{else}}unmet{{end}}">{{.Name}} — {{.Detail}}</li>
      {{end}}
    </ul>
    <button class="btn" id="open-milestone-call" data-ready="{{.Decision.Ready}}"
            {{if not .Decision.CanOpen}}hidden{{end}}>Open a decision on this milestone</button>
  {{end}}
</section>
```

`internal/httpapi/assets/js/milestones.js` (existing file) wires the button with the pattern the
feature page already uses — `sendAs('POST', ...)` with the token from `session.js`, then a
reload so the server-rendered state is what the reader sees:

```js
wireMilestoneDecision() {
  const btn = document.getElementById('open-milestone-call');
  if (!btn) return;
  btn.addEventListener('click', async () => {
    btn.disabled = true;
    btn.setAttribute('aria-busy', 'true');
    const res = await sendAs('POST', `/api/v1/projects/${SLUG}/milestones/${ID}/consensus`, {});
    btn.setAttribute('aria-busy', 'false');
    if (res.ok) { location.reload(); return; }
    btn.disabled = false;
    showError(res.body?.error ?? 'could not open a decision');
  });
}
```

`reader_coverage_test.go` needs a row for each new route in the same commit — `make verify`
fails without it (Match / Reader / Anchor / Routes).

**Verify:** `make verify` → `all checks passed`; then the browser walk in M9.

---

## M9 — Gates, docs, live (30 min)

1. `internal/store/milestone_consensus_mutants.json` — one arm per rule marked "(arm)" in the
   spec: `deliverable` forced true, `dated` forced true, sent-back writing a status, the
   both-subjects guard dropped, the one-open-call check loosened to `> 1`, and the cascade
   non-goal (`setMilestonePhase` also walking its features). Wire two
   `python3 internal/mutate.py ...` lines into `make gates` beside the other store gates.
2. `python3 scripts/check_doc_numbers.py` — fix every stale count it names (the test count moves).
3. `docs/REMAINING.md` §47 — the as-built record, written AFTER the walk, with the curl output.
4. `docs/PLAN-r4.md` — the R17 row loses "except a milestone-level consensus call"; the
   "where the next milestone should go" block names the one after this.
5. Deploy (`ssh thinkcentre 'cd /personal/documents/code/projects/concord && make deploy'`), then
   the live walk: open a call on a probe milestone, read it back, refuse the second, record
   `accepted`, confirm `active` + the audit row, and confirm **no feature changed status**.
   Paste the transcript into §47 and delete the probe rows.

Done when: `make verify`, `make gates`, `make e2e` all green on one commit, the live transcript
is in `REMAINING`, and the four success criteria of the spec are each answered by a line of it.
