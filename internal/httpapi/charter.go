package httpapi

import (
	"net/http"

	"git.polarisocial.xyz/concord/concord/internal/governance"
)

// handleGetCharter returns the charter for a project.
func (s *Server) handleGetCharter(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	charter, err := s.Store.GetCharterForProject(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, charter)
}

// handleUpdateCharter updates a project's charter (maintainer+).
func (s *Server) handleUpdateCharter(w http.ResponseWriter, r *http.Request) {
	// Authenticate before resolving the project, so an anonymous caller gets 401
	// whether or not the slug exists. The reverse order leaks which slugs are
	// registered by making the status code depend on it.
	if _, ok := s.requireWriteActor(w, r); !ok {
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
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
