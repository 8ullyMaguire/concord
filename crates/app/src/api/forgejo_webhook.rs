//! The signed Forgejo webhook (step 3, ADR 0014).
//!
//! Handler, HMAC helpers and RFC3339 parser moved verbatim from `lib.rs`.
//! Every refusal stays an identical 401: the difference would tell an
//! attacker whether the header format and digest were right.

use axum::{
    extract::State,
    http::{header::HeaderMap, StatusCode},
    Json, Router,
};
use polaris_core::ApiError;
use serde_json::Value;

use crate::{app_mutant, internal, AppState};

/// `POST /hooks/forgejo` — a repository event, signed by the forge.
///
/// **Configured or off.** With no secret the route answers 503 rather than accepting anything: an unauthenticated write endpoint
/// that is live because nobody finished setting it up is worse than one that says so.
///
/// **Constant-time comparison over the exact bytes received.** The signature is `sha256=<hex>` of the body; comparing with `==` on a
/// decoded string leaks the length of the matching prefix through timing, and the secret is the only thing standing between a
/// stranger and a table that moves a published median.
///
/// **Every refusal is the same 401.** "No header", "wrong scheme", "wrong digest" answer identically, because the difference tells an
/// attacker whether they have the right format.
///
/// The audit row is written before the event row and regardless of it: a delivery that cannot be given a trustworthy timestamp is
/// still something that happened, and losing the record of it would make a forge problem invisible.
async fn forgejo_webhook(
    State(state): State<AppState>,
    headers: HeaderMap,
    body: axum::body::Bytes,
) -> Result<(StatusCode, Json<Value>), ApiError> {
    let secret = std::env::var("CONCORD_WEBHOOK_SECRET").unwrap_or_default();
    if secret.is_empty() {
        return Err(ApiError::unavailable(
            "no webhook secret configured; deliveries are refused until one is set",
        ));
    }
    let refused = || ApiError::unauthenticated("webhook signature is not valid");
    let provided = headers
        .get("x-hub-signature-256")
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.strip_prefix("sha256="))
        .ok_or_else(refused)?;
    let expected = hex(&hmac_sha256(secret.as_bytes(), &body));
    if !constant_time_eq(provided.as_bytes(), expected.as_bytes()) {
        return Err(refused());
    }

    let event = headers
        .get("x-gitea-event")
        .and_then(|value| value.to_str().ok())
        .unwrap_or("")
        .to_string();
    let payload: Value = serde_json::from_slice(&body)
        .map_err(|error| ApiError::invalid_request(format!("unparseable payload: {error}")))?;
    let repo_id = payload
        .pointer("/repository/id")
        .and_then(Value::as_i64)
        .unwrap_or(0);
    let repo_full = payload
        .pointer("/repository/full_name")
        .and_then(Value::as_str)
        .unwrap_or("")
        .to_string();
    let text = |pointer: &str| -> String {
        payload
            .pointer(pointer)
            .and_then(Value::as_str)
            .unwrap_or("")
            .to_string()
    };

    // What to record, per arm. An arm that does not match the event's own `action` is `ignored` and NOT an error: a forge that adds
    // an action must not start failing deliveries to an endpoint it cannot distinguish from a broken one.
    let mut record: Option<(concord_db::forge_events::Kind, i64, String, String, String)> = None;
    let mut detail = String::new();
    let mut status = "processed";
    match event.as_str() {
        "push" => {
            record = Some((
                concord_db::forge_events::Kind::Push,
                0,
                text("/pusher/login"),
                text("/commits/0/timestamp"),
                String::new(),
            ));
        }
        "pull_request" => match text("/action").as_str() {
            "opened" | "reopened" => {
                let merged = payload.pointer("/pull_request/merged").and_then(Value::as_bool);
                detail = format!(
                    "pr_number={} title={:?} status=open merged={}",
                    payload.pointer("/number").and_then(Value::as_i64).unwrap_or(0),
                    text("/pull_request/title"),
                    merged.is_some_and(|value| value),
                );
                record = Some((
                    concord_db::forge_events::Kind::PrOpened,
                    payload.pointer("/number").and_then(Value::as_i64).unwrap_or(0),
                    text("/pull_request/user/login"),
                    // `created_at`, not the receipt: a reopen reuses it, so the same PR does not acquire a second opening later.
                    text("/pull_request/created_at"),
                    detail.clone(),
                ));
            }
            "closed" => {
                let merged = payload
                    .pointer("/pull_request/merged")
                    .and_then(Value::as_bool)
                    .unwrap_or(false);
                detail = format!(
                    "pr_number={} title={:?} status={} merged={merged}",
                    payload.pointer("/number").and_then(Value::as_i64).unwrap_or(0),
                    text("/pull_request/title"),
                    if merged { "merged" } else { "rejected" },
                );
                record = Some((
                    concord_db::forge_events::Kind::PrClosed,
                    payload.pointer("/number").and_then(Value::as_i64).unwrap_or(0),
                    text("/pull_request/user/login"),
                    text("/pull_request/created_at"),
                    detail.clone(),
                ));
            }
            other => {
                status = "ignored";
                detail = format!("action={other}");
            }
        },
        "pull_request_review" => {
            if matches!(text("/action").as_str(), "submitted" | "created") {
                detail = format!(
                    "pr_number={} review_id={} reviewer={}",
                    payload
                        .pointer("/pull_request/number")
                        .and_then(Value::as_i64)
                        .unwrap_or(0),
                    payload.pointer("/review/id").and_then(Value::as_i64).unwrap_or(0),
                    text("/review/user/login"),
                );
                record = Some((
                    concord_db::forge_events::Kind::PrReview,
                    payload
                        .pointer("/pull_request/number")
                        .and_then(Value::as_i64)
                        .unwrap_or(0),
                    text("/review/user/login"),
                    text("/review/submitted_at"),
                    detail.clone(),
                ));
            } else {
                status = "ignored";
                detail = format!("action={}", text("/action"));
            }
        }
        "pull_request_comment" | "issue_comment" => {
            let issue_number = payload
                .pointer("/issue/number")
                .and_then(Value::as_i64)
                .unwrap_or(0);
            let pr_number = payload
                .pointer("/pull_request/number")
                .and_then(Value::as_i64)
                .unwrap_or(0);
            let is_pull = payload.pointer("/issue/pull_request").is_some() || pr_number != 0;
            if text("/action") != "created" {
                status = "ignored";
                detail = format!("action={}", text("/action"));
            } else if !is_pull {
                // A plain issue comment is not a response to a pull request; recording it would put a non-response into the
                // newcomer median.
                status = "ignored";
                detail = "reason=not a pull request".to_string();
            } else {
                let number = if pr_number != 0 { pr_number } else { issue_number };
                detail = format!(
                    "pr_number={number} comment_id={} author={}",
                    payload.pointer("/comment/id").and_then(Value::as_i64).unwrap_or(0),
                    text("/comment/user/login"),
                );
                record = Some((
                    concord_db::forge_events::Kind::PrComment,
                    number,
                    text("/comment/user/login"),
                    text("/comment/created_at"),
                    detail.clone(),
                ));
            }
        }
        _ => {
            // Unknown event type: acknowledged and dropped. A forge that adds an event must not start failing deliveries.
            return Ok((
                StatusCode::OK,
                Json(serde_json::json!({ "status": "ignored", "event": event })),
            ));
        }
    }

    let action = format!("forgejo_{event}");
    sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values (NULL, NULL, $1, 'repository', $2, $3, now())",
    )
    .bind(&action)
    .bind(repo_id)
    .bind(&detail)
    .execute(&state.pool)
    .await
    .map_err(internal)?;

    let mut stored = false;
    if let Some((kind, number, actor, at, _)) = record {
        // An unparseable timestamp writes NO event row: `now()` would invent a time, and the newcomer median is exactly the number
        // such a row would move. The audit row above still records that the delivery happened.
        let at = if app_mutant("webhook-timestamp-fallback") {
            // Control: an unreadable time falls back to the receipt time, which INVENTS a timestamp for an event that may have
            // happened a week ago — and the newcomer median is exactly the number such a row moves.
            Some(parse_rfc3339(&at).unwrap_or_else(|| {
                std::time::SystemTime::now()
                    .duration_since(std::time::UNIX_EPOCH)
                    .map_or(0.0, |elapsed| elapsed.as_secs_f64())
            }))
        } else {
            parse_rfc3339(&at)
        };
        if let Some(at) = at {
            stored = concord_db::forge_events::record(
                &state.pool,
                &repo_full,
                kind,
                number,
                &actor,
                at,
            )
            .await
            .map_err(internal)?;
        } else {
            tracing::warn!(repo = %repo_full, kind = kind.as_str(), "forge event without a parseable timestamp: recorded in the audit log only");
        }
    }
    Ok((
        StatusCode::OK,
        Json(serde_json::json!({ "status": status, "recorded": stored })),
    ))
}

