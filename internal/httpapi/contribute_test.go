package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/finder"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// R11, plan CF-2: the contribution feed over HTTP.
//
// The point of these tests is that the feed reuses the engine rather than reimplementing it, so
// they assert the ENGINE's semantics as they appear through the feed -- including the one that
// looks like a bug until you read finder-spec §2.7: an item with no stated time budget survives
// a time filter, because "nobody sized this" is not "this will take too long".

type contributeFixture struct {
	ts    *httptest.Server
	db    *store.DB
	slug  string
	owner int64
	// ids of the three features, by title.
	byTitle map[string]int64
}

func newContributeFixture(t *testing.T) contributeFixture {
	t.Helper()
	ts, st := newTestServerWithStore(t)
	ctx := context.Background()

	owner, err := st.GetUser(ctx, "testuser")
	if err != nil {
		t.Fatalf("read the test user: %v", err)
	}
	proj, err := st.CreateProject(ctx, owner.ID, "feed", "Feed", "", "collective", "")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	complaint, err := st.CreateComplaint(ctx, proj.ID, owner.ID, "The export drops the last row", "body", 3, 1.0, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	// Validated on purpose: a validated complaint is no longer open, so it is NOT a work item.
	// The feed's own complaint items are exercised by the "open complaint" test below.
	if err := st.ValidateComplaint(ctx, complaint.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}

	mk := func(title string, l store.WorkItemLabel) int64 {
		f, err := st.CreateFeature(ctx, proj.ID, owner.ID, title, "body", "M", nil, nil, []int64{complaint.ID})
		if err != nil {
			t.Fatalf("CreateFeature(%s): %v", title, err)
		}
		if err := st.UpdateFeatureStatus(ctx, f.ID, "ready"); err != nil {
			t.Fatalf("UpdateFeatureStatus(%s): %v", title, err)
		}
		if err := st.SetWorkItemLabel(ctx, proj.ID, f.ID, l); err != nil {
			t.Fatalf("SetWorkItemLabel(%s): %v", title, err)
		}
		return f.ID
	}
	easy, fifteen := "S", 15
	hard, allDay := "L", 480
	byTitle := map[string]int64{
		"Fix the export header": mk("Fix the export header", store.WorkItemLabel{
			Difficulty: &easy, TimeBudgetMinutes: &fifteen}),
		"Rework the parser": mk("Rework the parser", store.WorkItemLabel{
			Difficulty: &hard, TimeBudgetMinutes: &allDay, NeedsMentor: true}),
		// Deliberately unlabelled: no difficulty, no budget.
		"Write a changelog": mk("Write a changelog", store.WorkItemLabel{}),
	}
	return contributeFixture{ts: ts, db: st, slug: proj.Slug, owner: owner.ID, byTitle: byTitle}
}

func feedQuery(t *testing.T, ts *httptest.Server, query string) contributeResponse {
	t.Helper()
	code, body := authJSON(t, ts, http.MethodGet, "/api/v1/contribute/next?"+query, anonymousMarker, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /contribute/next?%s = %d: %s", query, code, body)
	}
	var res contributeResponse
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("feed response is not JSON: %s", body)
	}
	return res
}

func feedTitles(res contributeResponse) []string {
	out := make([]string, 0, len(res.Items))
	for _, it := range res.Items {
		out = append(out, it.Title)
	}
	return out
}

func TestContributeFeedReturnsEveryUnclaimedItem(t *testing.T) {
	f := newContributeFixture(t)
	res := feedQuery(t, f.ts, "")
	if res.Pool != 3 {
		t.Fatalf("pool = %d, want 3: the validated complaint is not open and the three ready "+
			"features are the feed", res.Pool)
	}
	if res.Count != 3 {
		t.Fatalf("items = %v, want all three", feedTitles(res))
	}
	for _, it := range res.Items {
		if it.ProjectSlug != f.slug {
			t.Fatalf("item %q has project %q, want %q", it.Title, it.ProjectSlug, f.slug)
		}
		if it.HealthMeasured {
			t.Fatalf("item %q claims a measured health, but no metrics were written", it.Title)
		}
		if it.HealthLabel != "health not measured" {
			t.Fatalf("health label = %q, want the honest phrase", it.HealthLabel)
		}
	}
}

func TestContributeFeedFiltersByTimeBudgetAndKeepsUnsizedItems(t *testing.T) {
	f := newContributeFixture(t)
	res := feedQuery(t, f.ts, "minutes=15")

	got := map[string]bool{}
	for _, it := range res.Items {
		got[it.Title] = true
	}
	if !got["Fix the export header"] {
		t.Fatalf("the fifteen-minute item was filtered out: %v", feedTitles(res))
	}
	if got["Rework the parser"] {
		t.Fatalf("an eight-hour item survived minutes=15: %v", feedTitles(res))
	}
	// §2.7, and the reason this assertion exists: "no budget stated" is UNKNOWN, which the
	// engine keeps. Dropping it here would mean the feed silently hides every task nobody has
	// sized -- which is most of them in a young catalog.
	if !got["Write a changelog"] {
		t.Fatalf("the unsized item was dropped; unknown is not over-budget: %v", feedTitles(res))
	}
}

