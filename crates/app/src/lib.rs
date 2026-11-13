//! S1's routes: projects, documents, the board, `/healthz`, `/api/v1/version`.
//!
//! The plan sketches a local `ApiError` enum here; the estate's `polaris_core::ApiError` is that
//! enum, already carrying the contract's status mapping and its refusal to serialize a code nobody
//! declared. Writing a second one would be the fifth copy the estate spent a review deleting, so
//! this module uses the crate's and the handlers only ever pick a constructor.
//!
//! A path under `/api` that is not a route answers the envelope with a 404, never a shell: the
//! service has no shell yet, and the rule outlives the frontend.

use axum::{
    extract::{Path, Query, State},
    http::HeaderMap,
    routing::{get, post, put},
    Json, Router,
};
use serde::Deserialize;
use polaris_core::ApiError;
use serde_json::{json, Value};
use sqlx::PgPool;

/// What every handler needs. One field today; the pool is what makes a handler testable without a
/// service, which is how the slice's tests will call them.
#[derive(Clone)]
pub struct AppState {
    pub pool: PgPool,
}

/// The router, complete for this slice.
pub fn router(state: AppState) -> Router {
    Router::new()
        .route("/healthz", get(healthz))
        .route("/api/v1/healthz", get(healthz))
        .route("/api/v1/version", get(version))
        .route("/api/v1/projects", get(list_projects))
        .route("/api/v1/projects/{slug}", get(get_project))
        .route("/api/v1/projects/{slug}/board", get(get_board))
        .route("/api/v1/auth/register", post(register))
        .route("/api/v1/projects", post(create_project))
        .route(
            "/api/v1/projects/{slug}/consensus",
            get(list_consensus),
        )
        .route(
            "/api/v1/projects/{slug}/consensus/{call}",
            get(get_consensus),
        )
        .route(
            "/api/v1/projects/{slug}/consensus",
            post(open_consensus),
        )
        .route(
            "/api/v1/projects/{slug}/consensus/{call}/position",
            post(cast_consensus_position),
        )
        .route(
            "/api/v1/projects/{slug}/consensus/{call}/objection",
            post(create_consensus_objection),
        )
        .route(
            "/api/v1/projects/{slug}/consensus/{call}/close",
            post(close_consensus),
        )
        .route(
            "/api/v1/projects/{slug}/consensus/{call}/override",
            get(get_override_window).post(open_override_window),
        )
        .route(
            "/api/v1/projects/{slug}/consensus/{call}/override/position",
            post(cast_override_position),
        )
        .route(
            "/api/v1/projects/{slug}/consensus/{call}/override/close",
            post(close_override_window),
        )
        .route(
            "/api/v1/projects/{slug}/consensus/objections/{objection}/resolve",
            put(resolve_consensus_objection),
        )
        .route("/api/v1/projects/{slug}/audit", get(list_audit))
        .route(
            "/api/v1/projects/{slug}/audit/actions",
            get(list_audit_actions),
        )
        .route(
            "/api/v1/projects/{slug}/priorities",
            get(list_priorities),
        )
        .route(
            "/api/v1/projects/{slug}/milestones",
            get(list_milestones),
        )
        .route("/api/v1/projects/{slug}/roadmap", get(roadmap))
        .route(
            "/api/v1/projects/{slug}/complaints",
            get(list_complaints),
        )
        .route(
            "/api/v1/projects/{slug}/features",
            get(list_features).post(file_feature),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}",
            get(get_feature),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/status",
            put(set_feature_status),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/complaints",
            post(link_complaint),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/complaints/{complaint}",
            axum::routing::delete(unlink_complaint),
        )
        .route(
            "/api/v1/projects/{slug}/board/cards/{card_id}/move",
            put(move_card),
        )
        .route("/api/v1/projects/{slug}/documents", get(list_documents))
        .route(
            "/api/v1/projects/{slug}/documents/{document}",
            get(get_document),
        )
        .fallback(not_found)
        .with_state(state)
}

/// `/healthz` answers `{"status":"ok"}` — the shape the deployed service answers, not a phrase.
async fn healthz() -> Json<Value> {
    Json(json!({"status": "ok"}))
}

