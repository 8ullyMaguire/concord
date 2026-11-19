//! The audit log, read from the project's own slug (step 3, ADR 0014).
//!
//! Handlers and routes moved verbatim from `lib.rs`; the filter vocabulary is
//! owned by the store, so only the HTTP shape lives here.

use axum::{
    extract::{Path, Query, State},
    http::HeaderMap,
    Json, Router,
};
use polaris_core::ApiError;
use crate::pagination::PageQuery;
use serde::Deserialize;
use serde_json::Value;

use crate::{bearer_user, internal, require_role, AppState};

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
    headers: HeaderMap,
) -> Result<Json<concord_db::audit::Page>, ApiError> {
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
    let mut page = concord_db::audit::list(&state.pool, &filter)
        .await
        .map_err(internal)?;
    // Undo is a write, so the read decides who MAY be offered it from the caller's real role, not from a
    // client-side guess. Anonymous readers still get the log; they simply get no undo controls.
    let caller = bearer_user(&state, &headers).await.ok().flatten();
    let may_undo = match &caller {
        Some(user) => require_role(&state, &project, "maintainer", user).await.is_ok(),
        None => false,
    };
    for entry in &mut page.entries {
        entry.can_undo = may_undo && entry.action == "arena.remove" && !entry.undone;
    }
    Ok(Json(page))
}

/// `GET /api/v1/projects/{slug}/audit/actions` — the filter vocabulary, alphabetical.
async fn list_audit_actions(
    State(state): State<AppState>,
    Query(page_query): Query<PageQuery>,
    Path(slug): Path<String>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let page = page_query.valid().map_err(ApiError::invalid_request)?;
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let actions = concord_db::audit::actions(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(crate::pagination::page_json(actions, &page)?)
}

/// Audit routes, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{slug}/audit",
            axum::routing::get(list_audit),
        )
        .route(
            "/api/v1/projects/{slug}/audit/actions",
            axum::routing::get(list_audit_actions),
        )
}
