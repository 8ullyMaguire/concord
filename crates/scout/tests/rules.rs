//! One test per rule. The rules are the specification's §3/§4, and each of these is a way the rule stops being true.
//!
//! Every test here runs without a database on purpose: these are properties of a decision, and a decision that needs a row to be
//! exercised is a decision nobody tests.

use concord_scout::decompose::{decompose, normalize};
use concord_scout::{
    classify, Candidate, Capability, CandidateVerdict, Health, Input, Options, Pain, Report, Verdict, DEAD_FLOOR, HEALTH_FLOOR,
};
use std::collections::BTreeMap;

fn catalog() -> Vec<Capability> {
    vec![
        Capability::catalog("wip-limits", "WIP limits", "flow"),
        Capability::catalog("offline-use", "Offline use", "reach"),
        Capability::catalog("self-host", "Self hosting", "deploy"),
        Capability::catalog("api", "API", "integration"),
    ]
}

fn candidate(id: i64, slug: &str, attrs: &[(&str, &str)]) -> Candidate {
    Candidate {
        project_id: id,
        slug: slug.to_string(),
        name: slug.to_string(),
        license: String::new(),
        attrs: attrs
            .iter()
            .map(|(key, value)| ((*key).to_string(), (*value).to_string()))
            .collect(),
        reports: 0,
        outcome_rate: 0.0,
    }
}

fn input(idea: &str, candidates: Vec<Candidate>) -> Input {
    Input {
        idea: idea.to_string(),
        capabilities: decompose(idea, &catalog()),
        candidates,
        health: BTreeMap::new(),
        pain: BTreeMap::new(),
    }
}

fn health(input: &mut Input, id: i64, value: f64) {
    input.health.insert(id, Health { value, known: true });
}

fn verdict_for(report: &Report, slug: &str) -> CandidateVerdict {
    report
        .verdicts
        .iter()
        .find(|verdict| verdict.slug == slug)
        .unwrap_or_else(|| panic!("no verdict for {slug} in {report:#?}"))
        .clone()
}

/// §2's trap: `project_metrics` is empty on a fresh instance and its stored column is 0 on the live one. A rule keyed on that number calls
/// the whole catalogue `avoid` and passes any test asserting "at least one avoid".
#[test]
fn unknown_health_is_not_zero_health() {
    let mut subject = input(
        "wip limits and self hosted",
        vec![candidate(1, "one", &[("wip-limits", "yes")])],
    );
    let report = classify(&subject, Options::default());
    let verdict = verdict_for(&report, "one");
    assert_ne!(verdict.verdict, "avoid", "an unmeasured project was called abandoned");
    assert!(!verdict.health_known);
    assert!(
        verdict.signals.iter().any(|signal| signal == "health_unmeasured"),
        "an unmeasured project carries no record that its health is unknown: {:?}",
        verdict.signals
    );
    // And the same candidate measuring as dead IS avoid — the rule is about the evidence, not about the number.
    health(&mut subject, 1, 0.05);
    let report = classify(&subject, Options::default());
    assert_eq!(verdict_for(&report, "one").verdict, "avoid");
}

/// The invariant every other test leans on: a classification with no signal cannot be explained, and the surface exists to answer "why did
/// it say that".
#[test]
fn every_verdict_carries_a_reason_and_a_signal() {
    let mut subject = input(
        "wip limits and self hosted and offline use",
        vec![
            candidate(1, "nothing-known", &[]),
            candidate(2, "subset", &[("wip-limits", "yes")]),
            candidate(3, "complete", &[("wip-limits", "yes"), ("self-host", "yes"), ("offline-use", "no")]),
            candidate(4, "rejected-evidence", &[("wip-limits", "no")]),
        ],
    );
    health(&mut subject, 2, 0.9);
    health(&mut subject, 3, 0.8);
    let mut licensed = candidate(5, "proprietary", &[("wip-limits", "yes")]);
    licensed.license = "proprietary".to_string();
    subject.candidates.push(licensed);
    let report = classify(&subject, Options { license_constraints: vec!["proprietary".to_string()], ..Options::default() });
    assert_eq!(report.verdicts.len(), 5);
    for verdict in &report.verdicts {
        assert!(!verdict.signals.is_empty(), "{} has no signals", verdict.slug);
        assert!(!verdict.reason.trim().is_empty(), "{} has no reason", verdict.slug);
        assert!(!verdict.verdict.is_empty(), "{} has no verdict", verdict.slug);
    }
}

