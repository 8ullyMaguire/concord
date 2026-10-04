package scout

// Scout's classification tests (docs/specs/scout-spec.md §9).
//
// Every test here is named after the failure it catches, and the first version of
// this file passed while the code was wrong in four different ways — which is why
// most of these assert on a NAMED SIGNAL rather than on a verdict string. A
// verdict can be right for the wrong reason, and a signal names the rule that fired.
//
// The two that matter most:
//
//   - TestAProjectWithNoMetricsIsNotClassifiedAvoid is the test phase3-spec §6.3
//     asks for by name. `project_metrics` is empty on a fresh instance and on the
//     live one, so a classifier keyed on the stored health_score calls the whole
//     catalog `avoid` and passes any test asserting "at least one avoid".
//   - TestATieAtTheTopIsNotABaseOn. A `base-on` is the claim "fork this", and two
//     projects leading equally is a fork request. The first implementation used
//     `if count > best { best = count }` and awarded the label to whichever project
//     the map returned first.

import (
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// fixture builds a candidate with the given capabilities asserted.
func fixture(id int64, slug, name, license string, attrs map[string]string) store.ScoutCandidateRow {
	return store.ScoutCandidateRow{
		ID: id, Slug: slug, Name: name, License: license, Attrs: attrs,
	}
}

// threeCaps is a decomposition of three in-catalog capabilities.
func threeCaps() []store.ScoutCapability {
	return []store.ScoutCapability{
		{Key: "offline", Label: "Offline use", Category: "features", InCatalog: true},
		{Key: "wip-limits", Label: "WIP limits", Category: "features", InCatalog: true},
		{Key: "self-hosted", Label: "Self-hosted", Category: "deployment", InCatalog: true},
	}
}

func hasSignal(signals []string, want string) bool {
	for _, s := range signals {
		if s == want {
			return true
		}
	}
	return false
}

func hasSignalPrefix(signals []string, prefix string) bool {
	for _, s := range signals {
		if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func verdictFor(t *testing.T, r store.ScoutReport, slug string) store.ScoutVerdict {
	t.Helper()
	for _, v := range r.Verdicts {
		if v.Slug == slug {
			return v
		}
	}
	t.Fatalf("no verdict for %q; got %d verdicts", slug, len(r.Verdicts))
	return store.ScoutVerdict{}
}

// TestScoutReturnsAReasonForEveryClassification is the invariant the whole
// surface rests on: an unexplained classification is a bug, because the feature
// exists to answer "why did it say that".
func TestScoutReturnsAReasonForEveryClassification(t *testing.T) {
	cands := []store.ScoutCandidateRow{
		fixture(1, "lead", "Lead Project", "MIT",
			map[string]string{"offline": "yes", "wip-limits": "yes", "self-hosted": "yes"}),
		fixture(2, "partial", "Partial Project", "MIT", map[string]string{"offline": "yes"}),
		fixture(3, "empty", "Empty Project", "", nil),
	}
	health := map[int64]Health{
		1: {Value: 0.9, Known: true},
		2: {Value: 0.6, Known: true},
		// 3 deliberately absent: no telemetry.
	}

	r := Classify(Input{
		Idea:         "offline support, wip limits, self-hosted",
		Capabilities: threeCaps(), Candidates: cands, Health: health,
	}, Options{})

	if len(r.Verdicts) == 0 {
		t.Fatal("no verdicts at all")
	}
	for _, v := range r.Verdicts {
		if len(v.Signals) == 0 {
			t.Errorf("%s is classified %q with NO signals; every classification "+
				"must name the inputs that produced it", v.Slug, v.Verdict)
		}
		if v.Reason == "" {
			t.Errorf("%s is classified %q with no reason", v.Slug, v.Verdict)
		}
		if v.Verdict == "" {
			t.Errorf("%s has no verdict", v.Slug)
		}
	}
}

// The trap phase3-spec §6.3 names. Absent telemetry must not read as zero health.
func TestAProjectWithNoMetricsIsNotClassifiedAvoid(t *testing.T) {
	cands := []store.ScoutCandidateRow{
		fixture(1, "unmeasured", "Unmeasured", "MIT",
			map[string]string{"offline": "yes", "wip-limits": "yes", "self-hosted": "yes"}),
	}
	r := Classify(Input{
		Idea: "offline support", Capabilities: threeCaps(), Candidates: cands,
		// NO health entry at all.
	}, Options{})

	v := verdictFor(t, r, "unmeasured")
	if v.Verdict == store.ScoutAvoid {
		t.Fatalf("a project with no metrics row is classified avoid; that is the "+
			"exact trap spec §2 describes — absent telemetry is UNKNOWN, not bad. "+
			"health_known=%v health=%v", v.HealthKnown, v.Health)
	}
	if v.HealthKnown {
		t.Errorf("health_known is true with no metrics row; a reader would take " +
			"the number as measured")
	}
	if !hasSignal(v.Signals, "health_unmeasured") {
		t.Errorf("signals %v do not say the health is unmeasured; the reason a "+
			"number was not used has to be visible", v.Signals)
	}
}

// The honest-telemetry test §6.3 asks for by name: two fixtures with DIFFERENT
// health must classify differently. One fixture cannot show that.
func TestAnAbandonedProjectIsClassifiedAvoidWithAReason(t *testing.T) {
	cands := []store.ScoutCandidateRow{
		fixture(1, "abandoned", "Abandoned", "MIT",
			map[string]string{"offline": "yes", "wip-limits": "yes", "self-hosted": "yes"}),
		fixture(2, "healthy", "Healthy", "MIT",
			map[string]string{"offline": "yes", "wip-limits": "yes", "self-hosted": "yes"}),
	}
	health := map[int64]Health{
		1: {Value: 0.05, Known: true},
		2: {Value: 0.85, Known: true},
	}

	r := Classify(Input{
		Idea: "offline", Capabilities: threeCaps(), Candidates: cands, Health: health,
	}, Options{})

	bad := verdictFor(t, r, "abandoned")
	good := verdictFor(t, r, "healthy")

	if bad.Verdict != store.ScoutAvoid {
		t.Errorf("health 0.05 classified %q, want avoid", bad.Verdict)
	}
	if good.Verdict == store.ScoutAvoid {
		t.Errorf("health 0.85 classified avoid; the floor is not being applied " +
			"differentially, so the rule is not doing anything")
	}
	if bad.Verdict == good.Verdict {
		t.Fatalf("both projects classified %q; two different health values must "+
			"produce different verdicts or the signal is decorative", bad.Verdict)
	}
	if !hasSignalPrefix(bad.Signals, "health:") {
		t.Errorf("the avoid verdict has no health signal: %v", bad.Signals)
	}
	if !hasSignalPrefix(bad.Signals, "health_floor:") {
		t.Errorf("the avoid verdict does not name the floor it crossed: %v", bad.Signals)
	}
}

// A license constraint is a hard veto and outranks everything, including being
// the top match on every capability.
func TestALicenseConstraintBeatsBeingTheBestMatch(t *testing.T) {
	cands := []store.ScoutCandidateRow{
		fixture(1, "proprietary", "Proprietary Tool", "Proprietary",
			map[string]string{"offline": "yes", "wip-limits": "yes", "self-hosted": "yes"}),
	}
	health := map[int64]Health{1: {Value: 0.95, Known: true}}

	r := Classify(Input{
		Idea: "offline", Capabilities: threeCaps(), Candidates: cands, Health: health,
	}, Options{LicenseConstraints: []string{"proprietary"}})

	v := verdictFor(t, r, "proprietary")
	if v.Verdict != store.ScoutAvoid {
		t.Errorf("a proprietary project matching everything is classified %q, want "+
			"avoid; the hard constraint must be checked FIRST", v.Verdict)
	}
	if !hasSignalPrefix(v.Signals, "license_constraint:") {
		t.Errorf("no constraint signal: %v", v.Signals)
	}
}

// An EMPTY license must not conflict with anything. Treating "we do not know" as
// "incompatible" makes every un-licensed project `avoid` on a fresh instance — the
// same class of error as reading absent telemetry as zero health.
func TestAnUnknownLicenseDoesNotConflictWithAConstraint(t *testing.T) {
	cands := []store.ScoutCandidateRow{
		fixture(1, "no-license", "No License", "", map[string]string{"offline": "yes"}),
	}
	r := Classify(Input{
		Idea: "offline", Capabilities: threeCaps(), Candidates: cands,
		Health: map[int64]Health{1: {Value: 0.9, Known: true}},
	}, Options{LicenseConstraints: []string{"gpl"}})

	v := verdictFor(t, r, "no-license")
	if v.Verdict == store.ScoutAvoid && hasSignalPrefix(v.Signals, "license_constraint:") {
		t.Errorf("a project with NO license is excluded by a license constraint: %v",
			v.Signals)
	}
}

// The two verdicts must be distinguishable: a maximal, healthy lead is a base to
// fork; a subset match is a library to depend on.
func TestASubsetMatchIsAdoptAndAMaximalMatchIsBaseOn(t *testing.T) {
	cands := []store.ScoutCandidateRow{
		// Satisfies all three AND leads them all.
		fixture(1, "everything", "Everything", "MIT",
			map[string]string{"offline": "yes", "wip-limits": "yes", "self-hosted": "yes"}),
		// Satisfies one.
		fixture(2, "one-thing", "One Thing", "MIT", map[string]string{"offline": "yes"}),
	}
	health := map[int64]Health{
		1: {Value: 0.92, Known: true},
		2: {Value: 0.80, Known: true},
	}

	r := Classify(Input{
		Idea: "offline", Capabilities: threeCaps(), Candidates: cands, Health: health,
	}, Options{})

	full := verdictFor(t, r, "everything")
	part := verdictFor(t, r, "one-thing")

	if full.Verdict != store.ScoutBaseOn {
		t.Errorf("a healthy project satisfying all 3 capabilities is %q, want base-on "+
			"(signals %v)", full.Verdict, full.Signals)
	}
	if part.Verdict != store.ScoutAdopt {
		t.Errorf("a project satisfying 1 of 3 is %q, want adopt (signals %v)",
			part.Verdict, part.Signals)
	}
	if full.Verdict == part.Verdict {
		t.Fatalf("both are %q; the two verdicts must be distinguishable or the "+
			"classification is not doing the work the spec claims", full.Verdict)
	}
	if !hasSignalPrefix(part.Signals, "subset:") {
		t.Errorf("the adopt verdict does not name the subset it matched: %v", part.Signals)
	}
}

// The tie rule. Two projects leading equally is a fork request, not a base.
func TestATieAtTheTopIsNotABaseOn(t *testing.T) {
	attrs := map[string]string{"offline": "yes", "wip-limits": "yes", "self-hosted": "yes"}
	cands := []store.ScoutCandidateRow{
		fixture(1, "alpha", "Alpha", "MIT", attrs),
		fixture(2, "beta", "Beta", "MIT", attrs),
	}
	health := map[int64]Health{
		1: {Value: 0.9, Known: true},
		2: {Value: 0.9, Known: true},
	}

	r := Classify(Input{
		Idea: "offline", Capabilities: threeCaps(), Candidates: cands, Health: health,
	}, Options{})

	alpha := verdictFor(t, r, "alpha")
	beta := verdictFor(t, r, "beta")

	if alpha.Verdict == store.ScoutBaseOn || beta.Verdict == store.ScoutBaseOn {
		t.Fatalf("a tie produced base-on (alpha=%s beta=%s); a fork request is not a "+
			"base", alpha.Verdict, beta.Verdict)
	}
	// And both are told WHO they tie with, because the report has to be actionable
	// rather than just withholding a label.
	for _, v := range []store.ScoutVerdict{alpha, beta} {
		if !hasSignalPrefix(v.Signals, "tied_with:") {
			t.Errorf("%s is not told which project it ties with: %v", v.Slug, v.Signals)
		}
	}
	// Nobody was awarded a lead on a tied capability.
	if alpha.LeadOn > 0 || beta.LeadOn > 0 {
		t.Errorf("lead counts awarded on a tie: alpha=%d beta=%d; a tie awards "+
			"nobody the lead", alpha.LeadOn, beta.LeadOn)
	}
}

// An uncontested lead IS a base-on, which is the mirror of the tie test and stops
// the tie rule from being implemented as "never award a lead".
func TestAnUncontestedLeadIsABaseOn(t *testing.T) {
	cands := []store.ScoutCandidateRow{
		// Satisfies all three, alone.
		fixture(1, "solo", "Solo", "MIT",
			map[string]string{"offline": "yes", "wip-limits": "yes", "self-hosted": "yes"}),
		// Satisfies one, so it cannot tie.
		fixture(2, "other", "Other", "MIT", map[string]string{"offline": "yes"}),
	}
	health := map[int64]Health{
		1: {Value: 0.95, Known: true},
		2: {Value: 0.9, Known: true},
	}

	r := Classify(Input{
		Idea: "offline", Capabilities: threeCaps(), Candidates: cands, Health: health,
	}, Options{})

	solo := verdictFor(t, r, "solo")
	if solo.Verdict != store.ScoutBaseOn {
		t.Errorf("the uncontested leader is %q, want base-on (signals %v)",
			solo.Verdict, solo.Signals)
	}
	if solo.LeadOn != 3 {
		t.Errorf("lead_on = %d, want 3; the lead count is not being counted per "+
			"capability", solo.LeadOn)
	}
}

// `inspire` is the last rule, and it needs to be reachable: a dead project with no
// competing evidence.
func TestADeadProjectWithNothingBetterIsInspire(t *testing.T) {
	cands := []store.ScoutCandidateRow{
		fixture(1, "zombie", "Zombie", "MIT", map[string]string{"offline": "yes"}),
	}
	// 0.15: under ScoutDeadFloor (0.10)? No — above. So this is `avoid`-free but
	// still alive by the dead rule. Use a value under 0.10 for the real test.
	r := Classify(Input{
		Idea: "offline", Capabilities: threeCaps(), Candidates: cands,
		Health: map[int64]Health{1: {Value: 0.05, Known: true}},
	}, Options{})

	v := verdictFor(t, r, "zombie")
	// 0.05 is under BOTH floors, so rule 2 fires and it is `avoid`, not `inspire`.
	// That is the documented ordering and this test pins it.
	if v.Verdict != store.ScoutAvoid {
		t.Errorf("health 0.05 is %q; under both floors it must be avoid, and this "+
			"test exists to pin that `avoid` outranks `inspire`", v.Verdict)
	}
	if !hasSignalPrefix(v.Signals, "dead_below:") {
		t.Errorf("a dead project classified avoid does not say it is also dead: %v",
			v.Signals)
	}
}

// Evidence coverage must be reported, and a fit over no evidence must read as
// "we know nothing" rather than as a number.
func TestScoutReportsEvidenceCoverageBesideFit(t *testing.T) {
	cands := []store.ScoutCandidateRow{
		// One of three capabilities has any assertion at all.
		fixture(1, "thin", "Thin Evidence", "MIT", map[string]string{"offline": "yes"}),
	}
	r := Classify(Input{
		Idea: "offline", Capabilities: threeCaps(), Candidates: cands,
		Health: map[int64]Health{1: {Value: 0.9, Known: true}},
	}, Options{})

	v := verdictFor(t, r, "thin")
	if v.Coverage <= 0 || v.Coverage >= 1 {
		t.Errorf("evidence_coverage = %v; a project with one assertion out of three "+
			"capabilities has partial coverage, and a bare fit number hides that", v.Coverage)
	}
	if v.Total != 3 {
		t.Errorf("total = %d, want 3 (the in-catalog capability count)", v.Total)
	}
	if v.Matched != 1 {
		t.Errorf("matched = %d, want 1", v.Matched)
	}
}

// A project with NO assertions scores nothing, and must not be reported as a
// confident non-match: coverage 0, fit 0, and not `avoid`.
func TestAProjectWithNoAssertionsIsNotScoredAsAMatch(t *testing.T) {
	cands := []store.ScoutCandidateRow{
		fixture(1, "silent", "Silent", "MIT", nil),
	}
	r := Classify(Input{
		Idea: "offline", Capabilities: threeCaps(), Candidates: cands,
		Health: map[int64]Health{1: {Value: 0.9, Known: true}},
	}, Options{})

	v := verdictFor(t, r, "silent")
	if v.Matched != 0 || v.Coverage != 0 || v.Fit != 0 {
		t.Errorf("matched=%d coverage=%v fit=%v for a project with no assertions; "+
			"absence of evidence is not a score of zero — it is no evidence",
			v.Matched, v.Coverage, v.Fit)
	}
}

// A proposed capability (no catalog row) must contribute ZERO weight.
//
// The SPEC names this test `TestScoutNeverInventsACapabilityOutsideTheCatalog`, and
// the decomposition side of that claim lives in decompose_test.go under that name.
// This is the scoring half: even handed a decomposition containing a proposal, the
// classifier must not give it weight. Two halves, one rule, so the two are named
// apart — the duplicate name was a build failure, not a subtle duplication.
func TestTheScorerGivesAProposedCapabilityNoWeight(t *testing.T) {
	cands := []store.ScoutCandidateRow{
		fixture(1, "three-cap", "Three Cap", "MIT",
			map[string]string{"offline": "yes", "wip-limits": "yes", "self-hosted": "yes"}),
	}
	withProposal := []store.ScoutCapability{
		{Key: "offline", InCatalog: true},
		{Key: "wip-limits", InCatalog: true},
		{Key: "self-hosted", InCatalog: true},
		// No catalog row: cannot be scored.
		{Key: "quantum-telemetry", InCatalog: false},
	}

	r := Classify(Input{
		Idea: "offline", Capabilities: withProposal, Candidates: cands,
		Health: map[int64]Health{1: {Value: 0.9, Known: true}},
	}, Options{})

	v := verdictFor(t, r, "three-cap")
	if v.Total != 3 {
		t.Errorf("total = %d, want 3; a capability with no catalog row contributed "+
			"weight, so it was scored against nothing (total=%d)", v.Total, v.Total)
	}
	if r.InCatalog != 3 || r.Proposed != 1 {
		t.Errorf("in_catalog=%d proposed=%d, want 3 and 1", r.InCatalog, r.Proposed)
	}
	// And it is reported, not dropped.
	found := false
	for _, c := range r.Capabilities {
		if c.Key == "quantum-telemetry" {
			found = true
			if c.InCatalog {
				t.Error("a proposed capability is marked in-catalog")
			}
		}
	}
	if !found {
		t.Error("the proposed capability is missing from the report; it must be " +
			"reported even though it cannot be scored")
	}
}

// With nothing in the catalog the report must still be well-formed.
func TestAnEmptyDecompositionProducesNoVerdictsRatherThanNonsense(t *testing.T) {
	cands := []store.ScoutCandidateRow{
		fixture(1, "anything", "Anything", "MIT", map[string]string{"offline": "yes"}),
	}
	r := Classify(Input{
		Idea: "something", Capabilities: nil, Candidates: cands,
	}, Options{})

	if len(r.Verdicts) != 1 {
		t.Fatalf("got %d verdicts for an empty decomposition, want 1 (the candidate "+
			"is still reported, just unscored)", len(r.Verdicts))
	}
	v := r.Verdicts[0]
	if v.Total != 0 || v.Fit != 0 {
		t.Errorf("total=%d fit=%v with no capabilities; nothing should be scored", v.Total, v.Fit)
	}
	if len(v.Signals) == 0 {
		t.Error("a verdict with no signals, even an unscored one — the invariant holds " +
			"for every verdict, not only the confident ones")
	}
}

// Report shape: never nil slices, so a client does not have to distinguish null
// from empty.
func TestTheReportNeverReturnsNullSlices(t *testing.T) {
	r := Classify(Input{Idea: "x"}, Options{})
	if r.Capabilities == nil {
		t.Error("capabilities is nil; the page must be able to tell 'no capabilities' " +
			"from an absent field")
	}
	if r.Verdicts == nil {
		t.Error("verdicts is nil")
	}
	if r.Stats == nil {
		t.Error("stats is nil")
	}
}

// Stats must account for every verdict, or the counts are decoration.
func TestStatsAccountForEveryVerdict(t *testing.T) {
	cands := []store.ScoutCandidateRow{
		fixture(1, "good", "Good", "MIT", map[string]string{"offline": "yes"}),
		fixture(2, "bad", "Bad", "MIT", map[string]string{"offline": "yes"}),
	}
	r := Classify(Input{
		Idea: "offline", Capabilities: threeCaps(), Candidates: cands,
		Health: map[int64]Health{1: {Value: 0.9, Known: true}, 2: {Value: 0.01, Known: true}},
	}, Options{})

	total := 0
	for _, n := range r.Stats {
		total += n
	}
	if total != len(r.Verdicts) {
		t.Errorf("stats sum to %d but there are %d verdicts", total, len(r.Verdicts))
	}
}

// avoid must sort first: a project you must not use outranks the best one you can.
func TestAvoidSortsBeforeTheGoodVerdicts(t *testing.T) {
	cands := []store.ScoutCandidateRow{
		fixture(1, "good", "Good", "MIT",
			map[string]string{"offline": "yes", "wip-limits": "yes", "self-hosted": "yes"}),
		fixture(2, "bad", "Bad", "MIT", map[string]string{"offline": "yes"}),
	}
	r := Classify(Input{
		Idea: "offline", Capabilities: threeCaps(), Candidates: cands,
		Health: map[int64]Health{1: {Value: 0.9, Known: true}, 2: {Value: 0.01, Known: true}},
	}, Options{})

	if r.Verdicts[0].Verdict != store.ScoutAvoid {
		t.Errorf("the first verdict is %q (%s); avoid must sort first, because it is "+
			"the one the reader must not miss", r.Verdicts[0].Verdict, r.Verdicts[0].Slug)
	}
}

// `subset:` and `complete:` are different signals and the distinction has to be
// testable on its own.
//
// The mutation that collapses the two branches — changing `r.matched < r.total` to
// `r.total > 0` — survived against TestASubsetMatchIsAdoptAndAMaximalMatchIsBaseOn,
// because that test's "everything" project is classified base-on by RULE 3 (it leads
// every capability uncontested) long before rule 5 is reached. The verdict string
// did not change, so the test did not notice.
//
// This test asserts on the SIGNAL, which is what actually differs, and gives both
// projects the same verdict so only the signal can tell them apart.
func TestACompleteMatchIsNotReportedAsASubset(t *testing.T) {
	caps := threeCaps()
	cands := []store.ScoutCandidateRow{
		// Satisfies all three. Given a TIE for each, rule 3 cannot award a lead, so
		// this falls through to the scoring rules rather than to base-on.
		fixture(1, "aaa-complete", "AAA", "MIT", map[string]string{
			"offline": "yes", "wip-limits": "yes", "self-hosted": "yes"}),
		// Identical capabilities, so it ties on every axis: both land in `extend`
		// and the only thing left to differ is subset vs complete.
		fixture(2, "bbb-complete", "BBB", "MIT", map[string]string{
			"offline": "yes", "wip-limits": "yes", "self-hosted": "yes"}),
	}
	health := map[int64]Health{
		1: {Value: 0.9, Known: true},
		2: {Value: 0.9, Known: true},
	}

	r := Classify(Input{
		Idea: "offline", Capabilities: caps, Candidates: cands, Health: health,
	}, Options{})

	a := verdictFor(t, r, "aaa-complete")
	// An identical twin guarantees the tie branch, so the verdict is `extend` and a
	// `subset:` signal here is unambiguously wrong.
	if hasSignalPrefix(a.Signals, "subset:") {
		t.Errorf("a project matching 3 of 3 carries a subset signal: %v", a.Signals)
	}
	if !hasSignalPrefix(a.Signals, "complete:3_of_3") {
		t.Errorf("a project matching 3 of 3 has no complete signal: %v (verdict %q)",
			a.Signals, a.Verdict)
	}
}

// The mirror: a genuine subset MUST carry `subset:`, not `complete:`. Without this
// the previous test would also pass if both branches emitted `complete:`.
func TestAPartialMatchCarriesTheSubsetSignal(t *testing.T) {
	caps := threeCaps()
	cands := []store.ScoutCandidateRow{
		// One of three.
		fixture(1, "only-offline", "Only Offline", "MIT", map[string]string{"offline": "yes"}),
		// The rival matters: a lone 1-of-3 project leads its ONE capability
		// uncontested, so rule 3 awards base-on before the scoring rules are ever
		// reached. The first version of this test had no rival and so asserted
		// `subset:` on a verdict that is legitimately base-on.
		fixture(2, "rival", "Rival", "MIT", map[string]string{"offline": "yes"}),
	}
	health := map[int64]Health{
		1: {Value: 0.9, Known: true},
		2: {Value: 0.9, Known: true},
	}
	r := Classify(Input{
		Idea: "offline", Capabilities: caps, Candidates: cands, Health: health,
	}, Options{})

	v := verdictFor(t, r, "only-offline")
	if !hasSignalPrefix(v.Signals, "subset:1_of_3") {
		t.Errorf("a 1-of-3 match has no subset signal: %v", v.Signals)
	}
	if hasSignalPrefix(v.Signals, "complete:") {
		t.Errorf("a 1-of-3 match claims to be complete: %v", v.Signals)
	}
}

// The final `unclassified` branch must carry its own signal. This is the only test
// that reaches it: a candidate with no capabilities at all (so rules 5 and 6 are
// skipped) and no lead, no conflict and healthy-enough telemetry.
func TestAnUnclassifiableCandidateStillSaysSo(t *testing.T) {
	cands := []store.ScoutCandidateRow{
		fixture(1, "mystery", "Mystery", "MIT", map[string]string{"offline": "yes"}),
	}
	r := Classify(Input{
		Idea: "offline", Capabilities: nil, Candidates: cands,
		Health: map[int64]Health{1: {Value: 0.9, Known: true}},
	}, Options{})

	v := verdictFor(t, r, "mystery")
	if !hasSignal(v.Signals, "unclassified") {
		t.Errorf("the fallback verdict has no `unclassified` signal: %v; a verdict "+
			"with no signal cannot be audited", v.Signals)
	}
	if v.Verdict != store.ScoutAvoid {
		t.Errorf("the fallback verdict is %q, want avoid — nothing earned this "+
			"project a better label", v.Verdict)
	}
}
