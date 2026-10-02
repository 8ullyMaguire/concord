# Concord — r4 implementation plan

Derived from `docs/concord-spec-r4.md` (recovered 2026-10-02 from the session
transcript; it had never been written to disk). M0–M12 in `docs/PLAN.md` are
complete. This plan covers what revision 4 adds.

## Where r4 stands against the code

> The rows below were surveyed before any milestone was built. Cells have been
> corrected against the running instance where the state has since changed; the
> survey's *reasoning* (that several r4 names are renames of existing tables) still
> holds and is worth keeping.

Measured by reading the live schema and the ranking package, not by inferring
from the spec's table list — several r4 names are renames of tables that already
exist under other names (`threads`→`comments`, `roles`→`members`,
`boards`/`columns`/`cards`→`board_*`, `reputations`→`reputation_events`). The
survey below was taken when the schema had 69 tables; it is now 73, and the table
count in the Status section is the current one.

| r4 requirement | state | note |
|---|---|---|
| Glicko-2 engine | **exists** | `internal/ranking/glicko2.go`, but feature-hardcoded |
| Multi-criteria ranking | **exists** | `criterion_ratings` keyed on `feature_id` |
| Vote log append-only | **partial** | `pairwise_votes` now has `arena_id` + `reason` (0017); still no DB trigger forbidding UPDATE/DELETE |
| **Arenas (§5)** | **shipped** | `arenas`, `arena_entries`; 70 live. Was the spec's central abstraction |
| **Solutions (§6.3–6.5)** | **shipped** | all six types, forks, claims |
| Solution arena + baseline | **shipped** | permanent per feature; see KNOWN-ISSUES on `is_baseline` |
| Coverage + challengeable claims | **shipped** | κ=200; contested claims stop counting until upheld |
| Derived/forked solutions | **shipped** | `parent_id`, rating inherited with inflated RD |
| Ranking->consensus gating | **partial** | gate + agenda live; no `decision_records` |
| Decision records (ADR) | **missing** | §6.5 — the one remaining gap in this pillar |
| Complaints | exists | + duplicate detection (this session) |
| Features | exists | r4 wants `outcome`/`criteria` fields |
| Consensus | exists | + hold, tiers, stand-aside fixed |
| Lists / Requests | exists | separate vote tables, not arenas |
| Field reports | **missing** | §4.7 |
| Capabilities matrix | **missing** | §4.1 |
| Scout | **missing** | §4.5, Phase 3 |
| Alternatives arena | **missing** | §5 table |

## Milestone order

Chosen so each ships something usable and each is independently verifiable.
Arenas come first because solutions need one, and generalizing the ranking engine
is a prerequisite for four of the five r4 pillars.

### R1 — Arenas: the generic ranking substrate
Migration `0017`: `arenas`, `arena_entries`, and `pairwise_votes` gains
`arena_id` + `reason` (existing feature votes backfilled into a
`feature-priority` arena, unchanged semantics).
- `internal/store/arenas.go`: create, get, list, `EnsureArena`,
  `UpsertEntry`, `RemoveEntry`, baseline handling.
- Generalize `ranking` to `(arena, entityType, entityID)`; keep
  `criterion_ratings` working for both features and solutions.
- Pair selection and vote application re-point at arenas; the existing
  `/vote/next` and `/vote` routes keep working through a per-project feature
  arena.
- AC: a feature vote still moves a rating; the same vote log now also scores a
  solution pair; `vote/next` never returns a pair the voter already judged.

### R2 — Solutions
Migration `0018`: `solutions`, `solution_complaint_claims`.
- Types `build-new`, `extend-existing`, `integrate-external`,
  `config-or-docs-only`, `workaround`, `do-nothing`; relation
  `exclusive`/`complementary`; `parent_id` for forks; `external_project_id`.
- Store: propose, get, list, fork (inherit `r` with inflated RD), challenge/
  uphold a coverage claim.
- API: `POST/GET /features/{id}/solutions`, fork, claim challenge.
- AC: forking a rated solution starts near its parent's rating, not at 1500;
  a `do-nothing` solution exists permanently in every feature's arena.

### R3 — Solution ranking with coverage
`κ = 200`, charter-configurable. `coverage = pain(resolved)/pain(all linked)`.
- Challenger cannot contest their own claim; a contested claim stops counting
  until upheld.
