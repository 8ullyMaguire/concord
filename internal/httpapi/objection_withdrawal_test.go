package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// §6.6 gives an objection one outcome that belongs to the objector: withdrawing it.
//
// Everything else about an objection is somebody else's record to make. `resolved`,
// `overridden` and `vetoed` are findings about the proposal that was blocked, and the
// person whose proposal it was is not the judge of whether the block was fair -- the
// handler already required a maintainer for all four statuses, which is right for
// three of them and wrong for the fourth. Requiring a maintainer to WITHDRAW a block
// its holder no longer holds is the mirror of the same mistake: a blocker who changes
// their mind has to ask the person they blocked to drop it for them, and until then
// §55's "no unresolved principled objection" bar stays up against a proposal nobody
// objects to any more.
//
// So the standing depends on the OUTCOME, not only on the route: see
// objectionOutcomeNeedsMaintainer. These tests pin both halves -- the objector's own
// withdrawal needs no standing at all, and the objector cannot do anything else.

// contributorWithMembership registers `name` and makes them a contributor of the
// fixture project, returning the user id.
func (f objectionFixture) contributorWithMembership(t *testing.T, name string) int64 {
	t.Helper()
	uid := userIDNamed(t, f.db, name)
	if _, err := f.db.ExecContext(context.Background(),
		`INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)`,
		f.pid, uid, "contributor", float64(1)); err != nil {
		t.Fatalf("add contributor %s: %v", name, err)
	}
	return uid
}

