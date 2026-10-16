# Spec — the field-report write half

**Status:** IMPLEMENTED 2026-10-16 in §28 (`internal/httpapi/field_reports_write.go`, the two routes,
the panel's form and reply control). Written before any code; what landed, what a surviving mutant
taught, and what is deliberately still unwritten are in `docs/REMAINING.md` §28.

## The problem

`docs/specs/finder-spec.md` §4 (R5) specifies field reports as *the* replacement for rants and
"does anyone use X?" threads: a structured record of somebody actually running the software, with an
outcome enum, an environment, and an owner's reply. The store implements it completely, with the
spec's own semantics — `migrated-away` must name its successor, `is_owner` is read from the membership
table and never from the request, a report is flagged rather than deleted, and the outcome rate is
weighted by reporter reputation with `CaveatCredit = 0.5` declared by name.

What is missing is the write half. `internal/httpapi/server.go` registers **one** route for field
reports — `GET /api/v1/projects/{project_id}/field-reports` — and the project page renders the panel
(`project.js` `fieldReportsPanel`, with the rate, the denominator, the per-environment breakdown and
the owner replies). So §27's inventory measures `CreateFieldReport`, `RespondToFieldReport`,
`RemoveFieldReport`, `EditFieldOwnerResponse` and `DeleteFieldOwnerResponse` as TEST-ONLY: five store
functions exercised by tests, reachable by no user.

The observable consequence is the one §25 found one layer up: **the panel can only ever say "No field
reports yet"**, because nothing can create one. That copy is not wrong — it is the state the code
guarantees forever.

## Scope

**In:** filing a report, and responding to one. These are the two writes that make the corpus real,
and the second is the spec's central rule ("Owners can respond but cannot delete reports").

**Out, and named in the inventory with reasons:** removal (the store refuses a project member, so the
route is `POST`-able only by a non-member — that is §4.7's "removed by quorum with appeal" and the
appeal flow does not exist), and editing/deleting a response (the store's rule is that only the author
may correct a reply and never an owner's published statement; exposing it needs the response addressed
by id plus a per-reply control).

## Routes

| route | actor | why |
| --- | --- | --- |
| `POST /api/v1/projects/{project_id}/field-reports` | any signed-in account | A field report is an experience record about *using* the project. Requiring membership would restrict the evidence to people who already joined, which inverts §4.7 ("worked for 83% **of reporters**") and makes the outcome rate a poll of the converted. |
| `POST /api/v1/projects/{project_id}/field-reports/{report_id}/responses` | any signed-in account | §4.7 gives the owner a right to respond; it does not forbid a bystander from adding "I had this too", and the store already marks which is which (`is_owner` from `GetRoleForProject`). |

Both are **project-scoped in the path**, including the respond route, so the visibility check is
`requireProjectID`'s and cannot be forgotten; the handler still proves `report.ProjectID ==
{project_id}`, because report ids are global and a route that read only `{report_id}` would let any
project's URL answer for any report (the nested-route defect §26 fixed one layer down).

Both are behind the instance consent gate like every other mutating route, and
**authentication is checked before the project is resolved** — the ordering rule in
`internal/httpapi/auth.go`, so an anonymous caller learns nothing about which slugs exist.

## Rules the handlers must not re-derive

- The outcome enum, the `migrated_to` requirement and the "needs an author/project" checks live in
  `CreateFieldReport`; the handler passes the actor id in and maps `ErrInvalid` to 400 with the
  store's message, which names the field.
- `is_owner` is **never read from the body**. A request field with that name must be ignored, and the
  test asserts the stored badge follows membership, not the payload.
- A removed report is refused a new response (a hidden report must not collect fresh replies).

## UI

The project page's field-reports panel gains:

1. **"File a field report"** — a form with the outcome as a `<select>` of §4.7's four values, plus
   version, use case, environment, and a detail textarea. `migrated_to` is rendered as a **labelled
   input that the page tells you is required for one of the four outcomes**, rather than a field the
   server rejects after the fact.
2. **A reply form on each report**, which posts to that report's response route.

Both write and then reload, and both render a refusal beside the control that caused it (the house
pattern from §26's attach control). Neither renders a control the server would refuse.

## Acceptance

1. Both routes exist and are reachable (POST without consent → 403, with consent and no token → 401,
   with a token → 201/200); a report filed through the API moves the panel's `sample_size` from 0 to
   1 and makes `outcome_rate` appear.
2. An invalid outcome and a `migrated-away` report with an empty `migrated_to` are 400 with the
   store's message naming the field.
3. `is_owner` on a response follows membership: a member's reply carries the badge, a non-member's
   does not, and a client-sent `is_owner: true` changes neither.
4. A report id addressed through another project's URL is 404 and writes nothing.
5. A response to a removed report is refused.
6. The browser asserts the panel's **rate and denominator change** after a report is filed (positive),
   and that a member's reply renders with the owner badge.
7. UI mutants: the create posts to the read route, the outcome select loses a value, the reply form
   posts with the wrong field name, the reply form is never wired — each killed by the browser test.
8. Go mutants: the in-project guard, the consent/auth ordering, the `is_owner`-from-body trap, the
   outcome pass-through.

## Named limits

- **Filing is not rate-limited in this pass.** Nothing stops one account filing a hundred reports;
  §4.2's reputation weighting limits the *effect* of a report but not its *number*. Named here
  rather than implied, because "weighted by reputation" reads as a defence against flooding and is
  not one.
- The panel's 10-report window is unchanged: a project with more reports shows the newest ten and
  says how many it did not show.
