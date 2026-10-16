package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// Guest interactions, consent, and folding.
//
// See docs/specs/guest-interactions-spec.md and docs/PLAN-guest.md. The tests here
// hold the three claims of §1 that live in the store: a guest can act without
// credentials, guests stop at the limit, and what a guest produced becomes the
// account's when they create one.
//
// The one test whose failure mode is silence is
// TestEveryColumnThatCarriesAUserIsFolded: a fold that misses a column does not
// fail, it loses rows quietly. That is why it derives its expectation from the
// schema instead of from this file.

func TestAGuestHasNoPasswordAndCannotLogIn(t *testing.T) {
	store := setup(t)
	ctx := context.Background()

	g, tok, err := store.CreateGuest(ctx)
	if err != nil {
		t.Fatalf("CreateGuest: %v", err)
	}
	if !g.IsGuest {
		t.Fatalf("a created guest is not marked as one: %+v", g)
	}
	if !strings.HasPrefix(g.Username, "guest-") {
		t.Errorf("guest username %q is not recognisable as a guest", g.Username)
	}
	if tok == "" || !strings.HasPrefix(tok, tokenPrefix) {
		t.Errorf("guest token %q is not a token", tok)
	}
	// The identity is usable: the token resolves to it.
	uid, err := store.ResolveToken(ctx, tok)
	if err != nil || uid != g.ID {
		t.Fatalf("ResolveToken(guest token) = %d, %v; want %d", uid, err, g.ID)
	}

	var hash string
	if err := store.QueryRowContext(ctx,
		`SELECT password_hash FROM users WHERE id = ?`, g.ID).Scan(&hash); err != nil {
		t.Fatalf("read password_hash: %v", err)
	}
	if hash != "" {
		t.Errorf("a guest has a password hash (%d bytes): it could be logged into", len(hash))
	}
	// Both halves of "cannot log in": the empty password, and any other password.
	for _, pw := range []string{"", "hunter2hunter2"} {
		if _, _, err := store.Login(ctx, g.Username, pw); !errors.Is(err, ErrAuth) {
			t.Errorf("Login(%q, %q) = %v; a guest must not be able to log in", g.Username, pw, err)
		}
	}
}

func TestAnAccountIsNotAGuestAndAGuestBecomesOneOnPromotion(t *testing.T) {
	store, uid := setupWithUser(t)
	ctx := context.Background()

	u, err := store.GetUserByID(ctx, uid)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if u.IsGuest {
		t.Fatalf("an account created by CreateUser is marked as a guest")
	}
	g, _, err := store.CreateGuest(ctx)
	if err != nil {
		t.Fatalf("CreateGuest: %v", err)
	}
	if isG, err := store.IsGuest(ctx, g.ID); err != nil || !isG {
		t.Fatalf("IsGuest(%d) = %v, %v; want true", g.ID, isG, err)
	}
	if isG, err := store.IsGuest(ctx, uid); err != nil || isG {
		t.Fatalf("IsGuest(%d) = %v, %v; want false", uid, isG, err)
	}
}

