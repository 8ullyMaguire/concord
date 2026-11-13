//! Near-duplicate detection: text to vector, and the thresholds that turn a score into a decision.
//!
//! This is the ported `internal/embed` package, kept behaviour-for-behaviour including the parts that look like
//! accidents. Two notes on the shape of it:
//!
//! * **the default embedder is a hashed bag of words plus character n-grams, not a transformer.** It has no synonymy
//!   and no compositionality; what it does have is the property duplicate detection actually needs at filing time —
//!   morphological and phrasing variants of the same complaint land close together, because the trigrams survive word
//!   order and inflection (`resume`/`restart`, `upload`/`uploads`). Measured on 13 hand-built pairs, it flagged 0 of 6
//!   paraphrases; Ollama's `nomic-embed-text` flags all 6. So this is the *fallback*, and the feature is only as good as
//!   which of the two is configured.
//! * **the signed hash is load-bearing.** Without a sign, hashing only ever adds to a bucket, so unrelated texts that
//!   collide look similar and the score drifts upward as the corpus grows. With it, collisions cancel in expectation and
//!   a fixed threshold stays safe. That property is why the same numeric band works at 1,000 rows and at 100,000.
//!
//! The three thresholds are calibrated against 400 real complaint titles (79,800 pairs) from the live database:
//!
//! ```text
//! identical titles              1.00
//! one title a prefix of another 0.92
//! same shape, different file    0.81
//! same shape, different word    0.74
//! unrelated                     0.04 mean, bottom pairs NEGATIVE
//! ```
//!
//! Only 0.20% of real pairs exceed 0.35, so the bands sit in a gap rather than inside a population.

use sha2::{Digest, Sha256};

pub mod bands;
pub mod ollama;

pub use bands::{classify, thresholds_for, Class};
pub use ollama::{normalize, BoxFuture, Ollama, OllamaConfig};

/// Where two texts are almost certainly the same filing. At or above this the API refuses a new entry unless the filer
/// explicitly overrides.
///
/// Set **above** the measured duplicate band (0.74–0.92) so that *"Application starts with SQLite"* vs *"Application
/// starts with PostgreSQL"* — a real pair at 0.74 — is surfaced as a strong match to look at rather than silently
/// refused. A false refusal is much worse than a false prompt: the filer can always confirm, but a complaint that was
/// quietly rejected is lost.
pub const STRONG_DUPLICATE_THRESHOLD: f64 = 0.85;

/// Where two texts are plausibly the same problem. At or above this they are shown as near-matches and filing proceeds
/// without an override. Set just below the structural-variant band (0.74) so morphological variants are caught.
pub const DUPLICATE_THRESHOLD: f64 = 0.62;

/// Where a match is worth listing but is probably noise. Below it results are omitted entirely: a list of ten weak
/// candidates trains people to ignore the panel, which is worse than showing nothing.
pub const WEAK_THRESHOLD: f64 = 0.40;

/// Width of the hashed embedder: 2048 keeps collisions rare for a corpus of this size while staying small — 8 KB per row
/// at 4 bytes per float. A transformer would use 768 dimensions for a much better result, so the width is a claim about
/// what fits in the database, not about quality.
pub const DEFAULT_DIMS: usize = 2048;

/// Whether `score` blocks a filing.
#[must_use]
pub fn is_strong_duplicate(score: f64) -> bool {
    score >= STRONG_DUPLICATE_THRESHOLD
}

/// Whether `score` surfaces as a near-match.
#[must_use]
pub fn is_duplicate(score: f64) -> bool {
    score >= DUPLICATE_THRESHOLD
}

/// The words dropped from the unigram and bigram signals.
///
/// Short on purpose. Aggressive stop-word lists throw away the words that carry the complaint (`cannot`, `not`) and are
/// a well-known cause of false negatives in duplicate detection.
const STOP_WORDS: [&str; 23] = [
    "a", "an", "the", "of", "to", "in", "on", "for", "and", "or", "is", "are", "was", "were", "be", "been", "it",
    "this", "that", "i", "my", "we", "you",
];

fn is_stop_word(word: &str) -> bool {
    STOP_WORDS.contains(&word)
}

/// Converts text to a fixed-width vector.
///
/// Async and object-safe on purpose: the remote embedder makes a network call on the filing path, and the store holds
/// one of these behind a `dyn` trait object. A sync trait would force the call to block a runtime thread.
pub trait Embedder: Send + Sync {
    /// Identifies the model that produced a vector. Two vectors are only comparable when their ids match.
    fn id(&self) -> String;
    /// The vector width. Constant for a given embedder.
    fn dims(&self) -> usize;
    /// Convert one text to a vector. Empty for text with nothing indexable.
    fn embed<'a>(&'a self, text: &'a str) -> BoxFuture<'a, Vec<f32>>;
}

