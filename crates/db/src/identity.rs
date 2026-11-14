//! Guest identities, the consent gate, and the fold that moves a guest's work onto the account it logs into.
//!
//! A visitor with no account can act — vote, comment, file a complaint — as a **guest**: a `users` row with `is_guest = 1` and an
//! empty `password_hash`. Two things follow from that and both are enforced here rather than in a handler:
//!
//! * **a guest can never log in.** It has no credential; it acts through the token it was issued. The way a guest becomes an
//!   account is to REGISTER, which promotes the row in place ([`promote_guest`]) — so every row it produced already points at the
//!   id that becomes the account, and nothing is copied, moved, or orphaned;
//! * **the instance has a size.** Guests are nameless contributions on someone else's server, so they are capped by the number of
//!   *accounts* active in the last month: past the cap the door shuts, and it shuts for guests already inside too ([`guest_status`]
//!   is consulted on every guest write, not only at mint — mint-only would let an instance cross the cap and keep serving one
//!   visitor forever, which is exactly the state the cap exists to end).
//!
//! The fold-on-login is the other half: a visitor who acted as a guest and then logs into an account *they already had* is two
//! identities, and their rows genuinely have to move. Which columns move is **derived from the catalogue** rather than listed by
//! hand — the Go tree keeps a 48-entry list that a test derives and compares; deriving it at every fold is the same property with
//! one fewer thing that can drift.

use sqlx::PgPool;

use crate::auth::{self, AuthError, User};

/// The default threshold, from the Go tree: the request's one thousand.
pub const DEFAULT_GUEST_MAU_LIMIT: i64 = 1000;

/// The default consent version.
pub const DEFAULT_CONSENT_VERSION: &str = "1";

/// The window the cap is measured over, in days.
pub const MAU_WINDOW_DAYS: i64 = 30;

/// Whether guests may act, and how big the instance is.
#[derive(Debug, Clone, serde::Serialize)]
pub struct GuestStatus {
    /// Guests are allowed only while under the cap. `limit <= 0` means switched off.
    pub allowed: bool,
    /// Distinct NON-guest accounts that appear in the audit log inside the window.
    pub mau: i64,
    /// The configured cap.
    pub limit: i64,
    /// The window, in days.
    pub window_days: i64,
    /// How many guest identities exist at all.
    pub guests: i64,
    /// How many of them are older than the window — the ones a sweep would delete.
    pub stale_guests: i64,
}

impl GuestStatus {
    /// The threshold reading, with the Go tree's two rules: a limit of zero or less switches guests off entirely (deliberately
    /// not a separate flag — an operator who sets 0 wants the door shut), and an unparseable value is the default rather than an
    /// error, because refusing to start over a knob whose only jobs are "open" and "shut" takes the instance down.
    #[must_use]
    pub fn limit_from_env() -> i64 {
        std::env::var("CONCORD_GUEST_MAU_LIMIT")
            .ok()
            .map(|raw| raw.trim().to_string())
            .filter(|raw| !raw.is_empty())
            .and_then(|raw| raw.parse::<i64>().ok())
            .unwrap_or(DEFAULT_GUEST_MAU_LIMIT)
    }

    /// The consent notice version.
    #[must_use]
    pub fn consent_version_from_env() -> String {
        std::env::var("CONCORD_CONSENT_VERSION")
            .ok()
            .map(|raw| raw.trim().to_string())
            .filter(|raw| !raw.is_empty())
            .unwrap_or_else(|| DEFAULT_CONSENT_VERSION.to_string())
    }
}

/// What a fold moved, so the response can say it plainly.
#[derive(Debug, Default, Clone, serde::Serialize)]
pub struct FoldReport {
    /// Rows that now belong to the account.
    pub moved: i64,
    /// Rows the account already had an equivalent of (a vote on the same pair, a document at the same address) — dropped with the
    /// guest, because keeping both would count one opinion twice.
    pub superseded: i64,
}

/// Why a guest operation was refused.
#[derive(Debug)]
pub enum Refused {
    /// The instance has grown past its guest cap.
    LimitReached { mau: i64, limit: i64 },
    /// The instance could not say how big it is.
    CheckFailed(sqlx::Error),
    /// The identity is not a guest, or does not exist.
    NotAGuest,
    /// The request does not describe something that can exist.
    Invalid(String),
    /// The username is taken.
    Duplicate(String),
    /// The database said no.
    Database(sqlx::Error),
}

