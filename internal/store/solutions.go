package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Solutions (spec revision 4 §6.3-6.5).
//
// A feature states what outcome and why; a solution states how. A feature has
// many competing solutions, ranked in the feature's own solution arena under
// "Which approach should we build?".
//
// The arena engine already scores any entity type, so ranking is not the work
// here. The work is the three things §6.3 and §6.4 add around it:
//
//   - a permanent baseline per arena ("do nothing"), without which "beats doing
//     nothing" is not a claim anyone can check;
//   - coverage claims that can be contested, because coverage is a term in the
//     score and an unchallengeable claim makes that term decorative;
//   - forking, so a good idea is refined rather than restarted.

// ErrSelfVote and ErrSameEntityVote are declared in arenas.go, beside the vote
// path that raises them: §5.2's rule is about ranking, and solutions reuse the
// same rule rather than restating it.
var (
	// ErrNotABaseline is returned when something treats a non-baseline entry as
	// the "do nothing" competitor.
	ErrNotABaseline = errors.New("entry is not the do-nothing baseline")

	// ErrContestedClaim is returned when a contested coverage claim is read as
	// counting toward coverage (§6.4: "the claim must stand to count").
	ErrContestedClaim = errors.New("coverage claim is contested and does not count")
)

// Solution type constants (§6.3's list).
const (
	SolutionBuildNew     = "build-new"
	SolutionExtend       = "extend-existing"
	SolutionIntegrateExt = "integrate-external"
	SolutionConfigDocs   = "config-or-docs-only"
	SolutionWorkaround   = "workaround"
	// SolutionDoNothing is the baseline type. A solution only matters if it
	// beats it (§6.3).
	SolutionDoNothing = "do-nothing"
)

// Solution relationship values (§6.3).
const (
	// RelationshipExclusive solutions compete with the others. The default.
	RelationshipExclusive = "exclusive"
	// RelationshipComplementary solutions stack, are phased, and are NOT
	// compared against their complements -- so they must not be put in the same
	// arena.
	RelationshipComplementary = "complementary"
)

// DefaultSolutionKappa is §6.4's default weight on coverage.
const DefaultSolutionKappa = 200.0

// Solution is one proposed way to deliver a feature's outcome.
type Solution struct {
	ID        int64 `json:"id"`
	FeatureID int64 `json:"feature_id"`
	AuthorID  int64 `json:"author_id"`

	Title       string         `json:"title"`
	Body        string         `json:"body"`
	Type        string         `json:"type"`
	ExternalRef sql.NullString `json:"external_ref"`
	Affiliation sql.NullString `json:"affiliation"`
	// Relationship is exclusive or complementary (§6.3).
	Relationship string `json:"relationship"`

	// ParentSolutionID is set for a forked solution (§6.3: "same, but with X").
	ParentSolutionID sql.NullInt64 `json:"parent_solution_id"`

	Status        string `json:"status"`
	ExpertiseTags string `json:"expertise_tags"`

	// Rating is the arena entry for this solution, joined in for display. Zero
	// when the solution has not been added to an arena yet.
	Rating ArenaEntry

	CreatedAt float64
	UpdatedAt float64
}

// IsBaseline reports whether this solution is the arena's "do nothing"
// competitor. A solution is the baseline by its type, not by a separate flag: two
// ways of saying the same thing is how the arena ended up consulting a column
// nothing populated and scoring every "neither" as a draw.
func (s *Solution) IsBaseline() bool {
	return s.Type == SolutionDoNothing
}

// SolutionCoverage is one §6.3 coverage claim: this solution resolves, or
// explicitly leaves unresolved, this complaint.
type SolutionCoverage struct {
	SolutionID  int64  `json:"solution_id"`
	ComplaintID int64  `json:"complaint_id"`
	Claim       string `json:"claim"`

	Contested     bool            `json:"contested"`
	ContestedBy   sql.NullInt64   `json:"contested_by"`
	ContestedAt   sql.NullFloat64 `json:"contested_at"`
	ContestReason sql.NullString  `json:"contest_reason"`

	CreatedAt float64 `json:"created_at"`
}

