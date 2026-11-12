// Package store is Concord's SQL data layer.
// This file adds the features domain (PLAN M2).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/governance"
	"git.polarisocial.xyz/concord/concord/internal/ranking"
)

// validEffortSizes is the t-shirt scale from spec §117. Kept as an explicit
// set rather than a range check so "XL" is spelled the same way everywhere.
var validEffortSizes = map[string]bool{"S": true, "M": true, "L": true, "XL": true}

// The intake modes (spec docs/specs/idea-intake-and-milestones-spec.md §3.1).
// Constants rather than literals because the CHECK constraint, the refusal
// message and the tests must agree on the spelling, and a typo in one of them
// is a mode that silently behaves like its neighbour.
const (
	IntakeComplaintFirst = "complaint_first"
	IntakeIdeaFirst      = "idea_first"
	IntakeBoth           = "both"
)

// ValidIntakeModes is what a route may accept, in the order a form shows them.
var ValidIntakeModes = []string{IntakeComplaintFirst, IntakeIdeaFirst, IntakeBoth}

// projectIntakeMode reads the one column that decides §6.2's traceability rule.
// It returns ErrNotFound rather than a zero value for a missing project, so a
// caller cannot mistake "no such project" for "complaint_first".
func (d *DB) projectIntakeMode(ctx context.Context, projectID int64) (string, error) {
	var mode string
	err := d.QueryRowContext(ctx,
		`SELECT intake_mode FROM projects WHERE id = ?`, projectID).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: project %d", ErrNotFound, projectID)
	}
	return mode, err
}

// countFeatureEvidence counts everything that can stand as the reason a feature
// exists: a linked complaint, or an alternative evidence row (a document revision
// or a field report). §3.4: the rule is about EVIDENCE, not about the complaints
// table, so a project documenting itself does not have to restate its own spec
// as pain.
func (d *DB) countFeatureEvidence(ctx context.Context, featureID int64) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM feature_complaints WHERE feature_id = ?)
		     + (SELECT count(*) FROM feature_evidence   WHERE feature_id = ?)`,
		featureID, featureID).Scan(&n)
	return n, err
}

// SetIntakeMode changes a project's intake mode and records the act.
//
// The mode decides whose work counts as legitimate input, so the change is an
// audit act as well as a column: docs/REMAINING.md §42 records that the value and
// the row must not be able to disagree.
func (d *DB) SetIntakeMode(ctx context.Context, projectID, actorID int64, mode string) error {
	valid := false
	for _, m := range ValidIntakeModes {
		if m == mode {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("%w: intake_mode must be one of %v", ErrInvalid, ValidIntakeModes)
	}
	prev, err := d.projectIntakeMode(ctx, projectID)
	if err != nil {
		return err
	}
	if prev == mode {
		return nil
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE projects SET intake_mode = ?, updated_at = ? WHERE id = ?`,
		mode, float64(time.Now().Unix()), projectID); err != nil {
		return err
	}
	return d.AddAudit(ctx, projectID, actorID, "intake_mode", "project", projectID,
		prev+" -> "+mode)
}

// Feature represents a proposed solution linked to complaints.
type Feature struct {
	ID              int64    `json:"id"`
	ProjectID       int64    `json:"project_id"`
	AuthorID        int64    `json:"author_id"`
	Title           string   `json:"title"`
	Body            string   `json:"body"`
	Effort          string   `json:"effort"`
	Impact          *int     `json:"impact"`
	EffortScore     *int     `json:"effort_score"`
	ImpactRatio     *float64 `json:"impact_ratio"`
	Status          string   `json:"status"`
	EloR            float64  `json:"elo_r"`
	EloRD           float64  `json:"elo_rd"`
	EloVol          float64  `json:"elo_vol"`
	StrategicWeight float64  `json:"strategic_weight"`
	// MilestoneID is NULL for the backlog, which is a real answer rather than a
	// missing value: the features no milestone claims ARE the backlog (spec §4.1).
	MilestoneID *int64 `json:"milestone_id"`
	// MilestonePosition orders the feature inside its milestone (§6); 0 means
	// "wherever", which is what almost every row says.
	MilestonePosition int     `json:"milestone_position"`
	CreatedAt         float64 `json:"created_at"`
	UpdatedAt         float64 `json:"updated_at"`
}

