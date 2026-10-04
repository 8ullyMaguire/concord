package httpapi

// Scout's HTTP surface (docs/specs/scout-spec.md §5–§8).
//
// One GET handler. Everything visible about Scout is a read: a user posts an idea
// and gets back a report they can argue with. There is no write endpoint here
// because §8 persists a report as a `project_documents` row of kind `scout`, which
// goes through the existing document handler rather than a second write path that
// would have to keep its authorization in step with the first.
//
// Two things this file is careful about, both of which were defects elsewhere in the
// codebase:
//
//   - **Coverage is per-capability across the whole candidate set, and `open` is a
//     real answer.** It comes from assertion counts (spec §6), never from the
//     absence of a field. A capability nobody has asserted anything about is `open`,
//     not `partial` and not a confident zero — the same lesson as the Finder's
//     `absence is not evidence`.
//   - **`reports == 0` never renders as `0%`.** The JSON carries `reports` beside
//     `outcome_rate` for exactly this reason, so a client can tell an unmeasured
//     rate from a measured zero without guessing. `finder.Candidate.Reports` exists
//     for the same distinction.

import (
	"net/http"
	"strconv"
	"strings"

	"git.polarisocial.xyz/concord/concord/internal/scout"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// maxScoutIdeaLen bounds the idea text.
//
// 4 KiB: far more than any idea, small enough that one request cannot ask the
// decomposer to chew through a pasted file.
const maxScoutIdeaLen = 4096

// handleScout is GET /api/v1/scout?idea=...
//
// No project scoping — Scout reports on the catalog as a whole, which is the point:
// the answer to "what should I build on" spans projects. Access control is therefore
// per-candidate and happens inside ListScoutCandidates, using the same
// CanAccessProject decision the rest of the API enforces. A project the viewer may
// not read is filtered out of the candidate list and so never appears, is not
// counted in `candidates_considered`, and contributes nothing to any coverage
// state — so the response cannot be used to infer that a hidden project exists.
func (s *Server) handleScout(w http.ResponseWriter, r *http.Request) {
	idea := strings.TrimSpace(r.URL.Query().Get("idea"))
	if idea == "" {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": "an idea is required"})
		return
	}
	if len(idea) > maxScoutIdeaLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "the idea is too long (limit " +
				strconv.Itoa(maxScoutIdeaLen) + " bytes)"})
		return
	}

	maxVerdicts, msg := scoutMax(r)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}

	viewer := actorID(r)

	catalog, err := s.Store.ListCapabilityCatalog(r.Context())
	if err != nil {
		mapError(w, err)
		return
	}

	// Already access-filtered by the viewer passed in.
	candidates, err := s.Store.ListScoutCandidates(r.Context(), viewer)
	if err != nil {
		mapError(w, err)
		return
	}

	caps := scout.Decompose(idea, catalog)
	if err := s.applyCoverage(r, caps, candidates); err != nil {
		mapError(w, err)
		return
	}

	// Health and pain are per candidate, and each is a separate query. Health in
	// particular must be read live rather than from a stored column: `project_metrics`
	// is empty on this instance, so a stored `health_score` would make every
	// project `avoid`. ProjectHealth returns whether its number means anything, and
	// that bool is the whole difference between `avoid` and `adopt`.
	health := make(map[int64]scout.Health, len(candidates))
	pain := map[int64][]store.ScoutPain{}
	for _, c := range candidates {
		v, known, err := s.Store.ProjectHealth(r.Context(), c.ID)
		if err != nil {
			mapError(w, err)
			return
		}
		health[c.ID] = scout.Health{Value: v, Known: known}

		ps, err := s.Store.ProjectPain(r.Context(), c.ID, 5)
		if err != nil {
			mapError(w, err)
			return
		}
		if len(ps) > 0 {
			pain[c.ID] = ps
		}
	}

	report := scout.Classify(scout.Input{
		Idea:         idea,
		Capabilities: caps,
		Candidates:   candidates,
		Health:       health,
		Pain:         pain,
	}, scout.Options{
		LicenseConstraints: splitConstraints(
			r.URL.Query().Get("exclude_licenses")),
		MaxVerdicts: maxVerdicts,
	})

	writeJSON(w, http.StatusOK, report)
}

// Coverage states (spec §6). These are the same three the capabilities panel keeps
// apart, which is the point — one vocabulary for "how well known is this", not one
// per surface. Declared here because the store package models assertion STATES and
// has no concept of coverage across a candidate set; that is a Scout question.
const (
	// CoverageCovered: enough independent users confirmed a value.
	CoverageCovered = "covered"
	// CoveragePartial: somebody said something, and either nobody has confirmed it
	// or the community is actively disputing it. Both are partial for different
	// reasons and the signal string carries which.
	CoveragePartial = "partial"
	// CoverageOpen: nobody has asserted anything. NOT a zero and NOT a finding.
	CoverageOpen = "open"
)

// scoutMax parses the optional `max` parameter, returning 0 to mean "the default".
//
// The error is a plain string rather than an httpapi error type because the handler
// writes the 400 itself — there is no store error to map and inventing an error
// wrapper for one string would be a type with one call site.
func scoutMax(r *http.Request) (int, string) {
	raw := strings.TrimSpace(r.URL.Query().Get("max"))
	if raw == "" {
		return 0, ""
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, "max must be a number"
	}
	if n <= 0 {
		return 0, "max must be positive"
	}
	return n, ""
}

// applyCoverage fills each decomposed capability's three-state coverage from the
// assertions across the visible candidate set (spec §6).
//
// `covered` needs an assertion CONFIRMED by independent users, `partial` means
// somebody said something without it being established, and `open` means nobody
// has looked. `disputed` is partial rather than open on purpose: the community is
// actively arguing, which is not the same as nobody having looked.
//
// The counts come from CapabilityStateCountsForSet, not from the candidates' Attrs.
// Attrs carries `a.value` — the yes/no/partial string a project claims — and a
// first version read states out of it, so `covered` was unreachable and every
// decomposed capability came back `partial`. The two are different questions: what
// a project claims, versus whether anyone backed it.
func (s *Server) applyCoverage(r *http.Request, caps []store.ScoutCapability,
	candidates []store.ScoutCandidateRow) error {
	ids := make([]int64, 0, len(candidates))
	for _, c := range candidates {
		ids = append(ids, c.ID)
	}
	counts, err := s.Store.CapabilityStateCountsForSet(r.Context(), ids)
	if err != nil {
		return err
	}

	for i := range caps {
		c := &caps[i]
		if !c.InCatalog {
			// A proposed capability has no catalog row and so nothing can be
			// asserted about it. `open` is the honest state, and it is more useful
			// to a reader than leaving the field empty.
			c.Coverage = CoverageOpen
			continue
		}
		n := counts[c.Key]
		switch {
		case n.Confirmed > 0:
			c.Coverage = CoverageCovered
		case n.Total() > 0:
			c.Coverage = CoveragePartial
		default:
			c.Coverage = CoverageOpen
		}
	}
	return nil
}

// splitConstraints parses `exclude_licenses`.
//
// Comma-separated, whitespace tolerated, empty entries dropped. An empty or absent
// parameter means NO constraint — not "exclude everything" — which is why this
// returns nil rather than a slice holding one empty string.
func splitConstraints(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
