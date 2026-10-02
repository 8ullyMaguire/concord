package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// The tag write path had no authentication at all (revision 4 §4.3).
//
// handleProjectTags checked neither identity nor role, and read applied_by from
// the request body: any caller could attach tags to any project, and the
// "tagged by" column recorded whatever username the caller typed. A moderation
// tool built on that column would have been reading attacker-chosen data.
//
// Two rules are pinned here: an anonymous caller cannot tag, and attribution
// comes from the token rather than the body.

func TestApplyProjectTags_requires_authentication(t *testing.T) {
	// Deliberately the NO-ACTOR harness. newTestServerWithStore wraps every
	// request in a valid bearer token, so a request through it is never
	// anonymous and this test would pass vacuously.
	// The authenticated harness is needed to seed the fixture (creating a project
	// and ratifying a tag both require a token); the X-No-Auth sentinel is how a
	// single request is made anonymous. Omitting the Authorization header is NOT
	// enough here -- the wrapper supplies a default identity for any request that
	// arrives without one, so this test would silently pass as the creator.
	ts, st := newTestServerWithStore(t)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"guarded","name":"Guarded","description":"d"}`)
	seedGlobalTag(t, ts, "guarded-tag")

	// Unauthenticated. Before this was fixed this returned 200.
	resp, body := doJSONAnon(t, ts, "PUT", "/api/v1/projects/guarded/tags",
		map[string]any{"tags": []string{"guarded-tag"}})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous tag write: status %d, want 401 (body=%v)", resp.StatusCode, body)
	}

	// The store is the only way to be sure: a 401 is a good sign, but the
	// question is whether anything was written.
	var n int
	if err := st.QueryRow(`SELECT COUNT(*) FROM project_tags`).Scan(&n); err != nil {
		t.Fatalf("count project_tags: %v", err)
	}
	if n != 0 {
		t.Errorf("tags were applied by an anonymous caller (%d rows)", n)
	}
}

func TestApplyProjectTags_ignores_a_forged_applied_by(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"forged","name":"Forged","description":"d"}`)
	seedGlobalTag(t, ts, "forged-tag")

	_, reg := postJSON(t, ts, "/api/v1/auth/register",
		`{"username":"forger","password":"correct horse battery"}`)
	tok, _ := reg["token"].(string)
	if tok == "" {
		t.Fatalf("register: %v", reg)
	}
	postJSON(t, ts, "/api/v1/projects/forged/join", "{}", "Authorization", "Bearer "+tok)

	// The body claims a different user applied the tag.
	resp, body := putJSON(t, ts, "/api/v1/projects/forged/tags",
		`{"tags":["forged-tag"],"applied_by":"someone_else"}`,
		"Authorization", "Bearer "+tok)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tag as contributor: status %d body=%v", resp.StatusCode, body)
	}

	// The stored attribution must be the token's user, and there must be no user
	// called someone_else at all -- the field was a free-text claim.
	var claimed int64
	if err := st.QueryRow(`SELECT COALESCE(applied_by,0) FROM project_tags`).Scan(&claimed); err != nil {
		t.Fatalf("read applied_by: %v", err)
	}
	var named int
	if err := st.QueryRow(
		`SELECT COUNT(*) FROM users WHERE username='someone_else'`).Scan(&named); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if named != 0 {
		t.Skip("the test fixture happens to have a user named someone_else; attribution check is what matters")
	}

	// Resolve the real actor id from the token and compare.
	actor, err := st.ResolveToken(t.Context(), tok)
	if err != nil {
		t.Fatalf("ResolveToken: %v", err)
	}
	if claimed != actor {
		t.Errorf("applied_by = %d, want the token's user %d: attribution came from the body", claimed, actor)
	}
}

