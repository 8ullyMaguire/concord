//! Free text → catalogue capabilities (`docs/specs/scout-spec.md` §4).
//!
//! The decomposition is what makes Scout **reviewable rather than authoritative**: it names the axes it will score on, so a user can see
//! "it read my idea as being about offline support and self-hosting" and correct it before reading verdicts.
//!
//! Two properties, and the second is the one that keeps it honest:
//!
//! * **A capability comes from the catalogue or it is a proposal.** A word in the text that no catalogue row defines produces a
//!   [`Capability`] with `in_catalog: false`, which the classifier scores ZERO weight. It is reported, never silently dropped, and never
//!   scored against nothing.
//!
//! * **Matching is by explicit signal.** Verbatim key, then a label or category word, then nothing. "Nothing" means `coverage: open` —
//!   not a weak match.

use crate::Capability;

/// The shortest word that can contribute to a label match.
///
/// Four characters. Three-letter words are mostly noise ("the", "and") — and `api` is three and a real one, so it is handled by the exact
/// key match above rather than by lowering the floor for everything.
const MIN_WORD_LEN: usize = 4;

/// Words excluded from label matching.
///
/// Small closed-class words, plus the ones that describe the *scout* rather than the project being scouted. Without them, "deploy" in "we
/// should deploy it" matches every capability labelled Deployment — a match on the user's sentence rather than on their idea.
const STOP_WORDS: [&str; 42] = [
    "this", "that", "with", "from", "have", "been", "were", "they", "them", "then", "than", "when", "what", "which", "will", "would",
    "could", "should", "about", "into", "your", "their", "there", "here", "just", "like", "make", "made", "want", "need", "some", "more",
    "most", "very", "also", "because", "using", "used", "uses", "does", "done", "onto",
];

