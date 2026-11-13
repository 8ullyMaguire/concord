//! §15's maintenance-health score and §4.5.7's newcomer responsiveness, as arithmetic.
//!
//! The score is **transparent, not vibes**: every component is exposed, and the two facts that are *not*
//! measurements are handled as absence rather than as zero.
//!
//! Four rules, each of which changed a number when it was wrong in the Go tree:
//!
//! * **A repository nobody has opened a pull request on has NOT demonstrated instant reviews.** `0` hours once
//!   meant "reviewed within a day", which scored exactly the projects with no review process at all as perfect.
//!   The sentinel is negative, and reads are `known` or not.
//! * **An unknown component is EXCLUDED and the rest renormalised**, not counted as a zero: dividing by the full
//!   weight sum would cap a project that is perfect at everything measurable at 0.75, which reads as mediocrity
//!   rather than as missing data.
//! * **A weight set that does not sum to 1 is normalised, not trusted.** A hand-typed set could otherwise produce
//!   a score outside `[0,1]` and write it into `project_metrics.health_score` where every reader treats it as
//!   normalised.
//! * **Only a newcomer's FIRST pull request counts**, a self-comment is not a response, and the median is the
//!   lower-middle value rather than an interpolation: `{2h, 10h}` is 2, because a median that interpolates
//!   invents a response time nobody had.

/// The signals that feed the score. Every one is a forge-synced fact, never an opinion.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Metrics {
    /// Days since the last commit; `0` is today, a huge value if the repository never had one.
    pub last_commit_age_days: f64,
    /// Median hours to the first response on a pull request, or [`UNKNOWN_REVIEW_HOURS`].
    pub median_review_hours: f64,
    /// Distinct authors in the last year.
    pub contributors: i64,
    /// Releases or tags in the last 90 days.
    pub releases_90d: i64,
}

/// The sentinel for "there is nothing to measure here". Any negative value means the same thing, so a caller
/// cannot accidentally publish `-1` hours.
pub const UNKNOWN_REVIEW_HOURS: f64 = -1.0;

/// The exposed weights.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Weights {
    /// Recency of the last commit.
    pub recency: f64,
    /// Speed of the first response.
    pub responsiveness: f64,
    /// How many people contribute.
    pub breadth: f64,
    /// How often something ships.
    pub cadence: f64,
}

/// Concord's neutral weighting.
pub const DEFAULT_WEIGHTS: Weights = Weights {
    recency: 0.35,
    responsiveness: 0.25,
    breadth: 0.20,
    cadence: 0.20,
};

impl Weights {
    /// Rescale so the components sum to 1, and report whether that was possible.
    ///
    /// An all-zero set is returned unchanged with `false`: there is no meaningful normalisation of "no opinion",
    /// and silently substituting the defaults would leave a caller unable to tell its input was rejected. The zero
    /// set scores 0 everywhere, which is honest.
    #[must_use]
    pub fn normalized(self) -> (Self, bool) {
        let sum = self.recency + self.responsiveness + self.breadth + self.cadence;
        if sum <= 0.0 {
            return (self, false);
        }
        if (sum - 1.0).abs() < 1e-9 {
            return (self, true);
        }
        (
            Self {
                recency: self.recency / sum,
                responsiveness: self.responsiveness / sum,
                breadth: self.breadth / sum,
                cadence: self.cadence / sum,
            },
            true,
        )
    }
}

/// Each component, so a reader can see *why* a project scores what it scores.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Breakdown {
    /// Recency component.
    pub recency: f64,
    /// Responsiveness component, `0` when unknown.
    pub responsiveness: f64,
    /// Breadth component.
    pub breadth: f64,
    /// Cadence component.
    pub cadence: f64,
    /// The weighted total in `[0,1]`.
    pub total: f64,
    /// Whether responsiveness was measured at all: the component is excluded from the total when it is not.
    pub responsiveness_known: bool,
}

/// Exponential decay with a 30-day half-life.
fn recency_score(age_days: f64) -> f64 {
    (0.5_f64).powf(age_days / 30.0)
}

/// `<= 24h` → 1.0, `>= 14 days` → 0.0, log-linear between.
fn responsiveness_score(median_hours: f64) -> f64 {
    if median_hours <= 24.0 {
        return 1.0;
    }
    if median_hours >= 14.0 * 24.0 {
        return 0.0;
    }
    let x = (median_hours / 24.0).ln() / 14.0_f64.ln();
    (1.0 - x).max(0.0)
}

/// 50 contributors → 1.0, log-scaled below, `0` at one or none.
fn breadth_score(contributors: i64) -> f64 {
    if contributors <= 1 {
        return 0.0;
    }
    // The clamp is on the RATIO, not on the logarithm: `log10(50).min(1.0)` clamps 1.699 to 1 and then divides
    // by 1.7076, reporting 0.5856 for fifty contributors — a port bug the oracle caught on its first run.
    ((contributors as f64).log10() / 51.0_f64.log10()).min(1.0)
}

