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

// Strategic weight is a steering input to the priority formula (§6.2), so
// changing it is a governance decision rather than housekeeping.
//
// The handler used to require only the maintainer role, which meant a maintainer
// could pin a feature to the top of the roadmap by hand — precisely what §5.3
// forbids. §6.2 replaces that dial with a proposal that eligible collaborators
// ratify, and with strategic themes (tag-scoped weights that expire) as the
// actual steering mechanism.

// StrategicWeightProposal is a pending or decided request to change a feature's
// strategic weight.
type StrategicWeightProposal struct {
	ID           int64   `json:"id"`
	ProjectID    int64   `json:"project_id"`
	FeatureID    int64   `json:"feature_id"`
	ProposedBy   int64   `json:"proposed_by"`
	WeightBefore float64 `json:"weight_before"`
	WeightAfter  float64 `json:"weight_after"`
	Rationale    string  `json:"rationale"`
	Status       string  `json:"status"`
	CreatedAt    float64 `json:"created_at"`
	// DecidedAt is 0 while the proposal is pending; the column is nullable and
	// scanned through sql.NullFloat64, because a NULL cannot be read into a
	// float64 and every pending proposal has one.
	DecidedAt float64 `json:"decided_at"`
}

// scan fills a proposal from the shared column order used by every read path.
func scanStrategicWeightProposal(rows interface{ Scan(...any) error }) (StrategicWeightProposal, error) {
	var p StrategicWeightProposal
	var decided sql.NullFloat64
	err := rows.Scan(&p.ID, &p.ProjectID, &p.FeatureID, &p.ProposedBy, &p.WeightBefore,
		&p.WeightAfter, &p.Rationale, &p.Status, &p.CreatedAt, &decided)
	if decided.Valid {
		p.DecidedAt = decided.Float64
	}
	return p, err
}

// StrategicTheme is a tag-scoped strategic weight with an expiry (§6.2).
//
// The expiry is not decoration: a weight that never lapses is permanent
// aristocracy under a different name, and §6.2 requires themes to expire at the
// next release cycle unless renewed.
type StrategicTheme struct {
	ID        int64   `json:"id"`
	ProjectID int64   `json:"project_id"`
	Tag       string  `json:"tag"`
	Namespace string  `json:"namespace"`
	Weight    float64 `json:"weight"`
	Rationale string  `json:"rationale"`
	SetBy     int64   `json:"set_by"`
	CreatedAt float64 `json:"created_at"`
	ExpiresAt float64 `json:"expires_at"`
	ThemeKey  string  `json:"-"`
}

// ValidThemeWeight bounds a theme's weight. A theme large enough to dominate
// the priority formula would reintroduce the hand-steering this whole
// mechanism exists to prevent, so the ceiling is the point rather than a
// formality.
const (
	ThemeWeightMin   = -2.0
	ThemeWeightMax   = 3.0
	FeatureWeightMin = 0.0
	FeatureWeightMax = 3.0
)

// ErrProposalStale is returned when the target changed after the proposal was
// opened, so applying it would layer one decision on top of another.
var ErrProposalStale = errors.New("proposal is stale: the target changed since it was opened")

// ProposeStrategicWeight opens a proposal to change a feature's weight.
//
// Refuses when a pending proposal already exists for the feature, so two people
// cannot open competing changes to the same dial and have one ratified by
// accident. The recorded weight_before is what makes a later ratification
// able to detect that the target moved.
func (d *DB) ProposeStrategicWeight(ctx context.Context, projectID, featureID, userID int64, weight float64, rationale string) (StrategicWeightProposal, error) {
	if weight < FeatureWeightMin || weight > FeatureWeightMax {
		return StrategicWeightProposal{}, fmt.Errorf("%w: weight must be between %g and %g",
			ErrInvalid, FeatureWeightMin, FeatureWeightMax)
	}
	f, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return StrategicWeightProposal{}, err
	}
	if f.ProjectID != projectID {
		return StrategicWeightProposal{}, fmt.Errorf("%w: feature %d is not in project %d",
			ErrInvalid, featureID, projectID)
	}
	rationale = strings.TrimSpace(rationale)
	if rationale == "" {
		// A weight change with no stated reason is the steering lever the
		// consensus route exists to remove, so it is refused outright rather
		// than allowed with an empty justification.
		return StrategicWeightProposal{}, fmt.Errorf("%w: a rationale is required", ErrInvalid)
	}

	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO strategic_weight_proposals
			(project_id, feature_id, proposed_by, weight_before, weight_after,
			 rationale, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', ?)`,
		projectID, featureID, userID, f.StrategicWeight, weight, rationale, now)
	if err != nil {
		// The partial unique index is the enforcement point; translate the
		// driver error into the domain error callers can present.
		if strings.Contains(err.Error(), "UNIQUE constraint failed") ||
			strings.Contains(err.Error(), "idx_stratweight_one_pending") {
			return StrategicWeightProposal{}, fmt.Errorf("%w: a proposal is already pending for this feature", ErrDuplicate)
		}
		return StrategicWeightProposal{}, err
	}
	id, _ := res.LastInsertId()
	_ = d.AddAudit(ctx, projectID, userID, "propose_strategic_weight", "feature", featureID,
		fmt.Sprintf("proposal %d: %g → %g (%s)", id, f.StrategicWeight, weight, rationale))
	return d.GetStrategicWeightProposal(ctx, id)
}

