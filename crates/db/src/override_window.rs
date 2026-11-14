//! §6.6's second window: the ballot that can answer a block.
//!
//! The rules, and why each is a refusal rather than a default:
//!
//! * **it opens only on a call that closed `blocked`.** An override needs a recorded block to override;
//!   a call that closed with a null result (which the store could produce before the seventh pass) has
//!   nothing to open a window on.
//! * **and only while an objection is open.** The window's subject is a standing block. If the objector
//!   withdrew or converted, §6.6's other branch has already resolved it and a vote decides nothing.
//! * **one window, ever.** `UNIQUE(call_id)` is the refusal, not a check: repeating the ballot until the
//!   room yields is attrition, not an override. A conflict names that, because a 409 saying "already has
//!   a window" is actionable and a generic 500 is not.
//! * **below quorum there is no verdict.** Recording "upheld" would claim the room decided against an
//!   override when most of the room did not vote; §6.6 lets the window lapse instead.
//! * **an override needs the objector's remedy answered in writing.** The check happens before any write,
//!   because the verdict is what makes the answer required.
//! * **a held call's window cannot resolve it.** Suspension means the call does not resolve while the
//!   hold is in force, so without this the hold would be bypassable by a route it never heard of.

use serde::Serialize;
use sqlx::PgPool;

use concord_governance as governance;

use crate::consensus::call;
use crate::feature_writes::{add_audit, WriteError};

/// One override window, in the Go struct's wire shape.
///
/// The tally fields are embedded on the window itself rather than nested, because the Go struct embeds
/// them: a client reads `window.consent`, and moving them under `window.tally` would be a different API.
#[derive(Debug, Clone, Serialize)]
pub struct Window {
    pub id: i64,
    pub call_id: i64,
    pub opened_by: i64,
    pub opened_at: f64,
    pub closes_at: f64,
    pub status: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub closed_at: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result: Option<String>,
    pub note: String,
    pub participants: i64,
    pub eligible: i64,
    pub quorum_needed: i64,
    /// Null while the window is open: §6.6's hidden tally applies to this ballot too, and the reader
    /// strips it rather than each handler remembering to.
    pub consent: Option<i64>,
    pub stand_aside: Option<i64>,
    pub block: Option<i64>,
    pub abstain: Option<i64>,
    /// The caller's own stance, `""` when they have not voted.
    pub my_position: String,
}

/// One stance in an override window.
#[derive(Debug, Clone, Serialize)]
pub struct Position {
    pub window_id: i64,
    pub user_id: i64,
    pub position: String,
    pub reason: String,
    pub updated_at: f64,
}

const WINDOW_SQL: &str = "select id, call_id, opened_by, \
     extract(epoch from opened_at)::double precision, \
     extract(epoch from closes_at)::double precision, status, \
     case when closed_at is null then null else extract(epoch from closed_at)::double precision end, \
     result, coalesce(note, '') \
     from override_windows";

/// One window by call id, with its tally as §6.6 allows it to be shown.
///
/// # Errors
/// [`WriteError::NotFound`] when the call has no window; [`WriteError::Database`] otherwise.
pub async fn get(pool: &PgPool, call_id: i64, viewer: i64) -> Result<Window, WriteError> {
    use sqlx::Row;
    let row = sqlx::query(&format!("{WINDOW_SQL} where call_id = $1"))
        .bind(call_id)
        .fetch_optional(pool)
        .await?;
    let Some(row) = row else {
        return Err(WriteError::NotFound(format!(
            "call {call_id} has no override window"
        )));
    };
    let mut window = Window {
        id: row.try_get(0)?,
        call_id: row.try_get(1)?,
        opened_by: row.try_get(2)?,
        opened_at: row.try_get(3)?,
        closes_at: row.try_get(4)?,
        status: row.try_get(5)?,
        closed_at: row.try_get(6)?,
        result: row.try_get(7)?,
        note: row.try_get(8)?,
        participants: 0,
        eligible: 0,
        quorum_needed: 0,
        consent: None,
        stand_aside: None,
        block: None,
        abstain: None,
        my_position: String::new(),
    };
    fill_tally(pool, &mut window, viewer).await?;
    Ok(window)
}

/// Attach the counts the window may show, and the caller's own stance.
async fn fill_tally(pool: &PgPool, window: &mut Window, viewer: i64) -> Result<(), WriteError> {
    use sqlx::Row;
    let project_id: i64 = sqlx::query_scalar("select project_id from consensus_calls where id = $1")
        .bind(window.call_id)
        .fetch_one(pool)
        .await?;
    let rows = sqlx::query(
        "select user_id, position from override_positions where window_id = $1",
    )
    .bind(window.id)
    .fetch_all(pool)
    .await?;
    let (mut consent, mut stand_aside, mut block, mut abstain) = (0_i64, 0_i64, 0_i64, 0_i64);
    for row in &rows {
        let user_id: i64 = row.try_get(0)?;
        let stance: String = row.try_get(1)?;
        match stance.as_str() {
            "consent" => consent += 1,
            "stand_aside" => stand_aside += 1,
            "block" => block += 1,
            "abstain" => abstain += 1,
            _ => {}
        }
        if viewer != 0 && user_id == viewer {
            window.my_position = stance;
        }
    }
    window.participants = rows.len() as i64;
    let charter = crate::charter::charter(pool, project_id).await?;
    window.eligible = crate::consensus::eligible_collaborators(
        pool,
        project_id,
        crate::consensus::DEFAULT_ACTIVITY_WINDOW_DAYS,
    )
    .await?
    .len() as i64;
    window.quorum_needed = governance::quorum_threshold(window.eligible, &charter);
    if window.status == "closed" {
        window.consent = Some(consent);
        window.stand_aside = Some(stand_aside);
        window.block = Some(block);
        window.abstain = Some(abstain);
    }
    Ok(())
}

