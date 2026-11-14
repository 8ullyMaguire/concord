# Concord after the port — roadmap, mapped against the "Rethinking GitHub" note

The port is done (`docs/plans/port-to-profile.md`, rows 1–13). This file is the first post-port roadmap, and it is a **mapping**, not
a wish list: every idea from the "Rethinking GitHub for the Age of AI" note is put against what this tree and the live instance
actually do today, with the file or route that proves it. An idea that already ships is not refiled; an idea that ships *partly* says
which half is missing.

Status vocabulary, and nothing else: **SHIPPED** (a reader can use it today), **PARTIAL** (the engine exists, the surface does not, or
the reverse — the missing half is named), **NEW** (nothing in the tree), **REFUSED** (concord should not do this, with the reason).

Measured 2026-11-14 against `crates/` on `port-rust` and the live instance (`73 projects`, `638 → 88 users` after reconciliation).

---

## The mapping

| # | Idea from the note | Status | What exists, measured | What is missing |
|---|---|---|---|---|
| 1 | **Project lifecycle stages** (seed / sprout / mature / dormant / archived) | **NEW** | — (`grep -ril lifecycle crates` → 0 files). Signals to derive it already ship: `project_metrics.last_commit_at`, `health_score`, `features.status`, `members` | The stage itself, and every surface that would read it |
| 2 | **Intent-first creation** ("12 projects do this — contribute instead") | **PARTIAL** | `crates/finder` (question-driven matching, 12 tests) and `crates/scout` (22 tests) answer "what should I work on" | Nothing at the *create* path: a new project is still a blank form, and the duplicate check is a title match, not intent |
| 3 | **Project DNA / dedupe / merge suggestion / canonical election** | **PARTIAL** | Alternatives arenas rank competitors inside one project (`crates/db/src/alternatives.rs`); `features` dedupe is title-based | No cross-project fingerprint, no clustering, no merge suggestion, no instance-level canonical |
| 4 | **Vitality score** (bus factor, dep health, AI ratio, community growth) | **PARTIAL** | `concord_ranking::health` — last commit age, median review hours, contributors, releases/90d, with unknown components excluded and renormalised | Bus factor, dependency health, community growth, AI ratio; and the per-component display |
| 5 | **Graveyard & resurrection** (declared abandonment, adoption pool, auto-archive with grace, succession link) | **NEW** | `projects.visibility` and `intake_mode` are the nearest neighbours | All of it. Auto-archive is a *job*, not a page |
| 6 | **AI transparency badges** | **NEW** | — | A declaration per project and its display. The only idea here that is close to free |
| 7 | **Problem spaces** (a layer above projects, community-ranked within) | **PARTIAL** | `crates/db/src/taxonomy.rs` — tags, proposals ratified by quorum, and `completeness` | Tags are a flat vocabulary, not a space with a canonical / alternative / emerging ordering across projects |
| 8 | **Composable fragments** (sub-project code units) | **REFUSED for now** | — | Concord hosts *decisions and ideas*, not code. A fragment library would need code hosting, which is the one thing this product deliberately does not do; revisit only if a forge integration makes it a lens over someone else's code |
| 9 | **Maintainer load / succession / paid tiers** | **PARTIAL** | Roles (`concord_governance::Role`), `trust_grants`, invites, the curation queue, `contribution_feed` | Load per maintainer; a succession field; and money, which is out of scope until the licence question is answered |
| 10 | **Federated reputation / skill graph / web of trust** | **PARTIAL** | `trust_grants` + `trust_config.max_level` (a ceiling, refusal not clamp); votes weighted by Glicko-2 reputation with an age gate | Portability across instances (ActivityPub-shaped), a skill graph over *what* a person does, and endorsements as a web |

**Two of the ten are already core to the product and the note is describing them without knowing it:** the pairwise arena ranking
(idea 3, partially) and the finder (idea 2, partially). Building them "new" would be the third implementation of a thing this repo
has already built twice.

---

## The order, and why

Sequenced by what unblocks the most and by what the existing engine can carry without new data collection.

**Step 1 — lifecycle stage as a pure function.** Idea 1 depends on nothing. Every other discovery idea (dormant warnings, the
adoption pool, "mature projects first", auto-archive) needs a stage to exist first, and the signals are already in `project_metrics`.
Gate: `cargo test -p concord-ranking`, with one test per rule, including the absence rules.

**Step 2 — the stage on the wire, on the page, and in the API.** A stage nobody can see changes nothing. Adds the field to
`GET /api/v1/projects` and `/api/v1/projects/{slug}`, renders it on `/projects` and the project page, and states it in the badge.
Gate: `probe_pages.py` + `probe_project_segments.py` + the six browser suites, because a new field on a public shape is exactly what
the parity corpus exists to catch.

