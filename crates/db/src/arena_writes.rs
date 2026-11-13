//! §5.1's pairwise vote: recording the comparison and moving both ratings in one period.
//!
//! The rules that are refusals, and the one that is a deliberate no-op:
//!
//! * **both sides must be the same kind of entity** — a solution against a project in one arena is a comparison with no
//!   meaning, and the rating it produces looks exactly like a real one;
//! * **an entry cannot be compared against itself**;
//! * **an author cannot vote on their own entry** (§5.2) — for a project, "author" has to go through `members` with an
//!   owner/maintainer role, because there is no owner column;
//! * **a `skip` changes nothing.** §5.1's skip means "I don't understand this", which is a flag about the write-up, not a
//!   judgement about which is better. It is recorded (so the flag exists) and no rating moves;
//! * **`neither` in an arena with a baseline is two losses to the baseline**, not a draw between the competitors. A draw
//!   would leave both where they were, and the whole point of §5.1's wording is that doing nothing quietly beats them.
//!
//! Both ratings are computed from their **pre-vote** values and written together. Updating A and then scoring B against the
//! new A gives the winner a smaller gain than the loser's loss, which is a bug that reads as reasonable behaviour.

use sqlx::PgPool;

/// Why a vote was refused.
#[derive(Debug)]
pub enum Refused {
    /// The request does not describe a comparison that can be scored.
    Invalid(String),
    /// An entity is not in this arena — or not in this project at all.
    NotFound,
    /// §5.2: an author cannot vote on their own entry.
    SelfVote,
    /// Both sides are the same entry.
    SameEntity,
    /// The database said no.
    Database(sqlx::Error),
}

