# Intake modes and milestones: an idea does not have to be pain, and a plan is a group

**Status:** spec, 2026-11-12. **Not implemented.** The idea inventory is filed on the live instance
(`seed/concord_ideas.json`, 38 rows, `docs/IDEAS.md`, `docs/REMAINING.md` §42); M1 and M2 have an
implementable plan in `docs/plans/idea-intake-and-milestones.md`. M3 and M4 are specified here and
planned there only to the depth of a route list and an invariant set.

**Supersedes:** nothing. Extends `docs/concord-spec-r4.md` §6.2 (a feature traces to a validated
complaint) with the condition under which that rule applies, and adds the grouping §4.5's
contribution surfaces have been missing.

## 1. What was measured, and what it licenses

The premise this spec answers is not a preference; it is the shape of the data on the live instance:

| measurement | value | query |
|---|---|---|
| features on project `concord` | 332 | `select count(*) from features where project_id=49` |
| of those, with no linked complaint | **0** | `select count(*) from features f where project_id=49 and not exists (select 1 from feature_complaints fc where fc.feature_id=f.id)` |
| features by status | 330 draft, 2 rejected | `group by status` |
| `board_cards` rows for those 332 (a row means somebody MOVED a card; the board derives one per feature) | 20 | `select count(*) from board_cards where project_id=49` |
| tables whose name contains `milestone` | 0 | `grep -c milestone internal/db/migrations/0001_init.sql` (comments only) |

Two facts follow. **Every single feature on this project carries a complaint**, so the enforcement
works and nothing has slipped through; and **no feature can exist without one**, so a project that
wants to collect ideas before anyone has been hurt has only one legal move — invent the pain. That is
the mechanism this spec changes. The second table's last row is the other half: a project's **plan**
has no representation at all, which is why 330 drafts read as one pile and the roadmap lives in a
Markdown file that the page cannot contradict.

## 2. The value rule, stated before the scores were written

Every idea in the inventory carries a value out of ten, an effort tag, and a cost in weeks; the filer
uploads the value as the feature's `impact` and the weeks as `effort_score`, so the page's
impact-over-effort order *is* the document's order and the two cannot disagree.

- **6 or more — filed as a draft.** This is the bar I would use to put something in a project's
  backlog: it either removes a block a reader can name, or it makes an existing number trustworthy.
- **3 to 5 — filed as `rejected`, with the reason in the body.** Not omitted. An idea that lost is
  recorded once so that the next session does not re-propose it, which is the same reasoning
  `docs/REMAINING.md` uses for a defect it chose not to fix.
- **below 3 — no row.** A row costs a reader's attention and the corpus is already 349 ideas; the
  ones that failed this hard are named in §9 instead.

29 of the 38 cleared the bar; 9 did not and say why (§9). One of the nine is a case of this file
correcting itself: it was written at V7 and demoted to V3 after the board was measured.

## 3. M1 — the intake mode (V10, V9, V8, V8)

### 3.1 The setting

`projects.intake_mode TEXT NOT NULL DEFAULT 'complaint_first' CHECK (intake_mode IN
('complaint_first','idea_first','both'))`. The default is the current behaviour, so the migration
changes no project's semantics on deploy, and the column is read at the ONE place that enforces the
rule — `store.CreateFeature` — rather than by every caller.

- `complaint_first` — today's rule: a feature needs at least one linked complaint, and nothing else
  is accepted. This stays the default because it is the product's premise and the instance's data.
- `idea_first` — a feature may be created with no complaint. The ranking is unaffected (see §3.3).
- `both` — either is accepted, per feature, so a project can take pain-driven work and speculative
  work side by side without a second project.

### 3.2 Surfaces

1. The refusal must name the mode and the route that changes it. Today's message states the rule as
   though it were the instance's law, which is correct for `complaint_first` and wrong the moment the
   mode exists.
2. The new-idea form renders the project's mode above the complaint picker, and hides the picker when
   the mode admits an idea with no evidence. The form already loads the project; this is a template
   change and one field in the page payload.
3. The feature page and the features list carry one badge — `complaint-backed` or `idea-first` — read
   from the linked-complaint count the feature page already loads. A reader judging an idea is
   entitled to know whether somebody suffered it or somebody liked it.
4. Promotion is gated, not free: a feature reaches `ready` only if it has a validated complaint **or**
   the project's mode is `idea_first`. The check lives in the status transition, beside the other
   §6.2 rules, because a second checker is a second answer.
5. Changing the mode is a **proposal** through the curation queue (V6, M), not a maintainer dial: the
   mode decides whose work counts as legitimate input, which is a charter question.

### 3.3 The false claim this spec has to settle

The shipped copy (`docs/IDEAS.md`, part 1) says an unlinked feature "ranks on nothing". **No code
enforces that.** `/ranking` reads `features.elo_r` directly and never joins a complaint; a feature
created with no complaint would rank exactly like any other. So either the sentence goes, or the
ranking gains a real complaint term — and the honest answer is that the sentence goes: the arena
ranks ideas by judged pairs, and an idea's provenance is not a rating. V8 exists to force that
decision into a test rather than into a release note.

