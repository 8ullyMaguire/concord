# Concord after the port — roadmap, mapped against the "Rethinking GitHub" note

The port is done (`docs/plans/port-to-profile.md`, rows 1–13). This file is the first post-port roadmap, and it is a **mapping**, not
a wish list: every idea from the "Rethinking GitHub for the Age of AI" note is put against what this tree and the live instance
actually do today, with the file or route that proves it. An idea that already ships is not refiled; an idea that ships *partly* says
which half is missing.

> **Status 2026-11-14 (second pass):** the review's engineering items 1, 2, 3, 4 (boot/timers), 6 (feature edit) and 7 (gate + CI) are
> **done** — see the work log at the top of `docs/reviews/2026-11-14-from-scratch-review.md`. Still open there: the server-rendered project
> page, `archived` as a declaration, the frontend split, and `port-context.md`'s stale header. Roadmap: steps 1, 2a, 2b, 3, 3b, 3c, 4 **done**; step 5 built (sweep + timer) with its **pool blocked on 68 unstated
> repositories**. Rows open: 1. Blocked: 1. Nothing in this file is "complete" while that number is above zero.
> A critical review of the product as it now stands, with what I would do differently, is `docs/reviews/2026-11-14-from-scratch-review.md`.

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

### Step 3 — the engine already existed, and its index was empty (2026-11-14)

I planned an intent check, wrote it (`crates/db/src/intent.rs`: term decomposition, any-term scoring, the terms reported back), and
the compiler refused the route name — which is how I found `GET /api/v1/similar/{kind}`, shipped since the port, with an embedder,
per-model score bands and a `has_duplicate` flag. **The module was deleted, not shipped beside it.** Two implementations of one answer
is exactly what this repo has already paid for twice (the finder, the arena ranking).

Then the measurement, which is the real state of step 3:

```
$ curl -s -G https://concord.polarisocial.xyz/api/v1/similar/project \
       --data-urlencode "text=booru image board" --data-urlencode limit=3
available True  model nomic:nomic-embed-text:latest  dup False
  (no rows)

$ ssh thinkcentre psql "$DATABASE_URL" -tAc "select kind, count(*) from embeddings group by 1"
feature|5342
```

**The route answers, and it cannot answer: `embeddings` holds features only — zero rows of `kind='project'`.** A project lookup therefore
returns an empty `similar` array with `available: true`, which reads as "nothing like this exists" for every intent, including intents
whose project is on the page. Coverage, from the binary's own report:

```
$ concord-rs embedbackfill --kind project --status
project        0.0%  INCOMPLETE -- duplicate detection will miss rows (73 rows)
```

