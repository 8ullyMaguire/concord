//! Consensus calls: the write side.
//!
//! The rules carried across, in the order they bite:
//!
//! * **a call opens only on work that is not already decided.** `draft`, `discussion`, `ready` and
//!   `consensus` are allowed, and `consensus` is in that list deliberately: the deployed instance holds
//!   87 features parked at `status='consensus'` with no call, and the old rule refused every one of
//!   them, so the status that *claims* a decision is in progress made the only route that starts one
//!   unreachable. What the rule is for is the second call, so that is what it checks.
//! * **one open call per feature**, and at least one linked complaint — a call on a feature with no
//!   evidence is a vote on a rumour.
//! * **opening a call moves the feature to `consensus`.** This was missing entirely, and the
//!   measurement that found it is the number the rule exists to stop: 87 features at
//!   `status='consensus'` with zero `consensus_calls` rows, while a feature with a real call sat in
//!   `discussion` for the whole decision. The board's consensus column named a phase nothing was in.
//! * **a held call does not resolve.** §6.6's emergency hold suspends a call; suspension means it does
//!   not resolve while the hold is in force. That is the entire substance of the power — a hold cannot
//!   override a block or force a result, it can only pause one, so refusing to close a held call is what
//!   stops it being a veto in disguise.
//! * **the verdict is the project's own charter's**, not the default. The two agree until someone tunes
//!   the charter, and a project that raised its consent ratio would otherwise have its calls judged
//!   against the default number while the page showed the tuned one.

use serde_json::{json, Value};
use sqlx::PgPool;

use concord_governance as governance;

use crate::consensus::{call, Call};
use crate::feature_writes::{add_audit, WriteError};

/// A call opens on a feature in one of these phases, or on no feature at all.
const OPENABLE_PHASES: [&str; 4] = ["draft", "discussion", "ready", "consensus"];

/// Open a call.
///
/// `opens_at` and `closes_at` are both `now`: that is what the Go store writes, and the latency code
/// calls it "the open-call trap" — a fresh call is already past its nominal deadline, so anything that
/// decides a call by comparing the clock to `closes_at` must use the extension rules in
/// [`governance::Deadline`] rather than a bare comparison. Reproduced rather than corrected, because a
/// call that suddenly had a seven-day window would change what every open call's deadline means.
///
/// # Errors
/// [`WriteError::Invalid`] when the feature is in the wrong phase, already has an open call, or has no
/// evidence; [`WriteError::Database`] otherwise.
pub async fn create_call(
    pool: &PgPool,
    project_id: i64,
    feature_id: i64,
    opened_by: i64,
    title: &str,
    description: &str,
) -> Result<Call, WriteError> {
    let mut tx = pool.begin().await?;
    if feature_id != 0 {
        let status: Option<String> =
            sqlx::query_scalar("select status from features where id = $1 and project_id = $2")
                .bind(feature_id)
                .bind(project_id)
                .fetch_optional(&mut *tx)
                .await?;
        let Some(status) = status else {
            return Err(WriteError::NotFound(format!("feature {feature_id}")));
        };
        if !OPENABLE_PHASES.contains(&status.as_str()) {
            return Err(WriteError::Invalid(format!(
                "feature must be draft, discussion, ready or consensus (it is {status:?})"
            )));
        }
        let open: i64 = sqlx::query_scalar(
            "select count(*) from consensus_calls where feature_id = $1 and status = 'open'",
        )
        .bind(feature_id)
        .fetch_one(&mut *tx)
        .await?;
        if open > 0 {
            return Err(WriteError::Invalid(
                "a consensus call is already open on this feature".into(),
            ));
        }
        let evidence: i64 =
            sqlx::query_scalar("select count(*) from feature_complaints where feature_id = $1")
                .bind(feature_id)
                .fetch_one(&mut *tx)
                .await?;
        if evidence == 0 {
            return Err(WriteError::Invalid(
                "feature must have at least one linked complaint".into(),
            ));
        }
    }

    // NULL, not 0: `feature_id` is a foreign key and 0 would be a dangling reference rather than an
    // explicit "no feature". The Go store passes a nil `any` for exactly this reason.
    let feature = if feature_id == 0 { None } else { Some(feature_id) };
    let opener = if opened_by > 0 { Some(opened_by) } else { None };
    let id: i64 = sqlx::query_scalar(
        "insert into consensus_calls \
             (project_id, feature_id, opened_by, opens_at, closes_at, status, question, \
              description, public_id) \
         values ($1, $2, $3, now(), now(), 'open', nullif(trim($4), ''), \
                 nullif(trim($5), ''), $6) \
         returning id",
    )
    .bind(project_id)
    .bind(feature)
    .bind(opener)
    .bind(title)
    .bind(description)
    .bind(ulid::Ulid::new().to_string())
    .fetch_one(&mut *tx)
    .await?;

    if feature_id != 0 {
        // The phase moves HERE, because this is where a call begins and both routes that can open one
        // come through it.
        let current: String = sqlx::query_scalar("select status from features where id = $1")
            .bind(feature_id)
            .fetch_one(&mut *tx)
            .await?;
        if current != "consensus" {
            sqlx::query("update features set status = 'consensus', updated_at = now() where id = $1")
                .bind(feature_id)
                .execute(&mut *tx)
                .await?;
        }
    }
    tx.commit().await?;

    if feature_id != 0 {
        add_audit(
            pool,
            project_id,
            opened_by,
            "feature.status",
            "feature",
            feature_id,
            &format!("-> consensus: consensus call #{id} opened"),
        )
        .await?;
    }
    add_audit(pool, project_id, opened_by, "create_consensus_call", "consensus_call", id, title)
        .await?;
    call(pool, id)
        .await?
        .ok_or_else(|| WriteError::NotFound(format!("consensus call {id}")))
}

