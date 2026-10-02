// Package store is Concord's SQL data layer (database/sql + plain SQL).
// Project CRUD + discovery data + search is the exemplar vertical: follow
// this handler→store→SQL pattern when adding complaints, features,
// consensus, and the board (docs/PLAN.md).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/discovery"
	"git.polarisocial.xyz/concord/concord/internal/governance"
)

// Domain errors mapped to HTTP status codes by internal/httpapi.
var (
	ErrNotFound  = errors.New("not found")
	ErrDuplicate = errors.New("duplicate")
	ErrInvalid   = errors.New("invalid")
	ErrAuth      = errors.New("authentication required")
	ErrPerm      = errors.New("permission denied")
	// ErrConflict is a valid request that the current state refuses: a
	// consensus call suspended by an emergency hold, a stale proposal whose
	// target already moved. 409 rather than 400 -- nothing about the request is
	// malformed, and a client that retries unchanged should expect the same
	// answer until the state changes.
	ErrConflict = errors.New("conflicts with current state")
)

// DB wraps the pool. All queries take ctx for cancellation.
type DB struct {
	*sql.DB

	// embedder produces the vectors used for duplicate detection. Optional: with
	// none configured, the similar-endpoints report that they are unavailable
	// rather than pretending to have no duplicates. See internal/embed for why
	// the default is a local embedder rather than a model server.
	//
	// Set once via SetEmbedder and never per-request: an embedder that changed
	// between writing and reading would make the stored model_id meaningless.
	embedder Embedder
}

func New(d *sql.DB) *DB { return &DB{DB: d} }

// ---------------------------------------------------------------- users

type Member struct {
	ProjectID   int64   `json:"project_id"`
	UserID      int64   `json:"user_id"`
	Username    string  `json:"username"`
	Role        string  `json:"role"`
	IsModerator int     `json:"is_moderator"`
	JoinedAt    float64 `json:"joined_at"`
}

type User struct {
	ID          int64   `json:"id"`
	Username    string  `json:"username"`
	DisplayName string  `json:"display_name"`
	Role        string  `json:"role"`
	CreatedAt   float64 `json:"created_at"`
}

func (d *DB) CreateUser(ctx context.Context, username, displayName string) (User, error) {
	if !validUsername(username) {
		return User{}, fmt.Errorf("%w: username must be lowercase alnum with - _ .", ErrInvalid)
	}
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx,
		`INSERT INTO users (username, display_name, created_at) VALUES (?, ?, ?)`,
		username, displayName, now)
	if isUniqueViolation(err) {
		return User{}, fmt.Errorf("%w: user %q", ErrDuplicate, username)
	}
	if err != nil {
		return User{}, err
	}
	id, _ := res.LastInsertId()
	return User{ID: id, Username: username, DisplayName: displayName, CreatedAt: now}, nil
}

func (d *DB) GetUser(ctx context.Context, username string) (User, error) {
	var u User
	err := d.QueryRowContext(ctx,
		`SELECT id, username, COALESCE(display_name,''), created_at FROM users WHERE username=?`,
		username).Scan(&u.ID, &u.Username, &u.DisplayName, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, fmt.Errorf("%w: user %q", ErrNotFound, username)
	}
	u.Role = "member"
	return u, err
}

