# Moderation: taking an idea out of an arena, and the log that makes it checkable

**Status:** spec, 2026-11-11. Written before any code; nothing here is implemented.
**Supersedes:** nothing. Extends `docs/specs/audit-log.md` (the viewer) into a moderation surface.

## 1. The request, and what it turns into

The owner asked for three things that are one mechanism:

1. as a high-trust user or an admin, remove an idea from the arena page
   (`/projects/ferrisfeed/rank`) or from the idea's own page;
2. a confirmation, or a way to undo the removal;
3. **all** moderation recorded in a stash-box-style modlog with filters.

Item 2 rules out a `DELETE`. Item 3 rules out a bespoke endpoint that writes nothing:
an act nobody can read afterwards is not moderation, it is a silent edit. So the design
is one path for every moderation act — *store the act, derive the state, publish the
log* — and the arena removal is the first act to use it.

## 2. What already exists (measured, not remembered)

| surface | where | state |
|---|---|---|
| `audit_log` table | `internal/db/migrations/0001_init.sql:338` — `(id, project_id, actor_id, action, entity, entity_id, detail, created_at)`, index on `(project_id, created_at)` | live, 20+ writers |
| filtered reader over it | `internal/store/audit.go:91` `ListAudit` — filters `Action`, `ActorID`, `EntityType`, `EntityID`, `Text`, `Since`; returns `entries` + `total` | live |
| the page | `GET /projects/{slug}/audit` (`server.go:947`) → `assets/js/audit.js` (213 lines) — action + text filters, "showing N of total" | live |
| role ladder | `internal/httpapi/handlers.go:2445` — guest 0, user 1, contributor 2, reviewer 3, maintainer 4, owner 5; `requireRole` at :2475 | live |
| arena entry removal | `internal/store/arenas.go:495` `RemoveArenaEntry` | **exists, no caller, no route** |
| arena entry schema | `migrations/0017_arenas.sql:103` — `(arena_id, entity_type, entity_id, r, rd, sigma, games, updated_at)` | live |
| feature status write | `PUT /api/v1/projects/{slug}/features/{id}/status` | live |
| the estate's shared rules crate | `~/code-local/polaris-ledger` (Rust) — `Actor`, `Subject`, `Redaction`, `Rules`, `Act`, `Projected`, `project()`, `validate()`, `Cursor` | exists, **concord does not adopt it** — `internal/db/emergency_hold_rules_test.go:9` is a conformance table, and the last commit on this repo says so in its subject: "conformance, not adoption" |
| the estate's working modlog | darkhold `crates/app/src/routes/governance.rs:966-1030`, store `crates/db/src/governance.rs:1833` — `/modlog`, keyset cursor on `(created_at, id)`, actor published as pseudonym, panel decisions anonymised | live |
| stash-box's own log | `~/code-local/go/stash-box` — `grep -rn "modif" --include=*.graphql --include=*.go --include=*.sql` returns criterion *modifiers* and edit field-state only: **no mod log**. Its moderation record is per-entity edit history carried in `edit` rows | measured 2026-11-11 |

So two of the three asks are half-built already and the third has an unused primitive
(`RemoveArenaEntry`). The work is adoption and wiring, not a new subsystem — which is
also why the spec is short.

## 3. Decisions, with the alternative that lost

**D1 — "Remove" means out of the arena, not out of the database.** The act deletes the
`arena_entries` row and sets `features.status='rejected'` (a status the schema already
has) and writes one audit row carrying the prior values. The feature, its complaints, its
board card and any decision record survive.
*Rejected:* `DELETE FROM features`. §6.5's decision records and §9.3's audit both point
at the feature; a hard delete turns a public record into dangling references, and the
"undo" the owner asked for becomes impossible without a tombstone table that would be
the same row with a flag.
*Rejected:* a `deleted_at` column on `features`. A soft delete is the flag version of the
same idea, but it puts the state in a column every existing query must remember to
filter — 40+ readers — and a forgotten filter is a rejected idea still ranking.

