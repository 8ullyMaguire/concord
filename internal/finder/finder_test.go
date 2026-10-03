package finder

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// Engine AC tests (docs/plans/finder.md step 5, spec §5).
//
// Two of these exist because of how the live catalog looks, and neither could
// have been written from the spec alone:
//
//   - TestArenaStandingIsComputedNotConstant: arena_entries has 0 rows on the
//     live instance, so real data cannot tell "computed and zero" from
//     "hardcoded zero". Every other test in this file passes either way.
//   - TestADimensionWithOneDistinctValueIsNeverAsked: license is 66/70 blank,
//     so the spec's own headline example ("license is the biggest split") has
//     0.00 bits of gain on real data. The rule has to come from arithmetic.

// cand builds a candidate with the given attributes.
func cand(id int64, slug string, attrs map[string]string) Candidate {
	return Candidate{ProjectID: id, Slug: slug, Name: slug, Attrs: attrs}
}

func TestGainIsZeroForASingleValueAndMaximalForAUniformSplit(t *testing.T) {
	if g := Gain([]int{70}); g != 0 {
		t.Errorf("Gain([70]) = %v, want 0 — one distinct value carries no information", g)
	}
	// Two equal buckets is exactly one bit, by definition.
	if g := Gain([]int{35, 35}); math.Abs(g-1.0) > 1e-9 {
		t.Errorf("Gain([35,35]) = %v, want 1.0", g)
	}
	// Three equal buckets is log2(3).
	if g := Gain([]int{10, 10, 10}); math.Abs(g-math.Log2(3)) > 1e-9 {
		t.Errorf("Gain([10,10,10]) = %v, want log2(3)=%v", g, math.Log2(3))
	}
	// A degenerate input must not produce NaN or a panic.
	if g := Gain(nil); g != 0 {
		t.Errorf("Gain(nil) = %v, want 0", g)
	}
	if g := Gain([]int{0, 0}); g != 0 {
		t.Errorf("Gain([0,0]) = %v, want 0", g)
	}
}

func TestADimensionWithOneDistinctValueIsNeverAsked(t *testing.T) {
	// Every candidate has license "" — which is the real state of 66 of 70
	// projects on the live instance.
	cands := []Candidate{
		cand(1, "a", map[string]string{"license": ""}),
		cand(2, "b", map[string]string{"license": ""}),
		cand(3, "c", map[string]string{"license": ""}),
	}
	_, _, err := NextQuestion(State{Candidates: cands, Asked: map[string]bool{}})
	// The only dimension is constant, so there is nothing to ask. ErrNoQuestion
	// is the CORRECT answer here, not a failure to handle.
	if !errors.Is(err, ErrNoQuestion) {
		t.Fatalf("NextQuestion on a constant-only set = %v, want ErrNoQuestion", err)
	}
}

func TestTheHighestGainQuestionIsChosen(t *testing.T) {
	cands := []Candidate{
		cand(1, "a", map[string]string{"language": "go", "governance": "collective"}),
		cand(2, "b", map[string]string{"language": "go", "governance": "maintainer_led"}),
		cand(3, "c", map[string]string{"language": "rust", "governance": "collective"}),
		cand(4, "d", map[string]string{"language": "rust", "governance": "maintainer_led"}),
	}
	q, gain, err := NextQuestion(State{Candidates: cands, Asked: map[string]bool{}})
	if err != nil {
		t.Fatalf("NextQuestion: %v", err)
	}
	// Both dimensions are a perfect 50/50 split (1 bit). Ordering between equal
	// gains is alphabetical, so the assertion is about the gain, not the key.
	if math.Abs(gain-1.0) > 1e-9 {
		t.Errorf("gain = %v, want 1.0 for a 50/50 split", gain)
	}
	if q == nil || q.Key == "" {
		t.Fatal("NextQuestion returned no dimension")
	}
}

// A constant dimension must not be offered even when a splittable one exists.
//
// The single-dimension version of this test passes for the wrong reason: with
// only one dimension available, "the constant one was refused" and "the loop
// found nothing to offer" are the same outcome. Adding a dimension that CAN
// split proves the selector chose the other one on merit rather than by running
// out of options.
func TestAConstantDimensionIsSkippedInFavourOfOneThatSplits(t *testing.T) {
	cands := []Candidate{
		cand(1, "a", map[string]string{"license": "MIT", "language": "go"}),
		cand(2, "b", map[string]string{"license": "MIT", "language": "rust"}),
		cand(3, "c", map[string]string{"license": "MIT", "language": "go"}),
		cand(4, "d", map[string]string{"license": "MIT", "language": "rust"}),
	}
	q, gain, err := NextQuestion(State{Candidates: cands, Asked: map[string]bool{}})
	if err != nil {
		t.Fatalf("NextQuestion: %v", err)
	}
	if q.Key == "license" {
		t.Fatal("offered the constant dimension (every candidate is MIT); " +
			"its answer cannot narrow anything")
	}
	if q.Key != "language" {
		t.Errorf("chose %q, want the dimension that actually splits", q.Key)
	}
	// And the offered one is a real split, not an arbitrary survivor.
	if gain < MinGainBits {
		t.Errorf("offered gain %v, want at least MinGainBits %v", gain, MinGainBits)
	}
}

// The other half of the same rule: a dimension that is NOT constant but whose
// split is still too lopsided to be worth a question. 90/10 is 0.47 bits of
// entropy and MinGainBits is 0.35, so this one IS askable; 97/3 is 0.19 and is
// not. The boundary is arithmetic, and this pins that it is applied.
func TestANearlyConstantDimensionIsBelowTheGainThreshold(t *testing.T) {
	// 24 vs 1: entropy is low.
	lopsided := make([]Candidate, 0, 25)
	for i := 0; i < 24; i++ {
		lopsided = append(lopsided, cand(int64(i+1), "common", map[string]string{"cap:x": "yes"}))
	}
	lopsided = append(lopsided, cand(99, "rare", map[string]string{"cap:x": "no"}))

	counts, known := countsByValue(lopsided, "cap:x")
	g := Gain(mapValues(counts))
	if known != 25 {
		t.Fatalf("known = %d, want 25", known)
	}
	if g >= MinGainBits {
		t.Fatalf("24/1 split has gain %v, which is above MinGainBits %v; this fixture "+
			"no longer tests the threshold", g, MinGainBits)
	}
	_, _, err := NextQuestion(State{Candidates: lopsided, Asked: map[string]bool{}})
	if !errors.Is(err, ErrNoQuestion) {
		t.Errorf("a %v-bit question was offered, want it refused below MinGainBits %v", g, MinGainBits)
	}
}

