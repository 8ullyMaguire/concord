package httpapi

// §4.10's project-page panels at the API boundary (phase3-spec §4).
//
// Both stores shipped with migrations, full AC test coverage and no read path
// whatsoever: a project's capability claims and field reports existed, were
// fully exercised at the store layer, and were invisible to every client. This
// file is the read path, and each test is written to fail if the rule it names
// stops holding — because the whole risk of a read endpoint is that it is
// correct and unhelpful, or helpful and leaky, and neither shows up in a
// status-code assertion.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// seedPanels builds: 3 catalog capabilities, assertions on 2 of them (one
// confirmed, one disputed), and 3 field reports (worked, worked-with-caveats,
// abandoned) so the outcome rate is neither 0 nor 1.
//
// Everything is seeded through the STORE, not through HTTP. A panel test that
// creates its data through the API under test cannot fail for the reason it
// exists.
func seedPanels(t *testing.T) (*httptest.Server, *store.DB, string, int64, int64) {
	t.Helper()
	ts, db := newTestServerWithStore(t)
	ctx := context.Background()

	slug, pid := makeProject(t, ts, "panel-project")

	author, err := db.CreateUser(ctx, "reporter", "Reporter")
	if err != nil {
		t.Fatalf("CreateUser(reporter): %v", err)
	}

	for _, c := range []struct{ key, label, category string }{
		{"wip-limits", "WIP limits", "features"},
		{"offline", "Offline use", "features"},
		{"self-hosted", "Self-hosted", "deployment"},
	} {
		if err := db.EnsureCapability(ctx, store.Capability{
			Key: c.key, Label: c.label, Category: c.category,
			Kind: "boolean", Values: []string{"yes", "partial", "no", "unknown"},
		}); err != nil {
			t.Fatalf("EnsureCapability(%s): %v", c.key, err)
		}
	}

	// wip-limits: asserted yes by the owner, then confirmed by TWO other people,
	// so its state is "confirmed". CapQuorum is 2, not 1 -- one confirmation
	// leaves the claim `asserted`, which is the whole point of the threshold, so a
	// fixture with one confirmation is testing the wrong state.
	if _, err := db.AssertCapability(ctx, "wip-limits", slug, "yes",
		"we enforce it", pid); err != nil {
		t.Fatalf("AssertCapability(wip-limits): %v", err)
	}
	ids, err := db.ListCapabilityAssertions(ctx, pid)
	if err != nil || len(ids) != 1 {
		t.Fatalf("ListCapabilityAssertions: %v (%d)", err, len(ids))
	}
	for _, name := range []string{"confirmer1", "confirmer2"} {
		u, err := db.CreateUser(ctx, name, name)
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", name, err)
		}
		if _, err := db.ConfirmCapability(ctx, ids[0].ID, u.ID, true); err != nil {
			t.Fatalf("ConfirmCapability(%s): %v", name, err)
		}
	}

	// offline: asserted "no" by the owner, then DISPUTED by two people. The
	// value must survive the dispute — that is the store's rule and the panel's
	// job is not to undo it.
	if _, err := db.AssertCapability(ctx, "offline", slug, "no",
		"we removed it in 2.0", pid); err != nil {
		t.Fatalf("AssertCapability(offline): %v", err)
	}
	ids, err = db.ListCapabilityAssertions(ctx, pid)
	if err != nil {
		t.Fatalf("ListCapabilityAssertions: %v", err)
	}
	for _, d := range ids {
		if d.Capability != "offline" {
			continue
		}
		for _, name := range []string{"disputer1", "disputer2"} {
			u, err := db.CreateUser(ctx, name, name)
			if err != nil {
				t.Fatalf("CreateUser(%s): %v", name, err)
			}
			if _, err := db.ConfirmCapability(ctx, d.ID, u.ID, false); err != nil {
				t.Fatalf("ConfirmCapability(dispute): %v", err)
			}
		}
	}

	// self-hosted is deliberately left unasserted: it must appear in the panel
	// with Asserted=false, which is different from being "no".

	for i, outcome := range []string{"worked", "worked-with-caveats", "abandoned"} {
		if _, err := db.CreateFieldReport(ctx, store.FieldReport{
			ProjectID: pid, UserID: author.ID,
			Version: "1.0.0", UseCase: "small teams",
			Environment: []string{"arm64", "amd64", "amd64"}[i],
			Outcome:     outcome,
			Caveats:     "slow on large boards",
			Advice:      "fine for small teams",
		}); err != nil {
			t.Fatalf("CreateFieldReport(%s): %v", outcome, err)
		}
	}

	return ts, db, slug, pid, author.ID
}

