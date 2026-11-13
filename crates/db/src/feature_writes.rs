//! Writes: filing a feature, moving it along, and the two rules that make `ready` and `in_progress`
//! mean something.
//!
//! `docs/REMAINING.md` §44/§46 record them, and they are the reason this file exists separately from
//! the read side:
//!
//! * a feature reaches `ready` only **with evidence** — a validated complaint, a document, or a field
//!   report — and in every intake mode, because `idea_first` frees the *filing* (§3.1) and not the
//!   commitment to build (§3.2);
//! * it starts work only when **nothing open blocks it**, and the evidence is re-checked at
//!   `in_progress`, because evidence can be removed after a feature is ready: `UnlinkComplaint` and
//!   `RemoveFeatureEvidence` both delete rows and neither un-readies anything. Without the re-check
//!   the gate on `ready` would have a hole — ready, unlink, start work.

use sqlx::PgPool;

use crate::{features::Feature, projects::CreateError};

/// What can go wrong writing a feature, in the shapes the routes answer.
#[derive(Debug)]
pub enum WriteError {
    /// `ErrInvalid`: the caller's input, and the message says which input.
    Invalid(String),
    /// `ErrNotFound`: no such feature, or no such complaint.
    NotFound(String),
    Database(sqlx::Error),
}

impl From<sqlx::Error> for WriteError {
    fn from(error: sqlx::Error) -> Self {
        WriteError::Database(error)
    }
}

fn valid_effort(effort: &str) -> bool {
    matches!(effort, "S" | "M" | "L" | "XL")
}

/// Append one audit row.
///
/// Not decoration: the milestone counts treat a feature as *shipped* only when an
/// `action = 'feature.status'` row says `-> shipped`, and as *seeded* when there is no
/// `action = 'create_feature'` row at all. A write that skipped this would make the roadmap's
/// progress numbers disagree with the work.
///
/// # Errors
/// [`WriteError::Database`] when the insert fails.
pub async fn add_audit(
    pool: &PgPool,
    project_id: i64,
    actor_id: i64,
    action: &str,
    entity: &str,
    entity_id: i64,
    detail: &str,
) -> Result<(), WriteError> {
    sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, \
                                created_at) values ($1, $2, $3, $4, $5, $6, now())",
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

/// A project's intake mode, `complaint_first` when the row does not say.
async fn intake_mode(pool: &PgPool, project_id: i64) -> Result<String, WriteError> {
    let mode: Option<String> =
        sqlx::query_scalar("select intake_mode from projects where id = $1")
            .bind(project_id)
            .fetch_optional(pool)
            .await?;
    Ok(mode.unwrap_or_else(|| "complaint_first".to_string()))
}

