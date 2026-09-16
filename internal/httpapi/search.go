package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// handleSearch serves GET /api/v1/search — the first-class discovery
// endpoint (spec §15). Query params:
//
//	q            free text (FTS, AND-combined terms)
//	tag          repeatable; all must match
//	language     repeatable; all must match
//	min_lang_pct minimum language percentage (with language filters)
//	min_health   minimum health score in [0,1]
//	model        collective | maintainer_led
//	license      exact license string
//	sort         relevance (default) | updated | health | newest
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	filters := store.SearchFilters{
		Tags:      r.URL.Query()["tag"],
		Languages: r.URL.Query()["language"],
		Model:     r.URL.Query().Get("model"),
		License:   r.URL.Query().Get("license"),
	}
	if v := r.URL.Query().Get("min_lang_pct"); v != "" {
		pct, err := strconv.ParseFloat(v, 64)
		if err != nil || pct < 0 || pct > 100 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "min_lang_pct must be a number in [0,100]"})
			return
		}
		filters.MinLangPct = pct
	}
	if v := r.URL.Query().Get("min_health"); v != "" {
		h, err := strconv.ParseFloat(v, 64)
		if err != nil || h < 0 || h > 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "min_health must be a number in [0,1]"})
			return
		}
		filters.MinHealth = h
	}
	if filters.Model != "" && filters.Model != "collective" && filters.Model != "maintainer_led" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model must be collective or maintainer_led"})
		return
	}
	sortBy := strings.ToLower(r.URL.Query().Get("sort"))
	switch sortBy {
	case "", "relevance", "updated", "health", "newest":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sort must be relevance, updated, health, or newest"})
		return
	}

	res, err := s.Store.SearchProjects(r.Context(), strings.TrimSpace(r.URL.Query().Get("q")), filters, sortBy)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
