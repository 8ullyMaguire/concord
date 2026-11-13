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
        // `target_date` is a TIMESTAMPTZ in this schema (the Go stored a REAL unix stamp), so it is converted in
        // SQL. Decoding it as f64 fails with "mismatched types … FLOAT8 is not compatible with TIMESTAMPTZ", which
        // names a column NUMBER and not a column.
        "select id, project_id, title, body, \
                extract(epoch from target_date)::double precision, status, \
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

/// One milestone's publication event, for the milestones feed.
///
/// `status_changed_at` is `0` when the audit log has never recorded a change for it: the feed reports
/// `created_at` for such a row rather than a change that did not happen. The Go store expresses the same thing
/// with `COALESCE(…, 0)`, and the caller reads the zero as "no change recorded" instead of as 1970.
#[derive(Debug, Clone)]
pub struct Event {
    /// The milestone.
    pub milestone_id: i64,
    /// Its title.
    pub title: String,
    /// Its current status.
    pub status: String,
    /// When it was created, unix seconds.
    pub created_at: f64,
    /// When its status last changed, or zero.
    pub status_changed_at: f64,
}

/// Milestones for a project, newest id first, with the last recorded status change.
///
/// # Errors
/// `sqlx::Error`.
pub async fn events(pool: &PgPool, project_id: i64) -> Result<Vec<Event>, sqlx::Error> {
    let rows = sqlx::query(
        "select m.id, m.title, m.status, \
                extract(epoch from m.created_at)::double precision, \
                coalesce((select max(extract(epoch from a.created_at))::double precision \
                            from audit_log a \
                           where a.entity = 'milestone' and a.entity_id = m.id \
                             and a.action = 'milestone.status'), 0) \
           from milestones m where m.project_id = $1 order by m.id desc",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await?;
    let mut out = Vec::with_capacity(rows.len());
    for row in rows {
        out.push(Event {
            milestone_id: row.try_get(0)?,
            title: row.try_get(1)?,
            status: row.try_get(2)?,
            created_at: row.try_get(3)?,
            status_changed_at: row.try_get(4)?,
        });
    }
    Ok(out)
}

/// One milestone, by id, inside a project — `None` when it is not that project's.
///
/// The `project_id` predicate is in the QUERY rather than a comparison after the read: a handler that fetches by id
/// and compares afterwards works until somebody forgets the comparison in the next handler, and then one project's
/// milestone is served under another's URL.
///
/// # Errors
/// [`sqlx::Error`] when a read fails.
pub async fn milestone(
    pool: &PgPool,
    project_id: i64,
    id: i64,
) -> Result<Option<Milestone>, sqlx::Error> {
    let Some(row) = sqlx::query(
        "select id, project_id, title, body,
                extract(epoch from target_date)::double precision, status,
                extract(epoch from created_at)::double precision
           from milestones where id = $1 and project_id = $2",
    )
    .bind(id)
    .bind(project_id)
    .fetch_optional(pool)
    .await?
    else {
        return Ok(None);
    };
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
    let counts = sqlx::query(COUNTS_SQL).bind(milestone.id).fetch_one(pool).await?;
    milestone.total = counts.try_get(0)?;
    milestone.open = counts.try_get(1)?;
    milestone.shipped = counts.try_get(2)?;
    milestone.seeded = counts.try_get(3)?;
    milestone.unshipped = counts.try_get(4)?;
    // Derived, never stored, and derived the same way the list does it: two places computing `at_risk` differently
    // is how a badge and a page start disagreeing about the same milestone.
    milestone.at_risk = matches!(milestone.target_date, Some(target) if target > 0.0)
        && milestone.open > 0
        && milestone.target_date.unwrap_or(0.0) < chrono_now_unix()
        && matches!(milestone.status.as_str(), "planned" | "active");
    Ok(Some(milestone))
}

/// A refusal from a milestone write.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Refused {
    /// No such milestone in this project. Another project's milestone is **not found**, never forbidden: a distinct
    /// status for "exists but not yours" is an existence oracle.
    NotFound,
    /// Malformed or not applicable: no title, an unknown status, a close with open features and no successor, a
    /// feature that is not in this milestone, a milestone succeeding itself.
    Invalid(String),
    /// Already `done` or `cancelled` and being closed again.
    Conflict(String),
    /// A title already used in this project.
    Duplicate(String),
}

/// The four statuses. `done` and `cancelled` are only reachable through [`close`], because that is the only path that
/// decides what happens to the features still open under it.
pub const STATUSES: [&str; 4] = ["planned", "active", "done", "cancelled"];

