//! Feature tags, milestone assignment and strategic weight (step 3, ADR 0014).
//! The tag reader joined the module; its GET route stayed with the writes (D-48).

use axum::{
    extract::{Path, State},
    http::{header::HeaderMap, StatusCode},
    Extension, Json, Router,
};
use polaris_core::ApiError;

use crate::{
    bearer_user, caller_feature, feature_for, internal, project_for, readable, readable_project_segment,
    require_role, strategy_refusal, write_audit, AppState,
};

/// `GET /api/v1/projects/{slug}/features/{feature}/tags` — the feature's tags, alphabetical.
async fn feature_tags(
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
    let tags = concord_db::panels::feature_tags(&state.pool, feature.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({ "feature_id": feature.id, "tags": tags })))
}

// ── feature tags, feature milestone, strategic weight (§4.5, §6.2, §17) ────────────────────────────────────────────

/// `POST /projects/{id}/features/{feature}/tags` — attach a tag that already exists.
///
/// **Contributor**, and the response is the tags as stored: the tags decide §6.2's theme weight, so whoever may add one
/// must be able to see the result without a second request. An unknown name is a 400 naming the proposing route — the
/// feature in the URL is right there and the name in the body is what is wrong.
///
/// The store spells the tag **once**; see its comment for the mutation-found bug where a second lowering left an
/// `INSERT … SELECT` matching nothing and returning success.
async fn add_feature_tag(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let tag = body.get("tag").and_then(|v| v.as_str()).unwrap_or_default();
    match concord_db::taxonomy::add_feature_tag(&state.pool, project.id, feature_id, user.id, tag)
        .await
        .map_err(internal)?
    {
        Ok(()) => {}
        Err(concord_db::taxonomy::Applied::UnknownName { name }) => {
            return Err(ApiError::invalid_request(format!(
                "no tag named {name:?}; propose it with POST /api/v1/taxonomy/proposals (action=create)"
            )));
        }
        Err(concord_db::taxonomy::Applied::UnknownFeature) => {
            return Err(ApiError::not_found("no such feature in this project"));
        }
    }
    let stored = concord_db::panels::feature_tags(&state.pool, feature_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({ "feature_id": feature_id, "tags": stored })))
}

/// `POST /projects/{id}/features/{feature}/tags/remove` — detach one.
///
/// Not housekeeping: a wrong tag silently steers the priority, so it must be removable by whoever can add one. Removing
/// a tag that is not attached is a no-op — the caller's intent is satisfied either way.
async fn remove_feature_tag(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let tag = body.get("tag").and_then(|v| v.as_str()).unwrap_or_default();
    match concord_db::taxonomy::remove_feature_tag(&state.pool, project.id, feature_id, tag)
        .await
        .map_err(internal)?
    {
        Ok(()) => {}
        Err(concord_db::taxonomy::Applied::UnknownName { name }) => {
            return Err(ApiError::invalid_request(format!("no tag named {name:?}")));
        }
        Err(concord_db::taxonomy::Applied::UnknownFeature) => {
            return Err(ApiError::not_found("no such feature in this project"));
        }
    }
    let stored = concord_db::panels::feature_tags(&state.pool, feature_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({ "feature_id": feature_id, "tags": stored })))
}

/// `PUT /projects/{id}/features/{feature}/milestone` — place one feature in the roadmap, or take it out.
///
/// `milestone_id: null` clears it. The response reports **`open_blockers`** rather than a boolean: a feature may be
/// planned before what it depends on has shipped, and refusing that would make a roadmap unable to express "this lands
/// after X". The count is what tells a reader how much work stands in front of it.
async fn set_feature_milestone(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let milestone_id = body.get("milestone_id").and_then(serde_json::Value::as_i64);
    let position = body
        .get("position")
        .and_then(serde_json::Value::as_i64)
        .unwrap_or_default();
    let open = concord_db::milestones::set_feature_milestone(
        &state.pool, project.id, feature_id, milestone_id, position,
    )
    .await
    .map_err(internal)?
    .map_err(|_| ApiError::not_found("no such milestone in this project"))?;
    let detail = match milestone_id {
        Some(id) => format!("milestone {id}"),
        None => "milestone cleared".to_string(),
    };
    write_audit(&state, Some(project.id), user.id, "feature.milestone", "feature", feature_id, &detail).await?;
    Ok(Json(serde_json::json!({ "feature_id": feature_id, "open_blockers": open })))
}

/// `PUT /projects/{id}/features/{feature}/strategic-weight` — **propose** a weight change; it does not apply one.
///
/// §6.2's `μ` is a steering lever, so moving it is a consensus decision: this writes a pending proposal and the route's
/// message says so, naming the proposal to consent to. A rationale is required and refused at the handler with a message
/// that names the field, because "invalid" is no help to a caller who left it out.
///
/// **Why this exists when `/strategy/weight-proposals` already did.** The store was wired, but under the port's own path
/// (`project_id` in the path, `feature_id` in the body) while the reference — and therefore the pages built against it —
/// uses `PUT /features/{id}/strategic-weight` with the weight in the body. Two shapes for one act: the port's is a
/// reasonable API and the reference's is the one a caller already sends, so both are served from the same store rather
/// than one of them 404ing. The route inventory is a gate precisely because "wired somewhere" is not "wired where the
/// caller looks".
async fn propose_strategic_weight(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let weight = body
        .get("weight")
        .and_then(serde_json::Value::as_f64)
        .unwrap_or_default();
    let rationale = body.get("rationale").and_then(|v| v.as_str()).unwrap_or_default();
    if rationale.trim().is_empty() {
        return Err(ApiError::invalid_request(
            "a rationale is required: a weight change is a steering decision, so it must say why".to_string(),
        ));
    }
    let proposal = concord_db::strategy::propose(&state.pool, project.id, feature_id, user.id, weight, rationale)
        .await
        .map_err(|refused| strategy_refusal(refused))?;
    Ok((
        StatusCode::CREATED,
        Json(serde_json::json!({
            "proposal": proposal,
            "message": format!("strategic weight changes are ratified by consensus; consent to proposal {}", proposal.id),
        })),
    ))
}

/// Feature metadata routes, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{slug}/features/{feature}/tags",
            axum::routing::get(feature_tags).post(add_feature_tag),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/tags/remove",
            axum::routing::post(remove_feature_tag),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/milestone",
            axum::routing::put(set_feature_milestone),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/strategic-weight",
            axum::routing::put(propose_strategic_weight),
        )
}
