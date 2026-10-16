package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// Who may close a call, who may record a stance, and what the close leaves behind.
//
// Three gaps measured against the same fixture, all of the family "the rule exists and
// nothing enforced it":
//
//  1. `POST /consensus/{call_id}/close` was gated on nothing but a session. It is the
//     act that fixes a call's result, and §6.8 puts "call and close consensus" in the
//     maintainer's column -- so any account could decide any project's call.
//  2. `POST /position` and `POST /objection` were gated on nothing but a session too.
//     §6.6 opens a position to "everyone eligible", and a position is counted in
//     `Participants`, which is what quorum is measured against -- so a stance from
//     outside the pool both met quorum and moved both ratios.
//  3. the close recorded nothing: `consensus_calls.result` was left NULL, and an
//     override left the objection it overrode still standing.
//
// The fixture is one project, one open call, and accounts minted with each kind of
// standing, because the refusals have to be told apart: a test that only ever tries an
// account with no membership passes against a gate that only checks membership.

type closeFixture struct {
	ts   *httptest.Server
	db   *store.DB
	slug string
	pid  int64
	call int64
}

func newCloseFixture(t *testing.T) closeFixture {
	t.Helper()
	ts, db, slug, pid, _ := seedPanels(t)
	call, err := db.CreateConsensusCall(context.Background(), pid, 0, 1, "Adopt X?", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	return closeFixture{ts: ts, db: db, slug: slug, pid: pid, call: call.ID}
}

func (f closeFixture) callPath() string {
	return "/api/v1/projects/" + f.slug + "/consensus/" + strconv.FormatInt(f.call, 10)
}

// ownerToken is the harness actor, who created the project and is therefore the owner.
//
// `tokenFor` returns the token alone; the scheme is part of an Authorization header, and
// a bare token there is an unauthenticated request (which is what it looked like the
// first time this ran: 401 on every close).
func (f closeFixture) ownerToken(t *testing.T) string {
	t.Helper()
	return "Bearer " + tokenFor(t, f.db, "testuser")
}

func (f closeFixture) status(t *testing.T) string {
	t.Helper()
	var status string
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT status FROM consensus_calls WHERE id = ?`, f.call).Scan(&status); err != nil {
		t.Fatalf("read call status: %v", err)
	}
	return status
}

func (f closeFixture) storedResult(t *testing.T) string {
	t.Helper()
	var result *string
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT result FROM consensus_calls WHERE id = ?`, f.call).Scan(&result); err != nil {
		t.Fatalf("read call result: %v", err)
	}
	if result == nil {
		return "<NULL>"
	}
	return *result
}

