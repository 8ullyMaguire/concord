package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/governance"
)

type MergeRequest struct {
	ID          int64    `json:"id"`
	ProjectID   int64    `json:"project_id"`
	FeatureID   int64    `json:"feature_id"`
	AuthorID    int64    `json:"author_id"`
	Title       string   `json:"title"`
	ExternalRef *string  `json:"external_ref,omitempty"`
	Status      string   `json:"status"`
	OpenedAt    float64  `json:"opened_at"`
	ClosedAt    *float64 `json:"closed_at,omitempty"`
}

type MergeApproval struct {
	MRID      int64   `json:"mr_id"`
	UserID    int64   `json:"user_id"`
	Weight    float64 `json:"weight"`
	CreatedAt float64 `json:"created_at"`
}

// MergeGateState is §5.7's verdict for one merge request: who has confirmed, what
// the charter requires, and whether it passes.
//
// It exists because the gate was invisible. A client that has to work out the
// quorum for itself is a second implementation of §5.7, and the one that gets it
// wrong -- the mistake the criteria page avoids by rendering the forge's own
// arithmetic rather than re-deriving it.
//
// There was a gate all along: governance.CheckMergeGate has implemented the rule
// since the beginning, with its own unit test, and no caller. ExecuteMerge went
// straight from "somebody is logged in" to status='merged', shipping the feature and
// closing every complaint linked to it.
//
// Approvals is the COUNTED number, not the number of rows: an approval by the
// request's own author, or by somebody who is no longer an eligible collaborator,
// exists in the table and does not count. A page that counted rows would show a
// filled quorum the server refuses to honour.
type MergeGateState struct {
	Approvals      int  `json:"approvals"`
	Needed         int  `json:"needed"`
	Eligible       int  `json:"eligible"`
	QuorumOK       bool `json:"quorum_ok"`
	ReviewerOK     bool `json:"reviewer_ok"`
	RequiresQuorum bool `json:"requires_quorum"`
	Passed         bool `json:"passed"`
}

// MergeRequestView is a merge request with its gate attached, which is what the
// list endpoint returns. Embedding keeps every existing field at the top level, so
// this is additive for clients.
type MergeRequestView struct {
	MergeRequest
	Gate MergeGateState `json:"gate"`
}

// Sentinel errors for the confirmation rules. Both wrap a domain error so
// internal/httpapi's mapError answers correctly without a special case: a 409 for
// the two state refusals, 403 for somebody who is not part of the project.
var (
	// ErrMergeGateNotMet is §5.7's quorum or reviewer requirement unmet.
	ErrMergeGateNotMet = errors.New("merge confirmation gate not met")
	// ErrSelfApproval is an author confirming their own request. §6.3 counts
	// "distinct collaborator confirmations" -- confirmations of each other's work,
	// which is what the eligibility engine's route (4) already says when it defines
	// a substantive review as "a merge_approval on somebody else's merge request".
	ErrSelfApproval = errors.New("an author cannot confirm their own merge request")
	// ErrNotProjectMember is a merge action by somebody with no role here.
	ErrNotProjectMember = errors.New("merging requires membership of the project")
)

// Confirmations counts the merge request's confirmations: distinct users who
// approved, are not the author, and are eligible collaborators of the project now.
//
// Returns the count, whether any of them holds ≥ reviewer, and the eligible
// population the threshold is measured against.
func (d *DB) Confirmations(ctx context.Context, mr MergeRequest) (int, bool, int, error) {
	eligibleIDs, err := d.EligibleCollaborators(ctx, mr.ProjectID, DefaultActivityWindowDays)
	if err != nil {
		return 0, false, 0, err
	}
	eligible := make(map[int64]bool, len(eligibleIDs))
	for _, id := range eligibleIDs {
		eligible[id] = true
	}
	rows, err := d.GetMergeApprovals(ctx, mr.ID)
	if err != nil {
		return 0, false, 0, err
	}
	counted := 0
	reviewer := false
	for _, a := range rows {
		// The author's own approval and a lapsed collaborator's are both rows that
		// do not hold the gate open. Ownership is decided here rather than at insert
		// time so that an approval recorded before this rule existed cannot count
		// retroactively either.
		if a.UserID == mr.AuthorID || !eligible[a.UserID] {
			continue
		}
		counted++
		if !reviewer {
			role, err := d.GetRoleForProject(ctx, mr.ProjectID, a.UserID)
			if err == nil && governance.Role(role).AtLeast(governance.Reviewer) {
				reviewer = true
			}
		}
	}
	return counted, reviewer, len(eligibleIDs), nil
}

