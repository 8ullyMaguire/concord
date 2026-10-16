package httpapi

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// Guest interactions, the cookie notice, and folding.
//
// See docs/specs/guest-interactions-spec.md and docs/PLAN-guest.md. The two rules
// this file enforces are deliberately in ONE middleware rather than spread over the
// ~60 mutating handlers:
//
//   - a mutating request needs an accepted cookie notice, and
//   - a mutating request from a guest needs the instance to still be under its
//     guest limit.
//
// Applying either per-handler would mean a new route is ungated by default, and the
// route list grows constantly. One gate over the whole mutating surface fails
// closed: a route added later is gated without anyone remembering to gate it.

const (
	// guestMAULimitEnv is the configured threshold. 0 switches guest interactions
	// off entirely; a negative value is treated as 0.
	guestMAULimitEnv = "CONCORD_GUEST_MAU_LIMIT"
	// consentVersionEnv is the version of the notice text a visitor accepts.
	// Bumping it re-asks everyone, which is what changing the notice should do.
	consentVersionEnv = "CONCORD_CONSENT_VERSION"
	// consentCookieName holds the accepted version. It is HttpOnly: the page is
	// TOLD the state by the server (body[data-consent]) rather than reading it,
	// because a cookie a script can read is a cookie a script can forge, and the
	// only reason to read it in JS would be to skip the server's answer.
	consentCookieName = "concord_consent"
	// consentMaxAge is a year: the notice is about cookies, and a notice that
	// re-asks every week is a notice people dismiss without reading.
	consentMaxAge = 365 * 24 * 60 * 60
	// defaultGuestMAULimit is the request's 1k.
	defaultGuestMAULimit = 1000
	// defaultConsentVersion is version 1 of the notice.
	defaultConsentVersion = "1"
)

// guestMAULimitFromEnv reads the threshold.
//
// An unparseable value is the default rather than an error, and that is a decision:
// refusing to start because an operator typed CONCORD_GUEST_MAU_LIMIT=1,000 would
// take the whole instance down over a knob whose only jobs are "open" and "shut".
// The value is logged when it is ignored, so it is not silent either.
func guestMAULimitFromEnv() int {
	raw := strings.TrimSpace(os.Getenv(guestMAULimitEnv))
	if raw == "" {
		return defaultGuestMAULimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("%s=%q is not a number; using the default %d", guestMAULimitEnv, raw, defaultGuestMAULimit)
		return defaultGuestMAULimit
	}
	if n < 0 {
		return 0
	}
	return n
}

func consentVersionFromEnv() string {
	if v := strings.TrimSpace(os.Getenv(consentVersionEnv)); v != "" {
		return v
	}
	return defaultConsentVersion
}

// isMutating reports whether a method changes state. HEAD is included with GET
// because a HEAD request that reached a mutating handler would be a router bug, not
// a request to gate.
func isMutating(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// consentExempt reports whether a mutating path is deliberately not gated, with the
// reason attached at the route table below.
func consentExempt(path string) bool {
	switch {
	case path == "/api/v1/consent":
		// The gate's own door. Gating it would make acceptance impossible without
		// accepting first.
		return true
	case strings.HasPrefix(path, "/api/v1/auth/"):
		// Establishing or ending an identity is not an interaction with the
		// project's data, and hiding registration behind a cookie banner is a dark
		// pattern: a visitor must be able to create an account, and to leave, without
		// agreeing to anything.
		return true
	case path == "/api/v1/hooks/forgejo":
		// Server to server, authenticated by a shared secret. There is no browser
		// here and no cookie to accept, so a gate would refuse every webhook.
		return true
	}
	return false
}

// interactionGate is the single place consent and the guest limit are enforced.
//
// Consent is a gate, not a credential. It permits; identity still comes from the
// bearer token. That is why the cookie is safe to set from a page the visitor does
// not control and why planting it gains an attacker nothing.
func (s *Server) interactionGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isMutating(r.Method) || consentExempt(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if !s.consentAccepted(r) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error":   "cookies must be accepted before interacting with this site",
				"consent": "required",
			})
			return
		}
		if s.guestRefusal(w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// consentAccepted reports whether this request carries the accepted notice.
func (s *Server) consentAccepted(r *http.Request) bool {
	c, err := r.Cookie(consentCookieName)
	if err != nil {
		return false
	}
	return c.Value == s.ConsentVersion
}

// guestRefusal answers whether a mutating request from a guest must be refused
// because the instance has grown past its limit, and writes the refusal when it is.
//
// Checked on EVERY guest write, not only at mint. Mint-only would let an instance
// cross the limit with a visitor mid-session and keep serving that visitor forever,
// which is exactly the state the limit exists to end.
func (s *Server) guestRefusal(w http.ResponseWriter, r *http.Request) bool {
	uid := getActorID(r)
	if uid == 0 {
		return false
	}
	isGuest, err := s.Store.IsGuest(r.Context(), uid)
	if err != nil || !isGuest {
		// Not a guest (or not resolvable): the route's own gates decide. A store
		// error here must not turn into a 403 for a legitimate account, so the
		// failure is reported by the handler that needs the actor, not here.
		return false
	}
	st, err := s.Store.GuestStatusFor(r.Context(), s.GuestMAULimit)
	if err != nil {
		log.Printf("guest limit check: %v", err)
		// Fail closed for a guest: if the instance cannot say how big it is, it
		// must not keep taking nameless contributions.
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "guest contributions are paused: this instance could not check its own size",
			"code":  "guest_check_failed",
		})
		return true
	}
	if st.Allowed {
		return false
	}
	writeJSON(w, http.StatusForbidden, map[string]any{
		"error": "this instance has grown past its guest limit; create an account to take part",
		"code":  "guest_limit_reached",
		"mau":   st.MAU,
		"limit": st.Limit,
	})
	return true
}

