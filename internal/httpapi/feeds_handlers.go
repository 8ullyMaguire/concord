package httpapi

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// Feed handlers. See docs/specs/feeds-spec.md.
//
// Three rules govern all of them:
//
//   - The base URL comes from CONCORD_BASE_URL and from nowhere else. r.Host is
//     attacker-controlled via the Host header on a directly-reachable instance, so
//     building links from it lets one request mint a feed whose links point at an
//     attacker's host. Caches serve that to every subscriber, forever.
//   - A private project 404s through the same requireProjectID as its page. An EMPTY
//     feed would tell a reader the project exists and is merely quiet.
//   - No tally, ever. §6.6 hides the running counts, and a feed is a durable copy held
//     by third parties.

// feedBase returns the configured public origin, or ok=false when feeds are off.
//
// 503 rather than 404 when unset: the feature is configured off, which is a different
// fact from the resource not existing, and an operator who set nothing should be told
// which variable to set.
func (s *Server) feedBase(w http.ResponseWriter) (string, bool) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("CONCORD_BASE_URL")), "/")
	if base == "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(
			"Feeds are disabled. Set CONCORD_BASE_URL to this site's public origin, " +
				"for example https://concord.example.org\n"))
		return "", false
	}
	return base, true
}

// feedPath builds an absolute URL under the configured base.
func feedPath(base, path string) string { return base + path }

// resolveFeedFormat maps a path suffix or a ?format= parameter to a serialisation.
//
// An unknown format is a 400 rather than a silent default: a reader asking for
// ?format=pdf and handed RSS has no way to tell what happened.
func resolveFeedFormat(r *http.Request, suffix string) (string, bool) {
	// An explicit ?format= wins over the path suffix. The other order was wrong and a
	// test caught it: /feed.xml?format=atom answered with RSS, because feedSuffix sees
	// any .xml path and returned "rss" before the parameter was ever read. Silently
	// ignoring an explicit request is worse than ignoring the suffix -- the suffix is a
	// default, the parameter is a choice.
	if f := r.URL.Query().Get("format"); f != "" {
		switch f {
		case "rss", "atom":
			return f, true
		default:
			return "", false
		}
	}
	switch suffix {
	case "rss", "atom":
		return suffix, true
	default:
		return "rss", true
	}
}

// feedUpdated is the newest item time, or the feed's own time when it has none. A feed
// whose <updated> is the server's clock changes on every fetch, so readers that diff on
// it re-download everything; an empty feed must be stable.
func feedUpdated(items []FeedItem) time.Time {
	var newest time.Time
	for _, it := range items {
		if it.Updated.After(newest) {
			newest = it.Updated
		}
	}
	return newest
}

// feedSelfAndAlt are the two URLs every document carries: the machine copy and the page
// a human should read instead.
func feedSelfAndAlt(base, selfPath, altPath string) (string, string) {
	return feedPath(base, selfPath), feedPath(base, altPath)
}

// --- F1: public projects -----------------------------------------------------

func (s *Server) handleProjectsFeed(w http.ResponseWriter, r *http.Request) {
	format, ok := resolveFeedFormat(r, feedSuffix(r))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported format"})
		return
	}
	base, ok := s.feedBase(w)
	if !ok {
		return
	}
	// ListProjects, not ListProjectsVisibleTo: a feed is read by anonymous aggregators,
	// and a member's extra private projects must not leak into a public document. This is
	// the same call handleListProjects makes.
	projects, err := s.Store.ListProjects(r.Context())
	if err != nil {
		mapError(w, err)
		return
	}
	if len(projects) > feedItemLimit {
		projects = projects[:feedItemLimit]
	}
	self, alt := feedSelfAndAlt(base, "/feed.xml", "/projects")
	f := Feed{
		Title:       "Concord projects",
		SelfURL:     self,
		AltURL:      alt,
		Description: "Public projects on this Concord instance.",
	}
	for _, p := range projects {
		f.Items = append(f.Items, FeedItem{
			ID:      feedPath(base, "/projects/"+p.Slug),
			Title:   p.Name,
			Link:    feedPath(base, "/projects/"+p.Slug),
			Updated: floatTime(p.UpdatedAt),
			Summary: plainText(p.Description, 400),
		})
	}
	f.Updated = feedUpdated(f.Items)
	if err := writeFeed(w, format, f); err != nil {
		mapError(w, err)
	}
}

// floatTime converts a REAL unix timestamp to a time.Time. The store models timestamps as
// float64 everywhere, so this is the boundary where that stops.
func floatTime(sec float64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(sec), 0).UTC()
}

// --- F2: one project's complaints -------------------------------------------

