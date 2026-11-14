//! Concord's Akinator-style discovery engine (§4, §5).
//!
//! It takes candidate attributes and returns questions and rankings. It holds no pool and reads no database: everything it needs
//! arrives as plain values. That is a deliberate constraint, not tidiness — the properties that matter here (unknown never
//! removes a candidate, a constant dimension is never asked, skip and doesn't-matter never filter) are the ones most likely to be
//! quietly broken by a query change, and they are only testable without one.
//!
//! The engine's honesty rule is §2.7: **unknown is not no.** A candidate whose capability value nobody has asserted is KEPT and
//! demoted, never removed. A Finder that quietly dropped candidates for missing data would look identical to one that had found
//! the right answer, which is the failure this whole crate exists to prevent.

use std::collections::BTreeMap;

/// Answer modes. Three of the four do not filter at all, and the distinction between them is carried to the UI so the engine can
/// tell "do not ask me again" (doesn't matter) from "do not filter on this, ever" (skip).
pub const MODE_REQUIRED: &str = "required";
/// The user declined to answer.
pub const MODE_SKIP: &str = "skip";
/// The user does not care.
pub const MODE_DOESNT_MATTER: &str = "doesnt-matter";
/// The user will decide later: a preference, not a filter.
pub const MODE_DECIDE_LATER: &str = "decide-later";

/// The non-filtering option every question carries.
pub const MODE_ANY: &str = "any";

/// Dimension families, used for the diversity rule and for grouping the catalog in the API.
pub const FAMILY_CAPABILITY: &str = "capability";
/// Language and platform.
pub const FAMILY_PLATFORM: &str = "platform";
/// Who decides.
pub const FAMILY_GOVERNANCE: &str = "governance";
/// Licence terms.
pub const FAMILY_LICENSE: &str = "license";
/// How far along a project is.
pub const FAMILY_MATURITY: &str = "maturity";
/// How much of an evening a piece of work costs (§CF).
pub const FAMILY_EFFORT: &str = "effort";
/// Whether somebody can help.
pub const FAMILY_SUPPORT: &str = "support";

/// The entropy below which a question is not worth asking: 0.35 bits is roughly a 78/22 split. Below that the answer excludes
/// too few candidates to be worth the user's attention, so a near-constant dimension is refused by arithmetic rather than by a
/// maintainer's list of forbidden questions.
pub const MIN_GAIN_BITS: f64 = 0.35;

/// §2.8: "a hard cap of ~10".
pub const MAX_QUESTIONS: i32 = 10;

/// At or below this many candidates, stop and show them.
pub const SATURATION_COUNT: usize = 5;

/// The absolute fit a top candidate needs before "we've narrowed it down" is offered.
pub const HIGH_CONFIDENCE: f64 = 0.85;

/// And the margin it must hold over the runner-up. 1.15 rather than 1.0 because two candidates within 15% are not a finding,
/// they are a pair.
pub const CONFIDENCE_MARGIN: f64 = 1.15;

/// Applied per unknown field to a candidate's fit, scaled by coverage.
///
/// It demotes rather than removes, and it is small enough that a strongly matching candidate with one gap outranks a weak match
/// with none: absence of data is weaker evidence than presence of it, but it is not evidence of absence.
pub const UNKNOWN_PENALTY: f64 = 0.05;

/// Weight of the capability term.
pub const WEIGHT_CAPABILITY: f64 = 0.35;
/// Weight of the platform/language term.
pub const WEIGHT_PLATFORM: f64 = 0.15;
/// Weight of the governance term.
pub const WEIGHT_GOVERNANCE: f64 = 0.05;
/// Weight of the field-report term.
pub const WEIGHT_FIELD_REPORT: f64 = 0.25;
/// Weight of the arena term.
pub const WEIGHT_ARENA: f64 = 0.20;

/// The bucket for a dimension nobody has a value for.
pub const UNKNOWN_VALUE: &str = "__unknown__";

/// The literal the capability store writes when somebody has said "nobody knows".
pub const STORE_UNKNOWN_WORD: &str = "unknown";

/// Stop reasons.
pub const STOP_SATURATED: &str = "SATURATED";
/// One candidate is clear of the rest.
pub const STOP_HIGH_CONFIDENCE: &str = "HIGH_CONFIDENCE";
/// Nothing left to ask.
pub const STOP_LOW_GAIN: &str = "LOW_GAIN";
/// §2.8's cap.
pub const STOP_QUESTION_CAP: &str = "QUESTION_CAP";
/// Every candidate was filtered away.
pub const STOP_NO_MATCHES: &str = "NO_MATCHES";

/// The contribution feed's dimension keys. Plain dimension keys to the engine, so a contribution question gets the same
/// unknown-keeps and tie handling as any other — no special case in [`apply`] or [`score`].
pub const DIM_MINUTES: &str = "minutes";
/// Whether somebody can help with the task.
pub const DIM_MENTOR: &str = "mentor";
/// How much of a run-up the task needs.
pub const DIM_DIFFICULTY: &str = "difficulty";

/// One answer to a question, with the number of candidates it would remove. Impact is computed from the live set, never
/// estimated.
///
/// Named `AnswerOption` rather than `Option` because this crate's own type would shadow `std::option::Option` in every signature
/// below it — and the ASCII spelling the Go uses is not worth that.
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct AnswerOption {
    /// The value.
    pub id: String,
    /// What a person sees.
    pub label: String,
    /// How many candidates choosing this would rule out.
    pub impact: i64,
}

/// A question the engine can ask about the candidate set.
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct Dimension {
    /// `cap:wip-limits`, `language`, `governance`…
    pub key: String,
    /// One of the `FAMILY_*` constants.
    pub family: String,
    /// The human label.
    pub label: String,
    /// `single`.
    pub kind: String,
    /// The answers on offer, always ending in [`MODE_ANY`].
    pub options: Vec<AnswerOption>,
    /// The expected information gain of asking this dimension now.
    #[serde(rename = "gain_bits")]
    pub gain_bits: f64,
    /// How much of the candidate set has a real value here. A dimension that is 94% unknown produces mostly-unknown answers,
    /// which is why the selector deprioritises it even when its entropy looks healthy.
    #[serde(rename = "known_fraction")]
    pub known_fraction: f64,
}

