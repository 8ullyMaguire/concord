package store

import (
	"context"
	"fmt"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/embed"
)

// Duplicate detection at filing time (§6.1).
//
// The property that matters most is not "does it catch duplicates" -- it does,
// and that is easy. It is "does it ever refuse a complaint that is not a
// duplicate", because a false refusal loses a real report that nobody will notice
// was lost. TestDistinctComplaintsAreNotFlagged is the one to break.

func dupFixture(t *testing.T) (*DB, int64, int64) {
	t.Helper()
	store, uid, pid := setupWithProject(t)
	store.SetEmbedder(embed.NewHashed())
	return store, uid, pid
}

func fileComplaint(t *testing.T, store *DB, pid, uid int64, title, body string) int64 {
	t.Helper()
	c, err := store.CreateComplaint(context.Background(), pid, uid, title, body, 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint(%q): %v", title, err)
	}
	if err := store.ValidateComplaint(context.Background(), c.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	// UpsertEntityText, not UpsertEmbedding: the create handlers index both the
	// title and the full text, and a fixture that indexes only one produces a
	// duplicate-detection environment no real filer ever meets.
	if err := store.UpsertEntityText(context.Background(), KindComplaint, c.ID, pid, title, body); err != nil {
		t.Fatalf("UpsertEntityText: %v", err)
	}
	return c.ID
}

func TestFindSimilarCatchesAnExactDuplicate(t *testing.T) {
	store, uid, pid := dupFixture(t)
	ctx := context.Background()
	fileComplaint(t, store, pid, uid, "Search results are identical for every query", "bm25 not applied")

	sims, err := store.FindSimilar(ctx, KindComplaint,
		"Search results are identical for every query", pid, 5)
	if err != nil {
		t.Fatalf("FindSimilar: %v", err)
	}
	if len(sims) == 0 {
		t.Fatal("an identical complaint produced no match")
	}
	if !embed.IsStrongDuplicate(sims[0].Score) {
		t.Errorf("top score %.3f is below the strong threshold %.2f",
			sims[0].Score, embed.StrongDuplicateThreshold)
	}
	// A match a filer cannot act on is useless, so the title comes back with it.
	if sims[0].Title == "" {
		t.Error("match has no title: the filer cannot tell what they collided with")
	}
	if sims[0].URL == "" {
		t.Error("match has no URL: the filer cannot jump to it")
	}
}

func TestFindSimilarCatchesTheSameComplaintFiledTwice(t *testing.T) {
	// The actual workflow: a second person hits the same bug and files it.
	store, uid, pid := dupFixture(t)
	ctx := context.Background()
	const title = "Upload fails silently on a network drop"
	fileComplaint(t, store, pid, uid, title, "No error, no retry, the file just stops")

	sims, err := store.FindSimilar(ctx, KindComplaint, title+" It fails silently again", pid, 5)
	if err != nil {
		t.Fatalf("FindSimilar: %v", err)
	}
	if len(sims) == 0 || !embed.IsDuplicate(sims[0].Score) {
		t.Errorf("a repeat report scored %.3f, want >= %.2f",
			firstScore(sims), embed.DuplicateThreshold)
	}
}

func TestDistinctComplaintsAreNotFlagged(t *testing.T) {
	// The false-positive test. These are complaints from the real corpus about
	// genuinely different problems in the same project. If any of them scores
	// above the advisory threshold, the checker is noise and filers will learn
	// to dismiss the panel.
	store, uid, pid := dupFixture(t)
	ctx := context.Background()
	for _, title := range []string{
		"No authentication existed, so the site was read-only for everyone",
		"Project-scoped routes silently operated on project 0",
		"Anyone can hijack a ranking by voting on their own proposal",
		"No way to see why a thread is ranked where it is",
		"Moderation removes content without recording why",
		"No loading or error state on any data-backed route",
	} {
		fileComplaint(t, store, pid, uid, title, "body")
	}

	probes := []string{
		"The ranking explanation panel shows no sources",
		"Deleting a comment leaves no record in the audit log",
		"Every page renders before the data arrives",
		"Sign-up silently fails with a generic error",
	}
	for _, probe := range probes {
		sims, err := store.FindSimilar(ctx, KindComplaint, probe, pid, 5)
		if err != nil {
			t.Fatalf("FindSimilar(%q): %v", probe, err)
		}
		if s := firstScore(sims); embed.IsDuplicate(s) {
			t.Errorf("unrelated complaint %q scored %.3f against the corpus: "+
				"the panel would show false duplicates", probe, s)
		}
	}
}

func TestEmptyQueryMatchesNothing(t *testing.T) {
	// An empty or stop-words-only query has no vector. Returning every complaint
	// would be the "matches everything" failure; returning nothing is honest.
	store, uid, pid := dupFixture(t)
	ctx := context.Background()
	fileComplaint(t, store, pid, uid, "A real complaint about uploads", "body")

	for _, q := range []string{"", "   ", "the of and to"} {
		sims, err := store.FindSimilar(ctx, KindComplaint, q, pid, 5)
		if err != nil {
			t.Fatalf("FindSimilar(%q): %v", q, err)
		}
		if len(sims) != 0 {
			t.Errorf("query %q returned %d matches, want 0", q, len(sims))
		}
	}
}

func TestFindSimilarScopesToProjectUnlessAskedOtherwise(t *testing.T) {
	// §6.1 also says duplicate detection "searches other projects" -- the
	// cross-project case. The instance-wide query must find it; the
	// project-scoped one must not.
	store, uid, pidA := dupFixture(t)
	ctx := context.Background()
	projB, err := store.CreateProject(ctx, uid, "other-project", "Other", "d", "collective", "")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	const title = "Search results are identical for every query"
	fileComplaint(t, store, projB.ID, uid, title, "in the other project")

	scoped, err := store.FindSimilar(ctx, KindComplaint, title, pidA, 5)
	if err != nil {
		t.Fatalf("scoped FindSimilar: %v", err)
	}
	if len(scoped) != 0 {
		t.Errorf("project-scoped query leaked %d matches from another project", len(scoped))
	}

	global, err := store.FindSimilar(ctx, KindComplaint, title, 0, 5)
	if err != nil {
		t.Fatalf("instance-wide FindSimilar: %v", err)
	}
	if len(global) == 0 {
		t.Error("instance-wide query found nothing: the cross-project case is broken")
	}
}

func TestUpsertEmbeddingReplacesRatherThanAccumulates(t *testing.T) {
	// One vector per (entity, field). An accumulating table would let a stale
	// vector keep winning after an edit -- two rows for one title field, and the
	// old one still answering queries.
	store, uid, pid := dupFixture(t)
	ctx := context.Background()
	id := fileComplaint(t, store, pid, uid, "original title about caching", "body")

	if err := store.UpsertEmbedding(ctx, KindComplaint, id, pid, "completely different subject matter"); err != nil {
		t.Fatalf("re-embed full: %v", err)
	}
	if err := store.UpsertTitleEmbedding(ctx, KindComplaint, id, pid, "a completely different title"); err != nil {
		t.Fatalf("re-embed title: %v", err)
	}

	// Two fields, one row each.
	var n int
	if err := store.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM embeddings WHERE kind=? AND entity_id=?`, KindComplaint, id).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("entity has %d vectors after re-embedding both fields, want 2 (title and full)", n)
	}

	// The re-embedded title must be what is stored, not the original.
	var src string
	if err := store.QueryRowContext(ctx,
		`SELECT source FROM embeddings WHERE kind=? AND entity_id=? AND field=?`,
		KindComplaint, id, FieldTitle).Scan(&src); err != nil {
		t.Fatalf("read title source: %v", err)
	}
	if src != "a completely different title" {
		t.Errorf("stored title source = %q, want the new text", src)
	}

	// And the old text must no longer match: otherwise the stale vector is still
	// winning queries.
	sims, err := store.FindSimilarField(ctx, KindComplaint, "original title about caching", pid, FieldTitle, 5)
	if err != nil {
		t.Fatalf("FindSimilar: %v", err)
	}
	for _, s := range sims {
		if s.EntityID == id && s.Score > 0.9 {
			t.Errorf("stale title still matches itself at %.3f", s.Score)
		}
	}
}

func TestGenuinelyEmptyTextRemovesTheStaleVector(t *testing.T) {
	// If the text becomes empty, the old vector is worse than absent: it keeps
	// answering queries for content the entity no longer has.
	store, uid, pid := dupFixture(t)
	ctx := context.Background()
	id := fileComplaint(t, store, pid, uid, "a title that will be emptied", "body")

	if err := store.UpsertTitleEmbedding(ctx, KindComplaint, id, pid, ""); err != nil {
		t.Fatalf("embed empty: %v", err)
	}
	var n int
	if err := store.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM embeddings WHERE kind=? AND entity_id=? AND field=?`,
		KindComplaint, id, FieldTitle).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("empty text left %d title vectors, want 0: a stale vector keeps matching", n)
	}
}

