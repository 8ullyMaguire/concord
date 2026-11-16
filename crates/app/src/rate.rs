//! How many requests a client may make, and what happens on the one that exceeds it.
//!
//! Measured on 2026-11-16, before this module existed: `grep -rn "rate\|ratelimit" crates/app/src/lib.rs` found nothing,
//! and a loop of forty `POST /api/v1/auth/login` requests from one address was answered forty times. The vendored
//! `polaris_ratelimit` crate has been in `vendor/` since the port — `cargo tree -i polaris-ratelimit` named it as a
//! dependency of nothing, so 592 lines of tested bucket arithmetic sat next to a login form that had no budget at all.
//!
//! Three decisions worth the words:
//!
//! * **The class is chosen by the matched route, not by the caller.** A route that mints sessions draws from `login`
//!   whatever the path looks like; everything under `/assets/` draws from `asset`, because one page load asks for a
//!   dozen scripts and charging those to the same bucket as the API would refuse a reader for reading.
//! * **The client is `client_key`'s answer, never the raw header.** It reads `CF-Connecting-IP` and `X-Forwarded-For`
//!   only from a loopback peer, so a client that mints its own identity has to be nginx first.
//! * **A refusal is a plain 429 with `Retry-After`**, because that is the one part of a rate limit a client can act on.
//!   The budget is also echoed on every allowed response: a limit nobody can observe is a limit nobody can tune.

use std::net::{IpAddr, SocketAddr};
use std::sync::Arc;
use std::time::Instant;

use axum::extract::{MatchedPath, Request, State};
use axum::http::{header, HeaderName, HeaderValue, StatusCode};
use axum::middleware::Next;
use axum::response::{IntoResponse, Response};
use polaris_ratelimit::{client_key, Limits, Limiter, Quota};

use crate::AppState;

/// The class a route draws from. `default` catches anything unclassified rather than exempting it.
pub fn class_for(matched: Option<&str>, raw_path: &str) -> &'static str {
    let path = matched.unwrap_or(raw_path);
    if path.starts_with("/assets/") || path == "/favicon.ico" || path == "/robots.txt" {
        return "asset";
    }
    // Session-minting routes: the ones a credential-stuffing loop aims at. `/api/v1/auth/login` and `register` both,
    // plus the guest handshake, because it also writes a session row.
    if path.ends_with("/auth/login") || path.ends_with("/auth/register") || path.ends_with("/guests") {
        return "login";
    }
    if path.starts_with("/api/") {
        return "api";
    }
    "default"
}

/// The budgets, with every one of them overridable from the environment.
///
/// The defaults are generous on purpose: this is the floor that stops a loop, not the shape of normal traffic, and a
/// limiter that trips on an ordinary reader is a worse bug than the one it fixes. The e2e suites and the probe lower
/// them per process (`CONCORD_RATE_*`) so the refusal is provable in seconds instead of by flooding a real budget.
pub fn limits_from_env() -> Limits {
    fn permits(var: &str, fallback: u32) -> u32 {
        std::env::var(var)
            .ok()
            .and_then(|raw| raw.trim().parse::<u32>().ok())
            .unwrap_or(fallback)
    }
    Limits::new(Quota::per_minute(permits("CONCORD_RATE_DEFAULT", 300)))
        .class("api", Quota::per_minute(permits("CONCORD_RATE_API", 600)))
        .class("login", Quota::per_minute(permits("CONCORD_RATE_LOGIN", 10)))
        .class("asset", Quota::per_minute(permits("CONCORD_RATE_ASSET", 3_000)))
}

/// The client address this request is budgeted against.
///
/// `ConnectInfo` is present because `serve` builds its listener with `into_make_service_with_connect_info`. When it is
/// absent — a unit test calling the router directly — the peer is treated as loopback, which is what a test is.
fn peer_of(request: &Request) -> IpAddr {
    let peer = request
        .extensions()
        .get::<axum::extract::ConnectInfo<SocketAddr>>()
        .map(|info| info.0.ip())
        .unwrap_or_else(|| IpAddr::from([127, 0, 0, 1]));
    let header = |name: &str| {
        request
            .headers()
            .get(name)
            .and_then(|value| value.to_str().ok())
            .map(str::to_owned)
    };
    client_key(
        peer,
        header("cf-connecting-ip").as_deref(),
        header("x-forwarded-for").as_deref(),
    )
}

