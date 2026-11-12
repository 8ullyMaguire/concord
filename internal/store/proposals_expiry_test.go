package store

import (
	"context"
	"strings"
	"testing"
)

// seqAgedProposal inserts one pending proposal of each kind, aged `days` behind now, and returns
// their ids. Direct SQL on purpose: the point of this test is what the sweep does to a row it
// finds old, and every propose/ratify path has its own tests. Writing created_at is the only way
// to make a proposal old without waiting a month.
func seqAgedProposal(t *testing.T, d *DB, uid, pid int64, tag string, ageDays float64) (taxonomy, strategy, theme int64) {
	t.Helper()
	ctx := context.Background()
	created := float64(1794000000) - ageDays*86400

	res, err := d.ExecContext(ctx, `INSERT INTO taxonomy_proposals
		(project_id, proposed_by, action, target_tag, value, status, created_at)
		VALUES (?,?,'create',?,'', 'pending', ?)`, pid, uid, tag, created)
	if err != nil {
		t.Fatalf("insert taxonomy proposal: %v", err)
	}
	taxonomy, _ = res.LastInsertId()

	// One PENDING weight proposal per feature (0012's index), so each aging needs its own
	// feature: two against the same one is the schema refusing a shape the product forbids.
	// One PENDING weight proposal per feature (0012's index), so each aging needs its own. The
	// feature is inserted directly: CreateFeature refuses a feature with no validated complaint
	// behind it, which is the right rule for the product and irrelevant to a sweep fixture.
	fres, err := d.ExecContext(ctx, `INSERT INTO features
		(project_id, author_id, title, body, effort, status, created_at, updated_at)
		VALUES (?,?,?, 'b', 'M', 'discussion', ?, ?)`, pid, uid, "weight target "+tag, created, created)
	if err != nil {
		t.Fatalf("insert feature: %v", err)
	}
	fID2, _ := fres.LastInsertId()
	res, err = d.ExecContext(ctx, `INSERT INTO strategic_weight_proposals
		(project_id, feature_id, proposed_by, weight_before, weight_after, rationale, status, created_at)
		VALUES (?,?,?,0,1,'', 'pending', ?)`, pid, fID2, uid, created)
	if err != nil {
		t.Fatalf("insert weight proposal: %v", err)
	}
	strategy, _ = res.LastInsertId()

	res, err = d.ExecContext(ctx, `INSERT INTO strategic_theme_proposals
		(project_id, theme_tag, theme_namespace, proposed_by, weight_before, weight_after, rationale, status, created_at)
		VALUES (?,?,'topic',?,NULL,2,'', 'pending', ?)`, pid, "theme-"+tag, uid, created)
	if err != nil {
		t.Fatalf("insert theme proposal: %v", err)
	}
	theme, _ = res.LastInsertId()
	return taxonomy, strategy, theme
}

func seqStatus(t *testing.T, d *DB, table string, id int64) string {
	t.Helper()
	var st string
	if err := d.QueryRow(`SELECT status FROM `+table+` WHERE id = ?`, id).Scan(&st); err != nil {
		t.Fatalf("read %s %d status: %v", table, id, err)
	}
	return st
}

