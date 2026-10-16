package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/governance"
)

// §6.2's themes, end to end. The pillar arrived as two tables and nothing else: no route
// could create a theme, no code read the proposals, and ThemeWeightTotal -- the function that
// computes the spec's third steering input -- had no caller. So the first test here is the
// one that matters: a ratified theme has to MOVE A SCORE. A test that a row appeared would
// pass against a theme that steers nothing, which is exactly what shipped.

// themeFixture builds a project with one feature carrying a tag, and returns the ids.
//
// The tag is created through ensureTag, which is what the taxonomy's proposal route calls:
// the theme's own check is that the tag EXISTS (inventing one through a steering dial is how
// a tag list becomes a write-only surface).
func themeFixture(t *testing.T) (store *DB, pid, uid, featureID int64, tag string) {
	t.Helper()
	store, uid, pid = setupWithProject(t)
	ctx := context.Background()
	// A feature exists only as an answer to reported pain (§5), so the fixture reports
	// some: without a validated complaint the create is refused, and a fixture that
	// worked around that would be testing a feature the product cannot have.
	comp, err := store.CreateComplaint(ctx, pid, uid, "The login flow is brittle", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	f, err := store.CreateFeature(ctx, pid, uid, "Harden the login flow", "body", "", nil, nil, []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	tag = "security-hardening"
	if _, err := store.ensureTag(ctx, tag); err != nil {
		t.Fatalf("ensureTag: %v", err)
	}
	return store, pid, uid, f.ID, tag
}

// eligibleConsenter is a collaborator whose contribution makes them an eligible consenter
// (§8.2), and nothing else: the theme path must ratify on a real population, not on a role.
func eligibleConsenter(t *testing.T, store *DB, pid, featureID int64, name string) int64 {
	t.Helper()
	ctx := context.Background()
	voter := newUserNamed(t, store, name)
	if err := store.JoinProject(ctx, pid, voter); err != nil {
		t.Fatalf("JoinProject: %v", err)
	}
	mr, err := store.CreateMergeRequest(ctx, pid, featureID, voter, "the work that counts")
	if err != nil {
		t.Fatalf("CreateMergeRequest: %v", err)
	}
	if _, err := store.ExecContext(ctx,
		`UPDATE merge_requests SET status = 'merged', closed_at = ? WHERE id = ?`,
		float64(time.Now().Unix()), mr.ID); err != nil {
		t.Fatalf("merge: %v", err)
	}
	return voter
}

// priorityOf reads the score and the theme half the API reports beside it.
func priorityOf(t *testing.T, store *DB, pid, featureID int64) (total, theme float64) {
	t.Helper()
	charter := governance.DefaultCharter(governance.Collective)
	prios, err := store.GetFeaturePriorities(context.Background(), pid, charter)
	if err != nil {
		t.Fatalf("GetFeaturePriorities: %v", err)
	}
	for _, p := range prios {
		if p.ID == featureID {
			return p.PriorityScore, p.ThemeWeight
		}
	}
	t.Fatalf("feature %d is not in the priority list", featureID)
	return 0, 0
}

// TestARatifiedThemeMovesThePriorityScore is the point of the whole pillar.
func TestARatifiedThemeMovesThePriorityScore(t *testing.T) {
	store, pid, uid, featureID, tag := themeFixture(t)
	ctx := context.Background()
	charter := governance.DefaultCharter(governance.Collective)

	if err := store.AddFeatureTag(ctx, pid, featureID, uid, tag); err != nil {
		t.Fatalf("AddFeatureTag: %v", err)
	}
	before, themeBefore := priorityOf(t, store, pid, featureID)
	if themeBefore != 0 {
		t.Fatalf("theme_weight = %v before any theme, want 0", themeBefore)
	}

	p, err := store.ProposeStrategicTheme(ctx, pid, uid, tag, "topic", 2.0, "it is the safety floor")
	if err != nil {
		t.Fatalf("ProposeStrategicTheme: %v", err)
	}
	voter := eligibleConsenter(t, store, pid, featureID, "themeweightvoter")
	eligible, err := store.EligibleCollaboratorCount(ctx, pid)
	if err != nil {
		t.Fatalf("EligibleCollaboratorCount: %v", err)
	}
	if eligible < 1 {
		t.Fatalf("eligible collaborators = %d; the consenter is not eligible, so a "+
			"refusal here would not prove the quorum path", eligible)
	}

	got, result, err := store.ConsentStrategicTheme(ctx, p.ID, voter)
	if err != nil {
		t.Fatalf("ConsentStrategicTheme: %v", err)
	}
	if result != string(governance.ResultAccepted) || got.Status != "ratified" {
		t.Fatalf("result = %q, status = %q; want accepted and ratified", result, got.Status)
	}

	after, themeAfter := priorityOf(t, store, pid, featureID)
	// μ is the weight of the theme term, so the delta is exactly μ·2. Computed from the
	// charter rather than hardcoded: a test that pins 200 breaks when the default moves and
	// teaches nothing when it does not.
	want := charter.Mu * 2.0
	if themeAfter != 2.0 {
		t.Errorf("theme_weight = %v, want 2.0", themeAfter)
	}
	if diff := after - before; diff != want {
		t.Errorf("priority moved by %v, want %v (mu %v x theme 2.0)", diff, want, charter.Mu)
	}
	if after == before {
		t.Error("the score did not move: the theme is inert, which is the defect this " +
			"pass exists to fix")
	}
}

// TestAProposedThemeAppliesNothing -- the proposal is a decision, not a lever.
func TestAProposedThemeAppliesNothing(t *testing.T) {
	store, pid, uid, featureID, tag := themeFixture(t)
	ctx := context.Background()

	if err := store.AddFeatureTag(ctx, pid, featureID, uid, tag); err != nil {
		t.Fatalf("AddFeatureTag: %v", err)
	}
	before, _ := priorityOf(t, store, pid, featureID)
	if _, err := store.ProposeStrategicTheme(ctx, pid, uid, tag, "topic", 3.0, "why not"); err != nil {
		t.Fatalf("ProposeStrategicTheme: %v", err)
	}
	after, theme := priorityOf(t, store, pid, featureID)
	if after != before || theme != 0 {
		t.Errorf("priority moved on PROPOSAL: %v -> %v (theme %v). A theme that takes "+
			"effect on proposal is the hand-steering §6.2's consensus route exists to prevent",
			before, after, theme)
	}
	themes, err := store.ListStrategicThemes(ctx, pid)
	if err != nil {
		t.Fatalf("ListStrategicThemes: %v", err)
	}
	if len(themes) != 0 {
		t.Errorf("live themes = %d after a proposal, want 0", len(themes))
	}
}

// TestAnExpiredThemeContributesNothing -- §366's expiry, which had a column, a comment
// explaining the column, and no way to write or read it.
func TestAnExpiredThemeContributesNothing(t *testing.T) {
	store, pid, uid, featureID, tag := themeFixture(t)
	ctx := context.Background()

	if err := store.AddFeatureTag(ctx, pid, featureID, uid, tag); err != nil {
		t.Fatalf("AddFeatureTag: %v", err)
	}
	p, err := store.ProposeStrategicTheme(ctx, pid, uid, tag, "topic", 2.0, "safety floor")
	if err != nil {
		t.Fatalf("ProposeStrategicTheme: %v", err)
	}
	voter := eligibleConsenter(t, store, pid, featureID, "expiryvoter")
	if _, _, err := store.ConsentStrategicTheme(ctx, p.ID, voter); err != nil {
		t.Fatalf("ConsentStrategicTheme: %v", err)
	}
	if _, theme := priorityOf(t, store, pid, featureID); theme != 2.0 {
		t.Fatalf("theme_weight = %v while in force, want 2.0", theme)
	}

	// Backdate the expiry rather than waiting a quarter: the question is whether a read
	// honours the column, not whether a clock advances.
	if _, err := store.ExecContext(ctx,
		`UPDATE strategic_themes SET expires_at = ? WHERE project_id = ? AND tag = ?`,
		float64(time.Now().Unix())-1, pid, tag); err != nil {
		t.Fatalf("expire: %v", err)
	}

	if _, theme := priorityOf(t, store, pid, featureID); theme != 0 {
		t.Errorf("an expired theme still contributes %v to the priority", theme)
	}
	themes, err := store.ListStrategicThemes(ctx, pid)
	if err != nil {
		t.Fatalf("ListStrategicThemes: %v", err)
	}
	if len(themes) != 0 {
		t.Errorf("ListStrategicThemes still lists %d expired theme(s): a lapsed weight "+
			"listed as current misrepresents the roadmap", len(themes))
	}
}

// TestRenewingAThemeMovesItsExpiryWithoutDoublingIt -- the upsert, and the reason the
// identity index exists. Insert-on-renew would leave two live rows for one tag and
// ThemeWeightTotal would sum both, so renewing a +2 theme would make it +4.
func TestRenewingAThemeMovesItsExpiryWithoutDoublingIt(t *testing.T) {
	store, pid, uid, featureID, tag := themeFixture(t)
	ctx := context.Background()

	if err := store.AddFeatureTag(ctx, pid, featureID, uid, tag); err != nil {
		t.Fatalf("AddFeatureTag: %v", err)
	}
	first, err := store.ProposeStrategicTheme(ctx, pid, uid, tag, "topic", 2.0, "first")
	if err != nil {
		t.Fatalf("ProposeStrategicTheme: %v", err)
	}
	voter := eligibleConsenter(t, store, pid, featureID, "renewvoter")
	if _, _, err := store.ConsentStrategicTheme(ctx, first.ID, voter); err != nil {
		t.Fatalf("ConsentStrategicTheme: %v", err)
	}
	var firstExpiry float64
	if err := store.QueryRowContext(ctx,
		`SELECT expires_at FROM strategic_themes WHERE project_id = ? AND tag = ?`,
		pid, tag).Scan(&firstExpiry); err != nil {
		t.Fatalf("read expiry: %v", err)
	}

	// A week later, the same tag is proposed again at a higher weight.
	later := firstExpiry
	if _, err := store.ExecContext(ctx,
		`UPDATE strategic_themes SET expires_at = ? WHERE project_id = ? AND tag = ?`,
		later-7*86400+3600, pid, tag); err != nil {
		t.Fatalf("age the theme: %v", err)
	}
	second, err := store.ProposeStrategicTheme(ctx, pid, uid, tag, "topic", 3.0, "still the floor")
	if err != nil {
		t.Fatalf("re-propose: %v", err)
	}
	if second.WeightBefore == nil || *second.WeightBefore != 2.0 {
		t.Errorf("weight_before = %v, want the live 2.0 -- a renewal has to know what it "+
			"replaces", second.WeightBefore)
	}
	if _, _, err := store.ConsentStrategicTheme(ctx, second.ID, voter); err != nil {
		t.Fatalf("consent to the renewal: %v", err)
	}

	themes, err := store.ListStrategicThemes(ctx, pid)
	if err != nil {
		t.Fatalf("ListStrategicThemes: %v", err)
	}
	if len(themes) != 1 {
		t.Fatalf("live themes = %d after a renewal, want 1 (a second row would be counted "+
			"twice by ThemeWeightTotal)", len(themes))
	}
	if themes[0].Weight != 3.0 {
		t.Errorf("weight = %v after renewal, want 3.0", themes[0].Weight)
	}
	if themes[0].ExpiresAt <= firstExpiry-7*86400+3600 {
		t.Errorf("expiry did not move forward: renewal left %v", themes[0].ExpiresAt)
	}
	if _, theme := priorityOf(t, store, pid, featureID); theme != 3.0 {
		t.Errorf("theme_weight = %v after renewal, want 3.0 (not 5.0, which is what a "+
			"duplicate row would produce)", theme)
	}
}

// TestTheThemeProposerCannotConsentToTheirOwnProposal -- §5.1, and the reason 0012's
// table needed a column it did not have.
func TestTheThemeProposerCannotConsentToTheirOwnProposal(t *testing.T) {
	store, pid, uid, featureID, tag := themeFixture(t)
	ctx := context.Background()
	_ = uid

	// The proposer must be an ELIGIBLE collaborator, and this is not decoration: the
	// project's creator is not eligible (§8.2 wants a qualifying contribution), so a test
	// that had the owner propose would be refused by the eligibility check with or without
	// the self-consent guard. Written that way first, and the mutation run caught it: the
	// mutant that deletes the guard SURVIVED, because the other guard answered first and
	// the test only asserted ErrPerm. Now the guard under test is the only thing that can
	// refuse.
	proposer := eligibleConsenter(t, store, pid, featureID, "selfconsentproposer")
	p, err := store.ProposeStrategicTheme(ctx, pid, proposer, tag, "topic", 2.0, "mine")
	if err != nil {
		t.Fatalf("ProposeStrategicTheme: %v", err)
	}
	if _, _, err := store.ConsentStrategicTheme(ctx, p.ID, proposer); !errors.Is(err, ErrPerm) {
		t.Errorf("the proposer's own consent: err = %v, want ErrPerm", err)
	}
	if err := errorMentions(err, "own proposal"); err != nil {
		t.Error(err)
	}
	// And nothing was applied: with quorum capped to the eligible population, a proposer
	// who could consent would ratify alone, which is the dial §6.2 replaced.
	if _, theme := priorityOf(t, store, pid, featureID); theme != 0 {
		t.Errorf("theme_weight = %v after a refused self-consent, want 0", theme)
	}
}

func errorMentions(err error, want string) error {
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), want) {
		return fmt.Errorf("refusal %q does not mention %q", err.Error(), want)
	}
	return nil
}

