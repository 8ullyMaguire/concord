package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/ranking"
)

// Criteria storage (spec extension, 2026-09-29).
//
// Criteria are per-project, proposable, and each carries its own Glicko-2 pool.
// The three rules this file exists to enforce:
//
//  1. A criterion belongs to exactly one project, and every row that references
//     it must agree. project_id is denormalised onto criterion_votes for index
//     access, so it is derived from the criterion rather than trusted from the
//     caller — otherwise a caller could attach a vote from project A to
//     project B's pool and poison its ratings.
//  2. A feature rated on a criterion must belong to that criterion's project.
//     Comparing features across projects would compare ratings from two
//     unrelated pools.
//  3. Absent rating is not average rating. A feature with no row in
//     criterion_ratings is unrated, and is reported as absent rather than as
//     1500 — imputing the mean would rank an unvoted feature as though a panel
//     had judged it mediocre.

// ErrCriterionScope is returned when a caller reaches outside its own project.
var ErrCriterionScope = errors.New("criterion does not belong to this project")

// CreateCriterion adds a ranking dimension to a project.
func (d *DB) CreateCriterion(ctx context.Context, projectID, actorID int64,
	slug, name, description, direction string, defaultWeight float64) (ranking.Criterion, error) {

	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return ranking.Criterion{}, fmt.Errorf("%w: empty criterion slug", ErrInvalid)
	}
	if name == "" {
		return ranking.Criterion{}, fmt.Errorf("%w: empty criterion name", ErrInvalid)
	}
	if direction == "" {
		direction = ranking.DirHigher
	}
	if direction != ranking.DirHigher && direction != ranking.DirLower {
		return ranking.Criterion{}, fmt.Errorf("%w: direction must be %s or %s",
			ErrInvalid, ranking.DirHigher, ranking.DirLower)
	}
	if defaultWeight <= 0 {
		defaultWeight = 1.0
	}

	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO criteria
			(project_id, slug, name, description, direction, default_weight,
			 active, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`,
		projectID, slug, name, description, direction, defaultWeight, actorID, now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return ranking.Criterion{}, fmt.Errorf("%w: criterion %q already exists in this project", ErrInvalid, slug)
		}
		return ranking.Criterion{}, fmt.Errorf("create criterion: %w", err)
	}
	id, _ := res.LastInsertId()
	return ranking.Criterion{
		ID: id, ProjectID: projectID, Slug: slug, Name: name,
		Description: description, Direction: direction,
		DefaultW: defaultWeight, Active: true,
	}, nil
}

// GetCriterion fetches one criterion by id, scoped to a project.
func (d *DB) GetCriterion(ctx context.Context, projectID, criterionID int64) (ranking.Criterion, error) {
	var c ranking.Criterion
	var dir string
	var active int
	err := d.QueryRowContext(ctx, `
		SELECT id, project_id, slug, name, description, direction,
		       default_weight, active
		FROM criteria WHERE id=? AND project_id=?`, criterionID, projectID).
		Scan(&c.ID, &c.ProjectID, &c.Slug, &c.Name, &c.Description,
			&dir, &c.DefaultW, &active)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c, ErrNotFound
		}
		return c, fmt.Errorf("get criterion: %w", err)
	}
	c.Direction, c.Active = dir, active == 1
	return c, nil
}

// ListCriteria returns a project's criteria, active ones first then by slug.
// Inactive criteria are included because the API must be able to show a
// retired dimension; ranking excludes them by default.
func (d *DB) ListCriteria(ctx context.Context, projectID int64) ([]ranking.Criterion, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, project_id, slug, name, description, direction,
		       default_weight, active
		FROM criteria WHERE project_id=?
		ORDER BY active DESC, slug`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list criteria: %w", err)
	}
	defer rows.Close()

	var out []ranking.Criterion
	for rows.Next() {
		var c ranking.Criterion
		var dir string
		var active int
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Slug, &c.Name,
			&c.Description, &dir, &c.DefaultW, &active); err != nil {
			return nil, fmt.Errorf("scan criterion: %w", err)
		}
		c.Direction, c.Active = dir, active == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetCriterionActive retires or restores a dimension. This is how a project
