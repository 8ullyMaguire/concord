package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/ranking"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// §8.3's formula at the vote boundary, and the bug this file exists to keep
// fixed: the solution vote used to record whatever weight the client sent.
//
// §8.3 says "money cannot buy weight", and the feature-vote handler has always
// computed the weight server-side. The solution vote did not, so a POST with
// "weight": 3.0 bought a maximum-weight vote from a brand-new account, and
// nothing bounded it. The field is still accepted so an old client does not
// break, but it is ignored.

// solutionAuthor registers an account that proposes the solutions under test.
//
// §5.2 refuses a vote from the person who wrote either side, so a vote fixture
// needs an author who is not the voter. Every test in this file uses a fresh
// voter *and* a fresh author: reusing one across the two roles would trade a
// clear 403 for a confusing one.
func solutionAuthor(t *testing.T, ts *httptest.Server, name string) string {
	t.Helper()
	return registerOn(t, ts, name)
}

// ageTheAccount backdates a user's created_at past §12.3's age gate.
//
// Without this every weight in these tests is 0, because the gate ramps a new
// account from zero and a fixture-registered account is seconds old. That is the
// gate working, not a bug -- but a weight of 0.0 makes every assertion here
// vacuous, because 0.0 is also what "the client asked for 3.0 and we ignored it"
// would look like if the bug were still present in a different form.
func ageTheAccount(t *testing.T, st *store.DB, username string) {
	t.Helper()
	old := float64(time.Now().Add(-90 * 24 * time.Hour).Unix())
	if _, err := st.ExecContext(t.Context(),
		`UPDATE users SET created_at = ? WHERE username = ?`, old, username); err != nil {
		t.Fatalf("backdate %s: %v", username, err)
	}
}

// mustUserID resolves a username to its id in the test database, so a test can
// set up attribution directly rather than only through the API.
func mustUserID(t *testing.T, st *store.DB, username string) int64 {
	t.Helper()
	u, err := st.GetUser(t.Context(), username)
	if err != nil {
		t.Fatalf("GetUser(%s): %v", username, err)
	}
	return u.ID
}

// voteSolutionOn posts a solution vote and returns the response and body.
func voteSolutionOn(t *testing.T, ts *httptest.Server, featureID, a, b int64, outcome, extra, token string) (*http.Response, map[string]any) {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%s/features/%d/solutions/vote", solProject, featureID)
	return postJSON(t, ts, path, fmt.Sprintf(
		`{"solution_a":%d,"solution_b":%d,"outcome":%q%s}`,
		a, b, outcome, extra), "Authorization", "Bearer "+token)
}

// voteWeightOf pulls the recorded weight out of a vote response.
func voteWeightOf(t *testing.T, body map[string]any) float64 {
	t.Helper()
	vote, ok := body["vote"].(map[string]any)
	if !ok {
		t.Fatalf("no vote in %v", body)
	}
	w, _ := vote["weight"].(float64)
	return w
}

// TestASolutionVoteIgnoresAClientSuppliedWeight is the regression. The fixture
// deliberately gives the voter no reputation, so a server-computed weight is
// close to the floor and any large number the client asked for is obviously
// wrong rather than coincidentally right.
func TestASolutionVoteIgnoresAClientSuppliedWeight(t *testing.T) {
	ts, st, featureID := solutionFixture(t)
	author := solutionAuthor(t, ts, "weightbuyauthor")
	voter := registerOn(t, ts, "weightbuy1")
	ageTheAccount(t, st, "weightbuy1")
	a := fileSolution(t, ts, featureID, "Rewrite the exporter", "build-new", "", author)
	b := fileSolution(t, ts, featureID, "Document the workaround", "workaround", "", author)

	resp, body := voteSolutionOn(t, ts, featureID, a, b, "a", `,"weight":3.0`, voter)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("vote: %d %v", resp.StatusCode, body)
	}
	vote, _ := body["vote"].(map[string]any)
	if vote == nil {
		t.Fatalf("no vote in the response: %v", body)
	}
	weight, _ := vote["weight"].(float64)
	// The ceiling is 3.0. A brand-new account with no reputation and an untagged
	// solution cannot reach it by any legitimate route.
	if weight >= 3.0 {
		t.Errorf("recorded weight %.4f: a client-supplied weight was honoured", weight)
	}
	if weight <= 0 {
		t.Errorf("recorded weight %.4f: a brand-new account should still have some weight", weight)
	}
}

