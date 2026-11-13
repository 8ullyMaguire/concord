//! The oracle suite: every number the Go ranking code produces, replayed against the Rust port.
//!
//! `scripts/parity/golden_ranking.go` runs the functions that still serve concord.polarisocial.xyz and
//! writes their outputs at 17 significant digits; this file reads that table and compares. The point is
//! not coverage — it is that the mutation gate found 99 survivors across this crate and the governance
//! crate, almost all of them arithmetic operators, and no assertion loose enough to be written by hand
//! (`r > 100`) can kill `phi_star * SCALE -> phi_star / SCALE`.
//!
//! The tolerance is 1e-9 relative rather than bit equality: `exp`, `ln` and `atanh` are not required to be
//! correctly rounded, so the two languages may differ in the last ulp. Every mutant the gate produces
//! moves a value by far more than that — the smallest here is a `+`/`-` flip in a sum of magnitudes
//! around 1, which lands at ~1e-2.

use concord_ranking::{
    age_gate_multiplier, apply_pairwise_vote, decay_points, pain_score, priority_score, rate,
    role_multiplier, vote_weight, Game, Outcome, Rated,
};

fn rows() -> Vec<Vec<String>> {
    let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../scripts/parity/golden.tsv");
    let text = std::fs::read_to_string(path)
        .unwrap_or_else(|error| panic!("read {path}: {error} -- run `go run ./scripts/parity/golden_ranking.go > scripts/parity/golden.tsv` on the host with the Go tree"));
    text.lines()
        .filter(|line| !line.trim().is_empty() && !line.starts_with('#'))
        .map(|line| line.split('\t').map(str::to_string).collect())
        .collect()
}

fn number(row: &[String], index: usize) -> f64 {
    row[index]
        .parse::<f64>()
        .unwrap_or_else(|error| panic!("row {:?} field {index}: {error}", row))
}

/// Compare a computed value with the Go one, naming both when they differ.
fn same(label: &str, got: f64, want: f64) -> bool {
    if got.is_nan() && want.is_nan() {
        return true;
    }
    // `-inf` is a legitimate answer (a negative reputation), and `-inf - -inf` is NaN, so equality is
    // tested before the subtraction.
    if got == want {
        return true;
    }
    let tolerance = 1e-9 * want.abs().max(1.0);
    let ok = (got - want).abs() <= tolerance;
    if !ok {
        println!("MISMATCH {label}: rust {got:.17e} vs go {want:.17e}");
    }
    ok
}

fn outcome(name: &str) -> Outcome {
    match name {
        "a" => Outcome::A,
        "b" => Outcome::B,
        "both" => Outcome::Both,
        "neither" => Outcome::Neither,
        "skip" => Outcome::Skip,
        other => panic!("the oracle names an outcome the port does not: {other}"),
    }
}

