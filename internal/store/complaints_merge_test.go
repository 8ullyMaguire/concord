package store

import (
	"errors"
	"testing"
)

// MergeComplaints had NO tests at all, which is how it kept a missing guard, an audit
// row naming actor 0, and a handler that read a path parameter that does not exist.
// These are the invariants it is supposed to hold.

// A merge may not cross projects.
//
// The check did not exist: the function loaded both complaints and compared nothing, so
// a maintainer of one project could fold that project's complaint into a complaint
// belonging to another project -- and the source's impacts were then INSERT OR IGNORE'd
// against a foreign complaint, indistinguishable from a legitimate impact afterwards.
// Same shape as the catalogue's Defect 4: loading the row is not a guard on the row.
func TestMergeComplaintsRefusesToCrossProjects(t *testing.T) {
	st, uid := setupWithUser(t)
	ctx := t.Context()

	pa, err := st.CreateProject(ctx, uid, "merge-a", "Merge A", "", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject A: %v", err)
	}
	pb, err := st.CreateProject(ctx, uid, "merge-b", "Merge B", "", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject B: %v", err)
	}

	source, err := st.CreateComplaint(ctx, pa.ID, uid, "the pain", "body", 3, 1.0, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint source: %v", err)
	}
	foreign, err := st.CreateComplaint(ctx, pb.ID, uid, "another project's pain", "body", 3, 1.0, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint foreign: %v", err)
	}

	err = st.MergeComplaints(ctx, source.ID, foreign.ID, uid)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-project merge returned %v, want ErrInvalid", err)
	}
	// And nothing moved.
	after, err := st.GetComplaint(ctx, source.ID)
	if err != nil {
		t.Fatalf("GetComplaint: %v", err)
	}
	if after.MergedInto != nil {
		t.Errorf("the source complaint was marked merged into %v by a refused merge",
			*after.MergedInto)
	}
}

// A complaint cannot be merged into itself: merged_into would point at its own row, and
// every reader that follows the chain loops.
func TestMergeComplaintsRefusesToMergeIntoItself(t *testing.T) {
	st, uid := setupWithUser(t)
	ctx := t.Context()

	pid, err := st.CreateProject(ctx, uid, "merge-self", "Merge Self", "", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	c, err := st.CreateComplaint(ctx, pid.ID, uid, "the pain", "body", 3, 1.0, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}

	if err := st.MergeComplaints(ctx, c.ID, c.ID, uid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("self-merge returned %v, want ErrInvalid", err)
	}
}

// The legitimate case, which also pins the audit row's actor: it recorded 0 (nobody)
// for every merge, so the one write in the store that most needs "who did this" had no
// answer.
func TestMergeComplaintsRecordsTheActorAndMergesInProject(t *testing.T) {
	st, uid := setupWithUser(t)
	ctx := t.Context()

	pid, err := st.CreateProject(ctx, uid, "merge-ok", "Merge Ok", "", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	source, err := st.CreateComplaint(ctx, pid.ID, uid, "duplicate", "body", 3, 1.0, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint source: %v", err)
	}
	target, err := st.CreateComplaint(ctx, pid.ID, uid, "the original", "body", 3, 1.0, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint target: %v", err)
	}

	if err := st.MergeComplaints(ctx, source.ID, target.ID, uid); err != nil {
		t.Fatalf("MergeComplaints: %v", err)
	}
	after, err := st.GetComplaint(ctx, source.ID)
	if err != nil {
		t.Fatalf("GetComplaint: %v", err)
	}
	if after.MergedInto == nil || *after.MergedInto != target.ID {
		t.Fatalf("merged_into = %v, want %d", after.MergedInto, target.ID)
	}

	page, err := st.ListAudit(ctx, AuditFilter{ProjectID: pid.ID, Action: "merge_complaints"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("audit rows for the merge = %d, want 1", len(page.Entries))
	}
	if page.Entries[0].ActorID == nil || *page.Entries[0].ActorID != uid {
		t.Errorf("the audit row records actor %v, want %d -- a merge is exactly the "+
			"action an operator needs attributed", page.Entries[0].ActorID, uid)
	}
}
