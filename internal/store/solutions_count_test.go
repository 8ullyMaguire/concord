package store

// CountSolutions, and specifically the omission rule it exists to serve.
//
// The interesting case is a feature whose solution count is LARGER than any
// leaderboard limit, because that is the case the naive "ask for limit+1 and look
// for an extra row" implementation gets wrong: ListSolutions applies its limit
// inside ArenaLeaderboard and then skips arena entries whose solution row is
// gone, so the number of rows that come back is not the number of solutions that
// exist. A fixture with exactly `limit` solutions passes either way and proves
// nothing.

import (
	"context"
	"fmt"
	"testing"
)

// countFeatureWithN builds a feature with exactly n solutions and returns its id.
//
// Through the store, not through raw SQL: a fixture that bypasses CreateSolution
// can create solutions the product would refuse, and then the test is asserting
// about a state nothing can reach.
//
// The solution author is NOT the feature author. CreateSolution refuses that with
// "a feature's author cannot author its solutions" -- §5.2 stops an author
// judging their own entry, and a feature's author is the person most likely to
// propose its solutions. The first draft of this helper passed the feature's
// author and every test in the file failed at CreateSolution. solutionFixture
// already returns a distinct solution author for exactly this reason.
func countFeatureWithN(t *testing.T, d *DB, featureAuthor, pid int64, n int) int64 {
	t.Helper()
	fid := newFeature(t, d, pid, featureAuthor, fmt.Sprintf("feature with %d solutions", n))
	solutionAuthors++
	solAuthor := newUserNamed(t, d, fmt.Sprintf("countauthor%d", solutionAuthors))
	for i := 0; i < n; i++ {
		if _, err := d.CreateSolution(context.Background(), CreateSolutionInput{
			FeatureID: fid, AuthorID: solAuthor,
			Title: fmt.Sprintf("solution %d", i), Body: "a body", Type: "build-new",
		}); err != nil {
			t.Fatalf("CreateSolution %d: %v", i, err)
		}
	}
	return fid
}

func TestCountSolutionsReturnsTheWholeSetNotTheLeaderboardsSlice(t *testing.T) {
	d, author, pid, _ := solutionFixture(t)
	ctx := context.Background()

	// More solutions than any leaderboard limit used below, so the count and the
	// slice disagree and the difference is the thing being tested.
	fid := countFeatureWithN(t, d, author, pid, 7)

	got, err := d.CountSolutions(ctx, fid)
	if err != nil {
		t.Fatalf("CountSolutions: %v", err)
	}
	if got != 7 {
		t.Errorf("CountSolutions = %d, want 7", got)
	}

	// The slice is bounded; the count is not. If these ever agree, this test is
	// no longer distinguishing them and should be rewritten.
	scores, err := d.ListSolutions(ctx, fid, 3)
	if err != nil {
		t.Fatalf("ListSolutions: %v", err)
	}
	if len(scores) != 3 {
		t.Fatalf("ListSolutions(limit=3) returned %d rows; the fixture makes this "+
			"test's premise wrong if it did not bound the slice", len(scores))
	}
	if got <= len(scores) {
		t.Errorf("CountSolutions = %d and ListSolutions(limit=3) returned %d; the "+
			"count must describe the whole set, not the slice", got, len(scores))
	}

	// And the arithmetic a panel depends on is now expressible.
	if omitted := got - len(scores); omitted != 4 {
		t.Errorf("omitted would be %d, want 4", omitted)
	}
}

// A feature nobody proposed for: zero. Not nil, not an error.
func TestCountSolutionsOnAFeatureWithNoProposalsIsZero(t *testing.T) {
	d, author, pid, _ := solutionFixture(t)
	ctx := context.Background()

	fid := newFeature(t, d, pid, author, "nobody has proposed anything here")
	n, err := d.CountSolutions(ctx, fid)
	if err != nil {
		t.Fatalf("CountSolutions: %v", err)
	}
	if n != 0 {
		t.Errorf("CountSolutions = %d for a feature with no solutions, want 0", n)
	}
}

