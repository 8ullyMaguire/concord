package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// §6.5's decision record, after §6.8's close.
//
// The two halves of this file exist because they were mutually exclusive in the
// code while both being required by the spec, and neither the tests nor the page
// could see it:
//
//   - §6.8 gives the maintainer "close consensus", and the close writes the
//     verdict. It sets status='closed'.
//   - §6.5 says the call's result, positions, objections and remedies are
//     "auto-written into a decision record (ADR) attached to the feature", which
//     RecordCallOutcome does -- and it refused any call whose status was not
//     'open'.
//
// So the maintainer's own close was what made the decision record unwritable: the
// verdict could be recorded, or the record could, but not both, and the page only
// ever offered the close. The record -- the part a reader consults a year later,
// and the only place "why this and not the runner-up" is written down -- was
// reachable only by NOT closing the call.

func TestASolutionCallKeepsItsDecisionRecordReachableAfterTheVerdict(t *testing.T) {
	f := newCallFixture(t)
	ctx := context.Background()
	call := openCallableCall(t, f)

	// The maintainer closes the call. This is the page's own control, and the
	// verdict it writes is the whole point of §6.8.
	if _, err := f.d.CloseConsensusCall(ctx, call.ID); err != nil {
		t.Fatalf("CloseConsensusCall: %v", err)
	}

	// Then §6.5's outcome is recorded. Before the fix this answered
	// "conflict: call N is already closed", so the record could not be written
	// for any call that had been closed the ordinary way.
	after, err := f.d.RecordCallOutcome(ctx, call.ID, OutcomeAccepted,
		"adopted: the rewrite is cheaper than the workaround", 0, nil)
	if err != nil {
		t.Fatalf("RecordCallOutcome after a close: %v "+
			"(without this the decision record is unreachable exactly when a call has been decided)", err)
	}
	if after.Outcome == nil || *after.Outcome != OutcomeAccepted {
		t.Errorf("outcome = %v, want accepted", after.Outcome)
	}
	// The verdict stays: an outcome is what the call decided about the SOLUTION
	// (§6.5), the result is §6.6's verdict on the CALL. Recording one must not
	// erase the other -- the entire reason they are separate columns.
	if after.Result == nil || *after.Result == "" {
		t.Error("recording an outcome erased the close's verdict; the two are different facts")
	}
	if after.Status != "closed" {
		t.Errorf("status = %q after recording an outcome, want closed", after.Status)
	}

	// And the record itself exists.
	if _, err := f.d.GetDocumentBySlug(ctx, f.project, "adr", fmt.Sprintf("call-%d", call.ID)); err != nil {
		t.Errorf("the decision record was not written: %v", err)
	}
}

func TestAHeldCallCannotBeResolvedByRecordingItsOutcome(t *testing.T) {
	// §6.6's hold: "an emergency hold suspends a call", and suspension means the call
	// does not resolve while the hold is in force. Recording an outcome RESOLVES it --
	// the write sets status='closed' -- so this route has to refuse a held call for
	// exactly the reason CloseConsensusCall and the override window do: a hold that can
	// be walked around by choosing a different menu item is not a pause.
	f := newCallFixture(t)
	ctx := context.Background()
	call := openCallableCall(t, f)

	if _, err := f.d.PlaceEmergencyHold(ctx, f.project, call.ID, f.author,
		"security", "a live vulnerability in the exporter", 7); err != nil {
		t.Fatalf("PlaceEmergencyHold: %v", err)
	}
	if _, err := f.d.RecordCallOutcome(ctx, call.ID, OutcomeAccepted, "adopted", 0, nil); !errors.Is(err, ErrConflict) {
		t.Errorf("recording an outcome on a held call: err = %v, want ErrConflict naming the hold", err)
	}

	// Nothing was written: the refusal happens before the call is resolved and before
	// the record is written, so a held call is left exactly as it was.
	after, err := f.d.GetConsensusCall(ctx, call.ID)
	if err != nil {
		t.Fatalf("GetConsensusCall: %v", err)
	}
	if after.Status != "open" {
		t.Errorf("status = %q, want open: a refused outcome must not resolve the call", after.Status)
	}
	if after.Outcome != nil {
		t.Errorf("outcome = %v, want none: the call was not decided", *after.Outcome)
	}
	if _, err := f.d.GetDocumentBySlug(ctx, f.project, "adr", fmt.Sprintf("call-%d", call.ID)); err == nil {
		t.Error("a refused outcome still wrote a decision record")
	}
}

func TestACallWithADecisionRecordIsNotDecidedTwice(t *testing.T) {
	// The old guard was `status != "open"`, which refused this AND the wanted
	// case above. The guard that survives the loosening is the one that was
	// always the real concern: a call that already carries an outcome is
	// decided, and a second outcome would overwrite the first.
	f := newCallFixture(t)
	ctx := context.Background()
	call := openCallableCall(t, f)

	if _, err := f.d.RecordCallOutcome(ctx, call.ID, OutcomeAccepted, "first", 0, nil); err != nil {
		t.Fatalf("RecordCallOutcome: %v", err)
	}
	if _, err := f.d.RecordCallOutcome(ctx, call.ID, OutcomeRejected, "second", 0, nil); !errors.Is(err, ErrConflict) {
		t.Errorf("recording a second outcome: err = %v, want ErrConflict", err)
	}
}