/// Cast or replace a position.
///
/// An unknown stance is refused rather than stored: `positions.position` is TEXT and a typo would sit in
/// the table looking like a stance the tally ignores, which reads exactly like a voter who was not
/// counted.
///
/// # Errors
/// [`WriteError::Invalid`] for an unknown stance or a closed call; [`WriteError::NotFound`] when the call
/// does not exist.
pub async fn cast_position(
    pool: &PgPool,
    project_id: i64,
    call_id: i64,
    user_id: i64,
    stance: &str,
) -> Result<(), WriteError> {
    let Some(stance) = governance::Stance::parse(stance) else {
        return Err(WriteError::Invalid(
            "position must be one of consent, abstain, stand_aside, block".into(),
        ));
    };
    let status: Option<String> =
        sqlx::query_scalar("select status from consensus_calls where id = $1 and project_id = $2")
            .bind(call_id)
            .bind(project_id)
            .fetch_optional(pool)
            .await?;
    let Some(status) = status else {
        return Err(WriteError::NotFound(format!("consensus call {call_id}")));
    };
    if status == "closed" {
        return Err(WriteError::Invalid(
            "this call is closed; a closed call's tally is its record".into(),
        ));
    }
    sqlx::query(
        "insert into positions (call_id, user_id, position, updated_at) \
         values ($1, $2, $3, now()) \
         on conflict (call_id, user_id) do update set position = excluded.position, \
                                                      updated_at = excluded.updated_at",
    )
    .bind(call_id)
    .bind(user_id)
    .bind(stance.as_str())
    .execute(pool)
    .await?;
    Ok(())
}

/// Record an objection — §6.6's block, in writing.
///
/// # Errors
/// [`WriteError::Invalid`] for a missing principle or violation; [`WriteError::NotFound`] when the call
/// does not exist.
pub async fn create_objection(
    pool: &PgPool,
    project_id: i64,
    call_id: i64,
    user_id: i64,
    principle: &str,
    violation: &str,
    remedy: &str,
) -> Result<i64, WriteError> {
    if principle.trim().is_empty() || violation.trim().is_empty() {
        return Err(WriteError::Invalid(
            "an objection needs a principle and the violation it sees".into(),
        ));
    }
    let exists: Option<i64> =
        sqlx::query_scalar("select id from consensus_calls where id = $1 and project_id = $2")
            .bind(call_id)
            .bind(project_id)
            .fetch_optional(pool)
            .await?;
    if exists.is_none() {
        return Err(WriteError::NotFound(format!("consensus call {call_id}")));
    }
    let id: i64 = sqlx::query_scalar(
        "insert into objections (call_id, user_id, principle, violation, remedy, status, created_at) \
         values ($1, $2, $3, $4, $5, 'open', now()) returning id",
    )
    .bind(call_id)
    .bind(user_id)
    .bind(principle.trim())
    .bind(violation.trim())
    .bind(remedy.trim())
    .fetch_one(pool)
    .await?;
    Ok(id)
}

