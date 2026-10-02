package ranking

import (
	"math"
	"strings"
)

// §6.4's expertise weighting: "Votes on solutions weight expertise tags more
// heavily (a Rust-heavy design is weighed by Rust-reputed voters), within the
// existing weight caps."
//
// Two properties make this different from the other vote multipliers:
//
//  1. It is TAG-SCOPED (§8.3: "Expertise is tag-scoped"). A Rust expert's vote is
//     worth more on a Rust design and exactly the same as anyone else's on a
//     design with no Rust in it. A global reputation multiplier would make a
//     generalist heavier everywhere, which is not what the spec line says and not
//     what a reviewer expects to happen.
//  2. It is a MULTIPLIER on the existing weight, not a replacement (§6.4 says
//     "within the existing weight caps"). So the charter's VoteWeightCap still
//     binds, and this can never produce a vote heavier than the cap allows.

// ExpertiseMultiplier bounds. §8.3 says expertise is a multiplier and that weight
// is capped; it does not say how far expertise can move a vote. These bounds are
// the judgement call, and they are deliberately narrow in both directions:
//
//   - Below 1.0 would mean a design is worth LESS because nobody who knows the
//     topic judged it. That is not expertise weighting, it is a popularity
//     penalty wearing its clothes, and it would punish exactly the novel designs
//     a forge exists to attract.
//   - Above 2.0 would let a domain expert outvote the entire rest of a project
//     combined, which is plutocracy with a technical justification. §8.3's whole
//     point is "Capped to prevent plutocracy".
const (
	// MinExpertiseMultiplier keeps the floor at 1.0 -- never a penalty.
	MinExpertiseMultiplier = 1.0
	// MaxExpertiseMultiplier is the ceiling. A Rust expert's vote is at most
	// twice a stranger's on a Rust design.
	MaxExpertiseMultiplier = 2.0

	// ExpertiseHalfSaturating is the tag-reputation at which the multiplier
	// reaches 1.5x -- halfway between no weight and the cap. Log-saturating rather
	// than linear so that going from 0 to a real track record matters and the
	// difference between 30 and 300 tagged contributions does not.
	//
	// The two anchors this fixes are therefore: 1.0x at zero, 1.5x at 30, and
	// 2.0x -- the cap -- at 3x that, or 90. That ceiling is reached by a member
	// with roughly eighteen validated contributions in one tag, which is a track
	// record rather than a preference, and the charter's own VoteWeightCap still
	// applies on top (see ExpertiseWeight).
	ExpertiseHalfSaturating = 30.0
)

// ExpertiseMultiplier is §6.4/§8.3's multiplier for judging one solution.
//
// tagReputation is the voter's reputation *within the solution's expertise tags*
// (ParseExpertiseTags reads the solution's list; the store's SolutionExpertise
// computes the per-tag totals). tagsRequired is how many distinct tags the
// solution declares, which is what keeps an empty tag list from becoming a 1.0x
// everybody receives by accident.
//
// Which tags count is BestTagReputation's decision, not this one's.
func ExpertiseMultiplier(tagReputation float64, tagsRequired int) float64 {
	// No declared tags means no claim about what kind of judgement this
	// solution needs, so every voter weighs the same. This is the honest
	// default: an author who declares no expertise has not asked for expert
	// weighting, and inventing it from the solution's title would be reading
	// meaning into text nobody tagged.
	if tagsRequired <= 0 {
		return 1.0
	}
	if tagReputation < 0 {
		tagReputation = 0
	}
	// log1p against the half-saturation constant, normalised by log1p(3) so the
	// three documented anchors hold exactly: 0 reputation is base 0 (1.0x), H is
	// base 0.5 (1.5x), and 3H is base 1 (the 2.0x cap).
	//
	// The constant is inline rather than a named var because there is exactly one
	// use and Go cannot call math.Log1p in a const block; a second copy is the
	// risk a named var would prevent, and there is no second copy.
	base := math.Log1p(tagReputation/ExpertiseHalfSaturating) / math.Log1p(3)

	m := 1.0 + (MaxExpertiseMultiplier-1.0)*base
	if m < MinExpertiseMultiplier {
		return MinExpertiseMultiplier
	}
	if m > MaxExpertiseMultiplier {
		return MaxExpertiseMultiplier
	}
	return m
}

// ParseExpertiseTags splits a solution's comma-separated expertise tags into
// normalised names.
//
// Normalised to lowercase and trimmed because perTagReputation is keyed by
// normalised names, so "Rust" would miss a "rust" key and score the voter zero
// reputation in their own speciality. Duplicates are dropped because "rust,rust"
// is one tag written twice, and the duplicate would otherwise inflate tagsRequired
// and make the multiplier depend on formatting.
//
// An empty tag list is not an error -- it means the author claimed no special
// expertise -- so it returns nil, not a one-element slice of "".
func ParseExpertiseTags(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		t := strings.ToLower(strings.TrimSpace(p))
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ExpertiseWeight is the full §8.3 weight for one vote on one solution: the
// reputation-and-role weight, multiplied by the age gate, then by expertise, then
// capped.
//
// The cap is applied LAST and to the product, which is the only ordering that
// makes "within the existing weight caps" true. Capping before multiplying would
// let a 3.0-capped weight become 6.0 after a 2.0x expertise multiplier, and
// §8.3's cap exists precisely to stop weight from compounding.
func ExpertiseWeight(baseWeight, tagReputation float64, tagsRequired int, cap float64) float64 {
	w := baseWeight * ExpertiseMultiplier(tagReputation, tagsRequired)
	if cap > 0 && w > cap {
		return cap
	}
	return w
}

// BestTagReputation reduces per-tag reputation totals to the single number
// ExpertiseMultiplier takes, by taking the best tag rather than the mean.
//
// Best-tag, not mean, and this is the substantive judgement in this file: a
// reviewer who is a recognised expert in one of the three areas a solution
// touches has real standing to judge it. Averaging would let two unrelated strong
// tags cancel one absent tag and land on a middling multiplier, so breadth
// substitutes for depth. §6.4's own example is singular -- "a Rust-heavy design
// is weighed by Rust-reputed voters" -- and that is the best-tag rule.
//
// tagsRequired is the count of tags the solution declares, used so an untagged
// solution cannot produce a best over zero tags.
func BestTagReputation(perTag map[string]float64, tags []string) (float64, int) {
	if len(tags) == 0 {
		return 0, 0
	}
	best := 0.0
	for _, t := range tags {
		if v, ok := perTag[t]; ok && v > best {
			best = v
		}
	}
	return best, len(tags)
}
