# Plan — guest interactions, consent, and folding

Implements `docs/specs/guest-interactions-spec.md`. Every step ends with the command that
proves it, and the output that proves the command worked. Steps are ordered so that nothing
later is needed to verify anything earlier.

Baseline before step 1 (measured on this tree, `c145179`):
`make verify` exit 0 / 1028 Go tests; `make gates` exit 0 / 13 lines, 128 mutants killed;
`make e2e` exit 0 / 282 e2e tests; schema 27, 27 migrations, 81 tables.

---

## Step 1 — Migration `0027_guest_interactions.sql`

File: `internal/db/migrations/0027_guest_interactions.sql` (new).

```sql
-- Guests, consent, and the MAU read that closes the door.
--
-- A guest is a real users row. Everything a guest does already points at a
-- users.id (47 columns carry `REFERENCES users(id)`), so a parallel guests table
-- would mean a second owner column on all of them or a nullable one -- two
-- sources of truth for "who did this", which this schema has already paid for.
--
-- password_hash stays '' for a guest, which the existing code already reads as
-- "cannot log in" (0003's comment; Login refuses it explicitly).

ALTER TABLE users ADD COLUMN is_guest INTEGER NOT NULL DEFAULT 0;

-- When the visitor accepted the cookie notice. NULL = not accepted. Stamped on
-- the identity when there is one, so the record survives the browser.
ALTER TABLE users ADD COLUMN consented_at REAL;

-- The MAU read is COUNT(DISTINCT actor_id) over a 30-day window, on the request
-- path of every guest write. Without this index that is a full scan of the audit
-- log each time.
CREATE INDEX IF NOT EXISTS idx_audit_actor_created ON audit_log(actor_id, created_at);

-- Guests are looked up by this flag when the operator surface counts them.
CREATE INDEX IF NOT EXISTS idx_users_is_guest ON users(is_guest);
```

Then update the two places that state the schema's shape, because both are gates:

- `scripts/check_doc_numbers.py`: the migration count `26` → `27` (wherever it asserts it).
- `docs/REMAINING.md`: the same number and the table count if it moved (it does not; nothing
  is created or dropped).

Verify:

```
cd /home/alvaro/code/projects/concord
rm -f /tmp/g1.db && CONCORD_DB=/tmp/g1.db ./bin/concord -migrate-only 2>&1 | tail -2
sqlite3 /tmp/g1.db "SELECT COUNT(*) FROM schema_migrations;"           # expect 27
sqlite3 /tmp/g1.db "PRAGMA table_info(users);" | grep -c is_guest      # expect 1
sqlite3 /tmp/g1.db "PRAGMA index_list(audit_log);" | grep -c idx_audit_actor_created  # expect 1
/usr/bin/python3 scripts/check_doc_numbers.py                          # expect: all N numbers agree
```

Expected: `27`, `1`, `1`, and the doc gate's success line. If `-migrate-only` does not exist,
use `CONCORD_DB=/tmp/g1.db ./bin/concord -selfcheck` or the migration runner the tests use
(`internal/db` `Migrate`); the point is a fresh DB at 27.

---

## Step 2 — Store: `internal/store/guest.go` (new) + tests

### 2a. Constants and status

```go
package store

// mauWindowDays is the "M" in MAU, as a constant rather than a setting: it is the
// definition of the word, not a policy knob. The configurable number is the limit.
const mauWindowDays = 30

// GuestStatus is what the UI and the operator surface read.
type GuestStatus struct {
	// Allowed is false when the limit is reached or guests are switched off.
	Allowed bool `json:"allowed"`
	// MAU is distinct accounts with a recorded action inside the window.
	MAU int `json:"mau"`
	// Limit is the configured threshold; 0 means guests are switched off.
	Limit int `json:"limit"`
	// WindowDays is the window MAU was counted over.
	WindowDays int `json:"window_days"`
	// Guests is how many guest identities exist, and StaleGuests how many were
	// created more than WindowDays ago and never folded -- the operator's number
	// for the retention decision this pass deliberately does not make.
	Guests      int `json:"guests"`
	StaleGuests int `json:"stale_guests"`
}
```

### 2b. MAU and the door