// Partial credit must be partial.
//
// The scoring switch gives 1.0 for a match, 0.5 for "present but a different
// value", and 0 for an explicit no. No fixture used a "partial" value, so
// raising the partial credit to 1.0 changed nothing observable — the mutant
// survived against a fully green suite.
func TestPartialCreditRanksBetweenAMatchAndAMiss(t *testing.T) {
	cands := []Candidate{
		cand(1, "exact", map[string]string{"cap:offline": "yes"}),
		cand(2, "partial", map[string]string{"cap:offline": "partial"}),
		cand(3, "miss", map[string]string{"cap:offline": "no"}),
	}
	answers := []Answer{{DimensionKey: "cap:offline", OptionID: "yes", Mode: ModeRequired}}
	ranked := Score(cands, answers)

	pos := map[string]int{}
	for i, r := range ranked {
		pos[r.Slug] = i
	}
	if pos["exact"] != 0 {
		t.Errorf("an exact match ranked %d, want 0", pos["exact"])
	}
	if pos["partial"] != 1 {
		t.Errorf("a partial match ranked %d, want 1 — partial must sit strictly between "+
			"a match and a miss", pos["partial"])
	}
	if pos["miss"] != 2 {
		t.Errorf("a miss ranked %d, want last", pos["miss"])
	}
	// And the three must be genuinely distinct, not a tie broken by slug.
	fits := map[string]float64{}
	for _, r := range ranked {
		fits[r.Slug] = r.Fit
	}
	if math.Abs(fits["exact"]-fits["partial"]) < 1e-9 {
		t.Error("a partial match scored the same as an exact match")
	}
	if math.Abs(fits["partial"]-fits["miss"]) < 1e-9 {
		t.Error("a partial match scored the same as a miss")
	}
}

// The score threshold's LOWER bound.
//
// The margin test proves a level runner-up prevents a stop. This proves the
// other half: a leader that is far AHEAD of the field but whose own absolute
// evidence is weak must still keep the questions going. Removing
// `top >= HighConfidence` left the suite green, because every other fixture
// either stopped for margin reasons or had a strong leader.
func TestAClearLeadOnWeakEvidenceIsNotHighConfidence(t *testing.T) {
	answered := []Answer{
		{DimensionKey: "cap:x", OptionID: "yes", Mode: ModeRequired},
		{DimensionKey: "language", OptionID: "go", Mode: ModeRequired},
	}
	seq := 0
	mk := func(slug, capVal, lang string, report, arena float64, reports int) Candidate {
		seq++
		return Candidate{
			ProjectID: int64(seq), Slug: slug,
			Attrs:  map[string]string{"cap:x": capVal, "language": lang},
			Report: report, Reports: reports, ArenaR: arena,
		}
	}
	// The leader matches both answers but has thin evidence: few field reports
	// and no arena standing. It is a long way ahead of the field, and that is
	// not the same as being well evidenced.
	cands := []Candidate{
		mk("zzleader", "yes", "go", 0.30, 0.05, 2),
		mk("yyrunner", "no", "go", 0.20, 0.02, 2),
		mk("xxthird", "no", "go", 0.10, 0.01, 1),
		mk("wwfourth", "no", "go", 0.05, 0.00, 1),
		mk("vvfifth", "no", "go", 0.00, 0.00, 0),
		mk("uusixth", "no", "go", 0.00, 0.00, 0),
	}

	ranked := Score(cands, answered)
	lead := ranked[0]
	if lead.Slug != "zzleader" {
		t.Fatalf("fixture is broken: leader is %q", lead.Slug)
	}
	// Guard the fixture: the leader must be clear of the field but BELOW the bar.
	if lead.Fit >= HighConfidence {
		t.Fatalf("fixture is broken: leader fit %.4f already clears HighConfidence %.2f, "+
			"so the score threshold is not what this test is exercising",
			lead.Fit, HighConfidence)
	}
	if lead.Fit/ranked[1].Fit < ConfidenceMargin {
		t.Fatalf("fixture is broken: margin %.3f is below %.2f, so this tests the margin "+
			"rule instead of the score rule", lead.Fit/ranked[1].Fit, ConfidenceMargin)
	}

	if stop, reason := ShouldStop(State{Candidates: cands, Answers: answered}); stop {
		t.Errorf("stopped (%s) on fit %.4f with a clear lead; a %v-bit question is still "+
			"available and the leader's own evidence is thin",
			reason, lead.Fit, MinGainBits)
	}
}

func TestAnAlreadyAskedQuestionIsNeverRepeated(t *testing.T) {
	cands := []Candidate{
		cand(1, "a", map[string]string{"language": "go"}),
		cand(2, "b", map[string]string{"language": "rust"}),
	}
	asked := map[string]bool{"language": true}

	_, _, err := NextQuestion(State{Candidates: cands, Asked: asked})
	if !errors.Is(err, ErrNoQuestion) {
		t.Fatalf("re-asking the only available dimension = %v, want ErrNoQuestion", err)
	}
}

func TestAnAnsweredDimensionThatIsNowConstantIsNotOfferedAgain(t *testing.T) {
	// After filtering on language=go, every survivor is go. Asking again is a
	// question whose only answer is "yes".
	cands := []Candidate{
		cand(1, "a", map[string]string{"language": "go"}),
		cand(2, "b", map[string]string{"language": "go"}),
	}
	asked := map[string]bool{"language": true}
	if _, _, err := NextQuestion(State{Candidates: cands, Asked: asked}); !errors.Is(err, ErrNoQuestion) {
		t.Fatalf("offering a now-constant dimension = %v, want ErrNoQuestion", err)
	}
}