// Counts reports whether this claim contributes to coverage.
//
// §6.4: "Coverage claims are challengeable. A reviewer can contest a claim, and
// the claim must stand to count." A contested claim contributes nothing in
// either direction -- an unproven "leaves unresolved" should not lower a
// solution's score, any more than an unproven "resolves" should raise it.
func (c *SolutionCoverage) Counts() bool {
	return !c.Contested
}

// CoverageScore is §6.4's coverage term: the pain of the complaints a solution
// verifiably resolves, over the pain of all the feature's linked complaints.
//
// Complaint pain is severity × frequency × strategic_multiplier, the same
// measure the priority ranking uses, so a solution that covers the loudest
// complaint outranks one that covers the quietest. Clamped to 0..1: a solution
// claiming more pain than the feature actually has is a bad claim, not a high
// score, and without the clamp a single inflated claim could dominate the
// ranking.
type CoverageScore struct {
	// Coverage is 0..1.
	Coverage float64 `json:"coverage"`
	// ResolvedPain, TotalPain and ClaimedPain are the raw pain sums, kept so a
	// caller can show "resolves 340 of 510 pain" rather than only a percentage.
	ResolvedPain float64 `json:"resolved_pain"`
	TotalPain    float64 `json:"total_pain"`
	ClaimedPain  float64 `json:"claimed_pain"`
	// Contested is how many claims are currently contested, so the UI can say so
	// rather than quietly using a smaller denominator.
	Contested int `json:"contested"`
}

// SolutionScore is §6.4's score and its parts.
//
// JSON tags are explicit because this type is served directly as the board's
// rows. Without them Go's default marshalling emits "Score", "IsBaseline" and
// so on, and the frontend would have to know that -- inconsistent with every
// other response in this API, where fields are snake_case.
//
// StableHours is §6.5's third condition for opening a call (the position has been
// stable for N hours). It is reported as 0 until the stability tracker exists;
// the consensus-opening flow is not written yet, and reporting 0 says "not
// measured" rather than claiming stability nobody has checked.
type SolutionScore struct {
	SolutionID int64 `json:"solution_id"`
	// Title is the proposal's own text. Added 2026-10-03 for the project page's
	// standings panel: without it the panel renders a column of scores with no
	// way to tell which proposal is which, which is not a ranking a reader can
	// use. §6.5's conditions are all numbers, so nothing that already depended on
	// this struct needed it, which is why it can be added without a gate entry.
	Title string `json:"title"`
	// Rating is the conservative Glicko score, r - 2*RD, that §5.1 displays.
	Rating float64 `json:"rating"`
	// Coverage is 0..1.
	Coverage float64 `json:"coverage"`
	// Kappa is the charter-configurable weight from §6.4.
	Kappa float64 `json:"kappa"`
	// Score = Rating + Kappa*Coverage.
	Score float64 `json:"score"`
	// IsBaseline marks the "do nothing" competitor, which is ranked but never
	// selectable.
	IsBaseline bool `json:"is_baseline"`
	// DistinctVoters is how many different people judged it, which §6.5 requires
	// before a call may open on it.
	DistinctVoters int `json:"distinct_voters"`
	// StableHours is how long the solution has held its position, §6.5's third
	// condition for opening a call.
	StableHours float64 `json:"stable_hours"`
}

// CreateSolutionInput is the write side of §6.3.
type CreateSolutionInput struct {
	FeatureID   int64
	AuthorID    int64
	Title       string
	Body        string
	Type        string
	ExternalRef string
	Affiliation string
	// Relationship defaults to exclusive when empty.
	Relationship  string
	ExpertiseTags string
	// ParentSolutionID forks this solution from an existing one (§6.3).
	ParentSolutionID int64
}

