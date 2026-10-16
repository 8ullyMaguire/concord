package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// §6.6's second window over the wire: who may open it, who may vote in it, who may
// close it, and what the close leaves behind.
//
// The window exists because the override is not something a first-window close can
// decide (see TestACloseRecordsABlockRatherThanOverridingIt) and because the ratio it
// is decided by counts NON-ABSTAIN participants, stand-asides included -- the two
// things the eighth pass measured and changed. These tests hold the routes that
// expose it.

// windowPath is §6.6's second window on a call.
func (f closeFixture) windowPath() string { return f.callPath() + "/override" }

func (f closeFixture) openWindow(t *testing.T, tok string) {
	t.Helper()
	code, body := authJSON(t, f.ts, http.MethodPost, f.windowPath(), tok, nil)
	if code != http.StatusCreated {
		t.Fatalf("open the override window: %d %s", code, body)
	}
}

// voteInWindow casts a stance in the second window, through the real route.
func (f closeFixture) voteInWindow(t *testing.T, tok, stance string) {
	t.Helper()
	code, body := authJSON(t, f.ts, http.MethodPost, f.windowPath()+"/position", tok,
		map[string]any{"position": stance})
	if code != http.StatusCreated {
		t.Fatalf("cast %s in the override window: %d %s", stance, code, body)
	}
}

// blockedCall is a call that closed blocked with a standing objection: the state a
// window opens on. It returns the fixture and the tokens of the voters who consented.
func blockedCall(t *testing.T, consents int) (closeFixture, []string) {
	t.Helper()
	f := newCloseFixture(t)
	toks := make([]string, 0, consents)
	for i := 0; i < consents; i++ {
		tok := voterFor(t, f.ts, f.db, f.pid, "window-consent-"+strconv.Itoa(i))
		castAs(t, f.ts, f.slug, f.call, tok, "consent")
		toks = append(toks, tok)
	}
	blocker := voterFor(t, f.ts, f.db, f.pid, "window-blocker")
	castAs(t, f.ts, f.slug, f.call, blocker, "block")
	code, body := authJSON(t, f.ts, http.MethodPost, f.callPath()+"/objection", blocker,
		map[string]any{
			"principle": "Reversibility",
			"violation": "a half-written export is unrecoverable",
			"remedy":    "keep the last complete file",
		})
	if code != http.StatusCreated {
		t.Fatalf("create objection: %d %s", code, body)
	}
	if code, body := authJSON(t, f.ts, http.MethodPost, f.callPath()+"/close", f.ownerToken(t), nil); code != http.StatusOK {
		t.Fatalf("close the blocked call: %d %s", code, body)
	}
	if got := f.storedResult(t); got != "blocked" {
		t.Fatalf("fixture closed %q, not blocked; the window opens on nothing then", got)
	}
	return f, toks
}

