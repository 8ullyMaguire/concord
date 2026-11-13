//! §5.1's **feature** vote: the project's priority arena.
//!
//! This is a different arena from the solution one on purpose — it ranks *what to build* — and it has three behaviours the
//! solution vote does not:
//!
//! * **the weight ignores the member's role.** The reference computes `VoteWeight(reputation, 1.0, cap)`: a maintainer's
//!   opinion about which feature matters is worth what their reputation says, not what their title says. That is a
//!   deliberate difference from the solution vote, which does multiply the role in;
//! * **a repeat vote replaces the earlier one.** The reference writes its vote row with `INSERT OR REPLACE`, which relies on
//!   a uniqueness constraint that **does not exist in this schema** (there is no unique index on
//!   `(voter_id, feature_a, feature_b)`, only a primary key). Without the replacement, a voter could cast the same comparison
//!   every second and walk a feature up the board; the rule is therefore enforced here, by hand;
//! * **the vote row carries BOTH shapes**: the legacy `feature_a`/`feature_b` and `project_id` columns that the priority
//!   query reads, and the generic `arena_id`/`entity_type`/`a`/`b` columns that the arena queries and the recompute read. A
//!   row with only one of the two is invisible to one of the readers, and the recompute would rebuild every rating at the
//!   prior and silently erase the ranking.

use sqlx::PgPool;

/// Why a feature vote was refused.
#[derive(Debug)]
pub enum Refused {
    /// No outcome, or an outcome that means nothing.
    Invalid(String),
    /// A feature is not in this project.
    NotFound,
    /// §5.2: an author cannot rank their own feature.
    SelfVote,
    /// The database said no.
    Database(sqlx::Error),
}

