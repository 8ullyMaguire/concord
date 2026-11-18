//! Consent, guest identities, login and the request gates (step 3, ADR 0014).
//!
//! The whole section was lifted verbatim from `lib.rs`. These functions are the
//! app's identity surface: the router installs the gates and middleware, other
//! handlers borrow `acting_user`/`coded`, and the cookie tests exercise the
//! token helpers in place (D-32).

use axum::{
    extract::{Extension, State},
    http::{header::HeaderMap, StatusCode},
    routing::Router,
    Json,
    response::IntoResponse,
};
use polaris_core::ApiError;
use serde::Deserialize;
use serde_json::Value;

use crate::{internal, policy, AppState, TOKEN_NOTE};

// ── The consent gate, guest identities, and login ────────────────────────────────────────────────────────────────

/// The cookie that records the accepted notice. HttpOnly because the page is TOLD the state by the server rather than reading it:
/// a cookie a script can read is a cookie a script can forge, and the only reason to read it in JavaScript would be to skip the
/// server's answer.
pub(crate) const CONSENT_COOKIE: &str = "concord_consent";

/// A year. The notice is about cookies, and a notice that re-asks every week is a notice people dismiss without reading.
pub(crate) const CONSENT_MAX_AGE: i64 = 365 * 24 * 60 * 60;

/// The navigation cookie. Accepted ONLY on a GET whose path is not under `/api/` — every mutating route is an API route, so this
/// cookie structurally cannot authorize a write, and the bearer header remains the only credential for the API. Without it a
/// member page would be cacheable and shareable, and with it a GET is a session.
pub(crate) const NAV_COOKIE: &str = "concord_nav";

/// Methods that change something.
pub(crate) fn is_mutating(method: &axum::http::Method) -> bool {
    use axum::http::Method;
    matches!(
        *method,
        Method::POST | Method::PUT | Method::PATCH | Method::DELETE
    )
}

/// Paths the gate does not touch.
///
/// Establishing or ending an identity is not an interaction with the project's data, and hiding registration behind a cookie
/// banner is a dark pattern: a visitor must be able to create an account, and to leave, without agreeing to anything. The
/// webhook is server-to-server and has no browser and no cookie to accept.
pub(crate) fn consent_exempt(path: &str) -> bool {
    path == "/api/v1/consent"
        || path == "/api/v1/guest"
        || path.starts_with("/api/v1/auth/")
        || path == "/api/v1/hooks/forgejo"
        || !path.starts_with("/api/")
}

/// One cookie's value from the request headers.
pub(crate) fn cookie_value(headers: &HeaderMap, name: &str) -> Option<String> {
    let raw = headers.get(axum::http::header::COOKIE)?.to_str().ok()?;
    raw.split(';')
        .map(str::trim)
        .find_map(|pair| pair.strip_prefix(&format!("{name}=")).map(str::to_string))
}

/// An empty header is NOT a credential: the page used to build it from `localStorage`, so a caller holding an empty string must
/// fall through to the cookie instead of shadowing it. The cookie is `SameSite=Lax`, so a cross-site form post does not carry it —
/// the CSRF boundary is the cookie attribute, not a refusal to read the cookie.
///
/// The credential this request presents: the `Authorization: Bearer` header, else the `concord_nav` cookie.
///
/// The header came first historically because the page kept its token in `localStorage` — which is readable by any script that
/// gets to run, the exact thing `script-src 'self'` narrows but cannot promise. The cookie the login route already sets is
/// `HttpOnly; SameSite=Lax`, so it is unreadable from a script and not sent on a cross-site POST: accepting it as a credential is
/// what lets the page stop storing the token at all. Header first so every existing client and every suite keeps working while the
/// two credentials are both live; the cookie is not weaker, it is the same token.
pub(crate) fn presented_token(headers: &HeaderMap) -> Option<String> {
    headers
        .get(axum::http::header::AUTHORIZATION)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.strip_prefix("Bearer "))
        // An empty `Bearer ` is not a credential, and treating it as one would let it SHADOW a valid cookie: the page
        // used to build that header from `localStorage`, and any caller with an empty token would otherwise be refused
        // while holding a perfectly good session cookie.
        .filter(|token| !token.is_empty())
        .map(str::to_string)
        .or_else(|| cookie_value(headers, NAV_COOKIE))
}

