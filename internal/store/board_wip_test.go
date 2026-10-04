package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// THE WIP LIMIT WAS DISPLAYED AND NEVER ENFORCED.
//
// wip_limit was written by SetBoardColumn, read by GetBoard, and printed by the board
// page as "n / WIP k" -- and nothing consulted it on a write. So the column header said
// a limit was in force while the server accepted any number of cards into it. A limit
// that is only displayed is worse than none: it teaches readers that the board is
// policed, and then lets the board grow.
//
// These four tests are what that claim rests on.

// wipBoard builds a board with a WIP limit on the "capped" column and two cards already
// in it, so the column is exactly at its limit of 2. Returns the id of the card that
// must be refused, and of one already inside.
func wipBoard(t *testing.T, st *DB, uid, pid int64) (spare, inside int64) {
	t.Helper()
	if _, err := st.ExecContext(context.Background(), `
		INSERT INTO board_columns (project_id, phase, position, wip_limit)
		VALUES (?, 'backlog', 0, NULL), (?, 'capped', 1, 2)`, pid, pid); err != nil {
		t.Fatalf("columns: %v", err)
	}
	var col int64
	if err := st.QueryRowContext(context.Background(),
		`SELECT id FROM board_columns WHERE project_id=? AND phase='capped'`,
		pid).Scan(&col); err != nil {
		t.Fatalf("capped column: %v", err)
	}
	if _, err := st.ExecContext(context.Background(),
		`INSERT INTO features (project_id, title, body, status, author_id, created_at, updated_at)
		 VALUES (?, 'one', '', 'in_progress', ?, ?, ?), (?, 'two', '', 'in_progress', ?, ?, ?),
		        (?, 'three', '', 'in_progress', ?, ?, ?)`,
		pid, uid, 1.0, 1.0, pid, uid, 1.0, 1.0, pid, uid, 1.0, 1.0); err != nil {
		t.Fatalf("features: %v", err)
	}
	rows, err := st.QueryContext(context.Background(),
		`SELECT id FROM features WHERE project_id=? ORDER BY id`, pid)
	if err != nil {
		t.Fatalf("features: %v", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	rows.Close()

	// BOTH cards go in by FEATURE id -- the placeFeature path -- which is what the
	// original fixture did and what left moveRow unverified.
	//
	// The order was wrong on the first attempt: it looked up the card id BEFORE
	// creating any card, so every test failed at "no rows in result set" and the
	// limit itself was never exercised.
	for _, id := range ids[:2] {
		if err := st.MoveCard(context.Background(), 0, id, "capped", pid); err != nil {
			t.Fatalf("seed card: %v", err)
		}
	}
	// Now that the rows exist, the CARD IDs are available for the moveRow tests.
	var firstCard int64
	if err := st.QueryRowContext(context.Background(),
		`SELECT id FROM board_cards WHERE feature_id=?`, ids[0]).Scan(&firstCard); err != nil {
		t.Fatalf("seeded card id: %v", err)
	}
	return ids[2], firstCard
}

// TestTheWipLimitIsEnforcedAndSaysWhy: the limit refuses the third card, with the count
// and the limit in the message.
func TestTheWipLimitIsEnforcedAndSaysWhy(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	third, _ := wipBoard(t, store, uid, pid)

	err := store.MoveCard(context.Background(), 0, third, "capped", pid)
	if err == nil {
		t.Fatal("a third card was accepted into a column whose limit is 2")
	}
	// ErrConflict, so the HTTP layer answers 409 rather than blaming the request.
	if !errors.Is(err, ErrConflict) {
		t.Errorf("got %v, want ErrConflict; a 400 would tell the reader they made a "+
			"mistake in their request when it is the board's state that refuses", err)
	}
	// And the message says what is full and by how much, because "WIP limit reached"
	// is not something a reader can act on.
	for _, want := range []string{"capped", "2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %q", want, err)
		}
	}
}