/// The build string. During the port it names what answers, so a probe can tell the Rust service
/// from the Go one without reading a process list.
async fn version() -> Json<Value> {
    Json(json!({"version": concat!("concord-rs ", env!("CARGO_PKG_VERSION"))}))
}

async fn list_projects(State(state): State<AppState>) -> Result<Json<Value>, ApiError> {
    let projects = concord_db::projects::list_projects(&state.pool)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(projects).map_err(internal)?))
}

async fn get_project(
    State(state): State<AppState>,
    Path(slug): Path<String>,
) -> Result<Json<Value>, ApiError> {
    match concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
    {
        Some(project) => Ok(Json(serde_json::to_value(project).map_err(internal)?)),
        None => Err(ApiError::not_found(format!("project {slug}"))),
    }
}

async fn get_board(
    State(state): State<AppState>,
    Path(slug): Path<String>,
) -> Result<Json<Value>, ApiError> {
    match concord_db::projects::board_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
    {
        Some(board) => Ok(Json(serde_json::to_value(board).map_err(internal)?)),
        None => Err(ApiError::not_found(format!("project {slug}"))),
    }
}

async fn list_documents(
    State(state): State<AppState>,
    Path(slug): Path<String>,
) -> Result<Json<Value>, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let documents = concord_db::projects::list_documents(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(documents).map_err(internal)?))
}

/// `GET /api/v1/projects/{slug}/documents/{doc_id}` — one document, body and all.
///
/// By integer id, because that is the Go route's key. A non-integer is a 400 with the Go handler's
/// own message rather than a 404: the path is well-formed and the id is not.
async fn get_document(
    State(state): State<AppState>,
    Path((slug, document)): Path<(String, String)>,
) -> Result<Json<Value>, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let Ok(id) = document.parse::<i64>() else {
        return Err(ApiError::invalid_request("document id must be an integer"));
    };
    match concord_db::projects::document(&state.pool, project.id, id)
        .await
        .map_err(internal)?
    {
        Some(doc) => Ok(Json(serde_json::to_value(doc).map_err(internal)?)),
        None => Err(ApiError::not_found(format!("document {id}"))),
    }
}

/// Any other path: the envelope, with a 404. `/api/**` is never a shell and neither is anything else
/// here yet — an SPA fallback belongs with the frontend slice, not with the API.
async fn not_found() -> ApiError {
    ApiError::not_found("no such route")
}

/// One place where a database failure becomes a refusal. The cause is logged and not answered: an
/// operator gets the trace, a caller gets "internal".
fn internal(error: impl std::fmt::Display) -> ApiError {
    tracing::error!(error = %error, "internal");
    ApiError::internal("internal error")
}


/// The token note the Go handler returns with a fresh token, verbatim: it tells the caller the one
/// thing about the token that matters, and a paraphrase would be a different sentence in the same
/// field.
const TOKEN_NOTE: &str = "Store this token. It cannot be retrieved again; only its hash is kept.";

#[derive(Debug, Deserialize)]
struct RegisterRequest {
    username: String,
    #[serde(default)]
    display_name: String,
    password: String,
}

/// `POST /api/v1/auth/register` — an account and its first token.
async fn register(
    State(state): State<AppState>,
    Json(body): Json<RegisterRequest>,
) -> Result<(axum::http::StatusCode, Json<Value>), ApiError> {
    use concord_db::auth::AuthError;
    let display_name = if body.display_name.trim().is_empty() {
        body.username.trim().to_string()
    } else {
        body.display_name.trim().to_string()
    };
    match concord_db::auth::register_user(&state.pool, &body.username, &display_name, &body.password)
        .await
    {
        Ok((user, token)) => Ok((
            axum::http::StatusCode::CREATED,
            Json(json!({
                "user": serde_json::to_value(user).map_err(internal)?,
                "token": token,
                "token_note": TOKEN_NOTE,
            })),
        )),
        Err(AuthError::Invalid(message)) => Err(ApiError::invalid_request(message)),
        Err(AuthError::Duplicate(message)) => Err(ApiError::conflict(format!("{message} exists"))),
        Err(AuthError::Database(error)) => Err(internal(error)),
    }
}

