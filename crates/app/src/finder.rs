//! The finder's routes (§4, §5) and its session store.
//!
//! **Sessions are in memory, not in a table — and that is a decision, not an oversight.** Finder §4.6 offers "save a session" and
//! a shareable results URL, and both are expressible from an answer list without a table. The reference keeps them in a mutex
//! around a map and says why; the port keeps them the same way, because a migration for the feature whose only durable need is "a
//! list of answers" is not yet earned, and a *port* that added a table the Go service does not have would be adding durability as a
//! side effect of ownership. The cost is the same and is stated to the client: `persisted: false` travels in every state response,
//! and an unsaved session is lost on restart — which for an exploration nobody committed to is the honest behaviour rather than a
//! silent failure.
//!
//! What the port cannot copy blindly is the reference's session id, which is `f<unix-nano>-<seq>`: fine in a single process, and
//! still fine here because the service is one process. It is kept identical so a session id from the Go service looks like a
//! session id here.

use std::collections::BTreeMap;
use std::sync::{Mutex, OnceLock};

use axum::{
    extract::{Path, State},
    http::{HeaderMap, StatusCode},
    Json,
};
use polaris_core::ApiError;
use serde_json::{json, Value};

use super::{bearer_user, internal, readable, AppState};

/// One finder session: the seed, the surviving candidates, and the answers so far.
#[derive(Debug, Clone)]
pub struct Session {
    /// The id a client holds between requests.
    pub id: String,
    /// The seed as it was given, so "back" and "lift" can rebuild from the same starting point.
    pub seed_text: String,
    /// The seed's category.
    pub seed_category: String,
    /// The seed's explicit project ids, if the caller named any.
    pub seed_ids: Vec<i64>,
    /// The surviving candidates.
    pub candidates: Vec<concord_finder::Candidate>,
    /// Every answer given, in order.
    pub answers: Vec<concord_finder::Answer>,
    /// Which dimensions have been asked.
    pub asked: Vec<String>,
    /// How many required answers.
    pub depth: i32,
    /// The previous question's family, for the diversity rule.
    pub last_family: String,
    /// How many candidates the session started with.
    pub initial_count: usize,
    /// What each answered filter removed, for the results page's "filtered out" list.
    pub removed: Vec<Value>,
    /// When it was created, for the age-out.
    pub created_at: std::time::Instant,
}

/// The process-wide session table.
///
/// One map behind one mutex, exactly the reference's shape. Pruned on every write: an abandoned exploration must not accumulate
/// for the life of the process, and a session is cheap to recreate.
fn sessions() -> &'static Mutex<BTreeMap<String, Session>> {
    static SESSIONS: OnceLock<Mutex<BTreeMap<String, Session>>> = OnceLock::new();
    SESSIONS.get_or_init(|| Mutex::new(BTreeMap::new()))
}

/// Sessions older than this are dropped when any session is created.
const SESSION_MAX_AGE: std::time::Duration = std::time::Duration::from_secs(3600);

fn new_session_id() -> String {
    static SEQ: std::sync::atomic::AtomicI64 = std::sync::atomic::AtomicI64::new(0);
    let seq = SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
    let nanos = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or(0);
    format!("f{nanos}-{seq}")
}

fn put_session(session: Session) {
    let mut table = sessions().lock().unwrap_or_else(|e| e.into_inner());
    table.retain(|_, existing| existing.created_at.elapsed() < SESSION_MAX_AGE);
    table.insert(session.id.clone(), session);
}

fn get_session(id: &str) -> Option<Session> {
    let table = sessions().lock().unwrap_or_else(|e| e.into_inner());
    table.get(id).cloned()
}

/// Replace a session after a change.
fn replace_session(session: Session) {
    let mut table = sessions().lock().unwrap_or_else(|e| e.into_inner());
    table.insert(session.id.clone(), session);
}

/// No session under that id.
fn no_session() -> ApiError {
    ApiError::not_found("no such finder session")
}