// FeaturePriority carries a feature's priority score for the leaderboard.
type FeaturePriority struct {
	ID              int64   `json:"id"`
	Title           string  `json:"title"`
	EloR            float64 `json:"elo_r"`
	EloRD           float64 `json:"elo_rd"`
	PainScore       float64 `json:"pain_score"`
	StrategicWeight float64 `json:"strategic_weight"`
	// ThemeWeight is §6.2's tag-scoped half of the μ term, summed from the live themes the
	// feature's tags match. Reported beside StrategicWeight so the μ term is explainable.
	ThemeWeight   float64 `json:"theme_weight"`
	PriorityScore float64 `json:"priority_score"`
}

// FeatureComplaintLink links a feature to a complaint.
type FeatureComplaintLink struct {
	FeatureID   int64   `json:"feature_id"`
	ComplaintID int64   `json:"complaint_id"`
	LinkedAt    float64 `json:"linked_at"`
}

// CreateFeature inserts a new feature proposal (M2).
// Must be linked to at least one validated complaint.
// CreateFeature records a proposed feature. effort is the t-shirt size
// (spec §117); impact and effortScore are the optional 1-10 judgements that
// produce impact_ratio. Both are validated here rather than trusted from the
// caller: effort is constrained by the schema too, but a clear error at the
// boundary beats a constraint violation surfacing as a 500.
//
// effortScore is accepted separately from effort because they are different
// claims: 'S' is how big the change is, 1-10 is how many points it costs.
// Collapsing them into one field is what made the original column unreadable.
func (d *DB) CreateFeature(ctx context.Context, projectID, authorID int64, title, body, effort string, impact, effortScore *int, linkedComplaints []int64) (Feature, error) {
	if projectID == 0 || authorID == 0 {
		return Feature{}, fmt.Errorf("%w: project_id and author_id are required", ErrInvalid)
	}
	// §6.2's traceability rule is per PROJECT, not per instance (spec §3.1). The
	// old message stated it as the platform's law, which was true while every
	// project was complaint_first and became wrong the moment one could say
	// otherwise -- so a reader in an idea_first project was told to file a
	// complaint the project does not ask for.
	if len(linkedComplaints) == 0 {
		mode, err := d.projectIntakeMode(ctx, projectID)
		if err != nil {
			return Feature{}, err
		}
		if mode == IntakeComplaintFirst {
			return Feature{}, fmt.Errorf(
				"%w: this project is complaint_first, so a feature needs at least one validated "+
					"complaint; a maintainer can change the project's intake mode to idea_first or both",
				ErrInvalid)
		}
	}
	if effort == "" {
		effort = "M"
	}
	if !validEffortSizes[effort] {
		return Feature{}, fmt.Errorf("%w: effort must be one of S, M, L, XL", ErrInvalid)
	}
	for label, v := range map[string]*int{"impact": impact, "effort_score": effortScore} {
		if v != nil && (*v < 1 || *v > 10) {
			return Feature{}, fmt.Errorf("%w: %s must be between 1 and 10", ErrInvalid, label)
		}
	}
	now := float64(time.Now().Unix())
	// Verify complaints are validated before starting transaction
	for _, cid := range linkedComplaints {
		c, err := d.GetComplaint(ctx, cid)
		if err != nil || c.Status != "validated" {
			return Feature{}, fmt.Errorf("%w: complaint %d must be validated", ErrInvalid, cid)
		}
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Feature{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO features (project_id, author_id, title, body, effort, status, elo_r, elo_rd, elo_vol, strategic_weight, impact, effort_score, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'draft', 1500, 350, 0.06, 1.0, ?, ?, ?, ?)`,
		projectID, authorID, title, body, effort, impact, effortScore, now, now)
	if err != nil {
		return Feature{}, err
	}
	featureID, _ := res.LastInsertId()
	// Link to validated complaints
	for _, cid := range linkedComplaints {
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO feature_complaints (feature_id, complaint_id) VALUES (?, ?)`, featureID, cid)
		if err != nil {
			return Feature{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Feature{}, err
	}
	return d.GetFeature(ctx, featureID)
}

// GetFeature retrieves a feature by ID.
func (d *DB) GetFeature(ctx context.Context, id int64) (Feature, error) {
	var f Feature
	err := d.QueryRowContext(ctx, `
		SELECT id, project_id, author_id, title, body, effort, impact, effort_score,
			impact_ratio, status, elo_r, elo_rd, elo_vol, strategic_weight,
			created_at, updated_at, milestone_id, milestone_position
		FROM features WHERE id=?`, id).Scan(
		&f.ID, &f.ProjectID, &f.AuthorID, &f.Title, &f.Body, &f.Effort,
		&f.Impact, &f.EffortScore, &f.ImpactRatio,
		&f.Status, &f.EloR, &f.EloRD, &f.EloVol, &f.StrategicWeight,
		&f.CreatedAt, &f.UpdatedAt, &f.MilestoneID, &f.MilestonePosition)
	if errors.Is(err, sql.ErrNoRows) {
		return Feature{}, fmt.Errorf("%w: feature %d", ErrNotFound, id)
	}
	return f, err
}

// ListFeatures returns features for a project, filtered by status.
func (d *DB) ListFeatures(ctx context.Context, projectID int64, status string) ([]Feature, error) {
	query := `SELECT id, project_id, author_id, title, body, effort, impact, effort_score,
		impact_ratio, status, elo_r, elo_rd, elo_vol, strategic_weight,
		created_at, updated_at, milestone_id, milestone_position
		FROM features WHERE project_id=?`
	var args []any = []any{projectID}
	if status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := d.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var features []Feature
	for rows.Next() {
		var f Feature
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.AuthorID, &f.Title, &f.Body,
			&f.Effort, &f.Impact, &f.EffortScore, &f.ImpactRatio,
			&f.Status, &f.EloR, &f.EloRD, &f.EloVol, &f.StrategicWeight,
			&f.CreatedAt, &f.UpdatedAt, &f.MilestoneID, &f.MilestonePosition); err != nil {
			return nil, err
		}
		features = append(features, f)
	}
	return features, rows.Err()
}

// LinkComplaint links a validated complaint to a feature (M2).

// GetFeaturePriorities returns priority scores for all features
// in a project, sorted by priority descending.
func (d *DB) GetFeaturePriorities(ctx context.Context, projectID int64, charter governance.Charter) ([]FeaturePriority, error) {
	features, err := d.ListFeatures(ctx, projectID, "")
	if err != nil {
		return nil, err
	}
	var result []FeaturePriority
	for _, f := range features {
		// Sum pain across all complaints linked to this feature
		// Collect the linked complaint ids first, then close the rows before
		// querying anything else.
		//
		// db.Open sets SetMaxOpenConns(1): the pool has exactly one connection.
		// Calling GetComplaintPain while rows from feature_complaints is still
		// open asks for a second connection, so the request waits for one that
		// cannot be handed out until the open rows is closed — which happens
		// four lines below. The handler hung for the full request timeout, and
		// because the query is correct the failure looked like a network
		// problem rather than a deadlock.
		//
		// Read the ids into a slice, close rows, then do the per-complaint work.
		var cids []int64
		rows, err := d.QueryContext(ctx, `SELECT complaint_id FROM feature_complaints WHERE feature_id=?`, f.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var cid int64
			if err := rows.Scan(&cid); err == nil {
				cids = append(cids, cid)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}

		var painSum float64
		for _, cid := range cids {
			p, err := d.GetComplaintPain(ctx, cid)
			if err != nil {
				return nil, err
			}
			painSum += p
		}
		// §6.2's third steering input. `priority = … + μ·theme_weight`, and theme_weight is
		// the sum of the two things this project has ratified for this feature: its own
		// weight (§12.3's proposals) and the live themes its TAGS match (§6.2's own words:
		// "tag-based weights such as 'security hardening: +2'").
		//
		// The theme half had no caller at all before this line -- ThemeWeightTotal was
		// written, documented as §6.2's input, and reachable from nothing, so every ranking
		// this server has ever produced had a theme_weight of 0 and the spec's third
		// steering input did not exist. Summing both rather than choosing is recorded as
		// docs/QUESTIONS.md Q2: §366 names only themes, and dropping the per-feature half
		// would make the weight-proposal mechanism inert instead.
		// The feature's tags come from feature_tags, not a column: a feature has no `tags`
		// field, and until this pass nothing could even put a row in that table (0029).
		// So the input the spec's third steering input reads had no writer either --
		// ThemeWeightTotal was unreachable AND unfillable.
		tags, err := d.GetFeatureTags(ctx, f.ID)
		if err != nil {
			return nil, err
		}
		themeWeight, err := d.ThemeWeightTotal(ctx, projectID, tags)
		if err != nil {
			return nil, err
		}
		prio := ranking.PriorityScore(f.EloR, f.EloRD, painSum,
			f.StrategicWeight+themeWeight, charter.Lam, charter.Mu)
		result = append(result, FeaturePriority{
			ID:              f.ID,
			Title:           f.Title,
			EloR:            f.EloR,
			EloRD:           f.EloRD,
			PainScore:       painSum,
			StrategicWeight: f.StrategicWeight,
			// Sent separately as well as summed, because a score a reader cannot take
			// apart is the defect this repository keeps finding: the ranking page can
			// show that a tag is worth +2 and WHICH tag it was.
			ThemeWeight:   themeWeight,
			PriorityScore: prio,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].PriorityScore > result[j].PriorityScore
	})
	return result, nil
}

// LinkComplaint attaches a validated complaint to an existing feature (§354's extra link).
//
// projectID is a parameter rather than something the caller checks because a feature id and a
// complaint id are both GLOBAL: this function's whole body is "turn two ids into a row", and
// without the project it will join a feature in one project to a complaint in another. That is
// the nested-route defect -- a URL that names one id and acts on another -- one layer lower,
// where no route-level check can reach it. The signature changed for this reason and there was
// no caller to update; the next caller inherits the guard instead of having to remember it.
//
// Idempotent: INSERT OR IGNORE, so a retried request leaves one row rather than failing.
func (d *DB) LinkComplaint(ctx context.Context, projectID, featureID, complaintID int64) error {
	f, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return err
	}
	if f.ProjectID != projectID {
		return fmt.Errorf("%w: feature %d is not in project %d", ErrNotFound, featureID, projectID)
	}
	c, err := d.GetComplaint(ctx, complaintID)
	if err != nil {
		return err
	}
	if c.ProjectID != projectID {
		return fmt.Errorf("%w: complaint %d is not in project %d", ErrNotFound, complaintID, projectID)
	}
	if c.Status != "validated" {
		return fmt.Errorf("%w: complaint must be validated to link", ErrInvalid)
	}
	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `INSERT OR IGNORE INTO feature_complaints (feature_id, complaint_id) VALUES (?, ?)`, featureID, complaintID)
	if err != nil {
		return err
	}
	_, err = d.ExecContext(ctx, `UPDATE features SET updated_at=? WHERE id=?`, now, featureID)
	return err
}

