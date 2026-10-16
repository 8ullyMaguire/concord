# Concord — the Consensus page

**Status:** written 2026-10-04, before any code.
**Scope:** `/{project}/consensus` — priority 2 of `frontend-spec.md` §3.1, and the
first page that is *core to the product* rather than merely useful. §3.3 calls it
"the core of the product" and says §5.5 is unbuildable without it.
**Companion:** `frontend-spec.md` (of record for the frontend), `phase3-spec.md`,
`scout-spec.md`. Where they disagree about the Consensus page, this one is correct
about what is built and they are aspirational.

---

## 1. Why this page, and why now

`frontend-spec.md` §8 rule 2: *"A shipped endpoint with no UI is unfinished work, not
a backend detail."* The consensus API is the largest unsurfaced surface in the
product — nine endpoints under `/api/v1/projects/{id}/consensus` — and none of it is
reachable from a page. A member can cast a position only with `curl`.

---

## 2. The one defect to fix first, because the page must not be built on it

**Quorum was never enforced.** `EvaluateConsensus` opens with

```go
if cc.Participants < QuorumThreshold(cc.Eligible, c) { return ResultInsufficientQuorum }
```

and `QuorumThreshold` returns 0 for any `eligible <= 0`. `CloseConsensusCall` never
set `Eligible`, so the threshold was always 0 and the check could not fire. Measured
before the fix: in a project with three eligible members, one member consenting to
their own call was **accepted**.

Fixed in `b8d7b87`, with a regression test that fails when the fix is removed.

---

## 3. What the page must NOT do: compute consensus in JavaScript

`frontend-spec.md` §3.3 is emphatic that §6.3's ratio counts a stand-aside *against*
consent, and that collapsing the counts "would display the bug". The fix landed in
`internal/governance`, and there are **two** ratios with different meanings:

| measure | denominator | what it answers |
|---|---|---|
| `support_ratio` | consent + stand_aside + block | how many went along **with reservations** |
| `decisive_ratio` | consent + block | how many took a **side against each other** |

§6.6 requires **both** (`support >= 0.50` and `decisive >= 0.70`). They disagree
constantly: 4 consent / 3 stand-aside / 0 block is `support 0.57` but `decisive 1.00`.

**Rule: the page renders counts and ratios; it never derives them.** The server
already owns this arithmetic in `governance.ConsensusCounts`, and
`store.ConsensusThresholdsSummary` already serialises exactly the shape §3.3 needs.
That function currently has **no callers** — it was written for this page and never
wired. Step C2 exists to give it one.

A client that recomputes either ratio is a client that can be wrong about what
consent means, and it will be wrong silently.

### 3.5 The rule this spec originally missed: the tally is HIDDEN until close

**Added 2026-10-04, after the first version of this page shipped in violation of it.**

`concord-spec-r4.md` §6.6 states, as its own paragraph:

> **Hidden tally.** Running counts are hidden until the call closes, to prevent
> bandwagoning. Participation progress and the quorum bar remain visible.

The page shipped showing live counts and live ratios, which is precisely the
bandwagon §6.6 forbids. Worse, this spec's own §3 quoted §6.3 — a section about a
ratio that counts a stand-aside against consent — and never opened §6.6, which is
where the hidden-tally rule lives. **A spec that cites the neighbouring section is
evidence the section was not read.**

The rule, and what stays visible because §6.6 says so explicitly:

| while the call is OPEN | after it CLOSES |
|---|---|
| participants / quorum bar | all four counts |
| **NOT** the counts | both ratios |
| **NOT** the ratios | the result |
| your own position | every position |

`eligible` and `quorum_required` are not counts of anyone's stance, so they stay
visible throughout — they are the quorum bar, which §6.6 preserves.

**The server decides, not the client.** The read response gains a `tally_visible`
flag (step C9), and the page renders what it is told. A client that decided for
itself would let anyone bypass the rule with devtools, which defeats it.

---

## 4. The page

`GET /projects/{slug}/consensus`.

### 4.1 Data

`GET /api/v1/projects/{slug}/consensus/{id}` answers:

