package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

// listSeq keeps list slugs unique within a test binary; lists.slug is globally UNIQUE.
var listSeq int

// TestAComparativeVoteMovesTwoListEntries is §16's central claim, tested.
//
// "Entries are ranked, not just listed. Entries compete in pairwise comparisons (the
// same Glicko-2 engine as feature priority), so 'best first' reflects community
// judgment instead of alphabetical accident."
//
// list_entry_votes and the elo columns on list_entries were in migration 0001 and had
// NO Go code at all -- no method wrote the table and no route called one -- so every
// entry read back at its 1500 seed forever and a list was precisely the alphabetical
// accident the section forbids. This is the test that would have caught that.
func TestAComparativeVoteMovesTwoListEntries(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	l, err := listWithEntries(t, store, pid, uid, 3)
	if err != nil {
		t.Fatalf("listWithEntries: %v", err)
	}
	before, err2 := store.GetListEntry(ctx, l.entries[0])
	if err2 != nil {
		t.Fatalf("GetListEntry: %v", err)
	}
	if before.ER != 1500 {
		t.Fatalf("a fresh entry reads elo_r %v, want the 1500 seed -- if the fixture "+
			"is not fresh this test is asserting on stale state", before.ER)
	}

	voter := secondVoter(t, store)
	if err := store.VoteListEntry(ctx, l.id, l.entries[0], l.entries[1], voter,
		"a", 1.0, testCharter(t, store, pid)); err != nil {
		t.Fatalf("VoteListEntry: %v", err)
	}

	a, _ := store.GetListEntry(ctx, l.entries[0])
	b, _ := store.GetListEntry(ctx, l.entries[1])
	if a.ER <= before.ER {
		t.Errorf("the winning entry did not rise: %v -> %v", before.ER, a.ER)
	}
	if b.ER >= before.ER {
		t.Errorf("the losing entry did not fall: %v -> %v", before.ER, b.ER)
	}
	if a.RD >= before.RD {
		t.Errorf("a comparison did not narrow the winner's deviation: %v -> %v",
			before.RD, a.RD)
	}
	// And the ORDER is now not the insertion order, which is the whole point.
	if !(a.ER > b.ER) {
		t.Errorf("after one comparison the winner (%v) does not outrank the loser (%v)",
			a.ER, b.ER)
	}
}

// TestAComparativeVoteIsRecordedOnce: UNIQUE (voter_id, entry_a, entry_b).
//
// One vote per pair, so a voter cannot move a rating by clicking twice. The store
// translates the constraint into a message a page can show.
func TestAComparativeVoteIsRecordedOnce(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	l, err := listWithEntries(t, store, pid, uid, 2)
	if err != nil {
		t.Fatalf("listWithEntries: %v", err)
	}
	voter := secondVoter(t, store)
	ch := testCharter(t, store, pid)

	if err := store.VoteListEntry(ctx, l.id, l.entries[0], l.entries[1], voter,
		"a", 1.0, ch); err != nil {
		t.Fatalf("first vote: %v", err)
	}
	first, _ := store.GetListEntry(ctx, l.entries[0])

	if err := store.VoteListEntry(ctx, l.id, l.entries[0], l.entries[1], voter,
		"a", 1.0, ch); err == nil {
		t.Error("the same voter compared the same pair twice; UNIQUE (voter_id, " +
			"entry_a, entry_b) is there so a rating cannot be moved by repetition")
	}

	after, _ := store.GetListEntry(ctx, l.entries[0])
	if after.ER != first.ER {
		t.Errorf("the refused second vote still moved the rating: %v -> %v",
			first.ER, after.ER)
	}
}

// TestYouCannotVoteOnAnEntryYouProposed is §5.1 applied to list entries.
//
// RecordVote had this check for features and VoteAnswer for answers; a list entry
// proposer could move their own link's rating, which for a list of links is a direct
// route to the top of the ranking.
func TestYouCannotVoteOnAnEntryYouProposed(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	l, err := listWithEntries(t, store, pid, uid, 2)
	if err != nil {
		t.Fatalf("listWithEntries: %v", err)
	}
	// Both entries were proposed by `uid`, so `uid` cannot compare them.
	if err := store.VoteListEntry(ctx, l.id, l.entries[0], l.entries[1], uid,
		"a", 1.0, testCharter(t, store, pid)); err == nil {
		t.Error("an entry's proposer voted on their own entry")
	}
}

// TestAComparativeVoteCannotCrossLists: both entries must belong to the list in the
// path, or one list's tally advances another list's entries -- and a reader of the
// second list sees a rank they never voted on.
func TestAComparativeVoteCannotCrossLists(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	first, err := listWithEntries(t, store, pid, uid, 2)
	if err != nil {
		t.Fatalf("first list: %v", err)
	}
	second, err := listWithEntries(t, store, pid, uid, 2)
	if err != nil {
		t.Fatalf("second list: %v", err)
	}
	voter := secondVoter(t, store)

	if err := store.VoteListEntry(ctx, first.id, first.entries[0], second.entries[0],
		voter, "a", 1.0, testCharter(t, store, pid)); err == nil {
		t.Error("a comparison spanning two lists was accepted")
	}
}

