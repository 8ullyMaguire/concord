//! Trust levels: per-account, instance-wide, bounded by `trust_config.max_level`.
//!
//! This is deliberately **not** the same thing as a project role. A role answers "what may this person do in this project" and is granted
//! per project; a trust level answers "how much do we believe this account on this instance". Mixing them would mean an account's power
//! changes silently when it joins a project, which is the wrong coupling for a threshold that gates approving other people's work.
//!
//! There is no HTTP route that writes a trust level, which is why the operator binary has a subcommand for it: without one, no account can
//! ever reach the configured minimum and every trust-gated endpoint — feature status changes above all — is unreachable for everyone,
//! including the operator.

use sqlx::PgPool;

/// A trust assignment that would exceed the instance's configured maximum.
///
/// Distinct from a permission failure so a caller can tell "you may not do this" from "this would exceed the ceiling" — the two need
/// different sentences in front of an operator, and one of them is fixable by an administrator while the other is not.
#[derive(Debug)]
pub struct CeilingExceeded {
    /// The level that was asked for.
    pub level: i64,
    /// The instance's maximum.
    pub ceiling: i64,
}

impl std::fmt::Display for CeilingExceeded {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            formatter,
            "trust level {} exceeds the configured ceiling {}",
            self.level, self.ceiling
        )
    }
}

impl std::error::Error for CeilingExceeded {}

/// An account's trust level, `0` for an account that does not exist.
///
/// Unknown is `0` rather than an error because every caller is an authorisation check: a missing user must fail closed without a separate
/// branch at each call site.
///
/// # Errors
/// `sqlx::Error`.
pub async fn get_trust_level(pool: &PgPool, user: i64) -> Result<i64, sqlx::Error> {
    let level: Option<i64> = sqlx::query_scalar("select trust_level from users where id = $1")
        .bind(user)
        .fetch_optional(pool)
        .await?;
    Ok(level.unwrap_or(0))
}

/// The instance's maximum trust level.
///
/// Read from the single `trust_config` row. The table's key is an identity column, so `id = 1` is an assumption the Go made and this does
/// not: the row is *the* row, whatever its id, and an absent row means the configured default of `1`.
///
/// # Errors
/// `sqlx::Error`.
pub async fn trust_ceiling(pool: &PgPool) -> Result<i64, sqlx::Error> {
    let ceiling: Option<i64> =
        sqlx::query_scalar("select max_level from trust_config order by id limit 1")
            .fetch_optional(pool)
            .await?;
    Ok(ceiling.unwrap_or(1))
}

/// Assign a trust level, refusing to exceed the ceiling, and record who did it.
///
/// Both writes are one transaction: the grant is the audit trail and the account column is what every authorisation check reads, so a
/// commit that lands one and not the other leaves the instance either permissive without a record or recorded without effect.
///
/// The ceiling is read **inside** the transaction, so a concurrent second writer cannot slip past a read-then-write check, and the refusal
/// happens before any write, so a refused assignment leaves no history of an attempt that did not happen.
///
/// # Errors
/// `sqlx::Error`, or [`CeilingExceeded`] boxed with it.
pub async fn set_trust_level(
    pool: &PgPool,
    target: i64,
    level: i64,
    granted_by: i64,
    reason: &str,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let mut tx = pool.begin().await?;
    let ceiling: Option<i64> =
        sqlx::query_scalar("select max_level from trust_config order by id limit 1")
            .fetch_optional(&mut *tx)
            .await?;
    let ceiling = ceiling.unwrap_or(1);
    // `granttrust-clamp` writes the ceiling instead of refusing, which is the shape of the defect the refusal exists to prevent: the
    // operator leaves believing the level they asked for is in force.
    let level = if level > ceiling {
        if armed("granttrust-clamp") {
            ceiling
        } else {
            return Err(Box::new(CeilingExceeded { level, ceiling }));
        }
    } else {
        level
    };
    sqlx::query(
        "insert into trust_grants (user_id, level, granted_by, reason, created_at) values ($1, $2, $3, $4, now())",
    )
    .bind(target)
    .bind(level)
    .bind(granted_by)
    .bind(reason)
    .execute(&mut *tx)
    .await?;
    let updated = sqlx::query("update users set trust_level = $1 where id = $2")
        .bind(level)
        .bind(target)
        .execute(&mut *tx)
        .await?;
    if updated.rows_affected() == 0 {
        // The grant row's foreign key already refused this, but say so in words an operator can act on rather than as a constraint name.
        return Err(Box::new(UnknownAccount { user: target }));
    }
    tx.commit().await?;
    Ok(())
}

/// A named deliberate defect, armed at runtime so a control run needs no rebuild.
fn armed(name: &str) -> bool {
    std::env::var("CONCORD_MUTANT")
        .unwrap_or_default()
        .split(',')
        .any(|mutant| mutant.trim() == name)
}

/// The account named for a grant does not exist.
#[derive(Debug)]
pub struct UnknownAccount {
    /// The id that was asked for.
    pub user: i64,
}

impl std::fmt::Display for UnknownAccount {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(formatter, "no account with id {}", self.user)
    }
}

impl std::error::Error for UnknownAccount {}