/// A hard licence veto outranks everything, including a healthy lead — it is the caller's own constraint, and a user who said "nothing
/// proprietary" wants that answered before a fit ranking.
#[test]
fn a_licence_constraint_beats_a_healthy_lead() {
    let mut subject = input("wip limits", vec![candidate(1, "lead", &[("wip-limits", "yes")])]);
    health(&mut subject, 1, 0.95);
    let report = classify(&subject, Options::default());
    assert_eq!(verdict_for(&report, "lead").verdict, "base-on");

    subject.candidates[0].license = "Proprietary".to_string();
    let report = classify(&subject, Options { license_constraints: vec!["proprietary".to_string()], ..Options::default() });
    let verdict = verdict_for(&report, "lead");
    assert_eq!(verdict.verdict, "avoid");
    assert!(verdict.signals.iter().any(|signal| signal == "license_constraint:proprietary"));
}

/// An EMPTY licence never conflicts. "We do not know" is not "incompatible", or every project with an unset licence is `avoid` on a fresh
/// instance.
#[test]
fn an_unknown_licence_is_not_a_violation() {
    let mut subject = input("wip limits", vec![candidate(1, "unlicensed", &[("wip-limits", "yes")])]);
    health(&mut subject, 1, 0.9);
    let report = classify(&subject, Options { license_constraints: vec!["proprietary".to_string()], ..Options::default() });
    assert_eq!(verdict_for(&report, "unlicensed").verdict, "base-on");
}

/// Two projects leading equally is a fork request, not a base: the report names both rather than awarding the label to whichever row the
/// query returned first.
#[test]
fn a_tie_awards_nobody_the_lead() {
    let mut subject = input(
        "wip limits",
        vec![
            candidate(1, "alpha", &[("wip-limits", "yes")]),
            candidate(2, "beta", &[("wip-limits", "yes")]),
        ],
    );
    health(&mut subject, 1, 0.9);
    health(&mut subject, 2, 0.9);
    let report = classify(&subject, Options::default());
    for slug in ["alpha", "beta"] {
        let verdict = verdict_for(&report, slug);
        assert_eq!(verdict.verdict, "extend", "{slug} was awarded a base it does not own");
        assert_eq!(verdict.lead_on, 0);
        assert!(
            verdict.signals.iter().any(|signal| signal == "tied_with:alpha,beta"),
            "{slug} does not name who it ties with: {:?}",
            verdict.signals
        );
    }
    // A tie is GLOBAL, not per capability: fit, coverage and matched are per project, so two candidates that tie on one axis tie on all of
    // them. "Tied here, ahead there" cannot be expressed by this scoring, in this port or the reference — recorded so nobody adds an
    // assertion for it later.
}

/// Nobody forks a corpse — but at the spec's own thresholds a dead project is `avoid`, never `inspire`, because the abandonment rule runs
/// first and only `avoid` can say "abandoned".
///
/// `inspire` is reachable only when the caller lowers the health floor under the dead floor AND the project leads nothing: the lead branch
/// sits above it, so a dead project that fits best is still `avoid`, and one that fits second-best is `adopt`. The reference has the same
/// ordering, and the same contradiction: the branch's comment says "still the closest thing in the catalog", while the only path that
/// reaches it requires matching nothing. Recorded in the ledger as a wart rather than repaired, because the verdict is the API and a client
/// stores it.
#[test]
fn a_dead_project_is_avoid_and_inspire_is_nearly_unreachable() {
    let mut subject = input("wip limits", vec![candidate(1, "corpse", &[("wip-limits", "yes")])]);
    health(&mut subject, 1, DEAD_FLOOR / 2.0);
    let report = classify(&subject, Options::default());
    let verdict = verdict_for(&report, "corpse");
    assert_eq!(verdict.verdict, "avoid", "abandoned must be the harder sentence, not the softer one");
    assert!(verdict.signals.iter().any(|signal| signal.starts_with("dead_below:")));

    // `inspire` is reachable only when the idea names NO catalogue capability at all: every other path out of the scoring has a branch
    // above it. With `total == 0` the subset and complete branches cannot fire, so a dead project lands here.
    //
    // That is the contradiction worth recording: the branch's own comment calls it "still the closest thing in the catalog", and the only
    // way to reach it is an idea the catalogue knows nothing about. The reference behaves identically; the verdict is part of the API, so it
    // is reported rather than removed.
    let mut subject = input("we want live-sync", vec![candidate(1, "corpse", &[])]);
    assert_eq!(subject.capabilities.iter().filter(|capability| capability.in_catalog).count(), 0);
    health(&mut subject, 1, 0.05);
    let report = classify(&subject, Options { health_floor: 0.01, ..Options::default() });
    let verdict = verdict_for(&report, "corpse");
    assert_eq!(verdict.verdict, "inspire");
    assert!(verdict.signals.iter().any(|signal| signal.starts_with("dead:")));
    // With ONE catalogue capability in the idea the same dead project is `adopt` — the subset branch sits above `inspire`.
    let mut subject = input("we want live-sync and wip limits", vec![candidate(1, "corpse", &[])]);
    health(&mut subject, 1, 0.05);
    let report = classify(&subject, Options { health_floor: 0.01, ..Options::default() });
    assert_eq!(verdict_for(&report, "corpse").verdict, "adopt");
}

