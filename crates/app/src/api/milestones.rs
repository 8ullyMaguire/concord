//! Project milestones: the milestone reader, list, roadmap detail and the six writes
//! (step 3, ADR 0014). write_error stays at the crate root: other resources share it (D-41/D-43).

use axum::{
    extract::{Path, Query, State},
    http::{header::HeaderMap, StatusCode},
    Extension, Json, Router,
};
use polaris_core::ApiError;
use crate::pagination::PageQuery;
use serde::Serialize;
use serde_json::{json, Value};
use utoipa::ToSchema;

use crate::{
    internal, numeric_project, readable_project_segment, require_role, write_audit, AppState,
};

/// `GET /api/v1/projects/{project_id}/milestones/{id}` — the milestone and the work planned into it.
///
/// Two keys in one body, because the two are always read together: the counts answer "how far along is it" and the
/// feature list answers "what is left", and a page that had to fetch them separately could render one without the
/// other and show a milestone whose remaining work contradicts its own progress bar.
async fn get_milestone(
    State(state): State<AppState>,
    Path((project_id, milestone)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = numeric_project(&state, &project_id, &headers).await?;
    let id = milestone
        .parse::<i64>()
        .map_err(|_| ApiError::invalid_request("milestone id must be an integer"))?;
    let found = concord_db::milestones::milestone(&state.pool, project.id, id)
        .await
        .map_err(internal)?;
    let Some(found) = found else {
        return Err(ApiError::not_found(format!("milestone {milestone}")));
    };
    let features = concord_db::features::milestone_features(&state.pool, found.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({ "milestone": found, "features": features })))
}

