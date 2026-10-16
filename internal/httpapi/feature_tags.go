package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// A feature's tags are §6.2's steering input: the ratified themes a feature's tags match are
// summed into its priority. That made this the highest-consequence metadata on the page and
// the API had no way to set it (feature_tags: 0 rows on the live instance, 537 features, no
// INSERT outside a test). Reading it back matters for the same reason: a user cannot confirm
// what steers their feature unless the answer is served.

type featureTagRequest struct {
	Tag string `json:"tag"`
}

// requireFeatureInProject resolves {id} and proves it belongs to the URL's project. Both
// halves are load-bearing: feature ids are global, so a route that read only {id} would let
// any project's URL address any feature (the mistake handleGetFeature documents).
func (s *Server) requireFeatureInProject(w http.ResponseWriter, r *http.Request, projectID int64) (store.Feature, bool) {
	var f store.Feature
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return f, false
	}
	f, err = s.Store.GetFeature(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return f, false
	}
	if f.ProjectID != projectID {
		mapError(w, store.ErrNotFound)
		return f, false
	}
	return f, true
}

// handleGetFeatureTags serves the tags that decide which themes steer this feature.
func (s *Server) handleGetFeatureTags(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	f, ok := s.requireFeatureInProject(w, r, projectID)
	if !ok {
		return
	}
	tags, err := s.Store.GetFeatureTags(r.Context(), f.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feature_id": f.ID, "tags": tags})
}

// handleAddFeatureTag attaches an existing tag to a feature.
//
// Contributor and above: applying a tag that is already in the taxonomy is the same act as
// applying one to a project (ApplyExistingProjectTag), and that route requires contributor.
// The tag must exist -- inventing taxonomy through a metadata field is how a tag list becomes
// write-only, the lesson §4.3 records.
func (s *Server) handleAddFeatureTag(w http.ResponseWriter, r *http.Request) {
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
	f, ok := s.requireFeatureInProject(w, r, projectID)
	if !ok {
		return
	}
	var req featureTagRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.Store.AddFeatureTag(r.Context(), projectID, f.ID, getActorID(r), req.Tag); err != nil {
		mapError(w, err)
		return
	}
	tags, err := s.Store.GetFeatureTags(r.Context(), f.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feature_id": f.ID, "tags": tags})
}

// handleRemoveFeatureTag detaches a tag. Not housekeeping: the tags decide the priority, so a
// wrong one silently steers the feature and must be removable by whoever can add one.
func (s *Server) handleRemoveFeatureTag(w http.ResponseWriter, r *http.Request) {
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
	f, ok := s.requireFeatureInProject(w, r, projectID)
	if !ok {
		return
	}
	var req featureTagRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.Store.RemoveFeatureTag(r.Context(), projectID, f.ID, getActorID(r), req.Tag); err != nil {
		mapError(w, err)
		return
	}
	tags, err := s.Store.GetFeatureTags(r.Context(), f.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feature_id": f.ID, "tags": tags})
}
