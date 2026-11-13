//! The health score and the newcomer median, checked against the Go implementation that still serves
//! `concord.polarisocial.xyz`.
//!
//! `golden.tsv` holds 1,344 `health` rows crossing every boundary the piecewise components have — the 24-hour
//! and 14-day ends of the responsiveness ramp, one contributor and three releases on the log and linear ramps,
//! the unknown sentinel, and ages from today to eleven years — plus five `weights` rows and six `newcomer`
//! fixtures. They were printed by `scripts/parity/golden_ranking.go`, not written by hand, because a hand
//! value is a value the port could get wrong in the same direction and agree with itself about.
//!
//! The tolerance is loose on purpose — `%.17g` round-trips a float64 exactly through text, but the two sides
//! reach the total by different arithmetic orderings — and the failure message prints both numbers, so a
//! mismatch names the row that broke rather than the count.

use concord_ranking::health::{
    health_score, newcomer_response, ForgeEvent, Metrics, Weights, DEFAULT_WEIGHTS, UNKNOWN_REVIEW_HOURS,
};

const ORACLE: &str = include_str!("golden.tsv");

fn rows() -> Vec<Vec<&'static str>> {
    ORACLE
        .lines()
        .filter(|line| !line.trim().is_empty())
        .map(|line| line.split('\t').collect())
        .collect()
}

fn number(text: &str) -> f64 {
    text.parse::<f64>()
        .unwrap_or_else(|_| panic!("the oracle's number did not parse: {text:?}"))
}

fn same(label: &str, got: f64, want: f64) -> bool {
    if got == want {
        return true;
    }
    // Relative, and never absolute-tight: a component at 1e-18 is a zero that floated.
    let tolerance = 1e-9 * want.abs().max(1.0);
    let ok = (got - want).abs() <= tolerance;
    if !ok {
        println!("MISMATCH {label}: rust {got:.17e} vs go {want:.17e}");
    }
    ok
}

#[test]
fn every_go_health_number_survives_the_port() {
    let mut checked = 0_usize;
    let mut failures = Vec::new();
    let mut health_rows = 0_usize;

    for row in rows() {
        match row[0] {
            "health" => {
                health_rows += 1;
                let metrics = Metrics {
                    last_commit_age_days: number(row[1]),
                    median_review_hours: number(row[2]),
                    contributors: row[3].parse().expect("contributors"),
                    releases_90d: row[4].parse().expect("releases"),
                };
                let breakdown = health_score(metrics, DEFAULT_WEIGHTS);
                let label = format!("health(age={} review={} c={} r={})", row[1], row[2], row[3], row[4]);
                for (got, at) in [
                    (breakdown.recency, 5),
                    (breakdown.responsiveness, 6),
                    (breakdown.breadth, 7),
                    (breakdown.cadence, 8),
                    (breakdown.total, 9),
                ] {
                    checked += 1;
                    if !same(&format!("{label}.{at}"), got, number(row[at])) {
                        failures.push(format!("{label}.{at}"));
                    }
                }
                let known = row[10] == "true";
                if breakdown.responsiveness_known != known {
                    failures.push(format!("{label}.responsiveness_known"));
                }
                checked += 1;
            }
            "weights" => {
                let weights = Weights {
                    recency: number(row[1]),
                    responsiveness: number(row[2]),
                    breadth: number(row[3]),
                    cadence: number(row[4]),
                };
                // The same fixed metrics the generator scored, so the normalisation is compared and not just
                // the arithmetic that follows it.
                let metrics = Metrics {
                    last_commit_age_days: 12.0,
                    median_review_hours: 40.0,
                    contributors: 7,
                    releases_90d: 2,
                };
                let label = format!("weights({} {} {} {})", row[1], row[2], row[3], row[4]);
                checked += 1;
                if !same(&label, health_score(metrics, weights).total, number(row[5])) {
                    failures.push(label);
                }
            }
            "newcomer" => {
                let stream = fixture(row[1]);
                let got = newcomer_response(&stream);
                let want_ok = row[2] == "true";
                let want_hours = number(row[3]);
                let want_sample = row[4].parse::<usize>().expect("sample");
                checked += 3;
                match got {
                    Some((hours, sample)) => {
                        if !want_ok {
                            failures.push(format!("newcomer({}).ok: published a number the Go side refuses", row[1]));
                        }
                        if !same(&format!("newcomer({}).hours", row[1]), hours, want_hours) {
                            failures.push(format!("newcomer({}).hours", row[1]));
                        }
                        if sample != want_sample {
                            failures.push(format!(
                                "newcomer({}).sample: rust {sample} vs go {want_sample}",
                                row[1]
                            ));
                        }
                    }
                    None => {
                        if want_ok {
                            failures.push(format!("newcomer({}).ok: refused a number the Go side publishes", row[1]));
                        }
                    }
                }
            }
            // Every other tag belongs to `golden.rs` or the governance crate, from the same table. They are
            // SKIPPED here on purpose and read there — and `golden.rs` panics on a tag it does not know, so a
            // new oracle section nobody reads still fails a test.
            _ => {}
        }
    }

    println!("{checked} values compared over {health_rows} health rows, {} mismatched", failures.len());
    assert!(health_rows > 1_000, "the health grid is too small to mean anything: {health_rows}");
    assert!(failures.is_empty(), "{} mismatches: {failures:#?}", failures.len());
}

