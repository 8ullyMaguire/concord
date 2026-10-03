package httpapi

// The Solutions panel at the API boundary (solutions-panel-spec.md).
//
// The per-feature board already had a read path and its own tests
// (solutions_api_test.go). What is new here is the PROJECT-level view, and it
// has two failure modes that a status-code test cannot see:
//
//   - a project-wide leaderboard. Merging every feature's scores into one ranked
//     list compares numbers computed against different opponents, and lets one
//     feature's "do nothing" baseline outrank another feature's real proposal.
//     Asserted structurally: scores stay inside their feature.
//   - silent omission. A panel that renders only the features that have
//     proposals reads as though the project had one feature. The features with
//     none are included and counted, and both counts are asserted.
//
// Fixtures are built through the store and the existing test helpers rather than
// through the new endpoint, for the reason every panel test in this package gives:
// a test whose data comes from the handler under test cannot fail for the reason
// it exists.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// solutionsBody is the decoded response.
type solutionsBody struct {
	ProjectID int64 `json:"project_id"`
	Features  []struct {
		FeatureID    int64  `json:"feature_id"`
		FeatureTitle string `json:"feature_title"`
		Solutions    []struct {
			SolutionID int64   `json:"solution_id"`
			Title      string  `json:"title"`
			Rating     float64 `json:"rating"`
			Score      float64 `json:"score"`
			Coverage   float64 `json:"coverage"`
			IsBaseline bool    `json:"is_baseline"`
		} `json:"solutions"`
		Shown   int `json:"shown"`
		Omitted int `json:"omitted"`
	} `json:"features"`
	FeaturesWithSolutions int `json:"features_with_solutions"`
	FeaturesWithout       int `json:"features_without"`
}

func (b solutionsBody) feature(id int64) (int, bool) {
	for i, f := range b.Features {
		if f.FeatureID == id {
			return i, true
		}
	}
	return -1, false
}

// solutionsFixture builds a project with two features: one carrying several
// solutions, one carrying none. That pair is the whole fixture — it is the
// difference between "this project has proposals" and "this project has one
// feature people have thought about".
func solutionsFixture(t *testing.T) (*httptest.Server, *store.DB, string, int64, int64) {
	t.Helper()
	ts, db := newTestServerWithStore(t)
	ctx := context.Background()
	slug, pid := makeProject(t, ts, "standings-project")

	withSolutions := makeRankedPair(t, ts, slug, pid, "Export must be lossless")[0]
	withoutSolutions := makeRankedPair(t, ts, slug, pid, "Filters need composing")[0]

	author := mustUser(t, db, "solution-author")
	for _, title := range []string{"Stream the export", "Chunk the export", "Verify the export"} {
		if _, err := db.CreateSolution(ctx, store.CreateSolutionInput{
			FeatureID: withSolutions, AuthorID: author,
			Title: title, Body: "a body", Type: "build-new",
		}); err != nil {
			t.Fatalf("CreateSolution(%s): %v", title, err)
		}
	}
	return ts, db, slug, withSolutions, withoutSolutions
}

// ---------------------------------------------------------------------------

// THE structural test. Scores must not escape their feature.
//
// Asserted as containment rather than as an absence: a project-wide leaderboard
// would still return each feature's solutions somewhere in the payload, just
// merged into one list. Checking that every solution in a feature's block came
// from that feature's own feature_id is the property that has to hold, and it is
// the one a merge breaks.
func TestStandingsStayGroupedUnderTheirFeatureAndAreNotMergedIntoARanking(t *testing.T) {
	ts, _, slug, withSol, _ := solutionsFixture(t)

	// Ask for the raw JSON as well, so a merge into a top-level list would be
	// visible as a key this test then notices is missing.
	resp, raw := doJSON(t, ts, "GET", "/api/v1/projects/"+slug+"/solutions", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("= %d: %v", resp.StatusCode, raw)
	}
	if _, merged := raw["solutions"]; merged {
		t.Error("the response carries a top-level `solutions` list; a single merged " +
			"list ranks scores computed against different opponents, and lets one " +
			"feature's do-nothing baseline outrank another's real proposal")
	}
	if _, merged := raw["leaderboard"]; merged {
		t.Error("the response carries a top-level leaderboard; see solutions-panel-spec.md")
	}

	b := decodeSolutions(t, raw)

	i, ok := b.feature(withSol)
	if !ok {
		t.Fatalf("the feature that has solutions is absent from the payload: %+v", b.Features)
	}
	if len(b.Features[i].Solutions) != 3 {
		t.Errorf("the feature has 3 solutions, the payload groups %d under it",
			len(b.Features[i].Solutions))
	}
	// Each solution carries its own text. A standings list of scores with no
	// names is not a ranking a reader can act on, and the panel test caught the
	// title's absence only because it asserted on the rendered row.
	for _, sol := range b.Features[i].Solutions {
		if strings.TrimSpace(sol.Title) == "" {
			t.Errorf("solution %d has no title; the panel can only render a column "+
				"of scores", sol.SolutionID)
		}
	}
	if b.Features[i].FeatureTitle != "Export must be lossless" {
		t.Errorf("feature_title = %q; the panel needs it because the reader has no "+
			"other way to know which feature these standings belong to",
			b.Features[i].FeatureTitle)
	}
	// Each feature carries its own shown count; a project-wide cap would make
	// these wrong.
	if b.Features[i].Shown != 3 {
		t.Errorf("shown = %d, want 3", b.Features[i].Shown)
	}
}

