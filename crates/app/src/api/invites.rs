//! Project invites (step 3, ADR 0014): mint, list, revoke and redeem.
//!
//! The whole invite section was lifted verbatim from `lib.rs`; the two visibility
//! promotions below let it borrow the shared visibility rules rather than copy them.

use axum::{
    extract::{Path, Query, State},
    http::{header::HeaderMap, StatusCode},
    Extension, Json, Router,
};
use polaris_core::ApiError;
use crate::pagination::PageQuery;

use crate::{internal, readable_project_segment, require_role, AppState};

// ── invites (§8.2) ────────────────────────────────────────────────────────────────────────────────────────────────

/// What a mint sends. Both limits are optional and mean "none" when absent or zero.
#[derive(serde::Deserialize, Default)]
struct CreateInviteRequest {
    #[serde(default)]
    expires_in_seconds: i64,
    #[serde(default)]
    max_uses: i64,
}

/// What a redemption sends.
///
/// **The token is in the body, not the URL**, so it does not end up in access logs, referrer headers or browser history — and
/// so a link that is shared in a chat does not leave a redeemable token in a server log.
#[derive(serde::Deserialize)]
struct RedeemInviteRequest {
    #[serde(default)]
    token: String,
}

/// `POST /projects/{id}/invites` — mint a link.
///
/// **Any member may mint one, not only a maintainer.** That is the reference's choice and it is recorded here because it is
/// the kind of rule a reader would otherwise assume: an invite is the mechanism by which a project grows, and restricting it
/// to maintainers makes a collective's growth depend on one role being awake. The role it grants is `contributor`, which
/// cannot admit others or move a decision.
async fn create_invite(
    State(state): State<AppState>,
    Path(project_segment): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
    body: Option<Json<CreateInviteRequest>>,
) -> Result<impl axum::response::IntoResponse, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user).await.map_err(|_| {
        // The reference answers 404 here rather than 403: a non-member is not told that the project exists under this id.
        ApiError::not_found("no such project")
    })?;
    let body = body.map(|Json(body)| body).unwrap_or_default();
    let invite = concord_db::invite_writes::create(
        &state.pool,
        project.id,
        user.id,
        body.expires_in_seconds,
        body.max_uses,
    )
    .await
    .map_err(invite_refusal)?;
    let _ = sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1,$2,'create_invite','invite',$3,$4,now())",
    )
    .bind(project.id)
    .bind(user.id)
    .bind(invite.id)
    .bind(&project.slug)
    .execute(&state.pool)
    .await;
    Ok((StatusCode::CREATED, Json(invite)))
}

/// `GET /projects/{id}/invites` — the project's links, newest first, **including revoked and expired ones**: an invite list is
/// a record of who was let in and by which link, and hiding the dead ones hides the history.
async fn list_invites(
    State(state): State<AppState>,
    Query(page_query): Query<PageQuery>,
    Path(project_segment): Path<String>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let page = page_query.valid().map_err(ApiError::invalid_request)?;
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user)
        .await
        .map_err(|_| ApiError::not_found("no such project"))?;
    let invites = concord_db::invite_writes::list(&state.pool, project.id)
        .await
        .map_err(internal)?;
    Ok(crate::pagination::page_json(invites, &page)?)
}

/// `POST /projects/{id}/invites/{invite}/revoke` — withdraw a link, keeping the record of it.
///
/// The invite is checked against **this** project before it is withdrawn: without that, a member of one project could revoke
/// an invite belonging to another by naming its id under their own project's URL.
async fn revoke_invite(
    State(state): State<AppState>,
    Path((project_segment, invite)): Path<(String, String)>,
    Extension(user): Extension<concord_db::auth::User>,
    headers: HeaderMap,
) -> Result<Json<serde_json::Value>, ApiError> {
    let project = readable_project_segment(&state, &project_segment, &headers).await?;
    require_role(&state, &project, "contributor", &user)
        .await
        .map_err(|_| ApiError::not_found("no such project"))?;
    let invite_id: i64 = invite
        .parse()
        .map_err(|_| ApiError::not_found("no such invite"))?;
    let existing = concord_db::invite_writes::get(&state.pool, invite_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| ApiError::not_found("no such invite"))?;
    if existing.project_id != project.id {
        return Err(ApiError::not_found("no such invite"));
    }
    concord_db::invite_writes::revoke(&state.pool, invite_id)
        .await
        .map_err(invite_refusal)?;
    let _ = sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1,$2,'revoke_invite','invite',$3,$4,now())",
    )
    .bind(project.id)
    .bind(user.id)
    .bind(invite_id)
    .bind(&project.slug)
    .execute(&state.pool)
    .await;
    Ok(Json(serde_json::json!({ "status": "revoked" })))
}

/// `POST /auth/redeem-invite` — turn a token into a membership.
///
/// **404 for every failure**, on purpose: expired, revoked, exhausted and unknown are indistinguishable, or the endpoint
/// becomes an oracle for testing tokens. The response names the project's slug so the caller can navigate, and the role it
/// granted, because a client that has just joined should not have to ask what it joined as.
async fn redeem_invite(
    State(state): State<AppState>,
    Extension(user): Extension<concord_db::auth::User>,
    Json(body): Json<RedeemInviteRequest>,
) -> Result<Json<serde_json::Value>, ApiError> {
    if body.token.trim().is_empty() {
        return Err(ApiError::invalid_request("a token is required"));
    }
    let project_id = concord_db::invite_writes::redeem(&state.pool, body.token.trim(), user.id)
        .await
        .map_err(|refused| match refused {
            concord_db::invite_writes::Refused::Invalid(reason) => ApiError::invalid_request(reason),
            concord_db::invite_writes::Refused::Database(error) => internal(error),
            // Everything else is the same 404, deliberately.
            _ => ApiError::not_found("that invite cannot be redeemed"),
        })?;
    let project = concord_db::projects::project_by_id(&state.pool, project_id)
        .await
        .map_err(internal)?
        .ok_or_else(|| internal("the project vanished after the invite was redeemed"))?;
    let _ = sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1,$2,'redeem_invite','invite',$1,$3,now())",
    )
    .bind(project_id)
    .bind(user.id)
    .bind(&project.slug)
    .execute(&state.pool)
    .await;
    Ok(Json(serde_json::json!({
        "project": project.slug,
        "slug": project.slug,
        "status": "joined",
        "role": "contributor",
    })))
}

/// An invite refusal, as the status it deserves.
fn invite_refusal(refused: concord_db::invite_writes::Refused) -> ApiError {
    use concord_db::invite_writes::Refused;
    match refused {
        Refused::Invalid(reason) => ApiError::invalid_request(reason),
        Refused::NotFound => ApiError::not_found("no such invite"),
        Refused::WrongProject => ApiError::not_found("no such invite"),
        Refused::AlreadyRevoked => ApiError::invalid_request("that invite is already revoked"),
        Refused::Database(error) => internal(error),
    }
}



/// All invite routes, typed on `AppState` (D-18). The first path keeps both its
/// GET and POST methods exactly as in the old chain.
pub fn routes() -> Router<AppState> {
    Router::new()
        .route(
            "/api/v1/projects/{slug}/invites",
            axum::routing::get(list_invites).post(create_invite),
        )
        .route(
            "/api/v1/projects/{slug}/invites/{invite}/revoke",
            axum::routing::post(revoke_invite),
        )
        .route("/api/v1/auth/redeem-invite", axum::routing::post(redeem_invite))
}
