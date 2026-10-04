package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// The Consensus page's API contract (docs/specs/consensus-page-spec.md).
//
// The response has to carry the tally because the page must not compute consensus
// itself: there are two ratios with different denominators, and §6.6 requires both.
// A client that derives them can be wrong about what a stand-aside means without
// anything looking broken.

// The real helper vocabulary in this package, so nothing here is invented:
//   seedPanels(t)                     -> (ts, db, slug, projectID, authorID)
//   registerAndLogin(t, ts, name)     -> a bearer token for a NEW user
//   authGet(t, ts, path, auth)        -> (status, raw body), token-aware, anonymous
//                                        when auth is the isAnon() sentinel
//   doJSON(t, ts, method, path, body) -> (response, decoded map) as the default user
//   doJSONAnon(...)                   -> the same, with no identity at all

const anonAuth = "" // isAnon() treats this as "no identity"

// castAs records a stance as a given user, through the real endpoint, so these tests
// cover the write path the page uses rather than inserting rows behind its back.
func castAs(t *testing.T, ts *httptest.Server, slug string, callID int64, tok, position string) {
	t.Helper()
	body := mustJSON(t, map[string]any{"position": position})
	req, err := http.NewRequest(http.MethodPost,
		ts.URL+"/api/v1/projects/"+slug+"/consensus/"+strconv.FormatInt(callID, 10)+"/position",
		bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", tok)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("cast %s: %v", position, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("cast %s as a distinct user: status %d, body %s", position, resp.StatusCode, raw)
	}
}

