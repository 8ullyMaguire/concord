//! §6.5's five outcomes: what the call decided about the *solution*.
//!
//! Distinct from [`concord_governance::Result`], which is the verdict on the *call*. Both live on the same
//! row and mean different things, which is exactly why the guards below check `outcome` and not `status`:
//! the deployed store guarded on `status`, and that made the decision record unreachable in the flow the
//! page actually offers — the maintainer closes the call (§6.8, which sets `status='closed'`), and then
//! recording the outcome was refused as "already closed". The record was writable only by NOT closing the
//! call, the opposite of what the spec asks for.
//!
//! Three more rules, each a refusal that keeps the board honest:
//!
//! * **a held call cannot record an outcome** — recording one resolves the call, and a hold that can be
//!   walked around by choosing another route is not a pause.
//! * **"sent back" is not a rejection.** The call decided not to decide, so the feature returns to
//!   discussion. Writing `rejected` would record a decision nobody made.
//! * **a fall-back must name a different solution.** Falling back to the solution the call was about is a
//!   no-op that leaves the board where it started while claiming progress.

use serde_json::Value;
use sqlx::PgPool;

use crate::feature_writes::{add_audit, WriteError};

/// The adopted solution becomes ready.
pub const ACCEPTED: &str = "accepted";
/// Adopted with amendments, and a derived solution is spawned with the amendment text.
pub const AMENDED: &str = "accepted-with-amendments";
/// The runner-up takes over: §6.5's ranking IS the fallback chain, so no second vote is needed.
pub const FALL_BACK: &str = "fall-back";
/// Rejected.
pub const REJECTED: &str = "rejected";
/// The call decided not to decide: the feature returns to discussion.
pub const SENT_BACK: &str = "sent-back";

/// Every outcome, so a typo is a refusal rather than a status the board never learns.
pub const OUTCOMES: [&str; 5] = [ACCEPTED, AMENDED, FALL_BACK, REJECTED, SENT_BACK];

/// The amendment text an accepted-with-amendments outcome carries.
#[derive(Debug, Clone)]
pub struct Amendments {
    /// Who authored the amended solution.
    pub author_id: i64,
    /// Its title.
    pub title: String,
    /// Its body.
    pub body: String,
    /// §6.3's solution type.
    pub solution_type: String,
}

/// The phase a feature belongs in after an outcome.
///
/// `None` for an outcome this switch has not learned: a status invented for it would move the board on a
/// decision nobody can name.
#[must_use]
pub fn feature_phase_for_outcome(outcome: &str) -> Option<(&'static str, &'static str)> {
    match outcome {
        ACCEPTED | AMENDED => Some(("ready", "adopted")),
        FALL_BACK => Some(("ready", "fell back to the runner-up")),
        REJECTED => Some(("rejected", "rejected")),
        SENT_BACK => Some(("discussion", "sent back to discussion")),
        _ => None,
    }
}

