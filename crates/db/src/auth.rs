//! S1's auth: a user with a password, and the bearer token that identifies them.
//!
//! The Go store's format is the contract here and not a preference: `api_tokens.token_hash` holds
//! `base64.RawStdEncoding(sha256(token))`, and a hash written in any other encoding would be a token
//! that authenticates against the Rust service and not against the Go one — which, during a port
//! where both run side by side, is the worst possible failure. The password hash keeps argon2id's
//! parameters exactly as `internal/store/auth.go` sets them (m=65536, t=1, p=2, 32-byte key, 16-byte
//! salt), because the `$argon2id$v=19$m=...` string is parsed by whichever side verifies it.

use argon2::Argon2;
use base64::Engine;
use rand::RngCore;
use serde::Serialize;
use sha2::{Digest, Sha256};
use sqlx::PgPool;

/// The prefix that makes a token recognisable in a log, from `internal/store/auth.go:39`.
const TOKEN_PREFIX: &str = "clt_";

/// A user as `/api/v1/auth/register` returns it.
#[derive(Debug, Clone, Serialize)]
pub struct User {
    pub id: i64,
    pub username: String,
    pub display_name: String,
    pub role: String,
    #[serde(serialize_with = "unix_f64")]
    pub created_at: f64,
    pub is_guest: bool,
}

fn unix_f64<S: serde::Serializer>(value: &f64, serializer: S) -> Result<S::Ok, S::Error> {
    use serde_json::Number;
    let number = if value.fract() == 0.0 {
        Number::from(*value as i64)
    } else {
        Number::from_f64(*value).unwrap_or_else(|| Number::from(0))
    };
    number.serialize(serializer)
}

/// What can go wrong registering, in the four ways the Go store can.
#[derive(Debug)]
pub enum AuthError {
    /// `ErrInvalid`: the username or the password does not meet the rules.
    Invalid(String),
    /// `ErrDuplicate`: the username is taken.
    Duplicate(String),
    Database(sqlx::Error),
}

impl From<sqlx::Error> for AuthError {
    fn from(error: sqlx::Error) -> Self {
        AuthError::Database(error)
    }
}

/// `validUsername`: lowercase alphanumeric with `-`, `_`, `.`.
fn valid_username(name: &str) -> bool {
    !name.is_empty()
        && name.len() <= 64
        && name
            .chars()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || matches!(c, '-' | '_' | '.'))
}

/// `validPassword`: at least 8 characters, at most 200. Deliberately modest — the token is the
/// security boundary, and a hostile minimum pushes people toward pasteable passwords.
fn valid_password(password: &str) -> Result<(), AuthError> {
    if password.len() < 8 {
        return Err(AuthError::Invalid(
            "password must be at least 8 characters".into(),
        ));
    }
    if password.len() > 200 {
        return Err(AuthError::Invalid(
            "password must be at most 200 characters".into(),
        ));
    }
    Ok(())
}

/// argon2id with the Go tree's parameters, in the PHC string format both sides parse.
fn hash_password(password: &str) -> String {
    use argon2::password_hash::{PasswordHasher, SaltString};
    // The parameters are the Go store's: m=65536 KiB, t=1, p=2, 32-byte output, 16-byte salt.
    // `Params::new` refuses anything outside its bounds, which is why the unwrap is safe here and
    // would not be for a caller-supplied cost.
    let params = argon2::Params::new(64 * 1024, 1, 2, Some(32)).expect("argon2 params");
    let argon = Argon2::new(argon2::Algorithm::Argon2id, argon2::Version::V0x13, params);
    let mut salt_bytes = [0u8; 16];
    rand::thread_rng().fill_bytes(&mut salt_bytes);
    let salt = SaltString::encode_b64(&salt_bytes).expect("salt encoding");
    argon
        .hash_password(password.as_bytes(), &salt)
        .expect("argon2 hash")
        .to_string()
}