impl From<sqlx::Error> for Refused {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

/// Distinct non-guest accounts that appear in the audit log inside the window.
///
/// The audit log rather than a last-seen column: it is the one table every write already records into, and counting DISTINCT
/// actors over a time window is what the table is for.
///
/// # Errors
/// `sqlx::Error`.
pub async fn monthly_active_users(pool: &PgPool) -> Result<i64, sqlx::Error> {
    sqlx::query_scalar(
        "select count(distinct a.actor_id)::bigint
           from audit_log a
           join users u on u.id = a.actor_id
          where a.actor_id is not null and a.actor_id > 0
            and a.created_at >= now() - ($1 || ' days')::interval
            and coalesce(u.is_guest, false) = false",
    )
    .bind(MAU_WINDOW_DAYS.to_string())
    .fetch_one(pool)
    .await
}

/// The guest status against `limit`.
///
/// # Errors
/// `sqlx::Error`.
pub async fn guest_status(pool: &PgPool, limit: i64) -> Result<GuestStatus, sqlx::Error> {
    let mau = monthly_active_users(pool).await?;
    let guests: i64 = sqlx::query_scalar("select count(*)::bigint from users where coalesce(is_guest, false)")
        .fetch_one(pool)
        .await?;
    let stale_guests: i64 = sqlx::query_scalar(
        "select count(*)::bigint from users
          where coalesce(is_guest, false)
            and created_at < now() - ($1 || ' days')::interval",
    )
    .bind(MAU_WINDOW_DAYS.to_string())
    .fetch_one(pool)
    .await?;
    Ok(GuestStatus {
        allowed: limit > 0 && mau < limit,
        mau,
        limit,
        window_days: MAU_WINDOW_DAYS,
        guests,
        stale_guests,
    })
}

/// Mint a guest identity and its token.
///
/// The name is `guest-` plus 8 hex characters — 2^32 names, retried on a collision rather than assumed collision-free, because
/// "astronomically unlikely" is not a reason to answer 500 to a visitor.
///
/// # Errors
/// [`Refused::LimitReached`] when the cap is reached, [`Refused::CheckFailed`] when the size cannot be read.
pub async fn create_guest(pool: &PgPool, limit: i64) -> Result<(User, String), Refused> {
    let status = guest_status(pool, limit).await.map_err(Refused::CheckFailed)?;
    if !status.allowed {
        return Err(Refused::LimitReached {
            mau: status.mau,
            limit: status.limit,
        });
    }
    for _ in 0..5 {
        let suffix = random_hex(4);
        let inserted: Result<i64, sqlx::Error> = sqlx::query_scalar(
            "insert into users (username, display_name, password_hash, created_at, is_guest, public_id)
             values ($1, 'guest', '', now(), true, $2) returning id",
        )
        .bind(format!("guest-{suffix}"))
        .bind(ulid::Ulid::new().to_string())
        .fetch_one(pool)
        .await;
        match inserted {
            Ok(id) => {
                let user = user_by_id(pool, id).await?;
                let token = auth::issue_token(pool, id).await.map_err(|error| match error {
                    AuthError::Database(error) => Refused::Database(error),
                    other => Refused::Invalid(format!("{other:?}")),
                })?;
                return Ok((user, token));
            }
            Err(sqlx::Error::Database(error)) if error.code().as_deref() == Some("23505") => continue,
            Err(error) => return Err(Refused::Database(error)),
        }
    }
    Err(Refused::Invalid(
        "could not find a free guest name after 5 attempts".to_string(),
    ))
}

/// Turn a guest identity into an account **in place**.
///
/// Every row the guest produced already points at this id, so registering with a guest token copies nothing, orphans nothing, and
/// keeps the history one identity's: "this account took these positions while it had no name".
///
/// # Errors
/// [`Refused::NotAGuest`] when the id is not a guest row (which is also how "already an account" answers),
/// [`Refused::Duplicate`] when the username is taken.
pub async fn promote_guest(
    pool: &PgPool,
    guest_id: i64,
    username: &str,
    display_name: &str,
    password: &str,
) -> Result<(User, String), Refused> {
    let username = username.trim();
    let updated = sqlx::query(
        "update users set username = $1, display_name = $2, password_hash = $3, is_guest = false
          where id = $4 and coalesce(is_guest, false)",
    )
    .bind(username)
    .bind(display_name)
    .bind(crate::auth::hash_for(password))
    .bind(guest_id)
    .execute(pool)
    .await;
    match updated {
        Ok(result) if result.rows_affected() == 1 => {}
        Ok(_) => return Err(Refused::NotAGuest),
        Err(sqlx::Error::Database(error)) if error.code().as_deref() == Some("23505") => {
            return Err(Refused::Duplicate(format!("user {username:?}")))
        }
        Err(error) => return Err(Refused::Database(error)),
    }
    let user = user_by_id(pool, guest_id).await?;
    let token = auth::issue_token(pool, guest_id).await.map_err(|error| match error {
        AuthError::Database(error) => Refused::Database(error),
        other => Refused::Invalid(format!("{other:?}")),
    })?;
    Ok((user, token))
}

/// Every `(table, column)` in the schema that carries a `users.id`, derived from the catalogue.
///
/// Two sources, and both are in the schema rather than in this file:
/// * every foreign key pointing at `users(id)`;
/// * the FK-less columns that carry an actor id on purpose — `audit_log.actor_id` (the log outlives the row it names) and
///   `admin_ledger.actor_id`. They are found by name-and-type, which is the same rule the Go tree's test applies, and the reason
///   the list is derived is that a fold which silently misses a column is how "my votes disappeared" is discovered months later,
///   by the one person who cannot debug it.
///
/// `maintenance_signals.set_by` is deliberately NOT here: it is TEXT, an operator's name, not a user id.
///
/// # Errors
/// `sqlx::Error`.
pub async fn fold_columns(pool: &PgPool) -> Result<Vec<(String, String)>, sqlx::Error> {
    let rows: Vec<(String, String)> = sqlx::query_as(
        "select distinct cl.relname::text, att.attname::text
           from pg_constraint con
           join pg_class cl on cl.oid = con.conrelid
           join pg_namespace ns on ns.oid = cl.relnamespace
           join pg_attribute att on att.attrelid = con.conrelid and att.attnum = any(con.conkey)
          where con.contype = 'f' and con.confrelid = 'users'::regclass and ns.nspname = 'public'
          order by 1, 2",
    )
    .fetch_all(pool)
    .await?;
    let mut columns: Vec<(String, String)> = rows
        .into_iter()
        .filter(|(table, _)| table != "users")
        .collect();
    // The FK-less carriers, added only if the columns really exist and really are integers.
    for table in ["audit_log", "admin_ledger"] {
        let exists: bool = sqlx::query_scalar(
            "select exists (
                select 1 from information_schema.columns
                 where table_schema = 'public' and table_name = $1 and column_name = 'actor_id'
                   and data_type in ('bigint','integer'))",
        )
        .bind(table)
        .fetch_one(pool)
        .await?;
        if exists && !columns.iter().any(|(t, c)| t == table && c == "actor_id") {
            columns.push((table.to_string(), "actor_id".to_string()));
        }
    }
    columns.sort();
    Ok(columns)
}