/// One project the engine can rank.
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct Candidate {
    /// The project.
    pub project_id: i64,
    /// Its slug.
    pub slug: String,
    /// Its name.
    pub name: String,
    /// A dimension key to its value. A MISSING key means nobody has asserted it, which is distinct from a key present with the
    /// value `"unknown"` — the difference §3.1 exists to preserve.
    pub attrs: BTreeMap<String, String>,
    /// The reputation-weighted field-report outcome rate, 0..1.
    pub report: f64,
    /// The sample size behind `report`. Zero means "no reports", which is different from "reports say it did not work".
    pub reports: i64,
    /// The arena standing, normalised to 0..1. Zero when no arena entries exist.
    pub arena_r: f64,
}

impl Candidate {
    /// A candidate with the given identity and no attributes.
    #[must_use]
    pub fn new(project_id: i64, slug: &str, name: &str) -> Self {
        Self {
            project_id,
            slug: slug.to_string(),
            name: name.to_string(),
            attrs: BTreeMap::new(),
            report: 0.0,
            reports: 0,
            arena_r: 0.0,
        }
    }
}

/// One user response.
#[derive(Debug, Clone, PartialEq, serde::Deserialize, serde::Serialize)]
pub struct Answer {
    /// The dimension answered.
    pub dimension_key: String,
    /// The option chosen.
    pub option_id: String,
    /// One of the `MODE_*` constants.
    pub mode: String,
}

/// A scored candidate with the reasoning that produced the score.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Ranked {
    /// The candidate, flattened into this object the way the API serves it.
    #[serde(flatten)]
    pub candidate: Candidate,
    /// The fit score.
    pub fit: f64,
    /// Dimension to the value that matched.
    pub matches: BTreeMap<String, String>,
    /// Dimension to why it did not.
    pub warnings: BTreeMap<String, String>,
    /// Dimensions nobody has a value for on this candidate.
    pub unknown: Vec<String>,
    /// The terms of the score, so a rank can be explained without a second copy of the weights.
    pub explanation: BTreeMap<String, f64>,
}

/// The engine's input for one question.
#[derive(Debug, Clone, Default)]
pub struct State {
    /// The surviving candidates.
    pub candidates: Vec<Candidate>,
    /// Every answer so far, in order.
    pub answers: Vec<Answer>,
    /// Which dimensions have been asked.
    pub asked: Vec<String>,
    /// How many required answers were given, for the question cap.
    pub depth: i32,
    /// The family of the previous question, for the diversity rule.
    pub last_family: String,
}

impl State {
    /// Whether a dimension has been asked.
    #[must_use]
    pub fn has_asked(&self, key: &str) -> bool {
        self.asked.iter().any(|k| k == key)
    }
}

/// Why no question can be asked. Not a failure: the engine saying the catalog cannot narrow this set further, and the caller
/// should show the gaps instead.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct NoQuestion;

impl std::fmt::Display for NoQuestion {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("no question worth asking")
    }
}

impl std::error::Error for NoQuestion {}

/// Whether a value counts as unknown. All four ways of being unusable collapse into one bucket: an absent attribute, a blank
/// value, the explicit [`UNKNOWN_VALUE`] sentinel, and the literal `"unknown"` the capability store records when somebody has
/// said "nobody knows".
#[must_use]
pub fn is_unknown(value: Option<&String>) -> bool {
    match value {
        None => true,
        Some(v) => {
            let t = v.trim();
            t.is_empty() || v == UNKNOWN_VALUE || v == STORE_UNKNOWN_WORD
        }
    }
}

/// The Shannon entropy of a value distribution, in bits. A single distinct value has 0 bits, which is the whole mechanism
/// behind "never ask a question the answer cannot change".
#[must_use]
pub fn gain(counts: &[i64]) -> f64 {
    let total: i64 = counts.iter().filter(|c| **c >= 0).sum();
    if total <= 1 {
        return 0.0;
    }
    let mut h = 0.0;
    for c in counts {
        if *c <= 0 {
            continue;
        }
        #[allow(clippy::cast_precision_loss)]
        let p = *c as f64 / total as f64;
        h -= p * p.log2();
    }
    h
}

/// Tallies a dimension's values across the candidate set. A candidate with no value at all is tallied under [`UNKNOWN_VALUE`],
/// so the distribution includes what is missing rather than quietly shrinking the denominator — otherwise a dimension asserted
/// on 2 of 70 candidates would look like a clean 50/50 split.
#[must_use]
pub fn counts_by_value(candidates: &[Candidate], key: &str) -> (BTreeMap<String, i64>, i64) {
    let mut counts: BTreeMap<String, i64> = BTreeMap::new();
    let mut known = 0i64;
    for candidate in candidates {
        let value = candidate.attrs.get(key);
        if is_unknown(value) {
            *counts.entry(UNKNOWN_VALUE.to_string()).or_insert(0) += 1;
            continue;
        }
        *counts.entry(value.cloned().unwrap_or_default()).or_insert(0) += 1;
        known += 1;
    }
    (counts, known)
}

/// Derives a question from one dimension's observed distribution.
///
/// Options are only the values actually present in the candidate set, plus `any` (§5.4: "Finder never asks about an option the
/// catalog can't actually deliver"). Offering a value no candidate has would be a question whose every answer is equivalent.
#[must_use]
pub fn build_dimension(
    key: &str,
    family: &str,
    label: &str,
    counts: &BTreeMap<String, i64>,
    known: i64,
    total: usize,
) -> Dimension {
    let values: Vec<i64> = counts.values().copied().collect();
    let mut dimension = Dimension {
        key: key.to_string(),
        family: family.to_string(),
        label: label.to_string(),
        kind: "single".to_string(),
        options: Vec::new(),
        gain_bits: gain(&values),
        known_fraction: 0.0,
    };
    if total > 0 {
        #[allow(clippy::cast_precision_loss)]
        {
            dimension.known_fraction = known as f64 / total as f64;
        }
    }
    // Sorted for a stable question: two identical candidate sets must produce byte-identical questions, or a session resumed
    // from a share URL reorders its own options. `BTreeMap` iterates in key order, which is the sort.
    let total_i64 = i64::try_from(total).unwrap_or(i64::MAX);
    for (value, count) in counts {
        if value == UNKNOWN_VALUE {
            continue; // an unknown is not an answer a user can pick
        }
        dimension.options.push(AnswerOption {
            id: value.clone(),
            label: value.clone(),
            impact: total_i64 - count,
        });
    }
    dimension.options.push(AnswerOption {
        id: MODE_ANY.to_string(),
        label: "Doesn't matter".to_string(),
        impact: 0,
    });
    dimension
}