// A pending proposal older than the cutoff is the case the moderation spec left open: the queue
// lists it forever, so a reader who never returns to the page keeps work in front of reviewers that
// nobody is arguing for any more. Expiry marks it stale -- it stays in the log, it leaves the queue.
func TestExpireProposalsMarksOnlyOldPendingOnes(t *testing.T) {
	d, uid, pid, _ := arenaFixture(t)
	ctx := context.Background()

	oldTax, oldW, oldTh := seqAgedProposal(t, d, uid, pid, "ancient", 40)
	// A fresh one of each kind, which the same sweep must leave alone.
	newTax, newW, newTh := seqAgedProposal(t, d, uid, pid, "fresh", 1)
	// A decided proposal that is also old: decided is decided, and a sweep that re-marks it
	// would be rewriting a decision record.
	decidedTax, decidedW, decidedTh := seqAgedProposal(t, d, uid, pid, "decided", 40)
	for _, u := range []struct {
		table string
		id    int64
	}{
		{"taxonomy_proposals", decidedTax},
		{"strategic_weight_proposals", decidedW},
		{"strategic_theme_proposals", decidedTh},
	} {
		if _, err := d.ExecContext(ctx, `UPDATE `+u.table+` SET status='ratified', decided_at=created_at WHERE id = ?`, u.id); err != nil {
			t.Fatalf("mark %s ratified: %v", u.table, err)
		}
	}

	cutoff := float64(1794000000) - 30*86400
	rep, err := d.ExpireProposals(ctx, cutoff, false)
	if err != nil {
		t.Fatalf("ExpireProposals: %v", err)
	}
	if rep.Total != 3 {
		t.Fatalf("expired %d, want 3 (%+v)", rep.Total, rep.Expired)
	}

	for _, tc := range []struct {
		table string
		id    int64
		want  string
	}{
		{"taxonomy_proposals", oldTax, "stale"},
		{"strategic_weight_proposals", oldW, "stale"},
		{"strategic_theme_proposals", oldTh, "stale"},
		{"taxonomy_proposals", newTax, "pending"},
		{"strategic_weight_proposals", newW, "pending"},
		{"strategic_theme_proposals", newTh, "pending"},
		{"taxonomy_proposals", decidedTax, "ratified"},
		{"strategic_weight_proposals", decidedW, "ratified"},
		{"strategic_theme_proposals", decidedTh, "ratified"},
	} {
		if got := seqStatus(t, d, tc.table, tc.id); got != tc.want {
			t.Errorf("%s %d is %q, want %q", tc.table, tc.id, got, tc.want)
		}
	}

	// Second run: nothing left to expire, and no second act for the same proposal.
	again, err := d.ExpireProposals(ctx, cutoff, false)
	if err != nil {
		t.Fatalf("second ExpireProposals: %v", err)
	}
	if again.Total != 0 {
		t.Fatalf("second sweep expired %d, want 0", again.Total)
	}
}

// An expiry nobody can find afterwards is a deletion with extra steps. Each expired proposal writes
// one act naming the proposal, its proposer and the fact that the clock ran out -- not a count.
func TestExpireProposalsLeavesOneActPerProposal(t *testing.T) {
	d, uid, pid, _ := arenaFixture(t)
	ctx := context.Background()
	tax, _, _ := seqAgedProposal(t, d, uid, pid, "quiet", 45)

	cutoff := float64(1794000000) - 30*86400
	if _, err := d.ExpireProposals(ctx, cutoff, false); err != nil {
		t.Fatalf("ExpireProposals: %v", err)
	}

	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'stale_taxonomy_proposal' AND entity = 'taxonomy_proposal' AND entity_id = ?`, tax).Scan(&n); err != nil {
		t.Fatalf("count acts: %v", err)
	}
	if n != 1 {
		t.Fatalf("%d acts for the expired proposal, want 1", n)
	}

	var detail string
	if err := d.QueryRow(`SELECT detail FROM audit_log WHERE action='stale_taxonomy_proposal' AND entity_id = ?`, tax).Scan(&detail); err != nil {
		t.Fatalf("read detail: %v", err)
	}
	if !strings.Contains(detail, "30 days") {
		t.Errorf("detail does not say why it expired: %s", detail)
	}
}

// A dry run is how an operator sees what a first sweep would do to a live database before it does
// it. It must therefore compute the same set and write nothing -- including no act.
func TestExpireProposalsDryRunWritesNothing(t *testing.T) {
	d, uid, pid, _ := arenaFixture(t)
	ctx := context.Background()
	tax, _, _ := seqAgedProposal(t, d, uid, pid, "dry", 40)

	cutoff := float64(1794000000) - 30*86400
	rep, err := d.ExpireProposals(ctx, cutoff, true)
	if err != nil {
		t.Fatalf("ExpireProposals dry: %v", err)
	}
	if rep.Total != 3 || !rep.DryRun {
		t.Fatalf("dry run reported %d expired, dry=%v; want 3 and true", rep.Total, rep.DryRun)
	}
	if got := seqStatus(t, d, "taxonomy_proposals", tax); got != "pending" {
		t.Fatalf("a dry run changed the status to %q", got)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action LIKE 'stale_%'`).Scan(&n); err != nil {
		t.Fatalf("count acts: %v", err)
	}
	if n != 0 {
		t.Fatalf("a dry run wrote %d acts", n)
	}
}