/// File a feature.
///
/// # Errors
/// [`WriteError::Invalid`] for a bad effort or impact, for a complaint that is not validated, or for
/// a feature with no complaint in a `complaint_first` project; [`WriteError::Database`] otherwise.
pub async fn create_feature(
    pool: &PgPool,
    project_id: i64,
    author_id: i64,
    title: &str,
    body: &str,
    effort: &str,
    impact: Option<i64>,
    effort_score: Option<i64>,
    linked_complaints: &[i64],
) -> Result<Feature, WriteError> {
    if project_id == 0 || author_id == 0 {
        return Err(WriteError::Invalid(
            "project_id and author_id are required".into(),
        ));
    }
    // §6.2's traceability rule is per PROJECT, not per instance: a reader in an `idea_first` project
    // must not be told to file a complaint the project does not ask for.
    if linked_complaints.is_empty() && intake_mode(pool, project_id).await? == "complaint_first" {
        return Err(WriteError::Invalid(
            "this project is complaint_first, so a feature needs at least one validated complaint; \
             a maintainer can change the project's intake mode to idea_first or both"
                .into(),
        ));
    }
    let effort = if effort.is_empty() { "M" } else { effort };
    if !valid_effort(effort) {
        return Err(WriteError::Invalid("effort must be one of S, M, L, XL".into()));
    }
    for (label, value) in [("impact", impact), ("effort_score", effort_score)] {
        if let Some(number) = value {
            if !(1..=10).contains(&number) {
                return Err(WriteError::Invalid(format!(
                    "{label} must be between 1 and 10"
                )));
            }
        }
    }
    // Verified before the transaction opens, and against the *status*: a complaint that has not been
    // validated is not evidence, and linking one would let a feature reach `ready` on a report nobody
    // confirmed.
    for complaint in linked_complaints {
        let status: Option<String> =
            sqlx::query_scalar("select status from complaints where id = $1")
                .bind(complaint)
                .fetch_optional(pool)
                .await?;
        match status.as_deref() {
            Some("validated") => {}
            Some(_) => {
                return Err(WriteError::Invalid(format!(
                    "complaint {complaint} must be validated"
                )))
            }
            None => return Err(WriteError::NotFound(format!("complaint {complaint}"))),
        }
    }

    let mut tx = pool.begin().await?;
    let id: i64 = sqlx::query_scalar(
        "insert into features (project_id, author_id, title, body, effort, status, elo_r, elo_rd, \
                                elo_vol, strategic_weight, impact, effort_score, created_at, \
                                updated_at, public_id) \
         values ($1, $2, $3, $4, $5, 'draft', 1500, 350, 0.06, 1.0, $6, $7, now(), now(), $8) \
         returning id",
    )
    .bind(project_id)
    .bind(author_id)
    .bind(title)
    .bind(body)
    .bind(effort)
    .bind(impact)
    .bind(effort_score)
    .bind(ulid::Ulid::new().to_string())
    .fetch_one(&mut *tx)
    .await?;
    for complaint in linked_complaints {
        sqlx::query(
            "insert into feature_complaints (feature_id, complaint_id) values ($1, $2) \
             on conflict do nothing",
        )
        .bind(id)
        .bind(complaint)
        .execute(&mut *tx)
        .await?;
    }
    tx.commit().await?;
    add_audit(pool, project_id, author_id, "create_feature", "feature", id, title).await?;
    crate::features::feature(pool, id)
        .await?
        .ok_or_else(|| WriteError::NotFound(format!("feature {id}")))
}

/// How much evidence a feature has: linked complaints plus cited documents and field reports.
async fn evidence_count(pool: &PgPool, feature_id: i64) -> Result<i64, WriteError> {
    let count: i64 = sqlx::query_scalar(
        "select (select count(*) from feature_complaints where feature_id = $1) \
              + (select count(*) from feature_evidence where feature_id = $1)",
    )
    .bind(feature_id)
    .fetch_one(pool)
    .await?;
    Ok(count)
}

/// The features still blocking this one, as `#id "title" (status)`.
async fn open_blockers(pool: &PgPool, feature_id: i64) -> Result<Vec<String>, WriteError> {
    use sqlx::Row;
    let rows = sqlx::query(
        "select f.id, f.title, f.status from feature_deps dep \
         join features f on f.id = dep.blocker_id \
         where dep.blocked_id = $1 and f.status <> 'shipped' order by f.id",
    )
    .bind(feature_id)
    .fetch_all(pool)
    .await?;
    rows.iter()
        .map(|row| {
            Ok(format!(
                "#{} \"{}\" ({})",
                row.try_get::<i64, _>(0)?,
                row.try_get::<String, _>(1)?,
                row.try_get::<String, _>(2)?
            ))
        })
        .collect()
}

