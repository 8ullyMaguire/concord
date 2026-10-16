package store

import (
	"context"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/governance"
)

// §6.6's second branch, and the record it has to leave.
//
//   **Override:** a second window where ≥ 80% of non-abstain participants vote to
//   override, with the objector's remedy answered in writing.
//
// `EvaluateConsensus` computes exactly that verdict -- an open block plus a decisive
// ratio at or above the charter's override ratio answers `accepted_overridden` -- and
// `CloseConsensusCall` returns it. Two things it did not do, both measured:
//
//  1. it never wrote the verdict down. The UPDATE sets `status` and `closed_at` and
//     nothing else, so `consensus_calls.result` stays NULL and the Consensus page --
//     which renders `call.result` -- shows a closed call with no outcome. The verdict
//     existed only in the HTTP response of whoever closed it.
//  2. it left the objection it overrode standing. §55's bar is "no unresolved
//     principled objection" and `TallyConsensus` counts `status = 'open'`, so a call
//     that was ACCEPTED OVER a block closed with that block still open: the record
//     contradicted its own result, and the §6.6 sentence "with the objector's remedy
//     answered in writing" had nowhere to be written.
//
// These tests are written against the real eligibility fixture (a pool of members with
// a qualifying contribution each), because an eligible population of zero makes quorum
// vacuous and the call then resolves for reasons other than the ones under test.

// overrideCallFixture builds a call that is decided by the override branch: five
// participants, four consenting and one blocking, i.e. decisive 0.8 -- exactly the
// default charter's override ratio, which is a pass (`>=`).
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

func seedOverrideCall(t *testing.T) overrideCall {
	t.Helper()
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	pool := eligibleMemberPool(t, d, pid, 6, "override-member")
	call, err := d.CreateConsensusCall(ctx, pid, 0, uid, "", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	consenters := pool[:4]
	objector := pool[4]
	for _, u := range consenters {
		if _, err := d.CastPosition(ctx, call.ID, u, "consent"); err != nil {
			t.Fatalf("CastPosition(consent) for %d: %v", u, err)
		}
	}
	if _, err := d.CastPosition(ctx, call.ID, objector, "block"); err != nil {
		t.Fatalf("CastPosition(block): %v", err)
	}
	return overrideCall{db: d, callID: call.ID, objector: objector, call: call}
}

func TestACallThatPassesByOverrideRecordsTheOverride(t *testing.T) {
	f := seedOverrideCall(t)
	objID := f.objection(t)
	ctx := context.Background()

	sum, err := f.db.CloseConsensusCall(ctx, f.callID,
		"the export resumes from the last complete file, so the half-written state is gone")
	if err != nil {
		t.Fatalf("CloseConsensusCall: %v", err)
	}
	if sum.Result != string(governance.ResultAcceptedOverridden) {
		t.Fatalf("result = %q; the fixture is meant to reach %q -- if the ratio moved, "+
			"fix the fixture rather than the assertion", sum.Result,
			governance.ResultAcceptedOverridden)
	}
	if got := f.storedResult(t); got != string(governance.ResultAcceptedOverridden) {
		t.Errorf("the stored result is %q after a close that answered %q: the verdict "+
			"exists only in the response, so the page renders a closed call with no "+
			"outcome and the decision leaves no record", got, sum.Result)
	}
	status, resolution := f.statusOf(t, objID)
	if status != "overridden" {
		t.Errorf("the objection the call was accepted OVER is %q, not %q: §55's bar is "+
			"counted from `status = 'open'`, so the call closed with its own block still "+
			"standing", status, "overridden")
	}
	if !strings.Contains(resolution, "last complete file") {
		t.Errorf("the overridden objection's resolution is %q; §6.6 says the override "+
			"happens with the objector's remedy answered in writing, and the place that "+
			"answer is written is the objection", resolution)
	}
	// And the bar itself, read through the tally the page and the close both use.
	call, err := f.db.GetConsensusCall(ctx, f.callID)
	if err != nil {
		t.Fatalf("GetConsensusCall: %v", err)
	}
	counts, err := f.db.TallyConsensus(ctx, call)
	if err != nil {
		t.Fatalf("TallyConsensus: %v", err)
	}
	if counts.OpenObjections != 0 {
		t.Errorf("open objections = %d after the override; the tally and the result "+
			"disagree about the same call", counts.OpenObjections)
	}
}

func TestAnOverrideWithNoRemedyAnsweredInWritingIsRefused(t *testing.T) {
	f := seedOverrideCall(t)
	objID := f.objection(t)

	_, err := f.db.CloseConsensusCall(context.Background(), f.callID, "   ")
	if err == nil {
		t.Fatalf("the call was accepted over a block with no note; §6.6 requires the " +
			"objector's remedy to be answered in writing, and an override that answers " +
			"nothing is a silent veto of the objection")
	}
	if !strings.Contains(err.Error(), "remedy") {
		t.Errorf("the refusal is %q; it should say what is missing", err.Error())
	}
	// Nothing may be half-written: a refused close must not close the call, record the
	// result, or touch the objection.
	if got := f.storedResult(t); got != "<NULL>" {
		t.Errorf("result = %q after a refused close", got)
	}
	if status, _ := f.statusOf(t, objID); status != "open" {
		t.Errorf("objection status = %q after a refused close; want it left open", status)
	}
	call, err := f.db.GetConsensusCall(context.Background(), f.callID)
	if err != nil {
		t.Fatalf("GetConsensusCall: %v", err)
	}
	if call.Status != "open" {
		t.Errorf("call status = %q after a refused close; want open", call.Status)
	}
}

// A block that stands is still a block: the call is refused and the objection stays
// open, because nothing answered it. Only the override branch clears the bar.
func TestABlockedCallLeavesItsObjectionStanding(t *testing.T) {
	f := seedOverrideCall(t)
	objID := f.objection(t)
	ctx := context.Background()
	// Drop the decisive ratio below the override threshold: one consent becomes a
	// stand-aside, which the decisive measure excludes (3 consent / 1 block = 0.75).
	// `LIMIT` is not available on UPDATE without SQLITE_ENABLE_UPDATE_DELETE_LIMIT, so
	// the row is named by rowid.
	if _, err := f.db.ExecContext(ctx,
		`UPDATE positions SET position = 'stand_aside'
		  WHERE rowid = (SELECT rowid FROM positions
		                  WHERE call_id = ? AND position = 'consent' LIMIT 1)`,
		f.callID); err != nil {
		t.Fatalf("stand aside: %v", err)
	}
	sum, err := f.db.CloseConsensusCall(ctx, f.callID, "the remedy is not answered")
	if err != nil {
		t.Fatalf("CloseConsensusCall: %v", err)
	}
	if sum.Result != string(governance.ResultBlocked) {
		t.Fatalf("result = %q; want %q -- the fixture has to reach the blocked branch "+
			"for this test to say anything", sum.Result, governance.ResultBlocked)
	}
	if got := f.storedResult(t); got != string(governance.ResultBlocked) {
		t.Errorf("the stored result is %q; want the verdict written down", got)
	}
	if status, _ := f.statusOf(t, objID); status != "open" {
		t.Errorf("objection status = %q after a BLOCKED call; a block that stood is not "+
			"overridden, so it must stay open", status)
	}
}

// The plain path: no block, so there is nothing to answer and no note is needed. The
// verdict still has to be recorded -- the missing UPDATE was not override-specific.
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
	sum, err := d.CloseConsensusCall(ctx, call.ID, "")
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
