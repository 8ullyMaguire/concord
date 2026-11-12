package httpapi

// Milestones, the roadmap, dependencies, alternative evidence and the intake mode
// (docs/specs/idea-intake-and-milestones-spec.md, R16/R17/R18).
//
// One file rather than five because they are one product decision: a feature's
// evidence, the group it belongs to, the date that group promises and the work it
// waits on. The routes stay project-scoped ({project_id} is a SLUG on these
// paths) so visibility, consent and the role ladder apply without a second gate.

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// projectFromPath resolves the {project_id} slug parameter for this file's routes.
func (s *Server) milestoneProject(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	proj, ok := s.requireProjectIDProject(w, r)
	if !ok {
		return store.Project{}, false
	}
	return proj, true
}

// requireProjectIDProject is requireProjectID's sibling that hands back the row:
// these handlers need the project's id AND its visibility, and two lookups for one
// answer is how the two can disagree.
func (s *Server) requireProjectIDProject(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	slug := chi.URLParam(r, "project_id")
	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		mapError(w, err)
		return store.Project{}, false
	}
	if !s.projectReadable(r, proj) {
		mapError(w, store.ErrNotFound)
		return store.Project{}, false
	}
	return proj, true
}

// ---------- milestones ----------

func (s *Server) handleListMilestones(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.milestoneProject(w, r)
	if !ok {
		return
	}
	ms, err := s.Store.ListMilestones(r.Context(), proj.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ms)
}

type createMilestoneRequest struct {
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	TargetDate *float64 `json:"target_date"`
}

func (s *Server) handleCreateMilestone(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.milestoneProject(w, r)
	if !ok {
		return
	}
	if err := s.requireRole(proj.ID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	var req createMilestoneRequest
	if !readJSON(w, r, &req) {
		return
	}
	m, err := s.Store.CreateMilestone(r.Context(), proj.ID, req.Title, req.Body, req.TargetDate)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), proj.ID, getActorID(r), "milestone.create", "milestone", m.ID, m.Title)
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) handleGetMilestone(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.milestoneProject(w, r)
	if !ok {
		return
	}
	id, ok := milestoneIDParam(w, r)
	if !ok {
		return
	}
	m, err := s.Store.GetMilestone(r.Context(), proj.ID, id)
	if err != nil {
		mapError(w, err)
		return
	}
	features, err := s.Store.MilestoneFeatures(r.Context(), proj.ID, id)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"milestone": m, "features": features})
}

