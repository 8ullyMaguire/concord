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
	writeJSON(w, http.StatusOK, authResponse{User: u, Token: tok, Expiry: tokenNote})
}

// handleLogout revokes the presented token. Idempotent: a client that logs out
// twice, or whose token was already revoked, gets 204 both times.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if tok := bearerToken(r); tok != "" {
		if err := s.Store.RevokeToken(r.Context(), tok); err != nil {
			mapError(w, err)
			return
		}
	}
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