// TestTheRecordedWeightIsIndependentOfWhatTheClientAsksFor pins the property
// rather than one value: two votes by the same voter, one asking for 3.0 and one
// asking for 0.01, must record the same weight. A handler that read the field
// would fail this even if the first test's number happened to be off.
func TestTheRecordedWeightIsIndependentOfWhatTheClientAsksFor(t *testing.T) {
	ts, st, featureID := solutionFixture(t)
	author := solutionAuthor(t, ts, "weightbuy2author")
	voter := registerOn(t, ts, "weightbuy2")
	ageTheAccount(t, st, "weightbuy2")
	a := fileSolution(t, ts, featureID, "One", "build-new", "", author)
	b := fileSolution(t, ts, featureID, "Two", "workaround", "", author)

	// Same two solutions, opposite direction, so this is a second vote rather
	// than an update of the first.
	_, high := voteSolutionOn(t, ts, featureID, a, b, "a", `,"weight":3.0`, voter)
	wHigh := voteWeightOf(t, high)
	_, low := voteSolutionOn(t, ts, featureID, b, a, "b", `,"weight":0.01`, voter)
	wLow := voteWeightOf(t, low)
	if wHigh != wLow {
		t.Errorf("asking for 3.0 recorded %.4f and asking for 0.01 recorded %.4f; the body is being read",
			wHigh, wLow)
	}
}

