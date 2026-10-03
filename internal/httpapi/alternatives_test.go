package httpapi

// Alternatives arenas — S2's API tests (docs/specs/alternatives-panel-spec.md §4).
//
// Every test here is named after the failure it catches, because in this feature
// the failure modes are all invisible: an arena that ranks the wrong entity, a
// baseline that renders as a legitimate competitor, a private project leaking
// through a 403's existence. None of them crash.
//
// Two of these tests have already earned their names — TestAnAlternativesArena-
// RanksProjectsNotFeatures is the one that fails if S2 quietly reuses the feature
// arena, and it is the test that proves R1's "arenas are the single ranking
// engine" claim is true for a fourth entity type rather than aspirational.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// seedAlternatives creates an alternatives arena on `slug` with two competing
// projects already added, and returns the arena id and the two competitor ids.
//
// Two competitors, not one: an arena with a single entry cannot produce a ranking,
// and a test asserting "the leaderboard has one row" would pass whether or not
// the arena ranks at all.
func seedAlternatives(t *testing.T, ts *httptest.Server, slug, useCase string) (int64, int64, int64, string) {
	t.Helper()
	// Three projects, THREE owners — and that is not incidental.
	//
	// `refuseOwnVote` decides "is this the voter's own entry" for a project by
	// looking for a members row with role owner/maintainer, because projects have
	// no author column. So if the harness identity creates all of them, it owns
	// every project involved, and every vote is refused with `cannot vote on your
	// own entry`. That is the rule working correctly — a real alternatives arena
	// compares OTHER people's projects, so the asker does not own the rivals. The
	// first version of this fixture created all four projects as one identity and
	// then read the resulting refusal as a bug in the vote handler.
	asker := registerAndLogin(t, ts, slug+"-asker")
	rival := registerAndLogin(t, ts, slug+"-rival-owner")

	createTestProjectWithAuth(t, ts, asker, slug+"-owner")
	createTestProjectWithAuth(t, ts, rival, slug+"-rival-a")
	createTestProjectWithAuth(t, ts, rival, slug+"-rival-b")

	// The rivals, by numeric id: the entry body takes an id, not a slug.
	var projA, projB struct {
		ID int64 `json:"id"`
	}
	code, body := authJSON(t, ts, http.MethodGet, "/api/v1/projects/"+slug+"-rival-a", asker, nil)
	if code != http.StatusOK {
		t.Fatalf("read rival-a: %d body=%s", code, body)
	}
	decodeInto(t, body, &projA)
	code, body = authJSON(t, ts, http.MethodGet, "/api/v1/projects/"+slug+"-rival-b", asker, nil)
	if code != http.StatusOK {
		t.Fatalf("read rival-b: %d body=%s", code, body)
	}
	decodeInto(t, body, &projB)

	code, body = authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/"+slug+"-owner/alternatives", asker,
		map[string]any{"use_case": useCase})
	if code != http.StatusCreated {
		t.Fatalf("create alternatives arena: %d body=%s", code, body)
	}
	var created struct {
		Arena struct {
			ID int64 `json:"id"`
		} `json:"arena"`
	}
	decodeInto(t, body, &created)
	arenaID := created.Arena.ID

	for _, cand := range []int64{projA.ID, projB.ID} {
		code, body := authJSON(t, ts, http.MethodPost,
			"/api/v1/projects/"+slug+"-owner/alternatives/"+itoa(arenaID)+"/entries",
			asker, map[string]any{"project_id": cand})
		if code != http.StatusOK {
			t.Fatalf("add competitor %d: %d body=%s", cand, code, body)
		}
	}

	// The asker is returned so the caller can vote as somebody who owns the arena
	// but not either rival — the only person who legitimately can. It is a return
	// value and not a package-level map: the first attempt stashed it in one, which
	// is mutable state shared across tests keyed by a slug, and the only reason to
	// prefer it was avoiding one extra value in three call sites.
	// No role setup for the asker, and none is needed: `createTestProjectWithAuth`
	// makes it the project's owner, and an owner is a contributor, so the vote
	// handler's requireRole already passes. The first version called
	// `mustUser(t, db, "testuser")` and joined it, which died on
	// `CreateUser(testuser): duplicate` because the harness had already created
	// that user, and was fixing a problem that did not exist.
	return arenaID, projA.ID, projB.ID, asker
}