/// Every dimension key any candidate carries, in a stable order.
///
/// Derived from the data rather than from a list of known dimensions, which is §2.2: "Questions are selected dynamically from the
/// data, not from a hardcoded script. If the catalog changes, the questions change."
#[must_use]
pub fn candidate_dimension_keys(candidates: &[Candidate]) -> Vec<String> {
    let mut seen: BTreeMap<&str, ()> = BTreeMap::new();
    for candidate in candidates {
        for key in candidate.attrs.keys() {
            seen.insert(key.as_str(), ());
        }
    }
    seen.into_keys().map(str::to_string).collect()
}

/// Assigns a family and a human label to a dimension key. The prefix is the contract: `cap:` is a capability from the matrix,
/// everything else is a project attribute, and a new capability needs no change here.
#[must_use]
pub fn classify(key: &str) -> (String, String) {
    if let Some(bare) = key.strip_prefix("cap:") {
        return (
            FAMILY_CAPABILITY.to_string(),
            bare.replace('-', " "),
        );
    }
    match key {
        "language" => (FAMILY_PLATFORM.to_string(), "Programming language".to_string()),
        DIM_MINUTES | DIM_DIFFICULTY => (
            FAMILY_EFFORT.to_string(),
            "How much time it takes".to_string(),
        ),
        DIM_MENTOR => (
            FAMILY_SUPPORT.to_string(),
            "Whether someone can help".to_string(),
        ),
        "governance" => (FAMILY_GOVERNANCE.to_string(), "How is it governed?".to_string()),
        "license" => (FAMILY_LICENSE.to_string(), "License".to_string()),
        "maturity" => (FAMILY_MATURITY.to_string(), "Maturity".to_string()),
        other => (FAMILY_CAPABILITY.to_string(), other.to_string()),
    }
}

/// Picks the dimension whose answer most reduces uncertainty, or [`NoQuestion`] when nothing clears [`MIN_GAIN_BITS`].
///
/// The score is expected information gain adjusted by three things plain entropy does not know: **catalog confidence** (a
/// dimension most of the set has no value for produces mostly-unknown answers, so it is scaled down), **question quality**
/// (small-cardinality questions early), and **diversity** (two consecutive questions from one family read as a quiz).
///
/// # Errors
/// [`NoQuestion`] when the set is empty or nothing is askable.
pub fn next_question(state: &State) -> Result<(Dimension, f64), NoQuestion> {
    if state.candidates.is_empty() {
        return Err(NoQuestion);
    }
    let mut best: Option<Dimension> = None;
    let mut best_score = 0.0f64;
    for key in candidate_dimension_keys(&state.candidates) {
        if state.has_asked(&key) {
            continue;
        }
        let (family, label) = classify(&key);
        let (counts, known) = counts_by_value(&state.candidates, &key);
        let dimension = build_dimension(&key, &family, &label, &counts, known, state.candidates.len());
        if dimension.gain_bits < MIN_GAIN_BITS {
            continue;
        }
        let mut score = dimension.gain_bits;
        // Never zero, so a fully-unknown dimension cannot outrank a real one even when its entropy looks high.
        score *= 0.5 + 0.5 * dimension.known_fraction;
        if dimension.known_fraction > 0.0 && dimension.options.len() > 4 && state.depth < 3 {
            score *= 0.9;
        }
        if !state.last_family.is_empty() && family == state.last_family {
            score *= 0.7;
        }
        if score > best_score {
            best_score = score;
            best = Some(dimension);
        }
    }
    match best {
        Some(dimension) => {
            let gain_bits = dimension.gain_bits;
            Ok((dimension, gain_bits))
        }
        None => Err(NoQuestion),
    }
}

/// Every dimension derivable from the set, with its live gain — including the ones too weak to ask about. This is what the
/// catalog endpoint shows, and it exists because §5.4's promise is otherwise only checkable by driving the whole UI.
#[must_use]
pub fn all_dimensions(state: &State) -> Vec<Dimension> {
    let mut out = Vec::new();
    if state.candidates.is_empty() {
        return out;
    }
    for key in candidate_dimension_keys(&state.candidates) {
        if state.has_asked(&key) {
            continue;
        }
        let (family, label) = classify(&key);
        let (counts, known) = counts_by_value(&state.candidates, &key);
        out.push(build_dimension(
            &key,
            &family,
            &label,
            &counts,
            known,
            state.candidates.len(),
        ));
    }
    out
}

/// Whether to stop asking, and why. Both a high score AND a margin over the runner-up are required for
/// [`STOP_HIGH_CONFIDENCE`]: a top candidate at 0.99 with #2 at 0.98 has not been found, it has been tied.
#[must_use]
pub fn should_stop(state: &State) -> (bool, String) {
    if state.candidates.is_empty() {
        return (true, STOP_NO_MATCHES.to_string());
    }
    if state.candidates.len() <= SATURATION_COUNT {
        return (true, STOP_SATURATED.to_string());
    }
    if state.depth >= MAX_QUESTIONS {
        return (true, STOP_QUESTION_CAP.to_string());
    }
    let ranked = score(&state.candidates, &state.answers);
    if ranked.len() >= 2 {
        let (top, second) = (ranked[0].fit, ranked[1].fit);
        if top >= HIGH_CONFIDENCE && second > 0.0 && top / second >= CONFIDENCE_MARGIN {
            return (true, STOP_HIGH_CONFIDENCE.to_string());
        }
    }
    if next_question(state).is_err() {
        return (true, STOP_LOW_GAIN.to_string());
    }
    (false, String::new())
}

