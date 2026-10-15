package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/embed"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// decodeBody turns a raw response body into the map getJSON would have returned.
func decodeBody(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return m
}

// Duplicate detection at filing time, at the API boundary (§6.1).
//
// The store-level tests prove the scoring. These prove the workflow: that the
// panel is reachable before submitting, that a strong duplicate is reported
// rather than silently accepted, and -- the one that matters -- that a filer who
// disagrees with the score can still file.

// withEmbedder builds a server whose store has the real embedder configured.
// The other harnesses leave it unset, which is the "not configured" state the
// endpoint reports honestly rather than faking an empty result.
func withEmbedder(t *testing.T) (*httptest.Server, *store.DB) {
	t.Helper()
	ts, st := newTestServerWithStore(t)
	st.SetEmbedder(embed.NewHashed())
	return ts, st
}

func TestSimilarEndpointIsReachableBeforeSubmitting(t *testing.T) {
	ts, _ := withEmbedder(t)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"dupwork","name":"DupWork","description":"d"}`)

	// Read-only and unauthenticated: a panel that needs a token is a panel nobody
	// sees, and the point is to show matches while the filer is still typing.
	resp, body := getJSON(t, ts,
		`/api/v1/similar/complaint?text=Search+results+are+identical+for+every+query`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("similar: status %d body=%v", resp.StatusCode, body)
	}
	if available, _ := body["available"].(bool); !available {
		t.Fatalf("endpoint reports unavailable with an embedder configured: %v", body)
	}
	if _, ok := body["thresholds"]; !ok {
		t.Error("response has no thresholds: a client cannot know what a score means")
	}
}

func TestSimilarRejectsAnUnknownKind(t *testing.T) {
	ts, _ := withEmbedder(t)
	resp, _ := getJSON(t, ts, "/api/v1/similar/nonsense?text=hello")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown kind: status %d, want 400", resp.StatusCode)
	}
}

func TestSimilarRequiresText(t *testing.T) {
	ts, _ := withEmbedder(t)
	resp, _ := getJSON(t, ts, "/api/v1/similar/complaint")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing text: status %d, want 400", resp.StatusCode)
	}
}

func TestReportsUnavailableRatherThanEmptyWhenNotConfigured(t *testing.T) {
	// An instance with no embedder must say so. An empty list would read as
	// "nothing similar exists", which is a false claim rather than an admission.
	ts, st := newTestServerWithStore(t)
	_ = st

	resp, body := getJSON(t, ts, "/api/v1/similar/complaint?text=anything")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200 (unavailable is a state, not an error)", resp.StatusCode)
	}
	if available, present := body["available"]; !present || available == true {
		t.Errorf("available = %v, want explicitly false", body["available"])
	}
}

func TestFilingAnExactDuplicateIsReportedNotSilentlyAccepted(t *testing.T) {
	const slug = "dupwork"
	ts, _ := withEmbedder(t)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"gate","name":"Gate","description":"d"}`)

	const title = "Search results are identical for every query"
	resp, body := postJSON(t, ts, "/api/v1/projects/"+slug+"/complaints", `{
		"project_id": 1,
		"title": "`+title+`",
		"body": "bm25 is not applied to the project index",
		"severity": 3}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first complaint: %d %v", resp.StatusCode, body)
	}

	// The same complaint again: refused, with the existing one named.
	resp, body = postJSON(t, ts, "/api/v1/projects/"+slug+"/complaints", `{
		"project_id": 1,
		"title": "`+title+`",
		"body": "I hit this too, every query returns the same rows",
		"severity": 2}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate filing: status %d, want 409 (body=%v)", resp.StatusCode, body)
	}
	sims, _ := body["similar"].([]any)
	if len(sims) == 0 {
		t.Fatalf("409 carries no candidates: the filer cannot see what they collided with (%v)", body)
	}
	first, _ := sims[0].(map[string]any)
	if first["title"] != title {
		t.Errorf("top candidate title = %v, want %q", first["title"], title)
	}
	// The escape hatch has to be discoverable from the error itself.
	if _, ok := body["resubmit_with"]; !ok {
		t.Error("409 does not say how to proceed after a false positive")
	}
}