// TestExpertiseRaisesASolutionVoteWeight is the positive half: §6.4's weighting
// has to actually do something, or the fix above has only removed a knob.
func TestExpertiseRaisesASolutionVoteWeight(t *testing.T) {
	ts, st, featureID := solutionFixture(t)
	expert := registerOn(t, ts, "expertisev1")
	plain := registerOn(t, ts, "expertisev2")
	ageTheAccount(t, st, "expertisev1")
	ageTheAccount(t, st, "expertisev2")

	// The expert must have SHIPPED a Rust solution for tag reputation to exist.
	// Tag reputation is derived from real work and never declared, so the fixture
	// has to actually do the work -- an earlier version of this test updated rows
	// matching a solution the expert had never filed, silently matched nothing,
	// and skipped.
	expertID := mustUserID(t, st, "expertisev1")
	if _, err := st.ExecContext(t.Context(), `
		INSERT INTO solutions (feature_id, author_id, title, type, status,
		                       expertise_tags, created_at, updated_at)
		VALUES (?, ?, 'An earlier Rust rewrite', 'build-new', 'shipped', 'rust', 0, 0)`,
		featureID, expertID); err != nil {
		t.Fatalf("expert contribution: %v", err)
	}

	// The solutions under comparison: one tagged, one not, both by a third party
	// so neither voter is disqualified by §5.2.
	author := solutionAuthor(t, ts, "expertisev1author")
	tagged := fileSolution(t, ts, featureID, "Rewrite the exporter in Rust", "build-new",
		`,"expertise_tags":"rust"`, author)
	other := fileSolution(t, ts, featureID, "Do nothing", "do-nothing", "", author)

	// Both voters get identical project reputation, so any difference in the
	// recorded weight can only have come from the expertise multiplier.
	for _, name := range []string{"expertisev1", "expertisev2"} {
		if _, err := st.ExecContext(t.Context(), `
			INSERT INTO reputation_events (project_id, user_id, kind, points, created_at)
			VALUES (1, ?, 'vote', 5.0, 0)`, mustUserID(t, st, name)); err != nil {
			t.Fatalf("bank reputation for %s: %v", name, err)
		}
	}

	// Guards. Each of these has already been a silent skip in this file: a test
	// whose fixture came out empty is indistinguishable from a passing one.
	perTag, err := st.TagReputation(t.Context(), 1, expertID)
	if err != nil {
		t.Fatalf("TagReputation: %v", err)
	}
	if perTag["rust"] <= 0 {
		t.Fatalf("the expert has no rust tag reputation (%v); the fixture proves nothing", perTag)
	}
	if perTag["rust"] >= 3*30 {
		t.Errorf("rust tag reputation %.1f already saturates the multiplier; the comparison is trivial",
			perTag["rust"])
	}
	repExpert, _ := st.GetReputation(t.Context(), 1, expertID)
	repPlain, _ := st.GetReputation(t.Context(), 1, mustUserID(t, st, "expertisev2"))
	if repExpert != repPlain {
		t.Fatalf("project reputations differ (%.1f vs %.1f); the test would pass on the reputation term alone",
			repExpert, repPlain)
	}
	// The stranger's weight must leave headroom under the cap, or the expert's
	// multiplier is invisible and the test passes for the wrong reason. This is
	// the guard that matters here: with 60 reputation the base term is
	// 1+log10(61) = 2.79, and a 1.1x multiplier on top of that clamps to 3.0 -- the
	// original fixture produced two identical capped weights and the test failed
	// for the right reason only by accident.
	rolePlain, err := st.GetRoleForProject(t.Context(), 1, mustUserID(t, st, "expertisev2"))
	if err != nil {
		t.Fatalf("GetRoleForProject: %v", err)
	}
	basePlain := ranking.VoteWeight(repPlain, ranking.RoleMultiplier(rolePlain), 3.0)
	if basePlain <= 0 {
		t.Fatalf("the stranger's base weight is %.4f; nothing to compare", basePlain)
	}
	if basePlain >= 3.0 {
		t.Fatalf("the stranger's base weight %.4f is already at the cap; the multiplier has no room",
			basePlain)
	}

	_, expertVote := voteSolutionOn(t, ts, featureID, tagged, other, "a", "", expert)
	_, plainVote := voteSolutionOn(t, ts, featureID, tagged, other, "a", "", plain)
	wExpert := voteWeightOf(t, expertVote)
	wPlain := voteWeightOf(t, plainVote)

	if wExpert <= wPlain {
		t.Errorf("the Rust expert's vote weighs %.4f and a stranger's %.4f; §6.4's weighting did nothing",
			wExpert, wPlain)
	}
	if wExpert > 3.0 {
		t.Errorf("expert weight %.4f exceeds the cap of 3.0", wExpert)
	}
	if wPlain >= 3.0 {
		t.Errorf("the stranger's vote is already at the cap (%.4f); the comparison has no room", wPlain)
	}
}