/// The six `newcomer` fixtures, rebuilt exactly as the generator built them — event streams, because "first
/// pull request" is the earliest opening by that author in that repository and not a flag a caller sets.
fn fixture(name: &str) -> Vec<ForgeEvent<'static>> {
    use ForgeEvent as E;
    let make = |rows: &[(&'static str, i64, &'static str, f64)]| -> Vec<ForgeEvent<'static>> {
        rows.iter()
            .map(|(kind, number, actor, at)| E {
                repo: "owner/repo",
                kind,
                number: *number,
                actor,
                at: *at,
            })
            .collect()
    };
    const OPENED: &str = "pr_opened";
    const COMMENT: &str = "pr_comment";
    const REVIEW: &str = "pr_review";
    match name {
        "five_first_prs" => make(&[
            (OPENED, 1, "alice", 0.0), (COMMENT, 1, "m", 3600.0),
            (OPENED, 2, "bob", 0.0), (COMMENT, 2, "m", 7200.0),
            (OPENED, 3, "carol", 0.0), (COMMENT, 3, "m", 10800.0),
            (OPENED, 4, "dave", 0.0), (COMMENT, 4, "m", 14400.0),
            (OPENED, 5, "erin", 0.0), (COMMENT, 5, "m", 18000.0),
        ]),
        "four_below_floor" => make(&[
            (OPENED, 1, "alice", 0.0), (COMMENT, 1, "m", 3600.0),
            (OPENED, 2, "bob", 0.0), (COMMENT, 2, "m", 7200.0),
            (OPENED, 3, "carol", 0.0), (COMMENT, 3, "m", 10800.0),
            (OPENED, 4, "dave", 0.0), (COMMENT, 4, "m", 14400.0),
        ]),
        "self_comments_ignored" => make(&[
            (OPENED, 1, "alice", 0.0), (COMMENT, 1, "alice", 60.0),
            (OPENED, 2, "bob", 0.0), (COMMENT, 2, "bob", 60.0),
            (OPENED, 3, "carol", 0.0), (COMMENT, 3, "carol", 60.0),
            (OPENED, 4, "dave", 0.0), (COMMENT, 4, "dave", 60.0),
            (OPENED, 5, "erin", 0.0), (COMMENT, 5, "erin", 60.0),
        ]),
        "second_prs_ignored" => make(&[
            (OPENED, 1, "alice", 0.0), (COMMENT, 1, "m", 3600.0),
            (OPENED, 2, "alice", 100000.0), (COMMENT, 2, "m", 460000.0),
            (OPENED, 3, "bob", 0.0), (COMMENT, 3, "m", 7200.0),
            (OPENED, 4, "carol", 0.0), (COMMENT, 4, "m", 10800.0),
            (OPENED, 5, "dave", 0.0), (COMMENT, 5, "m", 14400.0),
            (OPENED, 6, "erin", 0.0), (COMMENT, 6, "m", 18000.0),
        ]),
        "lower_middle_median" => make(&[
            (OPENED, 1, "alice", 0.0), (COMMENT, 1, "m", 3600.0),
            (OPENED, 2, "bob", 0.0), (COMMENT, 2, "m", 7200.0),
            (OPENED, 3, "carol", 0.0), (COMMENT, 3, "m", 10800.0),
            (OPENED, 4, "dave", 0.0), (COMMENT, 4, "m", 14400.0),
            (OPENED, 5, "erin", 0.0), (COMMENT, 5, "m", 18000.0),
            (OPENED, 6, "frank", 0.0), (COMMENT, 6, "m", 720000.0),
        ]),
        "out_of_order_redelivery" => make(&[
            (OPENED, 7, "alice", 50000.0), (OPENED, 1, "alice", 0.0),
            (REVIEW, 1, "m", 3600.0), (REVIEW, 7, "m", 500000.0),
        ]),
        other => panic!("the oracle names a newcomer fixture the port does not build: {other}"),
    }
}

#[test]
fn the_unknown_sentinel_is_not_a_measurement_of_zero() {
    // The one rule worth restating outside the grid: a repository with no measured review time must not score
    // as if it answers instantly.
    let unmeasured = health_score(
        Metrics {
            last_commit_age_days: 0.0,
            median_review_hours: UNKNOWN_REVIEW_HOURS,
            contributors: 50,
            releases_90d: 3,
        },
        DEFAULT_WEIGHTS,
    );
    assert!(!unmeasured.responsiveness_known);
    // Fifty contributors is 0.995 of the breadth ramp, not 1.0 (the ramp tops out at 51), so the assertion is
    // the renormalisation and not a round number: the total is the measured components over 0.75, and it is
    // above the 0.75 that dividing by the full weight sum would cap it at.
    let over_measured =
        (0.35 * unmeasured.recency + 0.20 * unmeasured.breadth + 0.20 * unmeasured.cadence) / 0.75;
    assert!((unmeasured.total - over_measured).abs() < 1e-12, "{unmeasured:?}");
    assert!(unmeasured.total > 0.75, "{unmeasured:?}");
}