// MergeGateFor returns §5.7's verdict for one merge request.
func (d *DB) MergeGateFor(ctx context.Context, mrID int64) (MergeGateState, error) {
	mr, err := d.GetMergeRequest(ctx, mrID)
	if err != nil {
		return MergeGateState{}, err
	}
	charter, err := d.GetCharterForProject(ctx, mr.ProjectID)
	if err != nil {
		return MergeGateState{}, err
	}
	approvals, reviewer, eligible, err := d.Confirmations(ctx, mr)
	if err != nil {
		return MergeGateState{}, err
	}
	// The rule itself is governance's, unchanged and un-duplicated: this computes
	// the inputs it needs.
	gate := governance.CheckMergeGate(approvals, eligible, reviewer, charter)
	return MergeGateState{
		Approvals:      gate.Approvals,
		Needed:         gate.QuorumNeeded,
		Eligible:       eligible,
		QuorumOK:       gate.QuorumOK,
		ReviewerOK:     gate.ReviewerOK,
		RequiresQuorum: gate.MergeRequiresQuorum,
		Passed:         gate.Passed(),
	}, nil
}

// shortfall explains an unmet gate in the client's own terms. A refusal that says
// only "gate not met" leaves a reader unable to tell whether they need another
// confirmation or a reviewer.
func (g MergeGateState) shortfall() string {
	var why []string
	if g.RequiresQuorum && !g.QuorumOK {
		why = append(why, fmt.Sprintf("%d of %d confirmations given", g.Approvals, g.Needed))
	}
	if !g.ReviewerOK {
		why = append(why, "a reviewer approval is required")
	}
	if len(why) == 0 {
		return "the merge gate is not met"
	}
	return strings.Join(why, "; ")
}

