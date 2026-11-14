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
    extract::{DefaultBodyLimit, Path, Query, State},
    http::{HeaderMap, StatusCode},
    routing::{get, post, put},
    Json, Router,
};
use serde::Deserialize;
pub mod feeds;
pub mod finder;

use polaris_core::ApiError;
use serde_json::{json, Value};
use sqlx::PgPool;

/// What every handler needs. One field today; the pool is what makes a handler testable without a
/// service, which is how the slice's tests will call them.
#[derive(Clone)]
pub struct AppState {
    pub pool: PgPool,
    /// The near-duplicate embedder. Configured from the environment, defaulting to the local hashed one, because a
    /// filing service that refuses to boot when a model server is down is worse than one that degrades.
    pub embedder: std::sync::Arc<dyn concord_embed::Embedder>,
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
        .route("/healthz", get(healthz))
        .route("/api/v1/healthz", get(healthz))
        .route("/api/v1/version", get(version))
        // Every page needs to know who it is looking at, and the Go's `/auth/me` is what tells it.
        .route("/api/v1/auth/me", get(auth_me))
        .route(
            "/api/v1/projects/{slug}/tags",
            get(project_tags).put(apply_project_tags),
        )
        .route("/api/v1/projects/{slug}/tags/completeness", get(tag_completeness))
        .route("/api/v1/projects/{project_id}/members", get(list_members))
        // The project page's panels: the Go nests these under `r.Route("/criteria", …)` and registers GET "/",
        // so both the bare path and the trailing-slash form answer — a client that appends a slash must not get
        // a 404.
        .route(
            "/api/v1/projects/{project_id}/criteria",
            get(list_criteria).post(create_criterion),
        )
        .route("/api/v1/projects/{project_id}/criteria/", get(list_criteria))
        .route(
            "/api/v1/projects/{project_id}/criteria-profiles",
            get(list_criteria_profiles).post(save_criteria_profile),
        )
        .route("/api/v1/projects/{project_id}/criteria/{criterion}/active", put(set_criterion_active))
        .route("/api/v1/projects/{project_id}/criteria/{criterion}/vote", post(cast_criterion_vote))
        .route("/api/v1/projects/{project_id}/criteria-profiles/", get(list_criteria_profiles))
        .route(
            "/api/v1/projects/{slug}/languages",
            get(project_languages).put(set_project_languages),
        )
        .route("/api/v1/projects/{project_id}/intake-mode", put(set_intake_mode))
        // Top-level `{project_id}`, NOT nested under the `{slug}` subtree: nested, axum binds the segment by its
        // own name and a handler reading a numeric id would get the slug — the Go comment records the same bug
        // chi had ("requireProjectID reads project_id, gets "", and every call 404s with project \"\"").
        .route("/api/v1/projects/{project_id}/capabilities", get(list_capabilities))
        .route("/api/v1/projects/{project_id}/field-reports", get(list_field_reports))
        .route("/api/v1/projects/{project_id}/complaints/{complaint}", get(get_complaint))
        .route("/api/v1/projects/{project_id}/complaints/{complaint}/features", get(complaint_features))
        .route(
            "/api/v1/projects/{project_id}/milestones/{milestone}",
            // `{project_id}` and `{milestone}`, not `{slug}`/`{id}`: axum refuses two routes that differ only in a
            // parameter's NAME, and these five writes share the path with this read.
            get(get_milestone).patch(update_milestone),
        )
        .route(
            "/api/v1/projects/{project_id}/milestones/{milestone}/status",
            put(set_milestone_status),
        )
        .route(
            "/api/v1/projects/{project_id}/milestones/{milestone}/close",
            post(close_milestone),
        )
        .route(
            "/api/v1/projects/{project_id}/milestones/{milestone}/features",
            post(assign_milestone_features),
        )
        .route(
            "/api/v1/projects/{project_id}/milestones/{milestone}/order",
            put(reorder_milestone),
        )
        .route("/api/v1/projects/{project_id}/lists/{list}", get(get_list))
        .route("/api/v1/projects/{project_id}/lists/{list}/", get(get_list))
        .route(
            "/api/v1/projects/{project_id}/lists/{list}/entries",
            get(get_list_entries).post(create_list_entry),
        )
        .route(
            "/api/v1/projects/{project_id}/lists/{list}/entries/vote",
            post(vote_list_entry),
        )
        .route("/api/v1/projects/{slug}/metrics", put(set_project_metrics))
        .route("/api/v1/projects/{slug}/visibility", put(set_project_visibility))
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
            "/api/v1/requests",
            get(list_instance_requests).post(create_instance_request),
        )
        .route("/api/v1/similar/{kind}", get(find_similar))
        .route(
            "/api/v1/projects/{project_id}/requests",
            get(list_project_requests).post(create_project_request),
        )
        // The complaint writes bind `{slug}` like every sibling route: axum refuses two routes that differ only in a
        // parameter's NAME, and the read side of this path is already registered as `{slug}`. The reference calls the
        // segment `project_id` while resolving it as a slug, so the shape is the same either way.
        .route(
            "/api/v1/projects/{slug}/complaints",
            get(list_complaints).post(create_complaint),
        )
        .route(
            "/api/v1/projects/{slug}/complaints/{id}/impact",
            post(add_complaint_impact),
        )
        .route(
            "/api/v1/projects/{slug}/complaints/{id}/validate",
            post(validate_complaint),
        )
        .route(
            "/api/v1/projects/{slug}/complaints/{id}/reject",
            post(reject_complaint),
        )
        .route(
            "/api/v1/projects/{slug}/complaints/{id}/merge/{target_id}",
            post(merge_complaints),
        )
        .route("/api/v1/requests/{request_id}", get(one_request))
        .route(
            "/api/v1/requests/{request_id}/accept",
            put(accept_answer).delete(unaccept_answer),
        )
        .route(
            "/api/v1/requests/{request_id}/answers",
            get(request_answers).post(answer_request),
        )
        .route(
            "/api/v1/requests/{request_id}/tags",
            get(request_tags).put(set_request_tags),
        )
        .route("/api/v1/answers/{request_id}/vote", post(vote_answer))
        .route(
            "/api/v1/projects/{project_id}/features/{id}/solutions",
            get(feature_solutions).post(create_solution),
        )
        .route(
            "/api/v1/projects/{project_id}/features/{id}/solutions/vote",
            post(vote_solutions),
        )
        .route(
            "/api/v1/projects/{project_id}/features/{id}/solutions/{solution_id}/coverage",
            post(claim_coverage),
        )
        .route(
            "/api/v1/projects/{project_id}/features/{id}/solutions/{solution_id}/coverage/{complaint_id}/contest",
            post(contest_coverage),
        )
        .route(
            "/api/v1/projects/{project_id}/features/{id}/solutions/{solution_id}",
            get(one_solution),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/deps",
            get(feature_deps).post(add_feature_dep).delete(remove_feature_dep),
        )
        .route("/api/v1/projects/{slug}/features/{feature}/evidence", get(feature_evidence))
        // §5.1's FEATURE vote: what to build, ranked in the project's priority arena. A different route from the solutions
        // vote on purpose — the two arenas rank different things, and a shared handler would infer which from the id.
        .route("/api/v1/projects/{slug}/features/{feature}/vote", post(cast_feature_vote))
        // Invites: §8.2's trust mechanism — how a stranger becomes a member of a protected project.
        .route(
            "/api/v1/projects/{slug}/invites",
            get(list_invites).post(create_invite),
        )
        .route(
            "/api/v1/projects/{slug}/invites/{invite}/revoke",
            post(revoke_invite),
        )
        .route("/api/v1/auth/redeem-invite", post(redeem_invite))
        // §4/§5: Finder. Sessions live in memory and say so (`persisted: false`) — the reference's own decision, kept.
        .route("/api/v1/finder/questions", get(finder::finder_questions))
        // S2: alternatives arenas — the kind that ranks competing PROJECTS. The route names the owning project (a segment
        // that may be a slug or an id); the competitor in a body is a numeric id, and that asymmetry is the only confusing
        // thing about the shape.
        .route(
            "/api/v1/projects/{slug}/alternatives",
            get(list_alternatives).post(create_alternative),
        )
        .route(
            "/api/v1/projects/{slug}/alternatives/{arena}/entries",
            post(add_alternative_competitor),
        )
        .route(
            "/api/v1/projects/{slug}/alternatives/{arena}/vote",
            post(cast_alternatives_vote),
        )
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
        .route(
            "/api/v1/projects/{slug}/merge",
            get(list_merge_requests).post(create_merge_request),
        )
        .route("/api/v1/projects/{slug}/merge/{merge}/approve", put(approve_merge))
        .route("/api/v1/projects/{slug}/merge/{merge}/execute", put(execute_merge))
        .route("/api/v1/projects/{slug}/merge/{merge}/reject", put(reject_merge))
        // `{slug}` and not `{project_id}`: axum refuses two routes that differ only in a parameter NAME ("Insertion
        // failed due to conflict with previously registered route"), and the sibling feature routes under this
        // prefix already bind `{slug}`. The Go's shape is the same path — parameter names are not part of it.
        .route("/api/v1/projects/{slug}/features/{feature}/complaints", get(feature_complaints))
        .route(
            "/api/v1/projects/{slug}/features/{feature}/tags",
            get(feature_tags).post(add_feature_tag),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/tags/remove",
            post(remove_feature_tag),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/milestone",
            put(set_feature_milestone),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/work-item",
            put(set_work_item_label),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/claim",
            post(claim_work_item).delete(release_claim),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/evidence",
            post(add_feature_evidence).delete(remove_feature_evidence),
        )
        // §6.5's call, at the REFERENCE's path as well as the port's `/solution-call`: a caller written against the Go
        // service asks for `/consensus`, and answering 404 to a path that means the same thing is a migration papercut
        // that costs a client author an afternoon. Two paths, one pair of handlers.
        .route(
            "/api/v1/projects/{slug}/features/{feature}/consensus",
            get(solution_call_readiness).post(open_solution_call),
        )
        .route(
            "/api/v1/projects/{slug}/features/{feature}/strategic-weight",
            put(propose_strategic_weight),
        )
        .route(
            "/api/v1/projects/{project_id}/lists",
            get(project_lists).post(create_list),
        )
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
            get(get_charter).put(update_charter),
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
            get(list_milestones).post(create_milestone),
        )
        .route("/api/v1/projects/{slug}/roadmap", get(roadmap))
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
        // A project's documents: written by a contributor, removed by a maintainer. The PUT is an upsert at
        // (kind, slug) that bumps `revision` rather than replacing the row, so links and comments keep resolving.
        // The contribution feed and the two reads under it. The feed ranks work ITEMS with the finder's own engine — it is
        // deliberately not a second finder — and the two project reads are what its page and the vote page need.
        .route("/api/v1/contribute/next", get(contribute_next))
        .route("/api/v1/projects/{slug}/work-items", get(list_work_items))
        .route("/api/v1/projects/{slug}/join", post(join_project))
        .route("/api/v1/projects/{slug}/votes/next", get(next_vote_pair))
        .route(
            "/api/v1/projects/{slug}/documents",
            // 8 MB, not axum's 2 MB default: a specification is not a comment, and a 3,982-line spec is the shape this
            // raises the bound FOR. It is still a bound — an unbounded read is one POST away from taking the service down.
            get(list_documents)
                .put(put_document)
                .layer(DefaultBodyLimit::max(DOCUMENT_BODY_LIMIT)),
        )
        .route(
            "/api/v1/projects/{slug}/documents/{document}",
            get(get_document).delete(delete_document),
        )
        // The identity cluster: a nameless visitor can act, can become an account in place, and can log in and out.
        .route(
            "/api/v1/guest",
            get(guest_status_handler).post(claim_guest),
        )
        .route("/api/v1/consent", post(accept_consent))
        .route("/api/v1/auth/login", post(login))
        .route("/api/v1/auth/logout", post(logout))
        // The gate is one layer rather than 184 handlers: consent and the guest cap are properties of every mutation, and a rule
        // copied into every handler is a rule that will be missing from the one written next month.
        .layer(axum::middleware::from_fn_with_state(
            state.clone(),
            interaction_gate,
        ))
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
pub(crate) fn internal(error: impl std::fmt::Display) -> ApiError {
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
///
/// A visitor who has been acting as a guest registers INTO their guest identity: the row becomes the account, so everything they
/// did as a guest is theirs without a copy, and the history stays one identity's — "this account took these positions while it
/// had no name". A caller who is already an account takes the ordinary path and gets a second account, which is unusual but not
/// wrong; the caller is signed in either way.
async fn register(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(body): Json<RegisterRequest>,
) -> Result<(axum::http::StatusCode, Json<Value>), ApiError> {
    use concord_db::auth::AuthError;
    use concord_db::identity::Refused;
    let display_name = if body.display_name.trim().is_empty() {
        body.username.trim().to_string()
    } else {
        body.display_name.trim().to_string()
    };
    if body.username.trim().is_empty() || body.password.is_empty() {
        return Err(ApiError::invalid_request("username and password are required"));
    }
    if let Some(actor) = acting_user(&state, &headers).await? {
        let guest = concord_db::identity::is_guest(&state.pool, actor.id)
            .await
            .unwrap_or(false);
        if guest {
            return match concord_db::identity::promote_guest(
                &state.pool,
                actor.id,
                &body.username,
                &display_name,
                &body.password,
            )
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
                Err(Refused::Invalid(message)) => Err(ApiError::invalid_request(message)),
                Err(Refused::Duplicate(message)) => Err(ApiError::conflict(format!("{message} exists"))),
                Err(Refused::NotAGuest) => {
                    Err(ApiError::conflict("this identity is no longer a guest"))
                }
                Err(other) => Err(internal(format!("{other:?}"))),
            };
        }
    }
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
        Err(AuthError::Credentials(message)) => Err(ApiError::unauthenticated(message)),
        Err(AuthError::NoCredential) => Err(ApiError::unauthenticated("invalid credentials")),
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
pub(crate) async fn bearer_user(
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
    headers: HeaderMap,
    body: Option<Json<SolutionCallRequest>>,
) -> Result<(StatusCode, Json<Value>), ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
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
pub(crate) async fn readable(
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


/// Resolve a `{project_id}` path segment, which the reference reads as a **slug** whatever the parameter is called.
///
/// Go's `requireProjectID` calls `GetProject(slug)` on a parameter named `project_id`, and this module's earlier routes
/// took a numeric id. Both are accepted here — slug first, because that is the service's behaviour, then a numeric id,
/// because that is this router's existing convention — and a segment matching neither is 404 with no distinction between
/// "does not exist" and "not yours".
async fn readable_project_segment(
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


// ── filing with the duplicate gate (§6.1) ────────────────────────────────────────────────────────────────────────────

/// Ask the index whether this text already exists, and refuse the filing when it very nearly does.
///
/// Returns `Ok(None)` to proceed, or the 409 the filer must resolve. The gates, in the order they matter:
///
/// 1. **title first, then title+body.** The title is the strongest single signal and the thing the filer is looking at.
///    Measured against two complaints sharing a title and differing bodies: title-vs-title 1.00, concatenation 0.58 — a
///    literal duplicate scored *below* the advisory band and was filed twice with no warning. The full-text query is
///    still run, because two filings can share a vague title and an identical description;
/// 2. **instance-wide scope** (`project_id > 0` is a filter, `0` is the whole instance) — §6.1's *"this was fixed in X"*
///    case, which a per-project search made invisible: same text, score 1.0, no match;
/// 3. **a failure to check must not block the filing.** Refusing on an internal error would lose a real complaint to a
///    bug in the similarity code, so an error means "proceed";
/// 4. **the band is per model**, because the hashed and nomic scales are not comparable.
async fn check_duplicates(
    state: &AppState,
    kind: &str,
    title: &str,
    body: &str,
    actor_id: i64,
    confirmed: bool,
) -> Result<Option<axum::response::Response>, ApiError> {
    let mut sims = match concord_db::embeddings::find_similar(
        &state.pool,
        state.embedder.as_ref(),
        kind,
        title,
        0,
        actor_id,
        Some(concord_db::embeddings::FIELD_TITLE),
        5,
    )
    .await
    {
        Ok(sims) => sims,
        // Rule 3: proceed.
        Err(_) => return Ok(None),
    };
    if sims.is_empty() && !body.trim().is_empty() {
        if let Ok(full) = concord_db::embeddings::find_similar(
            &state.pool,
            state.embedder.as_ref(),
            kind,
            &format!("{title} {body}"),
            0,
            actor_id,
            Some(concord_db::embeddings::FIELD_FULL),
            5,
        )
        .await
        {
            sims = full;
        }
    }
    let Some(top) = sims.first() else {
        return Ok(None);
    };
    let model = state.embedder.id();
    if !concord_embed::bands::is_strong_duplicate_for(top.score, &model) || confirmed {
        return Ok(None);
    }
    let (strong, _, _) = concord_embed::bands::thresholds_for(&model);
    // The 409 carries the candidates, the band and the way out, because a refusal with no route forward is a lost
    // filing. It is a **response**, not an `ApiError`: the error envelope has room for a message and nothing else, so
    // routing this through it dropped the four fields the filer needs and left a 409 that said only that something was
    // wrong.
    let payload = serde_json::json!({
        "error": "an almost identical entry already exists",
        "similar": sims,
        "top_score": top.score,
        "threshold": strong,
        "resubmit_with": { "confirm_duplicate": true },
        "note": "if this is a genuinely different problem, resubmit with confirm_duplicate=true; the existing entry stays untouched either way",
    });
    Ok(Some(
        (StatusCode::CONFLICT, Json(payload)).into_response(),
    ))
}

/// `POST /projects/{project_id}/requests` — file a request under a project.
async fn create_project_request(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<axum::response::Response, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to file a request"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let title = body.get("title").and_then(|v| v.as_str()).unwrap_or_default().to_string();
    let text = body.get("body").and_then(|v| v.as_str()).unwrap_or_default().to_string();
    let confirmed = body
        .get("confirm_duplicate")
        .and_then(serde_json::Value::as_bool)
        .unwrap_or(false);

    if let Some(refusal) = check_duplicates(
        &state,
        concord_db::embeddings::KIND_REQUEST,
        &title,
        &text,
        user.id,
        confirmed,
    )
    .await?
    {
        return Ok(refusal);
    }

    let request = concord_db::requests::create(&state.pool, project.id, user.id, &title, &text)
        .await
        .map_err(internal)?;
    // An embed failure is audited, never fatal: the request exists and a missing vector is recoverable by a backfill,
    // while a 500 here would lose a filing that is already in the database.
    if let Err(error) = concord_db::embeddings::upsert_entity_text(
        &state.pool,
        state.embedder.as_ref(),
        concord_db::embeddings::KIND_REQUEST,
        request.id,
        project.id,
        &title,
        &text,
    )
    .await
    {
        write_audit(&state, Some(project.id), user.id, "embed_failed", "request", request.id, &error.to_string()).await?;
    }
    write_audit(&state, Some(project.id), user.id, "create_request", "request", request.id, &title).await?;
    Ok((StatusCode::CREATED, Json(serde_json::json!(request))).into_response())
}

/// `POST /requests` — file an instance-wide request.
///
/// **No duplicate gate.** That is not an oversight: this route predates the embedder path and the reference never checked
/// here, so an instance-wide filing is accepted unconditionally while a project filing is refused. Recorded rather than
/// silently improved — matching the service is the job until cutover.
async fn create_instance_request(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to file a request"))?;
    let title = body.get("title").and_then(|v| v.as_str()).unwrap_or_default().to_string();
    if title.trim().is_empty() {
        return Err(ApiError::invalid_request("title is required"));
    }
    let text = body.get("body").and_then(|v| v.as_str()).unwrap_or_default().to_string();
    let request = concord_db::requests::create(&state.pool, 0, user.id, &title, &text)
        .await
        .map_err(internal)?;
    let _ = concord_db::embeddings::upsert_entity_text(
        &state.pool,
        state.embedder.as_ref(),
        concord_db::embeddings::KIND_REQUEST,
        request.id,
        0,
        &title,
        &text,
    )
    .await;
    // Audited against project 0 — the request belongs to the instance, and inventing a project here would misfile it
    // under whichever project happened to be first.
    write_audit(&state, None, user.id, "create_instance_request", "request", request.id, &title).await?;
    Ok((StatusCode::CREATED, Json(serde_json::json!(request))))
}

/// `GET /similar/{kind}` — "does this already exist?", asked while the filer is still writing.
///
/// Unauthenticated on purpose (§6.1's panel appears before anyone signs in), which is exactly why the store applies the
/// visibility clause: without it this is an oracle that answers "does a matching row exist in a private project, and what
/// is it called".
async fn find_similar(
    State(state): State<AppState>,
    Path(kind): Path<String>,
    Query(query): Query<SimilarQuery>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let kinds = [
        concord_db::embeddings::KIND_COMPLAINT,
        concord_db::embeddings::KIND_FEATURE,
        concord_db::embeddings::KIND_REQUEST,
        concord_db::embeddings::KIND_PROJECT,
    ];
    if !kinds.contains(&kind.as_str()) {
        return Err(ApiError::invalid_request(
            "kind must be one of complaint, feature, request, project",
        ));
    }
    let text = query.text.unwrap_or_default();
    if text.trim().is_empty() {
        return Err(ApiError::invalid_request("text is required"));
    }
    let project_id = query.project_id.and_then(|v| v.parse::<i64>().ok()).unwrap_or(0);
    let limit = query
        .limit
        .and_then(|v| v.parse::<usize>().ok())
        .filter(|n| *n > 0)
        .unwrap_or(5);
    let actor = bearer_user(&state, &headers).await?.map_or(0, |user| user.id);

    // Two queries, two fields, then the union: the title carries the strongest signal and the full text catches a vague
    // title with an identical description.
    let mut sims = concord_db::embeddings::find_similar(
        &state.pool,
        state.embedder.as_ref(),
        &kind,
        &text,
        project_id,
        actor,
        Some(concord_db::embeddings::FIELD_TITLE),
        limit,
    )
    .await
    .map_err(internal)?;
    if let Ok(full) = concord_db::embeddings::find_similar(
        &state.pool,
        state.embedder.as_ref(),
        &kind,
        &text,
        project_id,
        actor,
        Some(concord_db::embeddings::FIELD_FULL),
        limit,
    )
    .await
    {
        merge_similarities(&mut sims, full, limit);
    }
    sims = concord_embed::rank(sims, limit);

    let model = state.embedder.id();
    let (strong, likely, weak) = concord_embed::bands::thresholds_for(&model);
    let mut response = serde_json::json!({
        "available": true,
        "kind": kind,
        "model_id": model,
        "similar": sims,
        "has_duplicate": sims.first().is_some_and(|top| {
            concord_embed::bands::is_strong_duplicate_for(top.score, &state.embedder.id())
        }),
        // The bands travel with the answer because they are per model: a client that hardcodes one set misreads the
        // other.
        "thresholds": { "strong": strong, "likely": likely, "weak": weak },
    });
    // Coverage travels too: a filer told "no duplicates" on a corpus that is 12% indexed has been told nothing useful,
    // and this is the only place they can find out.
    if let Ok(coverage) = concord_db::embeddings::coverage(&state.pool, state.embedder.as_ref(), &kind).await {
        if let Some(object) = response.as_object_mut() {
            object.insert("coverage".into(), serde_json::json!(coverage));
        }
    }
    Ok(Json(response))
}

/// Union two similarity lists, keeping the higher score per entity, then rank and truncate.
///
/// A union rather than a concatenation: the same entry appears in both the title and the full-text query, and listing it
/// twice pads the panel and makes its count meaningless.
fn merge_similarities(into: &mut Vec<concord_embed::Similarity>, other: Vec<concord_embed::Similarity>, limit: usize) {
    for candidate in other {
        match into.iter().position(|existing| existing.entity_id == candidate.entity_id) {
            Some(index) if candidate.score > into[index].score => into[index] = candidate,
            Some(_) => {}
            None => into.push(candidate),
        }
    }
    let merged = std::mem::take(into);
    *into = concord_embed::rank(merged, limit);
}

#[derive(Debug, serde::Deserialize)]
struct SimilarQuery {
    text: Option<String>,
    project_id: Option<String>,
    limit: Option<String>,
}


/// Map a request refusal onto its status, keeping the three meanings the accept path already distinguishes.
fn request_refusal(refusal: concord_db::requests::Refusal, request_id: i64) -> ApiError {
    use concord_db::requests::Refusal;
    match refusal {
        Refusal::NotAuthor => ApiError::forbidden("only the request's author may change this"),
        Refusal::ForeignAnswer => {
            ApiError::not_found(format!("no answer of that request ({request_id})"))
        }
        Refusal::NotActive(reason) => ApiError::invalid_request(reason),
    }
}

/// `PUT /requests/{id}/tags` — replace the request's tags. Author-only.
///
/// The answer is the tags **as stored**, not the list that was sent: the writer trims, drops whitespace-only names and
/// reuses instance-wide tag rows, so what the caller sent and what landed can differ and the caller should see the
/// difference rather than have it hidden.
async fn set_request_tags(
    State(state): State<AppState>,
    Path(request_id): Path<String>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to set a request's tags"))?;
    let request = require_readable_request(&state, &request_id, &headers).await?;
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
    concord_db::requests::set_tags(&state.pool, request.id, user.id, &tags)
        .await
        .map_err(internal)?
        .map_err(|refusal| request_refusal(refusal, request.id))?;
    let stored = concord_db::requests::tags(&state.pool, request.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({ "tags": stored })))
}

/// `POST /requests/{id}/answers` — a **project** answers a request.
///
/// The project comes from the request row, never from the caller: the reference's earlier version put the caller's user id
/// in the project column, which attributed the answer to whichever project shared that id.
///
/// An **instance-wide request** has no project column to inherit. The reference passed it straight through as 0, which
/// violates NOT NULL and answers 500 for a question the instance board is for. The port requires the body to name the
/// answering project in that case and refuses with 400 otherwise — a decision, recorded in the ledger, because the
/// alternative (guessing a project from the caller's memberships) invents an answer's provenance.
async fn answer_request(
    State(state): State<AppState>,
    Path(request_id): Path<String>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to answer a request"))?;
    let request = require_readable_request(&state, &request_id, &headers).await?;
    let text = body.get("body").and_then(|value| value.as_str()).unwrap_or_default().to_string();
    let named = body
        .get("project_id")
        .and_then(serde_json::Value::as_i64)
        .filter(|id| *id > 0);
    let project = named.unwrap_or(request.project_id);
    if project <= 0 {
        return Err(ApiError::invalid_request(
            "an instance-wide request must name the answering project in project_id",
        ));
    }
    let answering = readable_project(&state, project, &headers).await?;
    let answer = concord_db::requests::answer(&state.pool, request.id, answering.id, user.id, &text)
        .await
        .map_err(|error| match &error {
            sqlx::Error::Database(database) if database.is_unique_violation() => {
                ApiError::conflict("that project has already answered this request")
            }
            _ => internal(error),
        })?;
    write_audit(
        &state,
        Some(answering.id),
        user.id,
        "answer_request",
        "request_answer",
        answer.id,
        &format!("request={}", request.id),
    )
    .await?;
    Ok((StatusCode::CREATED, Json(serde_json::json!(answer))))
}

/// `POST /answers/{id}/vote` — a pairwise comparison between two answers to one request.
///
/// Order, all ported and all observable:
///
/// 1. **both answers are read and checked against this request before anything is written.** The route has no project
///    segment, so visibility comes from the answer's own request — a vote naming an answer from another question would
///    otherwise spend this request's vote and move an unrelated rating;
/// 2. **a foreign answer is 403, not 404.** It exists and the caller may read it; it simply is not this request's;
/// 3. **the voter's weight comes from §8.3**, computed here rather than read from the body. `weight` in the request body
///    is accepted and ignored — §8.3's "money cannot buy weight";
/// 4. **the response carries the new ratings**, because a vote whose effect the caller cannot see is a vote they cannot
///    trust.
async fn vote_answer(
    State(state): State<AppState>,
    Path(request_id): Path<String>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to vote"))?;
    let request = require_readable_request(&state, &request_id, &headers).await?;
    let answer_a = body.get("answer_a").and_then(serde_json::Value::as_i64).unwrap_or(0);
    let answer_b = body.get("answer_b").and_then(serde_json::Value::as_i64).unwrap_or(0);
    let outcome = body
        .get("outcome")
        .and_then(|value| value.as_str())
        .unwrap_or_default()
        .to_string();

    // Rule 2, before any write: an answer that exists but belongs elsewhere is a permission refusal, whether it is one of
    // this request's answers or not.
    for id in [answer_a, answer_b] {
        let found = concord_db::requests::one_answer(&state.pool, id)
            .await
            .map_err(internal)?;
        match found {
            None => return Err(ApiError::not_found("no such answer")),
            Some(answer) if answer.request_id != request.id => {
                return Err(ApiError::forbidden("that answer belongs to a different request"));
            }
            Some(_) => {}
        }
    }

    let charter = concord_db::charter::charter(&state.pool, request.project_id)
        .await
        .map_err(internal)?;
    let weight = concord_db::voteweight::answer_vote_weight(&state.pool, request.project_id, user.id, &charter)
        .await
        .map_err(internal)?;

    let answers = concord_db::requests::vote_answer(
        &state.pool,
        request.id,
        answer_a,
        answer_b,
        user.id,
        &outcome,
        weight,
        charter.glicko_tau,
    )
    .await
    .map_err(|error| match &error {
        // `unique (voter_id, answer_a, answer_b)`: a given voter compares a given ordered pair once. The reference lets
        // the constraint error out as a 500, so a second click on the same pair looks like a server fault; the refusal is
        // the same and only the status changes, which is worth the honesty.
        sqlx::Error::Database(database) if database.is_unique_violation() => {
            ApiError::conflict("you have already voted on that pair of answers")
        }
        _ => internal(error),
    })?
    .map_err(|refusal| match refusal {
        concord_db::requests::VoteRefusal::Invalid(reason) => ApiError::invalid_request(reason),
        concord_db::requests::VoteRefusal::ForeignAnswer => {
            ApiError::forbidden("that answer belongs to a different request")
        }
        concord_db::requests::VoteRefusal::SelfVote => {
            ApiError::forbidden("you cannot vote on an answer you proposed")
        }
    })?;

    write_audit(
        &state,
        Some(request.project_id).filter(|id| *id > 0),
        user.id,
        "vote_request_answer",
        "request",
        request.id,
        &format!("a={answer_a} b={answer_b} outcome={outcome}"),
    )
    .await?;
    Ok(Json(serde_json::json!({
        "status": "voted",
        "weight": weight,
        "answers": answers,
    })))
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
async fn require_role(
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

/// `POST /projects/{slug}/complaints` — file a complaint.
///
/// Six rules, five of them things that were once wrong here:
///
/// 1. **authenticated.** This handler had no check while every sibling did, so a complaint could be filed anonymously and
///    written with `author_id 0` — a claim about a project, and the main input to the ranking, attributed to nobody;
/// 2. **the path and the body must agree** about the project. The route was later mounted under `/projects/{id}/…`, so
///    the same project arrives twice; the path wins, because it is what the caller's standing was checked against, and a
///    contradicting body id is refused rather than silently ignored;
/// 3. **`strategic_multiplier` defaults to 1.0**, not 0. The form sends severity and frequency only, and pain is
///    multiplied by this number: 0 makes the complaint contribute exactly nothing, with no indication why;
/// 4. **§6.1's duplicate gate runs before the write**, instance-wide, and `confirm_duplicate` proceeds;
/// 5. **indexing may fail without losing the complaint** — the audit says `embed_failed` and the filing stands;
/// 6. **filing makes you a participant**: +5 reputation for `submit_complaint` and `contributor` membership, or-ignore so an
///    owner who files is not demoted.
async fn create_complaint(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<axum::response::Response, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to file a complaint"))?;
    let project = readable_project_segment(&state, &slug, &headers).await?;
    if let Some(named) = body.get("project_id").and_then(serde_json::Value::as_i64) {
        if named > 0 && named != project.id {
            return Err(ApiError::invalid_request(format!(
                "project_id {named} does not match the project in the path"
            )));
        }
    }
    let title = body.get("title").and_then(|v| v.as_str()).unwrap_or_default().to_string();
    let text = body.get("body").and_then(|v| v.as_str()).unwrap_or_default().to_string();
    let severity = body.get("severity").and_then(serde_json::Value::as_i64).unwrap_or(1);
    let frequency = body.get("frequency").and_then(serde_json::Value::as_f64).unwrap_or(1.0);
    let multiplier = body
        .get("strategic_multiplier")
        .and_then(serde_json::Value::as_f64)
        .filter(|value| *value > 0.0)
        .unwrap_or(1.0);
    let confirmed = body
        .get("confirm_duplicate")
        .and_then(serde_json::Value::as_bool)
        .unwrap_or(false);

    // Rule 4. The gate is the same one the request path uses; its scope is instance-wide for §6.1's "this was fixed in X".
    let mut warned = 0usize;
    let mut top = 0.0f64;
    if let Ok(sims) = concord_db::embeddings::find_similar(
        &state.pool,
        state.embedder.as_ref(),
        concord_db::embeddings::KIND_COMPLAINT,
        &title,
        0,
        user.id,
        Some(concord_db::embeddings::FIELD_TITLE),
        5,
    )
    .await
    {
        warned = sims.len();
        top = sims.first().map_or(0.0, |sim| sim.score);
        let model = state.embedder.id();
        if warned > 0 && !confirmed && concord_embed::bands::is_strong_duplicate_for(top, &model) {
            let (strong, _, _) = concord_embed::bands::thresholds_for(&model);
            return Ok((
                StatusCode::CONFLICT,
                Json(serde_json::json!({
                    "error": "an almost identical complaint already exists",
                    "similar": sims,
                    "top_score": top,
                    "threshold": strong,
                    "resubmit_with": { "confirm_duplicate": true },
                })),
            )
                .into_response());
        }
    }

    let complaint = concord_db::complaints::create(
        &state.pool,
        project.id,
        user.id,
        &title,
        &text,
        severity,
        frequency,
        multiplier,
    )
    .await
    .map_err(internal)?;

    // Rule 5.
    if let Err(error) = concord_db::embeddings::upsert_entity_text(
        &state.pool,
        state.embedder.as_ref(),
        concord_db::embeddings::KIND_COMPLAINT,
        complaint.id,
        project.id,
        &title,
        &text,
    )
    .await
    {
        write_audit(&state, Some(project.id), user.id, "embed_failed", "complaint", complaint.id, &error.to_string())
            .await?;
    }
    write_audit(&state, Some(project.id), user.id, "create_complaint", "complaint", complaint.id, &title).await?;
    if warned > 0 {
        // Recording that a filer was warned, and filed anyway, is what makes the threshold arguable with evidence rather
        // than intuition.
        write_audit(
            &state,
            Some(project.id),
            user.id,
            "duplicate_warning_shown",
            "complaint",
            complaint.id,
            &format!("{warned} similar entries, top score {top:.3}"),
        )
        .await?;
    }
    // Rule 6.
    concord_db::membership::add_reputation(&state.pool, project.id, user.id, "submit_complaint", 5.0)
        .await
        .map_err(internal)?;
    concord_db::membership::join(&state.pool, project.id, user.id)
        .await
        .map_err(internal)?;
    Ok((StatusCode::CREATED, Json(serde_json::json!(complaint))).into_response())
}

/// `POST /projects/{slug}/complaints/{id}/impact` — how much this hurts. `?severity=` defaults to 1.
///
/// The project half of the path is checked even though the reference's version reads only the id: this route is nested
/// under the same path as the others, and leaving one handler reading half a URL is how the others were wrong.
///
/// **A signed-in caller is required**, which the reference does not enforce: it passes `getActorID(r)` straight into
/// `complaint_impacts.user_id`, which is a foreign key — so an anonymous impact was a 500 there and is a 401 here. The
/// row cannot exist without an attributable person, so 401 states the constraint the schema already holds.
async fn add_complaint_impact(
    State(state): State<AppState>,
    Path((slug, id)): Path<(String, String)>,
    Query(query): Query<SeverityQuery>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to record an impact"))?;
    let project = readable_project_segment(&state, &slug, &headers).await?;
    let complaint = caller_complaint(&state, &project, &id).await?;
    let severity = query
        .severity
        .and_then(|value| value.parse::<i64>().ok())
        .filter(|value| *value != 0)
        .unwrap_or(1);
    concord_db::complaints::add_impact(&state.pool, complaint.id, user.id, severity)
        .await
        .map_err(internal)?
        .map_err(complaint_refusal)?;
    Ok(Json(serde_json::json!({ "status": "impact recorded" })))
}

#[derive(Debug, serde::Deserialize)]
struct SeverityQuery {
    severity: Option<String>,
}

/// `POST /projects/{slug}/complaints/{id}/validate` — §M1's first verdict. Requires **contributor**, not the §8.2
/// eligible-collaborator rule: eligibility requires a qualifying contribution, and one of the qualifiers is a validated
/// complaint that led to a shipped solution — so gating validation on eligibility would ask for a validation before
/// allowing the first one. Joining grants `contributor`, so this is "a member of this project" and nothing narrower.
async fn validate_complaint(
    State(state): State<AppState>,
    Path((slug, id)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to validate a complaint"))?;
    let project = readable_project_segment(&state, &slug, &headers).await?;
    let complaint = caller_complaint(&state, &project, &id).await?;
    require_role(&state, &project, "contributor", &user).await?;
    concord_db::complaints::validate(&state.pool, complaint.id)
        .await
        .map_err(internal)?
        .map_err(complaint_refusal)?;
    write_audit(&state, Some(project.id), user.id, "validate_complaint", "complaint", complaint.id, "").await?;
    Ok(Json(serde_json::json!({ "status": "validated" })))
}

/// `POST /projects/{slug}/complaints/{id}/reject` — the other verdict. §M1's Rejected state, which the reference could not
/// reach at all: `RejectComplaint` existed since M1 with no caller, in production or in a test, so a spam complaint could
/// only be ignored or validated. Same gate as validation, because it is the same act with the opposite verdict — and the
/// same audit treatment, so the corpus can tell who decided what.
async fn reject_complaint(
    State(state): State<AppState>,
    Path((slug, id)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to reject a complaint"))?;
    let project = readable_project_segment(&state, &slug, &headers).await?;
    let complaint = caller_complaint(&state, &project, &id).await?;
    require_role(&state, &project, "contributor", &user).await?;
    concord_db::complaints::reject(&state.pool, complaint.id, user.id)
        .await
        .map_err(internal)?
        .map_err(complaint_refusal)?;
    write_audit(
        &state,
        Some(project.id),
        user.id,
        "reject_complaint",
        "complaint",
        complaint.id,
        "rejected as spam: the author's reputation takes the M1 penalty",
    )
    .await?;
    Ok(Json(serde_json::json!({ "status": "rejected" })))
}

/// `POST /projects/{slug}/complaints/{id}/merge/{target_id}` — fold one complaint into another.
///
/// **The destructive one, and the reference had no actor check at all**: an anonymous caller could merge any two
/// complaints on the instance, and a merge is not reversible from the API, so the distinction between two separately
/// reported problems was destroyable by a stranger.
///
/// Both halves of the path are checked, and in this order: the caller must hold **maintainer**, the source must exist
/// **and belong to this project** (a complaint id from another project must not be reachable through this project's
/// path), and the store refuses a target in a different project. The reference's handler reaches for the source id from a
/// parameter named `source_id` while the router binds `id`, so the source was **always 0** and every merge answered 404
/// — an endpoint that could never have worked, with no auth check on it, and nothing noticed because nothing called it.
/// The audit row is written by the store with the real actor.
async fn merge_complaints(
    State(state): State<AppState>,
    Path((slug, id, target_id)): Path<(String, String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to merge complaints"))?;
    let project = readable_project_segment(&state, &slug, &headers).await?;
    let source = caller_complaint(&state, &project, &id).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let Ok(target) = target_id.parse::<i64>() else {
        return Err(ApiError::not_found("no such complaint"));
    };
    concord_db::complaints::merge(&state.pool, source.id, target, user.id)
        .await
        .map_err(internal)?
        .map_err(complaint_refusal)?;
    Ok(Json(serde_json::json!({ "status": "merged" })))
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
) -> Result<(concord_db::projects::Project, concord_db::auth::User), ApiError> {
    let user = bearer_user(state, headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to change a milestone"))?;
    let project = readable_project_segment(state, segment, headers).await?;
    require_role(state, &project, "maintainer", &user).await?;
    Ok((project, user))
}

/// `POST /projects/{slug}/milestones` — create one, `planned`, dated or not.
async fn create_milestone(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
    let (project, user) = milestone_write_context(&state, &headers, &slug).await?;
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
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let (project, _) = milestone_write_context(&state, &headers, &slug).await?;
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
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let (project, user) = milestone_write_context(&state, &headers, &slug).await?;
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
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let (project, user) = milestone_write_context(&state, &headers, &slug).await?;
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
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let (project, user) = milestone_write_context(&state, &headers, &slug).await?;
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
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<StatusCode, ApiError> {
    let (project, _) = milestone_write_context(&state, &headers, &slug).await?;
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
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to apply tags"))?;
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
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to set languages"))?;
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
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to change the intake mode"))?;
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


// ── criteria writes (§4.10, §6.5) ──────────────────────────────────────────────────────────────────────────────────

fn criteria_refusal(refused: concord_db::criteria_writes::Refused) -> ApiError {
    use concord_db::criteria_writes::Refused;
    match refused {
        Refused::Invalid(message) => ApiError::invalid_request(message),
        Refused::Scope => ApiError::invalid_request(
            "that criterion or feature belongs to another project".to_string(),
        ),
        Refused::NotFound => ApiError::not_found("no such criterion"),
    }
}

/// `POST /projects/{id}/criteria` — add one.
///
/// The two defaults are silent because a caller leaving a field out is not making a mistake: an absent `direction` is
/// `higher_is_better`, and a non-positive `default_weight` becomes `1.0` — a criterion that counts for zero would be a
/// criterion that is off, which is a different thing with its own route. An explicit **bad** direction is refused,
/// because that one changes what the ranking means.
async fn create_criterion(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to add a criterion"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let slug = body.get("slug").and_then(|v| v.as_str()).unwrap_or_default();
    let name = body.get("name").and_then(|v| v.as_str()).unwrap_or_default();
    let description = body.get("description").and_then(|v| v.as_str()).unwrap_or_default();
    let direction = body.get("direction").and_then(|v| v.as_str()).unwrap_or_default();
    let default_weight = body
        .get("default_weight")
        .and_then(serde_json::Value::as_f64)
        .unwrap_or(1.0);
    let criterion = concord_db::criteria_writes::create(
        &state.pool,
        project.id,
        user.id,
        slug,
        name,
        description,
        direction,
        default_weight,
    )
    .await
    .map_err(internal)?
    .map_err(criteria_refusal)?;
    Ok((StatusCode::CREATED, Json(serde_json::to_value(criterion).map_err(internal)?)))
}

/// `PUT /projects/{id}/criteria/{criterion}/active` — switch it off without deleting it.
///
/// Inactive is **not** hidden: the read sorts `active DESC, slug`, so a criterion that stopped counting stays visible
/// after the live set. Deleting it would make "why did this stop counting?" unanswerable from the page.
async fn set_criterion_active(
    State(state): State<AppState>,
    Path((project_segment, criterion)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to change a criterion"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let id: i64 = criterion
        .parse()
        .map_err(|_| ApiError::not_found("no such criterion"))?;
    let active = body
        .get("active")
        .and_then(serde_json::Value::as_bool)
        .unwrap_or(false);
    concord_db::criteria_writes::set_active(&state.pool, project.id, id, active)
        .await
        .map_err(internal)?
        .map_err(criteria_refusal)?;
    let updated = concord_db::criteria_writes::get(&state.pool, project.id, id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such criterion"))?;
    Ok(Json(serde_json::to_value(updated).map_err(internal)?))
}

/// `POST /projects/{id}/criteria/{criterion}/vote` — one comparison, and the two ratings it moved.
///
/// The response carries **both ratings** because a voter should see what their comparison did, not a bare 200. A neutral
/// outcome (`skip`, `both`, `neither`) is accepted and recorded and moves nothing — abstention is data — so its response
/// simply reports the unchanged ratings.
async fn cast_criterion_vote(
    State(state): State<AppState>,
    Path((project_segment, criterion)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to vote"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let id: i64 = criterion
        .parse()
        .map_err(|_| ApiError::not_found("no such criterion"))?;
    let feature_a = body
        .get("feature_a")
        .and_then(serde_json::Value::as_i64)
        .unwrap_or_default();
    let feature_b = body
        .get("feature_b")
        .and_then(serde_json::Value::as_i64)
        .unwrap_or_default();
    let outcome = body.get("outcome").and_then(|v| v.as_str()).unwrap_or_default();
    let weight = body
        .get("weight")
        .and_then(serde_json::Value::as_f64)
        .unwrap_or(1.0);
    concord_db::criteria_writes::cast_vote(
        &state.pool,
        project.id,
        id,
        user.id,
        feature_a,
        feature_b,
        outcome,
        weight,
        0.5,
    )
    .await
    .map_err(internal)?
    .map_err(criteria_refusal)?;
    let mut out = serde_json::Map::new();
    for (key, feature) in [("feature_a", feature_a), ("feature_b", feature_b)] {
        if let Some(rating) = concord_db::criteria_writes::rating(&state.pool, project.id, id, feature)
            .await
            .map_err(internal)?
        {
            out.insert(key.to_string(), serde_json::to_value(rating).map_err(internal)?);
        }
    }
    Ok(Json(serde_json::Value::Object(out)))
}

/// `POST /projects/{id}/criteria-profiles` — save a weight profile.
///
/// `weights` is keyed by criterion id as a **string**, because JSON object keys are strings and the panel builds them
/// from ids; a key that is not a number is a 400 naming the key rather than a silent skip, since a skipped weight is a
/// profile that does not mean what its author wrote. Re-saving an existing slug **replaces** the weights wholesale.
async fn save_criteria_profile(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to save a profile"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let slug = body.get("slug").and_then(|v| v.as_str()).unwrap_or_default();
    let name = body.get("name").and_then(|v| v.as_str()).unwrap_or_default();
    let mut weights = Vec::new();
    if let Some(map) = body.get("weights").and_then(|v| v.as_object()) {
        for (key, value) in map {
            let criterion_id: i64 = key
                .parse()
                .map_err(|_| ApiError::invalid_request(format!("weights keys must be criterion ids: {key}")))?;
            let weight = value.as_f64().unwrap_or_default();
            weights.push(concord_db::criteria_writes::Weight {
                criterion_id,
                weight,
            });
        }
    }
    let id = concord_db::criteria_writes::save_profile(
        &state.pool, project.id, user.id, slug, name, &weights,
    )
    .await
    .map_err(internal)?
    .map_err(criteria_refusal)?;
    Ok((StatusCode::CREATED, Json(serde_json::json!({ "id": id, "slug": slug }))))
}


/// `PUT /projects/{id}/charter` — the numbers every threshold in the system reads.
///
/// **Authenticate before resolving the project**, so an anonymous caller gets 401 whether or not the slug exists; the
/// reverse order makes the status code depend on whether the slug is registered, which is a slug oracle.
///
/// The four bounds are refusals, not clamps, and each one guards a switch that can otherwise be set to a value which
/// silently disables a mechanism for the whole project — the failure mode where nothing errors and nothing works. They
/// live in the store beside the `UPDATE`, because a second copy here would be the one that goes stale.
///
/// **A partial body is filled from the current charter rather than zeroed.** The reference decodes the body straight
/// into its `Charter` and an omitted `solution_call_confidence` arrives as `0`, which fails its own bound and answers
/// "must be between 0 and 1 exclusive" to a request that never mentioned the field. The settings panel posts the whole
/// form, so no real client sees a difference; a curl with three fields now gets the sane result. Decision recorded in the
/// ledger, dated, rather than left as a surprise.
async fn update_charter(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to edit the charter"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let mut charter = concord_db::charter::charter(&state.pool, project.id)
        .await
        .map_err(internal)?;
    apply_charter_fields(&mut charter, &body);
    concord_db::charter::set_charter(&state.pool, project.id, &charter)
        .await
        .map_err(internal)?
        .map_err(|refused| ApiError::invalid_request(refused.0))?;
    let detail = body
        .as_object()
        .map(|map| map.keys().cloned().collect::<Vec<_>>().join(","))
        .unwrap_or_default();
    write_audit(&state, Some(project.id), user.id, "charter.update", "project", project.id, &detail).await?;
    Ok(Json(serde_json::json!({ "status": "charter updated" })))
}

/// Overlay whichever charter fields the body carries, leaving the rest as they were.
fn apply_charter_fields(charter: &mut concord_governance::Charter, body: &serde_json::Value) {
    macro_rules! floats {
        ($($field:ident),* $(,)?) => {
            $(
                if let Some(value) = body.get(stringify!($field)).and_then(serde_json::Value::as_f64) {
                    charter.$field = value;
                }
            )*
        };
    }
    macro_rules! ints {
        ($($field:ident),* $(,)?) => {
            $(
                if let Some(value) = body.get(stringify!($field)).and_then(serde_json::Value::as_i64) {
                    charter.$field = value;
                }
            )*
        };
    }
    macro_rules! bools {
        ($($field:ident),* $(,)?) => {
            $(
                if let Some(value) = body.get(stringify!($field)).and_then(serde_json::Value::as_bool) {
                    charter.$field = value;
                }
            )*
        };
    }
    floats!(
        quorum_ratio,
        consent_ratio,
        support_ratio_min,
        override_ratio,
        vote_window_days,
        merge_quorum_ratio,
        lam,
        mu,
        theme_lifetime_days,
        pain_halflife_days,
        rep_halflife_days,
        glicko_tau,
        vote_weight_cap,
        solution_call_confidence,
        solution_stable_hours,
    );
    ints!(
        quorum_min,
        merge_quorum_min,
        wip_in_progress,
        wip_review,
        solution_call_min_voters,
    );
    bools!(merge_requires_quorum, require_reviewer_approval);
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
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to create a list"))?;
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
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to vote"))?;
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
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to set metrics"))?;
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
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to change visibility"))?;
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


// ── feature tags, feature milestone, strategic weight (§4.5, §6.2, §17) ────────────────────────────────────────────

/// `POST /projects/{id}/features/{feature}/tags` — attach a tag that already exists.
///
/// **Contributor**, and the response is the tags as stored: the tags decide §6.2's theme weight, so whoever may add one
/// must be able to see the result without a second request. An unknown name is a 400 naming the proposing route — the
/// feature in the URL is right there and the name in the body is what is wrong.
///
/// The store spells the tag **once**; see its comment for the mutation-found bug where a second lowering left an
/// `INSERT … SELECT` matching nothing and returning success.
async fn add_feature_tag(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to tag a feature"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let tag = body.get("tag").and_then(|v| v.as_str()).unwrap_or_default();
    match concord_db::taxonomy::add_feature_tag(&state.pool, project.id, feature_id, user.id, tag)
        .await
        .map_err(internal)?
    {
        Ok(()) => {}
        Err(concord_db::taxonomy::Applied::UnknownName { name }) => {
            return Err(ApiError::invalid_request(format!(
                "no tag named {name:?}; propose it with POST /api/v1/taxonomy/proposals (action=create)"
            )));
        }
        Err(concord_db::taxonomy::Applied::UnknownFeature) => {
            return Err(ApiError::not_found("no such feature in this project"));
        }
    }
    let stored = concord_db::panels::feature_tags(&state.pool, feature_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({ "feature_id": feature_id, "tags": stored })))
}

/// `POST /projects/{id}/features/{feature}/tags/remove` — detach one.
///
/// Not housekeeping: a wrong tag silently steers the priority, so it must be removable by whoever can add one. Removing
/// a tag that is not attached is a no-op — the caller's intent is satisfied either way.
async fn remove_feature_tag(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to change a feature's tags"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let tag = body.get("tag").and_then(|v| v.as_str()).unwrap_or_default();
    match concord_db::taxonomy::remove_feature_tag(&state.pool, project.id, feature_id, tag)
        .await
        .map_err(internal)?
    {
        Ok(()) => {}
        Err(concord_db::taxonomy::Applied::UnknownName { name }) => {
            return Err(ApiError::invalid_request(format!("no tag named {name:?}")));
        }
        Err(concord_db::taxonomy::Applied::UnknownFeature) => {
            return Err(ApiError::not_found("no such feature in this project"));
        }
    }
    let stored = concord_db::panels::feature_tags(&state.pool, feature_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({ "feature_id": feature_id, "tags": stored })))
}

/// `PUT /projects/{id}/features/{feature}/milestone` — place one feature in the roadmap, or take it out.
///
/// `milestone_id: null` clears it. The response reports **`open_blockers`** rather than a boolean: a feature may be
/// planned before what it depends on has shipped, and refusing that would make a roadmap unable to express "this lands
/// after X". The count is what tells a reader how much work stands in front of it.
async fn set_feature_milestone(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to change the roadmap"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let milestone_id = body.get("milestone_id").and_then(serde_json::Value::as_i64);
    let position = body
        .get("position")
        .and_then(serde_json::Value::as_i64)
        .unwrap_or_default();
    let open = concord_db::milestones::set_feature_milestone(
        &state.pool, project.id, feature_id, milestone_id, position,
    )
    .await
    .map_err(internal)?
    .map_err(|_| ApiError::not_found("no such milestone in this project"))?;
    let detail = match milestone_id {
        Some(id) => format!("milestone {id}"),
        None => "milestone cleared".to_string(),
    };
    write_audit(&state, Some(project.id), user.id, "feature.milestone", "feature", feature_id, &detail).await?;
    Ok(Json(serde_json::json!({ "feature_id": feature_id, "open_blockers": open })))
}

/// `PUT /projects/{id}/features/{feature}/strategic-weight` — **propose** a weight change; it does not apply one.
///
/// §6.2's `μ` is a steering lever, so moving it is a consensus decision: this writes a pending proposal and the route's
/// message says so, naming the proposal to consent to. A rationale is required and refused at the handler with a message
/// that names the field, because "invalid" is no help to a caller who left it out.
///
/// **Why this exists when `/strategy/weight-proposals` already did.** The store was wired, but under the port's own path
/// (`project_id` in the path, `feature_id` in the body) while the reference — and therefore the pages built against it —
/// uses `PUT /features/{id}/strategic-weight` with the weight in the body. Two shapes for one act: the port's is a
/// reasonable API and the reference's is the one a caller already sends, so both are served from the same store rather
/// than one of them 404ing. The route inventory is a gate precisely because "wired somewhere" is not "wired where the
/// caller looks".
async fn propose_strategic_weight(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to propose a weight"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let weight = body
        .get("weight")
        .and_then(serde_json::Value::as_f64)
        .unwrap_or_default();
    let rationale = body.get("rationale").and_then(|v| v.as_str()).unwrap_or_default();
    if rationale.trim().is_empty() {
        return Err(ApiError::invalid_request(
            "a rationale is required: a weight change is a steering decision, so it must say why".to_string(),
        ));
    }
    let proposal = concord_db::strategy::propose(&state.pool, project.id, feature_id, user.id, weight, rationale)
        .await
        .map_err(|refused| strategy_refusal(refused))?;
    Ok((
        StatusCode::CREATED,
        Json(serde_json::json!({
            "proposal": proposal,
            "message": format!("strategic weight changes are ratified by consensus; consent to proposal {}", proposal.id),
        })),
    ))
}

/// Resolve `{feature}` under `{slug}`, refusing a feature that belongs to another project with **404**.
async fn caller_feature(
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


// ── work items, dependencies, evidence (§4.5.7, R18, §3.4) ─────────────────────────────────────────────────────────

fn feature_work_refusal(refused: concord_db::feature_work::Refused) -> ApiError {
    use concord_db::feature_work::Refused;
    match refused {
        Refused::Invalid(message) => ApiError::invalid_request(message),
        Refused::NotFound => ApiError::not_found("no such feature in this project"),
        Refused::Held(message) => ApiError::conflict(message),
    }
}

/// `PUT /projects/{id}/features/{feature}/work-item` — what the task costs a newcomer.
///
/// **Maintainer only.** Labelling something "good first" is a promise about review capacity, and only somebody who will
/// review it can make that promise. `difficulty` of `""` clears the label rather than storing an empty chip; a budget of
/// zero or less is refused, because a task someone says finishes in no time misleads whoever picks it up.
async fn set_work_item_label(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to label work"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let difficulty = body.get("difficulty").and_then(|v| v.as_str());
    let budget = body
        .get("time_budget_minutes")
        .and_then(serde_json::Value::as_i64);
    let needs_mentor = body
        .get("needs_mentor")
        .and_then(serde_json::Value::as_bool)
        .unwrap_or(false);
    concord_db::feature_work::set_work_item_label(
        &state.pool,
        project.id,
        feature_id,
        difficulty,
        budget,
        needs_mentor,
    )
    .await
    .map_err(internal)?
    .map_err(feature_work_refusal)?;
    let item = concord_db::feature_work::work_item(&state.pool, project.id, feature_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such feature in this project"))?;
    Ok(Json(serde_json::to_value(item).map_err(internal)?))
}

/// `POST /projects/{id}/features/{feature}/claim` — take the single live claim.
///
/// A claim **does not move the feature**: the board is the only mechanism that moves work, and a claim that changed status
/// would make two surfaces disagree about what is being built. Only `ready`/`in_progress` work is claimable, because a
/// draft is work nobody has agreed to do.
async fn claim_work_item(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to claim work"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    concord_db::feature_work::claim(&state.pool, project.id, feature_id, user.id)
        .await
        .map_err(internal)?
        .map_err(feature_work_refusal)?;
    let item = concord_db::feature_work::work_item(&state.pool, project.id, feature_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such feature in this project"))?;
    Ok((StatusCode::CREATED, Json(serde_json::to_value(item).map_err(internal)?)))
}

/// `DELETE /projects/{id}/features/{feature}/claim` — withdraw a claim.
///
/// The row stays with `released_at` set: a withdrawal is a state, not a deletion, so "picked up and dropped twice" stays
/// answerable. Releasing a claim you do not hold is a 404 rather than a silent success, because a page that says "you
/// released it" about somebody else's claim is telling the wrong story.
async fn release_claim(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<StatusCode, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to release a claim"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    concord_db::feature_work::release_claim(&state.pool, project.id, feature_id, user.id)
        .await
        .map_err(internal)?
        .map_err(|_| ApiError::not_found("no claim of yours on this feature"))?;
    Ok(StatusCode::NO_CONTENT)
}

/// `POST /projects/{id}/features/{feature}/deps` — record that `blocker_id` must ship first.
///
/// A cycle is refused **with the path that would close it**: a graph with a cycle can never be started, and the reader who
/// built it needs to see what they closed rather than the word "invalid".
async fn add_feature_dep(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to change dependencies"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let blocked = caller_feature(&state, &project, &feature).await?;
    let blocker = body
        .get("blocker_id")
        .and_then(serde_json::Value::as_i64)
        .unwrap_or_default();
    concord_db::feature_work::add_dependency(&state.pool, project.id, blocker, blocked)
        .await
        .map_err(internal)?
        .map_err(feature_work_refusal)?;
    write_audit(
        &state,
        Some(project.id),
        user.id,
        "feature.dep.add",
        "feature",
        blocked,
        &format!("blocked by #{blocker}"),
    )
    .await?;
    Ok(Json(serde_json::json!({ "feature_id": blocked, "blocker_id": blocker })))
}

/// `DELETE /projects/{id}/features/{feature}/deps?blocker_id=` — remove an edge.
///
/// Removing one that does not exist is a **no-op**: the caller's intent ("this must not be a dependency") is satisfied
/// either way, and a page that reloads into an empty state should not have to distinguish the two.
async fn remove_feature_dep(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    Query(query): Query<std::collections::HashMap<String, String>>,
    headers: HeaderMap,
) -> Result<StatusCode, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to change dependencies"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let blocked = caller_feature(&state, &project, &feature).await?;
    let blocker: i64 = query
        .get("blocker_id")
        .and_then(|value| value.parse().ok())
        .unwrap_or_default();
    concord_db::feature_work::remove_dependency(&state.pool, project.id, blocker, blocked)
        .await
        .map_err(internal)?
        .map_err(feature_work_refusal)?;
    write_audit(
        &state,
        Some(project.id),
        user.id,
        "feature.dep.remove",
        "feature",
        blocked,
        &format!("unblocked from #{blocker}"),
    )
    .await?;
    Ok(StatusCode::NO_CONTENT)
}

/// `POST /projects/{id}/features/{feature}/evidence` — cite a document or a field report.
///
/// Scoped **twice**: the feature, and the cited row. A reference id is global, so without the second check a feature can
/// cite another project's document and the page will render its title as if it were evidence for this idea.
async fn add_feature_evidence(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to add evidence"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let kind = body.get("kind").and_then(|v| v.as_str()).unwrap_or_default();
    let ref_id = body
        .get("ref_id")
        .and_then(serde_json::Value::as_i64)
        .unwrap_or_default();
    concord_db::feature_work::add_evidence(&state.pool, project.id, feature_id, kind, ref_id)
        .await
        .map_err(internal)?
        .map_err(feature_work_refusal)?;
    write_audit(
        &state,
        Some(project.id),
        user.id,
        "feature.evidence",
        "feature",
        feature_id,
        &format!("{kind} #{ref_id}"),
    )
    .await?;
    Ok((
        StatusCode::CREATED,
        Json(serde_json::json!({ "kind": kind, "ref_id": ref_id })),
    ))
}

/// `DELETE /projects/{id}/features/{feature}/evidence?kind=&ref_id=` — remove one citation.
///
/// A feature left with no evidence stays where it is: the rule is enforced on the next transition to `ready`, not
/// retroactively, because silently un-readying work somebody is doing is worse than letting the next reader see it.
async fn remove_feature_evidence(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    Query(query): Query<std::collections::HashMap<String, String>>,
    headers: HeaderMap,
) -> Result<StatusCode, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to change evidence"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let kind = query.get("kind").cloned().unwrap_or_default();
    let ref_id: i64 = query
        .get("ref_id")
        .and_then(|value| value.parse().ok())
        .unwrap_or_default();
    concord_db::feature_work::remove_evidence(&state.pool, project.id, feature_id, &kind, ref_id)
        .await
        .map_err(internal)?
        .map_err(feature_work_refusal)?;
    Ok(StatusCode::NO_CONTENT)
}


// ── §6.3: filing a solution ────────────────────────────────────────────────────────────────────────────────────────

/// What a filer sends. Every field is optional except the title, and `type` defaults to `build-new` in the store.
#[derive(serde::Deserialize)]
struct CreateSolutionRequest {
    #[serde(default)]
    title: String,
    #[serde(default)]
    body: String,
    #[serde(default, rename = "type")]
    kind: String,
    #[serde(default)]
    external_ref: String,
    #[serde(default)]
    affiliation: String,
    #[serde(default)]
    relationship: String,
    #[serde(default)]
    parent_solution_id: i64,
    #[serde(default)]
    expertise_tags: Vec<String>,
}

/// `POST /projects/{id}/features/{feature}/solutions` — file a solution against a feature.
///
/// **Any signed-in user may file**, not only a collaborator: filing a proposal is contributing, and a feature whose
/// solutions could only be written by people already inside the project is a project that never hears from the people it
/// affects. What is refused is the narrow set of filings that cannot be ranked at all — see
/// [`concord_db::solution_writes::file_solution`], which owns those rules beside the write.
///
/// The response carries `is_baseline` because the caller needs it: the first `do-nothing` becomes the arena's permanent
/// baseline, and a client that does not know it just filed the baseline cannot label the board correctly.
async fn create_solution(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<CreateSolutionRequest>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to file a solution"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let new = concord_db::solution_writes::NewSolution {
        title: body.title,
        body: body.body,
        kind: body.kind,
        external_ref: body.external_ref,
        affiliation: body.affiliation,
        relationship: body.relationship,
        parent_solution_id: body.parent_solution_id,
        expertise_tags: body.expertise_tags,
    };
    let is_do_nothing = new.kind.trim() == concord_db::solution_writes::TYPE_DO_NOTHING;
    let solution_id = concord_db::solution_writes::file_solution(
        &state.pool,
        project.id,
        feature_id,
        user.id,
        new,
    )
    .await
    .map_err(solution_refusal)?;
    let solution = concord_db::arena::get_solution(&state.pool, solution_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| internal("the solution vanished after it was written"))?;
    let baseline = if is_do_nothing {
        sqlx::query_scalar::<_, bool>(
            "select coalesce(e.is_baseline, false) from arena_entries e
               join arenas a on a.id = e.arena_id
              where a.feature_id = $1 and a.type = 'solution' and e.entity_type = 'solution' and e.entity_id = $2",
        )
        .bind(feature_id)
        .bind(solution_id)
        .fetch_optional(&state.pool)
        .await
        .map_err(internal)?
        .unwrap_or(false)
    } else {
        false
    };
    Ok((
        StatusCode::CREATED,
        Json(serde_json::json!({ "solution": solution, "is_baseline": baseline })),
    ))
}

/// A refusal from the solution store, as the status each rule deserves.
fn solution_refusal(refused: concord_db::solution_writes::Refused) -> ApiError {
    use concord_db::solution_writes::Refused;
    match refused {
        Refused::Invalid(detail) => ApiError::invalid_request(detail),
        Refused::NotFound => ApiError::not_found("no such feature in this project"),
        Refused::SelfVote => ApiError::forbidden(
            "a feature's author cannot author its solutions (§5.2: nobody may judge their own entry)".to_string(),
        ),
        Refused::ParentNotInArena => {
            ApiError::invalid_request("the fork's parent is not in this feature's arena")
        }
        Refused::Database(error) => internal(error),
    }
}


// ── §5.1: the solution arena's pairwise vote ──────────────────────────────────────────────────────────────────────

/// What a voter sends: the two solutions and which one is better.
#[derive(serde::Deserialize)]
struct VoteSolutionsRequest {
    #[serde(default)]
    solution_a: i64,
    #[serde(default)]
    solution_b: i64,
    #[serde(default)]
    outcome: String,
    #[serde(default)]
    reason: String,
}

/// `POST /projects/{id}/features/{feature}/solutions/vote` — compare two solutions in one feature's arena.
///
/// **A separate route from the feature vote on purpose.** The two arenas rank different things against different
/// competitors, and a shared handler would have to infer which was meant from the id — which is exactly how a solution id
/// ends up being compared against a feature.
///
/// Voting **joins the project** first, as the reference does: a comparison cast by someone with no membership row is a vote
/// the reputation system cannot attribute, and the join is what §8.3's weight is later computed against.
///
/// Both solutions are checked against the feature **before the arena is touched**, so a request naming a solution from
/// another feature cannot spend a vote in this one.
async fn vote_solutions(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<VoteSolutionsRequest>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to vote"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    concord_db::membership::join(&state.pool, project.id, user.id)
        .await
        .map_err(internal)?;

    for id in [body.solution_a, body.solution_b] {
        let solution = concord_db::arena::get_solution(&state.pool, id)
            .await
            .map_err(internal)?;
        match solution {
            Some(solution) if solution.feature_id == feature_id => {}
            Some(_) => {
                return Err(ApiError::forbidden(
                    "a solution in this comparison belongs to another feature".to_string(),
                ))
            }
            None => return Err(ApiError::not_found("no such solution")),
        }
    }

    let arena_id = concord_db::arena::find_solution_arena(&state.pool, feature_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("this feature has no solutions to compare"))?;
    let charter = concord_db::charter::charter(&state.pool, project.id)
        .await
        .map_err(internal)?;

    // §8.3's weight is computed here and NEVER taken from the body: a client-supplied weight is client-supplied influence.
    // The tags of both solutions are the scope, because a vote between a Rust design and a Rust design is a Rust judgement.
    let mut tags: Vec<String> = Vec::new();
    for id in [body.solution_a, body.solution_b] {
        if let Some(solution) = concord_db::arena::get_solution(&state.pool, id).await.map_err(internal)? {
            for tag in concord_ranking::parse_expertise_tags(&solution.expertise_tags) {
                if !tags.contains(&tag) {
                    tags.push(tag);
                }
            }
        }
    }
    let weight = concord_db::voteweight::solution_vote_weight(
        &state.pool,
        project.id,
        user.id,
        &charter,
        &tags,
    )
    .await
    .map_err(internal)?;

    let vote_id = concord_db::arena_writes::cast_vote(
        &state.pool,
        arena_id,
        user.id,
        "solution",
        body.solution_a,
        "solution",
        body.solution_b,
        &body.outcome,
        &body.reason,
        weight,
    )
    .await
    .map_err(vote_refusal)?;

    let solutions = concord_db::arena::list_solutions(&state.pool, feature_id, 50)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({
        "vote": { "id": vote_id, "outcome": body.outcome, "weight": weight },
        "solutions": solutions,
    })))
}

/// A refusal from the arena store, as the status each rule deserves.
fn vote_refusal(refused: concord_db::arena_writes::Refused) -> ApiError {
    use concord_db::arena_writes::Refused;
    match refused {
        Refused::Invalid(detail) => ApiError::invalid_request(detail),
        Refused::NotFound => ApiError::not_found("that entry is not in this arena"),
        Refused::SelfVote => ApiError::forbidden(
            "you proposed one of these: an entry cannot be ranked by the person who wrote it".to_string(),
        ),
        Refused::SameEntity => ApiError::invalid_request("both sides of a comparison must differ"),
        Refused::Database(error) => internal(error),
    }
}


// ── §6.3/§6.4: coverage claims and their contests ────────────────────────────────────────────────────────────────

/// What a claim sends.
#[derive(serde::Deserialize)]
struct ClaimCoverageRequest {
    #[serde(default)]
    complaint_id: i64,
    #[serde(default)]
    claim: String,
}

/// What a contest sends.
#[derive(serde::Deserialize)]
struct ContestCoverageRequest {
    #[serde(default)]
    reason: String,
}

/// `POST /projects/{id}/features/{feature}/solutions/{solution}/coverage` — say what this solution covers.
///
/// The response is the whole [`concord_db::coverage_writes::CoverageScore`], not just the ratio: "resolves 340 of 510 pain"
/// is what a reader can act on, and a bare percentage hides both how much is at stake and whether the denominator moved.
///
/// **Restating a claim clears its contest**, which is the answer a contest invites. Leaving it contested would make every
/// contest permanent.
async fn claim_coverage(
    State(state): State<AppState>,
    Path((project_segment, feature, solution)): Path<(String, String, String)>,
    headers: HeaderMap,
    Json(body): Json<ClaimCoverageRequest>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to claim coverage"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let solution_id = solution_in_feature(&state, feature_id, &solution).await?;
    let _ = user;
    concord_db::coverage_writes::claim_coverage(
        &state.pool,
        solution_id,
        body.complaint_id,
        &body.claim,
    )
    .await
    .map_err(coverage_refusal)?;
    let score = concord_db::coverage_writes::coverage_score(&state.pool, feature_id, solution_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({ "coverage": score })))
}

/// `POST /projects/{id}/features/{feature}/solutions/{solution}/coverage/{complaint}/contest` — challenge a claim.
///
/// §6.4: "Coverage claims are challengeable." A contest **needs a reason** (a contest with none is indistinguishable from a
/// deletion) and **the claimant cannot be the contestor** (the same rule §5.2 applies to rankings). The contested claim
/// stops counting until it is restated.
async fn contest_coverage(
    State(state): State<AppState>,
    Path((project_segment, feature, solution, complaint)): Path<(String, String, String, String)>,
    headers: HeaderMap,
    Json(body): Json<ContestCoverageRequest>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to contest a claim"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let solution_id = solution_in_feature(&state, feature_id, &solution).await?;
    let complaint_id: i64 = complaint
        .parse()
        .map_err(|_| ApiError::invalid_request("the complaint id must be a number"))?;
    concord_db::coverage_writes::contest_coverage(
        &state.pool,
        solution_id,
        complaint_id,
        user.id,
        &body.reason,
    )
    .await
    .map_err(coverage_refusal)?;
    let score = concord_db::coverage_writes::coverage_score(&state.pool, feature_id, solution_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({ "coverage": score })))
}

/// The solution named in the path, refusing one that belongs to another feature — the id in the path and the row must agree,
/// or a caller can claim coverage on a solution they never named.
async fn solution_in_feature(
    state: &AppState,
    feature_id: i64,
    segment: &str,
) -> Result<i64, ApiError> {
    let id: i64 = segment
        .parse()
        .map_err(|_| ApiError::invalid_request("the solution id must be a number"))?;
    match concord_db::arena::get_solution(&state.pool, id).await.map_err(internal)? {
        Some(solution) if solution.feature_id == feature_id => Ok(id),
        Some(_) => Err(ApiError::not_found("that solution belongs to another feature")),
        None => Err(ApiError::not_found("no such solution")),
    }
}

/// A refusal from the coverage store, as the status each rule deserves.
fn coverage_refusal(refused: concord_db::coverage_writes::Refused) -> ApiError {
    use concord_db::coverage_writes::Refused;
    match refused {
        Refused::Invalid(detail) => ApiError::invalid_request(detail),
        Refused::NotFound => ApiError::not_found("no such solution or coverage claim"),
        Refused::SelfVote => ApiError::forbidden(
            "an author cannot contest their own coverage claim".to_string(),
        ),
        Refused::Database(error) => internal(error),
    }
}


// ── §5.1: the feature vote (what to build) ──────────────────────────────────────────────────────────────────────

/// What a voter sends: the two features and which matters more.
///
/// The reference's body names them `feature_a`/`feature_b`; both spellings are accepted because the port's own clients have
/// used `a`/`b` on the solution arena and a client that swaps one for the other should get a comparison, not a 400 about a
/// field name.
#[derive(serde::Deserialize)]
struct CastFeatureVoteRequest {
    #[serde(default, alias = "feature_a")]
    a: i64,
    #[serde(default, alias = "feature_b")]
    b: i64,
    #[serde(default)]
    outcome: String,
}

/// `POST /projects/{id}/features/{feature}/vote` — rank this feature against another.
///
/// The feature named in the path is the **left** side, so a client that names one feature and a rival gets the comparison it
/// meant; `a` in the body may repeat it.
///
/// **A first vote enrols the voter**, and the join happens *before* the weight is computed — a voter with no membership row
/// has no reputation to weigh. The reference's comment records the same ordering bug being found the hard way: refusing
/// guests first made the very first vote impossible.
///
/// The weight is `VoteWeight(reputation, 1.0, cap)` times §12.3's age gate: **the role is not a multiplier here**, unlike the
/// solution vote.
async fn cast_feature_vote(
    State(state): State<AppState>,
    Path((project_segment, feature)): Path<(String, String)>,
    headers: HeaderMap,
    Json(body): Json<CastFeatureVoteRequest>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to vote"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let feature_id = caller_feature(&state, &project, &feature).await?;
    let charter = concord_db::charter::charter(&state.pool, project.id)
        .await
        .map_err(internal)?;
    let weight = concord_db::voteweight::feature_vote_weight(&state.pool, project.id, user.id, &charter)
        .await
        .map_err(internal)?;
    let left = if body.a == 0 { feature_id } else { body.a };
    let vote_id = concord_db::votes_writes::record_feature_vote(
        &state.pool,
        project.id,
        user.id,
        concord_db::votes_writes::NewFeatureVote {
            feature_a: left,
            feature_b: body.b,
            outcome: body.outcome.clone(),
        },
        weight,
        charter.glicko_tau,
    )
    .await
    .map_err(feature_vote_refusal)?;
    // Enrolment happens AFTER the vote is recorded, which is what the reference's own comment says and what its code did not
    // do: a vote refused for ranking your own feature must not add you to the project. The weight is unaffected — a voter with
    // no membership row has no reputation in the project either way, and this arena does not use the role.
    concord_db::membership::join(&state.pool, project.id, user.id)
        .await
        .map_err(internal)?;
    // The audit line and the reputation point are what make participating worth anything (§8.3 counts "good-faith consensus
    // participation"); neither is allowed to fail the vote itself, because the vote has already been recorded.
    let _ = sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1,$2,'cast_vote','vote',$1,$3,now())",
    )
    .bind(project.id)
    .bind(user.id)
    .bind(format!("feature_a={left} feature_b={} outcome={}", body.b, body.outcome))
    .execute(&state.pool)
    .await;
    let _ = sqlx::query(
        "insert into reputation_events (project_id, user_id, kind, points, created_at)
         values ($1,$2,'vote',1,now())",
    )
    .bind(project.id)
    .bind(user.id)
    .execute(&state.pool)
    .await;
    Ok(Json(serde_json::json!({
        "vote": { "id": vote_id, "outcome": body.outcome, "weight": weight },
    })))
}

/// A refusal from the feature-vote store, as the status each rule deserves.
fn feature_vote_refusal(refused: concord_db::votes_writes::Refused) -> ApiError {
    use concord_db::votes_writes::Refused;
    match refused {
        Refused::Invalid(detail) => ApiError::invalid_request(detail),
        Refused::NotFound => ApiError::forbidden(
            "both features must belong to this project".to_string(),
        ),
        Refused::SelfVote => ApiError::forbidden(
            "you proposed one of these features: a feature cannot be ranked by the person who proposed it".to_string(),
        ),
        Refused::Database(error) => internal(error),
    }
}


// ── invites (§8.2) ────────────────────────────────────────────────────────────────────────────────────────────────

/// What a mint sends. Both limits are optional and mean "none" when absent or zero.
#[derive(serde::Deserialize, Default)]
struct CreateInviteRequest {
    #[serde(default)]
    expires_in_seconds: i64,
    #[serde(default)]
    max_uses: i64,
}

/// What a redemption sends.
///
/// **The token is in the body, not the URL**, so it does not end up in access logs, referrer headers or browser history — and
/// so a link that is shared in a chat does not leave a redeemable token in a server log.
#[derive(serde::Deserialize)]
struct RedeemInviteRequest {
    #[serde(default)]
    token: String,
}

/// `POST /projects/{id}/invites` — mint a link.
///
/// **Any member may mint one, not only a maintainer.** That is the reference's choice and it is recorded here because it is
/// the kind of rule a reader would otherwise assume: an invite is the mechanism by which a project grows, and restricting it
/// to maintainers makes a collective's growth depend on one role being awake. The role it grants is `contributor`, which
/// cannot admit others or move a decision.
async fn create_invite(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    headers: HeaderMap,
    body: Option<Json<CreateInviteRequest>>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated(
            "creating an invite needs an account; a guest identity cannot extend membership",
        ))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await.map_err(|_| {
        // The reference answers 404 here rather than 403: a non-member is not told that the project exists under this id.
        ApiError::not_found("no such project")
    })?;
    let body = body.map(|Json(body)| body).unwrap_or_default();
    let invite = concord_db::invite_writes::create(
        &state.pool,
        project.id,
        user.id,
        body.expires_in_seconds,
        body.max_uses,
    )
    .await
    .map_err(invite_refusal)?;
    let _ = sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1,$2,'create_invite','invite',$3,$4,now())",
    )
    .bind(project.id)
    .bind(user.id)
    .bind(invite.id)
    .bind(&project.slug)
    .execute(&state.pool)
    .await;
    Ok((StatusCode::CREATED, Json(invite)))
}

/// `GET /projects/{id}/invites` — the project's links, newest first, **including revoked and expired ones**: an invite list is
/// a record of who was let in and by which link, and hiding the dead ones hides the history.
async fn list_invites(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    headers: HeaderMap,
) -> Result<Json<Vec<concord_db::invite_writes::Invite>>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to see invites"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user)
        .await
        .map_err(|_| ApiError::not_found("no such project"))?;
    let invites = concord_db::invite_writes::list(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(invites))
}

/// `POST /projects/{id}/invites/{invite}/revoke` — withdraw a link, keeping the record of it.
///
/// The invite is checked against **this** project before it is withdrawn: without that, a member of one project could revoke
/// an invite belonging to another by naming its id under their own project's URL.
async fn revoke_invite(
    State(state): State<AppState>,
    Path((project_segment, invite)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("revoking an invite needs an account"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user)
        .await
        .map_err(|_| ApiError::not_found("no such project"))?;
    let invite_id: i64 = invite
        .parse()
        .map_err(|_| ApiError::not_found("no such invite"))?;
    let existing = concord_db::invite_writes::get(&state.pool, invite_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such invite"))?;
    if existing.project_id != project.id {
        return Err(ApiError::not_found("no such invite"));
    }
    concord_db::invite_writes::revoke(&state.pool, invite_id)
        .await
        .map_err(invite_refusal)?;
    let _ = sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1,$2,'revoke_invite','invite',$3,$4,now())",
    )
    .bind(project.id)
    .bind(user.id)
    .bind(invite_id)
    .bind(&project.slug)
    .execute(&state.pool)
    .await;
    Ok(Json(serde_json::json!({ "status": "revoked" })))
}

/// `POST /auth/redeem-invite` — turn a token into a membership.
///
/// **404 for every failure**, on purpose: expired, revoked, exhausted and unknown are indistinguishable, or the endpoint
/// becomes an oracle for testing tokens. The response names the project's slug so the caller can navigate, and the role it
/// granted, because a client that has just joined should not have to ask what it joined as.
async fn redeem_invite(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(body): Json<RedeemInviteRequest>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to redeem an invite"))?;
    if body.token.trim().is_empty() {
        return Err(ApiError::invalid_request("a token is required"));
    }
    let project_id = concord_db::invite_writes::redeem(&state.pool, body.token.trim(), user.id)
        .await
        .map_err(|refused| match refused {
            concord_db::invite_writes::Refused::Invalid(reason) => ApiError::invalid_request(reason),
            concord_db::invite_writes::Refused::Database(error) => internal(error),
            // Everything else is the same 404, deliberately.
            _ => ApiError::not_found("that invite cannot be redeemed"),
        })?;
    let project = concord_db::projects::project_by_id(&state.pool, project_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| internal("the project vanished after the invite was redeemed"))?;
    let _ = sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1,$2,'redeem_invite','invite',$1,$3,now())",
    )
    .bind(project_id)
    .bind(user.id)
    .bind(&project.slug)
    .execute(&state.pool)
    .await;
    Ok(Json(serde_json::json!({
        "project": project.slug,
        "slug": project.slug,
        "status": "joined",
        "role": "contributor",
    })))
}