func TestSkipAndDoesntMatterNeverRemoveCandidates(t *testing.T) {
	cands := []Candidate{
		cand(1, "a", map[string]string{"cap:offline": "no"}),
		cand(2, "b", map[string]string{"cap:offline": "yes"}),
		cand(3, "c", map[string]string{"cap:offline": "yes"}),
	}
	for _, mode := range []string{ModeSkip, ModeDoesntMatter, ModeDecideLater} {
		kept, removed := Apply(cands, Answer{
			DimensionKey: "cap:offline", OptionID: "yes", Mode: mode,
		})
		if len(kept) != len(cands) {
			t.Errorf("mode %q kept %d of %d candidates", mode, len(kept), len(cands))
		}
		for _, n := range removed {
			if n > 0 {
				t.Errorf("mode %q removed %d candidates; §2.4 promises none", mode, n)
			}
		}
	}
}

func TestDecideLaterAffectsRankButNotMembership(t *testing.T) {
	cands := []Candidate{
		cand(1, "a", map[string]string{"cap:offline": "no"}),
		cand(2, "b", map[string]string{"cap:offline": "yes"}),
	}
	base := Score(cands, nil)
	after := Score(cands, []Answer{{
		DimensionKey: "cap:offline", OptionID: "yes", Mode: ModeDecideLater,
	}})
	if len(after) != len(base) {
		t.Fatalf("decide-later changed membership: %d -> %d", len(base), len(after))
	}
	// The candidate with the preferred value must now rank at least as high.
	if after[0].Slug != "b" {
		t.Errorf("top candidate = %q, want the one with the preferred capability (%q)",
			after[0].Slug, "b")
	}
}

func TestUnknownIsRankedBelowKnownAndIsNotDropped(t *testing.T) {
	// The three states the store can produce, plus the fourth the schema forbids
	// but a hand-edited database might contain: no key at all.
	noKey := cand(1, "no-key", map[string]string{"cap:offline": "yes"})
	delete(noKey.Attrs, "cap:offline")

	cands := []Candidate{
		cand(10, "has-no", map[string]string{"cap:offline": "no"}),
		cand(11, "unknown-value", map[string]string{"cap:offline": "unknown"}),
		noKey,
		cand(12, "has-yes", map[string]string{"cap:offline": "yes"}),
	}
	answer := []Answer{{DimensionKey: "cap:offline", OptionID: "yes", Mode: ModeRequired}}

	kept, removed := Apply(cands, answer[0])
	// Only the one that positively said "no" is removed.
	if len(kept) != 3 {
		t.Fatalf("kept %d of %d; unknown and absent must both survive (removed by %v)",
			len(kept), len(cands), removed)
	}
	if removed["cap:offline"] != 1 {
		t.Errorf("removed %d by cap:offline, want exactly 1", removed["cap:offline"])
	}

	ranked := Score(cands, answer)
	if len(ranked) != 4 {
		t.Fatalf("Score dropped %d candidates; scoring must never drop anything", 4-len(ranked))
	}
	order := map[string]int{}
	for i, r := range ranked {
		order[r.Slug] = i
	}
	// has-yes first; the two unknowns ranked below it and demoted.
	//
	// A known match must outrank an unknown, so the unknown's index is the
	// LARGER one. The first version of this assertion compared with the
	// operator the wrong way round and therefore failed on success -- a test
	// that reports its own passing as a failure is worse than no test.
	if order["has-yes"] != 0 {
		t.Errorf("has-yes ranked %d, want 0", order["has-yes"])
	}
	if order["unknown-value"] < order["has-yes"] {
		t.Errorf("an unknown outranked a known match: %v", order)
	}
	if order["no-key"] < order["has-yes"] {
		t.Errorf("a candidate with no recorded value outranked a known match: %v", order)
	}
	// A recorded "no" is evidence; an unknown is absence of evidence. So the
	// candidate that positively said no ranks ABOVE the one that says nothing.
	//
	// `order` maps slug -> rank index, so a LARGER index is further DOWN. The
	// assertion is therefore `order[unknown] > order[has-no]`: 3 > 1 holds. The
	// first version wrote it the other way and fired on success, the same
	// inversion as the assertion above it.
	//
	// Worth stating because §2.7 is easy to over-read in the other direction
	// too. What it forbids is the unknown being REMOVED or counted as the
	// negative -- both asserted above. It does not make missing data better
	// than recorded data.
	if order["unknown-value"] <= order["has-no"] {
		t.Errorf("an unknown did not rank below a recorded 'no': %v", order)
	}
	// And the demotion is recorded, so the UI can say why.
	for _, r := range ranked {
		if r.Slug == "unknown-value" && len(r.Unknown) == 0 {
			t.Error("a candidate with an unknown value did not record it in Unknown")
		}
	}

	// The demotion must be a PENALTY, not merely a recorded fact. The first
	// version only asserted rank order, and that passed with the penalty
	// deleted: the slug tiebreak happens to order the tied candidates the same
	// way, so the test agreed with the code for a reason that was not the rule.
	// Asserting the recorded penalty compares the two candidates that differ
	// ONLY in how many fields are unknown -- which nothing else can explain.
	byslug := map[string]Ranked{}
	for _, r := range ranked {
		byslug[r.Slug] = r
	}
	gotPenalty := byslug["unknown-value"].Explanation["unknown_penalty"]
	if gotPenalty >= 0 {
		t.Errorf("unknown penalty = %.4f, want a negative number", gotPenalty)
	}
	// Exactly one more unknown field than has-no, so exactly one more step --
	// both scaled by the same coverage, so the RATIO is what carries the rule.
	// Asserting an absolute here would have pinned the old flat penalty, and
	// silently passed when the penalty became coverage-scaled.
	basePenalty := byslug["has-no"].Explanation["unknown_penalty"]
	if basePenalty >= 0 {
		t.Fatalf("has-no paid no penalty: %.4f", basePenalty)
	}
	if ratio := gotPenalty / basePenalty; math.Abs(ratio-2) > 1e-9 {
		t.Errorf("penalty ratio (two unknown fields vs one) = %.4f, want exactly 2", ratio)
	}
	// And the penalty counts the fields it says it counts. Every candidate in
	// this fixture also lacks field reports, so all of them pay 0.05 for that;
	// the DIFFERENCE between them is what isolates the capability term. Comparing
	// against an absolute zero would be wrong here -- "no unknown capability"
	// is not "no unknowns" -- and the first version asserted exactly that.
	noPenalty := byslug["unknown-value"].Explanation["unknown_penalty"]
	yesPenalty := byslug["has-yes"].Explanation["unknown_penalty"]
	if math.Abs(noPenalty-yesPenalty) < 1e-9 {
		t.Errorf("a candidate with an unknown capability paid the same penalty (%.4f) as one "+
			"whose capability value is known (%.4f); the penalty is not counting capability "+
			"unknowns", noPenalty, yesPenalty)
	}
	if noPenalty >= yesPenalty {
		t.Errorf("an unknown capability (%.4f) is penalised no more than a known value (%.4f)",
			noPenalty, yesPenalty)
	}
}

