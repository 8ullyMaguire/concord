//! S2 alternatives arenas (step 3, ADR 0014).
//!
//! The whole section — four handlers, the arena resolver and both refusal maps —
//! moved verbatim from `lib.rs`; only visibility and `routes()` assembly are new.

use axum::{
    extract::{Path, State},
    http::{header::HeaderMap, StatusCode},
    Extension, Json, Router,
};
use polaris_core::ApiError;
use serde::Deserialize;

use crate::{bearer_user, internal, readable, readable_project_segment, require_role, AppState};

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
    let user = bearer_user(&state, &headers).await?;
    let arenas = concord_db::alternatives::list(&state.pool, project.id, concord_db::alternatives::KIND)
        .await
        .map_err(internal)?;
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
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    body: Option<Json<CreateAlternative>>,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
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
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    body: Option<Json<AddCompetitor>>,
) -> Result<Json<serde_json::Value>, ApiError> {
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
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    body: Option<Json<AlternativesVote>>,
) -> Result<Json<serde_json::Value>, ApiError> {
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



/// The three alternatives routes, typed on `AppState` (D-18). The list path
/// keeps both GET and POST in one MethodRouter as the old chain declared it.
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{slug}/alternatives",
            axum::routing::get(list_alternatives).post(create_alternative),
        )
        .route(
            "/api/v1/projects/{slug}/alternatives/{arena}/entries",
            axum::routing::post(add_alternative_competitor),
        )
        .route(
            "/api/v1/projects/{slug}/alternatives/{arena}/vote",
            axum::routing::post(cast_alternatives_vote),
        )
}
