package embed

import "testing"

// The embedder's only job is answering one question well: is this the same
// problem as something already filed? These tests are about that, and the
// paraphrases are the real cases from the corpus rather than invented ones.

// score is a readable helper: tests assert on similarity, so this is the number
// under test.
func score(t *testing.T, h Embedder, a, b string) float64 {
	t.Helper()
	return Cosine(h.Embed(a), h.Embed(b))
}

func TestParaphraseScoresHigherThanUnrelated(t *testing.T) {
	h := NewHashed()

	// The case token overlap alone fails: one shared token, same problem.
	paraphrase := score(t, h,
		"cannot resume upload after a network drop",
		"large uploads fail silently and restart from zero")
	unrelated := score(t, h,
		"cannot resume upload after a network drop",
		"the changelog does not list contributors in order")

	if paraphrase <= unrelated {
		t.Errorf("paraphrase %v <= unrelated %v: the embedder cannot tell them apart",
			paraphrase, unrelated)
	}
}

// TestCalibratedThresholdsOnRealCorpus pins the numbers the filing-time gate
// depends on, measured rather than guessed.
//
// Measured over 400 real complaint titles from the live database (79,800 pairs)
// with cmd/embedcalib:
//
//	identical titles            1.00
//	prefix of another title     0.92
//	same shape, different file  0.81   (stats_icons_dark vs _light)
//	same shape, different verb  0.74   (SQLite vs PostgreSQL)
//	unrelated                   0.04 mean, and NEGATIVE at the bottom
//
// Two properties make the gate safe, and both come from the measurement:
//
//   - separation: duplicates sit at 0.70+, unrelated at or below ~0.10. There is
//     no middle band where the two populations overlap.
//   - signed hashing pushes collisions negative rather than positive, so an
//     unlucky bucket match lowers a score instead of raising it.
//
// This test asserts the separation holds for the cases that matter. It does not
// assert a paraphrase score, because a bag of words has no synonymy: "cannot
// resume upload" and "restart from zero" share one token, and no amount of
// n-gram tuning invents the missing concept. That limit is real and is why
// Embedder is an interface.
func TestCalibratedThresholdsOnRealCorpus(t *testing.T) {
	h := NewHashed()

	t.Run("true duplicate is well above the gate", func(t *testing.T) {
		if got := score(t, h,
			"Search results are identical for every query",
			"Search results are identical for every query"); got < StrongDuplicateThreshold {
			t.Errorf("identical text %v is below the strong threshold %v", got, StrongDuplicateThreshold)
		}
	})

	t.Run("one title being a prefix of another is caught", func(t *testing.T) {
		// Measured 0.92 on the real corpus. This is the "same complaint, more
		// detail added" case, which is the most common shape of a real duplicate.
		got := score(t, h,
			"Add the reader-facing page copy; end every feed description with the page URL",
			"Add the reader-facing page copy; end every feed description with the page URL; merge on publish")
		if got < DuplicateThreshold {
			t.Errorf("prefix pair scored %v, want >= %v", got, DuplicateThreshold)
		}
	})

	t.Run("morphological variants are caught", func(t *testing.T) {
		// Measured 0.81. Same file, different word -- caught by character
		// n-grams, missed entirely by token overlap.
		got := score(t, h,
			"Update stats_icons_darkmode.css",
			"Update stats_icons_lightmode.css")
		if got < DuplicateThreshold {
			t.Errorf("morphological pair scored %v, want >= %v", got, DuplicateThreshold)
		}
	})

	t.Run("unrelated stays far below the gate", func(t *testing.T) {
		unrelated := [][]string{
			{"No authentication existed, so the site was read-only for everyone", "epub-builder"},
			{"Configuration drift between services is invisible until runtime", "add errors.txt file"},
			{"Moderation removes content without recording why", "LLM providers"},
		}
		for _, p := range unrelated {
			if got := score(t, h, p[0], p[1]); got > DuplicateThreshold/2 {
				t.Errorf("unrelated pair scored %v (want well under %v): %q || %q",
					got, DuplicateThreshold, p[0], p[1])
			}
		}
	})
}

// TestParaphraseOrdersAboveUnrelatedButMayScoreLow is the honest statement of
// this embedder's limit, kept as a test so the boundary is documented rather than
// rediscovered.
func TestParaphraseOrdersAboveUnrelatedButMayScoreLow(t *testing.T) {
	h := NewHashed()
	const a = "cannot resume upload after a network drop"
	const b = "large uploads fail silently and restart from zero"
	got := score(t, h, a, b)

	// It does order correctly above unrelated text...
	if got <= score(t, h, a, "the changelog does not list contributors in order") {
		t.Error("paraphrase did not outscore unrelated text")
	}
	// ...but it does not reach the gate, and that is expected rather than a bug to
	// tune away: the two sentences share one content word. Catching this class
	// needs a model with synonymy, which is what swapping the Embedder does.
	if got >= DuplicateThreshold {
		t.Logf("paraphrase scored %v, at or above the gate -- better than expected", got)
	}
}