// TestTheComparativeOutcomeVocabularyIsTheSchemas: list_entry_votes.outcome is CHECKed
// to exactly ('a','b','both','neither','skip').
//
// Anything else -- "a" spelled differently, or a 1-5 scale -- is refused by the store
// rather than reaching the database, so the error names the valid set.
func TestTheComparativeOutcomeVocabularyIsTheSchemas(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	l, err := listWithEntries(t, store, pid, uid, 2)
	if err != nil {
		t.Fatalf("listWithEntries: %v", err)
	}
	voter := secondVoter(t, store)
	ch := testCharter(t, store, pid)

	// errors.Is(ErrInvalid), not merely "an error": without the store's own
	// validation the CHECK constraint catches the same values, so asserting only that
	// something failed is passed by a 500 -- and by the exact mutation that removed
	// the guard. The store must refuse, with a message naming the valid set.
	for _, bad := range []string{"", "1", "A", "both!", "tie"} {
		err := store.VoteListEntry(ctx, l.id, l.entries[0], l.entries[1], voter,
			bad, 1.0, ch)
		if err == nil {
			t.Errorf("outcome %q was accepted; the CHECK allows only "+
				"('a','b','both','neither','skip')", bad)
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("outcome %q gave %q, which is not ErrInvalid; a value that fell "+
				"through to the CHECK is a database error, not a refusal", bad, err)
		}
	}

	// An entry cannot be compared with itself, which would divide by nothing in Glicko.
	if err := store.VoteListEntry(ctx, l.id, l.entries[0], l.entries[0], voter,
		"a", 1.0, ch); err == nil {
		t.Error("an entry was compared with itself")
	}
}

// TestRemovedEntriesAreNotComparable: a removed entry is content a quorum took down, so
// ranking it against a live one asks the community to prefer it.
func TestRemovedEntriesAreNotComparable(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	l, err := listWithEntries(t, store, pid, uid, 2)
	if err != nil {
		t.Fatalf("listWithEntries: %v", err)
	}
	if _, err := store.ExecContext(ctx,
		`UPDATE list_entries SET status='removed' WHERE id=?`, l.entries[1]); err != nil {
		t.Fatalf("seed removed: %v", err)
	}

	voter := secondVoter(t, store)
	if err := store.VoteListEntry(ctx, l.id, l.entries[0], l.entries[1], voter,
		"a", 1.0, testCharter(t, store, pid)); err == nil {
		t.Error("a removed entry was compared for rank")
	}
}

// TestComparisonCountsAreReadable: §16's claim is that order reflects COMMUNITY
// JUDGMENT, and "3 comparisons" beside an entry is the evidence for that.
//
// Without the count, a 1610 and a 1500 look like the same number of opinions, and the
// page is asserting a consensus it cannot show.
func TestComparisonCountsAreReadable(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	l, err := listWithEntries(t, store, pid, uid, 3)
	if err != nil {
		t.Fatalf("listWithEntries: %v", err)
	}
	ch := testCharter(t, store, pid)

	// Three DISTINCT voters, or UNIQUE (voter_id, ...) refuses the repeats.
	for i := 0; i < 3; i++ {
		voter := newUserNamed(t, store, "listvoter"+string(rune('a'+i)))
		if err := store.VoteListEntry(ctx, l.id, l.entries[0], l.entries[1], voter,
			"a", 1.0, ch); err != nil {
			t.Fatalf("vote %d: %v", i, err)
		}
	}

	counts, err := store.GetListEntryVotes(ctx, l.id)
	if err != nil {
		t.Fatalf("GetListEntryVotes: %v", err)
	}
	// Entry 0 was on the winning side three times, entry 1 the losing side three
	// times, and entry 2 was never compared -- so it must be ABSENT from the map, not
	// zero-valued by a loop that touches everything.
	if counts[l.entries[0]] != 3 {
		t.Errorf("entry 0 has %d comparisons, want 3", counts[l.entries[0]])
	}
	if counts[l.entries[1]] != 3 {
		t.Errorf("entry 1 has %d comparisons, want 3", counts[l.entries[1]])
	}
	if n, present := counts[l.entries[2]]; present && n != 0 {
		t.Errorf("an uncompared entry appears with %d comparisons", n)
	}
}

// listWithEntries builds a list with n entries in one project.
//
// The slug is DERIVED FROM t.Name(), because lists.slug is `TEXT NOT NULL UNIQUE` --
// globally, not per project. A fixed slug meant the second call in any test failed with
// "UNIQUE constraint failed: lists.slug", which is the schema being right and the
// fixture being wrong.
func listWithEntries(t *testing.T, st *DB, projectID, authorID int64, n int) (struct {
	id      int64
	entries []int64
}, error) {
	t.Helper()
	var out struct {
		id      int64
		entries []int64
	}
	// A COUNTER, not t.Name(): one test calls this twice for two DIFFERENT lists, so
	// the name is the same both times and the second call collided.
	listSeq++
	l, err := st.CreateList(context.Background(), projectID, authorID,
		fmt.Sprintf("list-under-test-%d", listSeq), "A list", "d")
	if err != nil {
		return out, err
	}
	out.id = l.ID
	for i := 0; i < n; i++ {
		e, err := st.CreateListEntry(context.Background(), l.ID,
			"https://example.invalid/"+string(rune('a'+i)),
			"entry "+string(rune('a'+i)), "description")
		if err != nil {
			return out, err
		}
		out.entries = append(out.entries, e.ID)
	}
	return out, nil
}