/// Create a milestone, `planned` and dated or not.
///
/// `target_date` is unix seconds or nothing: an undated milestone has made no promise, which is why it sorts last
/// everywhere.
///
/// # Errors
/// `sqlx::Error`.
pub async fn create(
    pool: &PgPool,
    project_id: i64,
    title: &str,
    body: &str,
    target_date: Option<f64>,
) -> Result<Result<Milestone, Refused>, sqlx::Error> {
    if title.is_empty() {
        return Ok(Err(Refused::Invalid("title is required".to_string())));
    }
    // `public_id` is NOT NULL with no default on this table too — see the note in `complaints::create`.
    let inserted = sqlx::query_scalar::<_, i64>(
        "insert into milestones (project_id, title, body, target_date, status, created_at, public_id)
         values ($1, $2, $3, case when $4::double precision is null then null else to_timestamp($4) end,
                 'planned', now(), $5) returning id",
    )
    .bind(project_id)
    .bind(title)
    .bind(body)
    .bind(target_date)
    .bind(ulid::Ulid::new().to_string())
    .fetch_one(pool)
    .await;
    let id = match inserted {
        Ok(id) => id,
        Err(sqlx::Error::Database(database)) if database.is_unique_violation() => {
            return Ok(Err(Refused::Duplicate(format!(
                "a milestone called {title:?} already exists in this project"
            ))));
        }
        Err(error) => return Err(error),
    };
    Ok(Ok(milestone(pool, project_id, id)
        .await?
        .ok_or(sqlx::Error::RowNotFound)?))
}

/// Edit a milestone's title, body and date. Status is not editable here: it moves through [`set_status`] and [`close`],
/// each of which writes an act.
///
/// # Errors
/// `sqlx::Error`.
pub async fn update(
    pool: &PgPool,
    project_id: i64,
    id: i64,
    title: &str,
    body: &str,
    target_date: Option<f64>,
) -> Result<Result<Milestone, Refused>, sqlx::Error> {
    if title.is_empty() {
        return Ok(Err(Refused::Invalid("title is required".to_string())));
    }
    let updated = sqlx::query(
        "update milestones set title = $1, body = $2,
                target_date = case when $3::double precision is null then null else to_timestamp($3) end
          where id = $4 and project_id = $5",
    )
    .bind(title)
    .bind(body)
    .bind(target_date)
    .bind(id)
    .bind(project_id)
    .execute(pool)
    .await;
    match updated {
        Ok(result) if result.rows_affected() == 0 => {
            return Ok(Err(Refused::NotFound));
        }
        Ok(_) => {}
        Err(sqlx::Error::Database(database)) if database.is_unique_violation() => {
            return Ok(Err(Refused::Duplicate(format!(
                "a milestone called {title:?} already exists in this project"
            ))));
        }
        Err(error) => return Err(error),
    }
    Ok(Ok(milestone(pool, project_id, id)
        .await?
        .ok_or(sqlx::Error::RowNotFound)?))
}

/// Move a milestone between `planned` and `active`, writing the act.
///
/// A **no-op when the status already matches**, and no audit row in that case: recording "active -> active" as an act
/// makes the log noisy and the history unreadable.
///
/// Any of the four statuses is accepted here, `done` and `cancelled` included, which is what the reference does — the
/// `close` route is the only path that *reconciles* the features, but the status route can still set the word. That is a
/// wart: it lets a client mark a milestone done with open features under it, which `close` exists to prevent. Carried
/// as-is and recorded here rather than silently tightened, because a page that uses this route would start failing.
///
/// # Errors
/// `sqlx::Error`.
pub async fn set_status(
    pool: &PgPool,
    project_id: i64,
    id: i64,
    actor_id: i64,
    status: &str,
) -> Result<Result<Milestone, Refused>, sqlx::Error> {
    if !STATUSES.contains(&status) {
        return Ok(Err(Refused::Invalid(format!(
            "unknown milestone status {status:?}"
        ))));
    }
    let Some(current) = milestone(pool, project_id, id).await? else {
        return Ok(Err(Refused::NotFound));
    };
    if current.status == status {
        return Ok(Ok(current));
    }
    sqlx::query("update milestones set status = $1 where id = $2 and project_id = $3")
        .bind(status)
        .bind(id)
        .bind(project_id)
        .execute(pool)
        .await?;
    insert_audit(
        pool,
        project_id,
        actor_id,
        "milestone.status",
        "milestone",
        id,
        &format!("{} -> {status}", current.status),
    )
    .await?;
    Ok(Ok(milestone(pool, project_id, id)
        .await?
        .ok_or(sqlx::Error::RowNotFound)?))
}