// GetStrategicWeightProposal reads one proposal.
func (d *DB) GetStrategicWeightProposal(ctx context.Context, id int64) (StrategicWeightProposal, error) {
	p, err := scanStrategicWeightProposal(d.QueryRowContext(ctx, `
		SELECT id, project_id, feature_id, proposed_by, weight_before,
		       weight_after, rationale, status, created_at, decided_at
		FROM strategic_weight_proposals WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return StrategicWeightProposal{}, fmt.Errorf("%w: proposal %d", ErrNotFound, id)
	}
	return p, err
}

// ListStrategicWeightProposals returns a project's proposals, newest first.
func (d *DB) ListStrategicWeightProposals(ctx context.Context, projectID int64) ([]StrategicWeightProposal, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, project_id, feature_id, proposed_by, weight_before,
		       weight_after, rationale, status, created_at, decided_at
		FROM strategic_weight_proposals
		WHERE project_id = ? ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StrategicWeightProposal
	for rows.Next() {
		p, err := scanStrategicWeightProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RatifyStrategicWeightProposal applies a proposal once enough eligible
// collaborators have consented, using the same charter thresholds as any other
// consensus decision (§6.6).
//
// The weight is applied only when the feature's current weight still equals
// weight_before. Two ratified proposals for the same feature would otherwise
// each write in the dark, and the second would silently discard the first's
// effect.
func (d *DB) RatifyStrategicWeightProposal(ctx context.Context, proposalID, userID int64) (StrategicWeightProposal, error) {
	p, err := d.GetStrategicWeightProposal(ctx, proposalID)
	if err != nil {
		return StrategicWeightProposal{}, err
	}
	if p.Status != "pending" {
		return StrategicWeightProposal{}, fmt.Errorf("%w: proposal %d is %s", ErrInvalid, proposalID, p.Status)
	}
	// §5.1: nobody votes on their own items. Without this the proposer consents
	// to their own proposal, and in a one-collaborator project -- where the
	// quorum floor is capped down to the eligible count, so quorum is 1 -- that
	// single consent ratifies it. The proposal path would then be a slower way
	// of doing exactly what the maintainer-only PUT used to do.
	if p.ProposedBy == userID {
		return p, fmt.Errorf("%w: the proposer cannot consent to their own proposal", ErrPerm)
	}

	charter, err := d.GetCharterForProject(ctx, p.ProjectID)
	if err != nil {
		return StrategicWeightProposal{}, err
	}
	// The consenter must themselves be an eligible collaborator. Without this a
	// freshly-registered account that merely joined the project could supply the
	// deciding consent -- and §8.2's role-and-activity test exists precisely so
	// that arriving is not the same as participating.
	//
	// This is not a formality. With a project whose eligible count is 0,
	// QuorumThreshold returns 0, so the single consent below satisfies quorum and
	// ratifies the proposal on its own: the exact outcome the proposer-cannot-consent
	// guard above exists to prevent, reached by a different route.
	ok, err := d.IsEligibleCollaborator(ctx, p.ProjectID, userID)
	if err != nil {
		return StrategicWeightProposal{}, err
	}
	if !ok {
		return p, fmt.Errorf("%w: only an eligible collaborator can consent to a proposal", ErrPerm)
	}
	eligible, err := d.EligibleCollaboratorCount(ctx, p.ProjectID)
	if err != nil {
		return StrategicWeightProposal{}, err
	}

	// Ratification uses a lightweight consent record keyed on the proposal, so
	// it does not need its own positions table: a consent is one row and the
	// audit log records who did it.
	consents, err := d.strategyProposalConsents(ctx, proposalID)
	if err != nil {
		return StrategicWeightProposal{}, err
	}
	counts := governance.ConsensusCounts{
		Consent: consents + 1, Participants: consents + 1, Eligible: eligible,
	}
	if err := d.recordStrategyConsent(ctx, proposalID, userID); err != nil {
		return StrategicWeightProposal{}, err
	}

	result := governance.EvaluateConsensus(counts, charter)
	if result != governance.ResultAccepted {
		_ = d.AddAudit(ctx, p.ProjectID, userID, "strategic_weight_consent", "feature", p.FeatureID,
			fmt.Sprintf("proposal %d: %d/%d consents (%s)", proposalID, counts.Consent, counts.Participants, result))
		return p, fmt.Errorf("%w: proposal %d is %s (%d/%d consents)", ErrDuplicate, proposalID, result, counts.Consent, counts.Participants)
	}

	f, err := d.GetFeature(ctx, p.FeatureID)
	if err != nil {
		return StrategicWeightProposal{}, err
	}
	if f.StrategicWeight != p.WeightBefore {
		now := float64(time.Now().Unix())
		_, _ = d.ExecContext(ctx, `UPDATE strategic_weight_proposals
			SET status='stale', decided_at=? WHERE id=?`, now, proposalID)
		_ = d.AddAudit(ctx, p.ProjectID, userID, "strategic_weight_stale", "feature", p.FeatureID,
			fmt.Sprintf("proposal %d: expected weight %g, found %g", proposalID, p.WeightBefore, f.StrategicWeight))
		return StrategicWeightProposal{}, ErrProposalStale
	}

	if err := d.SetStrategicWeight(ctx, p.FeatureID, p.WeightAfter); err != nil {
		return StrategicWeightProposal{}, err
	}
	now := float64(time.Now().Unix())
	if _, err := d.ExecContext(ctx, `UPDATE strategic_weight_proposals
		SET status='ratified', decided_at=? WHERE id=?`, now, proposalID); err != nil {
		return StrategicWeightProposal{}, err
	}
	_ = d.AddAudit(ctx, p.ProjectID, userID, "ratify_strategic_weight", "feature", p.FeatureID,
		fmt.Sprintf("proposal %d ratified by quorum: %g → %g", proposalID, p.WeightBefore, p.WeightAfter))
	return d.GetStrategicWeightProposal(ctx, proposalID)
}

// WithdrawStrategicWeightProposal cancels a pending proposal. The proposer may
// withdraw their own; a maintainer may withdraw any, which is cleanup rather
// than steering because a withdrawn proposal changes no weight.
func (d *DB) WithdrawStrategicWeightProposal(ctx context.Context, proposalID, userID int64, isMaintainer bool) error {
	p, err := d.GetStrategicWeightProposal(ctx, proposalID)
	if err != nil {
		return err
	}
	if p.Status != "pending" {
		return fmt.Errorf("%w: proposal %d is %s", ErrInvalid, proposalID, p.Status)
	}
	if p.ProposedBy != userID && !isMaintainer {
		return ErrPerm
	}
	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `UPDATE strategic_weight_proposals
		SET status='withdrawn', decided_at=? WHERE id=?`, now, proposalID)
	if err != nil {
		return err
	}
	return d.AddAudit(ctx, p.ProjectID, userID, "withdraw_strategic_weight", "feature", p.FeatureID,
		fmt.Sprintf("proposal %d withdrawn", proposalID))
}

// strategyProposalConsents counts distinct collaborators who have consented.
//
// Counted from a dedicated table rather than by scanning audit_log: audit
// detail is JSON, so matching on a proposal id means a LIKE over encoded text
// with no index to help, and counting distinct actors is the actual question.
// The audit row is still written, because the audit log is the append-only
// record every decision must leave.
func (d *DB) strategyProposalConsents(ctx context.Context, proposalID int64) (int, error) {
	var n int
	err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM strategic_weight_consents WHERE proposal_id = ?`,
		proposalID).Scan(&n)
	return n, err
}

// recordStrategyConsent records one collaborator's consent. Re-consenting is a
// no-op rather than an error: a collaborator refreshing their position should
// not be punished, and the unique key makes double counting impossible.
func (d *DB) recordStrategyConsent(ctx context.Context, proposalID, userID int64) error {
	now := float64(time.Now().Unix())
	_, err := d.ExecContext(ctx, `
		INSERT INTO strategic_weight_consents (proposal_id, user_id, consented_at)
		VALUES (?, ?, ?)
		ON CONFLICT(proposal_id, user_id) DO NOTHING`,
		proposalID, userID, now)
	return err
}

// ListStrategicThemes returns a project's live themes. Expired themes are
// excluded: they no longer contribute a weight, and listing them as current
// would misrepresent the roadmap.
func (d *DB) ListStrategicThemes(ctx context.Context, projectID int64) ([]StrategicTheme, error) {
	now := float64(time.Now().Unix())
	rows, err := d.QueryContext(ctx, `
		SELECT id, project_id, tag, namespace, weight, rationale, set_by,
		       created_at, expires_at
		FROM strategic_themes
		WHERE project_id = ? AND expires_at > ?
		ORDER BY ABS(weight) DESC`, projectID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StrategicTheme
	for rows.Next() {
		var t StrategicTheme
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Tag, &t.Namespace, &t.Weight,
			&t.Rationale, &t.SetBy, &t.CreatedAt, &t.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ThemeWeightTotal sums the live theme weights a feature's tags match.
//
// This is additive rather than multiplicative: two matching themes both apply,
// because the alternative (taking the max) silently discards a theme somebody
// got ratified and makes the sum unpredictable.
func (d *DB) ThemeWeightTotal(ctx context.Context, projectID int64, tags []string) (float64, error) {
	if len(tags) == 0 {
		return 0, nil
	}
	themes, err := d.ListStrategicThemes(ctx, projectID)
	if err != nil {
		return 0, err
	}
	want := make(map[string]bool, len(tags))
	for _, t := range tags {
		want[strings.ToLower(t)] = true
	}
	total := 0.0
	for _, th := range themes {
		if want[strings.ToLower(th.Tag)] {
			total += th.Weight
		}
	}
	return total, nil
}
