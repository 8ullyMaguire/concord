//! The rules an audit log and its public projection must keep.
//!
//! Two products already keep ledgers and both got something important right that the other had not:
//!
//! **darkhold** keeps `audit_log` — the instance's own record, holding the acting account — and publishes the
//! modlog, which is a *different object*: the account id never appears, only the actor's pseudonym. A panel
//! decision is published with no actor at all, because metamoderation votes are anonymised — "the actor is the jury
//! panel, not the voter" — and a null there would read as "unknown", which is a different claim. Its pages are
//! ordered by the composite `(created_at, id)`, so two entries written in the same millisecond cannot make page two
//! repeat a row from page one.
//!
//! **concord** keeps `emergency_holds`, where a maintainer's power is bounded by construction: the reason is
//! `NOT NULL`, the grounds are a `CHECK`, and a `BEFORE INSERT` trigger aborts any row whose `expires_at` is not
//! after its `created_at`. An indefinite hold cannot exist as a row.
//!
//! Neither is better overall: each is better at what the other lacks. So this crate owns no table, no query and no
//! database handle — each product keeps the store and the client it has — and owns only the part that must not
//! drift between them:
//!
//! - the private record and the public projection are different types, and [`project`] is the only way to a
//!   publishable row;
//! - anonymisation is a declared rule per action ([`Redaction`]), never a boolean that changes what an entry means;
//! - an act that removes standing carries a reason, and a bounded power carries its expiry ([`Rules`]);
//! - pagination over an append-only log uses [`Cursor`], which is strictly-before on `(at, id)`.

use std::fmt;

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};

/// Who acted.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum Actor {
    /// A person.
    ///
    /// `account` is `None` when the reader never selected it — darkhold's modlog query reads only the pseudonym
    /// column, which is the strongest form of "do not publish the account": the value is not in memory to leak.
    /// An adopter in that position must not have to invent an empty string and mean "not read".
    Person {
        account: Option<String>,
        public: String,
    },
    /// A jury panel. Anonymised by construction: metamoderation votes have no single actor, and publishing an
    /// arbitrary member would be a lie about who decided.
    Panel,
    /// The instance itself (a timer, a migration, a rule).
    System,
}

/// What the act was done to.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum Subject {
    /// An account, identified privately and published as its public face.
    Account { account: String, public: String },
    /// A thing: a work, a post, a proposal. `kind` is the product's vocabulary; this crate does not police it.
    Object { kind: String, id: String },
}

/// How the actor of one kind of action may be published. Declared per action by the product, as data.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Redaction {
    /// Publish the acting person's handle. The site has decided that moderation is attributable.
    PublishHandle,
    /// Publish no actor, and mark the entry as a panel decision.
    PanelAnonymised,
    /// Publish no actor at all — the most an instance may say is that something happened.
    WithholdActor,
}

/// Whether an act must state why.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Reason {
    Required,
    Optional,
}

/// Whether an act is a bounded power.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Expiry {
    /// The act stands until it is reversed (a deletion, a ban with no end date).
    Unbounded,
    /// The act must carry an end (an emergency hold, a temporary suspension).
    Required,
}

/// The rules a product declares for one act: the two universal ones it can vary.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Rules {
    pub reason: Reason,
    pub expiry: Expiry,
}

impl Rules {
    /// The shape of a power that must be justified and must end — concord's emergency hold.
    pub const BOUNDED: Rules = Rules {
        reason: Reason::Required,
        expiry: Expiry::Required,
    };

    /// The shape of a permanent moderation decision — darkhold's sanctions and identity changes.
    pub const PERMANENT: Rules = Rules {
        reason: Reason::Required,
        expiry: Expiry::Unbounded,
    };

    /// The shape of a housekeeping act nobody needs to justify (a timer marking rows stale).
    pub const HOUSEKEEPING: Rules = Rules {
        reason: Reason::Optional,
        expiry: Expiry::Unbounded,
    };
}

/// The private record. Never serialised to a public surface.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Act {
    pub action: String,
    pub subject: Subject,
    pub actor: Actor,
    pub reason: Option<String>,
    pub expires_at: Option<DateTime<Utc>>,
    pub at: DateTime<Utc>,
}

/// What a public surface may show. The only type with `Serialize`, so "publish the private record by accident"
/// is a compile error rather than a review finding.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Projected {
    pub action: String,
    pub subject: String,
    /// The subject's kind, in the product's own vocabulary (`work`, `account`, `proposal`). The crate does not
    /// police it, but a public row that cannot say what it acted on is a row a reader cannot check.
    pub subject_kind: Option<String>,
    /// True when `subject` is a resolved public face rather than the stored identifier. darkhold needs this: an
    /// account whose pseudonyms are all gone is published as an empty subject, and the reader must be able to tell
    /// that apart from "the id is empty".
    pub subject_resolved: bool,
    pub actor: Option<String>,
    /// True when a panel decided. Distinct from `actor: None`, which is an anonymised or withheld actor.
    pub panel: bool,
    pub reason: Option<String>,
    pub at: DateTime<Utc>,
}