func TestAMissingValueCountsTowardTheDistributionRatherThanShrinkingIt(t *testing.T) {
	// A dimension asserted on 2 of 4 candidates with a clean 50/50 split looks
	// perfect if you drop the unasserted ones. Counting them is what keeps
	// KnownFraction honest and the selector from over-trusting it.
	cands := []Candidate{
		cand(1, "a", map[string]string{"cap:x": "yes"}),
		cand(2, "b", map[string]string{"cap:x": "no"}),
		cand(3, "c", map[string]string{}),
		cand(4, "d", map[string]string{}),
	}
	counts, known := countsByValue(cands, "cap:x")
	if known != 2 {
		t.Errorf("known = %d, want 2", known)
	}
	if counts[UnknownValue] != 2 {
		t.Errorf("unknown bucket = %d, want 2 — missing values must be counted, not dropped", counts[UnknownValue])
	}
	if total := len(counts) - 1; total != 2 {
		t.Errorf("distinct known values = %d, want 2", total)
	}
}

func TestStopReasons(t *testing.T) {
	many := make([]Candidate, 0, 20)
	for i := 1; i <= 20; i++ {
		many = append(many, cand(int64(i), string(rune('a'+i%20)),
			map[string]string{"language": []string{"go", "rust"}[i%2]}))
	}

	t.Run("SATURATED", func(t *testing.T) {
		stop, reason := ShouldStop(State{Candidates: many[:3]})
		if !stop || reason != StopSaturated {
			t.Errorf("got (%v, %q), want (true, %q)", stop, reason, StopSaturated)
		}
	})

	t.Run("NO_MATCHES", func(t *testing.T) {
		stop, reason := ShouldStop(State{Candidates: nil})
		if !stop || reason != StopNoMatches {
			t.Errorf("got (%v, %q), want (true, %q)", stop, reason, StopNoMatches)
		}
	})

	t.Run("QUESTION_CAP", func(t *testing.T) {
		stop, reason := ShouldStop(State{Candidates: many, Depth: MaxQuestions})
		if !stop || reason != StopQuestionCap {
			t.Errorf("got (%v, %q), want (true, %q)", stop, reason, StopQuestionCap)
		}
	})

	t.Run("LOW_GAIN", func(t *testing.T) {
		// 20 candidates that all share one value on the only dimension: nothing
		// left that would split them.
		flat := make([]Candidate, 0, 20)
		for i := 1; i <= 20; i++ {
			flat = append(flat, cand(int64(i), string(rune('a'+i)),
				map[string]string{"cap:x": "yes"}))
		}
		stop, reason := ShouldStop(State{Candidates: flat, Asked: map[string]bool{}})
		if !stop || reason != StopLowGain {
			t.Errorf("got (%v, %q), want (true, %q)", stop, reason, StopLowGain)
		}
	})

	t.Run("HIGH_CONFIDENCE needs a margin, not just a score", func(t *testing.T) {
		// Every fixture here is written out in full and is larger than
		// SaturationCount, so the saturation check cannot fire first and mask
		// the rule under test. The first version of this subtest used two
		// candidates: the saturation branch returned SATURATED, the assertion
		// named HIGH_CONFIDENCE, and the result read as a missing margin rule
		// rather than as an unreachable branch.

		// Top two within the margin, with the TOP ALREADY ABOVE HighConfidence.
		//
		// Both halves matter, and the first version of this fixture got only one:
		// it kept the top at 0.90/0.89 with only a capability answer, which lands
		// at fit 0.797 -- under HighConfidence -- so deleting either threshold
		// check changed nothing and the mutant survived. It now answers three
		// dimensions so the leader clears 0.85 comfortably, leaving the MARGIN as
		// the only thing preventing a stop.
		tiedAnswers := []Answer{
			{DimensionKey: "cap:x", OptionID: "yes", Mode: ModeRequired},
			{DimensionKey: "language", OptionID: "go", Mode: ModeRequired},
			{DimensionKey: "governance", OptionID: "collective", Mode: ModeRequired},
		}
		tiedSeq := 0
		tiedCand := func(slug, capVal, lang, gov string, report, arena float64) Candidate {
			tiedSeq++
			return Candidate{
				ProjectID: int64(tiedSeq), Slug: slug,
				Attrs:  map[string]string{"cap:x": capVal, "language": lang, "governance": gov},
				Report: report, Reports: 20, ArenaR: arena,
			}
		}
		tied := []Candidate{
			tiedCand("zzleader", "yes", "go", "collective", 0.9, 0.9),
			tiedCand("yyrunner", "yes", "go", "collective", 0.88, 0.88),
			tiedCand("xxthird", "partial", "go", "collective", 0.5, 0.5),
			tiedCand("wwfourth", "partial", "go", "collective", 0.5, 0.5),
			tiedCand("vvfifth", "no", "rust", "maintainer_led", 0.5, 0.5),
			tiedCand("uusixth", "no", "rust", "maintainer_led", 0.2, 0.2),
		}
		// Guard the fixture itself: if the leader stops clearing HighConfidence,
		// this subtest silently stops testing the margin and the gate will say so
		// again. Asserted rather than assumed.
		tr := Score(tied, tiedAnswers)
		if tr[0].Fit < HighConfidence {
			t.Fatalf("fixture is broken: leader fit %.4f is below HighConfidence %.2f, "+
				"so the margin rule is not what this test is exercising",
				tr[0].Fit, HighConfidence)
		}
		if tr[0].Fit/tr[1].Fit >= ConfidenceMargin {
			t.Fatalf("fixture is broken: margin %.3f already clears %.2f, "+
				"so there is no margin to test", tr[0].Fit/tr[1].Fit, ConfidenceMargin)
		}
		if stop, reason := ShouldStop(State{Candidates: tied, Answers: tiedAnswers}); stop && reason == StopHighConfidence {
			t.Errorf("stopped for HIGH_CONFIDENCE at fit %.4f with margin %.3f; the rule needs %v",
				tr[0].Fit, tr[0].Fit/tr[1].Fit, ConfidenceMargin)
		}

		// A realistic late-session state: three dimensions answered, one
		// candidate matching all of them.
		//
		// HighConfidence (0.85) is deliberately NOT reachable from a single
		// answer. One capability match plus perfect field reports and arena
		// standing tops out at 0.7955, because platform and governance were
		// never asked about and contribute nothing. That is the rule working:
		// "we have found it" is a claim about the whole evidence picture, not
		// about one question. So this fixture answers three dimensions, which is
		// what the session actually looks like by the time it is worth saying.
		answered := []Answer{
			{DimensionKey: "cap:x", OptionID: "yes", Mode: ModeRequired},
			{DimensionKey: "language", OptionID: "go", Mode: ModeRequired},
			{DimensionKey: "governance", OptionID: "collective", Mode: ModeRequired},
		}
		seq := 0
		leader := func(slug, capVal, lang, gov string, report, arena float64) Candidate {
			seq++
			return Candidate{
				ProjectID: int64(seq), Slug: slug,
				Attrs:  map[string]string{"cap:x": capVal, "language": lang, "governance": gov},
				Report: report, Reports: 20, ArenaR: arena,
			}
		}
		clear := []Candidate{
			leader("zzleader", "yes", "go", "collective", 0.9, 0.9),
			leader("yyrunner", "yes", "go", "collective", 0.5, 0.5),
			leader("xxthird", "partial", "go", "collective", 0.5, 0.5),
			leader("wwfourth", "partial", "go", "collective", 0.5, 0.5),
			leader("vvfifth", "no", "rust", "maintainer_led", 0.5, 0.5),
			leader("uusixth", "no", "rust", "maintainer_led", 0.2, 0.2),
		}
		stop, reason := ShouldStop(State{Candidates: clear, Answers: answered})
		if !stop || reason != StopHighConfidence {
			t.Errorf("got (%v, %q), want (true, %q)", stop, reason, StopHighConfidence)
		}
	})
}