The fix is a backfill, not code — and the first attempt at it was wrong in the way the command's own comment predicts: run bare, it
reported `embedder hashed-v1 (2048 dims)` while the service runs `nomic` (768), so it would have written vectors no reader uses and left
two models in one table. Run with the service's own `CONCORD_EMBED_URL`/`MODEL` (read from `systemctl --user show concord-rs -p
Environment`, not from memory), it reports `embedder nomic:nomic-embed-text:latest (768 dims)` and fills the index.

**An interrupted backfill is invisible in the row count.** The first attempt ran under a 600-second `timeout`, which killed it after 40 of
73 projects; nothing printed (the command reports per kind, at the end), and the table then held 80 rows — a number that looks like
progress and reads like completion. Only the coverage mode says otherwise:

```
$ concord-rs embedbackfill --kind project --status
project       54.8%  INCOMPLETE -- duplicate detection will miss rows (73 rows)
$ psql -tAc "select field, count(*) from embeddings where kind='project' group by 1"
full|40
title|40
```

At ~15 s per project (two fields, ollama `nomic`, local) the full run is about eighteen minutes, so it is a background job with the
status command as its proof — never a foreground command with a timeout.

What the partial index already established, since a half-filled index is enough to show the route works:
`/similar/project?text=a federated social platform in rust` → `lemmy` (0.682), `l2-adrenaline-scripts`, `penny-dreadful-tools`, which
are plausibly the semantic neighbours; `text=booru image board` still returns nothing, because the project that would match is one of the
33 not yet indexed.

### Step 3b — DONE 2026-11-14 (the create page, and a guest could create a project)

`/projects/new` now exists — the first creation surface in this tree, because until now `POST /api/v1/projects` was the only way to start
a project and it had no page at all. It asks *what are you trying to build?* first, answers it from `GET /api/v1/similar/project` via
`new-project.js`, and only then offers the name and slug fields. The intent answer is not re-derived: the page renders the mount and the
script renders the route's answer, including `has_duplicate`.

**An empty answer is a sentence, not a suggestion.** With no matches the page says nothing in the catalogue is described that way, names
the wording it searched, and links `/search` — it never manufactures a "closest project", and there is a browser test whose whole job is
that negative.

**The finding, and it is why the page was worth building.** The first end-to-end run of the form created a project as a visitor who had
signed into nothing. The trace is in `new_project_e2e.py`'s own diagnostic: `session.js` asks `POST /api/v1/guest` for every visitor, the
browser then POSTs `/api/v1/projects` with that guest token, and the server answered **201 Created**:

```
req POST /api/v1/guest      -> 201
req POST /api/v1/projects   -> 201
GET  /projects/new-project  -> 200
```

`create_project` checked `bearer_user` — which returns a guest identity happily — and never `identity::is_guest`, while the handler's own
doc comment claims "the Go handler refuses a guest identity here and nowhere else in the read path". **The comment was the only thing
refusing it**, which is the squatting vector it describes, reachable by clicking submit on a page nobody had signed into. Fixed with the
predicate the guest cap and the promotion path already ask; proved on the live host:

```
$ POST /api/v1/guest -> token (47 chars)
$ POST /api/v1/projects (Bearer <guest>) -> 403
  {"code":"forbidden","error":"a project needs an account, not a guest session"}
