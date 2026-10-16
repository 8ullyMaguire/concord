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

// §6.2's third steering input: tag-scoped weights that a project may change only by
// consensus, and only for a while.
//
// THE PILLAR ARRIVED AS DATA AND STAYED THAT WAY. `0012` created `strategic_themes`
// (with the `expires_at NOT NULL` the comment below explains) and
// `strategic_theme_proposals`, and then no code path in any revision ever wrote a row:
// no create route, no consent route, no call to `ThemeWeightTotal` from the ranking, and
// a proposal table with no `proposed_by` column -- so the consent rule every other
// decision here enforces ("§5.1: nobody votes on their own item") could not even be
// expressed against it. `0012`'s own comment says themes "expire at the next release
// cycle unless renewed"; what it did not have was a number for a release cycle.
//
// What this file adds: propose → consent → ratify, one live row per (project, tag,
// namespace), an expiry written from the charter, and the two reads that make the weights
// count. `strategy.go` keeps the per-FEATURE weight proposals; the two mechanisms both
// feed §6.2's μ term, which is `docs/QUESTIONS.md` Q2 rather than a house decision.

// StrategicThemeProposal is a pending or decided change to a tag's theme weight.
type StrategicThemeProposal struct {
	ID             int64  `json:"id"`
	ProjectID      int64  `json:"project_id"`
	ThemeTag       string `json:"theme_tag"`
	ThemeNamespace string `json:"theme_namespace"`
	ProposedBy     int64  `json:"proposed_by"`
	// WeightBefore is NULL when the tag has no live theme. NULL rather than 0 because 0 is
	// a legal theme weight (the range is -2..3): a proposal that cannot tell "no theme"
	// from "a theme worth nothing" cannot tell that its target moved.
	WeightBefore *float64 `json:"weight_before"`
	WeightAfter  float64  `json:"weight_after"`
	Rationale    string   `json:"rationale"`
	Status       string   `json:"status"`
	CreatedAt    float64  `json:"created_at"`
	DecidedAt    float64  `json:"decided_at"`
}

// scanThemeProposal fills a proposal from the shared column order every read path uses.
func scanThemeProposal(rows interface{ Scan(...any) error }) (StrategicThemeProposal, error) {
	var p StrategicThemeProposal
	var before sql.NullFloat64
	var decided sql.NullFloat64
	if err := rows.Scan(&p.ID, &p.ProjectID, &p.ThemeTag, &p.ThemeNamespace,
		&p.ProposedBy, &before, &p.WeightAfter, &p.Rationale, &p.Status,
		&p.CreatedAt, &decided); err != nil {
		return p, err
	}
	if before.Valid {
		v := before.Float64
		p.WeightBefore = &v
	}
	if decided.Valid {
		p.DecidedAt = decided.Float64
	}
	return p, nil
}

const themeProposalColumns = `id, project_id, theme_tag, theme_namespace, proposed_by,
	weight_before, weight_after, rationale, status, created_at, decided_at`

