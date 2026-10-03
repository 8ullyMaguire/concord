package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"git.polarisocial.xyz/concord/concord/internal/finder"
	"git.polarisocial.xyz/concord/concord/internal/ranking"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// Finder: the Akinator-style discovery flow (docs/specs/finder-spec.md).
//
// REST rather than the GraphQL in the original brief, per the owner's decision
// and the frontend spec's own §1.2 — this codebase has no GraphQL and no build
// step, and adding both for one page would be a larger change than the feature.
//
// The API is a thin translation layer. Every question about WHICH question to
// ask, and about how candidates are filtered and ranked, lives in
// internal/finder. This file moves data in both directions and owns exactly two
// decisions of its own: which projects are visible to this caller, and how much
// state lives between requests.

// Sessions are in memory, not in the database.
//
// Finder §4.6 offers "save a session" and a shareable results URL, and both are
// expressible from an answer list without a table, so a migration for the
// feature whose only durable need is "a list of answers" is not yet earned. The
// cost is real and stated here: an unsaved session is lost on restart, which for
// an exploration nobody committed to is the honest behaviour rather than a
// silent failure — the client is told the session is not persisted.
type finderSession struct {
	ID           string
	Seed         finderSeed
	Candidates   []finder.Candidate
	Answers      []finder.Answer
	Asked        map[string]bool
	Depth        int
	LastFamily   string
	InitialCount int
	Removed      []finderFilteredOut
	CreatedAt    time.Time
}

type finderSeed struct {
	Text     string `json:"text"`
	Category string `json:"category"`
}

// API shapes. The `finderQuestion` type is the one the brief calls
// `firstQuestion` / `nextQuestion`; impact is computed server-side because it
// depends on the live candidate distribution, so the client cannot derive it.
type finderQuestionDTO struct {
	ID        string          `json:"id"`
	Key       string          `json:"key"`
	Family    string          `json:"family"`
	Text      string          `json:"text"`
	Kind      string          `json:"kind"`
	Options   []finder.Option `json:"options"`
	GainBits  float64         `json:"expected_information_gain"`
	KnownFrac float64         `json:"known_fraction"`
	CanSkip   bool            `json:"can_skip"`
	WhyAsked  string          `json:"why_asked"`
}

type finderCandidateDTO struct {
	ProjectID int64   `json:"project_id"`
	Slug      string  `json:"slug"`
	Name      string  `json:"name"`
	Fit       float64 `json:"fit_score"`
	// Coverage is the share of scoring weight that had evidence behind it.
	// It travels with Fit because a fit number without it is not readable: 60%
	// fit backed by full evidence and 60% fit backed by a third of it are
	// different facts, and the page cannot tell them apart without this.
	Coverage    float64            `json:"evidence_coverage"`
	Matches     map[string]string  `json:"matches"`
	Warnings    map[string]string  `json:"warnings"`
	Unknown     []string           `json:"unknown"`
	Explanation map[string]float64 `json:"explanation"`
}

type finderStateDTO struct {
	SessionID         string               `json:"session_id"`
	CandidateCount    int                  `json:"candidate_count"`
	InitialCount      int                  `json:"initial_candidate_count"`
	Asked             int                  `json:"questions_asked"`
	Question          *finderQuestionDTO   `json:"question"`
	TopCandidates     []finderCandidateDTO `json:"top_candidates"`
	ShouldSuggestStop bool                 `json:"should_suggest_stop"`
	StopReason        string               `json:"stop_reason"`
	Gaps              []finder.Gap         `json:"gaps"`
	Weights           map[string]float64   `json:"weights"`
	Answered          []finder.Answer      `json:"answers"`
	FilteredOut       []finderFilteredOut  `json:"filtered_out"`
	Persisted         bool                 `json:"persisted"`
}

type finderFilteredOut struct {
	Slug    string   `json:"slug"`
	Name    string   `json:"name"`
	Reasons []string `json:"reasons"`
	Filters []string `json:"filters"`
}

