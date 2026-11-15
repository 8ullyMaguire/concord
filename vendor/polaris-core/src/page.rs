//! Pagination: the page request, the page response, and an opaque *scoped* cursor.
//!
//! Ported from `commons/crates/commons-http` during the family review, because five products had written
//! this and none of them had the scoping check. The scoping is the part with teeth: a cursor minted for
//! one ordering, handed back with another, silently produces a list that is not the one the caller asked
//! for. See [`CursorError::WrongScope`].
//!
//! Nothing here knows about any domain type. A service supplies its own `T` for [`Page<T>`] and its own
//! scope string for a [`Cursor`], and that is the whole contract.

use serde::{Deserialize, Serialize};
use serde_json::Value;

/// A page request, as it arrives from a query string.
///
/// Both fields are optional because clients omit what they do not care about. The clamp is a method and
/// not a `Default`, so the two limits live in one place a service can read.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Pagination {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub limit: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cursor: Option<String>,
}

impl Pagination {
    /// The page size to use, clamped to `[1, max]`.
    ///
    /// Zero is refused rather than honoured: a client asking for zero items is almost always a bug (an
    /// unset integer), and answering with an empty page and a cursor that never advances is a loop.
    /// Negative values cannot arrive in a `u32`, so they are a deserialisation error, which is the right
    /// answer — a query string is not a place to silently reinterpret a number.
    pub fn page_size(&self, default: u32, max: u32) -> u32 {
        let asked = self.limit.unwrap_or(default);
        asked.clamp(1, max.max(1))
    }
}

/// A page response.
///
/// `next_cursor` is `None` on the last page, and that is the only signal a client needs: an empty
/// `items` with a cursor present means "nothing here, but the ordering is not exhausted", which is a
/// real state for a filtered feed and must not be confused with the end.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Page<T> {
    pub items: Vec<T>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub next_cursor: Option<String>,
}

impl<T> Page<T> {
    /// A page with no continuation, for a caller that has fetched everything.
    pub fn last(items: Vec<T>) -> Self {
        Self {
            items,
            next_cursor: None,
        }
    }

    /// A page that continues. `cursor` comes from [`Cursor::encode`].
    pub fn continuing(items: Vec<T>, cursor: Cursor) -> Self {
        Self {
            items,
            next_cursor: Some(cursor.encode()),
        }
    }

    /// Map the items without touching the cursor — the shape a handler needs when it converts rows to
    /// DTOs after fetching a page.
    pub fn map<U>(self, f: impl FnMut(T) -> U) -> Page<U> {
        Page {
            items: self.items.into_iter().map(f).collect(),
            next_cursor: self.next_cursor,
        }
    }
}

/// Why a cursor was refused.
///
/// Each variant is distinct because the caller's next move differs: a malformed cursor should re-read the
/// page from the start, a cursor from another scope should be told the scope changed rather than being fed
/// into a query that cannot honour it, and an expired one should start over.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum CursorError {
    /// Not base64url, or not the JSON this crate writes.
    Malformed { got: String },
    /// Valid, but minted for a different ordering/scope. `expected` is what this endpoint paginates.
    WrongScope { expected: String, got: String },
    /// Valid and scoped, but older than the server allows.
    Expired { scope: String },
}

impl std::fmt::Display for CursorError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            CursorError::Malformed { got } => write!(
                f,
                "cursor is not a cursor this service issued: {got:?}. Start again from the first page"
            ),
            CursorError::WrongScope { expected, got } => write!(
                f,
                "cursor was issued for {got:?} but this request paginates {expected:?}. A cursor \
                 cannot be moved between orderings: the position it encodes means nothing in \
                 another one"
            ),
            CursorError::Expired { scope } => write!(
                f,
                "cursor for {scope:?} is older than this service keeps; start again from the first page"
            ),
        }
    }
}

impl std::error::Error for CursorError {}

/// An opaque pagination position, safe to put in a query string and refused outside its scope.
///
/// The wire form is base64url of a small JSON object, with `=` padding stripped. Two properties are
/// deliberate:
///
/// * **URL-safe by construction.** `|`, `:`, `{`, `}`, space and `&` are not legal in a query value
///   without encoding, and a raw control character kills the request inside the HTTP stack with an error
///   that reads as a server fault (`InvalidUri`). Base64url's alphabet is `A-Z a-z 0-9 - _`.
/// * **Scoped.** `scope` is a string the caller chooses (`"new"`, `"hot:7d"`), and [`Cursor::decode`]
///   refuses a cursor whose scope is not the one being paginated. That check is the whole reason this
///   type exists: a client that reuses a cursor across sorts otherwise gets a wrong list, silently.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Cursor {
    scope: String,
    payload: Value,
}

#[derive(Serialize, Deserialize)]
struct CursorWire {
    s: String,
    p: Value,
}

impl Cursor {
    /// Mint a cursor for `scope` at `payload` (whatever the ordering needs to resume: a timestamp, an
    /// id, a tuple — this type does not care).
    pub fn new(scope: impl Into<String>, payload: impl Into<Value>) -> Self {
        Self {
            scope: scope.into(),
            payload: payload.into(),
        }
    }

    pub fn scope(&self) -> &str {
        &self.scope
    }

    pub fn payload(&self) -> &Value {
        &self.payload
    }

    /// The string to hand a client.
    pub fn encode(&self) -> String {
        use base64::Engine;
        use base64::engine::general_purpose::URL_SAFE_NO_PAD;
        let wire = CursorWire {
            s: self.scope.clone(),
            p: self.payload.clone(),
        };
        let json = serde_json::to_vec(&wire).expect("a cursor is serialisable");
        URL_SAFE_NO_PAD.encode(json)
    }

