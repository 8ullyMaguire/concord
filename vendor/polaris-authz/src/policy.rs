//! Roles, grants, overrides — the policy a product loads from its own tables.

use crate::capability::Capability;
use crate::scope::Scope;
use serde::{Deserialize, Serialize};
use std::collections::BTreeSet;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Effect {
    Allow,
    Deny,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Requirement {
    TrustAtLeast(u8),
    ReputationAtLeast(i64),
    AccountAgeAtLeastDays(u32),
    MustBeCurator,
    /// A humanity level derived from evidence — vouches, invite depth, verified contact — never a badge set by
    /// hand. A level the subject's graph can lose, which a boolean cannot.
    HumanityAtLeast(u8),
}

impl Requirement {
    pub fn describe(&self) -> String {
        match self {
            Requirement::TrustAtLeast(n) => format!("trust >= {n}"),
            Requirement::ReputationAtLeast(n) => format!("reputation >= {n}"),
            Requirement::AccountAgeAtLeastDays(d) => format!("account_age >= {d} days"),
            Requirement::MustBeCurator => "must be a curator".to_string(),
            Requirement::HumanityAtLeast(n) => format!("humanity >= {n}"),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Grant {
    pub role: String,
    pub capability: Capability,
    pub scope: Scope,
    #[serde(default)]
    pub requires: Vec<Requirement>,
    /// Unix seconds. `None` is permanent — the default, because a grant that expires by accident is a
    /// permission nobody can debug.
    #[serde(default)]
    pub expires_unix: Option<i64>,
}

impl Grant {
    pub fn role(role: impl Into<String>, capability: Capability, scope: Scope) -> Self {
        Self {
            role: role.into(),
            capability,
            scope,
            requires: Vec::new(),
            expires_unix: None,
        }
    }

    pub fn requiring(mut self, r: Requirement) -> Self {
        self.requires.push(r);
        self
    }

    pub fn expiring(mut self, unix: i64) -> Self {
        self.expires_unix = Some(unix);
        self
    }
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Override {
    pub subject: String,
    pub capability: Capability,
    pub effect: Effect,
    pub reason: String,
    #[serde(default)]
    pub expires_unix: Option<i64>,
}

#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct Policy {
    pub capabilities: BTreeSet<Capability>,
    pub grants: Vec<Grant>,
    pub overrides: Vec<Override>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PolicyError {
    pub why: String,
}

impl std::fmt::Display for PolicyError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.why)
    }
}

impl std::error::Error for PolicyError {}

impl Policy {
    pub fn new() -> Self {
        Self::default()
    }

    /// Declaring a capability is what makes it answerable. A policy that grants something it never declared
    /// is a policy with a typo, and the typo is caught here rather than by a user who cannot press a button.
    pub fn declare(&mut self, capability: Capability) {
        self.capabilities.insert(capability);
    }

    pub fn grant(&mut self, grant: Grant) {
        debug_assert!(
            self.capabilities.contains(&grant.capability),
            "granting {} before declaring it",
            grant.capability
        );
        self.grants.push(grant);
    }

    pub fn override_for(
        &mut self,
        subject: &str,
        capability: Capability,
        effect: Effect,
        reason: &str,
    ) -> Result<(), PolicyError> {
        if !self.capabilities.contains(&capability) {
            return Err(PolicyError {
                why: format!("`{capability}` is not declared, so no override can refer to it"),
            });
        }
        if reason.trim().len() < 4 {
            return Err(PolicyError {
                why: "an override needs a reason; `because` is not one".to_string(),
            });
        }
        self.overrides.push(Override {
            subject: subject.to_string(),
            capability,
            effect,
            reason: reason.to_string(),
            expires_unix: None,
        });
        Ok(())
    }
}
