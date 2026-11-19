//! Concord's product rules, as pure functions. No database, no HTTP, no I/O.
//!
//! The store remains the adapter: it reads rows and calls these rules. A rule that spans aggregates belongs here,
//! where it can be enumerated exhaustively instead of inferred from a query's shape.

pub use concord_ranking::lifecycle::{stage, Signals, Verdict};

use concord_governance::ConsensusCounts;

/// Why a project may not be adopted. The strings live with the rule so the API and the tests cannot invent different
/// reasons for the same refusal.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ClaimRefusal {
    Guest,
    Archived(String),
    Unknown(String),
    NotDormant(String, String),
}

impl ClaimRefusal {
    pub fn message(&self) -> String {
        match self {
            ClaimRefusal::Guest => "a guest cannot adopt: the record must name somebody".to_string(),
            ClaimRefusal::Archived(slug) => {
                format!("{slug} was declared archived by its maintainer")
            }
            ClaimRefusal::Unknown(slug) => format!(
                "{slug} names no repository this instance can read, so its silence has not been measured — it cannot be offered for adoption on the strength of a number nobody took"
            ),
            ClaimRefusal::NotDormant(slug, stage) => {
                format!("{slug} is {stage}, not dormant")
            }
        }
    }
}

/// The adoption rule: only a non-guest may claim a project the lifecycle has measured dormant.
///
/// The reason sentence is returned separately by the caller when it builds the refusal, because the store owns that
/// sentence and the domain owns the decision.
pub fn claim_eligible(is_guest: bool, stage: &str) -> Result<(), ClaimRefusal> {
    if is_guest {
        return Err(ClaimRefusal::Guest);
    }
    match stage {
        "dormant" => Ok(()),
        "archived" => Err(ClaimRefusal::Archived(String::new())),
        "unknown" => Err(ClaimRefusal::Unknown(String::new())),
        other => Err(ClaimRefusal::NotDormant(other.to_string(), String::new())),
    }
}

/// Cast positions become counts. One row is one participant even when its stance is unrecognised; only known stances
/// increment a stance counter. Objections and the eligible population are measurements supplied by the store.
#[must_use]
pub fn tally_counts(positions: &[&str], open_objections: i64, eligible: i64) -> ConsensusCounts {
    let mut counts = ConsensusCounts::default();
    for position in positions {
        counts.participants += 1;
        match *position {
            "consent" => counts.consent += 1,
            "stand_aside" => counts.stand_aside += 1,
            "block" => counts.block += 1,
            "abstain" => counts.abstain += 1,
            _ => {}
        }
    }
    counts.open_objections = open_objections;
    counts.eligible = eligible;
    counts
}

#[cfg(test)]
mod tests {
    use super::*;
    use concord_ranking::lifecycle::Stage;

    #[test]
    fn claim_eligibility_is_exhaustive_over_its_small_domain() {
        let stages = ["dormant", "archived", "unknown", "mature", "seed"];
        for is_guest in [false, true] {
            for name in stages {
                let result = claim_eligible(is_guest, name);
                if is_guest {
                    assert_eq!(result, Err(ClaimRefusal::Guest), "guest beats {name}");
                } else if name == "dormant" {
                    assert_eq!(result, Ok(()));
                } else {
                    assert!(result.is_err(), "{name} must not be adoptable");
                }
            }
        }
    }

    #[test]
    fn every_stance_and_an_unknown_one_are_counted_once() {
        let counts = tally_counts(&["consent", "consent", "abstain", "stand_aside", "block", "odd"], 2, 9);
        assert_eq!(counts.participants, 6);
        assert_eq!(counts.consent, 2);
        assert_eq!(counts.abstain, 1);
        assert_eq!(counts.stand_aside, 1);
        assert_eq!(counts.block, 1);
        assert_eq!(counts.open_objections, 2);
        assert_eq!(counts.eligible, 9);
    }

    #[test]
    fn stage_inference_keeps_the_act_first_and_the_unmeasured_honest() {
        let base = Signals {
            has_repository: true,
            archived: None,
            last_commit_age_days: None,
            health_score: None,
            features_shipped: 0,
            age_days: 10.0,
        };
        assert_eq!(stage(Signals { archived: Some("done upstream"), ..base }).stage, Stage::Archived);
        assert_eq!(stage(Signals { last_commit_age_days: Some(120.0), ..base }).stage, Stage::Dormant);
        assert_eq!(stage(base).stage, Stage::Unknown);
    }
}
