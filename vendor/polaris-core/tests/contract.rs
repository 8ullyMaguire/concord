//! The crate is checked against the *contract*, not against a hand-written expectation.
//!
//! `fixtures/error.schema.json` is a byte copy of `estate/contracts/error.schema.json`; the estate's gate
//! 19 compares the two, so a change to the contract that nobody carried into this crate fails the estate
//! rather than surfacing as a client bug six weeks later.

use jsonschema::validator_for;
use polaris_core::{ApiError, ErrorCode, MAX_MESSAGE_LEN, MIN_REQUEST_ID_LEN};
use serde_json::{Value, json};

const SCHEMA: &str = include_str!("fixtures/error.schema.json");

fn validator() -> jsonschema::Validator {
    let schema: Value = serde_json::from_str(SCHEMA).expect("the contract is valid JSON");
    validator_for(&schema).expect("the contract is a valid JSON Schema")
}

fn assert_conforms(body: &Value) {
    let v = validator();
    if let Err(err) = v.validate(body) {
        panic!("body does not conform to the contract: {err}\nbody: {body}");
    }
}

/// One instance per code, built through the crate's public surface — which is the point: `rate_limited`
/// has no general constructor, so the only way to make one is with details.
fn instance(code: ErrorCode) -> ApiError {
    match code {
        ErrorCode::InvalidRequest => ApiError::invalid_request("m"),
        ErrorCode::InvalidLimit => ApiError::invalid_limit("m"),
        ErrorCode::InvalidSort => ApiError::invalid_sort("m"),
        ErrorCode::InvalidCursor => ApiError::invalid_cursor("m"),
        ErrorCode::Unauthenticated => ApiError::unauthenticated("m"),
        ErrorCode::Forbidden => ApiError::forbidden("m"),
        ErrorCode::NotFound => ApiError::not_found("m"),
        ErrorCode::MethodNotAllowed => ApiError::method_not_allowed("m"),
        ErrorCode::Conflict => ApiError::conflict("m"),
        ErrorCode::Gone => ApiError::gone("m"),
        ErrorCode::Unprocessable => ApiError::unprocessable("m"),
        ErrorCode::RateLimited => ApiError::rate_limited("m", 5),
        ErrorCode::Internal => ApiError::internal("m"),
        ErrorCode::Unavailable => ApiError::unavailable("m"),
    }
}

#[test]
fn every_code_produces_a_conforming_envelope() {
    for code in ErrorCode::ALL {
        let body = instance(code).to_json();
        assert_conforms(&body);
        assert_eq!(body["error"]["code"], code.as_str());
        assert_eq!(body["error"]["message"], "m");
        // The two keys the envelope may add only when they have something to say. `rate_limited` is
        // the exception the contract itself demands.
        if code != ErrorCode::RateLimited {
            assert!(
                body["error"].get("details").is_none(),
                "{code:?} invented details"
            );
        }
        assert!(
            body["error"].get("request_id").is_none(),
            "{code:?} invented a request id"
        );
    }
}

#[test]
fn the_status_each_code_defaults_to_is_the_one_a_client_expects() {
    use ErrorCode::*;
    let expected = [
        (InvalidRequest, 400),
        (InvalidLimit, 400),
        (InvalidSort, 400),
        (InvalidCursor, 400),
        (Unauthenticated, 401),
        (Forbidden, 403),
        (NotFound, 404),
        (MethodNotAllowed, 405),
        (Conflict, 409),
        (Gone, 410),
        (Unprocessable, 400),
        (RateLimited, 429),
        (Internal, 500),
        (Unavailable, 503),
    ];
    assert_eq!(expected.len(), ErrorCode::ALL.len());
    for (code, status) in expected {
        assert_eq!(instance(code).status().as_u16(), status, "{code:?}");
    }
}

#[test]
fn a_rate_limit_always_says_when_to_come_back() {
    let body = ApiError::rate_limited("slow down", 30).to_json();
    assert_conforms(&body);
    assert_eq!(body["error"]["details"]["retry_after_secs"], 30);
}

#[test]
fn domain_detail_rides_in_details_and_never_beside_code() {
    let body = ApiError::forbidden("no")
        .with_details(json!({ "binding": "disk", "waitlist_pos": 3 }))
        .to_json();
    assert_conforms(&body);
    assert_eq!(body["error"]["details"]["binding"], "disk");
    assert!(body["error"].get("waitlist_pos").is_none());
}

#[test]
fn an_overlong_message_is_capped_and_still_conforms() {
    let body = ApiError::invalid_request("é".repeat(1000)).to_json();
    assert_conforms(&body);
    let msg = body["error"]["message"].as_str().expect("a string");
    assert!(msg.len() <= MAX_MESSAGE_LEN);
}

#[test]
fn a_request_id_shorter_than_the_floor_is_omitted() {
    let short = ApiError::internal("boom").with_request_id("abc").to_json();
    assert_conforms(&short);
    assert!(short["error"].get("request_id").is_none());

    let long = ApiError::internal("boom")
        .with_request_id("9f2c4b1e8a7d")
        .to_json();
    assert_conforms(&long);
    assert_eq!(long["error"]["request_id"], "9f2c4b1e8a7d");
    assert!(long["error"]["request_id"].as_str().unwrap().len() >= MIN_REQUEST_ID_LEN);
}

#[test]
fn the_contracts_code_list_and_the_crates_are_the_same_list() {
    let schema: Value = serde_json::from_str(SCHEMA).expect("json");
    let declared: Vec<&str> = schema["properties"]["error"]["properties"]["code"]["enum"]
        .as_array()
        .expect("an enum")
        .iter()
        .map(|v| v.as_str().expect("strings"))
        .collect();
    let ours: Vec<&str> = ErrorCode::ALL.iter().map(|c| c.as_str()).collect();
    assert_eq!(
        declared, ours,
        "the contract and the crate disagree on the codes"
    );
}

#[test]
fn the_contract_is_strict_so_a_conforming_body_is_worth_something() {
    // If this passes, the validation above is decoration: an extra key would be allowed.
    let v = validator();
    let too_much = json!({ "error": { "code": "not_found", "message": "x", "extra": true } });
    assert!(v.validate(&too_much).is_err());

    let no_message = json!({ "error": { "code": "not_found" } });
    assert!(v.validate(&no_message).is_err());

    let unknown_code = json!({ "error": { "code": "teapot", "message": "x" } });
    assert!(v.validate(&unknown_code).is_err());
}