// TestANonEligibleCollaboratorCannotConsentThemes -- with an eligible count of 0 the quorum
// is 0, so a single consent would ratify. The eligibility guard is what stops the same
// one-person ratification arriving through a different door.
func TestANonEligibleCollaboratorCannotConsentThemes(t *testing.T) {
	store, pid, uid, _, tag := themeFixture(t)
	ctx := context.Background()
	p, err := store.ProposeStrategicTheme(ctx, pid, uid, tag, "topic", 2.0, "mine")
	if err != nil {
		t.Fatalf("ProposeStrategicTheme: %v", err)
	}
	arrived := newUserNamed(t, store, "themenobody")
	if err := store.JoinProject(ctx, pid, arrived); err != nil {
		t.Fatalf("JoinProject: %v", err)
	}
	if _, _, err := store.ConsentStrategicTheme(ctx, p.ID, arrived); !errors.Is(err, ErrPerm) {
		t.Errorf("a collaborator with no qualifying contribution: err = %v, want ErrPerm", err)
	}
}

// TestAThemeWeightOutsideTheRangeIsRefused, named by its bounds rather than "invalid".
func TestAThemeWeightOutsideTheRangeIsRefused(t *testing.T) {
	store, pid, uid, _, tag := themeFixture(t)
	ctx := context.Background()
	for _, w := range []float64{ThemeWeightMax + 0.5, ThemeWeightMin - 0.5} {
		_, err := store.ProposeStrategicTheme(ctx, pid, uid, tag, "topic", w, "out of range")
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("weight %v: err = %v, want ErrInvalid", w, err)
			continue
		}
		if !strings.Contains(err.Error(), "-2") || !strings.Contains(err.Error(), "3") {
			t.Errorf("weight %v: message %q does not name the range", w, err.Error())
		}
	}
}