#[test]
fn every_go_number_survives_the_port() {
    let rows = rows();
    let mut checked = 0_usize;
    let mut failures = Vec::new();

    for row in &rows {
        match row[0].as_str() {
            "rate" => {
                let (r, rd, sigma) = (number(row, 1), number(row, 2), number(row, 3));
                let count = row[4].parse::<usize>().expect("game count");
                // The generator emits the grid in a fixed order; rebuild the same opponents by re-running
                // the same loop rather than storing 30 opponent triples per row.
                let games = opponents(count);
                let (got_r, got_rd, got_vol) = rate(r, rd, sigma, &games, 0.5);
                for (index, (got, want)) in [
                    (got_r, number(row, 5)),
                    (got_rd, number(row, 6)),
                    (got_vol, number(row, 7)),
                ]
                .into_iter()
                .enumerate()
                {
                    checked += 1;
                    let label = format!("rate({r}, {rd}, {sigma}, {count} games)[{index}]");
                    if !same(&label, got, want) {
                        failures.push(label);
                    }
                }
            }
            "rate1" => {
                // The regula-falsi `else` branch: one game whose result matches the expectation.
                let (r, rd, sigma) = (number(row, 1), number(row, 2), number(row, 3));
                let game = Game {
                    opponent_r: number(row, 4),
                    opponent_rd: number(row, 5),
                    score: number(row, 6),
                };
                let (got_r, got_rd, got_vol) = rate(r, rd, sigma, &[game], 0.5);
                for (index, (got, want)) in [
                    (got_r, number(row, 7)),
                    (got_rd, number(row, 8)),
                    (got_vol, number(row, 9)),
                ]
                .into_iter()
                .enumerate()
                {
                    checked += 1;
                    let label = format!("rate1({r}, {rd}, {sigma}, {} vs {}, {})[{index}]", game.opponent_r, game.opponent_rd, game.score);
                    if !same(&label, got, want) {
                        failures.push(label);
                    }
                }
            }
            "pairwise" => {
                let out = outcome(&row[1]);
                let weight = number(row, 2);
                let a = Rated { r: 1520.0, rd: 90.0, vol: 0.06, strategic_weight: 1.5 };
                let b = Rated { r: 1470.0, rd: 110.0, vol: 0.061, strategic_weight: 0.5 };
                let (got_a, got_b) = apply_pairwise_vote(a, b, out, weight, 0.5);
                for (index, (got, want)) in [
                    (got_a.r, number(row, 3)),
                    (got_a.rd, number(row, 4)),
                    (got_a.vol, number(row, 5)),
                    (got_a.strategic_weight, number(row, 6)),
                    (got_b.r, number(row, 7)),
                    (got_b.rd, number(row, 8)),
                    (got_b.vol, number(row, 9)),
                    (got_b.strategic_weight, number(row, 10)),
                ]
                .into_iter()
                .enumerate()
                {
                    checked += 1;
                    let label = format!("apply_pairwise_vote({}, {weight})[{index}]", row[1]);
                    if !same(&label, got, want) {
                        failures.push(label);
                    }
                }
            }
            "role" => {
                checked += 1;
                if !same(&format!("role_multiplier({})", row[1]), role_multiplier(&row[1]), number(row, 2)) {
                    failures.push(format!("role_multiplier({})", row[1]));
                }
            }
            "vote_weight" => {
                let (reputation, role, cap) = (number(row, 1), number(row, 2), number(row, 3));
                checked += 1;
                let label = format!("vote_weight({reputation}, {role}, {cap})");
                if !same(&label, vote_weight(reputation, role, cap), number(row, 4)) {
                    failures.push(label);
                }
            }
            "age_gate" => {
                let age = number(row, 1);
                checked += 1;
                let label = format!("age_gate_multiplier({age})");
                if !same(&label, age_gate_multiplier(age), number(row, 2)) {
                    failures.push(label);
                }
            }
            "decay" => {
                let (points, age, half) = (number(row, 1), number(row, 2), number(row, 3));
                checked += 1;
                let label = format!("decay_points({points}, {age}, {half})");
                if !same(&label, decay_points(points, age, half), number(row, 4)) {
                    failures.push(label);
                }
            }
            "pain" => {
                let severity = row[1].parse::<i64>().expect("severity");
                let (frequency, strategic, affected) = (number(row, 2), number(row, 3), number(row, 4));
                let (age, half) = (number(row, 5), number(row, 6));
                checked += 1;
                let label = format!("pain_score({severity}, {frequency}, {strategic}, {affected}, {age}, {half})");
                if !same(
                    &label,
                    pain_score(severity, frequency, strategic, affected, age, half),
                    number(row, 7),
                ) {
                    failures.push(label);
                }
            }
            "priority" => {
                let (r, rd, pain) = (number(row, 1), number(row, 2), number(row, 3));
                let (weight, lam, mu) = (number(row, 4), number(row, 5), number(row, 6));
                checked += 1;
                let label = format!("priority_score({r}, {rd}, {pain}, {weight}, {lam}, {mu})");
                if !same(&label, priority_score(r, rd, pain, weight, lam, mu), number(row, 7)) {
                    failures.push(label);
                }
            }
            "outcome" => {
                // The oracle also prints Go's answer for an unknown outcome ("nonsense"), which Go
                // treats as a no-op. The port refuses it instead, so the assertion is that refusal --
                // not a comparison against a value the port has no representation for.
                if Outcome::parse(&row[1]).is_none() {
                    checked += 1;
                    if Outcome::parse(&row[1]).is_some() {
                        failures.push(format!("parse({})", row[1]));
                    }
                    continue;
                }
                let out = outcome(&row[1]);
                let (got_a, got_b) = out.score_pair();
                checked += 2;
                let label = format!("score_pair({})", row[1]);
                if !same(&format!("{label}.0"), got_a, number(row, 3))
                    || !same(&format!("{label}.1"), got_b, number(row, 4))
                {
                    failures.push(label);
                }
                checked += 1;
                let applies = row[2] == "true";
                if out.applies_rating_change() != applies {
                    failures.push(format!("applies_rating_change({})", row[1]));
                }
            }
            // The counts rows belong to the governance crate; they are read there.
            "counts" => {}
            other => panic!("unknown oracle row {other}"),
        }
    }

    println!("{checked} values compared, {} mismatched", failures.len());
    assert!(checked > 300, "the oracle table is too small to mean anything: {checked} values");
    assert!(failures.is_empty(), "{} mismatches: {failures:#?}", failures.len());
}

/// The generator's opponent grid, in its order: ratings 1200/1500/1800 x RDs 50/100/350 x scores 0/0.5/1.
fn opponents(count: usize) -> Vec<Game> {
    let mut games = Vec::with_capacity(count);
    'outer: for opponent_r in [1200.0, 1500.0, 1800.0] {
        for opponent_rd in [50.0, 100.0, 350.0] {
            for score in [0.0, 0.5, 1.0] {
                if games.len() == count {
                    break 'outer;
                }
                games.push(Game { opponent_r, opponent_rd, score });
            }
        }
    }
    games
}