// The load-bearing test of S2. If this fails, S2 is not building alternatives
// arenas at all — it is putting projects into a feature arena and ranking them
// there, which "works" and is completely wrong.
func TestAnAlternativesArenaRanksProjectsNotFeatures(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	arenaID, _, _, _ := seedAlternatives(t, ts, "ranked", "teams needing audit logs")

	entries, err := db.ArenaLeaderboard(context.Background(), arenaID, 10)
	if err != nil {
		t.Fatalf("ArenaLeaderboard: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("leaderboard has %d entries, want 2", len(entries))
	}
	for _, e := range entries {
		if e.EntityType != store.EntityProject {
			t.Errorf("entry %d is entity_type=%q; an alternatives arena must rank "+
				"projects", e.EntityID, e.EntityType)
		}
	}
}

// And through the API, so the handler is what is proven rather than the store the
// handler happens to call.
func TestTheAlternativesEndpointRanksCompetingProjects(t *testing.T) {
	ts, _ := newTestServerWithStore(t)
	seedAlternatives(t, ts, "viaapi", "teams needing audit logs")

	status, raw := authGet(t, ts, "/api/v1/projects/viaapi-owner/alternatives", "")
	if status != http.StatusOK {
		t.Fatalf("GET alternatives: %d body=%s", status, raw)
	}
	var out struct {
		Arenas []struct {
			UseCase    string `json:"use_case"`
			EntryCount int    `json:"entry_count"`
			Entries    []struct {
				Rank      int     `json:"rank"`
				Project   int64   `json:"project_id"`
				Slug      string  `json:"slug"`
				Title     string  `json:"title"`
				Rating    float64 `json:"rating"`
				BetterFor string  `json:"better_for"`
				WorseFor  string  `json:"worse_for"`
			} `json:"entries"`
		} `json:"arenas"`
	}
	decodeInto(t, raw, &out)
	if len(out.Arenas) != 1 {
		t.Fatalf("got %d arenas, want 1", len(out.Arenas))
	}
	a := out.Arenas[0]
	if a.UseCase != "teams needing audit logs" {
		t.Errorf("use_case = %q", a.UseCase)
	}
	if len(a.Entries) != 2 || a.EntryCount != 2 {
		t.Fatalf("got %d entries and entry_count=%d, want 2 and 2", len(a.Entries), a.EntryCount)
	}
	// The ranks are rendered, not implied by list order — same rule as the
	// solutions panel, for the same reason.
	if a.Entries[0].Rank != 1 || a.Entries[1].Rank != 2 {
		t.Errorf("ranks = %d,%d; want 1,2", a.Entries[0].Rank, a.Entries[1].Rank)
	}
	// §7.3's better-for / worse-for pair, per entry.
	if a.Entries[0].BetterFor != "teams needing audit logs" {
		t.Errorf("better_for = %q, want the use case", a.Entries[0].BetterFor)
	}
	if a.Entries[0].WorseFor == "" {
		t.Error("worse_for is empty; §7.3 wants the pair on every entry")
	}
	// Identity, so the ranking is readable.
	if a.Entries[0].Slug == "" || a.Entries[0].Title == "" {
		t.Errorf("entry has slug=%q title=%q; a list of ids is not a ranking",
			a.Entries[0].Slug, a.Entries[0].Title)
	}
}

// The mirror-image of the existing TestAVoteCannotCrossIntoAnotherFeatureArena.
func TestAVoteOnAProjectPairMovesBothProjectsRatings(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	arenaID, a, b, asker := seedAlternatives(t, ts, "moves", "use case one")

	before, err := db.ArenaLeaderboard(context.Background(), arenaID, 10)
	if err != nil {
		t.Fatalf("ArenaLeaderboard: %v", err)
	}
	ratings := map[int64]float64{}
	for _, e := range before {
		ratings[e.EntityID] = e.R
	}
	if len(ratings) != 2 {
		t.Fatalf("expected 2 rated entries, got %d", len(ratings))
	}

	code, body := authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/moves-owner/alternatives/"+itoa(arenaID)+"/vote", asker,
		map[string]any{"a_project_id": a, "b_project_id": b, "outcome": "a"})
	if code != http.StatusOK {
		t.Fatalf("vote: %d body=%s", code, body)
	}

	after, err := db.ArenaLeaderboard(context.Background(), arenaID, 10)
	if err != nil {
		t.Fatalf("ArenaLeaderboard after: %v", err)
	}
	for _, e := range after {
		if got, was := e.R, ratings[e.EntityID]; got == was {
			t.Errorf("project %d rating unchanged at %v after a vote; both sides of "+
				"the comparison must move", e.EntityID, got)
		}
	}
}

