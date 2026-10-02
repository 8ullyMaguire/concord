package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/embed"
)

// Duplicate detection at filing time (spec revision 4 §6.1).
//
// "Duplicate detection at filing time uses semantic similarity before submit,
// and also searches other projects." Two capabilities: find near-duplicates
// within a project, and find the same problem elsewhere in the instance.
//
// Similarity is computed in Go rather than in SQL. SQLite has no vector
// extension here (the binary is pure-Go modernc, CGO off), so a dot product
// would have to be written as a correlated subquery over a BLOB -- which is
// both slow and impossible without a nested query. The corpus is ~1000
// embeddable rows, so loading a project's vectors and scanning in Go is
// microseconds, and it keeps TestNoNestedQueriesInStore satisfied.
//
// The cost is that this does not scale to a large instance. That is recorded in
// the schema comment on `embeddings` rather than left for someone to discover:
// the migration path is a vector index, and this code is the thing that gets
// replaced.

// embeddableKinds are the entity types that can be embedded.
//
// The list is closed and CHECK-constrained in the schema. An open-ended set
// would mean a typo silently writes rows that no query ever matches, which looks
// exactly like "duplicate detection does not work".
const (
	KindComplaint = "complaint"
	KindFeature   = "feature"
	KindRequest   = "request"
	KindSolution  = "solution"
	KindListEntry = "list_entry"
	KindProject   = "project"
)

// ErrEmbedderMismatch is returned when stored vectors came from a different
// model than the query vector.
//
// Comparing them would produce a number in [-1,1] that means nothing, so this
// is refused rather than returned. The alternative -- cosine over mismatched
// vectors -- is the kind of error that surfaces as "duplicate detection is
// nonsense" weeks later.
var ErrEmbedderMismatch = errors.New("stored embeddings were produced by a different model")

// Embedder is the vector source. Injected rather than constructed so tests can
// use a tiny deterministic embedder and so a real model can be swapped in
// without touching the store.
type Embedder interface {
	ID() string
	Dims() int
	Embed(text string) []float32
}

// DB.embedder is set once at construction. There is deliberately no per-request
// setter: an embedder that changed between writing and reading would make the
// stored model_id meaningless.
var _ = embed.Cosine // the similarity math lives in internal/embed

// SetEmbedder configures the embedder. Call before any embedding is stored.
//
// Stored vectors carry the model id they were made with, and a query refuses to
// compare across models, so swapping the embedder does not corrupt data -- it
// makes old rows invisible until they are re-embedded. BackfillEmbeddingIndex
// does that work.
func (d *DB) SetEmbedder(e Embedder) { d.embedder = e }

// EmbedderID returns the current model id, or "" when none is configured.
func (d *DB) EmbedderID() string {
	if d.embedder == nil {
		return ""
	}
	return d.embedder.ID()
}

// Embedding field selectors.
const (
	FieldTitle = "title"
	FieldFull  = "full"
)

// UpsertEmbedding stores or replaces an entity's full-text vector.
func (d *DB) UpsertEmbedding(ctx context.Context, kind string, entityID, projectID int64, source string) error {
	return d.UpsertEmbeddingField(ctx, kind, entityID, projectID, FieldFull, source)
}

// UpsertTitleEmbedding stores an entity's title vector.
//
// Kept separate from the full-text vector because a filer checks for duplicates
// with the title they have typed so far, while the existing row was indexed as
// title+body. Comparing a title query against a full-text vector dilutes it with
// body words it cannot see: measured, an identical complaint scored 0.80 instead
// of 1.0, and a title is the strongest single signal for a complaint.
func (d *DB) UpsertTitleEmbedding(ctx context.Context, kind string, entityID, projectID int64, title string) error {
	return d.UpsertEmbeddingField(ctx, kind, entityID, projectID, FieldTitle, title)
}

