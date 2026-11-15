//! Who is asking. Built by the product from its session or token; this crate never reads one.

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Subject {
    pub id: String,
    pub roles: Vec<String>,
    pub trust: u8,
    pub reputation: i64,
    pub account_age_days: u32,
    pub curator: bool,
    /// A level derived from evidence (vouches, invite depth, verified contact), never a stored badge. `0` is
    /// anonymous; see the estate's governance note for the ladder.
    pub humanity: u8,
}

impl Subject {
    /// A subject with no roles beyond `everyone`, which every subject has by definition.
    pub fn new(id: impl Into<String>, trust: u8) -> Self {
        Self {
            id: id.into(),
            roles: vec!["everyone".to_string()],
            trust,
            reputation: 0,
            account_age_days: 0,
            curator: false,
            humanity: 0,
        }
    }

    pub fn with_role(mut self, role: impl Into<String>) -> Self {
        let role = role.into();
        if !self.roles.iter().any(|r| r == &role) {
            self.roles.push(role);
        }
        self
    }

    pub fn with_reputation(mut self, reputation: i64) -> Self {
        self.reputation = reputation;
        self
    }

    pub fn with_account_age(mut self, days: u32) -> Self {
        self.account_age_days = days;
        self
    }

    pub fn with_curator(mut self, curator: bool) -> Self {
        self.curator = curator;
        self
    }

    pub fn with_humanity(mut self, humanity: u8) -> Self {
        self.humanity = humanity;
        self
    }

    pub fn has_role(&self, role: &str) -> bool {
        role == "everyone" || self.roles.iter().any(|r| r == role)
    }
}