// ProposeStrategicTheme opens a proposal to set a tag's theme weight.
//
// Refuses when a pending proposal already exists for the same (tag, namespace), so two
// people cannot open competing changes to one dial and have one ratified by accident --
// the partial unique index is the enforcement point, as it is on the weight side.
//
// The weight is NOT applied here. A theme that took effect on proposal would be the
// hand-steering §6.2's consensus route exists to prevent, and the handler's test asserts
// exactly that the live themes are unchanged after a 201.
func (d *DB) ProposeStrategicTheme(ctx context.Context, projectID, userID int64, tag, namespace string, weight float64, rationale string) (StrategicThemeProposal, error) {
	if weight < ThemeWeightMin || weight > ThemeWeightMax {
		return StrategicThemeProposal{}, fmt.Errorf("%w: theme weight must be between %g and %g",
			ErrInvalid, ThemeWeightMin, ThemeWeightMax)
	}
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" {
		return StrategicThemeProposal{}, fmt.Errorf("%w: a theme needs a tag", ErrInvalid)
	}
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		namespace = "topic"
	}
	rationale = strings.TrimSpace(rationale)
	if rationale == "" {
		// Same rule as the weight proposals: a weight change with no stated reason is the
		// steering lever this route exists to remove.
		return StrategicThemeProposal{}, fmt.Errorf("%w: a rationale is required", ErrInvalid)
	}
	// §4.3: a tag that does not exist is an error naming the proposal route, not a side
	// effect. The theme is the same shape of thing -- a weight for a tag -- so it gets the
	// same rule: inventing taxonomy through a steering dial is how the global tag list
	// became a write-only surface the first time.
	if _, err := d.globalTagID(ctx, tag); err != nil {
		// Bad request rather than 404, for the reason AddFeatureTag records: the route
		// exists and the tag in the body does not.
		return StrategicThemeProposal{}, fmt.Errorf(
			"%w: no such tag -- propose it with POST /api/v1/taxonomy/proposals (action=create)",
			ErrInvalid)
	}

	now := float64(time.Now().Unix())
	before, err := d.liveThemeWeight(ctx, projectID, tag, namespace)
	if err != nil {
		return StrategicThemeProposal{}, err
	}
	res, err := d.ExecContext(ctx, `
		INSERT INTO strategic_theme_proposals
			(project_id, theme_tag, theme_namespace, proposed_by, weight_before,
			 weight_after, rationale, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?)`,
		projectID, tag, namespace, userID, before, weight, rationale, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") ||
			strings.Contains(err.Error(), "idx_theme_one_pending") {
			return StrategicThemeProposal{}, fmt.Errorf(
				"%w: a proposal is already pending for this tag", ErrDuplicate)
		}
		return StrategicThemeProposal{}, err
	}
	id, _ := res.LastInsertId()
	_ = d.AddAudit(ctx, projectID, userID, "propose_strategic_theme", "theme", id,
		fmt.Sprintf("proposal %d: tag %q → %g (%s)", id, tag, weight, rationale))
	return d.GetStrategicThemeProposal(ctx, id)
}

// liveThemeWeight reads the tag's weight if it is in force, or NULL if it is not.
func (d *DB) liveThemeWeight(ctx context.Context, projectID int64, tag, namespace string) (any, error) {
	var w float64
	err := d.QueryRowContext(ctx, `
		SELECT weight FROM strategic_themes
		WHERE project_id = ? AND tag = ? AND namespace = ? AND expires_at > ?`,
		projectID, tag, namespace, float64(time.Now().Unix())).Scan(&w)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return w, nil
}

// GetStrategicThemeProposal reads one proposal.
func (d *DB) GetStrategicThemeProposal(ctx context.Context, id int64) (StrategicThemeProposal, error) {
	row := d.QueryRowContext(ctx, `SELECT `+themeProposalColumns+`
		FROM strategic_theme_proposals WHERE id = ?`, id)
	p, err := scanThemeProposal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("%w: theme proposal %d", ErrNotFound, id)
	}
	return p, err
}

