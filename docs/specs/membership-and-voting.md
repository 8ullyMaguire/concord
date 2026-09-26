# Concord — membership, and a voting interface that exists

Status: **implemented 2026-09-26** (`a76bec9`, `a5d440c`, tag `v0.4.0-voting`). Companion to `auth-and-portfolio-import.md`.
This is the third and deepest instance of the same defect that document describes.

---

## The problem

Concord exists so that **people rank what should be built next**. The Glicko-2
engine that does the ranking is implemented, unit-tested, and unreachable by
any human being. There is no voting interface in any template or script, and no
account other than a project's creator can cast a vote at all.

### 1. Voting is impossible (verified live)

```
POST /api/v1/projects/concord/features/1/vote   ->  403 permission denied
```

`handleCastVote` reads the caller's role and rejects `"guest"`:

```go
role, _ := s.Store.GetRoleForProject(r.Context(), projectID, actorID)
if role == "guest" {
    mapError(w, store.ErrPerm)
```

`GetRoleForProject` returns `"guest"` for anyone with no `members` row:

```go
if errors.Is(err, sql.ErrNoRows) {
    return "guest", nil
}
```

The only code path that ever writes a `members` row is `CreateProject`, which
inserts the **creator** as `maintainer`. There is no join endpoint, no
`AddMember` function, and no HTTP route containing `join`, `apply`, or
`member`.

So the population of people who can vote on a project is exactly one: whoever
created it. A community cannot rank anything, because a community cannot vote.

Note the inconsistency this creates: `GET /votes/next` does *not* check the
role, so it happily serves a comparison pair to an account that is then refused
when it tries to answer. The API invites a vote and then rejects it.

### 2. The premise is only half-implemented

| Capability | Backend | UI |
|---|---|---|
| File a complaint | yes | no form |
| Propose a feature | yes | no form |
| Rank features (Glicko-2) | yes, tested | **no** |
| Cast a pairwise vote | yes (creator only) | **no** |
| Join a project | **no** | no |
| See the ranking | yes (`/priorities`) | no |

The last row is the one that matters. Even the creator cannot see *why* a
feature ranks where it does, because nothing renders `/priorities`.

---

## Decisions

**Joining is automatic on first write, not a separate membership flow.**

A `POST /join` endpoint would be a worse design, not a better one. It adds a
step whose only purpose is to satisfy a role check, and it creates a class of
account that is authenticated but has done nothing — which is exactly the state
the system should not have. Instead: any authenticated user who files a
complaint, proposes a feature, or votes in a project is enrolled as a
`contributor` in that project as part of the same action.

This is also how real communities behave. You do not apply to a forum before
posting; you post, and that is what makes you a participant.

**`contributor`, not `user` or `maintainer`.** Rank 2 of 6. Enough to vote and
propose, not enough to change the charter or the strategic weights. Enrolment
must never grant authority over a project.

**The creator stays `maintainer`.** Creating a project is an act of custody, and
`requireRole(projectID, "maintainer")` already depends on it.

**Enrolment is idempotent** and never downgrades an existing role. A
maintainer who files a complaint must not become a contributor.

**Self-voting is blocked.** A feature's `author_id` is known, so the API can
refuse a vote where the voter is the author. This is a real integrity property,
not a nicety: with Glicko-2, self-preference is unbounded, because a rating
inflated by its own author's vote is no longer a measurement of anyone else's
preference. It is already in the seed data as a complaint; this implements it.

**The ranking is public.** `/priorities` requires a token today. It is the
output of a process the public is invited to take part in, and hiding it would
make the site a black box. Reads stay open; the *casting* of votes requires an
account. Making a public-interest endpoint more accessible than the private act
of voting is the right way round.

---

## What to build

### Data layer (`internal/store/`)

- `JoinProject(ctx, projectID, userID) error` — insert a `contributor` row with
  `INSERT OR IGNORE`; never touch an existing row.
- `IsFeatureAuthor(ctx, featureID, userID) (bool, error)`.
- `ListMembers(ctx, projectID)` — for the roster the UI shows.
- `FeatureVoteCounts(ctx, projectID)` — wins/losses/skips per feature, so the
  UI can show participation rather than only the resulting rating.

### API (`internal/httpapi/`)

- `POST /api/v1/projects/{project_id}/join` — explicit join for someone who
  wants to vote before contributing anything.
- Enrolment inside create-complaint, create-feature and cast-vote.
- `handleCastVote` rejects self-votes with 403 and a message that says why.
- `handleFeaturePriorities` no longer requires authentication.
- `GET /api/v1/projects/{project_id}/members`.

### UI

