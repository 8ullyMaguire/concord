//! Migrations, which this tree did not have (review decision 1).
//!
//! # The failure this exists to prevent
//!
//! `crates/db/sql/0001_baseline.sql` was applied by hand, and every column added after it was added to the baseline **and** to the running
//! database by hand — this session did exactly that for `projects.ai_involvement`. Two statements, one artefact each, nothing comparing
//! them. The failure mode is silent: a fresh database gets the column, the running one gets it only if somebody remembered, and the
//! divergence is discovered by a query in production.
//!
//! # The rule
//!
//! * One numbered file per change. `MIGRATIONS` is the ordered list, compiled in, so a binary carries the schema it was built against.
//! * `schema_version` records what has been applied. Applied once, never replayed.
//! * **A database ahead of the binary is refused, not tolerated.** A newer schema read by older code is how a column disappears from a
//!   `SELECT *` and a page renders somebody's project with half its facts. The binary says so at boot and exits.
//! * **A database that predates this mechanism is adopted, not re-created.** The live instance already has the baseline's 79 tables and no
//!   `schema_version` row; refusing it would mean every deploy needs a human with `psql`, which is the situation being fixed. Adoption is
//!   recorded as its own row (`adopted = true`) so the history says "this was assumed, not applied".
//!
//! # What is NOT claimed
//!
//! Migrations are not reversible here. A down-migration is a second guess about data that a forward migration already moved; the rollback
//! for a bad schema change in this tree is the database backup, and the doc says so instead of shipping `down.sql` files nobody tests.

use sqlx::{PgPool, Row};

/// One schema change, compiled into the binary.
pub struct Migration {
    /// Monotonic. Never reused, never reordered.
    pub version: i64,
    /// Human name, for `migrate --status` and the failure message.
    pub name: &'static str,
    /// The SQL, included at compile time so a binary cannot migrate itself with a file that has since changed.
    pub sql: &'static str,
}

/// Every migration this binary knows, in order.
pub const MIGRATIONS: &[Migration] = &[
    Migration {
        version: 1,
        name: "baseline",
        sql: include_str!("../sql/0001_baseline.sql"),
    },
    Migration {
        version: 2,
        name: "project_archived",
        sql: include_str!("../sql/0002_project_archived.sql"),
    },
];

/// What one `apply` did.
#[derive(Debug, Default)]
pub struct Report {
    /// Versions applied by this call, in order.
    pub applied: Vec<i64>,
    /// True when an existing database with the baseline's tables had no `schema_version` row and was recorded as migrated.
    pub adopted: bool,
}

/// The highest applied version that this binary does not know, if any.
///
/// Pure, so the refusal is a unit test rather than a claim: `Some(v)` means the database was migrated by something newer and this binary
/// must not serve it.
pub fn unknown_ahead(applied: &[i64], known: &[i64]) -> Option<i64> {
    let max_known = known.iter().copied().max().unwrap_or(0);
    applied
        .iter()
        .copied()
        .filter(|v| *v > max_known)
        .max()
}

