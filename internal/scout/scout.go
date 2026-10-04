package scout

// Scout's classification — docs/specs/scout-spec.md §3 and §4.
//
// This is the one part of Scout with no analogue in the rest of the codebase: the
// Finder asks questions and ranks, and nothing anywhere says "this project is a
// base to fork and that one is a library to depend on". The decomposition is
// here too, because it is the same kind of judgement — text in, catalog-shaped
// structure out, with the reasoning recorded.
//
// Two rules are load-bearing enough to state at the top.
//
//   - **Every verdict carries its signals.** The surface exists to answer "why
//     did it say that", so a classification without a named input is a bug rather
//     than a terse style. `classify` cannot produce one: the switch has no
//     default, and an unclassified candidate is returned as `avoid` with
//     `unclassified` as its signal rather than being given a plausible-sounding
//     verdict nobody earned.
//
//   - **Unknown health is not zero health.** `project_metrics` is empty on a fresh
//     instance and on the live one (spec §2), so a rule keyed on the stored
//     `health_score` column calls the entire catalog `avoid` and passes any test
//     asserting "at least one avoid". Every rule below therefore requires
//     `known` before it uses the number, and an unmeasured project is `adopt`
//     with `health_unmeasured` among its signals — not `avoid`.

import (
	"fmt"
	"sort"
	"strings"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// Verdict constants are re-exported from the store package so a caller of this
// package does not need both.
const (
	Adopt   = store.ScoutAdopt
	BaseOn  = store.ScoutBaseOn
	Extend  = store.ScoutExtend
	Inspire = store.ScoutInspire
	Avoid   = store.ScoutAvoid
)

// Options are the caller's stated constraints and thresholds.
//
// Everything here has a default, because a classifier whose behaviour depends on
// an unset field is a classifier whose behaviour nobody can describe. The
// defaults are the spec's values; a caller overrides them only when it has a
// reason, and `Report` echoes the thresholds it used so a reader can see them.
type Options struct {
	// HealthFloor: below this, a project with KNOWN metrics is `avoid`.
	// Defaults to store.ScoutHealthFloor.
	HealthFloor float64
	// DeadFloor: below this, a project with KNOWN metrics is `inspire` — dead but
	// instructive — rather than merely `avoid`.
	DeadFloor float64
	// LicenseConstraints are license expressions the caller will not accept, e.g.
	// "proprietary" or a list of SPDX ids. Matched case-insensitively against the
	// project's own license. Empty means no license constraint, and a project with
	// no license is NOT violating one: an unknown license is unknown, not
	// incompatible.
	LicenseConstraints []string
	// MaxVerdicts bounds the returned list. Every verdict is classified, not just
	// the top N — the whole point is also learning what to avoid — so this trims a
	// tail that is ordered by verdict severity first and fit second.
	MaxVerdicts int
}

// withDefaults fills the zero values.
func (o Options) withDefaults() Options {
	if o.HealthFloor <= 0 {
		o.HealthFloor = store.ScoutHealthFloor
	}
	if o.DeadFloor <= 0 {
		o.DeadFloor = store.ScoutDeadFloor
	}
	if o.MaxVerdicts <= 0 {
		o.MaxVerdicts = 25
	}
	return o
}

// Input is everything Classify needs, assembled by the caller.
type Input struct {
	Idea string
	// Capabilities is the decomposition: catalog keys plus explicit proposals.
	// A capability with InCatalog false contributes NO scoring weight — it cannot,
	// because there is nothing in the catalog to match it against.
	Capabilities []store.ScoutCapability
	// Candidates are the readable catalog projects.
	Candidates []store.ScoutCandidateRow
	// Health maps project id to its computed score and whether it means anything.
	// A caller that omits a project here is asserting "no telemetry", which is the
	// same as a metrics row being absent.
	Health map[int64]Health
	// Pain is the known pain per project.
	Pain map[int64][]store.ScoutPain
}

// Health is a score and whether it means anything.
type Health struct {
	Value float64
	Known bool
}

// Classify produces the report.
//
// The sequence is fixed by spec §3 and the order IS the rule, so it is
// implemented as one ordered pass rather than as independent predicates combined
// later: a license conflict must beat a healthy lead, and an abandoned project
// must not be `base-on` however well it matches.
func Classify(in Input, opts Options) store.ScoutReport {
	opts = opts.withDefaults()

	report := store.ScoutReport{
		Idea:                 strings.TrimSpace(in.Idea),
		Stats:                map[string]int{},
		CandidatesConsidered: len(in.Candidates),
	}
	for _, c := range in.Capabilities {
		report.Capabilities = append(report.Capabilities, c)
		if c.InCatalog {
			report.InCatalog++
		} else {
			report.Proposed++
		}
	}
	report.Constraints = opts.LicenseConstraints

	scored := score(in)
	leads, tiedWith := leadCounts(in, scored)

	for _, cand := range in.Candidates {
		r := scored[cand.ID]
		h := in.Health[cand.ID]
		v := store.ScoutVerdict{
			ProjectID: cand.ID,
			Slug:      cand.Slug,
			Name:      cand.Name,
			Fit:       r.fit,
			Coverage:  r.coverage,
			Matched:   r.matched,
			Total:     r.total,
			LeadOn:    leads[cand.ID],
			// Signals carrying tie information are merged in below, after
			// applyVerdict has chosen the verdict.
			Health:      h.Value,
			HealthKnown: h.Known,
			License:     cand.License,
			OutcomeRate: cand.OutcomeRate,
			Reports:     cand.Reports,
			Pain:        in.Pain[cand.ID],
		}
		applyVerdict(&v, cand, h, leads[cand.ID], r, opts)
		// Tie signals are additive: whatever verdict the rules chose, the reader
		// also learns that this project was level with another. They are appended
		// rather than passed in, so a tie can never suppress the signal a verdict
		// is required to carry.
		v.Signals = append(v.Signals, tiedWith[cand.ID]...)
		report.Verdicts = append(report.Verdicts, v)
		report.Stats[v.Verdict]++
	}

	sortVerdicts(report.Verdicts)
	if len(report.Verdicts) > opts.MaxVerdicts {
		report.Verdicts = report.Verdicts[:opts.MaxVerdicts]
	}
	if report.Capabilities == nil {
		report.Capabilities = []store.ScoutCapability{}
	}
	if report.Verdicts == nil {
		report.Verdicts = []store.ScoutVerdict{}
	}
	return report
}

// applyVerdict sets the verdict, reason and signals for one candidate.
//
// Every branch appends at least one signal, and the `default` case cannot leave
// them empty. That is the invariant spec §3 asks for and the one
// TestScoutReturnsAReasonForEveryClassification checks.
func applyVerdict(v *store.ScoutVerdict, cand store.ScoutCandidateRow,
	h Health, leadOn int, r scoreResult, opts Options) {
	// 1. A license constraint is a hard veto and outranks everything. It is first
	//    because a user who said "nothing proprietary" wants that answered before
	//    any question of how well something fits.
	if conflict := licenseConflict(cand.License, opts.LicenseConstraints); conflict != "" {
		v.Verdict = Avoid
		v.Signals = append(v.Signals, "license_constraint:"+conflict,
			"license:"+cand.License)
		v.Reason = fmt.Sprintf(
			"excluded by the caller's license constraint (%s says %q)",
			conflict, cand.License)
		return
	}

	// 2. Abandoned, but only on KNOWN metrics. An unmeasured project is not an
	//    abandoned one, which is the trap spec §2 exists to close.
	if h.Known && h.Value < opts.HealthFloor {
		v.Signals = append(v.Signals, fmt.Sprintf("health:%.2f", h.Value),
			fmt.Sprintf("health_floor:%.2f", opts.HealthFloor))
		if h.Value < opts.DeadFloor {
			// Dead as well as unhealthy. `avoid` still, because "abandoned" is the
			// more useful thing to hear, but the signal distinguishes them.
			v.Signals = append(v.Signals, fmt.Sprintf("dead_below:%.2f", opts.DeadFloor))
			v.Verdict = Avoid
			v.Reason = fmt.Sprintf(
				"looks abandoned: health %.2f is under the %.2f floor, and under the "+
					"%.2f dead threshold as well", h.Value, opts.HealthFloor, opts.DeadFloor)
			return
		}
		v.Verdict = Avoid
		v.Reason = fmt.Sprintf(
			"looks abandoned: health %.2f is under the %.2f floor", h.Value, opts.HealthFloor)
		return
	}

	// 3. The lead — but only an UNCONTESTED one, and only if it is healthy enough
	//    and licensed enough to be a base.
	//
	//    A tie is not a base-on. Two projects leading equally is a fork request,
	//    and the report says so with both names rather than awarding the label to
	//    whichever row happened to come first (spec §3).
	if tieNames := tiedNames(v.Signals); tieNames != "" && leadOn == 0 {
		// Tied at the top of some capability and nobody was awarded the lead, so
		// this is a starting point rather than a base.
		v.Verdict = Extend
		v.Reason = "level with another project on at least one capability, so it is " +
			"a starting point to extend rather than a base to fork (" + tieNames + ")"
		return
	}
	if leadOn > 0 && h.Known {
		v.Verdict = BaseOn
		v.Signals = append(v.Signals, fmt.Sprintf("leads:%d", leadOn),
			fmt.Sprintf("health:%.2f", h.Value))
		if cand.License != "" {
			v.Signals = append(v.Signals, "license:"+cand.License)
		}
		v.Reason = fmt.Sprintf(
			"the strongest starting point: top match on %d of %d capabilities, "+
				"health %.2f", leadOn, r.total, h.Value)
		return
	}

	// 5. A subset match: satisfy some of it and be a library to depend on.
	if r.total > 0 && r.matched < r.total {
		v.Verdict = Adopt
		v.Signals = append(v.Signals, fmt.Sprintf("subset:%d_of_%d", r.matched, r.total))
		if !h.Known {
			v.Signals = append(v.Signals, "health_unmeasured")
		}
		if cand.License != "" {
			v.Signals = append(v.Signals, "license:"+cand.License)
		}
		v.Reason = fmt.Sprintf(
			"covers %d of the %d capabilities in this idea — a dependency to take "+
				"rather than a base to fork", r.matched, r.total)
		return
	}
	if r.total > 0 && r.matched == r.total {
		v.Verdict = Adopt
		v.Signals = append(v.Signals, fmt.Sprintf("complete:%d_of_%d", r.matched, r.total))
		// The unmeasured-health signal belongs here too, not only on the subset
		// branch. A first version put it on `subset:` alone, so a project matching
		// EVERY capability with no metrics row carried no record that its health
		// was unknown — and the reader would take the absence of `health:` in the
		// signals as "health was not relevant" rather than "we never measured it".
		if !h.Known {
			v.Signals = append(v.Signals, "health_unmeasured")
		}
		if cand.License != "" {
			v.Signals = append(v.Signals, "license:"+cand.License)
		}
		v.Reason = fmt.Sprintf(
			"satisfies all %d capabilities — a complete dependency", r.total)
		return
	}

	// 6. Dead but instructive. Last, because it is the weakest claim: a dead
	//    project that is nonetheless the best available answer is still worth
	//    reading, but not forking.
	if h.Known && h.Value < opts.DeadFloor {
		v.Verdict = Inspire
		v.Signals = append(v.Signals, fmt.Sprintf("dead:%.2f", h.Value),
			fmt.Sprintf("dead_below:%.2f", opts.DeadFloor))
		v.Reason = fmt.Sprintf(
			"dead (health %.2f, under %.2f) but still the closest thing in the "+
				"catalog — worth reading, not worth forking", h.Value, opts.DeadFloor)
		return
	}

	// Unreachable given the ordering above, and deliberately so: `classify` has no
	// way to produce a verdict without signals, so an unclassifiable candidate is
	// reported as what it is rather than given a plausible label.
	v.Verdict = Avoid
	v.Signals = append(v.Signals, "unclassified")
	v.Reason = "no rule matched; nothing here says this is usable"
}

// licenseConflict returns the constraint this license violates, or "".
//
// An EMPTY project license never conflicts. Treating "we do not know" as
// "incompatible" would make every project with an unset license `avoid` on a
// fresh instance, which is the same class of error as reading absent telemetry as
// zero health — an absence of information turned into a verdict.
func licenseConflict(license string, constraints []string) string {
	l := strings.ToLower(strings.TrimSpace(license))
	if l == "" || len(constraints) == 0 {
		return ""
	}
	for _, c := range constraints {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" {
			continue
		}
		if strings.Contains(l, c) || strings.Contains(c, l) {
			return c
		}
	}
	return ""
}