func (f closeFixture) windowRow(t *testing.T) (status string, result *string, note string) {
	t.Helper()
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT status, result, COALESCE(note,'') FROM override_windows WHERE call_id = ?`,
		f.call).Scan(&status, &result, &note); err != nil {
		t.Fatalf("read override window: %v", err)
	}
	return status, result, note
}

// The whole flow, over HTTP: open, vote, carry, and the record that has to survive it.
func TestAnOverrideWindowCarriesOverHTTP(t *testing.T) {
	f, consents := blockedCall(t, 4)
	owner := f.ownerToken(t)
	f.openWindow(t, owner)
	for _, tok := range consents {
		f.voteInWindow(t, tok, "consent")
	}

	note := "the export resumes from the last complete file, so nothing half-written survives"
	code, body := authJSON(t, f.ts, http.MethodPost, f.windowPath()+"/close", owner,
		map[string]any{"note": note})
	if code != http.StatusOK {
		t.Fatalf("close the window: %d %s", code, body)
	}
	// The wire names, asserted as NAMES. The window is a new response body and the same
	// trap applies: `json.Unmarshal` matches field names case-insensitively, so a Go
	// test that decodes into a tagged struct cannot tell `{"Result":…}` from
	// `{"result":…}` while the browser -- which looks up `data.result` -- gets undefined.
	for _, want := range []string{`"status"`, `"result"`, `"consent"`, `"quorum_needed"`, `"call_id"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the window response has no %s key: %s", want, body)
		}
	}
	for _, unwanted := range []string{`"Status"`, `"Result"`, `"Consent"`, `"QuorumNeeded"`} {
		if strings.Contains(string(body), unwanted) {
			t.Errorf("the window response spells %s in PascalCase: %s", unwanted, body)
		}
	}
	var win struct {
		Status string  `json:"status"`
		Result *string `json:"result"`
		Note   string  `json:"note"`
		CallID int64   `json:"call_id"`
	}
	if err := json.Unmarshal(body, &win); err != nil {
		t.Fatalf("decode window: %v (%s)", err, body)
	}
	if win.Result == nil || *win.Result != "overridden" {
		t.Fatalf("window result = %v; 4 consents of 4 non-abstain participants carries", win.Result)
	}
	if win.Status != "closed" || !strings.Contains(win.Note, "last complete file") {
		t.Errorf("window is %s with note %q after a carrying close", win.Status, win.Note)
	}
	if got := f.storedResult(t); got != "accepted_overridden" {
		t.Errorf("the call's result is %q; the window's verdict has to reach the call it decided", got)
	}
	_, status, resolution := f.objectionRow(t)
	if status != "overridden" {
		t.Errorf("the objection is %q after an override; a call accepted over a block whose block "+
			"is still open is the record the seventh pass removed", status)
	}
	if !strings.Contains(resolution, "last complete file") {
		t.Errorf("the objection's resolution is %q; the remedy's answer belongs where the objector reads it",
			resolution)
	}

	// And the window has to be readable, with its counts at last visible: §6.6's hidden
	// tally is about a RUNNING count, and this one has stopped.
	code, body = authGet(t, f.ts, f.windowPath(), owner)
	if code != http.StatusOK {
		t.Fatalf("read the window: %d %s", code, body)
	}
	var read struct {
		Consent      int    `json:"consent"`
		Participants int    `json:"participants"`
		QuorumNeeded int    `json:"quorum_needed"`
		MyPosition   string `json:"my_position"`
	}
	if err := json.Unmarshal(body, &read); err != nil {
		t.Fatalf("decode window read: %v (%s)", err, body)
	}
	if read.Consent != 4 {
		t.Errorf("the closed window reports consent=%d, want 4: a closed window's counts are the "+
			"record of the decision", read.Consent)
	}
	if read.Participants != 4 || read.QuorumNeeded == 0 {
		t.Errorf("the closed window reports participants=%d quorum_needed=%d", read.Participants, read.QuorumNeeded)
	}
}

// The remedy answered in writing is required when the window CARRIES, and the refusal
// happens before any write -- the same discipline the seventh pass applied to the close.
func TestAnOverrideWindowNeedsTheRemedyAnsweredInWritingOverHTTP(t *testing.T) {
	f, consents := blockedCall(t, 4)
	owner := f.ownerToken(t)
	f.openWindow(t, owner)
	for _, tok := range consents {
		f.voteInWindow(t, tok, "consent")
	}

	code, body := authJSON(t, f.ts, http.MethodPost, f.windowPath()+"/close", owner, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("the window carried with no note: status %d body %s\n"+
			"§6.6 records an override \"with the objector's remedy answered in writing\"", code, body)
	}
	if !strings.Contains(string(body), "remedy") {
		t.Errorf("the refusal does not say what is missing: %s", body)
	}
	status, result, _ := f.windowRow(t)
	if status != "open" || result != nil {
		t.Errorf("window status=%s result=%v after a refused close; want it left open with no verdict",
			status, result)
	}
	if got := f.storedResult(t); got != "blocked" {
		t.Errorf("the call's result is %q after a refused close", got)
	}
	if _, status, _ := f.objectionRow(t); status != "open" {
		t.Errorf("the objection is %q after a refused close; want it left open", status)
	}
}

// §6.8 puts "call and close consensus" in the maintainer's column, and the second
// window is both acts again. The refusal has to be earned: a contributor -- who IS an
// eligible collaborator, so the eligibility gate would let them through -- is refused,
// and the owner is not.
func TestTheOverrideWindowNeedsAMaintainerToOpenAndToClose(t *testing.T) {
	f, consents := blockedCall(t, 3)
	owner := f.ownerToken(t)
	contributor := consents[0] // eligible, and not a maintainer

	code, body := authJSON(t, f.ts, http.MethodPost, f.windowPath(), contributor, nil)
	if code != http.StatusForbidden {
		t.Fatalf("a contributor opened the override window: status %d body %s; §6.8 gives "+
			"\"call and close consensus\" to the maintainer", code, body)
	}
	if !strings.Contains(string(body), "maintainer") {
		t.Errorf("the refusal does not name the role: %s", body)
	}

	f.openWindow(t, owner)
	f.voteInWindow(t, contributor, "consent")
	code, body = authJSON(t, f.ts, http.MethodPost, f.windowPath()+"/close", contributor,
		map[string]any{"note": "the export resumes"})
	if code != http.StatusForbidden {
		t.Fatalf("a contributor closed the override window: status %d body %s", code, body)
	}
	status, _, _ := f.windowRow(t)
	if status != "open" {
		t.Errorf("the window is %q after a refused close", status)
	}
}

