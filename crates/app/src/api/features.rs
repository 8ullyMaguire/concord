//! Project features: catalogue, filing, edits and status (step 3, ADR 0014).
//! FeatureQuery and write_error stay at the crate root: the complaint,
//! milestone and roadmap readers use the query, complaints use the error mapper (D-41).

use axum::{
    extract::{Path, Query, State},
    Extension, Json, Router,
};
use polaris_core::ApiError;
use serde::Deserialize;
use serde_json::{json, Value};

use crate::{internal, write_error, FeatureQuery, AppState};

/// `GET /api/v1/projects/{slug}/features[?status=]` — the project's features, newest first.
async fn list_features(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Query(query): Query<FeatureQuery>,
) -> Result<Json<Vec<concord_db::features::Feature>>, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let features = concord_db::features::list_features(
        &state.pool,
        project.id,
        query.status.as_deref().unwrap_or(""),
    )
    .await
    .map_err(internal)?;
    Ok(Json(features))
}

/// `GET /api/v1/projects/{slug}/features/{id}` — one feature, but only under its own project.
///
/// Two refusals, both load-bearing and both in the Go handler: the id must parse (a non-integer is a
/// 400, not a 404), and the feature's `project_id` must be this project's. Without the second, a
/// caller reads any project's work by counting ids while the list endpoint answers 404.
async fn get_feature(
    State(state): State<AppState>,
    Path((slug, feature_id)): Path<(String, String)>,
) -> Result<Json<concord_db::features::Feature>, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let Ok(id) = feature_id.parse::<i64>() else {
        return Err(ApiError::invalid_request("feature id must be an integer"));
    };
    let found = concord_db::features::feature(&state.pool, id)
        .await
        .map_err(internal)?
        .filter(|feature| feature.project_id == project.id);
    match found {
        Some(feature) => Ok(Json(feature)),
        None => Err(ApiError::not_found(format!("feature {id}"))),
    }
}


#[derive(Debug, Deserialize)]
struct FileFeatureRequest {
    title: String,
    #[serde(default)]
    body: String,
    #[serde(default)]
    effort: String,
    #[serde(default)]
    impact: Option<i64>,
    #[serde(default)]
    effort_score: Option<i64>,
    #[serde(default)]
    linked_complaints: Vec<i64>,
    #[serde(default)]
    complaint_ids: Vec<i64>,
}

/// `POST /api/v1/projects/{slug}/features` — file a feature.
///
/// Needs an account: an idea is a claim about work, and a nameless one cannot be attributed or
/// rate-limited. `complaint_ids` is accepted beside `linked_complaints` because both spellings are in
/// the Go struct's history and a caller that used either must not be silently ignored.
async fn file_feature(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<FileFeatureRequest>,
) -> Result<(axum::http::StatusCode, Json<Value>), ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let mut linked = body.linked_complaints.clone();
    for id in &body.complaint_ids {
        if !linked.contains(id) {
            linked.push(*id);
        }
    }
    let feature = concord_db::feature_writes::create_feature(
        &state.pool,
        project.id,
        user.id,
        body.title.trim(),
        &body.body,
        &body.effort,
        body.impact,
        body.effort_score,
        &linked,
    )
    .await
    .map_err(write_error)?;
    Ok((
        axum::http::StatusCode::CREATED,
        Json(serde_json::to_value(feature).map_err(internal)?),
    ))
}

#[derive(Debug, Deserialize)]
struct SetStatusRequest {
    status: String,
}

/// `PUT /api/v1/projects/{slug}/features/{id}/status` — the evidence and dependency gates.
async fn set_feature_status(
    State(state): State<AppState>,
    Path((slug, feature)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<SetStatusRequest>,
) -> Result<Json<Value>, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let Ok(id) = feature.parse::<i64>() else {
        return Err(ApiError::invalid_request("feature id must be an integer"));
    };
    // The feature must be this project's before it is moved: a write addressed to one project must
    // not change another's work.
    let found = concord_db::features::feature(&state.pool, id)
        .await
        .map_err(internal)?
        .filter(|row| row.project_id == project.id)
        .ok_or_else(|| ApiError::not_found(format!("feature {id}")))?;
    let from = found.status.clone();
    concord_db::feature_writes::update_feature_status_with_event(
        &state.pool,
        project.id,
        id,
        user.id,
        &body.status,
        &from,
    )
    .await
    .map_err(write_error)?;
    let _ = concord_db::feature_writes::add_audit(
        &state.pool,
        project.id,
        user.id,
        "set_feature_status",
        "feature",
        id,
        &format!("{from} -> {}", body.status),
    )
    .await;
    Ok(Json(json!({"status": "ok", "from": from, "to": body.status})))
}


