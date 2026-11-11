package httpapi

import (
	"net/http"
	"strings"

	"git.polarisocial.xyz/concord/concord/internal/config"
	"git.polarisocial.xyz/concord/concord/internal/ledger"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// arenaActRequest is the body of both moderation routes.
type arenaActRequest struct {
	FeatureID int64  `json:"feature_id"`
	ArenaID   int64  `json:"arena_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
	Undoes    int64  `json:"undoes"`
}

// permitModeration answers whether this reader may moderate this project.
//
// Two paths, because the owner asked for "a high-trust user or an admin" and this repo
// already has both: an instance-wide trust level (users.trust_level, gated by
// CONCORD_MOD_TRUST_MIN) and a per-project role. Either is enough. A refusal says which
// role it wanted, and requireRole's error carries the 403 through mapError.
func (s *Server) permitModeration(w http.ResponseWriter, r *http.Request, projectID int64, minRole string) bool {
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return false
	}
	if lvl, err := s.Store.GetTrustLevel(r.Context(), actorID); err == nil && lvl >= config.ModTrustMin() {
		return true
	}
	if err := s.requireRole(projectID, minRole, r); err != nil {
		mapError(w, err)
		return false
	}
	return true
}

// requiredReason trims and validates, answering 400 with the crate's sentence.
func requiredReason(w http.ResponseWriter, reason string) (string, bool) {
	reason = strings.TrimSpace(reason)
	if ledger.TrimmedReason(reason) == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": ledger.ReasonRequiredDefect.Error()})
		return "", false
	}
	return reason, true
}

// handleArenaRemove implements POST /api/v1/projects/{slug}/moderation/arena/remove.
//
// The act deletes the entry row, rejects the feature and records both prior values, in one
// transaction. It never deletes the feature: see docs/specs/moderation-spec.md D1.
func (s *Server) handleArenaRemove(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	if !s.permitModeration(w, r, projectID, "reviewer") {
		return
	}
	var req arenaActRequest
	if !readJSON(w, r, &req) {
		return
	}
	if !req.Confirm {
		// The browser confirms in the page too; this is for the script that does not.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this act needs confirmation"})
		return
	}
	if req.FeatureID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "feature_id is required"})
		return
	}
	// The page names the idea, not the arena: an idea's arena is a property of the ranking.
	// One arena is inferred; several must be named, because guessing which ranking to take
	// an idea out of is exactly the kind of silent choice this feature must not make.
	if req.ArenaID == 0 {
		arenas, err := s.Store.ArenasForFeature(r.Context(), req.FeatureID)
		if err != nil {
			mapError(w, err)
			return
		}
		switch len(arenas) {
		case 0:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "this idea is not in an arena"})
			return
		case 1:
			req.ArenaID = arenas[0]
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "this idea is in more than one arena; name the arena_id",
			})
			return
		}
	}
	reason, ok := requiredReason(w, req.Reason)
	if !ok {
		return
	}
	act, err := s.Store.RemoveFeatureFromArena(r.Context(), projectID, req.ArenaID, req.FeatureID, getActorID(r), reason)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"act":  act,
		"undo": map[string]any{"action": "arena.restore", "target": act.ID},
	})
}

// handleArenaRestore implements POST /api/v1/projects/{slug}/moderation/arena/restore, which
// is what the undo control posts to. It takes the id of the act it reverses, not the
// feature's prior values: those are read from the log (store.RestoreFeatureToArena).
func (s *Server) handleArenaRestore(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	if !s.permitModeration(w, r, projectID, "reviewer") {
		return
	}
	var req arenaActRequest
	if !readJSON(w, r, &req) {
		return
	}
	if !req.Confirm {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this act needs confirmation"})
		return
	}
	if req.Undoes == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "undoes is required"})
		return
	}
	reason, ok := requiredReason(w, req.Reason)
	if !ok {
		return
	}
	act, err := s.Store.RestoreFeatureToArena(r.Context(), projectID, getActorID(r), req.Undoes, reason)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"act": act})
}
