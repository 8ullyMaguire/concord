//! Request answers, acceptance badge and answer votes
//! (step 3, ADR 0014). The answers reader stays root-owned.

use axum::{
    extract::{Path, State},
    http::{header::HeaderMap, StatusCode},
    Extension, Json, Router,
};
use polaris_core::ApiError;

use crate::{
    bearer_user, internal, readable_project, request_answers, require_readable_request, write_audit,
    AppState,
};

/// Map the accept rule's three refusals onto the three statuses that mean them.
fn refusal_error(refusal: concord_db::requests::Refusal, request_id: i64) -> ApiError {
    match refusal {
        concord_db::requests::Refusal::NotAuthor => {
            ApiError::forbidden("only the request's author may accept an answer")
        }
        concord_db::requests::Refusal::ForeignAnswer => {
            ApiError::not_found(format!("answer on request {request_id}"))
        }
        concord_db::requests::Refusal::NotActive(status) => {
            ApiError::invalid_request(format!("that answer is {status} and cannot be accepted"))
        }
    }
}

/// `PUT /requests/{id}/accept` — §17's badge. Actor from the bearer token, never from the body.
///
/// The author check runs FIRST, before the answer is looked up, so a stranger naming a nonexistent answer gets 403 rather
/// than 404. That order is the ported order and it is observable: the other order tells people who may not accept
/// anything which answer ids exist.
async fn accept_answer(
    State(state): State<AppState>,
    Path(request_id): Path<String>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to accept an answer"))?;
    let request = require_readable_request(&state, &request_id, &headers).await?;
    let answer_id = body
        .get("answer_id")
        .and_then(serde_json::Value::as_i64)
        .unwrap_or(0);
    concord_db::requests::accept(&state.pool, request.id, answer_id, user.id)
        .await
        .map_err(internal)?
        .map_err(|refusal| refusal_error(refusal, request.id))?;
    // The updated request back, so the caller sees the badge they just set — and can see that the ranking did not move.
    let updated = concord_db::requests::get(&state.pool, request.id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("request {}", request.id)))?;
    Ok(Json(serde_json::json!(updated)))
}

/// `DELETE /requests/{id}/accept` — take the badge back. Author-only, and unaffected by the answer's status: an accepted
/// answer that was later removed still leaves a badge, and the author must be able to clear it.
async fn unaccept_answer(
    State(state): State<AppState>,
    Path(request_id): Path<String>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to unaccept an answer"))?;
    let request = require_readable_request(&state, &request_id, &headers).await?;
    concord_db::requests::unaccept(&state.pool, request.id, user.id)
        .await
        .map_err(internal)?
        .map_err(|refusal| refusal_error(refusal, request.id))?;
    let updated = concord_db::requests::get(&state.pool, request.id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("request {}", request.id)))?;
    Ok(Json(serde_json::json!(updated)))
}


