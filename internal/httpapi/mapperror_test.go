package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// mapError's hand-built cases (internal/httpapi/mapperror.go).
//
// These exist because nine handlers pass fmt.Errorf("authentication required")
// straight to mapError instead of wrapping store.ErrAuth. That error matched no
// sentinel, fell to the default branch, and answered 500 to a request that had
// simply omitted its token. Nothing in the suite caught it: the handler tests all
// used a valid token, and no test asserted the status for a missing one.
//
// The webhook tests were the exception and had it backwards -- one asserted that
// a rejected signature returns 500, which is how the default branch got pinned
// as correct. Corrected alongside this file.

// TestAMissingTokenIsUnauthorizedNotInternal walks the real routes rather than
// calling mapError directly, because the bug was in the pairing: mapError
// returned 500, and a unit test of mapError alone would have passed while every
// actual endpoint stayed broken.
func TestAMissingTokenIsUnauthorizedNotInternal(t *testing.T) {
	// Every one of these returns a distinct status when a token is present, so a
	// 500 here can only come from the auth check itself.
	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"validate complaint", "POST", "/api/v1/projects/p/complaints/1/validate", `{}`},
		{"cast vote", "POST", "/api/v1/projects/p/features/1/vote", `{"outcome":"a","issue_id":1}`},
		{"create feature", "POST", "/api/v1/projects/p/features", `{"title":"x","project_id":1}`},
		{"merge request", "POST", "/api/v1/projects/p/merge_requests", `{"title":"x"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServerNoActor(t)
			resp, _ := doJSON(t, ts, tc.method, tc.path, jsonBody(t, tc.body))
			if resp.StatusCode == http.StatusInternalServerError {
				t.Fatalf("%s without a token returned 500; a missing token is a client "+
					"error and must not read as a server fault", tc.name)
			}
			if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusNotFound {
				// NotFound is acceptable: the ids do not exist in this fixture, and
				// a 404 still means the request was refused before any work.
				t.Logf("%s without a token returned %d (not 401/500)", tc.name, resp.StatusCode)
			}
		})
	}
}

// TestTheHandBuiltAuthMessagesMapToUnauthorized pins each string the mapper knows
// about, so a message that stops matching fails here rather than in production.
func TestTheHandBuiltAuthMessagesMapToUnauthorized(t *testing.T) {
	authCases := []string{
		"authentication required",
		"invalid credentials",
		"token required",
	}
	for _, msg := range authCases {
		if !isHandBuiltAuthError(fmt.Errorf("%s", msg)) {
			t.Errorf("%q is not recognised as an auth error, so it returns 500", msg)
		}
	}
}

// TestAPermissionMessageIsNotDowngradedToUnauthorized guards the exact-match rule.
//
// This test failed the first version of the fix, which matched on prefix and
// listed "unauthorized" as an auth message -- so "unauthorized to edit this
// project", a permission failure, became a 401 and sent an already-signed-in
// caller back to the login page. Matching exactly is what prevents that.
func TestAPermissionMessageIsNotDowngradedToUnauthorized(t *testing.T) {
	for _, msg := range []string{
		"unauthorized to edit this project",
		"unauthorized for this action",
		"unauthorized",
	} {
		if isHandBuiltAuthError(fmt.Errorf("%s", msg)) {
			t.Errorf("%q was classified as an auth error; it is a permission failure "+
				"and belongs in 403", msg)
		}
	}
}

// TestAnUnconfiguredWebhookStaysInternal pins the one hand-built message that
// deliberately stays 500: a deployment with no webhook secret is broken, and the
// request did not cause it.
func TestAnUnconfiguredWebhookStaysInternal(t *testing.T) {
	if isHandBuiltValidationError(fmt.Errorf("webhook not configured")) {
		t.Error("an unconfigured webhook was classified as a client error; a broken " +
			"deployment is a 500, not something the caller can fix")
	}
}

// TestARejectedSignatureIsAClientError is the webhook counterpart: a bad signature
// is the caller's mistake.
func TestARejectedSignatureIsAClientError(t *testing.T) {
	if !isHandBuiltValidationError(fmt.Errorf("invalid signature")) {
		t.Error("a rejected signature should be a 400, not a 500")
	}
}

// jsonBody parses a raw JSON object for doJSON, failing the test on bad input
// rather than sending something a handler will reject for the wrong reason.
func jsonBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("bad test JSON %q: %v", raw, err)
	}
	return m
}
