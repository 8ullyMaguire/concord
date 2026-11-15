//! How many requests may this client make, and when may it try again.
//!
//! Six products answer that question, and the two good answers disagree: darkhold's quotas are configurable
//! per class and suki's client identity trusts `X-Forwarded-For` only from a loopback peer. Neither is a
//! preference — a hard-coded budget is a deployment you cannot tune, and a spoofable header is a memory leak
//! with a nicer name. This crate is both, with the clock injected so the tests are deterministic.
//!
//! Not here, on purpose: a distributed limiter (one host, one process per product — a Redis bucket can be
//! down while the site is up), and a nginx replacement (the edge must refuse before a connection reaches the
//! app; this is the per-class budget the edge cannot express).

pub mod client;
pub mod limits;
pub mod limiter;
pub mod quota;

pub use client::client_key;
pub use limits::Limits;
pub use limiter::{Decision, Limiter};
pub use quota::Quota;