/// Between the two floors: unhealthy enough to avoid, not dead enough to inspire. The order matters — "abandoned" must be the harder
/// sentence, not the softer one.
#[test]
fn unhealthy_but_alive_is_avoid() {
    let mut subject = input("wip limits", vec![candidate(1, "ailing", &[("wip-limits", "yes")])]);
    health(&mut subject, 1, (HEALTH_FLOOR + DEAD_FLOOR) / 2.0);
    let report = classify(&subject, Options::default());
    assert_eq!(verdict_for(&report, "ailing").verdict, "avoid");
}

/// Absence is not evidence: a project satisfying one of three capabilities scores 1/3 of the ones it can answer, with `matched` and `total`
/// saying so — and a project whose every attribute is `no` still counts as evidence, because hiding the `no` would hide the most useful
/// thing on the page.
#[test]
fn a_missing_attribute_is_unknown_and_a_no_is_evidence() {
    let subject = input(
        "wip limits and offline use",
        vec![
            candidate(1, "one-of-two", &[("wip-limits", "yes")]),
            candidate(2, "says-no", &[("wip-limits", "no")]),
        ],
    );
    let report = classify(&subject, Options::default());
    for slug in ["one-of-two", "says-no"] {
        let verdict = verdict_for(&report, slug);
        assert_eq!((verdict.matched, verdict.total), (1, 2), "{slug}");
        assert!((verdict.fit - 0.5).abs() < 1e-9, "{slug} fit {}", verdict.fit);
    }
    // A `no` IS evidence: it counts toward matched exactly as a `yes` does, because hiding the `no` would hide the most useful thing on the
    // page. The two projects tie because they are indistinguishable to this scoring, which is itself the honest answer.
    assert_eq!(verdict_for(&report, "says-no").fit, verdict_for(&report, "one-of-two").fit);
    assert_eq!(verdict_for(&report, "says-no").verdict, "extend");
}

/// The unmeasured-health signal belongs on the COMPLETE branch too, not only on the subset one.
///
/// An earlier reference version put it on `subset:` alone, so a project matching EVERY capability with no metrics row carried no record
/// that its health was unknown — and a reader takes the absence of `health:` in the signals as "health was not relevant" rather than "we
/// never measured it".
#[test]
fn a_complete_match_still_says_its_health_is_unmeasured() {
    let subject = input(
        "wip limits and offline use",
        vec![candidate(1, "complete", &[("wip-limits", "yes"), ("offline-use", "yes")])],
    );
    let report = classify(&subject, Options::default());
    let verdict = verdict_for(&report, "complete");
    assert_eq!((verdict.matched, verdict.total), (2, 2));
    assert!(
        verdict.signals.iter().any(|signal| signal == "health_unmeasured"),
        "a complete match with no telemetry does not record that its health is unknown: {:?}",
        verdict.signals
    );
}