/// Extract capabilities from free text.
///
/// `catalog` is the whole vocabulary. Passing a filtered catalogue silently changes what Scout can see, which is why the store exposes the
/// whole thing and why the caller is expected to pass everything.
#[must_use]
pub fn decompose(idea: &str, catalog: &[Capability]) -> Vec<Capability> {
    let text = normalize(idea);
    let words = words_of(&text);
    // The RAW lowercased text, not the normalized one: `normalize` collapses punctuation to spaces, so `wip-limits` becomes `wip limits`
    // and a `contains(normalize(key))` test against the raw text can never fire for a hyphenated key — which is most of them. The first
    // Go version matched the raw text and so silently matched almost nothing.
    let raw = idea.trim().to_lowercase();

    let mut out: Vec<Capability> = Vec::new();

    // Step 1: verbatim key. The strongest signal available, and the one the spec calls out by name: a user who typed `wip-limits` meant
    // it. `text` is normalized, so the key must be normalized too — both undergo the same punctuation collapse, which is what lets
    // `wip-limits` meet `wip limits`.
    for capability in catalog {
        let key = capability.key.trim().to_lowercase();
        if key.is_empty() {
            continue;
        }
        if key.chars().count() > 3 && text.contains(&normalize(&key)) {
            out.push(Capability {
                key: capability.key.clone(),
                label: capability.label.clone(),
                category: capability.category.clone(),
                in_catalog: true,
                coverage: String::new(),
                evidence: "verbatim_key".to_string(),
                signals: Vec::new(),
            });
        }
    }

    // Step 2: a label or category word. Weaker, and marked as such, because "offline" in the text matching a capability labelled "Offline
    // use" is a good match while "deploy" matching "Deployment" is a mediocre one — the signal tells the reader which kind they are
    // looking at.
    let mut hits: Vec<(Capability, &'static str)> = Vec::new();
    for capability in catalog {
        let evidence = if matches_word(&words, &normalize(&capability.label)) {
            Some("label_word")
        } else if !capability.category.is_empty() && matches_word(&words, &normalize(&capability.category)) {
            Some("category_word")
        } else {
            None
        };
        if let Some(evidence) = evidence {
            hits.push((capability.clone(), evidence));
        }
    }

    // Two capabilities sharing a label are a near-duplicate, and an editor should merge them rather than have the idea scored twice on one
    // axis. The earlier one wins and the later records the collision.
    let mut by_label: std::collections::BTreeMap<String, Vec<usize>> = std::collections::BTreeMap::new();
    for (index, (capability, _)) in hits.iter().enumerate() {
        by_label.entry(normalize(&capability.label)).or_default().push(index);
    }
    for indices in by_label.values() {
        if indices.len() < 2 {
            continue;
        }
        let first = hits[indices[0]].0.key.clone();
        for index in &indices[1..] {
            hits[*index].0.key.clone_from(&first);
        }
    }

    // Seeded with the keys already matched verbatim, so this ONE map dedupes across both phases.
    //
    // A separate `claimed` set beside this one was removed from the Go as "provably redundant" and that was wrong: `added` only ever
    // deduplicated within `hits`, so it could not see what step 1 had already pushed. A capability findable BOTH ways — `wip-limits`
    // typed verbatim and matched by its label words — then appeared twice and the same axis was scored twice. Deleting the guard did not
    // remove a duplicate mechanism; it removed the only check spanning the two phases.
    let mut added: std::collections::BTreeSet<String> = out.iter().map(|capability| capability.key.clone()).collect();
    for (capability, evidence) in hits {
        if added.contains(&capability.key) {
            continue;
        }
        added.insert(capability.key.clone());
        out.push(Capability {
            key: capability.key,
            label: capability.label,
            category: capability.category,
            in_catalog: true,
            coverage: String::new(),
            evidence: evidence.to_string(),
            signals: Vec::new(),
        });
    }

    // Step 3: proposals. A word that is neither a catalogue key nor a matched label is NOT a proposal — a free-text idea is full of
    // ordinary words, and turning each of them into a capability would bury the real ones. A proposal requires a shape the catalogue would
    // recognise: a capability-style token, i.e. a hyphenated slug.
    //
    // This is the deliberate narrowing the spec asks for ("never invents a capability outside the catalog"), and it is why the report can
    // say `proposed: 0` honestly rather than proposing `dashboard`.
    //
    // The RAW tokens, not `words`: `words` comes from the normalized text, which has no hyphens left, so scanning it for a token
    // CONTAINING a hyphen could never match — the first Go version produced no proposals at all. The test that caught it asserted on WHICH
    // key came out rather than on a count, which is the only reason it was caught rather than passing on an empty result.
    let mut seen_proposals: std::collections::BTreeSet<String> = std::collections::BTreeSet::new();
    for token in raw_tokens(&raw) {
        if token.chars().count() < MIN_WORD_LEN || is_stop_word(&token) || !token.contains('-') {
            continue;
        }
        if is_catalog_key(catalog, &token) {
            // Already captured in step 1 — it IS in the catalogue, so matching it again as a proposal would double-count the axis.
            continue;
        }
        if !seen_proposals.insert(token.clone()) {
            continue;
        }
        out.push(Capability {
            key: token.clone(),
            label: humanise(&token),
            category: String::new(),
            in_catalog: false,
            coverage: "open".to_string(),
            evidence: "text_token".to_string(),
            signals: Vec::new(),
        });
    }

    // In-catalogue capabilities first: they are the scorable half, and a reader should see what will actually be scored before what will
    // not. Then category, then key, so the order never depends on the catalogue's row order.
    out.sort_by(|a, b| {
        b.in_catalog
            .cmp(&a.in_catalog)
            .then_with(|| a.category.cmp(&b.category))
            .then_with(|| a.key.cmp(&b.key))
    });
    out
}

fn is_stop_word(word: &str) -> bool {
    STOP_WORDS.contains(&word)
}

fn is_catalog_key(catalog: &[Capability], key: &str) -> bool {
    catalog.iter().any(|capability| normalize(&capability.key) == normalize(key))
}

/// Whether any word of `words` is a whole word of the label.
///
/// **Whole word, not substring**: "ran" inside "truncated" is not a match for a capability labelled "Runs locally", and a substring rule
/// produces exactly that kind of nonsense on the first text anyone types.
fn matches_word(words: &[&str], normalized_label: &str) -> bool {
    if normalized_label.is_empty() {
        return false;
    }
    label_words(normalized_label)
        .iter()
        .any(|candidate| words.iter().any(|word| word == candidate))
}

fn label_words(normalized: &str) -> Vec<String> {
    normalized
        .split(|ch: char| !ch.is_alphanumeric())
        .filter(|part| !part.is_empty())
        .map(str::to_string)
        .collect()
}

/// Lowercase and collapse punctuation to single spaces, so `WIP-Limits`, `wip limits` and `wip_limits` all compare equal.
#[must_use]
pub fn normalize(text: &str) -> String {
    let lowered = text.trim().to_lowercase();
    let mut out = String::with_capacity(lowered.len());
    let mut last_space = true;
    for ch in lowered.chars() {
        if ch.is_alphanumeric() {
            out.push(ch);
            last_space = false;
        } else if !last_space {
            out.push(' ');
            last_space = true;
        }
    }
    out.trim().to_string()
}

fn words_of(normalized: &str) -> Vec<&str> {
    normalized.split_whitespace().collect()
}

/// Split the RAW lowercased text into tokens, keeping punctuation that carries meaning — specifically the hyphen, which is what
/// distinguishes a capability-shaped slug from an ordinary word.
fn raw_tokens(raw: &str) -> Vec<String> {
    raw.split(|ch: char| !ch.is_alphanumeric() && ch != '-' && ch != '_')
        .filter(|part| !part.is_empty())
        .map(str::to_string)
        .collect()
}

/// `live-sync` → `Live Sync`, for a proposal's label.
fn humanise(key: &str) -> String {
    key.split(['-', '_'])
        .filter(|part| !part.is_empty())
        .map(|part| {
            let mut chars = part.chars();
            match chars.next() {
                Some(first) => first.to_uppercase().collect::<String>() + chars.as_str(),
                None => String::new(),
            }
        })
        .collect::<Vec<_>>()
        .join(" ")
}
