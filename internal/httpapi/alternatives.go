package httpapi

// Alternatives arenas — S2's API (spec §2, phase3-spec §5).
//
// Four routes, and the point of all four is that they are WIRING. The arena kind
// (`store.ArenaAlternatives`), the entry entity (`store.EntityProject`), the
// rating maths (`CastArenaVote`) and the leaderboard (`ArenaLeaderboard`) all
// already existed and already worked; `ArenaAlternatives` had never been created
// by anything, so all 70 live arenas were feature-priority. This file is what
// makes the kind reachable, which is the whole of R1's claim.
//
// Two rules here are load-bearing enough to name at the top, because both fail
// invisibly:
//
//   - **No baseline.** §6.3's "do nothing" is a solution concept. In an
//     alternatives arena "do nothing" means "keep using the incumbent", which is
//     the arena's own subject rather than a competitor to it. Nothing here calls
//     setBaseline for this arena type. A baseline appearing would render as a
//     legitimate-looking ranked entry, so it is a mutation-gate mutant.
//
//   - **Visibility on both sides.** A candidate the caller cannot read cannot be
//     added, and must not be ranked into a response the caller can see. Both
//     refusals are a bare `not found` — a 403 or a 400 naming the id confirms the
//     project exists, which is exactly the enumeration oracle S1's panels already
//     closed once.
import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/ranking"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// AlternativesPerArena bounds the ranked competitors rendered for one use case.
const AlternativesPerArena = 50

type createAlternativeRequest struct {
	// UseCase is REQUIRED and the refusal names it. FindArena keys the other
	// arena shapes on `use_case = ''`, so an empty use case would collide with
	// them under a different type — the same collision that makes a missing
	// field here a correctness problem rather than a validation nicety.
	UseCase string `json:"use_case"`
	// Question is optional: DefaultArenaQuestion supplies "Which is the better
	// alternative?" when empty. A use case is required because the arena is keyed
	// on it; the question is not.
	Question string `json:"question"`
}

type addCompetitorRequest struct {
	// ProjectID is the COMPETING project's numeric id — not a slug. This is the
	// one place in the file where the route's {project_id} (a slug, resolved by
	// requireProjectID) and a body's project_id (a number) coexist, which is why
	// the field is named for its role rather than for its type.
	ProjectID int64 `json:"project_id"`
}

type castAlternativesVoteRequest struct {
	AProjectID int64  `json:"a_project_id"`
	BProjectID int64  `json:"b_project_id"`
	Outcome    string `json:"outcome"`
	Reason     string `json:"reason"`
}

type alternativesRes struct {
	ProjectID int64              `json:"project_id"`
	Arenas    []alternativeArena `json:"arenas"`
}

type alternativeArena struct {
	ArenaID    int64              `json:"arena_id"`
	UseCase    string             `json:"use_case"`
	Question   string             `json:"question"`
	EntryCount int                `json:"entry_count"`
	Entries    []alternativeEntry `json:"entries"`
}

type alternativeEntry struct {
	Rank    int   `json:"rank"`
	Project int64 `json:"project_id"`
	// Slug and Title are the identity of the competitor. Without them a ranked
	// list of numeric ids is not readable — the same defect the solutions panel
	// had when SolutionScore carried no Title. The text is store.Project's Name;
	// the JSON field is `title` because that is what the page calls it.
	Slug      string  `json:"slug"`
	Title     string  `json:"title"`
	Rating    float64 `json:"rating"`
	RD        float64 `json:"rd"`
	Games     int     `json:"games"`
	BetterFor string  `json:"better_for"`
	WorseFor  string  `json:"worse_for"`
}