/// `POST /requests/{id}/answers` — a **project** answers a request.
///
/// The project comes from the request row, never from the caller: the reference's earlier version put the caller's user id
/// in the project column, which attributed the answer to whichever project shared that id.
///
/// An **instance-wide request** has no project column to inherit. The reference passed it straight through as 0, which
/// violates NOT NULL and answers 500 for a question the instance board is for. The port requires the body to name the
/// answering project in that case and refuses with 400 otherwise — a decision, recorded in the ledger, because the
/// alternative (guessing a project from the caller's memberships) invents an answer's provenance.
async fn answer_request(
    State(state): State<AppState>,
    Path(request_id): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<(StatusCode, Json<serde_json::Value>), ApiError> {
    let request = require_readable_request(&state, &request_id, &headers).await?;
    let text = body.get("body").and_then(|value| value.as_str()).unwrap_or_default().to_string();
    let named = body
        .get("project_id")
        .and_then(serde_json::Value::as_i64)
        .filter(|id| *id > 0);
    let project = named.unwrap_or(request.project_id);
    if project <= 0 {
        return Err(ApiError::invalid_request(
            "an instance-wide request must name the answering project in project_id",
        ));
    }
    let answering = readable_project(&state, project, &headers).await?;
    let answer = concord_db::requests::answer(&state.pool, request.id, answering.id, user.id, &text)
        .await
        .map_err(|error| match &error {
            sqlx::Error::Database(database) if database.is_unique_violation() => {
                ApiError::conflict("that project has already answered this request")
            }
            _ => internal(error),
        })?;
    write_audit(
        &state,
        Some(answering.id),
        user.id,
        "answer_request",
        "request_answer",
        answer.id,
        &format!("request={}", request.id),
    )
    .await?;
    Ok((StatusCode::CREATED, Json(serde_json::json!(answer))))
}

/// `POST /answers/{id}/vote` — a pairwise comparison between two answers to one request.
///
/// Order, all ported and all observable:
///
/// 1. **both answers are read and checked against this request before anything is written.** The route has no project
///    segment, so visibility comes from the answer's own request — a vote naming an answer from another question would
///    otherwise spend this request's vote and move an unrelated rating;
/// 2. **a foreign answer is 403, not 404.** It exists and the caller may read it; it simply is not this request's;
/// 3. **the voter's weight comes from §8.3**, computed here rather than read from the body. `weight` in the request body
///    is accepted and ignored — §8.3's "money cannot buy weight";
/// 4. **the response carries the new ratings**, because a vote whose effect the caller cannot see is a vote they cannot
///    trust.
async fn vote_answer(
    State(state): State<AppState>,
    Path(request_id): Path<String>,
    headers: HeaderMap,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to vote"))?;
    let request = require_readable_request(&state, &request_id, &headers).await?;
    let answer_a = body.get("answer_a").and_then(serde_json::Value::as_i64).unwrap_or(0);
    let answer_b = body.get("answer_b").and_then(serde_json::Value::as_i64).unwrap_or(0);
    let outcome = body
        .get("outcome")
        .and_then(|value| value.as_str())
        .unwrap_or_default()
        .to_string();

    // Rule 2, before any write: an answer that exists but belongs elsewhere is a permission refusal, whether it is one of
    // this request's answers or not.
    for id in [answer_a, answer_b] {
        let found = concord_db::requests::one_answer(&state.pool, id)
            .await
            .map_err(internal)?;
        match found {
            None => return Err(ApiError::not_found("no such answer")),
            Some(answer) if answer.request_id != request.id => {
                return Err(ApiError::forbidden("that answer belongs to a different request"));
            }
            Some(_) => {}
        }
    }

    let charter = concord_db::charter::charter(&state.pool, request.project_id)
        .await
        .map_err(internal)?;
    let weight = concord_db::voteweight::answer_vote_weight(&state.pool, request.project_id, user.id, &charter)
        .await
        .map_err(internal)?;

    let answers = concord_db::requests::vote_answer(
        &state.pool,
        request.id,
        answer_a,
        answer_b,
        user.id,
        &outcome,
        weight,
        charter.glicko_tau,
    )
    .await
    .map_err(|error| match &error {
        // `unique (voter_id, answer_a, answer_b)`: a given voter compares a given ordered pair once. The reference lets
        // the constraint error out as a 500, so a second click on the same pair looks like a server fault; the refusal is
        // the same and only the status changes, which is worth the honesty.
        sqlx::Error::Database(database) if database.is_unique_violation() => {
            ApiError::conflict("you have already voted on that pair of answers")
        }
        _ => internal(error),
    })?
    .map_err(|refusal| match refusal {
        concord_db::requests::VoteRefusal::Invalid(reason) => ApiError::invalid_request(reason),
        concord_db::requests::VoteRefusal::ForeignAnswer => {
            ApiError::forbidden("that answer belongs to a different request")
        }
        concord_db::requests::VoteRefusal::SelfVote => {
            ApiError::forbidden("you cannot vote on an answer you proposed")
        }
    })?;

    write_audit(
        &state,
        Some(request.project_id).filter(|id| *id > 0),
        user.id,
        "vote_request_answer",
        "request",
        request.id,
        &format!("a={answer_a} b={answer_b} outcome={outcome}"),
    )
    .await?;
    Ok(Json(serde_json::json!({
        "status": "voted",
        "weight": weight,
        "answers": answers,
    })))
}

/// Answer routes, typed on `AppState` (D-18). GET+POST on one path
/// stays in one MethodRouter (D-20).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/requests/{request_id}/accept",
            axum::routing::put(accept_answer).delete(unaccept_answer),
        )
        .route(
            "/api/v1/requests/{request_id}/answers",
            axum::routing::get(request_answers).post(answer_request),
        )
        .route(
            "/api/v1/answers/{request_id}/vote",
            axum::routing::post(vote_answer),
        )
}
