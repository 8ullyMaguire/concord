package store

// R16/R17/R18: intake modes, milestones, the roadmap's numbers and dependencies.
//
// Each test here is one of the spec's invariants (docs/specs/idea-intake-and-
// milestones-spec.md §7), named after the rule it guards, because a test that says
// "milestone CRUD" passes against a milestone that groups nothing.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// milestoneFixture: a project, an author, a validated complaint and one feature.
func milestoneFixture(t *testing.T) (db *DB, pid, uid, fid int64) {
	t.Helper()
	db, uid, pid = setupWithProject(t)
	ctx := context.Background()
	comp, err := db.CreateComplaint(ctx, pid, uid, "The export button does nothing", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := db.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	f, err := db.CreateFeature(ctx, pid, uid, "Make the export button export", "body", "", nil, nil, []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	return db, pid, uid, f.ID
}

// §7 invariant 1: the default is complaint_first and the guard ENFORCES it.
//
// This is the test the whole R16 exists for. Before it, the rule lived in the
// create handler and the store's message, so a second caller was a second opinion
// about the platform's premise.
func TestIntakeModeIsPerProjectAndTheGuardReadsIt(t *testing.T) {
	db, pid, uid, _ := milestoneFixture(t)
	ctx := context.Background()

	// The default, from the migration, is the behaviour that already shipped.
	mode, err := db.projectIntakeMode(ctx, pid)
	if err != nil {
		t.Fatalf("projectIntakeMode: %v", err)
	}
	if mode != IntakeComplaintFirst {
		t.Fatalf("a new project is %q, want %q", mode, IntakeComplaintFirst)
	}

	// complaint_first: an unbacked idea is refused, and the message says why.
	_, err = db.CreateFeature(ctx, pid, uid, "An idea nobody asked for", "", "", nil, nil, nil)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("create without a complaint in complaint_first: got %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "complaint_first") {
		t.Fatalf("the refusal must name the mode it is obeying: %q", err)
	}

	// idea_first: the same call is accepted.
	if err := db.SetIntakeMode(ctx, pid, uid, IntakeIdeaFirst); err != nil {
		t.Fatalf("SetIntakeMode: %v", err)
	}
	f, err := db.CreateFeature(ctx, pid, uid, "An idea nobody asked for", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("create in idea_first: %v", err)
	}
	if f.ID == 0 {
		t.Fatal("idea_first created no row")
	}

	// The mode is a governance column, so it left an act: the value and the log
	// must not be able to disagree.
	var n int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM audit_log WHERE project_id = ? AND action = 'intake_mode'`, pid).Scan(&n); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	if n != 1 {
		t.Fatalf("intake mode change wrote %d audit rows, want 1", n)
	}

	// An unknown mode is refused rather than stored by a typo.
	if err := db.SetIntakeMode(ctx, pid, uid, "idea-first"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("SetIntakeMode(typo): got %v, want ErrInvalid", err)
	}
}

// §7 invariant 2: filing without evidence does not mean COMMITTING without it.
//
// idea_first relaxes the create; ready still needs evidence. The two are different
// decisions and conflating them would let an idea-first project ship unbacked work.
func TestReadyStillNeedsEvidenceInAnIdeaFirstProject(t *testing.T) {
	db, pid, uid, _ := milestoneFixture(t)
	ctx := context.Background()
	if err := db.SetIntakeMode(ctx, pid, uid, IntakeIdeaFirst); err != nil {
		t.Fatalf("SetIntakeMode: %v", err)
	}
	f, err := db.CreateFeature(ctx, pid, uid, "Idea with no evidence", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	if err := db.UpdateFeatureStatus(ctx, f.ID, "ready"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ready without evidence: got %v, want ErrInvalid", err)
	}

	// A document is evidence (§3.4). The project gets one, and the same move passes.
	doc, err := db.PutDocument(ctx, pid, uid, "spec", "intake", "Intake", "body text")
	if err != nil {
		t.Fatalf("PutDocument: %v", err)
	}
	docID := doc.ID
	if err := db.AddFeatureEvidence(ctx, pid, f.ID, EvidenceDocument, docID); err != nil {
		t.Fatalf("AddFeatureEvidence: %v", err)
	}
	if err := db.UpdateFeatureStatus(ctx, f.ID, "ready"); err != nil {
		t.Fatalf("ready with a document as evidence: %v", err)
	}
	// And the evidence is readable back, with a label a page can print.
	ev, err := db.ListFeatureEvidence(ctx, f.ID)
	if err != nil {
		t.Fatalf("ListFeatureEvidence: %v", err)
	}
	if len(ev) != 1 || ev[0].Kind != EvidenceDocument || ev[0].RefID != docID {
		t.Fatalf("evidence read back as %+v", ev)
	}
	// A document from ANOTHER project is not evidence this project can cite.
	other := mustProject(t, db, uid)
	if err := db.AddFeatureEvidence(ctx, other, f.ID, EvidenceDocument, docID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project evidence: got %v, want ErrNotFound", err)
	}
}

// §7 invariant 3: progress counts SHIPMENTS, not statuses.
//
// The seeded feature is the trap: 925 of 1,874 features on the instance have no
// creation event, so a bar computed from `status` measures whoever ran the import.
func TestMilestoneProgressCountsOnlyRealShipments(t *testing.T) {
	db, pid, uid, fid := milestoneFixture(t)
	ctx := context.Background()
	m, err := db.CreateMilestone(ctx, pid, "v1", "", nil)
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}
	if _, err := db.SetFeatureMilestone(ctx, pid, fid, &m.ID, 0); err != nil {
		t.Fatalf("SetFeatureMilestone: %v", err)
	}
	// The migration's default status is 'proposed'; a status written straight into
	// the column is exactly the import artefact the rule is about.
	if _, err := db.ExecContext(ctx, `UPDATE features SET status='shipped' WHERE id=?`, fid); err != nil {
		t.Fatalf("raw status update: %v", err)
	}
	got, err := db.GetMilestone(ctx, pid, m.ID)
	if err != nil {
		t.Fatalf("GetMilestone: %v", err)
	}
	if got.Total != 1 {
		t.Fatalf("total = %d, want 1", got.Total)
	}
	if got.Shipped != 0 || got.Unshipped != 1 {
		t.Fatalf("shipped=%d unshipped=%d, want 0 and 1: a shipped status with no shipment event is an import artefact",
			got.Shipped, got.Unshipped)
	}
	// Now ship it through the audited path.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
		VALUES (?, ?, 'feature.status', 'feature', ?, 'in_progress -> shipped', ?)`,
		pid, uid, fid, float64(time.Now().Unix())); err != nil {
		t.Fatalf("audit insert: %v", err)
	}
	got, err = db.GetMilestone(ctx, pid, m.ID)
	if err != nil {
		t.Fatalf("GetMilestone: %v", err)
	}
	if got.Shipped != 1 || got.Unshipped != 0 {
		t.Fatalf("after a real shipment: shipped=%d unshipped=%d, want 1 and 0", got.Shipped, got.Unshipped)
	}
}

// §7 invariant 4: closing a milestone with open features is reconciled, not blocked.
//
// Done with open work is a lie; a blocked close is a trap. The successor moves them,
// with one act per feature, so "why did this move" is answerable from the log.
func TestClosingAMilestoneCarriesUnfinishedWorkToTheSuccessor(t *testing.T) {
	db, pid, uid, fid := milestoneFixture(t)
	ctx := context.Background()
	m1, err := db.CreateMilestone(ctx, pid, "v1", "", nil)
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}
	m2, err := db.CreateMilestone(ctx, pid, "v2", "", nil)
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}
	if _, err := db.SetFeatureMilestone(ctx, pid, fid, &m1.ID, 0); err != nil {
		t.Fatalf("SetFeatureMilestone: %v", err)
	}
	// No successor and not cancelling: refused, naming what stands in the way.
	if _, _, err := db.CloseMilestone(ctx, pid, m1.ID, uid, nil, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("closing with open work and no successor: got %v, want ErrInvalid", err)
	}
	m, moved, err := db.CloseMilestone(ctx, pid, m1.ID, uid, &m2.ID, false)
	if err != nil {
		t.Fatalf("CloseMilestone: %v", err)
	}
	if moved != 1 || m.Status != MilestoneDone {
		t.Fatalf("moved=%d status=%q, want 1 and done", moved, m.Status)
	}
	f, err := db.GetFeature(ctx, fid)
	if err != nil {
		t.Fatalf("GetFeature: %v", err)
	}
	if f.MilestoneID == nil || *f.MilestoneID != m2.ID {
		t.Fatalf("the open feature did not move to the successor: %+v", f.MilestoneID)
	}
	var acts int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM audit_log
		 WHERE project_id = ? AND entity = 'feature' AND entity_id = ? AND action = 'feature.milestone'`,
		pid, fid).Scan(&acts); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	if acts != 1 {
		t.Fatalf("the move wrote %d feature acts, want 1", acts)
	}
	// Closing twice is a conflict, not a second move.
	if _, _, err := db.CloseMilestone(ctx, pid, m1.ID, uid, &m2.ID, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("double close: got %v, want ErrConflict", err)
	}
}

// §7 invariant 5: a cycle is refused, and the refusal names the path.
func TestADependencyCycleIsRefusedNamingIt(t *testing.T) {
	db, pid, uid, fid := milestoneFixture(t)
	ctx := context.Background()
	b := backedFeature(t, db, pid, uid, "Second idea")
	if err := db.AddDependency(ctx, pid, fid, b.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	err := db.AddDependency(ctx, pid, b.ID, fid)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("closing the cycle: got %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("the refusal must say it is a cycle: %q", err)
	}
	// Both ends are named, so the reader can go and look at the edge they closed.
	if !strings.Contains(err.Error(), "#"+itoa(fid)) || !strings.Contains(err.Error(), "#"+itoa(b.ID)) {
		t.Fatalf("the refusal must name both features: %q", err)
	}
	if err := db.AddDependency(ctx, pid, fid, fid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("self-dependency: got %v, want ErrInvalid", err)
	}
}

// §7 invariant 6: a blocked feature cannot start, and the refusal names the blocker.
func TestInProgressIsRefusedWhileABlockerIsOpen(t *testing.T) {
	db, pid, uid, fid := milestoneFixture(t)
	ctx := context.Background()
	blocker := backedFeature(t, db, pid, uid, "The blocker")
	if err := db.AddDependency(ctx, pid, blocker.ID, fid); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	err := db.UpdateFeatureStatus(ctx, fid, "in_progress")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("in_progress while blocked: got %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "blocked by") || !strings.Contains(err.Error(), itoa(blocker.ID)) {
		t.Fatalf("the refusal must name the blocker: %q", err)
	}
	// A shipped blocker clears it.
	if _, err := db.ExecContext(ctx, `UPDATE features SET status='shipped' WHERE id=?`, blocker.ID); err != nil {
		t.Fatalf("ship the blocker: %v", err)
	}
	if err := db.UpdateFeatureStatus(ctx, fid, "in_progress"); err != nil {
		t.Fatalf("in_progress after the blocker shipped: %v", err)
	}
	open, err := db.OpenBlockers(ctx, fid)
	if err != nil {
		t.Fatalf("OpenBlockers: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("open blockers after shipping: %+v", open)
	}
}

// §7 invariant 7: a milestone answers only inside its own project.
//
// A distinct status for "exists but not yours" is an existence oracle, so the read
// must be indistinguishable from a missing row.
func TestAMilestoneIsScopedToItsProject(t *testing.T) {
	db, pid, uid, _ := milestoneFixture(t)
	ctx := context.Background()
	m, err := db.CreateMilestone(ctx, pid, "v1", "", nil)
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}
	other := mustProject(t, db, uid)
	if _, err := db.GetMilestone(ctx, other, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another project's milestone: got %v, want ErrNotFound", err)
	}
}

// The at-risk flag is derived, and it must not fire on a milestone that is already
// closed: a badge on finished work is a false alarm that trains readers to ignore it.
func TestAtRiskOnlyOnAnOpenOverdueMilestone(t *testing.T) {
	db, pid, uid, fid := milestoneFixture(t)
	ctx := context.Background()
	past := float64(time.Now().Add(-48 * time.Hour).Unix())
	m, err := db.CreateMilestone(ctx, pid, "overdue", "", &past)
	if err != nil {
		t.Fatalf("CreateMilestone: %v", err)
	}
	if _, err := db.SetFeatureMilestone(ctx, pid, fid, &m.ID, 0); err != nil {
		t.Fatalf("SetFeatureMilestone: %v", err)
	}
	got, err := db.GetMilestone(ctx, pid, m.ID)
	if err != nil {
		t.Fatalf("GetMilestone: %v", err)
	}
	if !got.AtRisk {
		t.Fatal("an overdue milestone with an open feature is not flagged at risk")
	}
	if _, _, err := db.CloseMilestone(ctx, pid, m.ID, uid, nil, true); err != nil {
		t.Fatalf("CloseMilestone: %v", err)
	}
	got, err = db.GetMilestone(ctx, pid, m.ID)
	if err != nil {
		t.Fatalf("GetMilestone: %v", err)
	}
	if got.AtRisk {
		t.Fatal("a cancelled milestone is still flagged at risk")
	}
}

// backedFeature creates a feature in the only way a complaint_first project allows:
// with a fresh validated complaint. A fixture that bypassed the guard would be
// testing a feature the product cannot have.
func backedFeature(t *testing.T, db *DB, pid, uid int64, title string) Feature {
	t.Helper()
	ctx := context.Background()
	comp, err := db.CreateComplaint(ctx, pid, uid, title+" pain", "body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := db.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	f, err := db.CreateFeature(ctx, pid, uid, title, "", "", nil, nil, []int64{comp.ID})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	return f
}

// §7 invariant 9: an idea-first feature RANKS.
//
// The false sentence in the shipped copy was "an unlinked feature has no pain
// score and never ranks". The ranking is built from features.elo_r and never joins
// feature_complaints, so an unbacked feature competes on its comparisons like any
// other -- and the point of idea_first is that such a feature can exist at all.
// This test is the sentence's replacement: not a claim, a measurement.
func TestAnIdeaFirstFeatureStillRanks(t *testing.T) {
	db, pid, uid, fid := milestoneFixture(t)
	ctx := context.Background()
	if err := db.SetIntakeMode(ctx, pid, uid, IntakeIdeaFirst); err != nil {
		t.Fatalf("SetIntakeMode: %v", err)
	}
	unbacked, err := db.CreateFeature(ctx, pid, uid, "An idea with no complaint", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	// One comparison's worth of rating, written the way the arena writes it.
	if _, err := db.ExecContext(ctx,
		`UPDATE features SET elo_r = 1700, elo_rd = 60 WHERE id = ?`, unbacked.ID); err != nil {
		t.Fatalf("rate the unbacked feature: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE features SET elo_r = 1300, elo_rd = 60 WHERE id = ?`, fid); err != nil {
		t.Fatalf("rate the complaint-backed feature: %v", err)
	}
	prios, err := db.GetFeaturePriorities(ctx, pid, testCharter(t, db, pid))
	if err != nil {
		t.Fatalf("GetFeaturePriorities: %v", err)
	}
	var seen bool
	var unbackedPriority, backedPriority float64
	for _, p := range prios {
		if p.ID == unbacked.ID {
			seen, unbackedPriority = true, p.PriorityScore
		}
		if p.ID == fid {
			backedPriority = p.PriorityScore
		}
	}
	if !seen {
		t.Fatal("the idea-first feature is absent from the ranking input; the false doc claim is still true")
	}
	if !(unbackedPriority > backedPriority) {
		t.Fatalf("the higher-rated unbacked feature ranked %v, the lower-rated backed one %v",
			unbackedPriority, backedPriority)
	}
	// And its evidence count is honestly zero: being ranked is not being backed.
	n, err := db.countFeatureEvidence(ctx, unbacked.ID)
	if err != nil {
		t.Fatalf("countFeatureEvidence: %v", err)
	}
	if n != 0 {
		t.Fatalf("evidence count = %d, want 0", n)
	}
}

// mustProject creates a second project in the same database.
func mustProject(t *testing.T, db *DB, uid int64) int64 {
	t.Helper()
	p, err := db.CreateProject(context.Background(), uid,
		"other-"+itoa(time.Now().UnixNano()%1000000), "Other", "", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return p.ID
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
