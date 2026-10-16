# Concord — §6.6's override window, and the denominator it is counted with

Written 2026-10-16, eighth pass. The authority is `docs/concord-spec-r4.md` §6.6/§6.7;
this file records how the second window is built, and one place where the code had
stopped agreeing with the spec's own words.

## 1. What §6.6 says, exactly

> **Resolving a block:**
> - The author amends the solution and the blocker withdraws or converts, or
> - **Override:** a second window where ≥ 80% of non-abstain participants vote to
>   override, with the objector's remedy answered in writing.

Two things are load-bearing in that sentence and both were wrong or missing in the
tree before this pass:

1. **"a second window"** — the override is a SEPARATE ballot, not a verdict the first
   call's close can reach on its own. §6.6's pass conditions end with
   "**4. No unresolved block**", so a call with a standing block closes `blocked`; the
   override is the second window's job.
2. **"≥ 80% of non-abstain participants"** — the denominator is everyone who took a
   substantive position: consent + stand_aside + block. Not "the people who took a
   side".

## 2. The denominator: a divergence, resolved in the spec's favour

`EvaluateConsensus` computed the override as `consent / (consent + block) ≥
OverrideRatio` — the DECISIVE ratio, whose denominator excludes stand-asides — behind
this comment:

> The override supermajority is measured the same way as a passing call: over the people
> who took a side. Including stand-asides here would mean a reservation could block an
> override it never opposed.

The spec says "non-abstain participants". The two readings differ, and the fixtures in
the tree could not tell them apart: every existing override case had either no
stand-asides (4 consent + 1 block → 0.8 either way) or a supermajority large enough to
clear both (~8/9 and 6/7). The one case that DID discriminate, `6 consent / 2 stand_aside
/ 1 block`, pinned the reading the spec does not state.

**Why the spec's reading wins**, on three grounds rather than the letter alone:

