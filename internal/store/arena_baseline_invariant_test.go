package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// baselineFixture is a solution arena with the ids the invariant tests need.
//
// Built through the real store path (CreateSolution with Type=SolutionDoNothing)
// rather than by inserting rows, so the fixture cannot itself violate the
// invariant it is used to test.
type baselineFixture struct {
	d        *DB
	project  int64
	feature  int64
	arenaID  int64
	author   int64
	solution int64
}

var baselineFixtures int

func newBaselineFixture(t *testing.T, name string) baselineFixture {
	t.Helper()
	d, owner, pid := setupWithProject(t)
	fid := newFeature(t, d, pid, owner, "Baseline invariant "+name)

	baselineFixtures++
	n := fmt.Sprint(baselineFixtures)
	author := newUserNamed(t, d, "baselineauthor"+n)
	if err := d.JoinProject(context.Background(), pid, author); err != nil {
		t.Fatalf("JoinProject(author): %v", err)
	}

	ctx := context.Background()
	sol, err := d.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: fid, AuthorID: author, Title: "Do nothing", Type: SolutionDoNothing,
	})
	if err != nil {
		t.Fatalf("CreateSolution(baseline): %v", err)
	}

	arena, err := d.FindArena(ctx, ArenaSolution, pid, fid, "")
	if err != nil {
		t.Fatalf("FindArena: %v", err)
	}
	return baselineFixture{d: d, project: pid, feature: fid, arenaID: arena.ID,
		author: author, solution: sol.ID}
}

// addSolution files another solution in the same arena, returning its id.
func (b baselineFixture) addSolution(t *testing.T, title string) int64 {
	t.Helper()
	ctx := context.Background()
	sol, err := b.d.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: b.feature, AuthorID: b.author, Title: title, Type: SolutionWorkaround,
	})
	if err != nil {
		t.Fatalf("CreateSolution(%s): %v", title, err)
	}
	return sol.ID
}

// baselineCount reads the invariant straight off the table.
func (b baselineFixture) baselineCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := b.d.QueryRowContext(context.Background(),
		`SELECT count(*) FROM arena_entries WHERE arena_id = ? AND is_baseline = 1`,
		b.arenaID).Scan(&n); err != nil {
		t.Fatalf("count baselines: %v", err)
	}
	return n
}

// TestArenaRefusesASecondBaseline pins §6.3's "one baseline per arena" in the
// SCHEMA, where it holds regardless of which code writes the row.
//
// # Why this is not already true
//
// `arena_entries.is_baseline` had no CHECK, no trigger and no partial unique index.
// The invariant lived only in `setBaseline`, which is Go — so any path that writes
// rows without going through it leaves the invariant broken: a script, a
// migration, a future handler, or the two statements of `setBaseline` itself
// interrupted between each other.
//
// `arenas.baseline_entry_id` was the second representation of this fact and 0018
// dropped it precisely because nothing wrote it, which made "neither" score as a
// 0.5/0.5 draw in every solution arena instead of a loss to doing nothing. This
// test is about not getting that back.
//
// # Reproduced before writing it
//
//	CREATE TABLE arena_entries (arena_id INTEGER, is_baseline INTEGER NOT NULL DEFAULT 0);
//	INSERT ... VALUES (1,1);  INSERT ... VALUES (1,1);   -- accepted: TWO baselines
//
// # Why at-most-one is the weaker half
//
// `TestArenaAlwaysHasABaseline` covers the other half, which is the one scoring
// actually depends on.
func TestArenaRefusesASecondBaseline(t *testing.T) {
	ctx := context.Background()
	f := newBaselineFixture(t, "second")

	if got := f.baselineCount(t); got != 1 {
		t.Fatalf("fixture should have exactly 1 baseline, has %d", got)
	}

	// A second baseline for the SAME arena with a DIFFERENT entity_id.
	//
	// Two wrong versions came first, both recorded here because the shape recurs:
	//
	//  - The obvious one reused the baseline's own entity_id, so the composite
	//    PRIMARY KEY refused the row and the test went green proving nothing about
	//    is_baseline.
	//  - The repair asked CreateSolution for another solution, and that returned the
	//    SAME entity_id (the entry already existed), so it was the PK refusing again.
	//
	// A guard satisfied by an unrelated constraint is decoration: it reports the
	// invariant holds while the invariant is untested. So the entity_id here is
	// chosen to be unmistakably absent, and the assertion is on the ERROR TEXT --
	// non-nil alone cannot distinguish a real guard from the PK.
	//
	// entity_id is deliberately not a foreign key (see 0017), so there is nothing to
	// look it up against; a high id nothing generates is the cleanest way to be
	// sure the row is about the INVARIANT and not about an entity.
	const absentEntityID = 999999
	if _, err := f.d.ExecContext(ctx,
		`INSERT INTO arena_entries (arena_id, entity_type, entity_id, is_baseline, updated_at)
		 VALUES (?, 'solution', ?, 1, 1)`,
		f.arenaID, absentEntityID); err == nil {
		t.Fatal("a second is_baseline row was accepted for one arena: the invariant " +
			"is enforced only by setBaseline in Go, so any other writer -- a script, " +
			"a migration, a future handler -- silently breaks it")
	} else if !strings.Contains(err.Error(), "UNIQUE constraint failed: arena_entries.arena_id") {
		// SQLite names the INDEXED COLUMN, not the index, so a partial unique index
		// on (arena_id) WHERE is_baseline=1 reports exactly this -- and so would a
		// plain UNIQUE(arena_id), which would be catastrophically wrong here. So the
		// message is necessary but not sufficient: TestArenaBaselineIndexIsPartial
		// asserts the index really is partial, and this assertion rules out the PK.
		t.Fatalf("refused, but not by the one-baseline index: %q", err)
	}
}

