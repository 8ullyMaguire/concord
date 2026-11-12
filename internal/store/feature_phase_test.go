package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The feature's status and the consensus record were never wired together.
//
// Measured on the deployed instance: 87 features carry status='consensus' and
// consensus_calls holds ZERO rows. Two defects in one:
//
//   - nothing ever sets features.status='consensus' -- createSolutionCall moves
//     the SOLUTION into consensus (its own ladder) and leaves the feature where it
//     was, so a call can be open, decided and closed while the board still shows
//     the feature in discussion;
//   - CreateConsensusCall refused every status but draft/discussion, so a feature
//     that DID carry 'consensus' -- the 87, set by an import -- could never open
//     the call that the status claims is in progress. The phase was reachable by a
//     writer that bypasses the gate and unreachable by the gate itself.
//
// The fix is the pair the rest of this file already follows for solutions: the
// status follows the decision, and the write is audited so the board's claim is
// attributable to the act that made it.

// openTestCall opens a call on the fixture's feature through the early path, which
// needs only a leader in the arena and a collaborator role -- the arena-decided path
// is proven elsewhere and is not what these tests are about.
func (f callFixture) openTestCall(t *testing.T) ConsensusCall {
	t.Helper()
	call, err := f.d.OpenSolutionCallEarly(context.Background(), f.feature, f.author, "test: opening to exercise the phase move")
	if err != nil {
		t.Fatalf("OpenSolutionCallEarly: %v", err)
	}
	return call
}

func featureStatus(t *testing.T, d *DB, id int64) string {
	t.Helper()
	f, err := d.GetFeature(context.Background(), id)
	if err != nil {
		t.Fatalf("GetFeature(%d): %v", id, err)
	}
	return f.Status
}

func TestOpeningACallMovesTheFeatureIntoConsensus(t *testing.T) {
	f := newCallFixture(t)
	before := featureStatus(t, f.d, f.feature)
	if before != "draft" {
		t.Fatalf("fixture status = %q, want draft", before)
	}

	call := f.openTestCall(t)

	if got := featureStatus(t, f.d, f.feature); got != "consensus" {
		t.Errorf("feature status after opening call %d = %q, want consensus -- a call in progress is what the status means", call.ID, got)
	}
}

// The board's claim has to be attributable: a status move with no audit row is the
// same class of lie as the 87 rows this test file exists for.
func TestThePhaseMoveIsInTheAuditLogNamingTheCall(t *testing.T) {
	f := newCallFixture(t)
	call := f.openTestCall(t)

	var detail string
	err := f.d.QueryRowContext(context.Background(), `
		SELECT detail FROM audit_log
		WHERE entity = 'feature' AND entity_id = ? AND action = 'feature.status'
		ORDER BY id DESC LIMIT 1`, f.feature).Scan(&detail)
	if err != nil {
		t.Fatalf("no feature.status audit row: %v", err)
	}
	if !strings.Contains(detail, "-> consensus") {
		t.Errorf("detail = %q, want the transition into consensus", detail)
	}
	if !strings.Contains(detail, fmt.Sprint(call.ID)) {
		t.Errorf("detail = %q, want the call id %d -- the move happened because of that call", detail, call.ID)
	}
}

func TestAnAcceptedOutcomeReadiesTheFeature(t *testing.T) {
	f := newCallFixture(t)
	call := f.openTestCall(t)

	if _, err := f.d.RecordCallOutcome(context.Background(), call.ID, OutcomeAccepted, "adopted", 0, nil); err != nil {
		t.Fatalf("RecordCallOutcome: %v", err)
	}
	if got := featureStatus(t, f.d, f.feature); got != "ready" {
		t.Errorf("status after an accepted outcome = %q, want ready", got)
	}
}

func TestARejectedOutcomeRejectsTheFeature(t *testing.T) {
	f := newCallFixture(t)
	call := f.openTestCall(t)

	if _, err := f.d.RecordCallOutcome(context.Background(), call.ID, OutcomeRejected, "not now", 0, nil); err != nil {
		t.Fatalf("RecordCallOutcome: %v", err)
	}
	if got := featureStatus(t, f.d, f.feature); got != "rejected" {
		t.Errorf("status after a rejected outcome = %q, want rejected", got)
	}
}

// Sent back is NOT rejected: the call decided not to decide, so the feature returns
// to discussion. Rejected would record a decision nobody made.
func TestASentBackOutcomeReturnsTheFeatureToDiscussion(t *testing.T) {
	f := newCallFixture(t)
	call := f.openTestCall(t)

	if _, err := f.d.RecordCallOutcome(context.Background(), call.ID, OutcomeSentBack, "needs more work", 0, nil); err != nil {
		t.Fatalf("RecordCallOutcome: %v", err)
	}
	if got := featureStatus(t, f.d, f.feature); got != "discussion" {
		t.Errorf("status after a sent-back outcome = %q, want discussion", got)
	}
}

// The live state: a feature parked at 'consensus' with no call anywhere. It must be
// able to open one, or the status is a dead end that only a database write can leave.
func TestAFeatureAlreadyInConsensusCanStillOpenItsFirstCall(t *testing.T) {
	f := newCallFixture(t)
	ctx := context.Background()
	if err := f.d.UpdateFeatureStatus(ctx, f.feature, "consensus"); err != nil {
		t.Fatalf("park the feature in consensus: %v", err)
	}

	call, err := f.d.OpenSolutionCallEarly(ctx, f.feature, f.author, "the status said a decision was in progress")
	if err != nil {
		t.Fatalf("OpenSolutionCallEarly on a feature parked at consensus: %v -- the 87 live rows are exactly this state", err)
	}
	if call.ID == 0 {
		t.Error("no call id")
	}
}

// The other direction: the same status must not become a way to open a SECOND call
// while one is open. Otherwise "in consensus" means "as many decisions as you like".
func TestASecondCallIsRefusedWhileOneIsOpen(t *testing.T) {
	f := newCallFixture(t)
	f.openTestCall(t)

	_, err := f.d.OpenSolutionCallEarly(context.Background(), f.feature, f.author, "second")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("second call: err = %v, want ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "already open") {
		t.Errorf("refusal = %q, want it to say a call is already open", err)
	}
	// And the refusal must not have moved anything.
	if got := featureStatus(t, f.d, f.feature); got != "consensus" {
		t.Errorf("status after a refused second call = %q, want consensus", got)
	}
}

// A shipped feature is not up for a decision. This is the one case the old message
// named correctly and the relaxation must keep refusing.
func TestAShippedFeatureCannotOpenACall(t *testing.T) {
	f := newCallFixture(t)
	ctx := context.Background()
	if _, err := f.d.ExecContext(ctx, `UPDATE features SET status='shipped' WHERE id=?`, f.feature); err != nil {
		t.Fatalf("shipped: %v", err)
	}
	_, err := f.d.CreateConsensusCall(ctx, f.project, f.feature, f.author, "t", "q")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid for a shipped feature", err)
	}
	if !strings.Contains(err.Error(), "shipped") {
		t.Errorf("refusal = %q, want it to name the status it found", err)
	}
}
