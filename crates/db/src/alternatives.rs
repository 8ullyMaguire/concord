//! Alternatives arenas — S2's four routes (§2 of the alternatives-panel spec).
//!
//! The point of all four is that they are **wiring**. The arena kind, the entry entity (`project`), the rating maths
//! ([`crate::arena_writes::cast_vote`]) and the leaderboard ([`crate::arena::leaderboard`]) already existed and already worked;
//! no arena of this kind had ever been created, so every live arena was feature-priority. This module is what makes the kind
//! reachable.
//!
//! Two rules are load-bearing enough to state here, because both fail invisibly:
//!
//! * **no baseline.** §6.3's "do nothing" is a SOLUTION concept. In an alternatives arena "do nothing" means "keep using the
//!   incumbent", which is the arena's own subject rather than a competitor to it. Nothing here inserts a baseline, and a baseline
//!   appearing would render as a legitimate-looking ranked entry;
//! * **visibility on both sides** — enforced in the app layer, where every other visibility rule lives: a competitor the caller
//!   cannot read cannot be added, and must not be ranked into a response the caller can see.

use sqlx::PgPool;

/// The arena kind that ranks competing projects.
pub const KIND: &str = "alternatives";

/// The question an alternatives arena asks when the caller does not supply one.
pub const DEFAULT_QUESTION: &str = "Which is the better alternative?";

/// The `use-case` kind, which ranks projects by fit to a stated use case.
pub const KIND_USE_CASE: &str = "use-case";

/// Why an alternatives write was refused.
#[derive(Debug)]
pub enum Refused {
    /// The request does not describe an arena that can exist.
    Invalid(String),
    /// No such arena in this project.
    NotFound,
    /// The arena is not one that ranks projects.
    WrongKind(String),
    /// The database said no.
    Database(sqlx::Error),
}

impl From<sqlx::Error> for Refused {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

/// One arena, with its ranked competitors.
#[derive(Debug, Clone, serde::Serialize)]
pub struct AlternativeArena {
    /// The arena.
    pub arena_id: i64,
    /// What it is for — the key an arena of this kind is created against.
    pub use_case: String,
    /// What the voter is asked.
    pub question: String,
    /// How many competitors this caller can see. Recounted from the rendered rows, not from the arena's own count: a panel that
    /// says "4 competitors" above three visible rows is a contradiction, and the count is the sentence a reader acts on.
    pub entry_count: usize,
    /// The ranked competitors.
    pub entries: Vec<AlternativeEntry>,
}

/// One ranked competitor.
#[derive(Debug, Clone, serde::Serialize)]
pub struct AlternativeEntry {
    /// 1-based position in the arena's real ranking.
    pub rank: usize,
    /// The competing project.
    pub project_id: i64,
    /// Its slug.
    pub slug: String,
    /// Its name. The JSON field is `title` because that is what the page calls it.
    pub title: String,
    /// Its rating.
    pub rating: f64,
    /// Its deviation.
    pub rd: f64,
    /// How many comparisons it has been in.
    pub games: i64,
    /// The use case this arena asks about.
    pub better_for: String,
    /// The other half of §7.3's pair — see [`not_for_use_case`].
    pub worse_for: String,
}

/// The "worse for" half of a §7.3 better-for/worse-for pair.
///
/// A use case is stated positively ("a team that needs audit logs"), so its negative is not a negation of the words but the
/// question the arena answers: a competitor ranked below the leader is worse FOR that use case. Stated as the question rather
/// than as "not a team that needs audit logs", because the latter reads as a claim that the project is unsuitable for the use
/// case — which is precisely what an arena cannot tell you. It ranks competitors; it does not disqualify its subject.
#[must_use]
pub fn not_for_use_case(use_case: &str) -> String {
    let trimmed = use_case.trim();
    if trimmed.is_empty() {
        String::new()
    } else {
        format!("worse than the leader for {trimmed}")
    }
}

/// This project's arenas of one kind, oldest first.
///
/// # Errors
/// `sqlx::Error`.
pub async fn list(pool: &PgPool, project_id: i64, kind: &str) -> Result<Vec<crate::arena::Arena>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (i64, i64, String, String, String)>(
        "select id, project_id, type, coalesce(question, ''), coalesce(use_case, '')
           from arenas where project_id = $1 and type = $2 order by id",
    )
    .bind(project_id)
    .bind(kind)
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(|(id, project_id, entity_type, question, use_case)| crate::arena::Arena {
            id,
            project_id,
            entity_type,
            question,
            use_case,
        })
        .collect())
}