```go
// MonthlyActiveUsers counts distinct accounts with a recorded action inside the
// window. Guests are excluded: the limit says "this instance is big enough to ask
// people to sign up", and counting the guests themselves would let the gate
// satisfy itself.
//
// audit_log is the source because every write path in the product already passes
// through it (58 call sites). Union'ing the activity tables instead would be more
// "complete" and worse: that list grows with every migration and would undercount
// silently the first time someone forgot.
func (d *DB) MonthlyActiveUsers(ctx context.Context) (int, error) {
	since := float64(time.Now().AddDate(0, 0, -mauWindowDays).Unix())
	var n int
	err := d.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT a.actor_id)
		FROM audit_log a
		JOIN users u ON u.id = a.actor_id
		WHERE a.actor_id IS NOT NULL AND a.actor_id > 0
		  AND a.created_at >= ? AND u.is_guest = 0`, since).Scan(&n)
	return n, err
}
```

### 2c. Minting a guest

```go
// CreateGuest makes an identity that can act but cannot log in, and returns it
// with a token exactly like RegisterUser. The token is the identity's only
// credential and is returned once.
func (d *DB) CreateGuest(ctx context.Context) (User, string, error) {
	for attempt := 0; attempt < 5; attempt++ {
		name := "guest-" + randomHex(4)
		now := float64(time.Now().Unix())
		res, err := d.ExecContext(ctx,
			`INSERT INTO users (username, display_name, password_hash, created_at, is_guest)
			 VALUES (?, 'guest', '', ?, 1)`, name, now)
		if isUniqueViolation(err) {
			continue // astronomically unlikely; retry rather than fail
		}
		if err != nil {
			return User{}, "", err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return User{}, "", err
		}
		tok, err := d.issueToken(ctx, id)
		if err != nil {
			return User{}, "", err
		}
		u, err := d.GetUserByID(ctx, id)
		return u, tok, err
	}
	return User{}, "", fmt.Errorf("could not find a free guest name")
}
```

`randomHex(n)` and `GetUserByID` are small helpers; if `GetUserByID` does not exist yet it is
`SELECT id, username, display_name, role, created_at FROM users WHERE id = ?` (copy the scan
shape from `GetUser`), and `randomHex` is `rand.Read` + `hex.EncodeToString`.

### 2d. The fold

```go
// foldColumns is every column in the schema that carries a user id.
//
// It is 47 `REFERENCES users(id)` columns, plus two that carry a user id without
// a foreign key:
//
//   - audit_log.actor_id, which has no FK on purpose (the log outlives the row it
//     names), and is the reason folding is auditable after the fact;
//   - maintenance_signals.set_by.
//
// This list is NOT maintained by hand. TestEveryColumnThatCarriesAUserIsFolded
// derives the real list from the schema and fails when a column exists that this
// does not name, so a migration that adds one cannot silently leave a guest's row
// behind.
var foldColumns = []struct{ Table, Column string }{
	{"admin_ledger", "actor_id"},
	{"api_tokens", "user_id"},
	{"audit_log", "actor_id"},
	{"capability_assertions", "asserted_by"},
	{"capability_confirmations", "user_id"},
	{"comment_votes", "user_id"},
	{"comments", "author_id"},
	{"complaint_impacts", "user_id"},
	{"complaints", "author_id"},
	{"consensus_calls", "opened_by"},
	{"criteria", "created_by"},
	{"criteria_profiles", "created_by"},
	{"criterion_votes", "voter_id"},
	{"emergency_holds", "held_by"},
	{"features", "author_id"},
	{"field_report_responses", "user_id"},
	{"field_reports", "removed_by"},
	{"field_reports", "user_id"},
	{"invite_redemptions", "user_id"},
	{"list_entries", "proposed_by"},
	{"list_entry_votes", "voter_id"},
	{"lists", "created_by"},
	{"maintenance_signals", "set_by"},
	{"members", "user_id"},
	{"merge_approvals", "user_id"},
	{"merge_requests", "author_id"},
	{"merge_requests", "closed_by"},
	{"objections", "user_id"},
	{"override_positions", "user_id"},
	{"override_windows", "opened_by"},
	{"pairwise_votes", "voter_id"},
	{"positions", "user_id"},
	{"project_documents", "author_id"},
	{"project_invites", "created_by"},
	{"project_tags", "applied_by"},
	{"reputation_events", "user_id"},
	{"request_answer_votes", "voter_id"},
	{"request_answers", "proposed_by"},
	{"requests", "author_id"},
	{"solution_coverage", "contested_by"},
	{"solutions", "author_id"},
	{"strategic_themes", "set_by"},
	{"strategic_weight_consents", "user_id"},
	{"strategic_weight_proposals", "proposed_by"},
	{"tag_aliases", "created_by"},
	{"taxonomy_consents", "user_id"},
	{"taxonomy_proposals", "proposed_by"},
	{"trust_grants", "granted_by"},
	{"trust_grants", "user_id"},
}
```

```go
// FoldGuestInto moves everything guestID produced onto accountID, then removes the
// guest identity. One transaction: a half-folded guest is an identity whose votes
// are in two places, which is worse than either end state.
//
// The account keeps its own history and gains the guest's. Nothing is copied: the
// rows change owner, which is what "the data is folded into the account" means.
func (d *DB) FoldGuestInto(ctx context.Context, guestID, accountID int64) error {
	if guestID == 0 || accountID == 0 || guestID == accountID {
		return fmt.Errorf("%w: fold needs two distinct identities", ErrInvalid)
	}
	g, err := d.GetUserByID(ctx, guestID)
	if err != nil {
		return err
	}
	if !g.IsGuest {
		return fmt.Errorf("%w: user %d is not a guest", ErrInvalid, guestID)
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range foldColumns {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET %s = ? WHERE %s = ?`, c.Table, c.Column, c.Column),
			accountID, guestID); err != nil {
			return fmt.Errorf("fold %s.%s: %w", c.Table, c.Column, err)
		}
	}
	// The guest's tokens are the account's now only in the sense that they are
	// dead: the browser holds an account token after login, and leaving a live
	// token on a deleted row would be a credential with no owner.
	if _, err := tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE user_id = ?`, guestID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ? AND is_guest = 1`, guestID); err != nil {
		return err
	}
	return tx.Commit()
}
```

```go
// PromoteGuest turns a guest identity into an account in place. Every row the
// guest produced already points at this id, so registration with a guest token
// copies nothing, orphans nothing, and keeps the history one identity's: "this
// account took these positions while it had no name".
func (d *DB) PromoteGuest(ctx context.Context, guestID int64, username, displayName, password string) (User, string, error) {
	g, err := d.GetUserByID(ctx, guestID)
	if err != nil {
		return User{}, "", err
	}
	if !g.IsGuest {
		return User{}, "", fmt.Errorf("%w: user %d is not a guest", ErrInvalid, guestID)
	}
	if !validUsername(username) {
		return User{}, "", fmt.Errorf("%w: username must be lowercase alnum with - _ .", ErrInvalid)
	}
	if err := validPassword(password); err != nil {
		return User{}, "", err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return User{}, "", err
	}
	_, err = d.ExecContext(ctx,
		`UPDATE users SET username = ?, display_name = ?, password_hash = ?, is_guest = 0
		 WHERE id = ? AND is_guest = 1`, username, displayName, hash, guestID)
	if isUniqueViolation(err) {
		return User{}, "", fmt.Errorf("%w: user %q", ErrDuplicate, username)
	}
	if err != nil {
		return User{}, "", err
	}
	u, err := d.GetUserByID(ctx, guestID)
	if err != nil {
		return User{}, "", err
	}
	tok, err := d.issueToken(ctx, guestID)
	if err != nil {
		return User{}, "", err
	}
	return u, tok, nil
}
```

### 2e. Consent

```go
// RecordConsent stamps the identity's acceptance. The cookie is the gate the
// request path reads; this is the record that survives the browser, so "who
// accepted, and when" is answerable without asking the visitor's laptop.
func (d *DB) RecordConsent(ctx context.Context, userID int64) error {
	if userID == 0 {
		return fmt.Errorf("%w: consent needs an actor to record against", ErrInvalid)
	}
	_, err := d.ExecContext(ctx, `UPDATE users SET consented_at = ? WHERE id = ?`,
		float64(time.Now().Unix()), userID)
	return err
}
```

### 2f. Tests — `internal/store/guest_test.go` (new)

Each test states its claim, and each was chosen so a wrong implementation fails it:

1. `TestAGuestHasNoPasswordAndCannotLogIn` — `CreateGuest` then `Login(username, "")` and
   `Login(username, "hunter2hunter2")` both `ErrAuth`; `SELECT password_hash` is `''`.
2. `TestMonthlyActiveUsersCountsAccountsAndNotGuests` — three accounts write (via any audited
   store call), two guests write; MAU is 3, and drops to 2 when one account's audit rows are
   older than the window. The window assertion inserts a row at `now - 31d` directly.
3. `TestRegisteringFoldsTheGuestsWork` — guest files 2 complaints; `PromoteGuest`; the complaints'
   `author_id` is unchanged and the row's `is_guest` is 0; counting complaints by the new account
   returns 2, by guest name returns 0.
4. `TestLoggingInFoldsTheGuestsWork` — guest casts 3 positions as a member; `FoldGuestInto` to an
   existing account; positions now `user_id = account`, and the count read back through the
   ordinary reader (`TallyConsensus` or the positions reader) equals the pre-fold count. Also
   asserts the guest row is gone and its tokens are gone.
5. `TestEveryColumnThatCarriesAUserIsFolded` — **the completeness gate.** Derives the real list
   from the migrated test DB: every `PRAGMA foreign_key_list(t)` row pointing at `users`, plus
   every column matching `(?i)(^|_)(user|actor|author|voter|assignee|…)_?id$|_by$`, minus an
   explicit exemption list of columns that merely *look* like user ids (`charters.
   solution_call_min_voters`). Asserts set equality with `foldColumns` **both ways**, so both a
   missing column and a stale one fail.
6. `TestFoldingIsRefusedForANonGuest` and `TestPromotingAUserThatIsNotAGuestIsRefused` — the
   guards, because `FoldGuestInto` is reachable from a request body.
7. `TestTheFoldIsAllOrNothing` — make one fold statement fail (drop a table inside the test's
   own transaction scope, or pass a guest id whose rows violate a UNIQUE), assert no table
   changed.

Verify:

```
go test ./internal/store/ -run 'Guest|MonthlyActive|Fold|Promote|Consent|EveryColumn' -count=1 -v 2>&1 | tail -30
```
Expected: every named test PASS, 0 FAIL.

Prove the completeness gate can fail (it is the one gate in this plan whose failure mode is
silence):

```
# temporarily add a column the fold does not name, and watch the gate go red
go test ./internal/store/ -run TestEveryColumnThatCarriesAUserIsFolded -count=1   # PASS
# then, in a scratch copy of 0027: CREATE TABLE scratch_owner(id INTEGER REFERENCES users(id));
go test ./internal/store/ -run TestEveryColumnThatCarriesAUserIsFolded -count=1   # FAIL, names it
# revert the scratch migration
```
Record both outputs in the commit message; a gate nobody has seen fail is decoration.

---

## Step 3 — HTTP: routes, the consent gate, and the guest door

File: `internal/httpapi/guest_handlers.go` (new), plus edits to `server.go`.

### 3a. Config

```go
// GuestMAULimit is the configured threshold. 0 switches guest interactions off
// entirely; negative is treated as 0. Read once at Start, like the other
// CONCORD_* variables, so the value cannot change under a running request.
const guestMAULimitEnv = "CONCORD_GUEST_MAU_LIMIT"
const consentVersionEnv = "CONCORD_CONSENT_VERSION"
```

`Server` gains `GuestMAULimit int` and `ConsentVersion string`, set in the constructor
(`internal/httpapi/server.go`, `New(...)`) from the environment with defaults `1000` and `"1"`.

### 3b. The middleware

```go
// consentGate refuses a mutating request that has not accepted the cookie notice.
//
// Consent is a gate, not a credential: it permits, and identity still comes from
// the bearer token, so a page that plants this cookie gains nothing.
//
// Exemptions, each with a reason:
//   - POST /api/v1/consent  -- the gate's own door
//   - POST /api/v1/auth/*   -- establishing or ending an identity is not an
//     interaction, and hiding registration behind a cookie banner is a dark
//     pattern
//   - POST /api/v1/hooks/forgejo -- server-to-server, authenticated by a shared
//     secret; no browser, no cookie
func (s *Server) consentGate(next http.Handler) http.Handler
```

It reads `r.Method` (GET/HEAD/OPTIONS pass), the path (exempt list), and the cookie
`s.consentCookieName` == `s.ConsentVersion`. On refusal:

```json
{"error":"cookies must be accepted before interacting with this site","consent":"required"}
```
with `403`.

### 3c. Routes

```go
r.Post("/api/v1/consent", s.handleAcceptConsent)   // sets the cookie, stamps the actor
r.Get("/api/v1/guest", s.handleGuestStatus)        // public status for the notice and operator
r.Post("/api/v1/guest", s.handleClaimGuest)        // mints a guest identity + token
```

- `handleAcceptConsent` sets the cookie (`HttpOnly`, `SameSite=Lax`, `Secure` when
  `r.TLS != nil || X-Forwarded-Proto: https`, `MaxAge: 365d`), calls `RecordConsent` when
  `getActorID(r) != 0`, writes an audit entry, and answers
  `{"ok":true,"consent":<version>}`.
- `handleGuestStatus` answers `store.GuestStatus` plus `consent_version` and `consented`
  (from the cookie) — read-only, no gate.
- `handleClaimGuest` requires consent (the middleware already did) and refuses when
  `!status.Allowed`:

```go
if _, err := s.store.MonthlyActiveUsers(r.Context()); ...
if status.Limit <= 0 || status.MAU >= status.Limit {
	writeJSON(w, http.StatusForbidden, map[string]any{
		"error": "this instance has grown past its guest limit; create an account to take part",
		"code":  "guest_limit_reached", "mau": status.MAU, "limit": status.Limit,
	})
	return
}
```

### 3d. The guest write surface

A new helper beside `requireEligibleCollaborator`:

```go
// requireAccount refuses a write that needs a nameless party to be namable.
// Guests may do everything else an unprivileged actor may do; §8.1/§8.2 role and
// eligibility gates are untouched and refuse a guest on eligibility, not on
// guesthood.
func (s *Server) requireAccount(w http.ResponseWriter, r *http.Request, why string) bool
```

and a guest check used by every write that a guest may perform:

```go
// guestRefusal answers whether this write must be refused because the actor is a
// guest and guests are closed. Checked on EVERY guest write, not only at mint:
// mint-only would let an instance cross the limit and keep serving guests.
func (s *Server) guestRefusal(r *http.Request) (string, bool)
```

Applied to: `handleCreateProject`, `handleCreateInvite`, `handleRevokeInvite` (account required),
and to every other mutating handler through the gate below.

Route registration: `r.Use(s.consentGate)` in `Router()` after `s.authenticate` (it needs the
actor to stamp consent) and before `noStoreAPI`.

### 3e. Tests — `internal/httpapi/guest_handlers_test.go` (new)

1. `TestAPostWithoutConsentIsRefusedAndSaysWhy` — 403, body has `"consent":"required"`.
2. `TestAGetWithoutConsentIsNotRefused` — reads are unaffected (`/api/v1/projects` 200).
3. `TestTheExemptRoutesStayExempt` — `POST /api/v1/auth/login`, `.../register`,
   `POST /api/v1/consent` and the forgejo hook answer non-403 without the cookie.
4. `TestAGuestMayFileAComplaint` (consent cookie set) — 200/201, and the complaint's author is a
   guest row.
5. `TestAGuestMayNotCreateAProject` — 403, message names the account requirement.
6. `TestGuestsStopAtTheLimit` — build a server with `GuestMAULimit: 2`, make MAU 2, assert
   `POST /api/v1/guest` is 403 with `guest_limit_reached`, and that an *existing* guest's write is
   also refused. Then lower the limit to 0 and assert `GET /api/v1/guest` reports `allowed:false`.
7. `TestRegisteringWithAGuestTokenFoldsIt` — register with the guest's Bearer; the guest's
   complaints are the new account's, the username is the chosen one, and the guest name is gone.
8. `TestLoggingInFoldsTheGuestToken` — login with the guest's Bearer present; fold happens, the
   response is the account's token.
9. `TestConsentIsRecordedAgainstTheActor` — accept while authenticated; `users.consented_at` set.
10. `TestTheConsentCookieIsNotACredential` — a request with ONLY the consent cookie (no token) is
    still refused on a route that needs an actor (401/403 by the existing auth path), proving the
    cookie permits but does not authorize.

Verify:

```
go test ./internal/httpapi/ -run 'Consent|Guest|Fold' -count=1 2>&1 | tail -5
```
Expected: `ok ... internal/httpapi`.

Then update `readerInventory` in `reader_coverage_test.go` with rows for the three new routes
(`Routes: 3` under a `/guest`+`/consent` row read by `consent.js`), and run:

```
go test ./internal/httpapi/ -run ReaderCoverage -count=1 2>&1 | tail -5
```
Expected: PASS. (The inventory matches by substring; a new route with no row fails the test,
which is the point.)

---

## Step 4 — The notice, the client guard, and the page state

Files: `internal/httpapi/assets/js/consent.js` (new), `assets/css/style.css`,
`templates/base.html`, `assets/js/session.js`, `internal/httpapi/server.go` (the page data
struct).

1. `base.html`: after `<body>`, `{{if not .ConsentAccepted}}<div id="cookie-notice" role="dialog"
   aria-live="polite" ...>…</div>{{end}}`, and `<body data-consent="{{if .ConsentAccepted}}accepted
   {{else}}required{{end}}" data-consent-version="{{.ConsentVersion}}">`. `pageData` gains
   `ConsentAccepted bool` and `ConsentVersion string`, filled in `s.page(...)` from the request's
   cookie. Every page therefore carries the state without a fetch.
2. `consent.js`: on Accept → `POST /api/v1/consent`; on success set `data-consent="accepted"`,
   remove the notice, and dispatch a `concord:consent` event. It also renders the notice's reason
   from `GET /api/v1/guest` when the visitor is a guest-to-be.
3. `session.js`'s request helper: refuse to send a mutating request while
   `document.body.dataset.consent !== "accepted"`, returning the same message the server would —
   a courtesy, because the server is the authority. The notice element is included in
   `base.html` for *every* page, so a write from any page meets the same guard.
4. The guest path: the first mutating action with `data-consent="accepted"` and no token calls
   `POST /api/v1/guest`, stores the returned token exactly where the account token goes, and
   retries once. On `guest_limit_reached` the UI says so and offers the register link.

Verify (the client is not the gate, so this is asserted against the server):

```
cd /home/alvaro/code/projects/concord && go build -o bin/concord ./cmd/concord
/usr/bin/python3 -m pytest tests/e2e/guest_e2e.py -q --no-header 2>&1 | tail -5
```
Expected (after step 5's e2e file exists): all pass.

---

## Step 5 — e2e and UI mutants

File: `tests/e2e/guest_e2e.py` (new). Tests, each with its own `X-Forwarded-For` (the rate
limiter's bucket is per client), mirroring the existing suite's fixtures:

1. `test_the_notice_is_shown_until_accepted` — fresh context; `#cookie-notice` visible; a write
   attempt is refused with the same sentence; click Accept; the notice disappears; the write
   succeeds.
2. `test_a_guest_can_act_without_an_account` — accept, file a complaint from the UI, assert the
   complaint appears with author "guest".
3. `test_the_guest_is_refused_at_the_limit` — start the server for this test with
   `CONCORD_GUEST_MAU_LIMIT=0`; assert the notice says guest interaction is closed and the
   complaint button explains why instead of failing silently.
4. `test_registering_keeps_the_guests_complaint` — file as a guest, register through the UI, and
   assert the complaint is listed among the account's own.

`tests/e2e/guest_ui_mutants.py` (new), same shape as `consensus_ui_mutants.py` (which shares
`ui_mutants.py`), mutants that must each be killed:

- the notice is never rendered (`{{if not .ConsentAccepted}}` → always false)
- the accept handler does not set the cookie
- the client guard is removed from the request helper
- `guest_limit_reached` is rendered as a generic error

Verify:

```
/usr/bin/python3 tests/e2e/guest_ui_mutants.py 2>&1 | tail -8
```
Expected: `4/4 killed, 0 survived, 0 not applicable, 0 not run` and
`source restored byte-identical: True`.

---

## Step 6 — Docs, full verification, deploy

1. Config documentation for `CONCORD_GUEST_MAU_LIMIT` (default 1000, 0 = off) and
   `CONCORD_CONSENT_VERSION` (default 1): what MAU counts, what a guest may do, the exemptions.
   **This step named `docs/CONFIGURATION.md`, which does not exist** — there is no such file in
   this tree, and the environment facts live in `docs/HANDOFF.md`'s "Environment facts" section.
   The plan was written from memory of another project; the number of CONCORD_* variables was
   checked by grep before writing the section, which is what caught it.
2. `docs/REMAINING.md`: a new section recording this pass — the three claims, the decisions of
   the spec's §4, the fold's column count, the numbers after the change, and the open questions
   of the spec's §8 with their defaults.
3. `docs/HANDOFF.md`: the deploy note (migration 27 applied on both instances, and that a guest
   who was mid-session before the deploy simply gets a new identity).
4. `scripts/check_doc_numbers.py`: keep every number it asserts in agreement (this is a gate, not
   a formality).

```
make verify && make gates && make e2e
```
Expected: all three exit 0, with the new counts (Go tests 1004 → ~1025, e2e 277 → 281, schema 27,
mutants 16 → 20) and 0 failures, 0 survivors.

Then deploy both instances per `docs/HANDOFF.md` §"deploy" (make build first — the harness shares
`bin/concord`), and confirm:

```
curl -s https://concord.polarisocial.xyz/api/v1/guest | head -c 200
curl -s https://concord.polarisocial.xyz/api/v1/version
```
Expected: `{"allowed":true,"mau":N,"limit":1000,"window_days":30,...}` and the new version string.

---

## Order of work, and what would make me stop

1 → 2 → 3 → 4 → 5 → 6, each verified before the next. If the completeness gate of step 2f cannot
be made to fail, step 2 is not done. If the e2e suite cannot show the server refusing a write
with no consent cookie, step 4 is not done — the client guard is a courtesy and must never be the
thing the test asserts.

---

## Where this plan met reality

Recorded because a plan that only ever records the intended path cannot be used to judge the next
one, and because three of the differences below are the substance of the pass.

1. **Step 4's expected counts were wrong in the small and right in the kind.** The plan guessed
   "Go tests 1004 → ~1025, e2e 277 → 281, schema 27, mutants 16 → 20". Actual: 1028 Go tests, 282
   e2e in 20 suite files, 23/23 UI mutants (7 audit.js, 9 consensus.js, 5 consent.js, 2 auth.js),
   13 Go mutation gates armed. The guesses were close because they were guesses about the SIZE of
   work already done; the numbers a gate reads are the ones that matter.
2. **Two defects were found by the BROWSER, after every Go gate was green, and both made the fold
   unreachable.** `auth.js` sent any visitor holding a token away from `/register` — and a guest
   holds a token, because that is how it acts — so the one visitor who needed the form was bounced
   off it. And the register form never presented the guest's token, which is the ONLY thing that
   tells the server there is anything to fold. Neither is visible from the server side: the
   endpoints were correct, complete and tested. Both now have mutants (2 in `auth.js`), and the
   e2e test drives the REAL form rather than a hand-made fetch, which is what would have hidden it.
3. **The step 4 client rule needed a third state the plan did not have.** The plan said the client
   refuses a write locally when the notice is unaccepted. What it did not consider is the visitor
   who accepts and immediately acts: the acceptance is in flight, not absent, and refusing that
   request is the client telling a lie about the visitor's own state. `consent.js` now waits for an
   in-flight acceptance (`pendingAccept`) and rejoins the routing afterwards.
4. **The mutation gate reported its own weakness first, and that is the point.** The first run of
   `tests/e2e/guest_ui_mutants.py` printed `3/5 killed, 1 survived, 1 not applicable`: one pattern
   no longer matched the line it was written against (an inserted `pendingAccept = ` moved it), and
   one mutant SURVIVED because the sentence it mutated is spoken in two places, so mutating one left
   the other saying the same thing to the visitor. The fix was to mutate the rule both places share
   (the refusal must offer the way forward), not to weaken the test. A gate that reports 5/5 when
   one mutant was never applied is the failure this whole file's §"what would make me stop" is about.
5. **The plan's claim about the fold's column count was right and its list was not.** The gate
   caught the first hand-written list naming `maintenance_signals.set_by` — `TEXT`, an operator's
   name, not a `users.id`. Deriving the set from the schema is what made the difference, and it was
   proven to fire with a scratch table before it was trusted.
