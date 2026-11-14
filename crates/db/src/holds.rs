//! §6.6's emergency hold: the maintainer's narrow power.
//!
//! The spec described this veto two ways at once — an override path for blocks in §5.5/§6.3, and limited
//! to security and legal in the role definition. Read the first way, a maintainer could cancel a community
//! block, which contradicts §7's "maintainers hold no steering power". What is implemented is the narrow
//! reading, because it is the only one consistent with the rest of the spec, and every rule below is part
//! of that reading:
//!
//! * it **suspends** a proposal and does nothing else — it cannot force a result, cannot override a block,
//!   cannot close a call;
//! * it requires a **written reason** and a stated **ground** (security or legal), because the reason is
//!   the accountability mechanism: a hold nobody can review is indistinguishable from an unjustified one;
//! * it **expires on its own**, so no cleanup job is needed for an old hold to stop working — it simply
//!   stops matching;
//! * a requested duration **above the ceiling is refused, not clamped**: silently shortening a hold the
//!   maintainer asked for is as surprising as silently extending one. Without a ceiling the emergency power
//!   is an indefinite veto with a different name.
//!
//! The one defect this file exists to keep closed: a maintainer of project B suspending project A's call
//! through B's URL. The call's own project is checked here, at the last point before the row is written,
//! because the HTTP layer's check and the store's must not be the same check — a route added later would
//! otherwise inherit the hole.

use serde::Serialize;
use sqlx::PgPool;

use crate::feature_writes::WriteError;

/// The default hold, matching the default consensus window: long enough to run a security response, short
/// enough that a hold cannot quietly become the decision.
pub const DEFAULT_HOLD_DAYS: i64 = 7;

/// The ceiling. Beyond it the power is an indefinite veto.
pub const MAX_HOLD_DAYS: i64 = 30;

/// A hold, in the Go struct's wire shape.
#[derive(Debug, Clone, Serialize)]
pub struct Hold {
    pub id: i64,
    pub project_id: i64,
    pub call_id: i64,
    pub held_by: i64,
    pub reason: String,
    pub grounds: String,
    pub created_at: f64,
    pub expires_at: f64,
    pub confirmation_call_id: i64,
    pub released_at: f64,
    pub release_reason: String,
    /// True while the hold is in force. Computed from the timestamps rather than stored, so a hold cannot
    /// be left looking active by a row that was never updated.
    pub active: bool,
}

const HOLD_SQL: &str = "select id, project_id, call_id, \
     coalesce(held_by, 0), coalesce(reason, ''), coalesce(grounds, ''), \
     extract(epoch from created_at)::double precision, \
     extract(epoch from expires_at)::double precision, \
     coalesce(confirmation_call_id, 0), \
     coalesce(extract(epoch from released_at)::double precision, 0), \
     coalesce(release_reason, '') \
     from emergency_holds";

fn from_row(row: &sqlx::postgres::PgRow, now: f64) -> Result<Hold, sqlx::Error> {
    use sqlx::Row;
    let released_at: f64 = row.try_get(9)?;
    let expires_at: f64 = row.try_get(7)?;
    Ok(Hold {
        id: row.try_get(0)?,
        project_id: row.try_get(1)?,
        call_id: row.try_get(2)?,
        held_by: row.try_get(3)?,
        reason: row.try_get(4)?,
        grounds: row.try_get(5)?,
        created_at: row.try_get(6)?,
        expires_at,
        confirmation_call_id: row.try_get(8)?,
        released_at,
        release_reason: row.try_get(10)?,
        // Released-at 0 is the "never released" sentinel, which is why `coalesce(..., 0)` is used above
        // rather than an Option: the wire shape has a float64 and a null would become 0 anyway.
        active: released_at == 0.0 && expires_at > now,
    })
}

/// The in-force hold on a call, if any.
///
/// A call is suspended only by a hold that is neither released nor expired, so an expired hold needs no
/// cleanup: it stops matching.
///
/// # Errors
/// [`sqlx::Error`] when the read fails.
pub async fn active_for_call(pool: &PgPool, call_id: i64) -> Result<Option<Hold>, sqlx::Error> {
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|elapsed| elapsed.as_secs_f64())
        .unwrap_or(0.0);
    let row = sqlx::query(&format!(
        "{HOLD_SQL} where call_id = $1 and released_at is null and expires_at > now() \
         order by expires_at desc limit 1"
    ))
    .bind(call_id)
    .fetch_optional(pool)
    .await?;
    row.map(|row| from_row(&row, now)).transpose()
}