/// The token to revoke: the header if present, else the navigation cookie.
///
/// Logout is the one route that reads the cookie, because a browser that navigates to `/logout` may send only that. Revoking is
/// not a privileged action — it only ever ends the session it was handed — so accepting the weaker credential here costs nothing
/// that accepting it on a write would.
pub(crate) fn presented_token_for_logout(headers: &HeaderMap) -> Option<String> {
    presented_token(headers).or_else(|| cookie_value(headers, NAV_COOKIE))
}

#[cfg(test)]
mod credential_tests {
    use super::{presented_token, presented_token_for_logout, NAV_COOKIE};
    use axum::http::{header, HeaderMap, HeaderValue};

    fn headers(bearer: Option<&str>, cookie: Option<&str>) -> HeaderMap {
        let mut h = HeaderMap::new();
        if let Some(token) = bearer {
            h.insert(
                header::AUTHORIZATION,
                HeaderValue::from_str(&format!("Bearer {token}")).unwrap(),
            );
        }
        if let Some(value) = cookie {
            h.insert(
                header::COOKIE,
                HeaderValue::from_str(&format!("{NAV_COOKIE}={value}")).unwrap(),
            );
        }
        h
    }

    /// The regression the page half depends on: a script that used to read `localStorage` can still send `Bearer ` with an empty
    /// token after a reload. Treating that as a credential would refuse a browser that holds a perfectly good session cookie.
    #[test]
    fn an_empty_bearer_does_not_shadow_the_cookie() {
        let h = headers(Some(""), Some("cookie-token"));
        assert_eq!(presented_token(&h).as_deref(), Some("cookie-token"));
    }

    #[test]
    fn a_real_header_still_wins_over_the_cookie() {
        let h = headers(Some("header-token"), Some("cookie-token"));
        assert_eq!(presented_token(&h).as_deref(), Some("header-token"));
    }

    #[test]
    fn a_cookie_without_a_header_is_a_credential() {
        let h = headers(None, Some("cookie-token"));
        assert_eq!(presented_token(&h).as_deref(), Some("cookie-token"));
        assert_eq!(presented_token_for_logout(&h).as_deref(), Some("cookie-token"));
    }

    #[test]
    fn neither_credential_is_none() {
        assert!(presented_token(&headers(None, None)).is_none());
    }
}

/// The user this request acts as, by either credential.
pub(crate) async fn acting_user(
    state: &AppState,
    headers: &HeaderMap,
) -> Result<Option<concord_db::auth::User>, ApiError> {
    let Some(token) = presented_token(headers) else {
        return Ok(None);
    };
    concord_db::auth::user_for_token(&state.pool, &token)
        .await
        .map_err(|error| match error {
            concord_db::auth::AuthError::Database(error) => internal(error),
            other => internal(format!("{other:?}")),
        })
}


