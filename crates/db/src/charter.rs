//! A project's charter — the numbers every threshold in the system reads.
//!
//! The rule this exists to enforce: **the charter the verdict is computed with must be the charter the
//! page publishes.** The Go store's `CloseConsensusCall` reads `GetCharterForProject` rather than
//! `DefaultCharter(governance model)`, and the comment there says why — the two agree until someone
//! tunes the charter, and the settings page has a charter editor. A project that raised its consent ratio
//! would otherwise have had its calls judged against the default number while the page showed the tuned
//! one.
//!
//! A project with no charter row is not an error: it is a project using the defaults for its governance
//! model, which is what `create_project` writes on creation.

use sqlx::PgPool;

use concord_governance::{Charter, GovernanceModel};

/// A project's charter, with the model's defaults filling anything the row does not carry.
///
/// # Errors
/// [`sqlx::Error`] when the read fails.
pub async fn charter(pool: &PgPool, project_id: i64) -> Result<Charter, sqlx::Error> {
    use sqlx::Row;
    let model: Option<String> =
        sqlx::query_scalar("select governance_model from projects where id = $1")
            .bind(project_id)
            .fetch_optional(pool)
            .await?;
    let model = model
        .as_deref()
        .and_then(GovernanceModel::parse)
        .unwrap_or(GovernanceModel::Collective);
    let mut charter = Charter::default_for(model);

    let row = sqlx::query(
        "select quorum_ratio, quorum_min, consent_ratio, override_ratio, vote_window_days, \
                merge_requires_quorum, merge_quorum_min, merge_quorum_ratio, \
                require_reviewer_approval, wip_in_progress, wip_review, lam, mu, \
                pain_halflife_days, rep_halflife_days, glicko_tau, vote_weight_cap, \
                support_ratio_min, theme_lifetime_days, \
                coalesce(solution_call_min_voters, 3), \
                coalesce(solution_call_confidence, 0.8), \
                coalesce(solution_stable_hours, 72) \
         from charters where project_id = $1",
    )
    .bind(project_id)
    .fetch_optional(pool)
    .await?;
    let Some(row) = row else {
        return Ok(charter);
    };
    charter.quorum_ratio = row.try_get(0)?;
    charter.quorum_min = row.try_get(1)?;
    charter.consent_ratio = row.try_get(2)?;
    charter.override_ratio = row.try_get(3)?;
    charter.vote_window_days = row.try_get(4)?;
    charter.merge_requires_quorum = row.try_get(5)?;
    charter.merge_quorum_min = row.try_get(6)?;
    charter.merge_quorum_ratio = row.try_get(7)?;
    charter.require_reviewer_approval = row.try_get(8)?;
    charter.wip_in_progress = row.try_get(9)?;
    charter.wip_review = row.try_get(10)?;
    charter.lam = row.try_get(11)?;
    charter.mu = row.try_get(12)?;
    charter.pain_halflife_days = row.try_get(13)?;
    charter.rep_halflife_days = row.try_get(14)?;
    charter.glicko_tau = row.try_get(15)?;
    charter.vote_weight_cap = row.try_get(16)?;
    charter.support_ratio_min = row.try_get(17)?;
    charter.theme_lifetime_days = row.try_get(18)?;
    charter.solution_call_min_voters = row.try_get(19)?;
    charter.solution_call_confidence = row.try_get(20)?;
    charter.solution_stable_hours = row.try_get(21)?;
    Ok(charter)
}

/// Export the charter's JSON shape, as `/api/v1/projects/{slug}/charter` returns it.
#[must_use]
pub fn to_json(charter: &Charter) -> serde_json::Value {
    serde_json::json!({
        "quorum_ratio": charter.quorum_ratio,
        "quorum_min": charter.quorum_min,
        "consent_ratio": charter.consent_ratio,
        "override_ratio": charter.override_ratio,
        "vote_window_days": charter.vote_window_days,
        "merge_requires_quorum": charter.merge_requires_quorum,
        "merge_quorum_min": charter.merge_quorum_min,
        "merge_quorum_ratio": charter.merge_quorum_ratio,
        "require_reviewer_approval": charter.require_reviewer_approval,
        "wip_in_progress": charter.wip_in_progress,
        "wip_review": charter.wip_review,
        "lam": charter.lam,
        "mu": charter.mu,
        "pain_halflife_days": charter.pain_halflife_days,
        "rep_halflife_days": charter.rep_halflife_days,
        "glicko_tau": charter.glicko_tau,
        "vote_weight_cap": charter.vote_weight_cap,
        "support_ratio_min": charter.support_ratio_min,
        "theme_lifetime_days": charter.theme_lifetime_days,
        "solution_call_min_voters": charter.solution_call_min_voters,
        "solution_call_confidence": charter.solution_call_confidence,
        "solution_stable_hours": charter.solution_stable_hours,
    })
}

