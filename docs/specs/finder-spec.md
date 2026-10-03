# Concord — Finder: Akinator-style software discovery

**Status:** approved for build. Extends `docs/concord-spec-r4.md` §4.5 (Scout) and
§4 (discovery). Scout stays "I know roughly what I want, give me a build-vs-adopt
analysis". **Finder** is "I don't know what exists, ask me questions".

**Decision (owner, 2026-10-03):** the capability matrix and field reports (§R5)
are built **first**, because the Finder spec states Finder "uses what Concord
already has... No new data model is required" and that premise was measured to be
false. Finder then matches its mockups against real data rather than against
empty tables.

**Decision (owner, 2026-10-03):** REST under `/api/v1/finder/*` plus one embedded
vanilla-JS page, matching every existing Concord page and the frontend spec's own
§1.2 decision (no build step, embedded assets). The spec's GraphQL contracts are
recorded in §9 as the shape the REST endpoints mirror.

---

## 1. What was measured before designing

Every claim below is a query against the running instance on thinkcentre
(`~/.local/share/concord/concord.db`), not an inference from the spec.

| Finder's claimed substrate | Measured state | Consequence |
|---|---|---|
| capability matrix | **no table** | the headline questions (`wip-limits`, `offline`) have no data |
| field reports | **no table** | no `worked for 83% on arm64` |
| project arena standings | **0 `arena_entries` rows**; 70 arenas are all `feature-priority` | no community judgment |
| `health_score` | **0/70 populated** | `health_quality` term scores 0 for everyone |
| `license` | **66/70 blank**, only `AGPL-3.0`×2, `MIT`×2 | the spec's own example of the biggest split has 0.00 bits of gain |
| alternatives graph | missing | "better for / worse for" edges cannot seed questions |
| tags | **27 tags on ≥3 projects**, 283 assignments, 6 projects untagged | real |
| languages | **10 languages**, 59/70 projects | real |
| governance | `collective`×62, `maintainer_led`×8 | real, 0.51 bits |
| FTS5 text | `projects_fts` live | real |