/// The reference's error body shape, restored at the boundary.
///
/// The estate's `polaris_core::ApiError` answers `{"error": {"code": ..., "message": ...}}`. The Go service this port replaces answers
/// `{"error": "a sentence"}`, and its own page scripts read exactly that: `consensus.js` does
/// `throw new Error(data.error || ('HTTP ' + r.status))`, so a nested body renders as `[object Object]` in the alert a reader is supposed
/// to act on. Three browser suites were failing on that literal string.
///
/// The fix is here rather than in `polaris_core` because that crate is the ESTATE's contract — every other site on the profile answers the
/// nested shape, and re-shaping it for one port would break the thing the port exists to conform to. It is here rather than in each handler
/// because a hundred call sites is a hundred chances to forget, and the transformation is mechanical: an error status with a JSON body of
/// exactly `{"error": {"code", "message"}}` becomes `{"error": "message", "code": "code"}`, which is what the reference's `mapError` and its
/// hand-written `coded` refusals both emit. A body that does not match that shape is passed through untouched — including a successful one,
/// and including an error whose body is a plain sentence already.
pub(crate) async fn flatten_error_bodies(response: axum::response::Response) -> axum::response::Response {
    use axum::body::Body;
    use axum::response::IntoResponse;

    if !response.status().is_client_error() && !response.status().is_server_error() {
        return response;
    }
    let is_json = response
        .headers()
        .get(axum::http::header::CONTENT_TYPE)
        .and_then(|value| value.to_str().ok())
        .is_some_and(|value| value.starts_with("application/json"));
    if !is_json {
        return response;
    }
    let (parts, body) = response.into_parts();
    let Ok(bytes) = axum::body::to_bytes(body, 64 * 1024).await else {
        return axum::response::Response::from_parts(parts, Body::empty());
    };
    let Ok(value) = serde_json::from_slice::<serde_json::Value>(&bytes) else {
        return axum::response::Response::from_parts(parts, Body::from(bytes));
    };
    let flattened = match value.get("error") {
        Some(serde_json::Value::Object(inner)) => {
            let message = inner.get("message").cloned().unwrap_or(serde_json::Value::Null);
            let code = inner.get("code").cloned().unwrap_or(serde_json::Value::Null);
            serde_json::json!({ "error": message, "code": code })
        }
        _ => value,
    };
    (parts, axum::Json(flattened)).into_response()
}

/// A refusal with a `code` a client can switch on and a sentence a person can read.
pub(crate) fn coded(status: StatusCode, code: &str, message: &str) -> axum::response::Response {
    use axum::response::IntoResponse;
    (
        status,
        Json(serde_json::json!({ "code": code, "error": message })),
    )
        .into_response()
}

/// The response headers that turn a page into something a browser is allowed to trust.
///
/// Measured on 2026-11-15: the deployed instance sent **no** `Content-Security-Policy`, no `X-Frame-Options`, no
/// `X-Content-Type-Options` and no `Strict-Transport-Security` — `curl -sI https://concord.polarisocial.xyz/` was the
/// whole proof. With the session token in `localStorage` and 137 `innerHTML` sites in the page scripts, one injected
/// string is a full account takeover, and nothing in the response told the browser to refuse one.
///
/// `script-src 'self'` with **no `'unsafe-inline'`** — the live instance on 2026-11-16 sent
/// `script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'`, which permits exactly the injected `<script>` the policy
/// exists to refuse. `'unsafe-inline'` is not a lesser evil here, it is the evil: with it a CSP does nothing for XSS.
///
/// No nonce either. A nonce has to be threaded through every one of the 39 `<script>` tags this renderer emits, and its
/// absence is invisible until a page silently stops working. Removing the inline blocks instead (`crates/web` — the
/// seven dead placeholder passes and the archive control, now `project-archive.js`) makes the *stricter* policy the one
/// that needs no plumbing. Styles keep `'unsafe-inline'` because two pages carry a `style=` attribute; scripts do not.
///
/// `img-src` allows `https:` because avatars are remote; `connect-src 'self'` because every `fetch` in the page
/// scripts goes to this origin (measured: `grep -c 'fetch(' assets/js/*.js` finds four call sites, all through
/// `api.js`). `frame-ancestors 'none'` and `X-Frame-Options: DENY` say the same thing twice on purpose: the older
/// header is what browsers without `frame-ancestors` understand.
pub(crate) fn security_headers(request: axum::extract::Request, next: axum::middleware::Next) -> impl std::future::Future<Output = axum::response::Response> {
    const POLICY: &str = "default-src 'self'; \
        script-src 'self'; \
        style-src 'self' 'unsafe-inline'; \
        img-src 'self' data: https:; \
        font-src 'self'; \
        connect-src 'self'; \
        object-src 'none'; \
        base-uri 'none'; \
        form-action 'self'; \
        frame-ancestors 'none'";
    async move {
        let mut response = next.run(request).await;
        let headers = response.headers_mut();
        let set = |headers: &mut axum::http::HeaderMap, name: axum::http::HeaderName, value: &str| {
            if let Ok(value) = axum::http::HeaderValue::from_str(value) {
                headers.insert(name, value);
            }
        };
        set(headers, axum::http::header::CONTENT_SECURITY_POLICY, POLICY);
        set(headers, axum::http::HeaderName::from_static("x-frame-options"), "DENY");
        set(headers, axum::http::header::X_CONTENT_TYPE_OPTIONS, "nosniff");
        set(headers, axum::http::header::REFERRER_POLICY, "strict-origin-when-cross-origin");
        set(
            headers,
            axum::http::header::STRICT_TRANSPORT_SECURITY,
            "max-age=31536000; includeSubDomains",
        );
        response
    }
}

