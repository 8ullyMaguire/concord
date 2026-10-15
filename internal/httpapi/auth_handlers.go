package httpapi

import (
	"net/http"
	"strings"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// JSON API for authentication.
//
// The design question this file answers is what an anonymous visitor can do.
// The answer is deliberately: read everything, write nothing. Reads never
// require a token, so the site is browsable and the ranking is public; every
// write already fails closed on getActorID(r) == 0, and now that check has
// something to succeed against.

type registerRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// authResponse returns the token exactly once. There is no endpoint that can
// return it again — only its SHA-256 is stored — so a client that loses it must
// log in again rather than asking.
type authResponse struct {
	User   store.User `json:"user"`
	Token  string     `json:"token"`
	Expiry string     `json:"token_note"`
}

const tokenNote = "Store this token. It cannot be retrieved again; only its hash is kept."

// navCookieName is the cookie a browser NAVIGATION can carry.
//
// The problem it solves, measured rather than assumed: the session is a bearer token
// held in localStorage and attached by script, so the HTML for a page arrives
// anonymous. Every page behind `projectReadable` then 404s for a member of a
// protected or private project -- their own project's settings page included -- while
// the API reads they are entitled to (which the page's script makes, token in hand)
// work fine. The failing request is the NAVIGATION, and only a cookie can carry a
// credential on one.
//
// WHY THIS IS NOT A CSRF HOLE, stated as the property the tests assert: the cookie is
// accepted ONLY on a GET whose path is not under /api/. Every mutating route in this
// server is an API route, so the cookie structurally cannot authorize a write, and an
// /api/ read never sees it either. The bearer header remains the only credential for
// the API. SameSite=Lax keeps it off cross-site subrequests as well.
//
// It is deliberately a SESSION cookie (no Max-Age): the localStorage token lives until
// it is revoked, and the navigation convenience should not outlive the browser session
// that created it.
const navCookieName = "concord_nav"

// setNavCookie hands the browser a credential its navigations can present.
func setNavCookie(w http.ResponseWriter, r *http.Request, tok string) {
	http.SetCookie(w, &http.Cookie{
		Name:     navCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true, // no script reads this; the header path is the script's
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsHTTPS(r),
	})
}

// clearNavCookie expires it. Called on logout beside the token revocation: a cookie
// left behind would keep rendering member pages for a credential that no longer
// resolves, which is a logout that looks like it did not happen.
func clearNavCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     navCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsHTTPS(r),
		MaxAge:   -1,
	})
}

// requestIsHTTPS reports whether the browser reached us over TLS. The deployed
// instance listens on loopback behind a proxy, so the proxy's header is the only
// evidence on that hop; a Secure cookie set over plain HTTP would be dropped by the
// browser, which would make the fix look inert in exactly the deployment it is for.
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		mapError(w, store.ErrInvalid)
		return
	}
	u, tok, err := s.Store.RegisterUser(r.Context(), req.Username, req.DisplayName, req.Password)
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), 0, u.ID, "register_user", "user", u.ID, u.Username)
	setNavCookie(w, r, tok)
	writeJSON(w, http.StatusCreated, authResponse{User: u, Token: tok, Expiry: tokenNote})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !readJSON(w, r, &req) {
		return
	}
	u, tok, err := s.Store.Login(r.Context(), strings.TrimSpace(req.Username), req.Password)
	if err != nil {
		mapError(w, err)
		return
	}
	setNavCookie(w, r, tok)
	writeJSON(w, http.StatusOK, authResponse{User: u, Token: tok, Expiry: tokenNote})
}

// handleLogout revokes the presented token. Idempotent: a client that logs out
// twice, or whose token was already revoked, gets 204 both times.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// The cookie may be the only credential the browser sent (a plain navigation to
	// /logout), so the revocation takes whichever is present, and clears the cookie
	// either way.
	tok := bearerToken(r)
	if tok == "" {
		if c, err := r.Cookie(navCookieName); err == nil {
			tok = c.Value
		}
	}
	if tok != "" {
		if err := s.Store.RevokeToken(r.Context(), tok); err != nil {
			mapError(w, err)
			return
		}
	}
	clearNavCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// handleMe reports the caller's identity. Requires a token: unlike the read
// endpoints above there is no meaningful anonymous answer, so 401 is correct.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	uid := getActorID(r)
	if uid == 0 {
		mapError(w, store.ErrAuth)
		return
	}
	// The token only carries the id, so look the user up by id to render a
	// name. GetUser takes a username, so query by id here.
	var u store.User
	err := s.Store.QueryRowContext(r.Context(),
		`SELECT id, username, COALESCE(display_name,''), created_at FROM users WHERE id = ?`, uid).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.CreatedAt)
	if err != nil {
		mapError(w, store.ErrAuth)
		return
	}
	u.Role = "member"
	writeJSON(w, http.StatusOK, u)
}