func TestIdenticalTextScoresOne(t *testing.T) {
	h := NewHashed()
	const text = "kanban board ignores WIP limits on drag and drop"
	got := score(t, h, text, text)
	if got < 0.999 {
		t.Errorf("identical text scored %v, want 1.0", got)
	}
}

func TestEmptyTextDoesNotMatchEverything(t *testing.T) {
	// A zero vector must be inert. If it were not, every filing would be
	// reported as similar to every other filing, which is the failure mode that
	// makes a duplicate checker unusable.
	h := NewHashed()
	empty := h.Embed("")
	if len(empty) != h.Dims() {
		t.Fatalf("empty text produced %d dims, want %d", len(empty), h.Dims())
	}
	if got := Cosine(empty, h.Embed("something entirely different")); got != 0 {
		t.Errorf("empty text scored %v against real text, want 0", got)
	}
	if got := Cosine(empty, empty); got != 0 {
		t.Errorf("empty vs empty scored %v, want 0", got)
	}
}

func TestStopWordsOnlyIsTreatedAsEmpty(t *testing.T) {
	// "the of and to" carries no topic. Embedding it must not produce a vector
	// that spuriously resembles other stop-word-only text.
	h := NewHashed()
	a := Cosine(h.Embed("the of and to"), h.Embed("it is was a"))
	if a > 0.001 {
		t.Errorf("stop-word-only texts scored %v, want ~0", a)
	}
}

func TestVeryShortTextIsStillEmbedded(t *testing.T) {
	// Padding in trigrams exists for exactly this: without it a one-word
	// complaint produces no character features and is invisible.
	h := NewHashed()
	got := Cosine(h.Embed("crash"), h.Embed("crash"))
	if got < 0.999 {
		t.Errorf("one-word text scored %v against itself, want 1.0", got)
	}
	if Cosine(h.Embed("crash"), h.Embed("crashes")) <= 0 {
		t.Error("'crash' and 'crashes' are unrelated: no morphological signal")
	}
}

func TestCosineRejectsMismatchedDimensions(t *testing.T) {
	// A dimension mismatch means the vectors came from different models.
	// Returning 0 means a caller who forgets to check gets "no similarity"
	// rather than a panic or a silent truncation of one vector.
	if got := Cosine(make([]float32, 4), make([]float32, 8)); got != 0 {
		t.Errorf("mismatched dims scored %v, want 0", got)
	}
	if got := Cosine(nil, nil); got != 0 {
		t.Errorf("nil vectors scored %v, want 0", got)
	}
}

func TestEncodeDecodeRoundTrips(t *testing.T) {
	h := NewHashed()
	orig := h.Embed("round trip through a blob")
	back := Decode(Encode(orig))
	if len(back) != len(orig) {
		t.Fatalf("decoded %d dims, want %d", len(back), len(orig))
	}
	for i := range orig {
		if orig[i] != back[i] {
			t.Fatalf("dim %d: got %v, want %v", i, back[i], orig[i])
		}
	}
}

func TestDecodeRejectsPartialFloats(t *testing.T) {
	// A truncated blob must be ignored, not half-read: a vector of garbage
	// would produce confident nonsense similarities.
	if got := Decode([]byte{1, 2, 3}); got != nil {
		t.Errorf("3-byte blob decoded to %v, want nil", got)
	}
	if got := Decode(nil); got != nil {
		t.Errorf("nil decoded to %v, want nil", got)
	}
}

func TestRankOrdersAndLimitsDeterministically(t *testing.T) {
	sims := []Similarity{
		{EntityID: 3, Score: 0.5},
		{EntityID: 1, Score: 0.9},
		{EntityID: 2, Score: 0.9}, // ties with 1
	}
	got := Rank(sims, 2)
	if len(got) != 2 {
		t.Fatalf("Rank(3, 2) returned %d", len(got))
	}
	// Ties break on entity id, so 1 precedes 2 and pagination is stable.
	if got[0].EntityID != 1 || got[1].EntityID != 2 {
		t.Errorf("order = [%d %d], want [1 2] (ties break on id)", got[0].EntityID, got[1].EntityID)
	}
	// A limit of 0 means "no limit", which callers rely on for "is there any
	// duplicate at all".
	if n := len(Rank(sims, 0)); n != 3 {
		t.Errorf("Rank(_, 0) returned %d, want all 3", n)
	}
}

func TestHashedVectorsAreUnitLength(t *testing.T) {
	// Cosine does its own normalisation, but unit vectors mean a stored vector
	// can be compared by a dot product if the scan ever moves into SQL.
	h := NewHashed()
	v := h.Embed("some complaint about upload failures")
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum < 0.99 || sum > 1.01 {
		t.Errorf("vector length squared = %v, want ~1.0", sum)
	}
}
