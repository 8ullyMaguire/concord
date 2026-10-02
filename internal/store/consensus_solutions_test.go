package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/ranking"
)

// §6.5: from ranking to consensus.
//
// The three conditions are the substance here. Each exists to stop a specific
// way the agenda gets captured, and each is easy to satisfy accidentally:
//
//   - confidence: a leader must beat the runner-up AND the baseline, not either.
//     Beating every rival while losing to "do nothing" has not won anything.
//   - voters: counted as DISTINCT people, scaled to project size, so two votes
//     cannot decide a 200-person project and a 2-person project is not stuck.
//   - stability: the leader's *rating* must have been still for N hours, which
//     is what stops a call opening the instant one vote lands.
//
// The fallback chain gets the same treatment, because "nobody restarts from
// scratch" is the property that makes a ranking worth having at all.

var callFixtures int

// callFixture builds a project with a feature, two solutions, and a baseline,
// and returns the ids a §6.5 test needs.
type callFixture struct {
	d        *DB
	project  int64
	feature  int64
	leader   int64
	runnerUp int64
	baseline int64
	author   int64
	voter    int64
}

func newCallFixture(t *testing.T) callFixture {
	t.Helper()
	d, owner, pid := setupWithProject(t)
	fid := newFeature(t, d, pid, owner, "Lossless export")

	callFixtures++
	n := fmt.Sprint(callFixtures)
	author := newUserNamed(t, d, "callauthor"+n)
	if err := d.JoinProject(context.Background(), pid, author); err != nil {
		t.Fatalf("JoinProject(author): %v", err)
	}

	ctx := context.Background()
	leader, err := d.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: fid, AuthorID: author, Title: "Rewrite the exporter", Type: SolutionBuildNew,
	})
	if err != nil {
		t.Fatalf("CreateSolution(leader): %v", err)
	}
	runner, err := d.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: fid, AuthorID: author, Title: "Document the workaround", Type: SolutionWorkaround,
	})
	if err != nil {
		t.Fatalf("CreateSolution(runner-up): %v", err)
	}
	baseline, err := d.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: fid, AuthorID: author, Title: "Do nothing", Type: SolutionDoNothing,
	})
	if err != nil {
		t.Fatalf("CreateSolution(baseline): %v", err)
	}
	return callFixture{d: d, project: pid, feature: fid,
		leader: leader.ID, runnerUp: runner.ID, baseline: baseline.ID,
		author: author, voter: owner}
}

// condition returns the named condition, failing the test if it is absent.
func (c CallReadiness) condition(t *testing.T, name string) CallCondition {
	t.Helper()
	for _, cond := range c.Conditions {
		if cond.Name == name {
			return cond
		}
	}
	t.Fatalf("no condition named %q in %+v", name, c.Conditions)
	return CallCondition{}
}

// backdateLeader rewinds the leader's arena entry so the stability condition is
// (or is not) satisfied.
//
// Written as a direct UPDATE rather than by waiting: a test that sleeps 72 hours
// to prove a 72-hour threshold is not a test, and the column is updated_at on
// every game, which is exactly what solutionStableHours reads.
func (f callFixture) backdateLeader(t *testing.T, hours float64) {
	t.Helper()
	ctx := context.Background()
	arena, err := f.d.FindArena(ctx, ArenaSolution, f.project, f.feature, "")
	if err != nil {
		t.Fatalf("FindArena: %v", err)
	}
	ts := float64(time.Now().Unix()) - hours*3600
	if _, err := f.d.ExecContext(ctx, `
		UPDATE arena_entries SET updated_at = ? WHERE arena_id = ? AND entity_type = ? AND entity_id = ?`,
		ts, arena.ID, EntitySolution, f.leader); err != nil {
		t.Fatalf("backdate leader: %v", err)
	}
}