/// A dimension that WOULD have discriminated the set but cannot, because nobody has filled in the data.
///
/// This is the honest end of a session: a Finder that stops with "here are your results" would be claiming a precision it does
/// not have. A gap says: these are the questions we cannot ask, and each one is a contribution someone could make.
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct Gap {
    /// The dimension.
    pub key: String,
    /// Its human label.
    pub label: String,
    /// Its family.
    pub family: String,
    /// How many candidates have no value for it.
    #[serde(rename = "unknown_fields")]
    pub unknown_fields: i64,
    /// How many candidates filling it would unblock — the same number, because the unknown value is exactly what holds the
    /// question back.
    #[serde(rename = "held_back")]
    pub held_back: i64,
}

/// The dimensions nobody has data for, most useful first (then by key, so the list is stable).
#[must_use]
pub fn gaps(state: &State) -> Vec<Gap> {
    if state.candidates.is_empty() {
        return Vec::new();
    }
    let mut out = Vec::new();
    for key in candidate_dimension_keys(&state.candidates) {
        if state.has_asked(&key) {
            continue;
        }
        let (counts, _) = counts_by_value(&state.candidates, &key);
        // A gap is UNKNOWN DATA, not "cannot be asked about". The two are independent: a dimension can split perfectly and
        // still have 60% of its candidates unknown, and those 60% are exactly what §5.3 wants contributed.
        let unknown = counts.get(UNKNOWN_VALUE).copied().unwrap_or(0);
        if unknown == 0 {
            continue;
        }
        let (family, label) = classify(&key);
        out.push(Gap {
            key,
            label,
            family,
            unknown_fields: unknown,
            held_back: unknown,
        });
    }
    out.sort_by(|a, b| {
        b.held_back
            .cmp(&a.held_back)
            .then_with(|| a.key.cmp(&b.key))
    });
    out
}

/// Narrows a candidate set by an answer, returning the survivors and how many were removed per dimension key.
///
/// The unknown rule lives here and nowhere else: a required answer removes a candidate whose value is a different value, and
/// KEEPS one whose value is absent or unknown. A candidate kept for want of data is not a lesser answer to the question, it is
/// an unanswered one.
#[must_use]
pub fn apply(candidates: &[Candidate], answer: &Answer) -> (Vec<Candidate>, BTreeMap<String, i64>) {
    let mut removed_by: BTreeMap<String, i64> = BTreeMap::new();
    // Every non-filtering mode returns the set untouched — §2.4: "Skipping never removes candidates."
    match answer.mode.as_str() {
        MODE_SKIP | MODE_DOESNT_MATTER | MODE_DECIDE_LATER | "" => {
            return (candidates.to_vec(), removed_by);
        }
        _ => {}
    }
    let mut kept = Vec::with_capacity(candidates.len());
    for candidate in candidates {
        let value = candidate.attrs.get(&answer.dimension_key);
        if answer.option_id == MODE_ANY {
            kept.push(candidate.clone());
        } else if is_unknown(value) {
            kept.push(candidate.clone()); // unknown keeps, §2.7
        } else if value.cloned().unwrap_or_default() == answer.option_id {
            kept.push(candidate.clone());
        } else {
            *removed_by.entry(answer.dimension_key.clone()).or_insert(0) += 1;
        }
    }
    (kept, removed_by)
}

