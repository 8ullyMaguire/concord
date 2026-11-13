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
    http::{HeaderMap, StatusCode},
    routing::{get, post, put},
    Json, Router,
};
use serde::Deserialize;
pub mod feeds;

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
        // Every page needs to know who it is looking at, and the Go's `/auth/me` is what tells it.
        .route("/api/v1/auth/me", get(auth_me))
        .route("/api/v1/projects/{slug}/tags", get(project_tags))
        .route("/api/v1/projects/{project_id}/members", get(list_members))
        // The project page's panels: the Go nests these under `r.Route("/criteria", …)` and registers GET "/",
        // so both the bare path and the trailing-slash form answer — a client that appends a slash must not get
        // a 404.
        .route("/api/v1/projects/{project_id}/criteria", get(list_criteria))
        .route("/api/v1/projects/{project_id}/criteria/", get(list_criteria))
        .route("/api/v1/projects/{project_id}/criteria-profiles", get(list_criteria_profiles))
        .route("/api/v1/projects/{project_id}/criteria-profiles/", get(list_criteria_profiles))
        .route("/api/v1/projects/{slug}/languages", get(project_languages))
        // Top-level `{project_id}`, NOT nested under the `{slug}` subtree: nested, axum binds the segment by its
        // own name and a handler reading a numeric id would get the slug — the Go comment records the same bug
        // chi had ("requireProjectID reads project_id, gets "", and every call 404s with project \"\"").
        .route("/api/v1/projects/{project_id}/capabilities", get(list_capabilities))
        .route("/api/v1/projects/{project_id}/field-reports", get(list_field_reports))
        .route("/api/v1/projects/{project_id}/complaints/{complaint}", get(get_complaint))
        .route("/api/v1/projects/{project_id}/complaints/{complaint}/features", get(complaint_features))
        .route("/api/v1/projects/{project_id}/milestones/{milestone}", get(get_milestone))
        .route("/api/v1/projects/{project_id}/lists/{list}", get(get_list))
        .route("/api/v1/projects/{project_id}/lists/{list}/", get(get_list))
        .route("/api/v1/projects/{project_id}/lists/{list}/entries", get(get_list_entries))
        .route("/api/v1/projects/{project_id}/tallies", get(feature_tallies))
        .route("/api/v1/projects/{slug}/documents/kinds", get(document_kinds))
        .route("/api/v1/projects/{slug}/documents/search", get(search_documents))
        .route("/api/v1/projects/{project_id}/rank/composite", post(composite_rank))
        .route(
            "/api/v1/projects/{project_id}/arenas/{arena_id}/votes",
            get(arena_votes),
        )
        .route(
            "/api/v1/projects/{project_id}/arenas/{arena_id}/next-pair",
            get(arena_next_pair),
        )
        .route(
            "/api/v1/projects/{project_id}/arenas/{arena_id}/verification",
            get(arena_verification),
        )
        .route(
            "/api/v1/projects/{project_id}/solutions",
            get(project_solutions),
        )
        .route(
            "/api/v1/projects/{project_id}/requests",
            get(list_project_requests),
        )
        .route("/api/v1/requests", get(list_instance_requests))
        .route("/api/v1/requests/{request_id}", get(one_request))
        .route(
            "/api/v1/requests/{request_id}/accept",
            put(accept_answer).delete(unaccept_answer),
        )
        .route(
            "/api/v1/requests/{request_id}/answers",
            get(request_answers),
        )
        .route("/api/v1/requests/{request_id}/tags", get(request_tags))
        .route(
            "/api/v1/projects/{project_id}/features/{id}/solutions",
            get(feature_solutions),
        )
        .route(
            "/api/v1/projects/{project_id}/features/{id}/solutions/{solution_id}",
            get(one_solution),
        )
        .route("/api/v1/projects/{slug}/features/{feature}/deps", get(feature_deps))
        .route("/api/v1/projects/{slug}/features/{feature}/evidence", get(feature_evidence))
        // `{slug}` and not `{project_id}`: axum refuses two routes that differ only in a parameter NAME ("Insertion
        // failed due to conflict with previously registered route"), and the sibling feature routes under this
        // prefix already bind `{slug}`. The Go's shape is the same path — parameter names are not part of it.
        .route("/api/v1/projects/{slug}/features/{feature}/complaints", get(feature_complaints))
        .route("/api/v1/projects/{slug}/features/{feature}/tags", get(feature_tags))
        .route("/api/v1/projects/{project_id}/lists", get(project_lists))
        .route("/api/v1/projects/{project_id}/lists/", get(project_lists))
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
            "/api/v1/projects/{project_id}/charter",
            get(get_charter),
        )
        // §12: the thirteen `.xml` documents. `.xml` and `?format=` are both accepted because they are both
        // what people type: `/feed.xml` in a reader's UI, `?format=atom` in a link.
        .route("/feed.xml", get(feeds::projects_feed))
        .route("/rss.xml", get(feeds::projects_feed))
        .route("/atom.xml", get(feeds::projects_feed))
        .route("/projects/{slug}/feed.xml", get(feeds::project_feed))
        .route("/projects/{slug}/rss.xml", get(feeds::project_feed))
        .route("/projects/{slug}/atom.xml", get(feeds::project_feed))
        .route("/projects/{slug}/board/feed.xml", get(feeds::board_feed))
        .route("/projects/{slug}/board/atom.xml", get(feeds::board_feed))
        .route("/projects/{slug}/milestones/feed.xml", get(feeds::milestones_feed))
        .route("/projects/{slug}/consensus/feed.xml", get(feeds::consensus_feed))
        .route("/projects/{slug}/consensus/atom.xml", get(feeds::consensus_feed))
        .route("/projects/{slug}/documents/feed.xml", get(feeds::documents_feed))
        .route("/projects/{slug}/documents/atom.xml", get(feeds::documents_feed))
        .route("/api/v1/search", get(search))
        .route("/api/v1/stats", get(stats))
        .route("/api/v1/stats/summary", get(stats_summary))
        .route("/api/v1/stats/history", get(stats_history))
        .route("/api/v1/stats/responsiveness", get(stats_responsiveness))
        .route("/api/v1/curation", get(list_curation))
        // The Go mount is `/api/v1/curation` with the page at `GET /` and votes below it; both spellings of
        // the queue path answer here, because a client that appends a slash must not get a 404.
        .route("/api/v1/curation/", get(list_curation))
        .route("/api/v1/curation/{kind}/{id}/vote", post(curation_vote))
        .route(
            // The Go route is `{project_id}` -- a number, not a slug. A client posting to the Go URL must not
            // get a 404 from this one, and the page has the id to hand.
            "/api/v1/projects/{project_id}/threads/{thread_kind}/{thread_id}",
            get(thread_comments).post(post_comment),
        )
        .route(
            "/api/v1/projects/{project_id}/threads/{thread_kind}/{thread_id}/{id}",
            axum::routing::delete(delete_comment),
        )
        .route(
            "/api/v1/projects/{project_id}/threads/{thread_kind}/{thread_id}/{id}/vote",
            post(vote_comment),
        )
        // §5.1/§6.2's strategy surface, at the GO's own paths: the frontend reads
        // `/api/v1/projects/{id}/strategy/themes` (settings.js), so a port that only answered the curation queue
        // would leave the page blank with a 404 while every probe passed.
        .route(
            "/api/v1/projects/{project_id}/strategy/weight-proposals",
            get(list_weight_proposals).post(propose_weight),
        )
        .route(
            "/api/v1/projects/{project_id}/strategy/weight-proposals/{id}/consent",
            post(consent_weight),
        )
        .route(
            "/api/v1/projects/{project_id}/strategy/weight-proposals/{id}/withdraw",
            post(withdraw_weight),
        )
        .route(
            "/api/v1/projects/{project_id}/strategy/themes",
            get(list_themes).post(propose_theme),
        )
        .route(
            "/api/v1/projects/{project_id}/strategy/themes/{id}/consent",
            post(consent_theme),
        )
        .route(
            "/api/v1/projects/{project_id}/strategy/themes/{id}/withdraw",
            post(withdraw_theme),
        )
        .route(
            "/api/v1/projects/{project_id}/strategy/theme-proposals",
            get(list_theme_proposals),
        )
        .route(
            "/api/v1/projects/{slug}/moderation/arena/remove",
            post(arena_remove),
        )
        .route(
            "/api/v1/projects/{slug}/moderation/arena/restore",
            post(arena_restore),
        )
        .route(
            "/api/v1/notifications",
            get(list_notifications),
        )
        .route(
            "/api/v1/notifications/read-all",
            post(mark_all_notifications_read),
        )
        .route(
            "/api/v1/notifications/{id}/read",
            post(mark_notification_read),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/solution-call",
            get(solution_call_readiness).post(open_solution_call),
        )
        .route(
            "/api/v1/projects/{slug}/consensus/{call}/fallback",
            get(call_fallback),
        )
        .route(
            "/api/v1/projects/{slug}/consensus/{call}/outcome",
            post(record_call_outcome),
        )
        .route(
            "/api/v1/projects/{slug}/consensus/{call}/hold",
            get(get_call_hold).post(place_emergency_hold),
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
        WriteError::Forbidden(message) => ApiError::forbidden(message),
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

/// The project named by a numeric id, as the Go table's `{project_id}` routes expect.
///
/// A separate function from `project_for` on purpose: the Go router mounts some collections on the slug and
/// others on the id, and a route that quietly accepted either would hide a client calling the wrong one.
async fn project_by_id(
    state: &AppState,
    raw: &str,
) -> Result<concord_db::projects::Project, ApiError> {
    let id = parse_id(raw, "project id")?;
    concord_db::projects::project_by_id(&state.pool, id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {id}")))
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

/// `GET /api/v1/projects/{slug}/consensus/{call_id}/hold` — the in-force hold, or null.
///
/// The call's OWN project answers, not the URL's: the id is sequential, so a route that resolved the call
/// by id alone would let anyone who can read project A read project B's hold grounds and expiry.
async fn get_call_hold(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
) -> Result<Json<Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    let call_id = parse_id(&call, "call id")?;
    let call = concord_db::consensus::call(&state.pool, call_id)
        .await
        .map_err(internal)?
        .filter(|found| found.project_id == project.id)
        .ok_or_else(|| ApiError::not_found(format!("consensus call {call_id}")))?;
    let _ = call;
    let hold = concord_db::holds::active_for_call(&state.pool, call_id)
        .await
        .map_err(internal)?;
    // `{"hold": null}` rather than 404: "no hold" is a fact about the call, not a missing resource.
    Ok(Json(json!({
        "hold": hold.map(|hold| serde_json::to_value(hold)).transpose().map_err(internal)?
    })))
}

#[derive(Debug, Deserialize)]
struct HoldRequest {
    #[serde(default)]
    grounds: String,
    #[serde(default)]
    reason: String,
    #[serde(default)]
    days: i64,
}

/// `POST /api/v1/projects/{slug}/consensus/{call_id}/hold` — suspend a call.
///
/// The call is resolved first, so the role is asked for on the project that owns it. Asking for the role
/// against the URL's project while loading the call by id alone let a maintainer of one project suspend
/// another's call.
async fn place_emergency_hold(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<HoldRequest>,
) -> Result<(axum::http::StatusCode, Json<Value>), ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let _url_project = project_for(&state, &slug).await?;
    let call_id = parse_id(&call, "call id")?;
    let owner = concord_db::consensus::call(&state.pool, call_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("consensus call {call_id}")))?;
    require_maintainer(&state.pool, owner.project_id, user.id).await?;
    let hold = concord_db::holds::place(
        &state.pool,
        owner.project_id,
        call_id,
        user.id,
        &body.grounds,
        &body.reason,
        body.days,
    )
    .await
    .map_err(write_error)?;
    Ok((
        axum::http::StatusCode::CREATED,
        Json(serde_json::to_value(hold).map_err(internal)?),
    ))
}

