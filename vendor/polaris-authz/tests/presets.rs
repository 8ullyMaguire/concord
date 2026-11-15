use polaris_authz::{authorize, build, Capability, Preset, Scope, Subject};

fn caps(list: &[&str]) -> Vec<Capability> {
    list.iter().map(|s| Capability::parse(s).unwrap()).collect()
}

#[test]
fn the_staff_preset_reproduces_suki() {
    let all = caps(&["image.trash", "report.decide", "comment.write"]);
    let staff = caps(&["report.decide", "comment.write"]);
    let public = caps(&["comment.write"]);
    let p = build(Preset::Staff, &all, &public, &staff);

    let admin = Subject::new("a", 0).with_role("admin");
    let moderator = Subject::new("m", 0).with_role("moderator");
    let member = Subject::new("u", 0).with_role("member");

    assert!(authorize(&p, &admin, Capability::parse("image.trash").unwrap(), Scope::Site, 0).is_allowed());
    assert!(!authorize(&p, &moderator, Capability::parse("image.trash").unwrap(), Scope::Site, 0).is_allowed());
    assert!(authorize(&p, &moderator, Capability::parse("report.decide").unwrap(), Scope::Site, 0).is_allowed());
    assert!(!authorize(&p, &member, Capability::parse("image.trash").unwrap(), Scope::Site, 0).is_allowed());
    assert!(authorize(&p, &member, Capability::parse("comment.write").unwrap(), Scope::Own, 0).is_allowed());
    // A member may act on their own objects and nothing wider.
    assert!(!authorize(&p, &member, Capability::parse("comment.write").unwrap(), Scope::Site, 0).is_allowed());
}

#[test]
fn the_reputation_preset_reproduces_blackrep() {
    let c = caps(&["report.decide"]);
    let p = build(Preset::ReputationEarned, &c, &[], &[]);
    let cap = Capability::parse("report.decide").unwrap();
    assert!(!authorize(&p, &Subject::new("u", 0).with_reputation(4999), cap.clone(), Scope::Site, 0).is_allowed());
    assert!(authorize(&p, &Subject::new("u", 0).with_reputation(5000), cap, Scope::Site, 0).is_allowed());
}

#[test]
fn the_trust_ladder_moves_the_ceiling_and_names_the_shortfall() {
    let c = caps(&["work.edit"]);
    let p = build(Preset::TrustLadder, &c, &[], &[]);
    let cap = Capability::parse("work.edit").unwrap();
    assert!(authorize(&p, &Subject::new("u", 1), cap.clone(), Scope::Own, 0).is_allowed());
    let v = authorize(&p, &Subject::new("u", 1), cap, Scope::Site, 0);
    assert_eq!(v.reason(), "your grant reaches own but this needs site");
}

#[test]
fn open_grants_everything_to_everyone_and_says_so() {
    let c = caps(&["image.trash"]);
    let p = build(Preset::Open, &c, &[], &[]);
    assert!(authorize(
        &p,
        &Subject::new("nobody", 0),
        Capability::parse("image.trash").unwrap(),
        Scope::Estate,
        0
    )
    .is_allowed());
}

#[test]
fn a_fine_grained_policy_grants_only_the_public_set_and_leaves_the_rest_to_overrides() {
    let c = caps(&["image.trash", "comment.write"]);
    let public = caps(&["comment.write"]);
    let p = build(Preset::FineGrained, &c, &public, &[]);
    let admin = Subject::new("a", 0).with_role("admin");
    assert!(!authorize(&p, &admin, Capability::parse("image.trash").unwrap(), Scope::Site, 0).is_allowed());
    assert!(authorize(&p, &admin, Capability::parse("comment.write").unwrap(), Scope::Own, 0).is_allowed());
}