// voteFor casts one pairwise vote in the solution arena and confirms the caller
// is not a solution's author.
func (f callFixture) voteFor(t *testing.T, voter int64, winner, loser int64) {
	t.Helper()
	ctx := context.Background()
	arena, err := f.d.EnsureArena(ctx, ArenaSolution, f.project, f.feature, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	if _, err := f.d.CastArenaVote(ctx, arena.ID, voter,
		EntitySolution, winner, EntitySolution, loser, "a", "", 1.0); err != nil {
		t.Fatalf("CastArenaVote: %v", err)
	}
}

func TestAFeatureWithNoSolutionsHasNoLeader(t *testing.T) {
	d, uid, pid := setupWithProject(t)
	fid := newFeature(t, d, pid, uid, "Unworked feature")

	r, err := d.SolutionCallReadiness(context.Background(), fid)
	if err != nil {
		t.Fatalf("SolutionCallReadiness: %v", err)
	}
	if r.LeaderID != 0 {
		t.Errorf("leader = %d on a feature with no solutions", r.LeaderID)
	}
	if r.Ready {
		t.Error("ready with no solutions: an empty arena must never open a call")
	}
	// The reason has to be reported, or a project cannot tell "nobody has
	// proposed anything" from "the arena is broken".
	if len(r.Conditions) == 0 || r.Conditions[0].Met {
		t.Errorf("conditions = %+v, want one unmet condition naming the absence", r.Conditions)
	}
}

func TestNoCallOpensBeforeTheConditionsHold(t *testing.T) {
	// A fresh arena: everyone is at the prior, so nothing is decided.
	f := newCallFixture(t)
	_, err := f.d.OpenSolutionCall(context.Background(), f.feature, f.voter)
	if !errors.Is(err, ErrNotCallable) {
		t.Fatalf("OpenSolutionCall on an untouched arena: err = %v, want ErrNotCallable", err)
	}
	if !errors.Is(err, ErrNotCallable) && err != nil {
		t.Errorf("the refusal is not ErrNotCallable, so a handler cannot map it to 409: %v", err)
	}
}

func TestTheRefusalNamesTheConditionThatFailed(t *testing.T) {
	// "the conditions are not met" is indistinguishable from "this feature is
	// broken". The whole point of reporting three conditions separately is lost
	// if the error collapses them.
	f := newCallFixture(t)
	_, err := f.d.OpenSolutionCall(context.Background(), f.feature, f.voter)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	msg := err.Error()
	// Untouched arena: confidence is 50/50 between equals, so the leader cannot
	// clear 0.80 and that is the first failing condition.
	if !strings.Contains(msg, "beats runner-up and baseline") {
		t.Errorf("refusal %q does not name the failing condition", msg)
	}
}

// TestConfidenceMustHoldAgainstTheBaselineToo is the one that matters most.
//
// A solution that beats the runner-up but loses to "do nothing" has not won, so
// a call must not open on it. Building the state by hand is the only way to get
// there: an ordinary sequence of votes would take many more than the fixture
// provides, and the point is the arithmetic, not the vote count.
func TestConfidenceMustHoldAgainstTheBaselineToo(t *testing.T) {
	f := newCallFixture(t)
	ctx := context.Background()
	arena, err := f.d.FindArena(ctx, ArenaSolution, f.project, f.feature, "")
	if err != nil {
		t.Fatalf("FindArena: %v", err)
	}
	// Leader is strong, runner-up is middling, baseline is weak. The leader beats
	// the runner-up comfortably and the baseline overwhelmingly, so this
	// establishes the case where the baseline check is NOT the binding one --
	// which the next test then inverts.
	setRatings(t, f.d, arena.ID, f.leader, 1900, 40)
	setRatings(t, f.d, arena.ID, f.runnerUp, 1500, 40)
	setRatings(t, f.d, arena.ID, f.baseline, 1000, 40)

	r, err := f.d.SolutionCallReadiness(ctx, f.feature)
	if err != nil {
		t.Fatalf("SolutionCallReadiness: %v", err)
	}
	cond := r.condition(t, "beats runner-up and baseline")
	if !cond.Met {
		t.Errorf("a 1900 leader against a 1500 runner-up and 1000 baseline did not clear "+
			"confidence: actual %.3f required %.3f (%s)",
			cond.Actual, cond.Required, cond.Detail)
	}

	// Now the inversion: the leader beats the runner-up DECISIVELY but loses to
	// the baseline. §6.3's baseline exists so "keep documenting the workaround"
	// is a real option, so this must NOT open a call.
	//
	// The runner-up is pushed far down rather than nudged, because a leader at
	// 1600 against a runner-up at 1500 only reaches 0.64 -- below the threshold on
	// its own. The first version of this test set those numbers and passed for
	// the wrong reason: it was asserting that a leader loses to the runner-up,
	// and survived a mutation that deleted the baseline from the check entirely.
	setRatings(t, f.d, arena.ID, f.leader, 1900, 25)
	setRatings(t, f.d, arena.ID, f.runnerUp, 1100, 25)
	setRatings(t, f.d, arena.ID, f.baseline, 2150, 25)

	r, err = f.d.SolutionCallReadiness(ctx, f.feature)
	if err != nil {
		t.Fatalf("SolutionCallReadiness: %v", err)
	}
	// Guard the guard: confirm the runner-up really is beaten before asserting
	// that the baseline is what stops it. Otherwise this test can pass while
	// checking neither.
	if p := ranking.WinProbability(1900, 25, 1100, 25); p < 0.8 {
		t.Fatalf("the fixture does not beat the runner-up (%.3f), so it proves nothing "+
			"about the baseline", p)
	}
	if p := ranking.WinProbability(1900, 25, 2150, 25); p >= 0.8 {
		t.Fatalf("the fixture also beats the baseline (%.3f), so the condition would "+
			"pass either way", p)
	}
	cond = r.condition(t, "beats runner-up and baseline")
	if cond.Met {
		t.Errorf("a leader that loses to the baseline cleared confidence (%.3f): "+
			"§6.3's baseline is the thing a solution has to beat", cond.Actual)
	}
	if _, err := f.d.OpenSolutionCall(ctx, f.feature, f.voter); !errors.Is(err, ErrNotCallable) {
		t.Errorf("OpenSolutionCall opened on a solution losing to the baseline: %v", err)
	}
}

func TestAnArenaWithNoBaselineCannotOpenACall(t *testing.T) {
	// §6.3 says every solution arena has a permanent baseline. An arena without
	// one is malformed, and the store must refuse to manufacture a decision from
	// it rather than quietly assuming doing nothing is weak.
	f := newCallFixture(t)
	ctx := context.Background()
	arena, err := f.d.FindArena(ctx, ArenaSolution, f.project, f.feature, "")
	if err != nil {
		t.Fatalf("FindArena: %v", err)
	}
	setRatings(t, f.d, arena.ID, f.leader, 1900, 30)
	setRatings(t, f.d, arena.ID, f.runnerUp, 1200, 30)
	f.backdateLeader(t, 1000)
	if _, err := f.d.ExecContext(ctx,
		`UPDATE arena_entries SET is_baseline = 0 WHERE arena_id = ?`, arena.ID); err != nil {
		t.Fatalf("clear baseline: %v", err)
	}

	r, err := f.d.SolutionCallReadiness(ctx, f.feature)
	if err != nil {
		t.Fatalf("SolutionCallReadiness: %v", err)
	}
	if r.Ready {
		t.Errorf("an arena with no baseline reported ready: %+v", r.Conditions)
	}
	if _, err := f.d.OpenSolutionCall(ctx, f.feature, f.voter); !errors.Is(err, ErrNotCallable) {
		t.Errorf("OpenSolutionCall opened with no baseline present: %v", err)
	}
}

func TestEnoughDistinctVotersMeansDistinctPeople(t *testing.T) {
	f := newCallFixture(t)
	ctx := context.Background()
	arena, err := f.d.FindArena(ctx, ArenaSolution, f.project, f.feature, "")
	if err != nil {
		t.Fatalf("FindArena: %v", err)
	}
	setRatings(t, f.d, arena.ID, f.leader, 1900, 30)
	setRatings(t, f.d, arena.ID, f.runnerUp, 1200, 30)
	setRatings(t, f.d, arena.ID, f.baseline, 900, 30)
	f.backdateLeader(t, 1000)

	// One person voting repeatedly must not satisfy "enough distinct voters".
	for i := 0; i < 4; i++ {
		f.voteFor(t, f.voter, f.leader, f.runnerUp)
	}
	r, err := f.d.SolutionCallReadiness(ctx, f.feature)
	if err != nil {
		t.Fatalf("SolutionCallReadiness: %v", err)
	}
	cond := r.condition(t, "enough distinct voters")
	if cond.Met {
		t.Errorf("four votes from one person satisfied the distinct-voter condition "+
			"(required %v): %s", cond.Required, cond.Detail)
	}

	// A second person closes the gap. EligibleCollaboratorCount counts members
	// with contributor or above, so the voter has to join the project.
	second := newUserNamed(t, f.d, "callvoter2")
	if err := f.d.JoinProject(ctx, f.project, second); err != nil {
		t.Fatalf("JoinProject: %v", err)
	}
	f.voteFor(t, second, f.leader, f.runnerUp)
	r, err = f.d.SolutionCallReadiness(ctx, f.feature)
	if err != nil {
		t.Fatalf("SolutionCallReadiness: %v", err)
	}
	cond = r.condition(t, "enough distinct voters")
	if got := cond.Actual; got != 2 {
		t.Errorf("distinct voters = %v after two people voted, want 2 (%s)", got, cond.Detail)
	}
}

func TestTheVoterThresholdIsScaledToProjectSize(t *testing.T) {
	// §6.5 says "default scaled to project size". A charter minimum of 3 in a
	// two-person project would make the agenda permanently un-openable, which is
	// the failure mode the scaling exists to prevent.
	d, owner, pid := setupWithProject(t)
	fid := newFeature(t, d, pid, owner, "Small project feature")
	ctx := context.Background()

	solutionAuthors++
	author := newUserNamed(t, d, fmt.Sprintf("smallauthor%d", solutionAuthors))
	if err := d.JoinProject(ctx, pid, author); err != nil {
		t.Fatalf("JoinProject: %v", err)
	}
	sol, err := d.CreateSolution(ctx, CreateSolutionInput{
		FeatureID: fid, AuthorID: author, Title: "Only option", Type: SolutionBuildNew,
	})
	if err != nil {
		t.Fatalf("CreateSolution: %v", err)
	}
	eligible, err := d.EligibleCollaboratorCount(ctx, pid)
	if err != nil {
		t.Fatalf("EligibleCollaboratorCount: %v", err)
	}
	if eligible >= 3 {
		t.Skipf("project has %d eligible collaborators, so the scaling never binds", eligible)
	}

	r, err := d.SolutionCallReadiness(ctx, fid)
	if err != nil {
		t.Fatalf("SolutionCallReadiness: %v", err)
	}
	cond := r.condition(t, "enough distinct voters")
	if int(cond.Required) != eligible {
		t.Errorf("threshold = %v with %d eligible collaborators, want %d: a charter "+
			"minimum above the eligible count makes the agenda un-openable",
			cond.Required, eligible, eligible)
	}
	_ = sol
}

func TestAPositionMustBeStableBeforeACallOpens(t *testing.T) {
	f := newCallFixture(t)
	ctx := context.Background()
	arena, err := f.d.FindArena(ctx, ArenaSolution, f.project, f.feature, "")
	if err != nil {
		t.Fatalf("FindArena: %v", err)
	}
	setRatings(t, f.d, arena.ID, f.leader, 1900, 30)
	setRatings(t, f.d, arena.ID, f.runnerUp, 1200, 30)
	setRatings(t, f.d, arena.ID, f.baseline, 900, 30)
	// Two eligible people, so only stability stands in the way.
	second := newUserNamed(t, f.d, "stablevoter")
	if err := f.d.JoinProject(ctx, f.project, second); err != nil {
		t.Fatalf("JoinProject: %v", err)
	}
	third := newUserNamed(t, f.d, "stablevoter3")
	if err := f.d.JoinProject(ctx, f.project, third); err != nil {
		t.Fatalf("JoinProject: %v", err)
	}
	f.voteFor(t, f.voter, f.leader, f.runnerUp)
	f.voteFor(t, second, f.leader, f.runnerUp)
	f.voteFor(t, third, f.leader, f.runnerUp)

	// Every game rewrites updated_at, so the leader is unstable right now.
	r, err := f.d.SolutionCallReadiness(ctx, f.feature)
	if err != nil {
		t.Fatalf("SolutionCallReadiness: %v", err)
	}
	if r.Ready {
		t.Fatalf("ready immediately after voting, before any stability window: %+v", r.Conditions)
	}
	if _, err := f.d.OpenSolutionCall(ctx, f.feature, f.voter); !errors.Is(err, ErrNotCallable) {
		t.Errorf("OpenSolutionCall opened with no stability: %v", err)
	}

	// Backdate past the charter's 72 hours and it opens.
	f.backdateLeader(t, 100)
	r, err = f.d.SolutionCallReadiness(ctx, f.feature)
	if err != nil {
		t.Fatalf("SolutionCallReadiness: %v", err)
	}
	if !r.Ready {
		t.Errorf("not ready after 100 stable hours: %+v", r.Conditions)
	}
	call, err := f.d.OpenSolutionCall(ctx, f.feature, f.voter)
	if err != nil {
		t.Fatalf("OpenSolutionCall: %v", err)
	}
	if call.SolutionID == nil || *call.SolutionID != f.leader {
		t.Errorf("call is about %v, want the leading solution %d", call.SolutionID, f.leader)
	}
}

func TestACollaboratorMayOpenACallEarlyWithAReason(t *testing.T) {
	// §6.5's one discretionary path. It has to be *recorded*, or the decision
	// reads as one the arena endorsed when it did not.
	f := newCallFixture(t)
	ctx := context.Background()

	call, err := f.d.OpenSolutionCallEarly(ctx, f.feature, f.author, "the competitor left")
	if err != nil {
		t.Fatalf("OpenSolutionCallEarly: %v", err)
	}
	if call.OpenedEarly == nil || !*call.OpenedEarly {
		t.Error("the call is not flagged as early, so a reader cannot tell the conditions were waived")
	}
	if call.EarlyReason == nil || *call.EarlyReason != "the competitor left" {
		t.Errorf("early reason = %v, want the stated reason", call.EarlyReason)
	}
	if call.SolutionID == nil || *call.SolutionID != f.leader {
		t.Errorf("call is about %v, want the leading solution %d", call.SolutionID, f.leader)
	}
}

func TestAnEarlyCallNeedsAReason(t *testing.T) {
	// An early call with no reason is indistinguishable from ignoring §6.5.
	f := newCallFixture(t)
	for _, reason := range []string{"", "   ", "\t\n"} {
		_, err := f.d.OpenSolutionCallEarly(context.Background(), f.feature, f.author, reason)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("OpenSolutionCallEarly(%q): err = %v, want ErrInvalid", reason, err)
		}
	}
}

