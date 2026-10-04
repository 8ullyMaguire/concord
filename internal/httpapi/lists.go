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
	// requireProjectID checks the caller's access to the project in the path. It
	// was missing, so a list belonging to a private project was readable by anyone
	// who counted to its id.
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return
	}
	l, err := s.Store.GetList(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return
	}
	// And the list must belong to THAT project, or one project's list is served
	// under another's URL.
	if l.ProjectID != projectID {
		mapError(w, store.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// handleGetListEntries returns entries for a list.
func (s *Server) handleGetListEntries(w http.ResponseWriter, r *http.Request) {
	// Entries carry the list's own visibility, so the LIST is checked rather than
	// the project: a protected list inside a public project must not become
	// readable just because its parent is.
	list, ok := s.requireReadableList(w, r)
	if !ok {
		return
	}
	listID := list.ID
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
	// The route is /api/v1/requests/{request_id} -- there is NO project segment --
	// so the project has to be reached THROUGH the request row. Checking a path
	// parameter that does not exist is how this endpoint stayed unguarded: it does
	// not look like a project-scoped route, so the project-scoped guard was never
	// applied to it.
	request, ok := s.requireReadableRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, request)
}

// handleGetRequestAnswers returns answers for a request.
func (s *Server) handleGetRequestAnswers(w http.ResponseWriter, r *http.Request) {
	// The answers belong to the request, so the request is what gets checked --
	// /api/v1/requests/{request_id}/answers carries no project segment of its own.
	request, ok := s.requireReadableRequest(w, r)
	if !ok {
		return
	}
	answers, err := s.Store.GetRequestAnswers(r.Context(), request.ID)
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
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
	var req struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		// As on complaints and features: proceed past a near-identical request.
		ConfirmDuplicate bool `json:"confirm_duplicate"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	// A request is a "what fits my need?" question, so a near-duplicate is
	// especially costly: it fragments the answers instead of pooling them.
	_, proceed := s.checkDuplicates(w, r, store.KindRequest,
		req.Title, req.Body, projectID, req.ConfirmDuplicate)
	if !proceed {
		return
	}
	request, err := s.Store.CreateRequest(r.Context(), projectID, getActorID(r), req.Title, req.Body)
	if err != nil {
		mapError(w, err)
		return
	}
	if err := s.Store.UpsertEntityText(r.Context(), store.KindRequest, request.ID, projectID,
		req.Title, req.Body); err != nil {
		_ = s.Store.AddAudit(r.Context(), projectID, getActorID(r), "embed_failed", "request", request.ID, err.Error())
	}
	_ = s.Store.AddAudit(r.Context(), projectID, getActorID(r), "create_request", "request", request.ID, req.Title)
	writeJSON(w, http.StatusCreated, request)
}

// handleListRequests returns requests for a project.
func (s *Server) handleListRequests(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
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
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
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
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}
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


// requireReadableList loads the list named in {id} and confirms the caller may
// read it, answering the request itself when they may not.
//
// Entries are read through this rather than by bare id: a list lives inside a
// project, so checking the project is what actually decides the answer, and a
// numeric id on its own carries no access information at all.
func (s *Server) requireReadableList(w http.ResponseWriter, r *http.Request) (store.List, bool) {
	listID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return store.List{}, false
	}
	l, err := s.Store.GetList(r.Context(), listID)
	if err != nil {
		mapError(w, err)
		return store.List{}, false
	}
	proj, err := s.Store.GetProjectByID(r.Context(), l.ProjectID)
	if err != nil {
		mapError(w, store.ErrNotFound)
		return store.List{}, false
	}
	if !s.projectReadable(r, proj) {
		// 404, not 403: a 403 would confirm the id exists.
		mapError(w, store.ErrNotFound)
		return store.List{}, false
	}
	return l, true
}

// requireReadableRequest loads the request named in {request_id} and confirms the
// caller may read it.
//
// This exists because the route is /api/v1/requests/{request_id}: there is no
// project in the path to run requireProjectID against. Reaching the project through
// the row is the only way to apply the same access rule, and it is what these two
// handlers were missing.
func (s *Server) requireReadableRequest(w http.ResponseWriter, r *http.Request) (store.Request, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "request_id"), 10, 64)
	if err != nil {
		mapError(w, store.ErrInvalid)
		return store.Request{}, false
	}
	req, err := s.Store.GetRequest(r.Context(), id)
	if err != nil {
		mapError(w, err)
		return store.Request{}, false
	}
	proj, err := s.Store.GetProjectByID(r.Context(), req.ProjectID)
	if err != nil {
		mapError(w, store.ErrNotFound)
		return store.Request{}, false
	}
	if !s.projectReadable(r, proj) {
		mapError(w, store.ErrNotFound)
		return store.Request{}, false
	}
	return req, true
}