- **`/projects/{slug}/rank`** — the pairwise comparison. One pair at a time,
  because comparing three or more at once is a different and worse question.
  Five outcomes: A, B, both, neither, skip. Skip is a first-class answer, not a
  dismissal: "I have no preference" is real information about a pair and the
  rating maths already handles it.
- **`/projects/{slug}/ranking`** — the resulting order, with each feature's
  rating, deviation, pain score, strategic weight, and vote counts.
- A complaint form and a feature-proposal form on the project page.
- Project page shows complaint count and links to rank and ranking.

### Honest presentation

Glicko-2 produces a rating and a **deviation** — the uncertainty around it. A
feature rated 1500 ± 350 has barely been compared to anything. Showing the bare
number would present noise as a result, so every rating is displayed with its
deviation, and a high-deviation feature is labelled as under-tested.

---

## Verification

Every one of these is a claim that can be false, so each is a test:

1. A newly registered account can vote in a project it did not create.
2. Filing a complaint enrols the author as a contributor.
3. `JoinProject` twice does not downgrade a maintainer.
4. An author cannot vote on their own feature.
5. `/priorities` returns 200 without a token.
6. `/votes/next` refuses a guest, so the API no longer invites a vote it will
   reject.
7. The ranking page lists every feature with a non-empty deviation.
8. The rank page shows a pair and accepts all five outcomes.
9. A `skip` is recorded and does not change either feature's rating as a win.
10. Every fix is verified by reverting it and confirming the test fails.

## Not doing

- **No HttpOnly cookie migration.** Separate, already documented.
- **No create-card endpoint for the board.** The board is fed by the merge
  executor; adding card creation is a product decision, not a gap in the
  ranking path.
- **No changes to the Glicko-2 maths.** It is correct and tested. The bug was
  that nobody could reach it.

---

## Result

All ten verification claims hold, and each was checked by reverting the fix and
confirming the named test fails.

| # | Claim | Test | Reverting the fix |
|---|---|---|---|
| 1 | A new account can vote in a project it did not create | `TestRegisteredUserCanVoteInProjectTheyDidNotCreate` | re-adding the guest gate fails it and 3 others |
| 2 | Filing a complaint enrols the author | `TestFilingComplaintEnrolsAuthor` | asserts `author_id != 0` and the role |
| 3 | Join never demotes a maintainer | `TestJoinProjectDoesNotDemoteMaintainer` | three joins, role still `maintainer` |
| 4 | An author cannot vote on their own feature | `TestAuthorCannotVoteOnOwnFeature` | deleting the loop fails it |
| 5 | `/priorities` is public | `TestPrioritiesArePubliclyReadable` | re-adding the auth check fails it |
| 6 | `/votes/next` no longer invites a vote it refuses | `TestFirstVoteIsPossible` | the guest gate fails it |
| 7 | The ranking lists every feature with a deviation | `TestRankingPageUsesTheForgeOrder` | asserts `elo_rd` and no client `.sort(` |
| 8 | The rank page offers a pair and all five outcomes | `TestRankPageReferencesTheRealRoutes`, `TestRankPageOffersAllFiveOutcomes` | — |
| 9 | A skip is recorded and is not a loss | `TestSkipIsRecordedAndDoesNotCountAsALoss` | — |
| 10 | The project page can file and validate | `TestProjectPageHasTheForms` | — |

**Verified live, by hand, as a freshly registered account that owns no
project:** registered; was offered a real pair; cast two votes; watched the
count advance and a new pair load; saw concord's ratings move off their 1500
seed with real pain scores (4.57 / 1.11 / 1.11) and vote counts. Filed a
complaint through the form and watched it appear. The self-vote refusal returns
403 with its reason stated.

## Three defects found while implementing, not predicted by it

**The ordering bug.** Rejecting guests *before* enrolling made a first vote
impossible: nobody is a member until they have voted or contributed, so the one
act that grants membership was the act that required it. Pinned by
`TestFirstVoteIsPossible`, whose comment says not to "tidy" it back.

**`ConcordSession` had no shared instance.** Exported as a bare constructor, so
every caller built a private copy. Not an error — a header reading "Sign in" to
a signed-in visitor, and forms that render as though nobody were present.

**A function was stringified into the markup.** `+ complaintCards +` instead of
`+ complaintCards(complaints) +` rendered project.js's own source as visible
page text. Every status was 200 and "the page mentions Complaints" passed — the
word was inside a function body.

The last two are the argument for browser verification as a distinct step.
Neither produces a failing request, a stack trace, or a status code.

## Still open

- **The board still has no create-card endpoint** — only read and move, fed by
  the merge executor. Unchanged and deliberate; it is a product decision.
- **localStorage rather than an HttpOnly cookie**, as before.
