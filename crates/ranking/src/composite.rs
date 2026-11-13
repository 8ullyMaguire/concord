//! Composite ranking across criteria — `rank/composite`'s arithmetic, ported exactly.
//!
//! Five rules, all measurable, all easy to get plausibly wrong:
//!
//! 1. **Naming any weight makes that the whole query.** An unnamed ACTIVE criterion is EXCLUDED, not defaulted. The
//!    behaviour this replaced — unnamed criteria joining at their own default — made "rank by design only" blend
//!    efficiency in at equal weight, so the result matched neither criterion's ordering. An empty weight list means
//!    "rank with every active criterion's default".
//! 2. **Negative weights are dropped**, and when the surviving total is `<= 1e-9` every weight becomes **uniform**
//!    (`1/n`), not zero: zero would silently report every feature as scoring nothing.
//! 3. **Per-criterion normalisation is min–max with `1 - v` for `lower_is_better`**, and the degenerate cases answer
//!    **0.5**: one rating, or every rating equal. `1.0` for a single data point would call an untested feature the
//!    best one.
//! 4. **`require_min_rd` drops a criterion nobody has played.** A criterion whose every rating is still at the initial
//!    state contributes an arbitrary 0.5 to every feature — dilution with no vote behind it.
//! 5. **Rank is 1-based after `Score desc, feature_id asc` with a `1e-9` tolerance**, so a tie is deterministic and a
//!    "rank 3" claim is reproducible.

use std::collections::BTreeMap;

/// The rating a criterion starts at, and the one `total_confidence` reports when nothing contributed.
pub const INITIAL_RD: f64 = 350.0;

/// Which end of a criterion's scale is better.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Direction {
    /// A larger raw rating is better.
    Higher,
    /// A smaller raw rating is better — `lower_is_better`, and the `1 - v` branch below.
    Lower,
}

/// One ranking criterion.
#[derive(Debug, Clone)]
pub struct Criterion {
    /// The row.
    pub id: i64,
    /// Its slug.
    pub slug: String,
    /// Its name.
    pub name: String,
    /// Which end is better.
    pub direction: Direction,
    /// The weight it carries when the caller names none.
    pub default_weight: f64,
    /// Whether it counts.
    pub active: bool,
}

/// One criterion's rating of one feature.
#[derive(Debug, Clone, Copy)]
pub struct Rating {
    /// The feature.
    pub feature_id: i64,
    /// The rating.
    pub r: f64,
    /// Its deviation.
    pub rd: f64,
    /// Rated comparisons behind it.
    pub games: i64,
}

/// A caller's emphasis on one criterion.
#[derive(Debug, Clone, Copy)]
pub struct Weight {
    /// Which criterion.
    pub criterion_id: i64,
    /// How much.
    pub weight: f64,
}

/// Inclusion rules.
#[derive(Debug, Clone, Copy, Default)]
pub struct Options {
    /// Rank against criteria flagged inactive. Off by default: deactivating a criterion is how a project retires a
    /// dimension, and honouring it silently would make the flag a lie.
    pub include_inactive: bool,
    /// Drop criteria where every rating is unplayed.
    pub require_min_rd: bool,
}

/// One criterion's slice of a score, kept so a result can explain itself.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Contribution {
    /// Which criterion.
    pub criterion_id: i64,
    /// Its slug.
    pub slug: String,
    /// Its name.
    pub name: String,
    /// Its weight, after normalisation.
    pub weight: f64,
    /// The feature's raw rating.
    pub raw: f64,
    /// Uncertainty on that rating.
    pub rd: f64,
    /// Rated comparisons behind it.
    pub games: i64,
    /// 0..1 within this criterion.
    pub normalised: f64,
    /// `weight * normalised`.
    pub score: f64,
}

/// A ranked feature with its explanation attached.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Result_ {
    /// The feature.
    pub feature_id: i64,
    /// Its composite score.
    pub score: f64,
    /// Its 1-based rank.
    pub rank: i64,
    /// Why it scored what it did.
    pub contributions: Vec<Contribution>,
}

/// A ranked result with its confidence, which is what the route returns.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Ranked {
    /// The underlying result, flattened into this object.
    #[serde(flatten)]
    pub result: Result_,
    /// How settled the ordering is.
    pub confidence: f64,
}

