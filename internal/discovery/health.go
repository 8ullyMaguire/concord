// Package discovery implements Concord's first-class search support
// (spec §15): the transparent maintenance-health score and, later, ranking
// of search results. Pure logic only.
package discovery

import "math"

// Metrics are the public signals feeding the health score. All inputs are
// forge-synced facts, never opinions — users can dispute them by filing a
// complaint (spec §15).
type Metrics struct {
	LastCommitAgeDays float64 // 0 = committed today; use a large value if never
	MedianReviewHours float64 // median time to first review on PRs
	Contributors      int     // distinct authors in the last year
	Releases90d       int     // releases/tags in the last 90 days
}

// Weights are the transparent, exposed components of the health score.
// They must sum to 1 — users may reweight their own search (spec §15).
//
// They are *expected* to sum to 1, not required to: the value is a plain
// struct so callers can construct one from user input, and a hand-typed
// `Weights{Recency: 0.5, Responsiveness: 0.5}` sums to 1 by accident rather
// than by intent. Nothing stopped a malformed set from producing a score
// outside [0,1], which would then be written into project_metrics.health_score
// and read back as if it were a normalised score. HealthScore normalises, so
// the invariant is enforced where it is used rather than trusted at the call
// site.
type Weights struct {
	Recency, Responsiveness, Breadth, Cadence float64
}

// DefaultWeights is Concord's neutral weighting.
var DefaultWeights = Weights{Recency: 0.35, Responsiveness: 0.25, Breadth: 0.20, Cadence: 0.20}

// Normalized returns w rescaled so its components sum to 1, and reports
// whether any rescaling was needed.
//
// An all-zero weight set is returned unchanged alongside ok=false: there is no
// meaningful normalisation of "no opinion", and silently substituting the
// defaults would make a caller that passed zeros unable to tell its input was
// rejected. The zero set scores 0 everywhere, which is honest.
func (w Weights) Normalized() (Weights, bool) {
	sum := w.Recency + w.Responsiveness + w.Breadth + w.Cadence
	if sum <= 0 {
		return w, false
	}
	if math.Abs(sum-1.0) < 1e-9 {
		return w, true
	}
	return Weights{
		Recency:        w.Recency / sum,
		Responsiveness: w.Responsiveness / sum,
		Breadth:        w.Breadth / sum,
		Cadence:        w.Cadence / sum,
	}, true
}

// Breakdown exposes each component so search UIs can show *why* a project
// scores what it scores.
type Breakdown struct {
	Recency, Responsiveness, Breadth, Cadence, Total float64
}

// recency: exponential decay with a 30-day half-life on last commit.
func recencyScore(lastCommitAgeDays float64) float64 {
	return math.Pow(0.5, lastCommitAgeDays/30.0)
}

// responsiveness: ≤24h median review → 1.0, ≥14 days → 0.0, log-linear
// between.
func responsivenessScore(medianReviewHours float64) float64 {
	if medianReviewHours <= 24 {
		return 1.0
	}
	if medianReviewHours >= 14*24 {
		return 0.0
	}
	// log-linear from (24h, 1.0) to (336h, 0.0)
	x := math.Log(medianReviewHours/24.0) / math.Log(14.0)
	return math.Max(0.0, 1.0-x)
}

// breadth: 50 distinct contributors → 1.0, log-scaled below that.
func breadthScore(contributors int) float64 {
	if contributors <= 1 {
		return 0.0
	}
	return math.Min(1.0, math.Log10(float64(contributors))/math.Log10(51.0))
}

// cadence: ≥3 releases in 90 days → 1.0, linear below.
func cadenceScore(releases90d int) float64 {
	return math.Min(1.0, float64(releases90d)/3.0)
}

// HealthScore computes the composite maintenance-health score in [0,1].
// Every component is exposed in the returned Breakdown (spec §15:
// "transparent, not vibes").
//
// The weights are normalised first, so a caller that supplied a set not summing
// to 1 gets a score on the same scale as everyone else's rather than one that
// can exceed 1 or collapse toward 0.
func HealthScore(m Metrics, w Weights) Breakdown {
	if n, ok := w.Normalized(); ok {
		w = n
	}
	b := Breakdown{
		Recency:        recencyScore(m.LastCommitAgeDays),
		Responsiveness: responsivenessScore(m.MedianReviewHours),
		Breadth:        breadthScore(m.Contributors),
		Cadence:        cadenceScore(m.Releases90d),
	}
	b.Total = w.Recency*b.Recency +
		w.Responsiveness*b.Responsiveness +
		w.Breadth*b.Breadth +
		w.Cadence*b.Cadence
	return b
}
