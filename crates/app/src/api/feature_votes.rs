//! The feature arena's pairwise vote (step 3, ADR 0014).
//!
//! Request struct, handler and refusal map lifted verbatim from `lib.rs`.
//! The three borrowed helpers are already `pub(crate)` from earlier moves.

use axum::{
    extract::{Path, State},
    http::HeaderMap,
    Extension, Json, Router,
};
use polaris_core::ApiError;

use crate::{caller_feature, internal, readable_project_segment, AppState};

// ── §5.1: the feature vote (what to build) ──────────────────────────────────────────────────────────────────────

/// What a voter sends: the two features and which matters more.
///
/// The reference's body names them `feature_a`/`feature_b`; both spellings are accepted because the port's own clients have
/// used `a`/`b` on the solution arena and a client that swaps one for the other should get a comparison, not a 400 about a
/// field name.
#[derive(serde::Deserialize)]
struct CastFeatureVoteRequest {
    #[serde(default, alias = "feature_a")]
    a: i64,
    #[serde(default, alias = "feature_b")]
    b: i64,
    #[serde(default)]
    outcome: String,
}

/// `POST /projects/{id}/features/{feature}/vote` — rank this feature against another.
///
/// The feature named in the path is the **left** side, so a client that names one feature and a rival gets the comparison it
/// meant; `a` in the body may repeat it.
///
/// **A first vote enrols the voter**, and the join happens *before* the weight is computed — a voter with no membership row
/// has no reputation to weigh. The reference's comment records the same ordering bug being found the hard way: refusing
/// guests first made the very first vote impossible.
///
/// The weight is `VoteWeight(reputation, 1.0, cap)` times §12.3's age gate: **the role is not a multiplier here**, unlike the
/// solution vote.
async fn cast_feature_vote(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<CastFeatureVoteRequest>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let charter = concord_db::charter::charter(&state.pool, project.id)
        .await
        .map_err(internal)?;
    let weight = concord_db::voteweight::feature_vote_weight(&state.pool, project.id, user.id, &charter)
        .await
        .map_err(internal)?;
    let left = if body.a == 0 { feature_id } else { body.a };
    let vote_id = concord_db::votes_writes::record_feature_vote(
        &state.pool,
        project.id,
        user.id,
        concord_db::votes_writes::NewFeatureVote {
            feature_a: left,
            feature_b: body.b,
            outcome: body.outcome.clone(),
        },
        weight,
        charter.glicko_tau,
    )
    .await
    .map_err(feature_vote_refusal)?;
    // Enrolment happens AFTER the vote is recorded, which is what the reference's own comment says and what its code did not
    // do: a vote refused for ranking your own feature must not add you to the project. The weight is unaffected — a voter with
    // no membership row has no reputation in the project either way, and this arena does not use the role.
    concord_db::membership::join(&state.pool, project.id, user.id)
        .await
        .map_err(internal)?;
    // The audit line and the reputation point are what make participating worth anything (§8.3 counts "good-faith consensus
    // participation"); neither is allowed to fail the vote itself, because the vote has already been recorded.
    let _ = sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1,$2,'cast_vote','vote',$1,$3,now())",
    )
    .bind(project.id)
    .bind(user.id)
    .bind(format!("feature_a={left} feature_b={} outcome={}", body.b, body.outcome))
    .execute(&state.pool)
    .await;
    let _ = sqlx::query(
        "insert into reputation_events (project_id, user_id, kind, points, created_at)
         values ($1,$2,'vote',1,now())",
    )
    .bind(project.id)
    .bind(user.id)
    .execute(&state.pool)
    .await;
    Ok(Json(serde_json::json!({
        "vote": { "id": vote_id, "outcome": body.outcome, "weight": weight },
    })))
}

/// A refusal from the feature-vote store, as the status each rule deserves.
fn feature_vote_refusal(refused: concord_db::votes_writes::Refused) -> ApiError {
    use concord_db::votes_writes::Refused;
    match refused {
        Refused::Invalid(detail) => ApiError::invalid_request(detail),
        Refused::NotFound => ApiError::forbidden(
            "both features must belong to this project".to_string(),
        ),
        Refused::SelfVote => ApiError::forbidden(
            "you proposed one of these features: a feature cannot be ranked by the person who proposed it".to_string(),
        ),
        Refused::Database(error) => internal(error),
    }
}



/// The standalone feature-vote route, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new().route(
        "/api/v1/projects/{slug}/features/{feature}/vote",
        axum::routing::post(cast_feature_vote),
    )
}
