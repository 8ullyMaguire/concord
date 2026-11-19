//! Project lifecycle: create, claim, archive, unarchive and AI declaration
//! (step 3, ADR 0014). Moved verbatim from `lib.rs`.

use axum::{
    extract::{Path, State},
    http::header::HeaderMap,
    Extension, Json, Router,
};
use polaris_core::ApiError;
use serde::Deserialize;
use serde_json::{json, Value};

use crate::{bearer_user, internal, AppState};

#[derive(Debug, Deserialize)]
struct CreateProjectRequest {
    slug: String,
    name: String,
    #[serde(default)]
    description: String,
    #[serde(default)]
    governance_model: String,
    #[serde(default)]
    license: String,
}


#[derive(Debug, Deserialize, Default)]
struct ArchiveRequest {
    #[serde(default)]
    reason: String,
}

/// The actor, if they may speak for this project. One helper because these four refusals are the same four for every act that speaks for
/// a project, and a fifth copy is a fifth chance to disagree.
async fn project_maintainer(
    state: &AppState,
    headers: &HeaderMap,
    slug: &str,
) -> Result<(concord_db::projects::Project, concord_db::auth::User), ApiError> {
    let user = bearer_user(state, headers)
        .await?
        .ok_or_else(|| ApiError::unauthenticated("sign in to do this"))?;
    if concord_db::identity::is_guest(&state.pool, user.id).await.map_err(internal)? {
        return Err(ApiError::forbidden(
            "a guest cannot speak for a project: the record must name somebody",
        ));
    }
    let project = concord_db::projects::project_by_slug(&state.pool, slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;
    let role = concord_db::feed_reads::role_for(&state.pool, project.id, user.id)
        .await
        .map_err(internal)?;
    if !matches!(role.as_deref(), Some("owner") | Some("maintainer")) {
        return Err(ApiError::forbidden(
            "only the project's owner or a maintainer may do this",
        ));
    }
    Ok((project, user))
}

/// `POST /api/v1/projects/{slug}/claim` — take on a project the forge has measured dormant (roadmap step 5's pool).
///
/// This is not "join": joining says you are taking part, and claiming says you are answerable. So the refusals are about the MEASUREMENT,
/// not about the person — a project that is alive cannot be claimed, because offering a live project to a stranger is taking it from
/// somebody who is still working on it.
///
/// Three refusals, and each one names the number it read:
///  * `unknown` — the project names no repository this instance can read, so its silence was never measured and nothing can be said about
///    whether it is abandoned. Refusing here is the same rule the badges follow: unknown is never zero.
///  * `dormant` is the only stage that may be claimed. A project that is merely old, or has no features, is not abandoned; a project its
///    maintainer declared archived is a decision, and a decision is reversed by the maintainer, not outvoted by a claim.
///  * the claimant must be an account, not a guest, because the whole point is that the record names somebody.
async fn claim_project(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<Json<Value>, ApiError> {
    if concord_db::identity::is_guest(&state.pool, user.id).await.map_err(internal)? {
        return Err(ApiError::forbidden(
            "a guest cannot adopt: the record must name somebody",
        ));
    }
    let project = concord_db::projects::project_by_slug(&state.pool, &slug)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found(format!("project {slug}")))?;

    match project.stage.as_str() {
        "dormant" => {}
        "archived" => {
            return Err(ApiError::invalid_request(format!(
                "{} was declared archived by its maintainer: {}",
                project.slug, project.stage_because
            )))
        }
        "unknown" => {
            return Err(ApiError::invalid_request(format!(
                "{} names no repository this instance can read, so its silence has not been measured — it cannot be offered for adoption \
                 on the strength of a number nobody took",
                project.slug
            )))
        }
        stage => {
            return Err(ApiError::invalid_request(format!(
                "{} is {stage}, not dormant: {}",
                project.slug, project.stage_because
            )))
        }
    }

    // Read the number the refusal above would have quoted, so the success carries the same measurement the pool showed.
    let days = concord_db::adoption::days_silent(&state.pool, project.id)
        .await
        .map_err(internal)?
        .unwrap_or(0.0);
    concord_db::projects::claim(&state.pool, project.id, user.id)
        .await
        .map_err(internal)?;
    let _ = concord_db::feature_writes::add_audit(
        &state.pool,
        project.id,
        user.id,
        "claim_project",
        "project",
        project.id,
        &format!("adopted after {days:.0} days without a commit: {}", user.username),
    )
    .await;
    Ok(Json(json!({
        "status": "ok",
        "slug": project.slug,
        "role": "maintainer",
        "days_silent_display": format!("{days:.0} days without a commit"),
    })))
}

/// `POST /api/v1/projects/{slug}/archive` — declare a project dead, in the maintainer's own words.
///
/// `Archived` is the one stage the lifecycle function refuses to infer, and this route is why: it is a person saying the project is over.
/// The reason is stored as a sentence rather than an enum, because an abandonment that carries no reason leaves the next reader with a
/// dead end instead of a pointer, and because the note's own requirement is that abandonment names suggested alternatives.
///
/// An empty reason is refused rather than stored: the sentence IS the value of the act.
async fn archive_project(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    headers: HeaderMap,
    body: Option<Json<ArchiveRequest>>,
) -> Result<Json<Value>, ApiError> {
    let (project, user) = project_maintainer(&state, &headers, &slug).await?;
    let reason = body.map(|Json(b)| b.reason).unwrap_or_default();
    let reason = reason.trim().to_string();
    if reason.is_empty() {
        return Err(ApiError::invalid_request(
            "archiving needs a reason: it is the sentence the next reader acts on",
        ));
    }
    concord_db::projects::set_archived_with_event(
        &state.pool,
        project.id,
        user.id,
        Some((user.id, &reason)),
        &reason,
    )
    .await
    .map_err(internal)?;
    let _ = concord_db::feature_writes::add_audit(
        &state.pool,
        project.id,
        user.id,
        "archive_project",
        "project",
        project.id,
        &reason,
    )
    .await;
    Ok(Json(json!({"status": "ok", "stage": "archived", "reason": reason})))
}

/// `DELETE /api/v1/projects/{slug}/archive` — the maintainer changes their mind.
///
/// No body and no argument, because the only thing it can mean is "clear the declaration". The audit row keeps the old reason, so the fact
/// that it was once declared dead survives the reversal.
async fn unarchive_project(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let (project, user) = project_maintainer(&state, &headers, &slug).await?;
    let previous = concord_db::projects::archived_reason(&state.pool, project.id)
        .await
        .map_err(internal)?;
    concord_db::projects::set_archived_with_event(
        &state.pool,
        project.id,
        user.id,
        None,
        &previous.clone().unwrap_or_else(|| "(no reason recorded)".to_string()),
    )
    .await
    .map_err(internal)?;
    let _ = concord_db::feature_writes::add_audit(
        &state.pool,
        project.id,
        user.id,
        "unarchive_project",
        "project",
        project.id,
        &format!("was archived: {}", previous.unwrap_or_else(|| "(no reason recorded)".to_string())),
    )
    .await;
    Ok(Json(json!({"status": "ok"})))
}

/// `PUT /api/v1/projects/{slug}/ai-involvement` — declare, or withdraw, what a project says about AI in its code.
///
/// The body is `{"value": "human"|"assisted"|"generated"|"audited"}`; `{"value": null}` withdraws a declaration, which is a real request
/// and not an error (a label somebody regrets must be removable). Anything else is a 400 that names the four.
///
/// Authorisation is the project's own role ladder, read through the store: a declaration speaks for the project, so it takes somebody who
/// speaks for it. A stranger gets 403 — not 404 — because the project is public here and hiding it would be a different lie.
async fn declare_ai_involvement(
    State(state): State<AppState>,
    Path(slug): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<serde_json::Value>,
) -> Result<Json<serde_json::Value>, ApiError> {
    use concord_db::projects::DeclareRefusal;
    if concord_db::identity::is_guest(&state.pool, user.id).await.map_err(internal)? {
        return Err(ApiError::forbidden("a project's declaration needs an account, not a guest session"));
    }
    let value = match body.get("value") {
        None => return Err(ApiError::invalid_request("value is required: one of human, assisted, generated, audited, or null to withdraw")),
        Some(serde_json::Value::Null) => None,
        Some(serde_json::Value::String(text)) => Some(text.as_str()),
        Some(_) => return Err(ApiError::invalid_request("value must be a string or null")),
    };
    match concord_db::projects::set_ai_involvement(&state.pool, &slug, user.id, value).await {
        Ok(stored) => Ok(Json(serde_json::json!({ "ai_involvement": stored }))),
        Err(DeclareRefusal::NotAValue(bad)) => Err(ApiError::invalid_request(format!(
            "{bad:?} is not a declaration: want one of human, assisted, generated, audited"
        ))),
        Err(DeclareRefusal::NotMaintainer) => {
            Err(ApiError::forbidden("only a project's owner or maintainer may declare its AI involvement"))
        }
        Err(DeclareRefusal::Missing) => Err(ApiError::not_found(format!("no project {slug:?}"))),
        Err(DeclareRefusal::Database(error)) => Err(internal(error)),
    }
}

/// `POST /api/v1/projects` — a namespace with a slug, a charter and a board.
///
/// Needs an account, not merely a token: a nameless party holding namespaces is a squatting vector,
/// which is why the Go handler refuses a guest identity here and nowhere else in the read path.
///
/// The anonymous refusal is no longer here: `POST /api/v1/projects` is an enforced row, so `auth_gate` resolves the
/// identity once and supplies it in the request extensions. The guest check stays — the gate proves *somebody* is here,
/// not that they have an account, and that depth belongs to this handler.
async fn create_project(
    State(state): State<AppState>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<CreateProjectRequest>,
) -> Result<(axum::http::StatusCode, Json<Value>), ApiError> {
    use concord_db::projects::CreateError;
    // A GUEST identity is not an account, and the comment above says the Go handler refuses one here and nowhere else in the read path.
    // It was not refused: measured on 2026-11-14, a browser that had just been issued a guest session by `POST /api/v1/guest` got
    // `201 Created` from this route — the squatting vector the rule exists to prevent, reachable by clicking submit on a page nobody
    // signed into. The check is `identity::is_guest`, the same predicate the guest cap and the promotion path ask.
    if concord_db::identity::is_guest(&state.pool, user.id).await.map_err(internal)? {
        return Err(ApiError::forbidden("a project needs an account, not a guest session"));
    }
    let model = if body.governance_model.is_empty() {
        "collective"
    } else {
        body.governance_model.as_str()
    };
    match concord_db::projects::create_project(
        &state.pool,
        body.slug.trim(),
        body.name.trim(),
        &body.description,
        model,
        &body.license,
        user.id,
    )
    .await
    {
        Ok(project) => Ok((
            axum::http::StatusCode::CREATED,
            Json(serde_json::to_value(project).map_err(internal)?),
        )),
        Err(CreateError::Invalid(message)) => Err(ApiError::invalid_request(message)),
        Err(CreateError::Duplicate(slug)) => Err(ApiError::conflict(format!("project {slug:?} exists"))),
        Err(CreateError::Database(error)) => Err(internal(error)),
    }
}


/// The five lifecycle routes, typed on `AppState` (D-18). Archive keeps
/// POST and DELETE in one MethodRouter (D-20).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route("/api/v1/projects", axum::routing::post(create_project))
        .route("/api/v1/projects/{slug}/claim", axum::routing::post(claim_project))
        .route(
            "/api/v1/projects/{slug}/archive",
            axum::routing::post(archive_project).delete(unarchive_project),
        )
        .route(
            "/api/v1/projects/{slug}/ai-involvement",
            axum::routing::put(declare_ai_involvement),
        )
}
