//! Where a project is in its life, derived from measurements that already exist.
//!
//! # Why this is a pure function and not a column
//!
//! A stage is a *reading* of signals — last commit age, measured health, how much has shipped, how old the project is — and every one
//! of those is already stored and already swept. Writing the answer into a column would create a second truth that goes stale the
//! moment the sweep runs, and the first reader to disagree with it would have no way to tell which one was right. So the stage is
//! computed, from the same numbers the score is computed from, in a crate that needs no database to test.
//!
//! The two stages this module **refuses** to infer are `Archived` and `Forked & alive`: both are acts by a person, and a stage the code
//! assigns to somebody's project is a stage that is wrong about it. They arrive as columns written by an act; this enum will read them
//! when they exist (`docs/plans/post-port-roadmap.md`, step 5).
//!
//! # The five rules, and the absence rules that are half of them
//!
//! 1. **Dormant wins over everything measurable**: ninety days without a commit. That is the note's number and it agrees with the
//!    sweep's cadence — a project that has not moved in a season is not moving.
//! 2. **An unmeasured project is NEVER dormant.** `None` is "nobody has measured this", not "nothing has happened". This repo has paid
//!    for that confusion once already: `project_metrics.health_score` is `0` for every project until the sweep writes it, and
//!    `sort=health` over that column was a total tie that silently degraded to `updated_at`.
//! 3. **Nothing shipped and nothing committed is `Seed`** — the note's first stage, and the only one that can be established from
//!    absence *because both absences are measured*: a feature count of zero is a fact about the database, not about a forge.
//! 4. **Something shipped but young or unproven is `Sprout`.**
//! 5. **Shipped, old enough and measured healthy is `Mature`.**
//!
//! And when none of them can be established, the answer is [`Stage::Unknown`] with the missing signal named, because [`Stage::Seed`] is
//! a claim about the project and "I could not measure it" is a claim about the measurement.

/// Where a project is in its life.
///
/// `Archived` and `Forked` are deliberately absent — see the module comment.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Stage {
    /// An idea: nothing shipped, nothing committed.
    Seed,
    /// Something works, but it is young or not yet measured healthy.
    Sprout,
    /// Shipped, at least half a year old, and measured healthy.
    Mature,
    /// Ninety days without a commit, measured.
    Dormant,
    /// The signals cannot support any of the above; `because` names what is missing.
    Unknown,
}

impl Stage {
    /// The word a page prints. Lowercase, because every other label on these pages is.
    pub fn as_str(self) -> &'static str {
        match self {
            Stage::Seed => "seed",
            Stage::Sprout => "sprout",
            Stage::Mature => "mature",
            Stage::Dormant => "dormant",
            Stage::Unknown => "unknown",
        }
    }

    /// Whether a reader should be warned before relying on the project.
    ///
    /// True for `Dormant` only. `Unknown` is not a warning: it says this instance has not measured the project, which is a statement
    /// about us, and warning a reader about our own gap would teach them to ignore the warning that matters.
    pub fn is_warning(self) -> bool {
        matches!(self, Stage::Dormant)
    }
}

/// Everything the rules read. Each field is a stored measurement; none is an opinion.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Signals {
    /// Whether the project names a repository at all. It is the difference between "holds only ideas" and "names a repository this
    /// instance has never read", and those two must not be the same stage.
    pub has_repository: bool,
    /// Days since the last commit. `None` means the forge has not been read — either because there is no repository
    /// (`has_repository == false`) or because the sweep has never run for it.
    pub last_commit_age_days: Option<f64>,
    /// The maintenance score in `[0,1]`, or `None` when nothing has been measured.
    pub health_score: Option<f64>,
    /// Features whose status is `shipped`.
    pub features_shipped: i64,
    /// Days since the project row was created.
    pub age_days: f64,
}

/// A stage and the sentence that justifies it.
///
/// `because` is not decoration: a stage on a page with no reason is a label a reader has to trust, and the sweep that will read this
/// (step 5) has to be able to say why it marked something dormant.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Verdict {
    pub stage: Stage,
    pub because: &'static str,
}

