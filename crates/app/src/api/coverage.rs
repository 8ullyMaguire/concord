//! Coverage claims and their contests (step 3, ADR 0014).
//!
//! The whole §6.3/§6.4 section — two requests, two handlers, the solution
//! resolver and the refusal map — moved verbatim from `lib.rs`.

use axum::{
    extract::{Path, State},
    http::HeaderMap,
    Extension, Json, Router,
};
use polaris_core::ApiError;

use crate::{caller_feature, internal, readable_project_segment, AppState};

// ── §6.3/§6.4: coverage claims and their contests ────────────────────────────────────────────────────────────────

/// What a claim sends.
#[derive(serde::Deserialize)]
struct ClaimCoverageRequest {
    #[serde(default)]
    complaint_id: i64,
    #[serde(default)]
    claim: String,
}

/// What a contest sends.
#[derive(serde::Deserialize)]
struct ContestCoverageRequest {
    #[serde(default)]
    reason: String,
}

/// `POST /projects/{id}/features/{feature}/solutions/{solution}/coverage` — say what this solution covers.
///
/// The response is the whole [`concord_db::coverage_writes::CoverageScore`], not just the ratio: "resolves 340 of 510 pain"
/// is what a reader can act on, and a bare percentage hides both how much is at stake and whether the denominator moved.
///
/// **Restating a claim clears its contest**, which is the answer a contest invites. Leaving it contested would make every
/// contest permanent.
async fn claim_coverage(
    State(state): State<AppState>,
    Path((project_segment, feature, solution)): Path<(String, String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<ClaimCoverageRequest>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let solution_id = solution_in_feature(&state, feature_id, &solution).await?;
    let _ = user;
    concord_db::coverage_writes::claim_coverage(
        &state.pool,
        solution_id,
        body.complaint_id,
        &body.claim,
    )
    .await
    .map_err(coverage_refusal)?;
    let score = concord_db::coverage_writes::coverage_score(&state.pool, feature_id, solution_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({ "coverage": score })))
}

/// `POST /projects/{id}/features/{feature}/solutions/{solution}/coverage/{complaint}/contest` — challenge a claim.
///
/// §6.4: "Coverage claims are challengeable." A contest **needs a reason** (a contest with none is indistinguishable from a
/// deletion) and **the claimant cannot be the contestor** (the same rule §5.2 applies to rankings). The contested claim
/// stops counting until it is restated.
async fn contest_coverage(
    State(state): State<AppState>,
    Path((project_segment, feature, solution, complaint)): Path<(String, String, String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<ContestCoverageRequest>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let solution_id = solution_in_feature(&state, feature_id, &solution).await?;
    let complaint_id: i64 = complaint
        .parse()
        .map_err(|_| ApiError::invalid_request("the complaint id must be a number"))?;
    concord_db::coverage_writes::contest_coverage(
        &state.pool,
        solution_id,
        complaint_id,
        user.id,
        &body.reason,
    )
    .await
    .map_err(coverage_refusal)?;
    let score = concord_db::coverage_writes::coverage_score(&state.pool, feature_id, solution_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({ "coverage": score })))
}

/// The solution named in the path, refusing one that belongs to another feature — the id in the path and the row must agree,
/// or a caller can claim coverage on a solution they never named.
async fn solution_in_feature(
    state: &AppState,
    feature_id: i64,
    segment: &str,
) -> Result<i64, ApiError> {
    let id: i64 = segment
        .parse()
        .map_err(|_| ApiError::invalid_request("the solution id must be a number"))?;
    match concord_db::arena::get_solution(&state.pool, id).await.map_err(internal)? {
        Some(solution) if solution.feature_id == feature_id => Ok(id),
        Some(_) => Err(ApiError::not_found("that solution belongs to another feature")),
        None => Err(ApiError::not_found("no such solution")),
    }
}

/// A refusal from the coverage store, as the status each rule deserves.
fn coverage_refusal(refused: concord_db::coverage_writes::Refused) -> ApiError {
    use concord_db::coverage_writes::Refused;
    match refused {
        Refused::Invalid(detail) => ApiError::invalid_request(detail),
        Refused::NotFound => ApiError::not_found("no such solution or coverage claim"),
        Refused::SelfVote => ApiError::forbidden(
            "an author cannot contest their own coverage claim".to_string(),
        ),
        Refused::Database(error) => internal(error),
    }
}



/// Both coverage writes, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{project_id}/features/{id}/solutions/{solution_id}/coverage",
            axum::routing::post(claim_coverage),
        )
        .route(
            "/api/v1/projects/{project_id}/features/{id}/solutions/{solution_id}/coverage/{complaint_id}/contest",
            axum::routing::post(contest_coverage),
        )
}
