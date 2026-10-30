# Contribution feed — spec

**Status:** specified 2026-10-30, nothing built.
**Implements:** spec §4.5.6 (*"Where can I contribute?"*) and §8.6 (onboarding), which have
been prose in the spec since revision 4 and code never.
**Plan:** `docs/plans/contribution-feed.md` (four tickets, CF-1 … CF-4).
**Supersedes nothing.** It reuses the Finder engine (§4.6 candidate generation already
exists), `project_metrics`, and the Forgejo webhook rather than introducing a second
recommender or a second health scorer.

## 1. What is broken today, measured

Every number below was read from the live instance on 2026-10-30.

| claim | measurement |
|---|---|
| Health is unmeasured for the whole catalog | 74 projects, 71 `project_metrics` rows, `health_score` non-null on **0**; `last_commit_at` set on **0**; `contributors` = 0 on all 71 |
| Nothing fills it | `UpdateProjectMetrics` (`internal/store/store.go`) is reachable only from `PUT /projects/{slug}/metrics`, a push route. The webhook writes an `audit_log` row and computes nothing |
| Two shipped surfaces are already wrong | `sort=health` orders by `COALESCE(health_score,0)` — one tie for the entire catalog — and `min_health` is the same COALESCE, so any `min_health > 0` returns **zero rows** |
| There is no way to contribute *to* an indexed project | `projects` has **no repository URL column**, and 2 of 74 descriptions contain a URL. The catalog cannot name the place a contribution would go |
| The supply of work is empty | complaints: 738 `validated`, **0 in any other state**; features: 981 draft / 442 shipped / 216 in_progress / 40 ready; board cards: 523, **0 in `ready`**; 69 of 74 projects have exactly one member |
| Nothing labels a work item | `features` has `effort` (S/M/L/XL) and `effort_score`, and no time budget, no claim, no newcomer flag, no difficulty of *approach* |

A feed built on this is correct and empty. The spec below therefore starts at the data,
not at the page.

## 2. Definitions

**Repository identity.** `projects.repo_url` + `projects.repo_host`, both nullable. A
project with no `repo_url` is a *native* project (it lives in Concord only) and cannot be
contributed to upstream; it can still have work items. `repo_url` is the **only** place a
contribution link comes from — never inferred from a slug, because a wrong guess sends a
stranger to a repository that is not the project's.

**Work item.** A unit a person can pick up. Today that is one of: a `complaints` row in
state `open` (needs validating), a `features` row in `ready` (needs implementing), a
`solutions` row without an implementer (§6.3 claims), or a project with
`newcomer_requests > 0` (needs a first field report). A work item is **not** a project:
"contribute to X" without a named task is the failure this spec exists to fix.

**Time budget.** Minutes a contributor is offering: `15`, `60`, `480` (a day), `2400`
(a week). Stored on the work item as `time_budget_minutes`; the contributor states their
own ceiling and matching is `work_item.time_budget_minutes <= offered`.

**Provenance.** `features.origin` / `complaints.origin` ∈ `api | seed | import | ui`,
default `api`. Seeded rows are rows nobody opened in the product: on the live instance
1,306 of 1,860 features were created on 12 distinct days in batches of 100–601, and the
audit log holds 949 `create_feature` rows. **Every statistic and every feed that cannot
tell a seed from a contribution is a statistic about an import script.** This is the
column that makes §4.5.6.7 and any velocity surface honest (see the aggregate-stats note).

## 3. Data model

One migration per ticket; each is additive, so a replay of the `0008` rebuilds keeps the
columns (§ the preserve-columns marker in `internal/db/rebuild_preserve.go`).

