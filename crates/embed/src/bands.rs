//! Score bands, per model.
//!
//! The hashed and nomic scales are **not comparable**: identical titles score 1.00 under both, but a paraphrase scores
//! 0.37 under one and 0.61 under the other. A single global threshold silently mis-calibrates whichever embedder it was
//! not measured on, so the panel's bands and the 409 refusal both come from here, keyed by model id.

/// The band a score falls in. [`Class::None`] means below the weak band and not worth showing at all.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum Class {
    /// Not worth listing.
    None,
    /// Loosely related — context, not a duplicate claim.
    Weak,
    /// "You may already have this" — an advisory.
    Likely,
    /// Almost certainly the same filing.
    Strong,
}

/// nomic's strong band, and the reasoning is a correction of an earlier measurement rather than a refinement of it.
///
/// A first pass over 13 hand-built pairs put paraphrases at 0.613–0.787 and different-problem pairs at 0.371–0.473,
/// which looked like 0.14 of clean separation and suggested a 0.60 gate. Measured again against the **live** index —
/// paraphrases of stored complaints on one side, genuinely new complaints on the same topics on the other:
///
/// ```text
/// paraphrase of a stored complaint   0.730 - 0.862  (mean 0.810)
/// genuinely new, same topic          0.680 - 0.800  (mean 0.733)
/// separation                          -0.070
/// ```
///
/// The populations **overlap**. A real new complaint scored 0.800 while a true paraphrase scored 0.730, so no threshold
/// separates them and a 0.60 gate would refuse filings a maintainer wanted. The 13 hand-built pairs were too easy: both
/// members were written to be obviously about the same thing, and a benchmark built that way measures the benchmark.
///
/// 0.92 is therefore set where refusal is **safe** rather than where detection is best. Identical titles score 1.000 and
/// near-identical restatements 0.93, so the band still refuses the case it exists for — the same words twice — while
/// every measured paraphrase falls below it and is surfaced as a hint instead. That asymmetry is deliberate: a duplicate
/// in the queue costs a maintainer a minute, a lost complaint costs the report.
pub const OLLAMA_STRONG_THRESHOLD: f64 = 0.92;

/// nomic's advisory line, deliberately permissive: because the two populations overlap, precision is impossible here and
/// the right response to an imprecise signal is a cheap one. A filer who sees four related complaints and recognises
/// their own costs a second; a filer whose report is silently dropped because the checker was certain costs the report.
pub const OLLAMA_DUPLICATE_THRESHOLD: f64 = 0.60;

/// nomic's weak band, just under the observed different-problem range, so related-but-distinct items surface as context
/// without implying a duplicate.
pub const OLLAMA_WEAK_THRESHOLD: f64 = 0.35;

/// The score bands that belong to an embedder.
///
/// Returns `(strong, likely, weak)`. The nomic bands are measured against this instance's own corpus, not guessed.
#[must_use]
pub fn thresholds_for(model_id: &str) -> (f64, f64, f64) {
    if is_nomic(model_id) {
        return (
            OLLAMA_STRONG_THRESHOLD,
            OLLAMA_DUPLICATE_THRESHOLD,
            OLLAMA_WEAK_THRESHOLD,
        );
    }
    (
        super::STRONG_DUPLICATE_THRESHOLD,
        super::DUPLICATE_THRESHOLD,
        super::WEAK_THRESHOLD,
    )
}

/// Whether the model id names a nomic checkpoint.
///
/// A prefix test rather than an equality test because the tag varies (`nomic-embed-text:latest`, `:v1.5`) and every tag
/// is the same vector space; a *different model family* is not.
#[must_use]
pub fn is_nomic(model_id: &str) -> bool {
    model_id.starts_with("nomic")
}

/// The single place a score becomes a decision, for a given model.
///
/// One function so the panel, the 409 gate and the tests cannot drift apart on which band they use.
#[must_use]
pub fn classify(score: f64, model_id: &str) -> Class {
    let (strong, likely, weak) = thresholds_for(model_id);
    if score >= strong {
        Class::Strong
    } else if score >= likely {
        Class::Likely
    } else if score >= weak {
        Class::Weak
    } else {
        Class::None
    }
}

/// Whether `score` blocks a filing for this model.
#[must_use]
pub fn is_strong_duplicate_for(score: f64, model_id: &str) -> bool {
    score >= thresholds_for(model_id).0
}

/// Whether `score` is advisory-worthy for this model.
#[must_use]
pub fn is_duplicate_for(score: f64, model_id: &str) -> bool {
    score >= thresholds_for(model_id).1
}
