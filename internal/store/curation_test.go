package store

import (
	"context"
	"errors"
	"strconv"
	"testing"
)

// The queue is a union of three tables that were never designed to be listed together, so the
// first thing to pin is that each kind survives the projection with its own action and target
// intact -- a UNION that silently projected NULLs would still "return three rows".
func TestCurationListsEveryKindNewestFirst(t *testing.T) {
	d, uid, pid, fID := arenaFixture(t)
	ctx := context.Background()

	if _, err := d.ProposeStrategicWeight(ctx, pid, fID, uid, 2.5, "the release theme"); err != nil {
		t.Fatalf("ProposeStrategicWeight: %v", err)
	}
	if _, err := d.ProposeTaxonomyChange(ctx, 0, uid, "create", "topic:kanban", "", "the board's vocabulary"); err != nil {
		t.Fatalf("ProposeTaxonomyChange: %v", err)
	}
	a, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	if err := d.UpsertArenaEntry(ctx, a.ID, "feature", fID); err != nil {
		t.Fatalf("UpsertArenaEntry: %v", err)
	}
	if _, err := d.RemoveFeatureFromArena(ctx, pid, a.ID, fID, uid, "spam"); err != nil {
		t.Fatalf("RemoveFeatureFromArena: %v", err)
	}

	page, err := d.ListCuration(ctx, CurationFilter{})
	if err != nil {
		t.Fatalf("ListCuration: %v", err)
	}
	seen := map[string]bool{}
	for _, it := range page.Items {
		seen[it.Kind] = true
		if it.Summary == "" {
			t.Errorf("%s/%d has no summary: the union projected a NULL where the row has text", it.Kind, it.ID)
		}
		if it.ID == 0 {
			t.Errorf("%s item has no id", it.Kind)
		}
	}
	for _, kind := range []string{"taxonomy", "strategy", "act"} {
		if !seen[kind] {
			t.Errorf("kind %q is missing from the queue", kind)
		}
	}
	for i := 1; i < len(page.Items); i++ {
		if page.Items[i].OpenedAt > page.Items[i-1].OpenedAt {
			t.Fatalf("not newest first at %d: %v after %v", i, page.Items[i].OpenedAt, page.Items[i-1].OpenedAt)
		}
	}
}