// handleListAlternatives returns every alternatives arena on this project, each
// with its ranked competitors.
//
// Read-side visibility filtering happens here, not only on add: a competitor
// that was public when added and is private now must not be ranked into a
// response the caller can see. Filtering is applied AFTER the leaderboard read so
// the ranking is the arena's real ranking — dropping unreadable entries from the
// sorted output, rather than sorting a filtered subset.
func (s *Server) handleListAlternatives(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}

	arenas, err := s.Store.ListProjectArases(r.Context(), projectID, store.ArenaAlternatives)
	if err != nil {
		mapError(w, err)
		return
	}

	res := alternativesRes{ProjectID: projectID, Arenas: []alternativeArena{}}
	for _, a := range arenas {
		rows, err := s.Store.ArenaLeaderboard(r.Context(), a.ID, AlternativesPerArena)
		if err != nil {
			mapError(w, err)
			return
		}
		entry := alternativeArena{
			ArenaID:  a.ID,
			UseCase:  a.UseCase,
			Question: a.Question,
			// Never nil, so a client does not have to distinguish "no entries"
			// from "null", for the same reason the solutions panel does.
			Entries: []alternativeEntry{},
		}

		for _, e := range rows {
			// Defence in depth: the arena is project-scoped and
			// AddArenaCompetitor only adds EntityProject entries, so this should
			// never skip anything. It is here because a leaderboard entry whose
			// project row is gone (deleted while ranked) renders as a blank row
			// otherwise, and a blank ranked row is worse than an absent one.
			if e.EntityType != store.EntityProject {
				continue
			}
			proj, err := s.Store.GetProjectByID(r.Context(), e.EntityID)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				mapError(w, err)
				return
			}
			// The visibility rule, applied at read time. Same anti-enumeration
			// reasoning as above: a reader learns nothing about the unreadable
			// project, including that it is in this arena at all.
			if !s.projectReadable(r, proj) {
				continue
			}
			entry.Entries = append(entry.Entries, alternativeEntry{
				Rank:      len(entry.Entries) + 1,
				Project:   proj.ID,
				Slug:      proj.Slug,
				Title:     proj.Name,
				Rating:    e.R,
				RD:        e.RD,
				Games:     e.Games,
				BetterFor: a.UseCase,
				WorseFor:  notForUseCase(a.UseCase),
			})
		}
		// Recount from what is actually rendered, not from arena.Count. The
		// subquery count includes entries this caller may not see, so a panel
		// showing "4 competitors" above three visible rows is a contradiction on
		// the page — and the count is the sentence a reader acts on.
		entry.EntryCount = len(entry.Entries)
		res.Arenas = append(res.Arenas, entry)
	}

	writeJSON(w, http.StatusOK, res)
}

// notForUseCase is the "worse for" half of a §7.3 better-for/worse-for pair.
//
// A use case is stated positively ("a team that needs audit logs"), so its
// negative is not a negation of the words but the question the arena answers: a
// competitor ranked below the leader is worse FOR that use case. Stated as the
// question rather than as "not a team that needs audit logs" because the latter
// reads as a claim that the project is unsuitable for the use case, which is
// precisely what an arena cannot tell you — it ranks competitors, it does not
// disqualify its subject.
func notForUseCase(useCase string) string {
	useCase = strings.TrimSpace(useCase)
	if useCase == "" {
		return ""
	}
	return "worse than the leader for " + useCase
}

// handleCreateAlternative creates an alternatives arena for this project.
func (s *Server) handleCreateAlternative(w http.ResponseWriter, r *http.Request) {
	// Authentication FIRST, before the project is resolved. The reverse order
	// would make the status depend on whether the project exists: an anonymous
	// POST to a real project and to a nonexistent one would answer 403 and 404,
	// which enumerates the slugs on the instance (auth.go:105).
	if _, ok := s.requireWriteActor(w, r); !ok {
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	// Authentication before the project is resolved is requireProjectID's job;
	// the ROLE check needs the resolved id, so it comes after. A caller who is
	// neither gets 403 here having already learned nothing new, because
	// requireProjectID already refused an unreadable project as a bare 404.
	if err := s.requireRole(projectID, "contributor", r); err != nil {
		mapError(w, err)
		return
	}

	var req createAlternativeRequest
	if !readJSON(w, r, &req) {
		return
	}
	useCase := strings.TrimSpace(req.UseCase)
	if useCase == "" {
		// The field is named in the message on purpose: this is the one refusal a
		// client can fix without reading the spec, so the message is the fix.
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "use_case is required: an alternatives arena is keyed on it, " +
				"and an empty one collides with the other arena kinds",
		})
		return
	}

	arena, err := s.Store.EnsureArena(r.Context(), store.ArenaAlternatives,
		projectID, 0, useCase, req.Question)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"arena": arena})
}

