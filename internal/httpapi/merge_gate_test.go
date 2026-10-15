package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/governance"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// §5.7's merge gate, at the API boundary.
//
// The gate itself is not new. `governance.CheckMergeGate` has implemented §5.7's
// arithmetic since the beginning -- quorum threshold, reviewer requirement,
// blanket drop when the charter says so -- and `governance_test.go` pins all four
// of its branches. What was missing was a caller:
//
//	$ grep -rn 'CheckMergeGate' --include=*.go .
//	./internal/governance/governance.go:264:func CheckMergeGate(...)
//	./internal/governance/governance_test.go:140:  g := CheckMergeGate(1, 10, true, c)
//
// A pure function with one caller, its own test. Meanwhile `store.ExecuteMerge` --
// reached through PUT /api/v1/projects/{slug}/merge/{id}/execute -- required only a
// non-zero actor id, set status='merged', moved the feature to `shipped`, closed
// every complaint linked to it, and wrote an audit row. The charter's four merge
// columns (merge_requires_quorum, merge_quorum_min, merge_quorum_ratio,
// require_reviewer_approval) have existed since migration 0001 with defaults
// (1, 2, 0.25, 1) and were read and written by the store and consulted by nothing.
//
// So the default state of every Concord project was: one person who can read the
// project presses a button, and a feature lands with zero confirmations, in a
// charter that says it needs two.
//
// This is the twin of the bug `consensus_quorum_test.go` records -- there,
// `ConsensusCounts.Eligible` was never set, so `QuorumThreshold` returned 0 and the
// insufficient-quorum branch could never fire. Same family: the rule was written,
// tested in isolation, and never reached.
//
// These tests are at the API boundary because that is where the hole was: a store
// test of ExecuteMerge would have passed before the fix and after it.

// mergeFixture is a project with a validated complaint and a feature that can
// carry a merge request, plus one account that is not a member of it.
type mergeFixture struct {
	ts      *httptest.Server
	db      *store.DB
	slug    string
	pid     int64
	feature int64

	// owner is the harness's default actor: it created the project, so it holds
	// the owner role and is a member. It is the actor every request gets when a
	// test does not name somebody else.
	owner string

	// author files the merge requests, and is NOT the feature's author, because
	// §5.2 refuses a feature's own author the solutions on it and the fixtures
	// here follow the same instinct: the person landing the work is not the
	// person who asked for it.
	author      string
	authorToken string
}

