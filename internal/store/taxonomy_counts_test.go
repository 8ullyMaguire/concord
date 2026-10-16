package store

import (
	"context"
	"errors"
	"testing"
)

// TestTheCountsThePageShowsAreTheCountsTheServerUses.
//
// The taxonomy page has to answer "how many more consents does this need", and the
// list endpoint did not carry the number -- it appeared only in the 202 message after
// consenting, so it could be learned only by acting. ListTaxonomyProposals now
// computes consent_count and eligible_count per row, and this holds those two
// numbers against the ones RatifyTaxonomyProposal actually divides.
//
// Both halves matter, and the denominator more than the numerator:
//
//   - a consent count that misses rows makes the page understate progress;
//   - an ELIGIBLE count that disagrees with eligibleTaggers shows a quorum the
//     server will not enforce. A displayed denominator that is not the enforced one
//     is worse than no denominator, because it reads as a measurement.
//
// The two spellings are a correlated subquery in the list and a standalone COUNT in
// eligibleTaggers, which is exactly the shape that drifts.
func TestTheCountsThePageShowsAreTheCountsTheServerUses(t *testing.T) {
	ctx := context.Background()
	st, owner, pid := setupWithProject(t)
	contributor := newUserNamed(t, st, "count-contributor")
	guest := newUserNamed(t, st, "count-guest")

	// A contributor in the project, and a user with no role anywhere: the eligible
	// count must include the first and exclude the second, or the denominator is
	// just "how many accounts exist".
	if _, err := st.ExecContext(ctx,
		`INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?, 'contributor', ?)`,
		pid, contributor, float64(1790000000)); err != nil {
		t.Fatalf("add contributor: %v", err)
	}
	if _, err := st.ExecContext(ctx,
		`INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?, 'guest', ?)`,
		pid, guest, float64(1790000000)); err != nil {
		t.Fatalf("add guest: %v", err)
	}

	instanceWide, err := st.ProposeTaxonomyChange(ctx, 0, owner, TaxonomyCreate, "topic:counts-instance", "", "why")
	if err != nil {
		t.Fatalf("ProposeTaxonomyChange(instance): %v", err)
	}
	projectScoped, err := st.ProposeTaxonomyChange(ctx, pid, owner, TaxonomyCreate, "topic:counts-project", "", "why")
	if err != nil {
		t.Fatalf("ProposeTaxonomyChange(project): %v", err)
	}

	for _, tc := range []struct {
		name     string
		proposal TaxonomyProposal
		scope    int64
	}{
		{"instance-wide", instanceWide, 0},
		{"project-scoped", projectScoped, pid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := st.eligibleTaggers(ctx, tc.scope)
			if err != nil {
				t.Fatalf("eligibleTaggers: %v", err)
			}
			if want == 0 {
				t.Fatal("eligibleTaggers returned 0; this test would pass against a " +
					"count of nothing, which is the failure mode it exists to catch")
			}
			v := viewFor(t, ctx, st, tc.scope, tc.proposal.ID)
			if v.EligibleCount != want {
				t.Errorf("eligible_count = %d in the list, but the server divides by %d "+
					"(eligibleTaggers): the page would show a quorum that is not the "+
					"one enforced", v.EligibleCount, want)
			}
			if v.ConsentCount != 0 {
				t.Errorf("a proposal nobody has consented to reports %d consents", v.ConsentCount)
			}
		})
	}

	// The numerator, and the fact that a consent is recorded even when it does not
	// ratify. That is the state most consents are in -- below quorum is the ordinary
	// case, not an error -- and it is exactly the state the page exists to display.
	_, err = st.RatifyTaxonomyProposal(ctx, instanceWide.ID, contributor)
	if err != nil && !errors.Is(err, ErrConflict) {
		t.Fatalf("RatifyTaxonomyProposal: %v", err)
	}
	v := viewFor(t, ctx, st, 0, instanceWide.ID)
	if v.ConsentCount != 1 {
		t.Errorf("consent_count = %d after one consent, want 1", v.ConsentCount)
	}
	if v.Status != "pending" {
		// Not an error if it DID ratify -- but then the count assertion above is
		// about a decided proposal and the test means something different, so say so
		// rather than leaving it implicit.
		t.Logf("note: the proposal reached %q, so this ran against a decided proposal", v.Status)
	}

	// The project-scoped one must not have moved: consenting to an instance-wide
	// proposal must not count towards a project's.
	other := viewFor(t, ctx, st, pid, projectScoped.ID)
	if other.ConsentCount != 0 {
		t.Errorf("consenting to an instance-wide proposal gave a project-scoped one "+
			"%d consents", other.ConsentCount)
	}
}

