//! Solution calls: readiness, opening and arena fallback
//! (step 3, ADR 0014). Shared feature resolver stays at the root.

use axum::{
    extract::{Path, Query, State},
    http::{header::HeaderMap, StatusCode},
    Extension, Json, Router,
};
use polaris_core::ApiError;
use serde::Deserialize;
use serde_json::Value;

use crate::{
    bearer_user, feature_for, internal, parse_id, project_for, require_role, write_audit, write_error,
    AppState,
};

/// `GET /api/v1/projects/{slug}/features/{feature}/solution-call` — §6.5's three conditions.
///
/// A feature nobody has proposed a solution to is one unmet condition, not a 404: "no solutions yet" and
/// "no such feature" must not look the same to a caller.
async fn solution_call_readiness(
    State(state): State<AppState>,
    Path((slug, feature)): Path<(String, String)>,
) -> Result<Json<Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    let feature = feature_for(&state, project.id, &feature).await?;
    let readiness = concord_db::arena::readiness(&state.pool, feature.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(readiness).map_err(internal)?))
}

#[derive(Debug, Deserialize, Default)]
struct SolutionCallRequest {
    #[serde(default)]
    solution_id: i64,
    /// §6.5's discretionary marker, as the reference sends it.
    #[serde(default)]
    early: bool,
    /// The stated reason, as the reference sends it. Required when `early` is true and refused when it is blank — an
    /// early call with no reason is an override that looks like a decision.
    #[serde(default)]
    reason: String,
    /// The port's own older spelling of the same two fields, kept because a client that has been calling
    /// `/solution-call` since before this alias existed should not have to change its body to keep working.
    #[serde(default)]
    early_reason: String,
}

/// `POST /api/v1/projects/{slug}/features/{feature}/solution-call` — open §6.5's call on the leader.
///
/// The call opens on the LEADING solution, as the Go store does, and refuses unless the three conditions
/// hold — naming the first one that failed. Opening it and letting it fail later spends a consensus window
/// on a decision the arena already says is premature.
///
/// §6.5's discretionary path: a collaborator may open a call early with a stated reason. Early calls are
/// flagged, not hidden: the record has to say the conditions were not met, or the decision reads as one the
/// arena endorsed when it did not.
async fn open_solution_call(
    State(state): State<AppState>,
    Path((slug, feature)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    body: Option<Json<SolutionCallRequest>>,
) -> Result<(StatusCode, Json<Value>), ApiError> {
    let project = project_for(&state, &slug).await?;
    let feature = feature_for(&state, project.id, &feature).await?;
    let body = body.map(|Json(body)| body).unwrap_or_default();

    // §6.5's conditions are checked by the STORE, next to the write, in the same function the readiness page reads. They
    // used to be re-derived here, which is two answers to "may this be called?" — and the second one is the one that goes
    // stale the first time a threshold is tuned.
    // `early: true` is the reference's marker and takes its own `reason`, which the store refuses when blank. The port's
    // older `{early_reason}` means the same thing, so a client written against `/solution-call` keeps working.
    let (early, reason) = if body.early {
        (true, body.reason.trim().to_string())
    } else if !body.early_reason.trim().is_empty() {
        (true, body.early_reason.trim().to_string())
    } else {
        (false, String::new())
    };
    // A caller naming a specific solution is asking for a call the arena may not have led with; that is a request-shape
    // complaint about an id the caller supplied, so it belongs here rather than among the conditions.
    if body.solution_id != 0 {
        let readiness = concord_db::arena::readiness(&state.pool, feature.id)
            .await
            .map_err(internal)?;
        if readiness.leader_id == 0 {
            return Err(ApiError::invalid_request(
                "no solution leads this feature's arena, so there is nothing to call".to_string(),
            ));
        }
        if body.solution_id != readiness.leader_id {
            return Err(ApiError::invalid_request(format!(
                "solution {} is not the leader ({} leads this arena)",
                body.solution_id, readiness.leader_id
            )));
        }
    }
    let opened = if !early {
        concord_db::consensus_writes::open_solution_call_on_merit(&state.pool, project.id, feature.id, user.id)
            .await
            .map_err(write_error)?
    } else {
        // §6.5's discretionary path is a COLLABORATOR's discretion: a guest cannot open a call early. Checked here because
        // the merit path's own conditions are the gate for opening at all, and a role is not one of them.
        require_role(&state, &project, "contributor", &user).await?;
        concord_db::consensus_writes::open_solution_call_early(
            &state.pool,
            project.id,
            feature.id,
            user.id,
            &reason,
        )
        .await
        .map_err(write_error)?
    };
    Ok((
        StatusCode::CREATED,
        Json(serde_json::to_value(opened).map_err(internal)?),
    ))
}

/// `GET /api/v1/projects/{slug}/consensus/{call}/fallback` — §6.5's chain, the call's own solution absent.
async fn call_fallback(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
) -> Result<Json<Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    let call_id = parse_id(&call, "call id")?;
    let found = concord_db::consensus::call(&state.pool, call_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("consensus call {call_id}")))?;
    if found.project_id != project.id {
        return Err(ApiError::not_found(format!("consensus call {call_id}")));
    }
    let chain = concord_db::arena::fallback(&state.pool, call_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({
        "call_id": call_id,
        "fallback": chain,
    })))
}

/// Solution-call routes, typed on `AppState` (D-18). The Go and
/// port spellings of the call path share one MethodRouter (D-20).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{slug}/features/{feature}/consensus",
            axum::routing::get(solution_call_readiness).post(open_solution_call),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/solution-call",
            axum::routing::get(solution_call_readiness).post(open_solution_call),
        )
        .route(
            "/api/v1/projects/{slug}/consensus/{call}/fallback",
            axum::routing::get(call_fallback),
        )
}
