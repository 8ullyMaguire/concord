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
        .route("/api/v1/projects/{project_id}/members", get(list_members))
        // The project page's panels: the Go nests these under `r.Route("/criteria", …)` and registers GET "/",
        // so both the bare path and the trailing-slash form answer — a client that appends a slash must not get
        // a 404.
        // Top-level `{project_id}`, NOT nested under the `{slug}` subtree: nested, axum binds the segment by its
        // own name and a handler reading a numeric id would get the slug — the Go comment records the same bug
        // chi had ("requireProjectID reads project_id, gets "", and every call 404s with project \"\"").
        // §4.7's write half. The read half has been routed since the port began, which is why its panel's "No field reports yet"
        // was not a state the instance happened to be in — it was the state the code guaranteed.
        .merge(api::field_reports::routes())
        .merge(api::records::routes())
        .merge(api::solutions::routes())
        // The complaint writes bind `{slug}` like every sibling route: axum refuses two routes that differ only in a
        // parameter's NAME, and the read side of this path is already registered as `{slug}`. The reference calls the
        // segment `project_id` while resolving it as a slug, so the shape is the same either way.
        .merge(api::complaints::routes())
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
        // §6.5's call, at the REFERENCE's path as well as the port's `/solution-call`: a caller written against the Go
        // service asks for `/consensus`, and answering 404 to a path that means the same thing is a migration papercut
        // that costs a client author an afternoon. Two paths, one pair of handlers.
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
        // §5.1/§6.2's strategy surface, at the GO's own paths: the frontend reads
        // `/api/v1/projects/{id}/strategy/themes` (settings.js), so a port that only answered the curation queue
        // would leave the page blank with a 404 while every probe passed.
        .merge(api::capabilities::routes())
        .merge(api::curation::routes())
        .merge(api::notifications::routes())
        .merge(api::audit::routes())
        .merge(api::priorities::routes())
        .merge(api::features::routes())
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
        .merge(api::moderation::routes())
        .merge(api::stats::routes())
        .merge(api::strategy::routes())
        .merge(api::solution_calls::routes())
        .merge(api::consensus_outcomes::routes())
        .merge(api::overrides::routes())
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
