// Package httpapi serves Concord's JSON API v1 over chi (the router
// Forgejo uses). Follow projects.go/search.go as the pattern for new
// resources: handler → store → JSON, with mapError for domain errors.
package httpapi

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
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
		// img-src must allow data: because base.html's favicon is an inline SVG
		// data URI. Without it the directive falls back to default-src 'self',
		// which does not admit data:, so every browser blocks the site's own icon
		// on every page. The e2e suite caught it as a console error rather than a
		// visible failure, since a blocked favicon renders nothing anyone looks
		// for.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'unsafe-inline'; "+
				"style-src 'self' 'unsafe-inline'; img-src 'self' data:")
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

// limiter is process-global, so every test in this package shares one 100
// req/minute budget keyed on the client IP — and every test in this package
// runs on 127.0.0.1. Adding a handful of tests therefore starts producing 429s
// in tests that ran fine a commit earlier, which is a test suite that reports
// failures nobody caused.
//
// It is a variable rather than a const so tests can swap in a generous budget
// and, where a test is specifically about limiting, a real one. Production
// always gets defaultMaxRequests.
var limiter = &rateLimiter{visitors: make(map[string]*visitorRate)}

const defaultMaxRequests = 100

// maxRequests reads the current limit, preferring a test override.
var maxRequests = func() int { return defaultMaxRequests }

// lastPrune tracks the last idle-bucket sweep so the visitors map cannot
// grow without bound on a long-running server.
var lastPrune time.Time

// clientKey identifies the visitor for rate limiting. Concord binds to
// loopback and is exposed via a local reverse proxy / cloudflared tunnel,
// so RemoteAddr is the proxy, not the visitor — keying on it would put
// every visitor in one shared bucket. Only loopback clients can reach the
// listener, which makes the forwarded-IP headers trustworthy here.
func clientKey(r *http.Request) string {
	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	if ip == "127.0.0.1" || ip == "::1" {
		if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
			return "cf:" + cf
		}
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return "xff:" + strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	return ip
}

// pruneIdle drops visitor buckets idle longer than idleFor.
func (l *rateLimiter) pruneIdle(now time.Time, idleFor time.Duration) {
	for k, v := range l.visitors {
		if now.Sub(v.lastSeen) > idleFor {
			delete(l.visitors, k)
		}
	}
}