/// An invite refusal, as the status it deserves.
fn invite_refusal(refused: concord_db::invite_writes::Refused) -> ApiError {
    use concord_db::invite_writes::Refused;
    match refused {
        Refused::Invalid(reason) => ApiError::invalid_request(reason),
        Refused::NotFound => ApiError::not_found("no such invite"),
        Refused::WrongProject => ApiError::not_found("no such invite"),
        Refused::AlreadyRevoked => ApiError::invalid_request("that invite is already revoked"),
        Refused::Database(error) => internal(error),
    }
}


// ── §5.7: merge requests ──────────────────────────────────────────────────────────────────────────────────────────

/// What a merge request needs: the feature it lands, and a title.
#[derive(serde::Deserialize)]
struct CreateMergeRequest {
    #[serde(default)]
    feature_id: i64,
    #[serde(default)]
    title: String,
}

/// `GET /projects/{id}/merge` — the project's merge requests, newest first, each with its §5.7 gate.
///
/// The gate is served **with** the request because a client that re-derives the quorum is a second implementation of §5.7, and
/// the one that gets it wrong shows a filled quorum the server refuses to honour.
async fn list_merge_requests(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let rows = concord_db::merge_writes::list(&state.pool, project.id)
        .await
        .map_err(internal)?;
    let mut out = Vec::with_capacity(rows.len());
    for row in rows {
        let state_gate = concord_db::merge_writes::gate(&state.pool, &row)
            .await
            .map_err(merge_refusal)?;
        out.push(serde_json::json!({ "request": row, "gate": state_gate }));
    }
    Ok(Json(serde_json::json!(out)))
}

