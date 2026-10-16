# Spec — §6.2's strategic themes, end to end

Status: **IMPLEMENTED** 2026-10-16 (migrations 0028–0029, `internal/store/themes.go`,
`internal/store/feature_tags.go`, the strategy and feature-tag routes, the settings and feature
panels). Written at `bc930ed` before any code, which is the order the standing rule requires.
`docs/REMAINING.md` §25 is the as-built record, including the three defects found while building
and the two mutants that survived their first run (one of them because the test could not tell two
guards apart, the other because it was hiding a real bug). Two deviations from this document as
written: the lifetime bound is 1–36500 (a century, so the check catches a typo rather than a
policy), and fixing the themes exposed a second missing writer — `feature_tags` had none — which
was built too rather than recorded. Implements §6.2's third steering input.

## 1. What the spec requires

- **§363** `priority = (r − 2·RD) + λ·log(1 + pain_sum) + μ·theme_weight`
- **§366** *"`theme_weight` comes from themes, which are tag-based weights such as
  'security hardening: +2'. They are changed only by consensus (Normal tier), have a public
  changelog, and expire at the next release cycle unless renewed. Nobody can pin an item to
  the top by hand. Steering happens through public inputs: pain, pairwise votes, and
  ratified themes."*
- **§649** the data model names `themes(tag, weight, expires_at)`.
- **§452** "themes affecting priority globally" are a **Major**-tier decision ⇒ a
  project-scoped theme is the Normal tier §366 names.
- **§580** the charter covers the "strategic themes process".
- **§348–350** the PAIN formula's `theme_multiplier` is *also* "taken from
  consensus-ratified strategic themes (§6.2)".

## 2. What exists, measured at `bc930ed`

| piece | state |
|---|---|
| `strategic_themes` table (0012) | exists, with `expires_at REAL NOT NULL`, `namespace`, `set_by`, `rationale` |
| `strategic_theme_proposals` table (0012) | exists: `theme_tag`, `theme_namespace`, `weight_after`, `status` (pending/ratified/rejected/withdrawn/stale), `decided_at`, plus `idx_theme_one_pending` |
| `store.StrategicTheme` | has `ExpiresAt`, `ThemeKey` |
| `store.ListStrategicThemes` + `GET /projects/{project_id}/strategy/themes` | exist; the settings page lists them |
| `store.ThemeWeightTotal(projectID, tags)` | **exists and nothing calls it** — so `theme_weight` is 0 in every ranking, in every project, today |
| anything reading/writing `strategic_theme_proposals` | **nothing at all**: no store function, no route, no test |
| the μ slot | fed `f.StrategicWeight` (`features.go:216`) — the per-*feature* ratified weight, in the slot the spec's formula calls `theme_weight` |
| live instance | 0 themes, 0 theme proposals (nothing can create either) |

Two consequences worth stating plainly, because both are traps for a reader:

1. **The migration is honest and the code is not.** `0012`'s own comment says "expires_at is
   NOT NULL because a weight that never expires is permanent aristocracy by another name",
   and the column is there. Nothing writes it, nothing reads it, nothing enforces it.
2. **The spec's third steering input is inert while a parallel mechanism occupies its
   slot.** A feature's own ratified weight moves the ranking; the theme sum — the mechanism
   §366 actually names — is computed by a function with no caller.

## 3. Decisions

### D1. `theme_weight` is the SUM of both ratified inputs (feature weight + matching themes)

The spec's formula has one term. The tree has two mechanisms reaching for it: the per-feature
weight (§12.3's weight proposals, built, gated, tests pinning that ratification moves the
score) and tag-scoped themes (built as data, unwired).

- **Chosen:** `theme_weight = f.StrategicWeight + Σ(live theme weights whose tag is on the feature)`.
- **Rejected — themes only:** would make the entire weight-proposal mechanism inert, which is
  a bigger regression than the gap being fixed, and its tests are the evidence that
  ratification is supposed to move the score.
- **Rejected — feature weight only (status quo):** leaves §366 unimplemented.
- The sum is a **spec question**, not a house decision: §366 names one input. Filed as
  `docs/QUESTIONS.md` Q2 so the owner can make it themes-only if that is what was meant.
  Until then the sum is the choice that leaves no built mechanism inert.

### D2. "The next release cycle" is a charter field, `ThemeLifetimeDays`, default 90

