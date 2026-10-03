package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The capability matrix's AC tests (docs/plans/finder.md step 2).
//
// Every test here names a rule from docs/specs/finder-spec.md §3.3, and each
// must fail when its rule is broken. The one to watch is the dispute test: a
// plausible implementation substitutes 'no' for a disputed value, and every
// other test in this file would still pass.

func capFixture(t *testing.T) (*DB, int64, string) {
	t.Helper()
	store, uid, _ := setupWithProject(t)
	ctx := context.Background()

	if err := store.EnsureCapability(ctx, Capability{
		Key:      "WIP-Limits",
		Label:    "WIP limits",
		Category: "project-management",
		Kind:     CapBoolean,
	}); err != nil {
		t.Fatalf("EnsureCapability: %v", err)
	}
	return store, uid, "test-project"
}

func TestTwoIndependentConfirmationsPromoteAnAssertion(t *testing.T) {
	store, uid, slug := capFixture(t)
	ctx := context.Background()

	a, err := store.AssertCapability(ctx, "wip-limits", slug, CapYes, "docs", uid)
	if err != nil {
		t.Fatalf("AssertCapability: %v", err)
	}
	if a.State != CapStateAsserted {
		t.Errorf("fresh assertion state = %q, want %q", a.State, CapStateAsserted)
	}

	// One confirmation from a stranger is not yet a quorum.
	a, err = store.ConfirmCapability(ctx, a.ID, secondVoter(t, store), true)
	if err != nil {
		t.Fatalf("ConfirmCapability: %v", err)
	}
	if a.State != CapStateAsserted {
		t.Errorf("after 1 confirm state = %q, want %q", a.State, CapStateAsserted)
	}

	a, err = store.ConfirmCapability(ctx, a.ID, newUserNamed(t, store, "thirdvoter"), true)
	if err != nil {
		t.Fatalf("ConfirmCapability: %v", err)
	}
	if a.State != CapStateConfirmed {
		t.Errorf("after 2 confirms state = %q, want %q", a.State, CapStateConfirmed)
	}
	if a.Confirms != 2 {
		t.Errorf("Confirms = %d, want 2", a.Confirms)
	}
}

func TestTheAuthorCannotConfirmTheirOwnAssertion(t *testing.T) {
	store, uid, slug := capFixture(t)
	ctx := context.Background()

	a, err := store.AssertCapability(ctx, "wip-limits", slug, CapYes, "", uid)
	if err != nil {
		t.Fatalf("AssertCapability: %v", err)
	}
	if _, err := store.ConfirmCapability(ctx, a.ID, uid, true); !errors.Is(err, ErrPerm) {
		t.Fatalf("author confirming own assertion = %v, want ErrPerm", err)
	}

	// And the refusal is not a silent no-op: nothing was written.
	got, err := store.getAssertion(ctx, a.ID)
	if err != nil {
		t.Fatalf("getAssertion: %v", err)
	}
	if got.Confirms != 0 {
		t.Errorf("author confirmation was counted: Confirms = %d, want 0", got.Confirms)
	}
}

func TestTwoDisputesMarkAClaimDisputedAndNeverNo(t *testing.T) {
	store, uid, slug := capFixture(t)
	ctx := context.Background()

	a, err := store.AssertCapability(ctx, "wip-limits", slug, CapYes, "docs", uid)
	if err != nil {
		t.Fatalf("AssertCapability: %v", err)
	}
	// One confirmation and two disputes: the contested case, because the
	// plausible bug reads "has a quorum, so confirmed".
	if _, err := store.ConfirmCapability(ctx, a.ID, secondVoter(t, store), true); err != nil {
		t.Fatalf("ConfirmCapability: %v", err)
	}
	if _, err := store.ConfirmCapability(ctx, a.ID, newUserNamed(t, store, "disputer1"), false); err != nil {
		t.Fatalf("dispute 1: %v", err)
	}
	a, err = store.ConfirmCapability(ctx, a.ID, newUserNamed(t, store, "disputer2"), false)
	if err != nil {
		t.Fatalf("dispute 2: %v", err)
	}

	if a.State != CapStateDisputed {
		t.Errorf("state = %q, want %q", a.State, CapStateDisputed)
	}
	// The load-bearing assertion: a dispute is not a negative finding.
	if a.Value == CapNo {
		t.Error("a disputed claim was rewritten to 'no'; dispute and falsehood are different claims")
	}
	if a.Value != CapYes {
		t.Errorf("dispute changed the recorded value to %q, want the asserted %q", a.Value, CapYes)
	}
}

func TestAValueOutsideTheDeclaredValuesArrayIsRejected(t *testing.T) {
	store, uid, slug := capFixture(t)
	ctx := context.Background()

	// "maybe" is plausible English and is not one of the four states.
	_, err := store.AssertCapability(ctx, "wip-limits", slug, "maybe", "", uid)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("asserting 'maybe' = %v, want ErrInvalid", err)
	}
	// And the message must name the capability, so a caller can fix it.
	if err != nil && !strings.Contains(err.Error(), "wip-limits") {
		t.Errorf("error %q does not name the capability", err)
	}
}

func TestAssertingTheSameCapabilityTwiceUpdatesRatherThanDuplicates(t *testing.T) {
	store, uid, slug := capFixture(t)
	ctx := context.Background()

	if _, err := store.AssertCapability(ctx, "wip-limits", slug, CapNo, "", uid); err != nil {
		t.Fatalf("first assert: %v", err)
	}
	if _, err := store.AssertCapability(ctx, "wip-limits", slug, CapYes, "later evidence", uid); err != nil {
		t.Fatalf("re-assert: %v", err)
	}

	list, err := store.ListCapabilityAssertions(ctx, mustProjectID(t, store, slug))
	if err != nil {
		t.Fatalf("ListCapabilityAssertions: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d assertions, want 1 — the matrix holds one current value per (capability, project)", len(list))
	}
	if list[0].Value != CapYes {
		t.Errorf("value = %q, want %q", list[0].Value, CapYes)
	}
}