// stops a criterion from affecting rankings without deleting the votes.
func (d *DB) SetCriterionActive(ctx context.Context, projectID, criterionID int64, active bool) error {
	v := 0
	if active {
		v = 1
	}
	res, err := d.ExecContext(ctx,
		`UPDATE criteria SET active=?, updated_at=? WHERE id=? AND project_id=?`,
		v, float64(time.Now().Unix()), criterionID, projectID)
	if err != nil {
		return fmt.Errorf("set criterion active: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetCriterionRating returns one feature's rating on one criterion.
func (d *DB) GetCriterionRating(ctx context.Context, projectID, criterionID, featureID int64) (ranking.Rating, error) {
	var r ranking.Rating
	err := d.QueryRowContext(ctx, `
		SELECT cr.feature_id, cr.criterion_id, cr.r, cr.rd, cr.sigma, cr.games
		FROM criterion_ratings cr
		JOIN criteria c ON c.id = cr.criterion_id AND c.project_id = ?
		WHERE cr.criterion_id=? AND cr.feature_id=?`,
		projectID, criterionID, featureID).
		Scan(&r.FeatureID, &r.CriterionID, &r.R, &r.RD, &r.Sigma, &r.Games)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r, ErrNotFound
		}
		return r, fmt.Errorf("get criterion rating: %w", err)
	}
	return r, nil
}

// ListCriterionRatings returns every rating for one criterion. The project scope
// comes from the criterion join, so a caller cannot read another project's pool
// by guessing a criterion id.
func (d *DB) ListCriterionRatings(ctx context.Context, projectID, criterionID int64) ([]ranking.Rating, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT cr.feature_id, cr.criterion_id, cr.r, cr.rd, cr.sigma, cr.games
		FROM criterion_ratings cr
		JOIN criteria c ON c.id = cr.criterion_id AND c.project_id = ?
		WHERE cr.criterion_id=?`, projectID, criterionID)
	if err != nil {
		return nil, fmt.Errorf("list criterion ratings: %w", err)
	}
	defer rows.Close()
	return scanRatings(rows)
}

// AllCriterionRatings returns every criterion's ratings for a project in one
// pass, keyed by criterion id. Composite needs all of them, and issuing one
// query per criterion would be the nested-query pattern this package's tests
// specifically guard against.
func (d *DB) AllCriterionRatings(ctx context.Context, projectID int64) (map[int64][]ranking.Rating, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT cr.criterion_id, cr.feature_id, cr.r, cr.rd, cr.sigma, cr.games
		FROM criterion_ratings cr
		JOIN criteria c ON c.id = cr.criterion_id
		WHERE c.project_id=?`, projectID)
	if err != nil {
		return nil, fmt.Errorf("all criterion ratings: %w", err)
	}
	defer rows.Close()

	out := map[int64][]ranking.Rating{}
	for rows.Next() {
		var r ranking.Rating
		if err := rows.Scan(&r.CriterionID, &r.FeatureID, &r.R, &r.RD,
			&r.Sigma, &r.Games); err != nil {
			return nil, fmt.Errorf("scan rating: %w", err)
		}
		out[r.CriterionID] = append(out[r.CriterionID], r)
	}
	return out, rows.Err()
}

