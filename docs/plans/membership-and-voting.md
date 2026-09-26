# Plan — membership and voting

Implements `specs/membership-and-voting.md`. Work in this order; each step's
verification must print the exact expected line before the next step starts.

Run everything from `/home/alvaro/code/projects/concord`.

**Before starting:** `git log --oneline | head -1` should be `bf1ad77` and
`git status --short` should be empty. The tree is clean, so every step below is
individually revertible with `git checkout -- .`.

---

## Step 1 — `JoinProject` and the integrity helpers

Add to `internal/store/membership.go` (new file).

```go
// JoinProject enrols a user as a contributor in a project.
//
// It is deliberately not "SetRole": an existing membership is never modified.
// Enrolment is a side effect of taking part, so re-running it must be a no-op
// and must never demote a maintainer who files a complaint. ON CONFLICT DO
// NOTHING is load-bearing here, not a shortcut.
func (d *DB) JoinProject(ctx context.Context, projectID, userID int64) error {
	_, err := d.ExecContext(ctx, `
		INSERT OR IGNORE INTO members (project_id, user_id, role, joined_at)
		VALUES (?, ?, 'contributor', ?)`,
		projectID, userID, float64(time.Now().Unix()))
	if err != nil {
		return fmt.Errorf("join project: %w", err)
	}
	return nil
}
```

Also in that file:

```go
// IsFeatureAuthor reports whether userID proposed featureID.
//
// Voting on your own feature is refused because Glicko-2 has no way to
// discount it: a rating inflated by its author's own vote is not a measurement
// of anyone else's preference, and the inflation is unbounded.
func (d *DB) IsFeatureAuthor(ctx context.Context, featureID, userID int64) (bool, error) {
	var authorID int64
	err := d.QueryRowContext(ctx,
		`SELECT author_id FROM features WHERE id = ?`, featureID).Scan(&authorID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("feature %d: %w", featureID, ErrNotFound)
	}
	if err != nil {
		return false, fmt.Errorf("load feature author: %w", err)
	}
	return authorID == userID, nil
}
```

`ListMembers` and `FeatureVoteCounts` follow the same shape; both must drain
their rows with `rows.Close()` before returning, and must not issue a query
inside the loop — see the 1-connection deadlock in the spec.

**Verify:**
```bash
go build ./... && gofmt -l internal/store/membership.go
```
Expected: no output at all. Any file listed is unformatted — run `gofmt -w`.

---

## Step 2 — Make voting reachable

In `internal/httpapi/handlers.go`:

1. `handleCastVote`: after the role check, reject self-voting.
   ```go
   if isAuthor, err := s.Store.IsFeatureAuthor(r.Context(), featureA, actorID); err == nil && isAuthor {
       // Also check feature B, and only then refuse.
   }
   ```
   Both features in the pair must be checked, and the 403 message must say
   *why* — "you proposed one of these" — because a bare "permission denied" for
   a legitimate action is indistinguishable from a bug to the person hitting it.

2. On success, and **only** on success, call `JoinProject`. Enrolling someone
   who was refused a vote would be a lie about what they did.

3. `handleGetNextPair`: apply the same role check `handleCastVote` uses. The
   API must not serve a comparison to an account that will be refused when it
   answers.

4. `handleFeaturePriorities`: remove the `requireRole` call. Public output.

5. `handleCreateComplaint` and `handleCreateFeature`: `JoinProject` after a
   successful insert, same as voting.

Add the route in `server.go`:
```go
r.Post("/api/v1/projects/{project_id}/join", s.handleJoinProject)
```

**Verify:**
```bash
go build ./... && go test ./internal/httpapi/ -timeout 120s
```
Expected: `ok ... internal/httpapi` and **no** existing test newly failing. If a
test asserted that a guest is refused, it must now assert the specific reason.

---

## Step 3 — Tests first-class

New file `internal/httpapi/membership_test.go`. Each test names the defect:

| Test | Asserts |
|---|---|
| `TestRegisteredUserCanVoteInProjectTheyDidNotCreate` | the 403 above is gone |
| `TestFilingComplaintEnrolsAuthor` | role becomes `contributor` |
| `TestJoinProjectDoesNotDemoteMaintainer` | creator stays `maintainer` |
| `TestAuthorCannotVoteOnOwnFeature` | 403, with a reason |
| `TestPrioritiesArePubliclyReadable` | 200 with no token |
| `TestNextPairRefusesGuest` | the invite/reject inconsistency is gone |
| `TestJoinIsIdempotent` | two joins, one row |