/// `GET /api/v1/projects/{project_id}/charter` — the numbers every threshold reads.
///
/// The path segment is the NUMERIC project id, as the Go route is; the neighbouring routes take a slug. A
/// slug here answers a parse error rather than silently resolving, which is what the Go handler does —
/// `requireProjectID` parses and refuses, it does not look the slug up.
async fn get_charter(
    State(state): State<AppState>,
    Path(project): Path<String>,
) -> Result<Json<Value>, ApiError> {
    let project_id = parse_id(&project, "project id")?;
    let exists: Option<i64> = sqlx::query_scalar("select id from projects where id = $1")
        .bind(project_id)
        .fetch_optional(&state.pool)
        .await
        .map_err(internal)?;
    if exists.is_none() {
        return Err(ApiError::not_found(format!("project {project_id}")));
    }
    let charter = concord_db::charter::charter(&state.pool, project_id)
        .await
        .map_err(internal)?;
    Ok(Json(concord_db::charter::to_json(&charter)))
}

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
    headers: HeaderMap,
    Json(body): Json<CallOutcomeRequest>,
) -> Result<Json<Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
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
/// route that answered it would leak one project's board to another's reader.
async fn feature_for(
    state: &AppState,
    project_id: i64,
    feature: &str,
) -> Result<concord_db::features::Feature, ApiError> {
    let id = feature
        .parse::<i64>()
        .map_err(|_| ApiError::invalid_request("feature id must be an integer"))?;
    concord_db::features::feature(&state.pool, id)
        .await
        .map_err(internal)?
        .filter(|found| found.project_id == project_id)
        .ok_or_else(|| ApiError::not_found(format!("feature {feature}")))
}

/// `GET /api/v1/projects/{slug}/features/{feature}/solution-call` — §6.5's three conditions.
///
/// A feature nobody has proposed a solution to is one unmet condition, not a 404: "no solutions yet" and
/// "no such feature" must not look the same to a caller.
async fn solution_call_readiness(
    State(state): State<AppState>,
    Path((slug, feature)): Path<(String, String)>,
) -> Result<Json<Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    let feature = feature_for(&state, project.id, &feature).await?;
    let readiness = concord_db::arena::readiness(&state.pool, feature.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(readiness).map_err(internal)?))
}

#[derive(Debug, Deserialize, Default)]
struct SolutionCallRequest {
    #[serde(default)]
    solution_id: i64,
    #[serde(default)]
    early_reason: String,
}

/// `POST /api/v1/projects/{slug}/features/{feature}/solution-call` — open §6.5's call on the leader.
///
/// The call opens on the LEADING solution, as the Go store does, and refuses unless the three conditions
/// hold — naming the first one that failed. Opening it and letting it fail later spends a consensus window
/// on a decision the arena already says is premature.
///
/// §6.5's discretionary path: a collaborator may open a call early with a stated reason. Early calls are
/// flagged, not hidden: the record has to say the conditions were not met, or the decision reads as one the
/// arena endorsed when it did not.
async fn open_solution_call(
    State(state): State<AppState>,
    Path((slug, feature)): Path<(String, String)>,
    headers: HeaderMap,
    body: Option<Json<SolutionCallRequest>>,
) -> Result<(StatusCode, Json<Value>), ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = project_for(&state, &slug).await?;
    let feature = feature_for(&state, project.id, &feature).await?;
    let body = body.map(|Json(body)| body).unwrap_or_default();

    let readiness = concord_db::arena::readiness(&state.pool, feature.id)
        .await
        .map_err(internal)?;
    if readiness.leader_id == 0 {
        return Err(ApiError::invalid_request(
            "no solution leads this feature's arena, so there is nothing to call".to_string(),
        ));
    }
    // A caller naming a different solution is asking the store to open a call the arena says is premature.
    if body.solution_id != 0 && body.solution_id != readiness.leader_id {
        return Err(ApiError::invalid_request(format!(
            "solution {} is not the leader ({} leads this arena)",
            body.solution_id, readiness.leader_id
        )));
    }
    let early = body.early_reason.trim().to_string();
    if !early.is_empty() {
        let role: String = sqlx::query_scalar(
            "select coalesce((select role from members where project_id = $1 and user_id = $2), 'guest')",
        )
        .bind(project.id)
        .bind(user.id)
        .fetch_optional(&state.pool)
        .await
        .map_err(internal)?
        .unwrap_or_else(|| "guest".to_string());
        if !matches!(role.as_str(), "contributor" | "reviewer" | "maintainer" | "owner") {
            return Err(ApiError::forbidden(format!(
                "opening a call early is a collaborator's discretion; you are {role}"
            )));
        }
    } else if !readiness.ready {
        let failed = readiness
            .conditions
            .iter()
            .find(|condition| !condition.met)
            .map(|condition| format!("{} ({})", condition.name, condition.detail))
            .unwrap_or_else(|| "unknown".to_string());
        return Err(ApiError::invalid_request(format!(
            "§6.5's conditions are not met: {failed}"
        )));
    }

    let opened = concord_db::consensus_writes::open_solution_call(
        &state.pool,
        project.id,
        feature.id,
        user.id,
        readiness.leader_id,
        if early.is_empty() { None } else { Some(early.as_str()) },
        readiness.leader_title.as_str(),
    )
    .await
    .map_err(write_error)?;
    Ok((
        StatusCode::CREATED,
        Json(serde_json::to_value(opened).map_err(internal)?),
    ))
}

/// `GET /api/v1/projects/{slug}/consensus/{call}/fallback` — §6.5's chain, the call's own solution absent.
async fn call_fallback(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
) -> Result<Json<Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    let call_id = parse_id(&call, "call id")?;
    let found = concord_db::consensus::call(&state.pool, call_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("consensus call {call_id}")))?;
    if found.project_id != project.id {
        return Err(ApiError::not_found(format!("consensus call {call_id}")));
    }
    let chain = concord_db::arena::fallback(&state.pool, call_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({
        "call_id": call_id,
        "fallback": chain,
    })))
}

/// `GET /api/v1/notifications` — the reader's own inbox, newest first, with the badge.
///
/// `limit` outside `1..=200` becomes 50 rather than a 400: the header asks for this on every render, and a
/// bad numeral must not take the page down with it.
async fn list_notifications(
    State(state): State<AppState>,
    Query(query): Query<std::collections::HashMap<String, String>>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let limit = query
        .get("limit")
        .and_then(|value| value.parse::<i64>().ok())
        .unwrap_or(50);
    let inbox = concord_db::notifications::list(&state.pool, user.id, limit)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(inbox).map_err(internal)?))
}

/// `POST /api/v1/notifications/{id}/read` — idempotent, and only ever the reader's own row.
async fn mark_notification_read(
    State(state): State<AppState>,
    Path(id): Path<String>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let id = parse_id(&id, "notification id")?;
    match concord_db::notifications::mark_read(&state.pool, user.id, id).await {
        Ok(()) => Ok(Json(serde_json::json!({ "id": id, "read": true }))),
        // Not found and not yours are the same answer: a distinct one would confirm the id is real.
        Err(sqlx::Error::RowNotFound) => Err(ApiError::not_found(format!("notification {id}"))),
        Err(error) => Err(internal(error)),
    }
}

/// `POST /api/v1/notifications/read-all` — clears the badge and says how many rows it took.
async fn mark_all_notifications_read(
    State(state): State<AppState>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let cleared = concord_db::notifications::mark_all_read(&state.pool, user.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({ "read": cleared })))
}

/// `GET /api/v1/curation` — §4's queue: every proposal kind and every reviewed act, one page.
///
/// The default is the queue view, not the log view: `state=open` unless the caller says otherwise, because a
/// page that opened on the whole history is a log with a review button bolted on. `unvoted=1` needs a
/// reader to mean anything, so an anonymous caller asking for it gets the unfiltered list rather than an
/// empty one that looks like good news.
async fn list_curation(
    State(state): State<AppState>,
    Query(query): Query<std::collections::HashMap<String, String>>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let viewer = bearer_user(&state, &headers).await?.map_or(0, |user| user.id);
    let mut filter = concord_db::curation::Filter {
        viewer_id: viewer,
        ..Default::default()
    };
    if let Some(value) = query.get("kind") {
        filter.kind = value.trim().to_string();
    }
    if let Some(value) = query.get("state") {
        filter.state = value.trim().to_string();
    }
    if let Some(value) = query.get("q") {
        filter.text = value.trim().to_string();
    }
    if let Some(value) = query.get("cursor") {
        filter.cursor = value.clone();
    }
    if let Some(slug) = query.get("project") {
        if let Some(project) = concord_db::projects::project_by_slug(&state.pool, slug)
            .await
            .map_err(internal)?
        {
            filter.project_id = project.id;
        }
    }
    if let Some(value) = query.get("actor_id").and_then(|value| value.parse::<i64>().ok()) {
        filter.actor_id = value;
    }
    if let Some(value) = query.get("limit").and_then(|value| value.parse::<i64>().ok()) {
        filter.limit = value;
    }
    if let Some(value) = query.get("since").and_then(|value| value.parse::<f64>().ok()) {
        filter.since = value;
    }
    if let Some(value) = query.get("until").and_then(|value| value.parse::<f64>().ok()) {
        filter.until = value;
    }
    if query.get("unvoted").map(String::as_str) == Some("1") && viewer != 0 {
        filter.unvoted_by = viewer;
    }

    let page = concord_db::curation::list(&state.pool, &filter)
        .await
        .map_err(|error| match error {
            sqlx::Error::Decode(message) => ApiError::invalid_request(message.to_string()),
            other => internal(other),
        })?;
    Ok(Json(serde_json::to_value(page).map_err(internal)?))
}

