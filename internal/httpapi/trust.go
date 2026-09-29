// Trust-level gated status changes.
//
// The store has had UpdateFeatureStatus since M2 but nothing could reach it: a
// feature was created as `draft` and only ever reached `shipped` by approving
// a merge request. That is the right path for code and the wrong one for
// importing a portfolio that already exists — there was no way to say "this is
// a plan" or "this is an idea" at all.
//
// This is the direct route, gated on a per-user numeric trust level whose
// threshold is configuration, defaulting to the highest level the instance
// grants. Role is deliberately not used: role is per-project and answers "what
// may this person do here", trust level is per-user and answers "how much do
// we believe them instance-wide".
package httpapi

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/config"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// validFeatureStatuses mirrors the CHECK constraint on features.status. The
// list is duplicated deliberately: a Go constant cannot read a SQL constraint,
// and a status the database would reject should be refused here with a useful
// message rather than surfacing as a constraint violation.
var validFeatureStatuses = map[string]bool{
	"draft": true, "discussion": true, "consensus": true, "ready": true,
	"in_progress": true, "review": true, "shipped": true, "rejected": true,
}

// requireTrustLevel returns an error unless the actor's trust level meets the
// configured minimum.
func (s *Server) requireTrustLevel(r *http.Request) error {
	actorID := getActorID(r)
	if actorID == 0 {
		return store.ErrAuth
	}
	level, err := s.Store.GetTrustLevel(r.Context(), actorID)
	if err != nil {
		return err
	}
	if level < config.TrustLevelMin() {
		return store.ErrPerm
	}
	return nil
}

// handleSetFeatureStatus sets a feature's status directly.
//
// The trust check runs *before* the body is read, so an under-privileged
// caller cannot learn the request shape. The status is validated against the
// same set the schema enforces: accepting an unknown status here would produce
// a database constraint error at write time instead of a clear refusal here.
//
// GetFeature takes no project id, so the ownership check compares the returned
// row's ProjectID. Without it a trusted caller could restatus a feature in
// another project by guessing an id, which is the whole point of the route
// being project-scoped.
func (s *Server) handleSetFeatureStatus(w http.ResponseWriter, r *http.Request) {
	if err := s.requireTrustLevel(r); err != nil {
		mapError(w, err)
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	featureID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrNotFound)
		return
	}

	var req struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !validFeatureStatuses[req.Status] {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("unknown status %q; valid: draft, discussion, consensus, ready, in_progress, review, shipped, rejected", req.Status),
		})
		return
	}

	f, err := s.Store.GetFeature(r.Context(), featureID)
	if err != nil {
		mapError(w, err)
		return
	}
	if f.ProjectID != projectID {
		mapError(w, store.ErrNotFound)
		return
	}

	from := f.Status
	if err := s.Store.UpdateFeatureStatus(r.Context(), featureID, req.Status); err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), projectID, getActorID(r), "set_feature_status", "feature", featureID,
		fmt.Sprintf("%s -> %s: %s", from, req.Status, req.Reason))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "from": from, "to": req.Status})
}
