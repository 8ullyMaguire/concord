//! Project strategy: weight and theme proposals, consents and
//! withdrawals (step 3, ADR 0014). Shared refusal mappers stay at
//! the crate root (D-51).

use axum::{
    extract::{Path, State},
    http::{header::HeaderMap, StatusCode},
    Extension, Json, Router,
};
use polaris_core::ApiError;
use serde_json::Value;

use crate::{
    bearer_user, internal, parse_id, project_by_id, strategy_refusal, taxonomy_refusal, AppState,
};

/// The caller and their role in the project named by the path. `project_id` is a NUMBER here because that is
/// what the Go routes take: the frontend passes `data-project-id`.
struct StrategyCaller {
    /// The authenticated user.
    user: concord_db::auth::User,
    /// Their role in this project, `guest` when they are not a member.
    role: String,
}

async fn strategy_project(
    state: &AppState,
    project_id: i64,
    user: &concord_db::auth::User,
) -> Result<StrategyCaller, ApiError> {
    // The identity comes from auth_gate; this helper keeps only project existence and the member role lookup.
    let exists: Option<i64> = sqlx::query_scalar("select id from projects where id = $1")
        .bind(project_id)
        .fetch_optional(&state.pool)
        .await
        .map_err(internal)?;
    if exists.is_none() {
        return Err(ApiError::not_found("no such project"));
    }
    let role: String = sqlx::query_scalar(
        "select coalesce((select role from members where project_id = $1 and user_id = $2), 'guest')",
    )
    .bind(project_id)
    .bind(user.id)
    .fetch_one(&state.pool)
    .await
    .map_err(internal)?;
    Ok(StrategyCaller {
        user: user.clone(),
        role,
    })
}

