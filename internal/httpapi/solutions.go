package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"git.polarisocial.xyz/concord/concord/internal/store"
	"github.com/go-chi/chi/v5"
)

// solutionLimit reads the ?limit= parameter, defaulting to 100 like the other
// list handlers. A solution board is read whole in practice -- a feature has a
// handful of solutions, not thousands -- but an unbounded query against a
// caller-chosen limit is not something to hand out by default.
func solutionLimit(r *http.Request) int {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	return limit
}

// featureInProject resolves {id} as a feature and checks it belongs to the
// project in the path.
//
// Shared by every solution route because getting it wrong means serving another
// project's feature under this project's URL, which is a visibility bug rather
// than a routing one.
func (s *Server) featureInProject(w http.ResponseWriter, r *http.Request, projectID int64) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return 0, false
	}
	feature, err := s.Store.GetFeature(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return 0, false
	}
	if feature.ProjectID != projectID {
		mapError(w, store.ErrPerm)
		return 0, false
	}
	return id, true
}

// Solutions (spec revision 4 §6.3-6.5).
//
// The feature page's solution board: §6.4 names the columns it shows -- rank,
// score, confidence, coverage, effort, risk, type, and a pro/con digest -- and
// §6.3's inputs are all writable through here.
//
// Reads are unauthenticated on purpose, matching §6.1's duplicate panel and §2.5's
// vote log: a ranking nobody can read is not a ranking anybody trusts.

// handleListSolutions serves the solution board: every solution for a feature,
// strongest first, with its coverage and its rating.
func (s *Server) handleListSolutions(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	featureID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return
	}
	// Scoped to the project in the path: a feature id from another project must
	// not return that project's solutions under this project's URL.
	feature, err := s.Store.GetFeature(r.Context(), featureID)
	if err != nil {
		mapError(w, err)
		return
	}
	if feature.ProjectID != projectID {
		mapError(w, store.ErrPerm)
		return
	}

	scores, err := s.Store.ListSolutions(r.Context(), featureID, solutionLimit(r))
	if err != nil {
		mapError(w, err)
		return
	}
	if scores == nil {
		// A feature nobody has proposed for is the normal state of a new feature.
		// An empty JSON array is the honest answer; a 404 reads as breakage.
		scores = []store.SolutionScore{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"feature_id": featureID,
		"solutions":  scores,
	})
}

// handleGetSolution serves one solution with its design detail and its claims.
func (s *Server) handleGetSolution(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	featureID, ok := s.featureInProject(w, r, projectID)
	if !ok {
		return
	}
	solutionID, err := strconv.ParseInt(chi.URLParam(r, "solution_id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return
	}

	solution, err := s.Store.GetSolution(r.Context(), solutionID)
	if err != nil {
		mapError(w, err)
		return
	}
	// Checked against the feature in the path as well as the project: a solution
	// belongs to exactly one feature, and serving feature A's solution under
	// feature B's URL would show the board a competitor that is not in it.
	if solution.FeatureID != featureID {
		mapError(w, store.ErrNotFound)
		return
	}
	coverage, err := s.Store.ListCoverage(r.Context(), solutionID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"solution":    solution,
		"coverage":    coverage,
		"is_baseline": solution.IsBaseline(),
	})
}

type createSolutionRequest struct {
	Title       string `json:"title"`
	Body        string `json:"body"`
	Type        string `json:"type"`
	ExternalRef string `json:"external_ref"`
	Affiliation string `json:"affiliation"`
	// ParentSolutionID forks this solution: §6.3's "same, but with X".
	ParentSolutionID int64 `json:"parent_solution_id"`
}

// handleCreateSolution files a solution against a feature.
func (s *Server) handleCreateSolution(w http.ResponseWriter, r *http.Request) {
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	featureID, ok := s.featureInProject(w, r, projectID)
	if !ok {
		return
	}
	var req createSolutionRequest
	if !readJSON(w, r, &req) {
		return
	}

	solution, err := s.Store.CreateSolution(r.Context(), store.CreateSolutionInput{
		FeatureID:        featureID,
		AuthorID:         actorID,
		Title:            req.Title,
		Body:             req.Body,
		Type:             req.Type,
		ExternalRef:      req.ExternalRef,
		Affiliation:      req.Affiliation,
		ParentSolutionID: req.ParentSolutionID,
	})
	if err != nil {
		s.mapSolutionError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"solution":    solution,
		"is_baseline": solution.IsBaseline(),
	})
}

type claimCoverageRequest struct {
	ComplaintID int64  `json:"complaint_id"`
	Claim       string `json:"claim"`
}

