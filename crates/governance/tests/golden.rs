//! The governance half of the oracle: the counts' four rules, replayed against Go.
//!
//! `Counts` is where a comparison operator *is* the rule — `stand_aside > consent` is §6.6's reluctance
//! flag, `consent / (consent + block)` is the decisive ratio, `consent / (consent + stand_aside)` is the
//! support ratio — so the mutation gate's `> -> >=` and `delete !` survivors in this file are not
//! cosmetic: each one is a rule that silently stops firing at its boundary.
//!
//! The values come from `scripts/parity/golden_ranking.go`, which runs `internal/governance` as it is
//! deployed.

use concord_governance::{ConsensusCounts, GovernanceModel, Result, Stance};

fn rows() -> Vec<Vec<String>> {
    let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../scripts/parity/golden.tsv");
    let text = std::fs::read_to_string(path)
        .unwrap_or_else(|error| panic!("read {path}: {error} -- regenerate with `go run ./scripts/parity/golden_ranking.go > scripts/parity/golden.tsv`"));
    text.lines()
        .filter(|line| line.starts_with("counts"))
        .map(|line| line.split('\t').map(str::to_string).collect())
        .collect()
}

#[test]
fn every_go_count_ratio_and_flag_survives_the_port() {
    let rows = rows();
    assert_eq!(rows.len(), 5, "the oracle's count fixtures moved: {}", rows.len());
    let mut checked = 0_usize;
    let mut failures = Vec::new();
    for row in &rows {
        // The last field is the reluctance FLAG (`true`/`false`), not a number: the oracle prints Go's
        // `bool`, and parsing the whole row as floats turns that into "invalid float literal".
        let reluctant_at = row.len() - 1;
        let numbers: Vec<f64> = row[1..reluctant_at]
            .iter()
            .map(|field| field.parse::<f64>().unwrap_or_else(|error| panic!("{field}: {error}")))
            .collect();
        let counts = ConsensusCounts {
            consent: numbers[0] as i64,
            abstain: numbers[1] as i64,
            stand_aside: numbers[2] as i64,
            block: numbers[3] as i64,
            participants: numbers[4] as i64,
            eligible: numbers[5] as i64,
            open_objections: numbers[6] as i64,
        };
        let (want_decisive, want_support, want_decisive_ratio) = (numbers[7], numbers[8], numbers[9]);
        let want_reluctant = row[reluctant_at] == "true";
        let label = format!("{counts:?}");

        // Both ratios are compared to 1e-12: they are quotients of small integers, so an exact tick apart
        // is already a different rule.
        checked += 1;
        if counts.decisive() != want_decisive as i64 {
            failures.push(format!("{label}.decisive: {} != {want_decisive}", counts.decisive()));
        }
        checked += 1;
        if (counts.support_ratio() - want_support).abs() > 1e-12 {
            failures.push(format!("{label}.support_ratio: {} != {want_support}", counts.support_ratio()));
        }
        checked += 1;
        if (counts.decisive_ratio() - want_decisive_ratio).abs() > 1e-12 {
            failures.push(format!(
                "{label}.decisive_ratio: {} != {want_decisive_ratio}",
                counts.decisive_ratio()
            ));
        }
        checked += 1;
        if counts.reluctant() != want_reluctant {
            failures.push(format!("{label}.reluctant: {} != {want_reluctant}", counts.reluctant()));
        }
    }
    println!("{checked} values compared, {} mismatched", failures.len());
    assert!(failures.is_empty(), "{failures:#?}");
}

/// Every arm of every enum, named. A `delete match arm` mutant survives unless the arm is exercised, and
/// an `as_str` replaced by `""` survives unless its output is compared to a literal — so the spelling is
/// asserted, not just the round trip.
#[test]
fn the_enums_round_trip_every_arm() {
    for (text, stance) in [
        ("consent", Stance::Consent),
        ("abstain", Stance::Abstain),
        ("stand_aside", Stance::StandAside),
        ("block", Stance::Block),
    ] {
        assert_eq!(Stance::parse(text), Some(stance), "parse {text}");
        assert_eq!(stance.as_str(), text, "as_str {text}");
    }
    for nonsense in ["", "Consent", "standaside", "blocked", "xyzzy"] {
        assert_eq!(Stance::parse(nonsense), None, "refuse {nonsense:?}");
    }

    // Two models, not three: `delegated` is not a stored value, and a test that expects it would have
    // had to invent a variant to hold it.
    for (text, model) in [
        ("collective", GovernanceModel::Collective),
        ("maintainer_led", GovernanceModel::MaintainerLed),
    ] {
        assert_eq!(GovernanceModel::parse(text), Some(model), "parse {text}");
        assert_eq!(model.as_str(), text, "as_str {text}");
    }
    for nonsense in ["", "Collective", "maintainer-led", "xyzzy"] {
        assert_eq!(GovernanceModel::parse(nonsense), None, "refuse {nonsense:?}");
    }

    for (text, result) in [
        ("accepted", Result::Accepted),
        ("accepted_overridden", Result::AcceptedOverridden),
        ("rejected", Result::Rejected),
        ("blocked", Result::Blocked),
        ("insufficient_quorum", Result::InsufficientQuorum),
        ("overridden", Result::Overridden),
        ("upheld", Result::Upheld),
    ] {
        assert_eq!(result.as_str(), text, "as_str {text}");
    }
    // The three results a caller can act on are distinct strings: folding any two would make "rejected"
    // and "blocked" the same answer to the feature's author, and they are different remedies.
    let all = [
        Result::Accepted,
        Result::AcceptedOverridden,
        Result::Rejected,
        Result::Blocked,
        Result::InsufficientQuorum,
        Result::Overridden,
        Result::Upheld,
    ];
    let mut spellings: Vec<&str> = all.iter().map(|result| result.as_str()).collect();
    spellings.sort_unstable();
    spellings.dedup();
    assert_eq!(spellings.len(), all.len(), "distinct results must have distinct spellings: {spellings:?}");
}


