//! A voter's influence in a project — §8.3's formula, in the one place it is computed.
//!
//! The tempting shortcut is a simpler weight for the smaller surfaces ("it is only an answer vote"). That is exactly how
//! an account manufactures influence: §8.3's terms exist so reputation, role and account age all have to be earned, and
//! a vote weight is a vote weight whatever it is voting on.
//!
//! Two deliberate omissions, both ported:
//!
//! * **no expertise term for answers.** §8.3 multiplies in an expertise multiplier scoped to the items being compared.
//!   Solutions have `expertise_tags`; a request answer has no tags and there is no reader behind `request_tags`. Reusing
//!   the solution's tags or the voter's whole tag reputation would apply a multiplier computed from an unrelated
//!   question, which is worse than omitting one the data cannot support;
//! * **reputation is a plain `sum(points)`**, not decayed by `rep_halflife_days`. The charter carries a half-life but the
//!   reference's `GetReputation` never applies it; the port follows the reference and this note exists so the difference
//!   is a decision rather than a discovery.

use sqlx::PgPool;

use concord_governance::Charter;

/// This voter's weight on a request answer in `project_id`.
///
/// # Errors
/// `sqlx::Error`.
pub async fn answer_vote_weight(
    pool: &PgPool,
    project_id: i64,
    actor: i64,
    charter: &Charter,
) -> Result<f64, sqlx::Error> {
    let reputation: f64 = sqlx::query_scalar(
        "select coalesce(sum(points), 0)::double precision from reputation_events
          where project_id = $1 and user_id = $2",
    )
    .bind(project_id)
    .bind(actor)
    .fetch_one(pool)
    .await?;
    let role: Option<String> =
        sqlx::query_scalar("select role from members where project_id = $1 and user_id = $2")
            .bind(project_id)
            .bind(actor)
            .fetch_optional(pool)
            .await?;
    let mut weight = concord_ranking::vote_weight(
        reputation,
        concord_ranking::role_multiplier(role.as_deref().unwrap_or("user")),
        charter.vote_weight_cap,
    );

    // §12.3's Sybil defence, applied LAST: a defence an expertise multiplier can halve is not a defence.
    let created_at: Option<f64> = sqlx::query_scalar(
        "select extract(epoch from created_at)::double precision from users where id = $1",
    )
    .bind(actor)
    .fetch_optional(pool)
    .await?;
    if let Some(created_at) = created_at {
        weight *= concord_ranking::age_gate_multiplier(concord_ranking::account_age_days(
            created_at,
            crate::now_unix(),
        ));
    }
    Ok(weight.clamp(0.0, charter.vote_weight_cap))
}
