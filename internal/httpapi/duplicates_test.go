package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/embed"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

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