func TestContributeFeedMentorIsAConstraintForYesAndAPreferenceForNo(t *testing.T) {
	f := newContributeFixture(t)

	yes := feedQuery(t, f.ts, "mentor=yes")
	if len(yes.Items) != 1 || yes.Items[0].Title != "Rework the parser" {
		t.Fatalf("mentor=yes returned %v, want only the item somebody can help with", feedTitles(yes))
	}

	// "No, I'll find my way" is a PREFERENCE: it must not remove the mentored item, only rank
	// it lower. A filter here would tell a newcomer that declining help excludes them.
	no := feedQuery(t, f.ts, "mentor=no")
	if len(no.Items) != 3 {
		t.Fatalf("mentor=no returned %v, want all three: declining a mentor is not a filter", feedTitles(no))
	}
	if no.Items[0].Title == "Rework the parser" {
		t.Fatalf("the mentored item still ranks first under mentor=no: %v", feedTitles(no))
	}
}

func TestContributeFeedExcludesClaimedItems(t *testing.T) {
	f := newContributeFixture(t)
	helper, err := f.db.CreateUser(context.Background(), "helper", "Helper")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// The real project id, not 0: ClaimFeature validates it, and a refused claim would leave
	// this test asserting that a claimable item is hidden.
	if err := f.db.ClaimFeature(context.Background(),
		projectIDBySlug(t, f.db, f.slug), f.byTitle["Fix the export header"], helper.ID); err != nil {
		t.Fatalf("ClaimFeature: %v", err)
	}
	res := feedQuery(t, f.ts, "")
	for _, it := range res.Items {
		if it.Title == "Fix the export header" {
			t.Fatalf("a claimed item was offered: %v", feedTitles(res))
		}
	}
	if res.Count != 2 {
		t.Fatalf("items = %v, want the two unclaimed ones", feedTitles(res))
	}
}

func TestContributeFeedHidesProjectsTheCallerCannotRead(t *testing.T) {
	f := newContributeFixture(t)
	if _, err := f.db.SetProjectVisibility(context.Background(), f.slug, store.VisibilityPrivate); err != nil {
		t.Fatalf("SetProjectVisibility: %v", err)
	}
	// A STRANGER, not the owner: hiding a project from its own owner would be a different bug,
	// and testing it as the owner would pass on a feed that leaks every private project.
	stranger := registerAndLogin(t, f.ts, "stranger")

	code, body := authJSON(t, f.ts, http.MethodGet, "/api/v1/contribute/next", stranger, nil)
	if code != http.StatusOK {
		t.Fatalf("feed = %d: %s", code, body)
	}
	var res contributeResponse
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("feed response is not JSON: %s", body)
	}
	if res.Count != 0 || res.Pool != 0 {
		t.Fatalf("a stranger saw %d items from a private project: %v", res.Count, feedTitles(res))
	}
}

func TestContributeFeedRefusesAnUnknownAnswerNamingIt(t *testing.T) {
	f := newContributeFixture(t)
	code, body := authJSON(t, f.ts, http.MethodGet, "/api/v1/contribute/next?minutes=2h", anonymousMarker, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("minutes=2h = %d, want 400: ignoring the value would return an unfiltered feed "+
			"under a URL that claims a filter", code)
	}
	if !strings.Contains(strings.ToLower(string(body)), "minutes") {
		t.Fatalf("the refusal does not name the parameter: %s", body)
	}
}

func TestContributePageRendersTheFeedServerSide(t *testing.T) {
	f := newContributeFixture(t)
	code, body := authJSON(t, f.ts, http.MethodGet, "/contribute", anonymousMarker, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /contribute = %d", code)
	}
	html := string(body)
	for _, want := range []string{"Contribute", "Rework the parser", "health not measured"} {
		if !strings.Contains(html, want) {
			t.Fatalf("the page does not contain %q; a server-rendered feed that needs JavaScript "+
				"to show its items is a blank page for a reader without it", want)
		}
	}
}

// projectIDBySlug is a small helper for the claim test, which needs the id the store's own
// validation demands.
func projectIDBySlug(t *testing.T, st *store.DB, slug string) int64 {
	t.Helper()
	p, err := st.GetProject(context.Background(), slug)
	if err != nil {
		t.Fatalf("GetProject(%s): %v", slug, err)
	}
	return p.ID
}

// TestWhyRanked pins the sentence under each item. It is unit-tested rather than asserted through
// the feed because the three cases need candidate facts the fixture does not have (a project with
// a stated language, for one), and the failure mode here is not a crash: the page renders an empty
// paragraph, which looks like a styling problem. A mutant that flipped the branch to
// `len(parts) > 0` survived the whole feed suite before this test existed.
func TestWhyRanked(t *testing.T) {
	matched := finder.Ranked{Candidate: finder.Candidate{Slug: "p#1"},
		Matches: map[string]string{"minutes": "15", "language": "go"}}
	got := whyRanked(matched, map[string]string{"minutes": "15", "language": "go"})
	if got != "Matches — language: go; minutes: 15." {
		t.Fatalf("why = %q, want the matched facts in sorted order", got)
	}

	unknown := finder.Ranked{Candidate: finder.Candidate{Slug: "p#2"}}
	got = whyRanked(unknown, map[string]string{"minutes": "15"})
	if !strings.Contains(got, "unknown") {
		t.Fatalf("why = %q, want the sentence that says the fit is unknown rather than claiming "+
			"a match the engine did not report", got)
	}

	got = whyRanked(unknown, nil)
	if got != "Nothing narrows this list yet — pick an answer above." {
		t.Fatalf("why = %q, want the unfiltered sentence", got)
	}
}
