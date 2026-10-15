package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Three mutating routes had NO guard at all, and two of them were verified against the
// live instance before this file existed.
//
// `PUT /projects/{slug}/languages` and `PUT /projects/{slug}/metrics` are registered
// beside `PUT /tags`, which calls `requireWriteActor` + `requireRole`, and `PUT
// /visibility`, which resolves the actor and checks membership. The two that were left
// out answer 200 to an anonymous caller: measured on the running instance,
//
//	$ curl -X PUT .../projects/test-project/languages -d '{"languages":[...]}' -> 200
//	$ curl -X PUT .../projects/test-project/metrics   -d '{"stars":999999,...}' -> 200
//
// and `project_languages` gained the row and `project_metrics`'s stars became 999999.
//
// `POST /complaints/{source_id}/merge/{target_id}` is the same shape and worse: it
// merges two complaints by id, it is destructive, and it never looks at the actor.
//
// These tests were written as FAILING tests first. The question is not "can I add a
// guard" but "was the hole real", and a test that goes green only after the fix proves
// nothing about what the fix fixed.

func TestProjectLanguagesCannotBeSetByAnAnonymousCaller(t *testing.T) {
	ts := newTestServer(t)
	createTestProject(t, ts, "lang-guard")

	code, body := authJSON(t, ts, http.MethodPut, "/api/v1/projects/lang-guard/languages",
		anonymousMarker, map[string]any{
			"languages": []map[string]any{{"language": "AnonymousOwned", "pct": 100}},
		})
	if code != http.StatusUnauthorized {
		t.Errorf("an anonymous PUT /languages answered %d, want 401; body=%s", code, body)
	}
}

func TestProjectMetricsCannotBeSetByAnAnonymousCaller(t *testing.T) {
	ts := newTestServer(t)
	createTestProject(t, ts, "metrics-guard")

	code, body := authJSON(t, ts, http.MethodPut, "/api/v1/projects/metrics-guard/metrics",
		anonymousMarker, map[string]any{
			"stars": 999999, "forks": 0, "open_issues": 0, "commit_count": 0,
			"contributors": 0, "last_commit_age_days": 0, "median_review_hours": 0,
			"releases_90d": 0, "license": "MIT",
		})
	if code != http.StatusUnauthorized {
		t.Errorf("an anonymous PUT /metrics answered %d, want 401; body=%s", code, body)
	}
}

