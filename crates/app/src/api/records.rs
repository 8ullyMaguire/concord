//! Document queries — the schema vocabulary and full-text search (step 3, ADR 0014).
//!
//! Lifted verbatim from `lib.rs`; only visibility and `routes()` assembly are new.
//! `project_for`, `bearer_user`, `readable` and `internal` stay in the app root and are
//! borrowed here rather than copied — a second copy of the visibility rule would drift.

use axum::{
    extract::{Path, Query, State},
    http::HeaderMap,
    Json, Router,
};
use polaris_core::ApiError;

use crate::{bearer_user, internal, project_for, readable, AppState};

/// `GET /api/v1/projects/{slug}/documents/kinds` — the schema's own document vocabulary.
///
/// Static, and deliberately so: the kinds are `store.DocumentKinds`, and deriving them with `select distinct kind`
/// would let one typo invent a kind the writer would then validate against. The route exists so the frontend stops
/// hard-coding the list in a second place.
async fn document_kinds(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The project is resolved first: the kinds are instance-wide, but a route that answers for an unknown project
    // would hand back a list attached to nothing.
    let project = project_for(&state, &slug).await?;
    let user = bearer_user(&state, &headers).await?;
    if !readable(&state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    Ok(Json(serde_json::json!({ "kinds": concord_db::panels::DOCUMENT_KINDS })))
}

/// `GET /api/v1/projects/{slug}/documents/search?q=&limit=` — full text over a project's documents.
///
/// A missing `q` is **400**, while an empty-after-trim `q` is an empty result: "you did not ask" and "you asked for
/// nothing" are different requests, and collapsing them makes a broken client look like a quiet one.
async fn search_documents(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Query(params): Query<std::collections::HashMap<String, String>>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    let user = bearer_user(&state, &headers).await?;
    if !readable(&state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    let Some(query) = params.get("q") else {
        return Err(ApiError::invalid_request("q is required"));
    };
    let limit = params
        .get("limit")
        .and_then(|text| text.parse::<i64>().ok())
        .unwrap_or(0);
    let found = concord_db::panels::search_documents(&state.pool, project.id, query, limit)
        .await
        .map_err(internal)?;
    match found {
        Ok(rows) => Ok(Json(serde_json::json!(rows))),
        // A malformed search expression is the caller's mistake, not the server's.
        Err(message) => Err(ApiError::invalid_request(message)),
    }
}


/// The routes this module owns. Typed on `AppState` so merging does not change the
/// state inferred by the rest of `router()` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route("/api/v1/projects/{slug}/documents/kinds", axum::routing::get(document_kinds))
        .route("/api/v1/projects/{slug}/documents/search", axum::routing::get(search_documents))
}
