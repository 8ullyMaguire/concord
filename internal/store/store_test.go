package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/db"
	"git.polarisocial.xyz/concord/concord/internal/governance"
	"git.polarisocial.xyz/concord/concord/internal/ranking"
)

var testDB *sql.DB
var testStore *DB

func setup() (*sql.DB, *DB, error) {
	if testDB != nil {
		return testDB, testStore, nil
	}
	tmpPath := "/tmp/concord_test.db"
	os.Remove(tmpPath)
	d, err := db.Open(tmpPath)
	if err != nil {
		return nil, nil, err
	}
	if err := db.Migrate(context.Background(), d); err != nil {
		return nil, nil, err
	}
	testDB = d
	testStore = New(d)
	// Insert a default test user
	_, err = d.ExecContext(context.Background(), `INSERT INTO users (username, display_name, created_at) VALUES (?, ?, ?)`, "testuser", "Test User", float64(time.Now().Unix()))
	if err != nil && !isUniqueViolation(err) {
		return nil, nil, err
	}
	return d, testStore, nil
}

func TestMain(m *testing.M) {
	_, _, err := setup()
	if err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestCreateAndGetUser(t *testing.T) {
	_, store, _ := setup()
	u, err := store.CreateUser(context.Background(), "newuser", "New User")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.Username != "newuser" {
		t.Errorf("expected username newuser, got %s", u.Username)
	}
	got, err := store.GetUser(context.Background(), u.Username)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.DisplayName != "New User" {
		t.Errorf("expected display name New User, got %s", got.DisplayName)
	}
}

func TestCreateAndGetProject(t *testing.T) {
	_, store, _ := setup()
	proj, err := store.CreateProject(context.Background(), "test-project", "Test Project", "A test project", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if proj.Slug != "test-project" {
		t.Errorf("expected slug test-project, got %s", proj.Slug)
	}
	got, err := store.GetProjectByID(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("GetProjectByID: %v", err)
	}
	if got.Name != "Test Project" {
		t.Errorf("expected name Test Project, got %s", got.Name)
	}
}

func TestGetProjectNotFound(t *testing.T) {
	_, store, _ := setup()
	_, err := store.GetProjectByID(context.Background(), 999999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestCreateAndGetComplaint(t *testing.T) {
	_, store, _ := setup()
	_, err := store.CreateUser(context.Background(), "compuser", "Comp User")
	if err != nil && !isUniqueViolation(err) {
		t.Fatalf("CreateUser: %v", err)
	}
	proj, err := store.CreateProject(context.Background(), "cproj", "Complaint Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	c, err := store.CreateComplaint(context.Background(), proj.ID, 1, "Test complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if c.ProjectID != proj.ID {
		t.Errorf("expected project ID %d, got %d", proj.ID, c.ProjectID)
	}
	got, err := store.GetComplaint(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetComplaint: %v", err)
	}
	if got.Title != "Test complaint" {
		t.Errorf("expected title Test complaint, got %s", got.Title)
	}
}

func TestGetComplaintPain(t *testing.T) {
	_, store, _ := setup()
	_, err := store.CreateUser(context.Background(), "painuser", "Pain User")
	if err != nil && !isUniqueViolation(err) {
		t.Fatalf("CreateUser: %v", err)
	}
	proj, err := store.CreateProject(context.Background(), "pain-project", "Pain Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	c, err := store.CreateComplaint(context.Background(), proj.ID, 1, "Pain complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	pain, err := store.GetComplaintPain(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetComplaintPain: %v", err)
	}
	if pain <= 0 {
		t.Errorf("expected positive pain for new complaint, got %f", pain)
	}
}

func TestCreateAndGetFeature(t *testing.T) {
	_, store, _ := setup()
	_, err := store.CreateUser(context.Background(), "featuser", "Feat User")
	if err != nil && !isUniqueViolation(err) {
		t.Fatalf("CreateUser: %v", err)
	}
	proj, err := store.CreateProject(context.Background(), "fproject", "Feature Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	comp, err := store.CreateComplaint(context.Background(), proj.ID, 1, "Test feature", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(context.Background(), comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	feat, err := store.CreateFeature(context.Background(), proj.ID, 1, "Test feature", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	if feat.Status != "draft" {
		t.Errorf("expected status draft, got %s", feat.Status)
	}
	got, err := store.GetFeature(context.Background(), feat.ID)
	if err != nil {
		t.Fatalf("GetFeature: %v", err)
	}
	if got.Title != "Test feature" {
		t.Errorf("expected title Test feature, got %s", got.Title)
	}
}

func TestCreateAndGetList(t *testing.T) {
	_, store, _ := setup()
	_, err := store.CreateUser(context.Background(), "listuser", "List User")
	if err != nil && !isUniqueViolation(err) {
		t.Fatalf("CreateUser: %v", err)
	}
	proj, err := store.CreateProject(context.Background(), "lproject", "List Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	l, err := store.CreateList(context.Background(), proj.ID, "myslug", "My List", "desc")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	if l.Slug != "myslug" {
		t.Errorf("expected slug myslug, got %s", l.Slug)
	}
	got, err := store.GetList(context.Background(), l.ID)
	if err != nil {
		t.Fatalf("GetList: %v", err)
	}
	if got.Name != "My List" {
		t.Errorf("expected name My List, got %s", got.Name)
	}
}

func TestCreateAndGetBoard(t *testing.T) {
	_, store, _ := setup()
	proj, err := store.CreateProject(context.Background(), "bproject", "Board Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	cols, cards, err := store.GetBoard(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if len(cols) != 9 {
		t.Errorf("expected 9 default columns, got %d", len(cols))
	}
	if len(cards) != 0 {
		t.Errorf("expected 0 cards, got %d", len(cards))
	}
}

func TestCreateAndGetRequest(t *testing.T) {
	_, store, _ := setup()
	_, err := store.CreateUser(context.Background(), "requser", "Req User")
	if err != nil && !isUniqueViolation(err) {
		t.Fatalf("CreateUser: %v", err)
	}
	proj, err := store.CreateProject(context.Background(), "rproject", "Request Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	req, err := store.CreateRequest(context.Background(), proj.ID, 1, "My Request", "body")
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if req.Title != "My Request" {
		t.Errorf("expected title My Request, got %s", req.Title)
	}
	got, err := store.GetRequest(context.Background(), req.ID)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if got.Body != "body" {
		t.Errorf("expected body body, got %s", got.Body)
	}
}

func TestCreateAndGetConsensusCall(t *testing.T) {
	_, store, _ := setup()
	_, err := store.CreateUser(context.Background(), "ccuser", "CC User")
	if err != nil && !isUniqueViolation(err) {
		t.Fatalf("CreateUser: %v", err)
	}
	proj, err := store.CreateProject(context.Background(), "ccproject", "Consensus Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	comp, err := store.CreateComplaint(context.Background(), proj.ID, 1, "Consensus complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(context.Background(), comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	feat, err := store.CreateFeature(context.Background(), proj.ID, 1, "Consensus feature", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	cc, err := store.CreateConsensusCall(context.Background(), proj.ID, feat.ID, "Test Consensus", "desc")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	if cc.ProjectID != proj.ID {
		t.Errorf("expected project ID %d, got %d", proj.ID, cc.ProjectID)
	}
	got, err := store.GetConsensusCall(context.Background(), cc.ID)
	if err != nil {
		t.Fatalf("GetConsensusCall: %v", err)
	}
	if got.ID != 0 {
		if got.ID == 0 {
			t.Error("expected non-zero ID")
		}
	}
}

func TestRecordVote(t *testing.T) {
	_, store, _ := setup()
	_, err := store.CreateUser(context.Background(), "voteuser", "Vote User")
	if err != nil && !isUniqueViolation(err) {
		t.Fatalf("CreateUser: %v", err)
	}
	proj, err := store.CreateProject(context.Background(), "vote-project", "Vote Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	comp, err := store.CreateComplaint(context.Background(), proj.ID, 1, "Vote complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(context.Background(), comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	comp2, err := store.CreateComplaint(context.Background(), proj.ID, 1, "Vote complaint 2", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint 2: %v", err)
	}
	if err := store.ValidateComplaint(context.Background(), comp2.ID); err != nil {
		t.Fatalf("ValidateComplaint 2: %v", err)
	}
	feat, err := store.CreateFeature(context.Background(), proj.ID, 1, "Vote feature", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	feat2, err := store.CreateFeature(context.Background(), proj.ID, 1, "Vote feature 2", "desc", []int64{comp2.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	charter := governance.DefaultCharter(governance.Collective)
	_, err = store.RecordVote(context.Background(), proj.ID, 1, feat.ID, feat2.ID, "a", 1.0, charter)
	if err != nil {
		t.Fatalf("RecordVote: %v", err)
	}
	votes, err := store.GetFeatureVotes(context.Background(), feat.ID)
	if err != nil {
		t.Fatalf("GetFeatureVotes: %v", err)
	}
	if len(votes) == 0 {
		t.Error("expected at least one vote")
	}
}

func TestGetNextPair(t *testing.T) {
	_, store, _ := setup()
	_, err := store.CreateUser(context.Background(), "npuser", "NP User")
	if err != nil && !isUniqueViolation(err) {
		t.Fatalf("CreateUser: %v", err)
	}
	proj, err := store.CreateProject(context.Background(), "np-project", "Next Pair Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	comp, err := store.CreateComplaint(context.Background(), proj.ID, 1, "Next Pair complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(context.Background(), comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	feat, err := store.CreateFeature(context.Background(), proj.ID, 1, "Next Pair feature", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	feat2, err := store.CreateFeature(context.Background(), proj.ID, 1, "Next Pair feature 2", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature 2: %v", err)
	}
	a, b, err := store.GetNextPair(context.Background(), proj.ID, 1)
	if err != nil {
		t.Fatalf("GetNextPair: %v", err)
	}
	if (a.ID != feat.ID && a.ID != feat2.ID) && (b.ID != feat.ID && b.ID != feat2.ID) {
		t.Errorf("expected one of the pair to be feature %d", feat.ID)
	}
}

func TestGetNextPairExhausted(t *testing.T) {
	_, store, _ := setup()
	proj, err := store.CreateProject(context.Background(), "ex-project", "Exhaust Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	comp, err := store.CreateComplaint(context.Background(), proj.ID, 1, "Exhaust complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(context.Background(), comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	feat, err := store.CreateFeature(context.Background(), proj.ID, 1, "Exhaust feature", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	feat2, err := store.CreateFeature(context.Background(), proj.ID, 1, "Exhaust feature 2", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature 2: %v", err)
	}
	charter := governance.DefaultCharter(governance.Collective)
	_, err = store.RecordVote(context.Background(), proj.ID, 1, feat.ID, feat2.ID, "a", 1.0, charter)
	if err != nil {
		t.Fatalf("RecordVote: %v", err)
	}
	_, _, err = store.GetNextPair(context.Background(), proj.ID, 1)
	_ = err
}

func TestCreateAndGetMergeRequest(t *testing.T) {
	_, store, _ := setup()
	proj, err := store.CreateProject(context.Background(), "mr-project", "Merge Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	comp, err := store.CreateComplaint(context.Background(), proj.ID, 1, "MR complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(context.Background(), comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	feat, err := store.CreateFeature(context.Background(), proj.ID, 1, "MR feature", "desc", []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	mr, err := store.CreateMergeRequest(context.Background(), proj.ID, feat.ID, 1, "test merge")
	if err != nil {
		t.Fatalf("CreateMergeRequest: %v", err)
	}
	if mr.Title != "test merge" {
		t.Errorf("expected title test merge, got %s", mr.Title)
	}
	got, err := store.GetMergeRequest(context.Background(), mr.ID)
	if err != nil {
		t.Fatalf("GetMergeRequest: %v", err)
	}
	if got.Title != "test merge" {
		t.Errorf("expected title test merge, got %s", got.Title)
	}
}

func TestGetRoleForProject(t *testing.T) {
	_, store, _ := setup()
	proj, err := store.CreateProject(context.Background(), "role-project", "Role Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	role, err := store.GetRoleForProject(context.Background(), proj.ID, 1)
	if err != nil {
		t.Fatalf("GetRoleForProject: %v", err)
	}
	if role == "" {
		t.Error("expected non-empty role")
	}
}

func TestAddImpact(t *testing.T) {
	_, store, _ := setup()
	_, err := store.CreateUser(context.Background(), "impactuser", "Impact User")
	if err != nil && !isUniqueViolation(err) {
		t.Fatalf("CreateUser: %v", err)
	}
	proj, err := store.CreateProject(context.Background(), "impact-project", "Impact Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	c, err := store.CreateComplaint(context.Background(), proj.ID, 1, "Impact complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	err = store.AddImpact(context.Background(), c.ID, 1, 5)
	if err != nil {
		t.Fatalf("AddImpact: %v", err)
	}
}

func TestGetFeatureVotes(t *testing.T) {
	_, store, _ := setup()
	_, err := store.CreateUser(context.Background(), "fvuser", "FV User")
	if err != nil && !isUniqueViolation(err) {
		t.Fatalf("CreateUser: %v", err)
	}
	proj, err := store.CreateProject(context.Background(), "fv-project", "FV Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	comp, err := store.CreateComplaint(context.Background(), proj.ID, 1, "FV complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(context.Background(), comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	comp2, err := store.CreateComplaint(context.Background(), proj.ID, 1, "FV complaint 2", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint 2: %v", err)
	}
	if err := store.ValidateComplaint(context.Background(), comp2.ID); err != nil {
		t.Fatalf("ValidateComplaint 2: %v", err)
	}
	feat, err := store.CreateFeature(context.Background(), proj.ID, 1, "FV feature", "desc", []int64{comp.ID})
	feat2, err := store.CreateFeature(context.Background(), proj.ID, 1, "FV feature 2", "desc", []int64{comp2.ID})
	if err != nil {
		t.Fatalf("CreateFeature 2: %v", err)
	}
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	charter := governance.DefaultCharter(governance.Collective)
	_, err = store.RecordVote(context.Background(), proj.ID, 1, feat.ID, feat2.ID, "a", 1.0, charter)
	if err != nil {
		t.Fatalf("RecordVote: %v", err)
	}
	votes, err := store.GetFeatureVotes(context.Background(), feat.ID)
	if err != nil {
		t.Fatalf("GetFeatureVotes: %v", err)
	}
	if len(votes) == 0 {
		t.Error("expected at least one vote")
	}
}

func TestGetListsByProject(t *testing.T) {
	_, store, _ := setup()
	_, err := store.CreateUser(context.Background(), "lspuser", "LSP User")
	if err != nil && !isUniqueViolation(err) {
		t.Fatalf("CreateUser: %v", err)
	}
	proj, err := store.CreateProject(context.Background(), "lsp-project", "LSP Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	_, err = store.CreateList(context.Background(), proj.ID, "slug1", "List 1", "desc1")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	lists, err := store.GetListsByProject(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("GetListsByProject: %v", err)
	}
	if len(lists) == 0 {
		t.Error("expected at least one list")
	}
}

func TestCreateListEntry(t *testing.T) {
	_, store, _ := setup()
	_, err := store.CreateUser(context.Background(), "leuser", "LE User")
	if err != nil && !isUniqueViolation(err) {
		t.Fatalf("CreateUser: %v", err)
	}
	proj, err := store.CreateProject(context.Background(), "le-project", "LE Project", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	l, err := store.CreateList(context.Background(), proj.ID, "le-slug", "LE List", "desc")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	le, err := store.CreateListEntry(context.Background(), l.ID, proj.ID, "Entry Title", "Entry Body")
	if err != nil {
		t.Fatalf("CreateListEntry: %v", err)
	}
	if le.Title != "Entry Title" {
		t.Errorf("expected title Entry Title, got %s", le.Title)
	}
	got, err := store.GetListEntry(context.Background(), le.ID)
	if err != nil {
		t.Fatalf("GetListEntry: %v", err)
	}
	if got.Description != "Entry Body" {
		if got.Description != "Entry Body" {
			t.Errorf("expected description Entry Body, got %s", got.Description)
		}
	}
}

func TestPriorityScore(t *testing.T) {
	score := ranking.PriorityScore(1500, 350, 5.0, 1.0, 0.5, 0.3)
	if score <= 0 {
		t.Errorf("expected positive priority score, got %f", score)
	}
}

func TestGlicko2Rating(t *testing.T) {
	a := ranking.Feature{R: 1500, RD: 350, Vol: 0.06}
	b := ranking.Feature{R: 1550, RD: 330, Vol: 0.06}
	a2, b2 := ranking.ApplyPairwiseVote(a, b, ranking.OutcomeA, 1.0, 0.3)
	if a2.R == 1500 {
		t.Error("expected rating to change after vote")
	}
	if b2.R == 1550 {
		t.Error("expected opponent rating to change after vote")
	}
}