// tallyFor GETs a call as `tok` and returns its tally.
func tallyFor(t *testing.T, ts *httptest.Server, slug string, callID int64, tok string) map[string]any {
	t.Helper()
	code, raw := authGet(t, ts,
		"/api/v1/projects/"+slug+"/consensus/"+strconv.FormatInt(callID, 10), tok)
	if code != http.StatusOK {
		t.Fatalf("read consensus: status %d, body %s", code, raw)
	}
	var out struct {
		Tally        map[string]any `json:"tally"`
		TallyVisible bool           `json:"tally_visible"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode consensus read: %v", err)
	}
	if out.Tally == nil {
		t.Fatalf("the read carries no tally: %s", raw)
	}
	// §6.6 hides the running counts until the call closes, so a test asking for the
	// tally must close the call first. Saying so HERE turns a future mistake into one
	// clear message: four tests that read an open call all failed with
	// "consent=<nil>, want 4", which is true but reads as a broken tally rather than as
	// a test reading the wrong path.
	if !out.TallyVisible {
		t.Fatalf("the call is open, so §6.6 hides the tally -- close it first "+
			"(db.CloseConsensusCall) before asking for counts. body: %s", raw)
	}
	return out.Tally
}

// closeForTally closes a call so its tally becomes visible (§6.6).
func closeForTally(t *testing.T, db *store.DB, callID int64) {
	t.Helper()
	if _, err := db.CloseConsensusCall(context.Background(), callID); err != nil {
		t.Fatalf("CloseConsensusCall: %v", err)
	}
}

func TestTheConsensusReadCarriesTheTally(t *testing.T) {
	ts, db, slug, pid, authorID := seedPanels(t)
	_ = authorID
	call, err := db.CreateConsensusCall(context.Background(), pid, 0, 1, "Adopt X?", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}

	// Closed, because §6.6 hides the counts while the call is open and this test is
	// about the shape a page receives on the path where the numbers exist.
	closeForTally(t, db, call.ID)
	tally := tallyFor(t, ts, slug, call.ID, anonAuth)

	// All thirteen keys, because a page that reads one it cannot find has to guess.
	// The two ratios and the two thresholds are the load-bearing ones.
	for _, k := range []string{
		"consent", "abstain", "stand_aside", "block", "participants", "eligible",
		"quorum_required", "support_ratio", "decisive_ratio",
		"support_required", "decisive_required", "reluctant", "open_objections",
	} {
		if _, present := tally[k]; !present {
			t.Errorf("tally is missing %q; it has %v", k, keysOf(tally))
		}
	}
}

// The load-bearing test. 4 consent / 3 stand-aside / 0 block is the case the spec §3
// is about: support counts reservations and so reads 0.57, decisive counts sides
// taken and so reads 1.00. Collapsing them to one number displays the bug §6.3 fixed,
// so this fails if anyone "simplifies" the response.
func TestTheTallyReportsBothRatiosSeparately(t *testing.T) {
	ts, db, slug, pid, _ := seedPanels(t)
	call, err := db.CreateConsensusCall(context.Background(), pid, 0, 1, "Adopt X?", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}

	// Seven DISTINCT users. CastPosition upserts on (call_id, user_id), so casting
	// as the same identity seven times would leave one position row and a tally of
	// 1/1 rather than the 4/3/0 split this test is about -- it would then compare
	// two ratios that are equal, which is the opposite of the claim.
	for i := 0; i < 7; i++ {
		tok := registerAndLogin(t, ts, "tally-voter-"+strconv.Itoa(i))
		stance := "consent"
		if i >= 4 {
			stance = "stand_aside"
		}
		castAs(t, ts, slug, call.ID, tok, stance)
	}

	// Closed: §6.6's hidden tally means an open call reports null for every count, so
	// the two-ratio claim can only be made once the call has resolved.
	closeForTally(t, db, call.ID)
	tally := tallyFor(t, ts, slug, call.ID, anonAuth)

	if got := numOf(tally["consent"]); got != 4 {
		t.Errorf("consent=%v, want 4", tally["consent"])
	}
	if got := numOf(tally["stand_aside"]); got != 3 {
		t.Errorf("stand_aside=%v, want 3", tally["stand_aside"])
	}
	// support = consent / (consent + stand_aside + block) = 4/7 = 0.571...
	if got := numOf(tally["support_ratio"]); !closeTo(got, 4.0/7.0, 0.01) {
		t.Errorf("support_ratio=%v, want ~0.571 (reservations count against support)", got)
	}
	// decisive = consent / (consent + block) = 4/4 = 1.00 -- the reservations do NOT
	// count here. If this equals support_ratio, one of the two is wrong.
	if got := numOf(tally["decisive_ratio"]); !closeTo(got, 1.0, 0.001) {
		t.Errorf("decisive_ratio=%v, want 1.0 (reservations are not opposition)", got)
	}
	if numOf(tally["support_ratio"]) == numOf(tally["decisive_ratio"]) {
		t.Error("the two ratios are equal on a 4/3/0 split, so they have been collapsed " +
			"into one number -- which is the bug §3 of the spec exists to prevent")
	}
}

// Quorum progress has to be readable while the call is OPEN. That is the whole
// reason the tally was extracted out of CloseConsensusCall: before, the only way to
// get these numbers was to close the call.
func TestTheTallyShowsQuorumProgressWhileTheCallIsOpen(t *testing.T) {
	ts, db, slug, pid, _ := seedPanels(t)
	call, err := db.CreateConsensusCall(context.Background(), pid, 0, 1, "Adopt X?", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	if call.Status != "open" {
		t.Fatalf("fixture is wrong: the call is %q, want open", call.Status)
	}
	castAs(t, ts, slug, call.ID, anonAuth, "consent")

	code, raw := authGet(t, ts,
		"/api/v1/projects/"+slug+"/consensus/"+strconv.FormatInt(call.ID, 10), anonAuth)
	if code != http.StatusOK {
		t.Fatalf("read: %d %s", code, raw)
	}
	var out struct {
		Call struct {
			Status string `json:"status"`
		} `json:"call"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Call.Status != "open" {
		t.Errorf("reading the call changed its status to %q", out.Call.Status)
	}
	// Read raw, NOT via tallyFor: this test is about what an OPEN call may show, and
	// §6.6 says participation and the quorum bar stay visible while the counts do not.
	// Going through a helper that demands a revealed tally would invert the claim.
	var open struct {
		Tally        map[string]any `json:"tally"`
		TallyVisible bool           `json:"tally_visible"`
	}
	if err := json.Unmarshal(raw, &open); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if open.TallyVisible {
		t.Error("an open call reports the tally visible; §6.6 hides it until close")
	}
	if got := numOf(open.Tally["participants"]); got != 1 {
		t.Errorf("participants=%v, want 1 -- §6.6 keeps participation progress visible", got)
	}
	if _, present := open.Tally["quorum_required"]; !present {
		t.Error("an open call reports no quorum_required, so the page cannot show progress")
	}
	// And the other half of the same rule: the counts themselves stay hidden.
	for _, k := range []string{"consent", "stand_aside", "block",
		"support_ratio", "decisive_ratio"} {
		if v := open.Tally[k]; v != nil {
			t.Errorf("an open call reports %s=%v; §6.6 hides the running counts", k, v)
		}
	}
}

// A private project must be indistinguishable from a nonexistent one, so the body
// carries the generic message and never the slug.
func TestAConsensusPageForAPrivateProjectIsNotFoundToAStranger(t *testing.T) {
	ts, db, slug, pid, _ := seedPanels(t)
	if _, err := db.SetProjectVisibility(context.Background(), slug, "private"); err != nil {
		t.Fatalf("SetProjectVisibility: %v", err)
	}
	call, err := db.CreateConsensusCall(context.Background(), pid, 0, 1, "Adopt X?", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	stranger := registerAndLogin(t, ts, "consensus-stranger")
	path := "/api/v1/projects/" + slug + "/consensus/" + strconv.FormatInt(call.ID, 10)

	code, raw := authGet(t, ts, path, stranger)
	if code != http.StatusNotFound {
		t.Fatalf("a stranger got %d for a private project's call, want 404; "+
			"403 would confirm the slug exists", code)
	}
	if strings.Contains(string(raw), slug) {
		t.Errorf("the 404 body leaks the slug: %s", raw)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg, _ := out["error"].(string); msg != "not found" {
		t.Errorf("error=%q, want %q", msg, "not found")
	}

	// Indistinguishable from a project that does not exist: same status, same body.
	// Without this, 404 leaks that the slug is real.
	missingCode, missingRaw := authGet(t, ts,
		"/api/v1/projects/no-such-project-at-all/consensus/"+strconv.FormatInt(call.ID, 10),
		stranger)
	if missingCode != code {
		t.Errorf("absent project = %d, forbidden project = %d; the two must be "+
			"indistinguishable", missingCode, code)
	}
	if strings.TrimSpace(string(raw)) != strings.TrimSpace(string(missingRaw)) {
		t.Errorf("the 404 bodies differ, so the forbidden one is identifiable:\n"+
			"  forbidden: %s\n  absent:   %s", raw, missingRaw)
	}
}

// Consensus reads are public: the page is readable signed out, like every other
// read-only page. §8 rule 3 then requires the WRITE controls to be prompts instead.
func TestTheConsensusReadWorksSignedOut(t *testing.T) {
	ts, db, slug, pid, _ := seedPanels(t)
	call, err := db.CreateConsensusCall(context.Background(), pid, 0, 1, "Adopt X?", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	path := "/api/v1/projects/" + slug + "/consensus/" + strconv.FormatInt(call.ID, 10)
	code, raw := authGet(t, ts, path, anonAuth)
	if code != http.StatusOK {
		t.Fatalf("an anonymous read got %d, want 200: %s", code, raw)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["tally"]; !ok {
		t.Error("the anonymous read carries no tally")
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// numOf reads a JSON number. json.Unmarshal gives float64 for every number, so a
// plain type assertion would silently yield 0 and make a wrong tally look right.
func numOf(v any) float64 {
	f, _ := v.(float64)
	return f
}

func closeTo(a, b, tol float64) bool {
	d := a - b
	return d < tol && d > -tol
}

// The visibility hole, measured rather than asserted from reading.
//
// handleGetConsensus took the call id from the URL and loaded it by id alone. It
// never resolved `project_id` from the path, never checked the call belongs to that
// project, and never asked whether the caller may read it. Two consequences, and
// this probe pins both:
//
//   - a private project's call is readable by anyone who can guess a small integer;
//   - a call in project A is readable through project B's URL, so the project scope
//     in the path is decorative.
//
// Written as a probe first and kept, because the fix is one guard and a regression
// test that a guard exists is the only thing standing between this and a repeat.
func TestAConsensusCallCannotBeReadThroughAnotherProjectsURL(t *testing.T) {
	ts, db, slugA, pidA, _ := seedPanels(t)

	// A second project, private, with no call of its own.
	_, pidB := makeSecondProject(t, ts, db, "other-private")

	call, err := db.CreateConsensusCall(context.Background(), pidA, 0, 1, "Adopt X?", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	stranger := registerAndLogin(t, ts, "cross-project-stranger")

	// Project B's URL, project A's call.
	viaOther := "/api/v1/projects/other-private/consensus/" + strconv.FormatInt(call.ID, 10)
	if code, raw := authGet(t, ts, viaOther, stranger); code != http.StatusNotFound {
		t.Errorf("reading project A's call through project B's URL = %d, want 404: %s",
			code, raw)
	}

	// Project A made private, then read with the correct URL.
	if _, err := db.SetProjectVisibility(context.Background(), slugA, "private"); err != nil {
		t.Fatalf("SetProjectVisibility: %v", err)
	}
	ownURL := "/api/v1/projects/" + slugA + "/consensus/" + strconv.FormatInt(call.ID, 10)
	if code, raw := authGet(t, ts, ownURL, stranger); code != http.StatusNotFound {
		t.Errorf("a stranger read a private project's call = %d, want 404: %s", code, raw)
	}

	// Sanity: the row really is there, so the 404s above are the guard and not an
	// empty fixture.
	if _, err := db.GetConsensusCall(context.Background(), call.ID); err != nil {
		t.Fatalf("the fixture's call does not exist: %v", err)
	}
	_ = pidB
}

// makeSecondProject creates a project through the API, returning its slug and id.
func makeSecondProject(t *testing.T, ts *httptest.Server, db *store.DB, slug string) (string, int64) {
	t.Helper()
	resp, out := doJSON(t, ts, http.MethodPost, "/api/v1/projects", map[string]any{
		"slug": slug, "name": "Other project", "description": "another",
		"governance_model": "maintainer_led", "license": "MIT",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %s: status %d, body %v", slug, resp.StatusCode, out)
	}
	id := mustProjectIDBySlug(t, db, slug)
	if _, err := db.SetProjectVisibility(context.Background(), slug, "private"); err != nil {
		t.Fatalf("SetProjectVisibility(%s): %v", slug, err)
	}
	return slug, id
}
