//! §6.3's solutions: filing one, and the arena it lands in.
//!
//! A solution is not a row in a table with a rating next to it — filing one is **three** writes that have to agree:
//! the solution, the arena that ranks it, and its slot in that arena. If any of the three can happen without the others,
//! the feature's arena contains entries nobody filed or solutions nobody can rank, and the difference is invisible from
//! every read the product has.
//!
//! The rules, and why each one is a refusal rather than a note on the row:
//!
//! * **a feature's author cannot author its solutions** — §5.2 forbids voting on your own entry, so a solution its
//!   author is the only person who could sensibly judge is not a solution anybody else can judge;
//! * **`integrate-external` needs a catalog link** — the type MEANS "adopt the thing at this link", and without the link
//!   it is a solution nobody can evaluate;
//! * **a complementary solution is refused** — it is stackable, so it does not compete; comparing "do A, then B" against
//!   "do C" is not a question anyone can answer, and putting it in the rival arena makes the engine ask it;
//! * **the first `do-nothing` in an arena becomes the baseline, permanently** — §6.3, and it is what makes every other
//!   rating mean "better or worse than doing nothing" rather than "better than the average of whatever is here".
//!
//! A fork inherits its parent's rating **with doubled RD**, so a good idea is not forced to start from zero and is not
//! trusted as much as the thing it forked from.

use sqlx::PgPool;

/// The solution kinds §6.3 names.
pub const TYPE_BUILD_NEW: &str = "build-new";
/// Adopt something that already exists, at a catalog link.
pub const TYPE_INTEGRATE_EXTERNAL: &str = "integrate-external";
/// The permanent baseline: what happens if nobody does anything.
pub const TYPE_DO_NOTHING: &str = "do-nothing";

/// Solutions compete for the same slot.
pub const RELATIONSHIP_EXCLUSIVE: &str = "exclusive";
/// Stackable with its rivals, so not ranked against them.
pub const RELATIONSHIP_COMPLEMENTARY: &str = "complementary";

/// What a caller asked to file.
#[derive(Debug, Clone, Default)]
pub struct NewSolution {
    /// The solution's headline. Required.
    pub title: String,
    /// The argument. Optional.
    pub body: String,
    /// One of [`TYPE_BUILD_NEW`], [`TYPE_INTEGRATE_EXTERNAL`], [`TYPE_DO_NOTHING`]. Empty means `build-new`.
    pub kind: String,
    /// Required when `kind` is [`TYPE_INTEGRATE_EXTERNAL`].
    pub external_ref: String,
    /// Who the author is writing for, when that matters to the reader.
    pub affiliation: String,
    /// Empty or [`RELATIONSHIP_EXCLUSIVE`].
    pub relationship: String,
    /// The solution this one forks, if any.
    pub parent_solution_id: i64,
    /// Free-form tags the author claims expertise in.
    pub expertise_tags: Vec<String>,
}

/// Why a filing was refused.
#[derive(Debug)]
pub enum Refused {
    /// The body does not describe a solution that can be ranked.
    Invalid(String),
    /// The feature is not there, or not in this project.
    NotFound,
    /// §5.2: the feature's author cannot author its solutions.
    SelfVote,
    /// The fork's parent is not in this feature's arena.
    ParentNotInArena,
    /// The database said no.
    Database(sqlx::Error),
}

impl From<sqlx::Error> for Refused {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

/// File a solution and put it in its feature's arena.
///
/// # Errors
/// See [`Refused`]. The refusals are the §6.3 rules above; none of them is enforced only by the schema, because a
/// constraint violation is a 500 where the caller needed a sentence.
pub async fn file_solution(
    pool: &PgPool,
    project_id: i64,
    feature_id: i64,
    author_id: i64,
    new: NewSolution,
) -> Result<i64, Refused> {
    let title = new.title.trim();
    if title.is_empty() {
        return Err(Refused::Invalid("a solution needs a title".to_string()));
    }
    let kind = if new.kind.trim().is_empty() {
        TYPE_BUILD_NEW
    } else {
        new.kind.trim()
    };
    if !matches!(kind, TYPE_BUILD_NEW | TYPE_INTEGRATE_EXTERNAL | TYPE_DO_NOTHING) {
        return Err(Refused::Invalid(format!(
            "`{kind}` is not a solution type (§6.3 names build-new, integrate-external and do-nothing)"
        )));
    }
    let relationship = if new.relationship.trim().is_empty() {
        RELATIONSHIP_EXCLUSIVE
    } else {
        new.relationship.trim()
    };
    if relationship == RELATIONSHIP_COMPLEMENTARY {
        return Err(Refused::Invalid(
            "a complementary solution is not ranked against its rivals; it needs its own arena".to_string(),
        ));
    }
    if relationship != RELATIONSHIP_EXCLUSIVE {
        return Err(Refused::Invalid(format!(
            "`{relationship}` is not a relationship (§6.3 names exclusive and complementary)"
        )));
    }
    if kind == TYPE_INTEGRATE_EXTERNAL && new.external_ref.trim().is_empty() {
        return Err(Refused::Invalid(
            "an integrate-external solution needs a catalog link".to_string(),
        ));
    }

    let (feature_author, feature_project): (i64, i64) = sqlx::query_as(
        "select author_id, project_id from features where id = $1",
    )
    .bind(feature_id)
    .fetch_optional(pool)
    .await?
    .ok_or(Refused::NotFound)?;
    if feature_project != project_id {
        return Err(Refused::NotFound);
    }
    if feature_author == author_id {
        return Err(Refused::SelfVote);
    }
    // The parent is checked BEFORE the insert: `solutions.parent_solution_id` is a foreign key, so a bad parent that got
    // as far as the INSERT would surface as a 500 for a mistake the caller can fix in one field.
    if new.parent_solution_id != 0 {
        let sibling: Option<i64> = sqlx::query_scalar(
            "select id from solutions where id = $1 and feature_id = $2",
        )
        .bind(new.parent_solution_id)
        .bind(feature_id)
        .fetch_optional(pool)
        .await?;
        if sibling.is_none() {
            return Err(Refused::ParentNotInArena);
        }
    }

    let solution_id: i64 = sqlx::query_scalar(
        "insert into solutions (feature_id, author_id, title, body, type, external_ref, affiliation,
                                relationship, parent_solution_id, status, expertise_tags, created_at, updated_at)
         values ($1,$2,$3,$4,$5,nullif($6,''),nullif($7,''),$8,nullif($9,0),'draft',$10,now(),now())
         returning id",
    )
    .bind(feature_id)
    .bind(author_id)
    .bind(title)
    .bind(new.body.trim())
    .bind(kind)
    .bind(new.external_ref.trim())
    .bind(new.affiliation.trim())
    .bind(relationship)
    .bind(new.parent_solution_id)
    .bind(&new.expertise_tags)
    .fetch_one(pool)
    .await?;