// capabilitiesBody is the decoded response.
type capabilitiesBody struct {
	ProjectID     int64 `json:"project_id"`
	UnknownCount  int   `json:"unknown_count"`
	AssertedCount int   `json:"asserted_count"`
	Capabilities  []struct {
		Key      string `json:"key"`
		Label    string `json:"label"`
		Asserted bool   `json:"asserted"`
		Value    string `json:"value"`
		State    string `json:"state"`
	} `json:"capabilities"`
}

func (b capabilitiesBody) row(key string) (int, bool) {
	for i, c := range b.Capabilities {
		if c.Key == key {
			return i, true
		}
	}
	return -1, false
}

func getCapabilities(t *testing.T, ts *httptest.Server, slug string) (int, capabilitiesBody) {
	t.Helper()
	resp, body := doJSON(t, ts, "GET", "/api/v1/projects/"+slug+"/capabilities", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET capabilities = %d: %v", resp.StatusCode, body)
	}
	raw, _ := json.Marshal(body)
	var out capabilitiesBody
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	return resp.StatusCode, out
}

// ---------------------------------------------------------------------------

// A confirmed claim must read as confirmed. The panel's first job is to say
// which claims are load-bearing, and a panel that renders every assertion
// identically is not doing that.
func TestAConfirmedCapabilityClaimReadsAsConfirmed(t *testing.T) {
	ts, _, slug, _, _ := seedPanels(t)
	_, body := getCapabilities(t, ts, slug)

	i, ok := body.row("wip-limits")
	if !ok {
		t.Fatalf("wip-limits is absent from the panel: %+v", body.Capabilities)
	}
	row := body.Capabilities[i]
	if !row.Asserted {
		t.Error("wip-limits has an assertion but Asserted=false")
	}
	if row.State != "confirmed" {
		t.Errorf("wip-limits state = %q, want confirmed (two independent claims on it)",
			row.State)
	}
	if row.Value != "yes" {
		t.Errorf("wip-limits value = %q, want yes", row.Value)
	}
}

// THE distinction the capability matrix exists for. A disputed claim keeps its
// original value and its disputed state; the panel must not present "no" as
// settled, because "people disagree" and "this is false" are different claims
// resting on different evidence.
//
// This fails if the handler maps state != confirmed onto a boolean, which is the
// obvious way to write a "capability: yes/no" panel and exactly what the store
// deliberately refuses to do.
func TestADisputedClaimIsShownAsDisputedNotAsNo(t *testing.T) {
	ts, _, slug, _, _ := seedPanels(t)
	_, body := getCapabilities(t, ts, slug)

	i, ok := body.row("offline")
	if !ok {
		t.Fatalf("offline is absent from the panel: %+v", body.Capabilities)
	}
	row := body.Capabilities[i]
	if row.State != "disputed" {
		t.Errorf("offline state = %q, want disputed; two people contested it", row.State)
	}
	// The stored value is "no" and the dispute is recorded separately, so the
	// panel showing Value=="no" is CORRECT — what must not happen is the state
	// reading as anything settled. Asserted is what proves it is not presented
	// as a fact: a settled claim and a contested one are both `asserted: true`.
	if !row.Asserted {
		t.Error("a disputed claim must still count as asserted; it is contested, not absent")
	}
	if row.Value != "no" {
		t.Errorf("offline value = %q; the store keeps the original value through a "+
			"dispute and the panel must not rewrite it either", row.Value)
	}
}

