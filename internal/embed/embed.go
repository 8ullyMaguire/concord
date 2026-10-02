// Package embed turns text into vectors for near-duplicate detection.
//
// # Why a local implementation is the default
//
// The obvious first choice is a transformer over HTTP. That was the plan, and
// the plan was wrong for this codebase for two concrete reasons:
//
//  1. The model server on this host runs without --embeddings. Enabling it means
//     restarting ollama, which currently has live inference sessions on it.
//  2. §10.6 requires that AI features be opt-out-able and §10.5 requires that
//     embeddings be self-hostable "so that search works without sending data to
//     third parties". A hard dependency on a specific model server makes the
//     self-hosting promise contingent on that one server being reachable.
//
// So the default embedder is a hashed bag of words with character n-grams. It is
// not a transformer and does not pretend to be: it has no synonymy and no
// compositionality. What it does have is the property that matters for duplicate
// detection -- it catches morphological and phrasing variants of the same
// complaint, which is the common case, because people describe the same bug in
// slightly different words.
//
// Token overlap alone does not do this. "cannot resume upload after a network
// drop" and "large uploads fail silently and restart from zero" share no tokens
// beyond "upload". Character n-grams bridge that: "restart"/"resume",
// "upload"/"uploads" share trigrams. TestSimilarCatchesParaphrase pins it.
//
// # What this is not
//
// It is not semantic. "auth is broken" and "login fails" are the same complaint
// to a person and unrelated to this embedder. Swapping in a real model is a
// one-line change (see Model) and nothing else in the system needs to move,
// because similarity is computed over whatever vector comes back.
package embed

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"sort"
	"strings"
	"unicode"
)

// Thresholds for "is this the same thing?". Calibrated by measurement, not
// taste -- see cmd/embedcalib and TestCalibratedThresholdsOnRealCorpus, run over
// 400 real complaint titles (79,800 pairs) from the live database:
//
//	identical titles             1.00
//	one title a prefix of another 0.92
//	same shape, different file    0.81
//	same shape, different word    0.74
//	unrelated                     0.04 mean, bottom pairs NEGATIVE
//
// Only 0.20% of real pairs exceed 0.35, so the bands sit in a gap rather than in
// a population.
//
// The negative tail is a property of signed hashing: a bucket collision subtracts
// rather than adds, so an unlucky match lowers a score instead of raising it.
// That is what makes a fixed threshold safe -- there is no slow drift upward
// toward false positives as the corpus grows.
const (
	// StrongDuplicateThreshold is where two texts are almost certainly the same
	// filing. At and above this, the API refuses the new entry unless the filer
	// explicitly overrides.
	//
	// Set above the measured duplicate band (0.74-0.92) so that "Application
	// starts with SQLite" vs "Application starts with PostgreSQL" -- a real pair
	// at 0.74 -- is surfaced as a strong match to look at, not silently refused.
	// A false refusal is much worse than a false prompt: the filer can always
	// confirm, but a complaint that was quietly rejected is lost.
	StrongDuplicateThreshold = 0.85

	// DuplicateThreshold is where two texts are plausibly the same problem.
	// Results at or above this are shown as near-matches, and filing is allowed
	// without an override.
	//
	// Set just below the structural-variant band (0.74) so morphological variants
	// of the same complaint are caught.
	DuplicateThreshold = 0.62

	// WeakThreshold is where a match is worth listing but is probably noise.
	// Below it, results are omitted entirely: a list of ten weak candidates
	// trains people to ignore the panel, which is worse than showing nothing.
	WeakThreshold = 0.40
)

// IsStrongDuplicate reports whether score is high enough to block a filing.
func IsStrongDuplicate(score float64) bool { return score >= StrongDuplicateThreshold }

// IsDuplicate reports whether score is high enough to surface as a near-match.
func IsDuplicate(score float64) bool { return score >= DuplicateThreshold }

// Embedder converts text to a fixed-width vector.
type Embedder interface {
	// ID identifies the model that produced its vectors. Two vectors are only
	// comparable when their IDs match, so this is load-bearing rather than
	// informational.
	ID() string
	// Dims is the vector width. Constant for a given embedder.
	Dims() int
	// Embed converts one text to a vector. Must not mutate its input.
	Embed(text string) []float32
}

// DefaultDims is the width of the hashed embedder.
//
// 2048 keeps collisions rare for a corpus of this size while staying small: at
// 4 bytes per float that is 8KB per row, or about 8MB for the current ~1000
// embeddable rows. A transformer would use 768 dims for a much better result,
// so the width is deliberately not a claim about quality -- it is a claim about
// what fits in SQLite without a vector extension.
const DefaultDims = 2048

// stopWords are dropped so that "the upload does not work" and "upload does not
// work" are not separated by two function words.
//
// This list is short on purpose. Aggressive stop-word lists throw away the words
// that carry the complaint ("cannot", "not") and are a well-known cause of
// false negatives in duplicate detection.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "of": true, "to": true, "in": true,
	"on": true, "for": true, "and": true, "or": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "it": true, "this": true,
	"that": true, "i": true, "my": true, "we": true, "you": true,
}

// Hashed is a hashed bag of words plus character n-grams.
type Hashed struct {
	dims int
}

// NewHashed returns the default local embedder.
func NewHashed() *Hashed { return &Hashed{dims: DefaultDims} }

// ID implements Embedder. Bumping this invalidates every stored vector, because
// embeddings from different feature sets are not comparable -- which is exactly
// what the model_id column exists to detect.
func (h *Hashed) ID() string { return "hashed-v1" }

