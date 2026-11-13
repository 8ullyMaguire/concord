//! §8's comment threads: a reply tree on a feature, a complaint, a merge request or a project.
//!
//! Four guards in the Go store exist because the schema cannot provide them, and every one of them is a hole
//! that lets a comment be filed where it does not belong:
//!
//! * **`comments.thread_id` is a bare integer with no foreign key** — the one column in this table that
//!   references something and does not check it. A comment could name a feature in ANOTHER project and be
//!   filed under this one.
//! * **`parent_id` IS a real foreign key**, so it points at a real row — but not necessarily one in this
//!   thread. A reply whose parent sits on another feature would render nested under an unrelated comment.
//! * **a blank body is not a comment.** `body` is `NOT NULL` and nothing else, so `''` and `'   '` both
//!   insert, and a thread then carries a row that renders as nothing, which a reader cannot distinguish
//!   from a comment that failed to load.
//! * **an empty label must be NULL, not `''`.** `label` carries
//!   `CHECK (label IN ('question','objection','support','evidence','offtopic'))` and no `NOT NULL`, so `''`
//!   is not a legal value — it is the absence of one. Writing `''` made every unlabelled comment fail its
//!   own constraint, which is the ordinary case.

use serde::Serialize;
use sqlx::PgPool;

use crate::feature_writes::{add_audit, WriteError};

/// The thread kinds a comment can hang on.
pub const THREAD_KINDS: [&str; 5] =
    ["complaint", "feature", "merge_request", "release", "project"];

/// The labels §8's reader can filter by. Empty means none, and is stored as NULL.
pub const LABELS: [&str; 5] = ["question", "objection", "support", "evidence", "offtopic"];

/// One comment, as the Go struct serializes it.
#[derive(Debug, Clone, Serialize)]
pub struct Comment {
    /// The row.
    pub id: i64,
    /// The project it was filed under.
    pub project_id: i64,
    /// What kind of thing it hangs on.
    pub thread_kind: String,
    /// That thing's id.
    pub thread_id: i64,
    /// Who wrote it.
    pub author_id: i64,
    /// The comment it replies to, if any.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub parent_id: Option<i64>,
    /// Its text.
    pub body: String,
    /// `question`, `objection`, `support`, `evidence`, `offtopic`, or absent.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub label: String,
    /// Sum of the votes on it.
    pub score: f64,
    /// When it was written.
    pub created_at: f64,
}

/// A whole thread, oldest first, deleted comments excluded.
///
/// Oldest first rather than newest: a conversation reads downwards, and a reply that arrived before its
/// parent renders as a detached remark when the order is inverted.
pub async fn thread(
    pool: &PgPool,
    project_id: i64,
    thread_id: i64,
    thread_kind: &str,
) -> Result<Vec<Comment>, sqlx::Error> {
    use sqlx::Row;
    let rows = sqlx::query(
        "select id, project_id, thread_kind, thread_id, author_id, parent_id, body, label, score, \
                extract(epoch from created_at)::double precision \
           from comments \
          where project_id = $1 and thread_id = $2 and thread_kind = $3 and deleted_at is null \
          order by created_at asc",
    )
    .bind(project_id)
    .bind(thread_id)
    .bind(thread_kind)
    .fetch_all(pool)
    .await?;
    let mut out = Vec::with_capacity(rows.len());
    for row in rows {
        out.push(Comment {
            id: row.try_get(0)?,
            project_id: row.try_get(1)?,
            thread_kind: row.try_get(2)?,
            thread_id: row.try_get(3)?,
            author_id: row.try_get(4)?,
            parent_id: row.try_get(5)?,
            body: row.try_get(6)?,
            // NULL and '' are different failures, so the read accepts both and reports neither.
            label: row.try_get::<Option<String>, _>(7)?.unwrap_or_default(),
            score: row.try_get(8)?,
            created_at: row.try_get(9)?,
        });
    }
    Ok(out)
}