// The window's voters are §8.2's population, like the first window's: an outsider is
// refused, and a member with no contribution is refused, or a membership-only gate
// would pass this.
func TestAStanceInTheOverrideWindowNeedsBothHalvesOfEligibility(t *testing.T) {
	f, consents := blockedCall(t, 3)
	f.openWindow(t, f.ownerToken(t))

	outsider := registerAndLogin(t, f.ts, "window-outsider")
	code, body := authJSON(t, f.ts, http.MethodPost, f.windowPath()+"/position", outsider,
		map[string]any{"position": "consent"})
	if code != http.StatusForbidden {
		t.Fatalf("an outsider voted in the override window: status %d body %s; §8.2's pool is "+
			"what counts toward its quorum", code, body)
	}
	if !strings.Contains(string(body), "8.2") {
		t.Errorf("the refusal does not name §8.2: %s", body)
	}

	// A member with no qualifying contribution: the other half.
	memberOnly := registerAndLogin(t, f.ts, "window-member-only")
	addMemberOnly(t, f.db, f.pid, userIDNamed(t, f.db, "window-member-only"), "window-member-only", "contributor")
	code, body = authJSON(t, f.ts, http.MethodPost, f.windowPath()+"/position", memberOnly,
		map[string]any{"position": "consent"})
	if code != http.StatusForbidden {
		t.Fatalf("a member with no contribution voted: status %d body %s; §8.2 is a conjunction",
			code, body)
	}

	// And the gate is not refusing everybody.
	f.voteInWindow(t, consents[0], "consent")
}

// A call with no window is a 404 on the read, not an empty window: the difference
// between "there is no window" and "the window has no voters yet" is the difference
// between not showing a control and showing a vote with nobody in it.
func TestReadingTheOverrideWindowOfACallThatHasNoneIs404(t *testing.T) {
	f := newCloseFixture(t)
	code, body := authGet(t, f.ts, f.windowPath(), f.ownerToken(t))
	if code != http.StatusNotFound {
		t.Fatalf("read the window of a call that has none: status %d body %s", code, body)
	}
}

// The read carries the reader's own stance, which is what the page marks their button
// with -- and it is the only thing about the window the client is told.
func TestTheWindowReadCarriesTheReadersOwnStance(t *testing.T) {
	f, consents := blockedCall(t, 3)
	f.openWindow(t, f.ownerToken(t))
	f.voteInWindow(t, consents[0], "stand_aside")

	code, body := authGet(t, f.ts, f.windowPath(), consents[0])
	if code != http.StatusOK {
		t.Fatalf("read the window: %d %s", code, body)
	}
	var read struct {
		MyPosition   string `json:"my_position"`
		Consent      int    `json:"consent"`
		StandAside   int    `json:"stand_aside"`
		Participants int    `json:"participants"`
	}
	if err := json.Unmarshal(body, &read); err != nil {
		t.Fatalf("decode window read: %v (%s)", err, body)
	}
	if read.MyPosition != "stand_aside" {
		t.Errorf("my_position = %q, want stand_aside: the page cannot mark a button it is not told about",
			read.MyPosition)
	}
	if read.Participants != 1 {
		t.Errorf("participants = %d; §6.6 keeps participation progress visible while a window is open",
			read.Participants)
	}
	if read.Consent != 0 || read.StandAside != 0 {
		t.Errorf("an OPEN window reports counts (consent=%d stand_aside=%d); a running count is a bandwagon",
			read.Consent, read.StandAside)
	}

	// A reader who has not voted gets "", and an anonymous reader gets the same thing --
	// which is why the page must not read standing out of it.
	code, body = authGet(t, f.ts, f.windowPath(), consents[1])
	if code != http.StatusOK {
		t.Fatalf("read the window as another voter: %d %s", code, body)
	}
	if strings.Contains(string(body), `"my_position":"stand_aside"`) {
		t.Errorf("another reader is shown a stance that is not theirs: %s", body)
	}
}
