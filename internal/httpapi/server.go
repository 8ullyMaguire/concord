// Package httpapi serves Concord's JSON API v1 over chi (the router
// Forgejo uses). Follow projects.go/search.go as the pattern for new
// resources: handler → store → JSON, with mapError for domain errors.
package httpapi

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// securityHeaders adds security headers to all responses.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'")
		next.ServeHTTP(w, r)
	})
}

// rateLimiter is a simple in-memory rate limiter.
type rateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitorRate
}

type visitorRate struct {
	count    int
	lastSeen time.Time
}

var limiter = &rateLimiter{visitors: make(map[string]*visitorRate)}

// rateLimit middleware limits requests per IP.
func rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		if idx := strings.LastIndex(ip, ":"); idx != -1 {
			ip = ip[:idx]
		}
		limiter.mu.Lock()
		defer limiter.mu.Unlock()
		v, exists := limiter.visitors[ip]
		if !exists {
			limiter.visitors[ip] = &visitorRate{count: 1, lastSeen: time.Now()}
		} else {
			if time.Since(v.lastSeen) > time.Minute {
				v.count = 1
				v.lastSeen = time.Now()
			} else {
				v.count++
				if v.count > 100 {
					http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

//go:embed templates/*.html
var templateFS embed.FS

//go:embed assets/css/*.css assets/js/*.js
var staticFS embed.FS

type Server struct {
	Store         *store.DB
	Version       string
	WebhookSecret string
	templates     *template.Template
}

func NewServer(store *store.DB, version string, webhookSecret ...string) (*Server, error) {
	s := &Server{Store: store, Version: version}
	if len(webhookSecret) > 0 {
		s.WebhookSecret = webhookSecret[0]
	}
	if err := s.loadTemplates(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) loadTemplates() error {
	templates, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return err
	}
	s.templates = templates
	return nil
}

func (s *Server) render(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if s.templates == nil {
		_, _ = fmt.Fprintf(w, "<html><body><h1>%s</h1></body></html>", name)
		return
	}
	_ = s.templates.ExecuteTemplate(w, "base.html", data)
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(securityHeaders)
	r.Use(rateLimit)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	})

	r.Get("/api/v1/healthz", s.handleHealthz)
	r.Get("/api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": s.Version})
	})

	// Forgejo webhooks
	r.Post("/api/v1/hooks/forgejo", s.handleForgejoWebhook)

	r.Route("/api/v1/projects", func(r chi.Router) {
		r.Get("/", s.handleListProjects)
		r.Post("/", s.handleCreateProject)
		r.Route("/{slug}", func(r chi.Router) {
			r.Get("/", s.handleGetProject)
			r.Put("/tags", s.handleProjectTags)
			r.Put("/languages", s.handleProjectLanguages)
			r.Put("/metrics", s.handleProjectMetrics)
		})
	})

	r.Get("/api/v1/search", s.handleSearch)

	// Complaints
	r.Route("/api/v1/projects/{project_id}/complaints", func(r chi.Router) {
		r.Get("/", s.handleListComplaints)
		r.Post("/", s.handleCreateComplaint)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", s.handleGetComplaint)
			r.Post("/impact", s.handleAddImpact)
			r.Post("/validate", s.handleValidateComplaint)
			r.Post("/merge/{target_id}", s.handleMergeComplaints)
		})
	})

	// Features
	r.Route("/api/v1/projects/{project_id}/features", func(r chi.Router) {
		r.Get("/", s.handleListFeatures)
		r.Post("/", s.handleCreateFeature)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", s.handleGetFeature)
			r.Put("/strategic-weight", s.handleSetStrategicWeight)
		})
	})

	// Pairwise votes
	r.Get("/api/v1/projects/{project_id}/votes/next", s.handleGetNextPair)
	r.Get("/api/v1/projects/{project_id}/priorities", s.handleFeaturePriorities)
	r.Post("/api/v1/projects/{project_id}/features/{feature_id}/vote", s.handleCastVote)

	// Consensus
	r.Route("/api/v1/projects/{project_id}/consensus", func(r chi.Router) {
		r.Post("/", s.handleCreateConsensus)
		r.Route("/{call_id}", func(r chi.Router) {
			r.Get("/", s.handleGetConsensus)
			r.Post("/position", s.handleCastConsensusPosition)
			r.Post("/objection", s.handleCreateObjection)
			r.Post("/close", s.handleCloseConsensus)
		})
		r.Route("/objections/{id}", func(r chi.Router) {
			r.Put("/resolve", s.handleResolveObjection)
		})
	})

	// Charter
	r.Get("/api/v1/projects/{project_id}/charter", s.handleGetCharter)
	r.Put("/api/v1/projects/{project_id}/charter", s.handleUpdateCharter)

	// Comments/threads
	r.Route("/api/v1/projects/{project_id}/threads/{thread_kind}/{thread_id}", func(r chi.Router) {
		r.Get("/", s.handleGetThreadComments)
		r.Post("/", s.handleCreateComment)
		r.Route("/{id}", func(r chi.Router) {
			r.Delete("/", s.handleDeleteComment)
			r.Post("/vote", s.handleVoteComment)
		})
	})

	// Board
	r.Route("/api/v1/projects/{project_id}/board", func(r chi.Router) {
		r.Get("/", s.handleGetBoard)
		r.Put("/cards/{card_id}/move", s.handleMoveCard)
	})

	// Merge
	r.Route("/api/v1/projects/{project_id}/merge", func(r chi.Router) {
		r.Post("/", s.handleCreateMergeRequest)
		r.Put("/{mr_id}/approve", s.handleApproveMerge)
		r.Put("/{mr_id}/execute", s.handleExecuteMerge)
		r.Put("/{mr_id}/reject", s.handleRejectMerge)
	})

	// Lists
	r.Route("/api/v1/projects/{project_id}/lists", func(r chi.Router) {
		r.Get("/", s.handleListLists)
		r.Post("/", s.handleCreateList)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", s.handleGetList)
			r.Get("/entries", s.handleGetListEntries)
			r.Post("/entries", s.handleCreateListEntry)
		})
	})

	// Requests
	r.Route("/api/v1/projects/{project_id}/requests", func(r chi.Router) {
		r.Get("/", s.handleListRequests)
		r.Post("/", s.handleCreateRequest)
	})
	r.Route("/api/v1/requests/{request_id}", func(r chi.Router) {
		r.Get("/", s.handleGetRequest)
	})
	r.Route("/api/v1/requests/{request_id}/answers", func(r chi.Router) {
		r.Get("/", s.handleGetRequestAnswers)
		r.Post("/", s.handleAnswerRequest)
	})
	r.Route("/api/v1/answers/{answer_id}/vote", func(r chi.Router) {
		r.Post("/", s.handleVoteAnswer)
	})

	// Web UI
	r.Get("/", s.handleIndex)
	r.Get("/search", s.handleSearchPage)
	r.Get("/projects", s.handleProjectsPage)
	r.Get("/projects/{slug}", s.handleProjectPage)
	r.Get("/projects/{slug}/board", s.handleBoardPage)
	r.Mount("/assets", http.FileServer(http.FS(staticFS)))

	return r
}

// ---------------------------------------------------------------- helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return false
	}
	return true
}

// mapError maps store domain errors to HTTP status codes.
func mapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, store.ErrDuplicate):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, store.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, store.ErrAuth):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
	case errors.Is(err, store.ErrPerm):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

// pageData wraps template data with common fields.
type pageData struct {
	Title   string
	Version string
}

func (s *Server) page(title string) pageData {
	return pageData{Title: title, Version: s.Version}
}