func TestAGuestMayNotOpenACallEarly(t *testing.T) {
	// §6.5 gives the discretion to collaborators. A solution's own author is
	// precisely the person with the most stake in it, and a non-member has no
	// standing at all.
	f := newCallFixture(t)
	ctx := context.Background()
	outsider := newUserNamed(t, f.d, "calloutsider")

	if _, err := f.d.OpenSolutionCallEarly(ctx, f.feature, outsider, "I think this is ready"); !errors.Is(err, ErrNotEligibleCollaborator) {
		t.Errorf("a non-member opened an early call: %v", err)
	}
}

func TestAnEarlyCallStillNeedsASolutionToCall(t *testing.T) {
	// The discretion is over §6.5's *conditions*, not over the absence of a
	// subject. A call with nothing to decide is not an early call.
	d, owner, pid := setupWithProject(t)
	fid := newFeature(t, d, pid, owner, "No solutions at all")
	second := newUserNamed(t, d, "earlynosol")
	if err := d.JoinProject(context.Background(), pid, second); err != nil {
		t.Fatalf("JoinProject: %v", err)
	}
	if _, err := d.OpenSolutionCallEarly(context.Background(), fid, second, "genuinely early"); !errors.Is(err, ErrNotCallable) {
		t.Errorf("OpenSolutionCallEarly with no solutions: %v, want ErrNotCallable", err)
	}
}

