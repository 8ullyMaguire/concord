//! Who may do what, and why not.
//!
//! Every product in this estate answered that question its own way: suki with a role→capability table,
//! darkhold with a computed curator flag, blackrep with a reputation threshold, stash-box with one role
//! string per user, and the estate itself with a trust ladder that has no object in sight. This crate
//! expresses all of them as one shape — a grant of a capability to a role, bounded by a scope and a
//! requirement — and answers with a verdict that names its reason.
//!
//! Two properties are load-bearing:
//!
//! * **Pure.** `authorize` takes `now` as an argument and touches nothing. No clock, no database, no
//!   environment. A permission system that cannot be tested deterministically is one nobody tests.
//! * **Deny-overrides.** An explicit deny beats every allow, at every scope, always. The alternative — "the
//!   most specific grant wins" — is how a temporary ban gets outvoted by a role someone forgot they had.

pub mod capability;
pub mod eval;
pub mod governance;
pub mod policy;
pub mod preset;
pub mod scope;
pub mod subject;
pub mod verdict;

pub use capability::Capability;
pub use eval::authorize;
pub use governance::{decide, Act, Basis, Decision, Mandate, QuorumRule, Sovereignty, Tally};
pub use policy::{Effect, Grant, Override, Policy, Requirement};
pub use preset::{build, Preset};
pub use scope::Scope;
pub use subject::Subject;
pub use verdict::{Denial, Verdict, Via};