**D2 — undo is a second act, appended.** `arena.remove` stores `{"reason": …, "prev":
{"status":"draft","elo_r":1500,"elo_rd":350,"elo_vol":0.06,"games":0}}` in
`audit_log.detail`; `arena.restore` carries `undoes = <id of the act it reverses>` and
restores those values. The log is append-only and the *derived* answer comes from a
subquery: an act is undone when another row names it.
*Rejected:* mutating the original row (`undone_at`). It makes the log editable, which is
the one property that makes a log evidence, and it loses the interesting fact — who
reversed it and why.

**D3 — one moderation path, and it validates against polaris-ledger's rules.** Every act
in §5 goes through `moderate()`: role check → rule check (`Reason::Required` for anything
that removes standing) → store write → audit row. The rules live in a Go mirror of
`polaris-ledger` (`internal/ledger`), because concord is Go and the crate is Rust; the
mirror is generated from the same test-vector file the crate's own tests use, so the two
cannot drift silently. `polaris-ledger` states the boundary itself: it owns no table and
no query, and neither does the mirror.
*Rejected:* a Rust sidecar or a cgo binding. A 273-line rules module is not worth a
second runtime in the deploy, and the estate's whole Go-side story is "same vectors, two
languages".

**D4 — authority is the trust level concord already has, OR the role ladder.** "High-trust
level user or admin" is not a new noun: `users.trust_level` is a per-user instance-wide
integer (`internal/store/trust.go:29`), and `requireTrustLevel`
(`internal/httpapi/trust.go:38`) already gates the direct feature-status route on
`config.TrustLevelMin()` (`CONCORD_TRUST_LEVEL_MIN`, default 1). Role is the other axis —
per project, "what may this person do here" — and both are already used together on
adjacent surfaces. So an act is permitted when **either** holds:

| act | permitted when | why |
|---|---|---|
| `arena.remove`, `arena.restore` | `trust_level >= CONCORD_MOD_TRUST_MIN` (default 1) **or** project role ≥ `reviewer` | instance-wide trust is the owner's own moderation; the role is a project's decision about its own arena |
| `feature.reject`, `feature.unreject` | role ≥ `maintainer` (unchanged: `PUT …/features/{id}/status` already exists and its gate is not moved) | §6.8 puts phase transitions there |
| `moderation.undo` | the actor of the original act, or role ≥ `maintainer` | who did it can take it back; a maintainer can take anybody's back |
| `feature.delete` | **not in v1** — see §8 | |

**Measured 2026-11-11, and it changes the default:** the live instance has 88 accounts,
**87 at `trust_level` 0 and exactly one at 1**, and `trust_config.max_level = 1`. A gate
at `>= 1` therefore means "the one account the owner has promoted", which is correct for
the request ("as a high-trust user or admin") but must be said out loud: there is no
second moderator today, and raising `max_level` is an operator decision, not a side
effect of this feature. The role half is what lets a *project*'s reviewers moderate its
arena without the owner promoting anybody instance-wide.
*Rejected:* a reputation floor (`reviewer` **and** `reputation >= N`). `reputation_events`
is real but has never been used as a gate anywhere in the tree, and picking a threshold
with no measurement is how a permission becomes a lockout.