/// Open §6.6's second window on a call that closed blocked.
///
/// # Errors
/// [`WriteError::Invalid`] for the three refusals above; [`WriteError::Conflict`] when a window exists;
/// [`WriteError::NotFound`] when the call does not.
pub async fn open(
    pool: &PgPool,
    project_id: i64,
    call_id: i64,
    opened_by: i64,
) -> Result<Window, WriteError> {
    let Some(found) = call(pool, call_id).await? else {
        return Err(WriteError::NotFound(format!("consensus call {call_id}")));
    };
    if found.project_id != project_id {
        return Err(WriteError::NotFound(format!("consensus call {call_id}")));
    }
    if found.status != "closed" || found.result.as_deref() != Some(governance::Result::Blocked.as_str()) {
        return Err(WriteError::Invalid(format!(
            "§6.6's override window opens on a call that closed blocked; call {call_id} is {} with result {}",
            found.status,
            found.result.unwrap_or_else(|| "null".into())
        )));
    }
    let open_objections = crate::consensus::objections(pool, call_id)
        .await?
        .iter()
        .filter(|objection| objection.status == "open")
        .count();
    if open_objections == 0 {
        return Err(WriteError::Invalid(format!(
            "call {call_id} closed blocked but no objection is open, so there is no block to override"
        )));
    }
    let charter = crate::charter::charter(pool, project_id).await?;
    let window_days = if charter.vote_window_days > 0.0 {
        charter.vote_window_days
    } else {
        7.0
    };
    let inserted = sqlx::query_scalar::<_, i64>(
        "insert into override_windows (call_id, opened_by, opened_at, closes_at, status, note) \
         values ($1, $2, now(), now() + ($3::double precision * interval '1 day'), 'open', '') \
         returning id",
    )
    .bind(call_id)
    .bind(opened_by)
    .bind(window_days)
    .fetch_one(pool)
    .await;
    let id = match inserted {
        Ok(id) => id,
        Err(error) => {
            // The unique index is the "already has a window" refusal. Distinguishing it from other write
            // errors matters: one is an answer, the other is a bug.
            if error
                .as_database_error()
                .and_then(sqlx::error::DatabaseError::code)
                .is_some_and(|code| code == "23505")
            {
                return Err(WriteError::Conflict(format!(
                    "call {call_id} already has an override window"
                )));
            }
            return Err(WriteError::Database(error));
        }
    };
    add_audit(
        pool,
        project_id,
        opened_by,
        "open_override_window",
        "consensus_call",
        call_id,
        &format!("window {id}"),
    )
    .await?;
    get(pool, call_id, opened_by).await
}

/// Cast or replace a stance in the window.
///
/// # Errors
/// [`WriteError::Invalid`] for an unknown stance or a closed window; [`WriteError::NotFound`] for a
/// missing window.
pub async fn cast_position(
    pool: &PgPool,
    call_id: i64,
    user_id: i64,
    stance: &str,
    reason: &str,
) -> Result<(), WriteError> {
    let Some(stance) = governance::Stance::parse(stance) else {
        return Err(WriteError::Invalid(
            "position must be one of consent, abstain, stand_aside, block".into(),
        ));
    };
    let window = get(pool, call_id, user_id).await?;
    if window.status != "open" {
        return Err(WriteError::Invalid(format!(
            "the override window on call {call_id} is {}; a closed window's tally is its record",
            window.status
        )));
    }
    sqlx::query(
        "insert into override_positions (window_id, user_id, position, reason, updated_at) \
         values ($1, $2, $3, $4, now()) \
         on conflict (window_id, user_id) do update set position = excluded.position, \
                                                         reason = excluded.reason, \
                                                         updated_at = excluded.updated_at",
    )
    .bind(window.id)
    .bind(user_id)
    .bind(stance.as_str())
    .bind(reason.trim())
    .execute(pool)
    .await?;
    Ok(())
}

