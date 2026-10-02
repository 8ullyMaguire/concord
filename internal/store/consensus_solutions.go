package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/ranking"
)

// §6.5: from ranking to consensus.
//
// "Ranking does not decide. It sets the agenda."
//
// Everything in this file exists to keep those two sentences true of each other.
// A Glicko score is evidence, and §6.5 is careful about what it takes to turn
// evidence into an agenda item: three conditions, all of which exist to stop a
// single loud voter from manufacturing a decision. The fallback chain is the
// other half -- a call that stalls or is blocked must not send anyone back to
// the whiteboard, because "nobody restarts from scratch" is the property that
// makes a ranking worth having.

// ErrNotCallable is returned when §6.5's opening conditions do not hold.
//
// Distinct from ErrInvalid because the caller's request is perfectly reasonable
// -- the leader genuinely is not ready -- and "unavailable" is a different answer
// from "malformed". A handler that maps both to 400 tells a collaborator that
// opening a call is something they did wrong.
var ErrNotCallable = errors.New("§6.5 conditions for opening a call are not met")

// ErrNotEligibleCollaborator is returned when somebody without §8.2 standing
// tries to open a call early (§6.5's one discretionary path).
var ErrNotEligibleCollaborator = errors.New("opening a call early requires an eligible collaborator")

// §6.5's five outcomes. Distinct from governance.Result, which is the verdict on
// the *call*: these say what the call decided about the *solution*.
const (
	OutcomeAccepted = "accepted"
	OutcomeAmended  = "accepted-with-amendments"
	OutcomeFallBack = "fall-back"
	OutcomeRejected = "rejected"
	OutcomeSentBack = "sent-back"
)

// CallCondition is one of §6.5's three conditions, with whether it currently
// holds and what the actual value is.
//
// Reported rather than just aggregated because "the call cannot open" with no
// reason is the same failure mode §6.5's conditions were written to prevent: an
// agenda that is silently stuck looks identical to a feature nobody wants.
type CallCondition struct {
	// Name is the spec's phrase: "beats runner-up and baseline", "enough
	// distinct voters", "stable position".
	Name string `json:"name"`
	// Met is whether this condition currently holds.
	Met bool `json:"met"`
	// Actual and Required make the failure legible in one line.
	Actual   float64 `json:"actual"`
	Required float64 `json:"required"`
	// Detail is a human-readable sentence naming the numbers.
	Detail string `json:"detail"`
}

// CallReadiness is §6.5's evaluation of a feature's solution arena.
type CallReadiness struct {
	FeatureID int64 `json:"feature_id"`
	// Leader is the solution a call would open on. 0 when the arena has no
	// non-baseline competitor yet.
	LeaderID int64 `json:"leader_id"`
	// RunnerUpID is the next-ranked solution, the first fallback.
	RunnerUpID int64 `json:"runner_up_id"`
	// BaselineID is the do-nothing competitor.
	BaselineID int64 `json:"baseline_id"`
	// Ready is the conjunction of ReadyConditions, i.e. §6.5's first condition.
	Ready bool `json:"ready"`
	// Conditions, in spec order, so a caller can show why.
	Conditions []CallCondition `json:"conditions"`
	// LeaderTitle and LeaderScore are for a one-line agenda item.
	LeaderTitle string  `json:"leader_title"`
	LeaderScore float64 `json:"leader_score"`
	// MinVoters is the effective threshold after scaling, which is the number a
	// charter amendment would change.
	MinVoters int `json:"min_voters"`
}

// StableHours is how long the leader has held the top of the arena.
//
// §6.5's third condition. Derived from the arena entry's updated_at, which is
// written by every game, so it is the last time the leader's *rating* moved
// rather than the last time it was first filed. A leader that has not been voted
// on since being created reports its creation time, which means it is not yet
// stable -- correct, since nobody has tested it.
func (d *DB) solutionStableHours(ctx context.Context, arenaID, leaderID int64, now float64) (float64, error) {
	var updated float64
	err := d.QueryRowContext(ctx, `
		SELECT updated_at FROM arena_entries
		WHERE arena_id = ? AND entity_type = ? AND entity_id = ?`,
		arenaID, EntitySolution, leaderID).Scan(&updated)
	if err != nil {
		return 0, fmt.Errorf("leader stability: %w", err)
	}
	hours := (now - updated) / 3600.0
	if hours < 0 {
		return 0, nil
	}
	return hours, nil
}

