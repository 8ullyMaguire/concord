package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// readerRow declares what reads one API surface.
//
// Reader names a file under internal/httpapi/assets/js; Anchor is a string that
// must appear in it. The pair is deliberately weak on its own -- a file that
// contains "/rank/composite" is doing something with that endpoint, and nothing
// more is claimed -- and deliberately strong in aggregate: it cannot be satisfied
// by a comment, an empty file, or a name, only by the endpoint's path being
// spelled in code the browser loads.
//
// Routes is the number of registered routes this row matches, MEASURED. It is a
// count rather than a wildcard because of what it does when somebody adds a
// route: the number stops matching, the gate goes red, and the author has to read
// the row -- its reader and its note -- before bumping it. A row that covered
// /api/v1/projects/{project_id}/features and every future sub-route under it would
// be a gate that can never fire for the next feature.
//
// Exempt rows carry no reader and must say why. There are four, and each is a
// surface that is not a page's business: a forge calls the webhook, an operator
// releases an emergency hold, a probe reads /healthz, and /version belongs to
// deployment tooling.
type readerRow struct {
	Match  string
	Reader string
	Anchor string
	Routes int
	Note   string
}

// readerInventory is the answer to "which page reads this endpoint?", written
// down once and checked by the compiler of record (this test) rather than by hand.
//
// The hand-built version of this table -- docs/specs/frontend-spec.md §"inventory"
// -- is what this replaces. It was wrong within two passes: it predates the merge
// queue and the criteria table, and it never listed composite rank, the coverage
// contest or the objection resolver at all. Its author (this repository's) measured
// it twice by hand and found something missing both times.
var readerInventory = []readerRow{
	// --- auth and session ---------------------------------------------------
	{Match: "/auth", Reader: "session.js", Anchor: "/auth/", Routes: 5,
		Note: "login, logout, register, me, redeem-invite; authHeaders() gates every write"},
	// --- projects ------------------------------------------------------------
	{Match: "/api/v1/projects/{project_id}/capabilities", Reader: "project.js", Anchor: "capabilities",
		Routes: 1, Note: "which controls the page offers"},
	{Match: "/api/v1/projects/{project_id}/charter", Reader: "settings.js", Anchor: "charter",
		Routes: 2, Note: "read and update"},
	{Match: "/api/v1/projects/{project_id}/members", Reader: "settings.js", Anchor: "/members",
		Routes: 1},
	{Match: "/api/v1/projects/{project_id}/audit", Reader: "audit.js", Anchor: "/audit",
		Routes: 2},
	{Match: "/api/v1/projects/{project_id}/board", Reader: "board.js", Anchor: "board",
		Routes: 2, Note: "read the board, move a card"},
	{Match: "/api/v1/projects/{project_id}/complaints", Reader: "complaint.js", Anchor: "/complaints",
		Routes: 6},
	// The two verdicts on a complaint get their own rows, and their own reader. The
	// coarse row above matched them by the word "/complaints" alone -- which
	// complaint.js satisfies while it renders no verdict control at all -- and the
	// buttons live in project.js beside the status badge. Same reasoning as the close
	// route below: a row broad enough to hide a route is where routes hide.
	// (Longest fragment wins, so a specific row must be written in FULL -- a short
	// "/complaints/{id}/validate" loses to the row above it and matches nothing.)
	//
	// THE ANCHORS ARE THE RENDERED BUTTON, not the attribute name. The first version of
	// these two rows said "data-validate"/"data-reject", and a mutant that deleted the
	// button from the renderer SURVIVED: both strings appear a second time in the click
	// wiring a few hundred lines below, so the row could not tell a control that renders
	// from a control that is merely mentioned. This file already warns about exactly
	// that ("the served HTML would contain /complaints/ through the script's own
	// source"); the anchor has to be the markup only the renderer can produce.
	{Match: "/api/v1/projects/{project_id}/complaints/{id}/validate", Reader: "project.js",
		Anchor: `data-validate="' + esc(c.id) + '">Validate</button>`, Routes: 1},
	{Match: "/api/v1/projects/{project_id}/complaints/{id}/reject", Reader: "project.js",
		Anchor: `data-reject="' + esc(c.id) + '">Reject as spam</button>`, Routes: 1,
		Note: "§M1's Rejected state: the control the API offered no way to reach"},
	{Match: "/api/v1/projects/{project_id}/consensus/objections", Reader: "consensus.js",
		Anchor: "objections/", Routes: 1, Note: "the maintainer's resolution of an objection (§6.6)"},
	// The close route gets its own row rather than riding the fragment above it: the
	// coarse row matched nine routes whose only anchor was the word "/consensus", which
	// is satisfied by a page that merely reads a call. "Who can close a call, and what
	// the close records" is a different act with its own reader, and it went unreached
	// for exactly as long as the row was broad enough to hide it.
	{Match: "/api/v1/projects/{project_id}/consensus/{call_id}/close", Reader: "consensus.js",
		Anchor: "/close", Routes: 1,
		Note: "§6.8's close: the verdict, including `blocked`, written down"},
	// And §6.6's second window gets its own row for the same reason. It is four routes
	// under a call that already has a reader, so it would have ridden the coarse row --
	// and the second window is the ONLY way a block is ever answered, which makes it
	// the most consequential thing in the subtree to have no reader.
	{Match: "/api/v1/projects/{project_id}/consensus/{call_id}/override", Reader: "consensus.js",
		Anchor: "/override", Routes: 4,
		Note: "§6.6's override window: open, read, vote, close"},
	// §6.5's three per-call surfaces, split out of the coarse row for the same reason
	// as the two above: they are what a SOLUTION call is decided with, and "a page that
	// reads a call" satisfied the row that hid them.
	//
	// The decision record is the one that matters most. It is written by /outcome, and
	// until this pass that route refused any call that had been closed -- which is the
	// order the page itself produces -- so §6.5's record was writable only by not
	// closing the call, and no reader existed to notice.
	{Match: "/api/v1/projects/{project_id}/consensus/{call_id}/outcome", Reader: "consensus.js",
		Anchor: "/outcome", Routes: 1,
		Note: "§6.5's five outcomes: the decision record attached to the feature is written here"},
	{Match: "/api/v1/projects/{project_id}/consensus/{call_id}/fallback", Reader: "consensus.js",
		Anchor: "/fallback", Routes: 1,
		Note: "§6.5's pre-agreed fallback chain, read BEFORE the decision"},
	{Match: "/api/v1/projects/{project_id}/consensus/{call_id}/hold", Reader: "consensus.js",
		Anchor: "/hold", Routes: 2,
		Note: "§6.6's emergency hold: the page says why a suspended call resolves nothing"},
	{Match: "/api/v1/projects/{project_id}/consensus", Reader: "consensus.js", Anchor: "/consensus",
		Routes: 4,
		Note:   "open a call, read it, take a position, raise an objection"},
	// §29's arena loop. Four routes, and the anchors are the RENDERED controls
	// (the buttons and the vote form the panel builds), not the fetch paths: an
	// anchor that is a path stays satisfied after the control is deleted.
	{Match: "/api/v1/projects/{project_id}/arenas/{arena_id}/next-pair", Reader: "project.js",
		Anchor: "data-arena-pair", Routes: 1,
		Note: "§5.1 pair selection: the comparison a voter is asked to judge"},
	{Match: "/api/v1/projects/{project_id}/arenas/{arena_id}/votes", Reader: "project.js",
		Anchor: "data-arena-log", Routes: 1,
		Note: "§5.2's per-user vote audit view, rendered behind the Vote log control"},
	{Match: "/api/v1/projects/{project_id}/arenas/{arena_id}/verification", Reader: "project.js",
		Anchor: "arena-agrees", Routes: 1,
		Note: "§5.1 Anyone can verify a ranking; arena-agrees is the verdict the panel renders"},
	{Match: "/api/v1/projects/{project_id}/arenas/{arena_id}/recompute", Reader: "project.js",
		Anchor: "data-arena-recompute", Routes: 1,
		Note: "the repair; its control is rendered with the verification verdict"},
	{Match: "/api/v1/projects/{project_id}/criteria-profiles", Reader: "criteria.js",
		Anchor: "/criteria-profiles", Routes: 2, Note: "saved weightings: list and create"},
	{Match: "/api/v1/projects/{project_id}/criteria", Reader: "criteria.js", Anchor: "/criteria",
		Routes: 4, Note: "the comparison table and a criterion's own vote"},
	{Match: "/api/v1/projects/{project_id}/features/{id}/solutions/{solution_id}/coverage", Reader: "feature.js",
		Anchor: "coverage/", Routes: 2, Note: "claim and contest a coverage claim (§6.4)"},
	{Match: "/api/v1/projects/{project_id}/features", Reader: "feature.js", Anchor: "tag-pill",
		// The anchor is the RENDERED pill, not the "/features/" path in the fetch table: a
		// path is present whether or not any control exists, so an anchor like it stays
		// satisfied after the tags UI is deleted (the lesson §24 records).
		Routes: 15},
	// §354's extra link, with its own row because the coarse one above is satisfied by the
	// tags panel alone -- a route whose only reader is "a page that reads a feature" is how
	// the link went unbuilt for the life of the schema. The anchor is the RENDERED option:
	// the wiring refers to the select by id, so an anchor naming the route or the control's
	// attribute would be satisfied by a page that renders neither.
	{Match: "/api/v1/projects/{project_id}/features/{id}/complaints", Reader: "feature.js",
		Anchor: `'<option value="' + esc(c.id) + '">#'`, Routes: 2,
		Note: "the read, and the write that pairs with it (store.LinkComplaint had no caller)"},
	{Match: "/api/v1/projects/{project_id}/lists", Reader: "lists.js", Anchor: "/lists",
		Routes: 6},
	{Match: "/api/v1/projects/{project_id}/merge", Reader: "merge.js", Anchor: "/merge",
		Routes: 5, Note: "the merge queue (fifth pass)"},
	{Match: "/api/v1/projects/{project_id}/alternatives", Reader: "project.js", Anchor: "/alternatives",
		Routes: 4},
	{Match: "/api/v1/projects/{project_id}/rank/composite", Reader: "criteria.js", Anchor: "/rank/composite",
		Routes: 1, Note: "ONE request builds the whole comparison table"},
	{Match: "/api/v1/projects/{project_id}/votes", Reader: "rank.js", Anchor: "/votes",
		Routes: 1, Note: "the next thing to vote on"},
	{Match: "/api/v1/projects/{project_id}/priorities", Reader: "ranking.js", Anchor: "priorities",
		Routes: 1},
	{Match: "/api/v1/projects/{project_id}/tallies", Reader: "ranking.js", Anchor: "tallies",
		Routes: 1},
	{Match: "/api/v1/projects/{project_id}/field-reports", Reader: "project.js", Anchor: "field-reports",
		// §28 added the two writes; this row counts the two at THIS path (the panel's read and the
		// filing form). The reply route is below, on its own row and with its own anchor, because a
		// coarse row satisfied by the panel's read is exactly how a write went unbuilt until §28.
		Routes: 2},
	{Match: "/api/v1/projects/{project_id}/field-reports/{report_id}/responses", Reader: "project.js",
		// The RENDERED reply form, not the route or the fetch path: `data-respond-report` appears
		// only in the form the panel builds per report, so an anchor like the path would stay
		// satisfied after the reply control is deleted.
		Anchor: "data-respond-report", Routes: 1,
		Note: "§28's reply write: the store's RespondToFieldReport, reachable only from tests before it"},
	{Match: "/api/v1/projects/{project_id}/strategy/weight-proposals", Reader: "settings.js",
		// Rendered markup, not the fetch path: the withdrawal control is what has to exist.
		Anchor: "data-withdraw-weight", Routes: 4,
		Note: "list, propose, consent, withdraw (§6.2)"},
	{Match: "/api/v1/projects/{project_id}/strategy/themes", Reader: "settings.js",
		Anchor: "data-strategy-themes", Routes: 4,
		Note: "list live themes, propose, consent, withdraw (§6.2)"},
	{Match: "/api/v1/projects/{project_id}/strategy/theme-proposals", Reader: "settings.js",
		Anchor: "data-theme-proposals", Routes: 1,
		Note: "the pending theme changes, with their consent state (§6.2)"},
	{Match: "/api/v1/projects/{project_id}/threads", Reader: "comments.js", Anchor: "/threads",
		Routes: 4},
	{Match: "/api/v1/projects/{project_id}/join", Reader: "project.js", Anchor: "/join",
		Routes: 1, Note: "the Join button on a project page"},
	{Match: "/api/v1/projects/{slug}/documents", Reader: "documents.js", Anchor: "/documents",
		Routes: 6},
	{Match: "/api/v1/projects/{slug}/invites", Reader: "settings.js", Anchor: "/invites",
		Routes: 3},
	{Match: "/api/v1/projects/{slug}/languages", Reader: "settings.js", Anchor: "/languages",
		Routes: 2},
	{Match: "/api/v1/projects/{slug}/tags", Reader: "settings.js", Anchor: "/tags",
		Routes: 3, Note: "tags, and the completeness figure beside them"},
	{Match: "/api/v1/projects/{slug}/visibility", Reader: "settings.js", Anchor: "/visibility",
		Routes: 1},
	{Match: "/api/v1/projects", Reader: "projects.js", Anchor: "/api/v1/projects",
		Routes: 6, Note: "the index, the project page's own GET, and the panels that" +
			" hang off it without a route of their own"},
	// --- the reading half ----------------------------------------------------
	{Match: "/api/v1/requests", Reader: "requests.js", Anchor: "/api/v1/requests",
		Routes: 9},
	{Match: "/api/v1/answers", Reader: "requests.js", Anchor: "/answers", Routes: 1},
	{Match: "/api/v1/finder", Reader: "finder.js", Anchor: "/finder", Routes: 7},
	{Match: "/api/v1/lists", Reader: "lists.js", Anchor: "/lists", Routes: 1},
	{Match: "/api/v1/scout", Reader: "scout.js", Anchor: "/scout", Routes: 1},
	{Match: "/api/v1/search", Reader: "search.js", Anchor: "/search", Routes: 1},
	{Match: "/api/v1/similar", Reader: "project.js", Anchor: "/similar", Routes: 1},
	{Match: "/api/v1/taxonomy", Reader: "taxonomy.js", Anchor: "/taxonomy", Routes: 4},
	// --- guests, and the notice that gates them --------------------------------
	//
	// Both routes are read by consent.js, which every page loads from base.html.
	// The anchor is the Accept button: it is the control the file exists for, and
	// the one a page cannot render without the file.
	{Match: "/api/v1/guest", Reader: "consent.js", Anchor: "cookie-accept", Routes: 2,
		Note: "mint a guest identity for a visitor who has accepted the notice, and read whether guest interaction is still open"},
	{Match: "/api/v1/consent", Reader: "consent.js", Anchor: "cookie-accept", Routes: 1,
		Note: "accepting the cookie notice; the interaction gate's own door, so it is exempt from the gate it opens"},
	// --- surfaces that are not a page's business ------------------------------
	{Match: "/api/v1/hooks", Routes: 1, Note: "a forge POSTs here; nothing in the browser may"},
	{Match: "/api/v1/emergency-holds", Routes: 1, Note: "an operator releases a hold out of band; the ledger records it. This route had NO caller AND a project id its URL does not carry, so it answered 404 for everyone and nothing could notice -- the API test is the caller of record now (§21.7)"},
	{Match: "/api/v1/healthz", Routes: 1, Note: "the deploy probe and make deploy read it"},
	{Match: "/api/v1/version", Routes: 1, Note: "deploy tooling; every JS hit for 'version' is a cache-buster, not this route"},
	{Match: "/api/v1/admin-ledger", Routes: 1, Note: "deliberate: no UI (REMAINING §11)"},
	{Match: "/api/v1/projects/{slug}/metrics", Routes: 1, Note: "deliberate: no UI (REMAINING §11)"},
}