func TestFilerCanProceedPastAFalsePositive(t *testing.T) {
	const slug = "override"
	// The asymmetry that sets the whole threshold policy: a duplicate in the queue
	// costs maintainer time, a lost complaint costs the report itself.
	ts, _ := withEmbedder(t)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"override","name":"Override","description":"d"}`)

	const title = "Search results are identical for every query"
	postJSON(t, ts, "/api/v1/projects/"+slug+"/complaints", `{
		"project_id": 1, "title": "`+title+`", "body": "first", "severity": 3}`)

	resp, body := postJSON(t, ts, "/api/v1/projects/"+slug+"/complaints", `{
		"project_id": 1, "title": "`+title+`", "body": "genuinely different root cause",
		"severity": 3, "confirm_duplicate": true}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("override: status %d body=%v, want 201 -- a filer must always be able to proceed", resp.StatusCode, body)
	}

	// The existing complaint is untouched: confirming does not overwrite it.
	// The existing complaint is untouched: confirming does not overwrite or
	// remove it. Listed under the project slug, since that is the route.
	resp, body = getJSON(t, ts, "/api/v1/projects/"+slug+"/complaints")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %v", resp.StatusCode, body)
	}
	items, _ := body["items"].([]any)
	if n := len(items); n != 2 {
		t.Errorf("after an override there are %d complaints, want 2 (the original must survive)", n)
	}
}

func TestUnrelatedComplaintIsAcceptedWithNoWarning(t *testing.T) {
	const slug = "nofalse"
	// The false-positive test at the API boundary. Every one of these is a
	// different problem, filed against a project that already has other
	// complaints, and each must go through untouched.
	ts, _ := withEmbedder(t)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"nofalse","name":"NoFalse","description":"d"}`)

	existing := []string{
		"No authentication existed, so the site was read-only for everyone",
		"Project-scoped routes silently operated on project 0",
		"Anyone can hijack a ranking by voting on their own proposal",
	}
	for _, t2 := range existing {
		resp, body := postJSON(t, ts, "/api/v1/projects/"+slug+"/complaints", `{
			"project_id": 1, "title": "`+t2+`", "body": "detail", "severity": 2}`)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("seeding %q: %d %v", t2, resp.StatusCode, body)
		}
	}

	fresh := []string{
		"Deleting a comment leaves no row in the audit log",
		"The changelog does not list contributors in order",
		"Sign-up fails silently with a generic error message",
		"Every page renders before the data has arrived",
	}
	for _, t2 := range fresh {
		resp, body := postJSON(t, ts, "/api/v1/projects/"+slug+"/complaints", `{
			"project_id": 1, "title": "`+t2+`", "body": "detail", "severity": 2}`)
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("unrelated complaint %q was refused with %d: %v",
				t2, resp.StatusCode, body)
		}
	}
}

func TestFilingWorksWhenDuplicateDetectionIsNotConfigured(t *testing.T) {
	const slug = "noconf"
	// An unconfigured instance must not block filing. Refusing on a missing
	// embedder would make duplicate detection a dependency of the whole product
	// rather than an enhancement of it.
	ts, _ := newTestServerWithStore(t)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"noconf","name":"NoConf","description":"d"}`)

	resp, body := postJSON(t, ts, "/api/v1/projects/"+slug+"/complaints", `{
		"project_id": 1, "title": "A perfectly normal complaint",
		"body": "detail", "severity": 2}`)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("filing without an embedder: %d %v, want 201", resp.StatusCode, body)
	}
}