func TestMonthlyActiveUsersCountsAccountsAndNotGuests(t *testing.T) {
	store, authorID, projectID := setupWithProject(t)
	ctx := context.Background()

	// Three accounts act, two guests act. Only the accounts count.
	second := newUserNamed(t, store, "mau-second")
	third := newUserNamed(t, store, "mau-third")
	for _, uid := range []int64{authorID, second, third} {
		if err := store.AddAudit(ctx, projectID, uid, "acted", "project", projectID, ""); err != nil {
			t.Fatalf("AddAudit: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		g, _, err := store.CreateGuest(ctx)
		if err != nil {
			t.Fatalf("CreateGuest: %v", err)
		}
		if err := store.AddAudit(ctx, projectID, g.ID, "acted", "project", projectID, ""); err != nil {
			t.Fatalf("AddAudit(guest): %v", err)
		}
	}
	mau, err := store.MonthlyActiveUsers(ctx)
	if err != nil {
		t.Fatalf("MonthlyActiveUsers: %v", err)
	}
	if mau != 3 {
		t.Fatalf("MAU = %d; want 3 (three accounts acted, two of the five actors are guests)", mau)
	}

	// The window is real: the same query one day later than the data is 0.
	old := float64(time.Now().AddDate(0, 0, -(mauWindowDays + 1)).Unix())
	if _, err := store.ExecContext(ctx, `UPDATE audit_log SET created_at = ?`, old); err != nil {
		t.Fatalf("age the log: %v", err)
	}
	mau, err = store.MonthlyActiveUsers(ctx)
	if err != nil {
		t.Fatalf("MonthlyActiveUsers: %v", err)
	}
	if mau != 0 {
		t.Fatalf("MAU = %d after ageing every action past the window; want 0", mau)
	}
}

func TestGuestStatusReportsTheDoorAndTheOperatorsNumbers(t *testing.T) {
	store, authorID, projectID := setupWithProject(t)
	ctx := context.Background()

	if err := store.AddAudit(ctx, projectID, authorID, "acted", "project", projectID, ""); err != nil {
		t.Fatalf("AddAudit: %v", err)
	}
	g, _, err := store.CreateGuest(ctx)
	if err != nil {
		t.Fatalf("CreateGuest: %v", err)
	}
	// A guest older than the window, which is the operator's retention number.
	old := float64(time.Now().AddDate(0, 0, -(mauWindowDays + 5)).Unix())
	if _, err := store.ExecContext(ctx, `UPDATE users SET created_at = ? WHERE id = ?`, old, g.ID); err != nil {
		t.Fatalf("age the guest: %v", err)
	}

	st, err := store.GuestStatusFor(ctx, 1000)
	if err != nil {
		t.Fatalf("GuestStatusFor: %v", err)
	}
	if !st.Allowed {
		t.Errorf("guest interactions are closed at MAU %d / limit %d", st.MAU, st.Limit)
	}
	if st.MAU != 1 {
		t.Errorf("MAU = %d; want 1", st.MAU)
	}
	if st.Limit != 1000 || st.WindowDays != mauWindowDays {
		t.Errorf("status says limit %d window %d; want 1000 and %d", st.Limit, st.WindowDays, mauWindowDays)
	}
	if st.Guests != 1 || st.StaleGuests != 1 {
		t.Errorf("guests %d / stale %d; want 1 and 1", st.Guests, st.StaleGuests)
	}

	// The limit is the door: at the limit, and at 0 ("switched off"), it is shut.
	for _, limit := range []int{1, 0, -1} {
		st, err := store.GuestStatusFor(ctx, limit)
		if err != nil {
			t.Fatalf("GuestStatusFor(%d): %v", limit, err)
		}
		if st.Allowed {
			t.Errorf("guest interactions are open with MAU %d and limit %d; want closed", st.MAU, limit)
		}
	}
}

func TestRegisteringFoldsTheGuestsWork(t *testing.T) {
	store, uid, projectID := setupWithProject(t)
	ctx := context.Background()

	g, guestToken, err := store.CreateGuest(ctx)
	if err != nil {
		t.Fatalf("CreateGuest: %v", err)
	}
	// The guest files two complaints.
	for i := 0; i < 2; i++ {
		if _, err := store.CreateComplaint(ctx, projectID, g.ID,
			fmt.Sprintf("guest complaint %d", i), "body", 3, 0.5, 1.0); err != nil {
			t.Fatalf("CreateComplaint: %v", err)
		}
	}
	before := complaintsByAuthor(t, store, g.ID)
	if before != 2 {
		t.Fatalf("fixture: guest has %d complaints; want 2", before)
	}

	u, tok, err := store.PromoteGuest(ctx, g.ID, "promoted-guest", "Promoted", "hunter2hunter2")
	if err != nil {
		t.Fatalf("PromoteGuest: %v", err)
	}
	if u.ID != g.ID {
		t.Fatalf("promotion changed the id: %d -> %d; every row the guest owns points at the first", g.ID, u.ID)
	}
	if u.IsGuest {
		t.Errorf("the promoted identity is still marked a guest")
	}
	if u.Username != "promoted-guest" {
		t.Errorf("username = %q; want promoted-guest", u.Username)
	}
	if tok == "" {
		t.Errorf("promotion returned no token")
	}
	// The work did not move, because it did not have to: same owner, same rows.
	if after := complaintsByAuthor(t, store, g.ID); after != 2 {
		t.Errorf("the promoted account has %d complaints; want the 2 the guest filed", after)
	}
	// The guest token still works, and now resolves to the account.
	if uid2, err := store.ResolveToken(ctx, guestToken); err != nil || uid2 != u.ID {
		t.Errorf("ResolveToken(guest token after promotion) = %d, %v; want %d", uid2, err, u.ID)
	}
	// And the account can log in, which is the whole point of promoting rather
	// than folding: the identity keeps its history and gains a credential.
	if _, _, err := store.Login(ctx, "promoted-guest", "hunter2hunter2"); err != nil {
		t.Errorf("Login after promotion: %v", err)
	}
	_ = uid
}

func TestLoggingInFoldsTheGuestsWork(t *testing.T) {
	store, accountID, projectID := setupWithProject(t)
	ctx := context.Background()

	g, _, err := store.CreateGuest(ctx)
	if err != nil {
		t.Fatalf("CreateGuest: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := store.CreateComplaint(ctx, projectID, g.ID,
			fmt.Sprintf("guest complaint %d", i), "body", 3, 0.5, 1.0); err != nil {
			t.Fatalf("CreateComplaint: %v", err)
		}
	}
	// The token is a real credential, so it must not outlive the identity.
	if _, err := store.DB.ExecContext(ctx,
		`INSERT INTO api_tokens (token_hash, user_id, created_at, last_used_at) VALUES (?,?,?,?)`,
		"deadbeef", g.ID, 0.0, 0.0); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	rep, err := store.FoldGuestInto(ctx, g.ID, accountID)
	if err != nil {
		t.Fatalf("FoldGuestInto: %v", err)
	}
	if rep.Moved < 3 {
		t.Errorf("fold reported %d moved rows; the guest's 3 complaints at least must be among them", rep.Moved)
	}
	if rep.Superseded != 0 {
		t.Errorf("fold reported %d superseded rows; nothing collided in this fixture", rep.Superseded)
	}
	// The account owns them now, read back through the ordinary reader.
	if got := complaintsByAuthor(t, store, accountID); got != 3 {
		t.Errorf("the account has %d complaints; want the guest's 3", got)
	}
	// The guest is gone, and so is everything that pointed at it.
	if _, err := store.GetUserByID(ctx, g.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetUserByID(guest) after the fold = %v; the identity must be gone", err)
	}
	if _, err := store.ResolveToken(ctx, "clt_deadbeef"); !errors.Is(err, ErrAuth) {
		t.Errorf("a guest token survived the fold: %v", err)
	}
	var leftover int
	for _, c := range foldColumns {
		var n int
		q := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s = ?`, c.Table, c.Column)
		if err := store.QueryRowContext(ctx, q, g.ID).Scan(&n); err != nil {
			t.Fatalf("count %s.%s: %v", c.Table, c.Column, err)
		}
		leftover += n
	}
	if leftover != 0 {
		t.Errorf("%d rows still name the folded guest", leftover)
	}
}

func TestARowThatCannotCoexistIsReportedNotSilentlyDropped(t *testing.T) {
	store, accountID, projectID := setupWithProject(t)
	ctx := context.Background()

	g, _, err := store.CreateGuest(ctx)
	if err != nil {
		t.Fatalf("CreateGuest: %v", err)
	}
	// A comment both identities upvoted. comment_votes is keyed
	// (comment_id, user_id), so the guest's row cannot simply change owner.
	comp, err := store.CreateComplaint(ctx, projectID, accountID, "shared comment host", "body", 3, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	c, err := store.CreateComment(ctx, projectID, comp.ID, "complaint", accountID, "a comment", "question", nil)
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	for _, uid := range []int64{accountID, g.ID} {
		if err := store.VoteComment(ctx, c.ID, uid, 1); err != nil {
			t.Fatalf("VoteComment(%d): %v", uid, err)
		}
	}
	rep, err := store.FoldGuestInto(ctx, g.ID, accountID)
	if err != nil {
		t.Fatalf("FoldGuestInto: %v", err)
	}
	if rep.Superseded != 1 {
		t.Errorf("fold reported %d superseded rows; exactly the one colliding vote cannot move", rep.Superseded)
	}
	// The account's vote is still there and still the account's. The account's row
	// wins, so the value is the account's (1) and there is exactly one row.
	var n, value int
	if err := store.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(value),0) FROM comment_votes WHERE comment_id = ? AND user_id = ?`,
		c.ID, accountID).Scan(&n, &value); err != nil {
		t.Fatalf("read the surviving vote: %v", err)
	}
	if n != 1 || value != 1 {
		t.Errorf("comment_votes for the account = %d rows value %d; want 1 and 1", n, value)
	}
	// And the drop is on the record, not in the code's intentions.
	var detail string
	if err := store.QueryRowContext(ctx,
		`SELECT detail FROM audit_log WHERE actor_id = ? AND action = 'guest-followed-into-account'
		 ORDER BY id DESC LIMIT 1`, accountID).Scan(&detail); err != nil {
		t.Fatalf("read the fold's audit entry: %v", err)
	}
	if !strings.Contains(detail, `"superseded":1`) {
		t.Errorf("audit detail %q does not record the superseded row", detail)
	}
}

func TestTheFoldIsAllOrNothing(t *testing.T) {
	store, accountID, projectID := setupWithProject(t)
	ctx := context.Background()

	g, _, err := store.CreateGuest(ctx)
	if err != nil {
		t.Fatalf("CreateGuest: %v", err)
	}
	if _, err := store.CreateComplaint(ctx, projectID, g.ID, "guest complaint", "body", 3, 0.5, 1.0); err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	before := countComplaints(t, store, projectID)

	// Make a later fold statement impossible: the transaction must undo the ones
	// that already ran. This is the whole reason the fold is one transaction, so
	// the test drives the failure rather than asserting the comment.
	original := foldColumns
	t.Cleanup(func() { foldColumns = original })
	foldColumns = append(append([]struct{ Table, Column string }{}, original...),
		struct{ Table, Column string }{"no_such_table", "user_id"})

	if _, err := store.FoldGuestInto(ctx, g.ID, accountID); err == nil {
		t.Fatalf("the fold succeeded with an impossible statement in the list")
	}
	foldColumns = original

	if after := countComplaints(t, store, projectID); after != before {
		t.Errorf("complaints went %d -> %d across a failed fold; the transaction did not roll back", before, after)
	}
	if _, err := store.GetUserByID(ctx, g.ID); err != nil {
		t.Errorf("the guest was deleted by a failed fold: %v", err)
	}
	if got := complaintsByAuthor(t, store, g.ID); got != 1 {
		t.Errorf("the guest's complaint moved during a failed fold (%d)", got)
	}
}

func TestFoldingAndPromotingRefuseANonGuest(t *testing.T) {
	store, accountID := setupWithUser(t)
	ctx := context.Background()
	other := newUserNamed(t, store, "another-account")

	if _, err := store.FoldGuestInto(ctx, accountID, other); !errors.Is(err, ErrInvalid) {
		t.Errorf("FoldGuestInto(account) = %v; want ErrInvalid", err)
	}
	if _, _, err := store.PromoteGuest(ctx, accountID, "sneaky", "Sneaky", "hunter2hunter2"); !errors.Is(err, ErrInvalid) {
		t.Errorf("PromoteGuest(account) = %v; want ErrInvalid", err)
	}
	if _, err := store.FoldGuestInto(ctx, accountID, accountID); !errors.Is(err, ErrInvalid) {
		t.Errorf("FoldGuestInto(self) = %v; want ErrInvalid", err)
	}
	// A fold that names a guest which does not exist is an error, not a no-op:
	// a client that got the id wrong must not be told it worked.
	if _, err := store.FoldGuestInto(ctx, 999999, accountID); !errors.Is(err, ErrNotFound) {
		t.Errorf("FoldGuestInto(unknown guest) = %v; want ErrNotFound", err)
	}
}

func TestConsentIsRecordedAgainstTheIdentity(t *testing.T) {
	store, uid := setupWithUser(t)
	ctx := context.Background()

	if n, err := store.ConsentRecords(ctx); err != nil || n != 0 {
		t.Fatalf("ConsentRecords before any consent = %d, %v; want 0", n, err)
	}
	if err := store.RecordConsent(ctx, uid); err != nil {
		t.Fatalf("RecordConsent: %v", err)
	}
	var at float64
	if err := store.QueryRowContext(ctx,
		`SELECT COALESCE(consented_at, 0) FROM users WHERE id = ?`, uid).Scan(&at); err != nil {
		t.Fatalf("read consented_at: %v", err)
	}
	if at == 0 {
		t.Errorf("consent was not recorded against the identity")
	}
	if n, err := store.ConsentRecords(ctx); err != nil || n != 1 {
		t.Errorf("ConsentRecords = %d, %v; want 1", n, err)
	}
	// An anonymous visitor may accept, but there is nothing to record against.
	if err := store.RecordConsent(ctx, 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("RecordConsent(0) = %v; want ErrInvalid", err)
	}
}

// TestEveryColumnThatCarriesAUserIsFolded is the gate the fold is built around.
//
// A fold that misses a column does not fail: it silently leaves rows owned by an
// identity that is about to be deleted, and the symptom ("my votes are gone")
// shows up months later, in front of the person least able to debug it. So the
// expectation is derived from the migrated schema, not written here:
//
//  1. every column with a `REFERENCES users(id)` must be in foldColumns;
//  2. every column named like a user reference must be in foldColumns or be listed
//     below as a name that lies -- the FK-less ones (audit_log.actor_id,
//     maintenance_signals.set_by) are exactly the reason rule 1 is not enough;
//  3. and foldColumns must not name anything that no longer exists, because a
//     stale entry is a fold statement that fails at runtime in production only.
func TestEveryColumnThatCarriesAUserIsFolded(t *testing.T) {
	store := setup(t)
	ctx := context.Background()

	// Columns that LOOK like a user reference and are not one. Explicit, because
	// a silent exemption list is the same failure as the missing column.
	notAUser := map[string]string{
		"charters.solution_call_min_voters": "an integer threshold on a solution call, not a party",
		"users.username":                    "the identity's own name",
	}

	userish := regexp.MustCompile(`^(user_id|actor_id|author_id|voter_id|assignee_id|assigned_to|created_by|proposed_by|set_by|granted_by|asserted_by|held_by|removed_by|contested_by|applied_by|opened_by|closed_by)$`)

	tables := tableNames(t, store)
	fromSchema := map[string]string{} // "table.column" -> where it came from
	for _, table := range tables {
		rows, err := store.QueryContext(ctx, fmt.Sprintf(`PRAGMA foreign_key_list(%q)`, table))
		if err != nil {
			t.Fatalf("PRAGMA foreign_key_list(%s): %v", table, err)
		}
		for rows.Next() {
			var id, seq int
			var refTable, from, to, onUpdate, onDelete, match string
			if err := rows.Scan(&id, &seq, &refTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				t.Fatalf("scan FK row: %v", err)
			}
			if refTable == "users" {
				fromSchema[table+"."+from] = "REFERENCES users(id)"
			}
		}
		rows.Close()
		// Rule 2: the FK-less ones. A column that names a user without declaring
		// the reference is exactly how a fold loses rows.
		cols, err := store.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%q)`, table))
		if err != nil {
			t.Fatalf("PRAGMA table_info(%s): %v", table, err)
		}
		type colInfo struct {
			name string
			typ  string
		}
		var cs []colInfo
		for cols.Next() {
			var cid, notnull, pk int
			var name, typ string
			var dflt any
			if err := cols.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				t.Fatalf("scan column row: %v", err)
			}
			cs = append(cs, colInfo{name, typ})
		}
		cols.Close()
		for _, c := range cs {
			if _, exempt := notAUser[table+"."+c.name]; exempt {
				continue
			}
			if userish.MatchString(c.name) && !strings.Contains(strings.ToUpper(c.typ), "TEXT") {
				if _, already := fromSchema[table+"."+c.name]; !already {
					fromSchema[table+"."+c.name] = "named like a user reference, without an FK"
				}
			}
		}
	}

	inList := map[string]bool{}
	for _, c := range foldColumns {
		inList[c.Table+"."+c.Column] = true
	}

	var missing, stale []string
	for key, why := range fromSchema {
		if !inList[key] {
			missing = append(missing, fmt.Sprintf("%s (%s)", key, why))
		}
	}
	for _, c := range foldColumns {
		if _, ok := fromSchema[c.Table+"."+c.Column]; !ok {
			stale = append(stale, c.Table+"."+c.Column)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("these columns carry a user id and the fold does not move them:\n  %s\n"+
			"a merged account would silently keep rows owned by the deleted guest",
			strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("foldColumns names these, but nothing in the schema does:\n  %s\n"+
			"a stale entry is a statement that fails on the live database only",
			strings.Join(stale, "\n  "))
	}
	if len(fromSchema) < 40 {
		t.Fatalf("the schema yielded only %d user-carrying columns; the derivation is broken, "+
			"not the fold", len(fromSchema))
	}
}

// ---------------------------------------------------------------- helpers

func tableNames(t *testing.T, store *DB) []string {
	t.Helper()
	rows, err := store.Query(`SELECT name FROM sqlite_master WHERE type = 'table'
		AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		out = append(out, n)
	}
	return out
}

func complaintsByAuthor(t *testing.T, store *DB, authorID int64) int {
	t.Helper()
	var n int
	if err := store.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM complaints WHERE author_id = ?`, authorID).Scan(&n); err != nil {
		t.Fatalf("count complaints by %d: %v", authorID, err)
	}
	return n
}

func countComplaints(t *testing.T, store *DB, projectID int64) int {
	t.Helper()
	var n int
	if err := store.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM complaints WHERE project_id = ?`, projectID).Scan(&n); err != nil {
		t.Fatalf("count complaints: %v", err)
	}
	return n
}