// SolutionCallReadiness evaluates §6.5's three conditions for a feature.
//
// This is a pure evaluation: it opens nothing and creates nothing. It is
// separately callable because the UI needs to answer "why has no call opened on
// this yet?" and the only honest answer is the three conditions, measured.
func (d *DB) SolutionCallReadiness(ctx context.Context, featureID int64) (CallReadiness, error) {
	out := CallReadiness{FeatureID: featureID}
	feature, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return out, err
	}
	arena, err := d.FindArena(ctx, ArenaSolution, feature.ProjectID, featureID, "")
	if err != nil {
		if errors.Is(err, ErrArenaNotFound) {
			// No solutions filed yet, so no conditions are met and there is
			// nothing to explain beyond that.
			out.Conditions = []CallCondition{{
				Name:   "a solution to call",
				Met:    false,
				Detail: "no solution has been filed for this feature",
			}}
			return out, nil
		}
		return out, err
	}

	entries, err := d.ArenaLeaderboard(ctx, arena.ID, 200)
	if err != nil {
		return out, err
	}
	var leader, runnerUp, baseline *ArenaEntry
	for i := range entries {
		e := &entries[i]
		switch {
		case e.IsBaseline && baseline == nil:
			baseline = e
		case leader == nil:
			leader = e
		case runnerUp == nil:
			runnerUp = e
		}
	}
	if leader == nil {
		out.Conditions = []CallCondition{{
			Name:   "a solution to call",
			Met:    false,
			Detail: "the arena holds only a baseline, so there is nothing to call",
		}}
		if baseline != nil {
			out.BaselineID = baseline.EntityID
		}
		return out, nil
	}
	out.LeaderID = leader.EntityID
	if runnerUp != nil {
		out.RunnerUpID = runnerUp.EntityID
	}
	if baseline != nil {
		out.BaselineID = baseline.EntityID
	}
	sol, err := d.GetSolution(ctx, leader.EntityID)
	if err != nil {
		return out, err
	}
	out.LeaderTitle = sol.Title

	charter, err := d.GetCharterForProject(ctx, feature.ProjectID)
	if err != nil {
		return out, err
	}

	// Condition 1: beats the runner-up AND the baseline with >= confidence.
	//
	// Both, not either. A solution that beats every rival but loses to doing
	// nothing has not won -- §6.3's baseline exists precisely so that "keep
	// documenting the workaround" is a real option, and a call that opened on a
	// solution losing to it would be asking the room to abandon the alternative
	// the arena says is better.
	worst := ranking.WinProbability(leader.R, leader.RD, baselineOrSelf(baseline, leader).R, baselineOrSelf(baseline, leader).RD)
	if runnerUp != nil {
		if p := ranking.WinProbability(leader.R, leader.RD, runnerUp.R, runnerUp.RD); p < worst {
			worst = p
		}
	}
	conf := charter.SolutionCallConfidence
	out.Conditions = append(out.Conditions, CallCondition{
		Name:     "beats runner-up and baseline",
		Met:      worst >= conf,
		Actual:   worst,
		Required: conf,
		Detail: fmt.Sprintf("%.0f%% confidence against the strongest alternative, %0.0f%% required",
			worst*100, conf*100),
	})

	// Condition 2: enough distinct voters, scaled to project size.
	//
	// min(charter minimum, eligible collaborators) so a two-person project is
	// not permanently un-callable, and the charter minimum so a large project
	// cannot decide on two votes.
	eligible, err := d.EligibleCollaboratorCount(ctx, feature.ProjectID)
	if err != nil {
		return out, err
	}
	minVoters := charter.SolutionCallMinVoters
	if eligible > 0 && minVoters > eligible {
		minVoters = eligible
	}
	voters, err := d.distinctSolutionVoters(ctx, arena.ID, leader.EntityID)
	if err != nil {
		return out, err
	}
	out.MinVoters = minVoters
	out.Conditions = append(out.Conditions, CallCondition{
		Name:     "enough distinct voters",
		Met:      voters >= minVoters,
		Actual:   float64(voters),
		Required: float64(minVoters),
		Detail: fmt.Sprintf("%d of %d distinct voters (%d eligible in this project)",
			voters, minVoters, eligible),
	})

	// Condition 3: stable for N hours.
	now := float64(time.Now().Unix())
	stable, err := d.solutionStableHours(ctx, arena.ID, leader.EntityID, now)
	if err != nil {
		return out, err
	}
	want := charter.SolutionStableHours
	out.Conditions = append(out.Conditions, CallCondition{
		Name:     "stable position",
		Met:      stable >= want,
		Actual:   stable,
		Required: want,
		Detail:   fmt.Sprintf("leading for %.0f hours, %0.0f required", stable, want),
	})

	out.Ready = true
	for _, c := range out.Conditions {
		if !c.Met {
			out.Ready = false
			break
		}
	}
	return out, nil
}

