//! Project capabilities: the public catalogue read with each project's claim
//! (step 3, ADR 0014). Shared resolvers stay at the crate root (D-41).

use axum::{
    extract::{Path, State},
    http::HeaderMap,
    Json, Router,
};
use polaris_core::ApiError;

use crate::{internal, project_by_id, readable_project, AppState};

/// `GET /api/v1/projects/{id}/capabilities` — the whole catalogue, each entry with this project's claim.
async fn list_capabilities(
    State(state): State<AppState>,
    Path(project): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The segment may be an id or a slug: the reference's page scripts build these URLs from the SLUG, and a path extractor typed
    // `i64` rejected the slug before any handler ran (`Invalid URL: Cannot parse `suki` to a `i64``). One resolver, asked once, at
    // the top of the handler — the `project_id` the body uses is its id.
    let project_id = project_by_id(&state, &project).await?.id;
    let project = readable_project(&state, project_id, &headers).await?;
    let panel = concord_db::capabilities::panel(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(panel)))
}

/// The reader's GET route, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new().route(
        "/api/v1/projects/{project_id}/capabilities",
        axum::routing::get(list_capabilities),
    )
}
