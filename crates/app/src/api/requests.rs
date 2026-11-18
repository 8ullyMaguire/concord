//! Request filing, duplicate gate and similar search (step 3, ADR 0014).
//! Reader handlers stay root-owned; their routes move with the writes.

use axum::{
    extract::{Path, Query, State},
    http::{header::HeaderMap, StatusCode},
    response::IntoResponse,
    Extension, Json, Router,
};
use polaris_core::ApiError;
use serde::Deserialize;
use serde_json::{json, Value};

use crate::{
    bearer_user, internal, project_by_id, readable_project, readable_project_segment, require_readable_request,
    require_role, write_audit, AppState,
};

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
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<serde_json::Value>,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
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
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
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

/// Request routes, typed on `AppState` (D-18). GET+POST and GET+PUT
/// pairs stay in one MethodRouter (D-20).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/requests",
            axum::routing::get(list_instance_requests).post(create_instance_request),
        )
        .route("/api/v1/requests/{request_id}", axum::routing::get(one_request))
        .route("/api/v1/similar/{kind}", axum::routing::get(find_similar))
        .route(
            "/api/v1/projects/{project_id}/requests",
            axum::routing::get(list_project_requests).post(create_project_request),
        )
        .route(
            "/api/v1/requests/{request_id}/tags",
            axum::routing::get(request_tags).put(set_request_tags),
        )
}