/// `GET /api/v1/projects/{slug}/threads/{kind}/{id}` — a whole thread, oldest first.
///
/// An unknown kind is a 400 rather than an empty list: "this thread has no comments" and "this is not a
/// thread kind" are different answers, and an empty list for the second hides a client bug.
async fn thread_comments(
    State(state): State<AppState>,
    Path((project_id, kind, thread_id)): Path<(String, String, String)>,
) -> Result<Json<Value>, ApiError> {
    let project = project_by_id(&state, &project_id).await?;
    let thread_id = parse_id(&thread_id, "thread id")?;
    if !concord_db::comments::THREAD_KINDS.contains(&kind.as_str()) {
        return Err(ApiError::invalid_request(format!(
            "invalid thread_kind {kind:?}"
        )));
    }
    let comments = concord_db::comments::thread(&state.pool, project.id, thread_id, &kind)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(comments).map_err(internal)?))
}

#[derive(Debug, Deserialize, Default)]
struct CommentRequest {
    #[serde(default)]
    body: String,
    #[serde(default)]
    label: String,
    #[serde(default)]
    parent_id: Option<i64>,
}

/// `POST /api/v1/projects/{slug}/threads/{kind}/{id}` — post a comment.
async fn post_comment(
    State(state): State<AppState>,
    Path((project_id, kind, thread_id)): Path<(String, String, String)>,
    headers: HeaderMap,
    Json(body): Json<CommentRequest>,
) -> Result<(StatusCode, Json<Value>), ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = project_by_id(&state, &project_id).await?;
    let thread_id = parse_id(&thread_id, "thread id")?;
    let comment = concord_db::comments::create(
        &state.pool,
        project.id,
        thread_id,
        &kind,
        user.id,
        &body.body,
        &body.label,
        body.parent_id,
    )
    .await
    .map_err(write_error)?;
    Ok((
        StatusCode::CREATED,
        Json(serde_json::to_value(comment).map_err(internal)?),
    ))
}

/// `DELETE /api/v1/projects/{slug}/threads/{kind}/{id}/{comment}` — soft delete, author or maintainer.
async fn delete_comment(
    State(state): State<AppState>,
    Path((project_id, _kind, _thread_id, id)): Path<(String, String, String, String)>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = project_by_id(&state, &project_id).await?;
    let comment_id = parse_id(&id, "comment id")?;
    concord_db::comments::delete(&state.pool, project.id, comment_id, user.id)
        .await
        .map_err(write_error)?;
    Ok(Json(serde_json::json!({ "id": comment_id, "deleted": true })))
}

#[derive(Debug, Deserialize, Default)]
struct CommentVoteRequest {
    #[serde(default)]
    value: i64,
}

/// `POST .../{comment}/vote` — one vote per person, and the score is recomputed from the rows.
async fn vote_comment(
    State(state): State<AppState>,
    Path((project_id, _kind, _thread_id, id)): Path<(String, String, String, String)>,
    headers: HeaderMap,
    Json(body): Json<CommentVoteRequest>,
) -> Result<Json<Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = project_by_id(&state, &project_id).await?;
    let comment_id = parse_id(&id, "comment id")?;
    let score = concord_db::comments::vote(&state.pool, project.id, comment_id, user.id, body.value)
        .await
        .map_err(write_error)?;
    Ok(Json(serde_json::json!({ "id": comment_id, "score": score })))
}

/// A moderation act's body.
///
/// Every scalar is `Option`, because `Json<T>` refuses `null` for a bare `i64` and answers with axum's own
/// **422 and a plain-text body** -- not the API's error envelope, and not a status any client of this API
/// handles. A `null` is an ordinary thing for a form to send; it is "no value", and `unwrap_or` turns it into
/// the same 400 the absent field gets.
#[derive(Debug, Deserialize, Default)]
struct ArenaActRequest {
    #[serde(default)]
    feature_id: Option<i64>,
    #[serde(default)]
    arena_id: Option<i64>,
    #[serde(default)]
    reason: Option<String>,
    #[serde(default)]
    confirm: Option<bool>,
    #[serde(default)]
    undoes: Option<i64>,
}

impl ArenaActRequest {
    fn feature_id(&self) -> i64 {
        self.feature_id.unwrap_or(0)
    }

    fn arena_id(&self) -> i64 {
        self.arena_id.unwrap_or(0)
    }

    fn reason(&self) -> &str {
        self.reason.as_deref().unwrap_or("")
    }

    fn confirmed(&self) -> bool {
        self.confirm.unwrap_or(false)
    }

    fn undoes(&self) -> i64 {
        self.undoes.unwrap_or(0)
    }
}

/// The trust floor for a moderation act; `CONCORD_MOD_TRUST_MIN` moves it, and 1 is the default the Go
/// config ships with.
fn mod_trust_min() -> i32 {
    std::env::var("CONCORD_MOD_TRUST_MIN")
        .ok()
        .and_then(|value| value.parse::<i32>().ok())
        .filter(|value| *value >= 0)
        .unwrap_or(1)
}

/// The two-door permission check, in one place: instance trust, or a project role at `reviewer`.
async fn permit_moderation(
    state: &AppState,
    project_id: i64,
    user_id: i64,
) -> Result<(), ApiError> {
    let allowed = concord_db::moderation::may_moderate(&state.pool, project_id, user_id, mod_trust_min())
        .await
        .map_err(internal)?;
    if allowed {
        Ok(())
    } else {
        Err(ApiError::forbidden(
            "this act needs a reviewer role in the project, or a high enough trust level",
        ))
    }
}

/// `POST /api/v1/projects/{slug}/moderation/arena/remove`
///
/// The page names the idea, not the arena: an idea's arena is a property of the ranking. One arena is
/// inferred; several must be named, because guessing which ranking to take an idea out of is exactly the
/// silent choice this surface must not make.
async fn arena_remove(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    headers: HeaderMap,
    Json(body): Json<ArenaActRequest>,
) -> Result<(StatusCode, Json<Value>), ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = project_for(&state, &slug).await?;
    permit_moderation(&state, project.id, user.id).await?;
    if !body.confirmed() {
        // The browser confirms in the page too; this is for the script that does not.
        return Err(ApiError::invalid_request("this act needs confirmation"));
    }
    if body.feature_id() == 0 {
        return Err(ApiError::invalid_request("feature_id is required"));
    }
    let mut arena_id = body.arena_id();
    if arena_id == 0 {
        let arenas = concord_db::moderation::arenas_for_feature(&state.pool, body.feature_id())
            .await
            .map_err(internal)?;
        match arenas.len() {
            0 => return Err(ApiError::not_found("this idea is not in an arena")),
            1 => arena_id = arenas[0],
            _ => {
                return Err(ApiError::invalid_request(
                    "this idea is in more than one arena; name the arena_id",
                ))
            }
        }
    }
    let act = concord_db::moderation::remove_feature_from_arena(
        &state.pool,
        project.id,
        arena_id,
        body.feature_id(),
        user.id,
        body.reason(),
    )
    .await
    .map_err(write_error)?;
    Ok((
        StatusCode::CREATED,
        Json(serde_json::json!({
            "act": act,
            "undo": { "action": "arena.restore", "target": act.id },
        })),
    ))
}

/// `POST /api/v1/projects/{slug}/moderation/arena/restore`
///
/// Takes the id of the act it reverses, not the feature's prior values: those are read from the log.
async fn arena_restore(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    headers: HeaderMap,
    Json(body): Json<ArenaActRequest>,
) -> Result<Json<Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = project_for(&state, &slug).await?;
    permit_moderation(&state, project.id, user.id).await?;
    if !body.confirmed() {
        return Err(ApiError::invalid_request("this act needs confirmation"));
    }
    if body.undoes() == 0 {
        return Err(ApiError::invalid_request("undoes is required"));
    }
    let act = concord_db::moderation::restore_feature_to_arena(
        &state.pool,
        project.id,
        user.id,
        body.undoes(),
        body.reason(),
    )
    .await
    .map_err(write_error)?;
    Ok(Json(serde_json::json!({ "act": act })))
}

#[derive(Debug, Deserialize, Default)]
struct CurationVoteRequest {
    #[serde(default)]
    vote: Option<String>,
    #[serde(default)]
    rationale: Option<String>,
}