/// Move a guest's rows onto an account, then delete the guest.
///
/// The Postgres translation of `UPDATE OR IGNORE` is a savepoint per column: an update that violates a uniqueness constraint (the
/// account already voted on that pair, already has a document at that address) is rolled back to the savepoint and counted as
/// **superseded**, and whatever the account did not take is deleted with the guest — counted, never silently dropped.
///
/// The guest's tokens are folded with everything else, which means the browser's guest token now names the ACCOUNT. That is the
/// behaviour the reference has, and it is the right one: after a login the browser holds a working credential, and deleting it
/// instead would log the visitor out of the page they just signed into.
///
/// # Errors
/// [`Refused::NotAGuest`] when `guest_id` is not a guest, [`Refused::Invalid`] for a fold onto itself.
pub async fn fold_guest_into(
    pool: &PgPool,
    guest_id: i64,
    account_id: i64,
) -> Result<FoldReport, Refused> {
    if guest_id == 0 || account_id == 0 || guest_id == account_id {
        return Err(Refused::Invalid(
            "a fold needs two distinct identities".to_string(),
        ));
    }
    let is_guest: bool = sqlx::query_scalar(
        "select coalesce(is_guest, false) from users where id = $1",
    )
    .bind(guest_id)
    .fetch_optional(pool)
    .await?
    .unwrap_or(false);
    if !is_guest {
        return Err(Refused::NotAGuest);
    }
    let account_exists: bool =
        sqlx::query_scalar("select exists (select 1 from users where id = $1)")
            .bind(account_id)
            .fetch_one(pool)
            .await?;
    if !account_exists {
        return Err(Refused::Invalid(format!(
            "account {account_id} does not exist"
        )));
    }

    let columns = fold_columns(pool).await?;
    let mut report = FoldReport::default();
    let mut transaction = pool.begin().await?;
    for (table, column) in columns {
        let statement_count = format!("select count(*)::bigint from {table} where {column} = $1");
        let before: i64 = sqlx::query_scalar(&statement_count)
            .bind(guest_id)
            .fetch_one(&mut *transaction)
            .await?;
        if before == 0 {
            continue;
        }
        sqlx::query("savepoint fold_column")
            .execute(&mut *transaction)
            .await?;
        let statement_update = format!("update {table} set {column} = $1 where {column} = $2");
        match sqlx::query(&statement_update)
            .bind(account_id)
            .bind(guest_id)
            .execute(&mut *transaction)
            .await
        {
            Ok(result) => {
                sqlx::query("release savepoint fold_column")
                    .execute(&mut *transaction)
                    .await?;
                let moved = i64::try_from(result.rows_affected()).unwrap_or(i64::MAX);
                report.moved += moved;
                report.superseded += before - moved;
            }
            Err(sqlx::Error::Database(error)) if error.code().as_deref() == Some("23505") => {
                // The account already holds an equivalent row. The guest's is dropped below, with the guest.
                sqlx::query("rollback to savepoint fold_column")
                    .execute(&mut *transaction)
                    .await?;
                report.superseded += before;
            }
            Err(error) => return Err(Refused::Database(error)),
        }
    }
    // Whatever the account did not take still belongs to an identity about to be deleted, so it goes with it.
    for (table, column) in fold_columns(pool).await? {
        let statement = format!("delete from {table} where {column} = $1");
        sqlx::query(&statement)
            .bind(guest_id)
            .execute(&mut *transaction)
            .await?;
    }
    sqlx::query("delete from api_tokens where user_id = $1")
        .bind(guest_id)
        .execute(&mut *transaction)
        .await?;
    sqlx::query("delete from users where id = $1 and coalesce(is_guest, false)")
        .bind(guest_id)
        .execute(&mut *transaction)
        .await?;
    transaction.commit().await?;
    Ok(report)
}