// An unasserted capability must be absent-but-present: listed, Asserted=false,
// and counted in unknown_count. Substituting the string "unknown" for the
// absence is what `ListCapabilitiesForSet`'s doc comment says must not happen,
// and this is the API boundary where it could creep back in.
func TestAnUnassertedCapabilityIsListedAsUnassertedAndCountedAsUnknown(t *testing.T) {
	ts, _, slug, _, _ := seedPanels(t)
	_, body := getCapabilities(t, ts, slug)

	i, ok := body.row("self-hosted")
	if !ok {
		t.Fatalf("self-hosted is absent from the panel; the catalog's capabilities " +
			"must all be listed so the unasserted ones are the contribution queue")
	}
	row := body.Capabilities[i]
	if row.Asserted {
		t.Error("self-hosted was never asserted but the panel says it was")
	}
	if row.Value != "" {
		t.Errorf("self-hosted value = %q, want empty; nobody has said anything, "+
			"which is not the same as having said \"unknown\"", row.Value)
	}
	if row.State != "" {
		t.Errorf("self-hosted state = %q, want empty; only an assertion has a state", row.State)
	}
	if body.UnknownCount != 1 {
		t.Errorf("unknown_count = %d, want 1 (self-hosted of 3 capabilities)", body.UnknownCount)
	}
	if body.AssertedCount != 2 {
		t.Errorf("asserted_count = %d, want 2 (wip-limits and offline)", body.AssertedCount)
	}
}

// §4.7's promise is "worked for 83% of reporters". The denominator is the part
// that carries the meaning, and the no-data case must be distinguishable from a
// zero rate — a project nobody has tried is not a project that failed.
func TestAFieldReportRateIsServedWithItsSampleSizeAndAbsentWhenThereIsNoData(t *testing.T) {
	ts, _, slug, _, _ := seedPanels(t)

	type frBody struct {
		SampleSize  int                `json:"sample_size"`
		OutcomeRate *float64           `json:"outcome_rate"`
		ByEnv       map[string]float64 `json:"by_environment"`
		Reports     []struct {
			Outcome   string `json:"outcome"`
			Responses []struct {
				Body    string `json:"body"`
				IsOwner bool   `json:"is_owner"`
			} `json:"responses"`
		} `json:"reports"`
		Shown   int `json:"shown"`
		Omitted int `json:"omitted"`
	}

	resp, raw := doJSON(t, ts, "GET", "/api/v1/projects/"+slug+"/field-reports", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET field-reports = %d: %v", resp.StatusCode, raw)
	}
	b, _ := json.Marshal(raw)
	var out frBody
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if out.SampleSize != 3 {
		t.Errorf("sample_size = %d, want 3", out.SampleSize)
	}
	if out.OutcomeRate == nil {
		t.Fatal("outcome_rate is absent for a project with three reports")
	}
	// One worked (1.0), one worked-with-caveats (CaveatCredit), one abandoned
	// (0). Asserted as a RANGE rather than a hand-computed constant: the point
	// is that the number is a weighted mean of three outcomes, and pinning it to
	// a literal would make this test fail the day CaveatCredit is retuned for
	// a defensible reason instead of silently passing a wrong one.
	if *out.OutcomeRate <= 0 || *out.OutcomeRate >= 1 {
		t.Errorf("outcome_rate = %v; one of three worked and one abandoned, so the "+
			"rate must be strictly between 0 and 1", *out.OutcomeRate)
	}
	if len(out.ByEnv) == 0 {
		t.Error("by_environment is empty; §4.7's \"worked for 83% of reporters on arm64\" " +
			"needs the per-environment split")
	}
	if out.Shown != 3 || out.Omitted != 0 {
		t.Errorf("shown=%d omitted=%d, want 3/0", out.Shown, out.Omitted)
	}

	// A project with no reports at all: a rate is NOT 0.
	emptySlug, _ := makeProject(t, ts, "no-reports-project")
	resp, raw = doJSON(t, ts, "GET", "/api/v1/projects/"+emptySlug+"/field-reports", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("empty project = %d: %v", resp.StatusCode, raw)
	}
	b, _ = json.Marshal(raw)
	var empty frBody
	if err := json.Unmarshal(b, &empty); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if empty.SampleSize != 0 {
		t.Errorf("sample_size = %d for a project with no reports", empty.SampleSize)
	}
	if empty.OutcomeRate != nil {
		t.Errorf("outcome_rate = %v for a project with no reports; it must be absent, "+
			"because 0%% means reporters said it did not work and nil means nobody has "+
			"reported — a different claim about the project", *empty.OutcomeRate)
	}
	if empty.OutcomeRate != nil && *empty.OutcomeRate == 0 {
		t.Error("outcome_rate is 0 rather than absent")
	}
}