// "Not voted by me" is the queue's default in the donor, and it is the difference between a
// work list and a wall. It must exclude a reader's own proposal too, or the queue never empties.
//
// The consent row is written directly rather than through RatifyTaxonomyProposal: eligibility
// (role AND recent activity, §8.2) is that function's business and has its own tests, and a
// fixture that has to manufacture a qualifying contribution to ask this question would be
// testing the wrong thing.
func TestCurationCanExcludeWhatIHaveAlreadyVotedOn(t *testing.T) {
	d, uid, _, _ := arenaFixture(t)
	ctx := context.Background()
	p, err := d.ProposeTaxonomyChange(ctx, 0, uid, "create", "topic:kanban", "", "why")
	if err != nil {
		t.Fatalf("ProposeTaxonomyChange: %v", err)
	}
	reader, err := d.CreateUser(ctx, "reviewer-curation", "Reviewer")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO taxonomy_consents (proposal_id, user_id, consented_at) VALUES (?,?,?)`,
		p.ID, reader.ID, float64(1)); err != nil {
		t.Fatalf("seed consent: %v", err)
	}

	unvoted, err := d.ListCuration(ctx, CurationFilter{UnvotedBy: reader.ID, ViewerID: reader.ID})
	if err != nil {
		t.Fatalf("ListCuration(unvoted): %v", err)
	}
	for _, it := range unvoted.Items {
		if it.Kind == "taxonomy" && it.ID == p.ID {
			t.Fatal("a proposal the reader consented to is still in their work list")
		}
	}

	// And the same reader's own view must report the position they took.
	all, err := d.ListCuration(ctx, CurationFilter{ViewerID: reader.ID})
	if err != nil {
		t.Fatalf("ListCuration(viewer): %v", err)
	}
	var found bool
	for _, it := range all.Items {
		if it.Kind == "taxonomy" && it.ID == p.ID {
			found = true
			if it.MyVote != 1 {
				t.Fatalf("my_vote is %d, want 1 for a proposal this reader consented to", it.MyVote)
			}
			if it.Yes != 1 {
				t.Fatalf("yes is %d, want 1", it.Yes)
			}
		}
	}
	if !found {
		t.Fatal("the proposal is missing from the unfiltered list")
	}
}

// A cursor that is really an offset repeats or skips rows the moment a new act lands. This
// inserts one between two page reads and asserts the union of the pages is exactly the rows
// that existed when paging started.
func TestCurationCursorDoesNotRepeatOrSkip(t *testing.T) {
	d, uid, _, _ := arenaFixture(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := d.ProposeTaxonomyChange(ctx, 0, uid, "create", "topic:tag"+strconv.Itoa(i), "", "r"); err != nil {
			t.Fatalf("propose %d: %v", i, err)
		}
	}
	first, err := d.ListCuration(ctx, CurationFilter{Limit: 2})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("page 1 returned %d items, cursor %q", len(first.Items), first.NextCursor)
	}
	// Inserted after page 1 was read, and a second later -- a row that arrives LATER must not
	// appear in pages already begun. Two rows inside the same second are ordered by (kind, id)
	// and may legitimately cross a page boundary; that is the cursor working, not skipping.
	late, err := d.ProposeTaxonomyChange(ctx, 0, uid, "create", "topic:late", "", "r")
	if err != nil {
		t.Fatalf("propose late: %v", err)
	}
	if _, err := d.ExecContext(ctx, `UPDATE taxonomy_proposals SET created_at = created_at + 1 WHERE id = ?`, late.ID); err != nil {
		t.Fatalf("move the late proposal forward: %v", err)
	}
	seen := map[string]bool{}
	for _, it := range first.Items {
		seen[it.Kind+"/"+strconv.FormatInt(it.ID, 10)] = true
	}
	total := len(first.Items)
	cursor := first.NextCursor
	for page := 0; page < 5 && cursor != ""; page++ {
		p, err := d.ListCuration(ctx, CurationFilter{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", page+2, err)
		}
		for _, it := range p.Items {
			k := it.Kind + "/" + strconv.FormatInt(it.ID, 10)
			if seen[k] {
				t.Fatalf("row %s came back twice across pages", k)
			}
			seen[k] = true
			total++
		}
		cursor = p.NextCursor
	}
	if total != 5 {
		t.Fatalf("paged through %d rows, want the 5 that existed when paging began", total)
	}
}

// A malformed cursor must be refused, not silently treated as the first page: a silent first
// page turns a paging bug into an infinite scroll.
func TestCurationRefusesAMalformedCursor(t *testing.T) {
	d, _, _, _ := arenaFixture(t)
	if _, err := d.ListCuration(context.Background(), CurationFilter{Cursor: "nonsense"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed cursor: got %v, want ErrInvalid", err)
	}
}

// Two reviews from one reader must collapse to one row, and an actor must not review their own
// act -- the count is the only thing the queue shows, so a double row is a lie about agreement.
func TestReviewingAnActIsOneRowPerReader(t *testing.T) {
	d, uid, pid, fID := arenaFixture(t)
	ctx := context.Background()
	a, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	if err := d.UpsertArenaEntry(ctx, a.ID, "feature", fID); err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	act, err := d.RemoveFeatureFromArena(ctx, pid, a.ID, fID, uid, "spam")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	other, err := d.CreateUser(ctx, "reviewer-act", "Reviewer")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if yes, no, err := d.ReviewAct(ctx, act.ID, other.ID, 1, "agreed"); err != nil || yes != 1 || no != 0 {
		t.Fatalf("first review: yes=%d no=%d err=%v", yes, no, err)
	}
	if yes, no, err := d.ReviewAct(ctx, act.ID, other.ID, -1, "changed my mind"); err != nil || yes != 0 || no != 1 {
		t.Fatalf("after changing their mind: yes=%d no=%d err=%v", yes, no, err)
	}
	if _, _, err := d.ReviewAct(ctx, act.ID, uid, 1, "self"); !errors.Is(err, ErrPerm) {
		t.Fatalf("self review: got %v, want ErrPerm", err)
	}
}