func TestAcceptingASolutionMovesItToReadyAndWritesTheRecord(t *testing.T) {
	f := newCallFixture(t)
	ctx := context.Background()
	call := openCallableCall(t, f)

	if _, err := f.d.RecordCallOutcome(ctx, call.ID, OutcomeAccepted,
		"adopted: cheaper than the rewrite", 0, nil); err != nil {
		t.Fatalf("RecordCallOutcome: %v", err)
	}

	after, err := f.d.GetConsensusCall(ctx, call.ID)
	if err != nil {
		t.Fatalf("GetConsensusCall: %v", err)
	}
	if after.Outcome == nil || *after.Outcome != OutcomeAccepted {
		t.Errorf("outcome = %v, want accepted", after.Outcome)
	}
	// outcome and result are different vocabularies: one is what the call decided
	// about the solution, the other §6.6's verdict on the call. Collapsing them
	// would make the fallback chain unrepresentable.
	if after.Result != nil && *after.Result == "accepted" {
		t.Error("outcome and result both say accepted; §6.5's outcome must stay distinct")
	}

	sol, err := f.d.GetSolution(ctx, f.leader)
	if err != nil {
		t.Fatalf("GetSolution: %v", err)
	}
	if sol.Status != "ready" {
		t.Errorf("accepted solution status = %q, want ready", sol.Status)
	}

	doc, err := f.d.GetDocumentBySlug(ctx, f.project, "adr", fmt.Sprintf("call-%d", call.ID))
	if err != nil {
		t.Fatalf("the decision record was not written: %v", err)
	}
	for _, want := range []string{OutcomeAccepted, "adopted: cheaper than the rewrite"} {
		if !strings.Contains(doc.Body, want) {
			t.Errorf("the decision record does not mention %q", want)
		}
	}
}

