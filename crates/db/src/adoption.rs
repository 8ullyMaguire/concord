//! The adoption pool: projects the lifecycle has already measured dormant, in the order somebody looking for one to take on would want
//! them (roadmap step 5).
//!
//! # Why this is a query and not a table
//!
//! Dormancy is a measurement, computed on read by `concord_ranking::lifecycle`. A stored pool would go stale the moment a project got a
//! commit, and the one thing an adoption list must never do is offer a project that is alive. So the pool is a query, and its emptiness is
//! a measurement too — which is why it returns counts as well as candidates.
//!
//! # Why the threshold is not written here
//!
//! `Stage::Dormant` already owns the question "is this abandoned", with its own constant and its own tests. A second threshold in this file
//! would be a second answer, and the first time they disagreed the badge would say alive while the pool offered the project.

use sqlx::{PgPool, Row};

use crate::projects::{project_from_row, Project, PROJECT_COLUMNS};

/// What the page needs to be honest: the candidates, and what was and was not measured.
#[derive(Debug, Default)]
pub struct Pool {
    /// Measured dormant, longest silence first.
    pub candidates: Vec<Candidate>,
    /// Projects whose commit history this instance has actually read. The denominator that makes an empty pool mean something: a
    /// catalogue where nothing was measured is not a catalogue where nothing is abandoned.
    pub measured: i64,
    /// Projects in the catalogue.
    pub total: i64,
    /// The longest silence among the measured, in days. `None` when nothing is measured: an empty pool and an unmeasured catalogue must
    /// not read the same on the page.
    pub longest_silence: Option<f64>,
}

/// One adoptable project, with the number that made it adoptable.
#[derive(Debug, Clone)]
pub struct Candidate {
    pub project: Project,
    pub days_silent: f64,
}

/// The pool, plus the two counts that make an empty pool legible.
pub async fn pool(pool: &PgPool) -> Result<Pool, sqlx::Error> {
    let sql = format!(
        "select {PROJECT_COLUMNS}, \
         case when m.last_commit_at is null then null \
              else extract(epoch from (now() - m.last_commit_at))::double precision / 86400.0 end as days_silent \
           from projects p left join project_metrics m on m.project_id = p.id \
          where p.archived_at is null \
          order by p.id"
    );
    let rows = sqlx::query(&sql).fetch_all(pool).await?;

    let mut out = Pool { total: rows.len() as i64, ..Pool::default() };
    for row in &rows {
        let project = project_from_row(row)?;
        let days_silent: Option<f64> = row.try_get("days_silent")?;
        // Counted from the measurement, not from the URL: a project can name a repository this instance has never read, and calling that
        // "measured" would put the page's denominator above the number of projects whose silence is actually known.
        if let Some(days) = days_silent {
            out.measured += 1;
            out.longest_silence = Some(out.longest_silence.map_or(days, |seen: f64| seen.max(days)));
        }
        if project.stage == "dormant" {
            out.candidates.push(Candidate {
                project,
                // A dormant project always has a measured age (a project with no repository reads `unknown`, not dormant), so this
                // fallback is unreachable — and it is a `0.0` rather than an invented number because the filter above proves the row.
                days_silent: days_silent.unwrap_or(0.0),
            });
        }
    }
    out.candidates.sort_by(|a, b| b.days_silent.total_cmp(&a.days_silent).then(a.project.slug.cmp(&b.project.slug)));
    Ok(out)
}

/// How long since this project's last measured commit, in days, or `None` when its history has never been read.
pub async fn days_silent(pool: &PgPool, project_id: i64) -> Result<Option<f64>, sqlx::Error> {
    sqlx::query_scalar(
        "select case when last_commit_at is null then null \
                     else extract(epoch from (now() - last_commit_at))::double precision / 86400.0 end \
           from project_metrics where project_id = $1",
    )
    .bind(project_id)
    .fetch_optional(pool)
    .await
    .map(|row| row.flatten())
}
