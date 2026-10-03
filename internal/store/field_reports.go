package store

// Field reports (spec revision 4 §4.7, milestone R5).
//
// "A field report is a structured experience record, designed to replace the
// rants and 'does anyone use X?' threads."
//
// That sentence is the design constraint. A free-text outcome field would BE the
// rant the section rules out — 'works fine mostly' parses, reads as evidence,
// and cannot be aggregated. So outcome is one of four values, and a report that
// does not have one is refused.
//
// Three properties from §4.7 that shape the code:
//
//   - "Owners can respond but cannot delete reports." The response's owner flag
//     is read from the membership table at write time, never from the request.
//   - "Abusive or fabricated reports are removed by quorum with appeal." So a
//     report is flagged, never deleted: a DELETE would destroy the evidence the
//     appeal is conducted on.
//   - "They are weighted by reputation and by report quality." The weight is
//     GetReputation, which is the same source solution coverage ranking uses.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Field report outcomes (§4.7's four).
const (
	ReportWorked    = "worked"
	ReportCaveats   = "worked-with-caveats"
	ReportAbandoned = "abandoned"
	ReportMigrated  = "migrated-away"
)

// CaveatCredit is how much a 'worked with caveats' report counts toward the
// outcome rate.
//
// Named, with its reason, because an unnamed 0.5 is a number nobody can later
// argue with: it reads as a measurement when it is a choice. Caveated success is
// real success and must not rank below abandonment, nor equal clean success --
// the person reading "worked for 83% of reporters" needs to know some of those
// 83% needed a workaround.
const CaveatCredit = 0.5

// MaxReportReputation is the reputation at which a reporter's weight stops
// growing. See reportWeight for why the weight is capped at all.
const MaxReportReputation = 100.0

// outcomeCredit maps each outcome to its contribution to the outcome rate.
func outcomeCredit(outcome string) float64 {
	switch outcome {
	case ReportWorked:
		return 1.0
	case ReportCaveats:
		return CaveatCredit
	default: // abandoned, migrated-away
		return 0.0
	}
}

// FieldReport is one structured experience record.
//
// ProjectSlug is a write-side convenience only — the store resolves it to
// ProjectID before anything is persisted, so every reader sees one identifier
// and a report can never name a project that does not exist.
type FieldReport struct {
	ID            int64     `json:"id"`
	ProjectID     int64     `json:"project_id"`
	ProjectSlug   string    `json:"project_slug,omitempty"`
	UserID        int64     `json:"user_id"`
	Version       string    `json:"version"`
	UseCase       string    `json:"use_case"`
	Environment   string    `json:"environment"`
	Scale         string    `json:"scale"`
	Duration      string    `json:"duration"`
	Outcome       string    `json:"outcome"`
	MigratedTo    string    `json:"migrated_to"`
	Caveats       string    `json:"caveats"`
	Workaround    string    `json:"workaround"`
	Advice        string    `json:"advice"`
	Removed       bool      `json:"removed"`
	RemovedBy     int64     `json:"removed_by"`
	RemovalReason string    `json:"removal_reason"`
	CreatedAt     time.Time `json:"created_at"`
}

