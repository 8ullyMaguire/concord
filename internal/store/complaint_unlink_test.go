package store

import (
	"context"
	"strings"
	"testing"
)

// The unlink is the inverse §354 never shipped: `feature_complaints` could grow and never
// shrink, so a feature filed against the wrong complaint stayed attached for good and
// "convert a feature between complaint-backed and idea-first" was a one-way door.
//
// The interesting tests are not "the row goes away" -- that is one DELETE -- they are the
// two policies the other removal paths already set, and the hole this one would open if
// neither applied:
//
//   * idempotent, like LinkComplaint: a retried request must not fail;
//   * cross-project refused, like LinkComplaint: both ids are global;
//   * removal does NOT un-ready anything, like RemoveFeatureEvidence -- so the gate that
//     matters has to be the NEXT transition, and this file proves it still is.

func TestUnlinkComplaintRemovesOneLinkAndLeavesTheRest(t *testing.T) {
	fx := setupLinkFixture(t)
	ctx := context.Background()

	if err := fx.store.LinkComplaint(ctx, fx.pid, fx.feat.ID, fx.spare.ID); err != nil {
		t.Fatalf("LinkComplaint: %v", err)
	}
	if got := fx.complaintIDs(t); len(got) != 2 {
		t.Fatalf("fixture: linked complaints = %v, want 2", got)
	}

	if err := fx.store.UnlinkComplaint(ctx, fx.pid, fx.feat.ID, fx.linked.ID); err != nil {
		t.Fatalf("UnlinkComplaint: %v", err)
	}

	// Identity, not count: "one complaint is attached" is satisfied by the wrong one.
	after := fx.complaintIDs(t)
	if len(after) != 1 || after[0] != fx.spare.ID {
		t.Fatalf("linked complaints = %v, want [%d]", after, fx.spare.ID)
	}
}

func TestUnlinkComplaintIsIdempotent(t *testing.T) {
	fx := setupLinkFixture(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := fx.store.UnlinkComplaint(ctx, fx.pid, fx.feat.ID, fx.linked.ID); err != nil {
			t.Fatalf("UnlinkComplaint (attempt %d): %v", i+1, err)
		}
	}
	// A retried request must leave the same state and no error: the response is the
	// feature's remaining complaints, and that list is the truth.
	if got := fx.complaintIDs(t); len(got) != 0 {
		t.Fatalf("linked complaints = %v, want none", got)
	}
}

func TestUnlinkComplaintRefusesAComplaintFromAnotherProject(t *testing.T) {
	fx := setupLinkFixture(t)
	ctx := context.Background()

	foreign, err := fx.store.CreateComplaint(ctx, fx.other, fx.uid, "Not yours", "body", 2, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint(other): %v", err)
	}
	if err := fx.store.ValidateComplaint(ctx, foreign.ID); err != nil {
		t.Fatalf("ValidateComplaint(other): %v", err)
	}

	// The id exists and is validated; only the project is wrong. Without the guard this
	// would report "removed" while deleting nothing, and the reader would believe a
	// complaint they never attached had been detached.
	err = fx.store.UnlinkComplaint(ctx, fx.pid, fx.feat.ID, foreign.ID)
	if err == nil {
		t.Fatal("unlinking another project's complaint succeeded")
	}
	if got := fx.complaintIDs(t); len(got) != 1 || got[0] != fx.linked.ID {
		t.Fatalf("the refusal changed the links: %v", got)
	}
}

func TestUnlinkComplaintRefusesAFeatureFromAnotherProject(t *testing.T) {
	fx := setupLinkFixture(t)
	ctx := context.Background()

	// The feature is real and DOES cite this complaint; the project in the call is not its
	// own. Both ids being global is exactly why this has to be refused rather than
	// resolved: a nested route that checks one id and acts on the other is the defect this
	// signature exists to prevent.
	err := fx.store.UnlinkComplaint(ctx, fx.other, fx.feat.ID, fx.linked.ID)
	if err == nil {
		t.Fatal("unlinking through another project's id succeeded")
	}
	if got := fx.complaintIDs(t); len(got) != 1 {
		t.Fatalf("the refusal changed the links: %v", got)
	}
}

// The hole this route would open, and the reason in_progress re-checks evidence: unlinking
// the last complaint from a READY feature is legal (removal never un-readies anything, by
// the policy RemoveFeatureEvidence set), so if `in_progress` trusted that the `ready` gate
// had already run, "ready with nothing behind it" would be one edit away from work.
func TestUnlinkingTheLastComplaintDoesNotUnReadyButDoesBlockTheStart(t *testing.T) {
	fx := setupLinkFixture(t)
	ctx := context.Background()

	if err := fx.store.UpdateFeatureStatus(ctx, fx.feat.ID, "ready"); err != nil {
		t.Fatalf("ready with a validated complaint: %v", err)
	}
	if err := fx.store.UnlinkComplaint(ctx, fx.pid, fx.feat.ID, fx.linked.ID); err != nil {
		t.Fatalf("UnlinkComplaint: %v", err)
	}

	// NOT un-readied: silently undoing a decision because an edit touched its evidence is
	// worse than letting the next reader see the state, and that is the documented policy
	// of the sibling removal path.
	f, err := fx.store.GetFeature(ctx, fx.feat.ID)
	if err != nil {
		t.Fatalf("GetFeature: %v", err)
	}
	if f.Status != "ready" {
		t.Fatalf("unlinking changed the status to %q; removal must not un-ready work", f.Status)
	}

	err = fx.store.UpdateFeatureStatus(ctx, fx.feat.ID, "in_progress")
	if err == nil {
		t.Fatal("a ready feature with no evidence started work")
	}
	if !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("the refusal does not name the rule: %v", err)
	}
	if f, _ := fx.store.GetFeature(ctx, fx.feat.ID); f.Status != "ready" {
		t.Fatalf("the refused transition still moved the status to %q", f.Status)
	}

	// And the door is open again the moment there is other evidence: a document counts,
	// which is what makes the rule a rule rather than a dead end.
	doc, err := fx.store.PutDocument(ctx, fx.pid, fx.uid, "spec", "README-doc", "body", "")
	if err != nil {
		t.Fatalf("PutDocument: %v", err)
	}
	if err := fx.store.AddFeatureEvidence(ctx, fx.pid, fx.feat.ID, EvidenceDocument, doc.ID); err != nil {
		t.Fatalf("AddFeatureEvidence: %v", err)
	}
	if err := fx.store.UpdateFeatureStatus(ctx, fx.feat.ID, "in_progress"); err != nil {
		t.Fatalf("in_progress with a document as evidence: %v", err)
	}
}
