package httpapi

import (
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Comments on a feature and a complaint.
//
// This is NOT a new page: the API is thread-shaped
// (/api/v1/projects/{id}/threads/{kind}/{thread_id}) and it is complete -- create, list,
// vote, delete, all tested at the store layer. What was missing is any way to REACH
// it. A grep for "threads/" across internal/httpapi/assets/js returned nothing: four
// working endpoints and a write-only surface, the same shape as the feature_complaints
// table this repo already found once.
//
// So the contract here is not "a page renders". It is:
//
//  1. both detail pages RENDER a comment thread
//  2. the thread hits the thread-shaped URL, not /comments
//  3. it shows a THREADED comment with its parent, because ParentID is the column
//  4. it can post, and the optimistic UI rolls back when the POST fails
//  5. a comment with no label renders -- Label is nullable and omitempty, so the
//     ordinary case has no label at all
//  6. the two pages share ONE script rather than two copies that drift

// TestBothDetailPagesRenderACommentThread is part 1.
func TestBothDetailPagesRenderACommentThread(t *testing.T) {
	ts := newTestServer(t)
	seedFeatureForPage(t, ts)

	for _, page := range []struct {
		name string
		path string
	}{
		{"feature", "/projects/pagefeat/features/1"},
		{"complaint", "/projects/pagefeat/complaints/1"},
	} {
		code, body := getBody(t, ts.URL+page.path)
		if code != http.StatusOK {
			t.Fatalf("%s page returned %d", page.name, code)
		}
		for _, want := range []string{"comment-thread", "/assets/js/comments.js"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s page missing %q; the comment thread has no mount point "+
					"and four working endpoints stay unreachable", page.name, want)
			}
		}
	}
}

// TestTheCommentThreadUsesTheThreadShapedURL is part 2.
//
// Comments are NOT at /comments. They are at
// /api/v1/projects/{project_id}/threads/{thread_kind}/{thread_id}, which is what lets
// one table carry comments on complaints, features, merge requests, releases and
// projects. A page that guessed /comments would 404 -- and the JS maps a failed fetch
// to an empty array, so the page would show "no comments" rather than an error.
func TestTheCommentThreadUsesTheThreadShapedURL(t *testing.T) {
	src, err := os.ReadFile("assets/js/comments.js")
	if err != nil {
		t.Fatalf("read comments.js: %v", err)
	}
	for _, want := range []string{
		"/threads/",
		"thread_kind",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("comments.js missing %q; the endpoint is "+
				"/api/v1/projects/{id}/threads/{thread_kind}/{thread_id}", want)
		}
	}
	// And it must not invent a /comments path, which does not exist.
	if strings.Contains(string(src), `"/comments`) {
		t.Error("comments.js uses a /comments path; no such route is registered")
	}
}

// TestTheCommentThreadIsThreaded is part 3, and it is the substantive one.
//
// Comment.ParentID is a real nullable FK and the spec's threads are replies, so a
// thread rendered as a flat list loses the structure the schema records. A reply must
// be visibly a reply.
func TestTheCommentThreadIsThreaded(t *testing.T) {
	src, err := os.ReadFile("assets/js/comments.js")
	if err != nil {
		t.Fatalf("read comments.js: %v", err)
	}
	for _, want := range []string{
		"parent_id",
		"data-parent-id",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("comments.js does not use %q, so a reply renders as a top-level "+
				"comment and the thread structure is lost", want)
		}
	}
}

// TestAPostedCommentAppearsWithoutAReload is part 4: the write path is reachable.
func TestAPostedCommentAppearsWithoutAReload(t *testing.T) {
	src, err := os.ReadFile("assets/js/comments.js")
	if err != nil {
		t.Fatalf("read comments.js: %v", err)
	}
	if !strings.Contains(string(src), "POST") {
		t.Error("comments.js never POSTs; a comment thread nobody can write to is a " +
			"read-only view of other people's comments")
	}
}

// TestTheCommentThreadRollsBackAFailedPost is part 4b.
//
// The optimistic path -- show the comment, then send it -- is the right UX and it is
// also a lie if it survives a refusal. The store refuses a blank body and a parent from
// another thread, so a comment that stays on screen after either is a comment nobody
// wrote.
func TestTheCommentThreadRollsBackAFailedPost(t *testing.T) {
	src, err := os.ReadFile("assets/js/comments.js")
	if err != nil {
		t.Fatalf("read comments.js: %v", err)
	}
	for _, want := range []string{"catch", "error"} {
		if !strings.Contains(string(src), want) {
			t.Errorf("comments.js has no %q path; a refused comment would stay on "+
				"screen as though it were saved", want)
		}
	}
}

