//! `project_metrics`: the one place a health measurement is written, and the four rules that keep it honest.
//!
//! The score itself is [`concord_ranking::health::health_score`] — this module owns the *write*, and the writes
//! are where the numbers become facts that other readers (`search`'s ORDER BY, the board, an export) trust.
//!
//! * **A failed measurement writes NOTHING.** Not zeros, not the previous value with a new `computed_at`: a row
//!   that says "computed 2 minutes ago" while every number in it is from last week is worse than a stale row,
//!   because it hides that it is stale.
//! * **An unknown review time is `NULL`, not a negative sentinel.** The column is `REAL` and every other reader
//!   treats a number as a measurement; writing `-1` would publish "minus one hours to first review" to anything
//!   that does not know the convention.
//! * **A repository with no commit gets `NULL` for `last_commit_at`**, for the same reason.
//! * **One scorer.** `health_score` is computed here, from the same metrics that are stored, so a row can never
//!   disagree with its own inputs.

use sqlx::PgPool;

use concord_ranking::health::{health_score, Metrics, DEFAULT_WEIGHTS};

pub use concord_ranking::health::UNKNOWN_REVIEW_HOURS;

/// One forge measurement, as the sync reads it.
#[derive(Debug, Clone)]
pub struct Measurement {
    /// The project.
    pub slug: String,
    /// Whether the forge answered. A `false` here is a failure and writes nothing at all.
    pub measured: bool,
    /// Stars.
    pub stars: i64,
    /// Forks.
    pub forks: i64,
    /// Open issues.
    pub open_issues: i64,
    /// Commits on the default branch.
    pub commit_count: i64,
    /// Distinct authors in the last year.
    pub contributors: i64,
    /// Days since the last commit, or [`UNKNOWN_AGE`] when the repository has none.
    pub last_commit_age_days: f64,
    /// Median hours to first response, or [`UNKNOWN_REVIEW_HOURS`].
    pub median_review_hours: f64,
    /// Releases or tags in the last 90 days.
    pub releases_90d: i64,
    /// The repository's licence, when the forge reports one.
    pub license: Option<String>,
}

/// The sentinel for "this repository has no commit at all". A value at or above this is not an age.
pub const UNKNOWN_AGE: f64 = 1e12;

/// What one sync did.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Synced {
    /// The health score written.
    pub health_score: f64,
    /// Whether the review time was measured.
    pub responsiveness_known: bool,
    /// Whether a commit timestamp was written.
    pub commit_time_known: bool,
}

/// Why a measurement was not written.
#[derive(Debug, Clone, PartialEq)]
pub enum Skipped {
    /// The forge did not answer: nothing is written.
    Unmeasured,
    /// No project has that slug.
    UnknownProject,
}

/// Write one measurement over the project's `project_metrics` row, or explain why not.
///
/// # Errors
/// `sqlx::Error`.
pub async fn sync(pool: &PgPool, measurement: &Measurement) -> Result<Result<Synced, Skipped>, sqlx::Error> {
    if !measurement.measured {
        return Ok(Err(Skipped::Unmeasured));
    }
    let Some(project_id) = sqlx::query_scalar::<_, i64>("select id from projects where slug = $1")
        .bind(&measurement.slug)
        .fetch_optional(pool)
        .await?
    else {
        return Ok(Err(Skipped::UnknownProject));
    };
    let metrics = Metrics {
        last_commit_age_days: measurement.last_commit_age_days,
        median_review_hours: measurement.median_review_hours,
        contributors: measurement.contributors,
        releases_90d: measurement.releases_90d,
    };
    let breakdown = health_score(metrics, DEFAULT_WEIGHTS);
    let now = crate::now_unix();
    let commit_known = measurement.last_commit_age_days < UNKNOWN_AGE;
    let last_commit_at = if commit_known {
        Some(now - measurement.last_commit_age_days * 86400.0)
    } else {
        None
    };
    let review = if measurement.median_review_hours >= 0.0 {
        Some(measurement.median_review_hours)
    } else {
        None
    };
    sqlx::query(
        "insert into project_metrics (project_id, stars, forks, open_issues, commit_count, contributors,
            last_commit_at, median_review_hours, releases_90d, health_score, computed_at, newcomer_response_hours)
         values ($1, $2, $3, $4, $5, $6, case when $7::double precision is null then null
                else to_timestamp($7::double precision) end, $8, $9, $10, to_timestamp($11), null)
         on conflict (project_id) do update set
            stars = excluded.stars, forks = excluded.forks, open_issues = excluded.open_issues,
            commit_count = excluded.commit_count, contributors = excluded.contributors,
            last_commit_at = excluded.last_commit_at,
            median_review_hours = excluded.median_review_hours,
            releases_90d = excluded.releases_90d, health_score = excluded.health_score,
            computed_at = excluded.computed_at",
    )
    .bind(project_id)
    .bind(measurement.stars)
    .bind(measurement.forks)
    .bind(measurement.open_issues)
    .bind(measurement.commit_count)
    .bind(measurement.contributors)
    .bind(last_commit_at)
    .bind(review)
    .bind(measurement.releases_90d)
    .bind(breakdown.total)
    .bind(now)
    .execute(pool)
    .await?;
    if let Some(license) = &measurement.license {
        sqlx::query("update projects set license = $1 where id = $2 and license is distinct from $1")
            .bind(license)
            .bind(project_id)
            .execute(pool)
            .await?;
    }
    Ok(Ok(Synced {
        health_score: breakdown.total,
        responsiveness_known: breakdown.responsiveness_known,
        commit_time_known: commit_known,
    }))
}