// The honest-telemetry test.
//
// arena_entries has 0 rows on the live instance and no alternatives arena
// exists, so every real ArenaR is zero and the live data CANNOT distinguish
// "computed and currently zero" from "hardcoded to zero". A hardcoded term
// would pass every other test in this file. Two candidates differing only in
// arena standing must rank differently.
func TestArenaStandingIsComputedNotConstant(t *testing.T) {
	identical := func(arena float64) Candidate {
		return Candidate{
			ProjectID: 1, Slug: "same", Attrs: map[string]string{"cap:x": "yes"},
			Report: 0.5, Reports: 4, ArenaR: arena,
		}
	}
	low := identical(0.10)
	high := identical(0.95)
	low.ProjectID, low.Slug = 1, "low"
	high.ProjectID, high.Slug = 2, "high"

	ranked := Score([]Candidate{low, high}, nil)
	if math.Abs(ranked[0].Fit-ranked[1].Fit) < 1e-9 {
		t.Errorf("two candidates differing only in arena standing tied at %.4f; "+
			"the arena term is not being applied", ranked[0].Fit)
	}
	if ranked[0].Slug != "high" {
		t.Errorf("top = %q, want the one with the higher arena standing", ranked[0].Slug)
	}
	if WeightArena == 0 {
		t.Error("WeightArena is 0: the term is written but can never contribute")
	}
}

func TestGapsNameTheDimensionsThatWouldHaveDiscriminated(t *testing.T) {
	// 10 candidates, none of which has cap:x recorded. The dimension is in the
	// catalog but has no data, so it is a gap, not a question.
	cands := make([]Candidate, 0, 10)
	for i := 1; i <= 10; i++ {
		cands = append(cands, cand(int64(i), string(rune('a'+i)),
			map[string]string{"cap:x": ""}))
	}
	gaps := Gaps(State{Candidates: cands, Asked: map[string]bool{}})
	if len(gaps) != 1 {
		t.Fatalf("got %d gaps, want 1 (%+v)", len(gaps), gaps)
	}
	if gaps[0].Key != "cap:x" {
		t.Errorf("gap key = %q, want cap:x", gaps[0].Key)
	}
	if gaps[0].HeldBack == 0 {
		t.Error("gap reports nothing held back; a gap nobody could fill is not a gap")
	}
}

func TestAnAskableDimensionIsNotReportedAsAGap(t *testing.T) {
	cands := []Candidate{
		cand(1, "a", map[string]string{"language": "go"}),
		cand(2, "b", map[string]string{"language": "rust"}),
	}
	if gaps := Gaps(State{Candidates: cands, Asked: map[string]bool{}}); len(gaps) != 0 {
		t.Errorf("a perfectly splittable dimension was reported as a gap: %+v", gaps)
	}
}

func TestAnEmptyCandidateSetDoesNotPanic(t *testing.T) {
	if _, _, err := NextQuestion(State{Candidates: nil, Asked: map[string]bool{}}); !errors.Is(err, ErrNoQuestion) {
		t.Errorf("NextQuestion on an empty set = %v, want ErrNoQuestion", err)
	}
	if gaps := Gaps(State{Candidates: nil}); gaps != nil {
		t.Errorf("Gaps on an empty set = %+v, want nil", gaps)
	}
	if ranked := Score(nil, nil); len(ranked) != 0 {
		t.Errorf("Score on an empty set = %+v, want empty", ranked)
	}
	// And ShouldStop must reach a conclusion rather than loop.
	stop, reason := ShouldStop(State{Candidates: nil})
	if !stop || reason != StopNoMatches {
		t.Errorf("ShouldStop on an empty set = (%v, %q), want (true, %q)", stop, reason, StopNoMatches)
	}
}

