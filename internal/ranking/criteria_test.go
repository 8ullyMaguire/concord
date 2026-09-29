package ranking

import (
	"math"
	"testing"
)

// crit is a terse criterion builder for the table tests.
func crit(id int64, slug, dir string, def float64) Criterion {
	return Criterion{ID: id, Slug: slug, Name: slug, Direction: dir,
		DefaultW: def, Active: true}
}

func rat(feature, criterion int64, r, rd float64, games int) Rating {
	return Rating{FeatureID: feature, CriterionID: criterion,
		R: r, RD: rd, Sigma: 0.06, Games: games}
}

func closeTo(t *testing.T, got, want, tol float64, what string) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %v, want %v (±%v)", what, got, want, tol)
	}
}

// Naming one criterion must rank by THAT criterion alone.
//
// This test exists because the first implementation got it wrong: an unnamed
// criterion fell back to its own default weight, so "rank by design only"
// blended efficiency in at 0.5 and returned an order matching neither
// criterion. Caught only by inspecting a live response, because the unit tests
// all passed weights for every criterion and so never exercised the case.
func TestNamingOneCriterionExcludesTheOthers(t *testing.T) {
	cs := []Criterion{crit(1, "design", DirHigher, 1), crit(2, "efficiency", DirHigher, 1)}
	// Design: f1 > f2 > f3. Efficiency: f3 > f2 > f1 — exactly reversed.
	rs := map[int64][]Rating{
		1: {rat(1, 1, 1700, 30, 10), rat(2, 1, 1500, 30, 10), rat(3, 1, 1300, 30, 10)},
		2: {rat(1, 2, 1300, 30, 10), rat(2, 2, 1500, 30, 10), rat(3, 2, 1700, 30, 10)},
	}

	byDesign := Composite(cs, rs, []Weight{{CriterionID: 1, Weight: 1}}, CompositeOptions{})
	if len(byDesign) != 3 {
		t.Fatalf("want 3 results, got %d", len(byDesign))
	}
	for _, r := range byDesign {
		if len(r.Contributions) != 1 {
			t.Fatalf("feature %d has %d contributions; naming one criterion "+
				"must exclude the rest", r.FeatureID, len(r.Contributions))
		}
		if r.Contributions[0].CriterionID != 1 {
			t.Errorf("feature %d contributed criterion %d, want 1",
				r.FeatureID, r.Contributions[0].CriterionID)
		}
	}
	if byDesign[0].FeatureID != 1 {
		t.Errorf("design leader = %d, want 1", byDesign[0].FeatureID)
	}

	// And it must equal the single-criterion path, or "best designed" answers
	// two different questions depending on which endpoint you call.
	direct := RankByCriterion(cs[0], rs[1])
	for i := range direct {
		if direct[i].FeatureID != byDesign[i].FeatureID {
			t.Errorf("position %d: single-criterion=%d composite=%d",
				i, direct[i].FeatureID, byDesign[i].FeatureID)
		}
	}

	// Naming the other one must give the reverse order.
	byEfficiency := Composite(cs, rs, []Weight{{CriterionID: 2, Weight: 1}}, CompositeOptions{})
	if byEfficiency[0].FeatureID != 3 {
		t.Errorf("efficiency leader = %d, want 3", byEfficiency[0].FeatureID)
	}
}

// Naming no criteria at all still means "everything at its defaults", so a
// caller who just wants the project's overall view is not left with nothing.
func TestNoWeightsUsesEveryDefault(t *testing.T) {
	cs := []Criterion{crit(1, "design", DirHigher, 2), crit(2, "speed", DirHigher, 1)}
	rs := map[int64][]Rating{
		1: {rat(1, 1, 1700, 30, 10), rat(2, 1, 1300, 30, 10)},
		2: {rat(1, 2, 1700, 30, 10), rat(2, 2, 1300, 30, 10)},
	}
	got := Composite(cs, rs, nil, CompositeOptions{})
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	if len(got[0].Contributions) != 2 {
		t.Errorf("no weights gave %d contributions, want both criteria",
			len(got[0].Contributions))
	}
	// The 2:1 default must be honoured, not flattened to equal weight.
	w1 := map[string]float64{}
	for _, c := range got[0].Contributions {
		w1[c.Slug] = c.Weight
	}
	closeTo(t, w1["design"], 2.0/3.0, 1e-9, "design default share")
	closeTo(t, w1["speed"], 1.0/3.0, 1e-9, "speed default share")
}

