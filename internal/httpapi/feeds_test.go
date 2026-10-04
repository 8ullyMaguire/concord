package httpapi

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Feeds (docs/specs/feeds-spec.md).
//
// The load-bearing claims are the three that are easy to get wrong and hard to notice:
// a private project has no feed at all, the request's Host never reaches the output,
// and §6.6's hidden tally is not published to aggregators.

const testBase = "https://concord.test"

// feedGet fetches a feed with CONCORD_BASE_URL set to testBase.
//
// Over real HTTP rather than through ts.Config, because seedPanels returns an
// *httptest.Server. That is also what makes the Host-header test possible: a Host header
// only means anything on a real request.
func feedGet(t *testing.T, ts *httptest.Server, path string) (int, string, string) {
	t.Helper()
	t.Setenv("CONCORD_BASE_URL", testBase)
	code, raw, ctype := feedGetHost(t, ts, path, "")
	return code, string(raw), ctype
}

// feedGetHost is feedGet with an explicit Host header, or "" to leave it alone.
func feedGetHost(t *testing.T, ts *httptest.Server, path, host string) (int, []byte, string) {
	t.Helper()
	t.Setenv("CONCORD_BASE_URL", testBase)
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-No-Auth", "1")
	if host != "" {
		req.Host = host
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return resp.StatusCode, raw, resp.Header.Get("Content-Type")
}

// Titles pulls every <title> out of a feed body, in document order. Works for both
// formats because both carry <title>; the point of the comparison is that the same items
// appear, not that the format is identical.
func titles(body string) []string {
	var out []string
	dec := xml.NewDecoder(strings.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "title" {
			var text string
			if err := dec.DecodeElement(&text, &se); err == nil {
				out = append(out, text)
			}
		}
	}
}

// --- the two formats carry the same items ------------------------------------

func TestBothFormatsCarryIdenticalItems(t *testing.T) {
	ts, _, slug, _, _ := seedPanels(t)

	rssCode, rss, rssType := feedGet(t, ts, "/projects/"+slug+"/feed.xml")
	if rssCode != http.StatusOK {
		t.Fatalf("rss: %d %s", rssCode, rss)
	}
	if !strings.Contains(rssType, "application/rss+xml") {
		t.Errorf("rss content-type is %q", rssType)
	}
	atomCode, atom, atomType := feedGet(t, ts, "/projects/"+slug+"/atom.xml")
	if atomCode != http.StatusOK {
		t.Fatalf("atom: %d %s", atomCode, atom)
	}
	if !strings.Contains(atomType, "application/atom+xml") {
		t.Errorf("atom content-type is %q", atomType)
	}

	gotRSS, gotAtom := titles(rss), titles(atom)
	if len(gotRSS) == 0 {
		t.Fatal("the rss feed has no titles at all")
	}
	if len(gotRSS) != len(gotAtom) {
		t.Fatalf("rss carries %d titles and atom %d: %v vs %v",
			len(gotRSS), len(gotAtom), gotRSS, gotAtom)
	}
	for i := range gotRSS {
		if gotRSS[i] != gotAtom[i] {
			t.Errorf("title %d differs: rss=%q atom=%q", i, gotRSS[i], gotAtom[i])
		}
	}
}

// The feed and the API the page renders must agree on the items, or a subscriber and a
// reader are looking at different things.
func TestTheFeedCarriesExactlyWhatTheProjectsAPIReturns(t *testing.T) {
	ts, db, _, _, _ := seedPanels(t)
	ctx := context.Background()

	// The fixture's project is PRIVATE by default, so the global feed is legitimately
	// empty and this test would have skipped rather than asserted. Make two public and
	// leave one private: the feed must carry exactly the two.
	// Measured, not assumed: seedPanels already creates exactly ONE public project
	// (id 1, "panel-project"). A probe run first -- asserting "the fixture created no
	// public projects" made this test SKIP, and then assuming it created none and
	// publishing ids 1..2 by hand made it fail with "the API lists 0". Neither guess was
	// worth a round trip: it is public already.
	//
	// So: add one more public project and one private one. Public count goes to 2.
	if _, err := db.CreateProject(ctx, 1, "second-public", "Second Public", "d",
		"collective", "MIT"); err != nil {
		t.Fatalf("create second project: %v", err)
	}
	if _, err := db.SetProjectVisibility(ctx, "second-public", "public"); err != nil {
		t.Fatalf("publish second-public: %v", err)
	}
	if _, err := db.CreateProject(ctx, 1, "second-private", "Second Private", "d",
		"collective", "MIT"); err != nil {
		t.Fatalf("create the private project: %v", err)
	}
	// Left private on purpose: it is the negative case.
	if _, err := db.SetProjectVisibility(ctx, "second-private", "private"); err != nil {
		t.Fatalf("set second-private: %v", err)
	}

	code, apiBody := authGet(t, ts, "/api/v1/projects", anonAuth)
	if code != http.StatusOK {
		t.Fatalf("projects api: %d %s", code, apiBody)
	}
	// Count slug occurrences. The separator matters: writeJSON indents, so the bytes are
	// `"slug": "panel-project"` with a space. The first version counted `"slug":"` and
	// matched ZERO against a correct response -- a green store and a 200 API, and the
	// assertion said "0 public projects".
	wantCount := strings.Count(string(apiBody), `"slug": "`)
	if wantCount != 2 {
		t.Fatalf("the API lists %d public projects, want 2 -- the fixture is wrong:\n%s",
			wantCount, apiBody)
	}

	_, feed, _ := feedGet(t, ts, "/feed.xml")
	// The feed has one extra title: the CHANNEL title is not an item.
	if got := len(titles(feed)); got != wantCount+1 {
		t.Errorf("the feed carries %d titles (%d items + channel) and the API lists "+
			"%d projects", got, got-1, wantCount)
	}
	// Both public projects, named.
	for _, want := range []string{"panel-project", "Second Public"} {
		if !strings.Contains(feed, want) {
			t.Errorf("the feed omits the public project %q:\n%s", want, feed)
		}
	}
	if strings.Contains(feed, "Second Private") {
		t.Errorf("the global feed lists a private project:\n%s", feed)
	}
}

// --- private means absent, not empty -----------------------------------------

func TestAPrivateProjectHasNoFeedAtAll(t *testing.T) {
	ts, db, slug, _, _ := seedPanels(t)
	if _, err := db.SetProjectVisibility(context.Background(), slug, "private"); err != nil {
		t.Fatalf("SetProjectVisibility: %v", err)
	}
	for _, path := range []string{
		"/projects/" + slug + "/feed.xml",
		"/projects/" + slug + "/atom.xml",
		"/projects/" + slug + "/board/feed.xml",
		"/projects/" + slug + "/consensus/feed.xml",
		"/projects/" + slug + "/documents/feed.xml",
	} {
		code, body, _ := feedGet(t, ts, path)
		if code != http.StatusNotFound {
			t.Errorf("%s answered %d for a private project, want 404: %s", path, code, body)
		}
		// And the body must not name the project: a 404 that echoes the slug is a
		// confirmation that it exists.
		if strings.Contains(body, slug) {
			t.Errorf("%s leaks the slug in its 404 body: %s", path, body)
		}
	}
}

// --- the Host header never reaches the output ---------------------------------

// The poisoning test. r.Host is attacker-controlled on a directly-reachable instance, so
// link origins built from it would let one request mint a feed whose links point at an
// attacker's host -- and caches serve that to every subscriber, permanently.
func TestTheRequestHostNeverReachesTheFeed(t *testing.T) {
	ts, _, slug, _, _ := seedPanels(t)
	for _, path := range []string{
		"/feed.xml",
		"/projects/" + slug + "/feed.xml",
		"/projects/" + slug + "/consensus/feed.xml",
	} {
		code, raw, _ := feedGetHost(t, ts, path, "evil.example")
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, code, raw)
		}
		body := string(raw)
		if strings.Contains(body, "evil.example") {
			t.Errorf("%s put the request Host in the feed:\n%s", path, body)
		}
		// And every link is absolute, which is why the base URL is a setting.
		if !strings.Contains(body, testBase) {
			t.Errorf("%s does not use the configured base %s:\n%s", path, testBase, body)
		}
	}
}