#[derive(Debug, Deserialize)]
struct MoveQuery {
    to: Option<String>,
    feature_id: Option<i64>,
}

/// `PUT /api/v1/projects/{slug}/board/cards/{card_id}/move?to=phase[&feature_id=N]`.
///
/// A card is named either by its row id or by the feature it represents: a derived card has no row
/// and no id, and placing it is what creates one.
async fn move_card(
    State(state): State<AppState>,
    Path((slug, card_id)): Path<(String, i64)>,
    Query(query): Query<MoveQuery>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    use concord_db::projects::MoveError;
    if bearer_user(&state, &headers).await?.is_none() {
        return Err(ApiError::unauthenticated("authentication required"));
    }
    let to = query
        .to
        .ok_or_else(|| ApiError::invalid_request("to is required"))?;
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    match concord_db::projects::move_card(
        &state.pool,
        project.id,
        card_id,
        query.feature_id.unwrap_or(0),
        &to,
    )
    .await
    {
        Ok(()) => Ok(Json(json!({"status": "moved"}))),
        Err(MoveError::Invalid(message)) => Err(ApiError::invalid_request(message)),
        Err(MoveError::NoSuchColumn(message)) => Err(ApiError::not_found(message)),
        Err(MoveError::NotFound(message)) => Err(ApiError::not_found(message)),
        Err(MoveError::ColumnFull(message)) => Err(ApiError::conflict(message)),
        Err(MoveError::Database(error)) => Err(internal(error)),
    }
}


#[derive(Debug, Deserialize)]
struct CreateProjectRequest {
    slug: String,
    name: String,
    #[serde(default)]
    description: String,
    #[serde(default)]
    governance_model: String,
    #[serde(default)]
    license: String,
}

/// `POST /api/v1/projects` — a namespace with a slug, a charter and a board.
///
/// Needs an account, not merely a token: a nameless party holding namespaces is a squatting vector,
/// which is why the Go handler refuses a guest identity here and nowhere else in the read path.
async fn create_project(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(body): Json<CreateProjectRequest>,
) -> Result<(axum::http::StatusCode, Json<Value>), ApiError> {
    use concord_db::projects::CreateError;
    let user = match bearer_user(&state, &headers).await? {
        Some(user) => user,
        None => return Err(ApiError::unauthenticated("authentication required")),
    };
    let model = if body.governance_model.is_empty() {
        "collective"
    } else {
        body.governance_model.as_str()
    };
    match concord_db::projects::create_project(
        &state.pool,
        body.slug.trim(),
        body.name.trim(),
        &body.description,
        model,
        &body.license,
        user.id,
    )
    .await
    {
        Ok(project) => Ok((
            axum::http::StatusCode::CREATED,
            Json(serde_json::to_value(project).map_err(internal)?),
        )),
        Err(CreateError::Invalid(message)) => Err(ApiError::invalid_request(message)),
        Err(CreateError::Duplicate(slug)) => Err(ApiError::conflict(format!("project {slug:?} exists"))),
        Err(CreateError::Database(error)) => Err(internal(error)),
    }
}

/// The user behind `Authorization: Bearer …`, or `None` when there is no usable token.
///
/// One helper rather than a middleware: the port has routes that treat an absent identity as an
/// error and routes that treat it as an anonymous read, and a layer that decided for both would be
/// wrong for one of them.
async fn bearer_user(
    state: &AppState,
    headers: &HeaderMap,
) -> Result<Option<concord_db::auth::User>, ApiError> {
    let Some(token) = headers
        .get(axum::http::header::AUTHORIZATION)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.strip_prefix("Bearer "))
    else {
        return Ok(None);
    };
    concord_db::auth::user_for_token(&state.pool, token)
        .await
        .map_err(|error| match error {
            concord_db::auth::AuthError::Database(e) => internal(e),
            other => internal(format!("{other:?}")),
        })
}


#[derive(Debug, Deserialize)]
struct FeatureQuery {
    status: Option<String>,
}

/// `GET /api/v1/projects/{slug}/features[?status=]` — the project's features, newest first.
async fn list_features(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Query(query): Query<FeatureQuery>,
) -> Result<Json<Value>, ApiError> {
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
    Ok(Json(serde_json::to_value(features).map_err(internal)?))
}

