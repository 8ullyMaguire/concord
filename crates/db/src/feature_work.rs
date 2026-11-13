//! What a feature costs a newcomer, who has picked it up, what it depends on, and what it cites (§4.5.7, R10, R18, §3.4).
//!
//! Five rules, each of them a refusal or a deliberate omission:
//!
//! * **a claim never moves the feature.** The board is the only mechanism that moves work; two mechanisms that both move
//!   it is how a board and a feed start disagreeing about what is being built;
//! * **only `ready` and `in_progress` work is claimable.** A draft is work nobody has agreed to do, so a claim on one is
//!   a promise made on the project's behalf;
//! * **one live claim per feature**, enforced by a partial unique index (`… where released_at is null`): a released claim
//!   is history, and "picked up and dropped twice" has to stay answerable;
//! * **a dependency edge that would close a cycle is refused with the path**, not with "invalid": a graph with a cycle can
//!   never be started, and the reader who built it needs to see what they closed. Only `shipped` clears a blocker — a
//!   rejected blocker is still a blocker, because the refusal is a decision somebody has to revisit;
//! * **evidence is scoped to the project twice** (the feature, and the document or field report it names), because a
//!   reference id is global and a citation from another project's document is a citation of something else.

use sqlx::{PgPool, Row};

/// Which statuses a claim may be taken on.
const CLAIMABLE: [&str; 2] = ["ready", "in_progress"];

/// Why one of these writes was refused.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Refused {
    /// The request is well-formed and wrong.
    Invalid(String),
    /// No such feature (or document, or field report) in this project.
    NotFound,
    /// Somebody else holds the claim, or a proposal is already open.
    Held(String),
}

/// One thing a person can pick up.
#[derive(Debug, Clone, serde::Serialize)]
pub struct WorkItem {
    /// Always `feature` — the feed's other kinds are not ported yet, and a field that is always the same value is still
    /// worth carrying because the page keys off it.
    pub kind: String,
    /// The feature.
    pub id: i64,
    /// The project.
    pub project_id: i64,
    /// What it is.
    pub title: String,
    /// Where it is on the board.
    pub status: String,
    /// `S`, `M`, `L`, or empty when nobody sized it.
    pub difficulty: String,
    /// The offered time budget in minutes, or `null` — "no budget stated" and "a budget of zero" are different claims.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub time_budget_minutes: Option<i64>,
    /// Whether it needs a mentor.
    pub needs_mentor: bool,
    /// Who holds the live claim, if anyone.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub claimed_by: Option<i64>,
    /// The impact ratio, when one has been computed.
    pub impact_ratio: f64,
}

/// One feature, as a dependency edge needs to name it.
#[derive(Debug, Clone, serde::Serialize)]
pub struct FeatureRef {
    /// Which idea.
    pub id: i64,
    /// What it is called.
    pub title: String,
    /// Where it is.
    pub status: String,
}

/// Read one feature as a work item.
///
/// # Errors
/// `sqlx::Error`.
pub async fn work_item(
    pool: &PgPool,
    project_id: i64,
    feature_id: i64,
) -> Result<Option<WorkItem>, sqlx::Error> {
    let row = sqlx::query(
        // Every expression is ALIASED. `row.get("difficulty")` looks up the column NAME PostgreSQL reports, and a bare
        // `coalesce(f.difficulty, '')` is reported as `coalesce` — the first run of this panicked the worker with
        // ColumnNotFound("difficulty"), which is a 500 with a stack trace rather than a test failure.
        "select f.id, f.project_id, f.title, f.status,
                coalesce(f.difficulty, '') as difficulty,
                f.time_budget_minutes as time_budget_minutes,
                coalesce(f.needs_mentor, false) as needs_mentor,
                coalesce(f.impact_ratio, 0)::double precision as impact_ratio,
                (select wc.user_id from work_item_claims wc
                  where wc.feature_id = f.id and wc.released_at is null) as claimed_by
           from features f where f.id = $1 and f.project_id = $2",
    )
    .bind(feature_id)
    .bind(project_id)
    .fetch_optional(pool)
    .await?;
    Ok(row.map(|row| WorkItem {
        kind: "feature".to_string(),
        id: row.get("id"),
        project_id: row.get("project_id"),
        title: row.get("title"),
        status: row.get("status"),
        difficulty: row.get("difficulty"),
        time_budget_minutes: row.get("time_budget_minutes"),
        needs_mentor: row.get("needs_mentor"),
        claimed_by: row.get("claimed_by"),
        impact_ratio: row.get("impact_ratio"),
    }))
}

