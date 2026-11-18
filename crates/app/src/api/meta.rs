//! Health and build identity — the first pure module move of step 3 (ADR 0014).
//!
//! These two handlers were lifted verbatim from `lib.rs`; only their visibility and
//! the `routes()` assembly are new. No wire shape, status or log line changed.

use axum::routing::get;
use axum::{Json, Router};

use crate::AppState;
use serde::Serialize;
use utoipa::ToSchema;

#[derive(Debug, Serialize, ToSchema)]
pub struct HealthResponse {
    pub status: String,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct VersionResponse {
    pub version: String,
}

/// `/healthz` answers `{"status":"ok"}` — the shape the deployed service answers, not a phrase.
async fn healthz() -> Json<HealthResponse> {
    Json(HealthResponse { status: "ok".to_owned() })
}

/// The build string. During the port it names what answers, so a probe can tell the Rust service
/// from the Go one without reading a process list.
/// The build this process IS, in the one string the operator and the cutover gate both read.
///
/// `0.0.0` is what every crate in the workspace says, so it identifies nothing: after a cutover `{"version":"concord-rs 0.0.0"}` cannot
/// tell the reader which commit is answering, which is the only question `/api/v1/version` exists to answer. `CONCORD_VERSION` is baked at
/// compile time (see the release command in `docs/plans/port-to-profile.md`, part 77) and carries the git sha; the fallback is honest about
/// being a build that was not stamped.
const VERSION: &str = match option_env!("CONCORD_VERSION") {
    Some(stamped) => stamped,
    None => concat!("concord-rs ", env!("CARGO_PKG_VERSION"), " (unstamped build)"),
};

async fn version() -> Json<VersionResponse> {
    Json(VersionResponse { version: VERSION.to_owned() })
}


/// The routes this module owns. `router()` merges them into the one app router.
pub fn routes() -> Router<AppState> {
    Router::new()
        .route("/healthz", get(healthz))
        .route("/api/v1/healthz", get(healthz))
        .route("/api/v1/version", get(version))
}