/// `GET /api/v1/projects/{slug}/features/{id}` — one feature, but only under its own project.
///
/// Two refusals, both load-bearing and both in the Go handler: the id must parse (a non-integer is a
/// 400, not a 404), and the feature's `project_id` must be this project's. Without the second, a
/// caller reads any project's work by counting ids while the list endpoint answers 404.
async fn get_feature(
    State(state): State<AppState>,
    Path((slug, feature_id)): Path<(String, String)>,
) -> Result<Json<Value>, ApiError> {
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
        Some(feature) => Ok(Json(serde_json::to_value(feature).map_err(internal)?)),
        None => Err(ApiError::not_found(format!("feature {id}"))),
    }
}


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


/// `GET /api/v1/projects/{slug}/milestones` — the milestones, with counts computed on read.
async fn list_milestones(
    State(state): State<AppState>,
    Path(slug): Path<String>,
) -> Result<Json<Value>, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let milestones = concord_db::milestones::list_milestones(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(milestones).map_err(internal)?))
}

/// `GET /api/v1/projects/{slug}/roadmap` — the milestones, their totals, and how late each one is.
///
/// One object rather than three answers: the totals are the sum of the rows beside them, and a
/// client that had to add them up itself would be writing the rule down a second time.
async fn roadmap(
    State(state): State<AppState>,
    Path(slug): Path<String>,
) -> Result<Json<Value>, ApiError> {
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
    Ok(Json(json!({
        "slug": project.slug,
        "milestones": serde_json::to_value(milestones).map_err(internal)?,
        "totals": {"total": total, "open": open, "shipped": shipped},
        "slippage": serde_json::to_value(slippage).map_err(internal)?,
    })))
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

fn write_error(error: concord_db::feature_writes::WriteError) -> ApiError {
    use concord_db::feature_writes::WriteError;
    match error {
        WriteError::Invalid(message) => ApiError::invalid_request(message),
        WriteError::NotFound(message) => ApiError::not_found(message),
        // 409, not 400: the client's request is well-formed and the thing they want already exists.
        WriteError::Conflict(message) => ApiError::conflict(message),
        WriteError::Database(error) => internal(error),
    }
}

/// `POST /api/v1/projects/{slug}/features` — file a feature.
///
/// Needs an account: an idea is a claim about work, and a nameless one cannot be attributed or
/// rate-limited. `complaint_ids` is accepted beside `linked_complaints` because both spellings are in
/// the Go struct's history and a caller that used either must not be silently ignored.
async fn file_feature(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    headers: HeaderMap,
    Json(body): Json<FileFeatureRequest>,
) -> Result<(axum::http::StatusCode, Json<Value>), ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
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
    headers: HeaderMap,
    Json(body): Json<SetStatusRequest>,
) -> Result<Json<Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
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
    concord_db::feature_writes::update_feature_status(&state.pool, id, &body.status)
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
struct LinkComplaintRequest {
    complaint_id: i64,
}

/// `POST /api/v1/projects/{slug}/features/{id}/complaints` — attach a validated complaint.
///
/// Answers 200 with the feature's complaints rather than 201 with the link: the section renders its
/// own count and must not re-read to learn what it just caused.
async fn link_complaint(
    State(state): State<AppState>,
    Path((slug, feature)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<LinkComplaintRequest>,
) -> Result<Json<Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let Ok(id) = feature.parse::<i64>() else {
        return Err(ApiError::invalid_request("feature id must be an integer"));
    };
    let _ = concord_db::features::feature(&state.pool, id)
        .await
        .map_err(internal)?
        .filter(|row| row.project_id == project.id)
        .ok_or_else(|| ApiError::not_found(format!("feature {id}")))?;
    concord_db::feature_writes::link_complaint(&state.pool, id, body.complaint_id)
        .await
        .map_err(write_error)?;
    let _ = concord_db::feature_writes::add_audit(
        &state.pool,
        project.id,
        user.id,
        "link_complaint",
        "feature",
        id,
        &format!("complaint {}", body.complaint_id),
    )
    .await;
    let complaints = concord_db::feature_writes::feature_complaints(&state.pool, id)
        .await
        .map_err(write_error)?;
    Ok(Json(json!({
        "feature_id": id,
        "complaints": serde_json::to_value(complaints).map_err(internal)?,
    })))
}

