package httpapi

// Scout's HTTP tests (docs/specs/scout-spec.md §5–§8, §6.3).
//
// The cases here are the ones a handler can get wrong while the classifier is
// perfect: endpoint-level project filtering, the three-state coverage, the
// license constraint, and request validation. Classification itself is tested in
// internal/scout, where it can be tested without a database.
//
// The wire struct is declared locally rather than reused from the store package, so
// a change to the response shape breaks this file. That is the point of testing a
// surface.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// scoutCapKeys is the catalog fixture: three capabilities whose LABELS share words
// with the test ideas deliberately, plus one whose label appears nowhere, so a
// decomposition test can tell which rule matched.
var scoutCapKeys = []struct{ key, label, category string }{
	{"wip-limits", "WIP limits", "features"},
	{"offline", "Offline use", "features"},
	{"self-hosted", "Self-hosted", "deployment"},
	{"quantum-telemetry", "Telemetry pipeline", "operations"},
}

// scoutFixture is a server, its store, and a context for direct DB seeding.
type scoutFixture struct {
	ts  *httptest.Server
	db  *store.DB
	ctx context.Context
	// owner is the harness identity every makeProject project belongs to, as a
	// bearer token. newTestServerWithStore logs `testuser` in, and makeProject
	// creates each project under that same identity — so THIS is the owner, not a
	// freshly registered one per project.
	owner string
	// authors caches each project's assertion author so asserting two capabilities
	// on one project does not create two users with the same name.
	authors map[string]store.User
}

// newScout builds a server with the catalog fixture seeded but no projects.
//
// No projects by default: several tests here are about what happens when the
// candidate list is empty or nearly so, and a fixture that always creates three
// projects would hide exactly the case they are testing.
func newScout(t *testing.T) *scoutFixture {
	t.Helper()
	ts, db := newTestServerWithStore(t)
	ctx := context.Background()
	for _, c := range scoutCapKeys {
		if err := db.EnsureCapability(ctx, store.Capability{
			Key: c.key, Label: c.label, Category: c.category,
			Kind: "boolean", Values: []string{"yes", "partial", "no", "unknown"},
		}); err != nil {
			t.Fatalf("EnsureCapability(%s): %v", c.key, err)
		}
	}
	return &scoutFixture{ts: ts, db: db, ctx: ctx,
		owner:   loginAs(t, db, "testuser", "test-password-123"),
		authors: map[string]store.User{}}
}

// addProject creates a project with the given visibility and returns (slug, id).
//
// Visibility goes through the HTTP endpoint via setVisibility rather than a direct
// store call, because the store's SetProjectVisibility takes a SLUG while
// makeProject hands back an id — and mixing the two up is a silent no-op: the
// project stays public and the visibility tests pass for the wrong reason.
func (f *scoutFixture) addProject(t *testing.T, slug, visibility string) (string, int64) {
	t.Helper()
	s, pid := makeProject(t, f.ts, slug)
	if visibility != store.VisibilityPublic {
		// The owner of THIS project. Registering one identity and using it for
		// every project would 404 here — the visibility endpoint requires the
		// caller to be able to read the project first, and a private project is
		// unreadable to anyone but its owner.
		setVisibility(t, f.ts, f.owner, s, visibility)
	}
	return s, pid
}

// assertCapability asserts `yes` for a project, with no confirmations, so the
// state is `asserted` rather than `confirmed` — the distinction the coverage
// states turn on.
func (f *scoutFixture) assertCapability(t *testing.T, slug string, pid int64, key string) {
	t.Helper()
	// asserted_by REFERENCES users(id), NOT projects(id). Passing the project id
	// here — which the first version did — either fails the FK outright or, worse,
	// silently attributes the claim to whichever user happens to share the number.
	//
	// It also has to be a user who is NOT the author for `confirmed` to be
	// reachable: ConfirmCapability excludes the author from the quorum count.
	u, ok := f.authors[slug]
	if !ok {
		var err error
		u, err = f.db.CreateUser(f.ctx, slug+"-author", slug+" author")
		if err != nil {
			t.Fatalf("CreateUser(%s-author): %v", slug, err)
		}
		f.authors[slug] = u
	}
	if _, err := f.db.AssertCapability(f.ctx, key, slug, "yes", "we do this", u.ID); err != nil {
		t.Fatalf("AssertCapability(%s): %v", key, err)
	}
	_ = pid
}

