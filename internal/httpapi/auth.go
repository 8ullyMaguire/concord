package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// actorKey is the context key for the authenticated user id.
//
// Deliberately a private typed key rather than the string "actor_id" that
// getActorID used to read. Two reasons, both of which have bitten this codebase
// already:
//
//   - A string key can collide with any other package that picks the same
//     name. A typed unexported key cannot be constructed outside this package,
//     so nothing can overwrite it by accident.
//   - The old string key was read by 20-odd handlers and set by nothing, so
//     every one of them silently saw actor 0 and returned 401. With a typed
//     key, "set somewhere else" stops compiling.
type actorKey struct{}

// bearerToken extracts a token from the Authorization header, falling back to
// an X-Auth-Token header for browser clients that cannot set headers on a
// navigation. Returns "" when absent.
func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		// "Bearer clt_..." — case-insensitive scheme per RFC 7235.
		if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
			return strings.TrimSpace(h[7:])
		}
		return ""
	}
	return strings.TrimSpace(r.Header.Get("X-Auth-Token"))
}

// authenticate resolves a bearer token to a user id and puts it on the request
// context.
//
// Two properties that are easy to get wrong and are load-bearing here:
//
//  1. A missing token is NOT an error. The request continues with no actor, so
//     public reads keep working and writes still fail closed at their own
//     getActorID checks. This is what makes "anonymous" the default rather than
//     something every public handler has to special-case.
//  2. A present but invalid token IS an error. Silently downgrading a bad
//     token to anonymous would let a client with an expired credential believe
//     it is still working, and would turn a typo into a confusing 401 on read
//     instead of an honest one.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearerToken(r)
		if tok == "" {
			next.ServeHTTP(w, r)
			return
		}
		uid, err := s.Store.ResolveToken(r.Context(), tok)
		if err != nil {
			mapError(w, store.ErrAuth)
			return
		}
		ctx := context.WithValue(r.Context(), actorKey{}, uid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// actorID returns the authenticated user id, or 0 when anonymous.
func actorID(r *http.Request) int64 {
	if a, ok := r.Context().Value(actorKey{}).(int64); ok {
		return a
	}
	return 0
}

// requireProjectID resolves the {project_id} route parameter to a numeric
// project id, writing the error response itself and returning false on failure.
//
// The parameter is named project_id but is filled with a *slug*: the sibling
// route is /api/v1/projects/{slug}, the existing tests pass values like
// "governance-lab", and GetProject looks up WHERE p.slug = ?. Twenty-one
// handlers were written as
//
//	projectID, _ := strconv.ParseInt(chi.URLParam(r, "project_id"), 10, 64)
//
// which parses a slug to 0, discards the error, and then queries project 0. The
// tests did not catch it because they assert only that the status is 200, and
// "no rows for project 0" is still 200.
//
// Discarding a parse error and continuing with the zero value is the defect
// itself: it turns a bad request into a well-formed query against the wrong
// row. This helper refuses instead.
// requireWriteActor rejects an anonymous caller before the project is resolved.
//
// Authentication is checked first, deliberately, on every write path. The
// reverse order would make the response code depend on whether a project
// exists: an anonymous PUT to a real project and to a nonexistent one would
// return 403 and 404 respectively, which is enough to enumerate the slugs
// registered on the site. Checking the actor first makes both return 401, so
// the only thing an anonymous caller can learn is that they are anonymous.
func (s *Server) requireWriteActor(w http.ResponseWriter, r *http.Request) (int64, bool) {
	uid := getActorID(r)
	if uid == 0 {
		mapError(w, store.ErrAuth)
		return 0, false
	}
	return uid, true
}

// requireProjectID resolves {project_id} to a numeric id AND enforces the
// project's visibility. Every project-scoped API route funnels through here, so
// the check sits here rather than in each handler: a handler that forgot it
// would serve a private project's complaints, votes or documents to anyone.
//
// A visibility refusal is reported as ErrNotFound, never ErrForbidden. A 403
// would confirm the slug exists, and an instance whose private projects answer
// 403 is enumerable -- the same leak requireWriteActor above refuses to create
// for anonymous callers. The audit row records what actually happened, so the
// attempt is still visible to an operator.
//
// The BODY is the same too, and that part was missing until the panels'
// anti-enumeration test caught it. mapError writes err.Error(), so the two cases
// answered:
//
//	missing:  {"error": "not found: project \"governance-lab\""}
//	forbidden:{"error": "not found"}
//
// Identical status codes, different bodies -- which is an existence oracle in
// exactly the way the 403 would have been, and one that a status-code test
// cannot see. Both branches now answer a bare `not found`, with no slug in it.
// Fixing it here rather than in mapError: mapError's per-sentinel detail is
// useful on the routes where the caller is already known to be allowed to ask,
// and stripping it globally would lose real debugging information (which id,
// which constraint) for every error in the API. The single line is deliberate.
//
// It is NOT the wrapped sentinel -- store.ErrNotFound has no slug in it, and
// GetProject's wrap does. Writing the literal here is what keeps the two
// answers identical.
func (s *Server) requireProjectID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	slug := chi.URLParam(r, "project_id")
	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		// An unreadable project and a nonexistent one must be indistinguishable,
		// so this does not distinguish "no rows" from any other store failure
		// either: a 500 names nothing about whether the slug exists.
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return 0, false
		}
		mapError(w, err)
		return 0, false
	}
	if !s.projectReadable(r, proj) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return 0, false
	}
	return proj.ID, true
}

// projectReadable is the visibility decision for a loaded project. Split out so
// the handlers that resolve a project by slug themselves -- rather than through
// requireProjectID -- apply exactly the same rule.
//
// unlisted is readable by anyone, including an anonymous caller: the URL is the
// capability. Only private and protected require a session.
// isProjectMember reports whether a role string from GetRoleForProject counts as
// membership.
//
// This check is not optional. GetRoleForProject returns the literal string
// "guest" for a user with no members row -- not "", and not an error. A gate
// written as `role == ""` therefore never fires, which silently grants every
// caller the member-only actions: any signed-in account could set a project's
// visibility or revoke its invites. Caught by testing against a real stranger
// account rather than the harness default identity.
func (s *Server) isProjectMember(role string) bool {
	switch role {
	case "", "guest":
		return false
	default:
		return true
	}
}

func (s *Server) projectReadable(r *http.Request, proj store.Project) bool {
	ok, err := s.Store.CanAccessProject(r.Context(), proj, getActorID(r))
	if err != nil {
		// An unknown visibility value must fail closed. Logging is deliberately
		// absent here: this runs before writeJSON and an error path that panics
		// or double-writes is worse than a 404.
		return false
	}
	return ok
}