// UpsertEmbeddingField stores or replaces one vector for an entity field.
//
// Replace rather than accumulate is the point of the (kind, entity_id, field)
// primary key: a re-embed after an edit must not leave the previous vector
// competing with the current one.
func (d *DB) UpsertEmbeddingField(ctx context.Context, kind string, entityID, projectID int64, field, source string) error {
	if d.embedder == nil {
		return fmt.Errorf("%w: no embedder configured", ErrInvalid)
	}
	vec := d.embedder.Embed(source)
	if !embed.HasContent(vec) {
		// No vector means no searchable content -- empty text, or stop-words only.
		// Removing the row is better than leaving a stale one: a previous title
		// would keep matching after the text became empty.
		//
		// The check is HasContent, not len(vec) == 0: Embed always returns a
		// full-width slice, and stop-words-only text produces a vector of all
		// zeros. A length check would store that zero vector and, because a zero
		// vector scores 0 against everything, the row would silently occupy an
		// index slot forever.
		_, _ = d.ExecContext(ctx,
			`DELETE FROM embeddings WHERE kind=? AND entity_id=? AND field=?`, kind, entityID, field)
		return nil
	}
	var pid any
	if projectID > 0 {
		pid = projectID
	}
	_, err := d.ExecContext(ctx, `
		INSERT INTO embeddings (kind, entity_id, field, project_id, vector, dims, model_id, source, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(kind, entity_id, field) DO UPDATE SET
			project_id = excluded.project_id,
			vector     = excluded.vector,
			dims       = excluded.dims,
			model_id   = excluded.model_id,
			source     = excluded.source,
			updated_at = excluded.updated_at`,
		kind, entityID, field, pid, embed.Encode(vec), len(vec), d.embedder.ID(), source,
		float64(time.Now().Unix()))
	return err
}

// UpsertEntityText indexes both the title and the full text of an entity, which
// is what every create path wants.
func (d *DB) UpsertEntityText(ctx context.Context, kind string, entityID, projectID int64, title, body string) error {
	if err := d.UpsertTitleEmbedding(ctx, kind, entityID, projectID, title); err != nil {
		return err
	}
	return d.UpsertEmbeddingField(ctx, kind, entityID, projectID, FieldFull, title+" "+body)
}

// DeleteEmbedding removes an entity's vector. Called when an entity is deleted,
// so similarity does not keep pointing at rows that no longer exist.
func (d *DB) DeleteEmbedding(ctx context.Context, kind string, entityID int64) error {
	_, err := d.ExecContext(ctx,
		`DELETE FROM embeddings WHERE kind = ? AND entity_id = ?`, kind, entityID)
	return err
}

// FindSimilar returns the entities most similar to text.
//
// Scope: projectID > 0 restricts to that project; projectID == 0 searches the
// whole instance, which is the "this was fixed in X" case of §6.1.
//
// limit 0 means "no limit", which callers use for the "is there any duplicate at
// all" question.
//
// Results below embed.WeakThreshold are omitted: a panel of ten weak candidates
// teaches people to ignore it.
func (d *DB) FindSimilar(ctx context.Context, kind, text string, projectID int64, limit int) ([]embed.Similarity, error) {
	return d.FindSimilarField(ctx, kind, text, projectID, "", limit)
}

