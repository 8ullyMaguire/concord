# Every API route has a reader, or a reason

Status: **implemented** (`e2ef524`+1). Supersedes the hand-built table in
`docs/REMAINING.md` §12.1, which was a heuristic search for a route's static tail and
was wrong within two passes.

## The rule

`internal/httpapi/reader_coverage_test.go` walks the router the binary actually serves
(`chi.Walk` over `srv.Router()`), then checks a declared inventory in both directions:

| assertion | what it prevents |
|---|---|
| every API route is matched by a row of the inventory | a new endpoint with no reader, which is how the merge gate (§16), the criteria table (§15), the settings panels (§13) and this pass's four were each found |
| every row matches at least one registered route | a row left behind by a deleted route, asserting a reader for something that is not served |
| every row's `Routes` count equals the routes it matches | a route added UNDER an existing row's fragment, which would otherwise be silently covered |
| every row with a `Reader` names a file that exists and contains the row's `Anchor` | a claim of a reader that does not reference the endpoint |
| a row with no `Reader` must carry a `Note` | an exemption that is not stated |

The `Routes` count is the load-bearing part. A row that matched
`/api/v1/projects/{project_id}/features` and every future sub-route would be a gate
that can never fire for the next feature, so each row pins the number of routes it
covers and the count going stale is the signal. The failure message prints the
corrected row and the routes it now matches, so bumping it is a two-line change that
forces the author to read the row.

## What it proves, and what it does not

It proves a client file **references** the endpoint's path. That is a floor:

- it cannot tell a real reader from a file that mentions a path in a comment, and it
  does not try — the anchor is the endpoint's own spelling precisely because a file
  that spells it is doing something with it;
- it cannot prove every VERB is reachable. `POST /strategy/weight-proposals` had a
  reader by this measure — `settings.js` consents to proposals — while the propose
  form (§12.6.3) was missing for the whole life of the panel. The gate named the
  endpoint as covered and was right about what it measures;
- it says nothing about whether the control does the right thing. That is the browser
  suites' half, and for this pass `tests/e2e/readers_e2e.py`.

## The four surfaces it named

| endpoint | what was missing | now |
|---|---|---|
| `POST /projects/{id}/join` | no way to join a project from the browser: "participate" was a route with no door | a Join button on the project page, offered only when a session exists |
| `PUT /consensus/objections/{id}/resolve` | §6.6's resolution, unreachable | per-objection control on the consensus page: the four statuses the schema permits, a required reason, the server's refusal when the viewer is not a maintainer |
| `POST /strategy/weight-proposals` | §12.6.3: the panel could list and consent, never open one | a propose form with a feature picker and a rationale |
| `POST /solutions/{id}/coverage/{cid}/contest` | §6.4's challenge, unreachable because no read exposed which claims exist | the claims ride with the solutions payload, and the feature page renders each with a Contest control |

## Bugs found in the same pass, all one shape

1. **A junk resolution status was a 500 with the schema in the message.**
   `UPDATE objections SET status=?` passed the caller's string straight through, so the
   table's CHECK was what refused `"banana"`, and a constraint failure arrives as
   `500 constraint failed: CHECK constraint failed: status IN (...)`. The rule now lives
   in the store, beside the write, and answers 400 naming the four statuses.
2. **The response reported a hardcoded `"resolved"`.** Whatever status was written —
   `vetoed`, `overridden` — the 200 said `{"status":"resolved"}`. A page rendering the
   answer showed a resolution nobody made.
3. **`handleProposeStrategicWeight` had no contributor gate**, while its own doc comment
   said it "follows the taxonomy proposal's shape deliberately: contributor gate". Any
   signed-in account on the instance could open a weight proposal on any project,
   including one it was not a member of.

## Verification

- `internal/httpapi/objection_resolution_test.go` (6 tests): the status whitelist, all
  four permitted statuses, `open` refused as an outcome, a blank reason refused, a
  non-maintainer refused, the response echoing what was written.
- `internal/httpapi/weight_proposal_gate_test.go` (2 tests): outsider refused, `user`
  refused, contributor accepted; the proposal does not move the weight.
- `internal/httpapi/coverage_claims_read_test.go`: the solutions payload carries the
  claims, and a contested one says so.
- `tests/e2e/readers_e2e.py` (8 tests): each control appears where a reader looks for
  it, does the thing, and reports the server's refusal to a reader who may not do it.
- Eight mutations, each verified to turn a named test red (`M1`–`M3` the gate itself;
  `M4`–`M6` the resolution rules; `M7` the claims field; `M8` the contributor gate).
