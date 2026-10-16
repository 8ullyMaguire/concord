package httpapi

// §5's arena loop — the routes behind docs/specs/arena-verification-spec.md.
//
// The tests are named after the failures they catch. Three of these are the ones
// that matter:
//
//   - TestTheRecomputeIsANoOpOnAHealthyArena is §5.1's "recomputes run frequently"
//     made safe to say. Before this pass the recompute DISAGREED with the live path
//     (it scored "neither" as a draw in arenas with a baseline, wrote games = 1 for
//     every entry and applied the log in a different order), so "frequent
//     recomputes" would have meant a leaderboard that quietly got worse.
//   - TestArenaVerificationNamesTheEntryThatDrifted is the same claim from the other
//     side: a verification that can only agree is decoration.
//   - TestEveryArenaRouteRefusesAnotherProjectsArena is the anti-enumeration rule
//     from §26 and §28 applied to a fourth route family: arena ids are global.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// voteInAlternativesArena casts one comparison through the alternatives route and
// returns the response, failing the test if the vote is refused.
func voteInAlternativesArena(t *testing.T, ts *httptest.Server, slug, token string, arenaID, a, b int64, outcome, reason string) {
	t.Helper()
	payload := map[string]any{"a_project_id": a, "b_project_id": b, "outcome": outcome}
	if reason != "" {
		payload["reason"] = reason
	}
	code, body := authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/"+slug+"-owner/alternatives/"+itoa(arenaID)+"/vote", token, payload)
	if code != http.StatusOK {
		t.Fatalf("vote (%s): %d body=%s", outcome, code, body)
	}
}

func TestArenaNextPairLabelsBothSides(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	arenaID, projA, projB, asker := seedAlternatives(t, db, ts, "pairlabels", "teams needing audit logs")

	code, body := authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/pairlabels-owner/arenas/"+itoa(arenaID)+"/next-pair", asker, nil)
	if code != http.StatusOK {
		t.Fatalf("next-pair: %d body=%s", code, body)
	}
	var pair struct {
		ArenaID  int64  `json:"arena_id"`
		Question string `json:"question"`
		A        struct {
			EntityType     string  `json:"entity_type"`
			EntityID       int64   `json:"entity_id"`
			Label          string  `json:"label"`
			R              float64 `json:"r"`
			RD             float64 `json:"rd"`
			Conservative   float64 `json:"conservative"`
			WinProbability float64 `json:"win_probability"`
		} `json:"a"`
		B struct {
			EntityID int64  `json:"entity_id"`
			Label    string `json:"label"`
		} `json:"b"`
	}
	decodeInto(t, body, &pair)

	if pair.ArenaID != arenaID {
		t.Errorf("arena_id = %d, want %d", pair.ArenaID, arenaID)
	}
	if pair.Question == "" {
		t.Error("the pair carries no question: §5 says an arena is competitors AND a question, and a " +
			"voter asked to choose between two things by name alone is being asked to guess")
	}
	// A voter has to know WHAT they are judging. An empty label is the failure this
	// catches: the ids are there and the response renders two unlabelled blobs.
	if pair.A.Label == "" || pair.B.Label == "" {
		t.Errorf("unlabelled sides: a=%q b=%q (ids %d/%d)",
			pair.A.Label, pair.B.Label, pair.A.EntityID, pair.B.EntityID)
	}
	if pair.A.EntityType != "project" {
		t.Errorf("entity_type = %q, want project: this is an alternatives arena", pair.A.EntityType)
	}
	// Both competitors, not one competitor twice.
	if pair.A.EntityID == pair.B.EntityID {
		t.Errorf("both sides are entity %d", pair.A.EntityID)
	}
	seen := map[int64]bool{projA: true, projB: true}
	if !seen[pair.A.EntityID] || !seen[pair.B.EntityID] {
		t.Errorf("the pair is %d vs %d, and the arena holds %d and %d",
			pair.A.EntityID, pair.B.EntityID, projA, projB)
	}
	// §5.1's display: the conservative score is r - 2*RD, and the win probability
	// is the Glicko expectation against the other side. A pair of equal priors is
	// 0.5, so a zero here means the field was never filled.
	if pair.A.Conservative != pair.A.R-2*pair.A.RD && pair.A.RD != 0 {
		t.Errorf("conservative = %.3f, want r - 2*rd = %.3f", pair.A.Conservative, pair.A.R-2*pair.A.RD)
	}
	if pair.A.WinProbability == 0 {
		t.Error("win_probability is 0: §5.1 shows it to the voter as \"A is better with N% confidence\"")
	}
}

