//! The one function. Deny-overrides, expiry-aware, and pure.

use crate::capability::Capability;
use crate::policy::{Effect, Policy, Requirement};
use crate::scope::Scope;
use crate::subject::Subject;
use crate::verdict::{Denial, Verdict, Via};

pub fn authorize(
    policy: &Policy,
    subject: &Subject,
    capability: Capability,
    needed: Scope,
    now_unix: i64,
) -> Verdict {
    if !policy.capabilities.contains(&capability) {
        return Verdict::Deny {
            why: Denial::NotDeclared,
        };
    }

    // 1. An override for this subject is the most specific statement there is, in either direction. Deny
    //    wins over allow when both are live: an override that was meant to stop something must not be
    //    outvoted by a second override that was meant to start something else.
    let mut allowed_override = false;
    let mut denied: Option<String> = None;
    for o in &policy.overrides {
        if o.subject == subject.id
            && o.capability == capability
            && o.expires_unix.map_or(true, |t| t > now_unix)
        {
            match o.effect {
                Effect::Deny => {
                    if denied.as_ref().map_or(true, |d| o.reason.len() > d.len()) {
                        denied = Some(o.reason.clone());
                    }
                }
                Effect::Allow => allowed_override = true,
            }
        }
    }
    if let Some(reason) = denied {
        return Verdict::Deny {
            why: Denial::ExplicitDeny { reason },
        };
    }
    if allowed_override {
        return Verdict::Allow {
            via: Via::Override,
        };
    }

    // 2. Any role grant that reaches far enough, whose requirements hold, and which has not expired.
    let mut best: Option<&crate::policy::Grant> = None;
    let mut narrowest: Option<Scope> = None;
    let mut unmet: Option<String> = None;
    let mut expired = false;

    for g in &policy.grants {
        if g.capability != capability || !subject.has_role(&g.role) {
            continue;
        }
        if let Some(t) = g.expires_unix {
            if t <= now_unix {
                expired = true;
                continue;
            }
        }
        if let Some(r) = g.requires.iter().find(|r| !satisfied(subject, r)) {
            unmet = Some(r.describe());
            continue;
        }
        if !g.scope.covers(needed) {
            narrowest = Some(narrowest.map_or(g.scope, |n: Scope| n.max(g.scope)));
            continue;
        }
        if best.map_or(true, |b| g.scope < b.scope) {
            best = Some(g);
        }
    }

    if let Some(g) = best {
        return Verdict::Allow {
            via: Via::Role(g.role.clone()),
        };
    }
    if let Some(held) = narrowest {
        return Verdict::Deny {
            why: Denial::ScopeTooNarrow { needed, held },
        };
    }
    if let Some(what) = unmet {
        return Verdict::Deny {
            why: Denial::RequirementUnmet { what },
        };
    }
    if expired {
        return Verdict::Deny {
            why: Denial::Expired,
        };
    }
    Verdict::Deny {
        why: Denial::NoGrant,
    }
}

fn satisfied(subject: &Subject, requirement: &Requirement) -> bool {
    match requirement {
        Requirement::TrustAtLeast(n) => subject.trust >= *n,
        Requirement::ReputationAtLeast(n) => subject.reputation >= *n,
        Requirement::AccountAgeAtLeastDays(d) => subject.account_age_days >= *d,
        Requirement::MustBeCurator => subject.curator,
        Requirement::HumanityAtLeast(n) => subject.humanity >= *n,
    }
}
