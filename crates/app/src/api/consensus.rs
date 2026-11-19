//! Project consensus calls: reads and the four call writes
//! (step 3, ADR 0014). Shared project helpers stay at the root.

use axum::{
    extract::{Path, Query, State},
    http::header::HeaderMap,
    Extension, Json, Router,
};
use polaris_core::ApiError;
use crate::pagination::PageQuery;
use serde::Deserialize;
use serde_json::{json, Value};

use crate::{
    bearer_user, internal, parse_id, project_for, readable_project_segment, require_maintainer,
    require_role, write_error, AppState,
};

/// `GET /api/v1/projects/{slug}/consensus` — the calls index, newest first.
///
/// The project page's Consensus tab links here with no call named; without this route the page fetched
/// an empty call id and answered 405 to every reader.
async fn list_consensus(
    State(state): State<AppState>,
    Query(page_query): Query<PageQuery>,
    Path(slug): Path<String>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let page = page_query.valid().map_err(ApiError::invalid_request)?;
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let calls = concord_db::consensus::list_calls(&state.pool, project.id, 0)
        .await
        .map_err(internal)?;
    Ok(crate::pagination::page_json(calls, &page)?)
}

/// `GET /api/v1/projects/{slug}/consensus/{call_id}` — one call, with §6.6's hidden tally.
///
/// While the call is open the counts are blanked and only the caller's own position is returned: §6.6
/// hides the tally to prevent bandwagoning, and the rule is enforced here rather than in a client,
/// because a client-side rule is bypassed by devtools.
async fn get_consensus(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let Ok(call_id) = call.parse::<i64>() else {
        return Err(ApiError::invalid_request("call id must be an integer"));
    };
    // Resolved against THIS project: a call id alone would let one project's URL read another's call.
    let found = concord_db::consensus::call(&state.pool, call_id)
        .await
        .map_err(internal)?
        .filter(|row| row.project_id == project.id)
        .ok_or_else(|| ApiError::not_found(format!("consensus call {call_id}")))?;
    let actor = bearer_user(&state, &headers).await?.map(|user| user.id).unwrap_or(0);
    let positions = concord_db::consensus::positions(&state.pool, call_id)
        .await
        .map_err(internal)?;
    let objections = concord_db::consensus::objections(&state.pool, call_id)
        .await
        .map_err(internal)?;
    let counts = concord_db::consensus::tally(&state.pool, project.id, call_id)
        .await
        .map_err(internal)?;
    let model = concord_db::projects::governance_model(&state.pool, project.id)
        .await
        .map_err(internal)?;
    let charter = match concord_governance::GovernanceModel::parse(&model) {
        Some(model) => concord_governance::Charter::default_for(model),
        // An unknown model falls back to collective, exactly as the Go handler does when the stored
        // value is not valid — a project with a typo in its model is not a project with no rules.
        None => concord_governance::Charter::default_for(concord_governance::GovernanceModel::Collective),
    };
    let (tally, visible, shown_positions) = if found.status == "closed" {
        (
            concord_db::consensus::thresholds_summary(&counts, &charter),
            true,
            positions,
        )
    } else {
        (
            concord_db::consensus::hidden_tally(concord_db::consensus::thresholds_summary(
                &counts, &charter,
            )),
            false,
            concord_db::consensus::own_positions_only(positions, actor),
        )
    };
    Ok(Json(json!({
        "call": serde_json::to_value(found).map_err(internal)?,
        "objections": serde_json::to_value(objections).map_err(internal)?,
        "tally": tally,
        "tally_visible": visible,
        "positions": serde_json::to_value(shown_positions).map_err(internal)?,
    })))
}

#[derive(Debug, Deserialize)]
struct OpenConsensusRequest {
    #[serde(default)]
    feature_id: i64,
    #[serde(default)]
    title: String,
    #[serde(default)]
    description: String,
}

/// `POST /api/v1/projects/{slug}/consensus` — open a call.
///
/// `opened_by` is the authenticated caller. It used to be hardcoded to 1, so every call in the database
/// claimed user 1 opened it.
async fn open_consensus(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<OpenConsensusRequest>,
) -> Result<(axum::http::StatusCode, Json<Value>), ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    require_maintainer(&state.pool, project.id, user.id).await?;
    let call = concord_db::consensus_writes::create_call(
        &state.pool,
        project.id,
        body.feature_id,
        user.id,
        &body.title,
        &body.description,
    )
    .await
    .map_err(write_error)?;
    Ok((
        axum::http::StatusCode::CREATED,
        Json(serde_json::to_value(call).map_err(internal)?),
    ))
}