### 3.4 Evidence that is not a complaint (V6, M)

A project documenting itself often has a spec section or a field report rather than pain. §6.2's
traceability rule is about *evidence*, not about the complaints table: accept any one of a validated
complaint, a project document revision, or a validated field report, with the same "at least one"
shape, and record which kind satisfied it in the audit row so the ledger can say.

## 4. M2 — milestones (V9, V8, V8, V8)

### 4.1 Data

```sql
CREATE TABLE milestones (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    body        TEXT NOT NULL DEFAULT '',
    target_date REAL,                      -- nullable: a date nobody agreed to is worse than none
    status      TEXT NOT NULL DEFAULT 'planned'
                CHECK (status IN ('planned','active','done','cancelled')),
    created_at  REAL NOT NULL,
    UNIQUE (project_id, title)
);
ALTER TABLE features ADD COLUMN milestone_id INTEGER REFERENCES milestones(id) ON DELETE SET NULL;
CREATE INDEX features_milestone ON features(milestone_id);
```

**Nullable in both directions, deliberately.** `features.milestone_id` is null for everything not yet
claimed, and that set *is* the backlog — a default milestone would make the backlog unrepresentable.
`target_date` is nullable because a milestone dated by wish is a deadline nobody agreed to.

### 4.2 Surfaces

1. `GET /api/v1/projects/{slug}/milestones` (list, ordered by `target_date NULLS LAST, id`),
   `POST` (create), `GET .../{id}` (one, with its counts), `PATCH .../{id}` (title, body, target), and
   `PUT .../{id}/status`.
2. `PUT /api/v1/projects/{slug}/features/{id}/milestone` with `{"milestone_id": n|null}` — assigning
   and unassigning are the same call, because unassigning must be as easy as a wrong click.
3. `/projects/{slug}/milestones/{id}` — the milestone's features grouped by status, its progress, and
   the two dates that matter (target, and last shipment).
4. The board takes a `milestone` query parameter, composing with the existing phase filter. **One
   board, filtered** — not a board per milestone, which would fork the WIP limits and the placement
   rules (V3, rejected).
5. The project overview names the earliest open milestone and its remaining count (V6, S).

### 4.3 Progress that is not an import artefact (V7)

Closed-over-total computed from `features.status` alone measures whoever ran the seeding script: the
instance already knows this ($925$ of 1,874 features have no creation event — `internal/store/stats.go`'s
provenance split). Milestone progress is derived from status transitions that carry an `audit_log`
row, and the provenance split is printed beside it. A milestone whose "shipped" count is entirely
seeded says so.

### 4.4 Closing a milestone (V7)

`done` with open features is a lie; a blocked close is a trap. Closing moves the unfinished features
to a named successor, one audit act per feature, so "why did this move" is answerable from the log.
`cancelled` closes it without a successor and leaves the features where they are.

## 5. M3 — the roadmap (V8, V7, V7)

`/projects/{slug}/roadmap` and `GET /api/v1/projects/{slug}/roadmap`: each milestone as a column,
ordered by target date, with its features grouped by status and its progress from §4.3. Slippage
(V7) is the number a roadmap exists for — for a closed milestone the hours between target and last
shipment, for an open one the age of its oldest open feature — and it is computable from `audit_log`
plus `target_date` with no new storage. The page and the route read ONE shape: every page in this
repo that computes its own numbers is a page whose numbers can disagree with the route's.

## 6. M4 — scheduling (V8, V7, V6, V6)

`feature_deps(blocker_id, blocked_id)` with both FKs and a unique pair; a feature with an open blocker
refuses `in_progress` **at the same transition that enforces §6.2**, so the constraint lives where work
moves. Insert refuses a cycle and names the path (`a blocks b blocks a`) rather than answering
`invalid`. A blocked badge names its blockers in the list and on the feature page.

**And a measurement that refuted one of this spec's own ideas, which is why it is here.** The
first draft of the inventory carried "place a card for every new feature" at V7, on the strength of
`select count(*) from board_cards where project_id=49` returning 20 against 332 features. The board
page renders 362 cards in `inbox`, not 20 — `GetBoard` returns a DERIVED card for every feature, and
`board_cards` holds only the ones somebody has moved (`handleMoveCard`'s own comment says a derived
card "has no row and therefore no id"). The idea is filed at **V3 and rejected**: a row per feature
would duplicate what the renderer derives and would make "placed" mean nothing. The residual finding
is smaller and still true — a derived card and a placed row are indistinguishable to a reader, and
neither is identified as such in the API's response.

## 7. Invariants, each with the test that holds it

1. `complaint_first` + no complaint → `ErrInvalid`, and the message names the mode — store test
   asserting the error text, not just the error.
2. `idea_first` + no complaint → created; `both` → created; `complaint_first` → refused. One table
   test over the three modes, because a mode that behaves like its neighbour is a mode nobody set.
