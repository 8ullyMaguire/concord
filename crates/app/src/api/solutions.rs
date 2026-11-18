//! Solutions (step 3, ADR 0014): the project/feature reads and the write.
//!
//! Handlers lifted verbatim from `lib.rs`; only visibility and `routes()` assembly
//! are new. Shared resolvers are borrowed from the crate root (D-19/D-23).

use axum::{
    extract::{Path, Query, State},
    http::{header::HeaderMap, StatusCode},
    Extension, Json, Router,
};
use polaris_core::ApiError;

use crate::{
    bearer_user, caller_feature, internal, project_by_id, readable, readable_project,
    readable_project_segment, AppState, STANDINGS_PER_FEATURE,
};


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
    Path(project): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The segment may be an id or a slug: the reference's page scripts build these URLs from the SLUG, and a path extractor typed
    // `i64` rejected the slug before any handler ran (`Invalid URL: Cannot parse `suki` to a `i64``). One resolver, asked once, at
    // the top of the handler — the `project_id` the body uses is its id.
    let project_id = project_by_id(&state, &project).await?.id;
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
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<CreateSolutionRequest>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
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



/// All three solution paths, typed on `AppState`. The feature path keeps both
/// GET and POST in one MethodRouter, exactly as the old chain declared it.
pub fn routes() -> Router<AppState> {
    Router::new()
        .route("/api/v1/projects/{project_id}/solutions", axum::routing::get(project_solutions))
        .route(
            "/api/v1/projects/{project_id}/features/{id}/solutions",
            axum::routing::get(feature_solutions).post(create_solution),
        )
        .route(
            "/api/v1/projects/{project_id}/features/{id}/solutions/{solution_id}",
            axum::routing::get(one_solution),
        )
        .route(
            "/api/v1/projects/{project_id}/rank/composite",
            axum::routing::post(composite_rank),
        )
}
