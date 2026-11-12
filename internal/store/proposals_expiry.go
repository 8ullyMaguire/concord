// Expiry for proposals: the one status every proposal table models and nothing ever set.
//
// The moderation spec's queue lists pending proposals by age from newest to oldest, and nothing
// removes one. A reader who opens a proposal and does not come back leaves work in front of
// reviewers that nobody is arguing for any more, and the queue -- whose whole value is that a
// reviewer can start at the top and finish -- grows without bound.
//
// The decision (spec §8.6) is to MARK, not to withdraw: an expired proposal keeps its row, leaves
// the open queue, and gets an act saying the clock ran out. Deleting it would erase the record of
// someone having proposed something; withdrawing it under the proposer's name would put words in
// their mouth. `stale` is the only status that says exactly what happened.
//
// Where this runs is as deliberate as what it does: a one-shot command under a timer, like
// healthsync and statsnapshot, not a goroutine in the server and not a sweep on read. The server
// restarts on every deploy (a loop loses its schedule), and a sweep that runs inside a GET turns a
// page view into a write.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// DefaultProposalTTLDays is how long a proposal may sit unanswered. Thirty days is a sprint plus a
// margin: long enough that a proposal raised before a quiet week can still gather consent, short
// enough that the queue does not accumulate decisions nobody will make.
const DefaultProposalTTLDays = 30

// ExpiredProposal is one row the sweep marked, or would mark in a dry run.
type ExpiredProposal struct {
	Kind       string `json:"kind"`
	ID         int64  `json:"id"`
	ProjectID  int64  `json:"project_id"`
	ProposedBy int64  `json:"proposed_by"`
	Consents   int    `json:"consents"`
}

// ExpireReport is what a sweep did. Total is len(Expired) and exists so a timer's journal line can
// be one number without the operator having to count an array.
type ExpireReport struct {
	Expired []ExpiredProposal `json:"expired"`
	Total   int               `json:"total"`
	DryRun  bool              `json:"dry_run"`
	Cutoff  float64           `json:"cutoff"`
}

// proposalKind names the per-table facts the sweep needs, so the loop below is written once
// instead of three times with three chances to differ.
type proposalKind struct {
	kind     string
	table    string
	consents string
	action   string // the act written for each expired proposal
	entity   string // what the act's entity_id points at
}

var proposalKinds = []proposalKind{
	{"taxonomy", "taxonomy_proposals", "taxonomy_consents", "stale_taxonomy_proposal", "taxonomy_proposal"},
	{"weight", "strategic_weight_proposals", "strategic_weight_consents", "stale_weight_proposal", "strategic_weight_proposal"},
	{"theme", "strategic_theme_proposals", "strategic_theme_consents", "stale_theme_proposal", "strategic_theme_proposal"},
}

// ExpireProposals marks every pending proposal created before cutoff as stale, writes one act per
// proposal, and returns what it did. With dryRun it computes the same set and writes nothing: an
// operator pointing this at a live database for the first time needs to see the list before it
// happens, and the list is the only thing that makes the first run safe.
//
// Idempotent: the UPDATE is guarded by `status = 'pending'`, so a second run finds nothing and
// writes no second act for the same proposal. That matters because the timer reruns the pass.
func (d *DB) ExpireProposals(ctx context.Context, cutoff float64, dryRun bool) (ExpireReport, error) {
	rep := ExpireReport{Expired: []ExpiredProposal{}, DryRun: dryRun, Cutoff: cutoff}
	now := float64(time.Now().Unix())

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return rep, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, k := range proposalKinds {
		// One query per kind, not one per row: the consents are a correlated subquery rather than
		// a second round trip for each proposal, and the count is what the act's detail records.
		rows, err := tx.QueryContext(ctx, `SELECT p.id, p.project_id, p.proposed_by,
				(SELECT COUNT(*) FROM `+k.consents+` c WHERE c.proposal_id = p.id)
			FROM `+k.table+` p
			WHERE p.status = 'pending' AND p.created_at < ?
			ORDER BY p.created_at`, cutoff)
		if err != nil {
			return rep, fmt.Errorf("list %s: %w", k.table, err)
		}
		var batch []ExpiredProposal
		for rows.Next() {
			var e ExpiredProposal
			if err := rows.Scan(&e.ID, &e.ProjectID, &e.ProposedBy, &e.Consents); err != nil {
				rows.Close()
				return rep, fmt.Errorf("scan %s: %w", k.table, err)
			}
			e.Kind = k.kind
			batch = append(batch, e)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return rep, fmt.Errorf("iterate %s: %w", k.table, err)
		}
		rows.Close()

		for _, e := range batch {
			if !dryRun {
				res, err := tx.ExecContext(ctx, `UPDATE `+k.table+`
					SET status = 'stale' WHERE id = ? AND status = 'pending'`, e.ID)
				if err != nil {
					return rep, fmt.Errorf("mark %s %d stale: %w", k.table, e.ID, err)
				}
				// Zero rows affected means another writer decided it between the SELECT and the
				// UPDATE. Reporting it as expired would be a lie the log then repeats.
				if n, _ := res.RowsAffected(); n == 0 {
					continue
				}
				detail, _ := json.Marshal(map[string]any{
					"reason":      fmt.Sprintf("no consensus within %d days", int(DefaultProposalTTLDays)),
					"proposed_by": e.ProposedBy,
					"consents":    e.Consents,
					"cutoff":      cutoff,
				})
				// actor_id NULL: nobody did this. Recording a user here -- the proposer, the
				// reviewer, whoever last looked -- would attribute an automatic expiry to a person.
				if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log
					(project_id, actor_id, action, entity, entity_id, detail, created_at)
					VALUES (?, NULL, ?, ?, ?, ?, ?)`,
					e.ProjectID, k.action, k.entity, e.ID, string(detail), now); err != nil {
					return rep, fmt.Errorf("act for %s %d: %w", k.table, e.ID, err)
				}
			}
			rep.Expired = append(rep.Expired, e)
		}
	}

	if dryRun {
		rep.Total = len(rep.Expired)
		return rep, nil // the deferred Rollback discards the read-only transaction
	}
	if err := tx.Commit(); err != nil {
		return rep, fmt.Errorf("commit: %w", err)
	}
	rep.Total = len(rep.Expired)
	return rep, nil
}