/// What is wrong with an act. Each variant names the rule, so a caller can surface it rather than log it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Defect {
    /// The act removes standing and states no reason.
    ReasonRequired,
    /// A bounded power with no end, or one that has already ended.
    ExpiryInvalid,
    /// A bounded power declared as bounded but carrying no expiry.
    ExpiryRequired,
}

impl fmt::Display for Defect {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let text = match self {
            Defect::ReasonRequired => "this act requires a reason, and none was given",
            Defect::ExpiryInvalid => "this act's expiry is not after the moment it was taken",
            Defect::ExpiryRequired => "this act is a bounded power and carries no expiry",
        };
        f.write_str(text)
    }
}

impl std::error::Error for Defect {}

/// An instant from unix seconds and nanoseconds.
///
/// The adopter's date library is not this crate's business: darkhold's `crates/db` uses `time`, concord stores epoch
/// REALs, and a ledger that insists on `chrono` at its boundary makes every adopter convert through a second date
/// library. Both of those expose (seconds, nanos) natively, so that pair is the boundary instead of a type.
pub fn from_unix(secs: i64, nanos: u32) -> DateTime<Utc> {
    DateTime::from_timestamp(secs, nanos).unwrap_or_else(|| DateTime::from_timestamp(0, 0).expect("epoch"))
}

/// Prove an act against the rules a product declared. The store may enforce the same thing (concord's trigger
/// does); this is what stops a second client disagreeing with the store.
pub fn validate(act: &Act, rules: Rules) -> Result<(), Defect> {
    if rules.reason == Reason::Required {
        match act.reason.as_deref().map(str::trim) {
            Some(reason) if !reason.is_empty() => {}
            _ => return Err(Defect::ReasonRequired),
        }
    }
    match (rules.expiry, act.expires_at) {
        (Expiry::Required, None) => Err(Defect::ExpiryRequired),
        (_, Some(expires_at)) if expires_at <= act.at => Err(Defect::ExpiryInvalid),
        _ => Ok(()),
    }
}

/// The public projection of an act.
///
/// A panel is anonymised whatever the rule says: there is no single person to name. A withheld actor stays
/// withheld even when the rule would publish a handle — the stricter of the two wins, because the failure mode of
/// getting this wrong is publishing a name.
pub fn project(act: &Act, rule: Redaction) -> Projected {
    let (actor, panel) = match (&act.actor, rule) {
        (Actor::Panel, _) => (None, true),
        (Actor::Person { public, .. }, Redaction::PublishHandle) => (Some(public.clone()), false),
        (Actor::Person { .. }, _) | (Actor::System, _) => (None, false),
    };
    let (subject, subject_kind, subject_resolved) = match &act.subject {
        Subject::Account { public, .. } => (public.clone(), Some("account".to_string()), true),
        // The id, not `kind/id`: a combined string is a presentation choice, and a product that publishes a
        // path and one that publishes a bare id would both be "using the crate" while disagreeing about the row.
        Subject::Object { kind, id } => (id.clone(), Some(kind.clone()), false),
    };
    Projected {
        action: act.action.clone(),
        subject,
        subject_kind,
        subject_resolved,
        actor,
        panel,
        reason: act.reason.clone(),
        at: act.at,
    }
}

/// A position in an append-only log: `(at, id)`, compared strictly.
///
/// `at` alone is not a cursor. Two entries can share a millisecond, and a `< at` comparison then either repeats a
/// row on the next page or skips one — and a log that skips a row is not an audit.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Cursor {
    pub at: DateTime<Utc>,
    pub id: String,
}

impl Cursor {
    pub fn new(at: DateTime<Utc>, id: impl Into<String>) -> Self {
        Self { at, id: id.into() }
    }

    /// Strictly before `other` in newest-first ordering: earlier time, or the same time and a smaller id.
    pub fn before(&self, other: &Cursor) -> bool {
        (self.at, &self.id) < (other.at, &other.id)
    }

    /// The wire form: RFC 3339, a `|`, then the id. Ids are opaque strings that must not contain `|`, and
    /// [`decode`] refuses rather than truncating at the first one.
    pub fn encode(&self) -> String {
        format!("{}|{}", self.at.to_rfc3339(), self.id)
    }

    /// Parse what [`encode`] produced. A malformed cursor is an error, never a silent first page — a silent first
    /// page turns a paging bug into an infinite scroll.
    pub fn decode(raw: &str) -> Result<Self, CursorError> {
        let (at, id) = raw.split_once('|').ok_or(CursorError::Malformed)?;
        if id.is_empty() {
            return Err(CursorError::Malformed);
        }
        let at = DateTime::parse_from_rfc3339(at)
            .map_err(|_| CursorError::Malformed)?
            .with_timezone(&Utc);
        Ok(Self::new(at, id))
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum CursorError {
    Malformed,
}

impl fmt::Display for CursorError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("this cursor is not a position in the log")
    }
}

impl std::error::Error for CursorError {}