// TestAnUnlabelledCommentRenders is part 5.
//
// comments.label is nullable and Label carries omitempty, so the COMMON case arrives
// with no label key at all. A renderer that calls a method straight on the label
// throws on every ordinary comment and takes the whole thread with it -- which is the
// ReferenceError shape this repo has now hit twice.
func TestAnUnlabelledCommentRenders(t *testing.T) {
	src, err := os.ReadFile("assets/js/comments.js")
	if err != nil {
		t.Fatalf("read comments.js: %v", err)
	}
	if strings.Contains(string(src), "label.toUpperCase") {
		t.Error("comments.js calls a method straight on label; label is omitempty so " +
			"the ordinary comment has no label and this throws on the whole thread")
	}
}

// TestBothDetailPagesShareOneCommentScript is part 6.
//
// Two copies of the same renderer drift, and then one page keeps a bug the other has
// fixed. The mount point carries the kind and the id, so ONE script serves both.
func TestBothDetailPagesShareOneCommentScript(t *testing.T) {
	for _, tmpl := range []string{"templates/feature.html", "templates/complaint.html"} {
		data, err := os.ReadFile(tmpl)
		if err != nil {
			t.Fatalf("read %s: %v", tmpl, err)
		}
		if !strings.Contains(string(data), "/assets/js/comments.js") {
			t.Errorf("%s does not load comments.js", tmpl)
		}
		// The mount point has to say WHICH thread, or the shared script cannot know.
		if !strings.Contains(string(data), "data-thread-kind") {
			t.Errorf("%s has no data-thread-kind, so the shared script has nothing to "+
				"tell which thread it is rendering", tmpl)
		}
	}
}

// TestCommentsScriptParses is the syntax half, in CI, before any browser runs.
func TestCommentsScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	path := "assets/js/comments.js"
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if out, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
		t.Errorf("node --check %s: %v\n%s", path, err, out)
	}
}

// TestTheCommentAPIIsReachableFromADetailPage checks the whole chain with a real
// request, because the source gates above only prove the strings exist -- which is how
// a page can be entirely broken with every one of them green.
func TestTheCommentAPIIsReachableFromADetailPage(t *testing.T) {
	ts := newTestServer(t)
	owner := seedFeatureForPage(t, ts)

	if code, body := authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/pagefeat/threads/feature/1", owner,
		map[string]any{"body": "top level"}); code != http.StatusCreated {
		t.Fatalf("post comment: %d %s", code, body)
	}
	code, body := authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/pagefeat/threads/feature/1", anonymousMarker, nil)
	if code != http.StatusOK {
		t.Fatalf("read thread: %d %s", code, body)
	}
	if !strings.Contains(string(body), "top level") {
		t.Errorf("the posted comment is not in the thread it was posted to: %s", body)
	}

	// A blank body is refused, which is what makes the rollback assertion meaningful.
	if code, _ := authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/pagefeat/threads/feature/1", owner,
		map[string]any{"body": ""}); code == http.StatusCreated {
		t.Error("a blank comment was accepted")
	}

	// An unknown thread id reads empty rather than 500, so the page renders "no
	// comments" instead of an error.
	code, _ = authJSON(t, ts, http.MethodGet,
		"/api/v1/projects/pagefeat/threads/feature/9999", anonymousMarker, nil)
	if code != http.StatusOK {
		t.Errorf("reading a thread with no comments returned %d, want 200 with an "+
			"empty list", code)
	}
}

// TestACommentOnAnotherProjectsFeatureIsRefused is the ownership guard: comments are
// project-scoped, so a thread id that belongs to another project must not accept a
// post through this project's URL.
//
// This is the "guard on the path is not a guard on the row" defect from
// id_enumeration_test.go in a new place, and it is the one a source gate cannot see.
func TestACommentOnAnotherProjectsFeatureIsRefused(t *testing.T) {
	ts := newTestServer(t)
	owner := seedFeatureForPage(t, ts)

	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects", owner,
		map[string]any{"slug": "otherq", "name": "OtherQ", "description": "d"}); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("second project: %d %s", code, body)
	}
	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/otherq/complaints",
		owner, map[string]any{"project_id": 2, "title": "t", "body": "b",
			"severity": 3, "frequency": 1.0}); code != http.StatusCreated {
		t.Fatalf("complaint: %d %s", code, body)
	}
	if code, body := authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/otherq/complaints/2/validate", owner, map[string]any{}); code != http.StatusOK {
		t.Fatalf("validate: %d %s", code, body)
	}
	if code, body := authJSON(t, ts, http.MethodPost, "/api/v1/projects/otherq/features",
		owner, map[string]any{"project_id": 2, "author_id": 1, "title": "their feature",
			"body": "b", "linked_complaints": []int64{2}}); code != http.StatusCreated {
		t.Fatalf("feature: %d %s", code, body)
	}

	// Feature 1 belongs to pagefeat; post to thread feature/1 through the OTHER
	// project's URL.
	if code, body := authJSON(t, ts, http.MethodPost,
		"/api/v1/projects/otherq/threads/feature/1", owner,
		map[string]any{"body": "cross-project comment"}); code == http.StatusCreated {
		t.Errorf("a comment was accepted on a feature belonging to another project "+
			"(%s)", body)
	}
}
