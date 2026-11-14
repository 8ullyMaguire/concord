//! Forge events: what a repository did, as the forge reported it.
//!
//! §4.7's newcomer responsiveness rule reads these rows rather than the forge's API, because the forge's own timeline is a paged
//! remote call behind a token while this is one index scan. The table exists for that read; this module is its only writer.
//!
//! **The timestamp is the payload's, not the receipt's.** The unique key is `(repo_full, kind, number, actor, at)`, so a redelivery
//! of the same event collapses onto the row it already wrote. A receipt time would make every retry a new row, and a forge that
//! retried a delivery five times would outweigh one that did not — silently, because nothing about the row would look wrong.
//!
//! **A row with an unparseable timestamp is not written at all.** There is no safe substitute: `now()` is the receipt time and
//! invents an event that may have happened a week ago, and the newcomer median is exactly the number such a row would move. The
//! caller records the audit row regardless, so the delivery is not lost — only its untrustworthy timing.

use sqlx::PgPool;

/// What a repository did. The five kinds §4.7 measures.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Kind {
    /// A commit landed.
    Push,
    /// A pull request was opened (or reopened).
    PrOpened,
    /// A pull request was closed, merged or not.
    PrClosed,
    /// Someone reviewed a pull request.
    PrReview,
    /// Someone commented on a pull request.
    PrComment,
}

impl Kind {
    /// The stored spelling.
    #[must_use]
    pub fn as_str(self) -> &'static str {
        match self {
            Kind::Push => "push",
            Kind::PrOpened => "pr_opened",
            Kind::PrClosed => "pr_closed",
            Kind::PrReview => "pr_review",
            Kind::PrComment => "pr_comment",
        }
    }
}

/// Record one event, or nothing if it is already there.
///
/// Returns whether a row was written, which is the only way a caller can tell a first delivery from a redelivery without a second
/// query — and the webhook reports it, because "processed" on a duplicate is otherwise indistinguishable from work.
///
/// # Errors
/// `sqlx::Error`.
pub async fn record(
    pool: &PgPool,
    repo_full: &str,
    kind: Kind,
    number: i64,
    actor: &str,
    at: f64,
) -> Result<bool, sqlx::Error> {
    let inserted = sqlx::query(
        "insert into forge_events (repo_full, kind, number, actor, at, received_at)
         values ($1, $2, $3, $4, case when $6 then now() else to_timestamp($5::double precision) end, now())
         on conflict (repo_full, kind, number, actor, at) do nothing",
    )
    .bind(repo_full)
    .bind(kind.as_str())
    // A push has no number. Storing `0` rather than NULL keeps the unique key total: in a unique index NULL never equals NULL, so a
    // NULL number would let every duplicate push through as a new row.
    .bind(number)
    .bind(actor)
    .bind(at)
    // Control: the RECEIPT time instead of the payload's. A retried delivery then becomes a new row, and the forge that retried
    // outweighs one that did not — silently, because nothing about the row looks wrong.
    .bind(std::env::var("CONCORD_MUTANT").unwrap_or_default().split(',').any(|name| name.trim() == "webhook-receipt-time"))
    .execute(pool)
    .await?;
    Ok(inserted.rows_affected() > 0)
}