func seedMerge(t *testing.T) mergeFixture {
	t.Helper()
	ts, db := newTestServerWithStore(t)
	ctx := context.Background()
	slug, pid := makeProject(t, ts, "merge-project")

	owner := "testuser" // newTestServerWithStore's account
	c, err := db.CreateComplaint(ctx, pid, userIDNamed(t, db, owner),
		"the exporter cannot resume", "body", 3, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE complaints SET status = 'validated' WHERE id = ?`, c.ID); err != nil {
		t.Fatalf("validate complaint: %v", err)
	}
	f, err := db.CreateFeature(ctx, pid, userIDNamed(t, db, owner),
		"resumable export", "body", "M", nil, nil, []int64{c.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	author := "merge-author"
	if err := db.JoinProject(ctx, pid, userIDNamed(t, db, author)); err != nil {
		t.Fatalf("JoinProject: %v", err)
	}
	return mergeFixture{
		ts: ts, db: db, slug: slug, pid: pid, feature: f.ID,
		owner: owner, author: author, authorToken: tokenFor(t, db, author),
	}
}

// userIDNamed returns an existing account's id, creating it if it is not there.
//
// mustUser is CreateUser, and the harness's default actor already exists, so a
// fixture that names the project owner has to look it up rather than re-create it.
func userIDNamed(t *testing.T, db *store.DB, name string) int64 {
	t.Helper()
	ctx := context.Background()
	if u, err := db.CreateUser(ctx, name, name); err == nil {
		return u.ID
	}
	var id int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, name).
		Scan(&id); err != nil {
		t.Fatalf("no account named %q: %v", name, err)
	}
	return id
}

// tokenFor gives a store-created account a real bearer token, the same way
// newTestServer does for its default actor.
func tokenFor(t *testing.T, db *store.DB, username string) string {
	t.Helper()
	pw := "test-password-123"
	if err := db.SetPassword(context.Background(), username, pw); err != nil {
		t.Fatalf("SetPassword(%s): %v", username, err)
	}
	_, tok, err := db.Login(context.Background(), username, pw)
	if err != nil {
		t.Fatalf("Login(%s): %v", username, err)
	}
	return tok
}

// collaborator creates an account that IS an eligible collaborator under §8.2:
// a member with the given role and a qualifying contribution. Route (1) of
// qualifyingContributionsSQL is a project document, which is the cheapest honest
// way to say "this person has done something here" -- no merge request involved,
// so a fixture that needs eligible people does not depend on the very gate under
// test.
func (f mergeFixture) collaborator(t *testing.T, name, role string) (int64, string) {
	t.Helper()
	ctx := context.Background()
	uid := userIDNamed(t, f.db, name)
	if _, err := f.db.ExecContext(ctx,
		`INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)`,
		f.pid, uid, role, float64(1)); err != nil {
		t.Fatalf("add member %s: %v", name, err)
	}
	if _, err := f.db.PutDocument(ctx, f.pid, uid,
		store.DocumentKinds[0], name+"-doc", "notes", "body"); err != nil {
		t.Fatalf("PutDocument(%s): %v", name, err)
	}
	return uid, tokenFor(t, f.db, name)
}

// outsider creates an account that is NOT a member of the project.
func (f mergeFixture) outsider(t *testing.T, name string) string {
	t.Helper()
	userIDNamed(t, f.db, name)
	return tokenFor(t, f.db, name)
}

func (f mergeFixture) openMR(t *testing.T, title string) int64 {
	t.Helper()
	resp, body := postJSON(t, f.ts,
		fmt.Sprintf("/api/v1/projects/%s/merge", f.slug),
		fmt.Sprintf(`{"feature_id":%d,"title":%q}`, f.feature, title),
		"Authorization", "Bearer "+f.authorToken)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("open merge request: %d %v", resp.StatusCode, body)
	}
	id, ok := body["id"].(float64)
	if !ok {
		t.Fatalf("no id in %v", body)
	}
	return int64(id)
}

func (f mergeFixture) approve(t *testing.T, mrID int64, tok string) (*http.Response, map[string]any) {
	t.Helper()
	return putJSON(t, f.ts,
		fmt.Sprintf("/api/v1/projects/%s/merge/%d/approve", f.slug, mrID), `{}`,
		"Authorization", "Bearer "+tok)
}

func (f mergeFixture) execute(t *testing.T, mrID int64, tok string) (*http.Response, map[string]any) {
	t.Helper()
	return putJSON(t, f.ts,
		fmt.Sprintf("/api/v1/projects/%s/merge/%d/execute", f.slug, mrID), `{}`,
		"Authorization", "Bearer "+tok)
}

// featureStatus reads what the merge is supposed to cause. Asserting only on the
// response would let a refusal that still shipped the feature pass.
func (f mergeFixture) featureStatus(t *testing.T) string {
	t.Helper()
	var status string
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT status FROM features WHERE id = ?`, f.feature).Scan(&status); err != nil {
		t.Fatalf("read feature status: %v", err)
	}
	return status
}

func (f mergeFixture) ownerToken(t *testing.T) string { return tokenFor(t, f.db, f.owner) }

// TestAMergeWithNoApprovalsIsRefused is §5.7 in one line: the charter says two
// distinct collaborator confirmations, nobody has confirmed, and the project's
// own owner presses merge.
func TestAMergeWithNoApprovalsIsRefused(t *testing.T) {
	f := seedMerge(t)
	mr := f.openMR(t, "land the resumable export")

	resp, body := f.execute(t, mr, f.ownerToken(t))
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("a merge with zero confirmations was executed: %d %v\n"+
			"§5.7 wants %s; the charter's defaults are merge_quorum_min=2, "+
			"merge_quorum_ratio=0.25, require_reviewer_approval=1",
			resp.StatusCode, body, "distinct collaborator confirmations")
	}
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("refusal was %d, want 409 (the client needs the shortfall, not "+
			"\"bad request\"): %v", resp.StatusCode, body)
	}
	if msg, _ := body["error"].(string); msg == "" {
		t.Errorf("the refusal does not say why: %v", body)
	}
	if got := f.featureStatus(t); got == "shipped" {
		t.Errorf("the feature shipped anyway: status=%q", got)
	}
}

// TestAMergeIsRefusedToSomebodyOutsideTheProject: the merge route required only a
// non-zero actor id, so a registered account with no membership at all could land
// a change and close the complaints behind it.
func TestAMergeIsRefusedToSomebodyOutsideTheProject(t *testing.T) {
	f := seedMerge(t)
	mr := f.openMR(t, "land the resumable export")
	tok := f.outsider(t, "merge-outsider")

	resp, body := f.execute(t, mr, tok)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("an account with no membership in the project executed a merge: %v", body)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("refusal was %d, want 403: %v", resp.StatusCode, body)
	}
	if got := f.featureStatus(t); got == "shipped" {
		t.Errorf("the feature shipped anyway: status=%q", got)
	}
}

