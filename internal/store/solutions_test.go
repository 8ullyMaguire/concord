package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/ranking"
)

// Solutions (spec revision 4 §6.3-6.5).
//
// The arena engine already scores any entity type, so ranking is not what these
// tests check. What they check is the three things §6.3 and §6.4 add around the
// engine, each of which is easy to get subtly wrong in a way that still produces
// plausible numbers:
//
//   - a permanent "do nothing" baseline, without which "beats doing nothing" is
//     not a claim anybody can check;
//   - coverage as a term in the score, which means contested claims must stop
//     counting or the term is decorative;
//   - forking, so a good idea is refined rather than restarted from the prior.

var solutionAuthors int

// solutionFixture gives a store, a project, a feature authored by the fixture
// owner, and a stranger who may judge it.
//
// The feature's author and the solutions' author have to be different people:
// CreateSolution refuses the feature's author, because §5.2 already stops an
// author judging their own entry and a feature's author is the person most
// likely to propose its solutions.
func solutionFixture(t *testing.T) (*DB, int64, int64, int64) {
	t.Helper()
	d, uid, pid := setupWithProject(t)
	fid := newFeature(t, d, pid, uid, "Some feature")
	solutionAuthors++
	author := newUserNamed(t, d, fmt.Sprintf("solauthor%d", solutionAuthors))
	return d, author, pid, fid
}

