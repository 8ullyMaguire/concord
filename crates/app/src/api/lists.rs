//! Project lists, list entries and project metrics/visibility writes
//! (step 3, ADR 0014). Shared root helpers stay at the crate root (D-41/D-43).

use axum::{
    extract::{Path, State},
    http::{header::HeaderMap, StatusCode},
    Extension, Json, Router,
};
use polaris_core::ApiError;

use crate::{
    bearer_user, internal, numeric_project, readable, readable_project_segment,
    require_role, write_audit, AppState,
};

/// `GET /api/v1/projects/{id}/lists` — the project's own lists first, then the instance-wide ones.
async fn project_lists(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The segment is a SLUG first and an id second, because that is what the reference's `requireProjectID` does: it calls
    // `GetProject(slug)` on a parameter it names `project_id`. This route took a numeric id, so a page linking to
    // `/projects/suki/lists` — the form every other project route accepts — got a 400 from a path that names the right project.
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let rows = concord_db::criteria::lists(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

/// The visibility decision for a LIST, which is not the decision for its project.
///
/// A protected list inside a public project must not become readable because its parent is, and an instance-wide list
/// (no project at all) is readable from everywhere by §16. Both refusals are 404: a 403 would confirm the id exists.
///
/// The path's project is still checked, because the route names one and a client that pairs a list with the wrong
/// project is asking a question the URL cannot answer — the Go keeps the same check for the same reason.
async fn readable_list(
    state: &AppState,
    path_project: &concord_db::projects::Project,
    list_segment: &str,
    headers: &HeaderMap,
) -> Result<concord_db::criteria::ProjectList, ApiError> {
    let Ok(id) = list_segment.parse::<i64>() else {
        return Err(ApiError::invalid_request("list id must be an integer"));
    };
    let Some(list) = concord_db::criteria::list_by_id(&state.pool, id)
        .await
        .map_err(internal)?
    else {
        return Err(ApiError::not_found(format!("list {list_segment}")));
    };
    match list.project_id {
        // Instance-wide: no project to consult, and the content is public by definition.
        None => Ok(list),
        Some(owner) => {
            let user = bearer_user(state, headers).await?;
            let owner_project = concord_db::projects::project_by_id(&state.pool, owner)
                .await
                .map_err(internal)?
                .ok_or_else(|| ApiError::not_found(format!("list {list_segment}")))?;
            if !readable(state, &owner_project, user.as_ref()).await?
                || !readable(state, path_project, user.as_ref()).await?
            {
                return Err(ApiError::not_found(format!("list {list_segment}")));
            }
            Ok(list)
        }
    }
}

/// `GET /api/v1/projects/{project_id}/lists/{id}`
async fn get_list(
    State(state): State<AppState>,
    Path((project_id, list)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = numeric_project(&state, &project_id, &headers).await?;
    let found = readable_list(&state, &project, &list, &headers).await?;
    Ok(Json(serde_json::json!(found)))
}

/// `GET /api/v1/projects/{project_id}/lists/{id}/entries` — §16's ranked entries.
async fn get_list_entries(
    State(state): State<AppState>,
    Path((project_id, list)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = numeric_project(&state, &project_id, &headers).await?;
    let found = readable_list(&state, &project, &list, &headers).await?;
    let rows = concord_db::criteria::entries(&state.pool, found.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

// ── lists (§16) and project settings (§4.1) ────────────────────────────────────────────────────────────────────────

/// `POST /projects/{id}/lists` — a list of resources, scoped to the project or instance-wide.
///
/// **Instance-wide must be ASKED for** (`instance_wide: true`). The project in the path is also the access check, so
/// defaulting to global would let any project member mint a global list. The audit row is written against the **path's**
/// project even when the scope is global: an instance-wide list has no project to audit under, and the trail is what says
/// who may do this.
async fn create_list(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let instance_wide = body
        .get("instance_wide")
        .and_then(serde_json::Value::as_bool)
        .unwrap_or(false);
    let slug = body.get("slug").and_then(|v| v.as_str()).unwrap_or_default();
    let title = body.get("title").and_then(|v| v.as_str()).unwrap_or_default();
    let description = body.get("description").and_then(|v| v.as_str()).unwrap_or_default();
    let scope = if instance_wide { None } else { Some(project.id) };
    let list = concord_db::list_writes::create(&state.pool, scope, user.id, slug, title, description)
        .await
        .map_err(internal)?;
    write_audit(&state, Some(project.id), user.id, "create_list", "list", list.id, slug).await?;
    Ok((StatusCode::CREATED, Json(serde_json::to_value(list).map_err(internal)?)))
}

/// `POST /projects/{id}/lists/{list}/entries` — propose one entry.
///
/// The list is resolved through the read helper, so its own project decides readability (an instance-wide list is
/// readable by everyone, §16) and a refusal is a 404 rather than a 403, which would confirm the id is real.
async fn create_list_entry(
    State(state): State<AppState>,
    Path((project_segment, list)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to propose an entry"))?;
    let _project = readable_project_segment(&state, &project_segment, &headers).await?;
    let list_id: i64 = list
        .parse()
        .map_err(|_| ApiError::not_found("no such list"))?;
    let list = concord_db::criteria::list_by_id(&state.pool, list_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such list"))?;
    if let Some(owner) = list.project_id {
        if owner != _project.id {
            return Err(ApiError::not_found("no such list"));
        }
    }
    let url = body.get("url").and_then(|v| v.as_str()).unwrap_or_default();
    let title = body.get("title").and_then(|v| v.as_str()).unwrap_or_default();
    let description = body.get("description").and_then(|v| v.as_str()).unwrap_or_default();
    let entry = concord_db::list_writes::add_entry(&state.pool, list_id, user.id, url, title, description)
        .await
        .map_err(internal)?;
    write_audit(&state, Some(_project.id), user.id, "create_list_entry", "list_entry", entry.id, title).await?;
    Ok((StatusCode::CREATED, Json(serde_json::to_value(entry).map_err(internal)?)))
}

/// `POST /projects/{id}/lists/{list}/entries/vote` — compare two entries.
///
/// **An instance-wide list cannot be voted on through a project URL**: the weight formula needs a project and there is
/// none, and computing one from an unrelated project would be inventing an input. It answers 404, the same as any other
/// list that does not belong to this project.
///
/// The response carries the **entries back**, because a vote the caller cannot see take effect is a vote they cannot
/// trust, and the count of comparisons beside an entry is what makes the ranking legible.
async fn vote_list_entry(
    State(state): State<AppState>,
    Path((project_segment, list)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let list_id: i64 = list
        .parse()
        .map_err(|_| ApiError::not_found("no such list"))?;
    let list = concord_db::criteria::list_by_id(&state.pool, list_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such list"))?;
    if list.project_id != Some(project.id) {
        return Err(ApiError::not_found("no such list"));
    }
    let entry_a = body
        .get("entry_a")
        .and_then(serde_json::Value::as_i64)
        .unwrap_or_default();
    let entry_b = body
        .get("entry_b")
        .and_then(serde_json::Value::as_i64)
        .unwrap_or_default();
    let outcome = body.get("outcome").and_then(|v| v.as_str()).unwrap_or_default();
    let charter = concord_db::charter::charter(&state.pool, project.id)
        .await
        .map_err(internal)?;
    let weight = concord_db::voteweight::answer_vote_weight(&state.pool, project.id, user.id, &charter)
        .await
        .map_err(internal)?;
    match concord_db::list_writes::vote_entry(
        &state.pool,
        list_id,
        entry_a,
        entry_b,
        user.id,
        outcome,
        weight,
        charter.glicko_tau,
    )
    .await
    .map_err(internal)?
    {
        Ok(()) => {}
        Err(concord_db::list_writes::Refused::Invalid(message)) => {
            return Err(ApiError::invalid_request(message));
        }
        Err(concord_db::list_writes::Refused::Forbidden(message)) => {
            return Err(ApiError::forbidden(message));
        }
        Err(concord_db::list_writes::Refused::NotFound) => {
            return Err(ApiError::not_found("no such entry"));
        }
    }
    write_audit(
        &state,
        Some(project.id),
        user.id,
        "vote_list_entry",
        "list",
        list_id,
        &format!("a={entry_a} b={entry_b} outcome={outcome}"),
    )
    .await?;
    let entries = concord_db::criteria::entries(&state.pool, list_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({ "status": "voted", "entries": entries })))
}

/// `PUT /projects/{slug}/metrics` — the forge numbers a maintainer supplies by hand.
///
/// The write goes through the same `sync` the forge importer uses, so the health score is computed in one place: a second
/// formula here would be a second answer to "how healthy is this project" and the search ordering reads the first one.
///
/// A missing `median_review_hours` means **unknown**, not zero — the sentinel, which is written as NULL. Writing `0`
/// would publish "responds instantly" for a repository nobody has measured, and `-1` would outlive its convention.
async fn set_project_metrics(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &slug, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let number = |key: &str| body.get(key).and_then(serde_json::Value::as_i64).unwrap_or_default();
    let float = |key: &str| body.get(key).and_then(serde_json::Value::as_f64);
    let measurement = concord_db::metrics::Measurement {
        slug: project.slug.clone(),
        measured: true,
        stars: number("stars"),
        forks: number("forks"),
        open_issues: number("open_issues"),
        commit_count: number("commit_count"),
        contributors: number("contributors"),
        last_commit_age_days: float("last_commit_age_days")
            .unwrap_or(concord_db::metrics::UNKNOWN_AGE),
        median_review_hours: float("median_review_hours")
            .unwrap_or(concord_db::metrics::UNKNOWN_REVIEW_HOURS),
        releases_90d: number("releases_90d"),
        license: body
            .get("license")
            .and_then(|v| v.as_str())
            .map(str::to_string),
    };
    let _synced = concord_db::metrics::sync(&state.pool, &measurement)
        .await
        .map_err(internal)?
        .map_err(|_| ApiError::invalid_request("the measurement was not usable".to_string()))?;
    write_audit(
        &state,
        Some(project.id),
        user.id,
        "set_metrics",
        "project",
        project.id,
        &format!(
            "stars={} forks={} commits={} license={}",
            measurement.stars,
            measurement.forks,
            measurement.commit_count,
            measurement.license.clone().unwrap_or_default()
        ),
    )
    .await?;
    let refreshed = concord_db::projects::project_by_id(&state.pool, project.id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such project"))?;
    Ok(Json(serde_json::to_value(refreshed).map_err(internal)?))
}

/// `PUT /projects/{slug}/visibility` — §4.1's four states.
///
/// A **non-member gets 404, not 403**: a 403 confirms the slug exists, and visibility is the one setting where the
/// existence of the project is the secret. An unknown word is refused with the valid set in the message, because the
/// panel offers a fixed list and a caller who typed one is owed that list.
async fn set_project_visibility(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = match concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
    {
        Some(project) => project,
        None => return Err(ApiError::not_found("no such project")),
    };
    if concord_db::projects::member_role(&state.pool, project.id, user.id)
        .await
        .map_err(internal)?
        .is_none()
    {
        return Err(ApiError::not_found("no such project"));
    }
    let visibility = body
        .get("visibility")
        .and_then(|v| v.as_str())
        .unwrap_or_default();
    let before = project.visibility.clone();
    let updated = concord_db::projects::set_visibility(&state.pool, &slug, visibility)
        .await
        .map_err(internal)?
        .map_err(|invalid| ApiError::invalid_request(invalid.0))?;
    write_audit(
        &state,
        Some(project.id),
        user.id,
        "set_visibility",
        "project",
        project.id,
        &format!("{before} -> {}", updated.visibility),
    )
    .await?;
    Ok(Json(serde_json::to_value(updated).map_err(internal)?))
}



/// List and project-settings write routes, typed on `AppState` (D-18).
/// GET+POST pairs stay in one MethodRouter (D-20).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{project_id}/lists",
            axum::routing::get(project_lists).post(create_list),
        )
        .route(
            "/api/v1/projects/{project_id}/lists/",
            axum::routing::get(project_lists),
        )
        .route(
            "/api/v1/projects/{project_id}/lists/{list}",
            axum::routing::get(get_list),
        )
        .route(
            "/api/v1/projects/{project_id}/lists/{list}/",
            axum::routing::get(get_list),
        )
        .route(
            "/api/v1/projects/{project_id}/lists/{list}/entries",
            axum::routing::get(get_list_entries).post(create_list_entry),
        )
        .route(
            "/api/v1/projects/{project_id}/lists/{list}/entries/vote",
            axum::routing::post(vote_list_entry),
        )
        .route(
            "/api/v1/projects/{slug}/metrics",
            axum::routing::put(set_project_metrics),
        )
        .route(
            "/api/v1/projects/{slug}/visibility",
            axum::routing::put(set_project_visibility),
        )
}