// FindSimilarField is FindSimilar with an explicit field selector.
//
// field is FieldTitle to compare against title vectors, FieldFull for full text,
// or "" for the best score over all fields. The pre-submit panel passes "" because
// the filer is usually holding only a title; a caller comparing a full description
// passes FieldFull.
func (d *DB) FindSimilarField(ctx context.Context, kind, text string, projectID int64, field string, limit int) ([]embed.Similarity, error) {
	if d.embedder == nil {
		return nil, fmt.Errorf("%w: no embedder configured", ErrInvalid)
	}
	query := d.embedder.Embed(text)
	if !embed.HasContent(query) {
		// Empty or stop-words-only text has no vector, so every candidate would
		// score 0. Returning nothing is the honest answer; returning everything
		// would be the "match everything" failure.
		return nil, nil
	}

	// An empty field means "best over all fields", which is what the pre-submit
	// panel wants because the filer is usually holding only a title.
	sel := field
	if sel == "" {
		sel = "auto"
	}

	q := `SELECT e.entity_id, e.project_id, e.field, e.vector, e.dims, e.model_id
	      FROM embeddings e
	      WHERE e.kind = ? AND e.model_id = ?`
	args := []any{kind, d.embedder.ID()}
	if projectID > 0 {
		q += ` AND e.project_id = ?`
		args = append(args, projectID)
	}

	// Every vector is loaded before any scoring happens: the rows cursor stays
	// open for the whole scan, and calling another d.* method inside the loop
	// would need a second pooled connection while this one is held. Collecting
	// first, then scoring, is what keeps TestNoNestedQueriesInStore passing.
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("load embeddings: %w", err)
	}
	type candidate struct {
		entityID  int64
		projectID int64
		field     string
		vector    []float32
	}
	var candidates []candidate
	for rows.Next() {
		var entityID, pid int64
		var f string
		var blob []byte
		var dims int
		var modelID string
		if err := rows.Scan(&entityID, &pid, &f, &blob, &dims, &modelID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan embedding: %w", err)
		}
		// A dimension mismatch against the query vector is a model change that
		// was not re-backfilled. Skip it rather than scoring nonsense.
		if dims != len(query) {
			continue
		}
		if sel != "auto" && f != sel {
			continue
		}
		if v := embed.Decode(blob); v != nil {
			candidates = append(candidates, candidate{entityID, pid, f, v})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// Max over fields, keyed by entity.
	//
	// Max and not average: a filing matches when EITHER its title or its full
	// text matches. Averaging lets a strong title match be cancelled by a weak
	// body match, which is precisely the duplicate we most want to catch.
	//
	// This is why the title is embedded separately rather than folded into the
	// full text. Measured, with two complaints sharing a title and differing
	// bodies:
	//
	//	full-text query vs full-text vector   0.58   (drowned in body words)
	//	full-text query vs title vector       0.73
	//	title query   vs title vector         1.00
	//
	// So an identical complaint scored 0.58 -- below the advisory threshold --
	// and was filed twice without comment. The title carries the signal and the
	// body dilutes it, which is the opposite of what should happen for a title.
	best := make(map[int64]float64, len(candidates))
	for _, c := range candidates {
		s := embed.Cosine(query, c.vector)
		if s > best[c.entityID] {
			best[c.entityID] = s
		}
	}
	scored := make([]embed.Similarity, 0, len(best))
	for entityID, s := range best {
		if s < embed.WeakThreshold {
			continue
		}
		scored = append(scored, embed.Similarity{Kind: kind, EntityID: entityID, Score: s})
	}
	scored = embed.Rank(scored, limit)
	return d.hydrateSimilarities(ctx, kind, scored)
}

// hydrateSimilarities fills in the title, status and URL that make a match
// actionable. Done after the scan closes, so each lookup is a separate query
// rather than a nested one.
//
// A missing row is skipped rather than returned with an empty title: the
// embeddings table is denormalised, so an entity deleted by a path that skipped
// DeleteEmbedding leaves a stale row, and showing "complaint #412 — " is worse
// than not showing it.
func (d *DB) hydrateSimilarities(ctx context.Context, kind string, sims []embed.Similarity) ([]embed.Similarity, error) {
	if len(sims) == 0 {
		return sims, nil
	}
	out := make([]embed.Similarity, 0, len(sims))
	for _, s := range sims {
		var title, status sql.NullString
		var err error
		switch kind {
		case KindComplaint:
			err = d.QueryRowContext(ctx,
				`SELECT title, status FROM complaints WHERE id = ?`, s.EntityID).Scan(&title, &status)
			s.URL = fmt.Sprintf("/api/v1/complaints/%d", s.EntityID)
		case KindFeature:
			err = d.QueryRowContext(ctx,
				`SELECT title, status FROM features WHERE id = ?`, s.EntityID).Scan(&title, &status)
			s.URL = fmt.Sprintf("/api/v1/features/%d", s.EntityID)
		case KindRequest:
			err = d.QueryRowContext(ctx,
				`SELECT title, status FROM requests WHERE id = ?`, s.EntityID).Scan(&title, &status)
			s.URL = fmt.Sprintf("/api/v1/requests/%d", s.EntityID)
		default:
			// A kind with no hydration table yet (solutions arrive with §6.3).
			// Returned with its score so the caller still learns a match exists.
		}
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return nil, fmt.Errorf("hydrate %s %d: %w", kind, s.EntityID, err)
		}
		s.Title, s.Status = title.String, status.String
		out = append(out, s)
	}
	return out, nil
}

