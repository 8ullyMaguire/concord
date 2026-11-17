//! Project complaints: public list and authenticated filing (step 3, ADR 0014).
//! Shared helpers and FeatureQuery stay at the crate root (D-41/D-43).

use axum::{
    extract::{Path, Query, State},
    http::{header::HeaderMap, StatusCode},
    response::IntoResponse,
    Extension, Json, Router,
};
use polaris_core::ApiError;
use serde_json::Value;

use crate::{
    bearer_user, caller_complaint, complaint_refusal, internal, readable_project_segment, require_role,
    write_audit, AppState, FeatureQuery,
};

/// `GET /api/v1/projects/{slug}/complaints[?status=]` — the project's complaints, newest first.
async fn list_complaints(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Query(query): Query<FeatureQuery>,
) -> Result<Json<Value>, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let complaints = concord_db::complaints::list_complaints(
        &state.pool,
        project.id,
        query.status.as_deref().unwrap_or(""),
    )
    .await
    .map_err(internal)?;
    Ok(Json(serde_json::to_value(complaints).map_err(internal)?))
}


/// `POST /projects/{slug}/complaints` — file a complaint.
///
/// Six rules, five of them things that were once wrong here:
///
/// 1. **authenticated.** This handler had no check while every sibling did, so a complaint could be filed anonymously and
///    written with `author_id 0` — a claim about a project, and the main input to the ranking, attributed to nobody;
/// 2. **the path and the body must agree** about the project. The route was later mounted under `/projects/{id}/…`, so
///    the same project arrives twice; the path wins, because it is what the caller's standing was checked against, and a
///    contradicting body id is refused rather than silently ignored;
/// 3. **`strategic_multiplier` defaults to 1.0**, not 0. The form sends severity and frequency only, and pain is
///    multiplied by this number: 0 makes the complaint contribute exactly nothing, with no indication why;
/// 4. **§6.1's duplicate gate runs before the write**, instance-wide, and `confirm_duplicate` proceeds;
/// 5. **indexing may fail without losing the complaint** — the audit says `embed_failed` and the filing stands;
/// 6. **filing makes you a participant**: +5 reputation for `submit_complaint` and `contributor` membership, or-ignore so an
///    owner who files is not demoted.
async fn create_complaint(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<axum::response::Response, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to file a complaint"))?;
    let project = readable_project_segment(&state, &slug, &headers).await?;
    if let Some(named) = body.get("project_id").and_then(serde_json::Value::as_i64) {
        if named > 0 && named != project.id {
            return Err(ApiError::invalid_request(format!(
                "project_id {named} does not match the project in the path"
            )));
        }
    }
    let title = body.get("title").and_then(|v| v.as_str()).unwrap_or_default().to_string();
    let text = body.get("body").and_then(|v| v.as_str()).unwrap_or_default().to_string();
    let severity = body.get("severity").and_then(serde_json::Value::as_i64).unwrap_or(1);
    let frequency = body.get("frequency").and_then(serde_json::Value::as_f64).unwrap_or(1.0);
    let multiplier = body
        .get("strategic_multiplier")
        .and_then(serde_json::Value::as_f64)
        .filter(|value| *value > 0.0)
        .unwrap_or(1.0);
    let confirmed = body
        .get("confirm_duplicate")
        .and_then(serde_json::Value::as_bool)
        .unwrap_or(false);

    // Rule 4. The gate is the same one the request path uses; its scope is instance-wide for §6.1's "this was fixed in X".
    let mut warned = 0usize;
    let mut top = 0.0f64;
    if let Ok(sims) = concord_db::embeddings::find_similar(
        &state.pool,
        state.embedder.as_ref(),
        concord_db::embeddings::KIND_COMPLAINT,
        &title,
        0,
        user.id,
        Some(concord_db::embeddings::FIELD_TITLE),
        5,
    )
    .await
    {
        warned = sims.len();
        top = sims.first().map_or(0.0, |sim| sim.score);
        let model = state.embedder.id();
        if warned > 0 && !confirmed && concord_embed::bands::is_strong_duplicate_for(top, &model) {
            let (strong, _, _) = concord_embed::bands::thresholds_for(&model);
            return Ok((
                StatusCode::CONFLICT,
                Json(serde_json::json!({
                    "error": "an almost identical complaint already exists",
                    "similar": sims,
                    "top_score": top,
                    "threshold": strong,
                    "resubmit_with": { "confirm_duplicate": true },
                })),
            )
                .into_response());
        }
    }

    let complaint = concord_db::complaints::create(
        &state.pool,
        project.id,
        user.id,
        &title,
        &text,
        severity,
        frequency,
        multiplier,
    )
    .await
    .map_err(internal)?;

    // Rule 5.
    if let Err(error) = concord_db::embeddings::upsert_entity_text(
        &state.pool,
        state.embedder.as_ref(),
        concord_db::embeddings::KIND_COMPLAINT,
        complaint.id,
        project.id,
        &title,
        &text,
    )
    .await
    {
        write_audit(&state, Some(project.id), user.id, "embed_failed", "complaint", complaint.id, &error.to_string())
            .await?;
    }
    write_audit(&state, Some(project.id), user.id, "create_complaint", "complaint", complaint.id, &title).await?;
    if warned > 0 {
        // Recording that a filer was warned, and filed anyway, is what makes the threshold arguable with evidence rather
        // than intuition.
        write_audit(
            &state,
            Some(project.id),
            user.id,
            "duplicate_warning_shown",
            "complaint",
            complaint.id,
            &format!("{warned} similar entries, top score {top:.3}"),
        )
        .await?;
    }
    // Rule 6.
    concord_db::membership::add_reputation(&state.pool, project.id, user.id, "submit_complaint", 5.0)
        .await
        .map_err(internal)?;
    concord_db::membership::join(&state.pool, project.id, user.id)
        .await
        .map_err(internal)?;
    Ok((StatusCode::CREATED, Json(serde_json::json!(complaint))).into_response())
}