/// HMAC-SHA256, written out because the crate that has it is the one this module avoids depending on for six lines of arithmetic.
fn hmac_sha256(key: &[u8], message: &[u8]) -> [u8; 32] {
    use sha2::Digest as _;
    const BLOCK: usize = 64;
    let mut key_block = [0u8; BLOCK];
    if key.len() > BLOCK {
        let digest = sha2::Sha256::digest(key);
        key_block[..32].copy_from_slice(&digest);
    } else {
        key_block[..key.len()].copy_from_slice(key);
    }
    let mut inner = sha2::Sha256::new();
    inner.update(key_block.map(|byte| byte ^ 0x36));
    inner.update(message);
    let inner = inner.finalize();
    let mut outer = sha2::Sha256::new();
    outer.update(key_block.map(|byte| byte ^ 0x5c));
    outer.update(inner);
    outer.finalize().into()
}

/// Lowercase hex, which is the spelling `X-Hub-Signature-256` uses.
fn hex(bytes: &[u8]) -> String {
    use std::fmt::Write as _;
    let mut out = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        let _ = write!(out, "{byte:02x}");
    }
    out
}

/// Compare without early exit.
///
/// `==` on byte slices stops at the first difference, and the time it took is measurable across enough attempts; a signature is the
/// only thing between a stranger and a table that moves a published median, so the comparison is written to leak nothing. The length
/// check is not a leak: the digest's length is public.
fn constant_time_eq(left: &[u8], right: &[u8]) -> bool {
    if left.len() != right.len() {
        return false;
    }
    let mut difference = 0u8;
    for (a, b) in left.iter().zip(right) {
        difference |= a ^ b;
    }
    difference == 0
}

