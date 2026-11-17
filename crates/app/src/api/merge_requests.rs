//! Merge requests (step 3, ADR 0014): list, open, approve, execute, reject.
//!
//! The §5.7 block — handlers, path resolver and refusal map — moved verbatim
//! from `lib.rs`. Document writes that followed it in the same source section
//! stay in `lib.rs` and are a separate resource.

use axum::{
    extract::{Path, State},
    http::{header::HeaderMap, StatusCode},
    Extension, Json, Router,
};
use polaris_core::ApiError;

use crate::{internal, readable_project_segment, require_role, AppState};

// ── §5.7: merge requests ──────────────────────────────────────────────────────────────────────────────────────────

/// What a merge request needs: the feature it lands, and a title.
#[derive(serde::Deserialize)]
struct CreateMergeRequest {
    #[serde(default)]
    feature_id: i64,
    #[serde(default)]
    title: String,
}

/// `GET /projects/{id}/merge` — the project's merge requests, newest first, each with its §5.7 gate.
///
/// The gate is served **with** the request because a client that re-derives the quorum is a second implementation of §5.7, and
/// the one that gets it wrong shows a filled quorum the server refuses to honour.
async fn list_merge_requests(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let rows = concord_db::merge_writes::list(&state.pool, project.id)
        .await
        .map_err(internal)?;
    let mut out = Vec::with_capacity(rows.len());
    for row in rows {
        let state_gate = concord_db::merge_writes::gate(&state.pool, &row)
            .await
            .map_err(merge_refusal)?;
        out.push(serde_json::json!({ "request": row, "gate": state_gate }));
    }
    Ok(Json(serde_json::json!(out)))
}

/// `POST /projects/{id}/merge` — open a merge request for a feature.
///
/// A contributor may open one, and doing so earns §8.3's `create_merge_request` reputation — which is also what a member's tag
/// reputation is derived from, so an unmerged request earns the opening and nothing else.
async fn create_merge_request(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<CreateMergeRequest>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    if body.feature_id == 0 {
        return Err(ApiError::invalid_request("a merge request needs a feature"));
    }
    // The feature must be in this project: a merge request filed against somebody else's feature would ship it from here.
    let feature = concord_db::features::feature(&state.pool, body.feature_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such feature"))?;
    if feature.project_id != project.id {
        return Err(ApiError::not_found("no such feature"));
    }
    let title = if body.title.trim().is_empty() {
        feature.title.clone()
    } else {
        body.title.clone()
    };
    let request = concord_db::merge_writes::create(
        &state.pool,
        project.id,
        body.feature_id,
        user.id,
        &title,
    )
    .await
    .map_err(internal)?;
    let _ = sqlx::query(
        "insert into reputation_events (project_id, user_id, kind, points, created_at)
         values ($1,$2,'create_merge_request',3,now())",
    )
    .bind(project.id)
    .bind(user.id)
    .execute(&state.pool)
    .await;
    Ok((StatusCode::CREATED, Json(request)))
}

/// `PUT /projects/{id}/merge/{mr}/approve` — one collaborator's confirmation.
///
/// Two rules the reference learned the hard way: **the approver must be a member with role ≥ contributor**, not "an eligible
/// collaborator" — approving somebody else's merge request IS a qualifying contribution, and requiring eligibility first would
/// make that route unreachable — and **the author cannot approve their own request**.
async fn approve_merge(
    State(state): State<AppState>,
    Path((project_segment, merge)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let mr_id = merge_id(&state, &project, &merge).await?;
    concord_db::merge_writes::approve(&state.pool, mr_id, user.id)
        .await
        .map_err(merge_refusal)?;
    let gate = concord_db::merge_writes::gate_for(&state.pool, mr_id)
        .await
        .map_err(merge_refusal)?;
    Ok(Json(serde_json::json!({ "status": "approved", "gate": gate })))
}

/// `PUT /projects/{id}/merge/{mr}/execute` — land the change.
///
/// **This is the destructive one.** It closes the request, marks the feature `shipped` and closes every complaint linked to it,
/// in one transaction. Three things stop it: the request must belong to this project (or the answer is 404, never 403 — a 403
/// confirms the id is real somewhere else), it must still be `open` (a second execute used to re-run the whole landing), and
/// §5.7's gate must pass.
///
/// The gate's shortfall is returned in the 409, because "gate not met" leaves a reader unable to tell whether they need another
/// confirmation or a reviewer.
async fn execute_merge(
    State(state): State<AppState>,
    Path((project_segment, merge)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let mr_id = merge_id(&state, &project, &merge).await?;
    concord_db::merge_writes::execute(&state.pool, mr_id, project.id)
        .await
        .map_err(merge_refusal)?;
    Ok(Json(serde_json::json!({ "status": "merged" })))
}

/// `PUT /projects/{id}/merge/{mr}/reject` — refuse it without landing anything.
async fn reject_merge(
    State(state): State<AppState>,
    Path((project_segment, merge)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let mr_id = merge_id(&state, &project, &merge).await?;
    concord_db::merge_writes::reject(&state.pool, mr_id, project.id)
        .await
        .map_err(merge_refusal)?;
    Ok(Json(serde_json::json!({ "status": "rejected" })))
}

/// The merge request named in the path, proven to belong to the project in the path.
async fn merge_id(
    state: &AppState,
    project: &concord_db::projects::Project,
    segment: &str,
) -> Result<i64, ApiError> {
    let Ok(id) = segment.parse::<i64>() else {
        return Err(ApiError::not_found("no such merge request"));
    };
    match concord_db::merge_writes::get(&state.pool, id).await.map_err(internal)? {
        Some(request) if request.project_id == project.id => Ok(id),
        _ => Err(ApiError::not_found("no such merge request")),
    }
}

/// A merge refusal, as the status each rule deserves.
fn merge_refusal(refused: concord_db::merge_writes::Refused) -> ApiError {
    use concord_db::merge_writes::Refused;
    match refused {
        Refused::NotFound => ApiError::not_found("no such merge request"),
        Refused::AlreadyClosed(status) => {
            ApiError::conflict(format!("this merge request is already {status}"))
        }
        Refused::GateUnmet(shortfall) => {
            ApiError::conflict(format!("§5.7's merge gate is not met: {shortfall}"))
        }
        Refused::SelfApproval => {
            ApiError::forbidden("the author of a merge request cannot approve it".to_string())
        }
        Refused::NotAMember => ApiError::forbidden(
            "approving a merge request is for members with at least the contributor role".to_string(),
        ),
        Refused::Database(error) => internal(error),
    }
}



/// The four merge routes, typed on `AppState`. The list path keeps both
/// GET and POST in one MethodRouter as the old chain declared it.
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{slug}/merge",
            axum::routing::get(list_merge_requests).post(create_merge_request),
        )
        .route("/api/v1/projects/{slug}/merge/{merge}/approve", axum::routing::put(approve_merge))
        .route("/api/v1/projects/{slug}/merge/{merge}/execute", axum::routing::put(execute_merge))
        .route("/api/v1/projects/{slug}/merge/{merge}/reject", axum::routing::put(reject_merge))
}
