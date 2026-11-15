use polaris_authz::{
    decide, Act, Basis, Decision, Mandate, QuorumRule, Sovereignty, Tally,
};

#[test]
fn seventy_percent_of_the_eligible_set_is_the_bar() {
    let rule = QuorumRule::default_for(Act::Ban);
    assert_eq!(rule.permille, 700);
    assert_eq!(rule.basis, Basis::Eligible);

    let short = decide(&rule, &Tally::new(10, 6, 4), Sovereignty::UserDriven, false, false);
    match short {
        Decision::QuorumShort { found, needed, of, .. } => {
            assert_eq!((found, needed, of), (6, 7, 10));
            assert_eq!(short.reason(), "not adopted: 6 of 10 votes, 7 needed");
        }
        other => panic!("{other:?}"),
    }

    let met = decide(&rule, &Tally::new(10, 7, 3), Sovereignty::UserDriven, false, false);
    match met {
        Decision::Adopted { via: Mandate::Quorum { yes, needed, of, .. } } => {
            assert_eq!((yes, needed, of), (7, 7, 10))
        }
        other => panic!("{other:?}"),
    }
}

#[test]
fn the_threshold_rounds_up_because_two_of_three_is_not_seventy_percent() {
    let rule = QuorumRule::new(700, Basis::Eligible);
    assert_eq!(rule.needed(3), 3, "70% of 3 must be 3 votes, not 2");
    assert_eq!(rule.needed(10), 7);
    assert_eq!(rule.needed(1), 1);
}

#[test]
fn a_cast_basis_counts_the_votes_cast_and_can_carry_on_a_small_turnout() {
    let rule = QuorumRule::default_for(Act::Sanction);
    assert_eq!(rule.basis, Basis::Cast);
    // 3 of 4 cast is 75%, enough at 60%; the same 3 of 40 eligible would not be.
    assert!(decide(&rule, &Tally::new(40, 3, 1), Sovereignty::UserDriven, false, false).is_adopted());
    let eligible_rule = QuorumRule::new(600, Basis::Eligible);
    assert!(!decide(&eligible_rule, &Tally::new(40, 3, 1), Sovereignty::UserDriven, false, false).is_adopted());
}

#[test]
fn an_empty_electorate_cannot_carry_anything() {
    let rule = QuorumRule::default_for(Act::Ban);
    let decision = decide(&rule, &Tally::new(0, 0, 0), Sovereignty::Admin, false, false);
    assert!(!decision.is_adopted(), "a ban on zero eligible voters must not pass");
    match decision {
        Decision::QuorumShort { needed, found, .. } => assert_eq!((needed, found), (1, 0)),
        other => panic!("{other:?}"),
    }
}

#[test]
fn sovereignty_carries_the_act_and_says_that_is_what_happened() {
    let rule = QuorumRule::default_for(Act::Ban);
    let decision = decide(&rule, &Tally::new(10, 0, 0), Sovereignty::Admin, true, true);
    assert_eq!(decision, Decision::Adopted { via: Mandate::Sovereignty });
    assert_eq!(decision.reason(), "adopted by the operator under sovereignty");
}

#[test]
fn a_user_driven_instance_refuses_the_sovereignty_path_by_name() {
    let rule = QuorumRule::default_for(Act::Ban);
    let decision = decide(&rule, &Tally::new(10, 9, 1), Sovereignty::UserDriven, true, true);
    assert_eq!(decision, Decision::SovereigntyUnavailable);
    assert_eq!(
        decision.reason(),
        "this instance is user-driven: the act needs a quorum"
    );
}

#[test]
fn a_non_operator_cannot_invoke_sovereignty() {
    let rule = QuorumRule::default_for(Act::RuleAdopt);
    let decision = decide(&rule, &Tally::new(10, 2, 1), Sovereignty::Admin, false, true);
    assert!(matches!(decision, Decision::QuorumShort { .. }), "{decision:?}");
}

#[test]
fn a_met_quorum_is_recorded_as_a_quorum_even_when_sovereignty_is_available() {
    let rule = QuorumRule::default_for(Act::RuleAdopt);
    let decision = decide(&rule, &Tally::new(10, 8, 2), Sovereignty::Admin, true, false);
    assert!(matches!(decision, Decision::Adopted { via: Mandate::Quorum { .. } }));
}
