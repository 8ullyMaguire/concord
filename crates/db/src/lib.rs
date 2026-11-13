//! PostgreSQL access for the ported service: one module per slice, one pool.
//!
//! The store functions here are the port of `internal/store/*.go`, and the Go signatures are the
//! contract — a Rust function that returns a different *shape* than the one it replaces is a parity
//! failure even when its SQL is better.

use sqlx::postgres::{PgPool, PgPoolOptions};

pub mod auth;
pub mod arena;
pub mod audit;
pub mod call_outcome;
pub mod charter;
pub mod complaints;
pub mod comments;
pub mod consensus;
pub mod consensus_writes;
pub mod curation;
pub mod feature_writes;
pub mod features;
pub mod holds;
pub mod milestones;
pub mod notifications;
pub mod override_window;
pub mod priorities;
pub mod projects;

/// Build the pool the handlers share.
///
/// # Errors
/// Whatever `sqlx` reports: a malformed URL, an unreachable host, bad credentials. The caller
/// decides whether that is fatal, which for a service it is.
pub async fn pool(url: &str) -> Result<PgPool, sqlx::Error> {
    PgPoolOptions::new().max_connections(16).connect(url).await
}
