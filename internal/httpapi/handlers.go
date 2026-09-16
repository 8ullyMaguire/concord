package httpapi

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/ranking"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

type createComplaintRequest struct {
	ProjectID     int64   `json:"project_id"`
	Title         string  `json:"title"`
	Body          string  `json:"body"`
	Severity      int     `json:"severity"`
	Frequency     float64 `json:"frequency"`
	StrategicMult float64 `json:"strategic_multiplier"`
}

func (s *Server) handleCreateComplaint(w http.ResponseWriter, r *http.Request) {
	var req createComplaintRequest
	if !readJSON(w, r, &req) {
		return
	}
	c, err := s.Store.CreateComplaint(r.Context(), req.ProjectID, getActorID(r), req.Title, req.Body, req.Severity, req.Frequency, req.StrategicMult)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddReputation(r.Context(), req.ProjectID, getActorID(r), "submit_complaint", 5.0)
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleListComplaints(w http.ResponseWriter, r *http.Request) {
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	status := r.URL.Query().Get("status")
	complaints, err := s.Store.ListComplaints(r.Context(), projectID, status)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, complaints)
}

func (s *Server) handleGetComplaint(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	c, err := s.Store.GetComplaint(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleAddImpact(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	severity, _ := strconv.Atoi(r.URL.Query().Get("severity"))
	if severity == 0 {
		severity = 1
	}
	if err := s.Store.AddImpact(r.Context(), id, getActorID(r), severity); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "impact recorded"})
}

func (s *Server) handleValidateComplaint(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	if err := s.Store.ValidateComplaint(r.Context(), id); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "validated"})
}

func (s *Server) handleMergeComplaints(w http.ResponseWriter, r *http.Request) {
	sourceID, _ := strconv.ParseInt(chi.URLParam(r, "source_id"), 10, 64)
	targetID, _ := strconv.ParseInt(chi.URLParam(r, "target_id"), 10, 64)
	if err := s.Store.MergeComplaints(r.Context(), sourceID, targetID); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "merged"})
}

type createFeatureRequest struct {
	ProjectID        int64   `json:"project_id"`
	Title            string  `json:"title"`
	Body             string  `json:"body"`
	Effort           string  `json:"effort"`
	LinkedComplaints []int64 `json:"linked_complaints"`
}

func (s *Server) handleCreateFeature(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	var req createFeatureRequest
	if !readJSON(w, r, &req) {
		return
	}
	f, err := s.Store.CreateFeature(r.Context(), req.ProjectID, getActorID(r), req.Title, req.Body, req.LinkedComplaints)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddReputation(r.Context(), req.ProjectID, getActorID(r), "submit_feature", 5.0)
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) handleListFeatures(w http.ResponseWriter, r *http.Request) {
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	status := r.URL.Query().Get("status")
	features, err := s.Store.ListFeatures(r.Context(), projectID, status)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, features)
}


func (s *Server) handleFeaturePriorities(w http.ResponseWriter, r *http.Request) {
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	proj, err := s.Store.GetProject(r.Context(), chi.URLParam(r, "project_id"))
	if err != nil {
		mapError(w, err)
		return
	}
	charter, err := s.Store.GetCharterForProject(r.Context(), proj.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	priorities, err := s.Store.GetFeaturePriorities(r.Context(), projectID, charter)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, priorities)
}

