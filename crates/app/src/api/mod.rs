//! The `concord-app` resource modules (step 3, ADR 0014).
//! Each module owns its handlers and its `routes()`; `crate::router` merges them.

pub mod board;
pub mod meta;
pub mod records;
pub mod notifications;
