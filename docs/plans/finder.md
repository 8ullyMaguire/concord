# Concord — Finder: implementation plan

Derived from `docs/specs/finder-spec.md`. Every symbol referenced below was
verified against the tree on 2026-10-03; a symbol marked **(new)** does not exist
yet and is introduced by the step that names it.

## Why the order is this order

R5 before Finder is forced, not preferred. Finder's engine selects questions by
expected information gain over the candidate set, and the live catalog holds
4.68 bits across all hard-constraint dimensions (finder-spec §1). The engine
would be correct and useless. So the capability matrix and field reports are
built first and seeded, and only then does a question-selection engine have
something to select.

Within R5, capability matrix before field reports: both feed the fit score, but
capabilities are the substrate Finder's *questions* are drawn from (§5.1) while
field reports are one score term among six (§5.2). The reverse order works too
and is not worth a note; this order is recorded so a later reader does not
re-derive it.

## Baseline

Measured 2026-10-03 against thinkcentre: `make verify` green, schema **20**,
70 projects, 27 tags on ≥3 projects, 10 languages, 0 arena entries, 0 health
scores, 66/70 blank licenses.

Verify before starting:

```bash
cd ~/code/projects/concord
make verify                      # expect: vet/test/build all clean
sqlite3 ~/.local/share/concord/concord.db "select count(*) from schema_migrations"
# on thinkcentre: expect 20
```

---

## Step 1 — Migration 21: capability matrix

**Files:** `internal/db/migrations/0021_capabilities.sql` (new)

Follows `0015_tag_taxonomy_quorum.sql`'s style: a comment stating *why* each
column exists, especially the `unknown` distinction and why `license` style
`UNKNOWN` is not used here.

Then:

```bash
go test ./internal/db/... && echo OK
# expect: OK — migrate_test's per-version table must be extended, see below
```

**Trap to avoid:** `internal/db/migrate_test.go` enumerates expected versions.
Adding `0021` without updating that list makes a test fail for a reason that is
not the bug. Find it with:

```bash
grep -n "0020\|wantVersions\|expectedVersions\|len(versions)" internal/db/migrate_test.go
```

**Verification:** applied to a **copy** of the live DB, never the live one first:

```bash
ssh thinkcentre 'cp ~/.local/share/concord/concord.db /tmp/cap-test.db'
ssh thinkcentre 'sqlite3 /tmp/cap-test.db "select count(*) from sqlite_master where type=(char(116,97,98,108,101)) and name=(char(105,110,100,101,120));"'
# record the number BEFORE migrating
# run the migration against the copy, then re-run the same query
# expect: same number. A lost index here is the defect that bit the visibility work.
```

---

## Step 2 — `internal/store/capabilities.go`

**Files:** `internal/store/capabilities.go` (new), `internal/store/capabilities_test.go` (new)

Public surface (all **(new)**):

```go
type Capability struct {
    Key      string
    Label    string
    Category string
    Kind     string    // "boolean" | "enum"
    Values   []string
}

type CapabilityAssertion struct {
    ID         int64
    Capability string
    ProjectID  int64
    Value      string    // yes | partial | no | unknown
    Evidence   string
    AssertedBy int64
    AssertedAt time.Time
    Confirms   int      // independent confirmations
    Disputes   int      // independent disputes
    State      string   // "asserted" | "confirmed" | "disputed"
}

func (d *DB) EnsureCapability(ctx context.Context, c Capability) error
func (d *DB) GetCapability(ctx context.Context, key string) (Capability, error)
func (d *DB) ListCapabilities(ctx context.Context, category string) ([]Capability, error)
func (d *DB) AssertCapability(ctx context.Context, capability, slug, value, evidence string, by int64) (CapabilityAssertion, error)
func (d *DB) ConfirmCapability(ctx context.Context, assertionID, userID int64, confirm bool) (CapabilityAssertion, error)
func (d *DB) ListCapabilityAssertions(ctx context.Context, projectID int64) ([]CapabilityAssertion, error)
func (d *DB) ListCapabilitiesForSet(ctx context.Context, projectIDs []int64, category string) (map[int64]map[string]string, error)
```

