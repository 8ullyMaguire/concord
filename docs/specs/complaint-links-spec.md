# The extra complaint link — §354's "at least one" (docs/REMAINING.md §26)

Status: implementation spec, written before any code (`2cc5beb`). Small by design: this is a
missing WIRE, not a missing pillar.

## 1. What is missing, and why it reads as done

`feature_complaints` is a many-to-many join. `CreateFeature` writes it (one `INSERT OR IGNORE`
per validated complaint id it was handed), and two read routes serve it
(`GET /features/{id}/complaints`, `GET /complaints/{id}/features`). `store.LinkComplaint` exists —
it validates both rows, refuses a complaint that is not `validated`, inserts and touches
`features.updated_at` — and **nothing calls it**. Measured on this tree:

```
grep -rn "LinkComplaint(" internal/ | grep -v _test    ->  the definition, and no caller
```

§354 says a feature "must link to at least one validated complaint". A feature created with one
complaint therefore satisfies the letter of the spec while the schema's own extra-link path is
unreachable: the second complaint that later turns out to bear on the same work has no way in.
That is the same shape as the rest of this repository's findings — **a method whose data model
and reads landed first, so every schema dump and every read test says the surface exists.** What
is absent is the write and the control that offers it.

## 2. What the pass must be able to say when it is done

1. A contributor can attach a **validated** complaint to an **existing** feature.
2. The rule that a link is within one project is enforced **where the ids are turned into
   links**, not only in the URL resolution: today `LinkComplaint` takes two bare ids and never
   checks that they are in the same project, so the same function reachable by any future caller
   would happily join a feature in one project to a complaint in another. The read route
   (`handleListFeatureComplaints`) proves the feature belongs to the project; nothing proves the
   complaint does.
3. The action is audited with old→new information a reader can act on, like the other
   feature-level writes.
4. A refusal says which rule it hit, in the server's own words, on the page.
5. The page states what it knows: which of the project's complaints are already linked, and
   which are eligible to be.

## 3. Decisions (delegated judgement, recorded in `docs/QUESTIONS.md` as Q4)

**Where the control lives: the FEATURE page.** A feature gathers the evidence that justifies it;
the complaint page already lists the features answering it, and a control there would answer
"which feature should this complaint also bear on" — a question about the feature pool, asked
from the wrong end. §3.4 of `docs/specs/frontend-spec.md` lists linked complaints as required
content, so the control belongs beside them.

**The bar is `contributor`.** The same role that may create a feature (which is the same act,
with the ids coming from a body instead of the create form) and the same role that may attach a
feature tag. Not maintainer: a maintainer dial would make this a curation step rather than
participation, and §7's premise is that the corpus is built by contributors.

**The complaint id travels in the BODY** (`{"complaint_id": N}`), not in the path. The path's
`{id}` is the feature — the resource being modified; the complaint is the payload. A second
`{id}`-shaped path segment would make `requireFeatureInProject` and the complaint resolution
compete for the same param name, which is how a nested route ends up checking one id and acting
on the other (the defect §24 records for `/complaints/{id}/…`).

**No unlink route in this pass.** A feature must keep at least one validated complaint, so a
generic unlink can produce a state §354 forbids; deciding what "remove the only complaint" means
(delete the feature? refuse? demote it?) is a product question, not a missing wire. It is Q4, and
`DeleteFeatureComplaint`-shaped work stays out until it is answered. `docs/REMAINING.md` §210
already records "unlink a complaint (store-level only)" as the hazard.

**Idempotent, like its neighbours.** Linking twice leaves one row and answers 200 with the list.
`AddFeatureTag` and `RemoveFeatureTag` are idempotent for the same reason: a browser that retries
a request must not have to reason about which attempt landed.

## 4. The contract

```
POST /api/v1/projects/{project_id}/features/{id}/complaints
  body:  {"complaint_id": 41}
  200 -> {"feature_id": 12, "complaints": [ ...the full list, as the read route returns... ]}
  400 -> the complaint is not validated (store.ErrInvalid, in the store's words)
  403 -> the actor is not a contributor
  404 -> the feature is not in this project, OR the complaint is not in this project,
         OR either id does not exist (404 and never 403: a 403 confirms the id is real)
```

The response carries the full list rather than just the new row because the page must re-render
the section's own count; a second GET to learn what the page just caused is the read-after-write
that the tags panel avoided for the same reason.

Guards, in the house order (`internal/httpapi/feature_tags.go` is the model):

1. `requireWriteActor` — the consent/guest gate every write goes through.
2. `requireProjectID` — the URL's `{project_id}` is a SLUG, resolved here.
3. `requireRole(projectID, "contributor", r)`.
4. `requireFeatureInProject` — proves the feature belongs to the project.
5. The complaint: `GetComplaint`, then `c.ProjectID != projectID -> ErrNotFound`.

## 5. Store change

`LinkComplaint` takes the project id:

```go
func (d *DB) LinkComplaint(ctx context.Context, projectID, featureID, complaintID int64) error
```

and refuses, by name, when the feature is not in the project (`ErrNotFound`), when the complaint
is not (`ErrNotFound`, with the ids in the message), and when the complaint is not `validated`
(`ErrInvalid`, unchanged from today). The signature changes and that is deliberate: it is the
guard's home, not the handler's, so the next caller cannot skip it. There is exactly one caller
after this pass, and one before it (none), so the change costs nothing and removes the trap.

## 6. Proof, at the layer that can see each defect

| claim | what proves it |
|---|---|
| the write exists and is reachable | httpapi test: POST → 200, the list grows, `feature_complaints` has the row |
| the same-project rule on the COMPLAINT | store test: a complaint in another project → `ErrNotFound`, no row written |
| the same-project rule on the FEATURE | store test: a feature in another project → `ErrNotFound` |
| the validated rule | store test: an open complaint → `ErrInvalid` |
| idempotence | store test: two calls, one row |
| the audit | httpapi test: an `audit_log` row for the project naming the feature and the complaint |
| the role gate | httpapi test: a non-contributor → 403; a guest without consent → 401 |
| the page offers it and can be used | browser test: the select lists the eligible complaint, the attach click grows the count AND the list |
| the wiring cannot be swapped | `tests/e2e/feature_ui_mutants.py`: the path swapped to the read route, the control never wired, the list not re-read after the write |
| a route with no reader | `reader_coverage_test.go`: a dedicated row whose anchor is the RENDERED control, not the route string |

The last row is the §24 lesson: an anchor satisfied by the fetch table proves nothing about the
control existing, so the anchor is the markup the renderer alone produces.
