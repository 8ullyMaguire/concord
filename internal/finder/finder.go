// Package finder is Concord's Akinator-style discovery engine.
//
// It takes candidate attributes and returns questions and rankings. It holds no
// store pointer and reads no database: everything it needs arrives as plain
// structs. That is a deliberate constraint, not tidiness -- the properties that
// matter here (unknown never removes a candidate, a constant dimension is never
// asked, skip and doesn't-matter never filter) are the ones most likely to be
// quietly broken by a query change, and they are only testable without one.
//
// The engine's honesty rule, from the Finder spec §2.7: **unknown is not no.**
// A candidate whose capability value nobody has asserted is KEPT and demoted,
// never removed. A Finder that quietly dropped candidates for missing data would
// look identical to one that had found the right answer, which is the failure
// this whole package exists to prevent.
package finder

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Answer modes. Three of the four do not filter at all, and the distinction
// between them is carried all the way to the UI so the engine can tell "do not
// ask me again" (doesn't-matter) from "do not filter on this, ever" (skip).
const (
	ModeRequired     = "required"
	ModeSkip         = "skip"
	ModeDoesntMatter = "doesnt-matter"
	ModeDecideLater  = "decide-later"
)

// Filtering modes: the two that actually remove candidates.
const (
	ModeYes   = "yes"
	ModeNo    = "no"
	ModeAny   = "any"
	ModePart  = "partial"
	ModeOff   = "off"
)

// Dimension families. Used for the diversity rule and for grouping the question
// catalog in the API response.
const (
	FamilyCapability = "capability"
	FamilyPlatform   = "platform"
	FamilyGovernance = "governance"
	FamilyLicense    = "license"
	FamilyMaturity   = "maturity"
)

// Tunables. Named, and their reasons recorded, because each is a decision a
// reader would otherwise have to reverse-engineer from the arithmetic.
const (
	// MinGainBits is the entropy below which a question is not worth asking.
	//
	// 0.35 bits is roughly a 78/22 split. Below that the answer excludes too few
	// candidates to be worth the user's attention, and Finder's promise is that
	// every question earns its place -- so a near-constant dimension is refused
	// by arithmetic rather than by a maintainer's list of forbidden questions.
	MinGainBits = 0.35

	// MaxQuestions is the default cap. §2.8 of the spec: "a hard cap of ~10".
	MaxQuestions = 10

	// SaturationCount: at or below this many candidates, stop and show them.
	SaturationCount = 5

	// HighConfidence and ConfidenceMargin together decide the "we've narrowed it
	// down" prompt. Both are required: a high absolute score is useless when the
	// runner-up is level with it, so the top candidate must ALSO beat #2 by a
	// margin. 1.15 rather than 1.0 because two candidates within 15% are not a
	// finding, they are a pair.
	HighConfidence   = 0.85
	ConfidenceMargin = 1.15

	// UnknownPenalty is applied per unknown field to a candidate's fit.
	//
	// It demotes rather than removes, and it is small enough that a strongly
	// matching candidate with one gap outranks a weak match with none. That is
	// deliberate: absence of data is weaker evidence than presence of it, but it
	// is not evidence of absence.
	UnknownPenalty = 0.05
)

// Weights for the fit score. Every one is returned in the API response so the
// results page can explain a rank without the client holding a second copy of
// these numbers -- two copies drift.
const (
	WeightCapability  = 0.35
	WeightPlatform    = 0.15
	WeightGovernance  = 0.05
	WeightFieldReport = 0.25
	WeightArena       = 0.20
)

// ErrNoQuestion is returned when no dimension clears MinGainBits. It is not a
// failure: it is the engine saying the catalog cannot narrow this set further,
// and the caller should show the gaps instead.
var ErrNoQuestion = errors.New("no question worth asking")

// Option is one answer to a question, with the number of candidates it would
// remove. Impact is computed from the live candidate set, never estimated.
type Option struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Impact int    `json:"impact"`
}

// Dimension is a question the engine can ask about the candidate set.
type Dimension struct {
	Key     string   `json:"key"`   // "cap:wip-limits" | "language" | "governance"
	Family  string   `json:"family"`
	Label   string   `json:"label"`
	Kind    string   `json:"kind"`   // "single" | "tri-state"
	Options []Option `json:"options"`
	// GainBits is the expected information gain of asking this dimension now.
	GainBits float64 `json:"gain_bits"`
	// KnownFraction is how much of the candidate set has a real value here.
	// A dimension that is 94% unknown produces mostly-unknown answers, which is
	// why the selector deprioritises it even when its entropy looks healthy.
	KnownFraction float64 `json:"known_fraction"`
}

