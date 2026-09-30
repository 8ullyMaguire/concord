package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// documentsFixture returns a DB with a project and a user.
//
// setupWithProject, not setup: setup returns only a *DB, and a document needs
// both a project_id and an author_id.
func documentsFixture(t *testing.T) (*DB, int64, int64) {
	t.Helper()
	return setupWithProject(t)
}

func TestPutDocument_creates_then_replaces(t *testing.T) {
	d, uid, pid := documentsFixture(t)
	ctx := context.Background()

	first, err := d.PutDocument(ctx, pid, uid, "readme", "readme", "Tessera", "# Tessera\n\nfirst")
	if err != nil {
		t.Fatalf("first PutDocument: %v", err)
	}
	if first.Revision != 1 {
		t.Errorf("first write: revision = %d, want 1", first.Revision)
	}
	if first.Title != "Tessera" {
		t.Errorf("title = %q, want %q", first.Title, "Tessera")
	}

	second, err := d.PutDocument(ctx, pid, uid, "readme", "readme", "Tessera", "# Tessera\n\nsecond")
	if err != nil {
		t.Fatalf("second PutDocument: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("identity changed across an update: %d then %d", first.ID, second.ID)
	}
	if second.Revision != 2 {
		t.Errorf("revision after update = %d, want 2", second.Revision)
	}
	if !strings.Contains(second.Body, "second") {
		t.Errorf("body was not replaced: %q", second.Body)
	}
}

func TestPutDocument_rejects_unknown_kind(t *testing.T) {
	d, uid, pid := documentsFixture(t)

	_, err := d.PutDocument(context.Background(), pid, uid, "blog-post", "x", "X", "body")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown kind: err = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "kind must be one of") {
		t.Errorf("error should name the accepted kinds, got %q", err.Error())
	}
}

func TestPutDocument_rejects_empty_slug(t *testing.T) {
	d, uid, pid := documentsFixture(t)

	if _, err := d.PutDocument(context.Background(), pid, uid, "readme", "  ", "X", "b"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty slug: err = %v, want ErrInvalid", err)
	}
}

func TestPutDocument_one_readme_per_project_but_many_wiki_pages(t *testing.T) {
	d, uid, pid := documentsFixture(t)
	ctx := context.Background()

	// Two different slugs in the same kind are two documents.
	a, err := d.PutDocument(ctx, pid, uid, "wiki", "install", "Install", "a")
	if err != nil {
		t.Fatalf("wiki install: %v", err)
	}
	b, err := d.PutDocument(ctx, pid, uid, "wiki", "faq", "FAQ", "b")
	if err != nil {
		t.Fatalf("wiki faq: %v", err)
	}
	if a.ID == b.ID {
		t.Error("two wiki slugs collapsed onto one document")
	}

	// The same slug is the same document, replaced.
	again, err := d.PutDocument(ctx, pid, uid, "wiki", "install", "Install", "a2")
	if err != nil {
		t.Fatalf("rewrite install: %v", err)
	}
	if again.ID != a.ID {
		t.Errorf("rewriting a slug changed identity: %d then %d", a.ID, again.ID)
	}
}

