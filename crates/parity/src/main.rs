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
];

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
        let got = fingerprint(&body);
        // One implementation of the shape, run over both bodies: the recorder stores the body
        // rather than a shape it computed itself, because two implementations of "structurally
        // equal" drift and then agree with each other instead of with the service.
        let want_fp = match rec.get("body").and_then(Value::as_str) {
            Some(recorded) => fingerprint(recorded),
            None => {
                if verbose {
                    eprintln!("{shape_key}: no body recorded — status only");
                }
                continue;
            }
        };
        if got != want_fp {
            eprintln!("{shape_key}: body shape differs");
            if verbose {
                eprintln!("  want: {}", truncate(&want_fp));
                eprintln!("  got:  {}", truncate(&got));
            }
            bad += 1;
        } else if verbose {
            eprintln!("{shape_key}: ok ({})", truncate(&got));
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
