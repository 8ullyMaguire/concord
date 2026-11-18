//! S1's routes: projects, documents, the board, `/healthz`, `/api/v1/version`.
//!
//! The plan sketches a local `ApiError` enum here; the estate's `polaris_core::ApiError` is that
//! enum, already carrying the contract's status mapping and its refusal to serialize a code nobody
//! declared. Writing a second one would be the fifth copy the estate spent a review deleting, so
//! this module uses the crate's and the handlers only ever pick a constructor.
//!
//! A path under `/api` that is not a route answers the envelope with a 404, never a shell: the
//! service has no shell yet, and the rule outlives the frontend.

use axum::response::IntoResponse;
use axum::{
    extract::{Extension, Path, Query, State},
    http::{HeaderMap, StatusCode},
    routing::{get, post, put},
    Json, Router,
};
use serde::Deserialize;
pub mod api;

/// A deliberate defect reachable only by environment variable, so a probe's control does not cost a rebuild.
///
/// Crate-wide helper (D-38): several `api/*.rs` modules borrow it, so it stays
/// at the root even though it was first written inside the webhook section.
pub(crate) fn app_mutant(name: &str) -> bool {
    std::env::var("CONCORD_MUTANT")
        .unwrap_or_default()
        .split(',')
        .any(|candidate| candidate.trim() == name)
}
use api::identity::{
    acting_user, auth_gate, flatten_error_bodies, interaction_gate, presented_token,
    security_headers,
};
pub mod feeds;
pub mod finder;
pub mod policy;
pub mod rate;

use polaris_core::ApiError;
use serde_json::{json, Value};
use sqlx::PgPool;

/// What every handler needs. One field today; the pool is what makes a handler testable without a
/// service, which is how the slice's tests will call them.
/// The page routes read the pool and nothing else, so `AppState` hands them that one field rather than growing a second state type.
impl axum::extract::FromRef<AppState> for PgPool {
    fn from_ref(state: &AppState) -> Self {
        state.pool.clone()
    }
}

#[derive(Clone)]
pub struct AppState {
    pub pool: PgPool,
    /// The near-duplicate embedder. Configured from the environment, defaulting to the local hashed one, because a
    /// filing service that refuses to boot when a model server is down is worse than one that degrades.
    pub embedder: std::sync::Arc<dyn concord_embed::Embedder>,
    /// The per-client budget each route draws from. See `rate.rs` for why the class comes from the route.
    pub rate: std::sync::Arc<polaris_ratelimit::Limiter>,
}

/// Read the embedder configuration from the environment, exactly as the deployment sets it.
///
/// * `CONCORD_EMBED_URL` — the model server. Absent or empty means the local hashed embedder;
/// * `CONCORD_EMBED_MODEL` — the model name (`nomic-embed-text:latest` on this instance);
/// * `CONCORD_EMBED_TIMEOUT_MS` — one request's budget, 8000 on this instance. A cold model load can exceed it, and the
///   call then degrades to the hashed embedder — a degrade, not a failure;
/// * `CONCORD_EMBED_PREFIX` — prepended to every text. nomic was trained with `search_document: ` and scores worse
///   without it.
///
/// **Never falls back to "no embedder".** `§10.6` requires AI features to be opt-out-able and the filing path must keep
/// working with the model server down; the hashed embedder is always available and catches exact and morphological
/// duplicates.
#[must_use]
pub fn embedder_from_env() -> std::sync::Arc<dyn concord_embed::Embedder> {
    let endpoint = std::env::var("CONCORD_EMBED_URL").unwrap_or_default();
    let model = std::env::var("CONCORD_EMBED_MODEL").unwrap_or_default();
    if endpoint.is_empty() {
        return std::sync::Arc::new(concord_embed::Hashed::new());
    }
    let mut config = concord_embed::OllamaConfig::new(endpoint, model);
    if let Ok(raw) = std::env::var("CONCORD_EMBED_TIMEOUT_MS") {
        if let Ok(ms) = raw.parse::<u64>() {
            config.timeout = std::time::Duration::from_millis(ms);
        }
    }
    config.prefix = std::env::var("CONCORD_EMBED_PREFIX").unwrap_or_else(|_| "search_document: ".into());
    match concord_embed::Ollama::new(config) {
        Ok(embedder) => std::sync::Arc::new(embedder),
        // A misconfigured remote embedder is a degraded instance, not a dead one: the whole point of the fallback.
        Err(error) => {
            tracing::warn!(%error, "falling back to the local embedder");
            std::sync::Arc::new(concord_embed::Hashed::new())
        }
    }
}