#[derive(Debug, Deserialize)]
struct PositionRequest {
    position: String,
    #[serde(default)]
    #[allow(dead_code)]
    reason: String,
}

/// `POST /api/v1/projects/{slug}/consensus/{call_id}/position` — cast or replace a stance.
async fn cast_consensus_position(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<PositionRequest>,
) -> Result<Json<Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    let call_id = parse_id(&call, "call id")?;
    concord_db::consensus_writes::cast_position_with_event(
        &state.pool,
        project.id,
        call_id,
        user.id,
        &body.position,
    )
    .await
    .map_err(write_error)?;
    let _ = concord_db::feature_writes::add_audit(
        &state.pool,
        project.id,
        user.id,
        "cast_position",
        "consensus_call",
        call_id,
        &body.position,
    )
    .await;
    Ok(Json(json!({"status": "ok", "position": body.position})))
}

#[derive(Debug, Deserialize)]
struct ObjectionRequest {
    principle: String,
    violation: String,
    #[serde(default)]
    remedy: String,
}

/// `POST /api/v1/projects/{slug}/consensus/{call_id}/objection` — §6.6's block, in writing.
async fn create_consensus_objection(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<ObjectionRequest>,
) -> Result<(axum::http::StatusCode, Json<Value>), ApiError> {
    let project = project_for(&state, &slug).await?;
    let call_id = parse_id(&call, "call id")?;
    let id = concord_db::consensus_writes::create_objection(
        &state.pool,
        project.id,
        call_id,
        user.id,
        &body.principle,
        &body.violation,
        &body.remedy,
    )
    .await
    .map_err(write_error)?;
    Ok((
        axum::http::StatusCode::CREATED,
        Json(json!({"id": id, "status": "open"})),
    ))
}

/// `POST /api/v1/projects/{slug}/consensus/{call_id}/close` — fix the verdict.
///
/// No body: since the eighth pass a close never overrides (the override is the second window, with its
/// own route), so there is no note for it to carry.
async fn close_consensus(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<Json<Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    require_maintainer(&state.pool, project.id, user.id).await?;
    let call_id = parse_id(&call, "call id")?;
    let summary = concord_db::consensus_writes::close_call(&state.pool, project.id, call_id)
        .await
        .map_err(write_error)?;
    let verdict = summary
        .get("result")
        .and_then(Value::as_str)
        .unwrap_or("")
        .to_string();
    let _ = concord_db::feature_writes::add_audit(
        &state.pool,
        project.id,
        user.id,
        "close_consensus",
        "consensus_call",
        call_id,
        &verdict,
    )
    .await;
    Ok(Json(summary))
}


#[derive(Debug, Deserialize)]
struct ResolveObjectionRequest {
    status: String,
    resolution: String,
}

/// `PUT /api/v1/projects/{slug}/consensus/objections/{id}/resolve` — the objection's written answer.
async fn resolve_consensus_objection(
    State(state): State<AppState>,
    Path((slug, objection)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<ResolveObjectionRequest>,
) -> Result<Json<Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    require_maintainer(&state.pool, project.id, user.id).await?;
    let objection_id = parse_id(&objection, "objection id")?;
    concord_db::consensus_writes::resolve_objection(
        &state.pool,
        objection_id,
        &body.status,
        &body.resolution,
    )
    .await
    .map_err(write_error)?;
    Ok(Json(json!({"id": objection_id, "status": body.status})))
}

/// Consensus routes, typed on `AppState` (D-18). GET and POST on the
/// same path stay in one module (D-20).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{slug}/consensus",
            axum::routing::get(list_consensus).post(open_consensus),
        )
        .route(
            "/api/v1/projects/{slug}/consensus/{call}",
            axum::routing::get(get_consensus),
        )
        .route(
            "/api/v1/projects/{slug}/consensus/{call}/position",
            axum::routing::post(cast_consensus_position),
        )
        .route(
            "/api/v1/projects/{slug}/consensus/{call}/objection",
            axum::routing::post(create_consensus_objection),
        )
        .route(
            "/api/v1/projects/{slug}/consensus/{call}/close",
            axum::routing::post(close_consensus),
        )
        .route(
            "/api/v1/projects/{slug}/consensus/objections/{objection}/resolve",
            axum::routing::put(resolve_consensus_objection),
        )
}