/// Load the projects a seed names, enforcing visibility **once**, here.
///
/// A candidate set built from a private project would leak it through the Finder even though every other surface respects the
/// four visibility levels. An explicit id list that names a project the caller cannot see is a 404, not a 403: "a project you
/// cannot see" and "a project that does not exist" must be indistinguishable, or Finder becomes an enumeration oracle.
async fn seed_projects(
    state: &AppState,
    headers: &HeaderMap,
    text: &str,
    category: &str,
    ids: &[i64],
) -> Result<Vec<concord_db::projects::Project>, ApiError> {
    if !ids.is_empty() {
        let mut out = Vec::with_capacity(ids.len());
        for id in ids {
            let project = concord_db::projects::project_by_id(&state.pool, *id)
                .await
                .map_err(internal)?
                .ok_or_else(|| ApiError::not_found(format!("project {id}")))?;
            let user = bearer_user(state, headers).await?;
            if !readable(state, &project, user.as_ref()).await? {
                return Err(ApiError::not_found(format!("project {id}")));
            }
            out.push(project);
        }
        return Ok(out);
    }
    // The seed is a pre-filter, not a hard filter (§4.1): it narrows what is on offer, and an empty seed is the whole catalogue
    // the caller may see.
    let query = if text.trim().is_empty() {
        category.trim().to_string()
    } else {
        text.trim().to_string()
    };
    let actor = bearer_user(state, headers).await?.map_or(0, |user| user.id);
    let result = concord_db::search::search(
        &state.pool,
        &query,
        &concord_db::search::SearchFilters::default(),
        "relevance",
        actor,
    )
    .await
    .map_err(internal)?;
    Ok(result.results.into_iter().map(|hit| hit.project).collect())
}

/// The candidate set for a seed, with the widened fallback.
async fn seed_candidates(
    state: &AppState,
    headers: &HeaderMap,
    text: &str,
    category: &str,
    ids: &[i64],
) -> Result<Vec<concord_finder::Candidate>, ApiError> {
    let projects = seed_projects(state, headers, text, category, ids).await?;
    let mut candidates = concord_db::finder_reads::candidates(&state.pool, &projects)
        .await
        .map_err(internal)?;
    // A seed that matches NOTHING is widened to the whole catalogue (§4.1: a hint that matched nothing must not leave the user
    // with an empty session). Only the zero case: a legitimately narrow seed is §4.4 working as specified, and widening it would
    // hand back projects the user excluded. Never for an explicit id list — the caller named those projects, which is what makes a
    // Finder URL shareable to a chosen set.
    if candidates.is_empty() && ids.is_empty() && (!text.trim().is_empty() || !category.trim().is_empty()) {
        if let Ok(wide) = seed_projects(state, headers, "", "", &[]).await {
            candidates = concord_db::finder_reads::candidates(&state.pool, &wide)
                .await
                .map_err(internal)?;
        }
    }
    Ok(candidates)
}

/// The family of a dimension key, for the diversity rule.
fn family_for(key: &str) -> String {
    let probe = concord_finder::Candidate {
        project_id: 0,
        slug: String::new(),
        name: String::new(),
        attrs: {
            let mut attrs = BTreeMap::new();
            attrs.insert(key.to_string(), "x".to_string());
            attrs
        },
        report: 0.0,
        reports: 0,
        arena_r: 0.0,
    };
    let state = concord_finder::State {
        candidates: vec![probe],
        ..concord_finder::State::default()
    };
    concord_finder::all_dimensions(&state)
        .into_iter()
        .find(|dimension| dimension.key == key)
        .map_or_else(String::new, |dimension| dimension.family)
}

fn engine_state(session: &Session) -> concord_finder::State {
    concord_finder::State {
        candidates: session.candidates.clone(),
        answers: session.answers.clone(),
        asked: session.asked.clone(),
        depth: session.depth,
        last_family: session.last_family.clone(),
    }
}

fn candidate_dto(ranked: &concord_finder::Ranked) -> Value {
    json!({
        "project_id": ranked.candidate.project_id,
        "slug": ranked.candidate.slug,
        "name": ranked.candidate.name,
        "fit_score": ranked.fit,
        // Coverage travels with fit because a fit number without it is not readable: 60% fit backed by full evidence and 60%
        // fit backed by a third of it are different facts.
        "evidence_coverage": ranked.explanation.get("evidence_coverage").copied().unwrap_or(0.0),
        "matches": ranked.matches,
        "warnings": ranked.warnings,
        "unknown": ranked.unknown,
        "explanation": ranked.explanation,
    })
}

fn candidate_dtos(ranked: &[concord_finder::Ranked]) -> Vec<Value> {
    ranked.iter().map(candidate_dto).collect()
}

/// The candidates the engine cannot rank confidently: those carrying at least one unknown field. Separate from the main list
/// because "we don't have enough data on this" and "this is a poor match" are different answers, and folding them together is how
/// a project nobody has contributed data for becomes invisible.
fn unknown_fit(ranked: &[concord_finder::Ranked]) -> Vec<Value> {
    ranked
        .iter()
        .filter(|row| !row.unknown.is_empty())
        .map(candidate_dto)
        .collect()
}

