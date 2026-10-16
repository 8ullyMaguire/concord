package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// §354's extra link. `LinkComplaint` existed with a body and no caller, and the body's missing
// rule is what these tests are for: it took two bare ids, and both ids are global -- so nothing
// stopped it joining a feature in one project to a complaint in another. The route that finally
// calls it proves the FEATURE belongs to the URL's project (requireFeatureInProject); the
// complaint arrives in a body and nothing above this layer can tell whose it is.
//
// Two projects, not one with a missing row: "another project" has to be a real second project
// with its own valid complaint, or the cross-project test would pass because the complaint does
// not exist for an unrelated reason.
type linkFixture struct {
	store  *DB
	uid    int64
	pid    int64
	other  int64
	linked Complaint // validated, attached to the feature at creation
	spare  Complaint // validated, NOT attached: the one the extra link exists for
	open   Complaint // open, so linking it must be refused
	feat   Feature
}

func setupLinkFixture(t *testing.T) linkFixture {
	t.Helper()
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	var fx linkFixture
	fx.store, fx.uid, fx.pid = store, uid, pid

	other, err := store.CreateProject(ctx, uid, "other-project", "Other", "the second project",
		"collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject(other): %v", err)
	}
	fx.other = other.ID

	mk := func(project int64, title string, validate bool) Complaint {
		c, err := store.CreateComplaint(ctx, project, uid, title, "body", 3, 0.8, 1.0)
		if err != nil {
			t.Fatalf("CreateComplaint(%q): %v", title, err)
		}
		if validate {
			if err := store.ValidateComplaint(ctx, c.ID); err != nil {
				t.Fatalf("ValidateComplaint(%q): %v", title, err)
			}
		}
		return c
	}
	fx.linked = mk(pid, "Pages take too long", true)
	fx.spare = mk(pid, "The export drops rows", true)
	fx.open = mk(pid, "Something smells off", false)

	f, err := store.CreateFeature(ctx, pid, uid, "Speed up the page load", "body", "M", nil, nil,
		[]int64{fx.linked.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	fx.feat = f
	return fx
}

func (fx linkFixture) complaintIDs(t *testing.T) []int64 {
	t.Helper()
	cs, err := fx.store.GetFeatureComplaints(context.Background(), fx.feat.ID)
	if err != nil {
		t.Fatalf("GetFeatureComplaints: %v", err)
	}
	ids := make([]int64, 0, len(cs))
	for _, c := range cs {
		ids = append(ids, c.ID)
	}
	return ids
}

func TestLinkComplaintAttachesAValidatedComplaint(t *testing.T) {
	fx := setupLinkFixture(t)
	ctx := context.Background()

	before := fx.complaintIDs(t)
	if len(before) != 1 || before[0] != fx.linked.ID {
		t.Fatalf("fixture: linked complaints = %v, want [%d]", before, fx.linked.ID)
	}

	if err := fx.store.LinkComplaint(ctx, fx.pid, fx.feat.ID, fx.spare.ID); err != nil {
		t.Fatalf("LinkComplaint: %v", err)
	}

	// The count AND the identity: a link is "this complaint is now attached", not
	// "there is one more row of something".
	after := fx.complaintIDs(t)
	if len(after) != 2 {
		t.Fatalf("linked complaints = %v, want 2", after)
	}
	found := false
	for _, id := range after {
		if id == fx.spare.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("complaint %d is not among the feature's complaints %v", fx.spare.ID, after)
	}
}

func TestLinkComplaintIsIdempotent(t *testing.T) {
	fx := setupLinkFixture(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := fx.store.LinkComplaint(ctx, fx.pid, fx.feat.ID, fx.spare.ID); err != nil {
			t.Fatalf("LinkComplaint call %d: %v", i+1, err)
		}
	}
	if got := fx.complaintIDs(t); len(got) != 2 {
		t.Errorf("linked complaints = %v, want 2 after linking the same complaint twice", got)
	}
}

func TestLinkComplaintRefusesAComplaintFromAnotherProject(t *testing.T) {
	fx := setupLinkFixture(t)
	ctx := context.Background()

	foreign, err := fx.store.CreateComplaint(ctx, fx.other, fx.uid, "Belongs elsewhere",
		"body", 3, 0.8, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint(foreign): %v", err)
	}
	if err := fx.store.ValidateComplaint(ctx, foreign.ID); err != nil {
		t.Fatalf("ValidateComplaint(foreign): %v", err)
	}

	err = fx.store.LinkComplaint(ctx, fx.pid, fx.feat.ID, foreign.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound for a complaint in another project", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "not in project") {
		t.Errorf("message %q does not say which rule refused it", msg)
	}
	// The refusal is the claim, not the error: a function that returns an error and
	// writes the row anyway passes an errors.Is test.
	if got := fx.complaintIDs(t); len(got) != 1 {
		t.Errorf("linked complaints = %v after a refused link, want 1", got)
	}
}

func TestLinkComplaintRefusesAFeatureFromAnotherProject(t *testing.T) {
	fx := setupLinkFixture(t)
	ctx := context.Background()

	// The same cross-project join from the other side, arranged so that ONLY the
	// feature check can refuse it: the complaint offered is a valid one from the OTHER
	// project, so the complaint-side comparison is satisfied. Without that, the
	// complaint guard refuses first and this test passes while the guard it names is
	// dead -- which is exactly what happened: a mutant removing the feature check
	// SURVIVED this test until the fixture became a complaint belonging to the other
	// project. Two guards that answer the same way are one guard as far as a test can
	// tell, so the fixture has to leave only one of them able to answer.
	foreignComplaint, err := fx.store.CreateComplaint(ctx, fx.other, fx.uid, "The other one",
		"body", 3, 0.8, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint(foreign): %v", err)
	}
	if err := fx.store.ValidateComplaint(ctx, foreignComplaint.ID); err != nil {
		t.Fatalf("ValidateComplaint(foreign): %v", err)
	}

	err = fx.store.LinkComplaint(ctx, fx.other, fx.feat.ID, foreignComplaint.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound for a feature in another project", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "feature") {
		t.Errorf("message %q does not say which id is out of place", msg)
	}
	if got := fx.complaintIDs(t); len(got) != 1 {
		t.Errorf("linked complaints = %v after a refused link, want 1", got)
	}
}

func TestLinkComplaintRefusesAnUnvalidatedComplaint(t *testing.T) {
	fx := setupLinkFixture(t)
	ctx := context.Background()

	err := fx.store.LinkComplaint(ctx, fx.pid, fx.feat.ID, fx.open.ID)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid for an open complaint", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "validated") {
		t.Errorf("message %q does not name the rule", msg)
	}
	if got := fx.complaintIDs(t); len(got) != 1 {
		t.Errorf("linked complaints = %v after a refused link, want 1", got)
	}
}
