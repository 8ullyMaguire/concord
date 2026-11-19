//! Board reads and card moves (step 3, ADR 0014).
//!
//! Handlers and their query structs lifted verbatim from `lib.rs`; only visibility
//! and `routes()` assembly are new. Shared helpers stay in the app root.

use axum::{
    extract::{Path, Query, State},
    http::HeaderMap,
    Json, Router,
};
use serde::Deserialize;
use serde_json::{json, Value};
use polaris_core::ApiError;

use crate::{bearer_user, internal, AppState};

/// `GET /api/v1/projects/{slug}/board[?milestone=ID]` — the board, optionally NARROWED to one milestone's features.
///
/// The reference's rule, kept because each half of it is a decision: ONE board filtered, never a board per milestone (a column's WIP limit
/// is a statement about the project's capacity, and a limit that applied to a fifth of the work would be five times too generous); an
/// absent or unparseable `milestone` means the WHOLE board (a filter nobody asked for would change the default page, and a typo must not
/// empty it); and a card with no feature is dropped under a filter, because it belongs to no milestone and a filter is a claim about
/// membership.
///
/// The port answered `?milestone=` with the whole board — a filter that silently does nothing is worse than one that errors, because the
/// reader believes it. `milestones_e2e` caught it by counting a card that should not be there.
async fn get_board(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Query(query): Query<BoardQuery>,
) -> Result<Json<concord_db::projects::Board>, ApiError> {
    let Some(mut board) = concord_db::projects::board_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
    else {
        return Err(ApiError::not_found(format!("project {slug}")));
    };
    let milestone = query
        .milestone
        .as_deref()
        .and_then(|raw| raw.parse::<i64>().ok())
        .filter(|id| *id > 0);
    if let Some(milestone) = milestone {
        // `Board` carries no project id, so it is read here rather than added to the shape every client sees.
        let project_id = concord_db::projects::project_by_slug(&state.pool, &slug)
            .await
            .map_err(internal)?
            .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?
            .id;
        let membership = concord_db::projects::feature_milestones(&state.pool, project_id)
            .await
            .map_err(internal)?;
        board.cards.retain(|card| {
            card.feature_id != 0 && membership.get(&card.feature_id).copied() == Some(milestone)
        });
    }
    Ok(Json(board))
}

/// The board's query string. `milestone` is the only parameter, and it is a STRING here rather than an `Option<i64>` so that an
/// unparseable value can mean "the whole board" instead of failing the request — the reference's parser does the same.
#[derive(Debug, Deserialize)]
struct BoardQuery {
    #[serde(default)]
    milestone: Option<String>,
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
    let actor = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let to = query
        .to
        .ok_or_else(|| ApiError::invalid_request("to is required"))?;
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    match concord_db::projects::move_card_with_event(
        &state.pool,
        project.id,
        card_id,
        query.feature_id.unwrap_or(0),
        &to,
        actor.id,
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



/// Both board routes, typed on `AppState` so the merged chain keeps its state (D-18).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route("/api/v1/projects/{slug}/board", axum::routing::get(get_board))
        .route(
            "/api/v1/projects/{slug}/board/cards/{card_id}/move",
            axum::routing::put(move_card),
        )
}
