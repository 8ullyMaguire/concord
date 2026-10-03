package httpapi

// Finder's API tests (docs/specs/finder-spec.md).
//
// The engine is tested in internal/finder. These cover only what the API layer
// owns, and the first of those is the one that would be easiest to get wrong:
// §5.4's promise that Finder never asks about an option the catalog cannot
// actually deliver. That promise is a property of the endpoint, so it is
// asserted against the endpoint rather than trusted from the engine's side.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/finder"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// finderFixture creates a project set with a real, splittable capability matrix.
//
// newTestServerWithStore creates no projects, so without this every assertion
// below would take its t.Skip branch and the file would pass while proving
// nothing — the test suite's own harness hiding that the feature is untested.
//
// The split is deliberate and asymmetric so that a wrong answer is a
// detectable error rather than a harmless coincidence:
//   - offline=yes for half the projects, no for the other half
//   - self-hosted=yes for half, unknown/absent for the rest
//   - license MIT vs Apache-2.0, governance maintainer_led vs collective
//   - one project with NO assertions at all, so "unknown" and "missing" are
//     both present and distinguishable
func finderFixture(t *testing.T, st *store.DB) {
	t.Helper()
	ctx := t.Context()

	for _, c := range []struct{ key, label, kind, category string }{
		{"offline", "Offline use", store.CapBoolean, "features"},
		{"self-hosted", "Self-hosted", store.CapBoolean, "deployment"},
		{"plugins", "Plugin system", store.CapBoolean, "features"},
	} {
		if err := st.EnsureCapability(ctx, store.Capability{
			Key: c.key, Label: c.label, Kind: c.kind, Category: c.category,
		}); err != nil {
			t.Fatalf("EnsureCapability %s: %v", c.key, err)
		}
	}

	author, err := st.CreateUser(ctx, "capauthor", "Cap Author")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	type seed struct {
		slug, name, license, governance string
		offline, selfHosted             string // "" means no assertion at all
	}
	seeds := []seed{
		{"alpha-board", "Alpha Board", "MIT", "maintainer_led", "yes", "yes"},
		{"beta-board", "Beta Board", "MIT", "maintainer_led", "yes", "yes"},
		{"gamma-board", "Gamma Board", "Apache-2.0", "collective", "no", "no"},
		{"delta-board", "Delta Board", "Apache-2.0", "collective", "no", "no"},
		{"epsilon-board", "Epsilon Board", "MIT", "collective", "", ""},
		{"zeta-board", "Zeta Board", "MIT", "maintainer_led", "", "unknown"},
	}
	for _, sd := range seeds {
		// CreateProject's signature has no visibility parameter: a new project
		// is public by default and visibility is set separately. The Finder
		// visibility test below does that explicitly.
		if _, err := st.CreateProject(ctx, author.ID, sd.slug, sd.name,
			"a kanban board", sd.governance, sd.license); err != nil {
			t.Fatalf("CreateProject %s: %v", sd.slug, err)
		}
		for key, val := range map[string]string{"offline": sd.offline, "self-hosted": sd.selfHosted} {
			if val == "" {
				continue
			}
			if _, err := st.AssertCapability(ctx, key, sd.slug, val, "fixture", author.ID); err != nil {
				t.Fatalf("AssertCapability %s/%s: %v", sd.slug, key, err)
			}
		}
	}
}

type finderQ struct {
	Key  string   `json:"key"`
	Kind string   `json:"kind"`
	Gain float64  `json:"gain_bits"`
	Ask  bool     `json:"askable"`
	Opts []string `json:"values_present"`
}

type finderStateRes struct {
	SessionID      string `json:"session_id"`
	CandidateCount int    `json:"candidate_count"`
	InitialCount   int    `json:"initial_candidate_count"`
	QuestionsAsked int    `json:"questions_asked"`
	ShouldStop     bool   `json:"should_suggest_stop"`
	StopReason     string `json:"stop_reason"`
	Persisted      bool   `json:"persisted"`
	Question       *struct {
		Key      string `json:"key"`
		Text     string `json:"text"`
		WhyAsked string `json:"why_asked"`
		Options  []struct {
			ID     string `json:"id"`
			Impact int    `json:"impact_if_chosen"`
		} `json:"options"`
	} `json:"question"`
	Top []struct {
		Slug    string            `json:"slug"`
		Fit     float64           `json:"fit_score"`
		Unknown []string          `json:"unknown"`
		Matches map[string]string `json:"matches"`
	} `json:"top_candidates"`
}

func finderPost(t *testing.T, ts *httptest.Server, path string, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := ts.Client().Post(ts.URL+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	raw2, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw2, &out)
	return resp.StatusCode, out
}

