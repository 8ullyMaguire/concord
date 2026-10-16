package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/governance"
)

// §6.6's SECOND window: "a second window where ≥ 80% of non-abstain participants
// vote to override, with the objector's remedy answered in writing".
//
// The eighth pass moved the override here for two reasons, both measured rather
// than read: a close that computed the verdict itself made the second window
// redundant (see TestACloseNeverOverridesABlock), and the denominator the close
// used was consent/(consent+block) where §6.6 says NON-ABSTAIN -- which let a room
// of 5 consents, 6 reservations and 1 block override a block on a ratio of 0.83
// while its support of 0.42 was below the floor an ordinary call must clear to
// pass at all. `governance.TestEvaluateOverride` holds that fixture; these tests
// hold the window that applies it.

const overrideNote = "the export resumes from the last complete file, so the half-written state is gone"

type blockedCallFixture struct {
	db       *DB
	project  int64
	callID   int64
	objector int64
	// voters are eligible collaborators with no stance yet, for the second window.
	voters []int64
}

// seedBlockedCall builds the state §6.6's window opens on: a call that closed
// blocked with a standing objection.
//
// The consents are not decoration. Quorum comes first -- an eligible population of 6
// needs 3 participants -- and a close below quorum records `insufficient_quorum`,
// which is a verdict but not the one a window opens on. A fixture that cast one
// position and one objection closed with "insufficient_quorum" and every test built
// on it failed on its own setup rather than on the rule under test.
func seedBlockedCall(t *testing.T, eligible int) blockedCallFixture {
	t.Helper()
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	pool := eligibleMemberPool(t, d, pid, eligible, "override-member")
	call, err := d.CreateConsensusCall(ctx, pid, 0, uid, "", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	objector := pool[0]
	for _, u := range pool[1:] {
		if _, err := d.CastPosition(ctx, call.ID, u, "consent"); err != nil {
			t.Fatalf("CastPosition(consent) for %d: %v", u, err)
		}
	}
	if _, err := d.CreateObjection(ctx, call.ID, objector,
		"Reversibility", "a half-written export is unrecoverable", "keep the last complete file"); err != nil {
		t.Fatalf("CreateObjection: %v", err)
	}
	if _, err := d.CloseConsensusCall(ctx, call.ID); err != nil {
		t.Fatalf("CloseConsensusCall: %v", err)
	}
	closed, err := d.GetConsensusCall(ctx, call.ID)
	if err != nil {
		t.Fatalf("GetConsensusCall: %v", err)
	}
	if closed.Result == nil || *closed.Result != string(governance.ResultBlocked) {
		t.Fatalf("fixture did not close blocked (result=%v) with %d eligible; the window opens "+
			"on nothing then", resultOrNull(closed.Result), eligible)
	}
	return blockedCallFixture{db: d, project: pid, callID: call.ID, objector: objector, voters: pool}
}

func (f blockedCallFixture) open(t *testing.T) OverrideWindow {
	t.Helper()
	win, err := f.db.OpenOverrideWindow(context.Background(), f.callID, f.objector)
	if err != nil {
		t.Fatalf("OpenOverrideWindow: %v", err)
	}
	return win
}

func (f blockedCallFixture) vote(t *testing.T, win OverrideWindow, stance string, users ...int64) {
	t.Helper()
	for _, u := range users {
		if _, err := f.db.CastOverridePosition(context.Background(), win.ID, u, stance, ""); err != nil {
			t.Fatalf("CastOverridePosition(%s) for %d: %v", stance, u, err)
		}
	}
}

func (f blockedCallFixture) callResult(t *testing.T) string {
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

func (f blockedCallFixture) objectionState(t *testing.T) (status, resolution string) {
	t.Helper()
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT status, COALESCE(resolution, '') FROM objections WHERE call_id = ?`, f.callID).
		Scan(&status, &resolution); err != nil {
		t.Fatalf("read objection: %v", err)
	}
	return status, resolution
}

// The headline: a window that carries sets the call's result, clears the bar and keeps
// the answer where the objection is.
func TestAnOverrideWindowCarriesAndRecordsTheOverride(t *testing.T) {
	f := seedBlockedCall(t, 6)
	ctx := context.Background()
	win := f.open(t)
	f.vote(t, win, "consent", f.voters[1], f.voters[2], f.voters[3], f.voters[4])
	f.vote(t, win, "block", f.voters[5])

	closed, err := f.db.CloseOverrideWindow(ctx, f.callID, overrideNote)
	if err != nil {
		t.Fatalf("CloseOverrideWindow: %v", err)
	}
	if closed.Result == nil || *closed.Result != string(governance.ResultOverridden) {
		t.Fatalf("window result = %v, want overridden: 4 of 5 non-abstain participants consented (%.2f)",
			closed.Result, 4.0/5.0)
	}
	if closed.Status != "closed" || closed.ClosedAt == nil {
		t.Errorf("window is %s (closed_at %v); a recorded verdict implies a closed window",
			closed.Status, closed.ClosedAt)
	}
	if !strings.Contains(closed.Note, "last complete file") {
		t.Errorf("the window's note is %q; §6.6's override happens with the remedy answered in "+
			"writing, and the answer belongs to the record", closed.Note)
	}
	if got := f.callResult(t); got != string(governance.ResultAcceptedOverridden) {
		t.Errorf("the call's result is %q, want %q: the window's verdict has to reach the call it decided",
			got, governance.ResultAcceptedOverridden)
	}
	status, resolution := f.objectionState(t)
	if status != "overridden" {
		t.Errorf("the objection is %q; a call accepted over a block whose block is still open is the "+
			"inconsistent record the seventh pass removed", status)
	}
	if !strings.Contains(resolution, "last complete file") {
		t.Errorf("the objection's resolution is %q; the answer written for the objector belongs on the objection",
			resolution)
	}
	// §55's bar, read through the tally the page uses.
	call, err := f.db.GetConsensusCall(ctx, f.callID)
	if err != nil {
		t.Fatalf("GetConsensusCall: %v", err)
	}
	counts, err := f.db.TallyConsensus(ctx, call)
	if err != nil {
		t.Fatalf("TallyConsensus: %v", err)
	}
	if counts.OpenObjections != 0 {
		t.Errorf("open objections = %d after the override; the tally and the result disagree", counts.OpenObjections)
	}
}

// A window that does not carry changes NOTHING about the call. Recording "upheld" is
// the point: a block that stood is not quietly cleared by having been voted on.
func TestAnOverrideWindowThatDoesNotCarryLeavesTheCallBlocked(t *testing.T) {
	f := seedBlockedCall(t, 6)
	ctx := context.Background()
	win := f.open(t)
	// 3 consent, 2 stand-aside: the ratios AGREE (support 0.6, decisive 1.0) and the
	// break-even for an override is consent over everyone who took a position, so 0.6
	// is short. This is the case §6.6's wording decides and the old decisive
	// denominator did not.
	f.vote(t, win, "consent", f.voters[1], f.voters[2], f.voters[3])
	f.vote(t, win, "stand_aside", f.voters[4], f.voters[5])

	closed, err := f.db.CloseOverrideWindow(ctx, f.callID, "")
	if err != nil {
		t.Fatalf("CloseOverrideWindow: %v", err)
	}
	if closed.Result == nil || *closed.Result != string(governance.ResultUpheld) {
		t.Fatalf("window result = %v, want upheld: 3 of 5 non-abstain participants is 0.60, "+
			"under the override ratio", closed.Result)
	}
	if got := f.callResult(t); got != string(governance.ResultBlocked) {
		t.Errorf("the call's result changed to %q; a window that did not carry leaves the call as it was", got)
	}
	if status, _ := f.objectionState(t); status != "open" {
		t.Errorf("the objection is %q after an upheld window; nothing answered it", status)
	}
}

// §6.6's override carries the remedy answered in writing, and the refusal happens
// before any write -- the same discipline the seventh pass applied to the close.
func TestAnOverrideNeedsTheRemedyAnsweredInWriting(t *testing.T) {
	f := seedBlockedCall(t, 6)
	ctx := context.Background()
	win := f.open(t)
	f.vote(t, win, "consent", f.voters[1], f.voters[2], f.voters[3], f.voters[4])
	f.vote(t, win, "block", f.voters[5])

	_, err := f.db.CloseOverrideWindow(ctx, f.callID, "   ")
	if err == nil {
		t.Fatalf("the window carried with no note; §6.6 requires the objector's remedy to be " +
			"answered in writing")
	}
	if !strings.Contains(err.Error(), "remedy") {
		t.Errorf("the refusal is %q; it should say what is missing", err.Error())
	}
	// Nothing may be half-written.
	win, err = f.db.GetOverrideWindow(ctx, f.callID)
	if err != nil {
		t.Fatalf("GetOverrideWindow: %v", err)
	}
	if win.Status != "open" || win.Result != nil {
		t.Errorf("window status=%s result=%v after a refused close; want it left open with no verdict",
			win.Status, win.Result)
	}
	if got := f.callResult(t); got != string(governance.ResultBlocked) {
		t.Errorf("the call's result is %q after a refused close", got)
	}
	if status, _ := f.objectionState(t); status != "open" {
		t.Errorf("the objection is %q after a refused close; want it left open", status)
	}
}

// Below quorum there is no ratio to read, and "upheld" would claim the room decided
// against the override when most of the room did not vote.
func TestAnOverrideWindowBelowQuorumHasNoVerdict(t *testing.T) {
	f := seedBlockedCall(t, 10) // quorum is max(3, ceil(0.2*10)) = 3
	ctx := context.Background()
	win := f.open(t)
	f.vote(t, win, "consent", f.voters[1], f.voters[2])

	_, err := f.db.CloseOverrideWindow(ctx, f.callID, overrideNote)
	if err == nil {
		t.Fatalf("a window with 2 of 3 required participants recorded a verdict")
	}
	if !strings.Contains(err.Error(), "participants") {
		t.Errorf("the refusal is %q; it should name the quorum it did not reach", err.Error())
	}
	win, err = f.db.GetOverrideWindow(ctx, f.callID)
	if err != nil {
		t.Fatalf("GetOverrideWindow: %v", err)
	}
	if win.Status != "open" {
		t.Errorf("window status = %s after a refused close; want it left open for the window to lapse", win.Status)
	}
}

// The window's preconditions, each with its own refusal: an override needs a recorded
// block (and the record is what the seventh pass fixed) and something still standing
// to override.
func TestAnOverrideWindowOpensOnlyOnABlockedCallWithAStandingBlock(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	ctx := context.Background()
	pool := eligibleMemberPool(t, d, pid, 4, "precond-member")
	call, err := d.CreateConsensusCall(ctx, pid, 0, uid, "", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	if _, err := d.CastPosition(ctx, call.ID, pool[0], "consent"); err != nil {
		t.Fatalf("CastPosition: %v", err)
	}

	t.Run("open call", func(t *testing.T) {
		// An OPEN objection on the open call, deliberately: without one the refusal
		// below would also be produced by the "no objection is open" check, whose
		// message happens to contain the words "closed blocked" too -- so the
		// assertion could not tell the two refusals apart, and a mutation removing the
		// call's-state precondition survived it. With the objection present, only the
		// precondition can refuse here.
		if _, err := d.CreateObjection(ctx, call.ID, pool[1], "Reversibility",
			"a shipped migration cannot be undone", "ship behind a flag"); err != nil {
			t.Fatalf("CreateObjection: %v", err)
		}
		if _, err := d.OpenOverrideWindow(ctx, call.ID, pool[0]); err == nil {
			t.Fatal("a window opened on a call that has not closed; there is no block to override yet")
		} else if !strings.Contains(err.Error(), "is open with result") {
			t.Errorf("refusal = %q; it should say what state the call is in", err.Error())
		}
	})

	t.Run("closed without a block", func(t *testing.T) {
		// Three consents and no block: quorum 3 of 4 eligible, so this closes
		// `accepted` -- a call that resolved, and one §6.6's override has nothing to
		// say about.
		pool2 := eligibleMemberPool(t, d, pid, 4, "accepted-member")
		accepted, err := d.CreateConsensusCall(ctx, pid, 0, uid, "", "")
		if err != nil {
			t.Fatalf("CreateConsensusCall: %v", err)
		}
		for _, u := range pool2[:3] {
			if _, err := d.CastPosition(ctx, accepted.ID, u, "consent"); err != nil {
				t.Fatalf("CastPosition: %v", err)
			}
		}
		sum, err := d.CloseConsensusCall(ctx, accepted.ID)
		if err != nil {
			t.Fatalf("CloseConsensusCall: %v", err)
		}
		if sum.Result != string(governance.ResultAccepted) {
			t.Fatalf("the fixture closed %q, not accepted; the refusal below would be for the wrong reason", sum.Result)
		}
		if _, err := d.OpenOverrideWindow(ctx, accepted.ID, pool2[0]); err == nil {
			t.Fatal("a window opened on a call that closed accepted; §6.6's override answers a block")
		} else if !errors.Is(err, ErrInvalid) {
			t.Errorf("refusal %v is not an ErrInvalid, so it will not map to a 400", err)
		}
	})

	t.Run("block withdrawn after the close", func(t *testing.T) {
		// The same call, now blocked, with its objection withdrawn: §6.6's other
		// branch has resolved it and a vote would decide nothing.
		blocked := seedBlockedCall(t, 4)
		objs, err := blocked.db.GetObjections(ctx, blocked.callID)
		if err != nil {
			t.Fatalf("GetObjections: %v", err)
		}
		if err := blocked.db.ResolveObjection(ctx, objs[0].ID, "withdrawn", "the author amended the export"); err != nil {
			t.Fatalf("ResolveObjection: %v", err)
		}
		if _, err := blocked.db.OpenOverrideWindow(ctx, blocked.callID, blocked.objector); err == nil {
			t.Fatal("a window opened with no open objection; there is no standing block to override")
		} else if !strings.Contains(err.Error(), "no objection is open") {
			t.Errorf("refusal = %q; it should say the block is gone", err.Error())
		}
	})
}

// UNIQUE(call_id): a second window is A window. Repeating the ballot until the room
// yields is attrition, not an override.
func TestOnlyOneOverrideWindowPerCall(t *testing.T) {
	f := seedBlockedCall(t, 4)
	f.open(t)
	_, err := f.db.OpenOverrideWindow(context.Background(), f.callID, f.objector)
	if err == nil {
		t.Fatal("a second override window opened on the same call")
	}
	if !errors.Is(err, ErrConflict) {
		t.Errorf("refusal %v is not an ErrConflict, so it will not map to a 409", err)
	}
	if !strings.Contains(err.Error(), "second") && !strings.Contains(err.Error(), "already") {
		t.Errorf("refusal = %q; it should say a window already exists", err.Error())
	}
}

// §6.6's hidden tally, applied to the second window: participation and the quorum bar
// are visible while it is open, the running counts are not.
func TestTheOverrideWindowsRunningTallyIsHiddenUntilItCloses(t *testing.T) {
	f := seedBlockedCall(t, 6)
	ctx := context.Background()
	win := f.open(t)
	f.vote(t, win, "consent", f.voters[1], f.voters[2])

	open, err := f.db.GetOverrideWindow(ctx, f.callID)
	if err != nil {
		t.Fatalf("GetOverrideWindow: %v", err)
	}
	if open.Participants != 2 || open.QuorumNeeded == 0 || open.Eligible == 0 {
		t.Errorf("open window: participants=%d quorum=%d eligible=%d; §6.6 keeps progress and the "+
			"quorum bar visible", open.Participants, open.QuorumNeeded, open.Eligible)
	}
	if open.Consent != 0 || open.Block != 0 || open.StandAside != 0 || open.Abstain != 0 {
		t.Errorf("open window: counts are visible (consent=%d block=%d stand_aside=%d abstain=%d); "+
			"a running count is a bandwagon", open.Consent, open.Block, open.StandAside, open.Abstain)
	}

	f.vote(t, win, "consent", f.voters[3], f.voters[4])
	if _, err := f.db.CloseOverrideWindow(ctx, f.callID, overrideNote); err != nil {
		t.Fatalf("CloseOverrideWindow: %v", err)
	}
	closed, err := f.db.GetOverrideWindow(ctx, f.callID)
	if err != nil {
		t.Fatalf("GetOverrideWindow: %v", err)
	}
	if closed.Consent != 4 || closed.Block != 0 {
		t.Errorf("closed window: consent=%d block=%d; a closed window's counts are the record of "+
			"the decision", closed.Consent, closed.Block)
	}
}

// A stance recorded after the verdict would disagree with the ratio that decided it.
func TestAStanceInAClosedWindowIsRefused(t *testing.T) {
	f := seedBlockedCall(t, 6)
	ctx := context.Background()
	win := f.open(t)
	f.vote(t, win, "consent", f.voters[1], f.voters[2], f.voters[3])
	if _, err := f.db.CloseOverrideWindow(ctx, f.callID, overrideNote); err != nil {
		t.Fatalf("CloseOverrideWindow: %v", err)
	}
	_, err := f.db.CastOverridePosition(ctx, win.ID, f.voters[4], "block", "")
	if err == nil {
		t.Fatal("a stance was recorded in a closed window")
	}
	if !errors.Is(err, ErrConflict) {
		t.Errorf("refusal %v is not an ErrConflict, so it will not map to a 409", err)
	}
}

// §6.6's emergency hold suspends a call, and overriding a block resolves it. Without
// this check the hold would be a pause you can vote around.
func TestAHeldCallCannotHaveItsBlockOverridden(t *testing.T) {
	f := seedBlockedCall(t, 6)
	ctx := context.Background()
	win := f.open(t)
	f.vote(t, win, "consent", f.voters[1], f.voters[2], f.voters[3], f.voters[4])
	if _, err := f.db.PlaceEmergencyHold(ctx, f.project, f.callID, f.objector,
		"security", "the export leaks credentials", 3); err != nil {
		t.Fatalf("PlaceEmergencyHold: %v", err)
	}
	_, err := f.db.CloseOverrideWindow(ctx, f.callID, overrideNote)
	if err == nil {
		t.Fatal("a call under an emergency hold had its block overridden; a hold is a pause, " +
			"and this would make it bypassable by a route it never heard of")
	}
	if !strings.Contains(err.Error(), "hold") {
		t.Errorf("refusal = %q; it should name the hold", err.Error())
	}
	if got := f.callResult(t); got != string(governance.ResultBlocked) {
		t.Errorf("the call's result is %q while held", got)
	}
}

// The window is decided by the PROJECT'S charter, not by the governance model's
// defaults.
//
// The eighth pass found `CloseConsensusCall` reading `DefaultCharter(governance_model)`
// instead of `GetCharterForProject`, which agree only until someone tunes the charter --
// and the settings page HAS a charter editor (§6.9). A project that raised its override
// ratio had its calls judged by the default number while its own page published the tuned
// one. This fixture is the difference: 4 consent / 1 block is 0.80, which carries under
// the default 0.8 and is short of a tuned 0.95.
func TestTheOverrideRatioIsTheProjectsOwn(t *testing.T) {
	f := seedBlockedCall(t, 6)
	ctx := context.Background()
	charter, err := f.db.GetCharterForProject(ctx, f.project)
	if err != nil {
		t.Fatalf("GetCharterForProject: %v", err)
	}
	if charter.OverrideRatio == 0.95 {
		t.Fatal("the fixture project already has the ratio this test tunes; pick another")
	}
	charter.OverrideRatio = 0.95
	if err := f.db.UpdateCharter(ctx, f.project, charter); err != nil {
		t.Fatalf("UpdateCharter: %v", err)
	}
	t.Cleanup(func() {
		restore, err := f.db.GetCharterForProject(context.Background(), f.project)
		if err != nil {
			t.Errorf("re-read charter: %v", err)
			return
		}
		restore.OverrideRatio = 0.8
		if err := f.db.UpdateCharter(context.Background(), f.project, restore); err != nil {
			t.Errorf("restore charter: %v", err)
		}
	})

	win := f.open(t)
	f.vote(t, win, "consent", f.voters[1], f.voters[2], f.voters[3], f.voters[4])
	f.vote(t, win, "block", f.voters[5])

	closed, err := f.db.CloseOverrideWindow(ctx, f.callID, overrideNote)
	if err != nil {
		t.Fatalf("CloseOverrideWindow: %v", err)
	}
	if closed.Result == nil || *closed.Result != string(governance.ResultUpheld) {
		t.Fatalf("window result = %v, want upheld: 4 of 5 non-abstain participants is 0.80, "+
			"and this project raised its override ratio to 0.95 -- a verdict computed from the "+
			"model's defaults would carry it", closed.Result)
	}
	if got := f.callResult(t); got != string(governance.ResultBlocked) {
		t.Errorf("the call's result is %q; the tuned ratio refused the override", got)
	}
}

// §6.6's window carries at "≥ 80% of non-abstain participants", and the objector is one of
// them whenever they are §8.2-eligible — a member with a qualifying contribution, which is
// the ordinary case. Nothing in the code excludes them, and through the eighth pass that was
// coherent but unrecorded: REMAINING §20.7 item 3 listed it as "coherent but untested as a
// product decision".
//
// It is a decision now, and this is where it is recorded: the objector may vote to uphold
// their own block, that stance counts like any other, and they may equally consent to being
// overridden. The reason to keep it is the denominator — §6.6 counts everyone who took a
// position, and the objector's position is the most informative one in the window.
//
// The arithmetic is what makes the first case load-bearing rather than decorative: three
// consents of four non-abstain participants is 3/4 = 0.75, which does NOT carry, while the
// same three consents with the objector abstaining is 3/3 = 1.00, which does. The objector's
// own stance decides the window, which is exactly the kind of outcome that should be
// intended rather than discovered.
func TestTheObjectorsStanceInTheirOwnWindowIsCounted(t *testing.T) {
	ctx := context.Background()

	t.Run("the objector may uphold their own block, and that stance decides the window", func(t *testing.T) {
		f := seedBlockedCall(t, 6)
		win := f.open(t)
		f.vote(t, win, "consent", f.voters[1], f.voters[2], f.voters[3])

		if _, err := f.db.CastOverridePosition(ctx, win.ID, f.objector, "block", ""); err != nil {
			t.Fatalf("the objector could not take a position in their own window: %v\n"+
				"§6.6 counts non-abstain PARTICIPANTS, and an eligible objector is one", err)
		}
		counts, err := f.db.TallyOverride(ctx, win)
		if err != nil {
			t.Fatalf("TallyOverride: %v", err)
		}
		if counts.Participants != 4 || counts.Consent != 3 || counts.Block != 1 {
			t.Fatalf("tally = %d participants (%d consent, %d block), want 4 (3 consent, 1 block): "+
				"the objector's own stance is a position like any other",
				counts.Participants, counts.Consent, counts.Block)
		}
		if ratio := counts.SupportRatio(); ratio >= 0.8 {
			t.Fatalf("support ratio = %.2f, want under the 0.80 override ratio: 3 of 4 "+
				"non-abstain participants consent", ratio)
		}

		// No note: §6.6 requires one when the window CARRIES, and this one does not.
		closed, err := f.db.CloseOverrideWindow(ctx, f.callID, "")
		if err != nil {
			t.Fatalf("CloseOverrideWindow: %v", err)
		}
		if closed.Result == nil || *closed.Result != string(governance.ResultUpheld) {
			t.Fatalf("window result = %v, want upheld: the objector's own block is the "+
				"difference between 3/4 and the 3/3 that would have carried", closed.Result)
		}
		if got := f.callResult(t); got != string(governance.ResultBlocked) {
			t.Errorf("the call's result is %q; a window that does not carry leaves it as it was", got)
		}
		if status, _ := f.objectionState(t); status != "open" {
			t.Errorf("the objection is %q; a block that stood is not cleared by having been voted on", status)
		}
	})

	t.Run("the same three consents carry when the objector abstains", func(t *testing.T) {
		f := seedBlockedCall(t, 6)
		win := f.open(t)
		f.vote(t, win, "consent", f.voters[1], f.voters[2], f.voters[3])
		f.vote(t, win, "abstain", f.objector)

		counts, err := f.db.TallyOverride(ctx, win)
		if err != nil {
			t.Fatalf("TallyOverride: %v", err)
		}
		if counts.Participants != 4 || counts.Consent != 3 || counts.Abstain != 1 {
			t.Fatalf("tally = %d participants (%d consent, %d abstain), want 4 (3 consent, 1 abstain)",
				counts.Participants, counts.Consent, counts.Abstain)
		}
		closed, err := f.db.CloseOverrideWindow(ctx, f.callID, overrideNote)
		if err != nil {
			t.Fatalf("CloseOverrideWindow: %v", err)
		}
		if closed.Result == nil || *closed.Result != string(governance.ResultOverridden) {
			t.Fatalf("window result = %v, want overridden: an abstain is not a non-abstain "+
				"participant, so the same three consents read 3/3", closed.Result)
		}
		if got := f.callResult(t); got != string(governance.ResultAcceptedOverridden) {
			t.Errorf("the call's result is %q; the window that carried has to reach the call", got)
		}
	})

	t.Run("the objector may consent to being overridden", func(t *testing.T) {
		f := seedBlockedCall(t, 6)
		win := f.open(t)
		f.vote(t, win, "consent", f.voters[1], f.voters[2], f.voters[3], f.objector)

		closed, err := f.db.CloseOverrideWindow(ctx, f.callID, overrideNote)
		if err != nil {
			t.Fatalf("CloseOverrideWindow: %v", err)
		}
		if closed.Result == nil || *closed.Result != string(governance.ResultOverridden) {
			t.Fatalf("window result = %v, want overridden: the objector's own consent is a consent",
				closed.Result)
		}
		if status, resolution := f.objectionState(t); status != "overridden" || resolution != overrideNote {
			t.Errorf("objection = (%q, %q), want (overridden, the note): the answer belongs on the "+
				"objection the window resolved", status, resolution)
		}
	})
}