- **Otherwise an override is easier than an ordinary pass.** With the decisive
  denominator, `5 consent / 6 stand_aside / 1 block` reads decisive 5/6 = 0.833 ≥ 0.8 and
  carries — while support is 5/12 = 0.42, below the 0.5 floor an ordinary call must clear
  to pass at all. A room that is more reservations than consents (`Reluctant()`) would
  not be allowed to ACCEPT a proposal but would be allowed to OVERRIDE a principled
  objection to it. That is incoherent on its face, and it is the precise hazard §6.6's
  two-threshold design exists to prevent ("a room full of reservations can carry a
  proposal off a handful of supporters").
- **It makes the two rules mutually exclusive**, which the spec's design assumes: with
  the non-abstain denominator, `stand_aside > consent` implies a ratio below 0.5, so a
  reluctant consensus can never override anything.
- **The intent behind the old comment survives, weakened and correct.** A stand-aside is
  "reservations, but I won't block" — and under the spec's arithmetic they raise the bar
  without vetoing: 2 stand-asides in a 9-person room means the override needs 8 consents
  rather than a bare majority. They cannot stop it alone; they stop being counted as if
  they had consented.

So: `consent / (consent + stand_aside + block) ≥ OverrideRatio`, and
`OverrideRatio`'s doc comment now names the denominator. §6.7's Major tier — "override
needs ≥ 90%" — reads the same way.

## 3. The model

Two tables (migration 0026), because a second window is a second ballot with its own
votes, its own window and its own verdict:

| `override_windows` | |
|---|---|
| `id` | |
| `call_id` | the blocked call. `UNIQUE` — a second window is A second window, not a series |
| `opened_by`, `opened_at`, `closes_at` | who called it and its window |
| `status` | `open` \| `closed` |
| `closed_at`, `result` | `overridden` \| `upheld`, written on close |
| `note` | §6.6's "remedy answered in writing", when it carries |

| `override_positions` | |
|---|---|
| `(window_id, user_id)` | primary key, so a voter's stance is updated rather than duplicated |
| `position` | the same four values, same CHECK: `consent` \| `abstain` \| `stand_aside` \| `block` |
| `reason`, `updated_at` | |

One vocabulary on purpose: in an override window **consent = override the block** and
**block = uphold it**; abstain and stand-aside keep their meanings, and the ratio is the
support ratio with a higher threshold.

## 4. The rules

**Opening.** Only on a call that is `closed` with result `blocked` — an override needs a
standing block to override, and the verdict has to have been RECORDED first (which is
what the seventh pass fixed) — and only while an objection on it is still `open`: if the
objector withdrew or converted, §6.6's other branch already resolved it and a vote would
decide nothing. A maintainer opens it (§6.8: "call and close consensus"), once. Every
refusal is named rather than answered with an empty window: the call is not closed /
closed but not blocked / no objection is open / a window already exists (§409) / the call
is suspended by an emergency hold (§6.6's hold pauses a call, and overriding a block
RESOLVES it, so a held call's block cannot be voted away).

**Voting.** §8.2's eligible collaborators — the same gate as the first window's positions,
for the same reason: a stance is counted in `Participants`, which quorum is compared
against.

**Closing.** A maintainer, and the verdict is `governance.EvaluateOverride`:

```
quorum:  participants >= QuorumThreshold(eligible, charter)     // same rule as §6.6
carries: consent / (consent + stand_aside + block) >= OverrideRatio
```

- carries → the window records `overridden`; §6.6's remedy answered in writing is
  REQUIRED (refused before any write, naming §6.6); in ONE transaction the parent call's
  result becomes `accepted_overridden` and every open objection on it becomes
  `overridden` carrying that answer;
- does not carry → the window records `upheld`, the call stays `blocked`, and its
  objection stays open. A block that stood is not overridden, and the record says so.

**Reading.** `participants`, `eligible` and `quorum_needed` stay visible while the window
is open (§6.6's "participation progress and the quorum bar remain visible"); the four
running counts (`consent`, `stand_aside`, `block`, `abstain`) do not — stripped in the
READER, not the handler, so no caller can publish a live tally by forgetting: the same
anti-bandwagoning reason the call's tally is hidden. The window's struct carries json
tags, and the API answers snake_case keys — asserted as names on the raw body, not by
decoding into a struct (`json.Unmarshal` cannot tell the two apart, and a browser can).

## 5. Routes

| Route | Role |
|---|---|
| `POST /api/v1/projects/{project_id}/consensus/{call_id}/override` | maintainer: open the window |
| `GET  /api/v1/projects/{project_id}/consensus/{call_id}/override` | read it (public, like the call) |
| `POST /api/v1/projects/{project_id}/consensus/{call_id}/override/position` | §8.2 eligible |
| `POST /api/v1/projects/{project_id}/consensus/{call_id}/override/close` | maintainer: record the verdict |

All four need a reader in `readerInventory` (the ninth pass's split row means this subtree
gets its own rows rather than riding `/consensus`).

## 6. The page

The Consensus page grows an **Override window** section, shown only when the call is
closed with result `blocked`:

- no window yet, signed in → "Open an override window" (the server decides whether the
  reader may);
- a window open → progress ("N of M needed to reach quorum (K eligible)"), the four stance
  buttons, and the closer's note field labelled as §6.6's answer;
- a closed window → its result, and why: `upheld` says the block stood, `overridden` says
  the call was accepted over it and shows the answer kept on the objection.

The client decides nothing about standing (it cannot know who is a maintainer) and derives
no ratio (it cannot know the charter) — both are the server's answers, shown verbatim.

## 7. Definition of done

- `governance.EvaluateOverride` and the corrected `EvaluateConsensus`, both with the
  discriminating fixture (`4 consent / 3 stand_aside / 1 block`: NON-abstain 0.5, decisive
  0.8 — the case that tells the two readings apart).
- Store: open (wrong status, duplicate), cast, tally, close carries / does not carry, the
  note required only when it carries, the parent call and its objection updated in the
  same transaction.
- HTTP: the two role gates and the §8.2 gate, the read's shape and its wire names, the note
  refusal through the API.
- Browser: a blocker closes as `blocked`; the window opens, is voted in and carries; the
  call's meta shows `accepted_overridden` and the objection shows the answer.
- Mutations: each gate made to fail, then restored byte-for-byte.
- `make verify` and `make e2e` green; the doc numbers (`26 migrations`, tables, test
  counts) updated in every place the gates read.