// CreateSolution records a solution and registers it in the feature's solution
// arena (§6.3), creating the arena if the feature has none.
//
// A forked solution inherits its parent's rating with inflated RD, so a variant
// of a good idea starts from evidence rather than from the prior. RD is doubled
// rather than reset to 350: the fork is probably close to its parent, so the
// arena's uncertainty about it is genuinely lower than about a stranger, but
// nothing about the change itself has been tested, so it is not the parent's
// certainty either.
func (d *DB) CreateSolution(ctx context.Context, in CreateSolutionInput) (Solution, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return Solution{}, fmt.Errorf("%w: a solution needs a title", ErrInvalid)
	}
	if in.Type == "" {
		in.Type = SolutionBuildNew
	}
	if in.Relationship == "" {
		in.Relationship = RelationshipExclusive
	}
	// §6.3: "integrate-external (a catalog link)". Checked here as well as by the
	// schema because the error is much more useful before the insert.
	if in.Type == SolutionIntegrateExt && strings.TrimSpace(in.ExternalRef) == "" {
		return Solution{}, fmt.Errorf("%w: an integrate-external solution needs a catalog link", ErrInvalid)
	}
	// A complementary solution is stackable, so it does not compete. Putting one
	// in the same arena as its rivals would have the engine compare "do A, then
	// B" against "do C", which is not a question anybody can answer.
	if in.Relationship == RelationshipComplementary {
		return Solution{}, fmt.Errorf(
			"%w: a complementary solution is not ranked against its rivals; it needs its own arena",
			ErrInvalid)
	}

	feature, err := d.GetFeature(ctx, in.FeatureID)
	if err != nil {
		return Solution{}, err
	}
	// §5.2: an author may not vote on their own entry, and a solution that could
	// be voted on by its author is not a solution anybody else can judge.
	if feature.AuthorID == in.AuthorID {
		return Solution{}, fmt.Errorf("%w: a feature's author cannot author its solutions", ErrSelfVote)
	}

	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO solutions
			(feature_id, author_id, title, body, type, external_ref, affiliation,
			 relationship, parent_solution_id, status, expertise_tags,
			 created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, 0),
		        'draft', ?, ?, ?)`,
		in.FeatureID, in.AuthorID, title, strings.TrimSpace(in.Body), in.Type,
		strings.TrimSpace(in.ExternalRef), strings.TrimSpace(in.Affiliation),
		in.Relationship, nullableID(in.ParentSolutionID), in.ExpertiseTags, now, now)
	if err != nil {
		return Solution{}, fmt.Errorf("insert solution: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Solution{}, err
	}

	arena, err := d.EnsureArena(ctx, ArenaSolution, feature.ProjectID, in.FeatureID, "", "")
	if err != nil {
		return Solution{}, fmt.Errorf("solution arena: %w", err)
	}
	if err := d.UpsertArenaEntry(ctx, arena.ID, EntitySolution, id); err != nil {
		return Solution{}, fmt.Errorf("register solution in arena: %w", err)
	}

	// A forked solution starts near its parent, with more uncertainty.
	if in.ParentSolutionID != 0 {
		if err := d.inheritParentRating(ctx, arena.ID, id, in.ParentSolutionID); err != nil {
			return Solution{}, err
		}
	}

	// §6.3: "Every solution arena has a permanent baseline." The first
	// do-nothing solution in a feature's arena becomes it.
	if in.Type == SolutionDoNothing {
		if err := d.setBaseline(ctx, arena.ID, EntitySolution, id); err != nil {
			return Solution{}, err
		}
	}

	return d.GetSolution(ctx, id)
}

// inheritParentRating copies the parent's rating into the fork with doubled RD
// (§6.3: "inherits the parent's rating with inflated RD, so good ideas aren't
// forced to start from zero").
func (d *DB) inheritParentRating(ctx context.Context, arenaID, forkID, parentSolutionID int64) error {
	parent, err := d.GetArenaEntry(ctx, arenaID, EntitySolution, parentSolutionID)
	if err != nil {
		// A parent outside this feature's arena is a mistake worth reporting: the
		// fork would silently be ranked as a stranger.
		return fmt.Errorf("fork parent %d is not in arena %d: %w", parentSolutionID, arenaID, ErrNotFound)
	}
	if forkID == parentSolutionID {
		return fmt.Errorf("%w: a solution cannot fork itself", ErrInvalid)
	}
	now := float64(time.Now().Unix())
	if _, err := d.ExecContext(ctx, `
		UPDATE arena_entries
		SET r = ?, rd = ?, updated_at = ?
		WHERE arena_id = ? AND entity_type = 'solution' AND entity_id = ?`,
		parent.R, parent.RD*2, now, arenaID, forkID); err != nil {
		return fmt.Errorf("inherit rating: %w", err)
	}
	return nil
}

// setBaseline flags an arena entry as the do-nothing competitor, clearing any
// previous baseline in the same arena.
//
// One baseline per arena is the invariant, and the previous holder is demoted
// rather than deleted: its votes are history (§2.5) and dropping the row would
// destroy them.
func (d *DB) setBaseline(ctx context.Context, arenaID int64, entityType string, entityID int64) error {
	now := float64(time.Now().Unix())
	if _, err := d.ExecContext(ctx,
		`UPDATE arena_entries SET is_baseline = 0, updated_at = ?
		 WHERE arena_id = ? AND is_baseline = 1`, now, arenaID); err != nil {
		return fmt.Errorf("clear previous baseline: %w", err)
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE arena_entries SET is_baseline = 1, updated_at = ?
		 WHERE arena_id = ? AND entity_type = ? AND entity_id = ?`,
		now, arenaID, entityType, entityID); err != nil {
		return fmt.Errorf("set baseline: %w", err)
	}
	return nil
}