**Step 3 — intent-first creation.** At the create path, ask the intent and answer with the finder: "N projects match; the
closest is X". This is idea 2's missing half and it reuses `concord-finder` rather than adding a recommender. Gate: a differential
probe against the finder's own answers — the same question must return the same projects.

**Step 4 — AI transparency declaration.** Idea 1's cheap cousin: one enum per project, declared by the owner, displayed as a badge,
never inferred. Gate: probe + page check, and a refusal test (an undeclared project renders *nothing*, not "unknown 0").

**Step 5 — dormant → adoption pool.** The first thing that needs a job: a sweep that marks projects dormant after 90 days of no
measured activity and lists them for adoption, with notification. Gate: the sweep against a fixture project aged forward, asserting
it never marks a project whose activity is *unmeasured*.

**Deliberately not scheduled:** fragments (refused above), problem spaces as a first-class table (taxonomy has to prove it cannot
carry the ordering first), paid maintenance tiers (licence question), and resurrection (needs steps 1 and 5 to exist).

---

## Step 1 — the design, written before the code

`concord_ranking::lifecycle` — a pure module beside `health`, for the same reason that one is pure: the arithmetic is testable
without a database, so the rule can be proved by a unit test instead of by a page.

```rust
pub enum Stage { Seed, Sprout, Mature, Dormant, Unknown }

pub struct Signals {
    pub last_commit_age_days: Option<f64>, // None = no repository, or a repository never committed to
    pub health_score: Option<f64>,         // None = nothing measured
    pub features_shipped: i64,
    pub age_days: f64,
}

pub struct Verdict { pub stage: Stage, pub because: &'static str }
```

Rules, in order, each with the measurement that justifies it:

1. **Dormant wins over everything measurable.** `last_commit_age_days >= 90` → `Dormant`. Ninety days is the note's own number and
   it matches `healthsync`'s cadence: the sweep measures hourly, so a project that has not moved in a season is not moving.
2. **An unmeasured project is never dormant.** `last_commit_age_days == None` → not dormant, because "no data" and "no activity" are
   different facts, and this repo has already paid for confusing them once (`project_metrics.health_score = 0` for every project
   made `sort=health` a total tie).
3. **Nothing shipped and nothing committed → `Seed`.** An idea with no code is the note's first stage by definition.
4. **Something shipped, but young or unproven → `Sprout`.** `features_shipped > 0` and (`age_days < 180` or health unknown or
   `health_score < 0.6`).
5. **Shipped, old enough, and measured healthy → `Mature`.** `features_shipped > 0`, `age_days >= 180`, `health_score >= 0.6`.
6. **Nothing to go on → `Unknown`, with the missing signal named.** Not `Seed`: a project with a repository that has never been
   synced is not an idea, it is an unsynced project, and the difference is what the next sweep has to fix.

`Archived` and `Forked & alive` are **not** in this enum, and that is a decision rather than an omission: both are acts by a person
(declare dead, take over), and a stage that the code infers about somebody's project is a stage that will be wrong about it. They
arrive with step 5, as columns written by an act, and the projection can then read them.

### One field was added to the design while implementing it

The draft above had four signals, and under them `Unknown` was **unreachable**: "no repository" and "a repository this instance has
never read" are the same `None` in `last_commit_age_days`, so nothing-shipped always fell to `Seed`. The first test written for the
unknown arm produced `Seed` and the arm looked dead. `Signals` gained `has_repository`, which turns the pair into the real
distinction: *no repository and nothing shipped* is an idea, *a repository nobody has read* is a gap in this instance's measuring.
That is the whole reason the enum has an `Unknown` at all, and it was invisible until a test tried to reach it.

## Progress

### Step 1 — DONE 2026-11-14

`crates/ranking/src/lifecycle.rs` — `Stage`, `Signals`, `Verdict`, `stage()`, and twelve tests, one per rule plus the three absence
rules. Gate:

```
$ cargo test -p concord-ranking lifecycle
test result: ok. 12 passed; 0 failed; 0 ignored; 0 measured; 28 filtered out
$ cargo test -p concord-ranking
test result: ok. 40 passed; 0 failed        (+ 3 golden, + 2 health golden)
```

The gate was proved able to fail rather than assumed: `age >= DORMANT_AFTER_DAYS` mutated to `age >= 1e9` (dormancy unreachable)
turned the suite red on `ninety_days_without_a_commit_is_dormant` and the file was restored byte-identical.

### Step 2a — DONE 2026-11-14 (the wire)

