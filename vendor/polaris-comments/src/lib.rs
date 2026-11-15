//! Thread rules for the estate's comment systems — depth, order, deletion, body bounds — and no storage.
//!
//! # Why a rules crate and not a service
//!
//! Two products have comments (measured 2026-11-09): suki and whitebois. They agree on the hard part — a self-referencing
//! `parent_id`, an index on `(thread, created_at)`, a tombstone or a cascade for moderation — and disagree on three things
//! that are policy, not correctness:
//!
//! | | suki | whitebois |
//! |---|---|---|
//! | depth | unbounded (`parent_id` only) | `CHECK (depth BETWEEN 0 AND 2)` |
//! | order | newest-last, `(thread_id, created_at)` | cached `score`, then time |
//! | deletion | `deleted_at`, tombstone kept | `ON DELETE CASCADE`, replies go with the parent |
//! | body | `NOT NULL` | `CHECK (length BETWEEN 1 AND 5000)` |
//!
//! Neither is better. A board that keeps tombstones is making a moderation statement; a board that caps depth at two is
//! making a readability statement. So the crate owns the rules and each site **declares its policy**; there is no boolean
//! that changes what the component does, only a `Policy` value the operator sets.
//!
//! Storage stays in the product: this crate never sees a connection, a row id type, or a clock. That is the same shape as
//! `polaris-ledger` (ADR 0008) and for the same reason.
//!
//! # The four refusals, and why each is a refusal rather than a repair
//!
//! * **Empty body** — a whitespace-only comment is a moderation artefact people publish by accident.
//! * **Body over the bound** — truncating loses the end of a sentence silently; refusing names the limit.
//! * **Reply below the cap** — auto-reparenting onto the capped node silently changes who answered whom.
//! * **Reply to a node that is not in this thread** — the classic cross-thread injection: a valid id from elsewhere.

use std::fmt;

/// How deep a thread may go.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum MaxDepth {
    /// suki: `parent_id` and nothing else. The ceiling is human patience.
    Unbounded,
    /// whitebois: `CHECK (depth BETWEEN 0 AND 2)`.
    Capped(u8),
}

/// What to do with a reply that would sit below the cap.
///
/// Measured 2026-11-09 in whitebois (`api.rs`, §7.7): a reply deeper than two is **flattened** — the parent stays truthful
/// and only the stored/rendered depth is capped — "so a client with a stale nesting depth can still post". A crate that
/// only knew how to refuse would have broken that on adoption, which is why this is a declared value and not a rule.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Overflow {
    /// suki's shape: there is no cap to overflow.
    Refuse,
    /// whitebois' shape: keep the parent, cap the depth at the ceiling.
    Flatten,
}

/// Which comment is shown first.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Ordering {
    /// Oldest first — a conversation read top to bottom (suki's `(thread_id, created_at)` index).
    Oldest,
    /// Newest first — a feed of replies.
    Newest,
    /// By cached score, descending — whitebois' `score` column.
    Score,
}

/// What a deleted comment leaves behind.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Deletion {
    /// The row stays with its body cleared, so replies keep their parent (suki's `deleted_at`).
    Tombstone,
    /// The row and its replies go (whitebois' `ON DELETE CASCADE`).
    Cascade,
}

/// Inclusive bounds on a comment body, in characters.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct BodyBounds {
    pub min: usize,
    pub max: usize,
}

impl BodyBounds {
    /// `NOT NULL` in suki, `length BETWEEN 1 AND 5000` in whitebois. Whitespace is not content either way.
    pub const DEFAULT: BodyBounds = BodyBounds { min: 1, max: 5000 };
}

/// A site's declared comment policy.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Policy {
    pub max_depth: MaxDepth,
    pub overflow: Overflow,
    pub ordering: Ordering,
    pub deletion: Deletion,
    pub body: BodyBounds,
}

impl Policy {
    /// suki: unbounded depth, oldest first, tombstones, and **2000** characters — measured from
    /// `services/comments.rs` (`MAX_BODY_CHARS`), not assumed from the crate default. A preset that disagrees with the
    /// site it is named after is worse than no preset.
    pub const SUKI: Policy = Policy {
        max_depth: MaxDepth::Unbounded,
        overflow: Overflow::Refuse,
        ordering: Ordering::Oldest,
        deletion: Deletion::Tombstone,
        body: BodyBounds { min: 1, max: 2000 },
    };

    /// whitebois: two levels, by score, hard delete.
    pub const WHITEBOIS: Policy = Policy {
        max_depth: MaxDepth::Capped(2),
        overflow: Overflow::Flatten,
        ordering: Ordering::Score,
        deletion: Deletion::Cascade,
        body: BodyBounds { min: 1, max: 5000 },
    };
}

