//! The curation queue reader (step 3, ADR 0014): both spellings of the Go's
//! `/api/v1/curation` mount. Shared resolvers stay at the crate root (D-41).

use axum::{
    extract::{Query, State},
    http::HeaderMap,
    Json, Router,
};
use polaris_core::ApiError;
use serde_json::Value;

use crate::{bearer_user, internal, AppState};

/// `GET /api/v1/curation` — §4's queue: every proposal kind and every reviewed act, one page.
///
/// The default is the queue view, not the log view: `state=open` unless the caller says otherwise, because a
/// page that opened on the whole history is a log with a review button bolted on. `unvoted=1` needs a
/// reader to mean anything, so an anonymous caller asking for it gets the unfiltered list rather than an
/// empty one that looks like good news.
async fn list_curation(
    State(state): State<AppState>,
    Query(query): Query<std::collections::HashMap<String, String>>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let viewer = bearer_user(&state, &headers).await?.map_or(0, |user| user.id);
    let mut filter = concord_db::curation::Filter {
        viewer_id: viewer,
        ..Default::default()
    };
    if let Some(value) = query.get("kind") {
        filter.kind = value.trim().to_string();
    }
    if let Some(value) = query.get("state") {
        filter.state = value.trim().to_string();
    }
    if let Some(value) = query.get("q") {
        filter.text = value.trim().to_string();
    }
    if let Some(value) = query.get("cursor") {
        filter.cursor = value.clone();
    }
    if let Some(slug) = query.get("project") {
        if let Some(project) = concord_db::projects::project_by_slug(&state.pool, slug)
            .await
            .map_err(internal)?
        {
            filter.project_id = project.id;
        }
    }
    if let Some(value) = query.get("actor_id").and_then(|value| value.parse::<i64>().ok()) {
        filter.actor_id = value;
    }
    if let Some(value) = query.get("limit").and_then(|value| value.parse::<i64>().ok()) {
        filter.limit = value;
    }
    if let Some(value) = query.get("since").and_then(|value| value.parse::<f64>().ok()) {
        filter.since = value;
    }
    if let Some(value) = query.get("until").and_then(|value| value.parse::<f64>().ok()) {
        filter.until = value;
    }
    if query.get("unvoted").map(String::as_str) == Some("1") && viewer != 0 {
        filter.unvoted_by = viewer;
    }

    let page = concord_db::curation::list(&state.pool, &filter)
        .await
        .map_err(|error| match error {
            sqlx::Error::Decode(message) => ApiError::invalid_request(message.to_string()),
            other => internal(other),
        })?;
    Ok(Json(serde_json::to_value(page).map_err(internal)?))
}

/// The Go mount answers both `/curation` and `/curation/`; a client that appends a slash must not get a 404.
pub fn routes() -> Router<AppState> {
    Router::new()
        .route("/api/v1/curation", axum::routing::get(list_curation))
        .route("/api/v1/curation/", axum::routing::get(list_curation))
}