// handleAddAlternativeCompetitor adds a competing project to an arena.
func (s *Server) handleAddAlternativeCompetitor(w http.ResponseWriter, r *http.Request) {
	// Authentication first, for the same reason as handleCreateAlternative.
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
	arenaID, err := strconv.ParseInt(chi.URLParam(r, "arena_id"), 10, 64)
	if err != nil || arenaID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid arena id"})
		return
	}

	// The arena must belong to THIS project. Checking it before reading the body
	// means a request naming another project's arena is refused without spending
	// a parse, and — more importantly — without the existence oracle that a
	// body-derived 404 would otherwise be.
	arena, err := s.Store.GetArena(r.Context(), arenaID)
	if err != nil {
		s.mapAlternativesError(w, err)
		return
	}
	if arena.ProjectID != projectID {
		// Same answer as an arena that does not exist. A 403 here confirms the
		// arena id is real, which is the enumeration this whole route family
		// refuses to provide.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	var req addCompetitorRequest
	if !readJSON(w, r, &req) {
		return
	}

	// Visibility of the CANDIDATE, before the add. Same bare-404 rule: a refusal
	// that names the candidate would confirm a private project exists.
	cand, err := s.Store.GetProjectByID(r.Context(), req.ProjectID)
	if err != nil {
		s.mapAlternativesError(w, err)
		return
	}
	if !s.projectReadable(r, cand) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	if err := s.Store.AddArenaCompetitor(r.Context(), arenaID, req.ProjectID); err != nil {
		s.mapAlternativesError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"arena_id":   arenaID,
		"competitor": cand.Slug,
		"project_id": cand.ID,
	})
}

// handleCastAlternativesVote records a better-for / worse-for judgement.
//
// The existing `POST .../vote` reuse the spec §5.1 promised: CastArenaVote
// already takes (arenaID, voter, aType, aID, bType, bID, ...), so ranking
// projects needs no new vote code. That reuse is the point of R1, and this
// handler is where it either holds or quietly does not — hence
// TestAnAlternativesArenaRanksProjectsNotFeatures.
func (s *Server) handleCastAlternativesVote(w http.ResponseWriter, r *http.Request) {
	// Authentication first, for the same reason as handleCreateAlternative.
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
	arenaID, err := strconv.ParseInt(chi.URLParam(r, "arena_id"), 10, 64)
	if err != nil || arenaID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid arena id"})
		return
	}
	arena, err := s.Store.GetArena(r.Context(), arenaID)
	if err != nil {
		s.mapAlternativesError(w, err)
		return
	}
	if arena.ProjectID != projectID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	var req castAlternativesVoteRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.AProjectID == req.BProjectID {
		mapError(w, store.ErrSameEntityVote)
		return
	}

	// Both sides must be READABLE by this voter, not merely exist. Skipping this
	// would let a member rank a private competitor they cannot see, which both
	// leaks its existence through the rating movement and lets them judge it.
	// The arena's own project is excluded by AddArenaCompetitor, but a vote
	// naming it directly would bypass that, so the store's own-project guard is
	// NOT enough on its own here.
	for _, id := range []int64{req.AProjectID, req.BProjectID} {
		if id == arena.ProjectID {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "the arena's own project cannot be voted against another " +
					"project; an alternatives arena ranks competitors",
			})
			return
		}
		p, err := s.Store.GetProjectByID(r.Context(), id)
		if err != nil {
			s.mapAlternativesError(w, err)
			return
		}
		if !s.projectReadable(r, p) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
	}

	// Weight is computed here, never taken from the body — the same rule as the
	// feature and solution vote handlers, for the same reason: a client-supplied
	// weight is a client-supplied influence.
	//
	// Reputation and role only, with NO expertise multiplier and no age gate.
	// Those two are solution-specific: expertise is tag-scoped to the solutions
	// being judged, and there are no solution tags on a project. Applying
	// `ranking.ExpertiseMultiplier` with an empty tag set would multiply by a
	// neutral 1.0 and pretend to have done something.
	actorID := getActorID(r)
	charter, err := s.Store.GetCharterForProject(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	// The voter's own reputation, in the project that owns the arena. An
	// alternatives arena deliberately spans projects: the arena belongs to the
	// project asking the question, so reputation is the asker's, not the
	// competitors'. Reading it against a competitor's project would score a
	// voter by standing in a community they are not a member of.
	reputation, err := s.Store.GetReputation(r.Context(), projectID, actorID)
	if err != nil {
		mapError(w, err)
		return
	}
	role, err := s.Store.GetRoleForProject(r.Context(), projectID, actorID)
	if err != nil {
		mapError(w, err)
		return
	}
	weight := ranking.VoteWeight(reputation, ranking.RoleMultiplier(role), charter.VoteWeightCap)

	vote, err := s.Store.CastArenaVote(r.Context(), arenaID, actorID,
		store.EntityProject, req.AProjectID,
		store.EntityProject, req.BProjectID,
		req.Outcome, req.Reason, weight)
	if err != nil {
		s.mapAlternativesError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"vote": vote})
}

// mapAlternativesError maps the store's alternatives errors onto status codes.
//
// The bare `not found` for a nonexistent candidate is the anti-enumeration rule,
// identical in effect to requireProjectID's: a 404 that names the id confirms
// the project exists.
func (s *Server) mapAlternativesError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	mapError(w, err)
}
