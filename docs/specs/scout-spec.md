# Scout — S3 (phase3-spec §6)

Free-text idea in, a classified shortlist out: decompose the idea into catalog
capabilities, match catalog projects against them with the Finder's scorer, and
say what each match *means* — adopt / base-on / extend / inspire / avoid — with
the signals behind every verdict.

## 1. What already exists

Scout is assembly plus one genuinely new judgement.

Reusable as-is: `finder.Score` and `finder.Candidate` (fit scoring, with
`Explanation["evidence_coverage"]` for the honesty requirement), capability
assertions and their `asserted|confirmed|disputed` states, field reports and
their outcome rate, unresolved complaints, `discovery.HealthScore` with
`DefaultWeights`, `projects.license`, and `project_documents` for persisting a
report.

New: the five-way classification, which has no analogue anywhere in the tree,
and the text→capability decomposition.

## 2. The health trap, and how §6.3's warning is resolved

phase3-spec §6.3 warns that `health_score` is 0 for all 70 live projects, so an
`avoid` rule keyed on that column would classify **everything** `avoid` and pass
any test asserting "at least one avoid". Confirmed against the schema: the column
lives in `project_metrics`, written only by the Phase 0 discovery job.

**Resolution: never read `project_metrics.health_score` as the classifier's
input.** `discovery.HealthScore` is a pure function of `discovery.Metrics`
(recency, responsiveness, breadth, cadence), so Scout computes health from the
same *signals* through the same function, and reports **no signals at all** when
the metrics row is absent.

The distinguishing rule, and the one the honesty tests turn on:

- no metrics row → health is **unknown**, never 0. An unknown project is not an
  abandoned one.
- a metrics row exists → health is real and can cross the floor.

Without that distinction, `inspire` (dead or archived) and `avoid` (abandoned)
collapse into each other, and every project with no telemetry becomes `avoid`.

## 3. Classification rules

Every verdict carries `signals` — the named inputs that produced it. An
unexplained classification is a bug, because the surface exists to answer "why
did it say that".

| verdict | rule | requires |
|---|---|---|
| `avoid` | health known AND below `AbandonedHealthFloor` (0.25) | health + floor |
| `avoid` | license stated as a constraint by the user AND the project's license is incompatible | the constraint |
| `base-on` | top match on the **most** capabilities of any candidate AND health known AND health ≥ floor AND license known-and-permissive | the lead count + health |
| `extend` | the gap on this capability is a narrow, linked, **open** complaint against the matched project | the complaint |
| `adopt` | satisfies a **subset** — `matched < total`, and satisfies the rest vacuously (absent evidence) | the subset count |
| `inspire` | in the catalog, matches, and dead or archived: health known and below the **dead** floor, or no commit within `ArchivedDays` | the signal |

Ordering is load-bearing and is applied in this sequence, first match wins:

1. `avoid` (license) — a hard constraint overrides everything
2. `avoid` (health) — abandoned is abandoned
3. `base-on` — the lead, if healthy and licensed
4. `extend` — a concrete gap someone has already filed
5. `adopt` — a subset match
6. `inspire` — dead but instructive

`inspire` is last because it is the weakest claim: a dead project that is
nevertheless the top match everywhere is still worth reading, but not forking.

**A tie at the top is not a `base-on`.** Two projects leading equally is a fork
request, and the report says so with both names rather than picking the first
row. This is stated because the first implementation took
`if count > best { best = count }` and silently awarded `base-on` to whichever
project SQLite returned first.

## 4. Decomposition

Free text → capability keys from the **catalog only**. A key with no catalog row
becomes a *capability proposal* with `matched: false` and no score — never scored
against nothing, and never silently dropped.

Two things this must not do:

- **Invent a key.** `TestScoutNeverInventsACapabilityOutsideTheCatalog`.
- **Match on the key alone.** A project matching 1 of 9 capabilities is not a
  11% fit; it is a match on one axis with eight unknowns. `adopt` therefore
  requires the subset to be *stated*, and the report carries `coverage` beside
  `fit` so a low fit with high coverage reads differently from a low fit with
  none.

Matching is by explicit signal, in this order, and the order is recorded in
`signals`:

1. a capability key appearing verbatim in the text (strongest)
2. a label or category word from the catalog appearing in the text
3. nothing — the capability is `open`, not a match

## 5. Hard constraints

License, language, platform, self-hosting — read from capability assertions and
`projects.license`. A user-stated constraint that a match violates produces
`avoid` with `signals: ["license_constraint"]`, and the constraint is echoed in
the report so a reader can see it was applied rather than assumed.

## 6. Coverage heatmap

`covered` / `partial` / `open` per capability, from the assertion state: `confirmed`
→ covered, `asserted`/`disputed` → partial, absent → open. This is the same three
states the capabilities panel keeps apart, and reusing them is the point.

## 7. Surface known pain

Unresolved complaints (`status IN ('open','validated')`) and field reports with a
low outcome rate, for the matched projects only. `Reports == 0` is never shown as
a 0% rate — the same distinction `finder.Candidate.Reports` exists to make.

## 8. Persisting a report

`project_documents` with kind **`scout`**, which does not exist yet:
`DocumentKinds` is CHECK-constrained to
`readme|spec|plan|wiki|adr|changelog`. Migration adds the value. Stated here
rather than smuggled into an existing kind, because writing a scout report as
kind `plan` would make `ListDocuments` lie about what it contains.

## 9. Definition of done

| Test | catches |
|---|---|
| `TestScoutDecomposesAnIdeaIntoSeededCapabilities` | the decomposition returning nothing |
| `TestScoutNeverInventsACapabilityOutsideTheCatalog` | a scored key with no catalog row |
| `TestScoutReturnsAReasonForEveryClassification` | any candidate with empty `signals` |
| `TestAnAbandonedProjectIsClassifiedAvoidWithAReason` | the health trap of §2 |
| `TestAProjectWithNoMetricsIsNotClassifiedAvoid` | absent telemetry read as abandonment |
| `TestASubsetMatchIsAdoptAndAMaximalMatchIsBaseOn` | the two verdicts being indistinguishable |
| `TestATieAtTheTopIsNotABaseOn` | §3's tie rule |
| `TestScoutReportsEvidenceCoverageBesideFit` | a fit number with no coverage |
| `TestAReportWithNoFieldReportsShowsNoRate` | `Reports == 0` shown as 0% |
| Playwright | the Scout page renders a classified report |
| Live | `GET /api/v1/scout?idea=...` returns a report on the running instance |

## 10. Out of scope, with reasons

Carried from phase3-spec §6.2 unchanged: stack-file parsing, repo links, live
collaborator calls, one-click project creation, JSON export, dependency
intelligence. None is needed by any rule above.