// TestAnInstanceWideListIsReachable is §16's "attachable to a project or
// instance-wide" tested end to end.
//
// lists.project_id is nullable, migration 0001 labels the NULL "instance-wide", and
// §16 calls it a first-class scope. It was unreachable THREE ways at once:
//
//   - GetListsByProject filters `project_id=?`, which never matches NULL, so no index
//     could ever return one
//   - handleGetList required `l.ProjectID == projectID`, and an instance-wide list is
//     0, so the detail route 404'd
//   - List.ProjectID was an int64, so the SELECT's NULL failed the scan and answered
//     500 before the permission check was even reached
//
// Each alone would have been a bug. Together they meant an instance-wide list was
// created successfully and then invisible with no error at any layer to explain it.
func TestAnInstanceWideListIsReachable(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	// project_id NULL, written through the store's own constructor.
	global, err := store.CreateList(ctx, 0, uid, "instance-wide", "Shared", "d")
	if err != nil {
		t.Fatalf("CreateList with no project: %v", err)
	}
	if global.ProjectID != nil {
		t.Errorf("a list created with no project reports ProjectID %v, want nil; "+
			"an int64 field here makes the NULL a scan error at read time",
			*global.ProjectID)
	}

	// READABLE. This is the assertion the int64 field failed: the scan, not the
	// permission check.
	got, err := store.GetList(ctx, global.ID)
	if err != nil {
		t.Fatalf("GetList on an instance-wide list: %v", err)
	}
	if got.Name != "Shared" {
		t.Errorf("read back %q", got.Name)
	}

	// LISTED on its own.
	instances, err := store.GetInstanceLists(ctx)
	if err != nil {
		t.Fatalf("GetInstanceLists: %v", err)
	}
	if len(instances) != 1 || instances[0].ID != global.ID {
		t.Errorf("GetInstanceLists returned %d lists, want just the instance-wide one; "+
			"`project_id IS NULL` is the only way one can be found", len(instances))
	}

	// AND VISIBLE FROM A PROJECT'S INDEX, because §16 makes instance-wide the scope
	// every project shares.
	display, err := store.GetListsByProjectForDisplay(ctx, pid)
	if err != nil {
		t.Fatalf("GetListsByProjectForDisplay: %v", err)
	}
	seen := false
	for _, l := range display {
		if l.ID == global.ID {
			seen = true
		}
	}
	if !seen {
		t.Error("an instance-wide list is absent from a project's display list; a " +
			"project page that hid them would hide the lists that apply to it")
	}
}

// TestTheProjectScopedQueryStillExcludesInstanceWideLists: the display list ADDS the
// global ones; it must not stop being scoped. GetListsByProject exists for callers
// that must see only their project's, and a project page's OWN lists is one of them.
func TestTheProjectScopedQueryStillExcludesInstanceWideLists(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	if _, err := store.CreateList(ctx, 0, uid, "global-one", "G", "d"); err != nil {
		t.Fatalf("CreateList global: %v", err)
	}
	scoped, err := store.CreateList(ctx, pid, uid, "scoped-one", "S", "d")
	if err != nil {
		t.Fatalf("CreateList scoped: %v", err)
	}

	only, err := store.GetListsByProject(ctx, pid)
	if err != nil {
		t.Fatalf("GetListsByProject: %v", err)
	}
	if len(only) != 1 || only[0].ID != scoped.ID {
		t.Errorf("GetListsByProject returned %d lists, want only the scoped one", len(only))
	}

	both, err := store.GetListsByProjectForDisplay(ctx, pid)
	if err != nil {
		t.Fatalf("GetListsByProjectForDisplay: %v", err)
	}
	if len(both) != 2 {
		t.Errorf("the display list has %d lists, want both", len(both))
	}
}

// TestAListsProjectIsNullableInTheSchema: if this ever becomes NOT NULL the feature
// above dies silently, so the constraint itself is asserted.
func TestAListsProjectIsNullableInTheSchema(t *testing.T) {
	store, _, _ := setupWithProject(t)
	var notnull sql.NullString
	if err := store.QueryRowContext(context.Background(),
		`SELECT "notnull" FROM pragma_table_info('lists') WHERE name='project_id'`).
		Scan(&notnull); err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	if notnull.Valid && notnull.String == "1" {
		t.Error("lists.project_id is NOT NULL; §16 requires instance-wide lists and " +
			"every code path above depends on the NULL")
	}
}
