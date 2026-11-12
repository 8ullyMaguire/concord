# Milestone-level consensus — spec

**Status: spec'd, not built.** The last product idea from the filed inventory with no code
(`docs/IDEAS.md`, V6). Written 2026-11-12, after R17 shipped milestones and after `§46` made a
call and its feature's phase say the same thing — this surface must obey that lesson from the
first line, not be repaired into it later.

## 1. Problem

A milestone is a group of features a project promises to deliver together. Today every promise
is decided one feature at a time, and the milestone itself is never decided:

- `milestones` has no decision attached to it. A milestone can be created by a maintainer,
  pointed at twelve features, given a `target_date`, and shown on `/roadmap` as a commitment —
  with no record that anyone agreed to it. That is the same defect `§46` documents for
  `features.status='consensus'`: a claim about a decision, backed by no decision.
- Committing to a milestone costs N feature-level consensus calls, which is the wrong unit. The
  decision a project actually makes is "we deliver this group by this date", and the group is
  the thing that can fail.
- `arenas` has no `milestone` type, so milestones cannot even be ranked against each other —
  there is no answer to "which milestone first" anywhere in the product.

## 2. Goal

One decided object: a milestone. A consensus call whose subject is a milestone, opened through
the same conditions-and-outcomes machinery as `§6.5`, with the milestone's own status moving on
the outcome and nothing else moving silently.

Success is measurable on the deployed instance:

1. `POST /api/v1/projects/{slug}/consensus` with `{"milestone_id": N}` answers `201`, and
   `GET .../milestones/{N}` then reports `"decision": {"status": "open", "call_id": ...}`.
2. `POST` again for the same milestone answers `409` naming the open call.
3. Recording `accepted` moves `milestones.status` to `active` **and** writes one
   `milestone.status` audit row naming the call; `rejected` moves it to `cancelled`.
4. No feature's status changes as a side effect of any of the above (asserted, see §5).

## 3. Non-goals

- **No cascade.** Accepting a milestone does not ready, start or ship its features. A feature is
  not deliverable because its group was agreed; the feature-level gates (`§3.2` evidence,
  `§6` blockers) stay exactly where they are. Cascading would let a group decision walk around
  the per-feature evidence rule, which is the rule this product is built on.
- **No automatic opening.** Nothing opens a milestone call because a date arrived or because
  every feature reached `ready`. A call is a person's act.
- **No new voting UI.** The call page, position/objection/close routes and the hidden-tally rule
  (`§6.6`) are reused as they are. A second decision surface is a second place for the tally to
  leak.
- **No cross-project arenas.** A milestone belongs to one project; a multi-project roadmap is a
  different product.
- **No `status='consensus'` for milestones.** The CHECK is
  `planned|active|done|cancelled` and it stays that way: an open call is visible through the
  call, and inventing a sixth value to mean "deciding" is how `§46` happened.

## 4. Design

### 4.1 One arena per PROJECT, one entry per milestone

`arenas` type `milestone`, `project_id` set, `feature_id` NULL — the same shape as
`feature-priority`. Entries are `(entity_type='milestone', entity_id=milestone.id)`.

**Rejected alternative: one arena per milestone.** It gives each milestone an isolated rating
with no competitor, so `§6.4`'s confidence condition ("beats the runner-up AND the baseline")
can never be met and the ranking answers nothing. The whole reason to put milestones in an
arena is to rank them against each other and against doing nothing.

### 4.2 `consensus_calls.milestone_id`

A nullable FK beside the nullable `feature_id`. Exactly one of the two is set for a call that is
about something; both NULL is the existing "process decision" shape. Refused at insert when both
are set — a call about a feature and a milestone is a call about two things.

### 4.3 Readiness conditions (three, named in the refusal)

Reported separately, like `CallReadiness.Conditions`, so a project can tell "nobody proposed
anything" from "the group is not deliverable":

1. **deliverable** — every feature in the milestone is `ready`, `in_progress`, `review` or
   `shipped`. A milestone of drafts promises work nobody has designed. The refusal names the
   first offending feature by title.
2. **dated** — `target_date` is not NULL. A promise with no date is not a promise; this is the
   same rule that made the column nullable-with-no-default in `0037`.
3. **grouped** — the milestone holds at least one feature. An empty milestone is a name.

All three can be waived through the existing **early** path with a stated reason, exactly as
`§6.5` allows for solution calls, and the call records `opened_early=1` plus the reason.

### 4.4 Outcomes → milestone status (and nothing else)

| outcome | milestone.status | audit detail |
|---|---|---|
| `accepted`, `accepted-with-amendments`, `fall-back` | `active` | `planned -> active: adopted (call #N)` |
| `rejected` | `cancelled` | `planned -> cancelled: rejected (call #N)` |
| `sent-back` | unchanged (`planned`) | `planned -> planned` is NOT written; nothing happened |

Sent-back is the case worth stating twice: the call decided not to decide, so the milestone stays
where it was and **no** audit row is written. `§46` writes a `discussion` transition there because
`discussion` is a real feature phase; `milestones` has no equivalent, and inventing one to have
something to write would be the defect, not the fix.

A milestone already `done` or `cancelled` refuses to open a call (`400`, naming the status), for
the same reason a `shipped` feature does.

## 5. Invariants to test

Each one is a store test; the marked ones get a mutation arm in `make gates`.

1. A milestone whose features are all drafts refuses, and the refusal names the feature. (arm)
2. A milestone with no `target_date` refuses, and the refusal names the condition. (arm)
3. An empty milestone refuses. (arm)
4. The early path opens with a stated reason and records `opened_early=1`; with no reason it
   refuses. (arm)
5. Opening a call sets `milestone_id` on the call and leaves `feature_id` NULL. (arm)
6. A call with both a feature and a milestone refuses. (arm)
7. A second open call on the same milestone answers `ErrConflict`. (arm)
8. `accepted` → `active`, one `milestone.status` audit row, detail naming the call. (arm)
9. `rejected` → `cancelled`, audit row naming the call. (arm)
10. `sent-back` leaves the status AND writes no `milestone.status` row. (arm)
11. **No feature of the milestone changed status** — asserted by reading every feature before and
    after: this is non-goal §3, and it is the one a future contributor is most likely to "improve"
    into a cascade. (arm)
12. A `done` milestone refuses to open a call. (arm)
13. HTTP: `201` then `409`, and `GET .../milestones/{id}` reports the open call id. (arm on the
    readiness field in the handler)

## 6. Open questions (for the owner, not for the implementer)

- **Who may open a milestone call?** The plan uses the solution-call rule (`contributor`,
  `reviewer`, `maintainer`, `owner` via `eligibleCollaboratorRole`) for consistency. A stricter
  rule ("maintainer or owner") is defensible: a milestone is a project-level promise.
- **What does a milestone call's quorum count?** The plan reuses `§6.2`'s eligible-member count
  for the project, not the number of people who touch the milestone's features. The second would
  let three contributors who filed the ideas decide the promise for everyone.