// Candidate is one project the engine can rank.
type Candidate struct {
	ProjectID int64
	Slug      string
	Name      string
	// Attrs maps a dimension key to its value. A MISSING key means nobody has
	// asserted it, which is distinct from a key present with the value "unknown"
	// -- the difference finder-spec §3.1 exists to preserve.
	Attrs map[string]string
	// Report is the reputation-weighted field-report outcome rate, 0..1.
	Report float64
	// Reports is the sample size behind Report. Zero means "no reports", which
	// is different from "reports say it did not work".
	Reports int
	// ArenaR is the arena standing, normalised to 0..1. Zero when no arena
	// entries exist -- and COMPUTED, not hardcoded, so it starts working when
	// R5's alternatives arena lands with no code change.
	ArenaR float64
}

// Answer is one user response.
type Answer struct {
	DimensionKey string `json:"dimension_key"`
	OptionID     string `json:"option_id"`
	Mode         string `json:"mode"`
}

// Ranked is a scored candidate with the reasoning that produced the score.
type Ranked struct {
	Candidate
	Fit        float64            `json:"fit"`
	Matches    map[string]string  `json:"matches"`
	Warnings   map[string]string  `json:"warnings"`
	Unknown    []string           `json:"unknown"`
	Explanation map[string]float64 `json:"explanation"`
}

// StopReason explains why Finder stopped asking.
const (
	StopSaturated      = "SATURATED"
	StopHighConfidence = "HIGH_CONFIDENCE"
	StopLowGain        = "LOW_GAIN"
	StopQuestionCap    = "QUESTION_CAP"
	StopNoMatches      = "NO_MATCHES"
)

// State is the engine's input for one question.
type State struct {
	Candidates []Candidate
	Answers    []Answer
	Asked      map[string]bool
	// Depth is how many questions have been answered, for the question cap.
	Depth int
	// LastFamily is the family of the previous question, for the diversity rule.
	LastFamily string
}

// Gain returns the Shannon entropy of a value distribution, in bits.
//
// A single distinct value has 0 bits, which is the whole mechanism behind "never
// ask a question the answer cannot change".
func Gain(counts []int) float64 {
	total := 0
	for _, c := range counts {
		if c < 0 {
			continue
		}
		total += c
	}
	if total <= 1 {
		return 0
	}
	h := 0.0
	for _, c := range counts {
		if c <= 0 {
			continue
		}
		p := float64(c) / float64(total)
		h -= p * math.Log2(p)
	}
	return h
}

// countsByValue tallies a dimension's values across the candidate set.
// A candidate with no value at all is tallied under UnknownValue, so the
// distribution includes what is missing rather than quietly shrinking the
// denominator -- otherwise a dimension asserted on 2 of 70 candidates would look
// like a clean 50/50 split.
// countsByValue buckets candidates by a dimension's value.
//
// All four ways of being unusable collapse into ONE UnknownValue bucket:
// an absent attribute, a blank value, the explicit "__unknown__" sentinel, and
// the literal "unknown" the capability store records when somebody has said
// "nobody knows" (store.CapUnknown). The last one was missing, and it is the
// one that matters on real data: every Findable-capability assertion the
// capableseed tool writes uses "unknown", so Gaps() counted zero unknown fields
// for a catalog where most capability data was unknown -- the contribution loop
// that §5.3 promises had nothing to offer, on an instance whose entire problem
// is missing capability data.
//
// Scoring already handled the literal (line 538 checks storeUnknownWord), which
// is why the ranking looked right and the gap report was empty: two places
// enumerated the ways to be unknown and one of them was short.
func countsByValue(cands []Candidate, key string) (map[string]int, int) {
	counts := map[string]int{}
	known := 0
	for _, c := range cands {
		v, ok := c.Attrs[key]
		if !ok || strings.TrimSpace(v) == "" ||
			v == UnknownValue || v == storeUnknownWord() {
			counts[UnknownValue]++
			continue
		}
		counts[v]++
		known++
	}
	return counts, known
}