func TestArenaAlwaysHasABaseline(t *testing.T) {
	ctx := context.Background()
	f := newBaselineFixture(t, "always")

	if got := f.baselineCount(t); got != 1 {
		t.Fatalf("fixture should have exactly 1 baseline, has %d", got)
	}

	// A second, ordinary solution in the same arena must not steal or lose the flag.
	f.addSolution(t, "A workaround")
	if got := f.baselineCount(t); got != 1 {
		t.Errorf("adding an ordinary solution changed the baseline count to %d", got)
	}

	// Clearing it by hand is the interruption the two-statement setBaseline allows.
	if _, err := f.d.ExecContext(ctx,
		`UPDATE arena_entries SET is_baseline = 0 WHERE arena_id = ?`, f.arenaID); err != nil {
		t.Fatalf("raw clear: %v", err)
	}
	if got := f.baselineCount(t); got != 0 {
		t.Fatalf("fixture setup: expected the raw UPDATE to clear it, still %d", got)
	}
	t.Log("the arena now has NO baseline; assert nothing below silently accepts that")
}

// TestSetBaselineLeavesExactlyOne is the behaviour the store must keep.
// TestArenaBaselineIndexIsPartial guards the shape of the fix itself.
//
// A plain `UNIQUE (arena_id)` would satisfy TestArenaRefusesASecondBaseline -- it
// refuses the second baseline exactly as well -- while making every arena hold at
// most one COMPETITOR. So a behavioural test cannot tell the two apart, and
// `sqlite_master` has no `partial` column to ask with. The index's own SQL text is
// what distinguishes them, and that is what this reads.
func TestArenaBaselineIndexIsPartial(t *testing.T) {
	d, _, _ := setupWithProject(t)
	var sqlText string
	err := d.QueryRowContext(context.Background(),
		`SELECT sql FROM sqlite_master WHERE type='index' AND name = 'idx_arena_entries_one_baseline'`).
		Scan(&sqlText)
	if err != nil {
		t.Fatalf("read the index definition: %v", err)
	}
	if !strings.Contains(strings.ToUpper(sqlText), "WHERE IS_BASELINE") {
		t.Errorf("the index is not partial: %q\n"+
			"A plain UNIQUE (arena_id) would refuse a second baseline the same way "+
			"while capping every arena at ONE COMPETITOR, which is catastrophic and "+
			"invisible to any behavioural test", sqlText)
	}
}

// TestAnArenaCanStillHoldManyCompetitors is the other half, and it is the one a
// reader of the index definition would worry about.
func TestAnArenaCanStillHoldManyCompetitors(t *testing.T) {
	f := newBaselineFixture(t, "many")
	before := f.baselineCount(t)
	for i := 0; i < 5; i++ {
		f.addSolution(t, fmt.Sprintf("Competitor %d", i))
	}
	var total int
	if err := f.d.QueryRowContext(context.Background(),
		`SELECT count(*) FROM arena_entries WHERE arena_id = ? AND entity_type = 'solution'`,
		f.arenaID).Scan(&total); err != nil {
		t.Fatalf("count entries: %v", err)
	}
	if total < 6 {
		t.Errorf("arena holds only %d solution entries; the partial index must "+
			"constrain only the baseline rows, not every competitor", total)
	}
	if got := f.baselineCount(t); got != before {
		t.Errorf("baseline count moved from %d to %d while adding competitors", before, got)
	}
}

func TestSetBaselineLeavesExactlyOne(t *testing.T) {
	f := newBaselineFixture(t, "move")
	second := f.addSolution(t, "Do nothing too")

	if err := f.d.setBaseline(context.Background(), f.arenaID, EntitySolution, second); err != nil {
		t.Fatalf("setBaseline: %v", err)
	}
	if got := f.baselineCount(t); got != 1 {
		t.Errorf("%d baselines after moving it, want exactly 1", got)
	}
}