```json
{ "call": { "id": 1, "project_id": 1, "feature_id": 0, "opened_by": 1,
            "opens_at": 1750000000, "closes_at": 1750600000, "status": "open",
            "result": null, "summary": null, "created_at": 1750000000,
            "question": "Should we adopt X?", "description": null,
            "solution_id": null, "opened_early": null, "early_reason": null,
            "outcome": null, "fallback_to_solution_id": null },
  "positions": [ { "call_id": 1, "user_id": 2, "position": "consent",
                   "reason": "", "updated_at": 1750000100 } ],
  "objections": [ { "id": 1, "call_id": 1, "user_id": 3, "principle": "...",
                    "violation": "...", "remedy": "...", "status": "open",
                    "created_at": 1750000200 } ],
  "tally": { "consent": 1, "abstain": 0, "stand_aside": 0, "block": 0,
             "participants": 1, "eligible": 3, "quorum_required": 2,
             "support_ratio": 0.33, "decisive_ratio": 1,
             "support_required": 0.5, "decisive_required": 0.7,
             "reluctant": 0, "open_objections": 0 } }
```

`tally` is **added by step C2**. Everything else is the shape that ships today.

### 4.2 Positions — four, not three

The `positions` CHECK constraint permits `consent`, `abstain`, `stand_aside`,
`block`. `abstain` is neutral in **both** ratios and `stand_aside` only in one, so
collapsing them loses a real distinction. Four buttons, always.

### 4.3 Keyboard

`c` / `a` / `s` / `b`, arrows to change, `Enter` to submit — and **every shortcut
also has a button**, per §3.3: shortcuts are accelerators, never the only route.

### 4.4 Signed out

Read-only, with a sign-in prompt in place of each of the four position controls.
Same rule as every other page.

---

## 5. Definition of done (`frontend-spec.md` §8, all ten)

1. Spinner removed on **every** async path, including failure.
2. Every shipped consensus response rendered — including `outcome` and
   `fallback_to_solution_id`, which are §6.5's subject/vote distinction.
3. Signed out: read-only, prompt in place of each write control.
4. Private projects render for members, 404 for everyone else, no layout flash.
5. Keyboard-operable, visible focus ring on every control.
6. Markdown via a sanitising parser; **never** `innerHTML` for authored text.
7. Errors render `.error-state` with the server's message and a retry.
8. `internal/httpapi` tests cover `200`, `404`, and one anonymous case.
9. No raw hex, no ad-hoc margins, no new top-level CSS.
10. Registered in the document viewer if it lists documents — n/a here.

### 5.1 Two rules that are not negotiable

- **Both ratios, always, labelled.** Rendering one is rendering a bug. The labels
  are the spec's own words: support counts reservations, decisive counts sides taken.
- **`positions.reason` is never rendered.** `CastPosition` does not accept a reason
  (the column defaults to `''`), so any reason shown would be empty by construction.
  Render the stance or nothing.

### 5.2 Objections: who may record which outcome (added 2026-10-16, §18.1)

§6.6's remedy for a block has two branches, and only the override was reachable for the
life of the page: "the blocker withdraws" needed the maintainer role, because
`PUT /consensus/objections/{id}/resolve` gated all four statuses the same way. The
standing now depends on the **outcome**:

| status | who may record it | why |
|---|---|---|
| `withdrawn` | the objector, or a maintainer | the block is the objector's to drop. Neither role nor membership is required to withdraw: the gate on RAISING a block is being an eligible collaborator (§8.2), and dropping your own asks for less than that |
| `resolved` | a maintainer | a finding about the proposal, and the person who blocked it is not the judge of it |
| `overridden` | a maintainer | same, and it is the DIRECT route: §6.6's 80% override window (§20, `docs/specs/override-window-spec.md`) is the ballot that answers a block without anyone recording a status, and the status is what a maintainer records when the window is not the answer |
| `vetoed` | a maintainer | §6.6's emergency hold |

`open` is the column's DEFAULT, not an outcome, and the store refuses it.

The page renders accordingly and never decides standing for itself:

- an objection whose `user_id` is the signed-in reader's own id gets **Withdraw this
  objection** — one status, reason required — and NOT the four-status control, because
  the server refuses the objector all four of those. Reading the session's own id is
  the one judgement the page makes, and it is a fact about itself, not a claim about
  who may do what;