// GetCharterForProject loads the charter for a project by project_id.
func (d *DB) GetCharterForProject(ctx context.Context, projectID int64) (governance.Charter, error) {
	var c governance.Charter
	err := d.QueryRowContext(ctx, `
		SELECT quorum_ratio, quorum_min, consent_ratio, support_ratio_min,
		       override_ratio,
		       vote_window_days, merge_requires_quorum, merge_quorum_min,
		       merge_quorum_ratio, require_reviewer_approval, wip_in_progress,
		       wip_review, lam, mu, pain_halflife_days, rep_halflife_days,
		       glicko_tau, vote_weight_cap,
		       solution_call_min_voters, solution_call_confidence,
		       solution_stable_hours
		FROM charters WHERE project_id = ?`, projectID).Scan(
		&c.QuorumRatio, &c.QuorumMin, &c.ConsentRatio, &c.SupportRatioMin,
		&c.OverrideRatio,
		&c.VoteWindowDays, &c.MergeRequiresQuorum, &c.MergeQuorumMin,
		&c.MergeQuorumRatio, &c.RequireReviewerApproval, &c.WIPInProgress,
		&c.WIPReview, &c.Lam, &c.Mu, &c.PainHalflifeDays, &c.RepHalflifeDays,
		&c.GlickoTau, &c.VoteWeightCap,
		&c.SolutionCallMinVoters, &c.SolutionCallConfidence,
		&c.SolutionStableHours)
	if errors.Is(err, sql.ErrNoRows) {
		return governance.DefaultCharter(governance.GovernanceModel("collective")), nil
	}
	if err != nil {
		return governance.Charter{}, fmt.Errorf("load charter: %w", err)
	}
	return c, nil
}

// UpdateCharter updates a project's charter values (M5).
func (d *DB) UpdateCharter(ctx context.Context, projectID int64, charter governance.Charter) error {
	// §6.5's three conditions are checked here rather than by SQL CHECKs,
	// because a CHECK on an ALTERed column needs a table rebuild and rebuilding
	// charters is how 0008's cascade migration went wrong.
	//
	// The bounds are not cosmetic. A confidence above 1.0 can never be met, and a
	// stable window of zero means the leader has to have held the lead for no time
	// at all. Either one silently disables §6.5 for the whole project: calls would
	// never open on merit and nothing would say why.
	if charter.SolutionCallConfidence <= 0 || charter.SolutionCallConfidence >= 1 {
		return fmt.Errorf("%w: solution_call_confidence must be between 0 and 1 exclusive, got %v",
			ErrInvalid, charter.SolutionCallConfidence)
	}
	if charter.SolutionCallMinVoters < 1 {
		return fmt.Errorf("%w: solution_call_min_voters must be at least 1, got %d",
			ErrInvalid, charter.SolutionCallMinVoters)
	}
	if charter.SolutionStableHours <= 0 {
		return fmt.Errorf("%w: solution_stable_hours must be positive, got %v",
			ErrInvalid, charter.SolutionStableHours)
	}

	_, err := d.ExecContext(ctx, `
		UPDATE charters SET
			quorum_ratio = ?, quorum_min = ?, consent_ratio = ?,
			support_ratio_min = ?, override_ratio = ?,
			vote_window_days = ?, merge_requires_quorum = ?, merge_quorum_min = ?,
			merge_quorum_ratio = ?, require_reviewer_approval = ?, wip_in_progress = ?,
			wip_review = ?, lam = ?, mu = ?, pain_halflife_days = ?, rep_halflife_days = ?,
			glicko_tau = ?, vote_weight_cap = ?,
			solution_call_min_voters = ?, solution_call_confidence = ?,
			solution_stable_hours = ?
		WHERE project_id = ?`,
		charter.QuorumRatio, charter.QuorumMin, charter.ConsentRatio,
		charter.SupportRatioMin, charter.OverrideRatio,
		charter.VoteWindowDays, charter.MergeRequiresQuorum, charter.MergeQuorumMin,
		charter.MergeQuorumRatio, charter.RequireReviewerApproval, charter.WIPInProgress,
		charter.WIPReview, charter.Lam, charter.Mu, charter.PainHalflifeDays, charter.RepHalflifeDays,
		charter.GlickoTau, charter.VoteWeightCap,
		charter.SolutionCallMinVoters, charter.SolutionCallConfidence,
		charter.SolutionStableHours, projectID)
	return err
}