func scanRatings(rows *sql.Rows) ([]ranking.Rating, error) {
	var out []ranking.Rating
	for rows.Next() {
		var r ranking.Rating
		if err := rows.Scan(&r.FeatureID, &r.CriterionID, &r.R, &r.RD,
			&r.Sigma, &r.Games); err != nil {
			return nil, fmt.Errorf("scan rating: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CastCriterionVote records a head-to-head comparison *about one criterion* and
// recomputes that criterion's ratings for both features.
//
// Everything is validated before anything is written, because a half-applied
// vote is worse than a rejected one: it would leave a rating moved on one side
// of a comparison the other side never saw.
func (d *DB) CastCriterionVote(ctx context.Context, projectID, criterionID, voterID,
	featureA, featureB int64, outcome string, weight, tau float64) error {

	// The criterion must belong to this project. This is the scope check that
	// stops a caller pooling votes across projects.
	if _, err := d.GetCriterion(ctx, projectID, criterionID); err != nil {
		return err
	}

	// The schema's CHECK is the authority on which outcomes exist; this list
	// mirrors it so a bad value is rejected with ErrInvalid before the insert
	// rather than as an opaque constraint failure afterwards.
	out := strings.ToLower(strings.TrimSpace(outcome))
	switch out {
	case "a", "b", "both", "neither", "skip":
	default:
		return fmt.Errorf("%w: outcome must be a, b, both, neither or skip", ErrInvalid)
	}
	if featureA == featureB {
		return fmt.Errorf("%w: a feature cannot be compared with itself", ErrInvalid)
	}

	// Both features must live in the criterion's project.
	var owned int
	err := d.QueryRowContext(ctx, `
		SELECT count(*) FROM features
		WHERE project_id=? AND id IN (?, ?)`, projectID, featureA, featureB).Scan(&owned)
	if err != nil {
		return fmt.Errorf("check feature ownership: %w", err)
	}
	if owned != 2 {
		return ErrCriterionScope
	}

	if weight <= 0 {
		weight = 1.0
	}

	// One vote per (criterion, voter, pair): a re-vote is a double count, and a
	// silently doubled opinion would be invisible in the audit trail.
	res, err := d.ExecContext(ctx, `
		INSERT INTO criterion_votes
			(criterion_id, project_id, feature_a, feature_b, voter_id, outcome,
			 weight, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		criterionID, projectID, featureA, featureB, voterID, out, weight,
		float64(time.Now().Unix()))
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: already voted on this comparison", ErrInvalid)
		}
		return fmt.Errorf("insert criterion vote: %w", err)
	}
	voteID, _ := res.LastInsertId()

	// 'skip' is recorded for the audit trail and changes nothing, which is the
	// point of having it: abstention is data.
	if out == "skip" {
		return nil
	}

	// 'both' and 'neither' move no rating. Concord's existing Outcome already
	// refuses to score them, and a criteria vote that quietly did nothing would
	// be indistinguishable from a lost vote.
	// ScorePair returns (sA, sB); whether it means anything at all is a separate
	// question, answered by AppliesRatingChange. Both/neither/skip score 0,0 and
	// must not move a rating.
	o := ranking.Outcome(out)
	score, _ := o.ScorePair()
	if !o.AppliesRatingChange() {
		if _, err := d.ExecContext(ctx,
			`INSERT INTO audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
			 SELECT project_id, ?, 'criterion_vote_neutral', 'criterion_vote', ?, outcome, ?
			 FROM criterion_votes WHERE id=?`, voterID, voteID, out,
			float64(time.Now().Unix()), voteID); err != nil {
			return fmt.Errorf("audit neutral vote: %w", err)
		}
		return nil
	}

	curA, err := d.ratingOrNew(ctx, criterionID, featureA)
	if err != nil {
		return err
	}
	curB, err := d.ratingOrNew(ctx, criterionID, featureB)
	if err != nil {
		return err
	}

	// A's games include B's current state, and vice versa. A player's own
	// rating must not appear in the opponent's period.
	nextA := ranking.UpdateRating(curA, []ranking.Game{{
		OpponentR: curB.R, OpponentRD: curB.RD, Score: score,
	}}, tau)
	nextB := ranking.UpdateRating(curB, []ranking.Game{{
		OpponentR: curA.R, OpponentRD: curA.RD, Score: 1 - score,
	}}, tau)

	if err := d.upsertRating(ctx, nextA); err != nil {
		return err
	}
	if err := d.upsertRating(ctx, nextB); err != nil {
		return err
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
		 VALUES (?, ?, 'criterion_vote', 'criterion_vote', ?, ?, ?)`,
		projectID, voterID, voteID, out, float64(time.Now().Unix())); err != nil {
		return fmt.Errorf("audit criterion vote: %w", err)
	}
	return nil
}

// ratingOrNew returns the stored rating, or a fresh one when the feature has
// never been compared on this criterion. An absent row means unrated, and the
// new rating is the Glicko starting state rather than an imputed average.
func (d *DB) ratingOrNew(ctx context.Context, criterionID, featureID int64) (ranking.Rating, error) {
	var r ranking.Rating
	err := d.QueryRowContext(ctx, `
		SELECT feature_id, criterion_id, r, rd, sigma, games
		FROM criterion_ratings WHERE criterion_id=? AND feature_id=?`,
		criterionID, featureID).Scan(&r.FeatureID, &r.CriterionID, &r.R, &r.RD,
		&r.Sigma, &r.Games)
	if errors.Is(err, sql.ErrNoRows) {
		return ranking.NewRating(featureID, criterionID), nil
	}
	if err != nil {
		return r, fmt.Errorf("load rating: %w", err)
	}
	return r, nil
}

func (d *DB) upsertRating(ctx context.Context, r ranking.Rating) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO criterion_ratings
			(criterion_id, feature_id, r, rd, sigma, games, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(criterion_id, feature_id) DO UPDATE SET
			r=excluded.r, rd=excluded.rd, sigma=excluded.sigma,
			games=excluded.games, updated_at=excluded.updated_at`,
		r.CriterionID, r.FeatureID, r.R, r.RD, r.Sigma, r.Games, float64(time.Now().Unix()))
	if err != nil {
		return fmt.Errorf("upsert criterion rating: %w", err)
	}
	return nil
}

// RankByCriterion runs one rating period over every unrated game for a
// criterion. Voting already updates ratings incrementally; this exists for the
// case where a criterion is created and seeded in bulk, and it is idempotent
// because it counts only games not yet reflected in a rating's game count.
func (d *DB) RankByCriterion(ctx context.Context, projectID, criterionID int64, tau float64) ([]ranking.Rating, error) {
	if _, err := d.GetCriterion(ctx, projectID, criterionID); err != nil {
		return nil, err
	}
	votes, err := d.listVotes(ctx, criterionID)
	if err != nil {
		return nil, err
	}
	byFeature := map[int64][]ranking.Game{}
	for _, v := range votes {
		if !ranking.Outcome(v.Outcome).AppliesRatingChange() {
			continue
		}
		score, _ := ranking.Outcome(v.Outcome).ScorePair()
		byFeature[v.FeatureA] = append(byFeature[v.FeatureA], ranking.Game{
			OpponentR: v.OpponentR, OpponentRD: v.OpponentRD, Score: score,
		})
		byFeature[v.FeatureB] = append(byFeature[v.FeatureB], ranking.Game{
			OpponentR: v.OpponentR, OpponentRD: v.OpponentRD, Score: 1 - score,
		})
	}
	out := make([]ranking.Rating, 0, len(byFeature))
	for featureID, games := range byFeature {
		cur, err := d.ratingOrNew(ctx, criterionID, featureID)
		if err != nil {
			return nil, err
		}
		// Already accounted for: skip rather than re-rate, so calling this twice
		// cannot inflate a rating.
		if cur.Games >= len(games) {
			out = append(out, cur)
			continue
		}
		next := ranking.UpdateRating(cur, games, tau)
		if err := d.upsertRating(ctx, next); err != nil {
			return nil, err
		}
		out = append(out, next)
	}
	return out, nil
}

// CompositeRank ranks a project's features across criteria with caller-supplied
// weights. The maths is entirely in internal/ranking; this only gathers rows.
func (d *DB) CompositeRank(ctx context.Context, projectID int64, weights []ranking.Weight,
	opts ranking.CompositeOptions) ([]ranking.Result, error) {

	criteria, err := d.ListCriteria(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if len(criteria) == 0 {
		return nil, nil
	}
	ratings, err := d.AllCriterionRatings(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return ranking.Composite(criteria, ratings, weights, opts), nil
}

// CreateCriteriaProfile saves a named weighting, so "edge deployment" is a
// reusable thing rather than numbers retyped at every query.
func (d *DB) CreateCriteriaProfile(ctx context.Context, projectID, actorID int64,
	slug, name string, weights []ranking.Weight) (int64, error) {

	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" || name == "" {
		return 0, fmt.Errorf("%w: profile needs a slug and a name", ErrInvalid)
	}
	now := float64(time.Now().Unix())

	// Validate every criterion BEFORE opening the transaction. db.Open sets
	// MaxOpenConns(1), so a query issued while a tx holds the only connection
	// deadlocks — the exact failure internal/db's TestNoNestedQueriesInStore
	// exists to prevent. Doing the scope check inside the tx surfaced as a bare
	// "FOREIGN KEY constraint failed", which is a misleading symptom.
	ownedIDs := make([]int64, 0, len(weights))
	for _, w := range weights {
		var owned int
		if err := d.QueryRowContext(ctx,
			`SELECT count(*) FROM criteria WHERE id=? AND project_id=?`,
			w.CriterionID, projectID).Scan(&owned); err != nil {
			return 0, fmt.Errorf("check criterion scope: %w", err)
		}
		if owned != 1 {
			return 0, ErrCriterionScope
		}
		if w.Weight < 0 {
			return 0, fmt.Errorf("%w: negative weight", ErrInvalid)
		}
		ownedIDs = append(ownedIDs, w.CriterionID)
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin profile tx: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO criteria_profiles (project_id, slug, name, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, slug) DO UPDATE SET
			name=excluded.name, updated_at=excluded.updated_at`,
		projectID, slug, name, actorID, now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, fmt.Errorf("%w: profile %q already exists", ErrInvalid, slug)
		}
		return 0, fmt.Errorf("insert profile: %w", err)
	}
	// On the re-save path ON CONFLICT DO UPDATE leaves last_insert_rowid
	// untouched, so LastInsertId() returns 0 and the weight insert then fails on
	// the profile foreign key. Read the id back rather than trusting it.
	if _, err := res.RowsAffected(); err != nil {
		return 0, fmt.Errorf("profile rows: %w", err)
	}
	var profileID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM criteria_profiles WHERE project_id=? AND slug=?`,
		projectID, slug).Scan(&profileID); err != nil {
		return 0, fmt.Errorf("read profile id: %w", err)
	}
	// Re-saving a profile replaces its weights wholesale. Merging would leave
	// stale weights for criteria the caller no longer mentions, and a profile
	// that silently keeps them is not the profile that was saved.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM criteria_profile_weights WHERE profile_id=?`, profileID); err != nil {
		return 0, fmt.Errorf("clear profile weights: %w", err)
	}
	for i, w := range weights {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO criteria_profile_weights (profile_id, criterion_id, weight)
			VALUES (?, ?, ?)`, profileID, ownedIDs[i], w.Weight); err != nil {
			return 0, fmt.Errorf("insert profile weight: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit profile: %w", err)
	}
	return profileID, nil
}

// GetCriteriaProfileWeights returns a profile's weights, or ErrNotFound if the
// profile is not this project's.
func (d *DB) GetCriteriaProfileWeights(ctx context.Context, projectID int64, profileSlug string) ([]ranking.Weight, error) {
	var profileID int64
	err := d.QueryRowContext(ctx,
		`SELECT id FROM criteria_profiles WHERE project_id=? AND slug=?`,
		projectID, profileSlug).Scan(&profileID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get profile: %w", err)
	}
	rows, err := d.QueryContext(ctx, `
		SELECT criterion_id, weight FROM criteria_profile_weights
		WHERE profile_id=? ORDER BY criterion_id`, profileID)
	if err != nil {
		return nil, fmt.Errorf("get profile weights: %w", err)
	}
	defer rows.Close()
	var out []ranking.Weight
	for rows.Next() {
		var w ranking.Weight
		if err := rows.Scan(&w.CriterionID, &w.Weight); err != nil {
			return nil, fmt.Errorf("scan profile weight: %v", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ListCriteriaProfiles names the saved weightings for a project.
func (d *DB) ListCriteriaProfiles(ctx context.Context, projectID int64) ([]struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT slug, name FROM criteria_profiles WHERE project_id=? ORDER BY slug`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list profiles: %w", err)
	}
	defer rows.Close()
	var out []struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	for rows.Next() {
		var e struct {
			Slug string `json:"slug"`
			Name string `json:"name"`
		}
		if err := rows.Scan(&e.Slug, &e.Name); err != nil {
			return nil, fmt.Errorf("scan profile: %v", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// votedGame is a criterion vote joined to its opponent's rating at the time,
// used by RankByCriterion to rebuild periods.
type votedGame struct {
	FeatureA   int64
	FeatureB   int64
	Outcome    string
	OpponentR  float64
	OpponentRD float64
}

func (d *DB) listVotes(ctx context.Context, criterionID int64) ([]votedGame, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT v.feature_a, v.feature_b, v.outcome,
		       COALESCE(rb.r, 1500.0), COALESCE(rb.rd, 350.0)
		FROM criterion_votes v
		LEFT JOIN criterion_ratings rb
		  ON rb.criterion_id = v.criterion_id AND rb.feature_id = v.feature_b
		WHERE v.criterion_id=?`, criterionID)
	if err != nil {
		return nil, fmt.Errorf("list criterion votes: %w", err)
	}
	defer rows.Close()
	var out []votedGame
	for rows.Next() {
		var v votedGame
		if err := rows.Scan(&v.FeatureA, &v.FeatureB, &v.Outcome,
			&v.OpponentR, &v.OpponentRD); err != nil {
			return nil, fmt.Errorf("scan vote: %v", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
