package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Project documents (spec extension, 2026-09-30).
//
// A project in Concord was a name, a description and a list of features. The
// documents that actually explain it -- README, specification, plan, wiki --
// lived in a repository somewhere else and were invisible to anyone reading
// Concord.
//
// The three rules this file exists to enforce:
//
//  1. A document belongs to exactly one project, and kind+slug is unique within
//     it. A project has at most one README and at most one spec, but many wiki
//     pages. Enforcing that in the application would leave a race between two
//     concurrent creates; the UNIQUE index makes the second one fail.
//  2. Updating a document increments revision and never changes its identity.
//     A reader needs to know whether it is looking at the current document, and
//     an edit that silently reused another document's id would break every
//     external link to it.
//  3. Body is stored as given. Rendering markdown is the reader's job; storing
//     rendered HTML would make every spec edit a migration.

// Document is one stored document belonging to a project.
type Document struct {
	ID        int64   `json:"id"`
	ProjectID int64   `json:"project_id"`
	Kind      string  `json:"kind"`
	Slug      string  `json:"slug"`
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	Revision  int64   `json:"revision"`
	AuthorID  int64   `json:"author_id"`
	CreatedAt float64 `json:"created_at"`
	UpdatedAt float64 `json:"updated_at"`
}

// DocumentKinds are the accepted values of Document.Kind. Kept here rather than
// only in the CHECK constraint so the API can reject a bad kind with a useful
// message instead of surfacing a SQLite constraint error.
//
// `scout` is last, and was added by 0023_scout_document_kind.sql, which rebuilt
// the table's CHECK constraint to admit it. Keeping the Go list and the CHECK in
// step is the point of having this list at all: the API rejects a bad kind here
// with a useful message rather than surfacing a SQLite constraint error, and that
// only works if the two agree.
var DocumentKinds = []string{"readme", "spec", "plan", "wiki", "adr", "changelog",
	"scout"}

