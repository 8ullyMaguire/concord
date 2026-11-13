//! The inbox: the two things a moderator's decision owes the person it happened to.
//!
//! §7's rule, in one line: **a moderation act that removes somebody's work tells them why.** The
//! notification row is written inside the same transaction as the act, so an act without its notice is
//! not a state the database can hold.
//!
//! Two formats matter as much as the query:
//!
//! * `project_slug` and `actor` are `omitempty` in the Go struct, so a missing project renders no key at
//!   all rather than an empty string. A page that checked `if slug` would behave differently against the
//!   two services if this drifted.
//! * `read` is `read_at IS NOT NULL`, not a boolean column: the timestamp is the record of *when* the
//!   reader saw it, and a flag would throw that away for a value the query can compute.
//!
//! The rule that makes this safe: **the user id is in the WHERE clause, never a check afterwards.**
//! "Not found" and "not yours" are the same answer, because a distinct one would confirm that the id is
//! real — a fact about another person.

use serde::Serialize;
use sqlx::PgPool;

/// One row of the reader's inbox.
#[derive(Debug, Clone, Serialize)]
pub struct Notification {
    /// The row.
    pub id: i64,
    /// `feature_removed` or `complaint_rejected`.
    pub kind: String,
    /// The project it happened in.
    pub project_id: i64,
    /// Its slug, so a client can link without a second read. Omitted when the project is gone.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub project_slug: String,
    /// The feature or complaint.
    pub subject_id: i64,
    /// What was decided.
    pub title: String,
    /// Why, in the moderator's own words.
    pub reason: String,
    /// Who decided. Omitted when the account is gone, and the page renders "a moderator".
    #[serde(skip_serializing_if = "String::is_empty")]
    pub actor: String,
    /// When the notice was written.
    pub created_at: f64,
    /// Whether the reader has seen it.
    pub read: bool,
}

/// One inbox page, plus the badge.
#[derive(Debug, Clone, Serialize)]
pub struct Inbox {
    /// The reader's rows, newest first.
    pub notifications: Vec<Notification>,
    /// How many of the reader's rows are unread — over the whole inbox, not this page.
    pub unread: i64,
}

/// The reader's inbox, newest first, with the unread count over all of it.
///
/// The limit clamps to 50 rather than refusing a bad value: the header asks for a page on every render, and
/// a 400 there would make a broken numeral take the whole page down.
pub async fn list(pool: &PgPool, user_id: i64, limit: i64) -> Result<Inbox, sqlx::Error> {
    use sqlx::Row;
    let limit = if limit <= 0 || limit > 200 { 50 } else { limit };
    let rows = sqlx::query(
        "select n.id, n.kind, n.project_id, coalesce(p.slug, ''), n.subject_id, n.title, n.reason, \
                coalesce(u.username, ''), extract(epoch from n.created_at)::double precision, \
                n.read_at is not null \
           from notifications n \
           left join projects p on p.id = n.project_id \
           left join users u on u.id = n.actor_id \
          where n.user_id = $1 \
          order by n.created_at desc, n.id desc limit $2",
    )
    .bind(user_id)
    .bind(limit)
    .fetch_all(pool)
    .await?;
    let mut out = Vec::with_capacity(rows.len());
    for row in rows {
        out.push(Notification {
            id: row.try_get(0)?,
            kind: row.try_get(1)?,
            project_id: row.try_get(2)?,
            project_slug: row.try_get(3)?,
            subject_id: row.try_get(4)?,
            title: row.try_get(5)?,
            reason: row.try_get(6)?,
            actor: row.try_get(7)?,
            created_at: row.try_get(8)?,
            read: row.try_get(9)?,
        });
    }
    let unread = count_unread(pool, user_id).await?;
    Ok(Inbox { notifications: out, unread })
}

/// The badge, without reading the inbox.
///
/// Separate from [`list`] because the header calls it on every page render, and a page that paid for fifty
/// rows and two joins to draw one numeral would make the badge the most expensive thing on the site.
pub async fn count_unread(pool: &PgPool, user_id: i64) -> Result<i64, sqlx::Error> {
    sqlx::query_scalar("select count(*) from notifications where user_id = $1 and read_at is null")
        .bind(user_id)
        .fetch_one(pool)
        .await
}

/// Mark one of the reader's own notifications read.
///
/// Idempotent: `coalesce(read_at, now())` keeps the first timestamp, and updating an already-read row still
/// matches it, so a control clicked twice answers 200 both times. A control that fails the second time is a
/// control the reader learns to distrust.
///
/// # Errors
/// [`sqlx::Error::RowNotFound`] when the row does not exist **or belongs to somebody else** — deliberately
/// the same answer, because a different one would confirm that the id is real.
pub async fn mark_read(pool: &PgPool, user_id: i64, notification_id: i64) -> Result<(), sqlx::Error> {
    let changed = sqlx::query(
        "update notifications set read_at = coalesce(read_at, now()) where id = $1 and user_id = $2",
    )
    .bind(notification_id)
    .bind(user_id)
    .execute(pool)
    .await?
    .rows_affected();
    if changed == 0 {
        return Err(sqlx::Error::RowNotFound);
    }
    Ok(())
}

/// Clear the reader's badge, and say how many rows that took.
pub async fn mark_all_read(pool: &PgPool, user_id: i64) -> Result<i64, sqlx::Error> {
    let changed = sqlx::query(
        "update notifications set read_at = now() where user_id = $1 and read_at is null",
    )
    .bind(user_id)
    .execute(pool)
    .await?
    .rows_affected();
    Ok(i64::try_from(changed).unwrap_or(0))
}
