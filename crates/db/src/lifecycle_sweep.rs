//! The dormancy sweep: the first thing in this roadmap that is a JOB rather than a page (step 5).
//!
//! # What a sweep may and may not claim
//!
//! `concord_ranking::lifecycle` already decided what dormancy MEANS — ninety measured days without a commit — and this module's only job is
//! to ask it repeatedly and record the answer once. So the rule that matters is inherited rather than restated: **a project whose repository
//! has never been read is never dormant.** `None` is not ninety days of silence, it is one absence of measurement, and a sweep that confused
//! the two would mark 68 of this instance's 73 projects dead because nobody has stated where their code lives.
//!
//! # Why the act is an `audit_log` row and not a column
//!
//! The stage is computed, so there is nothing to write into the stage itself. What a sweep does write is the ACT — "this instance observed
//! this project dormant on this date" — and that is an append-only fact, which is what `audit_log` is for. Writing it rather than a
//! `projects.dormant_at` column also makes the job idempotent without a state machine: an act that already exists is not repeated.
//!
//! The consequence is stated because it is a real limitation: a project that goes quiet, is marked, comes back, and goes quiet again is
//! marked ONCE. Re-marking is a decision (a second act with a different meaning), and the sweep does not invent one.

use sqlx::{PgPool, Row};

/// The action name written to `audit_log.action`. Stable, because a reader (and the report) keys on it.
pub const ACTION: &str = "project.dormancy";

/// What one sweep found.
#[derive(Debug, Default)]
pub struct SweepReport {
    /// Projects that are dormant and were not already marked: `(slug, days silent)`.
    pub marked: Vec<(String, f64)>,
    /// Projects with a repository whose commit age could not be read. Reported so "0 marked" cannot hide them.
    pub unmeasured: i64,
    /// Projects considered, i.e. those with a repository at all.
    pub considered: i64,
}

/// Whether an age clears the threshold. `None` never does — see the module note.
///
/// A pure function so the rule is proved by a unit test and not by a job that has to be run against a database to be believed.
pub fn is_dormant(age_days: Option<f64>, threshold_days: f64) -> bool {
    matches!(age_days, Some(age) if age >= threshold_days)
}

/// Sweep for dormant projects.
///
/// `threshold_days` is the operator's number and it is NaN-proof by comparison: a NaN age fails `>=` and is therefore never dormant, which
/// is the right answer for an unreadable timestamp.
pub async fn sweep(pool: &PgPool, threshold_days: f64, apply: bool) -> Result<SweepReport, sqlx::Error> {
    let rows = sqlx::query(
        "select p.id, p.slug, \
                case when m.last_commit_at is null then null \
                     else extract(epoch from (now() - m.last_commit_at))::double precision / 86400.0 end as age_days \
           from projects p \
           left join project_metrics m on m.project_id = p.id \
          where p.repo_url is not null and p.repo_url <> '' \
          order by p.slug",
    )
    .fetch_all(pool)
    .await?;

    let mut report = SweepReport { considered: rows.len() as i64, ..Default::default() };
    for row in rows {
        let id: i64 = row.try_get("id")?;
        let slug: String = row.try_get("slug")?;
        let age: Option<f64> = row.try_get("age_days")?;
        if !is_dormant(age, threshold_days) {
            if age.is_none() {
                report.unmeasured += 1;
            }
            continue;
        }
        let age = age.unwrap_or_default();
        let already: i64 = sqlx::query_scalar(
            "select count(*) from audit_log where action = $1 and project_id = $2",
        )
        .bind(ACTION)
        .bind(id)
        .fetch_one(pool)
        .await?;
        if already > 0 {
            continue;
        }
        if apply {
            sqlx::query(
                "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail) \
                 values ($1, null, $2, 'project', $1, $3)",
            )
            .bind(id)
            .bind(ACTION)
            // The detail carries the MEASUREMENT, so the act explains itself a year later without re-running the query.
            .bind(format!("no commit for {age:.1} days (threshold {threshold_days:.0})"))
            .execute(pool)
            .await?;
        }
        report.marked.push((slug, age));
    }
    Ok(report)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ninety_days_clears_the_threshold_and_eighty_nine_does_not() {
        assert!(is_dormant(Some(90.0), 90.0));
        assert!(!is_dormant(Some(89.9), 90.0));
    }

    /// The rule this module exists to protect: an unmeasured project is not a dead one.
    #[test]
    fn an_unmeasured_project_is_never_dormant() {
        assert!(!is_dormant(None, 0.0));
        assert!(!is_dormant(None, 1.0));
    }

    /// NaN is not a measurement either, and `>=` already answers correctly for it — asserted so a future refactor to `!(age < t)` cannot
    /// turn an unreadable timestamp into a dormancy.
    #[test]
    fn an_unreadable_timestamp_is_not_dormant() {
        assert!(!is_dormant(Some(f64::NAN), 90.0));
    }

    #[test]
    fn a_zero_threshold_is_a_real_setting_not_a_disabled_one() {
        // `proposalsweep` treats ttl 0 as "disabled"; this one must not, because every measured project genuinely is dormant at a
        // zero-day threshold and an operator asking for that should see them.
        assert!(is_dormant(Some(0.0), 0.0));
        assert!(!is_dormant(None, 0.0));
    }
}