func TestTheDecisionRecordCarriesPositionsAndObjections(t *testing.T) {
	// §6.5: "the call's result, positions, objections, and remedies are
	// auto-written into a decision record". A record with only the outcome is
	// not the record the spec describes.
	f := newCallFixture(t)
	ctx := context.Background()
	call := openCallableCall(t, f)

	if _, err := f.d.CastPosition(ctx, call.ID, f.voter, "consent"); err != nil {
		t.Fatalf("CastPosition: %v", err)
	}
	if _, err := f.d.CreateObjection(ctx, call.ID, f.author,
		"data loss", "the rewrite rewrites the whole table",
		"rewrite only the export path"); err != nil {
		t.Fatalf("CreateObjection: %v", err)
	}
	if _, err := f.d.RecordCallOutcome(ctx, call.ID, OutcomeAccepted, "", 0, nil); err != nil {
		t.Fatalf("RecordCallOutcome: %v", err)
	}

	doc, err := f.d.GetDocumentBySlug(ctx, f.project, "adr", fmt.Sprintf("call-%d", call.ID))
	if err != nil {
		t.Fatalf("GetDocumentBySlug: %v", err)
	}
	for _, want := range []string{"consent", "data loss", "rewrite only the export path"} {
		if !strings.Contains(doc.Body, want) {
			t.Errorf("the decision record omits %q; §6.5 wants positions, objections and remedies", want)
		}
	}
}