/// Apply every migration this binary knows that the database has not recorded.
///
/// Fails when the database is ahead of the binary, when a migration's SQL fails, or when the record cannot be written. All three are fatal
/// to a service start: a server that boots on a schema it does not understand answers with wrong facts, which is worse than not answering.
pub async fn apply(pool: &PgPool) -> Result<Report, sqlx::Error> {
    sqlx::query(
        "create table if not exists schema_version ( \
           version bigint primary key, \
           name text not null, \
           adopted boolean not null default false, \
           applied_at timestamptz not null default now())",
    )
    .execute(pool)
    .await?;

    let mut report = Report::default();
    let mut applied: Vec<i64> = sqlx::query("select version from schema_version order by version")
        .fetch_all(pool)
        .await?
        .iter()
        .map(|row| row.try_get::<i64, _>("version"))
        .collect::<Result<_, _>>()?;

    let known: Vec<i64> = MIGRATIONS.iter().map(|m| m.version).collect();
    if let Some(ahead) = unknown_ahead(&applied, &known) {
        return Err(sqlx::Error::Protocol(format!(
            "the database is at schema version {ahead}, which this binary does not know (it knows {known:?}). \
             Refusing to serve: migrate with a newer binary, or restore a backup taken before that migration."
        )));
    }

    // A database from before this mechanism: the baseline's tables exist, no record does. Record it rather than replaying it — the SQL
    // would fail on the first `create table` and take the service down on a deploy that changed nothing.
    if applied.is_empty() {
        let baseline_there: bool =
            sqlx::query_scalar("select to_regclass('public.projects') is not null")
                .fetch_one(pool)
                .await?;
        if baseline_there {
            sqlx::query("insert into schema_version (version, name, adopted) values (1, 'baseline', true)")
                .execute(pool)
                .await?;
            applied.push(1);
            report.adopted = true;
        }
    }

    for migration in MIGRATIONS {
        if applied.contains(&migration.version) {
            continue;
        }
        // One transaction per migration: a half-applied change is a schema nobody designed.
        let mut tx = pool.begin().await?;
        sqlx::raw_sql(migration.sql).execute(&mut *tx).await?;
        sqlx::query("insert into schema_version (version, name) values ($1, $2)")
            .bind(migration.version)
            .bind(migration.name)
            .execute(&mut *tx)
            .await?;
        tx.commit().await?;
        report.applied.push(migration.version);
    }
    Ok(report)
}

/// What the database has recorded, and what this binary knows. Used by `migrate --status`.
pub async fn status(pool: &PgPool) -> Result<(Vec<(i64, String, bool)>, i64), sqlx::Error> {
    sqlx::query("create table if not exists schema_version ( \
                   version bigint primary key, name text not null, \
                   adopted boolean not null default false, applied_at timestamptz not null default now())")
        .execute(pool)
        .await?;
    let rows = sqlx::query("select version, name, adopted from schema_version order by version")
        .fetch_all(pool)
        .await?;
    let recorded = rows
        .iter()
        .map(|row| {
            Ok((
                row.try_get::<i64, _>("version")?,
                row.try_get::<String, _>("name")?,
                row.try_get::<bool, _>("adopted")?,
            ))
        })
        .collect::<Result<Vec<_>, sqlx::Error>>()?;
    let known = MIGRATIONS.iter().map(|m| m.version).max().unwrap_or(0);
    Ok((recorded, known))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_database_at_the_binarys_version_is_not_ahead() {
        assert_eq!(unknown_ahead(&[1], &[1]), None);
    }

    #[test]
    fn an_empty_record_is_not_ahead() {
        assert_eq!(unknown_ahead(&[], &[1]), None);
    }

    /// The refusal this module exists for: a newer binary migrated the database and this one must not serve it.
    #[test]
    fn a_database_past_the_binary_is_refused_and_names_the_version() {
        assert_eq!(unknown_ahead(&[1, 2], &[1]), Some(2));
        assert_eq!(unknown_ahead(&[1, 4, 3], &[1, 2]), Some(4));
    }

    /// A gap in the middle is not "ahead" — a version this binary knows but never recorded is simply unapplied, and `apply` runs it.
    #[test]
    fn a_gap_below_the_maximum_is_not_ahead() {
        assert_eq!(unknown_ahead(&[1, 3], &[1, 2, 3]), None);
    }

    #[test]
    fn the_binary_knows_the_baseline() {
        assert!(MIGRATIONS.iter().any(|m| m.version == 1 && m.name == "baseline"));
        // Ordering matters more than the contents: a binary that applied 2 before 1 would be its own outage.
        let mut versions: Vec<i64> = MIGRATIONS.iter().map(|m| m.version).collect();
        let sorted = {
            let mut v = versions.clone();
            v.sort_unstable();
            v
        };
        assert_eq!(versions, sorted);
        versions.dedup();
        assert_eq!(versions.len(), MIGRATIONS.len(), "a version number is used twice");
    }
}
