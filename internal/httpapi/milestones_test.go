package httpapi

// R16/R17/R18's routes, at the layer that decides what a client sees: status
// codes, refusals and shapes.
//
// The store's own tests cover the rules (internal/store/milestones_test.go). These
// cover the boundary: who may write, what a stranger learns, and whether the
// roadmap's shape is the one the page prints.

import (
	"net/http"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// ownerToken logs the harness user in again.
//
// authJSON's "" and "anonymous" BOTH opt out of the harness's default identity
// (see isAnon and the X-No-Auth sentinel), so "" is anonymous on this harness --
// the opposite of what authGet's empty string means to a reader. A write test that
// wants the project's creator has to say so with a real token.
func ownerToken(t *testing.T, db *store.DB) string {
	t.Helper()
	_, tok, err := db.Login(t.Context(), "testuser", "test-password-123")
	if err != nil {
		t.Fatalf("login testuser: %v", err)
	}
	return "Bearer " + tok
}

// TestAMilestoneIsCreatedOnlyByAMaintainer pins the role ladder on the one route
// that creates a group other people will be measured against.
func TestAMilestoneIsCreatedOnlyByAMaintainer(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	proj := seedAuditProject(t, db, "ms-roles")

	anon := struct {
		Title string `json:"title"`
	}{"v1"}
	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/ms-roles/milestones", anonymousMarker, anon)
	if code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("anonymous create: %d %s", code, body)
	}

	// A contributor is a member but not a maintainer.
	contributor := registerAndLogin(t, ts, "ms-contributor")
	u, err := db.GetUser(t.Context(), "ms-contributor")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	addMemberOnly(t, db, proj.ID, u.ID, "ms-contributor", "contributor")
	code, body = authJSON(t, ts, http.MethodPost, "/api/v1/projects/ms-roles/milestones", contributor,
		map[string]string{"title": "v1"})
	if code != http.StatusForbidden {
		t.Fatalf("contributor create: %d %s", code, body)
	}

	// The owner is a maintainer, and the create answers 201 with the row.
	code, body = authJSON(t, ts, http.MethodPost, "/api/v1/projects/ms-roles/milestones", ownerToken(t, db),
		map[string]string{"title": "v1"})
	if code != http.StatusCreated {
		t.Fatalf("maintainer create: %d %s", code, body)
	}
	var m store.Milestone
	decodeInto(t, body, &m)
	if m.ID == 0 || m.Title != "v1" {
		t.Fatalf("created milestone: %+v", m)
	}
	// A title is required, and the empty one is a 400 rather than a row called "".
	code, body = authJSON(t, ts, http.MethodPost, "/api/v1/projects/ms-roles/milestones", ownerToken(t, db),
		map[string]string{"title": ""})
	if code != http.StatusBadRequest {
		t.Fatalf("empty title: %d %s", code, body)
	}
}

// TestAMilestoneFromAnotherProjectIs404 keeps the project scope on the read: a
// deliberate status for "exists but not yours" is an existence oracle.
func TestAMilestoneFromAnotherProjectIs404(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	seedAuditProject(t, db, "ms-owner")
	seedAuditProject(t, db, "ms-other")

	code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/ms-owner/milestones", ownerToken(t, db),
		map[string]string{"title": "v1"})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var m store.Milestone
	decodeInto(t, body, &m)

	code, body = authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/ms-other/milestones/"+itoa(m.ID), ownerToken(t, db), nil)
	if code != http.StatusNotFound {
		t.Fatalf("another project's milestone: %d %s", code, body)
	}
	code, body = authJSON(t, ts, http.MethodGet, "/api/v1/projects/ms-owner/milestones/not-a-number", ownerToken(t, db), nil)
	if code != http.StatusNotFound {
		t.Fatalf("a malformed id in the path: %d %s", code, body)
	}
}

// TestTheRoadmapIsTheShapeThePagePrints is the §5 invariant: the page and the API
// read ONE shape, so the totals a reader sees are the totals the route added.
func TestTheRoadmapIsTheShapeThePagePrints(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	seedAuditProject(t, db, "ms-roadmap")
	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/ms-roadmap/milestones", ownerToken(t, db),
		map[string]string{"title": "v1"}); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	code, body := authJSON(t, ts, http.MethodGet, "/api/v1/projects/ms-roadmap/roadmap", anonymousMarker, nil)
	if code != http.StatusOK {
		t.Fatalf("roadmap: %d %s", code, body)
	}
	var got struct {
		Slug       string                    `json:"slug"`
		Milestones []store.Milestone         `json:"milestones"`
		Totals     map[string]int            `json:"totals"`
		Slippage   []store.MilestoneSlippage `json:"slippage"`
	}
	decodeInto(t, body, &got)
	if got.Slug != "ms-roadmap" {
		t.Fatalf("slug = %q", got.Slug)
	}
	if len(got.Milestones) != 1 {
		t.Fatalf("milestones = %d, want 1", len(got.Milestones))
	}
	// totals and slippage are PRESENT even when empty: a page that must distinguish
	// "zero" from "the key is missing" is a page that renders null.
	if got.Totals == nil {
		t.Fatal("totals is nil")
	}
	if got.Slippage == nil {
		t.Fatal("slippage is nil; an unmeasured slip must be null, not an absent key")
	}
	if len(got.Slippage) != 1 || got.Slippage[0].SlipDays != nil || got.Slippage[0].OldestOpenDays != nil {
		// A milestone with no features and no date has nothing to measure.
		t.Fatalf("slippage for an empty undated milestone: %+v", got.Slippage)
	}
}

