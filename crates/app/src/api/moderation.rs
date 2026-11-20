//! Moderation acts and curation votes (step 3, ADR 0014).
//! The curation queue reader stays root-owned.

use axum::{
    extract::{Path, State},
    http::StatusCode,
    Extension, Json, Router,
};
use polaris_core::ApiError;
use serde::Deserialize;
use serde_json::Value;

use crate::{
    internal, parse_id, project_for, strategy_refusal, taxonomy_refusal, write_error, AppState,
};

/// A moderation act's body.
///
/// Every scalar is `Option`, because `Json<T>` refuses `null` for a bare `i64` and answers with axum's own
/// **422 and a plain-text body** -- not the API's error envelope, and not a status any client of this API
/// handles. A `null` is an ordinary thing for a form to send; it is "no value", and `unwrap_or` turns it into
/// the same 400 the absent field gets.
#[derive(Debug, Deserialize, Default)]
struct ArenaActRequest {
    #[serde(default)]
    feature_id: Option<i64>,
    #[serde(default)]
    arena_id: Option<i64>,
    #[serde(default)]
    reason: Option<String>,
    #[serde(default)]
    confirm: Option<bool>,
    #[serde(default)]
    undoes: Option<i64>,
}

impl ArenaActRequest {
    fn feature_id(&self) -> i64 {
        self.feature_id.unwrap_or(0)
    }

    fn arena_id(&self) -> i64 {
        self.arena_id.unwrap_or(0)
    }

    fn reason(&self) -> &str {
        self.reason.as_deref().unwrap_or("")
    }

    fn confirmed(&self) -> bool {
        self.confirm.unwrap_or(false)
    }

    fn undoes(&self) -> i64 {
        self.undoes.unwrap_or(0)
    }
}

/// The trust floor for a moderation act; `CONCORD_MOD_TRUST_MIN` moves it, and 1 is the default the Go
/// config ships with.
pub(crate) fn mod_trust_min() -> i32 {
    std::env::var("CONCORD_MOD_TRUST_MIN")
        .ok()
        .and_then(|value| value.parse::<i32>().ok())
        .filter(|value| *value >= 0)
        .unwrap_or(1)
}

/// The two-door permission check, in one place: instance trust, or a project role at `reviewer`.
async fn permit_moderation(
    state: &AppState,
    project_id: i64,
    user_id: i64,
) -> Result<(), ApiError> {
    let allowed = concord_db::moderation::may_moderate(&state.pool, project_id, user_id, mod_trust_min())
        .await
        .map_err(internal)?;
    if allowed {
        Ok(())
    } else {
        Err(ApiError::forbidden(
            "this act needs a reviewer role in the project, or a high enough trust level",
        ))
    }
}

/// `POST /api/v1/projects/{slug}/moderation/arena/remove`
///
/// The page names the idea, not the arena: an idea's arena is a property of the ranking. One arena is
/// inferred; several must be named, because guessing which ranking to take an idea out of is exactly the
/// silent choice this surface must not make.
async fn arena_remove(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<ArenaActRequest>,
) -> Result<(StatusCode, Json<Value>), ApiError> {
    let project = project_for(&state, &slug).await?;
    permit_moderation(&state, project.id, user.id).await?;
    if !body.confirmed() {
        // The browser confirms in the page too; this is for the script that does not.
        return Err(ApiError::invalid_request("this act needs confirmation"));
    }
    if body.feature_id() == 0 {
        return Err(ApiError::invalid_request("feature_id is required"));
    }
    let mut arena_id = body.arena_id();
    if arena_id == 0 {
        let arenas = concord_db::moderation::arenas_for_feature(&state.pool, body.feature_id())
            .await
            .map_err(internal)?;
        match arenas.len() {
            0 => return Err(ApiError::not_found("this idea is not in an arena")),
            1 => arena_id = arenas[0],
            _ => {
                return Err(ApiError::invalid_request(
                    "this idea is in more than one arena; name the arena_id",
                ))
            }
        }
    }
    let act = concord_db::moderation::remove_feature_from_arena(
        &state.pool,
        project.id,
        arena_id,
        body.feature_id(),
        user.id,
        body.reason(),
    )
    .await
    .map_err(write_error)?;
    Ok((
        StatusCode::CREATED,
        Json(serde_json::json!({
            "act": act,
            "undo": { "action": "arena.restore", "target": act.id },
        })),
    ))
}

/// `POST /api/v1/projects/{slug}/moderation/arena/restore`
///
/// Takes the id of the act it reverses, not the feature's prior values: those are read from the log.
async fn arena_restore(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<ArenaActRequest>,
) -> Result<Json<Value>, ApiError> {
    let project = project_for(&state, &slug).await?;
    permit_moderation(&state, project.id, user.id).await?;
    if !body.confirmed() {
        return Err(ApiError::invalid_request("this act needs confirmation"));
    }
    if body.undoes() == 0 {
        return Err(ApiError::invalid_request("undoes is required"));
    }
    let act = concord_db::moderation::restore_feature_to_arena(
        &state.pool,
        project.id,
        user.id,
        body.undoes(),
        body.reason(),
    )
    .await
    .map_err(write_error)?;
    Ok(Json(serde_json::json!({ "act": act })))
}

