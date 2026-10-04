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
