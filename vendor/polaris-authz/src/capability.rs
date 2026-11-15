//! A capability is `resource.action`, and nothing else. The two-segment rule is enforced at parse time
//! because the alternative — free-form strings — is how a product ends up with `image.trash`, `image_trash`
//! and `trashImage` all meaning the same thing, checked in three places, agreeing in one.

use serde::{Deserialize, Serialize};
use std::fmt;

#[derive(Debug, Clone, PartialEq, Eq, Hash, PartialOrd, Ord, Serialize, Deserialize)]
pub struct Capability {
    resource: String,
    action: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CapabilityError {
    pub raw: String,
    pub why: &'static str,
}

impl fmt::Display for CapabilityError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{:?} is not a capability: {}", self.raw, self.why)
    }
}

impl std::error::Error for CapabilityError {}

fn valid_segment(part: &str) -> bool {
    part.chars()
        .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '_' || c == '-')
}

impl Capability {
    pub fn parse(raw: &str) -> Result<Self, CapabilityError> {
        let segments: Vec<&str> = raw.split('.').collect();
        // An empty segment is reported before the segment count, because `image..trash` is a typo with a
        // different fix from `image` — one word lost, one word missing.
        if segments.iter().any(|s| s.is_empty()) {
            return Err(CapabilityError {
                raw: raw.to_string(),
                why: "an empty segment is not a name",
            });
        }
        if segments.len() != 2 {
            return Err(CapabilityError {
                raw: raw.to_string(),
                why: "a capability is resource.action, e.g. `image.trash`",
            });
        }
        for part in &segments {
            if !valid_segment(part) {
                return Err(CapabilityError {
                    raw: raw.to_string(),
                    why: "segments are lowercase ascii, digits, `_` or `-`",
                });
            }
        }
        Ok(Self {
            resource: segments[0].to_string(),
            action: segments[1].to_string(),
        })
    }

    pub fn resource(&self) -> &str {
        &self.resource
    }
    pub fn action(&self) -> &str {
        &self.action
    }
}

impl fmt::Display for Capability {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}.{}", self.resource, self.action)
    }
}
