package store

import (
	"context"
	"errors"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/db"
	"git.polarisocial.xyz/concord/concord/internal/governance"
	"git.polarisocial.xyz/concord/concord/internal/ranking"
)

func setup(t *testing.T) *DB {
	t.Helper()
	raw, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Migrate(context.Background(), raw); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { raw.Close() })
	return New(raw)
}

func setupWithUser(t *testing.T) (*DB, int64) {
	t.Helper()
	store := setup(t)
	u, err := store.CreateUser(context.Background(), "testuser", "Test User")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return store, u.ID
}

func setupWithProject(t *testing.T) (*DB, int64, int64) {
	t.Helper()
	store, uid := setupWithUser(t)
	proj, err := store.CreateProject(context.Background(), uid, "test-project", "Test Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return store, uid, proj.ID
}

func TestCreateAndGetUser(t *testing.T) {
	store := setup(t)
	ctx := context.Background()
	u, err := store.CreateUser(ctx, "alice", "Alice")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	got, err := store.GetUser(ctx, "alice")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("expected ID %d, got %d", u.ID, got.ID)
	}
	if got.Username != "alice" {
		t.Errorf("expected username alice, got %s", got.Username)
	}
	if got.DisplayName != "Alice" {
		t.Errorf("expected display name Alice, got %s", got.DisplayName)
	}
	if got.Role != "member" {
		t.Errorf("expected default role member, got %s", got.Role)
	}
}

