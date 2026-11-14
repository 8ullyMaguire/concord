//! The contribution feed's reads, and the two pieces of plumbing it and `/votes/next` share.
//!
//! The feed answers "what can I do today, on something healthy, with the skills I have?" and it is deliberately **not a second
//! finder**: the project set, the constraint arithmetic and the ranking all come from [`concord_finder`], and the only thing this
//! module adds is the rows — unclaimed work items instead of projects — and the health score, which is the one signal the project
//! ranking does not already weigh.
//!
//! Two reads here exist for reasons worth stating:
//!
//! * [`health`] reads `project_metrics.health_score` rather than recomputing it from the signals. The score is written by the
//!   health sync and is what every other surface shows; recomputing it here from `project_metrics`' inputs would be a second
//!   implementation of [`concord_ranking::health`] that could disagree with the stored number at the same instant;
//! * [`next_pair`] is the plain-`r` closest-pair rule the reference uses for `/votes/next`, and it is NOT the arena's
//!   `next_pair`. The arena picks by information gain among entries that have a rating; this picks the two features whose
//!   `elo_r` is closest and that this voter has not judged, which is how the reference's vote page behaves and how its rows are
//!   already distributed.

use sqlx::PgPool;

use crate::feature_work::WorkItem;

/// The statuses that are still open to comparison, from the reference's own query: a merged or rejected feature is not something
/// to ask anyone to rank.
const COMPARABLE: [&str; 5] = ["draft", "discussion", "consensus", "ready", "in_progress"];

/// Every work item in a project, oldest first.
///
/// # Errors
/// `sqlx::Error`.
pub async fn work_items(pool: &PgPool, project_id: i64) -> Result<Vec<WorkItem>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (i64, i64, String, String, String, Option<i64>, bool, Option<i64>, f64)>(
        // `claimed_by` is NOT a column on `features`: a claim is a row in `work_item_claims` that has not been released, which is
        // what makes the history of who held a task survive the release. The live claim is the newest unreleased one.
        "select f.id, f.project_id, f.title, f.status,
                coalesce(f.difficulty, '')::text, f.time_budget_minutes,
                coalesce(f.needs_mentor, false),
                (select c.user_id from work_item_claims c
                  where c.feature_id = f.id and c.released_at is null
                  order by c.claimed_at desc, c.user_id limit 1),
                coalesce(f.impact_ratio, 0)::double precision
           from features f
          where f.project_id = $1
          order by f.id",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(|(id, project_id, title, status, difficulty, minutes, needs_mentor, claimed_by, impact)| {
            WorkItem {
                kind: "feature".to_string(),
                id,
                project_id,
                title,
                status,
                difficulty,
                time_budget_minutes: minutes,
                needs_mentor,
                claimed_by,
                impact_ratio: impact,
            }
        })
        .collect())
}

/// The stored health score of each of these projects, keyed by project id.
///
/// `None` means "not measured", which is not the same as zero and must not be rendered as zero: a project nobody has measured has
/// not been found unhealthy. The map omits projects with no `project_metrics` row for exactly that reason.
///
/// # Errors
/// `sqlx::Error`.
pub async fn health(
    pool: &PgPool,
    project_ids: &[i64],
) -> Result<std::collections::BTreeMap<i64, Option<f64>>, sqlx::Error> {
    if project_ids.is_empty() {
        return Ok(std::collections::BTreeMap::new());
    }
    let rows = sqlx::query_as::<_, (i64, Option<f64>)>(
        "select project_id, health_score::double precision from project_metrics where project_id = any($1)",
    )
    .bind(project_ids)
    .fetch_all(pool)
    .await?;
    let mut out = std::collections::BTreeMap::new();
    for (id, score) in rows {
        out.insert(id, score);
    }
    // A project with no metrics row is present with `None`, so a caller iterating the map sees every project it asked about.
    for id in project_ids {
        out.entry(*id).or_insert(None);
    }
    Ok(out)
}

