package httpapi

import (
	"fmt"
	"net/http"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// §354's extra link, the write half.
//
// `store.LinkComplaint` has existed since M2 with a complete body — it resolves both rows,
// refuses a complaint that is not `validated`, inserts and touches `features.updated_at` — and
// nothing called it, so `feature_complaints` could only ever be written by `CreateFeature`. A
// second complaint that later turns out to bear on the same feature had no way in, which is the
// repository's recurring shape: the data model and the reads landed first, and every schema dump
// and read test says the surface exists.
//
// The complaint id travels in the BODY. The path's {id} is the feature — the resource being
// modified — and a second {id}-shaped segment would have two resolutions competing for one
// chi.URLParam, which is how a nested route ends up checking one id and acting on the other.
type linkComplaintRequest struct {
	ComplaintID int64 `json:"complaint_id"`
}

// handleLinkComplaint attaches a validated complaint to an existing feature.
//
// The bar is `contributor`: the same role that may create a feature (this is the same act, with
// the ids arriving in a body instead of the create form) and the same role that may attach a
// feature tag. Not maintainer — §7's premise is that the corpus is built by contributors, and a
// maintainer dial here would make evidence a curation step.
func (s *Server) handleLinkComplaint(w http.ResponseWriter, r *http.Request) {
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
	// Proves the feature is this project's. The COMPLAINT is proved by the store, on the way
	// into the row: it arrives in the body, so nothing above can know whose it is — and a
	// second guard here would only add a place for the two to disagree.
	f, ok := s.requireFeatureInProject(w, r, projectID)
	if !ok {
		return
	}
	var req linkComplaintRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.ComplaintID <= 0 {
		// 400, not 404: an absent field is a malformed request, and answering "not found"
		// would send the reader looking for a complaint id they never sent.
		mapError(w, fmt.Errorf("%w: complaint_id is required", store.ErrInvalid))
		return
	}
	if err := s.Store.LinkComplaint(r.Context(), projectID, f.ID, req.ComplaintID); err != nil {
		mapError(w, err)
		return
	}
	// The link is written, so the complaint exists and is this project's. Read it only to name
	// it in the audit line — and fall back to the bare id rather than skipping the entry, so a
	// failure here cannot make a write invisible.
	detail := fmt.Sprintf("complaint %d", req.ComplaintID)
	if c, err := s.Store.GetComplaint(r.Context(), req.ComplaintID); err == nil {
		detail = fmt.Sprintf("complaint %d: %s", c.ID, c.Title)
	}
	_ = s.Store.AddAudit(r.Context(), projectID, getActorID(r), "link_complaint", "feature",
		f.ID, detail)
	complaints, err := s.Store.GetFeatureComplaints(r.Context(), f.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	// The whole list, not the new row: the section renders its own count, and a page that has
	// to re-read to learn what it just caused is the read-after-write the tags panel avoids
	// for the same reason.
	writeJSON(w, http.StatusOK, map[string]any{"feature_id": f.ID, "complaints": complaints})
}