// FieldOwnerResponse is a project owner's reply to a report.
type FieldOwnerResponse struct {
	ID        int64     `json:"id"`
	ReportID  int64     `json:"report_id"`
	UserID    int64     `json:"user_id"`
	Body      string    `json:"body"`
	IsOwner   bool      `json:"is_owner"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateFieldReport files a structured report against a project.
//
// The project may be named by ProjectID or by ProjectSlug; giving both requires
// them to agree, because two ways to say which project is one way to file a
// report against a different project than the caller believes.
func (d *DB) CreateFieldReport(ctx context.Context, r FieldReport) (FieldReport, error) {
	if r.UserID == 0 {
		return FieldReport{}, fmt.Errorf("%w: a field report needs an author", ErrInvalid)
	}
	if strings.TrimSpace(r.Outcome) == "" {
		return FieldReport{}, fmt.Errorf(
			"%w: outcome is required and must be one of worked, worked-with-caveats, abandoned, migrated-away",
			ErrInvalid)
	}
	switch r.Outcome {
	case ReportWorked, ReportCaveats, ReportAbandoned, ReportMigrated:
	default:
		return FieldReport{}, fmt.Errorf(
			"%w: outcome %q must be one of worked, worked-with-caveats, abandoned, migrated-away",
			ErrInvalid, r.Outcome)
	}

	// §4.7 says "migrated away (to what?)". The successor is the most useful
	// thing in the report -- it is a second candidate for whoever asked the
	// question -- so a migrated-away report that names none is refused, and the
	// message names the field rather than saying "invalid".
	if r.Outcome == ReportMigrated && strings.TrimSpace(r.MigratedTo) == "" {
		return FieldReport{}, fmt.Errorf(
			"%w: a migrated-away report must name the successor in migrated_to", ErrInvalid)
	}

	projectID := r.ProjectID
	if r.ProjectSlug != "" {
		id, err := d.projectIDBySlug(ctx, r.ProjectSlug)
		if err != nil {
			return FieldReport{}, err
		}
		if projectID != 0 && id != projectID {
			return FieldReport{}, fmt.Errorf(
				"%w: project_id %d is not %q", ErrInvalid, projectID, r.ProjectSlug)
		}
		projectID = id
	}
	if projectID == 0 {
		return FieldReport{}, fmt.Errorf("%w: a field report needs a project", ErrInvalid)
	}

	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO field_reports
			(project_id, user_id, version, use_case, environment, scale, duration,
			 outcome, migrated_to, caveats, workaround, advice, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		projectID, r.UserID, r.Version, r.UseCase, r.Environment, r.Scale, r.Duration,
		r.Outcome, r.MigratedTo, r.Caveats, r.Workaround, r.Advice, now)
	if err != nil {
		return FieldReport{}, fmt.Errorf("create field report: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return FieldReport{}, err
	}
	return d.GetFieldReport(ctx, id)
}

// GetFieldReport returns one report.
func (d *DB) GetFieldReport(ctx context.Context, id int64) (FieldReport, error) {
	row := d.QueryRowContext(ctx, fieldReportSelect+` WHERE id = ?`, id)
	r, err := scanFieldReport(row)
	if errors.Is(err, sql.ErrNoRows) {
		return FieldReport{}, fmt.Errorf("%w: field report %d", ErrNotFound, id)
	}
	return r, err
}

const fieldReportSelect = `
	SELECT id, project_id, user_id, version, use_case, environment, scale, duration,
	       outcome, migrated_to, caveats, workaround, advice,
	       removed, COALESCE(removed_by, 0), removal_reason, created_at
	  FROM field_reports`

func scanFieldReport(sc interface{ Scan(...any) error }) (FieldReport, error) {
	var (
		r  FieldReport
		rm int
		ct float64
	)
	if err := sc.Scan(&r.ID, &r.ProjectID, &r.UserID, &r.Version, &r.UseCase,
		&r.Environment, &r.Scale, &r.Duration, &r.Outcome, &r.MigratedTo,
		&r.Caveats, &r.Workaround, &r.Advice, &rm, &r.RemovedBy,
		&r.RemovalReason, &ct); err != nil {
		return FieldReport{}, err
	}
	r.Removed = rm == 1
	r.CreatedAt = time.Unix(int64(ct), 0)
	return r, nil
}

// ListFieldReports returns a project's reports, newest first.
//
// includeRemoved is false for every surface a reader sees and true only for the
// moderation view — a removed report stays in the table so the removal is
// auditable, which means something has to be the thing that hides it.
func (d *DB) ListFieldReports(ctx context.Context, projectID int64, includeRemoved bool) ([]FieldReport, error) {
	q := fieldReportSelect + ` WHERE project_id = ?`
	if !includeRemoved {
		q += ` AND removed = 0`
	}
	q += ` ORDER BY created_at DESC, id DESC`

	rows, err := d.QueryContext(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []FieldReport{}
	for rows.Next() {
		r, err := scanFieldReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RespondToFieldReport files an owner's reply to a report.
//
// is_owner is resolved from the membership table here. Taking it from the
// request body would let any reporter badge their own report with the authority
// of the project owner, which is the one field on this table whose whole purpose
// is to be trustworthy.
func (d *DB) RespondToFieldReport(ctx context.Context, reportID, userID int64, body string) (FieldOwnerResponse, error) {
	if strings.TrimSpace(body) == "" {
		return FieldOwnerResponse{}, fmt.Errorf("%w: a response needs a body", ErrInvalid)
	}
	report, err := d.GetFieldReport(ctx, reportID)
	if err != nil {
		return FieldOwnerResponse{}, err
	}

	role, err := d.GetRoleForProject(ctx, report.ProjectID, userID)
	if err != nil {
		return FieldOwnerResponse{}, err
	}
	// GetRoleForProject returns the literal "guest" for a non-member, never "".
	// A membership check written `role == ""` therefore never fires — see
	// docs/20-Areas/concord-project-visibility.md, where exactly that mistake
	// left every member-only action open to any signed-in account.
	isOwner := role != "guest" && role != ""

	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO field_report_responses (report_id, user_id, body, is_owner, created_at)
		VALUES (?,?,?,?,?)`,
		reportID, userID, body, boolInt(isOwner), now)
	if err != nil {
		return FieldOwnerResponse{}, fmt.Errorf("respond to field report: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return FieldOwnerResponse{}, err
	}
	return d.getFieldReportResponse(ctx, id)
}

