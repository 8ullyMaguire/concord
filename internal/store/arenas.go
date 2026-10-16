package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/ranking"
)

// Arenas: the single ranking engine (spec revision 4 §5).
//
// §5 is the structural change in r4, and this file is where it lands. Before it,
// ranking was hardcoded to features -- `pairwise_votes` named feature_a/feature_b,
// `criterion_ratings` was keyed on feature_id, and the rating lived in
// `features.elo_r`. The spec's §5 table lists six arena kinds, five of which
// cannot be expressed as a feature_id:
//
//	feature-priority   features, per project
//	solution           solutions, per feature        (§6.4)
//	list               list entries                   (§7.1)
//	request            project answers                (§7.2)
//	alternatives       projects, per use case         (§5)
//	use-case           projects                       (§5)
//
// So an arena is a set of competitors plus a question, and the rating triple
// (r, rd, sigma) moves out of the entity's own table and into `arena_entries`.
// Features keep their columns as well -- 532 complaints, 437 features and a live
// instance depend on `features.elo_r`, and a move that rewrote every rating
// would need vote-log backfill that does not exist yet. `syncFeatureEntry` keeps
// the two in step for features and nothing else reads the old columns.
//
// The invariant that makes §2.5 true ("every ranking is recomputable from the
// public vote log") is that the vote log is the only writer of ratings, and
// ratings are always derived. Nothing here stores a score that a vote log could
// not reproduce.

// Arena type constants (§5's table, and the CHECK constraint in 0017).
const (
	ArenaFeaturePriority = "feature-priority"
	ArenaSolution        = "solution"
	ArenaList            = "list"
	ArenaRequest         = "request"
	ArenaAlternatives    = "alternatives"
	ArenaUseCase         = "use-case"
)

// Arena entry entity types.
const (
	EntityFeature   = "feature"
	EntitySolution  = "solution"
	EntityProject   = "project"
	EntityListEntry = "list_entry"
)

// Arena is a set of competitors, a question, and a context (§5).
type Arena struct {
	ID        int64   `json:"id"`
	Type      string  `json:"type"`
	ProjectID int64   `json:"project_id"`
	FeatureID int64   `json:"feature_id"`
	Question  string  `json:"question"`
	UseCase   string  `json:"use_case,omitempty"`
	CreatedAt float64 `json:"created_at"`

	// Count is the number of competitors, filled by the list queries because a
	// client showing "3 options" should not have to count them.
	Count int `json:"count"`
}

// ArenaEntry is one competitor in an arena.
type ArenaEntry struct {
	ArenaID    int64   `json:"arena_id"`
	EntityType string  `json:"entity_type"`
	EntityID   int64   `json:"entity_id"`
	R          float64 `json:"r"`
	RD         float64 `json:"rd"`
	Sigma      float64 `json:"sigma"`
	Games      int     `json:"games"`

	// IsBaseline marks the permanent do-nothing competitor (§6.3).
	IsBaseline bool `json:"is_baseline"`
	// ParentEntryID is set on a derived solution forked from another (§6.3).
	ParentEntryID int64   `json:"parent_entry_id,omitempty"`
	UpdatedAt     float64 `json:"updated_at"`

	// Conservative is `r - 2*RD` (§5.1's displayed score). A field rather than a
	// method so it serialises into the leaderboard the UI reads directly.
	Conservative float64 `json:"conservative"`
	// WinProbabilityAgainstLeader is E(r, r_leader, RD, RD_leader) (§5.1:
	// "A is better with 87% confidence"). Zero when the entry leads.
	WinProbabilityAgainstLeader float64 `json:"win_probability_against_leader"`
}

// ErrArenaNotFound is returned for an arena that does not exist.
//
// It WRAPS ErrNotFound, and it did not until 2026-10-03. As a bare
// `errors.New("arena not found")` it matched no arm in the HTTP layer's
// mapError, so `GET /api/v1/projects/{p}/alternatives/999999/...` answered 500
// with `{"error":"arena not found"}` while an arena belonging to ANOTHER project
// answered 404 with `{"error":"not found"}`. Two answers, both naming the arena
// kind, one of them a server error for what is plainly a missing row — an
// existence oracle built out of a status code and a distinct string.
//
// Wrapping rather than replacing, so `errors.Is(err, store.ErrNotFound)` now
// holds and mapError's first arm catches it. solutions.go:425 already listed both
// sentinels explicitly, which is the tell that this was a known gap: the caller
// had to know about the second one because the mapping did not follow from it.
var ErrArenaNotFound = fmt.Errorf("arena not found: %w", ErrNotFound)

// setDerived fills the display fields from the stored triple.
func (e *ArenaEntry) setDerived() {
	e.Conservative = e.R - 2*e.RD
}

// ErrSelfVote is returned when a voter judges a competitor against itself.
var ErrSelfVote = errors.New("cannot vote on your own entry")

// ErrSameEntityVote is returned when both sides of a pair are the same entity.
var ErrSameEntityVote = errors.New("a comparison needs two different entries")

// EnsureArena returns the arena for (type, project, feature, use case),
// creating it if absent.
//
// Idempotent by lookup rather than by INSERT OR IGNORE alone, because the unique
// indexes are partial and cover only some of the types: a bare
// INSERT OR IGNORE would insert a duplicate solution arena for a second caller
// whose conflict is on a different index. The SELECT-then-INSERT under the
// single-connection pool (PLAN.md rule 6, MaxOpenConns(1)) is atomic in practice,
// and the unique indexes are the backstop if that ever stops being true.
//
// The question is only applied at creation. A caller passing an empty question
// gets the default, because an arena with a blank question is a UI with nothing
// to ask the voter, and re-deriving it on every call would mean the stored value
// was decorative.
func (d *DB) EnsureArena(ctx context.Context, arenaType string, projectID, featureID int64, useCase, question string) (Arena, error) {
	if arenaType == "" {
		return Arena{}, fmt.Errorf("%w: arena type is required", ErrInvalid)
	}
	arena, err := d.FindArena(ctx, arenaType, projectID, featureID, useCase)
	if err == nil {
		return arena, nil
	}
	if !errors.Is(err, ErrArenaNotFound) {
		return Arena{}, err
	}

	if question == "" {
		question = DefaultArenaQuestion(arenaType)
	}
	res, err := d.ExecContext(ctx, `
		INSERT INTO arenas (type, project_id, feature_id, question, use_case, created_at)
		VALUES (?, ?, NULLIF(?, 0), ?, NULLIF(?, ''), ?)`,
		arenaType, nullableID(projectID), nullableID(featureID), question,
		strings.TrimSpace(useCase), float64(time.Now().Unix()))
	if err != nil {
		// A concurrent creator won the race. Read theirs rather than failing:
		// two callers asking for "the" arena of a feature should both get it.
		if a2, ferr := d.FindArena(ctx, arenaType, projectID, featureID, useCase); ferr == nil {
			return a2, nil
		}
		return Arena{}, err
	}
	id, _ := res.LastInsertId()
	return d.GetArena(ctx, id)
}