// GetReputation returns the sum of reputation points for a user
// in a project.
func (d *DB) GetReputation(ctx context.Context, projectID, userID int64) (float64, error) {
	var rep float64
	err := d.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(points), 0) FROM reputation_events
		WHERE project_id = ? AND user_id = ?`, projectID, userID).Scan(&rep)
	if err != nil {
		return 0, fmt.Errorf("load reputation: %w", err)
	}
	return rep, nil
}

// AddReputation records a reputation event for a user in a project.
func (d *DB) AddReputation(ctx context.Context, projectID, userID int64, kind string, points float64) error {
	now := float64(time.Now().Unix())
	_, err := d.ExecContext(ctx, `
		INSERT INTO reputation_events (project_id, user_id, kind, points, created_at)
		VALUES (?, ?, ?, ?, ?)`, projectID, userID, kind, points, now)
	if err != nil {
		return fmt.Errorf("add reputation: %w", err)
	}
	return nil
}

// GetRoleForProject returns the role of a user in a project.
func (d *DB) GetRoleForProject(ctx context.Context, projectID, userID int64) (string, error) {
	var role string
	err := d.QueryRowContext(ctx, `
		SELECT role FROM members WHERE project_id = ? AND user_id = ?`,
		projectID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "guest", nil
	}
	if err != nil {
		return "", fmt.Errorf("load member role: %w", err)
	}
	return role, nil
}

// GetMember returns the full membership record for a user in a project.
func (d *DB) GetMember(ctx context.Context, projectID, userID int64) (Member, error) {
	var m Member
	err := d.QueryRowContext(ctx, `
		SELECT project_id, user_id, role, is_moderator, joined_at
		FROM members WHERE project_id = ? AND user_id = ?`,
		projectID, userID).Scan(&m.ProjectID, &m.UserID, &m.Role, &m.IsModerator, &m.JoinedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Member{}, fmt.Errorf("%w: not a member of project", ErrNotFound)
	}
	if err != nil {
		return Member{}, fmt.Errorf("load member: %w", err)
	}
	return m, nil
}

// ---------------------------------------------------------------- projects

// ---------------------------------------------------------------- projects

// ---------------------------------------------------------------- projects

type Project struct {
	ID              int64  `json:"id"`
	Slug            string `json:"slug"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	GovernanceModel string `json:"governance_model"`
	License         string `json:"license,omitempty"`
	// Visibility is one of VisibilityPrivate, VisibilityUnlisted,
	// VisibilityProtected, VisibilityPublic. It is always set: the column is
	// NOT NULL with a CHECK, so "unset" is a state the database refuses.
	Visibility  string   `json:"visibility"`
	CreatedAt   float64  `json:"created_at"`
	UpdatedAt   float64  `json:"updated_at"`
	HealthScore *float64 `json:"health_score,omitempty"` // nil until metrics exist
}

// The four visibility levels. See migration 0009 for what each one means and,
// more importantly, for why a non-member refusal is a 404 rather than a 403.
const (
	// VisibilityPublic is listed in every project listing and readable by
	// anyone, including an anonymous caller.
	VisibilityPublic = "public"
	// VisibilityUnlisted is listed nowhere but readable by direct link with no
	// session at all. Obscurity, not access control: the URL is the capability.
	VisibilityUnlisted = "unlisted"
	// VisibilityProtected is listed nowhere and readable only by a signed-in
	// user with a grant -- a members row or a redeemed invite.
	VisibilityProtected = "protected"
	// VisibilityPrivate is listed nowhere and readable only by a member.
	VisibilityPrivate = "private"
)

// ValidVisibility reports whether v is one of the four levels. Used at the API
// boundary so an unknown level is a 400 rather than a stored value no read path
// knows how to interpret.
func ValidVisibility(v string) bool {
	switch v {
	case VisibilityPublic, VisibilityUnlisted, VisibilityProtected, VisibilityPrivate:
		return true
	}
	return false
}

// IsListed reports whether a project at this visibility appears in listings and
// search results. Public is the only level that is listed; the other three are
// reachable by direct link alone.
func (p Project) IsListed() bool { return p.Visibility == VisibilityPublic }

const projectColumns = `p.id, p.slug, p.name, COALESCE(p.description,''),
	p.governance_model, COALESCE(p.license,''), COALESCE(p.visibility,'public'),
	p.created_at, p.updated_at,
	m.health_score`