func TestTheDecisionRecordCarriesTheRankingItWasMadeAgainst(t *testing.T) {
	// §6.5: "The ranking is the fallback chain. If the call stalls or a block is
	// upheld, the next-ranked solution is the pre-agreed fallback."
	//
	// "Pre-agreed" is the part a record has to carry: the chain is a fact about
	// the moment of the decision, and the arena keeps moving afterwards. Without
	// it the record says what was chosen and never why -- not the runner-up, not
	// the scores it was chosen over, not the baseline it beat.
	f := newCallFixture(t)
	ctx := context.Background()
	call := openCallableCall(t, f)

	if _, err := f.d.RecordCallOutcome(ctx, call.ID, OutcomeAccepted,
		"adopted", 0, nil); err != nil {
		t.Fatalf("RecordCallOutcome: %v", err)
	}
	doc, err := f.d.GetDocumentBySlug(ctx, f.project, "adr", fmt.Sprintf("call-%d", call.ID))
	if err != nil {
		t.Fatalf("the decision record was not written: %v", err)
	}
	body := doc.Body

	// The chosen solution, the runner-up and the baseline are all named, in the
	// markup of a ranking: the reader needs to see them side by side.
	for _, want := range []string{
		"Rewrite the exporter",    // the solution the call was about
		"Document the workaround", // the runner-up: the pre-agreed fallback
		"Do nothing",              // the baseline, ranked and never selectable
		"pre-agreed fallback",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the decision record does not mention %q:\n%s", want, body)
		}
	}
	// The chosen one is marked as chosen, and the fallback is marked as the
	// fallback: "which of these did we take" is the question the record answers.
	//
	// Asserted ON THE ROW, not merely somewhere in the document. The paragraph under
	// the table also contains the words "pre-agreed fallback", so a bare
	// strings.Contains passed even with no row marked -- which the mutation gate found
	// by mutating exactly that marker and watching the test stay green.
	if !strings.Contains(body, "the solution this call was about (adopted)") {
		t.Errorf("the record does not mark which solution was adopted:\n%s", body)
	}
	runnerRow := rowContaining(body, "Document the workaround")
	if !strings.Contains(runnerRow, "the pre-agreed fallback") {
		t.Errorf("the runner-up's own row does not mark it as the pre-agreed fallback: %q", runnerRow)
	}
	baselineRow := rowContaining(body, "Do nothing")
	if !strings.Contains(baselineRow, "baseline") {
		t.Errorf("the baseline's row does not say it is the baseline: %q", baselineRow)
	}
	// The record is dated, and dated NOW. This order -- deciding a call that has not
	// been closed -- used to print call.ClosedAt before the write had set it, so every
	// record written this way claimed 1970. That was invisible while this was the only
	// reachable order, and it is the reason `decidedAt` is passed in rather than read
	// off the call.
	if strings.Contains(body, "1970-01-01") {
		t.Errorf("the record is dated the epoch:\n%s", body)
	}
}

// rowContaining returns the first line of a markdown table row that mentions needle.
//
// It exists because "does this string appear in the document" is a weaker question
// than "does this row say it", and the difference is exactly where a mutation hid: a
// marker moved out of the table stayed in the prose underneath it.
func rowContaining(body, needle string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

func TestAFallbackDecisionNamesTheEndOfTheChain(t *testing.T) {
	// The fall-back outcome moves the board to another solution and rejects the
	// one on the table. The record has to name both, or "we fell back to #2"
	// leaves the reader to guess which proposal was displaced.
	f := newCallFixture(t)
	ctx := context.Background()
	call := openCallableCall(t, f)

	if _, err := f.d.RecordCallOutcome(ctx, call.ID, OutcomeFallBack,
		"the rewrite stalled", f.runnerUp, nil); err != nil {
		t.Fatalf("RecordCallOutcome(fall_back): %v", err)
	}
	doc, err := f.d.GetDocumentBySlug(ctx, f.project, "adr", fmt.Sprintf("call-%d", call.ID))
	if err != nil {
		t.Fatalf("the decision record was not written: %v", err)
	}
	if !strings.Contains(doc.Body, "Document the workaround") {
		t.Errorf("the record does not name the solution that took over:\n%s", doc.Body)
	}
	// On the LINE that reports the fall-back, not merely somewhere in the record: the
	// solution also appears in the ranking table, so a bare "does the title appear"
	// passes even when the fall-back line still names a bare id. §6.5's record is read
	// by someone asking "what did we move to", and an id is not an answer.
	if want := fmt.Sprintf("- **Fell back to:** Document the workaround (#%d)", f.runnerUp); !strings.Contains(doc.Body, want) {
		t.Errorf("the record does not report the fall-back as %q:\n%s", want, doc.Body)
	}
	if !strings.Contains(doc.Body, "Rewrite the exporter") {
		t.Errorf("the record does not name the solution that was displaced:\n%s", doc.Body)
	}
}
