package store

import (
	"context"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/governance"
)

// §6.6's first window, and the two things a close has to leave behind.
//
//   **Resolving a block:** the author amends ... or **Override:** a second window
//   where ≥ 80% of non-abstain participants vote to override, with the objector's
//   remedy answered in writing.
//
// The seventh pass measured a close that wrote neither its verdict nor the state of
// the objection it was accepted over:
//
//	PROBE verdict returned to the caller: "accepted_overridden"
//	PROBE stored result column: <nil>
//	PROBE objection status after an ACCEPTED-OVER call: "open"
//
// so the verdict lived only in the response of whoever closed the call, and a call
// accepted over a block closed with that block still standing -- §55's bar is read
// from `status = 'open'`, and the record contradicted its own result.
//
// The eighth pass moved the OVERRIDE to where §6.6 puts it (a second window, see
// override_window_test.go) and left the close with §6.6's conditions 1-4. What the
// close must still record is the verdict -- including `blocked`, which is a verdict
// and not an absence.
//
// These tests use the real eligibility fixture (a pool of members with a qualifying
// contribution each), because an eligible population of zero makes quorum vacuous and
// the call then resolves for reasons other than the ones under test.

type overrideCall struct {
	db       *DB
	callID   int64
	objector int64
	call     ConsensusCall
}

func (f overrideCall) objection(t *testing.T) int64 {
	t.Helper()
	obj, err := f.db.CreateObjection(context.Background(), f.callID, f.objector,
		"Reversibility", "a half-written export is unrecoverable", "keep the last complete file")
	if err != nil {
		t.Fatalf("CreateObjection: %v", err)
	}
	return obj.ID
}

func (f overrideCall) statusOf(t *testing.T, objID int64) (status, resolution string) {
	t.Helper()
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT status, COALESCE(resolution, '') FROM objections WHERE id = ?`, objID).
		Scan(&status, &resolution); err != nil {
		t.Fatalf("read objection %d: %v", objID, err)
	}
	return status, resolution
}

func (f overrideCall) storedResult(t *testing.T) string {
	t.Helper()
	var result *string
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT result FROM consensus_calls WHERE id = ?`, f.callID).Scan(&result); err != nil {
		t.Fatalf("read result: %v", err)
	}
	if result == nil {
		return "<NULL>"
	}
	return *result
}