No release cadence is modelled anywhere in the schema, and §366's phrase assumes one. §580
puts the themes process in the charter, and the charter is where this repository keeps
"judgements about how large this project is and how fast it moves" (the exact reasoning
`SolutionStableHours` uses). So: a new charter field, `ThemeLifetimeDays`, default 90, bounds
1–36500, persisted in `charters`, settable by the existing maintainer-gated `PUT /charter`.

`expires_at = now + ThemeLifetimeDays·86400` is written on ratification; a **renewal** is
another ratification for the same tag, which moves `expires_at` forward. That is "unless
renewed" with no second mechanism.

### D3. Expiry is enforced at READ time, not by a sweeper

There is no scheduler in this server, and a lapsed weight is only wrong when it is used.
Every read that contributes weight filters `expires_at > now` — `ThemeWeightTotal` and
`ListStrategicThemes`. The row is kept (the audit log and the row together are the public
changelog of what was in force and when).

### D4. One live theme row per (project, tag, namespace)

Ratification upserts: update the existing row's weight/expiry/set_by, or insert. The
`idx_theme_one_pending` index already prevents two competing PENDING changes to one tag;
upsert keeps the same shape on the live side.

### D5. The consent path mirrors the weight proposals exactly

Proposer cannot consent to their own proposal (§5.1); a consenter must be an eligible
collaborator (§8.2, with the "eligible count 0 makes quorum 0 and one consent ratifies"
hazard the weight code documents); quorum is `governance.EvaluateConsensus` over the charter
thresholds (§6.6); a second consent is idempotent-but-refused, and each consent writes an
audit row. Reusing the shape is deliberate — one ratification path, not two that drift.

### D6. Project-scoped themes only; a global theme is not built

§452 makes a global theme a Major-tier decision, which needs the tier machinery and a
cross-project scope the schema does not have (`strategic_themes.project_id` is NOT NULL). Not
built; recorded, with §452 cited, rather than silently narrowed.

### D7. The pain-side `theme_multiplier` is not built

§348–350 wants it from the same ratified themes. Complaints have **no tags** in the schema
(`complaints`: project, author, title, body, severity, frequency, strategic_multiplier,
status), so "which themes apply to this complaint" has no answer today. Inventing one
(project-wide? the linked feature's tags? the complaint's words?) would be a product decision
dressed as an implementation detail. Filed as `docs/QUESTIONS.md` Q3.

### D8. The public changelog is the audit log

§366's "public changelog": every propose/consent/ratify/withdraw already writes an
`audit_log` row (`strategic_theme_*`), and `GET /audit` is public. The themes list shows what
is in force; the log shows how each one got there. No second changelog is built.

## 4. Surfaces

| surface | route | gate |
|---|---|---|
| propose a theme change | `POST /api/v1/projects/{project_id}/strategy/themes` | contributor (a proposal is a contribution; §8.2 eligibility would demand a contribution before allowing the first, the same reasoning the weight route records) |
| consent / ratify | `POST /api/v1/projects/{project_id}/strategy/themes/{id}/consent` | eligible collaborator (§8.2), refused for the proposer |
| withdraw | `POST /api/v1/projects/{project_id}/strategy/themes/{id}/withdraw` | proposer, or maintainer |
| list proposals | `GET /api/v1/projects/{project_id}/strategy/theme-proposals` | any reader |
| list live themes | `GET /api/v1/projects/{project_id}/strategy/themes` | exists; now excludes expired |
| the UI | the settings page's strategy panel | propose form + per-proposal Consent/Withdraw + the live themes with their expiry |

## 5. Invariants the tests must hold

1. Proposing a theme applies **nothing** (the live themes and `theme_weight` are unchanged).
2. Consent below quorum is **202** with the server's own count, not 200.
3. Reaching quorum is **200**, writes one live row with `expires_at = now + lifetime`, and
   **moves `PriorityScore`** for every feature carrying that tag — the point of the pass.
4. An expired theme contributes **0** to `ThemeWeightTotal` and is absent from the live list.
5. A weight outside `[ThemeWeightMin, ThemeWeightMax]` is refused by name.
6. One pending proposal per (project, tag, namespace) — a second is 409.
7. The proposer cannot consent to their own proposal; a non-eligible account cannot consent.
8. The theme lifetime comes from the project's charter, and 0/negative lifetimes are refused.

## 6. Not in this pass

Global themes (D6), the pain-side multiplier (D7), a themes-only formula (D1 is a question),
a renewal shortcut other than re-ratifying, and any backfill: there are no existing themes.