/// Create the arena for `(project, use_case)` if it does not exist, and return it either way.
///
/// `use_case` is REQUIRED and empty is refused rather than defaulted: the arena is keyed on it, and an empty one would collide with
/// the other arena shapes (the feature-priority arena keys on `use_case IS NULL`). The question, by contrast, has a default —
/// the key must be the caller's, the prompt need not be.
///
/// # Errors
/// [`Refused::Invalid`] for an empty use case, [`Refused::NotFound`] if the arena cannot be read back.
pub async fn ensure(
    pool: &PgPool,
    project_id: i64,
    use_case: &str,
    question: &str,
) -> Result<crate::arena::Arena, Refused> {
    let use_case = use_case.trim();
    if use_case.is_empty() {
        return Err(Refused::Invalid(
            "use_case is required: an alternatives arena is keyed on it, and an empty one collides with the \
             other arena kinds"
                .to_string(),
        ));
    }
    let question = if question.trim().is_empty() {
        DEFAULT_QUESTION.to_string()
    } else {
        question.trim().to_string()
    };
    let existing: Option<i64> = sqlx::query_scalar(
        "select id from arenas where project_id = $1 and type = $2 and use_case = $3 order by id limit 1",
    )
    .bind(project_id)
    .bind(KIND)
    .bind(use_case)
    .fetch_optional(pool)
    .await?;
    let id = match existing {
        Some(id) => id,
        None => sqlx::query_scalar(
            "insert into arenas (type, project_id, feature_id, question, use_case, created_at)
             values ($1,$2,null,$3,$4,now()) returning id",
        )
        .bind(KIND)
        .bind(project_id)
        .bind(&question)
        .bind(use_case)
        .fetch_one(pool)
        .await?,
    };
    crate::arena::find(pool, id).await?.ok_or(Refused::NotFound)
}

/// Add a competing project to an alternatives arena.
///
/// The competitor gets the same prior as everything else in an arena ([`concord_ranking::DEFAULT_R`] and
/// [`concord_ranking::DEFAULT_RD`], sigma 0.06) and **no baseline flag**: see the module note.
///
/// # Errors
/// [`Refused::Invalid`] for the arena's own project or a non-positive id, [`Refused::WrongKind`] for an arena that does not rank
/// projects.
pub async fn add_competitor(pool: &PgPool, arena_id: i64, candidate: i64) -> Result<(), Refused> {
    let arena = crate::arena::find(pool, arena_id)
        .await?
        .ok_or(Refused::NotFound)?;
    if arena.entity_type != KIND && arena.entity_type != KIND_USE_CASE {
        return Err(Refused::WrongKind(format!(
            "an arena of type `{}` does not rank competing projects",
            arena.entity_type
        )));
    }
    if candidate <= 0 {
        return Err(Refused::Invalid("candidate project id required".to_string()));
    }
    if arena.project_id == candidate {
        return Err(Refused::Invalid(format!(
            "project {candidate} cannot compete against itself; an alternatives arena ranks other projects"
        )));
    }

    // The candidate must exist. Without this a typo'd id becomes an entry pointing at nothing, the leaderboard skips it, and the
    // arena looks like it ranked fewer projects than were added with nothing in the response to say why.
    if crate::projects::project_by_id(pool, candidate)
        .await?
        .is_none()
    {
        return Err(Refused::NotFound);
    }
    sqlx::query(
        "insert into arena_entries (arena_id, entity_type, entity_id, r, rd, sigma, updated_at)
         values ($1,'project',$2,$3,$4,0.06,now())
         on conflict (arena_id, entity_type, entity_id) do update set updated_at = excluded.updated_at",
    )
    .bind(arena_id)
    .bind(candidate)
    .bind(concord_ranking::DEFAULT_R)
    .bind(concord_ranking::DEFAULT_RD)
    .execute(pool)
    .await?;
    Ok(())
}