// TestEveryAPIRouteIsDeclaredWithAReaderOrAReason walks the router that the binary
// actually serves -- not a list of routes kept beside it, which is the artefact
// that goes stale -- and checks the inventory against it in both directions.
func TestEveryAPIRouteIsDeclaredWithAReaderOrAReason(t *testing.T) {
	routes, ok := newTestServerNoActor(t).Config.Handler.(chi.Routes)
	if !ok {
		t.Fatal("Router() is not a chi.Routes; the walk needs the mux itself")
	}
	var api []string
	if err := chi.Walk(routes, func(method, route string, _ http.Handler,
		_ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/api/") {
			api = append(api, method+" "+route)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(api) < 100 {
		t.Fatalf("walked %d API routes; the router has ~130 and a walk that finds a "+
			"handful is measuring nothing", len(api))
	}

	// Longest fragment wins, so a specific row is never shadowed by a general one.
	covered := make([][]string, len(readerInventory))
	var undeclared []string
	for _, route := range api {
		best, bestLen := -1, -1
		for i, row := range readerInventory {
			if strings.Contains(route, row.Match) && len(row.Match) > bestLen {
				best, bestLen = i, len(row.Match)
			}
		}
		if best < 0 {
			undeclared = append(undeclared, route)
			continue
		}
		covered[best] = append(covered[best], route)
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		t.Errorf("%d API route(s) have no reader declared:\n  %s\n"+
			"Add a row to readerInventory naming the page that reads it (Reader + "+
			"Anchor), or a row saying why no page should (Note). A route nobody "+
			"reads is how the merge gate, the criteria table and the settings page "+
			"were each found.",
			len(undeclared), strings.Join(undeclared, "\n  "))
	}

	for i, row := range readerInventory {
		if len(covered[i]) != row.Routes {
			sort.Strings(covered[i])
			t.Errorf("	{Match: %q, Reader: %q, Anchor: %q, Routes: %d}, // was %d\n"+
				"    the count changed: a route was added or removed under %q. Read the\n"+
				"    row, decide what should read the new route, then bump it. It now covers:\n      %s",
				row.Match, row.Reader, row.Anchor, len(covered[i]), row.Routes, row.Match,
				strings.Join(covered[i], "\n      "))
		}
		if row.Reader == "" {
			if row.Note == "" {
				t.Errorf("row %q has neither a reader nor a reason", row.Match)
			}
			continue
		}
		full := filepath.Join("assets", "js", row.Reader)
		body, err := os.ReadFile(full)
		if err != nil {
			t.Errorf("row %q names reader %s, which does not exist: %v", row.Match, full, err)
			continue
		}
		if !strings.Contains(string(body), row.Anchor) {
			t.Errorf("row %q names %s as its reader, but %q does not appear in it: "+
				"the claim is not backed by a reference to the endpoint",
				row.Match, full, row.Anchor)
		}
	}
}

// TestTheReaderInventoryDoesNotClaimRoutesThatDoNotExist is the other direction.
// A row left behind by a deleted route keeps asserting a reader for something that
// is not served, and the forward test cannot see it -- every route is covered and
// the gate is green with a dead claim in it.
func TestTheReaderInventoryDoesNotClaimRoutesThatDoNotExist(t *testing.T) {
	routes, ok := newTestServerNoActor(t).Config.Handler.(chi.Routes)
	if !ok {
		t.Fatal("Router() is not a chi.Routes")
	}
	var api []string
	if err := chi.Walk(routes, func(_ string, route string, _ http.Handler,
		_ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/api/") {
			api = append(api, route)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, row := range readerInventory {
		found := false
		for _, route := range api {
			if strings.Contains(route, row.Match) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("row %q matches no registered route; either the route was "+
				"removed (drop the row) or the fragment is misspelled (fix it)", row.Match)
		}
	}
	_ = fmt.Sprintf
}