// --- feeds off is 503, not 404 -------------------------------------------------

func TestAnUnsetBaseURLRefusesToServeFeeds(t *testing.T) {
	ts, _, slug, _, _ := seedPanels(t)
	// Deliberately NOT feedGet: that helper sets CONCORD_BASE_URL, so calling it here set
	// the variable this test exists to prove is unset -- and the handler, correctly,
	// served a feed and the test failed for the wrong reason. Use a bare request.
	t.Setenv("CONCORD_BASE_URL", "")
	req, err := http.NewRequest(http.MethodGet,
		ts.URL+"/projects/"+slug+"/feed.xml", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-No-Auth", "1")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	code, body := resp.StatusCode, string(raw)

	if code != http.StatusServiceUnavailable {
		t.Fatalf("with no base URL the feed answered %d, want 503", code)
	}
	if !strings.Contains(body, "CONCORD_BASE_URL") {
		t.Errorf("the 503 does not name the variable to set: %s", body)
	}
}

// --- §6.6 in the feed, the easiest thing to get wrong --------------------------

// A feed item is a durable copy held by third-party aggregators that cache indefinitely.
// Publishing the running tally there would defeat §6.6 no matter how carefully the page
// and the API obey it, and TallyConsensus is one method call away.
func TestAnOpenConsensusCallPublishesNoCountsInTheFeed(t *testing.T) {
	ts, db, slug, pid, _ := seedPanels(t)
	call, err := db.CreateConsensusCall(context.Background(), pid, 0, 1, "Adopt X?", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	for i := 0; i < 4; i++ {
		castAs(t, ts, slug, call.ID, registerAndLogin(t, ts, "feed-voter-"+itoaTest(i)), "consent")
	}

	for _, path := range []string{
		"/projects/" + slug + "/consensus/feed.xml",
		"/projects/" + slug + "/consensus/atom.xml",
	} {
		code, body, _ := feedGet(t, ts, path)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		for _, key := range []string{
			"consent", "stand_aside", "block", "abstain",
			"support_ratio", "decisive_ratio", "participants", "eligible",
			"quorum_required", "tally_visible",
		} {
			if strings.Contains(body, key) {
				t.Errorf("%s publishes %q; §6.6 hides the running counts. body:\n%s",
					path, key, body)
			}
		}
		// The state IS published: a subscriber watching calls wants to filter on it.
		if !strings.Contains(body, "open") {
			t.Errorf("%s does not publish the call's state:\n%s", path, body)
		}
	}
}

func itoaTest(i int) string { return string(rune('0' + i)) }

// --- escaping -------------------------------------------------------------------

// User text goes into XML. encoding/xml escapes text nodes, which is why this file
// marshals through structs instead of writing strings together.
func TestUserTextIsEscapedInBothFormats(t *testing.T) {
	ts, db, slug, pid, authorID := seedPanels(t)
	if _, err := db.CreateComplaint(context.Background(), pid, authorID,
		`<script>alert("xss")</script> & "quotes"`, `body <b>bold</b> & more`,
		3, 1.0, 1.0); err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	for _, path := range []string{
		"/projects/" + slug + "/feed.xml",
		"/projects/" + slug + "/atom.xml",
	} {
		code, body, _ := feedGet(t, ts, path)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		if strings.Contains(body, "<script>") {
			t.Errorf("%s emits a raw <script> tag: %s", path, body)
		}
		if !strings.Contains(body, "&lt;script&gt;") {
			t.Errorf("%s did not escape the title at all: %s", path, body)
		}
		// The document must still be well-formed XML, which is the real assertion:
		// an unescaped & anywhere makes every strict reader reject the whole feed.
		var probe struct{}
		if err := xml.Unmarshal([]byte(body), &probe); err != nil {
			t.Errorf("%s is not well-formed XML: %v", path, err)
		}
	}
}

// --- Atom specifics --------------------------------------------------------------

func TestAtomCarriesSelfAndAlternateLinks(t *testing.T) {
	ts, _, slug, _, _ := seedPanels(t)
	_, atom, _ := feedGet(t, ts, "/projects/"+slug+"/atom.xml")
	for _, want := range []string{
		`rel="self"`,
		`href="` + testBase + `/projects/` + slug + `/feed.xml"`,
		`rel="alternate"`,
	} {
		if !strings.Contains(atom, want) {
			t.Errorf("the atom feed is missing %s:\n%s", want, atom)
		}
	}
	// RSS 2.0 has no <updated>; it has RFC822 pubDate. A feed using RFC3339 in an RSS
	// <pubDate> is a real and reader-visible bug.
	_, rss, _ := feedGet(t, ts, "/projects/"+slug+"/feed.xml")
	if strings.Contains(rss, "T00:00:00Z") || strings.Contains(rss, "T12:") {
		t.Errorf("the rss feed carries an RFC3339 date where pubDate belongs:\n%s", rss)
	}
}

// An unsupported format is a 400, not a silent default: a reader asking for ?format=pdf
// and handed RSS has no way to tell what happened.
func TestAnUnsupportedFormatIsRejected(t *testing.T) {
	ts, _, slug, _, _ := seedPanels(t)
	code, _, _ := feedGet(t, ts, "/projects/"+slug+"/feed.xml?format=pdf")
	if code != http.StatusBadRequest {
		t.Errorf("?format=pdf answered %d, want 400", code)
	}
}
