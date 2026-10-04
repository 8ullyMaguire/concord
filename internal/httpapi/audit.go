package httpapi

import (
	"net/http"
	"strconv"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// handleListProjectAudit serves the project-scoped audit log.
//
// Unauthenticated, for the same reason admin_ledger is: a privileged action
// nobody can read is not auditable. The project gate is requireProjectID, which
// answers a private project with a bare `not found` byte-identical to a
// nonexistent one, so this route leaks no existence oracle -- an audit log must
// not become the side channel that says which slugs exist.
func (s *Server) handleListProjectAudit(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}

	f := store.AuditFilter{ProjectID: projectID}
	q := r.URL.Query()
	f.Action = q.Get("action")
	f.EntityType = q.Get("entity_type")
	// `q`, not `text`. The API spells it `q` because a bare `text` next to
	// `action` and `entity_type` reads as "the text of the entry" rather than
	// "text to search entries for", and the filter spans both action and detail.
	f.Text = q.Get("q")

	// Every numeric parameter is parsed and its error IGNORED, never fatal: a
	// malformed query string narrows the log to nothing the caller asked for,
	// it does not fail the request. handleListAdminLedger (handlers.go:480)
	// makes the same choice, and for the same reason — an audit viewer is read
	// by hand, typed into, and a 400 on a typo is worse than a wrong page.
	if v := q.Get("actor_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.ActorID = n
		}
	}
	if v := q.Get("entity_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.EntityID = n
		}
	}
	if v := q.Get("since"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			f.Since = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Offset = n
		}
	}
	// No upper bound here. ListAudit clamps to AuditMaxLimit and reports the
	// size it actually used in the page, so a caller that asks for a million
	// rows learns that it got the ceiling from the response rather than from an
	// error it has to special-case.
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}

	page, err := s.Store.ListAudit(r.Context(), f)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
