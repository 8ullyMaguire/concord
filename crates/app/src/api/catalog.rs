//! The catalogue reads: project list and one project (step 3, ADR 0014).
//!
//! The POST on `/api/v1/projects` stays with project creation in `lib.rs`;
//! axum merges the methods for the same path (D-20).

use axum::{
    extract::{Path, Query, State},
    Json, Router,
};
use polaris_core::ApiError;
use crate::pagination::PageQuery;
use serde_json::Value;

use crate::{internal, AppState};

async fn list_projects(
    State(state): State<AppState>,
    Query(page_query): Query<PageQuery>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let page = page_query.valid().map_err(ApiError::invalid_request)?;
    let projects = concord_db::projects::list_projects(&state.pool)
        .await
        .map_err(internal)?;
    Ok(crate::pagination::catalog_json(projects, &page)?)
}

async fn get_project(
    State(state): State<AppState>,
    Path(slug): Path<String>,
) -> Result<Json<concord_db::projects::Project>, ApiError> {
    match concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
    {
        Some(project) => Ok(Json(project)),
        None => Err(ApiError::not_found(format!("project {slug}"))),
    }
}


/// The two catalogue reads, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route("/api/v1/projects", axum::routing::get(list_projects))
        .route("/api/v1/projects/{slug}", axum::routing::get(get_project))
}