/// Record that this identity accepted the notice, if it has an identity at all.
///
/// # Errors
/// `sqlx::Error`.
pub async fn record_consent(pool: &PgPool, user_id: i64) -> Result<(), sqlx::Error> {
    sqlx::query("update users set consented_at = now() where id = $1")
        .bind(user_id)
        .execute(pool)
        .await?;
    Ok(())
}

/// Whether this user id is a guest.
///
/// # Errors
/// `sqlx::Error`.
pub async fn is_guest(pool: &PgPool, user_id: i64) -> Result<bool, sqlx::Error> {
    Ok(sqlx::query_scalar("select coalesce(is_guest, false) from users where id = $1")
        .bind(user_id)
        .fetch_optional(pool)
        .await?
        .unwrap_or(false))
}

async fn user_by_id(pool: &PgPool, id: i64) -> Result<User, Refused> {
    crate::auth::user_by_id_public(pool, id)
        .await
        .map_err(|error| match error {
            AuthError::Database(error) => Refused::Database(error),
            other => Refused::Invalid(format!("{other:?}")),
        })
}

fn random_hex(bytes: usize) -> String {
    use rand::RngCore;
    let mut buffer = vec![0u8; bytes];
    rand::thread_rng().fill_bytes(&mut buffer);
    buffer.iter().map(|byte| format!("{byte:02x}")).collect()
}