// seedOverrideCall is four consents and one block: decisive 0.8 and non-abstain 0.8,
// which is exactly the default charter's override ratio -- and, more to the point,
// a call with a standing block.
func seedOverrideCall(t *testing.T) overrideCall {
	t.Helper()
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	pool := eligibleMemberPool(t, d, pid, 6, "override-member")
	call, err := d.CreateConsensusCall(ctx, pid, 0, uid, "", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	objector := pool[4]
	for _, u := range pool[:4] {
		if _, err := d.CastPosition(ctx, call.ID, u, "consent"); err != nil {
			t.Fatalf("CastPosition(consent) for %d: %v", u, err)
		}
	}
	if _, err := d.CastPosition(ctx, call.ID, objector, "block"); err != nil {
		t.Fatalf("CastPosition(block): %v", err)
	}
	return overrideCall{db: d, callID: call.ID, objector: objector, call: call}
}

// A close never overrides, however the ratios read. Under 100% consent the consent
// ratio is 1.0 and the old code answered accepted_overridden on the decisive measure;
// §6.6's conditions end with "No unresolved block", so what the close records is
// `blocked` -- and recording it is the whole point.
func TestACloseNeverOverridesABlock(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	pool := eligibleMemberPool(t, d, pid, 5, "unanimous-member")
	call, err := d.CreateConsensusCall(ctx, pid, 0, uid, "", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	objector := pool[4]
	for _, u := range pool[:4] {
		if _, err := d.CastPosition(ctx, call.ID, u, "consent"); err != nil {
			t.Fatalf("CastPosition: %v", err)
		}
	}
	if _, err := d.CastPosition(ctx, call.ID, objector, "block"); err != nil {
		t.Fatalf("CastPosition(block): %v", err)
	}
	f := overrideCall{db: d, callID: call.ID, objector: objector, call: call}
	objID := f.objection(t)

	sum, err := d.CloseConsensusCall(ctx, call.ID)
	if err != nil {
		t.Fatalf("CloseConsensusCall: %v", err)
	}
	if sum.Result != string(governance.ResultBlocked) {
		t.Fatalf("result = %q, want blocked: 4 of 4 consents is not an argument for a close overriding anything -- "+
			"§6.6's override is the second window", sum.Result)
	}
	if got := f.storedResult(t); got != string(governance.ResultBlocked) {
		t.Errorf("the stored result is %q after a close that answered %q: the verdict exists only in the "+
			"response, so the page renders a closed call with no outcome", got, sum.Result)
	}
	if status, _ := f.statusOf(t, objID); status != "open" {
		t.Errorf("objection status = %q after a blocked close; the block was not answered, so it still stands", status)
	}
}

// §6.6's condition 4 with the ratios on the other side: 3 consents, 1 stand-aside and
// 1 block is support 0.6 and decisive 0.75, both of which a blockless call would pass.
// The block is the only reason this is not `accepted`, and the verdict must say so.
func TestABlockedCallIsBlockedOnTheRatiosOwnTerms(t *testing.T) {
	f := seedOverrideCall(t)
	objID := f.objection(t)
	ctx := context.Background()
	// One consent becomes a stand-aside, which the decisive measure excludes.
	if _, err := f.db.ExecContext(ctx,
		`UPDATE positions SET position = 'stand_aside'
		  WHERE rowid = (SELECT rowid FROM positions
		                  WHERE call_id = ? AND position = 'consent' LIMIT 1)`,
		f.callID); err != nil {
		t.Fatalf("stand aside: %v", err)
	}
	sum, err := f.db.CloseConsensusCall(ctx, f.callID)
	if err != nil {
		t.Fatalf("CloseConsensusCall: %v", err)
	}
	if sum.Result != string(governance.ResultBlocked) {
		t.Fatalf("result = %q; want blocked: without the block this fixture passes both ratios "+
			"(support %.2f, decisive %.2f), so the block is the only thing deciding it",
			sum.Result, sum.Counts.SupportRatio(), sum.Counts.DecisiveRatio())
	}
	if got := f.storedResult(t); got != string(governance.ResultBlocked) {
		t.Errorf("the stored result is %q; the verdict has to be written down", got)
	}
	if status, _ := f.statusOf(t, objID); status != "open" {
		t.Errorf("objection status = %q after a BLOCKED call; a block that stood is not "+
			"overridden, so it must stay open", status)
	}
}

// The plain path: no block, nothing to answer, no note. The verdict still has to be
// recorded -- the missing UPDATE was not override-specific.
func TestACallThatPassesWithoutABlockRecordsItsResult(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	pool := eligibleMemberPool(t, d, pid, 3, "plain-member")
	call, err := d.CreateConsensusCall(ctx, pid, 0, uid, "", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	for _, u := range pool {
		if _, err := d.CastPosition(ctx, call.ID, u, "consent"); err != nil {
			t.Fatalf("CastPosition: %v", err)
		}
	}
	sum, err := d.CloseConsensusCall(ctx, call.ID)
	if err != nil {
		t.Fatalf("CloseConsensusCall: %v", err)
	}
	if sum.Result != string(governance.ResultAccepted) {
		t.Fatalf("result = %q; want accepted", sum.Result)
	}
	var result *string
	if err := d.QueryRowContext(ctx,
		`SELECT result FROM consensus_calls WHERE id = ?`, call.ID).Scan(&result); err != nil {
		t.Fatalf("read result: %v", err)
	}
	if result == nil || *result != string(governance.ResultAccepted) {
		t.Errorf("stored result = %v; want %q -- a call that was accepted and left no "+
			"record of it is a decision nobody can audit", result, governance.ResultAccepted)
	}
}

// The close reads the PROJECT'S charter too, not the model's defaults.
//
// Same defect as TestTheOverrideRatioIsTheProjectsOwn, one route over: the close used
// `DefaultCharter(governance_model)`, so a project that LOWERED its support floor had its
// calls judged against the default 0.5 while its settings page published the lowered
// number. This fixture is 3 consent and 5 reservations: support 0.375, which the default
// floor rejects and a floor of 0.3 accepts.
func TestACloseUsesTheProjectsOwnCharter(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	pool := eligibleMemberPool(t, d, pid, 8, "charter-member")
	charter, err := d.GetCharterForProject(ctx, pid)
	if err != nil {
		t.Fatalf("GetCharterForProject: %v", err)
	}
	if charter.SupportRatioMin == 0.3 {
		t.Fatal("the fixture project already has the floor this test tunes; pick another")
	}
	charter.SupportRatioMin = 0.3
	if err := d.UpdateCharter(ctx, pid, charter); err != nil {
		t.Fatalf("UpdateCharter: %v", err)
	}

	call, err := d.CreateConsensusCall(ctx, pid, 0, uid, "", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	for _, u := range pool[:3] {
		if _, err := d.CastPosition(ctx, call.ID, u, "consent"); err != nil {
			t.Fatalf("CastPosition(consent): %v", err)
		}
	}
	for _, u := range pool[3:] {
		if _, err := d.CastPosition(ctx, call.ID, u, "stand_aside"); err != nil {
			t.Fatalf("CastPosition(stand_aside): %v", err)
		}
	}
	sum, err := d.CloseConsensusCall(ctx, call.ID)
	if err != nil {
		t.Fatalf("CloseConsensusCall: %v", err)
	}
	if sum.Result != string(governance.ResultAccepted) {
		t.Fatalf("result = %q, want accepted: support is %.3f and this project's floor is 0.30 "+
			"-- a close computed from the model's default floor (0.50) would reject it",
			sum.Result, sum.Counts.SupportRatio())
	}
}