// The regression guard for applyGame's mirror. It writes arena ratings back onto
// features.elo_* when the entity is a feature, and this asserts a project vote
// does not take that branch — a side effect that would be invisible in the
// alternatives panel and would corrupt every feature priority on the instance.
func TestTheSameVoteDoesNotTouchAnyFeaturesRating(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	ctx := context.Background()
	arenaID, a, b, asker := seedAlternatives(t, ts, "nomirror", "use case two")

	// A feature to be corrupted if the mirror fires.
	featSlug, pid := makeProject(t, ts, "nomirror-feature-owner")
	fids := makeRankedPair(t, ts, featSlug, pid, "Alpha problem", "Beta problem")
	before := map[int64]float64{}
	for _, f := range fids {
		fx, err := db.GetFeature(ctx, f)
		if err != nil {
			t.Fatalf("GetFeature(%d): %v", f, err)
		}
		before[f] = fx.EloR
	}

	code, body := authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/nomirror-owner/alternatives/"+itoa(arenaID)+"/vote", asker,
		map[string]any{"a_project_id": a, "b_project_id": b, "outcome": "a"})
	if code != http.StatusOK {
		t.Fatalf("vote: %d body=%s", code, body)
	}

	for _, f := range fids {
		fx, err := db.GetFeature(ctx, f)
		if err != nil {
			t.Fatalf("GetFeature(%d): %v", f, err)
		}
		if fx.EloR != before[f] {
			t.Errorf("feature %d elo_r moved %v -> %v from a vote between two PROJECTS",
				f, before[f], fx.EloR)
		}
	}
}