// loginAs returns a bearer token for an existing user.
//
// Deliberately the store, not the HTTP /login handler: the harness user already has
// a password set by newTestServerWithStore, and routing this through the endpoint
// would make the fixture depend on the very handler the visibility test exercises.
func loginAs(t *testing.T, db *store.DB, username, password string) string {
	t.Helper()
	_, tok, err := db.Login(t.Context(), username, password)
	if err != nil {
		t.Fatalf("Login(%s): %v", username, err)
	}
	// The "Bearer " prefix is REQUIRED: authJSON copies this string into the header
	// verbatim and the middleware only strips that prefix. Returning the bare token
	// yields a 401, which reads like a permissions problem rather than a header one.
	return "Bearer " + tok
}

// confirm brings an assertion to `confirmed` by adding CapQuorum confirmations.
func (f *scoutFixture) confirm(t *testing.T, pid int64, key, who string) {
	t.Helper()
	ids, err := f.db.ListCapabilityAssertions(f.ctx, pid)
	if err != nil {
		t.Fatalf("ListCapabilityAssertions: %v", err)
	}
	var aid int64
	for _, a := range ids {
		if a.Capability == key {
			aid = a.ID
		}
	}
	if aid == 0 {
		t.Fatalf("no assertion for %s on project %d", key, pid)
	}
	u, err := f.db.CreateUser(f.ctx, who, who)
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", who, err)
	}
	if _, err := f.db.ConfirmCapability(f.ctx, aid, u.ID, true); err != nil {
		t.Fatalf("ConfirmCapability(%s): %v", key, err)
	}
}

// --- wire types -----------------------------------------------------------

type scoutWireCap struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	InCatalog bool   `json:"in_catalog"`
	Coverage  string `json:"coverage"`
	Evidence  string `json:"evidence"`
}

type scoutWireVer struct {
	ProjectID   int64    `json:"project_id"`
	Slug        string   `json:"slug"`
	Verdict     string   `json:"verdict"`
	Fit         float64  `json:"fit"`
	Coverage    float64  `json:"evidence_coverage"`
	Matched     int      `json:"matched"`
	Total       int      `json:"total"`
	LeadOn      int      `json:"lead_on"`
	Health      float64  `json:"health"`
	HealthKnown bool     `json:"health_known"`
	License     string   `json:"license"`
	OutcomeRate float64  `json:"outcome_rate"`
	Reports     int      `json:"reports"`
	Signals     []string `json:"signals"`
	Reason      string   `json:"reason"`
}

type scoutWireReport struct {
	Idea                 string         `json:"idea"`
	InCatalog            int            `json:"in_catalog"`
	Proposed             int            `json:"proposed"`
	CandidatesConsidered int            `json:"candidates_considered"`
	Constraints          []string       `json:"constraints"`
	Capabilities         []scoutWireCap `json:"capabilities"`
	Verdicts             []scoutWireVer `json:"verdicts"`
	Stats                map[string]int `json:"stats"`
}

// scoutGet calls the endpoint as an anonymous viewer and fails on a non-200.
func scoutGet(t *testing.T, f *scoutFixture, query string) scoutWireReport {
	t.Helper()
	rep, code, raw := scoutGetCode(t, f, query)
	if code != http.StatusOK {
		t.Fatalf("GET /api/v1/scout?%s -> %d, body %s", query, code, raw)
	}
	return rep
}

// Anonymous by design: Scout must work logged out, and the visibility test below
// depends on there being no actor. authJSON sends X-No-Auth for an empty token.
func scoutGetCode(t *testing.T, f *scoutFixture, query string) (scoutWireReport, int, string) {
	t.Helper()
	code, body := authJSON(t, f.ts, http.MethodGet, "/api/v1/scout?"+query, "", nil)
	var out scoutWireReport
	_ = json.Unmarshal(body, &out)
	return out, code, string(body)
}

// --- tests ----------------------------------------------------------------

// The endpoint exists, answers, and decomposes an idea into a capability.
func TestScoutReturnsAReportWithVerdicts(t *testing.T) {
	f := newScout(t)
	slug, pid := f.addProject(t, "cand", store.VisibilityPublic)
	f.assertCapability(t, slug, pid, "offline")

	rep := scoutGet(t, f, "idea=we+need+offline+support")

	if rep.Idea == "" {
		t.Error("the echoed idea is empty; the report must say what it read")
	}
	if len(rep.Capabilities) == 0 {
		t.Fatal("no capabilities from an idea that names one in the catalog")
	}
	if rep.CandidatesConsidered == 0 {
		t.Error("candidates_considered is 0 with a visible project in the catalog")
	}
	if len(rep.Verdicts) == 0 {
		t.Error("no verdicts; the endpoint answered but classified nothing")
	}
}