/// Three releases in 90 days → 1.0, linear below.
fn cadence_score(releases_90d: i64) -> f64 {
    (releases_90d as f64 / 3.0).min(1.0)
}

/// The composite score in `[0,1]`, with every component exposed.
#[must_use]
pub fn health_score(metrics: Metrics, weights: Weights) -> Breakdown {
    let (weights, normalized) = weights.normalized();
    let weights = if normalized { weights } else { weights };
    let known = metrics.median_review_hours >= 0.0;
    let mut breakdown = Breakdown {
        recency: recency_score(metrics.last_commit_age_days),
        responsiveness: 0.0,
        breadth: breadth_score(metrics.contributors),
        cadence: cadence_score(metrics.releases_90d),
        total: 0.0,
        responsiveness_known: known,
    };
    if known {
        breakdown.responsiveness = responsiveness_score(metrics.median_review_hours);
        breakdown.total = weights.recency * breakdown.recency
            + weights.responsiveness * breakdown.responsiveness
            + weights.breadth * breakdown.breadth
            + weights.cadence * breakdown.cadence;
        return breakdown;
    }
    // Renormalise over the three components that WERE measured. Dividing by the full sum would cap a project
    // that is perfect at everything it can be judged on at 0.75, which reads as mediocrity rather than as
    // missing data.
    let rest = weights.recency + weights.breadth + weights.cadence;
    if rest <= 0.0 {
        // Every remaining weight is zero: there is no honest score to report.
        breakdown.total = 0.0;
        return breakdown;
    }
    breakdown.total =
        (weights.recency * breakdown.recency + weights.breadth * breakdown.breadth
            + weights.cadence * breakdown.cadence)
            / rest;
    breakdown
}

/// The sample floor below which no newcomer number is published.
///
/// Five is not a statistical threshold, it is an editorial one: a median over three pull requests is a number
/// nobody should act on, and publishing it invites a reader to rank projects on it.
pub const NEWCOMER_MIN_SAMPLE: usize = 5;

/// One thing that happened on a repository's forge, as `forge_events` records it.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct ForgeEvent<'a> {
    /// `owner/name`.
    pub repo: &'a str,
    /// `push`, `pr_opened`, `pr_closed`, `pr_review` or `pr_comment`.
    pub kind: &'a str,
    /// The pull request's number, `0` for a push.
    pub number: i64,
    /// Who did it: the pull request's author for `pr_opened`, the responder otherwise.
    pub actor: &'a str,
    /// Unix seconds.
    pub at: f64,
}

