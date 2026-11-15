//! The answer, and why. `Denial` is an enum because a 403 that says "scope" and a 403 that says "not
//! declared" are different bugs: the first is a user's privilege and the second is a developer's typo.

use crate::scope::Scope;

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Verdict {
    Allow { via: Via },
    Deny { why: Denial },
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Via {
    Override,
    Role(String),
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Denial {
    NotDeclared,
    NoGrant,
    ExplicitDeny { reason: String },
    RequirementUnmet { what: String },
    ScopeTooNarrow { needed: Scope, held: Scope },
    Expired,
}

impl Verdict {
    pub fn is_allowed(&self) -> bool {
        matches!(self, Verdict::Allow { .. })
    }

    /// The sentence a client sees. Never "forbidden": a denial that does not say what was missing makes a
    /// permission bug indistinguishable from a policy decision.
    pub fn reason(&self) -> String {
        match self {
            Verdict::Allow { .. } => "allowed".to_string(),
            Verdict::Deny { why } => match why {
                Denial::NotDeclared => "this capability is not declared on this instance".into(),
                Denial::NoGrant => "no role you hold grants this".into(),
                Denial::ExplicitDeny { reason } => format!("explicitly denied: {reason}"),
                Denial::RequirementUnmet { what } => format!("not yet: {what}"),
                Denial::ScopeTooNarrow { needed, held } => format!(
                    "your grant reaches {} but this needs {}",
                    held.as_str(),
                    needed.as_str()
                ),
                Denial::Expired => "the grant for this has expired".into(),
            },
        }
    }
}