// UnlinkComplaint removes one link, the inverse LinkComplaint never had.
//
// That asymmetry was real and it had a cost: an idea filed against the wrong
// complaint, or a feature converted from complaint-backed to idea-first, could be
// moved one way through the browser and not the other. `feature_complaints` is the
// same table `LinkComplaint` writes, so there is exactly one truth per pair.
//
// Policy, taken from RemoveFeatureEvidence rather than invented here: removing the
// last piece of evidence does NOT retroactively un-ready a feature. The traceability
// rule is enforced on the NEXT transition to ready or in_progress
// (UpdateFeatureStatus), so the state "ready with nothing behind it" is reachable for
// a moment and the next move forward is refused with the reason. Silently un-readying
// work somebody is doing, or refusing a legal edit because of a status the author did
// not choose, would both be worse -- and two removal paths in one package enforcing
// different policies is how a rule acquires a bypass.
//
// Idempotent, like LinkComplaint: deleting a link that is not there leaves the same
// state and no error, because the response is the feature's remaining complaints and
// that list is the truth. A retried request must not fail.
//
// projectID is a parameter for the reason LinkComplaint's comment gives: feature and
// complaint ids are both global, so without it this joins a feature in one project to
// a complaint in another and no route-level check can see it.
func (d *DB) UnlinkComplaint(ctx context.Context, projectID, featureID, complaintID int64) error {
	f, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return err
	}
	if f.ProjectID != projectID {
		return fmt.Errorf("%w: feature %d is not in project %d", ErrNotFound, featureID, projectID)
	}
	c, err := d.GetComplaint(ctx, complaintID)
	if err != nil {
		return err
	}
	if c.ProjectID != projectID {
		return fmt.Errorf("%w: complaint %d is not in project %d", ErrNotFound, complaintID, projectID)
	}
	if _, err := d.ExecContext(ctx,
		`DELETE FROM feature_complaints WHERE feature_id=? AND complaint_id=?`,
		featureID, complaintID); err != nil {
		return err
	}
	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `UPDATE features SET updated_at=? WHERE id=?`, now, featureID)
	return err
}