func TestArenaNextPairSaysWhenEveryPairIsJudged(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	arenaID, projA, projB, asker := seedAlternatives(t, db, ts, "judged", "teams needing audit logs")

	// The asker is the only identity that owns neither competitor, so they are the
	// one who can vote here. One pair, one vote: the arena is exhausted for them.
	voteInAlternativesArena(t, ts, "judged", asker, arenaID, projA, projB, "a", "")

	code, body := authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/judged-owner/arenas/"+itoa(arenaID)+"/next-pair", asker, nil)
	if code != http.StatusNotFound {
		t.Fatalf("next-pair for a voter who judged everything: %d body=%s, want 404", code, body)
	}
	if !strings.Contains(string(body), "judged every pair") {
		t.Errorf("the refusal does not say why: %s", body)
	}
}

func TestArenaVotesCarriesTheComparisonAndItsReason(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	arenaID, projA, projB, asker := seedAlternatives(t, db, ts, "logread", "teams needing audit logs")

	before, body := authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/logread-owner/arenas/"+itoa(arenaID)+"/votes", asker, nil)
	if before != http.StatusOK {
		t.Fatalf("votes (empty): %d body=%s", before, body)
	}
	var empty struct {
		Votes []json.RawMessage `json:"votes"`
	}
	decodeInto(t, body, &empty)
	if empty.Votes == nil {
		t.Error("votes is null for an arena with no votes: a client then has to tell null from []")
	}

	voteInAlternativesArena(t, ts, "logread", asker, arenaID, projA, projB, "a", "audit logs are the whole point")

	code, body := authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/logread-owner/arenas/"+itoa(arenaID)+"/votes", asker, nil)
	if code != http.StatusOK {
		t.Fatalf("votes: %d body=%s", code, body)
	}
	var log struct {
		Count int `json:"count"`
		Votes []struct {
			Outcome string `json:"outcome"`
			Reason  string `json:"reason"`
			A       struct {
				Label string `json:"label"`
			} `json:"a"`
			B struct {
				Label string `json:"label"`
			} `json:"b"`
		} `json:"votes"`
	}
	decodeInto(t, body, &log)
	if log.Count != 1 || len(log.Votes) != 1 {
		t.Fatalf("log has count=%d and %d rows after one vote", log.Count, len(log.Votes))
	}
	if log.Votes[0].Outcome != "a" {
		t.Errorf("outcome = %q, want a", log.Votes[0].Outcome)
	}
	if log.Votes[0].Reason != "audit logs are the whole point" {
		t.Errorf("reason = %q: §5.2's audit view is for appeals, and an appeal without the "+
			"voter's own words is a rating with no defence", log.Votes[0].Reason)
	}
	if log.Votes[0].A.Label == "" || log.Votes[0].B.Label == "" {
		t.Errorf("the log row does not name what was compared: a=%q b=%q",
			log.Votes[0].A.Label, log.Votes[0].B.Label)
	}
}

func TestTheRecomputeIsANoOpOnAHealthyArena(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	arenaID, projA, projB, asker := seedAlternatives(t, db, ts, "noop", "teams needing audit logs")
	voteInAlternativesArena(t, ts, "noop", asker, arenaID, projA, projB, "a", "")

	// The verification first: the live path wrote the ratings, and the log has to
	// reproduce them. This is §5.1's transparency claim over the wire.
	code, body := authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/noop-owner/arenas/"+itoa(arenaID)+"/verification", asker, nil)
	if code != http.StatusOK {
		t.Fatalf("verification: %d body=%s", code, body)
	}
	var verified struct {
		Agrees    bool `json:"agrees"`
		Divergent int  `json:"divergent"`
		Entries   []struct {
			StoredR   float64 `json:"stored_r"`
			ReplayedR float64 `json:"replayed_r"`
			Agrees    bool    `json:"agrees"`
		} `json:"entries"`
	}
	decodeInto(t, body, &verified)
	if !verified.Agrees || verified.Divergent != 0 {
		t.Errorf("the live ratings are not the ones the log produces: agrees=%v divergent=%d",
			verified.Agrees, verified.Divergent)
		for _, e := range verified.Entries {
			if !e.Agrees {
				t.Errorf("  entry: stored r=%.3f replayed r=%.3f", e.StoredR, e.ReplayedR)
			}
		}
	}
	if len(verified.Entries) != 2 {
		t.Errorf("verified %d entries, want the arena's 2", len(verified.Entries))
	}

	// And the repair is therefore a no-op, which is the property that makes
	// "recomputes run frequently" safe to promise.
	code, body = authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/noop-owner/arenas/"+itoa(arenaID)+"/recompute", asker, nil)
	if code != http.StatusOK {
		t.Fatalf("recompute: %d body=%s", code, body)
	}
	var recomputed struct {
		Changed int `json:"changed"`
		Entries []struct {
			Label string `json:"label"`
		} `json:"entries"`
	}
	decodeInto(t, body, &recomputed)
	if recomputed.Changed != 0 {
		t.Errorf("the recompute changed %d entries of an arena the verification agreed with", recomputed.Changed)
	}
	if len(recomputed.Entries) != 2 {
		t.Errorf("recompute returned %d entries, want 2", len(recomputed.Entries))
	}
}