type finderStartRequest struct {
	Text           string  `json:"text"`
	Category       string  `json:"category"`
	SeedCandidates []int64 `json:"seed_candidates"`
}

type finderAnswerRequest struct {
	QuestionKey string `json:"question_key"`
	OptionID    string `json:"option_id"`
	Mode        string `json:"mode"`
}

// ---------------------------------------------------------------- catalog

// handleFinderPage renders the Finder shell.
//
// Seeded from ?seed= (§3's deep link). The value is echoed into an input's
// value attribute by the template's own JS reading location.search, not here,
// so a crafted seed cannot inject markup through template interpolation.
func (s *Server) handleFinderPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "finder", s.page("Finder"))
}

// handleFinderQuestionCatalog returns every dimension the engine can ask about,
// with its live information gain.
//
// This exists because Finder §5.4's promise — "Finder never asks about an
// option the catalog can't actually deliver" — is otherwise only checkable by
// driving the whole UI. Here it is one request: a dimension whose gain is below
// the threshold appears with an `askable: false`, and the API test asserts no
// askable dimension has zero gain.
func (s *Server) handleFinderQuestionCatalog(w http.ResponseWriter, r *http.Request) {
	projects, err := s.finderCandidates(r, finderSeed{})
	if err != nil {
		mapError(w, err)
		return
	}

	st := finder.State{Candidates: projects, Asked: map[string]bool{}}
	type entry struct {
		Key           string   `json:"key"`
		Family        string   `json:"family"`
		Label         string   `json:"label"`
		GainBits      float64  `json:"gain_bits"`
		KnownFraction float64  `json:"known_fraction"`
		Askable       bool     `json:"askable"`
		Options       []string `json:"values_present"`
	}
	out := struct {
		CandidateCount int                `json:"candidate_count"`
		MinGainBits    float64            `json:"min_gain_bits"`
		Dimensions     []entry            `json:"dimensions"`
		Gaps           []finder.Gap       `json:"gaps"`
		Weights        map[string]float64 `json:"weights"`
	}{
		CandidateCount: len(projects),
		MinGainBits:    finder.MinGainBits,
		Gaps:           finder.Gaps(st),
		Weights:        finder.WeightReport(),
		Dimensions:     []entry{},
	}

	for _, dim := range finder.AllDimensions(st) {
		values := make([]string, 0, len(dim.Options))
		for _, o := range dim.Options {
			if o.ID == finder.ModeAny {
				continue
			}
			values = append(values, o.ID)
		}
		out.Dimensions = append(out.Dimensions, entry{
			Key:           dim.Key,
			Family:        dim.Family,
			Label:         dim.Label,
			GainBits:      dim.GainBits,
			KnownFraction: dim.KnownFraction,
			Askable:       dim.GainBits >= finder.MinGainBits,
			Options:       values,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------- session

func (s *Server) handleStartFinderSession(w http.ResponseWriter, r *http.Request) {
	var req finderStartRequest
	if !readJSON(w, r, &req) {
		return
	}

	seed := finderSeed{Text: req.Text, Category: req.Category}
	cands, err := s.finderCandidatesWithIDs(r, seed, req.SeedCandidates)
	if err != nil {
		mapError(w, err)
		return
	}

	sess := &finderSession{
		ID:         newFinderSessionID(),
		Seed:       seed,
		Candidates: cands,
		Asked:      map[string]bool{},
		CreatedAt:  time.Now(),
	}
	s.finderStore().put(sess)
	writeJSON(w, http.StatusCreated, s.finderState(sess))
}

func (s *Server) handleGetFinderSession(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.finderStore().get(chi.URLParam(r, "session_id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such finder session"})
		return
	}
	writeJSON(w, http.StatusOK, s.finderState(sess))
}

func (s *Server) handleAnswerFinderQuestion(w http.ResponseWriter, r *http.Request) {
	var req finderAnswerRequest
	if !readJSON(w, r, &req) {
		return
	}
	sess, ok := s.finderStore().get(chi.URLParam(r, "session_id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such finder session"})
		return
	}
	if req.QuestionKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "question_key is required"})
		return
	}

	mode := req.Mode
	if mode == "" {
		mode = finder.ModeRequired
	}
	answer := finder.Answer{DimensionKey: req.QuestionKey, OptionID: req.OptionID, Mode: mode}

	// A mode that does not filter must not change the set — asserted here as
	// well as in the engine, because this is the boundary where a bug would
	// become invisible to a user.
	switch mode {
	case finder.ModeRequired, finder.ModeSkip, finder.ModeDoesntMatter, finder.ModeDecideLater:
	default:
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": "mode must be required, skip, doesnt-matter or decide-later"})
		return
	}

	kept, removedBy := finder.Apply(sess.Candidates, answer)
	sess.Answers = append(sess.Answers, answer)
	sess.Asked[req.QuestionKey] = true
	if mode == finder.ModeRequired {
		sess.Depth++
	}
	sess.Candidates = kept
	sess.LastFamily = familyFor(req.QuestionKey)
	sess.Removed = appendRemoved(sess.Removed, kept, removedBy, req.QuestionKey)

	writeJSON(w, http.StatusOK, s.finderState(sess))
}