// The panel is bounded, and it says what it left out. Truncating a list of 400
// reports without a count reads as "there are ten".
func TestTheFieldReportPanelIsBoundedAndSaysWhatItOmitted(t *testing.T) {
	ts, db := newTestServerWithStore(t)
	ctx := context.Background()
	slug, pid := makeProject(t, ts, "many-reports")
	author, err := db.CreateUser(ctx, "prolific", "Prolific")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	for i := 0; i < FieldReportPageLimit+3; i++ {
		if _, err := db.CreateFieldReport(ctx, store.FieldReport{
			ProjectID: pid, UserID: author.ID,
			Version: "1.0", UseCase: "testing", Outcome: "worked",
		}); err != nil {
			t.Fatalf("CreateFieldReport %d: %v", i, err)
		}
	}

	resp, raw := doJSON(t, ts, "GET", "/api/v1/projects/"+slug+"/field-reports", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("= %d: %v", resp.StatusCode, raw)
	}
	b, _ := json.Marshal(raw)
	var out struct {
		Reports []json.RawMessage `json:"reports"`
		Shown   int               `json:"shown"`
		Omitted int               `json:"omitted"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Reports) != FieldReportPageLimit {
		t.Errorf("rendered %d reports, want the page limit %d", len(out.Reports), FieldReportPageLimit)
	}
	if out.Omitted != 3 {
		t.Errorf("omitted = %d, want 3; the panel must say what it left out", out.Omitted)
	}
}

// A removed report is hidden, not deleted. Serving it here would silently undo
// the quorum decision that removed it, while the row stays for the moderation
// record.
//
// The removal is performed by a NON-member: RemoveFieldReport refuses when the
// caller's role is anything but "guest", because §4.7 gives owners a response
// right and not a deletion one ("Owners can respond but cannot delete
// reports"). A test that removed as the owner would be testing a refusal.
func TestARemovedFieldReportIsNotServedButStaysOutOfTheRate(t *testing.T) {
	ts, db, slug, _, _ := seedPanels(t)
	ctx := context.Background()

	reports, err := db.ListFieldReports(ctx, mustProjectIDBySlug(t, db, slug), false)
	if err != nil || len(reports) == 0 {
		t.Fatalf("ListFieldReports: %v (%d)", err, len(reports))
	}
	moderator, err := db.CreateUser(ctx, "quorum-moderator", "Quorum Moderator")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.RemoveFieldReport(ctx, reports[0].ID, moderator.ID, "fabricated"); err != nil {
		t.Fatalf("RemoveFieldReport: %v", err)
	}

	resp, raw := doJSON(t, ts, "GET", "/api/v1/projects/"+slug+"/field-reports", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("= %d: %v", resp.StatusCode, raw)
	}
	b, _ := json.Marshal(raw)
	var out struct {
		SampleSize int `json:"sample_size"`
		Reports    []struct {
			ID      int64 `json:"id"`
			Removed bool  `json:"removed"`
		} `json:"reports"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, r := range out.Reports {
		if r.ID == reports[0].ID {
			t.Errorf("removed report %d is still served; removal is hidden, not ignored", r.ID)
		}
	}
	// And it is out of the denominator, not merely out of the list: the rate must
	// reflect the two reports that remain.
	if out.SampleSize != 2 {
		t.Errorf("sample_size = %d after a removal, want 2", out.SampleSize)
	}
}

// THE test for this milestone's first rule. Every project-scoped route funnels
// through requireProjectID, which answers 404 rather than 403 — a 403 would
// confirm the slug exists and make the instance enumerable. If a future refactor
// gives these two handlers their own project lookup, this is what catches it.
func TestAPrivateProjectsPanelsAreNotServedAndTheRefusalIsNotDistinguishable(t *testing.T) {
	ts, db, slug, _, _ := seedPanels(t)
	ctx := context.Background()

	if _, err := db.SetProjectVisibility(ctx, slug, "private"); err != nil {
		t.Fatalf("SetProjectVisibility: %v", err)
	}
	stranger := registerAndLogin(t, ts, "stranger")

	for _, path := range []string{"/capabilities", "/field-reports"} {
		code, body := authGet(t, ts, "/api/v1/projects/"+slug+path, stranger)
		if code != http.StatusNotFound {
			t.Errorf("%s as a stranger = %d, want 404; 403 would confirm the slug exists",
				path, code)
		}
		if strings.Contains(string(body), "wip-limits") ||
			strings.Contains(string(body), "worked") {
			t.Errorf("%s leaked private project data: %s", path, body)
		}
		// Indistinguishable from a project that does not exist.
		missingCode, missingBody := authGet(t, ts,
			"/api/v1/projects/no-such-project-at-all"+path, stranger)
		if missingCode != code {
			t.Errorf("%s: absent project = %d, forbidden project = %d; the two must "+
				"be indistinguishable", path, missingCode, code)
		}
		if strings.TrimSpace(string(body)) != strings.TrimSpace(string(missingBody)) {
			t.Errorf("%s: absent body %q vs forbidden body %q", path, missingBody, body)
		}
	}
}

// A public project stays readable. The visibility test above would also pass if
// these handlers refused EVERYTHING, which is the failure mode of a test that
// only checks refusals.
//
// Each route is checked for what it actually carries. The first draft asserted
// the seeded capability appeared in BOTH responses, so the field-reports route
// failed for the right reason with a misleading message.
func TestAPublicProjectsPanelsAreReadableByAnAnonymousCaller(t *testing.T) {
	ts, _, slug, _, _ := seedPanels(t)

	code, body := authGet(t, ts, "/api/v1/projects/"+slug+"/capabilities", anonymousMarker)
	if code != http.StatusOK {
		t.Errorf("/capabilities anonymously = %d, want 200: %s", code, body)
	}
	if !strings.Contains(string(body), "wip-limits") {
		t.Errorf("/capabilities anonymously: response omits the seeded capability: %s", body)
	}

	code, body = authGet(t, ts, "/api/v1/projects/"+slug+"/field-reports", anonymousMarker)
	if code != http.StatusOK {
		t.Errorf("/field-reports anonymously = %d, want 200: %s", code, body)
	}
	if !strings.Contains(string(body), "worked") {
		t.Errorf("/field-reports anonymously: response omits the seeded reports: %s", body)
	}
}

// A project id that is not a number must be a 404, not project 0.
//
// Twenty-one handlers in this codebase parsed {project_id} with strconv.ParseInt
// while the parameter is filled with a slug; they queried project 0 and the
// tests missed it because "no rows for project 0" is still a 200. These two
// routes use requireProjectID for exactly that reason, and this asserts the
// consequence rather than the mechanism.
func TestANonSlugProjectPathIsANotFoundRatherThanAnEmptyProject(t *testing.T) {
	ts, _, _, _, _ := seedPanels(t)
	for _, path := range []string{"/capabilities", "/field-reports"} {
		code, _ := authGet(t, ts, "/api/v1/projects/0"+path, anonymousMarker)
		if code != http.StatusNotFound {
			t.Errorf("/projects/0%s = %d, want 404; a numeric id in this slot must not "+
				"resolve to project 0 and return an empty panel", path, code)
		}
	}
}

// ---------------------------------------------------------------------------

func mustProjectIDBySlug(t *testing.T, db *store.DB, slug string) int64 {
	t.Helper()
	p, err := db.GetProject(context.Background(), slug)
	if err != nil {
		t.Fatalf("GetProject(%s): %v", slug, err)
	}
	return p.ID
}