/// Close the window and record its verdict.
///
/// # Errors
/// [`WriteError::Invalid`] for a closed window, a held call, below quorum, or an override with no written
/// note; [`WriteError::NotFound`] for a missing window.
pub async fn close(
    pool: &PgPool,
    project_id: i64,
    call_id: i64,
    note: &str,
) -> Result<Window, WriteError> {
    let window = get(pool, call_id, 0).await?;
    if window.status != "open" {
        return Err(WriteError::Invalid(format!(
            "the override window on call {call_id} is already {}",
            window.status
        )));
    }
    if let Some((grounds, reason, expires_at)) = active_hold(pool, call_id).await? {
        return Err(WriteError::Invalid(format!(
            "consensus call {call_id} is suspended by an emergency hold ({grounds} ground: {reason}) \
             until {expires_at}, so its override window cannot resolve it"
        )));
    }
    let charter = crate::charter::charter(pool, project_id).await?;
    let mut counts = governance::ConsensusCounts {
        participants: window.participants,
        eligible: window.eligible,
        ..Default::default()
    };
    for position in positions(pool, window.id).await? {
        match position.position.as_str() {
            "consent" => counts.consent += 1,
            "stand_aside" => counts.stand_aside += 1,
            "block" => counts.block += 1,
            "abstain" => counts.abstain += 1,
            _ => {}
        }
    }
    let verdict = governance::evaluate_override(&counts, &charter);
    if verdict == governance::Result::InsufficientQuorum {
        // Below quorum there is no ratio to read, and "upheld" would claim the room decided against an
        // override when most of the room did not vote at all.
        return Err(WriteError::Invalid(format!(
            "§6.6's override window needs {} participants and {} have voted, so there is no verdict to record",
            governance::quorum_threshold(window.eligible, &charter),
            window.participants
        )));
    }
    let trimmed = note.trim();
    if verdict == governance::Result::Overridden && trimmed.is_empty() {
        return Err(WriteError::Invalid(format!(
            "§6.6 overrides a block only with \"the objector's remedy answered in writing\"; this window \
             carries with {} of {} non-abstain participants consenting, so the override needs that answer",
            counts.consent,
            counts.consent + counts.stand_aside + counts.block
        )));
    }

    let mut tx = pool.begin().await?;
    sqlx::query(
        "update override_windows set status = 'closed', closed_at = now(), result = $1, note = $2 \
         where id = $3",
    )
    .bind(verdict.as_str())
    .bind(trimmed)
    .bind(window.id)
    .execute(&mut *tx)
    .await?;
    if verdict == governance::Result::Overridden {
        // The call's own result changes: the block was set aside, so the call is accepted by the override.
        // This is the write that makes the window a decision rather than a poll.
        sqlx::query("update consensus_calls set result = 'accepted_overridden' where id = $1")
            .bind(call_id)
            .execute(&mut *tx)
            .await?;
        // AND the objection stops being open. The reference writes both in one transaction
        // (`UPDATE objections SET status='overridden', resolution=? WHERE call_id=? AND status='open'`), and the page reads the
        // objection's own status: an override that moved the call and left the block reading "open" is a record that contradicts
        // itself — the call says the block was set aside, the block says it is still standing. The suite caught exactly that
        // (`.objection-status` was "open" after the window closed), which is the one place the two facts are compared.
        sqlx::query(
            "update objections set status = 'overridden', resolution = $1 \
             where call_id = $2 and status = 'open'",
        )
        .bind(trimmed)
        .bind(call_id)
        .execute(&mut *tx)
        .await?;
    }
    tx.commit().await?;

    add_audit(
        pool,
        project_id,
        window.opened_by,
        "close_override_window",
        "consensus_call",
        call_id,
        verdict.as_str(),
    )
    .await?;
    get(pool, call_id, 0).await
}

/// The window's stances, oldest first.
///
/// # Errors
/// [`WriteError::Database`] when the read fails.
pub async fn positions(pool: &PgPool, window_id: i64) -> Result<Vec<Position>, WriteError> {
    use sqlx::Row;
    let rows = sqlx::query(
        "select window_id, user_id, position, coalesce(reason, ''), \
                extract(epoch from updated_at)::double precision \
         from override_positions where window_id = $1 order by updated_at asc",
    )
    .bind(window_id)
    .fetch_all(pool)
    .await?;
    rows.iter()
        .map(|row| {
            Ok(Position {
                window_id: row.try_get(0)?,
                user_id: row.try_get(1)?,
                position: row.try_get(2)?,
                reason: row.try_get(3)?,
                updated_at: row.try_get(4)?,
            })
        })
        .collect()
}

/// Whether an active hold suspends this call, and its wording.
async fn active_hold(
    pool: &PgPool,
    call_id: i64,
) -> Result<Option<(String, String, f64)>, WriteError> {
    use sqlx::Row;
    let row = sqlx::query(
        "select grounds, reason, extract(epoch from expires_at)::double precision \
         from emergency_holds \
         where call_id = $1 and released_at is null and expires_at > now() \
         order by created_at desc limit 1",
    )
    .bind(call_id)
    .fetch_optional(pool)
    .await?;
    row.map(|row| {
        Ok((
            row.try_get::<String, _>(0)?,
            row.try_get::<String, _>(1)?,
            row.try_get::<f64, _>(2)?,
        ))
    })
    .transpose()
}