/// The response every session endpoint returns.
fn session_state(session: &Session) -> Value {
    let state = engine_state(session);
    let (stop, reason) = concord_finder::should_stop(&state);
    let ranked = concord_finder::score(&session.candidates, &session.answers);
    let mut top = candidate_dtos(&ranked);
    top.truncate(5);

    // A question is only offered when there is a reason to ask it. On a stop condition the caller gets the stop reason and the
    // gaps instead, which is the honest end of a session the catalogue cannot narrow further.
    let question = if stop {
        Value::Null
    } else {
        match concord_finder::next_question(&state) {
            Ok((dimension, gain_bits)) => json!({
                "id": dimension.key,
                "key": dimension.key,
                "family": dimension.family,
                "text": question_text(&dimension),
                "kind": dimension.kind,
                "options": dimension.options,
                "expected_information_gain": gain_bits,
                "known_fraction": dimension.known_fraction,
                "can_skip": true,
                "why_asked": why_asked(&dimension, session.candidates.len()),
            }),
            Err(_) => Value::Null,
        }
    };

    json!({
        "session_id": session.id,
        "candidate_count": session.candidates.len(),
        "initial_candidate_count": session.initial_count,
        "questions_asked": session.depth,
        "question": question,
        "top_candidates": top,
        "should_suggest_stop": stop,
        "stop_reason": reason,
        "gaps": concord_finder::gaps(&state),
        "weights": concord_finder::weight_report(),
        "answers": session.answers,
        "filtered_out": session.removed,
        "persisted": false,
    })
}

/// The question as a sentence a person can answer: the plain-language form, with the canonical key available separately.
fn question_text(dimension: &concord_finder::Dimension) -> String {
    if dimension.key.starts_with("cap:") {
        format!("Does {} matter to you?", dimension.label)
    } else {
        dimension.label.clone()
    }
}

/// Why this question, in terms the user can act on.
///
/// The first version of this in the reference picked the option that removes the MOST candidates and stated it absolutely —
/// "choosing nim would narrow 69 of 70". That is true of every option of the language dimension, because "most removed" is the
/// rarest option rather than the most informative one. §4.2 shows the shape: both ends of the split, side by side, and a flat
/// split says so rather than being called the biggest.
fn why_asked(dimension: &concord_finder::Dimension, total: usize) -> String {
    let mut least: Option<&concord_finder::AnswerOption> = None;
    let mut most: Option<&concord_finder::AnswerOption> = None;
    for option in &dimension.options {
        if option.id == concord_finder::MODE_ANY {
            continue;
        }
        match most {
            None => {
                least = Some(option);
                most = Some(option);
            }
            Some(current) => {
                if option.impact > current.impact {
                    most = Some(option);
                }
                if let Some(smallest) = least {
                    if option.impact < smallest.impact {
                        least = Some(option);
                    }
                }
            }
        }
    }
    let (Some(most), Some(least)) = (most, least) else {
        return String::new();
    };
    let total_i64 = i64::try_from(total).unwrap_or(i64::MAX);
    let survive_best = total_i64 - most.impact;
    let survive_worst = total_i64 - least.impact;
    let spread = survive_worst - survive_best;
    // Flat means the answer barely matters. The measure is how many candidates SURVIVE the best answer, not how many the worst
    // removes: 69 versus 53 sounds decisive and is not, because both leave a short-list of essentially the same size.
    if total_i64 > 0
        && (least.impact >= total_i64 * 9 / 10
            || survive_best <= total_i64 / 10
            || spread * 10 <= total_i64)
    {
        return format!(
            "Every answer here leaves only {survive_best} to {survive_worst} of {total_i64} candidates, so this question tells \
             us little about which project is right."
        );
    }
    if least.impact == most.impact {
        return "Every answer here rules out the same number of candidates, which usually means the catalogue holds one value \
                for this and cannot narrow further."
            .to_string();
    }
    format!(
        "\"{}\" would rule out {} of {total_i64} candidates; \"{}\" only {} — the biggest split left.",
        most.label, most.impact, least.label, least.impact
    )
}

#[derive(Debug, serde::Deserialize, Default)]
pub(crate) struct StartRequest {
    #[serde(default)]
    text: String,
    #[serde(default)]
    category: String,
    #[serde(default)]
    seed_candidates: Vec<i64>,
}

