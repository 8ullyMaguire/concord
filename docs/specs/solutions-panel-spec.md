# Concord — Solutions panel on the project page (S1.5)

Written 2026-10-03 against `1d07a82`. Implements the third panel of
`docs/specs/phase3-spec.md` §4.1 item 3, and closes S1.

The plan's step is S1.3 (see `docs/plans/phase3.md`); Capabilities and Field
reports shipped in `fd2ceae` + `633cdcc` and this is the remainder.

## The question, answered honestly first

§4.1 item 3 says "the feature's ranked solution standings, with coverage and
confidence, using the existing `ListSolutions` payload". Read literally, that is
the **roadmap** panel of §4.10 — standings *per feature*. But the project page
does not know which feature the reader cares about, and adding a feature picker
is a fourth surface that §4.1 did not scope and §4.2's "no new page" rule makes
expensive.

`ListSolutions` is per-feature:

```go
func (d *DB) ListSolutions(ctx context.Context, featureID int64, limit int) ([]SolutionScore, error)
```

So the honest readings are:

1. **Standings per feature, grouped.** For each of the project's features, its
   ranked solutions. That IS §4.10's "Roadmap (native: ranked features, solution
   standings)" read as one panel, and it needs no new store query — the project
   page already fetches the feature list.
2. **A project-wide leaderboard.** Requires a NEW cross-feature query, and
   cross-feature ranking is arithmetically dubious: a solution's score is
   relative to the arena entries competing with it, so "best solution in the
   project" mixes scores computed against different opponents.

Going with (1). It uses the existing payload, needs no new query, and does not
invent a comparison the ranking engine never computed. (2) is a real gap and is
recorded in KNOWN-ISSUES rather than shipped as a misleading leaderboard.

## Shape

`GET /api/v1/projects/{project_id}/solutions`

```json
{
  "project_id": 1,
  "features": [
    {
      "feature_id": 7,
      "feature_title": "...",
      "solutions": [ ...SolutionScore... ],
      "shown": 3,
      "omitted": 0
    }
  ],
  "features_with_solutions": 1,
  "features_without": 2
}
```

Decisions, each of which a test must be able to fail:

- **`features_without` is present and counted.** "One feature has proposals and
  four do not" is the more useful sentence than "1 solution", and a panel that
  silently omits the four reads as though one feature were the whole project.
- **Per-feature `shown`/`omitted`, not a project-wide cap.** Truncating inside a
  feature hides the feature's own runner-ups, which is the thing standings are
  for. The panel is bounded per feature at 5.
- **`features` includes features with zero solutions, each carrying an empty
  list.** Same reason as `features_without`, and it keeps the client's job
  trivial: no second fetch to find the empty ones.
- **No cross-feature score comparison is implied by the order of `features`.**
  It is the project's feature order, not a ranking.

## Rules

1. **Visibility first**, through `requireProjectID` like the other two panels. The
   `requireProjectID` existence-oracle fix from `fd2ceae` applies here
   automatically; this route is a new consumer of it, and there is a test that a
   new consumer would break if someone ever gave it a bespoke lookup.
2. **No new store query.** `ListSolutions` per feature, `ListFeatures` for the
   project. Anything else is a new ranking rule and needs its own spec.
3. **A feature with no solutions is not an error and not a 404.** The existing
   handler says the same about a feature nobody proposed for.
4. **Empty project-wide (no features at all) is a distinct state** from a project
   with features but no solutions: the former says the project has no roadmap,
   the latter says the roadmap has no proposals.

## Definition of done

- One handler + one route, top-level `{project_id}` (registering it inside the
  `/{slug}` block is the mistake `fd2ceae` documents — chi binds `slug`, the
  helper reads `project_id`, every call 404s with `project ""`).
- API tests: bounded-and-counted, empty states, private refusal indistinguishable
  from absent, non-slug path is 404.
- Script tests: fetches it, renders all three states, does not invent a
  cross-feature leaderboard, escapes the feature title.
- Playwright: standings in rank order under their feature, `features_without`
  stated, no console errors.
- `make verify`, `make gates`, `make e2e` green.

## Files

| file | change |
|---|---|
| `internal/httpapi/project_solutions.go` | **new** — handler + response type |
| `internal/httpapi/server.go` | one route |
| `internal/httpapi/project_surfaces_test.go` | API tests |
| `internal/httpapi/project_panels_script_test.go` | script tests |
| `internal/httpapi/assets/js/project.js` | panel |
| `internal/httpapi/assets/css/style.css` | append only |
| `tests/e2e/project_panels_e2e.py` | browser tests + fixture |
| `docs/specs/phase3-spec.md` | §4.4 gains this panel |
| `docs/KNOWN-ISSUES.md` | the cross-feature leaderboard gap |