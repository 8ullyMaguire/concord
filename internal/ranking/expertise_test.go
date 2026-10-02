package ranking

import (
	"math"
	"testing"
)

// §6.4/§8.3 expertise weighting.
//
// Every test here guards its own fixture first. The temptation in this file is
// worse than the usual one: expertise inputs are continuous, so a test can
// "prove" a bound by asserting a number that was never in the interesting range.
// The guards are the tests.

// TestAnUntaggedSolutionWeightsEveryoneEqually is the baseline the multiplier
// must not move. An author who declares no expertise has made no claim, so
// inventing a weighting from the solution's prose would be reading meaning into
// text nobody tagged.
func TestAnUntaggedSolutionWeightsEveryoneEqually(t *testing.T) {
	for _, rep := range []float64{0, 5, 30, 100, 100000} {
		if m := ExpertiseMultiplier(rep, 0); m != 1.0 {
			t.Errorf("untagged solution, reputation %.0f: multiplier %.4f, want 1.0", rep, m)
		}
	}
	// And the wrapper, since this is the path that actually runs.
	if w := ExpertiseWeight(1.5, 900, 0, 3.0); w != 1.5 {
		t.Errorf("untagged: weight %.4f, want the base 1.5 untouched", w)
	}
}

// TestExpertiseNeverPenalises is the floor. Below 1.0 would mean a design is
// worth less because nobody who knows the topic judged it -- a popularity
// penalty wearing expertise's clothes.
func TestExpertiseNeverPenalises(t *testing.T) {
	// Zero reputation on a tagged solution is the case that would be penalised.
	if m := ExpertiseMultiplier(0, 3); m != 1.0 {
		t.Errorf("zero tag reputation: %.4f, want exactly 1.0 (no penalty)", m)
	}
	// A negative value must clamp, not produce a sub-1.0 weight.
	if m := ExpertiseMultiplier(-500, 2); m != 1.0 {
		t.Errorf("negative tag reputation: %.4f, want clamped to 1.0", m)
	}
}

// TestExpertiseIsCappedAgainstPlutocracy is §8.3's "capped to prevent
// plutocracy". The guard matters: without it, an absurd reputation would assert
// the bound trivially and the clamp below would be untested.
func TestExpertiseIsCappedAgainstPlutocracy(t *testing.T) {
	// The curve is 1.0x at 0, 1.5x at 30 and the 2.0x cap at 3H = 90, so a
	// reputation below that is *approaching* the cap, not past it. Asserting
	// strict inequality against the cap here would only ever be satisfiable by a
	// curve that never reaches it -- which is the plutocracy the cap exists to
	// prevent, reached by mislabelling the fixture.
	//
	// So: below the saturation point the multiplier must be strictly under the
	// cap, and at or above it the clamp must hold it at the cap.
	if m := ExpertiseMultiplier(ExpertiseHalfSaturating*2, 1); m >= MaxExpertiseMultiplier {
		t.Errorf("at 2H (%.0f): %.4f reached the cap early, so the curve is not logarithmic",
			ExpertiseHalfSaturating*2, m)
	} else if m <= MinExpertiseMultiplier {
		t.Errorf("at 2H: %.4f did not rise above the floor either", m)
	}
	if m := ExpertiseMultiplier(3*ExpertiseHalfSaturating, 1); m != MaxExpertiseMultiplier {
		t.Errorf("at 3H: %.4f, want exactly the cap %.1f", m, MaxExpertiseMultiplier)
	}
	// An expert is at most twice a stranger: never more, however deep the record.
	for _, rep := range []float64{90, 1e4, 1e6, 1e9} {
		if m := ExpertiseMultiplier(rep, 5); m > MaxExpertiseMultiplier {
			t.Errorf("reputation %.0f multi-tag: %.4f exceeds the cap %.1f", rep, m, MaxExpertiseMultiplier)
		}
	}
}

// TestExpertiseRisesMonotonically is the curve's shape. A multiplier that is not
// monotone would make a more-established voter count for less than a
// less-established one, which is worse than not weighting at all. It asserts
// monotonicity only over the range where the curve has room to rise: the cap
// binds at 3H = 90, so asking for growth past that would be asking the clamp to
// not clamp.
func TestExpertiseRisesMonotonically(t *testing.T) {
	prev := 0.0
	for _, rep := range []float64{0, 1, 5, 15, 30, 45, 60, 80} {
		m := ExpertiseMultiplier(rep, 2)
		if m <= prev {
			t.Fatalf("multiplier did not rise: rep %.0f -> %.4f after %.4f", rep, m, prev)
		}
		prev = m
	}
	// At the cap the curve is flat, not broken: no decrease, and no overshoot.
	at := ExpertiseMultiplier(90, 2)
	beyond := ExpertiseMultiplier(1e7, 2)
	if beyond < at {
		t.Errorf("past the cap the multiplier fell: %.4f -> %.4f", at, beyond)
	}
	if beyond > MaxExpertiseMultiplier {
		t.Errorf("past the cap: %.4f, want it held at %.1f", beyond, MaxExpertiseMultiplier)
	}
}