/// Ranks candidates. It never removes anything.
///
/// A weighted sum over §5.2's terms, each normalised to 0..1 and each reported in `explanation` so a reader can see which term
/// produced a rank rather than being handed a number. The sum is **renormalised over available evidence**: missing evidence
/// leaves both the numerator and the denominator, so absence cannot drag a candidate down, and how thin the evidence was is
/// reported separately as coverage.
#[must_use]
pub fn score(candidates: &[Candidate], answers: &[Answer]) -> Vec<Ranked> {
    let mut required: BTreeMap<String, String> = BTreeMap::new();
    let mut preferred: BTreeMap<String, String> = BTreeMap::new();
    for answer in answers {
        match answer.mode.as_str() {
            MODE_REQUIRED => {
                required.insert(answer.dimension_key.clone(), answer.option_id.clone());
            }
            MODE_DECIDE_LATER | MODE_ANY => {
                preferred.insert(answer.dimension_key.clone(), answer.option_id.clone());
            }
            _ => {}
        }
    }

    // A decide-later answer still counts as a weak match: it is a real signal about the candidate, just not one the user asked
    // to filter on.
    let mut cap_keys: Vec<String> = Vec::new();
    for key in required.keys() {
        if key.starts_with("cap:") {
            cap_keys.push(key.clone());
        }
    }
    for key in preferred.keys() {
        if key.starts_with("cap:") && !cap_keys.contains(key) {
            cap_keys.push(key.clone());
        }
    }
    cap_keys.sort();

    let mut out: Vec<Ranked> = Vec::with_capacity(candidates.len());
    for candidate in candidates {
        let mut matches: BTreeMap<String, String> = BTreeMap::new();
        let mut warnings: BTreeMap<String, String> = BTreeMap::new();
        let mut unknown: Vec<String> = Vec::new();
        let mut explanation: BTreeMap<String, f64> = BTreeMap::new();

        // ── capability term ──
        let mut cap_total = 0.0f64;
        let mut cap_hit = 0.0f64;
        for key in &cap_keys {
            let value = candidate.attrs.get(key);
            // `cap_total` counts what the user ASKED, not what this candidate happens to have: an unknown field still
            // increments it, or a candidate with no capability data at all would lose the weight that carries its penalty.
            cap_total += 1.0;
            if is_unknown(value) {
                unknown.push(key.clone());
                continue;
            }
            let value = value.cloned().unwrap_or_default();
            if let Some(want) = required.get(key) {
                if value == *want {
                    cap_hit += 1.0;
                    matches.insert(key.clone(), value);
                } else if value == "no" {
                    // A positive "no" against a requirement is a miss, not a partial: half credit here would rank a project
                    // that explicitly lacks the thing above one that was never asked.
                    warnings.insert(key.clone(), "does not have it".to_string());
                } else {
                    // Present but a different value (partial where yes was wanted, or the reverse). Partial credit.
                    cap_hit += 0.5;
                    matches.insert(key.clone(), value);
                }
                continue;
            }
            // A decide-later preference. Matching it scores; not matching it does not — crediting every known value would
            // make a soft preference a flat bonus that reorders nothing.
            if let Some(want) = preferred.get(key) {
                if want != MODE_ANY {
                    if value == *want {
                        cap_hit += 1.0;
                    }
                    matches.insert(key.clone(), value);
                    continue;
                }
            }
            // "Doesn't matter" on a preference: having the value is mildly good, having nothing recorded is mildly bad.
            cap_hit += 0.5;
            matches.insert(key.clone(), value);
        }
        let cap_score = if cap_total > 0.0 {
            cap_hit / cap_total
        } else {
            0.0
        };
        explanation.insert("capability".to_string(), cap_score);

        // ── platform / governance / license / maturity term ──
        let mut struct_score = 0.0f64;
        let mut struct_total = 0i64;
        for key in ["language", "governance", "license", "maturity"] {
            let Some(want) = required.get(key) else {
                continue;
            };
            let value = candidate.attrs.get(key);
            // Same rule as cap_total: the denominator is what was asked.
            struct_total += 1;
            if is_unknown(value) {
                unknown.push(key.to_string());
                continue;
            }
            let value = value.cloned().unwrap_or_default();
            if value == *want {
                struct_score += 1.0;
                matches.insert(key.to_string(), value);
            } else {
                warnings.insert(key.to_string(), value);
            }
        }
        #[allow(clippy::cast_precision_loss)]
        let platform = if struct_total > 0 {
            struct_score / struct_total as f64
        } else {
            0.0
        };
        explanation.insert("platform".to_string(), platform);

        // Governance is a real but weak signal, so it has its own small weight rather than being folded into the platform term.
        let mut gov = 0.0f64;
        if required.contains_key("governance") {
            if let Some(value) = candidate.attrs.get("governance") {
                if !value.trim().is_empty() {
                    let want = required.get("governance").cloned().unwrap_or_default();
                    if *value == want {
                        gov = 1.0;
                        matches.insert("governance".to_string(), value.clone());
                    } else {
                        gov = 0.0;
                        warnings.insert("governance".to_string(), value.clone());
                    }
                }
            }
        }
        explanation.insert("governance".to_string(), gov);

        // Field report rate — zero reports is not zero quality, so it is EXCLUDED from the sum rather than entering as a zero.
        let report = candidate.report;
        let have_report = candidate.reports > 0;
        if !have_report {
            unknown.push("field-reports".to_string());
        }
        explanation.insert("field_report".to_string(), report);

        // Arena standing — same treatment: no arena is absence of evidence, not a score of zero.
        let have_arena = candidate.arena_r > 0.0;
        explanation.insert("arena".to_string(), candidate.arena_r);

        let mut num = 0.0f64;
        let mut den = 0.0f64;
        if cap_total > 0.0 {
            num += WEIGHT_CAPABILITY * cap_score;
            den += WEIGHT_CAPABILITY;
        }
        if struct_total > 0 {
            num += WEIGHT_PLATFORM * platform;
            den += WEIGHT_PLATFORM;
        }
        if required.contains_key("governance") {
            if let Some(value) = candidate.attrs.get("governance") {
                if !value.trim().is_empty() {
                    num += WEIGHT_GOVERNANCE * gov;
                    den += WEIGHT_GOVERNANCE;
                }
            }
        }
        if have_report {
            num += WEIGHT_FIELD_REPORT * report;
            den += WEIGHT_FIELD_REPORT;
        }
        if have_arena {
            num += WEIGHT_ARENA * candidate.arena_r;
            den += WEIGHT_ARENA;
        }
        // No evidence at all cannot produce a number; 0 is the only honest value.
        let mut fit = if den > 0.0 { num / den } else { 0.0 };

        // Soft preference bonus for the structured dimensions a user deferred. Capability preferences are scored inside the
        // capability term, so they are NOT counted again here — a double-count is how a nice-to-have quietly becomes worth more
        // than a requirement.
        let mut bonus = 0.0f64;
        for (key, want) in &preferred {
            if key.starts_with("cap:") || want == MODE_ANY {
                continue;
            }
            match candidate.attrs.get(key) {
                Some(value) if !value.trim().is_empty() => {
                    if value == want {
                        bonus += 0.05;
                    }
                    matches.insert(key.clone(), value.clone());
                }
                _ => unknown.push(key.clone()),
            }
        }
        explanation.insert("soft_preference".to_string(), bonus);

        // Coverage: the share of total weight that had evidence behind it — "100% of what we know, and we know 36% of it" is a
        // different statement from "36% fit".
        let total_weight = WEIGHT_CAPABILITY
            + WEIGHT_PLATFORM
            + WEIGHT_GOVERNANCE
            + WEIGHT_FIELD_REPORT
            + WEIGHT_ARENA;
        let coverage = if total_weight > 0.0 { den / total_weight } else { 0.0 };
        explanation.insert("evidence_coverage".to_string(), coverage);

        // Unknown penalty: demote, never drop, and scaled BY COVERAGE — the renormalised sum has already priced missing
        // evidence, so a flat penalty would charge for the same gap twice.
        #[allow(clippy::cast_precision_loss)]
        let penalty = -UNKNOWN_PENALTY * unknown.len() as f64 * coverage;
        explanation.insert("unknown_penalty".to_string(), penalty);

        fit += bonus;
        fit += penalty;
        if fit < 0.0 {
            fit = 0.0;
        }

        out.push(Ranked {
            candidate: candidate.clone(),
            fit,
            matches,
            warnings,
            unknown,
            explanation,
        });
    }

    out.sort_by(|a, b| {
        b.fit
            .partial_cmp(&a.fit)
            .unwrap_or(std::cmp::Ordering::Equal)
            // Deterministic tiebreak, so a shared results URL renders identically for everyone.
            .then_with(|| a.candidate.slug.cmp(&b.candidate.slug))
            .then_with(|| a.candidate.project_id.cmp(&b.candidate.project_id))
    });
    out
}

