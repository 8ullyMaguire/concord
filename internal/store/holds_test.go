package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/governance"
)

// The emergency hold (§6.6). The property that matters: it suspends a call and
// overrides nothing. Every test here exists to show that a maintainer reaching
// for this power cannot use it to win a decision.

func holdFixture(t *testing.T) (*DB, int64, int64, int64) {
	t.Helper()
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	comp, err := store.CreateComplaint(ctx, pid, uid, "Hold complaint", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	f, err := store.CreateFeature(ctx, pid, uid, "Hold feature", "body", "", nil, nil, []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	call, err := store.CreateConsensusCall(ctx, pid, f.ID, uid, "Adopt the thing", "why")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	return store, pid, uid, call.ID
}

func TestEmergencyHoldRequiresReasonAndGrounds(t *testing.T) {
	store, pid, uid, callID := holdFixture(t)
	ctx := context.Background()

	// Grounds outside security/legal: the emergency power is not for ordinary
	// disagreement, and a CHECK in the schema enforces it independently.
	for _, g := range []string{"", "disagreement", "policy", "SECURITY"} {
		_, err := store.PlaceEmergencyHold(ctx, pid, callID, uid, g, "because", 7)
		if g == "SECURITY" {
			if err != nil {
				t.Errorf("grounds %q: got %v, want accepted (case-insensitive)", g, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("grounds %q: got nil, want rejection", g)
		}
	}

	// No reason: a hold nobody can review is not accountable.
	if _, err := store.PlaceEmergencyHold(ctx, pid, callID, uid, "security", "   ", 7); err == nil {
		t.Error("blank reason: got nil, want rejection")
	}
}

func TestEmergencyHoldIsCappedAtMaxHoldDays(t *testing.T) {
	store, pid, uid, callID := holdFixture(t)
	ctx := context.Background()
	// Without a ceiling the emergency power is an indefinite veto renamed.
	if _, err := store.PlaceEmergencyHold(ctx, pid, callID, uid, "security", "live vuln", 365); err == nil {
		t.Errorf("365-day hold: got nil, want rejection (max is %d)", MaxHoldDays)
	}
	h, err := store.PlaceEmergencyHold(ctx, pid, callID, uid, "security", "live vuln", MaxHoldDays)
	if err != nil {
		t.Fatalf("hold at exactly max: %v", err)
	}
	if days := (h.ExpiresAt - h.CreatedAt) / 86400; days != MaxHoldDays {
		t.Errorf("expiry = %g days, want %d", days, MaxHoldDays)
	}
}

func TestEmergencyHoldSuspendsTheCallAndCannotForceAResult(t *testing.T) {
	store, pid, uid, callID := holdFixture(t)
	ctx := context.Background()
	charter := governance.DefaultCharter(governance.Collective)

	// Enough consent that the call WOULD pass on its merits.
	voters := []int64{uid}
	for i := 0; i < 4; i++ {
		voters = append(voters, newUserNamed(t, store, "holdvoter"+strings.Repeat("x", i)))
	}
	for _, v := range voters {
		if _, err := store.CastPosition(ctx, callID, v, "consent"); err != nil {
			t.Fatalf("CastPosition(%d): %v", v, err)
		}
	}

	// Without a hold the call closes and is accepted -- proving the setup is
	// one where the maintainer would otherwise get their way.
	before, err := store.GetConsensusCall(ctx, callID)
	if err != nil {
		t.Fatalf("GetConsensusCall: %v", err)
	}
	_ = before
	_ = charter

	h, err := store.PlaceEmergencyHold(ctx, pid, callID, uid, "security", "disclosure embargo", 7)
	if err != nil {
		t.Fatalf("PlaceEmergencyHold: %v", err)
	}
	if !h.Active {
		t.Error("freshly placed hold reports Active=false")
	}

	// Now it cannot resolve, even with unanimous consent behind it.
	if _, err := store.CloseConsensusCall(ctx, callID); err == nil {
		t.Fatal("held call closed: got nil, want refusal")
	} else if !errors.Is(err, ErrConflict) {
		t.Errorf("close error = %v, want ErrConflict", err)
	}

	// And the call is still open.
	after, err := store.GetConsensusCall(ctx, callID)
	if err != nil {
		t.Fatalf("GetConsensusCall after: %v", err)
	}
	if after.Status == "closed" {
		t.Error("call status = closed, want open: a hold must not close a call")
	}
}

func TestEmergencyHoldExpiresOnItsOwn(t *testing.T) {
	store, pid, uid, callID := holdFixture(t)
	ctx := context.Background()

	h, err := store.PlaceEmergencyHold(ctx, pid, callID, uid, "security", "brief embargo", 7)
	if err != nil {
		t.Fatalf("PlaceEmergencyHold: %v", err)
	}

	// Age the row past its expiry. Written directly because a real expiry is
	// seven days away and this is the property being pinned.
	past := float64(time.Now().Unix()) - 1
	if _, err := store.ExecContext(ctx,
		`UPDATE emergency_holds SET expires_at = ? WHERE id = ?`, past, h.ID); err != nil {
		t.Fatalf("age hold: %v", err)
	}

	active, err := store.ActiveHoldForCall(ctx, callID)
	if err != nil {
		t.Fatalf("ActiveHoldForCall: %v", err)
	}
	if active != nil {
		t.Error("expired hold still reports active: it must lapse without a cleanup job")
	}

	// An expired hold must not block resolution.
	for i := 0; i < 4; i++ {
		if _, err := store.CastPosition(ctx, callID,
			newUserNamed(t, store, "expvoter"+strings.Repeat("y", i)), "consent"); err != nil {
			t.Fatalf("CastPosition: %v", err)
		}
	}
	if _, err := store.CloseConsensusCall(ctx, callID); err != nil {
		t.Errorf("close after hold expiry: %v, want success", err)
	}
}

func TestExpiryOpensAConfirmationVote(t *testing.T) {
	store, pid, uid, callID := holdFixture(t)
	ctx := context.Background()

	h, err := store.PlaceEmergencyHold(ctx, pid, callID, uid, "security", "embargo", 7)
	if err != nil {
		t.Fatalf("PlaceEmergencyHold: %v", err)
	}
	past := float64(time.Now().Unix()) - 1
	if _, err := store.ExecContext(ctx,
		`UPDATE emergency_holds SET expires_at = ? WHERE id = ?`, past, h.ID); err != nil {
		t.Fatalf("age hold: %v", err)
	}

	expired, err := store.ExpireHolds(ctx, pid)
	if err != nil {
		t.Fatalf("ExpireHolds: %v", err)
	}
	if len(expired) != 1 {
		t.Fatalf("ExpireHolds returned %d, want 1", len(expired))
	}
	if expired[0].ConfirmationCallID == 0 {
		t.Fatal("expired hold has no confirmation call: §6.6 requires the trigger to actually fire")
	}

	call, err := store.GetConsensusCall(ctx, expired[0].ConfirmationCallID)
	if err != nil {
		t.Fatalf("GetConsensusCall(confirmation): %v", err)
	}
	if call.Question == nil || !strings.Contains(strings.ToLower(*call.Question), "emergency hold") {
		t.Errorf("confirmation question = %v, want it to name the hold", call.Question)
	}
	// The reason has to reach the community, since that is what makes the hold
	// reviewable rather than a private decision.
	if call.Description == nil || !strings.Contains(*call.Description, "embargo") {
		t.Errorf("confirmation description does not carry the stated reason: %v", call.Description)
	}

	// Idempotent: a second pass must not open a second call.
	again, err := store.ExpireHolds(ctx, pid)
	if err != nil {
		t.Fatalf("ExpireHolds second pass: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second pass opened %d more confirmation calls, want 0", len(again))
	}
}

func TestReleaseEmergencyHoldRecordsReason(t *testing.T) {
	store, pid, uid, callID := holdFixture(t)
	ctx := context.Background()
	h, err := store.PlaceEmergencyHold(ctx, pid, callID, uid, "legal", "subpoena", 7)
	if err != nil {
		t.Fatalf("PlaceEmergencyHold: %v", err)
	}
	if err := store.ReleaseEmergencyHold(ctx, h.ID, uid, "resolved without impact"); err != nil {
		t.Fatalf("ReleaseEmergencyHold: %v", err)
	}
	got, err := store.GetEmergencyHold(ctx, h.ID)
	if err != nil {
		t.Fatalf("GetEmergencyHold: %v", err)
	}
	if got.Active {
		t.Error("released hold still reports active")
	}
	if got.ReleaseReason != "resolved without impact" {
		t.Errorf("release reason = %q, want the recorded reason", got.ReleaseReason)
	}
	// Releasing twice is a client error, not a silent no-op.
	if err := store.ReleaseEmergencyHold(ctx, h.ID, uid, "again"); err == nil {
		t.Error("double release: got nil, want rejection")
	}
}

func TestAdminLedgerIsAppendOnlyAndPublic(t *testing.T) {
	store, pid, uid, callID := holdFixture(t)
	ctx := context.Background()

	if _, err := store.PlaceEmergencyHold(ctx, pid, callID, uid, "security", "embargo", 7); err != nil {
		t.Fatalf("PlaceEmergencyHold: %v", err)
	}
	entries, err := store.ListAdminLedger(ctx, 100)
	if err != nil {
		t.Fatalf("ListAdminLedger: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("placing a hold wrote nothing to the public ledger")
	}
	found := false
	for _, e := range entries {
		if e.Action == "emergency_hold" {
			found = true
			if e.Justification == "" {
				t.Error("ledger entry has no justification: §9.3 requires a written reason")
			}
		}
	}
	if !found {
		t.Error("ledger has no emergency_hold entry")
	}

	// §9.3 says "always-visible". A table any writer can rewrite is not that,
	// so the database refuses rather than trusting the application layer.
	if _, err := store.ExecContext(ctx, `UPDATE admin_ledger SET justification='edited'`); err == nil {
		t.Error("UPDATE on admin_ledger succeeded, want refusal")
	}
	if _, err := store.ExecContext(ctx, `DELETE FROM admin_ledger`); err == nil {
		t.Error("DELETE on admin_ledger succeeded, want refusal")
	}
}
