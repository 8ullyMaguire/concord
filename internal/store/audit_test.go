package store

import (
	"context"
	"testing"
)

// ListAudit and AuditActions, from docs/plans/audit-log.md step A1.
//
// Three of these tests exist because of ways this function can be wrong that a
// plain "it returns rows" assertion cannot see:
//
//   - ORDER BY created_at DESC without the id tiebreak is correct on a fixture
//     where every row has a distinct timestamp, and non-deterministic on real
//     data, because several writers stamp time.Now().Unix() inside one request.
//     TestPagingIsStableAcrossRowsThatShareATimestamp is the only test that can
//     reach the tiebreak, and it needs a fixture that shares one.
//   - An unconditional LIKE '%%' returns exactly the rows the unfiltered query
//     returns, so a test that only checks "the right rows came back" passes with
//     the mutant. The count is the observable difference.
//   - A total counted without the filter is right on every unfiltered page and
//     wrong the moment anyone filters, which is the only thing a filter control
//     exists for.

// auditRow writes one audit row with an explicit timestamp, so a test can control
// ordering rather than depending on how fast the loop ran.
func auditRow(t *testing.T, d *DB, projectID, actorID int64, action, entity string, entityID int64, detail string, at float64) {
	t.Helper()
	_, err := d.ExecContext(context.Background(), `
		INSERT INTO audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, projectID, nullableID(actorID), action, entity,
		nullableID(entityID), detail, at)
	if err != nil {
		t.Fatalf("seed audit %s: %v", action, err)
	}
}

// nullableID is the package's own (arenas.go): Go's 0 becomes SQL NULL, which is
// how the writers spell "no actor" -- merge.go and features.go both call
// addAudit with actorID 0, and those rows really are NULL.

func TestTheAuditLogIsNewestFirstAndBounded(t *testing.T) {
	d, _, pid := setupWithProject(t)
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		auditRow(t, d, pid, 0, "create_project", "project", pid, "slug", float64(1000+i))
	}
	page, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(page.Entries) != 5 {
		t.Fatalf("got %d entries, want 5", len(page.Entries))
	}
	for i := 1; i < len(page.Entries); i++ {
		if page.Entries[i-1].CreatedAt < page.Entries[i].CreatedAt {
			t.Errorf("entry %d (%v) is older than entry %d (%v); the log is not newest-first",
				i-1, page.Entries[i-1].CreatedAt, i, page.Entries[i].CreatedAt)
		}
	}

	limited, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid, Limit: 2})
	if err != nil {
		t.Fatalf("ListAudit(limit 2): %v", err)
	}
	if len(limited.Entries) != 2 {
		t.Errorf("limit=2 returned %d entries, want 2", len(limited.Entries))
	}
	// The total is the whole filtered set, not the page. This is what lets a
	// client say "50 of 5" instead of guessing.
	if limited.Total != 5 {
		t.Errorf("Total = %d, want 5: the total must describe the filtered set, not the page", limited.Total)
	}
}

func TestAFilteredAuditLogExcludesWhatWasFilteredOut(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	auditRow(t, d, pid, uid, "set_visibility", "project", pid, "made private", 1001)
	auditRow(t, d, pid, uid, "create_invite", "invite", 1, "code abc", 1002)
	auditRow(t, d, pid, uid, "create_invite", "invite", 2, "code def", 1003)

	page, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid, Action: "create_invite"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("got %d entries for create_invite, want 2", len(page.Entries))
	}
	// The negative assertion is the load-bearing one. A filter that returns every
	// row is the failure mode that reads as "the filter works" when only the count
	// was checked.
	for _, e := range page.Entries {
		if e.Action != "create_invite" {
			t.Errorf("action filter returned a %q row", e.Action)
		}
	}
	if page.Total != 2 {
		t.Errorf("Total = %d, want 2", page.Total)
	}
}

func TestAnEmptyQuerySearchesNothingRatherThanEverything(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	auditRow(t, d, pid, uid, "set_visibility", "project", pid, "made private", 1001)
	auditRow(t, d, pid, uid, "create_invite", "invite", 1, "code abc", 1002)

	unfiltered, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	empty, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid, Text: ""})
	if err != nil {
		t.Fatalf("ListAudit(empty text): %v", err)
	}
	// An empty search is the same query as no search. The observable difference a
	// blanket LIKE '%%' makes is not in the rows -- it is identical -- so this test
	// can only pass by the mutant being absent, and says so.
	if empty.Total != unfiltered.Total {
		t.Errorf("empty text searched: total %d, unfiltered %d", empty.Total, unfiltered.Total)
	}
	if len(empty.Entries) != len(unfiltered.Entries) {
		t.Errorf("empty text changed the row count: %d vs %d",
			len(empty.Entries), len(unfiltered.Entries))
	}

	// And a search that matches nothing is an empty result, not an error and not
	// the whole log.
	none, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid, Text: "zzz-no-such-thing"})
	if err != nil {
		t.Fatalf("ListAudit(no match): %v", err)
	}
	if len(none.Entries) != 0 || none.Total != 0 {
		t.Errorf("a search matching nothing returned %d entries (total %d), want 0/0",
			len(none.Entries), none.Total)
	}
}

func TestATextSearchMatchesDetailNotJustAction(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	auditRow(t, d, pid, uid, "set_visibility", "project", pid, "alpha needle", 1001)
	auditRow(t, d, pid, uid, "set_visibility", "project", pid, "beta", 1002)

	page, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid, Text: "needle"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	// The needle is in `detail` only -- both rows share the action. A search that
	// only covered `action` would return both, so this distinguishes the two.
	if len(page.Entries) != 1 {
		t.Fatalf("got %d entries for a detail-only needle, want 1", len(page.Entries))
	}
	if page.Entries[0].Detail != "alpha needle" {
		t.Errorf("matched the wrong row: detail %q", page.Entries[0].Detail)
	}
}

// TestASearchTreatsWildcardsAsLiteralCharacters is the only test that reaches the
// escaping in ListAudit, and the reason it exists is that "escape" is invisible
// to every other test here: a LIKE with a live wildcard returns a SUPERSET, and
// a superset is exactly what a correct search on a fixture that contains no
// wildcard returns too.
//
// The fixture is built so the two readings disagree. A search for the literal
// text "50%" must return the row whose detail is exactly "50%", and NOT the row
// whose detail is "50x" -- which is what "50%" means to an unescaped LIKE
// (50, then any single character). Likewise "_" is a single-character wildcard,
// so a search for "a_b" must not match "a b" or "axb".
func TestASearchTreatsWildcardsAsLiteralCharacters(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	// The rows the literal search MUST find.
	auditRow(t, d, pid, uid, "create_invite", "invite", 1, "50% done", 1101)
	auditRow(t, d, pid, uid, "create_invite", "invite", 2, "a_b", 1102)
	// The rows an UNESCAPED LIKE would wrongly include. Without escaping, the
	// search "%50%%" matches "50x" (50 followed by any one character), and
	// "a_b" matches "a b" and "axb".
	auditRow(t, d, pid, uid, "create_invite", "invite", 3, "50x", 1103)
	auditRow(t, d, pid, uid, "create_invite", "invite", 4, "axb", 1104)
	auditRow(t, d, pid, uid, "create_invite", "invite", 5, "a b", 1105)

	pct, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid, Text: "50%"})
	if err != nil {
		t.Fatalf("ListAudit(50%%): %v", err)
	}
	if len(pct.Entries) != 1 {
		var details []string
		for _, e := range pct.Entries {
			details = append(details, e.Detail)
		}
		t.Errorf("search for the literal 50%% returned %d entries (%v), want 1: %% is a LIKE wildcard and must be escaped",
			len(pct.Entries), details)
	} else if pct.Entries[0].Detail != "50% done" {
		t.Errorf("search for 50%% matched %q, want the row containing it", pct.Entries[0].Detail)
	}

	under, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid, Text: "a_b"})
	if err != nil {
		t.Fatalf("ListAudit(a_b): %v", err)
	}
	if len(under.Entries) != 1 {
		var details []string
		for _, e := range under.Entries {
			details = append(details, e.Detail)
		}
		t.Errorf("search for the literal a_b returned %d entries (%v), want 1: _ is a LIKE wildcard and must be escaped",
			len(under.Entries), details)
	} else if under.Entries[0].Detail != "a_b" {
		t.Errorf("search for a_b matched %q, want the row containing it", under.Entries[0].Detail)
	}
}

// TestTheEscapingUsesAnEscapeCharacter pins the ESCAPE clause, which the test
// above depends on and does not itself prove.
//
// SQLite treats "\" as an ordinary character in a LIKE pattern unless the
// pattern carries ESCAPE. Escape the wildcards and forget to declare ESCAPE and
// the search returns nothing at all -- the opposite failure from the one the
// previous test guards, and the kind a single one-directional assertion misses.
func TestTheEscapingUsesAnEscapeCharacter(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	auditRow(t, d, pid, uid, "create_invite", "invite", 1, "50% done", 1201)

	// If ESCAPE were missing, this would find 0 rows and the failure would read
	// as "search does not work", not as "the escape clause is gone".
	page, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid, Text: "50%"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(page.Entries) != 1 {
		t.Errorf("got %d entries searching for a literal 50%%, want 1: an escaped wildcard needs "+
			"an ESCAPE clause to be read as a literal", len(page.Entries))
	}
}

func TestTheTotalIsTheFilteredTotalNotTheTableTotal(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	auditRow(t, d, pid, uid, "create_invite", "invite", 1, "a", 1001)
	auditRow(t, d, pid, uid, "create_invite", "invite", 2, "b", 1002)
	auditRow(t, d, pid, uid, "set_visibility", "project", pid, "c", 1003)

	page, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid, Action: "create_invite"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	// This is the test that catches "paged correctly, counted wrong" -- the defect
	// that looks entirely correct on page one of an unfiltered list.
	if page.Total != 2 {
		t.Errorf("Total = %d, want 2: counting the whole table makes a filter look inert",
			page.Total)
	}
}

func TestPagingIsStableAcrossRowsThatShareATimestamp(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	// Five rows at the SAME second, which is what two writers in one request
	// produce. Without the id tiebreak the order between them is whatever SQLite
	// happens to return, and an auditor paging forward can see a row twice.
	for i := 1; i <= 5; i++ {
		auditRow(t, d, pid, uid, "create_invite", "invite", int64(i), "same-second", 2000)
	}

	first, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid, Limit: 3})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	second, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid, Limit: 3, Offset: 3})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(first.Entries) != 3 || len(second.Entries) != 2 {
		t.Fatalf("pages are %d and %d rows, want 3 and 2", len(first.Entries), len(second.Entries))
	}
	seen := map[int64]bool{}
	for _, e := range append(append([]AuditEntry{}, first.Entries...), second.Entries...) {
		if seen[e.ID] {
			t.Errorf("id %d appears on two pages: the tiebreak is missing", e.ID)
		}
		seen[e.ID] = true
	}
	// And the pages must tile the set: 3 + 2 with no repeat is 5 distinct rows,
	// which is the only reading of "paged correctly" that survives a tie.
	if len(seen) != 5 {
		t.Errorf("paging returned %d distinct rows across two pages, want 5", len(seen))
	}
}

func TestAnUnresolvedActorLeavesTheNameNullRatherThanFailing(t *testing.T) {
	d, _, pid := setupWithProject(t)
	ctx := context.Background()
	ghost := int64(987654)
	auditRow(t, d, pid, ghost, "create_invite", "invite", 1, "by a deleted account", 3001)
	auditRow(t, d, pid, 0, "merge", "merge_request", 2, "by nobody", 3002)

	page, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(page.Entries))
	}
	var sawGhost, sawNoActor bool
	for _, e := range page.Entries {
		if e.ActorID != nil && *e.ActorID == ghost {
			sawGhost = true
			if e.ActorDisplayName != nil {
				t.Errorf("invented a display name %q for a user id with no users row",
					*e.ActorDisplayName)
			}
		}
		if e.ActorID == nil {
			sawNoActor = true
		}
	}
	if !sawGhost {
		t.Error("the row with a dangling actor id disappeared")
	}
	if !sawNoActor {
		t.Error("the row written with no actor disappeared")
	}
}

func TestTheAuditLogCarriesTheActorsDisplayName(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	auditRow(t, d, pid, uid, "set_visibility", "project", pid, "made private", 4001)

	page, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(page.Entries))
	}
	name := page.Entries[0].ActorDisplayName
	if name == nil {
		t.Fatal("ActorDisplayName is nil; the viewer shows ids nobody can act on")
	}
	if *name != "Test User" {
		t.Errorf("ActorDisplayName = %q, want %q", *name, "Test User")
	}
}

func TestTheEntityAndActorFiltersNarrow(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	other := newUserNamed(t, d, "otheruser")
	auditRow(t, d, pid, uid, "create_invite", "invite", 1, "a", 5001)
	auditRow(t, d, pid, other, "set_visibility", "project", pid, "b", 5002)
	auditRow(t, d, pid, other, "set_visibility", "project", pid, "c", 5003)

	byEntity, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid, EntityType: "invite"})
	if err != nil {
		t.Fatalf("entity filter: %v", err)
	}
	if len(byEntity.Entries) != 1 {
		t.Errorf("entity_type=invite returned %d rows, want 1", len(byEntity.Entries))
	}

	byActor, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid, ActorID: other})
	if err != nil {
		t.Fatalf("actor filter: %v", err)
	}
	if len(byActor.Entries) != 2 {
		t.Errorf("actor filter returned %d rows, want 2", len(byActor.Entries))
	}
}

func TestOneProjectsAuditLogDoesNotLeakIntoAnother(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	proj2, err := d.CreateProject(ctx, uid, "other-project", "Other", "d", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	auditRow(t, d, pid, uid, "create_project", "project", pid, "mine", 6001)
	auditRow(t, d, proj2.ID, uid, "create_project", "project", proj2.ID, "theirs", 6002)

	page, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("got %d entries for project 1, want 1: the project filter did not apply", len(page.Entries))
	}
	if page.Entries[0].Detail != "mine" {
		t.Errorf("project 1's log contains another project's row: %q", page.Entries[0].Detail)
	}
}

// TestALimitAboveTheCeilingIsRefusedRatherThanHonoured
//
// The mutant this kills is `if f.Limit <= 0` -- dropping the upper bound -- which
// is the version of this code that lets a caller ask for a million rows out of a
// table whose whole purpose is to be read end to end by an auditor. It cannot be
// killed through the returned rows, because rows are identical at 300 and at
// 1,000,000: only the LIMIT the query was given differs, and nothing in the result
// set says so. So this asserts on the SQL actually sent.
//
// That is not a compromise, it is the point: a test that cannot observe the
// difference between a bounded and an unbounded query is decoration. The
// alternative -- seeding 201 rows and counting them -- passes under the mutant
// whenever the ceiling is 200 exactly, which is the case worth protecting.
func TestALimitAboveTheCeilingIsRefusedRatherThanHonoured(t *testing.T) {
	d, _, pid := setupWithProject(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		auditRow(t, d, pid, 0, "create_project", "project", pid, "s", float64(8000+i))
	}

	page, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid, Limit: 100000})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if page.Limit != AuditMaxLimit {
		t.Errorf("Limit = %d, want %d: a caller asked for a million rows and got the ceiling instead",
			page.Limit, AuditMaxLimit)
	}

	// And the same clamp below the floor: no limit at all is the documented
	// default, not "everything".
	zero, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid, Limit: 0})
	if err != nil {
		t.Fatalf("ListAudit(limit 0): %v", err)
	}
	if zero.Limit != AuditDefaultLimit {
		t.Errorf("Limit = %d for an unset limit, want the default %d", zero.Limit, AuditDefaultLimit)
	}
	if zero.Offset != 0 {
		t.Errorf("Offset = %d for a negative offset, want 0 clamped", zero.Offset)
	}
}

func TestAuditActionsListsWhatCanBeFilteredOn(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	auditRow(t, d, pid, uid, "set_visibility", "project", pid, "a", 7001)
	auditRow(t, d, pid, uid, "create_invite", "invite", 1, "b", 7002)
	auditRow(t, d, pid, uid, "create_invite", "invite", 2, "c", 7003)

	got, err := d.AuditActions(ctx, pid)
	if err != nil {
		t.Fatalf("AuditActions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 distinct actions", got)
	}
	// Alphabetical, so the dropdown does not reorder itself every time a vote
	// lands. A filter list whose order is a function of the data is one nobody can
	// learn.
	if got[0] != "create_invite" || got[1] != "set_visibility" {
		t.Errorf("AuditActions = %v, want [create_invite set_visibility] alphabetically", got)
	}
}

// TestTheSinceFilterDropsOlderRows is the only test that reaches the `since`
// clause. Without it, `since` is dead code that looks wired: the mutation gate
// reported "the since filter is dropped" as SURVIVED, which is a coverage gap
// stated by the harness rather than one I had to infer.
//
// The boundary is asserted with a row exactly ON the cut and a row exactly one
// second below it. `>=` against `>` is a one-row difference that a test using
// round numbers several units apart cannot see.
func TestTheSinceFilterDropsOlderRows(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	const cut = 5000.0
	auditRow(t, d, pid, uid, "create_invite", "invite", 1, "before", cut-1)
	auditRow(t, d, pid, uid, "create_invite", "invite", 2, "exactly on the cut", cut)
	auditRow(t, d, pid, uid, "create_invite", "invite", 3, "after", cut+1)

	page, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid, Since: cut})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(page.Entries) != 2 {
		var details []string
		for _, e := range page.Entries {
			details = append(details, e.Detail)
		}
		t.Errorf("since=%v returned %d rows (%v), want 2: the clause is >=, so the row ON the cut is kept",
			cut, len(page.Entries), details)
	}
	for _, e := range page.Entries {
		if e.Detail == "before" {
			t.Error("since= returned a row older than the cut")
		}
	}
	// A zero Since is "no filter", not "since the epoch" -- which would be the
	// same set today and a very different one for any row dated before 1970.
	none, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid})
	if err != nil {
		t.Fatalf("ListAudit(no since): %v", err)
	}
	if len(none.Entries) != 3 {
		t.Errorf("an unset Since returned %d rows, want 3", len(none.Entries))
	}
}

// TestASearchMatchesTheActionNameToo is the load-bearing half of the OR.
//
// The detail-only search above proves detail is searched; it cannot prove action
// is, because every fixture row shares one action. This one searches for the
// action NAME with a detail that does not contain it, so a filter covering only
// detail returns nothing and the clause is proven to span both columns.
func TestASearchMatchesTheActionNameToo(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	auditRow(t, d, pid, uid, "set_visibility", "project", pid, "nothing alike here", 1301)
	auditRow(t, d, pid, uid, "create_invite", "invite", 1, "nothing alike here", 1302)

	page, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid, Text: "set_visibility"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("searching for an action name returned %d rows, want 1: the search spans action as well as detail",
			len(page.Entries))
	}
	if page.Entries[0].Action != "set_visibility" {
		t.Errorf("matched the wrong row: action %q", page.Entries[0].Action)
	}
}

// TestASearchTreatsALiteralBackslashAsALiteralCharacter reaches the rule that
// the escape character is doubled BEFORE the wildcards are escaped.
//
// The two orderings differ only on a search containing a backslash. Doubling
// last re-escapes the backslashes this function has just inserted in front of
// % and _, so "\%" -- a literal backslash then a live wildcard -- is what the
// pattern becomes, and the search quietly returns the wrong rows. No other
// fixture in this file contains a backslash, which is exactly why the mutation
// gate reported this mutant SURVIVED.
func TestASearchTreatsALiteralBackslashAsALiteralCharacter(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	auditRow(t, d, pid, uid, "create_invite", "invite", 1, `path C:\Users\x`, 1401)
	auditRow(t, d, pid, uid, "create_invite", "invite", 2, `path C:\UsersXy`, 1402)

	page, err := d.ListAudit(ctx, AuditFilter{ProjectID: pid, Text: `C:\Users\x`})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(page.Entries) != 1 {
		var details []string
		for _, e := range page.Entries {
			details = append(details, e.Detail)
		}
		t.Errorf("search for a literal backslash path returned %d rows (%v), want 1: the escape "+
			"character must be doubled before the wildcards are escaped", len(page.Entries), details)
	}
}