/// `POST /projects/{id}/merge` — open a merge request for a feature.
///
/// A contributor may open one, and doing so earns §8.3's `create_merge_request` reputation — which is also what a member's tag
/// reputation is derived from, so an unmerged request earns the opening and nothing else.
async fn create_merge_request(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    headers: HeaderMap,
    Json(body): Json<CreateMergeRequest>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    if body.feature_id == 0 {
        return Err(ApiError::invalid_request("a merge request needs a feature"));
    }
    // The feature must be in this project: a merge request filed against somebody else's feature would ship it from here.
    let feature = concord_db::features::feature(&state.pool, body.feature_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such feature"))?;
    if feature.project_id != project.id {
        return Err(ApiError::not_found("no such feature"));
    }
    let title = if body.title.trim().is_empty() {
        feature.title.clone()
    } else {
        body.title.clone()
    };
    let request = concord_db::merge_writes::create(
        &state.pool,
        project.id,
        body.feature_id,
        user.id,
        &title,
    )
    .await
    .map_err(internal)?;
    let _ = sqlx::query(
        "insert into reputation_events (project_id, user_id, kind, points, created_at)
         values ($1,$2,'create_merge_request',3,now())",
    )
    .bind(project.id)
    .bind(user.id)
    .execute(&state.pool)
    .await;
    Ok((StatusCode::CREATED, Json(request)))
}

/// `PUT /projects/{id}/merge/{mr}/approve` — one collaborator's confirmation.
///
/// Two rules the reference learned the hard way: **the approver must be a member with role ≥ contributor**, not "an eligible
/// collaborator" — approving somebody else's merge request IS a qualifying contribution, and requiring eligibility first would
/// make that route unreachable — and **the author cannot approve their own request**.
async fn approve_merge(
    State(state): State<AppState>,
    Path((project_segment, merge)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let mr_id = merge_id(&state, &project, &merge).await?;
    concord_db::merge_writes::approve(&state.pool, mr_id, user.id)
        .await
        .map_err(merge_refusal)?;
    let gate = concord_db::merge_writes::gate_for(&state.pool, mr_id)
        .await
        .map_err(merge_refusal)?;
    Ok(Json(serde_json::json!({ "status": "approved", "gate": gate })))
}

/// `PUT /projects/{id}/merge/{mr}/execute` — land the change.
///
/// **This is the destructive one.** It closes the request, marks the feature `shipped` and closes every complaint linked to it,
/// in one transaction. Three things stop it: the request must belong to this project (or the answer is 404, never 403 — a 403
/// confirms the id is real somewhere else), it must still be `open` (a second execute used to re-run the whole landing), and
/// §5.7's gate must pass.
///
/// The gate's shortfall is returned in the 409, because "gate not met" leaves a reader unable to tell whether they need another
/// confirmation or a reviewer.
async fn execute_merge(
    State(state): State<AppState>,
    Path((project_segment, merge)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let mr_id = merge_id(&state, &project, &merge).await?;
    concord_db::merge_writes::execute(&state.pool, mr_id, project.id)
        .await
        .map_err(merge_refusal)?;
    Ok(Json(serde_json::json!({ "status": "merged" })))
}

/// `PUT /projects/{id}/merge/{mr}/reject` — refuse it without landing anything.
async fn reject_merge(
    State(state): State<AppState>,
    Path((project_segment, merge)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let mr_id = merge_id(&state, &project, &merge).await?;
    concord_db::merge_writes::reject(&state.pool, mr_id, project.id)
        .await
        .map_err(merge_refusal)?;
    Ok(Json(serde_json::json!({ "status": "rejected" })))
}

/// The merge request named in the path, proven to belong to the project in the path.
async fn merge_id(
    state: &AppState,
    project: &concord_db::projects::Project,
    segment: &str,
) -> Result<i64, ApiError> {
    let Ok(id) = segment.parse::<i64>() else {
        return Err(ApiError::not_found("no such merge request"));
    };
    match concord_db::merge_writes::get(&state.pool, id).await.map_err(internal)? {
        Some(request) if request.project_id == project.id => Ok(id),
        _ => Err(ApiError::not_found("no such merge request")),
    }
}

/// A merge refusal, as the status each rule deserves.
fn merge_refusal(refused: concord_db::merge_writes::Refused) -> ApiError {
    use concord_db::merge_writes::Refused;
    match refused {
        Refused::NotFound => ApiError::not_found("no such merge request"),
        Refused::AlreadyClosed(status) => {
            ApiError::conflict(format!("this merge request is already {status}"))
        }
        Refused::GateUnmet(shortfall) => {
            ApiError::conflict(format!("§5.7's merge gate is not met: {shortfall}"))
        }
        Refused::SelfApproval => {
            ApiError::forbidden("the author of a merge request cannot approve it".to_string())
        }
        Refused::NotAMember => ApiError::forbidden(
            "approving a merge request is for members with at least the contributor role".to_string(),
        ),
        Refused::Database(error) => internal(error),
    }
}


/// The document body limit: 8 MB, the reference's own, raised for specifications rather than comments.
const DOCUMENT_BODY_LIMIT: usize = 8 << 20;

/// What a document write sends.
#[derive(serde::Deserialize, Default)]
struct PutDocumentRequest {
    #[serde(default)]
    kind: String,
    #[serde(default)]
    slug: String,
    #[serde(default)]
    title: String,
    #[serde(default)]
    body: String,
}

/// `PUT /projects/{id}/documents` — create or replace the document at `(kind, slug)`.
///
/// Writing needs the **contributor** role, not merely an account: a document states what a project *is*, so it is not something
/// any signed-in visitor gets to rewrite. The reply is the stored document with its new `revision` — the one number that tells a
/// reader whether the spec in front of them is the one that was agreed.
async fn put_document(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    headers: HeaderMap,
    Json(body): Json<PutDocumentRequest>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("writing a document needs an account"))?;
    // Visibility before anything is read or written: a private project's spec is exactly the content that must not leak, and
    // this handler resolves its project by slug rather than through a project-id guard.
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let document = concord_db::document_writes::put(
        &state.pool,
        project.id,
        user.id,
        &body.kind,
        &body.slug,
        &body.title,
        &body.body,
    )
    .await
    .map_err(document_refusal)?;
    let _ = sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1,$2,'put_document','project_document',$3,$4,now())",
    )
    .bind(project.id)
    .bind(user.id)
    .bind(document.id)
    .bind(format!("{}/{}", document.kind, document.slug))
    .execute(&state.pool)
    .await;
    Ok(Json(serde_json::to_value(&document).map_err(internal)?))
}

/// `DELETE /projects/{id}/documents/{doc}` — remove a document. **Maintainer**, not contributor.
async fn delete_document(
    State(state): State<AppState>,
    Path((project_segment, document)): Path<(String, String)>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("deleting a document needs an account"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "maintainer", &user).await?;
    let Ok(id) = document.parse::<i64>() else {
        return Err(ApiError::invalid_request("document id must be an integer"));
    };
    concord_db::document_writes::delete(&state.pool, project.id, id)
        .await
        .map_err(document_refusal)?;
    let _ = sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1,$2,'delete_document','project_document',$3,null,now())",
    )
    .bind(project.id)
    .bind(user.id)
    .bind(id)
    .execute(&state.pool)
    .await;
    Ok(Json(serde_json::json!({ "status": "deleted" })))
}