/// How long without a commit makes a project dormant. The note's number.
pub const DORMANT_AFTER_DAYS: f64 = 90.0;

/// How old a project must be to be called mature. Half a year: long enough that "it survived" means something.
pub const MATURE_AFTER_DAYS: f64 = 180.0;

/// The health at which a project stops being "unproven". The same 0.6 the badge and the Scout's `avoid` floor use — one number, so
/// the three surfaces cannot disagree about what healthy means.
pub const MATURE_HEALTH: f64 = 0.6;

/// Derive the stage.
pub fn stage(signals: Signals) -> Verdict {
    // Rule 1. Dormant, and only from a measurement — `None` never reaches this arm.
    if let Some(age) = signals.last_commit_age_days {
        // `age >= 90` is false for NaN, which is the right answer: an unreadable timestamp is not a fortnight of silence.
        if age >= DORMANT_AFTER_DAYS {
            return Verdict {
                stage: Stage::Dormant,
                because: "measured: no commit for ninety days or more",
            };
        }
    }

    // Rules 4 and 5: anything that has shipped is past the idea stage, and only age plus a measured score make it mature.
    if signals.features_shipped > 0 {
        if signals.age_days < MATURE_AFTER_DAYS {
            return Verdict { stage: Stage::Sprout, because: "shipped something and is younger than six months" };
        }
        return match signals.health_score {
            Some(score) if score >= MATURE_HEALTH => Verdict {
                stage: Stage::Mature,
                because: "shipped, six months old, and measured healthy",
            },
            Some(_) => Verdict { stage: Stage::Sprout, because: "shipped, but measured below the healthy floor" },
            // Absence is not a low score. A project nobody has measured is unproven, which is what `Sprout` means.
            None => Verdict { stage: Stage::Sprout, because: "shipped, but its health has never been measured" },
        };
    }

    // Rule 3, and the arm that needed `has_repository` to exist. Nothing shipped splits three ways, and only the first is `Seed`.
    match (signals.has_repository, signals.last_commit_age_days) {
        // A repository exists and has never been read: this is a missing measurement, and calling it an idea would say something
        // about somebody's project that this instance has no basis for.
        (true, None) => Verdict { stage: Stage::Unknown, because: "names a repository this instance has never read" },
        // No repository, nothing shipped: an idea, and both halves of that are measured facts about this database.
        (false, _) => Verdict { stage: Stage::Seed, because: "nothing shipped and no repository named" },
        // Commits, nothing landed yet: there is code, so it is past the idea stage.
        (true, Some(_)) => Verdict { stage: Stage::Sprout, because: "commits, but nothing shipped yet" },
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn signals(commit: Option<f64>, health: Option<f64>, shipped: i64, age: f64) -> Signals {
        // A project that names a repository, unless a test says otherwise — that is the common shape and the interesting default:
        // the subtle arms are the ones where a repository exists and the forge has not been read.
        Signals { has_repository: commit.is_some(), last_commit_age_days: commit, health_score: health, features_shipped: shipped, age_days: age }
    }

    fn with_repo(has_repository: bool, commit: Option<f64>, shipped: i64) -> Signals {
        Signals { has_repository, last_commit_age_days: commit, health_score: None, features_shipped: shipped, age_days: 10.0 }
    }

    /// Rule 1 — and the number itself, because a threshold that drifts is a rule that drifts.
    #[test]
    fn ninety_days_without_a_commit_is_dormant() {
        assert_eq!(stage(signals(Some(90.0), Some(0.9), 40, 900.0)).stage, Stage::Dormant);
        assert_eq!(stage(signals(Some(89.9), Some(0.9), 40, 900.0)).stage, Stage::Mature);
    }

    /// Rule 2, the one this repo has already paid for: no measurement is not no activity.
    #[test]
    fn an_unmeasured_project_is_never_dormant() {
        let verdict = stage(signals(None, None, 40, 900.0));
        assert_ne!(verdict.stage, Stage::Dormant, "None is 'not measured', not 'dead'");
        assert!(!verdict.stage.is_warning());
    }

    /// Rule 3 — the idea stage, from two absences that are both measured.
    #[test]
    fn nothing_shipped_and_no_repository_is_seed() {
        assert_eq!(stage(signals(None, None, 0, 3.0)).stage, Stage::Seed);
    }

    /// Rule 4, first arm: young.
    #[test]
    fn shipped_and_young_is_sprout_however_healthy() {
        assert_eq!(stage(signals(Some(2.0), Some(1.0), 12, 30.0)).stage, Stage::Sprout);
    }

    /// Rule 4, second arm: measured but below the floor.
    #[test]
    fn shipped_and_measured_below_the_floor_is_sprout() {
        assert_eq!(stage(signals(Some(2.0), Some(0.59), 12, 400.0)).stage, Stage::Sprout);
        assert_eq!(stage(signals(Some(2.0), Some(0.60), 12, 400.0)).stage, Stage::Mature);
    }

    /// Rule 4, third arm: absence is not a bad score, and it is not a good one either.
    #[test]
    fn shipped_and_never_measured_is_sprout_not_mature() {
        let verdict = stage(signals(Some(2.0), None, 12, 400.0));
        assert_eq!(verdict.stage, Stage::Sprout);
        assert!(verdict.because.contains("never been measured"));
    }

    /// Nothing shipped, but a commit was measured: there is code, so it is past the idea stage.
    #[test]
    fn commits_without_anything_shipped_is_sprout() {
        assert_eq!(stage(signals(Some(5.0), None, 0, 40.0)).stage, Stage::Sprout);
    }

    /// The Unknown arm has to be REACHABLE, or it is a variant nobody ever sees and every later reader assumes it is dead code.
    /// The reachable case is a project that names a repository this instance has never read — a missing measurement, not an idea.
    #[test]
    fn a_repository_never_read_is_unknown_and_says_so() {
        let verdict = stage(with_repo(true, None, 0));
        assert_eq!(verdict.stage, Stage::Unknown);
        assert!(verdict.because.contains("never read"), "the reason must name what is missing: {}", verdict.because);
        // And it is NOT dormant: nothing was measured, so nothing can have gone quiet.
        assert!(!verdict.stage.is_warning());
    }

    /// The pair that motivated `has_repository`: the same signals, different stage, because the difference is a real fact.
    #[test]
    fn naming_a_repository_is_the_difference_between_idea_and_unmeasured() {
        assert_eq!(stage(with_repo(false, None, 0)).stage, Stage::Seed);
        assert_eq!(stage(with_repo(true, None, 0)).stage, Stage::Unknown);
    }

    /// NaN is not a measurement. `NaN >= 90` is false, so an unreadable timestamp must not become dormancy.
    #[test]
    fn an_unreadable_timestamp_is_not_dormancy() {
        let verdict = stage(with_repo(true, Some(f64::NAN), 0));
        assert_ne!(verdict.stage, Stage::Dormant);
    }

    /// `Dormant` is the only warning, and `Unknown` is not one.
    #[test]
    fn only_dormant_warns_a_reader() {
        assert!(Stage::Dormant.is_warning());
        assert!(!Stage::Unknown.is_warning());
        assert!(!Stage::Seed.is_warning());
        assert!(!Stage::Sprout.is_warning());
        assert!(!Stage::Mature.is_warning());
    }

    /// The words, because a page prints them and a probe asserts on them.
    #[test]
    fn every_stage_has_the_word_a_page_prints() {
        assert_eq!(Stage::Seed.as_str(), "seed");
        assert_eq!(Stage::Sprout.as_str(), "sprout");
        assert_eq!(Stage::Mature.as_str(), "mature");
        assert_eq!(Stage::Dormant.as_str(), "dormant");
        assert_eq!(Stage::Unknown.as_str(), "unknown");
    }
}
