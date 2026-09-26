package httpapi

import (
	"os"
	"sync"
	"testing"
)

// TestMain disables the rate limiter for the suite, then restores it for the
// one test that is specifically about limiting.
//
// Why this is necessary: limiter is package-global, keyed on client IP, and
// every test in this package arrives from 127.0.0.1. The budget is shared, so
// the suite has a hard ceiling of 100 requests per minute no matter how many
// tests exist. Adding the authentication suite pushed it past that ceiling and
// produced 429s in unrelated tests — failures caused by the shape of the test
// binary rather than by any code under test.
//
// The limit itself is not removed. TestRateLimitEnforced below sets a small
// budget explicitly and proves the middleware still rejects over-limit callers,
// so a change that disabled rate limiting would fail the suite.
func TestMain(m *testing.M) {
	prev := maxRequests
	maxRequests = func() int { return 1_000_000 }
	// Each test server gets its own limiter state; a fresh process starts clean
	// but parallel servers would otherwise share one budget.
	resetLimiter()

	code := m.Run()

	maxRequests = prev
	os.Exit(code)
}

var resetLimiterOnce sync.Once

// resetLimiter clears the shared visitor map.
func resetLimiter() {
	limiter.mu.Lock()
	limiter.visitors = make(map[string]*visitorRate)
	limiter.mu.Unlock()
}

// TestRateLimitEnforced keeps the production behaviour covered: a caller over
// the budget is rejected with 429. It is the counterpart to TestMain's
// override — the suite as a whole is unlimited, and this one test is not.
func TestRateLimitEnforced(t *testing.T) {
	ts := newAuthServer(t)

	prev := maxRequests
	maxRequests = func() int { return 5 }
	t.Cleanup(func() {
		maxRequests = prev
		resetLimiter()
	})
	resetLimiter()

	// Burn the budget, then confirm the next request is refused.
	var last int
	for i := 0; i < 12; i++ {
		last = getWith(t, ts, "/api/v1/projects").StatusCode
	}
	if last != 429 {
		t.Fatalf("after exceeding the budget the last request got %d, want 429", last)
	}
}