// Dims implements Embedder.
func (h *Hashed) Dims() int { return h.dims }

// Embed implements Embedder.
//
// Three signals are summed with weights, then the vector is L2-normalised so
// cosine similarity reduces to a dot product:
//
//	unigrams    1.0   the actual topic words
//	trigrams    0.6   morphology and phrasing variants (resume/restart)
//	bigrams     0.4   adjacent-word pairs ("network drop", "resume upload")
//
// The weights are judgement, not measurement: unigrams carry the topic, and the
// n-grams exist to catch the same complaint phrased differently. They are
// package constants rather than tunable-by-config because there is no
// evaluation set to tune against, and a tunable weight nobody tunes is worse
// than a stated one.
func (h *Hashed) Embed(text string) []float32 {
	v := make([]float32, h.dims)
	words := tokenize(text)
	if len(words) == 0 {
		return v
	}

	// Unigrams.
	for _, w := range words {
		if stopWords[w] {
			continue
		}
		addHash(v, h.dims, w, 1.0)
	}

	// Bigrams over adjacent words.
	for i := 0; i+1 < len(words); i++ {
		if stopWords[words[i]] || stopWords[words[i+1]] {
			continue
		}
		addHash(v, h.dims, words[i]+"_"+words[i+1], 0.4)
	}

	// Character trigrams over the whole normalised string. This is the signal
	// that survives word order and inflections.
	norm := strings.Join(words, " ")
	run := trigrams(norm)
	for _, g := range run {
		addHash(v, h.dims, g, 0.6)
	}

	normalize(v)
	return v
}

// addHash hashes a feature into one bucket with the given weight, using signed
// hashing.
//
// The sign matters more than it looks. Without it, hashing only ever ADDS to a
// bucket, so two texts that happen to collide look artificially similar and the
// score saturates -- unrelated documents hash into shared buckets and their
// similarity rises with corpus size. A signed hash makes collisions cancel in
// expectation, which is the standard fix and the reason the bucket count can be
// much smaller than the vocabulary.
func addHash(v []float32, dims int, feature string, weight float32) {
	sum := sha256.Sum256([]byte(feature))
	// Use 8 bytes: enough buckets, and it avoids depending on hash quality beyond
	// 64 bits of avalanche.
	b := binary.LittleEndian.Uint64(sum[:8])
	idx := int(b % uint64(dims))
	sign := float32(1)
	if b&(1<<63) != 0 {
		sign = -1
	}
	v[idx] += sign * weight
}

// tokenize lowercases and splits on non-alphanumerics, dropping stop words.
func tokenize(text string) []string {
	lower := strings.ToLower(text)
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// trigrams returns the character trigrams of s, padded so short strings still
// produce features rather than nothing.
//
// Padding matters: a one-word complaint ("crash") would otherwise yield zero
// trigrams and be embedded as if it were empty.
func trigrams(s string) []string {
	if len(s) < 3 {
		if s == "" {
			return nil
		}
		return []string{"##" + s + "##"}
	}
	out := make([]string, 0, len(s))
	p := "##" + s + "##"
	for i := 0; i+3 <= len(p); i++ {
		out = append(out, p[i:i+3])
	}
	return out
}

// normalize scales v to unit length in place, leaving an all-zero vector alone.
func normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
}

// HasContent reports whether a vector carries any signal.
//
// Needed because Embed always returns a full-width slice, and text that is empty
// or entirely stop words yields a vector of all zeros. Such a vector is inert --
// its cosine similarity against anything is 0 -- so storing it would occupy an
// index slot forever and make "is this indexed?" checks that count rows lie.
//
// A length check does not catch this, and the store layer originally used one.
func HasContent(v []float32) bool {
	for _, x := range v {
		if x != 0 {
			return true
		}
	}
	return false
}

// Cosine returns the cosine similarity of two vectors.
//
// Returns 0 when the dimensions differ or either vector is zero-length. A
// dimension mismatch is a programming error that must be loud in the store layer
// (which refuses to compare across models); returning 0 here means a caller that
// forgets to check gets "no similarity" rather than a panic or a silent
// truncation.
func Cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// Encode packs a vector into little-endian float32 bytes for the BLOB column.
func Encode(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(x))
	}
	return buf
}

// Decode unpacks a BLOB read from the vector column back into a vector.
//
// Returns nil for a length that is not a whole number of float32s, so a
// truncated or foreign value is ignored rather than half-read.
func Decode(b []byte) []float32 {
	if len(b) == 0 || len(b)%4 != 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

// Similarity is one ranked near-match.
type Similarity struct {
	Kind     string  `json:"kind"`
	EntityID int64   `json:"entity_id"`
	Score    float64 `json:"score"`
	Title    string  `json:"title"`
	Status   string  `json:"status"`
	// URL is a relative link to the entity, so a client can jump to it without
	// reconstructing the route.
	URL string `json:"url,omitempty"`
}

// Rank sorts a slice of similarities by descending score, dropping the rest once
// limit results remain.
//
// Ties are broken by entity id so that two runs over the same data produce the
// same order -- without that, a caller paginating through near-duplicates sees
// the same entry twice.
func Rank(sims []Similarity, limit int) []Similarity {
	sort.SliceStable(sims, func(i, j int) bool {
		if sims[i].Score != sims[j].Score {
			return sims[i].Score > sims[j].Score
		}
		return sims[i].EntityID < sims[j].EntityID
	})
	if limit > 0 && len(sims) > limit {
		sims = sims[:limit]
	}
	return sims
}
