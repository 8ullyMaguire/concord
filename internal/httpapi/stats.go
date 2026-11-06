package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/discovery"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// handleStatsSummary serves the instance census (spec §3.1, STAT-1).
//
// GET is not mutating, so the consent gate does not apply to it
// (guest_handlers.go: isMutating) -- a visitor must be able to read the
// instance's own numbers without accepting anything first.
func (s *Server) handleStatsSummary(w http.ResponseWriter, r *http.Request) {
	census, err := s.Store.Census(r.Context())
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, census)
}

// latencySection is the three medians. Each is a discovery.Latency, so every one
// of them carries its own n -- spec §4 refuses a median without one.
type latencySection struct {
	Triage   discovery.Latency `json:"triage"`
	Delivery discovery.Latency `json:"delivery"`
	Decision discovery.Latency `json:"decision"`
}

// statsPayload is GET /api/v1/stats: the census, the latencies, and how much of
// the instance is actually instrumented.
type statsPayload struct {
	Census      store.Census   `json:"census"`
	Latency     latencySection `json:"latency"`
	Measured    store.Measured `json:"measured"`
	GeneratedAt int64          `json:"generated_at"`
}

// handleStats serves the one payload the statistics page renders from.
//
// The page is a client of this endpoint and computes nothing itself: a page that
// recomputes an aggregate the API returns is two answers waiting to disagree, and
// the page is the one nobody notices is wrong (spec §3.4). The
// TestStatsPageRendersWhatTheAPIReturns test compares the rendered text against
// this JSON rather than comparing two bodies, because two bodies can be equal
// and both wrong.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	payload, err := s.statsPayload(r.Context())
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

// statsPayload computes the statistics payload once.
//
// The page, the badges and the feed all read it, so there is one computation and
// no second place for the numbers to drift. It returns an error rather than a
// partial payload: a statistics document with a missing section is a document
// that looks complete.
func (s *Server) statsPayload(ctx context.Context) (statsPayload, error) {
	var p statsPayload
	var err error
	if p.Census, err = s.Store.Census(ctx); err != nil {
		return p, err
	}
	if p.Latency.Triage, err = s.Store.TriageLatency(ctx); err != nil {
		return p, err
	}
	if p.Latency.Delivery, err = s.Store.DeliveryLatency(ctx); err != nil {
		return p, err
	}
	if p.Latency.Decision, err = s.Store.DecisionLatency(ctx); err != nil {
		return p, err
	}
	if p.Measured, err = s.Store.Measured(ctx); err != nil {
		return p, err
	}
	p.GeneratedAt = time.Now().Unix()
	return p, nil
}

// handleStatsHistory serves GET /api/v1/stats/history?days=90.
//
// A BARE ARRAY, not an envelope. This is the only series route in the spec -- every
// other statistics route is a frame -- and a charting client wants the rows. The
// store caps `days` at 365: an unbounded series route is how a database ends up
// serialized into somebody's RSS reader.
//
// A malformed `days` falls back to the default rather than answering 400, matching
// how the audit and finder routes read their paging parameters. The cap still
// applies, so a hostile value cannot widen the response.
func (s *Server) handleStatsHistory(w http.ResponseWriter, r *http.Request) {
	days := 0
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			days = n
		}
	}
	rows, err := s.Store.SnapshotHistory(r.Context(), days)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// handleStatsPage renders the statistics page.
//
// The page is a shell: every number on it is fetched from /api/v1/stats at
// runtime, so the page cannot publish a number the API does not. That is spec
// §3.4's "one computation, two renderings" -- a page that recomputes the same
// aggregate is two answers waiting to disagree, and the page is the one nobody
// notices is wrong.
func (s *Server) handleStatsPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "stats", struct {
		pageData
	}{s.pageFor(r, "Statistics")})
}

// badge is one segment of a badge: a label and a pre-rendered value.
type badge struct {
	label string
	value string
}