// A single feature has nothing to be compared against. Scoring it 1.0 would
// claim it is the best thing in the project, which is an assertion nobody voted
// for. 0.5 means "unranked", and is the whole point of the test.
func TestNormaliseSingleEntryIsNeutral(t *testing.T) {
	got := normalised([]Rating{rat(1, 1, 2000, 50, 9)}, DirHigher)
	closeTo(t, got[1], 0.5, 1e-9, "single entry")
}

// Identical ratings mean a genuine tie, not a set of losers. 0.5 for all keeps
// the composite honest and stops a tie from being broken arbitrarily.
func TestNormaliseAllEqualIsTie(t *testing.T) {
	rs := []Rating{rat(1, 1, 1500, 40, 5), rat(2, 1, 1500, 40, 5), rat(3, 1, 1500, 40, 5)}
	got := normalised(rs, DirHigher)
	for id, v := range got {
		closeTo(t, v, 0.5, 1e-9, "tie member")
		_ = id
	}
}

// Min-max normalisation is the mechanism, so assert the endpoints are exact.
func TestNormaliseSpansZeroToOne(t *testing.T) {
	rs := []Rating{rat(1, 1, 1200, 40, 5), rat(2, 1, 1800, 40, 5)}
	got := normalised(rs, DirHigher)
	closeTo(t, got[1], 0.0, 1e-9, "worst")
	closeTo(t, got[2], 1.0, 1e-9, "best")
}

// "lower is better" must invert, or a cost criterion would rank the most
// expensive option first. This is the bug the direction field exists to prevent.
func TestLowerIsBetterInverts(t *testing.T) {
	rs := []Rating{rat(1, 1, 100, 40, 5), rat(2, 1, 900, 40, 5)}
	got := normalised(rs, DirLower)
	closeTo(t, got[1], 1.0, 1e-9, "cheapest under DirLower")
	closeTo(t, got[2], 0.0, 1e-9, "dearest under DirLower")
}

// A rating with no games carries the initial deviation. Composite may be asked
// to ignore such criteria, or they silently contribute 0.5 to everyone.
func TestRequireMinRDDropsUnratedCriterion(t *testing.T) {
	cs := []Criterion{crit(1, "design", DirHigher, 1), crit(2, "speed", DirHigher, 1)}
	// criterion 2 has ratings, but all untouched
	rs := map[int64][]Rating{
		1: {rat(1, 1, 1800, 30, 12), rat(2, 1, 1200, 30, 12)},
		2: {rat(1, 2, 1500, InitialRD, 0), rat(2, 2, 1500, InitialRD, 0)},
	}
	got := Composite(cs, rs, nil, CompositeOptions{RequireMinRD: true})
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	for _, r := range got {
		if len(r.Contributions) != 1 || r.Contributions[0].CriterionID != 1 {
			t.Errorf("feature %d contributed %d criteria, want only criterion 1",
				r.FeatureID, len(r.Contributions))
		}
	}
	// Without the flag, the unrated criterion participates and drags both to 0.5,
	// which is exactly the dilution the flag exists to prevent.
	diluted := Composite(cs, rs, nil, CompositeOptions{})
	if len(diluted) != 2 {
		t.Fatalf("want 2 results, got %d", len(diluted))
	}
	if len(diluted[0].Contributions) != 2 {
		t.Errorf("unfiltered composite dropped a criterion; want 2 contributions")
	}
}

// Weights are relative. 3:1 and 0.75:0.25 must produce the same ordering, or a
// caller cannot express "matters 3x more" without knowing the scale.
func TestWeightsAreScaleInvariant(t *testing.T) {
	cs := []Criterion{crit(1, "a", DirHigher, 1), crit(2, "b", DirHigher, 1)}
	rs := map[int64][]Rating{
		1: {rat(1, 1, 1600, 30, 10), rat(2, 1, 1400, 30, 10)},
		2: {rat(1, 2, 1400, 30, 10), rat(2, 2, 1600, 30, 10)},
	}
	threeToOne := Composite(cs, rs, []Weight{{1, 3}, {2, 1}}, CompositeOptions{})
	normalized := Composite(cs, rs, []Weight{{1, 0.75}, {2, 0.25}}, CompositeOptions{})
	if len(threeToOne) != 2 || len(normalized) != 2 {
		t.Fatalf("want 2 results each")
	}
	closeTo(t, threeToOne[0].Score, normalized[0].Score, 1e-9, "top score under 3:1")
	closeTo(t, threeToOne[1].Score, normalized[1].Score, 1e-9, "second score under 3:1")
	// Feature 1 wins design, feature 2 wins speed; weighting design 3:1 flips it.
	if threeToOne[0].FeatureID != 1 {
		t.Errorf("design-weighted composite put feature %d first, want 1", threeToOne[0].FeatureID)
	}
	// Even weight is a genuine tie, broken deterministically by feature id.
	even := Composite(cs, rs, []Weight{{1, 1}, {2, 1}}, CompositeOptions{})
	closeTo(t, even[0].Score, even[1].Score, 1e-9, "even weighting is a tie")
	if even[0].FeatureID != 1 {
		t.Errorf("tie broken to feature %d, want lowest id 1", even[0].FeatureID)
	}
}