/// Write a project's charter back (§6.5, §6.2).
///
/// **Four bounds are refusals, not clamps**, and each one guards a switch that can be set to a value which silently
/// disables a mechanism for the whole project — the failure mode where nothing errors and nothing works:
///
/// * `solution_call_confidence` outside `(0, 1)` exclusive — a confidence above 1 can never be met, so §6.5 would never
///   open a call on merit;
/// * `solution_call_min_voters < 1` — a call needs at least one voter;
/// * `solution_stable_hours <= 0` — a leader would have to have held the lead for no time at all;
/// * `theme_lifetime_days` outside `1..=36500` — zero makes every theme lapse the instant it is ratified: ratified,
///   applied, contributing nothing, with the page showing a live theme whose weight counts for zero. The upper bound is a
///   century, past which the field is a typo rather than a policy.
///
/// No `update` when the project has no charter row: the reference answers 200 having changed nothing. That is carried
/// as-is and recorded in the ledger, because a probe on a seeded project would otherwise be asserting behaviour nobody
/// decided — but see [`set_charter`]'s caller: creating the row on demand would be a new product behaviour, not a port.
///
/// `support_ratio_min` is written too, and it is the field the reference's `UPDATE` fires without ever reading back in
/// the ticket that added it — it is a real column and leaving it out would make the editor silently drop a value.
///
/// # Errors
/// [`sqlx::Error`] when the write fails.
pub async fn set_charter(
    pool: &PgPool,
    project_id: i64,
    charter: &Charter,
) -> Result<Result<(), Refused>, sqlx::Error> {
    if charter.solution_call_confidence <= 0.0 || charter.solution_call_confidence >= 1.0 {
        return Ok(Err(Refused(format!(
            "solution_call_confidence must be between 0 and 1 exclusive, got {}",
            charter.solution_call_confidence
        ))));
    }
    if charter.solution_call_min_voters < 1 {
        return Ok(Err(Refused(format!(
            "solution_call_min_voters must be at least 1, got {}",
            charter.solution_call_min_voters
        ))));
    }
    if charter.solution_stable_hours <= 0.0 {
        return Ok(Err(Refused(format!(
            "solution_stable_hours must be positive, got {}",
            charter.solution_stable_hours
        ))));
    }
    if charter.theme_lifetime_days < 1.0 || charter.theme_lifetime_days > 36500.0 {
        return Ok(Err(Refused(format!(
            "theme_lifetime_days must be between 1 and 36500, got {}",
            charter.theme_lifetime_days
        ))));
    }
    sqlx::query(
        "update charters set
             quorum_ratio = $2, quorum_min = $3, consent_ratio = $4,
             support_ratio_min = $5, override_ratio = $6,
             vote_window_days = $7, merge_requires_quorum = $8, merge_quorum_min = $9,
             merge_quorum_ratio = $10, require_reviewer_approval = $11, wip_in_progress = $12,
             wip_review = $13, lam = $14, mu = $15, theme_lifetime_days = $16,
             pain_halflife_days = $17, rep_halflife_days = $18,
             glicko_tau = $19, vote_weight_cap = $20,
             solution_call_min_voters = $21, solution_call_confidence = $22,
             solution_stable_hours = $23
         where project_id = $1",
    )
    .bind(project_id)
    .bind(charter.quorum_ratio)
    .bind(charter.quorum_min)
    .bind(charter.consent_ratio)
    .bind(charter.support_ratio_min)
    .bind(charter.override_ratio)
    .bind(charter.vote_window_days)
    .bind(charter.merge_requires_quorum)
    .bind(charter.merge_quorum_min)
    .bind(charter.merge_quorum_ratio)
    .bind(charter.require_reviewer_approval)
    .bind(charter.wip_in_progress)
    .bind(charter.wip_review)
    .bind(charter.lam)
    .bind(charter.mu)
    .bind(charter.theme_lifetime_days)
    .bind(charter.pain_halflife_days)
    .bind(charter.rep_halflife_days)
    .bind(charter.glicko_tau)
    .bind(charter.vote_weight_cap)
    .bind(charter.solution_call_min_voters)
    .bind(charter.solution_call_confidence)
    .bind(charter.solution_stable_hours)
    .execute(pool)
    .await?;
    Ok(Ok(()))
}

/// A refused charter write, as a message the caller can act on.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Refused(pub String);
