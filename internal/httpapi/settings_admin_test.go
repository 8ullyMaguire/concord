package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The administration surface of the settings page: visibility, tags, languages and
// invites. Six endpoints that had handlers, guards and store tests and no UI at all --
// docs/specs/frontend-spec.md listed them as "none" and they stayed that way.
//
// These are the read-side gates (by NAME, so deleting a fetch turns them red) and the
// shape gates (the keys the page must read). The behaviour is in settings_e2e.py.

func TestTheSettingsPageReadsTheAdministrationEndpoints(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/settings.js")
	// The CALL, not a substring: this file's own header comment names all six endpoints,
	// and a substring check would be satisfied by the documentation (the catalogue's
	// Defect 17). The trailing `")` is what only a call site has.
	for _, want := range []string{
		`+ "/visibility"`, `+ "/tags")`, `+ "/languages")`, `+ "/invites")`,
		`+ "/invites/" `, `+ "/revoke"`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("settings.js never calls %s -- the endpoint has a handler and no "+
				"reader, which is the defect this page exists to close", want)
		}
	}
}

// GET /languages is the read half of §15's collaborative percentages. Without it the
// settings panel could not show what a PUT stored: the same write-blind shape `PUT /tags`
// had before `GET /tags` existed.
func TestTheLanguagesReadHalfAnswersTheShapeThePanelReads(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	createTestProject(t, ts, "langs-read")

	var pid int64
	if err := st.QueryRow(`SELECT id FROM projects WHERE slug='langs-read'`).Scan(&pid); err != nil {
		t.Fatalf("project id: %v", err)
	}
	if _, err := st.ExecContext(t.Context(),
		`INSERT INTO project_languages (project_id, language, pct) VALUES (?,?,?),(?,?,?)`,
		pid, "Rust", 70.0, pid, "Go", 30.0); err != nil {
		t.Fatalf("seed languages: %v", err)
	}

	code, body := authGet(t, ts, "/api/v1/projects/langs-read/languages", "")
	if code != http.StatusOK {
		t.Fatalf("GET /languages answered %d: %s", code, body)
	}
	// The wrapper key is `languages`, not a bare array: the page reads `.languages`, and a
	// bare array would render the panel as "No language breakdown yet" with data in the
	// response -- the shape mismatch that has cost this project the most time.
	var out struct {
		Languages []struct {
			Language string  `json:"language"`
			Pct      float64 `json:"pct"`
		} `json:"languages"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, body)
	}
	if len(out.Languages) != 2 {
		t.Fatalf("languages = %d, want 2 (body=%s)", len(out.Languages), body)
	}
	// Highest percentage first, so a 50/50 mix renders in the same order every request.
	if out.Languages[0].Language != "Rust" || out.Languages[1].Language != "Go" {
		t.Errorf("order = %s, %s; want Rust, Go",
			out.Languages[0].Language, out.Languages[1].Language)
	}
}

// A private project must not answer 200 with an empty language list: that 200 confirms
// the slug exists, which is the leak every other read on this surface was fixed for (and
// the reason GET /tags resolves the project before listing its tags).
func TestTheLanguagesReadHalfHidesAPrivateProject(t *testing.T) {
	ts := newTestServer(t)
	createTestProject(t, ts, "langs-private")
	// Created by the privileged harness actor, which is the owner, so the PUT is allowed.
	// doJSON sends the harness's privileged token; `authJSON(..., "")` means ANONYMOUS
	// in that helper, and the first version of this test therefore never made the project
	// private and asserted against a public one.
	if resp, body := doJSON(t, ts, http.MethodPut,
		"/api/v1/projects/langs-private/visibility",
		map[string]any{"visibility": "private"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("set visibility: %d %v", resp.StatusCode, body)
	}

	code, body := authGet(t, ts, "/api/v1/projects/langs-private/languages", anonymousMarker)
	if code != http.StatusNotFound {
		t.Errorf("an anonymous GET /languages on a private project answered %d, want 404 "+
			"(body=%s)", code, body)
	}
	if strings.Contains(string(body), "languages") {
		t.Errorf("the refusal body names the resource: %s", body)
	}
}

// The four writers must send the token the server reads, in the shape the server expects.
// A blanket assert rather than a count: this file has already had four silent-401 bugs
// from a wrong localStorage key and one from a wrong field name.
func TestTheAdministrationWritesSendTheTokenAndTheRightKeys(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/settings.js")
	// The token is read in ONE place -- the shared `write` helper -- and each writer goes
	// through it. Asserting a call COUNT was the first version of this check and it was
	// wrong twice over: the count dropped when the helper replaced four copies of the
	// header block, and a count cannot tell a correct call from a call with no token.
	// So: the helper reads the key, and there is a call per writer.
	if !strings.Contains(js, `function write(path, method, payload, onOk, btn)`) {
		t.Fatal("settings.js has no shared write helper")
	}
	helper := js[strings.Index(js, `function write(path, method, payload, onOk, btn)`):]
	if end := strings.Index(helper, "\n  function "); end > 0 {
		helper = helper[:end]
	}
	// The token is read in `authHeaders`, used by both the read and the write path. The
	// statement changed shape when the refactor landed: the first version of this check
	// asserted the token read inside `write`'s own body, and it correctly went red when
	// the two copies became one helper -- a gate that names the wrong level of the code
	// reports a refactor as a defect.
	if !strings.Contains(js, `function authHeaders(extra)`) {
		t.Fatal("settings.js has no shared authHeaders helper")
	}
	if !strings.Contains(helper, "authHeaders(") {
		t.Error("the shared write helper does not attach the session token")
	}
	src := js[strings.Index(js, "function authHeaders(extra)"):]
	if end := strings.Index(src, "\n  }"); end > 0 {
		src = src[:end]
	}
	if !strings.Contains(src, `localStorage.getItem("concord.token")`) {
		t.Error("authHeaders does not read concord.token, so no request from this page " +
			"carries the session")
	}
	// AND THE READ PATH. Every read on this page was anonymous until the visibility panel
	// made the consequence visible: `get` sent only `Accept`, which is harmless for a
	// public project and blanks seven panels for a member of a protected one. The
	// behavioural half of this gate is in settings_e2e.py (GET /invites is member-only, so
	// a rendered invite row proves the read carried the token); this half is the source
	// statement, because a future panel that fetches directly would bypass the helper.
	readHelper := js[strings.Index(js, "function get(path) {"):]
	if end := strings.Index(readHelper, "\n  }"); end > 0 {
		readHelper = readHelper[:end]
	}
	if strings.Contains(readHelper, "localStorage.getItem") {
		t.Error("the read helper reads the token itself instead of going through authHeaders")
	}
	if !strings.Contains(readHelper, "authHeaders(") {
		t.Error("the read helper does not attach the session token: every panel on this " +
			"page is then an anonymous reader, which is invisible on a public project and " +
			"breaks the page on a protected one")
	}
	if n := strings.Count(js, `write("/api/v1/projects/`); n < 5 {
		t.Errorf("only %d writers go through the helper; visibility, tags, languages, the "+
			"invite mint and the invite revoke are five", n)
	}
	if strings.Contains(js, "concord_token") {
		t.Error("settings.js reads concord_token, which does not exist; every write 401s " +
			"silently, because getItem returns null for a missing key rather than throwing")
	}
	// The wire keys, from the handlers: visibility, tags, languages[{language,pct}],
	// expires_in_seconds + max_uses.
	for _, want := range []string{
		"{ visibility: sel.value }",
		"{ tags: names }",
		"{ languages: langs }",
		"expires_in_seconds:",
		"max_uses:",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("settings.js does not send %s, which is the key the handler reads", want)
		}
	}
	// The visibility options are the four the store accepts. A fifth would be a 400 with
	// the server's list in the message, which is a worse way to learn that.
	for _, level := range []string{"public", "unlisted", "protected", "private"} {
		if !strings.Contains(js, `{ value: "`+level+`"`) {
			t.Errorf("settings.js does not offer the visibility level %q", level)
		}
	}
}

// §4.3's meter tells the reader to add tags. Until this panel existed there was no
// control anywhere on the site that applied one, so the meter delivered an instruction
// nobody could follow -- which is the defect the panel closes, and the one worth a gate:
// the meter's advice and the tags form must exist in the same file.
func TestTheMeterAndTheTagsControlShipTogether(t *testing.T) {
	ts := newTestServer(t)
	js := assetBody(t, ts, "/assets/js/settings.js")
	if !strings.Contains(js, "to reach the threshold") {
		t.Error("settings.js no longer renders the completeness advice; if the meter moved, " +
			"move this gate with it")
	}
	if !strings.Contains(js, `id="tags-input"`) {
		t.Error("the completeness meter tells the reader to add tags and there is no control " +
			"that applies one: an instruction that cannot be obeyed is worse than no meter")
	}
}
