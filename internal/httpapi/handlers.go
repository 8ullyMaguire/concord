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
	// This handler had no authentication check while every sibling did, so a
	// complaint could be filed anonymously: getActorID returned 0 and it was
	// written with author_id 0, attributing it to nobody. A complaint is a
	// claim about a project and is the main input to the ranking, so it needs
	// an author the same way a feature does.
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	var req createComplaintRequest
	if !readJSON(w, r, &req) {
		return
	}
	// Default the strategic multiplier to 1.0 rather than leaving it at the
	// zero value of an omitted field.
	//
	// PainScore multiplies by strategicMult, so a caller that sends severity
	// and frequency but not the multiplier — the obvious thing to do, since
	// only the first two appear in the complaint form — gets a pain score of
	// exactly 0. The complaint is stored, validated, and linked to a feature,
	// and it contributes nothing to that feature's priority with no indication
	// of why. A zero multiplier is not a meaningful value; a neutral one is.
	if req.StrategicMult <= 0 {
		req.StrategicMult = 1.0
	}

	c, err := s.Store.CreateComplaint(r.Context(), req.ProjectID, getActorID(r), req.Title, req.Body, req.Severity, req.Frequency, req.StrategicMult)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), req.ProjectID, getActorID(r), "create_complaint", "complaint", c.ID, req.Title)
	_ = s.Store.AddReputation(r.Context(), req.ProjectID, getActorID(r), "submit_complaint", 5.0)
	// Taking part is what makes someone a participant; see JoinProject.
	_ = s.Store.JoinProject(r.Context(), req.ProjectID, getActorID(r))
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleListComplaints(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
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
	_ = s.Store.AddAudit(r.Context(), 0, getActorID(r), "validate_complaint", "complaint", id, "")
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
	_ = s.Store.AddAudit(r.Context(), req.ProjectID, getActorID(r), "create_feature", "feature", f.ID, req.Title)
	_ = s.Store.AddReputation(r.Context(), req.ProjectID, getActorID(r), "submit_feature", 5.0)
	_ = s.Store.JoinProject(r.Context(), req.ProjectID, getActorID(r))
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) handleListFeatures(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	features, err := s.Store.ListFeatures(r.Context(), projectID, status)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, features)
}

func (s *Server) handleFeaturePriorities(w http.ResponseWriter, r *http.Request) {
	// Deliberately public. This is the output of a process the public is
	// invited to take part in; requiring a token to read the result while
	// anyone may read the proposals would make the site a black box. The
	// private act is casting a vote, not reading the ranking it produces.
	//
	// {project_id} is a slug, not a numeric id. This used to parse it as an
	// integer, discard the error, and then pass that 0 to the ranking query
	// while the project lookup immediately below did the right thing and
	// produced proj.ID. Every caller with a non-numeric slug therefore ranked
	// project 0. Resolve the project once and use its real id.
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
	priorities, err := s.Store.GetFeaturePriorities(r.Context(), proj.ID, charter)
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
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
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
	_ = s.Store.AddAudit(r.Context(), projectID, getActorID(r), "set_strategic_weight", "feature", id, fmt.Sprintf("weight=%g", req.Weight))
	writeJSON(w, http.StatusOK, map[string]string{"status": "weight updated"})
}

type castVoteRequest struct {
	FeatureA int64   `json:"feature_a"`
	FeatureB int64   `json:"feature_b"`
	Outcome  string  `json:"outcome"`
	Weight   float64 `json:"weight"`
}

func (s *Server) handleCastVote(w http.ResponseWriter, r *http.Request) {
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	// A first vote enrols the voter, so the guest check cannot run before it.
	// The order matters and was wrong at first: rejecting guests here made the
	// very first vote impossible, because nobody is a member until they have
	// voted or contributed. The one thing a guest must not do is vote *before*
	// being let in, and JoinProject is what lets them in.
	if err := s.Store.JoinProject(r.Context(), projectID, actorID); err != nil {
		mapError(w, err)
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
	// Refuse a vote on your own feature, in either position. Glicko-2 cannot
	// discount self-preference: a rating inflated by its own author's vote is
	// not a measurement of anyone else's preference, and the author may vote
	// for the same feature against every competitor, without limit. Both
	// features are checked because the pair is unordered as presented.
	//
	// The message names the reason. A bare "permission denied" for an action
	// the interface offered is indistinguishable from a bug to whoever hit it.
	for _, fid := range []int64{req.FeatureA, req.FeatureB} {
		isAuthor, err := s.Store.IsFeatureAuthor(r.Context(), fid, actorID)
		if err != nil {
			mapError(w, err)
			return
		}
		if isAuthor {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "you proposed one of these features",
				"detail": "a feature cannot be ranked by the person who proposed it; ask someone who has no stake in the outcome",
			})
			return
		}
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
	_ = s.Store.AddAudit(r.Context(), projectID, actorID, "cast_vote", "vote", projectID, fmt.Sprintf("feature_a=%d feature_b=%d outcome=%s", req.FeatureA, req.FeatureB, req.Outcome))
	_ = s.Store.AddReputation(r.Context(), projectID, actorID, "vote", 1.0)
	// Enrol only after the vote has been recorded. Enrolling someone whose
	// vote was refused would record a participation that did not happen.
	_ = s.Store.JoinProject(r.Context(), projectID, actorID)
	writeJSON(w, http.StatusCreated, vote)
}