/// `DELETE /api/v1/projects/{slug}/features/{id}/complaints/{complaint_id}` — the inverse.
///
/// The complaint id is a path segment and not a body field, because a DELETE with a body is not
/// portable.
async fn unlink_complaint(
    State(state): State<AppState>,
    Path((slug, feature, complaint)): Path<(String, String, String)>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let Ok(id) = feature.parse::<i64>() else {
        return Err(ApiError::invalid_request("feature id must be an integer"));
    };
    let Ok(complaint_id) = complaint.parse::<i64>() else {
        return Err(ApiError::invalid_request("complaint id must be an integer"));
    };
    let _ = concord_db::features::feature(&state.pool, id)
        .await
        .map_err(internal)?
        .filter(|row| row.project_id == project.id)
        .ok_or_else(|| ApiError::not_found(format!("feature {id}")))?;
    concord_db::feature_writes::unlink_complaint(&state.pool, id, complaint_id)
        .await
        .map_err(write_error)?;
    let _ = concord_db::feature_writes::add_audit(
        &state.pool,
        project.id,
        user.id,
        "unlink_complaint",
        "feature",
        id,
        &format!("complaint {complaint_id}"),
    )
    .await;
    let complaints = concord_db::feature_writes::feature_complaints(&state.pool, id)
        .await
        .map_err(write_error)?;
    Ok(Json(json!({
        "feature_id": id,
        "complaints": serde_json::to_value(complaints).map_err(internal)?,
    })))
}

/// `GET /api/v1/projects/{slug}/priorities` — the §6.2 ranking, ordered by its own score.
///
/// Deliberately public. This is the output of a process the public is invited to take part in;
/// requiring a token to read the result while anyone may read the proposals would make the site a
/// black box. The private act is casting a vote, not reading the ranking it produces.
async fn list_priorities(
    State(state): State<AppState>,
    Path(slug): Path<String>,
) -> Result<Json<Value>, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let priorities = concord_db::priorities::feature_priorities(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(priorities).map_err(internal)?))
}

#[derive(Debug, Deserialize, Default)]
struct AuditQuery {
    #[serde(default)]
    action: String,
    #[serde(default)]
    kind: String,
    #[serde(default)]
    q: String,
    #[serde(default)]
    actor_id: Option<String>,
    #[serde(default)]
    entity_type: String,
    #[serde(default)]
    entity_id: Option<String>,
    #[serde(default)]
    since: Option<String>,
    #[serde(default)]
    offset: Option<String>,
    #[serde(default)]
    limit: Option<String>,
}

/// `GET /api/v1/projects/{slug}/audit` — one page of the log, newest first.
///
/// Every numeric parameter is parsed and its error **ignored**, never fatal: a malformed query string
/// narrows the log to nothing the caller asked for rather than failing the request. An audit viewer is
/// read by hand and typed into, and a 400 on a typo is worse than a wrong page.
///
/// `kind` is the exception: an unknown kind is a 400, because an ignored one returns the whole log and
/// a whole log renders exactly like a filter that matched everything — a typo would be reported as
/// "moderation has done nothing" instead of "there is no such kind".
async fn list_audit(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Query(query): Query<AuditQuery>,
) -> Result<Json<Value>, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let actions = concord_db::audit::kind_actions(&query.kind).ok_or_else(|| {
        ApiError::invalid_request(format!(
            "{:?} is not a kind of audit entry; the kinds are: moderation",
            query.kind
        ))
    })?;
    let filter = concord_db::audit::Filter {
        project_id: project.id,
        action: query.action,
        actions,
        actor_id: query.actor_id.and_then(|value| value.parse().ok()).unwrap_or(0),
        entity_type: query.entity_type,
        entity_id: query.entity_id.and_then(|value| value.parse().ok()).unwrap_or(0),
        text: query.q,
        since: query.since.and_then(|value| value.parse().ok()).unwrap_or(0.0),
        offset: query.offset.and_then(|value| value.parse().ok()).unwrap_or(0),
        limit: query.limit.and_then(|value| value.parse().ok()).unwrap_or(0),
    };
    let page = concord_db::audit::list(&state.pool, &filter)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(page).map_err(internal)?))
}