/// The router, complete for this slice.
pub fn router(state: AppState) -> Router {
    Router::new()
        .merge(api::meta::routes())
        // Every page needs to know who it is looking at, and the Go's `/auth/me` is what tells it.
        .route("/api/v1/auth/me", get(auth_me))
        .route("/api/v1/projects/{project_id}/members", get(list_members))
        // The project page's panels: the Go nests these under `r.Route("/criteria", …)` and registers GET "/",
        // so both the bare path and the trailing-slash form answer — a client that appends a slash must not get
        // a 404.
        // Top-level `{project_id}`, NOT nested under the `{slug}` subtree: nested, axum binds the segment by its
        // own name and a handler reading a numeric id would get the slug — the Go comment records the same bug
        // chi had ("requireProjectID reads project_id, gets "", and every call 404s with project \"\"").
        .route("/api/v1/projects/{project_id}/capabilities", get(list_capabilities))
        // §4.7's write half. The read half has been routed since the port began, which is why its panel's "No field reports yet"
        // was not a state the instance happened to be in — it was the state the code guaranteed.
        .merge(api::field_reports::routes())
        .route("/api/v1/projects/{project_id}/complaints/{complaint}", get(get_complaint))
        .route("/api/v1/projects/{project_id}/complaints/{complaint}/features", get(complaint_features))
        .route("/api/v1/projects/{project_id}/lists/{list}", get(get_list))
        .route("/api/v1/projects/{project_id}/lists/{list}/", get(get_list))
        .route("/api/v1/projects/{project_id}/tallies", get(feature_tallies))
        .merge(api::records::routes())
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
        .merge(api::solutions::routes())
        // The complaint writes bind `{slug}` like every sibling route: axum refuses two routes that differ only in a
        // parameter's NAME, and the read side of this path is already registered as `{slug}`. The reference calls the
        // segment `project_id` while resolving it as a slug, so the shape is the same either way.
        .merge(api::complaints::routes())
        .route("/api/v1/requests/{request_id}", get(one_request))
        .merge(api::arena_votes::routes())
        .merge(api::coverage::routes())
        // §5.1's FEATURE vote: what to build, ranked in the project's priority arena. A different route from the solutions
        // vote on purpose — the two arenas rank different things, and a shared handler would infer which from the id.
        .merge(api::feature_votes::routes())
        // Invites: §8.2's trust mechanism — how a stranger becomes a member of a protected project.
        .merge(api::invites::routes())
        // §4/§5: Finder. Sessions live in memory and say so (`persisted: false`) — the reference's own decision, kept.
        .route("/api/v1/finder/questions", get(finder::finder_questions))
        // S2: alternatives arenas — the kind that ranks competing PROJECTS. The route names the owning project (a segment
        // that may be a slug or an id); the competitor in a body is a numeric id, and that asymmetry is the only confusing
        // thing about the shape.
        .merge(api::alternatives::routes())
        .route("/api/v1/finder/sessions", post(finder::start_finder_session))
        .route("/api/v1/finder/sessions/{session}", get(finder::get_finder_session))
        .route(
            "/api/v1/finder/sessions/{session}/answers",
            post(finder::answer_finder_question),
        )
        .route("/api/v1/finder/sessions/{session}/back", post(finder::back_finder))
        .route("/api/v1/finder/sessions/{session}/lift", post(finder::lift_finder))
        .route(
            "/api/v1/finder/sessions/{session}/results",
            get(finder::finder_results),
        )
        // §5.7's merge requests. `execute` is the destructive one: it ships the feature and closes its complaints, and its gate
        // lives in the store beside that change so no route can skip it.
        .merge(api::merge_requests::routes())
        // `{slug}` and not `{project_id}`: axum refuses two routes that differ only in a parameter NAME ("Insertion
        // failed due to conflict with previously registered route"), and the sibling feature routes under this
        // prefix already bind `{slug}`. The Go's shape is the same path — parameter names are not part of it.
        .route("/api/v1/projects/{slug}/features/{feature}/complaints", get(feature_complaints))
        // §6.5's call, at the REFERENCE's path as well as the port's `/solution-call`: a caller written against the Go
        // service asks for `/consensus`, and answering 404 to a path that means the same thing is a migration papercut
        // that costs a client author an afternoon. Two paths, one pair of handlers.
        .route(
            "/api/v1/projects/{slug}/features/{feature}/consensus",
            get(solution_call_readiness).post(open_solution_call),
        )
        .merge(api::catalog::routes())
        .merge(api::board::routes())
        .merge(api::accounts::routes())
        .merge(api::project_lifecycle::routes())
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
        .merge(api::notifications::routes())
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
        .merge(api::features::routes())
        .route(
            "/api/v1/projects/{slug}/features/{feature}/complaints",
            post(link_complaint),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/complaints/{complaint}",
            axum::routing::delete(unlink_complaint),
        )
        // A project's documents: written by a contributor, removed by a maintainer. The PUT is an upsert at
        // (kind, slug) that bumps `revision` rather than replacing the row, so links and comments keep resolving.
        // The contribution feed and the two reads under it. The feed ranks work ITEMS with the finder's own engine — it is
        // deliberately not a second finder — and the two project reads are what its page and the vote page need.
        // Three reads an operator page stands on. Batched because each is one read over a table the port already owns, and because
        // they share the rule that a missing measurement is not a zero and an empty set is not null.
        // The two destructive ops writes: an emergency power being lifted, and one complaint folded into another.
        .merge(api::milestones::routes())
        .merge(api::project_settings::routes())
        .merge(api::criteria::routes())
        .merge(api::lists::routes())
        .merge(api::feature_meta::routes())
        .merge(api::feature_work::routes())
        .merge(api::consensus::routes())
        .merge(api::requests::routes())
        .merge(api::answers::routes())
        .merge(api::comments::routes())
        .merge(api::emergency_holds::routes())
        .merge(api::ops_reads::routes())
        // §4.3's global taxonomy: the vocabulary that may not grow by being mentioned. Four routes, one cluster — the list is what
        // the suggestion UI reads, and a proposal is the only way a tag becomes real.
        // A badge is a page with numbers in it, read in someone else's README by people who will never visit this instance.
        // Mounted outside /api/v1 because it is fetched by image tags, not by fetch().
        .merge(api::badges::routes())
        // §3–§8: one read, no write. NOT scoped to a project — Scout reports on the catalogue as a whole, and access control is therefore
        // per candidate, inside the read.
        .merge(api::scout::routes())
        // The two writes nobody clicks: a rating replay over an arena, and a forge calling the instance.
        .merge(api::ops_writes::routes())
        .merge(api::forgejo_webhook::routes())
        .merge(api::taxonomy::routes())
        .merge(api::contributions::routes())
        .merge(api::documents::routes())
        // The identity cluster: a nameless visitor can act, can become an account in place, and can log in and out.
        .merge(api::identity::routes())
        // The gate is one layer rather than 184 handlers: consent and the guest cap are properties of every mutation, and a rule
        // copied into every handler is a rule that will be missing from the one written next month.
        // The page routes (row 12). Merged before the gate so a GET is not asked for consent — the gate refuses mutations, and a
        // reader who has not accepted the notice can still read.
        .merge(concord_web::router::<AppState>())
        // Inside the interaction gate, so an anonymous mutation still gets the consent refusal it gets today and
        // then the auth refusal, in that order. Authentication is a property of a route, declared in `policy` and
        // enforced here rather than remembered in 122 handler bodies.
        .layer(axum::middleware::from_fn_with_state(
            state.clone(),
            auth_gate,
        ))
        .layer(axum::middleware::from_fn_with_state(
            state.clone(),
            interaction_gate,
        ))
        .fallback(not_found)
        // LAST, so it sees the body every layer above produced — including the gate's own refusals.
        .layer(axum::middleware::map_response(flatten_error_bodies))
        // Outermost, and a plain `from_fn` rather than `map_response`: the headers must be on every response the service
        // can produce — the 404, the gate's refusals, an API error — not only on the ones a page renderer returns.
        .layer(axum::middleware::from_fn(security_headers))
        // Above the identity gates and below nothing else: a flood must be refused before it costs a session lookup or a
        // database round trip, and a refusal must not depend on who is calling.
        .layer(axum::middleware::from_fn_with_state(state.clone(), rate::rate_gate))
        .with_state(state)
}