/// A capability the catalogue does not define is reported and scores NOTHING. An idea made only of proposals therefore classifies nothing
/// as usable — which is the honest answer, not a low score.
#[test]
fn a_proposal_carries_no_weight() {
    let idea = "we want live-sync and edge-caching";
    let subject = input(idea, vec![candidate(1, "anything", &[("live-sync", "yes")])]);
    assert_eq!(subject.capabilities.iter().filter(|capability| capability.in_catalog).count(), 0);
    let report = classify(&subject, Options::default());
    assert_eq!((report.in_catalog, report.proposed), (0, 2));
    let verdict = verdict_for(&report, "anything");
    assert_eq!(verdict.verdict, "avoid");
    assert!(verdict.signals.iter().any(|signal| signal == "unclassified"));
    assert_eq!((verdict.matched, verdict.total), (0, 0));
}

/// `max_verdicts` trims the TAIL, and the stats still count every candidate — a reader who sees "1 avoid" must not be reading a count of
/// what happened to fit on the page.
#[test]
fn trimming_does_not_change_the_counts() {
    let candidates = (1..=6)
        .map(|id| candidate(id, &format!("p{id}"), &[("wip-limits", "yes")]))
        .collect();
    let subject = input("wip limits", candidates);
    let report = classify(&subject, Options { max_verdicts: 2, ..Options::default() });
    assert_eq!(report.verdicts.len(), 2);
    assert_eq!(report.candidates_considered, 6);
    assert_eq!(report.stats.values().sum::<i64>(), 6);
}

/// The order is the reader's order: what you must not use comes first, because the reason to read this page is usually to avoid a mistake.
#[test]
fn verdicts_sort_by_severity_then_fit() {
    let mut subject = input(
        "wip limits and offline use",
        vec![
            candidate(1, "good", &[("wip-limits", "yes"), ("offline-use", "yes")]),
            candidate(2, "blocked", &[("offline-use", "yes")]),
        ],
    );
    health(&mut subject, 1, 0.9);
    health(&mut subject, 2, 0.8);
    subject.candidates[1].license = "proprietary".to_string();
    let report = classify(&subject, Options { license_constraints: vec!["proprietary".to_string()], ..Options::default() });
    assert_eq!(
        report.verdicts.iter().map(|verdict| verdict.verdict.as_str()).collect::<Vec<_>>(),
        vec!["avoid", "base-on"],
        "a project you must not use must come before the best one you can"
    );
}

/// A hyphenated key typed verbatim must match. The Go's first version compared a raw key against normalized text, which can never fire for
/// a hyphenated key — "most of them" — and matched almost nothing.
#[test]
fn a_hyphenated_key_matches_verbatim() {
    let capabilities = decompose("we need WIP-Limits", &catalog());
    let matched = capabilities
        .iter()
        .find(|capability| capability.key == "wip-limits")
        .expect("wip-limits did not match its own key");
    assert_eq!(matched.evidence, "verbatim_key");
    assert!(matched.in_catalog);
    // And it appears ONCE, not again as a label match: two entries for one axis scores it twice.
    assert_eq!(
        capabilities.iter().filter(|capability| capability.key == "wip-limits").count(),
        1
    );
}

/// Whole word, not substring: "ran" inside "truncated" is not a match for a capability labelled "Runs locally".
#[test]
fn label_matching_is_whole_word() {
    let capabilities = decompose("the process truncated under load", &[Capability::catalog("runs-local", "Runs locally", "runtime")]);
    assert!(capabilities.is_empty(), "a substring matched: {capabilities:?}");
}

/// A label word IS a weaker signal than a verbatim key, and says so.
#[test]
fn a_label_word_matches_and_is_marked_as_weaker() {
    let capabilities = decompose("works offline", &catalog());
    let matched = capabilities
        .iter()
        .find(|capability| capability.key == "offline-use")
        .expect("the label word did not match");
    assert_eq!(matched.evidence, "label_word");
}

/// A capability-shaped token the catalogue does not define is a PROPOSAL, marked `open` for coverage, and reported rather than dropped.
/// Ordinary words are not proposals, which is why the report can say `proposed: 0` honestly.
#[test]
fn a_proposal_requires_a_capability_shaped_token() {
    let capabilities = decompose("we want live-sync and a dashboard", &catalog());
    let proposals: Vec<&Capability> = capabilities.iter().filter(|capability| !capability.in_catalog).collect();
    assert_eq!(proposals.len(), 1);
    assert_eq!(proposals[0].key, "live-sync");
    assert_eq!(proposals[0].label, "Live Sync");
    assert_eq!(proposals[0].coverage, "open");
    assert_eq!(proposals[0].evidence, "text_token");
    // And a proposal whose token IS a catalogue key is not proposed again — that would double-count the axis.
    let capabilities = decompose("we want api", &catalog());
    assert!(capabilities.iter().filter(|capability| !capability.in_catalog).count() <= 1);
    assert_eq!(
        capabilities.iter().filter(|capability| capability.key == "api").count(),
        1,
        "a catalogue key was counted twice: {capabilities:?}"
    );
}

