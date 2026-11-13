//! Invites: the link that turns a stranger into a member.
//!
//! A `protected` project is "listed nowhere, readable by a signed-in user who has been granted access", and the grant has to
//! come from somewhere other than the request that wants it — an unguessable token that a member mints and hands over. The
//! alternative, a boolean `has_access` column, cannot answer *"who let this person in, when, and under which link"*, which is
//! exactly what an audit has to answer.
//!
//! Three decisions carried over from the reference, each of them load-bearing:
//!
//! * **the token is 32 bytes of `crypto` randomness**, not a counter or a clock-seeded value. It is the only thing between a
//!   stranger and a protected project;
//! * **redemption is idempotent per (invite, user)**, and the use counter advances only on a first redemption — otherwise a
//!   person clicking a `max_uses=1` link twice exhausts it by themselves, and a browser retry turns an idempotent action into
//!   "this link is dead";
//! * **every failure is the same 404.** Expired, revoked, exhausted and unknown are indistinguishable on purpose, or the
//!   endpoint becomes an oracle for testing tokens.

use base64::Engine;
use sqlx::PgPool;

/// A minted access link for one project.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Invite {
    /// The invite.
    pub id: i64,
    /// The project it grants access to.
    pub project_id: i64,
    /// The project's slug, for a UI that lists invites without a second lookup.
    pub project: String,
    /// The token itself. Empty once revoked? No — a revoked invite keeps its token so an audit can match the link that was
    /// handed out to the record that withdrew it.
    pub token: String,
    /// Who minted it.
    pub created_by: i64,
    /// Unix seconds.
    pub created_at: f64,
    /// Unix seconds, if it expires.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub expires_at: Option<f64>,
    /// How many redemptions it allows, if capped.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_uses: Option<i64>,
    /// How many it has had.
    pub uses: i64,
    /// Unix seconds, if withdrawn.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub revoked_at: Option<f64>,
}

/// Why an invite operation was refused.
#[derive(Debug)]
pub enum Refused {
    /// The request describes no invite that can be minted.
    Invalid(String),
    /// `NotFound` — **and for redeeming, this is also expired, revoked and exhausted**, deliberately.
    NotFound,
    /// The invite belongs to another project.
    WrongProject,
    /// The invite was already withdrawn.
    AlreadyRevoked,
    /// The database said no.
    Database(sqlx::Error),
}