func (s *Server) handleGetFeature(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	f, err := s.Store.GetFeature(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) handleSetStrategicWeight(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	if err := s.requireRole(projectID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	var req struct {
		Weight float64 `json:"weight"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.Store.SetStrategicWeight(r.Context(), id, req.Weight); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "weight updated"})
}

type castVoteRequest struct {
	FeatureA int64   `json:"feature_a"`
	FeatureB int64   `json:"feature_b"`
	Outcome string  `json:"outcome"`
	Weight  float64 `json:"weight"`
}

func (s *Server) handleCastVote(w http.ResponseWriter, r *http.Request) {
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	// Check actor is at least a contributor in this project
	role, _ := s.Store.GetRoleForProject(r.Context(), projectID, actorID)
	if role == "guest" {
		mapError(w, store.ErrPerm)
		return
	}
	var req castVoteRequest
	if !readJSON(w, r, &req) {
		return
	}
	// Validate both features belong to the project
	a, err := s.Store.GetFeature(r.Context(), req.FeatureA)
	if err != nil {
		mapError(w, err)
		return
	}
	b, err := s.Store.GetFeature(r.Context(), req.FeatureB)
	if err != nil {
		mapError(w, err)
		return
	}
	if a.ProjectID != projectID || b.ProjectID != projectID {
		mapError(w, store.ErrPerm)
		return
	}
	// Load the project's charter for Glicko-2 tau and vote weight cap
	charter, err := s.Store.GetCharterForProject(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	// Compute weight server-side from voter reputation — never trust client-supplied weight
	voterReputation, _ := s.Store.GetReputation(r.Context(), projectID, actorID)
	weight := ranking.VoteWeight(voterReputation, 1.0, charter.VoteWeightCap)
	vote, err := s.Store.RecordVote(r.Context(), projectID, actorID, req.FeatureA, req.FeatureB, req.Outcome, weight, charter)
	if err != nil {
		mapError(w, err)
		return
	}
	// Award reputation for voting
	_ = s.Store.AddReputation(r.Context(), projectID, actorID, "vote", 1.0)
	writeJSON(w, http.StatusCreated, vote)
}

func (s *Server) handleGetNextPair(w http.ResponseWriter, r *http.Request) {
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	a, b, err := s.Store.GetNextPair(r.Context(), projectID, getActorID(r))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feature_a": a, "feature_b": b})
}

type createConsensusRequest struct {
	FeatureID   int64  `json:"feature_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

func (s *Server) handleCreateConsensus(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	var req createConsensusRequest
	if !readJSON(w, r, &req) {
		return
	}
	call, err := s.Store.CreateConsensusCall(r.Context(), projectID, req.FeatureID, req.Title, req.Description)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddReputation(r.Context(), projectID, getActorID(r), "create_consensus_call", 3.0)
	writeJSON(w, http.StatusCreated, call)
}

func (s *Server) handleGetConsensus(w http.ResponseWriter, r *http.Request) {
	callID, _ := strconv.ParseInt(chi.URLParam(r, "call_id"), 10, 64)
	c, err := s.Store.GetConsensusCall(r.Context(), callID)
	if err != nil {
		mapError(w, err)
		return
	}
	positions, err := s.Store.GetPositions(r.Context(), callID)
	if err != nil {
		mapError(w, err)
		return
	}
	objections, err := s.Store.GetObjections(r.Context(), callID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"call": c, "positions": positions, "objections": objections})
}

func (s *Server) handleCastConsensusPosition(w http.ResponseWriter, r *http.Request) {
	callID, _ := strconv.ParseInt(chi.URLParam(r, "call_id"), 10, 64)
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	var req struct {
		Position string `json:"position"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	pos, err := s.Store.CastPosition(r.Context(), callID, getActorID(r), req.Position)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, pos)
}

func (s *Server) handleCreateObjection(w http.ResponseWriter, r *http.Request) {
	callID, _ := strconv.ParseInt(chi.URLParam(r, "call_id"), 10, 64)
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	var req struct {
		Principle string `json:"principle"`
		Violation string `json:"violation"`
		Remedy    string `json:"remedy"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	obj, err := s.Store.CreateObjection(r.Context(), callID, getActorID(r), req.Principle, req.Violation, req.Remedy)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, obj)
}

func (s *Server) handleResolveObjection(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	// Load objection to get project via call
	obj, err := s.Store.GetObjection(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return
	}
	call, err := s.Store.GetConsensusCall(r.Context(), obj.CallID)
	if err != nil {
		mapError(w, err)
		return
	}
	if err := s.requireRole(call.ProjectID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	var req struct {
		Status     string `json:"status"`
		Resolution string `json:"resolution"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.Store.ResolveObjection(r.Context(), id, req.Status, req.Resolution); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

func (s *Server) handleCloseConsensus(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	callID, _ := strconv.ParseInt(chi.URLParam(r, "call_id"), 10, 64)
	summary, err := s.Store.CloseConsensusCall(r.Context(), callID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleGetBoard(w http.ResponseWriter, r *http.Request) {
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	columns, cards, err := s.Store.GetBoard(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"columns": columns, "cards": cards})
}

func (s *Server) handleMoveCard(w http.ResponseWriter, r *http.Request) {
	cardID, _ := strconv.ParseInt(chi.URLParam(r, "card_id"), 10, 64)
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	newColumn := r.URL.Query().Get("to")
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	if err := s.Store.MoveCard(r.Context(), cardID, newColumn, projectID); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "moved"})
}

func (s *Server) handleCreateMergeRequest(w http.ResponseWriter, r *http.Request) {
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	var req struct {
		FeatureID int64  `json:"feature_id"`
		Title     string `json:"title"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	mr, err := s.Store.CreateMergeRequest(r.Context(), projectID, req.FeatureID, getActorID(r), req.Title)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddReputation(r.Context(), mr.ProjectID, getActorID(r), "create_merge_request", 3.0)
	writeJSON(w, http.StatusCreated, mr)
}

func (s *Server) handleApproveMerge(w http.ResponseWriter, r *http.Request) {
	mrID, _ := strconv.ParseInt(chi.URLParam(r, "mr_id"), 10, 64)
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	if err := s.Store.ApproveMerge(r.Context(), mrID, getActorID(r)); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

func (s *Server) handleExecuteMerge(w http.ResponseWriter, r *http.Request) {
	mrID, _ := strconv.ParseInt(chi.URLParam(r, "mr_id"), 10, 64)
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	if err := s.Store.ExecuteMerge(r.Context(), mrID, projectID); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "merged"})
}

func (s *Server) handleRejectMerge(w http.ResponseWriter, r *http.Request) {
	mrID, _ := strconv.ParseInt(chi.URLParam(r, "mr_id"), 10, 64)
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	if getActorID(r) == 0 {
		mapError(w, fmt.Errorf("authentication required"))
		return
	}
	if err := s.Store.RejectMerge(r.Context(), mrID, projectID); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
}

func (s *Server) handleCreateList(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "list created"})
}

func (s *Server) handleListLists(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []map[string]string{})
}

func (s *Server) handleCreateRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "request created"})
}