// validDocumentKind reports whether kind is accepted.
func validDocumentKind(kind string) bool {
	for _, k := range DocumentKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// PutDocument creates a document, or replaces the body of the one already at
// (kind, slug) and returns its id either way.
//
// This is an upsert rather than separate Create and Update because the common
// case is "sync this file from the repository", which does not know whether the
// document is new. Returning the id in both cases is what lets the caller
// update a link without a prior read.
func (d *DB) PutDocument(ctx context.Context, projectID, authorID int64,
	kind, slug, title, body string) (Document, error) {

	kind = strings.ToLower(strings.TrimSpace(kind))
	slug = strings.TrimSpace(slug)
	title = strings.TrimSpace(title)

	if projectID == 0 || authorID == 0 {
		return Document{}, fmt.Errorf("%w: project_id and author_id are required", ErrInvalid)
	}
	if !validDocumentKind(kind) {
		return Document{}, fmt.Errorf("%w: kind must be one of %s",
			ErrInvalid, strings.Join(DocumentKinds, ", "))
	}
	if slug == "" {
		return Document{}, fmt.Errorf("%w: empty document slug", ErrInvalid)
	}
	if title == "" {
		// A README has no meaningful title; fall back to the slug rather than
		// storing an empty display name.
		title = slug
	}

	now := float64(time.Now().Unix())
	_, err := d.ExecContext(ctx, `
		INSERT INTO project_documents
			(project_id, kind, slug, title, body, revision, author_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)
		ON CONFLICT (project_id, kind, slug) DO UPDATE SET
			title     = excluded.title,
			body      = excluded.body,
			revision  = project_documents.revision + 1,
			author_id = excluded.author_id,
			updated_at = excluded.updated_at`,
		projectID, kind, slug, title, body, authorID, now, now)
	if err != nil {
		return Document{}, fmt.Errorf("put document: %w", err)
	}

	return d.GetDocumentBySlug(ctx, projectID, kind, slug)
}

// GetDocumentBySlug returns the document at (kind, slug), or ErrNotFound.
func (d *DB) GetDocumentBySlug(ctx context.Context, projectID int64,
	kind, slug string) (Document, error) {

	var doc Document
	err := d.QueryRowContext(ctx, `
		SELECT id, project_id, kind, slug, title, body, revision, author_id, created_at, updated_at
		FROM project_documents
		WHERE project_id = ? AND kind = ? AND slug = ?`,
		projectID, kind, slug).
		Scan(&doc.ID, &doc.ProjectID, &doc.Kind, &doc.Slug, &doc.Title,
			&doc.Body, &doc.Revision, &doc.AuthorID, &doc.CreatedAt, &doc.UpdatedAt)
	if err == sql.ErrNoRows {
		return Document{}, fmt.Errorf("%w: no %s document %q in project %d",
			ErrNotFound, kind, slug, projectID)
	}
	if err != nil {
		return Document{}, fmt.Errorf("get document: %w", err)
	}
	return doc, nil
}

// GetDocument returns a document by id, scoped to its project.
//
// The project_id is part of the WHERE clause rather than checked afterwards:
// a document id from another project must read as absent, not as a permission
// error, or the two become distinguishable to a caller.
func (d *DB) GetDocument(ctx context.Context, projectID, docID int64) (Document, error) {
	var doc Document
	err := d.QueryRowContext(ctx, `
		SELECT id, project_id, kind, slug, title, body, revision, author_id, created_at, updated_at
		FROM project_documents
		WHERE id = ? AND project_id = ?`, docID, projectID).
		Scan(&doc.ID, &doc.ProjectID, &doc.Kind, &doc.Slug, &doc.Title,
			&doc.Body, &doc.Revision, &doc.AuthorID, &doc.CreatedAt, &doc.UpdatedAt)
	if err == sql.ErrNoRows {
		return Document{}, fmt.Errorf("%w: document %d", ErrNotFound, docID)
	}
	if err != nil {
		return Document{}, fmt.Errorf("get document: %w", err)
	}
	return doc, nil
}

// ListDocuments returns every document in a project, optionally filtered by
// kind. An empty kind returns all of them.
//
// Ordering is by kind then slug, not by updated_at: a project page wants a
// stable order it can render as sections, and "most recently edited first"
// reorders the page under the reader on every edit.
func (d *DB) ListDocuments(ctx context.Context, projectID int64, kind string) ([]Document, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "" && !validDocumentKind(kind) {
		return nil, fmt.Errorf("%w: kind must be one of %s",
			ErrInvalid, strings.Join(DocumentKinds, ", "))
	}

	q := `
		SELECT id, project_id, kind, slug, title, body, revision, author_id, created_at, updated_at
		FROM project_documents
		WHERE project_id = ?`
	args := []any{projectID}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY kind, slug`

	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}
	defer rows.Close()

	out := []Document{}
	for rows.Next() {
		var doc Document
		if err := rows.Scan(&doc.ID, &doc.ProjectID, &doc.Kind, &doc.Slug, &doc.Title,
			&doc.Body, &doc.Revision, &doc.AuthorID, &doc.CreatedAt, &doc.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan document: %w", err)
		}
		out = append(out, doc)
	}
	return out, rows.Err()
}

// DeleteDocument removes a document. Returns ErrNotFound if it was not there,
// so a caller cannot distinguish "deleted" from "never existed" by the error.
func (d *DB) DeleteDocument(ctx context.Context, projectID, docID int64) error {
	res, err := d.ExecContext(ctx,
		`DELETE FROM project_documents WHERE id = ? AND project_id = ?`, docID, projectID)
	if err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: document %d", ErrNotFound, docID)
	}
	return nil
}

// hasFtsOperator reports whether the query uses FTS5 syntax rather than being
// a single bare term: AND, OR, NOT, NEAR, ^, *, :, or a bracketed phrase.
// Words are lowercased for the test because FTS5 keywords are case-insensitive.
func hasFtsOperator(q string) bool {
	lower := strings.ToLower(q)
	for _, kw := range []string{" and ", " or ", " not ", " near "} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	// A leading ^ anchors, a trailing * is a prefix, ':' is a column filter and
	// '(' or ')' already excluded the term from the rewrite above.
	return strings.HasPrefix(strings.TrimSpace(lower), "^") ||
		strings.HasSuffix(q, "*") ||
		strings.Contains(q, ":")
}

// ErrInvalidQuery is a malformed FTS5 expression from the caller.
//
// A sentinel rather than a substring test at the HTTP layer: the driver
// reports the same class of error with different text on SQLite and Postgres,
// and "SQL logic error" is a driver string that can change under us, while the
// classification is a property of the store's contract.
var ErrInvalidQuery = errors.New("invalid search query")

// SearchDocuments finds documents in a project matching a FTS5 query, best
// match first. An empty query returns nothing rather than everything: a search
// box that dumps the whole corpus when you focus it is not a search box.
func (d *DB) SearchDocuments(ctx context.Context, projectID int64, query string, limit int) ([]Document, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []Document{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	// A bare term containing a hyphen is the overwhelmingly common case --
	// "language-neutral", "content-addressed", "best-version" -- and FTS5 reads
	// the hyphen as the NOT operator. So `language-neutral` searches for
	// documents containing "language" and *not* "neutral", silently returning
	// the wrong rows. Escaping the term in quotes makes the hyphen literal.
	//
	// Only unquoted single terms are rewritten. A caller who writes a real
	// FTS5 expression (`"exact phrase" AND tag:x`) must get FTS5 semantics, and
	// there is no way to tell the two apart by inspection -- so a query
	// containing whitespace, a quote or an operator keyword is passed through
	// untouched. A user typing a hyphenated word gets a search; a user who
	// knows FTS5 gets FTS5.
	if !strings.ContainsAny(query, " \"()") && !hasFtsOperator(query) {
		query = `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
	}

	rows, err := d.QueryContext(ctx, `
		SELECT d.id, d.project_id, d.kind, d.slug, d.title, d.body,
		       d.revision, d.author_id, d.created_at, d.updated_at
		FROM project_documents_fts f
		JOIN project_documents d ON d.id = f.rowid
		WHERE project_documents_fts MATCH ? AND d.project_id = ?
		ORDER BY bm25(project_documents_fts)
		LIMIT ?`, query, projectID, limit)
	if err != nil {
		// A bad expression is the caller's mistake. Classify it here so the
		// HTTP layer maps it to 400 without knowing driver error strings.
		return nil, fmt.Errorf("%w: %v", ErrInvalidQuery, err)
	}
	defer rows.Close()

	out := []Document{}
	for rows.Next() {
		var doc Document
		if err := rows.Scan(&doc.ID, &doc.ProjectID, &doc.Kind, &doc.Slug, &doc.Title,
			&doc.Body, &doc.Revision, &doc.AuthorID, &doc.CreatedAt, &doc.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan document: %w", err)
		}
		out = append(out, doc)
	}
	return out, rows.Err()
}