/// `GET /api/v1/projects/{slug}/milestones` — the milestones, with counts computed on read.
async fn list_milestones(
    State(state): State<AppState>,
    Query(page_query): Query<PageQuery>,
    Path(slug): Path<String>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let page = page_query.valid().map_err(ApiError::invalid_request)?;
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let milestones = concord_db::milestones::list_milestones(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(crate::pagination::page_json(milestones, &page)?)
}

#[derive(Debug, Serialize, ToSchema)]
pub struct RoadmapTotals {
    pub total: i64,
    pub open: i64,
    pub shipped: i64,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct RoadmapResponse {
    pub milestones: Vec<concord_db::milestones::Milestone>,
    pub slippage: Vec<concord_db::milestones::MilestoneSlippage>,
    pub slug: String,
    pub totals: RoadmapTotals,
}

/// `GET /api/v1/projects/{slug}/roadmap` — the milestones, their totals, and how late each one is.
///
/// One object rather than three answers: the totals are the sum of the rows beside them, and a
/// client that had to add them up itself would be writing the rule down a second time.
async fn roadmap(
    State(state): State<AppState>,
    Path(slug): Path<String>,
) -> Result<Json<RoadmapResponse>, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let milestones = concord_db::milestones::list_milestones(&state.pool, project.id)
        .await
        .map_err(internal)?;
    let (mut total, mut open, mut shipped) = (0i64, 0i64, 0i64);
    for milestone in &milestones {
        total += milestone.total;
        open += milestone.open;
        shipped += milestone.shipped;
    }
    let slippage = concord_db::milestones::roadmap_slippage(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(RoadmapResponse {
        milestones,
        slippage,
        slug: project.slug,
        totals: RoadmapTotals { total, open, shipped },
    }))
}


// ── milestone writes (§4.4, §6) ─────────────────────────────────────────────────────────────────────────────────────

/// Map a milestone refusal onto its status: a missing row is 404 (another project's milestone is *not found*, never
/// forbidden — a distinct status is an existence oracle), a duplicate title is 409, an already-closed milestone is 409,
/// and every "not applicable" verdict is 400.
fn milestone_refusal(refusal: concord_db::milestones::Refused) -> ApiError {
    use concord_db::milestones::Refused;
    match refusal {
        Refused::NotFound => ApiError::not_found("no such milestone"),
        Refused::Invalid(reason) => ApiError::invalid_request(reason),
        Refused::Conflict(reason) => ApiError::conflict(reason),
        Refused::Duplicate(reason) => ApiError::conflict(reason),
    }
}

fn milestone_ids(body: &serde_json::Value) -> Vec<i64> {
    body.get("feature_ids")
        .and_then(|value| value.as_array())
        .map(|items| items.iter().filter_map(serde_json::Value::as_i64).collect())
        .unwrap_or_default()
}

fn milestone_fields(body: &serde_json::Value) -> (String, String, Option<f64>) {
    let title = body.get("title").and_then(|v| v.as_str()).unwrap_or_default().to_string();
    let text = body.get("body").and_then(|v| v.as_str()).unwrap_or_default().to_string();
    let target = body.get("target_date").and_then(serde_json::Value::as_f64);
    (title, text, target)
}

/// Resolve the milestone path halves and require **maintainer**: every write here rewrites what the project promises, so
/// the gate is the same one, in one place.
async fn milestone_write_context(
    state: &AppState,
    headers: &HeaderMap,
    segment: &str,
    user: &concord_db::auth::User,
) -> Result<concord_db::projects::Project, ApiError> {
    // The identity arrives from auth_gate as a request extension. The helper keeps only the project-visibility and
    // maintainer checks: two authorities answering "who is calling" would be the defect this step removes.
    let project = readable_project_segment(state, segment, headers).await?;
    require_role(state, &project, "maintainer", user).await?;
    Ok(project)
}

/// `POST /projects/{slug}/milestones` — create one, `planned`, dated or not.
async fn create_milestone(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
    let project = milestone_write_context(&state, &headers, &slug, &user).await?;
    let (title, text, target) = milestone_fields(&body);
    let milestone = concord_db::milestones::create(&state.pool, project.id, &title, &text, target)
        .await
        .map_err(internal)?
        .map_err(milestone_refusal)?;
    write_audit(&state, Some(project.id), user.id, "milestone.create", "milestone", milestone.id, &milestone.title)
        .await?;
    Ok((StatusCode::CREATED, Json(serde_json::json!(milestone))))
}

/// `PATCH /projects/{id}/milestones/{id}` — edit title, body and date. Status moves through the two routes below.
async fn update_milestone(
    State(state): State<AppState>,
    Path((slug, milestone_id)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = milestone_write_context(&state, &headers, &slug, &user).await?;
    let Ok(milestone_id) = milestone_id.parse::<i64>() else {
        return Err(ApiError::not_found("no such milestone"));
    };
    let (title, text, target) = milestone_fields(&body);
    let milestone = concord_db::milestones::update(&state.pool, project.id, milestone_id, &title, &text, target)
        .await
        .map_err(internal)?
        .map_err(milestone_refusal)?;
    Ok(Json(serde_json::json!(milestone)))
}

/// `PUT /projects/{id}/milestones/{id}/status` — `planned` ↔ `active`, with an act.
async fn set_milestone_status(
    State(state): State<AppState>,
    Path((slug, milestone_id)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = milestone_write_context(&state, &headers, &slug, &user).await?;
    let Ok(milestone_id) = milestone_id.parse::<i64>() else {
        return Err(ApiError::not_found("no such milestone"));
    };
    let status = body.get("status").and_then(|v| v.as_str()).unwrap_or_default();
    let milestone = concord_db::milestones::set_status(&state.pool, project.id, milestone_id, user.id, status)
        .await
        .map_err(internal)?
        .map_err(milestone_refusal)?;
    Ok(Json(serde_json::json!(milestone)))
}

/// `POST /projects/{id}/milestones/{id}/close` — the only path into `done`/`cancelled`, because it is the only one that
/// reconciles the features still open under it: a named successor moves them (one act each), a cancellation moves none,
/// and `done` with open features and no successor is refused with the count.
async fn close_milestone(
    State(state): State<AppState>,
    Path((slug, milestone_id)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = milestone_write_context(&state, &headers, &slug, &user).await?;
    let Ok(milestone_id) = milestone_id.parse::<i64>() else {
        return Err(ApiError::not_found("no such milestone"));
    };
    let successor = body.get("successor_id").and_then(serde_json::Value::as_i64);
    let cancel = body.get("cancel").and_then(serde_json::Value::as_bool).unwrap_or(false);
    let (milestone, moved) =
        concord_db::milestones::close(&state.pool, project.id, milestone_id, user.id, successor, cancel)
            .await
            .map_err(internal)?
            .map_err(milestone_refusal)?;
    Ok(Json(serde_json::json!({ "milestone": milestone, "moved": moved })))
}

/// `POST /projects/{id}/milestones/{id}/features` — a batch into the milestone, appended after the current tail.
async fn assign_milestone_features(
    State(state): State<AppState>,
    Path((slug, milestone_id)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = milestone_write_context(&state, &headers, &slug, &user).await?;
    let Ok(milestone_id) = milestone_id.parse::<i64>() else {
        return Err(ApiError::not_found("no such milestone"));
    };
    let ids = milestone_ids(&body);
    let assigned = concord_db::milestones::assign_features(&state.pool, project.id, milestone_id, &ids)
        .await
        .map_err(internal)?
        .map_err(milestone_refusal)?;
    write_audit(
        &state,
        Some(project.id),
        user.id,
        "feature.milestone",
        "milestone",
        milestone_id,
        &format!("assigned {assigned} feature(s)"),
    )
    .await?;
    Ok(Json(serde_json::json!({ "assigned": assigned })))
}

/// `PUT /projects/{id}/milestones/{id}/order` — an explicit order, one-based. **204 with no body**, as the reference
/// answers, because the order is the request: echoing it back would be a second copy that can disagree.
async fn reorder_milestone(
    State(state): State<AppState>,
    Path((slug, milestone_id)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<StatusCode, ApiError> {
    let project = milestone_write_context(&state, &headers, &slug, &user).await?;
    let Ok(milestone_id) = milestone_id.parse::<i64>() else {
        return Err(ApiError::not_found("no such milestone"));
    };
    let ids = milestone_ids(&body);
    concord_db::milestones::reorder(&state.pool, project.id, milestone_id, &ids)
        .await
        .map_err(internal)?
        .map_err(milestone_refusal)?;
    Ok(StatusCode::NO_CONTENT)
}



/// Milestone routes, typed on `AppState` (D-18). GET+PATCH share one
/// MethodRouter and stay in one module (D-20).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{project_id}/milestones/{milestone}",
            // `{project_id}` and `{milestone}`, not `{slug}`/`{id}`: axum refuses two routes that differ only in a
            // parameter's NAME, and these five writes share the path with this read.
            axum::routing::get(get_milestone).patch(update_milestone),
        )
        .route(
            "/api/v1/projects/{project_id}/milestones/{milestone}/status",
            axum::routing::put(set_milestone_status),
        )
        .route(
            "/api/v1/projects/{project_id}/milestones/{milestone}/close",
            axum::routing::post(close_milestone),
        )
        .route(
            "/api/v1/projects/{project_id}/milestones/{milestone}/features",
            axum::routing::post(assign_milestone_features),
        )
        .route(
            "/api/v1/projects/{project_id}/milestones/{milestone}/order",
            axum::routing::put(reorder_milestone),
        )
        .route(
            "/api/v1/projects/{slug}/milestones",
            axum::routing::get(list_milestones).post(create_milestone),
        )
        .route("/api/v1/projects/{slug}/roadmap", axum::routing::get(roadmap))
}
