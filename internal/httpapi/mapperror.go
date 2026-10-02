package httpapi

import (
	"errors"
	"net/http"
	"slices"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// mapError turns a store or handler error into a status code.
//
// The sentinel cases come first and are unchanged. The string cases below exist
// because handlers predate the sentinels and some still hand-construct errors for
// conditions that already have one: nine handlers pass
// fmt.Errorf("authentication required") straight to this function, which matches
// no sentinel, so an unauthenticated request got 500 instead of 401.
//
// Matching by message is a concession, not a design. Fixing it in the mapper
// rather than at fifteen call sites means a handler that forgets to wrap a
// sentinel still answers correctly, and a 500 on a missing token stops reading
// as a server fault when it is plainly a client's mistake.
func mapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, store.ErrDuplicate), errors.Is(err, store.ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, store.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, store.ErrAuth):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
	case errors.Is(err, store.ErrPerm):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
	case isHandBuiltAuthError(err):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
	case isHandBuiltValidationError(err):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

// handBuiltAuth are the messages handlers pass instead of wrapping store.ErrAuth.
//
// Every entry is a full message, checked exactly, because a prefix would be
// worse than useless: "unauthorized" is a prefix of "unauthorized to edit", which
// is a permission failure, and matching it would answer 401 to a caller who is
// already signed in and send them back to the login page. Exact match keeps the
// hand-built cases narrow -- only the three literals that actually appear in
// handlers -- rather than guessing at near-misses.
var handBuiltAuth = []string{
	"authentication required",
	"invalid credentials",
	"token required",
}

// handBuiltValidation are validation messages that should be 400, not 500.
//
// "webhook not configured" is deliberately absent: an unconfigured hook means the
// deployment is broken, not that the request was wrong, so 500 stays correct
// there. A rejected signature is the client's mistake and belongs in 400.
var handBuiltValidation = []string{
	"direction must be 1 or -1",
	"invalid signature",
}

func isHandBuiltAuthError(err error) bool {
	return matchesAnyExact(err, handBuiltAuth)
}

func isHandBuiltValidationError(err error) bool {
	return matchesAnyExact(err, handBuiltValidation)
}

// matchesAnyExact reports whether err's message is exactly one of the given
// messages.
//
// Exact, not prefix and not substring. Prefix looks tidier and is wrong here:
// handlers append context to some of these, but a prefix rule would also swallow
// the permission phrasings that happen to start the same way, and quietly turn a
// 403 into a 401. A message that needs a sentinel should have one.
func matchesAnyExact(err error, messages []string) bool {
	return slices.Contains(messages, err.Error())
}