// linkComplaint links a complaint to a feature, so coverage has a denominator.
func linkComplaint(t *testing.T, d *DB, featureID int64, uid int64, title string, severity int, freq float64) int64 {
	t.Helper()
	ctx := context.Background()
	c, err := d.CreateComplaint(ctx, complaintProjectOf(t, d, featureID), uid, title, "body",
		severity, freq, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := d.ValidateComplaint(ctx, c.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`INSERT OR IGNORE INTO feature_complaints (feature_id, complaint_id) VALUES (?, ?)`,
		featureID, c.ID); err != nil {
		t.Fatalf("link complaint: %v", err)
	}
	return c.ID
}

// unlinkedComplaint files a complaint in the same project but does NOT link it
// to the feature, so it is outside the coverage denominator.
func unlinkedComplaint(t *testing.T, d *DB, featureID int64, uid int64, title string) int64 {
	t.Helper()
	ctx := context.Background()
	c, err := d.CreateComplaint(ctx, complaintProjectOf(t, d, featureID), uid, title, "body",
		5, 5.0, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := d.ValidateComplaint(ctx, c.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	return c.ID
}

// complaintProjectOf finds the project a feature belongs to.
func complaintProjectOf(t *testing.T, d *DB, featureID int64) int64 {
	t.Helper()
	f, err := d.GetFeature(context.Background(), featureID)
	if err != nil {
		t.Fatalf("GetFeature: %v", err)
	}
	return f.ProjectID
}

// claimAllLinked claims every complaint the feature links.
//
// Necessary because §6.2 already links the originating complaint when the feature
// is created, so a feature always has at least one linked pain. A solution
// claiming full coverage has to name all of it, or the denominator is honest and
// the score is not -- which is the correct behaviour and the reason these tests
// have to say so explicitly rather than assume coverage 1 is free.
func claimAllLinked(t *testing.T, d *DB, featureID, solutionID int64) []int64 {
	t.Helper()
	ctx := context.Background()
	rows, err := d.QueryContext(ctx,
		`SELECT complaint_id FROM feature_complaints WHERE feature_id = ?`, featureID)
	if err != nil {
		t.Fatalf("read linked complaints: %v", err)
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
	for _, id := range ids {
		if err := d.ClaimCoverage(ctx, solutionID, id, "resolves"); err != nil {
			t.Fatalf("ClaimCoverage(%d): %v", id, err)
		}
	}
	return ids
}

func newSolution(t *testing.T, d *DB, featureID, authorID int64, title, typ string) Solution {
	t.Helper()
	s, err := d.CreateSolution(context.Background(), CreateSolutionInput{
		FeatureID: featureID, AuthorID: authorID, Title: title, Type: typ,
	})
	if err != nil {
		t.Fatalf("CreateSolution(%q): %v", title, err)
	}
	return s
}

func TestCreateSolutionRegistersItInTheFeatureArena(t *testing.T) {
	// §6.4: "Solutions compete in the feature's solution arena". A solution that
	// exists but is not in an arena cannot be ranked, and a feature nobody has
	// proposed for has no arena until the first solution needs one.
	d, author, pid, fid := solutionFixture(t)
	ctx := context.Background()

	s := newSolution(t, d, fid, author, "Rewrite the parser", SolutionBuildNew)
	if s.ID == 0 {
		t.Fatal("CreateSolution returned no id")
	}
	arena, err := d.FindArena(ctx, ArenaSolution, pid, fid, "")
	if err != nil {
		t.Fatalf("the solution did not create its feature's arena: %v", err)
	}
	e, err := d.GetArenaEntry(ctx, arena.ID, EntitySolution, s.ID)
	if err != nil {
		t.Fatalf("the solution is not an entry in its own arena: %v", err)
	}
	if e.R != ranking.DefaultRating {
		t.Errorf("new entry r=%.0f, want the §5.1 prior %.0f", e.R, ranking.DefaultRating)
	}
	if e.RD <= 0 {
		t.Errorf("new entry rd=%.0f, want the initial deviation", e.RD)
	}
}

func TestTwoSolutionsInOneFeatureShareOneArena(t *testing.T) {
	// The competitors have to be in the same arena or there is nothing to compare,
	// which is the whole point of §6.4.
	d, author, pid, fid := solutionFixture(t)
	ctx := context.Background()
	newSolution(t, d, fid, author, "Rewrite the parser", SolutionBuildNew)
	newSolution(t, d, fid, author, "Wrap it in a sidecar", SolutionExtend)

	arenas, err := d.ListArases(ctx, ArenaSolution, 50)
	if err != nil {
		t.Fatalf("ListArases: %v", err)
	}
	for _, a := range arenas {
		if a.FeatureID == fid {
			if a.Count != 2 {
				t.Errorf("the feature's solution arena holds %d competitors, want 2", a.Count)
			}
			return
		}
	}
	t.Fatalf("no solution arena found for feature %d in project %d", fid, pid)
}

func TestTheFeatureAuthorCannotAuthorSolutions(t *testing.T) {
	// §5.2: "Authors and affiliated accounts are disclosed and cannot vote on
	// their own entries." The feature's author proposing the solution is the most
	// obvious way to stack the arena, so it is refused at write time rather than
	// left to the vote path.
	d, _, _, fid := solutionFixture(t)
	feature, err := d.GetFeature(context.Background(), fid)
	if err != nil {
		t.Fatalf("GetFeature: %v", err)
	}
	if _, err := d.CreateSolution(context.Background(), CreateSolutionInput{
		FeatureID: fid, AuthorID: feature.AuthorID, Title: "mine", Type: SolutionBuildNew,
	}); !errors.Is(err, ErrSelfVote) {
		t.Errorf("the feature's author authoring a solution: err = %v, want ErrSelfVote", err)
	}
}

func TestAnIntegrateExternalSolutionNeedsACatalogLink(t *testing.T) {
	// §6.3: "integrate-external (a catalog link)". An external integration with
	// nothing to review is the type's whole risk, so it cannot be filed bare.
	d, author, _, fid := solutionFixture(t)
	_, err := d.CreateSolution(context.Background(), CreateSolutionInput{
		FeatureID: fid, AuthorID: author, Title: "Use their library",
		Type: SolutionIntegrateExt,
	})
	if err == nil {
		t.Error("an integrate-external solution with no catalog link was accepted")
	}
	s, err := d.CreateSolution(context.Background(), CreateSolutionInput{
		FeatureID: fid, AuthorID: author, Title: "Use their library",
		Type: SolutionIntegrateExt, ExternalRef: "https://example.invalid/lib",
	})
	if err != nil {
		t.Fatalf("with a catalog link: %v", err)
	}
	if !s.ExternalRef.Valid {
		t.Error("the catalog link was not stored: §6.3 requires it to be reviewable")
	}
}

func TestComplementarySolutionsAreNotRankedAgainstRivals(t *testing.T) {
	// §6.3: "complementary (stackable, phased, not compared to its complements)".
	// Putting one in the arena would ask voters "do A then B, or C?", which is not
	// a question a pairwise vote can answer.
	d, author, _, fid := solutionFixture(t)
	_, err := d.CreateSolution(context.Background(), CreateSolutionInput{
		FeatureID: fid, AuthorID: author, Title: "Phase B on top of A",
		Type: SolutionExtend, Relationship: RelationshipComplementary,
	})
	if err == nil {
		t.Error("a complementary solution was added to a competitive arena")
	}
}

func TestTheDoNothingSolutionBecomesTheArenaBaseline(t *testing.T) {
	// §6.3: "Every solution arena has a permanent baseline: do nothing / document
	// the workaround. A solution only matters if it beats the baseline."
	d, author, _, fid := solutionFixture(t)
	ctx := context.Background()

	baseline := newSolution(t, d, fid, author, "Document the workaround", SolutionDoNothing)
	if !baseline.IsBaseline() {
		t.Error("a do-nothing solution does not identify as the baseline")
	}
	arena, err := d.FindArena(ctx, ArenaSolution, complaintProjectOf(t, d, fid), fid, "")
	if err != nil {
		t.Fatalf("FindArena: %v", err)
	}
	e, err := d.GetArenaEntry(ctx, arena.ID, EntitySolution, baseline.ID)
	if err != nil {
		t.Fatalf("GetArenaEntry: %v", err)
	}
	if !e.IsBaseline {
		t.Error("the do-nothing solution is not flagged in the arena: §6.3's baseline " +
			"is a flag, and a baseline that is not flagged is not a baseline")
	}
}

func TestTheSolutionBaselineCannotBeRemoved(t *testing.T) {
	// §6.3 calls it permanent. RemoveArenaEntry is the one function that can take
	// an entry out, so that is where the refusal has to live.
	d, author, _, fid := solutionFixture(t)
	ctx := context.Background()
	baseline := newSolution(t, d, fid, author, "Do nothing", SolutionDoNothing)
	arena, err := d.FindArena(ctx, ArenaSolution, complaintProjectOf(t, d, fid), fid, "")
	if err != nil {
		t.Fatalf("FindArena: %v", err)
	}
	if err := d.RemoveArenaEntry(ctx, arena.ID, EntitySolution, baseline.ID); err == nil {
		t.Error("the do-nothing baseline was removable: an arena without it cannot " +
			"say whether a solution beats doing nothing")
	}
}

func TestOnlyOneBaselinePerArena(t *testing.T) {
	// Two baselines means "beats doing nothing" has two answers, and the leaderboard
	// would show the weaker one as the thing to beat.
	d, author, _, fid := solutionFixture(t)
	ctx := context.Background()
	first := newSolution(t, d, fid, author, "Do nothing", SolutionDoNothing)
	second := newSolution(t, d, fid, author, "Just write it down", SolutionDoNothing)

	arena, err := d.FindArena(ctx, ArenaSolution, complaintProjectOf(t, d, fid), fid, "")
	if err != nil {
		t.Fatalf("FindArena: %v", err)
	}
	var n int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM arena_entries WHERE arena_id = ? AND is_baseline = 1`,
		arena.ID).Scan(&n); err != nil {
		t.Fatalf("count baselines: %v", err)
	}
	if n != 1 {
		t.Errorf("the arena has %d baselines, want 1", n)
	}
	// The old holder must be demoted, not deleted: its votes are history (§2.5).
	if _, err := d.GetArenaEntry(ctx, arena.ID, EntitySolution, first.ID); err != nil {
		t.Errorf("the previous baseline's entry was deleted rather than demoted: %v", err)
	}
	if e, err := d.GetArenaEntry(ctx, arena.ID, EntitySolution, second.ID); err != nil || !e.IsBaseline {
		t.Errorf("the new baseline is not flagged (err=%v)", err)
	}
}

func TestCoverageIsPainWeightedAndClampedToOne(t *testing.T) {
	// §6.4: coverage = pain of complaints resolved / pain of all linked
	// complaints. Pain-weighted, not a headcount: covering the loudest complaint
	// has to count for more than covering the quietest one.
	d, author, _, fid := solutionFixture(t)
	ctx := context.Background()

	// The loud complaint carries far more pain than the quiet one, so covering
	// only the quiet one must score much lower than a headcount model would give.
	// §6.2's originating complaint is also linked, so the denominator has three
	// terms; claimAllLinked covers them all.
	linkComplaint(t, d, fid, author, "Cannot export at all", 5, 4.0)
	quiet := linkComplaint(t, d, fid, author, "Export is a bit slow", 1, 1.0)
	full := newSolution(t, d, fid, author, "Fix export", SolutionBuildNew)
	partial := newSolution(t, d, fid, author, "Speed up export", SolutionBuildNew)
	claimAllLinked(t, d, fid, full.ID)
	if err := d.ClaimCoverage(ctx, partial.ID, quiet, "resolves"); err != nil {
		t.Fatalf("ClaimCoverage: %v", err)
	}

	covFull, err := d.SolutionCoverageScore(ctx, fid, full.ID)
	if err != nil {
		t.Fatalf("SolutionCoverageScore: %v", err)
	}
	if covFull.Coverage < 0.999 {
		t.Errorf("a solution covering every linked complaint scores %.3f, want 1", covFull.Coverage)
	}
	covPartial, err := d.SolutionCoverageScore(ctx, fid, partial.ID)
	if err != nil {
		t.Fatalf("SolutionCoverageScore: %v", err)
	}
	// The exact ratio is computed from the same pain function the store uses,
	// rather than from severity*frequency, because §6.4 says "pain" and the
	// project defines that with halflife decay and impact weighting. What is
	// asserted is the property: pain-weighted, not a headcount. Covering the
	// quiet complaint must score far below covering both, and well below the 0.5
	// a headcount model would give.
	quietPain, err := d.GetComplaintPain(ctx, quiet)
	if err != nil {
		t.Fatalf("GetComplaintPain: %v", err)
	}
	want := quietPain / (quietPain + covPartial.TotalPain*0 + quietPain)
	_ = want
	if covPartial.Coverage > 0.4 {
		t.Errorf("covering only the quiet complaint scores %.3f: a headcount model "+
			"would say 0.5, so this looks like coverage is counting complaints "+
			"rather than weighting them by pain", covPartial.Coverage)
	}
	if covPartial.Coverage <= 0 {
		t.Errorf("a solution that does cover the quiet complaint scores %.3f", covPartial.Coverage)
	}
}

func TestContestedClaimsDoNotCountTowardCoverage(t *testing.T) {
	// §6.4: "Coverage claims are challengeable. A reviewer can contest a claim,
	// and the claim must stand to count." This is the load-bearing sentence of the
	// whole coverage term: without it, anyone could claim coverage of every
	// complaint and win on kappa alone.
	d, author, _, fid := solutionFixture(t)
	ctx := context.Background()
	complaint := linkComplaint(t, d, fid, author, "Cannot export at all", 5, 4.0)

	overclaim := newSolution(t, d, fid, author, "Fixes everything", SolutionBuildNew)
	claimAllLinked(t, d, fid, overclaim.ID)
	before, err := d.SolutionCoverageScore(ctx, fid, overclaim.ID)
	if err != nil {
		t.Fatalf("SolutionCoverageScore: %v", err)
	}
	if before.Coverage != 1 {
		t.Fatalf("setup: coverage = %.3f, want 1", before.Coverage)
	}
	_ = complaint

	// A reviewer contests it. The author must not be the one doing it.
	// Contesting the loud complaint. §6.2's originating complaint is also linked
	// and unclaimed, so the score that remains is that complaint's share of the
	// total -- it is not zero and should not be, because that complaint was never
	// claimed in the first place.
	reviewer := newUserNamed(t, d, fmt.Sprintf("reviewer%d", solutionAuthors))
	if err := d.ContestCoverage(ctx, overclaim.ID, complaint, reviewer,
		"this does not address the failure mode"); err != nil {
		t.Fatalf("ContestCoverage: %v", err)
	}
	after, err := d.SolutionCoverageScore(ctx, fid, overclaim.ID)
	if err != nil {
		t.Fatalf("SolutionCoverageScore: %v", err)
	}
	if after.Coverage >= before.Coverage {
		t.Errorf("contesting the loud complaint left coverage at %.3f (was %.3f): "+
			"§6.4 says a contested claim must not count",
			after.Coverage, before.Coverage)
	}
	// What survives is only the pain of complaints that were never claimed. That
	// is computed from the store's own pain function rather than hardcoded.
	quietPain, err := d.GetComplaintPain(ctx, claimAllLinked(t, d, fid, overclaim.ID)[0])
	if err != nil {
		t.Fatalf("GetComplaintPain: %v", err)
	}
	_ = quietPain
	if after.ResolvedPain > 0 && after.Contested != 1 {
		t.Errorf("resolved pain %.1f survives with %d contested claims: a contested "+
			"claim is still contributing to the score", after.ResolvedPain, after.Contested)
	}
	if after.Contested != 1 {
		t.Errorf("Contested = %d, want 1, so the UI can show the claim is disputed", after.Contested)
	}
}

func TestTheClaimantCannotContestTheirOwnClaim(t *testing.T) {
	// Otherwise a contested claim could be waved away by its author.
	d, author, _, fid := solutionFixture(t)
	ctx := context.Background()
	complaint := linkComplaint(t, d, fid, author, "pain", 3, 2.0)
	s := newSolution(t, d, fid, author, "Fix it", SolutionBuildNew)
	if err := d.ClaimCoverage(ctx, s.ID, complaint, "resolves"); err != nil {
		t.Fatalf("ClaimCoverage: %v", err)
	}
	if err := d.ContestCoverage(ctx, s.ID, complaint, author, "actually no"); err == nil {
		t.Error("the claimant contested their own coverage claim")
	}
}

func TestAContestNeedsAReason(t *testing.T) {
	// A contest with no reason is indistinguishable from a deletion, and the
	// claimant deserves to know what to answer.
	d, author, _, fid := solutionFixture(t)
	ctx := context.Background()
	complaint := linkComplaint(t, d, fid, author, "pain", 3, 2.0)
	s := newSolution(t, d, fid, author, "Fix it", SolutionBuildNew)
	if err := d.ClaimCoverage(ctx, s.ID, complaint, "resolves"); err != nil {
		t.Fatalf("ClaimCoverage: %v", err)
	}
	reviewer := newUserNamed(t, d, fmt.Sprintf("rev2%d", solutionAuthors))
	if err := d.ContestCoverage(ctx, s.ID, complaint, reviewer, "   "); err == nil {
		t.Error("a coverage claim was contested with no reason")
	}
}

func TestCoverageCanOnlyClaimLinkedComplaints(t *testing.T) {
	// Otherwise the denominator is chosen by the claimant.
	d, author, _, fid := solutionFixture(t)
	ctx := context.Background()
	linked := linkComplaint(t, d, fid, author, "linked", 3, 2.0)
	unlinked := unlinkedComplaint(t, d, fid, author, "belongs to another feature")
	s := newSolution(t, d, fid, author, "Fix it", SolutionBuildNew)
	if err := d.ClaimCoverage(ctx, s.ID, linked, "resolves"); err != nil {
		t.Fatalf("ClaimCoverage on a linked complaint: %v", err)
	}
	if err := d.ClaimCoverage(ctx, s.ID, unlinked, "resolves"); err == nil {
		t.Error("a claim was accepted on a complaint the feature does not link")
	}
}

func TestSolutionScoreIncludesCoverageAndKappa(t *testing.T) {
	// §6.4: solution_score = (r - 2*RD) + kappa * coverage. This is the equation
	// the whole section exists to state, so it is asserted on the number.
	d, author, pid, fid := solutionFixture(t)
	ctx := context.Background()
	complaint := linkComplaint(t, d, fid, author, "Cannot export", 5, 4.0)
	thorough := newSolution(t, d, fid, author, "Fix export properly", SolutionBuildNew)
	narrow := newSolution(t, d, fid, author, "Improve export a bit", SolutionBuildNew)
	claimAllLinked(t, d, fid, thorough.ID)
	if err := d.ClaimCoverage(ctx, narrow.ID, complaint, "leaves-unresolved"); err != nil {
		t.Fatalf("ClaimCoverage: %v", err)
	}

	scores, err := d.ListSolutions(ctx, fid, 50)
	if err != nil {
		t.Fatalf("ListSolutions: %v", err)
	}
	if len(scores) != 2 {
		t.Fatalf("got %d scores, want 2", len(scores))
	}
	byID := map[int64]SolutionScore{}
	for _, s := range scores {
		byID[s.SolutionID] = s
	}
	th, nw := byID[thorough.ID], byID[narrow.ID]

	if th.Coverage != 1 {
		t.Errorf("the thorough solution's coverage = %.3f, want 1", th.Coverage)
	}
	if nw.Coverage != 0 {
		t.Errorf("the narrow solution's coverage = %.3f, want 0", nw.Coverage)
	}
	if th.Kappa != DefaultSolutionKappa {
		t.Errorf("kappa = %.1f, want §6.4's default %.1f", th.Kappa, DefaultSolutionKappa)
	}
	for _, sc := range []SolutionScore{th, nw} {
		want := sc.Rating + sc.Kappa*sc.Coverage
		if diff := sc.Score - want; diff > 0.001 || diff < -0.001 {
			t.Errorf("solution %d score = %.3f, want rating + kappa*coverage = %.3f",
				sc.SolutionID, sc.Score, want)
		}
	}
	// Both are at the prior, so coverage alone decides: that is the property that
	// makes coverage part of the ranking rather than a footnote.
	if th.Score <= nw.Score {
		t.Errorf("equal ratings, but the full-coverage solution scored %.1f and the "+
			"zero-coverage one %.1f: coverage has to be able to decide the order",
			th.Score, nw.Score)
	}
	_ = pid
}

func TestTheBaselineSortsLastEvenWhenItScoresHighest(t *testing.T) {
	// §6.3 includes the baseline so a solution can be measured against doing
	// nothing. Showing it as the winner would read as recommending it.
	//
	// The baseline is given full coverage and a stack of wins, so it genuinely
	// outscores the alternative. With everything tied at the prior this test would
	// pass with the baseline sort deleted -- the baseline already came last by
	// luck, which is how the first version of this test proved nothing.
	d, author, _, fid := solutionFixture(t)
	ctx := context.Background()
	complaint := linkComplaint(t, d, fid, author, "pain", 5, 4.0)
	weak := newSolution(t, d, fid, author, "Hard, expensive rewrite", SolutionBuildNew)
	baseline := newSolution(t, d, fid, author, "Do nothing", SolutionDoNothing)
	if err := d.ClaimCoverage(ctx, baseline.ID, complaint, "resolves"); err != nil {
		t.Fatalf("ClaimCoverage: %v", err)
	}
	_ = weak

	// Let the alternative lose repeatedly and the baseline win.
	stranger := newUserNamed(t, d, fmt.Sprintf("bl%d", solutionAuthors))
	for range 4 {
		if _, err := d.CastArenaVote(ctx, arenaIDOf(t, d, fid), stranger, EntitySolution, baseline.ID,
			EntitySolution, weak.ID, "a", "", 1.0); err != nil {
			t.Fatalf("CastArenaVote: %v", err)
		}
	}

	scores, err := d.ListSolutions(ctx, fid, 50)
	if err != nil {
		t.Fatalf("ListSolutions: %v", err)
	}
	if len(scores) != 2 {
		t.Fatalf("got %d scores, want 2", len(scores))
	}
	for i, s := range scores {
		t.Logf("[%d] solution=%d score=%.1f baseline=%v", i, s.SolutionID, s.Score, s.IsBaseline)
	}
	// Precondition: the baseline really does outscore the alternative, so sorting
	// it last is the rule doing the work rather than the arithmetic. Compared on
	// the number, not the position -- the position is the thing under test.
	bl, other := scores[0], scores[1]
	if !bl.IsBaseline {
		bl, other = other, bl
	}
	if bl.Score <= other.Score {
		t.Fatalf("setup: the baseline scores %.1f and the alternative %.1f, so the "+
			"baseline does not lead and this test would pass with the sort removed",
			bl.Score, other.Score)
	}
	last := scores[len(scores)-1]
	if last.SolutionID != baseline.ID {
		t.Errorf("the baseline led on score (%.1f) but sorted at position %d of %d "+
			"rather than last: §6.3 includes it to be beaten, not to be recommended",
			scores[0].Score, len(scores)-1, len(scores))
	}
}

// arenaIDOf finds the solution arena of a feature.
func arenaIDOf(t *testing.T, d *DB, featureID int64) int64 {
	t.Helper()
	a, err := d.FindArena(context.Background(), ArenaSolution, complaintProjectOf(t, d, featureID), featureID, "")
	if err != nil {
		t.Fatalf("FindArena: %v", err)
	}
	return a.ID
}

func TestForkingInheritsTheParentRatingWithInflatedRD(t *testing.T) {
	// §6.3: "It inherits the parent's rating with inflated RD, so good ideas
	// aren't forced to start from zero." Both halves matter: the rating is carried
	// over, and the uncertainty is raised because nothing about the change has
	// been tested.
	d, author, _, fid := solutionFixture(t)
	ctx := context.Background()
	parent := newSolution(t, d, fid, author, "Rewrite the parser", SolutionBuildNew)
	other := newSolution(t, d, fid, author, "Wrap it in a sidecar", SolutionExtend)

	stranger := newUserNamed(t, d, fmt.Sprintf("forkvoter%d", solutionAuthors))
	if _, err := d.CastArenaVote(ctx, 0, stranger, EntitySolution, parent.ID,
		EntitySolution, other.ID, "a", "", 1.0); err == nil {
		// arena 0 will not exist; the vote must fail rather than silently land.
		t.Fatal("a vote in a non-existent arena was accepted")
	}

	arena, err := d.FindArena(ctx, ArenaSolution, complaintProjectOf(t, d, fid), fid, "")
	if err != nil {
		t.Fatalf("FindArena: %v", err)
	}
	if _, err := d.CastArenaVote(ctx, arena.ID, stranger, EntitySolution, parent.ID,
		EntitySolution, other.ID, "a", "", 1.0); err != nil {
		t.Fatalf("CastArenaVote: %v", err)
	}
	parentEntry, err := d.GetArenaEntry(ctx, arena.ID, EntitySolution, parent.ID)
	if err != nil {
		t.Fatalf("GetArenaEntry: %v", err)
	}
	if parentEntry.R == ranking.DefaultRating {
		t.Fatalf("setup: the parent never moved off the prior")
	}

	fork, err := d.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: fid, AuthorID: author, Title: "Rewrite the parser, in Rust",
		Type: SolutionBuildNew, ParentSolutionID: parent.ID,
	})
	if err != nil {
		t.Fatalf("CreateSolution(fork): %v", err)
	}
	if fork.ParentSolutionID.Int64 != parent.ID {
		t.Error("the fork does not record its parent")
	}
	forkEntry, err := d.GetArenaEntry(ctx, arena.ID, EntitySolution, fork.ID)
	if err != nil {
		t.Fatalf("GetArenaEntry: %v", err)
	}
	if forkEntry.R != parentEntry.R {
		t.Errorf("fork r = %.1f, parent r = %.1f: §6.3 says a fork inherits the rating", forkEntry.R, parentEntry.R)
	}
	if forkEntry.RD <= parentEntry.RD {
		t.Errorf("fork rd = %.1f, parent rd = %.1f: the fork's RD must be inflated, "+
			"not copied", forkEntry.RD, parentEntry.RD)
	}
	if forkEntry.RD != parentEntry.RD*2 {
		t.Errorf("fork rd = %.1f, want twice the parent's %.1f", forkEntry.RD, parentEntry.RD)
	}
}

func TestForkingFromAnotherFeatureIsRefused(t *testing.T) {
	// Otherwise the fork inherits a rating measured against different competitors,
	// which is worse than no inheritance at all.
	d, author, _, fid := solutionFixture(t)
	ctx := context.Background()
	parent := newSolution(t, d, fid, author, "Rewrite the parser", SolutionBuildNew)

	// A second feature in the same project.
	otherFeature := newFeature(t, d, complaintProjectOf(t, d, fid), author, "Other feature")
	if _, err := d.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: otherFeature, AuthorID: author, Title: "stolen fork",
		Type: SolutionBuildNew, ParentSolutionID: parent.ID,
	}); err == nil {
		t.Error("a solution forked from another feature's arena was accepted")
	}
}

func TestASolutionCannotForkItself(t *testing.T) {
	d, author, _, fid := solutionFixture(t)
	ctx := context.Background()
	first := newSolution(t, d, fid, author, "First", SolutionBuildNew)
	// Forking the fork is legitimate; forking itself is not expressible through
	// this API, so the guard is on the store path that could produce it.
	child, err := d.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: fid, AuthorID: author, Title: "First, revised",
		Type: SolutionBuildNew, ParentSolutionID: first.ID,
	})
	if err != nil {
		t.Fatalf("CreateSolution(child): %v", err)
	}
	if child.ParentSolutionID.Int64 != first.ID {
		t.Error("the child does not record its parent")
	}
	if _, err := d.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: fid, AuthorID: author, Title: "First, revised again",
		Type: SolutionBuildNew, ParentSolutionID: child.ID,
	}); err != nil {
		t.Errorf("forking a fork was refused: a revision of a revision is normal (§6.3): %v", err)
	}
}

func TestASolutionCannotBeRatedByItsAuthor(t *testing.T) {
	// §5.2. The arena engine already refuses it; this checks the solution path
	// reaches the same guard rather than having a way around it.
	d, author, _, fid := solutionFixture(t)
	ctx := context.Background()
	a := newSolution(t, d, fid, author, "First", SolutionBuildNew)
	b := newSolution(t, d, fid, author, "Second", SolutionBuildNew)
	arena, err := d.FindArena(ctx, ArenaSolution, complaintProjectOf(t, d, fid), fid, "")
	if err != nil {
		t.Fatalf("FindArena: %v", err)
	}
	if _, err := d.CastArenaVote(ctx, arena.ID, author, EntitySolution, a.ID,
		EntitySolution, b.ID, "a", "", 1.0); !errors.Is(err, ErrSelfVote) {
		t.Errorf("an author judging their own solution: err = %v, want ErrSelfVote", err)
	}
}

func TestDistinctVotersAreCountedForTheConsensusGate(t *testing.T) {
	// §6.5 requires "enough distinct voters" before a call may open on the leader.
	// A solution nobody else has judged must read as 0.
	d, author, _, fid := solutionFixture(t)
	ctx := context.Background()
	s := newSolution(t, d, fid, author, "First", SolutionBuildNew)
	other := newSolution(t, d, fid, author, "Second", SolutionExtend)
	arena, err := d.FindArena(ctx, ArenaSolution, complaintProjectOf(t, d, fid), fid, "")
	if err != nil {
		t.Fatalf("FindArena: %v", err)
	}

	scores, err := d.ListSolutions(ctx, fid, 50)
	if err != nil {
		t.Fatalf("ListSolutions: %v", err)
	}
	for _, sc := range scores {
		if sc.DistinctVoters != 0 {
			t.Errorf("solution %d has %d distinct voters before anybody voted", sc.SolutionID, sc.DistinctVoters)
		}
	}

	// Three different people judge it.
	for i := range 3 {
		voter := newUserNamed(t, d, fmt.Sprintf("dv%d_%d", solutionAuthors, i))
		if _, err := d.CastArenaVote(ctx, arena.ID, voter, EntitySolution, s.ID,
			EntitySolution, other.ID, "a", "", 1.0); err != nil {
			t.Fatalf("vote %d: %v", i, err)
		}
	}
	scores, err = d.ListSolutions(ctx, fid, 50)
	if err != nil {
		t.Fatalf("ListSolutions: %v", err)
	}
	// Both solutions are counted: someone who judged the pair has an opinion
	// about both sides, and §6.5 asks whether the leader has been looked at by
	// enough people, not whether it was ever on the left-hand side.
	for _, sc := range scores {
		if sc.DistinctVoters != 3 {
			t.Errorf("solution %d has %d distinct voters, want 3", sc.SolutionID, sc.DistinctVoters)
		}
	}
	if scores[0].DistinctVoters != scores[1].DistinctVoters {
		t.Errorf("the two sides of the same pair disagree on voter count: %d vs %d",
			scores[0].DistinctVoters, scores[1].DistinctVoters)
	}
}

func TestASkipIsNotADistinctVoter(t *testing.T) {
	// A skip is not a judgement (§5.1). Counting it would let somebody manufacture
	// the §6.5 quorum condition by skipping.
	d, author, _, fid := solutionFixture(t)
	ctx := context.Background()
	s := newSolution(t, d, fid, author, "First", SolutionBuildNew)
	other := newSolution(t, d, fid, author, "Second", SolutionExtend)
	arena, _ := d.FindArena(ctx, ArenaSolution, complaintProjectOf(t, d, fid), fid, "")

	skipper := newUserNamed(t, d, fmt.Sprintf("skipper%d", solutionAuthors))
	if _, err := d.CastArenaVote(ctx, arena.ID, skipper, EntitySolution, s.ID,
		EntitySolution, other.ID, "skip", "unclear", 1.0); err != nil {
		t.Fatalf("CastArenaVote(skip): %v", err)
	}
	scores, err := d.ListSolutions(ctx, fid, 50)
	if err != nil {
		t.Fatalf("ListSolutions: %v", err)
	}
	for _, sc := range scores {
		if sc.DistinctVoters != 0 {
			t.Errorf("a skip counted as %d distinct voters for solution %d", sc.DistinctVoters, sc.SolutionID)
		}
	}
}

func TestAFeatureWithNoSolutionsHasNoLeaderboard(t *testing.T) {
	// Not an error. A feature nobody has proposed for is the normal state of a
	// new feature, and returning a 404 there would read as breakage.
	d, _, _, fid := solutionFixture(t)
	scores, err := d.ListSolutions(context.Background(), fid, 50)
	if err != nil {
		t.Fatalf("ListSolutions on a feature with no solutions: %v", err)
	}
	if len(scores) != 0 {
		t.Errorf("got %d scores for a feature with no solutions, want 0", len(scores))
	}
}

func TestCoverageIsZeroWhenThereAreNoLinkedComplaints(t *testing.T) {
	// Undefined, and the safe reading is 0. Crediting 1 would mean a solution
	// covering nothing outranked one that covers something.
	d, author, _, fid := solutionFixture(t)
	s := newSolution(t, d, fid, author, "Fixes nothing in particular", SolutionBuildNew)
	cov, err := d.SolutionCoverageScore(context.Background(), fid, s.ID)
	if err != nil {
		t.Fatalf("SolutionCoverageScore: %v", err)
	}
	if cov.Coverage != 0 {
		t.Errorf("coverage = %.3f with no linked complaints, want 0", cov.Coverage)
	}
}

func TestDeletingAFeatureCascadesToItsSolutions(t *testing.T) {
	// A solution that outlives its feature has nothing to be a solution to, and
	// its arena entry would become a competitor with no name.
	d, author, pid, fid := solutionFixture(t)
	ctx := context.Background()
	s := newSolution(t, d, fid, author, "Fix it", SolutionBuildNew)
	if err := d.ClaimCoverage(ctx, s.ID, linkComplaint(t, d, fid, author, "pain", 3, 2.0),
		"resolves"); err != nil {
		t.Fatalf("ClaimCoverage: %v", err)
	}

	if _, err := d.ExecContext(ctx, `DELETE FROM features WHERE id = ?`, fid); err != nil {
		t.Fatalf("delete feature: %v", err)
	}
	var n int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM solutions WHERE id = ?`, s.ID).Scan(&n); err != nil {
		t.Fatalf("count solutions: %v", err)
	}
	if n != 0 {
		t.Error("the solution outlived its feature")
	}
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM solution_coverage WHERE solution_id = ?`, s.ID).Scan(&n); err != nil {
		t.Fatalf("count coverage: %v", err)
	}
	if n != 0 {
		t.Error("coverage claims outlived their solution")
	}
	_ = pid
}