// ListStrategicThemeProposals returns a project's theme proposals, newest first.
func (d *DB) ListStrategicThemeProposals(ctx context.Context, projectID int64) ([]StrategicThemeProposal, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+themeProposalColumns+`
		FROM strategic_theme_proposals WHERE project_id = ? ORDER BY id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// An empty set is an empty ARRAY, not null: a client that reads null has to special-case
	// a shape the API never promised, and the repo already has a gate for this.
	out := []StrategicThemeProposal{}
	for rows.Next() {
		p, err := scanThemeProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ConsentStrategicTheme records one consent, and applies the theme once the charter's
// quorum is reached. §6.2's "changed only by consensus (Normal tier)".
//
// Mirrors RatifyStrategicWeightProposal clause for clause, deliberately: one ratification
// path rather than two that drift. The clauses that carry weight:
//
//   - the proposer cannot consent to their own proposal (§5.1). Without it, a
//     one-collaborator project -- where QuorumThreshold caps to the eligible count, so
//     quorum is 1 -- ratifies on the single consent of the person who wanted it, which is
//     the maintainer-only dial this mechanism replaced, one slower step later.
//   - the consenter must be an eligible collaborator (§8.2). With an eligible count of 0,
//     QuorumThreshold returns 0, so the same single-consent ratification arrives by a
//     different door.
//
// Returns the proposal and the result string. Below quorum it returns ErrDuplicate
// carrying the count, which the API answers as 202: "recorded" and "ratified" are
// different answers and collapsing them reports a weight change that has not happened.
func (d *DB) ConsentStrategicTheme(ctx context.Context, proposalID, userID int64) (StrategicThemeProposal, string, error) {
	p, err := d.GetStrategicThemeProposal(ctx, proposalID)
	if err != nil {
		return p, "", err
	}
	if p.Status != "pending" {
		return p, "", fmt.Errorf("%w: proposal %d is %s", ErrInvalid, proposalID, p.Status)
	}
	if p.ProposedBy == userID {
		return p, "", fmt.Errorf("%w: the proposer cannot consent to their own proposal", ErrPerm)
	}
	charter, err := d.GetCharterForProject(ctx, p.ProjectID)
	if err != nil {
		return p, "", err
	}
	ok, err := d.IsEligibleCollaborator(ctx, p.ProjectID, userID)
	if err != nil {
		return p, "", err
	}
	if !ok {
		return p, "", fmt.Errorf("%w: only an eligible collaborator can consent to a proposal", ErrPerm)
	}
	eligible, err := d.EligibleCollaboratorCount(ctx, p.ProjectID)
	if err != nil {
		return p, "", err
	}
	consents, err := d.themeProposalConsents(ctx, proposalID)
	if err != nil {
		return p, "", err
	}
	counts := governance.ConsensusCounts{
		Consent: consents + 1, Participants: consents + 1, Eligible: eligible,
	}
	if err := d.recordThemeConsent(ctx, proposalID, userID); err != nil {
		return p, "", err
	}

	result := governance.EvaluateConsensus(counts, charter)
	if result != governance.ResultAccepted {
		_ = d.AddAudit(ctx, p.ProjectID, userID, "strategic_theme_consent", "theme", proposalID,
			fmt.Sprintf("proposal %d: %d/%d consents (%s)", proposalID, counts.Consent, counts.Participants, result))
		return p, string(result), fmt.Errorf("%w: proposal %d is %s (%d/%d consents)",
			ErrDuplicate, proposalID, result, counts.Consent, counts.Participants)
	}

	if err := d.applyTheme(ctx, p, userID, charter.ThemeLifetimeDays); err != nil {
		return p, string(result), err
	}
	now := float64(time.Now().Unix())
	if _, err := d.ExecContext(ctx, `UPDATE strategic_theme_proposals
		SET status='ratified', decided_at=? WHERE id=?`, now, proposalID); err != nil {
		return p, string(result), err
	}
	_ = d.AddAudit(ctx, p.ProjectID, userID, "ratify_strategic_theme", "theme", proposalID,
		fmt.Sprintf("proposal %d ratified by quorum: tag %q → %g for %g days",
			proposalID, p.ThemeTag, p.WeightAfter, charter.ThemeLifetimeDays))
	out, err := d.GetStrategicThemeProposal(ctx, proposalID)
	return out, string(result), err
}

// applyTheme puts a ratified theme in force: one live row per (project, tag, namespace),
// with an expiry taken from the charter's release cycle.
//
// Upsert rather than insert, because a RENEWAL is the same act: ratifying a second change
// to a tag already in force must move its `expires_at` forward, not create a second row
// that ThemeWeightTotal would then count twice. §366's "unless renewed" needs no separate
// mechanism -- it is this statement.
func (d *DB) applyTheme(ctx context.Context, p StrategicThemeProposal, userID int64, lifetimeDays float64) error {
	if lifetimeDays <= 0 {
		// The charter's own bound, restated where it is used: a zero lifetime would write a
		// row that is born expired -- ratified, listed by nothing, and worth 0.
		return fmt.Errorf("%w: theme_lifetime_days must be positive, got %v", ErrInvalid, lifetimeDays)
	}
	now := float64(time.Now().Unix())
	expires := now + lifetimeDays*86400
	_, err := d.ExecContext(ctx, `
		INSERT INTO strategic_themes
			(project_id, tag, namespace, weight, rationale, set_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, tag, namespace) DO UPDATE SET
			weight = excluded.weight,
			rationale = excluded.rationale,
			set_by = excluded.set_by,
			expires_at = excluded.expires_at`,
		p.ProjectID, p.ThemeTag, p.ThemeNamespace, p.WeightAfter, p.Rationale, userID, now, expires)
	if err != nil {
		return err
	}
	return d.AddAudit(ctx, p.ProjectID, userID, "set_strategic_theme", "theme", p.ID,
		fmt.Sprintf("tag %q is now %g until %s", p.ThemeTag, p.WeightAfter,
			time.Unix(int64(expires), 0).UTC().Format("2006-01-02")))
}

// WithdrawStrategicThemeProposal cancels a pending proposal. The proposer may withdraw
// their own; a maintainer may withdraw any, which is cleanup rather than steering because a
// withdrawn proposal changes no weight.
func (d *DB) WithdrawStrategicThemeProposal(ctx context.Context, proposalID, userID int64, isMaintainer bool) error {
	p, err := d.GetStrategicThemeProposal(ctx, proposalID)
	if err != nil {
		return err
	}
	if p.Status != "pending" {
		return fmt.Errorf("%w: proposal %d is %s", ErrInvalid, proposalID, p.Status)
	}
	if p.ProposedBy != userID && !isMaintainer {
		return fmt.Errorf("%w: only the proposer or a maintainer may withdraw a proposal", ErrPerm)
	}
	now := float64(time.Now().Unix())
	if _, err := d.ExecContext(ctx, `UPDATE strategic_theme_proposals
		SET status='withdrawn', decided_at=? WHERE id=?`, now, proposalID); err != nil {
		return err
	}
	return d.AddAudit(ctx, p.ProjectID, userID, "withdraw_strategic_theme", "theme", proposalID,
		fmt.Sprintf("proposal %d withdrawn", proposalID))
}

// themeProposalConsents counts distinct collaborators who have consented.
func (d *DB) themeProposalConsents(ctx context.Context, proposalID int64) (int, error) {
	var n int
	err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM strategic_theme_consents WHERE proposal_id = ?`,
		proposalID).Scan(&n)
	return n, err
}