// TestASecondPendingThemeProposalIsRefused -- two competing changes to one dial would make
// whichever was consented to first a coin toss.
func TestASecondPendingThemeProposalIsRefused(t *testing.T) {
	store, pid, uid, _, tag := themeFixture(t)
	ctx := context.Background()
	if _, err := store.ProposeStrategicTheme(ctx, pid, uid, tag, "topic", 2.0, "first"); err != nil {
		t.Fatalf("ProposeStrategicTheme: %v", err)
	}
	if _, err := store.ProposeStrategicTheme(ctx, pid, uid, tag, "topic", 1.0, "second"); !errors.Is(err, ErrDuplicate) {
		t.Errorf("a second pending proposal for one tag: err = %v, want ErrDuplicate", err)
	}
	// A different NAMESPACE is a different dial, so it is allowed: the index is on
	// (project, tag, namespace) and the check must not be broader than it.
	if _, err := store.ProposeStrategicTheme(ctx, pid, uid, tag, "tech", 1.0, "other dial"); err != nil {
		t.Errorf("a proposal in another namespace: err = %v, want none", err)
	}
}

// TestAThemeForATagThatDoesNotExistIsRefused -- §4.3's rule, applied to the steering dial.
func TestAThemeForATagThatDoesNotExistIsRefused(t *testing.T) {
	store, pid, uid, _, _ := themeFixture(t)
	ctx := context.Background()
	_, err := store.ProposeStrategicTheme(ctx, pid, uid, "no-such-tag-anywhere", "topic", 1.0, "why")
	if err == nil {
		t.Fatal("a theme for an unknown tag: got nil, want a refusal naming the route that " +
			"proposes tags")
	}
	if !strings.Contains(err.Error(), "taxonomy/proposals") {
		t.Errorf("the refusal does not say how to fix it: %q", err.Error())
	}
}