// The merge is the sharpest of the three: it takes two complaint ids and joins them, so
// an anonymous caller could destroy the distinction between two reported problems.
func TestMergingComplaintsRequiresAnActor(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	createTestProject(t, ts, "merge-guard")

	var pid, uid int64
	if err := st.QueryRow(`SELECT id FROM projects WHERE slug='merge-guard'`).Scan(&pid); err != nil {
		t.Fatalf("project id: %v", err)
	}
	if err := st.QueryRow(`SELECT id FROM users WHERE username='testuser'`).Scan(&uid); err != nil {
		t.Fatalf("owner id: %v", err)
	}
	var source, target int64
	for i, title := range []string{"the pain", "the other pain"} {
		// The columns are the schema's, not the report's: `complaints` has severity and
		// frequency and NO `pain` column -- pain is computed from severity, frequency
		// and age, which is why the first version of this fixture failed with
		// "table complaints has no column named pain".
		res, err := st.ExecContext(t.Context(),
			`INSERT INTO complaints (project_id, author_id, title, body, status,
			 created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
			pid, uid, title, "body", "open", 1790000000.0+float64(i), 1790000000.0+float64(i))
		if err != nil {
			t.Fatalf("insert complaint: %v", err)
		}
		id, _ := res.LastInsertId()
		if i == 0 {
			source = id
		} else {
			target = id
		}
	}

	// THE ROUTE IS PROJECT-SCOPED. The first version of this test posted to
	// /api/v1/complaints/{id}/merge/{target} -- a path that does not exist, so the
	// assertion "an anonymous merge answered 404" was reading chi's NotFound and the
	// test would have passed against a route with no guard at all. The route walk in
	// TestEveryMutatingRouteIsGuarded is what named the real path.
	path := "/api/v1/projects/merge-guard/complaints/" + itoa(source) + "/merge/" + itoa(target)

	code, body := authJSON(t, ts, http.MethodPost, path, anonymousMarker, nil)
	if code != http.StatusUnauthorized {
		t.Errorf("an anonymous merge answered %d, want 401; body=%s", code, body)
	}

	// POSITIVE HALF: the merge has to work for somebody, or "it is guarded" is a claim
	// about a route nothing can use. The endpoint was ALSO broken -- it read a path
	// parameter named source_id while the router registers {id}, so every call resolved
	// the source to 0 and answered 404 for as long as it existed.
	// doJSON, not authJSON with "": "" means ANONYMOUS in authJSON's helper (isAnon), and
	// the privileged caller is the default token only doJSON sends. The first version of
	// this half asserted the owner could not merge either.
	resp, pbody := doJSON(t, ts, http.MethodPost, path, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the project owner's merge answered %d, want 200; body=%v", resp.StatusCode, pbody)
	}
	var merged *int64
	if err := st.QueryRow(`SELECT merged_into FROM complaints WHERE id=?`, source).Scan(&merged); err != nil {
		t.Fatalf("read merged_into: %v", err)
	}
	if merged == nil || *merged != target {
		t.Errorf("merged_into = %v, want %d: the endpoint answered 200 without merging",
			merged, target)
	}
}

// THE CLASS GATE. The three holes above are one defect -- a mutating route registered
// without a guard -- and reading the handlers is how they were missed the first time:
// two of them sit in the same route group as two guarded neighbours, which is exactly
// what a reader skimming a group stops noticing.
//
// So this walks the ROUTE TABLE (the authority: a new route is covered the day it is
// registered) and resolves each handler to its source by reflected name, rather than
// re-deriving paths and handler names from the source text. Reading source is how the
// null-gate's four attempts produced six false positives.
//
// The allowlist is explicit and short, and every entry says why it can be public. A gate
// whose allowlist is long enough to be quiet is a gate people route around.
func TestEveryMutatingRouteIsGuarded(t *testing.T) {
	publicRoutes := map[string]string{
		"POST /api/v1/auth/register":      "creating an account cannot require one",
		"POST /api/v1/auth/login":         "obtaining a session cannot require one",
		"POST /api/v1/auth/logout":        "revokes only the token the caller already holds",
		"POST /api/v1/hooks/forgejo":      "authenticated by HMAC over the body, and fails closed",
		"POST /api/v1/auth/redeem-invite": "the invite IS the credential",
		"POST /api/v1/finder/sessions":    "§4.1: the finder works without an account",
		// The session id is the credential, and it is a random token, not a row id --
		// so these cannot be enumerated and cannot touch another visitor's session.
		"POST /api/v1/finder/sessions/{session_id}/answers": "operates on the caller's own session id",
		"POST /api/v1/finder/sessions/{session_id}/back":    "operates on the caller's own session id",
		"POST /api/v1/finder/sessions/{session_id}/lift":    "operates on the caller's own session id",
	}

	ts := newTestServerNoActor(t)
	bodies := handlerBodies(t)

	guards := []string{"requireWriteActor", "requireRole", "requireProjectID", "requireAuth",
		"projectReadable", "getActorID(r)", "validateHMAC"}

	var seen int
	var unguarded []string
	_ = chi.Walk(ts.Config.Handler.(chi.Routes), func(method, route string, h http.Handler,
		_ ...func(http.Handler) http.Handler) error {
		if method == "GET" || method == "HEAD" || method == "OPTIONS" {
			return nil
		}
		name := handlerName(h)
		// A MOUNT, not a handler: the asset tree answers every method through chi's
		// file server, so the walk sees CONNECT/DELETE/PATCH on /assets/* with no
		// handle* function behind them. Skipped by NAME, because "the handler is not a
		// method on Server" is the actual property, and a path allowlist would have to
		// grow with every mount.
		if !strings.HasPrefix(name, "handle") {
			return nil
		}
		seen++
		key := method + " " + strings.TrimSuffix(route, "/")
		if _, ok := publicRoutes[key]; ok {
			return nil
		}
		body, ok := bodies[name]
		if !ok {
			unguarded = append(unguarded, key+"  ("+name+": body not found)")
			return nil
		}
		for _, g := range guards {
			if strings.Contains(body, g) {
				return nil
			}
		}
		unguarded = append(unguarded, key+"  ("+name+")")
		return nil
	})

	if seen < 40 {
		t.Fatalf("the walk found %d mutating routes; the walk is broken, not the routes", seen)
	}
	// WHAT IT CANNOT CATCH, stated rather than discovered later:
	//
	//   * A guard call that is present but WRONG. `requireRole(otherProjectID, ...)` or a
	//     role floor of "user" satisfies the string match. Only the behavioural tests
	//     cover that, which is why each fixed route has one.
	//   * A guard call that is UNREACHABLE -- inside an `if false`, or after an early
	//     return. The string is there.
	//
	// Measured, not assumed: deleting `requireWriteActor` from handleMergeComplaints
	// leaves every test in this file GREEN, because `requireRole` returns ErrAuth for an
	// actor of 0 and the call below it refuses the same caller. That mutation removes a
	// redundant line rather than creating a hole, so the survival is correct -- and it is
	// exactly why a SURVIVED line is a statement about the mutant first. The case this
	// gate exists for is the one measured on the live instance: a handler with NO guard
	// call at all, which is what languages, metrics and the merge all were.
	if len(unguarded) > 0 {
		sort.Strings(unguarded)
		t.Errorf("mutating routes whose handler contains no guard call:\n  %s\n\n"+
			"Either add the guard (requireWriteActor + requireRole for a project-scoped "+
			"write) or add the route to publicRoutes with the reason it may be public.",
			strings.Join(unguarded, "\n  "))
	}
}

// handlerName returns "handleX" for a handler registered on the router. A bare method
// value prints as ".../httpapi.(*Server).handleX-fm"; a plain func as "...httpapi.handleX".
func handlerName(h http.Handler) string {
	f := runtime.FuncForPC(reflect.ValueOf(h).Pointer())
	if f == nil {
		return "?"
	}
	name := f.Name()
	name = strings.TrimSuffix(name, "-fm")
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// handlerBodies reads every handler body out of the package's non-test sources, keyed by
// the bare function name.
func handlerBodies(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "internal", "httpapi")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	out := map[string]string{}
	re := regexp.MustCompile(`func \(s \*Server\) (handle[A-Za-z0-9_]+)\(`)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		text, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		s := string(text)
		for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
			name := s[m[2]:m[3]]
			start := m[0]
			next := strings.Index(s[start+1:], "\nfunc ")
			end := len(s)
			if next >= 0 {
				end = start + 1 + next
			}
			out[name] = s[start:end]
		}
	}
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(dir)) // internal/httpapi -> repo root
}