/// A fresh bearer token: 32 random bytes, base64url without padding, behind the `clt_` prefix.
fn new_token() -> String {
    let mut bytes = [0u8; 32];
    rand::thread_rng().fill_bytes(&mut bytes);
    let encoded = base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(bytes);
    format!("{TOKEN_PREFIX}{encoded}")
}

/// `hashToken`: base64 raw standard of sha256, which is what `api_tokens.token_hash` stores.
fn hash_token(token: &str) -> String {
    let digest = Sha256::digest(token.as_bytes());
    base64::engine::general_purpose::STANDARD_NO_PAD.encode(digest)
}

async fn user_by_id(pool: &PgPool, id: i64) -> Result<User, AuthError> {
    use sqlx::Row;
    // `role` is not a column: the Go store sets it to the constant `"member"` in `GetUser`, and a
    // translation that dashed off a `coalesce(role, ...)` failed with "column role does not exist"
    // — which is the schema telling you the field is derived, not stored.
    let row = sqlx::query(
        "select id, username, coalesce(display_name, ''), \
                extract(epoch from created_at)::double precision, coalesce(is_guest, false) \
         from users where id = $1",
    )
    .bind(id)
    .fetch_one(pool)
    .await?;
    Ok(User {
        id: row.try_get(0)?,
        username: row.try_get(1)?,
        display_name: row.try_get(2)?,
        role: "member".to_string(),
        created_at: row.try_get(3)?,
        is_guest: row.try_get(4)?,
    })
}

/// Create an account and its first token. Returns the user and the plaintext token, which is
/// returned exactly once and is not recoverable afterwards.
///
/// # Errors
/// [`AuthError::Invalid`] for a username or password that breaks the rules, [`AuthError::Duplicate`]
/// when the username is taken, [`AuthError::Database`] for the rest.
pub async fn register_user(
    pool: &PgPool,
    username: &str,
    display_name: &str,
    password: &str,
) -> Result<(User, String), AuthError> {
    let username = username.trim();
    if !valid_username(username) {
        return Err(AuthError::Invalid(
            "username must be lowercase alnum with - _ .".into(),
        ));
    }
    valid_password(password)?;
    let hash = hash_password(password);

    // `public_id` is NOT NULL on every API-facing table since the baseline, so the port writes one
    // at insert — this is the "ULID generated at insert" half of the ADR's decision, and the mover
    // is the other half (deterministic, so a rebuild reproduces the same ids).
    let inserted = sqlx::query_scalar::<_, i64>(
        "insert into users (username, display_name, password_hash, created_at, public_id) \
         values ($1, $2, $3, now(), $4) returning id",
    )
    .bind(username)
    .bind(display_name)
    .bind(&hash)
    .bind(ulid::Ulid::new().to_string())
    .fetch_one(pool)
    .await;
    let id = match inserted {
        Ok(id) => id,
        Err(sqlx::Error::Database(error)) if error.code().as_deref() == Some("23505") => {
            return Err(AuthError::Duplicate(format!("user {username:?}")))
        }
        Err(error) => return Err(AuthError::Database(error)),
    };

    let user = user_by_id(pool, id).await?;
    let token = new_token();
    sqlx::query(
        "insert into api_tokens (token_hash, user_id, created_at, last_used_at) \
         values ($1, $2, now(), now())",
    )
    .bind(hash_token(&token))
    .bind(id)
    .execute(pool)
    .await?;
    Ok((user, token))
}

/// The user a bearer token names, `None` when the token is absent, unknown, or malformed.
///
/// # Errors
/// [`AuthError::Database`] when the lookup fails; a bad token is an absence, not an error, because
/// every caller either requires an identity (and answers 401) or does not.
pub async fn user_for_token(pool: &PgPool, token: &str) -> Result<Option<User>, AuthError> {
    let id: Option<i64> =
        sqlx::query_scalar("select user_id from api_tokens where token_hash = $1")
            .bind(hash_token(token))
            .fetch_optional(pool)
            .await?;
    match id {
        Some(id) => Ok(Some(user_by_id(pool, id).await?)),
        None => Ok(None),
    }
}
