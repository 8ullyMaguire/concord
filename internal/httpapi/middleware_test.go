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
	// Use a fresh rate limiter to avoid pollution from other tests
	origLimiter := limiter
	limiter = &rateLimiter{visitors: make(map[string]*visitorRate)}
	defer func() { limiter = origLimiter }()

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

func TestRateLimitPerVisitor(t *testing.T) {
	// Fresh limiter; each visitor keyed by CF-Connecting-IP behind the
	// loopback proxy must get its own bucket.
	origLimiter := limiter
	limiter = &rateLimiter{visitors: make(map[string]*visitorRate)}
	defer func() { limiter = origLimiter }()

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
