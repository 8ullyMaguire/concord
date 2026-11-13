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
