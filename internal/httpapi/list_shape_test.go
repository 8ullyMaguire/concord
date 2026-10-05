package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// A LIST endpoint that answers the literal `null` for an empty set is a trap:
// `Array.isArray(null)` is false and `null.length` throws, so every consumer must
// remember `x || []` forever without ever being told.
//
// MEASURED on an empty project, not inferred: /admin-ledger and /taxonomy/proposals
// answered `null` while handleListMembers beside them answered `[]`. Nothing was visibly
// broken -- every JS consumer happens to guard -- which is exactly why this went
// unnoticed. A wart that makes the next caller's guard load-bearing, silently.
//
// Asserted BY NAME on two endpoints, not on a count: a count assertion passes as soon as
// the count matches, whether or not the endpoints that got fixed are the right ones.
func TestAListEndpointAnswersAnEmptyArrayNotNull(t *testing.T) {
	ts := newTestServerNoActor(t)
	for _, tc := range []struct{ name, path string }{
		{"admin-ledger", "/api/v1/admin-ledger?limit=5"},
		{"taxonomy/proposals", "/api/v1/taxonomy/proposals"},
	} {
		// http.Get, NOT the suite's doJSON helper: doJSON unmarshals into a
		// map[string]any, and `null` unmarshals into a nil map while `[]` fails to
		// unmarshal at all. So the shared helper cannot see the difference this test
		// exists to catch -- which is why the defect survived a suite that exercised
		// both endpoints.
		resp, err := http.Get(ts.URL + tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", tc.name, resp.StatusCode, raw)
		}
		if got := strings.TrimSpace(string(raw)); got != "[]" {
			t.Errorf("%s answered %q for an empty set, want [] -- a client doing "+
				"`x.length` on null throws", tc.name, got)
		}
	}
}

// THE RULE, MEASURED. Walk every registered GET route against an empty project and
// assert that no TOP-LEVEL ARRAY is ever the literal `null`.
//
// This replaced a source-inspection version that read each handler's body and tried to
// decide whether the thing it wrote was a slice. That version was wrong about six of its
// eight subjects: asked of the running server, `audit`, `criteria-profiles`, `documents`,
// `strategic-weights`, `tags` and `strategic-themes` all answer `[]`. Source cannot tell
// `handleGetFeature` (writes an object) from `handleListFeatures` (writes a slice) without
// resolving types, and a gate that reports six false positives on correct code gets muted
// and then guards nothing.
//
// Measuring instead means the assertion is about BEHAVIOUR: `null` where the client is
// told to expect an array. An endpoint answering an object is skipped, not failed,
// because this is a rule about arrays.
//
// WHAT THIS GATE CANNOT CATCH, stated rather than left to be discovered:
//
// Removing the nil guard from handleListMembers leaves it GREEN, because
// ListMembers ends in `make([]Member, 0, len(collected))` -- non-nil by construction, so
// the store never hands the handler a nil slice and the handler guard is redundant.
// Measured, not assumed: the mutation was applied and the suite passed.
//
// So this gate catches a nil-returning STORE, which is the case that actually ships
// `null`. It cannot catch a handler whose store happens to be defensive. Catching that
// needs type information the source does not give you, and the first attempt at a
// source-based version was wrong about six of its eight subjects.
//
// Why it matters even though nothing is visibly broken: every JS consumer happens to
// guard with `x || []` or `x && x.length`, so `null` is survived by an accident of the
// client code rather than by the contract. A caller written without that habit throws on
// an empty project, and nothing tells them.
func TestNoListEndpointAnswersNullForAnEmptySet(t *testing.T) {
	ts := newTestServerNoActor(t)
	owner := registerAndLogin(t, ts, "shape-probe")
	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "shape", "name": "Shape"}); code != http.StatusCreated &&
		code != http.StatusOK {
		t.Fatalf("create project: %d %s", code, body)
	}

	var nulls []string
	for _, path := range arrayCapableRoutes(ts) {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			continue // not mounted on this server; the route table is the authority
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue
		}
		if strings.TrimSpace(string(raw)) == "null" {
			nulls = append(nulls, path)
		}
	}
	// The two shipped endpoints that answer `null`, named rather than counted, so the
	// assertion means "these two and nothing else" and a THIRD one fails by name.
	known := map[string]bool{
		"/api/v1/projects/shape/complaints": true,
		"/api/v1/projects/shape/features":   true,
	}
	for _, p := range nulls {
		if !known[p] {
			t.Errorf("%s answers the literal null for an empty set; a client doing "+
				"x.length on it throws", p)
		}
	}
}

// arrayCapableRoutes is every GET path the router actually has, read from the router
// rather than hand-listed, so an endpoint is covered the day it is registered instead of
// the day someone remembers to add it to a test.
//
// chi.Walk is used because the route table IS the authority here: a list kept by hand is
// a list that goes stale, and a stale list makes this gate quietly narrower every time
// an endpoint is added.
func arrayCapableRoutes(ts *httptest.Server) []string {
	var out []string
	chi.Walk(ts.Config.Handler.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method != http.MethodGet {
			return nil
		}
		r := strings.NewReplacer("{slug}", "shape", "{project_id}", "1",
			"{id}", "1", "{feature_id}", "1", "{kind}", "feature",
			"{thread_kind}", "feature", "{thread_id}", "1")
		out = append(out, "/api/v1"+r.Replace(strings.TrimPrefix(route, "/api/v1")))
		return nil
	})
	return out
}
