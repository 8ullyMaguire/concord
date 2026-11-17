//! The three operator-page reads (step 3, ADR 0014).
//!
//! Lifted verbatim from `lib.rs`. The emergency-hold write that shared the
//! source section stays in `lib.rs` — different resource (D-28).

use axum::{extract::{Query, State}, Json, Router};
use polaris_core::ApiError;
use serde_json::Value;

use crate::{internal, AppState};

// ── Three reads an operator page stands on ──────────────────────────────────────────────────────────────────────

/// `GET /admin-ledger` — §9.3's public ledger of admin actions.
///
/// **No token, no project filter, no role gate** — and that is the section, not an oversight: an admin with no content authority is
/// only a real constraint when anyone can watch them. A gate here would be the constraint watching itself.
async fn list_admin_ledger(
    State(state): State<AppState>,
    Query(query): Query<std::collections::HashMap<String, String>>,
) -> Result<Json<Value>, ApiError> {
    // A limit that is not a number is IGNORED rather than refused: an audit viewer is read by hand and typed into, and a 400 on a
    // typo is worse than a default page.
    let limit = query
        .get("limit")
        .and_then(|value| value.trim().parse::<i64>().ok());
    let entries = concord_db::ops_reads::admin_ledger(&state.pool, limit)
        .await
        .map_err(internal)?;
    // Control: the reference's own wart — `null` for an empty page, which is the thing this route exists not to repeat.
    if entries.is_empty()
        && std::env::var("CONCORD_MUTANT")
            .unwrap_or_default()
            .split(',')
            .any(|name| name.trim() == "admin-ledger-null")
    {
        return Ok(Json(Value::Null));
    }
    // `[]`, never `null`. The reference answers the literal null for an empty page and every consumer survives only because it
    // happens to write `|| []`; that makes the next client's guard load-bearing, silently.
    Ok(Json(serde_json::json!(entries)))
}

/// `GET /stats/rising` — §4.5.5.7's ranking, by organic work gained per week.
async fn stats_rising(State(state): State<AppState>) -> Result<Json<Value>, ApiError> {
    let rows = concord_db::ops_reads::rising(&state.pool).await.map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

/// `GET /lists` — the lists that belong to no project.
async fn instance_lists(State(state): State<AppState>) -> Result<Json<Value>, ApiError> {
    let rows = concord_db::ops_reads::instance_lists(&state.pool)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}



/// All three reads, typed on `AppState`. The `/lists` route was registered
/// elsewhere in the old chain; merging it here keeps the same path and method.
pub fn routes() -> Router<AppState> {
    Router::new()
        .route("/api/v1/admin-ledger", axum::routing::get(list_admin_ledger))
        .route("/api/v1/stats/rising", axum::routing::get(stats_rising))
        .route("/api/v1/lists", axum::routing::get(instance_lists))
}
