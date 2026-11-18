//! The reader's own notification inbox (step 3, ADR 0014).
//!
//! All three handlers and their route stanzas lifted verbatim from `lib.rs`.
//! `parse_id` is borrowed from the crate root rather than copied (D-19).

use axum::{
    extract::{Path, Query, State},
    Extension, Json, Router,
};
use serde_json::Value;
use polaris_core::ApiError;

use crate::{internal, parse_id, AppState};

/// `GET /api/v1/notifications` — the reader's own inbox, newest first, with the badge.
///
/// `limit` outside `1..=200` becomes 50 rather than a 400: the header asks for this on every render, and a
/// bad numeral must not take the page down with it.
async fn list_notifications(
    State(state): State<AppState>,
    Query(query): Query<std::collections::HashMap<String, String>>,
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<Json<concord_db::notifications::Inbox>, ApiError> {
    let limit = query
        .get("limit")
        .and_then(|value| value.parse::<i64>().ok())
        .unwrap_or(50);
    let inbox = concord_db::notifications::list(&state.pool, user.id, limit)
        .await
        .map_err(internal)?;
    Ok(Json(inbox))
}

/// `POST /api/v1/notifications/{id}/read` — idempotent, and only ever the reader's own row.
async fn mark_notification_read(
    State(state): State<AppState>,
    Path(id): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<Json<Value>, ApiError> {
    let id = parse_id(&id, "notification id")?;
    match concord_db::notifications::mark_read(&state.pool, user.id, id).await {
        Ok(()) => Ok(Json(serde_json::json!({ "id": id, "read": true }))),
        // Not found and not yours are the same answer: a distinct one would confirm the id is real.
        Err(sqlx::Error::RowNotFound) => Err(ApiError::not_found(format!("notification {id}"))),
        Err(error) => Err(internal(error)),
    }
}

/// `POST /api/v1/notifications/read-all` — clears the badge and says how many rows it took.
async fn mark_all_notifications_read(
    State(state): State<AppState>,
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<Json<Value>, ApiError> {
    let cleared = concord_db::notifications::mark_all_read(&state.pool, user.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({ "read": cleared })))
}


/// The three notification routes, typed on `AppState` (D-18). They were one contiguous
/// stanza group in the old chain; merging them keeps the same paths and methods.
pub fn routes() -> Router<AppState> {
    Router::new()
        .route("/api/v1/notifications", axum::routing::get(list_notifications))
        .route("/api/v1/notifications/read-all", axum::routing::post(mark_all_notifications_read))
        .route("/api/v1/notifications/{id}/read", axum::routing::post(mark_notification_read))
}
