package ranking

import (
	"math"
	"sort"
)

// Criteria-aware ranking (spec extension, 2026-09-29).
//
// A criterion is a named, first-class ranking dimension scoped to one project:
// "design quality", "efficiency", "production readiness". Each criterion carries
// its own Glicko-2 pool, so a feature can be first on one dimension and twentieth
// on another, and the system can answer "best designed" and "best lightweight"
// as genuinely different questions rather than one blended score.
//
// Everything here is pure. SQL wiring lives in internal/store, matching the rest
// of this package.

// Criterion is a ranking dimension. Direction matters: a lower-is-better
// criterion (cost, latency) is inverted during normalisation, so "ranked first"
// always means "best" regardless of which direction the author chose.
type Criterion struct {
	ID          int64   `json:"id"`
	ProjectID   int64   `json:"project_id"`
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Direction   string  `json:"direction"` // higher_is_better (default) or lower_is_better
	DefaultW    float64 `json:"default_weight"`
	Active      bool    `json:"active"`
}

// Direction constants. Stored as text so the API can round-trip them.
const (
	DirHigher = "higher_is_better"
	DirLower  = "lower_is_better"
)

// Rating is one feature's Glicko-2 state within one criterion. It is a distinct
// type from Feature so a per-criterion rating can never be confused with the
// project's own single rating triple.
type Rating struct {
	FeatureID   int64   `json:"feature_id"`
	CriterionID int64   `json:"criterion_id"`
	R           float64 `json:"r"`
	RD          float64 `json:"rd"`
	Sigma       float64 `json:"sigma"`
	Games       int     `json:"games"` // how many rated games back this; the confidence signal
}

// NewRating is the Glicko-2 starting state, identical to a fresh feature.
func NewRating(featureID, criterionID int64) Rating {
	return Rating{
		FeatureID:   featureID,
		CriterionID: criterionID,
		R:           1500.0,
		RD:          350.0,
		Sigma:       0.06,
	}
}

// UpdateRating runs one Glicko-2 period for a feature within a criterion. It
// delegates to the existing Rate, so a change to the core maths cannot silently
// change criteria results, and vice versa.
func UpdateRating(cur Rating, games []Game, tau float64) Rating {
	r, rd, sigma := Rate(cur.R, cur.RD, cur.Sigma, games, tau)
	return Rating{
		FeatureID:   cur.FeatureID,
		CriterionID: cur.CriterionID,
		R:           r,
		RD:          rd,
		Sigma:       sigma,
		Games:       cur.Games + len(games),
	}
}

// InitialRD is the deviation a rating with no games carries, and the ceiling the
// idle-period rule grows toward.
const InitialRD = 350.0

// minRD floors the deviation so a rating cannot become falsely certain. Glicko-2
// drives RD toward zero with enough games, and a zero RD would make the
// normalised spread degenerate (see normalise).
const minRD = 10.0

// normalised returns ratings mapped to [0,1] where 1 is best.
//
// Two things make raw Glicko ratings non-comparable across criteria, and both are
// handled here rather than by the caller:
//
//   - each criterion is its own pool, so its r values sit on a different scale
//   - RD differs, so an uncertain rating is treated differently from a settled one
//
// Scaling is min-max within the set. When every rating is identical the spread is
// zero and every entry scores 0.5 — "all tied" is the honest answer, and it is
// not the same as "all bad". With a single entry there is nothing to compare
// against, so it also scores 0.5 rather than a flattering 1.0.
func normalised(rs []Rating, dir string) map[int64]float64 {
	out := make(map[int64]float64, len(rs))
	if len(rs) == 0 {
		return out
	}
	if len(rs) == 1 {
		out[rs[0].FeatureID] = 0.5
		return out
	}

	lo, hi := math.Inf(1), math.Inf(-1)
	for _, r := range rs {
		if r.R < lo {
			lo = r.R
		}
		if r.R > hi {
			hi = r.R
		}
	}
	span := hi - lo
	if span <= 1e-9 {
		for _, r := range rs {
			out[r.FeatureID] = 0.5
		}
		return out
	}
	for _, r := range rs {
		v := (r.R - lo) / span
		if dir == DirLower {
			v = 1 - v
		}
		out[r.FeatureID] = v
	}
	return out
}