// sortVerdicts orders by severity of the verdict, then by fit.
//
// Severity order is the reader's order: a project you must not use is more
// important than the best one you can. Fit breaks ties within a verdict.
func sortVerdicts(v []store.ScoutVerdict) {
	severity := map[string]int{
		Avoid: 0, BaseOn: 1, Extend: 2, Adopt: 3, Inspire: 4,
	}
	sort.SliceStable(v, func(i, j int) bool {
		si, sj := severity[v[i].Verdict], severity[v[j].Verdict]
		if si != sj {
			return si < sj
		}
		if v[i].Fit != v[j].Fit {
			return v[i].Fit > v[j].Fit
		}
		return v[i].Name < v[j].Name
	})
}

// scoreResult is one project's score against the decomposition.
type scoreResult struct {
	// fit is the share of in-catalog capabilities this project satisfies, and
	// coverage is the share of those that had any evidence at all.
	//
	// Both are always reported together. A fit with no coverage is the
	// misleading-reporting failure the Finder fix already established, and Scout
	// reads a 100% fit over zero evidence as "we know nothing", which is exactly
	// what it means.
	fit      float64
	coverage float64
	// matched is how many in-catalog capabilities the project satisfies and total
	// is how many there are. `matched < total` is what makes a verdict `adopt`
	// rather than `base-on` (spec §3).
	matched int
	total   int
	// satisfied names the keys that matched, so a reader can see WHICH
	// capabilities a verdict covers rather than being told a number.
	satisfied []string
}