// TestAColumnWithNoLimitTakesAnyNumberOfCards: NULL is "no limit", not "zero".
func TestAColumnWithNoLimitTakesAnyNumberOfCards(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	third, _ := wipBoard(t, store, uid, pid)

	// The limit is 2 and the column holds 2; without the NULL exemption this would
	// refuse, because a NULL scanned as 0 means "take none".
	if err := store.MoveCard(context.Background(), 0, third, "backlog", pid); err != nil {
		t.Errorf("a column with no wip_limit refused a card: %v", err)
	}
}

// TestRedroppingACardOnItsOwnColumnIsNotARefusal.
//
// Without the "already here" exemption, re-dropping a card reported the column as full
// -- a false refusal about a card that is exactly where it already was, which is what a
// drag-and-drop UI does constantly.
func TestRedroppingACardOnItsOwnColumnIsNotARefusal(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	_, first := wipBoard(t, store, uid, pid)

	for i := 0; i < 3; i++ {
		if err := store.MoveCard(context.Background(), 0, first, "capped", pid); err != nil {
			t.Fatalf("re-drop %d: %v; the card is already in this column and the "+
				"count does not change", i, err)
		}
	}
}

// TestAFreedSlotLetsTheNextCardIn: the limit is a moving target, not a permanent
// refusal, so a card can leave and make room.
func TestAFreedSlotLetsTheNextCardIn(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	third, first := wipBoard(t, store, uid, pid)

	if err := store.MoveCard(context.Background(), 0, third, "capped", pid); err == nil {
		t.Fatal("expected the capped column to be full")
	}

	// Move one out by CARD ID -- the path that skips placeFeature's own check for a
	// column with no limit.
	backlog, err := store.boardColumnID(context.Background(), pid, "backlog")
	if err != nil {
		t.Fatalf("backlog id: %v", err)
	}
	if _, err := store.ExecContext(context.Background(),
		`UPDATE board_cards SET column_id=? WHERE feature_id=?`, backlog, first); err != nil {
		t.Fatalf("free a slot: %v", err)
	}

	if err := store.MoveCard(context.Background(), 0, third, "capped", pid); err != nil {
		t.Errorf("after a card left, the next was still refused: %v", err)
	}
}

// TestTheWipLimitIsEnforcedOnTheCardIdPathToo.
//
// moveRow and placeFeature are separate code paths, so the limit check is added to
// each. A mutation run caught this: removing the check from moveRow left the suite
// GREEN, because every test seeded and moved through placeFeature. So this one moves a
// card that ALREADY HAS A ROW -- which is what a drag on a placed card always does.
func TestTheWipLimitIsEnforcedOnTheCardIdPathToo(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	spare, inside := wipBoard(t, store, uid, pid)

	// A fourth feature, placed in the backlog so it HAS a row.
	//
	// `spare` -- the THIRD feature wipBoard created -- is used rather than a fourth:
	// the first version inserted one titled 'four' and then looked the card up by that
	// title, but features.title is not unique and two rows already said 'four' in
	// setupWithProject's fixture, so the lookup matched nothing. A handle from the
	// INSERT is the only reliable way to name a row you just made.
	if err := store.MoveCard(context.Background(), 0, spare, "backlog", pid); err != nil {
		t.Fatalf("place the spare card in the backlog: %v", err)
	}
	var spareCard int64
	if err := store.QueryRowContext(context.Background(),
		`SELECT id FROM board_cards WHERE feature_id=?`, spare).Scan(&spareCard); err != nil {
		t.Fatalf("spare card id: %v", err)
	}
	_ = inside

	// ...and moved INTO the capped column BY ITS CARD ID. The capped column holds 2.
	err := store.MoveCard(context.Background(), spareCard, 0, "capped", pid)
	if err == nil {
		t.Fatal("a card with an existing row was moved past the WIP limit by card id")
	}
	if !errors.Is(err, ErrConflict) {
		t.Errorf("got %v, want ErrConflict", err)
	}
}