async fn list_weight_proposals(
    State(state): State<AppState>,
    Path(project): Path<String>,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The segment may be an id or a slug: the reference's page scripts build these URLs from the SLUG, and a path extractor typed
    // `i64` rejected the slug before any handler ran (`Invalid URL: Cannot parse `suki` to a `i64``). One resolver, asked once, at
    // the top of the handler — the `project_id` the body uses is its id.
    let project_id = project_by_id(&state, &project).await?.id;
    let rows = concord_db::strategy::proposals(&state.pool, project_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

async fn list_theme_proposals(
    State(state): State<AppState>,
    Path(project): Path<String>,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The segment may be an id or a slug: the reference's page scripts build these URLs from the SLUG, and a path extractor typed
    // `i64` rejected the slug before any handler ran (`Invalid URL: Cannot parse `suki` to a `i64``). One resolver, asked once, at
    // the top of the handler — the `project_id` the body uses is its id.
    let project_id = project_by_id(&state, &project).await?.id;
    let rows = concord_db::themes::proposals(&state.pool, project_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

async fn list_themes(
    State(state): State<AppState>,
    Path(project): Path<String>,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The segment may be an id or a slug: the reference's page scripts build these URLs from the SLUG, and a path extractor typed
    // `i64` rejected the slug before any handler ran (`Invalid URL: Cannot parse `suki` to a `i64``). One resolver, asked once, at
    // the top of the handler — the `project_id` the body uses is its id.
    let project_id = project_by_id(&state, &project).await?.id;
    let rows = concord_db::themes::themes(&state.pool, project_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

async fn propose_weight(
    State(state): State<AppState>,
    Path(project): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<serde_json::Value>,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
    let project_id = project_by_id(&state, &project).await?.id;
    let who = strategy_project(&state, project_id, &user).await?;
    if !matches!(who.role.as_str(), "contributor" | "reviewer" | "maintainer" | "owner") {
        return Err(ApiError::forbidden(
            "proposing a strategic weight needs a contributor role in this project",
        ));
    }
    let feature_id = body
        .get("feature_id")
        .and_then(serde_json::Value::as_i64)
        .ok_or_else(|| ApiError::invalid_request("feature_id is required"))?;
    let weight = body
        .get("weight")
        .and_then(serde_json::Value::as_f64)
        .ok_or_else(|| ApiError::invalid_request("weight is required"))?;
    let rationale = body.get("rationale").and_then(serde_json::Value::as_str).unwrap_or("");
    match concord_db::strategy::propose(&state.pool, project_id, feature_id, who.user.id, weight, rationale).await
    {
        Ok(proposal) => Ok((StatusCode::CREATED, Json(serde_json::json!(proposal)))),
        Err(refusal) => Err(strategy_refusal(refusal)),
    }
}

async fn propose_theme(
    State(state): State<AppState>,
    Path(project): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<serde_json::Value>,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
    let project_id = project_by_id(&state, &project).await?.id;
    let who = strategy_project(&state, project_id, &user).await?;
    let tag = body.get("theme_tag").and_then(serde_json::Value::as_str).unwrap_or("");
    let namespace = body
        .get("theme_namespace")
        .and_then(serde_json::Value::as_str)
        .unwrap_or("");
    let weight = body
        .get("weight")
        .and_then(serde_json::Value::as_f64)
        .ok_or_else(|| ApiError::invalid_request("weight is required"))?;
    let rationale = body.get("rationale").and_then(serde_json::Value::as_str).unwrap_or("");
    match concord_db::themes::propose(
        &state.pool,
        project_id,
        who.user.id,
        tag,
        namespace,
        weight,
        rationale,
    )
    .await
    {
        Ok(proposal) => Ok((StatusCode::CREATED, Json(serde_json::json!(proposal)))),
        Err(refusal) => Err(theme_refusal(refusal)),
    }
}

/// A consent that ratified nothing is `202`, not `200`: answering 200 tells a client the roadmap moved when it
/// did not. The Go handler draws the same line, which is why this mirrors it clause for clause.
async fn consent_theme(
    State(state): State<AppState>,
    Path((project, id)): Path<(String, i64)>,
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
    let project_id = project_by_id(&state, &project).await?.id;
    let who = strategy_project(&state, project_id, &user).await?;
    match concord_db::themes::consent(&state.pool, id, who.user.id).await {
        Ok((proposal, ratified)) => Ok((
            if ratified { StatusCode::OK } else { StatusCode::ACCEPTED },
            Json(serde_json::json!({ "proposal": proposal, "ratified": ratified })),
        )),
        Err(refusal) => match refusal {
            // A recorded consent below the threshold is not an error the client should treat as a failure: it is
            // the state §5.1 exists to make visible.
            concord_db::themes::Refused::NotYet {
                verdict,
                consents,
                eligible,
            } => Ok((
                StatusCode::ACCEPTED,
                Json(serde_json::json!({
                    "recorded": true,
                    "ratified": false,
                    "verdict": verdict,
                    "consents": consents,
                    "eligible": eligible,
                })),
            )),
            other => Err(theme_refusal(other)),
        },
    }
}

async fn consent_weight(
    State(state): State<AppState>,
    Path((project, id)): Path<(String, i64)>,
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
    let project_id = project_by_id(&state, &project).await?.id;
    let who = strategy_project(&state, project_id, &user).await?;
    match concord_db::strategy::consent(&state.pool, id, who.user.id).await {
        Ok(recorded) => Ok((
            StatusCode::OK,
            Json(serde_json::json!({
                "recorded": true,
                "ratified": recorded.ratified,
                "stale": recorded.stale,
                "consents": recorded.consents,
                "eligible": recorded.eligible,
                "proposal": recorded.proposal,
            })),
        )),
        // The consent IS recorded when the threshold is not yet met. A 409 would tell the
        // reader the write failed when the store holds their vote, so answer 202 and let
        // the client quote the honest tally (D-180 applied to strategic weights).
        Err(concord_db::strategy::Refused::NotYet {
            verdict,
            consents,
            eligible,
        }) => Ok((
            StatusCode::ACCEPTED,
            Json(serde_json::json!({
                "recorded": true,
                "ratified": false,
                "decided": false,
                "verdict": verdict,
                "consents": consents,
                "eligible": eligible,
                "message": format!(
                    "Consent recorded. The quorum is not reached yet ({consents} of {eligible} eligible consenting)."
                ),
            })),
        )),
        Err(refusal) => Err(strategy_refusal(refusal)),
    }
}

async fn withdraw_theme(
    State(state): State<AppState>,
    Path((project, id)): Path<(String, i64)>,
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<StatusCode, ApiError> {
    let project_id = project_by_id(&state, &project).await?.id;
    let who = strategy_project(&state, project_id, &user).await?;
    let maintainer = who.role == "maintainer" || who.role == "owner";
    concord_db::themes::withdraw(&state.pool, id, who.user.id, maintainer)
        .await
        .map_err(theme_refusal)?;
    Ok(StatusCode::NO_CONTENT)
}

async fn withdraw_weight(
    State(state): State<AppState>,
    Path((project, id)): Path<(String, i64)>,
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<StatusCode, ApiError> {
    let project_id = project_by_id(&state, &project).await?.id;
    let who = strategy_project(&state, project_id, &user).await?;
    let maintainer = who.role == "maintainer" || who.role == "owner";
    concord_db::strategy::withdraw(&state.pool, id, who.user.id, maintainer)
        .await
        .map_err(strategy_refusal)?;
    Ok(StatusCode::NO_CONTENT)
}

/// The theme refusals, with the ones that are about the REQUEST at 400 and the ones about the CALLER at 403.
fn theme_refusal(refusal: concord_db::themes::Refused) -> ApiError {
    use concord_db::themes::Refused;
    match refusal {
        Refused::NotFound => ApiError::not_found("no such proposal"),
        Refused::Decided(status) => ApiError::conflict(format!("the proposal is already {status}")),
        Refused::Invalid(detail) => ApiError::invalid_request(detail),
        Refused::SelfConsent => {
            ApiError::forbidden("the proposer cannot consent to their own proposal")
        }
        Refused::NotEligible => ApiError::forbidden(
            "only an eligible collaborator may consent: §8.2 asks for a qualifying contribution",
        ),
        Refused::Duplicate(detail) => ApiError::conflict(detail),
        Refused::NotYet {
            verdict,
            consents,
            eligible,
        } => ApiError::conflict(format!(
            "the consent was recorded and the proposal is {verdict} ({consents} of {eligible} eligible \
             consenting)"
        )),
        Refused::NoLifetime(days) => ApiError::invalid_request(format!(
            "the charter's theme_lifetime_days is {days}: a theme written with no lifetime is born expired, so \
             nothing was applied"
        )),
        Refused::NotYours => {
            ApiError::forbidden("only the proposer or a maintainer may withdraw a proposal")
        }
        Refused::Failed(detail) => ApiError::conflict(format!("the change could not be applied: {detail}")),
    }
}

/// Strategy routes, typed on `AppState` (D-18). GET+POST pairs
/// stay in one MethodRouter (D-20).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{project_id}/strategy/weight-proposals",
            axum::routing::get(list_weight_proposals).post(propose_weight),
        )
        .route(
            "/api/v1/projects/{project_id}/strategy/weight-proposals/{id}/consent",
            axum::routing::post(consent_weight),
        )
        .route(
            "/api/v1/projects/{project_id}/strategy/weight-proposals/{id}/withdraw",
            axum::routing::post(withdraw_weight),
        )
        .route(
            "/api/v1/projects/{project_id}/strategy/themes",
            axum::routing::get(list_themes).post(propose_theme),
        )
        .route(
            "/api/v1/projects/{project_id}/strategy/themes/{id}/consent",
            axum::routing::post(consent_theme),
        )
        .route(
            "/api/v1/projects/{project_id}/strategy/themes/{id}/withdraw",
            axum::routing::post(withdraw_theme),
        )
        .route(
            "/api/v1/projects/{project_id}/strategy/theme-proposals",
            axum::routing::get(list_theme_proposals),
        )
}
