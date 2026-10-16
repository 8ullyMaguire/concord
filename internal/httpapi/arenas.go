// Package httpapi: §5's arena loop.
//
// §5 opens by saying that EVERYTHING ranked in Concord is an arena, and the store
// has been able to rank one since the first pass: CreateArena, AddArenaEntry,
// CastArenaVote, NextArenaPair, ArenaLeaderboard, ListArenaVotes and
// RecomputeArenaRatings all work. Three of those had no route at all, so the loop
// was half a loop: a voter could cast a comparison (the alternatives and solution
// routes do), and could not be asked for one, could not read the log, and could not
// check the ranking they were contributing to.
//
// The four routes here are deliberately NOT nested under /alternatives/... The
// store's arena functions are generic -- an arena is a set of competitors, a
// question and a context -- and nesting these under one kind would say they only
// work for that kind. `{project_id}` is the arena's owning project.
//
// §5.1's "Anyone can verify a ranking" is what the verification route exists for.
// It does not write; the recompute route is the repair, and it is the only writer.
package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/ranking"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// arenaSide is one competitor as a client renders it: what it is, what it is
// called, and where its rating stands.
//
// Label is resolved here rather than in the browser because the browser would need
// one request per competitor, and because a competitor whose row is gone (a project
// deleted while ranked) has to render as something other than "undefined".
type arenaSide struct {
	EntityType   string  `json:"entity_type"`
	EntityID     int64   `json:"entity_id"`
	Label        string  `json:"label"`
	Slug         string  `json:"slug,omitempty"`
	R            float64 `json:"r"`
	RD           float64 `json:"rd"`
	Conservative float64 `json:"conservative"`
	Games        int     `json:"games"`
	IsBaseline   bool    `json:"is_baseline"`
	// WinProbability is §5.1's "A is better with 87% confidence", against the
	// OTHER side of the pair. Zero on the leaderboard route, where there is no
	// pair to be compared with; against the leader on the leaderboard read.
	WinProbability float64 `json:"win_probability,omitempty"`
}

type arenaPair struct {
	ArenaID  int64     `json:"arena_id"`
	Type     string    `json:"type"`
	Question string    `json:"question"`
	UseCase  string    `json:"use_case,omitempty"`
	A        arenaSide `json:"a"`
	B        arenaSide `json:"b"`
}

