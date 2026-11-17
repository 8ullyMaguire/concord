//! The arena rating replay (step 3, ADR 0014).
//!
//! The forge webhook shares the source section but is a separate resource and
//! stays in `lib.rs` (D-28). The `app_mutant("recompute-count-rows")` branch and
//! the membership-before-role order are byte-identical (D-35).

use axum::{
    extract::{Path, State},
    Extension, Json, Router,
};
use polaris_core::ApiError;
use serde_json::Value;

use crate::{app_mutant, internal, require_role, AppState};

// ── The two writes nobody clicks ────────────────────────────────────────────────────────────────────────────────

/// `POST /projects/{slug}/arenas/{id}/recompute` — replay the vote log over the stored ratings.
///
/// Votes are applied incrementally, and an incremental application of a rating system drifts for reasons with no common shape: a
/// failed request between the game and the entry update, a manual fix, a bug shipped for an afternoon. The replay is the only answer
/// to "are these numbers right" as opposed to "these are the numbers the code produced".
///
/// **Only a contributor, and only in the arena's own project.** An arena id from another project answers 404 rather than 403: an
/// enumerable id that confirms existence is the failure mode, and a rating is not worth leaking a project's arena ids for.
async fn recompute_arena(
    State(state): State<AppState>,
    Path((slug, arena_id)): Path<(String, i64)>,
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<Json<Value>, ApiError> {
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such project"))?;
    // The arena's membership in the project is checked BEFORE the role: a role check first answers 403 "not your project" for an
    // arena that is not in this project either, which confirms the arena exists somewhere. 404 for both differences.
    let belongs: Option<i64> =
        sqlx::query_scalar("select id from arenas where id = $1 and project_id = $2")
            .bind(arena_id)
            .bind(project.id)
            .fetch_optional(&state.pool)
            .await
            .map_err(internal)?;
    if belongs.is_none() {
        return Err(ApiError::not_found("no such arena in this project"));
    }
    require_role(&state, &project, "contributor", &user).await?;
    // The drift is measured BEFORE the write, because afterwards the two agree by construction and the count is always zero —
    // a caller could not distinguish "nothing had drifted" from "I fixed twelve".
    // `verify` returns one row per entry with an `agrees` flag; counting ROWS would report every entry as drift and make the number
    // meaningless.
    let verifications = concord_db::arena::verify(&state.pool, arena_id)
        .await
        .map_err(internal)?;
    let changed = if app_mutant("recompute-count-rows") {
        // Control: count ROWS, not disagreements — the number then says "every entry" whatever state the arena is in.
        verifications.len()
    } else {
        verifications
            .iter()
            .filter(|verification| !verification.agrees)
            .count()
    };
    let entries = concord_db::arena_writes::recompute(&state.pool, arena_id)
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!({
        "arena_id": arena_id,
        "changed": changed,
        "entries": serde_json::to_value(entries).map_err(internal)?,
    })))
}


/// The recompute route, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new().route(
        "/api/v1/projects/{slug}/arenas/{arena_id}/recompute",
        axum::routing::post(recompute_arena),
    )
}