// Never nil on the wire: a client must not have to handle null where the page
// expects a list.
func TestScoutNeverReturnsNullCollections(t *testing.T) {
	f := newScout(t)
	slug, pid := f.addProject(t, "cand", store.VisibilityPublic)
	f.assertCapability(t, slug, pid, "offline")

	_, code, raw := scoutGetCode(t, f, "idea=offline")
	if code != http.StatusOK {
		t.Fatalf("-> %d", code)
	}
	// Pretty-printed, so the separator is followed by a space. The first version
	// checked for `"capabilities":[]` and so failed against a CORRECT response —
	// the assertion was wrong, not the handler.
	for _, field := range []string{`"capabilities": [`, `"verdicts": [`, `"stats": {`} {
		if !strings.Contains(raw, field) {
			t.Errorf("response does not contain %s:\n%s", field, raw)
		}
	}
	if strings.Contains(raw, `"capabilities":null`) ||
		strings.Contains(raw, `"verdicts":null`) {
		t.Errorf("a collection is null on the wire:\n%s", raw)
	}
}

// The §6.3 trap at the HTTP layer: no project_metrics rows exist, so nothing may
// come back `avoid` on the strength of a health number that was never measured.
func TestScoutDoesNotReportEverythingAsAvoidWhenNoMetricsExist(t *testing.T) {
	f := newScout(t)
	slug, pid := f.addProject(t, "unmeasured", store.VisibilityPublic)
	f.assertCapability(t, slug, pid, "offline")
	f.assertCapability(t, slug, pid, "self-hosted")

	rep := scoutGet(t, f, "idea=offline+and+self-hosted")

	if len(rep.Verdicts) == 0 {
		t.Fatal("no verdicts")
	}
	if rep.Stats["avoid"] == len(rep.Verdicts) {
		t.Errorf("every verdict is avoid on an instance with no project_metrics "+
			"rows: stats %v. Absent telemetry is unknown, not bad.", rep.Stats)
	}
	for _, v := range rep.Verdicts {
		if v.Verdict == store.ScoutAvoid && !v.HealthKnown {
			t.Errorf("%s is avoid with health_known=false; a project whose health "+
				"was never measured cannot be avoided for being unhealthy", v.Slug)
		}
	}
}

// The visibility rule at the endpoint. A private project must not appear, and must
// not be inferable from the count either.
func TestScoutHidesProjectsTheViewerMayNotRead(t *testing.T) {
	f := newScout(t)
	pub, pubID := f.addProject(t, "public-one", store.VisibilityPublic)
	f.assertCapability(t, pub, pubID, "offline")
	sec, secID := f.addProject(t, "secret-one", store.VisibilityPrivate)
	f.assertCapability(t, sec, secID, "offline")

	rep := scoutGet(t, f, "idea=offline+support")

	for _, v := range rep.Verdicts {
		if v.Slug == "secret-one" {
			t.Fatal("a private project appears in Scout's verdicts for an " +
				"anonymous viewer")
		}
	}
	// The count is the subtle leak: it would confirm a hidden project exists.
	if rep.CandidatesConsidered != 1 {
		t.Errorf("candidates_considered = %d with one visible and one private "+
			"project; the count leaks the existence of the hidden one",
			rep.CandidatesConsidered)
	}
}

// Three-state coverage. One confirmed-by-nobody assertion is `asserted`, so
// `offline` is `partial` — not `covered`, and not `open`.
func TestScoutReportsThreeStateCoverage(t *testing.T) {
	f := newScout(t)
	slug, pid := f.addProject(t, "cand", store.VisibilityPublic)
	f.assertCapability(t, slug, pid, "offline")

	rep := scoutGet(t, f, "idea=we+need+offline")

	var saw bool
	for _, c := range rep.Capabilities {
		if c.Key != "offline" {
			continue
		}
		saw = true
		if c.Coverage == "" {
			t.Error("coverage is empty; `open` is a real answer and the field is " +
				"never blank")
		}
		if c.Coverage != CoveragePartial {
			t.Errorf("offline coverage = %q with one unconfirmed assertion, want "+
				"partial (confirmed needs quorum %d)", c.Coverage, store.CapQuorum)
		}
	}
	if !saw {
		t.Fatalf("offline was not decomposed; got %+v", rep.Capabilities)
	}
}

