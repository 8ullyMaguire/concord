//! The `concord-app` resource modules (step 3, ADR 0014).
//! Each module owns its handlers and its `routes()`; `crate::router` merges them.

pub mod board;
pub mod meta;
pub mod records;
pub mod notifications;
pub mod invites;
pub mod badges;
pub mod solutions;
pub mod arena_votes;
pub mod coverage;
pub mod feature_votes;
pub mod merge_requests;
pub mod documents;
pub mod alternatives;
pub mod ops_reads;
pub mod identity;
pub mod contributions;
pub mod field_reports;
pub mod emergency_holds;
pub mod taxonomy;
pub mod ops_writes;
pub mod forgejo_webhook;
