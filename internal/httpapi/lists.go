package httpapi

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// handleGetList returns a single list.
func (s *Server) handleGetList(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	l, err := s.Store.GetList(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// handleGetListEntries returns entries for a list.
func (s *Server) handleGetListEntries(w http.ResponseWriter, r *http.Request) {
	listID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	entries, err := s.Store.GetListEntries(r.Context(), listID)
	if err != nil {
		mapError(w, err)
		return
	}
	if entries == nil {
		entries = []store.ListEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleCreateListEntry adds an entry to a list.
func (s *Server) handleCreateListEntry(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	listID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var req struct {
		URL         string `json:"url"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	entry, err := s.Store.CreateListEntry(r.Context(), listID, req.URL, req.Title, req.Description)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), 0, getActorID(r), "create_list_entry", "list_entry", entry.ID, req.Title)
	writeJSON(w, http.StatusCreated, entry)
}

// handleGetRequest returns a single request.
func (s *Server) handleGetRequest(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "request_id"), 10, 64)
	request, err := s.Store.GetRequest(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, request)
}

// handleGetRequestAnswers returns answers for a request.
func (s *Server) handleGetRequestAnswers(w http.ResponseWriter, r *http.Request) {
	requestID, _ := strconv.ParseInt(chi.URLParam(r, "request_id"), 10, 64)
	answers, err := s.Store.GetRequestAnswers(r.Context(), requestID)
	if err != nil {
		mapError(w, err)
		return
	}
	if answers == nil {
		answers = []store.RequestAnswer{}
	}
	writeJSON(w, http.StatusOK, answers)
}

// handleCreateRequest creates a new request (with auth).
func (s *Server) handleCreateRequest(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	var req struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	request, err := s.Store.CreateRequest(r.Context(), projectID, getActorID(r), req.Title, req.Body)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), projectID, getActorID(r), "create_request", "request", request.ID, req.Title)
	writeJSON(w, http.StatusCreated, request)
}

// handleListRequests returns requests for a project.
func (s *Server) handleListRequests(w http.ResponseWriter, r *http.Request) {
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	requests, err := s.Store.GetRequestsByProject(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	if requests == nil {
		requests = []store.Request{}
	}
	writeJSON(w, http.StatusOK, requests)
}

// handleListLists returns lists for a project.
func (s *Server) handleListLists(w http.ResponseWriter, r *http.Request) {
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	lists, err := s.Store.GetListsByProject(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	if lists == nil {
		lists = []store.List{}
	}
	writeJSON(w, http.StatusOK, lists)
}

// handleCreateList creates a new list (with auth).
func (s *Server) handleCreateList(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
	var req struct {
		Slug        string `json:"slug"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	l, err := s.Store.CreateList(r.Context(), projectID, req.Slug, req.Title, req.Description)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), projectID, getActorID(r), "create_list", "list", l.ID, req.Slug)
	writeJSON(w, http.StatusCreated, l)
}

// handleAnswerRequest adds an answer to a request (with auth).
func (s *Server) handleAnswerRequest(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	requestID, _ := strconv.ParseInt(chi.URLParam(r, "request_id"), 10, 64)
	var req struct {
		Body string `json:"body"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	answer, err := s.Store.AnswerRequest(r.Context(), requestID, getActorID(r), req.Body)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), 0, getActorID(r), "answer_request", "request_answer", answer.ID, fmt.Sprintf("request=%d", requestID))
	writeJSON(w, http.StatusCreated, answer)
}

// handleVoteAnswer votes on a request answer (with auth).
func (s *Server) handleVoteAnswer(w http.ResponseWriter, r *http.Request) {
	if getActorID(r) == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	answerID, _ := strconv.ParseInt(chi.URLParam(r, "answer_id"), 10, 64)
	var req struct {
		Direction int `json:"direction"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Direction != 1 && req.Direction != -1 {
		mapError(w, fmt.Errorf("direction must be 1 or -1"))
		return
	}
	if err := s.Store.VoteAnswer(r.Context(), answerID, getActorID(r), req.Direction); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "voted"})
}