/// A document refusal, as the status it deserves. An unknown kind is a 400 with the list of kinds, because the caller's next
/// question is which ones exist.
fn document_refusal(refused: concord_db::document_writes::Refused) -> ApiError {
    use concord_db::document_writes::Refused;
    match refused {
        Refused::Invalid(reason) => ApiError::invalid_request(reason),
        Refused::NotFound => ApiError::not_found("no such document"),
        Refused::Database(error) => internal(error),
    }
}


// ── S2: alternatives arenas ──────────────────────────────────────────────────────────────────────────────────────

/// `GET /projects/{id}/alternatives` — every alternatives arena on this project, each with its ranked competitors.
///
/// Visibility is applied at READ time, after the leaderboard is read: a competitor that was public when added and is private now
/// must not be ranked into a response the caller can see. Filtering after the sort keeps the ranking the arena's real one rather
/// than a ranking of the subset the caller happens to be allowed to know about.
async fn list_alternatives(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let arenas = concord_db::alternatives::list(&state.pool, project.id, concord_db::alternatives::KIND)
        .await
        .map_err(internal)?;
    let user = bearer_user(&state, &headers).await?;
    let mut out = Vec::with_capacity(arenas.len());
    for arena in arenas {
        let rows = concord_db::arena::leaderboard(&state.pool, arena.id, 50)
            .await
            .map_err(internal)?;
        let mut entries: Vec<concord_db::alternatives::AlternativeEntry> = Vec::new();
        for row in rows {
            // `leaderboard` is scoped to the arena, and this arena only ever holds `project` entries — so a row whose project
            // row is gone is the one thing that needs skipping, because it would render as a blank ranked line.
            let Some(competitor) = concord_db::projects::project_by_id(&state.pool, row.entity_id)
                .await
                .map_err(internal)?
            else {
                continue;
            };
            if !readable(&state, &competitor, user.as_ref()).await? {
                continue;
            }
            entries.push(concord_db::alternatives::AlternativeEntry {
                rank: entries.len() + 1,
                project_id: competitor.id,
                slug: competitor.slug.clone(),
                title: competitor.name.clone(),
                rating: row.r,
                rd: row.rd,
                games: row.games,
                better_for: arena.use_case.clone(),
                worse_for: concord_db::alternatives::not_for_use_case(&arena.use_case),
            });
        }
        out.push(concord_db::alternatives::AlternativeArena {
            arena_id: arena.id,
            use_case: arena.use_case,
            question: arena.question,
            entry_count: entries.len(),
            entries,
        });
    }
    Ok(Json(serde_json::json!({
        "project_id": project.id,
        "arenas": out,
    })))
}

