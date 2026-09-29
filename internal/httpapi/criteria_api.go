package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/ranking"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// Criteria-aware ranking endpoints (spec extension, 2026-09-29).
//
// The read path answers the questions this feature exists for: "best designed",
// "best lightweight", and any weighted blend of the two. Every ranked result
// carries the per-criterion breakdown that produced it, because a subjective
// ranking nobody can interrogate is indistinguishable from an opinion.
//
// Route shape mirrors the rest of the project: scoped by slug, camel_snake JSON,
// and the author always comes from the bearer token rather than the body.

type createCriterionRequest struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Direction   string   `json:"direction"`
	DefaultW    *float64 `json:"default_weight"`
}

func (s *Server) handleCreateCriterion(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	var req createCriterionRequest
	if !readJSON(w, r, &req) {
		return
	}
	weight := 1.0
	if req.DefaultW != nil {
		weight = *req.DefaultW
	}
	c, err := s.Store.CreateCriterion(r.Context(), projectID, getActorID(r),
		req.Slug, req.Name, req.Description, req.Direction, weight)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// mapCriteriaError maps the criteria store's scope error onto 400. A reference
// to another project's criterion is a bad request; without this it fell through
// mapError's default and became a 500, implying the platform had failed rather
// than that the caller addressed the wrong project.
func mapCriteriaError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrCriterionScope) {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": err.Error()})
		return
	}
	mapError(w, err)
}

func (s *Server) handleListCriteria(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	list, err := s.Store.ListCriteria(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	if list == nil {
		list = []ranking.Criterion{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleSetCriterionActive(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrNotFound)
		return
	}
	var req struct {
		Active bool `json:"active"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.Store.SetCriterionActive(r.Context(), projectID, id, req.Active); err != nil {
		mapError(w, err)
		return
	}
	c, err := s.Store.GetCriterion(r.Context(), projectID, id)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

type criterionVoteRequest struct {
	FeatureA int64   `json:"feature_a"`
	FeatureB int64   `json:"feature_b"`
	Outcome  string  `json:"outcome"`
	Weight   float64 `json:"weight"`
}

func (s *Server) handleCastCriterionVote(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrNotFound)
		return
	}
	var req criterionVoteRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.Store.CastCriterionVote(r.Context(), projectID, id, getActorID(r),
		req.FeatureA, req.FeatureB, req.Outcome, req.Weight, 0.5); err != nil {
		mapCriteriaError(w, err)
		return
	}
	// The two updated ratings are the useful response: a voter should see what
	// their comparison did, not just a 200.
	out := map[string]any{}
	for key, fid := range map[string]int64{"feature_a": req.FeatureA, "feature_b": req.FeatureB} {
		if rating, err := s.Store.GetCriterionRating(r.Context(), projectID, id, fid); err == nil {
			out[key] = rating
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type compositeRequest struct {
	// Weights is criterion_id -> weight. The caller states emphasis; the maths
	// normalises, so 3:1 and 0.75:0.25 mean the same thing.
	Weights map[string]float64 `json:"weights"`
	// Profile names a saved weighting. Explicit Weights wins, because a caller
	// who sent both has changed their mind since saving the profile.
	Profile string `json:"profile"`
	// IncludeInactive ranks against retired criteria. Off by default.
	IncludeInactive bool `json:"include_inactive"`
	// RequireMinRD drops criteria nobody has voted on, so an empty dimension
	// cannot dilute a result by contributing an arbitrary 0.5 to everyone.
	RequireMinRD bool `json:"require_min_rd"`
	Limit        int  `json:"limit"`
}

func (s *Server) handleCompositeRank(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	var req compositeRequest
	// An empty body is valid: "rank with every criterion's default weight".
	if r.ContentLength > 0 {
		if !readJSON(w, r, &req) {
			return
		}
	}

	var weights []ranking.Weight
	if len(req.Weights) > 0 {
		for k, v := range req.Weights {
			cid, err := strconv.ParseInt(k, 10, 64)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "weights keys must be criterion ids: " + k,
				})
				return
			}
			weights = append(weights, ranking.Weight{CriterionID: cid, Weight: v})
		}
	} else if req.Profile != "" {
		got, err := s.Store.GetCriteriaProfileWeights(r.Context(), projectID, req.Profile)
		if err != nil {
			mapError(w, err)
			return
		}
		weights = got
	}

	results, err := s.Store.CompositeRank(r.Context(), projectID, weights,
		ranking.CompositeOptions{
			IncludeInactive: req.IncludeInactive,
			RequireMinRD:    req.RequireMinRD,
		})
	if err != nil {
		mapError(w, err)
		return
	}
	if req.Limit > 0 && len(results) > req.Limit {
		results = results[:req.Limit]
	}
	if results == nil {
		results = []ranking.Result{}
	}
	// Confidence per feature, so a client can tell a settled ordering from a
	// provisional one without re-deriving it.
	type ranked struct {
		ranking.Result
		Confidence float64 `json:"confidence"`
	}
	out := make([]ranked, 0, len(results))
	for _, res := range results {
		out = append(out, ranked{Result: res, Confidence: ranking.TotalConfidence(res.Contributions)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": out,
		"count":   len(out),
	})
}

type profileRequest struct {
	Slug    string             `json:"slug"`
	Name    string             `json:"name"`
	Weights map[string]float64 `json:"weights"`
}

func (s *Server) handleSaveCriteriaProfile(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	var req profileRequest
	if !readJSON(w, r, &req) {
		return
	}
	weights := make([]ranking.Weight, 0, len(req.Weights))
	for k, v := range req.Weights {
		cid, err := strconv.ParseInt(k, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "weights keys must be criterion ids: " + k,
			})
			return
		}
		weights = append(weights, ranking.Weight{CriterionID: cid, Weight: v})
	}
	id, err := s.Store.CreateCriteriaProfile(r.Context(), projectID, getActorID(r),
		req.Slug, req.Name, weights)
	if err != nil {
		mapCriteriaError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "slug": req.Slug})
}

func (s *Server) handleListCriteriaProfiles(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	list, err := s.Store.ListCriteriaProfiles(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	if list == nil {
		list = []struct {
			Slug string `json:"slug"`
			Name string `json:"name"`
		}{}
	}
	writeJSON(w, http.StatusOK, list)
}