// A feature nobody has proposed for is INCLUDED, and counted. The panel leads
// with both counts, and this is the assertion that stops it leading with "1".
func TestAFeatureWithNoProposalsIsIncludedAndCounted(t *testing.T) {
	ts, _, slug, _, withoutSol := solutionsFixture(t)

	_, raw := doJSON(t, ts, "GET", "/api/v1/projects/"+slug+"/solutions", nil)
	b := decodeSolutions(t, raw)

	i, ok := b.feature(withoutSol)
	if !ok {
		t.Fatalf("the feature with no proposals is absent; a panel that renders only " +
			"features with proposals reads as though the project had one feature")
	}
	if len(b.Features[i].Solutions) != 0 {
		t.Errorf("a feature nobody proposed for carries %d solutions",
			len(b.Features[i].Solutions))
	}
	// An empty list, not null: `len(nil) == 0` would pass the check above and
	// leave the client's job to guess.
	if b.Features[i].Solutions == nil {
		t.Error("`solutions` is null for a feature with no proposals; an empty array " +
			"and null are different code on the page")
	}

	if b.FeaturesWithSolutions != 1 || b.FeaturesWithout != 1 {
		t.Errorf("with=%d without=%d, want 1 and 1 -- the counts are what the panel "+
			"leads with", b.FeaturesWithSolutions, b.FeaturesWithout)
	}
	if len(b.Features) != 2 {
		t.Errorf("payload carries %d features, want 2", len(b.Features))
	}
}

// The list is bounded per feature, and the panel is told what it left out.
//
// Per feature, not per project: a project-wide cap hides some features entirely,
// and a per-feature cap hides that feature's runner-ups, which is what standings
// exist to show. This asserts BOTH halves — the bound is per feature, and the
// count is per feature.
func TestStandingsAreBoundedPerFeatureAndSayWhatTheyOmitted(t *testing.T) {
	ts, db, slug, withSol, _ := solutionsFixture(t)
	ctx := context.Background()

	// The fixture already gave this feature 3 solutions, so the total is
	// 3 + added, not `added`. Asserting `omitted == 2` as a literal was wrong by
	// exactly that much and would have been "fixed" by loosening the handler.
	author := mustUser(t, db, "prolific-author")
	added := SolutionStandingsPerFeature + 2
	for i := 0; i < added; i++ {
		if _, err := db.CreateSolution(ctx, store.CreateSolutionInput{
			FeatureID: withSol, AuthorID: author,
			Title: "extra solution " + strconv.Itoa(i), Body: "b", Type: "build-new",
		}); err != nil {
			t.Fatalf("CreateSolution %d: %v", i, err)
		}
	}
	total, err := db.CountSolutions(ctx, withSol)
	if err != nil {
		t.Fatalf("CountSolutions: %v", err)
	}
	if total != 3+added {
		t.Fatalf("fixture seeded %d solutions, expected %d; the assertion below "+
			"is computed from the real total so a wrong fixture cannot pass it",
			total, 3+added)
	}
	wantOmitted := total - SolutionStandingsPerFeature

	_, raw := doJSON(t, ts, "GET", "/api/v1/projects/"+slug+"/solutions", nil)
	b := decodeSolutions(t, raw)

	i, ok := b.feature(withSol)
	if !ok {
		t.Fatalf("feature absent: %+v", b.Features)
	}
	row := b.Features[i]
	if len(row.Solutions) != SolutionStandingsPerFeature {
		t.Errorf("rendered %d solutions, want the per-feature bound %d",
			len(row.Solutions), SolutionStandingsPerFeature)
	}
	if row.Omitted != wantOmitted {
		t.Errorf("omitted = %d, want %d (of %d seeded, %d shown)",
			row.Omitted, wantOmitted, total, SolutionStandingsPerFeature)
	}
	if row.Shown != SolutionStandingsPerFeature {
		t.Errorf("shown = %d, want %d", row.Shown, SolutionStandingsPerFeature)
	}
}