func TestStopWordOnlyTextDoesNotMatchRealComplaints(t *testing.T) {
	// Character trigrams run over the whole string, so stop-words-only text still
	// produces a weak vector rather than none. That is deliberate -- "the of and
	// to" and "the to and of" are near-identical strings and a trigram model
	// notices.
	//
	// What must not happen is stop-word noise matching a REAL complaint. A filer
	// who has typed four function words and nothing else should see nothing, or
	// the panel becomes something they dismiss on sight.
	//
	// The query here is deliberately different from the stored text. Comparing
	// stop-word text against itself scores 1.0 by definition and says nothing.
	store, uid, pid := dupFixture(t)
	ctx := context.Background()
	for _, title := range []string{
		"a real complaint about ranking",
		"upload fails with no error message",
	} {
		fileComplaint(t, store, pid, uid, title, "body")
	}

	for _, probe := range []string{"the of and to", "it is was a", "to be or not"} {
		sims, err := store.FindSimilar(ctx, KindComplaint, probe, pid, 5)
		if err != nil {
			t.Fatalf("FindSimilar(%q): %v", probe, err)
		}
		for _, s := range sims {
			if embed.IsDuplicate(s.Score) {
				t.Errorf("probe %q matched %q (id %d) at %.3f: function-word noise "+
					"reached the advisory threshold", probe, s.Title, s.EntityID, s.Score)
			}
		}
	}
}