func TestAcceptingWithAmendmentsSpawnsADerivedSolution(t *testing.T) {
	// §6.5's outcome "accepted with amendments (spawns a derived solution)".
	f := newCallFixture(t)
	ctx := context.Background()
	call := openCallableCall(t, f)

	if _, err := f.d.RecordCallOutcome(ctx, call.ID, OutcomeAmended, "", 0, &SolutionAmendment{
		AuthorID: f.author, Title: "Rewrite the exporter, incrementally",
		Body: "only the streaming path first", Type: SolutionBuildNew,
	}); err != nil {
		t.Fatalf("RecordCallOutcome(amended): %v", err)
	}

	// Found by what makes it the fork, not by guessing an id: the amendment is
	// the only solution with a parent and the only one titled as amended.
	scores, err := f.d.ListSolutions(ctx, f.feature, 50)
	if err != nil {
		t.Fatalf("ListSolutions: %v", err)
	}
	var derived *SolutionScore
	for i := range scores {
		sol, serr := f.d.GetSolution(ctx, scores[i].SolutionID)
		if serr != nil {
			t.Fatalf("GetSolution: %v", serr)
		}
		if sol.Title == "Rewrite the exporter, incrementally" {
			derived = &scores[i]
			if sol.ParentSolutionID.Int64 != f.leader {
				t.Errorf("the derived solution's parent is %v, want %d",
					sol.ParentSolutionID.Int64, f.leader)
			}
			if sol.Status != "ready" {
				t.Errorf("derived solution status = %q, want ready", sol.Status)
			}
		}
	}
	if derived == nil {
		t.Fatalf("no amended solution exists; the board has %d entries", len(scores))
	}
}

func TestAmendmentsNeedTheirText(t *testing.T) {
	// Otherwise "accepted with amendments" records an outcome whose artifact is
	// missing, and the ADR would claim an amendment nobody can read.
	f := newCallFixture(t)
	call := openCallableCall(t, f)
	if _, err := f.d.RecordCallOutcome(context.Background(), call.ID,
		OutcomeAmended, "", 0, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("RecordCallOutcome(amended, no amendment): %v, want ErrInvalid", err)
	}
}

func TestFallingBackMovesToTheNextSolution(t *testing.T) {
	// §6.5's "fall back to #2". The ranking IS the chain, so the runner-up does
	// not need a second vote -- and the displaced solution is rejected, not left
	// dangling.
	f := newCallFixture(t)
	ctx := context.Background()
	call := openCallableCall(t, f)

	if _, err := f.d.RecordCallOutcome(ctx, call.ID, OutcomeFallBack,
		"the rewrite slips a quarter", f.runnerUp, nil); err != nil {
		t.Fatalf("RecordCallOutcome: %v", err)
	}
	displaced, err := f.d.GetSolution(ctx, f.leader)
	if err != nil {
		t.Fatalf("GetSolution: %v", err)
	}
	if displaced.Status != "rejected" {
		t.Errorf("displaced solution status = %q, want rejected", displaced.Status)
	}
	fallback, err := f.d.GetSolution(ctx, f.runnerUp)
	if err != nil {
		t.Fatalf("GetSolution: %v", err)
	}
	if fallback.Status != "ready" {
		t.Errorf("fallback solution status = %q, want ready", fallback.Status)
	}
}

func TestFallingBackMustNameADifferentSolution(t *testing.T) {
	// Falling back to the proposal on the table is a no-op that would claim
	// progress while leaving the board where it started.
	f := newCallFixture(t)
	ctx := context.Background()
	call := openCallableCall(t, f)

	if _, err := f.d.RecordCallOutcome(ctx, call.ID, OutcomeFallBack, "", 0, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("fall-back with no target: %v, want ErrInvalid", err)
	}
	if _, err := f.d.RecordCallOutcome(ctx, call.ID, OutcomeFallBack, "", f.leader, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("fall-back to itself: %v, want ErrInvalid", err)
	}
}