// baselineOrSelf returns the baseline entry, or the leader itself when the arena
// has none.
//
// The fallback is deliberate and not a convenience: with no baseline the leader
// is compared against itself, giving probability 0.5, which never reaches a 0.8
// threshold and so never opens a call on an arena that has no do-nothing
// competitor. That is the safe direction -- §6.3 says every arena has one, so an
// arena without one is malformed, and refusing to manufacture a decision from a
// malformed arena is better than quietly assuming the baseline is weak.
func baselineOrSelf(baseline, leader *ArenaEntry) *ArenaEntry {
	if baseline != nil {
		return baseline
	}
	return leader
}

// OpenSolutionCall opens a consensus call on a feature's leading solution (§6.5).
//
// It refuses unless §6.5's three conditions hold, and says which one failed.
// The refusal names the condition because the alternative -- opening the call and
// letting it fail later -- spends a consensus window on a decision the arena
// already says is premature.
func (d *DB) OpenSolutionCall(ctx context.Context, featureID, openedBy int64) (ConsensusCall, error) {
	readiness, err := d.SolutionCallReadiness(ctx, featureID)
	if err != nil {
		return ConsensusCall{}, err
	}
	if readiness.LeaderID == 0 {
		return ConsensusCall{}, fmt.Errorf("%w: no solution leads this feature's arena", ErrNotCallable)
	}
	if !readiness.Ready {
		return ConsensusCall{}, fmt.Errorf("%w: %s", ErrNotCallable, unmetCondition(readiness))
	}
	return d.createSolutionCall(ctx, featureID, readiness.LeaderID, openedBy, false, nil, nil)
}

// OpenSolutionCallEarly is §6.5's discretionary path: "A collaborator may open
// a call earlier with a stated reason. Early calls are flagged as such."
//
// Flagged, not hidden. The record has to say the conditions were not met, or the
// decision reads as one the arena endorsed when it did not.
func (d *DB) OpenSolutionCallEarly(ctx context.Context, featureID, openedBy int64, reason string) (ConsensusCall, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ConsensusCall{}, fmt.Errorf(
			"%w: §6.5 requires a stated reason for opening a call before its conditions are met",
			ErrInvalid)
	}
	readiness, err := d.SolutionCallReadiness(ctx, featureID)
	if err != nil {
		return ConsensusCall{}, err
	}
	if readiness.LeaderID == 0 {
		return ConsensusCall{}, fmt.Errorf("%w: no solution leads this feature's arena", ErrNotCallable)
	}
	feature, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return ConsensusCall{}, err
	}
	role, err := d.GetRoleForProject(ctx, feature.ProjectID, openedBy)
	if err != nil {
		return ConsensusCall{}, err
	}
	if !eligibleCollaboratorRole(role) {
		return ConsensusCall{}, fmt.Errorf(
			"%w: role %q", ErrNotEligibleCollaborator, role)
	}
	// Recorded even when the conditions happen to hold, because the caller said
	// they were opening early and the record should say what they believed.
	why := reason
	return d.createSolutionCall(ctx, featureID, readiness.LeaderID, openedBy, true, &why, &readiness)
}

// unmetCondition renders the first failing condition for an error message.
func unmetCondition(r CallReadiness) string {
	for _, c := range r.Conditions {
		if !c.Met {
			return fmt.Sprintf("%s (%s)", c.Name, c.Detail)
		}
	}
	return "unknown"
}

// eligibleCollaboratorRole is §8.2's collaborator bar: contributor or above.
//
// A guest cannot open an early call even if they are the solution's author,
// because §6.5 gives the discretion to collaborators and the author of a
// solution is precisely the person with the most stake in it.
func eligibleCollaboratorRole(role string) bool {
	switch role {
	case "contributor", "reviewer", "maintainer", "owner":
		return true
	}
	return false
}

