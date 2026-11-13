//! §5.1/§8.6's strategic weight: a proposal, decided by the eligible collaborators, applied only if the world
//! has not moved underneath it.
//!
//! Three rules from the Go store, each of which is a way this becomes a slower version of the absolute power it
//! replaced:
//!
//! * **The proposer cannot consent to their own proposal.** Without this, in a one-collaborator project — where
//!   the quorum floor is capped down to the eligible count, so quorum is 1 — that single consent ratifies it,
//!   and the proposal path is a slower way of doing what the maintainer-only `PUT` used to do.
//! * **The consenter must be in §8.2's eligible population, not merely a member.** With a project whose
//!   eligible count is 0, the quorum threshold is 0, so one consent satisfies quorum and ratifies on its own —
//!   the same outcome the rule above exists to prevent, reached by a different route. The eligible set is
//!   [`crate::consensus::eligible_collaborators`], the same one the arena's distinct-voter condition uses.
//! * **A ratified proposal whose `weight_before` no longer matches the feature is STALE, and nothing is
//!   written.** Somebody changed the weight while the vote was open; applying the proposal would overwrite a
//!   decision with an older one and record it as consensus.
//!
//! A consent below the threshold is recorded and reported as not-yet: §5.1's proposals accumulate, and a first
//! voter told they "failed" stops voting.

use sqlx::{PgPool, Row};

use concord_governance::{evaluate_consensus, ConsensusCounts};

use crate::consensus::eligible_collaborators;

/// One proposed strategic weight, with the decision it was put to.
#[derive(Debug, Clone, serde::Serialize)]
pub struct WeightProposal {
    /// The proposal.
    pub id: i64,
    /// The project.
    pub project_id: i64,
    /// The feature whose weight it changes.
    pub feature_id: i64,
    /// Who proposed it.
    pub proposed_by: i64,
    /// The weight the feature carried when it was proposed.
    pub weight_before: f64,
    /// The weight it would carry.
    pub weight_after: f64,
    /// Why.
    pub rationale: String,
    /// `pending`, `ratified`, `stale`.
    pub status: String,
}

/// What one consent did.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Consented {
    /// The proposal after the vote.
    pub proposal: WeightProposal,
    /// Consents recorded.
    pub consents: i64,
    /// The eligible population that consent was measured against.
    pub eligible: i64,
    /// Whether this consent ratified it.
    pub ratified: bool,
    /// Whether the proposal went stale instead — the weight had already changed.
    pub stale: bool,
}

/// Why a consent was refused.
#[derive(Debug, Clone)]
pub enum Refused {
    /// No such proposal.
    NotFound,
    /// Already decided.
    Decided(String),
    /// The proposer tried to consent to their own proposal.
    SelfConsent,
    /// Not in §8.2's eligible population.
    NotEligible,
    /// The threshold is not met; the consent is recorded.
    NotYet {
        /// The verdict's word.
        verdict: String,
        /// Consents recorded.
        consents: i64,
        /// The eligible population.
        eligible: i64,
    },
    /// The weight changed while the vote was open.
    Stale {
        /// The weight the feature carries now.
        found: f64,
    },
    /// The store failed.
    Failed(String),
}

const COLUMNS: &str = "id, project_id, feature_id, proposed_by, weight_before, weight_after, \
    coalesce(rationale, ''), status";

fn from_row(row: &sqlx::postgres::PgRow) -> Result<WeightProposal, sqlx::Error> {
    Ok(WeightProposal {
        id: row.try_get(0)?,
        project_id: row.try_get(1)?,
        feature_id: row.try_get(2)?,
        proposed_by: row.try_get(3)?,
        weight_before: row.try_get(4)?,
        weight_after: row.try_get(5)?,
        rationale: row.try_get(6)?,
        status: row.try_get(7)?,
    })
}

/// One proposal, or `None`.
///
/// # Errors
/// `sqlx::Error`.
pub async fn proposal(pool: &PgPool, id: i64) -> Result<Option<WeightProposal>, sqlx::Error> {
    let sql = format!("select {COLUMNS} from strategic_weight_proposals where id = $1");
    let row = sqlx::query(&sql).bind(id).fetch_optional(pool).await?;
    match row {
        Some(row) => Ok(Some(from_row(&row)?)),
        None => Ok(None),
    }
}