`ListCapabilitiesForSet` returns `project -> capability -> value` for a candidate
set and is the one call the Finder engine makes per question; it returns **only
rows that exist**, so an absent capability is distinguishable from
`value='unknown'` by the caller.

### AC tests, each of which must fail when its rule is broken

| test | asserts |
|---|---|
| `TestTwoIndependentConfirmationsPromoteAnAssertion` | 1 confirm → `asserted`; 2 → `confirmed` |
| `TestTheAuthorCannotConfirmTheirOwnAssertion` | author's confirm does not count; state stays `asserted` |
| `TestTwoDisputesMarkAClaimDisputedAndNeverNo` | state `disputed`; the value is NOT rewritten to `no` |
| `TestAValueOutsideTheDeclaredValuesArrayIsRejected` | `AssertCapability(...,"maybe")` → `ErrInvalid` |
| `TestAssertingTheSameCapabilityTwiceUpdatesRatherThanDuplicates` | second assert → 1 row |
| `TestCapabilityAssertionsAreScopedToTheProject` | project A's assertion is absent from project B |

Use `setupWithProject(t)` (`internal/store/store_test.go:59`) and
`newUserNamed(t, store, name)` for distinct voters — the author cannot confirm.

**Verification:**

```bash
go test ./internal/store/ -run 'Capabilit|Assert' -v 2>&1 | tail -30
# expect: ok  and each of the six named tests present
```

---

## Step 3 — Migration 22 + `internal/store/field_reports.go`

**Files:** `internal/db/migrations/0022_field_reports.sql` (new),
`internal/store/field_reports.go` (new), `internal/store/field_reports_test.go` (new)

```go
type FieldReport struct {
    ID            int64
    ProjectID     int64
    UserID        int64
    Version       string
    UseCase       string
    Environment   string
    Scale         string
    Duration      string
    Outcome       string  // worked | worked-with-caveats | abandoned | migrated-away
    MigratedTo    string
    Caveats       string
    Workaround    string
    Advice        string
    Removed       bool
    RemovalReason string
    CreatedAt     time.Time
}

func (d *DB) CreateFieldReport(ctx context.Context, r FieldReport) (FieldReport, error)
func (d *DB) ListFieldReports(ctx context.Context, projectID int64, includeRemoved bool) ([]FieldReport, error)
func (d *DB) RespondToFieldReport(ctx context.Context, reportID, userID int64, body string) (FieldOwnerResponse, error)
func (d *DB) RemoveFieldReport(ctx context.Context, reportID, by int64, reason string) error
func (d *DB) FieldReportOutcomeRate(ctx context.Context, projectID int64) (float64, int, error)
func (d *DB) FieldReportOutcomeRateByEnvironment(ctx context.Context, projectID int64) (map[string]float64, error)
```

`RespondToFieldReport` sets `is_owner` from `GetHighestRole(ctx, userID)` — never
from the request body. An owner is a `member` of the project, so the check is
`role != "guest"` on `GetHighestRole` for that project; use the existing
per-project role helper, not `GetHighestRole`, because a maintainer of another
project is not the owner of this one.

`FieldReportOutcomeRate` returns `(rate, sampleSize, error)`. The sample size is
returned so a caller cannot present a 100% rate from one report as if it were
established — §4.7's "83% of reporters" is meaningless without the denominator.

`CaveatCredit` is a named constant `0.5`, declared with its reason at the top of
the file. An unnamed 0.5 is a number nobody can later argue with.

### AC tests