// GetSolution reads one solution with its arena rating joined in.
func (d *DB) GetSolution(ctx context.Context, id int64) (Solution, error) {
	row := d.QueryRowContext(ctx, `
		SELECT s.id, s.feature_id, s.author_id, s.title, s.body, s.type,
		       s.external_ref, s.affiliation, s.relationship, s.parent_solution_id,
		       s.status, s.expertise_tags, s.created_at, s.updated_at,
		       COALESCE(e.r, 0), COALESCE(e.rd, 0), COALESCE(e.sigma, 0),
		       COALESCE(e.games, 0), COALESCE(e.is_baseline, 0)
		FROM solutions s
		LEFT JOIN arena_entries e
		  ON e.entity_type = 'solution' AND e.entity_id = s.id
		WHERE s.id = ?`, id)
	return scanSolution(id, row)
}

func scanSolution(id int64, row *sql.Row) (Solution, error) {
	var s Solution
	err := row.Scan(&s.ID, &s.FeatureID, &s.AuthorID, &s.Title, &s.Body, &s.Type,
		&s.ExternalRef, &s.Affiliation, &s.Relationship, &s.ParentSolutionID,
		&s.Status, &s.ExpertiseTags, &s.CreatedAt, &s.UpdatedAt,
		&s.Rating.R, &s.Rating.RD, &s.Rating.Sigma, &s.Rating.Games,
		&s.Rating.IsBaseline)
	if errors.Is(err, sql.ErrNoRows) {
		return Solution{}, fmt.Errorf("%w: solution %d", ErrNotFound, id)
	}
	if err != nil {
		return Solution{}, err
	}
	s.Rating.EntityType = EntitySolution
	s.Rating.EntityID = s.ID
	s.Rating.setDerived()
	return s, nil
}

