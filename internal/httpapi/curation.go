package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"git.polarisocial.xyz/concord/concord/internal/config"
	"git.polarisocial.xyz/concord/concord/internal/store"
	"github.com/go-chi/chi/v5"
)

// handleCurationPage renders the review queue. It reads the instance-wide API and needs no
// slug: a project's queue is a filter on the same list, not a different one.
func (s *Server) handleCurationPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "curation", s.pageFor(r, "Curation"))
}

// handleListCuration serves GET /api/v1/curation.
//
// Every parameter is optional and a malformed one narrows the list rather than failing the
// request, matching handleListProjectAudit: this is a surface a person types into. The one
// exception is the cursor -- a cursor that cannot be parsed is a paging bug, and answering
// with the first page would turn it into an infinite scroll.
func (s *Server) handleListCuration(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	viewer := getActorID(r)
	f := store.CurationFilter{
		Kind:     strings.TrimSpace(q.Get("kind")),
		State:    strings.TrimSpace(q.Get("state")),
		Text:     strings.TrimSpace(q.Get("q")),
		Cursor:   q.Get("cursor"),
		ViewerID: viewer,
	}
	if slug := q.Get("project"); slug != "" {
		if p, err := s.Store.GetProject(r.Context(), slug); err == nil {
			f.ProjectID = p.ID
		}
	}
	if n, err := strconv.ParseInt(q.Get("actor_id"), 10, 64); err == nil {
		f.ActorID = n
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil {
		f.Limit = n
	}
	if n, err := strconv.ParseFloat(q.Get("since"), 64); err == nil {
		f.Since = n
	}
	if n, err := strconv.ParseFloat(q.Get("until"), 64); err == nil {
		f.Until = n
	}
	// "Only what I have not voted on" is a queue default, and it needs a reader to mean
	// anything: an anonymous caller asking for it gets the unfiltered list rather than an
	// empty one that looks like good news.
	if q.Get("unvoted") == "1" && viewer != 0 {
		f.UnvotedBy = viewer
	}

	page, err := s.Store.ListCuration(r.Context(), f)
	if err != nil {
		mapError(w, err)
		return
	}
	s.fillCurationVoteRights(r, page.Items)
	writeJSON(w, http.StatusOK, page)
}

// handleCurationVote serves POST /api/v1/curation/{kind}/{id}/vote.
//
// Consent to a taxonomy or weight proposal is decided by that proposal's own rules, so a
// recorded vote that has not reached quorum answers 202 — the vote landed, the change has not.
// A review of a moderation act changes nothing but its count, and answers 200.
func (s *Server) handleCurationVote(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the id must be a number"})
		return
	}
	actorID := getActorID(r)
	if actorID == 0 {
		mapError(w, store.ErrAuth)
		return
	}

	var req struct {
		Vote      string `json:"vote"`
		Rationale string `json:"rationale"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var vote int
	switch strings.ToLower(strings.TrimSpace(req.Vote)) {
	case "yes", "consent", "endorse", "1":
		vote = 1
	case "no", "contest", "-1":
		vote = -1
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "vote must be yes or no"})
		return
	}

	// Reviewing an act is reviewer work: it writes a judgement about another person's
	// decision, so it takes the same standing that decision took.
	if kind == "act" {
		_, projectID, _, err := s.Store.ActTarget(r.Context(), id)
		if err != nil {
			mapError(w, err)
			return
		}
		if !s.permitModeration(w, r, projectID, "reviewer") {
			return
		}
	}

	yes, no, err := s.Store.VoteOnCuration(r.Context(), kind, id, actorID, vote, req.Rationale)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeJSON(w, http.StatusAccepted, map[string]any{
				"recorded": true, "decided": false, "yes": yes, "no": no,
			})
			return
		}
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recorded": true, "decided": true, "yes": yes, "no": no,
	})
}

// fillCurationVoteRights marks which rows this caller may actually vote on.
//
// The rules are the deciders' own, restated as the cheapest sufficient test: a taxonomy change
// is consented to by a contributor of its project (instance-wide proposals by a contributor
// somewhere), a weight proposal by an eligible collaborator, and an act is reviewed by a
// reviewer -- trust level or project role, the same pair permitModeration allows.
//
// It is a HINT for the page, not a gate. Every route re-checks, because a flag computed in a
// read is not a permission.
func (s *Server) fillCurationVoteRights(r *http.Request, items []store.CurationItem) {
	actorID := getActorID(r)
	if actorID == 0 {
		return
	}
	trusted := false
	if lvl, err := s.Store.GetTrustLevel(r.Context(), actorID); err == nil && lvl >= config.ModTrustMin() {
		trusted = true
	}
	roles := map[int64]string{}
	roleFor := func(projectID int64) string {
		if v, ok := roles[projectID]; ok {
			return v
		}
		v, _ := s.Store.GetRoleForProject(r.Context(), projectID, actorID)
		roles[projectID] = v
		return v
	}
	highest, highestErr := s.Store.GetHighestRole(r.Context(), actorID)

	for i := range items {
		it := &items[i]
		if it.ActorID == actorID {
			// §5.1: nobody decides on their own item.
			continue
		}
		switch it.Kind {
		case "taxonomy":
			if it.ProjectID == 0 {
				it.CanVote = highestErr == nil && roleAtLeast(highest, "contributor")
			} else {
				it.CanVote = roleAtLeast(roleFor(it.ProjectID), "contributor")
			}
		case "strategy":
			ok, err := s.Store.IsEligibleCollaborator(r.Context(), it.ProjectID, actorID)
			it.CanVote = err == nil && ok
		case "act":
			it.CanVote = trusted || roleAtLeast(roleFor(it.ProjectID), "reviewer")
		}
	}
}

// roleAtLeast answers whether a role outranks or matches another, using the one hierarchy the
// router's gates already use.
func roleAtLeast(role, min string) bool {
	rank, ok := roleHierarchy[role]
	if !ok {
		return false
	}
	need, ok := roleHierarchy[min]
	if !ok {
		need = 2
	}
	return rank >= need
}
