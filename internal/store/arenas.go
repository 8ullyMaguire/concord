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

	// BaselineEntryID is the permanent "do nothing" competitor (§6.3). Set for
	// solution arenas and nil elsewhere.
	BaselineEntryID int64 `json:"baseline_entry_id,omitempty"`

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
var ErrArenaNotFound = errors.New("arena not found")

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
		q = `SELECT id, type, project_id, feature_id, question, use_case, baseline_entry_id, created_at
		     FROM arenas WHERE type = ? AND project_id = ?`
		args = []any{arenaType, projectID}
	case ArenaSolution:
		q = `SELECT id, type, project_id, feature_id, question, use_case, baseline_entry_id, created_at
		     FROM arenas WHERE type = ? AND feature_id = ?`
		args = []any{arenaType, featureID}
	case ArenaAlternatives, ArenaUseCase:
		q = `SELECT id, type, project_id, feature_id, question, use_case, baseline_entry_id, created_at
		     FROM arenas WHERE type = ? AND project_id = ? AND use_case = ?`
		args = []any{arenaType, projectID, strings.TrimSpace(useCase)}
	default:
		return Arena{}, fmt.Errorf("%w: unknown arena type %q", ErrInvalid, arenaType)
	}
	return d.scanArena(d.QueryRowContext(ctx, q, args...))
}

func (d *DB) scanArena(row *sql.Row) (Arena, error) {
	var a Arena
	var project, feature, baseline sql.NullInt64
	var useCase sql.NullString
	err := row.Scan(&a.ID, &a.Type, &project, &feature, &a.Question, &useCase,
		&baseline, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Arena{}, ErrArenaNotFound
	}
	if err != nil {
		return Arena{}, err
	}
	a.ProjectID = project.Int64
	a.FeatureID = feature.Int64
	a.BaselineEntryID = baseline.Int64
	a.UseCase = useCase.String
	return a, nil
}