// DefaultArenaQuestion returns the §5 question for an arena type.
func DefaultArenaQuestion(arenaType string) string {
	switch arenaType {
	case ArenaFeaturePriority:
		return "Which should we prioritize first?"
	case ArenaSolution:
		return "Which approach should we build?"
	case ArenaList:
		return "Which is better for this category?"
	case ArenaRequest:
		return "Which fits this request better?"
	case ArenaAlternatives:
		return "Which is the better alternative?"
	case ArenaUseCase:
		return "Which is best for this use case?"
	}
	return "Which is better?"
}

// nullableID maps 0 to SQL NULL.
//
// Used instead of storing 0, because every arena query filters on
// `project_id = ?` or `feature_id = ?` and a stored 0 would match the
// "no owner" rows of a different type when a caller forgot the type.
func nullableID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

// FindArena reads one arena by its identity, not by id.
//
// The four (type, owner, use case) shapes are spelled out rather than assembled
// from a generic WHERE, because the use-case arenas key on a non-NULL use case
// and the others must NOT match a row that has one.
func (d *DB) FindArena(ctx context.Context, arenaType string, projectID, featureID int64, useCase string) (Arena, error) {
	var q string
	var args []any
	switch arenaType {
	case ArenaFeaturePriority:
		q = `SELECT id, type, project_id, feature_id, question, use_case, created_at
		     FROM arenas WHERE type = ? AND project_id = ?`
		args = []any{arenaType, projectID}
	case ArenaSolution:
		q = `SELECT id, type, project_id, feature_id, question, use_case, created_at
		     FROM arenas WHERE type = ? AND feature_id = ?`
		args = []any{arenaType, featureID}
	case ArenaAlternatives, ArenaUseCase:
		q = `SELECT id, type, project_id, feature_id, question, use_case, created_at
		     FROM arenas WHERE type = ? AND project_id = ? AND use_case = ?`
		args = []any{arenaType, projectID, strings.TrimSpace(useCase)}
	default:
		return Arena{}, fmt.Errorf("%w: unknown arena type %q", ErrInvalid, arenaType)
	}
	return d.scanArena(d.QueryRowContext(ctx, q, args...))
}

func (d *DB) scanArena(row *sql.Row) (Arena, error) {
	var a Arena
	var project, feature sql.NullInt64
	var useCase sql.NullString
	err := row.Scan(&a.ID, &a.Type, &project, &feature, &a.Question, &useCase,
		&a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Arena{}, ErrArenaNotFound
	}
	if err != nil {
		return Arena{}, err
	}
	a.ProjectID = project.Int64
	a.FeatureID = feature.Int64
	a.UseCase = useCase.String
	return a, nil
}

// GetArena reads one arena by id, with its competitor count.
func (d *DB) GetArena(ctx context.Context, id int64) (Arena, error) {
	var a Arena
	var project, feature sql.NullInt64
	var useCase sql.NullString
	err := d.QueryRowContext(ctx, `
		SELECT a.id, a.type, a.project_id, a.feature_id, a.question, a.use_case,
		       a.created_at,
		       (SELECT COUNT(*) FROM arena_entries e WHERE e.arena_id = a.id)
		FROM arenas a WHERE a.id = ?`, id).Scan(
		&a.ID, &a.Type, &project, &feature, &a.Question, &useCase,
		&a.CreatedAt, &a.Count)
	if errors.Is(err, sql.ErrNoRows) {
		return Arena{}, ErrArenaNotFound
	}
	if err != nil {
		return Arena{}, err
	}
	a.ProjectID = project.Int64
	a.FeatureID = feature.Int64
	a.UseCase = useCase.String
	return a, nil
}