| test | asserts |
|---|---|
| `TestMigratedAwayWithoutASuccessorIsRefused` | `ErrInvalid`, message names `migrated_to` |
| `TestAnOwnerResponseIsMarkedAsOwners` | member writes → `is_owner=1` |
| `TestTheProjectOwnerCannotRemoveAReport` | owner's `RemoveFieldReport` → `ErrPerm` |
| `TestARemovedReportIsHiddenNotDeleted` | row still exists with `removed=1` |
| `TestOutcomeRateIsWeightedByReporterReputation` | one high-rep + three low-rep → the three outweigh the one |
| `TestOutcomeRateReturnsItsSampleSize` | rate 1.0 with n=1 is distinguishable from n=11 |

**Verification:**

```bash
go test ./internal/store/ -run 'FieldReport' -v 2>&1 | tail -30
```

---

## Step 4 — Seed a real capability set

**Files:** `seed/capabilities.json` (new), `seed/seed.go` (check the existing
seed shape first), `internal/store/capabilities_seed_test.go` (new)

A capability set that does not exist in the catalog is a capability set with zero
gain. Seed the dimensions the Finder mockup actually names, so the engine has a
genuine choice of questions:

`wip-limits`, `offline`, `self-hosted`, `plugins`, `dark-mode`,
`keyboard-shortcuts`, `mobile-app`, `sync`, `encryption-at-rest`, `api`,
`export`, `mobile-friendly`.

```bash
go run ./cmd/... --help 2>/dev/null | head -20
ls seed/
# find the existing seed entry point and follow its pattern
```

**Verification:** after seeding, the catalog endpoint must report non-zero gain:

```bash
go test ./internal/store/ -run 'Seed' -v
# expect: a test asserting at least 3 categories and at least one capability
# asserted on >= 2 projects
```

---

## Step 5 — `internal/finder/` — the engine

**Files:** `internal/finder/finder.go` (new), `internal/finder/gain.go` (new),
`internal/finder/score.go` (new), `internal/finder/finder_test.go` (new)

This package takes **no** store pointer. It receives candidate attribute maps and
returns questions and rankings, which makes every property in §5.1 and §5.2
testable with plain structs and no database.

```go
package finder

type AttributeSource interface {
    Dimensions() []Dimension
    Candidates(ids []int64) []Candidate
}

type Dimension struct {
    Key      string    // "language" | "governance" | "cap:wip-limits"
    Family   string    // capability | platform | governance | license | maturity
    Label    string
    Options  []Option
    Kind     string    // "single" | "tri-state"
}

type Option struct {
    ID    string
    Label string
    // Impact is the number of candidates this option would remove.
    Impact int
}

type Candidate struct {
    ProjectID int64
    Slug      string
    Name      string
    Attrs     map[string]string  // dimension key -> value
    Report    float64            // 0..1 field-report outcome rate
    Reports   int                // sample size, 0 when there are none
    ArenaR    float64            // 0 when no arena entries exist
}

type Answer struct {
    DimensionKey string
    OptionID     string
    Mode         string  // "required" | "skip" | "doesnt-matter" | "decide-later"
}

const (
    MinGainBits      = 0.35
    MaxQuestions     = 10
    SaturationCount  = 5
    HighConfidence   = 0.85
    ConfidenceMargin = 1.15
    UnknownPenalty   = 0.05
)

// Gain returns the entropy of a value distribution in bits.
func Gain(counts []int) float64

// NextQuestion picks the highest-gain unasked dimension above MinGainBits.
func NextQuestion(cands []Candidate, asked map[string]bool, depth int) (*Dimension, float64, error)

// Score ranks candidates. Unknown never removes a candidate.
func Score(cands []Candidate, answers []Answer) []Ranked

// Gap describes a dimension that would have discriminated but could not.
type Gap struct {
    Key            string
    Label          string
    HeldBack       int
    UnknownFields  int
}

func Gaps(cands []Candidate, asked map[string]bool) []Gap
```

**The unknown rule, stated as code.** A `TriState` "required" answer removes a
candidate whose value is `no`. It **keeps** one whose value is absent or
`unknown`, and applies `UnknownPenalty`. The test that matters:

