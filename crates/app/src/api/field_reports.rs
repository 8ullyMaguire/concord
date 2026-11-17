//! Field reports: the read panel and the two writes (step 3, ADR 0014).
//!
//! The read lived in an earlier section of `lib.rs` than the writes; D-24
//! puts all three in one resource module.

use axum::{
    extract::{Path, State},
    http::{header::HeaderMap, StatusCode},
    Extension, Json, Router,
};
use polaris_core::ApiError;
use serde_json::Value;

use crate::{
    internal, project_by_id, readable_project, readable_project_segment, write_audit, AppState,
};

/// `GET /api/v1/projects/{id}/field-reports` — §4.7's "worked for 83% of reporters on arm64".
async fn list_field_reports(
    State(state): State<AppState>,
    Path(project): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The segment may be an id or a slug: the reference's page scripts build these URLs from the SLUG, and a path extractor typed
    // `i64` rejected the slug before any handler ran (`Invalid URL: Cannot parse `suki` to a `i64``). One resolver, asked once, at
    // the top of the handler — the `project_id` the body uses is its id.
    let project_id = project_by_id(&state, &project).await?.id;
    let project = readable_project(&state, project_id, &headers).await?;
    let panel = concord_db::field_reports::panel(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(panel)))
}

/// `POST /projects/{id}/field-reports` — file one structured experience record.
///
/// **Who may write, and why it is not a project role.** A field report is an experience record about USING the project, and
/// §4.7's sentence is "worked for 83% of reporters". A report that only members could file would make that number a poll of people
/// who already joined — the people most likely to have made it work. So this needs authentication and nothing else; the project is
/// still resolved through the readable-project path, so a private project's reports are 404 like everything else of its.
///
/// Auth is checked BEFORE the project, so an anonymous POST to a real project and to a nonexistent one both answer 401: a
/// different status for the second would enumerate the slugs on the instance.
async fn create_field_report(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<Value>,
) -> Result<(StatusCode, Json<Value>), ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let field = |name: &str| -> String {
        body.get(name)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string()
    };
    // There is no `user_id` and no `project_id` in this form: the author is the token's user and the project is the URL's. A body
    // that carries them is ignored, not preferred — a report whose author is whatever the body said is not evidence.
    let form = concord_db::field_report_writes::NewReport {
        version: field("version"),
        use_case: field("use_case"),
        environment: field("environment"),
        scale: field("scale"),
        duration: field("duration"),
        outcome: field("outcome"),
        migrated_to: field("migrated_to"),
        caveats: field("caveats"),
        workaround: field("workaround"),
        advice: field("advice"),
    };
    let report = match concord_db::field_report_writes::create(&state.pool, &project, user.id, &form)
        .await
        .map_err(internal)?
    {
        Ok(report) => report,
        Err(concord_db::field_report_writes::Refused::Invalid(message)) => {
            return Err(ApiError::invalid_request(message))
        }
        Err(concord_db::field_report_writes::Refused::NotFound(message)) => {
            return Err(ApiError::not_found(message))
        }
    };
    // The audit detail carries the OUTCOME, not the prose: the outcome is the field that decides what this report counts for in
    // §4.2's rate, and it is the one worth reconstructing later.
    write_audit(
        &state,
        Some(project.id),
        user.id,
        "file_field_report",
        "field_report",
        report.id,
        &report.outcome,
    )
    .await?;
    Ok((
        StatusCode::CREATED,
        Json(serde_json::to_value(report).map_err(internal)?),
    ))
}

/// `POST /projects/{id}/field-reports/{report_id}/responses` — reply to a report.
///
/// The route is project-scoped even though the report carries its own project, so the visibility check is the shared one and cannot
/// be forgotten; the store then proves the report is IN that project, because report ids are global and a URL that names one id and
/// acts on another is the nested-route defect this port has already met once.
///
/// `is_owner` is set from the membership table and never read from the request — the flag's whole value is that the server sets it.
async fn respond_to_field_report(
    State(state): State<AppState>,
    Path((project_segment, report_id)): Path<(String, i64)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<Value>,
) -> Result<(StatusCode, Json<Value>), ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let text = body.get("body").and_then(Value::as_str).unwrap_or_default();
    let response = match concord_db::field_report_writes::respond(&state.pool, &project, report_id, user.id, text)
        .await
        .map_err(internal)?
    {
        Ok(response) => response,
        Err(concord_db::field_report_writes::Refused::Invalid(message)) => {
            return Err(ApiError::invalid_request(message))
        }
        Err(concord_db::field_report_writes::Refused::NotFound(message)) => {
            return Err(ApiError::not_found(message))
        }
    };
    write_audit(
        &state,
        Some(project.id),
        user.id,
        "respond_field_report",
        "field_report",
        report_id,
        &format!("is_owner={}", response.is_owner),
    )
    .await?;
    Ok((
        StatusCode::CREATED,
        Json(serde_json::to_value(response).map_err(internal)?),
    ))
}



/// GET+POST on one path stay in one MethodRouter (D-20).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{project_id}/field-reports",
            axum::routing::get(list_field_reports).post(create_field_report),
        )
        .route(
            "/api/v1/projects/{project_id}/field-reports/{report_id}/responses",
            axum::routing::post(respond_to_field_report),
        )
}
