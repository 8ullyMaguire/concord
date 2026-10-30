package httpapi

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"

	"git.polarisocial.xyz/concord/concord/internal/finder"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// The contribution feed (R11, plan CF-2): "what can I do today, on something healthy, with the
// skills I have?"
//
// It is deliberately NOT a second finder. The project set comes from the same loader, the
// constraints go through finder.Apply, and the ordering starts from finder.Score. What is new is
// which rows are fed in -- work items instead of projects -- plus a final tiebreak that needs
// project health, because two tasks that fit equally are not equal if one lives in a project
// whose last commit was three years ago.
//
// It is a GET with query parameters, and the page is rendered on the server. Both are choices
// against a JavaScript feed: every answer is a URL that can be linked, shared, reloaded and read
// by a client without JavaScript, and a browser refresh cannot change what is offered.

type contributeItem struct {
	Key         string `json:"key"`
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Kind        string `json:"kind"`
	Difficulty  string `json:"difficulty,omitempty"`
	Minutes     *int   `json:"time_budget_minutes,omitempty"`
	NeedsMentor bool   `json:"needs_mentor"`
	ProjectSlug string `json:"project_slug"`
	ProjectName string `json:"project_name"`
	// Health is nil until the project has been measured, and HealthMeasured says which of the
	// two a consumer is looking at: an unmeasured project is not a project that scored zero.
	Health         *float64 `json:"health,omitempty"`
	HealthMeasured bool     `json:"health_measured"`
	// HealthLabel is the rendered form, so the page does no arithmetic in a template and the
	// API and the page cannot disagree about how a score is displayed.
	HealthLabel string `json:"health_label"`
}

type contributeRanked struct {
	contributeItem
	Fit     float64           `json:"fit"`
	Matches map[string]string `json:"matches,omitempty"`
	Unknown []string          `json:"unknown,omitempty"`
	// Why is the sentence under the item. Built from the engine's own Matches and Unknown
	// rather than from a second description of the scoring, so it cannot claim a reason the
	// ranker did not use.
	Why string `json:"why"`
}

type contributeResponse struct {
	Questions []finder.Dimension `json:"questions"`
	Items     []contributeRanked `json:"items"`
	Count     int                `json:"count"`
	// Pool is how many unclaimed items were considered before the answers filtered them, so a
	// short list reads as a small catalog rather than as a broken filter.
	Pool int `json:"pool"`
	// Applied echoes the constraints actually used, because a filter a caller cannot see is a
	// filter they cannot fix.
	Applied map[string]string `json:"applied"`
}