#[derive(Debug, Deserialize, Default)]
struct CreateAlternative {
    #[serde(default)]
    use_case: String,
    #[serde(default)]
    question: String,
}

/// `POST /projects/{id}/alternatives` — create (or find) the arena for a use case.
///
/// Authentication comes BEFORE the project is resolved, so the status cannot depend on whether the project exists: the reverse
/// order answers 403 for a real project and 404 for an absent one, which enumerates the instance's slugs.
async fn create_alternative(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    headers: HeaderMap,
    body: Option<Json<CreateAlternative>>,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let body = body.map(|Json(body)| body).unwrap_or_default();
    let arena = concord_db::alternatives::ensure(&state.pool, project.id, &body.use_case, &body.question)
        .await
        .map_err(alternative_refusal)?;
    Ok((
        StatusCode::CREATED,
        Json(serde_json::json!({ "arena": {
            "id": arena.id,
            "project_id": arena.project_id,
            "type": arena.entity_type,
            "question": arena.question,
            "use_case": arena.use_case,
        } })),
    ))
}

#[derive(Debug, Deserialize, Default)]
struct AddCompetitor {
    #[serde(default)]
    project_id: i64,
}

/// `POST /projects/{id}/alternatives/{arena}/entries` — add a competing project.
///
/// An arena belonging to another project is a bare 404, the same answer as one that does not exist: a 403 would confirm the id is
/// real, which is the enumeration this whole route family refuses to provide. The same rule covers an unreadable candidate.
async fn add_alternative_competitor(
    State(state): State<AppState>,
    Path((project_segment, arena)): Path<(String, String)>,
    headers: HeaderMap,
    body: Option<Json<AddCompetitor>>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let arena_id = alternative_arena_id(&state, &project, &arena).await?;
    let body = body.map(|Json(body)| body).unwrap_or_default();
    let candidate = concord_db::projects::project_by_id(&state.pool, body.project_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("not found"))?;
    if !readable(&state, &candidate, Some(&user)).await? {
        return Err(ApiError::not_found("not found"));
    }
    concord_db::alternatives::add_competitor(&state.pool, arena_id, body.project_id)
        .await
        .map_err(alternative_refusal)?;
    Ok(Json(serde_json::json!({
        "arena_id": arena_id,
        "competitor": candidate.slug,
        "project_id": candidate.id,
    })))
}