// A project with features but no proposals, and a project with no features at
// all, are different states and must not collapse into one.
//
// The first says the roadmap exists and nobody has answered it. The second says
// there is no roadmap. A page that renders both as "no solutions" is stating
// something untrue about one of them.
func TestNoFeaturesAndNoProposalsAreDifferentStates(t *testing.T) {
	ts, _, slug, _, _ := solutionsFixture(t)

	// The empty project: created, and given a member but no complaint, so no
	// feature can exist.
	emptySlug, _ := makeProject(t, ts, "no-features-project")
	_, raw := doJSON(t, ts, "GET", "/api/v1/projects/"+emptySlug+"/solutions", nil)
	empty := decodeSolutions(t, raw)
	if len(empty.Features) != 0 {
		t.Errorf("a project with no features carries %d", len(empty.Features))
	}
	if empty.Features == nil {
		t.Error("`features` is null for a project with no features; an empty array is " +
			"the honest answer and null makes the client guess")
	}
	if empty.FeaturesWithout != 0 {
		t.Errorf("features_without = %d for a project with no features at all; "+
			"a feature that does not exist is not a feature awaiting proposals",
			empty.FeaturesWithout)
	}

	// And the project that HAS features: features_without counts them, which is
	// the difference from above.
	_, raw = doJSON(t, ts, "GET", "/api/v1/projects/"+slug+"/solutions", nil)
	withFeatures := decodeSolutions(t, raw)
	if withFeatures.FeaturesWithout != 1 {
		t.Errorf("features_without = %d, want 1; a project with a feature nobody "+
			"has proposed for has exactly that many", withFeatures.FeaturesWithout)
	}
}

// The visibility rule, for the third panel. Same shape as the other two, and it
// is repeated rather than shared because a shared helper would be the thing that
// quietly stops being applied when a fourth panel is added.
func TestAPrivateProjectsStandingsAreNotServedAndTheRefusalIsNotDistinguishable(t *testing.T) {
	ts, db, slug, _, _ := solutionsFixture(t)
	ctx := context.Background()

	if _, err := db.SetProjectVisibility(ctx, slug, "private"); err != nil {
		t.Fatalf("SetProjectVisibility: %v", err)
	}
	stranger := registerAndLogin(t, ts, "stranger")

	code, body := authGet(t, ts, "/api/v1/projects/"+slug+"/solutions", stranger)
	if code != http.StatusNotFound {
		t.Errorf("standings as a stranger = %d, want 404", code)
	}
	if strings.Contains(string(body), "Export must be lossless") ||
		strings.Contains(string(body), "Stream the export") {
		t.Errorf("standings leaked a private project's solutions: %s", body)
	}
	// Indistinguishable from a project that does not exist -- the same
	// existence-oracle rule requireProjectID enforces, asserted for this route.
	missingCode, missingBody := authGet(t, ts,
		"/api/v1/projects/no-such-project-at-all/solutions", stranger)
	if missingCode != code {
		t.Errorf("absent=%d forbidden=%d; the two must be indistinguishable",
			missingCode, code)
	}
	if strings.TrimSpace(string(body)) != strings.TrimSpace(string(missingBody)) {
		t.Errorf("absent body %q vs forbidden body %q", missingBody, body)
	}
}

// The refusal above would also pass if this handler refused everything, which is
// the failure mode of a test that only checks refusals.
func TestAPublicProjectsStandingsAreReadableAnonymously(t *testing.T) {
	ts, _, slug, _, _ := solutionsFixture(t)
	code, body := authGet(t, ts, "/api/v1/projects/"+slug+"/solutions", anonymousMarker)
	if code != http.StatusOK {
		t.Fatalf("= %d, want 200: %s", code, body)
	}
	if !strings.Contains(string(body), "Export must be lossless") {
		t.Errorf("the response omits the seeded feature: %s", body)
	}
}

// The route is top-level {project_id}. Registered inside the /{slug} block, chi
// binds the parameter as `slug`, this handler reads `project_id", gets "", and
// every call 404s with `project ""` -- an error that points at the data rather
// than at the route. See the commit that added the capabilities panel, where this
// cost about an hour.
func TestASolutionsPathWithANonSlugProjectIDIsANotFoundRatherThanAnEmptyBoard(t *testing.T) {
	ts, _, _, _, _ := solutionsFixture(t)
	code, _ := authGet(t, ts, "/api/v1/projects/0/solutions", anonymousMarker)
	if code != http.StatusNotFound {
		t.Errorf("/projects/0/solutions = %d, want 404; a numeric id in this slot "+
			"must not resolve to project 0 and return an empty standings list", code)
	}
}

// ---------------------------------------------------------------------------

func decodeSolutions(t *testing.T, body map[string]any) solutionsBody {
	t.Helper()
	raw, _ := json.Marshal(body)
	var out solutionsBody
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func mustUser(t *testing.T, db *store.DB, username string) int64 {
	t.Helper()
	u, err := db.CreateUser(context.Background(), username, username)
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", username, err)
	}
	return u.ID
}