func scanProject(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var health sql.NullFloat64
	if err := row.Scan(&p.ID, &p.Slug, &p.Name, &p.Description,
		&p.GovernanceModel, &p.License, &p.Visibility,
		&p.CreatedAt, &p.UpdatedAt, &health); err != nil {
		return p, err
	}
	if health.Valid {
		h := health.Float64
		p.HealthScore = &h
	}
	return p, nil
}

// CreateProject inserts the project, its default charter, the board
// columns, an empty metrics row, and the FTS index entry — all in one tx.
func (d *DB) CreateProject(ctx context.Context, userID int64, slug, name, description, model, license string) (Project, error) {
	if !validSlug(slug) {
		return Project{}, fmt.Errorf("%w: slug must be lowercase kebab-case", ErrInvalid)
	}
	if !governance.GovernanceModel(model).Valid() {
		return Project{}, fmt.Errorf("%w: governance_model must be collective or maintainer_led", ErrInvalid)
	}
	gm := governance.GovernanceModel(model)
	charter := governance.DefaultCharter(gm)
	now := float64(time.Now().Unix())

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO projects (slug, name, description, governance_model, license, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		slug, name, description, model, license, now, now)
	if isUniqueViolation(err) {
		return Project{}, fmt.Errorf("%w: project %q", ErrDuplicate, slug)
	}
	if err != nil {
		return Project{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Project{}, err
	}

	charterCols := []string{
		"quorum_ratio", "quorum_min", "consent_ratio", "support_ratio_min",
		"override_ratio",
		"vote_window_days", "merge_requires_quorum", "merge_quorum_min",
		"merge_quorum_ratio", "require_reviewer_approval", "wip_in_progress",
		"wip_review", "lam", "mu", "pain_halflife_days", "rep_halflife_days",
		"glicko_tau", "vote_weight_cap",
		"solution_call_min_voters", "solution_call_confidence", "solution_stable_hours",
	}
	charterVals := []any{
		charter.QuorumRatio, charter.QuorumMin, charter.ConsentRatio,
		charter.SupportRatioMin, charter.OverrideRatio, charter.VoteWindowDays,
		boolInt(charter.MergeRequiresQuorum), charter.MergeQuorumMin,
		charter.MergeQuorumRatio, boolInt(charter.RequireReviewerApproval),
		charter.WIPInProgress, charter.WIPReview,
		charter.Lam, charter.Mu, charter.PainHalflifeDays,
		charter.RepHalflifeDays, charter.GlickoTau, charter.VoteWeightCap,
		charter.SolutionCallMinVoters, charter.SolutionCallConfidence,
		charter.SolutionStableHours,
	}
	query := "INSERT INTO charters (project_id, " + strings.Join(charterCols, ", ") +
		") VALUES (?, " + strings.Repeat("?, ", len(charterCols)-1) + "?)"
	if _, err := tx.ExecContext(ctx, query, append([]any{id}, charterVals...)...); err != nil {
		return Project{}, err
	}

	for i, phase := range governance.BoardPhases {
		var wip any
		if w := charter.WIPFor(phase); w > 0 {
			wip = w
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO board_columns (project_id, phase, position, wip_limit) VALUES (?, ?, ?, ?)`,
			id, phase, i, wip); err != nil {
			return Project{}, err
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?, ?, 'maintainer', ?)`, id, userID, now); err != nil {
		return Project{}, err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO project_metrics (project_id, computed_at) VALUES (?, ?)`, id, now); err != nil {
		return Project{}, err
	}

	if err := tx.Commit(); err != nil {
		return Project{}, err
	}
	if err := d.ReindexProject(ctx, id); err != nil {
		return Project{}, err
	}
	return d.GetProject(ctx, slug)
}

func (d *DB) GetProject(ctx context.Context, slug string) (Project, error) {
	row := d.QueryRowContext(ctx, `
		SELECT `+projectColumns+` FROM projects p
		LEFT JOIN project_metrics m ON m.project_id = p.id
		WHERE p.slug = ?`, slug)
	p, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, fmt.Errorf("%w: project %q", ErrNotFound, slug)
	}
	return p, err
}

// GetProjectByID loads a project by its numeric ID.
func (d *DB) GetProjectByID(ctx context.Context, projectID int64) (Project, error) {
	var p Project
	err := d.QueryRowContext(ctx, `
		SELECT `+projectColumns+` FROM projects p
		LEFT JOIN project_metrics m ON m.project_id = p.id
		WHERE p.id = ?`, projectID).Scan(
		&p.ID, &p.Slug, &p.Name, &p.Description,
		&p.GovernanceModel, &p.License, &p.Visibility,
		&p.CreatedAt, &p.UpdatedAt, &p.HealthScore)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, fmt.Errorf("%w: project %d", ErrNotFound, projectID)
	}
	return p, err
}

// ---------------------------------------------------------------- projects

// ListProjects returns only projects that appear in listings: the public ones.
// Every other level is reachable by direct link and must not be enumerable, so
// the filter lives here rather than in each caller -- a handler that forgot it
// would leak the whole list.
func (d *DB) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT `+projectColumns+` FROM projects p
		LEFT JOIN project_metrics m ON m.project_id = p.id
		WHERE p.visibility = 'public'
		ORDER BY p.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CanAccessProject decides whether actor may read project p, and is the single
// place that answers that question.
//
// actorID is 0 for an anonymous caller. The four levels behave as follows:
//
//	public     everyone, including anonymous.
//	unlisted   everyone, including anonymous -- the URL is the capability, and
//	           pretending otherwise would be a claim the level does not make.
//	protected  a member, or a signed-in user who redeemed an invite.
//	private    a member only.
//
// A member is a row in `members`, which is what a join, an invite redemption or
// a hand-added maintainer all produce. Reading the table rather than trusting
// the caller keeps "is this person a member" in one query.
func (d *DB) CanAccessProject(ctx context.Context, p Project, actorID int64) (bool, error) {
	switch p.Visibility {
	case VisibilityPublic, VisibilityUnlisted:
		return true, nil
	case VisibilityPrivate, VisibilityProtected:
		if actorID == 0 {
			return false, nil
		}
	default:
		// An unknown level must not fall through to "allow". Migration 0009's
		// CHECK makes this unreachable from the database, but the store is also
		// reachable from tests and future code, and fail-open is the wrong
		// direction for an access check.
		return false, fmt.Errorf("%w: project %d has unknown visibility %q",
			ErrInvalid, p.ID, p.Visibility)
	}

	var n int
	err := d.QueryRowContext(ctx,
		`SELECT count(*) FROM members WHERE project_id = ? AND user_id = ?`,
		p.ID, actorID).Scan(&n)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}

	if p.Visibility == VisibilityProtected {
		// A redeemed invite is what makes a protected project reachable by
		// someone who is not yet a member.
		var r int
		if err := d.QueryRowContext(ctx, `
			SELECT count(*) FROM invite_redemptions ir
			JOIN project_invites i ON i.id = ir.invite_id
			WHERE i.project_id = ? AND ir.user_id = ?`, p.ID, actorID).Scan(&r); err != nil {
			return false, err
		}
		return r > 0, nil
	}
	return false, nil
}

// SetProjectVisibility changes a project's level. Rejects an unknown value
// rather than storing it, so a typo cannot produce a project that no read path
// can classify.
func (d *DB) SetProjectVisibility(ctx context.Context, slug, visibility string) (Project, error) {
	if !ValidVisibility(visibility) {
		return Project{}, fmt.Errorf("%w: unknown visibility %q; valid: public, unlisted, protected, private",
			ErrInvalid, visibility)
	}
	res, err := d.ExecContext(ctx,
		`UPDATE projects SET visibility = ?, updated_at = ? WHERE slug = ?`,
		visibility, float64(time.Now().Unix()), slug)
	if err != nil {
		return Project{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Project{}, fmt.Errorf("%w: project %q", ErrNotFound, slug)
	}
	return d.GetProject(ctx, slug)
}

// ListProjectsVisibleTo returns every project actor may see: the listed public
// ones plus anything at another level that actor is entitled to. This is what a
// signed-in user's own view of the index should show, so that a private project
// does not vanish from its owner's screen.
func (d *DB) ListProjectsVisibleTo(ctx context.Context, actorID int64) ([]Project, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT `+projectColumns+` FROM projects p
		LEFT JOIN project_metrics m ON m.project_id = p.id
		WHERE p.visibility = 'public'
		   OR (p.visibility IN ('unlisted','private') AND EXISTS (
		         SELECT 1 FROM members mm WHERE mm.project_id = p.id AND mm.user_id = ?
		   ))
		   OR (p.visibility = 'protected' AND (
		         EXISTS (SELECT 1 FROM members mm2 WHERE mm2.project_id = p.id AND mm2.user_id = ?)
		      OR EXISTS (SELECT 1 FROM invite_redemptions ir
		                 JOIN project_invites i ON i.id = ir.invite_id
		                 WHERE i.project_id = p.id AND ir.user_id = ?)
		   ))
		ORDER BY p.created_at`, actorID, actorID, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) projectIDBySlug(ctx context.Context, slug string) (int64, error) {
	var id int64
	err := d.QueryRowContext(ctx, `SELECT id FROM projects WHERE slug=?`, slug).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: project %q", ErrNotFound, slug)
	}
	return id, err
}

// ApplyProjectTag attaches a tag (creating it in the global namespace if
// needed). Contributor-level permission is enforced upstream (spec §15).
// appliedBy is the acting user's id, or 0 for "unknown" (stored as NULL).
func (d *DB) ApplyProjectTag(ctx context.Context, slug, tag string, appliedBy int64) error {
	id, err := d.projectIDBySlug(ctx, slug)
	if err != nil {
		return err
	}
	tagID, err := d.ensureTag(ctx, tag)
	if err != nil {
		return err
	}
	var appliedByArg any
	if appliedBy > 0 {
		appliedByArg = appliedBy
	}
	_, err = d.ExecContext(ctx, `
		INSERT OR IGNORE INTO project_tags (project_id, tag_id, applied_by, created_at)
		VALUES (?, ?, ?, ?)`, id, tagID, appliedByArg, float64(time.Now().Unix()))
	if err != nil {
		return err
	}
	return d.ReindexProject(ctx, id)
}

// ProjectTags returns the names of every tag attached to a project, sorted.
//
// This exists because tags were write-only. `PUT /projects/{slug}/tags`
// attached them and the handler then returned the project -- whose struct has no
// Tags field -- so the caller got a 200 with no way to confirm what was stored
// and no route anywhere to read them back. Thirteen tags on Tessera were
// invisible: correct in the database, absent from the API, and only discoverable
// by opening the database by hand.
//
// Sorted rather than in insertion order so two callers comparing the result get
// the same string, which is what makes it assertable in a test.
func (d *DB) ProjectTags(ctx context.Context, slug string) ([]string, error) {
	id, err := d.projectIDBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	rows, err := d.QueryContext(ctx, `
		SELECT t.name
		FROM project_tags pt
		JOIN tags t ON t.id = pt.tag_id
		WHERE pt.project_id = ?
		ORDER BY t.name`, id)
	if err != nil {
		return nil, fmt.Errorf("project tags: %w", err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan project tag: %w", err)
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// ensureTag creates-or-returns a global-namespace tag (project_id NULL).
func (d *DB) ensureTag(ctx context.Context, name string) (int64, error) {
	name = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(name, " ", "-")))
	if name == "" {
		return 0, fmt.Errorf("%w: empty tag", ErrInvalid)
	}
	var id int64
	err := d.QueryRowContext(ctx,
		`SELECT id FROM tags WHERE project_id IS NULL AND name=?`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := d.ExecContext(ctx,
		`INSERT INTO tags (project_id, name) VALUES (NULL, ?)`, name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type Language struct {
	Language string  `json:"language"`
	Pct      float64 `json:"pct"`
}

// SetProjectLanguages replaces the language mix (spec §15: collaboratively
// correctable percentages).
func (d *DB) SetProjectLanguages(ctx context.Context, slug string, langs []Language) error {
	id, err := d.projectIDBySlug(ctx, slug)
	if err != nil {
		return err
	}
	total := 0.0
	for _, l := range langs {
		if l.Pct < 0 || l.Pct > 100 {
			return fmt.Errorf("%w: pct must be within 0..100", ErrInvalid)
		}
		total += l.Pct
	}
	if total > 100.01 {
		return fmt.Errorf("%w: language percentages sum to %.2f", ErrInvalid, total)
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_languages WHERE project_id=?`, id); err != nil {
		return err
	}
	for _, l := range langs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO project_languages (project_id, language, pct) VALUES (?, ?, ?)`,
			id, l.Language, l.Pct); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return d.ReindexProject(ctx, id)
}

// UpdateProjectMetrics upserts forge-synced facts and recomputes the
// transparent health score (internal/discovery).
func (d *DB) UpdateProjectMetrics(ctx context.Context, slug string, m discovery.Metrics, stars, forks, openIssues, commitCount int, license string) (Project, error) {
	id, err := d.projectIDBySlug(ctx, slug)
	if err != nil {
		return Project{}, err
	}
	b := discovery.HealthScore(m, discovery.DefaultWeights)
	now := float64(time.Now().Unix())
	var lastCommit any
	if m.LastCommitAgeDays < 1e12 {
		lastCommit = now - m.LastCommitAgeDays*86400
	}
	_, err = d.ExecContext(ctx, `
		INSERT INTO project_metrics (project_id, stars, forks, open_issues,
			commit_count, contributors, last_commit_at, median_review_hours,
			releases_90d, health_score, computed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			stars=excluded.stars, forks=excluded.forks, open_issues=excluded.open_issues,
			commit_count=excluded.commit_count, contributors=excluded.contributors,
			last_commit_at=excluded.last_commit_at,
			median_review_hours=excluded.median_review_hours,
			releases_90d=excluded.releases_90d,
			health_score=excluded.health_score, computed_at=excluded.computed_at`,
		id, stars, forks, openIssues, commitCount, m.Contributors,
		lastCommit, m.MedianReviewHours, m.Releases90d, b.Total, now)
	if err != nil {
		return Project{}, err
	}
	if license != "" {
		if _, err := d.ExecContext(ctx, `UPDATE projects SET license=?, updated_at=? WHERE id=?`,
			license, now, id); err != nil {
			return Project{}, err
		}
	}
	return d.GetProject(ctx, slug)
}

// ReindexProject rebuilds the project's FTS row from relational data.
func (d *DB) ReindexProject(ctx context.Context, projectID int64) error {
	var slug, name, description string
	if err := d.QueryRowContext(ctx,
		`SELECT slug, name, COALESCE(description,'') FROM projects WHERE id=?`,
		projectID).Scan(&slug, &name, &description); err != nil {
		return err
	}
	tagRows, err := d.QueryContext(ctx, `
		SELECT t.name FROM project_tags pt JOIN tags t ON t.id = pt.tag_id
		WHERE pt.project_id=? ORDER BY t.name`, projectID)
	if err != nil {
		return err
	}
	tags := joinNames(tagRows)

	langRows, err := d.QueryContext(ctx, `
		SELECT language FROM project_languages WHERE project_id=? ORDER BY pct DESC`, projectID)
	if err != nil {
		return err
	}
	languages := joinNames(langRows)

	if _, err := d.ExecContext(ctx, `DELETE FROM projects_fts WHERE slug=?`, slug); err != nil {
		return err
	}
	_, err = d.ExecContext(ctx, `
		INSERT INTO projects_fts (slug, name, description, tags, languages)
		VALUES (?, ?, ?, ?, ?)`, slug, name, description, tags, languages)
	return err
}

func joinNames(rows *sql.Rows) string {
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			names = append(names, n)
		}
	}
	return strings.Join(names, " ")
}

// ---------------------------------------------------------------- helpers

func validSlug(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return s[0] >= 'a' && s[0] <= 'z' || s[0] >= '0' && s[0] <= '9'
}

func validUsername(s string) bool {
	return validSlug(s) && strings.IndexAny(s, ".") == -1
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// isUniqueViolation recognizes SQLite constraint errors across drivers.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}