/// Take the single live claim on a feature.
///
/// # Errors
/// `sqlx::Error`.
pub async fn claim(
    pool: &PgPool,
    project_id: i64,
    feature_id: i64,
    user_id: i64,
) -> Result<Result<(), Refused>, sqlx::Error> {
    let status: Option<String> =
        sqlx::query_scalar("select status from features where id = $1 and project_id = $2")
            .bind(feature_id)
            .bind(project_id)
            .fetch_optional(pool)
            .await?;
    let Some(status) = status else {
        return Ok(Err(Refused::NotFound));
    };
    if !CLAIMABLE.contains(&status.as_str()) {
        return Ok(Err(Refused::Invalid(format!(
            "a {status} feature cannot be claimed; only ready or in_progress work can"
        ))));
    }
    let inserted = sqlx::query(
        "insert into work_item_claims (feature_id, user_id, claimed_at) values ($1, $2, now())",
    )
    .bind(feature_id)
    .bind(user_id)
    .execute(pool)
    .await;
    if let Err(error) = inserted {
        if matches!(&error, sqlx::Error::Database(db) if db.code().as_deref() == Some("23505")) {
            return Ok(Err(Refused::Held(format!(
                "another contributor holds the claim on feature {feature_id}"
            ))));
        }
        return Err(error);
    }
    Ok(Ok(()))
}

/// Withdraw a claim. The row stays with `released_at` set.
///
/// # Errors
/// `sqlx::Error`.
pub async fn release_claim(
    pool: &PgPool,
    project_id: i64,
    feature_id: i64,
    user_id: i64,
) -> Result<Result<(), Refused>, sqlx::Error> {
    let released = sqlx::query(
        "update work_item_claims set released_at = now()
          where feature_id = $1 and user_id = $2 and released_at is null
            and feature_id in (select id from features where project_id = $3)",
    )
    .bind(feature_id)
    .bind(user_id)
    .bind(project_id)
    .execute(pool)
    .await?;
    if released.rows_affected() == 0 {
        return Ok(Err(Refused::NotFound));
    }
    Ok(Ok(()))
}

/// Record what a task costs. Maintainer-only at the HTTP layer: labelling something "good first" is a promise about
/// review capacity, and only somebody who will review it can make that promise.
///
/// `difficulty` is `S`, `M`, `L`, or `None`/empty for "not sized". An empty string **clears** it rather than storing
/// `''`, which would render as a chip with nothing in it. A budget of zero or less is refused by the reference and by this
/// function: the schema allows it, and a task someone says finishes in no time is a claim that misleads the person who
/// picks it up.
///
/// # Errors
/// `sqlx::Error`.
pub async fn set_work_item_label(
    pool: &PgPool,
    project_id: i64,
    feature_id: i64,
    difficulty: Option<&str>,
    time_budget_minutes: Option<i64>,
    needs_mentor: bool,
) -> Result<Result<(), Refused>, sqlx::Error> {
    let difficulty = match difficulty {
        None => None,
        Some("") => None,
        Some("S") | Some("M") | Some("L") => difficulty,
        Some(other) => {
            return Ok(Err(Refused::Invalid(format!(
                "difficulty must be S, M or L, got {other:?}"
            ))));
        }
    };
    if matches!(time_budget_minutes, Some(minutes) if minutes <= 0) {
        return Ok(Err(Refused::Invalid(
            "time budget must be a positive number of minutes".to_string(),
        )));
    }
    let updated = sqlx::query(
        "update features set difficulty = $1, time_budget_minutes = $2, needs_mentor = $3, updated_at = now()
          where id = $4 and project_id = $5",
    )
    .bind(difficulty)
    .bind(time_budget_minutes)
    .bind(needs_mentor)
    .bind(feature_id)
    .bind(project_id)
    .execute(pool)
    .await?;
    if updated.rows_affected() == 0 {
        // Zero rows is not "nothing to change": the UPDATE matches on both keys, so it means the feature does not exist
        // in THIS project. Reporting success would let a maintainer of one project label another's feature and be told
        // it worked.
        return Ok(Err(Refused::NotFound));
    }
    Ok(Ok(()))
}