```go
func TestUnknownNeverRemovesACandidate(t *testing.T)
// candidate A: cap:offline = "no"     -> removed
// candidate B: cap:offline = "unknown" -> kept, ranked below A, penalty applied
// candidate C: cap:offline absent      -> kept, ranked below B
```

### AC tests for the engine

| test | asserts |
|---|---|
| `TestADimensionWithOneDistinctValueIsNeverAsked` | a constant dimension scores 0.00 bits and is not offered |
| `TestTheHighestGainQuestionIsChosen` | a 50/50 split beats a 90/10 split |
| `TestAnAlreadyAskedQuestionIsNeverRepeated` | re-running after an answer cannot return it |
| `TestSkipAndDoesntMatterNeverRemoveCandidates` | both modes leave the set identical |
| `TestDecideLaterAffectsRankButNotMembership` | membership unchanged; ordering changes |
| `TestUnknownIsRankedBelowKnownAndIsNotDropped` | the §2.7 rule |
| `TestStopReasons` | each of SATURATED / HIGH_CONFIDENCE / LOW_GAIN / QUESTION_CAP |
| `TestGapsNameTheDimensionsThatWouldHaveDiscriminated` | a constant-dimension catalog yields gaps, not a quiz |
| `TestAResetQuestionDoesNotFireOnAnEmptyCatalog` | no dimension, no panic, LOW_GAIN |
| `TestGainIsZeroForASingleValueAndMaximalForAUniformSplit` | the arithmetic itself |
| `TestArenaStandingIsComputedNotConstant` | **the honest-telemetry test.** Two candidates with different `ArenaR` rank differently. A hardcoded `0` for every candidate passes every other test in this file. |

`TestArenaStandingIsComputedNotConstant` exists because `arena_entries` has 0
rows today, so the live data cannot distinguish "computed and zero" from
"hardcoded zero". A test that passes for two different reasons is passing for one
of them by accident — and this one would pass for the wrong reason forever.

**Verification:**

```bash
go test ./internal/finder/ -v 2>&1 | tail -40
# expect: ok, all eleven named tests present
```

---

## Step 6 — REST API

**Files:** `internal/httpapi/finder.go` (new), `internal/httpapi/finder_test.go` (new), `internal/httpapi/server.go` (edit — register routes and add `"finder"` to the `pages` slice)

Routes (finder-spec §6). Session state in memory behind a `sync.RWMutex` on the
`Server`, so two concurrent sessions cannot interleave. `internal/httpapi` is
already known to have a 100 req/min process-global rate limit, so a Finder
session's ten answers cost ten of the budget — stated, not worked around.

`GET /api/v1/finder/questions/catalog` calls `finder.NextQuestion` against the
whole catalog and returns every dimension with its **live gain**, so "Finder
never asks about an option the catalog can't deliver" is one curl away from
being checked.

**AC tests** — use `newTestServer(t)` (`httpapi_test.go:15`) and
`newTestServerWithStore(t)` (`:114`) for DB-backed ones:

| test | asserts |
|---|---|
| `TestFinderSessionLifecycle` | start → answer → next question → results |
| `TestAnsweringNarrowsTheCandidateSet` | count strictly decreases |
| `TestSkippingNeverRemovesACandidates` | count identical before and after |
| `TestBackRecomputesRatherThanReplaying` | changing answer 1 re-derives question 2 |
| `TestLiftFilterRestoresCandidates` | lifted candidate returns to the short-list |
| `TestTheQuestionCatalogReportsNoZeroGainDimensionAsAskable` | the API-level half of the §5.4 promise |
| `TestFinderSurvivesAnEmptyCatalog` | no candidates → 200 + LOW_GAIN, never a panic |

Use `newTestServerNoActor` (`:145`) where the endpoint must work anonymously.

**Verification:**

```bash
go test ./internal/httpapi/ -run 'Finder' -v 2>&1 | tail -30
```

---

## Step 7 — The page