func TestListDocuments_orders_by_kind_then_slug(t *testing.T) {
	d, uid, pid := documentsFixture(t)
	ctx := context.Background()

	for _, doc := range []struct{ kind, slug string }{
		{"wiki", "faq"},
		{"spec", "architecture"},
		{"readme", "readme"},
		{"wiki", "install"},
	} {
		if _, err := d.PutDocument(ctx, pid, uid, doc.kind, doc.slug, doc.slug, "body"); err != nil {
			t.Fatalf("PutDocument %s/%s: %v", doc.kind, doc.slug, err)
		}
	}

	docs, err := d.ListDocuments(ctx, pid, "")
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	var got []string
	for _, doc := range docs {
		got = append(got, doc.Kind+"/"+doc.Slug)
	}
	want := []string{"readme/readme", "spec/architecture", "wiki/faq", "wiki/install"}
	if len(got) != len(want) {
		t.Fatalf("got %d documents %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestListDocuments_filters_by_kind(t *testing.T) {
	d, uid, pid := documentsFixture(t)
	ctx := context.Background()

	for _, doc := range []struct{ kind, slug string }{
		{"readme", "readme"}, {"spec", "a"}, {"spec", "b"}, {"wiki", "c"},
	} {
		if _, err := d.PutDocument(ctx, pid, uid, doc.kind, doc.slug, doc.slug, "body"); err != nil {
			t.Fatalf("PutDocument: %v", err)
		}
	}

	specs, err := d.ListDocuments(ctx, pid, "spec")
	if err != nil {
		t.Fatalf("ListDocuments(spec): %v", err)
	}
	if len(specs) != 2 {
		t.Errorf("got %d specs, want 2", len(specs))
	}
	for _, s := range specs {
		if s.Kind != "spec" {
			t.Errorf("kind filter leaked a %q document", s.Kind)
		}
	}
}

func TestGetDocument_is_scoped_to_its_project(t *testing.T) {
	d, uid, pid := documentsFixture(t)
	ctx := context.Background()

	doc, err := d.PutDocument(ctx, pid, uid, "readme", "readme", "T", "b")
	if err != nil {
		t.Fatalf("PutDocument: %v", err)
	}
	other := secondProject(t, d, uid)

	// Reading another project's document id must read as absent, not as a
	// permission error: the two are distinguishable to a caller otherwise.
	if _, err := d.GetDocument(ctx, other, doc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project read: err = %v, want ErrNotFound", err)
	}
	if got, err := d.GetDocument(ctx, pid, doc.ID); err != nil || got.ID != doc.ID {
		t.Fatalf("own read: %v, %v", got, err)
	}
}

func TestDeleteDocument_reports_absent_rather_than_succeeding(t *testing.T) {
	d, uid, pid := documentsFixture(t)
	ctx := context.Background()

	doc, err := d.PutDocument(ctx, pid, uid, "adr", "0001", "ADR 1", "b")
	if err != nil {
		t.Fatalf("PutDocument: %v", err)
	}
	if err := d.DeleteDocument(ctx, pid, doc.ID); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
	if err := d.DeleteDocument(ctx, pid, doc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice: err = %v, want ErrNotFound", err)
	}
	if _, err := d.GetDocument(ctx, pid, doc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("document survived deletion: %v", err)
	}
}

func TestSearchDocuments_finds_text_in_the_body(t *testing.T) {
	d, uid, pid := documentsFixture(t)
	ctx := context.Background()

	if _, err := d.PutDocument(ctx, pid, uid, "spec", "arch",
		"Architecture", "The identity of a tag is a concept, never a label."); err != nil {
		t.Fatalf("PutDocument: %v", err)
	}
	// A second document that must NOT match, so a search returning everything
	// fails rather than passing.
	if _, err := d.PutDocument(ctx, pid, uid, "readme", "readme",
		"Readme", "Install it and run it."); err != nil {
		t.Fatalf("PutDocument readme: %v", err)
	}

	hits, err := d.SearchDocuments(ctx, pid, "concept", 10)
	if err != nil {
		t.Fatalf("SearchDocuments: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1: %+v", len(hits), hits)
	}
	if hits[0].Kind != "spec" {
		t.Errorf("hit kind = %q, want spec", hits[0].Kind)
	}
}

func TestSearchDocuments_empty_query_returns_nothing(t *testing.T) {
	d, uid, pid := documentsFixture(t)
	ctx := context.Background()

	if _, err := d.PutDocument(ctx, pid, uid, "readme", "readme", "R", "plenty of text here"); err != nil {
		t.Fatalf("PutDocument: %v", err)
	}
	// A search box that dumps the whole corpus when the query is empty is a
	// way to publish a project's documents by accident.
	hits, err := d.SearchDocuments(ctx, pid, "   ", 10)
	if err != nil {
		t.Fatalf("SearchDocuments: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("empty query returned %d documents, want 0", len(hits))
	}
}

func TestSearchDocuments_index_follows_an_update(t *testing.T) {
	d, uid, pid := documentsFixture(t)
	ctx := context.Background()

	if _, err := d.PutDocument(ctx, pid, uid, "readme", "readme", "R", "alpha"); err != nil {
		t.Fatalf("PutDocument: %v", err)
	}
	if hits, _ := d.SearchDocuments(ctx, pid, "alpha", 10); len(hits) != 1 {
		t.Fatalf("before update: %d hits for alpha, want 1", len(hits))
	}

	if _, err := d.PutDocument(ctx, pid, uid, "readme", "readme", "R", "beta"); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	// The FTS index must not keep the old text: a stale index reports a
	// document as containing a word its body no longer has.
	if hits, _ := d.SearchDocuments(ctx, pid, "alpha", 10); len(hits) != 0 {
		t.Errorf("after update: %d hits for alpha, want 0 (stale FTS index)", len(hits))
	}
	if hits, _ := d.SearchDocuments(ctx, pid, "beta", 10); len(hits) != 1 {
		t.Errorf("after update: %d hits for beta, want 1", len(hits))
	}
}

func TestSearchDocuments_is_scoped_to_its_project(t *testing.T) {
	d, uid, pid := documentsFixture(t)
	ctx := context.Background()

	if _, err := d.PutDocument(ctx, pid, uid, "spec", "secret", "Secret", "a distinctive phrase"); err != nil {
		t.Fatalf("PutDocument: %v", err)
	}
	other := secondProject(t, d, uid)

	hits, err := d.SearchDocuments(ctx, other, "distinctive", 10)
	if err != nil {
		t.Fatalf("SearchDocuments: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("search leaked %d documents from another project", len(hits))
	}
}