**Information gain of the whole live catalog**, by dimension (Shannon entropy of
the value distribution, the quantity §5.1's selector maximises):

| dimension | distinct values | entropy (bits) |
|---|---|---|
| tags | 27 | **4.33** |
| language | 10 | **2.84** |
| governance | 2 | 0.51 |
| license | 1 (blank) | **0.00** |

So: **4.68 bits exist across all hard-constraint dimensions.** Akinator needs
20–30 bits to converge on a specific software product. The gap is not a UI
problem, and no amount of question-ordering fixes it — the catalog has to grow.
R5 is therefore not a prerequisite chosen for tidiness; it is the substance.

**Design consequence:** the engine must degrade honestly. A dimension with 0.00
bits must never be asked, because principle #1 ("every question earns its place")
is violated by asking it. §5.3 turns that from a silent failure into a visible
one: a catalogue of **declared gaps**, rather than a quiz that asks about nothing.

---

## 2. Scope

### In (Phase 1, ships with this work)

1. **Capability matrix** — structured claims, evidence, confirmation by quorum,
   disputed state (§R5).
2. **Field reports** — structured experience records, reputation-weighted
   outcomes, owner response right, removal by quorum with appeal.
3. **Finder engine** — dynamic question selection by expected information gain,
   tri-state and single-choice answers, live short-list, skip / doesn't-matter /
   decide-later, reversible history, stop conditions.
4. **Finder page** — two-pane, keyboard-driven, embedded vanilla JS.
5. **Results page** — ranked matches with per-answer explanations, unknown-fit
   panel with a contribution CTA, filtered-out panel with lift-filter, no-match
   fallback.

### Out (recorded, not built, with the reason)

| deferred | why |
|---|---|
| GraphQL (§10.3) | no GraphQL exists; `docs/PLAN-r4.md` "Non-goals" already excludes it |
| React/Svelte components in §7 | no build step exists; frontend spec §1.2 chose (b) |
| range sliders, batch mode, rank picker | Phase 2 in the Finder spec; Phase 1 ships single-choice + tri-state |
| collaborative sessions | spec §9 Q5 calls it "interesting, not MVP" |
| pairwise tiebreakers | spec §9 Q3; needs a `project` arena with real entries first |
| session learning from logs | privacy review not done; §5.5 |
| health score reweighting sliders | `health_score` is 0/70; there is nothing to reweight |

---

## 3. R5 — Capability matrix

### 3.1 Model

A capability is a **keyed question about a project** (`wip-limits`, `offline`,
`self-hosted`), grouped into a category, carrying the values a project may be
asserted to have. An **assertion** is one contributor's claim that a project has
some value for some capability. Assertions carry evidence and are promoted to a
**confirmed** state by quorum.

Three tables (`0021_capabilities.sql`):

```
capabilities(
  key         TEXT PRIMARY KEY,       -- 'wip-limits', normalised
  label       TEXT NOT NULL,          -- "WIP limits"
  category    TEXT NOT NULL,          -- 'project-management'
  kind        TEXT NOT NULL CHECK (kind IN ('boolean','enum')),
  values      TEXT NOT NULL,          -- JSON array; 'yes','partial','no' for boolean
  -- The three-way distinction the Finder engine depends on: `no` and
  -- `unknown` must be different strings or the engine cannot honour
  -- principle §2.7 ("unknown is not no").
  created_at  REAL NOT NULL
)

capability_assertions(
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  capability    TEXT NOT NULL REFERENCES capabilities(key),
  project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  value         TEXT NOT NULL CHECK (value IN ('yes','partial','no','unknown')),
  evidence      TEXT NOT NULL DEFAULT '',
  asserted_by   INTEGER NOT NULL REFERENCES users(id),
  asserted_at   REAL NOT NULL,
  UNIQUE(capability, project_id)      -- one current assertion per (cap, project)
)

capability_confirmations(
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  assertion_id INTEGER NOT NULL REFERENCES capability_assertions(id) ON DELETE CASCADE,
  user_id      INTEGER NOT NULL REFERENCES users(id),
  confirmed    INTEGER NOT NULL CHECK (confirmed IN (0,1)),
  at           REAL NOT NULL,
  UNIQUE(assertion_id, user_id)       -- one vote per person per assertion
)
```

**`unknown` is a value, not an absence.** A row with `value='unknown'` is a real,
recorded statement that nobody knows; an absent row is "nobody has said". Finder
treats absent rows as unknown for scoring but can offer to fill them.

### 3.2 Confirmation by quorum

§4.2: "Confirmation uses the same quorum machinery as everything else." The
existing machinery is `internal/store/taxonomy.go` — proposals plus consents
counted against `eligibleTaggers`. Reused shape:

- **2 independent confirmations promote an assertion to confirmed.** Same κ as
  solutions' coverage quorum (`0018`), and stated here so both are one number.
- A **dispute** is `confirmed=0` from a user who is not the author. Two disputes
  against one confirmation mark the claim **disputed**: shown as disputed, never
  silently counted as `no`.
- A dispute is never resolved by the author's own vote, and a user cannot
  confirm an assertion they asserted.

### 3.3 AC

- Two independent confirmations promote an assertion to `confirmed`.
- The author's own confirmations never promote anything.
- An assertion with 2 disputes shows `disputed`, and `no` is never substituted
  for `disputed`.
- A capability value outside its declared `values` array is rejected.
- Deleting a project deletes its assertions and confirmations (`CASCADE`).

---

## 4. R5 — Field reports

### 4.1 Model

`0022_field_reports.sql`:

```
field_reports(
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id      INTEGER NOT NULL REFERENCES users(id),
  version      TEXT NOT NULL DEFAULT '',
  use_case     TEXT NOT NULL DEFAULT '',   -- tagged; §4.7 "use case (tagged)"
  environment  TEXT NOT NULL DEFAULT '',   -- 'arm64','docker','bare metal'
  scale        TEXT NOT NULL DEFAULT '',
  duration     TEXT NOT NULL DEFAULT '',
  outcome      TEXT NOT NULL CHECK (outcome IN
                 ('worked','worked-with-caveats','abandoned','migrated-away')),
  migrated_to  TEXT NOT NULL DEFAULT '',
  caveats      TEXT NOT NULL DEFAULT '',
  workaround   TEXT NOT NULL DEFAULT '',
  advice       TEXT NOT NULL DEFAULT '',
  removed      INTEGER NOT NULL DEFAULT 0,
  removed_by   INTEGER REFERENCES users(id),
  removal_reason TEXT NOT NULL DEFAULT '',
  created_at   REAL NOT NULL
)

field_report_responses(
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  report_id   INTEGER NOT NULL REFERENCES field_reports(id) ON DELETE CASCADE,
  user_id     INTEGER NOT NULL REFERENCES users(id),
  body        TEXT NOT NULL,
  is_owner    INTEGER NOT NULL DEFAULT 0,
  created_at  REAL NOT NULL
)
```

**Outcome is an enum, not free text.** §4.7 requires a report be structured "so it
cannot be just a rant"; a free-text outcome field is exactly the rant it rules
out. `migrated_to` is `NOT NULL` in intent — a `migrated-away` report naming no
successor is half a report — so it is validated in Go, where the message can say
which field is missing.

**Owners respond but cannot delete.** `is_owner` is recorded at write time from
`GetHighestRole`, not trusted from the client. A report is never hard-deleted:
abusive or fabricated reports are *removed* by quorum, and the row stays with
`removed=1` so the decision is auditable.

### 4.2 Weighting

§4.7: reports "are weighted by reputation and by report quality". Weights come
from `internal/ranking.ExpertiseMultiplier`, which already exists. Outcome rate
for a project is the reputation-weighted share of `worked` +
`0.5 × worked-with-caveats`. The `0.5` is declared as a named constant with its
reason, because an unnamed 0.5 is a number nobody can later argue with.

### 4.3 AC

- A report with `outcome='migrated-away'` and an empty `migrated_to` is refused
  with a message naming the missing field.
- An owner response is marked `is_owner=1` when written by a project member, and
  cannot be edited or deleted by its author afterwards.
- The owner of a project cannot set `removed=1` on a report.
- `outcome_rate` weights by reporter reputation, and a single high-reputation
  report does not outvote three low-reputation ones.
- Deleting a project deletes its field reports.

---

## 5. Finder engine

### 5.1 Candidate set and question selection

The candidate set `C` is seeded by a text query (`SearchProjects`) or a category
tag, then narrowed by hard-filter answers. Questions are **enumerated from the
data**, never from a script:

| family | source | values |
|---|---|---|
| capability | `capabilities` asserted on ≥20% of `C` | `yes`/`partial`/`no`/`unknown` |
| platform | `project_languages`, `project_metrics` | distinct values present in `C` |
| governance | `projects.governance_model` | values present in `C` |
| license | `projects.license` | values present in `C`, only if ≥2 distinct |
| maturity | `project_metrics.last_commit_at`, `commit_count` | bands |

**Selection: highest expected information gain**, computed from the *observed*
distribution in `C`, and a candidate question is only eligible if its gain is
above `minGain` (0.35 bits). A dimension with one distinct value has 0.00 bits
and is therefore **never asked** — that is principle §2.1 enforced by arithmetic
rather than by a list of forbidden questions.

Boosts and penalties, in the order they are applied:

1. **asked** — a question already answered is excluded outright.
2. **correlation** — a dimension whose value is constant across `C` after
   filtering is already resolved; recomputed every turn, so it needs no table.
3. **catalog confidence** — prefer questions where most of `C` has a known value;
   a dimension that is 94% unknown is deprioritised, because asking it produces
   mostly-unknown answers and a mostly-unknown short-list.
4. **quality** — boolean/small-cardinality questions early; range and rank later.
5. **diversity** — no more than 2 consecutive questions from the same family.

Unknown values never filter. A `unknown` answer to a required capability
**keeps** the candidate with an `unknown_data_penalty`; it does not remove it,
which is §2.7 and §5.3 of the Finder spec.

### 5.2 Fit score

```
fit = capability_match      (0..1, unknown contributes 0 but incurs the penalty)
    + platform_match        (0..1)
    + governance_match      (0..1, small weight: it is a real but weak signal)
    + field_report_rate     (0..1, weighted by §4.2)
    + arena_standing        (0..1, 0 when there are no entries — see below)
    - unknown_data_penalty  (0.05 per unknown field)
```

Every weight is returned in the API response so the results page can show *why
this rank* without the client hardcoding a second set of numbers. §5.2 of the
Finder spec requires the weights to be user-visible and tunable; **tunable is
Phase 2** and recorded as such rather than half-built.

`arena_standing` is 0 for every candidate today because `arena_entries` has 0
rows and no `alternatives` arena exists. It is computed, not hardcoded to 0, so
that when R5's alternatives arena lands the term starts working with no code
change — and a test asserts it is *computed*, so the day it silently becomes a
constant it fails.

### 5.3 The gap catalogue

Because the catalog cannot yet answer most questions, the engine publishes its
own ignorance as a first-class result. When the highest-gain question falls below
`minGain`, the response carries:

- `stopReason: LOW_GAIN`
- `gaps[]` — the dimensions that would have discriminated and the number of
  candidates held back, sorted by how many they hold back.

This is the Finder spec's §6.4 "nothing fits" panel, generalised: a user who
reaches the end of the questions gets *what would have to be known*, which is
also the contribution queue for the capability matrix. §5.3 of the Finder spec
("unknown is not no") and §6.4 both land here.

### 5.4 Stop conditions

`SATURATED` (≤5 candidates), `HIGH_CONFIDENCE` (top fit ≥85% and ≥1.15× the
runner-up), `LOW_GAIN` (best next question < `minGain`), `QUESTION_CAP`
(10 questions, configurable per session).

---

## 6. API

REST, mirroring the GraphQL contract in the Finder spec §8 one-to-one so the
GraphQL version is a transcription rather than a redesign.

```
POST /api/v1/finder/sessions            → session id, seed summary, first question
GET  /api/v1/finder/sessions/{id}        → current state, question, short-list
POST /api/v1/finder/sessions/{id}/answer → candidate count, top candidates, next question
POST /api/v1/finder/sessions/{id}/back   → recompute, do not replay
POST /api/v1/finder/sessions/{id}/lift   → remove one filter, re-rank
POST /api/v1/finder/sessions/{id}/results → full ranked results
GET  /api/v1/finder/questions         → every question the engine can ask, with live gain
```

**Sessions live in memory, not in the database.** Finder §4.6 offers "Save a
session" and a shareable `/finder/results?q=<encoded answers>`, and both are
expressible from an answer list without a table — so no migration for a feature
whose only durable need is "a list of answers". An in-memory session is lost on
restart, which for an unsaved exploration is the honest behaviour. Recorded as a
decision, not an omission.

`GET /api/v1/finder/questions` exists for the honest reason that
"Finder never asks about an option the catalog can't actually deliver" (§5.4) is
untestable at the UI level but is one query away at the API level.

---

## 7. Page

One template, `finder.html`, following the exact shape of `ranking.html`: a mount
point, a skeleton, and one script tag. Two panes; question left, short-list
right. Keyboard: `1`–`9` select, `Enter` confirm, `S` skip, `Esc`/`Backspace`
back, `R` why-this-question, `V` results. Mobile: the short-list collapses to a
sticky header card.

`assets/js/finder.js` is one file, no framework, no build step — the frontend
spec's §1.2 decision, applied to a page that is genuinely more interactive than
the others and is the argument for revisiting that decision later.

---

## 8. Definition of done

1. `make verify` green.
2. Migration 21/22 applied to a **copy** of the live DB first, with the index
   count compared before and after and no index lost (the method the visibility
   note established, and the one that caught seven dropped indexes elsewhere).
3. Every §3.3 and §4.3 AC has a named test that fails when its rule is broken.
4. `GET /api/v1/finder/questions` on the live instance returns the real
   dimensions with real gains, and no dimension with 0.00 bits is askable.
5. A seeded capability set exists for at least three categories, so the engine
   has more than one possible question to ask.
6. The Finder page is opened in a browser and driven end to end.
7. `docs/HANDOFF.md` and `docs/PLAN-r4.md` updated: R5 shipped, R8 added.

## 9. What the live run changed

The DoD asked for the page to be opened in a browser. It was, against a copy of
the live database with 12 capabilities seeded, and the run found four defects
that the test suite as it stood could not. They are recorded here because each
one changed a rule in this document rather than only fixing a line of code.

**§5.2 changed.** `fit` is now a weighted sum **renormalised over available
evidence**, with `evidence_coverage` reported beside it. A candidate matching
every answer used to score 0.15 on this instance, because the field-report and
arena terms — both of which this instance has no data for — contributed 0.45 of
pure zero. The rule is: a term with no denominator leaves the sum entirely, in
both numerator and denominator. "Unknown" must never be evidence *against* a
candidate; it is the absence of evidence, and absence is priced by dividing, not
by subtracting.

**§3.3 gained a sibling case.** `capTotal` counts what the user **asked**, not
what a candidate happens to have data for. It did the latter, so the candidate
with the least data lost the weight carrying its own unknown penalty and was
never demoted at all.

**§3.3's gaps are about unknown data, not askability.** `Gaps()` previously
skipped every dimension whose gain cleared the threshold, which made the
contribution loop look functional for the wrong reason: a capability appeared as
a "gap" because its gain was 0.295, not because anything was unknown about it.
A dimension can be an excellent question *and* have 60% of its candidates
unknown, and those 60% are precisely what §5.3 wants contributed.

**§2.4 has a sharper statement than "never remove".** The engine counts a stored
`unknown` (the capability matrix's own value for *somebody recorded that nobody
knows*) as an unknown bucket. It did not, and the two places that enumerated
"the ways to be unknown" were written independently, so one was short.

**Live numbers, for the record.** 70 candidates, 12 dimensions derived, 3
askable: `language` at 2.71 bits, `governance` at 0.51, `license` at 0.37. The
other nine are between 0.11 and 0.30 bits and are correctly *not* offered — on
this instance there is nothing else worth asking. 11 capability gaps reported.
70 → 27 → 22 candidates across two answers, with the top match at "99% fit on
20% of the evidence".

The engine reporting that only 3 of 12 dimensions are worth asking is the
correct outcome on this data, not a defect: it is §2.1 refusing to waste the
user's time. An Akinator needs 20–30 bits of entropy to work; this catalog has
4.68 across every hard-constraint dimension.

## 10. The GraphQL contracts, recorded

The Finder spec's §8 contracts map one-to-one onto §6. The one shape difference
worth stating: `impactIfChosen` is computed server-side and returned, not derived
in the client, because it depends on the candidate set's live distribution.