/// `POST /api/v1/curation/{kind}/{id}/vote` — endorse or contest a moderation act in the queue.
///
/// `taxonomy` and `strategy` are refused with 501 rather than implemented halfway. Their ratification needs the
/// *eligible set* — `eligibleTaggers`, the collaborators a weight proposal is put to — and a consent recorded
/// without that check is the defect the Go store's own comment records: "anyone with an account could ratify a
/// tag proposal for a project they had no relationship with, and since guest identities exist, a nameless one
/// could." That set is a slice of its own, named in the ledger; a version of this route that skipped it would
/// pass a probe and reopen the hole.
async fn curation_vote(
    State(state): State<AppState>,
    Path((kind, id)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<CurationVoteRequest>,
) -> Result<(StatusCode, Json<Value>), ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let ref_id = parse_id(&id, "id")?;
    let raw = body.vote.as_deref().unwrap_or("");
    let rationale = body.rationale.as_deref().unwrap_or("");
    match kind.as_str() {
        "act" => {
            let vote = match raw.trim().to_ascii_lowercase().as_str() {
                "yes" | "consent" | "endorse" | "1" => 1,
                "no" | "contest" | "-1" => -1,
                _ => {
                    return Err(ApiError::invalid_request("vote must be yes or no"));
                }
            };
            // Reviewing an act is reviewer work: it writes a judgement about somebody else's decision, so it
            // takes the standing that decision took.
            let (_, project_id, _) = concord_db::curation::act_target(&state.pool, ref_id)
                .await
                .map_err(write_error)?;
            permit_moderation(&state, project_id, user.id).await?;
            let (yes, no) = concord_db::curation::review_act(
                &state.pool,
                ref_id,
                user.id,
                vote,
                rationale,
            )
            .await
            .map_err(write_error)?;
            Ok((
                StatusCode::OK,
                Json(serde_json::json!({ "recorded": true, "decided": true, "yes": yes, "no": no })),
            ))
        }
        // §4.3: a taxonomy proposal is decided by the ELIGIBLE SET, and the set is checked in the store before
        // anything is written. A consent recorded without it is the defect the Go comment records: "anyone with
        // an account could ratify a tag proposal for a project they had no relationship with, and since guest
        // identities exist, a nameless one could."
        "taxonomy" => match concord_db::taxonomy::consent(&state.pool, ref_id, user.id).await {
            Ok(recorded) => Ok((
                StatusCode::OK,
                Json(serde_json::json!({
                    "recorded": true,
                    "ratified": recorded.ratified,
                    "consents": recorded.consents,
                    "eligible": recorded.eligible,
                    "threshold": recorded.threshold,
                    "proposal": recorded.proposal,
                })),
            )),
            Err(refusal) => Err(taxonomy_refusal(refusal)),
        },
        // §5.1: a strategic weight is proposed, consented to by §8.2's eligible population, and applied only if
        // the feature still carries the weight the proposal was written against.
        "strategy" => match concord_db::strategy::consent(&state.pool, ref_id, user.id).await {
            Ok(recorded) => Ok((
                StatusCode::OK,
                Json(serde_json::json!({
                    "recorded": true,
                    "ratified": recorded.ratified,
                    "stale": recorded.stale,
                    "consents": recorded.consents,
                    "eligible": recorded.eligible,
                    "proposal": recorded.proposal,
                })),
            )),
            Err(refusal) => Err(strategy_refusal(refusal)),
        },
        other => Err(ApiError::invalid_request(format!(
            "unknown curation kind {other:?}"
        ))),
    }
}

/// `GET /api/v1/stats` — the one payload the statistics page renders from.
///
/// The page computes nothing itself: a page that recomputes an aggregate the API returns is two answers
/// waiting to disagree, and the page is the one nobody notices is wrong.
async fn stats(State(state): State<AppState>) -> Result<Json<Value>, ApiError> {
    let payload = concord_db::stats::payload(&state.pool)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(payload).map_err(internal)?))
}

/// `GET /api/v1/stats/summary` — the census alone, for the reader that wants the instance's own numbers
/// without accepting anything first.
async fn stats_summary(State(state): State<AppState>) -> Result<Json<Value>, ApiError> {
    let census = concord_db::stats::census(&state.pool)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(census).map_err(internal)?))
}

/// `GET /api/v1/search` — §15's discovery endpoint.
///
/// A malformed `min_lang_pct`, `min_health`, `model` or `sort` is a 400 that names the range, not a silently
/// ignored parameter: a filter a reader set and the server dropped returns hits they cannot explain.
async fn search(
    State(state): State<AppState>,
    Query(params): Query<SearchParams>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let filters = concord_db::search::SearchFilters {
        tags: params.tag,
        languages: params.language,
        min_lang_pct: params.min_lang_pct.unwrap_or(0.0),
        min_health: params.min_health.unwrap_or(0.0),
        model: params.model.unwrap_or_default(),
        license: params.license.unwrap_or_default(),
    };
    if !(0.0..=100.0).contains(&filters.min_lang_pct) {
        return Err(ApiError::invalid_request(
            "min_lang_pct must be a number in [0,100]",
        ));
    }
    if !(0.0..=1.0).contains(&filters.min_health) {
        return Err(ApiError::invalid_request(
            "min_health must be a number in [0,1]",
        ));
    }
    if !filters.model.is_empty() && filters.model != "collective" && filters.model != "maintainer_led" {
        return Err(ApiError::invalid_request(
            "model must be collective or maintainer_led",
        ));
    }
    let sort = params.sort.unwrap_or_default().to_ascii_lowercase();
    if !matches!(sort.as_str(), "" | "relevance" | "updated" | "health" | "newest") {
        return Err(ApiError::invalid_request(
            "sort must be relevance, updated, health, or newest",
        ));
    }
    let actor = bearer_user(&state, &headers).await?;
    let actor_id = actor.map_or(0, |user| user.id);
    let result = concord_db::search::search(
        &state.pool,
        params.q.as_deref().unwrap_or(""),
        &filters,
        &sort,
        actor_id,
    )
    .await
    .map_err(internal)?;
    Ok(Json(serde_json::to_value(result).map_err(internal)?))
}

#[derive(Debug, Deserialize, Default)]
struct SearchParams {
    #[serde(default)]
    q: Option<String>,
    /// Repeatable: every tag must match.
    #[serde(default)]
    tag: Option<Vec<String>>,
    /// Repeatable: every language must match.
    #[serde(default)]
    language: Option<Vec<String>>,
    #[serde(default)]
    min_lang_pct: Option<f64>,
    #[serde(default)]
    min_health: Option<f64>,
    #[serde(default)]
    model: Option<String>,
    #[serde(default)]
    license: Option<String>,
    #[serde(default)]
    sort: Option<String>,
}

/// `GET /api/v1/stats/history?days=90` — recorded days, newest first.
///
/// A BARE ARRAY, not an envelope: this is the only series route and a charting client wants the rows. A
/// malformed `days` falls back to the default rather than answering 400 — matching how the audit and finder
/// routes read their paging parameters — and the cap still applies, so a hostile value cannot widen the
/// response.
async fn stats_history(
    State(state): State<AppState>,
    Query(params): Query<HistoryParams>,
) -> Result<Json<Value>, ApiError> {
    // `days` is read as TEXT and parsed here, not deserialized as a number: axum's `Query` rejects a
    // non-integer with a 400, and the Go handler falls back to the default instead. The header asks this route
    // for its series on every render, so a 400 is a broken chart for a value the reader never typed.
    let days = params
        .days
        .as_deref()
        .and_then(|raw| raw.trim().parse::<i64>().ok())
        .unwrap_or(0);
    let rows = concord_db::stats::history(&state.pool, days)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(rows).map_err(internal)?))
}

#[derive(Debug, Deserialize, Default)]
struct HistoryParams {
    #[serde(default)]
    days: Option<String>,
}

/// `GET /api/v1/stats/responsiveness` — the newcomer-responsiveness ranking, as rows.
///
/// A project under the sample floor is ABSENT rather than last, and there is no `"unmeasured": true` row to
/// render as a dash: a dash in a leaderboard still assigns a position, and the position is not the instance's
/// to publish.
async fn stats_responsiveness(
    State(state): State<AppState>,
) -> Result<Json<Value>, ApiError> {
    let rows = concord_db::stats::responsiveness_leaderboard(&state.pool)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(rows).map_err(internal)?))
}

/// Translate a taxonomy refusal into the envelope, keeping the distinction that matters to the voter: "not yet"
/// (the consent counted, the threshold is not met) is a 200-level fact about the tally, not a failure, but this
/// route answers it as a 409 so a caller cannot mistake a recorded vote for a ratified change.
fn taxonomy_refusal(refusal: concord_db::taxonomy::Refused) -> ApiError {
    use concord_db::taxonomy::Refused;
    match refusal {
        Refused::NotFound => ApiError::not_found("no such proposal"),
        Refused::Decided(status) => {
            ApiError::conflict(format!("the proposal is already {status}"))
        }
        Refused::NotEligible => ApiError::forbidden(
            "only an eligible tagger may consent: a member with role contributor, reviewer, maintainer or \
             owner in this project",
        ),
        Refused::NotYet {
            verdict,
            consents,
            eligible,
        } => ApiError::conflict(format!(
            "the consent was recorded and the proposal is {verdict} ({consents} of {eligible} eligible \
             consenting)"
        )),
        Refused::ApplyFailed(detail) => {
            ApiError::conflict(format!("the change could not be applied: {detail}"))
        }
    }
}

/// Translate a strategy refusal into the envelope.
///
/// `Stale` is a 409 that names both numbers: the proposer needs to know the weight it expected and the one it
/// found, because the difference is the whole reason nothing was written.
fn strategy_refusal(refusal: concord_db::strategy::Refused) -> ApiError {
    use concord_db::strategy::Refused;
    match refusal {
        Refused::NotFound => ApiError::not_found("no such proposal"),
        Refused::Decided(status) => ApiError::conflict(format!("the proposal is already {status}")),
        Refused::SelfConsent => {
            ApiError::forbidden("the proposer cannot consent to their own proposal")
        }
        Refused::NotEligible => ApiError::forbidden(
            "only an eligible collaborator may consent: §8.2 asks for a qualifying contribution, not merely a \
             membership",
        ),
        Refused::NotYet {
            verdict,
            consents,
            eligible,
        } => ApiError::conflict(format!(
            "the consent was recorded and the proposal is {verdict} ({consents} of {eligible} eligible \
             consenting)"
        )),
        Refused::Stale { found } => ApiError::conflict(format!(
            "the proposal is stale: the feature's weight changed to {found} while the vote was open, and \
             nothing was written"
        )),
        Refused::Failed(detail) => {
            ApiError::conflict(format!("the weight could not be applied: {detail}"))
        }
    }
}

// ---------------------------------------------------------------- §5.1 / §6.2 strategy

/// The caller and their role in the project named by the path. `project_id` is a NUMBER here because that is
/// what the Go routes take: the frontend passes `data-project-id`.
struct StrategyCaller {
    /// The authenticated user.
    user: concord_db::auth::User,
    /// Their role in this project, `guest` when they are not a member.
    role: String,
}

