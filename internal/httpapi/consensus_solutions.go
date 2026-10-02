package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// §6.5 at the API boundary: the agenda and the fallback chain.
//
// The one route that matters most here is the readiness GET. §6.5's whole
// mechanism is three conditions, and a caller who cannot see why a call has not
// opened has no way to tell "the arena has not settled" from "this feature is
// broken". So the refusal and the read are the same information, and both name
// the failing condition.

// handleSolutionCallReadiness serves §6.5's three conditions for a feature's
// solution arena.
//
// Unauthenticated on purpose: it is a ranking explanation, in the same family as
// §6.1's duplicate panel and §2.5's vote log, and it exposes no more than the
// public solution board does.
func (s *Server) handleSolutionCallReadiness(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	featureID, ok := s.featureInProject(w, r, projectID)
	if !ok {
		return
	}
	readiness, err := s.Store.SolutionCallReadiness(r.Context(), featureID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, readiness)
}

type openSolutionCallRequest struct {
	// Early with a reason is §6.5's discretionary path. Kept on the same route
	// rather than as a separate verb because it is the same decision, taken with
	// fewer preconditions -- and a client that has to pick between two endpoints
	// picks wrong.
	Early  bool   `json:"early"`
	Reason string `json:"reason"`
}

// handleOpenSolutionCall opens a consensus call on a feature's leading solution.
func (s *Server) handleOpenSolutionCall(w http.ResponseWriter, r *http.Request) {
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	featureID, ok := s.featureInProject(w, r, projectID)
	if !ok {
		return
	}
	var req openSolutionCallRequest
	// An empty body is fine: opening on merit needs no parameters, and requiring
	// "{}" from a client to do the obvious thing is a 400 nobody can act on.
	if r.ContentLength > 0 {
		if !readJSON(w, r, &req) {
			return
		}
	}

	if req.Early {
		s.openEarlySolutionCall(w, r, featureID, actorID, req.Reason)
		return
	}
	call, err := s.Store.OpenSolutionCall(r.Context(), featureID, actorID)
	if err != nil {
		s.mapCallReadinessError(w, r, featureID, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"call": call})
}

// openEarlySolutionCall is the shared tail of both routes' early path.
func (s *Server) openEarlySolutionCall(w http.ResponseWriter, r *http.Request, featureID, actorID int64, reason string) {
	call, err := s.Store.OpenSolutionCallEarly(r.Context(), featureID, actorID, reason)
	if err != nil {
		s.mapCallReadinessError(w, r, featureID, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"call": call})
}

type recordOutcomeRequest struct {
	Outcome string `json:"outcome"`
	Summary string `json:"summary"`
	// FallbackTo names the solution that takes over on a fall-back outcome
	// (§6.5's "fall back to #2").
	FallbackTo int64 `json:"fallback_to"`
	// Amendment spawns the derived solution §6.5's "accepted with amendments"
	// requires. Separate from the outcome so a rejected outcome cannot smuggle
	// one in.
	Amendment *struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Type  string `json:"type"`
	} `json:"amendment"`
}

// handleRecordCallOutcome records §6.5's decision and writes the ADR.
func (s *Server) handleRecordCallOutcome(w http.ResponseWriter, r *http.Request) {
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	callID, ok := s.requireCallID(w, r)
	if !ok {
		return
	}
	call, err := s.Store.GetConsensusCall(r.Context(), callID)
	if err != nil {
		mapError(w, err)
		return
	}
	// The call's own project is the authority, not the URL's. A call id from
	// another project must not be decided through this project's endpoint.
	if call.ProjectID != projectID {
		mapError(w, store.ErrPerm)
		return
	}
	var req recordOutcomeRequest
	if !readJSON(w, r, &req) {
		return
	}

	var amendment *store.SolutionAmendment
	if req.Amendment != nil {
		amendment = &store.SolutionAmendment{
			AuthorID: actorID,
			Title:    req.Amendment.Title,
			Body:     req.Amendment.Body,
			Type:     req.Amendment.Type,
		}
	}
	updated, err := s.Store.RecordCallOutcome(r.Context(), callID, req.Outcome,
		req.Summary, req.FallbackTo, amendment)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"call": updated})
}

// handleCallFallback serves §6.5's chain: where a stalled or blocked call goes
// next, strongest first, baseline last.
func (s *Server) handleCallFallback(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	callID, ok := s.requireCallID(w, r)
	if !ok {
		return
	}
	call, err := s.Store.GetConsensusCall(r.Context(), callID)
	if err != nil {
		mapError(w, err)
		return
	}
	if call.ProjectID != projectID {
		mapError(w, store.ErrPerm)
		return
	}
	chain, err := s.Store.CallFallback(r.Context(), callID)
	if err != nil {
		mapError(w, err)
		return
	}
	if chain == nil {
		chain = []store.SolutionScore{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"call_id": callID, "fallback": chain})
}

// requireCallID resolves {call_id} from the URL.
func (s *Server) requireCallID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "call_id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return 0, false
	}
	return id, true
}

// mapCallReadinessError turns §6.5's gate failures into something actionable.
//
// 409, not 400: the request was well-formed and the requester had every right to
// make it, but the arena does not support a decision yet. That is a conflict with
// current state, and the body carries the three conditions so the client can show
// which one is short rather than a bare "not ready".
func (s *Server) mapCallReadinessError(w http.ResponseWriter, r *http.Request, featureID int64, err error) {
	switch {
	case errors.Is(err, store.ErrNotCallable):
		readiness, rerr := s.Store.SolutionCallReadiness(r.Context(), featureID)
		if rerr != nil {
			mapError(w, err)
			return
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":     "§6.5's conditions for opening a call are not met",
			"detail":    err.Error(),
			"readiness": readiness,
		})
	case errors.Is(err, store.ErrNotEligibleCollaborator):
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "opening a call early is for collaborators",
			"detail": "§6.5 gives the discretion to collaborators; " + err.Error(),
		})
	case errors.Is(err, store.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "that call cannot be opened as described",
			"detail": err.Error(),
		})
	default:
		mapError(w, err)
	}
}
