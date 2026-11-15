use polaris_authz::{Capability, Scope, Subject};

#[test]
fn a_capability_is_resource_dot_action() {
    let c = Capability::parse("image.trash").expect("valid");
    assert_eq!(c.resource(), "image");
    assert_eq!(c.action(), "trash");
    assert_eq!(c.to_string(), "image.trash");
}

#[test]
fn a_capability_that_is_not_two_segments_is_refused_with_the_reason() {
    let err = Capability::parse("image").unwrap_err();
    assert!(err.to_string().contains("resource.action"), "{err}");
    let err = Capability::parse("image..trash").unwrap_err();
    assert!(err.to_string().contains("empty"), "{err}");
}

#[test]
fn a_capability_with_capitals_or_spaces_is_refused() {
    assert!(Capability::parse("Image.trash").is_err());
    assert!(Capability::parse("image. trash").is_err());
}

#[test]
fn scopes_are_ordered_and_a_wider_scope_covers_a_narrower_one() {
    assert!(Scope::Estate.covers(Scope::Site));
    assert!(Scope::Site.covers(Scope::Curated));
    assert!(Scope::Curated.covers(Scope::Own));
    assert!(!Scope::Own.covers(Scope::Curated));
}

#[test]
fn everyone_is_a_role_every_subject_has() {
    let s = Subject::new("01M6E5HR", 0);
    assert!(s.has_role("everyone"));
    assert!(!s.has_role("admin"));
}
