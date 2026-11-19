//! The project's feature-priority ranking (step 3, ADR 0014).
//!
//! Deliberately public: the ranking is the published output of a public
//! process. Handler and route moved verbatim from `lib.rs`.

use axum::{
    extract::{Path, Query, State},
    Json, Router,
};
use polaris_core::ApiError;
use crate::pagination::PageQuery;
use serde_json::Value;

use crate::{internal, AppState};

/// `GET /api/v1/projects/{slug}/priorities` — the §6.2 ranking, ordered by its own score.
///
/// Deliberately public. This is the output of a process the public is invited to take part in;
/// requiring a token to read the result while anyone may read the proposals would make the site a
/// black box. The private act is casting a vote, not reading the ranking it produces.
async fn list_priorities(
    State(state): State<AppState>,
    Query(page_query): Query<PageQuery>,
    Path(slug): Path<String>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let page = page_query.valid().map_err(ApiError::invalid_request)?;
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let priorities = concord_db::priorities::feature_priorities(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(crate::pagination::page_json(priorities, &page)?)
}

/// Priority routes, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new().route(
        "/api/v1/projects/{slug}/priorities",
        axum::routing::get(list_priorities),
    )
}
