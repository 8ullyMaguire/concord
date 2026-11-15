//! Quorum and sovereignty: how a governed act is decided, and who may skip the vote.
//!
//! Two kinds of statement are being kept apart on purpose. A **quorum** is a mandate: it says how many of the
//! people who could vote actually did, in favour. **Sovereignty** is a break-glass: the operator may act
//! without one. They are never the same answer — [`Mandate`] names which one produced a decision, so a ledger
//! entry saying "adopted" can be read back as either "700‰ of 42 eligible" or "the operator did it", and an
//! audit that cannot tell those apart is not an audit.
//!
//! The rule is data: a percentage, a basis and (at the call site) the eligible set. Nothing here knows what a
//! user is, what a ban means, or how votes were collected — `polaris-ledger` owns those and calls this.

use serde::{Deserialize, Serialize};

/// Whether the operator may act without a quorum on this instance.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Sovereignty {
    /// The operator may skip the vote; every such act is recorded as [`Mandate::Sovereignty`].
    Admin,
    /// User-driven only: the sovereignty path is refused, not silently ignored.
    UserDriven,
}

/// What the percentage is a percentage *of*.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Basis {
    /// Of everyone eligible to vote. Apathy does not lower the bar.
    Eligible,
    /// Of the votes actually cast. Faster, and easier to carry by a motivated minority.
    Cast,
}

/// The governed acts this estate has. A product adds one by naming it, not by inventing a threshold.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Act {
    /// Adopt or amend an instance rule.
    RuleAdopt,
    /// Ban an account from the instance.
    Ban,
    /// A time-out or lesser sanction.
    Sanction,
    /// Remove a moderator.
    ModRemove,
    /// Defederate from another instance.
    Defederate,
}

/// A threshold: a fraction of a denominator, and which denominator.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub struct QuorumRule {
    /// Parts per thousand. `700` is 70%.
    pub permille: u16,
    /// Eligible voters or votes cast.
    pub basis: Basis,
}

impl QuorumRule {
    pub fn new(permille: u16, basis: Basis) -> Self {
        Self {
            permille: permille.min(1000),
            basis,
        }
    }

    /// The estate's default for an act: 70% of eligible for the acts that change who may speak or what the
    /// rules are, 60% for the acts that undo a decision or a person's standing.
    pub fn default_for(act: Act) -> Self {
        match act {
            Act::RuleAdopt | Act::Ban | Act::Defederate => Self::new(700, Basis::Eligible),
            Act::Sanction => Self::new(600, Basis::Cast),
            Act::ModRemove => Self::new(600, Basis::Eligible),
        }
    }

    /// How many affirmative votes are needed out of `denominator`.
    ///
    /// Rounded **up**: 70% of 3 is 2.1, and 2 votes is not 70%. A threshold that rounds down lets a smaller
    /// share than the operator configured carry the act.
    pub fn needed(&self, denominator: u32) -> u32 {
        let scaled = u64::from(self.permille) * u64::from(denominator);
        ((scaled + 999) / 1000) as u32
    }
}

/// The votes, as the ledger counted them.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default, Serialize, Deserialize)]
pub struct Tally {
    /// How many subjects were eligible to vote (one person, one vote; the subject of the act excluded).
    pub eligible: u32,
    /// Affirmative votes.
    pub yes: u32,
    /// Negative votes.
    pub no: u32,
}

impl Tally {
    pub fn new(eligible: u32, yes: u32, no: u32) -> Self {
        Self { eligible, yes, no }
    }

    fn denominator(&self, basis: Basis) -> u32 {
        match basis {
            Basis::Eligible => self.eligible,
            Basis::Cast => self.yes.saturating_add(self.no),
        }
    }
}

/// What carried the decision.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Mandate {
    /// A quorum was met, with the numbers that met it.
    Quorum {
        basis: Basis,
        yes: u32,
        needed: u32,
        of: u32,
    },
    /// The operator acted without one. Recorded so a later reader can never mistake it for a mandate.
    Sovereignty,
}

/// The answer.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Decision {
    Adopted { via: Mandate },
    QuorumShort {
        basis: Basis,
        found: u32,
        needed: u32,
        of: u32,
    },
    /// The instance is user-driven and the caller tried to use the sovereignty path. A named refusal, because
    /// silently falling through to the quorum would look like the vote had been counted and lost.
    SovereigntyUnavailable,
}

impl Decision {
    pub fn is_adopted(&self) -> bool {
        matches!(self, Decision::Adopted { .. })
    }

    /// What happened, in a sentence a page can print.
    pub fn reason(&self) -> String {
        match self {
            Decision::Adopted { via: Mandate::Sovereignty } => {
                "adopted by the operator under sovereignty".to_string()
            }
            Decision::Adopted { via: Mandate::Quorum { yes, needed, of, .. } } => {
                format!("adopted: {yes} of {of} eligible votes, {needed} needed")
            }
            Decision::QuorumShort { found, needed, of, .. } => {
                format!("not adopted: {found} of {of} votes, {needed} needed")
            }
            Decision::SovereigntyUnavailable => {
                "this instance is user-driven: the act needs a quorum".to_string()
            }
        }
    }
}

/// Decide a governed act.
///
/// `actor_is_operator` is the caller's answer to "is this the break-glass account"; this function does not
/// authenticate and cannot. Sovereignty is consulted **only** when the instance allows it, and never when a
/// quorum was already met — the mandate is the better record.
pub fn decide(
    rule: &QuorumRule,
    tally: &Tally,
    sovereignty: Sovereignty,
    actor_is_operator: bool,
    actor_invokes_sovereignty: bool,
) -> Decision {
    if actor_is_operator && actor_invokes_sovereignty {
        return match sovereignty {
            Sovereignty::Admin => Decision::Adopted {
                via: Mandate::Sovereignty,
            },
            Sovereignty::UserDriven => Decision::SovereigntyUnavailable,
        };
    }

    let of = tally.denominator(rule.basis);
    let needed = rule.needed(of);
    // An empty electorate cannot carry an act. Without this, `needed` is 0 and a ban passes on zero votes —
    // which is not a quorum of nobody, it is a bug.
    if of == 0 || tally.yes < needed {
        return Decision::QuorumShort {
            basis: rule.basis,
            found: tally.yes,
            needed: needed.max(1),
            of,
        };
    }
    Decision::Adopted {
        via: Mandate::Quorum {
            basis: rule.basis,
            yes: tally.yes,
            needed,
            of,
        },
    }
}