func TestRejectingASolutionLeavesTheBaselineTheAnswer(t *testing.T) {
	// §6.5's "rejected (baseline wins)".
	f := newCallFixture(t)
	ctx := context.Background()
	call := openCallableCall(t, f)

	if _, err := f.d.RecordCallOutcome(ctx, call.ID, OutcomeRejected, "the cost is not justified", 0, nil); err != nil {
		t.Fatalf("RecordCallOutcome: %v", err)
	}
	sol, err := f.d.GetSolution(ctx, f.leader)
	if err != nil {
		t.Fatalf("GetSolution: %v", err)
	}
	if sol.Status != "rejected" {
		t.Errorf("rejected solution status = %q, want rejected", sol.Status)
	}
	// The baseline must survive the rejection: doing nothing is the answer, so
	// removing the competitor that lost to it would be wrong.
	base, err := f.d.GetSolution(ctx, f.baseline)
	if err != nil {
		t.Fatalf("GetSolution(baseline): %v", err)
	}
	if !base.IsBaseline() {
		t.Error("the baseline was disturbed by rejecting the leader: §6.3 says it is permanent")
	}
}

func TestSentBackIsNeitherAcceptedNorRejected(t *testing.T) {
	// "sent back" means the call did not decide. Marking it rejected would
	// record a decision nobody made.
	f := newCallFixture(t)
	ctx := context.Background()
	call := openCallableCall(t, f)

	if _, err := f.d.RecordCallOutcome(ctx, call.ID, OutcomeSentBack, "needs more data", 0, nil); err != nil {
		t.Fatalf("RecordCallOutcome: %v", err)
	}
	sol, err := f.d.GetSolution(ctx, f.leader)
	if err != nil {
		t.Fatalf("GetSolution: %v", err)
	}
	if sol.Status != "discussion" {
		t.Errorf("sent-back solution status = %q, want discussion", sol.Status)
	}
}

func TestAnOutcomeCannotBeRecordedTwice(t *testing.T) {
	f := newCallFixture(t)
	ctx := context.Background()
	call := openCallableCall(t, f)

	if _, err := f.d.RecordCallOutcome(ctx, call.ID, OutcomeAccepted, "", 0, nil); err != nil {
		t.Fatalf("RecordCallOutcome: %v", err)
	}
	if _, err := f.d.RecordCallOutcome(ctx, call.ID, OutcomeRejected, "", 0, nil); !errors.Is(err, ErrConflict) {
		t.Errorf("second outcome on a closed call: %v, want ErrConflict", err)
	}
}

