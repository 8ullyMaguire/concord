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

use polaris_comments::{BodyBounds, Deletion, MaxDepth, Ordering, Overflow, Policy, Refused};

use crate::feature_writes::{add_audit, WriteError};

/// Concord's declared thread policy, and the evidence for each field.
///
/// * **depth unbounded**, because `comments` has no depth column at all — the same shape as suki, and the reason
///   `place()` is called with a depth the store computes by walking `parent_id` rather than reading one.
/// * **overflow refused**, which is unreachable at unbounded depth: it is the honest value, not a guess.
/// * **oldest first**, because the Go read is `ORDER BY created_at ASC` and a conversation reads downwards.
/// * **tombstone**, because the Go delete sets `deleted_at` and the read filters it — a delete that removed the
///   row would take its replies with it through `parent_id`.
/// * **no body ceiling**, because the product has never had one: `body` is `TEXT NOT NULL` and nothing else. The
///   bound is the largest the crate accepts rather than an invented number, since a cap would be a new product
///   rule and this row is a port.
pub const POLICY: Policy = Policy {
    max_depth: MaxDepth::Unbounded,
    overflow: Overflow::Refuse,
    ordering: Ordering::Oldest,
    deletion: Deletion::Tombstone,
    body: BodyBounds {
        min: 1,
        max: usize::MAX,
    },
};

/// The product's sentence for a refusal.
///
/// The crate decides *whether*; the product keeps saying *what it says today*. That split is deliberate: adopting
/// a rule must not silently rewrite the text a person reads, and the port's job is to change who owns the rule,
/// not what the API answers. Every arm names the crate's own words in a comment so the mapping cannot drift.
fn refusal(refused: Refused) -> WriteError {
    match refused {
        // "a comment needs at least one non-space character" -> the Go store's sentence, unchanged.
        Refused::Empty => WriteError::Invalid("a comment needs a body".into()),
        // Unreachable at `usize::MAX`; if the policy ever gains a ceiling, this is the sentence it should produce.
        Refused::TooLong { max, got } => WriteError::Invalid(format!(
            "a comment may be at most {max} characters, this one is {got}"
        )),
        // "this thread stops at depth {cap}; that reply would sit at {n}" -- unreachable while unbounded.
        Refused::TooDeep { cap, parent_depth } => WriteError::Invalid(format!(
            "this thread stops at depth {cap}; that reply would sit at {}",
            parent_depth + 1
        )),
        // "parent {id} is not a comment in this thread" -- the product names the whole mismatch instead, because it
        // also knows the parent's project and kind, which is what the person needs to fix it.
        Refused::ParentNotInThread { parent_id } => {
            WriteError::Invalid(format!("parent {parent_id} is not a comment in this thread"))
        }
    }
}

/// A stored row as the rules see it: identity is the product's, shape is not.
fn as_rule_comment(comment: &Comment, depth: u8) -> polaris_comments::Comment {
    polaris_comments::Comment {
        id: comment.id,
        parent_id: comment.parent_id,
        depth,
        body: comment.body.clone(),
        // The crate's clock is seconds; the store's is a double-precision epoch. Truncation is safe here because
        // ordering only compares, and two comments in the same second are separated by the crate's id tie-break.
        created_at: comment.created_at as i64,
        score: comment.score as i64,
        deleted: comment.deleted,
    }
}

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
    /// Whether the row is a tombstone. Not in the JSON: the thread read does not return deleted rows at all, so
    /// this is for the rules crate and nothing else.
    #[serde(skip)]
    pub deleted: bool,
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
            deleted: false,
        });
    }
    // THE ORDER IS THE CRATE'S. `ORDER BY created_at` in the query is only a cheap pre-sort; the tie-break that
    // makes two renders of one page agree is `polaris_comments::order`, and a page whose order changes between
    // requests is how a comment gets answered twice.
    let depths = depths_for(pool, &out).await?;
    let mut ruled: Vec<polaris_comments::Comment> = out
        .iter()
        .zip(&depths)
        .map(|(comment, depth)| as_rule_comment(comment, *depth))
        .collect();
    polaris_comments::order(&POLICY, &mut ruled);
    let by_id: std::collections::HashMap<i64, Comment> =
        out.into_iter().map(|comment| (comment.id, comment)).collect();
    Ok(ruled
        .into_iter()
        .filter_map(|rule| by_id.get(&rule.id).cloned())
        .collect())
}

/// How deep each of these comments sits, walked from `parent_id`.
///
/// The table has no depth column — concord's shape is suki's, `parent_id` and nothing else — so the depth the
/// rules crate asks for is computed here. `place()` needs the *parent's* depth; this needs every row's.
async fn depths_for(pool: &PgPool, comments: &[Comment]) -> Result<Vec<u8>, sqlx::Error> {
    let mut out = Vec::with_capacity(comments.len());
    for comment in comments {
        out.push(depth_of(pool, comment.id).await?);
    }
    Ok(out)
}

/// Walk one comment up its `parent_id` chain. Twenty is a ceiling, not a rule: a cycle cannot exist while
/// `parent_id` is written by `create` (which requires the parent to precede the child), but a corrupt row must
/// not be able to hang a read.
pub async fn depth_of(pool: &PgPool, comment_id: i64) -> Result<u8, sqlx::Error> {
    let mut depth = 0_u8;
    let mut current = Some(comment_id);
    while let Some(id) = current {
        if depth >= 20 {
            break;
        }
        let parent: Option<Option<i64>> =
            sqlx::query_scalar("select parent_id from comments where id = $1")
                .bind(id)
                .fetch_optional(pool)
                .await?;
        match parent {
            Some(Some(parent_id)) => {
                depth = depth.saturating_add(1);
                current = Some(parent_id);
            }
            _ => break,
        }
    }
    Ok(depth)
}