func (d *DB) getFieldReportResponse(ctx context.Context, id int64) (FieldOwnerResponse, error) {
	var (
		resp FieldOwnerResponse
		own  int
		ct   float64
	)
	err := d.QueryRowContext(ctx, `
		SELECT id, report_id, user_id, body, is_owner, created_at
		  FROM field_report_responses WHERE id = ?`, id).
		Scan(&resp.ID, &resp.ReportID, &resp.UserID, &resp.Body, &own, &ct)
	if errors.Is(err, sql.ErrNoRows) {
		return FieldOwnerResponse{}, fmt.Errorf("%w: response %d", ErrNotFound, id)
	}
	if err != nil {
		return FieldOwnerResponse{}, err
	}
	resp.IsOwner = own == 1
	resp.CreatedAt = time.Unix(int64(ct), 0)
	return resp, nil
}

// ListFieldReportResponses returns a report's replies, oldest first, so the
// exchange reads in the order it happened.
func (d *DB) ListFieldReportResponses(ctx context.Context, reportID int64) ([]FieldOwnerResponse, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, report_id, user_id, body, is_owner, created_at
		  FROM field_report_responses WHERE report_id = ?
		 ORDER BY created_at, id`, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []FieldOwnerResponse{}
	for rows.Next() {
		var (
			resp FieldOwnerResponse
			own  int
			ct   float64
		)
		if err := rows.Scan(&resp.ID, &resp.ReportID, &resp.UserID, &resp.Body, &own, &ct); err != nil {
			return nil, err
		}
		resp.IsOwner = own == 1
		resp.CreatedAt = time.Unix(int64(ct), 0)
		out = append(out, resp)
	}
	return out, rows.Err()
}

// RemoveFieldReport flags a report as removed by quorum, with a reason.
//
// §4.7 gives owners the right to respond and explicitly not the right to delete,
// so the one actor refused here is a project member acting as owner. Removal
// keeps the row: the appeal is conducted on the report, and deleting it would
// destroy both the accusation and the defence.
func (d *DB) RemoveFieldReport(ctx context.Context, reportID, by int64, reason string) error {
	report, err := d.GetFieldReport(ctx, reportID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: removing a field report requires a reason", ErrInvalid)
	}

	role, err := d.GetRoleForProject(ctx, report.ProjectID, by)
	if err != nil {
		return err
	}
	if role != "guest" && role != "" {
		return fmt.Errorf("%w: a project owner may respond to a report but not remove it", ErrPerm)
	}

	_, err = d.ExecContext(ctx, `
		UPDATE field_reports SET removed = 1, removed_by = ?, removal_reason = ?
		 WHERE id = ?`, by, reason, reportID)
	if err != nil {
		return fmt.Errorf("remove field report: %w", err)
	}
	return nil
}

// FieldReportOutcomeRate returns a project's reputation-weighted outcome rate
// and the number of live reports behind it.
//
// The sample size is returned because "worked for 100% of reporters" from one
// report and from eleven are different claims, and a caller that only receives
// the rate cannot tell them apart. Every surface that renders a percentage
// should render the denominator next to it.
//
// Weighting uses 1 + reputation, so a reporter with no reputation events still
// counts — a new account filing a real field report should not be erased by the
// weighting — while a long-standing contributor weighs more. Unbounded
// reputation would let one account dominate a project's rate, which is the
// vote-ring shape §5.2 exists to prevent.
func (d *DB) FieldReportOutcomeRate(ctx context.Context, projectID int64) (float64, int, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT r.user_id, r.outcome,
		       COALESCE((SELECT SUM(points) FROM reputation_events e
		                  WHERE e.project_id = r.project_id AND e.user_id = r.user_id), 0)
		  FROM field_reports r
		 WHERE r.project_id = ? AND r.removed = 0`, projectID)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()

	var weighted, weightSum float64
	var n int
	for rows.Next() {
		var (
			uid int64
			out string
			rep float64
		)
		if err := rows.Scan(&uid, &out, &rep); err != nil {
			return 0, 0, err
		}
		w := reportWeight(rep)
		weighted += w * outcomeCredit(out)
		weightSum += w
		n++
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if weightSum == 0 {
		return 0, 0, nil
	}
	return weighted / weightSum, n, nil
}