// handleClaimCoverage records §6.3's coverage claim: which linked complaints a
// solution resolves, and which it explicitly leaves unresolved.
func (s *Server) handleClaimCoverage(w http.ResponseWriter, r *http.Request) {
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	featureID, ok := s.featureInProject(w, r, projectID)
	if !ok {
		return
	}
	solutionID, err := strconv.ParseInt(chi.URLParam(r, "solution_id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return
	}
	var req claimCoverageRequest
	if !readJSON(w, r, &req) {
		return
	}

	solution, err := s.Store.GetSolution(r.Context(), solutionID)
	if err != nil {
		mapError(w, err)
		return
	}
	if solution.FeatureID != featureID {
		mapError(w, store.ErrNotFound)
		return
	}
	if err := s.Store.ClaimCoverage(r.Context(), solutionID, req.ComplaintID, req.Claim); err != nil {
		s.mapSolutionError(w, err)
		return
	}
	score, err := s.Store.SolutionCoverageScore(r.Context(), featureID, solutionID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"coverage": score})
}

type contestCoverageRequest struct {
	Reason string `json:"reason"`
}

// handleContestCoverage contests a coverage claim (§6.4: "Coverage claims are
// challengeable"). A contested claim stops counting until it is restated.
func (s *Server) handleContestCoverage(w http.ResponseWriter, r *http.Request) {
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	featureID, ok := s.featureInProject(w, r, projectID)
	if !ok {
		return
	}
	solutionID, err := strconv.ParseInt(chi.URLParam(r, "solution_id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return
	}
	complaintID, err := strconv.ParseInt(chi.URLParam(r, "complaint_id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return
	}
	var req contestCoverageRequest
	if !readJSON(w, r, &req) {
		return
	}

	solution, err := s.Store.GetSolution(r.Context(), solutionID)
	if err != nil {
		mapError(w, err)
		return
	}
	if solution.FeatureID != featureID {
		mapError(w, store.ErrNotFound)
		return
	}
	if err := s.Store.ContestCoverage(r.Context(), solutionID, complaintID, actorID, req.Reason); err != nil {
		s.mapSolutionError(w, err)
		return
	}
	score, err := s.Store.SolutionCoverageScore(r.Context(), featureID, solutionID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"coverage": score})
}

type voteSolutionRequest struct {
	SolutionA int64   `json:"solution_a"`
	SolutionB int64   `json:"solution_b"`
	Outcome   string  `json:"outcome"`
	Reason    string  `json:"reason"`
	Weight    float64 `json:"weight"`
}

// handleVoteSolutions casts a pairwise vote in a feature's solution arena.
//
// It is a separate route from the feature vote on purpose. The two arenas rank
// different things against different competitors, and a shared handler would have
// to infer which one was meant from the id -- which is how a solution id ends up
// compared against a feature.
func (s *Server) handleVoteSolutions(w http.ResponseWriter, r *http.Request) {
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	featureID, ok := s.featureInProject(w, r, projectID)
	if !ok {
		return
	}
	if err := s.Store.JoinProject(r.Context(), projectID, actorID); err != nil {
		mapError(w, err)
		return
	}
	var req voteSolutionRequest
	if !readJSON(w, r, &req) {
		return
	}
	// Both solutions must belong to this feature. Checked before the arena is
	// touched, so a request naming a solution from another feature cannot spend
	// votes in this one.
	a, err := s.Store.GetSolution(r.Context(), req.SolutionA)
	if err != nil {
		mapError(w, err)
		return
	}
	b, err := s.Store.GetSolution(r.Context(), req.SolutionB)
	if err != nil {
		mapError(w, err)
		return
	}
	if a.FeatureID != featureID || b.FeatureID != featureID {
		mapError(w, store.ErrPerm)
		return
	}

	arena, err := s.Store.EnsureArena(r.Context(), store.ArenaSolution, projectID, featureID, "", "")
	if err != nil {
		mapError(w, err)
		return
	}
	weight := req.Weight
	if weight <= 0 {
		weight = 1.0
	}
	vote, err := s.Store.CastArenaVote(r.Context(), arena.ID, actorID,
		store.EntitySolution, a.ID, store.EntitySolution, b.ID,
		req.Outcome, req.Reason, weight)
	if err != nil {
		s.mapSolutionError(w, err)
		return
	}
	scores, err := s.Store.ListSolutions(r.Context(), featureID, solutionLimit(r))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"vote": vote, "solutions": scores})
}

// mapSolutionError turns the store's solution errors into something a client can
// act on.
//
// The messages are specific because these are the errors a filer hits on the way
// in: "a complementary solution is not ranked against its rivals" is a different
// correction from "permission denied", and a filer who cannot tell them apart
// cannot tell which mistake they made.
func (s *Server) mapSolutionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrSelfVote):
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "you proposed one of these",
			"detail": "an entry cannot be ranked by the person who wrote it; ask someone with no stake in the outcome",
		})
	case errors.Is(err, store.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "that solution cannot be filed as written",
			"detail": err.Error(),
		})
	case errors.Is(err, store.ErrNotABaseline):
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "the do-nothing baseline cannot be removed",
			"detail": "a solution only matters if it beats doing nothing, so the baseline stays",
		})
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrArenaNotFound):
		mapError(w, store.ErrNotFound)
	default:
		mapError(w, err)
	}
}
