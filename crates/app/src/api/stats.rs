//! Instance statistics and search (step 3, ADR 0014).

use axum::{
    extract::{Query, State},
    http::header::HeaderMap,
    Json, Router,
};
use polaris_core::ApiError;
use serde::Deserialize;
use serde_json::Value;

use crate::{bearer_user, internal, AppState};

/// `GET /api/v1/stats` — the one payload the statistics page renders from.
///
/// The page computes nothing itself: a page that recomputes an aggregate the API returns is two answers
/// waiting to disagree, and the page is the one nobody notices is wrong.
async fn stats(State(state): State<AppState>) -> Result<Json<concord_db::stats::StatsPayload>, ApiError> {
    let payload = concord_db::stats::payload(&state.pool)
        .await
        .map_err(internal)?;
    Ok(Json(payload))
}

/// `GET /api/v1/stats/summary` — the census alone, for the reader that wants the instance's own numbers
/// without accepting anything first.
async fn stats_summary(State(state): State<AppState>) -> Result<Json<concord_db::stats::Census>, ApiError> {
    let census = concord_db::stats::census(&state.pool)
        .await
        .map_err(internal)?;
    Ok(Json(census))
}

/// `GET /api/v1/search` — §15's discovery endpoint.
///
/// A malformed `min_lang_pct`, `min_health`, `model` or `sort` is a 400 that names the range, not a silently
/// ignored parameter: a filter a reader set and the server dropped returns hits they cannot explain.
async fn search(
    State(state): State<AppState>,
    Query(params): Query<SearchParams>,
    headers: HeaderMap,
) -> Result<Json<concord_db::search::SearchResult>, ApiError> {
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
    Ok(Json(result))
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
) -> Result<Json<Vec<concord_db::stats::Snapshot>>, ApiError> {
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
    Ok(Json(rows))
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
) -> Result<Json<Vec<concord_db::stats::ResponsivenessRow>>, ApiError> {
    let rows = concord_db::stats::responsiveness_leaderboard(&state.pool)
        .await
        .map_err(internal)?;
    Ok(Json(rows))
}

/// Stats and search routes, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route("/api/v1/search", axum::routing::get(search))
        .route("/api/v1/stats", axum::routing::get(stats))
        .route("/api/v1/stats/summary", axum::routing::get(stats_summary))
        .route("/api/v1/stats/history", axum::routing::get(stats_history))
        .route(
            "/api/v1/stats/responsiveness",
            axum::routing::get(stats_responsiveness),
        )
}