// objectionRow reads the one objection on the call.
func (f closeFixture) objectionRow(t *testing.T) (id int64, status, resolution string) {
	t.Helper()
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT id, status, COALESCE(resolution, '') FROM objections WHERE call_id = ?`,
		f.call).Scan(&id, &status, &resolution); err != nil {
		t.Fatalf("read objection: %v", err)
	}
	return id, status, resolution
}

// blockWith builds the override case: `consents` eligible voters consenting and one
// eligible voter blocking and raising an objection, so the call's decisive ratio sits
// exactly at the default charter's override ratio (a pass).
func (f closeFixture) blockWith(t *testing.T, consents int) {
	t.Helper()
	for i := 0; i < consents; i++ {
		tok := voterFor(t, f.ts, f.db, f.pid, "close-consent-"+strconv.Itoa(i))
		castAs(t, f.ts, f.slug, f.call, tok, "consent")
	}
	blocker := voterFor(t, f.ts, f.db, f.pid, "close-blocker")
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
}

func TestTheCloseRouteNeedsAMaintainer(t *testing.T) {
	f := newCloseFixture(t)
	// An eligible contributor: everything a stance needs, and not the role §6.8 gives
	// "close consensus" to.
	contributor := voterFor(t, f.ts, f.db, f.pid, "close-contributor")

	code, body := authJSON(t, f.ts, http.MethodPost, f.callPath()+"/close", contributor, nil)
	if code != http.StatusForbidden {
		t.Fatalf("a contributor closed a call: status %d body %s -- §6.8 puts "+
			"\"call and close consensus\" in the maintainer's column", code, body)
	}
	if !strings.Contains(string(body), "maintainer") {
		t.Errorf("the refusal does not say which role it wanted: %s", body)
	}
	if got := f.status(t); got != "open" {
		t.Errorf("the call is %q after a refused close; a refusal must not act", got)
	}

	code, body = authJSON(t, f.ts, http.MethodPost, f.callPath()+"/close", f.ownerToken(t), nil)
	if code != http.StatusOK {
		t.Fatalf("the owner could not close the call: %d %s", code, body)
	}
	if got := f.status(t); got != "closed" {
		t.Errorf("the call is %q after the owner closed it", got)
	}
}

// A close RECORDS a block rather than overriding it: §6.6's pass conditions end with
// "No unresolved block" and its override is a second window with its own route. What
// the close must still do is write the verdict down -- including `blocked`, which is a
// verdict and not an absence -- and leave the objection standing, because nothing
// answered it.
func TestACloseRecordsABlockRatherThanOverridingIt(t *testing.T) {
	f := newCloseFixture(t)
	f.blockWith(t, 4) // 4 consent / 1 block

	code, body := authJSON(t, f.ts, http.MethodPost, f.callPath()+"/close", f.ownerToken(t), nil)
	if code != http.StatusOK {
		t.Fatalf("close a blocked call: %d %s", code, body)
	}
	var sum struct {
		Result string `json:"result"`
		Call   struct {
			Status string  `json:"status"`
			Result *string `json:"result"`
		} `json:"call"`
	}
	if err := json.Unmarshal(body, &sum); err != nil {
		t.Fatalf("decode close response: %v (%s)", err, body)
	}
	// The wire names, asserted as names. `json.Unmarshal` matches field names
	// case-insensitively, so a Go test decoding into a tagged struct cannot tell
	// `{"Result":…}` from `{"result":…}` -- and the browser can: it looks up
	// `data.result` and gets undefined. This is the PASCALCASE trap the settings page
	// hit (governance.Charter has no tags), and the e2e suite is where it surfaced:
	// the page said "Recorded: closed" because the verdict had no key it could read.
	for _, want := range []string{`"result"`, `"call"`, `"counts"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the close response has no %s key: %s", want, body)
		}
	}
	for _, unwanted := range []string{`"Result"`, `"Call"`, `"Counts"`} {
		if strings.Contains(string(body), unwanted) {
			t.Errorf("the close response spells %s in PascalCase: %s", unwanted, body)
		}
	}
	if sum.Result != "blocked" {
		t.Fatalf("result = %q, want blocked: 4 consents and 1 block is not an override under §6.6, "+
			"and the second window is where a block is answered", sum.Result)
	}
	if sum.Call.Status != "closed" {
		t.Errorf("the response's call is %q after a successful close", sum.Call.Status)
	}
	if sum.Call.Result == nil || *sum.Call.Result != "blocked" {
		t.Errorf("the response's call carries result %v", sum.Call.Result)
	}
	if got := f.storedResult(t); got != "blocked" {
		t.Errorf("the stored result is %q; the verdict has to survive the request that "+
			"produced it, or the page renders a closed call with no outcome", got)
	}
	if _, status, _ := f.objectionRow(t); status != "open" {
		t.Errorf("the objection is %q after a blocked close; a block that stood is not overridden", status)
	}

	// And the objection has to be READABLE, not only stored. A resolution written to a
	// column no reader selects is a record in the same sense a diary in a locked drawer
	// is: the row is there and nobody can act on it. GetObjections did not select the
	// column at all -- which also meant a maintainer's reason for resolving an objection
	// (§17.3's "a resolution is recorded with its reason") was write-only.
	code, body = authGet(t, f.ts, f.callPath(), f.ownerToken(t))
	if code != http.StatusOK {
		t.Fatalf("read the closed call: %d %s", code, body)
	}
	var read struct {
		Objections []struct {
			Status     string `json:"status"`
			Resolution string `json:"resolution"`
		} `json:"objections"`
	}
	if err := json.Unmarshal(body, &read); err != nil {
		t.Fatalf("decode call: %v (%s)", err, body)
	}
	if len(read.Objections) != 1 {
		t.Fatalf("the read carries %d objections, want 1: %s", len(read.Objections), body)
	}
	if read.Objections[0].Status != "open" {
		t.Errorf("the read objection is %q after a blocked close", read.Objections[0].Status)
	}
}

