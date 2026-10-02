package store

import (
	"context"
	"errors"
	"testing"
)

// Regression tests for the board finding in the site-wide e2e sweep of
// 2026-10-02 (20-Areas/concord-site-e2e-verification.md, F2).
//
// board_cards had no writer anywhere in the codebase, so it held zero rows for
// all 70 projects while MoveCard and GetBoardCard both operated on rows that
// could not exist. Every board rendered nine columns of "0", and a user could
// not tell "no work" from "broken". GetBoard now derives cards from each
// feature's own workflow status.

// boardFixture makes a project with a board and a user to own it.
//
// Built on setupWithUser rather than setupWithProject because that helper
// hardcodes the slug "test-project", and calling it per-test in one package is
// fine but a second project in the same DB collides on the UNIQUE constraint.
func boardFixture(t *testing.T, slug string) (*DB, int64, int64) {
	t.Helper()
	ctx := context.Background()
	d, uid := setupWithUser(t)

	proj, err := d.CreateProject(ctx, uid, slug, slug, "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return d, proj.ID, uid
}

// validatedComplaint makes one validated complaint. A feature cannot be created
// without linking a validated complaint -- that is the system's own rule, not a
// fixture convenience -- so every feature fixture starts here.
func validatedComplaint(t *testing.T, d *DB, projectID, authorID int64, title string) int64 {
	t.Helper()
	ctx := context.Background()
	comp, err := d.CreateComplaint(ctx, projectID, authorID, title, "body", 3, 0.8, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint %q: %v", title, err)
	}
	if err := d.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint %q: %v", title, err)
	}
	return comp.ID
}

// makeFeature creates a feature and sets its status.
func makeFeature(t *testing.T, d *DB, projectID, authorID int64, title, status string) Feature {
	t.Helper()
	ctx := context.Background()
	compID := validatedComplaint(t, d, projectID, authorID, title+" complaint")
	f, err := d.CreateFeature(ctx, projectID, authorID, title, "body", "", nil, nil, []int64{compID})
	if err != nil {
		t.Fatalf("CreateFeature %q: %v", title, err)
	}
	if status != "" {
		if err := d.UpdateFeatureStatus(ctx, f.ID, status); err != nil {
			t.Fatalf("UpdateFeatureStatus %q: %v", status, err)
		}
	}
	return f
}

// F2: a board shows the work a project has, derived from feature status.
func TestBoardDerivesCardsFromFeatureStatus(t *testing.T) {
	d, projectID, uid := boardFixture(t, "board-derivation-project")

	// draft maps to inbox and shipped to done -- neither name is a column, which
	// is the point: the mapping is explicit rather than an identity match.
	makeFeature(t, d, projectID, uid, "first draft", "draft")
	makeFeature(t, d, projectID, uid, "already shipped", "shipped")

	columns, cards, err := d.GetBoard(context.Background(), projectID)
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if len(columns) == 0 {
		t.Fatal("no columns; the fixture did not set up a board")
	}
	if len(cards) != 2 {
		t.Fatalf("cards = %d, want 2 derived from feature status", len(cards))
	}
	for _, c := range cards {
		if !c.Derived {
			t.Errorf("card for feature %d is not marked derived", c.FeatureID)
		}
		// Without the title the board renders "Feature #<id>", which is the bare
		// number the finding complained about in a different form.
		if c.Title == "" {
			t.Errorf("card for feature %d has no title", c.FeatureID)
		}
		// A derived card has no row, so it must not look movable: a non-zero ID
		// would make MoveCard target a row that is not there.
		if c.ID != 0 {
			t.Errorf("derived card has ID %d; a derived card has no row", c.ID)
		}
	}
}