// tiedNames extracts the "tied_with:..." signal into a readable phrase.
//
// Parsed back out of the signals rather than carried as a struct field, because
// a tie is extra information about a verdict rather than part of the scoring:
// every verdict branch is required to emit its own signals, and the tie has to be
// able to coexist with all of them.
func tiedNames(signals []string) string {
	for _, s := range signals {
		if strings.HasPrefix(s, "tied_with:") {
			return strings.TrimPrefix(s, "tied_with:")
		}
	}
	return ""
}

// score computes each candidate against the decomposition.
//
// `absence is not evidence` is the rule that runs through all of it (finder.go:
// 83): a capability with NO assertion for a project is unknown, not false. So a
// project that satisfies one of nine capabilities does NOT score 11% — it scores
// 100% of the one axis it has evidence on, with coverage recording that eight
// axes were unknown. Reporting it as 11% would tell the reader this project is
// nearly a non-match, which is a claim the data does not support.
func score(in Input) map[int64]scoreResult {
	inCatalog := make([]string, 0, len(in.Capabilities))
	for _, c := range in.Capabilities {
		if c.InCatalog {
			inCatalog = append(inCatalog, c.Key)
		}
	}
	out := make(map[int64]scoreResult, len(in.Candidates))
	for _, cand := range in.Candidates {
		r := scoreResult{total: len(inCatalog)}
		if r.total == 0 {
			out[cand.ID] = r
			continue
		}
		// Evidence is "the project says something about this capability". The value
		// itself does not gate the match here — Scout matches on coverage of an
		// axis, and whether the value is `yes` or `no` is the user's call to read,
		// not Scout's to filter on. Filtering would make Scout answer a question
		// nobody asked ("which projects are a yes?") and hide the evidence that
		// says no, which is often the most useful thing on the page.
		var known int
		for _, key := range inCatalog {
			v, ok := cand.Attrs[key]
			if !ok || v == "" {
				continue
			}
			known++
			r.satisfied = append(r.satisfied, key)
		}
		r.matched = len(r.satisfied)
		if known > 0 {
			r.coverage = float64(known) / float64(r.total)
			r.fit = float64(known) / float64(r.total)
		}
		out[cand.ID] = r
	}
	return out
}