// SetStrategicWeight changes a feature's strategic weight (M2).
// Maintainer+ only. Audited with old→new.
func (d *DB) SetStrategicWeight(ctx context.Context, featureID int64, newWeight float64) error {
	f, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return err
	}
	now := float64(time.Now().Unix())
	_, err = d.ExecContext(ctx, `UPDATE features SET strategic_weight=?, updated_at=? WHERE id=?`, newWeight, now, featureID)
	if err != nil {
		return err
	}
	return d.AddAudit(ctx, f.ProjectID, 0, "set_strategic_weight", "feature", featureID, fmt.Sprintf("weight: %f → %f", f.StrategicWeight, newWeight))
}

// UpdateFeatureStatus applies the two rules that belong where work actually moves:
// §6.2's evidence rule (spec §3.2 item 4) and the dependency rule (spec §6).
//
// They live in the store rather than in the two handlers that call it, because a
// rule enforced at one entry point is a rule with a bypass -- and the board's move
// route, the trust route and any future caller all end up here.
func (d *DB) UpdateFeatureStatus(ctx context.Context, featureID int64, status string) error {
	if status == "ready" || status == "in_progress" {
		if _, err := d.GetFeature(ctx, featureID); err != nil {
			return err
		}
		if status == "ready" {
			// An idea may be FILED without a complaint in an idea_first project
			// (spec §3.1), but it may not be COMMITTED to without evidence.
			n, err := d.countFeatureEvidence(ctx, featureID)
			if err != nil {
				return err
			}
			// NO mode exemption. idea_first frees the FILING (spec §3.1); reaching
			// ready is a commitment to build, and §3.2's gate asks for evidence at
			// that point in every mode. The escape hatch is §3.4's alternatives -- a
			// document or a field report -- not a mode that switches the rule off,
			// because a mode that did would be a mode where "ready" means nothing.
			if n == 0 {
				return fmt.Errorf("%w: a feature reaches ready with evidence -- a validated "+
					"complaint, a project document, or a field report", ErrInvalid)
			}
		}
		if status == "in_progress" {
			blockers, err := d.OpenBlockers(ctx, featureID)
			if err != nil {
				return err
			}
			if len(blockers) > 0 {
				return fmt.Errorf("%w: blocked by %s", ErrInvalid, describeBlockers(blockers))
			}
			// Evidence is re-checked HERE, not only at ready, because evidence can be
			// removed after a feature is ready: UnlinkComplaint and RemoveFeatureEvidence
			// both delete rows and neither un-readies anything (that policy is theirs, and
			// it exists so an edit does not silently undo a decision). Without this check
			// the gate on `ready` would be a gate with a hole in it -- ready, unlink, then
			// start work -- and "committed with evidence" would be unenforceable by the
			// time the work actually begins, which is the moment it costs something.
			n, err := d.countFeatureEvidence(ctx, featureID)
			if err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("%w: a feature starts work with evidence -- a validated "+
					"complaint, a project document, or a field report", ErrInvalid)
			}
		}
	}
	now := float64(time.Now().Unix())
	_, err := d.ExecContext(ctx, `UPDATE features SET status=?, updated_at=? WHERE id=?`, status, now, featureID)
	return err
}

