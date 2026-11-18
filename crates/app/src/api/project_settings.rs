//! Project settings: tags, languages and intake mode (step 3, ADR 0014).
//! Its readers joined the module; each GET+PUT route already lives with the writes (D-20/D-48/D-87).

use axum::{
    extract::{Path, State},
    http::header::HeaderMap,
    Extension, Json, Router,
};
use polaris_core::ApiError;

use crate::{
    bearer_user, internal, project_for, readable, readable_project_segment, require_role,
    write_audit, AppState,
};

/// `GET /api/v1/projects/{slug}/tags` — the project's tag names.
async fn project_tags(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    // Resolve and check the project FIRST: an empty list for a project the caller may not read is a 200 that
    // confirms the slug exists, which is the leak this rule exists to close.
    let user = bearer_user(&state, &headers).await?;
    if !readable(&state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    let tags = concord_db::taxonomy::project_tags(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(tags)))
}

/// `GET /api/v1/projects/{slug}/languages` — highest percentage first.
async fn project_languages(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    let user = bearer_user(&state, &headers).await?;
    if !readable(&state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    let rows = concord_db::criteria::languages(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

// ── project settings (§4.3, §6.2) ───────────────────────────────────────────────────────────────────────────────────

/// `PUT /projects/{slug}/tags` — apply tags that **already exist** in the instance vocabulary.
///
/// Three rules, each visible in what the endpoint refuses:
///
/// 1. the project is resolved first and its visibility checked, so a private project answers 404 rather than confirming
///    its slug;
/// 2. **contributor**, not maintainer: tagging is how a project becomes findable and every member can do it. A name the
///    taxonomy has never seen is **refused with the route that can propose one** — applied silently, a typo becomes a
///    vocabulary entry nobody ratified that every taxonomy-filtered search then has to carry;
/// 3. the response carries the tags **as stored**, because a client that just applied tags should not have to make a
///    second request to learn what stuck.
async fn apply_project_tags(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &slug, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let tags: Vec<String> = body
        .get("tags")
        .and_then(|value| value.as_array())
        .map(|items| {
            items
                .iter()
                .filter_map(|item| item.as_str().map(str::to_string))
                .collect()
        })
        .unwrap_or_default();
    for tag in &tags {
        match concord_db::taxonomy::apply_project_tag(&state.pool, project.id, tag, user.id)
            .await
            .map_err(internal)?
        {
            Ok(()) => {}
            Err(concord_db::taxonomy::Applied::UnknownName { name }) => {
                return Err(ApiError::invalid_request(format!(
                    "no tag named {name:?}; propose it with POST /api/v1/taxonomy/proposals (action=create)"
                )));
            }
            // Unreachable for a project's own tags: a project has no feature to be wrong about. Spelled out rather
            // than `_` so the day the enum grows, this match says where it did not apply.
            Err(concord_db::taxonomy::Applied::UnknownFeature) => {
                return Err(ApiError::not_found("no such project"));
            }
        }
    }
    write_audit(
        &state,
        Some(project.id),
        user.id,
        "apply_tags",
        "project",
        project.id,
        &tags.join(","),
    )
    .await?;
    let stored = concord_db::taxonomy::project_tags(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({ "tags": stored })))
}

/// `GET /projects/{slug}/tags/completeness` — §4.3's meter, with both thresholds in the body so the page can explain the
/// number instead of only showing it.
async fn tag_completeness(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &slug, &headers).await?;
    let meter = concord_db::taxonomy::completeness(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(meter).map_err(internal)?))
}

/// `PUT /projects/{slug}/languages` — replace the breakdown.
///
/// Two refusals, both of which are real defects if dropped: a percentage outside `0..=100`, and a **sum above 100.01**.
/// The discovery score uses these as shares, so a breakdown summing to 140 overstates the project everywhere it is ranked.
/// A change here was also never audited — a write that changes a project's face to discovery search had no record of who
/// changed it.
async fn set_project_languages(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &slug, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let languages: Vec<(String, f64)> = body
        .get("languages")
        .and_then(|value| value.as_array())
        .map(|items| {
            items
                .iter()
                .filter_map(|item| {
                    let language = item.get("language").and_then(|v| v.as_str())?;
                    let pct = item.get("pct").and_then(serde_json::Value::as_f64)?;
                    Some((language.to_string(), pct))
                })
                .collect()
        })
        .unwrap_or_default();
    concord_db::projects::set_languages(&state.pool, project.id, &languages)
        .await
        .map_err(internal)?
        .map_err(|invalid| ApiError::invalid_request(invalid.0))?;
    let summary = languages
        .iter()
        .map(|(language, pct)| format!("{language} {pct}%"))
        .collect::<Vec<_>>()
        .join(", ");
    write_audit(&state, Some(project.id), user.id, "set_languages", "project", project.id, &summary).await?;
    let refreshed = concord_db::projects::project_by_id(&state.pool, project.id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such project"))?;
    Ok(Json(serde_json::to_value(refreshed).map_err(internal)?))
}

/// `PUT /projects/{id}/intake-mode` — which side of the funnel the project opens (§6.2).
///
/// **Two field names for one value, and both are read.** `mode` is what the settings panel sends; `intake_mode` is what
/// the response calls it. A caller that echoes the response back is doing the obvious thing, and answering "must be one
/// of [complaint_first idea_first both]" to a request that sent exactly one of those reads as a validation bug — it was
/// found by a live probe, and the fallback costs nothing.
///
/// A save that changes nothing writes **no act**, because the panel posts the whole form.
async fn set_intake_mode(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let mut mode = body.get("mode").and_then(|v| v.as_str()).unwrap_or_default().to_string();
    if mode.is_empty() {
        mode = body
            .get("intake_mode")
            .and_then(|v| v.as_str())
            .unwrap_or_default()
            .to_string();
    }
    concord_db::projects::set_intake_mode(&state.pool, project.id, user.id, &mode)
        .await
        .map_err(internal)?
        .map_err(|invalid| ApiError::invalid_request(invalid.0))?;
    Ok(Json(serde_json::json!({ "intake_mode": mode })))
}



/// Project settings routes, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{slug}/tags",
            axum::routing::get(project_tags).put(apply_project_tags),
        )
        .route(
            "/api/v1/projects/{slug}/tags/completeness",
            axum::routing::get(tag_completeness),
        )
        .route(
            "/api/v1/projects/{slug}/languages",
            axum::routing::get(project_languages).put(set_project_languages),
        )
        .route(
            "/api/v1/projects/{project_id}/intake-mode",
            axum::routing::put(set_intake_mode),
        )
}