#[derive(Debug, Deserialize, Default)]
struct AlternativesVote {
    #[serde(default)]
    a_project_id: i64,
    #[serde(default)]
    b_project_id: i64,
    #[serde(default)]
    outcome: String,
    #[serde(default)]
    reason: String,
}

/// `POST /projects/{id}/alternatives/{arena}/vote` — a better-for/worse-for judgement.
///
/// The weight is computed here and never taken from the body: a client-supplied weight is client-supplied influence. §8.3's
/// formula is reputation × role, then §12.3's account-age gate LAST — **no expertise multiplier**, because expertise is tag-scoped
/// to solutions and a project has no solution tags; multiplying by a neutral 1.0 would pretend to have done something.
///
/// **Both sides must be readable by this voter**, not merely exist: ranking a private competitor would leak its existence through
/// the rating movement and let the voter judge something they cannot see.
async fn cast_alternatives_vote(
    State(state): State<AppState>,
    Path((project_segment, arena)): Path<(String, String)>,
    headers: HeaderMap,
    body: Option<Json<AlternativesVote>>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await?;
    let arena_id = alternative_arena_id(&state, &project, &arena).await?;
    let body = body.map(|Json(body)| body).unwrap_or_default();
    if body.a_project_id == body.b_project_id {
        return Err(ApiError::invalid_request(
            "both sides of a comparison must be different projects",
        ));
    }
    for id in [body.a_project_id, body.b_project_id] {
        if id == project.id {
            return Err(ApiError::invalid_request(
                "the arena's own project cannot be voted against another project; an alternatives arena ranks \
                 competitors",
            ));
        }
        let competitor = concord_db::projects::project_by_id(&state.pool, id)
            .await
            .map_err(internal)?
            .ok_or_else(|| ApiError::not_found("not found"))?;
        if !readable(&state, &competitor, Some(&user)).await? {
            return Err(ApiError::not_found("not found"));
        }
    }
    let charter = concord_db::charter::charter(&state.pool, project.id)
        .await
        .map_err(internal)?;
    let weight = concord_db::voteweight::answer_vote_weight(&state.pool, project.id, user.id, &charter)
        .await
        .map_err(internal)?;
    let vote_id = concord_db::arena_writes::cast_vote(
        &state.pool,
        arena_id,
        user.id,
        "project",
        body.a_project_id,
        "project",
        body.b_project_id,
        &body.outcome,
        &body.reason,
        weight,
    )
    .await
    .map_err(arena_vote_refusal)?;
    Ok(Json(serde_json::json!({ "vote": { "id": vote_id } })))
}