/// One comment, deleted or not — the read a permission decision needs.
pub async fn get(pool: &PgPool, comment_id: i64) -> Result<Option<Comment>, sqlx::Error> {
    use sqlx::Row;
    let row = sqlx::query(
        "select id, project_id, thread_kind, thread_id, author_id, parent_id, body, label, score, \
                extract(epoch from created_at)::double precision, deleted_at is not null \
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
        deleted: row.try_get(10)?,
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
    // The body rule is the estate's (`polaris_comments::check_body`): a whitespace-only comment is a moderation
    // artefact people publish by accident, and truncating an over-long one loses the end of a sentence silently.
    polaris_comments::check_body(&POLICY, body).map_err(refusal)?;
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
        // The product knows the parent's project and kind, which is what the person needs to fix it; the crate is
        // told only whether the parent is in this thread, which is the rule it owns (the cross-thread injection:
        // a valid id from elsewhere).
        let in_thread =
            parent_thread == thread_id && parent_kind == thread_kind && parent_project == project_id;
        if !in_thread {
            return Err(WriteError::Invalid(format!(
                "parent comment {parent_id} is on {parent_kind}/{parent_thread} in project \
                 {parent_project}, not {thread_kind}/{thread_id} in project {project_id}"
            )));
        }
        // And where it would sit. Unbounded depth today, so this cannot refuse; it exists so that the day the
        // policy gains a ceiling, the reply is refused (or flattened) by the crate rather than by a rule nobody
        // wrote here.
        let depth = depth_of(pool, parent_id).await?;
        polaris_comments::place(
            &POLICY,
            Some(polaris_comments::Parent {
                id: parent_id,
                depth,
                in_thread,
            }),
        )
        .map_err(refusal)?;
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
    // WHAT A DELETE LEAVES BEHIND IS THE CRATE'S DECISION. Under `POLICY.deletion = Tombstone` it answers `Some`
    // (the row stays, the reader is shown that something was removed); under `Cascade` it answers `None` and the
    // caller deletes the row, with `replies_lost` naming the subtree that goes with it. A unit test binds the
    // policy to this branch, so changing the policy without changing this code fails loudly.
    let ruled = as_rule_comment(&comment, depth_of(pool, comment_id).await?);
    let kept = polaris_comments::after_delete(&POLICY, ruled);
    if kept.is_none() {
        // Unreachable under the declared policy. If it ever runs, the product's schema has no ON DELETE CASCADE
        // on `parent_id`, so deleting here would leave replies pointing at nothing -- refuse rather than orphan.
        return Err(WriteError::Invalid(
            "this thread's policy says a delete takes its replies, and the schema cannot do that safely; \
             delete them first"
                .into(),
        ));
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

#[cfg(test)]
mod tests {
    use super::*;

    fn ruled(id: i64, parent: Option<i64>, minute: i64) -> polaris_comments::Comment {
        polaris_comments::Comment {
            id,
            parent_id: parent,
            depth: 0,
            body: "words".into(),
            created_at: minute,
            score: 0,
            deleted: false,
        }
    }

    /// The policy says tombstone, and the delete path branches on that answer. If somebody changes the policy to
    /// `Cascade`, this fails before the product starts orphaning replies against a schema with no
    /// `ON DELETE CASCADE`.
    #[test]
    fn the_declared_policy_is_a_tombstone() {
        let kept = polaris_comments::after_delete(&POLICY, ruled(1, None, 0));
        assert!(kept.is_some(), "the declared policy must be Deletion::Tombstone");
        assert_eq!(
            polaris_comments::replies_lost(&POLICY, &[ruled(1, None, 0), ruled(2, Some(1), 1)], 1),
            0,
            "a tombstone loses nothing"
        );
    }

    /// The crate refuses an empty body; the product keeps its own sentence. Both halves are asserted, because the
    /// mapping is the part a refactor drops.
    #[test]
    fn an_empty_body_is_refused_in_the_products_words() {
        assert!(polaris_comments::check_body(&POLICY, "   ").is_err());
        match refusal(polaris_comments::check_body(&POLICY, "   ").unwrap_err()) {
            WriteError::Invalid(message) => assert_eq!(message, "a comment needs a body"),
            other => panic!("expected Invalid, got {other:?}"),
        }
    }

    /// Two comments in the same second are ordered by id — the tie-break that makes two renders of one page agree.
    #[test]
    fn the_order_ties_break_by_id() {
        let mut page = vec![ruled(9, None, 100), ruled(3, None, 100), ruled(5, None, 99)];
        polaris_comments::order(&POLICY, &mut page);
        assert_eq!(page.iter().map(|c| c.id).collect::<Vec<_>>(), vec![5, 3, 9]);
    }

    /// The declared bounds preserve the product's behaviour: no ceiling was ever enforced, so none is invented.
    #[test]
    fn the_body_bound_does_not_invent_a_ceiling() {
        assert_eq!(POLICY.body.min, 1);
        assert_eq!(POLICY.body.max, usize::MAX);
        let long = "x".repeat(50_000);
        assert!(polaris_comments::check_body(&POLICY, &long).is_ok());
    }
}