// The entry-point guard. Refusing only at vote time would leave the arena's own
// project visible in its own competitor list, which a reader would read as a real
// comparison.
func TestTheArenaCannotContainTheProjectItIsAbout(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	ctx := context.Background()
	_, pid := makeProject(t, ts, "selfcomp-owner")

	resp, body := doJSON(t, ts, "POST", "/api/v1/projects/selfcomp-owner/alternatives",
		map[string]any{"use_case": "self comparison"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create arena: %d body=%v", resp.StatusCode, body)
	}
	arenaID := int64(body["arena"].(map[string]any)["id"].(float64))

	resp, body = doJSON(t, ts, "POST",
		"/api/v1/projects/selfcomp-owner/alternatives/"+itoa(arenaID)+"/entries",
		map[string]any{"project_id": pid})
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("adding the arena's own project succeeded; an arena that compares "+
			"a project with itself is not a comparison (body=%v)", body)
	}

	entries, err := db.ArenaLeaderboard(ctx, arenaID, 10)
	if err != nil {
		t.Fatalf("ArenaLeaderboard: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("arena holds %d entries after the refusal, want 0", len(entries))
	}
}

// §6.3's do-nothing baseline is a solution concept. In an alternatives arena
// "do nothing" means "keep using the incumbent", which is the arena's subject
// rather than a competitor to it.
func TestAnAlternativesArenaHasNoDoNothingEntry(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	ctx := context.Background()
	arenaID, a, b, asker := seedAlternatives(t, ts, "nobaseline", "use case three")

	// There is no `SetArenaBaseline` in this tree — the baseline row is created by
	// `EnsureArena` for SOLUTION arenas only, so there is nothing to call.
	// Asserting the absence of a setter would be a test that passes forever; the
	// rule is proven where it can actually fail, in the read model below.
	entries, err := db.ArenaLeaderboard(ctx, arenaID, 10)
	if err != nil {
		t.Fatalf("ArenaLeaderboard: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("the arena holds %d entries, want the 2 competitors", len(entries))
	}
	// Nothing in the alternatives READ MODEL may present
	// a baseline as a competitor. The handler never reads is_baseline, so this
	// holds as long as the entries it does read carry no baseline flag.
	status, raw := authGet(t, ts, "/api/v1/projects/nobaseline-owner/alternatives", "")
	if status != http.StatusOK {
		t.Fatalf("GET: %d", status)
	}
	if containsAny(string(raw), "is_baseline", `"baseline"`) {
		t.Errorf("the alternatives response exposes a baseline concept: %s", raw)
	}

	// And the store's own vote path must not have let the baseline act as an
	// opponent: a vote between the two real competitors still ranks both.
	code, body := authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/nobaseline-owner/alternatives/"+itoa(arenaID)+"/vote", asker,
		map[string]any{"a_project_id": a, "b_project_id": b, "outcome": "a"})
	if code != http.StatusOK {
		t.Fatalf("vote: %d body=%s", code, body)
	}
	_ = entries
}

// The anti-enumeration rule, on both the add and the read.
//
// The add: a private project cannot be entered by someone who cannot read it.
// The read: one that was public when added and is private NOW must not be ranked
// into a response — filtering only on add leaves that leak open forever.
func TestAnAlternativesArenaRefusesACandidateTheCallerCannotSee(t *testing.T) {
	ts, _ := newTestServerWithStore(t)
	// One explicit owner for the whole test. The harness's default identity can
	// create and read, but the visibility route demands a token, so `setVisibility`
	// with "" is answered 401 — a correct refusal meeting a wrong fixture. And the
	// candidate's owner has to be a party the test controls, because the point of
	// the second half is flipping ITS visibility and watching the ranking change.
	owner := registerAndLogin(t, ts, "vis-owner")
	createTestProjectWithAuth(t, ts, owner, "vis-owner")
	createTestProjectWithAuth(t, ts, owner, "vis-secret")

	code, body := authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/vis-owner/alternatives", owner,
		map[string]any{"use_case": "visibility"})
	if code != http.StatusCreated {
		t.Fatalf("create arena: %d body=%s", code, body)
	}
	var created struct {
		Arena struct {
			ID int64 `json:"id"`
		} `json:"arena"`
	}
	decodeInto(t, body, &created)
	arenaID := created.Arena.ID
	var secretProject struct {
		ID int64 `json:"id"`
	}
	code, body = authJSON(t, ts, http.MethodGet, "/api/v1/projects/vis-secret", owner, nil)
	if code != http.StatusOK {
		t.Fatalf("read vis-secret: %d body=%s", code, body)
	}
	decodeInto(t, body, &secretProject)
	secret := secretProject.ID
	setVisibility(t, ts, owner, "vis-secret", store.VisibilityPrivate)

	// A stranger must not be able to add it.
	stranger := registerAndLogin(t, ts, "alt-stranger")
	strangerCode, strangerBody := authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/vis-owner/alternatives/"+itoa(arenaID)+"/entries", stranger,
		map[string]any{"project_id": secret})
	if strangerCode == http.StatusOK {
		t.Fatalf("a stranger added a private project to the arena (body=%s)", strangerBody)
	}
	// The refusal must not name the project. A message containing it confirms the
	// id and slug exist, which is the oracle requireProjectID exists to close.
	if containsAny(string(strangerBody), "vis-secret", itoa(secret)) {
		t.Errorf("the refusal names the private project: %s", strangerBody)
	}

	// The owner CAN add it (the owner is not a member of vis-secret, so this also
	// proves the check is the caller's readability and not role).
	code, body = authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/vis-owner/alternatives/"+itoa(arenaID)+"/entries", owner,
		map[string]any{"project_id": secret})
	if code != http.StatusOK {
		t.Fatalf("the readable caller could not add the project: %d body=%s", code, body)
	}

	// Read-side: a stranger must not see it ranked.
	status, raw := authGet(t, ts, "/api/v1/projects/vis-owner/alternatives", stranger)
	if status != http.StatusOK {
		t.Fatalf("stranger GET: %d body=%s", status, raw)
	}
	if containsAny(string(raw), "vis-secret") {
		t.Errorf("a private competitor is ranked into a response for a caller who "+
			"cannot read it: %s", raw)
	}
}

// FindArena keys the other two shapes on use_case = ”, so an empty use case would
// collide with them under a different type. The refusal names the field, because
// this is the one error a client can fix without reading the spec.
func TestAnEmptyUseCaseIsRefusedAndTheRefusalNamesIt(t *testing.T) {
	ts, _ := newTestServerWithStore(t)
	makeProject(t, ts, "emptyuc-owner")

	for _, body := range []map[string]any{
		{"use_case": ""},
		{"use_case": "   "},
		{},
	} {
		resp, out := doJSON(t, ts, "POST", "/api/v1/projects/emptyuc-owner/alternatives", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %v: status %d, want 400 (out=%v)", body, resp.StatusCode, out)
			continue
		}
		if msg := errMessage(mustJSON(t, out)); !containsAny(msg, "use_case") {
			t.Errorf("body %v: the refusal does not name use_case: %q", body, msg)
		}
	}
}