// F2: a status whose mapped phase is not a column on this project is omitted
// rather than forced somewhere. Guessing a phase would be the same fabrication
// one level down.
//
// The mapping table is exercised by TestBoardDerivesCardsFromFeatureStatus; this
// covers the second guard, which is what keeps the derivation honest on a project
// whose board is missing columns.
func TestBoardOmitsStatusWhosePhaseIsNotAColumn(t *testing.T) {
	ctx := context.Background()
	d, projectID, uid := boardFixture(t, "board-missing-columns-project")

	// Remove one column so a status that maps to it has nowhere to land.
	if _, err := d.ExecContext(ctx,
		`DELETE FROM board_columns WHERE project_id=? AND phase='done'`, projectID); err != nil {
		t.Fatalf("delete done column: %v", err)
	}

	// shipped maps to done. The mapping is valid; the column is not there.
	makeFeature(t, d, projectID, uid, "shipped, but no done column", "shipped")
	makeFeature(t, d, projectID, uid, "ready, and ready is a column", "ready")

	columns, err := d.GetBoardColumns(ctx, projectID)
	if err != nil {
		t.Fatalf("GetBoardColumns: %v", err)
	}
	for _, c := range columns {
		if c.Name == "done" {
			t.Fatal("fixture failed to remove the done column")
		}
	}

	_, cards, err := d.GetBoard(ctx, projectID)
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("cards = %d, want 1 -- a card with no column must be omitted", len(cards))
	}
	if cards[0].Title != "ready, and ready is a column" {
		t.Errorf("card title = %q, want the feature whose phase is a column", cards[0].Title)
	}
}

// F2: an explicit placement must beat the derived one. If the derived card also
// appeared, a move would look like it worked and then revert on the next load.
func TestExplicitPlacementSuppressesDerivedCard(t *testing.T) {
	ctx := context.Background()
	d, projectID, uid := boardFixture(t, "board-placement-wins-project")

	columns, err := d.GetBoardColumns(ctx, projectID)
	if err != nil {
		t.Fatalf("GetBoardColumns: %v", err)
	}
	// The feature's status (draft) maps to inbox, so place it somewhere else and
	// assert the explicit placement wins over that mapping.
	target := ""
	for _, c := range columns {
		if c.Name != "inbox" {
			target = c.Name
			break
		}
	}
	if target == "" {
		t.Fatal("fixture has no column other than inbox")
	}

	f := makeFeature(t, d, projectID, uid, "placed by hand", "draft")

	// Place it in a column its own status does not name.
	var columnID int64
	if err := d.QueryRowContext(ctx,
		`SELECT id FROM board_columns WHERE project_id=? AND phase=?`,
		projectID, target).Scan(&columnID); err != nil {
		t.Fatalf("resolve column id: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		// kind is NOT NULL and CHECKed; the column set was added by whoever
		// designed the table to hold complaints as well as features.
		`INSERT INTO board_cards (project_id, kind, feature_id, column_id, entered_at)
		 VALUES (?, 'feature', ?, ?, ?)`,
		projectID, f.ID, columnID, 1.0); err != nil {
		t.Fatalf("insert explicit card: %v", err)
	}

	_, cards, err := d.GetBoard(ctx, projectID)
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("cards = %d, want 1 -- a placed feature must not also appear derived", len(cards))
	}
	if cards[0].Derived {
		t.Error("card is marked derived despite an explicit row existing")
	}
	if cards[0].Column != target {
		t.Errorf("column = %q, want %q; the explicit placement was ignored",
			cards[0].Column, target)
	}
}

// F2: a project with no features gets an empty board, not a broken one. This is
// the state the finding was reported from, so it needs its own assertion.
func TestBoardWithNoFeaturesIsEmptyNotBroken(t *testing.T) {
	d, projectID, _ := boardFixture(t, "board-no-features-project")

	columns, cards, err := d.GetBoard(context.Background(), projectID)
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if len(cards) != 0 {
		t.Errorf("cards = %d, want 0 for a project with no features", len(cards))
	}
	if len(columns) == 0 {
		t.Error("columns = 0; the board columns should still exist")
	}
}

// F1: "need at least 2 features to vote" was a bare fmt.Errorf, so mapError fell
// through to `default` and answered HTTP 500. A project with one feature -- which
// `concord` itself is -- showed an error with a Try again button that could never
// succeed. ErrConflict is exactly this situation: a valid request the current
// state refuses, which a retry cannot change.
func TestFewerThanTwoFeaturesIsNotAServerFault(t *testing.T) {
	ctx := context.Background()
	d, projectID, uid := boardFixture(t, "solo-feature-project")

	makeFeature(t, d, projectID, uid, "the only feature", "draft")

	_, _, err := d.GetNextPair(ctx, projectID, uid)
	if err == nil {
		t.Fatal("GetNextPair succeeded with one feature; want an error")
	}
	if !errors.Is(err, ErrConflict) {
		t.Errorf("GetNextPair error = %v, want it to wrap ErrConflict so mapError "+
			"answers 4xx rather than 500", err)
	}
}