// recordThemeConsent records one collaborator's consent. Re-consenting is a no-op rather
// than an error, and the primary key makes double counting impossible.
func (d *DB) recordThemeConsent(ctx context.Context, proposalID, userID int64) error {
	now := float64(time.Now().Unix())
	_, err := d.ExecContext(ctx, `
		INSERT INTO strategic_theme_consents (proposal_id, user_id, consented_at)
		VALUES (?, ?, ?)
		ON CONFLICT(proposal_id, user_id) DO NOTHING`,
		proposalID, userID, now)
	return err
}

// ListStrategicThemes returns a project's live themes. Expired themes are excluded: they no
// longer contribute a weight, and listing them as current would misrepresent the roadmap.
//
// The filter is the whole of the expiry mechanism. There is no sweeper -- nothing in this
// server runs on a clock -- and a lapsed weight is only wrong when it is read, so it is
// enforced at every read that contributes weight (this one and ThemeWeightTotal). The rows
// stay: the row plus the audit log is the public changelog §366 asks for, and a deleted row
// could not answer "what was in force in March".
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
	out := []StrategicTheme{}
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

// ThemeWeightTotal sums the live theme weights a feature's tags match: §6.2's
// `theme_weight`, the third steering input, and the reason this file exists.
//
// This is additive rather than multiplicative: two matching themes both apply, because the
// alternative (taking the max) silently discards a theme somebody got ratified and makes the
// sum unpredictable.
//
// It had NO CALLER for the life of the pillar -- the function that computes the spec's
// steering input, written, correct, and unreachable -- which is how a mechanism can be
// simultaneously implemented and absent.
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
