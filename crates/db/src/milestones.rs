//! S2's milestones and the roadmap: progress that is computed, never stored.
//!
//! Two rules from the Go store are carried across literally, because both exist to stop a number
//! lying: the counts are computed on read (a stored total drifts the first time a feature leaves by
//! another path), and a feature counts as *shipped* only when an audit event says so — 925 of 1,874
//! features on this instance were created by the seeding script and have no creation event at all,
//! so a progress bar computed from `status` measures whoever ran the import.

use serde::Serialize;
use sqlx::{PgPool, Row};

use crate::projects::ser_unix;

/// One milestone with its computed counts.
#[derive(Debug, Clone, Serialize)]
pub struct Milestone {
    pub id: i64,
    pub project_id: i64,
    pub title: String,
    pub body: String,
    #[serde(serialize_with = "ser_go_float_opt")]
    pub target_date: Option<f64>,
    pub status: String,
    #[serde(serialize_with = "ser_unix")]
    pub created_at: f64,
    pub total: i64,
    pub open: i64,
    pub shipped: i64,
    /// Features with no creation audit event — i.e. the seeding script's.
    pub seeded: i64,
    /// Shipped features with no shipment event.
    pub unshipped: i64,
    pub at_risk: bool,
}

/// How late a milestone is, which is a different question for a closed one than an open one.
#[derive(Debug, Clone, Serialize)]
pub struct MilestoneSlippage {
    pub milestone_id: i64,
    pub title: String,
    pub status: String,
    #[serde(serialize_with = "ser_go_float_opt")]
    pub target_date: Option<f64>,
    /// Positive when the last shipment came after the target date. Only a closed milestone has one.
    #[serde(serialize_with = "ser_go_float_opt")]
    pub slip_days: Option<f64>,
    /// How long the oldest unfinished feature has waited. Only an open milestone has one.
    #[serde(serialize_with = "ser_go_float_opt")]
    pub oldest_open_days: Option<f64>,
}

fn ser_go_float_opt<S: serde::Serializer>(
    value: &Option<f64>,
    serializer: S,
) -> Result<S::Ok, S::Error> {
    match value {
        Some(number) => ser_unix(number, serializer),
        None => serializer.serialize_none(),
    }
}

/// The counts, in one query over the milestone's features.
///
/// The five aggregates are the Go `countMilestoneFeatures` query verbatim, including the two
/// `EXISTS` clauses that make "shipped" mean "an audit event says so" and "seeded" mean "no creation
/// event exists".
const COUNTS_SQL: &str = "select count(*), \
        coalesce(sum(case when f.status in ('shipped','rejected') then 0 else 1 end), 0), \
        coalesce(sum(case when f.status = 'shipped' and exists ( \
             select 1 from audit_log a where a.entity = 'feature' and a.entity_id = f.id \
               and a.action = 'feature.status' and a.detail like '%-> shipped%' \
        ) then 1 else 0 end), 0), \
        coalesce(sum(case when not exists ( \
             select 1 from audit_log a where a.entity = 'feature' and a.entity_id = f.id \
               and a.action = 'create_feature' \
        ) then 1 else 0 end), 0), \
        coalesce(sum(case when f.status = 'shipped' and not exists ( \
             select 1 from audit_log a where a.entity = 'feature' and a.entity_id = f.id \
               and a.action = 'feature.status' and a.detail like '%-> shipped%' \
        ) then 1 else 0 end), 0) \
     from features f where f.milestone_id = $1";

/// Every milestone of a project, in the Go query's order: dated ones by target date, then undated
/// by id. `ORDER BY target_date IS NULL, target_date, id` — the same clause, because the order is
/// what the roadmap renders.
///
/// # Errors
/// [`sqlx::Error`] when a read fails.
pub async fn list_milestones(pool: &PgPool, project_id: i64) -> Result<Vec<Milestone>, sqlx::Error> {
    let rows = sqlx::query(
        "select id, project_id, title, body, target_date, status, \
                extract(epoch from created_at)::double precision \
         from milestones where project_id = $1 \
         order by target_date is null, target_date, id",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await?;

    let now = chrono_now_unix();
    let mut out = Vec::with_capacity(rows.len());
    for row in &rows {
        let mut milestone = Milestone {
            id: row.try_get(0)?,
            project_id: row.try_get(1)?,
            title: row.try_get(2)?,
            body: row.try_get(3)?,
            target_date: row.try_get(4)?,
            status: row.try_get(5)?,
            created_at: row.try_get(6)?,
            total: 0,
            open: 0,
            shipped: 0,
            seeded: 0,
            unshipped: 0,
            at_risk: false,
        };
        let counts = sqlx::query(COUNTS_SQL)
            .bind(milestone.id)
            .fetch_one(pool)
            .await?;
        milestone.total = counts.try_get(0)?;
        milestone.open = counts.try_get(1)?;
        milestone.shipped = counts.try_get(2)?;
        milestone.seeded = counts.try_get(3)?;
        milestone.unshipped = counts.try_get(4)?;
        // Derived, never stored: a stale "on track" badge is worse than no badge.
        milestone.at_risk = matches!(milestone.target_date, Some(target) if target > 0.0)
            && milestone.open > 0
            && milestone.target_date.unwrap_or(0.0) < now
            && matches!(milestone.status.as_str(), "planned" | "active");
        out.push(milestone);
    }
    Ok(out)
}

/// How late each milestone is, measured the way the Go store measures it: a closed milestone slips
/// against its last shipment, an open one is as late as its oldest unfinished feature is old. Both
/// are `None` rather than `0.0` when there is nothing to measure — zero days of slip is the best
/// possible answer and an easy thing to fake.
///
/// # Errors
/// [`sqlx::Error`] when a read fails.
pub async fn roadmap_slippage(
    pool: &PgPool,
    project_id: i64,
) -> Result<Vec<MilestoneSlippage>, sqlx::Error> {
    let milestones = list_milestones(pool, project_id).await?;
    let now = chrono_now_unix();
    let mut out = Vec::with_capacity(milestones.len());
    for milestone in milestones {
        let mut row = MilestoneSlippage {
            milestone_id: milestone.id,
            title: milestone.title.clone(),
            status: milestone.status.clone(),
            target_date: milestone.target_date,
            slip_days: None,
            oldest_open_days: None,
        };
        if matches!(milestone.status.as_str(), "done" | "cancelled") {
            let last_ship: Option<f64> = sqlx::query_scalar(
                "select extract(epoch from max(a.created_at))::double precision \
                 from audit_log a join features f on f.id = a.entity_id \
                 where f.milestone_id = $1 and a.entity = 'feature' \
                   and a.action = 'feature.status' and a.detail like '%-> shipped%'",
            )
            .bind(milestone.id)
            .fetch_one(pool)
            .await?;
            if let (Some(last), Some(target)) = (last_ship, milestone.target_date) {
                if target > 0.0 {
                    row.slip_days = Some((last - target) / 86_400.0);
                }
            }
        } else {
            let oldest: Option<f64> = sqlx::query_scalar(
                "select extract(epoch from min(f.created_at))::double precision \
                 from features f where f.milestone_id = $1 \
                   and f.status not in ('shipped','rejected')",
            )
            .bind(milestone.id)
            .fetch_one(pool)
            .await?;
            if let Some(oldest) = oldest {
                if oldest > 0.0 {
                    row.oldest_open_days = Some((now - oldest) / 86_400.0);
                }
            }
        }
        out.push(row);
    }
    Ok(out)
}

fn chrono_now_unix() -> f64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|elapsed| elapsed.as_secs_f64())
        .unwrap_or(0.0)
}