// createSolutionCall is the shared insert for both §6.5 paths.
func (d *DB) createSolutionCall(ctx context.Context, featureID, solutionID, openedBy int64,
	early bool, reason *string, readiness *CallReadiness) (ConsensusCall, error) {

	feature, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return ConsensusCall{}, err
	}
	// Reuses CreateConsensusCall's validation (feature must be draft or
	// discussion, must trace to a complaint) rather than duplicating it, so a
	// §6.6 rule cannot be enforced on one path and forgotten on the other.
	call, err := d.CreateConsensusCall(ctx, feature.ProjectID, featureID, openedBy,
		callTitle(readiness, feature), callQuestion(feature))
	if err != nil {
		return ConsensusCall{}, err
	}
	var earlyVal int
	if early {
		earlyVal = 1
	}
	if _, err := d.ExecContext(ctx, `
		UPDATE consensus_calls
		SET solution_id = ?, opened_early = ?, early_reason = ?
		WHERE id = ?`, solutionID, earlyVal, reason, call.ID); err != nil {
		return ConsensusCall{}, fmt.Errorf("attach solution to call: %w", err)
	}
	// The solution enters the decision phase (§6.6's Solutioning -> Consent), so
	// its own status has to move with it or the board shows it still in solutioning.
	if _, err := d.ExecContext(ctx,
		`UPDATE solutions SET status = 'consensus', updated_at = ? WHERE id = ?`,
		float64(time.Now().Unix()), solutionID); err != nil {
		return ConsensusCall{}, fmt.Errorf("mark solution in consensus: %w", err)
	}
	return d.GetConsensusCall(ctx, call.ID)
}

// callTitle names the call after the solution it is about, which is what §6.5
// means by "a call opens on the leading solution".
func callTitle(readiness *CallReadiness, feature Feature) string {
	if readiness != nil && readiness.LeaderTitle != "" {
		return fmt.Sprintf("%s: %s", feature.Title, readiness.LeaderTitle)
	}
	return feature.Title
}

// callQuestion is the decision actually on the table, which 0014 made a column
// for precisely because "the title" is not an answer to "what are we deciding".
func callQuestion(feature Feature) string {
	return fmt.Sprintf("Adopt this solution for: %s?", feature.Title)
}

