package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
		} else if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 on request %d, got %d", i, resp.StatusCode)
		}
	}
}