```

Gates: `new_project_e2e.py` 6 passed (the guest arm asserts the STATUS CODE, because a 403 that still wrote a row would read as a
refusal); mutation `if false && is_guest(…)` turns `test_creating_without_an_account_says_so` red with the source restored
byte-identical; `probe_pages.py` grew four contract checks for the new route.

### Step 3c — DONE 2026-11-14 (the index, and why a filled index still answered badly)

The backfill finished: `project 100.0% OK (73 rows)`, 146 vectors (title and full text per project). And the route then answered with the
score right and the answer unusable:

```json
{"entity_id": 74, "score": 0.659, "title": "", "status": ""}
```

`embeddings::hydrate` — the function that fills title, status and URL so a match is actionable — had arms for complaint, feature and
request, and a catch-all that returned the row as scored. **`project` was in the catch-all**, so every project match came back nameless
and linkless, and the create page could only have rendered a numeric id. The project arm now reads `name` as the title, `visibility` as
the status and builds `/projects/{slug}` — no `{id}` segment, because that is how a project is addressed.

The intent router now answers:

```
GET /api/v1/similar/project?text=booru image board&limit=2
0.659  Suki     /projects/suki
0.626  Tessera  /projects/tessera
```

Gates at the end of step 3: `probe_pages.py` 136/136 (four new contract checks for `/projects/new`), `probe_project_segments.py` 86/86,
`concord-parity` 0 disagreed of 21 checked 0 skipped, eight browser suites 90 tests (feature 15, milestones 5, rank_links 4, audit 11,
consensus 15, project_panels 26, lifecycle 8, new_project 6), deployed as `concord-rs 0.0.0+80f5155`.

### Step 4 — DONE 2026-11-14 (the AI declaration, and the state that is not a value)

`projects.ai_involvement` — `human` / `assisted` / `generated` / `audited`, or `NULL`. Declared through
`PUT /api/v1/projects/{slug}/ai-involvement` by an `owner` or `maintainer` (the store reads `feed_reads::role_for`, so the role ladder is
asked rather than re-derived), rendered as a badge beside the stage on `/` and `/projects` and in the project page's title, and **never
inferred** — not from commit metadata, not from the project's age, not from a model's guess.

**`NULL` is a state, not a gap to fill.** It renders no badge at all, and that is the test the feature is built around: a badge there
would be this instance asserting something about somebody's code that nobody asserted, which is worse than the silence the note complains
about. A mutation that renders `unknown` for it turns `test_an_undeclared_project_renders_nothing` red (`1 failed, 6 deselected`), source
restored byte-identical.

Withdrawal is a legitimate request — `{"value": null}` — because a label a maintainer regrets has to be removable without a database
session. A stranger gets **403**, not 404: the project is public here, and a 404 would be a different lie. A value outside the four is a
400 that names them, refused in the store rather than left to the `CHECK`, so the caller reads a sentence instead of a constraint name.

Schema, in this tree's shape: `0001_baseline.sql` is the only schema file and there is no migration runner, so the column was added to the
baseline **and** applied to the live database with `alter table … add column if not exists`. That is an operational step this repo has not
automated, and it is named here rather than assumed: a release that only edits the baseline works on a fresh database and fails on the
running one with `column "ai_involvement" does not exist`.

The wire changed additively, and the corpus said so before a reader could: replaying the old recording reported
`.ai_involvement: unexpected` on the three project-carrying shapes (`3 shape(s) disagreed of 21 checked`), then a clean re-record gives
`0 disagreed of 21 checked, 0 skipped`.

Gate: `ai_declaration_e2e.py` 7 passed (four rendering, three API: declare-and-withdraw, stranger refused, value outside the four
refused). One fixture trap cost a round and is worth the line: the harness's token must be hashed `base64 RawStd` — **no padding** — or
every authenticated request answers 401, which reads as a permissions bug and is an encoding one.

### Step 5 — the sweep, the POOL and the claim are built; the CANDIDATES are blocked on unstated repositories

**2026-11-15 — the pool page and the claim act are live** (`0d8d5dc`). `GET /adopt` lists what the lifecycle has already measured dormant,
sorted by silence, server-rendered; `POST /api/v1/projects/{slug}/claim` refuses on the measurement rather than on the person (401 anonymous,
400 not dormant / never measured / declared archived) and on success makes the claimant a maintainer with an audit row carrying the number.

The live answer is the honest one, and it is a measurement rather than a promise:

```
$ curl -s https://concord.polarisocial.xyz/adopt | grep -o 'Nothing is dormant right now[^<]*'
Nothing is dormant right now. 23 of 97 projects have a measured commit history, and the longest measured silence is 74.6 days.
```

So the page exists and the claim works, and the pool has **no candidates because nothing measured is past ninety days**. What still blocks
candidates is the same thing that always did — the repositories nobody has stated:

Measured, not assumed: `embedbackfill` was easy because a project's text is in the database. Dormancy needs commit age, and commit age needs
a repository. **Zero projects in PostgreSQL have a `repo_url`**, and the Go tree's seed file — the only place the mapping ever lived — says
why:

```
$ python3 -c "…json.load(open('seed/forge_repos.json'))"
rows 74
with repo_url 5
{'slug': 'ao3', 'skip': True, 'reason': 'not stated (Q5)'}
```

Five stated, sixty-nine skipped with `not stated (Q5)`. So `healthsync` can measure at most five projects, and `mature` / `dormant` stay
unreachable for the rest — which means the adoption pool (mark dormant after 90 days, list for takeover, notify) would launch with a pool
of at most five candidates and a false "everything else is fine".

**This is a decision, not work.** Stating a project's upstream is somebody saying where its code lives, and a guess would put a stranger's
repository on a project's page. What blocks it: **the owner stating the remaining 68 repository URLs** (or stating that some have none,
which is also an answer).

What was not blocked, and is now done: `seed/forge_repos.json` copied into this tree from the Go checkout and applied with the port's own
`forgeseed`. Two things that run taught:

- **The file refused to apply, and it was right.** It names `piefed-rs-broken-git`, a project that is not in the catalogue — a typo or a
  deleted project. `forgeseed` refuses a mapping that does not account for the catalogue rather than applying the rest quietly, which is
  the difference between a job that reports success and a job that is right. The stale row was removed from the file (73 rows for 73
  projects) and the dry run then read `applied 5 of 73 (0 already set, 0 cleared, 68 skipped)`.
- **The stage function immediately responded to new measurement**, which is the whole argument for computing it instead of storing it.
  Before: 61 `seed`, 12 `sprout`. After: 59 `seed`, 14 `sprout` — `stash` and `stash-box` moved because commits are now readable, and
  `commons` / `darkhold` / `tessera` moved because they ship and are old enough to be measured.

Their health was already present (inherited from the Go database copy): `commons 0.389`, `darkhold 0.505`, `stash 0.592`, `stash-box
0.400`, `tessera 0.491` — all below the 0.6 mature floor, so **no project claims `mature` yet, and that is the honest reading rather than
a bug**: five projects are measured and none is above the line.

The Rust `healthsync` does not fetch from forges — it takes `--from <measurements.json>` — so keeping those five current is a file to
produce, not a job to schedule. That is a design question for step 5, not an oversight: the Go binary scraped GitHub and Forgejo itself,
and the port moved the scraping out.

Until then the honest state is what the page shows: `seed` and `sprout` only, no `mature` claim, no `dormant` claim — because a project
with no stated repository cannot be shown to be dormant, only to be unmeasured.


With project vectors present, `/api/v1/similar/project` becomes the intent router the note asks for, and the remaining work is the
creation surface, which does not exist yet either: there is no `/projects/new` page at all — `POST /api/v1/projects` is the only way to
start one, so "intent first" currently has no form to sit in front of. That page asks the intent, calls the existing route, and shows
`N projects match; the closest is X` before the slug and name fields, with the empty answer stated as "nothing here is described this way"
rather than as a suggestion nobody can act on.


Intent-first creation: at `/projects/new`, ask what the person is trying to build and answer with `concord-finder` before the blank
form. The gate is a differential probe — the same question asked of the create page and of `/api/v1/finder` must return the same
projects, in the same order, because two callers deriving one answer is how they disagree.

### Open, found by the suites on 2026-11-15 — not the roadmap's work, but not nobody's either

* ~~**`board_e2e::test_a_private_project_serves_no_board` fails**~~ — **fixed 2026-11-15**, and the port was the wrong one: `resolve`
  answered "does this slug exist" and not "may this reader see it", so all six sub-pages (`board`, `roadmap`, `consensus`, `documents`,
  `criteria`, `audit`) rendered a private project's name, tabs and shell to an anonymous reader while the API refused the data. `resolve` now
  calls `projects::readable_for`, the check the overview and the API already used, in one place rather than six. `board_e2e` went from
  `1 failed, 7 passed` to `8 passed in 18.77s`; `consensus_e2e 15`, `audit_e2e 11`, `milestones_e2e 5` still pass.
* ~~**`documents_e2e` and `requests_e2e` cannot run at all**~~ — **`documents_e2e` fixed and passing 2026-11-15** (`5 passed in 22.35s`).
  Both suites called `sqlite3.connect` directly instead of `harness.connect`, so neither tested the Rust binary; `PgSeed` grew `cursor()`
  and both use the harness now. Making `documents_e2e` run found five defects that had made `/projects/{slug}/documents` a page nobody could
  use: no `#project-documents` mount (the script was never loaded, so no kinds rail, reading pane or search), `skeleton.js` missing (so
  `done()` never ran and the region stayed `aria-busy` forever), `markdown.js` missing (the reader threw), and a list that shipped every
  document BODY — **11.7 MB, still downloading at thirty seconds** — now `?fields=meta` at `37104b` in `0.20s`, with the single document
  fetched from `/documents/{id}`. The search endpoint also refused an unbalanced quote, which `websearch_to_tsquery` silently accepted:
  `400 "the query could not run: a quoted phrase is not closed"`.
* **`requests_e2e` still cannot run**: it seeds through the harness now, but `16 failed, 2 passed` on the first real run, and every failure
  needs its own look. `requests.js` adopted `api.js` this session, so this suite is the only proof that panel has.