// TestSomebodyOutsideTheProjectCannotConfirm: an approval is a member's act. It
// used to be any signed-in account's, which mattered once approvals started
// counting -- the population a quorum is measured against must not be fillable
// from outside it.
func TestSomebodyOutsideTheProjectCannotConfirm(t *testing.T) {
	f := seedMerge(t)
	mr := f.openMR(t, "land the resumable export")
	tok := f.outsider(t, "merge-outsider")

	resp, body := f.approve(t, mr, tok)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("an account with no membership confirmed a merge request: %v", body)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("refusal was %d, want 403: %v", resp.StatusCode, body)
	}
}

// TestAnAuthorCannotConfirmTheirOwnMergeRequest: §6.3 counts "distinct
// collaborator confirmations" and the eligibility engine's own route (4) already
// defines a substantive review as "a merge_approval on somebody else's merge
// request", comparing user_id against author_id to say so. A self-confirmation is
// therefore not a confirmation, and accepting one would let an author be half
// their own quorum.
func TestAnAuthorCannotConfirmTheirOwnMergeRequest(t *testing.T) {
	f := seedMerge(t)
	mr := f.openMR(t, "land the resumable export")

	resp, body := f.approve(t, mr, f.authorToken)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("the author confirmed their own merge request: %v", body)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("refusal was %d, want 409: %v", resp.StatusCode, body)
	}
}

// TestAMergeRequestCannotBeExecutedThroughAnotherProject: ExecuteMerge took the
// project id from the URL and used it only for the audit row, so a member of any
// readable project could execute any merge request by naming its id under their
// own project's slug -- landing somebody else's feature and writing the audit
// entry into the wrong project's log.
func TestAMergeRequestCannotBeExecutedThroughAnotherProject(t *testing.T) {
	f := seedMerge(t)
	mr := f.openMR(t, "land the resumable export")

	other, _ := makeProject(t, f.ts, "some-other-project")
	resp, body := putJSON(t, f.ts,
		fmt.Sprintf("/api/v1/projects/%s/merge/%d/execute", other, mr), `{}`,
		"Authorization", "Bearer "+f.ownerToken(t))
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("a merge request of project %s was executed through project %s: %v",
			f.slug, other, body)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("refusal was %d, want 404 (the request is not a member of this "+
			"project's collection): %v", resp.StatusCode, body)
	}
	if got := f.featureStatus(t); got == "shipped" {
		t.Errorf("the feature shipped anyway: status=%q", got)
	}
}

// TestTheMergeListReportsTheGateAndItsShortfall: without this, every client has to
// re-derive policy from the charter -- the mistake the criteria page was built to
// avoid. The list carries the verdict the server would apply.
func TestTheMergeListReportsTheGateAndItsShortfall(t *testing.T) {
	f := seedMerge(t)
	mr := f.openMR(t, "land the resumable export")
	_, tokA := f.collaborator(t, "merge-one", "contributor")
	// Two more collaborators who have not confirmed, so the eligible population is
	// 3 and the threshold is the `max(2, ceil(0.25*3))` floor rather than the clamp
	// at an eligible of 1 -- a one-person project cannot need two confirmations from
	// two people, and MergeQuorumThreshold says so.
	f.collaborator(t, "merge-two", "contributor")
	f.collaborator(t, "merge-three", "contributor")
	if resp, body := f.approve(t, mr, tokA); resp.StatusCode != http.StatusOK {
		t.Fatalf("a collaborator could not confirm: %d %v", resp.StatusCode, body)
	}

	resp, body := getJSON(t, f.ts, fmt.Sprintf("/api/v1/projects/%s/merge", f.slug))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list merge requests: %d %v", resp.StatusCode, body)
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("want one merge request, got %v", body)
	}
	got, _ := items[0].(map[string]any)
	gate, ok := got["gate"].(map[string]any)
	if !ok {
		t.Fatalf("the list item carries no gate: %v\n"+
			"a client that has to compute the quorum itself is a second "+
			"implementation of §5.7, and the one that gets it wrong", got)
	}
	if approvals, _ := gate["approvals"].(float64); approvals != 1 {
		t.Errorf("approvals = %v, want 1", gate["approvals"])
	}
	// min(eligible, max(2, ceil(0.25*eligible))): with the fixture's members this
	// is 2, and it must not be reported as 0 -- a zero threshold is how the
	// consensus quorum silently stopped applying.
	if needed, _ := gate["needed"].(float64); needed < 2 {
		t.Errorf("needed = %v, want at least 2 (merge_quorum_min=2)", gate["needed"])
	}
	if passed, _ := gate["passed"].(bool); passed {
		t.Errorf("the gate reports passed with one confirmation and no reviewer: %v", gate)
	}
	if reviewerOK, ok := gate["reviewer_ok"].(bool); !ok || reviewerOK {
		t.Errorf("reviewer_ok = %v, want false: %v", gate["reviewer_ok"], gate)
	}
}

