//! Project thread comments and comment votes (step 3, ADR 0014).

use axum::{
    extract::{Path, State},
    http::{header::HeaderMap, StatusCode},
    Extension, Json, Router,
};
use polaris_core::ApiError;
use serde::Deserialize;
use serde_json::Value;

use crate::{
    bearer_user, internal, parse_id, project_by_id, write_error, AppState,
};

/// `GET /api/v1/projects/{slug}/threads/{kind}/{id}` — a whole thread, oldest first.
///
/// An unknown kind is a 400 rather than an empty list: "this thread has no comments" and "this is not a
/// thread kind" are different answers, and an empty list for the second hides a client bug.
async fn thread_comments(
    State(state): State<AppState>,
    Path((project_id, kind, thread_id)): Path<(String, String, String)>,
) -> Result<Json<Value>, ApiError> {
    let project = project_by_id(&state, &project_id).await?;
    let thread_id = parse_id(&thread_id, "thread id")?;
    if !concord_db::comments::THREAD_KINDS.contains(&kind.as_str()) {
        return Err(ApiError::invalid_request(format!(
            "invalid thread_kind {kind:?}"
        )));
    }
    let comments = concord_db::comments::thread(&state.pool, project.id, thread_id, &kind)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(comments).map_err(internal)?))
}

#[derive(Debug, Deserialize, Default)]
struct CommentRequest {
    #[serde(default)]
    body: String,
    #[serde(default)]
    label: String,
    #[serde(default)]
    parent_id: Option<i64>,
}

/// `POST /api/v1/projects/{slug}/threads/{kind}/{id}` — post a comment.
async fn post_comment(
    State(state): State<AppState>,
    Path((project_id, kind, thread_id)): Path<(String, String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<CommentRequest>,
) -> Result<(StatusCode, Json<Value>), ApiError> {
    let project = project_by_id(&state, &project_id).await?;
    let thread_id = parse_id(&thread_id, "thread id")?;
    let comment = concord_db::comments::create(
        &state.pool,
        project.id,
        thread_id,
        &kind,
        user.id,
        &body.body,
        &body.label,
        body.parent_id,
    )
    .await
    .map_err(write_error)?;
    Ok((
        StatusCode::CREATED,
        Json(serde_json::to_value(comment).map_err(internal)?),
    ))
}

/// `DELETE /api/v1/projects/{slug}/threads/{kind}/{id}/{comment}` — soft delete, author or maintainer.
async fn delete_comment(
    State(state): State<AppState>,
    Path((project_id, _kind, _thread_id, id)): Path<(String, String, String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<Json<Value>, ApiError> {
    let project = project_by_id(&state, &project_id).await?;
    let comment_id = parse_id(&id, "comment id")?;
    concord_db::comments::delete(&state.pool, project.id, comment_id, user.id)
        .await
        .map_err(write_error)?;
    Ok(Json(serde_json::json!({ "id": comment_id, "deleted": true })))
}

#[derive(Debug, Deserialize, Default)]
struct CommentVoteRequest {
    #[serde(default)]
    value: i64,
}

/// `POST .../{comment}/vote` — one vote per person, and the score is recomputed from the rows.
async fn vote_comment(
    State(state): State<AppState>,
    Path((project_id, _kind, _thread_id, id)): Path<(String, String, String, String)>,
    headers: HeaderMap,
    Json(body): Json<CommentVoteRequest>,
) -> Result<Json<Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = project_by_id(&state, &project_id).await?;
    let comment_id = parse_id(&id, "comment id")?;
    let score = concord_db::comments::vote(&state.pool, project.id, comment_id, user.id, body.value)
        .await
        .map_err(write_error)?;
    Ok(Json(serde_json::json!({ "id": comment_id, "score": score })))
}

/// Comment routes, typed on `AppState` (D-18). GET+POST on one path
/// stays in one MethodRouter (D-20).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{project_id}/threads/{thread_kind}/{thread_id}",
            axum::routing::get(thread_comments).post(post_comment),
        )
        .route(
            "/api/v1/projects/{project_id}/threads/{thread_kind}/{thread_id}/{id}",
            axum::routing::delete(delete_comment),
        )
        .route(
            "/api/v1/projects/{project_id}/threads/{thread_kind}/{thread_id}/{id}/vote",
            axum::routing::post(vote_comment),
        )
}