// GetFeatureComplaints returns all validated complaints linked to a feature.
func (d *DB) GetFeatureComplaints(ctx context.Context, featureID int64) ([]Complaint, error) {
	rows, err := d.QueryContext(ctx, `SELECT c.id, c.project_id, c.author_id, c.title, c.body, c.severity, c.frequency,
		c.strategic_multiplier, c.status, c.merged_into, c.created_at, c.updated_at
		FROM feature_complaints fc JOIN complaints c ON c.id = fc.complaint_id
		WHERE fc.feature_id=? ORDER BY c.created_at DESC`, featureID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// An EMPTY slice, never nil.
	//
	// `var complaints []Complaint` starts as nil, and a feature with no linked
	// complaints returned it unchanged -- so the JSON was `null`, not `[]`. A
	// client written against an array (`res.map(...)`, `Array.isArray`, a JSON
	// schema with type: array) then fails, and it fails as a null dereference far
	// from the store method that produced it.
	//
	// The state is reachable even though CreateFeature refuses to create it (§6.2
	// requires a validated complaint): unlinking, or a migration that orphaned a
	// feature_complaints row, both land here. Test:
	// TestGetFeatureComplaintsReturnsEmptyNotNil.
	complaints := []Complaint{}
	for rows.Next() {
		var c Complaint
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.AuthorID, &c.Title, &c.Body, &c.Severity,
			&c.Frequency, &c.StrategicMultiplier, &c.Status, &c.MergedInto,
			&c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		complaints = append(complaints, c)
	}
	return complaints, rows.Err()
}

// GetComplaintFeatures returns the features linked to a complaint.
//
// The mirror of GetFeatureComplaints, which until this commit was the only
// direction with a read side. So a complaint could be linked to features and no
// response anywhere could say so -- the same write-only hole, in the direction
// spec's page ranking actually asks for ("Carries impact meter, linked features,
// status").
//
// Ordering is by the FEATURE's rating, not by link order or by id: this list is
// "which work answers this pain, best first", and a reader comparing three
// features wants the strongest one at the top. elo_rd travels with it for the same
// uncertainty reason as everywhere else -- a 1500 that has never been compared and
// a settled 1500 are not the same claim.
func (d *DB) GetComplaintFeatures(ctx context.Context, complaintID int64) ([]Feature, error) {
	rows, err := d.QueryContext(ctx, `SELECT f.id, f.project_id, f.author_id, f.title, f.body,
		f.effort, f.impact, f.effort_score, f.impact_ratio, f.status,
		f.elo_r, f.elo_rd, f.elo_vol, f.strategic_weight, f.created_at, f.updated_at
		FROM feature_complaints fc JOIN features f ON f.id = fc.feature_id
		WHERE fc.complaint_id=? ORDER BY f.elo_r DESC, f.id`, complaintID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// An empty slice, never nil: nil marshals to null. See GetFeatureComplaints.
	features := []Feature{}
	for rows.Next() {
		var f Feature
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.AuthorID, &f.Title, &f.Body, &f.Effort,
			&f.Impact, &f.EffortScore, &f.ImpactRatio,
			&f.Status, &f.EloR, &f.EloRD, &f.EloVol, &f.StrategicWeight,
			&f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		features = append(features, f)
	}
	return features, rows.Err()
}
