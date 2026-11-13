//! §4.10's criteria **writes**: creating one, switching it off, voting on one, and saving a weight profile.
//!
//! The reads live in [`crate::criteria`]. They are apart because the read side is what a project page needs and the
//! write side is what four maintainer routes need, and mixing them made the read module's doc-comment a list of two
//! unrelated jobs.
//!
//! Four decisions worth stating, because each one is a refusal the caller sees:
//!
//! * **A criterion is scoped to its project, twice** — the row is looked up with the project in the `WHERE`, and a vote
//!   re-checks that both features belong to that same project. Without the second check a caller can pool votes across
//!   projects by guessing ids, and the resulting rating is arithmetic on rows that never met.
//! * **One vote per (criterion, voter, pair).** A re-vote is a double count, and a silently doubled opinion is invisible
//!   in the audit trail. The unique index is the authority; the store turns its violation into a readable refusal.
//! * **`skip` and `both`/`neither` are recorded and change no rating.** Abstention is data — and a vote that quietly did
//!   nothing would be indistinguishable from a lost vote, which is why the neutral outcomes get an audit row of their own.
//! * **A weight of zero is a complete no-op on the rating** (see [`concord_ranking::apply_pairwise_vote`]): that is the
//!   account-age gate refusing a new account's influence, and it must not leave RD collapsing in its wake.

use sqlx::{PgPool, Row};

use crate::criteria::Criterion;

/// A weight to apply, keyed by criterion.
#[derive(Debug, Clone, Copy)]
pub struct Weight {
    /// Which criterion.
    pub criterion_id: i64,
    /// How much it counts.
    pub weight: f64,
}

/// Why a criteria write was refused.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Refused {
    /// The body was well-formed and the request is wrong: an empty slug, a bad direction, a re-vote.
    Invalid(String),
    /// The id belongs to the project the caller named? No — it belongs to another one.
    Scope,
    /// No such criterion (or feature) in this project.
    NotFound,
}

/// Create one criterion.
///
/// `direction` defaults to `higher_is_better` and a non-positive weight becomes `1.0`, both silently: a caller that
/// leaves a field out is not making a mistake, and the reference did the same. An explicit *bad* direction is refused,
/// because that one changes what the ranking means.
///
/// # Errors
/// `sqlx::Error`.
pub async fn create(
    pool: &PgPool,
    project_id: i64,
    actor_id: i64,
    slug: &str,
    name: &str,
    description: &str,
    direction: &str,
    default_weight: f64,
) -> Result<Result<Criterion, Refused>, sqlx::Error> {
    let slug = slug.trim().to_lowercase();
    if slug.is_empty() {
        return Ok(Err(Refused::Invalid("empty criterion slug".to_string())));
    }
    if name.is_empty() {
        return Ok(Err(Refused::Invalid("empty criterion name".to_string())));
    }
    let direction = if direction.is_empty() {
        "higher_is_better"
    } else {
        direction
    };
    if direction != "higher_is_better" && direction != "lower_is_better" {
        return Ok(Err(Refused::Invalid(
            "direction must be higher_is_better or lower_is_better".to_string(),
        )));
    }
    let default_weight = if default_weight <= 0.0 { 1.0 } else { default_weight };
    let row = sqlx::query(
        "insert into criteria (project_id, slug, name, description, direction, default_weight,
                               active, created_by, created_at, updated_at)
         values ($1, $2, $3, $4, $5, $6, 1, $7, now(), now())
         returning id, project_id, slug, name, description, direction, default_weight, active",
    )
    .bind(project_id)
    .bind(&slug)
    .bind(name)
    .bind(description)
    .bind(direction)
    .bind(default_weight)
    .bind(actor_id)
    .fetch_one(pool)
    .await;
    match row {
        Ok(row) => Ok(Ok(map_criterion(&row))),
        Err(error) if is_unique_violation(&error) => Ok(Err(Refused::Invalid(format!(
            "criterion {slug:?} already exists in this project"
        )))),
        Err(error) => Err(error),
    }
}

/// Switch a criterion on or off. An id in another project is `NotFound`, not a scope error: the caller is addressing a
/// row that does not exist *here*.
///
/// # Errors
/// `sqlx::Error`.
pub async fn set_active(
    pool: &PgPool,
    project_id: i64,
    criterion_id: i64,
    active: bool,
) -> Result<Result<(), Refused>, sqlx::Error> {
    let updated = sqlx::query(
        "update criteria set active = $1, updated_at = now() where id = $2 and project_id = $3",
    )
    .bind(i64::from(active))
    .bind(criterion_id)
    .bind(project_id)
    .execute(pool)
    .await?;
    if updated.rows_affected() == 0 {
        return Ok(Err(Refused::NotFound));
    }
    Ok(Ok(()))
}

/// One criterion, scoped to its project.
///
/// # Errors
/// `sqlx::Error`.
pub async fn get(
    pool: &PgPool,
    project_id: i64,
    criterion_id: i64,
) -> Result<Option<Criterion>, sqlx::Error> {
    let row = sqlx::query(
        "select id, project_id, slug, name, description, direction, default_weight, active
           from criteria where id = $1 and project_id = $2",
    )
    .bind(criterion_id)
    .bind(project_id)
    .fetch_optional(pool)
    .await?;
    Ok(row.as_ref().map(map_criterion))
}