/// Resolve an objection, in writing.
///
/// The resolution is required: §6.6's override asks for "the objector's remedy answered in writing", and
/// a resolution of "" is an answer nobody gave.
///
/// # Errors
/// [`WriteError::Invalid`] for an empty resolution; [`WriteError::NotFound`] when the objection does not
/// exist.
pub async fn resolve_objection(
    pool: &PgPool,
    objection_id: i64,
    status: &str,
    resolution: &str,
) -> Result<(), WriteError> {
    if resolution.trim().is_empty() {
        return Err(WriteError::Invalid(
            "resolving an objection requires a written answer".into(),
        ));
    }
    if !matches!(status, "upheld" | "withdrawn" | "answered") {
        return Err(WriteError::Invalid(
            "status must be upheld, withdrawn or answered".into(),
        ));
    }
    let affected = sqlx::query(
        "update objections set status = $1, resolution = $2 where id = $3",
    )
    .bind(status)
    .bind(resolution.trim())
    .bind(objection_id)
    .execute(pool)
    .await?
    .rows_affected();
    if affected == 0 {
        return Err(WriteError::NotFound(format!("objection {objection_id}")));
    }
    Ok(())
}

/// Whether an active emergency hold suspends this call, and if so its wording.
async fn active_hold(pool: &PgPool, call_id: i64) -> Result<Option<(String, String, f64)>, WriteError> {
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

/// Close a call and record the verdict.
///
/// A first-window close never overrides: §6.6's pass conditions end with "no unresolved block", and its
/// override is a second window. So an unresolved block ends this call as `blocked`, with the objection
/// recorded and standing.
///
/// # Errors
/// [`WriteError::Invalid`] when the call is held or already closed; [`WriteError::NotFound`] when it does
/// not exist.
pub async fn close_call(
    pool: &PgPool,
    project_id: i64,
    call_id: i64,
) -> Result<Value, WriteError> {
    let Some(found) = call(pool, call_id).await? else {
        return Err(WriteError::NotFound(format!("consensus call {call_id}")));
    };
    if found.project_id != project_id {
        return Err(WriteError::NotFound(format!("consensus call {call_id}")));
    }
    if let Some((grounds, reason, expires_at)) = active_hold(pool, call_id).await? {
        return Err(WriteError::Invalid(format!(
            "consensus call {call_id} is suspended by an emergency hold ({grounds} ground: {reason}) \
             until {expires_at}"
        )));
    }
    let counts = crate::consensus::tally(pool, project_id, call_id).await?;
    // The project's OWN charter, not the default: the two agree until someone tunes it, and a call judged
    // against the default while the page shows the tuned number is the rule enforced with a different
    // number than the one published.
    let charter = crate::charter::charter(pool, project_id).await?;
    let verdict = governance::evaluate_consensus(&counts, &charter);

    // One transaction: the verdict and the call's own record are one decision. Half of it committed is
    // the state that made this necessary — a call stored as accepted over a block still standing.
    let mut tx = pool.begin().await?;
    sqlx::query(
        "update consensus_calls set status = 'closed', closed_at = now(), result = $1 where id = $2",
    )
    .bind(verdict.as_str())
    .bind(call_id)
    .execute(&mut *tx)
    .await?;
    tx.commit().await?;

    // Re-read so the summary describes what was written: the previous version returned the call as it
    // stood BEFORE the close — status "open", result null — a response contradicting its own write.
    let closed = call(pool, call_id)
        .await?
        .ok_or_else(|| WriteError::NotFound(format!("consensus call {call_id}")))?;
    Ok(json!({
        "call": serde_json::to_value(closed).map_err(|error| {
            WriteError::Invalid(format!("serialise the closed call: {error}"))
        })?,
        "counts": {
            "consent": counts.consent,
            "abstain": counts.abstain,
            "stand_aside": counts.stand_aside,
            "block": counts.block,
            "participants": counts.participants,
            "eligible": counts.eligible,
            "open_objections": counts.open_objections,
        },
        "result": verdict.as_str(),
    }))
}

/// §6.5's call on a feature's leading solution.
///
/// Reuses [`create_call`] rather than re-implementing its validation, so a rule cannot be enforced on one
/// path and forgotten on the other. Three things are added after it succeeds:
///
/// * the call is attached to the solution it is about;
/// * `opened_early` is set with the stated reason when the caller exercised §6.5's discretion — flagged,
///   not hidden, so the record says the conditions were not met;
/// * the solution moves to `consensus`, because §6.6 puts it in the decision phase and the board would
///   otherwise show it still in solutioning while a call about it runs.
pub async fn open_solution_call(
    pool: &PgPool,
    project_id: i64,
    feature_id: i64,
    opened_by: i64,
    solution_id: i64,
    early_reason: Option<&str>,
    leader_title: &str,
) -> Result<Call, WriteError> {
    let feature_title: Option<String> =
        sqlx::query_scalar("select title from features where id = $1 and project_id = $2")
            .bind(feature_id)
            .bind(project_id)
            .fetch_optional(pool)
            .await?;
    let Some(feature_title) = feature_title else {
        return Err(WriteError::NotFound(format!("feature {feature_id}")));
    };
    // §6.5 names the call after the solution it is about; without that the agenda reads as a call on the
    // feature, which is what the feature page already says.
    let title = if leader_title.is_empty() {
        feature_title.clone()
    } else {
        format!("{feature_title}: {leader_title}")
    };
    let question = format!("Adopt {leader_title}?");

    let call = create_call(pool, project_id, feature_id, opened_by, &title, &question).await?;

    let mut tx = pool.begin().await?;
    sqlx::query(
        "update consensus_calls set solution_id = $1, opened_early = $2, early_reason = $3 where id = $4",
    )
    .bind(solution_id)
    .bind(i64::from(early_reason.is_some()))
    .bind(early_reason)
    .bind(call.id)
    .execute(&mut *tx)
    .await?;
    sqlx::query("update solutions set status = 'consensus', updated_at = now() where id = $1")
        .bind(solution_id)
        .execute(&mut *tx)
        .await?;
    tx.commit().await?;

    crate::consensus::call(pool, call.id)
        .await?
        .ok_or_else(|| WriteError::NotFound(format!("consensus call {}", call.id)))
}

/// Open a call **on merit**: the arena must already have (a) a solution leading the feature's arena and (b) every §6.5
/// condition met.
///
/// The gate lives here rather than in the handler because it is the same gate the page shows: the readiness report and
/// the refusal to open are two readings of one function, and a handler that re-derived the conditions is a second answer
/// to "may this be called?" that drifts the first time a threshold is tuned.
///
/// # Errors
/// [`WriteError::Conflict`] when the arena has no leader or a condition is unmet, with the unmet condition's own sentence
/// so the caller learns which one; [`WriteError::NotFound`] when the feature is not in the project.
pub async fn open_solution_call_on_merit(
    pool: &PgPool,
    project_id: i64,
    feature_id: i64,
    opened_by: i64,
) -> Result<Call, WriteError> {
    let readiness = crate::arena::readiness(pool, feature_id)
        .await
        .map_err(WriteError::Database)?;
    if readiness.leader_id == 0 {
        return Err(WriteError::Conflict(
            "no solution leads this feature's arena".to_string(),
        ));
    }
    if !readiness.ready {
        return Err(WriteError::Conflict(unmet_condition(&readiness)));
    }
    open_solution_call(
        pool,
        project_id,
        feature_id,
        opened_by,
        readiness.leader_id,
        None,
        &readiness.leader_title,
    )
    .await
}

/// §6.5's discretionary path: "a collaborator may open a call earlier with a stated reason. Early calls are flagged as
/// such."
///
/// **Flagged, not hidden.** The record says the conditions were not met, so the decision does not read as one the arena
/// endorsed. The reason is required (a call opened early with no reason is an override that looks like a decision), and
/// the leader must still exist — an early call is early, not about nothing.
///
/// # Errors
/// [`WriteError::Invalid`] when the reason is blank; [`WriteError::Conflict`] when there is no leader.
pub async fn open_solution_call_early(
    pool: &PgPool,
    project_id: i64,
    feature_id: i64,
    opened_by: i64,
    reason: &str,
) -> Result<Call, WriteError> {
    if reason.trim().is_empty() {
        return Err(WriteError::Invalid(
            "§6.5 requires a stated reason for opening a call before its conditions are met".to_string(),
        ));
    }
    let readiness = crate::arena::readiness(pool, feature_id)
        .await
        .map_err(WriteError::Database)?;
    if readiness.leader_id == 0 {
        return Err(WriteError::Conflict(
            "no solution leads this feature's arena".to_string(),
        ));
    }
    open_solution_call(
        pool,
        project_id,
        feature_id,
        opened_by,
        readiness.leader_id,
        Some(reason.trim()),
        &readiness.leader_title,
    )
    .await
}

/// The unmet conditions, as one sentence a reader can act on.
fn unmet_condition(readiness: &crate::arena::CallReadiness) -> String {
    let unmet: Vec<&str> = readiness
        .conditions
        .iter()
        .filter(|condition| !condition.met)
        .map(|condition| condition.detail.as_str())
        .collect();
    if unmet.is_empty() {
        "the call conditions are not met".to_string()
    } else {
        unmet.join("; ")
    }
}