/// One comment, deleted or not — the read a permission decision needs.
pub async fn get(pool: &PgPool, comment_id: i64) -> Result<Option<Comment>, sqlx::Error> {
    use sqlx::Row;
    let row = sqlx::query(
        "select id, project_id, thread_kind, thread_id, author_id, parent_id, body, label, score, \
                extract(epoch from created_at)::double precision \
           from comments where id = $1",
    )
    .bind(comment_id)
    .fetch_optional(pool)
    .await?;
    let Some(row) = row else { return Ok(None) };
    Ok(Some(Comment {
        id: row.try_get(0)?,
        project_id: row.try_get(1)?,
        thread_kind: row.try_get(2)?,
        thread_id: row.try_get(3)?,
        author_id: row.try_get(4)?,
        parent_id: row.try_get(5)?,
        body: row.try_get(6)?,
        label: row.try_get::<Option<String>, _>(7)?.unwrap_or_default(),
        score: row.try_get(8)?,
        created_at: row.try_get(9)?,
    }))
}

/// Does this thread belong to this project?
///
/// The check the schema cannot make, because `comments.thread_id` has no foreign key. `release` is refused
/// rather than accepted: there is no releases table to comment on, and saying so is better than a thread
/// that can never be read.
pub async fn thread_belongs_to_project(
    pool: &PgPool,
    thread_kind: &str,
    thread_id: i64,
    project_id: i64,
) -> Result<(), WriteError> {
    let table = match thread_kind {
        "complaint" => "complaints",
        "feature" => "features",
        "merge_request" => "merge_requests",
        "project" => {
            if thread_id != project_id {
                return Err(WriteError::Invalid(format!(
                    "project {project_id} does not thread on itself; comment kind 'project' uses the \
                     project's own id, not {thread_id}"
                )));
            }
            return Ok(());
        }
        "release" => {
            return Err(WriteError::Invalid(
                "thread_kind 'release' has no releases table to comment on; comments on a release are not \
                 implemented"
                    .into(),
            ));
        }
        other => return Err(WriteError::Invalid(format!("invalid thread_kind {other:?}"))),
    };
    let owner: Option<i64> = sqlx::query_scalar(&format!(
        "select project_id from {table} where id = $1"
    ))
    .bind(thread_id)
    .fetch_optional(pool)
    .await?;
    match owner {
        None => Err(WriteError::NotFound(format!("{thread_kind} {thread_id}"))),
        Some(owner) if owner != project_id => Err(WriteError::Invalid(format!(
            "{thread_kind} {thread_id} belongs to project {owner}, not {project_id}"
        ))),
        Some(_) => Ok(()),
    }
}

/// Post a comment.
///
/// # Errors
/// [`WriteError::Invalid`] for an unknown kind or label, a blank body, a parent on another thread, or a
/// thread in another project; [`WriteError::NotFound`] when the thread or the parent does not exist.
#[allow(clippy::too_many_arguments)]
pub async fn create(
    pool: &PgPool,
    project_id: i64,
    thread_id: i64,
    thread_kind: &str,
    author_id: i64,
    body: &str,
    label: &str,
    parent_id: Option<i64>,
) -> Result<Comment, WriteError> {
    if !THREAD_KINDS.contains(&thread_kind) {
        return Err(WriteError::Invalid(format!(
            "invalid thread_kind {thread_kind:?}"
        )));
    }
    if !label.is_empty() && !LABELS.contains(&label) {
        return Err(WriteError::Invalid(format!("invalid label {label:?}")));
    }
    // `trim`, not a length check: a body of spaces is the same empty comment with more effort behind it.
    if body.trim().is_empty() {
        return Err(WriteError::Invalid("a comment needs a body".into()));
    }
    thread_belongs_to_project(pool, thread_kind, thread_id, project_id).await?;

    if let Some(parent_id) = parent_id {
        let parent: Option<(i64, String, i64)> = sqlx::query_as(
            "select thread_id, thread_kind, project_id from comments where id = $1",
        )
        .bind(parent_id)
        .fetch_optional(pool)
        .await?;
        let Some((parent_thread, parent_kind, parent_project)) = parent else {
            return Err(WriteError::NotFound(format!("parent comment {parent_id}")));
        };
        if parent_thread != thread_id || parent_kind != thread_kind || parent_project != project_id {
            return Err(WriteError::Invalid(format!(
                "parent comment {parent_id} is on {parent_kind}/{parent_thread} in project \
                 {parent_project}, not {thread_kind}/{thread_id} in project {project_id}"
            )));
        }
    }

    let id: i64 = sqlx::query_scalar(
        "insert into comments (project_id, thread_kind, thread_id, author_id, parent_id, body, label, \
                               score, created_at) \
         values ($1, $2, $3, $4, $5, $6, $7, 0, now()) returning id",
    )
    .bind(project_id)
    .bind(thread_kind)
    .bind(thread_id)
    .bind(author_id)
    .bind(parent_id)
    .bind(body)
    // `''` would violate the label CHECK; NULL is the absence of one.
    .bind(if label.is_empty() { None } else { Some(label) })
    .fetch_one(pool)
    .await?;
    get(pool, id)
        .await?
        .ok_or_else(|| WriteError::NotFound(format!("comment {id}")))
}