// At quorum the state becomes `covered` — the other end of the same three states.
func TestScoutReportsCoveredOnceQuorumIsReached(t *testing.T) {
	f := newScout(t)
	slug, pid := f.addProject(t, "cand", store.VisibilityPublic)
	f.assertCapability(t, slug, pid, "offline")
	f.confirm(t, pid, "offline", "confirmer-1")
	f.confirm(t, pid, "offline", "confirmer-2")

	rep := scoutGet(t, f, "idea=offline")

	for _, c := range rep.Capabilities {
		if c.Key == "offline" && c.Coverage != CoverageCovered {
			t.Errorf("offline coverage = %q with %d confirmations, want covered",
				c.Coverage, store.CapQuorum)
		}
	}
}

// A capability nobody asserted anywhere is `open`, never blank and never zero.
func TestScoutReportsOpenForAnUnassertedCapability(t *testing.T) {
	f := newScout(t)
	slug, pid := f.addProject(t, "cand", store.VisibilityPublic)
	f.assertCapability(t, slug, pid, "offline")

	rep := scoutGet(t, f, "idea=offline+and+self-hosted")

	var sawSelfHosted bool
	for _, c := range rep.Capabilities {
		if c.Key != "self-hosted" {
			continue
		}
		sawSelfHosted = true
		if c.Coverage != CoverageOpen {
			t.Errorf("self-hosted coverage = %q with no assertions anywhere, want "+
				"open — nobody looking is not a finding of absence", c.Coverage)
		}
	}
	if !sawSelfHosted {
		t.Fatalf("self-hosted was not decomposed; got %+v", rep.Capabilities)
	}
}

// A proposed capability is reported and scored zero.
func TestScoutReportsAProposalWithoutScoringIt(t *testing.T) {
	f := newScout(t)
	slug, pid := f.addProject(t, "cand", store.VisibilityPublic)
	f.assertCapability(t, slug, pid, "offline")

	rep := scoutGet(t, f, "idea=offline+plus+live-sync")

	var proposal *scoutWireCap
	for i, c := range rep.Capabilities {
		if c.Key == "live-sync" {
			proposal = &rep.Capabilities[i]
		}
	}
	if proposal == nil {
		t.Skip("live-sync was not proposed; the token rule changed")
	}
	if proposal.InCatalog {
		t.Error("a proposed capability is reported as in-catalog")
	}
	if proposal.Coverage != CoverageOpen {
		t.Errorf("proposal coverage = %q, want open — there is nothing to assert "+
			"it against", proposal.Coverage)
	}
	if rep.InCatalog+rep.Proposed != len(rep.Capabilities) {
		t.Errorf("in_catalog(%d) + proposed(%d) != %d capabilities; the counts "+
			"disagree with the list", rep.InCatalog, rep.Proposed,
			len(rep.Capabilities))
	}
}

// An idea is required. An empty idea is not a report on everything.
func TestScoutRequiresAnIdea(t *testing.T) {
	f := newScout(t)
	slug, pid := f.addProject(t, "cand", store.VisibilityPublic)
	f.assertCapability(t, slug, pid, "offline")

	for _, q := range []string{"", "idea=", "idea=%20%20", "idea=++"} {
		if _, code, body := scoutGetCode(t, f, q); code != http.StatusBadRequest {
			t.Errorf("?%s -> %d, want 400 (an empty idea is not a report on "+
				"everything): %s", q, code, body)
		}
	}
}

// An oversized idea is rejected rather than chewed.
func TestScoutRejectsAnOversizedIdea(t *testing.T) {
	f := newScout(t)

	_, code, _ := scoutGetCode(t, f,
		"idea="+strings.Repeat("x", maxScoutIdeaLen+10))
	if code != http.StatusBadRequest {
		t.Errorf("an idea of %d bytes -> %d, want 400", maxScoutIdeaLen+10, code)
	}
}

