//! Replay the parity corpus against a candidate service and diff structurally.
//!
//! Structural, not textual: key order is normalised, timestamps are compared as shapes
//! (a value of the right type), ids by shape unless the corpus names them. A textual diff
//! over JSON reports every reordered key as a regression, which is how a parity gate gets
//! ignored.
//!
//! The plan's sketch compared only the status code, which it calls a structural diff in its own
//! doc comment. A 200 says the route exists; it says nothing about the payload, and the payload is
//! what the slices are judged on. This compares bodies too, by shape, so a candidate can differ in
//! values that are allowed to differ (ids, timestamps, versions) and still fail on a renamed key,
//! a changed type, a vanished field or a different array length.
//!
//! Usage:
//!     CANDIDATE=https://concord.polarisocial.xyz cargo run -p concord-parity -- \
//!         --corpus crates/parity/corpus.json
use std::collections::BTreeMap;

use serde_json::Value;

/// Keys whose value is allowed to differ between two recordings: they carry the moment, an
/// identity, or the build. Their *type* is still compared, so a timestamp that becomes a string
/// still fails.
const VOLATILE_KEYS: &[&str] = &[
    "id", "project_id", "feature_id", "author_id", "user_id", "comment_id", "complaint_id",
    "org_id", "milestone_id", "tag_id", "created_at", "updated_at", "deleted_at", "at", "time",
    "timestamp", "recorded_at", "version", "etag", "generated_at", "as_of", "elapsed_ms",
    "duration_ms", "public_id", "sha", "hash", "cursor", "next_cursor", "total",
    // Recomputed on a sweep, not on read: `project_metrics.health_score` is written by the Go
    // service's background pass, so the same project answers 0.5081 and twenty minutes later
    // 0.5134. Moving it to the tolerant tier was not enough — 1% is ~20 minutes of drift — and the
    // port has no formula of its own to get wrong here: it reads the column. Compared by type, so a
    // string where a number belongs still fails.
    "health_score",
];

/// Keys compared with a tolerance rather than for equality: derived scores that decay with the
/// clock. `health_score` is computed from metrics and a half-life, so two readings a minute apart
/// differ in the fourth decimal — a fingerprint over exact numbers reported that as a body change,
/// and a recorder that called it a regression would have hidden the next real one behind it.
const TOLERANT_KEYS: &[&str] = &["elo", "rating", "weight"];

/// How far a tolerant value may move and still be the same reading: 1% relative. Wide enough for
/// any half-life in the charter to move a score across a minute, narrow enough that a wrong formula
/// (which is off by tens of percent) still fails.
const TOLERANCE: f64 = 0.01;

fn is_tolerant_context(path: &str) -> bool {
    let leaf = path.rsplit('.').next().unwrap_or(path);
    let leaf = leaf.split('[').next().unwrap_or(leaf);
    TOLERANT_KEYS.iter().any(|k| k.eq_ignore_ascii_case(leaf))
}

fn is_volatile_context(path: &str) -> bool {
    let leaf = path.rsplit('.').next().unwrap_or(path);
    let leaf = leaf.split('[').next().unwrap_or(leaf);
    VOLATILE_KEYS.iter().any(|k| k.eq_ignore_ascii_case(leaf))
}

/// The shape of a JSON value: its structure and the types of its leaves, with volatile leaves
/// reduced to their type. Objects are key-sorted, so key order cannot fail a replay.
fn shape(value: &Value, path: &str, out: &mut String) {
    match value {
        Value::Object(map) => {
            let mut keys: Vec<&String> = map.keys().collect();
            keys.sort();
            out.push('{');
            for key in keys {
                out.push_str(key);
                out.push(':');
                shape(&map[key], &format!("{path}.{key}"), out);
                out.push(',');
            }
            out.push('}');
        }
        Value::Array(items) => {
            // Length is compared, contents by shape: an array of twelve pictures and an array of
            // eleven is a regression, and the shape of the first element stands for the rest.
            out.push_str(&format!("[{}:", items.len()));
            if let Some(first) = items.first() {
                shape(first, &format!("{path}[]"), out);
            }
            out.push(']');
        }
        Value::Null => out.push_str("null"),
        Value::Bool(b) => {
            if is_volatile_context(path) {
                out.push_str("bool")
            } else {
                out.push_str(&b.to_string())
            }
        }
        Value::Number(n) => {
            if is_volatile_context(path) {
                out.push_str(if n.is_f64() { "number:float" } else { "number:int" });
            } else {
                out.push_str(&format!("number:{}", n));
            }
        }
        Value::String(s) => {
            if is_volatile_context(path) {
                out.push_str("string");
            } else {
                out.push_str(&format!("string:{s}"));
            }
        }
    }
}