/// `GET /api/v1/projects/{slug}/audit/actions` — the filter vocabulary, alphabetical.
async fn list_audit_actions(
    State(state): State<AppState>,
    Path(slug): Path<String>,
) -> Result<Json<Value>, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let actions = concord_db::audit::actions(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(actions).map_err(internal)?))
}

/// `GET /api/v1/projects/{slug}/consensus` — the calls index, newest first.
///
/// The project page's Consensus tab links here with no call named; without this route the page fetched
/// an empty call id and answered 405 to every reader.
async fn list_consensus(
    State(state): State<AppState>,
    Path(slug): Path<String>,
) -> Result<Json<Value>, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let calls = concord_db::consensus::list_calls(&state.pool, project.id, 0)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(calls).map_err(internal)?))
}

/// `GET /api/v1/projects/{slug}/consensus/{call_id}` — one call, with §6.6's hidden tally.
///
/// While the call is open the counts are blanked and only the caller's own position is returned: §6.6
/// hides the tally to prevent bandwagoning, and the rule is enforced here rather than in a client,
/// because a client-side rule is bypassed by devtools.
async fn get_consensus(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let Ok(call_id) = call.parse::<i64>() else {
        return Err(ApiError::invalid_request("call id must be an integer"));
    };
    // Resolved against THIS project: a call id alone would let one project's URL read another's call.
    let found = concord_db::consensus::call(&state.pool, call_id)
        .await
        .map_err(internal)?
        .filter(|row| row.project_id == project.id)
        .ok_or_else(|| ApiError::not_found(format!("consensus call {call_id}")))?;
    let actor = bearer_user(&state, &headers).await?.map(|user| user.id).unwrap_or(0);
    let positions = concord_db::consensus::positions(&state.pool, call_id)
        .await
        .map_err(internal)?;
    let objections = concord_db::consensus::objections(&state.pool, call_id)
        .await
        .map_err(internal)?;
    let counts = concord_db::consensus::tally(&state.pool, project.id, call_id)
        .await
        .map_err(internal)?;
    let model = concord_db::projects::governance_model(&state.pool, project.id)
        .await
        .map_err(internal)?;
    let charter = match concord_governance::GovernanceModel::parse(&model) {
        Some(model) => concord_governance::Charter::default_for(model),
        // An unknown model falls back to collective, exactly as the Go handler does when the stored
        // value is not valid — a project with a typo in its model is not a project with no rules.
        None => concord_governance::Charter::default_for(concord_governance::GovernanceModel::Collective),
    };
    let (tally, visible, shown_positions) = if found.status == "closed" {
        (
            concord_db::consensus::thresholds_summary(&counts, &charter),
            true,
            positions,
        )
    } else {
        (
            concord_db::consensus::hidden_tally(concord_db::consensus::thresholds_summary(
                &counts, &charter,
            )),
            false,
            concord_db::consensus::own_positions_only(positions, actor),
        )
    };
    Ok(Json(json!({
        "call": serde_json::to_value(found).map_err(internal)?,
        "objections": serde_json::to_value(objections).map_err(internal)?,
        "tally": tally,
        "tally_visible": visible,
        "positions": serde_json::to_value(shown_positions).map_err(internal)?,
    })))
}

/// Resolve a project by slug, or the 404 envelope.
async fn project_for(
    state: &AppState,
    slug: &str,
) -> Result<concord_db::projects::Project, ApiError> {
    concord_db::projects::project_by_slug(&state.pool, slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))
}

/// Parse a path id, naming the segment that was wrong.
fn parse_id(raw: &str, what: &str) -> Result<i64, ApiError> {
    raw.parse::<i64>()
        .map_err(|_| ApiError::invalid_request(format!("{what} must be an integer")))
}

