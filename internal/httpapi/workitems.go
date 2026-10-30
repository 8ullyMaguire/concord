package httpapi

import (
	"net/http"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// Work items — spec §4.5.7, store methods in internal/store/workitems.go.
//
// Four routes, and the shape of each is dictated by a rule rather than by taste:
//
//   - the read is unauthenticated like every other project read (§2.5: a ranking nobody can read is
//     not a ranking anybody trusts), and visibility comes from requireProjectID;
//   - labelling is maintainer-only, because "good first issue" is a promise about review capacity
//     and only somebody who will review it can make it;
//   - claiming is contributor-only and 409s on a live claim, mapped from store.ErrDuplicate;
//   - releasing is a DELETE that leaves the row, because a withdrawal is a state.

// handleListWorkItems serves what a person can pick up in this project.
func (s *Server) handleListWorkItems(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	items, err := s.Store.ListWorkItems(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

type workItemLabelRequest struct {
	Difficulty        *string `json:"difficulty"`
	TimeBudgetMinutes *int    `json:"time_budget_minutes"`
	NeedsMentor       bool    `json:"needs_mentor"`
}

// handleSetWorkItemLabel records what a task costs a newcomer.
func (s *Server) handleSetWorkItemLabel(w http.ResponseWriter, r *http.Request) {
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
	f, ok := s.requireFeatureInProject(w, r, projectID)
	if !ok {
		return
	}
	var req workItemLabelRequest
	if !readJSON(w, r, &req) {
		return
	}
	err := s.Store.SetWorkItemLabel(r.Context(), projectID, f.ID, storeWorkItemLabel(req))
	if err != nil {
		mapError(w, err)
		return
	}
	item, err := s.Store.GetWorkItem(r.Context(), projectID, f.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// handleClaimWorkItem takes the single live claim on a feature.
//
// A claim does NOT move the feature: the board is the only mechanism that moves work, and a claim
// that changed status would make two surfaces disagree about what is being built.
func (s *Server) handleClaimWorkItem(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.requireWriteActor(w, r)
	if !ok {
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
	if err := s.Store.ClaimFeature(r.Context(), projectID, f.ID, uid); err != nil {
		mapError(w, err)
		return
	}
	item, err := s.Store.GetWorkItem(r.Context(), projectID, f.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

// handleReleaseClaim withdraws a claim; the row stays with released_at set.
func (s *Server) handleReleaseClaim(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.requireWriteActor(w, r)
	if !ok {
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	f, ok := s.requireFeatureInProject(w, r, projectID)
	if !ok {
		return
	}
	if err := s.Store.ReleaseClaim(r.Context(), projectID, f.ID, uid); err != nil {
		mapError(w, err)
		return
	}
	item, err := s.Store.GetWorkItem(r.Context(), projectID, f.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// storeWorkItemLabel converts the request shape into the store's. It lives here rather than in the
// store so the store keeps one input type and the JSON tags stay a transport concern.
func storeWorkItemLabel(req workItemLabelRequest) store.WorkItemLabel {
	return store.WorkItemLabel{
		Difficulty:        req.Difficulty,
		TimeBudgetMinutes: req.TimeBudgetMinutes,
		NeedsMentor:       req.NeedsMentor,
	}
}