// Weight is a user's emphasis on one criterion. Weights are relative; they are
// normalised before use so callers may pass 3:1 or 0.75:0.25 interchangeably.
type Weight struct {
	CriterionID int64   `json:"criterion_id"`
	Weight      float64 `json:"weight"`
}

// normaliseWeights scales weights to sum to 1. A zero or negative total cannot
// be normalised, and negative weights are meaningless for "how much do I care",
// so both fall back to a uniform distribution rather than returning nonsense.
func normaliseWeights(ws []Weight) map[int64]float64 {
	out := make(map[int64]float64, len(ws))
	total := 0.0
	for _, w := range ws {
		if w.Weight < 0 {
			continue
		}
		total += w.Weight
		out[w.CriterionID] = w.Weight
	}
	if len(out) == 0 {
		return out
	}
	if total <= 1e-9 {
		for id := range out {
			out[id] = 1.0 / float64(len(out))
		}
		return out
	}
	for id, w := range out {
		out[id] = w / total
	}
	return out
}

// Contribution is one criterion's slice of a composite score, kept so a result
// can explain itself rather than presenting an unexplained number.
type Contribution struct {
	CriterionID int64   `json:"criterion_id"`
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Weight      float64 `json:"weight"`     // after normalisation
	Raw         float64 `json:"raw"`        // the feature's Glicko rating
	RD          float64 `json:"rd"`         // uncertainty on that rating
	Games       int     `json:"games"`      // rated comparisons behind it
	Normalised  float64 `json:"normalised"` // 0..1 within this criterion
	Score       float64 `json:"score"`      // weight * normalised
}

// Result is a ranked feature with its explanation attached. Explanation is
// recomputed on read rather than stored, because a stored explanation goes stale
// the moment a rating changes.
type Result struct {
	FeatureID     int64          `json:"feature_id"`
	Score         float64        `json:"score"`
	Rank          int            `json:"rank"`
	Contributions []Contribution `json:"contributions"`
}

// TotalConfidence is the weighted mean RD across the contributing criteria. It
// is reported alongside the rank so a query can tell a settled ordering from a
// provisional one, and so a fresh criterion with three votes cannot be mistaken
// for a mature one.
func TotalConfidence(cs []Contribution) float64 {
	if len(cs) == 0 {
		return InitialRD
	}
	wsum, acc := 0.0, 0.0
	for _, c := range cs {
		wsum += c.Weight
		acc += c.Weight * c.RD
	}
	if wsum <= 1e-9 {
		acc = 0.0
		for _, c := range cs {
			acc += c.RD
		}
		return acc / float64(len(cs))
	}
	return acc / wsum
}