#[derive(Debug, Deserialize)]
struct EditFeatureRequest {
    // `Option<Option<T>>` would be the honest type for "leave it" vs "blank it", and serde gives it to us for free with this shape: a
    // field that is absent stays `None`, and `{"body": ""}` arrives as `Some("")`.
    title: Option<String>,
    body: Option<String>,
    impact: Option<i64>,
    effort_score: Option<i64>,
}

/// `PUT /api/v1/projects/{slug}/features/{id}` — edit a feature's prose and its two scores.
///
/// This is §42.6's "no route edits a feature's body, impact or effort", the gap that forced a raw `UPDATE` plus a hand-written audit row
/// when an ideas pass had to correct its own wording. It is not a status move: `PUT .../status` keeps its own gates (evidence,
/// dependencies), and nothing here can change a status.
///
/// Authority is owner or maintainer, through the same `role_for` the panels use — a contributor may file and argue, but rewriting the
/// text somebody else's board is organised around is a maintainer's act. A guest is refused for the same reason a guest cannot create a
/// project: the record must name somebody.
///
/// The audit row carries the CHANGE, field by field ("body: 120 chars -> 98 chars"), because an audit row that says only "edited" makes
/// the reader reconstruct the difference from timestamps.
async fn edit_feature(
    State(state): State<AppState>,
    Path((slug, feature)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<EditFeatureRequest>,
) -> Result<Json<Value>, ApiError> {
    if concord_db::identity::is_guest(&state.pool, user.id).await.map_err(internal)? {
        return Err(ApiError::forbidden(
            "a guest cannot edit a feature: the record must name somebody",
        ));
    }
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let Ok(id) = feature.parse::<i64>() else {
        return Err(ApiError::invalid_request("feature id must be an integer"));
    };
    let role = concord_db::feed_reads::role_for(&state.pool, project.id, user.id)
        .await
        .map_err(internal)?;
    if !matches!(role.as_deref(), Some("owner") | Some("maintainer")) {
        return Err(ApiError::forbidden(
            "only the project's owner or a maintainer may edit this feature",
        ));
    }
    let before = concord_db::features::feature(&state.pool, id)
        .await
        .map_err(internal)?
        .filter(|row| row.project_id == project.id)
        .ok_or_else(|| ApiError::not_found(format!("feature {id}")))?;

    let edit = concord_db::feature_writes::FeatureEdit {
        title: body.title.clone(),
        body: body.body.clone(),
        impact: body.impact,
        effort_score: body.effort_score,
    };
    let after = concord_db::feature_writes::update_feature_content(&state.pool, id, &edit)
        .await
        .map_err(write_error)?;

    let mut changed: Vec<String> = Vec::new();
    if before.title != after.title {
        changed.push(format!("title: {:?} -> {:?}", before.title, after.title));
    }
    if before.body != after.body {
        changed.push(format!(
            "body: {} chars -> {} chars",
            before.body.chars().count(),
            after.body.chars().count()
        ));
    }
    // `Option` printed with `{:?}` writes `Some(7)` into an audit row a person reads; "none -> 7" is the sentence.
    let score = |value: Option<i64>| match value {
        Some(number) => number.to_string(),
        None => "none".to_string(),
    };
    if before.impact != after.impact {
        changed.push(format!(
            "impact: {} -> {}",
            score(before.impact),
            score(after.impact)
        ));
    }
    if before.effort_score != after.effort_score {
        changed.push(format!(
            "effort_score: {} -> {}",
            score(before.effort_score),
            score(after.effort_score)
        ));
    }
    concord_db::feature_writes::add_audit(
        &state.pool,
        project.id,
        user.id,
        "edit_feature",
        "feature",
        id,
        &changed.join("; "),
    )
    .await
    .map_err(write_error)?;

    Ok(Json(json!({"status": "ok", "changed": changed, "feature": after})))
}


/// Feature routes, typed on `AppState` (D-18); GET and POST stay in one MethodRouter (D-20).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{slug}/features",
            axum::routing::get(list_features).post(file_feature),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}",
            axum::routing::get(get_feature).put(edit_feature),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/status",
            axum::routing::put(set_feature_status),
        )
}