func (d *DB) CreateMergeRequest(ctx context.Context, projectID, featureID, authorID int64, title string) (MergeRequest, error) {
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO merge_requests (project_id, feature_id, author_id, title, status, opened_at)
		VALUES (?, ?, ?, ?, 'open', ?)`, projectID, featureID, authorID, title, now)
	if err != nil {
		return MergeRequest{}, err
	}
	id, _ := res.LastInsertId()
	return MergeRequest{ID: id, ProjectID: projectID, FeatureID: featureID, AuthorID: authorID, Title: title, Status: "open", OpenedAt: now}, nil
}

func (d *DB) GetMergeRequest(ctx context.Context, id int64) (MergeRequest, error) {
	var mr MergeRequest
	err := d.QueryRowContext(ctx, `SELECT id, project_id, feature_id, author_id, title, external_ref, status, opened_at, closed_at
		FROM merge_requests WHERE id=?`, id).Scan(
		&mr.ID, &mr.ProjectID, &mr.FeatureID, &mr.AuthorID, &mr.Title, &mr.ExternalRef, &mr.Status, &mr.OpenedAt, &mr.ClosedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MergeRequest{}, fmt.Errorf("%w: merge request %d", ErrNotFound, id)
	}
	return mr, err
}

// ApproveMerge records one collaborator's confirmation.
//
// Two rules, neither of which existed -- the handler asked only for a non-zero
// actor id, so any signed-in account could fill a table nothing read:
//
//   - the approver must be a member of the project with role ≥ contributor. NOT
//     "an eligible collaborator", which was the first version of this rule and was
//     wrong: route (4) of qualifyingContributionsSQL says that approving somebody
//     else's merge request IS a qualifying contribution, so requiring eligibility
//     first would make that route unreachable -- nobody could ever become a
//     collaborator by reviewing, which is what reviewing is for.
//   - the author cannot confirm their own request. §6.3 counts confirmations of
//     each other's work, and the same query excludes self-approval for that reason.
//
// Membership is what makes the act real, and eligibility is what makes the
// confirmation COUNT -- decided in Confirmations, where the threshold is measured.
func (d *DB) ApproveMerge(ctx context.Context, mrID, userID int64) error {
	mr, err := d.GetMergeRequest(ctx, mrID)
	if err != nil {
		return err
	}
	if userID == mr.AuthorID {
		return fmt.Errorf("%w: %w", ErrConflict, ErrSelfApproval)
	}
	role, err := d.GetRoleForProject(ctx, mr.ProjectID, userID)
	if err != nil || !governance.Role(role).AtLeast(governance.Contributor) {
		// 403 rather than 409: the request is fine, this account is not part of the
		// decision. A member below contributor has read-standing only.
		return fmt.Errorf("%w: %w", ErrPerm, ErrNotProjectMember)
	}
	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `INSERT OR IGNORE INTO merge_approvals (mr_id, user_id, weight, created_at)
		VALUES (?, ?, 1.0, ?)`, mrID, userID, now)
	return err
}

func (d *DB) GetMergeApprovals(ctx context.Context, mrID int64) ([]MergeApproval, error) {
	rows, err := d.QueryContext(ctx, `SELECT mr_id, user_id, weight, created_at FROM merge_approvals WHERE mr_id=?`, mrID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var approvals []MergeApproval
	for rows.Next() {
		var a MergeApproval
		if err := rows.Scan(&a.MRID, &a.UserID, &a.Weight, &a.CreatedAt); err != nil {
			return nil, err
		}
		approvals = append(approvals, a)
	}
	return approvals, rows.Err()
}

// ExecuteMerge lands the change, and is where §5.7's gate lives.
//
// The quorum belongs on the write path, next to the state change it guards, so no
// caller can route around it -- CloseConsensusCall sets the same precedent for the
// consensus quorum. Three rules, none of which existed:
//
//  1. the merge request must belong to the project named in the request. projectID
//     was previously used only as the audit row's project, so a member of any
//     readable project could execute any merge request by id and have the record
//     filed under their own project;
//  2. the gate must pass. When the charter dropped the quorum the merge is legal
//     and the audit row says so, because §5.7 allows the exception only "every such
//     merge is logged".
//
// Who may press the button -- a member with role ≥ contributor, never an outsider --
// is a role question the HTTP layer answers with requireRole, because this signature
// carries no actor. The gate is deliberately not delegable that way.
func (d *DB) ExecuteMerge(ctx context.Context, mrID int64, projectID int64) error {
	mr, err := d.GetMergeRequest(ctx, mrID)
	if err != nil {
		return err
	}
	if mr.ProjectID != projectID {
		// Indistinguishable from a merge request that does not exist, the way every
		// other project-scoped read behaves: a 403 here would confirm that the id is
		// real somewhere else.
		return fmt.Errorf("%w: merge request %d", ErrNotFound, mrID)
	}
	// open -> merged|rejected, once. Without this a second execute re-ran the whole
	// landing (re-shipping the feature and re-closing complaints that were already
	// closed) and a reject after a merge flipped a landed change to rejected.
	if mr.Status != "open" {
		return fmt.Errorf("%w: this merge request is already %s", ErrConflict, mr.Status)
	}
	gate, err := d.MergeGateFor(ctx, mrID)
	if err != nil {
		return err
	}
	if !gate.Passed {
		return fmt.Errorf("%w: %w: %s", ErrConflict, ErrMergeGateNotMet, gate.shortfall())
	}
	detail := "merged"
	if !gate.RequiresQuorum {
		detail = "merged without a quorum (the charter dropped it)"
	}
	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `UPDATE merge_requests SET status='merged', closed_at=? WHERE id=?`, now, mrID)
	if err != nil {
		return err
	}
	if err := d.UpdateFeatureStatus(ctx, mr.FeatureID, "shipped"); err != nil {
		return err
	}
	complaints, err := d.GetFeatureComplaints(ctx, mr.FeatureID)
	if err != nil {
		return err
	}
	for _, c := range complaints {
		_ = d.CloseComplaint(ctx, c.ID)
	}
	return d.AddAudit(ctx, projectID, 0, "merge", "merge_request", mrID, detail)
}

func (d *DB) RejectMerge(ctx context.Context, mrID int64, projectID int64) error {
	mr, err := d.GetMergeRequest(ctx, mrID)
	if err != nil {
		return err
	}
	if mr.ProjectID != projectID {
		return fmt.Errorf("%w: merge request %d", ErrNotFound, mrID)
	}
	if mr.Status != "open" {
		return fmt.Errorf("%w: this merge request is already %s", ErrConflict, mr.Status)
	}
	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `UPDATE merge_requests SET status='rejected', closed_at=? WHERE id=?`, now, mrID)
	if err != nil {
		return err
	}
	return d.AddAudit(ctx, projectID, 0, "merge_reject", "merge_request", mrID, "rejected")
}

// ListMergeRequests returns a project's merge requests, newest first.
//
// There was no read side: the table had CreateMergeRequest, GetMergeRequest and the
// approve/execute/reject transitions, and nothing that listed them. So a merge-request
// page could not exist, and the four write endpoints had no list to appear on.
//
// external_ref and closed_at are nullable and both are already *string/*float64 on
// the struct, so they scan straight through. status is NOT NULL.
//
// Empty, never nil.
func (d *DB) ListMergeRequests(ctx context.Context, projectID int64) ([]MergeRequest, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, project_id, feature_id, author_id, title, external_ref, status,
			opened_at, closed_at
		FROM merge_requests WHERE project_id=? ORDER BY opened_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MergeRequest{}
	for rows.Next() {
		var m MergeRequest
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.FeatureID, &m.AuthorID, &m.Title,
			&m.ExternalRef, &m.Status, &m.OpenedAt, &m.ClosedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