// handleJoinProject enrols the caller as a contributor.
//
// Almost nobody needs this: filing a complaint, proposing a feature and voting
// all enrol you as a side effect, which is the intended path. It exists for
// the person who wants to vote before contributing anything — to rank a
// backlog they did not write — and it is deliberately idempotent, so calling it
// repeatedly never demotes a maintainer.
func (s *Server) handleJoinProject(w http.ResponseWriter, r *http.Request) {
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	role, _ := s.Store.GetRoleForProject(r.Context(), projectID, actorID)
	// A guest and a member get the same 200: the caller's goal is "I can take
	// part", and telling a non-member that they were already enrolled would
	// leak membership state for no benefit.
	_ = role
	if err := s.Store.JoinProject(r.Context(), projectID, actorID); err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), projectID, actorID, "join_project", "project", projectID, "")
	after, _ := s.Store.GetRoleForProject(r.Context(), projectID, actorID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "joined", "role": after})
}

// handleListMembers returns the project's membership.
//
// Public, like the ranking. The roster answers "who is taking part", which is
// the first question anyone asks of a community, and a project whose
// membership is hidden reads as empty.
// handleFeatureTallies returns how much attention each feature has received.
//
// Public, like the ranking. A rating without a count beside it is not
// interpretable: 1500 from four votes and 1500 from four hundred are the same
// number and mean very different things.
func (s *Server) handleFeatureTallies(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	tallies, err := s.Store.FeatureTallies(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	if tallies == nil {
		tallies = []store.FeatureTally{}
	}
	writeJSON(w, http.StatusOK, tallies)
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	members, err := s.Store.ListMembers(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	if members == nil {
		members = []store.Member{}
	}
	writeJSON(w, http.StatusOK, members)
}

func (s *Server) handleGetNextPair(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	// The same check handleCastVote applies, and for the same reason: this
	// endpoint used to serve a comparison to any authenticated account, which
	// was then refused with 403 when it tried to answer. The API was inviting
	// a vote it had already decided to reject. Better to refuse here, before
	// the pair is shown, than after someone has read both descriptions.
	if err := s.Store.JoinProject(r.Context(), projectID, actorID); err != nil {
		mapError(w, err)
		return
	}
	a, b, err := s.Store.GetNextPair(r.Context(), projectID, actorID)
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
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	var req createConsensusRequest
	if !readJSON(w, r, &req) {
		return
	}
	call, err := s.Store.CreateConsensusCall(r.Context(), projectID, req.FeatureID, req.Title, req.Description)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), projectID, getActorID(r), "create_consensus_call", "consensus_call", call.ID, req.Title)
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
	_ = s.Store.AddAudit(r.Context(), 0, getActorID(r), "cast_position", "position", callID, fmt.Sprintf("stance=%s", req.Position))
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
	_ = s.Store.AddAudit(r.Context(), 0, getActorID(r), "create_objection", "objection", obj.ID, req.Principle)
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
	_ = s.Store.AddAudit(r.Context(), summary.Call.ProjectID, getActorID(r), "close_consensus", "consensus_call", callID, summary.Result)
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleGetBoard(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	columns, cards, err := s.Store.GetBoard(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"columns": columns, "cards": cards})
}

func (s *Server) handleMoveCard(w http.ResponseWriter, r *http.Request) {
	cardID, _ := strconv.ParseInt(chi.URLParam(r, "card_id"), 10, 64)
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
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
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
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
	_ = s.Store.AddAudit(r.Context(), 0, getActorID(r), "approve_merge", "merge_request", mrID, "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

func (s *Server) handleExecuteMerge(w http.ResponseWriter, r *http.Request) {
	mrID, _ := strconv.ParseInt(chi.URLParam(r, "mr_id"), 10, 64)
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
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
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
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

// List and request handlers are in lists.go

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

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "login", s.page("Sign in - Concord"))
}

func (s *Server) handleRegisterPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "register", s.page("Create an account - Concord"))
}

// getActorID returns the authenticated user id, or 0 when anonymous.
//
// It delegates to actorID, which reads the typed actorKey that the
// authenticate middleware sets. The old implementation read the string key
// "actor_id", which nothing ever set: grep found no context.WithValue anywhere
// in non-test code, so every caller saw 0 and every write returned 401.
//
// Kept as a separate name because 62 call sites depend on it, and a missed
// rename would be a handler that silently stopped authenticating.
func getActorID(r *http.Request) int64 {
	return actorID(r)
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

// handleListRequests is in lists.go
