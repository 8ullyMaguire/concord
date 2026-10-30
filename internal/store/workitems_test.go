package store

import (
	"context"
	"errors"
	"testing"
)

// workItemFixture builds a project with one validated complaint and a feature, the shape every test
// below needs. The feature starts in `draft` because that is what CreateFeature writes: claiming is
// only legal on ready/in_progress, so a test that wants a claimable feature must move it first, and
// the tests that forget to are the ones that prove the refusal.
func workItemFixture(t *testing.T) (*DB, int64, int64, int64) {
	t.Helper()
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	c, err := store.CreateComplaint(ctx, pid, uid, "The export drops the last row", "body", 3, 1.0, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(ctx, c.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	f, err := store.CreateFeature(ctx, pid, uid, "Make the export complete", "body", "S", nil, nil, []int64{c.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	return store, uid, pid, f.ID
}

func budget(n int) *int     { return &n }
func diff(s string) *string { return &s }

func TestWorkItemLabelRoundTrips(t *testing.T) {
	store, _, pid, fid := workItemFixture(t)
	ctx := context.Background()

	if err := store.SetWorkItemLabel(ctx, pid, fid, WorkItemLabel{
		Difficulty: diff("S"), TimeBudgetMinutes: budget(60), NeedsMentor: true,
	}); err != nil {
		t.Fatalf("SetWorkItemLabel: %v", err)
	}
	if err := store.UpdateFeatureStatus(ctx, fid, "ready"); err != nil {
		t.Fatalf("UpdateFeatureStatus: %v", err)
	}
	items, err := store.ListWorkItems(ctx, pid)
	if err != nil {
		t.Fatalf("ListWorkItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want the one ready feature: %+v", len(items), items)
	}
	got := items[0]
	if got.Difficulty != "S" || got.TimeBudgetMinutes == nil || *got.TimeBudgetMinutes != 60 || !got.NeedsMentor {
		t.Errorf("label round-trip = %+v, want S/60/mentor", got)
	}
}

func TestWorkItemLabelRefusesAnUnknownDifficulty(t *testing.T) {
	store, _, pid, fid := workItemFixture(t)
	err := store.SetWorkItemLabel(context.Background(), pid, fid, WorkItemLabel{Difficulty: diff("XS")})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestWorkItemLabelRefusesAZeroBudget(t *testing.T) {
	store, _, pid, fid := workItemFixture(t)
	err := store.SetWorkItemLabel(context.Background(), pid, fid, WorkItemLabel{TimeBudgetMinutes: budget(0)})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid for a zero budget", err)
	}
}

// A label write that matches no row must NOT report success: the UPDATE matches on (id, project_id),
// so "zero rows affected" means the feature is not in that project, not that nothing changed.
func TestWorkItemLabelRefusesAFeatureFromAnotherProject(t *testing.T) {
	store, uid, pid, fid := workItemFixture(t)
	ctx := context.Background()
	other, err := store.CreateProject(ctx, uid, "other-project", "Other", "", "collective", "")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	err = store.SetWorkItemLabel(ctx, other.ID, fid, WorkItemLabel{Difficulty: diff("S")})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound for a feature in another project", err)
	}
	if err := store.SetWorkItemLabel(ctx, pid, fid, WorkItemLabel{Difficulty: diff("S")}); err != nil {
		t.Fatalf("control: the label in its own project must still work: %v", err)
	}
}

func TestClaimIsExclusive(t *testing.T) {
	store, uid, pid, fid := workItemFixture(t)
	ctx := context.Background()
	if err := store.UpdateFeatureStatus(ctx, fid, "ready"); err != nil {
		t.Fatalf("UpdateFeatureStatus: %v", err)
	}
	if err := store.ClaimFeature(ctx, pid, fid, uid); err != nil {
		t.Fatalf("ClaimFeature: %v", err)
	}
	other := newUserNamed(t, store, "claimant-two")
	if err := store.ClaimFeature(ctx, pid, fid, other); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second claim err = %v, want ErrDuplicate", err)
	}
	// Exactly one live claim, asserted in the database rather than through the API surface.
	var live int
	if err := store.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM work_item_claims WHERE feature_id=? AND released_at IS NULL`,
		fid).Scan(&live); err != nil {
		t.Fatalf("count live claims: %v", err)
	}
	if live != 1 {
		t.Fatalf("live claims = %d, want exactly 1", live)
	}
}

// Releasing keeps the row: a withdrawal is a state, not a deletion, so "picked up and dropped twice"
// stays answerable. And after a release the item can be claimed again.
func TestReleaseClaimKeepsTheRowAndFreesTheItem(t *testing.T) {
	store, uid, pid, fid := workItemFixture(t)
	ctx := context.Background()
	if err := store.UpdateFeatureStatus(ctx, fid, "ready"); err != nil {
		t.Fatalf("UpdateFeatureStatus: %v", err)
	}
	if err := store.ClaimFeature(ctx, pid, fid, uid); err != nil {
		t.Fatalf("ClaimFeature: %v", err)
	}
	if err := store.ReleaseClaim(ctx, pid, fid, uid); err != nil {
		t.Fatalf("ReleaseClaim: %v", err)
	}
	var total, live int
	if err := store.QueryRowContext(ctx,
		`SELECT COUNT(*), SUM(CASE WHEN released_at IS NULL THEN 1 ELSE 0 END)
		 FROM work_item_claims WHERE feature_id=?`, fid).Scan(&total, &live); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if total != 1 || live != 0 {
		t.Fatalf("rows=%d live=%d, want one row with no live claim", total, live)
	}
	other := newUserNamed(t, store, "claimant-three")
	if err := store.ClaimFeature(ctx, pid, fid, other); err != nil {
		t.Fatalf("claim after release: %v", err)
	}
}

func TestClaimRefusesADraftFeature(t *testing.T) {
	store, uid, pid, fid := workItemFixture(t)
	err := store.ClaimFeature(context.Background(), pid, fid, uid)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid: a draft is work nobody has agreed to do", err)
	}
}

func TestClaimRefusesAFeatureFromAnotherProject(t *testing.T) {
	store, uid, _, fid := workItemFixture(t)
	ctx := context.Background()
	if err := store.UpdateFeatureStatus(ctx, fid, "ready"); err != nil {
		t.Fatalf("UpdateFeatureStatus: %v", err)
	}
	other, err := store.CreateProject(ctx, uid, "other-claim", "Other", "", "collective", "")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := store.ClaimFeature(ctx, other.ID, fid, uid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListWorkItemsIsOrderedAndScoped(t *testing.T) {
	store, uid, pid, fid := workItemFixture(t)
	ctx := context.Background()
	// A second, cheaper feature in `ready`, plus one that stays `draft`.
	var compID int64
	if err := store.QueryRowContext(ctx,
		`SELECT id FROM complaints WHERE project_id=? AND status='validated' LIMIT 1`, pid).Scan(&compID); err != nil {
		t.Fatalf("read the fixture's validated complaint: %v", err)
	}
	cheap, err := store.CreateFeature(ctx, pid, uid, "Cheap fix", "body", "S", nil, nil, []int64{compID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	if err := store.UpdateFeatureStatus(ctx, cheap.ID, "ready"); err != nil {
		t.Fatalf("UpdateFeatureStatus(cheap): %v", err)
	}
	if err := store.SetWorkItemLabel(ctx, pid, cheap.ID, WorkItemLabel{TimeBudgetMinutes: budget(15)}); err != nil {
		t.Fatalf("SetWorkItemLabel: %v", err)
	}
	if err := store.SetWorkItemLabel(ctx, pid, fid, WorkItemLabel{TimeBudgetMinutes: budget(480)}); err != nil {
		t.Fatalf("SetWorkItemLabel: %v", err)
	}
	// The fixture's feature starts as a draft, and a draft is not work you can pick up.
	if err := store.UpdateFeatureStatus(ctx, fid, "ready"); err != nil {
		t.Fatalf("UpdateFeatureStatus(fixture): %v", err)
	}
	draft, err := store.CreateFeature(ctx, pid, uid, "Still a draft", "body", "M", nil, nil, []int64{compID})
	if err != nil {
		t.Fatalf("CreateFeature(draft): %v", err)
	}
	// An OPEN complaint is a work item; the fixture's complaint is validated, so it is not.
	open, err := store.CreateComplaint(ctx, pid, uid, "Something smells off", "body", 2, 1.0, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint(open): %v", err)
	}
	// A feature in another project must not appear in this project's list. That project needs its
	// own validated complaint first, because a feature exists only as an answer to reported pain.
	otherProject, err := store.CreateProject(ctx, uid, "elsewhere", "Elsewhere", "", "collective", "")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	elsewhereComplaint, err := store.CreateComplaint(ctx, otherProject.ID, uid, "Elsewhere is broken", "body", 3, 1.0, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint(other): %v", err)
	}
	if err := store.ValidateComplaint(ctx, elsewhereComplaint.ID); err != nil {
		t.Fatalf("ValidateComplaint(other): %v", err)
	}
	if _, err := store.CreateFeature(ctx, otherProject.ID, uid, "Not this project", "body", "S", nil, nil,
		[]int64{elsewhereComplaint.ID}); err != nil {
		t.Fatalf("CreateFeature(other): %v", err)
	}

	items, err := store.ListWorkItems(ctx, pid)
	if err != nil {
		t.Fatalf("ListWorkItems: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3 (one open complaint, two ready features): %+v", len(items), items)
	}
	if items[0].Kind != WorkKindComplaint || items[0].ID != open.ID {
		t.Errorf("first item = %+v, want the open complaint first (the fifteen-minute contribution)", items[0])
	}
	if items[1].ID != cheap.ID {
		t.Errorf("second item = %+v, want the 15-minute feature before the 480-minute one", items[1])
	}
	for _, w := range items {
		if w.ID == draft.ID {
			t.Errorf("a draft feature appeared as a work item: %+v", w)
		}
	}
}