/// RFC 3339 to Unix seconds, without a date library in the request path.
///
/// Returns `None` for anything it cannot read: the caller must be able to tell "no usable time" from "midnight, 1970".
fn parse_rfc3339(text: &str) -> Option<f64> {
    let text = text.trim();
    let (date, rest) = text.split_once('T').or_else(|| text.split_once(' '))?;
    let mut date_parts = date.split('-');
    let year: i64 = date_parts.next()?.parse().ok()?;
    let month: i64 = date_parts.next()?.parse().ok()?;
    let day: i64 = date_parts.next()?.parse().ok()?;
    if !(1..=12).contains(&month) || !(1..=31).contains(&day) {
        return None;
    }
    // Split the clock from the offset at the first `Z`, `+`, or the `-` that begins a negative offset. A `-` inside the clock cannot
    // occur, so the first one after the seconds is the offset's.
    let offset_at = rest
        .find(['Z', 'z', '+'])
        .or_else(|| rest.find('-'))
        .unwrap_or(rest.len());
    let (clock, offset) = rest.split_at(offset_at);
    let mut clock_parts = clock.split(':');
    let hour: i64 = clock_parts.next()?.parse().ok()?;
    let minute: i64 = clock_parts.next()?.parse().ok()?;
    let second: f64 = match clock_parts.next() {
        Some(raw) => raw.parse().ok()?,
        None => 0.0,
    };
    if !(0..=23).contains(&hour) || !(0..=59).contains(&minute) || !(0.0..=60.0).contains(&second) {
        return None;
    }
    let (sign, hours, minutes) = match offset.trim() {
        "" | "Z" | "z" => (0i64, 0i64, 0i64),
        other => {
            let sign = if other.starts_with('-') { -1 } else { 1 };
            let body = other.trim_start_matches(['+', '-']);
            let mut parts = body.split(':');
            let hours: i64 = parts.next()?.parse().ok()?;
            let minutes: i64 = parts.next().unwrap_or("0").parse().ok()?;
            (sign, hours, minutes)
        }
    };
    let days = days_from_civil(year, month, day);
    let local = days as f64 * 86_400.0 + hour as f64 * 3_600.0 + minute as f64 * 60.0 + second;
    Some(local - (sign * (hours * 3_600 + minutes * 60)) as f64)
}