/// Record that `blocker` must ship before `blocked` starts.
///
/// # Errors
/// `sqlx::Error`.
pub async fn add_dependency(
    pool: &PgPool,
    project_id: i64,
    blocker: i64,
    blocked: i64,
) -> Result<Result<(), Refused>, sqlx::Error> {
    if blocker == 0 || blocked == 0 {
        return Ok(Err(Refused::Invalid(
            "blocker_id and blocked_id are required".to_string(),
        )));
    }
    if blocker == blocked {
        return Ok(Err(Refused::Invalid(
            "a feature cannot block itself".to_string(),
        )));
    }
    for id in [blocker, blocked] {
        let owner: Option<i64> = sqlx::query_scalar("select project_id from features where id = $1")
            .bind(id)
            .fetch_optional(pool)
            .await?;
        // Both ends must belong to the project in the path, or a maintainer of one project reaches into another's graph.
        if owner != Some(project_id) {
            return Ok(Err(Refused::NotFound));
        }
    }
    // Would `blocker` already wait on `blocked`, transitively? Adding the edge would close the cycle.
    if let Some(path) = path_to(pool, blocked, blocker).await? {
        return Ok(Err(Refused::Invalid(format!(
            "that would close a cycle: {}",
            describe_cycle(blocker, &path, blocked)
        ))));
    }
    sqlx::query(
        "insert into feature_deps (blocker_id, blocked_id, created_at) values ($1, $2, now())
         on conflict do nothing",
    )
    .bind(blocker)
    .bind(blocked)
    .execute(pool)
    .await?;
    Ok(Ok(()))
}

/// Delete an edge. Removing one that does not exist is a **no-op**: the caller's intent ("this must not be a dependency")
/// is satisfied either way.
///
/// # Errors
/// `sqlx::Error`.
pub async fn remove_dependency(
    pool: &PgPool,
    project_id: i64,
    blocker: i64,
    blocked: i64,
) -> Result<Result<(), Refused>, sqlx::Error> {
    let owner: Option<i64> = sqlx::query_scalar("select project_id from features where id = $1")
        .bind(blocked)
        .fetch_optional(pool)
        .await?;
    if owner != Some(project_id) {
        return Ok(Err(Refused::NotFound));
    }
    sqlx::query("delete from feature_deps where blocker_id = $1 and blocked_id = $2")
        .bind(blocker)
        .bind(blocked)
        .execute(pool)
        .await?;
    Ok(Ok(()))
}

/// The blockers of a feature that have not shipped.
///
/// # Errors
/// `sqlx::Error`.
pub async fn open_blockers(pool: &PgPool, feature_id: i64) -> Result<Vec<FeatureRef>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (i64, String, String)>(
        "select f.id, f.title, f.status
           from feature_deps dep join features f on f.id = dep.blocker_id
          where dep.blocked_id = $1 and f.status <> 'shipped'
          order by f.id",
    )
    .bind(feature_id)
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(|(id, title, status)| FeatureRef { id, title, status })
        .collect())
}

