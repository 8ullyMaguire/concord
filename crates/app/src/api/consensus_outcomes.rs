//! Consensus call outcomes and amendments (step 3, ADR 0014).
//! The charter reader stays root-owned (D-51).

use axum::{
    extract::{Path, State},
    http::StatusCode,
    Extension, Json, Router,
};
use polaris_core::ApiError;
use serde::Deserialize;
use serde_json::Value;

use crate::{
    internal, parse_id, project_for, require_maintainer, write_error, AppState,
};

#[derive(Debug, Deserialize)]
struct CallOutcomeRequest {
    outcome: String,
    #[serde(default)]
    summary: String,
    #[serde(default)]
    fallback_to: i64,
    #[serde(default)]
    amendments: Option<AmendmentRequest>,
}

#[derive(Debug, Deserialize)]
struct AmendmentRequest {
    #[serde(default)]
    title: String,
    #[serde(default)]
    body: String,
    #[serde(default = "default_solution_type")]
    r#type: String,
}

fn default_solution_type() -> String {
    "internal".to_string()
}

/// `POST /api/v1/projects/{slug}/consensus/{call_id}/outcome` — §6.5's decision record.
///
/// The guard is "already decided", not "already closed": the page closes the call first (§6.8 writes the
/// verdict), and the decision record has to be writable after that. Guarding on `status` made the record
/// writable only by NOT closing the call.
async fn record_call_outcome(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<CallOutcomeRequest>,
) -> Result<Json<Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    require_maintainer(&state.pool, project.id, user.id).await?;
    let call_id = parse_id(&call, "call id")?;
    let amendments = body.amendments.map(|amendment| {
        concord_db::call_outcome::Amendments {
            author_id: user.id,
            title: amendment.title,
            body: amendment.body,
            solution_type: amendment.r#type,
        }
    });
    let recorded = concord_db::call_outcome::record(
        &state.pool,
        project.id,
        call_id,
        &body.outcome,
        &body.summary,
        body.fallback_to,
        amendments.as_ref(),
        user.id,
    )
    .await
    .map_err(write_error)?;
    Ok(Json(recorded))
}

/// A feature id in this project, or a 404 naming the feature.
///
/// The scope check matters: an id that exists in another project is not this project's business, and a

/// Outcome routes, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new().route(
        "/api/v1/projects/{slug}/consensus/{call}/outcome",
        axum::routing::post(record_call_outcome),
    )
}
