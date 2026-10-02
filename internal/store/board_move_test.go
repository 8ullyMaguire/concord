package store

import (
	"context"
	"errors"
	"testing"
)

// Regression tests for the board's MOVE path.
//
// F2 of the site-wide e2e sweep (20-Areas/concord-site-e2e-verification.md)
// reported that board_cards held zero rows across all 70 projects because no
// code path inserted one, so MoveCard and GetBoardCard both operated on rows
// that could not exist.
//
// The first fix made the board DISPLAY derived cards. That left the finding's
// actual defect in place and turned it into a worse one: a derived card is
// served with ID 0, and MoveCard was `UPDATE board_cards ... WHERE id=?`, so
// moving one matched no rows, returned no error, and answered HTTP 200 while
// discarding the move. The card looked movable and was not. Nothing tested it --
// handleMoveCard had no test and no client called it.
//
// These use boardFixture and makeFeature from findings_20261002_test.go: a
// feature cannot be created without a validated complaint, which is the system's
// own rule and not something to reimplement in a fixture.

// countBoardCards is the row count the finding turned on.
func countBoardCards(t *testing.T, d *DB, projectID int64) int {
	t.Helper()
	var n int
	if err := d.QueryRowContext(context.Background(),
		`SELECT count(*) FROM board_cards WHERE project_id=?`, projectID).Scan(&n); err != nil {
		t.Fatalf("count board_cards: %v", err)
	}
	return n
}

// findCard returns the board card for a feature.
func findCard(t *testing.T, d *DB, projectID, featureID int64) BoardCard {
	t.Helper()
	_, cards, err := d.GetBoard(context.Background(), projectID)
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	for _, c := range cards {
		if c.FeatureID == featureID {
			return c
		}
	}
	t.Fatalf("feature %d is not on the board (cards: %d)", featureID, len(cards))
	return BoardCard{}
}

// The core of F2: a move has to be able to CREATE the first row. Before, nothing
// in the codebase inserted into board_cards.
func TestMoveDerivedFeatureCreatesTheFirstRow(t *testing.T) {
	d, projectID, uid := boardFixture(t, "board-move-create")
	ctx := context.Background()

	f := makeFeature(t, d, projectID, uid, "first thing to place", "draft")
	if n := countBoardCards(t, d, projectID); n != 0 {
		t.Fatalf("board_cards should start empty for this project, has %d", n)
	}

	if err := d.MoveCard(ctx, 0, f.ID, "triaged", projectID); err != nil {
		t.Fatalf("MoveCard on a derived feature: %v", err)
	}

	if n := countBoardCards(t, d, projectID); n != 1 {
		t.Fatalf("the move did not create a row: board_cards has %d rows, want 1. "+
			"This is F2 -- no code path inserted a card, so the table stayed empty "+
			"and every move was silently discarded", n)
	}

	card := findCard(t, d, projectID, f.ID)
	if card.Column != "triaged" {
		t.Errorf("card is in %q, want triaged", card.Column)
	}
	if card.Derived {
		t.Error("the card is still served as derived; once it has a row it is an " +
			"explicit placement and the derived guess no longer applies")
	}
	if card.ID == 0 {
		t.Error("a card backed by a row has no id, so it cannot be addressed again")
	}
}

// Moving the same feature twice must not accumulate rows -- which is what
// migration 0020's unique index on (project_id, feature_id) guarantees.
func TestMoveIsIdempotentPerFeature(t *testing.T) {
	d, projectID, uid := boardFixture(t, "board-move-idempotent")
	ctx := context.Background()
	f := makeFeature(t, d, projectID, uid, "moved repeatedly", "draft")

	for _, phase := range []string{"triaged", "solution_draft", "review"} {
		if err := d.MoveCard(ctx, 0, f.ID, phase, projectID); err != nil {
			t.Fatalf("MoveCard to %s: %v", phase, err)
		}
	}

	if n := countBoardCards(t, d, projectID); n != 1 {
		t.Errorf("three moves of one feature produced %d rows, want 1; a repeat "+
			"move must update the row the first one created", n)
	}
	if got := findCard(t, d, projectID, f.ID).Column; got != "review" {
		t.Errorf("card is in %q after the last move, want review", got)
	}
}

// A derived card is addressed by feature id, so the feature has to be named.
// Inferring it -- picking the lowest-id status-bearing feature -- moves the wrong
// card as soon as a project has two. An earlier version of this fix did that.
func TestMoveDerivedFeatureMovesTheNamedFeatureNotTheFirst(t *testing.T) {
	d, projectID, uid := boardFixture(t, "board-move-named")

	first := makeFeature(t, d, projectID, uid, "lower id", "draft")
	second := makeFeature(t, d, projectID, uid, "higher id", "draft")
	if first.ID >= second.ID {
		t.Fatalf("test assumes the first id is lower: %d vs %d", first.ID, second.ID)
	}

	if err := d.MoveCard(context.Background(), 0, second.ID, "consensus", projectID); err != nil {
		t.Fatalf("MoveCard: %v", err)
	}

	if got := findCard(t, d, projectID, second.ID).Column; got != "consensus" {
		t.Errorf("feature %d is in %q, want consensus", second.ID, got)
	}
	// The one that was NOT named must still be derived in its own column. If the
	// implementation inferred the feature, this is the card that gets moved.
	if got := findCard(t, d, projectID, first.ID).Column; got == "consensus" {
		t.Errorf("feature %d was moved to consensus, but the caller named feature "+
			"%d; the wrong card was placed", first.ID, second.ID)
	}
}