// Scoped to the feature. The obvious bug in a COUNT is a missing WHERE, and a
// project with several features is the only way to see it.
func TestCountSolutionsIsScopedToItsFeature(t *testing.T) {
	d, author, pid, _ := solutionFixture(t)
	ctx := context.Background()

	a := countFeatureWithN(t, d, author, pid, 2)
	b := countFeatureWithN(t, d, author, pid, 5)
	// Two features, five solutions between them; a COUNT missing its WHERE would
	// report 7 for both.

	na, err := d.CountSolutions(ctx, a)
	if err != nil {
		t.Fatalf("CountSolutions(a): %v", err)
	}
	nb, err := d.CountSolutions(ctx, b)
	if err != nil {
		t.Fatalf("CountSolutions(b): %v", err)
	}
	if na != 2 || nb != 5 {
		t.Errorf("counts = %d and %d, want 2 and 5; the count is not scoped to "+
			"the feature", na, nb)
	}
}

// A solution the leaderboard cannot see still exists and must still be counted.
//
// This is the state `ListSolutions`' skip branch exists for: an arena entry whose
// solution row is gone is skipped rather than reported as a zero-score solution,
// so the leaderboard can be SHORTER than the number of solutions. A panel that
// computed its omitted count from the leaderboard's length therefore under-counts,
// and the count has to come from the solutions table.
//
// The arena entry is removed by hand after the solution is created normally. The
// first draft tried to build a solution that never got an arena entry, and
// skipped when it found CreateSolution always calls UpsertArenaEntry. The skip was
// honest — the state really is unreachable — but it cost the gate its
// armed==declared check, so 24 other tests went unmeasured for the price of one.
// The reachable state is the one that matters: the entry exists, then goes away.
func TestCountSolutionsCountsSolutionsOutsideTheLeaderboard(t *testing.T) {
	d, author, _, fid := solutionFixture(t)
	ctx := context.Background()

	sol, err := d.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: fid, AuthorID: author,
		Title: "proposed once", Body: "b", Type: "build-new",
	})
	if err != nil {
		t.Fatalf("CreateSolution: %v", err)
	}
	before, err := d.CountSolutions(ctx, fid)
	if err != nil {
		t.Fatalf("CountSolutions: %v", err)
	}

	// Ranked, so the leaderboard can see it. Without this the fixture could pass
	// for the wrong reason: a solution that was never ranked and one that lost its
	// entry are indistinguishable from the outside.
	scores, err := d.ListSolutions(ctx, fid, 50)
	if err != nil {
		t.Fatalf("ListSolutions: %v", err)
	}
	ranked := false
	for _, sc := range scores {
		if sc.SolutionID == sol.ID {
			ranked = true
		}
	}
	if !ranked {
		t.Fatal("the fresh solution is not in the leaderboard; this fixture cannot " +
			"produce the state it is about")
	}

	// Drop the arena entry, leaving a solution row nothing ranks.
	if _, err := d.ExecContext(ctx,
		`DELETE FROM arena_entries WHERE entity_type='solution' AND entity_id=?`,
		sol.ID); err != nil {
		t.Fatalf("delete arena entry: %v", err)
	}

	scores, err = d.ListSolutions(ctx, fid, 50)
	if err != nil {
		t.Fatalf("ListSolutions: %v", err)
	}
	for _, sc := range scores {
		if sc.SolutionID == sol.ID {
			t.Fatal("the solution is still ranked after its arena entry was removed")
		}
	}

	n, err := d.CountSolutions(ctx, fid)
	if err != nil {
		t.Fatalf("CountSolutions: %v", err)
	}
	if n != before {
		t.Errorf("CountSolutions = %d after deleting the arena entry, want %d; the "+
			"count must describe the solutions table, or a panel understates work "+
			"that exists", n, before)
	}
}