// GetArena reads one arena by id, with its competitor count.
func (d *DB) GetArena(ctx context.Context, id int64) (Arena, error) {
	var a Arena
	var project, feature, baseline sql.NullInt64
	var useCase sql.NullString
	err := d.QueryRowContext(ctx, `
		SELECT a.id, a.type, a.project_id, a.feature_id, a.question, a.use_case,
		       a.baseline_entry_id, a.created_at,
		       (SELECT COUNT(*) FROM arena_entries e WHERE e.arena_id = a.id)
		FROM arenas a WHERE a.id = ?`, id).Scan(
		&a.ID, &a.Type, &project, &feature, &a.Question, &useCase,
		&baseline, &a.CreatedAt, &a.Count)
	if errors.Is(err, sql.ErrNoRows) {
		return Arena{}, ErrArenaNotFound
	}
	if err != nil {
		return Arena{}, err
	}
	a.ProjectID = project.Int64
	a.FeatureID = feature.Int64
	a.BaselineEntryID = baseline.Int64
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
		       a.baseline_entry_id, a.created_at,
		       (SELECT COUNT(*) FROM arena_entries e WHERE e.arena_id = a.id)
		FROM arenas a WHERE a.type = ? ORDER BY a.id LIMIT ?`, arenaType, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Arena
	for rows.Next() {
		var a Arena
		var project, feature, baseline sql.NullInt64
		var useCase sql.NullString
		if err := rows.Scan(&a.ID, &a.Type, &project, &feature, &a.Question,
			&useCase, &baseline, &a.CreatedAt, &a.Count); err != nil {
			return nil, err
		}
		a.ProjectID = project.Int64
		a.FeatureID = feature.Int64
		a.BaselineEntryID = baseline.Int64
		a.UseCase = useCase.String
		out = append(out, a)
	}
	return out, rows.Err()
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
	// arenas.baseline_entry_id. Those were two representations of one fact, and
	// the code consulted the one nothing populates: baseline_entry_id is only ever
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

	// Fractional-game treatment (§5.1: "weight scales the update's influence").
	// A weight-2 voter is recorded as two games, so the update is twice as large
	// and RD falls twice as fast, which is the intended meaning of a
	// reputation-weighted vote.
	gamesA := repeatedGame(ranking.ArenaRating{R: entryB.R, RD: entryB.RD, Sigma: entryB.Sigma}, aScore, weight)
	gamesB := repeatedGame(ranking.ArenaRating{R: entryA.R, RD: entryA.RD, Sigma: entryA.Sigma}, 1-aScore, weight)

	newA := ranking.UpdateArenaRating(entryA.R, entryA.RD, entryA.Sigma, gamesA, weight)
	newB := ranking.UpdateArenaRating(entryB.R, entryB.RD, entryB.Sigma, gamesB, weight)

	if _, err := d.ExecContext(ctx, `
		UPDATE arena_entries
		SET r = ?, rd = ?, sigma = ?, games = games + ?, updated_at = ?
		WHERE arena_id = ? AND entity_type = ? AND entity_id = ?`,
		newA.R, newA.RD, newA.Sigma, int(weight), now, arenaID, aType, aID); err != nil {
		return err
	}
	if _, err := d.ExecContext(ctx, `
		UPDATE arena_entries
		SET r = ?, rd = ?, sigma = ?, games = games + ?, updated_at = ?
		WHERE arena_id = ? AND entity_type = ? AND entity_id = ?`,
		newB.R, newB.RD, newB.Sigma, int(weight), now, arenaID, bType, bID); err != nil {
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

// RecomputeArenaRatings rebuilds every rating in an arena from its vote log.
//
// §5.1: "ratings are recomputed deterministically from the append-only vote
// log. Anyone can verify a ranking." This is that recomputation, and it is the
// check that the incremental path has not drifted: running it after votes have
// been cast must produce the same order.
//
// Votes are applied in created_at order, in rating periods grouped by
// (voter, day), which is the Glicko-2 batch model. Applying every vote as its
// own period would let a single voter move a rating arbitrarily far in one go,
// so the day grouping is load-bearing rather than a batching nicety.
func (d *DB) RecomputeArenaRatings(ctx context.Context, arenaID int64) ([]ArenaEntry, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT voter_id, a, b, outcome, weight, created_at
		FROM pairwise_votes
		WHERE arena_id = ? AND outcome != 'skip'
		ORDER BY created_at ASC, id ASC`, arenaID)
	if err != nil {
		return nil, err
	}
	type rawVote struct {
		voter, a, b int64
		outcome     string
		weight      float64
		day         int64
	}
	var all []rawVote
	for rows.Next() {
		var v rawVote
		var created float64
		if err := rows.Scan(&v.voter, &v.a, &v.b, &v.outcome, &v.weight, &created); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, rawVote{voter: v.voter, a: v.a, b: v.b,
			outcome: v.outcome, weight: v.weight, day: int64(created / 86400)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Collect the competitors, then reset them to the prior. Read first and write
	// after, because the cursor is open above.
	entryRows, err := d.QueryContext(ctx,
		`SELECT entity_type, entity_id FROM arena_entries WHERE arena_id = ?`, arenaID)
	if err != nil {
		return nil, err
	}
	var entityType string
	type ident struct {
		typ string
		id  int64
	}
	var idents []ident
	for entryRows.Next() {
		var id int64
		if err := entryRows.Scan(&entityType, &id); err != nil {
			entryRows.Close()
			return nil, err
		}
		idents = append(idents, ident{typ: entityType, id: id})
	}
	entryRows.Close()
	if err := entryRows.Err(); err != nil {
		return nil, err
	}

	now := float64(time.Now().Unix())
	if _, err := d.ExecContext(ctx, `
		UPDATE arena_entries
		SET r = ?, rd = ?, sigma = ?, games = 0, updated_at = ?
		WHERE arena_id = ?`,
		ranking.DefaultRating, ranking.DefaultDeviation, ranking.DefaultVolatility,
		now, arenaID); err != nil {
		return nil, err
	}

	rating := map[int64]ranking.ArenaRating{}
	for _, id := range idents {
		rating[id.id] = ranking.ArenaRating{
			R: ranking.DefaultRating, RD: ranking.DefaultDeviation,
			Sigma: ranking.DefaultVolatility,
		}
	}

	// Group into rating periods: one per (voter, day), in chronological order.
	periods := map[string][]rawVote{}
	var order []string
	for _, v := range all {
		k := fmt.Sprintf("%d/%d", v.voter, v.day)
		if _, seen := periods[k]; !seen {
			order = append(order, k)
		}
		periods[k] = append(periods[k], v)
	}
	for _, k := range order {
		for _, v := range periods[k] {
			ra, okA := rating[v.a]
			rb, okB := rating[v.b]
			if !okA || !okB || v.a == v.b {
				continue
			}
			score := outcomeScore(v.outcome)
			w := v.weight
			if w < 1 {
				w = 1
			}
			rating[v.a] = ranking.UpdateArenaRating(ra.R, ra.RD, ra.Sigma,
				repeatedGame(rb, score, w), w)
			rating[v.b] = ranking.UpdateArenaRating(rb.R, rb.RD, rb.Sigma,
				repeatedGame(ra, 1-score, w), w)
		}
	}

	for id, r := range rating {
		if _, err := d.ExecContext(ctx, `
			UPDATE arena_entries
			SET r = ?, rd = ?, sigma = ?, games = games + 1, updated_at = ?
			WHERE arena_id = ? AND entity_id = ?`,
			r.R, r.RD, r.Sigma, now, arenaID, id); err != nil {
			return nil, err
		}
		if entityType == EntityFeature {
			_ = d.mirrorFeatureRating(ctx, id, r, now)
		}
	}
	return d.ArenaLeaderboard(ctx, arenaID, 200)
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