// writeBadgeSVG renders a text-only SVG badge (spec §3.4).
//
// No font subsetting, no raster, no JavaScript: a badge that needs a font
// download is a badge that renders as tofu in somebody's README. Two system font
// families only -- the label in the UI family, the value in the monospace one.
//
// Cache-Control: max-age=3600. A badge is fetched on every README render, and a
// median moves by minutes, so an hour is the longest a wrong number should be
// able to survive in a cache.
func writeBadgeSVG(w http.ResponseWriter, segments []badge) {
	const fontSize = 11
	const charWidth = 7 // conservative for 11px system-ui; a wide glyph overflows
	const pad = 8       // left padding of a segment
	const gap = 12      // between the label and the value

	width := 0
	for _, s := range segments {
		width += pad + len(s.label)*charWidth + gap + len(s.value)*charWidth + pad
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="Concord badge">`, width)
	x := 0
	for _, s := range segments {
		labelW := len(s.label) * charWidth
		fmt.Fprintf(&b,
			`<text x="%d" y="14" font-family="system-ui, -apple-system, sans-serif" font-size="%d" fill="#555">%s</text>`,
			x+pad, fontSize, escapeXML(s.label))
		fmt.Fprintf(&b,
			`<text x="%d" y="14" font-family="ui-monospace, SFMono-Regular, monospace" font-size="%d" font-weight="600" fill="#111">%s</text>`,
			x+pad+labelW+gap, fontSize, escapeXML(s.value))
		x += pad + labelW + gap + len(s.value)*charWidth + pad
	}
	b.WriteString(`</svg>`)

	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

func escapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// formatLatencyValue renders a median for a badge, or an em dash when there is
// nothing to report.
//
// THE DASH IS THE POINT (spec §4.13). An unmeasured project renders "—", never
// "0.0": a zero is a claim that the instance measured nothing and found it
// instantaneous, and that claim propagates into a README where nobody can check
// it. The same applies to a project with no finished episode -- n=0 is not a
// median of zero.
func formatLatencyValue(lat discovery.Latency) string {
	if lat.N == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f", lat.MedianHours)
}

// handleBadge serves GET /badge/{slug}.svg and GET /badge/instance.svg.
//
// The instance badge is a reserved slug, not a project that happens to be called
// "instance": the route is mounted before the project lookup, and a project
// actually named "instance" would otherwise be shadowed by its own badge.
func (s *Server) handleBadge(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if slug == "instance" {
		s.writeInstanceBadge(w, r)
		return
	}
	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		mapError(w, err)
		return
	}
	// An unreadable project and a nonexistent one are indistinguishable, and the
	// body must never carry the slug (the same rule feedProjectID applies).
	if !s.projectReadable(r, proj) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	health, ok, err := s.Store.ProjectHealth(r.Context(), proj.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	lat, err := s.Store.DeliveryLatencyForProject(r.Context(), proj.ID)
	if err != nil {
		mapError(w, err)
		return
	}
	shipped, err := s.Store.ShippedSince(r.Context(), proj.ID, float64(time.Now().AddDate(0, 0, -30).Unix()))
	if err != nil {
		mapError(w, err)
		return
	}

	healthValue := "—"
	if ok {
		healthValue = fmt.Sprintf("%.2f", health)
	}
	writeBadgeSVG(w, []badge{
		{label: "health", value: healthValue},
		{label: "latency", value: formatLatencyValue(lat)},
		{label: "shipped-30d", value: fmt.Sprintf("%d", shipped)},
	})
}

// writeInstanceBadge renders the instance-level badge: how many projects have a
// computed health score, the instance delivery median, and features shipped in
// the last 30 days across all projects.
func (s *Server) writeInstanceBadge(w http.ResponseWriter, r *http.Request) {
	measured, err := s.Store.Measured(r.Context())
	if err != nil {
		mapError(w, err)
		return
	}
	lat, err := s.Store.DeliveryLatency(r.Context())
	if err != nil {
		mapError(w, err)
		return
	}
	shipped, err := s.Store.ShippedSince(r.Context(), 0, float64(time.Now().AddDate(0, 0, -30).Unix()))
	if err != nil {
		mapError(w, err)
		return
	}
	writeBadgeSVG(w, []badge{
		{label: "health", value: fmt.Sprintf("%d/%d", measured.WithHealth, measured.Projects)},
		{label: "latency", value: formatLatencyValue(lat)},
		{label: "shipped-30d", value: fmt.Sprintf("%d", shipped)},
	})
}