/// Min–max normalise one criterion's ratings into 0..1.
///
/// The degenerate cases are 0.5 and not 1.0: one rating, or a span that rounds to zero.
#[must_use]
pub fn normalised(ratings: &[Rating], direction: Direction) -> BTreeMap<i64, f64> {
    let mut out = BTreeMap::new();
    if ratings.is_empty() {
        return out;
    }
    if ratings.len() == 1 {
        out.insert(ratings[0].feature_id, 0.5);
        return out;
    }
    let mut lo = f64::INFINITY;
    let mut hi = f64::NEG_INFINITY;
    for rating in ratings {
        lo = lo.min(rating.r);
        hi = hi.max(rating.r);
    }
    let span = hi - lo;
    if span <= 1e-9 {
        for rating in ratings {
            out.insert(rating.feature_id, 0.5);
        }
        return out;
    }
    for rating in ratings {
        let mut value = (rating.r - lo) / span;
        if direction == Direction::Lower {
            value = 1.0 - value;
        }
        out.insert(rating.feature_id, value);
    }
    out
}

/// Scale weights to sum to 1.
///
/// Negative weights are dropped and a non-positive total becomes UNIFORM, not zero: zero would report every feature
/// as scoring nothing, which is a different and false answer.
#[must_use]
pub fn normalise_weights(weights: &[Weight]) -> BTreeMap<i64, f64> {
    let mut out = BTreeMap::new();
    let mut total = 0.0;
    for weight in weights {
        if weight.weight < 0.0 {
            continue;
        }
        total += weight.weight;
        out.insert(weight.criterion_id, weight.weight);
    }
    if out.is_empty() {
        return out;
    }
    if total <= 1e-9 {
        let uniform = 1.0 / out.len() as f64;
        for value in out.values_mut() {
            *value = uniform;
        }
        return out;
    }
    for value in out.values_mut() {
        *value /= total;
    }
    out
}

/// The weighted mean deviation across contributions — how settled this ordering is.
#[must_use]
pub fn total_confidence(contributions: &[Contribution]) -> f64 {
    if contributions.is_empty() {
        return INITIAL_RD;
    }
    let mut weight_sum = 0.0;
    let mut accumulated = 0.0;
    for contribution in contributions {
        weight_sum += contribution.weight;
        accumulated += contribution.weight * contribution.rd;
    }
    if weight_sum <= 1e-9 {
        accumulated = 0.0;
        for contribution in contributions {
            accumulated += contribution.rd;
        }
        return accumulated / contributions.len() as f64;
    }
    accumulated / weight_sum
}

