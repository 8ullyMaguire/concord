//! `statsnapshot`: today's row in the series, plus one row per project.
//!
//! One pass writes both tables, and that is the point: a day with the instance row and no project rows would be
//! a day whose medians and whose slope describe different moments, and two surfaces read them side by side.
//!
//! The medians are NOT recomputed here. They come from [`crate::stats`], the same code `GET /api/v1/stats`
//! answers from, because a snapshot that computed them its own way would be a second answer to the same
//! question — and the two would eventually disagree.
//!
//! Two counting rules, each of which makes a series lie:
//!
//! * **`seed` is `origin IN ('seed','import')`, and `unknown` is neither.** A two-way split reports the
//!   pre-backfill population as organic, which flatters the instance on exactly the day it is least true.
//! * **"filed" is every organic complaint whatever its status.** Filtering on status would make the series fall
//!   when a maintainer closed one — a slope that goes down because the work was done.

use sqlx::PgPool;

use crate::stats;

/// One row of the per-project series.
pub struct ProjectSnapshot {
    /// The project.
    pub project_id: i64,
    /// Features shipped that a writer naming its source created.
    pub features_shipped: i64,
    /// Complaints filed by such a writer, whatever their status now.
    pub complaints_filed: i64,
}

/// The instance row for one day.
pub struct Snapshot {
    /// `YYYY-MM-DD`.
    pub day: String,
    /// Projects.
    pub projects: i64,
    /// Projects with a computed health score.
    pub projects_measured: i64,
    /// Features.
    pub features_total: i64,
    /// Features from a batch.
    pub features_seed: i64,
    /// Complaints.
    pub complaints_total: i64,
    /// Complaints from a batch.
    pub complaints_seed: i64,
    /// Triage median, or `None` when the day has no sample.
    pub triage_median_h: Option<f64>,
    /// Triage sample.
    pub triage_n: i64,
    /// Delivery median, or `None`.
    pub delivery_median_h: Option<f64>,
    /// Delivery sample.
    pub delivery_n: i64,
    /// Decision median, or `None`.
    pub decision_median_h: Option<f64>,
    /// Decision sample.
    pub decision_n: i64,
    /// Features with no provenance at all.
    pub no_origin: i64,
    /// When this row was computed, unix seconds.
    pub computed_at: f64,
}

/// `count(*)` and the seeded share of one table.
async fn count_and_seed(pool: &PgPool, table: &str) -> Result<(i64, i64), sqlx::Error> {
    let sql = format!(
        "select count(*), sum(case when origin in ('seed','import') then 1 else 0 end) from {table}"
    );
    let row = sqlx::query(&sql).fetch_one(pool).await?;
    let total: i64 = sqlx::Row::try_get(&row, 0)?;
    let seed: Option<i64> = sqlx::Row::try_get(&row, 1)?;
    Ok((total, seed.unwrap_or(0)))
}

/// Compute one day's instance row.
///
/// # Errors
/// `sqlx::Error`.
pub async fn compute(pool: &PgPool, day: &str) -> Result<Snapshot, sqlx::Error> {
    let measured = stats::measured(pool).await?;
    let (features_total, features_seed) = count_and_seed(pool, "features").await?;
    let (complaints_total, complaints_seed) = count_and_seed(pool, "complaints").await?;
    let triage = stats::triage_latency(pool).await?;
    let delivery = stats::delivery_latency(pool).await?;
    let decision = stats::decision_latency(pool).await?;
    // A median with no sample is NULL, never zero: a zero is the best possible answer and the easiest number in
    // the world to fake by accident.
    let median = |latency: &stats::Latency| {
        if latency.n == 0 {
            None
        } else {
            Some(latency.median_hours)
        }
    };
    let no_origin: i64 = sqlx::query_scalar("select count(*) from features where origin = 'unknown'")
        .fetch_one(pool)
        .await?;
    Ok(Snapshot {
        day: day.to_string(),
        projects: measured.projects,
        projects_measured: measured.with_health,
        features_total,
        features_seed,
        complaints_total,
        complaints_seed,
        triage_median_h: median(&triage),
        triage_n: triage.n,
        delivery_median_h: median(&delivery),
        delivery_n: delivery.n,
        decision_median_h: median(&decision),
        decision_n: decision.n,
        no_origin,
        computed_at: crate::now_unix(),
    })
}