/// Consent and the guest cap, in the one place both are enforced.
///
/// Consent is a GATE, not a credential: it permits, while identity still comes from the bearer token. That is why the cookie is
/// safe to set from a page the visitor does not control, and why planting it gains an attacker nothing.
///
/// The guest cap is checked on every guest write and not only at mint: mint-only would let an instance cross the cap with a
/// visitor mid-session and keep serving that visitor forever, which is exactly the state the cap exists to end. A guest is the
/// only identity that can be refused this way, and a store failure while checking fails CLOSED for a guest — an instance that
/// cannot say how big it is must not keep taking nameless contributions.
/// Authentication as a property of the route, not a line in the handler.
///
/// The route's own declaration is the input (`policy::policy_for`), matched against the path **axum registered**
/// — `{slug}` and all — so a caller cannot reach a policy by inventing a path. Three outcomes:
///
/// * a page route (not `/api/…`): pass. Pages are `concord_web`'s and decide their own visibility;
/// * a declared route: pass when the policy is `Public`; for `Session`/`Account`/`Operator` require a resolvable
///   identity, refusing an anonymous caller with the same envelope and message the handlers use;
/// * an undeclared route: `500`, because a route nobody declared is a deployment bug and silence is what this
///   layer exists to end. `scripts/parity/route_policies.py` makes this unreachable in a passing build.
///
/// What it deliberately does **not** do yet: refuse a guest on an `Account` route, or a non-operator on an
/// `Operator` one. Those two refusals live inside handlers today, and moving them is step 1b — a change that
/// wants the e2e suites watching, because it moves a refusal from after the body is read to before it.
pub(crate) async fn auth_gate(
    State(state): State<AppState>,
    request: axum::extract::Request,
    next: axum::middleware::Next,
) -> axum::response::Response {
    // No `MatchedPath` means axum matched nothing and the request is headed for the fallback's 404. That is a caller's
    // mistake, not a missing declaration, and turning it into a 500 would replace every unknown-URL 404 with a bug.
    let Some(matched) = request.extensions().get::<axum::extract::MatchedPath>() else {
        return next.run(request).await;
    };
    let route = matched.as_str().to_string();
    if !route.starts_with("/api/") {
        return next.run(request).await;
    }
    let method = request.method().as_str().to_uppercase();
    let Some((policy, enforced)) = policy::policy_for(&method, &route) else {
        tracing::error!(%method, %route, "no policy declared for this route");
        return coded(
            StatusCode::INTERNAL_SERVER_ERROR,
            "route_declares_no_policy",
            "this route declares no policy; the deployment is incomplete",
        )
        .into_response();
    };
    // Declared is not the same as enforced. Every row says who may call the route; `enforced` says whether this
    // layer is the one refusing yet. It started at the five routes the live instance was measured on — four that
    // already answered 401 to an anonymous caller and one, the admin ledger, that answered 200. Widening it is
    // step 1b: it moves a refusal from inside a handler to in front of it, which changes the message and what
    // the body costs, so each surface flips with the e2e suites watching. The count is a ratchet in policy.rs.
    if !enforced || !policy.requires_identity() {
        return next.run(request).await;
    }
    match acting_user(&state, request.headers()).await {
        Ok(Some(user)) => {
            // Only enforced rows reach here. The handler consumes this identity from its extensions rather than
            // opening a second store read and writing its own refusal — the 122-copy pattern this step removes.
            let mut request = request;
            request.extensions_mut().insert(user);
            next.run(request).await
        }
        Ok(None) => ApiError::unauthenticated("authentication required").into_response(),
        Err(error) => {
            // A store failure is not "you are not signed in". Report it as what it is rather than turning a database
            // blip into an authentication failure that the client would answer by logging the user out.
            tracing::warn!(error = ?error, %route, "identity could not be resolved");
            coded(
                StatusCode::SERVICE_UNAVAILABLE,
                "identity_unavailable",
                "this instance could not check who is calling; try again",
            )
            .into_response()
        }
    }
}