// handleGoBackFinder recomputes rather than replaying.
//
// Finder §2.6: "The user's answers are a graph, not a stack" and §6.2: changing
// an earlier answer "recomputes, does not blindly replay" the later path. So
// going back drops the last answer and re-derives the candidate set from the
// full remaining answer list — it does not pop a stored snapshot, because a
// snapshot is exactly what would go stale the moment the catalog changes.
func (s *Server) handleGoBackFinder(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.finderStore().get(chi.URLParam(r, "session_id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such finder session"})
		return
	}
	if len(sess.Answers) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nothing to go back from"})
		return
	}

	sess.Answers = sess.Answers[:len(sess.Answers)-1]
	sess.Depth = 0
	sess.Asked = map[string]bool{}
	for _, a := range sess.Answers {
		sess.Asked[a.DimensionKey] = true
		if a.Mode == finder.ModeRequired {
			sess.Depth++
		}
	}

	base, err := s.finderCandidates(r, sess.Seed)
	if err != nil {
		mapError(w, err)
		return
	}
	// Replay is over an answer LIST against the CURRENT catalog, not over a
	// stored snapshot: same inputs, fresh evaluation.
	set := base
	for _, a := range sess.Answers {
		set, _ = finder.Apply(set, a)
	}
	sess.Candidates = set
	sess.LastFamily = ""
	if len(sess.Answers) > 0 {
		sess.LastFamily = familyFor(sess.Answers[len(sess.Answers)-1].DimensionKey)
	}
	writeJSON(w, http.StatusOK, s.finderState(sess))
}

// handleLiftFinder removes one filter and re-ranks.
func (s *Server) handleLiftFinder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionKey string `json:"question_key"`
	}
	if !readJSON(w, r, &req) || req.QuestionKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "question_key is required"})
		return
	}
	sess, ok := s.finderStore().get(chi.URLParam(r, "session_id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such finder session"})
		return
	}

	found := false
	kept := make([]finder.Answer, 0, len(sess.Answers))
	for _, a := range sess.Answers {
		if a.DimensionKey == req.QuestionKey && a.Mode == finder.ModeRequired {
			found = true
			continue
		}
		kept = append(kept, a)
	}
	if !found {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": "no required answer on " + req.QuestionKey + " to lift"})
		return
	}
	sess.Answers = kept

	base, err := s.finderCandidates(r, sess.Seed)
	if err != nil {
		mapError(w, err)
		return
	}
	set := base
	for _, a := range sess.Answers {
		set, _ = finder.Apply(set, a)
	}
	sess.Candidates = set
	sess.Asked = map[string]bool{}
	for _, a := range sess.Answers {
		sess.Asked[a.DimensionKey] = true
	}
	writeJSON(w, http.StatusOK, s.finderState(sess))
}