/// `GET /finder/questions` — every dimension the engine can ask about, with its live gain.
///
/// This exists because §5.4's promise — "Finder never asks about an option the catalogue can't actually deliver" — is otherwise
/// only checkable by driving the whole UI. Here it is one request, and a dimension below the threshold appears with
/// `askable: false`.
pub(super) async fn finder_questions(
    State(state): State<AppState>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let candidates = seed_candidates(&state, &headers, "", "", &[]).await?;
    let engine = concord_finder::State {
        candidates,
        ..concord_finder::State::default()
    };
    let mut dimensions = Vec::new();
    for dimension in concord_finder::all_dimensions(&engine) {
        let values: Vec<&str> = dimension
            .options
            .iter()
            .filter(|option| option.id != concord_finder::MODE_ANY)
            .map(|option| option.id.as_str())
            .collect();
        dimensions.push(json!({
            "key": dimension.key,
            "family": dimension.family,
            "label": dimension.label,
            "gain_bits": dimension.gain_bits,
            "known_fraction": dimension.known_fraction,
            "askable": dimension.gain_bits >= concord_finder::MIN_GAIN_BITS,
            "values_present": values,
        }));
    }
    Ok(Json(json!({
        "candidate_count": engine.candidates.len(),
        "min_gain_bits": concord_finder::MIN_GAIN_BITS,
        "dimensions": dimensions,
        "gaps": concord_finder::gaps(&engine),
        "weights": concord_finder::weight_report(),
    })))
}

/// `POST /finder/sessions` — start one.
pub(super) async fn start_finder_session(
    State(state): State<AppState>,
    headers: HeaderMap,
    body: Option<Json<StartRequest>>,
) -> Result<(StatusCode, Json<Value>), ApiError> {
    let body = body.map(|Json(body)| body).unwrap_or_default();
    let candidates = seed_candidates(
        &state,
        &headers,
        &body.text,
        &body.category,
        &body.seed_candidates,
    )
    .await?;
    let session = Session {
        id: new_session_id(),
        seed_text: body.text,
        seed_category: body.category,
        seed_ids: body.seed_candidates,
        initial_count: candidates.len(),
        candidates,
        answers: Vec::new(),
        asked: Vec::new(),
        depth: 0,
        last_family: String::new(),
        removed: Vec::new(),
        created_at: std::time::Instant::now(),
    };
    let response = session_state(&session);
    put_session(session);
    Ok((StatusCode::CREATED, Json(response)))
}

/// `GET /finder/sessions/{id}`.
pub(super) async fn get_finder_session(
    State(_state): State<AppState>,
    Path(session_id): Path<String>,
) -> Result<Json<Value>, ApiError> {
    let session = get_session(&session_id).ok_or_else(no_session)?;
    Ok(Json(session_state(&session)))
}

#[derive(Debug, serde::Deserialize, Default)]
pub(crate) struct AnswerRequest {
    #[serde(default)]
    question_key: String,
    #[serde(default)]
    option_id: String,
    #[serde(default)]
    mode: String,
}

/// `POST /finder/sessions/{id}/answers` — record one answer and re-derive the state.
pub(super) async fn answer_finder_question(
    State(_state): State<AppState>,
    Path(session_id): Path<String>,
    body: Option<Json<AnswerRequest>>,
) -> Result<Json<Value>, ApiError> {
    let body = body.map(|Json(body)| body).unwrap_or_default();
    let mut session = get_session(&session_id).ok_or_else(no_session)?;
    if body.question_key.is_empty() {
        return Err(ApiError::invalid_request("question_key is required"));
    }
    let mode = if body.mode.is_empty() {
        concord_finder::MODE_REQUIRED.to_string()
    } else {
        body.mode.clone()
    };
    // A mode that does not filter must not change the set — checked here as well as in the engine, because this is the boundary
    // where a bug would become invisible to a user.
    if ![
        concord_finder::MODE_REQUIRED,
        concord_finder::MODE_SKIP,
        concord_finder::MODE_DOESNT_MATTER,
        concord_finder::MODE_DECIDE_LATER,
    ]
    .contains(&mode.as_str())
    {
        return Err(ApiError::invalid_request(
            "mode must be required, skip, doesnt-matter or decide-later",
        ));
    }
    let answer = concord_finder::Answer {
        dimension_key: body.question_key.clone(),
        option_id: body.option_id.clone(),
        mode: mode.clone(),
    };
    let (kept, removed_by) = concord_finder::apply(&session.candidates, &answer);
    let removed_total: i64 = removed_by.values().sum();
    session.answers.push(answer);
    if !session.asked.contains(&body.question_key) {
        session.asked.push(body.question_key.clone());
    }
    if mode == concord_finder::MODE_REQUIRED {
        session.depth += 1;
    }
    session.candidates = kept;
    session.last_family = family_for(&body.question_key);
    if removed_total > 0 {
        // The reference records the filter and a reason, and not the exports: a "filtered out" list that named individual
        // projects would tell a visitor which private-adjacent projects the catalogue holds.
        session.removed.push(json!({
            "slug": "",
            "name": "",
            "reasons": [format!("filtered by {}", body.question_key)],
            "filters": [body.question_key.clone()],
        }));
    }
    let response = session_state(&session);
    replace_session(session);
    Ok(Json(response))
}