// TestWithdrawingAThemeProposalChangesNothing.
func TestWithdrawingAThemeProposalChangesNothing(t *testing.T) {
	store, pid, uid, featureID, tag := themeFixture(t)
	ctx := context.Background()
	if err := store.AddFeatureTag(ctx, pid, featureID, uid, tag); err != nil {
		t.Fatalf("AddFeatureTag: %v", err)
	}
	p, err := store.ProposeStrategicTheme(ctx, pid, uid, tag, "topic", 2.0, "why")
	if err != nil {
		t.Fatalf("ProposeStrategicTheme: %v", err)
	}
	if err := store.WithdrawStrategicThemeProposal(ctx, p.ID, uid, false); err != nil {
		t.Fatalf("WithdrawStrategicThemeProposal: %v", err)
	}
	got, err := store.GetStrategicThemeProposal(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetStrategicThemeProposal: %v", err)
	}
	if got.Status != "withdrawn" {
		t.Errorf("status = %q, want withdrawn", got.Status)
	}
	if _, theme := priorityOf(t, store, pid, featureID); theme != 0 {
		t.Errorf("theme_weight = %v after a withdrawal, want 0", theme)
	}
	// A withdrawn proposal is decided, so it can no longer be consented to.
	voter := eligibleConsenter(t, store, pid, featureID, "withdrawvoter")
	if _, _, err := store.ConsentStrategicTheme(ctx, p.ID, voter); err == nil {
		t.Error("consenting to a withdrawn proposal: got nil, want a refusal")
	}
}