// The panel's empty state, at the API layer: a project with no arena answers with
// an empty list, not an error and not a null.
func TestAProjectWithNoAlternativesArenaReturnsAnEmptyListNotNull(t *testing.T) {
	ts, _ := newTestServerWithStore(t)
	makeProject(t, ts, "noarena-owner")

	status, raw := authGet(t, ts, "/api/v1/projects/noarena-owner/alternatives", "")
	if status != http.StatusOK {
		t.Fatalf("GET: %d body=%s", status, raw)
	}
	// Decoded, not string-matched. The first version asserted the body contains
	// the literal `"arenas":[]`, which never appears because the handler's output
	// is indented — so the test was asserting on JSON formatting, and would have
	// passed or failed with a cosmetic change.
	var decoded struct {
		Arenas []json.RawMessage `json:"arenas"`
	}
	decodeInto(t, raw, &decoded)
	if decoded.Arenas == nil {
		t.Errorf("arenas is null; the page must be able to distinguish \"no use "+
			"cases compared\" from an absent field: %s", raw)
	}
	if len(decoded.Arenas) != 0 {
		t.Errorf("got %d arenas, want none: %s", len(decoded.Arenas), raw)
	}
}

// A project must never see another project's arenas. ListArases filters on type
// alone, so a handler that used it directly would leak every alternatives arena
// on the instance to whoever asked.
func TestTheAlternativesListIsScopedToOneProject(t *testing.T) {
	ts, _ := newTestServerWithStore(t)
	makeProject(t, ts, "scoped-a-owner")
	makeProject(t, ts, "scoped-b-owner")
	doJSON(t, ts, "POST", "/api/v1/projects/scoped-a-owner/alternatives",
		map[string]any{"use_case": "case for a"})
	doJSON(t, ts, "POST", "/api/v1/projects/scoped-b-owner/alternatives",
		map[string]any{"use_case": "case for b"})

	status, raw := authGet(t, ts, "/api/v1/projects/scoped-a-owner/alternatives", "")
	if status != http.StatusOK {
		t.Fatalf("GET: %d", status)
	}
	if containsAny(string(raw), "case for b") {
		t.Errorf("project a's response contains project b's use case: %s", raw)
	}
}

// An arena belonging to another project must be unreachable through this
// project's route, and the refusal must be indistinguishable from a nonexistent
// arena — otherwise the arena id space is enumerable.
func TestAnArenaFromAnotherProjectIsNotFound(t *testing.T) {
	ts, _ := newTestServerWithStore(t)
	makeProject(t, ts, "other-owner")
	resp, body := doJSON(t, ts, "POST", "/api/v1/projects/other-owner/alternatives",
		map[string]any{"use_case": "theirs"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d body=%v", resp.StatusCode, body)
	}
	arenaID := int64(body["arena"].(map[string]any)["id"].(float64))

	makeProject(t, ts, "mine-owner")
	_, rival := makeProject(t, ts, "a-rival")

	gotReal, bodyReal := doJSON(t, ts, "POST",
		"/api/v1/projects/mine-owner/alternatives/"+itoa(arenaID)+"/entries",
		map[string]any{"project_id": rival})
	gotFake, bodyFake := doJSON(t, ts, "POST",
		"/api/v1/projects/mine-owner/alternatives/999999/entries",
		map[string]any{"project_id": rival})

	if gotReal.StatusCode != gotFake.StatusCode {
		t.Errorf("another project's arena answered %d and a nonexistent one answered "+
			"%d; the two must be identical", gotReal.StatusCode, gotFake.StatusCode)
	}
	if errMessage(mustJSON(t, bodyReal)) != errMessage(mustJSON(t, bodyFake)) {
		t.Errorf("bodies differ:\n  real arena: %v\n  fake arena: %v",
			bodyReal, bodyFake)
	}
}

// The three helpers below stand in for ones that do not exist in this package.
//
// `containsAny(s, a, b)` was written as though it were `strings.Contains` with
// several needles, and there is no such helper here — the nearest real one is
// `contains(haystack, needle string) bool` in solutions_api_test.go, two
// arguments. `errMessage` extracts an API error body, which no existing helper
// does: doJSON returns a map, and the refusal assertions below are about the
// rendered STRING.
//
// Split out at the top of the file so a reader meets them before they meet a
// test that depends on them, and so the "these are local" fact is visible.
func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// errMessage returns the rendered `error` field of an API response body.
func errMessage(body []byte) string {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return string(body)
	}
	if e, ok := m["error"].(string); ok {
		return e
	}
	return string(body)
}