/// The arena named in the path, proven to belong to the project in the path and to rank projects.
async fn alternative_arena_id(
    state: &AppState,
    project: &concord_db::projects::Project,
    segment: &str,
) -> Result<i64, ApiError> {
    let Ok(id) = segment.parse::<i64>() else {
        return Err(ApiError::invalid_request("invalid arena id"));
    };
    if id <= 0 {
        return Err(ApiError::invalid_request("invalid arena id"));
    }
    match concord_db::arena::find(&state.pool, id).await.map_err(internal)? {
        Some(arena) if arena.project_id == project.id => Ok(id),
        _ => Err(ApiError::not_found("not found")),
    }
}

/// An alternatives refusal, as the status it deserves.
fn alternative_refusal(refused: concord_db::alternatives::Refused) -> ApiError {
    use concord_db::alternatives::Refused;
    match refused {
        Refused::Invalid(reason) => ApiError::invalid_request(reason),
        Refused::NotFound => ApiError::not_found("not found"),
        Refused::WrongKind(reason) => ApiError::invalid_request(reason),
        Refused::Database(error) => internal(error),
    }
}

/// A pairwise-vote refusal.
fn arena_vote_refusal(refused: concord_db::arena_writes::Refused) -> ApiError {
    use concord_db::arena_writes::Refused;
    match refused {
        Refused::Invalid(reason) => ApiError::invalid_request(reason),
        Refused::NotFound => ApiError::not_found("not found"),
        Refused::SelfVote => ApiError::forbidden(
            "an author cannot vote on their own entry".to_string(),
        ),
        Refused::SameEntity => {
            ApiError::invalid_request("both sides of a comparison must be different projects")
        }
        Refused::Database(error) => internal(error),
    }
}


// ── The consent gate, guest identities, and login ────────────────────────────────────────────────────────────────

/// The cookie that records the accepted notice. HttpOnly because the page is TOLD the state by the server rather than reading it:
/// a cookie a script can read is a cookie a script can forge, and the only reason to read it in JavaScript would be to skip the
/// server's answer.
const CONSENT_COOKIE: &str = "concord_consent";

/// A year. The notice is about cookies, and a notice that re-asks every week is a notice people dismiss without reading.
const CONSENT_MAX_AGE: i64 = 365 * 24 * 60 * 60;

/// The navigation cookie. Accepted ONLY on a GET whose path is not under `/api/` — every mutating route is an API route, so this
/// cookie structurally cannot authorize a write, and the bearer header remains the only credential for the API. Without it a
/// member page would be cacheable and shareable, and with it a GET is a session.
const NAV_COOKIE: &str = "concord_nav";

/// Methods that change something.
fn is_mutating(method: &axum::http::Method) -> bool {
    use axum::http::Method;
    matches!(
        *method,
        Method::POST | Method::PUT | Method::PATCH | Method::DELETE
    )
}

/// Paths the gate does not touch.
///
/// Establishing or ending an identity is not an interaction with the project's data, and hiding registration behind a cookie
/// banner is a dark pattern: a visitor must be able to create an account, and to leave, without agreeing to anything. The
/// webhook is server-to-server and has no browser and no cookie to accept.
fn consent_exempt(path: &str) -> bool {
    path == "/api/v1/consent"
        || path == "/api/v1/guest"
        || path.starts_with("/api/v1/auth/")
        || path == "/api/v1/hooks/forgejo"
        || !path.starts_with("/api/")
}

/// One cookie's value from the request headers.
fn cookie_value(headers: &HeaderMap, name: &str) -> Option<String> {
    let raw = headers.get(axum::http::header::COOKIE)?.to_str().ok()?;
    raw.split(';')
        .map(str::trim)
        .find_map(|pair| pair.strip_prefix(&format!("{name}=")).map(str::to_string))
}

/// The token this request presents in the `Authorization` header.
///
/// **The navigation cookie is deliberately NOT read here.** It exists so a browser navigation renders as the member, and it is
/// accepted only for a GET outside `/api/` — see [`presented_token_for_logout`] for the one exception. Reading it for API calls
/// would make every mutating API route reachable from a cross-site form post with the visitor's cookie attached, which is the
/// CSRF hole the split exists to prevent, and the header is what every script sends anyway.
fn presented_token(headers: &HeaderMap) -> Option<String> {
    headers
        .get(axum::http::header::AUTHORIZATION)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.strip_prefix("Bearer "))
        .map(str::to_string)
}

/// The token to revoke: the header if present, else the navigation cookie.
///
/// Logout is the one route that reads the cookie, because a browser that navigates to `/logout` may send only that. Revoking is
/// not a privileged action — it only ever ends the session it was handed — so accepting the weaker credential here costs nothing
/// that accepting it on a write would.
fn presented_token_for_logout(headers: &HeaderMap) -> Option<String> {
    presented_token(headers).or_else(|| cookie_value(headers, NAV_COOKIE))
}

/// The user this request acts as, by either credential.
async fn acting_user(
    state: &AppState,
    headers: &HeaderMap,
) -> Result<Option<concord_db::auth::User>, ApiError> {
    let Some(token) = presented_token(headers) else {
        return Ok(None);
    };
    concord_db::auth::user_for_token(&state.pool, &token)
        .await
        .map_err(|error| match error {
            concord_db::auth::AuthError::Database(error) => internal(error),
            other => internal(format!("{other:?}")),
        })
}

/// A refusal with a `code` a client can switch on and a sentence a person can read.
fn coded(status: StatusCode, code: &str, message: &str) -> axum::response::Response {
    use axum::response::IntoResponse;
    (
        status,
        Json(serde_json::json!({ "code": code, "error": message })),
    )
        .into_response()
}

/// Consent and the guest cap, in the one place both are enforced.
///
/// Consent is a GATE, not a credential: it permits, while identity still comes from the bearer token. That is why the cookie is
/// safe to set from a page the visitor does not control, and why planting it gains an attacker nothing.
///
/// The guest cap is checked on every guest write and not only at mint: mint-only would let an instance cross the cap with a
/// visitor mid-session and keep serving that visitor forever, which is exactly the state the cap exists to end. A guest is the
/// only identity that can be refused this way, and a store failure while checking fails CLOSED for a guest — an instance that
/// cannot say how big it is must not keep taking nameless contributions.
async fn interaction_gate(
    State(state): State<AppState>,
    request: axum::extract::Request,
    next: axum::middleware::Next,
) -> axum::response::Response {
    use axum::response::IntoResponse;
    let path = request.uri().path().to_string();
    if !is_mutating(request.method()) || consent_exempt(&path) {
        return next.run(request).await;
    }
    let version = concord_db::identity::GuestStatus::consent_version_from_env();
    if cookie_value(request.headers(), CONSENT_COOKIE).as_deref() != Some(version.as_str()) {
        return (
            StatusCode::FORBIDDEN,
            Json(serde_json::json!({
                "code": "consent_required",
                "error": "cookies must be accepted before interacting with this site",
                "consent": "required",
            })),
        )
            .into_response();
    }
    let Ok(Some(user)) = acting_user(&state, request.headers()).await else {
        // No identity, or one that cannot be resolved: the route's own gates decide. A store error here must not become a 403
        // for a legitimate account, so it is reported by the handler that needs the actor.
        return next.run(request).await;
    };
    let guest = concord_db::identity::is_guest(&state.pool, user.id)
        .await
        .unwrap_or(false);
    if !guest {
        return next.run(request).await;
    }
    let limit = concord_db::identity::GuestStatus::limit_from_env();
    match concord_db::identity::guest_status(&state.pool, limit).await {
        Ok(status) if status.allowed => next.run(request).await,
        Ok(status) => {
            use axum::response::IntoResponse;
            (
                StatusCode::FORBIDDEN,
                Json(serde_json::json!({
                    "code": "guest_limit_reached",
                    "error": "this instance has grown past its guest limit; create an account to take part",
                    "mau": status.mau,
                    "limit": status.limit,
                })),
            )
                .into_response()
        }
        Err(error) => {
            tracing::warn!(%error, "guest limit check failed");
            coded(
                StatusCode::SERVICE_UNAVAILABLE,
                "guest_check_failed",
                "guest contributions are paused: this instance could not check its own size",
            )
        }
    }
}

/// `GET /api/v1/guest` — the public, read-only question a page asks before it offers to let a visitor act.
async fn guest_status_handler(
    State(state): State<AppState>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let limit = concord_db::identity::GuestStatus::limit_from_env();
    let status = concord_db::identity::guest_status(&state.pool, limit)
        .await
        .map_err(internal)?;
    let version = concord_db::identity::GuestStatus::consent_version_from_env();
    Ok(Json(serde_json::json!({
        "allowed": status.allowed,
        "mau": status.mau,
        "limit": status.limit,
        "window_days": status.window_days,
        "guests": status.guests,
        "stale_guests": status.stale_guests,
        "consent_version": version,
        "consented": cookie_value(&headers, CONSENT_COOKIE).as_deref() == Some(version.as_str()),
    })))
}

/// `POST /api/v1/guest` — mint a guest identity.
///
/// Consent is already enforced by the gate, so reaching this handler means the visitor accepted the notice. The token is the
/// identity's only credential and is returned once.
async fn claim_guest(
    State(state): State<AppState>,
) -> Result<axum::response::Response, ApiError> {
    use axum::response::IntoResponse;
    use concord_db::identity::Refused;
    let limit = concord_db::identity::GuestStatus::limit_from_env();
    match concord_db::identity::create_guest(&state.pool, limit).await {
        Ok((user, token)) => Ok((
            StatusCode::CREATED,
            Json(serde_json::json!({
                "user": serde_json::to_value(user).map_err(internal)?,
                "token": token,
                "token_note": TOKEN_NOTE,
            })),
        )
            .into_response()),
        Err(Refused::LimitReached { mau, limit }) => Ok((
            StatusCode::FORBIDDEN,
            Json(serde_json::json!({
                "code": "guest_limit_reached",
                "error": "this instance has grown past its guest limit; create an account to take part",
                "mau": mau,
                "limit": limit,
            })),
        )
            .into_response()),
        Err(Refused::CheckFailed(error)) => Err(internal(error)),
        Err(other) => Err(internal(format!("{other:?}"))),
    }
}

/// `POST /api/v1/consent` — record acceptance: the cookie the gate reads, plus the identity's own record when there is an identity
/// to record it against.
async fn accept_consent(
    State(state): State<AppState>,
    headers: HeaderMap,
) -> Result<axum::response::Response, ApiError> {
    use axum::response::IntoResponse;
    let version = concord_db::identity::GuestStatus::consent_version_from_env();
    let secure = headers
        .get("x-forwarded-proto")
        .and_then(|value| value.to_str().ok())
        .is_some_and(|value| value.eq_ignore_ascii_case("https"));
    let mut response = Json(serde_json::json!({ "ok": true, "consent": version })).into_response();
    let cookie = format!(
        "{CONSENT_COOKIE}={version}; Path=/; Max-Age={CONSENT_MAX_AGE}; HttpOnly; SameSite=Lax{}",
        if secure { "; Secure" } else { "" }
    );
    if let Ok(value) = axum::http::HeaderValue::from_str(&cookie) {
        response
            .headers_mut()
            .append(axum::http::header::SET_COOKIE, value);
    }
    // A failure to record it against the identity is reported in the log and NOT turned into a failed acceptance: the visitor did
    // accept, and a 500 would tell them otherwise.
    if let Some(user) = acting_user(&state, &headers).await? {
        if let Err(error) = concord_db::identity::record_consent(&state.pool, user.id).await {
            tracing::warn!(%error, "record consent");
        }
    }
    Ok(response)
}

#[derive(Debug, Deserialize)]
struct LoginRequest {
    username: String,
    password: String,
}

/// `POST /api/v1/auth/login`.
///
/// The fold runs AFTER a successful login and its failure does not undo it: someone who has just proved they own an account must
/// not be stranded by a 500 from a housekeeping step. It is reported in the response instead, beside the counts it produced.
async fn login(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(body): Json<LoginRequest>,
) -> Result<axum::response::Response, ApiError> {
    use axum::response::IntoResponse;
    use concord_db::auth::AuthError;
    let (user, token) = match concord_db::auth::login(&state.pool, &body.username, &body.password).await {
        Ok(pair) => pair,
        Err(AuthError::Credentials(message) | AuthError::Invalid(message)) => {
            return Err(ApiError::unauthenticated(message))
        }
        Err(AuthError::NoCredential) => {
            return Err(ApiError::unauthenticated("invalid credentials"))
        }
        Err(AuthError::Duplicate(message)) => return Err(ApiError::conflict(message)),
        Err(AuthError::Database(error)) => return Err(internal(error)),
    };
    // The guest this browser was acting as, if any: its rows genuinely have to move, because a second identity already holds the
    // token this visitor proved they own.
    let mut fold = concord_db::identity::FoldReport::default();
    let mut fold_error: Option<String> = None;
    if let Some(actor) = acting_user(&state, &headers).await? {
        if actor.id != user.id {
            let guest = concord_db::identity::is_guest(&state.pool, actor.id)
                .await
                .unwrap_or(false);
            if guest {
                match concord_db::identity::fold_guest_into(&state.pool, actor.id, user.id).await {
                    Ok(report) => fold = report,
                    Err(error) => fold_error = Some(format!("{error:?}")),
                }
            }
        }
    }
    let mut body_json = serde_json::json!({
        "user": serde_json::to_value(&user).map_err(internal)?,
        "token": token,
        "token_note": TOKEN_NOTE,
    });
    if fold.moved > 0 || fold.superseded > 0 {
        body_json["fold"] = serde_json::to_value(&fold).map_err(internal)?;
    }
    if let Some(message) = fold_error {
        body_json["fold_error"] = Value::String(message);
    }
    let mut response = Json(body_json).into_response();
    // The navigation cookie is a SESSION cookie (no Max-Age): the localStorage token lives until it is revoked, and the navigation
    // convenience should not outlive the browser session that created it. Clearing it on logout is what makes logout look like it
    // happened.
    let cookie = nav_cookie(&format!("={token}"), &headers);
    if let Ok(value) = axum::http::HeaderValue::from_str(&cookie) {
        response
            .headers_mut()
            .append(axum::http::header::SET_COOKIE, value);
    }
    Ok(response)
}