/// Any other path: the envelope, with a 404. `/api/**` is never a shell and neither is anything else
/// here yet — an SPA fallback belongs with the frontend slice, not with the API.
async fn not_found() -> ApiError {
    ApiError::not_found("no such route")
}

/// One place where a database failure becomes a refusal. The cause is logged and not answered: an
/// operator gets the trace, a caller gets "internal".
pub(crate) fn internal(error: impl std::fmt::Display) -> ApiError {
    tracing::error!(error = %error, "internal");
    ApiError::internal("internal error")
}


/// The token note the Go handler returns with a fresh token, verbatim: it tells the caller the one
/// thing about the token that matters, and a paraphrase would be a different sentence in the same
/// field.
const TOKEN_NOTE: &str = "Store this token. It cannot be retrieved again; only its hash is kept.";

/// The user behind `Authorization: Bearer …`, or `None` when there is no usable token.
///
/// One helper rather than a middleware: the port has routes that treat an absent identity as an
/// error and routes that treat it as an anonymous read, and a layer that decided for both would be
/// wrong for one of them.
pub(crate) async fn bearer_user(
    state: &AppState,
    headers: &HeaderMap,
) -> Result<Option<concord_db::auth::User>, ApiError> {
    // Through `presented_token` rather than reading the header again: the cookie is a credential, and a handler that only
    // understood the header would refuse a browser that has the cookie and no reason to keep a token in `localStorage`.
    let Some(token) = presented_token(headers) else {
        return Ok(None);
    };
    concord_db::auth::user_for_token(&state.pool, &token)
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
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<LinkComplaintRequest>,
) -> Result<Json<Value>, ApiError> {
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
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<Json<Value>, ApiError> {
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

/// Resolve a project by slug, or the 404 envelope.
pub(crate) async fn project_for(
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
/// A project from a path segment that may be an **id or a slug**.
///
/// The reference's own page scripts build every one of these URLs from the SLUG — `comments.js` asks for
/// `/api/v1/projects/{slug}/threads/{kind}/{id}` — and the reference answers it: probed against the live instance
/// (`/api/v1/projects/suki/threads/project/1` → `200 []`). The port answered `400 project id must be an integer`, which turned every
/// thread on every project page into "Comments could not be loaded" without any route being missing. That is what an inventory cannot
/// see: the route existed, the shape of its argument did not.
///
/// A non-numeric segment is therefore looked up as a slug, and a numeric one as an id first and then a slug — a project whose slug is
/// `2024` is not something to fail over.
pub(crate) async fn project_by_id(
    state: &AppState,
    raw: &str,
) -> Result<concord_db::projects::Project, ApiError> {
    if let Ok(id) = raw.parse::<i64>() {
        if let Some(project) = concord_db::projects::project_by_id(&state.pool, id).await.map_err(internal)? {
            return Ok(project);
        }
    }
    if let Some(project) = concord_db::projects::project_by_slug(&state.pool, raw)
        .await
        .map_err(internal)?
    {
        return Ok(project);
    }
    Err(ApiError::not_found(format!("project {raw}")))
}

/// Parse a path id, naming the segment that was wrong.
pub(crate) fn parse_id(raw: &str, what: &str) -> Result<i64, ApiError> {
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
struct ResolveObjectionRequest {
    status: String,
    resolution: String,
}

/// `PUT /api/v1/projects/{slug}/consensus/objections/{id}/resolve` — the objection's written answer.
async fn resolve_consensus_objection(
    State(state): State<AppState>,
    Path((slug, objection)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<ResolveObjectionRequest>,
) -> Result<Json<Value>, ApiError> {
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
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<(axum::http::StatusCode, Json<Value>), ApiError> {
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
///
/// **201, not 200.** The reference answers `writeJSON(w, http.StatusCreated, pos)` here, and its script treats anything else as a
/// failure; the port answered 200 with a status sentence, so a vote that was recorded correctly reported itself as refused. The body is
/// the stance, as the reference sends it, rather than a `{"status": "ok"}` envelope the client has no use for.
async fn cast_override_position(
    State(state): State<AppState>,
    Path((slug, call)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<OverridePositionRequest>,
) -> Result<(axum::http::StatusCode, Json<Value>), ApiError> {
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
    Ok((
        axum::http::StatusCode::CREATED,
        Json(json!({"position": body.position, "reason": body.reason})),
    ))
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
    Extension(user): Extension<concord_db::auth::User>,
    body: Option<Json<OverrideCloseRequest>>,
) -> Result<Json<Value>, ApiError> {
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
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<HoldRequest>,
) -> Result<(axum::http::StatusCode, Json<Value>), ApiError> {
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
/// `GET /api/v1/projects/{slug}/charter` — the charter a project's decisions are made with.
///
/// The segment is an **id or a slug**, like every other project segment: `settings.js` asks for
/// `/api/v1/projects/{slug}/charter`, and this handler parsed the segment as an integer, so the settings page's whole body — quorum, vote
/// window, WIP limits — was a `400 project id must be an integer` in a page that looked empty. Fourth route in this family to fail that way;
/// the fix is the shared resolver rather than a fifth local parse.
async fn get_charter(
    State(state): State<AppState>,
    Path(project): Path<String>,
) -> Result<Json<Value>, ApiError> {
    let project_id = project_by_id(&state, &project).await?.id;
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
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<CallOutcomeRequest>,
) -> Result<Json<Value>, ApiError> {
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
    /// §6.5's discretionary marker, as the reference sends it.
    #[serde(default)]
    early: bool,
    /// The stated reason, as the reference sends it. Required when `early` is true and refused when it is blank — an
    /// early call with no reason is an override that looks like a decision.
    #[serde(default)]
    reason: String,
    /// The port's own older spelling of the same two fields, kept because a client that has been calling
    /// `/solution-call` since before this alias existed should not have to change its body to keep working.
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
    Extension(user): Extension<concord_db::auth::User>,
    body: Option<Json<SolutionCallRequest>>,
) -> Result<(StatusCode, Json<Value>), ApiError> {
    let project = project_for(&state, &slug).await?;
    let feature = feature_for(&state, project.id, &feature).await?;
    let body = body.map(|Json(body)| body).unwrap_or_default();

    // §6.5's conditions are checked by the STORE, next to the write, in the same function the readiness page reads. They
    // used to be re-derived here, which is two answers to "may this be called?" — and the second one is the one that goes
    // stale the first time a threshold is tuned.
    // `early: true` is the reference's marker and takes its own `reason`, which the store refuses when blank. The port's
    // older `{early_reason}` means the same thing, so a client written against `/solution-call` keeps working.
    let (early, reason) = if body.early {
        (true, body.reason.trim().to_string())
    } else if !body.early_reason.trim().is_empty() {
        (true, body.early_reason.trim().to_string())
    } else {
        (false, String::new())
    };
    // A caller naming a specific solution is asking for a call the arena may not have led with; that is a request-shape
    // complaint about an id the caller supplied, so it belongs here rather than among the conditions.
    if body.solution_id != 0 {
        let readiness = concord_db::arena::readiness(&state.pool, feature.id)
            .await
            .map_err(internal)?;
        if readiness.leader_id == 0 {
            return Err(ApiError::invalid_request(
                "no solution leads this feature's arena, so there is nothing to call".to_string(),
            ));
        }
        if body.solution_id != readiness.leader_id {
            return Err(ApiError::invalid_request(format!(
                "solution {} is not the leader ({} leads this arena)",
                body.solution_id, readiness.leader_id
            )));
        }
    }
    let opened = if !early {
        concord_db::consensus_writes::open_solution_call_on_merit(&state.pool, project.id, feature.id, user.id)
            .await
            .map_err(write_error)?
    } else {
        // §6.5's discretionary path is a COLLABORATOR's discretion: a guest cannot open a call early. Checked here because
        // the merit path's own conditions are the gate for opening at all, and a role is not one of them.
        require_role(&state, &project, "contributor", &user).await?;
        concord_db::consensus_writes::open_solution_call_early(
            &state.pool,
            project.id,
            feature.id,
            user.id,
            &reason,
        )
        .await
        .map_err(write_error)?
    };
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
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<ArenaActRequest>,
) -> Result<(StatusCode, Json<Value>), ApiError> {
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
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<ArenaActRequest>,
) -> Result<Json<Value>, ApiError> {
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
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<CurationVoteRequest>,
) -> Result<(StatusCode, Json<Value>), ApiError> {
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
        Refused::Invalid(detail) => ApiError::invalid_request(detail),
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
    user: &concord_db::auth::User,
) -> Result<StrategyCaller, ApiError> {
    // The identity comes from auth_gate; this helper keeps only project existence and the member role lookup.
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
    Ok(StrategyCaller {
        user: user.clone(),
        role,
    })
}

async fn list_weight_proposals(
    State(state): State<AppState>,
    Path(project): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The segment may be an id or a slug: the reference's page scripts build these URLs from the SLUG, and a path extractor typed
    // `i64` rejected the slug before any handler ran (`Invalid URL: Cannot parse `suki` to a `i64``). One resolver, asked once, at
    // the top of the handler — the `project_id` the body uses is its id.
    let project_id = project_by_id(&state, &project).await?.id;
    strategy_project(&state, project_id, &user).await?;
    let rows = concord_db::strategy::proposals(&state.pool, project_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

async fn list_theme_proposals(
    State(state): State<AppState>,
    Path(project): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The segment may be an id or a slug: the reference's page scripts build these URLs from the SLUG, and a path extractor typed
    // `i64` rejected the slug before any handler ran (`Invalid URL: Cannot parse `suki` to a `i64``). One resolver, asked once, at
    // the top of the handler — the `project_id` the body uses is its id.
    let project_id = project_by_id(&state, &project).await?.id;
    strategy_project(&state, project_id, &user).await?;
    let rows = concord_db::themes::proposals(&state.pool, project_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

async fn list_themes(
    State(state): State<AppState>,
    Path(project): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The segment may be an id or a slug: the reference's page scripts build these URLs from the SLUG, and a path extractor typed
    // `i64` rejected the slug before any handler ran (`Invalid URL: Cannot parse `suki` to a `i64``). One resolver, asked once, at
    // the top of the handler — the `project_id` the body uses is its id.
    let project_id = project_by_id(&state, &project).await?.id;
    strategy_project(&state, project_id, &user).await?;
    let rows = concord_db::themes::themes(&state.pool, project_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

async fn propose_weight(
    State(state): State<AppState>,
    Path(project_id): Path<i64>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<serde_json::Value>,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
    let who = strategy_project(&state, project_id, &user).await?;
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
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<serde_json::Value>,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
    let who = strategy_project(&state, project_id, &user).await?;
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
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
    let who = strategy_project(&state, project_id, &user).await?;
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
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
    let who = strategy_project(&state, project_id, &user).await?;
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
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<StatusCode, ApiError> {
    let who = strategy_project(&state, project_id, &user).await?;
    let maintainer = who.role == "maintainer" || who.role == "owner";
    concord_db::themes::withdraw(&state.pool, id, who.user.id, maintainer)
        .await
        .map_err(theme_refusal)?;
    Ok(StatusCode::NO_CONTENT)
}

async fn withdraw_weight(
    State(state): State<AppState>,
    Path((project_id, id)): Path<(i64, i64)>,
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<StatusCode, ApiError> {
    let who = strategy_project(&state, project_id, &user).await?;
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
pub(crate) async fn readable(
    state: &AppState,
    project: &concord_db::projects::Project,
    user: Option<&concord_db::auth::User>,
) -> Result<bool, ApiError> {
    // One implementation, in the store: `projects::readable_for` is also what the page asks before it renders a 200, and two copies of a
    // visibility rule is how the page and the API come to disagree about whether a private project exists.
    concord_db::projects::readable_for(&state.pool, project, user.map(|user| user.id))
        .await
        .map_err(internal)
}

/// `GET /api/v1/auth/me` — the caller, by id: the token carries only an id, so the name is looked up here.
async fn auth_me(
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<Json<serde_json::Value>, ApiError> {
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
    Path(project): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The segment may be an id or a slug: the reference's page scripts build these URLs from the SLUG, and a path extractor typed
    // `i64` rejected the slug before any handler ran (`Invalid URL: Cannot parse `suki` to a `i64``). One resolver, asked once, at
    // the top of the handler — the `project_id` the body uses is its id.
    let project_id = project_by_id(&state, &project).await?.id;
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
    Path(project): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The segment may be an id or a slug: the reference's page scripts build these URLs from the SLUG, and a path extractor typed
    // `i64` rejected the slug before any handler ran (`Invalid URL: Cannot parse `suki` to a `i64``). One resolver, asked once, at
    // the top of the handler — the `project_id` the body uses is its id.
    let project_id = project_by_id(&state, &project).await?.id;
    let project = readable_project(&state, project_id, &headers).await?;
    let rows = concord_db::criteria::list(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

/// `GET /api/v1/projects/{id}/criteria-profiles`
async fn list_criteria_profiles(
    State(state): State<AppState>,
    Path(project): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The segment may be an id or a slug: the reference's page scripts build these URLs from the SLUG, and a path extractor typed
    // `i64` rejected the slug before any handler ran (`Invalid URL: Cannot parse `suki` to a `i64``). One resolver, asked once, at
    // the top of the handler — the `project_id` the body uses is its id.
    let project_id = project_by_id(&state, &project).await?.id;
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

/// A project by id, refused with a 404 the caller cannot use to enumerate private projects.
///
/// 404 and not 403 on purpose: a 403 tells an anonymous caller that the id exists, which is the enumeration
/// this catalogue's private projects are meant to prevent.
pub(crate) async fn readable_project(
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


/// Resolve a `{project_id}` path segment, which the reference reads as a **slug** whatever the parameter is called.
///
/// Go's `requireProjectID` calls `GetProject(slug)` on a parameter named `project_id`, and this module's earlier routes
/// took a numeric id. Both are accepted here — slug first, because that is the service's behaviour, then a numeric id,
/// because that is this router's existing convention — and a segment matching neither is 404 with no distinction between
/// "does not exist" and "not yours".
pub(crate) async fn readable_project_segment(
    state: &AppState,
    segment: &str,
    headers: &HeaderMap,
) -> Result<concord_db::projects::Project, ApiError> {
    let by_slug = concord_db::projects::project_by_slug(&state.pool, segment)
        .await
        .map_err(internal)?;
    let project = match by_slug {
        Some(project) => project,
        None => {
            let Ok(id) = segment.parse::<i64>() else {
                return Err(ApiError::not_found("no such project"));
            };
            concord_db::projects::project_by_id(&state.pool, id)
                .await
                .map_err(internal)?
                .ok_or_else(|| ApiError::not_found("no such project"))?
        }
    };
    let user = bearer_user(state, headers).await?;
    if !readable(state, &project, user.as_ref()).await? {
        return Err(ApiError::not_found("no such project"));
    }
    Ok(project)
}

/// Write one audit row.
///
/// `project` is `None` for an instance-wide act: the column is nullable and a fabricated project id would misfile the
/// entry under whichever project happened to be first.
async fn write_audit(
    state: &AppState,
    project: Option<i64>,
    actor: i64,
    action: &str,
    entity: &str,
    entity_id: i64,
    detail: &str,
) -> Result<(), ApiError> {
    sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1, $2, $3, $4, $5, $6, now())",
    )
    .bind(project)
    .bind(actor)
    .bind(action)
    .bind(entity)
    .bind(entity_id)
    .bind(detail)
    .execute(&state.pool)
    .await
    .map_err(internal)?;
    Ok(())
}

/// `GET /api/v1/projects/{id}/capabilities` — the whole catalogue, each entry with this project's claim.
async fn list_capabilities(
    State(state): State<AppState>,
    Path(project): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The segment may be an id or a slug: the reference's page scripts build these URLs from the SLUG, and a path extractor typed
    // `i64` rejected the slug before any handler ran (`Invalid URL: Cannot parse `suki` to a `i64``). One resolver, asked once, at
    // the top of the handler — the `project_id` the body uses is its id.
    let project_id = project_by_id(&state, &project).await?.id;
    let project = readable_project(&state, project_id, &headers).await?;
    let panel = concord_db::capabilities::panel(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(panel)))
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
    // A non-numeric project segment is a 404 and not a 400, and the segment may be an id OR a slug: `complaint.js` asks for
    // `/api/v1/projects/{slug}/complaints/{id}` and the reference answers it, while every page script builds its URLs from the slug. This
    // handler was the last one still parsing the segment as an integer, so the complaint page rendered "No such complaint" for a complaint
    // that exists — the third route in this family to fail that way, and the reason `project_by_id` now resolves both forms instead of
    // each handler deciding for itself.
    let project = project_by_id(&state, &project_id)
        .await
        .map_err(|_| ApiError::not_found("no such project"))?;
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

/// A project from a path segment that may be an **id or a slug**, readable by the caller, refused with a 404 the caller cannot use to
/// enumerate.
///
/// The doc here used to say "NUMERIC id" and justify it from the reference's `requireProjectID`, which maps a parse failure to
/// `ErrNotFound`. **Measured otherwise**: the live reference answers `/api/v1/projects/suki/features/2128/complaints` with `200` and the
/// same path with `74` in place of `suki` with `404 No such API route.` — the slug is the accepted form, because the page scripts build
/// every one of these URLs from the slug. Requiring an integer turned the feature page's complaint list into a permanent 404 while every
/// route in the inventory was answered. A non-numeric segment is still a 404 and not a 400 — an unknown project must not answer
/// differently from a malformed one — but it is now looked up before it is refused.
async fn numeric_project(
    state: &AppState,
    segment: &str,
    headers: &HeaderMap,
) -> Result<concord_db::projects::Project, ApiError> {
    let project = project_by_id(state, segment)
        .await
        .map_err(|_| ApiError::not_found("no such project"))?;
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
    Path(project): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The segment may be an id or a slug: the reference's page scripts build these URLs from the SLUG, and a path extractor typed
    // `i64` rejected the slug before any handler ran (`Invalid URL: Cannot parse `suki` to a `i64``). One resolver, asked once, at
    // the top of the handler — the `project_id` the body uses is its id.
    let project_id = project_by_id(&state, &project).await?.id;
    let project = readable_project(&state, project_id, &headers).await?;
    let tallies = concord_db::tallies::tallies(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(tallies)))
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
    Path(project): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The segment may be an id or a slug: the reference's page scripts build these URLs from the SLUG, and a path extractor typed
    // `i64` rejected the slug before any handler ran (`Invalid URL: Cannot parse `suki` to a `i64``). One resolver, asked once, at
    // the top of the handler — the `project_id` the body uses is its id.
    let project_id = project_by_id(&state, &project).await?.id;
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
    // WRAPPED, because that is the contract `requests.js` was written to and documents at the top of the file:
    // `GET /api/v1/requests/{id}/tags -> {tags: [...]}`. It read `j.tags || []` and this route answered a bare array, so every request
    // page said "No constraints recorded" no matter what the database held — the tags were there and unreadable.
    Ok(Json(serde_json::json!({ "tags": tags })))
}

// ── complaint writes (§M1) ──────────────────────────────────────────────────────────────────────────────────────────

/// The role ladder, as the reference orders it. A `guest` is below a plain `user`, and `owner` is above `maintainer`.
fn role_rank(role: &str) -> u8 {
    match role {
        "owner" => 5,
        "maintainer" => 4,
        "reviewer" => 3,
        "contributor" => 2,
        "user" => 1,
        _ => 0,
    }
}

/// Require at least `min` in this project, 403 otherwise.
///
/// An unknown role ranks 0 and an unknown `min` defaults to `contributor`, which is the reference's behaviour and the safe
/// direction: a typo in a minimum must not open a gate.
pub(crate) async fn require_role(
    state: &AppState,
    project: &concord_db::projects::Project,
    min: &str,
    user: &concord_db::auth::User,
) -> Result<(), ApiError> {
    let role = concord_db::projects::member_role(&state.pool, project.id, user.id)
        .await
        .map_err(internal)?
        .unwrap_or_else(|| "guest".to_string());
    if role_rank(&role) < role_rank(min) {
        return Err(ApiError::forbidden(format!(
            "this needs the {min} role in this project"
        )));
    }
    Ok(())
}

/// Resolve the complaint half of `/projects/{slug}/complaints/{id}/…` and prove it belongs to the project in the path.
///
/// **The two halves used to be unconnected.** Every handler under this path read only `{id}`, and a complaint id is
/// global — so any signed-in account could reach any complaint on the instance through its own project's URL, and a role
/// check written against the URL's project would have been checking a project the complaint had nothing to do with.
///
/// The refusal is **404, never 403**: a 403 confirms that the id is real. A non-numeric id is also 404 here, which is the
/// reference's choice for these routes (its generic complaint-id path answers 400 instead) and is preserved rather than
/// harmonised, because a client that branches on the difference exists.
async fn caller_complaint(
    state: &AppState,
    project: &concord_db::projects::Project,
    raw_id: &str,
) -> Result<concord_db::complaints::Complaint, ApiError> {
    let Ok(id) = raw_id.parse::<i64>() else {
        return Err(ApiError::not_found("no such complaint"));
    };
    let complaint = concord_db::complaints::complaint(&state.pool, id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such complaint"))?;
    if complaint.project_id != project.id {
        return Err(ApiError::not_found("no such complaint"));
    }
    Ok(complaint)
}

/// Map a complaint refusal: a missing row is 404, and every "not applicable" verdict is 400 because the id was real.
fn complaint_refusal(refusal: concord_db::complaints::Refused) -> ApiError {
    match refusal {
        concord_db::complaints::Refused::NotFound => ApiError::not_found("no such complaint"),
        concord_db::complaints::Refused::Invalid(reason) => ApiError::invalid_request(reason),
    }
}

/// Resolve `{feature}` under `{slug}`, refusing a feature that belongs to another project with **404**.
pub(crate) async fn caller_feature(
    state: &AppState,
    project: &concord_db::projects::Project,
    feature: &str,
) -> Result<i64, ApiError> {
    let id: i64 = feature
        .parse()
        .map_err(|_| ApiError::not_found("no such feature"))?;
    let owner: Option<i64> = sqlx::query_scalar("select project_id from features where id = $1")
        .bind(id)
        .fetch_optional(&state.pool)
        .await
        .map_err(internal)?;
    if owner != Some(project.id) {
        return Err(ApiError::not_found("no such feature in this project"));
    }
    Ok(id)
}
