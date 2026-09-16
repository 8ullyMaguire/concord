package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/discovery"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// ---------------------------------------------------------------- projects

type createProjectRequest struct {
	Slug            string `json:"slug"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	GovernanceModel string `json:"governance_model"` // optional; default collective
	License         string `json:"license"`
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.GovernanceModel == "" {
		req.GovernanceModel = "collective"
	}
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	p, err := s.Store.CreateProject(r.Context(), actorID, req.Slug, req.Name,
		req.Description, req.GovernanceModel, req.License)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.Store.ListProjects(r.Context())
	if err != nil {
		mapError(w, err)
		return
	}
	if projects == nil {
		projects = []store.Project{}
	}
	writeJSON(w, http.StatusOK, projects)
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// ---------------------------------------------------------------- discovery

type tagsRequest struct {
	Tags      []string `json:"tags"`
	AppliedBy string   `json:"applied_by"` // username; auth maps this later (PLAN M8)
}

func (s *Server) handleProjectTags(w http.ResponseWriter, r *http.Request) {
	var req tagsRequest
	if !readJSON(w, r, &req) {
		return
	}
	slug := chi.URLParam(r, "slug")
	var appliedBy int64
	if req.AppliedBy != "" {
		u, err := s.Store.GetUser(r.Context(), req.AppliedBy)
		if err != nil {
			mapError(w, err)
			return
		}
		appliedBy = u.ID
	}
	for _, tag := range req.Tags {
		if err := s.Store.ApplyProjectTag(r.Context(), slug, tag, appliedBy); err != nil {
			mapError(w, err)
			return
		}
	}
	p, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

type languagesRequest struct {
	Languages []store.Language `json:"languages"`
}

func (s *Server) handleProjectLanguages(w http.ResponseWriter, r *http.Request) {
	var req languagesRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.Store.SetProjectLanguages(r.Context(), chi.URLParam(r, "slug"), req.Languages); err != nil {
		mapError(w, err)
		return
	}
	p, err := s.Store.GetProject(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

type metricsRequest struct {
	Stars             int     `json:"stars"`
	Forks             int     `json:"forks"`
	OpenIssues        int     `json:"open_issues"`
	CommitCount       int     `json:"commit_count"`
	Contributors      int     `json:"contributors"`
	LastCommitAgeDays float64 `json:"last_commit_age_days"`
	MedianReviewHours float64 `json:"median_review_hours"`
	Releases90d       int     `json:"releases_90d"`
	License           string  `json:"license"`
}

func (s *Server) handleProjectMetrics(w http.ResponseWriter, r *http.Request) {
	var req metricsRequest
	if !readJSON(w, r, &req) {
		return
	}
	m := discovery.Metrics{
		LastCommitAgeDays: req.LastCommitAgeDays,
		MedianReviewHours: req.MedianReviewHours,
		Contributors:      req.Contributors,
		Releases90d:       req.Releases90d,
	}
	p, err := s.Store.UpdateProjectMetrics(r.Context(), chi.URLParam(r, "slug"), m,
		req.Stars, req.Forks, req.OpenIssues, req.CommitCount, req.License)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}
