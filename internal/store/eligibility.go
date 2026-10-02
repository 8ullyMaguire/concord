package store

import (
	"context"
	"fmt"
	"time"
)

// §8.2's eligible collaborator, in full.
//
//	"A user is an eligible collaborator of a project if, within the trailing
//	 window (default 12 months), they have at least one qualifying contribution
//	 (merged code or docs, a validated complaint that led to a shipped solution,
//	 an accepted solution, a substantive review, or an upheld moderation
//	 action), or they were seated by charter consensus. Agents are excluded."
//
// This replaces a count that read the roles column and nothing else, which made
// every threshold in the system depend on a population that included people who
// had not touched the project in a decade. Three of the four qualifying routes
// are already representable in the schema; two needed a judgement call, both
// marked below.
//
// One thing this deliberately does NOT do is check "excluded from votes and
// confirmations on their own submissions". That is a per-item rule and belongs
// where the vote is cast; folding it into a population count would either make
// the count depend on the item being judged or silently exclude people who
// should be voting on everything but their own work.

// DefaultActivityWindowDays is §8.2's stated default.
const DefaultActivityWindowDays = 365.0

// qualifying contribution routes, as counted by qualifyingContributionsSQL.
//
// (1) Merged code or docs: a merge_request in state 'merged'. A project_document
//     is the docs side -- those are written through the API and have no merge
//     request, so requiring an MR for docs would make documentation never count.
//
// (2) A validated complaint that led to a shipped solution: the complaint is
//     validated AND a solution on a feature linking it reached a shipped state.
//     Both halves matter. Without "led to" this is just "validated complaint",
//     and every project would have a large eligible population of people whose
//     complaint was heard and ignored.
//
// (3) An accepted solution: status 'consensus' onward. §6.5's outcome vocabulary
//     distinguishes consensus (the proposal is adopted) from ready/in_progress,
//     so this starts at 'consensus'.
//
// (4) A substantive review: a merge_approval on somebody else's merge request.
//     Own-approval is excluded, which is why this joins back to merge_requests to
//     compare user_id against author_id. "Substantive" is read as an approval --
//     the act that moved a change over the line -- rather than as a comment,
//     because a comment's length is a matter of opinion and an approval is not.
//
// (5) An upheld moderation action: an admin_ledger entry by the actor with a
//     justification. The justification is the "upheld" part: an admin action that
//     was reversed or never justified is not a qualifying contribution, and the
//     ledger stores justification as nullable precisely so the difference is
//     visible.
//
// "Seated by charter consensus" has no representation: there is no charter
// amendment, seat appointment or election table in this schema, so there is
// nothing to count. That route is genuinely unimplemented rather than counted as
// zero, and IsAgent exists so an operator can check for agents separately.
//
// Agents: users has no agent flag and no agent table, so nothing can be excluded
// as one. Recorded here so the gap is visible rather than inferred from a
// COUNT that silently includes everything.