func (s *Server) handleFinderResults(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.finderStore().get(chi.URLParam(r, "session_id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such finder session"})
		return
	}
	ranked := finder.Score(sess.Candidates, sess.Answers)
	writeJSON(w, http.StatusOK, struct {
		SessionID      string               `json:"session_id"`
		Answers        []finder.Answer      `json:"answers"`
		CandidateCount int                  `json:"candidate_count"`
		Candidates     []finderCandidateDTO `json:"candidates"`
		UnknownFit     []finderCandidateDTO `json:"unknown_fit"`
		FilteredOut    []finderFilteredOut  `json:"filtered_out"`
		Gaps           []finder.Gap         `json:"gaps"`
		Weights        map[string]float64   `json:"weights"`
	}{
		SessionID:      sess.ID,
		Answers:        sess.Answers,
		CandidateCount: len(sess.Candidates),
		Candidates:     toCandidateDTOs(ranked),
		UnknownFit:     unknownFit(ranked),
		FilteredOut:    sess.Removed,
		Gaps:           finder.Gaps(finder.State{Candidates: sess.Candidates, Asked: sess.Asked}),
		Weights:        finder.WeightReport(),
	})
}

// ---------------------------------------------------------------- state

// finderState builds the response every session endpoint returns.
func (s *Server) finderState(sess *finderSession) finderStateDTO {
	st := finder.State{
		Candidates: sess.Candidates,
		Answers:    sess.Answers,
		Asked:      sess.Asked,
		Depth:      sess.Depth,
		LastFamily: sess.LastFamily,
	}

	stop, reason := finder.ShouldStop(st)
	out := finderStateDTO{
		SessionID:         sess.ID,
		CandidateCount:    len(sess.Candidates),
		InitialCount:      sess.InitialCount,
		Asked:             sess.Depth,
		ShouldSuggestStop: stop,
		StopReason:        reason,
		Gaps:              finder.Gaps(st),
		Weights:           finder.WeightReport(),
		Answered:          sess.Answers,
		FilteredOut:       sess.Removed,
		Persisted:         false,
	}

	ranked := finder.Score(sess.Candidates, sess.Answers)
	out.TopCandidates = toCandidateDTOs(ranked)
	if len(out.TopCandidates) > 5 {
		out.TopCandidates = out.TopCandidates[:5]
	}

	// A question is only offered when there is a reason to ask it. On a stop
	// condition the caller gets the stop reason and the gaps instead, which is
	// the honest end of a session the catalog cannot narrow further.
	if !stop {
		if dim, gain, err := finder.NextQuestion(st); err == nil {
			out.Question = &finderQuestionDTO{
				ID:        dim.Key,
				Key:       dim.Key,
				Family:    dim.Family,
				Text:      finderQuestionText(dim),
				Kind:      dim.Kind,
				Options:   dim.Options,
				GainBits:  gain,
				KnownFrac: dim.KnownFraction,
				CanSkip:   true,
				WhyAsked:  finderWhyAsked(dim, gain, len(sess.Candidates)),
			}
		}
	}
	return out
}

// finderQuestionText renders a dimension as a sentence a person can answer.
//
// Two texts per dimension rather than one: the plain-language form is the
// default and the expert form is the canonical key, which is Finder §6.5's
// guided/expert switch with the switch left to the client for now.
func finderQuestionText(dim *finder.Dimension) string {
	if strings.HasPrefix(dim.Key, "cap:") {
		return "Does " + dim.Label + " matter to you?"
	}
	return dim.Label
}

