//! Three reads an operator page stands on: the admin ledger, the rising table, and the instance-wide lists.
//!
//! They are one module because they share one rule that is easy to get wrong twice: **a missing measurement is not a zero, and an
//! empty set is not null.** The reference gets half of each — its ledger answers `null` for an empty page (a wart its own comment
//! documents) and its health column is a placeholder 0 for every project on the live instance. This module answers `[]` and
//! `None`.

use sqlx::PgPool;

/// A deliberate defect reachable only by environment variable, so a probe's control does not cost a rebuild. Nothing in the
/// product sets it; each name is asserted to make one specific probe check fail.
fn mutant(name: &str) -> bool {
    std::env::var("CONCORD_MUTANT")
        .unwrap_or_default()
        .split(',')
        .any(|candidate| candidate.trim() == name)
}

/// One admin action, as §9.3 publishes it.
#[derive(Debug, Clone, serde::Serialize)]
pub struct AdminLedgerEntry {
    pub id: i64,
    pub actor_id: i64,
    pub action: String,
    pub subject: String,
    pub justification: String,
    pub project_id: i64,
    pub created_at: f64,
}

/// The most rows any caller can get in one page.
pub const ADMIN_LEDGER_MAX: i64 = 500;

/// The default page size, which is also what an absurd `limit` silently becomes.
pub const ADMIN_LEDGER_DEFAULT: i64 = 100;

/// The public admin-action ledger, newest first.
///
/// No project filter and no role gate, on purpose: §9.3 makes admin actions publicly visible because an admin with no content
/// authority is only a real constraint when anyone can watch them.
///
/// `limit` is clamped rather than refused — `<= 0` or `> 500` becomes the default — so a viewer typed at by hand never 400s on a
/// typo, and a caller that asks for a million learns the ceiling from the size of the page instead of from an error it has to
/// special-case.
///
/// # Errors
/// `sqlx::Error`.
pub async fn admin_ledger(pool: &PgPool, limit: Option<i64>) -> Result<Vec<AdminLedgerEntry>, sqlx::Error> {
    let limit = match limit {
        Some(value) if (1..=ADMIN_LEDGER_MAX).contains(&value) => value,
        _ => ADMIN_LEDGER_DEFAULT,
    };
    let rows = sqlx::query_as::<_, (i64, Option<i64>, String, String, Option<String>, Option<i64>, f64)>(
        "select id, actor_id, action, subject, justification, project_id,
                extract(epoch from created_at)::double precision
           from admin_ledger
          order by created_at desc, id desc
          limit $1",
    )
    .bind(limit)
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(|(id, actor_id, action, subject, justification, project_id, created_at)| AdminLedgerEntry {
            id,
            actor_id: actor_id.unwrap_or_default(),
            action,
            subject,
            // An empty justification is an empty string: `null` would render as "missing" for an action that needs none.
            justification: justification.unwrap_or_default(),
            project_id: project_id.unwrap_or_default(),
            created_at,
        })
        .collect())
}

/// One row of §4.5.5.7's rising table.
#[derive(Debug, Clone, serde::Serialize)]
pub struct RisingRow {
    pub project_id: i64,
    pub slug: String,
    pub name: String,
    /// The span between the two snapshots the slope was taken over. Its own number, because a slope over 14 days and one over 90
    /// are different claims.
    pub days: i64,
    pub features_shipped: i64,
    pub complaints_filed: i64,
    pub features_per_week: f64,
    pub complaints_per_week: f64,
    /// A LEVEL shown beside the slope, never a substitute for it. `None` when nothing was measured.
    pub health_score: Option<f64>,
}

/// The shortest series that can carry a slope.
pub const RISING_MIN_DAYS: i64 = 14;

/// Projects ordered by how much organic work they gained per week between their own first and last snapshot.
///
/// A project whose series is shorter than [`RISING_MIN_DAYS`] is **absent**: below the floor there is no slope, and a `0.0` would
/// read as "flat" rather than as "not measurable". Private projects never appear.
///
/// # Errors
/// `sqlx::Error`.
pub async fn rising(pool: &PgPool) -> Result<Vec<RisingRow>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (i64, String, String, i64, i64, i64)>(
        // `day` is a text column in this schema, so the span is a date subtraction, not a numeric one.
        "with span as (
             select project_id, min(day) as first_day, max(day) as last_day
               from project_stats group by project_id
         )
         select p.id, p.slug, p.name,
                (s.last_day::date - s.first_day::date)::bigint as days,
                l.organic_features_shipped - f.organic_features_shipped,
                l.organic_complaints_filed - f.organic_complaints_filed
           from span s
           join project_stats f on f.project_id = s.project_id and f.day = s.first_day
           join project_stats l on l.project_id = s.project_id and l.day = s.last_day
           join projects p on p.id = s.project_id
          where p.visibility = 'public'
            and (s.last_day::date - s.first_day::date) >= case when $1 < 0 then 0 else $1 end
          order by (l.organic_features_shipped - f.organic_features_shipped)
                   / greatest((s.last_day::date - s.first_day::date)::double precision / 7, 1) desc,
                   p.id",
    )
    .bind(if mutant("rising-short-series") { -1 } else { RISING_MIN_DAYS as i32 })
    .fetch_all(pool)
    .await?;
    let mut out = Vec::with_capacity(rows.len());
    for (project_id, slug, name, days, features_shipped, complaints_filed) in rows {
        let weeks = days as f64 / 7.0;
        out.push(RisingRow {
            project_id,
            slug,
            name,
            days,
            features_shipped,
            complaints_filed,
            features_per_week: features_shipped as f64 / weeks,
            complaints_per_week: complaints_filed as f64 / weeks,
            health_score: project_health(pool, project_id).await?,
        });
    }
    Ok(out)
}

