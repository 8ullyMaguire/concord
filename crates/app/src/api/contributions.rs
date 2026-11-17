//! The contribution feed and the two reads under it (step 3, ADR 0014).
//!
//! Moved verbatim from `lib.rs`. Field reports and the emergency hold share
//! the old source area but are different resources and stay put (D-28).

use axum::{
    extract::{Path, Query, State},
    http::header::HeaderMap,
    Extension, Json, Router,
};
use polaris_core::ApiError;
use serde::Deserialize;
use serde_json::Value;

use crate::{internal, readable_project_segment, AppState};

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
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
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
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    use concord_db::feed_reads::NoPair;
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



/// The four contribution routes, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/contribute/next",
            axum::routing::get(contribute_next),
        )
        .route(
            "/api/v1/projects/{slug}/work-items",
            axum::routing::get(list_work_items),
        )
        .route(
            "/api/v1/projects/{slug}/join",
            axum::routing::post(join_project),
        )
        .route(
            "/api/v1/projects/{slug}/votes/next",
            axum::routing::get(next_vote_pair),
        )
}