/// `POST /projects/{slug}/complaints/{id}/impact` — how much this hurts. `?severity=` defaults to 1.
///
/// The project half of the path is checked even though the reference's version reads only the id: this route is nested
/// under the same path as the others, and leaving one handler reading half a URL is how the others were wrong.
///
/// **A signed-in caller is required**, which the reference does not enforce: it passes `getActorID(r)` straight into
/// `complaint_impacts.user_id`, which is a foreign key — so an anonymous impact was a 500 there and is a 401 here. The
/// row cannot exist without an attributable person, so 401 states the constraint the schema already holds.
async fn add_complaint_impact(
    State(state): State<AppState>,
    Path((slug, id)): Path<(String, String)>,
    Query(query): Query<SeverityQuery>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &slug, &headers).await?;
    let complaint = caller_complaint(&state, &project, &id).await?;
    let severity = query
        .severity
        .and_then(|value| value.parse::<i64>().ok())
        .filter(|value| *value != 0)
        .unwrap_or(1);
    concord_db::complaints::add_impact(&state.pool, complaint.id, user.id, severity)
        .await
        .map_err(internal)?
        .map_err(complaint_refusal)?;
    Ok(Json(serde_json::json!({ "status": "impact recorded" })))
}

#[derive(Debug, serde::Deserialize)]
struct SeverityQuery {
    severity: Option<String>,
}

/// `POST /projects/{slug}/complaints/{id}/validate` — §M1's first verdict. Requires **contributor**, not the §8.2
/// eligible-collaborator rule: eligibility requires a qualifying contribution, and one of the qualifiers is a validated
/// complaint that led to a shipped solution — so gating validation on eligibility would ask for a validation before
/// allowing the first one. Joining grants `contributor`, so this is "a member of this project" and nothing narrower.
async fn validate_complaint(
    State(state): State<AppState>,
    Path((slug, id)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &slug, &headers).await?;
    let complaint = caller_complaint(&state, &project, &id).await?;
    require_role(&state, &project, "contributor", &user).await?;
    concord_db::complaints::validate(&state.pool, complaint.id)
        .await
        .map_err(internal)?
        .map_err(complaint_refusal)?;
    write_audit(&state, Some(project.id), user.id, "validate_complaint", "complaint", complaint.id, "").await?;
    Ok(Json(serde_json::json!({ "status": "validated" })))
}


/// Complaint read and write routes, typed on `AppState` (D-18).
/// GET and POST stay in one MethodRouter (D-20).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{slug}/complaints",
            axum::routing::get(list_complaints).post(create_complaint),
        )
        .route(
            "/api/v1/projects/{slug}/complaints/{id}/impact",
            axum::routing::post(add_complaint_impact),
        )
        .route(
            "/api/v1/projects/{slug}/complaints/{id}/validate",
            axum::routing::post(validate_complaint),
        )
}