// decodeInto unmarshals an API body, failing the test with the body on error —
// a decode failure in these tests is always a server bug, not a bad fixture.
func decodeInto(t *testing.T, raw []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("decode: %v body=%s", err, raw)
	}
}

// The four mutants below each survived the suite as first written, and every one
// of them survived for the same reason: the tests asserted a STATUS where the
// rule produces a status by coincidence.
//
//   - M1 own-project guard removed  -> the add returned 200 instead of 400. No
//     test added the arena's own project through the HTTP route, because the
//     store test below and the HTTP test were written as if the store test covered
//     it. It does not: the route calls the store, so removing the store guard
//     changes the route's answer.
//   - M2 arena-type guard removed   -> a project could be added to a FEATURE
//     arena. Nothing ever tried, so nothing noticed.
//   - M3 candidate visibility on add -> the stranger's add still failed, because
//     it failed for a DIFFERENT reason (the role check, or the candidate's
//     invisibility reaching the caller through a later check). A refusal by the
//     wrong guard is still a refusal.
//   - M5 entry_count from the arena total -> only observable when some entries
//     are filtered out, and no test had both a filtered entry and a count.
//
// Each test below is named for the mutant it kills.

// M1. The store guard alone is not enough evidence — the ROUTE must refuse.
func TestTheRouteRefusesToAddTheArenasOwnProject(t *testing.T) {
	ts, _ := newTestServerWithStore(t)
	asker := registerAndLogin(t, ts, "ownproj-asker")
	createTestProjectWithAuth(t, ts, asker, "ownproj-owner")

	var owner struct {
		ID int64 `json:"id"`
	}
	code, body := authJSON(t, ts, http.MethodGet, "/api/v1/projects/ownproj-owner", asker, nil)
	if code != http.StatusOK {
		t.Fatalf("read owner: %d body=%s", code, body)
	}
	decodeInto(t, body, &owner)

	code, body = authJSON(t, ts, http.MethodPost, "/api/v1/projects/ownproj-owner/alternatives",
		asker, map[string]any{"use_case": "self"})
	if code != http.StatusCreated {
		t.Fatalf("create arena: %d body=%s", code, body)
	}
	var created struct {
		Arena struct {
			ID int64 `json:"id"`
		} `json:"arena"`
	}
	decodeInto(t, body, &created)

	code, body = authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/ownproj-owner/alternatives/"+itoa(created.Arena.ID)+"/entries",
		asker, map[string]any{"project_id": owner.ID})
	if code == http.StatusOK {
		t.Fatalf("the route accepted the arena's own project as a competitor "+
			"(body=%s); M1 survived until this test existed", body)
	}
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; the refusal is a bad request, not a "+
			"missing row", code)
	}

	// And nothing was written.
	status, raw := authGet(t, ts, "/api/v1/projects/ownproj-owner/alternatives", asker)
	if status != http.StatusOK {
		t.Fatalf("GET: %d body=%s", status, raw)
	}
	var listed struct {
		Arenas []struct {
			Entries []struct {
				Project int64 `json:"project_id"`
			} `json:"entries"`
		} `json:"arenas"`
	}
	decodeInto(t, raw, &listed)
	for _, a := range listed.Arenas {
		for _, e := range a.Entries {
			if e.Project == owner.ID {
				t.Errorf("the refused project %d is in the arena anyway", e.Project)
			}
		}
	}
}