// requireAccount refuses a write that needs a nameless party to be namable.
//
// Three writes need it: creating a project (a namespace is a name), and creating or
// revoking an invite (invites are the trust mechanism of §8.2, and a guest has no
// standing to extend trust). Everything else a guest may do is whatever an
// unprivileged actor may do -- §8.1 and §8.2 are untouched and refuse a guest on
// eligibility, not on guesthood.
func (s *Server) requireAccount(w http.ResponseWriter, r *http.Request, why string) bool {
	uid := getActorID(r)
	if uid == 0 {
		return false // the route's own auth gate answers
	}
	isGuest, err := s.Store.IsGuest(r.Context(), uid)
	if err != nil {
		// Fail closed: an unreadable identity must not be treated as an account.
		log.Printf("requireAccount: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "could not check whether this identity is an account",
			"code":  "identity_check_failed",
		})
		return true
	}
	if !isGuest {
		return false
	}
	writeJSON(w, http.StatusForbidden, map[string]any{
		"error": why,
		"code":  "account_required",
	})
	return true
}

// guestActorID returns the actor's id when the actor is a guest, and 0 otherwise.
// Used by register and login, which fold the guest's work into the account.
func (s *Server) guestActorID(r *http.Request) int64 {
	uid := getActorID(r)
	if uid == 0 {
		return 0
	}
	isGuest, err := s.Store.IsGuest(r.Context(), uid)
	if err != nil || !isGuest {
		return 0
	}
	return uid
}

// handleGuestStatus answers the public, read-only question a page asks before it
// shows a contribution control: is guest interaction open, and why not?
func (s *Server) handleGuestStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.GuestStatusFor(r.Context(), s.GuestMAULimit)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"allowed":         st.Allowed,
		"mau":             st.MAU,
		"limit":           st.Limit,
		"window_days":     st.WindowDays,
		"guests":          st.Guests,
		"stale_guests":    st.StaleGuests,
		"consent_version": s.ConsentVersion,
		"consented":       s.consentAccepted(r),
	})
}

// handleClaimGuest mints a guest identity. Consent is already enforced by the gate,
// so reaching this handler means the visitor accepted the notice.
func (s *Server) handleClaimGuest(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.GuestStatusFor(r.Context(), s.GuestMAULimit)
	if err != nil {
		mapError(w, err)
		return
	}
	if !st.Allowed {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "this instance has grown past its guest limit; create an account to take part",
			"code":  "guest_limit_reached",
			"mau":   st.MAU,
			"limit": st.Limit,
		})
		return
	}
	u, tok, err := s.Store.CreateGuest(r.Context())
	if err != nil {
		mapError(w, err)
		return
	}
	_ = s.Store.AddAudit(r.Context(), 0, u.ID, "guest_identity", "user", u.ID, u.Username)
	writeJSON(w, http.StatusCreated, authResponse{User: u, Token: tok, Expiry: tokenNote})
}

// handleAcceptConsent records acceptance: the cookie the gate reads, plus the
// identity's own record when there is an identity to record it against.
func (s *Server) handleAcceptConsent(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     consentCookieName,
		Value:    s.ConsentVersion,
		Path:     "/",
		MaxAge:   consentMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(r),
	})
	if uid := getActorID(r); uid != 0 {
		if err := s.Store.RecordConsent(r.Context(), uid); err != nil {
			// The cookie is set and the gate is satisfied; the missing record is
			// reported rather than turned into a failed acceptance, because the
			// visitor did accept and a 500 would tell them otherwise.
			log.Printf("record consent for user %d: %v", uid, err)
		} else {
			_ = s.Store.AddAudit(r.Context(), 0, uid, "accept_cookies", "user", uid,
				s.ConsentVersion)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "consent": s.ConsentVersion})
}

// isHTTPS reports whether the browser reached us over TLS, following the proxy.
// The instance is served through a cloudflared tunnel, so the request itself
// arrives on loopback over HTTP and r.TLS is nil while the visitor's connection is
// HTTPS: without the forwarded header the cookie would lose Secure on a public
// deployment, and with it unconditionally the cookie would be dropped on plain
// localhost.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// foldOnLogin moves a guest's work onto the account that just logged in, and
// reports what happened.
//
// The login itself has already succeeded, so a fold failure must not undo it: the
// person is logged in either way and would be stranded by a 500. The failure is
// returned so the response can say so plainly, and logged.
func (s *Server) foldOnLogin(r *http.Request, accountID int64) (store.FoldReport, error) {
	guestID := s.guestActorID(r)
	if guestID == 0 || guestID == accountID {
		return store.FoldReport{}, nil
	}
	rep, err := s.Store.FoldGuestInto(r.Context(), guestID, accountID)
	if err != nil {
		log.Printf("fold guest %d into account %d: %v", guestID, accountID, err)
		return rep, err
	}
	return rep, nil
}
