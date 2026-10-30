package discovery

import (
	"math"
	"testing"
)

func TestHealthScoreComponents(t *testing.T) {
	t.Run("fresh, responsive, broad, active repo scores high", func(t *testing.T) {
		b := HealthScore(Metrics{
			LastCommitAgeDays: 0, MedianReviewHours: 12,
			Contributors: 50, Releases90d: 3,
		}, DefaultWeights)
		if b.Total < 0.95 {
			t.Fatalf("total = %.3f, want ≈1.0 for a healthy repo: %+v", b.Total, b)
		}
	})
	t.Run("stale repo scores near zero", func(t *testing.T) {
		b := HealthScore(Metrics{
			LastCommitAgeDays: 365, MedianReviewHours: 24 * 30,
			Contributors: 1, Releases90d: 0,
		}, DefaultWeights)
		if b.Total > 0.05 {
			t.Fatalf("total = %.3f, want ≈0.0 for an abandoned repo: %+v", b.Total, b)
		}
	})
	t.Run("components are exposed", func(t *testing.T) {
		b := HealthScore(Metrics{LastCommitAgeDays: 30, MedianReviewHours: 24, Contributors: 10, Releases90d: 1}, DefaultWeights)
		if b.Recency != 0.5 {
			t.Fatalf("recency = %.3f, want 0.5 at one 30-day halflife", b.Recency)
		}
		if b.Responsiveness != 1.0 {
			t.Fatalf("responsiveness = %.3f, want 1.0 at ≤24h", b.Responsiveness)
		}
		if b.Breadth <= 0 || b.Breadth >= 1 {
			t.Fatalf("breadth = %.3f, want strictly between 0 and 1 for 10 contributors", b.Breadth)
		}
		if b.Cadence <= 0 || b.Cadence >= 1 {
			t.Fatalf("cadence = %.3f, want strictly between 0 and 1 for 1 release", b.Cadence)
		}
	})
	t.Run("total respects weights", func(t *testing.T) {
		m := Metrics{LastCommitAgeDays: 0, MedianReviewHours: 12, Contributors: 50, Releases90d: 3}
		allRecency := Weights{Recency: 1, Responsiveness: 0, Breadth: 0, Cadence: 0}
		b := HealthScore(m, allRecency)
		if math.Abs(b.Total-b.Recency) > 1e-9 {
			t.Fatalf("total %.3f should equal recency %.3f under an all-recency weighting", b.Total, b.Recency)
		}
	})

	// A perfect project under weights that do not sum to 1 must not score
	// outside [0,1]: the raw sum would be 10, and that value is written into
	// project_metrics.health_score and read back as a normalised score.
	t.Run("unnormalised weights still yield a score in [0,1]", func(t *testing.T) {
		perfect := Metrics{LastCommitAgeDays: 0, MedianReviewHours: 1, Contributors: 100, Releases90d: 9}
		for _, w := range []Weights{
			{Recency: 5, Responsiveness: 5, Breadth: 5, Cadence: 5}, // sums to 20
			{Recency: 0.1, Responsiveness: 0.1, Breadth: 0.1, Cadence: 0.1},
			{Recency: -1, Responsiveness: 2, Breadth: 0, Cadence: 0}, // negative component
		} {
			b := HealthScore(perfect, w)
			if b.Total < 0 || b.Total > 1 {
				t.Errorf("weights %+v summed to score %.4f, outside [0,1]", w, b.Total)
			}
		}
	})

	// Scaling every weight by the same factor expresses the same preference, so
	// it must produce the same score.
	t.Run("scaling all weights does not change the score", func(t *testing.T) {
		m := Metrics{LastCommitAgeDays: 14, MedianReviewHours: 72, Contributors: 8, Releases90d: 1}
		base := HealthScore(m, DefaultWeights).Total
		scaled := HealthScore(m, Weights{
			Recency:        DefaultWeights.Recency * 7,
			Responsiveness: DefaultWeights.Responsiveness * 7,
			Breadth:        DefaultWeights.Breadth * 7,
			Cadence:        DefaultWeights.Cadence * 7,
		}).Total
		if math.Abs(base-scaled) > 1e-9 {
			t.Errorf("scaling weights changed the score: %v vs %v", base, scaled)
		}
	})

	t.Run("zero weights are reported as unusable, not silently defaulted", func(t *testing.T) {
		if _, ok := (Weights{}).Normalized(); ok {
			t.Error("an all-zero weight set should report ok=false")
		}
		// DefaultWeights must be a valid, already-normalised set.
		if n, ok := DefaultWeights.Normalized(); !ok || n != DefaultWeights {
			t.Errorf("DefaultWeights should normalise to itself and report ok=true, got %+v ok=%v", n, ok)
		}
	})
}