/// `POST /api/v1/auth/logout` — revoke the presented token. Idempotent: a client that logs out twice, or whose token was already
/// revoked, gets 204 both times.
async fn logout(State(state): State<AppState>, headers: HeaderMap) -> Result<axum::response::Response, ApiError> {
    use axum::response::IntoResponse;
    if let Some(token) = presented_token_for_logout(&headers) {
        concord_db::auth::revoke_token(&state.pool, &token)
            .await
            .map_err(|error| match error {
                concord_db::auth::AuthError::Database(error) => internal(error),
                other => internal(format!("{other:?}")),
            })?;
    }
    let mut response = StatusCode::NO_CONTENT.into_response();
    // A cookie left behind would keep rendering member pages for a credential that no longer resolves.
    let cookie = nav_cookie("", &headers);
    if let Ok(value) = axum::http::HeaderValue::from_str(&cookie) {
        response
            .headers_mut()
            .append(axum::http::header::SET_COOKIE, value);
    }
    Ok(response)
}

/// The `concord_nav` Set-Cookie value. `payload` is `=<token>` to set it, or empty to expire it.
fn nav_cookie(payload: &str, headers: &HeaderMap) -> String {
    let secure = headers
        .get("x-forwarded-proto")
        .and_then(|value| value.to_str().ok())
        .is_some_and(|value| value.eq_ignore_ascii_case("https"));
    let base = format!(
        "{NAV_COOKIE}{payload}; Path=/; HttpOnly; SameSite=Lax{}",
        if secure { "; Secure" } else { "" }
    );
    if payload.is_empty() {
        format!("{base}; Max-Age=-1")
    } else {
        base
    }
}


// ── The contribution feed, and the two reads under it ────────────────────────────────────────────────────────────

/// `GET /projects/{id}/work-items` — every item the project offers, claimed or not.
///
/// Public, like the rest of a project's surface: a task board nobody can read is a board only its existing members can join. The
/// claimer is included because "somebody is on this" is the fact a visitor most needs before starting.
async fn list_work_items(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    let items = concord_db::feed_reads::work_items(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::to_value(items).map_err(internal)?))
}

/// `POST /projects/{id}/join` — take part as a contributor.
///
/// Idempotent, and the answer does not depend on whether the caller was already a member: their goal is "I can take part", and
/// telling a non-member they were already enrolled leaks membership state for no benefit. The role in the answer is the role they
/// NOW hold, which is the useful half of that sentence.
async fn join_project(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    concord_db::feed_reads::join(&state.pool, project.id, user.id)
        .await
        .map_err(internal)?;
    sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1,$2,'join_project','project',$1,'',now())",
    )
    .bind(project.id)
    .bind(user.id)
    .execute(&state.pool)
    .await
    .map_err(internal)?;
    let role = concord_db::feed_reads::role_for(&state.pool, project.id, user.id)
        .await
        .map_err(internal)?
        .unwrap_or_else(|| "contributor".to_string());
    Ok(Json(serde_json::json!({ "status": "joined", "role": role })))
}

/// `GET /projects/{id}/votes/next` — the pair this voter should judge next.
///
/// **The caller is enrolled first.** That is the reference's behaviour, and it is the honest one: asking someone to compare two
/// features and then refusing their vote with 403 would be an invitation followed by a refusal. It is also why `/join` exists
/// separately, so a page can enrol without asking for a pair.
async fn next_vote_pair(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    use concord_db::feed_reads::NoPair;
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    concord_db::feed_reads::join(&state.pool, project.id, user.id)
        .await
        .map_err(internal)?;
    match concord_db::feed_reads::next_pair(&state.pool, project.id, user.id)
        .await
        .map_err(internal)?
    {
        Ok((first, second)) => Ok(Json(serde_json::json!({
            "feature_a": serde_json::to_value(first).map_err(internal)?,
            "feature_b": serde_json::to_value(second).map_err(internal)?,
        }))),
        // 409, not 500: nothing is broken and nothing about the request is malformed — the project simply has nothing to compare
        // yet, which is exactly what a conflict means. A bare 500 here showed "Could not load a comparison — retry" with a button
        // that fails identically forever (the reference's own KNOWN-ISSUES entry).
        Err(NoPair::TooFewFeatures) => Err(ApiError::conflict(
            "need at least 2 features to vote: this project has fewer",
        )),
        Err(NoPair::AllCompared) => Err(ApiError::conflict(
            "no unvoted pairs remain; every pair of this project's features has been compared by you",
        )),
    }
}

#[derive(Debug, Deserialize)]
struct ContributeQuery {
    q: Option<String>,
    language: Option<String>,
    minutes: Option<String>,
    difficulty: Option<String>,
    mentor: Option<String>,
}

/// `GET /contribute/next` — "what can I do today, on something healthy, with the skills I have?"
///
/// The project set, the constraint arithmetic and the ranking all come from `concord-finder`; this function only decides which
/// ROWS are fed in (unclaimed work items, never projects), what the score is broken by (project health), and how the answer is
/// shaped. A second ranker here would drift from the finder's, and the two would disagree about the same catalogue.
async fn contribute_next(
    State(state): State<AppState>,
    headers: HeaderMap,
    Query(query): Query<ContributeQuery>,
) -> Result<Json<Value>, ApiError> {
    let (answers, applied) = contribute_answers(&query)?;
    // Two deliberate defects, reachable only by environment variable, so a probe's controls do not cost a rebuild each. This is a
    // test seam, not a feature switch: nothing in the product sets it, and each of the two names below is asserted to make a
    // specific probe check FAIL. A seam nobody can turn on is a seam nobody can accidentally turn on.
    let mutants: Vec<String> = std::env::var("CONCORD_MUTANT")
        .unwrap_or_default()
        .split(',')
        .map(|name| name.trim().to_string())
        .filter(|name| !name.is_empty())
        .collect();

    let text = query.q.clone().unwrap_or_default();
    let projects = crate::finder::seed_projects(&state, &headers, &text, "", &[]).await?;
    let candidates = concord_db::finder_reads::candidates(&state.pool, &projects)
        .await
        .map_err(internal)?;
    let base: std::collections::BTreeMap<i64, concord_finder::Candidate> =
        candidates.into_iter().map(|candidate| (candidate.project_id, candidate)).collect();

    let mut health = std::collections::BTreeMap::new();
    for project in &projects {
        health.insert(project.id, None);
    }
    let ids: Vec<i64> = projects.iter().map(|project| project.id).collect();
    let measured = concord_db::feed_reads::health(&state.pool, &ids)
        .await
        .map_err(internal)?;
    health.extend(measured);

    let mut pool: Vec<concord_finder::Candidate> = Vec::new();
    let mut meta: std::collections::HashMap<String, serde_json::Value> = std::collections::HashMap::new();
    for project in &projects {
        let inherited = base
            .get(&project.id)
            .map(|candidate| candidate.attrs.clone())
            .unwrap_or_default();
        let score = health.get(&project.id).copied().flatten();
        let items = concord_db::feed_reads::work_items(&state.pool, project.id)
            .await
            .map_err(internal)?;
        for item in items {
            // A CLAIMED item is not available, and offering it would send a newcomer to a task somebody is already doing — the
            // single most discouraging thing a contribution feed can do.
            if item.claimed_by.is_some() && !mutants.iter().any(|name| name == "offer-claimed") {
                continue;
            }
            let mut candidate = concord_finder::work_item_candidate(
                project.id,
                &project.slug,
                &item.title,
                item.id,
                item.time_budget_minutes,
                item.needs_mentor,
                &item.difficulty,
                &inherited,
            );
            // The project-level signals travel with the item, so a rank can be explained without a second query.
            if let Some(project_candidate) = base.get(&project.id) {
                candidate.report = project_candidate.report;
                candidate.reports = project_candidate.reports;
                candidate.arena_r = project_candidate.arena_r;
            }
            let key = candidate.slug.clone();
            pool.push(candidate);
            let mut row = serde_json::json!({
                "id": item.id,
                "title": item.title,
                "kind": item.kind,
                "needs_mentor": item.needs_mentor,
                "project_slug": project.slug,
                "project_name": project.name,
                "health_measured": score.is_some(),
            });
            {
                let object = row.as_object_mut().expect("just built as an object");
                // The reference's `omitempty`: an unmeasured project has NO score field — not a null one, and never a zero. A field
                // that is absent is not a field that says "0%". The same rule applies to the two optional fields below, and the
                // label is built here, in one place, because a page that computed it itself could disagree with the API.
                if let Some(value) = score {
                    object.insert("health".to_string(), serde_json::json!(value));
                }
                if !item.difficulty.is_empty() {
                    object.insert("difficulty".to_string(), Value::String(item.difficulty.clone()));
                }
                if let Some(minutes) = item.time_budget_minutes {
                    object.insert("time_budget_minutes".to_string(), serde_json::json!(minutes));
                }
                object.insert(
                    "health_label".to_string(),
                    Value::String(match score {
                        Some(value) => format!("health {}%", (value * 100.0).round() as i64),
                        None => "health not measured".to_string(),
                    }),
                );
            }
            meta.insert(key, row);
        }
    }

    let mut kept = pool.clone();
    for answer in &answers {
        kept = concord_finder::apply(&kept, answer).0;
    }
    let mut ranked = concord_finder::score(&kept, &answers);
    // The tiebreak the feed exists for: among items of equal fit, the healthier project comes first.
    let ordering_health = if mutants.iter().any(|name| name == "health-ignored") {
        std::collections::BTreeMap::new()
    } else {
        health.clone()
    };
    concord_finder::order_work_items(&mut ranked, &ordering_health);

    const LIMIT: usize = 50;
    let mut items = Vec::with_capacity(ranked.len().min(LIMIT));
    for position in ranked {
        let Some(item) = meta.get(&position.candidate.slug) else {
            // `score` must only return what it was given. A miss means the key contract between the engine's candidate and this
            // map broke, and rendering a wrong title would hide it.
            return Err(internal(format!(
                "the feed lost track of work item {:?}: a bug, not a miss",
                position.candidate.slug
            )));
        };
        let mut row = item.clone();
        let object = row.as_object_mut().ok_or_else(|| internal("item is not an object"))?;
        object.insert("key".to_string(), Value::String(position.candidate.slug.clone()));
        object.insert("fit".to_string(), serde_json::json!(position.fit));
        if !position.matches.is_empty() {
            object.insert("matches".to_string(), serde_json::json!(position.matches));
        }
        if !position.unknown.is_empty() {
            object.insert("unknown".to_string(), serde_json::json!(position.unknown));
        }
        object.insert(
            "why".to_string(),
            Value::String(why_ranked(&position, &applied)),
        );
        items.push(row);
        if items.len() == LIMIT {
            break;
        }
    }

    Ok(Json(serde_json::json!({
        "questions": feed_questions(&pool),
        "items": items,
        "count": items.len(),
        // How many unclaimed items were considered before the answers filtered them, so a short list reads as a small catalogue
        // rather than as a broken filter.
        "pool": pool.len(),
        "applied": applied,
    })))
}

/// The query, as engine answers, plus what was actually applied.
///
/// A value the engine does not know is REFUSED with the parameter named rather than dropped: silently ignoring `minutes=2h` would
/// return the unfiltered feed while the URL says it was filtered.
fn contribute_answers(
    query: &ContributeQuery,
) -> Result<(Vec<concord_finder::Answer>, std::collections::BTreeMap<String, String>), ApiError> {
    use concord_finder::{Answer, DIM_DIFFICULTY, DIM_MENTOR, DIM_MINUTES, MODE_ANY};
    let mut answers = Vec::new();
    let mut applied = std::collections::BTreeMap::new();

    if let Some(language) = query.language.as_deref().map(str::trim).filter(|value| !value.is_empty()) {
        if language != MODE_ANY {
            applied.insert("language".to_string(), language.to_string());
            answers.push(Answer {
                dimension_key: "language".to_string(),
                option_id: language.to_string(),
                mode: concord_finder::MODE_REQUIRED.to_string(),
            });
        }
    }
    if let Some(minutes) = query.minutes.as_deref().map(str::trim).filter(|value| !value.is_empty()) {
        match minutes {
            concord_finder::MINUTES_15 | concord_finder::MINUTES_60 | concord_finder::MINUTES_480 => {
                applied.insert("minutes".to_string(), minutes.to_string());
                answers.push(Answer {
                    dimension_key: DIM_MINUTES.to_string(),
                    option_id: minutes.to_string(),
                    mode: concord_finder::MODE_REQUIRED.to_string(),
                });
            }
            MODE_ANY => {}
            other => {
                return Err(ApiError::invalid_request(format!(
                    "minutes must be one of 15, 60, 480, any (got {other:?})"
                )))
            }
        }
    }
    if let Some(difficulty) = query.difficulty.as_deref().map(str::trim).filter(|value| !value.is_empty()) {
        match difficulty {
            "easy" | "medium" | "hard" => {
                applied.insert("difficulty".to_string(), difficulty.to_string());
                answers.push(Answer {
                    dimension_key: DIM_DIFFICULTY.to_string(),
                    option_id: difficulty.to_string(),
                    mode: concord_finder::MODE_REQUIRED.to_string(),
                });
            }
            MODE_ANY => {}
            other => {
                return Err(ApiError::invalid_request(format!(
                    "difficulty must be one of easy, medium, hard, any (got {other:?})"
                )))
            }
        }
    }
    if let Some(mentor) = query.mentor.as_deref().map(str::trim).filter(|value| !value.is_empty()) {
        match mentor {
            // Required, because "I would rather not be alone" is a constraint: a task whose onboarding depends on somebody who
            // never shows up is the worst outcome this feed can produce.
            "yes" => {
                applied.insert("mentor".to_string(), "yes".to_string());
                answers.push(Answer {
                    dimension_key: DIM_MENTOR.to_string(),
                    option_id: "yes".to_string(),
                    mode: concord_finder::MODE_REQUIRED.to_string(),
                });
            }
            // A PREFERENCE, not a filter: declining a mentor does not make a mentored task impossible, only less attractive.
            "no" => {
                applied.insert("mentor".to_string(), "no".to_string());
                answers.push(Answer {
                    dimension_key: DIM_MENTOR.to_string(),
                    option_id: "no".to_string(),
                    mode: concord_finder::MODE_DECIDE_LATER.to_string(),
                });
            }
            MODE_ANY => {}
            other => {
                return Err(ApiError::invalid_request(format!(
                    "mentor must be one of yes, no, any (got {other:?})"
                )))
            }
        }
    }
    Ok((answers, applied))
}

/// The declared question set, with the language options this instance can actually satisfy — a hard-coded list would offer
/// answers no project here can give.
fn feed_questions(pool: &[concord_finder::Candidate]) -> Vec<concord_finder::Dimension> {
    let mut questions = concord_finder::contribute_questions();
    let mut languages: Vec<String> = pool
        .iter()
        .filter_map(|candidate| candidate.attrs.get("language").cloned())
        .filter(|language| !language.is_empty())
        .collect();
    languages.sort();
    languages.dedup();
    for question in &mut questions {
        if question.key != "language" {
            continue;
        }
        let mut options: Vec<concord_finder::AnswerOption> = languages
            .iter()
            .map(|language| concord_finder::AnswerOption {
                id: language.clone(),
                label: language.clone(),
                impact: 0,
            })
            .collect();
        options.push(concord_finder::AnswerOption {
            id: concord_finder::MODE_ANY.to_string(),
            label: "Any language".to_string(),
            impact: 0,
        });
        question.options = options;
    }
    questions
}

/// The sentence under an item, built only from what the engine reported.
fn why_ranked(
    ranked: &concord_finder::Ranked,
    applied: &std::collections::BTreeMap<String, String>,
) -> String {
    if ranked.matches.is_empty() {
        if applied.is_empty() {
            return "Nothing narrows this list yet — pick an answer above.".to_string();
        }
        // The engine keeps candidates whose value is unknown, so an item with no matching fact is here because it is UNMEASURED,
        // not because it fits. Saying that out loud is the difference between a feed and a guess.
        return "No stated fact matches your answers; it is here because its fit is unknown, not because it fits."
            .to_string();
    }
    let parts: Vec<String> = ranked
        .matches
        .iter()
        .map(|(dimension, value)| format!("{dimension}: {value}"))
        .collect();
    format!("Matches — {}.", parts.join("; "))
}