/// Record §6.5's outcome for a call, and write the decision record the spec requires.
///
/// # Errors
/// [`WriteError::Conflict`] when the call was already decided or is held; [`WriteError::Invalid`] for an
/// unknown outcome, a call that is not about a solution or has no feature, an amended outcome without the
/// amendment text, or a fall-back that names nothing or names itself.
pub async fn record(
    pool: &PgPool,
    project_id: i64,
    call_id: i64,
    outcome: &str,
    summary: &str,
    fallback_to: i64,
    amendments: Option<&Amendments>,
    actor_id: i64,
) -> Result<Value, WriteError> {
    if !OUTCOMES.contains(&outcome) {
        return Err(WriteError::Invalid(format!(
            "outcome must be one of {OUTCOMES:?}, not {outcome:?}"
        )));
    }
    let Some(found) = crate::consensus::call(pool, call_id).await? else {
        return Err(WriteError::NotFound(format!("consensus call {call_id}")));
    };
    if found.project_id != project_id {
        return Err(WriteError::NotFound(format!("consensus call {call_id}")));
    }
    // The guard is "already decided", NOT "already closed": the page closes the call first, and the
    // decision record has to be writable after that.
    if found.outcome.is_some() {
        return Err(WriteError::Conflict(format!(
            "call {call_id} was already decided: {}",
            found.outcome.unwrap_or_default()
        )));
    }
    if let Some(hold) = crate::holds::active_for_call(pool, call_id).await? {
        return Err(WriteError::Conflict(format!(
            "consensus call {call_id} is suspended by an emergency hold ({} ground) until {}",
            hold.grounds, hold.expires_at
        )));
    }
    let Some(solution_id) = found.solution_id else {
        return Err(WriteError::Invalid(format!(
            "call {call_id} is not about a solution, so §6.5's outcomes do not apply"
        )));
    };
    if found.feature_id == 0 {
        return Err(WriteError::Invalid(format!(
            "call {call_id} has no feature, so there is no board to move"
        )));
    }
    if outcome == AMENDED && amendments.is_none() {
        return Err(WriteError::Invalid(
            "an accepted-with-amendments outcome needs the amendment text".into(),
        ));
    }
    if outcome == FALL_BACK && fallback_to == 0 {
        return Err(WriteError::Invalid(
            "fall-back must name the solution that takes over".into(),
        ));
    }
    if outcome == FALL_BACK && fallback_to == solution_id {
        return Err(WriteError::Invalid(
            "fall-back must name a different solution".into(),
        ));
    }

    // The amendment is spawned BEFORE the outcome is written, so a failure here leaves the call open
    // rather than recording an outcome whose artifact is missing.
    let mut amended_solution = 0_i64;
    if let Some(amendments) = amendments.filter(|_| outcome == AMENDED) {
        amended_solution = sqlx::query_scalar(
            "insert into solutions (feature_id, author_id, title, body, type, parent_solution_id) \
             values ($1, $2, $3, $4, $5, $6) returning id",
        )
        .bind(found.feature_id)
        .bind(amendments.author_id)
        .bind(&amendments.title)
        .bind(&amendments.body)
        .bind(&amendments.solution_type)
        .bind(solution_id)
        .fetch_one(pool)
        .await?;
    }

    let mut tx = pool.begin().await?;
    // `status = 'closed'` as well as `closed_at`: recording an outcome while leaving the call open lets a
    // second outcome overwrite the first, so the "already decided" guard above would never fire.
    sqlx::query(
        "update consensus_calls set status = 'closed', outcome = $1, \
                fallback_to_solution_id = $2, summary = nullif(trim($3), ''), closed_at = now() \
         where id = $4",
    )
    .bind(outcome)
    .bind(if fallback_to != 0 { Some(fallback_to) } else { None })
    .bind(summary)
    .bind(call_id)
    .execute(&mut *tx)
    .await?;

    // The adopted solution is ready, the displaced one is rejected, and the fallback is ready too: §6.5
    // says the ranking IS the fallback chain, so the runner-up does not need a second vote.
    match outcome {
        ACCEPTED | AMENDED => {
            sqlx::query("update solutions set status = 'ready', updated_at = now() where id = $1")
                .bind(solution_id)
                .execute(&mut *tx)
                .await?;
        }
        FALL_BACK => {
            sqlx::query("update solutions set status = 'rejected', updated_at = now() where id = $1")
                .bind(solution_id)
                .execute(&mut *tx)
                .await?;
            sqlx::query("update solutions set status = 'ready', updated_at = now() where id = $1")
                .bind(fallback_to)
                .execute(&mut *tx)
                .await?;
        }
        REJECTED => {
            sqlx::query("update solutions set status = 'rejected', updated_at = now() where id = $1")
                .bind(solution_id)
                .execute(&mut *tx)
                .await?;
        }
        SENT_BACK => {
            // Neither rejected nor ready: the call did not decide.
            sqlx::query("update solutions set status = 'discussion', updated_at = now() where id = $1")
                .bind(solution_id)
                .execute(&mut *tx)
                .await?;
        }
        _ => {}
    }
    if amended_solution != 0 {
        sqlx::query("update solutions set status = 'ready', updated_at = now() where id = $1")
            .bind(amended_solution)
            .execute(&mut *tx)
            .await?;
    }
    tx.commit().await?;

    // §6.5's outcomes decide what happens to the FEATURE, not only to its solutions. Without this the
    // decision was recorded in four tables and the board still showed the idea where it was before the vote.
    if let Some((status, why)) = feature_phase_for_outcome(outcome) {
        // Actor 0: this move is made by the DECISION, named by call id in the audit row. Attributing it to
        // whoever pressed "record outcome" would misplace it.
        if let Some(current) = crate::features::feature(pool, found.feature_id).await? {
            if current.status != status {
                sqlx::query("update features set status = $1, updated_at = now() where id = $2")
                    .bind(status)
                    .bind(found.feature_id)
                    .execute(pool)
                    .await?;
                add_audit(
                    pool,
                    project_id,
                    0,
                    "feature.status",
                    "feature",
                    found.feature_id,
                    &format!("-> {status}: {why}: call #{call_id}"),
                )
                .await?;
            }
        }
    }

    write_decision_record(
        pool,
        project_id,
        call_id,
        found.feature_id,
        found.solution_id.unwrap_or(0),
        outcome,
        summary,
        fallback_to,
        amended_solution,
        actor_id,
    )
    .await?;

    let recorded = crate::consensus::call(pool, call_id)
        .await?
        .ok_or_else(|| WriteError::NotFound(format!("consensus call {call_id}")))?;
    serde_json::to_value(recorded)
        .map_err(|error| WriteError::Invalid(format!("serialise the call: {error}")))
}

