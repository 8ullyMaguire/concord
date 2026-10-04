package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// §6.6's hidden tally, which the first version of this page violated.
//
//	"Hidden tally. Running counts are hidden until the call closes, to prevent
//	 bandwagoning. Participation progress and the quorum bar remain visible."
//
// The page shipped showing live counts and live ratios, which is the bandwagon the
// rule exists to prevent. It is enforced in handleGetConsensus rather than in the
// client, because a client-side rule is bypassed with devtools.

// readCall GETs a call as `tok` and returns the whole decoded body.
func readCall(t *testing.T, ts *httptest.Server, slug string, callID int64, tok string) map[string]any {
	t.Helper()
	code, raw := authGet(t, ts,
		"/api/v1/projects/"+slug+"/consensus/"+strconv.FormatInt(callID, 10), tok)
	if code != http.StatusOK {
		t.Fatalf("read call: %d %s", code, raw)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// stanceKeys are the tally fields that reveal how anyone voted. None may survive on
// an open call.
var stanceKeys = []string{
	"consent", "abstain", "stand_aside", "block",
	"support_ratio", "decisive_ratio", "reluctant",
}

// An open call must not leak the running tally, and must still show the quorum bar.
func TestAnOpenCallDoesNotLeakTheRunningTally(t *testing.T) {
	ts, db, slug, pid, _ := seedPanels(t)
	call, err := db.CreateConsensusCall(context.Background(), pid, 0, 1, "Adopt X?", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	for i := 0; i < 4; i++ {
		tok := registerAndLogin(t, ts, "hidden-tally-"+strconv.Itoa(i))
		stance := "consent"
		if i == 3 {
			stance = "stand_aside"
		}
		castAs(t, ts, slug, call.ID, tok, stance)
	}

	out := readCall(t, ts, slug, call.ID, anonAuth)

	if visible, _ := out["tally_visible"].(bool); visible {
		t.Fatal("an OPEN call reports tally_visible=true; §6.6 hides running counts " +
			"until the call closes")
	}
	tally, ok := out["tally"].(map[string]any)
	if !ok {
		t.Fatalf("no tally at all: the quorum bar must still be visible. keys=%v", out)
	}
	for _, k := range stanceKeys {
		if v := tally[k]; v != nil {
			t.Errorf("an open call leaked %s=%v; §6.6 hides the running counts", k, v)
		}
	}
	// What §6.6 preserves: participation progress and the quorum bar.
	for _, k := range []string{"participants", "eligible", "quorum_required"} {
		if _, present := tally[k]; !present {
			t.Errorf("an open call dropped %q; §6.6 keeps participation and the quorum bar", k)
		}
	}
	// And no other voter's stance anywhere in the body.
	//
	// Checked on VALUES, not on the raw text. The first version grepped the body for
	// the string "stand_aside" and failed on a response that was correct: the key is
	// still present, carrying null, because `tally` keeps its shape so the client can
	// rely on it. A key name is not a leak; a stance is.
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), `"position":`) {
		t.Errorf("an open call leaked a position row: %s", raw)
	}
	for _, k := range stanceKeys {
		if strings.Contains(string(raw), `"`+k+`":0`) ||
			strings.Contains(string(raw), `"`+k+`":0.`) ||
			strings.Contains(string(raw), `"`+k+`":1`) ||
			strings.Contains(string(raw), `"`+k+`":0,`) {
			t.Errorf("an open call reports a value for %s: %s", k, raw)
		}
	}
}

// Positions are reduced to the caller's own row. §6.6 hides the tally; it does not
// hide from a voter what they themselves recorded.
func TestAnOpenCallShowsOnlyYourOwnPosition(t *testing.T) {
	ts, db, slug, pid, _ := seedPanels(t)
	call, err := db.CreateConsensusCall(context.Background(), pid, 0, 1, "Adopt X?", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	mine := registerAndLogin(t, ts, "hidden-tally-mine")
	theirs := registerAndLogin(t, ts, "hidden-tally-theirs")
	castAs(t, ts, slug, call.ID, mine, "block")
	castAs(t, ts, slug, call.ID, theirs, "consent")

	out := readCall(t, ts, slug, call.ID, mine)
	positions, ok := out["positions"].([]any)
	if !ok {
		t.Fatalf("no positions array: %v", out["positions"])
	}
	if len(positions) != 1 {
		t.Fatalf("a voter sees %d positions on an open call, want only their own", len(positions))
	}
	row, _ := positions[0].(map[string]any)
	if row["position"] != "block" {
		t.Errorf("the caller's own position reads %v, want their own \"block\"", row["position"])
	}

	// Someone who has not voted sees none, not someone else's.
	out = readCall(t, ts, slug, call.ID, registerAndLogin(t, ts, "hidden-tally-nobody"))
	if positions, _ = out["positions"].([]any); len(positions) != 0 {
		t.Errorf("a non-voter sees %d positions on an open call, want 0", len(positions))
	}
}

// A closed call reveals everything, or the rule has no end state.
func TestAClosedCallRevealsTheTally(t *testing.T) {
	ts, db, slug, pid, _ := seedPanels(t)
	ctx := context.Background()
	call, err := db.CreateConsensusCall(ctx, pid, 0, 1, "Adopt X?", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	for i := 0; i < 4; i++ {
		tok := registerAndLogin(t, ts, "closed-tally-"+strconv.Itoa(i))
		stance := "consent"
		if i == 3 {
			stance = "stand_aside"
		}
		castAs(t, ts, slug, call.ID, tok, stance)
	}
	if _, err := db.CloseConsensusCall(ctx, call.ID); err != nil {
		t.Fatalf("CloseConsensusCall: %v", err)
	}

	out := readCall(t, ts, slug, call.ID, anonAuth)
	if visible, _ := out["tally_visible"].(bool); !visible {
		t.Fatal("a CLOSED call reports tally_visible=false")
	}
	tally := out["tally"].(map[string]any)
	if got := numOf(tally["consent"]); got != 3 {
		t.Errorf("consent=%v on a closed call, want 3", tally["consent"])
	}
	if got := numOf(tally["stand_aside"]); got != 1 {
		t.Errorf("stand_aside=%v on a closed call, want 1", tally["stand_aside"])
	}
	positions, _ := out["positions"].([]any)
	if len(positions) != 4 {
		t.Errorf("a closed call shows %d positions, want all 4", len(positions))
	}
}