/// The weights, for the "why this rank?" panel — so the client does not hold a second copy that can drift.
#[must_use]
pub fn weight_report() -> BTreeMap<String, f64> {
    let mut out = BTreeMap::new();
    out.insert("capability".to_string(), WEIGHT_CAPABILITY);
    out.insert("platform".to_string(), WEIGHT_PLATFORM);
    out.insert("governance".to_string(), WEIGHT_GOVERNANCE);
    out.insert("field_report".to_string(), WEIGHT_FIELD_REPORT);
    out.insert("arena".to_string(), WEIGHT_ARENA);
    out.insert("unknown_penalty".to_string(), UNKNOWN_PENALTY);
    out
}

// ── the contribution feed (§CF-2), same engine, different vocabulary ──────────────────────────────────────────────────────────

/// The bucket values the feed's time question offers.
pub const MINUTES_15: &str = "15";
/// Up to an hour.
pub const MINUTES_60: &str = "60";
/// Most of a day.
pub const MINUTES_480: &str = "480";

/// The feed's declared question set, in the order the page offers it. It exists so the page, the API and this module cannot
/// drift apart about the vocabulary.
#[must_use]
pub fn contribute_questions() -> Vec<Dimension> {
    vec![
        Dimension {
            key: "language".to_string(),
            family: FAMILY_PLATFORM.to_string(),
            label: "What do you write?".to_string(),
            kind: "single".to_string(),
            // Options are filled from the live candidate set by the handler: a hard-coded language list would offer answers no
            // project in this instance can satisfy.
            options: vec![AnswerOption {
                id: MODE_ANY.to_string(),
                label: "Any language".to_string(),
                impact: 0,
            }],
            gain_bits: 0.0,
            known_fraction: 0.0,
        },
        Dimension {
            key: DIM_MINUTES.to_string(),
            family: FAMILY_EFFORT.to_string(),
            label: "How long do you have?".to_string(),
            kind: "single".to_string(),
            options: vec![
                AnswerOption {
                    id: MINUTES_15.to_string(),
                    label: "About fifteen minutes".to_string(),
                    impact: 0,
                },
                AnswerOption {
                    id: MINUTES_60.to_string(),
                    label: "Up to an hour".to_string(),
                    impact: 0,
                },
                AnswerOption {
                    id: MINUTES_480.to_string(),
                    label: "Most of a day".to_string(),
                    impact: 0,
                },
                AnswerOption {
                    id: MODE_ANY.to_string(),
                    label: "Any length".to_string(),
                    impact: 0,
                },
            ],
            gain_bits: 0.0,
            known_fraction: 0.0,
        },
        Dimension {
            key: DIM_DIFFICULTY.to_string(),
            family: FAMILY_EFFORT.to_string(),
            label: "How much of a run-up do you want?".to_string(),
            kind: "single".to_string(),
            options: vec![
                AnswerOption {
                    id: "easy".to_string(),
                    label: "Something gentle".to_string(),
                    impact: 0,
                },
                AnswerOption {
                    id: "medium".to_string(),
                    label: "A real puzzle".to_string(),
                    impact: 0,
                },
                AnswerOption {
                    id: "hard".to_string(),
                    label: "Throw me in".to_string(),
                    impact: 0,
                },
                AnswerOption {
                    id: MODE_ANY.to_string(),
                    label: "Any difficulty".to_string(),
                    impact: 0,
                },
            ],
            gain_bits: 0.0,
            known_fraction: 0.0,
        },
        Dimension {
            key: DIM_MENTOR.to_string(),
            family: FAMILY_SUPPORT.to_string(),
            label: "Do you want someone to ask?".to_string(),
            kind: "single".to_string(),
            options: vec![
                AnswerOption {
                    id: "yes".to_string(),
                    label: "Yes — I'd rather not be alone".to_string(),
                    impact: 0,
                },
                AnswerOption {
                    id: "no".to_string(),
                    label: "No, I'll find my way".to_string(),
                    impact: 0,
                },
                AnswerOption {
                    id: MODE_ANY.to_string(),
                    label: "Either".to_string(),
                    impact: 0,
                },
            ],
            gain_bits: 0.0,
            known_fraction: 0.0,
        },
    ]
}

/// Collapses a stated budget onto the answer set. `None` or a non-positive budget returns `""`, which callers must NOT write
/// into attributes: a PRESENT key with a guess would silently exclude the item from every other answer.
#[must_use]
pub fn minutes_bucket(minutes: Option<i64>) -> &'static str {
    match minutes {
        None => "",
        Some(m) if m <= 0 => "",
        Some(m) if m <= 15 => MINUTES_15,
        Some(m) if m <= 60 => MINUTES_60,
        Some(_) => MINUTES_480,
    }
}

/// Translates the store's shorthand onto the feed's vocabulary. The label column holds S/M/L — maintainers' shorthand — and a
/// newcomer looking for their first task should not have to learn that "S" means "gentle".
///
/// An unrecognised value is passed through lowercased rather than mapped to something plausible: a filter that then excludes it
/// is correct, and the unfiltered feed still shows it. Guessing "medium" would put a newcomer on work sized wrong for them.
#[must_use]
pub fn difficulty_word(stored: &str) -> String {
    let trimmed = stored.trim();
    match trimmed.to_uppercase().as_str() {
        "S" => "easy".to_string(),
        "M" => "medium".to_string(),
        "L" => "hard".to_string(),
        "" => String::new(),
        _ => trimmed.to_lowercase(),
    }
}

/// The identity [`score`] echoes back for one work item.
#[must_use]
pub fn item_key(project_slug: &str, item_id: i64) -> String {
    format!("{project_slug}#{item_id}")
}

/// Reverses [`item_key`]. `None` rather than a zero for anything it did not build, because a caller that guessed here would
/// render somebody else's task.
#[must_use]
pub fn item_id_from_key(key: &str) -> Option<(String, i64)> {
    let index = key.rfind('#')?;
    // An empty slug would render a link to nothing, and the only way to produce one is a key this module did not build.
    if index == 0 || index == key.len() - 1 {
        return None;
    }
    let id = key[index + 1..].parse::<i64>().ok()?;
    Some((key[..index].to_string(), id))
}

