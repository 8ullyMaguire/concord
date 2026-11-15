use polaris_authz::*;

fn cap(s: &str) -> Capability {
    Capability::parse(s).unwrap()
}

#[test]
fn a_capability_nobody_declared_is_a_named_denial() {
    let policy = Policy::new();
    let v = authorize(&policy, &Subject::new("s1", 0), cap("image.trash"), Scope::Site, 1000);
    assert!(matches!(v, Verdict::Deny { why: Denial::NotDeclared }));
}

#[test]
fn a_role_grant_allows_and_says_which_role_did_it() {
    let mut policy = Policy::new();
    policy.declare(cap("image.trash"));
    policy.grant(Grant::role("moderator", cap("image.trash"), Scope::Site));
    let subj = Subject::new("s1", 0).with_role("moderator");
    match authorize(&policy, &subj, cap("image.trash"), Scope::Site, 1000) {
        Verdict::Allow { via: Via::Role(r) } => assert_eq!(r, "moderator"),
        other => panic!("expected allow by role, got {other:?}"),
    }
}

#[test]
fn a_narrower_grant_does_not_cover_a_wider_call() {
    let mut policy = Policy::new();
    policy.declare(cap("work.edit"));
    policy.grant(Grant::role("member", cap("work.edit"), Scope::Own));
    let subj = Subject::new("s1", 0).with_role("member");
    let v = authorize(&policy, &subj, cap("work.edit"), Scope::Site, 1000);
    assert!(matches!(v, Verdict::Deny { why: Denial::ScopeTooNarrow { .. } }));
}

#[test]
fn a_requirement_that_is_not_met_names_itself() {
    let mut policy = Policy::new();
    policy.declare(cap("report.decide"));
    policy.grant(
        Grant::role("moderator", cap("report.decide"), Scope::Site)
            .requiring(Requirement::TrustAtLeast(3)),
    );
    let v = authorize(
        &policy,
        &Subject::new("s1", 1).with_role("moderator"),
        cap("report.decide"),
        Scope::Site,
        1000,
    );
    match v {
        Verdict::Deny { why: Denial::RequirementUnmet { what } } => {
            assert!(what.contains("trust"), "{what}")
        }
        other => panic!("{other:?}"),
    }
}

#[test]
fn an_explicit_deny_beats_every_grant() {
    let mut policy = Policy::new();
    policy.declare(cap("image.trash"));
    policy.grant(Grant::role("admin", cap("image.trash"), Scope::Estate));
    policy
        .override_for("s1", cap("image.trash"), Effect::Deny, "banned for abuse")
        .unwrap();
    let subj = Subject::new("s1", 7).with_role("admin");
    match authorize(&policy, &subj, cap("image.trash"), Scope::Site, 1000) {
        Verdict::Deny { why: Denial::ExplicitDeny { reason } } => {
            assert_eq!(reason, "banned for abuse")
        }
        other => panic!("expected explicit deny, got {other:?}"),
    }
}

#[test]
fn an_allow_and_a_deny_override_on_one_subject_resolve_to_deny() {
    let mut policy = Policy::new();
    policy.declare(cap("report.decide"));
    policy.grant(Grant::role("moderator", cap("report.decide"), Scope::Site));
    policy
        .override_for("s1", cap("report.decide"), Effect::Allow, "weekend cover")
        .unwrap();
    policy
        .override_for("s1", cap("report.decide"), Effect::Deny, "under review")
        .unwrap();
    let v = authorize(
        &policy,
        &Subject::new("s1", 0).with_role("moderator"),
        cap("report.decide"),
        Scope::Site,
        1000,
    );
    assert!(matches!(v, Verdict::Deny { why: Denial::ExplicitDeny { .. } }), "{v:?}");
}

#[test]
fn an_expired_grant_stops_answering() {
    let mut policy = Policy::new();
    policy.declare(cap("image.trash"));
    policy.grant(Grant::role("moderator", cap("image.trash"), Scope::Site).expiring(1000));
    let subj = Subject::new("s1", 0).with_role("moderator");
    assert!(matches!(
        authorize(&policy, &subj, cap("image.trash"), Scope::Site, 999),
        Verdict::Allow { .. }
    ));
    assert!(matches!(
        authorize(&policy, &subj, cap("image.trash"), Scope::Site, 1001),
        Verdict::Deny { why: Denial::Expired }
    ));
}

#[test]
fn a_temporary_override_expires_too() {
    let mut policy = Policy::new();
    policy.declare(cap("report.decide"));
    policy
        .override_for("s1", cap("report.decide"), Effect::Allow, "weekend cover")
        .unwrap();
    policy.overrides.iter_mut().for_each(|o| o.expires_unix = Some(500));
    assert!(matches!(
        authorize(&policy, &Subject::new("s1", 0), cap("report.decide"), Scope::Site, 400),
        Verdict::Allow { .. }
    ));
    assert!(matches!(
        authorize(&policy, &Subject::new("s1", 0), cap("report.decide"), Scope::Site, 600),
        Verdict::Deny { .. }
    ));
}

#[test]
fn an_override_cannot_deny_a_capability_outside_the_vocabulary_silently() {
    let mut policy = Policy::new();
    let err = policy
        .override_for("s1", cap("nonsense.thing"), Effect::Allow, "test")
        .unwrap_err();
    assert!(err.to_string().contains("not declared"), "{err}");
}

#[test]
fn an_override_without_a_reason_is_refused() {
    let mut policy = Policy::new();
    policy.declare(cap("image.trash"));
    let err = policy
        .override_for("s1", cap("image.trash"), Effect::Deny, "  x ")
        .unwrap_err();
    assert!(err.to_string().contains("reason"), "{err}");
}

#[test]
fn a_denial_says_what_was_missing_and_never_just_forbidden() {
    let mut policy = Policy::new();
    policy.declare(cap("image.trash"));
    policy.grant(Grant::role("moderator", cap("image.trash"), Scope::Own));
    let v = authorize(
        &policy,
        &Subject::new("s1", 0).with_role("member"),
        cap("image.trash"),
        Scope::Own,
        0,
    );
    assert_eq!(v.reason(), "no role you hold grants this");

    let v = authorize(
        &policy,
        &Subject::new("s1", 0).with_role("moderator"),
        cap("image.trash"),
        Scope::Site,
        0,
    );
    assert_eq!(v.reason(), "your grant reaches own but this needs site");
}
