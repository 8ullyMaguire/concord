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
)

// DB wraps the pool. All queries take ctx for cancellation.
type DB struct {
	*sql.DB
}

func New(d *sql.DB) *DB { return &DB{DB: d} }

// ---------------------------------------------------------------- users

type User struct {
	ID          int64   `json:"id"`
	Username    string  `json:"username"`
	DisplayName string  `json:"display_name"`
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
	return u, err
}

// GetCharterForProject loads the charter for a project by project_id.
func (d *DB) GetCharterForProject(ctx context.Context, projectID int64) (governance.Charter, error) {
	var c governance.Charter
	err := d.QueryRowContext(ctx, `
		SELECT quorum_ratio, quorum_min, consent_ratio, override_ratio,
		       vote_window_days, merge_requires_quorum, merge_quorum_min,
		       merge_quorum_ratio, require_reviewer_approval, wip_in_progress,
		       wip_review, lam, mu, pain_halflife_days, rep_halflife_days,
		       glicko_tau, vote_weight_cap
		FROM charters WHERE project_id = ?`, projectID).Scan(
		&c.QuorumRatio, &c.QuorumMin, &c.ConsentRatio, &c.OverrideRatio,
		&c.VoteWindowDays, &c.MergeRequiresQuorum, &c.MergeQuorumMin,
		&c.MergeQuorumRatio, &c.RequireReviewerApproval, &c.WIPInProgress,
		&c.WIPReview, &c.Lam, &c.Mu, &c.PainHalflifeDays, &c.RepHalflifeDays,
		&c.GlickoTau, &c.VoteWeightCap)
	if errors.Is(err, sql.ErrNoRows) {
		return governance.DefaultCharter(governance.GovernanceModel("collective")), nil
	}
	if err != nil {
		return governance.Charter{}, fmt.Errorf("load charter: %w", err)
	}
	return c, nil
}

// ---------------------------------------------------------------- projects

type Project struct {
	ID              int64    `json:"id"`
	Slug            string   `json:"slug"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	GovernanceModel string   `json:"governance_model"`
	License         string   `json:"license,omitempty"`
	CreatedAt       float64  `json:"created_at"`
	UpdatedAt       float64  `json:"updated_at"`
	HealthScore     *float64 `json:"health_score,omitempty"` // nil until metrics exist
}

const projectColumns = `p.id, p.slug, p.name, COALESCE(p.description,''),
	p.governance_model, COALESCE(p.license,''), p.created_at, p.updated_at,
	m.health_score`

func scanProject(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var health sql.NullFloat64
	if err := row.Scan(&p.ID, &p.Slug, &p.Name, &p.Description,
		&p.GovernanceModel, &p.License, &p.CreatedAt, &p.UpdatedAt, &health); err != nil {
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
func (d *DB) CreateProject(ctx context.Context, slug, name, description, model, license string) (Project, error) {
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
		"quorum_ratio", "quorum_min", "consent_ratio", "override_ratio",
		"vote_window_days", "merge_requires_quorum", "merge_quorum_min",
		"merge_quorum_ratio", "require_reviewer_approval", "wip_in_progress",
		"wip_review", "lam", "mu", "pain_halflife_days", "rep_halflife_days",
		"glicko_tau", "vote_weight_cap",
	}
	charterVals := []any{
		charter.QuorumRatio, charter.QuorumMin, charter.ConsentRatio,
		charter.OverrideRatio, charter.VoteWindowDays,
		boolInt(charter.MergeRequiresQuorum), charter.MergeQuorumMin,
		charter.MergeQuorumRatio, boolInt(charter.RequireReviewerApproval),
		charter.WIPInProgress, charter.WIPReview,
		charter.Lam, charter.Mu, charter.PainHalflifeDays,
		charter.RepHalflifeDays, charter.GlickoTau, charter.VoteWeightCap,
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

func (d *DB) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT `+projectColumns+` FROM projects p
		LEFT JOIN project_metrics m ON m.project_id = p.id
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