// A move to a phase that does not exist is a client mistake. It used to write
// column_id=NULL and fail the NOT NULL constraint, surfacing as a 500.
func TestMoveToUnknownPhaseIsNotFoundNotInternal(t *testing.T) {
	d, projectID, uid := boardFixture(t, "board-move-badphase")
	f := makeFeature(t, d, projectID, uid, "bad phase", "draft")

	err := d.MoveCard(context.Background(), 0, f.ID, "no-such-phase", projectID)
	if err == nil {
		t.Fatal("moving to a phase that does not exist succeeded")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want an error wrapping ErrNotFound so it maps to 404; a "+
			"typo in the phase is a client mistake, not a server fault", err)
	}
}

// A move naming a card that does not exist must say so. The bare UPDATE this
// replaced matched zero rows and returned nil, so a caller could not tell a real
// move from a discarded one.
func TestMoveUnknownCardIsNotFound(t *testing.T) {
	d, projectID, _ := boardFixture(t, "board-move-unknown")

	err := d.MoveCard(context.Background(), 999999, 0, "triaged", projectID)
	if err == nil {
		t.Fatal("moving a card that does not exist reported success")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want an error wrapping ErrNotFound", err)
	}
}

// A feature from another project must not be placeable into this one's board.
//
// Both projects come from ONE fixture. Calling boardFixture twice opens a second
// in-memory database -- setup() runs db.Open(":memory:") per call -- so the two
// projects would not have shared a board at all, and the "foreign" feature would
// not have existed in the database under test. The first version of this test
// did that and passed for the wrong reason.
func TestMoveFeatureFromAnotherProjectIsRejected(t *testing.T) {
	ctx := context.Background()
	d, mine, uid := boardFixture(t, "board-move-owner-mine")

	theirs, err := d.CreateProject(ctx, uid, "board-move-owner-theirs",
		"board-move-owner-theirs", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject (second): %v", err)
	}

	foreign := makeFeature(t, d, theirs.ID, uid, "not yours", "draft")
	if foreign.ProjectID == mine {
		t.Fatalf("fixture is wrong: the foreign feature belongs to project %d, "+
			"which is the board under test", mine)
	}

	err = d.MoveCard(ctx, 0, foreign.ID, "triaged", mine)
	if err == nil {
		t.Fatal("a feature from another project was placed into this project's board")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want an error wrapping ErrNotFound", err)
	}
	if n := countBoardCards(t, d, mine); n != 0 {
		t.Errorf("the rejected move left %d row(s) behind", n)
	}
}

// Naming neither a card nor a feature is a client mistake, not a silent no-op.
func TestMoveWithoutIdentifyingACardIsInvalid(t *testing.T) {
	d, projectID, _ := boardFixture(t, "board-move-nothing")

	if err := d.MoveCard(context.Background(), 0, 0, "triaged", projectID); !errors.Is(err, ErrInvalid) {
		t.Errorf("got %v, want an error wrapping ErrInvalid", err)
	}
}

// Naming both is ambiguous: which one did the caller mean?
func TestMoveNamingBothCardAndFeatureIsInvalid(t *testing.T) {
	d, projectID, uid := boardFixture(t, "board-move-both")
	f := makeFeature(t, d, projectID, uid, "ambiguous", "draft")

	err := d.MoveCard(context.Background(), 1, f.ID, "triaged", projectID)
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("got %v, want an error wrapping ErrInvalid", err)
	}
}

// A placed card has a real id and moves by row id; the feature path is not a
// second way to do the same thing.
func TestMovePlacedRowByID(t *testing.T) {
	d, projectID, uid := boardFixture(t, "board-move-row")
	ctx := context.Background()
	f := makeFeature(t, d, projectID, uid, "has a row", "draft")

	if err := d.MoveCard(ctx, 0, f.ID, "triaged", projectID); err != nil {
		t.Fatalf("first placement: %v", err)
	}
	cardID := findCard(t, d, projectID, f.ID).ID
	if cardID == 0 {
		t.Fatal("the placed card has no id")
	}

	if err := d.MoveCard(ctx, cardID, 0, "done", projectID); err != nil {
		t.Fatalf("MoveCard by row id: %v", err)
	}
	if got := findCard(t, d, projectID, f.ID).Column; got != "done" {
		t.Errorf("card is in %q, want done", got)
	}
}

// The board must not show a card twice: once derived and once from the row the
// first move created. GetBoard filters derived cards by placed feature, and this
// is what catches that filter regressing.
func TestPlacingAFeatureDoesNotDuplicateItsCard(t *testing.T) {
	d, projectID, uid := boardFixture(t, "board-move-no-dup")
	ctx := context.Background()
	f := makeFeature(t, d, projectID, uid, "placed once", "draft")

	if err := d.MoveCard(ctx, 0, f.ID, "triaged", projectID); err != nil {
		t.Fatalf("MoveCard: %v", err)
	}

	_, cards, err := d.GetBoard(ctx, projectID)
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	seen := 0
	for _, c := range cards {
		if c.FeatureID == f.ID {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("feature %d appears on the board %d times, want 1; a placed "+
			"feature must not also appear as a derived card", f.ID, seen)
	}
}