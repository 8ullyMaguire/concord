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
