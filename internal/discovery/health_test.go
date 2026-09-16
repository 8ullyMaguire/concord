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
}