func (s *Server) handleProjectFeed(w http.ResponseWriter, r *http.Request) {
	format, ok := resolveFeedFormat(r, feedSuffix(r))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported format"})
		return
	}
	base, ok := s.feedBase(w)
	if !ok {
		return
	}
	projectID, slug, ok := s.feedProjectID(w, r)
	if !ok {
		return
	}
	complaints, err := s.Store.ListComplaints(r.Context(), projectID, "")
	if err != nil {
		mapError(w, err)
		return
	}
	self, alt := feedSelfAndAlt(base, "/projects/"+slug+"/feed.xml", "/projects/"+slug)
	f := Feed{
		Title:       slug + ": complaints",
		SelfURL:     self,
		AltURL:      alt,
		Description: "Complaints filed against " + slug + ".",
	}
	names, err := s.displayNamesFor(r, complaintAuthors(complaints))
	if err != nil {
		mapError(w, err)
		return
	}
	for _, c := range complaints {
		f.Items = append(f.Items, FeedItem{
			ID:         feedPath(base, "/projects/"+slug+"/complaints/"+strconv.FormatInt(c.ID, 10)),
			Title:      c.Title,
			Link:       feedPath(base, "/projects/"+slug+"/complaints/"+strconv.FormatInt(c.ID, 10)),
			Updated:    floatTime(c.UpdatedAt),
			Summary:    plainText(c.Body, 400),
			Author:     names[c.AuthorID],
			Categories: []string{c.Status},
		})
	}
	f.Updated = feedUpdated(f.Items)
	if err := writeFeed(w, format, f); err != nil {
		mapError(w, err)
	}
}

// complaintAuthors collects the distinct author ids, for one batched display-name lookup.
func complaintAuthors(complaints []store.Complaint) []int64 {
	seen := map[int64]bool{}
	out := []int64{}
	for _, c := range complaints {
		if c.AuthorID != 0 && !seen[c.AuthorID] {
			seen[c.AuthorID] = true
			out = append(out, c.AuthorID)
		}
	}
	return out
}

// --- F3: the board -----------------------------------------------------------

func (s *Server) handleBoardFeed(w http.ResponseWriter, r *http.Request) {
	format, ok := resolveFeedFormat(r, feedSuffix(r))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported format"})
		return
	}
	base, ok := s.feedBase(w)
	if !ok {
		return
	}
	projectID, slug, ok := s.feedProjectID(w, r)
	if !ok {
		return
	}
	// GetBoard is the same call handleGetBoard makes, so the feed and the page cannot
	// disagree about what is on the board.
	_, cards, err := s.Store.GetBoard(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}
	self, alt := feedSelfAndAlt(base, "/projects/"+slug+"/board/feed.xml", "/projects/"+slug+"/board")
	f := Feed{
		Title:       slug + ": board",
		SelfURL:     self,
		AltURL:      alt,
		Description: "Board cards on " + slug + ", most recently moved first.",
	}
	for _, c := range cards {
		f.Items = append(f.Items, FeedItem{
			ID:      feedPath(base, "/projects/"+slug+"/board#"+strconv.FormatInt(c.FeatureID, 10)),
			Title:   c.Title,
			Link:    feedPath(base, "/projects/"+slug+"/board"),
			Updated: floatTime(c.EnteredAt),
			Categories: []string{
				c.Column,
			},
		})
	}
	f.Updated = feedUpdated(f.Items)
	if err := writeFeed(w, format, f); err != nil {
		mapError(w, err)
	}
}

// --- F4: consensus calls, WITHOUT the tally ----------------------------------

func (s *Server) handleConsensusFeed(w http.ResponseWriter, r *http.Request) {
	format, ok := resolveFeedFormat(r, feedSuffix(r))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported format"})
		return
	}
	base, ok := s.feedBase(w)
	if !ok {
		return
	}
	projectID, slug, ok := s.feedProjectID(w, r)
	if !ok {
		return
	}
	calls, err := s.Store.ListConsensusCallsForFeed(r.Context(), projectID, feedItemLimit)
	if err != nil {
		mapError(w, err)
		return
	}
	self, alt := feedSelfAndAlt(base,
		"/projects/"+slug+"/consensus/feed.xml", "/projects/"+slug+"/consensus")
	f := Feed{
		Title:       slug + ": consensus calls",
		SelfURL:     self,
		AltURL:      alt,
		Description: "Consensus calls opened on " + slug + ".",
	}
	ids := make([]int64, 0, len(calls))
	for _, c := range calls {
		ids = append(ids, c.OpenedBy)
	}
	names, err := s.displayNamesFor(r, ids)
	if err != nil {
		mapError(w, err)
		return
	}
	for _, c := range calls {
		// The call's STATE, and never a count of anyone's stance. TallyConsensus is one
		// method call away and the page already uses it; a feed item is a copy kept by
		// third parties, so §6.6 has to be enforced here rather than relied upon.
		title := "Consensus call"
		if c.Question != nil && *c.Question != "" {
			title = *c.Question
		}
		item := FeedItem{
			ID:      feedPath(base, "/projects/"+slug+"/consensus?call="+strconv.FormatInt(c.ID, 10)),
			Title:   title,
			Link:    feedPath(base, "/projects/"+slug+"/consensus?call="+strconv.FormatInt(c.ID, 10)),
			Updated: floatTime(c.OpensAt),
			Author:  names[c.OpenedBy],
			Categories: []string{
				c.Status,
			},
		}
		if c.Description != nil {
			item.Summary = plainText(*c.Description, 400)
		}
		f.Items = append(f.Items, item)
	}
	f.Updated = feedUpdated(f.Items)
	if err := writeFeed(w, format, f); err != nil {
		mapError(w, err)
	}
}

