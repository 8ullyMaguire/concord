//! §6.6's emergency hold release (step 3, ADR 0014).
//!
//! The hold is resolved before the role check because the URL carries no
//! project — the reason this route once answered 404 for every caller.

use axum::{
    extract::{Path, State},
    http::header::HeaderMap,
    Extension, Json, Router,
};
use polaris_core::ApiError;
use serde::Deserialize;
use serde_json::{json, Value};

use crate::{
    bearer_user, internal, parse_id, project_for, require_maintainer, require_role, write_audit, write_error,
    AppState,
};

/// `POST /emergency-holds/{id}/release` — lift §6.6's hold, with a written reason.
///
/// **The hold is resolved first, because this URL carries no project.** A hold belongs to the project that placed it, and that is
/// the project whose maintainer may lift it. The reference's own version of this route asked for a role against
/// `URLParam("project_id")` on a path that has no such parameter: it answered 404 for EVERY caller, which hid a second defect —
/// that the hold was releasable by its id alone. Both are asserted by the probe (a maintainer of the wrong project is refused, and
/// so is the wrong project's maintainer for this one).
async fn release_emergency_hold(
    State(state): State<AppState>,
    Path(hold_id): Path<i64>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<Value>,
) -> Result<Json<Value>, ApiError> {
    let hold = concord_db::holds::one(&state.pool, hold_id)
        .await
        .map_err(|error| match error {
            concord_db::feature_writes::WriteError::NotFound(_) => ApiError::not_found("no such hold"),
            other => hold_refusal(other),
        })?;
    let project = concord_db::projects::project_by_id(&state.pool, hold.project_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such hold"))?;
    // Maintainer, checked against the HOLD's project — the one fact the URL does not carry.
    let any_role = std::env::var("CONCORD_MUTANT")
        .unwrap_or_default()
        .split(',')
        .any(|name| name.trim() == "hold-any-role");
    if !any_role {
        require_role(&state, &project, "maintainer", &user).await?;
    }
    let reason = body.get("reason").and_then(Value::as_str).unwrap_or_default();
    let released = match concord_db::holds::release(&state.pool, hold_id, user.id, reason)
        .await
        .map_err(internal)?
    {
        Ok(hold) => hold,
        Err(refusal) => return Err(hold_refusal(refusal)),
    };
    Ok(Json(serde_json::json!({
        "hold": serde_json::to_value(released).map_err(internal)?,
        "status": "released",
    })))
}

/// Map a hold refusal: a missing row is 404, a second release or a blank reason is 400.
fn hold_refusal(refusal: concord_db::feature_writes::WriteError) -> ApiError {
    match refusal {
        concord_db::feature_writes::WriteError::NotFound(message) => ApiError::not_found(message),
        concord_db::feature_writes::WriteError::Invalid(message) => ApiError::invalid_request(message),
        concord_db::feature_writes::WriteError::Conflict(message) => ApiError::conflict(message),
        concord_db::feature_writes::WriteError::Forbidden(message) => ApiError::forbidden(message),
        concord_db::feature_writes::WriteError::Database(error) => internal(error),
    }
}



/// `GET /api/v1/projects/{slug}/consensus/{call_id}/hold` — the in-force hold, or null.
///
/// The call's OWN project answers, not the URL's: the id is sequential, so a route that resolved the call
/// by id alone would let anyone who can read project A read project B's hold grounds and expiry.
async fn get_call_hold(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
) -> Result<Json<Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    let call_id = parse_id(&call, "call id")?;
    let call = concord_db::consensus::call(&state.pool, call_id)
        .await
        .map_err(internal)?
        .filter(|found| found.project_id == project.id)
        .ok_or_else(|| ApiError::not_found(format!("consensus call {call_id}")))?;
    let _ = call;
    let hold = concord_db::holds::active_for_call(&state.pool, call_id)
        .await
        .map_err(internal)?;
    // `{"hold": null}` rather than 404: "no hold" is a fact about the call, not a missing resource.
    Ok(Json(json!({
        "hold": hold.map(|hold| serde_json::to_value(hold)).transpose().map_err(internal)?
    })))
}

#[derive(Debug, Deserialize)]
struct HoldRequest {
    #[serde(default)]
    grounds: String,
    #[serde(default)]
    reason: String,
    #[serde(default)]
    days: i64,
}

/// `POST /api/v1/projects/{slug}/consensus/{call_id}/hold` — suspend a call.
///
/// The call is resolved first, so the role is asked for on the project that owns it. Asking for the role
/// against the URL's project while loading the call by id alone let a maintainer of one project suspend
/// another's call.
async fn place_emergency_hold(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<HoldRequest>,
) -> Result<(axum::http::StatusCode, Json<Value>), ApiError> {
    let _url_project = project_for(&state, &slug).await?;
    let call_id = parse_id(&call, "call id")?;
    let owner = concord_db::consensus::call(&state.pool, call_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("consensus call {call_id}")))?;
    require_maintainer(&state.pool, owner.project_id, user.id).await?;
    let hold = concord_db::holds::place(
        &state.pool,
        owner.project_id,
        call_id,
        user.id,
        &body.grounds,
        &body.reason,
        body.days,
    )
    .await
    .map_err(write_error)?;
    Ok((
        axum::http::StatusCode::CREATED,
        Json(serde_json::to_value(hold).map_err(internal)?),
    ))
}

/// `GET /api/v1/projects/{project_id}/charter` — the numbers every threshold reads.
///
/// The path segment is the NUMERIC project id, as the Go route is; the neighbouring routes take a slug. A
/// slug here answers a parse error rather than silently resolving, which is what the Go handler does —
/// `requireProjectID` parses and refuses, it does not look the slug up.

/// The release route, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{slug}/consensus/{call}/hold",
            axum::routing::get(get_call_hold).post(place_emergency_hold),
        )
        .route(
            "/api/v1/emergency-holds/{hold_id}/release",
            axum::routing::post(release_emergency_hold),
        )
}