// ListSolutions returns a feature's solutions, strongest first, each with its
// §6.4 score and coverage.
//
// Ranked by score, not by rating: §6.4's whole claim is that coverage changes the
// order, so a leaderboard sorted on the rating alone would be a leaderboard the
// spec does not describe.
func (d *DB) ListSolutions(ctx context.Context, featureID int64, limit int) ([]SolutionScore, error) {
	feature, err := d.GetFeature(ctx, featureID)
	if err != nil {
		return nil, err
	}
	arena, err := d.FindArena(ctx, ArenaSolution, feature.ProjectID, featureID, "")
	if err != nil {
		if errors.Is(err, ErrArenaNotFound) {
			// A feature nobody has proposed a solution to is not an error; it has
			// no arena yet.
			return nil, nil
		}
		return nil, err
	}
	entries, err := d.ArenaLeaderboard(ctx, arena.ID, limit)
	if err != nil {
		return nil, err
	}
	kappa, err := d.solutionKappa(ctx, feature.ProjectID)
	if err != nil {
		return nil, err
	}

	out := make([]SolutionScore, 0, len(entries))
	for _, e := range entries {
		// The arena may hold an entry whose solution was deleted, or a baseline
		// row from before solutions existed. Both are skipped rather than
		// reported as zero-score solutions.
		var exists int
		if err := d.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM solutions WHERE id = ?`, e.EntityID).Scan(&exists); err != nil {
			return nil, err
		}
		if exists == 0 {
			continue
		}
		cov, err := d.SolutionCoverageScore(ctx, featureID, e.EntityID)
		if err != nil {
			return nil, err
		}
		voters, err := d.distinctSolutionVoters(ctx, arena.ID, e.EntityID)
		if err != nil {
			return nil, err
		}
		score := SolutionScore{
			SolutionID:     e.EntityID,
			Rating:         e.Conservative,
			Coverage:       cov.Coverage,
			Kappa:          kappa,
			IsBaseline:     e.IsBaseline,
			DistinctVoters: voters,
		}
		// The title, for the same reason the row's existence check above exists:
		// a list of scores with no names attached is a ranking nobody can act on.
		// One indexed lookup per entry, on a page read.
		if err := d.QueryRowContext(ctx,
			`SELECT title FROM solutions WHERE id = ?`, e.EntityID).Scan(&score.Title); err != nil {
			return nil, err
		}
		score.Score = score.Rating + kappa*cov.Coverage
		out = append(out, score)
	}
	sortSolutionScores(out)
	return out, nil
}

// sortSolutionScores orders by score, keeping the baseline last regardless.
//
// The baseline is the thing to beat, not a competitor: showing it at the top of
// a list where everything else is ranked above "do nothing" is fine, but showing
// it as the winner would read as a recommendation to do nothing, which is the
// opposite of what §6.3 means by including it.
func sortSolutionScores(scores []SolutionScore) {
	for i := 1; i < len(scores); i++ {
		for j := i; j > 0; j-- {
			a, b := scores[j-1], scores[j]
			swap := a.Score < b.Score
			if a.IsBaseline && !b.IsBaseline {
				swap = false
			}
			if !b.IsBaseline && a.IsBaseline {
				swap = true
			}
			if !swap {
				break
			}
			scores[j-1], scores[j] = scores[j], scores[j-1]
		}
	}
}

// CountSolutions returns how many solutions a feature has.
//
// It exists because the project page's standings panel must say how many it
// did not show, and the obvious way to learn that -- call ListSolutions with
// limit+1 and look for one extra row -- does not work:
//
//   - ArenaLeaderboard clamps any limit above 200 to 50, so a limit of 6 is fine
//     but the boundary is not a contract.
//   - It applies LIMIT in SQL and ListSolutions then SKIPS arena entries whose
//     solution row is gone, so the number of rows returned can be smaller than
//     the limit for reasons that have nothing to do with how many solutions
//     there are. "One more row" is therefore not a reliable witness of "there is
//     one more solution", and a panel that trusts it reports a wrong omitted
//     count.
//
// Counting the solutions directly is unambiguous. It is a second query rather
// than a trick, and the panel is a page read rather than a hot loop.
func (d *DB) CountSolutions(ctx context.Context, featureID int64) (int, error) {
	var n int
	err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM solutions WHERE feature_id = ?`, featureID).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// distinctSolutionVoters counts how many distinct people have judged a solution
// (§6.5 requires "enough distinct voters" before a call may open on it).
func (d *DB) distinctSolutionVoters(ctx context.Context, arenaID, entityID int64) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT voter_id) FROM pairwise_votes
		WHERE arena_id = ?
		  AND outcome != 'skip'
		  AND ((entity_type = 'solution' AND a = ?) OR (entity_type = 'solution' AND b = ?))`,
		arenaID, entityID, entityID).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// solutionKappa reads the charter's coverage weight, falling back to §6.4's
// default when unset.
func (d *DB) solutionKappa(ctx context.Context, projectID int64) (float64, error) {
	var k sql.NullFloat64
	if err := d.QueryRowContext(ctx,
		`SELECT solution_kappa FROM charters WHERE project_id = ?`, projectID).Scan(&k); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DefaultSolutionKappa, nil
		}
		return 0, err
	}
	if !k.Valid || k.Float64 <= 0 {
		return DefaultSolutionKappa, nil
	}
	return k.Float64, nil
}

// ClaimCoverage records a §6.3 coverage claim.
func (d *DB) ClaimCoverage(ctx context.Context, solutionID, complaintID int64, claim string) error {
	if claim != "resolves" && claim != "leaves-unresolved" {
		return fmt.Errorf("%w: claim must be resolves or leaves-unresolved, got %q", ErrInvalid, claim)
	}
	// A claim has to point at a complaint the feature actually links, or coverage
	// is measured against a denominator the solution chose itself.
	var linked int
	if err := d.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM feature_complaints
		WHERE feature_id = (SELECT feature_id FROM solutions WHERE id = ?)
		  AND complaint_id = ?`, solutionID, complaintID).Scan(&linked); err != nil {
		return fmt.Errorf("check linked complaint: %w", err)
	}
	if linked == 0 {
		return fmt.Errorf("%w: complaint %d is not linked to the solution's feature", ErrInvalid, complaintID)
	}

	_, err := d.ExecContext(ctx, `
		INSERT INTO solution_coverage
			(solution_id, complaint_id, claim, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (solution_id, complaint_id)
		DO UPDATE SET claim = excluded.claim, contested = 0,
		              contested_by = NULL, contested_at = NULL, contest_reason = NULL`,
		solutionID, complaintID, claim, float64(time.Now().Unix()))
	if err != nil {
		return fmt.Errorf("claim coverage: %w", err)
	}
	return nil
}

