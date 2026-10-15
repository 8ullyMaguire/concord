package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// store.ProposeStrategicWeight has carried a full test suite since the strategy
// panel was written, and the handler that opens a proposal says of itself, in its own
// doc comment, that it "follows the taxonomy proposal's shape deliberately:
// contributor gate".
//
// There was no contributor gate. requireWriteActor asks for a signed-in account and
// nothing else, so any account on the instance could open a weight proposal on any
// project -- including one it is not a member of -- and §6.2's "nobody can pin an item
// to the top by hand" was, at the door, exactly that.
//
// Same family as §16 and §17: a rule that is written down in one place and enforced
// in none. This pins the gate that the comment claimed.
func TestOnlyAContributorCanOpenAWeightProposal(t *testing.T) {
	ts, db, slug, pid, authorID := seedPanels(t)
	ctx := context.Background()
	// One feature per case: a feature can hold only ONE pending proposal, so reusing
	// one would answer 409 and the gate would look like it fired when the duplicate
	// rule did.
	bodyFor := func() string {
		return fmt.Sprintf(`{"feature_id":%d,"weight":2,"rationale":"it is more urgent than it scores"}`,
			seedFeatureForWeightProposal(t, db, pid, authorID))
	}

	// (1) An outsider: a registered account with no membership at all.
	tok := registerAndLogin(t, ts, "weight-outsider")
	resp, payload := postJSON(t, ts,
		"/api/v1/projects/"+slug+"/strategy/weight-proposals", bodyFor(),
		"Authorization", tok)
	if resp.StatusCode == http.StatusCreated {
		t.Errorf("an account with no membership in the project opened a weight "+
			"proposal: %v", payload)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("an outsider's proposal answered %d; want 403 (the comment's "+
			"contributor gate)", resp.StatusCode)
	}

	// (2) A 'user' is a member with no standing to propose: the role ladder's floor
	// above guest, and §6.2's proposals are a contribution, so the gate is
	// contributor.
	outsiderID := userIDNamed(t, db, "weight-outsider")
	if _, err := db.ExecContext(ctx,
		`INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)`,
		pid, outsiderID, "user", float64(1)); err != nil {
		t.Fatalf("add viewer: %v", err)
	}
	resp, payload = postJSON(t, ts,
		"/api/v1/projects/"+slug+"/strategy/weight-proposals", bodyFor(),
		"Authorization", tok)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a user-role member opened a weight proposal: %d %v; want 403",
			resp.StatusCode, payload)
	}

	// (3) A contributor can, which is what keeps the gate from being a rule that
	// refuses everything. Same account, promoted.
	if _, err := db.ExecContext(ctx,
		`UPDATE members SET role = 'contributor' WHERE project_id = ? AND user_id = ?`,
		pid, outsiderID); err != nil {
		t.Fatalf("promote: %v", err)
	}
	resp, payload = postJSON(t, ts,
		"/api/v1/projects/"+slug+"/strategy/weight-proposals", bodyFor(),
		"Authorization", tok)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("a contributor's proposal was refused: %d %v; want 201",
			resp.StatusCode, payload)
	}
}

// TestTheWeightProposalCarriesItsRationaleAndNotItsWeight keeps the client honest
// about what happens next: the response must not look like an applied change.
func TestTheWeightProposalCarriesItsRationaleAndNotItsWeight(t *testing.T) {
	ts, db, slug, pid, authorID := seedPanels(t)
	featureID := seedFeatureForWeightProposal(t, db, pid, authorID)
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)`,
		pid, authorID, "contributor", float64(1)); err != nil {
		t.Fatalf("add contributor: %v", err)
	}
	resp, payload := postJSON(t, ts,
		"/api/v1/projects/"+slug+"/strategy/weight-proposals",
		fmt.Sprintf(`{"feature_id":%d,"weight":2,"rationale":"more urgent"}`, featureID),
		"Authorization", "Bearer "+tokenFor(t, db, "testuser"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("propose: %d %v", resp.StatusCode, payload)
	}
	if msg, _ := payload["message"].(string); msg == "" {
		t.Errorf("the response carries no message; a proposal that reads as applied "+
			"is the hand-steering §6.2 exists to prevent: %v", payload)
	}
	// The weight itself has NOT moved.
	var weight float64
	if err := db.QueryRowContext(context.Background(),
		`SELECT strategic_weight FROM features WHERE id = ?`, featureID).Scan(&weight); err != nil {
		t.Fatalf("read feature weight: %v", err)
	}
	if weight != 1 {
		t.Errorf("strategic_weight = %v after a proposal; want 1 until it is ratified",
			weight)
	}
}

// seedFeatureForWeightProposal creates a feature in the project, directly, because
// the proposal is what is under test.
func seedFeatureForWeightProposal(t *testing.T, db *store.DB, pid, authorID int64) int64 {
	t.Helper()
	res, err := db.ExecContext(context.Background(),
		`INSERT INTO features (project_id, author_id, title, body, status, effort,
		 strategic_weight, created_at, updated_at)
		 VALUES (?,?,?,?,'draft','M',1,?,?)`,
		pid, authorID, "a feature worth weighing", "body", float64(1), float64(1))
	if err != nil {
		t.Fatalf("insert feature: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("feature id: %v", err)
	}
	return id
}

var _ = json.Marshal