// UnknownReviewHours is not a duration and must not behave like one. These tests exist because
// the alternative -- scoring an unmeasured responsiveness as 0 hours, i.e. "reviewed instantly"
// -- inflates exactly the projects that have no review process at all, and the first real run
// against the catalog found five of six repositories with no pull requests.

func TestHealthScoreExcludesUnknownResponsiveness(t *testing.T) {
	// Best possible on the three measurable terms, nothing measurable on the fourth.
	m := Metrics{LastCommitAgeDays: 0, MedianReviewHours: UnknownReviewHours, Contributors: 60, Releases90d: 3}
	b := HealthScore(m, DefaultWeights)
	if b.ResponsivenessKnown {
		t.Fatal("ResponsivenessKnown is true for an unknown review time")
	}
	if b.Responsiveness != 0 {
		t.Fatalf("Responsiveness = %.2f, want 0 kept out of the total", b.Responsiveness)
	}
	// Renormalised over recency/breadth/cadence: a perfect project scores 1.0, not 0.75.
	if b.Total < 0.999 {
		t.Fatalf("Total = %.3f, want 1.0: dividing by the full weight sum would cap a project at "+
			"0.75 for data it cannot have", b.Total)
	}
}

func TestHealthScoreCountsAMeasuredBadResponsiveness(t *testing.T) {
	// The control: the same project WITH a measured, terrible review time must score lower.
	// If this passed at 1.0 too, the exclusion above would be hiding a real problem.
	m := Metrics{LastCommitAgeDays: 0, MedianReviewHours: 14 * 24, Contributors: 60, Releases90d: 3}
	b := HealthScore(m, DefaultWeights)
	if !b.ResponsivenessKnown {
		t.Fatal("a stated review time is reported as unknown")
	}
	if b.Total >= 0.999 {
		t.Fatalf("Total = %.3f with a two-week review time; the term is not being applied", b.Total)
	}
	if b.Total >= 0.8 {
		t.Fatalf("Total = %.3f, want the 0.25-weight term to cost about a quarter", b.Total)
	}
}

func TestHealthScoreZeroReviewHoursIsMeasuredAndBest(t *testing.T) {
	// Zero is a MEASUREMENT ("answered within the hour"), and it must stay distinct from the
	// sentinel. Collapsing the two is the bug this sentinel was added to fix.
	b := HealthScore(Metrics{MedianReviewHours: 0, Contributors: 2, Releases90d: 0}, DefaultWeights)
	if !b.ResponsivenessKnown {
		t.Fatal("0 review hours was treated as unknown")
	}
	if b.Responsiveness != 1.0 {
		t.Fatalf("Responsiveness = %.2f for 0 hours, want 1.0", b.Responsiveness)
	}
}

func TestHealthScoreWithNoUsableWeightsReportsZero(t *testing.T) {
	// Only responsiveness carries weight and it is unknown: there is no honest number, and a
	// silent 0 is better than a division by zero or a claimed 1.0.
	b := HealthScore(Metrics{MedianReviewHours: UnknownReviewHours, Contributors: 5},
		Weights{Responsiveness: 1})
	if b.Total != 0 {
		t.Fatalf("Total = %.2f, want 0 when nothing with weight was measured", b.Total)
	}
}