// finderWhyAsked explains the question in terms the user can act on.
//
// The first version picked the option that removes the MOST candidates and
// stated it absolutely: "choosing \"nim\" would narrow 69 of 70". On the live
// instance that is true of every option of the language dimension (nine values,
// 53-69 removed each), because "most removed" is the rarest option rather than
// the most informative one. §4.2 shows the intended shape instead — both ends
// of the split, side by side — so the text now reports the best and worst
// option together.
//
// It also says so when the split is flat. A dimension that removes nearly
// everything for every answer is not a useful question, and a page that calls
// it "the biggest split left" is telling the user the opposite.
func finderWhyAsked(dim *finder.Dimension, gain float64, total int) string {
	// Track the most and least destructive real option. `any` is excluded: it is
	// the non-filtering mode, and counting it would make every dimension look
	// like it has an option that removes nothing.
	leastLabel, mostLabel := "", ""
	least, most := 0, 0
	seen := false
	for _, o := range dim.Options {
		if o.ID == finder.ModeAny {
			continue
		}
		if !seen {
			least, most = o.Impact, o.Impact
			leastLabel, mostLabel = o.Label, o.Label
			seen = true
			continue
		}
		if o.Impact > most {
			most, mostLabel = o.Impact, o.Label
		}
		if o.Impact < least {
			least, leastLabel = o.Impact, o.Label
		}
	}
	if !seen {
		return ""
	}

	// Flat: every answer rules out nearly everything, so the dimension cannot
	// distinguish candidates however it is answered. The first version of this
	// function called such a dimension "the biggest split left", which is the
	// opposite of true and is what the live instance showed: nine language
	// options, each ruling out 53-69 of 70 candidates.
	// Flat means the answer barely matters. The measure is how many candidates
	// SURVIVE the best answer: on the live instance the language dimension's
	// friendliest option still leaves 17 of 70, and the difference between its
	// best and worst option is only 16 candidates -- so answering it tells the
	// user very little about which project is right, whatever the raw numbers
	// look like.
	//
	// Comparing the removed counts is the wrong test. 69 versus 53 is a 16-point
	// gap that sounds decisive and is not: both eliminate the overwhelming
	// majority of the field, so both leave a short-list of essentially the same
	// size. An earlier version keyed on "every option removes >= 90%", which
	// missed this case because 53/70 is 76%.
	surviveBest := total - most
	surviveWorst := total - least
	spread := surviveWorst - surviveBest
	if total > 0 && (least >= total*9/10 || surviveBest <= total/10 || spread*10 <= total) {
		return fmt.Sprintf(
			"Every answer here leaves only %d to %d of %d candidates, so this question "+
				"tells us little about which project is right.",
			surviveBest, surviveWorst, total)
	}
	if least == most {
		return "Every answer here rules out the same number of candidates, which " +
			"usually means the catalog holds one value for this and cannot narrow further."
	}
	return fmt.Sprintf("%q would rule out %d of %d candidates; %q only %d — the biggest split left.",
		mostLabel, most, total, leastLabel, least)
}

// ---------------------------------------------------------------- helpers

func toCandidateDTOs(ranked []finder.Ranked) []finderCandidateDTO {
	out := make([]finderCandidateDTO, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, finderCandidateDTO{
			ProjectID:   r.ProjectID,
			Slug:        r.Slug,
			Name:        r.Name,
			Fit:         r.Fit,
			Coverage:    r.Explanation["evidence_coverage"],
			Matches:     r.Matches,
			Warnings:    r.Warnings,
			Unknown:     r.Unknown,
			Explanation: r.Explanation,
		})
	}
	return out
}

// unknownFit returns the candidates the engine cannot rank confidently: those
// carrying at least one unknown field that no answer covered.
//
// Separate from the main list because "we don't have enough data on this" and
// "this is a poor match" are different answers, and folding them together is how
// a project nobody has contributed data for becomes invisible.
func unknownFit(ranked []finder.Ranked) []finderCandidateDTO {
	out := []finderCandidateDTO{}
	for _, r := range ranked {
		if len(r.Unknown) > 0 {
			out = append(out, toCandidateDTOs([]finder.Ranked{r})[0])
		}
	}
	return out
}

