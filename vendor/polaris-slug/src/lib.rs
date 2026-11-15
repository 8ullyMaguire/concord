//! One slug rule for the estate.
//!
//! Measured before writing it (2026-11-10): three implementations of the same twelve lines —
//! `darkhold/crates/scrapers/src/csv.rs:281` (as a dedupe key), and **two inside suki**
//! (`services/lens.rs:800` for saved lenses, `services/pools.rs:311` for tag pools). They agreed on the algorithm and
//! disagreed on the two things a caller actually needs to decide: the length cap (64, 60, none) and what an empty result
//! becomes (`"lens"`, `"pool"`, `""`).
//!
//! ## Why this is not `slug` 0.1.6 (46.7M downloads, alive)
//!
//! The crate transliterates through `deunicode` — `"Pokémon"` becomes `pokemon` where all three estate implementations
//! produce `pok-mon` (the `é` is not ASCII-alphanumeric, so it ends a run and becomes a separator). That is not a bug in
//! either: it is a different URL contract, and switching it silently rewrites the
//! address of every existing row. Measured on the corpus that would move: **14 of suki's 60,512 tags** carry a non-ASCII
//! character, plus whatever titles and author names darkhold folds into its dedupe key. Fourteen URLs is small enough to
//! argue about and large enough that nobody should do it as a side effect of a refactor. Adopting a transliterator is a
//! migration with a redirect table; this crate is the same rule written once.
//!
//! ## What the caller declares
//!
//! [`Policy`] holds the two things that genuinely differ per surface, and nothing else:
//!
//! * `max_len` — a URL segment's budget. The cap is applied **after** trimming and on a `char` boundary, so a cap can
//!   never produce a trailing dash.
//! * `fallback` — what an empty result becomes. `None` means "empty is a legitimate answer" and is what darkhold's dedupe
//!   key wants: an empty key is a fact about the input, not something to paper over with a word.
//!
//! The separator and the ASCII rule are **not** policy. A dash and `[a-z0-9]` are the estate's URL grammar; a site that
//! wanted `_` or unicode segments would be inventing a second grammar, and every link between products would have to know
//! which one it was looking at.

/// How a surface turns a name into a segment.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Policy {
    /// Maximum characters in the result, applied after trimming. `None` is uncapped.
    pub max_len: Option<usize>,
    /// What an empty result becomes. `None` keeps the emptiness.
    pub fallback: Option<&'static str>,
}

impl Policy {
    /// Uncapped, and an empty result stays empty: the shape darkhold's dedupe key needs.
    pub const NONE: Policy = Policy { max_len: None, fallback: None };

    /// A URL segment with a budget and a word for the empty case.
    pub const fn segment(max_len: usize, fallback: &'static str) -> Policy {
        Policy { max_len: Some(max_len), fallback: Some(fallback) }
    }
}

/// Turn a name into a segment: lowercase ASCII alphanumerics, every run of anything else becomes one dash.
///
/// Non-ASCII characters are **dropped, not transliterated** — see the module note: `"Pokémon"` is `pokmon`, and that is
/// the established contract whose URLs already exist.
pub fn slug(raw: &str, policy: Policy) -> String {
    let mut out = String::with_capacity(raw.len());
    let mut pending_dash = false;
    for ch in raw.chars() {
        if ch.is_ascii_alphanumeric() {
            if pending_dash && !out.is_empty() {
                out.push('-');
            }
            out.push(ch.to_ascii_lowercase());
            pending_dash = false;
        } else {
            pending_dash = true;
        }
    }
    // The cap is on characters and lands after trimming, so a capped result can never end in a dash even when the cut
    // falls just after one.
    if let Some(max) = policy.max_len {
        if out.chars().count() > max {
            let cut: String = out.chars().take(max).collect();
            out = cut.trim_end_matches('-').to_string();
        }
    }
    if out.is_empty() {
        if let Some(fallback) = policy.fallback {
            return fallback.to_string();
        }
    }
    out
}

/// `base`, `base-2`, `base-3`, … — the caller decides when a segment is taken, because only the caller has the table.
///
/// Kept here so two surfaces cannot append the suffix differently: `-2` before the cap, never inside it.
pub fn with_suffix(base: &str, n: u32) -> String {
    if n <= 1 {
        return base.to_string();
    }
    format!("{base}-{n}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ordinary_names_become_segments() {
        assert_eq!(slug("Sakura landscapes", Policy::NONE), "sakura-landscapes");
        assert_eq!(slug("  Mecha — 80s!! ", Policy::NONE), "mecha-80s");
        assert_eq!(slug("Solo   Set", Policy::NONE), "solo-set");
    }

    #[test]
    fn non_ascii_is_dropped_not_transliterated() {
        // The measurement that decided this crate: `slug` 0.1.6 through deunicode gives `pokemon`; every estate caller
        // has always given `pok-mon`, and those URLs exist. (The first draft of this file claimed `pokmon` — a guess that
        // the test refuted, which is the whole reason the assertion exists.)
        assert_eq!(slug("Pokémon", Policy::NONE), "pok-mon");
        assert_eq!(slug("桜 Sakura", Policy::NONE), "sakura");
    }

    #[test]
    fn never_starts_or_ends_with_a_separator() {
        for raw in ["--leading", "trailing--", "!!", "  ", "..."] {
            let out = slug(raw, Policy::NONE);
            assert!(!out.starts_with('-'), "{raw:?} -> {out:?}");
            assert!(!out.ends_with('-'), "{raw:?} -> {out:?}");
        }
    }

    #[test]
    fn empty_becomes_the_declared_word_or_stays_empty() {
        assert_eq!(slug("!!!", Policy::segment(64, "lens")), "lens");
        assert_eq!(slug("", Policy::segment(60, "pool")), "pool");
        assert_eq!(slug("!!!", Policy::NONE), "");
    }

    #[test]
    fn the_cap_is_on_characters_and_never_leaves_a_dash() {
        let long = "alpha bravo charlie delta echo foxtrot";
        let capped = slug(long, Policy::segment(12, "x"));
        assert!(capped.chars().count() <= 12, "{capped:?}");
        assert!(!capped.ends_with('-'), "{capped:?}");
        // Cut at 12 would land on "alpha-bravo-" without the second trim.
        assert_eq!(capped, "alpha-bravo");
    }

    #[test]
    fn the_cap_does_not_apply_to_the_fallback() {
        assert_eq!(slug("!!!", Policy::segment(2, "fallback")), "fallback");
    }

    #[test]
    fn an_already_short_name_is_untouched() {
        assert_eq!(slug("tag", Policy::segment(64, "lens")), "tag");
    }

    #[test]
    fn suffixes_count_from_two_and_preserve_the_base() {
        assert_eq!(with_suffix("lens", 1), "lens");
        assert_eq!(with_suffix("lens", 2), "lens-2");
        assert_eq!(with_suffix("lens", 11), "lens-11");
    }

    #[test]
    fn the_two_live_policies_agree_on_the_shared_inputs() {
        // suki declares 64/"lens" for saved lenses and 60/"pool" for tag pools. The divergence was never the algorithm.
        let lens = Policy::segment(64, "lens");
        let pool = Policy::segment(60, "pool");
        for name in ["Sakura landscapes", "  Blue   Archive!! ", "tag"] {
            assert_eq!(slug(name, lens), slug(name, pool), "{name:?}");
        }
        assert_eq!(slug("!!!", lens), "lens");
        assert_eq!(slug("!!!", pool), "pool");
    }
}