// SolutionCoverageScore computes §6.4's coverage term.
func (d *DB) SolutionCoverageScore(ctx context.Context, featureID, solutionID int64) (CoverageScore, error) {
	var out CoverageScore

	// The denominator: every complaint linked to the feature, contested or not. A
	// contested claim stays in it, because the complaint is still the pain the
	// feature exists to address.
	//
	// Pain is read through GetComplaintPain, the project's one definition of it,
	// rather than severity*frequency*multiplier. That function applies the
	// charter's halflife decay and weights by recorded impacts, so a loud new
	// complaint outranks an identical old one -- which is the whole reason pain
	// decays. Summing the raw columns instead would make coverage disagree with
	// every other pain-based ranking in the product, and the two numbers would
	// silently mean different things.
	linked, err := d.featurePainMap(ctx, featureID)
	if err != nil {
		return out, err
	}
	for _, pain := range linked {
		out.TotalPain += pain
	}
	if out.TotalPain == 0 {
		// No linked complaints means coverage is undefined, not zero and not one.
		// Zero would make every solution score 0 on coverage and flatten the
		// ranking; one would credit a solution for covering nothing.
		out.Coverage = 0
		return out, nil
	}

	var claimed int
	if err := d.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(sc.contested), 0)
		FROM solution_coverage sc
		WHERE sc.solution_id = ?`, solutionID).Scan(&claimed); err != nil {
		return out, fmt.Errorf("contested count: %w", err)
	}
	out.Contested = claimed

	rows, err := d.QueryContext(ctx, `
		SELECT sc.complaint_id, sc.claim
		FROM solution_coverage sc
		WHERE sc.solution_id = ? AND sc.contested = 0`, solutionID)
	if err != nil {
		return out, fmt.Errorf("read claims: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var complaintID int64
		var claim string
		if err := rows.Scan(&complaintID, &claim); err != nil {
			return out, err
		}
		pain, ok := linked[complaintID]
		if !ok {
			// A claim on a complaint the feature no longer links. Not an error:
			// unlinking a complaint is legitimate, and the claim is simply no
			// longer part of any denominator.
			continue
		}
		out.ClaimedPain += pain
		if claim == "resolves" {
			out.ResolvedPain += pain
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	cov := out.ResolvedPain / out.TotalPain
	if cov > 1 {
		// A solution cannot resolve more pain than the feature has. Clamping
		// rather than rejecting: the claim is the author's, and the honest
		// reading is that it does not count, not that the whole solution is bad.
		cov = 1
	}
	if cov < 0 {
		cov = 0
	}
	out.Coverage = cov
	return out, nil
}

// LinkedComplaints returns the complaints linked to a feature.
//
// §6.2 requires a feature to trace back to at least one validated complaint, so
// an empty result is a data problem rather than a normal state, and callers that
// depend on the guarantee can say so.
func (d *DB) LinkedComplaints(ctx context.Context, featureID int64) ([]int64, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT complaint_id FROM feature_complaints WHERE feature_id = ? ORDER BY complaint_id`,
		featureID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// featurePainMap is pain per linked complaint, read through the project's own