// leadCounts returns, per project, how many capabilities it is the TOP match
// for, and which projects it ties with at each of those leads.
//
// The tie is the point. A `base-on` verdict is the claim "this is the thing to
// fork", and two projects leading equally is a fork request, not a base — so the
// counts alone cannot decide the verdict and the tie has to be visible to the
// caller (spec §3).
//
// Ordering is explicit — fit, then coverage, then satisfied axes, then name — so
// the outcome never depends on row order. The defect that made an early version
// award `base-on` to whichever project the query returned first was exactly that
// dependence.
func leadCounts(in Input, scored map[int64]scoreResult) (map[int64]int, map[int64][]string) {
	// For each capability: the projects that satisfy it, best first.
	byCap := make(map[string][]store.ScoutCandidateRow)
	for _, cand := range in.Candidates {
		for _, key := range scored[cand.ID].satisfied {
			byCap[key] = append(byCap[key], cand)
		}
	}

	leads := make(map[int64]int)
	tiedWith := make(map[int64][]string)

	for key, cands := range byCap {
		// Ties are decided by the full comparator, so "tied" means genuinely equal
		// on every axis rather than equal on one.
		sort.SliceStable(cands, func(i, j int) bool {
			return betterMatch(scored[cands[i].ID], cands[i],
				scored[cands[j].ID], cands[j])
		})
		top := cands[0]
		leaders := []store.ScoutCandidateRow{top}
		for _, c := range cands[1:] {
			if matchEqual(scored[c.ID], c, scored[top.ID], top) {
				leaders = append(leaders, c)
			} else {
				break
			}
		}
		if len(leaders) == 1 {
			leads[top.ID]++
			continue
		}
		// A tie awards nobody the lead, and every tied project is told who it ties
		// with. Awarding it to one of them is the defect this exists to prevent.
		names := make([]string, 0, len(leaders))
		for _, l := range leaders {
			names = append(names, l.Slug)
			tiedWith[l.ID] = append(tiedWith[l.ID], key)
		}
		sort.Strings(names)
		for _, l := range leaders {
			tiedWith[l.ID] = append([]string{"capability:" + key,
				"tied_with:" + strings.Join(names, ",")}, tiedWith[l.ID]...)
		}
	}
	return leads, tiedWith
}

// matchEqual reports whether two candidates are indistinguishable for a lead.
func matchEqual(a scoreResult, aCand store.ScoutCandidateRow,
	b scoreResult, bCand store.ScoutCandidateRow) bool {
	return a.fit == b.fit && a.coverage == b.coverage && a.matched == b.matched
}

// betterMatch orders two candidates for a lead. Name is the final tiebreak so the
// comparator is total and the result cannot depend on iteration order.
func betterMatch(a scoreResult, aCand store.ScoutCandidateRow,
	b scoreResult, bCand store.ScoutCandidateRow) bool {
	if a.fit != b.fit {
		return a.fit > b.fit
	}
	if a.coverage != b.coverage {
		return a.coverage > b.coverage
	}
	if a.matched != b.matched {
		return a.matched > b.matched
	}
	if aCand.Name != bCand.Name {
		return aCand.Name < bCand.Name
	}
	return aCand.ID < bCand.ID
}
