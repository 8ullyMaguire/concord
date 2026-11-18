//! Feature work items, dependencies and evidence (step 3, ADR 0014).
//! Reader handlers joined the module; their routes stayed with the writes (D-48).

use axum::{
    extract::{Path, Query, State},
    http::{header::HeaderMap, StatusCode},
    Extension, Json, Router,
};
use polaris_core::ApiError;

use crate::{
    bearer_user, caller_feature, feature_for, internal, project_for, readable, readable_project_segment,
    require_role, write_audit, AppState,
};

/// `GET /api/v1/projects/{slug}/features/{feature}/deps` — blocked_by, blocks, and what is still open.
async fn feature_deps(
    State(state): State<AppState>,
    Path((slug, feature)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    let user = bearer_user(&state, &headers).await?;
    if !readable(&state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    let feature = feature_for(&state, project.id, &feature).await?;
    let panel = concord_db::deps::panel(&state.pool, feature.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(panel)))
}

/// `GET /api/v1/projects/{slug}/features/{feature}/evidence` — what says this feature is worth building.
async fn feature_evidence(
    State(state): State<AppState>,
    Path((slug, feature)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    let user = bearer_user(&state, &headers).await?;
    if !readable(&state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    let feature = feature_for(&state, project.id, &feature).await?;
    let rows = concord_db::panels::evidence(&state.pool, feature.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

// ── work items, dependencies, evidence (§4.5.7, R18, §3.4) ─────────────────────────────────────────────────────────

fn feature_work_refusal(refused: concord_db::feature_work::Refused) -> ApiError {
    use concord_db::feature_work::Refused;
    match refused {
        Refused::Invalid(message) => ApiError::invalid_request(message),
        Refused::NotFound => ApiError::not_found("no such feature in this project"),
        Refused::Held(message) => ApiError::conflict(message),
    }
}

/// `PUT /projects/{id}/features/{feature}/work-item` — what the task costs a newcomer.
///
/// **Maintainer only.** Labelling something "good first" is a promise about review capacity, and only somebody who will
/// review it can make that promise. `difficulty` of `""` clears the label rather than storing an empty chip; a budget of
/// zero or less is refused, because a task someone says finishes in no time misleads whoever picks it up.
async fn set_work_item_label(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let difficulty = body.get("difficulty").and_then(|v| v.as_str());
    let budget = body
        .get("time_budget_minutes")
        .and_then(serde_json::Value::as_i64);
    let needs_mentor = body
        .get("needs_mentor")
        .and_then(serde_json::Value::as_bool)
        .unwrap_or(false);
    concord_db::feature_work::set_work_item_label(
        &state.pool,
        project.id,
        feature_id,
        difficulty,
        budget,
        needs_mentor,
    )
    .await
    .map_err(internal)?
    .map_err(feature_work_refusal)?;
    let item = concord_db::feature_work::work_item(&state.pool, project.id, feature_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such feature in this project"))?;
    Ok(Json(serde_json::to_value(item).map_err(internal)?))
}

/// `POST /projects/{id}/features/{feature}/claim` — take the single live claim.
///
/// A claim **does not move the feature**: the board is the only mechanism that moves work, and a claim that changed status
/// would make two surfaces disagree about what is being built. Only `ready`/`in_progress` work is claimable, because a
/// draft is work nobody has agreed to do.
async fn claim_work_item(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    concord_db::feature_work::claim(&state.pool, project.id, feature_id, user.id)
        .await
        .map_err(internal)?
        .map_err(feature_work_refusal)?;
    let item = concord_db::feature_work::work_item(&state.pool, project.id, feature_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such feature in this project"))?;
    Ok((StatusCode::CREATED, Json(serde_json::to_value(item).map_err(internal)?)))
}

/// `DELETE /projects/{id}/features/{feature}/claim` — withdraw a claim.
///
/// The row stays with `released_at` set: a withdrawal is a state, not a deletion, so "picked up and dropped twice" stays
/// answerable. Releasing a claim you do not hold is a 404 rather than a silent success, because a page that says "you
/// released it" about somebody else's claim is telling the wrong story.
async fn release_claim(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
) -> Result<StatusCode, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    concord_db::feature_work::release_claim(&state.pool, project.id, feature_id, user.id)
        .await
        .map_err(internal)?
        .map_err(|_| ApiError::not_found("no claim of yours on this feature"))?;
    Ok(StatusCode::NO_CONTENT)
}

/// `POST /projects/{id}/features/{feature}/deps` — record that `blocker_id` must ship first.
///
/// A cycle is refused **with the path that would close it**: a graph with a cycle can never be started, and the reader who
/// built it needs to see what they closed rather than the word "invalid".
async fn add_feature_dep(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let blocked = caller_feature(&state, &project, &feature).await?;
    let blocker = body
        .get("blocker_id")
        .and_then(serde_json::Value::as_i64)
        .unwrap_or_default();
    concord_db::feature_work::add_dependency(&state.pool, project.id, blocker, blocked)
        .await
        .map_err(internal)?
        .map_err(feature_work_refusal)?;
    write_audit(
        &state,
        Some(project.id),
        user.id,
        "feature.dep.add",
        "feature",
        blocked,
        &format!("blocked by #{blocker}"),
    )
    .await?;
    Ok(Json(serde_json::json!({ "feature_id": blocked, "blocker_id": blocker })))
}

/// `DELETE /projects/{id}/features/{feature}/deps?blocker_id=` — remove an edge.
///
/// Removing one that does not exist is a **no-op**: the caller's intent ("this must not be a dependency") is satisfied
/// either way, and a page that reloads into an empty state should not have to distinguish the two.
async fn remove_feature_dep(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    Query(query): Query<std::collections::HashMap<String, String>>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
) -> Result<StatusCode, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let blocked = caller_feature(&state, &project, &feature).await?;
    let blocker: i64 = query
        .get("blocker_id")
        .and_then(|value| value.parse().ok())
        .unwrap_or_default();
    concord_db::feature_work::remove_dependency(&state.pool, project.id, blocker, blocked)
        .await
        .map_err(internal)?
        .map_err(feature_work_refusal)?;
    write_audit(
        &state,
        Some(project.id),
        user.id,
        "feature.dep.remove",
        "feature",
        blocked,
        &format!("unblocked from #{blocker}"),
    )
    .await?;
    Ok(StatusCode::NO_CONTENT)
}

/// `POST /projects/{id}/features/{feature}/evidence` — cite a document or a field report.
///
/// Scoped **twice**: the feature, and the cited row. A reference id is global, so without the second check a feature can
/// cite another project's document and the page will render its title as if it were evidence for this idea.
async fn add_feature_evidence(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let kind = body.get("kind").and_then(|v| v.as_str()).unwrap_or_default();
    let ref_id = body
        .get("ref_id")
        .and_then(serde_json::Value::as_i64)
        .unwrap_or_default();
    concord_db::feature_work::add_evidence(&state.pool, project.id, feature_id, kind, ref_id)
        .await
        .map_err(internal)?
        .map_err(feature_work_refusal)?;
    write_audit(
        &state,
        Some(project.id),
        user.id,
        "feature.evidence",
        "feature",
        feature_id,
        &format!("{kind} #{ref_id}"),
    )
    .await?;
    Ok((
        StatusCode::CREATED,
        Json(serde_json::json!({ "kind": kind, "ref_id": ref_id })),
    ))
}

/// `DELETE /projects/{id}/features/{feature}/evidence?kind=&ref_id=` — remove one citation.
///
/// A feature left with no evidence stays where it is: the rule is enforced on the next transition to `ready`, not
/// retroactively, because silently un-readying work somebody is doing is worse than letting the next reader see it.
async fn remove_feature_evidence(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    Query(query): Query<std::collections::HashMap<String, String>>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
) -> Result<StatusCode, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let kind = query.get("kind").cloned().unwrap_or_default();
    let ref_id: i64 = query
        .get("ref_id")
        .and_then(|value| value.parse().ok())
        .unwrap_or_default();
    concord_db::feature_work::remove_evidence(&state.pool, project.id, feature_id, &kind, ref_id)
        .await
        .map_err(internal)?
        .map_err(feature_work_refusal)?;
    Ok(StatusCode::NO_CONTENT)
}

/// Feature work routes, typed on `AppState` (D-18). GET+POST+DELETE
/// pairs stay in one MethodRouter (D-20).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{slug}/features/{feature}/deps",
            axum::routing::get(feature_deps)
                .post(add_feature_dep)
                .delete(remove_feature_dep),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/evidence",
            axum::routing::get(feature_evidence)
                .post(add_feature_evidence)
                .delete(remove_feature_evidence),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/work-item",
            axum::routing::put(set_work_item_label),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/claim",
            axum::routing::post(claim_work_item).delete(release_claim),
        )
}