/// The four boundaries the oracle cannot reach, because each is an equality or a negation rather than a
/// value: `stand_aside > consent` at the point they are equal, the override's `>=` at its own threshold,
/// the merge gate's `!needs_quorum ||` and `!require_reviewer_approval ||` with both switches off, and
/// the extension's span sign on a degenerate window.
///
/// These are the mutations the gate reported as surviving with only the oracle in place: `> -> >=`,
/// `delete !`, `- -> +`. Each one is a rule that stops firing exactly where it matters — at the boundary
/// it was written to test.
#[test]
fn the_boundaries_that_only_a_comparison_can_show() {
    use concord_governance::{
        check_merge_gate, evaluate_override, quorum_threshold, Charter, ConsensusCounts, Deadline,
        GovernanceModel, Result,
    };

    // 1. Reluctance is `stand_aside > consent`, so a 2/2 room is NOT reluctant: the reservations exactly
    //    match the support. `>=` says it is, and a passing call then gets flagged for review on the
    //    strength of a tie.
    let tie = ConsensusCounts { consent: 2, stand_aside: 2, participants: 4, ..Default::default() };
    assert!(!tie.reluctant(), "2 consent / 2 stand-aside is a tie, not a reluctance");
    let one_under = ConsensusCounts { consent: 2, stand_aside: 1, participants: 3, ..Default::default() };
    assert!(!one_under.reluctant());
    let one_over = ConsensusCounts { consent: 1, stand_aside: 2, participants: 3, ..Default::default() };
    assert!(one_over.reluctant(), "more reservations than support is a reluctance");

    // 2. The override's bar is `support_ratio >= override_ratio`: meeting it exactly sets the block
    //    aside. `<` refuses at the boundary and the block stands on a technicality.
    let mut charter = Charter::default_for(GovernanceModel::Collective);
    charter.override_ratio = 0.5;
    charter.quorum_ratio = 0.5;
    charter.quorum_min = 1;
    let exactly = ConsensusCounts {
        consent: 1,
        stand_aside: 1,
        participants: 2,
        eligible: 2,
        ..Default::default()
    };
    assert!(
        (exactly.support_ratio() - 0.5).abs() < 1e-12,
        "the fixture must land exactly on the bar: {}",
        exactly.support_ratio()
    );
    assert_eq!(
        evaluate_override(&exactly, &charter),
        Result::Overridden,
        "a support ratio exactly equal to the override bar overrides"
    );
    // ... and below the bar the block stands.
    let under = ConsensusCounts { consent: 1, stand_aside: 2, participants: 3, eligible: 3, ..Default::default() };
    assert_eq!(evaluate_override(&under, &charter), Result::Upheld);

    // 3. The merge gate's two negations. With the quorum switch OFF, zero approvals still passes the
    //    quorum half; `delete !` makes it fail, and a project that disabled the quorum would stop merging.
    let mut no_quorum = Charter::default_for(GovernanceModel::Collective);
    no_quorum.merge_requires_quorum = false;
    no_quorum.require_reviewer_approval = false;
    let gate = check_merge_gate(0, 0, false, &no_quorum);
    assert!(gate.quorum_ok, "a disabled quorum cannot fail: {gate:?}");
    assert!(gate.reviewer_ok, "an unrequired approval cannot fail: {gate:?}");
    assert_eq!(gate.quorum_needed, 0);
    assert!(gate.passed(), "both halves with their switches off is a pass");

    let mut with_quorum = Charter::default_for(GovernanceModel::Collective);
    with_quorum.merge_requires_quorum = true;
    with_quorum.merge_quorum_min = 2;
    with_quorum.merge_quorum_ratio = 0.0;
    with_quorum.require_reviewer_approval = true;
    assert!(!check_merge_gate(1, 10, true, &with_quorum).passed(), "one approval under a two-approval bar");
    assert!(!check_merge_gate(3, 10, false, &with_quorum).passed(), "enough approvals, no reviewer");
    assert!(check_merge_gate(3, 10, true, &with_quorum).passed());

    // 4. Extending adds the ORIGINAL span, and a degenerate window still gets a day. A `-` flipped to `+`
    //    in `span = closes - opens` yields a negative span, so an extension would move the deadline
    //    backwards on every call whose window is shorter than the extension added.
    let deadline = Deadline { opens_at: 1_000.0, closes_at: 1_000.0 + 7.0 * 86_400.0, extensions: 0 };
    let (extended, ok) = deadline.extend();
    assert!(ok);
    assert!(
        (extended.closes_at - (deadline.closes_at + 7.0 * 86_400.0)).abs() < 1e-9,
        "an extension adds one original window: {}",
        extended.closes_at
    );
    assert_eq!(extended.extensions, 1);

    let degenerate = Deadline { opens_at: 500.0, closes_at: 500.0, extensions: 0 };
    let (grown, ok) = degenerate.extend();
    assert!(ok);
    assert!(
        (grown.closes_at - (500.0 + 86_400.0)).abs() < 1e-9,
        "a zero-length window gets one day, not zero: {}",
        grown.closes_at
    );

    // The threshold itself: never below the floor, never above the room.
    assert_eq!(quorum_threshold(0, &charter), 0, "an empty room needs nobody");
}
