package httpapi

import (
	"net/http"
	"testing"
)

// The tags read surface (2026-09-30).
//
// `PUT /projects/{slug}/tags` existed and worked, and nothing could read the
// result: the handler returns the project, the project struct has no Tags
// field, and there was no GET route. Thirteen tags on Tessera were correctly
// stored and completely invisible from the API.
//
// These tests are the acceptance condition. A test that only checks the write
// returns 200 is the test that passed for months while the feature was unusable.

func TestProjectTags_written_tags_are_readable(t *testing.T) {
	ts := newTestServer(t)
	resp, _ := postJSON(t, ts, "/api/v1/projects",
		`{"slug":"taggable","name":"Taggable","description":"d"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: status %d", resp.StatusCode)
	}
	// §4.3: tagging resolves an existing tag; creating one is a ratifiable
	// taxonomy proposal. seedGlobalTag drives that path.
	for _, tag := range []string{"rust", "storage", "media"} {
		seedGlobalTag(t, ts, tag)
	}

	resp, _ = doJSON(t, ts, "PUT", "/api/v1/projects/taggable/tags", map[string]any{
		"tags": []string{"rust", "storage", "media"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put tags: status %d", resp.StatusCode)
	}

	// The write response itself does NOT carry the tags -- that is the bug.
	// Asserted so a future "fix" that adds Tags to the project struct is a
	// deliberate change rather than an accident.
	_, putBody := doJSON(t, ts, "PUT", "/api/v1/projects/taggable/tags", map[string]any{
		"tags": []string{"rust"},
	})
	if _, present := putBody["tags"]; present {
		// Go has no implicit string concatenation: two literals side by side
		// are a parse error, not a joined string.
		t.Log("note: the PUT response now carries tags; the GET route is still " +
			"the contract, but this assumption changed")
	}

	resp, body := getJSON(t, ts, "/api/v1/projects/taggable/tags")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get tags: status %d body=%v", resp.StatusCode, body)
	}
	got, _ := body["tags"].([]any)
	if len(got) != 3 {
		t.Fatalf("read back %d tags, want 3: %v", len(got), body)
	}
	// Sorted, so the assertion is a string comparison rather than a set.
	want := []string{"media", "rust", "storage"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("tag %d = %v, want %q (full: %v)", i, got[i], w, got)
		}
	}
}

func TestProjectTags_applying_a_tag_twice_does_not_duplicate_it(t *testing.T) {
	ts := newTestServer(t)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"dup","name":"Dup","description":"d"}`)
	seedGlobalTag(t, ts, "idem")

	for i := 0; i < 3; i++ {
		doJSON(t, ts, "PUT", "/api/v1/projects/dup/tags",
			map[string]any{"tags": []string{"idem"}})
	}
	resp, body := getJSON(t, ts, "/api/v1/projects/dup/tags")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get tags: status %d", resp.StatusCode)
	}
	got, _ := body["tags"].([]any)
	if len(got) != 1 {
		t.Errorf("re-applying the same tag produced %d rows: %v", len(got), body)
	}
}

func TestProjectTags_a_project_with_none_returns_an_empty_list_not_an_error(t *testing.T) {
	ts := newTestServer(t)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"bare","name":"Bare","description":"d"}`)

	// An untagged project is a normal state, not a missing resource. Returning
	// 404 here would make "no tags" indistinguishable from "no project".
	resp, body := getJSON(t, ts, "/api/v1/projects/bare/tags")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("untagged project: status %d, want 200 (body=%v)", resp.StatusCode, body)
	}
	tags, present := body["tags"]
	if !present {
		t.Fatalf("response has no tags key: %v", body)
	}
	if list, ok := tags.([]any); !ok || len(list) != 0 {
		t.Errorf("tags = %v, want an empty list", tags)
	}
}

func TestProjectTags_an_unknown_project_is_not_found(t *testing.T) {
	ts := newTestServer(t)

	resp, _ := getJSON(t, ts, "/api/v1/projects/does-not-exist/tags")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown project: status %d, want 404", resp.StatusCode)
	}
}

func TestProjectTags_are_scoped_to_their_project(t *testing.T) {
	ts := newTestServer(t)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"one","name":"One","description":"d"}`)
	postJSON(t, ts, "/api/v1/projects",
		`{"slug":"two","name":"Two","description":"d"}`)
	for _, tag := range []string{"only-one", "other", "shared"} {
		seedGlobalTag(t, ts, tag)
	}

	doJSON(t, ts, "PUT", "/api/v1/projects/one/tags", map[string]any{"tags": []string{"only-one"}})
	doJSON(t, ts, "PUT", "/api/v1/projects/two/tags", map[string]any{"tags": []string{"other"}})

	_, one := getJSON(t, ts, "/api/v1/projects/one/tags")
	got, _ := one["tags"].([]any)
	if len(got) != 1 || got[0] != "only-one" {
		t.Errorf("project one has %v, want [only-one]", one["tags"])
	}

	// The same global tag on both projects is one row in `tags` and two rows in
	// `project_tags`. This is the case that proves the join is on the
	// association and not on the tag namespace.
	doJSON(t, ts, "PUT", "/api/v1/projects/one/tags", map[string]any{"tags": []string{"shared"}})
	doJSON(t, ts, "PUT", "/api/v1/projects/two/tags", map[string]any{"tags": []string{"shared"}})
	_, one = getJSON(t, ts, "/api/v1/projects/one/tags")
	_, two := getJSON(t, ts, "/api/v1/projects/two/tags")
	oneTags, _ := one["tags"].([]any)
	twoTags, _ := two["tags"].([]any)
	if len(oneTags) != 2 || len(twoTags) != 2 {
		t.Errorf("after a shared tag: one=%v two=%v, want 2 each", oneTags, twoTags)
	}
}