/// Builds the engine's candidate for one work item.
///
/// The item inherits the PROJECT's dimensions — a task in a project written in Go is Go work — and overrides only its own. The
/// slug carries `project#item` because [`score`] returns ranked values and the caller has to map each one back to its row.
#[must_use]
pub fn work_item_candidate(
    project_id: i64,
    project_slug: &str,
    title: &str,
    item_id: i64,
    minutes: Option<i64>,
    needs_mentor: bool,
    difficulty: &str,
    inherited: &BTreeMap<String, String>,
) -> Candidate {
    let mut candidate = Candidate::new(project_id, &item_key(project_slug, item_id), title);
    candidate.attrs = inherited.clone();
    let bucket = minutes_bucket(minutes);
    if !bucket.is_empty() {
        candidate.attrs.insert(DIM_MINUTES.to_string(), bucket.to_string());
    }
    let word = difficulty_word(difficulty);
    if !word.is_empty() {
        candidate.attrs.insert(DIM_DIFFICULTY.to_string(), word);
    }
    // `needs_mentor` is a fact either way: false is "nobody is attached", a real answer, and must not be confused with the
    // missing attribute the engine keeps as unknown.
    candidate.attrs.insert(
        DIM_MENTOR.to_string(),
        if needs_mentor { "yes" } else { "no" }.to_string(),
    );
    candidate
}