impl From<sqlx::Error> for Refused {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

/// 32 bytes behind a token: base64url of 32 bytes is 43 characters, enough that guessing one is not a plan anyone has.
const TOKEN_LENGTH: usize = 32;

fn new_token() -> String {
    use rand::RngCore;
    let mut bytes = [0u8; TOKEN_LENGTH];
    rand::thread_rng().fill_bytes(&mut bytes);
    // Raw URL-safe so the token can be pasted into a path or a query string without escaping.
    base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(bytes)
}

/// Mint an invite.
///
/// `expires_in_seconds <= 0` means no expiry; `max_uses <= 0` means unlimited. Both are columns that allow NULL, and NULL is
/// the honest representation of "no limit" — a sentinel like 0 would need every reader to know what it meant.
///
/// # Errors
/// See [`Refused`].
pub async fn create(
    pool: &PgPool,
    project_id: i64,
    created_by: i64,
    expires_in_seconds: i64,
    max_uses: i64,
) -> Result<Invite, Refused> {
    let token = new_token();
    let id: i64 = sqlx::query_scalar(
        "insert into project_invites (project_id, token, created_by, created_at, expires_at, max_uses, uses)
         values ($1,$2,$3,now(),
                 case when $4 > 0 then now() + make_interval(secs => $4::double precision) end,
                 nullif($5,0), 0)
         returning id",
    )
    .bind(project_id)
    .bind(&token)
    .bind(created_by)
    .bind(expires_in_seconds)
    .bind(max_uses)
    .fetch_one(pool)
    .await?;
    get(pool, id).await?.ok_or(Refused::NotFound)
}

/// One invite, with its project's slug.
///
/// # Errors
/// `sqlx::Error`.
pub async fn get(pool: &PgPool, id: i64) -> Result<Option<Invite>, sqlx::Error> {
    let row: Option<(i64, i64, String, String, i64, f64, Option<f64>, Option<i64>, i64, Option<f64>)> =
        sqlx::query_as(
            "select i.id, i.project_id, p.slug, i.token, i.created_by,
                    extract(epoch from i.created_at)::double precision,
                    extract(epoch from i.expires_at)::double precision,
                    i.max_uses, i.uses,
                    extract(epoch from i.revoked_at)::double precision
               from project_invites i join projects p on p.id = i.project_id
              where i.id = $1",
        )
        .bind(id)
        .fetch_optional(pool)
        .await?;
    Ok(row.map(shape))
}

/// A project's invites, newest first.
///
/// # Errors
/// `sqlx::Error`.
pub async fn list(pool: &PgPool, project_id: i64) -> Result<Vec<Invite>, sqlx::Error> {
    let rows: Vec<(i64, i64, String, String, i64, f64, Option<f64>, Option<i64>, i64, Option<f64>)> =
        sqlx::query_as(
            "select i.id, i.project_id, p.slug, i.token, i.created_by,
                    extract(epoch from i.created_at)::double precision,
                    extract(epoch from i.expires_at)::double precision,
                    i.max_uses, i.uses,
                    extract(epoch from i.revoked_at)::double precision
               from project_invites i join projects p on p.id = i.project_id
              where i.project_id = $1
              order by i.created_at desc, i.id desc",
        )
        .bind(project_id)
        .fetch_all(pool)
        .await?;
    Ok(rows.into_iter().map(shape).collect())
}

fn shape(
    row: (i64, i64, String, String, i64, f64, Option<f64>, Option<i64>, i64, Option<f64>),
) -> Invite {
    Invite {
        id: row.0,
        project_id: row.1,
        project: row.2,
        token: row.3,
        created_by: row.4,
        created_at: row.5,
        expires_at: row.6,
        max_uses: row.7,
        uses: row.8,
        revoked_at: row.9,
    }
}

/// Withdraw an invite **while keeping the record of it**: `revoked_at` is set, and nothing is deleted.
///
/// # Errors
/// [`Refused::NotFound`] when there is no such invite, [`Refused::AlreadyRevoked`] when it is already withdrawn — the two are
/// distinguished so a caller can report honestly, which is the one place this module is precise about a failure.
pub async fn revoke(pool: &PgPool, id: i64) -> Result<(), Refused> {
    let affected = sqlx::query(
        "update project_invites set revoked_at = now() where id = $1 and revoked_at is null",
    )
    .bind(id)
    .execute(pool)
    .await?
    .rows_affected();
    if affected > 0 {
        return Ok(());
    }
    let exists: i64 = sqlx::query_scalar("select count(*) from project_invites where id = $1")
        .bind(id)
        .fetch_one(pool)
        .await?;
    if exists == 0 {
        Err(Refused::NotFound)
    } else {
        Err(Refused::AlreadyRevoked)
    }
}

/// Redeem a token into a membership, returning the project id.
///
/// One transaction for the redemption record, the membership and the use counter: split, a failure in the middle leaves a
/// membership nobody can attribute or a use counted for nothing.
///
/// The order inside matters and is the reference's: **check whether this user already redeemed BEFORE applying the use cap.**
/// A repeat redemption by someone who already holds access must succeed even when the invite is exhausted — they are not
/// consuming a new use, and refusing them turns an idempotent click into a dead link.
///
/// # Errors
/// [`Refused::NotFound`] for every failure mode, deliberately.
pub async fn redeem(pool: &PgPool, token: &str, user_id: i64) -> Result<i64, Refused> {
    let mut tx = pool.begin().await?;
    let row: Option<(i64, i64, Option<f64>, Option<i64>, i64, Option<f64>)> = sqlx::query_as(
        "select id, project_id, extract(epoch from expires_at)::double precision, max_uses, uses,
                extract(epoch from revoked_at)::double precision
           from project_invites where token = $1",
    )
    .bind(token)
    .fetch_optional(&mut *tx)
    .await?;
    let Some((invite_id, project_id, expires_at, max_uses, uses, revoked_at)) = row else {
        return Err(Refused::NotFound);
    };
    let now = crate::now_unix();
    if revoked_at.is_some() {
        return Err(Refused::NotFound);
    }
    if let Some(expiry) = expires_at {
        if expiry < now {
            return Err(Refused::NotFound);
        }
    }
    let already: i64 = sqlx::query_scalar(
        "select count(*) from invite_redemptions where invite_id = $1 and user_id = $2",
    )
    .bind(invite_id)
    .bind(user_id)
    .fetch_one(&mut *tx)
    .await?;
    let already_redeemed = already > 0;
    if let Some(cap) = max_uses {
        if uses >= cap && !already_redeemed {
            return Err(Refused::NotFound);
        }
    }

    if !already_redeemed {
        sqlx::query(
            "insert into invite_redemptions (invite_id, user_id, redeemed_at) values ($1,$2,now())
             on conflict (invite_id, user_id) do nothing",
        )
        .bind(invite_id)
        .bind(user_id)
        .execute(&mut *tx)
        .await?;
    }
    // The role an invite grants is `contributor`: a link lets someone in, it does not confer standing to admit others or move
    // a decision. Roles above that are given by a member, knowingly.
    sqlx::query(
        "insert into members (project_id, user_id, role, joined_at) values ($1,$2,'contributor',now())
         on conflict (project_id, user_id) do nothing",
    )
    .bind(project_id)
    .bind(user_id)
    .execute(&mut *tx)
    .await?;
    if !already_redeemed {
        sqlx::query("update project_invites set uses = uses + 1 where id = $1")
            .bind(invite_id)
            .execute(&mut *tx)
            .await?;
    }
    tx.commit().await?;
    Ok(project_id)
}