    /// Read a cursor that must belong to `expected_scope`.
    pub fn decode(raw: &str, expected_scope: &str) -> Result<Self, CursorError> {
        use base64::Engine;
        use base64::engine::general_purpose::URL_SAFE_NO_PAD;
        let bytes = URL_SAFE_NO_PAD
            .decode(raw.as_bytes())
            .map_err(|_| CursorError::Malformed {
                got: raw.to_string(),
            })?;
        let wire: CursorWire =
            serde_json::from_slice(&bytes).map_err(|_| CursorError::Malformed {
                got: raw.to_string(),
            })?;
        if wire.s != expected_scope {
            return Err(CursorError::WrongScope {
                expected: expected_scope.to_string(),
                got: wire.s,
            });
        }
        Ok(Self {
            scope: wire.s,
            payload: wire.p,
        })
    }

    /// The characters a cursor may contain, asserted by the tests and useful to a caller that has to
    /// validate one before it reaches a URL builder.
    pub fn is_url_safe(raw: &str) -> bool {
        raw.bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    /// A page size of zero is a client bug that would otherwise loop; the clamp answers with one item.
    #[test]
    fn page_size_is_clamped_at_both_ends() {
        let unset = Pagination {
            limit: None,
            cursor: None,
        };
        assert_eq!(unset.page_size(25, 100), 25);
        assert_eq!(
            Pagination {
                limit: Some(0),
                cursor: None
            }
            .page_size(25, 100),
            1
        );
        assert_eq!(
            Pagination {
                limit: Some(1000),
                cursor: None
            }
            .page_size(25, 100),
            100
        );
        assert_eq!(
            Pagination {
                limit: Some(50),
                cursor: None
            }
            .page_size(25, 100),
            50
        );
    }

    #[test]
    fn the_last_page_has_no_cursor_and_a_continuing_one_does() {
        let last: Page<u8> = Page::last(vec![1, 2]);
        assert!(last.next_cursor.is_none());
        let body = serde_json::to_string(&last).expect("serialisable");
        assert!(!body.contains("next_cursor"), "absent members are omitted");

        let continuing = Page::continuing(
            vec![1, 2],
            Cursor::new("new", json!({ "t": "2026-10-29T00:00:00Z", "id": 7 })),
        );
        assert!(continuing.next_cursor.is_some());
    }

    /// Map preserves the cursor: a handler that converts rows to DTOs must not silently end the
    /// pagination by dropping it.
    #[test]
    fn map_keeps_the_cursor() {
        let page = Page::continuing(vec![1u8], Cursor::new("new", json!(1)));
        let mapped = page.map(|v| v as u32 * 10);
        assert_eq!(mapped.items, vec![10u32]);
        assert!(mapped.next_cursor.is_some());
    }

    /// The round trip, and the one property that makes a cursor usable in a URL at all.
    #[test]
    fn a_cursor_round_trips_and_is_url_safe() {
        // Several payloads, because the alphabet a payload happens to produce decides whether an unsafe
        // encoder is caught: a short ASCII payload can encode without +, / or = and make a standard-base64
        // implementation look correct. Unicode bytes and a long id force all three.
        for payload in [
            json!({ "score": 41.5, "id": "a8f2" }),
            json!({ "t": "2026-10-29T00:00:00Z", "id": 7 }),
            json!({ "q": "Ünïcodé — 🎪", "n": 12345678901u64 }),
            json!({ "id": "x".repeat(120) }),
        ] {
            let cursor = Cursor::new("hot:7d", payload.clone());
            let raw = cursor.encode();
            assert!(
                Cursor::is_url_safe(&raw),
                "a cursor must survive a query string untouched, got {raw} for {payload}"
            );
            let back = Cursor::decode(&raw, "hot:7d").expect("decode");
            assert_eq!(back.payload(), &payload);
        }
    }

    /// THE SCOPE CHECK. A cursor minted for `new` handed to `hot` must be refused, because the position it
    /// encodes is a timestamp in one ordering and a score in the other — the query would run and return a
    /// list that is not what the caller asked for, with nothing to indicate it.
    #[test]
    fn a_cursor_from_another_ordering_is_refused_by_name() {
        let raw = Cursor::new("new", json!({ "id": 7 })).encode();
        match Cursor::decode(&raw, "hot") {
            Err(CursorError::WrongScope { expected, got }) => {
                assert_eq!(expected, "hot");
                assert_eq!(got, "new");
            }
            other => panic!("expected WrongScope, got {other:?}"),
        }
    }

    /// Garbage is a Malformed, never a panic and never an accidental `Ok`.
    #[test]
    fn garbage_is_malformed() {
        for raw in ["", "!!!!", "not-base64", "eyJ4IjoxfQ"] {
            assert!(
                matches!(
                    Cursor::decode(raw, "new"),
                    Err(CursorError::Malformed { .. })
                ),
                "{raw:?} was accepted"
            );
        }
    }

    /// The message a caller sees names what went wrong. A 400 that says "invalid cursor" makes a
    /// client-author do nothing; one that says the scope changed tells them to re-read.
    #[test]
    fn the_refusal_says_which_scope_was_expected() {
        let raw = Cursor::new("new", json!(1)).encode();
        let err = Cursor::decode(&raw, "trending").expect_err("wrong scope");
        let text = err.to_string();
        assert!(text.contains("trending") && text.contains("new"), "{text}");
    }
}