func TestCapabilityAssertionsAreScopedToTheProject(t *testing.T) {
	store, uid := setupWithUser(t)
	ctx := context.Background()
	if err := store.EnsureCapability(ctx, Capability{
		Key: "wip-limits", Label: "WIP limits", Category: "project-management", Kind: CapBoolean,
	}); err != nil {
		t.Fatalf("EnsureCapability: %v", err)
	}
	a, err := store.CreateProject(ctx, uid, "alpha", "Alpha", "", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject alpha: %v", err)
	}
	b, err := store.CreateProject(ctx, uid, "beta", "Beta", "", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject beta: %v", err)
	}
	if _, err := store.AssertCapability(ctx, "wip-limits", a.Slug, CapYes, "", uid); err != nil {
		t.Fatalf("AssertCapability: %v", err)
	}

	got, err := store.ListCapabilityAssertions(ctx, b.ID)
	if err != nil {
		t.Fatalf("ListCapabilityAssertions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("project beta sees %d of project alpha's assertions, want 0", len(got))
	}
}

// Unknown is a value, not an absence: this is the distinction the whole Finder
// contribution loop rests on, so it is pinned here rather than only in the
// engine's tests.
func TestAnUnknownAssertionIsDistinguishableFromAMissingOne(t *testing.T) {
	store, uid := setupWithUser(t)
	ctx := context.Background()
	for _, key := range []string{"offline", "wip-limits"} {
		if err := store.EnsureCapability(ctx, Capability{
			Key: key, Label: key, Category: "general", Kind: CapBoolean,
		}); err != nil {
			t.Fatalf("EnsureCapability(%s): %v", key, err)
		}
	}
	p, err := store.CreateProject(ctx, uid, "gamma", "Gamma", "", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := store.AssertCapability(ctx, "offline", p.Slug, CapUnknown, "", uid); err != nil {
		t.Fatalf("AssertCapability: %v", err)
	}

	set, err := store.ListCapabilitiesForSet(ctx, []int64{p.ID}, "")
	if err != nil {
		t.Fatalf("ListCapabilitiesForSet: %v", err)
	}
	got := set[p.ID]
	if got["offline"] != CapUnknown {
		t.Errorf("offline = %q, want %q", got["offline"], CapUnknown)
	}
	if _, present := got["wip-limits"]; present {
		t.Error("a capability nobody asserted appeared in the map; absence and 'unknown' must stay distinct")
	}
}

func TestEnsureCapabilityRefusesToChangeTheValueSetUnderLiveAssertions(t *testing.T) {
	store, uid, slug := capFixture(t)
	ctx := context.Background()
	if _, err := store.AssertCapability(ctx, "wip-limits", slug, CapYes, "", uid); err != nil {
		t.Fatalf("AssertCapability: %v", err)
	}

	// Same key, different kind: silently switching it would invalidate every
	// recorded assertion against the old value set.
	err := store.EnsureCapability(ctx, Capability{
		Key: "wip-limits", Label: "WIP limits", Category: "project-management", Kind: CapEnum,
		Values: []string{"yes", "no"},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("changing an existing capability's kind = %v, want ErrConflict", err)
	}
}

func TestEnsureCapabilityIsIdempotentForTheSameShape(t *testing.T) {
	store, _, _ := capFixture(t)
	ctx := context.Background()
	// Re-registering with a new label is ordinary bookkeeping.
	if err := store.EnsureCapability(ctx, Capability{
		Key: "wip-limits", Label: "Work-in-progress limits",
		Category: "project-management", Kind: CapBoolean,
	}); err != nil {
		t.Fatalf("re-registering the same shape: %v", err)
	}
	c, err := store.GetCapability(ctx, "WIP-Limits")
	if err != nil {
		t.Fatalf("GetCapability: %v", err)
	}
	if c.Label != "Work-in-progress limits" {
		t.Errorf("label = %q, want the updated one", c.Label)
	}
	if c.Key != "wip-limits" {
		t.Errorf("key = %q, want the normalised %q", c.Key, "wip-limits")
	}
}

func TestAnUnknownCapabilityIsNotFound(t *testing.T) {
	store, _, _ := capFixture(t)
	if _, err := store.GetCapability(context.Background(), "no-such-capability"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetCapability on an unknown key = %v, want ErrNotFound", err)
	}
}

// A hand-edited database can orphan an assertion by deleting the project row
// directly, which is exactly the two-schema-rules case a Go-level test never
// reaches: nothing in the application deletes a project this way. So the cascade
// is forced through raw SQL, and the assertion is that the DATABASE refuses to
// leave the row behind.
func TestDeletingAProjectRowCascadesToItsAssertions(t *testing.T) {
	store, uid, slug := capFixture(t)
	ctx := context.Background()
	if _, err := store.AssertCapability(ctx, "wip-limits", slug, CapYes, "", uid); err != nil {
		t.Fatalf("AssertCapability: %v", err)
	}
	pid := mustProjectID(t, store, slug)

	if _, err := store.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, pid); err != nil {
		t.Fatalf("raw delete: %v", err)
	}

	var n int
	if err := store.QueryRowContext(ctx, `SELECT COUNT(*) FROM capability_assertions`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("%d assertions survived their project; the CASCADE is declared but not effective", n)
	}
}