// rateLimit middleware limits requests per visitor (100/min).
func rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := clientKey(r)
		now := time.Now()

		limiter.mu.Lock()
		if now.Sub(lastPrune) > time.Minute {
			limiter.pruneIdle(now, 5*time.Minute)
			lastPrune = now
		}
		v, exists := limiter.visitors[key]
		switch {
		case !exists:
			limiter.visitors[key] = &visitorRate{count: 1, lastSeen: now}
		case now.Sub(v.lastSeen) > time.Minute:
			v.count = 1
			v.lastSeen = now
		default:
			v.count++
			if v.count > maxRequests() {
				limiter.mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"rate limit exceeded"}` + "\n"))
				return
			}
		}
		limiter.mu.Unlock()

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
	pages         map[string]*template.Template

	// Finder sessions are process-local and guarded by finderMu rather than
	// held in a map read without a lock. Lazy construction under the mutex means
	// NewServer stays the only constructor and no test has to remember to
	// initialise this -- an uninitialised map field that every call site
	// forgets to check is a nil-map write, which panics.
	finderMu sync.Mutex
	finder   *finderStore
}

func NewServer(store *store.DB, version string, webhookSecret ...string) (*Server, error) {
	// Hash the static assets here rather than inside loadTemplates. They were
	// loaded as a side effect of template parsing, which meant any code path
	// that served an asset without first building a server saw an empty map and
	// quietly served every asset as no-cache. The {{ asset }} function and the
	// cache headers must both work without a template load having happened
	// first, so the dependency is stated here where it is visible.
	loadAssetHashes(staticFS)

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
	// Each page is parsed TOGETHER with base.html so its "content" define
	// cannot collide with another page's (a shared namespace would make the
	// last-parsed page's body win on every route).
	// "documents" (2026-10-02) renders a project's README, spec, plan and ADR
	// history. The API for all of it existed since 2026-09-30; the page did not.
	// "notfound" (2026-10-02) is the page a browser lands on for a bad slug.
	// Before it existed every HTML route answered a miss with mapError, which
	// writes JSON -- so a mistyped URL returned 404 with a raw
	// {"error": "not found"} body and not one link. The status was correct; only
	// the presentation was missing.
	//
	// A template that is not listed here does not fail loudly. render() falls
	// back to writing "<html><body><h1>finder</h1></body></html>" with HTTP
	// 200, so adding a page without registering it produces a blank page that
	// looks like a successful load -- which is exactly what happened when
	// finder.html was first written. The list is the only thing standing
	// between a new template and that failure.
	pages := []string{"index", "search", "projects", "project", "board",
		"login", "register", "rank", "ranking", "documents", "notfound", "finder"}

	s.pages = make(map[string]*template.Template, len(pages))
	for _, name := range pages {
		tmpl, err := template.New("").Funcs(template.FuncMap{
			"asset": assetFunc(),
		}).ParseFS(templateFS, "templates/base.html", "templates/"+name+".html")
		if err != nil {
			return err
		}
		s.pages[name] = tmpl
	}
	return nil
}

// notFoundPage answers a miss on an HTML route with the styled 404 page.
//
// It exists because mapError is the wrong tool for a browser. mapError writes
// JSON, which is right for /api/v1/... and wrong for a page: a person who
// mistypes a project slug was served {"error": "not found"} as a document with
// no links out of it.
//
// The status is unchanged and must stay 404, not 403. Answering a slug the
// caller cannot see with 403 would confirm the slug exists, and that is the
// anti-enumeration decision these handlers already make deliberately (see
// handleProjectPage). This function therefore takes a reason string for the
// page's own copy and nothing that would distinguish absent from forbidden.
func (s *Server) notFoundPage(w http.ResponseWriter, r *http.Request, reason string) {
	s.render(w, http.StatusNotFound, "notfound", struct {
		pageData
		Reason string
	}{s.page("Not found"), reason})
}

// isAPIPath reports whether a path belongs to the JSON API. Used only to pick a
// representation for a 404 -- never to decide access -- so a path that is not
// under /api/ is treated as a browser page, which is the safer of the two
// mistakes: serving a browser JSON is cosmetic, serving an API client an HTML
// page breaks a machine consumer.
func isAPIPath(path string) bool {
	return strings.HasPrefix(path, "/api/")
}

func (s *Server) render(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	tmpl, ok := s.pages[name]
	if !ok {
		// An unregistered template is a programming error, not a user error,
		// so it must not be a 200. It used to render a near-empty page with a
		// success status, which is indistinguishable from a working page in a
		// browser and in a status-code test.
		log.Printf("render %s: no such template (registered: %v)", name, s.pageNames())
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "<html><body><h1>%s</h1></body></html>", name)
		return
	}
	if err := tmpl.ExecuteTemplate(w, "base.html", data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(securityHeaders)
	r.Use(rateLimit)
	// authenticate must come after rateLimit: an unauthenticated flood of bad
	// tokens should be rate limited, not turned into 64 MiB argon2 verifications.
	r.Use(s.authenticate)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		// One catch-all serves both audiences. An unmatched /api/v1/... path is
		// an API client and wants JSON; an unmatched browser path is a person
		// who followed a bad link and wants a page with somewhere to go. Both
		// keep the same status. Before this, /nope/nope returned a raw
		// {"error": "not found"} as a web page.
		if isAPIPath(r.URL.Path) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		s.notFoundPage(w, r, "There is nothing at this address.")
	})

	r.Get("/api/v1/healthz", s.handleHealthz)
	r.Get("/api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": s.Version})
	})

	// Forgejo webhooks
	r.Post("/api/v1/hooks/forgejo", s.handleForgejoWebhook)

	// Authentication. Everything else in the API already reads the actor from
	// the request context; these are the only endpoints that establish it.
	r.Post("/api/v1/auth/register", s.handleRegister)
	r.Post("/api/v1/auth/login", s.handleLogin)
	r.Post("/api/v1/auth/logout", s.handleLogout)
	r.Get("/api/v1/auth/me", s.handleMe)
	// Redeeming an invite is an auth-flow action, not a project action: the
	// caller has no project context yet, which is why the token identifies the
	// project rather than the other way round.
	r.Post("/api/v1/auth/redeem-invite", s.handleRedeemInvite)

	// Finder's page. The data comes from /api/v1/finder/* at runtime, so the
	// page is a shell that renders for anyone -- unlike the project page, which
	// must 404 for a project the caller cannot see. There is no slug to protect
	// and no data in the HTML.
	r.Get("/finder", s.handleFinderPage)

	// Finder (docs/specs/finder-spec.md): the question-at-a-time discovery flow.
	//
	// All of it read- and session-scoped: answering a Finder question writes to
	// a process-local session, never to the catalog. The contribution loop
	// (§5.3) is the one path that persists, and it goes through the same
	// capability endpoints the project page uses, not a Finder-specific write.
	r.Route("/api/v1/finder", func(r chi.Router) {
		r.Get("/questions", s.handleFinderQuestionCatalog)
		r.Post("/sessions", s.handleStartFinderSession)
		// Under /sessions, not beside it. Written the other way round the
		// {session_id} mount is a sibling of the /sessions literal, so the
		// literal wins that segment and every /sessions/{id}/answers request
		// dies as a chi subrouter miss -- a bare 404 with no JSON body, which
		// looks exactly like a missing route rather than a wrong one.
		r.Route("/sessions/{session_id}", func(r chi.Router) {
			r.Get("/", s.handleGetFinderSession)
			r.Post("/answers", s.handleAnswerFinderQuestion)
			r.Post("/back", s.handleGoBackFinder)
			r.Post("/lift", s.handleLiftFinder)
			r.Get("/results", s.handleFinderResults)
		})
	})

	r.Route("/api/v1/projects", func(r chi.Router) {
		r.Get("/", s.handleListProjects)
		r.Post("/", s.handleCreateProject)
		r.Route("/{slug}", func(r chi.Router) {
			r.Get("/", s.handleGetProject)
			r.Put("/tags", s.handleProjectTags)
			// The read half of the tags surface. Without it, PUT /tags is
			// write-only: the handler returns the project, which carries no
			// tags, so a client cannot confirm what it stored.
			r.Get("/tags", s.handleGetProjectTags)
			// §4.3 completeness meter for the under-tagged queue.
			r.Get("/tags/completeness", s.handleTagCompleteness)
			r.Put("/languages", s.handleProjectLanguages)
			r.Put("/metrics", s.handleProjectMetrics)

			// Visibility (2026-10-02). Member-only to change; see the handler
			// for why the gate is membership rather than trust level.
			r.Put("/visibility", s.handleSetProjectVisibility)

			// Invites: the grant a 'protected' project hands out. Minting and
			// revoking are member-only, and a member can only touch an invite
			// belonging to their own project.
			r.Post("/invites", s.handleCreateProjectInvite)
			r.Get("/invites", s.handleListProjectInvites)
			r.Post("/invites/{invite_id}/revoke", s.handleRevokeProjectInvite)

			// Project documents (2026-09-30): a project's README, spec, plan
			// and wiki. These are the documents that explain a project; until
			// now they lived only in a repository and were invisible here.
			//
			// Nested under {slug} rather than {project_id} so they sit beside
			// the rest of the project surface and share its slug resolution.
			// Write requires contributor, delete requires maintainer.
			r.Route("/documents", func(r chi.Router) {
				r.Get("/", s.handleListDocuments)
				r.Put("/", s.handlePutDocument)
				r.Get("/kinds", s.handleDocumentKinds)
				r.Get("/search", s.handleSearchDocuments)
				r.Get("/{doc_id}", s.handleGetDocument)
				r.Delete("/{doc_id}", s.handleDeleteDocument)
			})
		})
	})

	r.Get("/api/v1/search", s.handleSearch)

	// Criteria-aware ranking (2026-09-29). Criteria are per-project, proposable,
	// and each carries its own Glicko-2 pool, so "best designed" and "best
	// lightweight" are different questions rather than one blended score.
	r.Route("/api/v1/projects/{project_id}/criteria", func(r chi.Router) {
		r.Get("/", s.handleListCriteria)
		r.Post("/", s.handleCreateCriterion)
		r.Route("/{id}", func(r chi.Router) {
			r.Post("/vote", s.handleCastCriterionVote)
			r.Put("/active", s.handleSetCriterionActive)
		})
	})
	// A weight vector belongs in a body, so composite ranking is a POST. It is
	// the one query in the API that is not a plain GET.
	r.Post("/api/v1/projects/{project_id}/rank/composite", s.handleCompositeRank)

	// §4.10's Capabilities and Field reports panels (phase3-spec §4). Both
	// stores shipped with their data model and AC tests; neither had a read path
	// at all, so a project's capability claims and field reports were reachable
	// from the Finder's server-side calls and from nothing else.
	//
	// Top-level {project_id}, NOT nested inside the /{slug} route above. Nested,
	// chi binds the parameter as `slug`, `requireProjectID` reads `project_id`,
	// gets "", and every call 404s with `project ""` -- a failure whose message
	// points at the data rather than at the route.
	r.Get("/api/v1/projects/{project_id}/capabilities", s.handleListProjectCapabilities)
	r.Get("/api/v1/projects/{project_id}/field-reports", s.handleListProjectFieldReports)

	// The third §4.10 panel: solution standings, grouped per feature. Grouped
	// rather than merged into one project-wide leaderboard because a solution's
	// score is computed against the entries competing with it, so ranking across
	// features compares numbers that were never comparable — and a project's
	// "do nothing" baseline for one feature could outrank a real proposal for
	// another. See docs/specs/solutions-panel-spec.md.
	r.Get("/api/v1/projects/{project_id}/solutions", s.handleListProjectSolutions)

	// S2: alternatives arenas (docs/specs/alternatives-panel-spec.md).
	//
	// These are the FIRST arena routes in the API. `ArenaAlternatives` and
	// `ArenaUseCase` existed as constants, in DefaultArenaQuestion and in
	// FindArena's key shape, and nothing ever created one — all 70 live arenas
	// were feature-priority. Every kind of arena below already worked; this is
	// what makes the kind reachable.
	//
	// `{project_id}` in the path is the arena's OWNING project, resolved from a
	// slug by requireProjectID. The competing project in an entry body is a
	// numeric id, and that asymmetry is the only confusing thing about the shape.
	r.Route("/api/v1/projects/{project_id}/alternatives", func(r chi.Router) {
		r.Get("/", s.handleListAlternatives)
		r.Post("/", s.handleCreateAlternative)
		r.Post("/{arena_id}/entries", s.handleAddAlternativeCompetitor)
		r.Post("/{arena_id}/vote", s.handleCastAlternativesVote)
	})

	r.Route("/api/v1/projects/{project_id}/criteria-profiles", func(r chi.Router) {
		r.Get("/", s.handleListCriteriaProfiles)
		r.Post("/", s.handleSaveCriteriaProfile)
	})

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
			// Proposes a strategic-weight change; it does not apply one
			// directly (§6.2 -- weight is consensus-ratified, not a
			// maintainer dial).
			r.Put("/strategic-weight", s.handleSetStrategicWeight)
			r.Put("/status", s.handleSetFeatureStatus)
		})
	})

	// Solutions (§6.3-6.4). Nested under a feature because a solution has no
	// meaning without the outcome it is a way of delivering: "which approach
	// should we build?" is a question about a specific feature.
	//
	// Reads are unauthenticated on purpose, matching §6.1's duplicate panel and
	// §2.5's vote log. A ranking nobody can read is not a ranking anybody trusts.
	r.Route("/api/v1/projects/{project_id}/features/{id}/solutions", func(r chi.Router) {
		r.Get("/", s.handleListSolutions)
		r.Post("/", s.handleCreateSolution)
		r.Post("/vote", s.handleVoteSolutions)
		r.Route("/{solution_id}", func(r chi.Router) {
			r.Get("/", s.handleGetSolution)
			r.Post("/coverage", s.handleClaimCoverage)
			r.Post("/coverage/{complaint_id}/contest", s.handleContestCoverage)
		})
	})

	// Strategic weight proposals and themes (spec revision 4 §6.2).
	// Ratified by consensus rather than set by hand, which is what closes the
	// steering loophole the single maintainer-only PUT used to leave open.
	r.Route("/api/v1/projects/{project_id}/strategy", func(r chi.Router) {
		r.Get("/weight-proposals", s.handleListStrategicWeightProposals)
		r.Post("/weight-proposals/{proposal_id}/consent", s.handleConsentStrategicWeight)
		r.Get("/themes", s.handleListStrategicThemes)
	})

	// §9.3 public admin-action ledger. Instance-wide, unauthenticated on
	// purpose: the point is that everyone can watch what a steward did.
	r.Route("/api/v1/admin-ledger", func(r chi.Router) {
		r.Get("/", s.handleListAdminLedger)
	})

	// Global tag taxonomy (§4.3). Proposals are instance-scoped by default, so
	// these sit outside the project router: a taxonomy change affects every
	// project and cannot hang off one project's slug.
	// §6.1 duplicate detection at filing time. Read-only and unauthenticated on
	// purpose: the point is to show a filer what exists BEFORE they submit, and
	// a panel that needs a token is a panel nobody sees.
	r.Route("/api/v1/similar/{kind}", func(r chi.Router) {
		r.Get("/", s.handleFindSimilar)
	})

	r.Route("/api/v1/taxonomy", func(r chi.Router) {
		r.Get("/tags", s.handleListTags)
		r.Get("/proposals", s.handleListTaxonomyProposals)
		r.Post("/proposals", s.handleProposeTaxonomyChange)
		r.Post("/proposals/{proposal_id}/consent", s.handleConsentTaxonomyProposal)
	})

	// Emergency holds are project-scoped for writes (the hold carries a project
	// id) but the release route sits here so a client can find holds by id
	// without knowing the project first.
	r.Route("/api/v1/emergency-holds/{hold_id}", func(r chi.Router) {
		r.Post("/release", s.handleReleaseEmergencyHold)
	})

	// Pairwise votes
	r.Get("/api/v1/projects/{project_id}/votes/next", s.handleGetNextPair)
	r.Post("/api/v1/projects/{project_id}/join", s.handleJoinProject)
	r.Get("/api/v1/projects/{project_id}/members", s.handleListMembers)
	r.Get("/api/v1/projects/{project_id}/tallies", s.handleFeatureTallies)
	r.Get("/api/v1/projects/{project_id}/priorities", s.handleFeaturePriorities)
	r.Post("/api/v1/projects/{project_id}/features/{feature_id}/vote", s.handleCastVote)

	// Consensus
	// §6.5: the agenda. Opening a call is gated on three conditions, so it hangs
	// off a feature (whose solution arena is what is being judged) rather than
	// off the project.
	r.Route("/api/v1/projects/{project_id}/features/{id}/consensus", func(r chi.Router) {
		// Readiness is a GET and unauthenticated: it is the same public "why has
		// no call opened yet" view as §6.1's duplicate panel.
		r.Get("/", s.handleSolutionCallReadiness)
		r.Post("/", s.handleOpenSolutionCall)
	})

	r.Route("/api/v1/projects/{project_id}/consensus", func(r chi.Router) {
		r.Post("/", s.handleCreateConsensus)
		r.Route("/{call_id}", func(r chi.Router) {
			r.Get("/", s.handleGetConsensus)
			r.Post("/position", s.handleCastConsensusPosition)
			r.Post("/objection", s.handleCreateObjection)
			r.Post("/close", s.handleCloseConsensus)
			// §6.5's five outcomes, plus the fallback chain a stalled call
			// would follow.
			r.Post("/outcome", s.handleRecordCallOutcome)
			r.Get("/fallback", s.handleCallFallback)
			// Emergency hold (§6.6): suspends a call, overrides nothing.
			r.Get("/hold", s.handleGetCallHold)
			r.Post("/hold", s.handlePlaceEmergencyHold)
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
	r.Get("/projects/{slug}/rank", s.handleRankPage)
	r.Get("/projects/{slug}/ranking", s.handleRankingPage)
	r.Get("/projects/{slug}/documents", s.handleDocumentsPage)
	r.Get("/login", s.handleLoginPage)
	r.Get("/register", s.handleRegisterPage)
	r.Mount("/assets", staticHandler())

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
// pageData wraps template data with common fields.
type pageData struct {
	Title   string
	Version string
	Flash   string
	Scripts string
}

func (s *Server) page(title string) pageData {
	return pageData{Title: title, Version: s.Version}
}

func (s *Server) pageWithScript(title, script string) pageData {
	return pageData{Title: title, Version: s.Version, Scripts: script}
}

// pageNames lists the registered templates, for the error path above.
func (s *Server) pageNames() []string {
	out := make([]string, 0, len(s.pages))
	for n := range s.pages {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