/// A hashed bag of words plus character n-grams.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Hashed {
    dims: usize,
}

impl Hashed {
    /// The default local embedder.
    #[must_use]
    pub fn new() -> Self {
        Self { dims: DEFAULT_DIMS }
    }

    /// Identifies the model that produced a vector. Two vectors are only comparable when their ids match, which is why
    /// the database stores the id next to the blob rather than trusting configuration to stay fixed.
    ///
    /// Bumping this invalidates every stored vector, because embeddings from different feature sets are not comparable.
    #[must_use]
    pub fn id(&self) -> &'static str {
        "hashed-v1"
    }

    /// The vector width. Constant for a given embedder.
    #[must_use]
    pub fn dims(&self) -> usize {
        self.dims
    }

    /// Convert one text to a vector of `dims` floats.
    ///
    /// Three signals are summed with weights, then the vector is L2-normalised so cosine similarity reduces to a dot
    /// product:
    ///
    /// ```text
    /// unigrams    1.0   the actual topic words
    /// trigrams    0.6   morphology and phrasing variants (resume/restart)
    /// bigrams     0.4   adjacent-word pairs ("network drop", "resume upload")
    /// ```
    ///
    /// The weights are judgement, not measurement: unigrams carry the topic and the n-grams exist to catch the same
    /// complaint phrased differently. They are constants rather than configuration because there is no evaluation set to
    /// tune against, and a tunable weight nobody tunes is worse than a stated one.
    #[must_use]
    pub fn embed(&self, text: &str) -> Vec<f32> {
        let mut v = vec![0.0_f32; self.dims];
        let words = tokenize(text);
        if words.is_empty() {
            return v;
        }

        for word in &words {
            if is_stop_word(word) {
                continue;
            }
            add_hash(&mut v, word.as_bytes(), 1.0);
        }

        for pair in words.windows(2) {
            if is_stop_word(&pair[0]) || is_stop_word(&pair[1]) {
                continue;
            }
            add_hash(&mut v, format!("{}_{}", pair[0], pair[1]).as_bytes(), 0.4);
        }

        // Trigrams run over the whole normalised string, stop words included: dropping them here would break the
        // adjacency the signal is made of.
        let normalised = words.join(" ");
        for gram in trigrams(normalised.as_bytes()) {
            add_hash(&mut v, &gram, 0.6);
        }

        normalize_in_place(&mut v);
        v
    }
}

impl Embedder for Hashed {
    fn id(&self) -> String {
        "hashed-v1".to_string()
    }

    fn dims(&self) -> usize {
        self.dims
    }

    fn embed<'a>(&'a self, text: &'a str) -> BoxFuture<'a, Vec<f32>> {
        // The local embedder does no I/O, so the future is ready on the first poll. Boxed anyway to satisfy the trait:
        // one shape for both embedders beats two call sites.
        let vector = Hashed::embed(self, text);
        Box::pin(async move { vector })
    }
}

/// Hash one feature into one bucket at `weight`, with a sign taken from the hash.
///
/// The first 8 bytes of SHA-256 are read little-endian: enough buckets, and no dependence on hash quality beyond 64 bits
/// of avalanche.
fn add_hash(v: &mut [f32], feature: &[u8], weight: f32) {
    let sum = Sha256::digest(feature);
    let bucket = u64::from_le_bytes(sum[..8].try_into().expect("sha256 is 32 bytes"));
    let index = (bucket % v.len() as u64) as usize;
    // The top bit of the same 64 picks the sign, so the sign and the bucket are independent draws from one digest.
    let sign = if bucket & (1 << 63) != 0 { -1.0 } else { 1.0 };
    v[index] += sign * weight;
}

/// Lowercase, then split on anything that is not a letter or a digit.
///
/// Stop words are **not** removed here — they are skipped at the unigram and bigram sites and deliberately kept for the
/// trigram signal.
fn tokenize(text: &str) -> Vec<String> {
    let mut out = Vec::new();
    let mut current = String::new();
    for ch in text.to_lowercase().chars() {
        if ch.is_alphanumeric() {
            current.push(ch);
            continue;
        }
        if !current.is_empty() {
            out.push(std::mem::take(&mut current));
        }
    }
    if !current.is_empty() {
        out.push(current);
    }
    out
}