/// Why a comment was refused. Each variant names what the caller has to change.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Refused {
    Empty,
    TooLong { max: usize, got: usize },
    TooDeep { cap: u8, parent_depth: u8 },
    ParentNotInThread { parent_id: i64 },
}

impl fmt::Display for Refused {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Refused::Empty => write!(f, "a comment needs at least one non-space character"),
            Refused::TooLong { max, got } => write!(f, "a comment may be at most {max} characters, this one is {got}"),
            Refused::TooDeep { cap, parent_depth } => {
                write!(f, "this thread stops at depth {cap}; that reply would sit at {}", parent_depth + 1)
            }
            Refused::ParentNotInThread { parent_id } => {
                write!(f, "parent {parent_id} is not a comment in this thread")
            }
        }
    }
}

impl std::error::Error for Refused {}

/// A comment as far as the rules are concerned: its identity is the product's business, its shape is not.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Comment {
    pub id: i64,
    pub parent_id: Option<i64>,
    pub depth: u8,
    pub body: String,
    pub created_at: i64,
    pub score: i64,
    pub deleted: bool,
}

/// Check a body against the policy. Refuses rather than trimming: a silent trim is the kind of edit a person notices
/// after publishing.
pub fn check_body(policy: &Policy, body: &str) -> Result<(), Refused> {
    let trimmed = body.trim();
    if trimmed.chars().count() < policy.body.min.max(1) {
        return Err(Refused::Empty);
    }
    let got = trimmed.chars().count();
    if got > policy.body.max {
        return Err(Refused::TooLong { max: policy.body.max, got });
    }
    Ok(())
}

/// Where a reply to `parent` would sit, or why it cannot.
///
/// `parent` is the parent's own depth and thread membership, already resolved by the product's query — the crate is given
/// the answer, not a connection.
pub fn place(policy: &Policy, parent: Option<Parent>) -> Result<u8, Refused> {
    let Some(parent) = parent else { return Ok(0) };
    if !parent.in_thread {
        return Err(Refused::ParentNotInThread { parent_id: parent.id });
    }
    let depth = parent.depth.saturating_add(1);
    if let MaxDepth::Capped(cap) = policy.max_depth {
        if depth > cap {
            return match policy.overflow {
                Overflow::Refuse => Err(Refused::TooDeep { cap, parent_depth: parent.depth }),
                // The parent stays as the client sent it; only the depth is capped, so a stale client can still post and
                // the stored tree does not claim a nesting the reader will never see.
                Overflow::Flatten => Ok(cap),
            };
        }
    }
    Ok(depth)
}

/// What the product's query has already established about the parent.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Parent {
    pub id: i64,
    pub depth: u8,
    pub in_thread: bool,
}

/// Order a page of comments. Ties are broken by id ascending in every mode, so two renders of one page agree — a page
/// whose order changes between requests is how a comment gets answered twice.
pub fn order(policy: &Policy, comments: &mut [Comment]) {
    comments.sort_by(|a, b| {
        let primary = match policy.ordering {
            Ordering::Oldest => a.created_at.cmp(&b.created_at),
            Ordering::Newest => b.created_at.cmp(&a.created_at),
            Ordering::Score => b.score.cmp(&a.score).then(a.created_at.cmp(&b.created_at)),
        };
        primary.then(a.id.cmp(&b.id))
    });
}

/// Apply the deletion policy to one comment, returning what the reader should be shown.
///
/// Under `Tombstone` the body is cleared and `deleted` is set: replies keep their parent, and the reader sees that
/// something was removed instead of a hole. Under `Cascade` the caller deletes the row — this returns `None`, because the
/// decision the crate can make is "show nothing", and the `ON DELETE CASCADE` in the product's schema does the rest.
pub fn after_delete(policy: &Policy, mut comment: Comment) -> Option<Comment> {
    match policy.deletion {
        Deletion::Tombstone => {
            comment.body.clear();
            comment.deleted = true;
            Some(comment)
        }
        Deletion::Cascade => None,
    }
}

/// Descendant count still reachable from a comment after a delete — the number a tombstone must keep alive.
///
/// `Cascade` returns the subtree size (those rows go with it, which is the point the reader loses); `Tombstone` returns
/// zero, because nothing is lost.
pub fn replies_lost(policy: &Policy, comments: &[Comment], id: i64) -> usize {
    if policy.deletion == Deletion::Tombstone {
        return 0;
    }
    let mut frontier = vec![id];
    let mut lost = 0;
    while let Some(parent) = frontier.pop() {
        for c in comments.iter().filter(|c| c.parent_id == Some(parent)) {
            lost += 1;
            frontier.push(c.id);
        }
    }
    lost
}
