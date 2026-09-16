package httpapi

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// handleCreateComment creates a comment on a thread.
func (s *Server) handleCreateComment(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	threadID, _ := strconv.ParseInt(chi.URLParam(r, "thread_id"), 10, 64)
	threadKind := chi.URLParam(r, "thread_kind")

	var req struct {
		Body     string `json:"body"`
		Label    string `json:"label"`
		ParentID *int64 `json:"parent_id,omitempty"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	c, err := s.Store.CreateComment(r.Context(), projectID, threadID, threadKind, getActorID(r), req.Body, req.Label, req.ParentID)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), projectID, getActorID(r), "create_comment", "comment", c.ID, fmt.Sprintf("thread=%s:%d", threadKind, threadID))
	writeJSON(w, http.StatusCreated, c)
}

// handleGetThreadComments retrieves comments for a thread.
func (s *Server) handleGetThreadComments(w http.ResponseWriter, r *http.Request) {
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	threadID, _ := strconv.ParseInt(chi.URLParam(r, "thread_id"), 10, 64)
	threadKind := chi.URLParam(r, "thread_kind")
	comments, err := s.Store.GetThreadComments(r.Context(), projectID, threadID, threadKind)
	if err != nil {
		mapError(w, err)
		return
	}
	if comments == nil {
		comments = []store.Comment{}
	}
	writeJSON(w, http.StatusOK, comments)
}

// handleDeleteComment soft-deletes a comment (author or maintainer).
func (s *Server) handleDeleteComment(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	commentID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	c, err := s.Store.GetComment(r.Context(), commentID)
	if err != nil {
		mapError(w, err)
		return
	}
	if c.AuthorID != getActorID(r) {
		projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
		role, _ := s.Store.GetRoleForProject(r.Context(), projectID, getActorID(r))
		if role != "maintainer" && role != "owner" {
			mapError(w, store.ErrPerm)
			return
		}
	}
	if err := s.Store.DeleteComment(r.Context(), commentID); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleVoteComment adds a vote to a comment.
func (s *Server) handleVoteComment(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	commentID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var req struct {
		Value int `json:"value"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.Store.VoteComment(r.Context(), commentID, getActorID(r), req.Value); err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.UpdateCommentScore(r.Context(), commentID)
	writeJSON(w, http.StatusCreated, map[string]string{"status": "voted"})
}
