//! The project's feature-priority ranking (step 3, ADR 0014).
//!
//! Deliberately public: the ranking is the published output of a public
//! process. Handler and route moved verbatim from `lib.rs`.

use axum::{
    extract::{Path, State},
    Json, Router,
};
use polaris_core::ApiError;
use serde_json::Value;

use crate::{internal, AppState};

/// `GET /api/v1/projects/{slug}/priorities` — the §6.2 ranking, ordered by its own score.
///
/// Deliberately public. This is the output of a process the public is invited to take part in;
/// requiring a token to read the result while anyone may read the proposals would make the site a
/// black box. The private act is casting a vote, not reading the ranking it produces.
async fn list_priorities(
    State(state): State<AppState>,
    Path(slug): Path<String>,
) -> Result<Json<Vec<concord_db::priorities::FeaturePriority>>, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let priorities = concord_db::priorities::feature_priorities(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(priorities))
}

/// Priority routes, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new().route(
        "/api/v1/projects/{slug}/priorities",
        axum::routing::get(list_priorities),
    )
}