// ListArases returns every arena of a type, for diagnostics and the spec's
// "which arenas exist" surface.
func (d *DB) ListArases(ctx context.Context, arenaType string, limit int) ([]Arena, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.QueryContext(ctx, `
		SELECT a.id, a.type, a.project_id, a.feature_id, a.question, a.use_case,
		       a.created_at,
		       (SELECT COUNT(*) FROM arena_entries e WHERE e.arena_id = a.id)
		FROM arenas a WHERE a.type = ? ORDER BY a.id LIMIT ?`, arenaType, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Arena
	for rows.Next() {
		var a Arena
		var project, feature sql.NullInt64
		var useCase sql.NullString
		if err := rows.Scan(&a.ID, &a.Type, &project, &feature, &a.Question,
			&useCase, &a.CreatedAt, &a.Count); err != nil {
			return nil, err
		}
		a.ProjectID = project.Int64
		a.FeatureID = feature.Int64
		a.UseCase = useCase.String
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListProjectArases returns every arena of the given types that belongs to ONE
// project, with each one's entry count.
//
// Needed by S2 (alternatives arenas, spec §2): `ListArases` filters on TYPE
// alone, so it returns every alternatives arena on the instance to every caller
// who asks for one project. The alternatives panel is project-scoped, and a
// project listing another's arenas is both wrong and an enumeration leak.
//
// `types` is variadic so one query serves the alternatives panel (one type) and
// any future panel that wants several kinds. An empty list returns nothing
// rather than everything: "no kinds requested" is a caller bug, and defaulting
// it to every type would silently reintroduce the leak this exists to close.
func (d *DB) ListProjectArases(ctx context.Context, projectID int64, types ...string) ([]Arena, error) {
	if projectID <= 0 {
		return nil, fmt.Errorf("%w: project id required", ErrInvalid)
	}
	if len(types) == 0 {
		return nil, fmt.Errorf("%w: at least one arena type required", ErrInvalid)
	}
	for _, t := range types {
		if !IsArenaType(t) {
			return nil, fmt.Errorf("%w: unknown arena type %q", ErrInvalid, t)
		}
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(types)), ",")
	args := make([]any, 0, len(types)+2)
	args = append(args, projectID)
	for _, t := range types {
		args = append(args, t)
	}
	args = append(args, 500)

	rows, err := d.QueryContext(ctx, `
		SELECT a.id, a.type, a.project_id, a.feature_id, a.question, a.use_case,
		       a.created_at,
		       (SELECT COUNT(*) FROM arena_entries e WHERE e.arena_id = a.id)
		FROM arenas a
		WHERE a.project_id = ? AND a.type IN (`+placeholders+`)
		ORDER BY a.use_case, a.id
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Arena{}
	for rows.Next() {
		var a Arena
		var project, feature sql.NullInt64
		var useCase sql.NullString
		if err := rows.Scan(&a.ID, &a.Type, &project, &feature, &a.Question,
			&useCase, &a.CreatedAt, &a.Count); err != nil {
			return nil, err
		}
		a.ProjectID = project.Int64
		a.FeatureID = feature.Int64
		a.UseCase = useCase.String
		out = append(out, a)
	}
	return out, rows.Err()
}

// IsArenaType reports whether a string names an arena kind this tree knows.
//
// Exported because a caller constructing a list of types for
// `ListProjectArases` must be able to reject its own typo. Without it, an
// unknown type matches no rows and the caller reads "this project has no
// arenas" — a silent wrong answer, which is the same failure mode as the
// `use_case=”` collision S2's rules are built on.
func IsArenaType(t string) bool {
	switch t {
	case ArenaFeaturePriority, ArenaSolution, ArenaList, ArenaRequest,
		ArenaAlternatives, ArenaUseCase:
		return true
	}
	return false
}

// AddArenaCompetitor adds a project to an alternatives arena, refusing the two
// cases that would make the arena meaningless.
//
// This is `refuseOwnVote`'s sibling at the ENTRY point. The two rules are
// different: `refuseOwnVote` stops a voter judging something they authored,
// while this stops the arena ever holding a pair it cannot compare. Doing it
// here rather than at vote time is deliberate — a self-comparing arena would be
// visible to a reader as its own project listed as a competitor to itself, and a
// vote-time-only check leaves that on screen even though nobody can act on it.
//
// The own-project refusal is by ID, not by "is the caller an owner of it": the
// arena's project is `arenas.project_id`, and that is the thing that must not
// compete with itself.
//
// Not transactional against a concurrent add of the same project, and it does
// not need to be: `UpsertArenaEntry` is the idempotent half, and a duplicate
// add leaves one entry rather than two.
func (d *DB) AddArenaCompetitor(ctx context.Context, arenaID, candidateProjectID int64) error {
	arena, err := d.GetArena(ctx, arenaID)
	if err != nil {
		return err
	}
	if arena.Type != ArenaAlternatives && arena.Type != ArenaUseCase {
		return fmt.Errorf("%w: an arena of type %q ranks %s, not competing projects",
			ErrInvalid, arena.Type, entryKindFor(arena.Type))
	}
	if candidateProjectID <= 0 {
		return fmt.Errorf("%w: candidate project id required", ErrInvalid)
	}
	if arena.ProjectID == candidateProjectID {
		return fmt.Errorf("%w: project %d cannot compete against itself; an "+
			"alternatives arena ranks other projects", ErrInvalid, candidateProjectID)
	}
	// The candidate must exist. Without this, a typo'd id becomes an arena entry
	// pointing at nothing, and the leaderboard skips it (ListSolutions does the
	// same for a solution whose row is gone) — so the arena looks like it ranked
	// fewer projects than were added, with nothing in the response to say why.
	if _, err := d.GetProjectByID(ctx, candidateProjectID); err != nil {
		return err
	}
	return d.UpsertArenaEntry(ctx, arenaID, EntityProject, candidateProjectID)
}

// entryKindFor names what an arena type ranks, for a refusal message.
//
// A reader who posts a project to a feature arena is not asking for a technical
// error, so the message says what that arena does rank instead.
func entryKindFor(arenaType string) string {
	switch arenaType {
	case ArenaFeaturePriority:
		return "features"
	case ArenaSolution:
		return "solutions"
	case ArenaList:
		return "list entries"
	case ArenaRequest:
		return "requests"
	}
	return "entities"
}

// UpsertArenaEntry adds or refreshes a competitor's slot in an arena.
//
// Refresh semantics: the rating triple is only overwritten when the caller
// supplies one. `UpsertArenaEntry` with zero r/rd/sigma creates the slot at the
// Glicko-2 prior (1500/350) and leaves an existing rating alone, because the
// common call is "this entity now competes" and overwriting a 400-vote rating
// with the prior because someone re-registered the entity is a data-loss bug
// that only shows up much later.
func (d *DB) UpsertArenaEntry(ctx context.Context, arenaID int64, entityType string, entityID int64) error {
	now := float64(time.Now().Unix())
	_, err := d.ExecContext(ctx, `
		INSERT INTO arena_entries
			(arena_id, entity_type, entity_id, r, rd, sigma, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(arena_id, entity_type, entity_id) DO UPDATE SET
			updated_at = excluded.updated_at`,
		arenaID, entityType, entityID, ranking.DefaultRating,
		ranking.DefaultDeviation, ranking.DefaultVolatility, now)
	return err
}

// GetArenaEntry reads one competitor's slot.
func (d *DB) GetArenaEntry(ctx context.Context, arenaID int64, entityType string, entityID int64) (ArenaEntry, error) {
	var e ArenaEntry
	var baseline int
	var parent sql.NullInt64
	err := d.QueryRowContext(ctx, `
		SELECT arena_id, entity_type, entity_id, r, rd, sigma, games,
		       is_baseline, parent_entry_id, updated_at
		FROM arena_entries
		WHERE arena_id = ? AND entity_type = ? AND entity_id = ?`,
		arenaID, entityType, entityID).Scan(
		&e.ArenaID, &e.EntityType, &e.EntityID, &e.R, &e.RD, &e.Sigma,
		&e.Games, &baseline, &parent, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ArenaEntry{}, fmt.Errorf("%w: entry %s/%d in arena %d",
			ErrNotFound, entityType, entityID, arenaID)
	}
	if err != nil {
		return ArenaEntry{}, err
	}
	e.IsBaseline = baseline == 1
	e.ParentEntryID = parent.Int64
	e.setDerived()
	return e, nil
}

// RemoveArenaEntry drops a competitor from an arena.
//
// The baseline is refused rather than removed: §6.3 makes it permanent, and an
// arena whose baseline can be deleted is an arena where "beats doing nothing"
// silently becomes meaningless.
func (d *DB) RemoveArenaEntry(ctx context.Context, arenaID int64, entityType string, entityID int64) error {
	var isBaseline int
	err := d.QueryRowContext(ctx,
		`SELECT is_baseline FROM arena_entries
		 WHERE arena_id = ? AND entity_type = ? AND entity_id = ?`,
		arenaID, entityType, entityID).Scan(&isBaseline)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: entry %s/%d in arena %d", ErrNotFound, entityType, entityID, arenaID)
	}
	if err != nil {
		return err
	}
	if isBaseline == 1 {
		return fmt.Errorf("%w: the do-nothing baseline cannot be removed from an arena", ErrInvalid)
	}
	_, err = d.ExecContext(ctx, `
		DELETE FROM arena_entries
		WHERE arena_id = ? AND entity_type = ? AND entity_id = ?`,
		arenaID, entityType, entityID)
	return err
}

// ArenaLeaderboard returns an arena's competitors, best first.
//
// Ordered by the conservative score `r - 2*RD` (§5.1's displayed value) rather
// than by r, so a brand-new unrated competitor does not outrank a well-attested
// one on the strength of having not lost yet. That is the whole reason the spec
// displays the conservative figure: the leaderboard should reward evidence.
//
// The leader's rating is read first so every row can carry its win probability
// against it, which §5.1 requires the UI to show ("A is better with 87%
// confidence").
func (d *DB) ArenaLeaderboard(ctx context.Context, arenaID int64, limit int) ([]ArenaEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := d.QueryContext(ctx, `
		SELECT arena_id, entity_type, entity_id, r, rd, sigma, games,
		       is_baseline, parent_entry_id, updated_at
		FROM arena_entries WHERE arena_id = ?
		ORDER BY (r - 2*rd) DESC, r DESC, entity_id ASC
		LIMIT ?`, arenaID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Collected before any further query: PLAN.md rule 6, the pool is
	// MaxOpenConns(1) and a second query with this cursor open deadlocks.
	var out []ArenaEntry
	var leader *ArenaEntry
	for rows.Next() {
		var e ArenaEntry
		var baseline int
		var parent sql.NullInt64
		if err := rows.Scan(&e.ArenaID, &e.EntityType, &e.EntityID, &e.R, &e.RD,
			&e.Sigma, &e.Games, &baseline, &parent, &e.UpdatedAt); err != nil {
			return nil, err
		}
		e.IsBaseline = baseline == 1
		e.ParentEntryID = parent.Int64
		e.setDerived()
		if leader == nil {
			leader = &e
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if leader != nil {
		for i := range out {
			if out[i].EntityID == leader.EntityID && out[i].EntityType == leader.EntityType {
				// The leader has nothing to beat.
				out[i].WinProbabilityAgainstLeader = 0
				continue
			}
			out[i].WinProbabilityAgainstLeader = ranking.WinProbability(
				out[i].R, out[i].RD, leader.R, leader.RD)
		}
	}
	return out, nil
}

// ArenaVote is one recorded comparison.
type ArenaVote struct {
	ID      int64      `json:"id"`
	ArenaID int64      `json:"arena_id"`
	VoterID int64      `json:"voter_id"`
	A       ArenaEntry `json:"a"`
	B       ArenaEntry `json:"b"`
	Outcome string     `json:"outcome"`
	Weight  float64    `json:"weight"`
	// Reason is §5.1's "a vote can include a short reason". Nullable because
	// voting must never require writing an essay.
	Reason    string  `json:"reason,omitempty"`
	CreatedAt float64 `json:"created_at"`
}

// CastArenaVote records a comparison and applies it to both competitors'
// ratings.
//
// This is the only writer of ratings in the system. It is deliberately one
// function rather than "record the vote, then call the ranking engine": the
// rating update and the log write have to agree, and splitting them means a
// crash between the two produces a ranking that cannot be recomputed from the
// log -- exactly the property §2.5 promises.
//
// A `neither` outcome in an arena with a baseline is scored as both competitors
// losing to the baseline (§5.1), which is the spec's way of saying "compared to
// doing nothing, neither of these is worth it". Elsewhere it is a no-contest
// that raises a pair-quality flag, recorded in the audit log.
func (d *DB) CastArenaVote(ctx context.Context, arenaID, voterID int64, aType string, aID int64, bType string, bID int64, outcome, reason string, weight float64) (ArenaVote, error) {
	arena, err := d.GetArena(ctx, arenaID)
	if err != nil {
		return ArenaVote{}, err
	}
	// A solution in a solution arena, a feature in a feature arena. Without this
	// a caller could vote a solution against a project in the same arena and the
	// comparison would be meaningless.
	if aType != bType {
		return ArenaVote{}, fmt.Errorf("%w: both sides of a comparison must be the same entity type (%s vs %s)",
			ErrInvalid, aType, bType)
	}
	if aID == bID {
		return ArenaVote{}, ErrSameEntityVote
	}
	switch outcome {
	case "a", "b", "both", "neither", "skip":
	default:
		return ArenaVote{}, fmt.Errorf("%w: unknown outcome %q", ErrInvalid, outcome)
	}
	// §5.2: "Authors and affiliated accounts are disclosed and cannot vote on
	// their own entries."
	if err := d.refuseOwnVote(ctx, aType, aID, voterID); err != nil {
		return ArenaVote{}, err
	}
	if err := d.refuseOwnVote(ctx, bType, bID, voterID); err != nil {
		return ArenaVote{}, err
	}

	entryA, err := d.GetArenaEntry(ctx, arenaID, aType, aID)
	if err != nil {
		return ArenaVote{}, err
	}
	entryB, err := d.GetArenaEntry(ctx, arenaID, bType, bID)
	if err != nil {
		return ArenaVote{}, err
	}

	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO pairwise_votes
			(arena_id, entity_type, a, b, voter_id, outcome, weight, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?)`,
		arenaID, aType, aID, bID, voterID, outcome, weight,
		strings.TrimSpace(reason), now)
	if err != nil {
		return ArenaVote{}, err
	}
	voteID, _ := res.LastInsertId()

	// A skip is recorded and changes nothing (§5.1: "Skip (with optional
	// reason: 'I don't understand this' flags an unclear write-up)").
	if outcome == "skip" {
		_ = d.AddAudit(ctx, arena.ProjectID, voterID, "arena_vote_skipped",
			aType, aID, fmt.Sprintf("vs %s %d: %s", bType, bID, reason))
		return ArenaVote{
			ID: voteID, ArenaID: arenaID, VoterID: voterID,
			A: entryA, B: entryB, Outcome: outcome, Weight: weight,
			Reason: reason, CreatedAt: now,
		}, nil
	}

	// §5.1: "In arenas with a baseline, Neither counts as both competitors losing
	// to the baseline."
	//
	// The baseline is resolved from arena_entries.is_baseline, not from
	// the column arenas.baseline_entry_id. Those were two representations of one
	// fact, and the code consulted the one nothing populates: that column was only ever
	// set by the not-yet-written solutions table. So this branch never fired, and
	// "neither" fell through to a 0.5/0.5 draw -- both competitors held their
	// rating while doing nothing quietly outranked them. The flag is the
	// authoritative one because it is the flag RemoveArenaEntry refuses to clear.
	baseline, err := d.baselineEntry(ctx, arenaID)
	if err != nil {
		return ArenaVote{}, err
	}
	if outcome == "neither" && baseline != nil {
		// Two separate games, baseline against each competitor. The a-vs-b
		// comparison is not scored: neither won it.
		if baseline.EntityType == aType && baseline.EntityID == aID {
			if err := d.applyGames(ctx, arenaID, aType, aID, bType, bID, 0.0, weight, now); err != nil {
				return ArenaVote{}, err
			}
		} else {
			if err := d.applyGame(ctx, arenaID, *baseline,
				ArenaEntry{EntityType: aType, EntityID: aID}, 1.0, weight, now); err != nil {
				return ArenaVote{}, err
			}
		}
		if baseline.EntityType == bType && baseline.EntityID == bID {
			if err := d.applyGames(ctx, arenaID, aType, aID, bType, bID, 1.0, weight, now); err != nil {
				return ArenaVote{}, err
			}
		} else {
			if err := d.applyGame(ctx, arenaID, *baseline,
				ArenaEntry{EntityType: bType, EntityID: bID}, 1.0, weight, now); err != nil {
				return ArenaVote{}, err
			}
		}
	} else if err := d.applyGames(ctx, arenaID, aType, aID, bType, bID, outcomeScore(outcome), weight, now); err != nil {
		return ArenaVote{}, err
	}

	entryA, _ = d.GetArenaEntry(ctx, arenaID, aType, aID)
	entryB, _ = d.GetArenaEntry(ctx, arenaID, bType, bID)
	return ArenaVote{
		ID: voteID, ArenaID: arenaID, VoterID: voterID,
		A: entryA, B: entryB, Outcome: outcome, Weight: weight,
		Reason: reason, CreatedAt: now,
	}, nil
}

// outcomeScore maps an outcome to A's game score.
func outcomeScore(outcome string) float64 {
	switch outcome {
	case "a":
		return 1.0
	case "b":
		return 0.0
	case "both":
		return 0.5
	}
	return 0.5
}

// refuseOwnVote rejects a vote on an entry the voter authored.
func (d *DB) refuseOwnVote(ctx context.Context, entityType string, entityID, voterID int64) error {
	var author sql.NullInt64
	var q string
	switch entityType {
	case EntityFeature:
		q = `SELECT author_id FROM features WHERE id = ?`
	case EntitySolution:
		q = `SELECT author_id FROM solutions WHERE id = ?`
	case EntityProject:
		// Projects have no owner column: ownership is a row in members with a
		// role, so "did this voter create it" has to go through the membership
		// table. There is a projects.owner_id in no schema here, and querying for
		// it fails at runtime with "no such column" rather than at build time --
		// which is why this is worth stating rather than leaving implicit.
		// members has no id column; its key is (project_id, user_id), so the
		// tiebreak that makes this deterministic has to be on user_id.
		q = `SELECT user_id FROM members
		     WHERE project_id = ? AND role IN ('owner','maintainer')
		     ORDER BY user_id LIMIT 1`
	default:
		// list_entry authorship is not tracked; nothing to check.
		return nil
	}
	if err := d.QueryRowContext(ctx, q, entityID).Scan(&author); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if author.Valid && author.Int64 == voterID {
		return ErrSelfVote
	}
	return nil
}

// applyGames runs one rating period for both competitors and stores the result.
//
// The two ratings are computed from their pre-vote values and then written
// together, so neither is updated from a value the other has already moved. That
// is the standard Glicko-2 batch treatment and it is what makes a symmetric
// matchup symmetric: updating A first and recomputing B against the new A gives
// the winner a smaller gain than the loser, which is a bug that looks like
// reasonable behaviour.
// applyGames scores one comparison between two of the arena's own entries, and
// is the only shape most votes take.
func (d *DB) applyGames(ctx context.Context, arenaID int64, aType string, aID int64, bType string, bID int64, aScore, weight, now float64) error {
	if err := d.applyGame(ctx, arenaID, ArenaEntry{EntityType: aType, EntityID: aID}, ArenaEntry{EntityType: bType, EntityID: bID}, aScore, weight, now); err != nil {
		return err
	}
	return nil
}

// applyGame scores one entry as having taken aScore against another, and updates
// both. Split out from applyGames because a baseline vote is baseline-vs-competitor,
// where the competitor is not the other side of any a/b pair the voter was asked
// about -- scoring that through applyGames would invent a comparison that never
// happened.
func (d *DB) applyGame(ctx context.Context, arenaID int64, a, b ArenaEntry, aScore, weight, now float64) error {
	aType, aID := a.EntityType, a.EntityID
	bType, bID := b.EntityType, b.EntityID
	entryA, err := d.GetArenaEntry(ctx, arenaID, aType, aID)
	if err != nil {
		return err
	}
	entryB, err := d.GetArenaEntry(ctx, arenaID, bType, bID)
	if err != nil {
		return err
	}

	// One engine, two callers: this and replayArenaRatings both go through
	// rateArenaPair, because doing this arithmetic in two places is how the
	// replay lost the baseline rule (see rateArenaPair).
	newA, newB, games := rateArenaPair(
		ranking.ArenaRating{R: entryA.R, RD: entryA.RD, Sigma: entryA.Sigma},
		ranking.ArenaRating{R: entryB.R, RD: entryB.RD, Sigma: entryB.Sigma},
		aScore, weight)

	if _, err := d.ExecContext(ctx, `
		UPDATE arena_entries
		SET r = ?, rd = ?, sigma = ?, games = games + ?, updated_at = ?
		WHERE arena_id = ? AND entity_type = ? AND entity_id = ?`,
		newA.R, newA.RD, newA.Sigma, games, now, arenaID, aType, aID); err != nil {
		return err
	}
	if _, err := d.ExecContext(ctx, `
		UPDATE arena_entries
		SET r = ?, rd = ?, sigma = ?, games = games + ?, updated_at = ?
		WHERE arena_id = ? AND entity_type = ? AND entity_id = ?`,
		newB.R, newB.RD, newB.Sigma, games, now, arenaID, bType, bID); err != nil {
		return err
	}

	// Features keep their own rating columns, so a feature-priority arena vote
	// has to be mirrored or the project priority endpoint, which reads
	// features.elo_r, would drift from the arena ranking.
	if aType == EntityFeature {
		_ = d.mirrorFeatureRating(ctx, aID, newA, now)
	}
	if bType == EntityFeature {
		_ = d.mirrorFeatureRating(ctx, bID, newB, now)
	}
	return nil
}

// mirrorFeatureRating writes an arena rating back to features.elo_*.
func (d *DB) mirrorFeatureRating(ctx context.Context, featureID int64, r ranking.ArenaRating, now float64) error {
	_, err := d.ExecContext(ctx, `
		UPDATE features SET elo_r = ?, elo_rd = ?, elo_vol = ?, updated_at = ?
		WHERE id = ?`, r.R, r.RD, r.Sigma, now, featureID)
	return err
}

// NextArenaPair returns the next comparison to put to a voter (§5.1's active
// learning).
//
// Selection is: entries the voter has not judged, preferring close ratings and
// high deviation. High deviation is the interesting case because an unrated
// entry is exactly the one whose position the arena does not know, and close
// ratings are the ones a comparison actually resolves.
//
// A voter who has judged every pair gets an error rather than a repeat, because
// re-asking a settled question is how a voting queue turns into a chore.
func (d *DB) NextArenaPair(ctx context.Context, arenaID, voterID int64) (ArenaEntry, ArenaEntry, error) {
	// Read the arena first so a bad id fails with ErrArenaNotFound rather than
	// the "needs at least two entries" message, which would send a caller looking
	// for a data problem they do not have.
	arena, err := d.GetArena(ctx, arenaID)
	if err != nil {
		return ArenaEntry{}, ArenaEntry{}, err
	}
	entries, err := d.ArenaLeaderboard(ctx, arena.ID, 200)
	if err != nil {
		return ArenaEntry{}, ArenaEntry{}, err
	}
	if len(entries) < 2 {
		return ArenaEntry{}, ArenaEntry{}, fmt.Errorf("%w: an arena needs at least two entries to compare", ErrInvalid)
	}

	judged, err := d.judgedPairs(ctx, arenaID, voterID)
	if err != nil {
		return ArenaEntry{}, ArenaEntry{}, err
	}

	best := math.MaxFloat64
	var bestI, bestJ = -1, -1
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			a, b := entries[i], entries[j]
			key := pairKey(a, b)
			if _, seen := judged[key]; seen {
				continue
			}
			// Lower is a better pair to ask about.
			cost := pairSelectionCost(a, b)
			if cost < best {
				best, bestI, bestJ = cost, i, j
			}
		}
	}
	if bestI < 0 {
		return ArenaEntry{}, ArenaEntry{}, fmt.Errorf("%w: this voter has judged every pair in arena %d",
			ErrNotFound, arenaID)
	}
	return entries[bestI], entries[bestJ], nil
}

// pairSelectionCost is the active-learning cost of asking about this pair.
// Lower is better.
//
// Three terms, each with a reason:
//   - rating distance: a pair 400 points apart teaches nothing, and a crowd
//     asked to compare them mostly answers "both", which is noise.
//   - summed deviation: unrated entries need games, and the arena cannot rank
//     what it has never measured.
//   - baseline bias: a pair including the do-nothing baseline is the most
//     informative comparison in a solution arena, because "beats doing nothing"
//     is the question §6.3 says every solution has to answer.
//
// pairSelectionCost ranks candidate pairs; lower is the better pair to ask about.
//
// §5.1's active learning: ask about the comparisons the arena knows least. That
// is high RD, not low -- so the uncertainty term is added, which means the
// subtraction it replaces was backwards. With `350 - mean(RD)` as a bonus, a pair
// of thoroughly-tested entries looked more informative than a pair of untouched
// ones, and the arena kept asking questions it had already answered while new
// entries sat at the prior. TestNextPairPrefersUnratedEntries is what caught it.
//
// The distance term is the secondary criterion: of two equally uncertain pairs,
// the closer-rated one is worth asking, because the answer moves both ratings
// further. The baseline discount says every solution should be compared against
// doing nothing before it is compared against rivals, which is what §6.5 means by
// "the baseline wins".
func pairSelectionCost(a, b ArenaEntry) float64 {
	distance := math.Abs(a.R - b.R)
	uncertainty := (a.RD + b.RD) / 2
	cost := distance - uncertainty*0.5
	if a.IsBaseline || b.IsBaseline {
		cost -= 150
	}
	return cost
}

// pairKey is an order-independent identity for a pair.
func pairKey(a, b ArenaEntry) string {
	x := fmt.Sprintf("%s:%d", a.EntityType, a.EntityID)
	y := fmt.Sprintf("%s:%d", b.EntityType, b.EntityID)
	if x > y {
		x, y = y, x
	}
	return x + "|" + y
}

// judgedPairs is the set of pairs this voter has already settled in this arena.
//
// Read into a map before the selection loop: one query rather than one per
// candidate pair, and the selection loop runs while no cursor is open (PLAN.md
// rule 6).
func (d *DB) judgedPairs(ctx context.Context, arenaID, voterID int64) (map[string]struct{}, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT entity_type, a, b, outcome FROM pairwise_votes
		WHERE arena_id = ? AND voter_id = ?`, arenaID, voterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var entityType string
		var aID, bID int64
		var outcome string
		if err := rows.Scan(&entityType, &aID, &bID, &outcome); err != nil {
			return nil, err
		}
		// A skip is not a judgement: the voter did not understand the pair, so
		// re-asking it later is correct, and the slot stays open.
		if outcome == "skip" {
			continue
		}
		out[pairKey(
			ArenaEntry{EntityType: entityType, EntityID: aID},
			ArenaEntry{EntityType: entityType, EntityID: bID})] = struct{}{}
	}
	return out, rows.Err()
}

// baselineEntry returns the arena's do-nothing competitor, or nil if the arena
// has none.
//
// §6.3 requires one in every solution arena. The other arena types have none: a
// feature-priority arena is not asking "is this worth doing", so there is nothing
// for "neither" to lose to, and a bare 0.5 draw is the right answer there.
func (d *DB) baselineEntry(ctx context.Context, arenaID int64) (*ArenaEntry, error) {
	e, err := d.GetArenaEntry(ctx, arenaID, EntityFeature, 0)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if !e.IsBaseline {
		return nil, nil
	}
	return &e, nil
}

// ListArenaVotes returns an arena's vote log, newest first.
//
// §2.5's "anyone can verify a ranking" needs this to be readable, so there is no
// permission gate: a vote log that only its participants can read verifies
// nothing.
func (d *DB) ListArenaVotes(ctx context.Context, arenaID int64, limit int) ([]ArenaVote, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.QueryContext(ctx, `
		SELECT v.id, v.arena_id, v.voter_id, v.entity_type, v.a, v.b,
		       v.outcome, v.weight, COALESCE(v.reason, ''), v.created_at
		FROM pairwise_votes v
		WHERE v.arena_id = ? AND v.outcome != 'skip'
		ORDER BY v.created_at DESC, v.id DESC LIMIT ?`, arenaID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var votes []ArenaVote
	var types []string
	for rows.Next() {
		var v ArenaVote
		var entityType string
		if err := rows.Scan(&v.ID, &v.ArenaID, &v.VoterID, &entityType, &v.A.EntityID,
			&v.B.EntityID, &v.Outcome, &v.Weight, &v.Reason, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.A.ArenaID, v.A.EntityType = v.ArenaID, entityType
		v.B.ArenaID, v.B.EntityType = v.ArenaID, entityType
		v.A.setDerived()
		v.B.setDerived()
		votes = append(votes, v)
		types = append(types, entityType)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The entity type is constant per arena in practice but not by constraint, so
	// it is patched onto each row after the cursor closes.
	for i := range votes {
		if i < len(types) {
			votes[i].A.EntityType = types[i]
			votes[i].B.EntityType = types[i]
		}
	}
	sort.SliceStable(votes, func(i, j int) bool { return votes[i].ID > votes[j].ID })
	return votes, nil
}

// ArenaReplay is one entry's rating after replaying the arena's vote log.
type ArenaReplay struct {
	EntityType string
	EntityID   int64
	Rating     ranking.ArenaRating
	Games      int
}

// rateArenaPair is the ONE place a comparison changes a rating.
//
// §5.1's update, §5.1's fractional-game treatment and the games each side played
// are computed here so that the incremental path (CastArenaVote -> applyGame) and
// the replay (replayArenaRatings) cannot disagree. They disagreed by computing one
// rule twice, and the replay lost three of them: it scored "neither" as a 0.5 draw
// in arenas whose baseline makes it two losses, it wrote `games = 1` for every
// entry regardless of how many games that entry played, and it applied the log in
// (voter, day) groups instead of in the order the votes arrived -- which is a
// different order from the live one whenever two voters vote in the same day.
// Measured before the fix: two competitors swapped places, the baseline sat at its
// prior instead of leading, and every entry reported one game.
func rateArenaPair(a, b ranking.ArenaRating, aScore, weight float64) (
	newA, newB ranking.ArenaRating, games int) {
	games = gameCount(weight)
	newA = ranking.UpdateArenaRating(a.R, a.RD, a.Sigma, repeatedGame(b, aScore, weight), weight)
	newB = ranking.UpdateArenaRating(b.R, b.RD, b.Sigma, repeatedGame(a, 1-aScore, weight), weight)
	return newA, newB, games
}

// gameCount is how many games one comparison counts as, for one side.
//
// A weight-2 vote is two games (§5.1: "weight scales the update's influence: a
// weight-2 voter is recorded as two games"), and a weight BELOW 1 is still one
// game: the comparison was made. applyGame used to add int(weight), so a vote by a
// voter whose weight the account-age ramp had put at 0.4 was recorded in `games`
// as no game at all -- a comparison that moved the rating and did not count.
func gameCount(weight float64) int {
	n := int(weight)
	if n < 1 {
		return 1
	}
	return n
}

// replayArenaRatings rebuilds every rating in an arena from its vote log, WITHOUT
// writing. It is the read half of §5.1's "anyone can verify a ranking" and the
// single implementation behind both RecomputeArenaRatings and VerifyArenaRatings.
//
// Two properties are load-bearing, and both are what the incremental path does:
//
//   - Votes are applied in (created_at, id) order, one game per side. The update
//     depends on the ratings the pair had when the vote was cast, so the order is
//     the whole answer; the (voter, day) grouping this used to do batched two
//     voters' same-day votes together, which is not the order they arrived in.
//   - The baseline is resolved from arena_entries.is_baseline -- the same source
//     CastArenaVote reads -- and "neither" in an arena that has one is two losses
//     to the baseline, not a draw between the competitors.
func (d *DB) replayArenaRatings(ctx context.Context, arenaID int64) ([]ArenaReplay, error) {
	// The competitors first, at the prior. Read and closed before the next query:
	// the pool is MaxOpenConns(1) (PLAN.md rule 6) and a second cursor deadlocks.
	entryRows, err := d.QueryContext(ctx,
		`SELECT entity_type, entity_id FROM arena_entries WHERE arena_id = ? ORDER BY entity_id`, arenaID)
	if err != nil {
		return nil, err
	}
	type ident struct {
		typ string
		id  int64
	}
	var idents []ident
	for entryRows.Next() {
		var it ident
		if err := entryRows.Scan(&it.typ, &it.id); err != nil {
			entryRows.Close()
			return nil, err
		}
		idents = append(idents, it)
	}
	entryRows.Close()
	if err := entryRows.Err(); err != nil {
		return nil, err
	}

	rating := map[int64]ranking.ArenaRating{}
	games := map[int64]int{}
	for _, it := range idents {
		rating[it.id] = ranking.ArenaRating{
			R: ranking.DefaultRating, RD: ranking.DefaultDeviation,
			Sigma: ranking.DefaultVolatility,
		}
	}

	baseline, err := d.baselineEntry(ctx, arenaID)
	if err != nil {
		return nil, err
	}

	type rawVote struct {
		a, b    int64
		outcome string
		weight  float64
	}
	voteRows, err := d.QueryContext(ctx, `
		SELECT a, b, outcome, weight
		FROM pairwise_votes
		WHERE arena_id = ? AND outcome != 'skip'
		ORDER BY created_at ASC, id ASC`, arenaID)
	if err != nil {
		return nil, err
	}
	var votes []rawVote
	for voteRows.Next() {
		var v rawVote
		if err := voteRows.Scan(&v.a, &v.b, &v.outcome, &v.weight); err != nil {
			voteRows.Close()
			return nil, err
		}
		votes = append(votes, v)
	}
	voteRows.Close()
	if err := voteRows.Err(); err != nil {
		return nil, err
	}

	for _, v := range votes {
		ra, okA := rating[v.a]
		rb, okB := rating[v.b]
		if !okA || !okB || v.a == v.b {
			// A vote about something that is not in the arena is not scored. It
			// cannot happen through CastArenaVote (it resolves both entries
			// first), so this is a log that was written by something else.
			continue
		}
		if v.outcome == "neither" && baseline != nil {
			// §5.1: "In arenas with a baseline, Neither counts as both
			// competitors losing to the baseline." The shape below mirrors
			// CastArenaVote's branch game for game, including the case where a
			// side IS the baseline.
			if baseline.EntityID == v.a {
				na, nb, g := rateArenaPair(ra, rb, 0.0, v.weight)
				rating[v.a], rating[v.b] = na, nb
				games[v.a] += g
				games[v.b] += g
			} else {
				rbl := rating[baseline.EntityID]
				nbl, na, g := rateArenaPair(rbl, ra, 1.0, v.weight)
				rating[baseline.EntityID], rating[v.a] = nbl, na
				games[baseline.EntityID] += g
				games[v.a] += g
			}
			if baseline.EntityID == v.b {
				ra, rb = rating[v.a], rating[v.b]
				na, nb, g := rateArenaPair(ra, rb, 1.0, v.weight)
				rating[v.a], rating[v.b] = na, nb
				games[v.a] += g
				games[v.b] += g
			} else {
				rbl := rating[baseline.EntityID]
				nbl, nb, g := rateArenaPair(rbl, rb, 1.0, v.weight)
				rating[baseline.EntityID], rating[v.b] = nbl, nb
				games[baseline.EntityID] += g
				games[v.b] += g
			}
			continue
		}
		na, nb, g := rateArenaPair(ra, rb, outcomeScore(v.outcome), v.weight)
		rating[v.a], rating[v.b] = na, nb
		games[v.a] += g
		games[v.b] += g
	}

	out := make([]ArenaReplay, 0, len(idents))
	for _, it := range idents {
		out = append(out, ArenaReplay{
			EntityType: it.typ, EntityID: it.id,
			Rating: rating[it.id], Games: games[it.id],
		})
	}
	return out, nil
}

// RecomputeArenaRatings rebuilds every rating in an arena from its vote log and
// writes the result back.
//
// §5.1: "ratings are recomputed deterministically from the append-only vote log.
// Anyone can verify a ranking." This is the WRITE half (the check that does not
// write is VerifyArenaRatings). On an arena whose incremental path is correct this
// is a no-op; before 2026-10-16 it was not, and it was not obvious: see
// rateArenaPair for what the replay got wrong and the measured drift it caused.
func (d *DB) RecomputeArenaRatings(ctx context.Context, arenaID int64) ([]ArenaEntry, error) {
	replay, err := d.replayArenaRatings(ctx, arenaID)
	if err != nil {
		return nil, err
	}
	now := float64(time.Now().Unix())
	for _, r := range replay {
		if _, err := d.ExecContext(ctx, `
			UPDATE arena_entries
			SET r = ?, rd = ?, sigma = ?, games = ?, updated_at = ?
			WHERE arena_id = ? AND entity_type = ? AND entity_id = ?`,
			r.Rating.R, r.Rating.RD, r.Rating.Sigma, r.Games, now,
			arenaID, r.EntityType, r.EntityID); err != nil {
			return nil, err
		}
		// The type comes from the entry, not from a variable left over from the
		// previous loop: the old version mirrored only when the LAST entry it read
		// happened to be a feature.
		if r.EntityType == EntityFeature {
			_ = d.mirrorFeatureRating(ctx, r.EntityID, r.Rating, now)
		}
	}
	return d.ArenaLeaderboard(ctx, arenaID, 200)
}

// ArenaVerification is one entry's stored rating beside the replay of the log it
// should have come from.
type ArenaVerification struct {
	EntityType    string  `json:"entity_type"`
	EntityID      int64   `json:"entity_id"`
	IsBaseline    bool    `json:"is_baseline"`
	StoredR       float64 `json:"stored_r"`
	StoredRD      float64 `json:"stored_rd"`
	StoredGames   int     `json:"stored_games"`
	ReplayedR     float64 `json:"replayed_r"`
	ReplayedRD    float64 `json:"replayed_rd"`
	ReplayedGames int     `json:"replayed_games"`
	Agrees        bool    `json:"agrees"`
}

// VerifyArenaRatings replays the arena's vote log and compares the result with the
// stored ratings. It does not write, so it can be run by anyone: §5.1 says
// "Anyone can verify a ranking", and a verification only the server may perform is
// the promise this whole function exists to keep.
//
// A stored rating the log does not reproduce means the ranking is not derivable
// from the record, which is the property §2.5 and §5.1 both rest on. It is not
// necessarily wrong -- a rating can be right and derived from a log that no longer
// matches -- but it IS unverifiable, and the answer to "which entries" is the
// divergence report.
func (d *DB) VerifyArenaRatings(ctx context.Context, arenaID int64) ([]ArenaVerification, error) {
	replay, err := d.replayArenaRatings(ctx, arenaID)
	if err != nil {
		return nil, err
	}

	// EVERY entry, not the leaderboard's first page. The first version read through
	// ArenaLeaderboard(ctx, arenaID, 200) and compared what it returned, so an arena
	// with more than 200 competitors -- production has one with 327 -- was verified
	// for the top 200 and reported `agrees: true` about the rest by saying nothing.
	// A verification that silently covers a subset is the failure it exists to catch.
	rows, err := d.QueryContext(ctx, `
		SELECT entity_type, entity_id, r, rd, games, is_baseline
		FROM arena_entries WHERE arena_id = ? ORDER BY entity_id`, arenaID)
	if err != nil {
		return nil, err
	}
	type storedRow struct {
		entityType string
		entityID   int64
		r, rd      float64
		games      int
		baseline   bool
	}
	var stored []storedRow
	for rows.Next() {
		var s storedRow
		var baseline int
		if err := rows.Scan(&s.entityType, &s.entityID, &s.r, &s.rd, &s.games, &baseline); err != nil {
			rows.Close()
			return nil, err
		}
		s.baseline = baseline == 1
		stored = append(stored, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	byID := map[int64]ArenaReplay{}
	for _, r := range replay {
		byID[r.EntityID] = r
	}
	out := make([]ArenaVerification, 0, len(stored))
	for _, e := range stored {
		v := ArenaVerification{
			EntityType: e.entityType, EntityID: e.entityID, IsBaseline: e.baseline,
			StoredR: e.r, StoredRD: e.rd, StoredGames: e.games,
		}
		if r, ok := byID[e.entityID]; ok {
			v.ReplayedR, v.ReplayedRD, v.ReplayedGames = r.Rating.R, r.Rating.RD, r.Games
			v.Agrees = r.Rating.R == e.r && r.Rating.RD == e.rd && r.Games == e.games
		}
		out = append(out, v)
	}
	return out, nil
}

// repeatedGame builds the game list for one weighted comparison.
func repeatedGame(opponent ranking.ArenaRating, score, weight float64) []ranking.Game {
	n := int(weight)
	if n < 1 {
		n = 1
	}
	games := make([]ranking.Game, 0, n)
	for i := 0; i < n; i++ {
		games = append(games, ranking.Game{
			OpponentR:  opponent.R,
			OpponentRD: opponent.RD,
			Score:      score,
		})
	}
	return games
}