/// Two catalogue entries sharing a label are one axis: the earlier key wins, so the idea cannot be scored twice on one word.
#[test]
fn duplicate_labels_collapse_to_one_capability() {
    let catalogue = vec![
        Capability::catalog("offline-first", "Offline use", "reach"),
        Capability::catalog("offline-cache", "Offline use", "reach"),
    ];
    let capabilities = decompose("works offline", &catalogue);
    assert_eq!(capabilities.len(), 1, "{capabilities:?}");
    assert_eq!(capabilities[0].key, "offline-first");
}

/// The decomposition is ordered: what can be scored comes first, so the reader sees the axes that will count before the proposals that will
/// not.
#[test]
fn in_catalog_capabilities_come_first() {
    let capabilities = decompose("we want live-sync and offline use", &catalog());
    let first_proposal = capabilities.iter().position(|capability| !capability.in_catalog);
    let last_catalog = capabilities.iter().rposition(|capability| capability.in_catalog);
    if let (Some(first_proposal), Some(last_catalog)) = (first_proposal, last_catalog) {
        assert!(first_proposal > last_catalog, "{:?}", capabilities.iter().map(|c| (&c.key, c.in_catalog)).collect::<Vec<_>>());
    }
}

/// Normalization is what lets `wip_limits`, `WIP Limits` and `wip-limits` be one key.
#[test]
fn normalization_collapses_punctuation() {
    assert_eq!(normalize("  WIP-Limits "), "wip limits");
    assert_eq!(normalize("wip_limits"), "wip limits");
    assert_eq!(normalize("wip   limits"), "wip limits");
    assert_eq!(normalize(""), "");
}

/// Empty input is not a panic and not a null: the report is a report with nothing in it, which is what a client can render.
#[test]
fn an_empty_idea_is_an_empty_report() {
    let report = classify(&input("", vec![]), Options::default());
    assert_eq!(report.idea, "");
    assert!(report.capabilities.is_empty());
    assert!(report.verdicts.is_empty());
    assert_eq!(report.candidates_considered, 0);
    let json = serde_json::to_value(&report).expect("the report must serialize");
    assert!(json.get("verdicts").is_some());
    assert!(json.get("stats").is_some());
}

/// Pain travels with the verdict: a project with known trouble says so, and a project with none omits the key rather than sending `null` —
/// an empty list is not a claim that a project is trouble-free, and `omitempty` is what keeps a client from rendering "0 problems".
#[test]
fn pain_rides_along_and_omits_when_empty() {
    let mut subject = input(
        "wip limits",
        vec![
            candidate(1, "hurts", &[("wip-limits", "yes")]),
            candidate(2, "quiet", &[("wip-limits", "yes")]),
        ],
    );
    health(&mut subject, 1, 0.9);
    health(&mut subject, 2, 0.9);
    subject.pain.insert(
        1,
        vec![Pain {
            kind: "complaint".to_string(),
            title: "loses work on refresh".to_string(),
            detail: String::new(),
            severity: 0.0,
        }],
    );
    let report = classify(&subject, Options::default());
    let hurts = serde_json::to_value(verdict_for(&report, "hurts")).expect("serialize");
    let quiet = serde_json::to_value(verdict_for(&report, "quiet")).expect("serialize");
    assert_eq!(hurts["pain"][0]["title"], "loses work on refresh");
    assert!(quiet.get("pain").is_none(), "an empty pain list was sent: {quiet}");
    assert!(hurts.get("license").is_none(), "an empty licence was sent");
}

/// The five verdict spellings are the API's, not this crate's: a client stores them.
#[test]
fn verdict_spellings_are_stable() {
    assert_eq!(Verdict::Avoid.as_str(), "avoid");
    assert_eq!(Verdict::BaseOn.as_str(), "base-on");
    assert_eq!(Verdict::Extend.as_str(), "extend");
    assert_eq!(Verdict::Adopt.as_str(), "adopt");
    assert_eq!(Verdict::Inspire.as_str(), "inspire");
}
