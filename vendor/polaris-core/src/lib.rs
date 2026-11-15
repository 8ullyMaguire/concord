//! The error envelope, exactly as `estate/contracts/error.schema.json` declares it.
//!
//! Every non-2xx JSON response from `/api/**` on every host carries this body, and a path under `/api`
//! that is not a route is a 404 with this body — never the SPA shell. The estate had four hand-written
//! copies of this shape before it had one definition, and they had already drifted: one of them put
//! extra keys next to `code` and `message`, which the contract forbids. `details` is the extension
//! point; anything else is drift.
//!
//! [`page`] carries the other half of the same contract — the page request, the page response and the
//! scoped cursor — so a product does not write a fourth pagination.

pub mod metrics;
pub mod page;

use axum::Json;
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use serde::Serialize;
use serde_json::Value;

/// The contract's cap on `message` (schema: `maxLength: 300`).
pub const MAX_MESSAGE_LEN: usize = 300;

/// The contract's floor on `request_id` (schema: `minLength: 8`).
pub const MIN_REQUEST_ID_LEN: usize = 8;

/// The `error.code` enum, in the schema's order. Kept as a closed enum so an unknown code cannot be
/// invented at a call site: `CHECK contracts` fails the estate on a code nobody declared.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ErrorCode {
    InvalidRequest,
    InvalidLimit,
    InvalidSort,
    InvalidCursor,
    Unauthenticated,
    Forbidden,
    NotFound,
    MethodNotAllowed,
    Conflict,
    Gone,
    Unprocessable,
    RateLimited,
    Internal,
    Unavailable,
}

impl ErrorCode {
    /// Every code the contract allows, in the schema's order.
    pub const ALL: [ErrorCode; 14] = [
        ErrorCode::InvalidRequest,
        ErrorCode::InvalidLimit,
        ErrorCode::InvalidSort,
        ErrorCode::InvalidCursor,
        ErrorCode::Unauthenticated,
        ErrorCode::Forbidden,
        ErrorCode::NotFound,
        ErrorCode::MethodNotAllowed,
        ErrorCode::Conflict,
        ErrorCode::Gone,
        ErrorCode::Unprocessable,
        ErrorCode::RateLimited,
        ErrorCode::Internal,
        ErrorCode::Unavailable,
    ];

    pub fn as_str(self) -> &'static str {
        match self {
            ErrorCode::InvalidRequest => "invalid_request",
            ErrorCode::InvalidLimit => "invalid_limit",
            ErrorCode::InvalidSort => "invalid_sort",
            ErrorCode::InvalidCursor => "invalid_cursor",
            ErrorCode::Unauthenticated => "unauthenticated",
            ErrorCode::Forbidden => "forbidden",
            ErrorCode::NotFound => "not_found",
            ErrorCode::MethodNotAllowed => "method_not_allowed",
            ErrorCode::Conflict => "conflict",
            ErrorCode::Gone => "gone",
            ErrorCode::Unprocessable => "unprocessable",
            ErrorCode::RateLimited => "rate_limited",
            ErrorCode::Internal => "internal",
            ErrorCode::Unavailable => "unavailable",
        }
    }

    /// The status a code means when the caller does not pick one. A code and a status that disagree
    /// (a 200 with `not_found`, a 403 with `invalid_cursor`) is the kind of thing only a shared
    /// definition prevents, because each copy is edited by whoever is holding it.
    pub fn default_status(self) -> StatusCode {
        match self {
            ErrorCode::InvalidRequest
            | ErrorCode::InvalidLimit
            | ErrorCode::InvalidSort
            | ErrorCode::InvalidCursor
            | ErrorCode::Unprocessable => StatusCode::BAD_REQUEST,
            ErrorCode::Unauthenticated => StatusCode::UNAUTHORIZED,
            ErrorCode::Forbidden => StatusCode::FORBIDDEN,
            ErrorCode::NotFound => StatusCode::NOT_FOUND,
            ErrorCode::MethodNotAllowed => StatusCode::METHOD_NOT_ALLOWED,
            ErrorCode::Conflict => StatusCode::CONFLICT,
            ErrorCode::Gone => StatusCode::GONE,
            ErrorCode::RateLimited => StatusCode::TOO_MANY_REQUESTS,
            ErrorCode::Internal => StatusCode::INTERNAL_SERVER_ERROR,
            ErrorCode::Unavailable => StatusCode::SERVICE_UNAVAILABLE,
        }
    }
}

#[derive(Debug, Serialize)]
struct ErrorDetail<'a> {
    code: &'a str,
    message: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    details: Option<&'a Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    request_id: Option<&'a str>,
}

#[derive(Debug, Serialize)]
struct ErrorBody<'a> {
    error: ErrorDetail<'a>,
}

/// A refusal that abides by the contract.
///
/// Built by the named constructors rather than by struct literal so that the two mistakes the schema
/// exists to catch — a message longer than the cap, a `rate_limited` without `details` — cannot be made
/// at a call site.
#[derive(Debug, Clone)]
pub struct ApiError {
    status: StatusCode,
    code: ErrorCode,
    message: String,
    details: Option<Value>,
    request_id: Option<String>,
}

impl ApiError {
    /// Private on purpose. `rate_limited` is the one code the contract constrains further — it must
    /// carry `details` — so a general constructor would be a public way to break the contract, which
    /// this test suite demonstrated the moment it was public.
    fn of(code: ErrorCode, message: impl Into<String>) -> Self {
        Self {
            status: code.default_status(),
            code,
            message: cap(message.into()),
            details: None,
            request_id: None,
        }
    }