- Leaderboard: rank, score, confidence, coverage, effort, risk, type, pro/con
  digest.
- AC: a solution claiming coverage of an unlinked complaint is rejected; a
  challenged claim contributes 0 coverage.

### R4 — Ranking → consensus and ADRs
- Auto-open eligibility: leads runner-up **and** baseline at ≥80% win
  probability, enough distinct voters, stable N hours.
- Outcomes: accepted, accepted-with-amendments (spawns a derived solution),
  fall back to #2, rejected (baseline wins), sent back.
- `decision_records` written on every close, with positions, objections,
  remedies, and the fallback chain that was pre-agreed.
- AC: a call cannot auto-open against a solution that does not beat the
  baseline; a `fall back to #2` outcome records the next solution in the ADR.

### R5 — Discovery intelligence
- `field_reports(project, user, version, use_case, env, outcome, body)`.
- `capabilities` + `capability_confirmations`.
- Alternatives: an arena per (project × use-case) comparing projects.
- AC: a field report on an unclaimed version is listed as unconfirmed; two
  independent confirmations promote a capability claim.

### R6 — Scout
`scout_reports(input, capabilities, matches, verdicts)`: "I need X" → ranked
candidates with adopt/extend/build verdicts.
- AC: a Scout run for a capability the catalog has returns the ranking with
  coverage evidence, and a verdict per candidate.

### R7 — Documentation sync
- Replace `docs/concord-spec.md` with the r4 text (it is a copy of the master;
  the master was rewritten, so the copy is now two revisions stale).
- `docs/PLAN.md`: mark r4 milestones, correct the Current State block.
- `docs/ARCHITECTURE.md`: the arena abstraction and why.
- Import the spec into Concord itself as a document, as was done for
  `frontend-spec.md`.

## Non-goals

Phase 4 of the r4 roadmap (federation, mobile, code search) — the spec's own
roadmap puts these at months 12–24 and the current work is Phase 1–3.
GraphQL (§10.3) — the API surface is still moving; a generated schema now would
be rewritten. Federation and MCP are listed in the plan for later.

## Status

Measured against the running instance (schema 19, 73 tables, 443 tests,
`v0.4.0-voting-36-gb964f75`), not against the plan's own history. "Shipped"
means there is code, a migration and a test -- not that the surface is complete.

| Milestone | Status | Notes |
|---|---|---|
| r4 spec recovered to `docs/concord-spec-r4.md` | done | 55,987 chars, was only ever in the transcript |
| r4 gap survey | done | the table above |
| R1 arenas | **shipped** | `0017_arenas.sql`; 70 arenas live. Generalized ranking is the substrate; `pairwise_votes` gained `arena_id` + `reason` |
| R2 solutions | **shipped** | `0018_solutions.sql`; all six types, `exclusive`/`complementary`, forks with inflated RD, claim challenge/uphold |
| R3 solution ranking + coverage | **shipped** | kappa=200; leaderboard carries rank/score/confidence/coverage/effort/risk. Known issue: `is_baseline` has no DB-level guarantee |
| R4 ranking->consensus gating | **partial** | `0019_solution_consensus_calls.sql`, agenda and eligibility gate live. **`decision_records` does not exist** -- the ADR on close is the missing half |
| R5 field reports + capabilities | **not started** | no `field_reports`, `capabilities` or `capability_confirmations` table |
| R6 Scout | **not started** | no `scout_reports` table |
| R7 docs sync | **in progress** | this file, README, KNOWN-ISSUES and HANDOFF updated 2026-10-02. `docs/concord-spec.md` is still two revisions stale -- it remains a verbatim copy of the master that the r4 rewrite replaced |

### What is deliberately left

Section 8.2's eligible collaborator has two clauses with no representation, and
both are stated in `internal/store/eligibility.go` rather than approximated:
"seated by charter consensus" (no charter amendment or seat table exists to
count) and "agents are excluded" (`users` has no agent flag).

### Where the next milestone should go

R4's `decision_records`. A call can now open and decide, and the *fallback chain*
that section 6.5 requires to be pre-agreed has nowhere to live, so the part of the
outcome a reader most needs later -- why we did this instead of the runner-up --
is recorded nowhere. It is a small migration and a small write path, and it closes
the last gap in the consensus pillar.