// reportWeight maps a reporter's reputation onto a vote weight.
//
// 1 + reputation, squashed at 100 points, so:
//   - an account with no reputation events still weighs 1. A new user filing an
//     honest field report must not be erased by the weighting, which is the
//     difference between "weighted" and "only established users count".
//   - reputation is capped, so one heavily-rewarded account cannot swamp a small
//     pool of reporters. Without the cap a single account with 10,000 points
//     would carry 10,001 votes against three reports weighing 1 each, and the
//     "reputation-weighted outcome rate" would be one person's opinion.
func reportWeight(reputation float64) float64 {
	if reputation < 0 {
		reputation = 0
	}
	return 1 + math.Min(reputation, MaxReportReputation)/MaxReportReputation
}

// FieldReportOutcomeRateByEnvironment returns the outcome rate per environment,
// which is the query behind §4.7's "worked for 83% of reporters on arm64".
func (d *DB) FieldReportOutcomeRateByEnvironment(ctx context.Context, projectID int64) (map[string]float64, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT r.environment, r.outcome,
		       COALESCE((SELECT SUM(points) FROM reputation_events e
		                  WHERE e.project_id = r.project_id AND e.user_id = r.user_id), 0)
		  FROM field_reports r
		 WHERE r.project_id = ? AND r.removed = 0`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	weighted := map[string]float64{}
	weights := map[string]float64{}
	for rows.Next() {
		var (
			env string
			out string
			rep float64
		)
		if err := rows.Scan(&env, &out, &rep); err != nil {
			return nil, err
		}
		env = strings.ToLower(strings.TrimSpace(env))
		if env == "" {
			continue
		}
		w := reportWeight(rep)
		weighted[env] += w * outcomeCredit(out)
		weights[env] += w
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[string]float64{}
	for env, w := range weights {
		if w > 0 {
			out[env] = weighted[env] / w
		}
	}
	return out, nil
}