/// Write the newcomer responsiveness columns, which come from `forge_events` rather than from the forge's API.
///
/// `None` writes `NULL` hours and a sample of `0`. The table declares `newcomer_sample bigint NOT NULL DEFAULT 0`
/// — the "no number" state is a null median over an empty sample, and writing `NULL` there is refused by the
/// schema the Go tree created. The two columns are still one fact and are written together.
///
/// # Errors
/// `sqlx::Error`.
pub async fn write_newcomer(
    pool: &PgPool,
    slug: &str,
    response: Option<(f64, usize)>,
) -> Result<(), sqlx::Error> {
    let hours = response.map(|(hours, _)| hours);
    let sample = response.map_or(0, |(_, sample)| sample as i64);
    sqlx::query(
        "update project_metrics set newcomer_response_hours = $1, newcomer_sample = $2
          where project_id = (select id from projects where slug = $3)",
    )
    .bind(hours)
    .bind(sample)
    .bind(slug)
    .execute(pool)
    .await?;
    Ok(())
}

/// Read one project's stored metrics, for a probe or an operator.
///
/// # Errors
/// `sqlx::Error`.
pub async fn stored(pool: &PgPool, slug: &str) -> Result<Option<Stored>, sqlx::Error> {
    let row = sqlx::query_as::<_, (f64, Option<f64>, Option<f64>, Option<f64>, Option<i64>)>(
        "select health_score, median_review_hours,
                extract(epoch from last_commit_at)::double precision,
                newcomer_response_hours, newcomer_sample
           from project_metrics where project_id = (select id from projects where slug = $1)",
    )
    .bind(slug)
    .fetch_optional(pool)
    .await?;
    Ok(row.map(|(health, review, commit, newcomer, sample)| Stored {
        health_score: health,
        median_review_hours: review,
        last_commit_at: commit,
        newcomer_response_hours: newcomer,
        newcomer_sample: sample,
    }))
}

/// What is stored, with the unknown values as `None`.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Stored {
    /// The stored score.
    pub health_score: f64,
    /// The stored review median, `None` when it was not measured.
    pub median_review_hours: Option<f64>,
    /// The stored commit time in unix seconds, `None` when the repository has no commit.
    pub last_commit_at: Option<f64>,
    /// The newcomer median, `None` below the sample floor.
    pub newcomer_response_hours: Option<f64>,
    /// The sample the newcomer median was taken over.
    pub newcomer_sample: Option<i64>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_age_sentinel_is_not_a_plausible_age() {
        // 1e12 days is 2.7 billion years: no repository has one, so the sentinel cannot collide with a real
        // measurement, and the comparison is `<`, not `<=`, so a value exactly at it is unknown.
        assert!(UNKNOWN_AGE > 365.0 * 1000.0);
        assert!(!(UNKNOWN_AGE < UNKNOWN_AGE));
    }

    #[test]
    fn an_unmeasured_review_time_stays_negative() {
        // The store turns this into NULL; the constant must therefore be negative, or the write path would
        // publish a fake measurement.
        assert!(UNKNOWN_REVIEW_HOURS < 0.0);
    }
}