// TestAnUnknownIntakeModeIsRefusedAndNothingChanges: the column and the message
// must agree, and a typo must not be stored as its own mode.
func TestAnUnknownIntakeModeIsRefusedAndNothingChanges(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	_ = seedAuditProject(t, db, "ms-intake")

	code, body := authJSON(t, ts, http.MethodPut, "/api/v1/projects/ms-intake/intake-mode", ownerToken(t, db),
		map[string]string{"mode": "idea-first"})
	if code != http.StatusBadRequest {
		t.Fatalf("unknown mode: %d %s", code, body)
	}
	got, err := db.GetProject(t.Context(), "ms-intake")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.IntakeMode != store.IntakeComplaintFirst {
		t.Fatalf("the refusal still changed the mode: %q", got.IntakeMode)
	}

	code, body = authJSON(t, ts, http.MethodPut, "/api/v1/projects/ms-intake/intake-mode", ownerToken(t, db),
		map[string]string{"mode": store.IntakeBoth})
	if code != http.StatusOK {
		t.Fatalf("valid mode: %d %s", code, body)
	}
	got, err = db.GetProject(t.Context(), "ms-intake")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.IntakeMode != store.IntakeBoth {
		t.Fatalf("mode = %q, want %q", got.IntakeMode, store.IntakeBoth)
	}
	// The read route must carry it too, or a client cannot know the mode it is in.
	code, body = authJSON(t, ts, http.MethodGet, "/api/v1/projects/ms-intake", anonymousMarker, nil)
	if code != http.StatusOK {
		t.Fatalf("read project: %d %s", code, body)
	}
	// Indented JSON again: `"intake_mode":"both"` never appears in a pretty body.
	if !strings.Contains(string(body), `"intake_mode": "both"`) {
		t.Fatalf("the project read does not carry intake_mode: %s", body)
	}
}

// TestADependencyCycleIsARefusalNamingThePath: the 400 must contain the loop, not
// the word "invalid".
func TestADependencyCycleIsARefusalNamingThePath(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	p := seedAuditProject(t, db, "ms-deps")
	ctx := t.Context()
	owner := auditOwnerID(t, db)

	// Two features with a validated complaint each -- the only way a
	// complaint_first project admits a feature.
	mk := func(title string) store.Feature {
		comp, err := db.CreateComplaint(ctx, p.ID, owner, title+" pain", "body", 1, 0.5, 1.0)
		if err != nil {
			t.Fatalf("CreateComplaint: %v", err)
		}
		if err := db.ValidateComplaint(ctx, comp.ID); err != nil {
			t.Fatalf("ValidateComplaint: %v", err)
		}
		f, err := db.CreateFeature(ctx, p.ID, owner, title, "", "", nil, nil, []int64{comp.ID})
		if err != nil {
			t.Fatalf("CreateFeature: %v", err)
		}
		return f
	}
	a := mk("first")
	b := mk("second")

	code, body := authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/ms-deps/features/"+itoa(a.ID)+"/deps", ownerToken(t, db),
		map[string]int64{"blocker_id": b.ID})
	if code != http.StatusOK {
		t.Fatalf("add dep: %d %s", code, body)
	}
	code, body = authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/ms-deps/features/"+itoa(b.ID)+"/deps", ownerToken(t, db),
		map[string]int64{"blocker_id": a.ID})
	if code != http.StatusBadRequest {
		t.Fatalf("closing the cycle: %d %s", code, body)
	}
	if !strings.Contains(string(body), "cycle") {
		t.Fatalf("the refusal must say cycle: %s", body)
	}

	// And the read names the blocker, so the badge has something to point at.
	code, body = authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/ms-deps/features/"+itoa(a.ID)+"/deps", anonymousMarker, nil)
	if code != http.StatusOK {
		t.Fatalf("read deps: %d %s", code, body)
	}
	// Matched on the TITLE, not the id: the body is indented JSON, so an assertion
	// built from `"id":2` fails against `"id": 2` and looks like a missing row.
	if !strings.Contains(string(body), `"second"`) {
		t.Fatalf("the blocker is missing from the read: %s", body)
	}
}
