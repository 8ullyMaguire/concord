package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// The inbox (docs/specs/notification-spec.md §4.3–.4).
//
// Every route here is SELF-SCOPED: the reader comes from the request's session and there is no
// route that takes a user id, so there is no route to get wrong. A stranger's notification id
// answers 404 exactly like a missing one — a 403 would confirm the id is real, which is a fact
// about another person's inbox.

// handleListNotifications answers the signed-in reader's inbox and their unread count.
func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.requireWriteActor(w, r)
	if !ok {
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	list, unread, err := s.Store.ListNotifications(r.Context(), uid, limit)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": list, "unread": unread})
}

// handleMarkNotificationRead marks one of the reader's own notifications read.
//
// Idempotent: marking an already-read notification is a 200 with `read: true`, not an error.
// A control that fails when clicked twice is a control a reader learns to distrust.
func (s *Server) handleMarkNotificationRead(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.requireWriteActor(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		mapError(w, store.ErrNotFound)
		return
	}
	if err := s.Store.MarkNotificationRead(r.Context(), uid, id); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "read": true})
}

// handleMarkAllNotificationsRead clears the reader's badge and says how many it cleared.
func (s *Server) handleMarkAllNotificationsRead(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.requireWriteActor(w, r)
	if !ok {
		return
	}
	n, err := s.Store.MarkAllNotificationsRead(r.Context(), uid)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"marked": n})
}

// handleNotificationsPage renders /notifications: a shell that fetches the inbox.
//
// The shell is public and says "sign in" when nobody is; the API behind it answers 401, so the
// page cannot become a way to read somebody's inbox by asking nicely.
func (s *Server) handleNotificationsPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "notifications", struct {
		pageData
		SignedIn bool
	}{s.pageFor(r, "Notifications"), actorID(r) != 0})
}