// RecordCallOutcome closes a call with one of §6.5's five outcomes and writes the
// decision record the spec requires.
//
// The ADR is a project document of kind 'adr' rather than a new table: §6.5 asks
// for "the call's result, positions, objections, and remedies auto-written into a
// decision record attached to the feature", and project_documents already carries
// kind='adr' with UNIQUE (project_id, kind, slug). A second ADR store would be
// the second representation of one fact -- the duplication that
// arenas.baseline_entry_id used to be.
func (d *DB) RecordCallOutcome(ctx context.Context, callID int64, outcome, summary string,
	fallbackTo int64, amendments *SolutionAmendment) (ConsensusCall, error) {

	call, err := d.GetConsensusCall(ctx, callID)
	if err != nil {
		return ConsensusCall{}, err
	}
	if call.Status != "open" {
		return ConsensusCall{}, fmt.Errorf("%w: call %d is already %s", ErrConflict, callID, call.Status)
	}
	if call.SolutionID == nil {
		return ConsensusCall{}, fmt.Errorf(
			"%w: call %d is not about a solution, so §6.5's outcomes do not apply", ErrInvalid, callID)
	}
	if call.FeatureID == 0 {
		return ConsensusCall{}, fmt.Errorf(
			"%w: call %d has no feature, so there is no board to move", ErrInvalid, callID)
	}

	// "accepted with amendments" spawns a derived solution (§6.3's fork). Done
	// before the outcome is written, so a failure here leaves the call open
	// rather than recording an outcome whose artifact is missing.
	var amendedSolution int64
	if outcome == OutcomeAmended {
		if amendments == nil {
			return ConsensusCall{}, fmt.Errorf(
				"%w: an accepted-with-amendments outcome needs the amendment text", ErrInvalid)
		}
		derived, err := d.CreateSolution(ctx, CreateSolutionInput{
			FeatureID:        callFeatureID(call),
			AuthorID:         amendments.AuthorID,
			Title:            amendments.Title,
			Body:             amendments.Body,
			Type:             amendments.Type,
			ParentSolutionID: *call.SolutionID,
		})
		if err != nil {
			return ConsensusCall{}, fmt.Errorf("spawn amended solution: %w", err)
		}
		amendedSolution = derived.ID
	}
	if outcome == OutcomeFallBack && fallbackTo == 0 {
		return ConsensusCall{}, fmt.Errorf(
			"%w: fall-back must name the solution that takes over", ErrInvalid)
	}
	if outcome == OutcomeFallBack && fallbackTo == *call.SolutionID {
		// Falling back to the solution the call was about is a no-op that would
		// leave the board where it started while claiming progress.
		return ConsensusCall{}, fmt.Errorf(
			"%w: fall-back must name a different solution", ErrInvalid)
	}

	now := float64(time.Now().Unix())
	var fb any
	if fallbackTo != 0 {
		fb = fallbackTo
	}
	// status='closed' as well as closed_at. Recording an outcome while leaving
	// the call open lets a second outcome overwrite the first: the conflict guard
	// above checks status, so without this the "already closed" refusal never
	// fires and a call can be decided twice.
	if _, err := d.ExecContext(ctx, `
		UPDATE consensus_calls
		SET status = 'closed', outcome = ?, fallback_to_solution_id = ?,
		    summary = ?, closed_at = ?
		WHERE id = ?`, outcome, fb, nullIfEmpty(summary), now, callID); err != nil {
		return ConsensusCall{}, fmt.Errorf("record outcome: %w", err)
	}

	// The losing solution is rejected, the adopted one is ready, and the
	// fallback becomes ready too: §6.5 says the ranking IS the fallback chain,
	// so the runner-up does not need a second vote to be adopted.
	if err := d.applyOutcomeToSolutions(ctx, call, outcome, amendedSolution, fallbackTo, now); err != nil {
		return ConsensusCall{}, err
	}
	if err := d.writeDecisionRecord(ctx, call, outcome, summary, fallbackTo, amendedSolution); err != nil {
		return ConsensusCall{}, err
	}
	return d.GetConsensusCall(ctx, callID)
}

// SolutionAmendment is the derived solution §6.5's "accepted with amendments"
// spawns.
type SolutionAmendment struct {
	AuthorID int64
	Title    string
	Body     string
	Type     string
}

// applyOutcomeToSolutions moves each solution's status to match the decision.
func (d *DB) applyOutcomeToSolutions(ctx context.Context, call ConsensusCall,
	outcome string, amendedSolution, fallbackTo int64, now float64) error {

	target := *call.SolutionID
	switch outcome {
	case OutcomeAccepted, OutcomeAmended:
		if _, err := d.ExecContext(ctx,
			`UPDATE solutions SET status='ready', updated_at=? WHERE id=?`, now, target); err != nil {
			return fmt.Errorf("mark accepted solution ready: %w", err)
		}
	case OutcomeFallBack:
		if _, err := d.ExecContext(ctx,
			`UPDATE solutions SET status='rejected', updated_at=? WHERE id=?`, now, target); err != nil {
			return fmt.Errorf("reject displaced solution: %w", err)
		}
		if _, err := d.ExecContext(ctx,
			`UPDATE solutions SET status='ready', updated_at=? WHERE id=?`, now, fallbackTo); err != nil {
			return fmt.Errorf("mark fallback ready: %w", err)
		}
	case OutcomeRejected:
		if _, err := d.ExecContext(ctx,
			`UPDATE solutions SET status='rejected', updated_at=? WHERE id=?`, now, target); err != nil {
			return fmt.Errorf("reject solution: %w", err)
		}
	case OutcomeSentBack:
		// Back to discussion: the call did not decide, so the solution is not
		// rejected and not ready. Rejected would be a decision nobody made.
		if _, err := d.ExecContext(ctx,
			`UPDATE solutions SET status='discussion', updated_at=? WHERE id=?`, now, target); err != nil {
			return fmt.Errorf("return solution to discussion: %w", err)
		}
	}
	if amendedSolution != 0 {
		if _, err := d.ExecContext(ctx,
			`UPDATE solutions SET status='ready', updated_at=? WHERE id=?`, now, amendedSolution); err != nil {
			return fmt.Errorf("mark amendment ready: %w", err)
		}
	}
	return nil
}

