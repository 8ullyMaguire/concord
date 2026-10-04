package httpapi

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The board could not be moved on.
//
// handleMoveCard, MoveCard, PUT .../board/cards/{id}/move and the phase-gate and
// WIP-limit refusals all existed and were tested at the store layer. The board rendered
// columns and cards and offered NO way to move one: a kanban board nobody can move a
// card on is a read-only list wearing a board's layout.
//
// These three tests are the source half; tests/e2e/board_e2e.py moves a real card.

// TestTheBoardOffersThreeWaysToMoveACard.
//
// Drag alone is not an interface: it requires a pointer, it is unusable with a screen
// reader (the drop target is never announced), and it fails silently when
// dataTransfer.setData is missing. So the board needs a keyboard path as well.
func TestTheBoardOffersThreeWaysToMoveACard(t *testing.T) {
	src, err := os.ReadFile("assets/js/board.js")
	if err != nil {
		t.Fatalf("read board.js: %v", err)
	}
	body := string(src)

	for _, want := range []struct{ needle, why string }{
		{"/move?to=", "the move is never requested; the route exists and is unused"},
		{"kanban-card-move", "no accessible control: the board would need a mouse"},
		{"ArrowRight", "no keyboard path: arrows are how the board is operated " +
			"without a pointer at all"},
		{"dataTransfer.setData", "without this the drop handler's getData is empty " +
			"and dragging silently does nothing"},
		{"dragover", "without a dragover handler calling preventDefault the drop " +
			"never fires, silently"},
	} {
		if !strings.Contains(body, want.needle) {
			t.Errorf("board.js missing %q: %s", want.needle, want.why)
		}
	}
}

// TestACardIsAButtonNotADiv.
//
// A draggable div is not focusable and announces nothing, so the board is unusable by
// keyboard even with arrow-key handlers attached -- there is no element to focus.
// This is asserted as `button.kanban-card` because a bare "kanban-card" also matches
// the CSS selector and the label.
func TestACardIsAButtonNotADiv(t *testing.T) {
	src, err := os.ReadFile("assets/js/board.js")
	if err != nil {
		t.Fatalf("read board.js: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, `<button type="button" class="kanban-card"`) {
		t.Error("a card is not rendered as a <button>; a draggable div is not " +
			"focusable and announces nothing, so the arrow-key handlers would have " +
			"no element to attach to")
	}
	// And it names its column, so a screen reader says where the card is.
	if !strings.Contains(body, "aria-label") {
		t.Error("a card carries no aria-label; a screen reader would announce only " +
			"the title and the reader would not know which column they were in")
	}
}

// TestTheBoardIsReReadAfterAMoveRatherThanReordered.
//
// The move response is {"status":"moved"}, which says nothing about whether a phase
// GATE or a WIP LIMIT refused it. Reordering the card locally is how a board ends up
// showing a card in a column the server refused, and it looks correct.
//
// So the assertion is on the ORDER of operations: re-read, THEN announce. Announcing
// first meant the confirmation was wiped by the very repaint that confirmed it, because
// the live region lives inside the element whose innerHTML is replaced.
func TestTheBoardIsReReadAfterAMoveRatherThanReordered(t *testing.T) {
	src, err := os.ReadFile("assets/js/board.js")
	if err != nil {
		t.Fatalf("read board.js: %v", err)
	}
	body := string(src)

	hydrateAt := strings.Index(body, "hydrate(slug, box, true)")
	announceAt := strings.Index(body, "announce(box, 'Moved to '")
	if hydrateAt < 0 || announceAt < 0 {
		t.Fatalf("board.js does not re-read and announce after a move (hydrate=%d, "+
			"announce=%d)", hydrateAt, announceAt)
	}
	if hydrateAt > announceAt {
		t.Error("board.js announces BEFORE re-reading; the repaint replaces the " +
			"element the live region lives in, so the confirmation is wiped by the " +
			"render that confirms it")
	}
	if !strings.Contains(body, "hydrate(slug, box, true).then(") {
		t.Error("the re-read is not chained, so announce() fires on a discarded " +
			"promise and the accessible path is silent on every successful move")
	}
	// And the refusal path must surface the SERVER'S message: "that phase has a gate"
	// is something the reader can act on, and a generic failure tells them nothing.
	if !strings.Contains(body, "res.body && res.body.error") {
		t.Error("a refused move does not surface the server's message; the store " +
			"explains the gate or the WIP limit and a generic string discards it")
	}
}

// TestTheWIPLimitIsVisibleBeforeADragIsAttempted.
func TestTheWIPLimitIsVisibleBeforeADragIsAttempted(t *testing.T) {
	src, err := os.ReadFile("assets/js/board.js")
	if err != nil {
		t.Fatalf("read board.js: %v", err)
	}
	body := string(src)
	// The header already renders "n / WIP k". What was missing is the STATE, so a
	// reader sees the limit has been reached instead of discovering it by being
	// refused after the drag.
	if !strings.Contains(body, "is-at-wip") {
		t.Error("a column at its WIP limit is not marked; the reader finds out by " +
			"being refused, which reads as the board being broken")
	}
	if !strings.Contains(body, "col.wip && inCol.length >= col.wip") {
		t.Error("board.js does not compare the card count against the column's WIP " +
			"limit, so is-at-wip is never applied")
	}
}

// TestTheBoardScriptParses.
func TestTheBoardScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	path := "assets/js/board.js"
	if out, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
		t.Errorf("node --check %s: %v\n%s", path, err, out)
	}
}