```sql
-- CF-1
ALTER TABLE projects ADD COLUMN repo_url  TEXT;
ALTER TABLE projects ADD COLUMN repo_host TEXT;      -- 'forgejo' | 'github' | 'codeberg'
ALTER TABLE project_metrics ADD COLUMN newcomer_response_hours REAL;
ALTER TABLE project_metrics ADD COLUMN newcomer_sample         INTEGER NOT NULL DEFAULT 0;

CREATE TABLE forge_events (
    id           INTEGER PRIMARY KEY,
    repo_full    TEXT NOT NULL,          -- 'owner/name', as the forge spells it
    kind         TEXT NOT NULL           -- push | pr_opened | pr_closed | pr_review | pr_comment
                 CHECK (kind IN ('push','pr_opened','pr_closed','pr_review','pr_comment')),
    number       INTEGER,                -- PR number; NULL for push
    actor        TEXT NOT NULL DEFAULT '',
    at           REAL NOT NULL,
    received_at  REAL NOT NULL,
    UNIQUE (repo_full, kind, number, actor, at)   -- redelivery is idempotent
);
CREATE INDEX forge_events_repo ON forge_events (repo_full, at);

-- CF-2
ALTER TABLE features ADD COLUMN difficulty          TEXT;   -- S | M | L, NULL = unlabelled
ALTER TABLE features ADD COLUMN time_budget_minutes INTEGER;
ALTER TABLE features ADD COLUMN needs_mentor        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE features ADD COLUMN origin              TEXT NOT NULL DEFAULT 'api'
    CHECK (origin IN ('api','seed','import','ui'));
ALTER TABLE complaints ADD COLUMN origin            TEXT NOT NULL DEFAULT 'api'
    CHECK (origin IN ('api','seed','import','ui'));

CREATE TABLE work_item_claims (
    feature_id  INTEGER NOT NULL REFERENCES features(id) ON DELETE CASCADE,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    claimed_at  REAL NOT NULL,
    released_at REAL,
    PRIMARY KEY (feature_id, user_id)
);
CREATE UNIQUE INDEX work_item_claims_active
    ON work_item_claims (feature_id) WHERE released_at IS NULL;
```

**The active-claim index is the rule.** One live claim per work item, enforced by the
database rather than by a handler check: two people implementing the same `ready` feature
is the specific harm a contribution feed creates, and a check in Go loses the race between
two requests on a busy instance. Releasing a claim sets `released_at` and leaves the row,
so "this was picked up and dropped twice" is answerable — the same "a withdrawal is a
state, not a deletion" shape as the board.

`difficulty` is deliberately separate from `effort`: `effort` (S/M/L/XL) is how *big* the
change is, difficulty is how *hostile* the ground is for someone who has never seen the
tree (`S` = one file, a test exists, a documented command reproduces it). A feature can be
`effort=S` and `difficulty=L` — a one-line change in a file with no tests and no
reproduction is the worst first contribution, and the current schema cannot say so.

## 4. Repository identity, then ingest (CF-1)

A project's repository is stated once, by an operator or a maintainer, in
`projects.repo_url`. There is no crawler and no slug guessing in this ticket: the live
catalog is 74 rows and the correct mapping is known to its owner, so the backfill is
`seed/forge_repos.json` — `[{slug, repo_url}]` — applied by `cmd/forgeseed`, which refuses
the whole file unless **every** slug resolves and the file covers exactly the projects it
claims to, the same "refuse a partial parse" rule the generators already follow.

`cmd/healthsync` then, per project with a `repo_host`:

1. Fetch: last commit age, contributor count (distinct authors in 365d), releases in 90d,
   median hours to first review on PRs, stars/forks/open issues/license.
2. Call the existing `store.UpdateProjectMetrics` — **no new scorer**. The health weights
   (.35/.25/.20/.20) are already exposed and re-weightable per §15; a second formula would
   make the badge and search disagree.
3. Report a per-project line and exit non-zero if any project failed, so cron notices.

