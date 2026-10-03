# Alternatives arenas — S2 (phase3-spec §5)

A "which is better for X" list over **competing projects**, not features and not
solutions. §7.3 wants a ranked *better for* / *worse for* list per use case, and
§4.10's project page has no surface for it.

## 1. Why this is assembly, not construction

`ArenaAlternatives` and `ArenaUseCase` exist as constants
(`internal/store/arenas.go:49-50`), in `DefaultArenaQuestion`, and in
`FindArena`'s key shape. `EntityProject` is already a first-class entity in
`refuseOwnVote` (`arenas.go:590`) and `CastArenaVote` already refuses a
mixed-type comparison (`arenas.go:465`).

So: the arena kind, the entry entity, and the vote maths all exist. What is
missing is (a) a way to create one, (b) a way to add a competing project to it,
and (c) a read. **There is no HTTP surface for arenas at all** — no route in
`internal/httpapi/server.go` mentions `arena`. Every one of the four endpoints
below is new wiring over working store methods.

That is the point of R1 (arenas as the single ranking engine), and it is worth
stating as a check: if S2 had needed a new rating computation, it would not have
been S2.

## 2. Model

An alternatives arena is `(project, use_case)` with `EntityProject` entries.
`FindArena` already handles `ArenaAlternatives` on exactly that triple.

- `POST /api/v1/projects/{project_id}/alternatives` — `{use_case, question}`
  creates the arena. Contributor+ (S1's rule for panel-mutating writes).
- `POST /api/v1/projects/{project_id}/alternatives/{arena_id}/entries`
  `{project_id}` adds a competing project.
- `GET /api/v1/projects/{project_id}/alternatives` — every alternatives arena
  for this project, each with its ranked entries.
- `GET /api/v1/projects/{project_id}/alternatives/{arena_id}` — one arena's
  ranked list, each entry with `better_for` / `worse_for` naming the use case.

`{project_id}` in a route path is a **slug** — `requireProjectID` resolves it
(auth.go:143). The `project_id` in an **entry body** is a competing project's
numeric id, and it is the one place the two meanings coexist, so both are named
explicitly in the code that reads them.

## 3. Rules

**No baseline.** §6.3's "do nothing" is a *solution* concept. For an
alternatives arena "do nothing" means "keep using the incumbent", which is the
arena's own subject rather than a competitor to it. `setBaseline` is never called
for this arena type and `ArenaLeaderboard` must not expect one. This is asserted
as a mutant, because the failure — a baseline appearing — would look like a
legitimate entry in a response.

**The arena cannot contain the project it is about.** An arena on project P
comparing P against P is not a comparison. `refuseOwnVote`'s sibling rule: refuse
the add at the entry point, so the arena never holds the pair. Refusing only at
vote time would leave a reader able to see the arena's own project listed as a
competitor to itself.

**Visibility on both sides.** A candidate project the caller cannot read cannot
be added, and must not be ranked into a response the caller can see. Same
anti-enumeration reasoning as Finder and as S1's panels: the refusal is a bare
`not found`, not a 403, because a 403 confirms the project exists.

**A use case is required, and the refusal names it.** `FindArena` keys the other
two shapes on `use_case = ''`, so an empty use case collides with them under a
different type. Reject empty, and put the field name in the message so a client
knows which field to fix.

**Weight is computed server-side**, never read from the body — the same rule as
the feature and solution vote handlers, because a client-supplied weight is a
client-supplied influence.

**A vote cannot reach a feature.** `applyGame` mirrors a rating back onto
`features.elo_*` only `if aType == EntityFeature` (arenas.go:680). An
alternatives vote passes `EntityProject`, so it cannot move a feature's rating.
This is asserted, not assumed — the mirror is the kind of side effect that
regresses silently.

## 4. Definition of done

Named tests, each of which must be able to fail:

| Test | What it catches |
|---|---|
| `TestAnAlternativesArenaRanksProjectsNotFeatures` | S2 quietly reusing the feature arena: entries must be `entity_type='project'` |
| `TestAVoteOnAProjectPairMovesBothProjectsRatings` | one side of the vote not being applied |
| `TestTheSameVoteDoesNotTouchAnyFeaturesRating` | the `mirrorFeatureRating` guard regressing |
| `TestTheArenaCannotContainTheProjectItIsAbout` | the self-comparison entry point |
| `TestAnAlternativesArenaHasNoDoNothingEntry` | §6.3 leaking into this arena type |
| `TestAnAlternativesArenaRefusesACandidateTheCallerCannotSee` | the anti-enumeration rule |
| `TestAnEmptyUseCaseIsRefusedAndTheRefusalNamesIt` | the collision with `use_case=''` |
| `TestTheAlternativesPanelRendersARealRanking` | Playwright: the panel shows entries, not an empty state |

Plus a mutation gate over the new code with **own-project** and **no-baseline**
as mutants, because those are the two rules a reader would not notice breaking.

## 5. Panel placement

Fourth panel on the project page, after solution standings: a list of use cases,
each with its ranked competitors and the "better for / worse for" line. No arena
is a distinct empty state ("no use cases compared yet") — the same rule as the
solutions panel's "no roadmap yet" vs "no proposals yet" split.

## 6. Known deferrals

- **Migration notes on the edge** are a document, not a column. §7.3 wants them
  community-written and ranked. The edge itself is `pairwise_votes`, append-only.
- **Visibility filtering inside a ranked list.** A private competing project that
  predates S2's add guard stays in the arena. Entries are filtered at read time
  for the caller's readability, and that filter is the thing §7's mutants cover.