// A caller who weights a criterion to zero means it. It must not fall back to
// the criterion's own default, or "ignore efficiency" would be impossible.
func TestExplicitZeroWeightOverridesDefault(t *testing.T) {
	cs := []Criterion{crit(1, "design", DirHigher, 1), crit(2, "speed", DirHigher, 9)}
	rs := map[int64][]Rating{
		1: {rat(1, 1, 1200, 30, 10), rat(2, 1, 1800, 30, 10)},
		2: {rat(1, 2, 1800, 30, 10), rat(2, 2, 1200, 30, 10)},
	}
	got := Composite(cs, rs, []Weight{{CriterionID: 1, Weight: 0}, {CriterionID: 2, Weight: 1}}, CompositeOptions{})
	if got[0].FeatureID != 1 {
		t.Errorf("zero-weighting design gave %d first; want 1 (the fast one)", got[0].FeatureID)
	}
	if len(got[0].Contributions) != 1 {
		t.Errorf("zero-weighted criterion still contributed")
	}
}

// Unrated must not be scored as bad. Imputing 0 would rank an unvoted feature
// below a genuinely poor one, which is a different claim entirely.
func TestMissingRatingIsNotPenalised(t *testing.T) {
	cs := []Criterion{crit(1, "design", DirHigher, 1), crit(2, "speed", DirHigher, 1)}
	rs := map[int64][]Rating{
		1: {rat(1, 1, 1800, 30, 10), rat(2, 1, 1200, 30, 10), rat(3, 1, 1500, 30, 10)},
		2: {rat(1, 2, 1500, 30, 10), rat(2, 2, 1500, 30, 10)}, // feature 3 unrated
	}
	got := Composite(cs, rs, nil, CompositeOptions{})
	for _, r := range got {
		if r.FeatureID == 3 && len(r.Contributions) != 1 {
			t.Errorf("feature 3 has %d contributions, want 1 (only design)", len(r.Contributions))
		}
	}
	if len(got) != 3 {
		t.Errorf("want all 3 features ranked, got %d", len(got))
	}
}

// Every result must be reproducible, or "rank 3" is not a claim anyone can check.
func TestTiesBreakDeterministically(t *testing.T) {
	cs := []Criterion{crit(1, "a", DirHigher, 1)}
	rs := map[int64][]Rating{1: {rat(3, 1, 1500, 40, 5), rat(1, 1, 1500, 40, 5), rat(2, 1, 1500, 40, 5)}}
	first := Composite(cs, rs, nil, CompositeOptions{})
	for i := 0; i < 20; i++ {
		again := Composite(cs, rs, nil, CompositeOptions{})
		for j := range first {
			if first[j].FeatureID != again[j].FeatureID {
				t.Fatalf("run %d: order changed at %d: %d vs %d",
					i, j, first[j].FeatureID, again[j].FeatureID)
			}
		}
	}
	if first[0].FeatureID != 1 {
		t.Errorf("tie order = %d first, want 1", first[0].FeatureID)
	}
}

// Deactivating a criterion is how a project retires a dimension. Honouring it
// silently would make the flag a lie.
func TestInactiveExcludedByDefault(t *testing.T) {
	c := crit(1, "retired", DirHigher, 1)
	c.Active = false
	rs := map[int64][]Rating{1: {rat(1, 1, 1800, 30, 9)}}
	if got := Composite([]Criterion{c}, rs, nil, CompositeOptions{}); got != nil {
		t.Errorf("inactive criterion produced %d results, want none", len(got))
	}
	if got := Composite([]Criterion{c}, rs, nil, CompositeOptions{IncludeInactive: true}); len(got) != 1 {
		t.Errorf("IncludeInactive did not include it")
	}
}