/// Close a milestone, deciding what happens to the features still open under it (spec §4.4).
///
/// `done` with open features is a lie and a blocked close is a trap, so the two are reconciled explicitly:
///
/// * **a successor moves them** — one audit act per moved feature, so "why did this feature move" is answerable from the
///   log afterwards;
/// * **a cancellation moves none** — the features stay where they are, which is what "we are not doing this" means for
///   work already grouped under it;
/// * **`done` with open features and no successor is refused** with the count, because the alternative is a milestone
///   that says done while three of its features are not.
///
/// Returns the milestone and how many features moved.
///
/// # Errors
/// `sqlx::Error`.
pub async fn close(
    pool: &PgPool,
    project_id: i64,
    id: i64,
    actor_id: i64,
    successor: Option<i64>,
    cancel: bool,
) -> Result<Result<(Milestone, i64), Refused>, sqlx::Error> {
    let Some(current) = milestone(pool, project_id, id).await? else {
        return Ok(Err(Refused::NotFound));
    };
    if current.status == "done" || current.status == "cancelled" {
        return Ok(Err(Refused::Conflict(format!(
            "milestone {id} is already {}",
            current.status
        ))));
    }
    if let Some(successor_id) = successor {
        if successor_id == id {
            return Ok(Err(Refused::Invalid(
                "a milestone cannot succeed itself".to_string(),
            )));
        }
        if milestone(pool, project_id, successor_id).await?.is_none() {
            return Ok(Err(Refused::NotFound));
        }
    } else if !cancel && current.open > 0 {
        return Ok(Err(Refused::Invalid(format!(
            "{} feature(s) are still open; name a successor milestone or cancel instead",
            current.open
        ))));
    }

    let mut tx = pool.begin().await?;
    let mut moved_ids: Vec<i64> = Vec::new();
    if let Some(successor_id) = successor {
        moved_ids = sqlx::query_scalar(
            "select id from features where milestone_id = $1 and status not in ('shipped','rejected')",
        )
        .bind(id)
        .fetch_all(&mut *tx)
        .await?;
        for feature in &moved_ids {
            sqlx::query("update features set milestone_id = $1 where id = $2")
                .bind(successor_id)
                .bind(feature)
                .execute(&mut *tx)
                .await?;
        }
    }

    let status = if cancel { "cancelled" } else { "done" };
    sqlx::query("update milestones set status = $1 where id = $2")
        .bind(status)
        .bind(id)
        .execute(&mut *tx)
        .await?;
    let mut detail = format!("{} -> {status}", current.status);
    if let Some(successor_id) = successor {
        detail.push_str(&format!(
            "; {} feature(s) moved to milestone {successor_id}",
            moved_ids.len()
        ));
    }
    // One statement for the milestone, then one per moved feature, all in the same transaction: a feature that moved with
    // no record is the exact gap this rule exists to prevent.
    audit_in(
        &mut tx,
        project_id,
        actor_id,
        "milestone.status",
        "milestone",
        id,
        &detail,
    )
    .await?;
    if let Some(successor_id) = successor {
        for feature in &moved_ids {
            audit_in(
                &mut tx,
                project_id,
                actor_id,
                "feature.milestone",
                "feature",
                *feature,
                &format!("milestone {id} -> {successor_id} (closed)"),
            )
            .await?;
        }
    }
    tx.commit().await?;
    let moved = i64::try_from(moved_ids.len()).unwrap_or(i64::MAX);
    Ok(Ok((
        milestone(pool, project_id, id)
            .await?
            .ok_or(sqlx::Error::RowNotFound)?,
        moved,
    )))
}

/// Append a batch of features to a milestone (§6).
///
/// Positions **continue after the current tail**, so a batch lands after what is already there rather than interleaving.
/// Every feature must belong to this project, or the batch is refused: a feature id from another project would otherwise
/// be pulled into this milestone, which is a cross-project write dressed as a bulk assign.
///
/// # Errors
/// `sqlx::Error`.
pub async fn assign_features(
    pool: &PgPool,
    project_id: i64,
    milestone_id: i64,
    feature_ids: &[i64],
) -> Result<Result<i64, Refused>, sqlx::Error> {
    if milestone(pool, project_id, milestone_id).await?.is_none() {
        return Ok(Err(Refused::NotFound));
    }
    if feature_ids.is_empty() {
        return Ok(Err(Refused::Invalid("no features named".to_string())));
    }
    let mut tx = pool.begin().await?;
    let mut tail: i64 = sqlx::query_scalar(
        "select coalesce(max(milestone_position), 0) from features where milestone_id = $1",
    )
    .bind(milestone_id)
    .fetch_one(&mut *tx)
    .await?;
    let mut done = 0i64;
    for feature in feature_ids {
        let owner: Option<i64> = sqlx::query_scalar("select project_id from features where id = $1")
            .bind(feature)
            .fetch_optional(&mut *tx)
            .await?;
        if owner != Some(project_id) {
            return Ok(Err(Refused::NotFound));
        }
        tail += 1;
        sqlx::query(
            "update features set milestone_id = $1, milestone_position = $2, updated_at = now() where id = $3",
        )
        .bind(milestone_id)
        .bind(tail)
        .bind(feature)
        .execute(&mut *tx)
        .await?;
        done += 1;
    }
    tx.commit().await?;
    Ok(Ok(done))
}