// handleContributeNext is the JSON surface. The page calls the same function, so the two cannot
// rank differently.
func (s *Server) handleContributeNext(w http.ResponseWriter, r *http.Request) {
	resp, err := s.contributeFeed(r)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleContributePage renders the same feed as a document.
func (s *Server) handleContributePage(w http.ResponseWriter, r *http.Request) {
	resp, err := s.contributeFeed(r)
	if err != nil {
		mapError(w, err)
		return
	}
	s.render(w, http.StatusOK, "contribute", struct {
		pageData
		contributeResponse
	}{s.pageFor(r, "Contribute"), resp})
}

func (s *Server) contributeFeed(r *http.Request) (contributeResponse, error) {
	ctx := r.Context()
	q := r.URL.Query()

	answers, applied, err := contributeAnswers(q)
	if err != nil {
		return contributeResponse{}, err
	}

	seed := finderSeed{Text: strings.TrimSpace(q.Get("q"))}
	projects, err := s.finderProjects(r, seed, nil)
	if err != nil {
		return contributeResponse{}, err
	}
	// The same enriched candidates the finder ranks, so an item inherits its project's
	// language, capabilities, report rate and arena standing instead of a parallel query that
	// would drift from them.
	projectCands, err := s.finderCandidatesWithIDs(r, seed, nil)
	if err != nil {
		return contributeResponse{}, err
	}
	base := make(map[int64]finder.Candidate, len(projectCands))
	for _, c := range projectCands {
		base[c.ProjectID] = c
	}

	health := make(map[int64]*float64, len(projects))
	pool := make([]finder.Candidate, 0, 128)
	meta := make(map[string]contributeItem, 128)
	for _, p := range projects {
		health[p.ID] = p.HealthScore
		b := base[p.ID]
		items, err := s.Store.ListWorkItems(ctx, p.ID)
		if err != nil {
			return contributeResponse{}, err
		}
		for _, w := range items {
			// A claimed item is not available, and offering it would send a newcomer to a task
			// somebody is already doing -- the single most discouraging thing a feed can do.
			if w.ClaimedBy != nil {
				continue
			}
			c := finder.WorkItemCandidate(p.ID, p.Slug, w.Title, w.ID,
				w.TimeBudgetMinutes, w.NeedsMentor, w.Difficulty, b.Attrs)
			c.Report, c.Reports, c.ArenaR = b.Report, b.Reports, b.ArenaR
			pool = append(pool, c)
			meta[c.Slug] = contributeItem{
				Key:            c.Slug,
				ID:             w.ID,
				Title:          w.Title,
				Kind:           w.Kind,
				Difficulty:     w.Difficulty,
				Minutes:        w.TimeBudgetMinutes,
				NeedsMentor:    w.NeedsMentor,
				ProjectSlug:    p.Slug,
				ProjectName:    p.Name,
				Health:         p.HealthScore,
				HealthMeasured: p.HealthScore != nil,
				HealthLabel:    healthLabel(p.HealthScore),
			}
		}
	}

	kept := pool
	for _, a := range answers {
		kept, _ = finder.Apply(kept, a)
	}

	ranked := finder.Score(kept, answers)
	finder.OrderWorkItems(ranked, health)

	const limit = 50
	out := make([]contributeRanked, 0, min(len(ranked), limit))
	for _, rk := range ranked {
		item, ok := meta[rk.Slug]
		if !ok {
			// Score must only return what it was given. A miss means the key contract between
			// WorkItemCandidate and this map broke, and rendering a wrong title would hide it.
			return contributeResponse{}, fmt.Errorf(
				"the feed lost track of work item %q: a bug, not a miss", rk.Slug)
		}
		out = append(out, contributeRanked{
			contributeItem: item,
			Fit:            rk.Fit,
			Matches:        rk.Matches,
			Unknown:        rk.Unknown,
			Why:            whyRanked(rk, applied),
		})
		if len(out) == limit {
			break
		}
	}

	return contributeResponse{
		Questions: feedQuestions(pool),
		Items:     out,
		Count:     len(out),
		Pool:      len(pool),
		Applied:   applied,
	}, nil
}

// healthLabel is the one place a score becomes text. "not measured" is its own phrase because
// the honest reading of a missing score is not "0%".
func healthLabel(score *float64) string {
	if score == nil {
		return "health not measured"
	}
	return fmt.Sprintf("health %d%%", int(math.Round(*score*100)))
}

// whyRanked explains one item using only what the engine reported.
func whyRanked(rk finder.Ranked, applied map[string]string) string {
	keys := make([]string, 0, len(rk.Matches))
	for k := range rk.Matches {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %s", k, rk.Matches[k]))
	}
	if len(parts) == 0 {
		if len(applied) == 0 {
			return "Nothing narrows this list yet — pick an answer above."
		}
		// The engine keeps candidates whose value is unknown (§2.7), so an item with no
		// matching fact is here because it is UNMEASURED, not because it fits. Saying that out
		// loud is the difference between a feed and a guess.
		return "No stated fact matches your answers; it is here because its fit is unknown, not because it fits."
	}
	return "Matches — " + strings.Join(parts, "; ") + "."
}

// contributeAnswers turns the query into engine answers, and REFUSES a value it does not know
// rather than ignoring it: silently dropping `minutes=2h` would return the unfiltered feed while
// the URL says it was filtered.
func contributeAnswers(q map[string][]string) ([]finder.Answer, map[string]string, error) {
	get := func(k string) string {
		if v, ok := q[k]; ok && len(v) > 0 {
			return strings.TrimSpace(v[0])
		}
		return ""
	}
	out := []finder.Answer{}
	applied := map[string]string{}

	if v := get("language"); v != "" && v != finder.ModeAny {
		applied["language"] = v
		out = append(out, finder.Answer{DimensionKey: "language", OptionID: v, Mode: finder.ModeRequired})
	}
	if v := get("minutes"); v != "" {
		switch v {
		case finder.Minutes15, finder.Minutes60, finder.Minutes480:
			applied["minutes"] = v
			out = append(out, finder.Answer{DimensionKey: finder.DimMinutes, OptionID: v, Mode: finder.ModeRequired})
		case finder.ModeAny:
		default:
			return nil, nil, invalid("minutes must be one of 15, 60, 480, any (got %q)", v)
		}
	}
	if v := get("difficulty"); v != "" {
		switch v {
		case "easy", "medium", "hard":
			applied["difficulty"] = v
			out = append(out, finder.Answer{DimensionKey: finder.DimDifficulty, OptionID: v, Mode: finder.ModeRequired})
		case finder.ModeAny:
		default:
			return nil, nil, invalid("difficulty must be one of easy, medium, hard, any (got %q)", v)
		}
	}
	if v := get("mentor"); v != "" {
		switch v {
		case "yes":
			// Required, because "I would rather not be alone" is a constraint: a task whose
			// onboarding depends on somebody who never shows up is the worst outcome here.
			applied["mentor"] = "yes"
			out = append(out, finder.Answer{DimensionKey: finder.DimMentor, OptionID: "yes", Mode: finder.ModeRequired})
		case "no":
			// A PREFERENCE, not a filter: declining a mentor does not make a mentored task
			// impossible, only less attractive. ModeDecideLater is what expresses that.
			applied["mentor"] = "no"
			out = append(out, finder.Answer{DimensionKey: finder.DimMentor, OptionID: "no", Mode: finder.ModeDecideLater})
		case finder.ModeAny:
		default:
			return nil, nil, invalid("mentor must be one of yes, no, any (got %q)", v)
		}
	}
	return out, applied, nil
}

// feedQuestions serves the declared question set with the language options this instance can
// actually satisfy, so the page never offers an answer that matches nothing.
func feedQuestions(pool []finder.Candidate) []finder.Dimension {
	qs := finder.ContributeQuestions()
	langs := map[string]bool{}
	for _, c := range pool {
		if l := c.Attrs["language"]; l != "" {
			langs[l] = true
		}
	}
	names := make([]string, 0, len(langs))
	for l := range langs {
		names = append(names, l)
	}
	sort.Strings(names)
	for i := range qs {
		if qs[i].Key != "language" {
			continue
		}
		opts := make([]finder.Option, 0, len(names)+1)
		for _, n := range names {
			opts = append(opts, finder.Option{ID: n, Label: n})
		}
		opts = append(opts, finder.Option{ID: finder.ModeAny, Label: "Any language"})
		qs[i].Options = opts
	}
	return qs
}

// invalid wraps a message in the sentinel mapError turns into 400, so a bad query parameter is
// answered with the parameter named rather than a bare "invalid".
func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{store.ErrInvalid}, args...)...)
}