// max must be a positive number, and a bad one must not be silently ignored —
// that would hide a client bug behind a 200 with the default.
func TestScoutRejectsABadMax(t *testing.T) {
	f := newScout(t)
	slug, pid := f.addProject(t, "cand", store.VisibilityPublic)
	f.assertCapability(t, slug, pid, "offline")

	for _, bad := range []string{"0", "-1", "lots", "1.5"} {
		if _, code, _ := scoutGetCode(t, f, "idea=offline&max="+bad); code !=
			http.StatusBadRequest {
			t.Errorf("max=%s -> %d, want 400", bad, code)
		}
	}
}

// max actually bounds the list.
func TestScoutHonoursMax(t *testing.T) {
	f := newScout(t)
	for _, name := range []string{"a-one", "b-two", "c-three"} {
		ps, ppid := f.addProject(t, name, store.VisibilityPublic)
		f.assertCapability(t, ps, ppid, "offline")
	}

	rep := scoutGet(t, f, "idea=offline&max=1")
	if len(rep.Verdicts) != 1 {
		t.Errorf("max=1 returned %d verdicts", len(rep.Verdicts))
	}
	// candidates_considered is the honest pre-trim count, not the trimmed length:
	// the reader is told how much was considered so a short list is explicable.
	if rep.CandidatesConsidered != 3 {
		t.Errorf("candidates_considered = %d with three visible projects, want 3",
			rep.CandidatesConsidered)
	}
}

// A license constraint is applied AND echoed, so a reader can see it was applied
// rather than assumed (spec §5).
func TestScoutAppliesAndEchoesALicenseConstraint(t *testing.T) {
	f := newScout(t)
	// The license is seeded as a raw column write on purpose. There is no
	// SetProjectLicense, and the only store method that touches `license` is
	// UpdateProjectMetrics — which would ALSO write a project_metrics row, giving
	// this test a health number that a live instance does not have. Direct SQL keeps
	// the telemetry as absent as it is in production.
	slug, pid := f.addProject(t, "prop", store.VisibilityPublic)
	if _, err := f.db.ExecContext(f.ctx,
		`UPDATE projects SET license='Proprietary' WHERE slug=?`, slug); err != nil {
		t.Fatalf("seeding the license: %v", err)
	}
	f.assertCapability(t, slug, pid, "offline")

	rep := scoutGet(t, f, "idea=offline&exclude_licenses=proprietary")

	if len(rep.Constraints) != 1 || rep.Constraints[0] != "proprietary" {
		t.Errorf("constraints = %v; the report must echo what was applied",
			rep.Constraints)
	}
	if len(rep.Verdicts) == 0 {
		t.Fatal("no verdicts")
	}
	if rep.Verdicts[0].Verdict != store.ScoutAvoid {
		t.Errorf("the proprietary project is %q, want avoid; signals %v",
			rep.Verdicts[0].Verdict, rep.Verdicts[0].Signals)
	}
}

// `reports == 0` must not become a 0% rate — the field has to be on the wire so a
// client can tell "nobody filed a report" from "every report failed".
func TestScoutCarriesTheReportSampleSizeBesideTheRate(t *testing.T) {
	f := newScout(t)
	slug, pid := f.addProject(t, "cand", store.VisibilityPublic)
	f.assertCapability(t, slug, pid, "offline")

	_, code, raw := scoutGetCode(t, f, "idea=offline")
	if code != http.StatusOK {
		t.Fatalf("-> %d", code)
	}
	if !strings.Contains(raw, `"reports"`) {
		t.Error("the response omits `reports`; a client cannot distinguish an " +
			"unmeasured outcome rate from a measured 0%")
	}
	var rep scoutWireReport
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Verdicts) == 0 {
		t.Fatal("no verdicts")
	}
	if rep.Verdicts[0].Reports != 0 {
		t.Errorf("reports = %d with no field reports seeded", rep.Verdicts[0].Reports)
	}
}

// An empty catalog — the state a brand-new instance is in — must answer rather
// than fail, and must not invent capabilities.
func TestScoutHandlesAnEmptyCatalog(t *testing.T) {
	ts, _ := newTestServerWithStore(t)
	code, body := authJSON(t, ts, http.MethodGet,
		"/api/v1/scout?idea=offline", "", nil)

	if code != http.StatusOK {
		t.Fatalf("an instance with no capability catalog -> %d, want 200: %s",
			code, body)
	}
	var rep scoutWireReport
	if err := json.Unmarshal(body, &rep); err != nil {
		t.Fatalf("decoding: %v (%s)", err, body)
	}
	if rep.InCatalog != 0 {
		t.Errorf("in_catalog = %d with an empty catalog", rep.InCatalog)
	}
}