/// Add `user_id` to `project_id` as a contributor if they are not already a member.
///
/// `on conflict do nothing` rather than a read-then-write: joining twice is not an error (`/join` is idempotent, and `/votes/next`
/// joins as a side effect before it shows a pair), and doing it in one statement is what makes two concurrent joins agree.
///
/// # Errors
/// `sqlx::Error`.
pub async fn join(pool: &PgPool, project_id: i64, user_id: i64) -> Result<(), sqlx::Error> {
    sqlx::query(
        "insert into members (project_id, user_id, role, joined_at)
         values ($1, $2, 'contributor', now())
         on conflict do nothing",
    )
    .bind(project_id)
    .bind(user_id)
    .execute(pool)
    .await?;
    Ok(())
}

/// This user's role in the project, or `None` when they are not a member.
///
/// # Errors
/// `sqlx::Error`.
pub async fn role_for(pool: &PgPool, project_id: i64, user_id: i64) -> Result<Option<String>, sqlx::Error> {
    sqlx::query_scalar("select role from members where project_id = $1 and user_id = $2")
        .bind(project_id)
        .bind(user_id)
        .fetch_optional(pool)
        .await
}

/// Why no pair could be offered.
#[derive(Debug)]
pub enum NoPair {
    /// The project has fewer than two comparable features.
    TooFewFeatures,
    /// Every pair this voter could be shown has already been judged.
    AllCompared,
}

/// The two features this voter should compare next: the closest-rated unjudged pair.
///
/// The rule is the reference's, including its consequences: pairs are ordered by `abs(r_a - r_b)`, so the vote page asks about the
/// two features the arena cannot yet tell apart — which is where one vote carries the most information. `AllCompared` is a
/// distinct answer from `TooFewFeatures` because a client can act on one of them (stop asking) and not the other (wait for more
/// features).
///
/// # Errors
/// `sqlx::Error`.
pub async fn next_pair(
    pool: &PgPool,
    project_id: i64,
    voter_id: i64,
) -> Result<Result<(crate::features::Feature, crate::features::Feature), NoPair>, sqlx::Error> {
    let rows: Vec<(i64, f64)> = sqlx::query_as(
        "select id, coalesce(elo_r, 1500)::double precision from features
          where project_id = $1 and status = any($2)
          order by id",
    )
    .bind(project_id)
    .bind(&COMPARABLE[..])
    .fetch_all(pool)
    .await?;
    if rows.len() < 2 {
        return Ok(Err(NoPair::TooFewFeatures));
    }
    let judged: Vec<(i64, i64)> =
        sqlx::query_as("select feature_a, feature_b from pairwise_votes where project_id = $1 and voter_id = $2")
            .bind(project_id)
            .bind(voter_id)
            .fetch_all(pool)
            .await?;
    let mut seen = std::collections::HashSet::new();
    for (a, b) in judged {
        seen.insert((a.min(b), a.max(b)));
    }
    let mut best: Option<(f64, i64, i64)> = None;
    for (index, (id_a, r_a)) in rows.iter().enumerate() {
        for (id_b, r_b) in rows.iter().skip(index + 1) {
            if seen.contains(&((*id_a).min(*id_b), (*id_a).max(*id_b))) {
                continue;
            }
            let difference = (r_a - r_b).abs();
            if best.is_none_or(|(closest, _, _)| difference < closest) {
                best = Some((difference, *id_a, *id_b));
            }
        }
    }
    let Some((_, id_a, id_b)) = best else {
        return Ok(Err(NoPair::AllCompared));
    };
    let Some(first) = crate::features::feature(pool, id_a).await? else {
        return Ok(Err(NoPair::TooFewFeatures));
    };
    let Some(second) = crate::features::feature(pool, id_b).await? else {
        return Ok(Err(NoPair::TooFewFeatures));
    };
    Ok(Ok((first, second)))
}