/// Every difference between two JSON values, by path, with the tolerance rule applied.
///
/// A fingerprint string can only say "these differ"; the path says where, and the tolerance has to
/// be applied to the values themselves — bucketing them into the string would fail on the run where
/// a drifting value happens to straddle a bucket boundary, which is a gate that fails for the clock.
fn differences(want: &Value, got: &Value, path: &str, out: &mut Vec<String>) {
    match (want, got) {
        (Value::Object(a), Value::Object(b)) => {
            for key in a.keys() {
                match b.get(key) {
                    Some(other) => differences(&a[key], other, &format!("{path}.{key}"), out),
                    None => out.push(format!("{path}.{key}: missing")),
                }
            }
            for key in b.keys() {
                if !a.contains_key(key) {
                    out.push(format!("{path}.{key}: unexpected"));
                }
            }
        }
        (Value::Array(a), Value::Array(b)) => {
            if a.len() != b.len() {
                out.push(format!("{path}: {} items, want {}", b.len(), a.len()));
                return;
            }
            for (index, (one, two)) in a.iter().zip(b).enumerate() {
                differences(one, two, &format!("{path}[{index}]"), out);
            }
        }
        (Value::Number(a), Value::Number(b)) if is_tolerant_context(path) => {
            let (Some(x), Some(y)) = (a.as_f64(), b.as_f64()) else {
                out.push(format!("{path}: {got} vs {want}"));
                return;
            };
            let scale = x.abs().max(y.abs()).max(1e-9);
            if (x - y).abs() / scale > TOLERANCE {
                out.push(format!("{path}: {y} vs {x} (beyond {:.0}%)", TOLERANCE * 100.0));
            }
        }
        (a, b) if is_volatile_context(path) => {
            if std::mem::discriminant(a) != std::mem::discriminant(b) {
                out.push(format!("{path}: type changed"));
            }
        }
        (a, b) => {
            if a != b {
                out.push(format!("{path}: {} vs {}", short(b), short(a)));
            }
        }
    }
}

fn short(value: &Value) -> String {
    let text = value.to_string();
    if text.len() > 60 {
        format!("{}…", &text[..60])
    } else {
        text
    }
}

fn fingerprint(body: &str) -> String {
    match serde_json::from_str::<Value>(body) {
        Ok(value) => {
            let mut out = String::new();
            shape(&value, "", &mut out);
            out
        }
        // Not JSON: the whole body is the fingerprint, compared verbatim, because there is no
        // structure to normalise and the byte is all we have.
        Err(_) => format!("raw:{}", body.trim()),
    }
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let mut corpus_path = String::from("corpus.json");
    let mut verbose = false;
    let mut args = std::env::args().skip(1);
    while let Some(arg) = args.next() {
        match arg.as_str() {
            "--corpus" => corpus_path = args.next().expect("--corpus needs a path"),
            "--verbose" | "-v" => verbose = true,
            other => anyhow::bail!("unknown argument {other}"),
        }
    }
    let corpus: BTreeMap<String, Value> =
        serde_json::from_str(&tokio::fs::read_to_string(&corpus_path).await?)?;
    let base = std::env::var("CANDIDATE").unwrap_or_else(|_| "http://127.0.0.1:8106".into());
    let client = reqwest::Client::builder()
        .user_agent("curl/8") // Cloudflare 1010s a bare reqwest UA
        .build()?;

    let mut bad = 0usize;
    let mut checked = 0usize;
    let mut skipped = 0usize;
    for (shape_key, rec) in &corpus {
        if shape_key == "__meta" {
            continue;
        }
        if rec.get("unstable").is_some() || rec.get("expected_change").is_some() {
            skipped += 1;
            continue;
        }
        // The recorded path, not the shape: an id-shaped segment replayed as 0 asks the candidate
        // whether it has a row zero, which is a different question from the one the corpus asks.
        let path = rec
            .get("path")
            .and_then(Value::as_str)
            .unwrap_or(shape_key)
            .to_string();
        let url = format!("{base}{path}");
        let resp = client.get(&url).send().await?;
        let status = resp.status().as_u16();
        let want = rec["status"].as_u64().unwrap_or(200) as u16;
        let body = resp.text().await?;
        checked += 1;
        if status != want {
            eprintln!("{shape_key}: status {status} != {want}");
            bad += 1;
            continue;
        }
        // One comparison, run over both bodies: the recorder stores the body rather than a shape it
        // computed itself, because two implementations of "structurally equal" drift and then agree
        // with each other instead of with the service.
        let Some(recorded) = rec.get("body").and_then(Value::as_str) else {
            if verbose {
                eprintln!("{shape_key}: no body recorded — status only");
            }
            continue;
        };
        let (want_value, got_value) = (
            serde_json::from_str::<Value>(recorded),
            serde_json::from_str::<Value>(&body),
        );
        let mut diffs = Vec::new();
        match (want_value, got_value) {
            (Ok(want_json), Ok(got_json)) => differences(&want_json, &got_json, "", &mut diffs),
            // Not JSON: there is no structure to compare, so the bytes are the whole answer.
            _ => {
                if recorded.trim() != body.trim() {
                    diffs.push(format!("{}: body differs", shape_key));
                }
            }
        }
        if diffs.is_empty() {
            if verbose {
                eprintln!("{shape_key}: ok ({})", truncate(&fingerprint(&body)));
            }
        } else {
            eprintln!("{shape_key}: {} difference(s)", diffs.len());
            for line in diffs.iter().take(if verbose { 20 } else { 3 }) {
                eprintln!("  {line}");
            }
            bad += 1;
        }
    }
    println!("{bad} shape(s) disagreed of {checked} checked, {skipped} skipped");
    std::process::exit(if bad == 0 { 0 } else { 1 });
}

fn truncate(value: &str) -> String {
    const LIMIT: usize = 240;
    if value.len() <= LIMIT {
        return value.to_string();
    }
    format!("{}…", &value[..LIMIT])
}
