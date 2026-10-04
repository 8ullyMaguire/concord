package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheRequestsBoardPageRendersAndCarriesTheTabBar is the 1 of the three parts for
// the Requests INDEX, which the tab bar links to and which did not exist.
//
// Without it the tab 404'd and a reader could reach a request only by knowing its id.
func TestTheRequestsBoardPageRendersAndCarriesTheTabBar(t *testing.T) {
	ts := newTestServer(t)
	seedRequestForBoard(t, ts)

	code, body := getBody(t, ts.URL+"/projects/reqboard/requests")
	if code != http.StatusOK {
		t.Fatalf("requests board returned %d; a template missing from the `pages` "+
			"slice renders 500 with its name in the body", code)
	}
	for _, want := range []string{
		"<!DOCTYPE html>",
		"requests-root",
		"/assets/js/requests-index.js",
		"detail-tabs",
		// The TAB that links here, so the navigation is asserted at the same time.
		`href="/projects/reqboard/requests"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("requests board missing %q", want)
		}
	}
}

// TestTheRequestsTabIsActiveOnTheBoard is the small one that catches a tab that links
// correctly and highlights nowhere.
func TestTheRequestsTabIsActiveOnTheBoard(t *testing.T) {
	ts := newTestServer(t)
	seedRequestForBoard(t, ts)

	for _, tc := range []struct {
		path string
		want string
	}{
		{"/projects/reqboard/requests", "Requests"},
		{"/projects/reqboard/lists", "Lists"},
	} {
		_, body := getBody(t, ts.URL+tc.path)
		// is-active on the tab whose href ends in this section's path.
		// The whole tab bar, then find the tab BY ITS HREF. Searching backwards from
		// the label text with a fixed window was too narrow: it depended on the
		// anchor's text being near the end of its line, and it failed for BOTH
		// sections while the tabs were correct.
		bar := string(body)
		start := strings.Index(bar, `<nav class="detail-tabs"`)
		if start < 0 {
			t.Fatalf("%s: no tab bar", tc.path)
		}
		end := strings.Index(bar[start:], "</nav>")
		bar = bar[start : start+end]

		needle := `href="/projects/reqboard/` + strings.ToLower(tc.want) + `"`
		i := strings.Index(bar, needle)
		if i < 0 {
			t.Errorf("%s: no tab linking %s", tc.path, needle)
			continue
		}
		// The anchor opening tag containing that href must carry is-active.
		open := strings.LastIndex(bar[:i], "<a ")
		if !strings.Contains(bar[open:i], "is-active") {
			t.Errorf("%s: the %s tab is not marked active; anchor was %q",
				tc.path, tc.want, bar[open:i])
		}
	}
}

// TestTheListsAndRequestsTabsExist: the two sections had pages but NO TAB, so they were
// only reachable by scrolling the overview far enough. A page you have to find is a
// page most readers never do.
func TestTheListsAndRequestsTabsExist(t *testing.T) {
	ts := newTestServer(t)
	seedRequestForBoard(t, ts)

	_, body := getBody(t, ts.URL+"/projects/reqboard")
	for _, want := range []string{
		`href="/projects/reqboard/requests"`,
		`href="/projects/reqboard/lists"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the project tab bar has no link to %s", want)
		}
	}
}

// TestADetailPageKeepsItsTabActive: Sub is parts[2] of the path, so /lists/2 must
// highlight "Lists" and not fall through to no tab -- which is the case for any detail
// route whose third segment is a page the tab bar does not name.
func TestADetailPageKeepsItsTabActive(t *testing.T) {
	ts := newTestServer(t)
	seedRequestForBoard(t, ts)

	_, body := getBody(t, ts.URL+"/projects/reqboard/lists/1")
	idx := strings.Index(string(body), ">Lists<")
	if idx < 0 {
		t.Fatal("no Lists tab on the list detail page")
	}
	if !strings.Contains(string(body)[max(0, idx-260):idx], "is-active") {
		t.Error("the Lists tab is not active on /lists/1; Sub must be the THIRD path " +
			"segment, so a detail route still highlights its section")
	}
}