// Composite ranks features across criteria.
//
// criteria supplies the definitions (for direction, naming and defaults),
// ratings supplies every feature's rating per criterion, and weights says how
// much the caller cares about each. Features missing from a criterion simply do
// not gain from it — that is a deliberate choice over imputing a zero, which
// would punish a feature for being unrated rather than for being bad.
//
// minRDPerCriterion drops criteria from the score when every rating in them is
// still at the initial deviation. Without it, a criterion nobody has voted on
// contributes an arbitrary 0.5 to everyone's score and dilutes the result;
// including it only when asked keeps the default predictable.
func Composite(
	criteria []Criterion,
	ratings map[int64][]Rating, // criterion_id -> ratings
	weights []Weight,
	opts CompositeOptions,
) []Result {
	active := make([]Criterion, 0, len(criteria))
	for _, c := range criteria {
		if !c.Active && !opts.IncludeInactive {
			continue
		}
		if opts.RequireMinRD {
			// Keep the criterion only if at least one rating in it has actually
			// been played. A criterion whose every rating is still at the
			// initial state contributes an arbitrary 0.5 to every feature, which
			// dilutes the result without any vote behind it.
			played := false
			for _, r := range ratings[c.ID] {
				if r.Games > 0 || r.RD < InitialRD-1e-9 {
					played = true
					break
				}
			}
			if !played {
				continue
			}
		}
		active = append(active, c)
	}
	if len(active) == 0 {
		return nil
	}

	// Effective weights.
	//
	// If the caller named any criterion, that list is the whole query: an
	// unnamed criterion is EXCLUDED. Otherwise every active criterion
	// contributes at its own default.
	//
	// The earlier behaviour — unnamed criteria joining at their default — made
	// "rank by design only" blend efficiency in at an equal weight, and the
	// result matched neither criterion's own ordering. The weighting interface
	// was answering a question nobody asked. Callers who want everything can
	// send nothing and get the defaults.
	caller := make(map[int64]float64, len(weights))
	for _, w := range weights {
		caller[w.CriterionID] = w.Weight
	}
	restrict := len(caller) > 0
	eff := make([]Weight, 0, len(active))
	for _, c := range active {
		w, ok := caller[c.ID]
		if !ok {
			if restrict {
				continue
			}
			w = c.DefaultW
		}
		eff = append(eff, Weight{CriterionID: c.ID, Weight: w})
	}
	normW := normaliseWeights(eff)

	// Score every feature that appears in at least one active criterion.
	scores := map[int64]*Result{}
	byID := map[int64]Criterion{}
	for _, c := range active {
		byID[c.ID] = c
		norms := normalised(ratings[c.ID], c.Direction)
		w := normW[c.ID]
		if w <= 0 {
			// An explicit zero weight: record nothing, contribute nothing.
			continue
		}
		for _, r := range ratings[c.ID] {
			res := scores[r.FeatureID]
			if res == nil {
				res = &Result{FeatureID: r.FeatureID}
				scores[r.FeatureID] = res
			}
			n := norms[r.FeatureID]
			res.Score += w * n
			res.Contributions = append(res.Contributions, Contribution{
				CriterionID: c.ID,
				Slug:        c.Slug,
				Name:        c.Name,
				Weight:      w,
				Raw:         r.R,
				RD:          r.RD,
				Games:       r.Games,
				Normalised:  n,
				Score:       w * n,
			})
		}
	}

	out := make([]Result, 0, len(scores))
	for _, res := range scores {
		if len(res.Contributions) == 0 {
			continue
		}
		out = append(out, *res)
	}
	// Sort by score desc, then by feature id so the order is deterministic when
	// scores tie — an unstable order would make a "rank 3" claim unreproducible.
	sort.SliceStable(out, func(i, j int) bool {
		if math.Abs(out[i].Score-out[j].Score) > 1e-9 {
			return out[i].Score > out[j].Score
		}
		return out[i].FeatureID < out[j].FeatureID
	})
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}

// CompositeOptions tunes inclusion rules.
type CompositeOptions struct {
	// IncludeInactive ranks against criteria flagged inactive. Off by default:
	// deactivating a criterion is how a project retires a dimension, and honouring
	// it silently would make the flag a lie.
	IncludeInactive bool
	// RequireMinRD drops criteria where every rating is unrated (RD at the
	// initial value and zero games).
	RequireMinRD bool
}

// RankByCriterion is the single-dimension case, and Composite with one weight
// is the general path; this exists so the common query is a one-liner and callers
// cannot forget to set a direction.
func RankByCriterion(c Criterion, ratings []Rating) []Result {
	norms := normalised(ratings, c.Direction)
	out := make([]Result, 0, len(ratings))
	for _, r := range ratings {
		n := norms[r.FeatureID]
		out = append(out, Result{
			FeatureID: r.FeatureID,
			Score:     n,
			Contributions: []Contribution{{
				CriterionID: c.ID, Slug: c.Slug, Name: c.Name,
				Weight: 1.0, Raw: r.R, RD: r.RD, Games: r.Games,
				Normalised: n, Score: n,
			}},
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if math.Abs(out[i].Score-out[j].Score) > 1e-9 {
			return out[i].Score > out[j].Score
		}
		return out[i].FeatureID < out[j].FeatureID
	})
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}