/// Per-project cumulative organic counts.
///
/// NOT visibility-filtered: a private project's numbers are a fact about the project, and filtering here would
/// leave it permanently unrecorded. Publishing is a separate question, answered by the leaderboard.
///
/// # Errors
/// `sqlx::Error`.
pub async fn compute_projects(pool: &PgPool) -> Result<Vec<ProjectSnapshot>, sqlx::Error> {
    let mut by_project: std::collections::BTreeMap<i64, ProjectSnapshot> = Default::default();
    let shipped = sqlx::query(
        "select project_id, count(*) from features \
          where status = 'shipped' and origin in ('api','ui') group by project_id",
    )
    .fetch_all(pool)
    .await?;
    for row in shipped {
        let id: i64 = sqlx::Row::try_get(&row, 0)?;
        let count: i64 = sqlx::Row::try_get(&row, 1)?;
        by_project.entry(id).or_insert(ProjectSnapshot {
            project_id: id,
            features_shipped: 0,
            complaints_filed: 0,
        });
        by_project.get_mut(&id).expect("just inserted").features_shipped = count;
    }
    let filed = sqlx::query(
        "select project_id, count(*) from complaints where origin in ('api','ui') group by project_id",
    )
    .fetch_all(pool)
    .await?;
    for row in filed {
        let id: i64 = sqlx::Row::try_get(&row, 0)?;
        let count: i64 = sqlx::Row::try_get(&row, 1)?;
        by_project.entry(id).or_insert(ProjectSnapshot {
            project_id: id,
            features_shipped: 0,
            complaints_filed: 0,
        });
        by_project.get_mut(&id).expect("just inserted").complaints_filed = count;
    }
    Ok(by_project.into_values().collect())
}

/// Write the instance row, replacing any earlier row for the same day.
///
/// The upsert is what makes the job idempotent: the timer reruns it, and a second run must correct the day's
/// numbers rather than fail on a duplicate key.
///
/// # Errors
/// `sqlx::Error`.
pub async fn write(pool: &PgPool, snapshot: &Snapshot) -> Result<(), sqlx::Error> {
    sqlx::query(
        "insert into instance_stats (day, projects, projects_measured, features_total, features_seed, \
            complaints_total, complaints_seed, triage_median_h, triage_n, delivery_median_h, delivery_n, \
            decision_median_h, decision_n, no_origin, computed_at) \
         values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,to_timestamp($15)) \
         on conflict (day) do update set \
            projects = excluded.projects, projects_measured = excluded.projects_measured, \
            features_total = excluded.features_total, features_seed = excluded.features_seed, \
            complaints_total = excluded.complaints_total, complaints_seed = excluded.complaints_seed, \
            triage_median_h = excluded.triage_median_h, triage_n = excluded.triage_n, \
            delivery_median_h = excluded.delivery_median_h, delivery_n = excluded.delivery_n, \
            decision_median_h = excluded.decision_median_h, decision_n = excluded.decision_n, \
            no_origin = excluded.no_origin, computed_at = excluded.computed_at",
    )
    .bind(&snapshot.day)
    .bind(snapshot.projects)
    .bind(snapshot.projects_measured)
    .bind(snapshot.features_total)
    .bind(snapshot.features_seed)
    .bind(snapshot.complaints_total)
    .bind(snapshot.complaints_seed)
    .bind(snapshot.triage_median_h)
    .bind(snapshot.triage_n)
    .bind(snapshot.delivery_median_h)
    .bind(snapshot.delivery_n)
    .bind(snapshot.decision_median_h)
    .bind(snapshot.decision_n)
    .bind(snapshot.no_origin)
    .bind(snapshot.computed_at)
    .execute(pool)
    .await?;
    Ok(())
}

/// Write the per-project rows for one day.
///
/// # Errors
/// `sqlx::Error`.
pub async fn write_projects(
    pool: &PgPool,
    day: &str,
    rows: &[ProjectSnapshot],
) -> Result<(), sqlx::Error> {
    let mut transaction = pool.begin().await?;
    // The day's rows are REPLACED, not merged: a project that had organic features yesterday and has none today
    // because they were deleted must not keep yesterday's count in today's row.
    sqlx::query("delete from project_stats where day = $1")
        .bind(day)
        .execute(&mut *transaction)
        .await?;
    for row in rows {
        sqlx::query(
            "insert into project_stats (day, project_id, organic_features_shipped, \
                organic_complaints_filed) values ($1,$2,$3,$4)",
        )
        .bind(day)
        .bind(row.project_id)
        .bind(row.features_shipped)
        .bind(row.complaints_filed)
        .execute(&mut *transaction)
        .await?;
    }
    transaction.commit().await?;
    Ok(())
}