/// Character trigrams of `s`'s byte string, padded so short strings still produce a feature.
///
/// **Bytes, not characters.** The reference implementation slices Go strings by index, which is a byte slice, so a
/// non-ASCII text produces trigrams of raw bytes — including sequences that are not valid UTF-8. Hashing raw byte slices
/// reproduces that exactly; converting to `char`s would silently change every non-ASCII score.
///
/// Padding matters for the same reason: a one-word complaint (*"crash"*) would otherwise yield no trigrams at all and
/// embed as if it were empty. And a string shorter than three bytes yields the **whole padded string as one feature**
/// (`"##x##"`), not a three-byte slice of it — a distinction worth four bytes of score.
fn trigrams(bytes: &[u8]) -> Vec<Vec<u8>> {
    if bytes.is_empty() {
        return Vec::new();
    }
    let mut padded = Vec::with_capacity(bytes.len() + 4);
    padded.extend_from_slice(b"##");
    padded.extend_from_slice(bytes);
    padded.extend_from_slice(b"##");
    if bytes.len() < 3 {
        return vec![padded];
    }
    padded.windows(3).map(<[u8]>::to_vec).collect()
}

/// Scale `v` to unit length in place, leaving an all-zero vector alone.
fn normalize_in_place(v: &mut [f32]) {
    let mut sum = 0.0_f64;
    for x in v.iter() {
        sum += f64::from(*x) * f64::from(*x);
    }
    if sum == 0.0 {
        return;
    }
    // The reciprocal of the square root is taken in f64 and narrowed once, matching the reference: narrowing first then
    // multiplying would round differently in the last bit on a few buckets.
    #[allow(clippy::cast_possible_truncation)]
    let inverse = (1.0 / sum.sqrt()) as f32;
    for x in v.iter_mut() {
        *x *= inverse;
    }
}

/// Whether a vector carries any signal.
///
/// Needed because `embed` always returns a full-width slice, and text that is empty or entirely stop words yields all
/// zeros. Such a vector is inert — its cosine against anything is 0 — so storing it occupies an index slot forever and
/// makes "is this indexed?" checks that count rows lie. A length check does not catch this.
#[must_use]
pub fn has_content(v: &[f32]) -> bool {
    v.iter().any(|x| *x != 0.0)
}

/// Cosine similarity of two vectors.
///
/// Returns 0 when the dimensions differ or either vector is all zeros. A dimension mismatch is a programming error that
/// the store layer refuses loudly; returning 0 here means a caller that forgets to check gets "no similarity" rather
/// than a panic or a silent truncation.
#[must_use]
pub fn cosine(a: &[f32], b: &[f32]) -> f64 {
    if a.is_empty() || a.len() != b.len() {
        return 0.0;
    }
    let (mut dot, mut norm_a, mut norm_b) = (0.0_f64, 0.0_f64, 0.0_f64);
    for (x, y) in a.iter().zip(b.iter()) {
        let (x, y) = (f64::from(*x), f64::from(*y));
        dot += x * y;
        norm_a += x * x;
        norm_b += y * y;
    }
    if norm_a == 0.0 || norm_b == 0.0 {
        return 0.0;
    }
    dot / (norm_a.sqrt() * norm_b.sqrt())
}

/// Pack a vector into little-endian float32 bytes for the blob column.
#[must_use]
pub fn encode(v: &[f32]) -> Vec<u8> {
    let mut out = Vec::with_capacity(4 * v.len());
    for x in v {
        out.extend_from_slice(&x.to_le_bytes());
    }
    out
}

/// Unpack a blob back into a vector.
///
/// Returns empty for a length that is not a whole number of floats, so a truncated or foreign value is ignored rather
/// than half-read.
#[must_use]
pub fn decode(bytes: &[u8]) -> Vec<f32> {
    if bytes.is_empty() || bytes.len() % 4 != 0 {
        return Vec::new();
    }
    bytes
        .chunks_exact(4)
        .map(|c| f32::from_le_bytes([c[0], c[1], c[2], c[3]]))
        .collect()
}

/// One ranked near-match.
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct Similarity {
    /// Which entity type — `request`, `complaint`, `feature`.
    pub kind: String,
    /// Its row.
    pub entity_id: i64,
    /// Cosine score.
    pub score: f64,
    /// Its title, so the panel can show something a person recognises.
    pub title: String,
    /// Its status.
    pub status: String,
    /// A relative link to the entity, so a client can jump to it without reconstructing the route.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub url: Option<String>,
}

/// Sort by descending score, then by entity id, and truncate to `limit`.
///
/// The id tiebreak is not cosmetic: without it two runs over the same data can order equal scores differently, and a
/// caller paginating through near-duplicates sees the same entry twice.
#[must_use]
pub fn rank(mut sims: Vec<Similarity>, limit: usize) -> Vec<Similarity> {
    sims.sort_by(|a, b| {
        b.score
            .partial_cmp(&a.score)
            .unwrap_or(std::cmp::Ordering::Equal)
            .then(a.entity_id.cmp(&b.entity_id))
    });
    if limit > 0 && sims.len() > limit {
        sims.truncate(limit);
    }
    sims
}
