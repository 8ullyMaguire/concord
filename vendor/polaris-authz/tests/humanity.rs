//! Humanity is a derived level, not a badge: the requirement is evaluated like every other, and a subject whose
//! evidence is thin is refused with the level named.

use polaris_authz::*;

fn cap(s: &str) -> Capability {
    Capability::parse(s).unwrap()
}

#[test]
fn posting_can_require_a_humanity_level_and_says_which() {
    let mut policy = Policy::new();
    policy.declare(cap("post.create"));
    policy.grant(
        Grant::role("member", cap("post.create"), Scope::Own)
            .requiring(Requirement::HumanityAtLeast(2)),
    );

    let vouched = Subject::new("s1", 0).with_role("member").with_humanity(2);
    assert!(authorize(&policy, &vouched, cap("post.create"), Scope::Own, 0).is_allowed());

    let bare = Subject::new("s2", 0).with_role("member").with_humanity(1);
    let verdict = authorize(&policy, &bare, cap("post.create"), Scope::Own, 0);
    match verdict {
        Verdict::Deny { why: Denial::RequirementUnmet { what } } => {
            assert_eq!(what, "humanity >= 2");
        }
        other => panic!("{other:?}"),
    }
}

#[test]
fn a_humanity_level_that_falls_takes_the_permission_with_it() {
    let mut policy = Policy::new();
    policy.declare(cap("post.create"));
    policy.grant(
        Grant::role("member", cap("post.create"), Scope::Own)
            .requiring(Requirement::HumanityAtLeast(2)),
    );

    // The same subject, after a vouch was revoked: the level is recomputed, the grant is not rewritten.
    let before = Subject::new("s1", 0).with_role("member").with_humanity(2);
    let after = Subject::new("s1", 0).with_role("member").with_humanity(1);
    assert!(authorize(&policy, &before, cap("post.create"), Scope::Own, 0).is_allowed());
    assert!(!authorize(&policy, &after, cap("post.create"), Scope::Own, 0).is_allowed());
}

#[test]
fn humanity_survives_serde_so_a_policy_row_can_carry_it() {
    let requirement = Requirement::HumanityAtLeast(3);
    let json = serde_json::to_string(&requirement).unwrap();
    assert!(json.contains("humanity_at_least"), "{json}");
    let back: Requirement = serde_json::from_str(&json).unwrap();
    assert_eq!(back, requirement);
}
