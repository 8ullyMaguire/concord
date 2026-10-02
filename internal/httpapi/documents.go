package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// Project documents (spec extension, 2026-09-30).
//
// Handlers for the document store: a project's README, specification, plan and
// wiki. These are the documents that explain a project, and until now they lived
// only in a repository somewhere else.
//
// Two deliberate differences from the rest of the API:
//
//   - Writing requires the contributor role, not just authentication. A document
//     states what a project *is*, so it is not something any signed-in account
//     gets to rewrite.
//   - The body limit is raised. readJSON caps at 1 MB, which is right for a
//     feature body and wrong for a 3,982-line specification; the limit here is
//     8 MB. Both are still bounds -- an unbounded read is how a single POST
//     takes a service down.

const maxDocumentBody = 8 << 20 // 8 MB

type putDocumentRequest struct {
	Kind  string `json:"kind"`
	Slug  string `json:"slug"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// readDocumentJSON decodes a document body with a larger limit than readJSON.
// The size check happens after decoding so the caller gets a real message
// rather than a bare "request body too large".
func readDocumentJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxDocumentBody)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": "invalid JSON body: " + err.Error()})
		return false
	}
	return true
}

// handlePutDocument creates or replaces a document at (kind, slug).
func (s *Server) handlePutDocument(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	project, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		mapError(w, err)
		return
	}
	// Visibility, before anything is read or written. A private projects
	// specs and plans are exactly the content that must not leak, and these
	// handlers resolve the project by slug rather than through
	// requireProjectID, so they need the check explicitly.
	if !s.projectReadable(r, project) {
		mapError(w, store.ErrNotFound)
		return
	}
	if err := s.requireRole(project.ID, "contributor", r); err != nil {
		mapError(w, err)
		return
	}

	var req putDocumentRequest
	if !readDocumentJSON(w, r, &req) {
		return
	}
	actorID := getActorID(r)
	doc, err := s.Store.PutDocument(r.Context(), project.ID, actorID,
		req.Kind, req.Slug, req.Title, req.Body)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

// handleListDocuments returns every document in a project, or one kind if
// ?kind= is given.
func (s *Server) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	project, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		mapError(w, err)
		return
	}
	// Visibility, before anything is read or written. A private projects
	// specs and plans are exactly the content that must not leak, and these
	// handlers resolve the project by slug rather than through
	// requireProjectID, so they need the check explicitly.
	if !s.projectReadable(r, project) {
		mapError(w, store.ErrNotFound)
		return
	}
	docs, err := s.Store.ListDocuments(r.Context(), project.ID, r.URL.Query().Get("kind"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, docs)
}

// handleGetDocument returns one document by id.
func (s *Server) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	project, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		mapError(w, err)
		return
	}
	// Visibility, before anything is read or written. A private projects
	// specs and plans are exactly the content that must not leak, and these
	// handlers resolve the project by slug rather than through
	// requireProjectID, so they need the check explicitly.
	if !s.projectReadable(r, project) {
		mapError(w, store.ErrNotFound)
		return
	}
	docID, err := strconv.ParseInt(chi.URLParam(r, "doc_id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "document id must be an integer"})
		return
	}
	doc, err := s.Store.GetDocument(r.Context(), project.ID, docID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

// handleDeleteDocument removes a document. Requires the maintainer role:
// deleting a spec is not something a contributor can do.
func (s *Server) handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	project, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		mapError(w, err)
		return
	}
	// Visibility, before anything is read or written. A private projects
	// specs and plans are exactly the content that must not leak, and these
	// handlers resolve the project by slug rather than through
	// requireProjectID, so they need the check explicitly.
	if !s.projectReadable(r, project) {
		mapError(w, store.ErrNotFound)
		return
	}
	if err := s.requireRole(project.ID, "maintainer", r); err != nil {
		mapError(w, err)
		return
	}
	docID, err := strconv.ParseInt(chi.URLParam(r, "doc_id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "document id must be an integer"})
		return
	}
	if err := s.Store.DeleteDocument(r.Context(), project.ID, docID); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleSearchDocuments runs an FTS5 query over a project's documents.
//
// ?q= is required and an absent or empty query is a 400, not an empty result.
// A search endpoint that returns the whole corpus when the box is empty is a
// way to dump a project's private documents onto a page.
func (s *Server) handleSearchDocuments(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	project, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		mapError(w, err)
		return
	}
	// Visibility, before anything is read or written. A private projects
	// specs and plans are exactly the content that must not leak, and these
	// handlers resolve the project by slug rather than through
	// requireProjectID, so they need the check explicitly.
	if !s.projectReadable(r, project) {
		mapError(w, store.ErrNotFound)
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": "q is required"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	docs, err := s.Store.SearchDocuments(r.Context(), project.ID, q, limit)
	if err != nil {
		// A malformed FTS5 expression is the caller's fault, not the server's.
		// The store classifies it; this layer only maps it to a status.
		if errors.Is(err, store.ErrInvalidQuery) {
			writeJSON(w, http.StatusBadRequest,
				map[string]string{"error": err.Error()})
			return
		}
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, docs)
}

// documentKindsResponse is the accepted kind vocabulary, so a client does not
// have to hardcode it or guess from a 400.
func (s *Server) handleDocumentKinds(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"kinds": store.DocumentKinds})
}
