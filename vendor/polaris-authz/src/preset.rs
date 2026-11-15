//! The five models this estate already has, written as presets. An operator picks one and gets a working
//! policy with zero configuration; a product that wants suki's exact behaviour picks `Staff` and passes the
//! moderator's capability subset, because the two roles were never the same set.

use crate::capability::Capability;
use crate::policy::{Grant, Policy, Requirement};
use crate::scope::Scope;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Preset {
    Simple,
    Staff,
    TrustLadder,
    ReputationEarned,
    FineGrained,
    Open,
}

/// Every capability, granted to `role` at `scope`.
fn all_to(policy: &mut Policy, caps: &[Capability], role: &str, scope: Scope) {
    for c in caps {
        policy.grant(Grant::role(role, c.clone(), scope));
    }
}

/// Build a policy for a preset.
///
/// `caps` is the product's whole declared vocabulary, `public` the subset an ordinary signed-in subject may
/// exercise on their own objects, and `staff` the moderator subset of `caps`. `staff` is read only by
/// [`Preset::Staff`]; every other preset ignores it, so a product that has no moderators passes `&[]`.
pub fn build(preset: Preset, caps: &[Capability], public: &[Capability], staff: &[Capability]) -> Policy {
    let mut policy = Policy::new();
    for c in caps {
        policy.declare(c.clone());
    }

    match preset {
        // suki's shape: an admin who may do everything, a moderator with a named subset, and members who
        // may act on their own objects.
        Preset::Staff => {
            all_to(&mut policy, caps, "admin", Scope::Site);
            all_to(&mut policy, staff, "moderator", Scope::Site);
            for c in public {
                policy.grant(Grant::role("everyone", c.clone(), Scope::Own));
            }
        }
        // A site with no staff: one administrator, everyone else a member.
        Preset::Simple => {
            all_to(&mut policy, caps, "admin", Scope::Site);
            for c in public {
                policy.grant(Grant::role("everyone", c.clone(), Scope::Own));
            }
        }
        // The estate's ladder: nothing is granted by role, everything by trust.
        Preset::TrustLadder => {
            for (threshold, scope) in [
                (1u8, Scope::Own),
                (3, Scope::Curated),
                (5, Scope::Site),
                (6, Scope::Estate),
            ] {
                for c in caps {
                    policy.grant(
                        Grant::role("everyone", c.clone(), scope)
                            .requiring(Requirement::TrustAtLeast(threshold)),
                    );
                }
            }
        }
        // blackrep's `is_admin = reputation >= 5000`, made explicit and given two rungs below it.
        Preset::ReputationEarned => {
            for (rep, scope) in [(500i64, Scope::Own), (2000, Scope::Curated), (5000, Scope::Site)] {
                for c in caps {
                    policy.grant(
                        Grant::role("everyone", c.clone(), scope)
                            .requiring(Requirement::ReputationAtLeast(rep)),
                    );
                }
            }
        }
        // Nothing granted by role beyond the public set: every staff ability is an override, which is what
        // "fine-grained per user" means and why it needs no table of its own.
        Preset::FineGrained => {
            for c in public {
                policy.grant(Grant::role("everyone", c.clone(), Scope::Own));
            }
        }
        // Everything for everyone: an archive, a personal site, a demo. Named so that "no authorization"
        // is a decision somebody made, not a path somebody forgot to guard.
        Preset::Open => all_to(&mut policy, caps, "everyone", Scope::Estate),
    }
    policy
}