func TestVectorsFromADifferentModelAreIgnored(t *testing.T) {
	// The failure this guards: cosine over vectors from two different models
	// returns a number in [-1,1] that means nothing, and "duplicate detection is
	// nonsense" surfaces weeks later with no cause.
	store, uid, pid := dupFixture(t)
	ctx := context.Background()
	const title = "Search results are identical for every query"
	fileComplaint(t, store, pid, uid, title, "body")

	// Store a vector under a different model id, as a model change would leave.
	if _, err := store.ExecContext(ctx,
		`UPDATE embeddings SET model_id='other-model-v9' WHERE kind=?`, KindComplaint); err != nil {
		t.Fatalf("rewrite model_id: %v", err)
	}
	sims, err := store.FindSimilar(ctx, KindComplaint, title, pid, 5)
	if err != nil {
		t.Fatalf("FindSimilar: %v", err)
	}
	if len(sims) != 0 {
		t.Errorf("found %d matches against vectors from another model", len(sims))
	}

	// And coverage must report the index as empty for the current model, not full.
	cov, err := store.EmbeddingCoverage(ctx, KindComplaint)
	if err != nil {
		t.Fatalf("EmbeddingCoverage: %v", err)
	}
	if complete, _ := cov["complete"].(bool); complete {
		t.Error("coverage reports complete while every vector is from another model")
	}
}

func TestEmbeddingCoverageReflectsTheIndex(t *testing.T) {
	// Reported rather than assumed: a partially indexed corpus silently produces
	// "no duplicates found", which a filer reads as "nobody has reported this".
	store, uid, pid := dupFixture(t)
	ctx := context.Background()

	cov, err := store.EmbeddingCoverage(ctx, KindComplaint)
	if err != nil {
		t.Fatalf("EmbeddingCoverage: %v", err)
	}
	if total, _ := cov["total"].(int); total != 0 {
		t.Errorf("empty fixture reports %v complaints", cov["total"])
	}

	for i := 0; i < 3; i++ {
		fileComplaint(t, store, pid, uid, fmt.Sprintf("complaint number %d about a distinct problem", i), "body")
	}
	// Two more complaints that were never embedded.
	for i := 0; i < 2; i++ {
		if _, err := store.CreateComplaint(ctx, pid, uid,
			fmt.Sprintf("unindexed complaint %d", i), "body", 1, 0.5, 1.0); err != nil {
			t.Fatalf("CreateComplaint: %v", err)
		}
	}

	cov, err = store.EmbeddingCoverage(ctx, KindComplaint)
	if err != nil {
		t.Fatalf("EmbeddingCoverage: %v", err)
	}
	embedded, _ := cov["embedded"].(int)
	total, _ := cov["total"].(int)
	if embedded != 3 || total != 5 {
		t.Errorf("coverage = %d/%d, want 3/5", embedded, total)
	}
	if complete, _ := cov["complete"].(bool); complete {
		t.Error("coverage reports complete with two unindexed complaints")
	}
}

func TestBackfillIndexesOnlyMissingRowsAndIsIdempotent(t *testing.T) {
	store, uid, pid := dupFixture(t)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if _, err := store.CreateComplaint(ctx, pid, uid,
			fmt.Sprintf("complaint %d about a different subject", i), "body text", 1, 0.5, 1.0); err != nil {
			t.Fatalf("CreateComplaint: %v", err)
		}
	}

	done, err := store.BackfillEmbeddingIndex(ctx, []string{KindComplaint})
	if err != nil {
		t.Fatalf("BackfillEmbeddingIndex: %v", err)
	}
	if done[KindComplaint] != 4 {
		t.Errorf("backfilled %d, want 4", done[KindComplaint])
	}

	// Second run must embed nothing: the rows exist under the current model.
	again, err := store.BackfillEmbeddingIndex(ctx, []string{KindComplaint})
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if again[KindComplaint] != 0 {
		t.Errorf("second backfill embedded %d rows, want 0", again[KindComplaint])
	}

	// Now detection works against the backfilled corpus.
	sims, err := store.FindSimilar(ctx, KindComplaint, "complaint 2 about a different subject", pid, 5)
	if err != nil {
		t.Fatalf("FindSimilar after backfill: %v", err)
	}
	if len(sims) == 0 {
		t.Error("backfilled rows are not findable: duplicate detection does not work")
	}
}