/// §6.5's decision record, as a project document of kind `adr`.
///
/// An existing table rather than a new one: `project_documents` already carries `kind='adr'` with
/// `UNIQUE (project_id, kind, slug)`, and a second ADR store would be the second representation of one
/// fact.
///
/// The ranking is reproduced as the snapshot it was at the decision. Asked a year later, a live ranking
/// answers "what would we do now", which is a different question from the one this record exists to
/// answer: "why this and not the runner-up, then".
#[allow(clippy::too_many_arguments)]
async fn write_decision_record(
    pool: &PgPool,
    project_id: i64,
    call_id: i64,
    feature_id: i64,
    solution_id: i64,
    outcome: &str,
    summary: &str,
    fallback_to: i64,
    amended_solution: i64,
    author_id: i64,
) -> Result<(), WriteError> {
    use sqlx::Row;
    let positions = crate::consensus::positions(pool, call_id).await?;
    let objections = crate::consensus::objections(pool, call_id).await?;
    let feature_title: String = sqlx::query_scalar("select title from features where id = $1")
        .bind(feature_id)
        .fetch_optional(pool)
        .await?
        .unwrap_or_default();

    let mut body = format!("# Decision: {feature_title}\n\n- **Outcome:** {outcome}\n");
    if !summary.trim().is_empty() {
        body.push_str(&format!("- **Summary:** {}\n", summary.trim()));
    }
    if fallback_to != 0 {
        body.push_str(&format!("- **Fell back to:** solution #{fallback_to}\n"));
    }
    if amended_solution != 0 {
        body.push_str(&format!("- **Amended into:** solution #{amended_solution}\n"));
    }
    body.push_str("\n## Positions\n\n");
    if positions.is_empty() {
        body.push_str("Nobody cast a position.\n");
    } else {
        body.push_str("| User | Position | Reason |\n|---|---|---|\n");
        for position in &positions {
            body.push_str(&format!(
                "| {} | {} | {} |\n",
                position.user_id, position.position, position.reason
            ));
        }
    }
    body.push_str("\n## Objections\n\n");
    if objections.is_empty() {
        body.push_str("No objection was filed.\n");
    } else {
        for objection in &objections {
            body.push_str(&format!(
                "### {}\n\n- **Violates:** {}\n- **Remedy:** {}\n- **Status:** {} — {}\n\n",
                objection.principle, objection.violation, objection.remedy,
                objection.status, objection.resolution
            ));
        }
    }
    // The solutions the decision was made against, in the order the store can defend.
    //
    // NOT ordered by §6.4's score: `solutions` carries no rating, and the score is computed from the
    // feature's arena (`arena_entries`, coverage, kappa, distinct voters). Reproducing that here would be a
    // second implementation of the arena's arithmetic, which is the duplication the port exists to avoid.
    // So this lists the feature's solutions with their own status, and says where the score lives.
    let ranking = sqlx::query(
        "select id, title, status from solutions where feature_id = $1 order by id asc limit 50",
    )
    .bind(feature_id)
    .fetch_all(pool)
    .await?;
    if !ranking.is_empty() {
        body.push_str("\n## The solutions this decision was made against\n\n");
        body.push_str("§6.5: the ranking is the fallback chain, so the order the arena records is where a\n");
        body.push_str("stalled or blocked call falls back to, without a second vote. That ORDER and its\n");
        body.push_str("scores are §6.4's, computed from the feature's arena and recorded there; this table\n");
        body.push_str("records the solutions and the state each was left in.\n\n");
        body.push_str("| # | Solution | Status | Note |\n|---|---|---|---|\n");
        for (index, row) in ranking.iter().enumerate() {
            use sqlx::Row;
            let id: i64 = row.try_get(0)?;
            let title: String = row.try_get(1)?;
            let status: String = row.try_get(2)?;
            let note = if id == solution_id {
                if matches!(outcome, ACCEPTED | AMENDED) {
                    "the solution this call was about (adopted)"
                } else if outcome == FALL_BACK {
                    "the solution this call was about (displaced)"
                } else {
                    "the solution this call was about (not adopted)"
                }
            } else if id == fallback_to {
                "the fallback"
            } else if id == amended_solution {
                "the amendment"
            } else {
                ""
            };
            body.push_str(&format!("| {} | {title} | {status} | {note} |\n", index + 1));
        }
    }

    let slug = format!("decision-call-{call_id}");
    // `on conflict ... do update`: re-recording (which the guard makes impossible) or a retried write must
    // not fail the whole outcome on a unique collision.
    sqlx::query(
        "insert into project_documents (project_id, kind, slug, title, body, author_id, public_id) \
         values ($1, 'adr', $2, $3, $4, $5, $6) \
         on conflict (project_id, kind, slug) do update set body = excluded.body, \
                                                             title = excluded.title, \
                                                             updated_at = now()",
    )
    .bind(project_id)
    .bind(&slug)
    .bind(format!("Decision: {feature_title}"))
    .bind(&body)
    .bind(author_id)
    .bind(ulid::Ulid::new().to_string())
    .execute(pool)
    .await?;

    add_audit(
        pool,
        project_id,
        author_id,
        "record_outcome",
        "consensus_call",
        call_id,
        outcome,
    )
    .await?;
    Ok(())
}