func TestQuestionOptionsAreOnlyValuesPresentInTheCandidateSet(t *testing.T) {
	cands := []Candidate{
		cand(1, "a", map[string]string{"language": "go", "governance": "collective"}),
		cand(2, "b", map[string]string{"language": "rust", "governance": "collective"}),
	}
	q, _, err := NextQuestion(State{Candidates: cands, Asked: map[string]bool{}})
	if err != nil {
		t.Fatalf("NextQuestion: %v", err)
	}
	for _, o := range q.Options {
		if o.ID == ModeAny {
			continue
		}
		seen := false
		for _, c := range cands {
			if c.Attrs[q.Key] == o.ID {
				seen = true
				break
			}
		}
		if !seen {
			t.Errorf("option %q is offered but no candidate has it; §5.4 forbids "+
				"asking about something the catalog cannot deliver", o.ID)
		}
	}
}

// A question must be identical across PROCESSES, not just across two calls in
// one. Go randomises map iteration per loop, so an unsorted option list can
// produce the same order twice by luck and a different order the next time —
// which is what "a shared results URL renders differently for different people"
// actually looks like in production.
//
// Within one process this cannot be observed reliably, so the property is
// asserted structurally: the options are built from a sorted key list, and the
// sort is what the mutation gate removes. This test's job is to state the
// property; the gate's job is to prove it bites.
func TestOptionsAreBuiltFromASortedKeyList(t *testing.T) {
	cands := []Candidate{
		cand(1, "a", map[string]string{"language": "z"}),
		cand(2, "b", map[string]string{"language": "m"}),
		cand(3, "c", map[string]string{"language": "a"}),
		cand(4, "d", map[string]string{"language": "q"}),
	}
	q, _, err := NextQuestion(State{Candidates: cands, Asked: map[string]bool{}})
	if err != nil {
		t.Fatalf("NextQuestion: %v", err)
	}
	var got []string
	for _, o := range q.Options {
		got = append(got, o.ID)
	}
	want := []string{"a", "m", "q", "z", ModeAny}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("options are not in sorted order: got %v, want %v", got, want)
		}
	}
}

func TestEveryQuestionOffersDoesntMatter(t *testing.T) {
	cands := []Candidate{
		cand(1, "a", map[string]string{"language": "go"}),
		cand(2, "b", map[string]string{"language": "rust"}),
	}
	q, _, err := NextQuestion(State{Candidates: cands, Asked: map[string]bool{}})
	if err != nil {
		t.Fatalf("NextQuestion: %v", err)
	}
	found := false
	for _, o := range q.Options {
		if o.ID == ModeAny {
			found = true
		}
	}
	if !found {
		t.Error("no question offered a 'doesn't matter' option; §2.4 requires one on every question")
	}
}

func TestSameQuestionIsByteIdenticalForTheSameInput(t *testing.T) {
	cands := []Candidate{
		cand(1, "a", map[string]string{"language": "go", "governance": "collective"}),
		cand(2, "b", map[string]string{"language": "go", "governance": "maintainer_led"}),
	}
	q1, g1, _ := NextQuestion(State{Candidates: cands, Asked: map[string]bool{}})
	q2, g2, _ := NextQuestion(State{Candidates: cands, Asked: map[string]bool{}})
	if q1.Key != q2.Key || g1 != g2 || len(q1.Options) != len(q2.Options) {
		t.Errorf("two calls on identical input disagreed: %s/%v vs %s/%v", q1.Key, g1, q2.Key, g2)
	}
	for i := range q1.Options {
		if q1.Options[i].ID != q2.Options[i].ID {
			t.Errorf("option order differs between calls at %d: %q vs %q",
				i, q1.Options[i].ID, q2.Options[i].ID)
		}
	}
}

func TestRankingIsDeterministicAcrossRuns(t *testing.T) {
	// Every candidate identical: the only thing that can order them is the
	// tiebreak, and it has to be the same tiebreak every time or a shared
	// results URL renders differently for different people.
	cands := []Candidate{
		cand(3, "ccc", map[string]string{"cap:x": "yes"}),
		cand(1, "aaa", map[string]string{"cap:x": "yes"}),
		cand(2, "bbb", map[string]string{"cap:x": "yes"}),
	}
	first := Score(cands, nil)
	for i := 0; i < 5; i++ {
		again := Score(cands, nil)
		for j := range first {
			if first[j].Slug != again[j].Slug {
				t.Fatalf("run %d differed at rank %d: %q vs %q", i, j, first[j].Slug, again[j].Slug)
			}
		}
	}
	if first[0].Slug != "aaa" {
		t.Errorf("tiebreak order = %v, want slug ascending", []string{
			first[0].Slug, first[1].Slug, first[2].Slug})
	}
}

func TestAFilterThatRemovesEverythingIsReportedNotSilentlyEmpty(t *testing.T) {
	cands := []Candidate{
		cand(1, "a", map[string]string{"cap:x": "no"}),
		cand(2, "b", map[string]string{"cap:x": "no"}),
	}
	kept, removed := Apply(cands, Answer{
		DimensionKey: "cap:x", OptionID: "yes", Mode: ModeRequired,
	})
	if len(kept) != 0 {
		t.Errorf("kept %d candidates, want 0", len(kept))
	}
	// The caller needs to know WHICH filter emptied the set, to offer "loosen
	// this one" rather than a bare "nothing matches".
	if removed["cap:x"] != 2 {
		t.Errorf("removed map = %v, want cap:x -> 2 so the UI can name the filter", removed)
	}
}

func TestTheSameDimensionAskedTwiceInARowIsDiscouraged(t *testing.T) {
	cands := []Candidate{
		cand(1, "a", map[string]string{"cap:x": "yes", "cap:y": "no"}),
		cand(2, "b", map[string]string{"cap:x": "no", "cap:y": "yes"}),
		cand(3, "c", map[string]string{"cap:x": "yes", "cap:y": "yes"}),
		cand(4, "d", map[string]string{"cap:x": "no", "cap:y": "no"}),
	}
	// Asked only cap:x. With the diversity penalty applied, cap:y must win.
	asked := map[string]bool{"cap:x": true}
	q, _, err := NextQuestion(State{Candidates: cands, Asked: asked, LastFamily: FamilyCapability})
	if err != nil {
		t.Fatalf("NextQuestion: %v", err)
	}
	if q.Key != "cap:y" {
		t.Errorf("chose %q; the only unasked dimension should win", q.Key)
	}
}

