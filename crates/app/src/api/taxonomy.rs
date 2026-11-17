//! §4.3's global taxonomy (step 3, ADR 0014).
//!
//! The vocabulary that may not grow by being mentioned: four handlers,
//! the contributor check and three routes moved verbatim from `lib.rs`.

use axum::{
    extract::{Path, Query, State},
    http::{header::HeaderMap, StatusCode},
    Extension, Json, Router,
};
use polaris_core::ApiError;
use serde::Deserialize;
use serde_json::Value;

use crate::{
    bearer_user, internal, require_role, role_rank, AppState,
};

// ── §4.3's global taxonomy ──────────────────────────────────────────────────────────────────────────────────────

#[derive(Debug, Deserialize)]
struct TagQuery {
    namespace: Option<String>,
    include_suggested: Option<String>,
}

/// `GET /taxonomy/tags` — the vocabulary, for the suggest-before-create UI and the `tag:` facets.
///
/// Suggested tags are **opt-in**: a suggestion is a hint nobody has ratified, and listing it by default would make the vocabulary
/// grow by being mentioned, which is the behaviour §4.3 replaced with proposals.
async fn list_tags(
    State(state): State<AppState>,
    Query(query): Query<TagQuery>,
) -> Result<Json<Value>, ApiError> {
    let include_suggested = query.include_suggested.as_deref() == Some("true");
    let tags = concord_db::taxonomy::tags(&state.pool, query.namespace.as_deref(), include_suggested)
        .await
        .map_err(internal)?;
    // `[]`, never `null`: the reference answers null for an empty set on more than one list route and every consumer survives only
    // because it happens to write `|| []`.
    Ok(Json(serde_json::json!(tags)))
}

#[derive(Debug, Deserialize)]
struct ProposalQuery {
    project_id: Option<i64>,
}

/// `GET /taxonomy/proposals` — the proposals, pending first, with the counts a voter needs.
async fn list_taxonomy_proposals(
    State(state): State<AppState>,
    Query(query): Query<ProposalQuery>,
) -> Result<Json<Value>, ApiError> {
    let rows = concord_db::taxonomy::list(&state.pool, query.project_id.filter(|id| *id > 0))
        .await
        .map_err(internal)?;
    Ok(Json(serde_json::json!(rows)))
}

/// `POST /taxonomy/proposals` — propose a change to the shared vocabulary.
///
/// **Who may propose.** A project-scoped proposal needs `contributor` in THAT project; an instance-wide one needs `contributor`
/// somewhere, because the change affects every project and is decided by the instance charter rather than by whichever project
/// happens to hold the most members. Both refusals are 403: the caller is authenticated and the rule says "not you".
async fn propose_taxonomy_change(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(body): Json<Value>,
) -> Result<(StatusCode, Json<Value>), ApiError> {
    let user = bearer_user(&state, &headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("authentication required"))?;
    let project_id = body.get("project_id").and_then(Value::as_i64).unwrap_or(0);
    let text = |name: &str| -> String {
        body.get(name)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string()
    };
    if project_id > 0 {
        let project = concord_db::projects::project_by_id(&state.pool, project_id)
            .await
            .map_err(internal)?
            .ok_or_else(|| ApiError::not_found("no such project"))?;
        require_role(&state, &project, "contributor", &user).await?;
    } else if !contributor_somewhere(&state, user.id).await? {
        return Err(ApiError::forbidden(
            "an instance-wide taxonomy change needs the contributor role in some project",
        ));
    }
    let proposal = match concord_db::taxonomy::propose(
        &state.pool,
        project_id,
        user.id,
        &text("action"),
        &text("target_tag"),
        &text("value"),
        &text("rationale"),
    )
    .await
    .map_err(internal)?
    {
        Ok(proposal) => proposal,
        Err(concord_db::taxonomy::ProposeRefused::Invalid(message)) => {
            return Err(ApiError::invalid_request(message))
        }
        Err(concord_db::taxonomy::ProposeRefused::Duplicate) => {
            return Err(ApiError::conflict("an identical proposal is already pending"))
        }
    };
    Ok((
        StatusCode::CREATED,
        Json(serde_json::json!({
            "proposal": serde_json::to_value(proposal).map_err(internal)?,
            "message": "taxonomy changes are ratified by quorum; consent to this proposal to proceed",
        })),
    ))
}