Every one must be proven by mutation. The pattern, applied to each:

```bash
# 1. run it, see it pass
go test ./internal/httpapi/ -run TestRegisteredUserCanVote -v
# 2. revert the fix by hand, run again, see it FAIL with the defect named
# 3. restore
git checkout -- internal/httpapi/handlers.go
```

A test that has not been seen to fail against the bug is not evidence.

**Verify:**
```bash
go test ./internal/httpapi/ -timeout 120s -v 2>&1 | grep -c '^--- PASS'
```

---

## Step 4 — The rank page

New `internal/httpapi/templates/rank.html` and `assets/js/rank.js`, route
`/projects/{slug}/rank`.

The page fetches `/api/v1/projects/{slug}/votes/next`, renders the two
descriptions in full, and offers five buttons. Outcome strings must match the
store exactly: `a`, `b`, `both`, `neither`, `skip`. Check before writing.

`skip` is a real answer and must be presented as one — "I have no preference" is
information about a pair, and the rating maths already accounts for it.

After each vote, re-fetch. When the endpoint returns no pair, show the ranking
and a link onward rather than a dead end.

**Verify:**
```bash
go test ./internal/httpapi/ -run TestRankPage -v
```
Test that the page references `rank.js`, that all five outcome strings are
present in the script, and that the slug is used in the fetch path — that last
one is the bug from `bf1ad77`, and a numeric id there 404s and renders as a
valid empty state.

---

## Step 5 — The ranking page

`internal/httpapi/templates/ranking.html` + `assets/js/ranking.js`, route
`/projects/{slug}/ranking`. Fetches `/api/v1/projects/{slug}/priorities`.

Shows, per feature: title, Glicko rating, **deviation**, pain score, strategic
weight, and vote counts.

Deviation is not optional detail. A feature at 1500 ± 350 has barely been
compared to anything, and printing the bare rating presents noise as a
measurement. A high-deviation feature is labelled under-tested.

Sort by the priority order the API returns. Do not re-sort client-side by rating
— the API's order accounts for pain and strategic weight, and re-sorting by
rating would silently discard both.

**Verify:**
```bash
go test ./internal/httpapi/ -run TestRankingPage -v
```

---

## Step 6 — Complaint and feature forms

On `project.html`, add a disclosure-based form for filing a complaint and one
for proposing a feature. Both post through the existing endpoints and then
`location.reload()`, because the page is server-rendered and the forms change
state the server owns.

Do not build these as client-rendered inserts: a complaint that appears without
a validation round-trip will be shown as filed when the server rejected it.

**Verify:**
```bash
go test ./internal/httpapi/ -run TestProjectPageHasForms -v
```

---

## Step 7 — Full suite, deploy, and prove it in a browser

```bash
gofmt -l internal/ ; go vet ./... ; timeout 400 go test ./... -timeout 300s
```
Expected: `gofmt -l` empty, `go vet` silent, every package `ok`, and **no
package slower than a few seconds**. A package that suddenly takes minutes is
the 1-connection deadlock.

```bash
make deploy
```
Expected: `healthz OK`.

Then, in a browser, as a **freshly registered account that does not own any
project**:

1. `/register` → sign up
2. `/projects/concord` → the page shows a complaint count above zero
3. file a complaint → the project page now shows your complaint
4. `/projects/concord/rank` → a pair of features is offered
5. vote → the next pair appears
6. `/projects/concord/ranking` → your vote is reflected

Step 4 is the one that proves this work did anything: before it, the endpoint
that serves a pair existed and the endpoint that records the answer returned 403
for everyone who had not created the project.

---

## Step 8 — Commit, tag, document

```bash
git add -A
git commit -F /tmp/voting-msg.txt
git tag -a v0.4.0-voting -m "Membership and a voting interface that exists"
```

Update `docs/specs/membership-and-voting.md` with a Result section stating what
was verified live and what remains. Update the vault log at
`~/secondbrain/90-Meta/2026-09-26-concord-membership-and-voting.md`.

Do not tag until step 7 has been done by hand in a browser. A tag on a
suite-only verification is a claim the tag does not support.