// callFeatureID is the feature a call belongs to, refusing a call that has none.
func callFeatureID(call ConsensusCall) int64 {
	return call.FeatureID
}

// writeDecisionRecord is §6.5's "decision record attached to the feature".
func (d *DB) writeDecisionRecord(ctx context.Context, call ConsensusCall,
	outcome, summary string, fallbackTo, amendedSolution int64) error {

	positions, err := d.GetPositions(ctx, call.ID)
	if err != nil {
		return err
	}
	objections, err := d.GetObjections(ctx, call.ID)
	if err != nil {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", adrTitle(call, outcome))
	fmt.Fprintf(&b, "- **Outcome:** %s\n", outcome)
	if call.OpenedEarly != nil && *call.OpenedEarly {
		why := ""
		if call.EarlyReason != nil {
			why = *call.EarlyReason
		}
		fmt.Fprintf(&b, "- **Opened early:** yes — %s\n", why)
	}
	if summary != "" {
		fmt.Fprintf(&b, "- **Summary:** %s\n", summary)
	}
	if fallbackTo != 0 {
		fmt.Fprintf(&b, "- **Fell back to:** solution %d\n", fallbackTo)
	}
	if amendedSolution != 0 {
		fmt.Fprintf(&b, "- **Amended into:** solution %d\n", amendedSolution)
	}
	fmt.Fprintf(&b, "- **Closed at:** %s\n",
		time.Unix(int64(derefFloat(call.ClosedAt)), 0).UTC().Format(time.RFC3339))

	fmt.Fprintf(&b, "\n## Positions\n\n")
	if len(positions) == 0 {
		fmt.Fprintf(&b, "_No positions were cast._\n")
	} else {
		for _, p := range positions {
			fmt.Fprintf(&b, "- **%d** — %s", p.UserID, p.Position)
			if p.Reason != "" {
				fmt.Fprintf(&b, " — %s", p.Reason)
			}
			fmt.Fprintf(&b, "\n")
		}
	}

	fmt.Fprintf(&b, "\n## Objections\n\n")
	if len(objections) == 0 {
		fmt.Fprintf(&b, "_No objections were raised._\n")
	} else {
		for _, o := range objections {
			fmt.Fprintf(&b, "### %s\n\n", o.Status)
			fmt.Fprintf(&b, "- **Principle:** %s\n- **Why it is violated:** %s\n- **Remedy:** %s\n\n",
				o.Principle, o.Violation, o.Remedy)
		}
	}

	slug := fmt.Sprintf("call-%d", call.ID)
	if _, err := d.PutDocument(ctx, call.ProjectID, call.OpenedBy,
		"adr", slug, adrTitle(call, outcome), b.String()); err != nil {
		return fmt.Errorf("write decision record: %w", err)
	}
	return nil
}

func adrTitle(call ConsensusCall, outcome string) string {
	return fmt.Sprintf("Decision %d — %s", call.ID, outcome)
}

func derefFloat(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// CallFallback is §6.5's fallback chain: the solutions in the order a stalled or
// blocked call should fall back to them.
//
// The chain is the current ranking, so it changes as the arena does, with the
// baseline always last. The baseline is last rather than first because a chain
// that ends in "do nothing" is the honest worst case, and putting it first would
// make falling back to no-build the path of least resistance.
func (d *DB) CallFallback(ctx context.Context, callID int64) ([]SolutionScore, error) {
	call, err := d.GetConsensusCall(ctx, callID)
	if err != nil {
		return nil, err
	}
	if call.FeatureID == 0 {
		return nil, fmt.Errorf("%w: call %d is not attached to a feature", ErrInvalid, callID)
	}
	scores, err := d.ListSolutions(ctx, call.FeatureID, 50)
	if err != nil {
		return nil, err
	}
	chain := make([]SolutionScore, 0, len(scores))
	for _, s := range scores {
		// The solution the call is about is excluded: the fallback chain is what
		// you move TO when the proposal on the table fails, and including it makes
		// "fall back" a no-op the store has to special-case later.
		if call.SolutionID != nil && s.SolutionID == *call.SolutionID {
			continue
		}
		chain = append(chain, s)
	}
	return chain, nil
}

// unused keeps database/sql imported for the NullableID helper below, which is
// only referenced by CreateSolution's caller in this file's tests. Removed by the
// compiler as dead code the moment the tests use it directly.
var _ = sql.ErrNoRows