type arenaVoteRow struct {
	ID        int64     `json:"id"`
	VoterID   int64     `json:"voter_id"`
	Outcome   string    `json:"outcome"`
	Weight    float64   `json:"weight"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt float64   `json:"created_at"`
	A         arenaSide `json:"a"`
	B         arenaSide `json:"b"`
}

type arenaVotesRes struct {
	ArenaID int64          `json:"arena_id"`
	Count   int            `json:"count"`
	Votes   []arenaVoteRow `json:"votes"`
}

type arenaVerificationRes struct {
	ArenaID int64 `json:"arena_id"`
	// Agrees is the whole answer: every stored rating is the one the vote log
	// produces. The per-entry rows are there so a reader can see WHICH, not just
	// that something is wrong.
	Agrees    bool                      `json:"agrees"`
	Divergent int                       `json:"divergent"`
	Entries   []store.ArenaVerification `json:"entries"`
}

type arenaRecomputeRes struct {
	ArenaID int64 `json:"arena_id"`
	// Changed counts the entries whose stored rating the log did not reproduce --
	// the drift the recompute just repaired. Reported before the write, because
	// after it the number is always zero and the interesting question is what it
	// was.
	Changed int         `json:"changed"`
	Entries []arenaSide `json:"entries"`
}

// arenaInProject resolves {arena_id} under an already-resolved project.
//
// The project check is the same rule as §26's and §28's: arena ids are global, so
// a route that read only {arena_id} would let any project's URL answer for any
// arena, and a DISTINCT refusal (403) would confirm the id is real. An arena in
// another project therefore answers exactly what a missing arena answers.
func (s *Server) arenaInProject(w http.ResponseWriter, r *http.Request, projectID int64) (store.Arena, bool) {
	arenaID, err := strconv.ParseInt(chi.URLParam(r, "arena_id"), 10, 64)
	if err != nil || arenaID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid arena id"})
		return store.Arena{}, false
	}
	arena, err := s.Store.GetArena(r.Context(), arenaID)
	if err != nil {
		mapError(w, err)
		return store.Arena{}, false
	}
	if arena.ProjectID != projectID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return store.Arena{}, false
	}
	return arena, true
}

// arenaSideOf fills a competitor's label and its derived display fields.
//
// A competitor whose entity row is gone keeps its id and an empty label: the
// ranking still contains it, and dropping the row would make the leaderboard
// silently disagree with its own vote log.
func (s *Server) arenaSideOf(ctx context.Context, e store.ArenaEntry) arenaSide {
	side := arenaSide{
		EntityType: e.EntityType, EntityID: e.EntityID,
		R: e.R, RD: e.RD, Conservative: e.Conservative,
		Games: e.Games, IsBaseline: e.IsBaseline,
	}
	switch e.EntityType {
	case store.EntityFeature:
		if f, err := s.Store.GetFeature(ctx, e.EntityID); err == nil {
			side.Label = f.Title
		}
	case store.EntityProject:
		if p, err := s.Store.GetProjectByID(ctx, e.EntityID); err == nil {
			side.Label, side.Slug = p.Name, p.Slug
		}
	case store.EntitySolution:
		if sol, err := s.Store.GetSolution(ctx, e.EntityID); err == nil {
			side.Label = sol.Title
			if sol.IsBaseline() {
				side.IsBaseline = true
			}
		}
	case store.EntityListEntry:
		if le, err := s.Store.GetListEntry(ctx, e.EntityID); err == nil {
			side.Label = le.Title
		}
	}
	return side
}

// handleArenaNextPair answers "which comparison should I judge next?" (§5.1:
// "Pair selection: active learning (close ratings plus high RD) mixed with random
// exploration, a cap on repeated exposure, and no voting on your own items").
//
// A signed-out reader has judged nothing, so actorID(r) returns 0 and the answer is
// the arena's most informative pair. A voter gets a pair they have not already
// judged -- the store excludes theirs, including the pairs they skipped, because
// §5.1 makes a skip "I don't understand this", not a verdict.
func (s *Server) handleArenaNextPair(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	arena, ok := s.arenaInProject(w, r, projectID)
	if !ok {
		return
	}
	a, b, err := s.Store.NextArenaPair(r.Context(), arena.ID, actorID(r))
	if err != nil {
		// ErrNotFound here is the store's "this voter has judged every pair",
		// whose message says so. mapError turns it into a 404.
		mapError(w, err)
		return
	}
	sa := s.arenaSideOf(r.Context(), a)
	sb := s.arenaSideOf(r.Context(), b)
	// §5.1: the win probability is shown to the voter so the question has a
	// scale. It is computed from the ratings BEFORE the vote -- after it, the
	// answer would be the thing being tested.
	sa.WinProbability = ranking.WinProbability(a.R, a.RD, b.R, b.RD)
	sb.WinProbability = ranking.WinProbability(b.R, b.RD, a.R, a.RD)
	writeJSON(w, http.StatusOK, arenaPair{
		ArenaID: arena.ID, Type: arena.Type, Question: arena.Question,
		UseCase: arena.UseCase, A: sa, B: sb,
	})
}

// handleArenaVotes is §5.2's "per-user vote audit view feed appeals": the
// append-only log, newest first, with each comparison's reason.
//
// No role and no authentication: an audit view that only insiders can read is not
// an audit view, and this is a read of data the leaderboard is computed from.
func (s *Server) handleArenaVotes(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	arena, ok := s.arenaInProject(w, r, projectID)
	if !ok {
		return
	}
	votes, err := s.Store.ListArenaVotes(r.Context(), arena.ID, 50)
	if err != nil {
		mapError(w, err)
		return
	}
	// Never nil, so a client does not have to tell "no votes" from "null".
	res := arenaVotesRes{ArenaID: arena.ID, Votes: []arenaVoteRow{}}
	for _, v := range votes {
		res.Votes = append(res.Votes, arenaVoteRow{
			ID: v.ID, VoterID: v.VoterID, Outcome: v.Outcome, Weight: v.Weight,
			Reason: v.Reason, CreatedAt: v.CreatedAt,
			A: s.arenaSideOf(r.Context(), v.A),
			B: s.arenaSideOf(r.Context(), v.B),
		})
	}
	res.Count = len(res.Votes)
	writeJSON(w, http.StatusOK, res)
}

// handleVerifyArena is §5.1's "Anyone can verify a ranking": it replays the vote
// log and reports whether the stored ratings are the ones the log produces.
//
// It WRITES NOTHING, which is what makes it something any reader may run. The
// recompute route is the repair; keeping them apart is the difference between
// publishing a check and publishing a button that silently rewrites a ranking.
func (s *Server) handleVerifyArena(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	arena, ok := s.arenaInProject(w, r, projectID)
	if !ok {
		return
	}
	checks, err := s.Store.VerifyArenaRatings(r.Context(), arena.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	res := arenaVerificationRes{ArenaID: arena.ID, Agrees: true, Entries: checks}
	for _, c := range checks {
		if !c.Agrees {
			res.Agrees = false
			res.Divergent++
		}
	}
	writeJSON(w, http.StatusOK, res)
}

// handleRecomputeArena repairs an arena's ratings from its vote log.
//
// It writes, so it needs an account and the project's contributor role, checked in
// that order (auth before the project is resolved, or the status code enumerates
// the instance's slugs -- auth.go:105) and only after the project is readable.
//
// It reports how many entries it changed, measured BEFORE the write: on a healthy
// arena the answer is zero and the recompute is a no-op, which is the property
// §5.1 promises and which this route now demonstrates rather than assumes.
func (s *Server) handleRecomputeArena(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireWriteActor(w, r); !ok {
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	if err := s.requireRole(projectID, "contributor", r); err != nil {
		mapError(w, err)
		return
	}
	arena, ok := s.arenaInProject(w, r, projectID)
	if !ok {
		return
	}

	before, err := s.Store.VerifyArenaRatings(r.Context(), arena.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	changed := 0
	for _, c := range before {
		if !c.Agrees {
			changed++
		}
	}

	entries, err := s.Store.RecomputeArenaRatings(r.Context(), arena.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	res := arenaRecomputeRes{ArenaID: arena.ID, Changed: changed, Entries: []arenaSide{}}
	for _, e := range entries {
		res.Entries = append(res.Entries, s.arenaSideOf(r.Context(), e))
	}
	writeJSON(w, http.StatusOK, res)
}