// UnknownValue is the bucket for a dimension nobody has a value for.
const UnknownValue = "__unknown__"

// buildDimension derives a question from one dimension's observed distribution.
//
// Options are only the values actually present in the candidate set, plus
// "any". Finder spec §5.4: "Finder never asks about an option the catalog can't
// actually deliver." Offering a value no candidate has would be a question whose
// every answer is equivalent.
func buildDimension(key, family, label string, counts map[string]int, known, total int) Dimension {
	d := Dimension{
		Key:      key,
		Family:   family,
		Label:    label,
		Kind:     "single",
		GainBits: Gain(mapValues(counts)),
	}
	if total > 0 {
		d.KnownFraction = float64(known) / float64(total)
	}

	// Sorted for a stable question: two identical candidate sets must produce
	// byte-identical questions, or a session resumed from a share URL reorders
	// its own options.
	keys := sortedKeys(counts)
	for _, v := range keys {
		if v == UnknownValue {
			continue // an unknown is not an answer a user can pick
		}
		d.Options = append(d.Options, Option{
			ID:     v,
			Label:  v,
			Impact: total - counts[v],
		})
	}
	d.Options = append(d.Options, Option{ID: ModeAny, Label: "Doesn't matter", Impact: 0})
	return d
}

func mapValues(m map[string]int) []int {
	out := make([]int, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// NextQuestion picks the dimension whose answer most reduces uncertainty about
// the candidate set, or returns ErrNoQuestion when nothing clears MinGainBits.
//
// The scoring is expected information gain, adjusted by three things the plain
// entropy formula does not know:
//
//   - catalog confidence: a dimension most of the set has no value for produces
//     mostly-unknown answers and a mostly-unknown short-list, so it is scaled
//     down. Entropy alone would rank a 94%-unknown dimension as excellent.
//   - question quality: small-cardinality questions early, so the user is not
//     asked to rank eight options at question one.
//   - diversity: two consecutive questions from the same family is a
//     conversation about one topic, which reads as a quiz.
func NextQuestion(st State) (*Dimension, float64, error) {
	// Refuse an empty set explicitly. This is provably equivalent to falling
	// through the loop below with no candidates -- candidateDimensionKeys
	// returns nothing and `best` stays nil -- so no test can distinguish the
	// two, and a mutation gate run against this line reports a survivor that is
	// not a gap. It is kept because it states the intent at the point a caller
	// reads it, and because "no candidates" is a caller-visible state (the
	// NO_MATCHES stop reason) rather than an accident of iteration.
	if len(st.Candidates) == 0 {
		return nil, 0, ErrNoQuestion
	}

	var (
		best  *Dimension
		bestG float64
	)

	for _, key := range candidateDimensionKeys(st.Candidates) {
		if st.Asked[key] {
			continue
		}
		family, label := classify(key)

		// A dimension that was already asked and then lifted is re-derivable,
		// but a dimension that is constant across the current set cannot be
		// answered helpfully by anyone.
		counts, known := countsByValue(st.Candidates, key)
		d := buildDimension(key, family, label, counts, known, len(st.Candidates))
		if d.GainBits < MinGainBits {
			continue
		}

		score := d.GainBits

		// Catalog confidence. Never zero, so a fully-unknown dimension cannot
		// outrank a real one even when its entropy looks high.
		score *= 0.5 + 0.5*d.KnownFraction

		// Question quality: prefer small-cardinality early.
		if d.KnownFraction > 0 && len(d.Options) > 4 && st.Depth < 3 {
			score *= 0.9
		}

		// Diversity: same family twice running is a quiz.
		if st.LastFamily != "" && family == st.LastFamily {
			score *= 0.7
		}

		if score > bestG {
			d := d
			best = &d
			bestG = score
		}
	}

	if best == nil {
		return nil, 0, ErrNoQuestion
	}
	return best, best.GainBits, nil
}

// candidateDimensionKeys returns every dimension key any candidate carries, in a
// stable order.
//
// Derived from the data rather than from a list of known dimensions, which is
// spec §2.2: "Questions are selected dynamically from the data, not from a
// hardcoded script. If the catalog changes, the questions change."
func candidateDimensionKeys(cands []Candidate) []string {
	seen := map[string]bool{}
	for _, c := range cands {
		for k := range c.Attrs {
			seen[k] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// AllDimensions returns every dimension derivable from the candidate set, with
// its live gain -- including the ones too weak to ask about.
//
// The question selector picks one; this is what the catalog endpoint shows, and
// the reason it exists as a separate call rather than a debug flag on
// NextQuestion: "Finder never asks about an option the catalog can't actually
// deliver" (§5.4) is otherwise only checkable by driving the whole UI, and the
// dimensions that were NOT offered are the ones worth inspecting -- they are the
// instance's gaps.
func AllDimensions(st State) []Dimension {
	out := []Dimension{}
	if len(st.Candidates) == 0 {
		return out
	}
	for _, key := range candidateDimensionKeys(st.Candidates) {
		if st.Asked[key] {
			continue
		}
		family, label := classify(key)
		counts, known := countsByValue(st.Candidates, key)
		out = append(out, buildDimension(key, family, label, counts, known, len(st.Candidates)))
	}
	return out
}

// classify assigns a family and a human label to a dimension key.
//
// The prefix is the contract: "cap:" is a capability from the matrix, everything
// else is a project attribute. A new capability needs no change here.
func classify(key string) (family, label string) {
	if strings.HasPrefix(key, "cap:") {
		bare := strings.TrimPrefix(key, "cap:")
		return FamilyCapability, strings.ReplaceAll(bare, "-", " ")
	}
	switch key {
	case "language":
		return FamilyPlatform, "Programming language"
	case "governance":
		return FamilyGovernance, "How is it governed?"
	case "license":
		return FamilyLicense, "License"
	case "maturity":
		return FamilyMaturity, "Maturity"
	default:
		return FamilyCapability, key
	}
}

// ShouldStop reports whether to stop asking, and why. Both a high score AND a
// margin over the runner-up are required for HIGH_CONFIDENCE: a top candidate at
// 0.99 with #2 at 0.98 has not been found, it has been tied.
func ShouldStop(st State) (bool, string) {
	if len(st.Candidates) == 0 {
		return true, StopNoMatches
	}
	if len(st.Candidates) <= SaturationCount {
		return true, StopSaturated
	}
	if st.Depth >= MaxQuestions {
		return true, StopQuestionCap
	}

	ranked := Score(st.Candidates, st.Answers)
	if len(ranked) >= 2 {
		top, second := ranked[0].Fit, ranked[1].Fit
		if top >= HighConfidence && second > 0 && top/second >= ConfidenceMargin {
			return true, StopHighConfidence
		}
	}

	if _, _, err := NextQuestion(st); errors.Is(err, ErrNoQuestion) {
		return true, StopLowGain
	}
	return false, ""
}

// Gap describes a dimension that WOULD have discriminated the candidate set but
// cannot, because nobody has filled in the data.
//
// This is the honest end of a Finder session. The catalog cannot answer more
// questions, and a Finder that stops with "here are your results" would be
// claiming a precision it does not have. A Gap says: these are the questions we
// cannot ask, and each one is a contribution someone could make.
type Gap struct {
	Key           string `json:"key"`
	Label         string `json:"label"`
	Family        string `json:"family"`
	UnknownFields int    `json:"unknown_fields"`
	HeldBack      int    `json:"held_back"`
}

// Gaps returns the dimensions that were not asked because they are constant or
// nearly so across the candidate set, most useful first.
//
// "HeldBack" is the number of candidates whose unknown value on that dimension
// is what stops the question from being askable -- i.e. what filling the gap
// would unblock.
func Gaps(st State) []Gap {
	if len(st.Candidates) == 0 {
		return nil
	}
	out := []Gap{}
	for _, key := range candidateDimensionKeys(st.Candidates) {
		if st.Asked[key] {
			continue
		}
		counts, _ := countsByValue(st.Candidates, key)
		family, label := classify(key)
		// countsByValue returns (distribution, known-count). The unknown count
		// is the distribution's own bucket, read back from it rather than
		// recomputed: two ways to count the same thing is how a number starts
		// disagreeing with itself.
		// A gap is UNKNOWN DATA, not "cannot be asked about". Those are
		// independent: a dimension can split perfectly (a great question) and
		// still have 60% of its candidates unknown, and those 60% are exactly
		// what §5.3 wants contributed.
		//
		// This used to `continue` when the dimension was askable, which made
		// the live contribution loop appear to work for the wrong reason --
		// cap:wip-limits showed up because its gain was 0.295, not because
		// anything was unknown about it -- while a dimension that split badly
		// AND happened to be fully known was reported as a gap with zero
		// unknown fields.
		unknown := counts[UnknownValue]
		if unknown == 0 {
			continue // nothing to contribute here
		}
		out = append(out, Gap{
			Key:           key,
			Label:         label,
			Family:        family,
			UnknownFields: unknown,
			HeldBack:      unknown,
		})
	}
	// Most-blocking first, then by name so the list is stable.
	sort.Slice(out, func(i, j int) bool {
		if out[i].HeldBack != out[j].HeldBack {
			return out[i].HeldBack > out[j].HeldBack
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// Apply narrows a candidate set by an answer, returning the survivors and how
// many were removed per reason.
//
// The unknown rule lives here and nowhere else: a required answer removes a
// candidate whose value is "no", and KEEPS one whose value is absent or
// "unknown". A candidate kept for want of data is not a lesser answer to the
// question, it is an unanswered one.
func Apply(cands []Candidate, a Answer) (kept []Candidate, removedBy map[string]int) {
	removedBy = map[string]int{}

	// Every non-filtering mode returns the set untouched. This is the shape of
	// the promise in §2.4: "Skipping never removes candidates."
	switch a.Mode {
	case ModeSkip, ModeDoesntMatter, ModeDecideLater, "":
		return append([]Candidate(nil), cands...), removedBy
	}

	for _, c := range cands {
		v, present := c.Attrs[a.DimensionKey]
		switch {
		case a.OptionID == ModeAny:
			kept = append(kept, c)
		case !present || v == "" || v == UnknownValue || v == storeUnknownWord():
			// Unknown keeps. §2.7.
			kept = append(kept, c)
		case v == a.OptionID:
			kept = append(kept, c)
		default:
			removedBy[a.DimensionKey]++
		}
	}
	return kept, removedBy
}

// storeUnknownWord is the value the store records for "somebody said nobody
// knows". It is duplicated here rather than imported so this package stays free
// of the store, and it is checked in three places because getting it wrong means
// dropping candidates whose data is merely missing.
func storeUnknownWord() string { return "unknown" }

// Score ranks candidates. It never removes anything.
//
// Score is a weighted sum over the terms in finder-spec §5.2, each normalised to
// 0..1 and each reported in Explanation so a reader can see which term produced
// a rank rather than being handed a number.
func Score(cands []Candidate, answers []Answer) []Ranked {
	// The required answers decide matches; the decide-later ones decide bonuses.
	//
	// Both are keyed by dimension to the option the user picked. A bool map for
	// preferred would lose which option was wanted, and "I would like offline"
	// has to be checkable against a candidate's actual value to be worth
	// anything.
	required := map[string]string{}
	preferred := map[string]string{}
	for _, a := range answers {
		switch a.Mode {
		case ModeRequired:
			required[a.DimensionKey] = a.OptionID
		case ModeDecideLater, ModeAny:
			preferred[a.DimensionKey] = a.OptionID
		}
	}

	// A no-answer counts as a weak match: it is a real signal about the
	// candidate, just not one the user asked to filter on.
	capKeys := make([]string, 0, len(required)+len(preferred))
	for k := range required {
		if strings.HasPrefix(k, "cap:") {
			capKeys = append(capKeys, k)
		}
	}
	for k := range preferred {
		if strings.HasPrefix(k, "cap:") && !contains(capKeys, k) {
			capKeys = append(capKeys, k)
		}
	}
	sort.Strings(capKeys)

	out := make([]Ranked, 0, len(cands))
	for _, c := range cands {
		r := Ranked{
			Candidate:   c,
			Matches:     map[string]string{},
			Warnings:    map[string]string{},
			Explanation: map[string]float64{},
			Unknown:     []string{},
		}

		// Capability term.
		capTotal, capHit := 0.0, 0.0
		for _, k := range capKeys {
			// capTotal counts what the user ASKED, not what this candidate
			// happens to have. An unknown field still increments it.
			//
			// This ordering matters: an unknown branch used to `continue` before
			// capTotal++, so a candidate with no capability data at all had
			// capTotal == 0 -- and the weighted sum then left out
			// WeightCapability for it entirely. The candidate with the LEAST data
			// lost the weight that carries its unknown penalty, so its demotion
			// silently evaluated to zero. The rank order was still correct, which
			// is why only the penalty assertions caught it.
			v, present := c.Attrs[k]
			capTotal++
			if !present || v == "" || v == UnknownValue || v == storeUnknownWord() {
				r.Unknown = append(r.Unknown, k)
				continue
			}

			if want, asked := required[k]; asked {
				switch v {
				case want:
					capHit++
					r.Matches[k] = v
				case "no":
					// A positive "no" against a requirement is a miss, not a
					// partial: half credit here would rank a project that
					// explicitly lacks the thing above one that was never asked.
					r.Warnings[k] = "does not have it"
				default:
					// Present but a different value (partial where yes was
					// wanted, or the other way round). Partial credit.
					capHit += 0.5
					r.Matches[k] = v
				}
				continue
			}

			// A decide-later preference. This is a separate decision from a
			// requirement, and it has to be scored as one: matching the
			// preference scores, not matching it does not.
			//
			// The first version credited EVERY candidate with a known value,
			// which made a soft preference a flat +0.5 that reordered nothing --
			// "I'll decide later about offline" had no effect on the ranking at
			// all, while looking like it did. Found by a test asserting the
			// preference actually reorders.
			if want, asked := preferred[k]; asked && want != ModeAny {
				if v == want {
					capHit++
					r.Matches[k] = v
				} else {
					r.Matches[k] = v
				}
				continue
			}
			// "Doesn't matter" on a preference: the candidate having the value
			// is mildly good, having nothing recorded is mildly bad, and
			// neither is worth much.
			capHit += 0.5
			r.Matches[k] = v
		}
		capScore := 0.0
		if capTotal > 0 {
			capScore = capHit / capTotal
		}
		r.Explanation["capability"] = capScore

		// Platform / governance / license / maturity term.
		structScore := 0.0
		structTotal := 0
		for _, k := range []string{"language", "governance", "license", "maturity"} {
			want, asked := required[k]
			if !asked {
				continue
			}
			v, present := c.Attrs[k]
			// Same rule as capTotal: the denominator is what was asked.
			structTotal++
			if !present || v == "" {
				r.Unknown = append(r.Unknown, k)
				continue
			}
			if v == want {
				structScore++
				r.Matches[k] = v
			} else {
				r.Warnings[k] = v
			}
		}
		platform := 0.0
		if structTotal > 0 {
			platform = structScore / float64(structTotal)
		}
		r.Explanation["platform"] = platform

		// Governance is a real but weak signal, so it has its own small weight
		// rather than being folded into the platform term where a "no match"
		// would read as strongly as a missing language.
		gov := 0.0
		if want, asked := required["governance"]; asked {
			if v, present := c.Attrs["governance"]; present && v != "" {
				if v == want {
					gov = 1
					r.Matches["governance"] = v
				} else {
					gov = 0
					r.Warnings["governance"] = v
				}
			}
		}
		r.Explanation["governance"] = gov

		// Field report rate, and whether there is one at all. Zero reports is
		// not zero quality, so it is EXCLUDED from the weighted sum below
		// rather than entering it as a zero.
		report := c.Report
		haveReport := c.Reports > 0
		if !haveReport {
			r.Unknown = append(r.Unknown, "field-reports")
		}
		r.Explanation["field_report"] = report

		// Arena standing, and whether there is one. Same treatment: no arena
		// that ranks projects is absence of evidence, not a score of zero.
		haveArena := c.ArenaR > 0
		r.Explanation["arena"] = c.ArenaR

		// WEIGHTED SUM RENORMALISED OVER AVAILABLE EVIDENCE.
		//
		// This used to be a plain weighted sum, and on the live instance it
		// labelled a project that matched EVERY answer 15% fit -- because the
		// two terms there was no evidence for (field reports, arena standing)
		// contributed 0.25 + 0.20 of pure absence to the total. The rank order
		// was still right, but the number told the user a perfect match was
		// bad, which is the opposite of what §4.5 shows them.
		//
		// Missing evidence now leaves both the numerator and the denominator,
		// so absence can no longer drag a candidate down. What the user still
		// needs to know is how thin the evidence was, and that is reported
		// separately as coverage rather than hidden in a deflated percentage.
		// A term whose denominator is zero is a term for evidence that does not
		// exist, so it leaves the weighted sum entirely -- not just the
		// numerator. capScore is 0 whenever the user asked no capability
		// questions, yet WeightCapability stayed in `den`, so a candidate that
		// matched every question it was asked scored 0.65 instead of 1.0:
		// 0.35 of the total was a term for questions nobody asked. Same defect
		// as the arena term, one layer up.
		num, den := 0.0, 0.0
		if capTotal > 0 {
			num += WeightCapability * capScore
			den += WeightCapability
		}
		if structTotal > 0 {
			num += WeightPlatform * platform
			den += WeightPlatform
		}
		if _, asked := required["governance"]; asked {
			if v, present := c.Attrs["governance"]; present && v != "" {
				num += WeightGovernance * gov
				den += WeightGovernance
			}
		}
		if haveReport {
			num += WeightFieldReport * report
			den += WeightFieldReport
		}
		if haveArena {
			num += WeightArena * c.ArenaR
			den += WeightArena
		}
		// No evidence at all cannot produce a number; 0 is the only honest
		// value, and it is reachable only if nothing is known about anything.
		fit := 0.0
		if den > 0 {
			fit = num / den
		}

		// Soft preference bonus for the structured dimensions a user deferred:
		// matching a stated preference adds a little, having it recorded as
		// lacking does not subtract (an unknown is not a no).
		//
		// Capability preferences are scored inside the capability term above,
		// so they are deliberately NOT counted again here — a double-count is
		// how a nice-to-have quietly becomes worth more than a requirement.
		bonus := 0.0
		for k, want := range preferred {
			if strings.HasPrefix(k, "cap:") {
				continue
			}
			if want == ModeAny {
				continue
			}
			if v, present := c.Attrs[k]; present && v != "" {
				if v == want {
					bonus += 0.05
					r.Matches[k] = v
				} else {
					r.Matches[k] = v
				}
			} else {
				r.Unknown = append(r.Unknown, k)
			}
		}
		r.Explanation["soft_preference"] = bonus

		// Coverage: the share of total weight that had evidence behind it. This
		// is what makes a non-100% fit legible -- "100% of what we know, and we
		// know 36% of it" is a different statement from "36% fit", and the user
		// needs both to tell a well-evidenced match from a lucky one.
		//
		// Computed before the penalty because the penalty scales by it.
		coverage := 0.0
		totalWeight := WeightCapability + WeightPlatform + WeightGovernance + WeightFieldReport + WeightArena
		if totalWeight > 0 {
			coverage = den / totalWeight
		}
		r.Explanation["evidence_coverage"] = coverage

		// Unknown penalty: demote, never drop. Scaled BY COVERAGE, because the
		// renormalised sum has already priced missing evidence -- an unknown
		// field keeps its term out of both the numerator and the denominator.
		// Subtracting a flat UnknownPenalty on top charged for the same missing
		// field twice, and did it in absolute units, so a large penalty could
		// outrun the signal it was meant to modulate.
		penalty := -UnknownPenalty * float64(len(r.Unknown)) * coverage
		r.Explanation["unknown_penalty"] = penalty

		fit += bonus
		fit += penalty

		if fit < 0 {
			fit = 0
		}
		r.Fit = fit
		out = append(out, r)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Fit != out[j].Fit {
			return out[i].Fit > out[j].Fit
		}
		// Deterministic tiebreak, so a shared results URL renders identically
		// for everyone and a ranking cannot reshuffle between two loads.
		if out[i].Slug != out[j].Slug {
			return out[i].Slug < out[j].Slug
		}
		return out[i].ProjectID < out[j].ProjectID
	})
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// WeightReport renders the weights for the "why this rank?" panel, so the client
// does not hold a second copy that can drift from these.
func WeightReport() map[string]float64 {
	return map[string]float64{
		"capability":      WeightCapability,
		"platform":        WeightPlatform,
		"governance":      WeightGovernance,
		"field_report":    WeightFieldReport,
		"arena":           WeightArena,
		"unknown_penalty": UnknownPenalty,
	}
}

// ValidateCandidateSet is the guard the HTTP layer needs: a candidate set with no
// candidates is not an error condition to be papered over with an empty result,
// it is the NO_MATCHES stop reason.
func ValidateCandidateSet(cands []Candidate) error {
	if len(cands) == 0 {
		return fmt.Errorf("%w: candidate set is empty", ErrNoQuestion)
	}
	return nil
}