// Every zero or negative total is a degenerate input, not a crash.
func TestDegenerateWeights(t *testing.T) {
	got := normaliseWeights([]Weight{{1, 0}, {2, 0}})
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
	for id, w := range got {
		closeTo(t, w, 0.5, 1e-9, "uniform fallback")
		_ = id
	}
	// A negative weight is meaningless for "how much do I care"; it is dropped
	// rather than subtracting importance.
	neg := normaliseWeights([]Weight{{1, -5}, {2, 1}})
	if len(neg) != 1 {
		t.Errorf("negative weight kept: %v", neg)
	}
	closeTo(t, neg[2], 1.0, 1e-9, "survivor normalised to 1")
}

// Confidence must be reported, so a provisional ranking can be told from a
// settled one.
func TestTotalConfidence(t *testing.T) {
	if got := TotalConfidence(nil); got != InitialRD {
		t.Errorf("empty confidence = %v, want %v", got, InitialRD)
	}
	cs := []Contribution{{Weight: 1, RD: 40}, {Weight: 1, RD: 80}}
	closeTo(t, TotalConfidence(cs), 60, 1e-9, "mean RD")
	cs2 := []Contribution{{Weight: 3, RD: 40}, {Weight: 1, RD: 80}}
	closeTo(t, TotalConfidence(cs2), 50, 1e-9, "weighted RD")
}

// The single-criterion path must agree with Composite restricted to one
// criterion, or the common query and the general one would disagree.
func TestRankByCriterionMatchesComposite(t *testing.T) {
	c := crit(1, "design", DirHigher, 1)
	rs := []Rating{rat(1, 1, 1700, 30, 8), rat(2, 1, 1300, 30, 8), rat(3, 1, 1500, 30, 8)}
	direct := RankByCriterion(c, rs)
	viaComposite := Composite([]Criterion{c}, map[int64][]Rating{1: rs}, nil, CompositeOptions{})
	if len(direct) != len(viaComposite) {
		t.Fatalf("lengths differ: %d vs %d", len(direct), len(viaComposite))
	}
	for i := range direct {
		if direct[i].FeatureID != viaComposite[i].FeatureID {
			t.Errorf("position %d: direct=%d composite=%d",
				i, direct[i].FeatureID, viaComposite[i].FeatureID)
		}
		closeTo(t, direct[i].Score, viaComposite[i].Score, 1e-9, "score")
	}
	if direct[0].FeatureID != 1 || direct[0].Rank != 1 {
		t.Errorf("top = %d (rank %d), want feature 1 at rank 1", direct[0].FeatureID, direct[0].Rank)
	}
}

// A vote must move a per-criterion rating, which is the whole premise: the same
// feature can improve on one dimension without touching another.
func TestUpdateRatingMovesOnlyItsOwnCriterion(t *testing.T) {
	design := NewRating(1, 1)
	speed := NewRating(1, 2)
	updated := UpdateRating(design, []Game{{OpponentR: 1200, OpponentRD: 50, Score: 1}}, 0.5)
	if updated.R <= design.R {
		t.Errorf("winning should raise rating: %v -> %v", design.R, updated.R)
	}
	if updated.RD >= design.RD {
		t.Errorf("a rated period should reduce uncertainty: %v -> %v", design.RD, updated.RD)
	}
	if updated.Games != 1 {
		t.Errorf("Games = %d, want 1", updated.Games)
	}
	if speed.R != 1500 || speed.RD != InitialRD {
		t.Errorf("other criterion was disturbed: %+v", speed)
	}
}

// No games must still move RD toward the ceiling, or an idle feature would keep
// claiming precision it no longer has.
func TestUpdateRatingWithNoGamesGrowsRD(t *testing.T) {
	cur := Rating{FeatureID: 1, CriterionID: 1, R: 1600, RD: 40, Sigma: 0.06, Games: 20}
	out := UpdateRating(cur, nil, 0.5)
	if out.RD <= 40 {
		t.Errorf("idle RD should grow from 40, got %v", out.RD)
	}
	if out.R != 1600 {
		t.Errorf("idle rating drifted to %v", out.R)
	}
	if out.Games != 20 {
		t.Errorf("idle period changed game count to %d", out.Games)
	}
}

// Composite must degrade quietly, not panic, on empty input.
func TestCompositeEmpty(t *testing.T) {
	if got := Composite(nil, nil, nil, CompositeOptions{}); got != nil {
		t.Errorf("empty composite = %v, want nil", got)
	}
	if got := RankByCriterion(crit(1, "a", DirHigher, 1), nil); len(got) != 0 {
		t.Errorf("empty rank = %d results", len(got))
	}
	closeTo(t, TotalConfidence(nil), InitialRD, 1e-9, "empty confidence")
}