// TestTheThemeLifetimeComesFromTheCharterAndIsBounded.
//
// §6.2's themes expire at "the next release cycle", and the charter is where a project says how
// long that is. Two things need proving and neither is the row existing: that the value is READ
// back (a field that round-trips nothing is the dropped-field class), and that a lifetime which
// would make every theme born-expired is refused BY NAME rather than silently accepted.
func TestTheThemeLifetimeComesFromTheCharterAndIsBounded(t *testing.T) {
	store, pid, _, _, _ := themeFixture(t)
	ctx := context.Background()

	charter, err := store.GetCharterForProject(ctx, pid)
	if err != nil {
		t.Fatalf("GetCharterForProject: %v", err)
	}
	if charter.ThemeLifetimeDays != 90 {
		t.Errorf("default theme lifetime = %v, want 90", charter.ThemeLifetimeDays)
	}
	charter.ThemeLifetimeDays = 30
	if err := store.UpdateCharter(ctx, pid, charter); err != nil {
		t.Fatalf("UpdateCharter: %v", err)
	}
	again, err := store.GetCharterForProject(ctx, pid)
	if err != nil {
		t.Fatalf("GetCharterForProject: %v", err)
	}
	if again.ThemeLifetimeDays != 30 {
		t.Errorf("theme lifetime = %v after saving 30; the field does not round-trip",
			again.ThemeLifetimeDays)
	}

	// A zero lifetime would write themes that are ratified, listed by nothing and worth 0 --
	// so it is refused where the other §6.5 bounds are refused, by name.
	for _, bad := range []float64{0, -1, 99999} {
		broken := again
		broken.ThemeLifetimeDays = bad
		err := store.UpdateCharter(ctx, pid, broken)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("theme lifetime %v: err = %v, want ErrInvalid", bad, err)
			continue
		}
		if !strings.Contains(err.Error(), "theme_lifetime_days") {
			t.Errorf("theme lifetime %v: message %q does not name the field", bad, err.Error())
		}
	}
	// And the stored value is unchanged by the refusals.
	final, _ := store.GetCharterForProject(ctx, pid)
	if final.ThemeLifetimeDays != 30 {
		t.Errorf("theme lifetime = %v after refused updates, want 30", final.ThemeLifetimeDays)
	}
}
