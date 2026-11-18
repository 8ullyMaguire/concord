//! The checked-in API contract. Types live with their handlers; this module only assembles them.

use utoipa::OpenApi;

use crate::api::meta::{HealthResponse, VersionResponse};
use crate::api::milestones::{RoadmapResponse, RoadmapTotals};

#[derive(OpenApi)]
#[openapi(components(schemas(
    HealthResponse,
    VersionResponse,
    RoadmapResponse,
    RoadmapTotals,
    concord_db::consensus::Call,
    concord_db::curation::Item,
    concord_db::curation::Page,
    concord_db::notifications::Inbox,
    concord_db::notifications::Notification,
    concord_db::milestones::Milestone,
    concord_db::milestones::MilestoneSlippage,
    concord_db::stats::Census,
    concord_db::stats::FacetCount,
    concord_db::stats::Latency,
    concord_db::stats::LatencySection,
    concord_db::stats::Measured,
    concord_db::stats::ProvenanceCount,
    concord_db::stats::Snapshot,
    concord_db::stats::StatsPayload,
    concord_db::stats::ResponsivenessRow,
)))]
pub struct ApiDoc;

#[cfg(test)]
mod tests {
    use super::ApiDoc;
    use utoipa::OpenApi;

    #[test]
    fn emits_a_parseable_document() {
        let json = ApiDoc::openapi().to_json().expect("OpenAPI document serialises");
        let doc: serde_json::Value = serde_json::from_str(&json).expect("OpenAPI document is JSON");
        assert!(doc["components"]["schemas"]["HealthResponse"].is_object());
        assert!(doc["components"]["schemas"]["VersionResponse"].is_object());
        if let Ok(path) = std::env::var("CONCORD_OPENAPI_OUT") {
            // Cargo runs unit tests with the crate directory as cwd, so a repo-relative output path needs its parent
            // created here rather than by the Makefile.
            if let Some(parent) = std::path::Path::new(&path).parent() {
                std::fs::create_dir_all(parent).expect("create the contract directory");
            }
            std::fs::write(path, json).expect("write the generated contract");
        }
    }
}
