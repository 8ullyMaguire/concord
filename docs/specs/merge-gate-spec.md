# The merge gate: §5.7's confirmation rule, wired

Status: written before the code, 2026-10-15.
Follows `docs/concord-spec-r4.md` §5.7 (Code Review and Merge Confirmation), §6.3
(Consensus and Merge Thresholds), §4 (Governance models) and §8.2 (eligible
collaborators). Consumes `governance.MergeGate`, which already existed.

## 1. What the specification asks for

§6.3: "Merge quorum: `max(min_confirmations, ceil(merge_ratio * eligible))` distinct
collaborator confirmations, plus ≥1 reviewer technical approval. Defaults:
`min_confirmations = 2`, `merge_ratio = 0.25`."

§5.7: the merger asks a narrower question than consensus did — "is this ready to
land?" — and "in maintainer-led mode the charter may lower or drop the merge quorum;
every such merge is logged."

The schema has carried the four columns since migration 0001 — `merge_requires_quorum
INTEGER NOT NULL DEFAULT 1`, `merge_quorum_min … DEFAULT 2`, `merge_quorum_ratio …
DEFAULT 0.25`, `require_reviewer_approval … DEFAULT 1` — and `internal/store/store.go`
reads and writes all four through the charter.

## 2. What the tree did

`governance.CheckMergeGate(approvals, eligible int, reviewerApproval bool, Charter)
MergeGate` implements §5.7 exactly: `MergeQuorumThreshold` clamps the rule with
`min(eligible, …)`, `QuorumOK` honours a dropped quorum, `ReviewerOK` honours the
flag, and `Passed() = QuorumOK && ReviewerOK`. `governance_test.go` pins all four
branches.

It had exactly one caller, in its own test file:

```
$ grep -rn 'CheckMergeGate' --include=*.go .
./internal/governance/governance.go:264:func CheckMergeGate(approvals, eligible int, reviewerApproval bool, c Charter) MergeGate {
./internal/governance/governance_test.go:140:	g := CheckMergeGate(1, 10, true, c)
```

Meanwhile `store.ExecuteMerge` — reached by `PUT
/api/v1/projects/{project_slug}/merge/{mr_id}/execute` — required only a non-zero
actor id. It set `status='merged'`, moved the feature to `shipped`, closed **every
complaint linked to that feature**, and wrote an audit row. `ApproveMerge` accepted
any actor and inserted a row. Nothing counted confirmations, nothing consulted the
charter, nothing checked membership, and `ExecuteMerge` used the project id from the
URL only as the audit row's project — never to check that the merge request belonged
to it.

So every project's default state, with a charter that says two confirmations are
required, was: one person who can read the project presses a button and the change
lands.

### Reproduced, not inferred

`internal/httpapi/merge_gate_test.go` was written first and run against the
pre-fix tree. Six of its eight tests failed, and the failures are the defects:

```
--- FAIL: TestAMergeWithNoApprovalsIsRefused
    a merge with zero confirmations was executed: 200 map[status:merged]
--- FAIL: TestAMergeIsRefusedToSomebodyOutsideTheProject
    an account with no membership in the project executed a merge: map[status:merged]
--- FAIL: TestAnAuthorCannotConfirmTheirOwnMergeRequest
    the author confirmed their own merge request: map[status:approved]
--- FAIL: TestAMergeRequestCannotBeExecutedThroughAnotherProject
    a merge request of project merge-project was executed through project
    some-other-project: map[status:merged]
--- FAIL: TestTheMergeListReportsTheGateAndItsShortfall
    the list item carries no gate: map[author_id:2 feature_id:1 id:1 … status:open …]
--- FAIL: TestAReviewerApprovalIsNotJustAnyApproval
    two contributor confirmations and no reviewer passed a charter that requires
    reviewer approval (reviewer): map[status:merged]
```

The two that passed are the ones that must keep passing: the charter's dropped
quorum, and the happy path (a rule that refuses everything is as wrong as one that
refuses nothing).

This is the twin of the bug `consensus_quorum_test.go` records: there,
`ConsensusCounts.Eligible` was never set, so `QuorumThreshold` returned 0 and the
insufficient-quorum branch could never fire. Same family of defect — the rule was
written and unit-tested in isolation and never reached from the write path.

## 3. The gate, as implemented

### Definitions

