//! Membership and reputation — the two facts every write path ends up recording.
//!
//! Why these live together: "taking part makes you a participant" is a single rule (§M1's `JoinProject`), and the same
//! call that files a complaint grants `contributor` and awards reputation. Two functions, one file, because a write path
//! that remembers one and forgets the other produces a project whose contributors cannot validate anything.

use sqlx::PgPool;

/// Join a project as a contributor, or leave the existing role alone.
///
/// `or ignore`, never an upsert: an owner who files a complaint must not be demoted to `contributor` by doing so. The
/// reference uses `INSERT OR IGNORE` for exactly this reason.
///
/// # Errors
/// `sqlx::Error`.
pub async fn join(pool: &PgPool, project_id: i64, user_id: i64) -> Result<(), sqlx::Error> {
    sqlx::query(
        "insert into members (project_id, user_id, role, joined_at)
         values ($1, $2, 'contributor', now()) on conflict do nothing",
    )
    .bind(project_id)
    .bind(user_id)
    .execute(pool)
    .await?;
    Ok(())
}

/// Record a reputation event.
///
/// Points are signed and the `kind` is the reason, so the ledger explains itself: `submit_complaint` +5 is a contribution,
/// `complaint_rejected` −1 is the §M1 spam penalty. Reading reputation is `sum(points)` — see
/// [`crate::voteweight`], which documents why the charter's half-life is not applied.
///
/// # Errors
/// `sqlx::Error`.
pub async fn add_reputation(
    pool: &PgPool,
    project_id: i64,
    user_id: i64,
    kind: &str,
    points: f64,
) -> Result<(), sqlx::Error> {
    sqlx::query(
        "insert into reputation_events (project_id, user_id, kind, points, created_at)
         values ($1, $2, $3, $4, now())",
    )
    .bind(project_id)
    .bind(user_id)
    .bind(kind)
    .bind(points)
    .execute(pool)
    .await?;
    Ok(())
}