// viewFor reads one proposal out of the list the API serves.
//
// Reading it through ListTaxonomyProposals rather than GetTaxonomyProposal is the
// point: the counts exist only on the list path, and a helper that reached for the
// single-row read would assert against a struct the page never sees.
//
// The scope argument is not decoration: with no project the list answers
// instance-wide proposals ONLY (`project_id IS NULL OR project_id = ?` with a nil
// placeholder, so the second arm is never true). The first version of this helper
// listed with 0 and reported "proposal 2 is not in the list the API serves", which
// reads like a broken list and was the helper asking the wrong question.
func viewFor(t *testing.T, ctx context.Context, st *DB, scope, id int64) TaxonomyProposalView {
	t.Helper()
	views, err := st.ListTaxonomyProposals(ctx, scope)
	if err != nil {
		t.Fatalf("ListTaxonomyProposals: %v", err)
	}
	for _, v := range views {
		if v.ID == id {
			return v
		}
	}
	t.Fatalf("proposal %d is not in the list the API serves (%d rows)", id, len(views))
	return TaxonomyProposalView{}
}

// TestTheProposalListReportsTheTagsSideOfTheSameTaxonomy is a cheap guard on the
// other half of §4.3's page: ListTags must keep returning a non-nil slice, because
// the page groups them and `null` is not iterable.
func TestTheProposalListReportsTheTagsSideOfTheSameTaxonomy(t *testing.T) {
	st, _ := setupWithUser(t)
	tags, err := st.ListTags(context.Background(), "", true)
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if tags == nil {
		t.Error("ListTags returned nil; the page iterates it")
	}
}

// TestAConsentFromOutsideTheElectorateIsRefusedAndNotCounted.
//
// The quorum this store divides by has always been eligibleTaggers -- members with a
// contributing role -- and nothing checked that the CONSENTER was one of them. So a
// ballot from outside the electorate was counted into the numerator: any account could
// ratify a tag proposal for a project it had no relationship with, and a guest identity
// (no members row anywhere, by construction) could do it namelessly. Found by the
// browser suite, which clicked the button as a visitor and watched a proposal move to
// "insufficient_quorum (1/1 consents)".
//
// Both directions, because a refusal that also refuses the people it is meant to admit
// would satisfy half of this test: the outsider AND the guest are refused and counted
// by nobody, then an eligible contributor still consents.
func TestAConsentFromOutsideTheElectorateIsRefusedAndNotCounted(t *testing.T) {
	ctx := context.Background()
	st, owner, pid := setupWithProject(t)
	contributor := newUserNamed(t, st, "electorate-contributor")
	outsider := newUserNamed(t, st, "electorate-outsider")

	if _, err := st.ExecContext(ctx,
		`INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?, 'contributor', ?)`,
		pid, contributor, float64(1790000000)); err != nil {
		t.Fatalf("add contributor: %v", err)
	}
	guest, _, err := st.CreateGuest(ctx)
	if err != nil {
		t.Fatalf("CreateGuest: %v", err)
	}

	p, err := st.ProposeTaxonomyChange(ctx, pid, owner, TaxonomyCreate, "topic:electorate", "", "why")
	if err != nil {
		t.Fatalf("ProposeTaxonomyChange: %v", err)
	}

	// An outsider with an account, and a guest with no members row anywhere.
	for _, who := range []struct {
		name string
		id   int64
	}{{"an account with no role in the project", outsider},
		{"a guest identity", guest.ID}} {
		if _, err := st.RatifyTaxonomyProposal(ctx, p.ID, who.id); !errors.Is(err, ErrPerm) {
			t.Errorf("%s was allowed to consent (err = %v); the quorum counts eligible "+
				"taggers and the ballot has to come from one of them", who.name, err)
		}
		after := viewFor(t, ctx, st, pid, p.ID)
		if after.ConsentCount != 0 {
			t.Errorf("%s was refused but the consent was counted anyway (%d)",
				who.name, after.ConsentCount)
		}
		if after.Status != "pending" {
			t.Errorf("%s moved the proposal to %q without being able to consent",
				who.name, after.Status)
		}
	}

	// And the rule still admits the people it is about: below quorum is the ordinary
	// outcome, so a conflict here is the proposal not being ratified yet, not a refusal.
	if _, err := st.RatifyTaxonomyProposal(ctx, p.ID, contributor); err != nil &&
		!errors.Is(err, ErrConflict) {
		t.Fatalf("an eligible contributor could not consent: %v", err)
	}
	if got := viewFor(t, ctx, st, pid, p.ID).ConsentCount; got != 1 {
		t.Errorf("consent_count = %d after an eligible contributor consented, want 1", got)
	}
}