- everything else gets the four-status control, and the server's answer is what the
  page shows. A refusal names the role it wanted ("…needs the maintainer role or
  above"), which is the sentence the reader has to act on.

### 5.3 Closing a call, and where §6.6's override happens (added 2026-10-16, §19; rewritten in §20)

**Who may close.** §6.8's role table puts "call and close consensus" in the
maintainer's column, and both routes enforce it. The page does not: it offers the
control to anyone signed in and reports the server's refusal, because the page has no
way to know who is a maintainer — the same reason §5.2's objection controls are not
gated client-side. A hidden control would be a claim the page cannot check.

**A close records a verdict; it never overrides.** §6.6's pass conditions end with "No
unresolved block", so a call with one standing closes **`blocked`** — the oldest defect
this page had was that a close wrote nothing at all (§19.1), and the second was that a
close could reach `accepted_overridden` on its own, which made §6.6's second window
redundant. Answering a block is the override window's job: see `override-window-spec.md`,
and §20.2 for why the ratio it uses counts every non-abstain participant.

**What the close leaves behind.** Two things, in one transaction, because they are one
decision:

| Written | Read by |
|---|---|
| `consensus_calls.status`, `closed_at`, `result` | the page's meta line (`result: blocked`), and every later read |
| the audit entry | the admin ledger |

Nothing is written to the objection: a block that stood is not overridden, and the row
that says so is the one the reader is looking at.

**Standing to vote.** A position and a block both need §8.2's standing — a member of the
project with a qualifying contribution inside the activity window. Not for tidiness: a
position is counted in `Participants`, which is what quorum is compared against, so a
stance from outside the pool both meets quorum and moves both ratios. See §19.3.

### 5.4 §6.6's override window (added 2026-10-16, §20)

Its own section on this page, shown only for a call that closed `blocked`:

- **no window, signed in** — "Open an override window". Offered to anyone signed in and
  refused by the server when the reader is not a maintainer, for the reason the close
  control is: the page cannot know the role.
- **a window open** — the progress line ("N of M needed to reach quorum (K eligible)"),
  the four stance buttons (consent = override the block, block = uphold it), the note
  field labelled as §6.6's answer, and the closer's button. The running COUNTS are not
  on the page because they are not in the response (§6.6's hidden tally, §3.5).
- **a window closed** — its result and why: `upheld` says the block stood; `overridden`
  says the call is accepted over it, and shows the answer kept on the objection.

The client derives nothing: not who may act, not the ratio, not the verdict. Every one of
those is the server's answer, quoted.

### 5.5 §6.5's decision, the fallback chain, and §6.6's hold (added 2026-10-16, §21)

Three routes had handlers, store tests and no reader. All three are on this page now,
and each is read rather than inferred:

- **`GET …/consensus/{call_id}/fallback` — "If this call stalls or is blocked".**
  §6.5: *"the next-ranked solution is the pre-agreed fallback"*. `pre-agreed` is the
  whole word: the chain is rendered **while the call is open**, strongest first with the
  baseline last and marked as the baseline (ranked, never adopted). The call's own
  solution is absent by the server's own rule — falling back to what is on the table is a
  no-op the store refuses. **Requested only for a call that has a feature**: a §6.6
  process decision has no ranking, and asking anyway is a 400 in the reader's console for
  the ordinary case.
- **`POST …/consensus/{call_id}/outcome` — "Record the decision"**, offered on a call
  about a solution that has no outcome yet, to anyone signed in (the server requires
  §6.8's maintainer and says so). Five outcomes, the summary the record keeps, a fall-back
  picker drawn from the chain **minus the baseline** (that is §6.5's "rejected — the
  baseline wins", a different outcome, and two spellings of one thing is one too many),
  and the amendment fields for "accepted with amendments". The confirmation names the
  record it wrote (`kind adr`, `slug call-N`) and links to the documents page, and it is
  the one element kept OUTSIDE the section: the panel hides itself once the call carries
  an outcome, so a confirmation kept inside would be hidden the moment it was written.
- **`GET …/consensus/{call_id}/hold` — the emergency hold.** A suspended call resolves
  nothing (close, window and outcome are all refused) and otherwise looks entirely
  ordinary. The page says which ground the hold rests on and when it expires, so a reader
  who finds every control refused is told why.

"Record the decision" is not "Close the call" twice. A close writes §6.8's **verdict on
the call**; the outcome records **what the call decided about the solution** — a call can
pass and still fall back to #2, which is why the two live in separate columns and why both
are on the page at once. Neither erases the other, and neither can be recorded twice.

---

## 6. Out of scope, deliberately

- **Pairwise "Both"/"Neither".** §3.3 mentions them for pairwise features. The
  `positions` CHECK constraint has no such value, so the store would reject them.
  Genuinely unimplemented, not deferred — recorded here so it is not mistaken for an
  omission.
- **A calls index.** §3.3 lists no endpoint that lists calls. The page is reached
  from a feature or a solution; without an index there is nothing to list. Tracked
  as C6, not built here.
- **Holding outcomes, merge requests, requests/answers** — separate pages in §3.1.