**D5 — the modlog is the audit page, grown up. There is no second page.** `audit.js`
already renders filters, totals and actors. It gains: act rows rendered as acts (reason,
subject, undo control), a keyset cursor (the current offset paging repeats or skips rows
the moment a new act lands — `polaris-ledger`'s `Cursor` doc says exactly this), and the
filters the owner asked for (action, actor, subject kind, date range, text).
*Rejected:* `/modlog` beside `/audit`. Two readers of one table diverge; the audit page
already exists, is linked from the project nav, and its own comment says why it exists.
The page is **renamed in the navigation** to "Modlog" — same route, so no link breaks.

**D6 — the shared component already exists; concord's job is adoption.** `polaris-ledger`
is the estate's crate for exactly this and its doc-comment names both donors (darkhold's
modlog, concord's bounded holds). What it does not have is a Go form. The deliverable is
`polaris-ledger-go` — the rules, projection, validation and cursor with the crate's test
vectors — adopted by concord now and by the next Go member that needs a log. Until a
second Go adopter exists it lives *in* concord (`internal/ledger`) and is extracted with
its vectors; the estate note records that choice so it is not re-litigated.

## 4. Data model

One migration, `0034_moderation.sql`, additive only:

```sql
-- An act that reverses another. Nullable because most acts reverse nothing, and a
-- self-reference keeps the relationship in the log rather than in a second table.
ALTER TABLE audit_log ADD COLUMN undoes INTEGER REFERENCES audit_log(id);

-- Keyset paging. The existing index leads with project_id and created_at, so the
-- cursor's second key (id) is not ordered by it; without this, two acts in the same
-- millisecond can repeat or skip a row across pages.
CREATE INDEX idx_audit_keyset ON audit_log(project_id, created_at DESC, id DESC);
```

Nothing else. No `reason` column: the reason lives in `detail` as
`{"reason": "...", ...}`, which is where every one of the 20+ existing writers already
puts its prose, and `ListAudit`'s text filter already searches it. A second column would
be a second source of truth for the same sentence.

`detail` for the two new acts (exact shapes the page and the restore path depend on):

```json
{"reason":"spam: repeated link drops","prev":{"status":"draft","elo_r":1500,"elo_rd":350,"elo_vol":0.06,"games":3},"arena_id":7}
{"reason":"the removal was mistaken","undoes":1234}
```

## 5. The act vocabulary

| action | entity | reason | expiry | reversible | effect |
|---|---|---|---|---|---|
| `arena.remove` | `feature` | required | none | yes | delete `arena_entries` row(s) for that feature in that arena; `status='rejected'`; record `prev` |
| `arena.restore` | `feature` | required | none | — | re-insert the entry with `prev` ratings; `status=prev.status`; `undoes` the removal |
| `feature.reject` | `feature` | required | none | yes | `status='rejected'`, prior status in `prev` (no arena row touched) |
| `feature.unreject` | `feature` | required | none | — | restore `prev.status` |
| `moderation.undo` | `audit` | required | none | — | dispatch by the target act's action (`arena.remove`→`arena.restore`, `feature.reject`→`feature.unreject`) |

Everything else already in `audit_log` (`set_visibility`, `create_invite`,
`create_consensus_call`, holds…) is **unchanged and unfiltered**: it appears in the log,
it is not an act, and it gets no undo control. The page must render the two classes
differently, or an undo button appears beside a row that cannot be undone.

## 6. API

```
POST   /api/v1/projects/{slug}/moderation/arena/remove
       body {"feature_id": 123, "arena_id": 7, "reason": "…", "confirm": true}
       -> 201 {"act": {…projected row…}, "undo": {"action":"arena.restore","target":1234}}

POST   /api/v1/projects/{slug}/moderation/arena/restore
       body {"feature_id": 123, "arena_id": 7, "reason": "…", "undoes": 1234}
       -> 200 {"act": {…}}

POST   /api/v1/projects/{slug}/moderation/undo
       body {"audit_id": 1234, "reason": "…"}
       -> 200 {"act": {…}}          # dispatches to the right restore

GET    /api/v1/projects/{slug}/audit            (existing, gains parameters)
       ?action=arena.remove&actor=<id>&entity=feature&since=<unix>&until=<unix>
       &q=<text>&cursor=<opaque>&limit=<1..200>
       -> {"entries":[…], "total":N, "limit":N, "next_cursor": "…|…"|null}
```

- `confirm: true` is required. A client that posts without it gets
  `400 {"error":"this act needs confirmation"}`; the point is that no script deletes an
  entry by accident, not that the browser has a dialog (the browser has one too, §7).
- `reason` is trimmed and must be non-empty (`Defect::ReasonRequired`, the same sentence
  the Rust crate produces: "this act requires a reason, and none was given").
- 403 vs 404 follows the house rule: a role refusal is 403 with the role it wanted in the
  body (`roleRefusal`), an unreadable project is the generic 404.
- The response carries the **projected** act, so the page can render the row it just
  wrote without re-reading the log.
- `next_cursor` is `polaris-ledger`'s wire form: RFC 3339 `at`, a `|`, then the audit id.
  A malformed cursor is a 400, never a silent first page.

## 7. UI

- **Feature page (`/projects/{slug}/features/{id}`) is the canonical place.** A
  "Moderation" panel, visible only when `capabilities` says the reader may act: reason
  field, "Remove from the arena" or "Reject the idea", and the confirmation state is
  inline (the button becomes "Remove from arena? — this hides it from ranking" +
  Remove/Cancel) rather than a modal, because the page already has the reason field the
  modal would need.
- **Standings page (`/ranking`) gets a per-row control** for readers with standing —
  a link to the feature page's panel, not a second implementation. One write path.
- **`/rank` (the pairwise page) gets no destructive control.** A button that deletes the
  option under the reader's cursor, in the middle of a vote, is how a mis-click removes
  an idea. Each card links to its idea page instead; that is the "visit the idea page
  from this one" half of the request, done with a link rather than a weapon.
- **Undo appears twice**: inline on the act's row in the modlog (forever) and as a
  one-action toast on the page that performed the act (60 s), which is the
  `confirmation-or-undo` the owner asked for without a modal in front of every action.
- **The navigation entry for `/audit` is labelled "Modlog".** Same route, same reader.

## 8. Out of scope, deliberately

- **Hard delete of a feature.** Needs a decision about §6.5 decision records and §9.3
  audit references, and it is irreversible by definition, which is the property the
  owner asked to avoid. Refused, not deferred.
- **Notifying the affected author.** Concord has no inbox (measured: no notification
  table or route; `inbox` in the code is a board column). Removing an idea without
  telling its author is the weakest part of v1 and is recorded here as the first
  follow-on, not as a thing this spec silently omits.
- **Instance-wide modlog.** The log is per project because every act in it is. An
  instance page is a read over all projects and needs its own visibility rule; a
  separate row.
- **Appeals, bans, suspension.** Not in concord's governance model today; the act
  vocabulary in §5 is where they would be added, and polaris-ledger's `Expiry::Required`
  already carries the shape a suspension would need.
- **Reputation floors on by default.** See D4.

## 9. Success criteria

Each is a command, not an adjective; the plan carries the exact expected output.

1. `POST …/moderation/arena/remove` with a `reviewer` token removes exactly one
   `arena_entries` row and sets the feature's status, and the response's act id exists in
   `audit_log` with `undoes IS NULL`.
2. The same call from a `contributor` is `403`, and from a stranger `401`; a private
   project answers `404` to both.
3. A missing or whitespace `reason` is `400` with the crate's sentence; `confirm` absent
   is `400`.
4. `POST …/moderation/undo` restores the exact prior `elo_r`/`elo_rd`/`elo_vol`/`games`
   and status, and the modlog shows the original act as undone, by whom and why.
5. `GET …/audit` pages through 3+ pages of acts with no row repeated and none skipped
   when a new act is inserted between pages (the cursor test, not an offset test).
6. The feature page shows the panel to a reviewer and not to a contributor; the modlog
   page's filters each narrow the result and the `total` moves with them.
7. `make verify` green; the two new acts appear in `reader_coverage_test.go`'s inventory.

## 10. The shared component, and what a second adopter must supply

Verdict for the estate, recorded so it is not re-derived: **`polaris-ledger` is the
shared component; HYBRID, donor is the crate**. The crate owns the rules, the projection,
the validation and the cursor. Each product keeps what the crate deliberately refuses to
own: its table, its vocabulary, and the row it publishes.

Concord supplies: the acts (§5) as data, the store sentences, and the page. The Go mirror
supplies what the crate supplies, with the crate's test vectors as the contract between
them — one JSON file, two test suites, and a mismatch is a red test in whichever language
changed.

A second Go adopter must supply the same three things and nothing else. If it needs a
fourth — an act with an expiry, say — the act table grows in both languages in one
commit, or the mirror has drifted.