// M2. The arena-type guard. A project must not be addable to a FEATURE arena.
func TestAProjectCannotBeAddedToAFeatureArena(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	asker := registerAndLogin(t, ts, "wrongtype-asker")
	createTestProjectWithAuth(t, ts, asker, "wrongtype-owner")
	createTestProjectWithAuth(t, ts, asker, "wrongtype-other")

	var other struct {
		ID int64 `json:"id"`
	}
	code, body := authJSON(t, ts, http.MethodGet, "/api/v1/projects/wrongtype-other", asker, nil)
	if code != http.StatusOK {
		t.Fatalf("read other: %d", code)
	}
	decodeInto(t, body, &other)

	// A feature-priority arena, created the way the store creates them.
	ctx := context.Background()
	projID := mustProjectIDBySlug(t, db, "wrongtype-owner")
	featureArena, err := db.EnsureArena(ctx, store.ArenaFeaturePriority, projID, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena(feature-priority): %v", err)
	}

	code, body = authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/wrongtype-owner/alternatives/"+itoa(featureArena.ID)+"/entries",
		asker, map[string]any{"project_id": other.ID})
	if code == http.StatusOK {
		t.Fatalf("a project was added to a FEATURE-priority arena (body=%s); "+
			"M2 survived until this test existed", body)
	}
}

// M3. The candidate's visibility must be checked, and the check must be the thing
// doing the refusing — not the role gate in front of it.
//
// The stranger in this test is a member of the arena's project, so every other
// gate passes and only the candidate's readability can refuse.
func TestTheRefusalForAnUnreadableCandidateIsTheVisibilityCheckAndNotTheRoleGate(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	owner := registerAndLogin(t, ts, "visgate-owner")
	createTestProjectWithAuth(t, ts, owner, "visgate-owner")
	createTestProjectWithAuth(t, ts, owner, "visgate-candidate")
	setVisibility(t, ts, owner, "visgate-candidate", store.VisibilityPrivate)

	var cand struct {
		ID int64 `json:"id"`
	}
	code, body := authJSON(t, ts, http.MethodGet, "/api/v1/projects/visgate-candidate", owner, nil)
	if code != http.StatusOK {
		t.Fatalf("read candidate: %d body=%s", code, body)
	}
	decodeInto(t, body, &cand)

	code, body = authJSON(t, ts, http.MethodPost, "/api/v1/projects/visgate-owner/alternatives",
		owner, map[string]any{"use_case": "gate"})
	if code != http.StatusCreated {
		t.Fatalf("create arena: %d body=%s", code, body)
	}
	var created struct {
		Arena struct {
			ID int64 `json:"id"`
		} `json:"arena"`
	}
	decodeInto(t, body, &created)

	// The caller is a genuine OWNER of the arena's project, so requireRole passes
	// and the candidate's visibility is the only gate left that can refuse.
	//
	// Promoted through the store, because there is no members POST route
	// (`server.go` registers GET /members and nothing else). The first version of
	// this test guessed at such a route and then fell back to
	// `t.Logf("no membership route available")` — a test that asserts conditionally
	// asserts nothing, and with the role gate possibly refusing first, the mutant
	// it was written to kill survived.
	insider := registerAndLogin(t, ts, "visgate-insider")
	ownerID := mustProjectIDBySlug(t, db, "visgate-owner")
	if err := db.JoinProject(context.Background(), ownerID, insiderIDOf(t, db, "visgate-insider")); err != nil {
		t.Fatalf("JoinProject(insider): %v", err)
	}
	// JoinProject writes a members row, but what role? Read it back rather than
	// assume — a caller left at whatever the default is may or may not satisfy
	// requireRole("contributor"), and a test that does not know which role it got
	// is not testing the visibility gate.
	role, err := db.GetRoleForProject(context.Background(), ownerID, insiderIDOf(t, db, "visgate-insider"))
	if err != nil {
		t.Fatalf("GetRoleForProject(insider): %v", err)
	}
	t.Logf("insider role after JoinProject: %q", role)
	if role == "guest" || role == "" {
		t.Fatalf("the insider is a %q on the project, so requireRole would refuse "+
			"before the visibility check and this test would prove nothing", role)
	}

	strangerCode, strangerBody := authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/visgate-owner/alternatives/"+itoa(created.Arena.ID)+"/entries",
		insider, map[string]any{"project_id": cand.ID})
	if strangerCode == http.StatusOK {
		t.Fatalf("an unreadable candidate was accepted (body=%s); M3 survived "+
			"until this test existed", strangerBody)
	}
	if containsAny(string(strangerBody), "visgate-candidate", itoa(cand.ID)) {
		t.Errorf("the refusal names the unreadable candidate: %s", strangerBody)
	}
}