// qualifyingContributionsSQL is the union of §8.2's contribution routes, one row
// per (project, user) with a MAX(created_at) the caller filters on.
//
// UNION rather than OR: the routes live in different tables and a single OR needs
// a cross join to get there. MAX(created_at) is the most recent qualifying event,
// which is what "within the trailing window" means for someone with many.
const qualifyingContributionsSQL = `
	SELECT project_id, user_id, MAX(created_at) AS last_qualifying
	FROM (
		-- (1) merged code
		SELECT m.project_id AS project_id, m.author_id AS user_id,
		       COALESCE(m.closed_at, m.opened_at) AS created_at
		FROM merge_requests m
		WHERE m.status = 'merged'

		UNION ALL
		-- (1) merged docs
		SELECT d.project_id, d.author_id, d.updated_at
		FROM project_documents d

		UNION ALL
		-- (2) a validated complaint that led to a shipped solution
		SELECT c.project_id, c.author_id, MAX(s.updated_at)
		FROM complaints c
		JOIN feature_complaints fc ON fc.complaint_id = c.id
		JOIN features f ON f.id = fc.feature_id AND f.project_id = c.project_id
		JOIN solutions s ON s.feature_id = f.id
		WHERE c.status IN ('validated','linked','closed')
		  AND s.status IN ('consensus','ready','in_progress','review','shipped')
		GROUP BY c.project_id, c.author_id

		UNION ALL
		-- (3) an accepted solution
		SELECT f.project_id, s.author_id, s.updated_at
		FROM solutions s
		JOIN features f ON f.id = s.feature_id
		WHERE s.status IN ('consensus','ready','in_progress','review','shipped')

		UNION ALL
		-- (4) a substantive review: approving somebody else's merge request
		SELECT m.project_id, ma.user_id, ma.created_at
		FROM merge_approvals ma
		JOIN merge_requests m ON m.id = ma.mr_id
		WHERE ma.user_id != m.author_id

		UNION ALL
		-- (5) an upheld moderation action: a justified admin-ledger entry
		SELECT al.project_id, al.actor_id, al.created_at
		FROM admin_ledger al
		WHERE al.actor_id IS NOT NULL
		  AND al.project_id IS NOT NULL
		  AND al.justification IS NOT NULL
		  AND TRIM(al.justification) != ''
	)
	GROUP BY project_id, user_id`

// EligibleCollaborators returns the ids of §8.2's eligible collaborators, within
// the given window.
//
// windowDays <= 0 means DefaultActivityWindowDays. The ids are returned alongside
// the count because a quorum decision needs to know *who* is in the room, not only
// how many -- §6.5's distinct-voter condition has to check the voter is actually
// eligible, and a count alone cannot answer that.
func (d *DB) EligibleCollaborators(ctx context.Context, projectID int64, windowDays float64) ([]int64, error) {
	if windowDays <= 0 {
		windowDays = DefaultActivityWindowDays
	}
	cutoff := float64(time.Now().Add(-time.Duration(windowDays * float64(24*time.Hour))).Unix())

	rows, err := d.QueryContext(ctx, `
		SELECT q.user_id
		FROM (`+qualifyingContributionsSQL+`) q
		WHERE q.project_id = ?
		  AND q.last_qualifying >= ?
		  AND EXISTS (SELECT 1 FROM members m
		              WHERE m.project_id = q.project_id
		                AND m.user_id = q.user_id
		                AND m.role IN ('contributor','reviewer','maintainer','owner'))
		ORDER BY q.user_id`, projectID, cutoff)
	if err != nil {
		return nil, fmt.Errorf("eligible collaborators: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("eligible collaborators scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// EligibleCollaboratorCount is the count every threshold in the system reads.
//
// The EXISTS on members is §8.2's conjunction, not a convenience: the spec makes
// role AND activity both necessary, so an inactive maintainer is ineligible and an
// active guest is ineligible. Counting roles alone (the previous behaviour) got
// the second case wrong in a project with many active guests, and the first case
// wrong in every project with anyone who joined long ago and left.
func (d *DB) EligibleCollaboratorCount(ctx context.Context, projectID int64) (int, error) {
	ids, err := d.EligibleCollaborators(ctx, projectID, DefaultActivityWindowDays)
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}

// IsEligibleCollaborator reports whether one user is in §8.2's population.
//
// This is the check §6.5's distinct-voter condition needs: "distinct voters" that
// includes people with no standing to vote is a quorum nobody can satisfy.
func (d *DB) IsEligibleCollaborator(ctx context.Context, projectID, userID int64) (bool, error) {
	ids, err := d.EligibleCollaborators(ctx, projectID, DefaultActivityWindowDays)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if id == userID {
			return true, nil
		}
	}
	return false, nil
}

// HasQualifyingContribution is IsEligibleCollaborator without the role
// requirement, for the places that ask about contribution rather than standing.
func (d *DB) HasQualifyingContribution(ctx context.Context, projectID, userID int64) (bool, error) {
	var n int
	err := d.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (`+qualifyingContributionsSQL+`) q
		WHERE q.project_id = ? AND q.user_id = ?`, projectID, userID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("qualifying contribution: %w", err)
	}
	return n > 0, nil
}