`-dry-run` prints the `discovery.Metrics` it would submit. A project whose fetch fails
keeps its previous row and **is not written as zero**: unknown health is not zero health
(scout's rule, and the reason a naive ingest would rank the whole catalog `avoid`).

Cadence: hourly, via a `systemd` timer beside the unit — not a goroutine in the server,
because the server is restarted on every deploy and a timer that skips a beat is visible.

## 5. Work items (CF-2)

Routes (the `{project_id}` path param is a **slug**, as everywhere):

```
GET  /api/v1/projects/{slug}/work-items            # the project's open work, labelled
PUT  /api/v1/features/{id}/work-item               # difficulty, time_budget_minutes, needs_mentor
POST /api/v1/features/{id}/claim                   # {minutes}; 409 if an active claim exists
DELETE /api/v1/features/{id}/claim                 # release (released_at, never a delete)
GET  /api/v1/contribute/next?minutes=60&language=go&tag=cli
GET  /api/v1/contribute/answers                     # the Finder question catalog, as for /finder
```

`PUT /features/{id}/work-item` requires `maintainer` on the owning project — labelling a
task as a good first issue is a promise about review capacity, and only someone who will
review it can make that promise. `POST /claim` requires `contributor` and refuses a claim
on a feature that is not `ready` or `in_progress` (`400 invalid`): claiming a draft
commits you to work nobody has agreed to do.

**Claim does not change status.** A claim is not a work assignment; the board is still the
only thing that moves a feature, and `set_feature_status` still audits. Two mechanisms
that both move a feature is how a board and a feed start disagreeing.

## 6. The feed (CF-3)

`GET /api/v1/contribute/next` answers ranked **work items**, each carrying the project it
belongs to, its time budget, difficulty, whether a mentor is offered, and **why it is in
the list** (§4.5.4's reason is mandatory; a recommendation without one is an assertion).

Matching reuses the Finder engine over *projects*, then emits each surviving project's
work items:

1. Build `[]finder.Candidate` from the existing `finderCandidatesWithIDs` (it already
   carries capabilities, primary language, governance, license, field-report rate, arena
   standing) and add two attrs: `minutes` (`"15" | "60" | "480" | "2400"`, the smallest
   labelled work item in the project) and `mentor` (`"yes" | "no"`).
2. `finder.NextQuestion` / `finder.Apply` / `finder.Score` unchanged — including
   *unknown is not no*: an unlabelled project is kept and demoted, never dropped. This is
   the whole reason not to write a bespoke matcher; the engine's honesty rules are
   mutant-tested and a second implementation would not be.
3. `internal/finder/finder.go`'s `classify` gains two cases (`minutes` → `FamilyMaturity`,
   `mentor` → `FamilyGovernance`) so the question catalog does not label a time budget as a
   capability. Its existing `default` arm files unknown keys under capability, so this is a
   label bug, not a crash — which is exactly why it needs a test rather than a try.
4. Rank work items within a project by: claim-free first, then `time_budget_minutes`
   ascending, then `impact_ratio` descending, then feature id. Ties broken by id so two
   requests over one corpus return the same order.

`GET /contribute` renders the interview + results page using the same `finder.js` session
machinery as `/finder` (sessions, back, lift are already there); the results panel shows
the work item, not the project. Empty result set is a first-class answer:
`{"items": [], "gaps": [...]}` with `finder.Gaps`, never a blank page.

## 7. Measured responsiveness (CF-4)

The one signal that makes this different from a listing. `handlePullRequestEvent` already
receives `opened`/`closed`; the forge also sends `pull_request_review` and
`pull_request_comment`, which the handler currently ignores. CF-4 records them into
`forge_events` and derives, per project:

- **`newcomer_sample`** — PRs whose author has no earlier PR in that repository.
- **`newcomer_response_hours`** — median hours from such a PR's `opened` to the first
  `pr_review` or `pr_comment` by anyone else.

Written to `project_metrics` by `cmd/healthsync` (same command, same dry-run), displayed
on the project page as *"first response to a newcomer: 6h median (n=41)"*, and **not**
folded into `health_score`: health answers "is this maintained", responsiveness answers
"will anyone see me", and blending them loses both.

A project with `newcomer_sample < 5` shows the number **with its n**, never as a verdict.
A sample of two is not a rate, and publishing it as one is how a directory gets a
reputation for lying.

## 8. Non-goals (deliberate)

- **No engagement ranking.** §4.5.4 stands: fit, not time-on-site. No streaks, no XP, no
  "trending contributors".
- **No volume leaderboard.** Ranking people by complaints or features filed rewards the
  behaviour complaint fatigue exists to prevent. Responsiveness may be ranked (§7); output
  may not.
- **No automatic PRs.** The feed never opens anything on a forge. A tool that manufactures
  contributions into strangers' repositories is the asymmetry Concord's own 2026-09-29
  brief documents (cost to create fell, cost to review did not).
- **No second health scorer, no second ranker.** One of each, both existing.
- **No feed for a private project.** A private project has no feed, not an empty one — an
  empty feed still says the project exists (§6.6's feed rule).

## 9. Invariants to test

1. `/contribute/next` never returns a work item from a project the caller cannot read.
2. `min_health=1` returns rows **iff** at least one project has health > 0 (the current
   COALESCE bug, pinned by a test that fails on today's code).
3. An unlabelled feature never disappears because `difficulty` is NULL.
4. Two claims on one feature: the second gets `409`, and only one row has
   `released_at IS NULL` afterwards.
5. `origin='seed'` rows are excluded from the "new this week" count and included in the
   catalog total — the two must be separate numbers.
6. A failed fetch leaves the previous `health_score` untouched (unknown ≠ zero).
7. `repo_url` is never derived from `slug`: a project whose `repo_url` is NULL has no
   "contribute upstream" link at all.

## 10. What this does not fix

The supply problem. Labelling 40 `ready` features and 0 open complaints gives a feed with
40 items, and 69 of 74 projects have one member — the feed will be thin until the catalog
carries real collaborators. That is a demand-side fact, not a bug in this spec, and it is
the reason CF-2's `needs_mentor` and the responsiveness number matter more than the ranking
does: the first contribution either gets answered or the contributor is gone.