/// Whether this account holds `contributor` or above anywhere.
///
/// The one question an instance-wide proposal needs and no single project can answer. Written as one query rather than a loop over
/// projects: the loop is the same query N times and answers the same thing.
async fn contributor_somewhere(state: &AppState, user_id: i64) -> Result<bool, ApiError> {
    let role: Option<String> = sqlx::query_scalar(
        "select role from members where user_id = $1
          order by case role when 'owner' then 5 when 'maintainer' then 4 when 'reviewer' then 3
                               when 'contributor' then 2 else 1 end desc
          limit 1",
    )
    .bind(user_id)
    .fetch_optional(&state.pool)
    .await
    .map_err(internal)?;
    Ok(role.is_some_and(|role| role_rank(&role) >= role_rank("contributor")))
}

/// `POST /taxonomy/proposals/{id}/consent` — one voice towards the charter's threshold.
///
/// **Below the threshold is 202, not an error.** The first consent is an ordinary state of a consensus process, and answering it
/// as a failure makes the first voter think they broke something. The state comes back so the page can show what is still needed.
///
/// A repeated consent adds no voice — the tally counts rows — and it answers the same 202, because the caller's intent ("I agree")
/// is already satisfied.
async fn consent_taxonomy_proposal(
    State(state): State<AppState>,
    Path(proposal_id): Path<i64>,
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<(StatusCode, Json<Value>), ApiError> {
    use concord_db::taxonomy::Refused;
    match concord_db::taxonomy::consent(&state.pool, proposal_id, user.id).await {
        Ok(consented) => Ok((
            StatusCode::OK,
            Json(serde_json::json!({
                "proposal": serde_json::to_value(consented.proposal).map_err(internal)?,
                "status": "ratified",
            })),
        )),
        Err(Refused::NotFound) => Err(ApiError::not_found("no such taxonomy proposal")),
        Err(Refused::NotEligible) => Err(ApiError::forbidden(
            "only eligible taggers may consent: contributor or above in this project",
        )),
        Err(Refused::Decided(status)) => Err(ApiError::conflict(format!(
            "this proposal is already {status}"
        ))),
        // Not an error: the consent is recorded and the threshold is simply not met yet. 202 is the honest status — the request
        // was understood and will be acted on later, by other people.
        Err(Refused::NotYet {
            verdict,
            consents,
            eligible,
        }) => {
            // The current state travels with the 202, in the reference's shape: a page that shows "3 of 5" without the proposal
            // above it has to fetch it again to render the row it just voted on.
            let current = concord_db::taxonomy::proposal(&state.pool, proposal_id)
                .await
                .map_err(internal)?;
            Ok((
                StatusCode::ACCEPTED,
                Json(serde_json::json!({
                    "proposal": serde_json::to_value(current).map_err(internal)?,
                    "message": format!(
                        "consent recorded ({consents} of {eligible} eligible; verdict {verdict})"
                    ),
                })),
            ))
        }
        Err(Refused::ApplyFailed(reason)) => Err(internal(reason)),
    }
}



/// The three taxonomy routes, typed on `AppState` (D-18). GET+POST on
/// `/proposals` stay in one MethodRouter (D-20).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route("/api/v1/taxonomy/tags", axum::routing::get(list_tags))
        .route(
            "/api/v1/taxonomy/proposals",
            axum::routing::get(list_taxonomy_proposals).post(propose_taxonomy_change),
        )
        .route(
            "/api/v1/taxonomy/proposals/{proposal_id}/consent",
            axum::routing::post(consent_taxonomy_proposal),
        )
}