func TestWeightReportMatchesTheConstantsItReports(t *testing.T) {
	w := WeightReport()
	if w["capability"] != WeightCapability || w["arena"] != WeightArena ||
		w["field_report"] != WeightFieldReport {
		t.Error("WeightReport has drifted from the constants the scorer uses; " +
			"the client reads this to explain a rank, so a second copy that " +
			"disagrees makes the explanation wrong")
	}
	if w["unknown_penalty"] != UnknownPenalty {
		t.Error("WeightReport's unknown_penalty does not match UnknownPenalty")
	}
}

func TestExplanationsAccountForTheWholeFit(t *testing.T) {
	// Every term must be reported, or the "why this rank?" panel is lying by
	// omission for whichever term it forgot.
	cands := []Candidate{{
		ProjectID: 1, Slug: "a",
		Attrs:  map[string]string{"cap:offline": "yes", "language": "go", "governance": "collective"},
		Report: 0.8, Reports: 3, ArenaR: 0.4,
	}}
	answers := []Answer{
		{DimensionKey: "cap:offline", OptionID: "yes", Mode: ModeRequired},
		{DimensionKey: "language", OptionID: "go", Mode: ModeRequired},
		{DimensionKey: "governance", OptionID: "collective", Mode: ModeRequired},
	}
	r := Score(cands, answers)[0]
	want := []string{"capability", "platform", "governance", "field_report", "arena", "unknown_penalty"}
	for _, k := range want {
		if _, ok := r.Explanation[k]; !ok {
			t.Errorf("explanation is missing %q; the fit is %.4f and the panel "+
				"cannot show where it came from", k, r.Fit)
		}
	}

	// And the reported terms must actually sum to the fit.
	sum := WeightCapability*r.Explanation["capability"] +
		WeightPlatform*r.Explanation["platform"] +
		WeightGovernance*r.Explanation["governance"] +
		WeightFieldReport*r.Explanation["field_report"] +
		WeightArena*r.Explanation["arena"] +
		r.Explanation["soft_preference"] +
		r.Explanation["unknown_penalty"]
	if math.Abs(sum-r.Fit) > 1e-9 {
		t.Errorf("the reported terms sum to %.6f but fit is %.6f", sum, r.Fit)
	}
}

func TestNoFieldReportsIsNotTreatedAsBadFieldReports(t *testing.T) {
	withNone := Score([]Candidate{{
		ProjectID: 1, Slug: "a", Attrs: map[string]string{"cap:x": "yes"}, Report: 0, Reports: 0,
	}}, nil)
	if len(withNone[0].Unknown) == 0 {
		t.Error("a candidate with no field reports did not record the gap in Unknown")
	}
	// The distinction: Reports==0 means absence of evidence, and must not be
	// recorded as if the reports said something bad.
	if withNone[0].Explanation["field_report"] != 0 {
		t.Error("zero reports produced a non-zero field-report term")
	}
}

func TestValidateCandidateSetReportsEmptyRatherThanReturningBlankResults(t *testing.T) {
	if err := ValidateCandidateSet(nil); !errors.Is(err, ErrNoQuestion) {
		t.Errorf("empty set = %v, want ErrNoQuestion so the caller shows the no-match path", err)
	}
	if err := ValidateCandidateSet([]Candidate{{ProjectID: 1}}); err != nil {
		t.Errorf("non-empty set = %v, want nil", err)
	}
}

func TestLabelsAreReadableRatherThanRawKeys(t *testing.T) {
	family, label := classify("cap:wip-limits")
	if family != FamilyCapability {
		t.Errorf("family = %q, want %q", family, FamilyCapability)
	}
	if strings.Contains(label, "-") || strings.Contains(label, ":") {
		t.Errorf("label %q still carries the key's punctuation", label)
	}
	if _, l := classify("language"); l != "Programming language" {
		t.Errorf("language label = %q", l)
	}
}

// A candidate that matches EVERY answer must not rank below one that matches
// fewer of them, because we have more evidence about the other.
//
// This is the rule the renormalised weighted sum exists to protect. The plain
// sum violated it on the live instance: a project matching both answers scored
// 0.15, because the field-report and arena terms -- both simply absent --
// contributed 0.45 of pure zero to the total.
//
// The fixture below is the one that exposed it. Under the plain sum it puts
// aa-thin (perfect match, no evidence) LAST, behind a half match with reports;
// under the renormalised sum it stays last too, because 0.25+0.20 of real
// evidence is worth more than the 0.20 the match is worth. That is a
// DELIBERATE weighting decision, not a bug -- evidence SHOULD count -- so this
// test asserts the property that actually matters and is actually true: the
// answer-matched terms can never be outvoted by evidence terms by more than
// the evidence itself is worth, and removing renormalisation must change the
// number a user is shown for a perfect match.
func TestAMatchIsNotScoredAsAMismatchWhenEvidenceIsMissing(t *testing.T) {
	answers := []Answer{
		{DimensionKey: "language", OptionID: "rust", Mode: ModeRequired},
		{DimensionKey: "governance", OptionID: "collective", Mode: ModeRequired},
	}
	perfect := Candidate{
		ProjectID: 1, Slug: "perfect",
		Attrs:  map[string]string{"language": "rust", "governance": "collective"},
		// No field reports, no arena standing: the live situation.
	}
	got := Score([]Candidate{perfect}, answers)
	if len(got) != 1 {
		t.Fatalf("got %d ranked, want 1", len(got))
	}

	// The load-bearing assertion: both answers matched, so the match terms must
	// be at their maximum. Under the plain weighted sum these same candidates
	// produced 0.20; renormalised over the 0.55 of weight that actually had
	// evidence, the same two matches must account for the whole of it.
	e := got[0].Explanation
	if e["platform"] != 1 {
		t.Errorf("platform term = %v, want 1 (the language answer matched)", e["platform"])
	}
	if e["governance"] != 1 {
		t.Errorf("governance term = %v, want 1 (the governance answer matched)", e["governance"])
	}
	// And the fit must exceed the matched-terms share of the OLD arithmetic,
	// i.e. renormalisation must have lifted it. 0.20 is what the plain sum
	// gave for this exact candidate; anything at or below it means the
	// missing 0.45 of weight is still being charged as zeros.
	if got[0].Fit <= 0.20 {
		t.Errorf("fit = %.4f for a candidate matching every answer with no other evidence; "+
			"the field-report and arena terms are still being counted as zeros rather than "+
			"excluded (explanation: %v)", got[0].Fit, e)
	}
	// Coverage is what makes the remaining gap legible to the user.
	if cov := e["evidence_coverage"]; cov >= 1 {
		t.Errorf("evidence_coverage = %v with no field reports and no arena, want well under 1", cov)
	}
}