/// A project's health score, or `None` when nothing about it was ever measured.
///
/// **Recomputed, not read from `project_metrics.health_score`.** Project creation inserts an EMPTY metrics row for every project, so
/// the row's existence says nothing about whether anything was measured — and the live instance's stored score is 0 for every
/// project. Reading that column would publish a confident "0" for the whole catalogue, which is the §6.3 trap one layer down: every
/// brand-new project reads as "measured, mediocre".
///
/// So: no row, or a row with no signal in it, is unknown. Only a row carrying at least one real measurement produces a number.
///
/// # Errors
/// `sqlx::Error`.
pub async fn project_health(pool: &PgPool, project_id: i64) -> Result<Option<f64>, sqlx::Error> {
    let row = sqlx::query_as::<_, (Option<i64>, Option<f64>, Option<f64>, Option<i64>)>(
        "select contributors,
                extract(epoch from last_commit_at)::double precision,
                median_review_hours, releases_90d
           from project_metrics where project_id = $1",
    )
    .bind(project_id)
    .fetch_optional(pool)
    .await?;
    let Some((contributors, last_commit, median_review, releases)) = row else {
        return Ok(None);
    };
    if mutant("health-from-column") {
        // Control: read the stored column, which project creation writes as a placeholder 0 for every project.
        return Ok(
            sqlx::query_scalar::<_, Option<f64>>(
                "select health_score::double precision from project_metrics where project_id = $1",
            )
            .bind(project_id)
            .fetch_one(pool)
            .await?,
        );
    }
    // "Measured" can only be judged on the columns that CAN be null. `contributors` and `releases_90d` are `NOT NULL default 0` in
    // this schema, so a placeholder row carries 0 there and 0 is indistinguishable from a real measurement of zero — the reference
    // has the same blind spot, which is why its own comment says the live instance's scores are all 0. The two nullable columns are
    // `last_commit_at` and `median_review_hours`, so a row with neither is a row nothing was measured into.
    //
    // Decision (dated 2026-11-14): measured means `last_commit_at is not null or median_review_hours is not null`. Everything else
    // is unknown, because nothing else in this table can say "never measured".
    if last_commit.is_none() && median_review.is_none() {
        return Ok(None);
    }
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|duration| duration.as_secs_f64())
        .unwrap_or_default();
    let metrics = concord_ranking::health::Metrics {
        last_commit_age_days: last_commit.map_or(f64::MAX.sqrt(), |at| ((now - at) / 86_400.0).max(0.0)),
        median_review_hours: median_review.unwrap_or(concord_ranking::health::UNKNOWN_REVIEW_HOURS),
        contributors: contributors.unwrap_or_default(),
        releases_90d: releases.unwrap_or_default(),
    };
    Ok(Some(
        concord_ranking::health::health_score(metrics, concord_ranking::health::DEFAULT_WEIGHTS).total,
    ))
}

/// One list, in the reference's shape: the column is `title`, the JSON key is `name`.
#[derive(Debug, Clone, serde::Serialize)]
pub struct InstanceList {
    pub id: i64,
    /// `None` for an instance-wide list, which is a first-class scope and not a missing project.
    pub project_id: Option<i64>,
    pub slug: String,
    #[serde(rename = "name")]
    pub title: String,
    pub description: String,
    pub status: String,
    pub created_at: f64,
    pub updated_at: f64,
}

/// The lists that belong to no project.
///
/// The scope is what makes a "good resources" list useful to every project at once, so these are a scope of their own rather than
/// the absence of one.
///
/// # Errors
/// `sqlx::Error`.
pub async fn instance_lists(pool: &PgPool) -> Result<Vec<InstanceList>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (i64, Option<i64>, String, String, String, String, f64, f64)>(
        "select id, project_id, slug, title, coalesce(description, ''), status,
                extract(epoch from created_at)::double precision,
                extract(epoch from updated_at)::double precision
           from lists
          where project_id is null
          order by updated_at desc, id desc",
    )
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(
            |(id, project_id, slug, title, description, status, created_at, updated_at)| InstanceList {
                id,
                project_id,
                slug,
                title,
                description,
                status,
                created_at,
                updated_at,
            },
        )
        .collect())
}
