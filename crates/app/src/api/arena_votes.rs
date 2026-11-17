//! The solution arena's pairwise vote (step 3, ADR 0014).
//!
//! This section — request struct, handler and refusal mapping — moved verbatim
//! from `lib.rs`. All four helpers it borrows already live in the crate root.

use axum::{
    extract::{Path, State},
    http::HeaderMap,
    Json, Router,
};
use polaris_core::ApiError;

use crate::{bearer_user, caller_feature, internal, readable_project_segment, AppState};

// ── §5.1: the solution arena's pairwise vote ──────────────────────────────────────────────────────────────────────

/// What a voter sends: the two solutions and which one is better.
#[derive(serde::Deserialize)]
struct VoteSolutionsRequest {
    #[serde(default)]
    solution_a: i64,
    #[serde(default)]
    solution_b: i64,
    #[serde(default)]
    outcome: String,
    #[serde(default)]
    reason: String,
}

/// `POST /projects/{id}/features/{feature}/solutions/vote` — compare two solutions in one feature's arena.
///
/// **A separate route from the feature vote on purpose.** The two arenas rank different things against different
/// competitors, and a shared handler would have to infer which was meant from the id — which is exactly how a solution id
/// ends up being compared against a feature.
///
/// Voting **joins the project** first, as the reference does: a comparison cast by someone with no membership row is a vote
/// the reputation system cannot attribute, and the join is what §8.3's weight is later computed against.
///
/// Both solutions are checked against the feature **before the arena is touched**, so a request naming a solution from
/// another feature cannot spend a vote in this one.
async fn vote_solutions(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<VoteSolutionsRequest>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to vote"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    concord_db::membership::join(&state.pool, project.id, user.id)
        .await
        .map_err(internal)?;

    for id in [body.solution_a, body.solution_b] {
        let solution = concord_db::arena::get_solution(&state.pool, id)
            .await
            .map_err(internal)?;
        match solution {
            Some(solution) if solution.feature_id == feature_id => {}
            Some(_) => {
                return Err(ApiError::forbidden(
                    "a solution in this comparison belongs to another feature".to_string(),
                ))
            }
            None => return Err(ApiError::not_found("no such solution")),
        }
    }

    let arena_id = concord_db::arena::find_solution_arena(&state.pool, feature_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("this feature has no solutions to compare"))?;
    let charter = concord_db::charter::charter(&state.pool, project.id)
        .await
        .map_err(internal)?;

    // §8.3's weight is computed here and NEVER taken from the body: a client-supplied weight is client-supplied influence.
    // The tags of both solutions are the scope, because a vote between a Rust design and a Rust design is a Rust judgement.
    let mut tags: Vec<String> = Vec::new();
    for id in [body.solution_a, body.solution_b] {
        if let Some(solution) = concord_db::arena::get_solution(&state.pool, id).await.map_err(internal)? {
            for tag in concord_ranking::parse_expertise_tags(&solution.expertise_tags) {
                if !tags.contains(&tag) {
                    tags.push(tag);
                }
            }
        }
    }
    let weight = concord_db::voteweight::solution_vote_weight(
        &state.pool,
        project.id,
        user.id,
        &charter,
        &tags,
    )
    .await
    .map_err(internal)?;

    let vote_id = concord_db::arena_writes::cast_vote(
        &state.pool,
        arena_id,
        user.id,
        "solution",
        body.solution_a,
        "solution",
        body.solution_b,
        &body.outcome,
        &body.reason,
        weight,
    )
    .await
    .map_err(vote_refusal)?;

    let solutions = concord_db::arena::list_solutions(&state.pool, feature_id, 50)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({
        "vote": { "id": vote_id, "outcome": body.outcome, "weight": weight },
        "solutions": solutions,
    })))
}

/// A refusal from the arena store, as the status each rule deserves.
fn vote_refusal(refused: concord_db::arena_writes::Refused) -> ApiError {
    use concord_db::arena_writes::Refused;
    match refused {
        Refused::Invalid(detail) => ApiError::invalid_request(detail),
        Refused::NotFound => ApiError::not_found("that entry is not in this arena"),
        Refused::SelfVote => ApiError::forbidden(
            "you proposed one of these: an entry cannot be ranked by the person who wrote it".to_string(),
        ),
        Refused::SameEntity => ApiError::invalid_request("both sides of a comparison must differ"),
        Refused::Database(error) => internal(error),
    }
}



/// The standalone solution-vote route, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new().route(
        "/api/v1/projects/{project_id}/features/{id}/solutions/vote",
        axum::routing::post(vote_solutions),
    )
}