/// Record one consent to a strategic weight proposal, ratifying if it meets the charter's threshold.
///
/// # Errors
/// `sqlx::Error`, or the [`Refused`] wrapped in it.
pub async fn consent(
    pool: &PgPool,
    proposal_id: i64,
    user_id: i64,
) -> Result<Consented, Refused> {
    let inside = move |error: sqlx::Error| Refused::Failed(error.to_string());
    let Some(proposal) = proposal(pool, proposal_id).await.map_err(inside)? else {
        return Err(Refused::NotFound);
    };
    if proposal.status != "pending" {
        return Err(Refused::Decided(proposal.status));
    }
    if proposal.proposed_by == user_id {
        return Err(Refused::SelfConsent);
    }
    let eligible_ids = eligible_collaborators(pool, proposal.project_id, 0.0)
        .await
        .map_err(inside)?;
    if !eligible_ids.contains(&user_id) {
        return Err(Refused::NotEligible);
    }
    let eligible = eligible_ids.len() as i64;
    let charter = crate::charter::charter(pool, proposal.project_id)
        .await
        .map_err(inside)?;
    let before: i64 = sqlx::query_scalar(
        "select count(*) from strategic_weight_consents where proposal_id = $1",
    )
    .bind(proposal_id)
    .fetch_one(pool)
    .await
    .map_err(inside)?;
    sqlx::query(
        "insert into strategic_weight_consents (proposal_id, user_id, consented_at) \
         values ($1, $2, to_timestamp($3)) on conflict (proposal_id, user_id) do nothing",
    )
    .bind(proposal_id)
    .bind(user_id)
    .bind(crate::now_unix())
    .execute(pool)
    .await
    .map_err(inside)?;
    let consents: i64 = sqlx::query_scalar(
        "select count(*) from strategic_weight_consents where proposal_id = $1",
    )
    .bind(proposal_id)
    .fetch_one(pool)
    .await
    .map_err(inside)?;
    let _ = before;

    let counts = ConsensusCounts {
        consent: consents,
        abstain: 0,
        stand_aside: 0,
        block: 0,
        participants: consents,
        eligible,
        ..ConsensusCounts::default()
    };
    let verdict = evaluate_consensus(&counts, &charter);
    if verdict != concord_governance::Result::Accepted {
        return Err(Refused::NotYet {
            verdict: verdict.as_str().to_string(),
            consents,
            eligible,
        });
    }

    // THE WORLD MOVED CHECK: the feature's weight must still be the one the proposal was written against.
    let current: Option<f64> =
        sqlx::query_scalar("select strategic_weight from features where id = $1")
            .bind(proposal.feature_id)
            .fetch_optional(pool)
            .await
            .map_err(inside)?;
    let found = current.unwrap_or(0.0);
    if (found - proposal.weight_before).abs() > f64::EPSILON {
        sqlx::query(
            "update strategic_weight_proposals set status = 'stale', decided_at = to_timestamp($1) \
              where id = $2 and status = 'pending'",
        )
        .bind(crate::now_unix())
        .bind(proposal_id)
        .execute(pool)
        .await
        .map_err(inside)?;
        audit(
            pool,
            proposal.project_id,
            user_id,
            "strategic_weight_stale",
            "feature",
            proposal.feature_id,
            &format!(
                "proposal {proposal_id}: expected weight {}, found {found}",
                proposal.weight_before
            ),
        )
        .await
        .map_err(inside)?;
        return Err(Refused::Stale { found });
    }

    sqlx::query("update features set strategic_weight = $1 where id = $2")
        .bind(proposal.weight_after)
        .bind(proposal.feature_id)
        .execute(pool)
        .await
        .map_err(inside)?;
    sqlx::query(
        "update strategic_weight_proposals set status = 'ratified', decided_at = to_timestamp($1) \
          where id = $2 and status = 'pending'",
    )
    .bind(crate::now_unix())
    .bind(proposal_id)
    .execute(pool)
    .await
    .map_err(inside)?;
    audit(
        pool,
        proposal.project_id,
        user_id,
        "ratify_strategic_weight",
        "feature",
        proposal.feature_id,
        &format!(
            "proposal {proposal_id} ratified by quorum: {} -> {}",
            proposal.weight_before, proposal.weight_after
        ),
    )
    .await
    .map_err(inside)?;

    Ok(Consented {
        proposal: crate::strategy::proposal(pool, proposal_id)
            .await
            .map_err(inside)?
            .ok_or(Refused::NotFound)?,
        consents,
        eligible,
        ratified: true,
        stale: false,
    })
}

async fn audit(
    pool: &PgPool,
    project_id: i64,
    actor: i64,
    action: &str,
    entity: &str,
    entity_id: i64,
    detail: &str,
) -> Result<(), sqlx::Error> {
    sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at) \
         values (nullif($1,0), $2, $3, $4, $5, $6, to_timestamp($7))",
    )
    .bind(project_id)
    .bind(actor)
    .bind(action)
    .bind(entity)
    .bind(entity_id)
    .bind(detail)
    .bind(crate::now_unix())
    .execute(pool)
    .await?;
    Ok(())
}
