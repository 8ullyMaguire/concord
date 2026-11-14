//! PostgreSQL access for the ported service: one module per slice, one pool.
//!
//! The store functions here are the port of `internal/store/*.go`, and the Go signatures are the
//! contract — a Rust function that returns a different *shape* than the one it replaces is a parity
//! failure even when its SQL is better.

use sqlx::postgres::{PgPool, PgPoolOptions};

pub mod auth;
pub mod arena;
pub mod arena_writes;
pub mod coverage_writes;
pub mod solution_writes;
pub mod audit;
pub mod call_outcome;
pub mod charter;
pub mod complaints;
pub mod comments;
pub mod consensus;
pub mod consensus_writes;
pub mod curation;
pub mod feature_writes;
pub mod embeddings;
pub mod feature_work;
pub mod features;
pub mod holds;
pub mod invite_writes;
pub mod list_writes;
pub mod milestones;
pub mod moderation;
pub mod notifications;
pub mod proposals;
pub mod snapshot;
pub mod search;
pub mod capabilities;
pub mod field_reports;
pub mod criteria;
/// §4.10's criteria writes.
pub mod criteria_writes;
pub mod deps;
pub mod members;
pub mod merge_writes;
pub mod panels;
pub mod metrics;
pub mod stats;
pub mod themes;
pub mod strategy;
pub mod tallies;
pub mod votes_writes;
pub mod voteweight;
pub mod membership;
pub mod taxonomy;
pub mod override_window;
pub mod priorities;
pub mod projects;
pub mod requests;

/// Build the pool the handlers share.
///
/// # Errors
/// Whatever `sqlx` reports: a malformed URL, an unreachable host, bad credentials. The caller
/// decides whether that is fatal, which for a service it is.
pub async fn pool(url: &str) -> Result<PgPool, sqlx::Error> {
    PgPoolOptions::new().max_connections(16).connect(url).await
}

/// Unix seconds, from the system clock.
///
/// One definition for the whole crate: a timestamp written by a job and a timestamp written by the server must
/// be the same kind of number, or the series has two clocks in it.
#[must_use]
pub fn now_unix() -> f64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|elapsed| elapsed.as_secs_f64())
        .unwrap_or(0.0)
}