/// The median hours from a newcomer's first pull request to the first reply by somebody else, the sample
/// size, and whether there was enough of a sample to publish a number.
///
/// The Go API is event-shaped rather than pull-request-shaped, and the port keeps that shape deliberately: the
/// "first pull request" test is *the earliest opening by that author in that repository*, which is a fact about
/// the event stream and not a flag a caller could set wrongly. Webhooks re-deliver out of order, so slice order
/// is not a fact about time.
///
/// Three rules, each of which changed the number when it was wrong:
///
/// 1. **Only a newcomer's first pull request counts** — a contributor's second is not a newcomer experience.
/// 2. **A self-comment is not a response** — counting the author's own follow-up publishes a responsiveness
///    number no contributor experienced.
/// 3. **The lower-middle median, never an interpolation** — with `{2h, 10h}` the answer is 2, because a median
///    that interpolates invents a response time nobody had.
///
/// `None` below [`NEWCOMER_MIN_SAMPLE`]: `hours` is meaningless then, and a number published anyway is a number
/// a reader will rank on.
#[must_use]
pub fn newcomer_response(events: &[ForgeEvent<'_>]) -> Option<(f64, usize)> {
    use std::collections::HashMap;

    // A pull request's author and opening, keyed by (repo, number). Only `pr_opened` carries them; a review or
    // comment for a pull request we never saw opened is skipped, because without the author we cannot tell
    // whether they are a newcomer and without the opening there is no interval to measure.
    let mut author: HashMap<(&str, i64), &str> = HashMap::new();
    let mut opened_at: HashMap<(&str, i64), f64> = HashMap::new();
    for event in events {
        if event.kind != "pr_opened" {
            continue;
        }
        author.insert((event.repo, event.number), event.actor);
        opened_at.insert((event.repo, event.number), event.at);
    }

    // First observed pull request per (repo, author), by the EARLIEST opening rather than by slice order.
    let mut first_pr: HashMap<(&str, &str), f64> = HashMap::new();
    for (key, actor) in &author {
        let opened = opened_at[key];
        let entry = first_pr.entry((key.0, *actor));
        let current = entry.or_insert(opened);
        if opened < *current {
            *current = opened;
        }
    }

    // The earliest response by somebody else, per pull request.
    let mut response_at: HashMap<(&str, i64), f64> = HashMap::new();
    for event in events {
        if event.kind != "pr_review" && event.kind != "pr_comment" {
            continue;
        }
        let key = (event.repo, event.number);
        let Some(pr_author) = author.get(&key) else {
            continue; // a response to a pull request we never saw opened
        };
        if event.actor == *pr_author {
            continue; // rule 2: a self-comment is not a response
        }
        let entry = response_at.entry(key);
        let current = entry.or_insert(event.at);
        if event.at < *current {
            *current = event.at;
        }
    }

    let mut samples: Vec<f64> = Vec::with_capacity(response_at.len());
    for (key, responded) in &response_at {
        let pr_author = author[key];
        // Rule 1: only the author's first pull request in this repository.
        if opened_at[key] != first_pr[&(key.0, pr_author)] {
            continue;
        }
        let hours = (responded - opened_at[key]) / 3600.0;
        if hours < 0.0 {
            continue;
        }
        samples.push(hours);
    }
    if samples.len() < NEWCOMER_MIN_SAMPLE {
        return None;
    }
    samples.sort_by(f64::total_cmp);
    // Rule 3: the lower-middle value.
    let middle = if samples.len() % 2 == 1 {
        samples[samples.len() / 2]
    } else {
        samples[samples.len() / 2 - 1]
    };
    Some((middle, samples.len()))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn metrics(age: f64, review: f64, contributors: i64, releases: i64) -> Metrics {
        Metrics {
            last_commit_age_days: age,
            median_review_hours: review,
            contributors,
            releases_90d: releases,
        }
    }

    #[test]
    fn an_unknown_component_is_excluded_not_zeroed() {
        let perfect_elsewhere = metrics(0.0, UNKNOWN_REVIEW_HOURS, 50, 3);
        let breakdown = health_score(perfect_elsewhere, DEFAULT_WEIGHTS);
        assert!(!breakdown.responsiveness_known);
        // The total is the three measured components averaged over THEIR weights (0.35+0.20+0.20), not over all
        // four (0.35+0.25+0.20+0.20 = 1.0). Dividing by the full sum would cap this at 0.75 — the number the Go
        // comment calls "mediocrity rather than missing data".
        let over_measured =
            (0.35 * breakdown.recency + 0.20 * breakdown.breadth + 0.20 * breakdown.cadence) / 0.75;
        assert!((breakdown.total - over_measured).abs() < 1e-12, "{breakdown:?}");
        assert!(breakdown.total > 0.75, "{breakdown:?}");
    }

    #[test]
    fn a_weight_set_that_does_not_sum_to_one_is_normalised() {
        let doubled = Weights {
            recency: 0.70,
            responsiveness: 0.50,
            breadth: 0.40,
            cadence: 0.40,
        };
        let (normalized, ok) = doubled.normalized();
        assert!(ok);
        assert!((normalized.recency - 0.35).abs() < 1e-12);
        let breakdown = health_score(metrics(0.0, 1.0, 50, 3), doubled);
        assert!(breakdown.total <= 1.0 + 1e-12, "{breakdown:?}");
    }

    #[test]
    fn all_zero_weights_are_not_quietly_replaced() {
        let zero = Weights {
            recency: 0.0,
            responsiveness: 0.0,
            breadth: 0.0,
            cadence: 0.0,
        };
        let (returned, ok) = zero.normalized();
        assert!(!ok);
        assert_eq!(returned.recency, 0.0);
        assert_eq!(health_score(metrics(0.0, 1.0, 50, 3), zero).total, 0.0);
    }

    #[test]
    fn the_sentinel_is_negative_hours_not_zero() {
        // A repository with no measurable PR is not a repository reviewed within a day.
        let scored = health_score(metrics(30.0, 0.0, 1, 0), DEFAULT_WEIGHTS);
        let unknown = health_score(metrics(30.0, UNKNOWN_REVIEW_HOURS, 1, 0), DEFAULT_WEIGHTS);
        assert!(scored.responsiveness_known);
        assert!(!unknown.responsiveness_known);
        assert!((scored.responsiveness - 1.0).abs() < 1e-12);
        assert!(scored.total > unknown.total);
    }

    fn events(rows: &[(&'static str, i64, &'static str, f64)]) -> Vec<ForgeEvent<'static>> {
        rows.iter()
            .map(|(kind, number, actor, at)| ForgeEvent {
                repo: "owner/repo",
                kind,
                number: *number,
                actor,
                at: *at,
            })
            .collect()
    }

    #[test]
    fn only_a_newcomers_first_pull_request_counts() {
        // Five newcomers plus one author's SECOND pull request, answered after 100 hours. If the second counted,
        // the sample would be six and the median would move.
        let stream = events(&[
            ("pr_opened", 1, "alice", 0.0),
            ("pr_comment", 1, "maintainer", 3600.0),
            ("pr_opened", 2, "bob", 0.0),
            ("pr_comment", 2, "maintainer", 7200.0),
            ("pr_opened", 3, "carol", 0.0),
            ("pr_comment", 3, "maintainer", 10800.0),
            ("pr_opened", 4, "dave", 0.0),
            ("pr_comment", 4, "maintainer", 14400.0),
            ("pr_opened", 5, "erin", 0.0),
            ("pr_comment", 5, "maintainer", 18000.0),
            ("pr_opened", 6, "alice", 100000.0),
            ("pr_comment", 6, "maintainer", 100000.0 + 360000.0),
        ]);
        let (median, sample) = newcomer_response(&stream).expect("five first pull requests");
        assert_eq!(sample, 5, "alice's second pull request is not a newcomer experience");
        assert_eq!(median, 10800.0 / 3600.0);
    }

    #[test]
    fn a_self_comment_is_not_a_response() {
        let stream = events(&[
            ("pr_opened", 1, "alice", 0.0),
            ("pr_comment", 1, "alice", 60.0),
            ("pr_opened", 2, "bob", 0.0),
            ("pr_comment", 2, "bob", 60.0),
            ("pr_opened", 3, "carol", 0.0),
            ("pr_comment", 3, "carol", 60.0),
            ("pr_opened", 4, "dave", 0.0),
            ("pr_comment", 4, "dave", 60.0),
            ("pr_opened", 5, "erin", 0.0),
            ("pr_comment", 5, "erin", 60.0),
        ]);
        assert_eq!(newcomer_response(&stream), None, "nobody else ever replied");
    }

    #[test]
    fn a_response_to_an_unseen_opening_is_skipped() {
        let stream = events(&[
            ("pr_review", 99, "maintainer", 3600.0),
            ("pr_opened", 1, "alice", 0.0),
            ("pr_comment", 1, "maintainer", 3600.0),
        ]);
        assert_eq!(newcomer_response(&stream), None, "one sample, below the floor of five");
    }

    #[test]
    fn below_five_samples_there_is_no_number() {
        let stream = events(&[
            ("pr_opened", 1, "alice", 0.0),
            ("pr_comment", 1, "maintainer", 3600.0),
            ("pr_opened", 2, "bob", 0.0),
            ("pr_comment", 2, "maintainer", 7200.0),
            ("pr_opened", 3, "carol", 0.0),
            ("pr_comment", 3, "maintainer", 10800.0),
            ("pr_opened", 4, "dave", 0.0),
            ("pr_comment", 4, "maintainer", 14400.0),
        ]);
        assert_eq!(newcomer_response(&stream), None);
    }

    #[test]
    fn the_median_is_lower_middle_not_interpolated() {
        // Six samples: 1h, 2h, 3h, 4h, 5h, 200h. The lower-middle is 3h; an interpolating median over the two
        // middle values would say 3.5, a response time nobody had.
        let stream = events(&[
            ("pr_opened", 1, "alice", 0.0),
            ("pr_comment", 1, "m", 3600.0),
            ("pr_opened", 2, "bob", 0.0),
            ("pr_comment", 2, "m", 7200.0),
            ("pr_opened", 3, "carol", 0.0),
            ("pr_comment", 3, "m", 10800.0),
            ("pr_opened", 4, "dave", 0.0),
            ("pr_comment", 4, "m", 14400.0),
            ("pr_opened", 5, "erin", 0.0),
            ("pr_comment", 5, "m", 18000.0),
            ("pr_opened", 6, "frank", 0.0),
            ("pr_comment", 6, "m", 720000.0),
        ]);
        let (median, sample) = newcomer_response(&stream).expect("six samples");
        assert_eq!(sample, 6);
        assert_eq!(median, 3.0);
    }

    #[test]
    fn the_first_pull_request_is_the_earliest_opening_not_the_first_seen() {
        // alice's SECOND-arriving opening is the earlier one: a re-delivered webhook must not make her a
        // newcomer twice, nor make her first pull request look newer than it was.
        let stream = events(&[
            ("pr_opened", 7, "alice", 50000.0),
            ("pr_opened", 1, "alice", 0.0),
            ("pr_comment", 1, "m", 3600.0),
            ("pr_comment", 7, "m", 50000.0 + 360000.0),
        ]);
        assert_eq!(newcomer_response(&stream), None, "one sample only");
    }
}
