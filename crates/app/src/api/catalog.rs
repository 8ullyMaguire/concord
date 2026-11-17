//! The catalogue reads: project list and one project (step 3, ADR 0014).
//!
//! The POST on `/api/v1/projects` stays with project creation in `lib.rs`;
//! axum merges the methods for the same path (D-20).

use axum::{
    extract::{Path, State},
    Json, Router,
};
use polaris_core::ApiError;
use serde_json::Value;

use crate::{internal, AppState};

async fn list_projects(State(state): State<AppState>) -> Result<Json<Value>, ApiError> {
    let projects = concord_db::projects::list_projects(&state.pool)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(projects).map_err(internal)?))
}

async fn get_project(
    State(state): State<AppState>,
    Path(slug): Path<String>,
) -> Result<Json<Value>, ApiError> {
    match concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
    {
        Some(project) => Ok(Json(serde_json::to_value(project).map_err(internal)?)),
        None => Err(ApiError::not_found(format!("project {slug}"))),
    }
}


/// The two catalogue reads, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route("/api/v1/projects", axum::routing::get(list_projects))
        .route("/api/v1/projects/{slug}", axum::routing::get(get_project))
}
