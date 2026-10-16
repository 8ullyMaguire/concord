# Guest interactions, consent, and folding — spec

Status: draft for implementation, 2026-10-16. Target: Concord `main` after `c145179`.
Companion: `docs/PLAN-guest.md` (the implementation plan and its per-step gates).
Related: `docs/concord-spec-r4.md` §6 (consensus), §8.1/§8.2 (roles and eligibility),
§9 (data model); `docs/specs/consensus-page-spec.md`, `docs/specs/frontend-spec.md`.

---

## 1. The request, restated as three testable claims

1. **A visitor who is not signed in can interact** — cast a stance, file a complaint, comment,
   vote — without creating an account, and can keep doing so until the instance has grown to a
   configurable number of monthly active users (default **1000**), after which guest
   interactions stop.
2. **When a guest later creates an account, the data they produced as a guest becomes that
   account's data** — the same rows, the same history, no copy and no orphan.
3. **A cookie notice is shown, and interactions are refused until it is accepted.**

Each claim is checkable on its own, and each is checked by a named test in the plan. A claim
that cannot be checked is not in this spec.

## 2. Definitions, so the words in the code mean one thing

- **Guest** — an identity with a real `users.id` and `is_guest = 1`. It can act; it cannot log
  in (empty password hash, which `RegisterUser` and `LoginUser` already treat as "cannot log
  in"), and it is not a member of anything.
- **Account** — a `users` row with `is_guest = 0`: what registering produces.
- **MAU** — distinct **account** ids with at least one recorded action in the last 30 days,
  read from `audit_log`. Guests do not count: the threshold exists to say "this instance is big
  enough to ask people to sign up", and counting the guests themselves would make the gate
  satisfy itself.
- **Consent** — the visitor has accepted the cookie notice. Held as a cookie and, when an
  identity exists, written to `users.consented_at`.
- **Fold** — moving everything a guest identity produced onto another identity, so the rows are
  the account's and the guest row is gone.

## 3. What already exists (verified in the tree, not assumed)

- `users(id, username UNIQUE, display_name, created_at)` (0001) + `password_hash TEXT NOT NULL
  DEFAULT ''` (0003). `''` means "cannot log in" — that comment is already in the migration.
- Sessions are `api_tokens(token_hash PRIMARY KEY, user_id, created_at)`; the raw token is
  returned exactly once by `RegisterUser`/`LoginUser` and stored only as SHA-256.
- `navCookieName = "concord_nav"` is accepted **only** on a `GET` whose path is not under
  `/api/` (`navToken` in `internal/httpapi/auth.go`). The comment states the property: a cookie
  must never authorize a write, because every mutating route is an API route.
- `authenticate` puts the actor on the context; `getActorID(r) == 0` is the "no actor" case, and
  every write already fails closed on it.
- Config is environment-only: four `CONCORD_*` variables today (`CONCORD_DB`, `CONCORD_BASE_URL`,
  `CONCORD_FORGEJO_SECRET`, `CONCORD_TRUST_LEVEL_MIN`). There is no instance-settings table.
- `audit_log` carries `actor_id` with **no** foreign key (deliberate: the log outlives the row it
  names), 2,313 rows on the live instance, all with a non-null actor.
- Eligibility (§8.2) is `requireEligibleCollaborator`: member **and** a qualifying contribution
  inside the activity window. It is the reason a guest cannot vote in a consensus call, and it
  stays exactly as it is — guests do not relax it.
- 47 columns in the live schema carry a `REFERENCES users(id)`; `audit_log.actor_id` and
  `maintenance_signals.set_by` carry a user id without one.
- The reader inventory (`readerInventory` in `internal/httpapi/reader_coverage_test.go`) matches a
  route path by **substring** and requires either a page reader with an anchor or an exempt row
  with a reason.

## 4. Decisions

### D1 — A guest is a `users` row, and its identity arrives as a token, never a cookie

`POST /api/v1/guest` creates `users(is_guest=1, username='guest-<8 hex>', display_name='guest',
password_hash='')` and returns a Bearer token exactly like `RegisterUser` does.

Why a real row: everything a guest does is already a row whose owner is a `users.id` (48
columns). A parallel `guests` table would mean either a second foreign key on all of them, or a
nullable owner on each — both are the "two sources of truth for who someone is" defect this
codebase has already paid for twice.

Why a token and not a cookie: the property in §3 ("a cookie never authorizes a write") is
load-bearing against CSRF and has a test. Minting guest identity into a cookie would delete that
property to save one fetch. The guest token lives in the same place the account token lives
(`localStorage`, attached by `authHeaders()`), so every existing write path works unchanged.

Rejected: an ambient "anonymous session" cookie accepted on writes. It would be a CSRF hole
(a third-party page could make a visitor cast stances) and it would make guest identity
unrevocable.

### D2 — Consent is a gate, and it is a cookie because it is a gate

`POST /api/v1/consent` sets `concord_consent=1` (HttpOnly, `SameSite=Lax`, `Secure` when the
request is HTTPS — the same shape as `concord_nav`) and, when the request carries an actor,
stamps `users.consented_at` and writes an audit entry.

`consentGate` middleware refuses a **mutating** request without it: `403` with
`{"error": "cookies must be accepted before interacting (...)","consent":"required"}`. Exemptions,
each with a reason:

| route | why exempt |
|---|---|
| `POST /api/v1/consent` | it is the gate's own door |
| `POST /api/v1/auth/*` | establishing or ending an identity is not an interaction with the project's data, and gating registration behind a cookie banner is a dark pattern |
| `POST /api/v1/hooks/forgejo` | server-to-server, authenticated by a shared secret, no browser and no cookie |

Consent does **not** authorize anything: it only permits. An attacker who plants the cookie gains
nothing, because identity still comes from the Bearer token. That is also why the cookie is safe
to render into the page state (the toast is rendered server-side and disappears once accepted).

Consent is per version: `CONCORD_CONSENT_VERSION` (default `1`). Changing the notice text means
bumping the version, which re-asks everyone. Stored value is the version they accepted.

### D3 — MAU, and the moment guests stop

```
MAU = SELECT COUNT(DISTINCT actor_id) FROM audit_log
      WHERE actor_id IS NOT NULL AND actor_id > 0 AND created_at >= ?   -- now - 30d
```

Union'ing the ~48 activity columns would be more "complete" and is worse: `audit_log` is written
by 58 call sites and is the one place every write already passes through; the others are a list
that will grow and silently undercount. What MAU means here is documented in the spec, tested,
and shown on the page the operator reads.

- `CONCORD_GUEST_MAU_LIMIT` (default `1000`), `0` disables guest interactions entirely.
- The window is 30 days, a named constant (`mauWindowDays`), not an environment variable: it is
  the definition of the word, not a policy knob.
- Checked at **both** ends: when minting a guest identity, and on every write by a guest. Mint-only
  would let an instance cross 1,000 and keep serving guests forever.

### D4 — What a guest may do: everything an unprivileged account may do, minus three things

(One more was decided when the browser found it: a guest may cast a criteria vote -- a
pairwise comparison that feeds a ranking -- but may NOT consent to a taxonomy proposal, which
counts into a quorum. The store now checks that the consenter is one of the eligible taggers it
divides by; see §23.11 of `docs/REMAINING.md` for the sweep and the criteria-vote reasoning.)

Guests get every write that requires nothing more than "an actor", because that is exactly what
they are. §8.1/§8.2 role and eligibility gates are untouched, so a guest still cannot cast a
stance in a consensus call, open a call, hold a call, or resolve an objection — those refuse on
eligibility, not on guesthood.

Three writes require a credentialed account and refuse for a guest with the reason attached:

1. `POST /api/v1/projects` — creating a project. A project is a namespace with a slug, a charter
   and a page; a nameless party holding namespaces is a squatting vector.
2. `POST …/invites` and `POST …/invites/{id}/revoke` — invites are the trust mechanism (§8.2
   membership), and a guest has no standing to extend trust.

Guests are additionally marked in the audit log (`users.is_guest` is read at audit time, so the
operator can filter guest activity), and every page whose reader shows an actor shows
`display_name`, so a guest renders as "guest" rather than as a synthetic handle.

### D5 — Folding: promote on register, repoint on login

**Register with a guest token present** → `PromoteGuest`: `UPDATE users SET username=?,
display_name=?, password_hash=?, is_guest=0 WHERE id=?`, mint a normal token, done. Every row the
guest produced already points at that id, so nothing is copied, nothing is orphaned, and the
history stays one identity's — "this account took these positions while it had no name".

**Login while holding a guest token** → `FoldGuestInto(guestID, accountID)`: repoint all **48**
user-carrying columns to the account, delete the guest's tokens, delete the guest row, audit it.
The account keeps its own history *and* gains the guest's.

The 48 columns are enumerated in the plan, and the plan's list is held by a completeness test:
`TestEveryColumnThatCarriesAUserIsFolded` derives the real list from `PRAGMA foreign_key_list` and
from the name pattern, and fails when a column exists that the fold does not handle. Rationale: a
fold that silently misses a column produces "my votes disappeared" months later, and a hand-kept
list rots within one migration.

Rejected: copying rows (duplicates the same content under two owners, and UNIQUE constraints make
it fail on exactly the rows that matter); leaving the guest row alive as an alias (no FK can
express it, so every reader would have to).

### D6 — The toast, and what a refused visitor can still do

The server renders the notice into `base.html` when the request has no accepted consent cookie.
Accepting posts to `/api/v1/consent` and hides it; the page then publishes
`<body data-consent="accepted">`. Refusing leaves the site fully readable — every read route is
public — and the notice stays until accepted or the visitor leaves.

The client also refuses early, and it does so in ONE place: `assets/js/consent.js` wraps
`window.fetch` at load, before any page script runs, so every request the product makes passes
through it whether or not the page author thought about the notice. (Written after the fact: the
first plan put this in `session.js`'s `authHeaders`/`apiFetch`, which only the pages that call
those helpers would have been covered by -- and ~25 page scripts do not.) It refuses with the same
sentence the server would have, and with the same `code` the server now sends
(`consent_required`). That is a UI courtesy, not the gate: the server is the authority, and the
e2e suite asserts the server refuses when the cookie is absent **even if the client tries**.

The notice itself says what a visitor can do, because the old copy explained the cookies and left
out the mechanic they were being asked to agree to: you may take part without an account, you act
as a nameless guest (a random name and a timestamp -- no address is recorded anywhere in this
path), and creating an account later moves what you did into it. A visitor who is told only about
cookies has been asked to agree to something that was not described.

### D7 — Display and counting of guests, and what the operator can see

- A guest renders as "guest" (`display_name`).
- The audit log's actor column shows the identity, guest or not, and the settings/admin surface
  gains one read-only line: current MAU, the limit, and whether guests are open.
- After folding, nothing distinguishes the rows: the guest identity is gone, which is the point.

## 5. Data model — migration `0027_guest_interactions.sql`

```sql
ALTER TABLE users ADD COLUMN is_guest INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN consented_at REAL;             -- NULL = not accepted yet
CREATE INDEX IF NOT EXISTS idx_audit_actor_created ON audit_log(actor_id, created_at);
CREATE INDEX IF NOT EXISTS idx_users_is_guest ON users(is_guest);
```

No new tables. No column is dropped. `is_guest` defaults to 0, so every existing row keeps its
meaning and every existing test keeps passing.

## 6. Surfaces

| method | path | gate | purpose |
|---|---|---|---|
| `POST` | `/api/v1/guest` | consent, MAU | mint a guest identity + token |
| `GET` | `/api/v1/guest` | none | `{allowed, mau, limit, window_days, guests, stale_guests, consent_version, consented}` for the UI and the operator |
| `POST` | `/api/v1/consent` | none (the gate's door) | accept the notice; sets the cookie, stamps the actor |
| `GET` | `/api/v1/maunot` — see §7 | — | not shipping |

`GET /api/v1/guest` is public and read-only: a page can tell the visitor whether guest interaction
is still open without attempting a write. It is the surface the toast's "why" comes from.
`guests` and `stale_guests` (guests older than the MAU window, never folded into an account) were
added for §7's retention decision: it needs a number, and a number nobody prints is a decision made
in the dark.

## 7. Explicitly not shipping, and why

- **Email verification, captcha, rate-limiting guests harder.** The rate limiter already applies
  per client address; a captcha needs a third-party dependency and a privacy decision that is the
  owner's, not mine. Recorded as an open question below.
- **Guest retention/GC.** A guest who never registers leaves 48-column-owned rows behind. The
  plan adds no sweeper; the honest statement is in the operator surface ("N guests, N of them
  older than 30 days") so the decision can be made with data.
- **Per-project guest policy.** Instance-wide, because the threshold is an instance-size signal.
  A per-project toggle would be a second config surface for no additional decision.
- **Migrating a guest's data into an account they did not create** (e.g. an admin merging
  identities). Folding is the guest's own act, from the browser that holds the token.

## 8. Open questions, each with the default I implemented

1. **Should a signed-in account also have to accept the notice to interact?** Default: **yes**,
   uniformly — a notice that applies to some visitors only is a notice nobody understands, and the
   requirement says "cookies need to be accepted to allow interactions".
2. **Does a guest's activity count toward MAU?** Default: **no** (§2). If the owner wants the
   threshold to mean "anyone at all", that is a one-line change to the query.
3. **Should guests be able to create projects?** Default: **no** (§D4), because a namespace is a
   name. If the owner wants guests to try the product end to end, this is the one exemption to
   revisit.
4. **Captcha before the limit is reached?** Default: **no** (noted in §7).

## 9. Verification

The plan (`docs/PLAN-guest.md`) carries the commands. Every row below names a test that
exists -- checked by reading the tree, not by memory, after the first version of this table
named six tests that had never been written.

| claim | test |
|---|---|
| a guest can interact without an account | `TestAGuestMayActWithoutAnAccount`, `TestAGuestHasNoPasswordAndCannotLogIn`, e2e `guest_e2e.py::test_a_guest_can_file_a_complaint_without_an_account` |
| guests stop at the limit | `TestGuestsStopAtTheLimit` (mint AND write, both directions, limit from the config), `TestMonthlyActiveUsersCountsAccountsAndNotGuests` |
| the operator can see where the door is | `TestGuestStatusReportsTheDoorAndTheOperatorsNumbers` |
| data folds on account creation | `TestRegisteringFoldsTheGuestsWork` (same id, same rows, the account can log in), `TestRegisteringWithAGuestTokenKeepsTheGuestsWork` |
| data folds on login | `TestLoggingInFoldsTheGuestsWork`, `TestLoggingInWithAGuestTokenFoldsTheWorkAndSaysSo` |
| a fold is all or nothing | `TestTheFoldIsAllOrNothing` |
| a collision is reported, not dropped | `TestARowThatCannotCoexistIsReportedNotSilentlyDropped` (count AND audit detail) |
| every user-carrying column is folded | `TestEveryColumnThatCarriesAUserIsFolded` (derives the set from the schema, fails BOTH ways) |
| the fold refuses a non-guest | `TestFoldingAndPromotingRefuseANonGuest` |
| consent is required for interactions | `TestAWriteWithoutConsentIsRefusedAndSaysWhy`, `TestConsentIsNotRequiredToRead` |
| the consent cookie is not a credential | `TestTheConsentCookieIsNotACredential` (cookie, no token, must refuse) |
| a route added later is gated | `TestEveryMutatingAPIRouteRefusesWithoutConsent` (walks the served router: 70 routes, each asked for a write with no consent) |
| consent is recorded against whoever accepted | `TestConsentIsRecordedAgainstTheIdentity`, `TestConsentIsRecordedAgainstTheAccountThatAcceptedIt` |
| the notice is shown, and says where the visitor stands | e2e `guest_e2e.py::test_the_notice_is_shown_and_disappears_when_accepted`, `test_an_interaction_is_refused_until_the_notice_is_accepted`, `TestThePageTellsTheVisitorWhetherTheNoticeIsAccepted` |
| a shut door is explained, not just refused | e2e `guest_e2e.py::test_a_visitor_is_told_when_the_guest_door_is_shut` (a second instance with the limit at 0) |
| a guest may not name a project or hand out trust | `TestAGuestMayNotCreateAProjectOrAnInvite` |
| the fold is REACHABLE from the browser | e2e `guest_e2e.py::test_the_guest_identities_work_survives_registration` (drives the real form) and `tests/e2e/guest_ui_mutants.py` (5 consent.js + 2 auth.js mutants, 7/7 killed) |

The last row exists because the Go suite was green while the fold was unreachable: `auth.js`
bounced any visitor holding a token away from `/register`, and a guest holds a token -- that is
how it acts. A mechanism can be entirely correct and entirely unreachable, and only a browser
says so (docs/REMAINING.md §23.6).
