//! The classes a product declares, and the budget each one draws from.

use std::collections::BTreeMap;
use std::fmt;

use crate::quota::Quota;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Limits {
    default: Quota,
    classes: BTreeMap<String, Quota>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct LimitsError {
    pub why: String,
}

impl fmt::Display for LimitsError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.why)
    }
}

impl std::error::Error for LimitsError {}

impl Limits {
    pub fn new(default: Quota) -> Self {
        Self {
            default,
            classes: BTreeMap::new(),
        }
    }

    /// Declare a class. A class is a name a route chooses, not a branch in the limiter.
    pub fn class(mut self, name: &str, quota: Quota) -> Self {
        self.classes.insert(name.to_string(), quota);
        self
    }

    pub fn quota(&self, class: &str) -> Quota {
        self.classes.get(class).copied().unwrap_or(self.default)
    }

    /// `default` first, then the declared names, so a page can print the whole table.
    pub fn names(&self) -> impl Iterator<Item = &str> {
        std::iter::once("default").chain(self.classes.keys().map(String::as_str))
    }

    /// A quota of zero is legal on a **declared class** — it is how an operator locks one class out — but zero on
    /// the default means every route nobody classified is dead, which is a mistake rather than a decision.
    pub fn validate(&self) -> Result<(), LimitsError> {
        if self.default.permits == 0 {
            return Err(LimitsError {
                why: "the default quota permits zero requests: a class nobody declared would be a total denial"
                    .to_string(),
            });
        }
        Ok(())
    }
}