func (s *Server) handleUpdateMilestone(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.milestoneProject(w, r)
	if !ok {
		return
	}
	if err := s.requireRole(proj.ID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	id, ok := milestoneIDParam(w, r)
	if !ok {
		return
	}
	var req createMilestoneRequest
	if !readJSON(w, r, &req) {
		return
	}
	m, err := s.Store.UpdateMilestone(r.Context(), proj.ID, id, req.Title, req.Body, req.TargetDate)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleSetMilestoneStatus(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.milestoneProject(w, r)
	if !ok {
		return
	}
	if err := s.requireRole(proj.ID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	id, ok := milestoneIDParam(w, r)
	if !ok {
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	m, err := s.Store.SetMilestoneStatus(r.Context(), proj.ID, id, getActorID(r), req.Status)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handleCloseMilestone is the only way into done/cancelled, because it is the only
// path that reconciles the unfinished features (§4.4).
func (s *Server) handleCloseMilestone(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.milestoneProject(w, r)
	if !ok {
		return
	}
	if err := s.requireRole(proj.ID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	id, ok := milestoneIDParam(w, r)
	if !ok {
		return
	}
	var req struct {
		SuccessorID *int64 `json:"successor_id"`
		Cancel      bool   `json:"cancel"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	m, moved, err := s.Store.CloseMilestone(r.Context(), proj.ID, id, getActorID(r), req.SuccessorID, req.Cancel)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"milestone": m, "moved": moved})
}

// handleAssignFeatures is the bulk assign (§6): a selection of features into one
// milestone, appended after what is already there.
func (s *Server) handleAssignFeatures(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.milestoneProject(w, r)
	if !ok {
		return
	}
	if err := s.requireRole(proj.ID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	id, ok := milestoneIDParam(w, r)
	if !ok {
		return
	}
	var req struct {
		FeatureIDs []int64 `json:"feature_ids"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	n, err := s.Store.AssignFeatures(r.Context(), proj.ID, id, req.FeatureIDs)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), proj.ID, getActorID(r), "feature.milestone", "milestone", id,
		"assigned "+strconv.Itoa(n)+" feature(s)")
	writeJSON(w, http.StatusOK, map[string]any{"assigned": n})
}

// handleReorderMilestone writes an explicit order (§6).
func (s *Server) handleReorderMilestone(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.milestoneProject(w, r)
	if !ok {
		return
	}
	if err := s.requireRole(proj.ID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	id, ok := milestoneIDParam(w, r)
	if !ok {
		return
	}
	var req struct {
		FeatureIDs []int64 `json:"feature_ids"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.Store.ReorderMilestone(r.Context(), proj.ID, id, req.FeatureIDs); err != nil {
		mapError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- roadmap ----------

// handleRoadmap is the ONE shape the roadmap page renders and any client reads
// (spec §5). A page that computed its own numbers would be a page whose numbers
// can disagree with the route's.
func (s *Server) handleRoadmap(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.milestoneProject(w, r)
	if !ok {
		return
	}
	ms, err := s.Store.ListMilestones(r.Context(), proj.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	total, open, shipped := 0, 0, 0
	for _, m := range ms {
		total += m.Total
		open += m.Open
		shipped += m.Shipped
	}
	// Slippage (§5): for a closed milestone, the days between its target date and
	// the last shipment; for an open one, the age of its oldest open feature. Both
	// are null rather than 0 when there is nothing to measure -- 0 days of slip is
	// the best possible answer and an easy thing to fake with a zero.
	slip, err := s.Store.RoadmapSlippage(r.Context(), proj.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"slug":       proj.Slug,
		"milestones": ms,
		"totals":     map[string]int{"total": total, "open": open, "shipped": shipped},
		"slippage":   slip,
	})
}

// ---------- feature <-> milestone ----------

func (s *Server) handleSetFeatureMilestone(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.milestoneProject(w, r)
	if !ok {
		return
	}
	if err := s.requireRole(proj.ID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	featureID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrNotFound)
		return
	}
	var req struct {
		MilestoneID *int64 `json:"milestone_id"`
		Position    int    `json:"position"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	blockers, err := s.Store.SetFeatureMilestone(r.Context(), proj.ID, featureID, req.MilestoneID, req.Position)
	if err != nil {
		mapError(w, err)
		return
	}
	detail := "milestone cleared"
	if req.MilestoneID != nil {
		detail = "milestone " + strconv.FormatInt(*req.MilestoneID, 10)
	}
	_ = s.Store.AddAudit(r.Context(), proj.ID, getActorID(r), "feature.milestone", "feature", featureID, detail)
	writeJSON(w, http.StatusOK, map[string]any{"feature_id": featureID, "open_blockers": blockers})
}

// ---------- dependencies ----------

func (s *Server) handleGetFeatureDeps(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.milestoneProject(w, r)
	if !ok {
		return
	}
	featureID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrNotFound)
		return
	}
	blockedBy, err := s.Store.BlockedBy(r.Context(), featureID)
	if err != nil {
		mapError(w, err)
		return
	}
	blocks, err := s.Store.Blocks(r.Context(), featureID)
	if err != nil {
		mapError(w, err)
		return
	}
	open, err := s.Store.OpenBlockers(r.Context(), featureID)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = proj
	writeJSON(w, http.StatusOK, map[string]any{
		"blocked_by": blockedBy, "blocks": blocks, "open_blockers": open,
	})
}

func (s *Server) handleAddFeatureDep(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.milestoneProject(w, r)
	if !ok {
		return
	}
	if err := s.requireRole(proj.ID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	featureID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrNotFound)
		return
	}
	var req struct {
		BlockerID int64 `json:"blocker_id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.Store.AddDependency(r.Context(), proj.ID, req.BlockerID, featureID); err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), proj.ID, getActorID(r), "feature.dep.add", "feature", featureID,
		"blocked by #"+strconv.FormatInt(req.BlockerID, 10))
	writeJSON(w, http.StatusOK, map[string]any{"feature_id": featureID, "blocker_id": req.BlockerID})
}

func (s *Server) handleRemoveFeatureDep(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.milestoneProject(w, r)
	if !ok {
		return
	}
	if err := s.requireRole(proj.ID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	featureID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrNotFound)
		return
	}
	blockerID, _ := strconv.ParseInt(r.URL.Query().Get("blocker_id"), 10, 64)
	if err := s.Store.RemoveDependency(r.Context(), proj.ID, blockerID, featureID); err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), proj.ID, getActorID(r), "feature.dep.remove", "feature", featureID,
		"unblocked from #"+strconv.FormatInt(blockerID, 10))
	w.WriteHeader(http.StatusNoContent)
}

// ---------- alternative evidence ----------

func (s *Server) handleListFeatureEvidence(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.milestoneProject(w, r); !ok {
		return
	}
	featureID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrNotFound)
		return
	}
	ev, err := s.Store.ListFeatureEvidence(r.Context(), featureID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *Server) handleAddFeatureEvidence(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.milestoneProject(w, r)
	if !ok {
		return
	}
	if err := s.requireRole(proj.ID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	featureID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrNotFound)
		return
	}
	var req struct {
		Kind  string `json:"kind"`
		RefID int64  `json:"ref_id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.Store.AddFeatureEvidence(r.Context(), proj.ID, featureID, req.Kind, req.RefID); err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), proj.ID, getActorID(r), "feature.evidence", "feature", featureID,
		req.Kind+" #"+strconv.FormatInt(req.RefID, 10))
	writeJSON(w, http.StatusCreated, map[string]any{"kind": req.Kind, "ref_id": req.RefID})
}

func (s *Server) handleRemoveFeatureEvidence(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.milestoneProject(w, r)
	if !ok {
		return
	}
	if err := s.requireRole(proj.ID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	featureID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrNotFound)
		return
	}
	kind := r.URL.Query().Get("kind")
	refID, _ := strconv.ParseInt(r.URL.Query().Get("ref_id"), 10, 64)
	if err := s.Store.RemoveFeatureEvidence(r.Context(), proj.ID, featureID, kind, refID); err != nil {
		mapError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- intake mode ----------

func (s *Server) handleSetIntakeMode(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.milestoneProject(w, r)
	if !ok {
		return
	}
	if err := s.requireRole(proj.ID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.Store.SetIntakeMode(r.Context(), proj.ID, getActorID(r), req.Mode); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"intake_mode": req.Mode})
}

// ---------- pages ----------

func (s *Server) handleRoadmapPage(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil || !s.projectReadable(r, proj) {
		s.notFoundPage(w, r, "There is no project at this address.")
		return
	}
	s.render(w, http.StatusOK, "roadmap", struct {
		pageData
		Slug string
	}{s.pageFor(r, "Roadmap - "+slug), slug})
}

func (s *Server) handleMilestonePage(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil || !s.projectReadable(r, proj) {
		s.notFoundPage(w, r, "There is no project at this address.")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		s.notFoundPage(w, r, "There is no milestone at this address.")
		return
	}
	if _, err := s.Store.GetMilestone(r.Context(), proj.ID, id); err != nil {
		s.notFoundPage(w, r, "There is no milestone at this address.")
		return
	}
	s.render(w, http.StatusOK, "milestone", struct {
		pageData
		Slug        string
		MilestoneID int64
	}{s.pageFor(r, "Milestone - "+slug), slug, id})
}

// milestoneIDParam parses {id} and answers the styled 404 shape on a malformed one:
// a numeric path segment that is not a number is a missing row, not a 400 the
// reader can act on.
func milestoneIDParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrNotFound)
		return 0, false
	}
	return id, true
}