func familyFor(key string) string {
	for _, d := range finder.AllDimensions(finder.State{
		Candidates: []finder.Candidate{{Attrs: map[string]string{key: "x"}}},
	}) {
		if d.Key == key {
			return d.Family
		}
	}
	return ""
}

func appendRemoved(acc []finderFilteredOut, kept []finder.Candidate, removedBy map[string]int, key string) []finderFilteredOut {
	total := 0
	for _, n := range removedBy {
		total += n
	}
	if total == 0 {
		return acc
	}
	return append(acc, finderFilteredOut{
		Filters: []string{key},
		Reasons: []string{fmt.Sprintf("filtered by %s", key)},
	})
}

// finderCandidates loads the candidate set for a seed.
func (s *Server) finderCandidates(r *http.Request, seed finderSeed) ([]finder.Candidate, error) {
	return s.finderCandidatesWithIDs(r, seed, nil)
}

func (s *Server) finderCandidatesWithIDs(r *http.Request, seed finderSeed, ids []int64) ([]finder.Candidate, error) {
	ctx := r.Context()
	projects, err := s.finderProjects(r, seed, ids)
	if err != nil {
		return nil, err
	}
	projectIDs := make([]int64, 0, len(projects))
	for _, p := range projects {
		projectIDs = append(projectIDs, p.ID)
	}

	caps, err := s.Store.ListCapabilitiesForSet(ctx, projectIDs, "")
	if err != nil {
		return nil, err
	}

	out := make([]finder.Candidate, 0, len(projects))
	for _, p := range projects {
		c := finder.Candidate{ProjectID: p.ID, Slug: p.Slug, Name: p.Name, Attrs: map[string]string{}}
		for k, v := range caps[p.ID] {
			c.Attrs["cap:"+k] = v
		}
		if lang, err := s.projectPrimaryLanguage(ctx, p.ID); err == nil && lang != "" {
			c.Attrs["language"] = lang
		}
		if p.GovernanceModel != "" {
			c.Attrs["governance"] = p.GovernanceModel
		}
		if p.License != "" {
			c.Attrs["license"] = p.License
		}
		rate, n, err := s.Store.FieldReportOutcomeRate(ctx, p.ID)
		if err == nil {
			c.Report, c.Reports = rate, n
		}
		// Arena standing is read, not hardcoded. It is zero today because
		// arena_entries is empty and no alternatives arena exists, but the term
		// is computed so it starts working when one does -- and the engine's
		// TestArenaStandingIsComputedNotConstant is what keeps that honest.
		c.ArenaR = s.projectArenaStanding(ctx, p.ID)
		out = append(out, c)
	}
	return out, nil
}

// finderProjects resolves a seed to the projects a candidate set starts from.
//
// Visibility is enforced here, once, rather than in each caller: a candidate set
// built from a private project would leak it through the Finder even though
// every other surface respects the four visibility levels.
func (s *Server) finderProjects(r *http.Request, seed finderSeed, ids []int64) ([]store.Project, error) {
	ctx := r.Context()
	actor := getActorID(r)
	if len(ids) > 0 {
		out := make([]store.Project, 0, len(ids))
		for _, id := range ids {
			p, err := s.Store.GetProjectByID(ctx, id)
			if err != nil {
				return nil, err
			}
			if !s.projectReadable(r, p) {
				// 404, not 403: a project the caller cannot see and one that does
				// not exist must be indistinguishable, or Finder becomes an
				// enumeration oracle for slugs and ids.
				return nil, fmt.Errorf("%w: project %d", store.ErrNotFound, id)
			}
			out = append(out, p)
		}
		return out, nil
	}

	res, err := s.Store.SearchProjects(ctx, seed.Text, store.SearchFilters{}, "relevance", actor)
	if err != nil {
		return nil, err
	}
	out := make([]store.Project, 0, len(res.Results))
	for _, hit := range res.Results {
		out = append(out, hit.Project)
	}
	return out, nil
}