3. A mode change is an audit act and appears under the `intake` kind of the modlog — the same
   resolution path `moderation` uses (`store.AuditKindActions`).
4. A feature with no complaint still ranks: the ranking route returns it with its own `elo_r`, and a
   test asserts the rating is untouched by provenance (this is the V8 decision, made executable).
5. `ready` is refused for an evidence-free feature in `complaint_first` and allowed in `idea_first`.
6. `milestone_id` set to another project's milestone → 404, the same answer as a missing row.
7. Unassigning (`milestone_id: null`) returns the feature to the backlog and the count moves by one.
8. Milestone progress counts only features whose shipment carries an audit row; a seeded `shipped`
   feature is excluded and the response says so.
9. Closing with a successor moves every open feature and writes one audit act each; closing without
   one (`cancelled`) moves none.
10. A dependency cycle is refused with the cycle in the message; `in_progress` is refused while a
    blocker is open, and permitted once it ships.

## 8. Non-goals

- **No second board.** Milestones filter the board; they do not own columns, WIP limits or placement.
- **No roadmap parser.** `docs/PLAN-r4.md` describes the plan; the instance owns it. A doc parser makes
  the Markdown the schema, and the two disagree the first time someone edits prose.
- **No iCal or milestone badge.** Both imply the dates are commitments, which is a claim the project
  cannot yet keep (§5's slippage is what would earn it).
- **No automatic milestone assignment.** A toggle that puts every new feature in the active milestone
  makes the backlog unrepresentable — the set no milestone claims is what a backlog is.
- **No new consent vocabulary.** A milestone-scoped consensus call (V6) reuses §6.2's calls; the work
  is the many-to-one mapping, not the rules.

## 9. The nine ideas that lost, and why

| idea | V | why it loses |
|---|---|---|
| Intake-mode changes as their own audit kind | 5 | Redundant with the proposal kind (§3.2 item 5); alone it is a filter over rows nobody searches |
| Hand-ordered milestones with drag | 5 | `target_date` already orders them; hand order is a second ordering to keep in sync |
| Milestone badge SVG | 5 | The instance badge already answers "what is this project"; another SVG is another endpoint to keep alive |
| Roadmap generated from `PLAN-r4.md` | 4 | Makes the document the schema (§8) |
| iCal export of target dates | 4 | Advertises a schedule the project cannot yet keep |
| Milestone-scoped board columns | 3 | Two boards over one backlog, two answers to "which phase" |
| Dependency graph view | 5 | The badge and the refusal already prevent the harm |
| Auto-assign new features to the current milestone | 5 | Makes the backlog unrepresentable |
| Place a card for every new feature | 3 | `GetBoard` already derives a card per feature; a row per feature duplicates the renderer and makes "placed" meaningless (§6) |

## 10. The filing record

`scripts/file_ideas.py --only intake_modes,milestones,roadmap_view,scheduling` files the four theme
complaints (validated) and 38 features — 29 as `draft`, 9 as `rejected` with the reason above in their
bodies — plus a `board_cards` row in `inbox` for each draft. It is idempotent by title, so re-running after an edit adds only what is new. Values
upload as `impact`, weeks as `effort_score`.

The theme complaints are the pain statements, not the features: *"I have to invent a complaint to
record an idea"*, *"I cannot group the ideas I am planning into one milestone"*, *"I cannot tell what
is planned from what is merely written down"*, *"Grouping is not scheduling: I cannot say what blocks
what"*.

## 11. As built (2026-11-12)

Three decisions here were made while writing the code, and each is a place the
document and the implementation legitimately diverge. The divergence is recorded
rather than smoothed over, because a spec that quietly rewrites itself after the
fact cannot be used to check the next change.

**11.1 The ready gate has no mode exemption.** §3.1 frees the FILING in an
`idea_first` project, and an early draft of the store read that as "and therefore
frees `ready`". It does not: `ready` is a commitment to build, and the code asks for
evidence there in every mode. The escape hatch is §3.4's alternatives — a project
document or a field report counts — not a mode that switches the rule off. Without
this, `ready` would mean "somebody clicked" in exactly the projects that have the
least other support.

**11.2 Ordering inside a milestone is a column, not a join table** (`§6`).
`features.milestone_position` rather than `milestone_features(position)` because a
feature belongs to at most one milestone: a join table for a one-to-many is a second
primary key to keep in step with the first. The order is written by
`PUT .../milestones/{id}/order` with the whole list, so the server never infers what
a "move" meant.

**11.3 The intake mode ships as a maintainer write, not a proposal** (§3.5). The
proposal machinery proposes a feature weight or a theme weight — both have an arena
that supplies the comparison. "This project should accept ideas" has no comparator,
and building one to justify a single CHECKed column would be ceremony. The write is
maintainer-gated and audited (`action = intake_mode`), which is the same standard the
visibility column is held to. This is the one item in the filed inventory that lost
its shape on contact with the code, and it is listed as such in
`docs/REMAINING.md` §43.2.