// EmbeddingCoverage reports how much of a kind is indexed.
//
// Reported rather than assumed: a partially indexed corpus silently produces
// "no duplicates found", which a filer reads as "I am the first to report this".
func (d *DB) EmbeddingCoverage(ctx context.Context, kind string) (map[string]any, error) {
	var total, embedded int
	table := map[string]string{
		KindComplaint: "complaints", KindFeature: "features",
		KindRequest: "requests", KindProject: "projects",
	}[kind]
	if table == "" {
		return map[string]any{"kind": kind, "indexed": false,
			"note": "no backing table for this kind yet"}, nil
	}
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&total); err != nil {
		return nil, err
	}
	// COUNT(DISTINCT entity_id), not COUNT(*): each entity has up to two vectors
	// (title and full), so a row count reports twice the coverage and then
	// declares an unindexed entity "covered" because its sibling field is.
	//
	// Restricted to the CURRENT model: a re-embed that has not run yet leaves old
	// rows that FindSimilar will skip.
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT entity_id) FROM embeddings WHERE kind = ? AND model_id = ?`,
		kind, d.embedderID()).Scan(&embedded); err != nil {
		return nil, err
	}
	frac := 0.0
	if total > 0 {
		frac = float64(embedded) / float64(total)
	}
	return map[string]any{
		"kind": kind, "model_id": d.embedderID(),
		"total": total, "embedded": embedded,
		"coverage": frac, "complete": embedded >= total,
	}, nil
}

func (d *DB) embedderID() string {
	if d.embedder == nil {
		return ""
	}
	return d.embedder.ID()
}

// BackfillEmbeddingIndex embeds every entity of the given kinds that is not
// already indexed by the current model.
//
// Runs outside any open cursor for the same reason FindSimilar does. Batched by
// kind rather than all at once so a failure reports which kind failed.
func (d *DB) BackfillEmbeddingIndex(ctx context.Context, kinds []string) (map[string]int, error) {
	if d.embedder == nil {
		return nil, fmt.Errorf("%w: no embedder configured", ErrInvalid)
	}
	done := map[string]int{}
	for _, kind := range kinds {
		n, err := d.backfillKind(ctx, kind)
		if err != nil {
			return done, fmt.Errorf("backfill %s: %w", kind, err)
		}
		done[kind] = n
		now := float64(time.Now().Unix())
		_, _ = d.ExecContext(ctx, `
			INSERT INTO embedding_backfill (kind, model_id, total, embedded, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(kind, model_id) DO UPDATE SET
				total = excluded.total, embedded = excluded.embedded, updated_at = excluded.updated_at`,
			kind, d.embedder.ID(), n, n, now)
	}
	return done, nil
}

// backfillKind embeds one entity kind.
//
// The query selects entities with no embedding from the CURRENT model, so a
// re-run after a model change picks up exactly the stale rows. LEFT JOIN on
// (kind, entity_id, model_id) is what makes that precise.
func (d *DB) backfillKind(ctx context.Context, kind string) (int, error) {
	var rows *sql.Rows
	var err error
	switch kind {
	// Both fields are selected and both are written, because a filer checks for
	// duplicates with a title while the stored full-text vector would dilute it
	// (measured: an identical complaint scored 0.80 instead of 1.0). The LEFT
	// JOIN is on model_id, so after a model change exactly the stale rows are
	// picked up.
	//
	// `title` is selected separately from the concatenation rather than derived
	// in Go, so a row is embedded from exactly the text that was stored.
	case KindComplaint:
		rows, err = d.QueryContext(ctx, `
			SELECT c.id, c.project_id, c.title, c.title || ' ' || COALESCE(c.body, '')
			FROM complaints c
			WHERE NOT EXISTS (SELECT 1 FROM embeddings e
			                  WHERE e.kind='complaint' AND e.entity_id=c.id
			                    AND e.field='full' AND e.model_id=?)
			  AND NOT EXISTS (SELECT 1 FROM embeddings e
			                  WHERE e.kind='complaint' AND e.entity_id=c.id
			                    AND e.field='title' AND e.model_id=?)`,
			d.embedder.ID(), d.embedder.ID())
	case KindFeature:
		rows, err = d.QueryContext(ctx, `
			SELECT f.id, f.project_id, f.title, f.title || ' ' || COALESCE(f.body, '')
			FROM features f
			WHERE NOT EXISTS (SELECT 1 FROM embeddings e
			                  WHERE e.kind='feature' AND e.entity_id=f.id
			                    AND e.field='full' AND e.model_id=?)
			  AND NOT EXISTS (SELECT 1 FROM embeddings e
			                  WHERE e.kind='feature' AND e.entity_id=f.id
			                    AND e.field='title' AND e.model_id=?)`,
			d.embedder.ID(), d.embedder.ID())
	case KindRequest:
		rows, err = d.QueryContext(ctx, `
			SELECT r.id, r.project_id, r.title, r.title || ' ' || COALESCE(r.body, '')
			FROM requests r
			WHERE NOT EXISTS (SELECT 1 FROM embeddings e
			                  WHERE e.kind='request' AND e.entity_id=r.id
			                    AND e.field='full' AND e.model_id=?)
			  AND NOT EXISTS (SELECT 1 FROM embeddings e
			                  WHERE e.kind='request' AND e.entity_id=r.id
			                    AND e.field='title' AND e.model_id=?)`,
			d.embedder.ID(), d.embedder.ID())
	case KindProject:
		rows, err = d.QueryContext(ctx, `
			SELECT p.id, p.id, p.name, p.name || ' ' || COALESCE(p.description, '')
			FROM projects p
			WHERE NOT EXISTS (SELECT 1 FROM embeddings e
			                  WHERE e.kind='project' AND e.entity_id=p.id
			                    AND e.field='full' AND e.model_id=?)
			  AND NOT EXISTS (SELECT 1 FROM embeddings e
			                  WHERE e.kind='project' AND e.entity_id=p.id
			                    AND e.field='title' AND e.model_id=?)`,
			d.embedder.ID(), d.embedder.ID())
	default:
		return 0, fmt.Errorf("%w: cannot embed kind %q", ErrInvalid, kind)
	}
	if err != nil {
		return 0, err
	}

	// Collected before writing, for the same no-nested-queries reason.
	type item struct {
		id, projectID int64
		title, text   string
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.projectID, &it.title, &it.text); err != nil {
			rows.Close()
			return 0, err
		}
		if strings.TrimSpace(it.text) == "" {
			continue
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Both fields, via the same path the create handlers use, so a backfilled
	// row and a freshly filed one are byte-identical in how they were embedded.
	for _, it := range items {
		if err := d.UpsertTitleEmbedding(ctx, kind, it.id, it.projectID, it.title); err != nil {
			return len(items), err
		}
		if err := d.UpsertEmbeddingField(ctx, kind, it.id, it.projectID, FieldFull, it.text); err != nil {
			return len(items), err
		}
	}
	return len(items), nil
}