// TestTheBoardScriptReadsTheProjectIDAsAbsentNotZero.
//
// store.Request.ProjectID is `json:"project_id,omitempty"`, so an instance-wide
// request has NO KEY. A page testing `r.project_id === 0` would therefore never see an
// instance-wide request and would label a shared question as this project's own --
// which is the lists.title/name mistake one layer down, where a Go sentinel meets a
// JSON field that omits it.
func TestTheBoardScriptReadsTheProjectIDAsAbsentNotZero(t *testing.T) {
	src, err := os.ReadFile("assets/js/requests-index.js")
	if err != nil {
		t.Fatalf("read requests-index.js: %v", err)
	}
	body := string(src)
	if strings.Contains(body, "project_id === 0") &&
		!strings.Contains(body, "project_id === undefined") {
		t.Error("the board identifies an instance-wide request by project_id === 0, " +
			"but the key is OMITTED for those rows, so the check is never true and " +
			"every shared request is labelled as the project's own")
	}
	// And it must fetch the instance index too, or a shared question is invisible.
	if !strings.Contains(body, "/api/v1/requests") {
		t.Error("the board does not fetch /api/v1/requests; 17 makes a request with no " +
			"project open to the whole instance, and the project index omits those")
	}
}

// TestTheRequestsBoardScriptParses.
func TestTheRequestsBoardScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	path := "assets/js/requests-index.js"
	if out, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
		t.Errorf("node --check %s: %v\n%s", path, err, out)
	}
}

// TestAPrivateProjectServesNoRequestsBoard: part 3.
func TestAPrivateProjectServesNoRequestsBoard(t *testing.T) {
	ts := newTestServer(t)
	token := seedRequestForBoard(t, ts)
	setVisibility(t, ts, token, "reqboard", "private")

	code, body := authJSON(t, ts, http.MethodGet, "/projects/reqboard/requests",
		anonymousMarker, nil)
	if code != http.StatusNotFound {
		t.Errorf("requests board for a private project returned %d to an anonymous "+
			"caller, want 404", code)
	}
	if strings.Contains(string(body), "reqboard") {
		t.Error("the 404 echoed the private slug")
	}
}

// seedRequestForBoard creates the project the board tests read, and returns its OWNER's
// token -- setVisibility needs the account that owns the project.
func seedRequestForBoard(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	owner := registerAndLogin(t, ts, "reqboard-owner")

	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "reqboard", "name": "Req Board", "description": "d"}); code >= 300 {
		t.Fatalf("create project: %d %s", code, body)
	}
	for _, q := range []struct{ title, body string }{
		{"Keep WIP limits below six", "The point of the board is the limit."},
		{"Drop the nightly rebuild", "It fails on Sundays and nobody reads it."},
	} {
		if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/requests", owner,
			map[string]any{"title": q.title, "body": q.body}); code >= 300 {
			t.Fatalf("create request %q: %d %s", q.title, code, body)
		}
	}
	// A list, so the Lists tab's href resolves to a page rather than a 404.
	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/reqboard/lists",
		owner, map[string]any{"slug": "libs", "title": "Libraries",
			"description": "what we ship"}); code >= 300 {
		t.Fatalf("create list: %d %s", code, body)
	}
	return owner
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// TestEveryWriteSendsTheTokenTheServerReads.
//
// The session key is `concord.token` -- session.js, auth.js, consensus.js, finder.js,
// rank.js and scout_e2e.py all use it. Four scripts written for the comments, requests,
// lists and board pages read `concord_token`, with an UNDERSCORE.
//
// The result is not a crash and not a visible error: getItem returns null, the
// Authorization header is simply absent, and the endpoint answers 401. The page
// renders, the button is there, and the write silently never happens -- which is the
// single most expensive failure mode there is, because it looks like a permissions
// problem and is a typo.
//
// So the check is a cross-file consistency assertion rather than a per-file one: grep
// for the wrong key ANYWHERE under assets/js.
func TestEveryWriteSendsTheTokenTheServerReads(t *testing.T) {
	entries, err := os.ReadDir("assets/js")
	if err != nil {
		t.Fatalf("read assets/js: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		data, err := os.ReadFile(filepath.Join("assets/js", e.Name()))
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		if strings.Contains(string(data), "concord_token") {
			t.Errorf("%s reads localStorage key \"concord_token\"; the session key "+
				"is \"concord.token\" (see session.js). A wrong key returns null "+
				"rather than throwing, so every write on this page silently 401s",
				e.Name())
		}
	}
}

// TestTheSessionKeyIsTheOneTheServerSets: the positive half, so the gate above cannot
// be satisfied by deleting every authenticated write.
func TestTheSessionKeyIsTheOneTheServerSets(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("assets", "js", "session.js"))
	if err != nil {
		t.Fatalf("read session.js: %v", err)
	}
	if !strings.Contains(string(data), "concord.token") {
		t.Error("session.js no longer uses concord.token; whatever replaced it must " +
			"be updated in every page that reads it, and nothing enforces that")
	}
}