// pain function so coverage ranks against the same measure as everything else.
func (d *DB) featurePainMap(ctx context.Context, featureID int64) (map[int64]float64, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT complaint_id FROM feature_complaints WHERE feature_id = ?`, featureID)
	if err != nil {
		return nil, fmt.Errorf("read linked complaints: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The cursor is closed before the per-complaint pain reads: db.Open sets
	// SetMaxOpenConns(1), so nesting a query inside an open rows cursor
	// deadlocks. GetComplaintPain hit exactly this and every feature ranked with
	// pain 0.
	out := make(map[int64]float64, len(ids))
	for _, id := range ids {
		pain, err := d.GetComplaintPain(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("pain for complaint %d: %w", id, err)
		}
		out[id] = pain
	}
	return out, nil
}

// ContestCoverage contests a claim (§6.4). A contested claim stops counting until
// it is restated.
func (d *DB) ContestCoverage(ctx context.Context, solutionID, complaintID, contestorID int64, reason string) error {
	if strings.TrimSpace(reason) == "" {
		// A contest with no reason is indistinguishable from a deletion, and the
		// claim's author deserves to know what to answer.
		return fmt.Errorf("%w: contesting a coverage claim needs a reason", ErrInvalid)
	}
	// The contestor must not be the claimant, which is the same self-vote rule
	// §5.2 applies to rankings.
	var claimant int64
	if err := d.QueryRowContext(ctx,
		`SELECT author_id FROM solutions WHERE id = ?`, solutionID).Scan(&claimant); err != nil {
		return fmt.Errorf("read claimant: %w", err)
	}
	if claimant == contestorID {
		return fmt.Errorf("%w: an author cannot contest their own coverage claim", ErrSelfVote)
	}
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		UPDATE solution_coverage
		SET contested = 1, contested_by = ?, contested_at = ?, contest_reason = ?
		WHERE solution_id = ? AND complaint_id = ?`,
		contestorID, now, strings.TrimSpace(reason), solutionID, complaintID)
	if err != nil {
		return fmt.Errorf("contest coverage: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: no coverage claim for solution %d on complaint %d",
			ErrNotFound, solutionID, complaintID)
	}
	return nil
}

// ListCoverage returns a solution's claims, for the pro/con digest §6.4 shows.
func (d *DB) ListCoverage(ctx context.Context, solutionID int64) ([]SolutionCoverage, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT solution_id, complaint_id, claim, contested, contested_by,
		       contested_at, contest_reason, created_at
		FROM solution_coverage WHERE solution_id = ? ORDER BY complaint_id`, solutionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SolutionCoverage
	for rows.Next() {
		var c SolutionCoverage
		if err := rows.Scan(&c.SolutionID, &c.ComplaintID, &c.Claim, &c.Contested,
			&c.ContestedBy, &c.ContestedAt, &c.ContestReason, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RemoveArenaEntry refuses to remove a baseline (§6.3: "Every solution arena has
// a permanent baseline"). The refusal lives here, on the one function that can
// remove an entry, rather than in each caller.
func (d *DB) baselineIsProtected(ctx context.Context, arenaID int64, entityType string, entityID int64) error {
	var isBaseline int
	err := d.QueryRowContext(ctx,
		`SELECT is_baseline FROM arena_entries
		 WHERE arena_id = ? AND entity_type = ? AND entity_id = ?`,
		arenaID, entityType, entityID).Scan(&isBaseline)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // not present, so nothing to protect
	}
	if err != nil {
		return err
	}
	if isBaseline == 1 {
		return fmt.Errorf("%w: entry %s/%d in arena %d", ErrNotABaseline, entityType, entityID, arenaID)
	}
	return nil
}