    let arena_id = ensure_solution_arena(pool, project_id, feature_id).await?;
    // Defaults live in one place: `ranking`'s. A solution is ranked from the same starting point as every other
    // competitor in the estate, and a copy of the numbers here would be a second answer to "where does a new entry start?".
    sqlx::query(
        "insert into arena_entries (arena_id, entity_type, entity_id, r, rd, sigma, updated_at)
         values ($1,'solution',$2,$3,$4,0.06,now())
         on conflict (arena_id, entity_type, entity_id) do update set updated_at = excluded.updated_at",
    )
    .bind(arena_id)
    .bind(solution_id)
    .bind(concord_ranking::DEFAULT_R)
    .bind(concord_ranking::DEFAULT_RD)
    .execute(pool)
    .await?;

    if new.parent_solution_id != 0 {
        inherit_parent_rating(pool, arena_id, solution_id, new.parent_solution_id).await?;
    }
    if kind == TYPE_DO_NOTHING {
        set_baseline(pool, arena_id, solution_id).await?;
    }
    Ok(solution_id)
}

/// The feature's solution arena, created if this is the first solution.
///
/// A concurrent creator wins the race harmlessly: both callers asked for "the" arena of a feature, so the loser reads the
/// winner's row rather than failing. Without that, two people filing the first two solutions at the same moment is an
/// error in a product whose whole point is that several people can file at once.
async fn ensure_solution_arena(
    pool: &PgPool,
    project_id: i64,
    feature_id: i64,
) -> Result<i64, Refused> {
    if let Some(existing) = crate::arena::find_solution_arena(pool, feature_id).await? {
        return Ok(existing);
    }
    let inserted: Option<i64> = sqlx::query_scalar(
        "insert into arenas (type, project_id, feature_id, question, use_case, created_at)
         values ('solution',$1,$2,$3,null,now())
         on conflict do nothing
         returning id",
    )
    .bind(project_id)
    .bind(feature_id)
    .bind("Which solution should be adopted?")
    .fetch_optional(pool)
    .await?;
    match inserted {
        Some(id) => Ok(id),
        None => crate::arena::find_solution_arena(pool, feature_id)
            .await?
            .ok_or(Refused::NotFound),
    }
}

/// A fork starts near its parent with twice the uncertainty.
async fn inherit_parent_rating(
    pool: &PgPool,
    arena_id: i64,
    fork_id: i64,
    parent_id: i64,
) -> Result<(), Refused> {
    if fork_id == parent_id {
        return Err(Refused::Invalid(
            "a solution cannot fork itself".to_string(),
        ));
    }
    let parent: Option<f64> = sqlx::query_scalar(
        "select r from arena_entries where arena_id = $1 and entity_type = 'solution' and entity_id = $2",
    )
    .bind(arena_id)
    .bind(parent_id)
    .fetch_optional(pool)
    .await?;
    let parent_rating = parent.ok_or(Refused::ParentNotInArena)?;
    sqlx::query(
        // `parent_entry_id` is left NULL: this schema's `arena_entries` has a composite primary key and no surrogate
        // `id`, so there is nothing for it to point at. The fork relationship lives on `solutions.parent_solution_id`,
        // which is a real foreign key — one place that records it, and it is the place that can enforce it.
        "update arena_entries set r = $1, rd = rd * 2, updated_at = now()
         where arena_id = $2 and entity_type = 'solution' and entity_id = $3",
    )
    .bind(parent_rating)
    // Deliberately NOT clamped to `MAX_RD`: the reference doubles the parent's RD and leaves it, because a fork of an
    // entry nobody has judged is genuinely less known than the cap can express. Clamping here would look tidier and
    // would quietly give the fork more confidence than its parent — the opposite of what inflating the RD is for.
    .bind(arena_id)
    .bind(fork_id)
    .execute(pool)
    .await?;
    Ok(())
}

/// Clear the previous baseline and set this one: §6.3's permanent do-nothing.
async fn set_baseline(pool: &PgPool, arena_id: i64, solution_id: i64) -> Result<(), Refused> {
    sqlx::query("update arena_entries set is_baseline = false, updated_at = now() where arena_id = $1 and is_baseline")
        .bind(arena_id)
        .execute(pool)
        .await?;
    sqlx::query(
        "update arena_entries set is_baseline = true, updated_at = now()
         where arena_id = $1 and entity_type = 'solution' and entity_id = $2",
    )
    .bind(arena_id)
    .bind(solution_id)
    .execute(pool)
    .await?;
    Ok(())
}