- **A counted confirmation** is a `merge_approvals` row whose user (a) is not the
  merge request's author, (b) is an eligible collaborator of the project *now*, and
  (c) appears once. (a) is §6.3's "distinct collaborator confirmations" — people
  confirming each other's work; the eligibility engine already says the same thing in
  route (4) of `qualifyingContributionsSQL`, "a merge_approval on somebody else's
  merge request", comparing `user_id` against `author_id`. (b) is the reason a
  confirmation is not permanent: eligibility is role *and* a qualifying contribution
  inside the activity window, and a confirmation from someone who has gone dormant
  should stop holding the gate open. (c) is the schema's own `PRIMARY KEY (mr_id,
  user_id)`.
- **A reviewer approval** is a counted confirmation from a user holding ≥
  `governance.Reviewer`. Required only while the charter says so.
- **Needed** is `governance.MergeQuorumThreshold(eligible, charter) =
  min(eligible, max(merge_quorum_min, ceil(merge_quorum_ratio * eligible)))`, and 0
  when the charter dropped the quorum.
- **The gate passes** when `QuorumOK && ReviewerOK`, i.e. `governance.MergeGate.Passed()`
  — the existing function, called.

### Enforcement points

| act | rule |
|---|---|
| approve | the actor must be a project member with role ≥ contributor, and must not be the author |
| execute | role ≥ contributor, the request must belong to the project in the URL, its status must be `open`, **and** the gate must pass |
| reject | role ≥ contributor and status `open`; no quorum (refusing is the safe direction) |

The membership rules deliberately do not require *eligibility*: in maintainer-led mode
a maintainer who has not yet contributed could never land the first change, and —
decisively — approving somebody else's request **is** a qualifying contribution
(route (4) of `qualifyingContributionsSQL`), so requiring eligibility before the first
approval would make that route unreachable. Membership with role ≥ contributor
excludes every outsider, and the gate — not the button-presser — is the authority.
§5.6's neighbouring rule for a manual move to Done is likewise "maintainer-only,
logged".

**Status is checked because "what happens if this runs twice?" has an answer.** A
second execute re-ran the whole landing (re-shipping the feature, re-closing
complaints already closed) and a reject after a merge flipped a landed change to
`rejected`. `open -> merged|rejected`, once, as a conflict on anything else.

### The exception is logged

When the charter dropped the quorum, §5.7's "every such merge is logged" applies:
the audit detail says so rather than reading like any other merge.

### Errors

New sentinel errors in `internal/store/merge.go`, each mapping to a status the client
can act on:

- `ErrMergeGateNotMet` → **409**, message naming what is missing
  (`"2 of 2 confirmations are required"` / `"a reviewer approval is required"`).
  A 409 rather than a 400 because the client's next move is to get a confirmation.
- `ErrSelfApproval` → **409**: "an author cannot confirm their own merge request".
- `ErrNotEligibleCollaborator` (existing) → **403** for a non-collaborator approval.
- an execute by a non-member → **403**.
- a merge request under the wrong project → **404**, indistinguishable from a merge
  request that does not exist, the same way every other project-scoped read behaves.

## 4. API shape

`GET /api/v1/projects/{slug}/merge` returns the same rows plus a `gate` object, so no
client has to re-derive §5.7 from the charter — the mistake the criteria page exists
to avoid, where the server's arithmetic is rendered rather than recomputed:

```json
{"id": 1, "project_id": 1, "feature_id": 1, "author_id": 2, "title": "…",
 "status": "open", "opened_at": 1792100533,
 "gate": {"approvals": 1, "needed": 2, "eligible": 3, "quorum_ok": false,
          "reviewer_ok": false, "requires_quorum": true, "passed": false}}
```

`approvals` is the *counted* number, not the number of rows: an approval from the
author or from a lapsed collaborator exists and does not count, and a page that
counted rows would show a filled quorum the server refuses to honour.

## 5. The page

`/projects/{slug}/merge`, tab "Merge" — the reader the four endpoints have never had,
and where the gate becomes visible instead of being a refusal with no explanation:

- one row per merge request: title, status, opened date, and the gate as
  "1 of 2 confirmations" with reviewer state spelled out;
- **Confirm** enabled only for a viewer who may confirm (a reader learns the rule
  from the control rather than from a 409);
- **Merge** / **Reject** for a member with role ≥ contributor, with the server's own
  refusal message shown verbatim when the gate is unmet;
- a form to open a merge request against one of the project's features.

Deliberately not on the page: editing a merge request's title, deleting one, and a
diff view (Concord has no diff — the merge request records that a change is ready to
land, §9's recommended MVP keeps Git in Forgejo).

## 6. Verification

- `go test ./internal/store/ -run TestMergeGate -count=1` — the arithmetic: the
  author's own approval does not count; a lapsed collaborator's stops counting; the
  minimum-2 floor; the ratio; the clamp at `eligible`.
- `go test ./internal/httpapi/ -run 'TestAMerge|TestAnAuthor|TestTheMergeList|TestTheCharter|TestTwoConfirmations|TestAReviewerApproval' -count=1`
  — the eight tests above.
- `tests/e2e/merge_e2e.py` — the page: the gate is rendered for a seeded merge
  request; the merge button refuses while the gate is unmet and the server's message
  appears; after confirmations the merge succeeds and the row shows `merged`; a
  hostile title renders as text.
- Every gate is mutated to prove it can fail (see §7 of `docs/REMAINING.md` §16).

## 7. Decisions taken, and why

1. **The confirmations that COUNT must come from eligible collaborators; the people
   allowed to press Confirm need only be members.** §6.3 says "distinct collaborator
   confirmations", and `MergeQuorumThreshold(eligible)` is measured against §8.2's
   population, so the count is filtered by it — a quorum held open by people who have
   left is the failure mode `eligibility_test.go` exists to pin. But the *act* of
   confirming is a member's, because route (4) makes approving somebody else's
   request a way to become a collaborator in the first place. The first version of
   this rule required eligibility for the act and broke that test, which is how the
   distinction was found: an approval can be the contribution that makes it count.
2. **An author's self-approval is refused rather than recorded-and-ignored.** A
   recorded approval that does not count is a page that lies. The refusal names the
   rule.
3. **The gate lives in the store, not the handler.** `CloseConsensusCall` sets the
   precedent: the quorum belongs to the write path, so no other caller can route
   around it.
4. **The charter's dropped quorum is not a "silent" bypass.** The audit row says the
   merge happened without one.
5. **Cross-project execution is a 404.** Anything else tells a caller whether a
   merge request id exists elsewhere.
