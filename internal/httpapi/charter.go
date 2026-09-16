package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/governance"
)

// handleGetCharter returns the charter for a project.
func (s *Server) handleGetCharter(w http.ResponseWriter, r *http.Request) {
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	charter, err := s.Store.GetCharterForProject(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, charter)
}

// handleUpdateCharter updates a project's charter (maintainer+).
func (s *Server) handleUpdateCharter(w http.ResponseWriter, r *http.Request) {
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	if err := s.requireRole(projectID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	var charter governance.Charter
	if !readJSON(w, r, &charter) {
		return
	}
	if err := s.Store.UpdateCharter(r.Context(), projectID, charter); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "charter updated"})
}
