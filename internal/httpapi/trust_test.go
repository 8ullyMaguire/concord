package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/config"
	"git.polarisocial.xyz/concord/concord/internal/db"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// The trust-level gate is a security control, and a security control with no
// test is an assumption. These cases cover the ways the check can go wrong:
//
//   - a caller below the threshold is refused, and the row is untouched
//   - a caller exactly at the threshold is allowed (an off-by-one here would
//     either lock the owner out of their own instance or let anyone in)
//   - an unknown status is refused with the valid list, not a DB error
//   - a status cannot be set on a feature belonging to another project
//   - an anonymous caller is refused even when the threshold is 0
//   - a level above the configured ceiling cannot be assigned
//
// Every case goes through a real bearer token and the real middleware, for the
// reason spelled out in newTestServer: injecting actor_id directly tests the
// handler, not the system.
//
// This file has its own harness rather than reusing newTestServer because
// these tests need the *store to set a trust level, and newTestServer returns
// only the httptest.Server. It mirrors that function rather than
// generalising it, so a change to the shared harness cannot silently alter
// what these assert.

type trustHarness struct {
	ts    *httptest.Server
	store *store.DB
	tok   string // the owner's token
	uid   int64  // the owner's user id
	slug  string // slug of the project holding the feature under test
}

func newTrustHarness(t *testing.T) *trustHarness {
	t.Helper()
	sqlDB, err := db.Open(t.TempDir() + "/trust.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Migrate(t.Context(), sqlDB); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(sqlDB)
	u, err := st.CreateUser(t.Context(), "owner", "Owner")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	srv, err := NewServer(st, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPassword(t.Context(), u.Username, "test-password-123"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	_, tok, err := st.Login(t.Context(), u.Username, "test-password-123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	ts := httptest.NewServer(authenticated(srv.Router(), tok))
	t.Cleanup(ts.Close)
	return &trustHarness{ts: ts, store: st, tok: tok, uid: u.ID}
}

func (h *trustHarness) setTrust(t *testing.T, level int) {
	t.Helper()
	if err := h.store.SetTrustLevel(t.Context(), h.uid, level, h.uid, "test"); err != nil {
		t.Fatalf("set trust %d: %v", level, err)
	}
}

// createProjectFeature creates a project owned by the harness user plus a
// feature in it, returning (projectID, featureID).
//
// A complaint is created first because Concord is complaint-driven: a feature
// with no validated pain behind it is rejected by design, and a seed that
// worked without one would be seeding something the instance forbids.
func (h *trustHarness) createProjectFeature(t *testing.T, slug string) (int64, int64) {
	t.Helper()
	call := func(method, path, payload string) map[string]any {
		req, _ := http.NewRequest(method, h.ts.URL+path, bytes.NewReader([]byte(payload)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+h.tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode >= 300 {
			t.Fatalf("%s %s -> %d %v", method, path, resp.StatusCode, out)
		}
		return out
	}
	p := call("POST", "/api/v1/projects", fmt.Sprintf(
		`{"slug":%q,"name":%q,"description":"d","governance_model":"maintainer_led","license":"MIT"}`, slug, slug))
	projectID := int64(p["id"].(float64))
	// Both paths need the slug, not the id: the routes are nested under
	// /projects/{slug_or_id} and the request bodies also carry project_id
	// separately. The existing suite reaches for a slugOf helper to do this;
	// slugs are unique and this is the one just created, so it is reused.
	prefix := fmt.Sprintf("/api/v1/projects/%s", slug)
	c := call("POST", prefix+"/complaints", fmt.Sprintf(
		`{"title":"a pain","body":"why it hurts","severity":4,"frequency":0.5,"project_id":%d}`, projectID))
	_ = call("POST", prefix+"/complaints/"+fmt.Sprintf("%d", int64(c["id"].(float64)))+"/validate", "{}")
	f := call("POST", prefix+"/features", fmt.Sprintf(
		`{"title":"a feature","body":"b","linked_complaints":[%d],"project_id":%d}`,
		int64(c["id"].(float64)), projectID))
	h.slug = slug
	return projectID, int64(f["id"].(float64))
}

func (h *trustHarness) putStatus(t *testing.T, token string, projectID, featureID int64, status string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"status": status, "reason": "test"})
	_ = projectID // routes address the project by slug; see h.createProjectFeature
	url := fmt.Sprintf("%s/api/v1/projects/%s/features/%d/status", h.ts.URL, h.slug, featureID)
	req, _ := http.NewRequest("PUT", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else {
		// The shared authenticated() wrapper injects a default identity when
		// no header is present, so "no token" is not expressible as an absent
		// header. This sentinel is how the harness opts out.
		req.Header.Set("X-No-Auth", "1")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put status: %v", err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}

func (h *trustHarness) featureStatus(t *testing.T, projectID, featureID int64) string {
	t.Helper()
	_ = projectID
	req, _ := http.NewRequest("GET",
		fmt.Sprintf("%s/api/v1/projects/%s/features/%d", h.ts.URL, h.slug, featureID), nil)
	req.Header.Set("Authorization", "Bearer "+h.tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	defer resp.Body.Close()
	var f map[string]any
	json.NewDecoder(resp.Body).Decode(&f)
	s, _ := f["status"].(string)
	return s
}

func TestSetFeatureStatusRefusesBelowThreshold(t *testing.T) {
	t.Setenv("CONCORD_TRUST_LEVEL_MIN", "1")
	h := newTrustHarness(t)
	h.setTrust(t, 0)
	pid, fid := h.createProjectFeature(t, "below")

	code, body := h.putStatus(t, h.tok, pid, fid, "shipped")
	if code == http.StatusOK {
		t.Fatalf("an untrusted caller set a status: %s", body)
	}
	// The row must be untouched, not merely rejected in the response: a handler
	// that writes and then reports failure would pass the first assertion.
	if got := h.featureStatus(t, pid, fid); got != "draft" {
		t.Fatalf("status changed despite refusal: %q", got)
	}
}

func TestSetFeatureStatusAllowsAtThreshold(t *testing.T) {
	t.Setenv("CONCORD_TRUST_LEVEL_MIN", "1")
	h := newTrustHarness(t)
	h.setTrust(t, 1) // exactly at the threshold
	pid, fid := h.createProjectFeature(t, "atlevel")

	code, body := h.putStatus(t, h.tok, pid, fid, "shipped")
	if code != http.StatusOK {
		t.Fatalf("a caller at the threshold was refused: %d %s", code, body)
	}
	if got := h.featureStatus(t, pid, fid); got != "shipped" {
		t.Fatalf("status = %q, want shipped", got)
	}
}

func TestSetFeatureStatusRejectsUnknownStatus(t *testing.T) {
	t.Setenv("CONCORD_TRUST_LEVEL_MIN", "1")
	h := newTrustHarness(t)
	h.setTrust(t, 1)
	pid, fid := h.createProjectFeature(t, "badstatus")

	for _, bad := range []string{"done", "", "SHIPPED", "in progress"} {
		code, body := h.putStatus(t, h.tok, pid, fid, bad)
		if code != http.StatusBadRequest {
			t.Errorf("status %q -> %d, want 400 (body %s)", bad, code, body)
			continue
		}
		// The refusal must name the valid set; a bare "bad request" sends the
		// caller back to the source to guess.
		if !bytes.Contains([]byte(body), []byte("consensus")) {
			t.Errorf("status %q refusal does not list valid statuses: %s", bad, body)
		}
	}
	if got := h.featureStatus(t, pid, fid); got != "draft" {
		t.Fatalf("an invalid status was written: %q", got)
	}
}

func TestSetFeatureStatusRefusesCrossProject(t *testing.T) {
	t.Setenv("CONCORD_TRUST_LEVEL_MIN", "1")
	h := newTrustHarness(t)
	h.setTrust(t, 1)
	_, _ = h.createProjectFeature(t, "owner")
	ownerSlug := h.slug
	_, otherFid := h.createProjectFeature(t, "other")

	// A feature id belonging to the "other" project, addressed through
	// "owner": a trusted caller must not reach sideways by guessing an id.
	// The refusal is 404 rather than 403 so the route does not confirm the id
	// exists.
	//
	// h.slug now names "other", so it is restored to the project being
	// addressed. Without that, the request would go to /projects/other and be
	// refused for the right reason by accident — a test that passes because it
	// tested the wrong thing.
	h.slug = ownerSlug
	code, body := h.putStatus(t, h.tok, 0, otherFid, "shipped")
	if code != http.StatusNotFound {
		t.Fatalf("cross-project write -> %d, want 404 (body %s)", code, body)
	}
	// And the feature itself is untouched.
	h.slug = "other"
	if got := h.featureStatus(t, 0, otherFid); got != "draft" {
		t.Fatalf("the other project's feature was written anyway: %q", got)
	}
}

func TestSetFeatureStatusRefusesAnonymous(t *testing.T) {
	t.Setenv("CONCORD_TRUST_LEVEL_MIN", "0")
	h := newTrustHarness(t)
	h.setTrust(t, 1)
	pid, fid := h.createProjectFeature(t, "anon")

	// Threshold 0 is the most permissive setting there is. An anonymous caller
	// must still be refused: GetTrustLevel returns 0 for an unknown account,
	// so a check that only compared levels would let "no such user" through
	// at threshold 0. The handler therefore tests the actor id first.
	code, body := h.putStatus(t, "", pid, fid, "shipped")
	if code == http.StatusOK {
		t.Fatalf("an anonymous caller set a status: %s", body)
	}
}

func TestTrustLevelMinFailsClosedOnGarbage(t *testing.T) {
	// A typo in a systemd unit must not silently open or close the gate.
	for _, v := range []string{"", "abc", "-1", "1.5", "  "} {
		t.Setenv("CONCORD_TRUST_LEVEL_MIN", v)
		if got := config.TrustLevelMin(); got != config.DefaultTrustLevelMin {
			t.Errorf("TrustLevelMin(%q) = %d, want the default %d", v, got, config.DefaultTrustLevelMin)
		}
	}
	// An explicit 0 is the one value that must be honoured: it is how an owner
	// deliberately opens the gate on their own instance.
	t.Setenv("CONCORD_TRUST_LEVEL_MIN", "0")
	if got := config.TrustLevelMin(); got != 0 {
		t.Errorf("an explicit 0 must be honoured, got %d", got)
	}
	t.Setenv("CONCORD_TRUST_LEVEL_MIN", "1")
	if got := config.TrustLevelMin(); got != 1 {
		t.Errorf("TrustLevelMin = %d, want 1", got)
	}
}

func TestSetTrustLevelRefusesAboveCeiling(t *testing.T) {
	h := newTrustHarness(t)
	// The ceiling is the point of trust_config: a level that cannot be reached
	// by assignment is a real bound rather than documentation.
	err := h.store.SetTrustLevel(t.Context(), h.uid, 99, h.uid, "too high")
	if err == nil {
		t.Fatal("a level above the ceiling must be refused")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("exceeds the configured maximum")) {
		t.Fatalf("want a ceiling error, got %v", err)
	}
	// A refused assignment must leave the level alone.
	if lvl, err := h.store.GetTrustLevel(t.Context(), h.uid); err != nil || lvl != 0 {
		t.Fatalf("level = %d (err %v), want 0 after a refused assignment", lvl, err)
	}
}

func TestGetTrustLevelUnknownUserIsZero(t *testing.T) {
	h := newTrustHarness(t)
	// Fail closed: a missing account is 0, not an error, so every
	// authorisation call site needs no separate branch for it.
	lvl, err := h.store.GetTrustLevel(t.Context(), 999999)
	if err != nil {
		t.Fatalf("unknown user must not error: %v", err)
	}
	if lvl != 0 {
		t.Fatalf("unknown user level = %d, want 0", lvl)
	}
}