func TestFindSimilarWithoutAnEmbedderIsRefusedNotGuessed(t *testing.T) {
	// An unconfigured instance must say so rather than report "no duplicates".
	store, _, pid := setupWithProject(t)
	if _, err := store.FindSimilar(context.Background(), KindComplaint, "anything", pid, 5); err == nil {
		t.Error("FindSimilar with no embedder returned nil error, want a refusal")
	}
	if store.EmbedderID() != "" {
		t.Errorf("EmbedderID() = %q, want empty", store.EmbedderID())
	}
}

func TestDeleteEmbeddingRemovesAStaleVector(t *testing.T) {
	// Without this, a complaint deleted by a path that skips cleanup keeps
	// appearing in similarity results, pointing at a row that is gone.
	store, uid, pid := dupFixture(t)
	ctx := context.Background()
	id := fileComplaint(t, store, pid, uid, "a complaint that will be deleted", "body")

	if err := store.DeleteEmbedding(ctx, KindComplaint, id); err != nil {
		t.Fatalf("DeleteEmbedding: %v", err)
	}
	sims, err := store.FindSimilar(ctx, KindComplaint, "a complaint that will be deleted", pid, 5)
	if err != nil {
		t.Fatalf("FindSimilar: %v", err)
	}
	if len(sims) != 0 {
		t.Errorf("deleted complaint still appears in %d matches", len(sims))
	}
}

func TestStaleEmbeddingsForDeletedEntitiesAreSkipped(t *testing.T) {
	// Same problem, reached the other way: a vector survives because cleanup did
	// not run. Showing "complaint #412" with an empty title is worse than not
	// showing it, so hydrate drops it.
	store, uid, pid := dupFixture(t)
	ctx := context.Background()
	fileComplaint(t, store, pid, uid, "a complaint that exists", "body")

	// A vector for an entity that was never created.
	if err := store.UpsertEmbedding(ctx, KindComplaint, 999999, pid, "ghost entry text"); err != nil {
		t.Fatalf("UpsertEmbedding: %v", err)
	}
	sims, err := store.FindSimilar(ctx, KindComplaint, "ghost entry text", pid, 5)
	if err != nil {
		t.Fatalf("FindSimilar: %v", err)
	}
	for _, s := range sims {
		if s.EntityID == 999999 {
			t.Error("a match was returned for an entity that does not exist")
		}
	}
}

func firstScore(sims []embed.Similarity) float64 {
	if len(sims) == 0 {
		return 0
	}
	return sims[0].Score
}

// A row with nothing to embed must not hold coverage below 100% forever.
//
// BackfillEmbeddingIndex skips empty text on purpose, so if `total` counts those
// rows then a database whose ONLY unindexed rows are unembeddable reports
// "incomplete" for ever. The live instance hit exactly this: one complaint with an
// empty title AND body, complaint coverage stuck at 99.8%, and a startup warning
// that would fire on every deploy until someone learned to ignore it.
//
// A warning that always fires is worse than no warning, because it teaches the
// reader that warnings here are noise.
func TestEmbeddingCoverageReachesCompleteWhenOnlyUnembeddableRowsRemain(t *testing.T) {
	store, uid, pid := dupFixture(t)
	ctx := context.Background()

	if _, err := store.DB.ExecContext(ctx,
		`INSERT INTO complaints (project_id, author_id, title, body, created_at, updated_at)
		 VALUES (?, ?, '', '', 1, 1)`, pid, uid); err != nil {
		t.Fatalf("insert an unembeddable complaint: %v", err)
	}

	// Every embeddable row IS indexed, so coverage must read complete.
	cov, err := store.EmbeddingCoverage(ctx, KindComplaint)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if complete, _ := cov["complete"].(bool); !complete {
		unembed, _ := cov["unembeddable"].(int)
		total, _ := cov["total"].(int)
		embedded, _ := cov["embedded"].(int)
		t.Errorf("coverage is incomplete with only unembeddable rows left: "+
			"embedded=%d total=%d unembeddable=%d. An unembeddable row must not "+
			"be counted in the denominator, or coverage can never reach 100%%.",
			embedded, total, unembed)
	}
	// And the count must be REPORTED, not silently dropped: a reader seeing
	// 555/556 needs to know where the last row went.
	if unembed, _ := cov["unembeddable"].(int); unembed != 1 {
		t.Errorf("unembeddable=%d, want 1: the skipped row must be reported so "+
			"a coverage number that cannot reach 100%% stays explainable", unembed)
	}
}
