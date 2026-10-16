package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// §6.6's emergency hold at the API boundary.
//
// The route existed with a handler, a store method and a store test, and NOTHING read
// it: no page, and -- in the inventory -- no row of its own, because the coarse
// "/consensus" row was satisfied by any page that read a call. Two things followed, and
// the first is what this file is for:
//
//   - the route did not check that the call belonged to the project in the URL, so a
//     hold's grounds and expiry could be read through ANY project's URL. The id is a
//     sequence and the URL was the only thing naming which call was meant.
//   - a suspended call looked ordinary on the page while every write was refused.

func TestACallHoldIsReadableOnlyThroughItsOwnProjectsURL(t *testing.T) {
	ts, db, slugA, pidA, _ := seedPanels(t)
	makeSecondProject(t, ts, db, "hold-other")

	ctx := context.Background()
	call, err := db.CreateConsensusCall(ctx, pidA, 0, 1, "Adopt X?", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	if _, err := db.PlaceEmergencyHold(ctx, pidA, call.ID, 1,
		"security", "a live vulnerability in the exporter", 7); err != nil {
		t.Fatalf("PlaceEmergencyHold: %v", err)
	}
	reader := registerAndLogin(t, ts, "hold-reader")
	id := strconv.FormatInt(call.ID, 10)

	// The call's own project: the hold comes back, with the two things a page needs to
	// explain a call that resolves nothing -- why, and until when.
	code, raw := authGet(t, ts, "/api/v1/projects/"+slugA+"/consensus/"+id+"/hold", reader)
	if code != http.StatusOK {
		t.Fatalf("reading the hold at its own URL = %d, want 200: %s", code, raw)
	}
	body := decodeBody(t, raw)
	hold, _ := body["hold"].(map[string]any)
	if hold == nil {
		t.Fatalf("the hold is not in the response: %s", raw)
	}
	if g, _ := hold["grounds"].(string); g != "security" {
		t.Errorf("grounds = %v, want security: a suspended call that does not say why "+
			"is indistinguishable from a broken one", hold["grounds"])
	}
	if _, ok := hold["expires_at"].(float64); !ok {
		t.Errorf("expires_at missing from %v", hold)
	}

	// Another project's URL, the same call id: not found. Anything else confirms the
	// call exists and hands over a hold's grounds for it.
	if code, raw := authGet(t, ts, "/api/v1/projects/hold-other/consensus/"+id+"/hold", reader); code != http.StatusNotFound {
		t.Errorf("reading project A's hold through project B's URL = %d, want 404: %s", code, raw)
	}

	// Sanity: the row is really there, so the 404 above is the scope check and not an
	// empty fixture.
	if _, err := db.ActiveHoldForCall(ctx, call.ID); err != nil {
		t.Fatalf("the fixture's hold does not exist: %v", err)
	}
}

func TestACallWithNoHoldSaysSoRatherThanFailing(t *testing.T) {
	// null, not 404: "this call is not suspended" is a normal answer and the page asks
	// on every read. A 404 would make the ordinary case look like a missing resource.
	ts, db, slug, pid, _ := seedPanels(t)
	call, err := db.CreateConsensusCall(context.Background(), pid, 0, 1, "Adopt Y?", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	resp, body := getJSON(t, ts, "/api/v1/projects/"+slug+"/consensus/"+
		strconv.FormatInt(call.ID, 10)+"/hold", "X-No-Auth", "1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reading a hold that does not exist = %d, want 200", resp.StatusCode)
	}
	if body["hold"] != nil {
		t.Errorf("hold = %v, want null", body["hold"])
	}
}

func TestAMaintainerCannotHoldAnotherProjectsCall(t *testing.T) {
	// §8.1 gives the maintainer exactly one content-level power -- "run security embargo,
	// emergency hold" -- and this route was scoping it to the WRONG project. It resolved
	// the role against the project in the URL and the call by its id ALONE, then wrote the
	// hold to call.ProjectID: so a maintainer of any project could suspend any OTHER
	// project's call by asking through their own project's URL. The write landed on the
	// right row (the hold carries the call's project) which is what made it look correct.
	ts, db, _, pidA, _ := seedPanels(t)
	makeSecondProject(t, ts, db, "hold-other")
	ctx := context.Background()

	call, err := db.CreateConsensusCall(ctx, pidA, 0, 1, "Adopt Z?", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	id := strconv.FormatInt(call.ID, 10)

	// A maintainer of the OTHER project. §8.2's activity half is not required of a role
	// check, so a members row is enough here.
	other := registerAndLogin(t, ts, "hold-other-maintainer")
	addMemberOnly(t, db, secondProjectID(t, db, "hold-other"), userIDByName(t, db, "hold-other-maintainer"),
		"hold-other-maintainer", "maintainer")

	code, raw := authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/hold-other/consensus/"+id+"/hold", other,
		map[string]string{"grounds": "legal", "reason": "a patent claim arrived this morning"})
	if code == http.StatusCreated {
		t.Fatalf("a maintainer of project B suspended project A's call through B's URL "+
			"(201 Created): %s\nThe role was checked against the URL's project and the "+
			"call was resolved by its id alone, so the one power §8.1 reserves to a "+
			"maintainer could be exercised over a project the caller has no role in.", raw)
	}
	if code != http.StatusNotFound {
		t.Errorf("holding another project's call = %d, want 404 (the call is not in this "+
			"project): %s", code, raw)
	}
	// And nothing was written: the refusal must come BEFORE the hold, not after it.
	// ActiveHoldForCall answers (nil, nil) when a call carries no hold, so the pointer is
	// the assertion -- an err-only check passes either way.
	held, err := db.ActiveHoldForCall(ctx, call.ID)
	if err != nil {
		t.Fatalf("ActiveHoldForCall: %v", err)
	}
	if held != nil {
		t.Errorf("the call is suspended (hold %d) even though the request was refused", held.ID)
	}
}

// userIDByName returns a registered account's id, so a test can grant it a role directly.
func userIDByName(t *testing.T, db *store.DB, name string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(context.Background(),
		`SELECT id FROM users WHERE username = ?`, name).Scan(&id); err != nil {
		t.Fatalf("resolve user %q: %v", name, err)
	}
	return id
}

// secondProjectID resolves a slug to its project id, so a test can grant a role without
// going through the join route.
func secondProjectID(t *testing.T, db *store.DB, slug string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(context.Background(),
		`SELECT id FROM projects WHERE slug = ?`, slug).Scan(&id); err != nil {
		t.Fatalf("resolve project %q: %v", slug, err)
	}
	return id
}

func TestAHoldIsReleasedByTheProjectThatPlacedIt(t *testing.T) {
	// Two defects on the same route, and the first hid the second.
	//
	// It required a role against `chi.URLParam(r, "project_id")`, and the route is
	// `/api/v1/emergency-holds/{hold_id}/release` -- there is no project in that URL, so
	// the project resolved to the empty slug, `requireProjectID` answered 404, and the
	// route could not be used by ANYONE. Nothing noticed because nothing called it.
	//
	// Behind that: the hold was released by its id ALONE after a role check against the
	// URL's project. Had the URL carried a project, a maintainer of project B could have
	// lifted a security or legal hold on project A's call -- the same "the role and the
	// row are resolved independently" mistake as the placement route. A hold belongs to
	// the project that placed it, and that is the project whose maintainer may lift it.
	ts, db, _, pidA, _ := seedPanels(t)
	makeSecondProject(t, ts, db, "hold-release-other")
	ctx := context.Background()

	call, err := db.CreateConsensusCall(ctx, pidA, 0, 1, "Adopt Q?", "")
	if err != nil {
		t.Fatalf("CreateConsensusCall: %v", err)
	}
	hold, err := db.PlaceEmergencyHold(ctx, pidA, call.ID, 1,
		"security", "an unpatched deserialisation bug in the importer", 7)
	if err != nil {
		t.Fatalf("PlaceEmergencyHold: %v", err)
	}
	holdPath := "/api/v1/emergency-holds/" + strconv.FormatInt(hold.ID, 10) + "/release"

	// A maintainer of ANOTHER project: refused, and the hold stays active.
	outsider := registerAndLogin(t, ts, "hold-release-outsider")
	addMemberOnly(t, db, secondProjectID(t, db, "hold-release-other"),
		userIDByName(t, db, "hold-release-outsider"), "hold-release-outsider", "maintainer")
	if code, raw := authJSON(t, ts, http.MethodPost, holdPath, outsider,
		map[string]string{"reason": "not our embargo"}); code == http.StatusOK {
		t.Errorf("a maintainer of another project lifted this project's hold (200): %s", raw)
	}
	if held, err := db.ActiveHoldForCall(ctx, call.ID); err != nil || held == nil {
		t.Fatalf("the hold was lifted by an outsider, or the fixture never placed it: %v (%v)",
			held, err)
	}

	// The project's OWN maintainer: allowed, and the hold is gone.
	owner := registerAndLogin(t, ts, "hold-release-owner")
	addMemberOnly(t, db, pidA, userIDByName(t, db, "hold-release-owner"), "hold-release-owner", "maintainer")
	code, raw := authJSON(t, ts, http.MethodPost, holdPath, owner,
		map[string]string{"reason": "the bug is patched in 1.4.2 and the advisory is out"})
	if code != http.StatusOK {
		t.Fatalf("the project's own maintainer could not lift its hold = %d: %s\n"+
			"Before this was fixed the route answered 404 for EVERY caller, because it "+
			"asked for a project id the URL does not carry.", code, raw)
	}
	if held, err := db.ActiveHoldForCall(ctx, call.ID); err != nil || held != nil {
		t.Errorf("the hold is still active after a successful release: %v (%v)", held, err)
	}
}
