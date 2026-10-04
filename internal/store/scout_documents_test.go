package store

import (
	"context"
	"testing"
)

// Scout reports as documents (scout-spec.md §8, migration 0023).
//
// §8 asks for a report to be persistable as a `project_documents` row of kind
// `scout`, and says so explicitly rather than letting it be smuggled into an
// existing kind. Two things have to be true for that to work, and neither is
// obvious:
//
//   1. The Go `DocumentKinds` list admits `scout`, so PutDocument does not reject
//      it with ErrInvalid before the database is ever consulted.
//   2. The database's CHECK constraint admits it too. It did not, and SQLite
//      cannot ALTER a CHECK constraint, so 0023 rebuilds the table.
//
// Keeping (1) and (2) in step is the whole fragility. A test that only exercised
// one of them would pass with the other broken: with the Go list updated and the
// CHECK not, PutDocument returns a SQLite constraint error; with the CHECK updated
// and the Go list not, PutDocument returns ErrInvalid. Both look like "saving a
// scout report failed", from different layers.

// TestScoutIsAValidDocumentKind is the layer check.
func TestScoutIsAValidDocumentKind(t *testing.T) {
	if !validDocumentKind("scout") {
		t.Fatalf("validDocumentKind(\"scout\") is false, so PutDocument rejects a " +
			"scout report before the database is ever consulted")
	}
	found := false
	for _, k := range DocumentKinds {
		if k == "scout" {
			found = true
		}
	}
	if !found {
		t.Error("DocumentKinds does not list scout, so the error message a bad " +
			"kind produces would not name it")
	}
}

// TestPutDocument_savesAScoutReport is the end-to-end store check: both layers
// agree and the row round-trips.
func TestPutDocument_savesAScoutReport(t *testing.T) {
	d, uid, pid := documentsFixture(t)
	ctx := context.Background()

	doc, err := d.PutDocument(ctx, pid, uid, "scout", "offline-note-app",
		"Scout: offline note app", "we need offline support and wip limits")
	if err != nil {
		t.Fatalf("PutDocument(kind=scout): %v", err)
	}
	if doc.Kind != "scout" {
		t.Errorf("kind = %q, want scout", doc.Kind)
	}

	// And it comes back under the same kind filter, so a project listing only its
	// scout reports works.
	got, err := d.ListDocuments(ctx, pid, "scout")
	if err != nil {
		t.Fatalf("ListDocuments(scout): %v", err)
	}
	if len(got) != 1 || got[0].Title != "Scout: offline note app" {
		t.Errorf("ListDocuments(scout) = %+v", got)
	}
}

// The FTS mirror is the part the migration comment warns about, and it is the
// part most likely to rot: DROP TABLE takes a table's triggers with it, so a
// rebuild that forgot to recreate them leaves a search that finds old documents
// and never new ones — worse than one that is obviously broken, because it looks
// like it works.
func TestTheFtsMirrorStillIndexesDocumentsAfterTheScoutKindMigration(t *testing.T) {
	d, uid, pid := documentsFixture(t)
	ctx := context.Background()

	// A pre-existing document, written BEFORE any scout report. If the triggers
	// were dropped and not recreated, this stays findable and the new one never
	// appears — which is exactly the silent half-broken state.
	if _, err := d.PutDocument(ctx, pid, uid, "readme", "old-doc",
		"Existing readme", "kanban board for small teams"); err != nil {
		t.Fatalf("seeding the old document: %v", err)
	}
	if _, err := d.PutDocument(ctx, pid, uid, "scout", "new-report",
		"Scout report", "wip limits and self-hosted"); err != nil {
		t.Fatalf("PutDocument(scout): %v", err)
	}

	// The NEW document must be searchable. This is the assertion that fails if
	// 0023 rebuilt the table without recreating the insert trigger.
	hits, err := d.SearchDocuments(ctx, pid, "self-hosted", 20)
	if err != nil {
		t.Fatalf("SearchDocuments: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("a document inserted after the migration is NOT searchable; the " +
			"FTS triggers were dropped by the table rebuild and not recreated")
	}
	found := false
	for _, h := range hits {
		if h.Kind == "scout" {
			found = true
		}
	}
	if !found {
		t.Errorf("search hit %d documents but none of kind scout: %+v", len(hits), hits)
	}

	// And the OLD one still is, so the mirror was not rebuilt into a state that
	// only knows about rows written since.
	old, err := d.SearchDocuments(ctx, pid, "kanban", 20)
	if err != nil {
		t.Fatalf("SearchDocuments(old): %v", err)
	}
	if len(old) == 0 {
		t.Error("a document written before the migration is no longer searchable")
	}
}

// An UPDATE must also reach the mirror, which is the third trigger and the one
// most easily omitted.
func TestTheFtsMirrorStillIndexesAnUpdatedScoutReport(t *testing.T) {
	d, uid, pid := documentsFixture(t)
	ctx := context.Background()

	if _, err := d.PutDocument(ctx, pid, uid, "scout", "report",
		"Scout report", "original phrasing about kanban"); err != nil {
		t.Fatalf("PutDocument: %v", err)
	}
	// PutDocument replaces the body at the same (kind, slug).
	if _, err := d.PutDocument(ctx, pid, uid, "scout", "report",
		"Scout report", "revised phrasing about offline sync"); err != nil {
		t.Fatalf("PutDocument(replace): %v", err)
	}

	hits, err := d.SearchDocuments(ctx, pid, "offline", 20)
	if err != nil {
		t.Fatalf("SearchDocuments: %v", err)
	}
	if len(hits) == 0 {
		t.Error("an UPDATED document is not searchable; the update trigger was " +
			"dropped by the table rebuild and not recreated")
	}
}