pub(crate) async fn interaction_gate(
    State(state): State<AppState>,
    request: axum::extract::Request,
    next: axum::middleware::Next,
) -> axum::response::Response {
    use axum::response::IntoResponse;
    let path = request.uri().path().to_string();
    if !is_mutating(request.method()) || consent_exempt(&path) {
        return next.run(request).await;
    }
    let version = concord_db::identity::GuestStatus::consent_version_from_env();
    if cookie_value(request.headers(), CONSENT_COOKIE).as_deref() != Some(version.as_str()) {
        return (
            StatusCode::FORBIDDEN,
            Json(serde_json::json!({
                "code": "consent_required",
                "error": "cookies must be accepted before interacting with this site",
                "consent": "required",
            })),
        )
            .into_response();
    }
    let Ok(Some(user)) = acting_user(&state, request.headers()).await else {
        // No identity, or one that cannot be resolved: the route's own gates decide. A store error here must not become a 403
        // for a legitimate account, so it is reported by the handler that needs the actor.
        return next.run(request).await;
    };
    let guest = concord_db::identity::is_guest(&state.pool, user.id)
        .await
        .unwrap_or(false);
    if !guest {
        return next.run(request).await;
    }
    let limit = concord_db::identity::GuestStatus::limit_from_env();
    match concord_db::identity::guest_status(&state.pool, limit).await {
        Ok(status) if status.allowed => next.run(request).await,
        Ok(status) => {
            use axum::response::IntoResponse;
            (
                StatusCode::FORBIDDEN,
                Json(serde_json::json!({
                    "code": "guest_limit_reached",
                    "error": "this instance has grown past its guest limit; create an account to take part",
                    "mau": status.mau,
                    "limit": status.limit,
                })),
            )
                .into_response()
        }
        Err(error) => {
            tracing::warn!(%error, "guest limit check failed");
            coded(
                StatusCode::SERVICE_UNAVAILABLE,
                "guest_check_failed",
                "guest contributions are paused: this instance could not check its own size",
            )
        }
    }
}

/// `GET /api/v1/guest` — the public, read-only question a page asks before it offers to let a visitor act.
pub(crate) async fn guest_status_handler(
    State(state): State<AppState>,
    headers: HeaderMap,
) -> Result<Json<Value>, ApiError> {
    let limit = concord_db::identity::GuestStatus::limit_from_env();
    let status = concord_db::identity::guest_status(&state.pool, limit)
        .await
        .map_err(internal)?;
    let version = concord_db::identity::GuestStatus::consent_version_from_env();
    Ok(Json(serde_json::json!({
        "allowed": status.allowed,
        "mau": status.mau,
        "limit": status.limit,
        "window_days": status.window_days,
        "guests": status.guests,
        "stale_guests": status.stale_guests,
        "consent_version": version,
        "consented": cookie_value(&headers, CONSENT_COOKIE).as_deref() == Some(version.as_str()),
    })))
}