// --- F5: documents -----------------------------------------------------------

func (s *Server) handleDocumentsFeed(w http.ResponseWriter, r *http.Request) {
	format, ok := resolveFeedFormat(r, feedSuffix(r))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported format"})
		return
	}
	base, ok := s.feedBase(w)
	if !ok {
		return
	}
	projectID, slug, ok := s.feedProjectID(w, r)
	if !ok {
		return
	}
	// Every kind. The spec's first draft filtered on a "published" kind, which does not
	// exist: project_documents.kind is one of readme|spec|plan|wiki|adr|changelog|scout
	// and the table has NO draft/published column at all, so a document is published the
	// moment it exists. Filtering on it would have returned an ErrInvalid 400 for every
	// reader. Corrected here rather than carried forward.
	docs, err := s.Store.ListDocuments(r.Context(), projectID, "")
	if err != nil {
		mapError(w, err)
		return
	}
	if len(docs) > feedItemLimit {
		docs = docs[:feedItemLimit]
	}
	self, alt := feedSelfAndAlt(base,
		"/projects/"+slug+"/documents/feed.xml", "/projects/"+slug+"/documents")
	f := Feed{
		Title:       slug + ": documents",
		SelfURL:     self,
		AltURL:      alt,
		Description: "Published documents on " + slug + ".",
	}
	ids := make([]int64, 0, len(docs))
	for _, d := range docs {
		ids = append(ids, d.AuthorID)
	}
	names, err := s.displayNamesFor(r, ids)
	if err != nil {
		mapError(w, err)
		return
	}
	for _, d := range docs {
		f.Items = append(f.Items, FeedItem{
			ID:      feedPath(base, "/projects/"+slug+"/documents/"+d.Slug),
			Title:   d.Title,
			Link:    feedPath(base, "/projects/"+slug+"/documents/"+d.Slug),
			Updated: floatTime(d.UpdatedAt),
			Summary: plainText(d.Body, 400),
			Author:  names[d.AuthorID],
		})
	}
	f.Updated = feedUpdated(f.Items)
	if err := writeFeed(w, format, f); err != nil {
		mapError(w, err)
	}
}

// feedProjectID resolves {slug} to a project id, applying the SAME visibility rule as
// requireProjectID and returning the same generic 404.
//
// It exists because the feed routes are mounted on {slug} like the pages, while
// requireProjectID reads chi's {project_id} -- which is empty on a {slug} route. Calling
// requireProjectID from a feed handler answered {"error":"not found"} to EVERY reader,
// which reads exactly like a missing route. The fix is the pattern projects.go:71-79
// already uses for pages: GetProject on the slug, then projectReadable.
//
// The 404 body is the same generic {"error":"not found"} as everywhere else, so a private
// project's feed is indistinguishable from a nonexistent one (spec R3).
func (s *Server) feedProjectID(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
	slug := chi.URLParam(r, "slug")
	proj, err := s.Store.GetProject(r.Context(), slug)
	if err != nil {
		// An unreadable project and a nonexistent one must be indistinguishable, and the
		// body must never carry the slug.
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return 0, "", false
		}
		mapError(w, err)
		return 0, "", false
	}
	if !s.projectReadable(r, proj) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return 0, "", false
	}
	return proj.ID, slug, true
}

// displayNamesFor is a nil-safe wrapper, so a feed handler with no authors does not need
// its own guard.
func (s *Server) displayNamesFor(r *http.Request, ids []int64) (map[int64]string, error) {
	return s.Store.DisplayNamesFor(r.Context(), ids)
}

// feedSuffix reads the serialisation from the last path segment when the path ends in
// .xml -- /feed.xml and /atom.xml are aliases for ?format=rss and ?format=atom, because
// those are what people type and what <link rel="alternate"> wants.
func feedSuffix(r *http.Request) string {
	p := r.URL.Path
	if !strings.HasSuffix(p, ".xml") {
		return ""
	}
	switch {
	case strings.HasSuffix(p, "/atom.xml"):
		return "atom"
	case strings.HasSuffix(p, "/rss.xml"):
		return "rss"
	default:
		return "rss"
	}
}