`GET /api/v1/projects`, `/{slug}` and every route that embeds a project now carry `stage` and `stage_because`. Read side only: the
three columns it needs were added to `PROJECT_COLUMNS` and the stage is computed in `project_from_row`, so there is no write path and no
column to go stale.

```
$ curl -s https://concord.polarisocial.xyz/api/v1/projects | python3 -c '…'
73 projects  Counter({'seed': 61, 'sprout': 12})
```

**The distribution is the finding, not the feature.** Twelve is exactly the number of projects with a shipped feature
(`select count(distinct project_id) from features where status='shipped'` → 12), so the twelve are `sprout` and everything else is
`seed`. **`mature` and `dormant` are unreachable on this instance because no project in PostgreSQL has a `repo_url`** — the repository
identities still live in the Go's `seed/forge_repos.json`, which is the same gap that leaves every `health_score` unmeasured. A stage
that cannot be reached is not a bug in the stage; it is a measurement this instance does not take yet, and that is now step 5's
prerequisite rather than its surprise.

Two PostgreSQL details, each a round of debugging: `extract(epoch …)` is NUMERIC in this baseline, so it needs `::double precision`
before sqlx will decode it into `f64` ("Rust type f64 is not compatible with SQL type NUMERIC"), and repo identity is
`repo_url is not null and repo_url <> ''` because an empty string is not a repository.

**The corpus had to be re-recorded, and the replay tool caught the change before a reader could.** Replaying the *old* recording against
the new binary reported exactly the three shapes that carry a project:

```
/api/v1/projects: 2 difference(s)   .stage: unexpected, .stage_because: unexpected
/api/v1/projects/concord: 2 …
/api/v1/search?q=arena: 6 …         (two results, each with both fields)
3 shape(s) disagreed of 21 checked
```

Then a re-record. **A recording made with `--previous` pointing at the existing corpus marks every changed shape `unstable`, and
`unstable` shapes are SKIPPED by the replay** — six of them here, including `/api/v1/version`, which is the fastest way to end up with a
corpus that checks three quarters of itself and reports green. Recording against a nonexistent previous (`--previous /tmp/no-such.json`)
gives a clean baseline: 21 shapes, 0 unstable, `0 shape(s) disagreed of 21 checked, 0 skipped`.

Gates, all against the deployed file: `probe_pages.py` 132/132, `probe_project_segments.py` 86/86, six browser suites 76/76.

### Step 2b — DONE 2026-11-14 (the page)

`/projects` and `/` print the stage beside the project name, and the project page states it in its title. The tooltip is the reason
(`stage_because`), so the badge is not a label a reader has to take on faith, and `dormant` takes the warning colour while `unknown`
renders **nothing**.

Two things this step taught, both recorded because the next person will meet them:

**`/projects` is the one catalogue page still rendered on the server.** The first implementation added a `stageBadge()` to
`crates/web/assets/js/projects.js` — a file with a `card()` function that renders exactly the markup the suite looked for — and seven
tests failed against a correct page, because that script does not run on `/projects`. The badge belongs in `stage_badge()` in
`crates/web/src/lib.rs`, shared by both server-rendered lists so they cannot say different words; the asset edit was reverted rather
than left in place. A selector written from the source instead of from the DOM believes in dead code.

**The tooltip was the stage repeated.** `format!("{}: {}", stage, because)` produced `dormant: measured: no commit for ninety days or
more` — the badge already says the word, and the prefix buried the sentence a reader wanted. The title is the reason alone.

Gate — the new suite is `tests/e2e/lifecycle_e2e.py` (8 tests, port 8521), which seeds five projects differing only in the signals that
decide the stage, and asserts the rendered word, the warning class on the dormant one and on nothing else, the tooltip, and — the
assertion worth the fixture — that the unmeasured project renders **no stage at all**:

```
lifecycle_e2e        8 passed in 2.94s
feature_e2e         15 passed · milestones_e2e 5 · rank_links_e2e 4 · audit_e2e 11
consensus_e2e       15 passed · project_panels_e2e 26
probe_pages.py (live) 132/132 · probe_project_segments.py (live) 86/86
concord-parity       0 shape(s) disagreed of 21 checked, 0 skipped
```

Proved able to fail: `if project.stage == "unknown" { return; }` mutated to `"__never__"` — which renders `unknown` as a badge — turns
`test_an_unmeasured_project_renders_no_stage_at_all` red (`1 failed, 7 deselected`), and the source was restored byte-identical
(`diff -q` → clean).

### Step 3 — NEXT

Intent-first creation: at `/projects/new`, ask what the person is trying to build and answer with `concord-finder` before the blank
form. The gate is a differential probe — the same question asked of the create page and of `/api/v1/finder` must return the same
projects, in the same order, because two callers deriving one answer is how they disagree.