#[derive(Debug, Deserialize, Default)]
struct CurationVoteRequest {
    #[serde(default)]
    vote: Option<String>,
    #[serde(default)]
    rationale: Option<String>,
}

/// `POST /api/v1/curation/{kind}/{id}/vote` — endorse or contest a moderation act in the queue.
///
/// `taxonomy` and `strategy` are refused with 501 rather than implemented halfway. Their ratification needs the
/// *eligible set* — `eligibleTaggers`, the collaborators a weight proposal is put to — and a consent recorded
/// without that check is the defect the Go store's own comment records: "anyone with an account could ratify a
/// tag proposal for a project they had no relationship with, and since guest identities exist, a nameless one
/// could." That set is a slice of its own, named in the ledger; a version of this route that skipped it would
/// pass a probe and reopen the hole.
async fn curation_vote(
    State(state): State<AppState>,
    Path((kind, id)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<CurationVoteRequest>,
) -> Result<(StatusCode, Json<Value>), ApiError> {
    let ref_id = parse_id(&id, "id")?;
    let raw = body.vote.as_deref().unwrap_or("");
    let rationale = body.rationale.as_deref().unwrap_or("");
    match kind.as_str() {
        "act" => {
            let vote = match raw.trim().to_ascii_lowercase().as_str() {
                "yes" | "consent" | "endorse" | "1" => 1,
                "no" | "contest" | "-1" => -1,
                _ => {
                    return Err(ApiError::invalid_request("vote must be yes or no"));
                }
            };
            // Reviewing an act is reviewer work: it writes a judgement about somebody else's decision, so it
            // takes the standing that decision took.
            let (_, project_id, _) = concord_db::curation::act_target(&state.pool, ref_id)
                .await
                .map_err(write_error)?;
            permit_moderation(&state, project_id, user.id).await?;
            let (yes, no) = concord_db::curation::review_act(
                &state.pool,
                ref_id,
                user.id,
                vote,
                rationale,
            )
            .await
            .map_err(write_error)?;
            Ok((
                StatusCode::OK,
                Json(serde_json::json!({ "recorded": true, "decided": true, "yes": yes, "no": no })),
            ))
        }
        // §4.3: a taxonomy proposal is decided by the ELIGIBLE SET, and the set is checked in the store before
        // anything is written. A consent recorded without it is the defect the Go comment records: "anyone with
        // an account could ratify a tag proposal for a project they had no relationship with, and since guest
        // identities exist, a nameless one could."
        "taxonomy" => match concord_db::taxonomy::consent(&state.pool, ref_id, user.id).await {
            Ok(recorded) => Ok((
                StatusCode::OK,
                Json(serde_json::json!({
                    "recorded": true,
                    "ratified": recorded.ratified,
                    "consents": recorded.consents,
                    "eligible": recorded.eligible,
                    "threshold": recorded.threshold,
                    "proposal": recorded.proposal,
                })),
            )),
            Err(concord_db::taxonomy::Refused::NotYet {
                verdict, consents, eligible,
            }) => Ok((
                StatusCode::ACCEPTED,
                Json(serde_json::json!({
                    "recorded": true,
                    "ratified": false,
                    "decided": false,
                    "message": format!(
                        "the consent was recorded and the proposal is {verdict} ({consents} of {eligible} eligible consenting)"
                    ),
                })),
            )),
            Err(refusal) => Err(taxonomy_refusal(refusal)),
        },
        // §5.1: a strategic weight is proposed, consented to by §8.2's eligible population, and applied only if
        // the feature still carries the weight the proposal was written against.
        "strategy" => match concord_db::strategy::consent(&state.pool, ref_id, user.id).await {
            Ok(recorded) => Ok((
                StatusCode::OK,
                Json(serde_json::json!({
                    "recorded": true,
                    "ratified": recorded.ratified,
                    "stale": recorded.stale,
                    "consents": recorded.consents,
                    "eligible": recorded.eligible,
                    "proposal": recorded.proposal,
                })),
            )),
            Err(concord_db::strategy::Refused::NotYet {
                verdict, consents, eligible,
            }) => Ok((
                StatusCode::ACCEPTED,
                Json(serde_json::json!({
                    "recorded": true,
                    "ratified": false,
                    "decided": false,
                    "message": format!(
                        "the consent was recorded and the proposal is {verdict} ({consents} of {eligible} eligible consenting)"
                    ),
                })),
            )),
            Err(refusal) => Err(strategy_refusal(refusal)),
        },
        other => Err(ApiError::invalid_request(format!(
            "unknown curation kind {other:?}"
        ))),
    }
}

/// Moderation routes, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{slug}/moderation/arena/remove",
            axum::routing::post(arena_remove),
        )
        .route(
            "/api/v1/projects/{slug}/moderation/arena/restore",
            axum::routing::post(arena_restore),
        )
        .route(
            "/api/v1/curation/{kind}/{id}/vote",
            axum::routing::post(curation_vote),
        )
}