/// Breadth-first walk from `from` to `target` along `blocker -> blocked`.
///
/// **The direction is the whole check.** The edge points blocker → blocked, so the walk follows `blocked_id` given
/// `blocker_id`; walking the other way answers a different question ("what is waiting on me?") and finds no cycle, which
/// is how the first version of this let every cycle through.
async fn path_to(pool: &PgPool, from: i64, target: i64) -> Result<Option<Vec<i64>>, sqlx::Error> {
    const MAX_DEPTH: usize = 64;
    let mut visited = vec![from];
    let mut queue: Vec<Vec<i64>> = vec![vec![from]];
    while !queue.is_empty() {
        let path = queue.remove(0);
        let current = *path.last().expect("a path is never empty");
        if current == target {
            return Ok(Some(path));
        }
        if path.len() > MAX_DEPTH {
            return Ok(Some(path));
        }
        let next: Vec<i64> = sqlx::query_scalar(
            "select blocked_id from feature_deps where blocker_id = $1 order by blocked_id",
        )
        .bind(current)
        .fetch_all(pool)
        .await?;
        for id in next {
            if visited.contains(&id) {
                continue;
            }
            visited.push(id);
            let mut extended = path.clone();
            extended.push(id);
            queue.push(extended);
        }
    }
    Ok(None)
}

fn describe_cycle(blocker: i64, path: &[i64], blocked: i64) -> String {
    let mut parts = Vec::new();
    for pair in path.windows(2) {
        parts.push(format!("#{} blocks #{}", pair[0], pair[1]));
    }
    parts.push(format!("#{blocker} blocks #{blocked}"));
    parts.join(", ")
}

/// The two kinds of evidence a feature may cite (§3.4): a document, or a field report.
pub const EVIDENCE_KINDS: [&str; 2] = ["document", "field_report"];

/// Attach a citation to a feature.
///
/// Scoped twice: the feature must be this project's, and the **cited row** must be too. A reference id is global, so
/// without the second check a feature can cite another project's document and the page will happily render its title.
///
/// # Errors
/// `sqlx::Error`.
pub async fn add_evidence(
    pool: &PgPool,
    project_id: i64,
    feature_id: i64,
    kind: &str,
    ref_id: i64,
) -> Result<Result<(), Refused>, sqlx::Error> {
    if !EVIDENCE_KINDS.contains(&kind) {
        return Ok(Err(Refused::Invalid(format!(
            "kind must be \"document\" or \"field_report\", got {kind:?}"
        ))));
    }
    let owner: Option<i64> = sqlx::query_scalar("select project_id from features where id = $1")
        .bind(feature_id)
        .fetch_optional(pool)
        .await?;
    if owner != Some(project_id) {
        return Ok(Err(Refused::NotFound));
    }
    let table = if kind == "field_report" {
        "field_reports"
    } else {
        "project_documents"
    };
    let cited: Option<i64> = sqlx::query_scalar(&format!("select project_id from {table} where id = $1"))
        .bind(ref_id)
        .fetch_optional(pool)
        .await?;
    match cited {
        None => return Ok(Err(Refused::NotFound)),
        Some(project) if project != project_id => return Ok(Err(Refused::NotFound)),
        Some(_) => {}
    }
    sqlx::query(
        "insert into feature_evidence (feature_id, kind, ref_id, created_at) values ($1, $2, $3, now())
         on conflict do nothing",
    )
    .bind(feature_id)
    .bind(kind)
    .bind(ref_id)
    .execute(pool)
    .await?;
    Ok(Ok(()))
}

/// Remove one citation.
///
/// A feature that is left with **no** evidence stays where it is: the rule is enforced on the next transition to `ready`,
/// not retroactively, because silently un-readying work somebody is doing is worse than letting the next reader see it.
///
/// # Errors
/// `sqlx::Error`.
pub async fn remove_evidence(
    pool: &PgPool,
    project_id: i64,
    feature_id: i64,
    kind: &str,
    ref_id: i64,
) -> Result<Result<(), Refused>, sqlx::Error> {
    let owner: Option<i64> = sqlx::query_scalar("select project_id from features where id = $1")
        .bind(feature_id)
        .fetch_optional(pool)
        .await?;
    if owner != Some(project_id) {
        return Ok(Err(Refused::NotFound));
    }
    sqlx::query("delete from feature_evidence where feature_id = $1 and kind = $2 and ref_id = $3")
        .bind(feature_id)
        .bind(kind)
        .bind(ref_id)
        .execute(pool)
        .await?;
    Ok(Ok(()))
}