func (s *Server) handleAnswerRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Body string `json:"body"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "answer created"})
}

func (s *Server) handleVoteAnswer(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "voted"})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "index", s.page("Home"))
}

func (s *Server) handleSearchPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	s.render(w, http.StatusOK, "search", struct {
		pageData
		Query string
	}{s.page("Search"), q})
}

func (s *Server) handleProjectPage(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	s.render(w, http.StatusOK, "project", struct {
		pageData
		Slug string
	}{s.page(slug), slug})
}

func (s *Server) handleBoardPage(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	s.render(w, http.StatusOK, "board", struct {
		pageData
		Slug string
	}{s.page("Board - " + slug), slug})
}

func getActorID(r *http.Request) int64 {
	actor := r.Context().Value("actor_id")
	if a, ok := actor.(int64); ok {
		return a
	}
	return 0
}


// roleHierarchy maps roles to numeric ranks for comparison.
var roleHierarchy = map[string]int{
	"guest":       0,
	"user":        1,
	"contributor": 2,
	"reviewer":    3,
	"maintainer":  4,
	"owner":       5,
}

// requireRole returns an error if the actor's role in the project
// is below the minimum required role. Returns nil if authorized.
func (s *Server) requireRole(projectID int64, minRole string, r *http.Request) error {
	actorID := getActorID(r)
	if actorID == 0 {
		return store.ErrAuth
	}
	role, _ := s.Store.GetRoleForProject(r.Context(), projectID, actorID)
	actorRank, ok := roleHierarchy[role]
	if !ok {
		actorRank = 0
	}
	minRank, ok := roleHierarchy[minRole]
	if !ok {
		minRank = 2 // default to contributor
	}
	if actorRank < minRank {
		return store.ErrPerm
	}
	return nil
}

// getActorIDOr401 returns the actor ID or nil if not authenticated.

// ---------------------------------------------------------------- health

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------- list pages

func (s *Server) handleProjectsPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "projects", s.page("Projects"))
}

func (s *Server) handleListRequests(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []map[string]string{})
}