/// Rank features across criteria.
#[must_use]
pub fn composite(
    criteria: &[Criterion],
    ratings: &BTreeMap<i64, Vec<Rating>>,
    weights: &[Weight],
    options: Options,
) -> Vec<Result_> {
    let mut active: Vec<&Criterion> = Vec::with_capacity(criteria.len());
    for criterion in criteria {
        if !criterion.active && !options.include_inactive {
            continue;
        }
        if options.require_min_rd {
            let played = ratings
                .get(&criterion.id)
                .map(|rows| {
                    rows.iter()
                        .any(|rating| rating.games > 0 || rating.rd < INITIAL_RD - 1e-9)
                })
                .unwrap_or(false);
            if !played {
                continue;
            }
        }
        active.push(criterion);
    }
    if active.is_empty() {
        return Vec::new();
    }

    // Naming a weight makes that list the whole query.
    let mut caller: BTreeMap<i64, f64> = BTreeMap::new();
    for weight in weights {
        caller.insert(weight.criterion_id, weight.weight);
    }
    let restrict = !caller.is_empty();
    let mut effective: Vec<Weight> = Vec::with_capacity(active.len());
    for criterion in &active {
        let weight = match caller.get(&criterion.id) {
            Some(value) => *value,
            None => {
                if restrict {
                    continue;
                }
                criterion.default_weight
            }
        };
        effective.push(Weight {
            criterion_id: criterion.id,
            weight,
        });
    }
    let normalised_weights = normalise_weights(&effective);

    let mut scores: BTreeMap<i64, Result_> = BTreeMap::new();
    for criterion in &active {
        let rows = ratings.get(&criterion.id).cloned().unwrap_or_default();
        let norms = normalised(&rows, criterion.direction);
        let weight = normalised_weights.get(&criterion.id).copied().unwrap_or(0.0);
        if weight <= 0.0 {
            // An explicit zero weight: record nothing, contribute nothing.
            continue;
        }
        for rating in &rows {
            let entry = scores.entry(rating.feature_id).or_insert_with(|| Result_ {
                feature_id: rating.feature_id,
                score: 0.0,
                rank: 0,
                contributions: Vec::new(),
            });
            let value = norms.get(&rating.feature_id).copied().unwrap_or(0.0);
            entry.score += weight * value;
            entry.contributions.push(Contribution {
                criterion_id: criterion.id,
                slug: criterion.slug.clone(),
                name: criterion.name.clone(),
                weight,
                raw: rating.r,
                rd: rating.rd,
                games: rating.games,
                normalised: value,
                score: weight * value,
            });
        }
    }

    let mut out: Vec<Result_> = scores
        .into_values()
        .filter(|result| !result.contributions.is_empty())
        .collect();
    out.sort_by(|left, right| {
        if (left.score - right.score).abs() > 1e-9 {
            right
                .score
                .partial_cmp(&left.score)
                .unwrap_or(std::cmp::Ordering::Equal)
        } else {
            left.feature_id.cmp(&right.feature_id)
        }
    });
    for (index, result) in out.iter_mut().enumerate() {
        result.rank = i64::try_from(index).unwrap_or(0) + 1;
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn criterion(id: i64, direction: Direction, default_weight: f64) -> Criterion {
        Criterion {
            id,
            slug: format!("c{id}"),
            name: format!("C{id}"),
            direction,
            default_weight,
            active: true,
        }
    }

    #[test]
    fn a_single_rating_normalises_to_a_half_and_not_to_a_win() {
        let rows = vec![Rating {
            feature_id: 1,
            r: 1800.0,
            rd: 100.0,
            games: 5,
        }];
        assert_eq!(normalised(&rows, Direction::Higher).get(&1), Some(&0.5));
    }

    #[test]
    fn lower_is_better_reverses_the_scale() {
        let rows = vec![
            Rating {
                feature_id: 1,
                r: 10.0,
                rd: 100.0,
                games: 3,
            },
            Rating {
                feature_id: 2,
                r: 20.0,
                rd: 100.0,
                games: 3,
            },
        ];
        let higher = normalised(&rows, Direction::Higher);
        let lower = normalised(&rows, Direction::Lower);
        assert_eq!((higher[&1], higher[&2]), (0.0, 1.0));
        assert_eq!((lower[&1], lower[&2]), (1.0, 0.0));
    }

    #[test]
    fn naming_one_weight_excludes_the_criteria_it_did_not_name() {
        let criteria = vec![criterion(1, Direction::Higher, 1.0), criterion(2, Direction::Higher, 1.0)];
        let mut ratings = BTreeMap::new();
        ratings.insert(
            1,
            vec![Rating {
                feature_id: 10,
                r: 1600.0,
                rd: 200.0,
                games: 2,
            }],
        );
        ratings.insert(
            2,
            vec![Rating {
                feature_id: 10,
                r: 1600.0,
                rd: 200.0,
                games: 2,
            }],
        );
        let named = composite(
            &criteria,
            &ratings,
            &[Weight {
                criterion_id: 1,
                weight: 1.0,
            }],
            Options::default(),
        );
        // The excluded criterion contributes nothing, so the explanation has one row and not two.
        assert_eq!(named[0].contributions.len(), 1);
        let unnamed = composite(&criteria, &ratings, &[], Options::default());
        assert_eq!(unnamed[0].contributions.len(), 2);
    }

    #[test]
    fn a_non_positive_weight_total_becomes_uniform_and_not_zero() {
        let uniform = normalise_weights(&[
            Weight {
                criterion_id: 1,
                weight: 0.0,
            },
            Weight {
                criterion_id: 2,
                weight: 0.0,
            },
        ]);
        assert_eq!(uniform[&1], 0.5);
        assert_eq!(uniform[&2], 0.5);
        // Negative weights are dropped entirely, so this is one criterion at 1.0.
        let dropped = normalise_weights(&[
            Weight {
                criterion_id: 1,
                weight: 2.0,
            },
            Weight {
                criterion_id: 2,
                weight: -1.0,
            },
        ]);
        assert_eq!(dropped.len(), 1);
        assert_eq!(dropped[&1], 1.0);
    }
}
