//! One health answer for every estate site.
//!
//! # Why this exists (measured 2026-11-09)
//!
//! Wave 2's top-ranked candidate was "healthz + version" (leverage 176), measured as 16 repos / 147 files. The file count
//! was less interesting than what the *live* estate answers, so all 22 answering hostnames were probed:
//!
//! * `x-estate-version: <site>@2026.11.3` — **universal**, 22 of 22, injected at nginx. That half of the convention is
//!   already shared and enforced.
//! * the body — **fourteen distinct shapes**, including eleven hostnames whose `/healthz` returns the site's own HTML
//!   index page with `200`. A monitor reading the status code calls those healthy; nothing ever checked the body.
//! * the honest ones disagree with each other: darkhold `{status, version, build, environment, uptime_ms}`, ascend
//!   `{status, version}`, suki `{db, status}`, media `{ok, storage}`, id `ok`, kindred a rank-budget document under
//!   `/api/v1/health`.
//!
//! So the shared thing is not "a health route" (everyone has a route). It is **the shape and the status code**: `status`
//! is the field a monitor reads, and a body that is HTML must be a 5xx, not a 200.
//!
//! # What this crate does not do
//!
//! It owns no framework binding: `Body` serialises, `code()` picks 200 or 503, and the product's router returns it. That
//! keeps this crate usable from axum 0.7 and 0.8 (the estate has both) without a shared state type — the mistake ADR 0006
//! recorded for rate limiting.

use serde::{Deserialize, Serialize};

/// The version a site reports. Taken from `ESTATE_VERSION` (set by the deploy) and falling back to the crate's own
/// version, because a health endpoint that cannot say what is running is a health endpoint that lies during a rollback.
pub fn version() -> String {
    std::env::var("ESTATE_VERSION").unwrap_or_else(|_| env!("CARGO_PKG_VERSION").to_string())
}

/// How one dependency answered.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum CheckState {
    /// Answered in time and said what it should.
    Ok,
    /// Answered, but not usefully (a replica lagging, a cache refusing writes).
    Degraded,
    /// Did not answer, or answered wrongly.
    Down,
}

/// One named dependency and its state. The name is the product's ("db", "search", "media"), never a connection string.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Check {
    pub name: String,
    pub state: CheckState,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
}

impl Check {
    pub fn ok(name: impl Into<String>) -> Check {
        Check { name: name.into(), state: CheckState::Ok, detail: None }
    }

    pub fn degraded(name: impl Into<String>, detail: impl Into<String>) -> Check {
        Check { name: name.into(), state: CheckState::Degraded, detail: Some(detail.into()) }
    }

    pub fn down(name: impl Into<String>, detail: impl Into<String>) -> Check {
        Check { name: name.into(), state: CheckState::Down, detail: Some(detail.into()) }
    }
}

/// The answer. `status` is the only field a monitor must read, and it is a string rather than a boolean because
/// "degraded" is a state a boolean cannot express — and the estate has services that work while a dependency lags.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Body {
    pub status: &'static str,
    pub version: String,
    pub checks: Vec<Check>,
}

/// Build the body from the checks the product ran.
///
/// `Down` makes the whole answer `down`; `Degraded` makes it `degraded`; no checks at all is `ok` (a static site is
/// healthy if it is serving).
pub fn body(checks: Vec<Check>) -> Body {
    let status = if checks.iter().any(|c| c.state == CheckState::Down) {
        "down"
    } else if checks.iter().any(|c| c.state == CheckState::Degraded) {
        "degraded"
    } else {
        "ok"
    };
    Body { status, version: version(), checks }
}

/// The status code that goes with the body.
///
/// `degraded` is a **200**: the process is serving, and a load balancer that removes a working instance because a replica
/// lags is a worse failure than the lag. `down` is a 503, because that is the only answer that should take the instance
/// out of rotation.
pub fn code(body: &Body) -> u16 {
    match body.status {
        "down" => 503,
        _ => 200,
    }
}

/// Convenience for the common case: no dependencies to check.
pub fn healthy() -> Body {
    body(Vec::new())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn no_checks_is_ok_and_a_200() {
        let b = healthy();
        assert_eq!(b.status, "ok");
        assert_eq!(code(&b), 200);
        assert!(b.checks.is_empty());
    }

    #[test]
    fn a_degraded_dependency_is_a_200_because_the_process_is_serving() {
        let b = body(vec![Check::ok("db"), Check::degraded("search", "replica lagging")]);
        assert_eq!(b.status, "degraded");
        assert_eq!(code(&b), 200, "removing a working instance over a lag is the worse failure");
    }

    #[test]
    fn a_down_dependency_is_a_503() {
        let b = body(vec![Check::ok("search"), Check::down("db", "connection refused")]);
        assert_eq!(b.status, "down");
        assert_eq!(code(&b), 503);
    }

    #[test]
    fn down_beats_degraded_regardless_of_order() {
        let a = body(vec![Check::down("db", "x"), Check::degraded("search", "y")]);
        let b = body(vec![Check::degraded("search", "y"), Check::down("db", "x")]);
        assert_eq!(a.status, "down");
        assert_eq!(b.status, "down");
    }

    #[test]
    fn the_body_always_carries_a_status_and_a_version() {
        let json = serde_json::to_value(healthy()).unwrap();
        assert!(json.get("status").is_some());
        assert!(json.get("version").is_some(), "a health answer that cannot say what is running lies during a rollback");
        assert_eq!(json["status"], "ok");
    }

    #[test]
    fn a_check_with_no_detail_omits_the_field_rather_than_sending_null() {
        let json = serde_json::to_value(body(vec![Check::ok("db")])).unwrap();
        assert!(json["checks"][0].get("detail").is_none());
    }
}