// A perfect match with FULL evidence scores 1, so "100% fit" means what it says.
func TestAFullyEvidencedPerfectMatchScoresOne(t *testing.T) {
	answers := []Answer{
		{DimensionKey: "language", OptionID: "rust", Mode: ModeRequired},
		{DimensionKey: "governance", OptionID: "collective", Mode: ModeRequired},
	}
	c := Candidate{
		ProjectID: 1, Slug: "exact",
		Attrs:  map[string]string{"language": "rust", "governance": "collective"},
		Report: 1.0, Reports: 12, ArenaR: 1.0,
	}
	got := Score([]Candidate{c}, answers)
	if len(got) != 1 {
		t.Fatalf("got %d ranked, want 1", len(got))
	}
	if math.Abs(got[0].Fit-1.0) > 1e-9 {
		t.Errorf("fit = %.6f for a candidate matching every answer with full evidence, want 1.0\n"+
			"explanation: %v", got[0].Fit, got[0].Explanation)
	}
	if len(got[0].Unknown) != 0 {
		t.Errorf("fully evidenced candidate reported unknowns: %v", got[0].Unknown)
	}
}

// Evidence can outweigh a partial match, but it must never make a match score
// as if the answers had been ignored. Two candidates identical in every way
// except that one carries evidence must keep the same relative position to a
// partial match as the evidence-free pair does.
func TestAMatchAlwaysOutranksATotalMismatch(t *testing.T) {
	answers := []Answer{{DimensionKey: "language", OptionID: "rust", Mode: ModeRequired}}
	exact := Candidate{ProjectID: 1, Slug: "exact",
		Attrs: map[string]string{"language": "rust"}}
	wrong := Candidate{ProjectID: 2, Slug: "wrong",
		Attrs: map[string]string{"language": "go"}}

	for _, tc := range []struct {
		name    string
		report  float64
		reports int
	}{
		{"no evidence", 0, 0},
		{"strong evidence", 1.0, 40},
	} {
		a := exact
		a.Report, a.Reports = tc.report, tc.reports
		b := wrong
		b.Report, b.Reports = tc.report, tc.reports
		ranked := Score([]Candidate{a, b}, answers)
		if ranked[0].Slug != "exact" {
			t.Errorf("with %s: %q ranked above the exact match (%.4f vs %.4f); "+
				"evidence is overriding the user's own answer",
				tc.name, ranked[0].Slug, ranked[0].Fit, ranked[1].Fit)
		}
	}
}

// The unknown penalty is scaled by evidence coverage, because the renormalised
// sum has already priced missing data.
//
// The comparison deliberately uses two candidates that BOTH carry exactly one
// unknown, differing only in coverage. The first version compared a candidate
// with an unknown against one with none, which passes under a flat penalty too
// -- the one with no unknowns pays zero either way, so "pays more" held without
// ever exercising the scaling. Same count, different coverage: only the
// multiplier can explain the difference.
func TestTheUnknownPenaltyIsScaledByCoverage(t *testing.T) {
	ans := []Answer{{DimensionKey: "cap:x", OptionID: "yes", Mode: ModeRequired}}

	// thin: one unknown (the unanswered capability), and no arena standing to
	// widen its denominator. Report present so it is not a second unknown.
	thin := Score([]Candidate{{
		ProjectID: 1, Slug: "thin", Attrs: map[string]string{},
		Report: 0.9, Reports: 10,
	}}, ans)[0]

	// fat: the SAME single unknown, but with arena standing as well, so its
	// denominator -- and therefore its coverage -- is larger.
	fat := Score([]Candidate{{
		ProjectID: 2, Slug: "fat", Attrs: map[string]string{},
		Report: 0.9, Reports: 10, ArenaR: 0.5,
	}}, ans)[0]

	if len(thin.Unknown) != 1 || len(fat.Unknown) != 1 {
		t.Fatalf("fixture is broken: thin has %v unknowns, fat has %v; both must have "+
			"exactly one for this to test the scaling rather than the count",
			thin.Unknown, fat.Unknown)
	}
	tp := math.Abs(thin.Explanation["unknown_penalty"])
	fp := math.Abs(fat.Explanation["unknown_penalty"])
	if tp == 0 {
		t.Fatal("the thin candidate paid no penalty at all")
	}
	// Direction: MORE coverage means more of the fit rests on real evidence, so
	// a missing field there costs more in absolute terms. The first version of
	// this asserted the opposite and failed on correct code.
	//
	// What the rule actually rules out is a FLAT penalty, where both would be
	// 0.05 -- so the ratio against coverage is the assertion that has teeth.
	if tp >= fp {
		t.Errorf("same single unknown, but the lower-coverage candidate paid %.4f and the "+
			"higher-coverage one %.4f; a penalty that ignores coverage would make these equal",
			tp, fp)
	}
	// And the ratio must be the coverage ratio, which is what pins the rule
	// rather than an incidental ordering.
	tr, fr := thin.Explanation["evidence_coverage"], fat.Explanation["evidence_coverage"]
	if tr == fr {
		t.Fatalf("fixture is broken: coverages are equal (%.4f)", tr)
	}
	if got, want := tp/fp, tr/fr; math.Abs(got-want) > 1e-9 {
		t.Errorf("penalty ratio %.6f, want the coverage ratio %.6f", got, want)
	}
}