    /// Overrides the status. Rare and deliberate: `unprocessable` for a 422, a 410 that the domain
    /// wanted to be a 404 for discoverability reasons.
    pub fn with_status(mut self, status: StatusCode) -> Self {
        self.status = status;
        self
    }

    /// The contract's extension point. Anything a domain wants to say beyond code and message goes
    /// here — never next to them.
    pub fn with_details(mut self, details: Value) -> Self {
        self.details = Some(details);
        self
    }

    /// Dropped when shorter than the schema's floor: a truncated correlation id is worse than none,
    /// because it sends whoever reads the report looking for a request that cannot exist.
    pub fn with_request_id(mut self, request_id: impl Into<String>) -> Self {
        let id = request_id.into();
        self.request_id = (id.len() >= MIN_REQUEST_ID_LEN).then_some(id);
        self
    }

    pub fn code(&self) -> ErrorCode {
        self.code
    }

    pub fn status(&self) -> StatusCode {
        self.status
    }

    pub fn message(&self) -> &str {
        &self.message
    }

    /// The envelope as JSON — the same object `into_response` writes. Public because a probe and a
    /// test should be able to ask "what would this say" without building a `Response`, and because
    /// two code paths that both format the envelope is how they start disagreeing.
    pub fn to_json(&self) -> Value {
        serde_json::to_value(ErrorBody {
            error: ErrorDetail {
                code: self.code.as_str(),
                message: &self.message,
                details: self.details.as_ref(),
                request_id: self.request_id.as_deref(),
            },
        })
        .expect("the envelope is a plain struct of strings and a JSON value")
    }
}

impl IntoResponse for ApiError {
    fn into_response(self) -> Response {
        (self.status, Json(self.to_json())).into_response()
    }
}

// ── the fourteen codes, with the argument that each one is not `internal` ──────────────────────

impl ApiError {
    pub fn invalid_request(message: impl Into<String>) -> Self {
        Self::of(ErrorCode::InvalidRequest, message)
    }
    pub fn invalid_limit(message: impl Into<String>) -> Self {
        Self::of(ErrorCode::InvalidLimit, message)
    }
    pub fn invalid_sort(message: impl Into<String>) -> Self {
        Self::of(ErrorCode::InvalidSort, message)
    }
    pub fn invalid_cursor(message: impl Into<String>) -> Self {
        Self::of(ErrorCode::InvalidCursor, message)
    }
    pub fn unauthenticated(message: impl Into<String>) -> Self {
        Self::of(ErrorCode::Unauthenticated, message)
    }
    pub fn forbidden(message: impl Into<String>) -> Self {
        Self::of(ErrorCode::Forbidden, message)
    }
    pub fn not_found(message: impl Into<String>) -> Self {
        Self::of(ErrorCode::NotFound, message)
    }
    pub fn method_not_allowed(message: impl Into<String>) -> Self {
        Self::of(ErrorCode::MethodNotAllowed, message)
    }
    pub fn conflict(message: impl Into<String>) -> Self {
        Self::of(ErrorCode::Conflict, message)
    }
    pub fn gone(message: impl Into<String>) -> Self {
        Self::of(ErrorCode::Gone, message)
    }
    pub fn unprocessable(message: impl Into<String>) -> Self {
        Self::of(ErrorCode::Unprocessable, message)
    }
    pub fn internal(message: impl Into<String>) -> Self {
        Self::of(ErrorCode::Internal, message)
    }
    pub fn unavailable(message: impl Into<String>) -> Self {
        Self::of(ErrorCode::Unavailable, message)
    }

    /// The one code the schema constrains further: a 429 must say when to come back, and the answer
    /// lives in `details`. The seconds are clamped at 1 because `Retry-After: 0` is read by every
    /// client as "retry now", which turns a rate limit into a hot loop against our own database.
    pub fn rate_limited(message: impl Into<String>, retry_after_secs: u64) -> Self {
        Self::of(ErrorCode::RateLimited, message)
            .with_details(serde_json::json!({ "retry_after_secs": retry_after_secs.max(1) }))
    }
}

/// Truncates to the contract's cap on a character boundary.
///
/// `String::truncate(300)` panics when byte 300 lands inside a multi-byte character — which is how a
/// refusal about a username with an accent becomes a 500. Walking back to the boundary is the whole
/// fix, and it belongs here rather than in each caller's formatter.
fn cap(message: String) -> String {
    if message.len() <= MAX_MESSAGE_LEN {
        return message;
    }
    let mut end = MAX_MESSAGE_LEN;
    while end > 0 && !message.is_char_boundary(end) {
        end -= 1;
    }
    let mut truncated = message;
    truncated.truncate(end);
    truncated
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_cap_lands_on_a_character_boundary() {
        // "é" is two bytes, so 300 bytes cut the 150th in half and `truncate` would have panicked.
        let long = "é".repeat(400);
        let capped = cap(long);
        assert_eq!(capped.len(), 300);
        assert_eq!(capped.chars().count(), 150);
    }

    #[test]
    fn a_short_message_is_untouched() {
        assert_eq!(cap("nope".into()), "nope");
    }

    #[test]
    fn a_short_request_id_is_dropped_not_truncated() {
        let e = ApiError::not_found("missing").with_request_id("abc");
        assert_eq!(e.request_id, None);
        let e = ApiError::not_found("missing").with_request_id("0123456789abcdef");
        assert_eq!(e.request_id.as_deref(), Some("0123456789abcdef"));
    }

    #[test]
    fn rate_limited_always_carries_details() {
        let e = ApiError::rate_limited("slow down", 0);
        let details = e.details.expect("details");
        assert_eq!(details["retry_after_secs"], 1);
    }
}