// TestTheCharterCanDropTheMergeQuorum: §5.7, "In maintainer-led mode the charter
// may lower or drop the merge quorum; every such merge is logged." The dropped
// quorum is the one path where a merge with no confirmations is correct, which is
// why it is also the path that must leave a trace.
func TestTheCharterCanDropTheMergeQuorum(t *testing.T) {
	f := seedMerge(t)
	mr := f.openMR(t, "land the resumable export")
	owner := f.ownerToken(t)

	// The charter PUT takes the whole 22-scalar object, not a patch: a partial
	// body leaves every absent ratio at zero and the validator refuses it, which is
	// the settings page's own reason for round-tripping the GET.
	_, current := getJSON(t, f.ts, fmt.Sprintf("/api/v1/projects/%s/charter", f.slug))
	current["MergeRequiresQuorum"] = false
	current["RequireReviewerApproval"] = false
	patched, err := json.Marshal(current)
	if err != nil {
		t.Fatalf("marshal charter: %v", err)
	}
	resp, body := putJSON(t, f.ts,
		fmt.Sprintf("/api/v1/projects/%s/charter", f.slug), string(patched),
		"Authorization", "Bearer "+owner)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("drop the merge quorum: %d %v", resp.StatusCode, body)
	}
	resp, body = f.execute(t, mr, owner)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the charter dropped the quorum and the merge was still refused: "+
			"%d %v", resp.StatusCode, body)
	}
	if got := f.featureStatus(t); got != "shipped" {
		t.Errorf("feature status = %q, want shipped", got)
	}
	var detail string
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT detail FROM audit_log WHERE project_id = ? AND entity = 'merge_request'
		   AND entity_id = ?`, f.pid, mr).Scan(&detail); err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if !strings.Contains(detail, "without a quorum") {
		t.Errorf("the audit row for a quorumless merge reads %q. §5.7 allows the "+
			"exception only because \"every such merge is logged\": if the row reads "+
			"like any other merge, nothing was logged", detail)
	}
}

// TestTwoConfirmationsAndAReviewerPassTheGate is the other half of a gate: it must
// open. A rule that refuses everything passes the same tests as a correct one, so
// the happy path is asserted end to end -- distinct collaborators, a reviewer among
// them, and the feature actually shipped.
func TestTwoConfirmationsAndAReviewerPassTheGate(t *testing.T) {
	f := seedMerge(t)
	mr := f.openMR(t, "land the resumable export")
	_, tokOne := f.collaborator(t, "merge-one", "contributor")
	_, tokTwo := f.collaborator(t, "merge-two", "reviewer")

	if resp, body := f.approve(t, mr, tokOne); resp.StatusCode != http.StatusOK {
		t.Fatalf("contributor cannot confirm: %d %v", resp.StatusCode, body)
	}
	if resp, body := f.approve(t, mr, tokTwo); resp.StatusCode != http.StatusOK {
		t.Fatalf("reviewer cannot confirm: %d %v", resp.StatusCode, body)
	}
	// The reviewer is the one that satisfies require_reviewer_approval, so this
	// also pins that the flag is not simply "any approval will do".
	resp, body := f.execute(t, mr, f.ownerToken(t))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("two confirmations incl. a reviewer did not pass the gate: %d %v",
			resp.StatusCode, body)
	}
	if got := f.featureStatus(t); got != "shipped" {
		t.Errorf("feature status = %q, want shipped", got)
	}
}

// TestAReviewerApprovalIsNotJustAnyApproval drops the reviewer requirement's
// satisfaction to the floor: two contributors and no reviewer must still refuse,
// because otherwise RequireReviewerApproval is a column nobody reads -- the exact
// defect this pass exists to close.
func TestAReviewerApprovalIsNotJustAnyApproval(t *testing.T) {
	f := seedMerge(t)
	mr := f.openMR(t, "land the resumable export")
	_, tokOne := f.collaborator(t, "merge-one", "contributor")
	_, tokTwo := f.collaborator(t, "merge-two", "contributor")
	for _, tok := range []string{tokOne, tokTwo} {
		if resp, body := f.approve(t, mr, tok); resp.StatusCode != http.StatusOK {
			t.Fatalf("a contributor could not confirm: %d %v", resp.StatusCode, body)
		}
	}
	resp, body := f.execute(t, mr, f.ownerToken(t))
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("two contributor confirmations and no reviewer passed a charter that "+
			"requires reviewer approval (%s): %v", governance.Reviewer, body)
	}
	if got := f.featureStatus(t); got == "shipped" {
		t.Errorf("the feature shipped anyway: status=%q", got)
	}
}