/// Soft-delete a comment: the author, or a maintainer of the project.
///
/// A delete that removed the row would take its replies with it through the parent foreign key, and a
/// conversation with a hole in it is harder to read than one with a tombstone.
pub async fn delete(
    pool: &PgPool,
    project_id: i64,
    comment_id: i64,
    actor_id: i64,
) -> Result<(), WriteError> {
    let Some(comment) = get(pool, comment_id).await? else {
        return Err(WriteError::NotFound(format!("comment {comment_id}")));
    };
    if comment.project_id != project_id {
        return Err(WriteError::NotFound(format!("comment {comment_id}")));
    }
    if comment.author_id != actor_id {
        let role: Option<String> = sqlx::query_scalar(
            "select role from members where project_id = $1 and user_id = $2",
        )
        .bind(project_id)
        .bind(actor_id)
        .fetch_optional(pool)
        .await?;
        if !matches!(role.as_deref(), Some("maintainer" | "owner")) {
            return Err(WriteError::Invalid(
                "only the author or a maintainer may delete a comment".into(),
            ));
        }
    }
    sqlx::query("update comments set deleted_at = now() where id = $1")
        .bind(comment_id)
        .execute(pool)
        .await?;
    add_audit(
        pool,
        project_id,
        actor_id,
        "comment.delete",
        "comment",
        comment_id,
        if comment.author_id == actor_id {
            "by its author"
        } else {
            "by a maintainer"
        },
    )
    .await?;
    Ok(())
}

/// Vote a comment up or down, and recompute its score in the same breath.
///
/// The upsert keeps one vote per person: a second vote changes the first rather than adding to it, so a
/// score cannot be inflated by clicking twice.
///
/// # Errors
/// [`WriteError::Invalid`] for a value that is not 1 or -1, and [`WriteError::NotFound`] for a comment that
/// does not exist in this project.
pub async fn vote(
    pool: &PgPool,
    project_id: i64,
    comment_id: i64,
    user_id: i64,
    value: i64,
) -> Result<f64, WriteError> {
    if value != 1 && value != -1 {
        return Err(WriteError::Invalid("vote value must be 1 or -1".into()));
    }
    let Some(comment) = get(pool, comment_id).await? else {
        return Err(WriteError::NotFound(format!("comment {comment_id}")));
    };
    if comment.project_id != project_id {
        return Err(WriteError::NotFound(format!("comment {comment_id}")));
    }
    let mut tx = pool.begin().await?;
    sqlx::query(
        "insert into comment_votes (comment_id, user_id, value) values ($1, $2, $3) \
         on conflict (comment_id, user_id) do update set value = excluded.value",
    )
    .bind(comment_id)
    .bind(user_id)
    .bind(value)
    .execute(&mut *tx)
    .await?;
    let score: f64 = sqlx::query_scalar(
        "update comments set score = (select coalesce(sum(value), 0) from comment_votes \
                                       where comment_id = $1) where id = $1 returning score",
    )
    .bind(comment_id)
    .fetch_one(&mut *tx)
    .await?;
    tx.commit().await?;
    Ok(score)
}