func TestArenaVerificationNamesTheEntryThatDrifted(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	arenaID, _, _, asker := seedAlternatives(t, db, ts, "drift", "teams needing audit logs")

	// Drift is made real, not simulated: the log is append-only, so a stored rating
	// the log does not produce can only come from a write that scored something the
	// log does not contain.
	if _, err := db.ExecContext(context.Background(),
		`UPDATE arena_entries SET r = r + 40 WHERE arena_id = ?`, arenaID); err != nil {
		t.Fatalf("perturb: %v", err)
	}

	code, body := authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/drift-owner/arenas/"+itoa(arenaID)+"/verification", asker, nil)
	if code != http.StatusOK {
		t.Fatalf("verification: %d body=%s", code, body)
	}
	var verified struct {
		Agrees    bool `json:"agrees"`
		Divergent int  `json:"divergent"`
	}
	decodeInto(t, body, &verified)
	if verified.Agrees {
		t.Error("a verification that reports agreement for a rating the log does not produce is " +
			"decoration: §5.1's claim would hold by construction")
	}
	if verified.Divergent != 2 {
		t.Errorf("divergent = %d, want both entries (the perturbation hit the whole arena)", verified.Divergent)
	}

	// A contributor may repair it, and afterwards the log agrees again.
	code, body = authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/drift-owner/arenas/"+itoa(arenaID)+"/recompute", asker, nil)
	if code != http.StatusOK {
		t.Fatalf("recompute: %d body=%s", code, body)
	}
	var repaired struct {
		Changed int `json:"changed"`
	}
	decodeInto(t, body, &repaired)
	if repaired.Changed != 2 {
		t.Errorf("changed = %d, want 2", repaired.Changed)
	}

	code, body = authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/drift-owner/arenas/"+itoa(arenaID)+"/verification", asker, nil)
	if code != http.StatusOK {
		t.Fatalf("verification after repair: %d body=%s", code, body)
	}
	var after struct {
		Agrees bool `json:"agrees"`
	}
	decodeInto(t, body, &after)
	if !after.Agrees {
		t.Error("the arena still disagrees with its own log after a recompute: the repair did not repair")
	}
}

func TestEveryArenaRouteRefusesAnotherProjectsArena(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	arenaID, _, _, asker := seedAlternatives(t, db, ts, "owned", "teams needing audit logs")
	createTestProjectWithAuth(t, ts, asker, "somebody-else")

	for _, tc := range []struct {
		method, suffix string
	}{
		{http.MethodGet, "/next-pair"},
		{http.MethodGet, "/votes"},
		{http.MethodGet, "/verification"},
		{http.MethodPost, "/recompute"},
	} {
		url := "/api/v1/projects/somebody-else/arenas/" + itoa(arenaID) + tc.suffix
		code, body := authJSON(t, ts, tc.method, url, asker, nil)
		if code != http.StatusNotFound {
			t.Errorf("%s %s answered %d, want 404 (body=%s): arena ids are global, so a distinct "+
				"refusal confirms the id exists", tc.method, tc.suffix, code, body)
		}
	}
}

func TestArenaRecomputeNeedsAnAccountAndAContributor(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	arenaID, _, _, _ := seedAlternatives(t, db, ts, "guarded", "teams needing audit logs")
	audience := registerAndLogin(t, ts, "guarded-audience") // an account, no role on the project
	url := "/api/v1/projects/guarded-owner/arenas/" + itoa(arenaID) + "/recompute"

	code, body := authJSON(t, ts, http.MethodPost, url, "", nil)
	if code != http.StatusUnauthorized {
		t.Errorf("anonymous recompute: %d body=%s, want 401 -- the repair writes ratings", code, body)
	}
	code, body = authJSON(t, ts, http.MethodPost, url, audience, nil)
	if code != http.StatusForbidden {
		t.Errorf("a signed-in onlooker's recompute: %d body=%s, want 403", code, body)
	}
	// And the read it depends on needs no role at all: §5.1 says ANYONE can verify.
	code, body = authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/guarded-owner/arenas/"+itoa(arenaID)+"/verification", "", nil)
	if code != http.StatusOK {
		t.Errorf("anonymous verification: %d body=%s, want 200 -- \"Anyone can verify a ranking\"", code, body)
	}
}

func TestAnArenaRouteRejectsAGarbageArenaId(t *testing.T) {
	ts, _ := newTestServerWithStore(t)
	createTestProjectWithAuth(t, ts, registerAndLogin(t, ts, "garbage"), "garbage")
	code, body := authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/garbage/arenas/not-a-number/verification", "", nil)
	if code != http.StatusBadRequest {
		t.Errorf("garbage arena id: %d body=%s, want 400", code, body)
	}
}
