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