/// Write an explicit order for a milestone's features, one-based from the array.
///
/// A feature named that is **not in this milestone** is refused rather than silently skipped: the caller believed it was
/// reordering the whole set, and a quiet skip leaves two features sharing a position.
///
/// # Errors
/// `sqlx::Error`.
pub async fn reorder(
    pool: &PgPool,
    project_id: i64,
    milestone_id: i64,
    feature_ids: &[i64],
) -> Result<Result<(), Refused>, sqlx::Error> {
    if milestone(pool, project_id, milestone_id).await?.is_none() {
        return Ok(Err(Refused::NotFound));
    }
    let mut tx = pool.begin().await?;
    for (index, feature) in feature_ids.iter().enumerate() {
        let current: Option<Option<i64>> =
            sqlx::query_scalar("select milestone_id from features where id = $1 and project_id = $2")
                .bind(feature)
                .bind(project_id)
                .fetch_optional(&mut *tx)
                .await?;
        match current {
            None => return Ok(Err(Refused::NotFound)),
            Some(found) if found != Some(milestone_id) => {
                return Ok(Err(Refused::Invalid(format!(
                    "feature {feature} is not in this milestone"
                ))));
            }
            Some(_) => {}
        }
        sqlx::query("update features set milestone_position = $1 where id = $2")
            .bind(i64::try_from(index + 1).unwrap_or(i64::MAX))
            .bind(feature)
            .execute(&mut *tx)
            .await?;
    }
    tx.commit().await?;
    Ok(Ok(()))
}

async fn insert_audit(
    pool: &PgPool,
    project_id: i64,
    actor_id: i64,
    action: &str,
    entity: &str,
    entity_id: i64,
    detail: &str,
) -> Result<(), sqlx::Error> {
    sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1, $2, $3, $4, $5, $6, now())",
    )
    .bind(project_id)
    .bind(actor_id)
    .bind(action)
    .bind(entity)
    .bind(entity_id)
    .bind(detail)
    .execute(pool)
    .await?;
    Ok(())
}

async fn audit_in(
    tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
    project_id: i64,
    actor_id: i64,
    action: &str,
    entity: &str,
    entity_id: i64,
    detail: &str,
) -> Result<(), sqlx::Error> {
    sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1, $2, $3, $4, $5, $6, now())",
    )
    .bind(project_id)
    .bind(actor_id)
    .bind(action)
    .bind(entity)
    .bind(entity_id)
    .bind(detail)
    .execute(&mut **tx)
    .await?;
    Ok(())
}

/// Put one feature in a milestone, at a position, or take it out (`milestone_id` `None`).
///
/// The response is the **count of open blockers**, not a boolean: a feature may be assigned to a milestone whose
/// predecessors are unfinished, and the caller needs to know how much work stands in front of it. Refusing instead would
/// be wrong — a roadmap that cannot express "this lands after X" is not a roadmap.
///
/// # Errors
/// `sqlx::Error`.
pub async fn set_feature_milestone(
    pool: &PgPool,
    project_id: i64,
    feature_id: i64,
    milestone_id: Option<i64>,
    position: i64,
) -> Result<Result<i64, Refused>, sqlx::Error> {
    let owner: Option<i64> = sqlx::query_scalar("select project_id from features where id = $1")
        .bind(feature_id)
        .fetch_optional(pool)
        .await?;
    if owner != Some(project_id) {
        return Ok(Err(Refused::NotFound));
    }
    if let Some(milestone) = milestone_id {
        let exists: Option<i64> = sqlx::query_scalar("select id from milestones where id = $1 and project_id = $2")
            .bind(milestone)
            .bind(project_id)
            .fetch_optional(pool)
            .await?;
        if exists.is_none() {
            return Ok(Err(Refused::NotFound));
        }
    }
    sqlx::query(
        "update features set milestone_id = $1, milestone_position = $2, updated_at = now() where id = $3",
    )
    .bind(milestone_id)
    .bind(position)
    .bind(feature_id)
    .execute(pool)
    .await?;
    // **`feature_deps` is `(blocker_id, blocked_id)`**: the blocker is the row that must finish first. Counting from the
    // other side reports zero for every feature and reads as "nothing in the way", which is the answer that looks
    // reassuring and is wrong. `shipped` is the reference's word for a finished blocker.
    let open: i64 = sqlx::query_scalar(
        "select count(*) from feature_deps dep join features f on f.id = dep.blocker_id
          where dep.blocked_id = $1 and f.status <> 'shipped'",
    )
    .bind(feature_id)
    .fetch_one(pool)
    .await?;
    Ok(Ok(open))
}