pub async fn rate_gate(State(state): State<AppState>, request: Request, next: Next) -> Response {
    let matched = request
        .extensions()
        .get::<MatchedPath>()
        .map(|path| path.as_str().to_owned());
    let class = class_for(matched.as_deref(), request.uri().path());
    let client = peer_of(&request);

    let decision = state.rate.check_at(&client.to_string(), class, Instant::now());
    let limit = decision.limit.to_string();
    let remaining = decision.remaining.to_string();

    if !decision.allowed {
        // Seconds, rounded up: `Retry-After` is an integer and a client that retries at 0.4s has been told to retry
        // before a permit exists.
        let wait = decision
            .retry_after
            .map(|after| after.as_secs_f64().ceil().max(1.0) as u64)
            .unwrap_or(1);
        let body = serde_json::json!({
            "code": "rate_limited",
            "error": format!("too many requests: this route allows {}/min for this client", decision.limit),
            "retry_after": wait,
        });
        let mut response = (StatusCode::TOO_MANY_REQUESTS, axum::Json(body)).into_response();
        let headers = response.headers_mut();
        for (name, value) in [
            (header::RETRY_AFTER, Some(wait.to_string())),
            (HeaderName::from_static("x-ratelimit-limit"), Some(limit)),
            (HeaderName::from_static("x-ratelimit-remaining"), Some(remaining)),
        ] {
            if let Some(value) = value.and_then(|raw| HeaderValue::from_str(&raw).ok()) {
                headers.insert(name, value);
            }
        }
        return response;
    }

    let mut response = next.run(request).await;
    if let Ok(value) = HeaderValue::from_str(&limit) {
        response.headers_mut().insert(HeaderName::from_static("x-ratelimit-limit"), value);
    }
    if let Ok(value) = HeaderValue::from_str(&remaining) {
        response
            .headers_mut()
            .insert(HeaderName::from_static("x-ratelimit-remaining"), value);
    }
    response
}

/// The limiter a process starts with.
#[must_use]
pub fn limiter_from_env() -> Arc<Limiter> {
    let limits = limits_from_env();
    // A misconfigured table is a boot-time sentence, not a 500 on the first request: `validate` refuses a zero default,
    // which would otherwise refuse every unclassified route while looking like a healthy service.
    if let Err(error) = limits.validate() {
        eprintln!("WARNING: rate limits are invalid ({error}) — using built-in defaults");
        return Arc::new(Limiter::new(
            Limits::new(Quota::per_minute(300))
                .class("api", Quota::per_minute(600))
                .class("login", Quota::per_minute(10))
                .class("asset", Quota::per_minute(3_000)),
        ));
    }
    {
        let limits = limits.clone();
        tracing::info!(classes = ?limits.names().collect::<Vec<_>>(), "rate limits");
    }
    Arc::new(Limiter::new(limits))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;

    #[test]
    fn the_class_follows_the_route_not_the_caller() {
        assert_eq!(class_for(Some("/api/v1/auth/login"), "/x"), "login");
        assert_eq!(class_for(Some("/api/v1/auth/register"), "/x"), "login");
        assert_eq!(class_for(Some("/api/v1/guests"), "/x"), "login");
        assert_eq!(class_for(Some("/api/v1/projects/{slug}"), "/x"), "api");
        assert_eq!(class_for(Some("/assets/js/api.js"), "/x"), "asset");
        assert_eq!(class_for(Some("/projects"), "/x"), "default");
        // An unmatched path (the 404 fallback) still draws from `default` rather than being free.
        assert_eq!(class_for(None, "/nope"), "default");
    }

    #[test]
    fn the_login_bucket_refuses_and_says_when_to_retry() {
        let limiter = Limiter::new(
            Limits::new(Quota::per_minute(100)).class("login", Quota::per_minute(2)),
        );
        let start = Instant::now();
        let allowed = (0..2)
            .filter(|n| limiter.check_at("1.2.3.4", "login", start + Duration::from_secs(*n)).allowed)
            .count();
        assert_eq!(allowed, 2);
        let refused = limiter.check_at("1.2.3.4", "login", start + Duration::from_secs(2));
        assert!(!refused.allowed, "the third request in a 2/min bucket must be refused");
        let wait = refused.retry_after.expect("a refusal must say how long to wait");
        assert!(wait > Duration::ZERO && wait <= Duration::from_secs(30), "got {wait:?}");
        // A different client has its own bucket: the point of a key, and the reason one loop must not silence a site.
        assert!(limiter.check_at("5.6.7.8", "login", start + Duration::from_secs(2)).allowed);
    }
}