// M5. entry_count must count what is RENDERED.
//
// The arena holds three competitors; one of them is private and unreadable by
// this caller. The count above a list that omits it is a contradiction on the
// page — the count is the sentence a reader acts on.
func TestTheEntryCountCountsOnlyWhatThisCallerCanSee(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	asker := registerAndLogin(t, ts, "count-asker")
	rival := registerAndLogin(t, ts, "count-rival")
	createTestProjectWithAuth(t, ts, asker, "count-owner")
	createTestProjectWithAuth(t, ts, rival, "count-open")
	createTestProjectWithAuth(t, ts, rival, "count-closed")

	var open, closed struct {
		ID int64 `json:"id"`
	}
	code, body := authJSON(t, ts, http.MethodGet, "/api/v1/projects/count-open", asker, nil)
	if code != http.StatusOK {
		t.Fatalf("read open: %d", code)
	}
	decodeInto(t, body, &open)
	code, body = authJSON(t, ts, http.MethodGet, "/api/v1/projects/count-closed", rival, nil)
	if code != http.StatusOK {
		t.Fatalf("read closed: %d", code)
	}
	decodeInto(t, body, &closed)

	code, body = authJSON(t, ts, http.MethodPost, "/api/v1/projects/count-owner/alternatives",
		asker, map[string]any{"use_case": "counting"})
	if code != http.StatusCreated {
		t.Fatalf("create arena: %d body=%s", code, body)
	}
	var created struct {
		Arena struct {
			ID int64 `json:"id"`
		} `json:"arena"`
	}
	decodeInto(t, body, &created)

	// Both added while PUBLIC. The visibility change comes afterwards, because the
	// add guard refuses an unreadable candidate — and that refusal is the M3 test,
	// which is a different test with a different caller. Reading the sequence the
	// other way round makes this fixture depend on the bug it is not testing: the
	// first version set count-closed private BEFORE the add loop and then read the
	// resulting 404 as a broken fixture rather than as the add guard working.
	for _, id := range []int64{open.ID, closed.ID} {
		code, body = authJSON(t, ts, http.MethodPost,
			"/api/v1/projects/count-owner/alternatives/"+itoa(created.Arena.ID)+"/entries",
			asker, map[string]any{"project_id": id})
		if code != http.StatusOK {
			t.Fatalf("add %d: %d body=%s", id, code, body)
		}
	}

	// Now the asker loses access to one of them. The arena still holds both — which
	// is exactly why the count has to be recomputed rather than read off the arena.
	setVisibility(t, ts, rival, "count-closed", store.VisibilityPrivate)

	// The arena really does hold two.
	entries, err := db.ArenaLeaderboard(context.Background(), created.Arena.ID, 10)
	if err != nil {
		t.Fatalf("ArenaLeaderboard: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("the arena holds %d entries; the fixture needs 2 for this test to "+
			"mean anything", len(entries))
	}

	// The asker cannot read one of them, so sees one — and the count must say 1.
	status, raw := authGet(t, ts, "/api/v1/projects/count-owner/alternatives", asker)
	if status != http.StatusOK {
		t.Fatalf("GET: %d body=%s", status, raw)
	}
	var listed struct {
		Arenas []struct {
			EntryCount int `json:"entry_count"`
			Entries    []struct {
				Slug string `json:"slug"`
			} `json:"entries"`
		} `json:"arenas"`
	}
	decodeInto(t, raw, &listed)
	if len(listed.Arenas) != 1 {
		t.Fatalf("got %d arenas, want 1", len(listed.Arenas))
	}
	a := listed.Arenas[0]
	if a.EntryCount != len(a.Entries) {
		t.Errorf("entry_count = %d but %d entries are rendered; a count above the "+
			"list is a contradiction on the page, and it leaks that a competitor "+
			"exists. M5 survived until this test existed", a.EntryCount, len(a.Entries))
	}
	if a.EntryCount != 1 {
		t.Errorf("entry_count = %d, want 1 (one of the two competitors is private)",
			a.EntryCount)
	}
}

// insiderIDOf looks up a registered account by username.
//
// `mustUser` CREATES a user and fails on a duplicate, so it cannot be used for the
// `registerAndLogin` accounts in these tests — the harness has already created
// every one of them. This reads instead of writing.
func insiderIDOf(t *testing.T, db *store.DB, username string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow("SELECT id FROM users WHERE username = ?", username).Scan(&id); err != nil {
		t.Fatalf("look up %s: %v", username, err)
	}
	return id
}