func TestCreateAndGetProject(t *testing.T) {
	store := setup(t)
	ctx := context.Background()
	if _, err := store.CreateUser(ctx, "owner", "Owner"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	p, err := store.CreateProject(ctx, 1, "myproject", "My Project", "A test project", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.Slug != "myproject" {
		t.Errorf("expected slug myproject, got %s", p.Slug)
	}
	got, err := store.GetProject(ctx, "myproject")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.ID != p.ID {
		t.Errorf("expected ID %d, got %d", p.ID, got.ID)
	}
}

func TestGetProjectNotFound(t *testing.T) {
	store := setup(t)
	_, err := store.GetProject(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent project, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestCreateAndGetComplaint(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	c, err := store.CreateComplaint(ctx, pid, uid, "Test complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if c.Title != "Test complaint" {
		t.Errorf("expected title Test complaint, got %s", c.Title)
	}
	if c.Status != "open" {
		t.Errorf("expected status open, got %s", c.Status)
	}
	got, err := store.GetComplaint(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetComplaint: %v", err)
	}
	if got.Title != "Test complaint" {
		t.Errorf("expected title Test complaint, got %s", got.Title)
	}
}

func TestGetComplaintPain(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	c, err := store.CreateComplaint(ctx, pid, uid, "Pain complaint", "body", 3, 1.0, 2.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	pain, err := store.GetComplaintPain(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetComplaintPain: %v", err)
	}
	if pain <= 0 {
		t.Errorf("expected positive pain for severity=3, freq=1.0, mult=2.0; got %f", pain)
	}
}

func TestCreateAndGetFeature(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	comp, err := store.CreateComplaint(ctx, pid, uid, "Linked complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	f, err := store.CreateFeature(ctx, pid, uid, "Test feature", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	if f.Status != "draft" {
		t.Errorf("expected status draft, got %s", f.Status)
	}
	got, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatalf("GetFeature: %v", err)
	}
	if got.Title != "Test feature" {
		t.Errorf("expected title Test feature, got %s", got.Title)
	}
}

func TestCreateAndGetList(t *testing.T) {
	store, _, pid := setupWithProject(t)
	ctx := context.Background()
	l, err := store.CreateList(ctx, pid, "myslug", "My List", "A list")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	if l.Slug != "myslug" {
		t.Errorf("expected slug myslug, got %s", l.Slug)
	}
	got, err := store.GetList(ctx, l.ID)
	if err != nil {
		t.Fatalf("GetList: %v", err)
	}
	if got.Slug != "myslug" {
		t.Errorf("expected slug myslug, got %s", got.Slug)
	}
}

func TestCreateAndGetBoard(t *testing.T) {
	store, _, pid := setupWithProject(t)
	ctx := context.Background()
	cols, err := store.GetBoardColumns(ctx, pid)
	if err != nil {
		t.Fatalf("GetBoardColumns: %v", err)
	}
	if len(cols) != 9 {
		t.Errorf("expected 9 default columns, got %d", len(cols))
	}
}

func TestCreateAndGetRequest(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	r, err := store.CreateRequest(ctx, pid, uid, "Test Request", "body")
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if r.Title != "Test Request" {
		t.Errorf("expected title Test Request, got %s", r.Title)
	}
	got, err := store.GetRequest(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if got.Title != "Test Request" {
		t.Errorf("expected title Test Request, got %s", got.Title)
	}
}

func TestCreateAndGetConsensusCall(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	// CreateConsensusCall(projectID, featureID, title, description)
	// Need a feature for the call
	comp, err := store.CreateComplaint(ctx, pid, uid, "CC complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	feat, err := store.CreateFeature(ctx, pid, uid, "CC feature", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	call, err := store.CreateConsensusCall(ctx, pid, feat.ID, "Test Consensus", "Test Desc")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	if call.ID == 0 {
		t.Error("expected non-zero ID")
	}
}

func TestRecordVoteRatingMovement(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	comp, err := store.CreateComplaint(ctx, pid, uid, "Vote complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	featA, err := store.CreateFeature(ctx, pid, uid, "Feature A", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature A: %v", err)
	}
	featB, err := store.CreateFeature(ctx, pid, uid, "Feature B", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature B: %v", err)
	}
	charter := governance.DefaultCharter(governance.Collective)
	vote, err := store.RecordVote(ctx, pid, uid, featA.ID, featB.ID, "a", 1.0, charter)
	if err != nil {
		t.Fatalf("RecordVote: %v", err)
	}
	if vote.Weight <= 0 {
		t.Errorf("expected positive weight, got %f", vote.Weight)
	}
	gotA, _ := store.GetFeature(ctx, featA.ID)
	gotB, _ := store.GetFeature(ctx, featB.ID)
	if gotA.EloR == 1500 {
		t.Error("feature A rating did not change after winning vote")
	}
	if gotB.EloR == 1500 {
		t.Error("feature B rating did not change after losing vote")
	}
	if gotA.EloR <= gotB.EloR {
		t.Errorf("winner A rating %f should be > loser B rating %f", gotA.EloR, gotB.EloR)
	}
}

func TestRecordVoteWeightScaling(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	charter := governance.DefaultCharter(governance.Collective)

	comp, err := store.CreateComplaint(ctx, pid, uid, "WS complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	featA, _ := store.CreateFeature(ctx, pid, uid, "WS A", "desc", []int64{comp.ID})
	featB, _ := store.CreateFeature(ctx, pid, uid, "WS B", "desc", []int64{comp.ID})
	if _, err := store.RecordVote(ctx, pid, uid, featA.ID, featB.ID, "a", 0.0, charter); err != nil {
		t.Fatalf("RecordVote zero: %v", err)
	}
	gotLow, _ := store.GetFeature(ctx, featA.ID)

	featC, _ := store.CreateFeature(ctx, pid, uid, "WS C", "desc", []int64{comp.ID})
	featD, _ := store.CreateFeature(ctx, pid, uid, "WS D", "desc", []int64{comp.ID})
	if _, err := store.RecordVote(ctx, pid, uid, featC.ID, featD.ID, "a", 10.0, charter); err != nil {
		t.Fatalf("RecordVote high: %v", err)
	}
	gotHigh, _ := store.GetFeature(ctx, featC.ID)

	if gotHigh.EloR-gotLow.EloR < 0.001 {
		t.Errorf("high strategic weight should produce larger rating gain: low=%f, high=%f", gotLow.EloR, gotHigh.EloR)
	}
}

func TestGetNextPair(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	comp, err := store.CreateComplaint(ctx, pid, uid, "NP complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	feat, err := store.CreateFeature(ctx, pid, uid, "NP feature", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	feat2, err := store.CreateFeature(ctx, pid, uid, "NP feature 2", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature 2: %v", err)
	}
	a, b, err := store.GetNextPair(ctx, pid, uid)
	if err != nil {
		t.Fatalf("GetNextPair: %v", err)
	}
	valid := (a.ID == feat.ID && b.ID == feat2.ID) || (a.ID == feat2.ID && b.ID == feat.ID)
	if !valid {
		t.Errorf("expected pair (%d, %d), got (%d, %d)", feat.ID, feat2.ID, a.ID, b.ID)
	}
}

func TestGetNextPairExhausted(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	comp, err := store.CreateComplaint(ctx, pid, uid, "Ex complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	feat, err := store.CreateFeature(ctx, pid, uid, "Ex feature", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	feat2, err := store.CreateFeature(ctx, pid, uid, "Ex feature 2", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature 2: %v", err)
	}
	charter := governance.DefaultCharter(governance.Collective)
	if _, err := store.RecordVote(ctx, pid, uid, feat.ID, feat2.ID, "a", 1.0, charter); err != nil {
		t.Fatalf("RecordVote: %v", err)
	}
	_, _, err = store.GetNextPair(ctx, pid, uid)
	if err == nil {
		t.Error("expected error when all pairs voted, got nil")
	}
}

func TestCreateAndGetMergeRequest(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	comp, err := store.CreateComplaint(ctx, pid, uid, "MR complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	feat, err := store.CreateFeature(ctx, pid, uid, "MR feature", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	mr, err := store.CreateMergeRequest(ctx, pid, feat.ID, uid, "Test MR")
	if err != nil {
		t.Fatalf("CreateMergeRequest: %v", err)
	}
	got, err := store.GetMergeRequest(ctx, mr.ID)
	if err != nil {
		t.Fatalf("GetMergeRequest: %v", err)
	}
	if got.Title != "Test MR" {
		t.Errorf("expected title Test MR, got %s", got.Title)
	}
}

func TestGetRoleForProject(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	role, err := store.GetRoleForProject(ctx, pid, uid)
	if err != nil {
		t.Fatalf("GetRoleForProject: %v", err)
	}
	if role != "maintainer" {
		t.Errorf("expected maintainer for creator, got %s", role)
	}
	if _, err := store.CreateUser(ctx, "otheruser", "Other User"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	other, _ := store.GetUser(ctx, "otheruser")
	role, err = store.GetRoleForProject(ctx, pid, other.ID)
	if err != nil {
		t.Fatalf("GetRoleForProject: %v", err)
	}
	if role != "guest" {
		t.Errorf("expected guest for non-member, got %s", role)
	}
}

func TestAddImpact(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	comp, err := store.CreateComplaint(ctx, pid, uid, "Impact complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	// AddImpact(ctx, complaintID, userID, severity)
	if err := store.AddImpact(ctx, comp.ID, uid, 2); err != nil {
		t.Fatalf("AddImpact: %v", err)
	}
}

func TestGetFeatureVotes(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	comp, err := store.CreateComplaint(ctx, pid, uid, "FV complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	feat, err := store.CreateFeature(ctx, pid, uid, "FV feature", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	feat2, err := store.CreateFeature(ctx, pid, uid, "FV feature 2", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature 2: %v", err)
	}
	charter := governance.DefaultCharter(governance.Collective)
	if _, err := store.RecordVote(ctx, pid, uid, feat.ID, feat2.ID, "a", 1.0, charter); err != nil {
		t.Fatalf("RecordVote: %v", err)
	}
	votes, err := store.GetFeatureVotes(ctx, feat.ID)
	if err != nil {
		t.Fatalf("GetFeatureVotes: %v", err)
	}
	if len(votes) < 1 {
		t.Errorf("expected at least 1 vote, got %d", len(votes))
	}
}

func TestGetListsByProject(t *testing.T) {
	store, _, pid := setupWithProject(t)
	ctx := context.Background()
	if _, err := store.CreateList(ctx, pid, "list1", "List 1", "desc"); err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	if _, err := store.CreateList(ctx, pid, "list2", "List 2", "desc"); err != nil {
		t.Fatalf("CreateList 2: %v", err)
	}
	lists, err := store.GetListsByProject(ctx, pid)
	if err != nil {
		t.Fatalf("GetListsByProject: %v", err)
	}
	if len(lists) != 2 {
		t.Errorf("expected 2 lists, got %d", len(lists))
	}
}

func TestCreateListEntry(t *testing.T) {
	store, _, pid := setupWithProject(t)
	ctx := context.Background()
	l, err := store.CreateList(ctx, pid, "entry-list", "Entry List", "desc")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	// CreateListEntry(ctx, listID, projectID, url, title, body)
	le, err := store.CreateListEntry(ctx, l.ID, "https://example.com", "Entry Title", "Entry Body")
	if err != nil {
		t.Fatalf("CreateListEntry: %v", err)
	}
	if le.Title != "Entry Title" {
		t.Errorf("expected title Entry Title, got %s", le.Title)
	}
}

func TestPriorityScore(t *testing.T) {
	score := ranking.PriorityScore(1600, 50, 10.0, 2.0, 0.5, 0.1)
	if score <= 0 {
		t.Errorf("expected positive priority score, got %f", score)
	}
	scoreLow := ranking.PriorityScore(1400, 50, 10.0, 2.0, 0.5, 0.1)
	if score <= scoreLow {
		t.Errorf("higher elo should give higher score: %f vs %f", score, scoreLow)
	}
}

func TestGlicko2Rating(t *testing.T) {
	// ApplyPairwiseVote takes Feature structs and Outcome
	a := ranking.Feature{R: 1500, RD: 50, Vol: 0.06}
	b := ranking.Feature{R: 1500, RD: 50, Vol: 0.06}
	out := ranking.OutcomeA
	newA, newB := ranking.ApplyPairwiseVote(a, b, out, 1.0, 0.3)
	if newA.R <= a.R {
		t.Errorf("winner rating should increase, got %f (was %f)", newA.R, a.R)
	}
	if newB.R >= b.R {
		t.Errorf("loser rating should decrease, got %f (was %f)", newB.R, b.R)
	}
}

func TestReputation(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	rep, err := store.GetReputation(ctx, pid, uid)
	if err != nil {
		t.Fatalf("GetReputation: %v", err)
	}
	if rep != 0 {
		t.Errorf("expected 0 reputation initially, got %f", rep)
	}
	if err := store.AddReputation(ctx, pid, uid, "vote", 5.0); err != nil {
		t.Fatalf("AddReputation: %v", err)
	}
	rep, err = store.GetReputation(ctx, pid, uid)
	if err != nil {
		t.Fatalf("GetReputation after: %v", err)
	}
	if rep != 5.0 {
		t.Errorf("expected reputation 5.0, got %f", rep)
	}
}