/// One feature's rating on one criterion, if it has been compared yet.
#[derive(Debug, Clone, Copy, serde::Serialize)]
pub struct Rating {
    /// The feature.
    pub feature_id: i64,
    /// The criterion.
    pub criterion_id: i64,
    /// Glicko rating.
    pub r: f64,
    /// Rating deviation.
    pub rd: f64,
    /// Volatility.
    pub sigma: f64,
    /// How many comparisons it has.
    pub games: i64,
}

/// Read one rating, scoped through the criterion's project.
///
/// # Errors
/// `sqlx::Error`.
pub async fn rating(
    pool: &PgPool,
    project_id: i64,
    criterion_id: i64,
    feature_id: i64,
) -> Result<Option<Rating>, sqlx::Error> {
    let row = sqlx::query(
        "select cr.feature_id, cr.criterion_id, cr.r, cr.rd, cr.sigma, cr.games
           from criterion_ratings cr join criteria c on c.id = cr.criterion_id
          where c.project_id = $1 and cr.criterion_id = $2 and cr.feature_id = $3",
    )
    .bind(project_id)
    .bind(criterion_id)
    .bind(feature_id)
    .fetch_optional(pool)
    .await?;
    Ok(row.map(|row| Rating {
        feature_id: row.get("feature_id"),
        criterion_id: row.get("criterion_id"),
        r: row.get("r"),
        rd: row.get("rd"),
        sigma: row.get("sigma"),
        games: row.get("games"),
    }))
}

/// One criterion comparison, and its effect on both features' ratings.
///
/// The two returned [`Rating`]s are what the voter sees: their comparison's consequence, not a bare acknowledgement.
/// A neutral outcome returns the ratings untouched (and may return none at all, if the pair has never been rated).
///
/// # Errors
/// `sqlx::Error`.
pub async fn cast_vote(
    pool: &PgPool,
    project_id: i64,
    criterion_id: i64,
    voter_id: i64,
    feature_a: i64,
    feature_b: i64,
    outcome: &str,
    weight: f64,
    tau: f64,
) -> Result<Result<(), Refused>, sqlx::Error> {
    let criterion = get(pool, project_id, criterion_id).await?;
    if criterion.is_none() {
        return Ok(Err(Refused::NotFound));
    }
    let outcome = outcome.trim().to_lowercase();
    let Some(parsed) = concord_ranking::Outcome::parse(&outcome) else {
        return Ok(Err(Refused::Invalid(
            "outcome must be a, b, both, neither or skip".to_string(),
        )));
    };
    if feature_a == feature_b {
        return Ok(Err(Refused::Invalid(
            "a feature cannot be compared with itself".to_string(),
        )));
    }
    let owned: i64 = sqlx::query_scalar(
        "select count(*) from features where project_id = $1 and id in ($2, $3)",
    )
    .bind(project_id)
    .bind(feature_a)
    .bind(feature_b)
    .fetch_one(pool)
    .await?;
    if owned != 2 {
        return Ok(Err(Refused::Scope));
    }
    let weight = if weight <= 0.0 { 1.0 } else { weight };

    let mut tx = pool.begin().await?;
    let inserted = sqlx::query(
        "insert into criterion_votes (criterion_id, project_id, feature_a, feature_b, voter_id, outcome,
                                      weight, created_at)
         values ($1, $2, $3, $4, $5, $6, $7, now()) returning id",
    )
    .bind(criterion_id)
    .bind(project_id)
    .bind(feature_a)
    .bind(feature_b)
    .bind(voter_id)
    .bind(&outcome)
    .bind(weight)
    .fetch_one(&mut *tx)
    .await;
    let vote_id: i64 = match inserted {
        Ok(row) => row.get("id"),
        Err(error) if is_unique_violation(&error) => {
            return Ok(Err(Refused::Invalid(
                "already voted on this comparison".to_string(),
            )));
        }
        Err(error) => return Err(error),
    };

    if !parsed.applies_rating_change() {
        // Recorded, audited, and no rating moves: abstention is data, and a neutral vote that left no trace would be
        // indistinguishable from a lost one.
        sqlx::query(
            "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
             values ($1, $2, 'criterion_vote_neutral', 'criterion_vote', $3, $4, now())",
        )
        .bind(project_id)
        .bind(voter_id)
        .bind(vote_id)
        .bind(&outcome)
        .execute(&mut *tx)
        .await?;
        tx.commit().await?;
        return Ok(Ok(()));
    }

    for feature in [feature_a, feature_b] {
        sqlx::query(
            "insert into criterion_ratings (criterion_id, feature_id, r, rd, sigma, games, updated_at)
             values ($1, $2, 1500, 350, 0.06, 0, now()) on conflict do nothing",
        )
        .bind(criterion_id)
        .bind(feature)
        .execute(&mut *tx)
        .await?;
    }
    let row_a = load_rating(&mut tx, criterion_id, feature_a).await?;
    let row_b = load_rating(&mut tx, criterion_id, feature_b).await?;
    let (next_a, next_b) = concord_ranking::apply_pairwise_vote(row_a, row_b, parsed, weight, tau);
    for (feature, rated, previous) in [
        (feature_a, next_a, row_a),
        (feature_b, next_b, row_b),
    ] {
        let games = sqlx::query_scalar::<_, i64>(
            "select games from criterion_ratings where criterion_id = $1 and feature_id = $2",
        )
        .bind(criterion_id)
        .bind(feature)
        .fetch_one(&mut *tx)
        .await?;
        let _ = previous.strategic_weight;
        sqlx::query(
            "update criterion_ratings set r = $1, rd = $2, sigma = $3, games = $4, updated_at = now()
              where criterion_id = $5 and feature_id = $6",
        )
        .bind(rated.r)
        .bind(rated.rd)
        .bind(rated.vol)
        .bind(games + 1)
        .bind(criterion_id)
        .bind(feature)
        .execute(&mut *tx)
        .await?;
    }
    tx.commit().await?;
    Ok(Ok(()))
}