// The note is required only when it has something to answer, so the close has to stay
// usable with no body at all. `readJSON` turns an empty body into "invalid JSON body:
// EOF", which would have been a 400 on every unblocked call.
func TestACloseWithNoBodyIsFineWhenNothingWasBlocked(t *testing.T) {
	f := newCloseFixture(t)
	for i := 0; i < 3; i++ {
		tok := voterFor(t, f.ts, f.db, f.pid, "plain-consent-"+strconv.Itoa(i))
		castAs(t, f.ts, f.slug, f.call, tok, "consent")
	}
	code, body := authJSON(t, f.ts, http.MethodPost, f.callPath()+"/close", f.ownerToken(t), nil)
	if code != http.StatusOK {
		t.Fatalf("close with no body: %d %s", code, body)
	}
	if got := f.storedResult(t); got != "accepted" {
		t.Errorf("stored result = %q after an accepted call closed", got)
	}
}

// A body that cannot be parsed is refused -- on the route that reads one. Since the
// eighth pass the close takes no body at all (§6.6's note belongs to the override
// window), so this checks the override window's close, where "no body" and "a body
// that is not JSON" are still two different things and only the first may be absent.
func TestAMalformedOverrideCloseBodyIsRefused(t *testing.T) {
	f := newCloseFixture(t)
	f.blockWith(t, 4)
	if code, body := authJSON(t, f.ts, http.MethodPost, f.callPath()+"/close", f.ownerToken(t), nil); code != http.StatusOK {
		t.Fatalf("close the call: %d %s", code, body)
	}
	if code, body := authJSON(t, f.ts, http.MethodPost, f.callPath()+"/override", f.ownerToken(t), nil); code != http.StatusCreated {
		t.Fatalf("open the override window: %d %s", code, body)
	}

	// Sent by hand: the helpers marshal their payload, and the point here is a body that
	// cannot be marshalled at all.
	req, err := http.NewRequest(http.MethodPost, f.ts.URL+f.callPath()+"/override/close",
		strings.NewReader(`{"note":`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", f.ownerToken(t))
	resp, err := f.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST override close: %v", err)
	}
	defer resp.Body.Close()
	raw := new(bytes.Buffer)
	raw.ReadFrom(resp.Body)
	code, body := resp.StatusCode, raw.Bytes()
	if code != http.StatusBadRequest {
		t.Fatalf("a malformed body: status %d body %s; \"no body\" and \"a body that is "+
			"not JSON\" are different things and only the first may be absent", code, body)
	}
	if got := f.status(t); got != "closed" {
		t.Errorf("the call is %q after a malformed body", got)
	}
	var windowStatus string
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT status FROM override_windows WHERE call_id = ?`, f.call).Scan(&windowStatus); err != nil {
		t.Fatalf("read window status: %v", err)
	}
	if windowStatus != "open" {
		t.Errorf("the window is %q after a malformed body; want it left open", windowStatus)
	}
}

// The refusals have to be earned: both halves of §8.2 are needed, so the test asks
// about an outsider AND a member with no contribution, and then about a member who has
// contributed -- otherwise a gate that refused everybody would pass it.
func TestAPositionNeedsBothHalvesOfEligibility(t *testing.T) {
	f := newCloseFixture(t)

	outsider := registerAndLogin(t, f.ts, "stance-outsider")
	code, body := authJSON(t, f.ts, http.MethodPost, f.callPath()+"/position", outsider,
		map[string]any{"position": "consent"})
	if code != http.StatusForbidden || !strings.Contains(string(body), "§8.2") {
		t.Errorf("an account that is not a member cast a position: %d %s", code, body)
	}

	memberName := "stance-member"
	member := registerAndLogin(t, f.ts, memberName)
	addMemberOnly(t, f.db, f.pid, userIDNamed(t, f.db, memberName), memberName, "contributor")
	code, body = authJSON(t, f.ts, http.MethodPost, f.callPath()+"/position", member,
		map[string]any{"position": "consent"})
	if code != http.StatusForbidden {
		t.Errorf("a member with no qualifying contribution cast a position: %d %s\n"+
			"§8.2 makes role AND activity necessary", code, body)
	}

	eligible := voterFor(t, f.ts, f.db, f.pid, "stance-eligible")
	code, body = authJSON(t, f.ts, http.MethodPost, f.callPath()+"/position", eligible,
		map[string]any{"position": "consent"})
	if code != http.StatusCreated {
		t.Fatalf("an eligible collaborator could not cast a position: %d %s", code, body)
	}
}

func TestABlockNeedsBothHalvesOfEligibility(t *testing.T) {
	f := newCloseFixture(t)
	block := map[string]any{
		"principle": "Fairness", "violation": "rushed", "remedy": "wait",
	}

	outsider := registerAndLogin(t, f.ts, "block-outsider")
	code, body := authJSON(t, f.ts, http.MethodPost, f.callPath()+"/objection", outsider, block)
	if code != http.StatusForbidden {
		t.Errorf("an account that is not a member raised a block: %d %s\n"+
			"an objection holds the call below every pass condition, so a stranger's "+
			"block is a stranger's veto", code, body)
	}

	memberName := "block-member"
	member := registerAndLogin(t, f.ts, memberName)
	addMemberOnly(t, f.db, f.pid, userIDNamed(t, f.db, memberName), memberName, "contributor")
	code, body = authJSON(t, f.ts, http.MethodPost, f.callPath()+"/objection", member, block)
	if code != http.StatusForbidden {
		t.Errorf("a member with no qualifying contribution raised a block: %d %s", code, body)
	}

	eligible := voterFor(t, f.ts, f.db, f.pid, "block-eligible")
	code, body = authJSON(t, f.ts, http.MethodPost, f.callPath()+"/objection", eligible, block)
	if code != http.StatusCreated {
		t.Fatalf("an eligible collaborator could not raise a block: %d %s", code, body)
	}
}

func TestOpeningACallNeedsAMaintainer(t *testing.T) {
	f := newCloseFixture(t)
	contributor := voterFor(t, f.ts, f.db, f.pid, "open-contributor")
	payload := map[string]any{"title": "Opened by a contributor", "description": "x"}

	code, body := authJSON(t, f.ts, http.MethodPost,
		"/api/v1/projects/"+f.slug+"/consensus", contributor, payload)
	if code != http.StatusForbidden || !strings.Contains(string(body), "maintainer") {
		t.Fatalf("a contributor opened a call: %d %s -- §6.8 gives \"call and close "+
			"consensus\" to the maintainer, and a call is what quorum is later measured "+
			"against", code, body)
	}

	code, body = authJSON(t, f.ts, http.MethodPost,
		"/api/v1/projects/"+f.slug+"/consensus", f.ownerToken(t), payload)
	if code != http.StatusCreated {
		t.Fatalf("the owner could not open a call: %d %s", code, body)
	}
}
