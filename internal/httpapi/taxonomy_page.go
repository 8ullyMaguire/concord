package httpapi

import "net/http"

// handleTaxonomyPage renders §4.3's tag taxonomy: the tags, the proposals, and the
// consent control.
//
// Instance-wide, so there is no slug and no visibility guard. The API mounts these
// routes outside the project router for the same reason -- a taxonomy change affects
// every project and cannot hang off one project's slug -- and both reads are public.
//
// An anonymous visitor sees the proposals and what it would take to decide one, and
// the server refuses their consent with a 401. That is deliberate: the page does not
// hide the control when the server would refuse it, because a control that vanishes
// teaches nobody why the refusal happened. Same rule as the settings page, which
// shows the charter form to everyone and lets requireRole answer.
func (s *Server) handleTaxonomyPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "taxonomy", s.pageFor(r, "Taxonomy"))
}