async fn load_rating(
    tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
    criterion_id: i64,
    feature_id: i64,
) -> Result<concord_ranking::Rated, sqlx::Error> {
    let row = sqlx::query(
        "select r, rd, sigma from criterion_ratings where criterion_id = $1 and feature_id = $2",
    )
    .bind(criterion_id)
    .bind(feature_id)
    .fetch_one(&mut **tx)
    .await?;
    Ok(concord_ranking::Rated {
        r: row.get("r"),
        rd: row.get("rd"),
        vol: row.get("sigma"),
        strategic_weight: 0.0,
    })
}

/// Save a weight profile, **replacing** its weights wholesale.
///
/// Merging would leave stale weights for criteria the caller no longer mentions, and a profile that silently keeps them
/// is not the profile that was saved. Re-saving under an existing slug updates in place rather than erroring — the panel
/// is a form with one save button.
///
/// Every criterion is scope-checked **before** the transaction opens: this pool allows many connections, so the deadlock
/// the Go store's comment describes cannot happen here, but doing the check first still gives the caller a precise
/// refusal instead of a foreign-key failure.
///
/// # Errors
/// `sqlx::Error`.
pub async fn save_profile(
    pool: &PgPool,
    project_id: i64,
    actor_id: i64,
    slug: &str,
    name: &str,
    weights: &[Weight],
) -> Result<Result<i64, Refused>, sqlx::Error> {
    let slug = slug.trim().to_lowercase();
    if slug.is_empty() || name.is_empty() {
        return Ok(Err(Refused::Invalid(
            "profile needs a slug and a name".to_string(),
        )));
    }
    for weight in weights {
        let owned: i64 = sqlx::query_scalar("select count(*) from criteria where id = $1 and project_id = $2")
            .bind(weight.criterion_id)
            .bind(project_id)
            .fetch_one(pool)
            .await?;
        if owned != 1 {
            return Ok(Err(Refused::Scope));
        }
        if weight.weight < 0.0 {
            return Ok(Err(Refused::Invalid("negative weight".to_string())));
        }
    }
    let mut tx = pool.begin().await?;
    sqlx::query(
        "insert into criteria_profiles (project_id, slug, name, created_by, created_at, updated_at)
         values ($1, $2, $3, $4, now(), now())
         on conflict (project_id, slug) do update set name = excluded.name, updated_at = excluded.updated_at",
    )
    .bind(project_id)
    .bind(&slug)
    .bind(name)
    .bind(actor_id)
    .execute(&mut *tx)
    .await?;
    let profile_id: i64 =
        sqlx::query_scalar("select id from criteria_profiles where project_id = $1 and slug = $2")
            .bind(project_id)
            .bind(&slug)
            .fetch_one(&mut *tx)
            .await?;
    sqlx::query("delete from criteria_profile_weights where profile_id = $1")
        .bind(profile_id)
        .execute(&mut *tx)
        .await?;
    for weight in weights {
        sqlx::query(
            "insert into criteria_profile_weights (profile_id, criterion_id, weight) values ($1, $2, $3)",
        )
        .bind(profile_id)
        .bind(weight.criterion_id)
        .bind(weight.weight)
        .execute(&mut *tx)
        .await?;
    }
    tx.commit().await?;
    Ok(Ok(profile_id))
}

fn map_criterion(row: &sqlx::postgres::PgRow) -> Criterion {
    Criterion {
        id: row.get("id"),
        project_id: row.get("project_id"),
        slug: row.get("slug"),
        name: row.get("name"),
        description: row.get("description"),
        direction: row.get("direction"),
        default_weight: row.get("default_weight"),
        active: row.get::<i64, _>("active") != 0,
    }
}

fn is_unique_violation(error: &sqlx::Error) -> bool {
    matches!(error, sqlx::Error::Database(db) if db.code().as_deref() == Some("23505"))
}