// has to actually do something, or the fix above has only removed a knob.
func TestAnUntaggedSolutionIsUnaffectedByExpertise(t *testing.T) {
	ts, st, featureID := solutionFixture(t)
	expert := registerOn(t, ts, "untagged1")
	plain := registerOn(t, ts, "untagged2")
	ageTheAccount(t, st, "untagged1")
	ageTheAccount(t, st, "untagged2")

	// No expertise_tags on either solution.
	author := solutionAuthor(t, ts, "untagged1author")
	a := fileSolution(t, ts, featureID, "One", "build-new", "", author)
	b := fileSolution(t, ts, featureID, "Two", "workaround", "", author)

	// Give BOTH voters the same *project* reputation, and the expert extra
	// *tag* reputation on top.
	//
	// This distinction is the whole test. §8.3's formula has a reputation term
	// and an expertise_multiplier, and they are different things: a project total
	// makes someone heavier everywhere, a tag total only on tagged designs. If
	// only the expert had project reputation the two weights would differ
	// without the expertise multiplier being involved at all, and the test would
	// pass for the wrong reason -- which is the trap this file's other tests
	// already walked into once.
	for _, name := range []string{"untagged1", "untagged2"} {
		for i := 0; i < 12; i++ {
			if _, err := st.ExecContext(t.Context(), `
				INSERT INTO reputation_events (project_id, user_id, kind, points, created_at)
				VALUES (1, ?, 'vote', 10.0, 0)`, mustUserID(t, st, name)); err != nil {
				t.Fatalf("bank reputation for %s: %v", name, err)
			}
		}
	}
	// Only the expert has shipped Rust work, so only they have tag reputation.
	// No solution in this feature declares a tag, which is the point.
	//
	// It has to be work the mutation cannot reach: a test where the expert's only
	// tag reputation came from a vote on a tagged solution would still pass under a
	// global-reputation implementation, which is exactly the mutation this test
	// exists to kill.
	expertID := mustUserID(t, st, "untagged1")
	if _, err := st.ExecContext(t.Context(), `
		INSERT INTO solutions (feature_id, author_id, title, type, status,
		                       expertise_tags, created_at, updated_at)
		VALUES (?, ?, 'An older Rust contribution', 'build-new', 'shipped', 'rust', 0, 0)`,
		featureID, expertID); err != nil {
		t.Fatalf("expert contribution: %v", err)
	}
	// Guard: the tag reputation exists and the project reputations match, or the
	// comparison below cannot distinguish the two multipliers.
	perTag, err := st.TagReputation(t.Context(), 1, expertID)
	if err != nil {
		t.Fatalf("TagReputation: %v", err)
	}
	if perTag["rust"] <= 0 {
		t.Fatalf("the expert has no rust tag reputation (%v); the fixture proves nothing", perTag)
	}
	// The two tag maps must actually differ, or nothing here can distinguish a
	// tag-scoped implementation from an unscoped one.
	//
	// Known limitation, checked rather than assumed: a mutation that takes the
	// *largest* entry of the per-tag map without consulting the solution's tags
	// survives these tests, because both voters here have exactly one tag and so
	// the map has one key. The realistic version of that bug -- sourcing the
	// multiplier from project reputation, which is what a reader would write --
	// is killed by TestExpertiseRaisesASolutionVoteWeight. Closing the contrived
	// case needs a voter with two tags and a solution declaring one of them,
	// which is worth a test only if the unscoped implementation actually appears.
	plainTag, err := st.TagReputation(t.Context(), 1, mustUserID(t, st, "untagged2"))
	if err != nil {
		t.Fatalf("TagReputation: %v", err)
	}
	if perTag["rust"] == plainTag["rust"] {
		t.Fatalf("both voters have the same rust tag reputation (%v); the scoping test cannot fail", perTag)
	}
	repExpert, err := st.GetReputation(t.Context(), 1, mustUserID(t, st, "untagged1"))
	if err != nil {
		t.Fatalf("GetReputation: %v", err)
	}
	repPlain, err := st.GetReputation(t.Context(), 1, mustUserID(t, st, "untagged2"))
	if err != nil {
		t.Fatalf("GetReputation: %v", err)
	}
	if repExpert != repPlain {
		t.Fatalf("project reputations differ (%.1f vs %.1f); the test would pass on the reputation term alone",
			repExpert, repPlain)
	}

	_, expertVote := voteSolutionOn(t, ts, featureID, a, b, "a", "", expert)
	_, plainVote := voteSolutionOn(t, ts, featureID, a, b, "a", "", plain)
	if voteWeightOf(t, expertVote) != voteWeightOf(t, plainVote) {
		t.Errorf("on untagged solutions the expert weighs %.4f and the newcomer %.4f; the multiplier is not tag-scoped",
			voteWeightOf(t, expertVote), voteWeightOf(t, plainVote))
	}
}