impl From<sqlx::Error> for Refused {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

/// What a voter sends.
#[derive(Debug, Clone, Default)]
pub struct NewFeatureVote {
    /// The first feature.
    pub feature_a: i64,
    /// The second.
    pub feature_b: i64,
    /// `a`, `b`, `both`, `neither` or `skip`.
    pub outcome: String,
}

/// Record a feature comparison and move both features' ratings.
///
/// `weight` is computed by the caller from the charter, never from the body.
///
/// # Errors
/// See [`Refused`].
pub async fn record_feature_vote(
    pool: &PgPool,
    project_id: i64,
    voter_id: i64,
    vote: NewFeatureVote,
    weight: f64,
    tau: f64,
) -> Result<i64, Refused> {
    if !matches!(vote.outcome.as_str(), "a" | "b" | "both" | "neither" | "skip") {
        return Err(Refused::Invalid(format!(
            "unknown outcome `{}`",
            vote.outcome
        )));
    }
    if vote.feature_a == vote.feature_b {
        return Err(Refused::Invalid(
            "both sides of a comparison must differ".to_string(),
        ));
    }

    // Both features are read once: their project (so a feature from another project cannot be spent here), their author (so
    // §5.2 can be enforced in either position) and their current rating.
    let mut rated = Vec::new();
    for id in [vote.feature_a, vote.feature_b] {
        let row: Option<(i64, i64, f64, f64, f64)> = sqlx::query_as(
            "select project_id, author_id, elo_r, elo_rd, elo_vol from features where id = $1",
        )
        .bind(id)
        .fetch_optional(pool)
        .await?;
        match row {
            Some((feature_project, _, _, _, _)) if feature_project != project_id => {
                return Err(Refused::NotFound)
            }
            Some((_, author, r, rd, vol)) if author == voter_id => return Err(Refused::SelfVote),
            Some((_, _, r, rd, vol)) => rated.push((r, rd, vol)),
            None => return Err(Refused::NotFound),
        }
    }

    let outcome = match vote.outcome.as_str() {
        "a" => concord_ranking::Outcome::A,
        "b" => concord_ranking::Outcome::B,
        "both" => concord_ranking::Outcome::Both,
        "neither" => concord_ranking::Outcome::Neither,
        _ => concord_ranking::Outcome::Skip,
    };
    let a = concord_ranking::Rated {
        r: rated[0].0,
        rd: rated[0].1,
        vol: rated[0].2,
        strategic_weight: 0.0,
    };
    let b = concord_ranking::Rated {
        r: rated[1].0,
        rd: rated[1].1,
        vol: rated[1].2,
        strategic_weight: 0.0,
    };
    let (new_a, new_b) = concord_ranking::apply_pairwise_vote(a, b, outcome, weight, tau);

    for (id, rated) in [(vote.feature_a, new_a), (vote.feature_b, new_b)] {
        sqlx::query("update features set elo_r = $1, elo_rd = $2, elo_vol = $3 where id = $4")
            .bind(rated.r)
            .bind(rated.rd)
            .bind(rated.vol)
            .bind(id)
            .execute(pool)
            .await?;
    }

    let arena_id = ensure_priority_arena(pool, project_id).await?;

    // §5.1: one live vote per voter per pair. `INSERT OR REPLACE` has no equivalent here because the unique index it relied on
    // was never in this schema, so the earlier row is updated in place. A second row from the same voter on the same pair
    // would be a second rating move for one opinion.
    let existing: Option<i64> = sqlx::query_scalar(
        "select id from pairwise_votes
          where arena_id = $1 and voter_id = $2 and entity_type = 'feature'
            and ((a = $3 and b = $4) or (a = $4 and b = $3))
          order by id limit 1",
    )
    .bind(arena_id)
    .bind(voter_id)
    .bind(vote.feature_a)
    .bind(vote.feature_b)
    .fetch_optional(pool)
    .await?;

    let vote_id = match existing {
        Some(id) => {
            sqlx::query(
                "update pairwise_votes set outcome = $1, weight = $2, created_at = now(),
                        feature_a = $3, feature_b = $4
                  where id = $5",
            )
            .bind(&vote.outcome)
            .bind(weight)
            .bind(vote.feature_a)
            .bind(vote.feature_b)
            .bind(id)
            .execute(pool)
            .await?;
            id
        }
        None => sqlx::query_scalar(
            "insert into pairwise_votes
                (arena_id, project_id, voter_id, feature_a, feature_b, entity_type, a, b, outcome, weight, created_at)
             values ($1,$2,$3,$4,$5,'feature',$4,$5,$6,$7,now())
             returning id",
        )
        .bind(arena_id)
        .bind(project_id)
        .bind(voter_id)
        .bind(vote.feature_a)
        .bind(vote.feature_b)
        .bind(&vote.outcome)
        .bind(weight)
        .fetch_one(pool)
        .await?,
    };
    Ok(vote_id)
}

/// The project's feature-priority arena, created on demand.
///
/// Created on demand because this runs on the vote endpoint and a project made before the arena existed has none until
/// something needs one.
async fn ensure_priority_arena(pool: &PgPool, project_id: i64) -> Result<i64, Refused> {
    let existing: Option<i64> = sqlx::query_scalar(
        "select id from arenas where type = 'feature-priority' and project_id = $1 and feature_id is null",
    )
    .bind(project_id)
    .fetch_optional(pool)
    .await?;
    if let Some(id) = existing {
        return Ok(id);
    }
    let inserted: Option<i64> = sqlx::query_scalar(
        "insert into arenas (type, project_id, feature_id, question, use_case, created_at)
         values ('feature-priority',$1,null,$2,null,now())
         on conflict do nothing
         returning id",
    )
    .bind(project_id)
    .bind("Which feature should the project prioritise?")
    .fetch_optional(pool)
    .await?;
    match inserted {
        Some(id) => Ok(id),
        None => sqlx::query_scalar(
            "select id from arenas where type = 'feature-priority' and project_id = $1 and feature_id is null",
        )
        .bind(project_id)
        .fetch_optional(pool)
        .await?
        .ok_or(Refused::NotFound),
    }
}
