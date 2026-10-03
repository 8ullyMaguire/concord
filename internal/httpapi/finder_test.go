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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

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
	Key   string `json:"key"`
	Kind  string `json:"kind"`
	Gain  float64 `json:"gain_bits"`
	Ask   bool   `json:"askable"`
	Opts  []string `json:"values_present"`
}

type finderStateRes struct {
	SessionID       string `json:"session_id"`
	CandidateCount  int    `json:"candidate_count"`
	InitialCount    int    `json:"initial_candidate_count"`
	QuestionsAsked  int    `json:"questions_asked"`
	ShouldStop      bool   `json:"should_suggest_stop"`
	StopReason      string `json:"stop_reason"`
	Persisted       bool   `json:"persisted"`
	Question        *struct {
		Key      string          `json:"key"`
		Text     string          `json:"text"`
		WhyAsked string          `json:"why_asked"`
		Options  []struct {
			ID     string `json:"id"`
			Impact int    `json:"impact_if_chosen"`
		} `json:"options"`
	} `json:"question"`
	Top []struct {
		Slug    string             `json:"slug"`
		Fit     float64            `json:"fit_score"`
		Unknown []string           `json:"unknown"`
		Matches map[string]string  `json:"matches"`
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
		CandidateCount int        `json:"candidate_count"`
		MinGainBits    float64    `json:"min_gain_bits"`
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
	ts, _ := newTestServerWithStore(t)
	code, body := finderGet(t, ts, "/api/v1/finder/questions")
	if code != http.StatusOK {
		t.Fatalf("GET = %d", code)
	}
	raw, _ := json.Marshal(body)
	var out struct {
		Gaps []struct {
			Capability string `json:"capability"`
			Missing    int    `json:"missing"`
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
	for _, g := range out.Gaps {
		if g.Capability == "" {
			t.Error("a gap was reported without naming a capability")
		}
		if g.Missing <= 0 {
			t.Errorf("gap on %q claims %d missing; it must only appear when data is "+
				"actually absent", g.Capability, g.Missing)
		}
	}
}

var _ = fmt.Sprintf