/// `POST /api/v1/guest` — mint a guest identity.
///
/// Consent is already enforced by the gate, so reaching this handler means the visitor accepted the notice. The token is the
/// identity's only credential and is returned once.
pub(crate) async fn claim_guest(
    State(state): State<AppState>,
) -> Result<axum::response::Response, ApiError> {
    use axum::response::IntoResponse;
    use concord_db::identity::Refused;
    let limit = concord_db::identity::GuestStatus::limit_from_env();
    match concord_db::identity::create_guest(&state.pool, limit).await {
        Ok((user, token)) => Ok((
            StatusCode::CREATED,
            Json(serde_json::json!({
                "user": serde_json::to_value(user).map_err(internal)?,
                "token": token,
                "token_note": TOKEN_NOTE,
            })),
        )
            .into_response()),
        Err(Refused::LimitReached { mau, limit }) => Ok((
            StatusCode::FORBIDDEN,
            Json(serde_json::json!({
                "code": "guest_limit_reached",
                "error": "this instance has grown past its guest limit; create an account to take part",
                "mau": mau,
                "limit": limit,
            })),
        )
            .into_response()),
        Err(Refused::CheckFailed(error)) => Err(internal(error)),
        Err(other) => Err(internal(format!("{other:?}"))),
    }
}

/// `POST /api/v1/consent` — record acceptance: the cookie the gate reads, plus the identity's own record when there is an identity
/// to record it against.
pub(crate) async fn accept_consent(
    State(state): State<AppState>,
    headers: HeaderMap,
) -> Result<axum::response::Response, ApiError> {
    use axum::response::IntoResponse;
    let version = concord_db::identity::GuestStatus::consent_version_from_env();
    let secure = headers
        .get("x-forwarded-proto")
        .and_then(|value| value.to_str().ok())
        .is_some_and(|value| value.eq_ignore_ascii_case("https"));
    let mut response = Json(serde_json::json!({ "ok": true, "consent": version })).into_response();
    let cookie = format!(
        "{CONSENT_COOKIE}={version}; Path=/; Max-Age={CONSENT_MAX_AGE}; HttpOnly; SameSite=Lax{}",
        if secure { "; Secure" } else { "" }
    );
    if let Ok(value) = axum::http::HeaderValue::from_str(&cookie) {
        response
            .headers_mut()
            .append(axum::http::header::SET_COOKIE, value);
    }
    // A failure to record it against the identity is reported in the log and NOT turned into a failed acceptance: the visitor did
    // accept, and a 500 would tell them otherwise.
    if let Some(user) = acting_user(&state, &headers).await? {
        if let Err(error) = concord_db::identity::record_consent(&state.pool, user.id).await {
            tracing::warn!(%error, "record consent");
        }
    }
    Ok(response)
}

#[derive(Debug, Deserialize)]
pub(crate) struct LoginRequest {
    username: String,
    password: String,
}

/// `POST /api/v1/auth/login`.
///
/// The fold runs AFTER a successful login and its failure does not undo it: someone who has just proved they own an account must
/// not be stranded by a 500 from a housekeeping step. It is reported in the response instead, beside the counts it produced.
pub(crate) async fn login(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(body): Json<LoginRequest>,
) -> Result<axum::response::Response, ApiError> {
    use axum::response::IntoResponse;
    use concord_db::auth::AuthError;
    let (user, token) = match concord_db::auth::login(&state.pool, &body.username, &body.password).await {
        Ok(pair) => pair,
        Err(AuthError::Credentials(message) | AuthError::Invalid(message)) => {
            return Err(ApiError::unauthenticated(message))
        }
        Err(AuthError::NoCredential) => {
            return Err(ApiError::unauthenticated("invalid credentials"))
        }
        Err(AuthError::Duplicate(message)) => return Err(ApiError::conflict(message)),
        Err(AuthError::Database(error)) => return Err(internal(error)),
    };
    // The guest this browser was acting as, if any: its rows genuinely have to move, because a second identity already holds the
    // token this visitor proved they own.
    let mut fold = concord_db::identity::FoldReport::default();
    let mut fold_error: Option<String> = None;
    if let Some(actor) = acting_user(&state, &headers).await? {
        if actor.id != user.id {
            let guest = concord_db::identity::is_guest(&state.pool, actor.id)
                .await
                .unwrap_or(false);
            if guest {
                match concord_db::identity::fold_guest_into(&state.pool, actor.id, user.id).await {
                    Ok(report) => fold = report,
                    Err(error) => fold_error = Some(format!("{error:?}")),
                }
            }
        }
    }
    let mut body_json = serde_json::json!({
        "user": serde_json::to_value(&user).map_err(internal)?,
        "token": token,
        "token_note": TOKEN_NOTE,
    });
    if fold.moved > 0 || fold.superseded > 0 {
        body_json["fold"] = serde_json::to_value(&fold).map_err(internal)?;
    }
    if let Some(message) = fold_error {
        body_json["fold_error"] = Value::String(message);
    }
    let mut response = Json(body_json).into_response();
    // The navigation cookie is a SESSION cookie (no Max-Age): the localStorage token lives until it is revoked, and the navigation
    // convenience should not outlive the browser session that created it. Clearing it on logout is what makes logout look like it
    // happened.
    let cookie = nav_cookie(&format!("={token}"), &headers);
    if let Ok(value) = axum::http::HeaderValue::from_str(&cookie) {
        response
            .headers_mut()
            .append(axum::http::header::SET_COOKIE, value);
    }
    Ok(response)
}

