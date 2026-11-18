//! The solution arena's pairwise vote (step 3, ADR 0014).
//!
//! This section — request struct, handler and refusal mapping — moved verbatim
//! from `lib.rs`. All four helpers it borrows already live in the crate root.

use axum::{
    extract::{Path, State},
    http::HeaderMap,
    Json, Router,
};
use polaris_core::ApiError;

use crate::{
    bearer_user, caller_feature, internal, project_by_id, readable, readable_project_segment,
    STANDINGS_PER_FEATURE, AppState,
};

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

/// The standalone solution-vote route, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{project_id}/features/{id}/solutions/vote",
            axum::routing::post(vote_solutions),
        )
        .route(
            "/api/v1/projects/{project_id}/arenas/{arena_id}/next-pair",
            axum::routing::get(arena_next_pair),
        )
        .route(
            "/api/v1/projects/{project_id}/arenas/{arena_id}/verification",
            axum::routing::get(arena_verification),
        )
}