/// Suspend a call.
///
/// # Errors
/// [`WriteError::Invalid`] for a ground outside security/legal, a missing reason, a duration above the
/// ceiling, or a call belonging to another project.
pub async fn place(
    pool: &PgPool,
    project_id: i64,
    call_id: i64,
    user_id: i64,
    grounds: &str,
    reason: &str,
    days: i64,
) -> Result<Hold, WriteError> {
    let grounds = grounds.trim().to_lowercase();
    if grounds != "security" && grounds != "legal" {
        return Err(WriteError::Invalid(
            "emergency hold grounds must be security or legal".into(),
        ));
    }
    let reason = reason.trim();
    if reason.is_empty() {
        return Err(WriteError::Invalid(
            "an emergency hold requires a written reason".into(),
        ));
    }
    let days = if days <= 0 { DEFAULT_HOLD_DAYS } else { days };
    if days > MAX_HOLD_DAYS {
        // Refused, not clamped: silently shortening a hold the maintainer asked for is as surprising as
        // silently extending one.
        return Err(WriteError::Invalid(format!(
            "a hold may not exceed {MAX_HOLD_DAYS} days"
        )));
    }
    let owner: Option<i64> = sqlx::query_scalar("select project_id from consensus_calls where id = $1")
        .bind(call_id)
        .fetch_optional(pool)
        .await?;
    let Some(owner) = owner else {
        return Err(WriteError::NotFound(format!("consensus call {call_id}")));
    };
    if owner != project_id {
        // The measured defect: a maintainer of one project suspended another's call through their own
        // URL. A hold that names one project while suspending another's call must not be representable.
        return Err(WriteError::Invalid(format!(
            "call {call_id} does not belong to project {project_id}"
        )));
    }
    let id: i64 = sqlx::query_scalar(
        "insert into emergency_holds (project_id, call_id, held_by, reason, grounds, created_at, \
                                      expires_at) \
         values ($1, $2, $3, $4, $5, now(), now() + ($6::bigint * interval '1 day')) \
         returning id",
    )
    .bind(project_id)
    .bind(call_id)
    .bind(user_id)
    .bind(reason)
    .bind(&grounds)
    .bind(days)
    .fetch_one(pool)
    .await?;
    // §9.3 makes a steward's action on content publicly visible, and a hold is exactly that: it goes in
    // the admin ledger as well as the audit log.
    sqlx::query(
        "insert into admin_ledger (actor_id, action, subject, justification, project_id) \
         values ($1, 'emergency_hold', $2, $3, $4)",
    )
    .bind(user_id)
    .bind(format!("consensus call {call_id}"))
    .bind(reason)
    .bind(project_id)
    .execute(pool)
    .await?;
    crate::feature_writes::add_audit(
        pool,
        project_id,
        user_id,
        "emergency_hold",
        "consensus_call",
        call_id,
        &format!("{grounds} hold for {days} days: {reason}"),
    )
    .await?;
    one(pool, id).await
}

/// One hold by id.
///
/// # Errors
/// [`WriteError::NotFound`] when it does not exist.
pub async fn one(pool: &PgPool, id: i64) -> Result<Hold, WriteError> {
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|elapsed| elapsed.as_secs_f64())
        .unwrap_or(0.0);
    let row = sqlx::query(&format!("{HOLD_SQL} where id = $1"))
        .bind(id)
        .fetch_optional(pool)
        .await?;
    match row {
        Some(row) => Ok(from_row(&row, now)?),
        None => Err(WriteError::NotFound(format!("hold {id}"))),
    }
}

/// A deliberate defect reachable only by environment variable, so a probe's control does not cost a rebuild.
fn mutant(name: &str) -> bool {
    std::env::var("CONCORD_MUTANT")
        .unwrap_or_default()
        .split(',')
        .any(|candidate| candidate.trim() == name)
}

/// Lift a hold, with a written reason, and record the act twice: in §9.3's admin ledger and in the project's audit log.
///
/// **Both records, always.** A hold is an emergency power — it suspends a consensus call that is already open — so lifting one is
/// the event a later reader needs to find from either direction: the instance-wide ledger answers "what has an admin done", the
/// project's log answers "what happened to this project". One of them alone answers half the question.
///
/// The UPDATE carries `released_at is null` in its WHERE. Releasing twice is therefore refused rather than silently rewriting the
/// first reason: the reason is the record, and a second release with a different one would erase what was said at the time.
///
/// # Errors
/// [`WriteError::NotFound`] for an unknown hold, [`WriteError::Invalid`] for a second release or a blank reason, or
/// `sqlx::Error`.
pub async fn release(
    pool: &PgPool,
    hold_id: i64,
    user_id: i64,
    reason: &str,
) -> Result<Result<Hold, WriteError>, sqlx::Error> {
    let reason = reason.trim();
    if reason.is_empty() {
        return Ok(Err(WriteError::Invalid(
            "a written reason is required to lift a hold: the ledger records who did it, and with no reason it records nothing else"
                .to_string(),
        )));
    }
    let Ok(existing) = one(pool, hold_id).await else {
        return Ok(Err(WriteError::NotFound(format!("no such hold {hold_id}"))));
    };
    if existing.released_at != 0.0 && !mutant("hold-releasable-twice") {
        return Ok(Err(WriteError::Invalid(format!(
            "hold {hold_id} is already released"
        ))));
    }
    let mut tx = pool.begin().await?;
    let updated = sqlx::query(
        "update emergency_holds set released_at = now(), release_reason = $1
          where id = $2 and (released_at is null or $3)",
    )
    .bind(reason)
    .bind(hold_id)
    .bind(mutant("hold-releasable-twice"))
    .execute(&mut *tx)
    .await?;
    if updated.rows_affected() == 0 {
        // Somebody released it between the read and the write. The answer is the same as releasing twice, because that is what
        // happened; a 200 here would claim this caller's reason took effect.
        tx.rollback().await?;
        return Ok(Err(WriteError::Invalid(format!(
            "hold {hold_id} is already released"
        ))));
    }
    sqlx::query(
        "insert into admin_ledger (actor_id, action, subject, justification, project_id, created_at)
         values ($1, 'release_emergency_hold', $2, $3, $4, now())",
    )
    .bind(user_id)
    .bind(format!("consensus call {}", existing.call_id))
    .bind(reason)
    .bind(existing.project_id)
    .execute(&mut *tx)
    .await?;
    sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1, $2, 'release_emergency_hold', 'consensus_call', $3, $4, now())",
    )
    .bind(existing.project_id)
    .bind(user_id)
    .bind(existing.call_id)
    .bind(reason)
    .execute(&mut *tx)
    .await?;
    tx.commit().await?;
    let after = one(pool, hold_id).await.map_err(|error| match error {
        WriteError::Database(error) => error,
        other => sqlx::Error::Protocol(format!("hold vanished after release: {other:?}")),
    })?;
    Ok(Ok(after))
}
