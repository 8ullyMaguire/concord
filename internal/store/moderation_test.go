package store

import (
	"context"
	"errors"
	"testing"
)

// The act and its reversal are one test because the property that matters is the pair: after
// remove+restore the entry's four numbers and the feature's status are exactly what they
// were, and the log holds two rows that say so.
func TestRemovingAndRestoringAnArenaEntryKeepsTheNumbers(t *testing.T) {
	d, uid, pid, fID := arenaFixture(t)
	ctx := context.Background()

	a, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	if err := d.UpsertArenaEntry(ctx, a.ID, "feature", fID); err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	// UpsertArenaEntry writes the defaults; the numbers this test cares about are set here,
	// because "the restore put back exactly what was there" is the assertion.
	if _, err := d.ExecContext(ctx, `
		UPDATE arena_entries SET r=1532.5, rd=210.25, sigma=0.0625, games=4
		WHERE arena_id=? AND entity_type='feature' AND entity_id=?`, a.ID, fID); err != nil {
		t.Fatalf("seed numbers: %v", err)
	}

	first, err := d.RemoveFeatureFromArena(ctx, pid, a.ID, fID, uid, "spam: repeated link drops")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if first.ID == 0 {
		t.Fatal("the act has no id")
	}
	if _, err := d.GetArenaEntry(ctx, a.ID, "feature", fID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the entry survived the removal: %v", err)
	}
	if got, _ := d.GetFeature(ctx, fID); got.Status != "rejected" {
		t.Fatalf("status is %q, want rejected", got.Status)
	}

	second, err := d.RestoreFeatureToArena(ctx, pid, uid, first.ID, "the removal was mistaken")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	e, err := d.GetArenaEntry(ctx, a.ID, "feature", fID)
	if err != nil {
		t.Fatalf("the entry did not come back: %v", err)
	}
	if e.R != 1532.5 || e.RD != 210.25 || e.Sigma != 0.0625 || e.Games != 4 {
		t.Fatalf("restored numbers drifted: %+v", e)
	}
	if got, _ := d.GetFeature(ctx, fID); got.Status != "draft" {
		t.Fatalf("status is %q, want the prior draft", got.Status)
	}

	// The reversal is itself a row that names what it reverses -- that is the whole design.
	var undoes *int64
	if err := d.QueryRowContext(ctx,
		`SELECT undoes FROM audit_log WHERE id=?`, second.ID).Scan(&undoes); err != nil {
		t.Fatalf("read reversal: %v", err)
	}
	if undoes == nil || *undoes != first.ID {
		t.Fatalf("act %d names %v as reversed, want %d", second.ID, undoes, first.ID)
	}

	// A second reversal of the same act must refuse: the log is append-only, and "undo twice"
	// would re-insert an entry the second act did not remove.
	if _, err := d.RestoreFeatureToArena(ctx, pid, uid, first.ID, "again"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("double reversal: got %v, want ErrInvalid", err)
	}
}

// The act needs a reason, and the refusal happens before the transaction opens: an act with no
// reason must not move the entry at all.
func TestRemovingWithoutAReasonLeavesTheArenaAlone(t *testing.T) {
	d, uid, pid, fID := arenaFixture(t)
	ctx := context.Background()
	a, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	if err := d.UpsertArenaEntry(ctx, a.ID, "feature", fID); err != nil {
		t.Fatalf("seed entry: %v", err)
	}

	if _, err := d.RemoveFeatureFromArena(ctx, pid, a.ID, fID, uid, "   "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank reason: got %v, want ErrInvalid", err)
	}
	if _, err := d.GetArenaEntry(ctx, a.ID, "feature", fID); err != nil {
		t.Fatalf("the entry moved despite the refusal: %v", err)
	}
	if got, _ := d.GetFeature(ctx, fID); got.Status == "rejected" {
		t.Fatal("the feature was rejected despite the refusal")
	}
}

// A feature from another project cannot be removed through this project's log, and the path
// that would let it is the id in the body -- so this is the enumeration check for the route.
func TestRemovingAFeatureFromAnotherProjectIsNotFound(t *testing.T) {
	d, uid, pid, fID := arenaFixture(t)
	ctx := context.Background()
	other, err := d.CreateProject(ctx, uid, "other-project", "Other", "x", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	a, err := d.EnsureArena(ctx, ArenaFeaturePriority, other.ID, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	if _, err := d.RemoveFeatureFromArena(ctx, pid, a.ID, fID, uid, "not mine"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project removal: got %v, want ErrNotFound", err)
	}
}