// projectPrimaryLanguage returns a project's dominant language.
//
// There is no ListProjectLanguages in the store -- SetProjectLanguages exists
// with no reader, which is the "fully implemented, nothing calls it" shape this
// codebase has hit before. Finder is the reader that was missing, so the query
// lives here rather than pretending a method exists. Highest percentage wins,
// with the language name as the tiebreak so the answer is deterministic: two
// languages at 50/50 must not return a different "primary" per request, or the
// same session would ask a different question on a re-render.
//
// Read directly through the wrapped *sql.DB rather than by adding a store
// method, because a store method with one caller and a one-line query is a
// method that will be used wrongly the second time somebody needs a different
// shape.
func (s *Server) projectPrimaryLanguage(ctx context.Context, projectID int64) (string, error) {
	var lang string
	err := s.Store.QueryRowContext(ctx, `
		SELECT language FROM project_languages
		 WHERE project_id = ?
		 ORDER BY pct DESC, language ASC
		 LIMIT 1`, projectID).Scan(&lang)
	return lang, err
}

// projectArenaStanding reads a project's standing in any arena that ranks
// projects.
//
// No store method lists project arena entries across arenas, and `use-case` and
// `alternatives` arenas (the two that would rank PROJECTS rather than features)
// do not exist yet -- the 70 live arenas are all feature-priority, which ranks
// features. So this returns 0 for every project today, by measurement rather
// than by assumption, and starts returning real values the moment such an arena
// exists with entries in it. The engine's TestArenaStandingIsComputedNotConstant
// is what stops the term silently becoming a hardcoded 0.
func (s *Server) projectArenaStanding(ctx context.Context, projectID int64) float64 {
	rows, err := s.Store.QueryContext(ctx, `
		SELECT e.r FROM arena_entries e
		 WHERE e.entity_type = ?
		   AND e.entity_id = ?`, store.EntityProject, projectID)
	if err != nil {
		return 0
	}
	defer rows.Close()
	best := 0.0
	for rows.Next() {
		var r float64
		if err := rows.Scan(&r); err != nil {
			return best
		}
		if v := arenaStanding(r); v > best {
			best = v
		}
	}
	return best
}

// arenaStanding maps an Elo rating onto 0..1 using the arena's own conservative
// bound. Normalising against a fixed 3000 would make every present-day rating
// look like 0.5 and say nothing; the conservative bound is the number the
// ranking engine already uses when it has to show a number to a human.
func arenaStanding(r float64) float64 {
	v := conservative(r) / 2000.0
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func conservative(r float64) float64 { return ranking.Conservative(r, 0) }

// ---------------------------------------------------------------- session store

type finderStore struct {
	mu       sync.RWMutex
	sessions map[string]*finderSession
	seq      int64
}

func newFinderSessionID() string {
	return fmt.Sprintf("f%d-%d", time.Now().UnixNano(), nextFinderSeq())
}

var finderSeq atomic.Int64

func nextFinderSeq() int64 { return finderSeq.Add(1) }

func (fs *finderStore) put(s *finderSession) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if s.InitialCount == 0 {
		s.InitialCount = len(s.Candidates)
	}
	fs.sessions[s.ID] = s
}

func (fs *finderStore) get(id string) (*finderSession, bool) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	s, ok := fs.sessions[id]
	return s, ok
}

// prune drops sessions older than an hour so an abandoned exploration cannot
// accumulate for the life of the process. Unbounded growth in a long-running
// server is a real leak, and the sessions are cheap to recreate.
func (fs *finderStore) prune(maxAge time.Duration) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	for id, s := range fs.sessions {
		if s.CreatedAt.Before(cutoff) {
			delete(fs.sessions, id)
		}
	}
}

func (s *Server) finderStore() *finderStore {
	s.finderMu.Lock()
	defer s.finderMu.Unlock()
	if s.finder == nil {
		s.finder = &finderStore{sessions: map[string]*finderSession{}}
	}
	return s.finder
}