/// Days since the Unix epoch for a civil date, by Howard Hinnant's algorithm — the day count without a calendar crate.
fn days_from_civil(year: i64, month: i64, day: i64) -> i64 {
    let year = if month <= 2 { year - 1 } else { year };
    let era = if year >= 0 { year } else { year - 399 } / 400;
    let year_of_era = year - era * 400;
    let day_of_year = (153 * (if month > 2 { month - 3 } else { month + 9 }) + 2) / 5 + day - 1;
    let day_of_era = year_of_era * 365 + year_of_era / 4 - year_of_era / 100 + day_of_year;
    era * 146_097 + day_of_era - 719_468
}

#[cfg(test)]
mod forge_time_tests {
    use super::{hmac_sha256, hex, parse_rfc3339};

    /// Hand-rolled date arithmetic has exactly one failure mode worth a test: an off-by-one in the civil-day count that is invisible
    /// in the middle of a month and wrong at the boundaries. These are fixed vectors, not round-trips through another parser —
    /// a parser that agreed with itself would pass a round-trip while being a day out.
    #[test]
    fn rfc3339_parses_known_instants() {
        let cases = [
            ("2026-11-01T10:00:00Z", 1_793_527_200.0),
            ("1970-01-01T00:00:00Z", 0.0),
            ("2026-11-01T10:00:00+00:00", 1_793_527_200.0),
            // One hour ahead of UTC means one hour less of Unix time.
            ("2026-11-01T11:00:00+01:00", 1_793_527_200.0),
            ("2026-11-01T05:00:00-05:00", 1_793_527_200.0),
            // A leap day, which is the case a naive `month * 31` gets wrong.
            ("2024-02-29T00:00:00Z", 1_709_164_800.0),
        ];
        for (text, expected) in cases {
            let got = parse_rfc3339(text).unwrap_or_else(|| panic!("{text} did not parse"));
            assert!((got - expected).abs() < 0.5, "{text}: got {got} want {expected}");
        }
    }

    /// Anything unreadable is `None`, never a plausible-looking zero: midnight 1970 in the newcomer median is a row that says every
    /// response took fifty-six years.
    #[test]
    fn rfc3339_refuses_what_it_cannot_read() {
        for text in ["", "not-a-date", "2026-13-01T00:00:00Z", "2026-11-01T25:00:00Z", "2026-11", "10:00:00"] {
            assert!(parse_rfc3339(text).is_none(), "{text:?} parsed but should not have");
        }
    }

    /// The published test vector from RFC 4231 case 2, because a hand-written HMAC is a hand-written bug unless it is checked
    /// against something that was written by someone else.
    #[test]
    fn hmac_matches_rfc4231_case_2() {
        let got = hex(&hmac_sha256(b"Jefe", b"what do ya want for nothing?"));
        assert_eq!(got, "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843");
    }

    #[test]
    fn hmac_keys_longer_than_a_block_are_hashed_first() {
        // RFC 4231 case 6: a 131-byte key. A naive implementation writes past the block or truncates silently.
        let key = vec![0xaa_u8; 131];
        let got = hex(&hmac_sha256(&key, b"Test Using Larger Than Block-Size Key - Hash Key First"));
        assert_eq!(got, "60e431591ee0b67f0d8a26aacbf5b77f8e0bc6213728c5140546040f0ee37f54");
    }
}



/// The webhook route, typed on `AppState` (D-18).
pub fn routes() -> Router<AppState> {
    Router::new().route("/api/v1/hooks/forgejo", axum::routing::post(forgejo_webhook))
}
