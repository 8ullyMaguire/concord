//! Account registration (step 3, ADR 0014).
//!
//! The shared TOKEN_NOTE stays in the crate root: login in api/identity.rs
//! also uses it. A guest registers into their existing identity.

use axum::{
    extract::State,
    http::header::HeaderMap,
    Json, Router,
};
use polaris_core::ApiError;
use serde::Deserialize;
use serde_json::{json, Value};

use crate::{acting_user, internal, AppState, TOKEN_NOTE};

#[derive(Debug, Deserialize)]
struct RegisterRequest {
    username: String,
    #[serde(default)]
    display_name: String,
    password: String,
}

/// `POST /api/v1/auth/register` — an account and its first token.
///
/// A visitor who has been acting as a guest registers INTO their guest identity: the row becomes the account, so everything they
/// did as a guest is theirs without a copy, and the history stays one identity's — "this account took these positions while it
/// had no name". A caller who is already an account takes the ordinary path and gets a second account, which is unusual but not
/// wrong; the caller is signed in either way.
async fn register(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(body): Json<RegisterRequest>,
) -> Result<(axum::http::StatusCode, Json<Value>), ApiError> {
    use concord_db::auth::AuthError;
    use concord_db::identity::Refused;
    let display_name = if body.display_name.trim().is_empty() {
        body.username.trim().to_string()
    } else {
        body.display_name.trim().to_string()
    };
    if body.username.trim().is_empty() || body.password.is_empty() {
        return Err(ApiError::invalid_request("username and password are required"));
    }
    if let Some(actor) = acting_user(&state, &headers).await? {
        let guest = concord_db::identity::is_guest(&state.pool, actor.id)
            .await
            .unwrap_or(false);
        if guest {
            return match concord_db::identity::promote_guest(
                &state.pool,
                actor.id,
                &body.username,
                &display_name,
                &body.password,
            )
            .await
            {
                Ok((user, token)) => Ok((
                    axum::http::StatusCode::CREATED,
                    Json(json!({
                        "user": serde_json::to_value(user).map_err(internal)?,
                        "token": token,
                        "token_note": TOKEN_NOTE,
                    })),
                )),
                Err(Refused::Invalid(message)) => Err(ApiError::invalid_request(message)),
                Err(Refused::Duplicate(message)) => Err(ApiError::conflict(format!("{message} exists"))),
                Err(Refused::NotAGuest) => {
                    Err(ApiError::conflict("this identity is no longer a guest"))
                }
                Err(other) => Err(internal(format!("{other:?}"))),
            };
        }
    }
    match concord_db::auth::register_user(&state.pool, &body.username, &display_name, &body.password)
        .await
    {
        Ok((user, token)) => Ok((
            axum::http::StatusCode::CREATED,
            Json(json!({
                "user": serde_json::to_value(user).map_err(internal)?,
                "token": token,
                "token_note": TOKEN_NOTE,
            })),
        )),
        Err(AuthError::Invalid(message)) => Err(ApiError::invalid_request(message)),
        Err(AuthError::Duplicate(message)) => Err(ApiError::conflict(format!("{message} exists"))),
        Err(AuthError::Credentials(message)) => Err(ApiError::unauthenticated(message)),
        Err(AuthError::NoCredential) => Err(ApiError::unauthenticated("invalid credentials")),
        Err(AuthError::Database(error)) => Err(internal(error)),
    }
}


/// The registration route, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new().route("/api/v1/auth/register", axum::routing::post(register))
}