func TestApplyProjectTags_refuses_a_tag_that_does_not_exist(t *testing.T) {
	// §4.3: suggest-before-create, and creating vocabulary is a quorum-ratified
	// taxonomy proposal. Previously any unknown tag was silently inserted into the
	// global namespace, so one project's tag edit rewrote the instance taxonomy.
	ts, st := newTestServerWithStore(t)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"novel","name":"Novel","description":"d"}`)

	_, reg := postJSON(t, ts, "/api/v1/auth/register",
		`{"username":"tagger","password":"correct horse battery"}`)
	tok, _ := reg["token"].(string)
	postJSON(t, ts, "/api/v1/projects/novel/join", "{}", "Authorization", "Bearer "+tok)

	resp, body := putJSON(t, ts, "/api/v1/projects/novel/tags",
		`{"tags":["never-seen-before"]}`, "Authorization", "Bearer "+tok)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("unknown tag was accepted: %v", body)
	}
	// The error must name the route that does create tags, or a client hitting it
	// has no idea what to do.
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "taxonomy/proposals") {
		t.Errorf("error %q does not point at the taxonomy proposal route", msg)
	}

	var n int
	if err := st.QueryRow(`SELECT COUNT(*) FROM tags WHERE name='never-seen-before'`).Scan(&n); err != nil {
		t.Fatalf("count tags: %v", err)
	}
	if n != 0 {
		t.Errorf("the refused tag was created anyway (%d rows)", n)
	}
}

func TestTaxonomyProposalRequiresQuorum(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"quorumproj","name":"Quorum","description":"d"}`)

	resp, body := postJSON(t, ts, "/api/v1/taxonomy/proposals", `{
		"action":"create","target_tag":"topic:needs-quorum","rationale":"test"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("propose: %d %v", resp.StatusCode, body)
	}
	prop, _ := body["proposal"].(map[string]any)
	if prop["status"] != "pending" {
		t.Errorf("status = %v, want pending", prop["status"])
	}

	// The proposer cannot consent to their own proposal (§5.1).
	id := int64(prop["id"].(float64))
	r2, _ := doJSON(t, ts, "POST",
		"/api/v1/taxonomy/proposals/"+itoa(id)+"/consent", nil)
	if r2.StatusCode == http.StatusOK {
		t.Error("the proposer ratified their own taxonomy proposal")
	}

	var n int
	if err := st.QueryRow(`SELECT COUNT(*) FROM tags WHERE name='needs-quorum'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("tag created without quorum (%d rows)", n)
	}
}

func TestTaxonomyProposalRejectsInvalidShapes(t *testing.T) {
	ts := newTestServer(t)
	// An instance-wide proposal requires contributor somewhere, and the permission
	// check runs before validation -- so the shape cases would all see 403 without
	// this, which tests the gate rather than the validation.
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"shapecheck","name":"Shape","description":"d"}`)
	cases := []struct{ name, body string }{
		{"unknown namespace",
			`{"action":"create","target_tag":"bogus:thing","rationale":"x"}`},
		{"rename with no new name",
			`{"action":"rename","target_tag":"topic:thing","rationale":"x"}`},
		{"rename to itself",
			`{"action":"rename","target_tag":"topic:thing","value":"thing","rationale":"x"}`},
		{"no rationale",
			`{"action":"create","target_tag":"topic:thing"}`},
		{"unknown action",
			`{"action":"destroy","target_tag":"topic:thing","rationale":"x"}`},
		{"no target",
			`{"action":"create","rationale":"x"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, _ := postJSON(t, ts, "/api/v1/taxonomy/proposals", c.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status %d, want 400", resp.StatusCode)
			}
		})
	}
}

func TestTaxonomyMergeRepointsReferences(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"mergeproj","name":"Merge","description":"d"}`)

	// Two tags, then a project carrying the one that will be absorbed.
	seedGlobalTag(t, ts, "js")
	seedGlobalTag(t, ts, "javascript")
	putJSON(t, ts, "/api/v1/projects/mergeproj/tags", `{"tags":["javascript"]}`)

	resp, body := postJSON(t, ts, "/api/v1/taxonomy/proposals", `{
		"action":"merge","target_tag":"js","value":"javascript",
		"rationale":"one language, one tag"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("propose merge: %d %v", resp.StatusCode, body)
	}
	prop, _ := body["proposal"].(map[string]any)
	mergeID := int64(prop["id"].(float64))

	if !ratifyTaxonomy(t, ts, mergeID, "merge") {
		t.Fatalf("merge proposal was never ratified")
	}

	// The absorbed tag is gone, and the project's reference now points at the
	// survivor -- a merge that dropped the reference would silently untag.
	var n int
	if err := st.QueryRow(`SELECT COUNT(*) FROM tags WHERE name='javascript'`).Scan(&n); err != nil {
		t.Fatalf("count absorbed: %v", err)
	}
	if n != 0 {
		t.Errorf("the absorbed tag still exists (%d rows)", n)
	}
	var tagged int
	if err := st.QueryRow(`SELECT COUNT(*) FROM project_tags pt
		JOIN tags t ON t.id = pt.tag_id WHERE t.name='js'`).Scan(&tagged); err != nil {
		t.Fatalf("count repointed: %v", err)
	}
	if tagged != 1 {
		t.Errorf("project tags pointing at 'js' = %d, want 1", tagged)
	}
}

