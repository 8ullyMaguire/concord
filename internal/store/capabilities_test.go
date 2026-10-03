package store

import (
	"context"
	"database/sql"
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

// A mixed-case VALUE is accepted and stored normalized.
//
// The capability KEY is normalized in two places (NormalizeCapabilityKey on
// read, EnsureCapability on write) and every existing test exercises that: the
// fixture registers "WIP-Limits" and asserts on "wip-limits". The VALUE is a
// separate normalization at AssertCapability, and nothing asserted a mixed-case
// value, so removing it changed no observable — the mutant survived a fully
// green suite.
//
// The rule is not cosmetic. CapYes is "yes", and a contributor typing "Yes" or
// "YES" from a form must not be told their answer is invalid, and must not
// create a capability value that no query will ever match. Storing it raw would
// make "yes" and "Yes" two different values in the same column: both would pass
// the allowed-value check against the same declared set, and then Finder's
// question options would show two spellings of one answer.
func TestAMixedCaseValueIsStoredNormalized(t *testing.T) {
	store, uid, slug := capFixture(t)
	ctx := context.Background()

	a, err := store.AssertCapability(ctx, "wip-limits", slug, "  YES  ", "typed by hand", uid)
	if err != nil {
		t.Fatalf("AssertCapability with a mixed-case value: %v", err)
	}
	if a.Value != CapYes {
		t.Errorf("stored value = %q, want %q", a.Value, CapYes)
	}

	// And it is reachable by the normalized spelling, which is the point: one
	// stored value, one way to ask for it.
	got, err := store.ListCapabilitiesForSet(ctx, []int64{a.ProjectID}, "")
	if err != nil {
		t.Fatalf("ListCapabilitiesForSet: %v", err)
	}
	if v := got[a.ProjectID]["wip-limits"]; v != CapYes {
		t.Errorf("read back %q, want %q — the value must be normalized on write, "+
			"not only on the way in", v, CapYes)
	}

	// Two spellings of one answer must collapse onto one assertion, not
	// accumulate: "yes" and "Yes" are one claim, and two rows would make the
	// quorum count votes on a split claim.
	again, err := store.AssertCapability(ctx, "wip-limits", slug, "Yes", "typed again", uid)
	if err != nil {
		t.Fatalf("AssertCapability 'Yes': %v", err)
	}
	if again.ID != a.ID {
		t.Errorf("second spelling created assertion %d, want the existing %d — "+
			"normalization must make it an update", again.ID, a.ID)
	}
	var rows int
	if err := d0(store).QueryRowContext(ctx,
		`SELECT COUNT(*) FROM capability_assertions WHERE capability = ? AND project_id = ?`,
		"wip-limits", a.ProjectID).Scan(&rows); err != nil {
		t.Fatalf("count assertions: %v", err)
	}
	if rows != 1 {
		t.Errorf("%d assertion rows for one capability/project, want 1", rows)
	}
}

// ONE dispute does not dispute a claim. TWO does.
//
// The existing dispute test only ever creates two disputes, so it cannot
// distinguish CapDisputeQuorum=2 from CapDisputeQuorum=1: both pass. This pins
// the boundary from below, which is the half that decides whether one person
// objecting can relabel a community consensus as "disputed".
//
// Asymmetry is the point (see CapDisputeQuorum): disputing is meant to be
// expensive, so the test that matters is that the SECOND voice is required.
func TestASingleDisputeDoesNotDisputeAClaim(t *testing.T) {
	store, uid, slug := capFixture(t)
	ctx := context.Background()

	a, err := store.AssertCapability(ctx, "wip-limits", slug, CapYes, "docs", uid)
	if err != nil {
		t.Fatalf("AssertCapability: %v", err)
	}

	other := newUserNamed(t, store, "disputer1")

	after, err := store.ConfirmCapability(ctx, a.ID, other, false)
	if err != nil {
		t.Fatalf("ConfirmCapability (dispute): %v", err)
	}
	if after.State == CapStateDisputed {
		t.Errorf("state = %q after ONE dispute; a single objector must not be able to "+
			"relabel a claim as disputed (CapDisputeQuorum is %d)", after.State, CapDisputeQuorum)
	}
	// The single objection is still visible even though it does not win.
	confirmed, disputes, _ := selfVoteTally(t, store, a.ID)
	if confirmed != 0 || disputes != 1 {
		t.Errorf("tally = %d confirmed / %d disputed, want 0 / 1 — the single objection "+
			"must be recorded even though it does not win", confirmed, disputes)
	}
	if after.Value != CapYes {
		t.Errorf("value = %q, want %q — a disputed-but-not-won claim keeps its value",
			after.Value, CapYes)
	}
}

// An author's own confirmations never count toward quorum.
//
// assertionColumns excludes the author from BOTH subqueries. Without that
// exclusion an author could confirm their own assertion, reach quorum alone,
// and promote it to confirmed — the exact thing quorum exists to prevent. The
// existing author test covers the explicit ErrPerm guard in ConfirmCapability;
// this covers the counting, which is a different code path and was unguarded.
func TestAnAuthorsOwnConfirmationDoesNotCountTowardQuorum(t *testing.T) {
	store, uid, slug := capFixture(t)
	ctx := context.Background()

	a, err := store.AssertCapability(ctx, "wip-limits", slug, CapYes, "docs", uid)
	if err != nil {
		t.Fatalf("AssertCapability: %v", err)
	}

	// Inserted directly rather than through ConfirmCapability, which refuses
	// this outright. The point is to bypass the GUARD and test the COUNTING:
	// the row exists, so if the subquery ever stops excluding the author, the
	// count moves. A test that used the API would keep passing even if the
	// exclusion were deleted, because the guard would hide it.
	//
	// Column is `at`, and UNIQUE(assertion_id, user_id) means one row per
	// voter -- so a single self-vote, which is the only shape the schema allows.
	if _, err := d0(store).ExecContext(ctx, `
		INSERT INTO capability_confirmations (assertion_id, user_id, confirmed, at)
		VALUES (?, ?, 1, ?)`, a.ID, uid, 1.0); err != nil {
		t.Fatalf("seed self-confirmation: %v", err)
	}

	// Read the count the LIST query computes, since that is where the exclusion
	// lives. getAssertion would not exercise assertionColumns at all.
	selfConfirmed, disputes, state := selfVoteTally(t, store, a.ID)
	if selfConfirmed != 0 {
		t.Errorf("Confirmations = %d, want 0 — the author's own votes must be excluded "+
			"from the count (or one person promotes their own claim)", selfConfirmed)
	}
	if disputes != 0 {
		t.Errorf("Disputes = %d from self-votes, want 0", disputes)
	}
	if state == CapStateConfirmed {
		t.Error("the author promoted their own assertion to confirmed with their own votes")
	}

	// One independent confirmation DOES count. If this were 0 as well, the
	// exclusion would be excluding everything.
	other := newUserNamed(t, store, "independent1")
	if _, err := store.ConfirmCapability(ctx, a.ID, other, true); err != nil {
		t.Fatalf("ConfirmCapability: %v", err)
	}
	independent, disputes, state := selfVoteTally(t, store, a.ID)
	if independent != 1 {
		t.Errorf("Confirmations = %d after one independent vote, want 1 — if this is 0 "+
			"the exclusion is excluding everyone, not just the author", independent)
	}
	if disputes != 0 {
		t.Errorf("Disputes = %d, want 0", disputes)
	}
	if state == CapStateConfirmed {
		t.Errorf("state = %q after ONE independent vote, want %q (quorum is %d)",
			state, CapStateAsserted, CapQuorum)
	}
}

// d0 exposes the wrapped *sql.DB for the two assertions in this file that need
// to see rows the public API deliberately does not surface: confirmation counts
// as the list query computes them, and the raw row count behind an upsert.
func d0(s *DB) *sql.DB { return s.DB }

// selfVoteTally reads the confirm/dispute counts and derived state for one
// assertion, through the same scanAssertion/getAssertion path the API uses, so
// the exclusion under test is the production one and not a copy of it that can
// drift. getAssertion selects assertionColumns, whose subqueries carry the
// `AND c.user_id <> a.asserted_by` clause this test exists to pin.
func selfVoteTally(t *testing.T, s *DB, assertionID int64) (confirmed, disputes int, state string) {
	t.Helper()
	a, err := s.getAssertion(context.Background(), assertionID)
	if err != nil {
		t.Fatalf("getAssertion %d: %v", assertionID, err)
	}
	return a.Confirms, a.Disputes, a.State
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