func TestSimilarityResponseCarriesCoverage(t *testing.T) {
	const slug = "cov"
	// A filer told "no duplicates" on a corpus that is 12% indexed has been told
	// nothing useful, and this is the only place they can find out.
	ts, _ := withEmbedder(t)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"cov","name":"Cov","description":"d"}`)
	postJSON(t, ts, "/api/v1/projects/"+slug+"/complaints", `{
		"project_id": 1, "title": "something about caching", "body": "b", "severity": 1}`)

	resp, body := getJSON(t, ts, "/api/v1/similar/complaint?text=caching+behaviour+is+wrong")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	cov, ok := body["coverage"].(map[string]any)
	if !ok {
		t.Fatalf("no coverage in %v", body)
	}
	// The freshly filed complaint was indexed by the create path.
	if embedded, _ := cov["embedded"].(float64); embedded < 1 {
		t.Errorf("coverage reports %v embedded after filing one complaint, want >= 1", cov["embedded"])
	}
}

// TestFilingChecksForDuplicatesOutsideTheCurrentProject is the filing-path half of
// §6.1's "also searches other projects".
//
// TestCrossProjectSearchFindsTheSameProblemElsewhere already covers the /similar
// endpoint, and it passed while the filing path was still project-scoped: filing a
// verbatim copy of a complaint from another project returned 201 with no top_score,
// because checkDuplicates passed projectID instead of 0. Two tests, one requirement,
// and the one that passed tested the wrong caller.
func TestFilingChecksForDuplicatesOutsideTheCurrentProject(t *testing.T) {
	ts, _ := withEmbedder(t)
	postJSON(t, ts, "/api/v1/projects", `{"slug":"origin","name":"Origin","description":"d"}`)
	postJSON(t, ts, "/api/v1/projects", `{"slug":"elsewhere","name":"Elsewhere","description":"d"}`)

	const title = "The exporter silently drops the final row"
	resp, body := postJSON(t, ts, "/api/v1/projects/origin/complaints",
		`{"project_id":1,"title":"`+title+`","body":"every export loses one","severity":4}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("file in origin: %d %v", resp.StatusCode, body)
	}

	// Same text, different project. Identical text scores 1.0, so a working
	// cross-project scope must refuse this outright.
	resp, body = postJSON(t, ts, "/api/v1/projects/elsewhere/complaints",
		`{"project_id":2,"title":"`+title+`","body":"a different context entirely","severity":4}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("a verbatim copy filed in another project was accepted (%d); the filing "+
			"path is still project-scoped", resp.StatusCode)
	}
	if score, _ := body["top_score"].(float64); score < 0.85 {
		t.Errorf("top_score = %v, want >= 0.85 for identical text across projects", body["top_score"])
	}

	// The refusal must be confirmable, and the confirmation must be the escape
	// hatch: "this is the same bug but it also affects you" is a legitimate filing.
	resp, body = postJSON(t, ts, "/api/v1/projects/elsewhere/complaints",
		`{"project_id":2,"title":"`+title+`","body":"a different context entirely","severity":4,`+
			`"confirm_duplicate":true}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("confirm_duplicate should let the filing through: %d %v", resp.StatusCode, body)
	}
}