/// Move a feature to `status`, enforcing the two gates.
///
/// # Errors
/// [`WriteError::Invalid`] when the gate refuses, with the reason the Go store gives — it names the
/// evidence or the blockers rather than saying "invalid"; [`WriteError::NotFound`] when there is no
/// such feature.
pub async fn update_feature_status(
    pool: &PgPool,
    feature_id: i64,
    status: &str,
) -> Result<(), WriteError> {
    if status == "ready" || status == "in_progress" {
        crate::features::feature(pool, feature_id)
            .await?
            .ok_or_else(|| WriteError::NotFound(format!("feature {feature_id}")))?;
        if status == "ready" && evidence_count(pool, feature_id).await? == 0 {
            return Err(WriteError::Invalid(
                "a feature reaches ready with evidence -- a validated complaint, a project document, \
                 or a field report"
                    .into(),
            ));
        }
        if status == "in_progress" {
            let blockers = open_blockers(pool, feature_id).await?;
            if !blockers.is_empty() {
                return Err(WriteError::Invalid(format!(
                    "blocked by {}",
                    blockers.join(", ")
                )));
            }
            if evidence_count(pool, feature_id).await? == 0 {
                return Err(WriteError::Invalid(
                    "a feature starts work with evidence -- a validated complaint, a project \
                     document, or a field report"
                        .into(),
                ));
            }
        }
    }
    let affected = sqlx::query("update features set status = $1, updated_at = now() where id = $2")
        .bind(status)
        .bind(feature_id)
        .execute(pool)
        .await?
        .rows_affected();
    if affected == 0 {
        return Err(WriteError::NotFound(format!("feature {feature_id}")));
    }
    Ok(())
}

/// Link a validated complaint to a feature.
///
/// # Errors
/// [`WriteError::Invalid`] when the complaint is not validated; [`WriteError::NotFound`] when it does
/// not exist.
pub async fn link_complaint(
    pool: &PgPool,
    feature_id: i64,
    complaint_id: i64,
) -> Result<(), WriteError> {
    let status: Option<String> = sqlx::query_scalar("select status from complaints where id = $1")
        .bind(complaint_id)
        .fetch_optional(pool)
        .await?;
    match status.as_deref() {
        Some("validated") => {}
        Some(_) => {
            return Err(WriteError::Invalid(format!(
                "complaint {complaint_id} must be validated"
            )))
        }
        None => return Err(WriteError::NotFound(format!("complaint {complaint_id}"))),
    }
    sqlx::query(
        "insert into feature_complaints (feature_id, complaint_id) values ($1, $2) \
         on conflict do nothing",
    )
    .bind(feature_id)
    .bind(complaint_id)
    .execute(pool)
    .await?;
    Ok(())
}

/// Remove one complaint from a feature.
///
/// Deliberately not guarded on status: the traceability rule bites on the *next* transition, and a
/// second policy for one removal path would be the bypass.
///
/// # Errors
/// [`WriteError::Database`] when the delete fails.
pub async fn unlink_complaint(
    pool: &PgPool,
    feature_id: i64,
    complaint_id: i64,
) -> Result<(), WriteError> {
    sqlx::query("delete from feature_complaints where feature_id = $1 and complaint_id = $2")
        .bind(feature_id)
        .bind(complaint_id)
        .execute(pool)
        .await?;
    Ok(())
}

/// The complaints linked to a feature.
///
/// # Errors
/// [`WriteError::Database`] when the read fails.
pub async fn feature_complaints(
    pool: &PgPool,
    feature_id: i64,
) -> Result<Vec<crate::complaints::Complaint>, WriteError> {
    let ids: Vec<i64> = sqlx::query_scalar(
        "select complaint_id from feature_complaints where feature_id = $1 order by complaint_id",
    )
    .bind(feature_id)
    .fetch_all(pool)
    .await?;
    let mut out = Vec::with_capacity(ids.len());
    for id in ids {
        if let Some(complaint) = crate::complaints::complaint(pool, id).await? {
            out.push(complaint);
        }
    }
    Ok(out)
}

/// A complaint by id, for the link handler's existence check.
///
/// # Errors
/// [`WriteError::Database`] when the read fails.
pub async fn complaint_status(pool: &PgPool, complaint_id: i64) -> Result<Option<String>, WriteError> {
    Ok(sqlx::query_scalar("select status from complaints where id = $1")
        .bind(complaint_id)
        .fetch_optional(pool)
        .await?)
}

/// Unused today but part of the enum's surface: a caller that creates a project with a bad slug gets
/// this instead of a panic, which is why the conversion exists.
impl From<CreateError> for WriteError {
    fn from(error: CreateError) -> Self {
        match error {
            CreateError::Invalid(message) => WriteError::Invalid(message),
            CreateError::Duplicate(message) => WriteError::Invalid(message),
            CreateError::Database(error) => WriteError::Database(error),
        }
    }
}