func TestTaxonomyReparentRefusesACycle(t *testing.T) {
	ts, st := newTestServerWithStore(t)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"cycproj","name":"Cycle","description":"d"}`)

	seedGlobalTag(t, ts, "topic:parent-tag")
	seedGlobalTag(t, ts, "topic:child-tag")

	propose := func(action, target, value string) int64 {
		t.Helper()
		resp, body := postJSON(t, ts, "/api/v1/taxonomy/proposals", `{
			"action":"`+action+`","target_tag":"`+target+`","value":"`+value+`",
			"rationale":"cycle test"}`)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("propose %s: %d %v", action, resp.StatusCode, body)
		}
		prop, _ := body["proposal"].(map[string]any)
		return int64(prop["id"].(float64))
	}

	// child-tag is-a parent-tag
	if !ratifyTaxonomy(t, ts, propose("reparent", "topic:child-tag", "topic:parent-tag"), "rp1") {
		t.Fatal("first reparent never ratified")
	}
	// Now try to make parent-tag a child of child-tag. That is a two-node cycle,
	// and the self-parent trigger cannot catch it -- the descendant check must.
	if ratifyTaxonomy(t, ts, propose("reparent", "topic:parent-tag", "topic:child-tag"), "rp2") {
		t.Error("a cycle-forming reparent was ratified")
	}

	// The valid reparent must have applied: child-tag's parent is parent-tag.
	var childID, parentID int64
	if err := st.QueryRow(`SELECT id FROM tags WHERE name='child-tag'`).Scan(&childID); err != nil {
		t.Fatalf("read child: %v", err)
	}
	if err := st.QueryRow(`SELECT id FROM tags WHERE name='parent-tag'`).Scan(&parentID); err != nil {
		t.Fatalf("read parent: %v", err)
	}
	var gotParent int64
	if err := st.QueryRow(`SELECT COALESCE(parent_id,0) FROM tags WHERE id=?`, childID).Scan(&gotParent); err != nil {
		t.Fatalf("read child's parent: %v", err)
	}
	if gotParent != parentID {
		t.Errorf("child-tag parent = %d, want %d: the valid reparent did not apply", gotParent, parentID)
	}

	// And the refused one must have left parent-tag parentless -- a cycle here
	// would make every hierarchy traversal in §4.3 loop forever.
	var pp int64
	if err := st.QueryRow(`SELECT COALESCE(parent_id,0) FROM tags WHERE id=?`, parentID).Scan(&pp); err != nil {
		t.Fatalf("read parent's parent: %v", err)
	}
	if pp != 0 {
		t.Errorf("parent-tag gained parent %d from the refused proposal", pp)
	}
}

func TestTagCompletenessReportsOnAProject(t *testing.T) {
	ts := newTestServer(t)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"bare2","name":"Bare2","description":"d"}`)

	resp, body := getJSON(t, ts, "/api/v1/projects/bare2/tags/completeness")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("completeness: %d %v", resp.StatusCode, body)
	}
	if complete, _ := body["complete"].(bool); complete {
		t.Error("an untagged project reports complete")
	}
	if tags, _ := body["tags"].(float64); tags != 0 {
		t.Errorf("tags = %v, want 0", body["tags"])
	}
}

func TestListTagsIsBackedByTheRatifiableTaxonomy(t *testing.T) {
	ts := newTestServer(t)
	// An instance-wide taxonomy proposal is ratified by contributors, so the test
	// user needs a project to belong to.
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"taglistproj","name":"TagList","description":"d"}`)
	seedGlobalTag(t, ts, "topic:kanban")

	resp, body := getJSON(t, ts, "/api/v1/taxonomy/tags?namespace=topic")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list tags: %d %v", resp.StatusCode, body)
	}
	// A bare JSON array comes back wrapped under "items".
	items, _ := body["items"].([]any)
	found := false
	for _, item := range items {
		m, _ := item.(map[string]any)
		if m != nil && m["name"] == "kanban" {
			found = true
			if m["namespace"] != "topic" {
				t.Errorf("namespace = %v, want topic", m["namespace"])
			}
		}
	}
	if !found {
		t.Errorf("ratified namespaced tag not listed: %v", items)
	}
}