func TestCrossProjectSearchFindsTheSameProblemElsewhere(t *testing.T) {
	// §6.1: duplicate detection "also searches other projects". A project-scoped
	// search must not leak, and the instance-wide one must find it.
	ts, _ := withEmbedder(t)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"projone","name":"One","description":"d"}`)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"projtwo","name":"Two","description":"d"}`)

	const slug2 = "projtwo"
	_ = slug2
	resp, body := postJSON(t, ts, "/api/v1/projects/projtwo/complaints", `{
		"project_id": 2,
		"title": "Search results are identical for every query",
		"body": "reported against the second project", "severity": 3}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("file: %d %v", resp.StatusCode, body)
	}

	// Instance-wide: found.
	resp, body = getJSON(t, ts,
		"/api/v1/similar/complaint?text=Search+results+are+identical+for+every+query")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("instance-wide: %d", resp.StatusCode)
	}
	sims, _ := body["similar"].([]any)
	if len(sims) == 0 {
		t.Error("instance-wide search found nothing: the cross-project case is broken")
	}

	// Scoped to project 1: not found, because it lives in project 2.
	projOne, _ := getJSON(t, ts, "/api/v1/projects/projone")
	_ = projOne
	resp, body = getJSON(t, ts,
		"/api/v1/similar/complaint?text=Search+results+are+identical+for+every+query&project_id=1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scoped: %d", resp.StatusCode)
	}
	if sims, _ := body["similar"].([]any); len(sims) != 0 {
		t.Errorf("project-scoped search leaked %d matches from another project", len(sims))
	}
}

// ---------------------------------------------------------------- §6.1's leaks
//
// Two defects the /similar endpoint shipped with, both found by asking of a
// field the endpoint RETURNS what a reader does with it. Neither was reachable
// by any earlier test, because every one of them asserted that the response was
// shaped right rather than that it was usable.

// similarMatches pulls the match objects out of a /similar response body.
func similarMatches(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["similar"].([]any)
	if !ok {
		t.Fatalf("no similar array in %v", body)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		if mm, ok := m.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

// TestEverySimilarityMatchCarriesALinkThatResolves.
//
// hydrateSimilarities used to build `/api/v1/features/222`,
// `/api/v1/complaints/9`, `/api/v1/requests/4`. None of those routes exist. The
// live service answered
//
//	$ curl -s .../api/v1/similar/feature/?text=... | jq -r '.similar[0].url'
//	/api/v1/features/222
//	$ curl -s -o /dev/null -w '%{http_code}' .../api/v1/features/222
//	404
//
// for every match it returned, and the store test that covers this asserted
// `sims[0].URL != ""`. A non-empty string is not a link, and the panel this
// endpoint exists for had not been built yet, so nothing ever followed one.
//
// So this follows one. For each kind: seed the entity, find it through the API,
// take the URL the API handed out, and FETCH IT. Execution, not string shape.
func TestEverySimilarityMatchCarriesALinkThatResolves(t *testing.T) {
	// "" is anonymous on this harness (authJSON reads it as the X-No-Auth
	// opt-out), so every write here needs a real token. That is the harness
	// working as documented: passing "" is the LEAST privileged thing a test can
	// do, for authJSON, and the first version of this test filed 401s because it
	// read the getJSON convention across.
	type seed func(t *testing.T, ts *httptest.Server, tok string) string
	cases := []struct {
		kind string
		seed seed
	}{
		{"complaint", func(t *testing.T, ts *httptest.Server, tok string) string {
			code, body := authJSON(t, ts, http.MethodPost,
				"/api/v1/projects/linktest/complaints", tok,
				map[string]any{"project_id": 1, "title": "The exporter drops the final row",
					"body": "one row short", "severity": 4})
			if code != http.StatusCreated {
				t.Fatalf("file complaint: %d %s", code, body)
			}
			return "The exporter drops the final row"
		}},
		{"feature", func(t *testing.T, ts *httptest.Server, tok string) string {
			code, body := authJSON(t, ts, http.MethodPost,
				"/api/v1/projects/linktest/complaints", tok,
				map[string]any{"project_id": 1, "title": "Exports lose the last row",
					"body": "one row short", "severity": 4})
			if code != http.StatusCreated {
				t.Fatalf("file complaint: %d %s", code, body)
			}
			// §6.2: a feature must link to at least one VALIDATED complaint. The
			// first version of this seed skipped this and got a 400, which is the
			// schema catching a fixture that was asserting a state the product
			// forbids -- the same lesson as §3.3 of the tracker.
			if code, v := authJSON(t, ts, http.MethodPost,
				"/api/v1/projects/linktest/complaints/1/validate", tok,
				map[string]any{}); code != http.StatusOK {
				t.Fatalf("validate complaint: %d %s", code, v)
			}
			code, body = authJSON(t, ts, http.MethodPost,
				"/api/v1/projects/linktest/features", tok,
				map[string]any{"project_id": 1, "title": "Resumable uploads survive a drop",
					"body": "the design", "linked_complaints": []int64{1}})
			if code != http.StatusCreated {
				t.Fatalf("propose feature: %d %s", code, body)
			}
			return "Resumable uploads survive a drop"
		}},
		{"request", func(t *testing.T, ts *httptest.Server, tok string) string {
			code, body := authJSON(t, ts, http.MethodPost,
				"/api/v1/projects/linktest/requests", tok,
				map[string]any{"project_id": 1, "title": "Could the board show a countdown",
					"body": "asking for a feature"})
			if code != http.StatusCreated {
				t.Fatalf("open request: %d %s", code, body)
			}
			return "Could the board show a countdown"
		}},
	}

	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			ts, _ := withEmbedder(t)
			tok := registerAndLogin(t, ts, "linktest-owner")
			if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", tok,
				map[string]any{"slug": "linktest", "name": "Link", "description": "d"}); code != http.StatusCreated {
				t.Fatalf("create project: %d %s", code, body)
			}
			title := c.seed(t, ts, tok)

			code, raw := authJSON(t, ts, http.MethodGet, "/api/v1/similar/"+c.kind+
				"?text="+strings.ReplaceAll(title, " ", "+"), anonymousMarker, nil)
			if code != http.StatusOK {
				t.Fatalf("similar: %d %s", code, raw)
			}
			matches := similarMatches(t, decodeBody(t, raw))
			if len(matches) == 0 {
				t.Fatalf("no match for text just filed (%s); the URL cannot be checked "+
					"and this test would pass for the wrong reason: %s", c.kind, raw)
			}
			url, _ := matches[0]["url"].(string)
			if url == "" {
				t.Fatalf("%s match has no url at all: %v", c.kind, matches[0])
			}

			// Followed anonymously, because the panel is unauthenticated and the
			// project here is public: a link only the author can open is not the
			// link the panel needs.
			code, page := authJSON(t, ts, http.MethodGet, url, anonymousMarker, nil)
			if code != http.StatusOK {
				t.Errorf("the link the API hands out does not resolve: GET %s -> %d\n"+
					"  (it must be a route a person can open, not a guess at one)\n  %s",
					url, code, firstBytes(page, 200))
			}
			// And it must be a PAGE. The request case passed the status check
			// alone: `/api/v1/requests/1` is a real route that answers 200 with a
			// JSON document, so "the link resolves" is satisfied by an endpoint a
			// person cannot read. Mutation testing found that hole -- the mutation
			// that restored the API-path URLs left the request subtest green.
			if !strings.Contains(string(page), "<html") {
				t.Errorf("the link the API hands out is not a page: GET %s -> %d, "+
					"and the body is not HTML.\n  A filer cannot jump to a JSON "+
					"document.\n  %s", url, code, firstBytes(page, 200))
			}
		})
	}
}

func firstBytes(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}

// TestASimilarityProbeDoesNotRevealAPrivateProjectsTitles.
//
// /similar is unauthenticated on purpose (§6.1: "a panel that needs a token is a
// panel nobody sees"), and FindSimilarField selected from `embeddings` filtered by
// kind, model and an optional project id -- and NEVER by visibility. So an
// anonymous probe could ask "does this text exist anywhere" and get back the
// title, status and id of a match in a private project.
//
// That is the same defect as id_enumeration_test.go's six endpoints, one layer
// over: there a loop over integers walked a private roadmap; here a phrase does.
// And it is worse on the write path: checkDuplicates files instance-wide, so a
// filer in their own project was refused a filing (409) with a private project's
// complaint title in the response body.
//
// The two halves are both load-bearing. A fix that hides everything would break
// §6.1's cross-project search for the people entitled to it, so the owner must
// still find their own.
func TestASimilarityProbeDoesNotRevealAPrivateProjectsTitles(t *testing.T) {
	ts, _ := withEmbedder(t)
	owner := seedPrivateProject(t, ts) // private "privgate", complaint "Export drops the last row"
	const secret = "Export drops the last row"

	// 1. The owner finds it. Without this the rest of the test proves nothing:
	//    a probe that finds nothing for anybody is not a privacy fix.
	code, body := authJSON(t, ts, http.MethodGet,
		"/api/v1/similar/complaint?text="+strings.ReplaceAll(secret, " ", "+"), owner, nil)
	if code != http.StatusOK {
		t.Fatalf("owner probe: %d %s", code, body)
	}
	if !strings.Contains(string(body), secret) {
		t.Fatalf("the owner of the private project cannot find their own complaint; "+
			"§6.1's search must work for people entitled to the answer: %s", body)
	}

	// 2. An anonymous probe does not, including by id and by count.
	code, body = authJSON(t, ts, http.MethodGet,
		"/api/v1/similar/complaint?text="+strings.ReplaceAll(secret, " ", "+"),
		anonymousMarker, nil)
	if code != http.StatusOK {
		t.Fatalf("anonymous probe: %d %s", code, body)
	}
	if strings.Contains(string(body), secret) {
		t.Errorf("LEAK: an anonymous similarity probe returned a private project's "+
			"complaint title.\n  GET /api/v1/similar/complaint?text=... -> %d\n  %s",
			code, body)
	}
	if n := len(similarMatches(t, decodeBody(t, body))); n != 0 {
		t.Errorf("LEAK: an anonymous probe matched %d private-project row(s); the "+
			"count alone confirms the complaint exists.\n  %s", n, body)
	}

	// 3. And the filing path: a second user must not be refused a filing because
	//    of a complaint they cannot see, and must not be told its title.
	mallory := registerAndLogin(t, ts, "mallory")
	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", mallory,
		map[string]any{"slug": "mallorys", "name": "M", "description": "d"}); code != http.StatusCreated {
		t.Fatalf("create project: %d %s", code, body)
	}
	code, body = authJSON(t, ts, http.MethodPost, "/api/v1/projects/mallorys/complaints",
		mallory, map[string]any{"project_id": 2, "title": secret,
			"body": "I hit this too", "severity": 3})
	if code == http.StatusConflict {
		t.Errorf("LEAK: filing refused with 409 because of a complaint in a project "+
			"the filer cannot read. The body carries that project's title and id:\n  %s", body)
	}
	// The response echoes the filer's OWN title, so "does the body contain the
	// text" is not the question. The question is whether it carries a match: a
	// `similar` array, or the private project's slug or name.
	if _, leaked := decodeBody(t, body)["similar"]; leaked {
		t.Errorf("LEAK: the filing response carries similarity matches from a project "+
			"the filer cannot read:\n  %s", body)
	}
	for _, forbidden := range []string{"privgate", "P\"", "\"P\""} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("LEAK: the filing response names the private project (%q):\n  %s",
				forbidden, body)
		}
	}
}

// TestTheFilingFormsAskTheServerAboutDuplicates.
//
// The endpoint was complete and called by nothing: `grep -rin similar` over
// assets/js, templates/ and tests/ returned the route registration alone. So a
// filer could not see what they were about to duplicate; they could only be
// refused afterwards, which is the wrong end of the form.
//
// Asserted on the SOURCE, which is the weaker half of a pair: source inspection
// proves a string exists, never that the code runs (the ReferenceError that killed
// the whole project page parsed perfectly). The browser half is
// tests/e2e/duplicates_e2e.py, which types a title and reads the panel.
func TestTheFilingFormsAskTheServerAboutDuplicates(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("assets", "js", "project.js"))
	if err != nil {
		t.Fatalf("read project.js: %v", err)
	}
	src := string(data)

	// Wired on both forms, by name, so deleting one is visible.
	for _, want := range []string{
		"wireDupPanel(complaintForm, 'complaint'",
		"wireDupPanel(featureForm, 'feature'",
		"dupPanel('complaint')",
		"dupPanel('feature')",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("project.js no longer contains %q: one of the two filing forms "+
				"has no duplicate check", want)
		}
	}

	// It calls the endpoint, and names the kind rather than hardcoding one.
	if !strings.Contains(src, "'/api/v1/similar/' + encodeURIComponent(kind)") {
		t.Error("project.js does not call /api/v1/similar/<kind>: the panel has no " +
			"server to ask, and a hardcoded kind would report the wrong corpus on " +
			"the other form")
	}

	// The link is the API's own url field. Reassembling a path here is how the
	// server's 404 survived: nothing ever followed one.
	if !strings.Contains(src, "m.url ? '<a href=\"' + esc(m.url)") {
		t.Error("project.js does not build the match link from the API's url field")
	}

	// The positive half, so the pair cannot be satisfied by deleting the panel:
	// the titles are authored text and must go through esc().
	if !strings.Contains(src, "esc(m.title") {
		t.Error("project.js does not escape the match title it renders: a complaint " +
			"titled with markup would be executed in the filer's browser")
	}

	// The negative half of the defect: no path may be reassembled from the API's
	// route vocabulary, because those routes are what did not exist.
	for _, bad := range []string{"'/api/v1/features/'", "'/api/v1/complaints/'", "'/api/v1/requests/'"} {
		if strings.Contains(src, bad) {
			t.Errorf("project.js builds a link from %s; the API's match URLs are page "+
				"routes and come from the response's url field", bad)
		}
	}
}