func TestOutcomesDoNotApplyToACallWithNoSolution(t *testing.T) {
	// §6.6's process decisions (an emergency-hold confirmation, a charter
	// amendment) have no solution, so §6.5's vocabulary does not describe them.
	d, owner, pid := setupWithProject(t)
	ctx := context.Background()
	call, err := d.CreateConsensusCall(ctx, pid, 0, owner, "amend the charter", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	if _, err := d.RecordCallOutcome(ctx, call.ID, OutcomeAccepted, "", 0, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("§6.5's outcome on a process decision: %v, want ErrInvalid", err)
	}
}

func TestTheFallbackChainExcludesTheProposalOnTheTable(t *testing.T) {
	// "The ranking is the fallback chain." A chain that starts with the thing
	// being decided makes "fall back" a no-op the store has to special-case.
	f := newCallFixture(t)
	ctx := context.Background()
	call := openCallableCall(t, f)

	chain, err := f.d.CallFallback(ctx, call.ID)
	if err != nil {
		t.Fatalf("CallFallback: %v", err)
	}
	if len(chain) == 0 {
		t.Fatal("the chain is empty, so a stalled call has nowhere to go")
	}
	for _, s := range chain {
		if s.SolutionID == f.leader {
			t.Error("the chain contains the solution under discussion")
		}
	}
	// Strongest first, baseline last: a chain that ends in "do nothing" is the
	// honest worst case.
	if chain[0].IsBaseline {
		t.Error("the chain leads with the baseline: doing nothing is the last resort, not the first")
	}
	last := chain[len(chain)-1]
	if !last.IsBaseline {
		t.Errorf("the chain does not end with the baseline (last is %+v)", last)
	}
}

func TestTheCharterCanTightenOrLoosenTheConditions(t *testing.T) {
	// All three of §6.5's conditions are charter-configurable because all three
	// encode a judgement about how this project moves.
	f := newCallFixture(t)
	ctx := context.Background()

	charter, err := f.d.GetCharterForProject(ctx, f.project)
	if err != nil {
		t.Fatalf("GetCharterForProject: %v", err)
	}
	if charter.SolutionCallConfidence != 0.80 {
		t.Errorf("default confidence = %v, want the spec's 0.80", charter.SolutionCallConfidence)
	}
	if charter.SolutionStableHours != 72 {
		t.Errorf("default stable hours = %v, want the spec's 72", charter.SolutionStableHours)
	}

	charter.SolutionCallConfidence = 0.99
	charter.SolutionStableHours = 1
	charter.SolutionCallMinVoters = 1
	if err := f.d.UpdateCharter(ctx, f.project, charter); err != nil {
		t.Fatalf("UpdateCharter: %v", err)
	}
	round, err := f.d.SolutionCallReadiness(ctx, f.feature)
	if err != nil {
		t.Fatalf("SolutionCallReadiness: %v", err)
	}
	cond := round.condition(t, "beats runner-up and baseline")
	if cond.Required != 0.99 {
		t.Errorf("required confidence = %v after amending the charter, want 0.99", cond.Required)
	}
}

func TestAnUnreachableCharterIsRefused(t *testing.T) {
	// A confidence of 1.0 or a zero-hour window can never be satisfied, which
	// would silently disable §6.5 for the whole project with nothing to say why.
	f := newCallFixture(t)
	ctx := context.Background()
	charter, err := f.d.GetCharterForProject(ctx, f.project)
	if err != nil {
		t.Fatalf("GetCharterForProject: %v", err)
	}

	bad := charter
	bad.SolutionCallConfidence = 1.0
	if err := f.d.UpdateCharter(ctx, f.project, bad); !errors.Is(err, ErrInvalid) {
		t.Errorf("confidence 1.0 accepted: %v", err)
	}
	bad = charter
	bad.SolutionStableHours = 0
	if err := f.d.UpdateCharter(ctx, f.project, bad); !errors.Is(err, ErrInvalid) {
		t.Errorf("zero stable window accepted: %v", err)
	}
	bad = charter
	bad.SolutionCallMinVoters = 0
	if err := f.d.UpdateCharter(ctx, f.project, bad); !errors.Is(err, ErrInvalid) {
		t.Errorf("zero voter minimum accepted: %v", err)
	}
}

// openCallableCall satisfies all three conditions and opens a call, for the
// outcome tests that are about the outcome rather than the gate.
func openCallableCall(t *testing.T, f callFixture) ConsensusCall {
	t.Helper()
	ctx := context.Background()
	arena, err := f.d.FindArena(ctx, ArenaSolution, f.project, f.feature, "")
	if err != nil {
		t.Fatalf("FindArena: %v", err)
	}
	setRatings(t, f.d, arena.ID, f.leader, 1900, 30)
	setRatings(t, f.d, arena.ID, f.runnerUp, 1200, 30)
	setRatings(t, f.d, arena.ID, f.baseline, 900, 30)
	second := newUserNamed(t, f.d, "openvoter"+fmt.Sprint(callFixtures))
	if err := f.d.JoinProject(ctx, f.project, second); err != nil {
		t.Fatalf("JoinProject: %v", err)
	}
	f.voteFor(t, f.voter, f.leader, f.runnerUp)
	f.voteFor(t, second, f.leader, f.runnerUp)
	third := newUserNamed(t, f.d, "openvoter3"+fmt.Sprint(callFixtures))
	if err := f.d.JoinProject(ctx, f.project, third); err != nil {
		t.Fatalf("JoinProject: %v", err)
	}
	f.voteFor(t, third, f.leader, f.runnerUp)
	f.backdateLeader(t, 200)

	call, err := f.d.OpenSolutionCall(ctx, f.feature, f.voter)
	if err != nil {
		t.Fatalf("OpenSolutionCall: %v", err)
	}
	return call
}

// setRatings writes an arena entry's Glicko triple directly.
//
// Used to construct states that would take many votes to reach. The tests below
// are about the arithmetic §6.5 applies to a ranking, not about whether the
// ranking engine can produce that ranking from votes.
func setRatings(t *testing.T, d *DB, arenaID, entityID int64, r, rd float64) {
	t.Helper()
	res, err := d.ExecContext(context.Background(), `
		UPDATE arena_entries SET r = ?, rd = ?, sigma = 0.06, updated_at = ?
		WHERE arena_id = ? AND entity_type = ? AND entity_id = ?`,
		r, rd, float64(time.Now().Unix()), arenaID, EntitySolution, entityID)
	if err != nil {
		t.Fatalf("set ratings for %d: %v", entityID, err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("set ratings touched %d rows for solution %d, want 1", n, entityID)
	}
}