/// The feed's FINAL tiebreak after the engine has ranked: fit desc, then project health desc with unmeasured last, then the
/// newer item first. The health term is what makes this a contribution FEED rather than a task list — two equally-fitting tasks
/// are not equal if one is in a project whose last commit was in 2023, and an unmeasured project must never outrank a measured
/// one on the strength of having no data.
pub fn order_work_items(items: &mut [Ranked], health: &BTreeMap<i64, Option<f64>>) {
    items.sort_by(|a, b| {
        b.fit
            .partial_cmp(&a.fit)
            .unwrap_or(std::cmp::Ordering::Equal)
            .then_with(|| {
                let ha = health.get(&a.candidate.project_id).copied().flatten();
                let hb = health.get(&b.candidate.project_id).copied().flatten();
                match (ha, hb) {
                    (None, None) => std::cmp::Ordering::Equal,
                    (None, Some(_)) => std::cmp::Ordering::Greater,
                    (Some(_), None) => std::cmp::Ordering::Less,
                    (Some(x), Some(y)) => y.partial_cmp(&x).unwrap_or(std::cmp::Ordering::Equal),
                }
            })
            // Newer item first: the higher id is the later row.
            .then_with(|| b.candidate.slug.cmp(&a.candidate.slug))
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    fn candidate(slug: &str, attrs: &[(&str, &str)]) -> Candidate {
        let mut c = Candidate::new(1, slug, slug);
        for (k, v) in attrs {
            c.attrs.insert((*k).to_string(), (*v).to_string());
        }
        c
    }

    #[test]
    fn a_constant_dimension_is_never_asked() {
        let set = vec![
            candidate("a", &[("language", "rust")]),
            candidate("b", &[("language", "rust")]),
            candidate("c", &[("language", "rust")]),
        ];
        let state = State {
            candidates: set,
            ..State::default()
        };
        let (counts, _) = counts_by_value(&state.candidates, "language");
        assert_eq!(
            gain(&counts.values().copied().collect::<Vec<_>>()),
            0.0,
            "one distinct value has no entropy"
        );
        assert!(next_question(&state).is_err(), "a constant dimension is not askable");
    }

    #[test]
    fn unknown_is_not_no() {
        let set = vec![
            candidate("has", &[("cap:x", "yes")]),
            candidate("lacks", &[("cap:x", "no")]),
            candidate("unknown", &[]),
        ];
        let answer = Answer {
            dimension_key: "cap:x".to_string(),
            option_id: "yes".to_string(),
            mode: MODE_REQUIRED.to_string(),
        };
        let (kept, removed) = apply(&set, &answer);
        assert_eq!(
            kept.iter().map(|c| c.slug.as_str()).collect::<Vec<_>>(),
            vec!["has", "unknown"],
            "a candidate kept for want of data is unanswered, not wrong"
        );
        assert_eq!(removed.get("cap:x"), Some(&1));
    }

    #[test]
    fn skipping_never_removes() {
        let set = vec![candidate("a", &[("language", "go")]), candidate("b", &[])]; 
        for mode in [MODE_SKIP, MODE_DOESNT_MATTER, MODE_DECIDE_LATER, ""] {
            let (kept, removed) = apply(
                &set,
                &Answer {
                    dimension_key: "language".to_string(),
                    option_id: "rust".to_string(),
                    mode: mode.to_string(),
                },
            );
            assert_eq!(kept.len(), 2, "{mode} must not filter");
            assert!(removed.is_empty());
        }
    }

    #[test]
    fn a_candidate_matching_every_answer_scores_one() {
        // Two terms have no evidence (reports, arena): they must leave the denominator, or a perfect match reads as poor.
        let set = vec![candidate("perfect", &[("language", "rust"), ("cap:x", "yes")])];
        let answers = vec![
            Answer {
                dimension_key: "language".to_string(),
                option_id: "rust".to_string(),
                mode: MODE_REQUIRED.to_string(),
            },
            Answer {
                dimension_key: "cap:x".to_string(),
                option_id: "yes".to_string(),
                mode: MODE_REQUIRED.to_string(),
            },
        ];
        let ranked = score(&set, &answers);
        let fit = ranked[0].fit;
        // Coverage is den / total: only the capability and platform weights had evidence, so 0.35 + 0.15 over 1.0.
        let coverage = 0.5;
        // One unknown field ("field-reports"; no arena entry is absence, not an unknown field of its own).
        assert!(
            (fit - (1.0 - UNKNOWN_PENALTY * 1.0 * coverage)).abs() < 1e-9,
            "fit was {fit}: only the coverage-scaled unknown penalty may come off a full match"
        );
        assert!((ranked[0].explanation["evidence_coverage"] - coverage).abs() < 1e-9);
    }

    #[test]
    fn a_positive_no_is_not_half_credit() {
        let set = vec![
            candidate("has", &[("cap:x", "yes")]),
            candidate("lacks", &[("cap:x", "no")]),
            candidate("partial", &[("cap:x", "partial")]),
        ];
        let answers = vec![Answer {
            dimension_key: "cap:x".to_string(),
            option_id: "yes".to_string(),
            mode: MODE_REQUIRED.to_string(),
        }];
        let ranked = score(&set, &answers);
        assert_eq!(ranked[0].candidate.slug, "has");
        let lacks = ranked.iter().find(|r| r.candidate.slug == "lacks").unwrap();
        let partial = ranked.iter().find(|r| r.candidate.slug == "partial").unwrap();
        assert!(partial.fit > lacks.fit, "partial outranks a positive no");
        assert_eq!(lacks.explanation["capability"], 0.0);
        assert_eq!(partial.explanation["capability"], 0.5);
    }

    #[test]
    fn a_decide_later_preference_reorders() {
        let set = vec![
            candidate("offline", &[("cap:x", "yes"), ("license", "mit")]),
            candidate("online", &[("cap:x", "yes"), ("license", "gpl")]),
        ];
        let base = vec![Answer {
            dimension_key: "cap:x".to_string(),
            option_id: "yes".to_string(),
            mode: MODE_REQUIRED.to_string(),
        }];
        let ranked = score(&set, &base);
        assert_eq!(ranked[0].candidate.slug, "offline", "slug breaks the tie");
        let mut with_preference = base.clone();
        with_preference.push(Answer {
            dimension_key: "license".to_string(),
            option_id: "gpl".to_string(),
            mode: MODE_DECIDE_LATER.to_string(),
        });
        let ranked = score(&set, &with_preference);
        assert_eq!(
            ranked[0].candidate.slug, "online",
            "a stated preference must actually move the ranking"
        );
    }

    #[test]
    fn the_stop_rule_needs_a_margin() {
        let tied = vec![
            candidate("a", &[("cap:x", "yes")]),
            candidate("b", &[("cap:x", "yes")]),
            candidate("c", &[("cap:x", "yes")]),
            candidate("d", &[("cap:x", "yes")]),
            candidate("e", &[("cap:x", "yes")]),
            candidate("f", &[("cap:x", "yes")]),
        ];
        let answers = vec![Answer {
            dimension_key: "cap:x".to_string(),
            option_id: "yes".to_string(),
            mode: MODE_REQUIRED.to_string(),
        }];
        let state = State {
            candidates: tied,
            answers,
            asked: vec!["cap:x".to_string()],
            depth: 1,
            last_family: String::new(),
        };
        let (stop, reason) = should_stop(&state);
        assert!(!stop || reason == STOP_LOW_GAIN, "a six-way tie is not a finding: {reason}");
    }

    #[test]
    fn gaps_report_unknown_data_not_unaskable_questions() {
        let set = vec![
            candidate("a", &[("cap:x", "yes")]),
            candidate("b", &[("cap:x", "yes")]),
            candidate("c", &[]),
        ];
        let state = State {
            candidates: set,
            ..State::default()
        };
        let gaps = gaps(&state);
        let gap = gaps.iter().find(|g| g.key == "cap:x").unwrap();
        assert_eq!(gap.unknown_fields, 1, "the split is perfect AND one candidate is unknown");
        assert_eq!(gap.held_back, 1);
    }

    #[test]
    fn work_items_carry_their_own_dimensions_over_the_projects() {
        let mut inherited = BTreeMap::new();
        inherited.insert("language".to_string(), "rust".to_string());
        inherited.insert("cap:async".to_string(), "yes".to_string());
        let candidate = work_item_candidate(
            7,
            "concord",
            "Fix the parser",
            42,
            Some(20),
            true,
            " L ",
            &inherited,
        );
        assert_eq!(candidate.slug, "concord#42");
        assert_eq!(candidate.attrs["language"], "rust");
        assert_eq!(candidate.attrs["cap:async"], "yes");
        assert_eq!(candidate.attrs[DIM_MINUTES], MINUTES_60, "20 minutes buckets to an hour");
        assert_eq!(candidate.attrs[DIM_DIFFICULTY], "hard");
        assert_eq!(candidate.attrs[DIM_MENTOR], "yes");
        assert_eq!(item_id_from_key("concord#42"), Some(("concord".to_string(), 42)));
        assert_eq!(item_id_from_key("#42"), None);
        assert_eq!(item_id_from_key("concord#"), None);
        assert_eq!(item_id_from_key("concord#x"), None);
    }

    #[test]
    fn an_unstated_budget_is_unknown_not_a_guess() {
        let candidate = work_item_candidate(
            7, "p", "t", 1, None, false, "", &BTreeMap::new(),
        );
        assert!(!candidate.attrs.contains_key(DIM_MINUTES), "no budget means no attribute");
        assert!(!candidate.attrs.contains_key(DIM_DIFFICULTY));
        assert_eq!(candidate.attrs[DIM_MENTOR], "no", "false is a fact, not a missing value");
    }

    #[test]
    fn an_unmeasured_project_never_outranks_a_healthy_one() {
        let mk = |id: i64, fit: f64| Ranked {
            candidate: Candidate::new(id, &format!("p{id}"), "n"),
            fit,
            matches: BTreeMap::new(),
            warnings: BTreeMap::new(),
            unknown: Vec::new(),
            explanation: BTreeMap::new(),
        };
        let mut items = vec![mk(1, 0.5), mk(2, 0.5)];
        let mut health = BTreeMap::new();
        health.insert(1, None);
        health.insert(2, Some(0.2));
        order_work_items(&mut items, &health);
        assert_eq!(items[0].candidate.project_id, 2, "measured beats unmeasured at equal fit");

        let mut items = vec![mk(1, 0.5), mk(2, 0.5)];
        let mut health = BTreeMap::new();
        health.insert(1, Some(0.9));
        health.insert(2, Some(0.2));
        order_work_items(&mut items, &health);
        assert_eq!(items[0].candidate.project_id, 1, "healthier first");
    }
}