async fn strategy_project(
    state: &AppState,
    project_id: i64,
    headers: &HeaderMap,
) -> Result<StrategyCaller, ApiError> {
    let user = bearer_user(state, headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let exists: Option<i64> = sqlx::query_scalar("select id from projects where id = $1")
        .bind(project_id)
        .fetch_optional(&state.pool)
        .await
        .map_err(internal)?;
    if exists.is_none() {
        return Err(ApiError::not_found("no such project"));
    }
    let role: String = sqlx::query_scalar(
        "select coalesce((select role from members where project_id = $1 and user_id = $2), 'guest')",
    )
    .bind(project_id)
    .bind(user.id)
    .fetch_one(&state.pool)
    .await
    .map_err(internal)?;
    Ok(StrategyCaller { user, role })
}

async fn list_weight_proposals(
    State(state): State<AppState>,
    Path(project_id): Path<i64>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    strategy_project(&state, project_id, &headers).await?;
    let rows = concord_db::strategy::proposals(&state.pool, project_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

async fn list_theme_proposals(
    State(state): State<AppState>,
    Path(project_id): Path<i64>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    strategy_project(&state, project_id, &headers).await?;
    let rows = concord_db::themes::proposals(&state.pool, project_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

async fn list_themes(
    State(state): State<AppState>,
    Path(project_id): Path<i64>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    strategy_project(&state, project_id, &headers).await?;
    let rows = concord_db::themes::themes(&state.pool, project_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

async fn propose_weight(
    State(state): State<AppState>,
    Path(project_id): Path<i64>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
    let who = strategy_project(&state, project_id, &headers).await?;
    let feature_id = body
        .get("feature_id")
        .and_then(serde_json::Value::as_i64)
        .ok_or_else(|| ApiError::invalid_request("feature_id is required"))?;
    let weight = body
        .get("weight")
        .and_then(serde_json::Value::as_f64)
        .ok_or_else(|| ApiError::invalid_request("weight is required"))?;
    let rationale = body.get("rationale").and_then(serde_json::Value::as_str).unwrap_or("");
    match concord_db::strategy::propose(&state.pool, project_id, feature_id, who.user.id, weight, rationale).await
    {
        Ok(proposal) => Ok((StatusCode::CREATED, Json(serde_json::json!(proposal)))),
        Err(refusal) => Err(strategy_refusal(refusal)),
    }
}

async fn propose_theme(
    State(state): State<AppState>,
    Path(project_id): Path<i64>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
    let who = strategy_project(&state, project_id, &headers).await?;
    let tag = body.get("theme_tag").and_then(serde_json::Value::as_str).unwrap_or("");
    let namespace = body
        .get("theme_namespace")
        .and_then(serde_json::Value::as_str)
        .unwrap_or("");
    let weight = body
        .get("weight")
        .and_then(serde_json::Value::as_f64)
        .ok_or_else(|| ApiError::invalid_request("weight is required"))?;
    let rationale = body.get("rationale").and_then(serde_json::Value::as_str).unwrap_or("");
    match concord_db::themes::propose(
        &state.pool,
        project_id,
        who.user.id,
        tag,
        namespace,
        weight,
        rationale,
    )
    .await
    {
        Ok(proposal) => Ok((StatusCode::CREATED, Json(serde_json::json!(proposal)))),
        Err(refusal) => Err(theme_refusal(refusal)),
    }
}

/// A consent that ratified nothing is `202`, not `200`: answering 200 tells a client the roadmap moved when it
/// did not. The Go handler draws the same line, which is why this mirrors it clause for clause.
async fn consent_theme(
    State(state): State<AppState>,
    Path((project_id, id)): Path<(i64, i64)>,
    headers: HeaderMap,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
    let who = strategy_project(&state, project_id, &headers).await?;
    match concord_db::themes::consent(&state.pool, id, who.user.id).await {
        Ok((proposal, ratified)) => Ok((
            if ratified { StatusCode::OK } else { StatusCode::ACCEPTED },
            Json(serde_json::json!({ "proposal": proposal, "ratified": ratified })),
        )),
        Err(refusal) => match refusal {
            // A recorded consent below the threshold is not an error the client should treat as a failure: it is
            // the state §5.1 exists to make visible.
            concord_db::themes::Refused::NotYet {
                verdict,
                consents,
                eligible,
            } => Ok((
                StatusCode::ACCEPTED,
                Json(serde_json::json!({
                    "recorded": true,
                    "ratified": false,
                    "verdict": verdict,
                    "consents": consents,
                    "eligible": eligible,
                })),
            )),
            other => Err(theme_refusal(other)),
        },
    }
}

async fn consent_weight(
    State(state): State<AppState>,
    Path((project_id, id)): Path<(i64, i64)>,
    headers: HeaderMap,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
    let who = strategy_project(&state, project_id, &headers).await?;
    match concord_db::strategy::consent(&state.pool, id, who.user.id).await {
        Ok(recorded) => Ok((
            StatusCode::OK,
            Json(serde_json::json!({
                "recorded": true,
                "ratified": recorded.ratified,
                "stale": recorded.stale,
                "consents": recorded.consents,
                "eligible": recorded.eligible,
                "proposal": recorded.proposal,
            })),
        )),
        Err(refusal) => Err(strategy_refusal(refusal)),
    }
}

async fn withdraw_theme(
    State(state): State<AppState>,
    Path((project_id, id)): Path<(i64, i64)>,
    headers: HeaderMap,
) -> Result<StatusCode, ApiError> {
    let who = strategy_project(&state, project_id, &headers).await?;
    let maintainer = who.role == "maintainer" || who.role == "owner";
    concord_db::themes::withdraw(&state.pool, id, who.user.id, maintainer)
        .await
        .map_err(theme_refusal)?;
    Ok(StatusCode::NO_CONTENT)
}

async fn withdraw_weight(
    State(state): State<AppState>,
    Path((project_id, id)): Path<(i64, i64)>,
    headers: HeaderMap,
) -> Result<StatusCode, ApiError> {
    let who = strategy_project(&state, project_id, &headers).await?;
    let maintainer = who.role == "maintainer" || who.role == "owner";
    concord_db::strategy::withdraw(&state.pool, id, who.user.id, maintainer)
        .await
        .map_err(strategy_refusal)?;
    Ok(StatusCode::NO_CONTENT)
}

/// The theme refusals, with the ones that are about the REQUEST at 400 and the ones about the CALLER at 403.
fn theme_refusal(refusal: concord_db::themes::Refused) -> ApiError {
    use concord_db::themes::Refused;
    match refusal {
        Refused::NotFound => ApiError::not_found("no such proposal"),
        Refused::Decided(status) => ApiError::conflict(format!("the proposal is already {status}")),
        Refused::Invalid(detail) => ApiError::invalid_request(detail),
        Refused::SelfConsent => {
            ApiError::forbidden("the proposer cannot consent to their own proposal")
        }
        Refused::NotEligible => ApiError::forbidden(
            "only an eligible collaborator may consent: §8.2 asks for a qualifying contribution",
        ),
        Refused::Duplicate(detail) => ApiError::conflict(detail),
        Refused::NotYet {
            verdict,
            consents,
            eligible,
        } => ApiError::conflict(format!(
            "the consent was recorded and the proposal is {verdict} ({consents} of {eligible} eligible \
             consenting)"
        )),
        Refused::NoLifetime(days) => ApiError::invalid_request(format!(
            "the charter's theme_lifetime_days is {days}: a theme written with no lifetime is born expired, so \
             nothing was applied"
        )),
        Refused::NotYours => {
            ApiError::forbidden("only the proposer or a maintainer may withdraw a proposal")
        }
        Refused::Failed(detail) => ApiError::conflict(format!("the change could not be applied: {detail}")),
    }
}

/// May this caller read this project? The Go's `CanAccessProject`, rule for rule:
///
/// * `public` and `unlisted` are readable by anyone, including an anonymous caller;
/// * `private` and `protected` are readable by a member, and by nobody else — an anonymous caller is refused
///   before the membership query rather than after it;
/// * an unknown visibility FAILS CLOSED. Migration 0009's CHECK makes it unreachable from the database, but
///   fail-open is the wrong direction for an access check, and the store is reachable from tests.
async fn readable(
    state: &AppState,
    project: &concord_db::projects::Project,
    user: Option<&concord_db::auth::User>,
) -> Result<bool, ApiError> {
    match project.visibility.as_str() {
        "public" | "unlisted" => Ok(true),
        "private" | "protected" => {
            let Some(user) = user else {
                return Ok(false);
            };
            let count: i64 = sqlx::query_scalar(
                "select count(*) from members where project_id = $1 and user_id = $2",
            )
            .bind(project.id)
            .bind(user.id)
            .fetch_one(&state.pool)
            .await
            .map_err(internal)?;
            Ok(count > 0)
        }
        _ => Ok(false),
    }
}

/// `GET /api/v1/auth/me` — the caller, by id: the token carries only an id, so the name is looked up here.
async fn auth_me(
    State(state): State<AppState>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    // The Go answers `role: "member"` for every caller — it is not this project's role, it is "you are signed
    // in". Kept identical rather than improved: a client that switches on it exists.
    Ok(Json(serde_json::json!({
        "id": user.id,
        "username": user.username,
        "display_name": user.display_name,
        "created_at": user.created_at,
        "role": "member",
    })))
}

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

/// `GET /api/v1/projects/{id}/members` — most privileged first.
async fn list_members(
    State(state): State<AppState>,
    Path(project_id): Path<i64>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = project_by_id(&state, &project_id.to_string()).await?;
    let user = bearer_user(&state, &headers).await?;
    if !readable(&state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    let members = concord_db::members::list(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(members)))
}

/// `GET /api/v1/projects/{id}/criteria` — active first, then slug.
async fn list_criteria(
    State(state): State<AppState>,
    Path(project_id): Path<i64>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project(&state, project_id, &headers).await?;
    let rows = concord_db::criteria::list(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

/// `GET /api/v1/projects/{id}/criteria-profiles`
async fn list_criteria_profiles(
    State(state): State<AppState>,
    Path(project_id): Path<i64>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project(&state, project_id, &headers).await?;
    let rows = concord_db::criteria::profiles(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
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

/// `GET /api/v1/projects/{id}/lists` — the project's own lists first, then the instance-wide ones.
async fn project_lists(
    State(state): State<AppState>,
    Path(project_id): Path<i64>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project(&state, project_id, &headers).await?;
    let rows = concord_db::criteria::lists(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

/// A project by id, refused with a 404 the caller cannot use to enumerate private projects.
///
/// 404 and not 403 on purpose: a 403 tells an anonymous caller that the id exists, which is the enumeration
/// this catalogue's private projects are meant to prevent.
async fn readable_project(
    state: &AppState,
    project_id: i64,
    headers: &HeaderMap,
) -> Result<concord_db::projects::Project, ApiError> {
    let project = project_by_id(state, &project_id.to_string()).await?;
    let user = bearer_user(state, headers).await?;
    if !readable(state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    Ok(project)
}

/// `GET /api/v1/projects/{id}/capabilities` — the whole catalogue, each entry with this project's claim.
async fn list_capabilities(
    State(state): State<AppState>,
    Path(project_id): Path<i64>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project(&state, project_id, &headers).await?;
    let panel = concord_db::capabilities::panel(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(panel)))
}

/// `GET /api/v1/projects/{id}/field-reports` — §4.7's "worked for 83% of reporters on arm64".
async fn list_field_reports(
    State(state): State<AppState>,
    Path(project_id): Path<i64>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project(&state, project_id, &headers).await?;
    let panel = concord_db::field_reports::panel(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(panel)))
}

/// `GET /api/v1/projects/{slug}/documents/kinds` — the schema's own document vocabulary.
///
/// Static, and deliberately so: the kinds are `store.DocumentKinds`, and deriving them with `select distinct kind`
/// would let one typo invent a kind the writer would then validate against. The route exists so the frontend stops
/// hard-coding the list in a second place.
async fn document_kinds(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The project is resolved first: the kinds are instance-wide, but a route that answers for an unknown project
    // would hand back a list attached to nothing.
    let project = project_for(&state, &slug).await?;
    let user = bearer_user(&state, &headers).await?;
    if !readable(&state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    Ok(Json(serde_json::json!({ "kinds": concord_db::panels::DOCUMENT_KINDS })))
}

/// `GET /api/v1/projects/{slug}/features/{feature}/tags` — the feature's tags, alphabetical.
async fn feature_tags(
    State(state): State<AppState>,
    Path((slug, feature)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    let user = bearer_user(&state, &headers).await?;
    if !readable(&state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    let feature = feature_for(&state, project.id, &feature).await?;
    let tags = concord_db::panels::feature_tags(&state.pool, feature.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({ "feature_id": feature.id, "tags": tags })))
}

/// `GET /api/v1/projects/{slug}/features/{feature}/deps` — blocked_by, blocks, and what is still open.
async fn feature_deps(
    State(state): State<AppState>,
    Path((slug, feature)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    let user = bearer_user(&state, &headers).await?;
    if !readable(&state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    let feature = feature_for(&state, project.id, &feature).await?;
    let panel = concord_db::deps::panel(&state.pool, feature.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(panel)))
}

/// `GET /api/v1/projects/{slug}/features/{feature}/evidence` — what says this feature is worth building.
async fn feature_evidence(
    State(state): State<AppState>,
    Path((slug, feature)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    let user = bearer_user(&state, &headers).await?;
    if !readable(&state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    let feature = feature_for(&state, project.id, &feature).await?;
    let rows = concord_db::panels::evidence(&state.pool, feature.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

/// `GET /api/v1/projects/{project_id}/complaints/{id}` — one complaint, bound to the project in the path.
///
/// THE URL HAS TWO HALVES AND THEY MUST TIE. A complaint id is global, so a handler that reads only `{id}` makes
/// the project half decoration: any caller could reach any complaint on the instance through their own project's
/// URL, and a role check written against the URL's project would be checking a project the complaint has nothing to
/// do with. A mismatch is a 404, not a 403 — a 403 confirms the id is real.
///
/// A non-numeric id is INVALID, not 404: parsing it as 0 asks the store about a row that does not exist, which is a
/// 404 for the wrong reason.
async fn get_complaint(
    State(state): State<AppState>,
    Path((project_id, complaint)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    // A non-numeric project segment is a 404 and not a 400: the Go's `requireProjectID` maps a parse failure to
    // ErrNotFound, and an unknown project must not answer differently from a malformed one — both are "no such
    // project" to a caller who cannot see the catalogue.
    let Ok(project_numeric) = project_id.parse::<i64>() else {
        return Err(ApiError::not_found("no such project"));
    };
    let project = concord_db::projects::project_by_id(&state.pool, project_numeric)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such project"))?;
    let user = bearer_user(&state, &headers).await?;
    if !readable(&state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    let id = complaint
        .parse::<i64>()
        .map_err(|_| ApiError::invalid_request("complaint id must be an integer"))?;
    let found = concord_db::complaints::complaint(&state.pool, id)
        .await
        .map_err(internal)?;
    match found {
        Some(found) if found.project_id == project.id => Ok(Json(serde_json::json!(found))),
        _ => Err(ApiError::not_found(format!("complaint {complaint}"))),
    }
}

/// `GET /api/v1/projects/{project_id}/complaints/{id}/features` — the work built to answer this complaint.
///
/// An empty array, never null: `null` breaks a client that expects a list, and "no work answers this" is the common
/// case rather than the edge.
async fn complaint_features(
    State(state): State<AppState>,
    Path((project_id, complaint)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = numeric_project(&state, &project_id, &headers).await?;
    let id = complaint
        .parse::<i64>()
        .map_err(|_| ApiError::invalid_request("complaint id must be an integer"))?;
    // The complaint must belong to the project in the path, or one project's complaint is served under another's.
    let found = concord_db::complaints::complaint(&state.pool, id)
        .await
        .map_err(internal)?;
    match found {
        Some(found) if found.project_id == project.id => {}
        _ => return Err(ApiError::not_found(format!("complaint {complaint}"))),
    }
    let rows = concord_db::features::complaint_features(&state.pool, id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

/// `GET /api/v1/projects/{project_id}/features/{id}/complaints` — what justifies this feature.
async fn feature_complaints(
    State(state): State<AppState>,
    Path((project_id, feature)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = numeric_project(&state, &project_id, &headers).await?;
    let feature = feature_for(&state, project.id, &feature).await?;
    let rows = concord_db::complaints::feature_complaints(&state.pool, feature.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

/// A project by NUMERIC id, readable by the caller, refused with a 404 the caller cannot use to enumerate.
///
/// A non-numeric segment is a 404 and not a 400: `requireProjectID` maps a parse failure to `ErrNotFound`, and an
/// unknown project must not answer differently from a malformed one.
async fn numeric_project(
    state: &AppState,
    segment: &str,
    headers: &HeaderMap,
) -> Result<concord_db::projects::Project, ApiError> {
    let Ok(id) = segment.parse::<i64>() else {
        return Err(ApiError::not_found("no such project"));
    };
    let project = concord_db::projects::project_by_id(&state.pool, id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such project"))?;
    let user = bearer_user(state, headers).await?;
    if !readable(state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    Ok(project)
}

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

/// `GET /api/v1/projects/{id}/tallies` — the arena panel's per-feature counts.
///
/// Answers `null` and not `[]` for a project with no features, because the Go's nil slice marshals that way and a
/// deployed frontend is already reading it. The rest of the port's list routes answer `[]`; this one reproduces the
/// service, and the difference is recorded rather than tidied.
async fn feature_tallies(
    State(state): State<AppState>,
    Path(project_id): Path<i64>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project(&state, project_id, &headers).await?;
    let tallies = concord_db::tallies::tallies(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(tallies)))
}

/// `GET /api/v1/projects/{slug}/documents/search?q=&limit=` — full text over a project's documents.
///
/// A missing `q` is **400**, while an empty-after-trim `q` is an empty result: "you did not ask" and "you asked for
/// nothing" are different requests, and collapsing them makes a broken client look like a quiet one.
async fn search_documents(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Query(params): Query<std::collections::HashMap<String, String>>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    let user = bearer_user(&state, &headers).await?;
    if !readable(&state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    let Some(query) = params.get("q") else {
        return Err(ApiError::invalid_request("q is required"));
    };
    let limit = params
        .get("limit")
        .and_then(|text| text.parse::<i64>().ok())
        .unwrap_or(0);
    let found = concord_db::panels::search_documents(&state.pool, project.id, query, limit)
        .await
        .map_err(internal)?;
    match found {
        Ok(rows) => Ok(Json(serde_json::json!(rows))),
        // A malformed search expression is the caller's mistake, not the server's.
        Err(message) => Err(ApiError::invalid_request(message)),
    }
}

/// The body of `rank/composite`. Every field is optional: an absent body is "rank with every criterion's default".
#[derive(Debug, Default, serde::Deserialize)]
struct CompositeRequest {
    #[serde(default)]
    weights: std::collections::BTreeMap<String, f64>,
    #[serde(default)]
    profile: String,
    #[serde(default)]
    include_inactive: bool,
    #[serde(default)]
    require_min_rd: bool,
    #[serde(default)]
    limit: i64,
}

/// `POST /api/v1/projects/{id}/rank/composite` — rank features across criteria, with the explanation attached.
///
/// The body is optional and an empty one is meaningful ("rank with every criterion's default"). Naming ANY weight
/// makes that list the whole query: an unnamed active criterion is excluded, not defaulted — the difference this
/// route's probe is built around.
async fn composite_rank(
    State(state): State<AppState>,
    Path(project_id): Path<i64>,
    headers: HeaderMap,
    body: Option<Json<CompositeRequest>>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project(&state, project_id, &headers).await?;
    let request = body.map(|Json(inner)| inner).unwrap_or_default();

    let mut weights: Vec<concord_ranking::composite::Weight> = Vec::new();
    if !request.weights.is_empty() {
        for (key, value) in &request.weights {
            // A non-numeric key is the caller's mistake, and the key is echoed so the mistake is actionable.
            let Ok(criterion_id) = key.parse::<i64>() else {
                return Err(ApiError::invalid_request(format!(
                    "weights keys must be criterion ids: {key}"
                )));
            };
            weights.push(concord_ranking::composite::Weight {
                criterion_id,
                weight: *value,
            });
        }
    } else if !request.profile.is_empty() {
        match concord_db::criteria::profile_weights(&state.pool, project.id, &request.profile)
            .await
            .map_err(internal)?
        {
            Some(saved) => weights = saved,
            // A profile that does not exist is a 404: an empty weight list would silently answer a typo with the
            // default ranking, which is a plausible wrong answer.
            None => return Err(ApiError::not_found(format!("profile {}", request.profile))),
        }
    }

    let criteria = concord_db::criteria::list(&state.pool, project.id)
        .await
        .map_err(internal)?;
    if criteria.is_empty() {
        return Ok(Json(serde_json::json!({ "results": [], "count": 0 })));
    }
    let ratings = concord_db::criteria::criterion_ratings(&state.pool, project.id)
        .await
        .map_err(internal)?;
    let mapped: Vec<concord_ranking::composite::Criterion> = criteria
        .iter()
        .map(|criterion| concord_ranking::composite::Criterion {
            id: criterion.id,
            slug: criterion.slug.clone(),
            name: criterion.name.clone(),
            direction: if criterion.direction == "lower_is_better" {
                concord_ranking::composite::Direction::Lower
            } else {
                concord_ranking::composite::Direction::Higher
            },
            default_weight: criterion.default_weight,
            active: criterion.active,
        })
        .collect();
    let mut results = concord_ranking::composite::composite(
        &mapped,
        &ratings,
        &weights,
        concord_ranking::composite::Options {
            include_inactive: request.include_inactive,
            require_min_rd: request.require_min_rd,
        },
    );
    if request.limit > 0 && i64::try_from(results.len()).unwrap_or(0) > request.limit {
        results.truncate(usize::try_from(request.limit).unwrap_or(usize::MAX));
    }
    // Confidence rides beside the rank so a client can tell a settled ordering from a provisional one without
    // re-deriving it from the contributions.
    let out: Vec<concord_ranking::composite::Ranked> = results
        .into_iter()
        .map(|result| concord_ranking::composite::Ranked {
            confidence: concord_ranking::composite::total_confidence(&result.contributions),
            result,
        })
        .collect();
    let count = out.len();
    Ok(Json(serde_json::json!({ "results": out, "count": count })))
}

/// `GET /api/v1/projects/{id}/arenas/{arena}/votes` — the arena's vote log, newest first, skips excluded.
///
/// No authentication and no role: an audit view that only insiders can read is not an audit view, and this is a read
/// of the data the leaderboard is computed from.
///
/// Arena ids are GLOBAL, so an arena belonging to another project answers exactly what a missing arena answers (404):
/// a distinct refusal would confirm the id is real.
async fn arena_votes(
    State(state): State<AppState>,
    Path((project_id, arena_id)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    // A non-numeric or unknown project segment is 404, never 400: the Go maps a parse failure to ErrNotFound, and a
    // distinct refusal would confirm which ids exist.
    let project = project_by_id(&state, &project_id).await?;
    let user = bearer_user(&state, &headers).await?;
    if !readable(&state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    let arena_id = match arena_id.parse::<i64>() {
        Ok(id) if id > 0 => id,
        // "invalid arena id" is the Go's 400, and it is a different answer from "no such arena".
        _ => return Err(ApiError::invalid_request("invalid arena id")),
    };
    let arena = concord_db::arena::find(&state.pool, arena_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such arena"))?;
    if arena.project_id != project.id {
        return Err(ApiError::not_found("no such arena"));
    }
    let votes = concord_db::arena::list_votes(&state.pool, arena_id, 50)
        .await
        .map_err(internal)?;
    let count = votes.len();
    Ok(Json(serde_json::json!({
        "arena_id": arena_id,
        "votes": votes,
        "count": count,
    })))
}

/// One competitor as a pair shows it: its label, its rating, and the chance it wins THIS comparison.
#[derive(Debug, serde::Serialize)]
struct PairSide {
    entity_type: String,
    entity_id: i64,
    label: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    slug: String,
    r: f64,
    rd: f64,
    conservative: f64,
    games: i64,
    is_baseline: bool,
    /// The win probability against the OTHER side, computed from the ratings BEFORE the vote — after it, the figure
    /// would be the thing being tested.
    #[serde(skip_serializing_if = "Option::is_none")]
    win_probability: Option<f64>,
}

/// `GET /projects/{id}/arenas/{arena}/next-pair` — "which comparison should I judge next?".
///
/// A signed-out reader has judged nothing, so the answer is the arena's most informative pair. A voter gets a pair they
/// have not already judged, skips included, because a skip means "I don't understand this" and not a verdict.
async fn arena_next_pair(
    State(state): State<AppState>,
    Path((project_id, arena_id)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = project_by_id(&state, &project_id).await?;
    let user = bearer_user(&state, &headers).await?;
    if !readable(&state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    let arena_id = match arena_id.parse::<i64>() {
        Ok(id) if id > 0 => id,
        _ => return Err(ApiError::invalid_request("invalid arena id")),
    };
    let arena = concord_db::arena::find(&state.pool, arena_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such arena"))?;
    if arena.project_id != project.id {
        return Err(ApiError::not_found("no such arena"));
    }
    let voter = user.as_ref().map(|who| who.id).unwrap_or(0);
    let pair = concord_db::arena::next_pair(&state.pool, arena_id, voter)
        .await
        .map_err(internal)?;
    let (a, b) = match pair {
        Ok(pair) => pair,
        // A data problem the project can fix is 400; a voter who has judged everything is 404, because the resource
        // they asked for does not exist for them.
        Err(concord_db::arena::NoPair::TooFewEntries) => {
            return Err(ApiError::invalid_request(
                "an arena needs at least two entries to compare",
            ))
        }
        Err(concord_db::arena::NoPair::AllJudged) => {
            return Err(ApiError::not_found("this voter has judged every pair"))
        }
    };
    let a_win = concord_ranking::win_probability(a.r, a.rd, b.r, b.rd);
    let b_win = concord_ranking::win_probability(b.r, b.rd, a.r, a.rd);
    let mut sides = Vec::with_capacity(2);
    for (entry, win) in [(&a, a_win), (&b, b_win)] {
        let (label, slug, baseline) =
            concord_db::arena::label_of(&state.pool, &arena.entity_type, entry.entity_id)
                .await
                .map_err(internal)?;
        sides.push(PairSide {
            entity_type: arena.entity_type.clone(),
            entity_id: entry.entity_id,
            label,
            slug,
            r: entry.r,
            rd: entry.rd,
            conservative: entry.conservative(),
            games: entry.games,
            is_baseline: entry.is_baseline || baseline,
            win_probability: Some(win),
        });
    }
    let b_side = sides.pop().unwrap_or(PairSide {
        entity_type: arena.entity_type.clone(),
        entity_id: 0,
        label: String::new(),
        slug: String::new(),
        r: 0.0,
        rd: 0.0,
        conservative: 0.0,
        games: 0,
        is_baseline: false,
        win_probability: None,
    });
    let a_side = sides.pop().unwrap_or(PairSide {
        entity_type: arena.entity_type.clone(),
        entity_id: 0,
        label: String::new(),
        slug: String::new(),
        r: 0.0,
        rd: 0.0,
        conservative: 0.0,
        games: 0,
        is_baseline: false,
        win_probability: None,
    });
    Ok(Json(serde_json::json!({
        "arena_id": arena.id,
        "type": arena.entity_type,
        "question": arena.question,
        "use_case": arena.use_case,
        "a": a_side,
        "b": b_side,
    })))
}

/// `GET /projects/{id}/arenas/{arena}/verification` — replay the vote log and report whether the stored ratings are
/// the ones the log produces.
///
/// **Writes nothing**, which is what makes it something any reader may run: §5.1 says "anyone can verify a ranking",
/// and a verification only the server may perform is the promise this exists to keep. The recompute route is the
/// repair; keeping them apart is the difference between publishing a check and publishing a button that silently
/// rewrites a ranking.
///
/// Every entry is checked, not the leaderboard's first page: a verification that silently covers a subset is the
/// failure it exists to catch.
async fn arena_verification(
    State(state): State<AppState>,
    Path((project_id, arena_id)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = project_by_id(&state, &project_id).await?;
    let user = bearer_user(&state, &headers).await?;
    if !readable(&state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    let arena_id = match arena_id.parse::<i64>() {
        Ok(id) if id > 0 => id,
        _ => return Err(ApiError::invalid_request("invalid arena id")),
    };
    let arena = concord_db::arena::find(&state.pool, arena_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such arena"))?;
    if arena.project_id != project.id {
        return Err(ApiError::not_found("no such arena"));
    }
    let entries = concord_db::arena::verify(&state.pool, arena_id)
        .await
        .map_err(internal)?;
    let divergent = entries.iter().filter(|entry| !entry.agrees).count();
    Ok(Json(serde_json::json!({
        "arena_id": arena.id,
        "agrees": divergent == 0,
        "divergent": divergent,
        "entries": entries,
    })))
}

/// How many solutions ONE feature's standings render.
///
/// Per feature, not per project: truncating a project's whole list would hide some features entirely, and truncating
/// within a feature hides that feature's runner-ups, which is the thing standings exist to show.
const STANDINGS_PER_FEATURE: i64 = 5;

/// `GET /projects/{id}/solutions` — the project page's solution standings, grouped per feature.
///
/// Two rules that are load-bearing and easy to "improve" into bugs:
///
/// * standings are **grouped per feature, never merged**. A solution's score is computed against the arena entries
///   competing with it, so a project-wide "best solution" ranks numbers that were never comparable — and a merged list
///   would let one feature's do-nothing baseline outrank another feature's real proposal;
/// * features with **no** solutions are included with an empty array and counted in `features_without`. "One feature has
///   proposals and four do not" is the useful sentence; a panel that omits the four reads as if one were the project.
async fn project_solutions(
    State(state): State<AppState>,
    Path(project_id): Path<i64>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project(&state, project_id, &headers).await?;
    // Status "" is every status: a shipped feature whose proposals were all withdrawn is still part of the roadmap, and
    // a standings panel that hides closed work is a panel that erases it.
    let features = concord_db::features::list_features(&state.pool, project.id, "")
        .await
        .map_err(internal)?;
    let mut rows = Vec::with_capacity(features.len());
    let mut with_solutions = 0;
    let mut without = 0;
    for feature in &features {
        let scores = concord_db::arena::list_solutions(&state.pool, feature.id, STANDINGS_PER_FEATURE)
            .await
            .map_err(internal)?;
        // The omitted count comes from an explicit COUNT, not from asking for limit+1 rows: the standings walk the
        // arena and skip entries whose solution row is gone, so "one more row" is not a witness of "one more solution".
        let total = concord_db::arena::count_solutions(&state.pool, feature.id)
            .await
            .map_err(internal)?;
        let shown = i64::try_from(scores.len()).unwrap_or(0);
        if shown == 0 {
            without += 1;
        } else {
            with_solutions += 1;
        }
        rows.push(serde_json::json!({
            "feature_id": feature.id,
            "feature_title": feature.title,
            // Never null: a feature nobody has proposed for is a normal state, and an empty array is the honest answer
            // where a 404 reads as breakage.
            "solutions": scores,
            "shown": shown,
            "omitted": total.saturating_sub(shown),
        }));
    }
    Ok(Json(serde_json::json!({
        "project_id": project.id,
        "features": rows,
        "features_with_solutions": with_solutions,
        "features_without": without,
    })))
}

/// `GET /projects/{id}/features/{feature}/solutions?limit=` — one feature's standings.
///
/// Three rules, and the second is the one that differs from its neighbours:
///
/// * a feature nobody has proposed for answers an **empty array**, not 404 — that is the normal state of a new feature;
/// * a feature belonging to **another project** answers **403**, not 404. This is the Go's choice on the solutions
///   family (`ErrPerm`), and it does not match the complaint family's 404 for the same situation: the two were written
///   on different days and the port reproduces each rather than harmonising them behind the service's back;
/// * `limit` defaults to **100** and only a positive integer overrides it — there is no upper cap here, unlike the
///   leaderboard's 200.
async fn feature_solutions(
    State(state): State<AppState>,
    Path((project_id, feature_id)): Path<(String, String)>,
    Query(params): Query<std::collections::HashMap<String, String>>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = project_by_id(&state, &project_id).await?;
    let user = bearer_user(&state, &headers).await?;
    if !readable(&state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    let feature_id = match feature_id.parse::<i64>() {
        Ok(id) => id,
        Err(_) => return Err(ApiError::invalid_request("invalid feature id")),
    };
    let feature = concord_db::features::feature(&state.pool, feature_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such feature"))?;
    if feature.project_id != project.id {
        // 403 here and 404 in the complaint routes: two families, two choices, both reproduced.
        return Err(ApiError::forbidden("feature belongs to another project"));
    }
    let limit = params
        .get("limit")
        .and_then(|text| text.parse::<i64>().ok())
        .filter(|value| *value > 0)
        .unwrap_or(100);
    let scores = concord_db::arena::list_solutions(&state.pool, feature_id, limit)
        .await
        .map_err(internal)?;
    // Never null: the store returns nothing for a feature with no arena, and a nil slice marshals to `null`, which the
    // page then has to special-case.
    Ok(Json(serde_json::json!({
        "feature_id": feature_id,
        "solutions": scores,
    })))
}

/// `GET /projects/{id}/features/{feature}/solutions/{solution}` — one proposal, its rating and its coverage claims.
///
/// The ownership rule: a solution is checked against the **feature in the path**, not only the project, and a mismatch is
/// **404** — not the 403 this same family answers for a feature from another project. Serving feature A's proposal under
/// feature B's URL would show the board a competitor that is not in it, and a distinct refusal would confirm the id.
async fn one_solution(
    State(state): State<AppState>,
    Path((project_id, feature_id, solution_id)): Path<(String, String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = project_by_id(&state, &project_id).await?;
    let user = bearer_user(&state, &headers).await?;
    if !readable(&state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    let Ok(feature_id) = feature_id.parse::<i64>() else {
        return Err(ApiError::invalid_request("invalid feature id"));
    };
    let feature = concord_db::features::feature(&state.pool, feature_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such feature"))?;
    if feature.project_id != project.id {
        return Err(ApiError::forbidden("feature belongs to another project"));
    }
    let Ok(solution_id) = solution_id.parse::<i64>() else {
        return Err(ApiError::invalid_request("invalid solution id"));
    };
    let solution = concord_db::arena::get_solution(&state.pool, solution_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("solution {solution_id}")))?;
    if solution.feature_id != feature_id {
        // 404, not the 403 the cross-project case above answers: two sizes of "not yours" in one family, both the Go's.
        return Err(ApiError::not_found(format!("solution {solution_id}")));
    }
    let coverage = concord_db::arena::list_coverage(&state.pool, solution_id)
        .await
        .map_err(internal)?;
    // A solution is the baseline by its TYPE, so the flag the arena stores and the flag this answers cannot disagree.
    let is_baseline = solution.kind == concord_db::arena::SOLUTION_DO_NOTHING;
    Ok(Json(serde_json::json!({
        "solution": solution,
        "coverage": coverage,
        "is_baseline": is_baseline,
    })))
}

/// The one guard every request route but the instance board shares: reach the project THROUGH the request row.
///
/// Three branches, each a bug that existed:
///
/// * a non-numeric id is **400** and a missing one **404**;
/// * a request with **no project** is readable by everyone — it belongs to the instance, so there is no project whose
///   visibility could hide it. The unported code called `GetProjectByID(0)` unconditionally and answered 404, which made
///   every instance-wide request unreadable through every route that resolves one;
/// * a project-scoped request whose project has been **deleted** is 404 rather than 500: the cascade should have taken
///   the request with it, and a row that outlived its project is not a request anybody may read.
async fn require_readable_request(
    state: &AppState,
    request_id: &str,
    headers: &HeaderMap,
) -> Result<concord_db::requests::Request, ApiError> {
    let Ok(request_id) = request_id.parse::<i64>() else {
        return Err(ApiError::invalid_request("invalid request id"));
    };
    let request = concord_db::requests::get(&state.pool, request_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("request {request_id}")))?;
    if request.project_id == 0 {
        return Ok(request);
    }
    let user = bearer_user(state, headers).await?;
    let project = concord_db::projects::project_by_id(&state.pool, request.project_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("request {request_id}")))?;
    if !readable(state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found(format!("request {request_id}")));
    }
    Ok(request)
}

/// `GET /projects/{id}/requests` — a project's requests, newest first.
async fn list_project_requests(
    State(state): State<AppState>,
    Path(project_id): Path<i64>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project(&state, project_id, &headers).await?;
    let rows = concord_db::requests::by_project(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

/// `GET /requests` — the instance-wide board: every request with no project.
///
/// Unguarded on purpose. A request with no project belongs to the instance, so there is no project whose visibility could
/// hide it; the absence of an access check here is the correct code, not an oversight.
async fn list_instance_requests(
    State(state): State<AppState>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let rows = concord_db::requests::instance_board(&state.pool)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

/// `GET /requests/{id}` — one request.
async fn one_request(
    State(state): State<AppState>,
    Path(request_id): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let request = require_readable_request(&state, &request_id, &headers).await?;
    Ok(Json(serde_json::json!(request)))
}

/// `GET /requests/{id}/answers` — its answers, best rating first, never null.
async fn request_answers(
    State(state): State<AppState>,
    Path(request_id): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let request = require_readable_request(&state, &request_id, &headers).await?;
    let rows = concord_db::requests::answers(&state.pool, request.id)
        .await
        .map_err(internal)?;
    // Never null: "no answers yet" is the normal state of a new request, and an empty array is the honest answer where
    // `null` forces the client to special-case it.
    Ok(Json(serde_json::json!(rows)))
}

/// `GET /requests/{id}/tags` — its tag names, alphabetical.
async fn request_tags(
    State(state): State<AppState>,
    Path(request_id): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let request = require_readable_request(&state, &request_id, &headers).await?;
    let tags = concord_db::requests::tags(&state.pool, request.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(tags)))
}

/// Map the accept rule's three refusals onto the three statuses that mean them.
fn refusal_error(refusal: concord_db::requests::Refusal, request_id: i64) -> ApiError {
    match refusal {
        concord_db::requests::Refusal::NotAuthor => {
            ApiError::forbidden("only the request's author may accept an answer")
        }
        concord_db::requests::Refusal::ForeignAnswer => {
            ApiError::not_found(format!("answer on request {request_id}"))
        }
        concord_db::requests::Refusal::NotActive(status) => {
            ApiError::invalid_request(format!("that answer is {status} and cannot be accepted"))
        }
    }
}

/// `PUT /requests/{id}/accept` — §17's badge. Actor from the bearer token, never from the body.
///
/// The author check runs FIRST, before the answer is looked up, so a stranger naming a nonexistent answer gets 403 rather
/// than 404. That order is the ported order and it is observable: the other order tells people who may not accept
/// anything which answer ids exist.
async fn accept_answer(
    State(state): State<AppState>,
    Path(request_id): Path<String>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to accept an answer"))?;
    let request = require_readable_request(&state, &request_id, &headers).await?;
    let answer_id = body
        .get("answer_id")
        .and_then(serde_json::Value::as_i64)
        .unwrap_or(0);
    concord_db::requests::accept(&state.pool, request.id, answer_id, user.id)
        .await
        .map_err(internal)?
        .map_err(|refusal| refusal_error(refusal, request.id))?;
    // The updated request back, so the caller sees the badge they just set — and can see that the ranking did not move.
    let updated = concord_db::requests::get(&state.pool, request.id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("request {}", request.id)))?;
    Ok(Json(serde_json::json!(updated)))
}

/// `DELETE /requests/{id}/accept` — take the badge back. Author-only, and unaffected by the answer's status: an accepted
/// answer that was later removed still leaves a badge, and the author must be able to clear it.
async fn unaccept_answer(
    State(state): State<AppState>,
    Path(request_id): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to unaccept an answer"))?;
    let request = require_readable_request(&state, &request_id, &headers).await?;
    concord_db::requests::unaccept(&state.pool, request.id, user.id)
        .await
        .map_err(internal)?
        .map_err(|refusal| refusal_error(refusal, request.id))?;
    let updated = concord_db::requests::get(&state.pool, request.id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("request {}", request.id)))?;
    Ok(Json(serde_json::json!(updated)))
}