/// `POST /api/v1/auth/logout` — revoke the presented token. Idempotent: a client that logs out twice, or whose token was already
/// revoked, gets 204 both times.
pub(crate) async fn logout(State(state): State<AppState>, headers: HeaderMap) -> Result<axum::response::Response, ApiError> {
    use axum::response::IntoResponse;
    if let Some(token) = presented_token_for_logout(&headers) {
        concord_db::auth::revoke_token(&state.pool, &token)
            .await
            .map_err(|error| match error {
                concord_db::auth::AuthError::Database(error) => internal(error),
                other => internal(format!("{other:?}")),
            })?;
    }
    let mut response = StatusCode::NO_CONTENT.into_response();
    // A cookie left behind would keep rendering member pages for a credential that no longer resolves.
    let cookie = nav_cookie("", &headers);
    if let Ok(value) = axum::http::HeaderValue::from_str(&cookie) {
        response
            .headers_mut()
            .append(axum::http::header::SET_COOKIE, value);
    }
    Ok(response)
}

/// The `concord_nav` Set-Cookie value. `payload` is `=<token>` to set it, or empty to expire it.
pub(crate) fn nav_cookie(payload: &str, headers: &HeaderMap) -> String {
    let secure = headers
        .get("x-forwarded-proto")
        .and_then(|value| value.to_str().ok())
        .is_some_and(|value| value.eq_ignore_ascii_case("https"));
    let base = format!(
        "{NAV_COOKIE}{payload}; Path=/; HttpOnly; SameSite=Lax{}",
        if secure { "; Secure" } else { "" }
    );
    if payload.is_empty() {
        format!("{base}; Max-Age=-1")
    } else {
        base
    }
}



/// `GET /api/v1/auth/me` — the caller, by id: the token carries only an id, so the name is looked up here.
async fn auth_me(
    Extension(user): Extension<concord_db::auth::User>,
) -> Result<Json<serde_json::Value>, ApiError> {
    // The Go answers `role: "member"` for every caller — it is not this project's role, it is "you are signed
    // in". Kept identical rather than improved: a client that switches on it exists.
    Ok(Json(serde_json::json!({
        "id": user.id,
        "username": user.username,
        "display_name": user.display_name,
        "created_at": user.created_at,
        "role": "member",
    })))
}

/// The identity routes, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new()
        .route("/api/v1/guest", axum::routing::get(guest_status_handler).post(claim_guest))
        .route("/api/v1/consent", axum::routing::post(accept_consent))
        .route("/api/v1/auth/login", axum::routing::post(login))
        .route("/api/v1/auth/logout", axum::routing::post(logout))
        .route("/api/v1/auth/me", axum::routing::get(auth_me))
}
