package httpapi

import (
	"net/http"
)

// handleStatsSummary serves the instance census (spec §3.1, STAT-1).
//
// GET is not mutating, so the consent gate does not apply to it
// (guest_handlers.go: isMutating) -- a visitor must be able to read the
// instance's own numbers without accepting anything first.
func (s *Server) handleStatsSummary(w http.ResponseWriter, r *http.Request) {
	census, err := s.Store.Census(r.Context())
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, census)
}

// handleStatsPage renders the statistics page.
//
// The page is a shell: every number on it is fetched from
// /api/v1/stats/summary at runtime, so the page cannot publish a number the API
// does not. That is spec §3.4's "one computation, two renderings" -- a page that
// recomputes the same aggregate is two answers waiting to disagree, and the page
// is the one nobody notices is wrong.
func (s *Server) handleStatsPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "stats", struct {
		pageData
	}{s.pageFor(r, "Statistics")})
}