impl From<sqlx::Error> for Refused {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

/// One competitor's rating as it stands before the vote.
struct Entry {
    r: f64,
    rd: f64,
    sigma: f64,
    is_baseline: bool,
    entity_type: String,
    entity_id: i64,
}

/// Record a comparison and move both ratings.
///
/// `weight` is computed by the caller, never taken from the request body: a client-supplied weight is client-supplied
/// influence.
///
/// # Errors
/// See [`Refused`].
pub async fn cast_vote(
    pool: &PgPool,
    arena_id: i64,
    voter_id: i64,
    a_type: &str,
    a_id: i64,
    b_type: &str,
    b_id: i64,
    outcome: &str,
    reason: &str,
    weight: f64,
) -> Result<i64, Refused> {
    if a_type != b_type {
        return Err(Refused::Invalid(format!(
            "both sides of a comparison must be the same entity type ({a_type} vs {b_type})"
        )));
    }
    if a_id == b_id {
        return Err(Refused::SameEntity);
    }
    if !matches!(outcome, "a" | "b" | "both" | "neither" | "skip") {
        return Err(Refused::Invalid(format!("unknown outcome `{outcome}`")));
    }
    refuse_own_vote(pool, a_type, a_id, voter_id).await?;
    refuse_own_vote(pool, b_type, b_id, voter_id).await?;

    let entry_a = entry(pool, arena_id, a_type, a_id).await?;
    let entry_b = entry(pool, arena_id, b_type, b_id).await?;

    let vote_id: i64 = sqlx::query_scalar(
        "insert into pairwise_votes (arena_id, entity_type, a, b, voter_id, outcome, weight, reason, created_at)
         values ($1,$2,$3,$4,$5,$6,$7,nullif($8,''),now())
         returning id",
    )
    .bind(arena_id)
    .bind(a_type)
    .bind(a_id)
    .bind(b_id)
    .bind(voter_id)
    .bind(outcome)
    .bind(weight)
    .bind(reason.trim())
    .fetch_one(pool)
    .await?;

    if outcome == "skip" {
        return Ok(vote_id);
    }

    match baseline(pool, arena_id).await? {
        Some(baseline) if outcome == "neither" => {
            // §5.1: neither counts as BOTH competitors losing to the baseline. Two games, and the a-vs-b comparison is not
            // scored at all — neither of them won it.
            if baseline.entity_type == a_type && baseline.entity_id == a_id {
                apply_game(pool, arena_id, &entry_a, &entry_b, 0.0, weight).await?;
            } else {
                apply_game(pool, arena_id, &baseline, &entry_a, 1.0, weight).await?;
            }
            if baseline.entity_type == b_type && baseline.entity_id == b_id {
                apply_game(pool, arena_id, &entry_a, &entry_b, 1.0, weight).await?;
            } else {
                apply_game(pool, arena_id, &baseline, &entry_b, 1.0, weight).await?;
            }
        }
        _ => {
            apply_game(pool, arena_id, &entry_a, &entry_b, concord_ranking::outcome_score(outcome), weight)
                .await?;
        }
    }
    Ok(vote_id)
}

/// An entity in the arena, or `NotFound`.
async fn entry(pool: &PgPool, arena_id: i64, entity_type: &str, entity_id: i64) -> Result<Entry, Refused> {
    let row: Option<(f64, f64, f64, bool)> = sqlx::query_as(
        "select r, rd, sigma, is_baseline from arena_entries
          where arena_id = $1 and entity_type = $2 and entity_id = $3",
    )
    .bind(arena_id)
    .bind(entity_type)
    .bind(entity_id)
    .fetch_optional(pool)
    .await?;
    let (r, rd, sigma, is_baseline) = row.ok_or(Refused::NotFound)?;
    Ok(Entry {
        r,
        rd,
        sigma,
        is_baseline,
        entity_type: entity_type.to_string(),
        entity_id,
    })
}

/// The arena's baseline entry, if it has one.
async fn baseline(pool: &PgPool, arena_id: i64) -> Result<Option<Entry>, Refused> {
    let row: Option<(String, i64, f64, f64, f64)> = sqlx::query_as(
        "select entity_type, entity_id, r, rd, sigma from arena_entries
          where arena_id = $1 and is_baseline order by entity_id limit 1",
    )
    .bind(arena_id)
    .fetch_optional(pool)
    .await?;
    Ok(row.map(|(entity_type, entity_id, r, rd, sigma)| Entry {
        r,
        rd,
        sigma,
        is_baseline: true,
        entity_type,
        entity_id,
    }))
}

/// §5.2: an author cannot vote on their own entry.
async fn refuse_own_vote(
    pool: &PgPool,
    entity_type: &str,
    entity_id: i64,
    voter_id: i64,
) -> Result<(), Refused> {
    let author: Option<i64> = match entity_type {
        "feature" => {
            sqlx::query_scalar("select author_id from features where id = $1")
                .bind(entity_id)
                .fetch_optional(pool)
                .await?
        }
        "solution" => {
            sqlx::query_scalar("select author_id from solutions where id = $1")
                .bind(entity_id)
                .fetch_optional(pool)
                .await?
        }
        // A project has no owner column: ownership is a row in `members` with a role. `members` has no surrogate id either,
        // so the deterministic tiebreak has to be on `user_id`.
        "project" => sqlx::query_scalar(
            "select user_id from members where project_id = $1 and role in ('owner','maintainer')
              order by user_id limit 1",
        )
        .bind(entity_id)
        .fetch_optional(pool)
        .await?,
        // A list entry's authorship is not tracked, so there is nothing to check.
        _ => return Ok(()),
    };
    if author == Some(voter_id) {
        return Err(Refused::SelfVote);
    }
    Ok(())
}

/// Score one comparison between two entries and write both ratings.
async fn apply_game(
    pool: &PgPool,
    arena_id: i64,
    a: &Entry,
    b: &Entry,
    a_score: f64,
    weight: f64,
) -> Result<(), Refused> {
    let games = concord_ranking::games_for_weight(weight);
    let games_a = concord_ranking::repeated_game(b.r, b.rd, a_score, weight);
    let games_b = concord_ranking::repeated_game(a.r, a.rd, 1.0 - a_score, weight);
    let (new_ra, new_rda, new_sigma_a) =
        concord_ranking::update_arena_rating(a.r, a.rd, a.sigma, &games_a, weight);
    let (new_rb, new_rdb, new_sigma_b) =
        concord_ranking::update_arena_rating(b.r, b.rd, b.sigma, &games_b, weight);

    for (entry, r, rd, sigma) in [
        (a, new_ra, new_rda, new_sigma_a),
        (b, new_rb, new_rdb, new_sigma_b),
    ] {
        sqlx::query(
            "update arena_entries set r = $1, rd = $2, sigma = $3, games = games + $4, updated_at = now()
              where arena_id = $5 and entity_type = $6 and entity_id = $7",
        )
        .bind(r)
        .bind(rd)
        .bind(sigma)
        .bind(games)
        .bind(arena_id)
        .bind(&entry.entity_type)
        .bind(entry.entity_id)
        .execute(pool)
        .await?;
        // Features keep their own rating columns, so a feature-arena vote has to be mirrored or the priority endpoint — which
        // reads `features.elo_r` — drifts from the arena it is supposed to summarise.
        if entry.entity_type == "feature" {
            let _ = sqlx::query("update features set elo_r = $1, elo_rd = $2 where id = $3")
                .bind(r)
                .bind(rd)
                .bind(entry.entity_id)
                .execute(pool)
                .await;
        }
    }
    Ok(())
}