// objectionBy inserts an open objection authored by `uid`.
//
// Deliberately not `anOpenObjection`, which authors every row as the harness user:
// the whole rule under test is about who the row belongs to, so a fixture that makes
// the author implicit is the one place the author must be explicit.
func (f objectionFixture) objectionBy(t *testing.T, uid int64, principle string) int64 {
	t.Helper()
	res, err := f.db.ExecContext(context.Background(),
		`INSERT INTO objections (call_id, user_id, principle, violation, remedy, status, created_at)
		 VALUES (?, ?, ?, 'the chart is rewritten nightly', 'keep it', 'open', ?)`,
		f.callID, uid, principle, float64(1))
	if err != nil {
		t.Fatalf("insert objection: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	return id
}

// resolveAs answers as `username`, so a test can be the objector, a bystander or a
// maintainer without three copies of the request.
func (f objectionFixture) resolveAs(t *testing.T, username string, objID int64, body string) (*http.Response, map[string]any) {
	t.Helper()
	return putJSON(t, f.ts,
		"/api/v1/projects/"+f.slug+"/consensus/objections/"+itoa(objID)+"/resolve",
		body, "Authorization", "Bearer "+tokenFor(t, f.db, username))
}

// openObjections counts what the call still carries, through the same tally the
// consensus page renders and CloseConsensusCall evaluates -- so the assertion is
// about the number §55's bar is computed from, not about a status string.
func (f objectionFixture) openObjections(t *testing.T) int {
	t.Helper()
	call, err := f.db.GetConsensusCall(context.Background(), f.callID)
	if err != nil {
		t.Fatalf("GetConsensusCall: %v", err)
	}
	counts, err := f.db.TallyConsensus(context.Background(), call)
	if err != nil {
		t.Fatalf("TallyConsensus: %v", err)
	}
	return counts.OpenObjections
}

func TestAnObjectorMayWithdrawTheirOwnObjection(t *testing.T) {
	f := seedObjection(t)
	uid := f.contributorWithMembership(t, "objection-author")
	objID := f.objectionBy(t, uid, "the export cannot resume")

	before := f.openObjections(t)
	resp, body := f.resolveAs(t, "objection-author", objID,
		`{"status":"withdrawn","resolution":"the export now checkpoints, so I drop this"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the objector withdrew their own objection and was answered %d (%v); "+
			"want 200 -- withdrawing your own block needs no standing in the project",
			resp.StatusCode, body)
	}
	if got, _ := body["status"].(string); got != "withdrawn" {
		t.Errorf("the response reports %q; want the status written", got)
	}
	if got := f.statusOf(t, objID); got != "withdrawn" {
		t.Errorf("status = %q; want withdrawn", got)
	}
	// The bar §55 computes is "no unresolved principled objection". A withdrawal that
	// leaves the objection standing is a status string nobody asked about.
	if after := f.openObjections(t); after != before-1 {
		t.Errorf("open objections went %d -> %d; want one fewer -- the withdrawal has "+
			"to clear the bar the call is judged against", before, after)
	}
}

// TestAnObjectorCannotResolveTheirOwnObjection is the other half of the same rule.
// Withdrawal is theirs; the verdict on the proposal is not.
func TestAnObjectorCannotResolveTheirOwnObjection(t *testing.T) {
	f := seedObjection(t)
	uid := f.contributorWithMembership(t, "objection-author")
	objID := f.objectionBy(t, uid, "the export cannot resume")

	for _, status := range []string{"resolved", "overridden", "vetoed"} {
		resp, body := f.resolveAs(t, "objection-author", objID,
			`{"status":"`+status+`","resolution":"I decide this one"}`)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("the objector tried to record %q on their own objection and got %d "+
				"(%v); want 403 -- the verdict on a block is a maintainer's to record",
				status, resp.StatusCode, body)
		}
		if got := f.statusOf(t, objID); got != "open" {
			t.Errorf("status = %q after a refused %q; want it left open", got, status)
		}
	}
}

func TestSomebodyElseCannotWithdrawAnObjection(t *testing.T) {
	f := seedObjection(t)
	uid := f.contributorWithMembership(t, "objection-author")
	f.contributorWithMembership(t, "objection-bystander")
	objID := f.objectionBy(t, uid, "the export cannot resume")

	resp, body := f.resolveAs(t, "objection-bystander", objID,
		`{"status":"withdrawn","resolution":"I speak for them"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a bystander withdrew somebody else's objection: %d (%v); want 403 -- "+
			"an objection is the objector's to drop, and a maintainer's to overrule",
			resp.StatusCode, body)
	}
	if got := f.statusOf(t, objID); got != "open" {
		t.Errorf("status = %q after a refused withdrawal; want open", got)
	}
}

// TestAnAccountWithNoMembershipMayWithdrawItsOwnObjection: creating an objection has
// never needed a role (any signed-in account may block, which is what makes §6.6's
// remedy the thing that answers it). Withdrawing it needs less -- being the account
// that raised it -- and a rule that asked for membership would leave an objection
// standing that only a maintainer could drop.
func TestAnAccountWithNoMembershipMayWithdrawItsOwnObjection(t *testing.T) {
	f := seedObjection(t)
	uid := userIDNamed(t, f.db, "objection-nonmember") // registered, no members row
	objID := f.objectionBy(t, uid, "the export cannot resume")

	resp, body := f.resolveAs(t, "objection-nonmember", objID,
		`{"status":"withdrawn","resolution":"considered and dropped"}`)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("a non-member withdrawing their own objection was answered %d (%v); "+
			"want 200", resp.StatusCode, body)
	}
	if got := f.statusOf(t, objID); got != "withdrawn" {
		t.Errorf("status = %q; want withdrawn", got)
	}
}

func TestAMaintainerMayWithdrawAnybodysObjection(t *testing.T) {
	f := seedObjection(t)
	uid := f.contributorWithMembership(t, "objection-author")
	objID := f.objectionBy(t, uid, "the export cannot resume")

	resp, body := f.resolveAs(t, "objection-maintainer", objID,
		`{"status":"withdrawn","resolution":"the objector is on leave and asked me to"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a maintainer withdrawing somebody else's objection: %d (%v); want 200",
			resp.StatusCode, body)
	}
	if got := f.statusOf(t, objID); got != "withdrawn" {
		t.Errorf("status = %q; want withdrawn", got)
	}
}

// TestTheObjectionOutcomeStandingIsTheRuleItself pins the decision table, because the
// handler's use of it reads as one condition and the table is four cases.
//
// The zero-author case is the one worth having: a row with user_id 0 (nothing writes
// one, and a future migration that did) must not match a caller whose actor id is
// also 0, or an unauthenticated request would be treated as the objection's author.
func TestTheObjectionOutcomeStandingIsTheRuleItself(t *testing.T) {
	const author = int64(7)
	cases := []struct {
		name    string
		obj     int64 // the objection's author id
		actor   int64
		status  string
		wantMin bool
	}{
		{"the objector withdraws their own", author, author, "withdrawn", false},
		{"the objector resolves their own", author, author, "resolved", true},
		{"somebody else withdraws it", author, 9, "withdrawn", true},
		{"a maintainer resolves it", author, 9, "overridden", true},
		{"an unknown author and an unknown actor", 0, 0, "withdrawn", true},
		{"an unknown author, a known actor", 0, author, "withdrawn", true},
	}
	for _, c := range cases {
		got := objectionOutcomeNeedsMaintainer(store.Objection{UserID: c.obj}, c.actor, c.status)
		if got != c.wantMin {
			t.Errorf("%s: needsMaintainer = %v; want %v", c.name, got, c.wantMin)
		}
	}
}

// TestTheRefusalNamesTheRoleItWanted: every gate in the codebase answered a bare
// "permission denied", so a page could report that a write was refused and never why.
//
// Two DIFFERENT gates are exercised, because a message that named a constant role
// would pass a test that only ever hits one of them.
func TestTheRefusalNamesTheRoleItWanted(t *testing.T) {
	f := seedObjection(t)
	f.contributorWithMembership(t, "objection-contributor")
	objID := f.objectionBy(t, userIDNamed(t, f.db, "objection-bystander"), "the export cannot resume")
	// Registered, NOT a member of the project: the contributor gate on a weight
	// proposal is the second, differently-worded refusal.
	userIDNamed(t, f.db, "objection-nonmember")

	// A contributor resolving somebody else's objection needs maintainer.
	resp, body := f.resolveAs(t, "objection-contributor", objID,
		`{"status":"overridden","resolution":"not mine to decide"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a contributor resolved an objection: %d (%v); want 403",
			resp.StatusCode, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "maintainer") {
		t.Errorf("the refusal was %q; want it to name the role it wanted (maintainer) -- "+
			"a bare \"permission denied\" for an action the interface offered is "+
			"indistinguishable from a bug to whoever hit it", msg)
	}

	// A non-member opening a weight proposal needs contributor.
	resp2, body2 := postJSON(t, f.ts,
		"/api/v1/projects/"+f.slug+"/strategy/weight-proposals",
		`{"feature_id":1,"weight":2,"rationale":"pin it"}`,
		"Authorization", "Bearer "+tokenFor(t, f.db, "objection-nonmember"))
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("a non-member opened a weight proposal: %d (%v); want 403",
			resp2.StatusCode, body2)
	}
	msg2, _ := body2["error"].(string)
	if !strings.Contains(msg2, "contributor") {
		t.Errorf("the refusal was %q; want it to name contributor", msg2)
	}
	if strings.Contains(msg2, "maintainer") {
		t.Errorf("the refusal was %q; it names the wrong role -- the message has to be "+
			"the requirement the route actually has", msg2)
	}
}
