package store

import (
	"errors"
	"testing"
)

// RejectComplaint had NO test, and no caller either -- which is how it sat unnoticed for
// the life of the feature. The spec's complaint lifecycle is
// `Open -> Validated -> Linked -> Closed / Rejected` (§M1), and the API could reach only
// Validated: there was no reject route, and the store function that implements it was
// called from nowhere in production and nowhere in the suite.
//
// A function nothing calls and nothing tests is the shape this repository keeps finding,
// so these are the invariants a rejection is supposed to hold -- asserted against the row
// and against the reputation ledger, because the penalty is half of what a rejection IS.

func TestRejectComplaintClosesItAndPenalisesTheAuthor(t *testing.T) {
	st, uid := setupWithUser(t)
	ctx := t.Context()

	proj, err := st.CreateProject(ctx, uid, "reject-a", "Reject A", "", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	c, err := st.CreateComplaint(ctx, proj.ID, uid, "buy my unrelated product", "spam", 1, 1, 1)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	before, err := st.GetReputation(ctx, proj.ID, uid)
	if err != nil {
		t.Fatalf("GetReputation before: %v", err)
	}

	if err := st.RejectComplaint(ctx, c.ID); err != nil {
		t.Fatalf("RejectComplaint: %v", err)
	}

	// (1) The state §M1 names, on the row.
	got, err := st.GetComplaint(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetComplaint: %v", err)
	}
	if got.Status != "rejected" {
		t.Errorf("status is %q after a rejection, want rejected", got.Status)
	}

	// (2) The M1 spam penalty, in the ledger rather than in the reputation total alone:
	// a total that moved could have moved for any reason, and the KIND is what says the
	// rejection caused it.
	var kind string
	var points float64
	if err := st.QueryRowContext(ctx,
		`SELECT kind, points FROM reputation_events WHERE project_id=? AND user_id=?
		 ORDER BY rowid DESC LIMIT 1`, proj.ID, uid).Scan(&kind, &points); err != nil {
		t.Fatalf("read the reputation event: %v", err)
	}
	if kind != "complaint_rejected" || points != -1 {
		t.Errorf("the penalty is %q/%g, want complaint_rejected/-1", kind, points)
	}
	after, err := st.GetReputation(ctx, proj.ID, uid)
	if err != nil {
		t.Fatalf("GetReputation after: %v", err)
	}
	if after != before-1 {
		t.Errorf("reputation went %g -> %g; a rejection costs one point", before, after)
	}

	// (3) And it is terminal. A second rejection must not punish twice, which is what
	// the "only open complaints can be rejected" precondition is for.
	if err := st.RejectComplaint(ctx, c.ID); !errors.Is(err, ErrInvalid) {
		t.Errorf("rejecting a closed complaint returned %v, want ErrInvalid", err)
	}
	again, err := st.GetReputation(ctx, proj.ID, uid)
	if err != nil {
		t.Fatalf("GetReputation at the end: %v", err)
	}
	if again != after {
		t.Errorf("the refused second rejection still moved reputation: %g -> %g", after, again)
	}
}

// A validated complaint is not spam, and the row's precondition says so.
func TestAValidatedComplaintCannotThenBeRejected(t *testing.T) {
	st, uid := setupWithUser(t)
	ctx := t.Context()

	proj, err := st.CreateProject(ctx, uid, "reject-b", "Reject B", "", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	c, err := st.CreateComplaint(ctx, proj.ID, uid, "a real one", "body", 3, 1, 1)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := st.ValidateComplaint(ctx, c.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	if err := st.RejectComplaint(ctx, c.ID); !errors.Is(err, ErrInvalid) {
		t.Errorf("rejecting a validated complaint returned %v, want ErrInvalid", err)
	}
	got, err := st.GetComplaint(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetComplaint: %v", err)
	}
	if got.Status != "validated" {
		t.Errorf("status is %q; a refused rejection must leave the row alone", got.Status)
	}
}