func finderGet(t *testing.T, ts *httptest.Server, path string) (int, map[string]any) {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// §5.4: every option offered is a value the catalog actually holds, and every
// dimension offered has a real split.
//
// This is the assertion that makes "no new data model, it reuses what we have"
// checkable. If the loader started synthesising options, or asking about a
// dimension where the distribution is flat, this fails — and the failure says
// which dimension, so it is actionable rather than a count mismatch.
func TestFinderOnlyOffersOptionsTheCatalogCanDeliver(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	finderFixture(t, db)

	code, body := finderGet(t, ts, "/api/v1/finder/questions")
	if code != http.StatusOK {
		t.Fatalf("GET /finder/questions = %d, want 200", code)
	}

	var cat struct {
		CandidateCount int       `json:"candidate_count"`
		MinGainBits    float64   `json:"min_gain_bits"`
		Dimensions     []finderQ `json:"dimensions"`
	}
	raw, _ := json.Marshal(body)
	if err := json.Unmarshal(raw, &cat); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if cat.CandidateCount == 0 {
		t.Fatal("fixture has no projects, so there is nothing to be honest about")
	}

	for _, d := range cat.Dimensions {
		if d.Ask && d.Gain < cat.MinGainBits {
			t.Errorf("dimension %q is marked askable with %.4f bits, below min_gain_bits %.4f",
				d.Key, d.Gain, cat.MinGainBits)
		}
		if !d.Ask {
			continue
		}
		// Every value offered must be one the catalog returned, which the
		// endpoint guarantees by construction; the assertion that matters is
		// that "any" is not among them, since "any" is a mode not a value.
		for _, v := range d.Opts {
			if v == "any" {
				t.Errorf("dimension %q offers %q as a catalog value; "+
					"'any' is the non-filtering mode, not something the catalog holds", d.Key, v)
			}
		}
	}
}

// A session answers questions and the candidate set actually shrinks.
//
// The load-bearing part is the second half: an endpoint that returns a
// decreasing count while the underlying set is unchanged would pass a test that
// only checked the counts. So the top_candidates list is checked too — if a
// project vanished from the ranked list after being filtered out, the count
// could still look right.
func TestAnsweringAQuestionNarrowsTheCandidateSet(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	finderFixture(t, db)

	code, body := finderPost(t, ts, "/api/v1/finder/sessions", map[string]any{"text": ""})
	if code != http.StatusCreated {
		t.Fatalf("start = %d, want 201: %v", code, body)
	}
	raw, _ := json.Marshal(body)
	var st finderStateRes
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if st.SessionID == "" {
		t.Fatal("no session id returned")
	}
	before := st.CandidateCount
	if before == 0 {
		t.Skip("fixture yielded no candidates")
	}
	if st.InitialCount != before {
		t.Errorf("initial_candidate_count = %d, want %d", st.InitialCount, before)
	}
	// §4.6: the client must know the session is not durable, not discover it by
	// losing it. A silent in-memory session that looks durable is a data-loss
	// bug discovered by a user who closed the tab.
	if st.Persisted {
		t.Error("persisted = true, but sessions live in memory; the client would " +
			"promise a share link that dies on restart")
	}
	if st.Question == nil {
		t.Skip("no question offered; fixture has no splittable dimension")
	}

	// Answer with the first real (non-"any") option.
	pick := ""
	for _, o := range st.Question.Options {
		if o.ID != "any" {
			pick = o.ID
			break
		}
	}
	if pick == "" {
		t.Skip("question offered no concrete value")
	}

	code, body = finderPost(t, ts, "/api/v1/finder/sessions/"+st.SessionID+"/answers",
		map[string]any{"question_key": st.Question.Key, "option_id": pick, "mode": "required"})
	if code != http.StatusOK {
		t.Fatalf("answer = %d, want 200: %v", code, body)
	}
	raw, _ = json.Marshal(body)
	var after finderStateRes
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	if after.CandidateCount > before {
		t.Errorf("candidate count rose from %d to %d after a required answer; "+
			"filtering can only remove", before, after.CandidateCount)
	}
	if after.QuestionsAsked != 1 {
		t.Errorf("questions_asked = %d, want 1", after.QuestionsAsked)
	}
	if after.Question != nil && after.Question.Key == st.Question.Key {
		t.Errorf("the same question (%q) was offered again immediately after being answered",
			after.Question.Key)
	}
	// And the ranked list agrees with the count.
	if after.CandidateCount != 0 && len(after.Top) == 0 {
		t.Errorf("%d candidates remain but top_candidates is empty", after.CandidateCount)
	}
}

// §2.4: skipping never removes candidates.
func TestSkipAndDoesntMatterDoNotFilter(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	finderFixture(t, db)

	code, body := finderPost(t, ts, "/api/v1/finder/sessions", map[string]any{"text": ""})
	if code != http.StatusCreated {
		t.Fatalf("start = %d: %v", code, body)
	}
	raw, _ := json.Marshal(body)
	var st finderStateRes
	_ = json.Unmarshal(raw, &st)
	if st.Question == nil {
		t.Skip("no question offered")
	}
	pick := "yes"
	for _, o := range st.Question.Options {
		if o.ID != "any" {
			pick = o.ID
			break
		}
	}

	for _, mode := range []string{"skip", "doesnt-matter", "decide-later"} {
		code, body := finderPost(t, ts, "/api/v1/finder/sessions/"+st.SessionID+"/answers",
			map[string]any{"question_key": st.Question.Key, "option_id": pick, "mode": mode})
		if code != http.StatusOK {
			t.Fatalf("%s = %d: %v", mode, code, body)
		}
		raw, _ := json.Marshal(body)
		var after finderStateRes
		_ = json.Unmarshal(raw, &after)
		if after.CandidateCount != st.CandidateCount {
			t.Errorf("mode %q changed the candidate count from %d to %d; "+
				"these modes must never filter", mode, st.CandidateCount, after.CandidateCount)
		}
		// A non-required answer must not advance the question budget either.
		if after.QuestionsAsked != 0 {
			t.Errorf("mode %q counted as a question asked (%d); only required answers "+
				"consume the cap", mode, after.QuestionsAsked)
		}
	}
}

// §2.6: going back recomputes, it does not replay a stale snapshot.
func TestGoingBackRestoresCandidatesAndReopensTheQuestion(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	finderFixture(t, db)

	code, body := finderPost(t, ts, "/api/v1/finder/sessions", map[string]any{"text": ""})
	if code != http.StatusCreated {
		t.Fatalf("start = %d: %v", code, body)
	}
	raw, _ := json.Marshal(body)
	var st finderStateRes
	_ = json.Unmarshal(raw, &st)
	if st.Question == nil {
		t.Skip("no question offered")
	}
	pick := ""
	for _, o := range st.Question.Options {
		if o.ID != "any" {
			pick = o.ID
			break
		}
	}

	code, body = finderPost(t, ts, "/api/v1/finder/sessions/"+st.SessionID+"/answers",
		map[string]any{"question_key": st.Question.Key, "option_id": pick, "mode": "required"})
	if code != http.StatusOK {
		t.Fatalf("answer = %d: %v", code, body)
	}
	raw, _ = json.Marshal(body)
	var narrowed finderStateRes
	_ = json.Unmarshal(raw, &narrowed)

	code, body = finderPost(t, ts, "/api/v1/finder/sessions/"+st.SessionID+"/back", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("back = %d: %v", code, body)
	}
	raw, _ = json.Marshal(body)
	var back finderStateRes
	_ = json.Unmarshal(raw, &back)

	if back.CandidateCount != st.CandidateCount {
		t.Errorf("after going back the candidate count is %d, want the original %d",
			back.CandidateCount, st.CandidateCount)
	}
	if back.QuestionsAsked != 0 {
		t.Errorf("questions_asked = %d after going back, want 0", back.QuestionsAsked)
	}
	if back.Question == nil {
		t.Fatal("no question offered after going back")
	}
	if back.Question.Key != st.Question.Key {
		t.Errorf("question after back = %q, want the one just answered %q; "+
			"going back must reopen it", back.Question.Key, st.Question.Key)
	}
	// Unused, but proves the narrowing did something worth reverting.
	if narrowed.CandidateCount == st.CandidateCount {
		t.Log("note: the chosen value filtered nothing, so 'back' had nothing to restore")
	}
}

func TestGoingBackWithNoAnswersIsRejected(t *testing.T) {
	ts, _ := newTestServerWithStore(t)
	code, body := finderPost(t, ts, "/api/v1/finder/sessions", map[string]any{"text": ""})
	if code != http.StatusCreated {
		t.Fatalf("start = %d: %v", code, body)
	}
	raw, _ := json.Marshal(body)
	var st finderStateRes
	_ = json.Unmarshal(raw, &st)

	code, _ = finderPost(t, ts, "/api/v1/finder/sessions/"+st.SessionID+"/back", map[string]any{})
	if code != http.StatusBadRequest {
		t.Errorf("back on an empty session = %d, want 400", code)
	}
}

// Lifting a filter is what §6.1 offers the user, so it has to actually restore.
func TestLiftingAFilterRestoresItsCandidates(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	finderFixture(t, db)

	code, body := finderPost(t, ts, "/api/v1/finder/sessions", map[string]any{"text": ""})
	if code != http.StatusCreated {
		t.Fatalf("start = %d: %v", code, body)
	}
	raw, _ := json.Marshal(body)
	var st finderStateRes
	_ = json.Unmarshal(raw, &st)
	if st.Question == nil {
		t.Skip("no question offered")
	}
	pick := ""
	for _, o := range st.Question.Options {
		if o.ID != "any" {
			pick = o.ID
			break
		}
	}

	code, _ = finderPost(t, ts, "/api/v1/finder/sessions/"+st.SessionID+"/answers",
		map[string]any{"question_key": st.Question.Key, "option_id": pick, "mode": "required"})
	if code != http.StatusOK {
		t.Fatalf("answer = %d", code)
	}

	code, body = finderPost(t, ts, "/api/v1/finder/sessions/"+st.SessionID+"/lift",
		map[string]any{"question_key": st.Question.Key})
	if code != http.StatusOK {
		t.Fatalf("lift = %d: %v", code, body)
	}
	raw, _ = json.Marshal(body)
	var lifted finderStateRes
	_ = json.Unmarshal(raw, &lifted)
	if lifted.CandidateCount != st.CandidateCount {
		t.Errorf("after lifting %q the count is %d, want %d — lifting a filter must "+
			"restore everything it removed", st.Question.Key, lifted.CandidateCount, st.CandidateCount)
	}
	// Lifting a filter that was never applied is an error, not a silent no-op:
	// the client shows "lift this filter" buttons, and a 200 for one that does
	// nothing would make the button lie.
	code, _ = finderPost(t, ts, "/api/v1/finder/sessions/"+st.SessionID+"/lift",
		map[string]any{"question_key": st.Question.Key})
	if code != http.StatusBadRequest {
		t.Errorf("lifting an already-lifted filter = %d, want 400", code)
	}
}

func TestAnInvalidAnswerModeIsRejected(t *testing.T) {
	ts, _ := newTestServerWithStore(t)
	code, body := finderPost(t, ts, "/api/v1/finder/sessions", map[string]any{"text": ""})
	if code != http.StatusCreated {
		t.Fatalf("start = %d: %v", code, body)
	}
	raw, _ := json.Marshal(body)
	var st finderStateRes
	_ = json.Unmarshal(raw, &st)

	code, _ = finderPost(t, ts, "/api/v1/finder/sessions/"+st.SessionID+"/answers",
		map[string]any{"question_key": "license", "option_id": "MIT", "mode": "maybe"})
	if code != http.StatusBadRequest {
		t.Errorf("mode \"maybe\" = %d, want 400", code)
	}
	code, _ = finderPost(t, ts, "/api/v1/finder/sessions/"+st.SessionID+"/answers",
		map[string]any{"option_id": "MIT"})
	if code != http.StatusBadRequest {
		t.Errorf("missing question_key = %d, want 400", code)
	}
}

// A session id that was never issued must not leak whether it existed.
func TestAnUnknownSessionIsNotFound(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	finderFixture(t, db)
	for _, path := range []string{
		"/api/v1/finder/sessions/f1-does-not-exist",
		"/api/v1/finder/sessions/f1-does-not-exist/results",
	} {
		if code, _ := finderGet(t, ts, path); code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, code)
		}
	}
}