// TestExpertiseSaturatesLogarithmically pins the reason for the log: a track
// record of 30 and one of 300 must not be ten times apart in weight. Without this
// the only defence of the curve is that it happens to be clamped, which is not
// the same as being reasonable in the range people actually use.
func TestExpertiseSaturatesLogarithmically(t *testing.T) {
	low := ExpertiseMultiplier(30, 1)   // 10x
	high := ExpertiseMultiplier(300, 1) // 100x
	if high >= low*2 {
		t.Errorf("10x the reputation gave %.2fx the weight; the curve is not saturating", high/low)
	}
	if high <= low {
		t.Errorf("more reputation gave no more weight (%.4f -> %.4f)", low, high)
	}
	// The constant is documented as the half-ceiling point, so check it is one.
	half := ExpertiseMultiplier(ExpertiseHalfSaturating, 1)
	want := 1.0 + (MaxExpertiseMultiplier-1.0)*0.5
	if math.Abs(half-want) > 1e-9 {
		t.Errorf("at the documented half-saturation point: %.4f, want %.4f", half, want)
	}
}

// TestExpertiseAppliesTheCapToTheProduct is §6.4's "within the existing weight
// caps" and the reason the ordering inside ExpertiseWeight matters.
func TestExpertiseAppliesTheCapToTheProduct(t *testing.T) {
	// Guard: this base weight and reputation must multiply past the cap,
	// otherwise the test passes trivially on a value that was never over it.
	base, cap := 2.0, 3.0
	raw := base * ExpertiseMultiplier(1000, 1)
	if raw <= cap {
		t.Fatalf("fixture does not exceed the cap (raw %.4f <= %.1f)", raw, cap)
	}
	// So the cap must bind, and bind to the cap -- not to cap*2.
	if w := ExpertiseWeight(base, 1000, 1, cap); w != cap {
		t.Errorf("over-cap weight %.4f, want exactly the cap %.1f", w, cap)
	}
	// The trap this guards: capping first and multiplying after would give 6.0.
	if w := ExpertiseWeight(base, 1000, 1, cap); w > cap {
		t.Errorf("weight %.4f exceeds the cap; the cap is not binding last", w)
	}
	// Under the cap, the multiplier applies in full.
	if w := ExpertiseWeight(1.0, 10, 1, 3.0); w <= 1.0 {
		t.Errorf("under-cap weight %.4f, want the multiplier applied", w)
	}
	// A cap of zero means "no cap", not "weight zero".
	if w := ExpertiseWeight(1.0, 100, 1, 0); w <= 1.0 {
		t.Errorf("uncapped weight %.4f, want the multiplier applied", w)
	}
}

// TestExpertiseIsTagScoped is §8.3's "expertise is tag-scoped", and the property
// that separates this from a plain reputation multiplier. The same voter, the
// same reputation, on a Rust design and on an unrelated one.
func TestExpertiseIsTagScoped(t *testing.T) {
	perTag := map[string]float64{"rust": 200, "go": 5}
	onRust, n := BestTagReputation(perTag, ParseExpertiseTags("rust"))
	if n != 1 {
		t.Fatalf("tagsRequired %d, want 1", n)
	}
	if onRust != 200 {
		t.Fatalf("Rust reputation %.2f, want 200", onRust)
	}
	// The decisive comparison: same person, design with no Rust in it.
	onUnrelated, n2 := BestTagReputation(perTag, ParseExpertiseTags("cooking"))
	if n2 != 1 {
		t.Fatalf("tagsRequired %d, want 1", n2)
	}
	if m := ExpertiseMultiplier(onUnrelated, n2); m != 1.0 {
		t.Errorf("a Rust expert on a cooking design: %.4f, want 1.0 (tag-scoped)", m)
	}
	if onRust == onUnrelated {
		t.Fatal("the fixture gives the same reputation either way, so the scoping test proves nothing")
	}
}

// TestExpertiseCountsIsTheStrongestDeclaredTag is the best-tag judgement. A
// reviewer strong in ONE of a solution's three areas has standing to judge it.
func TestExpertiseCountsIsTheStrongestDeclaredTag(t *testing.T) {
	perTag := map[string]float64{"rust": 400, "wasm": 3, "sqlite": 80}
	best, n := BestTagReputation(perTag, ParseExpertiseTags("rust,wasm,sqlite"))
	if n != 3 {
		t.Errorf("tagsRequired %d, want 3", n)
	}
	if best != 400 {
		t.Errorf("best tag reputation %.2f, want 400 (the strongest declared tag)", best)
	}
	// The alternative -- a mean -- would give (400+3+80)/3 and let two weak tags
	// pull a genuine expert down. Assert the mean is meaningfully lower, because
	// that difference is the whole argument for best-tag.
	mean := (400.0 + 3 + 80) / 3.0
	if mean >= 400 {
		t.Fatalf("fixture does not separate best from mean (mean %.2f)", mean)
	}
	// A tag the voter has no reputation in must not be invented.
	best2, _ := BestTagReputation(perTag, ParseExpertiseTags("brainfuck"))
	if best2 != 0 {
		t.Errorf("unknown tag: %.2f, want 0", best2)
	}
}

// TestParseExpertiseTagsNormalises is what keeps the lookup in
// BestTagReputation from missing.
func TestParseExpertiseTagsNormalises(t *testing.T) {
	got := ParseExpertiseTags("  Rust , WASM ,, rust ")
	want := []string{"rust", "wasm"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	// Empty and whitespace-only both mean "no claim", not one empty tag.
	for _, raw := range []string{"", "   ", ",,,", " , , "} {
		if tags := ParseExpertiseTags(raw); tags != nil {
			t.Errorf("ParseExpertiseTags(%q) = %v, want nil", raw, tags)
		}
	}
	// A tag the voter is known by in a different case must still be found.
	perTag := map[string]float64{"rust": 250}
	best, n := BestTagReputation(perTag, ParseExpertiseTags("Rust"))
	if n != 1 || best != 250 {
		t.Errorf("mixed-case tag: best %.2f over %d tags, want 250 over 1", best, n)
	}
}