**Files:** `internal/httpapi/templates/finder.html` (new),
`internal/httpapi/assets/js/finder.js` (new), `internal/httpapi/assets/css/style.css` (edit — append only, never reorder existing rules)

`finder.html` is a copy of `ranking.html`'s shape: mount point, skeleton, script
tag, registered in `loadTemplates`' `pages` slice. `finder.js` fetches session
state from `/api/v1/finder/...` and renders both panes.

Copy the existing conventions exactly: `session.js` for the Bearer token,
`skeleton.js` for loading state, `{{ asset "/assets/js/finder.js" }}` for
versioning. **No build step, no framework, no node_modules.**

**Verification — the loading contract the frontend spec §0.4 requires is tested
by running the code:**

```bash
go test ./internal/httpapi/ -run 'Page|Asset' -v 2>&1 | tail -20
# then the manual gate, which no unit test can do:
go build -o /tmp/conc ./cmd/concord
CONCORD_DB=/tmp/finder-test.db CONCORD_LISTEN=127.0.0.1:8099 /tmp/conc &
sleep 3
curl -s http://127.0.0.1:8099/finder | grep -c 'finder-root'
# expect: 1
curl -s http://127.0.0.1:8099/api/v1/healthz | grep '"ok"'
# expect: ok
```

Then open `/finder` in a browser and drive it: answer three questions, go back,
lift a filter, stop early. Automated tests cannot prove the wiring works.

---

## Step 8 — Docs sync

**Files:** `docs/PLAN-r4.md` (edit), `docs/HANDOFF.md` (edit), `docs/specs/finder-spec.md` (edit if reality differed)

PLAN-r4: R5 → shipped, add an **R8 Finder** row. HANDOFF: the "Not started: R5
field reports / capabilities" line becomes "shipped in <commit>".

**The rule:** if any step's reality differed from this plan, fix the plan in the
same commit as the code fix and say in the commit message what the plan got
wrong and how it was caught.

---

## Step 9 — Deploy

```bash
cd ~/code/projects/concord
make verify                     # must be green before anything else
git add -A && git commit -m "..."
git push forgejo HEAD:main
git push github HEAD:master
```

Deployment happens on **thinkcentre**, which runs the real instance
(`v0.4.0-voting-49-g13d6bbb` = HEAD). This host has a stale copy at :8006
(`v0.4.0-voting-27`) — do not deploy here. See step 9b.

```bash
ssh thinkcentre 'cd ~/.../concord && git pull && make deploy'
# expect: "✓ healthz OK" then "Deployed successfully."
```

### 9b — The migrate-before-restart ordering

`make deploy` does not run migrations. Migrations run at **startup** (`cmd/concord`
calls `db.Migrate`), so a binary with a new migration applied to the live DB on
restart is correct — but only if the migration is backward-compatible with the
running binary during the restart window. New tables and new columns with
defaults are. A migration that drops or rewrites a column is not, and this plan
contains none.

**Verify the live instance after deploy, against the real DB, not the service:**

```bash
ssh thinkcentre 'sqlite3 ~/.local/share/concord/concord.db "select count(*) from schema_migrations;"'
# expect: 22
ssh thinkcentre 'sqlite3 ~/.local/share/concord/concord.db "select count(*) from capabilities;"'
# expect: > 0 after seeding
ssh thinkcentre 'curl -s http://127.0.0.1:8006/api/v1/finder/questions/catalog | head -c 400'
# expect: real dimensions with real gain values, NOT {"error":"not found"}
```

The last one is the one that matters. A deploy that reports success while the
route answers `not found` is a deploy that proved nothing, and `make deploy`'s
healthz check cannot see it — healthz is one route, and the new one is another.

## Completion gate

```bash
make verify
go test ./internal/finder/ ./internal/store/ ./internal/httpapi/ -count=1
# then the three live checks in 9b, then the browser session in step 7.
```

A green suite is necessary, not sufficient: the browser drive and the live curl
are the parts that prove the feature is reachable, and neither is replaceable by
a unit test.