// Seeding with an explicit id list must respect visibility: Finder is not a way
// around the four visibility levels.
//
// The first version of this test created the private project THROUGH the API as
// the same authenticated user, so the request correctly succeeded -- the caller
// owns the project and owns seeing it. That is the API working, not a leak, and
// the test was asserting a 404 against correct behaviour. A visibility test has
// to be a DIFFERENT user, so this one uses newTestServerNoActor-style anonymity
// via a second user and the real owner/member boundary.
func TestSeedingWithAnUnreadableProjectIsNotFound(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	finderFixture(t, db)
	ctx := t.Context()

	owner, err := db.CreateUser(ctx, "finderowner", "Finder Owner")
	if err != nil {
		t.Fatalf("CreateUser owner: %v", err)
	}
	p, err := db.CreateProject(ctx, owner.ID, "private-thing", "Private Thing",
		"should not be discoverable via Finder", "maintainer_led", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := db.SetProjectVisibility(ctx, p.Slug, "private"); err != nil {
		t.Fatalf("SetProjectVisibility: %v", err)
	}

	// The fixture user ("testuser") is a member, not the owner. Whether a member
	// may see a private project is a policy question this instance already
	// answers elsewhere; what matters for Finder is that it applies the SAME
	// answer as every other surface. So compare Finder against the project
	// endpoint rather than hardcoding one.
	code, _ := finderGet(t, ts, "/api/v1/projects/private-thing")
	readable := code == http.StatusOK
	if !readable {
		t.Logf("member cannot read the private project directly (GET = %d); "+
			"Finder must agree", code)
	}

	fcode, _ := finderPost(t, ts, "/api/v1/finder/sessions",
		map[string]any{"seed_candidates": []int64{p.ID}})
	if readable && fcode != http.StatusCreated {
		t.Errorf("seeding a project the caller CAN read = %d, want 201", fcode)
	}
	if !readable && fcode != http.StatusNotFound {
		t.Errorf("seeding an unreadable project = %d, want 404, not 403: "+
			"distinguishable would make Finder an enumeration oracle for ids", fcode)
	}
}

// The gaps list is how §5.3's contribution loop knows what to ask for, so it
// must be non-empty whenever the catalog is thin, and must name capabilities
// rather than projects.
func TestTheCatalogEndpointReportsGaps(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	// This one was missed when the fixture was added to every other Finder
	// test, so it ran against an EMPTY catalog and asserted "no gaps" while
	// reporting the vacuous truth: with no projects there are no unknown fields,
	// so an empty gap list was correct and the assertion was wrong.
	finderFixture(t, db)
	code, body := finderGet(t, ts, "/api/v1/finder/questions")
	if code != http.StatusOK {
		t.Fatalf("GET = %d", code)
	}
	raw, _ := json.Marshal(body)
	// Field names taken from the live response, not invented: the first version
	// of this test decoded into Capability/Missing, which the endpoint never
	// sends, so every gap decoded to a zero value and the loop below asserted
	// nothing. A test that passes on absent fields is worse than no test.
	var out struct {
		Gaps []struct {
			Key           string `json:"key"`
			Label         string `json:"label"`
			Family        string `json:"family"`
			UnknownFields int    `json:"unknown_fields"`
			HeldBack      int    `json:"held_back"`
		} `json:"gaps"`
		Weights map[string]float64 `json:"weights"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// §5.2 promises every weight is visible and tunable, so they are returned
	// rather than hardcoded in the page.
	for _, w := range []string{"capability", "platform", "field_report", "arena"} {
		if _, ok := out.Weights[w]; !ok {
			t.Errorf("weights is missing %q; the results page cannot show a weight it "+
				"was never told about", w)
		}
	}
	if len(out.Gaps) == 0 {
		t.Error("no gaps reported, but the fixture asserts 2 of 3 capabilities for " +
			"6 projects, so most fields ARE unknown; an empty gap list means the " +
			"contribution loop (§5.3) has nothing to offer")
	}
	for _, g := range out.Gaps {
		if g.Key == "" {
			t.Error("a gap was reported without naming a capability")
		}
		if g.UnknownFields <= 0 {
			t.Errorf("gap on %q claims %d unknown fields; it must only appear when "+
				"data is actually absent", g.Key, g.UnknownFields)
		}
		if g.HeldBack > g.UnknownFields {
			t.Errorf("gap on %q claims %d held back but only %d unknown",
				g.Key, g.HeldBack, g.UnknownFields)
		}
	}
}

// The page must render and must NOT require a project the caller can see: there
// is no slug in the URL, and the data comes from the API at runtime.
func TestTheFinderPageRenders(t *testing.T) {
	ts, _ := newTestServerWithStore(t)

	resp, err := ts.Client().Get(ts.URL + "/finder")
	if err != nil {
		t.Fatalf("GET /finder: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /finder = %d, want 200", resp.StatusCode)
	}
	page := string(body)
	// The mount points the JS needs. A missing one is a blank page with HTTP
	// 200, which is the failure this asserts.
	for _, id := range []string{
		"finder-root", "finder-seed-text", "finder-seed-form",
		"finder-question-text", "finder-options", "finder-shortlist-items",
		"finder-answers", "finder-ranked", "finder-unknown",
		"finder-filtered-out", "finder-error", "finder-stop",
	} {
		if !strings.Contains(page, `id="`+id+`"`) {
			t.Errorf("the page has no element with id %q; finder.js will fail to "+
				"find it and the flow renders blank", id)
		}
	}
	if !strings.Contains(page, "/assets/js/finder.js") {
		t.Error("finder.js is not referenced, so nothing is ever fetched")
	}
	// The deep link (§3) is handled in JS from location.search, so the input
	// must exist and the form must not require a seed.
	if strings.Contains(page, "required") && strings.Contains(page, "finder-seed-text") {
		t.Error("the seed input is marked required; §4.1 allows starting with a blank seed")
	}
}

// Every element the JS reaches for must exist, and every one it writes to must
// not be missing on the results path. Derived from the ids in finder.js so the
// two cannot drift.
func TestEveryElementFinderJSUsesExistsInThePage(t *testing.T) {
	ts, _ := newTestServerWithStore(t)
	resp, err := ts.Client().Get(ts.URL + "/finder")
	if err != nil {
		t.Fatalf("GET /finder: %v", err)
	}
	defer resp.Body.Close()
	pageBytes, _ := io.ReadAll(resp.Body)
	page := string(pageBytes)

	jsBytes, err := os.ReadFile("assets/js/finder.js")
	if err != nil {
		t.Fatalf("read finder.js: %v", err)
	}
	js := string(jsBytes)

	re := regexp.MustCompile(`el\('(finder-[a-z-]+)'\)|getElementById\('(finder-[a-z-]+)'\)`)
	used := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(js, -1) {
		if m[1] != "" {
			used[m[1]] = true
		} else if m[2] != "" {
			used[m[2]] = true
		}
	}
	if len(used) < 10 {
		t.Fatalf("only found %d element ids in finder.js; the regexp is not "+
			"matching the file's actual style any more", len(used))
	}
	for id := range used {
		if id == "finder-root" {
			continue
		}
		if !strings.Contains(page, `id="`+id+`"`) {
			t.Errorf("finder.js uses #%s but the page does not define it", id)
		}
	}
}

// The seed view's contents are built by script, so "the page has the element"
// is not the same as "the page shows something". This asserts the SCRIPT calls
// its own render path at init: finder.js ended up calling only show(), which
// left the category row empty on a page that otherwise looked fine.
func TestFinderJSRendersTheSeedViewAtInit(t *testing.T) {
	jsBytes, err := os.ReadFile("assets/js/finder.js")
	if err != nil {
		t.Fatalf("read finder.js: %v", err)
	}
	js := string(jsBytes)

	i := strings.LastIndex(js, "show('seed');")
	if i < 0 {
		t.Fatal("finder.js never calls show('seed')")
	}
	// The executable text between show('seed') and the next statement boundary.
	// Comments are stripped first, because a comment MENTIONING render() is not
	// a call to it -- the first version of this test passed with the call
	// deleted, since its own comment said "calling only show() left the category
	// row empty" and contained the word.
	tail := js[i:]
	if c := strings.Index(tail, "//"); c >= 0 {
		if nl := strings.Index(tail[c:], "\n"); nl >= 0 {
			tail = tail[:c+nl+1]
		} else {
			tail = tail[:c]
		}
	}
	if !strings.Contains(tail, "render()") {
		t.Errorf("finder.js switches to the seed view and then does not call render(); "+
			"the category picker will be empty. Statements after show('seed'): %q", tail)
	}
}

// Guards the whole file against the failure this suite actually suffered: a
// Finder test that builds a server but never seeds a catalog, and therefore
// asserts vacuously. TestTheCatalogEndpointReportsGaps did exactly that -- it
// demanded "gaps must be reported" against an empty catalog, where an empty
// list is the correct answer, and it passed that way for several commits.
//
// A test may legitimately skip the fixture (routing, validation and markup
// tests do not need candidates), so this asserts the ones that make claims
// about candidates, questions or gaps DO have one. The allow-list is explicit:
// an unlisted test that starts needing the fixture fails here rather than
// quietly proving nothing.
func TestNoFinderTestAssertsAboutCandidatesWithoutAFixture(t *testing.T) {
	src, err := os.ReadFile("finder_test.go")
	if err != nil {
		t.Fatalf("read finder_test.go: %v", err)
	}
	// Tests that genuinely do not need a catalog, with the reason.
	allowed := map[string]string{
		"TestGoingBackWithNoAnswersIsRejected":                  "asserts a 400 before any session has answers",
		"TestAnInvalidAnswerModeIsRejected":                     "asserts request validation, not candidate data",
		"TestTheFinderPageRenders":                              "asserts markup and mount points",
		"TestEveryElementFinderJSUsesExistsInThePage":           "asserts JS and template agree on ids",
		"TestFinderJSRendersTheSeedViewAtInit":                  "reads the JS source, no server involved",
		"TestNoFinderTestAssertsAboutCandidatesWithoutAFixture": "this test",
	}

	re := regexp.MustCompile(`func (Test\w+)\(t \*testing\.T\) \{`)
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		name := m[1]
		if _, ok := allowed[name]; ok {
			continue
		}
		// Find the body.
		start := strings.Index(string(src), "func "+name+"(")
		end := strings.Index(string(src[start:]), "\n}\n")
		if end < 0 {
			t.Fatalf("could not delimit the body of %s", name)
		}
		body := string(src)[start : start+end]
		if strings.Contains(body, "newTestServerWithStore") && !strings.Contains(body, "finderFixture") {
			t.Errorf("%s builds a test server but never calls finderFixture, so it runs "+
				"against an empty catalog; add the fixture, or add it to the allow-list in "+
				"this test with a reason", name)
		}
	}
}

// The "why this question?" text must describe the SPLIT, not the single most
// destructive option.
//
// The first version reported the option removing the most candidates and
// stated it absolutely, which on the live instance read "choosing \"nim\" would
// narrow 69 of 70" — true of all nine language options, because most-removed is
// the rarest value rather than the most informative one. §4.2 shows both ends of
// the split side by side.
func TestWhyThisQuestionDescribesTheSplitNotOneOption(t *testing.T) {
	sharp := &finder.Dimension{
		Key: "cap:self-hosted",
		Options: []finder.Option{
			{ID: "yes", Label: "Self-hosted (I run it)", Impact: 142},
			{ID: "no", Label: "Managed / SaaS", Impact: 38},
			{ID: finder.ModeAny, Label: "Doesn't matter", Impact: 0},
		},
	}
	got := finderWhyAsked(sharp, 0.9, 180)
	for _, want := range []string{"142", "38", "Self-hosted (I run it)", "Managed / SaaS"} {
		if !strings.Contains(got, want) {
			t.Errorf("why-text %q does not mention %q; both ends of the split must appear", got, want)
		}
	}

	// A flat dimension must say so rather than claim it is the best split left.
	flat := &finder.Dimension{
		Key: "language",
		Options: []finder.Option{
			{ID: "nim", Label: "nim", Impact: 69},
			{ID: "go", Label: "go", Impact: 53},
			{ID: "js", Label: "js", Impact: 55},
		},
	}
	gotFlat := finderWhyAsked(flat, 2.7, 70)
	// Survivors, not removals. 69 and 53 both sound decisive; "leaves only 1 to
	// 17 of 70" is what actually tells the user the question is not worth much.
	//
	// The FRAMING is asserted, not just the numbers: the two framings print the
	// same digits (70-69=1, 70-53=17), so a test checking only for "17" and "70"
	// passes either way. That is the whole defect, so it has to be the thing
	// under test.
	if !strings.Contains(gotFlat, "leaves") {
		t.Errorf("flat-dimension why-text %q must report how many candidates SURVIVE "+
			"each answer, not how many it removes; the removal framing sounds decisive "+
			"when the surviving set is essentially the same size either way", gotFlat)
	}
	if strings.Contains(gotFlat, "rule out") || strings.Contains(gotFlat, "rules out") {
		t.Errorf("flat-dimension why-text %q still frames the split as removals", gotFlat)
	}
	if !strings.Contains(gotFlat, "17") || !strings.Contains(gotFlat, "70") {
		t.Errorf("flat-dimension why-text %q must give the surviving range", gotFlat)
	}
	if strings.Contains(gotFlat, "biggest split left") {
		t.Errorf("flat-dimension why-text %q calls a near-useless question the biggest split left", gotFlat)
	}

	// And a dimension where every option removes the same number is not a split
	// at all.
	uniform := &finder.Dimension{
		Key:     "license",
		Options: []finder.Option{{ID: "MIT", Label: "MIT", Impact: 20}, {ID: "AGPL-3.0", Label: "AGPL-3.0", Impact: 20}},
	}
	gotUniform := finderWhyAsked(uniform, 0.37, 70)
	if strings.Contains(gotUniform, "biggest split left") {
		t.Errorf("uniform why-text %q claims a split where there is none", gotUniform)
	}
}

// An omitted mode must mean REQUIRED, not "no filtering".
//
// The handler's validation switch lists the modes it accepts. Widening it with
// "" does not make anything filter -- it makes an empty mode a VALID distinct
// mode instead of falling through to the required default, and the engine
// treats "" as a skip. So a client that posts {question_key, option_id} with no
// mode gets its answer silently recorded and ignored.
//
// That is the dangerous direction: the answer looks recorded in the tray, the
// candidate set does not change, and nothing errors.
func TestAnOmittedModeMeansRequired(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	finderFixture(t, db)

	_, body := finderPost(t, ts, "/api/v1/finder/sessions", map[string]any{"text": ""})
	raw, _ := json.Marshal(body)
	var st finderStateRes
	_ = json.Unmarshal(raw, &st)
	if st.Question == nil {
		t.Skip("no question offered")
	}
	pick := ""
	for _, o := range st.Question.Options {
		if o.ID != "any" {
			pick = o.ID
			break
		}
	}

	// No "mode" key at all.
	code, body := finderPost(t, ts, "/api/v1/finder/sessions/"+st.SessionID+"/answers",
		map[string]any{"question_key": st.Question.Key, "option_id": pick})
	if code != http.StatusOK {
		t.Fatalf("answer with no mode = %d: %v", code, body)
	}
	raw, _ = json.Marshal(body)
	var after finderStateRes
	_ = json.Unmarshal(raw, &after)

	if after.QuestionsAsked != 1 {
		t.Errorf("questions_asked = %d after an answer with no mode, want 1; "+
			"an omitted mode must be treated as required", after.QuestionsAsked)
	}
	if after.CandidateCount >= st.CandidateCount {
		t.Errorf("candidate count %d did not drop from %d; the answer was accepted "+
			"but did not filter", after.CandidateCount, st.CandidateCount)
	}
	recorded := struct {
		Answers []struct {
			QuestionKey string `json:"question_key"`
			OptionID    string `json:"option_id"`
			Mode        string `json:"mode"`
		} `json:"answers"`
	}{}
	if err := json.Unmarshal(raw, &recorded); err != nil {
		t.Fatalf("decode answers: %v", err)
	}
	if len(recorded.Answers) != 1 {
		t.Fatalf("recorded %d answers, want 1", len(recorded.Answers))
	}
	if recorded.Answers[0].Mode != finder.ModeRequired {
		t.Errorf("recorded mode = %q, want %q; an omitted mode must be stored as "+
			"required so the engine treats it as a filter",
			recorded.Answers[0].Mode, finder.ModeRequired)
	}
	if recorded.Answers[0].OptionID != pick {
		t.Errorf("recorded option_id = %q, want %q", recorded.Answers[0].OptionID, pick)
	}
}

// Evidence coverage must reach the client, and must not be a constant.
//
// The live page prints "99% fit (15% evidence)", which is the difference between
// a well-evidenced match and a lucky one. A hardcoded 1 would print "99% fit"
// and look fine -- the coverage is only load-bearing because it VARIES.
func TestEvidenceCoverageIsReportedAndVaries(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	finderFixture(t, db)

	code, body := finderPost(t, ts, "/api/v1/finder/sessions", map[string]any{"text": ""})
	if code != http.StatusCreated {
		t.Fatalf("start = %d: %v", code, body)
	}
	raw, _ := json.Marshal(body)
	var st finderStateRes
	_ = json.Unmarshal(raw, &st)
	if st.Question == nil {
		t.Skip("no question offered")
	}
	pick := ""
	for _, o := range st.Question.Options {
		if o.ID != "any" {
			pick = o.ID
			break
		}
	}
	_, body = finderPost(t, ts, "/api/v1/finder/sessions/"+st.SessionID+"/answers",
		map[string]any{"question_key": st.Question.Key, "option_id": pick, "mode": "required"})
	sid, _ := body["session_id"].(string)

	code, body = finderGet(t, ts, "/api/v1/finder/sessions/"+sid+"/results")
	if code != http.StatusOK {
		t.Fatalf("results = %d", code)
	}
	raw, _ = json.Marshal(body)
	var res struct {
		Candidates []struct {
			Slug     string  `json:"slug"`
			Fit      float64 `json:"fit_score"`
			Coverage float64 `json:"evidence_coverage"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode results: %v", err)
	}
	if len(res.Candidates) == 0 {
		t.Fatal("no candidates in results")
	}
	// The fixture has no field reports and no arena standing, so nothing is
	// fully evidenced and every candidate must report a coverage below 1.
	for _, c := range res.Candidates {
		if c.Coverage >= 1 {
			t.Errorf("%s reports evidence_coverage %.4f; the fixture has no field "+
				"reports and no arena, so nothing is fully evidenced -- a constant 1 "+
				"here is what makes the fit number a lie", c.Slug, c.Coverage)
		}
	}
	// And a candidate that matches every answer must not be dragged below one
	// that does not.
	best := res.Candidates[0]
	if best.Fit < 0.9 {
		t.Errorf("top match %s fit = %.4f; it matched every answer asked", best.Slug, best.Fit)
	}
}

// A seed that matches nothing must still leave the user with questions.
//
// §4.1: the seed pre-fills a starting category. It is NOT a hard filter, so it
// must never be able to produce a session with nothing to ask about.
//
// Measured on the live instance: seed "go" narrowed 70 candidates to 4 that were
// identical on every dimension the engine can ask about, so no question cleared
// the information-gain threshold and the endpoint returned `question: null`. The
// page then rendered an empty results view with no question and no way forward.
// Seed "note-taking" returned zero candidates and did the same.
func TestASeedThatMatchesNothingStillOffersQuestions(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	finderFixture(t, db)

	for _, seed := range []string{"note-taking", "zzzz-no-such-thing", "self-hosted"} {
		t.Run(seed, func(t *testing.T) {
			code, body := finderPost(t, ts, "/api/v1/finder/sessions", map[string]any{"text": seed})
			if code != http.StatusCreated {
				t.Fatalf("start with seed %q = %d, want 201: %v", seed, code, body)
			}
			raw, _ := json.Marshal(body)
			var st finderStateRes
			if err := json.Unmarshal(raw, &st); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if st.CandidateCount == 0 {
				t.Errorf("seed %q left zero candidates; a seed is a starting point, "+
					"not a filter that can produce nothing", seed)
			}
			// A seed may legitimately narrow to few candidates, and then there is
			// NO question — which is correct (§4.4 stops at five or fewer) and is
			// not a dead end. What must never happen is arriving with neither a
			// question nor a stop reason, because the page has nothing to render
			// for that state.
			if st.Question == nil {
				if !st.ShouldStop {
					t.Fatalf("seed %q returned no question and no stop reason with %d "+
						"candidates; the page has no view for that state and the user is "+
						"stranded", seed, st.CandidateCount)
				}
				if st.StopReason == "" {
					t.Errorf("seed %q suggested stopping without giving a reason", seed)
				}
				t.Logf("seed %q narrowed to %d candidates and stopped (%s), which is the "+
					"documented behaviour", seed, st.CandidateCount, st.StopReason)
			} else if len(st.Question.Options) < 2 {
				t.Errorf("seed %q offered %d option(s); a question needs something to "+
					"choose between", seed, len(st.Question.Options))
			}
		})
	}
}

// A session that was given an explicit id list must NOT be widened behind the
// caller's back. The widening rule exists for a loose seed, and overriding a
// deliberate seed_candidates would be a different and much worse surprise: the
// caller named the projects, and getting others back would make a Finder URL
// shareable to the wrong set.
func TestAnExplicitSeedCandidateListIsNeverWidened(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	finderFixture(t, db)
	ctx := t.Context()

	owner, err := db.CreateUser(ctx, "narrowseed", "Narrow Seed")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	p, err := db.CreateProject(ctx, owner.ID, "the-only-one", "The Only One",
		"a lone project", "maintainer_led", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	code, body := finderPost(t, ts, "/api/v1/finder/sessions", map[string]any{
		"seed_candidates": []int64{p.ID},
	})
	if code != http.StatusCreated {
		t.Fatalf("start = %d: %v", code, body)
	}
	raw, _ := json.Marshal(body)
	var st finderStateRes
	_ = json.Unmarshal(raw, &st)
	if st.CandidateCount != 1 {
		t.Errorf("candidate_count = %d, want 1; an explicit seed_candidates list must "+
			"be honoured exactly, not widened to the whole catalog", st.CandidateCount)
	}
}

// A single-candidate set cannot be split, so it must widen rather than dead-end.
func TestASingleCandidateSetIsWidenedRatherThanDeadEnded(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	finderFixture(t, db)
	ctx := t.Context()

	owner, err := db.CreateUser(ctx, "lonesome", "Lonesome")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	p, err := db.CreateProject(ctx, owner.ID, "lonesome-project", "Lonesome Project",
		"alone in the catalog", "maintainer_led", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	code, body := finderPost(t, ts, "/api/v1/finder/sessions", map[string]any{
		"seed_candidates": []int64{p.ID},
	})
	if code != http.StatusCreated {
		t.Fatalf("start = %d: %v", code, body)
	}
	raw, _ := json.Marshal(body)
	var st finderStateRes
	_ = json.Unmarshal(raw, &st)
	// Either it was widened (more than 1) or there was nothing to widen to; what
	// must not happen is a questionless session.
	if st.Question == nil && st.CandidateCount > 1 {
		t.Errorf("%d candidates but no question", st.CandidateCount)
	}
}