/// The roles that may open or close a call: §6.8 puts "call and close consensus" in the maintainer's
/// column, and calling one and closing one are the same power, so they carry the same gate.
///
/// A gate that only checked "is a member" let any account with a session decide any project's call.
async fn require_maintainer(
    pool: &sqlx::PgPool,
    project_id: i64,
    user_id: i64,
) -> Result<(), ApiError> {
    let role = concord_db::projects::member_role(pool, project_id, user_id)
        .await
        .map_err(internal)?
        .unwrap_or_default();
    if !matches!(role.as_str(), "owner" | "maintainer") {
        return Err(ApiError::forbidden(
            "calling and closing consensus is a maintainer's act",
        ));
    }
    Ok(())
}

#[derive(Debug, Deserialize)]
struct OpenConsensusRequest {
    #[serde(default)]
    feature_id: i64,
    #[serde(default)]
    title: String,
    #[serde(default)]
    description: String,
}

/// `POST /api/v1/projects/{slug}/consensus` — open a call.
///
/// `opened_by` is the authenticated caller. It used to be hardcoded to 1, so every call in the database
/// claimed user 1 opened it.
async fn open_consensus(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    headers: HeaderMap,
    Json(body): Json<OpenConsensusRequest>,
) -> Result<(axum::http::StatusCode, Json<Value>), ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    require_maintainer(&state.pool, project.id, user.id).await?;
    let call = concord_db::consensus_writes::create_call(
        &state.pool,
        project.id,
        body.feature_id,
        user.id,
        &body.title,
        &body.description,
    )
    .await
    .map_err(write_error)?;
    Ok((
        axum::http::StatusCode::CREATED,
        Json(serde_json::to_value(call).map_err(internal)?),
    ))
}

#[derive(Debug, Deserialize)]
struct PositionRequest {
    position: String,
    #[serde(default)]
    #[allow(dead_code)]
    reason: String,
}

/// `POST /api/v1/projects/{slug}/consensus/{call_id}/position` — cast or replace a stance.
async fn cast_consensus_position(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<PositionRequest>,
) -> Result<Json<Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = project_for(&state, &slug).await?;
    let call_id = parse_id(&call, "call id")?;
    concord_db::consensus_writes::cast_position(&state.pool, project.id, call_id, user.id, &body.position)
        .await
        .map_err(write_error)?;
    let _ = concord_db::feature_writes::add_audit(
        &state.pool,
        project.id,
        user.id,
        "cast_position",
        "consensus_call",
        call_id,
        &body.position,
    )
    .await;
    Ok(Json(json!({"status": "ok", "position": body.position})))
}

#[derive(Debug, Deserialize)]
struct ObjectionRequest {
    principle: String,
    violation: String,
    #[serde(default)]
    remedy: String,
}

/// `POST /api/v1/projects/{slug}/consensus/{call_id}/objection` — §6.6's block, in writing.
async fn create_consensus_objection(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<ObjectionRequest>,
) -> Result<(axum::http::StatusCode, Json<Value>), ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = project_for(&state, &slug).await?;
    let call_id = parse_id(&call, "call id")?;
    let id = concord_db::consensus_writes::create_objection(
        &state.pool,
        project.id,
        call_id,
        user.id,
        &body.principle,
        &body.violation,
        &body.remedy,
    )
    .await
    .map_err(write_error)?;
    Ok((
        axum::http::StatusCode::CREATED,
        Json(json!({"id": id, "status": "open"})),
    ))
}

/// `POST /api/v1/projects/{slug}/consensus/{call_id}/close` — fix the verdict.
///
/// No body: since the eighth pass a close never overrides (the override is the second window, with its
/// own route), so there is no note for it to carry.
async fn close_consensus(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = project_for(&state, &slug).await?;
    require_maintainer(&state.pool, project.id, user.id).await?;
    let call_id = parse_id(&call, "call id")?;
    let summary = concord_db::consensus_writes::close_call(&state.pool, project.id, call_id)
        .await
        .map_err(write_error)?;
    let verdict = summary
        .get("result")
        .and_then(Value::as_str)
        .unwrap_or("")
        .to_string();
    let _ = concord_db::feature_writes::add_audit(
        &state.pool,
        project.id,
        user.id,
        "close_consensus",
        "consensus_call",
        call_id,
        &verdict,
    )
    .await;
    Ok(Json(summary))
}

