package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSecurityHeaders(t *testing.T) {
	handler := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("expected X-Content-Type-Options: nosniff")
	}
	if resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Error("expected X-Frame-Options: DENY")
	}
}

func TestRateLimit(t *testing.T) {
	// Use a fresh rate limiter to avoid pollution from other tests, and restore
	// the production budget: TestMain raises maxRequests for the rest of the
	// suite, so without this the 101st request would still be 200.
	origLimiter := limiter
	limiter = &rateLimiter{visitors: make(map[string]*visitorRate)}
	origMax := maxRequests
	maxRequests = func() int { return defaultMaxRequests }
	defer func() {
		limiter = origLimiter
		maxRequests = origMax
	}()

	handler := rateLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	for i := 0; i < 101; i++ {
		resp, err := http.Get(ts.URL)
		if err != nil {
			t.Fatalf("GET %d: %v", i, err)
		}
		if i == 100 {
			if resp.StatusCode != http.StatusTooManyRequests {
				t.Errorf("expected 429 on 101st request, got %d", resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("expected JSON content type on 429, got %q", ct)
			}
		} else if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 on request %d, got %d", i, resp.StatusCode)
		}
	}
}

func TestStaticAssetsAreExemptFromTheRateLimit(t *testing.T) {
	// The limiter's worst case was a blank page. A visitor loading ~13 pages in a minute
	// exhausts the budget, and when the request that overflows is an ASSET it answered
	// JSON with `Content-Type: application/json` -- which a browser REFUSES to execute as
	// a script, so the page rendered nothing at all: no error state, no content, and one
	// console line saying the MIME type was wrong. This is the assertion that the asset
	// keeps being served after the budget is spent, because a rate limiter that can blank
	// a page is worse than the flood it prevents.
	origLimiter := limiter
	limiter = &rateLimiter{visitors: make(map[string]*visitorRate)}
	origMax := maxRequests
	maxRequests = func() int { return 2 }
	defer func() {
		limiter = origLimiter
		maxRequests = origMax
	}()

	handler := rateLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	asset := ts.URL + "/assets/js/consensus.js"
	api := ts.URL + "/api/v1/healthz"

	// Spend the budget on the API, which is what the limit is for.
	for i := 0; i < 3; i++ {
		resp, err := http.Get(api)
		if err != nil {
			t.Fatalf("API GET %d: %v", i, err)
		}
		if i >= 2 && resp.StatusCode != http.StatusTooManyRequests {
			t.Errorf("API request %d = %d, want 429: the budget was not spent", i, resp.StatusCode)
		}
	}
	// The asset is served anyway, ten times over.
	for i := 0; i < 10; i++ {
		resp, err := http.Get(asset)
		if err != nil {
			t.Fatalf("asset GET %d: %v", i, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("asset request %d after the budget was spent = %d, want 200: "+
				"a 429 here is a JSON body the browser refuses to run as a script",
				i, resp.StatusCode)
		}
	}
}

func TestRateLimitPerVisitor(t *testing.T) {
	// Fresh limiter; each visitor keyed by CF-Connecting-IP behind the
	// loopback proxy must get its own bucket.
	origLimiter := limiter
	limiter = &rateLimiter{visitors: make(map[string]*visitorRate)}
	origMax := maxRequests
	maxRequests = func() int { return defaultMaxRequests }
	defer func() {
		limiter = origLimiter
		maxRequests = origMax
	}()

	handler := rateLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	get := func(visitorIP string) int {
		req, _ := http.NewRequest("GET", ts.URL, nil)
		req.Header.Set("CF-Connecting-IP", visitorIP)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET as %s: %v", visitorIP, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// Visitor A exhausts its bucket (limit is 100/min).
	for i := 0; i < 100; i++ {
		if code := get("203.0.113.10"); code != http.StatusOK {
			t.Fatalf("visitor A request %d: expected 200, got %d", i, code)
		}
	}
	// Visitor A is limited; visitor B is untouched.
	if code := get("203.0.113.10"); code != http.StatusTooManyRequests {
		t.Errorf("visitor A: expected 429, got %d", code)
	}
	if code := get("203.0.113.11"); code != http.StatusOK {
		t.Errorf("visitor B: expected 200, got %d", code)
	}

	// Idle-bucket pruning keeps the map bounded.
	limiter.mu.Lock()
	n := len(limiter.visitors)
	limiter.mu.Unlock()
	if n == 0 {
		t.Error("expected non-empty visitors map before prune")
	}
	limiter.pruneIdle(time.Now().Add(10*time.Minute), 5*time.Minute)
	limiter.mu.Lock()
	n = len(limiter.visitors)
	limiter.mu.Unlock()
	if n != 0 {
		t.Errorf("expected empty visitors map after pruning idle buckets, got %d", n)
	}
}