/// `POST /finder/sessions/{id}/back` — drop the last answer and RECOMPUTE from the catalogue.
///
/// §2.6: "the user's answers are a graph, not a stack", and §6.2: changing an earlier answer recomputes rather than replaying a
/// stored path. So this re-derives the set from the remaining answers against the CURRENT catalogue — it does not pop a snapshot,
/// because a snapshot is exactly what goes stale the moment the catalogue changes.
pub(super) async fn back_finder(
    State(state): State<AppState>,
    Path(session_id): Path<String>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let mut session = get_session(&session_id).ok_or_else(no_session)?;
    if session.answers.is_empty() {
        return Err(ApiError::invalid_request("nothing to go back from"));
    }
    session.answers.pop();
    session.depth = 0;
    session.asked.clear();
    for answer in &session.answers {
        if !session.asked.contains(&answer.dimension_key) {
            session.asked.push(answer.dimension_key.clone());
        }
        if answer.mode == concord_finder::MODE_REQUIRED {
            session.depth += 1;
        }
    }
    let base = seed_candidates(
        &state,
        &headers,
        &session.seed_text,
        &session.seed_category,
        &session.seed_ids,
    )
    .await?;
    let mut set = base;
    for answer in &session.answers {
        let (kept, _) = concord_finder::apply(&set, answer);
        set = kept;
    }
    session.candidates = set;
    session.last_family = session
        .answers
        .last()
        .map(|answer| family_for(&answer.dimension_key))
        .unwrap_or_default();
    let response = session_state(&session);
    replace_session(session);
    Ok(Json(response))
}

#[derive(Debug, serde::Deserialize, Default)]
pub(crate) struct LiftRequest {
    #[serde(default)]
    question_key: String,
}

/// `POST /finder/sessions/{id}/lift` — remove one filter and re-rank.
pub(super) async fn lift_finder(
    State(state): State<AppState>,
    Path(session_id): Path<String>,
    headers: HeaderMap,
    body: Option<Json<LiftRequest>>,
) -> Result<Json<Value>, ApiError> {
    let body = body.map(|Json(body)| body).unwrap_or_default();
    let mut session = get_session(&session_id).ok_or_else(no_session)?;
    if body.question_key.is_empty() {
        return Err(ApiError::invalid_request("question_key is required"));
    }
    let before = session.answers.len();
    session.answers.retain(|answer| {
        !(answer.dimension_key == body.question_key && answer.mode == concord_finder::MODE_REQUIRED)
    });
    if session.answers.len() == before {
        return Err(ApiError::invalid_request(format!(
            "no required answer on {} to lift",
            body.question_key
        )));
    }
    session.depth = session
        .answers
        .iter()
        .filter(|answer| answer.mode == concord_finder::MODE_REQUIRED)
        .count() as i32;
    let base = seed_candidates(
        &state,
        &headers,
        &session.seed_text,
        &session.seed_category,
        &session.seed_ids,
    )
    .await?;
    let mut set = base;
    for answer in &session.answers {
        let (kept, _) = concord_finder::apply(&set, answer);
        set = kept;
    }
    session.candidates = set;
    session.asked = session
        .answers
        .iter()
        .map(|answer| answer.dimension_key.clone())
        .collect();
    let response = session_state(&session);
    replace_session(session);
    Ok(Json(response))
}

/// `GET /finder/sessions/{id}/results` — the ranked list, the unknowns, the gaps and the weights.
pub(super) async fn finder_results(
    State(_state): State<AppState>,
    Path(session_id): Path<String>,
) -> Result<Json<Value>, ApiError> {
    let session = get_session(&session_id).ok_or_else(no_session)?;
    let ranked = concord_finder::score(&session.candidates, &session.answers);
    let engine = engine_state(&session);
    Ok(Json(json!({
        "session_id": session.id,
        "answers": session.answers,
        "candidate_count": session.candidates.len(),
        "candidates": candidate_dtos(&ranked),
        "unknown_fit": unknown_fit(&ranked),
        "filtered_out": session.removed,
        "gaps": concord_finder::gaps(&engine),
        "weights": concord_finder::weight_report(),
    })))
}