#[derive(Debug, Deserialize)]
struct ResolveObjectionRequest {
    status: String,
    resolution: String,
}

/// `PUT /api/v1/projects/{slug}/consensus/objections/{id}/resolve` — the objection's written answer.
async fn resolve_consensus_objection(
    State(state): State<AppState>,
    Path((slug, objection)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<ResolveObjectionRequest>,
) -> Result<Json<Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = project_for(&state, &slug).await?;
    require_maintainer(&state.pool, project.id, user.id).await?;
    let objection_id = parse_id(&objection, "objection id")?;
    concord_db::consensus_writes::resolve_objection(
        &state.pool,
        objection_id,
        &body.status,
        &body.resolution,
    )
    .await
    .map_err(write_error)?;
    Ok(Json(json!({"id": objection_id, "status": body.status})))
}

/// `GET /api/v1/projects/{slug}/consensus/{call_id}/override` — the window, with §6.6's tally rule.
///
/// An open window's counts are null and the caller sees only their own stance; a closed window is the
/// record. The stripping lives in the reader (`override_window::fill_tally`) so no handler can publish a
/// live tally by forgetting to.
async fn get_override_window(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    let call_id = parse_id(&call, "call id")?;
    let viewer = bearer_user(&state, &headers)
        .await?
        .map(|user| user.id)
        .unwrap_or(0);
    let window = concord_db::override_window::get(&state.pool, call_id, viewer)
        .await
        .map_err(write_error)?;
    // The call is checked after the read on purpose: a window belonging to another project must 404, and
    // resolving the call first would answer 200 for a call id that is not in this project.
    let owner = concord_db::consensus::call(&state.pool, call_id)
        .await
        .map_err(internal)?
        .filter(|found| found.project_id == project.id)
        .ok_or_else(|| ApiError::not_found(format!("consensus call {call_id}")))?;
    let _ = owner;
    Ok(Json(serde_json::to_value(window).map_err(internal)?))
}

/// `POST /api/v1/projects/{slug}/consensus/{call_id}/override` — open §6.6's second window.
async fn open_override_window(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<(axum::http::StatusCode, Json<Value>), ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = project_for(&state, &slug).await?;
    require_maintainer(&state.pool, project.id, user.id).await?;
    let call_id = parse_id(&call, "call id")?;
    let window = concord_db::override_window::open(&state.pool, project.id, call_id, user.id)
        .await
        .map_err(write_error)?;
    Ok((
        axum::http::StatusCode::CREATED,
        Json(serde_json::to_value(window).map_err(internal)?),
    ))
}

#[derive(Debug, Deserialize)]
struct OverridePositionRequest {
    position: String,
    #[serde(default)]
    reason: String,
}

/// `POST /api/v1/projects/{slug}/consensus/{call_id}/override/position` — a stance in the second window.
async fn cast_override_position(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<OverridePositionRequest>,
) -> Result<Json<Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let _project = project_for(&state, &slug).await?;
    let call_id = parse_id(&call, "call id")?;
    concord_db::override_window::cast_position(
        &state.pool,
        call_id,
        user.id,
        &body.position,
        &body.reason,
    )
    .await
    .map_err(write_error)?;
    Ok(Json(json!({"status": "ok", "position": body.position})))
}

#[derive(Debug, Deserialize)]
struct OverrideCloseRequest {
    #[serde(default)]
    note: String,
}

/// `POST /api/v1/projects/{slug}/consensus/{call_id}/override/close` — fix the window's verdict.
///
/// The note is required exactly when the verdict is an override — §6.6's "the objector's remedy answered
/// in writing" — so it is accepted and validated in the store, not demanded by the route.
async fn close_override_window(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
    headers: HeaderMap,
    body: Option<Json<OverrideCloseRequest>>,
) -> Result<Json<Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = project_for(&state, &slug).await?;
    require_maintainer(&state.pool, project.id, user.id).await?;
    let call_id = parse_id(&call, "call id")?;
    let note = body.map(|Json(body)| body.note).unwrap_or_default();
    let window = concord_db::override_window::close(&state.pool, project.id, call_id, &note)
        .await
        .map_err(write_error)?;
    Ok(Json(serde_json::to_value(window).map_err(internal)?))
}
