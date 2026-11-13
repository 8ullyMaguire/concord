
//! §16's list writes: creating a list, adding an entry, and comparing two entries.
//!
//! Three decisions, each one a refusal or an omission the reference made deliberately:
//!
//! * **a list is project-scoped or instance-wide, and instance-wide must be ASKED for.** The project in the path is
//!   also the access check, so defaulting to global would let any project member mint an instance-wide list. A NULL
//!   `project_id` is the schema's spelling of "no project" and is written only when the caller says `instance_wide`;
//! * **an entry is proposed, not published.** It starts `proposed` with no vote behind it, and `proposed_by` is the
//!   proposer — which is what makes the self-vote refusal below enforceable;
//! * **an entry's comparison uses the same §8.3 weight as any other vote** and refuses to move a **removed** entry, to
//!   compare an entry with itself, to compare entries from another list, and to let a proposer vote on their own entry.

use sqlx::PgPool;

use crate::criteria::{Entry, ProjectList};

/// Why a list write was refused.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Refused {
    /// The request is well-formed and wrong.
    Invalid(String),
    /// The caller may not do this: a self-vote, or a second comparison of the same pair.
    Forbidden(String),
    /// No such list or entry (or it belongs elsewhere, which is the same answer to the caller).
    NotFound,
}

/// Create a list. `project_id` is `None` for an instance-wide one.
///
/// No slug or title validation is added here: the reference has none, and inventing a rule would reject calls the
/// reference accepts. A duplicate slug is a database error and surfaces as one — the read side already tolerates it, and
/// tightening it is a product decision, not a port.
///
/// # Errors
/// `sqlx::Error`.
pub async fn create(
    pool: &PgPool,
    project_id: Option<i64>,
    created_by: i64,
    slug: &str,
    title: &str,
    description: &str,
) -> Result<ProjectList, sqlx::Error> {
    let id: i64 = sqlx::query_scalar(
        "insert into lists (project_id, slug, title, description, status, created_by, created_at, updated_at)
         values ($1, $2, $3, $4, 'open', $5, now(), now()) returning id",
    )
    .bind(project_id)
    .bind(slug)
    .bind(title)
    .bind(description)
    .bind(created_by)
    .fetch_one(pool)
    .await?;
    Ok(crate::criteria::list_by_id(pool, id)
        .await?
        .expect("the row was just inserted"))
}

/// Add one entry to a list, `proposed`.
///
/// # Errors
/// `sqlx::Error`.
pub async fn add_entry(
    pool: &PgPool,
    list_id: i64,
    proposed_by: i64,
    url: &str,
    title: &str,
    description: &str,
) -> Result<Entry, sqlx::Error> {
    let id: i64 = sqlx::query_scalar(
        "insert into list_entries (list_id, url, title, description, status, proposed_by, created_at, updated_at)
         values ($1, $2, $3, $4, 'proposed', $5, now(), now()) returning id",
    )
    .bind(list_id)
    .bind(url)
    .bind(title)
    .bind(description)
    .bind(proposed_by)
    .fetch_one(pool)
    .await?;
    let entries = crate::criteria::entries(pool, list_id).await?;
    Ok(entries
        .into_iter()
        .find(|entry| entry.id == id)
        .expect("the row was just inserted"))
}

/// Compare two entries of one list, moving both elo ratings by the voter's weight.
///
/// The five outcomes and their effects are [`concord_ranking::Outcome`]'s, shared with every other comparison surface:
/// `a`, `b` move a rating; `both`, `neither` and `skip` are recorded and move nothing.
///
/// # Errors
/// `sqlx::Error`.
pub async fn vote_entry(
    pool: &PgPool,
    list_id: i64,
    entry_a: i64,
    entry_b: i64,
    voter_id: i64,
    outcome: &str,
    weight: f64,
    tau: f64,
) -> Result<Result<(), Refused>, sqlx::Error> {
    let outcome = outcome.trim().to_lowercase();
    let Some(parsed) = concord_ranking::Outcome::parse(&outcome) else {
        return Ok(Err(Refused::Invalid(
            "outcome must be a, b, both, neither or skip".to_string(),
        )));
    };
    if entry_a == entry_b {
        return Ok(Err(Refused::Invalid(
            "cannot compare an entry with itself".to_string(),
        )));
    }
    let mut authors = Vec::with_capacity(2);
    for entry in [entry_a, entry_b] {
        let row: Option<(Option<i64>, i64, String)> = sqlx::query_as(
            "select proposed_by, list_id, status from list_entries where id = $1",
        )
        .bind(entry)
        .fetch_optional(pool)
        .await?;
        let Some((proposer, list, status)) = row else {
            return Ok(Err(Refused::NotFound));
        };
        if list != list_id {
            return Ok(Err(Refused::Invalid(format!(
                "entry {entry} belongs to list {list}, not {list_id}"
            ))));
        }
        // A removed entry is not a candidate for "which of these two is better".
        if status == "removed" {
            return Ok(Err(Refused::Invalid(format!(
                "entry {entry} has been removed"
            ))));
        }
        authors.push(proposer);
    }
    if authors.iter().flatten().any(|author| *author == voter_id) {
        return Ok(Err(Refused::Forbidden(
            "you cannot vote on an entry you proposed".to_string(),
        )));
    }

    let mut tx = pool.begin().await?;
    let inserted = sqlx::query(
        "insert into list_entry_votes (list_id, entry_a, entry_b, voter_id, outcome, weight, created_at)
         values ($1, $2, $3, $4, $5, $6, now())",
    )
    .bind(list_id)
    .bind(entry_a)
    .bind(entry_b)
    .bind(voter_id)
    .bind(&outcome)
    .bind(weight)
    .execute(&mut *tx)
    .await;
    if let Err(error) = inserted {
        if matches!(&error, sqlx::Error::Database(db) if db.code().as_deref() == Some("23505")) {
            return Ok(Err(Refused::Forbidden(
                "you have already compared these two entries".to_string(),
            )));
        }
        return Err(error);
    }
    if parsed.applies_rating_change() && weight > 0.0 {
        let load = |id: i64| async move {
            sqlx::query_as::<_, (f64, f64, f64)>(
                "select elo_r, elo_rd, elo_vol from list_entries where id = $1",
            )
            .bind(id)
            .fetch_one(pool)
            .await
        };
        let (ra, rd_a, vol_a) = load(entry_a).await?;
        let (rb, rd_b, vol_b) = load(entry_b).await?;
        let (next_a, next_b) = concord_ranking::apply_pairwise_vote(
            concord_ranking::Rated {
                r: ra,
                rd: rd_a,
                vol: vol_a,
                strategic_weight: 0.0,
            },
            concord_ranking::Rated {
                r: rb,
                rd: rd_b,
                vol: vol_b,
                strategic_weight: 0.0,
            },
            parsed,
            weight,
            tau,
        );
        for (entry, rated) in [(entry_a, next_a), (entry_b, next_b)] {
            sqlx::query(
                "update list_entries set elo_r = $1, elo_rd = $2, elo_vol = $3, updated_at = now()
                  where id = $4",
            )
            .bind(rated.r)
            .bind(rated.rd)
            .bind(rated.vol)
            .bind(entry)
            .execute(&mut *tx)
            .await?;
        }
    }
    tx.commit().await?;
    Ok(Ok(()))
}
