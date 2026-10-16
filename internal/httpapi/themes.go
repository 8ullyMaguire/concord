package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// §6.2's third steering input, at the edge: themes are tag-scoped weights changed only by
// consensus, with a public changelog and an expiry. The store enforces the rules; this file
// only decides what a caller may ASK for and what the answer means.
//
// The two answers that must not be collapsed: 201 is "proposed", and 202 is "recorded, quorum
// not reached". Answering 200 to a consent that ratified nothing tells a user the roadmap
// moved when it did not -- the same mistake the weight-proposal handler documents, which is
// why this one mirrors it clause for clause rather than inventing a shape.

type strategicThemeRequest struct {
	ThemeTag       string  `json:"theme_tag"`
	ThemeNamespace string  `json:"theme_namespace"`
	Weight         float64 `json:"weight"`
	Rationale      string  `json:"rationale"`
}

// handleProposeStrategicTheme opens a proposal to change a tag's weight.
//
// It applies NOTHING. That is the whole design (and the test asserts it): a change that took
// effect on proposal would be the hand-steering §6.2's consensus route exists to remove.
//
// Contributor and not "eligible collaborator", for the reason the weight-proposal handler
// records: proposing is a contribution, and requiring §8.2 eligibility first would ask for a
// contribution before allowing one. Eligibility is enforced where it belongs -- on consent.
func (s *Server) handleProposeStrategicTheme(w http.ResponseWriter, r *http.Request) {
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
	var req strategicThemeRequest
	if !readJSON(w, r, &req) {
		return
	}
	p, err := s.Store.ProposeStrategicTheme(r.Context(), projectID, getActorID(r),
		req.ThemeTag, req.ThemeNamespace, req.Weight, req.Rationale)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), projectID, getActorID(r), "propose_strategic_theme",
		"theme", p.ID, fmt.Sprintf("proposal %d opened for tag %q", p.ID, p.ThemeTag))
	// The message is part of the contract: a caller who reads only this must still learn
	// that nothing has changed yet.
	writeJSON(w, http.StatusCreated, map[string]any{
		"proposal": p,
		"message":  "proposed, not applied -- it takes effect when the project consents",
	})
}

// handleConsentStrategicTheme records a consent, ratifying the theme at quorum.
//
// 200 means the theme is now in force and every feature carrying that tag scores
// differently; 202 means the consent was recorded and nothing has changed.
func (s *Server) handleConsentStrategicTheme(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireWriteActor(w, r); !ok {
		return
	}
	if _, ok := s.requireProjectID(w, r); !ok {
		return
	}
	proposalID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return
	}
	p, result, err := s.Store.ConsentStrategicTheme(r.Context(), proposalID, getActorID(r))
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			// Below quorum. An ordinary intermediate state of a consensus process, so the
			// current state goes back with 202 instead of an error a client retries.
			writeJSON(w, http.StatusAccepted, map[string]any{
				"proposal": p,
				"result":   result,
				"message":  err.Error(),
			})
			return
		}
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"proposal": p,
		"result":   result,
		"status":   "ratified",
		"message":  "ratified -- features tagged " + p.ThemeTag + " now score with this theme",
	})
}

// handleWithdrawStrategicTheme cancels a pending theme change. It changes no weight, which is
// why a maintainer may withdraw someone else's: this is cleanup, not steering.
func (s *Server) handleWithdrawStrategicTheme(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireWriteActor(w, r); !ok {
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	proposalID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return
	}
	role, _ := s.Store.GetRoleForProject(r.Context(), projectID, getActorID(r))
	isMaintainer := role == "maintainer" || role == "owner"
	if err := s.Store.WithdrawStrategicThemeProposal(r.Context(), proposalID, getActorID(r), isMaintainer); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "withdrawn"})
}

// handleListStrategicThemeProposals answers a project's theme proposals, newest first, so the
// settings page can show what is pending and how many consents it has.
func (s *Server) handleListStrategicThemeProposals(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	proposals, err := s.Store.ListStrategicThemeProposals(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, proposals)
}

// handleWithdrawStrategicWeightProposal exposes the withdrawal the store has had all along.
//
// WithdrawStrategicWeightProposal has existed since the weight proposals did, with no caller
// anywhere: a proposer who changed their mind had no way to cancel, and the only exit from a
// pending proposal was for it to be consented past quorum or to sit in the list forever. The
// function was written and unreachable, which is the defect class this whole pass is about.
func (s *Server) handleWithdrawStrategicWeightProposal(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireWriteActor(w, r); !ok {
		return
	}
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	proposalID, err := strconv.ParseInt(chi.URLParam(r, "proposal_id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return
	}
	role, _ := s.Store.GetRoleForProject(r.Context(), projectID, getActorID(r))
	isMaintainer := role == "maintainer" || role == "owner"
	if err := s.Store.WithdrawStrategicWeightProposal(r.Context(), proposalID, getActorID(r), isMaintainer); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "withdrawn"})
}
