//! Append-only events. One row is one state change that mattered.

use sqlx::{PgConnection, PgPool};

/// Append inside an existing transaction. Refusing to open its own transaction is the point: a state change without
/// its event must not be able to commit.
pub async fn append(
    db: &mut PgConnection,
    project_id: i64,
    actor_id: i64,
    kind: &str,
    entity: &str,
    entity_id: i64,
    detail: &str,
) -> Result<(), sqlx::Error> {
    sqlx::query(
        "insert into events (project_id, actor_id, kind, entity, entity_id, detail) \
         values ($1, $2, $3, $4, $5, $6)",
    )
    .bind(project_id)
    .bind(actor_id)
    .bind(kind)
    .bind(entity)
    .bind(entity_id)
    .bind(detail)
    .execute(&mut *db)
    .await?;
    Ok(())
}

/// The count a probe uses. An unknown kind is zero rows, never an invented default.
pub async fn count_kind(pool: &PgPool, kind: &str) -> Result<i64, sqlx::Error> {
    sqlx::query_scalar("select count(*) from events where kind = $1")
        .bind(kind)
        .fetch_one(pool)
        .await
}
